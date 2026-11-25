package atparse

import (
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var reCCLK = regexp.MustCompile(`\+CCLK:\s*"(\d{2})/(\d{2})/(\d{2}),(\d{2}):(\d{2}):(\d{2})([+-]\d{2})?"`)

// ParseCCLK 解析模组时钟。
//
// 3GPP 语义：不带后缀时按 UTC 理解；带 +/-NN（15 分钟为单位）时表示“报的是本地时间”，
// 换算成 UTC 要减掉。
//
// ⚠️ 但“报的是本地时间”并不是所有固件都守规矩：实测 **RM500U-CNV 会带上 +32 后缀，
// 可报的值本身就是 UTC**，按标准减 8 小时会净差 8 小时（skew 算出 -28798 秒）。
// 这种“命令一样、语义不一样”的机型差异统一登记在 atcmd.go 里
// （IgnoreCCLKTZ），命中时就不理会那个后缀。
//
// 返回：原始文本、unix 秒、与主机 UTC 的偏差秒、是否解析成功。
func ParseCCLK(resp []string, now time.Time, ignoreTZ bool) (raw string, unix int64, skew int64, ok bool) {
	for _, line := range resp {
		m := reCCLK.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		yy, _ := strconv.Atoi(m[1])
		mm, _ := strconv.Atoi(m[2])
		dd, _ := strconv.Atoi(m[3])
		hh, _ := strconv.Atoi(m[4])
		mi, _ := strconv.Atoi(m[5])
		ss, _ := strconv.Atoi(m[6])
		t := time.Date(2000+yy, time.Month(mm), dd, hh, mi, ss, 0, time.UTC)
		// 旧式时区字段：以 15 分钟为单位的有符号数（+32 = UTC+8）。
		if m[7] != "" && !ignoreTZ {
			quarters, err := strconv.Atoi(m[7])
			if err == nil {
				t = t.Add(-time.Duration(quarters) * 15 * time.Minute)
			}
		}
		unix = t.Unix()
		skew = unix - now.UTC().Unix()
		return strings.Trim(m[0][strings.Index(m[0], `"`)+1:], `"`), unix, skew, true
	}
	return "", 0, 0, false
}

var reCCID = regexp.MustCompile(`(?i)[+]?(?:MY)?I?CCID[?]?:\s*"?([0-9A-Fa-f]+)"?`)

var reCPIN = regexp.MustCompile(`\+CPIN:\s*(.+)$`)

var reCNUM = regexp.MustCompile(`\+CNUM:\s*"[^"]*"\s*,\s*"([^"]*)"`)

var reCPBS = regexp.MustCompile(`\+CPBS:\s*"([^"]*)"`)

var reCPBR = regexp.MustCompile(`\+CPBR:\s*\d+\s*,\s*"([^"]+)"`)

var reCRSM = regexp.MustCompile(`\+CRSM:\s*(\d+)\s*,\s*(\d+)\s*(?:,\s*"([0-9A-Fa-f]*)"\s*)?`)

// ParseICCID 从 AT+CCID 应答里取 ICCID（实测 20 位数字）
func ParseICCID(resp []string) string {
	for _, line := range resp {
		if m := reCCID.FindStringSubmatch(line); m != nil {
			return strings.ToUpper(m[1])
		}
	}
	return ""
}

// ParseIMSI 取 IMSI。AT+CIMI 直接回一行裸数字，也可能带 +CIMI: 前缀。
func ParseIMSI(resp []string) string {
	for _, line := range resp {
		s := strings.TrimSpace(line)
		if i := strings.Index(s, ":"); i >= 0 {
			s = strings.TrimSpace(s[i+1:])
		}
		s = strings.Trim(s, `"`)
		if len(s) >= 14 && len(s) <= 16 && IsAllDigits(s) {
			return s
		}
	}
	return ""
}

// ParseCPIN 取 SIM 卡状态原文
func ParseCPIN(resp []string) string {
	for _, line := range resp {
		if m := reCPIN.FindStringSubmatch(line); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// ParseCNUM 取本机号码（拿不到返回空串）
func ParseCNUM(resp []string) string {
	for _, line := range resp {
		if m := reCNUM.FindStringSubmatch(line); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// ParseCPBRNumber 取电话本条目里的号码（`+CPBR: 1,"+8613800138000",129,""`）
func ParseCPBRNumber(resp []string) string {
	for _, line := range resp {
		if m := reCPBR.FindStringSubmatch(line); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// ParseCPBSStorage 取电话本当前存储区（"SM"/"ON"/…）
func ParseCPBSStorage(resp []string) string {
	for _, line := range resp {
		if m := reCPBS.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// ParseCRSM 取 (sw1, sw2, 应答数据, 是否匹配到应答行)
func ParseCRSM(resp []string) (sw1, sw2 int, data string, ok bool) {
	for _, line := range resp {
		m := reCRSM.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		a, err1 := strconv.Atoi(m[1])
		b, err2 := strconv.Atoi(m[2])
		if err1 != nil || err2 != nil {
			continue
		}
		return a, b, m[3], true
	}
	return 0, 0, "", false
}

// CRSMOK 判断 APDU 是否成功（SW1SW2 = 9000）
func CRSMOK(sw1, sw2 int) bool { return sw1 == 0x90 && sw2 == 0x00 }

// CRSMFileUnavailable 判断错误码是不是“这个文件读不了/不存在”
// （SW1 0x94 = 文件/状态错误，0x6A = 参数错误，例如 6A82 文件找不到）。
// 用于记住该卡/该固件读不了，避免每次读状态都白试几轮 APDU。
func CRSMFileUnavailable(sw1, _ int) bool { return sw1 == 0x94 || sw1 == 0x6A }

// BCDDigits 把“半字节交换”的 BCD 字节串解成数字（EF_ICCID / EF_MSISDN 都是这个规则）。
//
// 每个字节低位在前：0x98 → "89"；遇到 0xF 填充或非数字半字节就停（剩下的 CCI/Ext 不属于号码）。
// 实测校验：EF_ICCID 读到 `98683042402055991325` → `89860324040255993152`，与 AT+CCID 完全一致。
func BCDDigits(raw []byte) string {
	var sb strings.Builder
	sb.Grow(len(raw) * 2)
	for _, c := range raw {
		for _, n := range []byte{c & 0x0F, c >> 4} {
			if n > 9 {
				return sb.String()
			}
			sb.WriteByte('0' + n)
		}
	}
	return sb.String()
}

// DecodeEFMSISDN 解析 EF_MSISDN（0x6F40）的透明文件内容：
//
//	<alpha 标识长度(1)><alpha 标识><号码长度(1，含 TON+NPI)><TON(1)><NPI(1)><BCD 号码><CCI?><Ext?>
//
// TON 的 bit6~4 = 001（0x9x）表示国际号码 → 前面补 `+`；其它（如 0x81 未知）按原样给数字。
// 号码不足 5 位或解不出数字就返回空串（宁可不显示，也不显示半截号码）。
func DecodeEFMSISDN(hexStr string) string {
	raw, err := hex.DecodeString(strings.TrimSpace(hexStr))
	if err != nil || len(raw) < 4 {
		return ""
	}
	off := 1 + int(raw[0]) // 跳过 <alpha 长度> + <alpha 标识>
	if off+2 >= len(raw) {
		return ""
	}
	numLen := int(raw[off]) // 含 TON + NPI 的字节数
	off++
	if numLen < 3 {
		return ""
	}
	ton := raw[off]
	end := off + numLen
	if end > len(raw) {
		end = len(raw)
	}
	digits := BCDDigits(raw[off+2 : end]) // 跳过 TON / NPI
	if len(digits) < 5 {
		return ""
	}
	if ton&0x70 == 0x10 { // bit6~4 = 001：国际号码
		return "+" + digits
	}
	return digits
}

// IMSIPLMN 从 IMSI 前几位推 MCC+MNC。
//
// ⚠️ MNC 长度是 2 位还是 3 位**取决于 MCC**（不是全球统一）：中国/日本/欧洲等
// 是 2 位（实测中国电信 IMSI 460115089161379 → 460 + 11），北美与加勒比多为 3 位。
// 这里按 ITU 的“3 位 MNC”MCC 名单区分，其余按 2 位。
// 展示上它只是“印证”：真正权威的是网络侧 PLMN（AT+QNWINFO）。
func IMSIPLMN(imsi string) (mcc, mnc string) {
	if len(imsi) < 6 || !IsAllDigits(imsi) {
		return "", ""
	}
	mcc = imsi[:3]
	if mncLen3[mcc] {
		return mcc, imsi[3:6]
	}
	return mcc, imsi[3:5]
}

// mncLen3 使用 3 位 MNC 的 MCC（主要在美洲）
var mncLen3 = map[string]bool{
	"302": true, "310": true, "311": true, "312": true, "313": true, "314": true,
	"315": true, "316": true, "330": true, "332": true, "334": true, "338": true,
	"342": true, "344": true, "346": true, "348": true, "350": true, "352": true,
	"354": true, "356": true, "358": true, "360": true, "362": true, "363": true,
	"364": true, "365": true, "366": true, "368": true, "370": true, "372": true,
	"374": true, "376": true,
}

// StatusText 状态码 → 中文（3GPP TS 27.007 <m>）
func StatusText(m int) string {
	switch m {
	case 0:
		return "应答完成"
	case 1:
		return "需要进一步操作（网络等待输入）"
	case 2:
		return "会话被网络结束"
	case 3:
		return "其它本地客户端已响应"
	case 4:
		return "操作不被支持"
	case 5:
		return "网络超时"
	}
	return "未知状态"
}

var reCode = regexp.MustCompile(`^[0-9*#+]{1,64}$`)

// ValidCode 校验 USSD 码：只允许数字与 * # +，长度 1~64。
// 它会拼进 AT 指令的参数里，所以必须严格限制（防止注入别的指令）。
func ValidCode(code string) bool {
	return reCode.MatchString(strings.TrimSpace(code))
}
