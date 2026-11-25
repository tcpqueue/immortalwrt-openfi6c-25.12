package kernel

import (
	"fmt"
	"go.uber.org/zap"
	"strings"
	"time"

	bandspkg "modemd/module/bands"
	"modemd/module/cell"
	"modemd/module/common"
	"modemd/module/platform"
)

// ====================== Modem 锁网操作 ======================

// errLockUnsupported 构造"厂商不支持锁网"的错误
func errLockUnsupported(vendor ModemVendor) error {
	return fmt.Errorf("模组厂商 %s 的固件不支持锁网指令（锁频段 / 锁基站）", vendor)
}

// ProbeLockCaps 探测模组实际支持哪些锁网指令（全部为只读查询，安全）
func (mod *Modem) ProbeLockCaps() (*LockCaps, error) {
	res := &LockCaps{ModemID: mod.ID, Vendor: mod.Vendor}
	l := mod.lockerFor()
	if l == nil {
		return res, nil
	}
	res.Supported = true
	res.Dialect, res.DialectName, res.DialectNote = mod.lockDialect()
	mod.mu.Lock()
	res.DialectTried = append([]string(nil), mod.lockTried...)
	mod.mu.Unlock()
	probes := []struct{ name, cmd string }{
		{"band_lock_caps", l.CommandBandLockCaps()},
		{"cell_lock_caps", l.CommandCellLockCaps()},
		{"mode_pref", l.CommandQueryModePref()},
		{"band_lte", l.CommandQueryBandLock(CellTechLTE)},
		{"band_nr", l.CommandQueryBandLock(CellTechNR)},
		{"policy_band", l.CommandQueryPolicyBand()},
		{"capability_band", l.CommandQueryBandCapability(CellTechLTE)},
		{"cell_lte", l.CommandQueryCellLock(CellTechLTE)},
		{"cell_nr", l.CommandQueryCellLock(CellTechNR)},
	}
	for _, p := range probes {
		if p.cmd == "" {
			continue
		}
		probe := LockCapProbe{Name: p.name, Command: p.cmd}
		if resp, err := mod.SendAT(p.cmd); err == nil {
			probe.OK = atError(resp) == ""
			probe.Response = resp
		} else {
			probe.Response = []string{err.Error()}
		}
		res.Probes = append(res.Probes, probe)
	}
	return res, nil
}

// EarfcnValid / ParseBandList / ParseLockTech 见 atcmd.go
// （同包，直接用）。

// ---- from: lock_query.go ----

// QueryLockStatus 查询锁频段 / 锁小区 / 组网模式状态（只读，安全）

// QueryLockStatus 查询锁频段 / 锁小区 / 组网模式状态（只读，安全）
func (mod *Modem) QueryLockStatus() (*bandspkg.LockStatus, error) {
	mod.lockQueryMu.Lock()
	defer mod.lockQueryMu.Unlock()
	return mod.queryLockStatus()
}

const lockStatusCacheTTL = 30 * time.Second

func (mod *Modem) lockStatusOrQuery(force bool) (*bandspkg.LockStatus, error) {
	mod.lockQueryMu.Lock()
	defer mod.lockQueryMu.Unlock()
	if !force {
		mod.mu.Lock()
		st := cloneLockStatus(mod.lockStatus)
		mod.mu.Unlock()
		if st != nil && !st.QueriedAt.IsZero() && time.Since(st.QueriedAt) <= lockStatusCacheTTL {
			return st, nil
		}
	}
	return mod.queryLockStatus()
}

