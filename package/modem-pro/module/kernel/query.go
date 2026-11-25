package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"modemd/module/cell"
	"modemd/module/common"
	"modemd/module/config"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ---- from: modem_query.go ----

// ---- from: modem_query.go ----
// QuerySignal 查询信号（统一接口，自动调用当前模组厂商指令）
// QuerySignal 查询信号（统一接口，自动调用当前模组厂商指令）

func (mod *Modem) QuerySignal() (SignalInfo, error) {
	// 展锐 SIPC（F50）：信号（含 NR）全部由 URC 日志在供 ——
	// `+CSQ` 与 `+CESQ` 都是主动上报（实测 URC 里两条都有，约 20 秒一条），
	// 所以这个接口**一条 AT 都不发**，直接返回跟随器维护的缓存。
	// ⚠️ 这是 HTTP 请求路径：绝不能“顺手多问一次”把秒级的往返挂上去。
	// 跟丢了（urcSignalStale）会自动落到下面的 AT 路径 —— 宁可慢，也不显示冻结值。
	if mu300FileBackedState(mod) {
		return mod.signalSnapshot(), nil
	}
	// 厂商 URC 日志（展锐 SIPC）已经在报 `+CSQ` 时，就不必每条轮询都去慢总线上
	// 问一遍 —— 只留 urcSignalPollInterval 的兜底（见 urclog.go）。
	// 其它模组 / 没有 URC 时 shouldPollSignal 恒为 true，行为与以前完全一致。
	if mod.shouldPollSignal(time.Now()) {
		if _, err := mod.SendAT(mod.cmdImpl.CommandCSQ()); err != nil {
			return mod.signalSnapshot(), err
		}
	}
	if config.Cfg.Modem.EnableNRSignal {
		// 用探测后的服务小区指令（NR 信号就藏在它的应答里）；
		// MT5700 这类模组可能是 +CPSI? / +QCSQ
		if cmd := mod.resolveVendorCmd(KindServing); cmd != "" {
			_, _ = mod.SendAT(cmd)
		}
		// 还没拿到 RSRP（服务小区行缺字段 / 固件不认已探测的那条）
		// → 退回 3GPP 标准 AT+CESQ。它覆盖面最广，不依赖厂商私有方言。
		if s := mod.signalSnapshot(); s.NRRSRP == 0 {
			_, _ = mod.SendAT("AT+CESQ")
		}
	}
	return mod.signalSnapshot(), nil
}

// QueryNetworkReg 查询网络注册状态
func (mod *Modem) QueryNetworkReg() (string, error) {
	// 展锐 SIPC（F50）：注册状态一直就在 URC 里上报（`+CREG`/`+CGREG`/`+CEREG`），
	// 没必要为了这个接口再去慢总线上问一遍 —— 一次往返秒级，接口就是这么卡住的。
	// 返回的仍然是**模组自己发的那一行原文**（Raw），只是从 URC 日志里拿的。
	if mu300FileBackedState(mod) {
		if s := mod.urcRegRaw(); s != "" {
			return s, nil
		}
	}
	resp, err := mod.SendAT(mod.cmdImpl.CommandNetworkReg())
	if err != nil {
		return "", err
	}
	return strings.Join(resp, "|"), nil
}

// QueryNetInfo 查询网络接入信息（AT+QNWINFO）
func (mod *Modem) QueryNetInfo() (string, error) {
	// ⚠️ 厂商明确声明"本机不提供"时指令是空串（如展锐 UMS9620：AT+QNWINFO 等
	// 全部回 +CME ERROR: 4）。空串直接返回，不要往 AT 通道发一条空指令 ——
	// 那会白占一个往返（共享慢总线上要 2 秒）。
	cmd := mod.cmdImpl.CommandNetInfo()
	if strings.TrimSpace(cmd) == "" {
		return "", nil
	}
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return "", err
	}
	return strings.Join(resp, "|"), nil
}

// ====================== 短信队列 ======================

// ---- from: modem_scan.go ----

// ScanCells 扫描基站：当前服务小区 + 附近基站（邻小区），并附带网络侧信息。
//
// 依次发送：
//
//	AT+QENG="servingcell"    当前服务小区（技术/频段/PCI/RSRP/RSRQ/SINR）
//	AT+QENG="neighbourcell"  附近基站（同频 intra / 异频 inter）
//	AT+QNWINFO               网络接入信息（技术/频段/运营商码）
//	AT+COPS?                 当前运营商名称
//	AT+CREG?                 网络注册状态
//
// 任一指令失败只记录日志，不影响其余结果，保证弱信号/未注册场景仍能返回已有数据。

