package netdev

import (
	"modemd/iface"
	"modemd/module/catalog"
	"modemd/module/common"
	"modemd/module/sysfs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PcieEvalSymlinks 解析 sysfs 软链。
//
// ⚠️ 抽成变量是为了单测：**Windows 上建符号链接需要特权**（开发者模式/管理员），
// 而这一层的逻辑（驱动名、网卡/端口归属）本来就只是“顺着软链看它指向谁”。
// 测试注入一个假解析器就能在开发机上验证全部判定，不用等路由器。
// 生产环境永远是真解析。
var PcieEvalSymlinks = filepath.EvalSymlinks

var PciDevicesRoot = "/sys/bus/pci/devices"

var WwanClassRoot = "/sys/class/wwan"

// PcieModemDrivers 已知的 5G 模组 PCIe 驱动：绑上这些驱动的设备**就是**模组。
//
//	mhi_pci_generic / mhi_pci  高通 MHI（SDX55/65/72/75 全系：RM500Q/520N/FM150/FM160/EM9190…）
//	mtk_t7xx / t7xx_pci       联发科 T7xx（FM350-GL PCIe 版、T700 系）
//	pcie_mhi                  某些 BSP 里的老名字
//
// ⚠️ 判据必须是"驱动"而不是"VID"：17cb 同时也是高通**无线网卡**（ath11k/ath12k）的 VID，
// 只按 VID 放行会把笔记本的 WiFi 当成 5G 模组。
var PcieModemDrivers = []string{"mhi_pci_generic", "mhi_pci", "pcie_mhi", "mtk_t7xx", "t7xx_pci"}

// PCIDevice 一台 PCIe 设备（只挑"像 5G 模组"的，见 ListPCIModemDevices）
type PCIDevice struct {
	BDF        string            `json:"bdf"`                   // PCI 地址，如 0000:01:00.0
	VID        string            `json:"vid,omitempty"`         // 4 位小写十六进制
	PID        string            `json:"pid,omitempty"`         //
	Driver     string            `json:"driver,omitempty"`      // 当前绑定的驱动（空 = 没绑）
	Bound      bool              `json:"bound"`                 // 是否已绑定驱动
	Class      string            `json:"class,omitempty"`       // PCI class（如 0x0b40）
	Vendor     iface.ModemVendor `json:"vendor,omitempty"`      // 厂商标识（quectel/fibocom/… 认得出才填）
	VendorName string            `json:"vendor_name,omitempty"` // 厂商显示名
	Modem      bool              `json:"modem"`                 // 判定是 5G 模组
	MHI        bool              `json:"mhi,omitempty"`         // 走的是 MHI（高通）
	Netdevs    []string          `json:"netdevs,omitempty"`     // 主机侧网卡（wwan0 / rmnet_mhi0 …）
	WwanPorts  []string          `json:"wwan_ports,omitempty"`  // wwan 子系统端口（wwan0at0 …）
	ATPorts    []string          `json:"at_ports,omitempty"`    // AT 候选口（/dev/mhi_DUN、/dev/wwan0at0 …）
	QMICtrl    string            `json:"qmi_ctrl,omitempty"`    // QMI 控制口（/dev/mhi_QMI0、/dev/qcqmi0 …）
	MBIMDev    string            `json:"mbim_dev,omitempty"`    // MBIM 控制口（/dev/wwan0mbim0 …）
	Note       string            `json:"note,omitempty"`        // 说明（为什么算模组 / 为什么不绑驱动）
	// CatalogModels 由 PCI VID 推出的"可能是哪些**支持 PCIe** 的机型"（型号写法，见 modem_catalog.go）：
	// 只是提示（VID 是芯片厂时不唯一），真正的型号由 ATI 决定。
	CatalogModels []string `json:"catalog_models,omitempty"`
}

// ListPCIModemDevices 列出所有"像 5G 模组"的 PCIe 设备（含还没绑驱动的）。
func ListPCIModemDevices() []PCIDevice {
	entries, err := os.ReadDir(PciDevicesRoot)
	if err != nil {
		return nil
	}
	netdevs := netdevDevicePaths() // 网卡名 → device 软链解析后的真实路径
	wwanPorts := wwanClassPorts()  // wwan 端口名列表（wwan0at0 …）
	atCand := PcieATPortCandidates()
	qmiCand := PcieQMIControlCandidates()
	mbimCand := PcieMBIMCandidates()

	var out []PCIDevice
	for _, e := range entries {
		if !e.IsDir() || !LooksLikeBDF(e.Name()) {
			continue
		}
		dir := filepath.Join(PciDevicesRoot, e.Name())
		vid := normalizePCIID(sysfs.ReadSysfsLine(filepath.Join(dir, "vendor")))
		pid := normalizePCIID(sysfs.ReadSysfsLine(filepath.Join(dir, "device")))
		driver := pciDriverOf(dir)
		cls := normalizePCIID(sysfs.ReadSysfsLine(filepath.Join(dir, "class")))
		if !PcieLooksLikeModem(driver, vid, cls) {
			continue
		}
		dev := PCIDevice{
			BDF: e.Name(), VID: vid, PID: pid, Driver: driver, Bound: driver != "",
			Class: cls, Modem: true, MHI: pciDriverIsMHI(driver),
		}
		dev.Vendor, dev.VendorName = catalog.PCIEVendorOf(vid)
		// 支持清单里跟这个 VID 相关、且**支持 PCIe** 的机型（提示用，不做判定）
		dev.CatalogModels = catalog.CatalogPCIeModelsForVID(vid)
		if !dev.Bound {
			dev.Note = "这块 PCIe 设备像 5G 模组，但**还没绑定驱动** —— " +
				"检查内核模块（kmod-mhi-pci-generic / kmod-mtk-t7xx）是否装上；" +
				"页面上的「绑定驱动」按钮也会帮它绑一次"
		}
		out = append(out, dev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BDF < out[j].BDF })

	// 把网卡 / wwan 端口 / AT & 控制口分派到各自的 PCIe 设备上
	single := len(out) == 1
	for i := range out {
		out[i].Netdevs = assignByBDF(out[i].BDF, netdevs, single, IsLikelyMHIface)
		out[i].WwanPorts = assignNamesByBDF(out[i].BDF, wwanPorts, single, func(string) bool { return true })
		out[i].ATPorts = assignNamesByBDF(out[i].BDF, atCand, single, func(string) bool { return true })
		if c := assignNamesByBDF(out[i].BDF, qmiCand, single, func(string) bool { return true }); len(c) > 0 {
			out[i].QMICtrl = "/dev/" + c[0]
		}
		if c := assignNamesByBDF(out[i].BDF, mbimCand, single, func(string) bool { return true }); len(c) > 0 {
			out[i].MBIMDev = "/dev/" + c[0]
		}
	}
	return out
}

// netdevDevicePaths /sys/class/net 里**有 device 软链**（真物理网卡）的：名字 → 解析后的路径
func netdevDevicePaths() map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(sysfs.NetClassRoot)
	if err != nil {
		return out
	}
	for _, e := range entries {
		real, err := PcieEvalSymlinks(filepath.Join(sysfs.NetClassRoot, e.Name(), "device"))
		if err != nil {
			continue
		}
		out[e.Name()] = real
	}
	return out
}

