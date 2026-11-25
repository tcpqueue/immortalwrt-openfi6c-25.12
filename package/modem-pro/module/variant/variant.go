package variant

// ====================== 厂商 × 芯片平台的变体表 ======================
//
// 从 module/kernel/vendor_platform.go 整块搬过来：机型家族 / 平台档 / 锁网候选 /
// USB · IMEI · 组网模式三种方言的按型号分派，全在这一张表里。
//
// ⚠️ 唯一留在内核的是 `(*Modem) device()` —— "内核状态 → iface.DeviceInfo" 的转换点。
// 表里所有工厂收的都是 DeviceInfo，所以搬出来之后
// **加机型 / 加平台变体完全不用动 kernel**。
//
// ⚠️ 依赖方向：本包 import usbdialect（表里要用它的方言工厂）⇒
// usbdialect 反过来**不能** import 本包，它靠 `LookupVariant` 注入点
// （见 module/usbdialect/hooks.go）。这条方向不能改，否则成环。
//
// ⚠️ 结构体字段（note/Locker/LockBase/family/flavor/usb/imei/mode…）跨包必须导出；
// kernel 侧的访问由 fix-field-exports.py 按编译器报错逐列对齐。

import (
	"fmt"
	"strings"

	"modemd/iface"
	"modemd/module/common"
	"modemd/module/fibocom"
	"modemd/module/foxconn"
	"modemd/module/gosuncn"
	"modemd/module/huawei"
	"modemd/module/imeidialect"
	"modemd/module/meig"
	"modemd/module/modedialect"
	"modemd/module/neoway"
	"modemd/module/openluat"
	"modemd/module/platform"
	"modemd/module/quectel"
	"modemd/module/sierra"
	"modemd/module/simcom"
	"modemd/module/tdtech"
	"modemd/module/telit"
	"modemd/module/thales"
	"modemd/module/unisoc"
	"modemd/module/usbdialect"
)

// ---- 外部契约 / 各包类型的本地别名（搬过来的函数体零改动）----

type (
	ModemVendor = iface.ModemVendor
	NetLocker   = iface.NetLocker
	DeviceInfo  = iface.DeviceInfo

	usbDialect  = usbdialect.USBDialect
	imeiDialect = imeidialect.ImeiDialect
	modeDialect = modedialect.Dialect

	// lockCandidate 的真身在本包（从 module/kernel/lock.go 搬来，名叫 LockCandidate）
	lockCandidate = LockCandidate

	QuectelCommand     = quectel.QuectelCommand
	FibocomCommand     = fibocom.FibocomCommand
	fibocomMTKCommand  = fibocom.FibocomMTKCommand
	MT5700Command      = tdtech.MT5700Command
	simcomLockCommand  = simcom.SimcomLockCommand
	qnwCellLockProfile = quectel.CellLockProfile
)

var (
	VendorQuectel = iface.VendorQuectel
	VendorFibocom = iface.VendorFibocom
	VendorSimcom  = iface.VendorSimcom
	VendorMeig    = iface.VendorMeig
	VendorTDTech  = iface.VendorTDTech
	VendorUnisoc  = iface.VendorUnisoc
	VendorUnknown = iface.VendorUnknown

	netLockerOf                = common.NetLockerOf
	qnwCellLockProfileForModel = quectel.CellLockProfileForModel
	qcfgBandCommand            = quectel.QcfgBandCommand
	gosuncnMode                = gosuncn.Mode
	quectelModeLTE             = quectel.ModePrefLTE
	quectelModeNR              = quectel.ModePrefNR
	unisocMode                 = unisoc.Mode
	simcomMode                 = simcom.Mode
	neowayMode                 = neoway.Mode
	sierraMode                 = sierra.Mode
	thalesMode                 = thales.Mode
	huaweiMode                 = huawei.Mode
	meigMode                   = meig.Mode
	foxconnMode                = foxconn.Mode
	fibocomMode                = fibocom.Mode
	telitMode                  = telit.Mode
	openluatMode               = openluat.Mode

	quectelUSB        = quectel.USB
	fibocomUSB        = fibocom.USB
	simcomUSBQualcomm = simcom.USBQualcomm
	simcomUSBLegacy   = simcom.USBLegacy
	gosuncnUSB        = gosuncn.USB
	foxconnUSB        = foxconn.USB
	sierraUSB         = sierra.USB
	thalesUSB         = thales.USB
	telitUSB          = telit.USB
	openluatUSB       = openluat.USB
	huaweiUSB         = huawei.USB
	meigUSB           = meig.USB

	fibocomIMEIGTSN    = fibocom.IMEIGTSN
	fibocomIMEIEGMREXT = fibocom.IMEIEGMREXT
	fibocomIMEILCTSN   = fibocom.IMEILCTSN
	huaweiIMEIPhynum   = huawei.IMEIPhynum
)

