package kernel

// 中兴 F50（展锐 UMS9620）"已取证"能力的回归测试。
//
// 每一条的**依据都是 2026-10-05 在该机上的只读实测**（记录见 atcmd.go 里
// UnisocCommand 各方法的注释与 `d:\Go\MD\F50-UMS9620-modem.md`）。
// 这些测试要钉住两件事：
//  1. 已经能用的功能不许悄悄退化（APN / USSD / 组网模式读）；
//  2. 明明做不到的功能不许"假装能做"（小区信息、流量计数器）——
//     那种假装会在共享慢总线上白发一堆注定失败的指令。

import (
	"errors"
	"modemd/module/common"
	"modemd/module/unisoc"
	"strings"
	"testing"
	"time"
)

// 空指令（厂商用空串声明"本机不提供该能力"）必须在 SendAT 入口被拦住。
//
// 放它过去的话会真往通道里发一条空指令：共享慢总线上白占一个往返（2 秒），
// 而且超时报的是 `AT command timeout: `（冒号后面什么都没有），现场根本看不懂。
// 实测触发点：展锐的 CommandNetInfo/CommandServingCell 返回空串。
func TestSendATRejectsEmptyCommand(t *testing.T) {
	mod := &Modem{writeChan: make(chan string, 4), readChan: make(chan string, 4)}
	for _, c := range []string{"", "   ", "\t"} {
		if _, err := mod.SendAT(c); !errors.Is(err, errATNoCommand) {
			t.Errorf("SendAT(%q) 应当直接报 errATNoCommand，实际 %v", c, err)
		}
		if _, err := mod.SendATTimeout(c, time.Second); !errors.Is(err, errATNoCommand) {
			t.Errorf("SendATTimeout(%q) 应当直接报 errATNoCommand，实际 %v", c, err)
		}
	}
	select {
	case got := <-mod.writeChan:
		t.Fatalf("空指令不该被写进 writeChan，却收到了 %q", got)
	default:
	}
}

// 真机原始应答（照抄，不要"净化"）
const (
	// AT+CGCONTRDP=1
	f50Cgcontrdp1 = `+CGCONTRDP: 1,6,ctnet,10.194.190.206.255.0.0.0,,218.2.2.2,218.4.4.4,,,,,0,,0,,,,0,2,1,0,1.0,0,0,0`
	// AT+CGPADDR（交叉验证用：cid 1 的 IPv4 必须与上面一致）
	f50Cgpaddr = `+CGPADDR: 1,10.194.190.206,0.0.0.0.0.0.0.0.24.219.155.235.140.225.1.146`
	// AT+SPRAT?
	f50Sprat = `+SPRAT: LTE 32`
	// AT+COPS=?（同一份应答里 HEX 形态与 ASCII 形态同时出现 —— 实机就是这样）
	f50Cops = `+COPS: (2,"4E2D56FD75354FE1","4E2D56FD75354FE1","46011",11),` +
		`(3,"CHINA MOBILE","CMCC","46000",7),,(0-1,2)`
)

// ---- 1. APN：展锐只认带 cid 的 CGCONTRDP ----

func TestUnisocAPNCandidates(t *testing.T) {
	impl := NewVendorCommand(VendorUnisoc)
	p, ok := impl.(APNCandidatesProvider)
	if !ok {
		t.Fatal("展锐必须声明 APNCandidatesProvider（不带 cid 的 CGCONTRDP 在本机回 +CME ERROR: 65536014）")
	}
	cands := p.APNCandidates()
	if len(cands) == 0 || cands[0] != "AT+CGCONTRDP=1" {
		t.Fatalf("首选必须是带 cid 的写法，实际 %v", cands)
	}

	// 真的会被用上：resolveVendorCmd 取候选表时走的就是它
	mod := &Modem{cmdImpl: impl}
	got := mod.vendorCandidates(KindAPN)
	if len(got) == 0 || got[0] != "AT+CGCONTRDP=1" {
		t.Fatalf("vendorCandidates(KindAPN) = %v，首选应为 AT+CGCONTRDP=1", got)
	}

	// ⚠️ 其它厂商**一个字都不能变**（默认那两条）
	for _, v := range []ModemVendor{VendorUnknown, VendorQuectel, VendorFibocom, VendorSimcom, VendorMeig, VendorTDTech} {
		other := (&Modem{cmdImpl: NewVendorCommand(v)}).vendorCandidates(KindAPN)
		if len(other) != 2 || other[0] != common.CmdCGCONTRDP || other[1] != "AT+CGDCONT?" {
			t.Errorf("厂商 %s 的 APN 候选被改动了：%v", v, other)
		}
	}
}