// wwanClassPorts /sys/class/wwan 下的端口名（wwan0at0 / wwan0qmi0 / wwan0mbim0 …）
func wwanClassPorts() []string {
	entries, err := os.ReadDir(WwanClassRoot)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// IsLikelyMHIface 是不是"模组交出来的数据网卡"名（MHI/rmnet/wwan）
func IsLikelyMHIface(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "wwan") || strings.HasPrefix(n, "rmnet") ||
		strings.HasPrefix(n, "mhi")
}

// assignByBDF 从"名字 → 真实路径"的表里挑出属于这个 BDF 的项。
// single=true（只有一台 PCIe 模组）时，路径里找不到 BDF 的也认（驱动写法各异，别把人卡死）。
func assignByBDF(bdf string, paths map[string]string, single bool, want func(string) bool) []string {
	var out []string
	for name, p := range paths {
		if !want(name) {
			continue
		}
		if PathHasBDF(p, bdf) || single {
			out = append(out, name)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := NetdevRank(out[i]), NetdevRank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}

// assignNamesByBDF 从名字表里挑属于这个 BDF 的（wwan 端口用 `<name>/device` 解析）
func assignNamesByBDF(bdf string, names []string, single bool, want func(string) bool) []string {
	var out []string
	for _, n := range names {
		if !want(n) {
			continue
		}
		if single {
			out = append(out, n)
			continue
		}
		real, err := PcieEvalSymlinks(filepath.Join(WwanClassRoot, n, "device"))
		if err != nil {
			real = filepath.Join(WwanClassRoot, n)
		}
		if PathHasBDF(real, bdf) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// PathHasBDF 路径里有没有这个 PCI 地址（BDF 在 sysfs 路径里一定是完整一段，如 /0000:01:00.0/）
func PathHasBDF(path, bdf string) bool {
	bdf = strings.TrimSpace(bdf)
	if bdf == "" || path == "" {
		return false
	}
	return strings.Contains(path, "/"+bdf+"/") || strings.HasSuffix(path, "/"+bdf)
}

// LooksLikeBDF 目录名是不是 PCI 地址（0000:01:00.0）。
//
// ⚠️ Windows 的文件名不能含 `:`，所以**测试**里搭的假 sysfs 树会用 `0000-01-00.0` 这种退化
// 写法（与 USB 那套 isUSBInterfaceDirName 认 `1-1.0` 同样的原因）；两种都认，
// 生产环境（Linux/OpenWrt 的 sysfs）永远是真写法 `0000:01:00.0`。
func LooksLikeBDF(name string) bool {
	s := strings.ReplaceAll(name, "-", ":")
	if len(s) < 12 || strings.Count(s, ":") != 2 || !strings.Contains(s, ".") {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') && r != ':' && r != '.' {
			return false
		}
	}
	return true
}

// normalizePCIID 归一 sysfs 里的 0x17cb → 17cb
func normalizePCIID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "0x")
	return s
}

// pciDriverOf 读设备当前绑的驱动名（没绑返回空）
func pciDriverOf(dir string) string {
	real, err := PcieEvalSymlinks(filepath.Join(dir, "driver"))
	if err != nil {
		return ""
	}
	return filepath.Base(strings.TrimSpace(real))
}

// pciDriverIsMHI 高通 MHI 系驱动
func pciDriverIsMHI(driver string) bool {
	d := strings.ToLower(strings.TrimSpace(driver))
	return strings.HasPrefix(d, "mhi") || d == "pcie_mhi"
}

// pciDriverIsModemDriver 是不是"模组专用"的 PCIe 驱动
func pciDriverIsModemDriver(driver string) bool {
	d := strings.ToLower(strings.TrimSpace(driver))
	for _, m := range PcieModemDrivers {
		if d == m {
			return true
		}
	}
	return false
}

// PcieLooksLikeModem 判定一个 PCI 设备是不是 5G 模组。
//
//	① 绑着模组驱动（mhi*/t7xx）→ 是（无线网卡用的是 ath11k/ath12k/mt76，不会命中）
//	② 没绑驱动、但 VID 在模组芯片厂表里 + class 是无线控制器（0x0b）→ 大概率是
//	   （给用户提示"绑定驱动"，不直接当模组用）
func PcieLooksLikeModem(driver, vid, class string) bool {
	if pciDriverIsModemDriver(driver) {
		return true
	}
	if driver != "" {
		return false // 绑了别的驱动（ath11k 等）→ 不是模组
	}
	if _, ok := catalog.PCIEVendorNames[vid]; !ok {
		return false
	}
	cls := strings.TrimPrefix(normalizePCIID(class), "0x")
	return strings.HasPrefix(cls, "0b") // 0x0b = Wireless controller
}

// PcieATPortCandidates PCIe 模组的 AT 候选口（绝对路径）。
//
//	/dev/mhi_*DUN*   高通 MHI 的 AT 通道（mhi_uci 驱动，实测可用）
//	/dev/mhi_*AT*    个别驱动直接把 AT 通道叫这个名字
//	/dev/wwan*at*    wwan 子系统的 AT 口（联发科 T7xx：/dev/wwan0at0）
//
// ⚠️ 只列候选，不保证都是 AT 口 —— 到底哪个能应答由现有的 AT 探测判定（ProbeATTimed），
// 判错的会被写进"非 AT 口"否定缓存，所以不影响正常流程。
func PcieATPortCandidates() []string {
	var out []string
	out = append(out, globDev("mhi_*DUN*")...)
	out = append(out, globDev("mhi_*AT*")...)
	out = append(out, globDev("wwan*at*")...)
	return common.DedupeStrings(out)
}

// PcieQMIControlCandidates QMI 控制口候选（主机侧拨号器 / qmicli 用）
func PcieQMIControlCandidates() []string {
	var out []string
	out = append(out, globDev("mhi_*QMI*")...)
	out = append(out, globDev("wwan*qmi*")...)
	out = append(out, globDev("qcqmi*")...)
	out = append(out, globDev("mhi_uci_q*")...)
	return common.DedupeStrings(out)
}

// PcieMBIMCandidates MBIM 控制口候选（netifd 的 mbim 协议 / umbim 用）
func PcieMBIMCandidates() []string {
	var out []string
	out = append(out, globDev("wwan*mbim*")...)
	out = append(out, globDev("mhi_*MBIM*")...)
	return common.DedupeStrings(out)
}

// globDev 在 /dev 下按模式找设备名（返回**不带 /dev/** 的名字，与 /sys/class/wwan 的写法一致）
func globDev(pattern string) []string {
	if strings.TrimSpace(sysfs.DevRoot) == "" {
		return nil
	}
	files, err := filepath.Glob(filepath.Join(sysfs.DevRoot, pattern))
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, filepath.Base(f))
	}
	sort.Strings(out)
	return out
}

