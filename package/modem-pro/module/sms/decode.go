package sms

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// SmsErrorLine 取出应答里的错误行（`ERROR` / `+CMS ERROR: xxx`）。
// 正文行可能是很长的十六进制串，不要把它带进错误信息。
func SmsErrorLine(resp []string) string {
	for _, line := range resp {
		u := strings.ToUpper(strings.TrimSpace(line))
		if u == "ERROR" || strings.HasPrefix(u, "+CME ERROR") || strings.HasPrefix(u, "+CMS ERROR") {
			return strings.TrimSpace(line)
		}
	}
	return "ERROR"
}

var ErrNotSMS = errors.New("unsupported PDU type")

const (
	AlphabetGSM7 = iota
	Alphabet8Bit
	AlphabetUCS2
)

// SmsAlphabet 从 DCS 判断字符集（TS 23.038 §4）。
// 0x00~0x0F 是"普通编码"，0xF0~0xFF 是带消息类的同一套编码，两者取 bit3-2；
// 其余取值（压缩位、reserved）按 7 位处理 —— 实测遇到的都是前两类。
func SmsAlphabet(dcs byte) int {
	switch {
	case dcs&0xC0 == 0x00, dcs&0xF0 == 0xF0:
		switch (dcs >> 2) & 0x03 {
		case 0x01:
			return Alphabet8Bit
		case 0x02:
			return AlphabetUCS2
		}
	}
	return AlphabetGSM7
}

// DecodePDU 解码一条 PDU（SMS-DELIVER / SMS-SUBMIT）。
// 返回的 SMS 只填 Number / Timestamp / Content 与分条信息，Index / Status 由调用方
// 从 `+CMGL:` 头里取（PDU 本体不带存储位置）。
func DecodePDU(pdu string) (SMS, ConcatInfo, error) {
	b, ok := HexToBytes(strings.ToUpper(strings.TrimSpace(pdu)))
	if !ok || len(b) < 2 {
		return SMS{}, ConcatInfo{}, fmt.Errorf("pdu 不是合法的十六进制串")
	}
	// ① 短信中心地址：1 字节长度（含类型字节）+ 该长度个字节；长度为 0 表示没带
	p := 1 + int(b[0])
	if p >= len(b) {
		return SMS{}, ConcatInfo{}, fmt.Errorf("pdu 在短信中心地址处被截断")
	}
	// ② 第一字节：TP-MTI（bit0-1）、TP-UDHI（bit6）
	fo := b[p]
	p++
	mti, udhi := fo&0x03, fo&0x40 != 0
	// ③ 地址：DELIVER 是发件人 OA，SUBMIT 是收件人 DA
	num, np, err := decodePDUAddress(b, p)
	if err != nil {
		return SMS{}, ConcatInfo{}, err
	}
	p = np
	out := SMS{Number: num}
	// ④ SUBMIT 比 DELIVER 多一个 TP-MR，且没有 SCTS、多一个有效期 VP（长度看 FO 的 VPF）
	vpLen := 0
	switch mti {
	case 0x00: // SMS-DELIVER
	case 0x01: // SMS-SUBMIT
		if p >= len(b) {
			return SMS{}, ConcatInfo{}, fmt.Errorf("pdu 在 TP-MR 处被截断")
		}
		p++
		switch (fo >> 3) & 0x03 {
		case 0x01, 0x03:
			vpLen = 7 // 增强格式 / 绝对格式
		case 0x02:
			vpLen = 1 // 相对格式
		}
	default:
		return SMS{}, ConcatInfo{}, ErrNotSMS
	}
	// ⑤ TP-PID + TP-DCS
	if p+1 >= len(b) {
		return SMS{}, ConcatInfo{}, fmt.Errorf("pdu 在 PID/DCS 处被截断")
	}
	p++ // TP-PID 用不上
	dcs := b[p]
	p++
	// ⑥ TP-SCTS（只有 DELIVER 有）+ SUBMIT 的有效期
	if mti == 0x00 {
		if p+7 > len(b) {
			return SMS{}, ConcatInfo{}, fmt.Errorf("pdu 在 TP-SCTS 处被截断")
		}
		out.Timestamp = PduTime(b[p : p+7])
		p += 7
	}
	p += vpLen
	// ⑦ TP-UDL + TP-UD
	if p >= len(b) {
		return SMS{}, ConcatInfo{}, fmt.Errorf("pdu 在 UDL 处被截断")
	}
	udl := int(b[p])
	p++
	ud := b[p:]

	// ⑧ 拼接信息：直接复用长短信那条路径的 UDH 解析（它读的就是整条 PDU）
	var ci ConcatInfo
	if mti == 0x00 {
		if c, ok := ParseConcatUDH(b); ok {
			ci = c
		}
	}

	// ⑨ 正文：UCS2 / 8 位数据按字节，7 位要按 septet 解包（UDH 占的位置要丢掉）
	data := ud
	udhBytes := 0
	if udhi && len(ud) > 0 {
		udhBytes = 1 + int(ud[0]) // UDHL 含自己
		if udhBytes > len(ud) {
			return out, ci, fmt.Errorf("pdu 的 UDH 长度越界")
		}
		data = ud[udhBytes:]
	}
	switch SmsAlphabet(dcs) {
	case AlphabetUCS2:
		out.Content = DecodeUCS2(data)
	case Alphabet8Bit:
		out.Content = strings.ToUpper(HexString(data))
	default:
		seps := unpack7Bit(ud, udl)
		if skip := SeptetPad(udhBytes); skip > 0 {
			if skip < len(seps) {
				seps = seps[skip:]
			} else {
				seps = nil
			}
		}
		out.Content = decodeGSM7(seps)
	}
	return out, ci, nil
}

