package usbids

// 本文件由 extract-decls.py 从 kernel 抽出（按名字表 + 括号配平），
// 内容逐字保留，仅把需要跨包使用的名字首字母大写。

import (
	"strings"

	"modemd/iface"
)

// 厂商标识与厂商常量的本包别名（表里的字段/字面量都用这些短名字）。
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

// ModemUSBFamily 一个厂商在 USB 上的身份（VID + 常见 PID）。
//
// 用途是“快速筛掉无关设备”，**不是**安全边界：命中之后还要看类码/能不能 AT 应答。
// 所以宁可少写也不要乱写 —— 误判的代价是去动别人设备的驱动绑定。
type ModemUSBFamily struct {
	Vendor ModemVendor // 厂商标识（与 atcmd 一致：quectel / fibocom / tdtech / simcom）
	VID    string      // 四位十六进制小写
	PIDs   []string    // 已知 PID（小写十六进制）；空 = 看 VendorOnlyOK / 类码
	Note   string
	// VendorOnlyOK 这个 VID 是该厂商独占的吗（如鼎桥 2deb、芯讯通 1e0e）。
	// ⚠️ 只有确认独占的才能给 true：联发科 0e8d 手机平板都在用，必须靠 PID 卡。
	VendorOnlyOK bool
}

// ModemUSBFamilies 已知模组的 USB 身份表。
//
// ⚠️ 0e8d 是**联发科通用 VID**（手机/平板/各种 MTK 设备都用它），
// 所以这里只认 MTK 平台模组的常见 PID 段，不能只靠 VID 放行 —— 见 IsKnownModemUSB。
var ModemUSBFamilies = []ModemUSBFamily{
	// 移远的 2c7c 是厂商自有 VID，但它下面挂着**高通 / 展锐 / MTK 三种平台**的模组，
	// PID 段各不一样 ⇒ 必须按 PID 卡，不能写 VendorOnlyOK。
	// 清单来源：QModem 的型号表（`modem_support.json` 的 `device.quectel` 三个平台段）
	// + 本仓库 `third_party/quectel-cm/device.c` 里那张 AT 口映射表（0x0900=RG500U …）。
	// ⚠️ 2026-10-09 真机踩到：**原来表里只有高通那批（0121…0802）**，展锐平台的
	// `2c7c:0900`（RM500U-CNV）不被认成“已知模组” ⇒ USB 热插拔页看不到它，
	// 「命令口绑定」的设备下拉是空的（只有顶部模组列表里有它），而且 AT 通道卡死时
	// 也不会被自动 USB 软重插救（那套只对“已知模组”动手）。展锐/MTK 那两批必须留着。
	{Vendor: VendorQuectel, VID: "2c7c",
		PIDs: []string{
			// 高通平台：EC20/EC21/EC25/EG91/EG95/BG96/EG06/EG12/EG20/RG500/RG520 等
			"0121", "0122", "0125", "0127", "0133", "0191", "0195", "0296", "0306", "030b",
			"0435", "0452", "0455", "0512", "0620", "0800", "0801", "0802",
			// 展锐平台：RM500U / RG500U / EC200U / EC200D / EC200S / EC200A / EC100Y / RG801H …
			"0900", "0901", "0902", "0903", "0904", "6001", "6002", "6004", "6005",
			"6006", "6007", "6026", "8101",
			// MTK 平台：0700 / 7001 / 7003
			"0700", "7001", "7003",
		},
		Note: "高通（EC20/EC25/RG500…）+ 展锐（RM500U/RG500U/EC200U…）+ MTK（0700/7001/7003）"},
	{Vendor: VendorFibocom, VID: "0e8d",
		PIDs: []string{"7003", "7005", "7101", "7126", "7127"}, Note: "MTK 平台（FM150/FM350 等）"},
	// 广和通**自有 VID**（2cb7）：FM650-CN（展锐平台）实测 2cb7:0a06。
	// ⚠️ 以前表里没有它 → FM650 不被 USB 层认成模组：USB 热插拔页看不到它，
	// 而且模组 AT 通道卡死时不会被自动 USB 软重插救（那套只对"已知模组"动手）。
	// 下面这几条（FG150/FM150/FG101/NL668）与 0a06 一样取自广和通自家的 qmi_wwan 驱动
	// （third_party/kmods/fibocom_QMI_WWAN/src/qmi_wwan_f.c 的 products[]）。
	{Vendor: VendorFibocom, VID: "2cb7", PIDs: []string{"0a06", "0104", "0109", "0113"},
		Note: "FM650-CN（展锐平台）/ FG150 / FM150 / FG101 等"},
	{Vendor: VendorFibocom, VID: "1508", PIDs: []string{"1000", "1001"}, Note: "NL668"},
	{Vendor: VendorFibocom, VID: "05c6", PIDs: []string{"9025"}, Note: "NL668（高通 VID）"},
	// 实测（2026-09-30，本机 MT5700M-CN）：VID_3466&PID_3301，4 个接口
	// （SUBClass_06&PROT_12/13/14/1C），AT 口是「PC UI Interface」那条。
	{Vendor: VendorTDTech, VID: "3466", PIDs: []string{"3301"}, Note: "鼎桥 TD Tech / TDTECH Connect"},
	// 鼎桥/芯讯通的 VID 是厂商独占的：拿不到类码时（Windows 上常见）也得能认出来
	{Vendor: VendorSimcom, VID: "1e0e", VendorOnlyOK: true, Note: "SIM8200/A7900 等"},
	// SIM8200 在 "QMI/Modem 模式" 下会枚举成高通的 05c6:90db —— 这条取自 Simcom 自家的
	// qmi_wwan 驱动（third_party/kmods/qmi_wwan_simcom/qmi_wwan_simcom.c 的 products[]），
	// 与 1e0e:9001 是同一张卡的不同枚举方式（05c6 是高通通用 VID，所以必须靠 PID 卡）。
	{Vendor: VendorSimcom, VID: "05c6", PIDs: []string{"90db"}, Note: "SIM8200（QMI 模式枚举）"},
	// 美格智能（MeiG/MeigLink）：2dee 是它自有 VID（SRM815 实测 2dee:4d22），
	// SLM750/SLM730 用的是高通通用 VID 05c6（所以必须靠 PID 卡，不能用 VendorOnlyOK）。
	// PID 取自美格自家的 qmi_wwan 驱动（third_party/kmods/meig_QMI_WWAN/src/qmi_wwan_m.c）。
	{Vendor: VendorMeig, VID: "2dee", PIDs: []string{"4d22"}, Note: "SRM815/SRM825 等 5G 模组"},
	{Vendor: VendorMeig, VID: "05c6", PIDs: []string{"f601"}, Note: "SLM750/SLM730（高通 VID）"},
}