// cloneLockStatus 深拷贝（含新增的 Bands；锁网状态会被缓存与推送，切片必须复制）
func cloneLockStatus(st *bandspkg.LockStatus) *bandspkg.LockStatus {
	if st == nil {
		return nil
	}
	copy := *st
	if st.Bands != nil {
		b := *st.Bands
		b.WCDMA = append([]int(nil), st.Bands.WCDMA...)
		b.LTE = append([]int(nil), st.Bands.LTE...)
		b.NRNSA = append([]int(nil), st.Bands.NRNSA...)
		b.NRSA = append([]int(nil), st.Bands.NRSA...)
		copy.Bands = &b
	}
	copy.BandLTE.Bands = append([]int(nil), st.BandLTE.Bands...)
	copy.BandLTE.Capable = append([]int(nil), st.BandLTE.Capable...)
	copy.BandLTE.Declared = append([]int(nil), st.BandLTE.Declared...)
	copy.BandNR.Bands = append([]int(nil), st.BandNR.Bands...)
	copy.BandNR.Capable = append([]int(nil), st.BandNR.Capable...)
	copy.BandNR.Declared = append([]int(nil), st.BandNR.Declared...)
	copy.BandNRNSA.Bands = append([]int(nil), st.BandNRNSA.Bands...)
	copy.BandNRNSA.Capable = append([]int(nil), st.BandNRNSA.Capable...)
	copy.BandNRNSA.Declared = append([]int(nil), st.BandNRNSA.Declared...)
	// Groups 里的切片也要各自复制：它会被缓存与 WebSocket 推送共享
	if st.Groups != nil {
		copy.Groups = make([]bandspkg.BandGroup, len(st.Groups))
		for i, g := range st.Groups {
			g.Bands = append([]int(nil), g.Bands...)
			g.Declared = append([]int(nil), g.Declared...)
			copy.Groups[i] = g
		}
	}
	if st.Cmds != nil {
		copy.Cmds = make(map[string]string, len(st.Cmds))
		for key, value := range st.Cmds {
			copy.Cmds[key] = value
		}
	}
	return &copy
}

func (mod *Modem) queryLockStatus() (*bandspkg.LockStatus, error) {
	l := mod.lockerFor()
	if l == nil {
		return nil, errLockUnsupported(mod.Vendor)
	}
	st := &bandspkg.LockStatus{
		ModemID:   mod.ID,
		QueriedAt: time.Now(),
		BandLTE:   bandspkg.BandLock{Tech: CellTechLTE},
		BandNR:    bandspkg.BandLock{Tech: CellTechNR},
		CellLTE:   CellLock{Tech: CellTechLTE},
		CellNR:    CellLock{Tech: CellTechNR},
	}
	if cmd := l.CommandQueryModePref(); cmd != "" {
		if resp, err := mod.SendAT(cmd); err == nil {
			if ops := lockVendorOpsOf(l); ops != nil {
				if v, ok := ops.ParseModePref(resp); ok {
					st.ModePref = v
				}
			} else if v, ok := parseQNWPREFCFG(resp, "mode_pref"); ok {
				st.ModePref = v
			}
		} else {
			logger.Warn("query mode_pref failed", zap.Int("modem", mod.ID), zap.Error(err))
		}
	}
	if ops := lockVendorOpsOf(l); ops != nil {
		st.Note = ops.StatusNote()
	}
	st.Dialect, st.DialectName, st.DialectNote = mod.lockDialect()
	st.Cmds = lockCmdSummary(l)
	caps := mod.queryBandCapabilities(l)
	st.BandLTE = mod.queryBandLock(l, CellTechLTE, caps[CellTechLTE])
	st.BandNR = mod.queryBandLock(l, CellTechNR, caps[CellTechNR])
	// NR 的 **NSA** 列表：只有"分两套"的方言才问 —— 一套的方言问出来的就是同一个值，
	// 界面却会多出一组镜像勾选框（用户 2026-10-06 报的"两边同时选上"就是这么来的）。
	st.SeparateNSA = nsaBandIndependent(l)
	if st.SeparateNSA {
		st.BandNRNSA = mod.queryBandLock(l, CellTechNRNSA, caps[CellTechNRNSA])
		bandspkg.ApplyDeclaredBands(&st.BandNRNSA, mod.Model+" "+mod.ModelName)
	}
	st.CellLTE = mod.queryCellLock(l, CellTechLTE)
	st.CellNR = mod.queryCellLock(l, CellTechNR)
	// 清单声明支持的频段（QModem 频段表）：出厂口径，与上面运行期读到的「设备能力」对照着看
	bandspkg.ApplyDeclaredBands(&st.BandLTE, mod.Model+" "+mod.ModelName)
	bandspkg.ApplyDeclaredBands(&st.BandNR, mod.Model+" "+mod.ModelName)
	// 四组原始数据（WCDMA / LTE / NR-NSA / NR-SA）：界面按它 + capable 自动分组排版
	if b, ok := bandspkg.BandsForModel(mod.Model + " " + mod.ModelName); ok {
		st.Bands = &b
	}
	// 锁频段页要的"组"（按本机清单 ∪ 设备上报自动排版）。
	// ⚠️ **必须在这里算好**：前端只读 JSON 的 `band_groups`，漏掉这一行界面上就永远是空的
	// （2026-10-06 真机踩过：字段与 `BandGroups()` 都写好了，但生产代码里没人调它）。
	st.Groups = st.BandGroups()

	logger.Info("query lock status",
		zap.Int("modem", mod.ID),
		zap.String("mode_pref", st.ModePref),
		zap.Int("band_groups", len(st.Groups)),
		zap.Bool("band_lte_locked", st.BandLTE.Locked),
		zap.Bool("band_nr_locked", st.BandNR.Locked),
		zap.Bool("cell_lte_locked", st.CellLTE.Locked),
		zap.Bool("cell_nr_locked", st.CellNR.Locked))
	mod.mu.Lock()
	mod.lockStatus = cloneLockStatus(st)
	mod.mu.Unlock()
	return st, nil
}

