// Package platform 是「模组芯片平台识别」——型号串 → 芯片平台的静态映射表。
//
// 为什么按**平台**而不是按厂商分：同一芯片平台的不同厂商/型号，AT 方言基本一致
// （服务小区 / 信号 / 锁网 / 温度…），所以"按平台"选指令比"按厂商"更通用 ——
// 新增机型只要认得出平台，就能直接复用该平台已有的指令集与解析。
//
// 参考 QModem 的支持列表（github.com/FUjr/QModem › docs/support_list.md）。
//
// ⚠️ 只登记**有把握**的平台归属；认不出的退回 PlatformUnknown
// （走自适应 + 候选探测），不瞎猜。
//
// 依赖方向：module/platform ──> 标准库。**不依赖 iface、也不依赖 kernel。**
package platform

import "strings"

type ModemPlatform string

const (
	PlatformUnknown   ModemPlatform = ""
	PlatformQualcomm  ModemPlatform = "qualcomm"  // 高通 SDX/MDM（最多的 5G 模组）
	PlatformUnisoc    ModemPlatform = "unisoc"    // 紫光展锐
	PlatformMediaTek  ModemPlatform = "mediatek"  // 联发科
	PlatformHiSilicon ModemPlatform = "hisilicon" // 华为巴龙（MT5700/MT5710/MH5000…）
	PlatformASR       ModemPlatform = "asr"       // 翱捷科技
)

// PlatformLabel 平台中文名（排查/界面用）
func PlatformLabel(p ModemPlatform) string {
	switch p {
	case PlatformQualcomm:
		return "高通 Qualcomm"
	case PlatformUnisoc:
		return "展锐 UNISOC"
	case PlatformMediaTek:
		return "联发科 MediaTek"
	case PlatformHiSilicon:
		return "海思 HiSilicon（巴龙）"
	case PlatformASR:
		return "翱捷 ASR"
	}
	return "未识别"
}

// platformModelTokens 型号关键字 → 平台（与 QModem 支持列表对齐）。
// 匹配方式是"型号串（大写）包含关键字"，所以关键字要够独特；
// 更具体/更长的写在前面（先命中先返回）。
//
// wordOnly：给**很短、容易撞车**的关键字用（如华为的 S10）——
// 要求命中时前后不是字母/数字，否则 "S100"、"MS10" 这类串会被误识别成 S10。
var platformModelTokens = []struct {
	key      string
	platform ModemPlatform
	wordOnly bool
}{
	// ---------- 联发科 MediaTek ----------
	{key: "FM350R", platform: PlatformMediaTek},
	{key: "RW350R", platform: PlatformMediaTek},
	{key: "FM350", platform: PlatformMediaTek},

	// ---------- 海思 HiSilicon（巴龙） ----------
	{key: "MT5700", platform: PlatformHiSilicon},
	{key: "MT5710", platform: PlatformHiSilicon},
	{key: "MH5000-31", platform: PlatformHiSilicon},
	{key: "MH5000-82", platform: PlatformUnisoc}, // 这一支是展锐平台（QModem 列表）
	{key: "RG801", platform: PlatformHiSilicon},
	{key: "S10", platform: PlatformHiSilicon, wordOnly: true}, // 华为 S10（支持列表里写的是 hisilicon）

	// ---------- 翱捷 ASR ----------
	{key: "A8200", platform: PlatformASR},

	// ---------- 展锐 UNISOC ----------
	{key: "FM650", platform: PlatformUnisoc},
	{key: "L610", platform: PlatformUnisoc},
	{key: "RM500U", platform: PlatformUnisoc},
	{key: "RG200U", platform: PlatformUnisoc},
	{key: "SRM821", platform: PlatformUnisoc},
	{key: "N510M", platform: PlatformUnisoc},
	{key: "AIR724", platform: PlatformUnisoc},

	// ---------- 高通 Qualcomm ----------
	// 广和通
	{key: "FM150", platform: PlatformQualcomm},
	{key: "FM160", platform: PlatformQualcomm},
	{key: "FM170", platform: PlatformQualcomm},
	{key: "FM190W", platform: PlatformQualcomm},
	{key: "NL668", platform: PlatformQualcomm},
	{key: "NL678", platform: PlatformQualcomm},
	// 移远 5G
	{key: "RM500Q", platform: PlatformQualcomm},
	{key: "RM500S", platform: PlatformQualcomm},
	{key: "RM502Q", platform: PlatformQualcomm},
	{key: "RM505Q", platform: PlatformQualcomm},
	{key: "RM510Q", platform: PlatformQualcomm},
	{key: "RM520N", platform: PlatformQualcomm},
	{key: "RM520F", platform: PlatformQualcomm},
	{key: "RM521F", platform: PlatformQualcomm},
	{key: "RM530N", platform: PlatformQualcomm},
	{key: "RM551E", platform: PlatformQualcomm},
	{key: "RG500Q", platform: PlatformQualcomm},
	{key: "RG501Q", platform: PlatformQualcomm},
	{key: "RG520N", platform: PlatformQualcomm},
	{key: "RG520F", platform: PlatformQualcomm},
	{key: "RG530F", platform: PlatformQualcomm},
	{key: "RG650V", platform: PlatformQualcomm},
	{key: "AG598", platform: PlatformQualcomm},
	{key: "MH8001", platform: PlatformQualcomm},
	// 移远 4G（共同方言）
	{key: "EM05", platform: PlatformQualcomm},
	{key: "EM12", platform: PlatformQualcomm},
	{key: "EM120", platform: PlatformQualcomm},
	{key: "EM160", platform: PlatformQualcomm},
	{key: "EP06", platform: PlatformQualcomm},
	{key: "EC20", platform: PlatformQualcomm},
	{key: "EC21", platform: PlatformQualcomm},
	{key: "EC25", platform: PlatformQualcomm},
	{key: "EG25", platform: PlatformQualcomm},
	{key: "EC200", platform: PlatformQualcomm},
	// 美格
	{key: "SRM815", platform: PlatformQualcomm},
	{key: "SRM825", platform: PlatformQualcomm},
	// 芯讯通（高通平台，但锁网用自家 CSYSSEL，见 module/simcom 的锁网方言）
	{key: "SIM8200", platform: PlatformQualcomm},
	{key: "SIM8202", platform: PlatformQualcomm},
	{key: "SIM8380", platform: PlatformQualcomm},
	{key: "D8200", platform: PlatformQualcomm},
	// Sierra / Telit / Foxconn / Thales / 高新兴
	{key: "EM9190", platform: PlatformQualcomm},
	{key: "EM9291", platform: PlatformQualcomm},
	{key: "MC7354", platform: PlatformQualcomm},
	{key: "MC7355", platform: PlatformQualcomm},
	{key: "FN990", platform: PlatformQualcomm},
	{key: "T77W", platform: PlatformQualcomm},
	{key: "T99W", platform: PlatformQualcomm},
	{key: "MV32", platform: PlatformQualcomm},
	{key: "GM800", platform: PlatformQualcomm},
}

