package cell

// ====================== kernel：按功能分文件 ======================

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"modemd/iface"
	"modemd/module/common"
)

// ---- from: cell/band.go ----
//
// reBandNum / BandLabel / BandNumFromText / TechFromBandText 已搬到 modemd/module/common
// （见 module/common/atutil.go）—— 厂商包（module/tdtech 等）的解析器也要用，
// 所以不能住在被依赖的内核包里。kernel 保留同名别名，见 atutil_alias.go。
// 搬家前的老名字（bandLabel / bandNumFromText / techFromBandText）的别名见 glue.go。

// ---- from: cell/ca.go ----

//
// 载波聚合下终端同时使用多个分量载波（CC）：一个主载波 PCC + 若干辅载波 SCC。
// 各家上报格式完全不同，这里放**纯解析**；发指令与缓存由 kernel 负责
// （lock.go 的 CAVendorOps / QueryCAInfo、atcmd.go 的 MT5700 实现）。
// —— 移远：AT+QCAINFO ——
//
//	+QCAINFO: "PCC",627264,3,"NR5G BAND 78",521                       空闲态（无信号值）
//	+QCAINFO: "PCC",2452,20,"LTE BAND 3",155,-95,-12,-65,14           连接态
//	+QCAINFO: "SCC",1650,20,"LTE BAND 3",300,-97,-11,-68,13
//
// 字段：<角色>,<频点>,<带宽>,<频段串>,<PCID>[,<RSRP>,<RSRQ>[,<RSSI>],<SINR>]
// 信号值只在进入连接态、聚合真正生效时才上报；空闲态通常只有前 5 个字段。

var reQCAINFO = regexp.MustCompile(`\+QCAINFO:\s*"([^"]*)"\s*,(.*)$`)

// ParseQCAINFO 解析 AT+QCAINFO 应答，返回各分量载波。
//
// 厂商不支持、未注册、或当前没有聚合时都可能返回空/裸 OK，此时返回空列表，
// 由调用方按"没有 CA 信息"处理，不做补偿猜测。
func ParseQCAINFO(resp []string) []iface.CCInfo {
	var out []iface.CCInfo
	for _, line := range resp {
		m := reQCAINFO.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		f := common.SplitATFields(m[2])
		if len(f) < 4 {
			continue // 至少要有 频点/带宽/频段/PCID
		}
		cc := iface.CCInfo{Role: strings.ToUpper(strings.TrimSpace(m[1])), Raw: strings.TrimSpace(line)}
		cc.EARFCN = common.AtIntOr0(f, 0)
		cc.Bandwidth = common.AtIntOr0(f, 1)
		cc.BandText = f[2]
		cc.Tech = common.TechFromBandText(cc.BandText)
		cc.Band = common.BandNumFromText(cc.BandText)
		if cc.Band == 0 { // 部分固件频段串里没数字，退回按频点推算
			cc.Band = iface.BandFromEARFCN(cc.Tech, cc.EARFCN)
		}
		cc.BandLabel = common.BandLabel(cc.Tech, cc.Band)

		// PCID：**取 RSRP 之前的最后一个合法值**（0~1007）。
		//
		// 为什么不用固定下标 f[3]：RM500U-CNV 的 PCC 行在频段串与 PCID 之间多一格
		// （`"PCC",1850,100,LTE BAND 3,1,140,-117,…`），按 f[3] 会把那个 1 当成 PCI；
		// 而 RM502Q-GL 是 `"PCC",627264,3,"NR5G BAND 78",521`（频段串后直接是 PCID）。
		// 两种写法按"信号锚点回推"都能对上：有信号值时取 RSRP 前一个合法值，
		// 没有信号值时取最后一个合法值（就是 PCID 那种简写行）。
		sigIdx := FindRSRPToken(f, 3)
		limit := len(f)
		if sigIdx >= 0 {
			limit = sigIdx
		}
		for i := limit - 1; i >= 3; i-- {
			if v, ok := common.ParseATInt(f[i]); ok && v >= 0 && v <= 1007 {
				cc.PCID = v
				break
			}
		}
		if sigIdx >= 0 {
			var sig CellInfo
			ApplySignalFields(&sig, f, sigIdx)
			cc.RSRP, cc.RSRQ, cc.RSSI, cc.SINR = sig.RSRP, sig.RSRQ, sig.RSSI, sig.SINR
		}
		out = append(out, cc)
	}
	return out
}

// —— 鼎桥 MT5700：AT^CASCELLINFO?（LTE CA）/ AT^MONSSC（NSA 辅连接）——
//
// 手册：《MT5700M-CN 5G 系列模组 AT 命令手册》
//
//	§13.18 AT^CASCELLINFO?  LTE CA 状态下 SCELL 信息（最多 4 个 SCell）
//	§13.27 AT^MONSSC        NSA(EN-DC) 情况下查询辅连接服务小区（PSCell + SCell，可多行）
//
//	^CASCELLINFO: <index>,<pci>,<rssi>,<rsrp>,<rsrq>,<band_class>,
//	              <ulfreqpoint>,<dlfreqpoint>,<ulfreq>,<dlfreq>,<ulbw>,<dlbw>
//	^MONSSC: NR,<ARFCNNR>,<PCI 十六进制>,<RSRP>,<RSRQ>,<SINR>,<MEASTYPE>
//	^MONSSC: NONE                     ← 当前不支持/没有辅连接
//
// 与移远（AT+QCAINFO）最大的不同：
//  1. **只有辅小区**，没有 PCC 行（主载波要看服务小区），所以这里全部按 SCC 填；
//  2. band 是十进制数字（`band_class`），不是 "LTE BAND 3" 这种字符串；
//  3. **"没有聚合"用 ERROR / NONE 表示**（手册明确），不是空应答 ——
//     所以拿到 ERROR 要当成"当前没有 CA"，而不是"指令不支持"；
//  4. 本固件**没有 NR SA 的载波聚合查询**：CASCELLINFO 只覆盖 LTE CA，
//     MONSSC 只在 NSA(EN-DC) 下有内容。
// 正则与带宽代号表已随解析器搬到 modemd/module/tdtech（见 module/tdtech/parse.go）。

// tdtech.ParseCASCellInfo 解析 `AT^CASCELLINFO?`（LTE CA 的 SCell）—— 实现已搬到
// modemd/module/tdtech（见 module/tdtech/parse.go）；kernel 保留同名别名，见 tdtech_alias.go。

// tdtech.ParseMONSSC 解析 `AT^MONSSC`（NSA 辅连接小区，可能多行）—— 实现已搬到
// modemd/module/tdtech（见 module/tdtech/parse.go）；kernel 保留同名别名，见 tdtech_alias.go。

// dropSentinel / atoiOr0 已搬到 modemd/module/common（DropSentinel / AtoiOr0）。
// kernel 保留同名别名，见 atutil_alias.go。

// ---- from: cell/cell.go ----

// iface.CellTech 与 AllCellTechs 已搬到 modemd/iface（厂商层也要用，不能反向依赖内核）。
// kernel 里保留同名别名，见 iface_alias.go。

// CellInfo 基站/小区信息
type CellInfo struct {
	Tech      iface.CellTech `json:"tech"`                // 接入技术: LTE/NR/WCDMA/GSM
	State     string         `json:"state"`               // 连接状态: NOCONN/SEARCH/CAMPED/CONNECTED...
	MCC       string         `json:"mcc"`                 // 移动国家码 460=中国
	MNC       string         `json:"mnc"`                 // 移动网络码
	CellID    string         `json:"cell_id"`             // 小区 ID（十六进制）
	PCID      int            `json:"pcid,omitempty"`      // 物理小区 ID（服务小区/邻小区，LTE PCI / NR PCI）
	TAC       int            `json:"tac"`                 // 跟踪区码
	EARFCN    int            `json:"earfcn"`              // 频点号（LTE EARFCN / NR ARFCN）
	Band      int            `json:"band"`                // 频段号
	Bandwidth int            `json:"bandwidth,omitempty"` // 带宽 MHz（部分厂商上报；0 = 未上报）
	RSRP      int            `json:"rsrp"`                // 参考信号接收功率 dBm
	RSRQ      int            `json:"rsrq"`                // 参考信号接收质量 dB
	RSSI      int            `json:"rssi,omitempty"`      // 接收信号强度 dBm
	SINR      int            `json:"sinr"`                // 信噪比 dB
	Serving   bool           `json:"serving"`             // 是否为当前服务小区
	IntraCell bool           `json:"intra_cell"`          // 是否同频邻小区（neighbourcell intra）
	Operator  string         `json:"operator,omitempty"`  // 运营商名称
	Raw       string         `json:"raw,omitempty"`       // 原始 AT 行，便于核对
}

// OperatorName 根据 MCC/MNC 推断中国运营商
func (c *CellInfo) OperatorName() string {
	return OperatorName(c.MCC, c.MNC)
}

// SignalLevel 按 RSRP 给出信号等级描述（0 表示模组未上报）
func (c *CellInfo) SignalLevel() string {
	switch {
	case c.RSRP == 0:
		return "未知"
	case c.RSRP >= -80:
		return "优"
	case c.RSRP >= -90:
		return "良"
	case c.RSRP >= -100:
		return "中"
	case c.RSRP >= -110:
		return "差"
	default:
		return "极差"
	}
}

// OperatorName 根据 MCC/MNC 推断中国运营商
func OperatorName(mcc, mnc string) string {
	switch mcc + mnc {
	case "46000", "46002", "46007":
		return "中国移动"
	case "46001", "46006", "46009":
		return "中国联通"
	case "46003", "46005", "46011":
		return "中国电信"
	case "46004", "46008":
		return "中国广电"
	}
	return ""
}

// 各厂商实测返回格式：
//
//	servingcell LTE: +QENG: "servingcell","NOCONN","LTE","FDD",460,11,400E21E,155,2452,5,3,3,404A,-104,-16,-69,7,0,-,19
//	servingcell NR:  +QENG: "servingcell","NR","FDD",460,00,1051E8F2,123,0,-95,-12,14,35
//	neighbour LTE:   +QENG: "neighbourcell intra","LTE",2452,155,-15,-103,-70,-,21,3,20,2,54
//	neighbour NR:    +QENG: "neighbourcell inter","NR",636666,15,-91,-11,16,...
//	广和通同结构，指令头为 +FENG
//
// ⚠️ RM500U-CNV（展锐平台）把服务小区拆成两行，数据行没有标记：
//
//	+QENG: "servingcell","CONNECT"
//	+QENG: "LTE","FDD",460,11,3FA6836,140,1850,3,5,5,404A,-117,-14,-83,-2,6,22,7
//	+QENG: "NR5G-NSA",0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0   ← 未连接 NR 时的全 0 占位行
var (
	// 匹配 `+QENG: "servingcell",...` / `+FENG: "neighbourcell intra",...` 这类行。
	// 捕获组 1 = 小区类型标签，捕获组 2 = 其余以逗号分隔的字段。
	reCellLine = regexp.MustCompile(`^\s*\+[A-Z]+:\s*"([^"]+)"\s*,\s*(.*)$`)
	// AT+COPS? 应答：+COPS: 0,0,"CHN-UNICOM",7
	ReCOPS = regexp.MustCompile(`\+COPS:\s*\d+,\d+,"([^"]*)"`)
	// +CPSI 服务小区上报（芯讯通 / MTK 系 / MT5700 这类模组的常见写法）：
	//   +CPSI: NR5G,Online,460-11,0x1051E8F2,627264,504,78,100,-,-95,-12,-7,13
	reCPSI = regexp.MustCompile(`^\s*\+CPSI:\s*(.*)$`)
	// SIMCom SIM8200 的 +CPSI 频率字段是**带名字**的（NR5G_BAND78 / EUTRAN-BAND3），
	// 而另一套写法是裸数字（...,504,78,100,...），两套字段顺序完全不同。
	// 这里用"带名字的频率"当锚点来定位其他字段（见 parseCPSIBandNameLayout）。
	reCPSIBandName = regexp.MustCompile(`^(?:NR5G_BAND|NR_BAND|EUTRAN_BAND|EUTRAN-BAND|BAND)(\d+)$`)
)