// ScanCells 扫描基站：当前服务小区 + 附近基站（邻小区），并附带网络侧信息。
//
// 依次发送：
//
//	AT+QENG="servingcell"    当前服务小区（技术/频段/PCI/RSRP/RSRQ/SINR）
//	AT+QENG="neighbourcell"  附近基站（同频 intra / 异频 inter）
//	AT+QNWINFO               网络接入信息（技术/频段/运营商码）
//	AT+COPS?                 当前运营商名称
//	AT+CREG?                 网络注册状态
//
// 任一指令失败只记录日志，不影响其余结果，保证弱信号/未注册场景仍能返回已有数据。
func (mod *Modem) ScanCells() (*cell.CellScanResult, error) {
	res := &cell.CellScanResult{
		ModemID:    mod.ID,
		ScannedAt:  time.Now(),
		Serving:    []cell.CellInfo{},
		Neighbours: []cell.CellInfo{},
	}

	// 服务小区
	servingCmd := mod.resolveVendorCmd(KindServing)
	var servingResp []string
	var gtNeighbours []cell.CellInfo
	if servingCmd != "" {
		resp, err := mod.SendAT(servingCmd)
		if err == nil {
			servingResp = resp
			for _, line := range servingResp {
				if ci, ok := cell.ParseCellLine(line); ok && ci.Serving {
					res.Serving = append(res.Serving, ci)
					break
				}
			}
			// +QENG/+FENG 没解析出东西时再试 +CPSI 格式（部分模组/MT5700 只有这个写法）
			if len(res.Serving) == 0 {
				for _, line := range servingResp {
					if ci, ok := cell.ParseCPSICell(line); ok {
						res.Serving = append(res.Serving, ci)
						break
					}
				}
			}
			// 广和通 FM650（展锐平台）：AT+GTCCINFO? 一条指令同时给出服务小区与**全部邻区**，
			// 所以邻区也在这里一起收下（后面若探测到邻区指令就是它自己，就不用再发一遍）。
			if gs, gn := cell.ParseGTCCINFO(servingResp); len(gs)+len(gn) > 0 {
				res.Serving = append(res.Serving, gs...)
				gtNeighbours = gn
			}
			// 鼎桥 MT5700：单行 `^MONSC: NR,…`（RSRP/RSRQ/SINR 已经是 dB，不是索引）
			if len(res.Serving) == 0 {
				for _, line := range servingResp {
					if ci, ok := cell.ParseMONSC(line); ok {
						res.Serving = append(res.Serving, ci)
						break
					}
				}
			}
		} else {
			logger.Warn("query serving cell failed",
				zap.Int("modem", mod.ID), zap.Error(err))
		}
	}

	// 展锐 SIPC（F50）：本机**没有**服务小区指令（上一条 servingCmd 为空）,
	// 但 URC 日志里已经有服务小区 ⇒ 用它填上（零 AT）。
	// 扫描缓存优先：真扫到的小区不会被这份"少几个字段"的覆盖。
	if len(res.Serving) == 0 {
		res.Serving = append(res.Serving, mod.urcServingCells()...)
	}

	// 附近基站（邻小区），按信号强度降序，便于上层直接选优。
	// ⚠️ 邻区指令与服务小区指令相同时（FM650 的 AT+GTCCINFO? 就是这一种）
	// 不再重发 —— 服务小区那一步已经把邻区一起拿回来了。
	neighCmd := mod.resolveVendorCmd(KindNeighbour)
	var neighResp []string
	if neighCmd != "" && neighCmd != servingCmd {
		if resp, err := mod.SendAT(neighCmd); err == nil {
			neighResp = resp
		} else {
			logger.Warn("scan neighbour cells failed",
				zap.Int("modem", mod.ID), zap.Error(err))
		}
	}
	switch {
	case len(neighResp) > 0:
		// 邻区解析按"专有 → 通用"三层回落：
		//  ① 鼎桥 MT5700 的 `^MONNC: NR,…`（一行一个）；
		//  ② 厂商自有的邻区格式（openluat Air724UG 的 `+CCED:LTE neighbor cell:`，见 vendor_cell_extra.go）；
		//  ③ 通用小区行解析（QENG neighbourcell intra/inter）。
		if mn := cell.ParseMONNCLines(neighResp); len(mn) > 0 {
			res.Neighbours = mn
		} else if vn := cell.VendorNeighbourCells(neighResp); len(vn) > 0 {
			res.Neighbours = vn
		} else {
			res.Neighbours = cell.ParseCellLines(neighResp)
		}
	case len(gtNeighbours) > 0:
		res.Neighbours = gtNeighbours
	}
	if len(res.Neighbours) > 0 {
		// 邻小区不上报 PLMN，用服务小区补齐，便于展示运营商
		cell.FillNeighbourPLMN(res.Serving, res.Neighbours)
		// 邻区应答里没有同频/异频标志的固件（MT5700 的 ^MONNC、广和通 GTCCINFO）
		// 按"频点与服务小区相同"补上，否则界面上全被写成"异频邻区"
		cell.MarkIntraFrequency(res.Serving, res.Neighbours)
		cell.SortCellsBySignal(res.Neighbours)
	}

	// 网络接入信息 / 运营商 / 注册状态（均为可选项）
	// ⚠️ 不认 AT+QNWINFO 的固件（实测 FM650/FM350/SIM8200 都回 ERROR）：
	// 这一栏以前会原样显示 "AT+QNWINFO|ERROR"，现在改成填**服务小区指令的原始应答**
	// 并带上指令名（与「网络」卡片 applyServingNetInfo 的做法一致），
	// 这样界面上看到的是真实数据来源，而不是一条看不懂的 ERROR。
	if resp, err := mod.SendAT(mod.cmdImpl.CommandNetInfo()); err == nil && !common.RespHasError(resp) {
		res.NetInfo = strings.Join(resp, "|")
	} else if servingCmd != "" && len(servingResp) > 0 {
		res.NetInfo = servingCmd + "|" + strings.Join(servingResp, "|")
	}
	if resp, err := mod.SendAT(mod.cmdImpl.CommandOperator()); err == nil {
		res.Operator = cell.ParseOperator(resp)
	}
	if resp, err := mod.SendAT(mod.cmdImpl.CommandNetworkReg()); err == nil {
		res.NetworkReg = strings.Join(resp, "|")
	}
	if res.Operator == "" && len(res.Serving) > 0 {
		res.Operator = res.Serving[0].Operator
	}

	// 载波聚合（CA）：部分厂商/固件不支持该指令，失败只记日志，不影响扫描结果
	if cc, err := mod.QueryCAInfo(); err == nil {
		res.CA = cc
		res.CAActive = CCsAggregated(cc)
	} else {
		logger.Warn("query CA info failed", zap.Int("modem", mod.ID), zap.Error(err))
	} // 本机用的是哪条 CA 指令 + 空列表怎么解释（界面空状态提示用）
	res.CACmds, res.CANote = mod.caHint()
	res.Cells = append(append([]cell.CellInfo{}, res.Serving...), res.Neighbours...)
	res.Count = len(res.Cells)
	res.ServingCount = len(res.Serving)
	res.NeighbourCnt = len(res.Neighbours)

	// 缓存最近一次扫描结果，供 /api/modems 与 /api/cells?cache=1 复用
	mod.mu.Lock()
	mod.Cells = res.Cells
	mod.CellScanAt = res.ScannedAt
	mod.CAInfo = res.CA
	mod.mu.Unlock()

	logger.Info("scan cells done",
		zap.Int("modem", mod.ID),
		zap.Int("serving", res.ServingCount),
		zap.Int("neighbour", res.NeighbourCnt))
	// 扫描结果推给所有在线客户端（多开页面时一起更新）
	pushCells(mod, res)
	return res, nil
}

