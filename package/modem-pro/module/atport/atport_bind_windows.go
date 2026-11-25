//go:build windows

package atport

// ====================== 命令口绑定：Windows 侧（不支持的桩） ======================
//
// Windows 上拿不到 sysfs 的 idVendor/idProduct，串口与 USB 设备的关联要去查注册表
// （Windows 自己的枚举在 usbhotplug_windows.go），所以这里不提供"按 VID:PID 绑定命令口"；
// 界面上会照实说"本平台不支持"（`ATPortBindSupported() == false`）。
//
// 想手动指定命令口，Windows 上仍可用「模组 → AT 口」那个下拉（go 那套）。

import "strings"

// ATPortBindSupported Windows 不支持按 VID:PID 绑定
func ATPortBindSupported() bool { return false }

// USBVIDPIDForTTY Windows 没有 sysfs，直接说读不到
func USBVIDPIDForTTY(tty string) (vid, pid string, ok bool) {
	_ = strings.TrimSpace(tty)
	return "", "", false
}

// USBVIDPIDForGroupTTYs 同上（保持与 unix 侧同一签名）
func USBVIDPIDForGroupTTYs(group []string) (vid, pid string, ok bool) {
	_ = len(group)
	return "", "", false
}

// BoundATPortForGroup Windows 上永远没有绑定（界面那条路不会显示）
func BoundATPortForGroup(group []string) (port, why string) {
	_ = len(group)
	return "", ""
}