// PcieGroupKeyForPort 给一个 PCIe 口（/dev/mhi_DUN、/dev/wwan0at0 …）算出**分组键**。
//
// 同一台模组的 AT 口 + QMI 口 + MBIM 口必须归到同一组，否则发现流程会把它们
// 当成三台模组分别去探测。分两种：
//
//	wwan 子系统：`wwan0at0` / `wwan0qmi0` 的前缀 `wwan0` 就是设备本身 → 用它分组（准确）
//	MHI：       `/dev/mhi_DUN` 名字里没有控制器编号（mhi_uci 的命名就这样）→ 只能：
//	            能反查到唯一一块 PCIe 模组就归它，否则用固定键 "/pci/mhi" 把它们并成一组
//
// 返回空串表示"这不是 PCIe 口"。
func PcieGroupKeyForPort(port string) string {
	base := filepath.Base(strings.TrimSpace(port))
	if base == "" || !IsCharATDevice(base) {
		return ""
	}
	// wwan 子系统：wwan0at0 / wwan0qmi0 / wwan0mbim0 → wwan0
	if strings.HasPrefix(base, "wwan") {
		if name, ok := wwanDeviceOfPort(base); ok {
			return "/wwan/" + name
		}
	}
	if dev, ok := PcieDeviceForPort(base); ok {
		return "/pci/" + dev.BDF
	}
	return "/pci/mhi"
}

