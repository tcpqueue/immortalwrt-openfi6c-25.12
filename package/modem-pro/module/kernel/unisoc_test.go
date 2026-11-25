package kernel

// 展锐（Unisoc）方言的回归测试。
//
// 重点钉住两件事：
//  1. **ATI 不可用的模组也能被认出来** —— 中兴 F50/UMS9620 回 `+CME ERROR: 4`，
//     识别必须靠 `AT+CGMR` 的固件串（真机原文见下面 fixture）。
//  2. 别把别的厂商误认成展锐（`SPRD` 这种短关键字最容易出事）。

import (
	"modemd/module/dial"
	"modemd/module/unisoc"
	"testing"
)

// 真机原文（192.168.77.1 / 中兴 F50，AT+CGMR）
var unisocCGMR = []string{
	"Platform Version: MOCORTM_V2_22C_W25.39.4_Debug",
	"Project Version:   QogirN6Pro_PS",
	"BASE  Version:    5G_MODEM_V2_22C_W25.39.4",
	"HW Version:        ums9620_modem",
	"09-25-2025 06:25:47",
	"OK",
}

func TestUnisocVendorDetection(t *testing.T) {
	if got := DetectVendorFromATI(unisocCGMR); got != VendorUnisoc {
		t.Fatalf("CGMR 原文应认成展锐，实际 %q", got)
	}
	// ATI 原文是错误行 —— 认不出来是**预期**的（调用方会再拿 CGMR 重跑一遍）
	if got := DetectVendorFromATI([]string{"+CME ERROR: 4"}); got != VendorUnknown {
		t.Errorf("只有错误行时应保持 unknown，实际 %q", got)
	}
}

func TestUnisocDoesNotStealOtherVendors(t *testing.T) {
	// ATI 原文（别的厂商）：不能被展锐的关键字抢走
	cases := map[string][]string{
		"移远":  {`ATI`, "Quectel", "RM520N-GL", "Revision: RM520NGLAAR03A03M4G", "OK"},
		"广和通": {"ATI", "Fibocom", "FM350-GL", "OK"},
		"芯讯通": {"ATI", "SIMCOM", "SIM8200EA-M2", "OK"},
	}
	for name, resp := range cases {
		cmd := &unisoc.UnisocCommand{}
		if cmd.DetectVendor(resp) != VendorUnknown {
			t.Errorf("%s 的 ATI 被展锐误认了：%q", name, resp)
		}
	}
}

func TestUnisocCommandOverrides(t *testing.T) {
	c := &unisoc.UnisocCommand{}
	// 展锐不支持 ATI：型号指令必须是 AT+CGMR
	if got := c.CommandModel(); got != "AT+CGMR" {
		t.Errorf("CommandModel() = %q，应为 AT+CGMR（展锐不支持 ATI）", got)
	}
	// 没有 AT+QCAINFO（移远私有）：必须返回空串 = "本厂商未实现，别去发"
	if got := c.CommandCAInfo(); got != "" {
		t.Errorf("CommandCAInfo() = %q，应为空串（展锐没有 AT+QCAINFO）", got)
	}
	// 标准 3GPP 指令沿用自适应底座（不能被覆写没了）
	if c.CommandIMEI() == "" || c.CommandCSQ() == "" || c.CommandOperator() == "" {
		t.Error("标准指令应由 adaptiveCommands 底座提供，不能为空")
	}
}

