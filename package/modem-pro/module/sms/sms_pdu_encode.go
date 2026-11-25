package sms

// ====================== 短信 PDU 编码（发送用） ======================
//
// 项目里原本只有**解码**（`DecodePDU` / `ParsePDUList`，读短信用的），
// 发短信一直靠**文本模式**（`AT+CMGS="号码"` + 正文 + Ctrl+Z）。
// 但有的固件不支持文本模式，或者文本模式不可信 —— 中兴 F50（展锐 UMS9620）正是：
//   · `AT+CSCS?` → `"HEX"`、`AT+CMGF?` → `0`（本机默认就是 PDU）；
//   · 厂商自己的 `sms send` 脚本就是"`AT+CMGS=<len>` + PDU + Ctrl+Z"两步走。
// 所以补一个编码器：把"号码 + 正文"编成 PDU 串。
//
// ── 权威样例（在 F50 上用厂商的编码器生成，一字不差地抄来当测试基准）──
//
//	+8613800138000 / HELLO → AT+CMGS=20 + 0011000D91683108108300F00000AA05C82293F904
//	+8613800138000 / 测试  → AT+CMGS=19 + 0011000D91683108108300F00008AA046D4B8BD5
//	13800138000    / OK    → AT+CMGS=16 + 0011000B813108108300F00000AA02CF25
//
// ── 结构（照这三条样例倒推，不是猜的）──
//
//	SCA     00            —— 用 SIM 卡里的短信中心（不自己带号码）
//	首字节  11            —— SMS-SUBMIT，且带相对格式的有效期（0x11 = 0b00010001）
//	MR      00            —— 参考号，本机自己填 0
//	DA 长度 号码位数（字符数，不是字节数）
//	TON/NPI 91 = 带 `+` 的国际号码；81 = 不带 `+` 的国内号码
//	号码    BCD 半字节交换（86→68），奇数位补 F
//	PID     00
//	DCS     00 = GSM 7bit；08 = UCS2（中文等 GSM 表外的字符）
//	VP      AA            —— 相对有效期（三条样例都是 AA）
//	UDL     GSM7 时是**七位组个数**；UCS2 时是**字节数**
//	UD      正文
//
// ⚠️ `AT+CMGS=<len>` 的 len 是 **TPDU 字节数** —— 也就是"整串 PDU 去掉开头 1 个
// SCA 字节"。样例：`0011000D91…C82293F904` 是 21 字节，len = 20。
//
// ⚠️ 本文件**只编码一条短信**（不分条）。超长正文返回错误而不是截断：
// 截断会把半句话发出去，比报错糟得多（长短信分条 UDH 还没有实现，见 TODO）。

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// smsGSM7Index / smsGSM7ExtCode 是**反查表**：rune → 七位组。
//
// ⚠️ 刻意从解码侧那两张表（go 的 `gsm7Default` / `gsm7Ext`）**反向生成**，
// 不再手抄第二份：两份表一旦不一致，就会出现“发出去能编、收回来解不开”
// （或反过来）的幽灵 bug，而且极难查。
// 解码侧的 `gsm7Ext` 里没有 `~`(0x3D) 这一格，所以 `~` 会退到 UCS2 —— 慢一点但绝对正确。
var (
	smsGSM7Index = func() map[rune]byte {
		m := make(map[rune]byte, len(Gsm7Default))
		for i, r := range Gsm7Default {
			if r == 0x1B {
				continue // 转义前缀本身不能当普通字符编
			}
			if _, dup := m[r]; !dup {
				m[r] = byte(i)
			}
		}
		return m
	}()
	smsGSM7ExtCode = func() map[rune]byte {
		m := make(map[rune]byte, len(Gsm7Ext))
		for code, r := range Gsm7Ext {
			m[r] = code
		}
		return m
	}()
)

// smsSubmitFirstOctet 三条样例的首字节都是 0x11：
// TP-MTI=01（SUBMIT）+ TP-VPF=10（相对格式有效期）
const smsSubmitFirstOctet = 0x11

