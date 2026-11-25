package meig

import (
	"modemd/iface"
	"modemd/module/cell"
	"modemd/module/common"
	"strconv"
	"strings"
)

// ParseCellInfo 解析美格的 `AT^CELLINFO=<cid>`（vendor_meig.sh 的 cell_info()）。
//
// CSV 字段（脚本里逐格编号，1 起）：
//
//	NR(5G)：f1=制式 f2=双工 f3=MCC f4=MNC f5=cell_id f6=PCI f7=TAC f8=band f9=dl_bw f10=SCS
//	        f15=RSRP f16=RSRQ f17=SINR(÷10)
//	EN-DC(LTE-NR)：f2=双工 f3=MCC f4=MNC f6=PCI f8=cell_id f9=TAC f10=LTE band f11=ul_bw
//	        f15=RSRP f16=RSRQ f17=SINR(÷10) f22=TX
//
// ⚠️ EN-DC 的 **NR 辅小区**字段在脚本里自相矛盾（$30 既当 PCI 又当 RSRP），
// 本项目**不猜**：只解析 LTE 主小区，NR 辅小区留给原始应答。
func ParseCellInfo(resp []string) (cell.CellInfo, bool) {
	f, line, ok := common.CSVAfterPrefix(resp, "^CELLINFO:")
	if !ok || len(f) < 5 {
		return cell.CellInfo{}, false
	}
	at := func(i int) string { // 1 起的字段
		if i-1 < len(f) {
			return strings.TrimSpace(f[i-1])
		}
		return ""
	}
	atoi := func(i int) int {
		n, _ := strconv.Atoi(strings.TrimSpace(strings.Trim(at(i), `"`)))
		return n
	}
	c := cell.CellInfo{Serving: true, Raw: line}
	rat := strings.ToUpper(at(1))
	switch {
	case rat == "5G":
		c.Tech, c.State = iface.CellTechNR, "5G"
		c.MCC, c.MNC = common.NormalizePLMN(at(3)), common.NormalizePLMN(at(4))
		c.CellID = at(5)
		c.PCID = atoi(6)
		c.TAC = atoi(7)
		c.Band = atoi(8)
		c.Bandwidth = atoi(9)
		c.RSRP, c.RSRQ = atoi(15), atoi(16)
		if s := atoi(17); s != 0 {
			c.SINR = s / 10 // 脚本：SINR 是 0.1dB 单位
		}
	case common.DashNorm(rat) == "LTE-NR" || strings.Contains(rat, "LTE-NR"):
		c.Tech, c.State = iface.CellTechLTE, "EN-DC"
		c.MCC, c.MNC = common.NormalizePLMN(at(3)), common.NormalizePLMN(at(4))
		c.PCID = atoi(6)
		c.CellID = at(8)
		c.TAC = atoi(9)
		c.Band = atoi(10)
		c.Bandwidth = atoi(11)
		c.RSRP, c.RSRQ = atoi(15), atoi(16)
		if s := atoi(17); s != 0 {
			c.SINR = s / 10
		}
	default:
		return cell.CellInfo{}, false
	}
	return c, true
}

// init 把自己注册进 cell 的厂商私有格式表（cell 不 import 品牌包，所以用自注册）。
func init() {
	cell.RegisterServingCells(50, ParseCellInfo, "^CELLINFO")
}