// ScanNeighbourCells 兼容旧接口：返回服务小区 + 附近基站列表。
func (mod *Modem) ScanNeighbourCells() ([]cell.CellInfo, error) {
	res, err := mod.ScanCells()
	if err != nil {
		return nil, err
	}
	return res.Cells, nil
}

// ---- from: modem_info.go ----

// ModemIdentity 模组硬件身份信息

type ModemIdentity struct {
	ModemID int         `json:"modem_id"`
	Vendor  ModemVendor `json:"vendor"`
	// VendorName 厂商可读名（界面直接显示它）
	VendorName string `json:"vendor_name,omitempty"`
	ATPort     string `json:"at_port"`
	// Model 是解析出的**型号短名**（与 /api/modems 的 model 字段一致）；
	// ModelRaw 是 ATI 原始应答（排查问题时看这个）。
	Model    string `json:"model,omitempty"`
	ModelRaw string `json:"model_raw,omitempty"`
	IMEI     string `json:"imei,omitempty"`
	Firmware string `json:"firmware,omitempty"`
}

// reIMEI 匹配 15 位 IMEI（部分模组会带引号或 "+CGSN: " 前缀）
var reIMEI = regexp.MustCompile(`(\d{15})`)

// parseIMEI 从 AT+CGSN 应答中提取 IMEI，跳过指令回显与 OK/ERROR
func parseIMEI(resp []string) string {
	for _, line := range resp {
		l := strings.TrimSpace(line)
		u := strings.ToUpper(l)
		if l == "" || u == "OK" || u == "ERROR" {
			continue
		}
		if strings.HasPrefix(u, "AT+") || strings.HasPrefix(u, "ATI") {
			continue // 指令回显
		}
		if m := reIMEI.FindStringSubmatch(l); m != nil {
			return m[1]
		}
	}
	return ""
}

