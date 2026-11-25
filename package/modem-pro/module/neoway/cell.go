package neoway

import (
	"modemd/iface"
	"modemd/module/cell"
	"modemd/module/common"
	"strconv"
	"strings"
)

// NetDMsgEx 解析有方的 `AT+NETDMSGEX`（vendor_neoway.sh 的 cell_info()，逐格 awk 取证）。
//
// 字段（1 起，脚本里就是这么编的）：
//
//	f1=制式串（`"TDD LTE"` / `"FDD LTE"` / `"NR to 5GCN"` / `"NR to EPS"` / `"NR-LTE ENDC"` / `"NR-LTE NEDC"` / WCDMA 系）
//	f2=`MCC+MNC`（脚本用 `cut -d'+' -f1/-f2` → 真机形如 `460+11`，兼容连写）
//	f3=频段  f4=频点
//	NR ：f5=cell_id  f6=PCI  f7=SS-RSRP(÷10)  f8=SS-RSRQ(÷10)  f9=SS-SINR(÷10)
//	LTE：f5=TAC  f6=cell_id  f7=PCI  f8=rx_dBm  f9=tx_dBm
//	     f10=RSRP(÷10)  f11=RSRQ(÷10)  f12=SINR(÷10)  f13=RSSI(÷10)  f17=下行带宽代号
//	WCDMA：f5=LAC  f6=cell_id  f7=PSC …  f11=RSCP(÷10)  f13=RSSI(÷10)
func NetDMsgEx(resp []string) (cell.CellInfo, bool) {
	f, line, ok := common.CSVAfterPrefix(resp, "+NETDMSGEX:")
	if !ok || len(f) < 5 {
		return cell.CellInfo{}, false
	}
	at := func(i int) string {
		if i-1 < len(f) {
			return strings.TrimSpace(strings.Trim(strings.TrimSpace(f[i-1]), `"`))
		}
		return ""
	}
	// 十分之一单位的读数（脚本统一 ÷10，未上报/占位值（非数字）当 0）
	tenth := func(i int) int {
		n, err := strconv.Atoi(strings.TrimSpace(at(i)))
		if err != nil {
			return 0
		}
		return n / 10
	}
	atoi := func(i int) int {
		n, _ := strconv.Atoi(strings.TrimSpace(at(i)))
		return n
	}
	mode := strings.ToUpper(at(1))
	c := cell.CellInfo{Serving: true, Raw: line}
	// PLMN：首选 `460+11`，其次纯数字连写
	plmn := at(2)
	if j := strings.Index(plmn, "+"); j > 0 {
		c.MCC, c.MNC = common.NormalizePLMN(plmn[:j]), common.NormalizePLMN(plmn[j+1:])
	} else if d := common.NormalizePLMN(plmn); len(d) >= 5 {
		c.MCC, c.MNC = d[:3], d[3:]
	}
	c.Band = common.BandNumFromText(at(3))
	c.EARFCN = atoi(4)
	switch {
	case strings.Contains(mode, "NR"):
		c.Tech = iface.CellTechNR
		c.CellID = at(5)
		c.PCID = atoi(6)
		c.RSRP, c.RSRQ, c.SINR = tenth(7), tenth(8), tenth(9)
	case strings.Contains(mode, "LTE"):
		c.Tech = iface.CellTechLTE
		c.TAC = atoi(5)
		c.CellID = at(6)
		c.PCID = atoi(7)
		c.RSRP, c.RSRQ, c.SINR, c.RSSI = tenth(10), tenth(11), tenth(12), tenth(13)
		if n := atoi(17); n > 0 {
			c.Bandwidth = n // 带宽代号（脚本用 get_bandwidth 转换，界面只当 MHz 参考）
		}
	case strings.Contains(mode, "WCDMA"), strings.Contains(mode, "HSDPA"), strings.Contains(mode, "HSUPA"):
		c.Tech = iface.CellTechWCDMA
		c.TAC = atoi(5) // LAC
		c.CellID = at(6)
		c.PCID = atoi(7) // PSC
		c.RSSI = tenth(13)
	default:
		return cell.CellInfo{}, false
	}
	c.State = mode
	if c.CellID == "" && c.PCID == 0 && c.Band == 0 && c.EARFCN == 0 {
		return cell.CellInfo{}, false
	}
	return c, true
}

// init 把自己注册进 cell 的厂商私有格式表（cell 不 import 品牌包，所以用自注册）。
func init() {
	cell.RegisterServingCells(50, NetDMsgEx, "+NETDMSGEX")
}