// ====================== 厂商 × 芯片平台：取值表变体层 ======================
//
// 为什么要有这一层：
//
//	"同一厂商、不同芯片平台 = 完全不同的取值表"是这批模组里**最容易写错**的地方 ——
//	广和通 USB 模式：高通 32/30/18/24、展锐 34/40/38/36、联发科只有 41；
//	华为 AT^SETMODE：海思 0/1/2、展锐 0..8 且一个码一个含义；
//	美格 AT+SER：高通 2=ECM，其它平台 2=NCM（**同一条指令、同一个数字、含义相反**）；
//	广和通改 IMEI：联发科 EGMREXT、lte 款 LCTSN、其余 GTSN。
//
//	这些差异**写错不会报错**，只会静默把模组切到另一个模式、或写下一个含义相反的值。
//	以前的写法是把 `if PlatformForModel(model) == PlatformXxx` 埋在每一家构造函数里，
//	要看清"这家到底有几种变体"得把七八个函数翻一遍，加平台时漏一处就是这类静默错误。
//
// 这一层把"厂商家族 × 平台档位"显式列成一张表（PlatformVariants）：
//   - 一眼能数出某家有几种变体、每种用哪几张表；
//   - 每行都写 label + evidence（依据哪个 QModem 脚本/手册），便于复核；
//   - 覆盖性由 vendor_platform_invariants_test.go 双向钉住：
//     ① 清单里每个型号"用到的档位"必须有精确行（禁止悄悄退化成别家的表）；
//     ② 表里每行都必须有型号真的用它（禁止留下死行/过期行）。
//
// ⚠️ 判定顺序、每档对应的**探针方言顺序**（LockBase）都逐条对齐改动前的行为，
// 所以这一层是纯重构：所有取值表与候选顺序与以前完全一致（差异只体现在"看得见"）。

// ModemFamily 厂商家族。
//
// 与 ModemVendor（ATI 识别出来的厂商常量）不是一回事：
// 高新兴/有方/Telit/Thales/Foxconn/Sierra/openluat/华为 都归在 VendorUnknown 或
// VendorTDTech，靠型号关键字认；所以家族判定 = 厂商常量 + 机型关键字 + 平台关键字。
type ModemFamily string

const (
	familyGeneric ModemFamily = "generic" // 通用：按平台走，没有厂商专属变体
	familyQuectel ModemFamily = "quectel"
	familyFibocom ModemFamily = "fibocom"
	familySimcom  ModemFamily = "simcom"
	familyMeig    ModemFamily = "meig"
	familyHuawei  ModemFamily = "huawei" // 华为 / 鼎桥（巴龙）
	familyGosuncn ModemFamily = "gosuncn"
)

// FamilyLabels 家族中文名（界面/文档用）
var FamilyLabels = map[ModemFamily]string{
	familyGeneric: "通用",
	familyQuectel: "移远",
	familyFibocom: "广和通",
	familySimcom:  "芯讯通",
	familyMeig:    "美格",
	familyHuawei:  "华为/鼎桥",
	familyGosuncn: "高新兴",
}

// FamilyLabel 家族中文名；未登记的家族原样返回。
func FamilyLabel(f ModemFamily) string {
	if s, ok := FamilyLabels[f]; ok {
		return s
	}
	return string(f)
}

// PlatformFlavor "取值表档位"——比 PlatformForModel 多两档：
//
//	FlavorLegacyLTE：QModem 里 platform=lte 的单 LTE 老模组（NL668/NL678、EC20/EC25…），
//	                 每家都给它单独一份取值表，**不能当高通处理**；
//	FlavorAny：      "*" = 本家族的兜底行（其它平台共用），不是真实平台。
type PlatformFlavor string

