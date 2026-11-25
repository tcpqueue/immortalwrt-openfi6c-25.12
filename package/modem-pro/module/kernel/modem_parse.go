package kernel

import (
	"modemd/module/cell"
	"modemd/module/common"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ====================== 厂商指令候选探测 ======================
//
// 部分模组（如 MT5700）拿不到官方指令手册，或同型号不同固件差异很大：
// 同一功能可能有 +QENG / +CPSI / +QCSQ 等多套写法。这里在运行期按顺序试探，
// 把第一条能正常应答的指令记住，之后固定使用 —— 只探测一次，不会每轮都试。

// ---- from: modem_urc.go ----

// parseLine 解析 AT 返回：CSQ 信号、NR 信号、短信通知
func (mod *Modem) parseLine(line string) { mod.parseLineCtx(line, false) }

// parseLineURC 解析一条**厂商 URC 日志**里的行（见 urclog.go 的跟随器）。
//
// 与 parseLine 共用同一套解析，唯一的区别是 `+CESQ`：URC 是这种模组信号的
// **唯一**来源，必须允许它持续**覆盖刷新**；而 AT 应答路径下 CESQ 只是兜底
// （服务小区应答才是首选来源）⇒ 只补空。不区分来源的话，URC 供的第一条 CESQ
// 之后信号就永远不动了（2026-10-05 用户实测报的“信号都不动”就是这个）。
func (mod *Modem) parseLineURC(line string) { mod.parseLineCtx(line, true) }

// parseLineCtx 两个入口共用的实现；fromURC 见 parseLineURC 的说明。
func (mod *Modem) parseLineCtx(line string, fromURC bool) {
	// SIM 状态 URC（`+CPIN: SIM REMOVED` / `+CPIN: READY` …）：与已知状态不同就置 dirty，
	// 巡检很快去核实（不等一个轮询周期）—— 拔卡/插卡要秒级发现，见 simhotswap.go。
	// 先做一个极便宜的过滤：绝大多数行里连 "CPIN" 都没有。
	if strings.Contains(line, "CPIN") {
		mod.noteCPINLine(line)
	}
	// 广和通 FM650（展锐平台）AT+GTCCINFO? 是"标签行 + 数据行"成组上报：
	//
	//	+GTCCINFO: / NR service cell: / 1,9,460,11,CFA102,... / NR neighbor cell: / 2,9,,,,,...
	//
	// 数据行本身只是一串逗号分隔的数字，单独看根本认不出是哪家的小区 ——
	// 必须靠上一条标签行才知道制式与服务/邻区。所以这里维护一个小状态机。
	if cell.IsGTCCINFOHead(line) {
		mod.gtccRat, mod.gtccServing, mod.gtccRow = "", false, 0
		return
	}
	if rat, serving, ok := cell.ParseGTCCINFLabel(line); ok {
		mod.gtccRat, mod.gtccServing, mod.gtccRow = rat, serving, 0
		return
	}
	if mod.gtccRat != "" {
		if ci, ok := cell.ParseGTCCINFRow(mod.gtccRat, mod.gtccServing, mod.gtccRow, line); ok {
			mod.gtccRow++
			if ci.Serving {
				// 邻区行不带信号意义（和服务小区不是同一套参考），只取服务小区这一行
				if ci.RSRP != 0 {
					mod.mu.Lock()
					mod.SignalInfo.NRRSRP = ci.RSRP
					mod.SignalInfo.NRRSRQ = ci.RSRQ
					mod.SignalInfo.NRSINR = ci.SINR
					mod.mu.Unlock()
					logger.Info("update GTCCINFO signal",
						zap.Int("modem", mod.ID),
						zap.Int("rsrp", ci.RSRP), zap.Int("rsrq", ci.RSRQ), zap.Int("sinr", ci.SINR))
					pushSignalDebounced(mod)
				}
			}
			return
		}
		// 标签行之后不是数据行（ERROR / 下一条指令回显）→ 结束这一段，避免状态串到后面
		mod.gtccRat = ""
	}
	// 有的固件（实测 FM350-GL）**不输出标签行**，制式与服务/邻区都写在数据行里：
	//
	//	+GTCCINFO: / 1,9,460,11,CFA103,… / OK
	//
	// 这种行用 ParseGTCCINFRowAuto 自己认（认不出返回 false，不影响下面的解析）。
	if ci, ok := cell.ParseGTCCINFRowAuto(line); ok && ci.Serving && ci.RSRP != 0 {
		mod.mu.Lock()
		mod.SignalInfo.NRRSRP = ci.RSRP
		mod.SignalInfo.NRRSRQ = ci.RSRQ
		mod.SignalInfo.NRSINR = ci.SINR
		mod.mu.Unlock()
		logger.Info("update GTCCINFO signal (label-less)",
			zap.Int("modem", mod.ID),
			zap.Int("rsrp", ci.RSRP), zap.Int("rsrq", ci.RSRQ), zap.Int("sinr", ci.SINR))
		pushSignalDebounced(mod)
		return
	}

	// 鼎桥 MT5700：`^MONSC: NR,460,11,627264,1,CFA116002,115,CFA103,-78,-10,30`
	// 一行就带 RSRP/RSRQ/SINR，而且**直接是 dB**（不是索引）→ 用 non-zero 覆盖（它是最准的）。
	if ci, ok := cell.ParseMONSC(line); ok && ci.Serving {
		if ci.RSRP != 0 || ci.RSRQ != 0 || ci.SINR != 0 {
			mod.mu.Lock()
			if ci.RSRP != 0 {
				mod.SignalInfo.NRRSRP = ci.RSRP
			}
			if ci.RSRQ != 0 {
				mod.SignalInfo.NRRSRQ = ci.RSRQ
			}
			if ci.SINR != 0 {
				mod.SignalInfo.NRSINR = ci.SINR
			}
			mod.mu.Unlock()
			logger.Info("update MONSC signal",
				zap.Int("modem", mod.ID),
				zap.Int("rsrp", ci.RSRP), zap.Int("rsrq", ci.RSRQ), zap.Int("sinr", ci.SINR))
			pushSignalDebounced(mod)
		}
		return
	}
	// 鼎桥 MT5700 每秒主动上报 `^HCSQ: "NR",<rsrp 索引>,<sinr 索引>,<rsrq 索引>`。
	// 它的 rsrq/sinr 与 ^MONSC 的直接 dB 值不完全一致，所以**只在 MONSC 还没拿到值时空缺填补**。
	if cell.IsHCSQLine(line) {
		if rsrp, rsrq, sinr, ok := cell.ParseHCSQ(line); ok {
			mod.mu.Lock()
			changed := false
			if mod.SignalInfo.NRRSRP == 0 && rsrp != 0 {
				mod.SignalInfo.NRRSRP, changed = rsrp, true
			}
			if mod.SignalInfo.NRRSRQ == 0 && rsrq != 0 {
				mod.SignalInfo.NRRSRQ, changed = rsrq, true
			}
			if mod.SignalInfo.NRSINR == 0 && sinr != 0 {
				mod.SignalInfo.NRSINR, changed = sinr, true
			}
			mod.mu.Unlock()
			if changed {
				logger.Info("update HCSQ signal（兜底，MONSC 优先）",
					zap.Int("modem", mod.ID),
					zap.Int("rsrp", rsrp), zap.Int("rsrq", rsrq), zap.Int("sinr", sinr))
				pushSignalDebounced(mod)
			}
		}
		return
	}

	if m := reCSQ.FindStringSubmatch(line); m != nil {
		rssi, _ := strconv.Atoi(m[1])
		ber, _ := strconv.Atoi(m[2])
		// ⚠️ 展锐（实测 URC 里全是这种行）会发 `+CSQ: 255,99` = “测不出来”。
		// 255 不在任何量纲里，存进去会被当成有效值算成 +397 dBm 那种鬼东西，
		// 所以与 0/99 一样当**未上报**，不覆盖已有值（真信号在 `+CESQ` 里）。
		if rssi == 255 {
			return
		}
		mod.mu.Lock()
		mod.SignalInfo.RSSI = rssi
		mod.SignalInfo.BER = ber
		mod.mu.Unlock()
		logger.Info("update CSQ signal",
			zap.Int("modem", mod.ID), zap.Int("rssi", rssi), zap.Int("ber", ber))
		// 推给在线的 WebSocket 客户端（去抖：一次查询会连着回好几行）
		pushSignalDebounced(mod)
		return
	}
	// 使用当前模组厂商的解析函数
	rsrp, rsrq, sinr, ok := mod.cmdImpl.ParseNRSignal(line)
	if !ok {
		// 兜底一：厂商的 ParseNRSignal 有的只认真·NR 行 ——
		// 但**LTE 驻网**（纯 LTE 模组 / NSA 当下挂在 LTE 上）时 RSRP/RSRQ/SINR
		// 同样在 +QENG:"servingcell" 里，不兜这一下就是“信号全是空的”。
		rsrp, rsrq, sinr, ok = cell.ParseServingCellSignal(line)
	}
	if !ok {
		// 兜底二：Thales/Foxconn 的 `AT^DEBUG` 与 Sierra 的 `AT!GSTATUS?` 是“标签: 值”同行，
		// 按行抠信号即可（见 vendor_cell_extra.go 的 VendorServingSignal）。
		// ⚠️ 只取 RSRP/RSRQ/SINR 三格：RSSI 在本项目里存的是 AT+CSQ 的**索引**（0..31），
		// 把 dBm 塞进去会让界面/曲线量纲对不上（RSSI 另有 CSQ 那条路）。
		if r, q, si, _, vok := cell.VendorServingSignal(line); vok {
			rsrp, rsrq, sinr, ok = r, q, si, vok
		}
	}
	if ok {
		mod.mu.Lock()
		mod.SignalInfo.NRRSRP = rsrp
		mod.SignalInfo.NRRSRQ = rsrq
		mod.SignalInfo.NRSINR = sinr
		mod.mu.Unlock()
		logger.Info("update NR signal",
			zap.Int("modem", mod.ID),
			zap.Int("rsrp", rsrp), zap.Int("rsrq", rsrq), zap.Int("sinr", sinr))
		pushSignalDebounced(mod)
		return
	}
	if reCMTI.MatchString(line) {
		logger.Info("new sms notify", zap.Int("modem", mod.ID), zap.String("raw", line))
		// 角标先 +1（立刻可见，不必等下面那几百毫秒的读取）
		mod.noteSMSArrival()
		// ReadSMS 内部会把新列表推给所有在线客户端
		go mod.ReadSMS()
		return
	}
	// +CESQ：3GPP 标准信号上报（rxlev,ber,rscp,ecno,rsrq,rsrp）。
	// 有些模组（实测 RM500U-CNV）不认 +QCSQ/+CPSI，而 CESQ 一直可用；
	// 把它当**最后一道兜底**：只有 RSRP 还没到手时才拿它填，不覆盖更精确的来源。
	// 广和通（FM350）的 CESQ 多出第 7~9 格 NR 扩展（ss_rsrq/ss_rsrp/ss_sinr），
	// 驻留在 NR 上时前 6 格的 rsrq/rsrp 是 255，只能靠扩展那三格 —— 一并收下。
	// ⚠️ 行来自 URC 日志时（fromURC）改成**覆盖式刷新**，见 parseLineURC。
	if strings.Contains(line, "+CESQ:") {
		mod.applyCESQLine(line, fromURC)
		return
	}
	// 展锐固件在 CS 类业务（USSD / 呼叫）失败时会主动上报 `+SPERROR: <a>,<b>,"<plmn>"`：
	// 实测 USSD 被网络拒的序列是 `+SPERROR: 5,41,"46011"` → `+CUSD: 2`（无正文）。
	// 只记不推：它的价值是给紧随其后的 USSD 结果当"哪一侧拒的"线索（见 recordUSSD）。
	if strings.Contains(line, "+SPERROR:") {
		mod.noteSPError(line)
		return
	}
	// USSD 应答是异步 URC（发 AT+CUSD 只回 OK），必须在这里收下并推给页面
	if strings.Contains(line, "+CUSD:") {
		if res, ok := ParseCUSD(line, time.Now()); ok {
			mod.recordUSSD(res)
		}
	}
}

// applyCESQLine 用一条 `+CESQ:` 更新 NR 信号（rsrp/rsrq/sinr）。
//
// ⚠️ force 决定“覆盖还是只补空”，两种来源的语义不同：
//   - force=true（行来自厂商 URC 日志）：**覆盖式刷新**。URC 是这种模组信号的唯一来源，
//     只补空的话第一条之后就再也不动（2026-10-05 用户实测报的“信号都不动”就是这个）；
//   - force=false（行是 AT 应答）：只补空。服务小区应答才是首选来源，
//     CESQ 只是“拿不到更精确值时的最后一道兜底”，不该反过来把它覆盖掉。
//
// 值为 0 一律当“本次未上报”，不写（否则会把好值冲成 0）。
func (mod *Modem) applyCESQLine(line string, force bool) {
	rsrp, rsrq, sinr, ok := cell.ParseCESQSignal([]string{line})
	if !ok {
		return
	}
	mod.mu.Lock()
	changed := false
	upd := func(dst *int, v int) {
		if v == 0 {
			return
		}
		if !force && *dst != 0 {
			return // 已有值，且本行只是兜底来源
		}
		if *dst != v {
			*dst = v
			changed = true
		}
	}
	upd(&mod.SignalInfo.NRRSRP, rsrp)
	upd(&mod.SignalInfo.NRRSRQ, rsrq)
	upd(&mod.SignalInfo.NRSINR, sinr)
	mod.mu.Unlock()
	if !changed {
		return
	}
	logger.Info("update CESQ signal",
		zap.Int("modem", mod.ID),
		zap.Int("rsrp", rsrp), zap.Int("rsrq", rsrq), zap.Int("sinr", sinr),
		zap.Bool("from_urc", force))
	pushSignalDebounced(mod)
}

// ---- from: modem_vendorcmd.go ----

// respHasError 判断 AT 应答里是否带 ERROR。
// 注意：sendATLocked 只在**超时**时返回 error，模组回 ERROR 时 err 为 nil，
// 所以判断“这条指令不被支持”必须看应答内容。

// respHasError 已搬到 modemd/module/common（RespHasError）
// —— 厂商层（module/lock 等）也要用它判断"指令不被支持"。
// kernel 保留同名别名，见 atutil_alias.go。

// vendorCandidates 返回某功能的候选指令列表。
// 未实现 CommandCandidates 的厂商（广和通/芯讯通）就是单条指令。
//
// 机型特例（`Quirk.ServingCellCmd` / `NeighbourCellCmd`）**排在最前面判定**：
// 这类型号（如 openluat Air724UG 用 AT+CCED）连候选表都不一样，
// 必须优先于厂商/自适应的候选（否则会被自适应的 QENG/CPSI 抢走）。
func (mod *Modem) vendorCandidates(kind string) []string {
	if c := mod.quirkCandidates(kind); len(c) > 0 {
		return common.DedupeStrings(append(append([]string{}, c...), mod.rawVendorCandidates(kind)...))
	}
	return mod.rawVendorCandidates(kind)
}

// quirkCandidates 机型特例给的服务小区/邻区指令（没有则空）
//
// ⚠️ 会取 mod.mu（见 Quirk()），**不要在已持锁的地方调**。
func (mod *Modem) quirkCandidates(kind string) []string {
	switch kind {
	case KindServing:
		return mod.Quirk().ServingCellCmds
	case KindNeighbour:
		return mod.Quirk().NeighbourCellCmds
	}
	return nil
}

// rawVendorCandidates 厂商指令集/自适应给的候选（不含机型特例）
func (mod *Modem) rawVendorCandidates(kind string) []string {
	if cc, ok := mod.cmdImpl.(CommandCandidates); ok {
		switch kind {
		case KindServing:
			if c := cc.ServingCellCandidates(); len(c) > 0 {
				return c
			}
		case KindNeighbour:
			if c := cc.NeighbourCellCandidates(); len(c) > 0 {
				return c
			}
		case KindTraffic:
			if c := cc.TrafficCandidates(); len(c) > 0 {
				return c
			}
		}
	}
	switch kind {
	case KindServing:
		return []string{mod.cmdImpl.CommandServingCell()}
	case KindNeighbour:
		return []string{mod.cmdImpl.CommandNeighbourCell()}
	case KindAPN:
		// APN 是 3GPP 标准指令，所有厂商通用：
		// 优先查"当前激活"的 PDP 上下文（带本机 IP），再退回"已配置"的列表
		//
		// ⚠️ 个别固件**只认带 cid 的写法**（实测展锐 UMS9620：不带 cid 回
		// `+CME ERROR: 65536014`，`AT+CGCONTRDP=1` 才给完整一行）。
		// 这类差异由厂商自己声明（见 APNCandidatesProvider），未声明的厂商行为不变。
		if p, ok := mod.cmdImpl.(APNCandidatesProvider); ok {
			if c := p.APNCandidates(); len(c) > 0 {
				return c
			}
		}
		return []string{common.CmdCGCONTRDP, "AT+CGDCONT?"}
	case KindPDPAddr:
		// 本机 IPv4/IPv6 地址也是 3GPP 标准指令（实测 RM502Q-GL 支持）。
		// 个别固件不认"不带 cid"的写法（实测 FM350-GL 的 AT+CGPADDR 直接回 ERROR，
		// 而 AT+CGPADDR=2 正常）——这种差异按机型登记在 quirks 里，
		// 只是插到候选最前面，失败还会退回标准写法，别的机型不受影响。
		if c := mod.Quirk().PDPAddrCmd; c != "" {
			return []string{c, "AT+CGPADDR"}
		}
		return []string{"AT+CGPADDR"}
	}
	// 流量计数器是厂商私有指令：未声明的厂商不发（宁可不报，也不发未验证指令）
	return nil
}

// resolveVendorCmd 找出该功能实际可用的指令并缓存（同一功能只探测一次）。
// 全部候选都失败时退回第一条，保证后续调用行为稳定（不会每轮重试一遍）。
func (mod *Modem) resolveVendorCmd(kind string) string {
	mod.mu.Lock()
	if c, ok := mod.probedCmds[kind]; ok {
		mod.mu.Unlock()
		return c
	}
	mod.mu.Unlock()

	cands := mod.vendorCandidates(kind)
	if len(cands) == 0 {
		return ""
	}
	pick := cands[0]
	accepted := false
	for i, c := range cands {
		if c == "" {
			continue
		}
		resp, err := mod.SendAT(c)
		if err != nil {
			logger.Debug("vendor command candidate timeout",
				zap.Int("modem", mod.ID), zap.String("kind", kind), zap.String("cmd", c))
			continue
		}
		if common.RespHasError(resp) {
			logger.Debug("vendor command candidate rejected",
				zap.Int("modem", mod.ID), zap.String("kind", kind), zap.String("cmd", c))
			continue
		}
		accepted, pick = true, c
		if i > 0 {
			logger.Info("vendor command fallback in use",
				zap.Int("modem", mod.ID), zap.String("kind", kind), zap.String("cmd", c))
		}
		break
	}
	// 流量计数器属于"有更好、没有也不影响"的可选能力：
	// 全部候选都被拒绝时直接停用，不退回首选（否则每轮都白发一条无效指令）
	if !accepted && kind == KindTraffic {
		pick = ""
	}

	mod.mu.Lock()
	if mod.probedCmds == nil {
		mod.probedCmds = make(map[string]string, 2)
	}
	mod.probedCmds[kind] = pick
	mod.mu.Unlock()
	// 记一条，方便在实机上核对未验证型号到底命中了哪条指令
	logger.Info("vendor command resolved",
		zap.Int("modem", mod.ID), zap.String("kind", kind), zap.String("cmd", pick))
	return pick
}

// vendorCmdSnapshot 返回已探测出的厂商指令（加锁拷贝，供 API 展示）
func (mod *Modem) vendorCmdSnapshot() map[string]string {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	out := make(map[string]string, len(mod.probedCmds))
	for k, v := range mod.probedCmds {
		out[k] = v
	}
	return out
}