// PlatformForModel 从型号串（ATI 原文或短名）判断芯片平台；认不出返回 PlatformUnknown。
// 大小写无关；传入 "" 直接返回 Unknown。
func PlatformForModel(model string) ModemPlatform {
	u := strings.ToUpper(model)
	if strings.TrimSpace(u) == "" {
		return PlatformUnknown
	}
	for _, m := range platformModelTokens {
		if m.wordOnly {
			if modelTokenAtWord(u, m.key) {
				return m.platform
			}
			continue
		}
		if strings.Contains(u, m.key) {
			return m.platform
		}
	}
	return PlatformUnknown
}

// ModelTokenAtWord 导出入口：kernel 的模组目录（modem_catalog.go）也用它做短关键字匹配。
func ModelTokenAtWord(u, key string) bool { return modelTokenAtWord(u, key) }

// modelTokenAtWord 关键字 key 是否在 u（已大写）里**按词**出现：前后不能是字母或数字。
// 给 S10 这类短关键字用，避免 "S100"/"MS10" 被误判。
func modelTokenAtWord(u, key string) bool {
	for from := 0; ; {
		i := strings.Index(u[from:], key)
		if i < 0 {
			return false
		}
		at := from + i
		end := at + len(key)
		before := at == 0 || !isModelWordByte(u[at-1])
		after := end == len(u) || !isModelWordByte(u[end])
		if before && after {
			return true
		}
		from = at + 1
	}
}

