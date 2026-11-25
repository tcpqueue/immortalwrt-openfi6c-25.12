package netprov

// ValidNetdevName 网卡名只允许字母数字与 . _ -（写 /proc/sys 前先挡一下路径穿越）
func ValidNetdevName(s string) bool {
	if s == "" || len(s) > 16 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}