// ====================== 签约速率 AMBR + QCI：AT+C5GQOSRDP ======================
//
// 真机原文（2026-10-06，192.168.77.1 / 中兴 F50 / 展锐 UMS9620，走 modemd 的 nr2 快路）：
//
//	AT+C5GQOSRDP    → +C5GQOSRDP:1,9,0,0,0,0,1000000,1000000,0   cid 1（数据 ctnet）：5QI=9、SAMBR 1000/1000 Mbps
//	                  +C5GQOSRDP:11,5,0,0,0,0,300000,75000,0     cid 11（IMS）：5QI=5、300/75 Mbps
//	AT+C5GQOSRDP=?  → +C5GQOSRDP:()     不带参数（"=1" 这种写法不被接受）
//
// 交叉验证：同一张卡（电信 5G SA）在 RM520 上 `AT+QNWCFG="nr5g_ambr"` 报的也是
// ctnet 1000/1000 与 IMS 300/75，两边对得上 ⇒ 字段口径可信。
var unisocC5GQOSRDP = []string{
	"+C5GQOSRDP:1,9,0,0,0,0,1000000,1000000,0",
	"+C5GQOSRDP:11,5,0,0,0,0,300000,75000,0",
	"OK",
}

// 展锐必须声明 AMBRQuerier（否则 F50 界面上「签约速率 / QCI」永远是"未上报"）
func TestUnisocAMBRCommand(t *testing.T) {
	var impl ModemCommand = &unisoc.UnisocCommand{}
	q, ok := impl.(AMBRQuerier)
	if !ok {
		t.Fatal("展锐必须实现 AMBRQuerier（真机支持 AT+C5GQOSRDP）")
	}
	if got := q.CommandAMBR(); got != "AT+C5GQOSRDP" {
		t.Fatalf("CommandAMBR() = %q，want AT+C5GQOSRDP", got)
	}
	mod := &Modem{ID: 4242, Model: "F50", cmdImpl: &unisoc.UnisocCommand{}}
	if got := mod.ambrCommand(); got != "AT+C5GQOSRDP" {
		t.Fatalf("ambrCommand() = %q，want AT+C5GQOSRDP", got)
	}
}

// 真机应答按**拨号的那条 cid** 选行：默认 cid 1 = 数据、切到 cid 11 = IMS
func TestUnisocAMBRParse(t *testing.T) {
	mod := &Modem{ID: 4242, Model: "F50", cmdImpl: &unisoc.UnisocCommand{}}

	// 没拨过号 → DefaultDataCID(1) = 数据 ctnet 那条：1000/1000 Mbps、QCI 9
	r, ok := parseAMBRForModem(mod, "AT+C5GQOSRDP", unisocC5GQOSRDP)
	if !ok || r == nil {
		t.Fatal("真机应答应能解析出 AMBR")
	}
	if r.CID != 1 || r.DLMbps != 1000 || r.ULMbps != 1000 || r.QCI != 9 {
		t.Fatalf("cid=1（数据）那条解析错误：%+v", r)
	}

	// 拨号状态记着 cid 11 → 必须取 IMS 那条
	mod.dialUpdate(func(s *dial.DialState) { s.CID, s.APN = 11, "ims" })
	r, ok = parseAMBRForModem(mod, "AT+C5GQOSRDP", unisocC5GQOSRDP)
	if !ok || r.CID != 11 || r.DLMbps != 300 || r.ULMbps != 75 || r.QCI != 5 {
		t.Fatalf("cid=11（IMS）那条解析错误：%+v ok=%v", r, ok)
	}

	// `=?` 那种空应答（+C5GQOSRDP:()）不能被当成有效速率
	if _, ok := parseAMBRForModem(mod, "AT+C5GQOSRDP", []string{"+C5GQOSRDP:()", "OK"}); ok {
		t.Error("+C5GQOSRDP:() 不应解析出 AMBR")
	}
}

func TestVendorLabelUnisoc(t *testing.T) {
	if got := VendorLabel(VendorUnisoc); got != "展锐 Unisoc" {
		t.Errorf("VendorLabel(VendorUnisoc) = %q", got)
	}
}

// 真机 IMEI（192.168.77.1 / 中兴 F50，AT+CGSN）
const unisocRealIMEI = "867306061176601"

