package kernel

import (
	"modemd/module/atparse"
	"modemd/module/cell"
	"regexp"
	"strconv"
	"strings"
)

// ====================== 供电电压（可选能力） ======================

// ---- from: netinfo/netinfo.go ----

// Package netinfo 是「网络类型 / 注册状态」的纯解析与文案部分。
//
// 概览页的「网络」卡片需要的信息分散在几条只读指令里，且都是裸串，
// 直接展示对人不友好（尤其 CREG 在 5G SA 下恒为 0,0，容易误判成没注册）：
//
//	AT+QNWINFO  → +QNWINFO: "TDD NR5G","46011","NR5G BAND 78",627264
//	AT+CREG?    → +CREG: 0,0                      电路域（CS）注册
//	AT+CGREG?   → +CGREG: 0,1                     分组域（PS）注册，数据业务看这个
//	AT+C5GREG?  → +C5GREG: 0,1                    5GS 注册（5G SA 下真正有用的那条）
//
// 这里只负责"裸串 → 结构体 / 中文"，不含任何 Modem、串口逻辑；
// 汇总成 NetworkStatus 的流程在 net.go（那边要发指令、查缓存）。
// RegState 一个域（CS/PS/5GS/EPS）的注册状态

type RegState struct {
	Stat       int    `json:"stat"`       // 0-5（3GPP TS 27.007）
	Text       string `json:"text"`       // 中文描述
	Registered bool   `json:"registered"` // stat 为 1 或 5
	Roaming    bool   `json:"roaming"`    // stat == 5
	LAC        string `json:"lac,omitempty"`
	CellID     string `json:"cell_id,omitempty"`
	ACT        int    `json:"act,omitempty"` // 接入技术编号（仅部分固件上报）
	Raw        string `json:"raw,omitempty"` // 原始应答，便于核对
}

var (
	// +QNWINFO: "TDD NR5G","46011","NR5G BAND 78",627264
	// 未注册时模组返回 `+QNWINFO: No Service`（无引号），下面的正则匹配不到，
	// 由调用方按"无服务"处理。
	//
	// ⚠️ **PLMN 不一定带引号**：实测 RM502Q-GL / RM500U-CNV 都是
	// `+QNWINFO: "FDD LTE",46011,"LTE BAND 3",1850`（PLMN 是裸数字）。
	// 早期正则要求 PLMN 也带引号 → 整条 QNWINFO 解析失败、只能退回服务小区，
	// 表现就是「网络」卡片里的**频段一直显示 `-`**（band_text 没赋值）。
	reQNWINFO = regexp.MustCompile(`\+QNWINFO:\s*"([^"]*)"\s*,\s*"?([0-9]*)"?\s*,\s*"([^"]*)"\s*,\s*(\d+)`)
	// +CREG: 0,2                （<n>,<stat>）
	// +CREG: 0,1,"1A2B","1234",7（<n>,<stat>,<lac>,<ci>,<AcT>）
	// +CEREG: 0,1 / +C5GREG: 0,1 —— 字段与 CREG 完全一致，共用一份解析
	reReg = regexp.MustCompile(`\+C(?:G|E|5G)?REG:\s*(\d+)\s*,\s*(\d+)(?:\s*,\s*"([^"]*)"\s*,\s*"([^"]*)"(?:\s*,\s*(\d+))?)?`)
)

// QNWInfo 一条 AT+QNWINFO 应答解析结果
type QNWInfo struct {
	NetTech  string // 模组原始制式，如 "TDD NR5G"
	PLMN     string // 46011（可能为空）
	BandText string // "NR5G BAND 78"
	EARFCN   int
}

// RegText 注册状态码 → 中文（3GPP TS 27.007 <stat>）
func RegText(stat int) string {
	switch stat {
	case 0:
		return "未注册，未在搜索"
	case 1:
		return "已注册，本地网"
	case 2:
		return "未注册，正在搜索"
	case 3:
		return "注册被拒绝"
	case 4:
		return "未知（模组未上报）"
	case 5:
		return "已注册，漫游中"
	}
	return "未知状态"
}

