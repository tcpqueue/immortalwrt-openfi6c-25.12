//go:build !windows

package usbdesc

// ====================== USB 描述符（Linux / OpenWrt：sysfs） ======================
//
// AT 口 /dev/ttyUSB3 → /sys/class/tty/ttyUSB3/device 是指向
// /sys/devices/…/usb1/1-1/1-1:1.3/ttyUSB3 的符号链接。往上找到带 idVendor 的那层
// 就是 USB 设备目录（1-1），与它同级的 1-1:1.x 是各接口，bInterfaceClass 就是类码。
//
// 非 Linux 系统（或内核没挂 sysfs）读不到文件 → 返回 nil，
// 调用方（USBType）会退化成"类型未知"而不是报错。

import (
	"modemd/module/dial"
	"modemd/module/netdev"
	"modemd/module/sysfs"
	"os"
	"path/filepath"
	"strings"
)

// USBDescriptor 读模组 USB 侧的实际描述（VID/PID + 各接口类码）。
// 拿不到（不是 USB 串口 / 没有 sysfs）时返回 nil。
func USBDescriptor(portName string) *dial.USBDesc {
	dev := UsbDeviceDir(portName)
	if dev == "" {
		return nil
	}
	d := &dial.USBDesc{Source: "sysfs"}
	// ⚠️ idVendor / idProduct 是 **4 位**十六进制，必须用 readSysfsID：
	// readSysfsHex 是按“一个字节”写的（len>2 直接返回空串），拿它读 VID/PID 永远是空
	// （实测：路由器上拨号页的「VID:PID」一直显示 "-"，而 /api/usb 那边是对的）。
	d.VID = sysfs.ReadSysfsID(filepath.Join(dev, "idVendor"))
	d.PID = sysfs.ReadSysfsID(filepath.Join(dev, "idProduct"))
	d.Product = sysfs.ReadSysfsLine(filepath.Join(dev, "product"))

	// 各接口（1-1:1.0 / 1-1:1.1 …）的类码
	if entries, err := os.ReadDir(dev); err == nil {
		for _, e := range entries {
			if !e.IsDir() || !strings.Contains(e.Name(), ":") {
				continue
			}
			if code := sysfs.UsbInterfaceCode(filepath.Join(dev, e.Name())); code != "" {
				d.Classes = append(d.Classes, code)
			}
		}
	}
	if len(d.Classes) == 0 {
		// 非复合设备（或老内核不在接口目录放类码）→ 退回设备级类码
		if code := sysfs.UsbInterfaceCode(dev); code != "" {
			d.Classes = append(d.Classes, code)
		}
	}
	// 去重 + 收敛成最具体的那条（sysfs 给的就是三段式，但不同机型写法不一）
	d.Classes = dial.ReduceUSBCodes(d.Classes)
	if d.VID == "" && len(d.Classes) == 0 {
		return nil
	}
	return d
}

// UsbDeviceDir AT 口 → USB 设备目录（往上逐层找到含 idVendor 的那层）。
// 返回空串表示这个口不是 USB 串口（主板物理口 / 蓝牙虚拟口 / 名字不是 tty*）。
func UsbDeviceDir(portName string) string {
	name := filepath.Base(strings.TrimSpace(portName))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return ""
	}
	real, err := filepath.EvalSymlinks(filepath.Join(netdev.TtyClassRoot, name, "device"))
	if err != nil {
		return ""
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(real, "idVendor")); err == nil {
			return real
		}
		parent := filepath.Dir(real)
		if parent == real || parent == string(filepath.Separator) {
			break
		}
		real = parent
	}
	return ""
}

// ⚠️ readSysfsLine / readSysfsHex / readSysfsID / usbInterfaceCode 这几个小工具
// 已经挪到跨平台的 sysfs_read.go（USB 描述符与"主机侧网卡驱动"两条路共用）。