const (
	FlavorUnknown   PlatformFlavor = ""         // 认不出平台（QModem 的 ASR 也走这档）
	FlavorQualcomm  PlatformFlavor = "qualcomm" // 高通
	FlavorUnisoc    PlatformFlavor = "unisoc"   // 展锐
	FlavorMediaTek  PlatformFlavor = "mediatek" // 联发科
	FlavorHiSilicon PlatformFlavor = "hisilicon"
	FlavorLegacyLTE PlatformFlavor = "lte" // QModem 的 platform=lte
	FlavorAny       PlatformFlavor = "*"   // 兜底行
)

// FlavorLabel 档位中文名（界面/文档用）
func FlavorLabel(f PlatformFlavor) string {
	switch f {
	case FlavorQualcomm:
		return "高通"
	case FlavorUnisoc:
		return "展锐"
	case FlavorMediaTek:
		return "联发科"
	case FlavorHiSilicon:
		return "海思巴龙"
	case FlavorLegacyLTE:
		return "lte（单 LTE 老款）"
	case FlavorAny:
		return "其它平台"
	}
	return "未识别平台"
}

// FamilyFor 判定厂商家族。
//
// ⚠️ 顺序与改动前 usbDialectFor / imeiDialectFor / preferredLocker 的判定顺序一致：
// 先厂商常量（五家有专属指令集的），再机型关键字（华为/高新兴），最后 generic。
// 新增一家"有专属取值表"的厂商，在这里加一个 case 即可。
func FamilyFor(vendor ModemVendor, model string) ModemFamily {
	switch vendor {
	case VendorQuectel:
		return familyQuectel
	case VendorFibocom:
		return familyFibocom
	case VendorSimcom:
		return familySimcom
	case VendorMeig:
		return familyMeig
	case VendorTDTech:
		return familyHuawei
	}
	switch {
	case isHuaweiModel(model), PlatformForModel(model) == PlatformHiSilicon:
		// 巴龙系（含只认得出平台的），IMEI 走 at^phynum、USB 走 AT^SETMODE
		return familyHuawei
	case isGosuncnModel(model):
		return familyGosuncn
	}
	return familyGeneric
}

// FlavorFor 判定"用哪张取值表"的平台档位。
//
// lte 档优先于芯片平台：QModem 的 support_list 里这批机型 platform 就写 lte，
// 各厂脚本对 lte 是**独立分支**（移远 AT+QCFG="band"、广和通 LCTSN/GTACT 旧写法）。
func FlavorFor(vendor ModemVendor, model string) PlatformFlavor {
	// 移远"旧款 4G"：改动前 preferredLocker 是**不看厂商常量**直接认型号的（EC20 这类
	// 型号串本身就够独特），这里保持一致。
	if isQuectelLegacyLTEModel(model) {
		return FlavorLegacyLTE
	}
	// 广和通 lte 款（NL668/NL678）：只在广和通名下判定（改动前 isFibocomLegacyLTEModel
	// 只在 VendorFibocom 分支里用）。
	if vendor == VendorFibocom && isFibocomLegacyLTEModel(model) {
		return FlavorLegacyLTE
	}
	switch PlatformForModel(model) {
	case PlatformQualcomm:
		return FlavorQualcomm
	case PlatformUnisoc:
		return FlavorUnisoc
	case PlatformMediaTek:
		return FlavorMediaTek
	case PlatformHiSilicon:
		return FlavorHiSilicon
	}
	return FlavorUnknown
}

// ---------- 探针方言顺序（lockCandidates 的 base 列表）----------
//
// 同平台方言更接近，所以按平台先把"最可能命中"的排在前面；名字对应
// LockCandidateBuilders，新增方言时在这里登记名字即可。
var (
	// 未识别平台 / 展锐 / lte 档：改动前走的是同一个 default 分支
	LockBaseDefault   = []string{"qnw", "mtk", "simcom", "balong"}
	LockBaseQualcomm  = []string{"qnw", "simcom", "mtk", "balong"}
	LockBaseMediaTek  = []string{"mtk", "qnw", "simcom", "balong"}
	LockBaseHiSilicon = []string{"balong", "qnw", "mtk", "simcom"}
)

