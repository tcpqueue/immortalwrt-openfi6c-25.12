package registry

// ====================== 厂商注册表（唯一注册点） ======================
//
// 这是"加一家厂商要改哪里"的**唯一答案**：新建 `module/<vendor>/` 目录，
// 再到这里加**一条** Entry。其余地方（kernel 的 NewVendorCommand / 候选探测 /
// AT 速查表）都从这张表派生，不再各写一个 switch。
//
// 为什么注册表住在 module/ 而不是 kernel：
//   · 条目里的东西全是 iface 契约类型（`ModemCommand` / `NetLocker`），
//     **不需要**内核的任何类型 ⇒ 可以住在模块层；
//   · 于是"内核依赖注册表"，方向与其它模块一致（kernel → module → iface）。
//
// ⚠️ 这里**只放"厂商级"的东西**。按**型号/平台**才决定的方言（MTK / 巴龙 / 高通的
// 细分、Neoway / Thales / Foxconn 的列表式、OpenLuat / Gosuncn 这些）仍然由
// kernel 的探测表决定 —— 那是"机型知识"，随型号清单走，不适合塞进厂商条目里。

import (
	"modemd/iface"
	"modemd/module/common"
	"modemd/module/fibocom"
	"modemd/module/meig"
	"modemd/module/quectel"
	"modemd/module/simcom"
	"modemd/module/tdtech"
	"modemd/module/unisoc"
)

// Entry 一个厂商的注册项。
//
//	Command —— 指令集工厂（**必填**：每个厂商都必须有一套指令集，认不出来时走 Adaptive）
//	Lock    —— 该厂商"不看型号就成立"的默认锁网方言（可选；按型号分的方言不写在这里）
type Entry struct {
	Command func() iface.ModemCommand
	Lock    func() iface.NetLocker
}

// entries 厂商 → 注册项。**加厂商就加一行**。
var entries = map[iface.ModemVendor]Entry{
	iface.VendorQuectel: {
		Command: func() iface.ModemCommand { return &quectel.QuectelCommand{} },
		Lock:    func() iface.NetLocker { return &quectel.QcfgBandLocker{QuectelCommand: &quectel.QuectelCommand{}} },
	},
	iface.VendorFibocom: {
		Command: func() iface.ModemCommand { return &fibocom.FibocomCommand{} },
		// FM350(MTK) / FM650(展锐) / FM160(高通) 三套方言按型号分 ⇒ 不在这里写死
	},
	iface.VendorSimcom: {
		Command: func() iface.ModemCommand { return &simcom.SimcomCommand{} },
		Lock:    func() iface.NetLocker { return &simcom.SimcomLockCommand{} },
	},
	iface.VendorMeig: {
		Command: func() iface.ModemCommand { return &meig.MeigCommand{} },
	},
	iface.VendorTDTech: {
		Command: func() iface.ModemCommand { return &tdtech.MT5700Command{} },
		Lock:    func() iface.NetLocker { return &tdtech.MT5700Command{} },
	},
	iface.VendorUnisoc: {
		Command: func() iface.ModemCommand { return &unisoc.UnisocCommand{} },
		Lock:    func() iface.NetLocker { return &unisoc.UnisocLockCommand{} },
	},
}

// Vendors 已注册的厂商（顺序 = 下面这个切片，界面/文档用它保证稳定输出）。
var Vendors = []iface.ModemVendor{
	iface.VendorQuectel,
	iface.VendorFibocom,
	iface.VendorSimcom,
	iface.VendorMeig,
	iface.VendorTDTech,
	iface.VendorUnisoc,
}

// Command 取该厂商的指令集；未注册（含未知厂商）一律返回自适应兜底。
//
// 兜底是**行为契约**：未识别型号必须能用（首选指令与移远一致 + 运行期候选回退），
// 不允许返回 nil。
func Command(v iface.ModemVendor) iface.ModemCommand {
	if e, ok := entries[v]; ok && e.Command != nil {
		return e.Command()
	}
	return &common.UnknownCommand{}
}

// NewLock 取该厂商的默认锁网方言；没有（或按型号分）返回 nil。
// nil 是**正常**返回值：调用方要接着走机型/平台探测，不要当成错误。
func NewLock(v iface.ModemVendor) iface.NetLocker {
	if e, ok := entries[v]; ok && e.Lock != nil {
		return e.Lock()
	}
	return nil
}

// Registered 该厂商是否在表里（文档/自检用）
func Registered(v iface.ModemVendor) bool {
	_, ok := entries[v]
	return ok
}
