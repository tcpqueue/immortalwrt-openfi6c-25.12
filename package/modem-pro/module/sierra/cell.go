package sierra

import (
	"modemd/iface"
	"modemd/module/cell"
	"modemd/module/common"
	"strings"
)

// GStatus 解析 Sierra 的 `AT!GSTATUS?`（vendor_sierra.sh 的 network_info()）。
//
// 脚本的 `_parse_gstatus` 是**通用 key:value**：键里含 SINR/RSRP/RSRQ/RSSI 的算信号，
// 其余当普通信息。这里把服务小区字段也认出来（`LTE band:` / `LTE bw:` / `LTE Rx chan:` 等），
// 信号键按同一规则匹配（`LTE RSRP` / `NR5G RSRP` / `LTE RSSI`…）。
//
// ⚠️ 认领门槛（很重要）：必须有 `!GSTATUS` 行或 `System mode:` 键才算 Sierra ——
// 否则 Thales/Foxconn 的 `AT^DEBUG` 块（也有裸 `rsrp:`）会被误吃、制式也会认错。
// 老机型（MC7354/MC7355）的键没有 `LTE `/`NR5G ` 前缀，所以下面每项都兼带无前缀候选。
func GStatus(resp []string) (cell.CellInfo, bool) {
	kv := common.ParseLabeledKV(resp)
	if len(kv) == 0 {
		return cell.CellInfo{}, false
	}
	raw := strings.Join(resp, "|")
	if !strings.Contains(strings.ToUpper(raw), "!GSTATUS") && common.KVStr(kv, "system mode") == "" {
		return cell.CellInfo{}, false
	}
	mode := strings.ToUpper(common.KVStr(kv, "system mode", "mode"))
	c := cell.CellInfo{Serving: true, Raw: strings.Join(resp, "|")}
	// 信号：按脚本规则"键里含关键字"匹配（NR/LTE 谁在场用谁，NR 优先）
	tech := iface.CellTechLTE
	switch {
	case strings.Contains(mode, "NR5G"), strings.Contains(mode, "NR"):
		tech = iface.CellTechNR
	}
	for k, v := range kv {
		n, ok := common.AvgNumber(v)
		if !ok {
			continue
		}
		up := strings.ToUpper(k)
		isNR := strings.Contains(up, "NR5G") || strings.Contains(up, "NR ")
		if tech == iface.CellTechNR && !isNR && c.RSRP != 0 {
			continue // NR 驻网时优先取 NR 那一组
		}
		switch {
		case strings.Contains(up, "RSRP") && c.RSRP == 0:
			c.RSRP = n
		case strings.Contains(up, "RSRQ") && c.RSRQ == 0:
			c.RSRQ = n
		case strings.Contains(up, "RSSI") && c.RSSI == 0:
			c.RSSI = n
		// 脚本只认 SINR；Sierra 部分固件写成 SNR（同一含义）⇒ 一并收（注释在文件头）
		case (strings.Contains(up, "SINR") || strings.Contains(up, "SNR")) && c.SINR == 0:
			c.SINR = n
		}
	}
	c.Tech = tech
	c.State = common.KVStr(kv, "system mode", "mode")
	// 服务小区：LTE 与 NR5G 两套键名（`LTE band: B3` / `NR5G band: n78`），老机型无前缀
	bandText := common.KVStr(kv, "nr5g band", "lte band", "active band", "band")
	if bandText != "" {
		c.Band = common.BandNumFromText(bandText)
		if t := common.TechFromBandText(bandText); t != "" {
			c.Tech = t
		}
	}
	if b, ok := common.KVInt(kv, "nr5g bw", "lte bw", "bw"); ok {
		c.Bandwidth = b
	}
	if a, ok := common.KVInt(kv, "nr5g rx chan", "lte rx chan", "nr5g chan", "lte chan", "rx chan", "chan"); ok {
		c.EARFCN = a
	}
	c.CellID = common.KVStr(kv, "nr5g cell id", "lte cell id", "cell id")
	if p, ok := common.KVInt(kv, "nr5g pci", "lte pci", "pci"); ok {
		c.PCID = p
	}
	if t, ok := common.KVInt(kv, "nr5g tac", "lte tac", "tac"); ok {
		c.TAC = t
	}
	c.MCC = common.NormalizePLMN(common.KVStr(kv, "mcc"))
	c.MNC = common.NormalizePLMN(common.KVStr(kv, "mnc"))
	if c.RSRP == 0 && c.PCID == 0 && c.CellID == "" && c.Band == 0 {
		return cell.CellInfo{}, false
	}
	return c, true
}

// init 把自己注册进 cell 的厂商私有格式表（cell 不 import 品牌包，所以用自注册）。
func init() {
	cell.RegisterServingCells(10, GStatus, "!GSTATUS", "SYSTEM MODE", "RAT:", "LTE_BAND:", "LTE_RSRP:", "NR_BAND:")
}