// LockCandidateBuilders 探针方言：名字 → 构造（名字同时是 lockFamilyLabels 的键）
var LockCandidateBuilders = map[string]func() lockCandidate{
	"qnw": func() lockCandidate {
		return lockCandidate{"qnw", &QuectelCommand{}, `AT+QNWPREFCFG="lte_band"`, "+QNWPREFCFG:"}
	},
	"mtk": func() lockCandidate { return lockCandidate{"mtk", &fibocomMTKCommand{}, "AT+GTACT?", "+GTACT:"} },
	"simcom": func() lockCandidate {
		return lockCandidate{"simcom", &simcomLockCommand{}, `AT+CSYSSEL="lte_band"`, "+CSYSSEL:"}
	},
	"balong": func() lockCandidate {
		return lockCandidate{"balong", &MT5700Command{}, "AT^LTEFREQLOCK?", "^LTEFREQLOCK:"}
	},
}

// LockCandidatesByName 把变体表里的名字列表还原成候选列表（未知名字跳过）。
func LockCandidatesByName(names []string) []lockCandidate {
	out := make([]lockCandidate, 0, len(names))
	for _, n := range names {
		if b, ok := LockCandidateBuilders[n]; ok {
			out = append(out, b())
		}
	}
	return out
}

// PlatformVariant 一行变体：某家族在某档平台上用到的全部取值表。
//
// 字段为 nil/空 = 该层没有平台差异（沿用厂商入口的默认值）。
type PlatformVariant struct {
	Family   ModemFamily
	Flavor   PlatformFlavor
	Label    string // 中文说明（"广和通 · 联发科"）
	Evidence string // 依据（哪个 QModem 脚本/手册的哪一段）

	Usb  func() *usbDialect
	Imei func() *imeiDialect
	Mode func() *modeDialect
	// Locker 锁网方言工厂。⚠️ 收的是 **iface.DeviceInfo**，不是 *Modem ——
	// 这样整张变体表才能搬出内核（见 iface/device.go 的说明）。
	Locker   func(iface.DeviceInfo) NetLocker
	LockBase []string // 探针方言顺序（见 LockCandidateBuilders）
	Note     string   // 追加到方言说明里的平台提示（可空）

	// safety：表里留着但清单里暂时没有型号命中（给未上市/新机型准备的兜底行）。
	// 只有"家族兜底行"（flavor == FlavorAny）允许这样，见 coverage 测试。
	Safety bool
}

// ---------- 兜底用的 Locker 构造 ----------

func Mt5700Locker(iface.DeviceInfo) NetLocker { return &MT5700Command{} }

// QnwLocker 通用高通实现；顺手把“锁小区平台档”按型号填上（零值 = 高通，
// 展锐/旧款 4G 的形式不同 —— 见 qnwCellLockProfile）
func QnwLocker(d iface.DeviceInfo) NetLocker { return ApplyCellProfile(&QuectelCommand{}, d) }

// CellProfileSetter 能被补上“锁小区平台档”的 Locker（内嵌 quectel.QnwLockCommands 的那些）
//
// ⚠️ 方法名从 setCellProfile 改成 SetCellProfile：实现已搬到 module/quectel，
// 跨包只能看到导出方法。参数类型孪别名后不变。
type CellProfileSetter interface{ SetCellProfile(qnwCellLockProfile) }

// ApplyCellProfile 给需要“按平台选锁小区写法”的 Locker 补上平台档。
//
// 为什么必须补：锁网候选表是一批**无参构造器**（LockCandidatesByName 里是
// func() lockCandidate），造的时候拿不到型号 ⇒ 只能拿到零值（高通）——
// 展锐/旧款 4G 就会发错指令。
func ApplyCellProfile(l NetLocker, d iface.DeviceInfo) NetLocker {
	if l == nil {
		return l
	}
	if s, ok := l.(CellProfileSetter); ok {
		s.SetCellProfile(qnwCellLockProfileForModel(d.Key()))
	}
	return l
}

// QcfgBandNetLocker 移远 lte 旧款的锁频段方言（AT+QCFG="band"，LTE 位图）
func QcfgBandNetLocker(iface.DeviceInfo) NetLocker { return qcfgBandCommand() }

// VendorLockerOrQNW 厂商自带的高通锁网实现；没有就退回通用 QNW（改动前 qualcomm 分支的行为）
func VendorLockerOrQNW(d iface.DeviceInfo) NetLocker {
	if l := common.NetLockerOf(d.Command); l != nil {
		return ApplyCellProfile(l, d)
	}
	return ApplyCellProfile(&QuectelCommand{}, d)
}

