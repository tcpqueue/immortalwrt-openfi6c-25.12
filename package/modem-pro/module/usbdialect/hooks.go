package usbdialect

// ====================== 内核注入点 ======================
//
// `platformVariants`（厂商 × 芯片平台的变体表）住在内核，表里同时引用
// USB / IMEI / 组网模式三种方言 ⇒ 它不能反向 import 本包以外的厂商包。
//
// 所以"按型号查平台变体"这件事通过注入点解决：内核在 init() 里灌实现，
// 默认返回 false（= 没有变体，走厂商默认档）—— 安全默认，不会 panic。
//
// ⏭ 等 `platformVariants` 整体搬成 module/variant 之后，这里改成直接调用，桥删掉。

import "modemd/iface"

// LookupVariant 按厂商 + 型号查平台变体里自带的 USB 方言。
var LookupVariant = func(vendor iface.ModemVendor, model string) (*USBDialect, bool) {
	return nil, false
}

// VariantNote 取平台变体的补充说明（追加到方言的 Note 后面；无则空串）。
// 单独开一个注入点是因为有的工厂（gosuncn）**只要说明、不要方言**。
var VariantNote = func(vendor iface.ModemVendor, model string) string { return "" }
