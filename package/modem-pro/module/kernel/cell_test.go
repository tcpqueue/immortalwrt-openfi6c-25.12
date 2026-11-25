package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"modemd/module/atparse"
	cellpkg "modemd/module/cell"
	"modemd/module/common"
	"modemd/module/fibocom"
	"modemd/module/quectel"
	"modemd/module/simcom"
	"strings"
	"testing"
	"time"
)

// ---- from: cell_test.go ----

// ---- from: cell_test.go ----
// TestParseServingLTENoconn 解析服务 LTE 小区（移远 RM502Q 实测格式）
// TestParseServingLTENoconn 解析服务 LTE 小区（移远 RM502Q 实测格式）

func TestParseServingLTENoconn(t *testing.T) {
	// 实测真实返回
	line := `+QENG: "servingcell","NOCONN","LTE","FDD",460,11,400E21E,155,2452,5,3,3,404A,-104,-16,-69,7,0,-,19`
	ci, ok := cellpkg.ParseCellLine(line)
	if !ok {
		t.Fatalf("should parse serving LTE: %s", line)
	}
	if ci.Tech != CellTechLTE || !ci.Serving {
		t.Fatalf("wrong tech/serving: %+v", ci)
	}
	if ci.State != "NOCONN" {
		t.Fatalf("wrong state: %s", ci.State)
	}
	if ci.MCC != "460" || ci.MNC != "11" {
		t.Fatalf("wrong plmn: %s/%s", ci.MCC, ci.MNC)
	}
	if ci.CellID != "400E21E" {
		t.Fatalf("wrong cellid: %s", ci.CellID)
	}
	if ci.EARFCN != 2452 || ci.Band != 5 {
		t.Fatalf("wrong earfcn/band: %d/%d", ci.EARFCN, ci.Band)
	}
	// LTE 服务小区在 cellID 之后也是 PCID，TAC 在带宽字段之后；
	// 曾漏掉 PCI 那一格，导致 PCI 丢失、TAC 被读成 155。
	if ci.PCID != 155 {
		t.Fatalf("wrong pcid: %d (want 155)", ci.PCID)
	}
	if ci.TAC != 0x404A {
		t.Fatalf("wrong tac: %d (want %d)", ci.TAC, 0x404A)
	}
	if ci.RSRP != -104 || ci.RSRQ != -16 {
		t.Fatalf("wrong signal: %+v", ci)
	}
	if ci.OperatorName() != "中国电信" {
		t.Fatalf("wrong operator: %s", ci.OperatorName())
	}
}

// TestParseNeighbourIntra 解析同频邻小区（移远实测格式）
func TestParseNeighbourIntra(t *testing.T) {
	line := `+QENG: "neighbourcell intra","LTE",2452,155,-15,-103,-70,-,21,3,20,2,54`
	ci, ok := cellpkg.ParseCellLine(line)
	if !ok {
		t.Fatalf("should parse neighbour intra: %s", line)
	}
	if !ci.IntraCell || ci.Serving {
		t.Fatalf("wrong intra/serving: %+v", ci)
	}
	if ci.Tech != CellTechLTE {
		t.Fatalf("wrong tech: %+v", ci)
	}
	if ci.EARFCN != 2452 {
		t.Fatalf("wrong earfcn: %d", ci.EARFCN)
	}
	if ci.RSRP != -103 || ci.RSRQ != -15 {
		t.Fatalf("wrong signal: %+v", ci)
	}
}

// TestParseNeighbourInter 解析异频邻小区（移远实测格式，部分字段为空）
func TestParseNeighbourInter(t *testing.T) {
	line := `+QENG: "neighbourcell inter","LTE",1850,-,-,-,-,-,21,3,24,14`
	ci, ok := cellpkg.ParseCellLine(line)
	if !ok {
		t.Fatalf("should parse neighbour inter: %s", line)
	}
	if ci.IntraCell {
		t.Fatalf("inter should not be intra: %+v", ci)
	}
	if ci.EARFCN != 1850 {
		t.Fatalf("wrong earfcn: %d", ci.EARFCN)
	}
}

// TestParseNeighbourCells 批量解析邻小区（移远实测完整应答）
func TestParseNeighbourCells(t *testing.T) {
	resp := []string{
		`+QENG: "neighbourcell intra","LTE",2452,155,-15,-103,-70,-,21,3,20,2,54`,
		`+QENG: "neighbourcell inter","LTE",1850,-,-,-,-,-,21,3,24,14`,
		`+QENG: "neighbourcell inter","LTE",100,-,-,-,-,-,0,3,10,14`,
		"OK",
	}
	cells := cellpkg.ParseCellLines(resp)
	if len(cells) != 3 {
		t.Fatalf("expected 3 neighbour cells, got %d", len(cells))
	}
	// 第一个是同频邻小区
	if !cells[0].IntraCell {
		t.Fatalf("first should be intra: %+v", cells[0])
	}
}

// TestParseCellMixed 混合解析：服务小区 + 邻小区
func TestParseCellMixed(t *testing.T) {
	resp := []string{
		`+QENG: "servingcell","CAMPED","LTE","FDD",460,11,400E21E,155,2452,5,3,3,404A,-104,-16,-69,7,0,-,19`,
		`+QENG: "neighbourcell inter","LTE",1850,-,-,-,-,-,21,3,24,14`,
		"OK",
	}
	cells := cellpkg.ParseCellLines(resp)
	if len(cells) != 2 {
		t.Fatalf("expected 2 cells, got %d", len(cells))
	}
	if !cells[0].Serving || cells[1].Serving {
		t.Fatalf("serving flag wrong: %+v", cells)
	}
}

// TestParseCellInvalid 无效行不应解析
func TestParseCellInvalid(t *testing.T) {
	if _, ok := cellpkg.ParseCellLine("+QENG: ERROR"); ok {
		t.Fatal("ERROR line should not parse")
	}
	if _, ok := cellpkg.ParseCellLine("OK"); ok {
		t.Fatal("OK line should not parse")
	}
}

// TestOperatorName 运营商推断
func TestOperatorName(t *testing.T) {
	cases := map[string]string{
		"46000": "中国移动",
		"46001": "中国联通",
		"46003": "中国电信",
		"46004": "中国广电",
		"99999": "",
	}
	for mccmnc, want := range cases {
		c := &cellpkg.CellInfo{MCC: mccmnc[:3], MNC: mccmnc[3:]}
		if got := c.OperatorName(); got != want {
			t.Errorf("OperatorName(%s) = %q, want %q", mccmnc, got, want)
		}
	}
}

// TestMarkIntraFrequency 邻区应答没有 intra/inter 标志的固件（MT5700 的 ^MONNC、
// 广和通 GTCCINFO）要按"频点是否与服务小区相同"补上，否则界面全写成"异频邻区"。
func TestMarkIntraFrequency(t *testing.T) {
	// 实测 MT5700：服务小区与邻区频点都是 627264 → 同频
	serving := []cellpkg.CellInfo{{Tech: CellTechNR, EARFCN: 627264, PCID: 277}}
	neigh := []cellpkg.CellInfo{
		{Tech: CellTechNR, EARFCN: 627264, PCID: 276}, // 同频
		{Tech: CellTechNR, EARFCN: 636666, PCID: 15},  // 异频
		{Tech: CellTechLTE, EARFCN: 0, PCID: 31},      // 频点未知 → 不推断
	}
	cellpkg.MarkIntraFrequency(serving, neigh)
	if !neigh[0].IntraCell {
		t.Error("同频点邻区应标记为同频")
	}
	if neigh[1].IntraCell || neigh[2].IntraCell {
		t.Errorf("异频/频点未知的邻区不该标成同频: %+v", neigh[1:])
	}
	// 已有标记不能被抹掉（+QENG 的 inter 行不能被误改成 intra）
	n2 := []cellpkg.CellInfo{{EARFCN: 627264}}
	cellpkg.MarkIntraFrequency([]cellpkg.CellInfo{{EARFCN: 627264}, {EARFCN: 627264}}, n2)
	if !n2[0].IntraCell {
		t.Error("两个服务小区任一同频都算同频")
	}
	// 入参为空时不 panic
	cellpkg.MarkIntraFrequency(nil, neigh)
	cellpkg.MarkIntraFrequency(serving, nil)
}

// ---- from: cell_scan_test.go ----

// TestParseServingNRWithState 解析带状态的 NR 服务小区（含 band 与带宽字段）

// TestParseServingNRWithState 解析带状态的 NR 服务小区（含 band 与带宽字段）
func TestParseServingNRWithState(t *testing.T) {
	line := `+QENG: "servingcell","NOCONN","NR","TDD",460,11,400E21E,155,2452,5,30,-95,-12,14,25,2,4`
	ci, ok := cellpkg.ParseCellLine(line)
	if !ok {
		t.Fatalf("should parse serving NR: %s", line)
	}
	if !ci.Serving || ci.Tech != CellTechNR {
		t.Fatalf("wrong serving/tech: %+v", ci)
	}
	if ci.State != "NOCONN" {
		t.Fatalf("wrong state: %s", ci.State)
	}
	if ci.MCC != "460" || ci.MNC != "11" {
		t.Fatalf("wrong plmn: %s/%s", ci.MCC, ci.MNC)
	}
	if ci.EARFCN != 2452 || ci.Band != 5 {
		t.Fatalf("wrong earfcn/band: %d/%d", ci.EARFCN, ci.Band)
	}
	// 字段错位是最容易出的 bug，这里严格校验
	if ci.RSRP != -95 || ci.RSRQ != -12 || ci.SINR != 14 {
		t.Fatalf("wrong NR signal: rsrp=%d rsrq=%d sinr=%d", ci.RSRP, ci.RSRQ, ci.SINR)
	}
}

// TestParseServingNRCompact 解析无状态的 NR 服务小区（简写固件格式）
func TestParseServingNRCompact(t *testing.T) {
	line := `+QENG: "servingcell","NR","FDD",460,00,1051E8F2,123,0,-95,-12,14,35`
	ci, ok := cellpkg.ParseCellLine(line)
	if !ok {
		t.Fatalf("should parse compact serving NR: %s", line)
	}
	if ci.State != "" {
		t.Fatalf("state should be empty: %q", ci.State)
	}
	if ci.RSRP != -95 || ci.RSRQ != -12 || ci.SINR != 14 {
		t.Fatalf("wrong NR signal: %+v", ci)
	}
}

// TestParseServingFibocom 解析广和通 +FENG 小区行（仅指令头不同）
func TestParseServingFibocom(t *testing.T) {
	line := `+FENG: "servingcell","NOCONN","LTE","FDD",460,11,400E21E,155,2452,5,3,3,404A,-104,-16,-69,7,0,-,19`
	ci, ok := cellpkg.ParseCellLine(line)
	if !ok {
		t.Fatalf("should parse +FENG serving LTE: %s", line)
	}
	if ci.Tech != CellTechLTE || ci.RSRP != -104 || ci.RSRQ != -16 {
		t.Fatalf("wrong ci: %+v", ci)
	}
	if ci.PCID != 155 || ci.TAC != 0x404A {
		t.Fatalf("wrong pcid/tac: %d/%d", ci.PCID, ci.TAC)
	}
}

// TestParseServingRSSIAndSINR 校验 RSRP/RSRQ/RSSI/SINR 四值未串位
func TestParseServingRSSIAndSINR(t *testing.T) {
	line := `+QENG: "servingcell","CONNECTED","LTE","FDD",460,00,1A2B3C4,300,1650,3,20,20,216,-97,-11,-68,13,0,-,19`
	ci, ok := cellpkg.ParseCellLine(line)
	if !ok {
		t.Fatalf("should parse: %s", line)
	}
	if ci.RSRP != -97 {
		t.Fatalf("RSRP want -97 got %d", ci.RSRP)
	}
	if ci.RSRQ != -11 {
		t.Fatalf("RSRQ want -11 got %d", ci.RSRQ)
	}
	if ci.RSSI != -68 {
		t.Fatalf("RSSI want -68 got %d", ci.RSSI)
	}
	if ci.SINR != 13 {
		t.Fatalf("SINR want 13 got %d", ci.SINR)
	}
	// 字段整体错位是最容易犯的错：这行有 2 个带宽字段，
	// PCID/EARFCN/band/TAC 必须各就各位。
	if ci.PCID != 300 {
		t.Fatalf("PCID want 300 got %d", ci.PCID)
	}
	if ci.EARFCN != 1650 || ci.Band != 3 {
		t.Fatalf("EARFCN/band want 1650/3 got %d/%d", ci.EARFCN, ci.Band)
	}
	if ci.TAC != 216 {
		t.Fatalf("TAC want 216 got %d", ci.TAC)
	}
	if ci.Band != 3 || ci.EARFCN != 1650 {
		t.Fatalf("band/earfcn wrong: %d/%d", ci.Band, ci.EARFCN)
	}
}

// TestParseNeighbourPCID 邻小区应解析出 PCID（此前被错误塞进 TAC）
func TestParseNeighbourPCID(t *testing.T) {
	line := `+QENG: "neighbourcell intra","LTE",2452,155,-15,-103,-70,-,21,3,20,2,54`
	ci, ok := cellpkg.ParseCellLine(line)
	if !ok {
		t.Fatalf("should parse: %s", line)
	}
	if ci.PCID != 155 {
		t.Fatalf("PCID want 155 got %d", ci.PCID)
	}
	if ci.RSRQ != -15 || ci.RSRP != -103 {
		t.Fatalf("signal wrong: %+v", ci)
	}
	// band 应由 EARFCN 推算：2452 → LTE B5
	if ci.Band != 5 {
		t.Fatalf("band want 5 got %d", ci.Band)
	}
}

// TestParseNeighbourNR 解析 NR 邻小区（ARFCN, PCID, RSRP, RSRQ, SINR）
func TestParseNeighbourNR(t *testing.T) {
	line := `+QENG: "neighbourcell inter","NR",636666,15,-91,-11,16,3,20`
	ci, ok := cellpkg.ParseCellLine(line)
	if !ok {
		t.Fatalf("should parse NR neighbour: %s", line)
	}
	if ci.Tech != CellTechNR || ci.Serving {
		t.Fatalf("wrong tech/serving: %+v", ci)
	}
	if ci.EARFCN != 636666 || ci.PCID != 15 {
		t.Fatalf("arfcn/pcid wrong: %d/%d", ci.EARFCN, ci.PCID)
	}
	if ci.RSRP != -91 || ci.RSRQ != -11 || ci.SINR != 16 {
		t.Fatalf("NR neighbour signal wrong: %+v", ci)
	}
	// 636666 → NR n78
	if ci.Band != 78 {
		t.Fatalf("band want 78 got %d", ci.Band)
	}
}

