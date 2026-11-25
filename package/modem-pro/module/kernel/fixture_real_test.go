package kernel

import (
	cellpkg "modemd/module/cell"
	"modemd/module/common"
	"modemd/module/lock"
	"testing"
)

// ====================== 真机 fixture 金标准测试 ======================
//
// 数据来源：QModem 仓库的真机采集 testcases（`testcases/<vendor>/<platform>/<model>-<md5>/`，
// `response_hex` 是模组原始输出）。
//
//	quectel / unisoc / rm500u-cnv  AT+QNWPREFCFG="lte_band" / "nr5g_band"
//	quectel / unisoc / rm500u-cnv  AT+QNWLOCK="common/5g"
//	quectel / unisoc / rm500u-cnv  AT+QENG="servingcell"
//
// 这些是**真机字节**，所以它们能戳穿"看着像对、其实字段错位"的解析 bug。
// 十六进制原文见下面各测试的注释。

// qnwLTEBandResp RM500U-CNV 的 AT+QNWPREFCFG="lte_band" 原始应答
//
//	response_hex: 41542b514e57505245464346473d226c74655f62616e64220d0d0a2b514e57505245464346473a20226c74655f62616e64222c313a333a353a383a33343a33383a33393a34303a34310d0a0d0a4f4b0d0a
//	= AT+QNWPREFCFG="lte_band"\r\r\n+QNWPREFCFG: "lte_band",1:3:5:8:34:38:39:40:41\r\n\r\nOK\r\n
var qnwLTEBandResp = []string{
	`AT+QNWPREFCFG="lte_band"`,
	`+QNWPREFCFG: "lte_band",1:3:5:8:34:38:39:40:41`,
	"",
	"OK",
}

// qnwNRBandResp RM500U-CNV 的 AT+QNWPREFCFG="nr5g_band" 原始应答
//
//	= +QNWPREFCFG: "nr5g_band",1:3:5:8:28:41:77:78:79
var qnwNRBandResp = []string{
	`AT+QNWPREFCFG="nr5g_band"`,
	`+QNWPREFCFG: "nr5g_band",1:3:5:8:28:41:77:78:79`,
	"",
	"OK",
}

// TestQNWPREFCFGRealFixture 真机 QNWPREFCFG 应答的解析（band 表是冒号分隔）
func TestQNWPREFCFGRealFixture(t *testing.T) {
	cases := []struct {
		resp []string
		key  string
		want []int
	}{
		{qnwLTEBandResp, "lte_band", []int{1, 3, 5, 8, 34, 38, 39, 40, 41}},
		{qnwNRBandResp, "nr5g_band", []int{1, 3, 5, 8, 28, 41, 77, 78, 79}},
	}
	for _, c := range cases {
		v, ok := parseQNWPREFCFG(c.resp, c.key)
		if !ok {
			t.Fatalf("%s: 真机应答没解析出来", c.key)
		}
		bands, ok := common.ParseBandList(v)
		if !ok {
			t.Fatalf("%s: band 表 %q 没解析出来", c.key, v)
		}
		if lock.JoinInts(bands, ",") != lock.JoinInts(c.want, ",") {
			t.Errorf("%s: bands = %v, want %v", c.key, bands, c.want)
		}
	}
}

