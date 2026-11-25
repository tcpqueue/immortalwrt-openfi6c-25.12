package gosuncn

import (
	"modemd/iface"
	"modemd/module/cell"
	"modemd/module/common"
	"strings"
)

// ZCellInfo 高新兴 `AT+ZCELLINFO?`，两种平台都认：
//
//   - **lte 平台**（ME3630-W）：`+ZCELLINFO: <TAC>,cellid:<CellID>,pci:<PCI>,band:<Band>`
//     （vendor_gosuncn.sh 的 cell_info_lte()，注释原文就是这个形状；信号另发 AT+CESQ）
//   - **qualcomm 平台**（GM800）：`+ZCELLINFO:<制式>:…tac:N,cell_id:N,pci:N,band:N,freq:N,mcc:N,mnc:N,rsrp:N,rsrq:N,rssi:N,sinr:N…`
//     （`cell_info_qualcomm()` 逐键 grep：tac/lac/cell_id/pci/psc/band/freq/mcc/mnc/rsrp/rsrq/rssi/sinr/rscp/ecio；
//     NSA 另有一段带 `narfcn:/nr5g_pci:/5g_rsrp:/5g_rsrq:/5g_sinr:` 的辅小区行）
func ZCellInfo(resp []string) (cell.CellInfo, bool) {
	for _, line := range resp {
		s := strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(s), "+ZCELLINFO") {
			continue
		}
		i := strings.Index(s, ":")
		if i < 0 {
			continue
		}
		rest := strings.TrimSpace(s[i+1:])
		// qualcomm 平台在 `+ZCELLINFO:` 后面还有一层制式：`+ZCELLINFO:NR5G:tac:…`
		//（lte 平台没有这层，首格直接是 TAC 数字）⇒ 先把它摘掉，再按标签拆
		ntype := ""
		if j := strings.Index(rest, ":"); j >= 0 {
			head := strings.ToUpper(strings.TrimSpace(rest[:j]))
			if head == "NR5G" || head == "NR" || head == "LTE" || head == "LAC" {
				ntype, rest = head, strings.TrimSpace(rest[j+1:])
			}
		}
		// 首格是 TAC/制式（没有标签），其余是 `cellid:` / `pci:` / `band:`
		kv := common.ParseInlinePairs(rest)
		c := cell.CellInfo{Serving: true, Raw: s}
		// qualcomm 平台：制式已摘出，tac/lac 都在标签里
		if ntype != "" {
			switch {
			case strings.HasPrefix(ntype, "NR"):
				c.Tech = iface.CellTechNR
			case ntype == "LTE":
				c.Tech = iface.CellTechLTE
			default:
				c.Tech = iface.CellTechWCDMA
				c.TAC, _ = common.KVInt(kv, "lac")
			}
			if c.Tech != iface.CellTechWCDMA {
				c.TAC, _ = common.KVInt(kv, "tac")
			}
			c.MCC, c.MNC = common.NormalizePLMN(common.KVStr(kv, "mcc")), common.NormalizePLMN(common.KVStr(kv, "mnc"))
			c.CellID = common.KVStr(kv, "cell_id")
			if p, ok := common.KVInt(kv, "pci"); ok {
				c.PCID = p
			}
			if p, ok := common.KVInt(kv, "psc"); ok && c.PCID == 0 {
				c.PCID = p
			}
			if b, ok := common.KVInt(kv, "band"); ok {
				c.Band = b
			}
			if a, ok := common.KVInt(kv, "freq"); ok {
				c.EARFCN = a
			}
			c.RSRP, _ = common.KVInt(kv, "rsrp")
			c.RSRQ, _ = common.KVInt(kv, "rsrq")
			c.RSSI, _ = common.KVInt(kv, "rssi")
			c.SINR, _ = common.KVInt(kv, "sinr")
			if c.CellID == "" && c.PCID == 0 && c.Band == 0 {
				continue
			}
			return c, true
		}
		// lte 平台：首格是 TAC 数字
		c.Tech = iface.CellTechLTE
		if j := strings.Index(rest, ","); j > 0 {
			if n, ok := common.FirstNumber(rest[:j]); ok {
				c.TAC = n
			}
		}
		c.CellID = common.KVStr(kv, "cellid", "cell_id")
		if p, ok := common.KVInt(kv, "pci"); ok {
			c.PCID = p
		}
		if b, ok := common.KVInt(kv, "band"); ok {
			c.Band = b
		}
		if c.CellID == "" && c.PCID == 0 && c.Band == 0 {
			continue
		}
		return c, true
	}
	return cell.CellInfo{}, false
}

// init 把自己注册进 cell 的厂商私有格式表（cell 不 import 品牌包，所以用自注册）。
func init() {
	cell.RegisterServingCells(50, ZCellInfo, "+ZCELLINFO")
}