// 真机那一行要能解出 APN 与本机 IP（3GPP 的"地址+掩码"拼写方式）
func TestParseRespF50Cgcontrdp(t *testing.T) {
	name, ip := ParseResp("AT+CGCONTRDP=1", []string{f50Cgcontrdp1, "OK"})
	if name != "ctnet" {
		t.Errorf("APN 应为 ctnet，实际 %q", name)
	}
	if ip != "10.194.190.206" {
		t.Errorf("本机 IP 应为 10.194.190.206，实际 %q", ip)
	}
	// 交叉验证：同一个 cid 的地址在 `AT+CGPADDR` 的应答里也是同一个值
	//（两条不同指令互相印证，说明地址段的切分是对的）。
	// ⚠️ 不走 ParseResp —— 它只认 CGCONTRDP / CGDCONT 两种格式，
	// CGPADDR 有自己的解析器，混着测只会测到一个假的结论。
	if !strings.Contains(f50Cgpaddr, ip) {
		t.Errorf("CGPADDR 的应答里也该出现同一个地址 %q：%q", ip, f50Cgpaddr)
	}
}

func TestCgcontrdpLocalAddr(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"10.194.190.206.255.0.0.0", "10.194.190.206"}, // 标准：地址 + 掩码
		{"192.168.1.5.255.255.255.0", "192.168.1.5"},
		{"10.0.0.1", "10.0.0.1"},               // 本来就是单个 IP（FM350 那种写法）
		{"0.0.0.0.0.0.0.0", ""},                // 未分配：宁可不显示，也不显示假地址
		{"", ""},                               // 空
		{"1.2.3.4.5.6.7", ""},                  // 段数不对
		{"1.2.3.4.5.6.7.999", ""},              // 有非法段
		{"0.0.0.0.0.0.0.0.24.219.155.233", ""}, // IPv6 的点分写法：不猜，交给其它路径
	}
	for _, c := range cases {
		if got := cgcontrdpLocalAddr(c.in); got != c.want {
			t.Errorf("cgcontrdpLocalAddr(%q) = %q，想要 %q", c.in, got, c.want)
		}
	}
}

// ---- 2. USSD：展锐默认 CSCS="HEX"，码必须 hex 编码 ----

func TestUnisocUSSDHexEncoded(t *testing.T) {
	impl := NewVendorCommand(VendorUnisoc)
	f, ok := impl.(USSDFormatter)
	if !ok {
		t.Fatal("展锐必须声明 USSDFormatter（AT+CSCS=\"HEX\"，非 hex 的 USSD 码回 +CME ERROR: 3）")
	}
	if got := f.USSDCode("*101#"); got != "2A31303123" {
		t.Errorf(`USSDCode("*101#") = %q，实测可用的形态是 "2A31303123"`, got)
	}
	if got := f.USSDCode("*100#"); got != "2A31303023" {
		t.Errorf(`USSDCode("*100#") = %q`, got)
	}

	// 其它厂商**不能**被改成 hex（它们要原码）
	for _, v := range []ModemVendor{VendorUnknown, VendorQuectel, VendorFibocom, VendorSimcom, VendorMeig, VendorTDTech} {
		if _, ok := NewVendorCommand(v).(USSDFormatter); ok {
			t.Errorf("厂商 %s 不该声明 USSDFormatter（会改掉它现有的 USSD 行为）", v)
		}
	}
}

