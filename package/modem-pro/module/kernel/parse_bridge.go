package kernel

import (
	cellpkg "modemd/module/cell"
	"modemd/module/common"
	"modemd/module/lock"
	"modemd/module/tdtech"
	"modemd/module/usbdialect"
	"modemd/module/variant"
)

// 把 kernel 的 NR 信号解析实现注入厂商层。
//
// 背景见 module/common/hooks.go：厂商指令类型的 ParseNRSignal 统一用
// module/kernel/cell.go 的 ParseServingNRSignal，而它依赖 cell.go 的整条解析链
// （ParseCellLine → CellInfo → 一堆字段解析），整体搬走会带出上千行与厂商无关的代码。
//
// ⏭ 等解析层整体搬成 module/parse 之后，本文件**整个删掉**，厂商包直接 import 那个包。
// 注入点只有这一处 —— 不要在别处再改这个变量。
func init() {
	common.ParseServingNRSignal = cellpkg.ParseServingNRSignal
	common.ParseCPSISignal = parseCPSISignal
	common.ParseLooseNRSignal = parseLooseNRSignal

	// 鼎桥 MT5700 的 ParseNRSignal 要用 ^MONSC / ^HCSQ 两个通用解析器。
	// 那两个函数返回内核的 CellInfo，所以只能在厂商层留窄接口、在这里注入。
	tdtech.ParseMONSCSignal = parseMONSCSignal
	tdtech.ParseHCSQSignal = cellpkg.ParseHCSQ
	tdtech.SetLogger(logger)

	// 锁网方言（module/lock）也用内核的日志实例。
	lock.SetLogger(logger)

	// USB 方言表按型号查平台变体：变体表还在内核（见 module/usbdialect/hooks.go）。
	usbdialect.LookupVariant = lookupUSBVariant
	usbdialect.VariantNote = variantNote
}

// lookupUSBVariant 把内核的 platformVariantFor 包成"只要 USB 方言"的窄接口，
// 不把整个 platformVariant 泄漏给厂商包。
func lookupUSBVariant(vendor ModemVendor, model string) (*usbdialect.USBDialect, bool) {
	v := variant.PlatformVariantFor(vendor, model)
	if v == nil || v.Usb == nil {
		return nil, false
	}
	return v.Usb(), true
}

// variantNote 只把变体的说明文字给出去
func variantNote(vendor ModemVendor, model string) string {
	if v := variant.PlatformVariantFor(vendor, model); v != nil {
		return v.Note
	}
	return ""
}

// parseMONSCSignal 把 `^MONSC: …` 包成"信号三元组"，只在内核认它且是服务小区、
// 且 RSRP 有效时返回 true（与搬家前 tdtech.MT5700Command.ParseNRSignal 里的判定逐字一致）。
func parseMONSCSignal(line string) (rsrp, rsrq, sinr int, ok bool) {
	ci, parsed := cellpkg.ParseMONSC(line)
	if parsed && ci.Serving && ci.RSRP != 0 {
		return ci.RSRP, ci.RSRQ, ci.SINR, true
	}
	return 0, 0, 0, false
}

// parseLooseNRSignal 宽容解析：先按已实机验证的 +QENG/+FENG 格式，
// 再对 +CPSI/+QCSQ 这类无固定文档的格式按量纲定位。
func parseLooseNRSignal(line string) (rsrp, rsrq, sinr int, ok bool) {
	if rsrp, rsrq, sinr, ok = cellpkg.ParseServingNRSignal(line); ok {
		return rsrp, rsrq, sinr, true
	}
	if !cellpkg.IsSignalReportLine(line) {
		return 0, 0, 0, false
	}
	return cellpkg.ParseSignalByDimension(line)
}

// parseCPSISignal 把 kernel 的 +CPSI 解析包成"信号三元组"，
// 只暴露厂商层真正需要的三个数（不把 CellInfo 这种内核类型泄漏出去）。
func parseCPSISignal(line string) (rsrp, rsrq, sinr int, ok bool) {
	cell, parsed := cellpkg.ParseCPSICell(line)
	if !parsed || cell.RSRP == 0 {
		return 0, 0, 0, false
	}
	return cell.RSRP, cell.RSRQ, cell.SINR, true
}