// ParseCellLine 解析一行 AT 返回的小区信息。
// 兼容移远 +QENG / 广和通 +FENG 的 servingcell 与 neighbourcell intra|inter 格式。
func ParseCellLine(line string) (CellInfo, bool) {
	m := reCellLine.FindStringSubmatch(line)
	if m == nil {
		return CellInfo{}, false
	}
	tag := strings.ToLower(strings.TrimSpace(m[1]))
	switch {
	case tag == "servingcell":
		return ParseServingCell(m[2], line)
	case strings.HasPrefix(tag, "neighbourcell"):
		return parseNeighbourCell(m[2], line, strings.Contains(tag, "intra"))
	}
	return CellInfo{}, false
}

// ParseServingCell 解析当前服务小区。
//
// 字段顺序（移远/广和通实测）：
//
//	<state>,<tech>,<duplex>,<MCC>,<MNC>,<CellID>,<TAC>,<EARFCN>,<band>,...
//	<tech>,<duplex>,<MCC>,<MNC>,<CellID>,<TAC>,<ARFCN>,...   （无 state 的简写固件）
//
// 不同固件在 band 之后附加的字段个数不一致（上行/下行带宽、PCID 等），
// 因此信号值按“量纲合理性”定位，而不是硬编码下标，避免字段错位。
func ParseServingCell(rest, raw string) (CellInfo, bool) {
	f := common.SplitATFields(rest)
	i := 0
	// 可选 state 字段（NOCONN/SEARCH/CAMPED/CONNECTED...）
	var state string
	if len(f) > 0 && !isTechToken(f[0]) {
		state = strings.ToUpper(f[0])
		i = 1
	}
	if i >= len(f) || !isTechToken(f[i]) {
		return CellInfo{}, false
	}
	techTok := f[i]
	cell := CellInfo{
		State:   state,
		Tech:    NormalizeTech(techTok),
		Serving: true,
		Raw:     raw,
	}
	i++
	// 可选双工模式
	if i < len(f) && isDuplexToken(f[i]) {
		i++
	}
	// MCC / MNC / CellID / TAC / EARFCN
	if i < len(f) {
		cell.MCC = f[i]
		i++
	}
	if i < len(f) {
		cell.MNC = f[i]
		i++
	}
	if i < len(f) {
		cell.CellID = strings.ToUpper(f[i])
		i++
	}
	// cellID 之后的字段顺序随制式不同（实测 RM502Q-GL + Quectel 文档）：
	//
	//	NR5G-SA / NR5G-NSA : <PCID>,<TAC(hex)>,<ARFCN>,<band>,...
	//	LTE                : <PCID>,<EARFCN>,<band>,<ulbw>,<dlbw>,<TAC>,<RSRP>,...
	//	通用 NR            : <TAC>,<ARFCN>,<band>,...
	//
	// ⚠️ LTE 也在 cellID 之后紧跟 PCID。漏掉这一格会把 PCI 读成 TAC 且 PCI 丢失
	// （EARFCN/band 恰好占掉了那一格，看起来还是对的，所以很容易蒙混过关）。
	if cell.Tech == iface.CellTechLTE {
		cell.PCID = common.AtIntOr0(f, i) // PCID
		i++
		if v, ok := common.IntAt(f, i); ok {
			cell.EARFCN = v
		}
		i++
		if v, ok := common.IntAt(f, i); ok && v >= 1 && v <= 261 {
			cell.Band = v
		}
		i++
		// TAC 在 RSRP 的前一格。不同固件的上下行带宽字段个数不一致，
		// 因此以 RSRP 为锚点回推，而不是硬编码 i+2。
		if idx := FindRSRPToken(f, i); idx > i {
			cell.TAC = common.AtIntOr0(f, idx-1)
			i = idx
		}
	} else {
		if IsNRSA(techTok) {
			cell.PCID = common.AtIntOr0(f, i)
			i++
		}
		if v, ok := common.IntAt(f, i); ok {
			cell.TAC = v
		}
		i++
		if v, ok := common.IntAt(f, i); ok {
			cell.EARFCN = v
		}
		i++
	}
	// 至少要上报 PLMN，否则视为无效应答（如 "+QENG: \"servingcell\",\"LTE\""）。
	// ⚠️ "0"/"00" 是占位：NR5G-NSA 未连接时模组会回一整行 0（`+QENG: "NR5G-NSA",0,0,0,…`），
	// 不挡住会被当成一个“MCC=0、MNC=0”的假服务小区。
	if !validPLMN(cell.MCC, cell.MNC) {
		return CellInfo{}, false
	}
	cell.Operator = OperatorName(cell.MCC, cell.MNC)

	// 定位信号字段：第一个落在 RSRP 量纲区间（-156 ~ -44 dBm）的字段
	idx := FindRSRPToken(f, i)
	if idx >= 0 {
		// band：EARFCN 与 RSRP 之间的第一个合法频段值
		if idx > i {
			if b, ok := common.ParseATInt(f[i]); ok && b >= 1 && b <= 261 {
				cell.Band = b
			}
		}
		ApplySignalFields(&cell, f, idx)
	}
	if cell.Band == 0 {
		cell.Band = iface.BandFromEARFCN(cell.Tech, cell.EARFCN)
	}
	return cell, true
}