// SplitUSBVidPid 归一 VID/PID 写法（大小写、0x 前缀、内含冒号/短横线都认）。
func SplitUSBVidPid(s string) (vid, pid string) {
	vid, pid = SplitVidPid(s)
	return strings.ToLower(vid), strings.ToLower(pid)
}

// IsKnownModemUSB 判断某个 USB 设备是不是“已知模组”。
//
// 规则（保守优先）：
//   - VID 不在表里 → 不是；
//   - VID 命中且表里给了 PID → 必须 PID 也命中（如 0e8d 是联发科通用 VID，不能放太宽）；
//   - VID 命中、表里没给 PID 但是**厂商独占 VID**（2deb / 1e0e）→ 算；
//   - 否则看接口类码里有没有模组常见的（厂商自定义 ff、MBIM/NCM/RNDIS/ECM）。
func IsKnownModemUSB(vid, pid string, classes []string) (ModemUSBFamily, bool) {
	// 归一写法：大小写、"VID_" 前缀、甚至把 "2c7c:0800" 整体传进来都认
	vid, pid = SplitUSBVidPid(strings.TrimSpace(vid) + ":" + strings.TrimSpace(pid))
	for _, f := range ModemUSBFamilies {
		if f.VID != vid {
			continue
		}
		if len(f.PIDs) > 0 {
			for _, p := range f.PIDs {
				if p == pid {
					return f, true
				}
			}
			continue
		}
		if f.VendorOnlyOK || HasModemLikeClass(classes) {
			return f, true
		}
	}
	return ModemUSBFamily{}, false
}