// VendorLockerOnly 厂商自带的锁网实现（可能为 nil）；改动前"未识别平台"分支的行为
func VendorLockerOnly(d iface.DeviceInfo) NetLocker { return common.NetLockerOf(d.Command) }

// PlatformVariants 变体总表。新增平台/新厂商就在这里加一行（覆盖性测试会盯着）。
var PlatformVariants = []PlatformVariant{
	// ---------------- 广和通 Fibocom ----------------
	{
		Family: familyFibocom, Flavor: FlavorMediaTek,
		Label: "广和通 · 联发科",
		Evidence: "Modem-Pro vendor/fibocom.sh（case $platform）：get_mode/set_mode=mediatek 只认 rndis=41；" +
			"set_imei=mediatek → AT+EGMREXT",
		Usb: fibocomUSBMediaTek, Imei: fibocomIMEIEGMREXT, Mode: fibocomMode,
		Locker: FibocomMTKLocker, LockBase: LockBaseMediaTek,
	},
	{
		Family: familyFibocom, Flavor: FlavorUnisoc,
		Label: "广和通 · 展锐",
		Evidence: "Modem-Pro vendor/fibocom.sh：unisoc ecm=34/mbim=40/rndis=38/ncm=36；" +
			"锁网走 AT+GTACT（对 fm650-cn 真机测试用例即此分支）",
		Usb: fibocomUSBUnisoc, Imei: fibocomIMEIGTSN, Mode: fibocomMode,
		Locker: FibocomMTKLocker, LockBase: LockBaseDefault,
	},
	{
		Family: familyFibocom, Flavor: FlavorLegacyLTE,
		Label: "广和通 · lte（NL668/NL678）",
		Evidence: "Modem-Pro vendor/fibocom.sh：set_imei 的 lte 分支 → AT+LCTSN；" +
			"USB 模式沿用高通语义（脚本里 lte 与 qualcomm 同一张码表）；锁网沿用高通档（厂商自带实现）",
		Usb: fibocomUSBQualcomm, Imei: fibocomIMEILCTSN, Mode: fibocomMode,
		Locker: VendorLockerOrQNW, LockBase: LockBaseDefault,
	},
	{
		Family: familyFibocom, Flavor: FlavorQualcomm,
		Label: "广和通 · 高通",
		Evidence: "Modem-Pro vendor/fibocom.sh：qualcomm qmi=32/ecm=18/mbim=30/rndis=24；" +
			"set_imei 的 *）分支 → AT+GTSN；锁网走厂商自带的高通实现（FibocomCommand 的 QNW 方言）",
		Usb: fibocomUSBQualcomm, Imei: fibocomIMEIGTSN, Mode: fibocomMode,
		Locker: VendorLockerOrQNW, LockBase: LockBaseQualcomm,
	},
	{
		Family: familyFibocom, Flavor: FlavorAny, Safety: true,
		Label:    "广和通 · 其它平台（兜底 = 高通语义）",
		Evidence: "改动前 fibocomUSB 的 default 分支 = 高通码表；set_imei 的 *）分支 = AT+GTSN",
		Usb:      fibocomUSBQualcomm, Imei: fibocomIMEIGTSN, Mode: fibocomMode,
		LockBase: LockBaseDefault,
	},

	// ---------------- 华为 / 鼎桥（巴龙） ----------------
	{
		Family: familyHuawei, Flavor: FlavorHiSilicon,
		Label: "华为 · 海思巴龙",
		Evidence: "Modem-Pro vendor/huawei.sh：set_mode 的 default 分支（只特判 unisoc）：ecm=0、ncm=4；" +
			"get_mode 同分支：0|2=ecm、1|3|4|5=ncm、6=rndis、7=mbim、8=ppp。" +
			"IMEI 用 at^phynum=IMEI,<imei>（cmds/huawei.sh 原文）；锁网 ^LTEFREQLOCK/^NRFREQLOCK",
		Usb: huaweiUSBHisilicon, Imei: huaweiIMEIPhynum, Mode: huaweiMode,
		Locker: Mt5700Locker, LockBase: LockBaseHiSilicon,
	},
	{
		Family: familyHuawei, Flavor: FlavorUnisoc,
		Label:    "华为 · 展锐平台（MH5000-82 那批）",
		Evidence: "Modem-Pro vendor/huawei.sh 的 unisoc 分支：set_mode 0=rndis、1=ecm、2=ncm",
		Usb:      huaweiUSBUnisoc, Imei: huaweiIMEIPhynum, Mode: huaweiMode,
		Locker: QnwLocker, LockBase: LockBaseDefault,
	},
	{
		Family: familyHuawei, Flavor: FlavorAny, Safety: true,
		Label:    "华为 · 其它平台（兜底 = 巴龙）",
		Evidence: "改动前 huaweiUSB 的 else 分支 = 海思巴龙码表",
		Usb:      huaweiUSBHisilicon, Imei: huaweiIMEIPhynum, Mode: huaweiMode,
		LockBase: LockBaseDefault,
	},

	// ---------------- 美格 Meig ----------------
	{
		Family: familyMeig, Flavor: FlavorQualcomm,
		Label:    "美格 · 高通",
		Evidence: "Modem-Pro vendor/meig.sh：AT+SER=<n>,1 的 qualcomm 映射 2=ECM、3=RNDIS",
		Usb:      meigUSBQualcomm, Mode: meigMode,
		Locker: VendorLockerOrQNW, LockBase: LockBaseQualcomm,
	},
	{
		Family: familyMeig, Flavor: FlavorUnisoc,
		Label:    "美格 · 展锐",
		Evidence: "Modem-Pro vendor/meig.sh：非高通平台 2=NCM、3=RNDIS（**同数字不同含义**）",
		Usb:      meigUSBOther, Mode: meigMode,
		Locker: QnwLocker, LockBase: LockBaseDefault,
	},
	{
		Family: familyMeig, Flavor: FlavorAny, Safety: true,
		Label:    "美格 · 其它平台（兜底）",
		Evidence: "改动前 meigUSB 的 else 分支 = 非高通码表",
		Usb:      meigUSBOther, Mode: meigMode, LockBase: LockBaseDefault,
	},

	// ---------------- 芯讯通 Simcom ----------------
	{
		Family: familySimcom, Flavor: FlavorQualcomm,
		Label:    "芯讯通 · 高通（AT+CUSBCFG）",
		Evidence: "Modem-Pro vendor/simcom.sh：高通平台读 AT+CUSBCFG? 的 USBID（SIM8200 那批）",
		Usb:      simcomUSBQualcomm, Mode: simcomMode,
		Locker: VendorLockerOrQNW, LockBase: LockBaseQualcomm,
	},
	{
		Family: familySimcom, Flavor: FlavorAny,
		Label: "芯讯通 · 其它平台（AT$MYCONFIG 旧款）",
		Evidence: "Modem-Pro vendor/simcom.sh：lte 平台用 AT$MYCONFIG=\"USBNETMODE\"（0=rndis/1=ecm/2=auto）；" +
			"A8200（ASR 平台）也落到这档",
		Usb: simcomUSBLegacy, Mode: simcomMode, LockBase: LockBaseDefault,
	},

	// ---------------- 高新兴 Gosuncn ----------------
	{
		Family: familyGosuncn, Flavor: FlavorQualcomm,
		Label:    "高新兴 · 高通",
		Evidence: "Modem-Pro cmds/gosuncn.sh：高通平台的组网模式字符含义与 lte 平台不同（lte 的 e 读作 MBIM）",
		Mode:     GosuncnModeQualcomm, LockBase: LockBaseQualcomm,
		Note: " 本机平台=高通。",
	},
	{
		Family: familyGosuncn, Flavor: FlavorAny,
		Label:    "高新兴 · 其它平台（lte 等）",
		Evidence: "Modem-Pro cmds/gosuncn.sh：lte 平台字符含义不同，取值以设备回读为准（见 USB 方言说明）",
		Mode:     GosuncnModeOther, LockBase: LockBaseDefault,
	},

	// ---------------- 移远 Quectel ----------------
	{
		Family: familyQuectel, Flavor: FlavorLegacyLTE,
		Label: "移远 · lte 旧款 4G（EM05/EC20/EC25/EG25…）",
		Evidence: "Modem-Pro vendor/quectel.sh set_lockband：case $platform in \"lte\") AT+QCFG=\"band\",0,<hex>,0" +
			"（*）分支才是 QNWPREFCFG）；组网模式用 lte 档码表",
		Mode: quectelModeLTE, Locker: QcfgBandNetLocker, LockBase: LockBaseDefault,
	},

	// ---------------- 通用（无厂商专属变体，按平台） ----------------
	{
		Family: familyGeneric, Flavor: FlavorQualcomm,
		Label:    "通用 · 高通",
		Evidence: "改动前 preferredLocker 的 qualcomm 分支：厂商自带实现优先，其次通用 QNWPREFCFG / QNWLOCK",
		Locker:   VendorLockerOrQNW, LockBase: LockBaseQualcomm,
	},
	{
		Family: familyGeneric, Flavor: FlavorUnisoc,
		Label:    "通用 · 展锐",
		Evidence: "Modem-Pro 真机用例 testcases/quectel/unisoc/rm500u-cnv：展锐平台照样认 AT+QNWPREFCFG（见 fixture_real_test.go）",
		Locker:   QnwLocker, LockBase: LockBaseDefault,
	},
	{
		Family: familyGeneric, Flavor: FlavorMediaTek, Safety: true,
		Label:    "通用 · 联发科",
		Evidence: "改动前 preferredLocker 的 MediaTek 分支 = AT+GTACT / AT+GTCELLLOCK（广和通 MTK 方言，其它 MTK 模组也认）",
		Locker:   FibocomMTKLocker, LockBase: LockBaseMediaTek,
	},
	{
		Family: familyGeneric, Flavor: FlavorHiSilicon,
		Label:    "通用 · 海思巴龙",
		Evidence: "改动前 preferredLocker 的 HiSilicon 分支 = ^LTEFREQLOCK / ^NRFREQLOCK（移远 RG801 这类非华为品牌的巴龙模组也走它）",
		Locker:   Mt5700Locker, LockBase: LockBaseHiSilicon,
	},
	{
		Family: familyGeneric, Flavor: FlavorLegacyLTE, Safety: true,
		Label:    "通用 · lte 旧款",
		Evidence: "改动前 preferredLocker 开头对 isQuectelLegacyLTEModel 的判定不看厂商常量",
		Locker:   QcfgBandNetLocker, LockBase: LockBaseDefault,
	},
	{
		Family: familyGeneric, Flavor: FlavorAny, Safety: true,
		Label:    "通用 · 未识别平台",
		Evidence: "改动前 preferredLocker 的 default 分支：**不猜**，只用厂商自带实现（没有就留空给运行期探测）",
		Locker:   VendorLockerOnly, LockBase: LockBaseDefault,
	},
}

