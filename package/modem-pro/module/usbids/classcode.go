package usbids

// 本文件由 extract-decls.py 从 kernel 抽出（按名字表 + 括号配平），
// 内容逐字保留，仅把需要跨包使用的名字首字母大写。

import (
	"strings"
)

// SplitUSBClassCode 拆一个"类码"字符串 → 类/子类/协议（都归一成两位小写十六进制）。
//
// 容忍三种写法（三种来源各写各的）：
//
//	02/06/00                            通用（sysfs 拼接、页面展示）
//	02-06-00                            连字符
//	USB\Class_02&SubClass_06&Prot_00    Windows 注册表 CompatibleIDs 原文
//
// 只有类也能用（子类/协议留空），因为有些固件不填后两位。
func SplitUSBClassCode(s string) (class, sub, proto string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", "", false
	}
	// Windows CompatibleIDs 的写法：Class_xx&SubClass_yy&Prot_zz
	if i := strings.Index(strings.ToUpper(s), "CLASS_"); i >= 0 {
		rest := s[i+len("CLASS_"):]
		rest = strings.NewReplacer("SubClass_", "", "Prot_", "", "subclass_", "", "prot_", "").Replace(rest)
		fields := strings.FieldsFunc(rest, func(r rune) bool { return r == '&' || r == '/' || r == '-' })
		return NormalizeHexFields(fields)
	}
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == '/' || r == '-' || r == ',' || r == ' ' || r == ':'
	})
	return NormalizeHexFields(fields)
}

func IsHexDigits(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return s != ""
}
