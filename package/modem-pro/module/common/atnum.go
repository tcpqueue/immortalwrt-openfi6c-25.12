package common

// ====================== AT 数值 / 应答 / 频段列表 工具 ======================
//
// 全是"与模组状态无关"的纯函数，厂商层（module/lock 等）也要用，
// 所以住在共用层；kernel 保留搬家前的老名字（ParseATInt / atIntAt / AtIntOr0 /
// respHasError / mergeBands / ParseBandList）作为别名。

import (
	"sort"
	"strconv"
	"strings"
)

// ParseATInt 解析 AT 字段整数，支持十进制与十六进制（如 PCID "404A"）。
// 字段为空或 "-"（未上报）时返回 ok=false。
func ParseATInt(s string) (int, bool) {
	s = strings.TrimSpace(strings.Trim(s, `"`))
	if s == "" || s == "-" {
		return 0, false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return int(n), true
	}
	if n, err := strconv.ParseInt(s, 16, 64); err == nil {
		return int(n), true
	}
	return 0, false
}

// IntAt 取第 idx 个字段的整数值
func IntAt(f []string, idx int) (int, bool) {
	if idx < 0 || idx >= len(f) {
		return 0, false
	}
	return ParseATInt(f[idx])
}

// AtIntOr0 取第 idx 个字段的整数值，解析失败返回 0
func AtIntOr0(f []string, idx int) int {
	v, _ := IntAt(f, idx)
	return v
}

// RespHasError 判断 AT 应答里是否带 ERROR。
//
// 注意：内核的 sendATLocked 只在**超时**时返回 error，模组回 ERROR 时 err 为 nil，
// 所以判断"这条指令不被支持"必须看应答内容。
func RespHasError(resp []string) bool {
	for _, line := range resp {
		u := strings.ToUpper(strings.TrimSpace(line))
		if u == "ERROR" || strings.HasPrefix(u, "+CME ERROR") || strings.HasPrefix(u, "+CMS ERROR") {
			return true
		}
	}
	return false
}

// MergeBands 多个频段列表取并集（升序、去重、丢掉 0 与负数）
func MergeBands(lists ...[]int) []int {
	seen := map[int]bool{}
	var out []int
	for _, l := range lists {
		for _, n := range l {
			if n <= 0 || seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// ParseBandList 解析频段列表（冒号或逗号分隔，如 "1:3:5:8"）。
// 空串或 "0" 表示未做限制，返回 ok=false。
func ParseBandList(s string) ([]int, bool) {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), `"`))
	if s == "" || s == "0" {
		return nil, false
	}
	var bands []int
	seen := make(map[int]bool)
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ':' || r == ',' }) {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n <= 0 || seen[n] {
			continue
		}
		seen[n] = true
		bands = append(bands, n)
	}
	if len(bands) == 0 {
		return nil, false
	}
	sort.Ints(bands)
	return bands, true
}
