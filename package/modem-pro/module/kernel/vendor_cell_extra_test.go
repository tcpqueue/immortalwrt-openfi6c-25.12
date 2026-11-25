package kernel

import (
	"modemd/module/atparse"
	"modemd/module/cell"
	"modemd/module/common"
	"modemd/module/gosuncn"
	"modemd/module/meig"
	"modemd/module/neoway"
	"modemd/module/openluat"
	"modemd/module/quectel"
	"modemd/module/sierra"
	"modemd/module/telit"
	"modemd/module/thales"
	"modemd/module/unisoc"
	"strings"
	"testing"
)

// ====================== 未识别厂商的服务小区/信号解析（补充层）回归测试 ======================
//
// 样例行按 **QModem vendor/*.sh 的取值口径**构造（脚本里逐字段 awk 的那一套），
// 注释里写明每个值对应脚本里的哪一格，便于以后拿真机应答核对。

// TestParseDebugCellThalesLTE Thales/Foxconn 的 `AT^DEBUG`（vendor_thales.sh cell_info()）
func TestParseDebugCellThalesLTE(t *testing.T) {
	resp := []string{
		"AT^DEBUG?",
		"RAT: LTE",
		"mcc: 460,11",
		"mnc: 11",
		"channel: 1650",
		"pci: 301",
		"lte_cell_id: 12AB34",
		"lte_band: 3,20MHz",
		"lte_band_width: 20",
		"lte_snr: 12",
		"rsrq: -11",
		"lte_rsrp: -95",
		"lte_rssi: -57",
		"lte_tac: 1234",
		"lte_tx_pwr: 20",
		"OK",
	}
	c, ok := thales.DebugCell(resp)
	if !ok {
		t.Fatal("LTE 块没解析出来")
	}
	if c.Tech != CellTechLTE || !c.Serving {
		t.Errorf("tech/serving = %v/%v", c.Tech, c.Serving)
	}
	if c.MCC != "460" || c.MNC != "11" {
		t.Errorf("PLMN = %s/%s，want 460/11（脚本对 mcc 取逗号前那段）", c.MCC, c.MNC)
	}
	if c.Band != 3 {
		t.Errorf("band = %d，want 3（`lte_band: 3,20MHz` 取第一个数，不能平均）", c.Band)
	}
	if c.Bandwidth != 20 || c.EARFCN != 1650 || c.PCID != 301 || c.TAC != 1234 {
		t.Errorf("带宽/频点/PCI/TAC = %d/%d/%d/%d", c.Bandwidth, c.EARFCN, c.PCID, c.TAC)
	}
	if c.CellID != "12AB34" {
		t.Errorf("cell_id = %q", c.CellID)
	}
	if c.RSRP != -95 || c.RSRQ != -11 || c.SINR != 12 || c.RSSI != -57 {
		t.Errorf("信号 = %d/%d/%d/%d，want -95/-11/12/-57", c.RSRP, c.RSRQ, c.SINR, c.RSSI)
	}
}

// TestParseDebugCellNR Thales 的 NR5G_SA 走另一套字段名
func TestParseDebugCellNR(t *testing.T) {
	resp := []string{
		"RAT: NR5G_SA",
		"mcc: 460,11",
		"mnc: 11",
		"channel: 627264",
		"pci: 521",
		"nr_cell_id: CFA116002",
		"nr_band: 78,100MHz",
		"nr_band_width: 100",
		"nr_snr: 25",
		"rsrp: -89",
		"rsrq: -11",
		"nr_tac: 115",
	}
	c, ok := thales.DebugCell(resp)
	if !ok || c.Tech != CellTechNR {
		t.Fatalf("NR 块没解析出来：%+v ok=%v", c, ok)
	}
	if c.Band != 78 || c.Bandwidth != 100 || c.EARFCN != 627264 || c.PCID != 521 || c.TAC != 115 {
		t.Errorf("小区字段 = band %d bw %d arfcn %d pci %d tac %d", c.Band, c.Bandwidth, c.EARFCN, c.PCID, c.TAC)
	}
	if c.RSRP != -89 || c.RSRQ != -11 || c.SINR != 25 {
		t.Errorf("信号 = %d/%d/%d", c.RSRP, c.RSRQ, c.SINR)
	}
	if c.CellID != "CFA116002" {
		t.Errorf("cell_id = %q", c.CellID)
	}
}

// TestDebugCellRejectsOthers 别的厂商的应答不能被这套解析吃掉
func TestDebugCellRejectsOthers(t *testing.T) {
	if _, ok := thales.DebugCell([]string{`+QENG: "servingcell","NOCONN","LTE"`, "OK"}); ok {
		t.Error("+QENG 行不该被 AT^DEBUG 解析认出")
	}
	if _, ok := thales.DebugCell([]string{"+CPSI: LTE,460,11", "OK"}); ok {
		t.Error("+CPSI 行不该被 AT^DEBUG 解析认出")
	}
}