// parseFirmware 从 AT+CGMR 应答中提取固件版本，跳过指令回显与 OK/ERROR
func parseFirmware(resp []string) string {
	for _, line := range resp {
		l := strings.TrimSpace(strings.Trim(strings.TrimSpace(line), `"`))
		u := strings.ToUpper(l)
		if l == "" || u == "OK" || u == "ERROR" || strings.HasPrefix(u, "AT+") {
			continue
		}
		l = strings.TrimSpace(strings.TrimPrefix(l, "+CGMR:"))
		return strings.Trim(strings.TrimSpace(l), `"`)
	}
	return ""
}

// shortModelName 从 ATI 原始应答里取出**型号短名**（界面「型号」一栏、{modem} 占位符都用它）。
//
// 各家 ATI 写法不同，实测过的四类：
//
//	Manufacturer: TD Tech Ltd.|Model: MT5700M-CN|Revision: …|IMEI: …|OK      → MT5700M-CN
//	Manufacturer: Fibocom Wireless Inc.|Model: FM160-EAU|Revision: …|OK       → FM160-EAU
//	ATI|Quectel|RM520N-GL|Revision: RM520NGLAAR01A07M4G|OK                    → RM520N-GL
//	SIMCOM_SIM8200EA-M2（或带 Revision 行）                                   → SIMCOM_SIM8200EA-M2
//
// 以前直接把整串 ATI 当型号显示，界面上一行里塞着厂商/固件/IMEI/能力表，完全没法看。
// 实在认不出时退回原串（截断到 40 字符），不编造。
func shortModelName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// ① 带标签的写法（鼎桥 / 广和通 / 部分芯讯通）：取 Model: 后的第一个字段
	if i := indexFold(raw, "Model:"); i >= 0 {
		s := raw[i+len("Model:"):]
		if j := strings.IndexAny(s, "|\r\n"); j >= 0 {
			s = s[:j]
		}
		if v := strings.TrimSpace(s); v != "" {
			return v
		}
	}
	// ② 逐行/逐段写法（移远）：ATI|Quectel|RM520N-GL|Revision: …|OK
	//    跳过回显与结束标记、带标签的段（Revision: …）、厂商标识段，
	//    取第一个“同时含字母和数字”的段当型号（RM520N-GL / EM05 / FM650-CN …）
	for _, seg := range strings.FieldsFunc(raw, func(r rune) bool { return r == '|' || r == '\r' || r == '\n' }) {
		seg = strings.TrimSpace(seg)
		if seg == "" || strings.Contains(seg, ":") {
			continue
		}
		switch strings.ToUpper(seg) {
		case "ATI", "AT", "OK", "ERROR", "QUECTEL", "FIBOCOM", "SIMCOM", "TD TECH":
			continue
		}
		if hasLetterAndDigit(seg) {
			return seg
		}
	}
	// ③ 逐行「标签: 值」的版本块（实测展锐 UMS9620 / 中兴 F50 的 AT+CGMR）：
	//
	//	Platform Version: MOCORTM_V2_22C_W25.39.4_Debug
	//	Project Version:   QogirN6Pro_PS
	//	BASE  Version:     5G_MODEM_V2_22C_W25.39.4
	//	HW Version:        ums9620_modem
	//	09-25-2025 06:25:47
	//
	// 这种串在 ① ② 都认不出，以前会一路掉到最后的"截断到 40 字符"，
	// 界面上「型号」一栏就显示成 `Platform Version: MOCORTM_V2_22C_W25.39.`（真机实测）。
	if v := labeledVersionValue(raw); v != "" {
		return v
	}
	if len(raw) > 40 {
		return raw[:40]
	}
	return raw
}

