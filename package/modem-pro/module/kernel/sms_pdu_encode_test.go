package kernel

// 短信 PDU **编码**的回归测试。
//
// 基准值是"权威样例"：下面三条 PDU 是 **F50（展锐 UMS9620）上厂商自己的编码器
// 生成出来的**（见 sms_pdu_encode.go 文件头）。编码器只要和它们一位不差，
// 就说明地址域、DCS、有效期、七位组打包这些全都对 —— 这比"自己写一遍再自己验一遍"
// 可靠得多。

import (
	"fmt"
	"modemd/module/sms"
	"strings"
	"testing"
)

func TestEncodeSMSSubmitGoldenVectors(t *testing.T) {
	cases := []struct {
		name   string
		phone  string
		text   string
		want   string
		wantLn int
	}{
		{
			name:  "国际号码 + GSM7（HELLO）",
			phone: "+8613800138000", text: "HELLO",
			want: "0011000D91683108108300F00000AA05C82293F904", wantLn: 20,
		},
		{
			name:  "国际号码 + UCS2（中文）",
			phone: "+8613800138000", text: "测试",
			want: "0011000D91683108108300F00008AA046D4B8BD5", wantLn: 19,
		},
		{
			name:  "国内号码（不带 +）+ GSM7（OK）",
			phone: "13800138000", text: "OK",
			want: "0011000B813108108300F00000AA02CF25", wantLn: 16,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pdu, n, err := sms.EncodeSMSSubmit(c.phone, c.text)
			if err != nil {
				t.Fatalf("编码失败：%v", err)
			}
			if pdu != c.want {
				t.Errorf("PDU 不匹配\n  得到 %s\n  想要 %s", pdu, c.want)
			}
			if n != c.wantLn {
				t.Errorf("AT+CMGS 的 len 应为 %d（TPDU 字节数），得到 %d", c.wantLn, n)
			}
			// 不变量：len = PDU 字节数 - 1（开头那个 SCA 长度字节不算在 TPDU 里）
			if n != len(pdu)/2-1 {
				t.Errorf("len 与 PDU 长度不自洽：len=%d，PDU=%d 字节", n, len(pdu)/2)
			}
		})
	}
}

// 号码写法：带 + / 不带 + 决定 TON（91 / 81），分隔符要去掉，非法输入要报错。
func TestNormalizeSMSNumber(t *testing.T) {
	ok := []struct {
		in   string
		want string
		intl bool
	}{
		{"+8613800138000", "8613800138000", true},
		{" 13800138000 ", "13800138000", false},
		{"138 0013 8000", "13800138000", false},
		{"+86-138-0013-8000", "8613800138000", true},
		{"(138)00138000", "13800138000", false},
	}
	for _, c := range ok {
		got, intl, err := sms.NormalizeSMSNumber(c.in)
		if err != nil || got != c.want || intl != c.intl {
			t.Errorf("normalizeSMSNumber(%q) = (%q, %v, %v)，想要 (%q, %v, nil)",
				c.in, got, intl, err, c.want, c.intl)
		}
	}
	bad := []string{"", "+", "1380013800a", "1380013800013800138000138001", "013800138000"}
	for _, c := range bad {
		if _, _, err := sms.NormalizeSMSNumber(c); err == nil {
			t.Errorf("normalizeSMSNumber(%q) 应当报错", c)
		}
	}
}

// 七位组打包：低位先出、跨字节续写（用权威样例里的两串钉死）
func TestPackSeptets(t *testing.T) {
	cases := []struct {
		septets []byte
		want    string
	}{
		{[]byte{0x4F, 0x4B}, "CF25"},                         // "OK"
		{[]byte{0x48, 0x45, 0x4C, 0x4C, 0x4F}, "C82293F904"}, // "HELLO"
		{nil, ""},
	}
	for _, c := range cases {
		got := fmt.Sprintf("%X", sms.PackSeptets(c.septets))
		if got != c.want {
			t.Errorf("packSeptets(%v) = %s，想要 %s", c.septets, got, c.want)
		}
	}
}

// 编码选择：能编 GSM7 就用 DCS=00；中文等表外字符整体退 DCS=08(UCS2)；
// 扩展字符占**两个**七位组。
func TestEncodeSMSSubmitDCSSelection(t *testing.T) {
	// 纯 GSM7
	if pdu, _, err := sms.EncodeSMSSubmit("13800138000", "hi"); err != nil || !strings.Contains(pdu, "0000AA02") {
		t.Errorf("纯 ASCII 应当走 GSM7（DCS=00/VP=AA）；得到 %s err=%v", pdu, err)
	}
	// 含一个中文 → 整体 UCS2
	pdu, _, err := sms.EncodeSMSSubmit("13800138000", "hi中")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pdu, "0008AA06") {
		t.Errorf("含中文应当整体走 UCS2（DCS=08，UDL=6 字节）；得到 %s", pdu)
	}
	// 扩展字符 `€` = 0x1B 0x65 → 两个七位组，UDL 记 2，打出来是 9B 32
	pdu, n, err := sms.EncodeSMSSubmit("13800138000", "€")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(pdu, "AA029B32") {
		t.Errorf("扩展字符应当占 2 个七位组（UDL=02，UD=9B32）；得到 %s", pdu)
	}
	if n != len(pdu)/2-1 {
		t.Errorf("len 与 PDU 长度不自洽：len=%d pdu=%s", n, pdu)
	}
}

// 超长正文必须**报错**而不是截断：截断会把半句话发出去，收不回来。
func TestEncodeSMSSubmitTooLong(t *testing.T) {
	// GSM7：161 个 ASCII 字符
	if _, _, err := sms.EncodeSMSSubmit("13800138000", strings.Repeat("A", 161)); err == nil {
		t.Error("161 个 GSM7 字符应当报错（单条上限 160）")
	}
	if _, _, err := sms.EncodeSMSSubmit("13800138000", strings.Repeat("A", 160)); err != nil {
		t.Errorf("160 个 GSM7 字符应当可以发：%v", err)
	}
	// UCS2：71 个汉字
	if _, _, err := sms.EncodeSMSSubmit("13800138000", strings.Repeat("中", 71)); err == nil {
		t.Error("71 个汉字应当报错（单条上限 70）")
	}
	if _, _, err := sms.EncodeSMSSubmit("13800138000", strings.Repeat("中", 70)); err != nil {
		t.Errorf("70 个汉字应当可以发：%v", err)
	}
}

// Ctrl+Z 混进正文会在模组那里被当成结束符（正文被截断），必须挡在前面
func TestEncodeSMSSubmitRejectsCtrlZ(t *testing.T) {
	if _, _, err := sms.EncodeSMSSubmit("13800138000", "ab\u001acd"); err == nil {
		t.Error("正文含 Ctrl+Z 应当报错")
	}
	if _, _, err := sms.EncodeSMSSubmit("13800138000", ""); err == nil {
		t.Error("正文为空应当报错")
	}
}

// —— 下面不再自己写十六进制助手：包里的测试文件已经有一个 `hexString`，
// 重复定义会编译不过，所以统一用 fmt.Sprintf("%X", …)。