// TestParseSierraGStatus Sierra 的 `AT!GSTATUS?`（vendor_sierra.sh network_info()）
func TestParseSierraGStatus(t *testing.T) {
	resp := []string{
		"!GSTATUS:",
		"Current Time:  1234		Temperature: 33",
		"System mode:   LTE",
		"PS state:      Attached",
		"LTE band:      B3",
		"LTE bw:        20 MHz",
		"LTE Rx chan:   1650",
		"LTE Cell ID:   1234567",
		"LTE PCI:       301",
		"LTE TAC:       1234",
		"LTE RSRP:      -95",
		"LTE RSRQ:      -11",
		"LTE RSSI:      -57",
		"LTE SNR:       12.5",
		"OK",
	}
	c, ok := sierra.GStatus(resp)
	if !ok {
		t.Fatal("GSTATUS 没解析出来")
	}
	if c.Tech != CellTechLTE || c.Band != 3 || c.EARFCN != 1650 || c.PCID != 301 || c.TAC != 1234 {
		t.Errorf("小区字段 = tech %v band %d arfcn %d pci %d tac %d", c.Tech, c.Band, c.EARFCN, c.PCID, c.TAC)
	}
	if c.CellID != "1234567" {
		t.Errorf("cell_id = %q", c.CellID)
	}
	// `LTE SNR` = SINR（真机就是 SNR 写法；脚本只认 SINR 会漏，本实现宽容一格）
	if c.RSRP != -95 || c.RSRQ != -11 || c.RSSI != -57 || c.SINR != 12 {
		t.Errorf("信号 = %d/%d/%d/%d，want -95/-11/-57/12", c.RSRP, c.RSRQ, c.RSSI, c.SINR)
	}
	// 带两个键的一行（Current Time / Temperature）不能被当成小区字段
	if strings.Contains(c.Raw, "Current Time") == false {
		t.Errorf("原始行应保留：%q", c.Raw)
	}
}

// TestSierraTempAndVolt AT!PCTEMP? / AT[!^]PCVOLT?
func TestSierraTempAndVolt(t *testing.T) {
	if got := sierra.ParsePCTemp([]string{"AT!PCTEMP?", "Temperature: 33.5", "OK"}); len(got) != 1 || got[0].Celsius != 33 {
		t.Errorf("ParseSierraPCTemp = %+v", got)
	}
	if got := sierra.ParsePCVolt([]string{"AT!PCVOLT?", "Power supply voltage: 3860 mV", "OK"}); got != "3.860 V" {
		t.Errorf("ParsePCVolt = %q，want 3.860 V", got)
	}
	// 总入口两种写法都认，认不出返回空
	if got := sierra.ParseVoltage([]string{"+CBC: 0,4130"}); got != "4.130 V" {
		t.Errorf("ParseVoltage(+CBC) = %q", got)
	}
	if got := sierra.ParseVoltage([]string{"AT!PCVOLT?", "Power supply voltage: 3860 mV"}); got != "3.860 V" {
		t.Errorf("ParseVoltage(PCVOLT) = %q", got)
	}
	if got := sierra.ParseVoltage([]string{"ERROR"}); got != "" {
		t.Errorf("认不出应返回空：%q", got)
	}
	// 温度总入口也要能接上（两家只会中一条）
	if got := atparse.ParseTempResp([]string{"Temperature: 41.5"}); len(got) != 1 || got[0].Celsius != 41 {
		t.Errorf("ParseTempResp(Sierra) = %+v", got)
	}
}

// TestParseMeigCellInfo 美格 `AT^CELLINFO=<cid>`（vendor_meig.sh cell_info()）
func TestParseMeigCellInfo(t *testing.T) {
	// NR(5G)：f1=制式 f2=双工 f3=MCC f4=MNC f5=cell_id f6=PCI f7=TAC f8=band f9=带宽
	//        f15=RSRP f16=RSRQ f17=SINR(÷10)  ← f11~f14 是脚本没用的几格
	nr := "5G,TDD,460,11,1234567,301,1234,78,100,1,0,0,0,0,-95,-11,125"
	c, ok := meig.ParseCellInfo([]string{"^CELLINFO: " + nr, "OK"})
	if !ok || c.Tech != CellTechNR {
		t.Fatalf("NR 没解析出来：%+v ok=%v", c, ok)
	}
	if c.MCC != "460" || c.MNC != "11" || c.CellID != "1234567" || c.PCID != 301 || c.TAC != 1234 ||
		c.Band != 78 || c.Bandwidth != 100 {
		t.Errorf("小区字段 = %+v", c)
	}
	if c.RSRP != -95 || c.RSRQ != -11 || c.SINR != 12 {
		t.Errorf("信号 = %d/%d/%d，want -95/-11/12（SINR 要 ÷10）", c.RSRP, c.RSRQ, c.SINR)
	}

	// EN-DC：f6=PCI f8=cell_id f9=TAC f10=band f15/16/17=信号（只取主小区，不猜 NR 辅小区）
	endc := "LTE-NR,FDD,460,11,-,301,-,1234567,1234,3,20,0,0,0,-101,-13,95,0,0,0,0,200"
	c2, ok := meig.ParseCellInfo([]string{"^CELLINFO: " + endc})
	if !ok || c2.State != "EN-DC" {
		t.Fatalf("EN-DC 没解析出来：%+v ok=%v", c2, ok)
	}
	if c2.PCID != 301 || c2.CellID != "1234567" || c2.TAC != 1234 || c2.Band != 3 {
		t.Errorf("EN-DC 主小区字段 = %+v", c2)
	}
	if c2.RSRP != -101 || c2.SINR != 9 {
		t.Errorf("EN-DC 信号 = %d/%d/%d", c2.RSRP, c2.RSRQ, c2.SINR)
	}

	// 别的格式不吃
	if _, ok := meig.ParseCellInfo([]string{"+CPSI: LTE,460,11"}); ok {
		t.Error("+CPSI 不该被美格解析认出")
	}
}