// PlatformVariantLookup 精确查一行（flavor=="" 视为"没有精确行"）。
func PlatformVariantLookup(fam ModemFamily, fl PlatformFlavor) *PlatformVariant {
	if fl == FlavorUnknown {
		return nil
	}
	for i := range PlatformVariants {
		if PlatformVariants[i].Family == fam && PlatformVariants[i].Flavor == fl {
			return &PlatformVariants[i]
		}
	}
	return nil
}

// PlatformVariantFor 查本机该用哪一行变体。
//
// 级联顺序（与改动前的分支嵌套一一对应）：
//
//	① 本家族精确档位 → ② 本家族兜底行（FlavorAny）→ ③ 通用家族同档位 → ④ 通用兜底行
//
// 例：广和通/高通 在 ① 命中"广和通 · 高通"；移远/高通 在 ①② 都没有，
// 落到 ③ 的"通用 · 高通"（= 用厂商自带的 QNW 实现）—— 正是改动前 qualcomm 分支的行为。
func PlatformVariantFor(vendor ModemVendor, model string) *PlatformVariant {
	fam, fl := FamilyFor(vendor, model), FlavorFor(vendor, model)
	if v := PlatformVariantLookup(fam, fl); v != nil {
		return v
	}
	if fam != familyGeneric {
		if v := PlatformVariantLookup(fam, FlavorAny); v != nil {
			return v
		}
		if v := PlatformVariantLookup(familyGeneric, fl); v != nil {
			return v
		}
	}
	return PlatformVariantLookup(familyGeneric, FlavorAny)
}

