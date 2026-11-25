//go:build !linux

package hostdisk

// HostDiskPercent 非 linux（Windows 上跑单测）没有 statfs：恒返回 0，
// 屏幕那边 0 会显示成 `0%`（DEV 环境下无意义，设备上永远走 hostdisk_linux.go）。
func HostDiskPercent() int { return 0 }