// TestParseZCellInfo 高新兴 lte 平台 `AT+ZCELLINFO?`（vendor_gosuncn.sh cell_info_lte()）
func TestParseZCellInfo(t *testing.T) {
	c, ok := gosuncn.ZCellInfo([]string{"AT+ZCELLINFO?", "+ZCELLINFO: 1234,cellid:12AB34,pci:301,band:3", "OK"})
	if !ok {
		t.Fatal("ZCELLINFO 没解析出来")
	}
	if c.TAC != 1234 || c.CellID != "12AB34" || c.PCID != 301 || c.Band != 3 || c.Tech != CellTechLTE {
		t.Errorf("字段 = %+v", c)
	}
}

// TestParseCAInfoExt Telit `AT#CAINFOEXT?`（vendor_telit.sh cell_info()）
func TestParseCAInfoExt(t *testing.T) {
	resp := []string{
		"AT#CAINFOEXT?",
		"#CAINFOEXT: 2, LTE",
		"PCC- BandClass: 3 , BW: 20 , CH: 1650 , PCI: 301 , RSRP: -95 , RSRQ: -11 , RSSI: -57 , SINR: 30 , TAC: 1234 , TX_PWR: 200",
		"SCC0- BandClass: 1 , BW: 20 , CH: 300 , PCI: 100 , RSRP: -101 , RSRQ: -13 , SINR: 25",
		"OK",
	}
	c, ca, ok := telit.CAInfoExt(resp)
	if !ok {
		t.Fatal("CAINFOEXT 没解析出来")
	}
	if c.Tech != CellTechLTE || c.Band != 3 || c.EARFCN != 1650 || c.PCID != 301 || c.TAC != 1234 {
		t.Errorf("主载波字段 = %+v", c)
	}
	// SINR 按脚本口径换算：dB = -20 + raw*0.2 → 30 → -14；25 → -15
	if c.RSRP != -95 || c.RSRQ != -11 || c.RSSI != -57 || c.SINR != -14 {
		t.Errorf("主载波信号 = %d/%d/%d/%d，want -95/-11/-57/-14", c.RSRP, c.RSRQ, c.RSSI, c.SINR)
	}
	if len(ca) != 1 || ca[0].Band != 1 || ca[0].PCID != 100 || ca[0].SINR != -15 {
		t.Errorf("辅载波 = %+v", ca)
	}
	// 辅载波必须带 Role（CCsAggregated 靠它判断），BandLabel 与主载波一样由频段号推
	if ca[0].Role != "SCC" || ca[0].BandLabel != "B1" || ca[0].Tech != CellTechLTE {
		t.Errorf("辅载波角色/标签 = %+v", ca[0])
	}
	// 辅载波是"分量载波"、不是"小区"：服务小区入口只给主载波（不给重复两份）
	got := cell.VendorServingCells(resp)
	if len(got) != 1 || got[0].PCID != 301 || !got[0].Serving {
		t.Errorf("服务小区入口 = %+v，want 只有主载波", got)
	}
}

// TestParseNetDMsgEx 有方 `AT+NETDMSGEX`（vendor_neoway.sh cell_info() 逐格取证）
func TestParseNetDMsgEx(t *testing.T) {
	// LTE：f5=TAC f6=cell_id f7=PCI f10~f13=RSRP/RSRQ/SINR/RSSI（均 ÷10）
	lte := `+NETDMSGEX: "FDD LTE","460+11","3",1650,1234,1234567,301,-70,-20,-950,-110,125,-570,0,0,20,20`
	c, ok := neoway.NetDMsgEx([]string{lte, "OK"})
	if !ok || c.Tech != CellTechLTE {
		t.Fatalf("LTE 没解析出来：%+v ok=%v", c, ok)
	}
	if c.MCC != "460" || c.MNC != "11" || c.TAC != 1234 || c.CellID != "1234567" || c.PCID != 301 ||
		c.Band != 3 || c.EARFCN != 1650 {
		t.Errorf("LTE 小区字段 = %+v", c)
	}
	if c.RSRP != -95 || c.RSRQ != -11 || c.SINR != 12 || c.RSSI != -57 {
		t.Errorf("LTE 信号 = %d/%d/%d/%d，want -95/-11/12/-57", c.RSRP, c.RSRQ, c.SINR, c.RSSI)
	}

	// NR：f5=cell_id f6=PCI f7/f8/f9=SS-RSRP/RSRQ/SINR（÷10）
	nr := `+NETDMSGEX: "NR to 5GCN","460+11","78",627264,CFA116002,521,-890,-110,255`
	c2, ok := neoway.NetDMsgEx([]string{nr})
	if !ok || c2.Tech != CellTechNR {
		t.Fatalf("NR 没解析出来：%+v ok=%v", c2, ok)
	}
	if c2.CellID != "CFA116002" || c2.PCID != 521 || c2.Band != 78 || c2.EARFCN != 627264 {
		t.Errorf("NR 小区字段 = %+v", c2)
	}
	if c2.RSRP != -89 || c2.RSRQ != -11 || c2.SINR != 25 {
		t.Errorf("NR 信号 = %d/%d/%d，want -89/-11/25", c2.RSRP, c2.RSRQ, c2.SINR)
	}

	// 别的格式不吃
	if _, ok := neoway.NetDMsgEx([]string{"+CPSI: LTE,460,11"}); ok {
		t.Error("+CPSI 不该被有方解析认出")
	}
}