// ---- 3. 小区信息 / 流量计数器：本机做不到，必须"干净地做不到" ----

func TestUnisocCellAndTrafficDisabled(t *testing.T) {
	impl := NewVendorCommand(VendorUnisoc)

	// 指令本身要声明"不提供"
	if c := impl.CommandServingCell(); c != "" {
		t.Errorf("服务小区指令应为空串（实测所有候选都回 +CME ERROR: 4），实际 %q", c)
	}
	if c := impl.CommandNeighbourCell(); c != "" {
		t.Errorf("邻区指令应为空串，实际 %q", c)
	}
	if c := impl.CommandNetInfo(); c != "" {
		t.Errorf("网络接入信息指令应为空串（AT+QNWINFO 本机回 +CME ERROR: 4），实际 %q", c)
	}

	// ⚠️ 关键是候选表也必须空：vendorCandidates 会**先问候选表**，
	// 只覆写 CommandXxx 是拦不住 adaptiveCommands 里那几条 QENG/CPSI/QCSQ 的。
	cc, ok := impl.(CommandCandidates)
	if !ok {
		t.Fatal("展锐应当实现 CommandCandidates")
	}
	if c := cc.ServingCellCandidates(); len(c) != 0 {
		t.Errorf("服务小区候选必须为空（否则每轮白发 4 条注定失败的指令），实际 %v", c)
	}
	if c := cc.NeighbourCellCandidates(); len(c) != 0 {
		t.Errorf("邻区候选必须为空，实际 %v", c)
	}
	if c := cc.TrafficCandidates(); len(c) != 0 {
		t.Errorf("流量计数器候选必须为空（本机无可用 AT 计数器），实际 %v", c)
	}
	if c := cc.TrafficEnableCommand(); c != "" {
		t.Errorf("流量计数器没有使能指令，实际 %q", c)
	}

	// 端到端：resolveVendorCmd 应当返回空串（= 该能力不可用），而不是一条注定失败的指令
	mod := &Modem{cmdImpl: impl}
	for _, kind := range []string{KindServing, KindNeighbour, KindTraffic} {
		if cmd := mod.vendorCandidates(kind); len(cmd) > 0 && cmd[0] != "" {
			t.Errorf("%s 的候选里居然有非空指令 %q", kind, cmd[0])
		}
	}
}

// 信号**不受影响**：本机走 3GPP 标准的 CSQ / CESQ（都实测可用）
func TestUnisocSignalStillStandard(t *testing.T) {
	impl := NewVendorCommand(VendorUnisoc)
	if got := impl.CommandCSQ(); got != "AT+CSQ" {
		t.Errorf("CommandCSQ = %q，应为标准的 AT+CSQ", got)
	}
	if got := impl.CommandIMEI(); got != "AT+CGSN" {
		t.Errorf("CommandIMEI = %q，应为 AT+CGSN", got)
	}
	if got := impl.CommandModel(); got != "AT+CGMR" {
		t.Errorf("CommandModel = %q，应为 AT+CGMR（本机不支持 ATI）", got)
	}
}