// TestParseNeighbourMissingFields 邻小区字段大量缺失（"-" 占位）不应解析异常
func TestParseNeighbourMissingFields(t *testing.T) {
	line := `+QENG: "neighbourcell inter","LTE",1850,-,-,-,-,-,-,-`
	ci, ok := cellpkg.ParseCellLine(line)
	if !ok {
		t.Fatalf("should parse: %s", line)
	}
	if ci.EARFCN != 1850 {
		t.Fatalf("earfcn want 1850 got %d", ci.EARFCN)
	}
	if ci.PCID != 0 || ci.RSRP != 0 || ci.RSRQ != 0 {
		t.Fatalf("missing fields should be zero: %+v", ci)
	}
}

// TestBandFromEARFCN 频段推算（模组未上报 band 时兜底）
func TestBandFromEARFCN(t *testing.T) {
	cases := []struct {
		tech   CellTech
		earfcn int
		want   int
	}{
		{CellTechLTE, 2452, 5},
		{CellTechLTE, 1650, 3},
		{CellTechLTE, 100, 1},
		{CellTechLTE, 38000, 38},
		{CellTechLTE, 40000, 41},
		{CellTechLTE, 999999, 0},
		{CellTechNR, 636666, 78},
		{CellTechNR, 504990, 41},
		{CellTechNR, 422000, 1},
		{CellTechNR, 0, 0},
	}
	for _, c := range cases {
		if got := BandFromEARFCN(c.tech, c.earfcn); got != c.want {
			t.Errorf("BandFromEARFCN(%s,%d) = %d, want %d", c.tech, c.earfcn, got, c.want)
		}
	}
}

// TestSortCellsBySignal 附近基站按信号强度降序，未上报的排最后
func TestSortCellsBySignal(t *testing.T) {
	cells := []cellpkg.CellInfo{
		{Tech: CellTechLTE, RSRP: -110},
		{Tech: CellTechLTE, RSRP: -85},
		{Tech: CellTechLTE, RSRP: 0},
		{Tech: CellTechLTE, RSRP: -95},
	}
	cellpkg.SortCellsBySignal(cells)
	want := []int{-85, -95, -110, 0}
	for i, w := range want {
		if cells[i].RSRP != w {
			t.Fatalf("sorted = %v, want %v", cells, want)
		}
	}
}

// TestCellSignalLevel RSRP 等级描述
func TestCellSignalLevel(t *testing.T) {
	cases := map[int]string{
		0:    "未知",
		-75:  "优",
		-88:  "良",
		-95:  "中",
		-105: "差",
		-120: "极差",
	}
	for rsrp, want := range cases {
		c := cellpkg.CellInfo{RSRP: rsrp}
		if got := c.SignalLevel(); got != want {
			t.Errorf("ci.SignalLevel(%d) = %q, want %q", rsrp, got, want)
		}
	}
}

// TestParseOperatorCOPS 从 AT+COPS? 应答提取运营商名称
func TestParseOperatorCOPS(t *testing.T) {
	resp := []string{`+COPS: 0,0,"CHN-UNICOM",7`, "OK"}
	if got := cellpkg.ParseOperator(resp); got != "CHN-UNICOM" {
		t.Fatalf("operator want CHN-UNICOM got %q", got)
	}
	if got := cellpkg.ParseOperator([]string{"OK"}); got != "" {
		t.Fatalf("no operator should be empty, got %q", got)
	}
}

// TestParseServingNRSignal 各厂商 ParseNRSignal 统一实现
func TestParseServingNRSignalShared(t *testing.T) {
	line := `+QENG: "servingcell","NOCONN","NR","TDD",460,11,400E21E,155,2452,5,30,-95,-12,14,25`
	for _, impl := range []ModemCommand{&quectel.QuectelCommand{}, &fibocom.FibocomCommand{}, &simcom.SimcomCommand{}} {
		rsrp, rsrq, sinr, ok := impl.ParseNRSignal(line)
		if !ok {
			t.Fatalf("%T should parse NR signal", impl)
		}
		if rsrp != -95 || rsrq != -12 || sinr != 14 {
			t.Fatalf("%T NR parse wrong: %d %d %d", impl, rsrp, rsrq, sinr)
		}
	}
}

// TestParseCellLTEOnly 非服务/非邻小区行不应被误解析
func TestParseCellLTEOnly(t *testing.T) {
	for _, line := range []string{
		`+CSQ: 22,0`,
		`+QENG: "servingcell","LTE"`,
		`+QNWINFO: "FDD LTE","46011","LTE BAND 5",2452`,
		`+COPS: 0,0,"CHN-UNICOM",7`,
	} {
		if ci, ok := cellpkg.ParseCellLine(line); ok {
			t.Errorf("line should not parse as ci: %s -> %+v", line, ci)
		}
	}
}

// ---- from: cell_nrsa_test.go ----

// TestParseServingNR5GSA 解析真实模组（移远 RM502Q-GL，SA 组网）上报的 NR5G-SA 服务小区。
//
// 报文为实测抓取：
//
//	+QENG: "servingcell","NOCONN","NR5G-SA","TDD",460,11,CFA11A009,521,CFA102,627264,78,3,-113,-12,6,1,7
//	                                          MCC MNC cellID    PCI TAC(hex) ARFCN band bw RSRP RSRQ SINR

// TestParseServingNR5GSA 解析真实模组（移远 RM502Q-GL，SA 组网）上报的 NR5G-SA 服务小区。
//
// 报文为实测抓取：
//
//	+QENG: "servingcell","NOCONN","NR5G-SA","TDD",460,11,CFA11A009,521,CFA102,627264,78,3,-113,-12,6,1,7
//	                                          MCC MNC cellID    PCI TAC(hex) ARFCN band bw RSRP RSRQ SINR
func TestParseServingNR5GSA(t *testing.T) {
	line := `+QENG: "servingcell","NOCONN","NR5G-SA","TDD",460,11,CFA11A009,521,CFA102,627264,78,3,-113,-12,6,1,7`
	ci, ok := cellpkg.ParseCellLine(line)
	if !ok {
		t.Fatalf("should parse NR5G-SA serving ci: %s", line)
	}
	if !ci.Serving {
		t.Fatalf("should be serving: %+v", ci)
	}
	if ci.Tech != CellTechNR {
		t.Fatalf("tech want NR got %q", ci.Tech)
	}
	if ci.State != "NOCONN" {
		t.Fatalf("state want NOCONN got %q", ci.State)
	}
	if ci.MCC != "460" || ci.MNC != "11" {
		t.Fatalf("plmn want 460/11 got %s/%s", ci.MCC, ci.MNC)
	}
	if ci.CellID != "CFA11A009" {
		t.Fatalf("cellid want CFA11A009 got %s", ci.CellID)
	}
	if ci.PCID != 521 {
		t.Fatalf("pcid want 521 got %d", ci.PCID)
	}
	if ci.TAC != 0xCFA102 {
		t.Fatalf("tac want %d got %d", 0xCFA102, ci.TAC)
	}
	if ci.EARFCN != 627264 {
		t.Fatalf("arfcn want 627264 got %d", ci.EARFCN)
	}
	if ci.Band != 78 {
		t.Fatalf("band want 78 got %d", ci.Band)
	}
	if ci.RSRP != -113 || ci.RSRQ != -12 || ci.SINR != 6 {
		t.Fatalf("signal wrong: rsrp=%d rsrq=%d sinr=%d", ci.RSRP, ci.RSRQ, ci.SINR)
	}
	if ci.OperatorName() != "中国电信" {
		t.Fatalf("operator want 中国电信 got %q", ci.OperatorName())
	}
}

// TestParseServingNR5GSASignal 各厂商 ParseNRSignal 也应能解析 NR5G-SA 报文
func TestParseServingNR5GSASignal(t *testing.T) {
	line := `+QENG: "servingcell","NOCONN","NR5G-SA","TDD",460,11,CFA11A009,521,CFA102,627264,78,3,-113,-12,6,1,7`
	for _, impl := range []ModemCommand{&quectel.QuectelCommand{}, &fibocom.FibocomCommand{}, &simcom.SimcomCommand{}} {
		rsrp, rsrq, sinr, ok := impl.ParseNRSignal(line)
		if !ok {
			t.Fatalf("%T should parse NR5G-SA signal", impl)
		}
		if rsrp != -113 || rsrq != -12 || sinr != 6 {
			t.Fatalf("%T NR5G-SA wrong: %d %d %d", impl, rsrp, rsrq, sinr)
		}
	}
}

// TestParseServingNRNSA 解析 NR5G-NSA 报文（与 SA 同一字段顺序：cellID,PCI,TAC,ARFCN,band）
func TestParseServingNRNSA(t *testing.T) {
	line := `+QENG: "servingcell","CONNECTED","NR5G-NSA","TDD",460,00,1051E8F2,123,1A2B,627264,78,3,-95,-12,14,1,6`
	ci, ok := cellpkg.ParseCellLine(line)
	if !ok {
		t.Fatalf("should parse NR5G-NSA: %s", line)
	}
	if ci.Tech != CellTechNR || ci.PCID != 123 {
		t.Fatalf("wrong tech/pcid: %+v", ci)
	}
	if ci.TAC != 0x1A2B || ci.EARFCN != 627264 || ci.Band != 78 {
		t.Fatalf("wrong tac/arfcn/band: %d/%d/%d", ci.TAC, ci.EARFCN, ci.Band)
	}
	if ci.RSRP != -95 || ci.RSRQ != -12 || ci.SINR != 14 {
		t.Fatalf("wrong signal: %+v", ci)
	}
	if ci.OperatorName() != "中国移动" {
		t.Fatalf("operator want 中国移动 got %q", ci.OperatorName())
	}
}

// TestParseOperatorPlaceholder COPS 的 "????" 占位符（未注册）不应作为运营商名
func TestParseOperatorPlaceholder(t *testing.T) {
	if got := cellpkg.ParseOperator([]string{`+COPS: 0,0,"????",11`, "OK"}); got != "" {
		t.Fatalf("placeholder operator should be empty, got %q", got)
	}
}

// TestNormalizeTech 接入技术归一化：NR 的多种上报写法统一为 CellTechNR
func TestNormalizeTech(t *testing.T) {
	cases := map[string]CellTech{
		"LTE":      CellTechLTE,
		"LTE-A":    CellTechLTE,
		"NR":       CellTechNR,
		"NR5G":     CellTechNR,
		"NR5G-SA":  CellTechNR,
		"NR5G-NSA": CellTechNR,
		"nr5g-sa":  CellTechNR,
		"WCDMA":    CellTechWCDMA,
		"GSM":      CellTechGSM,
	}
	for in, want := range cases {
		if got := cellpkg.NormalizeTech(in); got != want {
			t.Errorf("NormalizeTech(%q) = %q, want %q", in, got, want)
		}
	}
	if !cellpkg.IsNRSA("NR5G-SA") || !cellpkg.IsNRSA("nr5g-nsa") {
		t.Error("IsNRSA should accept NR5G-SA / NR5G-NSA")
	}
	if cellpkg.IsNRSA("NR") || cellpkg.IsNRSA("LTE") {
		t.Error("IsNRSA should reject NR / LTE")
	}
}

// TestCachedScanResultOperator 缓存结果也应还原运营商名（与实时扫描保持一致）
func TestCachedScanResultOperator(t *testing.T) {
	mod := &Modem{Cells: []cellpkg.CellInfo{
		{Tech: CellTechNR, Serving: true, MCC: "460", MNC: "11", Operator: "中国电信"},
		{Tech: CellTechLTE, Serving: false, MCC: "460", MNC: "11", Operator: "中国电信"},
	}}
	res := cachedScanResult(mod)
	if res.Count != 2 || res.ServingCount != 1 || res.NeighbourCnt != 1 {
		t.Fatalf("counts wrong: %+v", res)
	}
	if res.Operator != "中国电信" {
		t.Fatalf("operator want 中国电信 got %q", res.Operator)
	}
}

// ---- from: cell_plmn_test.go ----

// TestFillNeighbourPLMN 邻小区无 PLMN 时应用服务小区 PLMN 补齐，
// 已有 PLMN 的邻小区不被覆盖。

// TestFillNeighbourPLMN 邻小区无 PLMN 时应用服务小区 PLMN 补齐，
// 已有 PLMN 的邻小区不被覆盖。
func TestFillNeighbourPLMN(t *testing.T) {
	serving := []cellpkg.CellInfo{{Tech: CellTechLTE, MCC: "460", MNC: "11", Operator: "中国电信", Serving: true}}
	neighbours := []cellpkg.CellInfo{
		{Tech: CellTechLTE, PCID: 155},
		{Tech: CellTechLTE, PCID: 10, MCC: "460", MNC: "00", Operator: "中国移动"},
	}
	cellpkg.FillNeighbourPLMN(serving, neighbours)
	if neighbours[0].MCC != "460" || neighbours[0].MNC != "11" || neighbours[0].Operator != "中国电信" {
		t.Fatalf("neighbour[0] not filled: %+v", neighbours[0])
	}
	if neighbours[1].MNC != "00" || neighbours[1].Operator != "中国移动" {
		t.Fatalf("neighbour[1] should keep own PLMN: %+v", neighbours[1])
	}
}

// TestFillNeighbourPLMNNoServing 无服务小区时不做任何修改
func TestFillNeighbourPLMNNoServing(t *testing.T) {
	neighbours := []cellpkg.CellInfo{{Tech: CellTechLTE, PCID: 155}}
	cellpkg.FillNeighbourPLMN(nil, neighbours)
	if neighbours[0].MCC != "" || neighbours[0].Operator != "" {
		t.Fatalf("should stay empty: %+v", neighbours[0])
	}
}

// ---- from: cell_quectel_test.go ----