// labeledVersionValue 从「标签: 值」逐行的版本块里挑一个像型号的值。
//
// 优先级：Project / Model / Product（项目/机型，展锐的 `QogirN6Pro_PS`）
// > HW / Hardware（硬件，`ums9620_modem`）> 第一个带字母数字的值。
// 挑不出来返回 ""，调用方继续回落（不编造）。
//
// ⚠️ 只在 shortModelName 的 ① ② 都失败后才被调到，所以**不会**影响任何已经能
// 正确取名的厂商（那些串要么有 `Model:` 标签、要么有直接的型号段）。
func labeledVersionValue(raw string) string {
	type labelValue struct{ label, value string }
	var all []labelValue
	for _, line := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\r' || r == '\n' || r == '|'
	}) {
		// 分隔符可能是半角 ':' 或全角 '：'（固件两种都出现过），全角是 3 字节
		i := strings.IndexAny(line, ":：")
		if i <= 0 {
			continue
		}
		sep := len(":")
		if line[i] != ':' {
			sep = len("：")
		}
		label := strings.ToUpper(strings.TrimSpace(line[:i]))
		value := strings.Trim(strings.TrimSpace(line[i+sep:]), `"`)
		if label == "" || value == "" || !hasLetterAndDigit(value) {
			continue
		}
		all = append(all, labelValue{label, value})
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			for _, e := range all {
				if strings.Contains(e.label, k) {
					return e.value
				}
			}
		}
		return ""
	}
	if v := pick("PROJECT", "MODEL", "PRODUCT"); v != "" {
		return v
	}
	if v := pick("HW", "HARDWARE"); v != "" {
		return v
	}
	if len(all) > 0 {
		return all[0].value
	}
	return ""
}

// indexFold 大小写的 strings.Index（各家固件 "Model"/"MODEL"/"model" 都出现过）
func indexFold(s, sub string) int {
	return strings.Index(strings.ToUpper(s), strings.ToUpper(sub))
}

// hasLetterAndDigit 是否同时含字母与数字（用来区分型号与厂商名）
func hasLetterAndDigit(s string) bool {
	letter, digit := false, false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			letter = true
		}
	}
	return letter && digit
}

// QueryIdentity 查询模组身份信息（IMEI / 固件版本），并回写到 Modem 字段
func (mod *Modem) QueryIdentity() (*ModemIdentity, error) {
	mod.mu.Lock()
	info := &ModemIdentity{
		ModemID:    mod.ID,
		Vendor:     mod.Vendor,
		VendorName: VendorLabel(mod.Vendor),
		ATPort:     mod.ATPort,
		Model:      mod.ModelName,
		ModelRaw:   mod.Model,
	}
	mod.mu.Unlock()

	imei, firmware := "", ""
	if resp, err := mod.SendAT(mod.cmdImpl.CommandIMEI()); err == nil {
		imei = parseIMEI(resp)
	} else {
		logger.Warn("query imei failed", zap.Int("modem", mod.ID), zap.Error(err))
	}
	if resp, err := mod.SendAT(mod.cmdImpl.CommandFirmware()); err == nil {
		firmware = parseFirmware(resp)
	} else {
		logger.Warn("query firmware failed", zap.Int("modem", mod.ID), zap.Error(err))
	}
	info.IMEI, info.Firmware = imei, firmware

	mod.mu.Lock()
	if imei != "" {
		mod.IMEI = imei
	}
	if firmware != "" {
		mod.Firmware = firmware
	}
	mod.mu.Unlock()

	logger.Info("query identity done",
		zap.Int("modem", mod.ID), zap.String("imei", imei), zap.String("firmware", firmware))
	return info, nil
}

// ====================== 短信存储（AT+CPMS） ======================