// parseNeighbourCell 解析附近基站（邻小区）。
//
// 字段顺序**各固件不一致，而且同一厂商不同平台都反着来**，所以不硬编码：
//
//	RM502Q-GL（现网）：`…,"LTE",2452,155,-15,-103,-70,-,21,…` → RSRQ,RSRP,RSSI,SINR
//	RM500U-CNV（现网）：`…,"LTE",2452,155,-118,-12,0,0,255,…` → RSRP,RSRQ,…
//
// 做法：先在 pcid 之后找第一个落在 RSRP 量纲（-156~-44 dBm）的字段当 RSRP，
// 再从它**前/后**各试一格找 RSRQ（两种顺序都能对上），剩下的按量纲认 RSSI/SINR。
// 一个 RSRP 都找不到时（RM500U 的 intra 行只给相对值）只填 RSRQ，
// **绝不硬凑一个假 RSRP** —— 前端会把它显示成「—」，比显示错的值好。
func parseNeighbourCell(rest, raw string, intra bool) (CellInfo, bool) {
	f := common.SplitATFields(rest)
	if len(f) < 2 || !isTechToken(f[0]) {
		return CellInfo{}, false
	}
	cell := CellInfo{
		Tech:      NormalizeTech(f[0]),
		IntraCell: intra,
		Raw:       raw,
	}
	cell.EARFCN, _ = common.ParseATInt(f[1])
	cell.PCID = common.AtIntOr0(f, 2)
	cell.Band = iface.BandFromEARFCN(cell.Tech, cell.EARFCN)

	const sigStart = 3 // pcid 之后的第一个信号字段
	idx := FindRSRPToken(f, sigStart)
	if idx < 0 {
		// 没有绝对 RSRP：把第一个 RSRQ 量纲（-43~20）的值当 RSRQ
		for i := sigStart; i < len(f); i++ {
			if v, ok := common.IntAt(f, i); ok && v >= -43 && v <= 20 {
				cell.RSRQ = v
				break
			}
		}
		return cell, true
	}
	cell.RSRP, _ = common.ParseATInt(f[idx])

	// RSRQ：RSRP 前一格（先试，RM502Q 的顺序）或后一格（RM500U 的顺序）
	rsrqAt := -1
	if idx-1 >= sigStart {
		if v, ok := common.IntAt(f, idx-1); ok && v >= -43 && v <= 20 {
			cell.RSRQ, rsrqAt = v, idx-1
		}
	}
	if rsrqAt < 0 {
		if v, ok := common.IntAt(f, idx+1); ok && v >= -43 && v <= 20 {
			cell.RSRQ, rsrqAt = v, idx+1
		}
	}

	// RSSI / SINR：把剩下的字段按量纲依次认下来（未上报的 0/255/- 自然跳过）
	gotRSSI, gotSINR := false, false
	for i := sigStart; i < len(f); i++ {
		if i == idx || i == rsrqAt {
			continue
		}
		v, ok := common.IntAt(f, i)
		if !ok {
			continue
		}
		if !gotRSSI && v >= -113 && v <= -25 {
			cell.RSSI, gotRSSI = v, true
			continue
		}
		if !gotSINR && v >= -23 && v <= 40 {
			cell.SINR, gotSINR = v, true
		}
	}
	return cell, true
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func allZero(s string) bool {
	for _, r := range s {
		if r != '0' {
			return false
		}
	}
	return true
}

// ParseCellLines 批量解析 AT 应答中的小区信息。
//
// ⚠️ 服务小区有两种写法，都必须认（实测同一厂商不同平台就不一样）：
//
//	RM502Q-GL：`+QENG: "servingcell","NOCONN","LTE","FDD",460,11,…`（标记与数据同行）
//	RM500U-CNV：`+QENG: "servingcell","CONNECT"` 后再一行 `+QENG: "LTE","FDD",460,11,…`
//	            —— 数据行**不带 servingcell 标记**
//
// 只认带标记的行，就会把服务小区整个丢掉：表现是“服务小区/PCI/TAC/频段/信号全空”。
// 所以这里记住“上一个只有标记没有数据的 servingcell 行”，把紧随其后的数据行挂上去。
func ParseCellLines(resp []string) []CellInfo {
	var cells []CellInfo
	servingState := ""
	servingPending := false
	for _, line := range resp {
		if cell, ok := ParseCellLine(line); ok {
			cells = append(cells, cell)
			servingPending = false
			continue
		}
		if st, ok := servingHeaderOnly(line); ok {
			servingState, servingPending = st, true
			continue
		}
		if !servingPending {
			continue
		}
		if cell, ok := ParseServingDataLine(line); ok {
			if cell.State == "" {
				cell.State = servingState // 连接状态在上一行，补回去
			}
			cells = append(cells, cell)
			servingPending = false
		}
	}
	// 标准格式（QENG/CPSI/GTCCINFO/MONSC）一条都没解出时，再试**各厂商自有的**服务小区格式
	// （Thales/Foxconn 的 AT^DEBUG、Sierra 的 !GSTATUS、美格的 ^CELLINFO、高新兴的 +ZCELLINFO、
	//  Telit 的 #CAINFOEXT）—— 它们只认行里带自家标签，不会误吃别的机型。见 vendor_cell_extra.go。
	if len(cells) == 0 {
		cells = VendorServingCells(resp)
	}
	return cells
}

// servingHeaderOnly 判断是不是“只有标记、没有小区数据”的服务小区行，
// 返回其中的状态字段（如 CONNECT/SEARCH）。
func servingHeaderOnly(line string) (state string, ok bool) {
	m := reCellLine.FindStringSubmatch(line)
	if m == nil || !strings.EqualFold(strings.TrimSpace(m[1]), "servingcell") {
		return "", false
	}
	f := common.SplitATFields(m[2])
	if len(f) == 0 {
		return "", true
	}
	// 首字段是制式 = 数据同行，交给 ParseCellLine 处理，不算“只有标记”
	if isTechToken(f[0]) {
		return "", false
	}
	return strings.ToUpper(strings.TrimSpace(f[0])), true
}

// ParseServingDataLine 解析不带 servingcell 标记的服务小区数据行
// （形如 `+QENG: "LTE","FDD",460,11,3FA6836,140,1850,3,…`）。
// ⚠️ 这种写法里**制式是被正则当成"标签"抓走的**（`"LTE"` 在冒号后面第一格），
// 所以拼回字段序列时要把标签补回最前面，否则会从 "FDD" 开始解析、整行作废。
// 另外首字段必须是制式，否则不认 —— 否则会把 `+QENG: "NR5G-NSA",0,0,0,…`
// 这种"全 0 占位行"当成服务小区。
func ParseServingDataLine(line string) (CellInfo, bool) {
	m := reCellLine.FindStringSubmatch(line)
	if m == nil {
		return CellInfo{}, false
	}
	tag := strings.ToLower(strings.TrimSpace(m[1]))
	// 邻区行的标签是 neighbourcell*，首字段也是制式 —— 必须先排掉，
	// 否则邻区会被错认成服务小区。
	if strings.HasPrefix(tag, "neighbourcell") {
		return CellInfo{}, false
	}
	tech := strings.TrimSpace(m[1])
	f := common.SplitATFields(m[2])
	if !isTechToken(tech) && (len(f) == 0 || !isTechToken(f[0])) {
		return CellInfo{}, false
	}
	return ParseServingCell(tech+","+m[2], line)
}

// ParseServingCellSignal 从服务小区行里取 RSRP/RSRQ/SINR（**不限制制式**）。
//
// 与 ParseServingNRSignal 的区别：
//  1. 不要求 tech == NR —— LTE 驻网时（纯 LTE 模组 / NSA 当下挂在 LTE 上）
//     RSRP/RSRQ/SINR 同样在 +QENG:"servingcell" 里，不兜这一下就是“信号全是空的”；
//  2. 两种服务小区写法都认：标记与数据同行的，以及“标记行 + 数据行”分开的
//     （后者传进来的单行没有 servingcell 标记，靠 ParseServingDataLine 认）。
func ParseServingCellSignal(line string) (rsrp, rsrq, sinr int, ok bool) {
	cell, parsed := ParseCellLine(line)
	if !parsed {
		cell, parsed = ParseServingDataLine(line)
	}
	if !parsed || !cell.Serving || cell.RSRP == 0 {
		return 0, 0, 0, false
	}
	return cell.RSRP, cell.RSRQ, cell.SINR, true
}

// ParseCESQ 解析 3GPP AT+CESQ 应答（标准指令，覆盖面比厂商私有指令广）。
//
//	+CESQ: <rxlev>,<ber>,<rscp>,<ecno>,<rsrq>,<rsrp>[,<rssi>,…]
//
// 换算（TS 27.007 §8.69）：
//
//	rsrq 0~34  → (v-39)/2 dB（99/255 = 未上报）
//	rsrp 0~97  → v-140 dBm（255 = 未上报）
//
// 实测 RM500U-CNV：`+CESQ: 99,99,255,255,16,25,…` → RSRP=-115dBm / RSRQ=-11dB，
// 与同一次 AT+QENG="servingcell" 的 -115 / -12 一致 ✓
// （LTE 模组的 rxlev/rscp/ecno 恒为 99/255，只有后两个字段有用）
func ParseCESQ(resp []string) (rsrp, rsrq int, ok bool) {
	rsrp, rsrq, _, ok = ParseCESQSignal(resp)
	return rsrp, rsrq, ok
}

// ParseCESQSignal 解析 AT+CESQ 并连 SINR 一起返回（认不出返回 ok=false）。
//
// 除 3GPP 的 6 格写法外，还认**广和通扩展的第 7~9 格**（FM350 AT Commands §11.1.2）：
//
//	+CESQ: <rxlev>,<ber>,<rscp>,<ecno>,<rsrq>,<rsrp>,<ss_rsrq>,<ss_rsrp>,<ss_sinr>
//
// 前 6 格是"当前驻留制式"的（驻留在 NR 上时 rsrq/rsrp 恒为 255 = 未上报），
// 后 3 格是 NR 的 SSB 测量值。换算（同一份手册，注意 NR 与 LTE 的基准不同）：
//
//	ss_rsrp 0~126 → v-157 dBm（0 = < -156dBm，125 = -32~-31dBm，255 = 未上报）
//	ss_rsrq 0~126 → v*0.5-43.5 dB（0 = < -43dB，126 = 19.5~20dB，255 = 未上报）
//	ss_sinr 0~127 → v*0.5-23.5 dB（0 = < -23dB，127 = 40dB+，255 = 未上报）
//
// 只有前 6 格的 rsrq/rsrp **都**是"未上报"时才用扩展的三格，
// 所以其他模组（如 RM500U 的 `…,16,25,255,255,255`）行为完全不变。
func ParseCESQSignal(resp []string) (rsrp, rsrq, sinr int, ok bool) {
	for _, line := range resp {
		u := strings.ToUpper(line)
		i := strings.Index(u, "CESQ:")
		if i < 0 {
			continue
		}
		f := strings.FieldsFunc(line[i+len("CESQ:"):], func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\r'
		})
		if len(f) < 6 {
			continue
		}
		lteRsrq, err1 := strconv.Atoi(strings.TrimSpace(f[4]))
		lteRsrp, err2 := strconv.Atoi(strings.TrimSpace(f[5]))
		if err1 != nil || err2 != nil {
			continue
		}
		if lteRsrp >= 0 && lteRsrp <= 97 {
			rsrp = lteRsrp - 140
		}
		if lteRsrq >= 0 && lteRsrq <= 34 {
			rsrq = (lteRsrq - 39) / 2 // 负值向零取整：16 → -11（真值 -11.5）
		}
		// 前 6 格的两项都"未上报"→ 试试广和通扩展的 NR 三格（0 = 未上报）
		if rsrp == 0 && rsrq == 0 && len(f) >= 9 {
			if v, err := strconv.Atoi(strings.TrimSpace(f[7])); err == nil && v > 0 && v <= 126 {
				rsrp = v - 157
			}
			if v, err := strconv.Atoi(strings.TrimSpace(f[6])); err == nil && v > 0 && v <= 126 {
				rsrq = int(math.Round(float64(v)*0.5 - 43.5))
			}
			if v, err := strconv.Atoi(strings.TrimSpace(f[8])); err == nil && v > 0 && v <= 127 {
				sinr = int(math.Round(float64(v)*0.5 - 23.5))
			}
		}
		if rsrp == 0 && rsrq == 0 {
			continue
		}
		return rsrp, rsrq, sinr, true
	}
	return 0, 0, 0, false
}

// ParseServingNRSignal 从 NR servingcell 行解析 RSRP/RSRQ/SINR，
// 供各厂商 ModemCommand.ParseNRSignal 复用。
func ParseServingNRSignal(line string) (rsrp, rsrq, sinr int, ok bool) {
	cell, parsed := ParseCellLine(line)
	if !parsed || !cell.Serving || cell.Tech != iface.CellTechNR || cell.RSRP == 0 {
		return 0, 0, 0, false
	}
	return cell.RSRP, cell.RSRQ, cell.SINR, true
}

// IsSignalReportLine 判断是否为"信号上报"行（+CPSI / +QCSQ）。
// 只对这两类行做量纲解析，避免把邻区行（+QENG: "neighbourcell"）或
// 短信行里的负数误当成服务小区信号。
func IsSignalReportLine(line string) bool {
	u := strings.ToUpper(strings.TrimSpace(line))
	return strings.HasPrefix(u, "+CPSI:") || strings.HasPrefix(u, "+QCSQ:")
}

// ParseSignalByDimension 在一行里按"量纲合理性"找出 RSRP 并解析信号值。
//
// 用于 +CPSI / +QCSQ 这类没有官方文档、且各固件字段个数不一致的上报：
// 先找第一个落在 RSRP 区间（-156 ~ -44 dBm）的字段作为 RSRP，
// 再用 ApplySignalFields 区分后面的 RSRQ / RSSI / SINR。
//
// 关键性质：只有"负数"才可能落进 RSRP 区间，所以十六进制小区 ID、
// 频点号、PCI 这些非负字段不会被误判；找不到 RSRP 就整体放弃。
func ParseSignalByDimension(line string) (rsrp, rsrq, sinr int, ok bool) {
	f := common.SplitATFields(line)
	idx := FindRSRPToken(f, 0)
	if idx < 0 {
		return 0, 0, 0, false
	}
	cell := &CellInfo{}
	ApplySignalFields(cell, f, idx)
	if cell.RSRP == 0 {
		return 0, 0, 0, false
	}
	return cell.RSRP, cell.RSRQ, cell.SINR, true
}

// ParseCPSICell 解析 +CPSI 上报的服务小区。
//
// 兼容两套字段顺序（每项都做范围校验，任何一项不合法就整体放弃 ——
// 宁可不显示也不显示错的数据）：
//
//	写法① 频段是裸数字（MT5700/MTK 系实测）：
//	  +CPSI: <tech>,<state>,<PLMN>,<cellID>,<ARFCN>,<PCI>,<band>,<bw>,[...],<rsrp>,<rsrq>,...
//	  +CPSI: NR5G,Online,460-11,0x1051E8F2,627264,504,78,100,-,-95,-12,-7,13
//
//	写法② 频段带名字（SIMCom SIM8200EA-M2 实测，字段位置以频率名为锚点定位）：
//	  +CPSI: NR5G_SA,Online,460-11,0xCFA102,55735066633,52,NR5G_BAND78,627264,-1000,-120,25
func ParseCPSICell(line string) (CellInfo, bool) {
	m := reCPSI.FindStringSubmatch(line)
	if m == nil {
		return CellInfo{}, false
	}
	f := common.SplitATFields(m[1])
	// 字段数下限放到 4（NR5G_NSA 那套只有 <tech>,<PCellID>,<Band>,<arfcn>,… 共 7 个），
	// 具体的合法性交给各自的解析函数校验
	if len(f) < 4 {
		return CellInfo{}, false
	}
	tech := NormalizeTech(f[0])
	if tech != iface.CellTechLTE && tech != iface.CellTechNR {
		return CellInfo{}, false
	}
	// 两套写法的字段顺序不同，必须分开走：
	// 拿写法②的行当写法①解析（比如把 TAC 当 cellID、把 CellID 当频点）只会得到错数据。
	if cpsiBandNameIndex(f) >= 0 {
		return parseCPSIBandNameLayout(f, tech, line)
	}
	if len(f) < 8 {
		return CellInfo{}, false
	}
	// CellID 沿用原样十六进制字符串（与 +QENG 解析保持一致）；
	// 部分固件带 0x 前缀，统一去掉前缀并转大写
	cellID := strings.ToUpper(strings.TrimSpace(f[3]))
	cellID = strings.TrimPrefix(strings.TrimPrefix(cellID, "0X"), "0x")
	if cellID == "" || cellID == "-" {
		return CellInfo{}, false
	}
	if _, ok := common.ParseATInt(cellID); !ok {
		return CellInfo{}, false
	}
	earfcn, okArfcn := common.IntAt(f, 4)
	pcid, okPCI := common.IntAt(f, 5)
	band, okBand := common.IntAt(f, 6)
	if !okArfcn || !okPCI || !okBand {
		return CellInfo{}, false
	}
	// 范围校验：PCI 0~1007、频点为正、band 1~256（LTE 1~66 / NR 1~261 的保守交集）
	if pcid < 0 || pcid > 1007 || earfcn <= 0 || band < 1 || band > 256 {
		return CellInfo{}, false
	}
	cell := CellInfo{
		Tech:    tech,
		State:   strings.TrimSpace(f[1]),
		CellID:  cellID,
		EARFCN:  earfcn,
		PCID:    pcid,
		Band:    band,
		Serving: true,
		Raw:     line,
	}
	// PLMN：可能是 460-11 或 46011
	plmn := strings.ReplaceAll(strings.TrimSpace(f[2]), "-", "")
	if len(plmn) >= 5 {
		cell.MCC, cell.MNC = plmn[:3], plmn[3:]
		cell.Operator = cell.OperatorName()
	}
	cell.RSRP, cell.RSRQ, cell.SINR, _ = ParseSignalByDimension(line)
	return cell, cell.RSRP != 0
}