// ---- from: cell_rm500u_test.go ----
// 各机型实测样例的回归测试。
//
// 为什么单独一个文件：**适配新模组时先把真机应答固化在这里**，
// 再改解析代码 —— 这样既能证明新格式被支持，也能顺手证明旧机型没被改坏
// （典型的改坏方式是"把某个字段的下标挪一格"）。
//
// 样例全部来自真机原始应答（2026-09-29）。
// ── Quectel RM500U-CNV（展锐平台，COM6/COM10）────────────────────────────────
//
// 与 RM502Q-GL 的差异（这就是"适配"要处理的东西）：
//  1. 服务小区拆两行：`"servingcell","CONNECT"` + 下一行 `"LTE","FDD",…`（数据行没有标记）
//  2. 邻区信号字段顺序反了：RSRP 在前（RM502Q 是 RSRQ 在前）
//  3. `AT+QCSQ` / `AT+CPSI?` 直接回 +CME ERROR: 4 → 只能靠 +QENG / +CESQ
//  4. `AT+CCLK?` 带 +32 后缀但报的就是 UTC（见 atcmd.go）
// 各机型实测样例的回归测试。
//
// 为什么单独一个文件：**适配新模组时先把真机应答固化在这里**，
// 再改解析代码 —— 这样既能证明新格式被支持，也能顺手证明旧机型没被改坏
// （典型的改坏方式是"把某个字段的下标挪一格"）。
//
// 样例全部来自真机原始应答（2026-09-29）。
// ── Quectel RM500U-CNV（展锐平台，COM6/COM10）────────────────────────────────
//
// 与 RM502Q-GL 的差异（这就是"适配"要处理的东西）：
//  1. 服务小区拆两行：`"servingcell","CONNECT"` + 下一行 `"LTE","FDD",…`（数据行没有标记）
//  2. 邻区信号字段顺序反了：RSRP 在前（RM502Q 是 RSRQ 在前）
//  3. `AT+QCSQ` / `AT+CPSI?` 直接回 +CME ERROR: 4 → 只能靠 +QENG / +CESQ
//  4. `AT+CCLK?` 带 +32 后缀但报的就是 UTC（见 atcmd.go）

var rm500uServing = []string{
	`+QENG: "servingcell","CONNECT"`,
	`+QENG: "LTE","FDD",460,11,3FA6836,140,1850,3,5,5,404A,-117,-14,-83,-2,6,22,7`,
	`+QENG: "NR5G-NSA",0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0`,
	"OK",
}

func TestRM500UServingCellTwoLineFormat(t *testing.T) {
	cells := cellpkg.ParseCellLines(rm500uServing)
	if len(cells) != 1 {
		t.Fatalf("应解析出 1 个服务小区（NR5G-NSA 全 0 行不算），得到 %d 个: %+v", len(cells), cells)
	}
	c := cells[0]
	if !c.Serving || c.Tech != CellTechLTE {
		t.Fatalf("serving/tech 不对: serving=%v tech=%s", c.Serving, c.Tech)
	}
	if c.MCC != "460" || c.MNC != "11" {
		t.Errorf("PLMN = %s-%s, want 460-11", c.MCC, c.MNC)
	}
	if c.CellID != "3FA6836" {
		t.Errorf("cell_id = %q, want 3FA6836", c.CellID)
	}
	if c.PCID != 140 {
		t.Errorf("pcid = %d, want 140", c.PCID)
	}
	if c.EARFCN != 1850 || c.Band != 3 {
		t.Errorf("earfcn/band = %d/%d, want 1850/3", c.EARFCN, c.Band)
	}
	if c.TAC != 0x404A {
		t.Errorf("tac = %d, want %d (0x404A)", c.TAC, 0x404A)
	}
	if c.RSRP != -117 || c.RSRQ != -14 || c.RSSI != -83 || c.SINR != -2 {
		t.Errorf("信号 = rsrp %d / rsrq %d / rssi %d / sinr %d, want -117/-14/-83/-2",
			c.RSRP, c.RSRQ, c.RSSI, c.SINR)
	}
	if c.State != "CONNECT" {
		t.Errorf("state = %q, want CONNECT（状态在上一行的标记里）", c.State)
	}
	if c.Operator != "中国电信" {
		t.Errorf("operator = %q", c.Operator)
	}
	// 服务小区信号要能取出来（LTE 驻网时也要显示 RSRP/RSRQ/SINR）
	rsrp, rsrq, sinr, ok := cellpkg.ParseServingCellSignal(rm500uServing[1])
	if !ok || rsrp != -117 || rsrq != -14 || sinr != -2 {
		t.Errorf("ParseServingCellSignal = %d/%d/%d ok=%v", rsrp, rsrq, sinr, ok)
	}
}

// 邻区：RSRP 在前的顺序（RM500U）
func TestRM500UNeighbourRSRPFirst(t *testing.T) {
	line := `+QENG: "neighbourcell inter","LTE",2452,155,-118,-12,0,0,255,-1,-1,16`
	c, ok := cellpkg.ParseCellLine(line)
	if !ok {
		t.Fatal("应能解析 RM500U 的 inter 邻区行")
	}
	if c.EARFCN != 2452 || c.PCID != 155 || c.Band != 5 {
		t.Errorf("earfcn/pcid/band = %d/%d/%d, want 2452/155/5", c.EARFCN, c.PCID, c.Band)
	}
	if c.RSRP != -118 || c.RSRQ != -12 {
		t.Errorf("rsrp/rsrq = %d/%d, want -118/-12（RSRP 在前）", c.RSRP, c.RSRQ)
	}
}

// 邻区：intra 行只给相对值、没有绝对 RSRP → 宁可不填，也不要编一个 -13 dBm
func TestRM500UNeighbourIntraWithoutRSRP(t *testing.T) {
	line := `+QENG: "neighbourcell intra","LTE",1850,140,-13,-13,7,1,-1,-,-,-,-`
	c, ok := cellpkg.ParseCellLine(line)
	if !ok {
		t.Fatal("应能解析 intra 邻区行")
	}
	if c.RSRP != 0 {
		t.Errorf("rsrp = %d, want 0（不硬凑假值）", c.RSRP)
	}
	if c.RSRQ != -13 {
		t.Errorf("rsrq = %d, want -13", c.RSRQ)
	}
	if !c.IntraCell {
		t.Error("intra_cell 应为 true")
	}
}

// 旧机型回归：RM502Q-GL 的邻区是 RSRQ 在前 —— 量纲解析必须同时兼容两种顺序
func TestRM502QNeighbourRSRQFirst(t *testing.T) {
	line := `+QENG: "neighbourcell intra","LTE",2452,155,-15,-103,-70,-,21,3,20,2,54`
	c, ok := cellpkg.ParseCellLine(line)
	if !ok {
		t.Fatal("应能解析 RM502Q 的邻区行")
	}
	if c.RSRP != -103 {
		t.Errorf("rsrp = %d, want -103", c.RSRP)
	}
	if c.RSRQ != -15 {
		t.Errorf("rsrq = %d, want -15", c.RSRQ)
	}
}

// +CESQ 兜底：不依赖厂商方言，覆盖面最广
func TestParseCESQ(t *testing.T) {
	rsrp, rsrq, ok := cellpkg.ParseCESQ([]string{`+CESQ: 99,99,255,255,16,25,255,255,255`, "OK"})
	if !ok {
		t.Fatal("应能解析 +CESQ")
	}
	// 16 → (16-39)/2 = -11.5 → 取整 -11；25 → 25-140 = -115 dBm
	if rsrp != -115 || rsrq != -11 {
		t.Errorf("rsrp/rsrq = %d/%d, want -115/-11", rsrp, rsrq)
	}
	// 未上报（255）时不该给出假值
	if _, _, ok := cellpkg.ParseCESQ([]string{`+CESQ: 99,99,255,255,255,255`}); ok {
		t.Error("全 255 不应算有效上报")
	}
	if _, _, ok := cellpkg.ParseCESQ([]string{"OK"}); ok {
		t.Error("没有 +CESQ 行不应算解析成功")
	}
}

// ---- from: rm520_test.go ----

//
// 固件 RM520NGLAAR01A07M4G，中国电信 460/11。
// 与 RM502Q-GL 基线**基本一致**：`+QENG` 服务小区同格式、`AT+QNWINFO` / `AT+QCAINFO` /
// `AT+QGDCNT?` / `AT+QNWPREFCFG` / `AT+QCFG="usbnet"` / `AT+QTEMP`(17 路) 都可用，
// AMBR 甚至一次回两行（IMS 300/75 与 ctnet 1000/1000，取数据 APN 那行）。
// 实测差异只有两条，都进机型特例表（不是厂商接口，免得影响同厂其它型号）：
//  1. `AT+CCLK?` 回 `26/09/29,13:49:23+32`，但 13:49 就是 UTC（主机本地 21:49）
//     —— 与 RM500U / EM05 / FM650 同一个毛病，clock_skew 会显示 -28801s；
//  2. `AT+CBC` 可用，且是 EM05 同款三字段 `+CBC: 0,71,3861`（<bcs>,<bcl%>,<mV>）。

// ====================== Quectel RM520N-GL（2026-09-29 实机抓包，AT 口 COM27） ======================
//
// 固件 RM520NGLAAR01A07M4G，中国电信 460/11。
// 与 RM502Q-GL 基线**基本一致**：`+QENG` 服务小区同格式、`AT+QNWINFO` / `AT+QCAINFO` /
// `AT+QGDCNT?` / `AT+QNWPREFCFG` / `AT+QCFG="usbnet"` / `AT+QTEMP`(17 路) 都可用，
// AMBR 甚至一次回两行（IMS 300/75 与 ctnet 1000/1000，取数据 APN 那行）。
// 实测差异只有两条，都进机型特例表（不是厂商接口，免得影响同厂其它型号）：
//  1. `AT+CCLK?` 回 `26/09/29,13:49:23+32`，但 13:49 就是 UTC（主机本地 21:49）
//     —— 与 RM500U / EM05 / FM650 同一个毛病，clock_skew 会显示 -28801s；
//  2. `AT+CBC` 可用，且是 EM05 同款三字段 `+CBC: 0,71,3861`（<bcs>,<bcl%>,<mV>）。
func TestQuirkRM520(t *testing.T) {
	const model = "ATI|Quectel|RM520N-GL|Revision: RM520NGLAAR01A07M4G|OK"
	q := (&Modem{Model: model}).Quirk()
	if !q.IgnoreCCLKTZ {
		t.Error("RM520N-GL 的 AT+CCLK? 带 +32 后缀但报的是 UTC → 必须忽略时区后缀")
	}
	if q.VoltageCmd != "AT+CBC" {
		t.Errorf("RM520N-GL voltageCmd = %q, want AT+CBC", q.VoltageCmd)
	}
	if q.TempCmd != "" {
		t.Errorf("RM520N-GL 温度走厂商默认 AT+QTEMP，不该开特例：%q", q.TempCmd)
	}

	mod := &Modem{ID: 0, Model: model, cmdImpl: &quectel.QuectelCommand{}}
	if got := mod.voltageCommand(); got != "AT+CBC" {
		t.Errorf("voltageCommand = %q, want AT+CBC（机型特例）", got)
	}
	if got := mod.tempQueryCommand(); got != "AT+QTEMP" {
		t.Errorf("tempQueryCommand = %q, want AT+QTEMP", got)
	}

	// ⚠️ 子串不能撞车：RM502Q-GL 不能命中 RM520 的特例
	if got := (&Modem{Model: "ATI|Quectel|RM502Q-GL|OK"}).Quirk().VoltageCmd; got != "" {
		t.Errorf("RM502Q-GL 不该命中 RM520 的特例：%q", got)
	}
	// RM500U 只忽略时区、不开电压（原有特例不能被挤掉）
	q500 := (&Modem{Model: "ATI|Quectel|RM500U-CNV|OK"}).Quirk()
	if !q500.IgnoreCCLKTZ || q500.VoltageCmd != "" {
		t.Errorf("RM500U 特例被影响: %+v", q500)
	}
}

// RM520N-GL 的 AT+CBC 是 EM05 同款三字段：第二个是电量百分比，第三个才是 mV
func TestParseCBCRM520(t *testing.T) {
	if got := atparse.ParseCBC([]string{"+CBC: 0,71,3861", "OK"}); got != "3.861 V" {
		t.Errorf("RM520 +CBC = %q, want \"3.861 V\"", got)
	}
}

// 服务小区仍是经典 +QENG 单行写法：
//
//	+QENG: "servingcell","NOCONN","NR5G-SA","TDD",460,11,CFA11A009,521,CFA102,627264,78,3,-102,-12,12,1,18
//	         state        tech       duplex  MCC MNC CellID   PCI TAC(hex) ARFCN band bw RSRP RSRQ SINR ...
//
// 交叉验证：CellID/PCI/TAC 与此前同基站的 SIM8200 报的 0xCFA11A009 / 521 / TAC 0xCFA102 完全一致。
func TestRM520ServingCellRegression(t *testing.T) {
	const line = `+QENG: "servingcell","NOCONN","NR5G-SA","TDD",460,11,CFA11A009,521,CFA102,627264,78,3,-102,-12,12,1,18`
	cell, ok := cellpkg.ParseCellLine(line)
	if !ok || !cell.Serving {
		t.Fatalf("服务小区解析失败: %s", line)
	}
	if cell.Tech != CellTechNR || cell.Band != 78 || cell.EARFCN != 627264 || cell.PCID != 521 {
		t.Errorf("字段不对: %+v", cell)
	}
	if cell.CellID != "CFA11A009" || cell.TAC != 0xCFA102 {
		t.Errorf("CellID/TAC 不对: %q / %d（TAC 是十六进制）", cell.CellID, cell.TAC)
	}
	if cell.MCC != "460" || cell.MNC != "11" || cell.Operator != "中国电信" {
		t.Errorf("PLMN/运营商不对: %s/%s %q", cell.MCC, cell.MNC, cell.Operator)
	}
	if cell.RSRP != -102 || cell.RSRQ != -12 || cell.SINR != 12 {
		t.Errorf("信号不对: %d/%d/%d", cell.RSRP, cell.RSRQ, cell.SINR)
	}
}

// CA：SA 下只有 PCC（未聚合），解析不能把频段/PCID 读错位
func TestRM520CAInfoRegression(t *testing.T) {
	ccs := cellpkg.ParseQCAINFO([]string{`+QCAINFO: "PCC",627264,3,"NR5G BAND 78",521`, "OK"})
	if len(ccs) != 1 {
		t.Fatalf("分量载波数 = %d, want 1", len(ccs))
	}
	c := ccs[0]
	if c.Role != "PCC" || c.Band != 78 || c.EARFCN != 627264 || c.PCID != 521 {
		t.Errorf("CA 字段不对: %+v", c)
	}
	if CCsAggregated(ccs) {
		t.Error("只有 PCC 时应判为未聚合")
	}
}

