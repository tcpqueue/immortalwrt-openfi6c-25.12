package common

// ====================== AT 文本工具（厂商层与内核共用） ======================
//
// 这些函数只跟"AT 应答的文本形状"有关，与模组状态、串口、内核类型全都无关：
//
//	· 字段切分 / 宽松取整 / 哨兵值过滤
//	· 频段号的展示与反解（"NR5G BAND 78" → n78）
//	· AT^SYSCFGEX 的原始字段解析（鼎桥 MT5700 与展锐共用同一格式）
//
// 厂商包（module/tdtech 等）要用它们，所以必须住在**不依赖内核**的这一层。
// kernel 里保留搬家前的老名字（BandLabel / atoiOr0 / dropSentinel …）作为别名，
// 见 module/kernel/atutil_alias.go，调用点一个都不用改。

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"modemd/iface"
)

// ---- 字段切分 / 数值取整 ----

// SplitATFields 按逗号切分 AT 字段，去掉空白与引号；"-" 表示未上报，原样保留。
func SplitATFields(s string) []string {
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(strings.Trim(strings.TrimSpace(parts[i]), `"`))
	}
	return parts
}

// AtoiOr0 宽松取整：取不到（空串/非数字）返回 0
func AtoiOr0(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(strings.Trim(s, `"`)))
	if err != nil {
		return 0
	}
	return n
}

// DropSentinel 处理"未测到"的哨兵值并做量纲校验：
// 无效值（-1256/-348/-188 这种）与越界值一律当未上报，不显示可疑数字。
func DropSentinel(v, invalidAt, lo, hi int) int {
	if v <= invalidAt {
		return 0
	}
	if v < lo || v > hi {
		return 0
	}
	return v
}

// IsDigitsAndCommas 判断一行是不是"纯数字 + 逗号/空格"。
//
// 用途：从长应答里挑出真正的数据行 —— URC（^RSSI/^HCSQ/+CMTI…）、OK、ERROR、
// 指令回显都带字母或符号，会被排除。MT5700 的锁网应答就这么筛。
func IsDigitsAndCommas(s string) bool {
	if s == "" {
		return false
	}
	hasDigit := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == ',' || r == ' ':
		default:
			return false
		}
	}
	return hasDigit
}

// ---- 频段号与频段文本 ----

// reBandNum 频段串尾部的数字："NR5G BAND 78" → 78
var reBandNum = regexp.MustCompile(`(\d+)\s*$`)

// BandLabel 频段号的展示形式：NR 用 n78，LTE 用 B5。
func BandLabel(tech iface.CellTech, band int) string {
	if band <= 0 {
		return ""
	}
	if tech == iface.CellTechNR {
		return "n" + strconv.Itoa(band)
	}
	return "B" + strconv.Itoa(band)
}

// BandNumFromText 从 "NR5G BAND 78" / "LTE BAND 5" 里取出频段号
func BandNumFromText(s string) int {
	m := reBandNum.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// TechFromBandText 从频段串判断接入技术（"NR5G BAND 78" / "LTE BAND 3"）
func TechFromBandText(s string) iface.CellTech {
	up := strings.ToUpper(s)
	switch {
	case strings.Contains(up, "NR"):
		return iface.CellTechNR
	case strings.Contains(up, "LTE"):
		return iface.CellTechLTE
	case strings.Contains(up, "WCDMA"), strings.Contains(up, "UMTS"):
		return iface.CellTechWCDMA
	case strings.Contains(up, "GSM"):
		return iface.CellTechGSM
	}
	return ""
}

// ---- AT^SYSCFGEX（鼎桥 MT5700 / 展锐共用）----

// reLTEBandName 从 `AT^SYSCFGEX=?` 应答里抓 "LTE BC<N>" 名字
var reLTEBandName = regexp.MustCompile(`(?i)LTE\s*BC(\d+)`)

// SyscfgEX AT^SYSCFGEX? 的原始字段（band/lteband 都是**大十六进制位图**，
// 原样保留不做数值转换，改写时按手册要求整串带回去）。
type SyscfgEX struct {
	AcqOrder  string
	Band      string
	Roam      int
	SrvDomain int
	LTEBand   string
}

// ParseSyscfgEX 解析 `^SYSCFGEX: "080302",2000000680380,1,2,1E200000095`
func ParseSyscfgEX(resp []string) (SyscfgEX, bool) {
	for _, line := range resp {
		i := strings.Index(strings.ToUpper(line), "^SYSCFGEX:")
		if i < 0 {
			continue
		}
		f := SplitATFields(strings.TrimSpace(line[i+len("^SYSCFGEX:"):]))
		if len(f) < 5 {
			return SyscfgEX{}, false
		}
		roam, err1 := strconv.Atoi(f[2])
		dom, err2 := strconv.Atoi(f[3])
		if err1 != nil || err2 != nil {
			return SyscfgEX{}, false
		}
		return SyscfgEX{
			AcqOrder:  strings.Trim(f[0], `"`),
			Band:      f[1],
			Roam:      roam,
			SrvDomain: dom,
			LTEBand:   f[4],
		}, true
	}
	return SyscfgEX{}, false
}

// ParseLTEBandNames 从 `AT^SYSCFGEX=?` 应答里抓 `"LTE BC1/LTE BC3/…"` 名单。
// 实测/手册样例（两者逐字一致）：
//
//	^SYSCFGEX: ("01","02","03","08"),((2000000400380,"GSM900/GSM1800/WCDMA900/WCDMA2100"),
//	(280000,"GSM850/GSM1900"),(3fffffff,"All bands")),(0-2),(0-4),
//	((1e200000095,"LTE BC1/LTE BC3/LTE BC5/LTE BC8/LTE BC34/LTE BC38/LTE BC39/LTE BC40/LTE BC41"),
//	(7fffffffffffffff,"All bands"))
//
// ⚠️ 只取带名字的那一组；"All bands"（7fffffffffffffff）不是频段号，不能当成能力列表。
func ParseLTEBandNames(resp []string) []int {
	seen := map[int]bool{}
	var lte []int
	for _, line := range resp {
		for _, mm := range reLTEBandName.FindAllStringSubmatch(line, -1) {
			n, err := strconv.Atoi(mm[1])
			if err != nil || n <= 0 || seen[n] {
				continue
			}
			seen[n] = true
			lte = append(lte, n)
		}
	}
	sort.Ints(lte)
	return lte
}