// cpsiBandNameIndex 找出"频率字段带名字"（NR5G_BAND78 / EUTRAN-BAND3）的下标；没有返回 -1。
func cpsiBandNameIndex(f []string) int {
	for i, v := range f {
		if reCPSIBandName.MatchString(strings.ToUpper(strings.TrimSpace(v))) {
			return i
		}
	}
	return -1
}

// parseCPSIBandNameLayout 解析"频率字段带名字"的 +CPSI 写法（实测 SIMCom SIM8200EA-M2）。
//
// 字段定义来自官方手册 SIM8200_Series_AT_Command_Manual_V1.01 §4.2.13（p.96~98）：
//
//	LTE（在 LTE 小区上，含 EN-DC 模式的第一行）：
//	  +CPSI: LTE,<OperationMode>,<MCC>-<MNC>,<TAC>,<SCellID>,<PCellID>,<FrequencyBand>,<earfcn>,<dlbw>,<ulbw>,<RSRQ>,<RSRP>,<RSSI>,<RSSNR>
//	  +CPSI: LTE,Online,460-11,0x404A,67166750,155,EUTRAN-BAND5,2452,3,3,-113,-1035,-755,4
//
//	NR5G_SA（独立组网）：
//	  +CPSI: NR5G_SA,<OperationMode>,<MCC>-<MNC>,<TAC>,<SCellID>,<PCellID>,<FrequencyBand>,<earfcn><RSRP>,<RSRQ>,<SNR>
//	  +CPSI: NR5G_SA,Online,460-11,0xCFA102,55735066633,521,NR5G_BAND78,627264,-1080,-140,20
//
//	NR5G_NSA（EN-DC 的第二行，**没有** PLMN/TAC/CellID）：
//	  +CPSI: NR5G_NSA,[<PCellID>,<FrequencyBand>,<earfcn/ssb>,<RSRP>,<RSRQ>,<SNR>]
//
// 以"频率名"为锚点定位：TAC / CellID / PCI 在它前面，频点在它后面，
// 这样即使固件多报或少报字段，也不会把数据摆到错的位置。
func parseCPSIBandNameLayout(f []string, tech iface.CellTech, line string) (CellInfo, bool) {
	k := cpsiBandNameIndex(f)
	if k < 1 || k+1 >= len(f) {
		return CellInfo{}, false
	}
	m := reCPSIBandName.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(f[k])))
	if m == nil {
		return CellInfo{}, false
	}
	band, err := strconv.Atoi(m[1])
	if err != nil || band < 1 || band > 256 {
		return CellInfo{}, false
	}
	pcid, okPCI := common.IntAt(f, k-1)
	earfcn, okArfcn := common.IntAt(f, k+1)
	if !okPCI || !okArfcn || pcid < 0 || pcid > 1007 || earfcn <= 0 {
		return CellInfo{}, false
	}
	cell := CellInfo{
		Tech:    tech,
		State:   strings.TrimSpace(f[1]),
		PCID:    pcid,
		EARFCN:  earfcn,
		Band:    band,
		Serving: true,
		Raw:     line,
	}
	if k >= 3 {
		// 完整布局：<TAC(hex)>,<SCellID(十进制)>,<PCellID>。
		// ⚠️ 必须自己剥掉 "0x" 再解析：strconv 只在 base=0 时才认前缀，
		// 直接拿 "0xCFA102" 去 ParseInt(...,16) 是失败的（ParseATInt 同理）。
		tacTok := strings.TrimSpace(f[k-3])
		if len(tacTok) < 5 || !strings.HasPrefix(strings.ToLower(tacTok), "0x") {
			return CellInfo{}, false
		}
		tacNum, errTAC := strconv.ParseInt(tacTok[2:], 16, 64)
		if errTAC != nil || tacNum < 0 || tacNum > 0xFFFFFF {
			return CellInfo{}, false
		}
		cell.TAC = int(tacNum)
		// CellID：这个固件报的是**十进制**小区标识（NR 是 NCI，LTE 是 ECI）。
		// 已双向验证：55735066633 ↔ 0xCFA11A009（NR）、67166750 ↔ 0x400E21E（LTE B5，
		// 与同一设备上其他模组报的十六进制值完全一致）—— 所以按十进制转成十六进制，
		// 与其他模组的 CellID 展示风格保持一致；本来就带 A~F 的原样保留。
		cellID, okID := normalizeCellIDToken(f[k-2])
		if !okID {
			return CellInfo{}, false
		}
		cell.CellID = cellID
		// PLMN：可能是 460-11 或 46011
		plmn := strings.ReplaceAll(strings.TrimSpace(f[2]), "-", "")
		if len(plmn) >= 5 {
			cell.MCC, cell.MNC = plmn[:3], plmn[3:]
			cell.Operator = cell.OperatorName()
		}
	} else {
		// NR5G_NSA：<PCellID>,<FrequencyBand>,<earfcn/ssb>,…（没有 PLMN / TAC / SCellID）
		if !IsNRSA(f[0]) {
			return CellInfo{}, false
		}
	}
	if tech == iface.CellTechLTE {
		// <dlbw>,<ulbw>,<RSRQ>,<RSRP>,<RSSI>,<RSSNR>
		cell.RSRQ, cell.RSRP, cell.RSSI, cell.SINR = parseCPSILTESignal(f, k+2)
	} else {
		// <RSRP>,<RSRQ>,<SNR>
		cell.RSRP, cell.RSRQ, cell.SINR = parseCPSISignalTrio(f, k+2)
	}
	// 结构没问题就算解析成功（信号可能没上报，界面照旧展示小区信息）
	return cell, true
}

// normalizeCellIDToken 规范化 +CPSI 的 CellID：
//   - 纯数字（十进制小区标识）→ 转十六进制大写（本项目的 CellID 约定是十六进制）；
//   - 已带 A~F（固件直接报十六进制）→ 去前缀后大写；
//   - 空 / "-" / 含其他字符 → 返回 false（宁可不显示也不瞎猜）。
func normalizeCellIDToken(s string) (string, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0X"), "0x")
	if s == "" || s == "-" {
		return "", false
	}
	digits, hexLike := true, true
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
			digits = false
		default:
			hexLike = false
		}
	}
	if !hexLike {
		return "", false
	}
	if digits {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil || v < 0 {
			return "", false
		}
		return strings.ToUpper(strconv.FormatInt(v, 16)), true
	}
	return strings.ToUpper(s), true
}

// cpsiScale10 判断一组信号值是不是 ×10 的整数（SIM8200 就是这么上报的）。
//
// 判据：只要有一个值的绝对值超过 200 就整体除以 10 —— RSRQ/RSSI/SINR 都不可能到 ±200，
// 而 RSRP 按 ×10 上报时至少是 440（-44 dBm × 10），所以这条判据不会误判。
func cpsiScale10(vals []int) bool {
	for _, v := range vals {
		if v > 200 || v < -200 {
			return true
		}
	}
	return false
}

// parseCPSILTESignal 解析 LTE 尾部信号字段（手册 §4.2.13）：
//
//	<dlbw>,<ulbw>,<RSRQ>,<RSRP>,<RSSI>,<RSSNR>
//
// 实测（同一时刻交叉验证）：`...,2452,3,3,-113,-1035,-755,4`
// → RSRQ -11.3 dB / RSRP -103.5 dBm / RSSI -75.5 dBm / RSSNR 0.4 dB，
// 其中 RSSI -75.5 dBm 与同一时刻 AT+CSQ 的 19（-113+2×19 = -75 dBm）对得上 ✓
// （本函数返回 RSRQ/RSRP/RSSI/SINR，SINR 取 <RSSNR>；界面按整数显示，所以小数会截断）
func parseCPSILTESignal(f []string, start int) (rsrq, rsrp, rssi, rssnr int) {
	if start < 0 || len(f)-start < 6 {
		return 0, 0, 0, 0
	}
	vals := make([]int, 0, 6)
	for i := start; i < start+6; i++ {
		v, ok := common.IntAt(f, i)
		if !ok {
			return 0, 0, 0, 0 // 有一项没上报（"-"）就整体放弃，避免串位
		}
		vals = append(vals, v)
	}
	// 前两个是带宽（dlbw/ulbw），不参与换算
	sig := vals[2:]
	if cpsiScale10(sig) {
		for i := range sig {
			sig[i] /= 10
		}
	}
	rsrq, rsrp, rssi, rssnr = sig[0], sig[1], sig[2], sig[3]
	if rsrp < -156 || rsrp > -44 {
		rsrp = 0
	}
	if rsrq < -34 || rsrq > 0 {
		rsrq = 0
	}
	if rssi < -113 || rssi > -25 {
		rssi = 0
	}
	if rssnr < -30 || rssnr > 45 {
		rssnr = 0
	}
	return rsrq, rsrp, rssi, rssnr
}

// parseCPSISignalTrio 取"频率名/频点"之后的三个信号值：RSRP / RSRQ / SINR。
//
// SIM8200 这三个值是 **×10 的整数**（-1000/-120/25 ↔ -100.0 dBm / -12.0 dB / 2.5 dB）。
// 第三个值归为 SINR：RSRP/RSRQ 必为负，观测到的第三个是正数（25），不可能是 RSSI。
func parseCPSISignalTrio(f []string, start int) (rsrp, rsrq, sinr int) {
	if start < 0 || len(f)-start < 3 {
		return 0, 0, 0
	}
	vals := make([]int, 0, 3)
	for i := start; i < start+3; i++ {
		v, ok := common.IntAt(f, i)
		if !ok {
			return 0, 0, 0 // 有一项没上报（"-"）就整体放弃，避免把 RSRQ 当成 RSRP 摆上去
		}
		vals = append(vals, v)
	}
	if cpsiScale10(vals) {
		for i := range vals {
			vals[i] /= 10
		}
	}
	rsrp, rsrq, sinr = vals[0], vals[1], vals[2]
	// 量纲校验：不在合理区间就当没上报
	if rsrp < -156 || rsrp > -44 {
		rsrp = 0
	}
	if rsrq < -34 || rsrq > 0 {
		rsrq = 0
	}
	if sinr < -30 || sinr > 45 {
		sinr = 0
	}
	return rsrp, rsrq, sinr
}

// ParseOperator 从 AT+COPS? 应答中提取运营商名称（模组未返回时为空）。
// 未注册或 SIM 无法解析运营商名时，模组会上报 "????" 这类占位符，
// 此处视为无效并返回空串，交由上层用 PLMN 推断中文名兜底。
func ParseOperator(resp []string) string {
	for _, line := range resp {
		m := ReCOPS.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := strings.TrimSpace(m[1])
		if name == "" || strings.Trim(name, "?") == "" {
			continue
		}
		return name
	}
	return ""
}

// SplitATFields 已搬到 modemd/module/common（见 module/common/atutil.go）。
// kernel 保留同名别名，见 atutil_alias.go。