// decodePDUAddress 解码 PDU 里的地址字段：1 字节"数字个数" + 1 字节类型 + 若干 BCD 半字节。
//
// ⚠️ 号码前**不**无脑补 "+"：3GPP 是 TON（类型字节 bit6-4）= 001 才算国际号。
// 实测中国电信的短信中心把 0xA1 / 0xA0（TON=010 national）也用在号码上，
// 按"bit7=1 就补 +"会导致 "+10001" 这种假号码。
func decodePDUAddress(b []byte, p int) (string, int, error) {
	if p+1 >= len(b) {
		return "", p, fmt.Errorf("pdu 在地址字段处被截断")
	}
	nDigits := int(b[p])
	toa := b[p+1]
	p += 2
	if nDigits == 0 {
		return "", p, nil // 空地址：状态报告没有号码
	}
	if nDigits > 20 {
		return "", p, fmt.Errorf("pdu 地址长度异常: %d", nDigits)
	}
	nBytes := (nDigits + 1) / 2
	if p+nBytes > len(b) {
		return "", p, fmt.Errorf("pdu 在地址数字处被截断")
	}
	var sb strings.Builder
	for i := 0; i < nBytes; i++ {
		lo, hi := b[p+i]&0x0F, (b[p+i]>>4)&0x0F
		// 每个字节低半字节是前一位数字
		if d, ok := bcdDigit(lo); ok {
			sb.WriteByte(d)
		}
		if d, ok := bcdDigit(hi); ok {
			sb.WriteByte(d)
		}
	}
	num := sb.String()
	if (toa>>4)&0x07 == 0x01 { // TON = 001 国际号
		num = "+" + num
	}
	return num, p + nBytes, nil
}

// bcdDigit 半字节 → 号码字符；0x0F（补位）与越界值返回 false
func bcdDigit(v byte) (byte, bool) {
	switch {
	case v <= 9:
		return '0' + v, true
	case v == 0x0A:
		return '*', true
	case v == 0x0B:
		return '#', true
	}
	return 0, false
}