// PlatformVariantName 本机命中的变体名（界面/文档/排障用："广和通 · 展锐"）。
//
// 排查"为什么这台写出去了错的值"时，第一件要看的就是它 —— 与本机 ATI 型号对不上的话，
// 说明平台识别（platformModelTokens）需要补。
func PlatformVariantName(vendor ModemVendor, model string) string {
	if v := PlatformVariantFor(vendor, model); v != nil {
		return v.Label
	}
	return FamilyLabel(FamilyFor(vendor, model)) + " · " + FlavorLabel(FlavorFor(vendor, model))
}

// PlatformVariantNote 本机变体的一句话说明（证据来源），界面里跟变体名一起显示。
func PlatformVariantNote(vendor ModemVendor, model string) string {
	if v := PlatformVariantFor(vendor, model); v != nil {
		return v.Evidence
	}
	return ""
}

// PlatformVariantRows 表格只读副本（文档生成/测试用）
func PlatformVariantRows() []PlatformVariant {
	out := make([]PlatformVariant, len(PlatformVariants))
	copy(out, PlatformVariants)
	return out
}

// PlatformVariantMarkdown 变体总表 → Markdown（M.1 章节用；
// 由 TestPlatformVariantDocSync 保证与代码一致，**不要手改文档里的这张表**）。
func PlatformVariantMarkdown() string {
	var b strings.Builder
	b.WriteString("| 家族 | 平台档位 | 变体 | 平台差异点 | 依据 |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, v := range PlatformVariantRows() {
		var diffs []string
		if v.Usb != nil {
			diffs = append(diffs, "USB 网卡模式")
		}
		if v.Imei != nil {
			diffs = append(diffs, "IMEI 改写")
		}
		if v.Mode != nil {
			diffs = append(diffs, "组网模式")
		}
		if v.Locker != nil {
			diffs = append(diffs, "锁网方言")
		}
		if len(v.LockBase) > 0 {
			diffs = append(diffs, "探针顺序")
		}
		Flavor := FlavorLabel(v.Flavor)
		if v.Safety {
			Flavor += "（兜底）"
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n",
			FamilyLabel(v.Family), Flavor, MdCell(v.Label), strings.Join(diffs, "、"), MdCell(v.Evidence)))
	}
	return b.String()
}

