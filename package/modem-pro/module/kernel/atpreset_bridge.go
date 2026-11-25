package kernel

import (
	"modemd/module/atpreset"
	"modemd/module/fibocom"
	"modemd/module/meig"
	"modemd/module/quectel"
	"modemd/module/simcom"
	"modemd/module/tdtech"
	"modemd/module/unisoc"
)

// ====================== AT 预设：内核方法 + 别名 ======================
//
// 数据在 modemd/module/atpreset；这里保留两个要读内核状态的方法（mod.Vendor / mod.mu），
// 以及调用点/测试用的别名。

// ATPresets 返回**本模组**的常用指令清单（界面按这个渲染下拉）。
// 厂商还没识别出来时给纯 3GPP 那一份 —— 宁可少，也不要给用户看别家的私有指令。
func (mod *Modem) ATPresets() []atpreset.ATPreset {
	v, _ := mod.VendorSnapshot()
	return ATPresetsFor(v)
}

// VendorSnapshot 加锁读出厂商与显示名（API 层用；避免直接读未加锁的字段）
func (mod *Modem) VendorSnapshot() (ModemVendor, string) {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	return mod.Vendor, mod.VendorName
}

// VendorATPresets 厂商私有指令（**只有这一家会有**）；未识别厂商返回 nil。
//
// 派发放在内核：品牌包只提供「自己那家的表」，谁支持什么由这里决定。
// 反过来让 atpreset 包去 import 品牌包会形成循环（品牌包要 import atpreset 拿类型）。
func VendorATPresets(v ModemVendor) []atpreset.ATPreset {
	switch v {
	case VendorQuectel:
		return quectel.ATPresets()
	case VendorFibocom:
		return fibocom.ATPresets()
	case VendorSimcom:
		return simcom.ATPresets()
	case VendorMeig:
		return meig.ATPresets()
	case VendorTDTech:
		return tdtech.ATPresets()
	case VendorUnisoc:
		return unisoc.ATPresets()
	}
	return nil
}

// ATPresetsFor 按厂商拼出完整清单（合并规则见 atpreset.Combine）。
func ATPresetsFor(v ModemVendor) []atpreset.ATPreset {
	var suppressed []string
	if v == VendorUnisoc {
		suppressed = unisoc.SuppressedCommonPresets()
	}
	return atpreset.Combine(VendorATPresets(v), suppressed)
}
