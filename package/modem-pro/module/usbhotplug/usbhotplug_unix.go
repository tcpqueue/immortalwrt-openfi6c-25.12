//go:build !windows

package usbhotplug

// ====================== USB 热插拔（Linux / OpenWrt：sysfs + usb-serial 驱动补绑） ======================
//
// 这里解决的是"模组插上了，但 /dev/ttyUSB* 没出来"的情况 —— OpenWrt 上很常见：
// 内核的 option 驱动不认识这个 VID/PID（新模组、或者 kmod 没装），于是串口一个都不给。
// 做法就是标准的两步：
//
//	Modprobe option                          （不行再退到 usbserial）
//	echo "<vid> <pid>" > /sys/bus/usb-serial/drivers/option1/new_id
//
// 写进去之后内核会立刻给这个设备建 ttyUSB*，再交给原有的发现流程去找 AT 口。
//
// ⚠️ 只在"已经确认是模组"的设备上做（见 usbids.IsKnownModemUSB），不碰无关设备。

import (
	"fmt"
	"modemd/iface"
	"modemd/module/config"
	"modemd/module/dial"
	"modemd/module/hostnic"
	"modemd/module/netdev"
	"modemd/module/sysfs"
	"modemd/module/usbdesc"
	"modemd/module/usbids"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

// UsbDevicesRoot sysfs 的 USB 设备根目录。
// ⚠️ 是变量而不是常量：测试里会换成一棵**假的 sysfs 树**（含符号链接）来验证枚举逻辑 ——
// 这条路径上的两个坑（符号链接 + 4 位 VID/PID）只有用真目录树才测得出来。
var UsbDevicesRoot = "/sys/bus/usb/devices"

// IsUSBDeviceEntry /sys/bus/usb/devices 下的一个条目要不要当“设备”处理。
//
// 只要不是接口目录（名字带冒号，如 2-1:2.0）就当设备 —— **绝不能**用 DirEntry.IsDir()
// 去过滤：这个目录下每一项都是符号链接，IsDir() 会返回 false，于是什么都扫不到。
func IsUSBDeviceEntry(name string) bool {
	return !strings.Contains(name, ":")
}

// isDirOrSymlink 目录（或指向目录的符号链接）就算 true。sysfs 里两者混着来。
func isDirOrSymlink(path string) bool {
	st, err := os.Stat(path) // Stat 会跟随符号链接
	return err == nil && st.IsDir()
}

// UsbReauthorizeGap 写 0 之后等多久再写 1（让内核真的把这个设备拆干净）
const UsbReauthorizeGap = 1500 * time.Millisecond

// USBRescueSupported 本平台能不能做「USB 软重插」
func USBRescueSupported() bool { return true }

// ReauthorizeUSBDevice 对这个 USB 设备做一次**软重插**：sysfs 的 authorized 写 0 再写 1。
//
// ⚠️ 这跟“USB port reset（ioctl）”不是一回事：写 authorized 是**真正的重新枚举**（等于拔插一次），
// 实测能救活“冷启动后固件卡死、AT 口不应答”的模组（鼎桥 MT5700M-CN：重启路由器没用，
// 因为路由器重启并不断模组的电；软重插立刻恢复）。
//
// 副作用：设备重新枚举后 **tty 名会变**（实测 ttyUSB0-3 → ttyUSB0/2/3/4），
// 这正是“网卡/串口名不固定”那套逻辑要处理的情况（我们会重新分组、接口也会自动改绑）。
func ReauthorizeUSBDevice(busPath string) error {
	busPath = strings.TrimSpace(busPath)
	if busPath == "" || strings.ContainsAny(busPath, "/\\ \t\n") {
		return fmt.Errorf("USB 拓扑路径不合法：%q", busPath)
	}
	p := filepath.Join(UsbDevicesRoot, busPath, "authorized")
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("找不到 %s（设备或内核不支持 authorized）：%v", p, err)
	}
	if err := os.WriteFile(p, []byte("0\n"), 0o644); err != nil {
		return fmt.Errorf("写 0 失败：%v", err)
	}
	time.Sleep(UsbReauthorizeGap)
	if err := os.WriteFile(p, []byte("1\n"), 0o644); err != nil {
		return fmt.Errorf("写 1 失败：%v", err)
	}
	return nil
}