// AMBR：RM520 一次回两行（IMS + ctnet）→ 必须取**数据 APN** 那行，而不是第一行
func TestParseAMBRRM520(t *testing.T) {
	resp := []string{
		`AT+QNWCFG="nr5g_ambr"`,
		`+QNWCFG: "nr5g_ambr","IMS",6,300,6,75`,
		`+QNWCFG: "nr5g_ambr","ctnet",6,1000,6,1000`,
		"OK",
	}
	r, ok := atparse.ParseAMBR(resp)
	if !ok {
		t.Fatal("应解析出签约速率")
	}
	if r.APN != "ctnet" || r.DLMbps != 1000 || r.ULMbps != 1000 {
		t.Errorf("AMBR = %+v, want ctnet 1000/1000（不能取 IMS 那条）", r)
	}
}

// 5G SA 下 CS 域回 0,3（注册被拒）、CGREG/CEREG 回 0,0，真正的注册在 +C5GREG: 0,1
// → 必须触发 5GS 兜底，否则界面会显示"数据域未注册"。
func TestRM520RegFallback(t *testing.T) {
	for _, c := range []struct {
		name     string
		resp     []string
		wantNeed bool
	}{
		{
			// 实测原始应答：CS 3（被拒）/ PS 0（未搜索）
			name:     "RM520 SA 实测（CREG 0,3 + CGREG 0,0）",
			resp:     []string{`+CREG: 0,3`, `+CGREG: 0,0`},
			wantNeed: true,
		},
		{
			// 平时正常的模组（RM500U 那种）：不该多发指令
			name:     "正常注册（CREG 0,1 + CGREG 0,1）",
			resp:     []string{`+CREG: 0,1`, `+CGREG: 0,1`},
			wantNeed: false,
		},
	} {
		cs, _ := ParseReg(c.resp[:1])
		ps, _ := ParseReg(c.resp[1:])
		st := &NetworkStatus{CSReg: cs, PSReg: ps}
		if got := needRegFallback(st); got != c.wantNeed {
			t.Errorf("%s: needRegFallback = %v, want %v", c.name, got, c.wantNeed)
		}
	}
}

// ---- from: cell_fibocom_test.go ----

// ---- from: gtccinfo_test.go ----
// 广和通 FM650-CN（展锐平台）真机原始应答（2026-09-29 抓，COM23，中国电信 460/11）。
// 适配新模组的第一步就是把实测回显固化成样例，再改解析 —— 这份样例同时钉住：
//   - 字段口径（type/state/MCC/MNC/TAC/CellID/ARFCN/PCI/band/bw/SINR/RxLev/RSRP/RSRQ）
//   - 5~8 号字段是**十六进制**（ARFCN "9AC80" = 633472、PCI "1C0" = 448）
//   - 美式拼写的标签行 "NR neighbor cell:"
// 广和通 FM650-CN（展锐平台）真机原始应答（2026-09-29 抓，COM23，中国电信 460/11）。
// 适配新模组的第一步就是把实测回显固化成样例，再改解析 —— 这份样例同时钉住：
//   - 字段口径（type/state/MCC/MNC/TAC/CellID/ARFCN/PCI/band/bw/SINR/RxLev/RSRP/RSRQ）
//   - 5~8 号字段是**十六进制**（ARFCN "9AC80" = 633472、PCI "1C0" = 448）
//   - 美式拼写的标签行 "NR neighbor cell:"

const fm650GTCCINFO = `AT+GTCCINFO?
+GTCCINFO:
NR service cell:
1,9,460,11,CFA102,CFA11C101,9AC80,1C0,78,100,61,47,47,76
NR neighbor cell:
2,9,,,,,9AC80,48,,41,41,64
2,9,,,,,9AC80,8A,,38,38,66
2,9,,,,,9AC80,114,,34,34,50
2,9,,,,,99240,209,,47,47,67
2,9,,,,,99240,1A4,,42,42,56
OK`

func fm650GTCCINFOLines() []string {
	return []string{
		"AT+GTCCINFO?",
		"+GTCCINFO:",
		"NR service cell:",
		"1,9,460,11,CFA102,CFA11C101,9AC80,1C0,78,100,61,47,47,76",
		"NR neighbor cell:",
		"2,9,,,,,9AC80,48,,41,41,64",
		"2,9,,,,,9AC80,8A,,38,38,66",
		"2,9,,,,,9AC80,114,,34,34,50",
		"2,9,,,,,99240,209,,47,47,67",
		"2,9,,,,,99240,1A4,,42,42,56",
		"OK",
	}
}

// TestParseGTCCINFOFM650Serving 服务小区：字段口径、十六进制字段、信号换算
func TestParseGTCCINFOFM650Serving(t *testing.T) {
	serving, neighbours := cellpkg.ParseGTCCINFO(fm650GTCCINFOLines())
	if len(serving) != 1 {
		t.Fatalf("服务小区数 = %d, want 1", len(serving))
	}
	c := serving[0]
	if !c.Serving || c.Tech != CellTechNR {
		t.Errorf("应为 NR 服务小区: serving=%v tech=%v", c.Serving, c.Tech)
	}
	if c.MCC != "460" || c.MNC != "11" {
		t.Errorf("PLMN = %s/%s, want 460/11", c.MCC, c.MNC)
	}
	if c.Operator != "中国电信" {
		t.Errorf("运营商 = %q, want 中国电信", c.Operator)
	}
	// ⚠️ TAC / CellID / ARFCN / PCI 都是十六进制
	if c.TAC != 0xCFA102 {
		t.Errorf("TAC = %d (0x%X), want 0xCFA102", c.TAC, c.TAC)
	}
	if c.CellID != "CFA11C101" {
		t.Errorf("CellID = %q, want CFA11C101", c.CellID)
	}
	if c.EARFCN != 0x9AC80 {
		t.Errorf("ARFCN = %d, want 633984（0x9AC80；按十进制解会错成 99240）", c.EARFCN)
	}
	if c.PCID != 0x1C0 {
		t.Errorf("PCI = %d, want 448（0x1C0；按十进制解会错成 28 号字段位）", c.PCID)
	}
	if c.Band != 78 || c.Bandwidth != 100 {
		t.Errorf("band/bw = %d/%d, want 78/100", c.Band, c.Bandwidth)
	}
	// NR 换算：SINR = f11*0.5-23.5；RSRP = f13-157；RSRQ = (f14+1)*0.5-44
	if c.RSRP != -110 {
		t.Errorf("RSRP = %d, want -110", c.RSRP)
	}
	if c.SINR != 7 {
		t.Errorf("SINR = %d, want 7（61*0.5-23.5）", c.SINR)
	}
	if c.RSRQ != -6 {
		t.Errorf("RSRQ = %d, want -6（(76+1)*0.5-44 = -5.5 取整）", c.RSRQ)
	}

	if len(neighbours) != 5 {
		t.Fatalf("邻区数 = %d, want 5", len(neighbours))
	}
	n := neighbours[0]
	if n.Serving || n.Tech != CellTechNR {
		t.Errorf("邻区标记不对: serving=%v tech=%v", n.Serving, n.Tech)
	}
	if n.PCID != 0x48 || n.EARFCN != 0x9AC80 {
		t.Errorf("邻区 PCI/ARFCN = %d/%d, want 72/633984", n.PCID, n.EARFCN)
	}
	if n.Band != 0 {
		t.Errorf("邻区不上报 band 时应为 0，得到 %d", n.Band)
	}
	// 邻区行只有 12 格（服务小区 14 格）：信号在**行尾**三格 = RxLev,RSRP,RSRQ，没有 SINR
	if n.RSRP != -116 { // 41-157
		t.Errorf("邻区 RSRP = %d, want -116", n.RSRP)
	}
	if n.RSRQ != -12 { // (64+1)*0.5-44 = -11.5
		t.Errorf("邻区 RSRQ = %d, want -12", n.RSRQ)
	}
	if n.SINR != 0 {
		t.Errorf("邻区不上报 SINR，应为 0，得到 %d", n.SINR)
	}
	// 邻区不带 PLMN，由 FillNeighbourPLMN 用服务小区补齐
	cellpkg.FillNeighbourPLMN(serving, neighbours)
	if neighbours[0].MCC != "460" || neighbours[0].Operator != "中国电信" {
		t.Errorf("邻区 PLMN 补齐失败: %s/%s %q", neighbours[0].MCC, neighbours[0].MNC, neighbours[0].Operator)
	}
}

// TestParseGTCCINFOEmptyFields 模组刚切小区时 MCC/MNC/TAC 可能是空的/全 0 占位。
func TestParseGTCCINFOEmptyFields(t *testing.T) {
	serving, _ := cellpkg.ParseGTCCINFO([]string{
		"+GTCCINFO:",
		"NR service cell:",
		"1,9,000,00,0,CFA11C101,9AC80,1C0,78,5,59,47,47,74",
		"OK",
	})
	if len(serving) != 1 {
		t.Fatalf("服务小区数 = %d, want 1", len(serving))
	}
	c := serving[0]
	if c.MCC != "" || c.MNC != "" {
		t.Errorf("000/00 是占位值，应清空: %s/%s", c.MCC, c.MNC)
	}
	if c.TAC != 0 {
		t.Errorf("TAC = %d, want 0", c.TAC)
	}
	if c.Operator != "" {
		t.Errorf("没有 PLMN 时不该有运营商名: %q", c.Operator)
	}
	// 这一行带宽字段是 5 → 5MHz（不是 100），换算规则见官方脚本
	if c.Bandwidth != 5 {
		t.Errorf("带宽 = %d, want 5", c.Bandwidth)
	}
}

// TestParseGTCCINFOLTE 按官方脚本口径构造的 LTE 行（本机当前是 NR 驻网，暂未实测 LTE）：
//
//	band = f9-100（103 → B3）、带宽 = f10/5（75 → 15MHz）、RSRP = f13-141、RSRQ = f14*0.5-20
func TestParseGTCCINFOLTE(t *testing.T) {
	serving, _ := cellpkg.ParseGTCCINFO([]string{
		"+GTCCINFO:",
		"LTE service cell:",
		"1,7,460,11,404A,3FA6836,73A,8C,103,75,38,55,60,25",
		"OK",
	})
	if len(serving) != 1 {
		t.Fatalf("服务小区数 = %d, want 1", len(serving))
	}
	c := serving[0]
	if c.Tech != CellTechLTE {
		t.Errorf("tech = %v, want LTE", c.Tech)
	}
	if c.Band != 3 || c.Bandwidth != 15 {
		t.Errorf("band/bw = %d/%d, want 3/15", c.Band, c.Bandwidth)
	}
	if c.TAC != 0x404A || c.PCID != 0x8C || c.EARFCN != 0x73A {
		t.Errorf("TAC/PCI/EARFCN = %d/%d/%d, want 16458/140/1850", c.TAC, c.PCID, c.EARFCN)
	}
	if c.RSRP != -81 { // 60-141
		t.Errorf("RSRP = %d, want -81", c.RSRP)
	}
	if c.RSRQ != -8 { // 25*0.5-20 = -7.5
		t.Errorf("RSRQ = %d, want -8", c.RSRQ)
	}
}

// TestParseGTCCINFOENDC EN-DC（LTE-NR）标签下是两行：第一行 LTE 锚点、第二行 NR。
func TestParseGTCCINFOENDC(t *testing.T) {
	serving, _ := cellpkg.ParseGTCCINFO([]string{
		"+GTCCINFO:",
		"LTE-NR service cell:",
		"1,7,460,11,404A,3FA6836,73A,8C,103,75,38,55,60,25",
		"1,9,460,11,404A,3FA6836,9AC80,1C0,78,100,61,47,47,76",
		"OK",
	})
	if len(serving) != 2 {
		t.Fatalf("EN-DC 服务小区数 = %d, want 2", len(serving))
	}
	if serving[0].Tech != CellTechLTE || serving[1].Tech != CellTechNR {
		t.Errorf("第一行应为 LTE 锚点、第二行应为 NR: %v / %v", serving[0].Tech, serving[1].Tech)
	}
}

// 认不出的行一律不解析（宁可空白也不显示错的数据）。
func TestParseGTCCINFORejects(t *testing.T) {
	if _, _, ok := cellpkg.ParseGTCCINFLabel("+GTCCINFO:"); ok {
		t.Error("+GTCCINFO: 是头行，不是标签行")
	}
	if _, _, ok := cellpkg.ParseGTCCINFLabel("NR service cell:"); !ok {
		t.Error("NR service cell: 应被识别为标签行")
	}
	for _, line := range []string{
		"+GTCCINFO:", "OK", "", "AT+GTCCINFO?",
		"3,9,460,11,CFA102,CFA11C101,9AC80,1C0,78,100,61,47,47,76", // 类型既不是 1 也不是 2
		"1,9,460,11,CFA102", // 字段不够
		"+QENG: \"servingcell\",\"NOCONN\",\"LTE\",\"FDD\",460,11,1,2,3,4,5,6,7,8",
	} {
		if _, ok := cellpkg.ParseGTCCINFRow("NR", true, 0, line); ok {
			t.Errorf("不该解析成功: %q", line)
		}
	}
	// 只有标签行没有数据行 → 两个列表都为空
	serving, neighbours := cellpkg.ParseGTCCINFO([]string{"+GTCCINFO:", "NR service cell:", "OK"})
	if len(serving) != 0 || len(neighbours) != 0 {
		t.Errorf("没有数据行时不该有小区: %d/%d", len(serving), len(neighbours))
	}
}

// 全 0 的信号字段 = 模组未上报（转换后不能变成 -157 这种假读数）。
func TestParseGTCCINFORowAllZeroSignal(t *testing.T) {
	c, ok := cellpkg.ParseGTCCINFRow("NR", true, 0, "1,9,460,11,CFA102,CFA11C101,9AC80,1C0,78,100,0,0,0,0")
	if !ok {
		t.Fatal("结构合法就该解析成功")
	}
	if c.RSRP != 0 || c.RSRQ != 0 || c.SINR != 0 {
		t.Errorf("全 0 应记成未上报: rsrp=%d rsrq=%d sinr=%d", c.RSRP, c.RSRQ, c.SINR)
	}
}

