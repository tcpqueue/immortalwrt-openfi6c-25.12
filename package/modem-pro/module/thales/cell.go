package thales

import (
	"modemd/iface"
	"modemd/module/cell"
	"modemd/module/common"
	"strings"
)

// DebugCell 解析 Thales / Foxconn 的 `AT^DEBUG`（手册 §15.33 / vendor_*.sh 的 cell_info()）。
//
// 制式来自 `RAT:` 行：`WCDMA` / `LTE` / `LTE+NR` / `NR5G_SA`。
// 字段名各制式略有不同（LTE 的 RSRP 叫 `lte_rsrp:`、NR 叫 `rsrp:`），
// 所以每项都给一串候选键，按顺序取第一个命中的 —— 与脚本里的 awk -F 取值一一对应。
func DebugCell(resp []string) (cell.CellInfo, bool) {
	kv := common.ParseLabeledKV(resp)
	rat := strings.ToUpper(common.KVStr(kv, "rat"))
	if rat == "" {
		return cell.CellInfo{}, false
	}
	c := cell.CellInfo{Serving: true, Raw: strings.Join(resp, "|")}
	switch {
	case strings.Contains(rat, "NR"):
		c.Tech = iface.CellTechNR
		c.State = rat
		c.CellID = common.KVStr(kv, "nr_cell_id", "cell_id")
		c.EARFCN, _ = common.KVInt(kv, "channel", "nr_channel")
		c.PCID, _ = common.KVInt(kv, "pci")
		c.Band, _ = common.KVInt(kv, "nr_band", "band")
		c.Bandwidth, _ = common.KVInt(kv, "nr_band_width", "band_width")
		c.SINR, _ = common.KVAvgInt(kv, "nr_snr", "snr")
		c.RSRP, _ = common.KVAvgInt(kv, "rsrp", "nr_rsrp")
		c.RSRQ, _ = common.KVAvgInt(kv, "rsrq", "nr_rsrq")
		c.RSSI, _ = common.KVAvgInt(kv, "rssi", "nr_rssi")
		c.TAC, _ = common.KVInt(kv, "nr_tac", "tac")
	case strings.Contains(rat, "LTE"):
		c.Tech = iface.CellTechLTE
		c.State = rat
		c.CellID = common.KVStr(kv, "lte_cell_id", "cell_id")
		c.EARFCN, _ = common.KVInt(kv, "channel", "lte_channel")
		c.PCID, _ = common.KVInt(kv, "pci")
		c.Band, _ = common.KVInt(kv, "lte_band", "band")
		c.Bandwidth, _ = common.KVInt(kv, "lte_band_width", "band_width")
		c.SINR, _ = common.KVAvgInt(kv, "lte_snr", "snr")
		c.RSRP, _ = common.KVAvgInt(kv, "lte_rsrp", "rsrp")
		c.RSRQ, _ = common.KVAvgInt(kv, "rsrq", "lte_rsrq")
		c.RSSI, _ = common.KVAvgInt(kv, "lte_rssi", "rssi")
		c.TAC, _ = common.KVInt(kv, "lte_tac", "tac")
	case strings.Contains(rat, "WCDMA"):
		c.Tech = iface.CellTechWCDMA
		c.State = rat
		c.CellID = common.KVStr(kv, "cell_id")
		c.EARFCN, _ = common.KVInt(kv, "channel")
		c.Band, _ = common.KVInt(kv, "band")
		c.RSSI, _ = common.KVInt(kv, "rssi")
		c.TAC, _ = common.KVInt(kv, "lac")
	default:
		return cell.CellInfo{}, false
	}
	c.MCC = common.NormalizePLMN(common.KVStr(kv, "mcc"))
	c.MNC = common.NormalizePLMN(common.KVStr(kv, "mnc"))
	if c.Band == 0 && c.EARFCN == 0 && c.PCID == 0 && c.CellID == "" {
		return cell.CellInfo{}, false
	}
	return c, true
}

// dashNorm 兼容下划线写法（LTE_NR → LTE-NR）

// init 把自己注册进 cell 的厂商私有格式表（cell 不 import 品牌包，所以用自注册）。
func init() {
	cell.RegisterServingCells(20, DebugCell, "!GSTATUS", "SYSTEM MODE", "RAT:", "LTE_BAND:", "LTE_RSRP:", "NR_BAND:")
}