func isModelWordByte(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// ---------------------------------------------------------------------------
// 下面这些是「按型号归类到某套自有锁网方言」的判定。
//
// 它们与平台无关（同一平台也可能有不同方言），所以单独一组；
// 但同属"型号知识"，跟平台表住在一起。
// ---------------------------------------------------------------------------

// simcomLockModels 芯讯通用**自家**锁网指令（AT+CSYSSEL / AT+CCELLCFG / AT+C5GCELLCFG）
// 的型号。SIM8200 虽是高通平台，但官方手册给的是这一套，优先用它。
var simcomLockModels = []string{"SIM8200", "SIM8202", "SIM8380", "D8200"}

// IsSimcomLockModel 是否芯讯通自家锁网型号
func IsSimcomLockModel(model string) bool { return modelHasAny(model, simcomLockModels) }

// gosuncnModels 高新兴（Gosuncn）锁网型号（自家 AT+ZBAND / AT+ZLOCKCELL 方言）
var gosuncnModels = []string{"GM800", "ME3630"}

// IsGosuncnModel 是否高新兴锁网型号
func IsGosuncnModel(model string) bool { return modelHasAny(model, gosuncnModels) }

// 其它"自有锁频段方言"的型号关键字（均**无锁小区**指令）
//
// ⚠️ 这里**故意没有 nk**（QModem 控制台把它映射成 "Tom's Love"）：已逐项取证确认它
// 没有可实现的锁网指令 —— ① `docs/support_list.md` 里没有任何 tom/nk 型号；
// ② `vendor/` 目录下没有 `nk.sh`（GitHub API 列目录确认，只有 12 个厂商脚本）；
// ③ `docs/tom-modem-manual.md` 只是 `tom_modem` 命令行工具的说明，不含锁网 AT 指令。
var (
	telitModels   = []string{"FN990"}
	thalesModels  = []string{"MV32"}
	foxconnModels = []string{"T77W", "T99W"}
	neowayModels  = []string{"N510M"}
	// openluat：Air724UG 锁频段走 `AT*BAND`（应答是**复合字段**：mode/gsm/umts/lte_high/lte_low/roaming/srv_domain）。
	// 本仓库的实现在**回读**后只替换 lte_high/lte_low 两个字段，其余原样写回，
	// 不会动到漫游/服务域；它也没有锁小区（`AT*CELL` 不支持）。
	openluatModels = []string{"AIR724"}
)

func modelHasAny(model string, keys []string) bool {
	u := strings.ToUpper(model)
	for _, k := range keys {
		if strings.Contains(u, k) {
			return true
		}
	}
	return false
}

// IsTelitModel 是否 Telit（FN990）
func IsTelitModel(model string) bool { return modelHasAny(model, telitModels) }

// IsThalesModel 是否 Thales（MV32-W）
func IsThalesModel(model string) bool { return modelHasAny(model, thalesModels) }

// IsFoxconnModel 是否 Foxconn（T77W / T99W）
func IsFoxconnModel(model string) bool { return modelHasAny(model, foxconnModels) }

// IsNeowayModel 是否 有方（N510M）
func IsNeowayModel(model string) bool { return modelHasAny(model, neowayModels) }

// IsOpenluatModel 是否 合宙（Air724UG）
func IsOpenluatModel(model string) bool { return modelHasAny(model, openluatModels) }

// sierraModels Sierra Wireless（QModem docs/support_list.md：em9190 / em9291 / mc7354 / mc7355）
var sierraModels = []string{"EM9190", "EM9291", "MC7354", "MC7355", "EM9191"}

// IsSierraModel 是否 Sierra Wireless
func IsSierraModel(model string) bool { return modelHasAny(model, sierraModels) }

// huaweiModels 华为/巴龙系机型（IMEI 走 at^phynum、USB 模式走 AT^SETMODE）。
// ⚠️ 用词边界匹配的短名（S10）与常见前缀；其余巴龙机型靠 PlatformHiSilicon 兜底。
var huaweiModels = []string{"MT5700", "MT5710", "MH5000", "MU500", "S10", "E3372", "E8372"}

// IsHuaweiModel 是否 华为/巴龙系
func IsHuaweiModel(model string) bool {
	if !modelHasAny(model, huaweiModels) {
		return false
	}
	// S10 太短，避免撞上 S100 / MS10 这类
	if strings.Contains(strings.ToUpper(model), "S10") && !modelTokenAtWord(strings.ToUpper(model), "S10") {
		for _, k := range huaweiModels {
			if k != "S10" && strings.Contains(strings.ToUpper(model), k) {
				return true
			}
		}
		return false
	}
	return true
}

// fibocomLegacyLTEModels 广和通的 "lte 平台"机型（support_list：nl668 / nl678）——
// IMEI 用 AT+LCTSN、锁频段走 AT+GTACT 的旧写法
var fibocomLegacyLTEModels = []string{"NL668", "NL678"}

// IsFibocomLegacyLTEModel 是否广和通 lte 平台机型
func IsFibocomLegacyLTEModel(model string) bool { return modelHasAny(model, fibocomLegacyLTEModels) }

// quectelLegacyLTEModels 移远"旧款 4G"（QModem `docs/support_list.md` 里平台标成 **lte** 的那批）：
// em05 / em05-cn / ep06 / em12 / ec20f / ec25 / eg25 / ec21 / ec200a。
//
// 依据 `vendor/quectel.sh` 的平台分派：
//
//	set_lockband()：case $platform in "lte") set_lockband_lte  →  AT+QCFG="band",0,<hex 位图>,0
//	                             *)     set_lockband_nr   →  AT+QNWPREFCFG="lte_band"/"nr5g_band"
//
// ⚠️ **`lte12`（em120k / em160r-gl）不算**：它们落到 `*)` 分支，走的还是 QNWPREFCFG ——
// 所以下面逐字列出型号而不用 `EM12` 这种前缀（否则会把 em120k 误判进来）。
var quectelLegacyLTEModels = []string{"EM05", "EP06", "EC20", "EC21", "EC25", "EG25", "EC200"}

// IsQuectelLegacyLTEModel 是否是"平台=lte"的旧款移远 4G 机型（锁频段走 AT+QCFG="band"）
func IsQuectelLegacyLTEModel(model string) bool {
	u := strings.ToUpper(model)
	// EM12 是 lte、EM120K 是 lte12：用词边界区分
	if modelTokenAtWord(u, "EM12") {
		return true
	}
	for _, k := range quectelLegacyLTEModels {
		if strings.Contains(u, k) {
			return true
		}
	}
	return false
}