// TestParseZCellInfoQualcomm 高新兴 qualcomm 平台的标签形 `+ZCELLINFO:NR5G:…`
func TestParseZCellInfoQualcomm(t *testing.T) {
	nr := "+ZCELLINFO:NR5G:tac:115,cell_id:CFA116002,pci:521,band:78,freq:627264,mcc:460,mnc:11,rsrp:-89,rsrq:-11,rssi:-57,sinr:25,"
	c, ok := gosuncn.ZCellInfo([]string{nr, "OK"})
	if !ok || c.Tech != CellTechNR {
		t.Fatalf("qualcomm NR 没解析出来：%+v ok=%v", c, ok)
	}
	if c.TAC != 115 || c.CellID != "CFA116002" || c.PCID != 521 || c.Band != 78 || c.EARFCN != 627264 {
		t.Errorf("字段 = %+v", c)
	}
	if c.MCC != "460" || c.MNC != "11" || c.RSRP != -89 || c.RSRQ != -11 || c.RSSI != -57 || c.SINR != 25 {
		t.Errorf("PLMN/信号 = %+v", c)
	}
	lte := "+ZCELLINFO:LTE:tac:1234,cell_id:12AB34,pci:301,band:3,freq:1650,mcc:460,mnc:11,rsrp:-95,rsrq:-11,rssi:-57,sinr:12"
	c2, ok := gosuncn.ZCellInfo([]string{lte})
	if !ok || c2.Tech != CellTechLTE || c2.TAC != 1234 || c2.PCID != 301 || c2.Band != 3 {
		t.Errorf("qualcomm LTE 字段 = %+v ok=%v", c2, ok)
	}
}

// TestSierraGStatusLegacyKeys MC735x 老固件的键没有 `LTE `/`NR5G ` 前缀
func TestSierraGStatusLegacyKeys(t *testing.T) {
	resp := []string{
		"!GSTATUS:",
		"System mode: LTE",
		"Band: B3",
		"Rx chan: 1650",
		"PCI: 301",
		"Cell ID: 1234567",
		"TAC: 1234",
		"RSRP: -95",
		"RSRQ: -11",
		"RSSI: -57",
		"SNR: 12",
		"OK",
	}
	c, ok := sierra.GStatus(resp)
	if !ok {
		t.Fatal("老固件键名没解析出来")
	}
	if c.Band != 3 || c.EARFCN != 1650 || c.PCID != 301 || c.TAC != 1234 || c.CellID != "1234567" {
		t.Errorf("字段 = %+v", c)
	}
	if c.RSRP != -95 || c.RSRQ != -11 || c.RSSI != -57 || c.SINR != 12 {
		t.Errorf("信号 = %d/%d/%d/%d", c.RSRP, c.RSRQ, c.RSSI, c.SINR)
	}
	// 认领门槛：没有 !GSTATUS / System mode 的块（如 Thales DEBUG）不能被 Sierra 解析吃掉
	if _, ok := sierra.GStatus([]string{"RAT: LTE", "rsrp: -95", "pci: 301"}); ok {
		t.Error("Thales 风格的块不该被 ParseSierraGStatus 认领")
	}
}

// TestParseMeigDSAMBR 美格 `AT^DSAMBR=<cid>`：NR 布局是 f9=UL / f10=DL（vendor/meig.sh）
func TestParseMeigDSAMBR(t *testing.T) {
	line := `^DSAMBR: 1,0,0,0,0,0,0,0,1000000,1000000,"ctnet"`
	r, ok := meig.ParseDSAMBR([]string{"AT^DSAMBR=1", line, "OK"})
	if !ok || r == nil {
		t.Fatalf("美格 AMBR 没解析出来：%+v ok=%v", r, ok)
	}
	if r.ULMbps != 1000 || r.DLMbps != 1000 {
		t.Errorf("AMBR = UL %v / DL %v，want 1000/1000", r.ULMbps, r.DLMbps)
	}
	// 字段不够（LTE 那种含糊布局）→ 未上报，不编数字
	if _, ok := meig.ParseDSAMBR([]string{"^DSAMBR: 1,1000000,1000000"}); ok {
		t.Error("字段数不足时不该硬解")
	}
}