// NormalizeTech 将模组上报的接入技术 token 归一化为 iface.CellTech。
//
// 移远/广和通在 SA/NSA 场景上报的是 "NR5G-SA" / "NR5G-NSA" / "NR5G"，
// 芯讯通 SIM8200 的 +CPSI 用的是下划线写法（"NR5G_SA"），还有空格写法 ——
// 不归一化会被当作未知技术，整行小区信息被丢弃（表现为制式/频率/信号全空）。
func NormalizeTech(tok string) iface.CellTech {
	s := strings.ToUpper(strings.TrimSpace(tok))
	s = strings.NewReplacer("_", "-", " ", "-").Replace(s)
	switch s {
	case "LTE", "LTE-A", "LTE-A-PRO":
		return iface.CellTechLTE
	case "NR", "NR5G", "NR5G-SA", "NR5G-NSA":
		return iface.CellTechNR
	case "WCDMA", "UMTS", "HSPA":
		return iface.CellTechWCDMA
	case "GSM", "GPRS", "EDGE":
		return iface.CellTechGSM
	}
	return iface.CellTech(strings.ToUpper(strings.TrimSpace(tok)))
}

// IsNRSA 判断是否为 NR5G-SA / NR5G-NSA 这类显式组网 token（下划线/空格写法同样算）。
// 此类报文在 cellID 之后多出一个 PCI 字段（顺序为 cellID,PCI,TAC,ARFCN,band）。
func IsNRSA(tok string) bool {
	s := strings.ToUpper(strings.TrimSpace(tok))
	s = strings.NewReplacer("_", "-", " ", "-").Replace(s)
	return s == "NR5G-SA" || s == "NR5G-NSA"
}

// isTechToken 判断是否为接入技术 token
func isTechToken(s string) bool {
	switch NormalizeTech(s) {
	case iface.CellTechLTE, iface.CellTechNR, iface.CellTechWCDMA, iface.CellTechGSM:
		return true
	}
	return false
}

// isDuplexToken 判断是否为双工模式 token
func isDuplexToken(s string) bool {
	switch strings.ToUpper(s) {
	case "FDD", "TDD", "HD-FDD":
		return true
	}
	return false
}

// ParseATInt / atIntAt / AtIntOr0 已搬到 modemd/module/common
// （ParseATInt / IntAt / AtIntOr0）；kernel 保留同名别名，见 atutil_alias.go。

// FindRSRPToken 从 start 起找第一个落在 RSRP 量纲区间（-156 ~ -44 dBm）的字段，
// 找不到返回 -1。
func FindRSRPToken(f []string, start int) int {
	for i := start; i < len(f); i++ {
		if v, ok := common.ParseATInt(f[i]); ok && v >= -156 && v <= -44 {
			return i
		}
	}
	return -1
}

// ApplySignalFields 从 idx（RSRP 所在下标）起解析四个信号值，
// 按“量纲合理性”定位而不是硬编码下标 ——
// 不同固件在频段之后附加的字段个数不一致（有无 RSSI 等），硬编码会串位。
//
//   - RSRP: -156 ~ -44 dBm
//   - RSRQ: -43 ~ 20 dB
//   - RSSI: -113 ~ -25 dBm（比 RSRP 强，据此和 SINR 区分）
//   - SINR: 直接取剩余那个值
func ApplySignalFields(cell *CellInfo, f []string, idx int) {
	cell.RSRP, _ = common.ParseATInt(f[idx])
	// RSRQ：紧随 RSRP 且量纲合法
	if v, ok := common.IntAt(f, idx+1); ok && v >= -43 && v <= 20 {
		cell.RSRQ = v
	}
	// LTE 服务小区在 RSRP/RSRQ 之后依次是 RSSI、SINR；
	// 部分固件（及 NR）只给 SINR，其值通常大于 -25，据此区分。
	if v, ok := common.IntAt(f, idx+2); ok {
		if v >= -113 && v <= -25 {
			cell.RSSI = v
			if s, ok2 := common.IntAt(f, idx+3); ok2 {
				cell.SINR = s
			}
		} else {
			cell.SINR = v
		}
	}
}

// iface.BandFromEARFCN 已搬到 modemd/iface（厂商的锁网/小区解析也要用）。
// kernel 保留同名入口，见 iface_alias.go。

// FillNeighbourPLMN 用服务小区的 PLMN/运营商补齐邻小区信息。
//
// 邻小区（neighbourcell）报文中不携带 MCC/MNC，若原样返回，附近基站列表里
// 所有邻小区都会缺少运营商信息。这里以第一个上报了 PLMN 的服务小区为基准
// 补齐；邻小区若已自带 PLMN（部分固件会上报），则保持自身值不被覆盖。
func FillNeighbourPLMN(serving, neighbours []CellInfo) {
	if len(serving) == 0 || len(neighbours) == 0 {
		return
	}
	var mcc, mnc, op string
	for i := range serving {
		if serving[i].MCC != "" && serving[i].MNC != "" {
			mcc, mnc = serving[i].MCC, serving[i].MNC
			op = serving[i].Operator
			break
		}
	}
	if mcc == "" {
		return
	}
	if op == "" {
		op = OperatorName(mcc, mnc)
	}
	for i := range neighbours {
		if neighbours[i].MCC == "" || neighbours[i].MNC == "" {
			neighbours[i].MCC = mcc
			neighbours[i].MNC = mnc
			if neighbours[i].Operator == "" {
				neighbours[i].Operator = op
			}
		}
	}
}

// MarkIntraFrequency 按"频点是否与服务小区相同"补上同频邻区标记。
//
// 只有移远/广和通的 `+QENG`/`+FENG` 会在标签里写 intra/inter；鼎桥 `^MONNC`、
// 广和通 `AT+GTCCINFO?` 这类应答**没有**同频/异频标志，不补的话界面上所有邻区
// 都会被写成"异频邻区"（实测 MT5700：服务小区与邻区频点都是 627264）。
// 同频的定义就是同一个中心频点 —— 频点未知（0）时不推断。
func MarkIntraFrequency(serving, neighbours []CellInfo) {
	if len(serving) == 0 || len(neighbours) == 0 {
		return
	}
	flag := map[int]bool{}
	for _, s := range serving {
		if s.EARFCN > 0 {
			flag[s.EARFCN] = true
		}
	}
	if len(flag) == 0 {
		return
	}
	for i := range neighbours {
		if neighbours[i].EARFCN > 0 && flag[neighbours[i].EARFCN] {
			neighbours[i].IntraCell = true
		}
	}
}

// SortCellsBySignal 按信号强度（RSRP）降序排序，未上报信号的排到最后。
func SortCellsBySignal(cells []CellInfo) {
	sort.SliceStable(cells, func(i, j int) bool {
		ri, rj := cells[i].RSRP, cells[j].RSRP
		if ri == 0 {
			return false
		}
		if rj == 0 {
			return true
		}
		return ri > rj
	})
}

// CellScanResult 一次基站扫描的完整结果
// （服务小区 + 附近基站 + 网络/运营商信息）
type CellScanResult struct {
	ModemID      int        `json:"modem_id"`
	ScannedAt    time.Time  `json:"scanned_at"`
	Serving      []CellInfo `json:"serving"`
	Neighbours   []CellInfo `json:"neighbours"`
	Cells        []CellInfo `json:"cells"`
	Count        int        `json:"count"`
	ServingCount int        `json:"serving_count"`
	NeighbourCnt int        `json:"neighbour_count"`
	Operator     string     `json:"operator,omitempty"`
	NetInfo      string     `json:"net_info,omitempty"`
	NetworkReg   string     `json:"network_reg,omitempty"`
	// —— 载波聚合（CA）：AT+QCAINFO ——
	CA       []iface.CCInfo `json:"ca,omitempty"` // 分量载波：PCC + SSC
	CAActive bool           `json:"ca_active"`    // 是否真的聚合了（存在 SSC）
	// CACmds 本机实际使用的 CA 查询指令（界面空状态提示按它渲染，
	// 以前页面写死了移远的 AT+QCAINFO，换机型就不对）；CANote 是补充说明。
	CACmds []string `json:"ca_cmds,omitempty"`
	CANote string   `json:"ca_note,omitempty"`
}

// ---- from: cell/gtccinfo.go ----

//
// 广和通老 5G 机型（FM150 / FM160）用 `AT+FENG="servingcell"`，格式与移远 +QENG 一致
// （所以 FibocomCommand.ParseNRSignal 直接复用了 QENG 的解析）。
// 但 FM650-CN（固件 86600.1000.00.05.17.11）**不认 FENG**（回 ERROR），也不认
// AT+QENG / AT+QNWINFO / AT+CPSI? —— 它只认自家 `AT+GTCCINFO?`。
//
// 应答是"标签行 + 数据行"的成组写法：
//
//	AT+GTCCINFO?
//	+GTCCINFO:
//	NR service cell:
//	1,9,460,11,CFA102,CFA11C101,9AC80,1C0,78,100,61,47,47,76
//	NR neighbor cell:
//	2,9,,,,,9AC80,48,,41,41,64
//	2,9,,,,,99240,209,,47,47,67
//	OK
//
// 字段口径取自广和通官方集成脚本（luci-app-modem → fibocom.sh 的 fibocom_cell_info）：
//
//	 1 <cell type>  1 = 服务小区，2 = 邻区
//	 2 <state>
//	 3 MCC            4 MNC
//	 5 TAC（十六进制） 6 CellID / ECI / NCI（十六进制）
//	 7 ARFCN（十六进制）8 PCI（十六进制）
//	 9 band           10 带宽数字
//	11~14 信号（换算随制式不同，见下）
//
// ⚠️ 第 5~8 四个字段全是**十六进制**，而且经常长得像十进制（ARFCN "99240"、PCI "209"）。
// 用本项目通用的 AtIntOr0/parseATInt（先十进制、失败再按十六进制）会**读错一半**：
//
//	ARFCN "99240" 当十进制 = 99240，按十六进制 = 627264 ← 正解（n78 频点）
//	ARFCN "9AC80" 当十进制失败→十六进制 = 633984（其他 n78 频点）
//	PCI   "209"   当十进制 = 209，  按十六进制 = 521    ← 正解
//
// 这几个值都能和此前同基站 SIM8200 上报的 n78/627264/PCI521 对上，所以这里强制按 16 进制解析。
//
// 信号换算（同一份官方脚本，注意两套制式的公式并不一样）：
//
//	NR   : SINR = f11*0.5-23.5      RSRP = f13-157   RSRQ = (f14+1)*0.5-44   （RxLev = f12-157）
//	LTE  : RSSNR = f11/2            RSRP = f13-141   RSRQ = f14*0.5-20       （RxLev = f12-141）
//	WCDMA: f10 ECNO, f11 RSCP, f12 RAC, f13 RxLev(-121), f14 保留, f15 Ec/Io(*0.5-24.5)
//
// 实测核对（2026-09-29，FM650-CN / COM23，中国电信 460/11）：
//
//	1,9,460,11,CFA102,CFA11C101,9AC80,1C0,78,100,61,47,47,76
//	→ MCC=460 MNC=11 TAC=0xCFA102 CellID=CFA11C101 ARFCN=0x9AC80=633984 PCI=0x1C0=448
//	  band=78 带宽 100MHz RSRP=-110 RSRQ=-5.5 SINR=7.0
//
// 同基站的另一小区（ARFCN 627264 / PCI 521）此前被 SIM8200 报成 RSRP -108，两边一致 ✓
//
// ⚠️ FM350-GL（固件 81600.0000.00.29.22.40）用**同一条指令但另一种写法**：没有标签行，
// 制式写在数据行第 2 个字段（<rat>：9 = NR-RAN），服务/邻区看第 1 个字段（1/2）；
// 而且 **ARFCN / PCI 是十进制**。实测应答：
//
//		AT+GTCCINFO?
//		+GTCCINFO:
//		1,9,460,11,CFA103,CFA9DF601,176190,172,,,-14,60,60,30
//		 TAC(hex) CellID(hex) narfcn(dec) PCI(dec) band(空) bw(空) SINR RxLev RSRP RSRQ
//
//	  - band / bandwidth 这两格该固件**不填**（所以界面「频段」是空的，只有频点）；
//	  - narfcn 176190 按十进制 = 880.95 MHz（n5/n26 的 800MHz 下行），按十六进制是
//	    1532560（FR1 里不存在）→ 只能按十进制；
//	  - 协议位（SINR/RxLev/RSRP/RSRQ）与标签式口径一致，且已用同一时刻的 AT+CESQ
//	    交叉验证：SS-RSRP 索引 60 ↔ 60、SS-RSRQ 索引 30 ↔ 30 ✓
//	    （换算见 parseGTCCINFRowAs：SS-RSRP = 索引-157、SS-RSRQ = 索引*0.5-43.5）。