// bcdPair 一个字节 → 两位数字符串（低半字节在前）；含非法半字节返回 false
func bcdPair(x byte) (string, bool) {
	lo, hi := x&0x0F, (x>>4)&0x0F
	if lo > 9 || hi > 9 {
		return "", false
	}
	return string([]byte{'0' + lo, '0' + hi}), true
}

// PduTime 把 7 字节 BCD 的 TP-SCTS 转成与文本模式一致的 "YY/MM/DD,HH:MM:SS+32"。
//
// 时区（第 7 字节）单位是 15 分钟：**bit3 是符号位**（TS 23.040 §9.2.3.11），
// 量值仍是 BCD 两位（低半字节在前），所以负偏移要把低半字节的符号位掩掉再取数字：
// 0x32 → +32（+8:00），0x3A → -32（-8:00）。
//
// 解析失败（含非 BCD 值）返回空串 —— 上层按"时间未知"排在列表最后，而不是显示错时间。
func PduTime(b []byte) string {
	if len(b) < 7 {
		return ""
	}
	parts := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		p, ok := bcdPair(b[i])
		if !ok {
			return ""
		}
		parts = append(parts, p)
	}
	tz := ""
	// 量值也是 BCD，但**低半字节是十位、高半字节是个位**（与前面几个字段同规则），
	// 且 bit3 是符号位 —— 所以十位要先用 0x07 掩掉符号位
	mag := int(b[6]&0x07)*10 + int(b[6]>>4)
	if mag <= 56 {
		sign := "+"
		if b[6]&0x08 != 0 {
			sign = "-"
		}
		tz = fmt.Sprintf("%s%02d", sign, mag)
	}
	return fmt.Sprintf("%s/%s/%s,%s:%s:%s%s",
		parts[0], parts[1], parts[2], parts[3], parts[4], parts[5], tz)
}

// unpack7Bit 把 LSB 在前的 7 位压缩数据解成 n 个 septet（每格 0~0x7F）
func unpack7Bit(b []byte, n int) []byte {
	out := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		bit := i * 7
		idx, off := bit/8, uint(bit%8)
		var v uint16
		if idx < len(b) {
			v = uint16(b[idx]) >> off
		}
		if idx+1 < len(b) {
			v |= uint16(b[idx+1]) << (8 - off)
		}
		out = append(out, byte(v&0x7F))
	}
	return out
}

// SeptetPad UDH 占用 n 字节时，7 位流里被它"顶掉"的 septet 数（向上取整）
func SeptetPad(nBytes int) int {
	bits := nBytes * 8
	if bits%7 == 0 {
		return bits / 7
	}
	return bits/7 + 1
}

// decodeGSM7 把 septet 序列按 GSM 03.38 默认字母表还原成文本（0x1B 走扩展表）
func decodeGSM7(seps []byte) string {
	var sb strings.Builder
	for i := 0; i < len(seps); i++ {
		c := seps[i]
		if c == 0x1B {
			if i+1 < len(seps) {
				if r, ok := Gsm7Ext[seps[i+1]]; ok {
					sb.WriteRune(r)
				}
				i++
			}
			continue
		}
		if int(c) < len(Gsm7Default) {
			sb.WriteRune(Gsm7Default[c])
		}
	}
	// 7 位数据的末尾常见补位 CR，不是正文
	return strings.TrimRight(sb.String(), "\r")
}

// DecodeUCS2 按 UTF-16BE 解码（含代理对）；尾部补位/畸形半格丢掉
func DecodeUCS2(b []byte) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
	}
	return string(utf16.Decode(u))
}

// HexString 字节 → 大写十六进制
func HexString(b []byte) string {
	const hexDigits = "0123456789ABCDEF"
	var sb strings.Builder
	sb.Grow(len(b) * 2)
	for _, c := range b {
		sb.WriteByte(hexDigits[c>>4])
		sb.WriteByte(hexDigits[c&0x0F])
	}
	return sb.String()
}