// TestAMBRCommandQuirk 机型特例里的 AMBR 指令要能被 ambrCommand 取到（美格 SRM / 有方 N510M）
func TestAMBRCommandQuirk(t *testing.T) {
	meig := &Modem{Vendor: VendorUnknown, Model: "SRM815", ModelName: "SRM815", cmdImpl: NewVendorCommand(VendorUnknown)}
	if got := meig.ambrCommand(); got != "AT^DSAMBR=1" {
		t.Errorf("美格 AMBR 指令 = %q，want AT^DSAMBR=1", got)
	}
	n510 := &Modem{Vendor: VendorUnknown, Model: "N510M", ModelName: "N510M", cmdImpl: NewVendorCommand(VendorUnknown)}
	if got := n510.ambrCommand(); got != "AT+C5GQOSRDP" {
		t.Errorf("有方 AMBR 指令 = %q，want AT+C5GQOSRDP", got)
	}
	// 别的机型不受影响：移远走自己的 AMBRQuerier，未声明的返回空
	if got := (&Modem{Model: "RM520N-GL", cmdImpl: &quectel.QuectelCommand{}}).ambrCommand(); got != `AT+QNWCFG="nr5g_ambr"` {
		t.Errorf("移远 AMBR 指令 = %q", got)
	}
	if got := (&Modem{Model: "T99W175", cmdImpl: NewVendorCommand(VendorUnknown)}).ambrCommand(); got != "" {
		t.Errorf("Foxconn 不该凭空多出 AMBR 指令：%q", got)
	}
}

// TestParseC5GQOSRDP 有方 `AT+C5GQOSRDP`：3GPP 5GS QoS 布局，f7=DL_SAMBR / f8=UL_SAMBR（kbps）
func TestParseC5GQOSRDP(t *testing.T) {
	// +C5GQOSRDP: <cid>,<5QI>,<DL_GFBR>,<UL_GFBR>,<DL_MFBR>,<UL_MFBR>,<DL_SAMBR>,<UL_SAMBR>
	line := "+C5GQOSRDP: 1,9,100,50,200,100,1024000,512000"
	r, ok := unisoc.ParseC5GQOSRDP([]string{"AT+C5GQOSRDP", line, "OK"})
	if !ok || r == nil {
		t.Fatalf("有方 AMBR 没解析出来：%+v ok=%v", r, ok)
	}
	if r.DLMbps != 1024 || r.ULMbps != 512 {
		t.Errorf("AMBR = DL %d / UL %d，want 1024/512（f7/f8，脚本取的就是 $7/$8）", r.DLMbps, r.ULMbps)
	}
	if r.QCI != 9 {
		t.Errorf("QCI(5QI) = %d，want 9（f2；脚本没取，本项目取出来给界面显示）", r.QCI)
	}
	if r.CID != 1 {
		t.Errorf("CID = %d，want 1（f1 的 cid：选「拨号那条」时用）", r.CID)
	}
	// 负例：段数不足（没有 MFBR/SAMBR 那两段）、占位 `-`、全 0、别的指令前缀
	for _, resp := range [][]string{
		{"+C5GQOSRDP: 1,9,100,50"},                   // 4 段（没有 SAMBR）
		{"+C5GQOSRDP: 1,9,-,-,-,-,-,-"},              // 占位符
		{"+C5GQOSRDP: 1,9,100,50,200,100,0,0"},       // 全 0 = 未上报
		{"+CGDCONT: 1,\"IP\",\"ctnet\""},             // 别的指令
		{"^DSAMBR: 1,0,0,0,0,0,0,0,1000000,1000000"}, // 美格那条不能被它认领
	} {
		if _, ok := unisoc.ParseC5GQOSRDP(resp); ok {
			t.Errorf("%v 不该解析成有方 AMBR", resp)
		}
	}
}

// TestParseC5GQOSRDPQCI QCI（5QI）无效时只丢它、不影响签约速率：占位 `-` / 越界都记成未上报(0)
func TestParseC5GQOSRDPQCI(t *testing.T) {
	r, ok := unisoc.ParseC5GQOSRDP([]string{"+C5GQOSRDP: 1,-,100,50,200,100,1024000,512000"})
	if !ok || r == nil {
		t.Fatal("5QI 是占位 `-` 时 AMBR 仍应解析成功")
	}
	if r.QCI != 0 {
		t.Errorf("占位 5QI 应记未上报(0)，got %d", r.QCI)
	}
	r, ok = unisoc.ParseC5GQOSRDP([]string{"+C5GQOSRDP: 1,999,100,50,200,100,1024000,512000"})
	if !ok || r == nil {
		t.Fatal("5QI 越界时 AMBR 仍应解析成功")
	}
	if r.QCI != 0 {
		t.Errorf("越界 5QI(999) 应记未上报(0)，got %d", r.QCI)
	}
	// 上限内的边界值照收（255 是 5QI 的合法上界）
	if r, ok := unisoc.ParseC5GQOSRDP([]string{"+C5GQOSRDP: 1,255,100,50,200,100,1024000,512000"}); !ok || r.QCI != 255 {
		t.Errorf("边界 5QI(255) 应保留：%+v ok=%v", r, ok)
	}
}

