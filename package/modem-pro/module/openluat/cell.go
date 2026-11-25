package openluat

import (
	"math"
	"modemd/iface"
	"modemd/module/cell"
	"modemd/module/common"
	"strconv"
	"strings"
)

// CCEDLine 把一行 `+CCED:<标签>:<CSV>` 拆成（种类, 字段, ok）。
func CCEDLine(line string) (ccedKind, []string, bool) {
	s := strings.TrimSpace(line)
	if len(s) < len("+CCED") || !strings.HasPrefix(strings.ToUpper(s), "+CCED") {
		return ccedKind{}, nil, false
	}
	rest := strings.TrimSpace(s[len("+CCED"):])
	rest = strings.TrimSpace(strings.TrimPrefix(rest, ":"))
	up := strings.ToUpper(rest)
	for _, k := range CCEDKinds {
		if !strings.HasPrefix(up, k.label) {
			continue
		}
		tail := strings.TrimSpace(rest[len(k.label):])
		tail = strings.TrimSpace(strings.TrimPrefix(tail, ":"))
		if tail == "" {
			return ccedKind{}, nil, false
		}
		f := common.SplitATFields(tail)
		if len(f) != k.fields {
			return ccedKind{}, nil, false // 脚本口径：字段数不对就不认
		}
		return k, f, true
	}
	return ccedKind{}, nil, false
}

// openluatLTErsrp 把 `+CCED` 的 RSRP 原始读数换成 dBm（openluat_lte_rsrp_value / normalize_rsrp）。
func openluatLTErsrp(raw string) (int, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	if strings.HasPrefix(s, "-") {
		if n, err := strconv.Atoi(s); err == nil && n >= -141 && n <= -44 {
			return n, true
		}
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 97 {
		return 0, false
	}
	return n - 141, true
}

// openluatLTErsrq 同上一路：raw*0.5-20（raw ≤ 34），半格四舍五入到整数 dB。
func openluatLTErsrq(raw string) (int, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	if strings.HasPrefix(s, "-") {
		if n, err := strconv.Atoi(s); err == nil && n >= -20 && n <= -3 {
			return n, true
		}
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 34 {
		return 0, false
	}
	return int(math.Round(float64(n)*0.5 - 20)), true
}

func openluatLTEBandwidth(raw string) int {
	s := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(raw), "nN"))
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return lteBandwidthMHz[n]
}

// CCED 解析 openluat Air724UG 的**服务小区**（`AT+CCED=0,1`）。
//
// 刻意留空的字段（脚本也没有换算，或 CellInfo 没有对应槽）：
//   - `imsi` / `roaming`：CellInfo 没有这两个槽（脚本只用于显示）；
//   - `srxlev`：小区选择 RX 电平**索引**（不是 dBm），塞进 RSSI 会得到错值；
//   - GSM 的 `bsic`（基站识别码 NCC+BCC）与 `rxlev`（0~63 索引）：与 PCI/RSRP 不同口径 ⇒ 不填 PCID/信号。
func CCED(resp []string) (cell.CellInfo, bool) {
	for _, line := range resp {
		k, f, ok := CCEDLine(line)
		if !ok || !k.serving {
			continue
		}
		c := cell.CellInfo{Serving: true, Tech: k.tech, Raw: strings.TrimSpace(line)}
		c.MCC, c.MNC = common.NormalizePLMN(f[0]), common.NormalizePLMN(f[1])
		if !common.ValidPLMN(c.MCC, c.MNC) {
			continue
		}
		switch k.tech {
		case iface.CellTechLTE:
			c.State = "LTE"
			c.Band = common.BandNumFromText(f[4])
			c.Bandwidth = openluatLTEBandwidth(f[5])
			c.EARFCN = common.AtoiOr0(f[6])
			c.CellID = strings.ToUpper(f[7])
			c.RSRP, _ = openluatLTErsrp(f[8])
			c.RSRQ, _ = openluatLTErsrq(f[9])
			c.TAC = common.AtoiOr0(f[10])
			c.PCID = common.AtoiOr0(f[12])
		case iface.CellTechGSM:
			c.State = "GSM"
			c.TAC = common.AtoiOr0(f[2]) // LAC（3GPP 的 LAC 在 TAC 槽）
			c.CellID = strings.ToUpper(f[3])
			c.EARFCN = common.AtoiOr0(f[7]) // ARFCN
		default:
			continue
		}
		if c.CellID == "" && c.PCID == 0 && c.EARFCN == 0 && c.Band == 0 {
			continue
		}
		c.Operator = cell.OperatorName(c.MCC, c.MNC)
		return c, true
	}
	return cell.CellInfo{}, false
}