// ParseReg 解析 AT+CREG? / AT+CGREG? / AT+CEREG? / AT+C5GREG? 应答。
// 这几种指令字段完全相同，共用一份解析逻辑。
func ParseReg(resp []string) (RegState, bool) {
	for _, line := range resp {
		m := reReg.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		stat, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		rs := RegState{
			Stat:       stat,
			Text:       RegText(stat),
			Registered: stat == 1 || stat == 5,
			Roaming:    stat == 5,
			LAC:        m[3],
			CellID:     m[4],
			Raw:        strings.TrimSpace(line),
		}
		if m[5] != "" {
			rs.ACT, _ = strconv.Atoi(m[5])
		}
		return rs, true
	}
	return RegState{Text: "未获取"}, false
}

// SplitPLMN 把 "46011" 拆成 MCC="460" / MNC="11"。
// PLMN 的 MCC 固定 3 位，其余为 MNC（中国运营商均为 2 位，个别地区为 3 位，
// 这里统一按"前 3 位 + 余下"处理，不做国家特判以免猜错）。
func SplitPLMN(plmn string) (mcc, mnc string) {
	plmn = strings.TrimSpace(plmn)
	if len(plmn) < 5 || !atparse.IsAllDigits(plmn) {
		return "", ""
	}
	return plmn[:3], plmn[3:]
}

// TechLabel 把模组上报的制式串转成友好名称，例如：
//
//	"TDD NR5G" → "5G NR (TDD)"
//	"FDD LTE"  → "4G LTE (FDD)"
//	"GSM"      → "2G GSM"
//
// 注意模组上报的是 "TDD NR5G" 这类“双工+制式”组合串，
// 不能直接交给 NormalizeTech（它按整个 token 精确匹配）。
func TechLabel(netTech string) (CellTech, string) {
	up := strings.ToUpper(strings.TrimSpace(netTech))
	if up == "" {
		return "", ""
	}
	var tech CellTech
	var gen string
	switch {
	case strings.Contains(up, "NR5G"), strings.Contains(up, "NR"):
		tech, gen = CellTechNR, "5G NR"
	case strings.Contains(up, "LTE"):
		tech, gen = CellTechLTE, "4G LTE"
	case strings.Contains(up, "WCDMA"), strings.Contains(up, "UMTS"), strings.Contains(up, "HSPA"):
		tech, gen = CellTechWCDMA, "3G WCDMA"
	case strings.Contains(up, "GSM"), strings.Contains(up, "GPRS"), strings.Contains(up, "EDGE"):
		tech, gen = CellTechGSM, "2G GSM"
	default:
		tech, gen = CellTech(up), up
	}
	// 双工方式单独成 token，附在括号里
	if d := DuplexOf(up); d != "" {
		return tech, gen + " (" + d + ")"
	}
	return tech, gen
}

// TechLabelFromCellTech 由归一化制式生成展示名（QNWINFO 不可用时的兜底）。
func TechLabelFromCellTech(t CellTech) string {
	switch t {
	case CellTechNR:
		return "5G NR"
	case CellTechLTE:
		return "4G LTE"
	case CellTechWCDMA:
		return "3G WCDMA"
	case CellTechGSM:
		return "2G GSM"
	}
	return "未知制式"
}

// DuplexOf 从制式串里取出双工方式（TDD / FDD / HD-FDD）
func DuplexOf(netTech string) string {
	up := strings.ToUpper(netTech)
	for _, d := range []string{"TDD", "FDD", "HD-FDD"} {
		if strings.Contains(up, d) {
			return d
		}
	}
	return ""
}

// ParseQNWINFO 解析 AT+QNWINFO 应答。
// 未注册时模组返回 `+QNWINFO: No Service`，此时 ok=false。
func ParseQNWINFO(resp []string) (QNWInfo, bool) {
	for _, line := range resp {
		m := reQNWINFO.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		earfcn, _ := strconv.Atoi(m[4])
		return QNWInfo{NetTech: m[1], PLMN: m[2], BandText: m[3], EARFCN: earfcn}, true
	}
	return QNWInfo{}, false
}

