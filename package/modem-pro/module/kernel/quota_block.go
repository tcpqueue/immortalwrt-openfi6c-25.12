package kernel

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"modemd/module/tdtech"

	"go.uber.org/zap"
)

// ====================== 断网 / 恢复 ======================

// ---- from: modem_quota_block.go ----

// quotaActiveCIDs 读当前处于激活态的 PDP 上下文

// quotaActiveCIDs 读当前处于激活态的 PDP 上下文
func (mod *Modem) quotaActiveCIDs() []int {
	if mod.usesTDTechNDIS() {
		cids, err := mod.activeTDTechNDISCIDs()
		if err != nil {
			logger.Debug("query NDIS status failed", zap.Int("modem", mod.ID), zap.Error(err))
		}
		return cids
	}
	resp, err := mod.SendAT("AT+CGACT?")
	if err != nil {
		logger.Debug("query CGACT failed", zap.Int("modem", mod.ID), zap.Error(err))
		return nil
	}
	var out []int
	for cid, on := range ParseCGACT(resp) {
		if on {
			out = append(out, cid)
		}
	}
	return out
}

// quotaDataCIDs 从 AT+CGDCONT? 里挑"能上网"的上下文（跳过 IMS/sos 这类内部 APN）
func (mod *Modem) quotaDataCIDs() []int {
	resp, err := mod.SendAT("AT+CGDCONT?")
	if err != nil {
		return nil
	}
	var out []int
	for _, ctx := range ParseCGDCONT(resp) {
		if ctx.Internal {
			continue
		}
		switch ctx.PDPType {
		case "IP", "IPV6", "IPV4V6":
			out = append(out, ctx.CID)
		}
	}
	return out
}

// internalCIDSet 哪些 cid 是内部上下文（IMS / sos 等）。
//
// ⚠️ 实测踩坑：`AT+CGACT?` 在这台模组上会同时报 cid 1（数据）与 cid 2（IMS）都激活，
// 而对 cid 2 发 `AT+CGACT=0,2` 直接被拒（ERROR）—— 内部上下文由模组自己管。
// 所以断网只能挑“上网用的”那些 cid；取不到 CGDCONT 时返回空集（退化成都试一遍，
// 但单条失败不再算整体失败）。
func (mod *Modem) internalCIDSet() map[int]bool {
	out := map[int]bool{}
	resp, err := mod.SendAT("AT+CGDCONT?")
	if err != nil {
		return out
	}
	seen := false
	for _, ctx := range ParseCGDCONT(resp) {
		seen = true
		if ctx.Internal {
			out[ctx.CID] = true
		}
	}
	if !seen {
		return map[int]bool{}
	}
	return out
}

// quotaBlocked 配额是不是正把这台模组的网络掐着（block = 停数据 / block_rf = 关射频）。
//
// 主机侧拨号器据此让路：配额断网期间它不该再被组网循环拉起来（见 hostDialStopReason），
// 不然"断网"只断得掉模组侧的 AT 上下文，数据呼叫照样被拨号器建起来。
func quotaBlocked(mod *Modem) bool {
	if mod == nil {
		return false
	}
	return quotaState(mod).Blocked
}