var (
	// 标签行：`NR service cell:` / `NR neighbor cell:`（实测固件用美式拼写）/
	// `LTE neighbour cell:`（英式也认）/ `LTE-NR service cell:`
	reGTCCLabel = regexp.MustCompile(`(?i)^\s*([A-Za-z0-9][A-Za-z0-9-]*)\s+(?:service|neighbou?r)\s+cell\s*:?\s*$`)
	// `+GTCCINFO:` 单独一行（后面才是标签行）
	reGTCCHead = regexp.MustCompile(`(?i)^\s*\+GTCCINFO\s*:?\s*$`)
)

// gtccRatTech 把 GTCCINFO 标签里的制式串归一成 iface.CellTech。
// dual=true 表示 EN-DC（LTE-NR）—— 标签下会**两行**数据：第一行是 LTE 锚点，第二行是 NR。
func gtccRatTech(rat string) (tech iface.CellTech, dual bool, ok bool) {
	switch strings.ToUpper(strings.TrimSpace(rat)) {
	case "NR", "NR5G", "NR5G-SA", "NR5G-NSA":
		return iface.CellTechNR, false, true
	case "LTE", "EUTRAN", "EMTC", "NB-IOT", "NBIOT", "NB":
		return iface.CellTechLTE, false, true
	case "LTE-NR", "NR-LTE", "EN-DC", "ENDC", "EN_DC":
		return iface.CellTechLTE, true, true
	case "WCDMA", "UMTS", "HSPA":
		return iface.CellTechWCDMA, false, true
	case "GSM", "GERAN":
		return iface.CellTechGSM, false, true
	}
	return "", false, false
}

// IsGTCCINFOHead 判断一行是不是 `+GTCCINFO:`（后面跟着 label + 数据行）。
func IsGTCCINFOHead(line string) bool { return reGTCCHead.MatchString(line) }

// IsGTCCINFOReport 判断一段应答里有没有 GTCCINFO 的内容（决定要不要走这套解析）。
func IsGTCCINFOReport(resp []string) bool {
	for _, line := range resp {
		if reGTCCHead.MatchString(line) {
			return true
		}
		if _, _, ok := ParseGTCCINFLabel(line); ok {
			return true
		}
	}
	return false
}

// ParseGTCCINFLabel 判断一行是不是 GTCCINFO 的标签行。
// 命中时返回标签里的制式串（原样，如 "NR" / "LTE-NR"）与是否服务小区。
func ParseGTCCINFLabel(line string) (rat string, serving bool, ok bool) {
	m := reGTCCLabel.FindStringSubmatch(line)
	if m == nil {
		return "", false, false
	}
	if _, _, valid := gtccRatTech(m[1]); !valid {
		return "", false, false
	}
	return strings.ToUpper(m[1]), strings.Contains(strings.ToLower(line), "service"), true
}

// gtccHex 严格按十六进制解析（空串/非法返回 0）。
func gtccHex(tok string) int {
	tok = strings.TrimSpace(strings.Trim(tok, `"`))
	tok = strings.TrimPrefix(strings.TrimPrefix(tok, "0x"), "0X")
	if tok == "" {
		return 0
	}
	v, err := strconv.ParseInt(tok, 16, 64)
	if err != nil {
		return 0
	}
	return int(v)
}

// gtccDec 按十进制解析（空串/非法返回 0）。
func gtccDec(tok string) int {
	v, err := strconv.Atoi(strings.TrimSpace(strings.Trim(tok, `"`)))
	if err != nil {
		return 0
	}
	return v
}

// PcidMax 各制式物理小区 ID 的上限（3GPP：NR 1008 个、LTE 504 个、UMTS PSC 512 个）。
func PcidMax(tech iface.CellTech) int {
	switch tech {
	case iface.CellTechNR:
		return 1007
	case iface.CellTechLTE:
		return 503
	case iface.CellTechWCDMA:
		return 511
	}
	return 65535 // GSM/未知：不设限制（BSIC 之类没有这么严的上限）
}

// gtccNum 按指定进制解析 ARFCN / PCI（hex=true 走十六进制，否则十进制）。
func gtccNum(tok string, hex bool) int {
	if hex {
		return gtccHex(tok)
	}
	return gtccDec(tok)
}

// gtccRound 四舍五入到整数 dB（模组给的是 0.5 dB 步进的索引）。
func gtccRound(f float64) int { return int(math.Round(f)) }

// gtccBand 把 GTCCINFO 的频段字段归一成频段号。
//
//	实测 FM650 服务小区行是**裸 band**（78）；FM160 服务小区行与两台的
//	"能力/偏好列表"（AT+GTACT?）都是 **500+band** 的写法（5078 → n78、5257 → n257）。
//	官方集成脚本的做法就是"去掉开头的 50"（`band="${band#*50}"`），这里照抄：
//	只有数字以 "50" 开头且去掉后还是合法频段号（>0）才换算 ——
//	这样裸 band（78）和以 50 开头的真频段号（n50 就是 "50"，去掉后是 0 → 不换算）都不会被误改。
//
// LTE 一侧同理：实测两台的列表都用 100+band（105 → B5），而 LTE 真实频段号最大 88，
// 所以 100..188 减 100 无歧义。
func gtccBand(tech iface.CellTech, tok string) int {
	s := strings.TrimSpace(strings.Trim(tok, `"`))
	n := gtccDec(s)
	if n <= 0 {
		return 0
	}
	switch tech {
	case iface.CellTechLTE:
		if n >= 100 && n-100 <= 88 {
			n -= 100
		}
	case iface.CellTechNR:
		if strings.HasPrefix(s, "50") && len(s) > 2 {
			if v, err := strconv.Atoi(s[2:]); err == nil && v > 0 {
				n = v
			}
		}
	}
	return n
}

// gtccBandwidth 把带宽数字换成 MHz（官方脚本口径：NR 的 0 表示 5MHz，其余就是 MHz；
// LTE 的 6 表示 1.4MHz，其余除以 5）。
func gtccBandwidth(tech iface.CellTech, tok string) int {
	s := strings.TrimSpace(strings.Trim(tok, `"`))
	if s == "" {
		// 空 = 未上报（实测 FM350 的服务小区行这两格就是空的）——
		// 不能当数字 0 去查表，否则 NR 会被显示成"带宽 5MHz"
		return 0
	}
	n := gtccDec(s)
	if n < 0 {
		return 0
	}
	switch tech {
	case iface.CellTechNR:
		if n == 0 {
			return 5
		}
		return n
	case iface.CellTechLTE:
		if n == 6 {
			return 1 // 1.4MHz，取整到 1（界面只做展示）
		}
		return n / 5
	}
	return 0
}

// gtccTechFromRatCode 把数据行自带的 <rat> 字段归一成制式。
//
// 手册（FM350 AT Commands §11.1.15）定义：0 = 无效网络、2 = WCDMA、4 = LTE、9 = NR-RAN。
// 实测 FM350-GL **不输出标签行**，制式就写在这个字段里（`1,9,460,11,…` 的 `9` 就是 NR）。
func gtccTechFromRatCode(tok string) (iface.CellTech, bool) {
	switch strings.TrimSpace(tok) {
	case "2":
		return iface.CellTechWCDMA, true
	case "4":
		return iface.CellTechLTE, true
	case "9":
		return iface.CellTechNR, true
	}
	return "", false
}

// gtccStyle 同一条指令在两代固件上的两套落地写法（都有实机依据，见文件头注释）。
type gtccStyle struct {
	// decNums ARFCN / PCI 按十进制解析，而不是十六进制：
	//   FM650 / FM160 实测是十六进制（"99240" = 627264、"209" = 521）；
	//   FM350 实测是十进制（176190 = 880.95MHz(n5)，按十六进制是 FR1 里不存在的频点）。
	decNums bool
	// bandInServing 只有服务小区行（14 格）才带 band/bandwidth：
	//   FM350 的邻区行是 12 格，第 9/10 格是 SS-SINR / RxLev（手册 §11.1.15），
	//   当频段/带宽读会得到"带宽 48MHz"这种假数据。
	bandInServing bool
	// sinrInNeighbour 邻区行（12 格）里也有 SS-SINR：FM350 的手册 NR 邻区行是
	//   `<…,narfcn,pci,ss_sinr,rxlev,ss_rsrp,ss_rsrq>`；而 FM650 的邻区行这一格是
	//   <bandwidth>（实测为空），当 SINR 读会把带宽当成信噪比。
	sinrInNeighbour bool
}

var (
	// 标签式：FM650-CN / FM160-EAU（十六进制、邻区行有 bandwidth 无 SINR）
	gtccStyleLabelled = gtccStyle{}
	// 无标签式：FM350-GL（十进制、邻区行有 SINR 无 bandwidth）
	gtccStyleLabelLess = gtccStyle{decNums: true, bandInServing: true, sinrInNeighbour: true}
)

// ParseGTCCINFRow 解析 GTCCINFO 的一条**数据行**（不含标签行）。
// rat 来自 ParseGTCCINFLabel；serving 表示这行属于服务小区还是邻区；
// nrRow 是该标签下的第几条数据行（EN-DC 时 0 = LTE 锚点、1+ = NR 腿，普通制式传 0）。
func ParseGTCCINFRow(rat string, serving bool, nrRow int, row string) (CellInfo, bool) {
	tech, dual, ok := gtccRatTech(rat)
	if !ok {
		return CellInfo{}, false
	}
	if dual && (nrRow >= 1 || !serving) {
		tech = iface.CellTechNR // EN-DC：第一行是 LTE 锚点，之后是 NR（邻区少见的 EN-DC 行也按 NR）
	}
	return parseGTCCINFRowAs(tech, serving, row, gtccStyleLabelled)
}

// ParseGTCCINFRowAuto 解析**没有标签行**的数据行（实测 FM350-GL 的写法）。
//
// 与标签式（FM650/FM160）的区别：
//   - 制式取自行内的 <rat> 字段（2/4/9），服务/邻区取自第 1 个字段（1/2）；
//   - ARFCN / PCI 是**十进制**、band/bandwidth 只在服务小区行里、邻区行带 SINR
//     （见 gtccStyleLabelLess）；
//   - TAC / CellID 仍是十六进制（实测 `CFA103` / `CFA9DF601` 带字母，只可能是十六进制）。
func ParseGTCCINFRowAuto(row string) (CellInfo, bool) {
	f := common.SplitATFields(row)
	if len(f) < 2 {
		return CellInfo{}, false
	}
	tech, ok := gtccTechFromRatCode(f[1])
	if !ok {
		return CellInfo{}, false
	}
	serving := strings.TrimSpace(f[0]) == "1"
	return parseGTCCINFRowAs(tech, serving, row, gtccStyleLabelLess)
}