// TestQNWLOCKRealFixture 真机 `AT+QNWLOCK="common/5g"` 只回键名、**不带任何数值**：
//
//	response_hex: 41542b514e574c4f434b3d22636f6d6d6f6e2f3567220d0d0a2b514e574c4f434b3a2022636f6d6d6f6e2f3567220d0a0d0a4f4b0d0a
//	= AT+QNWLOCK="common/5g"\r\r\n+QNWLOCK: "common/5g"\r\n\r\nOK\r\n
//
// 这种"没锁任何小区"的形态必须**不产生**锁定状态（既不能报错，也不能当成已锁）。
func TestQNWLOCKRealFixture(t *testing.T) {
	resp := []string{
		`AT+QNWLOCK="common/5g"`,
		`+QNWLOCK: "common/5g"`,
		"",
		"OK",
	}
	cl, ok := parseQNWLOCK(resp, "common/5g")
	if ok {
		t.Errorf("只有键名（无数值）不该当作有效锁定：%+v", cl)
	}
	if cl.Locked {
		t.Error("不该判成已锁定")
	}
	// 对照组：真锁定形态（Quectel 文档写法）必须解析出来
	locked := []string{`+QNWLOCK: "common/4g",1,2452,155`, "OK"}
	cl, ok = parseQNWLOCK(locked, "common/4g")
	if !ok || !cl.Locked || cl.ARFCN != 2452 || cl.PCID != 155 {
		t.Errorf("已锁定形态解析失败：%+v ok=%v", cl, ok)
	}
}

// TestQENGServingRealFixture RM500U-CNV 的 AT+QENG="servingcell" 原始应答（NR5G-SA）：
//
//	+QENG: "servingcell","CONNECT","NR5G-SA","TDD",460,11,760BF0114,702,76010F,633984,78,100,-89,-8,15,18,34,1
//
// 字段口径（Quectel 文档 + 真机）：
//
//	state, tech, duplex, MCC, MNC, cellID(hex), PCID, TAC(hex), ARFCN, band, DL带宽, RSRP, RSRQ, SINR, …
func TestQENGServingRealFixture(t *testing.T) {
	line := `+QENG: "servingcell","CONNECT","NR5G-SA","TDD",460,11,760BF0114,702,76010F,633984,78,100,-89,-8,15,18,34,1`
	cell, ok := cellpkg.ParseCellLine(line)
	if !ok {
		t.Fatal("真机 NR5G-SA 服务小区行没解析出来")
	}
	if cell.Tech != CellTechNR {
		t.Errorf("tech = %q, want NR", cell.Tech)
	}
	if !cell.Serving {
		t.Error("servingcell 行应标记 Serving")
	}
	if cell.State != "CONNECT" {
		t.Errorf("state = %q, want CONNECT", cell.State)
	}
	if cell.MCC != "460" || cell.MNC != "11" {
		t.Errorf("PLMN = %s-%s, want 460-11（MNC 两位）", cell.MCC, cell.MNC)
	}
	if cell.CellID != "760BF0114" {
		t.Errorf("cellID = %q, want 760BF0114", cell.CellID)
	}
	if cell.PCID != 702 {
		t.Errorf("PCID = %d, want 702", cell.PCID)
	}
	if cell.EARFCN != 633984 {
		t.Errorf("ARFCN = %d, want 633984", cell.EARFCN)
	}
	if cell.Band != 78 {
		t.Errorf("band = %d, want 78", cell.Band)
	}
	if cell.RSRP != -89 || cell.RSRQ != -8 || cell.SINR != 15 {
		t.Errorf("信号 = RSRP %d / RSRQ %d / SINR %d，want -89 / -8 / 15", cell.RSRP, cell.RSRQ, cell.SINR)
	}
	if cell.TAC != 0x76010F {
		// TAC 真机是**十六进制原样上报**（76010F）——ParseATInt 先试十进制再回退十六进制，
		// 所以这里应当是 0x76010F，而不是 0（早先担心会读成 0，实际是对的：固定住别再退化）
		t.Errorf("TAC = %d（0x%X），want 0x76010F = %d", cell.TAC, cell.TAC, 0x76010F)
	}
}

