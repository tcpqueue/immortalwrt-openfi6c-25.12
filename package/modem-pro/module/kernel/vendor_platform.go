package kernel

// ====================== 厂商 × 芯片平台的变体表：只剩一个转换点 ======================
//
// 整张表（机型家族 / 平台档 / 锁网候选 / 三种方言的按型号分派）已搬到
// modemd/module/variant —— 加机型/平台变体不用再动这里。
//
// 本文件只保留 `device()`：把内核的模组状态压成厂商层能看的只读值
// （见 iface/DeviceInfo）。别名见 variant_alias.go。

import "modemd/iface"

// device 把内核的模组状态压成厂商层能看的只读值（见 iface.DeviceInfo）。
//
// 方言表只关心"哪一家、什么型号、指令集是谁" —— 这三样都与内核的锁/缓存无关，
// 所以只传一个值进去，而不是把 *Modem 透给厂商层。
func (mod *Modem) device() iface.DeviceInfo {
	return iface.DeviceInfo{
		Vendor:    mod.Vendor,
		Model:     mod.Model,
		ModelName: mod.ModelName,
		Command:   mod.cmdImpl,
	}
}
