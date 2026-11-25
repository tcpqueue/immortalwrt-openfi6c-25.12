package common

// IsDecimal 判断是否为纯数字（允许模组上报表外取值时直接下发）
// 从 module/kernel/net.go 搬过来（纯函数）。
// IsDecimal 判断是否为纯数字（允许模组上报表外取值时直接下发）
func IsDecimal(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
