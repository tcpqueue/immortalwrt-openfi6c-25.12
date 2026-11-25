package kernel

// ====================== PCIe（MHI / wwan）模组支持 ======================
//
// 为什么需要单独一层：PCIe 模组（RM500Q/RM520N 的 PCIe 版、FM350 PCIe 版、T99W175、
// EM9190/EM9291、SIM8380G-M2 …）**不在 USB 总线上**：
//
//	· 没有 /sys/bus/usb/devices 里的条目 → 现成的 USB 发现/分组/补驱动全部落空；
//	· AT 口不是 tty（/dev/mhi_DUN、/dev/wwan0at0）→ 按串口打开必失败（见 atdev.go）；
//	· 主机侧网卡是 mhi_netdev / rmnet / wwan 交出来的 wwan0，不是 qmi_wwan；
//	· "拔了再插"不存在，卡死时只能 PCIe 层复位（remove + rescan，见 pcie_unix.go）。
//
// 这一层只做"看"和"认"：读 /sys/bus/pci/devices、/sys/class/wwan、/sys/class/net，
// 判定哪些 PCI 设备是 5G 模组、它的 AT 候选口/控制口/网卡分别是什么。
// 真正打开设备在 atdev.go，拨号在 hostdial.go，自动组网在 netprov*.go。
//
// 平台：全是"读文件"，读不到就返回空（Windows 上没有 /sys，自然为空）——
// 这样逻辑本身可以在开发机上用假 sysfs 跑单测（见 pcie_test.go），不用等真机。

import (
	"fmt"
	"modemd/module/catalog"
	"modemd/module/netdev"
	"strings"
)

// ---- 可注入路径（单测替换成假 sysfs 树） ----

var (
// pciDevicesRoot PCI 设备目录（每个设备一个子目录，名字是 BDF，如 0000:01:00.0）
// pciDriversRoot PCI 驱动目录（找 <driver>/bind 用）
// pciRescanPath 写 1 触发整条 PCIe 总线重新枚举
// wwanClassRoot wwan 子系统（wwan0at0 / wwan0qmi0 / wwan0mbim0 这些口按它分组）
)

// PCIeInfo 这台模组的 PCIe 侧信息（挂在 DialTypeInfo 上给页面用）
type PCIeInfo struct {
	BDF       string   `json:"bdf,omitempty"`
	VID       string   `json:"vid,omitempty"`
	PID       string   `json:"pid,omitempty"`
	Driver    string   `json:"driver,omitempty"`
	MHI       bool     `json:"mhi,omitempty"`
	Netdevs   []string `json:"netdevs,omitempty"`
	ATPort    string   `json:"at_port,omitempty"`  // 本机实际用的 AT 口（就是 mod.ATPort）
	QMICtrl   string   `json:"qmi_ctrl,omitempty"` //
	MBIMDev   string   `json:"mbim_dev,omitempty"` //
	WwanPorts []string `json:"wwan_ports,omitempty"`
	Note      string   `json:"note,omitempty"`
	// Model / Kinds：命中**模块支持清单**（modem_catalog.go，数据来自 QModem
	// docs/support_list.md）时给出 —— 本型号在 PCIe 形态下支持哪几种数据协议（qmi/mbim/rmnet/gobinet）。
	Model string   `json:"model,omitempty"`
	Kinds []string `json:"kinds,omitempty"`
}

// KindsText 清单里的 PCIe 模式串（未命中清单时为空）
func (p *PCIeInfo) KindsText() string {
	if p == nil || len(p.Kinds) == 0 {
		return ""
	}
	return catalog.CatalogKindsText(p.Kinds)
}

// SupportsKind 本型号的 PCIe 形态支持这一档（型号不在清单里 → false，不当作支持）
func (p *PCIeInfo) SupportsKind(kind string) bool {
	if p == nil {
		return false
	}
	return catalog.CatalogKindInList(p.Kinds, strings.ToLower(strings.TrimSpace(kind)))
}

// Kind 这台 PCIe 模组该按哪种协议拨号：
//
//	有 QMI 控制口 → qmi（主机侧拨号器拨：quectel-CM 支持 MHI，见 third_party/quectel-cm/device.c 的
//	                mhidevice_detect："qrtr-N" / "/dev/mhi_xxx" 两条路它自己会认）
//	有 MBIM 控制口 → mbim（netifd 的 mbim 协议 + umbim 拨）
//	都没有           → 空串（调用方按 unknown 处理，不猜）
func (p *PCIeInfo) Kind() string {
	if p == nil {
		return ""
	}
	if strings.TrimSpace(p.QMICtrl) != "" {
		return "qmi"
	}
	if strings.TrimSpace(p.MBIMDev) != "" {
		return "mbim"
	}
	return ""
}

// Label 总线类型的显示名
func (p *PCIeInfo) Label() string {
	if p == nil {
		return ""
	}
	switch p.Kind() {
	case "qmi":
		return "QMI（PCIe/MHI）"
	case "mbim":
		return "MBIM（PCIe）"
	}
	return "PCIe"
}

// ====================== 枚举 ======================

// ====================== AT / 控制口候选 ======================

// ====================== 从 AT 口反查 / 分组 ======================