// 端到端：整段 AT+GTCCINFO? 应答必须能被 IsGTCCINFOReport 认出来（决定走不走这套解析）。
func TestIsGTCCINFOReport(t *testing.T) {
	if !cellpkg.IsGTCCINFOReport(fm650GTCCINFOLines()) {
		t.Error("应识别出 GTCCINFO 应答")
	}
	if cellpkg.IsGTCCINFOReport([]string{"+QENG: \"servingcell\",\"NOCONN\",\"LTE\"", "OK"}) {
		t.Error("移远应答不该被当成 GTCCINFO")
	}
	_ = fm650GTCCINFO
}

// ---- from: fm160_test.go ----

//
// 固件 89611.1000.00.04.01.07，中国电信 460/11。
// 服务小区同样是自家 `AT+GTCCINFO?`（`AT+FENG` / `AT+QENG` / `AT+QNWINFO` / `AT+CPSI?` 全 ERROR），
// 字段口径与 FM650 一致，**但 band 是带 500 前缀的写法**（5078 → n78）。
// 温度与电压与 FM650 不同：见 quirks 里的 FM160 条目。

// ====================== 广和通 FM160-EAU（2026-09-29 实机抓包，AT 口 COM32/COM33） ======================
//
// 固件 89611.1000.00.04.01.07，中国电信 460/11。
// 服务小区同样是自家 `AT+GTCCINFO?`（`AT+FENG` / `AT+QENG` / `AT+QNWINFO` / `AT+CPSI?` 全 ERROR），
// 字段口径与 FM650 一致，**但 band 是带 500 前缀的写法**（5078 → n78）。
// 温度与电压与 FM650 不同：见 quirks 里的 FM160 条目。
func TestQuirkFM160(t *testing.T) {
	const model = "Manufacturer: Fibocom Wireless Inc.|Model: FM160-EAU|Revision: 89611.1000.00.04.01.07|OK"
	q := (&Modem{Model: model}).Quirk()
	if q.TempCmd != "AT+GTLADC" {
		t.Errorf("FM160 温度指令 = %q, want AT+GTLADC（AT+QTEMP/AT+MTSM 都回 ERROR）", q.TempCmd)
	}
	if q.VoltageCmd != "AT+CBC" {
		t.Errorf("FM160 电压指令 = %q, want AT+CBC", q.VoltageCmd)
	}
	// ⚠️ FM160 报的是本地时间且 +32 后缀正确 → 绝不能忽略时区（这点和 FM650/RM500U 相反）
	if q.IgnoreCCLKTZ {
		t.Error("FM160 的 AT+CCLK? 报的是本地时间，忽略时区后缀会净差 8 小时")
	}

	mod := &Modem{ID: 0, Model: model, cmdImpl: &fibocom.FibocomCommand{}}
	if got := mod.tempQueryCommand(); got != "AT+GTLADC" {
		t.Errorf("tempQueryCommand = %q, want AT+GTLADC", got)
	}
	if got := mod.voltageCommand(); got != "AT+CBC" {
		t.Errorf("voltageCommand = %q, want AT+CBC", got)
	}

	// 同厂两台别串味：FM650 仍走 AT+MTSM 且忽略时区；FM160 反过来
	q650 := (&Modem{Model: "Fibocom FM650-CN"}).Quirk()
	if q650.TempCmd != "AT+MTSM=1,6" || !q650.IgnoreCCLKTZ {
		t.Errorf("FM650 特例被影响: %+v", q650)
	}
	if q650.TempCmd == q.TempCmd {
		t.Error("FM160 与 FM650 的温度指令不该一样（实测 FM160 不认 AT+MTSM）")
	}
}

// FM160 的 AT+GTLADC 实测应答：两种写法、1/1000 ℃、0 与 -40960 是无效值
const fm160GTLADC = `AT+GTLADC
+GTLADC:
in_temp_pmx65_ambient_therm_input: 30562
in_temp_pmx65_die_temp_input: 26970
in_temp_pmx65_mdm_case_therm_input: 30843
in_temp_pmx65_pa_therm1_input: 29798
in_temp_pmx65_pa_therm2_input: 30020
in_temp_pmk8350_xo_therm_input: 30681
[zone0]:modem-lte-sub6-pa1 29804
[zone2]:modem-beamer 0
[zone3]:modem-wifi 30000
[zone5]:modem-bcl-warn 0
[zone11]:modem-sdr0 25000
[zone19]:modem-mmw0 0
[zone32]:cpuss-0-usr 31800
[zone34]:mdmss-0-usr 32100
[zone40]:modem-skin-usr 30843
[zone42]:modem-beamer-usr -40960
HWID gpio value: 000
OK`

func TestParseGTLADCFM160(t *testing.T) {
	temps := atparse.ParseGTLADC(strings.Split(fm160GTLADC, "\n"))
	if len(temps) == 0 {
		t.Fatal("应解析出温度传感器")
	}
	byName := map[string]int{}
	for _, s := range temps {
		byName[s.Name] = s.Celsius
	}
	// 冒号写法：名字去掉 in_temp_ 前缀与 _input 后缀，值按 1/1000 ℃ 四舍五入
	if got := byName["pmx65_mdm_case_therm"]; got != 31 { // 30843 → 30.843 → 31
		t.Errorf("pmx65_mdm_case_therm = %d, want 31", got)
	}
	if got := byName["pmx65_die_temp"]; got != 27 { // 26970 → 26.97 → 27
		t.Errorf("pmx65_die_temp = %d, want 27", got)
	}
	// 空格写法：[zoneN]:名字 值
	if got := byName["cpuss-0-usr"]; got != 32 { // 31800 → 31.8 → 32
		t.Errorf("cpuss-0-usr = %d, want 32", got)
	}
	if got := byName["mdmss-0-usr"]; got != 32 { // 32100 → 32.1 → 32
		t.Errorf("mdmss-0-usr = %d, want 32", got)
	}
	// 0（未上报）与负数（没接那路）都要丢掉
	if _, ok := byName["modem-mmw0"]; ok {
		t.Error("值为 0 的传感器不该出现")
	}
	if _, ok := byName["modem-beamer-usr"]; ok {
		t.Error("值为 -40960 的无效传感器不该出现")
	}
	if _, ok := byName["HWID"]; ok {
		t.Error("HWID gpio 行不是温度")
	}
	// 最高温在前（界面「全部 N 个传感器」照这个顺序渲染）
	for i := 1; i < len(temps); i++ {
		if temps[i].Celsius > temps[i-1].Celsius {
			t.Fatalf("温度没有按从高到低排序: %v", temps)
		}
	}
	// 关键测温点：机身穿 case、CPU 穿 cpu 关键词都要能被挑出来
	hot, keys := atparse.PickSummary(temps)
	if hot == nil || hot.Celsius != 32 {
		t.Errorf("最高温 = %+v, want 32℃", hot)
	}
	names := make([]string, 0, len(keys))
	for _, k := range keys {
		names = append(names, k.Name)
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "case") {
		t.Errorf("机身（case）测温点没挑出来: %s", joined)
	}
	if !strings.Contains(joined, "cpu") {
		t.Errorf("CPU 测温点没挑出来: %s", joined)
	}
}

// ParseTempResp 的分派：GTLADC 应答不能被前面几种格式抢走
func TestParseTempRespDispatchGTLADC(t *testing.T) {
	out := atparse.ParseTempResp(strings.Split(fm160GTLADC, "\n"))
	if len(out) == 0 {
		t.Fatal("AT+GTLADC 应答应能解析出温度")
	}
	if len(out) < 5 {
		t.Errorf("解析出的传感器太少（%d 个），可能把两种写法漏了一种", len(out))
	}
}

// FM160 的 AT+CBC（3GPP 两字段写法，与 FM650 同形）
func TestParseCBCFM160(t *testing.T) {
	if got := atparse.ParseCBC([]string{"+CBC: 0,3816", "OK"}); got != "3.816 V" {
		t.Errorf("FM160 +CBC = %q, want \"3.816 V\"", got)
	}
}

// FM160 的服务小区行：字段口径与 FM650 相同，但 **band 带 500 前缀**（5078 → 78）
//
//	+GTCCINFO:
//	NR service cell:
//	1,9,460,11,CFA102,CFA11A007,99240,207,5078,100,59,52,52,64
//	 TAC(hex) CellID(hex) ARFCN(hex) PCI(hex) band bw SINR RxLev RSRP RSRQ
//
// 交叉验证：TAC 0xCFA102 / CellID 0xCFA11A007 / ARFCN 0x99240=627264 与同基站的
// SIM8200、RM520N 上报的完全一致（同一 gNB 0xCFA11 的不同扇区）。
func TestParseGTCCINFOFM160Serving(t *testing.T) {
	serving, _ := cellpkg.ParseGTCCINFO([]string{
		"+GTCCINFO:",
		"NR service cell:",
		"1,9,460,11,CFA102,CFA11A007,99240,207,5078,100,59,52,52,64",
		"NR neighbor cell:",
		"OK",
	})
	if len(serving) != 1 {
		t.Fatalf("服务小区数 = %d, want 1", len(serving))
	}
	c := serving[0]
	if c.Tech != CellTechNR || c.MCC != "460" || c.MNC != "11" {
		t.Errorf("制式/PLMN 不对: %v %s/%s", c.Tech, c.MCC, c.MNC)
	}
	if c.TAC != 0xCFA102 || c.CellID != "CFA11A007" {
		t.Errorf("TAC/CellID 不对: %d / %q", c.TAC, c.CellID)
	}
	if c.EARFCN != 627264 || c.PCID != 0x207 {
		t.Errorf("ARFCN/PCI 不对: %d / %d（都是十六进制；PCI 0x207 = 519）", c.EARFCN, c.PCID)
	}
	// ⚠️ 5078 要按 500+band 归一成 78，否则界面会显示成"n5078"
	if c.Band != 78 || c.Bandwidth != 100 {
		t.Errorf("band/bw = %d/%d, want 78/100", c.Band, c.Bandwidth)
	}
	if c.RSRP != -105 || c.SINR != 6 || c.RSRQ != -12 {
		t.Errorf("信号 = %d/%d/%d, want -105/-12/6", c.RSRP, c.RSRQ, c.SINR)
	}
	if c.Operator != "中国电信" {
		t.Errorf("运营商 = %q, want 中国电信", c.Operator)
	}
}

// 过渡期（刚重选完）模组会把 TAC/ARFCN/PCI 报成 0：结构仍要能解析，不能整行丢掉
func TestParseGTCCINFOFM160Transition(t *testing.T) {
	serving, _ := cellpkg.ParseGTCCINFO([]string{
		"+GTCCINFO:",
		"LTE service cell:",
		"1,4,460,11,0,400E21E,0,0,105,50,6,141,141,40",
		"OK",
	})
	if len(serving) != 1 {
		t.Fatalf("服务小区数 = %d, want 1", len(serving))
	}
	c := serving[0]
	if c.Tech != CellTechLTE || c.CellID != "400E21E" {
		t.Errorf("制式/CellID 不对: %v / %q", c.Tech, c.CellID)
	}
	if c.Band != 5 || c.Bandwidth != 10 { // 105-100 = B5；50/5 = 10MHz
		t.Errorf("band/bw = %d/%d, want 5/10", c.Band, c.Bandwidth)
	}
	// TAC/ARFCN/PCI = 0 就是"模组还没解出来"，不该报错也不该编造
	if c.TAC != 0 || c.EARFCN != 0 || c.PCID != 0 {
		t.Errorf("0 值不该被编造: TAC=%d ARFCN=%d PCI=%d", c.TAC, c.EARFCN, c.PCID)
	}
}

// ---- from: fm350_test.go ----

//
// 与 FM650/FM160 同厂不同平台（FM350 是 MTK），命令一样但**很多细节都不一样**，
// 下面这些实测样例就是适配的全部依据，改解析时先跑这一组。

// ====================== 广和通 FM350-GL（2026-09-30 实机抓包，AT 口 COM5） ======================
//
// 与 FM650/FM160 同厂不同平台（FM350 是 MTK），命令一样但**很多细节都不一样**，
// 下面这些实测样例就是适配的全部依据，改解析时先跑这一组。
const fm350ATI = "ATI|Manufacturer: Fibocom Wireless Inc.|Model: FM350-GL|" +
	"Revision: 81600.0000.00.29.22.40|SVN: 08|IMEI: 867197050281381|OK"

// TestQuirkFM350 机型特例：时钟时区后缀不可信、电压 AT+CBC、温度 AT+GTSENRDTEMP=0、
// 本机地址要带 cid（AT+CGPADDR=2）。
func TestQuirkFM350(t *testing.T) {
	q := (&Modem{Model: fm350ATI}).Quirk()
	if !q.IgnoreCCLKTZ {
		t.Error("FM350 的 AT+CCLK? 带 +32 但报的是 UTC（与 FM650/RM500U 同病）→ 必须忽略时区后缀")
	}
	if q.VoltageCmd != "AT+CBC" {
		t.Errorf("FM350 电压指令 = %q, want AT+CBC（实测 +CBC: 0,4130 → 4.130 V）", q.VoltageCmd)
	}
	if q.TempCmd != "AT+GTSENRDTEMP=0" {
		t.Errorf("FM350 温度指令 = %q, want AT+GTSENRDTEMP=0"+
			"（AT+QTEMP/AT+GTLADC/AT+MTSM=1,6/AT+CPMUTEMP 全 ERROR；且必须带参数=0）", q.TempCmd)
	}
	if q.PDPAddrCmd != "AT+CGPADDR=2" {
		t.Errorf("FM350 本机地址指令 = %q, want AT+CGPADDR=2（不带 cid 的 AT+CGPADDR 回 ERROR）", q.PDPAddrCmd)
	}
	// 同厂两台别串味：FM650 仍走 AT+MTSM=1,6、不带 cid 的 CGPADDR
	q650 := (&Modem{Model: "Manufacturer: Fibocom|Model: FM650-CN|OK"}).Quirk()
	if q650.TempCmd != "AT+MTSM=1,6" || q650.PDPAddrCmd != "" {
		t.Errorf("FM650 特例被 FM350 影响: tempCmd=%q pdpAddrCmd=%q", q650.TempCmd, q650.PDPAddrCmd)
	}
	if q650.TempCmd == q.TempCmd {
		t.Error("FM350 与 FM650 的温度指令不该一样（实测两台机型互不认对方的指令）")
	}
}