// queryBandLock 读取单个技术域的频段配置，并结合设备能力判断是否真的受限
func (mod *Modem) queryBandLock(l NetLocker, tech CellTech, capability []int) bandspkg.BandLock {
	bl := bandspkg.BandLock{Tech: tech, Capable: capability}
	cmd := l.CommandQueryBandLock(tech)
	if cmd == "" {
		// 该机型没有频段锁定查询（如广和通 MTK 只有一条 AT+GTACT，另有路径）
		return bl
	}
	resp, err := mod.SendAT(cmd)
	if err != nil {
		logger.Warn("query band lock failed",
			zap.Int("modem", mod.ID), zap.String("tech", string(tech)), zap.Error(err))
		return bl
	}
	if ops := lockVendorOpsOf(l); ops != nil {
		return mod.queryBandLockVendor(ops, tech, capability, resp)
	}
	if v, ok := parseQNWPREFCFG(resp, QnwBandKey(tech)); ok {
		bl.RawValue = v
		bl.Bands, _ = common.ParseBandList(v)
	}
	bl.Locked = bandLockRestricted(bl.Bands, bl.Capable)
	return bl
}

// queryBandLockVendor 厂商自己解析频段锁定（应答格式与移远完全不同，如鼎桥 MT5700）。
// capability 是上游查到的“设备能力频段”，必须带进来 —— 否则界面上那一列永远是 -。
func (mod *Modem) queryBandLockVendor(ops LockVendorOps, tech CellTech, capability []int, resp []string) bandspkg.BandLock {
	bands, locked := ops.ParseBandLockState(tech, resp)
	// 厂商只报“当前有哪些频段”、没给“是否受限”的结论时（如 Telit / 有方），
	// 用与移远那套相同的口径补判：当前值是设备能力表的真子集 = 确实锁了。
	// 拿不到能力表则保持 false（不臆断）。
	if !locked && len(bands) > 0 && len(capability) > 0 {
		locked = bandLockRestricted(bands, capability)
	}
	return bandspkg.BandLock{
		Tech:        tech,
		Capable:     capability,
		Bands:       bands,
		Locked:      locked,
		RawValue:    strings.Join(resp, "|"),
		CapableNote: ops.BandCapabilityNote(tech),
	}
}

// queryBandCapabilities 读取设备允许使用的频段（policy_band），作为是否受限的基准。
// 一次请求即可解析出 LTE 与 NR 两个技术域（应答含 lte_band / nr5g_band 多行）。
//
// 这里刻意不用 ue_capability_band：那是"当前配置 ∩ policy"，配置成 0 时它也是 0，
// 会把"频段被清空、模组脱网"误判成"未受限"。
func (mod *Modem) queryBandCapabilities(l NetLocker) map[CellTech][]int {
	// 厂商自己能查就直接用（如鼎桥 MT5700：AT^SYSCFGEX=? 的 LTE BC 名单）
	if ops := lockVendorOpsOf(l); ops != nil {
		if caps := ops.QueryBandCapability(mod); len(caps) > 0 {
			return caps
		}
	}
	out := make(map[CellTech][]int, 2)
	cmd := l.CommandQueryPolicyBand()
	if cmd == "" {
		return out
	}
	resp, err := mod.SendAT(cmd)
	if err != nil {
		logger.Warn("query policy band failed", zap.Int("modem", mod.ID), zap.Error(err))
		return out
	}
	for _, tech := range []CellTech{CellTechLTE, CellTechNR, CellTechNRNSA} {
		if v, ok := parseQNWPREFCFG(resp, QnwBandKey(tech)); ok {
			if bands, _ := common.ParseBandList(v); len(bands) > 0 {
				out[tech] = bands
			}
		}
	}
	return out
}