// HasModemLikeClass 接口类码里有没有“像模组”的那种。
//
// ⚠️ ClassifyUSBClasses 返回的是 **usbnet 取值**（"1" ECM / "2" MBIM / "3" RNDIS / "5" NCM），
// 不是协议名 —— 这里只关心“有没有一种模组常见的数据类”。
func HasModemLikeClass(classes []string) bool {
	for _, c := range classes {
		cc := strings.ToLower(strings.TrimSpace(c))
		if strings.HasPrefix(cc, "ff") {
			return true
		}
		if kind, ok := ClassifyUSBClasses([]string{cc}); ok {
			switch kind {
			case "1", "2", "3", "5":
				return true
			}
		}
	}
	return false
}

// ClassifyUSBClasses 从一组接口类码里推出 usbnet 取值。
//
// ⚠️ 判据是**控制接口（class 02）的子类**，不是"谁先出现"，更不能拿数据接口（class 0a）当协议：
//
//	02/0d    → "5" NCM      （NCM 控制口）
//	02/0e    → "2" MBIM
//	ef/04    → "3" RNDIS
//	02/06    → "1" CDC-ECM  （没写错：ECM 才是 02/06，0a 只是它俩共用的数据口）
//	0a 兜底   → "5" NCM      （只剩数据口、认不出控制口时按 NCM 处理，比"未知"有用）
//
// 曾经的坑：把 `0a` 当成 NCM 放进第一轮，于是**任何 ECM 模组**（02/06 控制口 + 0a 数据口）
// 都会被认成 NCM —— 协议正好被判反。数据口 0a 对 ECM / NCM / MBIM 是一模一样的，
// 只有控制口的子类（06 / 0d / 0e）能区分，所以 ECM 那轮要排在 0a 兜底前面。
func ClassifyUSBClasses(classes []string) (string, bool) {
	// 第一轮：控制口子类 / RNDIS（越具体越优先）
	for _, c := range classes {
		class, sub, _, ok := SplitUSBClassCode(c)
		if !ok {
			continue
		}
		switch {
		case class == "02" && sub == "0e":
			return "2", true
		case class == "02" && sub == "0d":
			return "5", true
		case class == "ef" && sub == "04":
			return "3", true
		}
	}
	// 第二轮：CDC-ECM（必须在 0a 兜底之前）
	for _, c := range classes {
		if class, sub, _, ok := SplitUSBClassCode(c); ok && class == "02" && sub == "06" {
			return "1", true
		}
	}
	// 第三轮：只剩 CDC 数据口（0a）→ 认不出 ECM / NCM / MBIM，按 NCM 处理
	for _, c := range classes {
		if class, _, _, ok := SplitUSBClassCode(c); ok && class == "0a" {
			return "5", true
		}
	}
	return "", false
}

// SplitVidPid 从 "VID_2C7C&PID_0800" / "2c7c:0800" / "2c7c" 里取 VID/PID（小写十六进制）。
func SplitVidPid(s string) (vid, pid string) {
	up := strings.ToUpper(strings.TrimSpace(s))
	up = strings.NewReplacer("VID_", "", "PID_", "").Replace(up)
	up = strings.ReplaceAll(up, ":", "&")
	parts := strings.Split(up, "&")
	get := func(i int) string {
		if i >= len(parts) {
			return ""
		}
		p := strings.ToLower(strings.TrimSpace(parts[i]))
		if !IsHexDigits(p) {
			return ""
		}
		return p
	}
	return get(0), get(1)
}