// USBHotplugPlatform 当前平台（openwrt 与 linux 分开：给的提示不一样）
func USBHotplugPlatform() string {
	if hostnic.IsOpenWrt() {
		return "openwrt"
	}
	return "linux"
}

// USBHotplugCanBind Linux/OpenWrt 上可以自动补驱动
func USBHotplugCanBind() bool { return true }

// USBHotplugBindNote 给页面的一句话说明
func USBHotplugBindNote() string {
	if hostnic.IsOpenWrt() {
		return "OpenWrt：会自动 Modprobe option 并登记 VID/PID（new_id）；" +
			"没装驱动时先 opkg install kmod-usb-serial-option"
	}
	return "Linux：会自动 Modprobe option/usbserial 并登记 VID/PID（new_id），把 ttyUSB 变出来"
}

// ListModemUSBDevices 枚举系统里"像模组"的 USB 设备（**包含还没有串口的**）。
//
// 只看命中身份表（usbids.IsKnownModemUSB）的设备：别的 USB 设备不进这个列表，
// 免得页面上的"补驱动"按钮误伤它们。
func ListModemUSBDevices() ([]ModemUSBDevice, error) {
	entries, err := os.ReadDir(UsbDevicesRoot)
	if err != nil {
		return nil, err
	}
	// 串口 → 所属 USB 设备目录名（1-1），用于把 /dev/ttyUSB3 归到设备上
	ttyByDev := map[string][]string{}
	if ports, err := netdev.ScanSerialPorts(); err == nil {
		for _, p := range ports {
			dir := usbdesc.UsbDeviceDir(p)
			if dir == "" {
				continue
			}
			base := filepath.Base(dir)
			ttyByDev[base] = append(ttyByDev[base], p)
		}
	}

	var out []ModemUSBDevice
	for _, e := range entries {
		name := e.Name()
		if !IsUSBDeviceEntry(name) {
			continue // 接口目录（1-1:1.0）/ usbN 根 hub：设备信息在父目录
		}
		dir := filepath.Join(UsbDevicesRoot, name)
		// ⚠️ 判据是“能不能读到 idVendor/idProduct”，**不要**先用 e.IsDir() 过滤：
		// /sys/bus/usb/devices 下的条目全是**符号链接**（`2-1 -> ../../../devices/.../usb2/2-1`），
		// 而 Go 的 DirEntry.IsDir() 对符号链接返回 false —— 那样会把每一台设备都跳过
		// （实测：OpenWrt 上枚举结果永远是空，插上了没串口的模组永远等不到补驱动，
		//  界面 /api/usb 也一直是空的）。读属性文件会自动跟随链接，只有真设备目录才有这两个。
		vid := sysfs.ReadSysfsID(filepath.Join(dir, "idVendor"))
		pid := sysfs.ReadSysfsID(filepath.Join(dir, "idProduct"))
		if vid == "" || pid == "" {
			continue
		}
		dev := ModemUSBDevice{
			VID: vid, PID: pid, BusPath: name,
			Product: sysfs.ReadSysfsLine(filepath.Join(dir, "product")),
			Serial:  sysfs.ReadSysfsLine(filepath.Join(dir, "serial")),
			TTYs:    ttyByDev[name],
		}
		var drivers []string
		if ifaces, err := os.ReadDir(dir); err == nil {
			for _, ie := range ifaces {
				if !strings.Contains(ie.Name(), ":") {
					continue // 设备目录里的非接口文件（authorized / busnum ...）
				}
				idir := filepath.Join(dir, ie.Name())
				if !isDirOrSymlink(idir) {
					continue // 同样是符号链接的话 IsDir() 认不出来，用 Stat 跟一下
				}
				if c := sysfs.UsbInterfaceCode(idir); c != "" {
					dev.Classes = append(dev.Classes, c)
				}
				if drv := usbInterfaceDriver(idir); drv != "" {
					drivers = append(drivers, drv)
				}
			}
		}
		dev.Classes = dial.ReduceUSBCodes(dev.Classes)
		dev.Drivers = dedupStrings(drivers)
		dev.HasSerialDriver = hasSerialDriverName(dev.Drivers) || len(dev.TTYs) > 0

		fam, ok := usbids.IsKnownModemUSB(vid, pid, dev.Classes)
		if !ok {
			continue
		}
		dev.Known = true
		dev.Family = string(fam.Vendor)
		dev.VendorCN = iface.VendorLabel(fam.Vendor)
		out = append(out, dev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BusPath < out[j].BusPath })
	return out, nil
}