// USB 网卡模式：本机没有这个概念（`AT+QCFG=?` 实测回 +CME ERROR: 4；
// 5G 基带在 SoC 内部、AT 走 SIPC，USB 侧只有给 LAN 用的 gadget）。
//
// 原来自适应指令集把这两个方法也继承了 ⇒ 每轮白发一条 AT+QCFG="usbnet"，
// 共享慢总线上白花 2 秒，界面还多一个注定为空的「USB 网卡模式」面板。
func TestUnisocHasNoUSBnetMode(t *testing.T) {
	impl := NewVendorCommand(VendorUnisoc)
	c, ok := impl.(USBnetConfigurator)
	if !ok {
		t.Fatal("结构上仍会实现该接口（嵌入式继承），靠空串表达\"不提供\"")
	}
	if got := c.CommandQueryUSBnet(); got != "" {
		t.Errorf(`CommandQueryUSBnet = %q，本机应返回空串（AT+QCFG 实测不支持）`, got)
	}
	if got := c.CommandSetUSBnet("2"); got != "" {
		t.Errorf(`CommandSetUSBnet("2") = %q，本机应返回空串`, got)
	}

	// 上层要据此直接报"不支持"，而不是把空串发出去
	mod := &Modem{Vendor: VendorUnisoc, cmdImpl: impl}
	if _, err := mod.usbnetQueryCommand(); err == nil {
		t.Error("usbnetQueryCommand 应当报错（不能返回空指令）")
	}
	if _, err := mod.usbnetSetCommand("2"); err == nil {
		t.Error("usbnetSetCommand 应当报错")
	}

	// 其它厂商不能被影响
	if got := NewVendorCommand(VendorUnknown).(USBnetConfigurator).CommandQueryUSBnet(); got == "" {
		t.Error("未识别厂商原来就走 AT+QCFG，不该被改掉")
	}
}

// ---- 4. 组网模式：5G 开关 AT+SP5GRAN（真机取证后可写，只有两档）----

func TestUnisocModeDialectSP5GRAN(t *testing.T) {
	mod := &Modem{Vendor: VendorUnisoc}
	d := modeDialectFor(mod)
	if d == nil {
		t.Fatal("展锐应当提供组网模式方言（AT+SP5GRAN 实测可读可写）")
	}
	if d.Query != "AT+SP5GRAN?" {
		t.Errorf("查询指令 = %q，应为 AT+SP5GRAN?（5G 开关）", d.Query)
	}
	if d.ReadOnly {
		t.Error("不该标只读：AT+SP5GRAN=0/1 实测都回 OK，写入后回读确认生效")
	}
	if got := d.Set(common.ModeLTE); got != "AT+SP5GRAN=0" {
		t.Errorf("仅 4G 应拼出 AT+SP5GRAN=0，实际 %q", got)
	}
	if got := d.Set(common.ModeLTENR5G); got != "AT+SP5GRAN=1" {
		t.Errorf("5G+4G 应拼出 AT+SP5GRAN=1，实际 %q", got)
	}
	// 本机没有「仅 5G」：不允许拼出任何指令（否则用户点了会白报一条错）
	if got := d.Set(common.ModeNR5G); got != "" {
		t.Errorf("本机没有仅 5G 档位，不该拼出指令：%q", got)
	}
	// 档位表只有两档，且都是实测能写的
	if len(d.Choices) != 2 {
		t.Errorf("档位应只有两档，实际 %v", d.Choices)
	}
	for _, c := range d.Choices {
		if c != common.ModeLTE && c != common.ModeLTENR5G {
			t.Errorf("档位 %q 不是本机实测支持的", c)
		}
	}
	// 读回：0/1 翻成语义档位，没见过的值原样返回
	if v, ok := d.Parse([]string{"+SP5GRAN: 1", "OK"}); !ok || v != common.ModeLTENR5G {
		t.Errorf("parse(+SP5GRAN: 1) = %q ok=%v", v, ok)
	}
	if v, ok := d.Parse([]string{"+SP5GRAN: 0", "OK"}); !ok || v != common.ModeLTE {
		t.Errorf("parse(+SP5GRAN: 0) = %q ok=%v", v, ok)
	}
	if v, ok := d.Parse([]string{"+SP5GRAN: 3", "OK"}); !ok || v != "3" {
		t.Errorf("未知取值应原样返回 3/true，实际 %q/%v", v, ok)
	}
	if _, ok := d.Parse([]string{"OK"}); ok {
		t.Error("没有这一行时不该当成读到值")
	}
	if d.Note == "" {
		t.Error("必须带说明（要说清只有两档、和为什么没有仅 5G）")
	}
	for _, bad := range []string{"**", "`"} {
		if strings.Contains(d.Note, bad) {
			t.Errorf("说明文案不能出现 markdown 记号 %q（界面上原样显示）：%s", bad, d.Note)
		}
	}

	// 其它厂商的组网模式不能被这个改动影响
	for _, v := range []ModemVendor{VendorQuectel, VendorFibocom, VendorSimcom, VendorMeig} {
		if got := modeDialectFor(&Modem{Vendor: v}); got == nil || got.ReadOnly {
			t.Errorf("厂商 %s 的组网模式方言被影响了", v)
		}
	}
}