// parseGTCCINFRowAs 按指定制式和写法解析数据行。
func parseGTCCINFRowAs(tech iface.CellTech, serving bool, row string, style gtccStyle) (CellInfo, bool) {
	raw := strings.TrimSpace(row)
	if raw == "" || !strings.Contains(raw, ",") {
		return CellInfo{}, false
	}
	f := common.SplitATFields(raw)
	if len(f) < 10 {
		return CellInfo{}, false
	}
	// <cell type> 只认 1（服务小区）/ 2（邻区），其它形状一律不当成小区行
	switch strings.TrimSpace(f[0]) {
	case "1", "2":
	default:
		return CellInfo{}, false
	}

	ci := CellInfo{
		Tech:    tech,
		Serving: serving,
		// ⚠️ 第 2 格是 `<rat>`（接入技术编号：0 无效 / 2 WCDMA / 4 LTE / 9 NR-RAN），
		// **不是**连接状态 —— 以前当成 State 填进去，界面「网络」卡片会显示"连接状态 9"。
		MCC:    strings.TrimSpace(f[2]),
		MNC:    strings.TrimSpace(f[3]),
		CellID: strings.ToUpper(strings.TrimSpace(f[5])),
		Raw:    raw,
	}
	// band / bandwidth：服务小区行（14 格）第 9/10 格才是它俩；
	// FM350 的邻区行（12 格）那两格是 SS-SINR / RxLev，硬读会得到假频段/假带宽。
	if len(f) >= 14 || !style.bandInServing {
		ci.Band = gtccBand(tech, f[8])
		ci.Bandwidth = gtccBandwidth(tech, f[9])
	}
	// TAC / CellID 都是十六进制（见文件头注释的实测依据）；
	// ARFCN / PCI 两家固件写法不同：标签式是十六进制，无标签式（FM350）是十进制
	ci.TAC = gtccHex(f[4])
	ci.EARFCN = gtccNum(f[6], !style.decNums)
	ci.PCID = gtccNum(f[7], !style.decNums)
	// PCI 必须落在协议允许的区间里，否则整行丢弃。
	// 实测 FM350-GL 的**邻区行**会报出 PCI 1711（NR 的 PCI 只到 1007）而且尾部三个
	// 信号字段是零填充的假值（`…,1711,,026,026,03` 与 `…,1711,,126,126,77` 交替出现，
	// 折算成 -131/-31 dBm 来回跳）—— 这种行整条不可信，宁可不显示，
	// 免得界面上出现「PCI 1711 · RSRP -31dBm」这种自相矛盾的邻区。
	if ci.PCID < 0 || ci.PCID > PcidMax(tech) {
		return CellInfo{}, false
	}

	switch tech {
	case iface.CellTechNR, iface.CellTechLTE:
		// 实测两种行长（服务小区 14 格、邻区 12 格）：
		//
		//	服务小区: 1,9,460,11,CFA102,CFA11C101,9AC80,1C0,78,100,61,47,47,76
		//	          …  band bw   SINR RxLev RSRP RSRQ
		//	邻区    : 2,9,,,,,9AC80,48,,41,41,64
		//	          …  band(空)  RxLev RSRP RSRQ      ← 少一格带宽、也少一格 SINR
		//
		// 所以**从行尾往前取**最稳：末格 = RSRQ、倒数第二 = RSRP、倒数第三 = RxLev、
		// 倒数第四（凑够 14 格才有）= SINR。硬编码下标会在邻区行上错位。
		if len(f) < 12 {
			break
		}
		n := len(f)
		rsrqIdx, rsrpIdx, rxlevIdx := gtccDec(f[n-1]), gtccDec(f[n-2]), gtccDec(f[n-3])
		if tech == iface.CellTechNR {
			// 索引 0 = 模组未上报（3GPP：RSRP 索引 0 表示 "小于 -156dBm"）
			if rsrpIdx > 0 {
				ci.RSRP = rsrpIdx - 157
			}
			if rsrqIdx > 0 {
				ci.RSRQ = gtccRound((float64(rsrqIdx)+1)*0.5 - 44)
			}
			if rxlevIdx > 0 {
				ci.RSSI = rxlevIdx - 157 // RxLev 借 RSSI 字段展示（NR 的 RSRP/RxLev 同基准）
			}
			// SS-SINR 固定在 RxLev 前一格（服务小区行第 11 格）。
			// 手册（§11.1.15 与 §11.1.2 同一张表）说它是 0~127 的**索引**，
			// dB = 索引*0.5-23.5；但实测 FM350-GL 直接给 dB（回 -14/-13，与同一时刻
			// AT+CESQ 的 ss_sinr 索引 21（= -13 dB）吻合，而索引不可能为负），
			// 所以按"负值就是已经换算好的 dB"处理 —— 两种固件都能对上。
			// 邻区行（12 格）里这一格只有无标签式固件（FM350）才有 SINR。
			if n >= 14 || (style.sinrInNeighbour && n >= 12) {
				switch sv := gtccDec(f[n-4]); {
				case sv > 0:
					ci.SINR = gtccRound(float64(sv)*0.5 - 23.5)
				case sv < 0:
					ci.SINR = sv
				}
			}
		} else {
			if rsrpIdx > 0 {
				ci.RSRP = rsrpIdx - 141
				ci.RSSI = rsrpIdx - 141 // LTE 的 RxLev 与 RSRP 同一索引、同一基准
			}
			if rsrqIdx > 0 {
				ci.RSRQ = gtccRound(float64(rsrqIdx)*0.5 - 20)
			}
			if n >= 14 {
				// LTE 第 11 格是 RSSNR（信噪比，官方脚本按 num/2 换算）
				ci.SINR = gtccDec(f[n-4]) / 2
			}
		}
	case iface.CellTechWCDMA:
		// WCDMA 行口径（官方脚本，本机未实测）：f10 ECNO; f11 RSCP; f12 RAC; f13 RxLev; f14 保留; f15 Ec/Io
		if len(f) >= 13 {
			ci.RSRP = gtccDec(f[10]) - 121 // RSCP 借 RSRP 位（界面按 RSRP 排序/判等级）
			ci.RSSI = gtccDec(f[12]) - 121
		}
		if len(f) >= 15 {
			ci.RSRQ = gtccRound(float64(gtccDec(f[14]))*0.5 - 24.5)
		}
	}

	// 信号值合理性校验：越界一律清零（宁可空白也不显示错的数据）
	ci.RSRP = clampSignal(ci.RSRP, -156, -25)
	ci.RSRQ = clampSignal(ci.RSRQ, -45, 25)
	ci.SINR = clampSignal(ci.SINR, -25, 45)
	ci.RSSI = clampSignal(ci.RSSI, -156, -25)
	// PLMN 全 0 / 空 = 模组还没解出小区身份（实测切小区瞬间会回 000,00）
	if ci.MCC == "" || ci.MCC == "000" {
		ci.MCC, ci.MNC = "", ""
	}
	ci.Operator = ci.OperatorName()
	return ci, true
}

// clampSignal 超出合理区间就清零（0 = 未上报）。
func clampSignal(v, lo, hi int) int {
	if v < lo || v > hi {
		return 0
	}
	return v
}

// ParseGTCCINFO 解析完整的 `AT+GTCCINFO?` 应答，返回服务小区与邻区列表。
// 认不出任何小区时两个列表都为空（调用方据此退回别的解析路径）。
//
// 兼容两种固件写法：
//
//	标签式（FM650 / FM160）：+GTCCINFO: / NR service cell: / 1,9,460,11,…（数据行靠标签行定性）
//	无标签式（FM350）：      +GTCCINFO: / 1,9,460,11,…              （数据行自带 <rat> 字段）
func ParseGTCCINFO(resp []string) (serving []CellInfo, neighbours []CellInfo) {
	var rat string
	servingLabel := false
	labelled := false // 当前数据段是不是"标签行 + 数据行"的写法
	nrRow := 0        // EN-DC 下同一标签内第几行（0 = LTE 锚点，1+ = NR）
	for _, line := range resp {
		if r, sv, ok := ParseGTCCINFLabel(line); ok {
			rat, servingLabel, nrRow = r, sv, 0
			labelled = true
			continue
		}
		if reGTCCHead.MatchString(line) || strings.TrimSpace(line) == "" || strings.EqualFold(line, "OK") {
			continue
		}
		var ci CellInfo
		var ok bool
		if labelled {
			ci, ok = ParseGTCCINFRow(rat, servingLabel, nrRow, line)
		} else {
			// 没有标签行：制式与服务/邻区都写在数据行里（实测 FM350-GL）
			ci, ok = ParseGTCCINFRowAuto(line)
		}
		if ok {
			nrRow++ // EN-DC 下第 0 行是 LTE 锚点、之后是 NR
			if ci.Serving {
				serving = append(serving, ci)
			} else {
				neighbours = append(neighbours, ci)
			}
			continue
		}
		// 标签行之后遇到不像数据的行（如 ERROR / 下一条指令的回显）→ 结束这一段
		rat, labelled = "", false
	}
	return serving, neighbours
}

// ---- from: cell/monsc.go ----

//
// 这台模组的网络类信息全在 `^` 私有命令里，移远/展锐那套（AT+QENG/CPSI/QCSQ/QNWINFO）
// **全部回 ERROR**（2026-09-30 实机核对）。手册：MT5700M-CN 5G 系列模组 AT 命令手册。
//
//	AT^MONSC   驻留小区（§13.9）
//	AT^MONNC   邻区（§13.10）
//	AT^HCSQ?   信号强度（§13.5，值是**索引**）
//
// 与其它厂商最大的不同：**MONSC/MONNC 直接给 dB 单位的 RSRP/RSRQ/SINR**（不是索引），
// 只有 ^HCSQ 给索引（>HCSQ 手册：rsrp 索引-140、sinr 索引*0.2-20、rsrq 索引*0.5-19.5）。
//
// 实机原始应答（中国电信 460/11，n78）：
//
//	AT^MONSC
//	^MONSC: NR,460,11,627264,1,CFA116002,115,CFA103,-78,-10,30
//	        RAT MCC MNC  ARFCN  SCS  CellID(hex) PCI(hex) TAC(hex) RSRP RSRQ SINR
//	AT^MONNC
//	^MONNC: NR,627264,114,-89,-11,12
//	        RAT ARFCN  PCI(hex) RSRP RSRQ SINR
//	AT^HCSQ?
//	^HCSQ: "NR",63,206,30          ← rsrp 索引 / sinr 索引 / rsrq 索引
//
// ⚠️ LTE 的字段顺序与 NR **不一样**（少 SCS、多一格 RSSI）：
//
//	^MONSC: LTE,460,01,1650,A54933,1F3,183D,-98,-5,-72     （手册 §13.9.5 例子）
//	                MCC MNC ARFCN CellID PCI TAC RSRP RSRQ RSSI
//	^MONNC: LTE,10000,1F,-82,-11,-1                         （手册 §13.10.5 例子，PCI 十进制）
// reMONSC / reMONNC / reHCSQ 匹配三种上报行（冒号后整段自己按逗号切）

const (
	monSCPrefix = "^MONSC:"
	monNCPrefix = "^MONNC:"
	hcsqPrefix  = "^HCSQ:"
)

// monFields 取 `^XXX:` 之后的字段（去空白与引号）。
func monFields(line, prefix string) ([]string, bool) {
	u := strings.ToUpper(strings.TrimSpace(line))
	i := strings.Index(u, prefix)
	if i < 0 {
		return nil, false
	}
	rest := strings.TrimSpace(line[i+len(prefix):])
	if rest == "" {
		return nil, false
	}
	return common.SplitATFields(rest), true
}