// queryCellLock 读取单个技术域的小区锁定状态
func (mod *Modem) queryCellLock(l NetLocker, tech CellTech) CellLock {
	cl := CellLock{Tech: tech}
	cmd := l.CommandQueryCellLock(tech)
	if cmd == "" {
		// 该机型没有锁小区指令（如广和通 MTK 平台）→ 视为未锁定，不下发任何指令
		return cl
	}
	resp, err := mod.SendAT(cmd)
	if err != nil {
		logger.Warn("query cell lock failed",
			zap.Int("modem", mod.ID), zap.String("tech", string(tech)), zap.Error(err))
		return cl
	}
	if ops := lockVendorOpsOf(l); ops != nil {
		return mod.queryCellLockVendor(ops, tech, resp)
	}
	if v, ok := parseQNWLOCK(resp, QnwLockKind(tech)); ok {
		v.Tech = tech
		cl = v
	}
	return cl
}

// queryCellLockVendor 厂商自己解析小区锁定（如鼎桥：锁频点/锁小区/锁频段共用一条指令）
func (mod *Modem) queryCellLockVendor(ops LockVendorOps, tech CellTech, resp []string) CellLock {
	cl, ok := ops.ParseCellLockState(tech, resp)
	if !ok {
		logger.Warn("parse cell lock state failed",
			zap.Int("modem", mod.ID), zap.String("tech", string(tech)))
		return CellLock{Tech: tech}
	}
	cl.Tech = tech
	return cl
}

// ---- from: lock_set.go ----

// ---- from: lock_set.go ----
// sendLockCommand 发送一条锁网写指令并检查模组是否接受
// sendLockCommand 发送一条锁网写指令并检查模组是否接受

func (mod *Modem) sendLockCommand(cmd string) error {
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return err
	}
	if e := atError(resp); e != "" {
		return fmt.Errorf("modem rejected %s: %s", cmd, e)
	}
	return nil
}

// unlockBand 解除某技术域的频段限制：把设备 policy 频段全量写回。
//
// 不能用 AT+QNWPREFCFG="lte_band",0 来解锁——实测该写法在本固件上会把频段
// 清空（而不是恢复全量），模组随即脱网，基站扫描于是永远返回空。详见
// QuectelCommand.CommandSetBandLock 的注释。
func (mod *Modem) unlockBand(l NetLocker, tech CellTech) error {
	if tech == CellTechNRNSA && !nsaBandIndependent(l) {
		return errNoSeparateNSA(mod.Vendor)
	}
	// 厂商自己提供了解锁指令（鼎桥：operatetype=0 一步到位，没有 policy 频段可写回）
	if ops := lockVendorOpsOf(l); ops != nil {
		if cmd := ops.CommandUnlockBand(tech); cmd != "" {
			return mod.sendLockCommand(cmd)
		}
	}
	// 需要"先回读再整串改写"的机型（广和通 MTK AT+GTACT）自己完成解锁
	if setter, ok := l.(BandLockSetter); ok {
		return setter.UnlockBandAt(mod, tech)
	}
	cmd := l.CommandQueryPolicyBand()
	if cmd == "" {
		return fmt.Errorf("vendor %s 不支持查询 policy 频段，无法安全解除 %s 频段限制", mod.Vendor, tech)
	}
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return err
	}
	v, ok := parseQNWPREFCFG(resp, QnwBandKey(tech))
	if !ok {
		return fmt.Errorf("policy 频段应答中缺少 %s", QnwBandKey(tech))
	}
	bands, _ := common.ParseBandList(v)
	if len(bands) == 0 {
		return fmt.Errorf("policy 频段为空（%s），拒绝写入 0 以免清空频段导致脱网", v)
	}
	logger.Info("restore band lock from policy",
		zap.Int("modem", mod.ID),
		zap.String("tech", string(tech)),
		zap.Ints("bands", bands))
	return mod.sendLockCommand(l.CommandSetBandLock(tech, bands))
}