// ParsePDUList 解析 PDU 模式（AT+CMGF=0 + AT+CMGL=4）的应答。
//
// 返回两条：短信列表，以及「存储位置 → 拼接信息」（交给 GroupSMSConcat 合成长短信）。
// 解码失败的单条（状态报告等）仍然保留"有头无正文"的条目 —— 与文本模式的行为一致，
// 不会让整条记录凭空消失。
//
// ⚠️ 应答里会混进 URC（实测 MT5700 每秒刷 ^RSSI/^CERSSI/^HCSQ）：非十六进制的行直接跳过，
// 并且**只有**消费掉一条 PDU 之后才认下一段，因此夹在中间的上报不会污染配对。
func ParsePDUList(resp []string) ([]SMS, map[string]ConcatInfo) {
	list := []SMS{}
	concat := map[string]ConcatInfo{}
	cur := -1 // 当前等待 PDU 的条目下标；-1 表示不在配对状态
	for _, line := range resp {
		if idx := strings.Index(strings.ToUpper(line), "+CMGL:"); idx >= 0 {
			f := SplitCMGLFields(line[idx+len("+CMGL:"):])
			cur = -1
			if len(f) == 0 || f[0] == "" {
				continue
			}
			s := SMS{Index: f[0]}
			if len(f) > 1 {
				s.Status = f[1]
			}
			list = append(list, s)
			cur = len(list) - 1
			continue
		}
		if cur < 0 {
			continue
		}
		t := strings.TrimSpace(line)
		if t == "" || !reHexOnly.MatchString(t) || len(t)%2 != 0 {
			continue // URC / OK / 空行：跳过，继续等这一条的 PDU
		}
		s, ci, err := DecodePDU(t)
		if err != nil {
			cur = -1
			continue
		}
		s.Index, s.Status = list[cur].Index, list[cur].Status
		list[cur] = s
		if ci.Total >= 2 && ci.Seq >= 1 && ci.Seq <= ci.Total {
			concat[s.Index] = ci
		}
		cur = -1
	}
	return list, concat
}

var reHexOnly = regexp.MustCompile(`^[0-9A-Fa-f]+$`)

