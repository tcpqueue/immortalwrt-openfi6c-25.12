package usbids

// 本文件由 extract-decls.py 从 kernel 抽出（按名字表 + 括号配平），
// 内容逐字保留，仅把需要跨包使用的名字首字母大写。

import (
	"strings"
)

// NormalizeHexFields 把切出来的字段补成两位十六进制；第一个字段非法就算失败
func NormalizeHexFields(fields []string) (class, sub, proto string, ok bool) {
	norm := make([]string, 0, 3)
	for _, f := range fields {
		if len(norm) == 3 {
			break
		}
		f = strings.TrimSpace(strings.ToLower(f))
		if f == "" {
			continue
		}
		if len(f) > 2 || !IsHexDigits(f) {
			return "", "", "", false
		}
		norm = append(norm, strings.Repeat("0", 2-len(f))+f)
	}
	if len(norm) == 0 {
		return "", "", "", false
	}
	for len(norm) < 3 {
		norm = append(norm, "")
	}
	return norm[0], norm[1], norm[2], true
}