// SetBandLock 锁定频段；bands 为空表示解除锁定（改用 policy 频段恢复，不会下发 0）。
// 返回写入后的最新锁网状态。
func (mod *Modem) SetBandLock(tech CellTech, bands []int) (*bandspkg.LockStatus, error) {
	l := mod.lockerFor()
	if l == nil {
		return nil, errLockUnsupported(mod.Vendor)
	}
	if tech != CellTechLTE && tech != CellTechNR && tech != CellTechNRNSA {
		return nil, fmt.Errorf("band lock only supports LTE/NR, got %q", tech)
	}
	if tech == CellTechNRNSA && !nsaBandIndependent(l) {
		return nil, errNoSeparateNSA(mod.Vendor)
	}
	if len(bands) == 0 {
		// 空列表 = 解除限制：用 policy 频段显式恢复。
		// 绝不能下发 0——那会把频段清空，模组直接脱网（基站扫描随即变空）。
		if err := mod.unlockBand(l, tech); err != nil {
			return nil, err
		}
		return mod.QueryLockStatus()
	}
	for _, b := range bands {
		if b < 1 || b > 261 {
			return nil, fmt.Errorf("invalid band %d (expect 1-261)", b)
		}
	}
	// 需要"先回读再整串改写"的机型（广和通 MTK AT+GTACT）自己完成下发；
	// 其余机型走单条 CommandSetBandLock。
	if setter, ok := l.(BandLockSetter); ok {
		if err := setter.SetBandLockAt(mod, tech, bands); err != nil {
			return nil, err
		}
	} else if cmd := l.CommandSetBandLock(tech, bands); cmd != "" {
		if err := mod.sendLockCommand(cmd); err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("vendor %s 未提供 %s 锁频段指令", mod.Vendor, tech)
	}
	logger.Info("band lock applied",
		zap.Int("modem", mod.ID),
		zap.String("tech", string(tech)),
		zap.Ints("bands", bands))
	return mod.QueryLockStatus()
}

// LockCell 锁定小区（基站）。
//
//	LockType=CellLockARFCNPCI  钉死频点+PCI（现场勘察最常用）
//	LockType=CellLockARFCN     只锁频点，该频点上的任意小区都可驻留
//	LockType=CellLockNone      解除锁定
//
// persist=true 时额外下发 AT+QNWLOCK="save_ctrl",1,1，使锁定在重启后仍生效。
//
// 注意：实测部分固件（RM502Q-GL 在 SA 组网下）会拒绝 NR（common/5g）小区锁定
// 并返回 +CME ERROR，此时如实返回模组错误并给出替代建议，不做静默降级。
func (mod *Modem) LockCell(req CellLockRequest, persist bool) (*bandspkg.LockStatus, error) {
	l := mod.lockerFor()
	if l == nil {
		return nil, errLockUnsupported(mod.Vendor)
	}
	if req.Tech != CellTechLTE && req.Tech != CellTechNR {
		return nil, fmt.Errorf("cell lock only supports LTE/NR, got %q", req.Tech)
	}
	switch req.LockType {
	case CellLockNone:
	case CellLockARFCN:
		if !EarfcnValid(req.Tech, req.ARFCN) {
			return nil, fmt.Errorf("invalid earfcn %d for %s", req.ARFCN, req.Tech)
		}
	case CellLockARFCNPCI:
		if !EarfcnValid(req.Tech, req.ARFCN) {
			return nil, fmt.Errorf("invalid earfcn %d for %s", req.ARFCN, req.Tech)
		}
		if req.PCID < 0 || req.PCID > 1007 {
			return nil, fmt.Errorf("invalid pcid %d (expect 0-1007)", req.PCID)
		}
		if req.Tech == CellTechNR && req.Band() == 0 {
			return nil, fmt.Errorf("cannot derive NR band from arfcn %d (lock needs band)", req.ARFCN)
		}
	default:
		return nil, fmt.Errorf("invalid lock_type %d (expect 0, 1 or 2)", req.LockType)
	}
	cmd := l.CommandLockCell(req)
	if cmd == "" {
		return nil, fmt.Errorf("该机型不支持这种锁定方式（%s / lock_type=%d）", req.Tech, req.LockType)
	}
	if err := mod.sendLockCommand(cmd); err != nil {
		if req.Tech == CellTechNR {
			return nil, fmt.Errorf("%w（部分固件在 SA 组网下不支持 NR 锁小区，可改用锁频段或锁组网模式）", err)
		}
		return nil, err
	}
	if persist {
		// 附加的持久化指令：厂商没声明时沿用移远的 AT+QNWLOCK="save_ctrl",1,1
		save := cmdQNWLockSave
		if ops := lockVendorOpsOf(l); ops != nil {
			save = ops.CommandSaveCellLock()
		}
		if save != "" {
			if err := mod.sendLockCommand(save); err != nil {
				logger.Warn("save cell lock failed", zap.Int("modem", mod.ID), zap.Error(err))
			}
		}
	}
	logger.Info("cell lock applied",
		zap.Int("modem", mod.ID),
		zap.String("tech", string(req.Tech)),
		zap.String("command", cmd),
		zap.Int("earfcn", req.ARFCN),
		zap.Int("pcid", req.PCID),
		zap.Bool("persist", persist))
	return mod.QueryLockStatus()
}