func TestUnisocIMEIDialect(t *testing.T) {
	d := imeiDialectFor(&Modem{Vendor: VendorUnisoc})
	if d == nil || d.Set == nil {
		t.Fatal("展锐必须有 IMEI 改写方言（真机取证：AT+SPIMEI=0 被接受）")
	}
	want := `AT+SPIMEI=0,"` + unisocRealIMEI + `"`
	if got := d.Set(unisocRealIMEI); got != want {
		t.Errorf("展锐 IMEI 写入指令 = %q，应为 %q", got, want)
	}
	if d.Note == "" {
		t.Error("方言必须带说明（界面要显示给用户看）")
	}
}

// 展锐方言不能顶掉别的厂商的方言（各家指令不同，串了就是刷错指令）
func TestUnisocIMEIDoesNotOverrideOthers(t *testing.T) {
	cases := []struct {
		vendor ModemVendor
		want   string
	}{
		{VendorQuectel, `AT+EGMR=1,7,"` + unisocRealIMEI + `"`},
		{VendorSimcom, "AT+SIMEI=" + unisocRealIMEI},
		{VendorMeig, `AT+LCTSN=1,7,"` + unisocRealIMEI + `"`},
		{VendorUnisoc, `AT+SPIMEI=0,"` + unisocRealIMEI + `"`},
	}
	for _, c := range cases {
		d := imeiDialectFor(&Modem{Vendor: c.vendor})
		if d == nil || d.Set == nil {
			t.Errorf("%s 的 IMEI 方言丢了", c.vendor)
			continue
		}
		if got := d.Set(unisocRealIMEI); got != c.want {
			t.Errorf("%s IMEI 指令 = %q，应为 %q", c.vendor, got, c.want)
		}
	}
}

// 展锐的短信**收发都必须走 PDU 模式**（F50 实机：CSCS 是 "HEX"、CMGF 是 0，
// 厂商自己的 sms 脚本也是 PDU）。这两个可选接口一旦掉了，短信会静默地读不出/发不出，
// 所以在这里钉住。
func TestUnisocSMSUsesPDU(t *testing.T) {
	impl := NewVendorCommand(VendorUnisoc)

	r, ok := impl.(PDUSMSReader)
	if !ok {
		t.Fatal("展锐必须实现 PDUSMSReader（文本模式在本机不可信）")
	}
	if r.CommandSMSModePDU() != "AT+CMGF=0" {
		t.Errorf("切 PDU 模式 = %q，应为 AT+CMGF=0", r.CommandSMSModePDU())
	}
	if r.CommandReadAllSMSPDU() != "AT+CMGL=4" {
		t.Errorf("PDU 读全部 = %q，应为 AT+CMGL=4", r.CommandReadAllSMSPDU())
	}

	s, ok := impl.(PDUSMSSender)
	if !ok {
		t.Fatal("展锐必须实现 PDUSMSSender（文本模式发不出去）")
	}
	if got := s.CommandSendSMSPDULength(20); got != "AT+CMGS=20" {
		t.Errorf("提交 PDU 的第一步 = %q，应为 AT+CMGS=20", got)
	}
	// 两个接口必须给**同一条**切模式指令：否则读一次会把模式切乱
	if s.CommandSMSModePDU() != r.CommandSMSModePDU() {
		t.Error("PDUSMSSender 与 PDUSMSReader 的切模式指令必须一致")
	}
}

// PDU 发送能力**只该**展锐有：别的厂商一个字都不受影响（doSendSMS 里是早退分支）。
func TestPDUSendOnlyDeclaredByUnisoc(t *testing.T) {
	for _, v := range []ModemVendor{VendorUnknown, VendorQuectel, VendorFibocom, VendorSimcom, VendorMeig, VendorTDTech} {
		if _, ok := NewVendorCommand(v).(PDUSMSSender); ok {
			t.Errorf("厂商 %s 不该声明 PDUSMSSender（会改变它现有的发送行为）", v)
		}
	}
	if _, ok := NewVendorCommand(VendorUnisoc).(PDUSMSSender); !ok {
		t.Error("展锐应当声明 PDUSMSSender")
	}
}
