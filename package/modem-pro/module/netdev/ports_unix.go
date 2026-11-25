//go:build !windows

package netdev

import (
	"modemd/module/common"
	"modemd/module/sysfs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// ---- from: ports/ports_unix.go ----

// devRoot / TtyClassRoot 串口与 tty 的根目录。
// ⚠️ 是变量：单元测试会把它们换成假的 /dev 与 /sys/class/tty 树跑完整流程 ——
// 否则在**真机**上（路由器上真的插着模组）测试会去认真实的 /dev/ttyUSB*，
// 结果把串口算到假设备头上（实测踩到）。生产代码永远是系统路径。
var (
	TtyClassRoot = "/sys/class/tty"
	// SysBusUSBRoot USB 设备目录（同样可被测试替换）
	SysBusUSBRoot = "/sys/bus/usb/devices"
)

// ScanSerialPorts 扫出所有"可能是模组 AT 口"的设备节点。
//
// 四类（少了任何一类都会让整类模组认不出来）：
//
//	/dev/ttyUSB*    经典 USB 模组串口（高通 option / 厂商驱动）
//	/dev/ttyACM*    ACM 类串口（部分模组、部分 PCIe 驱动的 AT 口就是 ACM）
//	/dev/mhi_*DUN*  高通 MHI（PCIe）的 AT 通道 —— **不是 tty**，见 atdev.go
//	/dev/wwan*at*   wwan 子系统的 AT 口（联发科 T7xx 的 /dev/wwan0at0 等）
//
// 只负责"列出来"：到底哪个能应答 AT 由 ProbeATTimed 判定（非 AT 口会进否定缓存，
// 不会每轮重复骚扰）。
func ScanSerialPorts() ([]string, error) {
	var files []string
	pats := []string{"ttyUSB*", "ttyACM*", "mhi_*DUN*", "mhi_*AT*", "wwan*at*"}
	// 展锐 SIPC 命令通道（nr2 直连快路）—— 列表与判据见 Mu300ATScanGlobs。
	// ⚠️ nr1（daemon 代理，1~4 s/条）已从列表里拿掉，不在候选内。
	pats = append(pats, Mu300ATScanGlobs()...)
	for _, pat := range pats {
		more, err := filepath.Glob(filepath.Join(sysfs.DevRoot, pat))
		if err != nil {
			return nil, err
		}
		files = append(files, more...)
	}
	files = common.DedupeStrings(files)
	// 排序：tty 按编号升序（COM 风格），非 tty（MHI/wwan）排在后头按名字。
	//
	// ⚠️ 展锐 SIPC 通道**不能按字典序**：发现流程对同一组取"第一个能应答的口"
	// 当 at_port，字典序会把备选口排到主命令口前面 ⇒ 主路永远轮不上。
	// 这里按 Mu300ATChanRank 排（nr2 在 nr3 之前）。
	sort.SliceStable(files, func(i, j int) bool {
		a, b := SerialPortIndex(files[i]), SerialPortIndex(files[j])
		switch {
		case a >= 0 && b >= 0:
			return a < b
		case a >= 0:
			return true
		case b >= 0:
			return false
		}
		ra, rb := Mu300ATChanRank(files[i]), Mu300ATChanRank(files[j])
		if ra >= 0 || rb >= 0 {
			switch {
			case ra < 0:
				return false
			case rb < 0:
				return true
			case ra != rb:
				return ra < rb
			}
		}
		return files[i] < files[j]
	})
	return files, nil
}

// usbDevicePath 解析一个串口设备对应的 USB 物理设备路径。
// 通过 /sys/class/tty/ttyUSBx/device 符号链接：
//
//	/sys/class/tty/ttyUSB0/device -> ../../devices/pci0000:00/.../usb1/1-1/1-1:1.0/ttyUSB0
//
// 提取到 USB 设备的接口路径 "usb1/1-1"（同一 USB 设备的所有 tty 共享同一父路径）。
// 返回空串表示无法关联（非 USB 串口，如主板自带）。
func USBDevicePath(devPath string) string {
	// /dev/ttyUSB0 -> ttyUSB0
	base := filepath.Base(devPath)
	// /sys/class/tty/ttyUSB0/device -> .../usb1/1-1/1-1:1.0/ttyUSB0
	sysDev := filepath.Join(TtyClassRoot, base, "device")
	real, err := filepath.EvalSymlinks(sysDev)
	if err != nil {
		return ""
	}
	// 找到 usbX/YYYY 段（USB 设备路径）
	re := regexp.MustCompile(`/(usb\d+)/([^/]+)/`)
	m := re.FindStringSubmatch(real)
	if len(m) < 3 {
		return ""
	}
	// 用 "/usb1/1-1" 作为设备标识（不含接口子路径，同设备所有接口共享）
	return "/" + m[1] + "/" + m[2]
}

// groupByUSBDevice 将串口按物理设备分组（USB 设备 / PCIe 模组）。
// 同一个设备下的所有口属于同一个模组（模组占 2/4/6 口都能正确对应）。
// 无法关联到任何物理设备的串口（主板 COM）各自独立成组。
func GroupByUSBDevice(ports []string) [][]string {
	groupsMap := make(map[string][]string)
	var order []string
	for _, p := range ports {
		key := portGroupKey(p)
		if key == "" {
			key = p // 独立成组
		}
		if _, ok := groupsMap[key]; !ok {
			order = append(order, key)
		}
		groupsMap[key] = append(groupsMap[key], p)
	}
	groups := make([][]string, 0, len(order))
	for _, k := range order {
		groups = append(groups, groupsMap[k])
	}
	return groups
}

// portGroupKey 口的物理归属键：USB 口用 USB 拓扑（"/usb2/2-1"），
// PCIe 口（MHI / wwan）用 PCI 归属（"/pci/0000:01:00.0" / "/wwan/wwan0"）。
// 空串表示查不到归属（各自成组）。
func portGroupKey(port string) string {
	if k := USBDevicePath(port); k != "" {
		return k
	}
	// 展锐 SIPC：nr2/nr3（直连命令口）与 nr0（URC 上报口）是**同一台模组的口**，
	// 不合并就会把同一台模组认成好几台：
	//   · URC 上报口（nr0）本不该出现在候选里，但设备节点扫描看得见它；
	//   · 两条命令口（nr2/nr3）若同时扫，也会各自成组、各占一个 at_port。
	// 合并后才是对的语义：一台模组，多条口，取优先的那条当 at_port。
	if k, ok := Mu300SIPCGroupKey(port); ok {
		return k
	}
	return PcieGroupKeyForPort(port)
}

// serialPortIndex 提取串口设备后面的数字编号，供排序/分组使用。
// Linux 下格式为 /dev/ttyUSB0、/dev/ttyUSB1、/dev/ttyACM0…；
// 非 tty 口（/dev/mhi_DUN、/dev/wwan0at0）返回 -1。
func SerialPortIndex(name string) int {
	re := regexp.MustCompile(`tty(?:USB|ACM)(\d+)`)
	m := re.FindStringSubmatch(name)
	if len(m) < 2 {
		return -1
	}
	n := 0
	for _, c := range m[1] {
		n = n*10 + int(c-'0')
	}
	return n
}

// UsbDeviceAge 返回某个串口所属 USB 设备**被枚举出来**距今多久。
//
// 为什么要问这个（真机踩到）：模组由路由器的 USB 供电，**路由器一断电模组也断电**。
// 5G 模组内部是一台小 Linux，开机后要 1~2 分钟才把 AT 守护进程拉起来；
// 这期间串口节点已经存在、open 也不报错，但发 AT 就是没应答（或只答第一条）。
// 于是第一轮探测全部超时 → 全被写进"非 AT 口"否定缓存 → 模组长时间认不出来。
//
// 判据用 sysfs 设备目录的 mtime：目录是内核枚举设备时创建的，
// 所以它**跨 modemd 重启依然有效**（这一点很关键：手动重启 modemd 不该重新等预热）。
// 返回 ok=false 表示查不到（非 USB 串口、或 Windows 上没有 sysfs），
// 调用方应当按"不做预热等待"处理，避免在别的平台上把功能等没了。
func UsbDeviceAge(devPath string) (time.Duration, bool) {
	p := USBDevicePath(devPath)
	if p == "" {
		// PCIe（MHI / wwan）模组没有 USB 拓扑：改看 **PCI 设备目录**的 mtime，
		// 逻辑一样（目录是内核枚举设备时建的，同样跨 modemd 重启有效）——
		// PCIe 模组内部也是一台小 Linux，开机同样要一两分钟才把 AT 拉起来。
		return PcieDeviceAge(devPath)
	}
	fi, err := os.Stat(filepath.Join(SysBusUSBRoot, filepath.Base(p)))
	if err != nil {
		return 0, false
	}
	return time.Since(fi.ModTime()), true
}
