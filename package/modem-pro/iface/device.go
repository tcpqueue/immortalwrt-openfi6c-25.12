package iface

import "strings"

// ====================== 设备上下文（厂商层能看到的"只读快照"） ======================
//
// 背景：厂商层的方言表（锁网候选 / 平台变体 / 组网模式 / USB 网卡模式 / IMEI 改写）
// 原先一律收 `*kernel.Modem`（因为它们的闭包签名就是这么写的）。这带来两个后果：
//
//	① 那些表**搬不出内核**（一搬就要 import kernel ⇒ 循环依赖）；
//	② 厂商层握到了整个内核对象（锁、缓存、串口都在手边），边界形同虚设。
//
// DeviceInfo 把这些表真正需要的信息压成**一个不可变值**：哪一家、什么型号、指令集是谁。
// 表和工厂改成收它之后，就能整体搬进 module/，而与内核再无关系。
//
// ⚠️ 这里**故意不放** `*Modem`、也不放任何指针到内核状态：
// 谁要是想加 `SendAT` 之类的能力，应该走 ATDoer / LockATDoer（见 doer.go）。
type DeviceInfo struct {
	// Vendor 已识别出的厂商（未识别 = VendorUnknown，此时靠型号/平台判断）
	Vendor ModemVendor
	// Model ATI 解析出的完整型号串（可能带 "ATI|Manufacturer: …" 前后缀）
	Model string
	// ModelName 界面显示用的短型号
	ModelName string
	// Command 当前型号的指令集实现（可选）。
	// 放在这里是为了让方言工厂能**在构造时改自己的状态** ——
	// 典型例子：锁小区平台档要写进 `QnwLockCommands.CellProfile`，而那个字段只有
	// 拿到具体实现类型才能设。nil = 还没定指令集。
	Command ModemCommand
}

// Key 型号合并串（与内核 `(*Modem).ModemModel()` 逐字一致）：
// `strings.TrimSpace(Model + " " + ModelName)`。
//
// 大量型号谓词（isHuaweiModel / PlatformForModel / QnwCellLockProfileForModel…）
// 都是按这个串匹配的，所以它就是"型号键"。
func (d DeviceInfo) Key() string {
	return strings.TrimSpace(d.Model + " " + d.ModelName)
}