// splitCMGLFields 按逗号切分 +CMGL 行字段：引号内的逗号不分割，空字段保留。
func SplitCMGLFields(s string) []string {
	var fields []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ',' && !inQuote:
			fields = append(fields, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(fields, strings.TrimSpace(cur.String()))
}

// ParseCMGLHeader 解析一条 +CMGL 头，字段顺序为：
//
//	<index>,<stat>,<oa/da>,[<alpha>],[<scts>]
func ParseCMGLHeader(line string) (SMS, bool) {
	const head = "+CMGL:"
	idx := strings.Index(strings.ToUpper(line), head)
	if idx < 0 {
		return SMS{}, false
	}
	f := SplitCMGLFields(line[idx+len(head):])
	if len(f) < 2 || f[0] == "" {
		return SMS{}, false
	}
	sms := SMS{Index: f[0], Status: f[1]}
	if len(f) > 2 {
		sms.Number = f[2]
	}
	if len(f) > 4 {
		sms.Timestamp = f[4] // f[3] 是 alpha（别名），实测常为空
	}
	return sms, true
}

// parseSMSList 解析 AT+CMGL 的完整应答。
// 短信内容可能跨多行（正文含换行），一直累加到下一条 +CMGL 或应答结束。
func ParseSMSList(resp []string) []SMS {
	list := []SMS{}
	var cur *SMS
	flush := func() {
		if cur != nil {
			cur.Content = DecodeSMSText(cur.Content)
			list = append(list, *cur)
			cur = nil
		}
	}
	for _, line := range resp {
		if sms, ok := ParseCMGLHeader(line); ok {
			flush()
			cur = &sms
			continue
		}
		t := strings.TrimSpace(line)
		if t == "" || t == "OK" || t == "ERROR" || strings.HasPrefix(t, "AT+") ||
			strings.HasPrefix(t, "+CME") || strings.HasPrefix(t, "+CMS") {
			continue
		}
		if cur != nil {
			if cur.Content == "" {
				cur.Content = t
			} else {
				cur.Content += "\n" + t
			}
		}
	}
	flush()
	return list
}

// decodeSMSText 尝试把 UCS2 十六进制内容还原为可读文本。
//
// 实测：本固件在文本模式下，UCS2 编码的短信（中文短信的绝大多数）会以
// "UTF-16BE 的十六进制串"返回，例如 "4F60597D" 即「你好」。
//
// 难点在于：纯数字短信（如 8 位验证码 "12345678"）本身就是"合法的十六进制
// 串"，盲目解码会显示成乱码。因此这里用三重判据，全部满足才解码：
//
//  1. 长度是 4 的倍数且 >= 8
//  2. 每个码元的高字节落在真实文本的取值范围内（见 ucs2HighBytePlausible）
//  3. 解出的文本至少含一个中日韩字符
//
// 判据 2 能挡掉绝大多数数字串：例如 12345678 的码元是 0x1234/0x5678，
// 高字节 0x12 不在白名单内，直接拒绝。
//
// 已知局限：形如 66668888（高字节 0x66/0x88 恰好落在 CJK 区间）的纯数字短信
// 仍会被误判。这类情况无解——文本模式下模组不提供编码信息，除非改用 PDU 模式解析。
func DecodeSMSText(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 8 || len(s)%4 != 0 || !reHexOnly.MatchString(s) {
		return s
	}
	u16 := make([]uint16, 0, len(s)/4)
	for i := 0; i < len(s); i += 4 {
		v, err := strconv.ParseUint(s[i:i+4], 16, 32)
		if err != nil {
			return s
		}
		if !ucs2HighBytePlausible(uint16(v)) {
			return s
		}
		u16 = append(u16, uint16(v))
	}
	runes := utf16.Decode(u16)
	if !containsCJK(runes) {
		return s
	}
	return string(runes)
}

// ucs2HighBytePlausible 判断一个 UTF-16 码元的高字节是否像真实文本。
// 白名单依据真实中文短信会用到的区块：
//
//	0x00        ASCII（数字、字母、英文标点）
//	0x20        普通空格
//	0x30-0x33   CJK 符号与标点（。、【】《》）、注音、假名
//	0x4E-0x9F   中日韩统一表意文字
//	0xFF        全角字符（全角逗号、！等）
//
// 白名单越窄，"纯数字短信被误当十六进制"的概率越低。
func ucs2HighBytePlausible(sym uint16) bool {
	hi := byte(sym >> 8)
	switch {
	case hi == 0x00, hi == 0x20, hi == 0xFF:
		return true
	case hi >= 0x30 && hi <= 0x33:
		return true
	case hi >= 0x4E && hi <= 0x9F:
		return true
	}
	return false
}

// containsCJK 判断是否含中日韩字符
func containsCJK(runes []rune) bool {
	for _, r := range runes {
		switch {
		case r >= 0x2E80 && r <= 0x2EFF, // CJK 部首
			r >= 0x3000 && r <= 0x303F, // CJK 标点
			r >= 0x3400 && r <= 0x4DBF, // 扩展 A
			r >= 0x4E00 && r <= 0x9FFF, // 统一表意文字
			r >= 0xF900 && r <= 0xFAFF, // 兼容表意文字
			r >= 0xFF00 && r <= 0xFFEF: // 全角字符
			return true
		}
	}
	return false
}

// ConcatInfo 一条短信的拼接信息（来自 PDU 模式 UDH 中的 IEI=0x00/0x08）
type ConcatInfo struct {
	Ref   int // 拼接引用号
	Total int // 总段数
	Seq   int // 段序号（1 起）
}

// HexToBytes 把偶数长度的十六进制串转成字节切片
func HexToBytes(s string) ([]byte, bool) {
	if len(s) == 0 || len(s)%2 != 0 {
		return nil, false
	}
	b := make([]byte, len(s)/2)
	for i := range b {
		v, err := strconv.ParseUint(s[i*2:i*2+2], 16, 8)
		if err != nil {
			return nil, false
		}
		b[i] = byte(v)
	}
	return b, true
}

// parseConcatUDH 从一条 SMS-DELIVER 的 PDU 中提取拼接 IE。
// 只解析到 UDH 为止，不去解正文编码（正文由文本模式那条路径负责）。
func ParseConcatUDH(b []byte) (ConcatInfo, bool) {
	// 短信中心地址：1 字节长度 + 该长度个字节
	if len(b) < 1 {
		return ConcatInfo{}, false
	}
	p := 1 + int(b[0])
	if p >= len(b) {
		return ConcatInfo{}, false
	}
	// 第一字节：TP-MTI == 0 表示 SMS-DELIVER；TP-UDHI(0x40) 为 0 表示没有 UDH
	fo := b[p]
	p++
	if fo&0x03 != 0 || fo&0x40 == 0 {
		return ConcatInfo{}, false
	}
	// 发件人地址：1 字节"半字节长度" + 1 字节类型 + ceil(长度/2) 字节号码
	if p >= len(b) {
		return ConcatInfo{}, false
	}
	p += 2 + (int(b[p])+1)/2
	// PID(1) + DCS(1) + SCTS(7) + UDL(1)
	p += 10
	if p >= len(b) {
		return ConcatInfo{}, false
	}
	// UD 起始处的 UDH：1 字节总长 + 若干 IE
	udhl := int(b[p])
	p++
	end := p + udhl
	if udhl == 0 || end > len(b) {
		return ConcatInfo{}, false
	}
	for i := p; i+1 < end; {
		iei, iel := b[i], int(b[i+1])
		body := i + 2
		if body+iel > end {
			break // 残缺 IE，放弃
		}
		switch iei {
		case 0x00: // 8 位引用号的拼接 IE
			if iel >= 3 {
				return ConcatInfo{Ref: int(b[body]), Total: int(b[body+1]), Seq: int(b[body+2])}, true
			}
		case 0x08: // 16 位引用号的拼接 IE
			if iel >= 4 {
				return ConcatInfo{
					Ref:   int(b[body])<<8 | int(b[body+1]),
					Total: int(b[body+2]),
					Seq:   int(b[body+3]),
				}, true
			}
		}
		i = body + iel
	}
	return ConcatInfo{}, false
}

// ParsePDUConcatList 解析 PDU 模式 AT+CMGL=4 的应答，返回「存储位置 → 拼接信息」。
// 只保留确实带拼接 UDH 的条目。
func ParsePDUConcatList(resp []string) map[string]ConcatInfo {
	out := make(map[string]ConcatInfo)
	index := ""
	for _, line := range resp {
		upper := strings.ToUpper(line)
		if idx := strings.Index(upper, "+CMGL:"); idx >= 0 {
			f := SplitCMGLFields(line[idx+len("+CMGL:"):])
			if len(f) > 0 && f[0] != "" {
				index = f[0]
			}
			continue
		}
		t := strings.TrimSpace(line)
		if index == "" || t == "" || t == "OK" || t == "ERROR" ||
			strings.HasPrefix(t, "AT+") || strings.HasPrefix(t, "+CME") || strings.HasPrefix(t, "+CMS") {
			continue
		}
		if b, ok := HexToBytes(t); ok {
			if ci, ok := ParseConcatUDH(b); ok {
				out[index] = ci
			}
		}
		index = "" // 一行 PDU 对应一条记录
	}
	return out
}

// groupSMSConcat 把同一条长短信的各段合并为「父条目 + Parts 子表」。
//
// concat 为空（PDU 模式读取失败/固件不支持）时原样返回，行为退化为旧版平铺列表。
func GroupSMSConcat(list []SMS, concat map[string]ConcatInfo) []SMS {
	if len(concat) == 0 {
		return list
	}
	// 同一 key 下可能有多组：引用号是 8 位，同一发件人不同时间的长短信会撞号，
	// 因此段号重复时另起一组，而不是把两组长短信混在一起。
	type concatGroup struct {
		outIdx int
		seqs   map[int]bool
	}
	pending := map[string][]*concatGroup{}
	out := make([]SMS, 0, len(list))

	for _, s := range list {
		ci, ok := concat[s.Index]
		if !ok || ci.Total < 2 || ci.Seq < 1 || ci.Seq > ci.Total {
			out = append(out, s)
			continue
		}
		s.PartRef, s.PartTotal, s.PartSeq = ci.Ref, ci.Total, ci.Seq

		key := s.Number + "|" + strconv.Itoa(ci.Ref) + "|" + strconv.Itoa(ci.Total)
		var target *concatGroup
		for _, g := range pending[key] {
			if !g.seqs[ci.Seq] {
				target = g
				break
			}
		}
		if target == nil {
			out = append(out, s) // 先以本段作为父条目占位
			target = &concatGroup{outIdx: len(out) - 1, seqs: map[int]bool{}}
			pending[key] = append(pending[key], target)
		}
		target.seqs[ci.Seq] = true
		out[target.outIdx].Parts = append(out[target.outIdx].Parts, SMSPart{
			Index:     s.Index,
			Status:    s.Status,
			Seq:       ci.Seq,
			Timestamp: s.Timestamp,
			Content:   s.Content,
		})
	}

	for i := range out {
		if len(out[i].Parts) < 2 {
			// 只有 1 段（其余段缺失/已删）不算分条：清掉子表，保持普通条目，
			// 但保留 PartTotal/PartSeq，UI 可据此提示"第 N/M 段"。
			out[i].Parts = nil
			continue
		}
		parts := out[i].Parts
		sort.SliceStable(parts, func(a, b int) bool { return parts[a].Seq < parts[b].Seq })
		var sb strings.Builder
		for _, p := range parts {
			sb.WriteString(p.Content)
		}
		out[i].Parts = parts
		out[i].Content = sb.String()  // 父条目正文 = 按段序拼好的全文
		out[i].Index = parts[0].Index // 主位置取第 1 段，便于"删除这一条"的语义
		out[i].Status = parts[0].Status
		out[i].Timestamp = parts[0].Timestamp
		out[i].PartSeq = parts[0].Seq
	}
	return out
}

type SMS struct {
	Index     string `json:"index"`
	Status    string `json:"status"`
	Number    string `json:"number"`
	Timestamp string `json:"timestamp"`
	Content   string `json:"content"`
	// —— 分条（长短信拼接）信息，单条短信时为零值 ——
	PartRef   int       `json:"part_ref,omitempty"`   // 拼接引用号，同一条长短信的各段相同
	PartTotal int       `json:"part_total,omitempty"` // 总段数
	PartSeq   int       `json:"part_seq,omitempty"`   // 当前段序号（1 起）
	Parts     []SMSPart `json:"parts,omitempty"`      // 子表：各段明细（仅成组的长短信有）
}

// SMSPart 分条短信中的一段，用于前端「分条」子表展示
type SMSPart struct {
	Index     string `json:"index"`  // 存储位置，删除该段时用
	Status    string `json:"status"` // 该段状态
	Seq       int    `json:"seq"`    // 段序号（1 起）
	Timestamp string `json:"timestamp,omitempty"`
	Content   string `json:"content"` // 该段正文
}
