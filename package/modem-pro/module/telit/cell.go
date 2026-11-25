package telit

import (
	"modemd/iface"
	"modemd/module/cell"
	"modemd/module/common"
	"strconv"
	"strings"
)

// CAInfoExt 解析 Telit 的 `AT#CAINFOEXT?`（vendor_telit.sh 的 cell_info()）。
//
// 形状：第一行 `#CAINFOEXT: <ca_count>,<rat>`，随后每个分量载波一行，主载波标 `PCC-`、辅载波 `SCC0-`…
// 主载波行内的键：`BandClass:`、`BW:`（或 `DL_BW:` 代号 0..5）、`CH:`（或 `RX_CH:`）、
// `PCI:`、`RSRP:`、`RSRQ:`、`RSSI:`、`SINR:`（**原始值，dB = -20 + raw*0.2**，见脚本）、`TAC:`、`TX_PWR:`（÷10）。
// 返回：服务小区（PCC）+ CA 分量载波列表。
func CAInfoExt(resp []string) (cell.CellInfo, []iface.CCInfo, bool) {
	var serving cell.CellInfo
	var ca []iface.CCInfo
	caCount := 0
	rat := ""
	for _, line := range resp {
		s := strings.TrimSpace(line)
		if s == "" || strings.EqualFold(s, "OK") {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(s), "#CAINFOEXT") {
			if i := strings.Index(s, ":"); i >= 0 {
				head := common.SplitATFields(strings.TrimSpace(s[i+1:]))
				if len(head) > 0 {
					caCount, _ = strconv.Atoi(strings.TrimSpace(head[0]))
				}
				if len(head) > 1 {
					rat = strings.TrimSpace(head[1])
				}
			}
			continue
		}
		up := strings.ToUpper(s)
		isPCC := strings.Contains(up, "PCC-")
		isSCC := strings.Contains(up, "SCC")
		if !isPCC && !isSCC {
			continue
		}
		kv := common.ParseInlinePairs(s)
		tech := iface.CellTechLTE
		if strings.Contains(strings.ToUpper(rat), "NR") {
			tech = iface.CellTechNR
		}
		cc := iface.CCInfo{Raw: s, Tech: tech}
		if b, ok := common.KVInt(kv, "bandclass"); ok {
			cc.Band = b
			cc.BandLabel = common.BandLabel(tech, b)
		}
		if a, ok := common.KVInt(kv, "ch", "rx_ch", "tx_ch"); ok {
			cc.EARFCN = a
		}
		if p, ok := common.KVInt(kv, "pci"); ok {
			cc.PCID = p
		}
		cc.RSRP, _ = common.KVInt(kv, "rsrp")
		cc.RSRQ, _ = common.KVInt(kv, "rsrq")
		if raw, ok := common.KVInt(kv, "sinr"); ok {
			cc.SINR = int(-20 + float64(raw)*0.2) // 脚本口径：dB = -20 + raw*0.2
		}
		if isPCC {
			serving = cell.CellInfo{
				Serving: true, Raw: s, Tech: tech,
				Band: cc.Band, EARFCN: cc.EARFCN, PCID: cc.PCID,
				RSRP: cc.RSRP, RSRQ: cc.RSRQ, SINR: cc.SINR,
			}
			if v, ok := common.KVInt(kv, "rssi"); ok {
				serving.RSSI = v
			}
			if t, ok := common.KVInt(kv, "tac"); ok {
				serving.TAC = t
			}
			if dl := common.KVStr(kv, "dl_mod"); dl != "" {
				serving.State = "DL_MOD:" + dl
			}
		} else {
			// 辅载波：Role 必须有值 —— CCsAggregated() 靠它判断"真的聚合了"，
			// 空 Role 会被当成"不等于 PCC"而误报有辅载波。
			cc.Role = "SCC"
			ca = append(ca, cc)
		}
	}
	if serving.PCID == 0 && serving.Band == 0 && serving.EARFCN == 0 {
		return cell.CellInfo{}, nil, false
	}
	_ = caCount
	return serving, ca, true
}

// init 把自己注册进 cell 的厂商私有格式表（cell 不 import 品牌包，所以用自注册）。
// servingCell 只取主载波当服务小区。
//
// Telit 的 `AT#CAINFOEXT?` **同时**含主载波与辅载波；辅载波是"分量载波"、不是"小区"，
// 交给 CA 那条路（mod.QueryCAInfo → telitCAOps，见 kernel/atcmd.go），
// 否则同一个 SCC 会又在小区列表里出现一次、又在 CA 卡片里出现一次。
func servingCell(resp []string) (cell.CellInfo, bool) {
	c, _, ok := CAInfoExt(resp)
	return c, ok
}

func init() {
	cell.RegisterServingCells(50, servingCell, "#CAINFOEXT")
}
