package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"encoding/hex"
	"net"
	"strings"
	"testing"
	"time"

	"modemd/module/atparse"
	"modemd/module/cell"
	"modemd/module/common"
	"modemd/module/dial"
	"modemd/module/sms"
	smspkg "modemd/module/sms"
	"modemd/module/unisoc"
	"modemd/module/usbdialect"
)

// ---- from: ambr/ambr_test.go ----

// 实测应答（RM502Q-GL / 中国电信 5G SA）：
//
//	AT+QNWCFG="nr5g_ambr"
//	+QNWCFG: "nr5g_ambr","IMS",6,300,6,75
//	OK

func TestParseRealResponse(t *testing.T) {
	resp := []string{
		`+QNWCFG: "nr5g_ambr","IMS",6,300,6,75`,
		"OK",
	}
	r, ok := atparse.ParseAMBR(resp)
	if !ok {
		t.Fatal("实测应答应能解析")
	}
	if r.APN != "IMS" {
		t.Fatalf("APN = %q, want IMS", r.APN)
	}
	if r.DLMbps != 300 || r.ULMbps != 75 {
		t.Fatalf("签约速率 = DL %d / UL %d Mbps, want 300 / 75", r.DLMbps, r.ULMbps)
	}
	if r.CQIDL != 6 || r.CQIUL != 6 {
		t.Fatalf("CQI = DL %d / UL %d, want 6 / 6", r.CQIDL, r.CQIUL)
	}
}

// 有数据 APN 时优先取它，IMS 只是兜底
func TestParsePreferDataAPN(t *testing.T) {
	resp := []string{
		`+QNWCFG: "nr5g_ambr","IMS",6,300,6,75`,
		`+QNWCFG: "nr5g_ambr","3GNET",9,500,8,100`,
	}
	r, ok := atparse.ParseAMBR(resp)
	if !ok {
		t.Fatal("应能解析")
	}
	if r.APN != "3GNET" || r.DLMbps != 500 || r.ULMbps != 100 {
		t.Fatalf("应优先数据 APN，实际 %+v", r)
	}
}

// 全 0（未建立会话 / 未注册）视为没有数据
func TestParseAllZero(t *testing.T) {
	if _, ok := atparse.ParseAMBR([]string{`+QNWCFG: "nr5g_ambr","",0,0,0,0`, "OK"}); ok {
		t.Fatal("全 0 不应当作有效签约速率")
	}
}

// 字段数不够（LTE 的 lte_ambr 只有两个数字、含义未实测）：宁可不显示也不标反
func TestParseLTELikeShortResponse(t *testing.T) {
	if _, ok := atparse.ParseAMBR([]string{`+QNWCFG: "lte_ambr","",0,0`, "OK"}); ok {
		t.Fatal("两个数字的应答不应解析（含义未实测）")
	}
	if _, ok := atparse.ParseAMBR([]string{`+QNWCFG: "lte_ambr","3GNET",500,100`, "OK"}); ok {
		t.Fatal("两个数字的应答不应解析（含义未实测）")
	}
}

// 非数字 / 无关行都要稳（ERROR、空应答、别的 QNWCFG 行）
func TestParseGarbage(t *testing.T) {
	cases := [][]string{
		nil,
		{"ERROR"},
		{`+QNWCFG: "usbnet",0`},
		{`+QNWCFG: "nr5g_ambr"`},
		{`+QNWCFG: "nr5g_ambr","IMS",a,b,c,d`},
	}
	for _, c := range cases {
		if _, ok := atparse.ParseAMBR(c); ok {
			t.Fatalf("不应解析：%v", c)
		}
	}
}

// 模组带 "\r"、空格也要能解析（串口回显常见）
func TestParseTrimCR(t *testing.T) {
	resp := []string{`+QNWCFG: "nr5g_ambr", "IMS" , 6 , 300 , 6 , 75` + "\r"}
	r, ok := atparse.ParseAMBR(resp)
	if !ok {
		t.Fatal("带空格/\\r 的应答应能解析")
	}
	if r.DLMbps != 300 || r.ULMbps != 75 {
		t.Fatalf("解析结果 %+v", r)
	}
}

// 鼎桥 MT5700：AT^DSAMBR=<cid> 的应答单位是 kbps，要换算成 Mbps；末位是**该承载的 QCI**
func TestParseDSAMBR(t *testing.T) {
	r, ok := atparse.ParseAMBR([]string{"AT^DSAMBR=1", `^DSAMBR: 1,1000000,1000000,"ctnet",6`, "OK"})
	if !ok || r.APN != "ctnet" || r.DLMbps != 1000 || r.ULMbps != 1000 {
		t.Fatalf("数据上下文解析错误: %+v ok=%v", r, ok)
	}
	if r.QCI != 6 {
		t.Errorf("数据上下文 QCI = %d, want 6", r.QCI)
	}
	if r.CID != 1 {
		t.Errorf("数据上下文 CID = %d, want 1", r.CID)
	}
	r2, ok := atparse.ParseAMBR([]string{"AT^DSAMBR=5", `^DSAMBR: 5,300000,75000,"ims",5`, "OK"})
	if !ok || r2.APN != "ims" || r2.DLMbps != 300 || r2.ULMbps != 75 {
		t.Fatalf("IMS 上下文解析错误: %+v ok=%v", r2, ok)
	}
	if r2.QCI != 5 {
		t.Errorf("IMS 上下文 QCI = %d, want 5（IMS 信令的标准等级）", r2.QCI)
	}
	if got := atparse.KbpsToMbps(75000); got != 75 {
		t.Errorf("KbpsToMbps(75000) = %d, want 75", got)
	}
	if got := atparse.KbpsToMbps(1499); got != 1 {
		t.Errorf("KbpsToMbps(1499) = %d, want 1（四舍五入）", got)
	}
}

// 鼎桥 DSAMBR 末位 QCI：缺失/越界只丢 QCI，不影响签约速率
func TestParseDSAMBRQCI(t *testing.T) {
	// 手册的三字段写法（没给 APN 与 QCI）→ AMBR 照收，QCI 记未上报
	if r, ok := atparse.ParseDSAMBR(`^DSAMBR: 1,1000000,1000000`); !ok || r.QCI != 0 {
		t.Fatalf("三字段写法应解析出 AMBR 且 QCI=0：%+v ok=%v", r, ok)
	}
	// 有 APN、但末端位（旧固件/截断）→ QCI=0
	if r, ok := atparse.ParseDSAMBR(`^DSAMBR: 1,1000000,1000000,"ctnet"`); !ok || r.QCI != 0 {
		t.Fatalf("无末位时 QCI 应为 0：%+v ok=%v", r, ok)
	}
	// 末位越界 → 只丢 QCI
	if r, ok := atparse.ParseDSAMBR(`^DSAMBR: 1,1000000,1000000,"ctnet",999`); !ok || r.QCI != 0 {
		t.Fatalf("越界 QCI 应记 0：%+v ok=%v", r, ok)
	}
}

// ====================== 只取「拨号的那条 PDP 上下文」 ======================
//
// 同一台模组常同时上报好几条承载（IMS + 数据 APN），AMBR / CQI / QCI 都是**逐承载**的，
// 取错就会把 IMS 的档位当成拨号的档位显示出来。下面这批用例钉住“按拨号 cid/APN 选行”。

// QNWCFG 只带 APN（不带 cid）：知道拨的是哪条 APN 时必须选中它
func TestParseAMBRContextPicksDialedAPN(t *testing.T) {
	resp := []string{
		`+QNWCFG: "nr5g_ambr","IMS",6,300,6,75`,
		`+QNWCFG: "nr5g_ambr","ctnet",6,1000,6,1000`,
	}
	if r, ok := atparse.ParseAMBRContext(resp, 3, "ctnet"); !ok || r.APN != "ctnet" || r.DLMbps != 1000 {
		t.Fatalf("按拨号 APN 选取错误：%+v ok=%v", r, ok)
	}
	// cid 匹配不到（QNWCFG 不带 cid）→ 退回“非内部”那条，仍不是 IMS
	if r, ok := atparse.ParseAMBRContext(resp, 3, ""); !ok || r.APN != "ctnet" {
		t.Fatalf("cid 匹配不到时应退回非内部承载：%+v ok=%v", r, ok)
	}
	// 拨的就是 IMS（知道 APN）→ 这次该拿 IMS
	if r, ok := atparse.ParseAMBRContext(resp, 5, "ims"); !ok || r.APN != "IMS" {
		t.Fatalf("拨号 APN=ims 时应取 IMS 那条：%+v ok=%v", r, ok)
	}
	// 两维都不给 = 旧口径（优先非 IMS）
	if r, ok := atparse.ParseAMBR(resp); !ok || r.APN != "ctnet" {
		t.Fatalf("旧口径应取非 IMS：%+v ok=%v", r, ok)
	}
}

// DSAMBR 行里带 cid：按拨号 cid 精确选中（哪怕数据行在前面/后面）
func TestParseAMBRContextPicksDialedCID(t *testing.T) {
	resp := []string{
		`^DSAMBR: 1,1000000,1000000,"ctnet",6`,
		`^DSAMBR: 5,300000,75000,"ims",5`,
	}
	r, ok := atparse.ParseAMBRContext(resp, 5, "")
	if !ok || r.CID != 5 || r.APN != "ims" || r.DLMbps != 300 || r.QCI != 5 {
		t.Fatalf("按拨号 cid=5 选取错误：%+v ok=%v", r, ok)
	}
	r, ok = atparse.ParseAMBRContext(resp, 1, "")
	if !ok || r.CID != 1 || r.APN != "ctnet" || r.QCI != 6 {
		t.Fatalf("按拨号 cid=1 选取错误：%+v ok=%v", r, ok)
	}
	// 拨号 cid 不在应答里 → 退回非内部承载（ctnet）
	if r, ok := atparse.ParseAMBRContext(resp, 9, ""); !ok || r.APN != "ctnet" {
		t.Fatalf("cid 不在应答里应退回非内部承载：%+v ok=%v", r, ok)
	}
}

// 有方 C5GQOSRDP：不带 cid 查询时每条上下文各回一行，只取拨号那条的 5QI(=QCI)
func TestParseC5GQOSRDPContext(t *testing.T) {
	resp := []string{
		"+C5GQOSRDP: 5,5,100,50,200,100,300000,75000",   // IMS（先出现）
		"+C5GQOSRDP: 1,9,100,50,200,100,1024000,512000", // 数据（拨号的那条）
	}
	r, ok := unisoc.ParseC5GQOSRDPContext(resp, 1)
	if !ok || r.CID != 1 || r.QCI != 9 || r.DLMbps != 1024 {
		t.Fatalf("按拨号 cid=1 选取错误：%+v ok=%v", r, ok)
	}
	r, ok = unisoc.ParseC5GQOSRDPContext(resp, 5)
	if !ok || r.CID != 5 || r.QCI != 5 || r.DLMbps != 300 {
		t.Fatalf("按拨号 cid=5 选取错误：%+v ok=%v", r, ok)
	}
	// 旧口径（不知道拨号上下文）仍取第一条有效行
	if r, ok := unisoc.ParseC5GQOSRDP(resp); !ok || r.CID != 5 {
		t.Fatalf("旧口径应取第一条：%+v ok=%v", r, ok)
	}
}

// AT+CGCONTRDP 尾部 AMBR 扩展：IMS 与数据两条都带尾巴时，只取拨号那条
func TestParseCGCONTRDPContext(t *testing.T) {
	resp := []string{
		`+CGCONTRDP: 5,5,"ims","",1,0,0,0,0,0,0,300000,75000`,
		`+CGCONTRDP: 1,5,"ctnet","",1,0,0,0,0,0,0,5000000,1000000`,
	}
	r, ok := ParseCGCONTRDPContext(resp, 1, "")
	if !ok || r.CID != 1 || r.APN != "ctnet" || r.DLMbps != 5000 {
		t.Fatalf("按拨号 cid=1 选取错误：%+v ok=%v", r, ok)
	}
	r, ok = ParseCGCONTRDPContext(resp, 3, "ims")
	if !ok || r.APN != "ims" || r.CID != 5 || r.DLMbps != 300 {
		t.Fatalf("按拨号 APN 选取错误：%+v ok=%v", r, ok)
	}
	// 旧口径：优先非内部（ctnet）
	if r, ok := ParseCGCONTRDP(resp); !ok || r.APN != "ctnet" {
		t.Fatalf("旧口径应取数据 APN：%+v ok=%v", r, ok)
	}
}

// dialedPDP：拨号状态 → preferredDialCID → 缓存里已激活的非内部上下文 → DefaultDataCID
func TestDialedPDP(t *testing.T) {
	mod := &Modem{ID: 4242}
	mod.dialUpdate(func(s *dial.DialState) { s.CID, s.APN = 5, "ctnet" })
	if cid, apn := mod.dialedPDP(); cid != 5 || apn != "ctnet" {
		t.Errorf("dialedPDP() = (%d,%q)，want (5,ctnet)", cid, apn)
	}
	// 未拨号：退回默认数据 cid
	idle := &Modem{ID: 4243}
	if cid, apn := idle.dialedPDP(); cid != DefaultDataCID || apn != "" {
		t.Errorf("未拨号时 dialedPDP() = (%d,%q)，want (%d,\"\")", cid, apn, DefaultDataCID)
	}
}

// ⚠️ 真机回归（2026-10-06，用户：「IMS 不能算，就是软件拨号的那一条」）：
// 厂商托管数据面（展锐 SIPC / F50）上 modemd 自己从没拨过号 ⇒ DialStatus 里没有 cid，
// 这时必须用**只读缓存**的 PDP 上下文挑“已激活且非内部”的那条，
// 而不是盲目退回 DefaultDataCID(1) —— 碰上 “cid 1 其实是 IMS”的固件就会显示 IMS 的档位。
func TestDialedPDPFromCachedContexts(t *testing.T) {
	mod := &Modem{ID: 4242}
	mod.mu.Lock()
	mod.paramsStatus = &Status{ModemID: mod.ID, APN: "ctnet", QueriedAt: time.Now(), Contexts: []Context{
		{CID: 11, PDPType: "IPV4V6", APN: "IMS", Active: true, Internal: true},
		{CID: 1, PDPType: "IPV4V6", APN: "", Active: true},
	}}
	mod.mu.Unlock()

	cid, apn := mod.dialedPDP()
	if cid != 1 {
		t.Fatalf("必须挑已激活的非内部上下文（cid 1 = 数据），实际 cid=%d", cid)
	}
	if apn != "ctnet" {
		t.Fatalf("配置 APN 为空时应从“当前在用 APN”补上（界面要显示 ctnet），实际 %q", apn)
	}

	// 只有 IMS（内部承载）激活 ⇒ 永远不能选中它，退回默认数据 cid
	mod.mu.Lock()
	mod.paramsStatus = &Status{ModemID: mod.ID, Contexts: []Context{
		{CID: 11, APN: "IMS", Active: true, Internal: true},
	}}
	mod.mu.Unlock()
	if cid, _ := mod.dialedPDP(); cid != DefaultDataCID {
		t.Errorf("只有 IMS 激活时应退回 DefaultDataCID，实际 %d", cid)
	}
}

// 即使模组把 IMS 那条排在前面，也只取拨号那条的 AMBR/5QI（用户：「IMS 不能算」）
func TestUnisocAMBRNeverPicksIMSFirstLine(t *testing.T) {
	mod := &Modem{ID: 4243, Model: "F50", cmdImpl: &unisoc.UnisocCommand{}}
	mod.dialUpdate(func(s *dial.DialState) { s.CID, s.APN = 1, "ctnet" })
	resp := []string{
		"+C5GQOSRDP:11,5,0,0,0,0,300000,75000,0",   // IMS 排第一行
		"+C5GQOSRDP:1,9,0,0,0,0,1660000,2000000,0", // 数据（拨号的那条）
		"OK",
	}
	r, ok := parseAMBRForModem(mod, "AT+C5GQOSRDP", resp)
	if !ok || r == nil {
		t.Fatal("应能解析出 AMBR")
	}
	if r.CID != 1 || r.QCI != 9 || r.DLMbps != 1660 || r.ULMbps != 2000 {
		t.Fatalf("IMS 排第一行时也必须取 cid 1（数据）：%+v", r)
	}
}