// wwanDeviceOfPort wwan0at0 → wwan0（按 /sys/class/wwan 的实际成员判断，避免把
// "wwan0extra" 这种名字切错；查不到时退回"去掉 at/qmi/mbim 后缀"的写法）
func wwanDeviceOfPort(port string) (string, bool) {
	if ports := wwanClassPorts(); len(ports) > 0 {
		best := ""
		for _, p := range ports {
			if p == port {
				continue
			}
			if strings.HasPrefix(port, p) && len(p) > len(best) {
				best = p // 取最长前缀（wwan10 优先于 wwan1）
			}
		}
		if best != "" {
			return best, true
		}
	}
	for _, suffix := range []string{"at", "qmi", "mbim", "adpl", "at0", "qmi0", "mbim0"} {
		if i := strings.Index(port, suffix); i > 0 {
			return port[:i], true
		}
	}
	return "", false
}

// PcieDeviceForPort 反查这个 /dev 口属于哪块 PCIe 模组。
// 先按名字归（wwan0xxx 属于 wwan0），再按"只有一块 PCIe 模组"兜底。
//
// ⚠️ 只接受 PCIe 类型的口（mhi_/wwan/qcqmi/qrtr-）：不然"场上只有一块 PCIe 模组"时，
// 任何查不到归属的串口（主板 COM、ttyUSB）都会被算到它头上 —— 预热时长、分组键全错。
func PcieDeviceForPort(port string) (PCIDevice, bool) {
	base := filepath.Base(strings.TrimSpace(port))
	if !IsCharATDevice(base) {
		return PCIDevice{}, false
	}
	devices := ListPCIModemDevices()
	if len(devices) == 0 {
		return PCIDevice{}, false
	}
	for _, d := range devices {
		for _, p := range append(append([]string{}, d.ATPorts...), d.WwanPorts...) {
			if p == base {
				return d, true
			}
		}
		if d.QMICtrl == "/dev/"+base || d.MBIMDev == "/dev/"+base {
			return d, true
		}
	}
	if len(devices) == 1 {
		return devices[0], true
	}
	return PCIDevice{}, false
}

// PcieDeviceAge PCIe 模组**被枚举出来**距今多久（判据 = PCI 设备目录的 mtime）。
//
// 与 USB 那边 usbDeviceAge 同一个用途（见 ports_unix.go 的注释）：
// PCIe 模组内部也是一台小 Linux，路由器一上电它要 1~2 分钟才把 AT 拉起来，
// 这期间设备节点已经在、open 也不报错，但发 AT 没应答 —— 没有这个信息的话
// 第一轮探测就把它写进"非 AT 口"否定缓存，然后一直认不出来。
//
// 目录 mtime 跨 modemd 重启依然有效（内核建目录时定的），所以重启进程不会重新等预热。
func PcieDeviceAge(port string) (time.Duration, bool) {
	dev, ok := PcieDeviceForPort(port)
	if !ok {
		return 0, false
	}
	fi, err := os.Stat(filepath.Join(PciDevicesRoot, dev.BDF))
	if err != nil {
		return 0, false
	}
	return time.Since(fi.ModTime()), true
}
