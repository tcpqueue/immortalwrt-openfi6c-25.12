package cell

import (
	"strings"
)

// ====================== 未识别厂商的「服务小区 / 信号」解析（补充层） ======================
//
// 为什么需要这一层：Telit / Sierra / Foxconn / Thales / 美格 / 高新兴 / 有方 / openluat
// 在本项目里没有专门的 Command 实现（走"未识别厂商 + 自适应 + 候选探测"），
// 而 QModem 的 `cmds/<vendor>.sh` 里**也没有**服务小区指令 ——
// 它们的服务小区/信号其实是 `vendor/<vendor>.sh` 里用**各厂商自己的指令**解析的。
// 本文件把这些**有取证的**格式补进来（指令由 atcmd.go 的机型特例表指定）。
//
// 证据（全部是 QModem 原文，本地副本 `d:\Go\_pdfx\qmodem\`）：
//
//	vendor_thales.sh  `AT^DEBUG`（手册 §15.33）：RAT:/mcc:/mnc:/channel:/pci:/lte_cell_id:/
//	                  lte_band:/lte_band_width:/lte_snr:/rsrq:/lte_rsrp:/lte_rssi:/lte_tac:/lte_tx_pwr:
//	                  （NR 是 nr_cell_id:/nr_band:/nr_band_width:/nr_snr:/rsrp:/nr_tac:/nr_tx_pwr:；WCDMA 是 band:/lac:/rssi:）
//	vendor_foxconn.sh 同上（同一套 `AT[!^]DEBUG` 字段，前缀 `at_pre` 随型号是 `AT!` 或 `AT^`）
//	vendor_sierra.sh   `AT!GSTATUS?`：逐行 `key: value`，信号键含 RSRP/RSRQ/RSSI/SINR(SINR)
//	vendor_meig.sh     `AT^CELLINFO=<cid>`：CSV，NR 制式 f15/f16/f17=RSRP/RSRQ/SINR(÷10)，
//	                  EN-DC 用 f15/f16/f17 给 LTE 主小区、f22=TX、f30… 给 NR 辅小区（**脚本自相矛盾，本实现只取主小区**）
//	vendor_gosuncn.sh  lte 平台 `AT+ZCELLINFO?` → `+ZCELLINFO: <TAC>,cellid:<CellID>,pci:<PCI>,band:<Band>`
//	                  （信号另用 `AT+CESQ`，本项目已有 ParseCESQ）
//	vendor_telit.sh    `AT#CAINFOEXT?`：`#CAINFOEXT: <ca_count>,<rat>` + `PCC-` 行内
//	                  `BandClass:/BW:|DL_BW:/CH:|RX_CH:/PCI:/RSRP:/RSRQ:/RSSI:/SINR:(raw, dB=-20+raw*0.2)/TAC:/TX_PWR:(÷10)`
//	vendor_openluat.sh `AT+CCED=0,1` / `AT+CCED=0,2`：字段名写在**消费端**（`IFS='|' read -r …` 的变量名），
//	                  解析函数只按字段数切分 ⇒ 两处合起来才是完整字段表（见下面 openluat 一节）
//
// ⚠️ 两条铁律：
//  1. **不臆造**：脚本里没写清楚的（美格 EN-DC 的 NR 辅小区字段、高新兴 GM800 的 NSA 辅小区）
//     一律不解析，只把原始行留着（界面「网络接入信息」会显示指令名 + 原文）；
//  2. **只对自己认得的格式生效**：所有解析都要求**行里带该厂商特有的标签**，
//     所以别的机型的应答不会被误吃。

// ---- Thales / Foxconn：AT^DEBUG / AT!DEBUG ----

// ---- Sierra：AT!GSTATUS? ----

// ---- 美格：AT^CELLINFO=<cid> ----

// ---- 高新兴（lte 平台）：AT+ZCELLINFO? ----

// ---- Telit：AT#CAINFOEXT? ----

// ---- 有方：AT+NETDMSGEX ----

// ---- openluat（合宙 Air724UG）：AT+CCED=0,1 / AT+CCED=0,2 ----
//
// 指令与字段表的取证：QModem `vendor/openluat.sh` 的 `openluat_parse_cced_serving()` /
// `openluat_parse_cced_neighbors()` **只按字段数切分**（`if (field_count != N) next`），
// 紧接着 `cell_info()` / `get_neighborcell()` 用固定顺序的 `IFS='|' read -r …` 把每个字段
// 读进有名字的变量 —— 字段含义就是从那里来的（不是猜的）：
//
//	+CCED:LTE current cell:      mcc,mnc,imsi,roaming,band,bandwidth,dl_earfcn,cell_id,
//	                             rsrp_raw,rsrq_raw,tac,srxlev,pci                    （13 段）
//	+CCED:GSM current cell info: mcc,mnc,lac,cell_id,bsic,rxlev,rxlev_sub,arfcn        （ 8 段）
//	+CCED:LTE neighbor cell:     mcc,mnc,arfcn,cellid,rsrp_raw,rsrq_raw,tac,srxlev,pci （ 9 段）
//	+CCED:GSM neighbor cell info:mcc,mnc,lac,cellid,bsic,rxlev                        （ 6 段）
//
// ⚠️ 信号换算是脚本里**写明的公式**（`openluat_lte_rsrp_value` / `openluat_lte_rsrq_value`）：
// RSRP = raw-141（raw ≤ 97）、RSRQ = raw*0.5-20（raw ≤ 34）；已经是负数的读数按量纲校验后原样用。

