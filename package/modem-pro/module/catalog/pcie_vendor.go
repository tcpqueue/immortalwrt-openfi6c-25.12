package catalog

// 本文件由 extract-decls.py 从 kernel 抽出（按名字表 + 括号配平），
// 内容逐字保留，仅把需要跨包使用的名字首字母大写。

import (
	"modemd/iface"
	"strings"
)

// PCIEVendorNames 5G 模组**芯片/模组厂**的 PCI VID（只在"没绑驱动"时用来提示，见 pcieLooksLikeModem）。
// 键是四位小写十六进制（去掉 0x）。
var PCIEVendorNames = map[string]string{
	"17cb": "高通 Qualcomm（MHI）",
	"14c3": "联发科 MediaTek（T7xx）",
	"105b": "富士康 Foxconn（T99W 系）",
	"2c7c": "移远 Quectel",
	"2cb7": "广和通 Fibocom",
	"1e0e": "芯讯通 SIMCom",
	"2dee": "美格 MeiG",
	"05c6": "高通（模组模式枚举）",
	"0e8d": "联发科（模组模式枚举）",
}

// PCIEVendorOf 由 PCI VID 推厂商（认不出返回空，不猜）

// 本包用到的短名字（原文件里是 kernel 的包内名字）。
type ModemVendor = iface.ModemVendor

const (
	VendorQuectel = iface.VendorQuectel
	VendorFibocom = iface.VendorFibocom
	VendorSimcom  = iface.VendorSimcom
	VendorMeig    = iface.VendorMeig
	VendorTDTech  = iface.VendorTDTech
	VendorUnisoc  = iface.VendorUnisoc
	VendorUnknown = iface.VendorUnknown
)

func vendorLabelOf(v ModemVendor) string { return iface.VendorLabel(v) }

func PCIEVendorOf(vid string) (ModemVendor, string) {
	switch strings.ToLower(strings.TrimSpace(vid)) {
	case "2c7c":
		return VendorQuectel, iface.VendorLabel(VendorQuectel)
	case "2cb7", "1508":
		return VendorFibocom, iface.VendorLabel(VendorFibocom)
	case "1e0e":
		return VendorSimcom, iface.VendorLabel(VendorSimcom)
	case "2dee":
		return VendorMeig, iface.VendorLabel(VendorMeig)
	case "105b":
		// 富士康没有独立的 ModemVendor 标识（机型特例走 isFoxconnModel 谓词，见 atcmd.go）：
		// 这里只把显示名给它，标识保持 unknown —— 与 USB 那边一致，不编一个厂商出来。
		return VendorUnknown, "富士康 Foxconn（T99W 系）"
	}
	// 17cb / 14c3 / 05c6 / 0e8d 是芯片厂的 VID，模组牌子要看 ATI（不在这里猜）
	if name, ok := PCIEVendorNames[strings.ToLower(strings.TrimSpace(vid))]; ok {
		return VendorUnknown, name
	}
	return VendorUnknown, ""
}