// C5GQOSRDP 应答没有 APN 字段 ⇒ QueryAMBR 用拨号那条的 APN 补上
// （界面显示「APN ctnet（cid 1）」，一眼看出取的不是 IMS）
func TestQueryAMBRFillsAPNForC5GQOSRDP(t *testing.T) {
	withATTimeout(t)
	mod := &Modem{ID: 4244, ATPort: "/dev/stty_nr2", Model: "F50", cmdImpl: &unisoc.UnisocCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	defer close(mod.writeChan)
	fakeATServe(mod, func(cmd string) []string {
		if strings.HasPrefix(cmd, "AT+C5GQOSRDP") {
			return []string{"+C5GQOSRDP:1,9,0,0,0,0,1660000,2000000,0",
				"+C5GQOSRDP:11,5,0,0,0,0,300000,75000,0", "OK"}
		}
		return []string{"ERROR"}
	})
	mod.dialUpdate(func(s *dial.DialState) { s.CID, s.APN = 1, "ctnet" })

	r := mod.QueryAMBR()
	if r == nil {
		t.Fatal("QueryAMBR 应返回 AMBR")
	}
	if r.APN != "ctnet" || r.CID != 1 || r.QCI != 9 || r.DLMbps != 1660 || r.ULMbps != 2000 {
		t.Fatalf("AMBR（含补 APN）不对：%+v", r)
	}
	if r.Source != "AT+C5GQOSRDP" {
		t.Errorf("来源应记实际指令，实际 %q", r.Source)
	}
}

// ====================== 备用来源：AT+CGCONTRDP 尾部 AMBR 扩展 ======================

// 用户提供的文档样例：末尾两个 = <下行 kbps>,<上行 kbps>
func TestParseCGCONTRDP(t *testing.T) {
	r, ok := ParseCGCONTRDP([]string{`+CGCONTRDP: 1,5,"cmiot","",1,0,0,0,0,0,0,5000000,1000000`, "OK"})
	if !ok {
		t.Fatal("带尾部 kbps 字段的应答应能解析")
	}
	if r.APN != "cmiot" || r.DLMbps != 5000 || r.ULMbps != 1000 {
		t.Fatalf("解析结果 %+v，want cmiot 5000/1000", r)
	}
}

// 本机 FM160-EAU 实测应答（引自 "9 格"）：行尾是引号包着的 P-CSCF 地址，**没有** AMBR → 不解析
func TestParseCGCONTRDPFM160NoTail(t *testing.T) {
	lines := []string{
		`+CGCONTRDP: 1,0,"ctnet","10.40.27.173.255.255.255.252","10.40.27.174","218.2.2.2","218.4.4.4","0.0.0.0","0.0.0.0"`,
		`+CGCONTRDP: 1,0,"ctnet","36.14.4.0.6.32.16.24.24.217.208.204.83.162.193.98.255.255.255.255.255.255.255.255.0.0.0.0.0.0.0.0","254.128.0.0.0.0.0.0.0.0.0.0.0.0.1","36.14.0.90.0.0.0.0.0.0.0.0.0.0.102.102","36.14.0.91.0.0.0.0.0.0.0.0.0.0.102.102","0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0","0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0"`,
		`+CGCONTRDP: 2,0,"IMS","36.14.5.121.6.104.10.102.24.217.208.173.156.151.222.160.255.255.255.255.255.255.255.255.0.0.0.0.0.0.0.0","254.128.0.0.0.0.0.0.0.0.0.0.0.0.1","36.14.0.90.0.0.0.0.0.0.0.0.0.0.102.102","36.14.0.91.0.0.0.0.0.0.0.0.0.0.102.102","36.14.0.46.130.1.192.0.0.7.0.0.0.0.0.1","36.14.0.46.130.1.192.0.0.99.0.0.0.0.0.1"`,
		"OK",
	}
	if r, ok := ParseCGCONTRDP(lines); ok {
		t.Fatalf("无尾部扩展的固件不应给出 AMBR，实际 %+v", r)
	}
}

// 尾部字段不像 AMBR（引号地址 / 小整数 / 只有一个大数字）→ 一律不报，宁可不显示也不误报
func TestParseCGCONTRDPRejectsNoise(t *testing.T) {
	cases := [][]string{
		nil,
		{"ERROR"},
		{"OK"},
		{`+CGCONTRDP: 1,0,"ctnet","10.0.0.1","10.0.0.2","1.1.1.1","8.8.8.8"`},         // 字段太少
		{`+CGCONTRDP: 1,0,"ctnet","10.0.0.1","10.0.0.2","1.1.1.1","8.8.8.8",0,1`},     // IM_CN 标志 + NSLPI（小整数）
		{`+CGCONTRDP: 1,0,"ctnet","10.0.0.1","10.0.0.2","1.1.1.1","8.8.8.8",1500,1`},  // IPv4_MTU=1500 + WLAN_Offload=1
		{`+CGCONTRDP: 1,0,"ctnet","10.0.0.1","10.0.0.2","1.1.1.1","8.8.8.8",999,999`}, // 不到 1 Mbps
		{`+CGCONTRDP: 1,0,"ctnet"`}, // 只有 apn
	}
	for _, c := range cases {
		if r, ok := ParseCGCONTRDP(c); ok {
			t.Fatalf("不应解析：%v → %+v", c, r)
		}
	}
}

// 只有内部 APN（IMS）带 AMBR 时也要能给出（拿不到数据 APN 的那份就用它，与移远路径一致）
func TestParseCGCONTRDPInternalFallback(t *testing.T) {
	resp := []string{
		`+CGCONTRDP: 1,0,"ctnet","10.0.0.1","10.0.0.2","1.1.1.1","8.8.8.8","0.0.0.0","0.0.0.0"`,
		`+CGCONTRDP: 2,0,"IMS","10.0.0.3","10.0.0.4","1.1.1.1","8.8.8.8","0.0.0.0","0.0.0.0",300000,75000`,
	}
	r, ok := ParseCGCONTRDP(resp)
	if !ok {
		t.Fatal("IMS 行带 AMBR 时应能回退给出")
	}
	if r.APN != "IMS" || r.DLMbps != 300 || r.ULMbps != 75 {
		t.Fatalf("解析结果 %+v，want IMS 300/75", r)
	}
}

// 数据 APN 那行优先（两行都带 AMBR 时不能拿 IMS 的）
func TestParseCGCONTRDPPreferDataAPN(t *testing.T) {
	resp := []string{
		`+CGCONTRDP: 2,0,"IMS","10.0.0.3","10.0.0.4","1.1.1.1","8.8.8.8","0.0.0.0","0.0.0.0",300000,75000`,
		`+CGCONTRDP: 1,0,"3gnet","10.0.0.1","10.0.0.2","1.1.1.1","8.8.8.8","0.0.0.0","0.0.0.0",1000000,100000`,
	}
	r, ok := ParseCGCONTRDP(resp)
	if !ok {
		t.Fatal("应能解析")
	}
	if r.APN != "3gnet" || r.DLMbps != 1000 || r.ULMbps != 100 {
		t.Fatalf("应优先数据 APN，实际 %+v", r)
	}
}

// 带 \r 与前后空格也要能解析
func TestParseCGCONTRDPTrimCR(t *testing.T) {
	r, ok := ParseCGCONTRDP([]string{
		`+CGCONTRDP: 1 , 5 , "cmiot" , "" , 1 , 0 , 0 , 0 , 0 , 0 , 0 , 5000000 , 1000000` + "\r"})
	if !ok || r.DLMbps != 5000 || r.ULMbps != 1000 {
		t.Fatalf("解析结果 %+v ok=%v", r, ok)
	}
}

// ---- from: apn/apn_test.go ----

// 实测应答（移远 RM502Q-GL，电信卡）

func TestParseRespCGCONTRDP(t *testing.T) {
	resp := []string{
		`+CGCONTRDP: 1,0,3GNET,10.149.102.75,36.14.4.0.6.24.12.75.24.217.129.210.10.3.26.30, 254.128.0.0.0.0.0.0.0.0.0.0.0.0.0.1,218.2.2.2 36.14.0.90.0.0.0.0.0.0.0.0.0.0.102.102,218.4.4.4`,
		`+CGCONTRDP: 2,0,IMS,36.14.5.120.6.32.0.214.24.217.129.211.40.108.195.174, 254.128.0.0.0.0.0.0.0.0.0.0.0.0.0.1`,
		"OK",
	}
	name, ip := ParseResp("AT+CGCONTRDP", resp)
	if name != "3GNET" {
		t.Fatalf("apn = %q, want 3GNET（IMS 是内部专用 APN，要跳过）", name)
	}
	if ip != "10.149.102.75" {
		t.Fatalf("ip = %q, want 10.149.102.75", ip)
	}
}

// 只有 IMS 上下文时视为未上报
func TestParseRespCGCONTRDPIMSonl(t *testing.T) {
	name, ip := ParseResp("AT+CGCONTRDP", []string{`+CGCONTRDP: 2,0,IMS,10.0.0.1,1.2.3.4`})
	if name != "" || ip != "" {
		t.Fatalf("只有 IMS 上下文时不应给出数据 APN：%q/%q", name, ip)
	}
}

// 实测应答：配置了 3GNET / IMS / ctwap / sos，应取 cid 最小的数据 APN
func TestParseRespCGDCONT(t *testing.T) {
	resp := []string{
		`+CGDCONT: 1,"IPV4V6","3GNET","0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0",0,0,0,0,,,,,,,,,"",,,,0`,
		`+CGDCONT: 2,"IPV4V6","IMS","0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0",0,0,0,0,,,,,,,,,"",,,,0`,
		`+CGDCONT: 3,"IPV4V6","ctwap","0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0",0,0,0,0,,,,,,,,,"",,,,0`,
		`+CGDCONT: 4,"IPV4V6","sos","0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0",0,0,0,1,,,,,,,,,"",,,,0`,
		"OK",
	}
	name, ip := ParseResp("AT+CGDCONT?", resp)
	if name != "3GNET" {
		t.Fatalf("apn = %q, want 3GNET", name)
	}
	if ip != "" {
		t.Fatalf("AT+CGDCONT? 不提供本机 IP，实际 %q", ip)
	}
}

// 只剩内部 APN 时退回第一个非空值（至少给出配置）
func TestParseRespCGDCONTOnlyInternal(t *testing.T) {
	resp := []string{
		`+CGDCONT: 2,"IPV4V6","IMS","0.0.0.0",0,0,0,0`,
		`+CGDCONT: 4,"IPV4V6","sos","0.0.0.0",0,0,0,0`,
	}
	name, _ := ParseResp("AT+CGDCONT?", resp)
	if name == "" {
		t.Fatal("全部为内部 APN 时也应回退给出一个配置值")
	}
}

func TestIsInternal(t *testing.T) {
	for _, s := range []string{"ims", "IMS", " sos ", "emergency"} {
		if !atparse.IsInternal(s) {
			t.Errorf("%q 应判为内部 APN", s)
		}
	}
	for _, s := range []string{"3gnet", "ctnet", "cmnet", "internet"} {
		if atparse.IsInternal(s) {
			t.Errorf("%q 不应判为内部 APN", s)
		}
	}
}

// 广和通 FM350-GL 实机应答：注意 `+CGCONTRDP: 2,,"ctnet",…` 的 <bearer_id> 是**空**的，
// 旧的正则要求它是数字 → 整行认不出来 → 界面「链路」的 APN 一直是 `-`。
const fm350CGCONTRDP = `AT+CGCONTRDP
+CGCONTRDP: 1,,"IMS","","","36.14.0.90.0.0.0.0.0.0.0.0.0.0.102.102","36.14.0.91.0.0.0.0.0.0.0.0.0.0.102.102","36.14.0.46.130.1.192.0.0.4.0.0.0.0.0.1","36.14.0.46.130.1.192.0.0.22.0.0.0.0.0.1",1,,0,,,,,,,1,1,0,"1,0",0,65535
+CGCONTRDP: 2,,"ctnet","","","218.2.2.2","218.4.4.4","","",0,,0,,,,,,,2,1,0,"1,0",0,65535
OK`

func TestParseRespFM350NoBearerID(t *testing.T) {
	resp := strings.Split(fm350CGCONTRDP, "\n")
	name, ip := ParseResp("AT+CGCONTRDP", resp)
	if name != "ctnet" {
		t.Errorf("APN = %q, want ctnet（IMS 是内部 APN，要跳过）", name)
	}
	if ip != "" {
		t.Errorf("该固件 CGCONTRDP 的本机地址是空的（IP 走 AT+CGPADD=2），got %q", ip)
	}
	// 有地址的写法仍要能取到（回归）
	name2, ip2 := ParseResp("AT+CGCONTRDP", []string{`+CGCONTRDP: 1,5,"3gnet","10.1.2.3","10.1.2.1"`})
	if name2 != "3gnet" || ip2 != "10.1.2.3" {
		t.Errorf("= %q/%q, want 3gnet/10.1.2.3", name2, ip2)
	}
}

// ---- from: device/device_test.go ----

// 实测应答（RM502Q-GL）：AT+CFUN? → +CFUN: 1 / +CFUN: 4

func TestParseCFUN(t *testing.T) {
	cases := []struct {
		name string
		resp []string
		want int
		ok   bool
	}{
		{"全功能", []string{"+CFUN: 1", "OK"}, 1, true},
		{"飞行模式", []string{"+CFUN: 4", "OK"}, 4, true},
		{"最小功能", []string{"+CFUN: 0", "OK"}, 0, true},
		{"带复位标志", []string{"+CFUN: 4,1", "OK"}, 4, true},
		{"带 \\r", []string{"+CFUN: 1\r", "OK"}, 1, true},
		{"空应答", nil, 0, false},
		{"ERROR", []string{"ERROR"}, 0, false},
		{"非数字", []string{"+CFUN: x"}, 0, false},
		{"只有键名", []string{"+CFUN:"}, 0, false},
		{"无关指令", []string{`+QNWPREFCFG: "mode_pref",AUTO`, "OK"}, 0, false},
	}
	for _, c := range cases {
		got, ok := ParseCFUN(c.resp)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%s: ParseCFUN(%v) = %d,%v；want %d,%v", c.name, c.resp, got, ok, c.want, c.ok)
		}
	}
}

// 常量取值要和 3GPP / 实测一致：飞行模式用 4，恢复用 1（1,1 会重启模组，不能用）
func TestCFUNConstants(t *testing.T) {
	if CFUNFull != 1 || CFUNAirplane != 4 || CFUNMinimum != 0 {
		t.Fatalf("CFUN 常量不对：full=%d airplane=%d minimum=%d", CFUNFull, CFUNAirplane, CFUNMinimum)
	}
	if got := AirplaneCommand(true); got != "AT+CFUN=4" {
		t.Errorf("开飞行模式 = %q, want AT+CFUN=4", got)
	}
	if got := AirplaneCommand(false); got != "AT+CFUN=1" {
		t.Errorf("关飞行模式 = %q, want AT+CFUN=1（不能用 1,1：那是复位）", got)
	}
}

// TestParseUSBnet 实测应答：+QCFG: "usbnet",0
func TestParseUSBnet(t *testing.T) {
	if v, ok := usbdialect.ParseUSBnet([]string{`+QCFG: "usbnet",0`, "OK"}); !ok || v != "0" {
		t.Fatalf("解析结果 = %q/%v, want 0/true", v, ok)
	}
	if v, ok := usbdialect.ParseUSBnet([]string{`+QCFG: "usbnet",2`, "OK"}); !ok || v != "2" {
		t.Fatalf("解析结果 = %q/%v, want 2/true", v, ok)
	}
	// 带空格
	if v, ok := usbdialect.ParseUSBnet([]string{`+QCFG: "usbnet", 5`}); !ok || v != "5" {
		t.Fatalf("解析结果 = %q/%v", v, ok)
	}
	for _, resp := range [][]string{
		nil,
		{"OK"},
		{"ERROR"},
		{`+QCFG: "usbnet"`},           // 没有取值
		{`+QCFG: "data_interface",1`}, // 别的 key
	} {
		if v, ok := usbdialect.ParseUSBnet(resp); ok {
			t.Errorf("不应解析成功(%q): %v", v, resp)
		}
	}
}

// TestUSBnetModeTable 取值表与展示名
func TestUSBnetModeTable(t *testing.T) {
	want := map[string]string{
		"0": "QMI/NDIS",
		"1": "ECM",
		"2": "MBIM",
		"3": "RNDIS",
		"5": "NCM",
	}
	modes := usbdialect.Modes()
	if len(modes) != len(want) {
		t.Fatalf("取值表条数 = %d, want %d", len(modes), len(want))
	}
	for _, m := range modes {
		if want[m.Value] != m.Name {
			t.Errorf("取值 %s → %q, want %q", m.Value, m.Name, want[m.Value])
		}
		if m.Note == "" {
			t.Errorf("取值 %s 缺少说明", m.Value)
		}
		if ModeName(m.Value) != m.Name {
			t.Errorf("ModeName(%s) = %q", m.Value, ModeName(m.Value))
		}
	}
	if ModeName("9") != "" {
		t.Errorf("表外取值应返回空名，实际 %q", ModeName("9"))
	}
}

func TestIsDecimal(t *testing.T) {
	for _, s := range []string{"0", "12", "999"} {
		if !common.IsDecimal(s) {
			t.Errorf("%q 应为纯数字", s)
		}
	}
	for _, s := range []string{"", "1a", "-1", " 2"} {
		if common.IsDecimal(s) {
			t.Errorf("%q 不应判为纯数字", s)
		}
	}
}

func TestFirstLine(t *testing.T) {
	if got := FirstLine(nil); got != "(empty)" {
		t.Errorf("空应答 = %q, want (empty)", got)
	}
	if got := FirstLine([]string{"ERROR", "OK"}); got != "ERROR" {
		t.Errorf("首行 = %q, want ERROR", got)
	}
}

// ---- from: modstat/modstat_test.go ----

func TestParseICCIDAndIMSI(t *testing.T) {
	if got := atparse.ParseICCID([]string{"+CCID: 89860324040255993152", "OK"}); got != "89860324040255993152" {
		t.Errorf("ICCID = %q", got)
	}
	if got := atparse.ParseICCID([]string{`+CCID: "89860324040255993152"`, "OK"}); got != "89860324040255993152" {
		t.Errorf("带引号的 ICCID = %q", got)
	}
	if got := atparse.ParseICCID([]string{"ERROR"}); got != "" {
		t.Errorf("ERROR 时 ICCID 应为空，得到 %q", got)
	}

	// AT+CIMI 直接回一行裸数字
	if got := atparse.ParseIMSI([]string{"460115089161379", "OK"}); got != "460115089161379" {
		t.Errorf("IMSI = %q", got)
	}
	if got := atparse.ParseIMSI([]string{"+CIMI: 460115089161379"}); got != "460115089161379" {
		t.Errorf("带前缀的 IMSI = %q", got)
	}
	if got := atparse.ParseIMSI([]string{"OK"}); got != "" {
		t.Errorf("无 IMSI 时应为空，得到 %q", got)
	}

	if got := atparse.ParseCPIN([]string{"+CPIN: READY", "OK"}); got != "READY" {
		t.Errorf("CPIN = %q", got)
	}
	// AT+CNUM 实测号码字段为空（很多卡不写本机号码）
	if got := atparse.ParseCNUM([]string{`+CNUM: ,"",255`, "OK"}); got != "" {
		t.Errorf("空号码应为空串，得到 %q", got)
	}
	if got := atparse.ParseCNUM([]string{`+CNUM: "号码","+8613800138000",129`}); got != "+8613800138000" {
		t.Errorf("CNUM = %q", got)
	}
}

