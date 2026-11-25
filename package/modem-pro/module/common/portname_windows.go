//go:build windows

package common

import "strings"

// NormalizePortName 串口名归一：Windows 下 COM10+ 需要加 "\\.\" 前缀才能正确打开。
// 所有 COM 口统一加前缀也无害，保证 tarm/serial 在 Windows 上可靠打开。
func NormalizePortName(name string) string {
	upper := strings.ToUpper(name)
	if !strings.HasPrefix(upper, "COM") {
		return name
	}
	return `\\.\` + name
}
