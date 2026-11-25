package common

import "strings"

// IsHexValue 判断是否为 `0x…` 形式的十六进制（从 module/kernel/net.go 搬过来，纯函数）。
// isHexValue 是否是 `0x` 前缀的十六进制（Sierra 的接口位图就是这种）
func IsHexValue(s string) bool {
	u := strings.ToUpper(strings.TrimSpace(s))
	if !strings.HasPrefix(u, "0X") || len(u) <= 2 {
		return false
	}
	for _, r := range u[2:] {
		if !strings.ContainsRune("0123456789ABCDEF", r) {
			return false
		}
	}
	return true
}