// 电话本条目：`+CPBR: 1,"+8613800138000",129,""`（读 "ON" 存储区拿本机号码）
func TestModstat_ParseCPBRNumber(t *testing.T) {
	resp := []string{`+CPBR: 1,"+8613800138000",129,""`, "OK"}
	if got := atparse.ParseCPBRNumber(resp); got != "+8613800138000" {
		t.Fatalf("ParseCPBRNumber = %q", got)
	}
	// 空存储区：模组直接回 ERROR，不应崩也不应给出假号码
	if got := atparse.ParseCPBRNumber([]string{"ERROR"}); got != "" {
		t.Fatalf("ERROR 时不应有号码，得到 %q", got)
	}
}

func TestModstat_ParseCPBSStorage(t *testing.T) {
	if got := atparse.ParseCPBSStorage([]string{`+CPBS: "SM",0,500`, "OK"}); got != "SM" {
		t.Fatalf("ParseCPBSStorage = %q", got)
	}
	if got := atparse.ParseCPBSStorage([]string{`+CPBS: "ON",0,2`, "OK"}); got != "ON" {
		t.Fatalf("ParseCPBSStorage = %q", got)
	}
	if got := atparse.ParseCPBSStorage([]string{"ERROR"}); got != "" {
		t.Fatalf("无 +CPBS 行时应为空，得到 %q", got)
	}
}

// BCD 半字节交换解码：用**真机读到的 EF_ICCID** 做校验
func TestModstat_BCDDigits(t *testing.T) {
	// 真机：AT+CRSM=176,12258,0,0,10 → "98683042402055991325"（EF_ICCID）
	raw, err := hex.DecodeString("98683042402055991325")
	if err != nil {
		t.Fatal(err)
	}
	if got := atparse.BCDDigits(raw); got != "89860324040255993152" {
		t.Fatalf("BCDDigits = %q, want ICCID 89860324040255993152（与 AT+CCID 必须一致）", got)
	}
	// 0xF 填充在高半字节（低半字节才是最后一位数字）：0x31 0xF0 → "13" + "0"
	if got := atparse.BCDDigits([]byte{0x31, 0xF0}); got != "130" {
		t.Fatalf("BCDDigits = %q, want 130（F 是高半字节填充）", got)
	}
	if got := atparse.BCDDigits([]byte{0xF1}); got != "1" {
		t.Fatalf("BCDDigits = %q, want 1（高位 F 直接停）", got)
	}
	// 非数字半字节（例如后面的 CCI/Ext）也要停住，不能把尾巴当号码
	if got := atparse.BCDDigits([]byte{0x21, 0x43, 0xAF}); got != "1234" {
		t.Fatalf("BCDDigits 应在非数字处停住，得到 %q", got)
	}
}

// EF_MSISDN（0x6F40）内容解析
func TestModstat_DecodeEFMSISDN(t *testing.T) {
	cases := []struct {
		name string
		hex  string
		want string
	}{
		{
			// <alpha 长度 0><号码长度 9><TON 91 国际><NPI 01><BCD +8613800138000>
			name: "国际号码（TON 0x91 → 补 +）",
			hex:  "00099101683108108300F0",
			want: "+8613800138000",
		},
		{
			// 带 2 字节 alpha 标识（长度 2）
			name: "带 alpha 标识",
			hex:  "02AABB099101683108108300F0",
			want: "+8613800138000",
		},
		{
			// TON 0x81（未知类型）→ 不补 +，按原样给数字
			name: "TON 未知类型",
			hex:  "00098101683108108300F0",
			want: "8613800138000",
		},
		// 号码长度字段声称超出实际内容 → 夹到末尾而不是越界（能解多少给多少）
		{name: "长度越界", hex: "0009FF01683108", want: "861380"},
		{name: "全 0（空文件）", hex: "000000", want: ""},
		{name: "太短", hex: "00", want: ""},
		{name: "非十六进制", hex: "ZZZZ", want: ""},
		{name: "位数不足", hex: "0009910100F0", want: ""},
	}
	for _, c := range cases {
		if got := atparse.DecodeEFMSISDN(c.hex); got != c.want {
			t.Errorf("%s: DecodeEFMSISDN(%q) = %q, want %q", c.name, c.hex, got, c.want)
		}
	}
}

// CRSM 应答解析与状态字判定
func TestModstat_ParseCRSM(t *testing.T) {
	// 成功：SW=9000 带数据
	sw1, sw2, data, ok := atparse.ParseCRSM([]string{`+CRSM: 144,0,"98683042402055991325"`, "OK"})
	if !ok || sw1 != 0x90 || sw2 != 0 || !atparse.CRSMOK(sw1, sw2) {
		t.Fatalf("成功应答解析错：%d/%d/%q/%v", sw1, sw2, data, ok)
	}
	if data != "98683042402055991325" {
		t.Fatalf("data = %q", data)
	}
	// 失败：本机实测读 EF_MSISDN 回 148,8（0x9408）→ 判为“文件读不了”
	sw1, sw2, _, ok = atparse.ParseCRSM([]string{"+CRSM: 148,8", "OK"})
	if !ok || atparse.CRSMOK(sw1, sw2) || !atparse.CRSMFileUnavailable(sw1, sw2) {
		t.Fatalf("0x9408 应判为文件不可读：%d/%d/%v", sw1, sw2, ok)
	}
	// 文件不存在：6A82（实测 0x6FC7 回 106,130）
	if sw1, _, _, _ = atparse.ParseCRSM([]string{"+CRSM: 106,130"}); !atparse.CRSMFileUnavailable(sw1, 130) {
		t.Fatalf("0x6A82 应判为文件不可读：%d", sw1)
	}
	// 没有 +CRSM 行
	if _, _, _, ok := atparse.ParseCRSM([]string{"ERROR"}); ok {
		t.Fatal("无 +CRSM 行时 ok 应为 false")
	}
}

// IMSI 前几位 → 运营商（与网络侧 PLMN 互相印证）
func TestModstat_IMSIPLMN(t *testing.T) {
	mcc, mnc := atparse.IMSIPLMN("460115089161379")
	if mcc != "460" || mnc != "11" {
		t.Fatalf("IMSI 拆分 = %s/%s, want 460/11（中国的 MNC 是 2 位）", mcc, mnc)
	}
	// 北美 MCC 用 3 位 MNC
	if mcc, mnc := atparse.IMSIPLMN("310260123456789"); mcc != "310" || mnc != "260" {
		t.Fatalf("北美 IMSI 拆分 = %s/%s, want 310/260", mcc, mnc)
	}
	for _, bad := range []string{"", "46011", "abcdefghijklmno"} {
		if mcc, mnc := atparse.IMSIPLMN(bad); mcc != "" || mnc != "" {
			t.Errorf("%q 不应解析出 PLMN: %s/%s", bad, mcc, mnc)
		}
	}
}

// ====================== 模组时钟 ======================

func TestParseCCLK(t *testing.T) {
	// 实测报的是 UTC（主机本地 12:31 时模组回 04:31:55）
	// ⚠️ 输入不要写死"主机本地时刻"：time.Local 在路由上常常就等于 UTC
	//（TZ 没设、也没 /etc/localtime），写死会在别的时区假失败（实测 OpenWrt 上报 -28800s）。
	// 这里按主机当前时区反推"模组该报的 UTC 时刻"，任何时区都对。
	hostLocal := time.Date(2026, 9, 29, 12, 31, 55, 0, time.Local)
	modemUTC := hostLocal.UTC()
	modemLine := "+CCLK: \"" + modemUTC.Format("06/01/02,15:04:05") + "\""
	raw, unix, skew, ok := atparse.ParseCCLK([]string{modemLine, "OK"}, hostLocal, false)
	if !ok || raw != modemUTC.Format("06/01/02,15:04:05") {
		t.Fatalf("时钟解析失败: raw=%q ok=%v", raw, ok)
	}
	if unix != modemUTC.Unix() {
		t.Errorf("unix = %d, want %d", unix, modemUTC.Unix())
	}
	// 主机本地时刻与模组上报的 UTC 是同一时刻 → 偏差应为 0
	if skew != 0 {
		t.Errorf("与主机 UTC 的偏差 = %ds, want 0", skew)
	}
	// 带旧式时区后缀：+32 = 32×15 分钟 = +8 小时 → 换算后 UTC 固定是 04:31:55
	//（这个与主机时区无关，所以直接和写死的 UTC 比）
	_, unix2, _, ok2 := atparse.ParseCCLK([]string{`+CCLK: "26/09/29,12:31:55+32"`}, time.Now(), false)
	wantUTC := time.Date(2026, 9, 29, 4, 31, 55, 0, time.UTC).Unix()
	if !ok2 || unix2 != wantUTC {
		t.Errorf("带时区的模组时钟换算错误: unix=%d, want %d", unix2, wantUTC)
	}
	// RM500U / MT5700 这类固件：带了 +32 后缀，但报的值本身就是 UTC（quirk 让它别减）
	_, unix3, _, ok3 := atparse.ParseCCLK([]string{`+CCLK: "26/09/29,10:03:23+32"`}, time.Now(), true)
	if !ok3 || unix3 != time.Date(2026, 9, 29, 10, 3, 23, 0, time.UTC).Unix() {
		t.Errorf("忽略时区后缀时换算错误: unix=%d", unix3)
	}
	if _, _, _, ok4 := atparse.ParseCCLK([]string{"OK"}, time.Now(), false); ok4 {
		t.Error("没有 +CCLK 行时不应算解析成功")
	}
}

// ====================== 温度（各厂商 6 种格式） ======================

func TestModstat_ParseQTEMPAndSummary(t *testing.T) {
	resp := []string{
		`+QTEMP:"qfe_wtr_pa0","26"`, `+QTEMP:"qfe_wtr_pa1","28"`, `+QTEMP:"qfe_wtr_pa2","29"`,
		`+QTEMP:"aoss0-usr","32"`, `+QTEMP:"mdm-q6-usr","33"`, `+QTEMP:"cpu0-a7-usr","33"`,
		`+QTEMP:"mdm-5g-usr","32"`, `+QTEMP:"mdm-kernel-usr","32"`, `+QTEMP:"xo-therm-usr","31"`,
		`+QTEMP:"sdx-case-therm-usr","31"`, "OK",
	}
	temps := atparse.ParseQTEMP(resp)
	if len(temps) != 10 {
		t.Fatalf("传感器数 = %d, want 10", len(temps))
	}
	hot, keys := atparse.PickSummary(temps)
	if hot == nil || hot.Celsius != 33 {
		t.Fatalf("最高温 = %+v, want 33", hot)
	}
	// 关键点：机身(case) / 5G / CPU，且不重复
	if len(keys) != 3 {
		t.Fatalf("关键测温点 = %d 个, want 3: %+v", len(keys), keys)
	}
	names := map[string]bool{}
	for _, k := range keys {
		names[k.Name] = true
	}
	for _, want := range []string{"sdx-case-therm-usr", "mdm-5g-usr", "cpu0-a7-usr"} {
		if !names[want] {
			t.Errorf("关键测温点缺少 %s（实际 %+v）", want, keys)
		}
	}
	// 没有 QTEMP 支持时不能 panic
	if h, k := atparse.PickSummary(nil); h != nil || k != nil {
		t.Errorf("空列表应返回 nil: %+v %+v", h, k)
	}
	if got := atparse.ParseQTEMP([]string{"ERROR"}); len(got) != 0 {
		t.Errorf("ERROR 应返回空列表: %+v", got)
	}
}

// 移远 EM05 的无名写法：`+QTEMP: 30,26,25`
func TestParseQTEMPValues(t *testing.T) {
	got := atparse.ParseQTEMPValues("+QTEMP: 30,26,25")
	if len(got) != 3 || got[0].Name != "传感器 1" || got[0].Celsius != 30 || got[2].Celsius != 25 {
		t.Fatalf("无名写法解析错误：%+v", got)
	}
	// 越界值 → 整行丢弃（别把别的行的数字当温度）
	if got := atparse.ParseQTEMPValues("+QTEMP: 30,999"); len(got) != 0 {
		t.Errorf("越界应丢弃整行：%+v", got)
	}
	if got := atparse.ParseQTEMPValues("+CSQ: 20,99"); len(got) != 0 {
		t.Errorf("别的指令不该被当成温度：%+v", got)
	}
	// parseQTEMP 两者都认
	if got := atparse.ParseQTEMP([]string{"+QTEMP: 30,26,25"}); len(got) != 3 {
		t.Errorf("ParseQTEMP 应兼容无名写法：%+v", got)
	}
}

// 广和通 FM160-EAU 实测：两种写法 + 1/1000 ℃ + 无效哨兵
func TestParseGTLADC(t *testing.T) {
	resp := []string{
		"AT+GTLADC", "+GTLADC:",
		"in_temp_pmx65_ambient_therm_input: 30562",
		"in_temp_pmx65_mdm_case_therm_input: 30843",
		"[zone32]:cpuss-0-usr 31800",
		"[zone42]:modem-beamer-usr -40960", // 该路没接
		"HWID gpio value: 000",
		"OK",
	}
	got := atparse.ParseGTLADC(resp)
	if len(got) != 3 {
		t.Fatalf("应解析出 3 路（-40960 与 HWID 行要丢掉）：%+v", got)
	}
	// 1/1000 ℃ 四舍五入：30562→31、30843→31、31800→32
	if got[0].Name != "cpuss-0-usr" || got[0].Celsius != 32 {
		t.Errorf("最高温应排在首位：%+v", got[0])
	}
	if got[2].Celsius != 31 {
		t.Errorf("换算错误：%+v", got)
	}
	if len(atparse.ParseGTLADC([]string{"ERROR"})) != 0 {
		t.Error("无应答应返回空")
	}
}

// 广和通 FM350-GL 实测：+GTSENRDTEMP / +GTZONERDMAXTEMP，1/1000 ℃
func TestParseGTSTemp(t *testing.T) {
	resp := []string{
		"+GTSENRDTEMP: 1,31773", "+GTSENRDTEMP: 2,30932", "+GTSENRDTEMP: 3,0",
		"+GTZONERDMAXTEMP: 1,31605", "OK",
	}
	got := atparse.ParseGTSTemp(resp)
	if len(got) != 3 {
		t.Fatalf("应解析出 3 路（0 = 没接要丢掉）：%+v", got)
	}
	// 名字带编号、前缀区分传感器/热区；31773 → 32 ℃
	if got[0].Name != "传感器 1" || got[0].Celsius != 32 {
		t.Errorf("传感器命名/换算错误：%+v", got[0])
	}
	found := false
	for _, s := range got {
		if s.Name == "热区 1" && s.Celsius == 32 {
			found = true
		}
	}
	if !found {
		t.Errorf("热区应命名为「热区 N」：%+v", got)
	}
}

// 鼎桥 MT5700 实测：^CHIPTEMP 12 路，0.1 ℃，65535 = 读取失败
func TestParseCHIPTEMP(t *testing.T) {
	got := atparse.ParseCHIPTEMP([]string{"AT^CHIPTEMP?", "^CHIPTEMP: 341,338,330,344,330,330,340,340,340,360,330,330", "OK"})
	if len(got) != 12 {
		t.Fatalf("应有 12 路：%+v", got)
	}
	// 高温在前：360（modem2）→ 36 ℃；341（sub3G PA）→ 34 ℃
	if got[0].Name != "modem2" || got[0].Celsius != 36 {
		t.Errorf("最高温应排首位 = %+v, want modem2 36", got[0])
	}
	byName := map[string]int{}
	for _, s := range got {
		byName[s.Name] = s.Celsius
	}
	if byName["sub3G PA"] != 34 || byName["sub6G PA"] != 34 || byName["bbp2"] != 33 {
		t.Errorf("手册命名/换算错误：%+v", byName)
	}
	// 65535 = 该路读失败，要丢掉
	got = atparse.ParseCHIPTEMP([]string{"^CHIPTEMP: 65535,100"})
	if len(got) != 1 {
		t.Fatalf("65535 应被丢掉：%+v", got)
	}
}

// ParseTempResp 按厂商顺序依次试
func TestParseTempResp(t *testing.T) {
	if got := atparse.ParseTempResp([]string{`+QTEMP:"cpu0-a7-usr","33"`}); len(got) != 1 {
		t.Errorf("应优先移远格式：%+v", got)
	}
	if got := atparse.ParseTempResp([]string{"+CPMUTEMP: 25"}); len(got) != 1 || got[0].Celsius != 25 {
		t.Errorf("芯讯通单值：%+v", got)
	}
	if got := atparse.ParseTempResp([]string{"+MTSM: 60"}); len(got) != 1 || got[0].Celsius != 60 {
		t.Errorf("广和通 MTSM：%+v", got)
	}
	if got := atparse.ParseTempResp([]string{"^CHIPTEMP: 400,300"}); len(got) != 2 {
		t.Errorf("鼎桥 CHIPTEMP：%+v", got)
	}
	if got := atparse.ParseTempResp([]string{"ERROR"}); got != nil {
		t.Errorf("认不出应返回 nil：%+v", got)
	}
}

// ---- from: netinfo/extra_test.go ----

