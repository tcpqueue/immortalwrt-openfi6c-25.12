package sysfs

// ====================== sysfs 小工具（跨平台编译，读不到就返回空） ======================
//
// 这些只是"读一个文本文件/一个属性"，本身没有平台依赖：
// Linux/OpenWrt 的 sysfs 上能读到值，Windows 上自然读不到（返回空串）。
//
// 放在跨平台文件里有两个好处：
//  1. usbdesc_unix.go（USB 描述符）与 usbnet_host_sysfs.go（主机侧网卡驱动）能共用；
//  2. 假 sysfs 的测试在开发机（Windows）上也能跑，不用等上路由器才发现写错。
//
// 注意：调用方拿到空串要当"读不到"处理，不能当成有效数据。

import (
	"modemd/module/usbids"
	"os"
	"path/filepath"
	"strings"
)

// NetClassRoot sysfs 里的网卡目录（可注入：单测里换成假目录）。
// 跨平台编译：Windows 上没有 /sys，读不到就返回空 —— 调用方保持“未识别”，不猜。
var NetClassRoot = "/sys/class/net"

// DevRoot 设备节点目录（/dev）。跨平台放在这里：PCIe（MHI/wwan）的 AT 候选口
// 是字符设备（/dev/mhi_DUN、/dev/wwan0at0），枚举那边（pcie.go）两个平台共用一份代码；
// 单测同样会把它换成假目录。
var DevRoot = "/dev"

// IsUSBInterfaceDirName USB 接口目录名。
//
// Linux sysfs 里形如 `1-1:1.0`（设备 1-1 的第 0 号配置第 0 个接口）；
// Windows 上无法创建含冒号的目录，所以**测试**里会用 `1-1.0` 这种退化写法 —— 两种都认。
// USB **设备**目录（`1-1`、`2-1.3`）既没冒号也没点，不会被误当成接口。
func IsUSBInterfaceDirName(name string) bool {
	if strings.Contains(name, ":") {
		return true
	}
	return strings.Contains(name, "-") && strings.Contains(name, ".")
}

// ReadSysfsLine 读 sysfs 文本文件的首行（sysfs 的"一行一个值"）
func ReadSysfsLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line := string(b)
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}

// ReadSysfsHex 读 sysfs 里的一个十六进制**字节**值（归一成两位小写，读不到返回空串）
// ⚠️ 只用于 bInterfaceClass / bDeviceClass 这类单字节属性 —— VID/PID 是 **4 位**，
// 用这个函数读会因为 len>2 直接返回空串（实测：Linux 枚举一直为空，见 ReadSysfsID）。
func ReadSysfsHex(path string) string {
	s := strings.ToLower(ReadSysfsLine(path))
	if s == "" || len(s) > 2 || !usbids.IsHexDigits(s) {
		return ""
	}
	return strings.Repeat("0", 2-len(s)) + s
}

// ReadSysfsID 读 sysfs 里的 USB ID（idVendor / idProduct = 4 位十六进制，小写归一）。
// ⚠️ 别用 ReadSysfsHex：它是按"一个字节"写的（len>2 就返回空串），
// 拿它读 VID/PID 永远是空 → Linux/OpenWrt 上会一个设备都认不出来。
func ReadSysfsID(path string) string {
	s := strings.ToLower(ReadSysfsLine(path))
	if len(s) != 4 || !usbids.IsHexDigits(s) {
		return ""
	}
	return s
}

// UsbInterfaceCode 读一个目录的接口类码，拼成 "02/06/00"（读不到返回空串）。
// 接口目录优先用 bInterface*；设备目录只有 bDeviceClass（非复合设备）。
func UsbInterfaceCode(dir string) string {
	class := ReadSysfsHex(filepath.Join(dir, "bInterfaceClass"))
	sub := ReadSysfsHex(filepath.Join(dir, "bInterfaceSubClass"))
	proto := ReadSysfsHex(filepath.Join(dir, "bInterfaceProtocol"))
	if class == "" {
		class = ReadSysfsHex(filepath.Join(dir, "bDeviceClass"))
		sub = ReadSysfsHex(filepath.Join(dir, "bDeviceSubClass"))
		proto = ReadSysfsHex(filepath.Join(dir, "bDeviceProtocol"))
	}
	if class == "" {
		return ""
	}
	return class + "/" + sub + "/" + proto
}