// AT+SPRAT 的两个实测取值 ↔ 5G 开关状态：双向对得上才翻（证据：切换时 +CEREG 制式码同步变化）。
func TestUnisocModeFromSPRAT(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		ok   bool
	}{
		{"LTE 32", common.ModeLTENR5G, true},
		{"lte 32", common.ModeLTENR5G, true}, // 大小写不敏感
		{"LTE 16", common.ModeLTE, true},
		{"LTE 64", "", false}, // 没取证的数值不翻
		{"NR 32", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := unisoc.UnisocModeFromSPRAT(c.raw)
		if ok != c.ok || got != c.want {
			t.Errorf("unisoc.UnisocModeFromSPRAT(%q) = (%q, %v)，想要 (%q, %v)", c.raw, got, ok, c.want, c.ok)
		}
	}
}

func TestParseSPRAT(t *testing.T) {
	cases := []struct {
		name  string
		resp  []string
		want  string
		wantO bool
	}{
		{"真机应答", []string{f50Sprat, "OK"}, "LTE 32", true},
		{"带引号", []string{`+SPRAT: "LTE 32"`, "OK"}, "LTE 32", true},
		{"设备不枚举时回 ()", []string{"+SPRAT:()", "OK"}, "", false},
		{"空应答", []string{"OK"}, "", false},
		{"错误", []string{"+CME ERROR: 4"}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := unisoc.ParseSPRAT(c.resp)
			if ok != c.wantO || got != c.want {
				t.Errorf("unisoc.ParseSPRAT(%v) = (%q, %v)，想要 (%q, %v)", c.resp, got, ok, c.want, c.wantO)
			}
		})
	}
}

// ---- 5. 扫网：HEX 字符集编出来的运营商名要还原 ----

func TestDecodeHexOperatorName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"4E2D56FD75354FE1", "中国电信"},     // 真机实测（46011）
		{"CHINA MOBILE", "CHINA MOBILE"}, // 本来就是 ASCII → 不动
		{"CMCC", "CMCC"},
		{"", ""},
		{"4E2D56F", "4E2D56F"}, // 奇数长度 → 不是 HEX 形态
		{"BEEF", "BEEF"},       // 凑巧全是十六进制字符的 ASCII 名字：解出来是韩文 ⇒ 拒绝还原
		{"DECADE", "DECADE"},   // 同上（真实存在的英文单词）
		{"1234", "1234"},       // 解出来是控制字符 ⇒ 拒绝
		{"CHN-UNICOM", "CHN-UNICOM"},
	}
	for _, c := range cases {
		if got := decodeHexOperatorName(c.in); got != c.want {
			t.Errorf("decodeHexOperatorName(%q) = %q，想要 %q", c.in, got, c.want)
		}
	}
}