//
// 手册 §4.2.18 p.106 的字段定义：
//
//	+CNWINFO: <SYS_MODE>,<EGCI>,<eNBID>,<MCS>,<DL_MOD>,<UL_MOD>,<CQI>,<TX_POWER>[,<BW>]
// 实测原始行（SIM8200EA-M2 / 中国电信 5G SA）：
//
//	+CNWINFO: NR5G,4601155735066633,0xCFA11A0,0,UNKOWN,UNKOWN,0,0,100M
//
// EGCI = PLMN+CellID 直接拼（460|11|55735066633）；调制字段固件拼错了（UNKOWN）→ 当未上报。

func TestParseExtraNetInfoRealNR(t *testing.T) {
	line := `+CNWINFO: NR5G,4601155735066633,0xCFA11A0,0,UNKOWN,UNKOWN,0,0,100M`
	ei, ok := ParseExtraNetInfo([]string{line, "OK"})
	if !ok {
		t.Fatalf("解析失败: %s", line)
	}
	if ei.SysMode != "NR5G" {
		t.Errorf("制式 = %q, want NR5G", ei.SysMode)
	}
	if ei.EGCI != "4601155735066633" {
		t.Errorf("EGCI = %q, want 4601155735066633", ei.EGCI)
	}
	if ei.ENBID != "0xCFA11A0" {
		t.Errorf("eNBID = %q, want 0xCFA11A0（前缀小写、数字大写）", ei.ENBID)
	}
	if ei.MCS != 0 || ei.CQI != 0 {
		t.Errorf("MCS/CQI = %d/%d, want 0/0", ei.MCS, ei.CQI)
	}
	if ei.DLMod != "" || ei.ULMod != "" {
		t.Errorf("固件写的是 UNKOWN（拼写错误），应当当未上报: %q/%q", ei.DLMod, ei.ULMod)
	}
	if ei.BWmHz != 100 {
		t.Errorf("带宽 = %d MHz, want 100（末尾多出来的那格）", ei.BWmHz)
	}
}

// 手册里的 LTE 例（没有带宽那一格，调制是正常值）
func TestParseExtraNetInfoLTE(t *testing.T) {
	ei, ok := ParseExtraNetInfo([]string{`+CNWINFO: LTE,4601195333890,0x5AEAE,1,QPSK,16QAM,15,9`, "OK"})
	if !ok {
		t.Fatal("手册示例应能解析")
	}
	if ei.SysMode != "LTE" || ei.MCS != 1 || ei.CQI != 15 || ei.TxPower != 9 {
		t.Errorf("字段不对: %+v", ei)
	}
	if ei.DLMod != "QPSK" || ei.ULMod != "16QAM" {
		t.Errorf("调制 = %q/%q, want QPSK/16QAM", ei.DLMod, ei.ULMod)
	}
	if ei.BWmHz != 0 {
		t.Errorf("没有带宽字段时应为 0, got %d", ei.BWmHz)
	}
}

// 越界 / 缺字段 / 无关行：一律不解析（宁可空白也不显示错的数据）
func TestParseExtraNetInfoRejects(t *testing.T) {
	for _, line := range []string{
		`+CNWINFO: NR5G,4601155735066633,0xCFA11A0,0,UNKOWN,UNKOWN,0`, // 字段不够
		`+CNWINFO: WCDMA,4601155735066633,0xCFA11A0,0,QPSK,16QAM,0,0`, // 制式只认 LTE/NR5G
		`+CNWINFO: NR5G,ABCDEF,0xCFA11A0,0,QPSK,16QAM,0,0`,            // EGCI 不是十进制
		`+CPSI: NR5G_SA,Online,460-11`,                                // 别的指令
		`ERROR`,
		``,
	} {
		if ei, ok := ParseExtraNetInfo([]string{line}); ok {
			t.Errorf("不该解析成功: %q → %+v", line, ei)
		}
	}
	// 越界的 MCS/CQI/功率 → 记成"未上报"，但整行仍然有效
	ei, ok := ParseExtraNetInfo([]string{`+CNWINFO: LTE,4601195333890,0x5AEAE,99,QPSK,16QAM,99,999`})
	if !ok {
		t.Fatal("结构合法就该解析成功")
	}
	if ei.MCS != -1 || ei.CQI != -1 || ei.TxPower != -99 {
		t.Errorf("越界值应记成未上报(-1/-1/-99): mcs=%d cqi=%d tx=%d", ei.MCS, ei.CQI, ei.TxPower)
	}
}

// ====================== 鼎桥 MT5700 工作频率 AT^HFREQINFO ======================

// 实测 MT5700M-CN（NR n78，20MHz）与手册的 LTE 例
func TestParseHFREQINFO(t *testing.T) {
	ei, ok := ParseHFREQINFO([]string{"AT^HFREQINFO?", "^HFREQINFO: 0,7,78,627300,3409500,20000,630000,3450000,100000", "OK"})
	if !ok {
		t.Fatal("实测 NR 应答应能解析")
	}
	if ei.Band != 78 || ei.BWmHz != 20 {
		t.Errorf("NR: band=%d bw=%dMHz, want 78 / 20", ei.Band, ei.BWmHz)
	}
	if ei.MCS != -1 || ei.CQI != -1 || ei.TxPower != -99 {
		t.Errorf("这三项该是未上报: %+v", ei)
	}
	ei2, ok := ParseHFREQINFO([]string{"^HFREQINFO: 0,6,18,5925,8675,20000,23925,8225,20000"})
	if !ok || ei2.Band != 18 || ei2.BWmHz != 20 {
		t.Errorf("LTE: %+v ok=%v, want band 18 / 20MHz", ei2, ok)
	}
	// 别的指令行不能被当成 HFREQINFO
	if _, ok := ParseHFREQINFO([]string{"+CNWINFO: NR5G,4601155735066633,0xCFA11A0,0,UNKOWN,UNKOWN,0,0,100M"}); ok {
		t.Error("+CNWINFO 行不该被 HFREQINFO 解析")
	}
}

// ====================== 供电电压 AT+CBC ======================

func TestParseCBC(t *testing.T) {
	// 实测芯讯通 SIM8200：带单位，直接就是伏
	if got := atparse.ParseCBC([]string{"+CBC: 3.858V", "OK"}); got != "3.858 V" {
		t.Errorf("芯讯通格式 = %q, want \"3.858 V\"", got)
	}
	// 3GPP/移远格式：<bcs>,<bcl(mV)>
	if got := atparse.ParseCBC([]string{"+CBC: 0,3858", "OK"}); got != "3.858 V" {
		t.Errorf("3GPP 格式 = %q, want \"3.858 V\"", got)
	}
	// 移远三字段：第二个是百分比，第三个才是 mV
	if got := atparse.ParseCBC([]string{"+CBC: 0,71,3862"}); got != "3.862 V" {
		t.Errorf("移远三字段格式 = %q, want \"3.862 V\"", got)
	}
	for _, line := range []string{"OK", "ERROR", "+CBC:", "+CBC: 0,0", "+CBC: 0,99999", ""} {
		if got := atparse.ParseCBC([]string{line}); got != "" {
			t.Errorf("%q 不该解析出电压: %q", line, got)
		}
	}
}