// TestQENGNeighbourRealFixture RM500U-CNV 的 AT+QENG="neighbourcell" 原始应答：
//
//	response_hex: ...2b514e473a20226e65696768626f757263656c6c222c224e52222c3633333938342c3236322c2d3130322c2d31372c2d362c2d2c33320d0a...4f4b0d0a
//	= +QENG: "neighbourcell","NR",633984,262,-102,-17,-6,-,32
//
// 注意两点（真机才有）：① 标签是**裸 `"neighbourcell"`**，没有 intra/inter 后缀；
// ② 中间会出现 `-` 占位（未上报）。同频与否由上层拿服务小区 ARFCN 回填。
func TestQENGNeighbourRealFixture(t *testing.T) {
	line := `+QENG: "neighbourcell","NR",633984,262,-102,-17,-6,-,32`
	cell, ok := cellpkg.ParseCellLine(line)
	if !ok {
		t.Fatal("真机邻区行没解析出来")
	}
	if cell.Serving {
		t.Error("邻区行不该标记 Serving")
	}
	if cell.Tech != CellTechNR {
		t.Errorf("tech = %q, want NR", cell.Tech)
	}
	if cell.EARFCN != 633984 {
		t.Errorf("ARFCN = %d, want 633984", cell.EARFCN)
	}
	if cell.PCID != 262 {
		t.Errorf("PCID = %d, want 262", cell.PCID)
	}
	if cell.RSRP != -102 || cell.RSRQ != -17 || cell.SINR != -6 {
		t.Errorf("信号 = RSRP %d / RSRQ %d / SINR %d，want -102 / -17 / -6", cell.RSRP, cell.RSRQ, cell.SINR)
	}
	// 同频标记：邻区 ARFCN 与服务小区相同 → 由 MarkIntraFrequency 补上
	serving := []cellpkg.CellInfo{{Serving: true, Tech: CellTechNR, EARFCN: 633984}}
	neigh := []cellpkg.CellInfo{cell}
	cellpkg.MarkIntraFrequency(serving, neigh)
	if !neigh[0].IntraCell {
		t.Error("与服务小区同频的邻区应被标成同频邻区")
	}
}

// TestQCAINFORealFixture RM500U-CNV 的 AT+QCAINFO 原始应答（PCC）：
//
//	response_hex: ...2b514341494e464f3a2022504343222c3633333938342c3130302c224e52204e3738222c312c3730322c2d39322c2d372c2d37322c31300d0a...
//	= +QCAINFO: "PCC",633984,100,"NR N78",1,702,-92,-7,-72,10
//
// 频段串与 PCID 之间**多一格**（这里的 `1`）—— 代码里按"信号锚点回推"取 PCID，
// 这条真机数据就是那个做法的回归保护。
func TestQCAINFORealFixture(t *testing.T) {
	resp := []string{"AT+QCAINFO", `+QCAINFO: "PCC",633984,100,"NR N78",1,702,-92,-7,-72,10`, "", "OK"}
	cc := cellpkg.ParseQCAINFO(resp)
	if len(cc) != 1 {
		t.Fatalf("CC 数 = %d, want 1（%+v）", len(cc), cc)
	}
	c := cc[0]
	if c.Role != "PCC" || c.EARFCN != 633984 || c.Band != 78 {
		t.Errorf("PCC = role %q / earfcn %d / band %d，want PCC / 633984 / 78", c.Role, c.EARFCN, c.Band)
	}
	if c.PCID != 702 {
		t.Errorf("PCID = %d, want 702（多一格时不能把那个 1 当 PCI）", c.PCID)
	}
	if c.RSRP != -92 || c.RSRQ != -7 {
		t.Errorf("信号 = RSRP %d / RSRQ %d，want -92 / -7", c.RSRP, c.RSRQ)
	}
}

// TestUnsupportedCmdRealFixture 真机对不支持指令的答复：`AT+QCFG="band"` → `+CME ERROR: 4`
//
//	response_hex: 41542b514346473d2262616e64220d0d0a2b434d45204552524f523a20340d0a
//
// 这条决定了"探测到不支持就不下发"的判定，必须认得出来。
func TestUnsupportedCmdRealFixture(t *testing.T) {
	resp := []string{`AT+QCFG="band"`, "+CME ERROR: 4"}
	if !common.RespHasError(resp) {
		t.Error("+CME ERROR 必须判成错误（否则会把不支持的写法当真继续用）")
	}
	if atError(resp) == "" {
		t.Error("atError 应给出该错误行")
	}
}