// usbInterfaceDriver 接口绑定的驱动名（没绑定返回空串）。
func usbInterfaceDriver(ifaceDir string) string {
	real, err := filepath.EvalSymlinks(filepath.Join(ifaceDir, "driver"))
	if err != nil {
		return ""
	}
	return filepath.Base(real)
}

// hasSerialDriverName 驱动名里有没有 usb-serial 系（option / usbserial）。
func hasSerialDriverName(drivers []string) bool {
	for _, d := range drivers {
		switch d {
		case "option", "usbserial", "qcserial", "cdc_acm":
			return true
		}
	}
	return false
}

func dedupStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// EnsureSerialTTY 给一台"插上了但没有串口"的模组补上 usb-serial 驱动。
//
// 返回一句给人看的结果说明（成功/失败都会写进 USB 事件）。调用方要等一小会儿
// （内核枚举 tty 需要几十到几百毫秒）再回去扫串口。
func EnsureSerialTTY(d ModemUSBDevice) (string, error) {
	drv, newID, err := usbSerialNewIDPath()
	if err != nil {
		// 没有 usb-serial 驱动 → 先 Modprobe
		if e := Modprobe("option"); e != nil {
			config.Logger.Debug("Modprobe option failed", zap.Error(e))
			if e2 := Modprobe("usbserial"); e2 != nil {
				return "", fmt.Errorf("加载 usb-serial 驱动失败（option/usbserial 都没有）: %v；%s",
					e2, USBSerialPackageHint())
			}
		}
		drv, newID, err = usbSerialNewIDPath()
		if err != nil {
			return "", fmt.Errorf("系统里没有 usb-serial 驱动（new_id 路径不存在）；%s", USBSerialPackageHint())
		}
	}
	line := d.VID + " " + d.PID
	if err := os.WriteFile(newID, []byte(line), 0o200); err != nil {
		return "", fmt.Errorf("写 %s 失败（需要 root 权限？）: %v", newID, err)
	}
	return fmt.Sprintf("已让 %s 接管 %s（%s）", drv, line, d.BusPath), nil
}

// usbSerialNewIDPath 找 usb-serial 驱动目录里的 new_id 文件（优先 option）。
func usbSerialNewIDPath() (driver, path string, err error) {
	dirs, _ := filepath.Glob("/sys/bus/usb-serial/drivers/*")
	sort.Strings(dirs)
	pick := ""
	for _, d := range dirs {
		base := filepath.Base(d)
		if strings.HasPrefix(base, "option") {
			pick = d
			break
		}
	}
	if pick == "" && len(dirs) > 0 {
		pick = dirs[0]
	}
	if pick == "" {
		return "", "", fmt.Errorf("no usb-serial driver loaded")
	}
	p := filepath.Join(pick, "new_id")
	if _, e := os.Stat(p); e != nil {
		return "", "", e
	}
	return filepath.Base(pick), p, nil
}

// Modprobe 加载内核模块（OpenWrt / 桌面 Linux 都有；失败时把 stderr 带出来）。
func Modprobe(mod string) error {
	cmd := exec.Command("modprobe", mod)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("%v: %s", err, msg)
		}
		return err
	}
	return nil
}