// CCEDNeighbours 解析 openluat Air724UG 的**邻区**（`AT+CCED=0,2`，可多行）。
//
// LTE 邻区 9 段：mcc,mnc,arfcn,cellid,rsrp_raw,rsrq_raw,tac,srxlev,pci；
// GSM 邻区 6 段：mcc,mnc,lac,cellid,bsic,rxlev（只有身份信息，无频率/信号）。
func CCEDNeighbours(resp []string) []cell.CellInfo {
	var out []cell.CellInfo
	for _, line := range resp {
		k, f, ok := CCEDLine(line)
		if !ok || k.serving {
			continue
		}
		c := cell.CellInfo{Tech: k.tech, Raw: strings.TrimSpace(line)}
		c.MCC, c.MNC = common.NormalizePLMN(f[0]), common.NormalizePLMN(f[1])
		if !common.ValidPLMN(c.MCC, c.MNC) {
			continue
		}
		switch k.tech {
		case iface.CellTechLTE:
			c.EARFCN = common.AtoiOr0(f[2])
			c.CellID = strings.ToUpper(f[3])
			c.RSRP, _ = openluatLTErsrp(f[4])
			c.RSRQ, _ = openluatLTErsrq(f[5])
			c.TAC = common.AtoiOr0(f[6])
			c.PCID = common.AtoiOr0(f[8])
			c.Band = iface.BandFromEARFCN(c.Tech, c.EARFCN)
		case iface.CellTechGSM:
			c.TAC = common.AtoiOr0(f[2]) // LAC
			c.CellID = strings.ToUpper(f[3])
		default:
			continue
		}
		if c.CellID == "" && c.PCID == 0 && c.EARFCN == 0 {
			continue
		}
		c.Operator = cell.OperatorName(c.MCC, c.MNC)
		out = append(out, c)
	}
	return out
}

// ccedKind 一行 `+CCED:` 的种类（字段数按 QModem 的切分口径，**必须完全相等**）。
type ccedKind struct {
	label   string
	tech    iface.CellTech
	serving bool
	fields  int
}

var CCEDKinds = []ccedKind{
	{"LTE CURRENT CELL", iface.CellTechLTE, true, 13},
	{"GSM CURRENT CELL INFO", iface.CellTechGSM, true, 8},
	{"LTE NEIGHBOR CELL", iface.CellTechLTE, false, 9},
	{"GSM NEIGHBOR CELL INFO", iface.CellTechGSM, false, 6},
}

// openluatLTEBandwidth 带宽字段是 **PRB 数**，换算表来自脚本的 openluat_lte_bandwidth()：
// n6→1.4 MHz、n15→3、n25→5、n50→10、n75→15、n100→20。
// CellInfo.Bandwidth 全项目统一按 MHz（int）⇒ 1.4 记成 1（与 MT5700 的带宽表同一口径）。
var lteBandwidthMHz = map[int]int{6: 1, 15: 3, 25: 5, 50: 10, 75: 15, 100: 20}

// init 把自己注册进 cell 的厂商私有格式表（cell 不 import 品牌包，所以用自注册）。
func init() {
	cell.RegisterServingCells(50, CCED, "+CCED")
	cell.RegisterNeighbourCells(CCEDNeighbours, "+CCED")
}