// PCIeInfoFor 取这台模组对应的 PCIe 信息（不是 PCIe 模组则返回 nil）
func PCIeInfoFor(mod *Modem) *PCIeInfo {
	if mod == nil || strings.TrimSpace(mod.ATPort) == "" {
		return nil
	}
	if !netdev.IsCharATDevice(mod.ATPort) {
		return nil
	}
	dev, ok := netdev.PcieDeviceForPort(mod.ATPort)
	if !ok {
		return catalogAnnotatePCIe(&PCIeInfo{ATPort: mod.ATPort,
			Note: "这个 AT 口是 PCIe/MHI 类型的字符设备，但没查回对应的 PCI 设备"}, mod)
	}
	info := &PCIeInfo{
		BDF: dev.BDF, VID: dev.VID, PID: dev.PID, Driver: dev.Driver, MHI: dev.MHI,
		Netdevs: dev.Netdevs, ATPort: mod.ATPort, QMICtrl: dev.QMICtrl, MBIMDev: dev.MBIMDev,
		WwanPorts: dev.WwanPorts,
	}
	if dev.QMICtrl == "" && dev.MBIMDev == "" {
		info.Note = "没找到 QMI/MBIM 控制口（内核驱动可能只交了 AT 与网卡）—— 拨号按网卡 dhcp 处理"
	}
	return catalogAnnotatePCIe(info, mod)
}

// catalogAnnotatePCIe 把**模块支持清单**（QModem docs/support_list.md）里的 PCIe 模式补进 PCIeInfo：
// 本型号 PCIe 形态支持哪几档、跟本机探测到的控制口是否一致。型号不在清单里就什么都不做。
func catalogAnnotatePCIe(info *PCIeInfo, mod *Modem) *PCIeInfo {
	if info == nil {
		return nil
	}
	e, ok := catalog.CatalogEntryForModel(modCatalogKeys(mod))
	if !ok {
		return info
	}
	info.Model = strings.ToUpper(e.Model)
	info.Kinds = append([]string(nil), e.PCIe...)
	hint := "QModem 支持列表：本型号 PCIe " + e.PCIeText() + "（USB " + e.USBText() + "）"
	switch {
	case !e.SupportsPCIe():
		hint = "⚠️ " + hint + " —— 支持列表里**没有 PCIe 形态**，但本机这个 AT 口是字符设备（拨号以实际探到的控制口为准）"
	case info.Kind() != "" && !catalog.CatalogKindInList(e.PCIe, info.Kind()):
		hint = "⚠️ " + hint + "，本机探到的控制口是 " + strings.ToUpper(info.Kind()) + "（以本机探测为准）"
	}
	info.Note = strings.TrimSpace(info.Note + " " + hint)
	return info
}

// pcieCtrlDeviceForModem 给自动组网/拨号器用：这台 PCIe 模组的 QMI 或 MBIM 控制口
func pcieCtrlDeviceForModem(mod *Modem) string {
	info := PCIeInfoFor(mod)
	if info == nil {
		return ""
	}
	if info.QMICtrl != "" {
		return info.QMICtrl
	}
	return info.MBIMDev
}

// pcieHostSetup PCIe 模组的"主机侧还要做"说明（DialState.HostSetup）。
//
// ⚠️ 必须与 USB 口径分开：USB 那边写的是 `quectel-CM -i wwan0 …` / `umbim -d /dev/cdc-wdm0 …`，
// 而 PCIe 上 ① 拨号器的 `-i` 是筛选语义（MHI 网卡名各家不同，传错就一台设备都找不到，
// 所以本实现**故意不传**），② 控制口是 /dev/mhi_QMI0 这类而不是 cdc-wdmN。
// 照抄 USB 那句会把用户带沟里，所以这里按实际形态给。
func pcieHostSetup(info *DialTypeInfo) string {
	if info == nil || info.PCIe == nil {
		return ""
	}
	p := info.PCIe
	switch p.Kind() {
	case "qmi":
		ctrl := p.QMICtrl
		if ctrl == "" {
			ctrl = "/dev/mhi_QMI0"
		}
		netdev := ""
		if len(p.Netdevs) > 0 {
			netdev = p.Netdevs[0]
		}
		return fmt.Sprintf("主机侧拨号：厂商拨号器（本包自带的 quectel-CM 已含 MHI 支持，它自己会扫 "+
			"/sys/bus/mhi/devices 找网卡与 %s）—— `quectel-CM -4 -6 -s <APN>`；"+
			"⚠️ **不要传 `-i`**：MHI 的网卡名各驱动不同（wwan0 / mhi_wwan0 / rmnet_mhi0），"+
			"名字对不上它会直接找不到设备。地址最终落在 %s 上。", ctrl, orFallback(netdev, "模组交出来的 wwan 网卡"))
	case "mbim":
		dev := orFallback(p.MBIMDev, "/dev/wwan0mbim0")
		return fmt.Sprintf("主机侧拨号：`umbim -d %s connect`（OpenWrt 用 `proto=mbim` + `device=%s` + APN，"+
			"netifd 会调 umbim）；Windows 由内置 MBIM 驱动处理。", dev, dev)
	}
	return "PCIe 模组已识别，但没找到 QMI / MBIM 控制口：数据面只能按网卡 DHCP 处理" +
		"（内核可能只交了 AT 与网卡，检查 kmod-mhi-net / kmod-wwan 是否装上）。"
}

// pcieRescueTargets 现在有哪些 PCIe 模组可以"复位"（AT 口不应答时用）
func pcieRescueTargets() []netdev.PCIDevice {
	devs := netdev.ListPCIModemDevices()
	out := make([]netdev.PCIDevice, 0, len(devs))
	for _, d := range devs {
		if d.Bound {
			out = append(out, d)
		}
	}
	return out
}