// MdCell 表格单元格转义：竖线与换行都会破坏 Markdown 表结构。
func MdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\r", "")
	return strings.ReplaceAll(s, "\n", " ")
}

type LockCandidate struct {
	Name   string
	Locker NetLocker
	Probe  string // 只读探测指令（安全，不改任何状态）
	Mark   string // 应答里出现该标记才算"固件接受这一套"
}

// ---- 补齐的别名（autoalias 漏掉的；类型用 type，函数/变量用 var）----
var (
	fibocomUSBMediaTek      = fibocom.USBMediaTek
	fibocomUSBQualcomm      = fibocom.USBQualcomm
	fibocomUSBUnisoc        = fibocom.USBUnisoc
	huaweiUSBHisilicon      = huawei.USBHisilicon
	huaweiUSBUnisoc         = huawei.USBUnisoc
	isFibocomLegacyLTEModel = platform.IsFibocomLegacyLTEModel
	isFoxconnModel          = platform.IsFoxconnModel
	isGosuncnModel          = platform.IsGosuncnModel
	isHuaweiModel           = platform.IsHuaweiModel
	isNeowayModel           = platform.IsNeowayModel
	isOpenluatModel         = platform.IsOpenluatModel
	isQuectelLegacyLTEModel = platform.IsQuectelLegacyLTEModel
	isSierraModel           = platform.IsSierraModel
	isSimcomLockModel       = platform.IsSimcomLockModel
	isTelitModel            = platform.IsTelitModel
	isThalesModel           = platform.IsThalesModel
	meigUSBOther            = meig.USBOther
	meigUSBQualcomm         = meig.USBQualcomm
)

// ---- resolve-undefined 生成的别名 ----
const ()

var (
	PlatformForModel = platform.PlatformForModel
)

// ---- resolve-undefined 生成的别名 ----
const (
	PlatformUnknown   = platform.PlatformUnknown
	PlatformQualcomm  = platform.PlatformQualcomm
	PlatformUnisoc    = platform.PlatformUnisoc
	PlatformMediaTek  = platform.PlatformMediaTek
	PlatformHiSilicon = platform.PlatformHiSilicon
	PlatformASR       = platform.PlatformASR
)