// FM350 的时钟：`26/09/29,17:20:14+32`，主机本地 01:20（= UTC 17:20）→ 报的是 UTC，
// +32 后缀是假的。忽略后缀后 skew ≈ 0；按标准语义减 8 小时会净差 -28800s。
func TestParseCCLKFM350(t *testing.T) {
	resp := []string{"AT+CCLK?", `+CCLK: "26/09/29,17:20:14+32"`, "OK"}
	hostUTC := time.Date(2026, 9, 29, 17, 20, 14, 0, time.UTC)

	_, unix, skew, ok := atparse.ParseCCLK(resp, hostUTC, true) // 命中 Quirk：忽略时区
	if !ok {
		t.Fatal("FM350 的 +CCLK 应当能解析")
	}
	if unix != hostUTC.Unix() || skew != 0 {
		t.Errorf("忽略时区后 unix=%d skew=%d, want %d/0", unix, skew, hostUTC.Unix())
	}
	if _, _, wrongSkew, _ := atparse.ParseCCLK(resp, hostUTC, false); wrongSkew != -28800 {
		t.Errorf("不忽略时区会净差 8 小时（%d），这正是要开 ignoreCCLKTZ 的原因", wrongSkew)
	}
}

// ====================== 服务小区：AT+GTCCINFO?（无标签行、十进制 ARFCN/PCI） ======================

// 实机原始应答（2026-09-30，中国电信 460/11）：
//
//	1,9,460,11,CFA103,CFA9DF601,176190,172,,,-14,60,60,30
//
//	IsServiceCell=1  rat=9(NR)  MCC=460  MNC=11
//	TAC=0xCFA103     CellID=0xCFA9DF601
//	narfcn=176190(十进制 → 880.95MHz, 800M)  PCI=172(十进制)
//	band/bandwidth 该固件不填
//	SS-SINR=-14(已换算 dB)  RxLev=60  SS-RSRP=60  SS-RSRQ=30
func fm350GTCCINFO() []string {
	return []string{
		"AT+GTCCINFO?",
		"+GTCCINFO:",
		"1,9,460,11,CFA103,CFA9DF601,176190,172,,,-14,60,60,30",
		"OK",
	}
}

func TestParseGTCCINFOFM350Serving(t *testing.T) {
	serving, neighbours := cellpkg.ParseGTCCINFO(fm350GTCCINFO())
	if len(serving) != 1 || len(neighbours) != 0 {
		t.Fatalf("serving=%d neighbours=%d, want 1/0", len(serving), len(neighbours))
	}
	c := serving[0]
	if !c.Serving || c.Tech != CellTechNR {
		t.Errorf("应为 NR 服务小区: serving=%v tech=%v", c.Serving, c.Tech)
	}
	if c.MCC != "460" || c.MNC != "11" || c.Operator != "中国电信" {
		t.Errorf("PLMN/运营商 = %s/%s %q, want 460/11 中国电信", c.MCC, c.MNC, c.Operator)
	}
	// TAC / CellID 是十六进制（带字母，只可能是十六进制）
	if c.TAC != 0xCFA103 || c.CellID != "CFA9DF601" {
		t.Errorf("TAC/CellID = %d(0x%X)/%q, want 0xCFA103/CFA9DF601", c.TAC, c.TAC, c.CellID)
	}
	// ⚠️ ARFCN / PCI 是**十进制**：176190 按十进制 = 880.95MHz（n5/n26 下行），
	// 按十六进制是 1532560（FR1 里不存在）
	if c.EARFCN != 176190 {
		t.Errorf("EARFCN = %d, want 176190（十进制；按十六进制会得到 1532560）", c.EARFCN)
	}
	if c.PCID != 172 {
		t.Errorf("PCI = %d, want 172（十进制；按十六进制会得到 370）", c.PCID)
	}
	// band / bandwidth 该固件留空 → 不显示（界面「频段」会是 `-`，只有频点）
	if c.Band != 0 || c.Bandwidth != 0 {
		t.Errorf("band/bw = %d/%d, want 0/0（该固件不填这两格）", c.Band, c.Bandwidth)
	}
	// 第 2 格是 <rat>（这里是 9 = NR-RAN），不是连接状态 → 不能填进 State，
	// 否则界面「网络」卡片会显示"连接状态 9"
	if c.State != "" {
		t.Errorf("State = %q, want 空（`9` 是接入技术编号不是连接状态）", c.State)
	}
	// 信号：SS-RSRP = 索引-157、SS-RSRQ = 索引*0.5-43.5、SS-SINR 直接用（负数即 dB）
	if c.RSRP != -97 || c.RSSI != -97 {
		t.Errorf("RSRP/RxLev = %d/%d, want -97/-97", c.RSRP, c.RSSI)
	}
	if c.RSRQ != -29 {
		t.Errorf("RSRQ = %d, want -29（索引 30 → 30*0.5-43.5 = -28.5，四舍五入取 -29）", c.RSRQ)
	}
	if c.SINR != -14 {
		t.Errorf("SINR = %d, want -14（该固件直接给 dB，不是 0~127 的索引）", c.SINR)
	}
}

// 邻区行（手册 §11.1.15 的 NR 邻区是 12 格：`…,narfcn,pci,ss_sinr,rxlev,ss_rsrp,ss_rsrq`，
// 没有 band/bandwidth）。本机当前没上报邻区，所以这里按手册构造：
// 关键是**不能把 ss_sinr/rxlev 当 band/bandwidth**。
func TestParseGTCCINFOFM350Neighbour(t *testing.T) {
	serving, neighbours := cellpkg.ParseGTCCINFO([]string{
		"+GTCCINFO:",
		"2,9,,,,,176190,173,-9,48,48,20",
		"OK",
	})
	if len(serving) != 0 || len(neighbours) != 1 {
		t.Fatalf("serving=%d neighbours=%d, want 0/1", len(serving), len(neighbours))
	}
	n := neighbours[0]
	if n.Serving || n.Band != 0 || n.Bandwidth != 0 {
		t.Errorf("邻区不该有 band/bw（那两格是 SS-SINR/RxLev）: serving=%v band=%d bw=%d",
			n.Serving, n.Band, n.Bandwidth)
	}
	if n.EARFCN != 176190 || n.PCID != 173 {
		t.Errorf("邻区 ARFCN/PCI = %d/%d, want 176190/173（十进制）", n.EARFCN, n.PCID)
	}
	// RSRP 48-157 = -109；RSRQ 20*0.5-43.5 = -33.5 → 四舍五入 -34；SINR 直接取 -9 dB
	if n.RSRP != -109 || n.RSRQ != -34 || n.SINR != -9 {
		t.Errorf("邻区信号 = %d/%d/%d, want -109/-34/-9", n.RSRP, n.RSRQ, n.SINR)
	}
}

// 认不出制式的行不能被当成小区行（避免把别的指令的应答串进来）
func TestParseGTCCINFOFM350Rejects(t *testing.T) {
	if _, ok := cellpkg.ParseGTCCINFRowAuto("1,0,460,11,CFA103,CFA9DF601,176190,172,,,-14,60,60,30"); ok {
		t.Error("rat=0（无效网络）不该当成小区行")
	}
	if _, ok := cellpkg.ParseGTCCINFRowAuto("+CSQ: 99,99"); ok {
		t.Error("非小区行不该被 ParseGTCCINFRowAuto 认下")
	}
}

// 实机里这一行邻区**整行丢弃**：PCI 1711 超出 NR 的 0~1007，尾部三个字段还是零填充的
// 假值（同一分钟内 `…,1711,,026,026,03` 与 `…,1711,,126,126,77` 交替出现，
// 折算成 -131/-31 dBm 来回跳）。界面上出现「PCI 1711 · RSRP -31dBm」比空着更糟。
func TestParseGTCCINFOFM350DropsBogusNeighbour(t *testing.T) {
	for _, row := range []string{
		"2,9,460,11,CFA103F,CFA9DF600,176190,1711,,026,026,03",
		"2,9,460,11,CFA103F,CFA9DF600,176190,1711,,126,126,77",
	} {
		if _, ok := cellpkg.ParseGTCCINFRowAuto(row); ok {
			t.Errorf("PCI 越界的邻区行不该被采信: %q", row)
		}
	}
	// 服务小区行 PCI 172 合法 → 照常解析（别把好行也丢了）
	serving, neighbours := cellpkg.ParseGTCCINFO([]string{
		"+GTCCINFO:",
		"1,9,460,11,CFA103,CFA9DF601,176190,172,,,-14,60,60,30",
		"2,9,460,11,CFA103F,CFA9DF600,176190,1711,,126,126,77",
		"OK",
	})
	if len(serving) != 1 || len(neighbours) != 0 {
		t.Errorf("serving=%d neighbours=%d, want 1/0", len(serving), len(neighbours))
	}
}

// ====================== 信号：AT+CESQ 的广和通 9 格写法 ======================

// 实机原始应答（同一时刻抓，与上面的 GTCCINFO 交叉一致）：
//
//	+CESQ: 99,99,255,255,255,255,30,60,21
//	       └──── LTE/3G 的 6 格（驻留 NR 时 rsrq/rsrp 恒为 255 = 未上报）┘
//	                                    └─ ss_rsrq=30  ss_rsrp=60  ss_sinr=21 ─┘
//	→ RSRP 60-157 = -97dBm（与 GTCCINFO 的 -97 一致 ✓）
//	   RSRQ 30*0.5-43.5 = -28.5dB（与 GTCCINFO 的 -29 一致 ✓）
//	   SINR 21*0.5-23.5 = -13dB（与 GTCCINFO 的 -14 差 1dB，采样时刻不同 ✓）
func TestParseCESQSignalFM350(t *testing.T) {
	rsrp, rsrq, sinr, ok := cellpkg.ParseCESQSignal([]string{"AT+CESQ", "+CESQ: 99,99,255,255,255,255,30,60,21", "OK"})
	if !ok {
		t.Fatal("广和通 9 格 CESQ 应当能解析出 NR 信号")
	}
	if rsrp != -97 || rsrq != -29 || sinr != -13 {
		t.Errorf("= %d/%d/%d, want -97/-29/-13", rsrp, rsrq, sinr)
	}
}

// 回归：只有 6 格（LTE 字段有效）的写法行为不变（RM500U-CNV 实机样例）
func TestParseCESQSignalLTEUnchanged(t *testing.T) {
	rsrp, rsrq, sinr, ok := cellpkg.ParseCESQSignal([]string{`+CESQ: 99,99,255,255,16,25,255,255,255`})
	if !ok || rsrp != -115 || rsrq != -11 {
		t.Errorf("= %d/%d ok=%v, want -115/-11 true", rsrp, rsrq, ok)
	}
	if sinr != 0 {
		t.Errorf("LTE 写法没有 SINR，want 0, got %d", sinr)
	}
	// 老接口（两个返回值）保持可用
	if rsrp2, rsrq2, ok2 := cellpkg.ParseCESQ([]string{`+CESQ: 99,99,255,255,16,25`}); !ok2 || rsrp2 != -115 || rsrq2 != -11 {
		t.Errorf("ParseCESQ = %d/%d ok=%v, want -115/-11 true", rsrp2, rsrq2, ok2)
	}
}

// ====================== 温度：AT+GTSENRDTEMP=0（必须带参数） ======================

// 实机原始应答（23 路，1/1000 ℃；传感器 18 没接 → 回 0，要丢掉）
const fm350GTSENRDTEMP = `AT+GTSENRDTEMP=0
+GTSENRDTEMP: 1,31773
+GTSENRDTEMP: 2,30932
+GTSENRDTEMP: 3,30871
+GTSENRDTEMP: 4,30825
+GTSENRDTEMP: 5,30840
+GTSENRDTEMP: 6,31773
+GTSENRDTEMP: 7,31436
+GTSENRDTEMP: 8,31131
+GTSENRDTEMP: 9,30993
+GTSENRDTEMP: 10,30351
+GTSENRDTEMP: 11,31054
+GTSENRDTEMP: 12,30810
+GTSENRDTEMP: 13,30154
+GTSENRDTEMP: 14,29314
+GTSENRDTEMP: 15,29463
+GTSENRDTEMP: 16,30002
+GTSENRDTEMP: 17,34000
+GTSENRDTEMP: 18,0
+GTSENRDTEMP: 19,30043
+GTSENRDTEMP: 20,29405
+GTSENRDTEMP: 21,28868
+GTSENRDTEMP: 22,28868
+GTSENRDTEMP: 23,30406
OK`

func TestParseGTSTempFM350(t *testing.T) {
	temps := atparse.ParseTempResp(strings.Split(fm350GTSENRDTEMP, "\n"))
	if len(temps) != 22 {
		t.Fatalf("传感器数 = %d, want 22（23 路里第 18 路回 0 = 没接，要丢掉）", len(temps))
	}
	// 31773/1000 → 31.8 ℃；34000 → 34.0 ℃（最高，排最前）
	if temps[0].Celsius != 34 || temps[0].Name != "传感器 17" {
		t.Errorf("最高温 = %s %d℃, want 传感器 17 34℃", temps[0].Name, temps[0].Celsius)
	}
	for _, s := range temps {
		if s.Celsius <= 0 || s.Celsius > 125 {
			t.Errorf("%s 温度 %d 不合理（1/1000 ℃ 换算错了？）", s.Name, s.Celsius)
		}
	}
	// 热区写法（AT+GTZONERDMAXTEMP=0）用同一套解析
	if z := atparse.ParseTempResp([]string{"AT+GTZONERDMAXTEMP=0", "+GTZONERDMAXTEMP: 1,31605", "OK"}); len(z) != 1 ||
		z[0].Name != "热区 1" || z[0].Celsius != 32 {
		t.Errorf("热区解析不对: %+v", z)
	}
}

// 电压：AT+CBC 是 3GPP 两字段（第二个是 mV）
func TestParseCBCFM350(t *testing.T) {
	if got := atparse.ParseCBC([]string{"+CBC: 0,4130", "OK"}); got != "4.130 V" {
		t.Errorf("FM350 +CBC = %q, want \"4.130 V\"", got)
	}
}

// ====================== 本机 IP：AT+CGPADDR=2 ======================

