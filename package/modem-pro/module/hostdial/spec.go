package hostdial

// 本文件由 extract-decls.py 从 kernel 抽出（按名字表 + 括号配平），
// 内容逐字保留，仅把需要跨包使用的名字首字母大写。

import (
	"modemd/iface"
)

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

// SimcomStyle 这家模组走的是 simcom-cm 那一套（程序名/参数/已知坑都与 quectel-CM 不同）。
//
// 两个用处（都在 kernel 的薄壳里）：
//   - 参数组装：simcom-cm 只认 `-s <apn>` 与 `-6`（**不认 `-4`**，给了会报错）
//   - 卡住提示：simcom-cm 只看 QMI 的 EPS 注册，纯 5G SA 下 +C5GREG 已注册但没有 +CEREG，
//     它会一直报 PS: Detached、拨不上去
func SimcomStyle(vendor ModemVendor) bool { return vendor == VendorSimcom }

// KindVendor 该用哪家的拨号器 —— **通常就是模组厂商**，唯一例外是 MBIM。
//
// MBIM 的会话建立（MBIM_CID_CONNECT）**只有 quectel-CM 实现了**：
//   - fibocom-dial / simcom-cm 源码里压根没有 MBIM 那套（连 get_driver_type 都没有）；
//   - meig-cm 虽然带了 mbim-cm.c，但没有任何地方把它接上。
//
// 而 quectel-CM 认 MBIM 靠的是**标准 USB CDC 描述符**（bInterfaceClass=COMM +
// bInterfaceSubClass=MBIM），与厂商无关 ⇒ 拿它去拨广和通/美格的 MBIM 模组是对路的。
//
// 真机实测（2026-10-13 FM160-CN / 2cb7:0111 MBIM）：quectel-CM 自动找到
// /dev/cdc-wdm0 + wwan0，读 caps / subscriber / register(Home) / attach 都没问题。
func KindVendor(vendor ModemVendor, isMBIM bool) ModemVendor {
	if isMBIM {
		return VendorQuectel
	}
	return vendor
}

// Spec 一个厂商对应的主机侧拨号器：名字、候选路径、找不到时的补救提示。
type Spec struct {
	Name  string
	Paths []string
	Hint  string
}

// SpecFor 这台模组该用**哪个**主机侧拨号器。
//
// ⚠️ 拨号器要按**厂商**分，不能一把梭 —— QMI 的初始化序列与厂商私有服务各家不一样：
//
//	· Quectel（移远）：quectel-CM（本项目自带源码，见 third_party/quectel-cm）
//	· SIMCom（芯讯通，SIM7600/SIM8200 这类）：**simcom-cm**（SIMCom 官方给的那份）
//	· Fibocom（广和通，FM150/FM350/FM650 这类）：**fibocom-dial**（third_party/fibocom-dial）
//	· MeiG（美格，SRM815/SLM750 这类）：**meig-cm**（third_party/meig-cm）
//
// 实测（2026-10-03，SIM8200EA-M2）用 quectel-CM 拨 SIMCom 模组：
// 控制口能打开、`requestSetupDataCall` 也发出去了，但模块回 `QMUXError = 0xe`
// （call_end_reason 1），而且一开始就报 `QMIType 11 has no clientID` —— 那是高通/移远
// 才有的 WDA 服务，SIMCom 固件不提供，所以要换成它自家的 simcom-cm。
// Fibocom / MeiG 各自带的是自家 QMI 初始化序列（各家 MPQMUX/QMIThread 都是自己那份），
// 同理不能拿别家的去拨。
//
// 顺序都是"本包的私有路径优先，其次固件自带"（互不干扰，见 README）。
// 用 [net] host_dial_bin 可以无视厂商指定一个。
// 认不出来厂商（unknown）或没为它准备拨号器时回落到 quectel-CM（覆盖面最广的那份）。
func SpecFor(vendor ModemVendor) Spec {
	switch vendor {
	case VendorSimcom:
		return Spec{
			Name:  "simcom-cm",
			Paths: Paths("simcom-cm"),
			Hint: "SIMCom 模组（SIM7600/SIM8200 这类）要用它自家的 simcom-cm（" +
				"quectel-CM 在它上面拨不通：会报 QMIType 11 has no clientID / QMUXError 0xe）。" +
				"它随 modemd-pro 装在 /usr/lib/modemd/simcom-cm；或用 [net] host_dial_bin 指一个",
		}
	case VendorFibocom:
		return Spec{
			Name:  "fibocom-dial",
			Paths: Paths("fibocom-dial"),
			Hint: "广和通模组（FM150/FM350/FM650 这类）要用它自家的 fibocom-dial（" +
				"QMI 初始化序列与移远/芯讯通不同，拿 quectel-CM 拨会卡在拿不到 WDS client）。" +
				"它随 modemd-pro 装在 /usr/lib/modemd/fibocom-dial；或用 [net] host_dial_bin 指一个",
		}
	case VendorMeig:
		return Spec{
			Name:  "meig-cm",
			Paths: Paths("meig-cm"),
			Hint: "美格模组（SRM815/SLM750 这类）要用它自家的 meig-cm（拿 quectel-CM 拨会卡在拿不到 WDS client）。" +
				"它随 modemd-pro 装在 /usr/lib/modemd/meig-cm；或用 [net] host_dial_bin 指一个",
		}
	}
	return Spec{
		Name:  "quectel-CM",
		Paths: Paths("quectel-CM"),
		Hint:  "它随 modemd-pro 一起装到 /usr/lib/modemd/quectel-CM；装回本包，或用 [net] host_dial_bin 指一个",
	}
}

// Paths 一个拨号器的候选路径：本包私有路径优先（很多 5G 固件自带
// /usr/bin/quectel-CM，装到同路径会 opkg 冲突，所以我们的装在 /usr/lib/modemd/），
// 其次固件自带的 /usr/bin、/usr/sbin。
func Paths(name string) []string {
	return []string{
		"/usr/lib/modemd/" + name,
		"/usr/bin/" + name,
		"/usr/sbin/" + name,
	}
}
