//go:build windows

package netdev

import "fmt"

// openCharATDevice Windows 上没有 MHI / wwan 字符设备（PCIe 模组在 Windows 上由驱动
// 自己交出一组 COM 口），所以这里如实报错，而不是拿别的写法硬开（开了也读不到 AT）。
func openCharATDevice(name string) (AtDev, error) {
	return nil, fmt.Errorf("%s：Windows 上没有 MHI/wwan 字符设备（PCIe 模组的 AT 口通常也是 COMx，按串口打开）", name)
}

// IsWouldBlock Windows 上不会用到（没有字符设备通道），恒 false。
func IsWouldBlock(err error) bool { return false }