// cid 1 是 IMS（只报接口标识），cid 2 才是上网用的 PDP（实机原始应答）
func TestParseCGPADDRFM350(t *testing.T) {
	ipv4, ipv6 := ParseCGPADDR([]string{
		`+CGPADDR: 2,"10.2.98.236","0.0.0.0.0.0.0.0.24.217.217.24.12.29.50.199"`,
		"OK",
	}, DefaultDataCID)
	if ipv4 != "10.2.98.236" {
		t.Errorf("IPv4 = %q, want 10.2.98.236", ipv4)
	}
	// ⚠️ 第二个字段前 8 字节全是 0（只有接口标识，没有前缀）→ 不能当成 IPv6 显示，
	// 否则界面上会出现 `::18d9:d918:c1d:32c7` 这种一看就不对的地址
	if ipv6 != "" {
		t.Errorf("IPv6 = %q, want 空（该 PDP 没下发前缀，模组只报了接口标识）", ipv6)
	}
}

// 前 64 位全 0 的"IPv6"一律视为未上报（FM350 的 CGPADDR 就是这种）；
// 完整地址（移远的老写法）照常解析出来，别误伤。
func TestHasZeroPrefixOnlyDropsIIDOnly(t *testing.T) {
	// FM350 实机：只报接口标识
	if v4, v6 := SplitAddrs("0.0.0.0.0.0.0.0.24.217.217.24.12.29.50.199"); v4 != "" || v6 != "" {
		t.Errorf("只有接口标识时 = %q / %q, want 空", v4, v6)
	}
	// 同一固件的 IMS 上下文（同样是接口标识）
	if _, v6 := SplitAddrs("0.0.0.0.0.0.0.0.24.217.217.20.143.53.184.88"); v6 != "" {
		t.Errorf("IMS 的接口标识也该丢掉: %q", v6)
	}
	// 标准冒号写法但前缀全 0 → 同样丢掉
	if _, v6 := SplitAddrs("::18d9:d918:c1d:32c7"); v6 != "" {
		t.Errorf("`::` 前缀的地址不该显示: %q", v6)
	}
	// 移远实机的完整地址（240e = 中国电信前缀）必须原样保留
	if _, v6 := SplitAddrs("10.149.102.75,36.14.4.0.6.24.12.75.24.217.129.210.10.3.26.30"); v6 != "240e:400:618:c4b:18d9:81d2:a03:1a1e" {
		t.Errorf("完整地址被误伤: %q", v6)
	}
	// 标准写法（带冒号）原样保留，只做小写化
	if _, v6 := SplitAddrs(`"10.161.107.67","240e:0400:5828:1856:18d9:c182:afc8:b63a"`); v6 != "240e:0400:5828:1856:18d9:c182:afc8:b63a" {
		t.Errorf("标准写法被误伤: %q", v6)
	}
}

// ====================== APN：AT+CGCONTRDP（<bearer_id> 是空的） ======================
// 该用例（含实机原始应答）已搬到 data.go（解析是纯逻辑）。

// ====================== 厂商指令探测：本机地址候选带上 cid ======================

func TestVendorPDPAddrCandidatesFM350(t *testing.T) {
	mod := &Modem{Model: fm350ATI}
	got := mod.vendorCandidates(KindPDPAddr)
	if len(got) != 2 || got[0] != "AT+CGPADDR=2" || got[1] != "AT+CGPADDR" {
		t.Errorf("FM350 本机地址候选 = %v, want [AT+CGPADDR=2 AT+CGPADDR]", got)
	}
	// 其它机型保持标准写法（不受影响）
	other := &Modem{Model: "Manufacturer: Quectel|Model: RM502Q-GL|OK"}
	if got := other.vendorCandidates(KindPDPAddr); len(got) != 1 || got[0] != "AT+CGPADDR" {
		t.Errorf("RM502Q 本机地址候选 = %v, want [AT+CGPADDR]", got)
	}
}

// ---- from: cell_simcom_test.go ----

// ---- from: cell_sim8200_test.go ----
//
// 这台固件的要点：
//  1. 服务小区/信号走 **AT+CPSI?**，不认移远的 AT+QENG="servingcell"（回 ERROR）；
//  2. +CPSI 的字段顺序与另一套写法（MT5700/MTK 系）**完全不同**：
//     多一个十六进制 TAC，频率字段带名字（NR5G_BAND78），频点在频率之后；
//  3. 最后三个信号值是 **×10 的整数**（-1000/-120/25 ↔ -100.0 dBm / -12.0 dB / 2.5 dB）。
// 实测原始行：+CPSI: NR5G_SA,Online,460-11,0xCFA102,55735066633,52,NR5G_BAND78,627264,-1000,-120,25
//
// 这台固件的要点：
//  1. 服务小区/信号走 **AT+CPSI?**，不认移远的 AT+QENG="servingcell"（回 ERROR）；
//  2. +CPSI 的字段顺序与另一套写法（MT5700/MTK 系）**完全不同**：
//     多一个十六进制 TAC，频率字段带名字（NR5G_BAND78），频点在频率之后；
//  3. 最后三个信号值是 **×10 的整数**（-1000/-120/25 ↔ -100.0 dBm / -12.0 dB / 2.5 dB）。
// 实测原始行：+CPSI: NR5G_SA,Online,460-11,0xCFA102,55735066633,52,NR5G_BAND78,627264,-1000,-120,25

func TestParseCPSICellSIM8200(t *testing.T) {
	line := `+CPSI: NR5G_SA,Online,460-11,0xCFA102,55735066633,52,NR5G_BAND78,627264,-1000,-120,25`
	cell, ok := cellpkg.ParseCPSICell(line)
	if !ok {
		t.Fatalf("解析失败: %s", line)
	}
	if cell.Tech != CellTechNR {
		t.Errorf("制式 = %v, want NR（NR5G_SA 要归一化成 NR）", cell.Tech)
	}
	if cell.MCC != "460" || cell.MNC != "11" {
		t.Errorf("PLMN = %s-%s, want 460-11", cell.MCC, cell.MNC)
	}
	if cell.Operator != "中国电信" {
		t.Errorf("运营商 = %q, want 中国电信", cell.Operator)
	}
	// CellID 统一成十六进制显示：模组报的十进制 55735066633 ↔ 0xCFA11A009
	if cell.CellID != "CFA11A009" {
		t.Errorf("CellID = %q, want CFA11A009", cell.CellID)
	}
	if cell.TAC != 0xCFA102 {
		t.Errorf("TAC = %d, want %d（0xCFA102）", cell.TAC, 0xCFA102)
	}
	if cell.PCID != 52 {
		t.Errorf("PCID = %d, want 52", cell.PCID)
	}
	if cell.Band != 78 || cell.EARFCN != 627264 {
		t.Errorf("频段/频点 = %d/%d, want 78/627264", cell.Band, cell.EARFCN)
	}
	// ×10 的信号值换算后：-1000/-120/25 → -100 / -12 / 2（25/10 取整）
	if cell.RSRP != -100 || cell.RSRQ != -12 || cell.SINR != 2 {
		t.Errorf("信号 = %d/%d/%d, want -100/-12/2", cell.RSRP, cell.RSRQ, cell.SINR)
	}
	if !cell.Serving {
		t.Error("CPSI 解析出来的应是服务小区")
	}
}

// 实测 LTE 模式下的原始行（同一台模组回落 LTE B5 时抓到的）：
//
//	+CPSI: LTE,Online,460-11,0x404A,67166750,155,EUTRAN-BAND5,2452,3,3,-113,-1035,-755,4
//
// 尾部按手册 §4.2.13 定义为 <dlbw>,<ulbw>,<RSRQ>,<RSRP>,<RSSI>,<RSSNR>，
// 值是 ×10 的整数：-113/-1035/-755/4 → RSRQ -11.3 dB / RSRP -103.5 dBm /
// RSSI -75.5 dBm / RSSNR 0.4 dB。
// 交叉验证：同一时刻 AT+CSQ 回 19 → RSSI = -113+2×19 = -75 dBm ✓ 与 -75.5 对得上。
func TestParseCPSICellSIM8200LTE(t *testing.T) {
	line := `+CPSI: LTE,Online,460-11,0x404A,67166750,155,EUTRAN-BAND5,2452,3,3,-113,-1035,-755,4`
	cell, ok := cellpkg.ParseCPSICell(line)
	if !ok {
		t.Fatalf("解析失败: %s", line)
	}
	if cell.Tech != CellTechLTE {
		t.Errorf("制式 = %v, want LTE", cell.Tech)
	}
	if cell.MCC != "460" || cell.MNC != "11" {
		t.Errorf("PLMN = %s-%s, want 460-11", cell.MCC, cell.MNC)
	}
	if cell.TAC != 16458 {
		t.Errorf("TAC = %d, want 16458（0x404A）", cell.TAC)
	}
	// 十进制 ECI 67166750 ↔ 0x400E21E（同一台设备上另一模组报的就是这个十六进制值）
	if cell.CellID != "400E21E" {
		t.Errorf("CellID = %q, want 400E21E", cell.CellID)
	}
	if cell.PCID != 155 || cell.Band != 5 || cell.EARFCN != 2452 {
		t.Errorf("小区字段串位了: PCID=%d Band=%d EARFCN=%d", cell.PCID, cell.Band, cell.EARFCN)
	}
	// RSRQ/RSRP 会丢小数（界面按整数显示）：-11.3 → -11、-103.5 → -103、RSSNR 0.4 → 0
	if cell.RSRP != -103 || cell.RSRQ != -11 || cell.RSSI != -75 {
		t.Errorf("信号 = RSRP %d / RSRQ %d / RSSI %d, want -103/-11/-75", cell.RSRP, cell.RSRQ, cell.RSSI)
	}
	if cell.SINR != 0 {
		t.Errorf("RSSNR 0.4 dB 截断后应为 0（界面显示未上报），got %d", cell.SINR)
	}
}

// EN-DC（NSA）的 NR 行：手册定义里**没有** PLMN / TAC / SCellID，只有 PCellID 开始
//
//	+CPSI: NR5G_NSA,[<PCellID>,<FrequencyBand>,<earfcn/ssb>,<RSRP>,<RSRQ>,<SNR>]
//
// 注意：这条是按手册字段定义构造的（本机一直是 NR5G_SA，没实抓到 NSA 行）。
func TestParseCPSICellSIM8200NSA(t *testing.T) {
	line := `+CPSI: NR5G_NSA,521,NR5G_BAND78,627264,-1080,-140,20`
	cell, ok := cellpkg.ParseCPSICell(line)
	if !ok {
		t.Fatalf("解析失败: %s", line)
	}
	if cell.Tech != CellTechNR || cell.PCID != 521 || cell.Band != 78 || cell.EARFCN != 627264 {
		t.Errorf("字段不对: %+v", cell)
	}
	if cell.RSRP != -108 || cell.RSRQ != -14 || cell.SINR != 2 {
		t.Errorf("信号 = %d/%d/%d, want -108/-14/2", cell.RSRP, cell.RSRQ, cell.SINR)
	}
	// NSA 行不带 PLMN / TAC / CellID，不应瞎填
	if cell.MCC != "" || cell.CellID != "" || cell.TAC != 0 {
		t.Errorf("NSA 行不该有 PLMN/TAC/CellID: %+v", cell)
	}
}

// 同一格数据的另一种上报刻度：固件直接按 dBm 报（无 ×10）时不能被误除 10
func TestParseCPSICellSIM8200Unscaled(t *testing.T) {
	line := `+CPSI: NR5G_SA,Online,460-11,0xCFA102,55735066633,52,NR5G_BAND78,627264,-100,-12,25`
	cell, ok := cellpkg.ParseCPSICell(line)
	if !ok {
		t.Fatalf("解析失败: %s", line)
	}
	if cell.RSRP != -100 || cell.RSRQ != -12 || cell.SINR != 25 {
		t.Errorf("信号 = %d/%d/%d, want -100/-12/25（没有超过 ±200 的值就不该除 10）",
			cell.RSRP, cell.RSRQ, cell.SINR)
	}
}

// 另一套写法（频段是裸数字）必须保持原样：回归测试
func TestParseCPSICellBareBandRegression(t *testing.T) {
	line := `+CPSI: NR5G,Online,460-11,0x1051E8F2,627264,504,78,100,-,-95,-12,-7,13`
	cell, ok := cellpkg.ParseCPSICell(line)
	if !ok {
		t.Fatalf("解析失败: %s", line)
	}
	if cell.CellID != "1051E8F2" || cell.EARFCN != 627264 || cell.PCID != 504 || cell.Band != 78 {
		t.Errorf("字段串位了: CellID=%q EARFCN=%d PCID=%d Band=%d",
			cell.CellID, cell.EARFCN, cell.PCID, cell.Band)
	}
	if cell.RSRP != -95 || cell.RSRQ != -12 || cell.SINR != -7 {
		t.Errorf("信号 = %d/%d/%d, want -95/-12/-7", cell.RSRP, cell.RSRQ, cell.SINR)
	}
}

// 字段不合规时必须整体放弃（宁可空白，也不能把 TAC 当 CellID 摆出来）
func TestParseCPSICellSIM8200RejectsBadFields(t *testing.T) {
	for _, line := range []string{
		// PCI 超范围（0~1007）
		`+CPSI: NR5G_SA,Online,460-11,0xCFA102,55735066633,1024,NR5G_BAND78,627264,-1000,-120,25`,
		// TAC 没有 0x 前缀 —— 说明字段顺序不是我们验证过的那套，不能瞎猜
		`+CPSI: NR5G_SA,Online,460-11,CFA102,55735066633,52,NR5G_BAND78,627264,-1000,-120,25`,
		// 频率号超范围
		`+CPSI: NR5G_SA,Online,460-11,0xCFA102,55735066633,52,NR5G_BAND300,627264,-1000,-120,25`,
		// 频点为 0
		`+CPSI: NR5G_SA,Online,460-11,0xCFA102,55735066633,52,NR5G_BAND78,0,-1000,-120,25`,
		// 制式不是 LTE/NR
		`+CPSI: GSM,Online,460-11,0xCFA102,55735066633,52,NR5G_BAND78,627264,-1000,-120,25`,
	} {
		if cell, ok := cellpkg.ParseCPSICell(line); ok {
			t.Errorf("不该解析成功: %s → %+v", line, cell)
		}
	}
}

// 信号没上报（"-"）时：小区结构照样要出来，只是信号留空
func TestParseCPSICellSIM8200NoSignal(t *testing.T) {
	line := `+CPSI: NR5G_SA,Online,460-11,0xCFA102,55735066633,52,NR5G_BAND78,627264,-,-,-`
	cell, ok := cellpkg.ParseCPSICell(line)
	if !ok {
		t.Fatalf("结构合法就该解析成功: %s", line)
	}
	if cell.RSRP != 0 || cell.RSRQ != 0 || cell.SINR != 0 {
		t.Errorf("信号项为 \"-\" 时应留空, got %d/%d/%d", cell.RSRP, cell.RSRQ, cell.SINR)
	}
	if cell.PCID != 52 || cell.Band != 78 {
		t.Errorf("小区信息不该受影响: %+v", cell)
	}
}