// SetDataBlocked 停用（block=true）/ 恢复（false）数据连接。
//
// 停用：把当前激活的**上网用** PDP 上下文逐个 AT+CGACT=0,<cid>，并回读确认；
// 内部上下文（IMS/sos）跳过。单条失败不算整体失败（只要至少断掉一条就算成），
// 但一条都没断成功则返回错误。
// 恢复：优先恢复“我们停用过的” cid（记在配额状态里，跨重启也认得），
// 没有记录时退回 AT+CGDCONT? 里第一个非内部上下文。
//
// ⚠️ QMI（主机侧拨号）例外：数据面在主机侧拨号器手里，AT 侧**没有**我们激活的上下文 ——
// 断网只能**停掉拨号器**（模组侧 AT+CGACT 既断不掉，恢复时的 AT+CGACT=1 还会反把承载占住，
// 拨号器连同 IPv6 就再也起不来）。恢复交给组网循环：状态不再是 Blocked 后它会重新拉起。
func (mod *Modem) SetDataBlocked(block bool) error {
	if modemUsesHostDialer(mod) {
		if block {
			if note := stopHostDial(mod.ID, "配额超量：停掉主机侧拨号器（数据呼叫归它）"); note != "" {
				logger.Warn("quota exceeded: host dialer stopped",
					zap.Int("modem", mod.ID), zap.String("note", note))
			}
		}
		return nil
	}
	if mod.usesTDTechNDIS() {
		return mod.setTDTechNDISBlocked(block)
	}
	if block {
		internal := mod.internalCIDSet()
		var done, failed []int
		for _, cid := range mod.quotaActiveCIDs() {
			if internal[cid] {
				continue // IMS/sos 上下文：模组自己管，发了也是 ERROR
			}
			cmd := fmt.Sprintf("AT+CGACT=0,%d", cid)
			resp, err := mod.SendAT(cmd)
			if err != nil {
				failed = append(failed, cid)
				continue
			}
			if msg := atError(resp); msg != "" {
				failed = append(failed, cid)
				logger.Debug("quota: deactivate cid rejected",
					zap.Int("modem", mod.ID), zap.Int("cid", cid), zap.String("err", msg))
				continue
			}
			done = append(done, cid)
		}
		if len(done) == 0 {
			if len(failed) > 0 {
				return fmt.Errorf("停用数据连接被拒（cid %v）", failed)
			}
			return nil // 本来就没有可断的数据连接
		}
		if len(failed) > 0 {
			logger.Warn("quota: some cids could not be deactivated",
				zap.Int("modem", mod.ID), zap.Ints("failed", failed), zap.Ints("ok", done))
		}
		// 回读确认（最多 3 秒）：模组回 OK 不代表真的断了
		deadline := time.Now().Add(3 * time.Second)
		for {
			still := mod.quotaActiveCIDs()
			left := false
			for _, cid := range done {
				for _, a := range still {
					if a == cid {
						left = true
					}
				}
			}
			if !left {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("停用数据连接后 cid %v 仍处于激活状态", done)
			}
			time.Sleep(300 * time.Millisecond)
		}
		quotaUpdate(mod, func(st *State) { st.BlockedCIDs = done })
		logger.Warn("quota exceeded: data connection deactivated",
			zap.Int("modem", mod.ID), zap.Ints("cids", done))
		return nil
	}

	cids := quotaState(mod).BlockedCIDs
	if len(cids) == 0 {
		cids = mod.quotaDataCIDs()
	}
	if len(cids) == 0 {
		return fmt.Errorf("找不到可恢复的数据上下文（cid）")
	}
	var errs []string
	for _, cid := range cids {
		cmd := fmt.Sprintf("AT+CGACT=1,%d", cid)
		resp, err := mod.SendAT(cmd)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if msg := atError(resp); msg != "" {
			errs = append(errs, cmd+": "+msg)
		}
	}
	quotaUpdate(mod, func(st *State) { st.BlockedCIDs = nil })
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	logger.Info("quota: data connection restored", zap.Int("modem", mod.ID), zap.Ints("cids", cids))
	return nil
}

// NDIS connections can only be released with NDISDUP (MT5700M-CN manual §16.1).
func (mod *Modem) activeTDTechNDISCIDs() ([]int, error) {
	states, err := mod.queryTDTechNDIS()
	if err != nil {
		return nil, err
	}
	cids := make([]int, 0, len(states))
	for cid, state := range states {
		if state != tdtech.NDISDisconnected {
			cids = append(cids, cid)
		}
	}
	slices.Sort(cids)
	return cids, nil
}

func (mod *Modem) setTDTechNDISBlocked(block bool) error {
	mod.ndisMu.Lock()
	defer mod.ndisMu.Unlock()
	cids := slices.Clone(quotaState(mod).BlockedCIDs)
	if !block {
		// Restore only sessions recorded when NDIS was blocked. If the user
		// explicitly disconnected, let the ordinary manual-stop policy stand.
		if len(cids) > 0 && !mod.DialStatus().ManualStop {
			states, err := mod.queryTDTechNDIS()
			if err != nil {
				return err
			}
			for _, cid := range cids {
				if !tdtech.ValidNDISCID(cid) || states[cid] == tdtech.NDISConnected {
					continue
				}
				if err := mod.changeTDTechNDIS(cid, true); err != nil {
					return err
				}
			}
		}
		quotaUpdate(mod, func(st *State) { st.BlockedCIDs = nil })
		return nil
	}

	active, err := mod.activeTDTechNDISCIDs()
	if err != nil {
		return err
	}
	internal := mod.internalCIDSet()
	var firstErr error
	for _, cid := range active {
		if internal[cid] {
			continue
		}
		if err := mod.changeTDTechNDIS(cid, false); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !slices.Contains(cids, cid) {
			cids = append(cids, cid)
		}
	}
	quotaUpdate(mod, func(st *State) { st.BlockedCIDs = cids })
	return firstErr
}

// setQuotaBlock 按动作类型执行/解除限制。
//
//	block    → 停用数据连接（只动数据面，AT 口/短信/注册不受影响，可恢复）：
//	           QMI 停主机侧拨号器，其它类型 AT+CGACT=0（见 SetDataBlocked）
//	block_rf → 关射频（AT+CFUN=4，“真不能联网”，但短信也一起停）
//
// ⚠️ block_rf 走的是飞行模式那套 AT+CFUN=4：机型不给开飞行模式的（F50，见
// AirplaneBlockedReason）这里会报错 —— 调用方（quotaEvaluate）只记日志不动状态，
// 所以旧配置留着 block_rf 也不会把用户的内网弄断；新配置在 API 那层就被挡住了。
func (mod *Modem) setQuotaBlock(on bool, action string) error {
	if action == ActionBlockRF {
		_, err := mod.SetAirplane(on)
		return err
	}
	return mod.SetDataBlocked(on)
}
