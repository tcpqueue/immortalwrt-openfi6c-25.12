package common

// ====================== 「标签: 值」应答的通用解析 ======================
//
// 这些原来都在 module/cell/vendor_common.go 里（只给三家厂商的私有格式用）。
// 按品牌拆目录之后，品牌包（module/thales、module/sierra …）各拿各的解析器，
// 而 cell 又要反过来 import 不了品牌包 —— 所以工具必须住在叶子包 common。
// module/cell/vendor_common.go 保留同名别名，cell.go 与老测试不用改。

import (
	"strconv"
	"strings"
)

// ParseLabeledKV 把"标签: 值"行拆成 map（键小写去空格，值去空格，`---`/空值丢弃）。
//
// 多家（Thales / Foxconn / Sierra）都是这种写法，值里可能带单位（`-95 dBm`），
// 取值时统一用 FirstNumber 抠数字。
func ParseLabeledKV(resp []string) map[string]string {
	out := make(map[string]string, len(resp))
	for _, line := range resp {
		s := strings.TrimSpace(line)
		if s == "" || strings.EqualFold(s, "OK") {
			continue
		}
		i := strings.Index(s, ":")
		if i <= 0 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(s[:i]))
		v := strings.TrimSpace(s[i+1:])
		if v == "" || v == "---" {
			continue
		}
		out[k] = v
	}
	return out
}

// FirstNumber 抠出字符串里的第一个（带符号/小数）数字，取整返回。
//
// Thales/Foxconn 的 `AT^DEBUG` 值里可能带多余内容（`lte_band: 3,20MHz`、`mcc: 460,11`），
// 脚本一律取**第一个**数字（`awk -F',' '{print $1}'`）⇒ 这里同样只取第一个。
func FirstNumber(s string) (int, bool) {
	nums := AtNumbersSigned(s)
	if len(nums) == 0 {
		return 0, false
	}
	return int(nums[0]), true
}

// AvgNumber 多个读数取平均 —— 只给**信号值**用：
// QModem 的 process_signal_value() 对一行里多个读数就是求和取平均
// （如 `rsrp: -95,-96,-94`）。小区字段（band/channel/pci）绝不平均。
func AvgNumber(s string) (int, bool) {
	nums := AtNumbersSigned(s)
	if len(nums) == 0 {
		return 0, false
	}
	sum := 0.0
	for _, f := range nums {
		sum += f
	}
	return int(sum / float64(len(nums))), true
}

// AtNumbersSigned 抠出字符串里所有带符号/小数的数字（`-95.5 dBm` → [-95.5]）
func AtNumbersSigned(s string) []float64 {
	var out []float64
	i := 0
	for i < len(s) {
		c := s[i]
		if (c >= '0' && c <= '9') || ((c == '-' || c == '+') && i+1 < len(s) && (s[i+1] >= '0' && s[i+1] <= '9')) {
			j := i + 1
			for j < len(s) && ((s[j] >= '0' && s[j] <= '9') || s[j] == '.') {
				j++
			}
			if v, err := strconv.ParseFloat(s[i:j], 64); err == nil {
				out = append(out, v)
			}
			i = j
			continue
		}
		i++
	}
	return out
}

// KVInt 从 map 里按候选键顺序取第一个能解析的数字
func KVInt(kv map[string]string, keys ...string) (int, bool) {
	for _, k := range keys {
		if v, ok := kv[k]; ok {
			if n, ok := FirstNumber(v); ok {
				return n, true
			}
		}
	}
	return 0, false
}

// KVStr 取字符串值
func KVStr(kv map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := kv[k]; ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// KVAvgInt 信号值专用：多个读数取平均（见 AvgNumber）
func KVAvgInt(kv map[string]string, keys ...string) (int, bool) {
	for _, k := range keys {
		if v, ok := kv[k]; ok {
			if n, ok := AvgNumber(v); ok {
				return n, true
			}
		}
	}
	return 0, false
}

// ParseInlinePairs 解析"同一行里用逗号分隔的多个 `键: 值`"。
//
// 用于 Telit 的 `PCC- BandClass: 3 , BW: 20 , CH: 1650 , PCI: 301 , RSRP: -95 …`
// 与高新兴的 `1234,cellid:12AB34,pci:301,band:3`。
// 没有冒号的片段（如高新兴的首格 TAC）不进 map，由调用方自己取。
func ParseInlinePairs(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(part)
		i := strings.Index(p, ":")
		if i <= 0 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(p[:i]))
		// 去掉载波前缀（`PCC- BandClass` → `bandclass`、`SCC0- RSRP` → `rsrp`）
		if j := strings.Index(k, "-"); j >= 0 && j < 6 {
			k = strings.TrimSpace(k[j+1:])
		}
		v := strings.TrimSpace(p[i+1:])
		if v == "" || v == "---" {
			continue
		}
		out[k] = v
	}
	return out
}

// CSVAfterPrefix 取第一条以 prefix 开头的应答行的 CSV 字段（大小写不敏感）
func CSVAfterPrefix(resp []string, prefix string) ([]string, string, bool) {
	up := strings.ToUpper(prefix)
	for _, line := range resp {
		s := strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(s), up) {
			continue
		}
		i := strings.Index(s, ":")
		if i < 0 {
			continue
		}
		return SplitATFields(strings.TrimSpace(s[i+1:])), s, true
	}
	return nil, "", false
}

// NormalizePLMN 去掉 PLMN 里的引号/空格（Thales 的 `mcc: 460,11` 会带上逗号后的东西）
func NormalizePLMN(s string) string {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), `"`))
	if i := strings.IndexAny(s, ", "); i > 0 {
		s = s[:i]
	}
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ValidPLMN 校验 MCC/MNC 像不像真的。
//
// ⚠️ 只挡 MCC：MNC 的 "00"/"0" 是**合法值**（46000 = 中国移动），
// 而 NR5G-NSA 未连接时模组会回一整行 0（`+QENG: "NR5G-NSA",0,0,0,…`）——
// 那种行的 MCC 是单位数 "0"，在这里就被拦住。
func ValidPLMN(mcc, mnc string) bool {
	if len(mcc) != 3 || !AllDigits(mcc) || allZero(mcc) {
		return false
	}
	return len(mnc) >= 1 && len(mnc) <= 3 && AllDigits(mnc)
}

// AllDigits 串是否全为数字（空串返回 false）
func AllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func allZero(s string) bool {
	for _, r := range s {
		if r != '0' {
			return false
		}
	}
	return s != ""
}

// DashNorm 把下划线统一成连字符（LTE_NR → LTE-NR）。
// meig / thales 的制式名写法不一致，比较前先归一。
func DashNorm(s string) string { return strings.ReplaceAll(s, "_", "-") }