func TestParseCOPSListF50HexNames(t *testing.T) {
	list := ParseCOPSList([]string{f50Cops, "OK"})
	if len(list) != 2 {
		t.Fatalf("应解析出 2 个运营商，实际 %d：%+v", len(list), list)
	}
	byNum := map[string]OperatorEntry{}
	for _, e := range list {
		byNum[e.Numeric] = e
	}
	if e, ok := byNum["46011"]; !ok {
		t.Fatal("缺 46011（中国电信）")
	} else if e.Long != "中国电信" || e.Short != "中国电信" {
		t.Errorf("46011 的名字应当从 hex 还原成「中国电信」，实际 long=%q short=%q", e.Long, e.Short)
	}
	if e, ok := byNum["46000"]; !ok {
		t.Fatal("缺 46000（中国移动）")
	} else if e.Long != "CHINA MOBILE" {
		t.Errorf("纯 ASCII 的名字不该被改动，实际 %q", e.Long)
	}
}

// ---- 6. PDP 上下文：本机的 AT+CGDCONT? **不带引号**（APN 还可能是空串）----

// 真机原文（照抄）：
//
//	+CGDCONT:1,IPV4V6,,0.0.0.0,0,0,0,2,,,,,,0,,,0,,0,0,0,0     ← cid 1：APN 空
//	+CGDCONT:11,IPV4V6,IMS,0.0.0.0,0,0,0,2,,,,,,0,,,0,,0,0,0,0 ← cid 11：IMS
const (
	f50Cgdcont1   = `+CGDCONT:1,IPV4V6,,0.0.0.0,0,0,0,2,,,,,,0,,,0,,0,0,0,0`
	f50CgdcontIms = `+CGDCONT:11,IPV4V6,IMS,0.0.0.0,0,0,0,2,,,,,,0,,,0,,0,0,0,0`
)

// 不带引号也必须解析出上下文 —— 否则 /api/params 的 contexts 恒为空（真机现象）。
func TestParseCGDCONTUnquotedF50(t *testing.T) {
	ctxs := ParseCGDCONT([]string{f50Cgdcont1, f50CgdcontIms, "OK"})
	if len(ctxs) != 2 {
		t.Fatalf("应解析出 2 个上下文，实际 %d：%+v", len(ctxs), ctxs)
	}
	if ctxs[0].CID != 1 || ctxs[0].PDPType != "IPV4V6" || ctxs[0].APN != "" {
		t.Errorf("cid 1 应为 (IPV4V6, APN 空)，实际 %+v", ctxs[0])
	}
	if ctxs[0].Internal {
		t.Error("空 APN 不该被判成内部上下文")
	}
	if ctxs[1].CID != 11 || ctxs[1].PDPType != "IPV4V6" || ctxs[1].APN != "IMS" {
		t.Errorf("cid 11 应为 (IPV4V6, IMS)，实际 %+v", ctxs[1])
	}
	if !ctxs[1].Internal {
		t.Error("IMS 必须被判成内部上下文（避免被当成上网 APN）")
	}
}

// ⚠️ 兜底解析不能变成"什么都收"：畸形行要原样拒掉（否则界面会多出假条目）
func TestParseCGDCONTUnquotedStrict(t *testing.T) {
	bad := []string{
		`+CGDCONT:99,IPV4V6,ctnet`, // cid 超出 0..42
		`+CGDCONT:1,WEIRD,ctnet`,   // PDP 类型不在 3GPP 集合里
		`+CGDCONT:1`,               // 字段不足
		`+CGDCONT:1,IPV4V6`,        // 字段不足
		`+CGDCONT:x,IPV4V6,ctnet`,  // cid 不是数字
		`+CGDCONT:1,IPV4V6,"a,b"`,  // APN 里有逗号（切分说明这行本来就不对）
		`AT+CGDCONT?`,              // 回显行
		`OK`,                       // 结果码
	}
	if got := ParseCGDCONT(bad); len(got) != 0 {
		t.Errorf("畸形行不该解析出上下文，实际 %+v", got)
	}
	// 标准带引号形态照旧（其它厂商一个字都不能变）
	std := ParseCGDCONT([]string{`+CGDCONT: 1,"IP","ctnet","10.0.0.1",0,0`, `+CGDCONT: 2,"IPV6","ims",""`})
	if len(std) != 2 || std[0].CID != 1 || std[0].APN != "ctnet" || std[1].CID != 2 || std[1].APN != "ims" {
		t.Errorf("带引号的形态被改坏了：%+v", std)
	}
}

