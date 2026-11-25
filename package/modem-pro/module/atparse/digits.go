package atparse

import ()

// IsAllDigits 判断是否全是 ASCII 数字（空串返回 true，调用方按需再判一次非空）。
func IsAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
