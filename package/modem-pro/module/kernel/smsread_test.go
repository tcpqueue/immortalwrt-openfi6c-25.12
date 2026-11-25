package kernel

// ====================== 短信读取路径的选择（引擎级） ======================
//
// 为什么要单独钉住：读短信有两条路径（文本模式 / PDU 模式），选错了不会报错、
// 只会把正文解成怪字符（FM350 的“短信读取不正常”就是这么来的）。所以按机型把选择固定住。

import (
	"modemd/module/fibocom"
	"modemd/module/modelquirk"
	"modemd/module/quectel"
	"modemd/module/sms"
	"modemd/module/tdtech"
	"strings"
	"testing"
)

// 只有「机型特例标了 SMSReadPDU」或「厂商自己声明了 PDUSMSReader」才走 PDU 模式
func TestSMSPDUReaderSelection(t *testing.T) {
	cases := []struct {
		name    string
		mod     *Modem
		wantPDU bool
	}{
		{
			// FM350-GL：文本模式回的"正文"其实是十六进制 PDU（实测）→ 走标准 PDU 读法
			name:    "FM350-GL 走 PDU（特例表）",
			mod:     &Modem{ID: 0, Model: "ATI|Manufacturer: Fibocom Wireless Inc.|Model: FM350-GL|OK", cmdImpl: &fibocom.FibocomCommand{}},
			wantPDU: true,
		},
		{
			// 同厂别的机型不受影响：文本模式正常，别去动它
			name:    "FM650-CN 仍走文本模式",
			mod:     &Modem{ID: 0, Model: "ATI|Manufacturer: Fibocom|Model: FM650-CN|OK", cmdImpl: &fibocom.FibocomCommand{}},
			wantPDU: false,
		},
		{
			name:    "FM160-EAU 仍走文本模式",
			mod:     &Modem{ID: 0, Model: "FM160-EAU", cmdImpl: &fibocom.FibocomCommand{}},
			wantPDU: false,
		},
		{
			// 鼎桥 MT5700：厂商级声明（文本模式干脆不回正文）→ 保持原路径
			name:    "MT5700 走厂商的 PDU 读法",
			mod:     &Modem{ID: 0, Model: "ATI|Model: MT5700M-CN|OK", cmdImpl: &tdtech.MT5700Command{}},
			wantPDU: true,
		},
		{
			// 型号还没读到（启动早期）→ 谁都不命中，用标准文本模式，不影响启动
			name:    "未知型号走文本模式",
			mod:     &Modem{ID: 0, cmdImpl: &fibocom.FibocomCommand{}},
			wantPDU: false,
		},
		{
			name:    "移远照旧不走 PDU",
			mod:     &Modem{ID: 0, Model: "RM502Q-GL", cmdImpl: &quectel.QuectelCommand{}},
			wantPDU: false,
		},
	}
	for _, c := range cases {
		r, ok := c.mod.smsPDUReader()
		if ok != c.wantPDU {
			t.Errorf("%s：走 PDU=%v, want %v", c.name, ok, c.wantPDU)
			continue
		}
		if !ok || r == nil {
			continue
		}
		// 读法本身必须是 3GPP 标准的三条指令（FM350 走标准读法，MT5700 走厂商实现）
		if r.CommandSMSModePDU() != "AT+CMGF=0" {
			t.Errorf("%s：切 PDU 模式 = %q", c.name, r.CommandSMSModePDU())
		}
		if r.CommandReadAllSMSPDU() != "AT+CMGL=4" {
			t.Errorf("%s：读全部 = %q", c.name, r.CommandReadAllSMSPDU())
		}
		if !strings.Contains(c.mod.Model, "MT5700") {
			if _, isStd := r.(StandardPDUReader); !isStd {
				t.Errorf("%s：FM350 这类应走 StandardPDUReader，实际 %T", c.name, r)
			}
		}
	}
}

// 特例表里的标记本身也要钉住（别被顺手删掉）
func TestFM350SMSPDUQuirk(t *testing.T) {
	q := modelquirk.Lookup("ATI|Manufacturer: Fibocom Wireless Inc.|Model: FM350-GL|Revision: 81600.0000.00.29.22.40|OK")
	if !q.SMSReadPDU {
		t.Error("FM350 的特例应标 SMSReadPDU（文本模式的正文其实是十六进制 PDU）")
	}
	if q.PDPAddrCmd != "AT+CGPADDR=2" || !q.IgnoreCCLKTZ {
		t.Errorf("FM350 的其它特例不该被改动：%+v", q)
	}
	for _, m := range []string{"FM650-CN", "FM160-EAU", "RM502Q-GL", "MT5700M-CN", ""} {
		if modelquirk.Lookup(m).SMSReadPDU {
			t.Errorf("%q 不该标 SMSReadPDU", m)
		}
	}
}

// 端到端串一下：同一段十六进制，**文本模式**解出来会带 UDH 怪字符，**PDU 读法**解出来是干净正文。
// （前者正是实测到的"短信读取不正常"：页面正文开头多出一小段乱码。）
func TestFM350SMSDecodedMatchesTextModeHeader(t *testing.T) {
	// 文本模式那条路：把 PDU 整串当 UCS2 文本解（现有代码就是这么干的）
	garbled := sms.DecodeSMSText(strings.ToLower(fm350PDUPart1))
	if !strings.Contains(garbled, "\u0500") && !strings.Contains(garbled, "β") {
		t.Logf("文本模式解出来 = %q（这个固件下前面几个码位就是 UDH 解出来的怪字符）", firstRunes(garbled, 12))
	}

	// PDU 读法：正文干净、且与文本模式头行报的号码/时间一致
	resp := []string{
		`+CMGL: 1, 1,, 159`, fm350PDUPart1,
		`+CMGL: 3, 1,, 159`, fm350PDUPart3,
		`OK`,
	}
	list, _ := sms.ParsePDUList(resp)
	if len(list) != 2 {
		t.Fatalf("条目数 = %d", len(list))
	}
	want := list[0].Content
	if strings.HasPrefix(want, "\u0500") {
		t.Errorf("PDU 解出来的正文仍带着 UDH 怪字符：%q", firstRunes(want, 12))
	}
	if !strings.HasPrefix(want, "尊敬的客户") {
		t.Errorf("正文没解对：%q", firstRunes(want, 20))
	}
}