// BandTextFor 由制式 + 频段号拼出模组那种频段串（"NR5G BAND 78" / "LTE BAND 5"）。
// 用于 QNWINFO 不可用的模组：界面「频段」一栏读的是 band_text，不补这一下就一直是 `-`。
func BandTextFor(tech CellTech, band int) string {
	if band <= 0 {
		return ""
	}
	if tech == CellTechNR {
		return "NR5G BAND " + strconv.Itoa(band)
	}
	return "LTE BAND " + strconv.Itoa(band)
}

// FirstServingCell 从一段“服务小区应答”里取出第一条服务小区。
// 四套写法都认：`+QENG`/`+FENG`（移远 / 广和通老机型）、`+CPSI`（芯讯通 / MTK 系）、
// `+GTCCINFO`（广和通 FM650 的“标签行 + 数据行”）、`^MONSC`（鼎桥 MT5700）。
func FirstServingCell(resp []string) (cell.CellInfo, bool) {
	for _, line := range resp {
		if ci, ok := cell.ParseCellLine(line); ok && ci.Serving {
			return ci, true
		}
		if ci, ok := cell.ParseCPSICell(line); ok {
			return ci, true
		}
		if ci, ok := cell.ParseMONSC(line); ok {
			return ci, true
		}
	}
	if serving, _ := cell.ParseGTCCINFO(resp); len(serving) > 0 {
		return serving[0], true
	}
	return cell.CellInfo{}, false
}

// ---- from: device/device.go ----

// Package device 是「模组设备模式」相关的纯逻辑：功能模式（AT+CFUN / 飞行模式）、
// USB 网卡模式（AT+QCFG="usbnet"）的取值表与应答解析，外加两个小工具。
//
// 为什么放一起：这两项都是"改模组自身工作方式"而不是"查网络/查卡"的参数，
// 共用一个包就不必为十几行解析各开一个目录；发指令与回读确认留在 kernel
// （见 net.go、net.go），因为要碰串口。
//
// 飞行模式要点（3GPP TS 27.007 的 AT+CFUN=<fun>）：
//
//	0 = minimum functionality         最小功能（射频关）
//	1 = full functionality            全功能（正常注网）
//	4 = disable TX and RX RF circuits 收发信机都关（飞行模式，实测固件用它）
//
// ⚠️ 恢复只用 AT+CFUN=1，**不要**用 AT+CFUN=1,1（那是"设置并复位"，会把模组整个重启掉，
// 与「重启模组」按钮是两件事）。
//
// USB 网卡模式要点：指"模块和主板/电脑之间"用哪种协议提供上网能力，移远/广和通等
// 工业 5G 模组通用指令：`AT+QCFG="usbnet"`（查询）/ `AT+QCFG="usbnet",<x>`（设置，需重启生效）。
// ⚠️ 模组自己不对 "usbnet" 提供 =? 枚举（实测 AT+QCFG=? 列表里没有这一项），
// 因此 Modes() 的取值表按移远文档列出常用项；模组上报表外的值时原样显示。

const (
	CFUNMinimum  = 0 // 最小功能（射频关）
	CFUNFull     = 1 // 全功能
	CFUNAirplane = 4 // 飞行模式（收发射频都关）
)

// AirplaneCommand 开关飞行模式的 AT 指令。
func AirplaneCommand(on bool) string {
	if on {
		return "AT+CFUN=" + strconv.Itoa(CFUNAirplane)
	}
	return "AT+CFUN=" + strconv.Itoa(CFUNFull)
}

// ParseCFUN 解析 AT+CFUN? 应答里的功能模式值。
// 形如 `+CFUN: 4`，也可能带复位标志 `+CFUN: 4,1`。
func ParseCFUN(resp []string) (int, bool) {
	for _, line := range resp {
		i := strings.Index(strings.ToUpper(line), "CFUN:")
		if i < 0 {
			continue
		}
		rest := line[i+len("CFUN:"):]
		fields := strings.FieldsFunc(rest, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\r'
		})
		if len(fields) == 0 {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(fields[0])); err == nil {
			return n, true
		}
	}
	return 0, false
}