// TestAMBRDispatchByCommand 解析分派以**指令文本**为准（不能"凡是 Quirk.AMBRCmd 就走美格那套"）
func TestAMBRDispatchByCommand(t *testing.T) {
	n510 := &Modem{Vendor: VendorUnknown, Model: "N510M", ModelName: "N510M", cmdImpl: NewVendorCommand(VendorUnknown)}
	r, ok := parseAMBRForModem(n510, "AT+C5GQOSRDP", []string{"+C5GQOSRDP: 1,9,100,50,200,100,1024000,512000"})
	if !ok || r.DLMbps != 1024 || r.ULMbps != 512 {
		t.Errorf("C5GQOSRDP 分派错误：%+v ok=%v", r, ok)
	}
	meig := &Modem{Vendor: VendorUnknown, Model: "SRM815", ModelName: "SRM815", cmdImpl: NewVendorCommand(VendorUnknown)}
	r, ok = parseAMBRForModem(meig, "AT^DSAMBR=1", []string{`^DSAMBR: 1,0,0,0,0,0,0,0,1000000,1000000,"ctnet"`})
	if !ok || r.ULMbps != 1000 || r.DLMbps != 1000 {
		t.Errorf("DSAMBR 分派错误：%+v ok=%v", r, ok)
	}
	// 指令与机型特例不符时走通用解析（这里用移远那套布局，确认没有被送进美格/有方解析器）
	if _, ok := parseAMBRForModem(n510, `AT+QNWCFG="nr5g_ambr"`, []string{"OK"}); ok {
		t.Error("指令与特例不符时不该用特例解析器")
	}
}