// TestAdaptiveServingCandidatesIncludeBalong 未识别厂商的扫描候选要包含华为/巴龙系的 ^ 指令。
//
// 以前候选只有 {QENG, CPSI?, QCSQ}，于是**未识别厂商的华为机型**（如 S10）永远扫不到服务小区 ——
// 尽管 ParseMONSC / ParseMONNCLines 早就在 ScanCells 里等着。加在最后是安全的：
// resolveVendorCmd 按顺序试，谁不被 ERROR 拒绝就用谁（QENG 命中的机型根本走不到这一条）。
func TestAdaptiveServingCandidatesIncludeBalong(t *testing.T) {
	cc := any(&common.UnknownCommand{}).(CommandCandidates)
	serving := cc.ServingCellCandidates()
	if len(serving) < 4 || serving[len(serving)-1] != "AT^MONSC" {
		t.Errorf("服务小区候选 = %v，最后一条应为 AT^MONSC", serving)
	}
	if serving[0] != common.CmdQENGServing {
		t.Errorf("首选仍是 %q（通用性最好），实际 %q", common.CmdQENGServing, serving[0])
	}
	neigh := cc.NeighbourCellCandidates()
	if len(neigh) < 2 || neigh[len(neigh)-1] != "AT^MONNC" {
		t.Errorf("邻区候选 = %v，最后一条应为 AT^MONNC", neigh)
	}
	// 解析器确实认得这两条的应答（否则加了候选也白加）
	// ^MONSC NR：<RAT>,<MCC>,<MNC>,<ARFCN>,<SCS>,<CellID>,<PCI hex>,<TAC hex>,<RSRP>,<RSRQ>,<SINR>
	sc, ok := cellpkg.ParseMONSC("^MONSC: NR,460,00,504990,15,760BF0114,115,1A2B3C,-89,-8,15")
	if !ok || sc.Tech != CellTechNR || sc.EARFCN != 504990 || sc.PCID != 277 {
		t.Fatalf("ParseMONSC 应能解析 ^MONSC 应答，得到 %+v ok=%v", sc, ok)
	}
	// ^MONNC NR：<ARFCN>,<PCI hex>,<RSRP>,<RSRQ>,<SINR>
	if got := cellpkg.ParseMONNCLines([]string{"^MONNC: NR,504990,115,-89,-8,15", "OK"}); len(got) != 1 || got[0].PCID != 277 {
		t.Errorf("ParseMONNCLines 应能解析 ^MONNC 应答，得到 %+v", got)
	}
}

// 芯讯通的指令集：服务小区首选 AT+CPSI?，不再首选移远的 QENG
func TestSimcomServingCellCommands(t *testing.T) {
	cmd := &simcom.SimcomCommand{}
	if got := cmd.CommandServingCell(); got != "AT+CPSI?" {
		t.Errorf("CommandServingCell = %q, want AT+CPSI?", got)
	}
	if got := cmd.CommandNRServingCell(); got != "AT+CPSI?" {
		t.Errorf("CommandNRServingCell = %q, want AT+CPSI?", got)
	}
	cands := any(cmd).(CommandCandidates).ServingCellCandidates()
	if len(cands) < 2 {
		t.Fatalf("候选数 = %d, want ≥2（%v）", len(cands), cands)
	}
	if cands[0] != "AT+CPSI?" {
		t.Errorf("首选应为 AT+CPSI?（实测 SIM8200 只认它），实际 %q", cands[0])
	}
	if cands[1] != common.CmdQENGServing {
		t.Errorf("第二候选应为 %q（部分型号复用移远写法），实际 %q", common.CmdQENGServing, cands[1])
	}
	// 流量计数器不提供 → 配额功能显示“不支持”，且不会每轮白发无效指令
	cc := any(cmd).(CommandCandidates)
	if got := cc.TrafficCandidates(); len(got) != 0 {
		t.Errorf("TrafficCandidates = %v, want 空（实测 QGDCNT 回 ERROR）", got)
	}
	if got := cc.TrafficEnableCommand(); got != "" {
		t.Errorf("TrafficEnableCommand = %q, want 空", got)
	}
}

// 芯讯通的 ParseNRSignal：+CPSI 能出信号值，QENG 写法（部分型号）也仍然认
func TestSimcomParseNRSignal(t *testing.T) {
	cmd := &simcom.SimcomCommand{}
	rsrp, rsrq, sinr, ok := cmd.ParseNRSignal(`+CPSI: NR5G_SA,Online,460-11,0xCFA102,55735066633,52,NR5G_BAND78,627264,-1000,-120,25`)
	if !ok || rsrp != -100 || rsrq != -12 || sinr != 2 {
		t.Errorf("CPSI 信号 = %d/%d/%d ok=%v, want -100/-12/2 true", rsrp, rsrq, sinr, ok)
	}
	// 结构对但没信号 → 不给信号值（让上层继续走别的兜底）
	if _, _, _, ok := cmd.ParseNRSignal(`+CPSI: NR5G_SA,Online,460-11,0xCFA102,55735066633,52,NR5G_BAND78,627264,-,-,-`); ok {
		t.Error("信号为 \"-\" 时不该报出信号值")
	}
	rsrp, rsrq, sinr, ok = cmd.ParseNRSignal(`+QENG: "servingcell","NR","FDD",460,00,1051E8F2,123,0,-95,-12,14,35`)
	if !ok || rsrp != -95 || rsrq != -12 || sinr != 14 {
		t.Errorf("QENG 信号 = %d/%d/%d ok=%v, want -95/-12/14 true", rsrp, rsrq, sinr, ok)
	}
	// 无关行（短信/注册）不能被当成信号
	for _, line := range []string{`+CMGL: 0,"REC UNREAD","13520309333",,"26/09/28,22:21:40+32"`, `+CSQ: 12,99`, `OK`} {
		if _, _, _, ok := cmd.ParseNRSignal(line); ok {
			t.Errorf("不该解析出信号: %s", line)
		}
	}
}

// ---- from: em05_test.go ----

//
// EM05（Quectel 4G Cat4，实测 2026-09-29，AT 口 COM20）与 5G 机型有四处差异：
//  ① AT+CCLK? 带 +32 后缀但报的是 UTC（与 RM500U 同一个毛病）→ quirks 里 ignoreCCLKTZ
//  ② AT+QTEMP 是**无名字**的三值写法：+QTEMP: 30,26,25
//  ③ AT+CBC 是三字段：+CBC: 0,71,3862（<bcs>,<bcl%>,<mV>），第二个字段是百分比不是毫伏
//  ④ 服务小区/邻区仍是经典 +QENG 单行写法（无需适配）
// ② 无名 QTEMP：三路温度都要出得来，名字统一叫"传感器 N"（不猜哪个是 CPU）

// ====================== 芯讯通/移远 EM05 等新机型的实测样例 ======================
//
// EM05（Quectel 4G Cat4，实测 2026-09-29，AT 口 COM20）与 5G 机型有四处差异：
//  ① AT+CCLK? 带 +32 后缀但报的是 UTC（与 RM500U 同一个毛病）→ quirks 里 ignoreCCLKTZ
//  ② AT+QTEMP 是**无名字**的三值写法：+QTEMP: 30,26,25
//  ③ AT+CBC 是三字段：+CBC: 0,71,3862（<bcs>,<bcl%>,<mV>），第二个字段是百分比不是毫伏
//  ④ 服务小区/邻区仍是经典 +QENG 单行写法（无需适配）

// ② 无名 QTEMP：三路温度都要出得来，名字统一叫"传感器 N"（不猜哪个是 CPU）
func TestParseQTEMPUnnamed(t *testing.T) {
	temps := atparse.ParseQTEMP([]string{"AT+QTEMP", "+QTEMP: 30,26,25", "OK"})
	if len(temps) != 3 {
		t.Fatalf("温度路数 = %d, want 3（%+v）", len(temps), temps)
	}
	if temps[0].Celsius != 30 || temps[1].Celsius != 26 || temps[2].Celsius != 25 {
		t.Errorf("温度值不对: %+v", temps)
	}
	if temps[0].Name != "传感器 1" || temps[2].Name != "传感器 3" {
		t.Errorf("无名写法应该给中性名字: %+v", temps)
	}
	// 最高温与关键测温点（匹配不到关键词时 keys 为空，界面靠最高温兜底）
	hot, _ := atparse.PickSummary(temps)
	if hot == nil || hot.Celsius != 30 {
		t.Errorf("最高温 = %+v, want 30℃", hot)
	}
	// 两种写法混在一起时都认（带名字的 + 无名的）
	mix := atparse.ParseQTEMP([]string{`+QTEMP:"sdx-case-therm-usr","31"`, "+QTEMP: 30,26,25"})
	if len(mix) != 4 {
		t.Errorf("混合写法应共 4 路, got %+v", mix)
	}
	// 非法值（超出 -40~125）整行丢弃，不显示可疑数字
	for _, bad := range []string{"+QTEMP: 999", "+QTEMP: -100,20", "+QTEMP: abc", "+QTEMP: 30,", "+CSQ: 20,99"} {
		if got := atparse.ParseQTEMP([]string{bad}); len(got) != 0 {
			t.Errorf("%q 不该解析出温度: %+v", bad, got)
		}
	}
}

// ③ 三字段 AT+CBC：必须取第三个字段（毫伏），不能把电量百分比 71 当成 71 mV
func TestParseCBCQuectelThreeFields(t *testing.T) {
	if got := atparse.ParseCBC([]string{"AT+CBC", "+CBC: 0,71,3862", "OK"}); got != "3.862 V" {
		t.Fatalf("EM05 格式 = %q, want \"3.862 V\"（取第三个字段）", got)
	}
	// 两字段（3GPP）与带单位（芯讯通）仍然认，互不干扰
	if got := atparse.ParseCBC([]string{"+CBC: 0,3858"}); got != "3.858 V" {
		t.Errorf("3GPP 两字段 = %q, want \"3.858 V\"", got)
	}
	if got := atparse.ParseCBC([]string{"+CBC: 3.858V"}); got != "3.858 V" {
		t.Errorf("芯讯通带单位 = %q, want \"3.858 V\"", got)
	}
	// 电压不在 2~6V 这类合理区间就放弃（宁可不显示）
	for _, bad := range []string{"+CBC: 0,71,0", "+CBC: 0,100,99999", "OK", "ERROR"} {
		if got := atparse.ParseCBC([]string{bad}); got != "" {
			t.Errorf("%q 不该解析出电压: %q", bad, got)
		}
	}
}

// ① 机型特例：EM05 要忽略 CCLK 时区后缀、并单独开电压；其它机型不能受影响
func TestQuirkEM05(t *testing.T) {
	cases := []struct {
		model       string
		ignoreTZ    bool
		wantVoltage string
	}{
		{"ATI|Quectel|EM05|Revision: EM05CEFCR06A04M1G_ND|OK", true, "AT+CBC"},
		{"ATI|Quectel|RM500U-CNV|OK", true, ""},             // 只忽略时区，不开电压
		{"ATI|Quectel|RM502Q-GL|OK", false, ""},             // 没命中任何特例
		{"ATI|SIMCOM INCORPORATED|SIM8200EA-M2", false, ""}, // 芯讯通走厂商接口，不靠机型特例
		{"", false, ""},
	}
	for _, c := range cases {
		q := (&Modem{Model: c.model}).Quirk()
		if q.IgnoreCCLKTZ != c.ignoreTZ {
			t.Errorf("%q: ignoreCCLKTZ = %v, want %v", c.model, q.IgnoreCCLKTZ, c.ignoreTZ)
		}
		if q.VoltageCmd != c.wantVoltage {
			t.Errorf("%q: voltageCmd = %q, want %q", c.model, q.VoltageCmd, c.wantVoltage)
		}
	}
	// 电压指令的取值链：机型特例 > 厂商接口 > 空串（不发指令）
	if got := (&Modem{Model: "Quectel EM05", cmdImpl: &quectel.QuectelCommand{}}).voltageCommand(); got != "AT+CBC" {
		t.Errorf("EM05 voltageCommand = %q, want AT+CBC（机型特例）", got)
	}
	if got := (&Modem{Model: "SIMCOM SIM8200EA-M2", cmdImpl: &simcom.SimcomCommand{}}).voltageCommand(); got != "AT+CBC" {
		t.Errorf("SIM8200 voltageCommand = %q, want AT+CBC（厂商接口）", got)
	}
	for _, m := range []struct {
		name  string
		model string
		impl  ModemCommand
	}{
		{"RM502Q-GL", "Quectel RM502Q-GL", &quectel.QuectelCommand{}},
		{"RM500U-CNV", "Quectel RM500U-CNV", &quectel.QuectelCommand{}},
		{"广和通", "Fibocom FM150", &fibocom.FibocomCommand{}},
		{"未知厂商", "", &common.UnknownCommand{}},
	} {
		mod := &Modem{ID: 1, Model: m.model, cmdImpl: m.impl}
		if got := mod.voltageCommand(); got != "" {
			t.Errorf("%s voltageCommand = %q, want 空（不发指令）", m.name, got)
		}
	}
}

// ④ EM05 的服务小区行保持经典写法：解析结果必须与实测一致（回归）
func TestEM05ServingCellRegression(t *testing.T) {
	const line = `+QENG: "servingcell","NOCONN","LTE","FDD",460,11,400E21E,155,2452,5,3,3,404A,-101,-11,-72,2,23`
	cell, ok := cellpkg.ParseCellLine(line)
	if !ok || !cell.Serving {
		t.Fatalf("服务小区解析失败: %s", line)
	}
	if cell.Tech != CellTechLTE || cell.Band != 5 || cell.EARFCN != 2452 || cell.PCID != 155 {
		t.Errorf("字段不对: %+v", cell)
	}
	if cell.CellID != "400E21E" || cell.TAC != 16458 {
		t.Errorf("CellID/TAC 不对: %q / %d", cell.CellID, cell.TAC)
	}
	if cell.RSRP != -101 || cell.RSRQ != -11 || cell.SINR != 2 {
		t.Errorf("信号不对: %d/%d/%d", cell.RSRP, cell.RSRQ, cell.SINR)
	}
}