// monHex 十六进制字段（空/非法 → 0）。MT5700 的 PCI / TAC / CellID 都是十六进制。
func monHex(tok string) int {
	return gtccHex(tok)
}

// monHexOrDec 含 a~f 的按十六进制，纯数字的按十进制（LTE 邻区的 PCI 两种写法都出现过）
func monHexOrDec(tok string) int {
	t := strings.TrimSpace(strings.Trim(tok, `"`))
	if t == "" || t == "-" {
		return 0
	}
	low := strings.ToLower(t)
	if strings.ContainsAny(low, "abcdef") {
		return gtccHex(t)
	}
	return monDec(t)
}

// monDec 十进制字段（空/非法 → 0），"-" 也算空。
func monDec(tok string) int {
	t := strings.TrimSpace(tok)
	if t == "" || t == "-" {
		return 0
	}
	v, err := strconv.Atoi(t)
	if err != nil {
		return 0
	}
	return v
}

// ParseMONSC 解析 `^MONSC:` 驻留小区（NR / LTE / WCDMA）。
// 认不出返回 ok=false（调用方据此退回别的解析路径）。
func ParseMONSC(line string) (CellInfo, bool) {
	f, ok := monFields(line, monSCPrefix)
	if !ok || len(f) < 4 {
		return CellInfo{}, false
	}
	ci := CellInfo{Serving: true, Raw: strings.TrimSpace(line)}
	switch strings.ToUpper(f[0]) {
	case "NR":
		// <RAT>,<MCC>,<MNC>,<ARFCN-NR>,<SCS>,<CellID>,<PCI>,<TAC>,<RSRP>,<RSRQ>,<SINR>
		if len(f) < 11 {
			return CellInfo{}, false
		}
		ci.Tech = iface.CellTechNR
		ci.MCC, ci.MNC = strings.TrimSpace(f[1]), strings.TrimSpace(f[2])
		ci.EARFCN = monDec(f[3])
		ci.CellID = strings.ToUpper(strings.TrimSpace(f[5]))
		ci.PCID = monHex(f[6]) // 十六进制（手册 §13.9.3；实测 "115" = 277）
		ci.TAC = monHex(f[7])
		ci.RSRP = monDec(f[8])
		ci.RSRQ = monDec(f[9])
		ci.SINR = monDec(f[10])
	case "LTE":
		// <RAT>,<MCC>,<MNC>,<ARFCN>,<CellID>,<PCI>,<TAC>,<RSRP>,<RSRQ>,<RSSI>
		if len(f) < 10 {
			return CellInfo{}, false
		}
		ci.Tech = iface.CellTechLTE
		ci.MCC, ci.MNC = strings.TrimSpace(f[1]), strings.TrimSpace(f[2])
		ci.EARFCN = monDec(f[3])
		ci.CellID = strings.ToUpper(strings.TrimSpace(f[4]))
		ci.PCID = monHex(f[5])
		ci.TAC = monHex(f[6])
		ci.RSRP = monDec(f[7])
		ci.RSRQ = monDec(f[8])
		ci.RSSI = monDec(f[9])
	case "WCDMA":
		// <RAT>,<MCC>,<MNC>,<ARFCN>,<PSC>,<Cell_ID>,<LAC>,<RSCP>,<RXLEV>,<EC/N0>,…
		if len(f) < 9 {
			return CellInfo{}, false
		}
		ci.Tech = iface.CellTechWCDMA
		ci.MCC, ci.MNC = strings.TrimSpace(f[1]), strings.TrimSpace(f[2])
		ci.EARFCN = monDec(f[3])
		ci.PCID = monDec(f[4])
		ci.CellID = strings.ToUpper(strings.TrimSpace(f[5]))
		ci.TAC = monHex(f[6])
		ci.RSRP = monDec(f[7])
		ci.RSSI = monDec(f[8])
	default:
		return CellInfo{}, false
	}
	// 无效值哨兵（手册：NR 的 -1256 / -348 / -188 = -157*8 / -43.5*8 / -23.5*8）
	if ci.RSRP <= -1250 || ci.RSRP > 0 {
		ci.RSRP = 0
	}
	if ci.RSRQ <= -340 || ci.RSRQ > 30 {
		ci.RSRQ = 0
	}
	if ci.SINR <= -185 || ci.SINR > 60 {
		ci.SINR = 0
	}
	if ci.RSSI > 0 {
		ci.RSSI = 0
	}
	// 与其它厂商口径一致：异常值一律丢掉（宁可空白也不显示错的数据）
	ci.RSRP = clampSignal(ci.RSRP, -156, -25)
	ci.RSRQ = clampSignal(ci.RSRQ, -45, 25)
	ci.SINR = clampSignal(ci.SINR, -25, 45)
	ci.RSSI = clampSignal(ci.RSSI, -156, -25)
	if ci.MCC == "" || ci.MCC == "000" {
		ci.MCC, ci.MNC = "", ""
	}
	ci.Operator = ci.OperatorName()
	return ci, true
}

// ParseMONNC 解析 `^MONNC:` 邻区行（NR / LTE / GSM / WCDMA）。
// `^MONNC: NONE` 表示没有邻区（返回 ok=false）。
func ParseMONNC(line string) (CellInfo, bool) {
	f, ok := monFields(line, monNCPrefix)
	if !ok || len(f) < 3 {
		return CellInfo{}, false
	}
	ci := CellInfo{Raw: strings.TrimSpace(line)}
	switch strings.ToUpper(f[0]) {
	case "NR":
		// <ARFCN-NR>,<PCI hex>,<RSRP>,<RSRQ>,<SINR>
		if len(f) < 6 {
			return CellInfo{}, false
		}
		ci.Tech = iface.CellTechNR
		ci.EARFCN = monDec(f[1])
		ci.PCID = monHex(f[2])
		ci.RSRP = monDec(f[3])
		ci.RSRQ = monDec(f[4])
		ci.SINR = monDec(f[5])
	case "LTE":
		// <ARFCN>,<PCI>,<RSRP>,<RSRQ>,<RXLEV>
		// ⚠️ 手册的字段表写"十进制"，但它自己的例子是 `LTE,10000,1F,…`（"1F" 只可能是十六进制）
		// → 含 a~f 就按十六进制，纯数字按十进制（NR 那边手册明说十六进制，见下）。
		if len(f) < 6 {
			return CellInfo{}, false
		}
		ci.Tech = iface.CellTechLTE
		ci.EARFCN = monDec(f[1])
		ci.PCID = monHexOrDec(f[2])
		ci.RSRP = monDec(f[3])
		ci.RSRQ = monDec(f[4])
		ci.RSSI = monDec(f[5])
	case "WCDMA":
		// <ARFCN>,<PSC>,<RSCP>,<EC/N0>
		if len(f) < 5 {
			return CellInfo{}, false
		}
		ci.Tech = iface.CellTechWCDMA
		ci.EARFCN = monDec(f[1])
		ci.PCID = monDec(f[2])
		ci.RSRP = monDec(f[3])
		ci.RSRQ = monDec(f[4])
	case "GSM":
		// <BAND>,<ARFCN>,<BSIC>,<Cell_ID>,<LAC>,<RXLEV>
		if len(f) < 7 {
			return CellInfo{}, false
		}
		ci.Tech = iface.CellTechGSM
		ci.EARFCN = monDec(f[2])
		ci.PCID = monDec(f[3])
		ci.CellID = strings.ToUpper(strings.TrimSpace(f[4]))
		ci.TAC = monHex(f[5])
		ci.RSSI = monDec(f[6])
	default: // NONE / 未知
		return CellInfo{}, false
	}
	// 无效值哨兵 + 越界清零（与 ParseMONSC 同一套口径）
	if ci.RSRP <= -1250 {
		ci.RSRP = 0
	}
	if ci.RSRQ <= -340 {
		ci.RSRQ = 0
	}
	if ci.SINR <= -185 {
		ci.SINR = 0
	}
	ci.RSRP = clampSignal(ci.RSRP, -156, -25)
	ci.RSRQ = clampSignal(ci.RSRQ, -45, 25)
	ci.SINR = clampSignal(ci.SINR, -25, 45)
	ci.RSSI = clampSignal(ci.RSSI, -156, -25)
	if ci.PCID < 0 || ci.PCID > PcidMax(ci.Tech) {
		return CellInfo{}, false
	}
	return ci, true
}

// ParseHCSQ 解析 `^HCSQ:` 信号上报（§13.5）。
//
//	NR : ^HCSQ: "NR",<rsrp 索引>,<sinr 索引>,<rsrq 索引>
//	LTE: ^HCSQ: "LTE",<rssi 索引>,<rsrp 索引>,<sinr 索引>,<rsrq 索引>
//
// 换算按手册的参数表（0 表示"小于下限"，255 = 未知/不可测）：
//
//	rsrp 索引 0~97 → 索引-140 dBm；sinr 索引 0~251 → 索引*0.2-20 dB；rsrq 索引 0~34 → 索引*0.5-19.5 dB
//
// ⚠️ 实测它的 rsrq/sinr 与 ^MONSC 报的直接 dB 值**不完全一致**（同一时刻 MONSC -10/30，
// HCSQ 折算出 -4.5/21.2），所以上层只在 MONSC 拿不到值时才用它兜底（见 modem_lifecycle.go）。
func ParseHCSQ(line string) (rsrp, rsrq, sinr int, ok bool) {
	f, found := monFields(line, hcsqPrefix)
	if !found || len(f) < 3 {
		return 0, 0, 0, false
	}
	mode := strings.ToUpper(strings.Trim(strings.TrimSpace(f[0]), `"`))
	idx := func(i int) int {
		if i >= len(f) {
			return -1
		}
		return monDec(f[i])
	}
	switch mode {
	case "NR":
		if v := idx(1); v > 0 && v <= 126 {
			rsrp = v - 140
		}
		if v := idx(2); v >= 0 && v <= 251 {
			sinr = int(float64(v)*0.2 - 20 + 0.5) // 四舍五入到整数 dB
			if v == 0 {
				sinr = -20 // 0 = "小于 -20dB"（界面上给个下限值，不给 0=未上报）
			}
		}
		if v := idx(3); v > 0 && v <= 34 {
			rsrq = (v - 39) / 2
		}
	case "LTE":
		// <rssi>,<rsrp>,<sinr>,<rsrq>
		if v := idx(2); v > 0 && v <= 97 {
			rsrp = v - 140
		}
		if v := idx(3); v >= 0 && v <= 251 {
			sinr = int(float64(v)*0.2 - 20 + 0.5)
		}
		if v := idx(4); v > 0 && v <= 34 {
			rsrq = (v - 39) / 2
		}
	default: // "NOSERVICE" / "GSM" / "WCDMA"：本程序不用它算 RSRP/RSRQ/SINR
		return 0, 0, 0, false
	}
	if rsrp == 0 && rsrq == 0 && sinr == 0 {
		return 0, 0, 0, false
	}
	return rsrp, rsrq, sinr, true
}

// IsHCSQLine 判断是否为 ^HCSQ 上报行（URC 与查询应答都是这一行）。
func IsHCSQLine(line string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), hcsqPrefix)
}

// ParseMONNCLines 解析一整段 `AT^MONNC` 应答（一行一个邻区；`^MONNC: NONE` 会被忽略）。
func ParseMONNCLines(resp []string) []CellInfo {
	var out []CellInfo
	for _, line := range resp {
		if ci, ok := ParseMONNC(line); ok {
			out = append(out, ci)
		}
	}
	return out
}

// ---- from: cell/types.go ----

// 小区/扫描相关数据类型
// iface.CCInfo 已搬到 modemd/iface（厂商的 CA 实现要返回它）；kernel 保留同名别名。
