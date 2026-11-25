//go:build !windows

package common

// NormalizePortName 串口名归一。Unix 上没有额外要求，原样返回
// （Windows 版见 portname_windows.go：COM 口要加 `\\.\` 前缀）。
func NormalizePortName(name string) string { return name }
