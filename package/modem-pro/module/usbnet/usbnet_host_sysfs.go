package usbnet

// ====================== 主机侧网卡 → USB 类型（读 sysfs） ======================
//
// 一张网卡在 Linux/OpenWrt 的 sysfs 里有：
//
//	/sys/class/net/wwan0/uevent          DRIVER=qmi_wwan（最省事的拿法）
//	/sys/class/net/wwan0/device/driver   → …/bus/usb/drivers/qmi_wwan（兜底）
//	/sys/class/net/wwan0/device          → …/usb1/1-1/1-1:1.0（USB 接口）或 …/1-1（USB 设备）
//
// 顺着 device 往上找含 idVendor 的那层就是 USB 设备目录，于是 VID:PID 与产品名也有了。
//
// 为什么不做平台 build tag：这段只是"读文件"，读不到就返回 nil（Windows 上没有 /sys，
// 自然返回 nil，行为与"未识别"一致）——而**跨平台编译**能让这套假 sysfs 的测试
// 在开发机上（Windows）也跑起来，不用等到路由器上才发现逻辑写错。

import (
	"modemd/module/dial"
	"modemd/module/sysfs"
	"os"
	"path/filepath"
	"strings"
)

// HostIfaceNames 候选网卡名（默认按 DialIfaceSnapshot 的强/弱候选排序）。
// 抽成变量是为了单测能塞一个假网卡名进来（真机上是 wwan0/usb0 这类）。
var HostIfaceNames = func() []string {
	list := dial.DialIfaceSnapshot()
	out := make([]string, 0, len(list))
	for _, c := range list {
		out = append(out, c.Name)
	}
	return out
}

// HostIfaceHintImpl 扫本机网卡，挑一张"像模组数据口"的，读它的驱动名与 USB 祖先。
//
// 优先返回**能据驱动判定协议**的那张（如 wwan0 + qmi_wwan）；
// 一张都判不出来时，返回有名字信息的那张（至少能把 VID:PID 补上）。
func HostIfaceHintImpl() *HostIfaceHint {
	var fallback *HostIfaceHint
	for _, name := range HostIfaceNames() {
		h := ReadHostIfaceHint(name)
		if h == nil {
			continue
		}
		if h.Value != "" {
			return h
		}
		if h.Driver != "" || h.VID != "" {
			if fallback == nil {
				fallback = h
			}
		}
	}
	return fallback
}

// ReadHostIfaceHint 读单张网卡的驱动名 + USB 设备信息（读不到返回 nil）
func ReadHostIfaceHint(name string) *HostIfaceHint {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	base := filepath.Join(sysfs.NetClassRoot, name)
	if _, err := os.Stat(base); err != nil {
		return nil
	}
	h := &HostIfaceHint{Iface: name}

	h.Driver = netUeventDriver(filepath.Join(base, "uevent"))
	if h.Driver == "" {
		if real, err := filepath.EvalSymlinks(filepath.Join(base, "device", "driver")); err == nil {
			h.Driver = filepath.Base(strings.TrimSpace(real))
		}
	}
	h.Value = UsbnetFromHostDriver(h.Driver)

	// USB 祖先目录（网卡绑在 USB 接口上时才有）：VID/PID/产品名 + 各接口类码
	if dev := usbAncestorDir(filepath.Join(base, "device")); dev != "" {
		h.VID = sysfs.ReadSysfsID(filepath.Join(dev, "idVendor"))
		h.PID = sysfs.ReadSysfsID(filepath.Join(dev, "idProduct"))
		h.Product = sysfs.ReadSysfsLine(filepath.Join(dev, "product"))
		if entries, err := os.ReadDir(dev); err == nil {
			for _, e := range entries {
				if !e.IsDir() || !sysfs.IsUSBInterfaceDirName(e.Name()) {
					continue
				}
				if code := sysfs.UsbInterfaceCode(filepath.Join(dev, e.Name())); code != "" {
					h.Classes = append(h.Classes, code)
				}
			}
		}
		if len(h.Classes) == 0 {
			if code := sysfs.UsbInterfaceCode(dev); code != "" {
				h.Classes = append(h.Classes, code)
			}
		}
		h.Classes = dial.ReduceUSBCodes(h.Classes)
	}
	if h.Driver == "" && h.VID == "" && len(h.Classes) == 0 {
		return nil
	}
	return h
}

// netUeventDriver 从网卡 uevent 里取 DRIVER=xxx（读不到返回空）
func netUeventDriver(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "DRIVER="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// usbAncestorDir 从一个 sysfs 路径往上找含 idVendor 的目录（= USB 设备目录）。
// 非 USB 设备（如 PCI 网卡）往上找不到 → 返回空串。
func usbAncestorDir(path string) string {
	real, err := filepath.EvalSymlinks(path)
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