// smsVP 相对有效期，样例固定 0xAA
const smsVP = 0xAA

// smsMaxUDLGSM7 / smsMaxUDLUCS2 单条短信的正文字数上限（七位组 / 字节）。
// 超了就返回错误：本项目还没实现分条（UDH 拼接），截断会把半句话发出去。
const (
	smsMaxUDLGSM7 = 160
	smsMaxUDLUCS2 = 70
)

// EncodeSMSSubmit 把"号码 + 正文"编成 PDU。
//
// 返回：
//
//	pdu  —— 完整 PDU 十六进制串（大写，含开头的 SCA 长度字节 `00`）；
//	       直接当 `AT+CMGS` 之后那一行正文发出去（后面跟 Ctrl+Z）。
//	len  —— `AT+CMGS=<len>` 里的 len，也就是 **TPDU 字节数**（= len(pdu)/2 - 1）。
//
// phone 支持带 `+` 的国际号码和不带 `+` 的国内号码（TON 分别是 91 / 81）；
// 分隔符（空格、`-`）会被去掉。正文能用 GSM 7bit 编就用 7bit，否则退 UCS2。
func EncodeSMSSubmit(phone, text string) (pdu string, tpduLen int, err error) {
	digits, international, err := NormalizeSMSNumber(phone)
	if err != nil {
		return "", 0, err
	}
	if text == "" {
		return "", 0, fmt.Errorf("短信正文为空")
	}
	if strings.ContainsRune(text, 0x1A) {
		// 0x1A 是发送结束符，混进正文会在模组那里被提前截断（同 validSMSContent 的判据）
		return "", 0, fmt.Errorf("短信正文不能包含 Ctrl+Z(0x1A)")
	}

	// 正文编码：整条一起决定用哪种，不混用
	var dcs byte
	var udl int
	var udHex string
	if septets, ok := gsm7Encode(text); ok {
		if len(septets) > smsMaxUDLGSM7 {
			return "", 0, fmt.Errorf("正文太长：GSM 7bit 单条上限 %d 个字符，当前 %d（长短信分条尚未实现）",
				smsMaxUDLGSM7, len(septets))
		}
		dcs = 0x00
		udl = len(septets)
		udHex = hex.EncodeToString(PackSeptets(septets))
	} else {
		b := []byte(string2UTF16BE(text))
		if len(b)/2 > smsMaxUDLUCS2 {
			return "", 0, fmt.Errorf("正文太长：UCS2 单条上限 %d 个字，当前 %d 个（长短信分条尚未实现）",
				smsMaxUDLUCS2, len(b)/2)
		}
		dcs = 0x08
		udl = len(b)
		udHex = hex.EncodeToString(b)
	}

	var sb strings.Builder
	sb.WriteString("00")                                     // SCA 长度 = 0 → 用 SIM 里的短信中心
	sb.WriteString(fmt.Sprintf("%02X", smsSubmitFirstOctet)) // 11
	sb.WriteString("00")                                     // MR（参考号）
	// DA 长度是"号码字符数"，不是字节数（样例：13 位 → 0D）
	sb.WriteString(fmt.Sprintf("%02X", len(digits)))
	if international {
		sb.WriteString("91") // 国际号码（带 +）
	} else {
		sb.WriteString("81") // 国内号码（不带 +）
	}
	sb.WriteString(bcdNumber(digits))
	sb.WriteString("00")                     // PID
	sb.WriteString(fmt.Sprintf("%02X", dcs)) // DCS
	sb.WriteString(fmt.Sprintf("%02X", smsVP))
	sb.WriteString(fmt.Sprintf("%02X", udl))
	sb.WriteString(udHex)

	pdu = strings.ToUpper(sb.String())
	if len(pdu)%2 != 0 {
		return "", 0, fmt.Errorf("内部错误：PDU 长度为奇数（%d）", len(pdu))
	}
	// AT+CMGS 要的是 TPDU 字节数 = 整个 PDU 去掉开头那个 SCA 字节
	return pdu, len(pdu)/2 - 1, nil
}

