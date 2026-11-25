// Package iface 是「契约层」：只放接口、枚举与稳定的纯类型。
//
// 为什么要单独成包：厂商实现（module/<vendor>）需要引用这些契约，
// 但又**绝不能**反向依赖内核（kernel）—— 否则就是循环依赖。
// 所以把「内核与厂商都认可的公共语言」提出来放在这里，
// 依赖方向固定为：kernel ──> module ──> iface，永远单向。
//
// ⚠️ 本包必须保持**窄**：只放两边都真的需要的东西。
// 一旦这里膨胀成新的「共享内核」，它自己就变成了新的耦合中心，
// 那就白拆了。新加东西前先问：内核和厂商都需要它吗？
package iface

// ModemVendor 模组厂商类型
type ModemVendor string

const (
	VendorUnknown ModemVendor = "unknown"
	VendorQuectel ModemVendor = "quectel" // 移远
	VendorFibocom ModemVendor = "fibocom" // 广和通
	VendorSimcom  ModemVendor = "simcom"  // 芯讯通
	// VendorMeig 美格智能（MeiG / MeigLink，SRM815/SRM825 这类 5G 模组，SLM750/SLM770 是 4G）。
	VendorMeig ModemVendor = "meig"
	// VendorTDTech 鼎桥（TD Tech，MT5700 系列；ATI 里写作 "TD Tech Ltd."）。
	// ⚠️ 以前这个标识叫 "mt5700" —— 那是**型号**不是厂商，界面上"厂商"一栏看着就不对。
	VendorTDTech ModemVendor = "tdtech"
	// VendorUnisoc 展锐（Unisoc / 紫光展锐）。目前实际服务于
	// **中兴 F50（UMS9620 SoC 内置 5G 基带）**：AT 口是 SIPC 虚拟串口
	// `/dev/stty_nr2`（直连命令口，见 atdev_mu300_policy.go），**ATI 不被支持**。
	VendorUnisoc ModemVendor = "unisoc"
)

// VendorLabel 厂商的可读名称（界面「厂商」一栏用）。
// 未识别的厂商原样返回标识（不要编一个名字出来）。
func VendorLabel(v ModemVendor) string {
	switch v {
	case VendorQuectel:
		return "移远 Quectel"
	case VendorFibocom:
		return "广和通 Fibocom"
	case VendorSimcom:
		return "芯讯通 SIMCom"
	case VendorMeig:
		return "美格智能 MeiG"
	case VendorTDTech:
		return "鼎桥 TD Tech"
	case VendorUnisoc:
		return "展锐 Unisoc"
	case VendorUnknown:
		return "未识别"
	}
	return string(v)
}

// AllVendors 全部已知厂商（用于需要枚举的场合，如识别兜底与界面选项）。
// 新增厂商时**只改这一行**，别去散落的 switch 里补 case。
var AllVendors = []ModemVendor{
	VendorQuectel,
	VendorFibocom,
	VendorSimcom,
	VendorMeig,
	VendorTDTech,
	VendorUnisoc,
}