// ---- 7. 小区视图：本机没有扫描指令，但 URC 里已经有服务小区 ----

// 「基站扫描」页在 F50 上不该是空的：服务小区用 URC 拼（零 AT），邻区仍然为空（无来源）。
func TestUnisocServingCellFromURC(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 35,11`)
	mod.feedURCLine(`+CESQ: 99,99,255,255,255,255,59,53,54`)
	mod.feedURCLine(`+CGREG: 1,"0000","0A11A009",11`)
	mod.feedURCLine(`+CEREG: 1,"CFA102","0A11A009",11`)
	mod.feedURCLine(`+SPPCI: 0000008C`)
	mod.feedURCLine(`+SPCLB: 3`)
	mod.feedURCLine(realSPNWNAME)

	if !mu300FileBackedState(mod) {
		t.Fatal("前提：应已进入本机文件供状态的路径")
	}
	cells := mod.urcServingCells()
	if len(cells) != 1 {
		t.Fatalf("应给出 1 个服务小区，实际 %d：%+v", len(cells), cells)
	}
	c := cells[0]
	if !c.Serving {
		t.Error("必须标记成服务小区（否则会被归到邻区）")
	}
	if c.PCID != 140 {
		t.Errorf("PCI 应为 140（+SPPCI: 0000008C），实际 %d", c.PCID)
	}
	if c.CellID != "0A11A009" {
		t.Errorf("小区 ID 应来自 +CxREG，实际 %q", c.CellID)
	}
	if c.Band != 3 {
		t.Errorf("频段应来自 +SPCLB，实际 %d", c.Band)
	}
	if c.Operator != "中国电信" {
		t.Errorf("运营商应来自 +SPNWNAME，实际 %q", c.Operator)
	}
	if !strings.HasPrefix(c.Raw, "urc:") {
		t.Errorf("必须带 urc: 标记（否则会被当成真实扫描结果），实际 %q", c.Raw)
	}

	// 缓存视图（界面首屏走 cache=1）也要带上它，而且**不写回**扫描缓存
	res := cachedScanResult(mod)
	if res.ServingCount != 1 || len(res.Serving) != 1 || res.Count != 1 {
		t.Errorf("缓存视图应带上这份服务小区，实际 serving=%d count=%d %+v",
			res.ServingCount, res.Count, res.Serving)
	}
	if res.NeighbourCnt != 0 {
		t.Errorf("邻区仍然没有来源，必须为 0，实际 %d", res.NeighbourCnt)
	}
	mod.mu.Lock()
	cachedCells := len(mod.Cells)
	mod.mu.Unlock()
	if cachedCells != 0 {
		t.Errorf("不该写回扫描缓存（mod.Cells 必须还是空），实际 %d", cachedCells)
	}
}

// ⚠️ 别的模组一个字都不能变：不是展锐 SIPC 就返回 nil（它们有自己的扫描指令）。
func TestUnisocServingCellFromURCOnlyForF50(t *testing.T) {
	for _, port := range []string{"/dev/ttyUSB0", "/dev/ttyACM1", "/dev/mhi_DUN", "COM3", ""} {
		mod := newURCTestModem(port)
		mod.noteURCFeedState(true, nil)
		mod.feedURCLine(`+CSQ: 35,11`)
		mod.feedURCLine(`+CGREG: 1,"0000","0A11A009",11`)
		mod.feedURCLine(`+SPPCI: 0000008C`)
		if got := mod.urcServingCells(); len(got) != 0 {
			t.Errorf("%q 不是展锐 SIPC，不该走 URC 服务小区那条路，实际 %+v", port, got)
		}
	}
}