// ====================== 统一入口 ======================

// VendorServingCells 用各厂商**自有的**服务小区格式解析应答（认不出返回 false）。
//
// 调用点：cell.go 的 ParseCellLines —— 标准格式（QENG/CPSI/GTCCINFO/MONSC）一条都没解出时，
// 再试这一层。**只看行里的厂商特有标签**，所以不会误吃别的机型的应答。
//
// 解析器本身住在 module/<品牌>/cell.go，由各品牌包在 init() 里登记（见 vendor_hooks.go）；
// 本包因此不需要 import 任何品牌包，新增厂商也不用改这里。
func VendorServingCells(resp []string) []CellInfo {
	for _, line := range resp {
		up := strings.ToUpper(strings.TrimSpace(line))
		for _, e := range servingEntries {
			if !hasAnyPrefix(up, e.tags) {
				continue
			}
			if c, ok := e.fn(resp); ok {
				return []CellInfo{c}
			}
		}
	}
	return nil
}

// hasAnyPrefix up 是否以 tags 里任意一个开头
func hasAnyPrefix(up string, tags []string) bool {
	for _, t := range tags {
		if strings.HasPrefix(up, t) {
			return true
		}
	}
	return false
}

// VendorNeighbourCells 用各厂商**自有的**邻区格式解析应答（认不出返回 nil）。
//
// 与 VendorServingCells 分开的理由：两类行的标签不同（`LTE current cell` vs `LTE neighbor cell`），
// 而且邻区**不能**走 ParseCellLines 的兜底 —— 那条路最终会在没有任何标准格式时
// 拿服务小区解析器试一遍邻区应答。
//
// 目前只有 openluat Air724UG（`AT+CCED=0,2`）有取证；其它厂商的 `vendor/*.sh` 里
// 没有邻区查询（只有服务小区的 cell_info()）⇒ 这里不编指令、不编格式。
func VendorNeighbourCells(resp []string) []CellInfo {
	for _, line := range resp {
		up := strings.ToUpper(strings.TrimSpace(line))
		for _, e := range neighbourEntries {
			if !hasAnyPrefix(up, e.tags) {
				continue
			}
			if cells := e.fn(resp); len(cells) > 0 {
				return cells
			}
		}
	}
	return nil
}

// VendorServingSignal 从**单行**里抠信号（Thales/Foxconn 的 `AT^DEBUG`、Sierra 的 `AT!GSTATUS`）。
//
// 为什么按行就能行：这两家的信号是"标签: 值"同行的（`lte_rsrp: -95` / `LTE RSRP: -88`），
// 所以 URC/应答的逐行解析路径（modem.go 的 parseLine）直接用得上，不需要攒整块。
func VendorServingSignal(line string) (rsrp, rsrq, sinr, rssi int, ok bool) {
	s := strings.TrimSpace(line)
	if s == "" {
		return 0, 0, 0, 0, false
	}
	i := strings.Index(s, ":")
	if i <= 0 {
		return 0, 0, 0, 0, false
	}
	key := strings.ToUpper(strings.TrimSpace(s[:i]))
	val := s[i+1:]
	// 只认这两家的键（`lte_rsrp` / `nr_rsrp` / `LTE RSRP` / `NR5G RSRQ`…），
	// 别把随便什么 `xxx rssi:` 当成信号
	isDebugKey := strings.HasPrefix(key, "LTE_") || strings.HasPrefix(key, "NR_") ||
		key == "RSRP" || key == "RSRQ" || key == "RSSI" || key == "SNR"
	isGSKey := strings.Contains(key, "RSRP") || strings.Contains(key, "RSRQ") ||
		strings.Contains(key, "RSSI") || strings.Contains(key, "SINR") || strings.Contains(key, "SNR")
	if !isDebugKey && !isGSKey {
		return 0, 0, 0, 0, false
	}
	// 负值才可能是 dBm/dB 读数（`LTE band: B3` 这类带数字的非信号键在此被挡掉）
	n := 0
	nums := atNumbersSigned(val)
	if len(nums) == 0 {
		return 0, 0, 0, 0, false
	}
	n = int(nums[0])
	switch {
	case strings.Contains(key, "RSRP"):
		rsrp = n
	case strings.Contains(key, "RSRQ"):
		rsrq = n
	case strings.Contains(key, "RSSI"):
		rssi = n
	case strings.Contains(key, "SINR"), strings.Contains(key, "SNR"):
		sinr = n
	}
	if rsrp == 0 && rsrq == 0 && rssi == 0 && sinr == 0 {
		return 0, 0, 0, 0, false
	}
	return rsrp, rsrq, sinr, rssi, true
}