// NormalizeSMSNumber 规范化号码，返回纯数字串与"是否国际号码"（决定 TON）。
func NormalizeSMSNumber(phone string) (digits string, international bool, err error) {
	p := strings.TrimSpace(phone)
	// 分隔符常见于"138 0013 8000"这种写法，去掉；其余字符一律不接受
	p = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '(', ')':
			return -1
		}
		return r
	}, p)
	if strings.HasPrefix(p, "+") {
		international = true
		p = p[1:]
	}
	if p == "" {
		return "", false, fmt.Errorf("号码为空")
	}
	if len(p) > 20 {
		return "", false, fmt.Errorf("号码太长：%d 位", len(p))
	}
	for _, r := range p {
		if r < '0' || r > '9' {
			return "", false, fmt.Errorf("号码只能包含数字与可选的 +（收到 %q）", string(r))
		}
	}
	if !international && strings.HasPrefix(p, "0") && len(p) > 1 {
		// 国内号码不该以 0 开头（那是长途前缀，模组按国内号码发会失败）
		return "", false, fmt.Errorf("国内号码不该以 0 开头；国际号码请写 +国家码…")
	}
	return p, international, nil
}

// bcdNumber 把号码编成 BCD 半字节交换形式，奇数位在末尾补 F。
//
//	"8613800138000"（13 位）→ 68 31 08 10 83 00 F0 → "683108108300F0"
func bcdNumber(digits string) string {
	if len(digits)%2 == 1 {
		digits += "F"
	}
	var sb strings.Builder
	for i := 0; i+1 < len(digits); i += 2 {
		sb.WriteByte(digits[i+1]) // 低位在前：86 → 68
		sb.WriteByte(digits[i])
	}
	return strings.ToUpper(sb.String())
}

// gsm7Encode 把正文编成七位组序列；只要有**一个**字符编不了就整体放弃（返回 ok=false）。
//
// 整条一起决定是关键：混着来只会把正文割裂（而且 DCS 只能有一个）。
func gsm7Encode(text string) ([]byte, bool) {
	out := make([]byte, 0, len(text))
	for _, r := range text {
		if r == 0x1B {
			return nil, false // 转义前缀本身不能当普通字符编
		}
		if code, ok := smsGSM7Index[r]; ok {
			out = append(out, code)
			continue
		}
		if code, ok := smsGSM7ExtCode[r]; ok {
			out = append(out, 0x1B, code) // 扩展字符占两个七位组
			continue
		}
		return nil, false // 表外字符（中文等）→ 调用方退 UCS2
	}
	return out, true
}

// PackSeptets 按 GSM 03.40 把七位组打包成字节（低位先出，跨字节续写）。
//
//	"OK"    → 4F 4B → CF 25
//	"HELLO" → 48 45 4C 4C 4F → C8 22 93 F9 04
func PackSeptets(septets []byte) []byte {
	out := make([]byte, 0, (len(septets)*7+7)/8)
	var acc uint32
	var bits int
	for _, s := range septets {
		acc |= uint32(s&0x7F) << bits
		bits += 7
		for bits >= 8 {
			out = append(out, byte(acc&0xFF))
			acc >>= 8
			bits -= 8
		}
	}
	if bits > 0 {
		out = append(out, byte(acc&0xFF))
	}
	return out
}

// string2UTF16BE 把字符串转成 UTF-16BE 字节（UCS2 正文用）。
// 用 []rune 而不是 []uint16 是为了正确处理 BMP 外的字符（代理对）。
func string2UTF16BE(s string) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, r := range s {
		if r > 0xFFFF {
			r -= 0x10000
			hi := 0xD800 + (r >> 10)
			lo := 0xDC00 + (r & 0x3FF)
			out = append(out, byte(hi>>8), byte(hi), byte(lo>>8), byte(lo))
			continue
		}
		out = append(out, byte(r>>8), byte(r))
	}
	return out
}