// UnlockAll 解除全部锁定（频段 + 小区），恢复模组默认行为。
// 频段部分先查 policy_band 再全量写回（不会下发 0，避免清空频段导致脱网）。
func (mod *Modem) UnlockAll() (*bandspkg.LockStatus, error) {
	l := mod.lockerFor()
	if l == nil {
		return nil, errLockUnsupported(mod.Vendor)
	}
	var firstErr error
	// 频段：用 policy 频段恢复。绝不能下发 0（会清空频段导致模组脱网）
	for _, tech := range []CellTech{CellTechLTE, CellTechNR} {
		if err := mod.unlockBand(l, tech); err != nil {
			logger.Warn("unlock band failed",
				zap.Int("modem", mod.ID), zap.String("tech", string(tech)), zap.Error(err))
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	cmds := []string{
		l.CommandLockCell(CellLockRequest{Tech: CellTechLTE, LockType: CellLockNone}),
		l.CommandLockCell(CellLockRequest{Tech: CellTechNR, LockType: CellLockNone}),
	}
	for _, cmd := range cmds {
		if cmd == "" {
			continue // 该机型没有对应指令（如 MTK 无锁小区），别发空指令
		}
		if err := mod.sendLockCommand(cmd); err != nil {
			logger.Warn("unlock failed", zap.Int("modem", mod.ID), zap.String("command", cmd), zap.Error(err))
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	logger.Info("unlock all applied", zap.Int("modem", mod.ID), zap.Bool("had_error", firstErr != nil))
	return mod.QueryLockStatus()
}

// ---- from: lock_mode.go ----

// QueryModePref 只查组网模式偏好（单条只读指令）。
// 用于「当前网络」卡片展示 + 修改后回显，避免为这一个字段跑整套 QueryLockStatus。

// QueryModePref 只查组网模式偏好（单条只读指令）。
// 用于「当前网络」卡片展示 + 修改后回显，避免为这一个字段跑整套 QueryLockStatus。
func (mod *Modem) QueryModePref() (string, error) {
	l := mod.lockerFor()
	if l == nil {
		return "", errLockUnsupported(mod.Vendor)
	}
	cmd := l.CommandQueryModePref()
	if cmd == "" {
		return "", fmt.Errorf("模组厂商 %s 未提供组网模式查询指令", mod.Vendor)
	}
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return "", err
	}
	if common.RespHasError(resp) {
		return "", fmt.Errorf("mode_pref query rejected")
	}
	if ops := lockVendorOpsOf(l); ops != nil {
		if v, ok := ops.ParseModePref(resp); ok {
			return v, nil
		}
	}
	v, _ := parseQNWPREFCFG(resp, "mode_pref")
	if v == "" {
		return "", fmt.Errorf("mode_pref not reported")
	}
	return v, nil
}

// SetModePref 设置组网模式偏好（AUTO / LTE / NR5G / LTE:NR5G）。
// 与锁频段/锁小区配合使用效果更好，例如只跑 LTE 时设为 LTE。
//
// ⚠️ 这是**锁网路径的兼容实现**：新代码请用 SetModePrefCaps（vendor_settings_engine.go）——
// 它按各家 mode 方言走，支持全部 7 个语义档位 + 未取证厂商的本机原码，界面上也是中文名。
// 本函数保留给旧接口 /api/lock/mode 与历史调用，档位仍限这 4 档。
//
// ⚠️ 模组是**异步生效**的（实测 RM502Q-GL）：写完立刻读 `AT+QNWPREFCFG="mode_pref"`
// 往往还是旧值，几秒后才变。以前直接回读会把旧值返回给前端，选择框随即“弹回去”，
// 看起来像设置失败 —— 所以这里做**带等待的回读确认**：一致才返回，否则明确告知。
func (mod *Modem) SetModePref(mode string) (*bandspkg.LockStatus, error) {
	l := mod.lockerFor()
	if l == nil {
		return nil, errLockUnsupported(mod.Vendor)
	}
	want := strings.ToUpper(strings.TrimSpace(mode))
	switch want {
	case "AUTO", "LTE", "NR5G", "LTE:NR5G":
	default:
		return nil, fmt.Errorf("invalid mode %q (expect AUTO/LTE/NR5G/LTE:NR5G)", mode)
	}
	// 有的固件不能"只写一个模式"：鼎桥 AT^SYSCFGEX 必须把 7 个必选参数整串下发
	// （手册 §13.2 注意事项 2：少一个就 +CME ERROR: 50），先回读再改写，交给厂商处理。
	if ops := lockVendorOpsOf(l); ops != nil {
		if handled, err := ops.SetModePrefVendor(mod, want); handled {
			if err != nil {
				return nil, err
			}
			return mod.confirmModePref(want)
		}
	}
	cmd := l.CommandSetModePref(mode)
	if cmd == "" {
		return nil, fmt.Errorf("模组厂商 %s 未提供组网模式设置指令", mod.Vendor)
	}
	if err := mod.sendLockCommand(cmd); err != nil {
		return nil, err
	}
	logger.Info("mode_pref applied", zap.Int("modem", mod.ID), zap.String("mode", mode))
	return mod.confirmModePref(want)
}

// confirmModePref 写完后带等待地回读确认（模组是异步生效的）。
//
// 实测 RM502Q-GL：写完立刻读往往还是旧值，几秒后才变。以前直接回读会把旧值返回给前端，
// 选择框随即"弹回去"，看起来像设置失败 —— 所以这里最多等约 3 秒；
// 没对上就把**实际值**返回给前端（让它显示真实状态，而不是让用户以为点没生效）。
func (mod *Modem) confirmModePref(want string) (*bandspkg.LockStatus, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if got, err := mod.QueryModePref(); err == nil && strings.EqualFold(got, want) {
			return mod.QueryLockStatus()
		}
		time.Sleep(time.Second)
	}
	st, err := mod.QueryLockStatus()
	if err != nil {
		return nil, err
	}
	logger.Warn("mode_pref read-back mismatch",
		zap.Int("modem", mod.ID), zap.String("want", want), zap.String("got", st.ModePref))
	return st, nil
}

// ---- from: ca.go ----

//
// 载波聚合下终端同时使用多个分量载波（CC）：一个主载波 PCC + 若干辅载波 SCC。
// **应答解析**在 cell.go（各厂商格式完全不同，都是纯解析）：
//
//	移远   AT+QCAINFO        → cellpkg.ParseQCAINFO
//	鼎桥   AT^CASCELLINFO?   → cellpkg.ParseCASCellInfo（LTE CA 的 SCell）
//	        AT^MONSSC         → cellpkg.ParseMONSSC（NSA 辅连接）
//
// 本文件是引擎侧：决定"用哪条指令查、怎么发、结果放哪"，以及给界面提供提示文案。
// CAVendorOps 等能力接口的定义在 atcmd.go（与厂商指令类型同包，
// 这样厂商实现的方法才能挂在类型上）。
// caVendorOf 取 CA 的厂商扩展；未实现返回 nil

func caVendorOf(cmd ModemCommand) CAVendorOps {
	ops, _ := cmd.(CAVendorOps)
	return ops
}

// caOpsForModel 机型特例的 CA 来源（不在厂商指令类型里、按型号挂的那一类）。
//
// 为什么需要：Telit 在 QModem 里的实现是 `vendor/telit.sh`（没有独立的 cmd 类型），
// 本项目里它走自适应指令集 ⇒ caVendorOf(mod.cmdImpl) 永远拿不到东西，
// 而它的 CA 与服务小区同一条 `AT#CAINFOEXT?`（见 atcmd.go 的 telitCAOps）。
// ⚠️ 只认型号，与厂商指令集互不干扰；以后加机型照 `isXxxModel` 的既有写法加。
func caOpsForModel(model string) CAVendorOps {
	if platform.IsTelitModel(model) {
		return telitCAOps{}
	}
	return nil
}

// caOpsFor 本机实际使用的 CA 扩展：先看厂商指令类型，再看机型特例。
//
// ⚠️ 与 usbDialectFor / modeDialectFor 一样直读 mod.Model（识别完成后不再变）。
func caOpsFor(mod *Modem) CAVendorOps {
	if mod == nil {
		return nil
	}
	if ops := caVendorOf(mod.cmdImpl); ops != nil {
		return ops
	}
	return caOpsForModel(mod.Model + " " + mod.ModelName)
}

// 编译期断言：引擎侧的 *Modem 满足指令层要的窄接口（ATDoer / LockATDoer），
// 所以厂商能力实现可以直接拿到 mod 发指令。
var (
	_ ATDoer     = (*Modem)(nil)
	_ LockATDoer = (*Modem)(nil)
)

// ModemID 实现 atcmd.ATDoer：指令层日志里区分是哪个模组
func (mod *Modem) ModemID() int { return mod.ID }

// ModemModel 型号串（ATI 原文 + 解析出的短名）—— 供指令层按平台/型号选写法
func (mod *Modem) ModemModel() string {
	return strings.TrimSpace(mod.Model + " " + mod.ModelName)
}

// SendLockCommand 实现 atcmd.LockATDoer：下发一条锁网指令
// （走 lock.go 的 sendLockCommand：串行化 + 去抖 + 回读确认）
func (mod *Modem) SendLockCommand(cmd string) error { return mod.sendLockCommand(cmd) }

// caHint 该机型的 CA 指令与空状态说明（给界面渲染用）。
// cmdImpl 为空时（测试里手搓的 Modem / 还没识别出厂商）安全返回空，不 panic。
func (mod *Modem) caHint() (cmds []string, note string) {
	if mod == nil || mod.cmdImpl == nil {
		return nil, ""
	}
	if ops := caOpsFor(mod); ops != nil {
		return ops.CACmds(), ops.CAEmptyNote()
	}
	if cmd := mod.cmdImpl.CommandCAInfo(); cmd != "" {
		return []string{cmd}, ""
	}
	return nil, ""
}

// CCsAggregated 判断分量载波列表里是否真的有辅载波（SCC），
// 即载波聚合是否真正生效。只有 PCC 时说明未聚合。
func CCsAggregated(ccs []CCInfo) bool {
	for _, cc := range ccs {
		if !strings.EqualFold(cc.Role, "PCC") {
			return true
		}
	}
	return false
}

// QueryCAInfo 查询载波聚合的分量载波列表（只读）。
//
// 声明了 CAVendorOps 的厂商自己发指令、自己解析（鼎桥 MT5700）；
// 其余厂商走 CommandCAInfo（移远 AT+QCAINFO）。
// 厂商未实现该指令时返回空列表且不报错 —— 与锁网的做法一致：
// 宁可不报，也不把未经验证的指令发给真实模组。
func (mod *Modem) QueryCAInfo() ([]CCInfo, error) {
	if mod == nil || mod.cmdImpl == nil {
		return nil, nil
	}
	if ops := caOpsFor(mod); ops != nil {
		return ops.QueryCA(mod)
	}
	cmd := mod.cmdImpl.CommandCAInfo()
	if cmd == "" {
		return nil, nil
	}
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return nil, err
	}
	return cell.ParseQCAINFO(resp), nil
}