// TestTelitCAOps Telit 的 CA 走**机型特例扩展**：与服务小区同一条 AT#CAINFOEXT?（vendor_telit.sh）
func TestTelitCAOps(t *testing.T) {
	if caOpsForModel("FN990A40") == nil {
		t.Error("Telit FN990A40 应挂上 CA 扩展（它没有专门的厂商指令类型）")
	}
	if caOpsForModel("RM520N-GL") != nil {
		t.Error("移远不该走机型特例的 CA（它有自己的 AT+QCAINFO）")
	}
	ops := telitCAOps{}
	if got := ops.CACmds(); len(got) != 1 || got[0] != "AT#CAINFOEXT?" {
		t.Errorf("CA 指令 = %v", got)
	}
	if ops.CAEmptyNote() == "" {
		t.Error("空状态说明不该为空")
	}

	withATTimeout(t)
	mod := &Modem{ID: 0, Model: "FN990A40", cmdImpl: &common.UnknownCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	sent := fakeATServe(mod, func(cmd string) []string {
		if strings.HasPrefix(cmd, "AT#CAINFOEXT?") {
			return []string{
				"#CAINFOEXT: 2, LTE",
				"PCC- BandClass: 3 , CH: 1650 , PCI: 301 , RSRP: -95 , RSRQ: -11",
				"SCC0- BandClass: 1 , CH: 300 , PCI: 100 , RSRP: -101 , RSRQ: -13",
				"OK",
			}
		}
		return []string{"OK"}
	})
	ccs, err := mod.QueryCAInfo()
	if err != nil {
		t.Fatalf("QueryCAInfo 出错：%v", err)
	}
	if !hasCmd(drainSent(sent), "AT#CAINFOEXT?") {
		t.Error("QueryCA 应该发 AT#CAINFOEXT?")
	}
	if len(ccs) != 2 || !CCsAggregated(ccs) {
		t.Fatalf("分量载波 = %+v，want PCC+SCC（CCsAggregated=true）", ccs)
	}
	if ccs[0].Role != "PCC" || ccs[0].PCID != 301 || ccs[0].BandLabel != "B3" || ccs[0].EARFCN != 1650 {
		t.Errorf("主载波 = %+v", ccs[0])
	}
	if ccs[1].Role != "SCC" || ccs[1].PCID != 100 || ccs[1].BandLabel != "B1" {
		t.Errorf("辅载波 = %+v", ccs[1])
	}
	if cmds, note := mod.caHint(); len(cmds) != 1 || cmds[0] != "AT#CAINFOEXT?" || note == "" {
		t.Errorf("caHint = %v / %q（应报出 Telit 实际用的那条指令）", cmds, note)
	}

	// 没有辅载波时（ca_count=1）：列表里只有主载波，CCsAggregated=false
	mod2 := &Modem{ID: 0, Model: "FN990A40", cmdImpl: &common.UnknownCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	fakeATServe(mod2, func(cmd string) []string {
		if strings.HasPrefix(cmd, "AT#CAINFOEXT?") {
			return []string{"#CAINFOEXT: 1, LTE", "PCC- BandClass: 3 , CH: 1650 , PCI: 301", "OK"}
		}
		return []string{"OK"}
	})
	ccs2, err := mod2.QueryCAInfo()
	if err != nil || len(ccs2) != 1 || CCsAggregated(ccs2) {
		t.Errorf("无辅载波时 = %+v err=%v，want 只有 PCC 且未聚合", ccs2, err)
	}
}

// TestVendorServingCellsDispatch 统一入口按前缀分派（认不出返回 nil）
func TestVendorServingCellsDispatch(t *testing.T) {
	if got := cell.VendorServingCells([]string{"+QENG: \"servingcell\",\"NOCONN\""}); got != nil {
		t.Errorf("标准格式不该走这一层：%+v", got)
	}
	got := cell.VendorServingCells([]string{"+ZCELLINFO: 1234,cellid:12AB34,pci:301,band:3"})
	if len(got) != 1 || !got[0].Serving {
		t.Errorf("ZCELLINFO 分派失败：%+v", got)
	}
	got = cell.VendorServingCells([]string{"#CAINFOEXT: 2, LTE", "PCC- BandClass: 3 , PCI: 301 , RSRP: -95"})
	if len(got) != 1 || got[0].PCID != 301 {
		t.Errorf("CAINFOEXT 分派失败：%+v", got)
	}
}

// ====================== openluat Air724UG：AT+CCED ======================
//
// 样例行按 **vendor/openluat.sh 消费端的读取顺序**构造（cell_info() 的
// `IFS='|' read -r serving_rat mcc mnc imsi roaming band_raw bandwidth_raw dl_earfcn
//  cell_id rsrp_raw rsrq_raw tac srxlev pci`），逐个值都在注释里对上了脚本里的哪一格。

// TestParseOpenluatCCEDLTEServing LTE 服务小区（13 段）
func TestParseOpenluatCCEDLTEServing(t *testing.T) {
	// mcc,mnc,imsi,roaming,band,bandwidth,dl_earfcn,cell_id,rsrp_raw,rsrq_raw,tac,srxlev,pci
	line := "+CCED:LTE current cell:460,11,460115091169489,0,LTE B3,100,1850,0A11A009,44,18,1609,15,140"
	c, ok := openluat.CCED([]string{line, "OK"})
	if !ok {
		t.Fatal("LTE 服务小区没解析出来")
	}
	if c.Tech != CellTechLTE || !c.Serving {
		t.Errorf("tech/serving = %v/%v", c.Tech, c.Serving)
	}
	if c.MCC != "460" || c.MNC != "11" || c.Operator != "中国电信" {
		t.Errorf("PLMN/运营商 = %s/%s/%s", c.MCC, c.MNC, c.Operator)
	}
	if c.Band != 3 {
		t.Errorf("band = %d，want 3（`LTE B3` 去前缀取尾数）", c.Band)
	}
	if c.Bandwidth != 20 {
		t.Errorf("带宽 = %d MHz，want 20（PRB 100 → 20MHz）", c.Bandwidth)
	}
	if c.EARFCN != 1850 || c.TAC != 1609 || c.PCID != 140 || c.CellID != "0A11A009" {
		t.Errorf("频点/TAC/PCI/小区 = %d/%d/%d/%q", c.EARFCN, c.TAC, c.PCID, c.CellID)
	}
	// 信号换算：RSRP=44-141=-97；RSRQ=18*0.5-20=-11
	if c.RSRP != -97 || c.RSRQ != -11 {
		t.Errorf("信号 = %d/%d，want -97/-11", c.RSRP, c.RSRQ)
	}
	// srxlev 是索引、不是 dBm ⇒ RSSI 必须留空
	if c.RSSI != 0 {
		t.Errorf("RSSI 应为 0（srxlev 不是 dBm）：%d", c.RSSI)
	}
}

// TestParseOpenluatCCEDGSMServing GSM 服务小区（8 段）
func TestParseOpenluatCCEDGSMServing(t *testing.T) {
	line := "+CCED:GSM current cell info:460,11,16458,12AB,45,20,0,67"
	c, ok := openluat.CCED([]string{line})
	if !ok {
		t.Fatal("GSM 服务小区没解析出来")
	}
	if c.Tech != CellTechGSM || c.MCC != "460" || c.MNC != "11" {
		t.Errorf("tech/PLMN = %v/%s/%s", c.Tech, c.MCC, c.MNC)
	}
	if c.TAC != 16458 || c.CellID != "12AB" || c.EARFCN != 67 {
		t.Errorf("LAC/小区/ARFCN = %d/%q/%d", c.TAC, c.CellID, c.EARFCN)
	}
	// bsic / rxlev 不是 PCI / RSRP 口径 ⇒ 不填（bsic=45、rxlev=20 在这里被刻意忽略）
	if c.PCID != 0 || c.RSRP != 0 || c.RSSI != 0 {
		t.Errorf("GSM 不该填 PCID/RSRP：%+v", c)
	}
}

// TestParseOpenluatCCEDNeighbourLTE LTE 邻区（9 段，可多行）
func TestParseOpenluatCCEDNeighbourLTE(t *testing.T) {
	resp := []string{
		"+CCED:LTE neighbor cell:460,11,1650,12AB34,44,18,1609,15,301",
		"+CCED:LTE neighbor cell:460,11,2452,34CD56,30,20,1609,10,155",
		"OK",
	}
	cells := openluat.CCEDNeighbours(resp)
	if len(cells) != 2 {
		t.Fatalf("邻区数 = %d，want 2：%+v", len(cells), cells)
	}
	c := cells[0]
	if c.EARFCN != 1650 || c.CellID != "12AB34" || c.TAC != 1609 || c.PCID != 301 {
		t.Errorf("邻区字段 = %d/%q/%d/%d", c.EARFCN, c.CellID, c.TAC, c.PCID)
	}
	if c.RSRP != -97 || c.RSRQ != -11 {
		t.Errorf("邻区信号 = %d/%d，want -97/-11", c.RSRP, c.RSRQ)
	}
	if c.Band != 3 {
		t.Errorf("邻区频段 = %d，want 3（EARFCN 1650 反推）", c.Band)
	}
	if c.Serving {
		t.Error("邻区不该标成服务小区")
	}
	if cells[1].PCID != 155 || cells[1].CellID != "34CD56" {
		t.Errorf("第二行邻区 = %+v", cells[1])
	}
	// 服务小区解析器不能把邻区行当成服务小区
	if _, ok := openluat.CCED(resp); ok {
		t.Error("邻区行不该被 ParseOpenluatCCED 认领")
	}
}

// TestOpenluatCCEDRejectsWrongFieldCount 字段数不对就不认（QModem 的 `!= N` 口径）
func TestOpenluatCCEDRejectsWrongFieldCount(t *testing.T) {
	bad := []string{
		"+CCED:LTE current cell:460,11,1,2,3",                        // 5 段 ≠ 13
		"+CCED:LTE neighbor cell:460,11,1650,12AB34",                 // 4 段 ≠ 9
		"+CCED:LTE current cell:",                                    // 无数据
		"+CCED:NR current cell:460,11,1,2,3,4,5,6,7,8,9,10,11,12,13", // 未取证的标签
		"+CCED:LTE current cell:1,2,3,4,5,6,7,8,9,10,11,12,13",       // MCC 不像真值
		"+QENG: \"servingcell\",\"LTE\"",                             // 不是 CCED
	}
	for _, line := range bad {
		if c, ok := openluat.CCED([]string{line}); ok {
			t.Errorf("%q 不该被认领：%+v", line, c)
		}
	}
	if cells := openluat.CCEDNeighbours(bad); len(cells) != 0 {
		t.Errorf("坏行里不该出邻区：%+v", cells)
	}
}

// TestVendorNeighbourCellsDispatch 邻区入口按前缀分派（别的机型返回 nil）
func TestVendorNeighbourCellsDispatch(t *testing.T) {
	if got := cell.VendorNeighbourCells([]string{"+QENG: \"neighbourcell intra\",\"LTE\",1650,301,-95,-11"}); got != nil {
		t.Errorf("标准邻区格式不该走这层：%+v", got)
	}
	got := cell.VendorNeighbourCells([]string{"+CCED:LTE neighbor cell:460,11,1650,12AB34,44,18,1609,15,301"})
	if len(got) != 1 || got[0].PCID != 301 {
		t.Errorf("openluat 邻区分派失败：%+v", got)
	}
	// 服务小区行不该从邻区入口出去
	if got := cell.VendorNeighbourCells([]string{"+CCED:LTE current cell:460,11,460115091169489,0,LTE B3,100,1850,0A11A009,44,18,1609,15,140"}); len(got) != 0 {
		t.Errorf("服务小区行不该从邻区入口出来：%+v", got)
	}
}

// TestVendorServingCellsOpenluat +CCED 进服务小区统一入口
func TestVendorServingCellsOpenluat(t *testing.T) {
	got := cell.VendorServingCells([]string{"+CCED:LTE current cell:460,11,460115091169489,0,LTE B3,100,1850,0A11A009,44,18,1609,15,140"})
	if len(got) != 1 || !got[0].Serving || got[0].PCID != 140 {
		t.Errorf("+CCED 服务小区分派失败：%+v", got)
	}
}

func TestVendorServingSignalPerLine(t *testing.T) {
	if r, q, s, _, ok := cell.VendorServingSignal("lte_rsrp: -95"); !ok || r != -95 || q != 0 || s != 0 {
		t.Errorf("lte_rsrp 行 = %d/%d/%d ok=%v", r, q, s, ok)
	}
	if _, q, _, _, ok := cell.VendorServingSignal("rsrq: -11"); !ok || q != -11 {
		t.Errorf("rsrq 行 = %d ok=%v", q, ok)
	}
	if _, _, s, _, ok := cell.VendorServingSignal("nr_snr: 25"); !ok || s != 25 {
		t.Errorf("nr_snr 行 = %d ok=%v", s, ok)
	}
	if r, _, _, _, ok := cell.VendorServingSignal("LTE RSRP:      -95"); !ok || r != -95 {
		t.Errorf("Sierra RSRP 行 = %d ok=%v", r, ok)
	}
	// 非信号行不能被当成信号（否则会把 band/PCI 写进信号）
	for _, line := range []string{"lte_band: 3,20MHz", "pci: 301", "channel: 1650", "LTE band: B3", "OK", ""} {
		if r, q, s, _, ok := cell.VendorServingSignal(line); ok {
			t.Errorf("%q 被误判成信号：%d/%d/%d", line, r, q, s)
		}
	}
}