// 调制方式归一：固件拼错的 "UNKOWN" / 未知值都当未上报
func TestNormalizeMod(t *testing.T) {
	for in, want := range map[string]string{
		"QPSK": "QPSK", "16qam": "16QAM", "256QAM": "256QAM", " 1024qam ": "1024QAM",
		"UNKOWN": "", "": "", "8PSK": "",
	} {
		if got := NormalizeMod(in); got != want {
			t.Errorf("NormalizeMod(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- from: netinfo/netinfo_test.go ----

// 以下应答均来自实机（RM502Q-GL，fw RM502QGLAAR11A02M4G）
// TestParseQNWINFO 网络类型/频段/频点解析

func TestParseQNWINFO(t *testing.T) {
	cases := []struct {
		name     string
		resp     []string
		tech     string
		plmn     string
		bandText string
		earfcn   int
		ok       bool
	}{
		{
			name:     "已注册 NR SA",
			resp:     []string{`AT+QNWINFO`, `+QNWINFO: "TDD NR5G","46011","NR5G BAND 78",627264`, "OK"},
			tech:     "TDD NR5G",
			plmn:     "46011",
			bandText: "NR5G BAND 78",
			earfcn:   627264,
			ok:       true,
		},
		{
			name:     "已注册 LTE",
			resp:     []string{`+QNWINFO: "FDD LTE","46011","LTE BAND 5",2452`, "OK"},
			tech:     "FDD LTE",
			plmn:     "46011",
			bandText: "LTE BAND 5",
			earfcn:   2452,
			ok:       true,
		},
		{
			// ⚠️ PLMN 不带引号（RM502Q-GL / RM500U-CNV 实测）：早期正则要求带引号
			// → 整条解析失败 → 界面「频段」永远是 `-`
			name:     "PLMN 裸数字",
			resp:     []string{`+QNWINFO: "FDD LTE",46011,"LTE BAND 3",1850`, "OK"},
			tech:     "FDD LTE",
			plmn:     "46011",
			bandText: "LTE BAND 3",
			earfcn:   1850,
			ok:       true,
		},
		{
			name: "未注册（No Service 无引号）",
			resp: []string{`+QNWINFO: No Service`, "OK"},
			ok:   false,
		},
		{
			name: "空应答",
			resp: []string{"OK"},
			ok:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			qi, ok := ParseQNWINFO(c.resp)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if !c.ok {
				return
			}
			if qi.NetTech != c.tech || qi.PLMN != c.plmn || qi.BandText != c.bandText || qi.EARFCN != c.earfcn {
				t.Fatalf("got (%q,%q,%q,%d), want (%q,%q,%q,%d)",
					qi.NetTech, qi.PLMN, qi.BandText, qi.EARFCN, c.tech, c.plmn, c.bandText, c.earfcn)
			}
		})
	}
}

// TestParseReg CREG/CGREG 解析。
// 重点是短格式（只有 stat）不能崩，且 5G SA 下 CREG 为 0,0 要如实反映"CS 未注册"。
func TestParseReg(t *testing.T) {
	cases := []struct {
		name       string
		resp       []string
		stat       int
		text       string
		registered bool
		roaming    bool
		lac        string
		cellID     string
		act        int
	}{
		{
			name: "5G SA 下 CREG 短格式",
			resp: []string{`+CREG: 0,0`, "OK"},
			stat: 0, text: "未注册，未在搜索",
		},
		{
			name: "正在搜网",
			resp: []string{`+CREG: 0,2`, "OK"},
			stat: 2, text: "未注册，正在搜索",
		},
		{
			name: "已注册并带位置信息",
			resp: []string{`+CREG: 0,1,"1A2B","12345678",7`, "OK"},
			stat: 1, text: "已注册，本地网", registered: true,
			lac: "1A2B", cellID: "12345678", act: 7,
		},
		{
			name: "PS 域已注册",
			resp: []string{`+CGREG: 0,1,"1A2B","12345678",11`, "OK"},
			stat: 1, text: "已注册，本地网", registered: true,
			lac: "1A2B", cellID: "12345678", act: 11,
		},
		{
			name: "漫游",
			resp: []string{`+CREG: 0,5`, "OK"},
			stat: 5, text: "已注册，漫游中", registered: true, roaming: true,
		},
		{
			name: "注册被拒绝",
			resp: []string{`+CREG: 0,3`, "OK"},
			stat: 3, text: "注册被拒绝",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs, _ := ParseReg(c.resp)
			if rs.Stat != c.stat || rs.Text != c.text ||
				rs.Registered != c.registered || rs.Roaming != c.roaming ||
				rs.LAC != c.lac || rs.CellID != c.cellID || rs.ACT != c.act {
				t.Fatalf("got %+v, want stat=%d text=%q reg=%v roam=%v lac=%q ci=%q act=%d",
					rs, c.stat, c.text, c.registered, c.roaming, c.lac, c.cellID, c.act)
			}
		})
	}

	// 无 +CREG 行时不应崩，并给出"未获取"
	rs, ok := ParseReg([]string{"OK"})
	if ok || rs.Text != "未获取" {
		t.Fatalf("空应答应返回未获取，got %+v ok=%v", rs, ok)
	}
}

// 5G SA 兜底要用的两条：+C5GREG（5GS）与 +CEREG（EPS）字段与 +CREG 完全一致，
// 共用一份解析；实测 RM502Q-GL：CREG/CGREG/CEREG 全是 0,0，而 +C5GREG: 0,1 表示已注册。
func TestParseRegNROrEPS(t *testing.T) {
	rs, ok := ParseReg([]string{"AT+C5GREG?", "+C5GREG: 0,1", "OK"})
	if !ok || !rs.Registered || rs.Stat != 1 || rs.Text != "已注册，本地网" {
		t.Fatalf("+C5GREG 解析失败: %+v ok=%v", rs, ok)
	}
	if rs.Raw != "+C5GREG: 0,1" {
		t.Errorf("原始行 = %q, want +C5GREG: 0,1", rs.Raw)
	}
	// 带位置信息的写法：+C5GREG: 2,1,"0001","1234",11
	rs, ok = ParseReg([]string{`+C5GREG: 2,1,"0001","1234",11`})
	if !ok || !rs.Registered || rs.LAC != "0001" || rs.CellID != "1234" || rs.ACT != 11 {
		t.Fatalf("带位置信息的 +C5GREG 解析失败: %+v ok=%v", rs, ok)
	}
	// EPS 同样认
	rs, ok = ParseReg([]string{"+CEREG: 0,5"})
	if !ok || !rs.Registered || !rs.Roaming {
		t.Fatalf("+CEREG 解析失败: %+v ok=%v", rs, ok)
	}
	// 不能误伤别的行（这些都不该被当成注册状态）
	for _, line := range []string{"+CGSN: 12345", "+CGSMS: 0", "+COPS: 0,0,\"???\",11", "OK", "+CSQ: 20,99"} {
		if _, ok := ParseReg([]string{line}); ok {
			t.Errorf("%q 不该被解析成注册状态", line)
		}
	}
}

// TestTechLabel 制式友好名。
//
// 注意模组上报的是 "TDD NR5G" 这种"双工+制式"组合串，
// 不能直接用 NormalizeTech（它按整个 token 精确匹配）。
func TestTechLabel(t *testing.T) {
	cases := []struct {
		in     string
		tech   CellTech
		label  string
		duplex string
	}{
		{"TDD NR5G", CellTechNR, "5G NR (TDD)", "TDD"},
		{"FDD NR5G", CellTechNR, "5G NR (FDD)", "FDD"},
		{"NR5G-SA", CellTechNR, "5G NR", ""},
		{"FDD LTE", CellTechLTE, "4G LTE (FDD)", "FDD"},
		{"LTE", CellTechLTE, "4G LTE", ""},
		{"GSM", CellTechGSM, "2G GSM", ""},
		{"WCDMA", CellTechWCDMA, "3G WCDMA", ""},
		{"", "", "", ""},
	}
	for _, c := range cases {
		tech, label := TechLabel(c.in)
		if tech != c.tech || label != c.label {
			t.Errorf("TechLabel(%q) = (%q,%q), want (%q,%q)", c.in, tech, label, c.tech, c.label)
		}
		if got := DuplexOf(c.in); got != c.duplex {
			t.Errorf("DuplexOf(%q) = %q, want %q", c.in, got, c.duplex)
		}
	}
}

// TestSplitPLMN PLMN 拆分与运营商推断
func TestSplitPLMN(t *testing.T) {
	cases := []struct {
		plmn string
		mcc  string
		mnc  string
	}{
		{"46011", "460", "11"},
		{"46000", "460", "00"},
		{"460001", "460", "001"}, // 3 位 MNC
		{"26201", "262", "01"},
		{"", "", ""},
		{"460", "", ""},   // 太短
		{"4601a", "", ""}, // 非数字
	}
	for _, c := range cases {
		mcc, mnc := SplitPLMN(c.plmn)
		if mcc != c.mcc || mnc != c.mnc {
			t.Errorf("SplitPLMN(%q) = (%q,%q), want (%q,%q)", c.plmn, mcc, mnc, c.mcc, c.mnc)
		}
	}
	// 46011 → 中国电信（与 cell.go 的 OperatorName 保持一致）
	mcc, mnc := SplitPLMN("46011")
	if got := cell.OperatorName(mcc, mnc); got != "中国电信" {
		t.Errorf("OperatorName(460,11) = %q, want 中国电信", got)
	}
}

// TestRegTextAllStats 注册状态文案覆盖 0-5
func TestRegTextAllStats(t *testing.T) {
	for stat := 0; stat <= 5; stat++ {
		if got := RegText(stat); got == "" || got == "未知状态" {
			t.Errorf("RegText(%d) 未覆盖，got %q", stat, got)
		}
	}
	if got := RegText(99); got != "未知状态" {
		t.Errorf("RegText(99) = %q, want 未知状态", got)
	}
}

// TestBandTextFor 频段串拼接（QNWINFO 不可用时的兜底路径）
func TestBandTextFor(t *testing.T) {
	if got := BandTextFor(CellTechNR, 78); got != "NR5G BAND 78" {
		t.Errorf("BandTextFor(NR,78) = %q, want NR5G BAND 78", got)
	}
	if got := BandTextFor(CellTechLTE, 5); got != "LTE BAND 5" {
		t.Errorf("BandTextFor(LTE,5) = %q, want LTE BAND 5", got)
	}
	if got := BandTextFor(CellTechNR, 0); got != "" {
		t.Errorf("BandTextFor(NR,0) 应为空，got %q", got)
	}
}

// TestIsAllDigits 空串为 true（调用方需自行判非空）
func TestIsAllDigits(t *testing.T) {
	if !atparse.IsAllDigits("") || !atparse.IsAllDigits("46011") {
		t.Error("空串与纯数字应返回 true")
	}
	if atparse.IsAllDigits("4601a") || atparse.IsAllDigits("46 11") {
		t.Error("含非数字字符应返回 false")
	}
}

// ---- from: params/params_test.go ----

// 实测：AT+CGDCONT? → 4 个上下文（数据 / IMS / ctwap / sos）

func TestParseCGDCONTRealHardware(t *testing.T) {
	resp := []string{
		`+CGDCONT: 1,"IPV4V6","3GNET","0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0",0,0,0,0,,,,,,,,,"",,,,0`,
		`+CGDCONT: 2,"IPV4V6","IMS","0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0",0,0,0,0,,,,,,,,,"",,,,0`,
		`+CGDCONT: 3,"IPV4V6","ctwap","0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0",0,0,0,0,,,,,,,,,"",,,,0`,
		`+CGDCONT: 4,"IPV4V6","sos","0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0",0,0,0,1,,,,,,,,,"",,,,0`,
		"OK",
	}
	list := ParseCGDCONT(resp)
	if len(list) != 4 {
		t.Fatalf("上下文数 = %d, want 4", len(list))
	}
	if list[0].CID != 1 || list[0].APN != "3GNET" || list[0].PDPType != "IPV4V6" {
		t.Errorf("第 1 个上下文不对: %+v", list[0])
	}
	if list[0].Internal {
		t.Error("3GNET 是数据 APN，不该标成内部")
	}
	// IMS / sos 属于内部专用，界面要标出来（它们不用于上网）
	if !list[1].Internal || !list[3].Internal {
		t.Errorf("IMS / sos 应标为内部: %+v", list)
	}
	// 空应答不能 panic
	if got := ParseCGDCONT(nil); len(got) != 0 {
		t.Errorf("空应答应返回空列表，得到 %+v", got)
	}
}

// 实测：AT+CGACT? → cid 1/2 已激活，3/4 未激活
func TestParseCGACT(t *testing.T) {
	act := ParseCGACT([]string{"+CGACT: 1,1", "+CGACT: 2,1", "+CGACT: 3,0", "+CGACT: 4,0", "OK"})
	if !act[1] || !act[2] || act[3] || act[4] {
		t.Fatalf("激活状态解析错误: %+v", act)
	}
	if len(ParseCGACT([]string{"OK"})) != 0 {
		t.Error("没有 +CGACT 行时应返回空 map")
	}
}

// ====================== PDP 激活（AT+CGACT） ======================

// 指令拼装：激活 = AT+CGACT=1,<cid>，停用 = AT+CGACT=0,<cid>
func TestCmdCGACT(t *testing.T) {
	if got := CmdCGACT(1, true); got != "AT+CGACT=1,1" {
		t.Errorf("激活指令 = %q, want AT+CGACT=1,1", got)
	}
	if got := CmdCGACT(3, false); got != "AT+CGACT=0,3" {
		t.Errorf("停用指令 = %q, want AT+CGACT=0,3", got)
	}
}

// 动作编排：重新激活时，只有"本来已激活"才需要先断开（多一次 AT+CGACT=0）
func TestPDPSteps(t *testing.T) {
	cases := []struct {
		name                     string
		active, wasActive, recon bool
		want                     []bool
	}{
		{"激活未激活的", true, false, false, []bool{true}},
		{"停用已激活的", false, true, false, []bool{false}},
		{"重新激活（原来已激活）→ 先断后连", true, true, true, []bool{false, true}},
		{"重新激活（原来没激活）→ 直接激活", true, false, true, []bool{true}},
		{"重新激活忽略 active=false", false, true, true, []bool{false, true}},
	}
	for _, c := range cases {
		got := Steps(c.active, c.wasActive, c.recon)
		if len(got) != len(c.want) {
			t.Errorf("%s: 步骤数 = %d, want %d (%v)", c.name, len(got), len(c.want), got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: 步骤 %v, want %v", c.name, got, c.want)
				break
			}
		}
		// 最后一步永远是"期望的最终状态"
		if last := got[len(got)-1]; c.recon && !last {
			t.Errorf("%s: 重新激活的最后一步必须是激活, got %v", c.name, got)
		}
	}
}

// ByCID：必须返回切片元素的地址（不能是循环变量副本），找不到要返回 nil
func TestContextByCID(t *testing.T) {
	list := []Context{
		{CID: 1, APN: "3GNET"},
		{CID: 2, APN: "IMS", Internal: true},
		{CID: 3, APN: "ctwap"},
	}
	c := ByCID(list, 2)
	if c == nil || c.APN != "IMS" || !c.Internal {
		t.Fatalf("cid=2 查错了: %+v", c)
	}
	// 改返回值必须改到原切片（证明返回的是元素地址而不是副本）
	c.Active = true
	if !list[1].Active {
		t.Error("ByCID 返回的应是切片元素的地址")
	}
	if got := ByCID(list, 9); got != nil {
		t.Errorf("不存在的 cid 应返回 nil, got %+v", got)
	}
	if got := ByCID(nil, 1); got != nil {
		t.Errorf("空列表应返回 nil, got %+v", got)
	}
}

func TestActiveWord(t *testing.T) {
	if ActiveWord(true) != "激活" || ActiveWord(false) != "停用" {
		t.Error("激活状态文案不对")
	}
}

// 内部上下文（IMS/sos）现在允许手动激活/重新激活 ——
// 实测有些模组出厂时 IMS 未激活，不给手动启动就一直起不来；
// 而部分固件不许手动停内部上下文（AT+CGACT=0,2 回 ERROR），
// 所以"重新激活"里的断开是 best-effort：被拒也要继续去激活。
func TestPDPInternalContextPolicy(t *testing.T) {
	// 内部上下文：停用被拒可以忽略并继续激活
	if !DeactivateBestEffort(true) {
		t.Error("内部上下文的停用应允许 best-effort（否则重新激活永远做不成）")
	}
	// 普通上网上下文：失败就是失败，不能悄悄继续
	if DeactivateBestEffort(false) {
		t.Error("普通上下文的停用失败不该被忽略")
	}
	// 内部上下文的动作编排与其他上下文一致：已激活时重新激活 = 先断后连
	steps := Steps(true, true, true)
	if len(steps) != 2 || steps[0] != false || steps[1] != true {
		t.Errorf("内部上下文重新激活步骤 = %v, want [false true]", steps)
	}
}

// 实测三种应答：自动模式（名字格式，可能是 "????"）/ 手动（数字格式）/ 无运营商
func TestParseCOPS(t *testing.T) {
	ns, ok := ParseCOPS([]string{`+COPS: 0,0,"????",11`, "OK"})
	if !ok || ns.Mode != 0 || ns.ModeText != "自动选网" {
		t.Fatalf("自动模式解析错误: %+v ok=%v", ns, ok)
	}
	// "????" 不是 PLMN，不能当 PLMN 展示（应落在 Operator）
	if ns.PLMN != "" || ns.Operator != "????" {
		t.Errorf("名字格式应进 Operator 而不是 PLMN: %+v", ns)
	}
	if ns.AcT != 11 {
		t.Errorf("AcT = %d, want 11", ns.AcT)
	}

	ns, ok = ParseCOPS([]string{`+COPS: 1,2,"46011",7`})
	if !ok || ns.Mode != 1 || ns.ModeText != "手动选网" || ns.PLMN != "46011" || ns.AcT != 7 {
		t.Errorf("手动模式解析错误: %+v", ns)
	}

	// +COPS: 0（无运营商字段）
	ns, ok = ParseCOPS([]string{"+COPS: 0"})
	if !ok || ns.Mode != 0 || ns.PLMN != "" {
		t.Errorf("裸 +COPS 解析错误: %+v ok=%v", ns, ok)
	}

	if _, ok := ParseCOPS([]string{"ERROR"}); ok {
		t.Error("ERROR 不该解析成功")
	}
	if CopsModeText(4) != "手动转自动" || CopsModeText(2) != "已注销（去注册）" || CopsModeText(9) != "未知模式" {
		t.Error("模式文案不对")
	}
}

// ====================== 校验（非法值必须挡在 AT 之前） ======================

func TestValidAPN(t *testing.T) {
	good := []string{"3GNET", "ctnet", "ctwap", "internet", "a.b-c_1", "CMNET", strings.Repeat("a", 100)}
	for _, s := range good {
		if !ValidAPN(s) {
			t.Errorf("%q 应通过校验", s)
		}
	}
	// 逗号/引号会破坏 AT 指令语法；空格与中文也不是合法 APN 字符
	bad := []string{"", "bad,apn", `ba"d`, "a b", "中文", strings.Repeat("a", 101), "#", "\n", "apn%"}
	for _, s := range bad {
		if ValidAPN(s) {
			t.Errorf("%q 应被拒绝", s)
		}
	}
}

func TestValidPDPTypeAndCID(t *testing.T) {
	for _, s := range []string{"IP", "ip", "IPV6", "ipv4v6", " IPV4V6 "} {
		if !ValidPDPType(s) {
			t.Errorf("%q 应通过", s)
		}
	}
	for _, s := range []string{"", "PPP", "IPV4V6X", "IPV4", "1"} {
		if ValidPDPType(s) {
			t.Errorf("%q 应被拒绝", s)
		}
	}
	// 实测 RM502Q-GL 的 AT+CGDCONT=? 报 (1-42)；广和通 FM650-CN 报 **(0-11)**
	// 且 `AT+CGDCONT?` 里真有 cid 0（默认承载，带 IP）→ 下限放宽到 0。
	if !ValidCID(0) || !ValidCID(1) || !ValidCID(42) {
		t.Error("0 / 1 / 42 应合法")
	}
	for _, c := range []int{-1, 43, 100} {
		if ValidCID(c) {
			t.Errorf("cid=%d 应被拒绝", c)
		}
	}
}

func TestValidPLMN(t *testing.T) {
	for _, s := range []string{"46011", "460015", "46000"} {
		if !ValidPLMN(s) {
			t.Errorf("%q 应通过", s)
		}
	}
	for _, s := range []string{"", "4601", "4601111", "46x11", " 46011", "46011 "} {
		if ValidPLMN(s) {
			t.Errorf("%q 应被拒绝", s)
		}
	}
}

// 指令拼装：一定要与实测过的格式一致（多一个空格/引号都会被模组拒绝）
func TestCmdCGDCONT(t *testing.T) {
	if got := CmdCGDCONT(1, "ipv4v6", "3GNET"); got != `AT+CGDCONT=1,"IPV4V6","3GNET"` {
		t.Errorf("拼装结果 = %q", got)
	}
}

// 回读确认：APN 大小写不敏感比较（有的固件会把 APN 转成大写存）
func TestApnInContexts(t *testing.T) {
	list := []Context{{CID: 1, APN: "3GNET"}, {CID: 3, APN: "ctwap"}}
	if !APNInContexts(list, 1, "3gnet") {
		t.Error("大小写不同应视为同一个 APN")
	}
	if APNInContexts(list, 1, "ctnet") {
		t.Error("值不同不该判为已生效")
	}
	if APNInContexts(list, 9, "3GNET") {
		t.Error("cid 不存在应返回 false")
	}
}

// ---- from: pdp/pdp_test.go ----

// TestFormatIPv6 RFC 5952 格式：小写、去前导零、最长零段压缩

func TestFormatIPv6(t *testing.T) {
	cases := []struct {
		bytes []byte
		want  string
	}{
		{[]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, "::1"},
		{[]byte{0xfe, 0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, "fe80::1"},
		{make([]byte, 16), "::"},
		// 240e:400:618:c4b:18d9:81d2:a03:1a1e（实测电信地址样式，前导零要去掉）
		{[]byte{0x24, 0x0e, 0x04, 0x00, 0x06, 0x18, 0x0c, 0x4b, 0x18, 0xd9, 0x81, 0xd2, 0x0a, 0x03, 0x1a, 0x1e},
			"240e:400:618:c4b:18d9:81d2:a03:1a1e"},
		// 只有一段零组时不压缩（RFC 5952 要求至少两段）
		{[]byte{0x20, 0x01, 0, 0, 0x12, 0x34, 0, 0, 0, 0, 0, 0, 0, 0, 0, 5}, "2001:0:1234::5"},
	}
	for _, c := range cases {
		got := formatIPv6(c.bytes)
		if got != c.want {
			t.Errorf("formatIPv6(%v) = %q, want %q", c.bytes, got, c.want)
		}
		if c.want != "" && net.ParseIP(got) == nil {
			t.Errorf("%q 不是合法 IPv6", got)
		}
	}
	if got := formatIPv6(make([]byte, 5)); got != "" {
		t.Errorf("长度不是 16 应返回空串，实际 %q", got)
	}
}

// TestSplitPDPAddrs 实测字段：IPv4 与 16 字节形态的 IPv6 用逗号分隔
func TestSplitPDPAddrs(t *testing.T) {
	field := "10.149.102.75,36.14.4.0.6.24.12.75.24.217.129.210.10.3.26.30"
	v4, v6 := SplitAddrs(field)
	if v4 != "10.149.102.75" {
		t.Errorf("ipv4 = %q", v4)
	}
	if v6 != "240e:400:618:c4b:18d9:81d2:a03:1a1e" {
		t.Errorf("ipv6 = %q", v6)
	}
	if net.ParseIP(v6) == nil {
		t.Errorf("解析出的 IPv6 不合法: %q", v6)
	}

	// 只有 IPv4
	if v4, v6 := SplitAddrs("192.168.1.2"); v4 != "192.168.1.2" || v6 != "" {
		t.Errorf("只有 IPv4 时 = %q / %q", v4, v6)
	}
	// 只有 IPv6（IMS 上下文实测形态）
	if v4, v6 := SplitAddrs("36.14.5.120.6.32.0.214.24.217.129.211.40.108.195.174"); v4 != "" || v6 == "" {
		t.Errorf("只有 IPv6 时 = %q / %q", v4, v6)
	}
	// 空字段 / 垃圾
	for _, s := range []string{"", "  ", "abc", "1.2.3", "999.1.1.1"} {
		if v4, v6 := SplitAddrs(s); v4 != "" || v6 != "" {
			t.Errorf("SplitAddrs(%q) 不应解析出地址: %q / %q", s, v4, v6)
		}
	}
}

// TestParseCGPADDR 解析 AT+CGPADDR 完整应答（实测 RM502Q-GL）
func TestParseCGPADDR(t *testing.T) {
	resp := []string{
		`+CGPADDR: 1,"10.149.102.75,36.14.4.0.6.24.12.75.24.217.129.210.10.3.26.30"`,
		`+CGPADDR: 2,"36.14.5.120.6.32.0.214.24.217.129.211.40.108.195.174"`,
		`+CGPADDR: 3,""`,
		`+CGPADDR: 4,""`,
		"OK",
	}
	v4, v6 := ParseCGPADDR(resp, DefaultDataCID)
	if v4 != "10.149.102.75" {
		t.Errorf("ipv4 = %q, want 10.149.102.75", v4)
	}
	if v6 != "240e:400:618:c4b:18d9:81d2:a03:1a1e" {
		t.Errorf("ipv6 = %q", v6)
	}
}

// TestParseCGPADDRFallback 没有 cid=1 时取第一个非空上下文
func TestParseCGPADDRFallback(t *testing.T) {
	resp := []string{
		`+CGPADDR: 3,""`,
		`+CGPADDR: 5,"192.168.9.9"`,
		"OK",
	}
	if v4, _ := ParseCGPADDR(resp, DefaultDataCID); v4 != "192.168.9.9" {
		t.Errorf("应回退到第一个非空上下文，实际 %q", v4)
	}
	// 全空
	if v4, v6 := ParseCGPADDR([]string{`+CGPADDR: 1,""`, "OK"}, DefaultDataCID); v4 != "" || v6 != "" {
		t.Errorf("全空时不应有地址：%q / %q", v4, v6)
	}
}

// ---- from: sms/pdu_test.go ----

//
// 以下 PDU 全部来自鼎桥 MT5700M-CN（fw V200R001C20B016）的 `AT+CMGF=0` + `AT+CMGL=4`
// 实测应答（原样拷过来，没有手工重组）。括号里标注的「文本模式头」是同一条短信在
// `AT+CMGF=1` 下 +CMGL 头里报的号码/时间 —— 用来交叉验证号码/时间解码：
// idx 0/1/2 报 "10001"、idx 8/9/10 报 "10000"，两边的号码与时间均**完全一致**（已逐条对过）。

const (
	// idx 2：单条 UCS2（DCS=0x08），正文「你好」
	// 文本模式头：+CMGL: 2,"REC READ","13520309333",,"26/09/28,22:21:40+32"
	mt5700PDUSingle = "0891688109520000F0240BA13125309033F3000862908222120423044F60597D"

	// idx 0：UCS2 + UDH 拼接（ref=0x5E total=2 seq=2），正文「…【中国电信】」
	// 文本模式头：+CMGL: 0,"REC READ","10001",,"25/04/29,10:56:44+32"
	mt5700PDUPart2 = "099164000339745702F06405A00100F1000852409201654423800500035E0202002E00310038" +
		"0039002E0063006E002F007900590045004500720041002067E5770B8BE660C530025FAE4FE151736CE8201C" +
		"6C5F82CF75354FE1201DFF0C56DE590D201C81EA52A85145503C201D5F00901A5FAE4FE181EA52A8514550" +
		"3CFF0C514D53D7505C673A70E6607C300230104E2D56FD75354FE13011"

	// idx 4：UCS2 + UDH 拼接（ref=0x87 total=2 seq=2），尾包很短（正文「水！」）
	mt5700PDUPart2b = "099164000339745702F04408A1201500100008525041906095230A0500038702026C34FF01"

	// idx 9：UCS2 + UDH 拼接（ref=0x26 total=3 seq=1）
	// 文本模式头：+CMGL: 9,"REC READ","10000",,"26/09/29,11:14:05+32"
	mt5700PDUPart3 = "099164000339745702F06405A00100F00008629092114150236005000326030130104F59989D4E0D8DB3" +
		"63D0919230115C0A656C76845BA26237FF0C622A81F30032003965E50031003165F6003100325206FF0C" +
		"60A8540D4E0B003100380030002A002A002A002A003700370031003976848D2662374F59989D"

	// idx 10：UCS2 + UDH 拼接（ref=0x26 total=3 seq=2，中间段）
	mt5700PDUPart3c = "099164000339745702F06405A00100F0000862909211415023600500032603025DF24E0D8DB3003100" +
		"305143FF0C4E3A4E864FDD969C60A876846B635E38901A8BAFFF0C70B951FB0020006800740074007000" +
		"3A002F002F0061002E003100380039002E0063006E002F007900590045004500720041002067E5"

	// idx 8：UCS2 + UDH 拼接（ref=0x26 total=3 seq=3，尾部）
	mt5700PDUPart3b = "099164000339745702F06405A00100F000086290921141502360050003260303770B8BE660C53002" +
		"5FAE4FE151736CE8201C6C5F82CF75354FE1201DFF0C56DE590D201C81EA52A85145503C201D5F00901A" +
		"5FAE4FE181EA52A85145503CFF0C514D53D7505C673A70E6607C300230104E2D56FD75354FE13011"
)

func TestDecodePDUMT5700Single(t *testing.T) {
	s, ci, err := smspkg.DecodePDU(mt5700PDUSingle)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if s.Number != "13520309333" {
		t.Errorf("号码 = %q, want 13520309333（TON=national 不能补 +）", s.Number)
	}
	if s.Timestamp != "26/09/28,22:21:40+32" {
		t.Errorf("时间 = %q, want 26/09/28,22:21:40+32（与文本模式头一致）", s.Timestamp)
	}
	if s.Content != "你好" {
		t.Errorf("正文 = %q, want 你好（DCS=0x08 UCS2）", s.Content)
	}
	if ci.Total != 0 {
		t.Errorf("单条短信不该有拼接信息：%+v", ci)
	}
	// 这条的时间戳必须能被排序用的 TimeKey 认出来（与文本模式同格式）
	if _, ok := TimeKey(s.Timestamp); !ok {
		t.Errorf("时间戳 %q 不能被 TimeKey 解析", s.Timestamp)
	}
}

func TestDecodePDUMT5700Parts(t *testing.T) {
	cases := []struct {
		name      string
		pdu       string
		seq, tot  int
		ref       int
		contains  []string
		chars     int // UCS2 字符数 = (UDL - UDH 字节) / 2
		number    string
		timestamp string
	}{
		{
			name: "idx 9 ref=0x26 第1段", pdu: mt5700PDUPart3, seq: 1, tot: 3, ref: 0x26,
			contains: []string{"【", "，"}, chars: 45, number: "10000", timestamp: "26/09/29,11:14:05+32",
		},
		{
			name: "idx 10 ref=0x26 第2段", pdu: mt5700PDUPart3c, seq: 2, tot: 3, ref: 0x26,
			contains: []string{"，"}, chars: 45, number: "10000", timestamp: "26/09/29,11:14:05+32",
		},
		{
			name: "idx 8 ref=0x26 第3段", pdu: mt5700PDUPart3b, seq: 3, tot: 3, ref: 0x26,
			contains: []string{"】", "，"}, chars: 45, number: "10000", timestamp: "26/09/29,11:14:05+32",
		},
		{
			name: "idx 0 ref=0x5E 尾段", pdu: mt5700PDUPart2, seq: 2, tot: 2, ref: 0x5E,
			contains: []string{"【", "】"}, chars: 61, number: "10001", timestamp: "25/04/29,10:56:44+32",
		},
		{
			name: "idx 4 ref=0x87 尾包（很短）", pdu: mt5700PDUPart2b, seq: 2, tot: 2, ref: 0x87,
			contains: []string{"！"}, chars: 2,
		},
	}
	for _, c := range cases {
		s, ci, err := smspkg.DecodePDU(c.pdu)
		if err != nil {
			t.Fatalf("%s 解码失败: %v", c.name, err)
		}
		if ci.Total != c.tot || ci.Seq != c.seq || ci.Ref != c.ref {
			t.Errorf("%s 分条信息 = %+v, want ref %#x %d/%d", c.name, ci, c.ref, c.seq, c.tot)
		}
		for _, want := range c.contains {
			if !strings.Contains(s.Content, want) {
				t.Errorf("%s 正文 = %q, 应包含 %q", c.name, s.Content, want)
			}
		}
		if n := len([]rune(s.Content)); c.chars > 0 && n != c.chars {
			t.Errorf("%s 正文长度 = %d 个字符, want %d（=(UDL-UDH)/2，多/少说明 UDH 偏移算错）",
				c.name, n, c.chars)
		}
		// UDH 的字节不能被当正文混进来（UCS2 path 靠 —UDHL 跳过）
		if strings.ContainsRune(s.Content, '\x00') || strings.Contains(s.Content, "\ufffd") {
			t.Errorf("%s 正文里混进了 UDH 字节: %q", c.name, s.Content)
		}
		if c.number != "" && s.Number != c.number {
			t.Errorf("%s 号码 = %q, want %q", c.name, s.Number, c.number)
		}
		if c.timestamp != "" && s.Timestamp != c.timestamp {
			t.Errorf("%s 时间 = %q, want %q", c.name, s.Timestamp, c.timestamp)
		}
	}
}

// TestParsePDUListFM350 广和通 FM350-GL 的 `AT+CMGF=0` + `AT+CMGL=4` 原样应答（实测 2026-09-30）。
//
// 为什么要专门钉住这个机型：它在**文本模式**下回的“正文”其实是十六进制 PDU
// （`AT+CMGF=1` + `AT+CMGL="ALL"` → 头行正常、正文行是 `050003B204015C0A656C…`），
// 按 UCS2 文本去解会把 UDH 解成怪字符 —— 页面表现就是“短信读取不正常”。
// 切 PDU 模式后回的是**完整 PDU**（带 SMSC 头 + UCTS2 正文），下面这两条是原样抓下来的：
// 同一条 4 段长短信的第 1、3 段（模组里只剩这两段，CPMS 报 2/40）。
func TestParsePDUListFM350(t *testing.T) {
	resp := []string{
		`AT+CMGL=4`,
		`+CMGL: 1, 1,, 159`,
		fm350PDUPart1,
		`+CMGL: 3, 1,, 159`,
		fm350PDUPart3,
		`OK`,
	}
	list, concat := smspkg.ParsePDUList(resp)
	if len(list) != 2 {
		t.Fatalf("条目数 = %d, want 2（%+v）", len(list), list)
	}
	if list[0].Index != "1" || list[1].Index != "3" {
		t.Errorf("存储位置 = %q/%q，want 1/3", list[0].Index, list[1].Index)
	}
	if list[0].Status != "1" {
		t.Errorf("状态应取自 +CMGL 头: %q", list[0].Status)
	}
	// 号码/时间必须与文本模式头行报的一致（交叉验证：头行是 +CMGL: 1,"REC READ","106591146189",,"2026/07/17 10:42:45+32"）
	for i, s := range list {
		if s.Number != "106591146189" {
			t.Errorf("第 %d 条号码 = %q, want 106591146189", i+1, s.Number)
		}
		if s.Timestamp != "26/07/17,10:42:45+32" {
			t.Errorf("第 %d 条时间 = %q, want 26/07/17,10:42:45+32", i+1, s.Timestamp)
		}
	}
	// 正文：第 1 段是 UCS2 中文（不能带 UDH 解出来的怪字符）
	if !strings.HasPrefix(list[0].Content, "尊敬的客户") {
		t.Errorf("第 1 段正文应以「尊敬的客户」开头，实际 %q", firstRunes(list[0].Content, 12))
	}
	// 第 3 段开头是 7-bit 时 ASCII（`-assist-remote/url/fjnj`），后面接 UCS2 中文
	if !strings.HasPrefix(list[1].Content, "-assist-remote/url/fjnj") {
		t.Errorf("第 3 段正文应以 -assist-remote/url/fjnj 开头，实际 %q", firstRunes(list[1].Content, 30))
	}
	if !strings.Contains(list[1].Content, "连接Wi-Fi访问") {
		t.Errorf("第 3 段正文里应能解出中文，实际 %q", firstRunes(list[1].Content, 60))
	}
	// 分条信息：同一条长短信（ref=0xB2, 共 4 段），段序 1 / 3
	for _, idx := range []string{"1", "3"} {
		ci, ok := concat[idx]
		if !ok || ci.Ref != 0xB2 || ci.Total != 4 {
			t.Errorf("idx %s 的分条信息 = %+v ok=%v, want ref=0xB2 total=4", idx, ci, ok)
		}
	}
	if concat["1"].Seq != 1 || concat["3"].Seq != 3 {
		t.Errorf("段序 = %d/%d, want 1/3", concat["1"].Seq, concat["3"].Seq)
	}
	// 缺段的合并：只有 1、3 两段时也要能合出一条（内容按段序拼，缺的段留空）
	grouped := smspkg.GroupSMSConcat(list, concat)
	if len(grouped) != 1 {
		t.Fatalf("应合成 1 条，实际 %d 条", len(grouped))
	}
	if !strings.HasPrefix(grouped[0].Content, "尊敬的客户") {
		t.Errorf("合并后应以第 1 段开头，实际 %q", firstRunes(grouped[0].Content, 20))
	}
}

// firstRunes 取前 n 个字符（错误信息里别把整条正文打出来）
func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// FM350-GL 实测抓下来的两条 PDU（`AT+CMGL=4` 原样，各 169 字节）
const (
	fm350PDUPart1 = `099164000339745702F0640CA00156194116980008627071012454238C050003B204015C0A656C76845BA26237FF0C60A8768453F77801957F671F672A4F7F752862164F7F75285F025E38FF0C6839636E300A4E2D534E4EBA6C115171548C56FD53CD75354FE17F517EDC8BC89A976CD5300B76F8517389816C42FF0C4E3A7EF462A460A8768454086CD5674376CAFF0C96326B6253F7536188AB4ED64EBA76D775284EA7751F4E0D`
	fm350PDUPart3 = `099164000339745702F0640CA00156194116980008627071012454238C050003B20403002D006100730073006900730074002D00720065006D006F00740065002F00750072006C002F0066006A006E006A0020FF088FDE63A500570069002D004600698BBF95EEFF09FF1B0032002E673A4E3B672C4EBA63018EAB4EFD8BC1548C624B673A536181F384254E1A53855904740630027ED960A85E2667654E0D4FBF656C8BF78C0589E3`
)

// TestParsePDUListMT5700 整段 `AT+CMGL=4` 应答（含实测的 URC 夹击）→ 列表 + 分条表
func TestParsePDUListMT5700(t *testing.T) {
	resp := []string{
		`AT+CMGL=4`,
		`+CMGL: 2,1,,23`,
		mt5700PDUSingle,
		`+CMGL: 4,1,,27`,
		mt5700PDUPart2b,
		// 实测模组会在长应答里插 URC，不能把配对搞乱
		`^RSSI: 0`,
		`^CERSSI: 0,0,255,0,0,0,3,14,0,0,99,99,99,99,99,99,99,99,-85,-10,17`,
		`^HCSQ: "NR",56,186,30`,
		`OK`,
	}
	list, concat := smspkg.ParsePDUList(resp)
	if len(list) != 2 {
		t.Fatalf("条目数 = %d, want 2（%+v）", len(list), list)
	}
	if list[0].Index != "2" || list[1].Index != "4" {
		t.Errorf("存储位置 = %q/%q", list[0].Index, list[1].Index)
	}
	if list[0].Content != "你好" || list[0].Number != "13520309333" {
		t.Errorf("第 1 条 = %+v", list[0])
	}
	if list[0].Status != "1" {
		t.Errorf("状态应取自 +CMGL 头: %q", list[0].Status)
	}
	// 只有 idx 4 带拼接 UDH → 只它进分条表
	ci, ok := concat["4"]
	if !ok || ci.Ref != 0x87 || ci.Total != 2 || ci.Seq != 2 {
		t.Fatalf("idx 4 的分条信息 = %+v ok=%v", ci, ok)
	}
	if _, ok := concat["2"]; ok {
		t.Error("单条短信不该进分条表")
	}
}

// TestParsePDUListMT5700ConcatAll 三段的同一条长短信（idx 8/9/10 同 ref=0x26）
// 必须能合出 1 条父记录 + 3 段子表，且段序按 seq 排好（存储位置顺序不可信）。
func TestParsePDUListMT5700ConcatAll(t *testing.T) {
	resp := []string{
		`+CMGL: 8,1,,112`, mt5700PDUPart3b,
		`+CMGL: 9,1,,112`, mt5700PDUPart3,
		`+CMGL: 10,1,,112`, mt5700PDUPart3c,
		`OK`,
	}
	list, concat := smspkg.ParsePDUList(resp)
	if len(list) != 3 || len(concat) != 3 {
		t.Fatalf("条目 %d / 分条 %d, want 3/3", len(list), len(concat))
	}
	grouped := smspkg.GroupSMSConcat(list, concat)
	if len(grouped) != 1 {
		t.Fatalf("应合成 1 条，实际 %d 条", len(grouped))
	}
	if grouped[0].PartTotal != 3 || len(grouped[0].Parts) != 3 {
		t.Errorf("父记录 total/parts = %d/%d, want 3/3", grouped[0].PartTotal, len(grouped[0].Parts))
	}
	for i, p := range grouped[0].Parts {
		if p.Seq != i+1 {
			t.Errorf("第 %d 段的 seq = %d，段序没排好", i, p.Seq)
		}
	}
	if !strings.Contains(grouped[0].Content, "【") || !strings.Contains(grouped[0].Content, "】") {
		t.Errorf("合并后的正文 = %q", grouped[0].Content)
	}
}

// TestDecodePDU7Bit GSM 7 位字母表与 UDH 偏移。
//
// 样例是**手工拼的**：SMSC / 发件人 / SCTS 取自仓库里已有的实机 PDU（sms_test.go 的
// pduIdx2），正文用 GSM 03.38 的经典向量 UD=E8329BFD4697D9EC37、UDL=10 → "hellohello"。
func TestDecodePDU7Bit(t *testing.T) {
	smsc := "0891688109520000F0"
	oa := "0BA16192258163F4" // 11 位 + 16295218364（TOA=A1 national，不补 +）
	scts := "52409251837123" // 25/04/29,15:38:17+32
	// FO=04（SMS-DELIVER、无 UDHI）、PID=00、DCS=00（7 位）
	pduPlain := smsc + "04" + oa + "00" + "00" + scts + "0A" + "E8329BFD4697D9EC37"
	s, ci, err := smspkg.DecodePDU(pduPlain)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if s.Content != "hellohello" {
		t.Errorf("7 位正文 = %q, want hellohello", s.Content)
	}
	if s.Number != "16295218364" {
		t.Errorf("号码 = %q, want 16295218364", s.Number)
	}
	if s.Timestamp != "25/04/29,15:38:17+32" {
		t.Errorf("时间 = %q, want 25/04/29,15:38:17+32", s.Timestamp)
	}
	if ci.Total != 0 {
		t.Errorf("没 UDH 不该报分条: %+v", ci)
	}

	// 同样的文本加上 6 字节 UDH（ref=0x99 1/2）→ 解码要跳过 UDH 占掉的 septet。
	// ⚠️ UD = UDH 位流 + 文本 septet（UDH 已经在位流里，不能再单独拼一遍）。
	udh := []byte{0x05, 0x00, 0x03, 0x99, 0x02, 0x01}
	packed, septets := pack7BitWithUDH(udh, "OK")
	pduUDH := smsc + "44" + oa + "00" + "00" + scts + byteHex(byte(septets)) + smspkg.HexString(packed)
	s2, ci2, err := smspkg.DecodePDU(pduUDH)
	if err != nil {
		t.Fatalf("带 UDH 的 7 位解码失败: %v", err)
	}
	if s2.Content != "OK" {
		t.Errorf("带 UDH 的 7 位正文 = %q, want OK", s2.Content)
	}
	if ci2.Ref != 0x99 || ci2.Total != 2 || ci2.Seq != 1 {
		t.Errorf("带 UDH 的分条信息 = %+v, want ref 0x99 1/2", ci2)
	}
}

// pack7BitWithUDH 把 UDH 与文本打包成 7 位流（UDH 后面补位到 septet 边界）。
// ⚠️ 只用于构造测试样例，不参与服务端解码。
func pack7BitWithUDH(udh []byte, text string) ([]byte, int) {
	var bits []byte
	push := func(v byte, n int) {
		for i := 0; i < n; i++ {
			bits = append(bits, (v>>uint(i))&1)
		}
	}
	for _, b := range udh {
		push(b, 8)
	}
	for len(bits)%7 != 0 { // 补位：正文必须从 septet 边界开始
		bits = append(bits, 0)
	}
	septets := len(bits) / 7
	for _, r := range text {
		v := byte(0)
		for i, c := range sms.Gsm7Default {
			if c == r {
				v = byte(i)
				break
			}
		}
		push(v, 7)
		septets++
	}
	out := make([]byte, (len(bits)+7)/8)
	for i, bit := range bits {
		if bit == 1 {
			out[i/8] |= 1 << uint(i%8)
		}
	}
	return out, septets
}

// byteHex 单字节 → 两位数十六进制（UDL）
func byteHex(b byte) string { return smspkg.HexString([]byte{b}) }

// TestSeptetPad UDH 占用的 septet 数（参考值：6 字节 UDH → 7 septets、7 字节 → 8）
func TestSeptetPad(t *testing.T) {
	if got := smspkg.SeptetPad(6); got != 7 {
		t.Errorf("septetPad(6) = %d, want 7", got)
	}
	if got := smspkg.SeptetPad(7); got != 8 {
		t.Errorf("septetPad(7) = %d, want 8", got)
	}
	if got := smspkg.SeptetPad(0); got != 0 {
		t.Errorf("septetPad(0) = %d, want 0", got)
	}
}

// TestDecodePDUBadInput 残缺/异常输入不能 panic、不能编出内容
func TestDecodePDUBadInput(t *testing.T) {
	// 残缺/非法输入：要么报错，要么解出空内容，绝不能 panic
	for _, pdu := range []string{
		"", "0", "ZZ", "0991", "0891688109520000F0240BA1", "07916681006100",
		"FFFFFFFFFFFFFFFFFFFF",
	} {
		if s, _, err := smspkg.DecodePDU(pdu); err == nil && (s.Content != "" || s.Number != "") {
			t.Errorf("%q 不该解出内容：%+v", pdu, s)
		}
	}
	// 非法 hex → 必须报错
	if _, _, err := smspkg.DecodePDU("GGGG"); err == nil {
		t.Error("非法十六进制应报错")
	}
	// 解析失败的单条仍要保留条目（与文本模式一致：有头无正文）
	list, concat := smspkg.ParsePDUList([]string{`+CMGL: 5,1,,20`, "FFFF", "OK"})
	if len(list) != 1 || list[0].Index != "5" || list[0].Content != "" {
		t.Errorf("解码失败的单条应保留空条目：%+v", list)
	}
	if len(concat) != 0 {
		t.Error("解码失败不该留下分条信息")
	}
}

// TestSMSAlphabetByDCS DCS → 字符集
func TestSMSAlphabetByDCS(t *testing.T) {
	cases := map[byte]int{
		0x00: smspkg.AlphabetGSM7, 0x08: smspkg.AlphabetUCS2, 0x04: smspkg.Alphabet8Bit,
		0xF0: smspkg.AlphabetGSM7, 0xF8: smspkg.AlphabetUCS2, 0x11: smspkg.AlphabetGSM7,
	}
	for dcs, want := range cases {
		if got := smspkg.SmsAlphabet(dcs); got != want {
			t.Errorf("smsAlphabet(%#x) = %d, want %d", dcs, got, want)
		}
	}
}

// TestPDUTimeSign 时区字节：量值是 BCD（低位半字节在前），**bit3 是符号位**。
// 实测本机短信里 +32（+8:00）就是 0x23 —— 不是"常见的 0x32"，别被直觉骗了。
func TestPDUTimeSign(t *testing.T) {
	b := []byte{0x62, 0x90, 0x82, 0x22, 0x12, 0x04, 0x23} // 26/09/28,22:21:40+32
	if got := smspkg.PduTime(b); got != "26/09/28,22:21:40+32" {
		t.Errorf("pduTime = %q", got)
	}
	b[6] = 0x2B // 同量值但 bit3 = 1 → -32（-8:00）
	if got := smspkg.PduTime(b); got != "26/09/28,22:21:40-32" {
		t.Errorf("负时区 pduTime = %q, want ...-32", got)
	}
	// 非 BCD（0xAB）→ 整条时间判为未知
	b[6] = 0x23
	b[0] = 0xAB
	if got := smspkg.PduTime(b); got != "" {
		t.Errorf("非 BCD 时间应返回空串，实际 %q", got)
	}
}

// ---- from: sms/sms_test.go ----

func TestSplitCMGLFields(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			// 实测移远格式：alpha 字段为空且不带引号
			name: "空 alpha 不带引号",
			in:   ` 0,"REC UNREAD","10001",,"25/04/29,10:56:44+32"`,
			want: []string{"0", "REC UNREAD", "10001", "", "25/04/29,10:56:44+32"},
		},
		{
			name: "空 alpha 带引号",
			in:   ` 1,"REC UNREAD","+8613800138000","","25/04/23,10:30:00+32"`,
			want: []string{"1", "REC UNREAD", "+8613800138000", "", "25/04/23,10:30:00+32"},
		},
		{
			name: "带别名",
			in:   ` 2,"REC READ","10086","中国移动","25/04/23,10:30:00+32"`,
			want: []string{"2", "REC READ", "10086", "中国移动", "25/04/23,10:30:00+32"},
		},
		{
			name: "引号内逗号不分割",
			in:   `"a,b","c"`,
			want: []string{"a,b", "c"},
		},
	}
	for _, c := range cases {
		got := smspkg.SplitCMGLFields(c.in)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}

func TestParseCMGLHeader(t *testing.T) {
	// 实测格式（空 alpha 不带引号）—— 旧的固定正则会在这里失配
	sms, ok := smspkg.ParseCMGLHeader(`+CMGL: 0,"REC UNREAD","10001",,"25/04/29,10:56:44+32"`)
	if !ok {
		t.Fatal("空 alpha（不带引号）的 +CMGL 行应能解析")
	}
	if sms.Index != "0" || sms.Status != "REC UNREAD" || sms.Number != "10001" {
		t.Fatalf("header wrong: %+v", sms)
	}
	// 时间戳必须是第 5 个字段，而不是 alpha
	if sms.Timestamp != "25/04/29,10:56:44+32" {
		t.Fatalf("timestamp wrong: %q", sms.Timestamp)
	}

	// 带引号空 alpha 的格式也要兼容
	sms2, ok := smspkg.ParseCMGLHeader(`+CMGL: 1,"REC READ","+8613800138000","","25/04/23,10:30:00+32"`)
	if !ok || sms2.Timestamp != "25/04/23,10:30:00+32" {
		t.Fatalf("quoted-alpha format wrong: %+v ok=%v", sms2, ok)
	}

	if _, ok := smspkg.ParseCMGLHeader("OK"); ok {
		t.Fatal("OK 不是 +CMGL 行")
	}
	if _, ok := smspkg.ParseCMGLHeader("+CMGL:"); ok {
		t.Fatal("空 +CMGL 行不应解析")
	}
}

// ====================== UCS2 内容解码 ======================

func TestDecodeSMSText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"中文 UCS2", "4F60597D", "你好"},
		{"中文短信实测内容", "002E003100380039002E0063006E002F007900590045004500720041002067E5770B8BE660C5", ".189.cn/yYEErA 查看详情"},
		{"短中文", "6C34FF01", "水！"},
		// 以下是"看起来像十六进制、其实是普通文本"的情况，必须原样保留
		{"8 位数字验证码不解码", "12345678", "12345678"},
		{"数字串高字节非法不解码", "34567890", "34567890"},
		{"数字串含非法高字节不解码", "87654321", "87654321"},
		{"普通英文不解码", "hello from mom", "hello from mom"},
		{"十六进制长度不整不解码", "123456", "123456"},
		{"纯 ASCII 的 UCS2 不误判（无中日韩字符）", "00480065006C006C006F", "00480065006C006C006F"},
	}
	for _, c := range cases {
		if got := smspkg.DecodeSMSText(c.in); got != c.want {
			t.Errorf("%s: DecodeSMSText(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// ====================== 完整应答解析 ======================

// TestParseSMSListRealHardware 用实机抓到的应答验证解析（含空 alpha 与 UCS2 内容）
func TestParseSMSListRealHardware(t *testing.T) {
	resp := []string{
		`AT+CMGL="ALL"`,
		`+CMGL: 0,"REC UNREAD","10001",,"25/04/29,10:56:44+32"`,
		`002E003100380039002E0063006E002F007900590045004500720041002067E5770B8BE660C5`,
		`+CMGL: 4,"REC UNREAD","02510001",,"25/05/14,09:06:59+32"`,
		`6C34FF01`,
		`+CMGL: 8,"REC UNREAD","17071260394",,"25/05/14,19:45:58+32"`,
		`hello`,
		"OK",
	}
	list := smspkg.ParseSMSList(resp)
	if len(list) != 3 {
		t.Fatalf("expected 3 sms, got %d: %+v", len(list), list)
	}

	if list[0].Index != "0" || list[0].Number != "10001" {
		t.Fatalf("sms[0] header wrong: %+v", list[0])
	}
	if list[0].Timestamp != "25/04/29,10:56:44+32" {
		t.Fatalf("sms[0] timestamp wrong: %q", list[0].Timestamp)
	}
	if list[0].Content != ".189.cn/yYEErA 查看详情" {
		t.Fatalf("sms[0] content wrong: %q", list[0].Content)
	}
	if list[1].Content != "水！" {
		t.Fatalf("sms[1] content wrong: %q", list[1].Content)
	}
	if list[2].Content != "hello" {
		t.Fatalf("sms[2] content wrong: %q", list[2].Content)
	}
}

// TestParseSMSListMultiLine 正文含换行时，多行内容都要累加到同一条短信
func TestParseSMSListMultiLine(t *testing.T) {
	resp := []string{
		`+CMGL: 0,"REC UNREAD","10001",,"25/04/29,10:56:44+32"`,
		"line one",
		"line two",
		"OK",
	}
	list := smspkg.ParseSMSList(resp)
	if len(list) != 1 {
		t.Fatalf("expected 1 sms, got %d", len(list))
	}
	if list[0].Content != "line one\nline two" {
		t.Fatalf("multi-line content wrong: %q", list[0].Content)
	}
}

func TestParseSMSListEmpty(t *testing.T) {
	if list := smspkg.ParseSMSList([]string{"OK"}); len(list) != 0 {
		t.Fatalf("空应答应得到 0 条，实际 %d", len(list))
	}
	// 模组报错时不应产生脏数据
	if list := smspkg.ParseSMSList([]string{"ERROR"}); len(list) != 0 {
		t.Fatalf("ERROR 应答应得到 0 条，实际 %d", len(list))
	}
	// PDU 模式的 +CMGL（如 +CMGL: 0,0,,38）字段数不足，不应崩溃
	_ = smspkg.ParseSMSList([]string{`+CMGL: 0,0,,38`, "07916681006100"})
}

// ====================== 长短信（分条）解析 ======================
//
// 以下 PDU 全部来自实机（RM502Q-GL，fw RM502QGLAAR11A02M4G）：
// AT+CMGF=0 后 AT+CMGL=4 的应答，截取到 UDH 为止。
// 注意 UDH 里的拼接 IE 长这样：05 00 03 <ref> <total> <seq>。
const (
	pduIdx0 = "099164000339745702F06405A00100F1000852409201654423800500035E0202002E00" // ref=0x5E 第2段
	pduIdx1 = "099164000339745702F06405A00100F1000852409201654423800500035E020130104F" // ref=0x5E 第1段
	pduIdx2 = "0891688109520000F0040BA16192258163F40008524092518371233C006A0020002000" // 单条，无 UDH
	pduIdx3 = "099164000339745702F04408A1201500100008525041906095238C0500038702016C5B" // ref=0x87 第1段
	pduIdx4 = "099164000339745702F04408A1201500100008525041906095230A0500038702026C34" // ref=0x87 第2段
	pduIdx5 = "099164000339745702F06405A00100F1000852504101236123720500034C0303002E00" // ref=0x4C 第3段
	pduIdx6 = "099164000339745702F06405A00100F1000852504101236123760500034C030254088B" // ref=0x4C 第2段
	pduIdx7 = "099164000339745702F06405A00100F1000852504101236123760500034C030130108D" // ref=0x4C 第1段
	pduIdx8 = "0891688109520000F0240BA17170210693F40008525041915485233400320030003100" // 单条，无 UDH
)

// TestParsePDUConcatList 用实机 PDU 验证 UDH 拼接信息解析。
//
// 这里最关键的结论是：存储位置顺序**不能**代表段序 ——
// ref=0x5E 是「位置1=第1段、位置0=第2段」(逆序)，
// ref=0x87 是「位置3=第1段、位置4=第2段」(正序)，
// ref=0x4C 是「位置7/6/5 = 第1/2/3段」(逆序)。
func TestParsePDUConcatList(t *testing.T) {
	resp := []string{
		"AT+CMGL=4",
		`+CMGL: 0,1,,144`, pduIdx0,
		`+CMGL: 1,1,,144`, pduIdx1,
		`+CMGL: 2,1,,79`, pduIdx2,
		`+CMGL: 3,1,,157`, pduIdx3,
		`+CMGL: 4,1,,27`, pduIdx4,
		`+CMGL: 5,1,,130`, pduIdx5,
		`+CMGL: 6,1,,134`, pduIdx6,
		`+CMGL: 7,1,,134`, pduIdx7,
		`+CMGL: 8,1,,71`, pduIdx8,
		"OK",
	}
	got := smspkg.ParsePDUConcatList(resp)
	want := map[string]smspkg.ConcatInfo{
		"0": {Ref: 0x5E, Total: 2, Seq: 2},
		"1": {Ref: 0x5E, Total: 2, Seq: 1},
		"3": {Ref: 0x87, Total: 2, Seq: 1},
		"4": {Ref: 0x87, Total: 2, Seq: 2},
		"5": {Ref: 0x4C, Total: 3, Seq: 3},
		"6": {Ref: 0x4C, Total: 3, Seq: 2},
		"7": {Ref: 0x4C, Total: 3, Seq: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("带拼接信息的条数 = %d, want %d (%+v)", len(got), len(want), got)
	}
	for idx, w := range want {
		if got[idx] != w {
			t.Errorf("位置 %s: got %+v, want %+v", idx, got[idx], w)
		}
	}
	// 单条短信（TP-UDHI=0）不应被当成分条
	for _, idx := range []string{"2", "8"} {
		if ci, ok := got[idx]; ok {
			t.Errorf("位置 %s 是单条短信，不应有拼接信息：%+v", idx, ci)
		}
	}
}

// TestParseConcatUDHBadInput 残缺/异常 PDU 不能 panic，也不能误判
func TestParseConcatUDHBadInput(t *testing.T) {
	for _, s := range []string{
		"", "0", "0991", pduIdx0[:40], "ZZZZ", "07916681006100",
	} {
		if b, ok := smspkg.HexToBytes(s); ok {
			if ci, ok := smspkg.ParseConcatUDH(b); ok {
				t.Errorf("输入 %q 不应解析出拼接信息：%+v", s, ci)
			}
		}
	}
}

// TestGroupSMSConcat 分条短信应合成「父条目 + 子表」，并按段序拼正文。
func TestGroupSMSConcat(t *testing.T) {
	concat := map[string]smspkg.ConcatInfo{
		"0": {Ref: 0x5E, Total: 2, Seq: 2},
		"1": {Ref: 0x5E, Total: 2, Seq: 1},
		"7": {Ref: 0x4C, Total: 3, Seq: 1},
		"6": {Ref: 0x4C, Total: 3, Seq: 2},
		"5": {Ref: 0x4C, Total: 3, Seq: 3},
	}
	list := []smspkg.SMS{
		{Index: "0", Status: "REC READ", Number: "10001", Timestamp: "T", Content: "二"},
		{Index: "1", Status: "REC READ", Number: "10001", Timestamp: "T", Content: "一"},
		{Index: "2", Status: "REC READ", Number: "16295218364", Timestamp: "T2", Content: "单条"},
		{Index: "5", Status: "REC READ", Number: "10001", Timestamp: "T3", Content: "丙"},
		{Index: "6", Status: "REC READ", Number: "10001", Timestamp: "T3", Content: "乙"},
		{Index: "7", Status: "REC READ", Number: "10001", Timestamp: "T3", Content: "甲"},
	}
	got := smspkg.GroupSMSConcat(list, concat)
	if len(got) != 3 {
		t.Fatalf("分组后应有 3 条，实际 %d：%+v", len(got), got)
	}

	// 第 1 组：ref=0x5E（位置 1 是第 1 段，位置 0 是第 2 段 → 逆序存放）
	g := got[0]
	if len(g.Parts) != 2 {
		t.Fatalf("组 0 应有 2 段，实际 %d", len(g.Parts))
	}
	if g.Content != "一二" {
		t.Errorf("组 0 正文应按段序拼接，got %q want %q", g.Content, "一二")
	}
	if g.Index != "1" || g.Status != "REC READ" {
		t.Errorf("组 0 父母位置/状态应取第 1 段，got index=%q status=%q", g.Index, g.Status)
	}
	if g.PartRef != 0x5E || g.PartTotal != 2 || g.PartSeq != 1 {
		t.Errorf("组 0 分条信息错误：%+v", g)
	}
	if g.Parts[0].Index != "1" || g.Parts[0].Seq != 1 || g.Parts[1].Index != "0" || g.Parts[1].Seq != 2 {
		t.Errorf("组 0 子表顺序错误：%+v", g.Parts)
	}

	// 单条短信保持原样
	if got[1].Index != "2" || len(got[1].Parts) != 0 || got[1].Content != "单条" {
		t.Errorf("单条短信不应被改动：%+v", got[1])
	}

	// 第 3 组：ref=0x4C，三段的存放顺序是 7/6/5 → 1/2/3
	g3 := got[2]
	if g3.Content != "甲乙丙" {
		t.Errorf("组 2 正文应按段序拼接，got %q want %q", g3.Content, "甲乙丙")
	}
	if g3.PartTotal != 3 || len(g3.Parts) != 3 || g3.Index != "7" {
		t.Errorf("组 2 分条信息错误：%+v", g3)
	}
	for i, wantIdx := range []string{"7", "6", "5"} {
		if g3.Parts[i].Index != wantIdx || g3.Parts[i].Seq != i+1 {
			t.Errorf("组 2 第 %d 段应为位置 %s，实际 %+v", i+1, wantIdx, g3.Parts[i])
		}
	}
}

// TestGroupSMSConcatEdgeCases 分组的边界行为
func TestGroupSMSConcatEdgeCases(t *testing.T) {
	single := []smspkg.SMS{{Index: "0", Number: "10001", Content: "x"}}
	// 拿不到分条信息时（PDU 读取失败/固件不支持）应原样返回，退化为旧版平铺列表
	if got := smspkg.GroupSMSConcat(single, nil); len(got) != 1 || len(got[0].Parts) != 0 {
		t.Errorf("concat 为空时应原样返回：%+v", got)
	}

	// 只有 1 段（其余段缺失）不成组，但要带出段号供 UI 提示
	partial := []smspkg.SMS{{Index: "0", Number: "10001", Content: "只有一段"}}
	got := smspkg.GroupSMSConcat(partial, map[string]smspkg.ConcatInfo{"0": {Ref: 1, Total: 3, Seq: 1}})
	if len(got) != 1 || len(got[0].Parts) != 0 {
		t.Fatalf("残缺长短信不应成组：%+v", got)
	}
	if got[0].PartTotal != 3 || got[0].PartSeq != 1 {
		t.Errorf("残缺长短信应带出段号：%+v", got[0])
	}

	// 引用号是 8 位，同一发件人不同时间的长短信会撞号：
	// 段号重复时另起一组，不能把两条长短信混在一起
	reuse := []smspkg.SMS{
		{Index: "0", Number: "10001", Content: "首条一"},
		{Index: "1", Number: "10001", Content: "首条二"},
		{Index: "2", Number: "10001", Content: "次条一"},
		{Index: "3", Number: "10001", Content: "次条二"},
	}
	got2 := smspkg.GroupSMSConcat(reuse, map[string]smspkg.ConcatInfo{
		"0": {Ref: 9, Total: 2, Seq: 1},
		"1": {Ref: 9, Total: 2, Seq: 2},
		"2": {Ref: 9, Total: 2, Seq: 1},
		"3": {Ref: 9, Total: 2, Seq: 2},
	})
	if len(got2) != 2 {
		t.Fatalf("撞号应分成 2 组，实际 %d：%+v", len(got2), got2)
	}
	if got2[0].Content != "首条一首条二" || got2[1].Content != "次条一次条二" {
		t.Errorf("撞号分组内容错误：%q / %q", got2[0].Content, got2[1].Content)
	}

	// 段号超出总段数的脏数据按普通短信处理
	bad := []smspkg.SMS{{Index: "0", Number: "10001", Content: "脏"}}
	if got3 := smspkg.GroupSMSConcat(bad, map[string]smspkg.ConcatInfo{"0": {Ref: 1, Total: 2, Seq: 5}}); len(got3[0].Parts) != 0 {
		t.Errorf("非法段号不应成组：%+v", got3)
	}
}

// ====================== 按时间排序 ======================

// TestSMSTimeKey 时间戳解析（3GPP 格式 YY/MM/DD,HH:MM:SS±ZZ）
func TestSMSTimeKey(t *testing.T) {
	valid := []string{
		"25/04/29,10:56:44+32", // 实机
		"25/05/14,09:06:59+32",
		"26/09/28,22:21:40+32",
		"25/05/14,19:45:58", // 不带时区
	}
	for _, ts := range valid {
		if _, ok := TimeKey(ts); !ok {
			t.Errorf("TimeKey(%q) 应可解析", ts)
		}
	}
	invalid := []string{
		"", "garbage", "25/05/14", "00/00/00,00:00:00+00",
		"25/13/01,00:00:00+32", "25/05/14,25:00:00+32", "25/05/14,10:60:00+32",
	}
	for _, ts := range invalid {
		if _, ok := TimeKey(ts); ok {
			t.Errorf("TimeKey(%q) 应判为无效", ts)
		}
	}

	// 严格递增：时间是可比较的
	ordered := []string{
		"25/04/29,10:56:44+32", // 2025-04-29
		"25/05/14,09:06:59+32", // 2025-05-14 早上
		"25/05/14,10:32:16+32", // 同日稍晚
		"25/05/14,19:45:58+32", // 同日更晚
		"26/09/28,22:21:40+32", // 2026 年
	}
	for i := 1; i < len(ordered); i++ {
		a, _ := TimeKey(ordered[i-1])
		b, _ := TimeKey(ordered[i])
		if !(a < b) {
			t.Errorf("%q 应早于 %q（%d vs %d）", ordered[i-1], ordered[i], a, b)
		}
	}
}

// TestSortSMSNewestFirst 实机数据：存储位置顺序（1,2,3,7,8）并非时间顺序，
// 且列表要按时间倒序 —— 最新的排最上面。
//
// 位置 2 是后来收到的新短信（26/09/28），占用了之前被删除记录空出的槽位。
func TestSortSMSNewestFirst(t *testing.T) {
	// 模组返回顺序 = 存储位置顺序
	list := []smspkg.SMS{
		{Index: "1", Timestamp: "25/04/29,10:56:44+32", Content: "A"},
		{Index: "2", Timestamp: "26/09/28,22:21:40+32", Content: "B"}, // 最新
		{Index: "3", Timestamp: "25/05/14,09:06:59+32", Content: "C"},
		{Index: "7", Timestamp: "25/05/14,10:32:16+32", Content: "D"},
		{Index: "8", Timestamp: "25/05/14,19:45:58+32", Content: "E"},
	}
	got := SortSMSNewestFirst(list)
	want := []string{"2", "8", "7", "3", "1"}
	if len(got) != len(want) {
		t.Fatalf("长度变了：%d", len(got))
	}
	for i, idx := range want {
		if got[i].Index != idx {
			t.Fatalf("第 %d 位应为位置 %s，实际 %s（顺序 %+v）", i, idx, got[i].Index, got)
		}
	}
}

// TestSortSMSUnknownTs 时间不可解析的条目排在最后，且保持原有相对顺序
func TestSortSMSUnknownTs(t *testing.T) {
	list := []smspkg.SMS{
		{Index: "0", Timestamp: ""},
		{Index: "1", Timestamp: "25/05/14,10:00:00+32"},
		{Index: "2", Timestamp: "garbage"},
		{Index: "3", Timestamp: "25/04/29,10:00:00+32"},
	}
	got := SortSMSNewestFirst(list)
	want := []string{"1", "3", "0", "2"} // 有效按时间倒序 → 无法解析的按原顺序垫底
	for i, idx := range want {
		if got[i].Index != idx {
			t.Fatalf("第 %d 位应为 %s，实际 %s（%+v）", i, idx, got[i].Index, got)
		}
	}

	// 空列表与全无效时间不应 panic
	if out := SortSMSNewestFirst(nil); len(out) != 0 {
		t.Error("nil 应返回空")
	}
	allBad := SortSMSNewestFirst([]smspkg.SMS{{Index: "0"}, {Index: "1"}})
	if allBad[0].Index != "0" || allBad[1].Index != "1" {
		t.Errorf("全无效时间时应保持原顺序：%+v", allBad)
	}
}

// ---- from: traffic/traffic_test.go ----

func TestParseCounters(t *testing.T) {
	cases := []struct {
		resp []string
		sent uint64
		recv uint64
		ok   bool
	}{
		{[]string{"+QGDCNT: 1234,5678", "OK"}, 1234, 5678, true},
		{[]string{"+QGDCNT: 0,0"}, 0, 0, true},
		{[]string{"+QGDCNT:4294967296,1"}, 4294967296, 1, true},
		{[]string{"OK"}, 0, 0, false},
		{[]string{"ERROR"}, 0, 0, false},
		{[]string{"+QGDCNT: abc,def"}, 0, 0, false},
		{nil, 0, 0, false},
	}
	for _, c := range cases {
		sent, recv, ok := ParseCounters(c.resp)
		if ok != c.ok || (ok && (sent != c.sent || recv != c.recv)) {
			t.Errorf("ParseCounters(%v) = (%d,%d,%v), want (%d,%d,%v)",
				c.resp, sent, recv, ok, c.sent, c.recv, c.ok)
		}
	}
}

// 美格 SRM：AT^DSFLOWQRY → `^DSFLOWRPT: <上行 B/s>,<下行 B/s>,…`（vendor_meig.sh 的 tx_rate/rx_rate）
func TestParseDSFlowRpt(t *testing.T) {
	up, down, ok := ParseDSFlowRpt([]string{"^DSFLOWRPT: 12345,6789", "OK"})
	if !ok || up != 12345 || down != 6789 {
		t.Errorf("ParseDSFlowRpt = (%v,%v,%v)，want (12345,6789,true)", up, down, ok)
	}
	// 少一格 / 非数字 / 别的厂商前缀都不能认
	for _, resp := range [][]string{
		{"^DSFLOWRPT: 12345"},
		{"^DSFLOWRPT: a,b"},
		{"^DSFLOWQRY: 1,2,3,4,5,6"}, // 鼎桥那套（计数式）不能被当速率
		{"OK"},
		nil,
	} {
		if _, _, ok := ParseDSFlowRpt(resp); ok {
			t.Errorf("%v 不该解析成美格速率", resp)
		}
	}
}

// 鼎桥 MT5700：AT^DSFLOWQRY 回 6 个十六进制字段，累计量在第 5/6 格
func TestParseHexCounters(t *testing.T) {
	cases := []struct {
		resp []string
		sent uint64
		recv uint64
		ok   bool
	}{
		// 实机样本：0x19A5 = 6565、0x1666 = 5734
		{[]string{"^DSFLOWQRY: 000002A1,00000000000019A5,0000000000001666,000002A1,00000000000019A5,0000000000001666", "OK"},
			6565, 5734, true},
		{[]string{"^dsflowqry: 1,2,3,4,FFFFFFFF,10"}, 4294967295, 16, true},
		{[]string{"OK"}, 0, 0, false},
		{[]string{"^DSFLOWQRY: 1,2,3,4"}, 0, 0, false},
		{nil, 0, 0, false},
	}
	for _, c := range cases {
		sent, recv, ok := ParseHexCounters(c.resp)
		if ok != c.ok || (ok && (sent != c.sent || recv != c.recv)) {
			t.Errorf("ParseHexCounters(%v) = (%d,%d,%v), want (%d,%d,%v)",
				c.resp, sent, recv, ok, c.sent, c.recv, c.ok)
		}
	}
}

// ParseTraffic 按应答形状分流：GTSTATIS / DSFLOWRPT → 速率式，QGDCNT / DSFLOWQRY → 计数式
func TestParseShape(t *testing.T) {
	if s, ok := ParseTraffic([]string{"+GTSTATIS: 2048,1024,1,2"}); !ok || !s.RateMode || s.UpBps != 1024 || s.DownBps != 2048 {
		t.Errorf("速率式分流错误: %+v ok=%v", s, ok)
	}
	// 美格：同样是速率式，但走 ^DSFLOWRPT（f1=上行、f2=下行）
	if s, ok := ParseTraffic([]string{"^DSFLOWRPT: 1024,2048,7,8"}); !ok || !s.RateMode || s.UpBps != 1024 || s.DownBps != 2048 {
		t.Errorf("美格速率式分流错误: %+v ok=%v", s, ok)
	}
	if s, ok := ParseTraffic([]string{"+QGDCNT: 10,20"}); !ok || s.RateMode || s.Sent != 10 || s.Recv != 20 {
		t.Errorf("计数式分流错误: %+v ok=%v", s, ok)
	}
	if s, ok := ParseTraffic([]string{"^DSFLOWQRY: 1,2,3,4,5,6"}); !ok || s.RateMode || s.Sent != 5 || s.Recv != 6 {
		t.Errorf("十六进制计数式分流错误: %+v ok=%v", s, ok)
	}
	if _, ok := ParseTraffic([]string{"OK"}); ok {
		t.Error("无数据应答不应算采样成功")
	}
}

func TestFormatRate(t *testing.T) {
	cases := []struct {
		bps  float64
		want string
	}{
		{0, "0 B/s"},
		{-1, "0 B/s"},
		{512, "512 B/s"},
		{1024, "1.0 KB/s"},
		{15360, "15.0 KB/s"},
		{1024 * 1024, "1.00 MB/s"},
		{3.5 * 1024 * 1024, "3.50 MB/s"},
	}
	for _, c := range cases {
		if got := FormatRate(c.bps); got != c.want {
			t.Errorf("FormatRate(%v) = %q, want %q", c.bps, got, c.want)
		}
	}
}

// ====================== 瞬时速率式（广和通 AT+GTSTATIS?） ======================

// TestParseRates 字段口径依官方集成脚本 fibocom.sh：$1=下载、$2=上传（Byte/s）
func TestParseRates(t *testing.T) {
	cases := []struct {
		resp     []string
		up, down float64
		ok       bool
	}{
		// 实机样本（本机数据面空闲，两格为 0；后两格官方脚本未使用）
		{[]string{"+GTSTATIS: 0,0,0,0", "OK"}, 0, 0, true},
		{[]string{"+GTSTATIS: 0,0,1063,1660", "OK"}, 0, 0, true},
		// 非零样本：$1=下载 2048、$2=上传 1024
		{[]string{"+GTSTATIS: 2048,1024,1,2"}, 1024, 2048, true},
		{[]string{"+GTSTATIS:0,0"}, 0, 0, true},
		{[]string{"OK"}, 0, 0, false},
		{[]string{"ERROR"}, 0, 0, false},
		// 不足两格的残缺应答不当成有效样本
		{[]string{"+GTSTATIS: 1"}, 0, 0, false},
		{nil, 0, 0, false},
	}
	for _, c := range cases {
		up, down, ok := ParseRates(c.resp)
		if ok != c.ok || (ok && (up != c.up || down != c.down)) {
			t.Errorf("ParseRates(%v) = (%.0f,%.0f,%v), want (%.0f,%.0f,%v)",
				c.resp, up, down, ok, c.up, c.down, c.ok)
		}
	}
}

// ---- from: ussd/ussd_test.go ----

func TestParseCUSD(t *testing.T) {
	// 英文/数字应答：原样返回
	res, ok := ParseCUSD(`+CUSD: 0,"Your balance is 10.00 yuan",15`, time.Now())
	if !ok || res.Status != 0 || res.Text != "Your balance is 10.00 yuan" {
		t.Fatalf("ASCII 应答解析错误: %+v ok=%v", res, ok)
	}
	// 中文应答：DCS=72(UCS2) 时是 UTF-16BE 十六进制串，要还原
	// 4E2D 56FD 7535 4FE1 = “中国电信”。
	// 期望值用码点拼而不是直接写中文字面量：这样断言与源文件编码无关。
	wantCJK := string([]rune{0x4E2D, 0x56FD, 0x7535, 0x4FE1})
	res, ok = ParseCUSD(`+CUSD: 0,"4E2D56FD75354FE1",72`, time.Now())
	if !ok || res.Text != wantCJK {
		t.Fatalf("UCS2 应答未还原: ok=%v text=%q want=%q", ok, res.Text, wantCJK)
	}
	if atparse.StatusText(0) == "" || atparse.StatusText(1) == "" || atparse.StatusText(2) == "" ||
		atparse.StatusText(5) == "" || atparse.StatusText(9) == "" {
		t.Error("USSD 状态文案不应为空")
	}
	if atparse.StatusText(0) == atparse.StatusText(5) {
		t.Error("不同状态码应有不同文案")
	}
	// 没有文本的应答（+CUSD: 2 = 会话结束）
	res, ok = ParseCUSD("+CUSD: 2", time.Now())
	if !ok || res.Status != 2 || res.Text != "" {
		t.Fatalf("无文本应答解析错误: %+v", res)
	}
	if _, ok := ParseCUSD("ERROR", time.Now()); ok {
		t.Error("ERROR 不该解析成功")
	}
	if _, ok := ParseCUSD(`+CMTI: "SM",3`, time.Now()); ok {
		t.Error("其它 URC 不该被当成 USSD")
	}
}

// USSD 被网络拒时，展锐固件会先报一条 `+SPERROR`、紧接着 `+CUSD: 2`（无正文）——
// 把这条线索附到结果上，界面才能说清"是网络侧拒的"（实测 F50/电信数据卡发 *100#）。
func TestAttachSPError(t *testing.T) {
	now := time.Now()
	res := Result{At: now, Status: 2, Raw: "+CUSD: 2"}
	if !AttachSPError(&res, `+SPERROR: 5,41,"46011"`, now.Add(-2*time.Second), now) {
		t.Fatal("刚发生的 +SPERROR 应该附上")
	}
	if !strings.Contains(res.SPError, "46011") {
		t.Errorf("SPError = %q，应带上原文", res.SPError)
	}
	// 过期的不附：上一次尝试的线索不能拿来解释这一次
	res2 := Result{Status: 2}
	if AttachSPError(&res2, "+SPERROR: 5,41", now.Add(-2*time.Minute), now) {
		t.Error("过期 2 分钟的 +SPERROR 不该附")
	}
	if res2.SPError != "" {
		t.Errorf("过期线索不该被写入：%q", res2.SPError)
	}
	// nil / 空行 / 零时间都不能崩
	if AttachSPError(nil, "+SPERROR: 1", now, now) ||
		AttachSPError(&Result{}, "", now, now) ||
		AttachSPError(&Result{}, "+SPERROR: 1", time.Time{}, now) {
		t.Error("nil / 空行 / 零时间都应返回 false")
	}
}

// USSD 码校验：只允许数字与 * # +（拼进 AT 指令的参数必须严格限制）
func TestValidCode(t *testing.T) {
	for _, ok := range []string{"*100#", "*123*1#", "#100#", "+8613800138000"} {
		if !atparse.ValidCode(ok) {
			t.Errorf("%q 应被接受", ok)
		}
	}
	for _, bad := range []string{"", "abc", `*100#"`, "1,2", "*100#;", "  "} {
		if atparse.ValidCode(bad) {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
	if atparse.ValidCode(string(make([]byte, 65))) {
		t.Error("超过 64 位应被拒绝")
	}
}
