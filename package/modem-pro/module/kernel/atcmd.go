package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"fmt"
	"strings"

	"modemd/module/common"
	"modemd/module/registry"
	"modemd/module/telit"
)

// ---- from: atcmd/at.go ----

// Package atcmd 是「指令层」：厂商指令集、厂商能力接口、机型特例表，
// 以及各厂商在通用指令之外自己实现的能力（如鼎桥 MT5700 的载波聚合、锁网）。
//
// 这里放**指令与解析**，不放模组状态：
//
//	· 指令字符串与应答解析（纯函数、带实测样例的单测）；
//	· 厂商识别（ATI → ModemVendor）与机型特例表（quirks）；
//	· 可选能力的接口（CommandCandidates / CAVendorOps / LockVendorOps …）
//	  与厂商侧实现。
//
// 需要发指令时，通过下面这两个窄接口向引擎取，而不是直接依赖 kernel 的 *Modem
// 这是这段代码的历史边界：Go 要求"方法与类型同包"，
// 只要厂商指令类型的全部方法都在同一个文件里，类型就能一直住在这里。
// ATDoer / LockATDoer 已搬到 modemd/iface（见 iface/doer.go）：
//
//   - 厂商实现要引用这两个接口，但不能反向依赖内核 ⇒ 接口必须住在中间层；
//   - kernel 里保留同名别名（iface_alias.go），调用点原样传 *Modem 即可。

// ---- MT5700 的 CA 实现已搬到 modemd/module/tdtech（见 module/tdtech/command.go）----

// ---- from: atcmd/caps.go ----

//
// 这些接口都是**可选**的：厂商指令类型实现了就启用，没实现就返回明确的"不支持"，
// 绝不会把未经验证的指令发给真实模组。
//
// 需要发指令的接口形参是 ATDoer / LockATDoer（而不是 kernel 的 *Modem），
// 这样厂商实现能跟指令类型住在同一个包里（Go 要求方法与类型同包）；
// 引擎侧调用点仍然原样传 *Modem —— 它实现了这两个接口。
// CAVendorOps 已搬到 modemd/iface（见 iface/command.go）：
// 厂商的 CA 实现要返回 `[]iface.CCInfo`，所以接口和它引用的类型都得住在中间层。
// kernel 保留同名别名，见 iface_alias.go。

// telitCAOps Telit 的 CA 来源：**与服务小区同一条指令** `AT#CAINFOEXT?`
// （QModem `vendor/telit.sh` 的 ca_info() / cell_info()：`#CAINFOEXT: <ca_count>,<rat>` 之后
//
//	PCC- 行是主载波、SCCn- 行是辅载波，解析同在 ParseCAInfoExt）。
//
// 为什么要单独挂：Telit 的 cmdImpl 是自适应的（本项目没有 TelitCommand），
// 走通用路径会发移远的 `AT+QCAINFO`（Telit 不认）⇒ CA 卡片永远是空的。
// 所以这里按**机型**挂（lock.go 的 caOpsForModel），其它厂商一点不受影响。
type telitCAOps struct{}

// telitCAInfoCmd Telit 的分量载波查询指令（与机型特例里指定的服务小区指令是同一条）
const telitCAInfoCmd = "AT#CAINFOEXT?"

func (telitCAOps) QueryCA(at ATDoer) ([]CCInfo, error) {
	resp, err := at.SendAT(telitCAInfoCmd)
	if err != nil {
		return nil, err
	}
	serving, ca, ok := telit.CAInfoExt(resp)
	if !ok {
		return nil, nil // 认不出（例如回了 ERROR）= 当前没有 CA，不猜
	}
	// 与移远 AT+QCAINFO 的形态对齐：列表里**含主载波**（Role=PCC），
	// 由 CCsAggregated() 看有没有 SCC 来判断"聚合真的生效了"。
	out := []CCInfo{{
		Role: "PCC", Tech: serving.Tech, Band: serving.Band, BandLabel: common.BandLabel(serving.Tech, serving.Band),
		EARFCN: serving.EARFCN, PCID: serving.PCID,
		RSRP: serving.RSRP, RSRQ: serving.RSRQ, RSSI: serving.RSSI, SINR: serving.SINR,
		Raw: serving.Raw,
	}}
	return append(out, ca...), nil
}

func (telitCAOps) CACmds() []string { return []string{telitCAInfoCmd} }

func (telitCAOps) CAEmptyNote() string {
	return "Telit 的 CA 与服务小区来自同一条 " + telitCAInfoCmd +
		"：应答头 `#CAINFOEXT: <ca_count>,<rat>` 的 ca_count = 1 表示只有主载波（没有载波聚合）。"
}

// 编译期断言：Telit 的 CA 扩展（按机型挂）
var _ CAVendorOps = telitCAOps{}

// NetLocker / LockVendorOps 已搬到 modemd/iface（见 iface/netlock.go）：
// 厂商的锁频方言要实现它们，就不能让它们住在被依赖的引擎包里。
// kernel 保留同名别名，见 iface_alias.go。

// ---- MT5700 的锁网实现（含 ^SYSCFGEX / 三段式应答解析）已搬到 modemd/module/tdtech ----

// ---- from: atcmd/lock_quectel.go ----

// qnwBandKey / qnwLockKind 已搬到 modemd/module/quectel（`BandKey` / `LockKind`）；
// FormatBandList 已搬到 modemd/module/common。
// kernel 在 iface_alias.go 里保留 QnwBandKey / QnwLockKind / FormatBandList，调用点不变。

// ---- from: atcmd/lockparams.go ----

//
// 频段列表写法（"1:3:5:8"）、技术域（LTE / NR，兼容 4g/5g 别名）、频点号合法性
// 这三件事都是"锁网指令的入参"问题，与模组无关，所以住在指令层；
// 引擎（lock.go）与定时器（schedule.go）都直接用这里的实现。
// ParseBandList 解析频段列表（冒号或逗号分隔，如 "1:3:5:8"）——
// 实现已搬到 modemd/module/common（ParseBandList）；kernel 保留同名别名，见 atutil_alias.go。

// ParseLockTech 解析 API / 定时任务传入的技术域参数（4g/5g 别名一并支持）
func ParseLockTech(s string) (CellTech, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "LTE", "4G":
		return CellTechLTE, nil
	case "NR", "5G", "NR5G", "NR5G-SA":
		return CellTechNR, nil
	case "NRNSA", "NR-NSA", "NSA", "5G-NSA", "NR5G-NSA":
		// NR 的 NSA 列表：只有分两套列表的方言才认（见 NSAIndependent）
		return CellTechNRNSA, nil
	}
	return "", fmt.Errorf("tech must be LTE or NR (or NRNSA), got %q", s)
}

// EarfcnValid 校验频点号是否落在该技术域的合法范围。
//   - LTE EARFCN: 0 ~ 65535（TS 36.101）
//   - NR  ARFCN : 0 ~ 3279165（TS 38.104，含 FR1/FR2）
func EarfcnValid(tech CellTech, v int) bool {
	if v <= 0 {
		return false
	}
	if tech == CellTechNR || tech == CellTechNRNSA {
		return v <= 3279165
	}
	return v <= 65535
}

// ---- from: atcmd/locktypes.go ----

// CellLock / CellLockRequest（含 Band()）/ NormalizeSCS / BandFromEARFCN
// 已搬到 modemd/iface —— 厂商的锁频方言与引擎都要用，不能让厂商反向依赖引擎。
// kernel 保留同名别名，见 iface_alias.go。

// ---- from: atcmd/quirks.go ----

// ---- from: atcmd/vendorcmd.go ----

// 本文件是「指令层」的主体：各厂商的指令集、能力接口与厂商识别逻辑。
//
// 这里只做「指令拼装 + 应答解析」，不碰串口与模组状态（小区/频段解析见 cell.go），
// 模组状态、串口、缓存都在 modem.go / query.go 段里。
// 需要发指令的厂商能力实现，通过 ATDoer / LockATDoer 这两个窄接口取，
// 所以厂商指令类型（QuectelCommand / tdtech.MT5700Command…）的全部方法都集中在这个文件里。
//
// 厂商枚举（ModemVendor）、小区技术枚举（CellTech）与 ATDoer / LockATDoer
// 已搬到 modemd/iface（厂商层也要用，不能反向依赖内核）；
// kernel 里保留同名别名，见 iface_alias.go —— 下面这些调用点一行都不用改。

// 需要"运行期候选探测"的功能（见 kernel 里 modem_at.go 的 resolveVendorCmd）
const (
	cmdKindServing   = "serving_cell"   // 服务小区 / NR 信号
	cmdKindNeighbour = "neighbour_cell" // 邻区
	cmdKindTraffic   = "traffic"        // 数据流量计数器（上下行速度）
	cmdKindAPN       = "apn"            // 当前 APN
	cmdKindPDPAddr   = "pdp_addr"       // PDP 上下文地址（IPv4/IPv6）
)

// 指令常量的源头在 module/common（多条指令被多个厂商共用，字面量只应存在一份）；
// cmdXxx 是包内短名，下面「指令常量（包内统一用大写名）」那段再导出成 CmdXxx。
const ()

// cmdCGCONTRDP 见 module/common/cmds.go（多厂商共用同一个字面量）。

// cmdCNMINotify 新短信上报配置（3GPP TS 27.005 标准指令，各家写法一致）。
//
//	mode=2 链路空闲时直接上报、忙时先缓冲；mt=1 新短信用 +CMTI 上报（本程序只解析 +CMTI）；
//	bm=0 不上报广播消息、ds=0 不上报状态报告、bfr=0 不额外清缓冲。
//
// 不显式下发就只能听模组自己的值：一旦它是 0,0,0,0,0（或 mt=2/3 改用 +CMT），
// 新短信推送会**静默失效** —— 页面看不出异常，只是永远不弹窗。
const cmdCNMINotify = "AT+CNMI=2,1,0,0,0"

// cmdQueryCNMINotify 回读新短信上报配置
const cmdQueryCNMINotify = "AT+CNMI?"

// ====================== 指令常量（包内统一用大写名） ======================
//
// 常量/兜底类型在这里统一成大写名（历史原因：以前它们住在子包里，
// 跨包只能看见大写名）。现在同一个包里两者等价，保留大写名只为少改调用点。
const (
	CmdCNMINotify      = cmdCNMINotify
	CmdQueryCNMINotify = cmdQueryCNMINotify
)

// 运行期候选探测的功能键（见 modem.go resolveVendorCmd）
const (
	KindServing   = cmdKindServing
	KindNeighbour = cmdKindNeighbour
	KindTraffic   = cmdKindTraffic
	KindAPN       = cmdKindAPN
	KindPDPAddr   = cmdKindPDPAddr
)

// ModemCommand 与全部「可选能力」接口已搬到 modemd/iface（见 iface/command.go）：
// 厂商实现要声明自己实现了哪些能力，就不能让这些接口住在被它们依赖的引擎包里。
// kernel 保留同名别名，见 iface_alias.go。
//
// ⚠️ 以后新增能力：加一个新接口，**别**往 ModemCommand 里塞方法 ——
// 每加一个方法，所有厂商实现都得跟着补，那正是本项目要避免的。

// ====================== 移远 Quectel ======================

// QuectelCommand 的完整实现已搬到 modemd/module/quectel（见 module/quectel/command.go）。
//
// 这里只留别名：`NewVendorCommand` / `quectel.QcfgBandLocker` / 各测试的 `&QuectelCommand{}`
// 全部原样工作，方法集随别名一并带过来。
//
// ⚠️ 别名不能定义方法 —— 移远的东西以后都去 module/quectel 里改。

// ====================== 广和通 Fibocom ======================

// FibocomCommand 的完整实现已搬到 modemd/module/fibocom（见 module/fibocom/command.go）。

// （NSABandKey / ServingCellCandidates / NeighbourCellCandidates 见 module/fibocom/）

// （TrafficCandidates / TrafficEnableCommand / CommandCAInfo / ParseNRSignal 见 module/fibocom/）

// ---------- NetLocker 的指令实现（锁频段 / 锁基站） ----------
//
// 广和通的**高通平台**机型（FM150 / FM160-EAU，SDX55/SDX62）与移远走同一套高通标准指令：
//
//	锁频段  AT+QNWPREFCFG="lte_band"/"nr5g_band",<1:3:5>     解锁靠 policy_band 全量写回（不能写 0）
//	锁小区  AT+QNWLOCK="common/4g"/"common/5g",…
//	组网模式 AT+QNWPREFCFG="mode_pref"
//
// 因此直接复用 lock.go 的通用流程与 parseQNWPREFCFG / parseQNWLOCK 解析，
// **不需要** LockVendorOps（没有鼎桥那种三段式应答）。
//
// ⚠️ 广和通的另两个平台固件**不认**这一套（`AT+QNWPREFCFG` 回 ERROR）：
//   - FM350-GL（MTK，固件 81600.…）——手册里只有 +GTACT / +EPBSEH 的频段选择，没有锁基站指令；
//   - FM650-CN（展锐）——同样不认 QNW。
//
// 这些机型下单前会被「探测固件支持」（ProbeLockCaps / GET /api/lock/caps）拦下，
// 不会把未验证的指令写进真实模组。
//
// 指令实现见 module/quectel 的 `QnwLockCommands`（与移远**字符级相同**，所以只存一份）。

// ====================== 芯讯通 Simcom ======================

// SimcomCommand 的完整实现已搬到 modemd/module/simcom（见 module/simcom/command.go）。

// （ServingCellCandidates / NeighbourCellCandidates / TrafficCandidates 见 module/simcom/）

// （CommandTemp / CommandExtraNetInfo / CommandVoltage 见 module/simcom/）

// （ParseNRSignal 见 module/simcom/）

// ====================== MT5700 / 未识别型号（自适应指令集） ======================

// adaptiveCommands 的完整实现已搬到 modemd/module/common（见 module/common/adaptive.go）。
//
// 嵌入它的三家（unknownCommand / UnisocCommand / tdtech.MT5700Command）靠别名原样工作。

// （CommandNRServingCell / CommandNeighbourCell / CommandServingCell / CommandNetInfo
//   与两组候选指令、流量候选见 module/common/adaptive.go）

// （流量候选 / USBnet 候选 / ParseNRSignal 见 module/common/adaptive.go）

// ====================== 美格智能（MeiG / MeigLink） ======================
//
// MeigCommand 的完整实现已搬到 modemd/module/meig（见 module/meig/command.go）——
// 那是个**跨包嵌入**的样板：它嵌 quectel.QuectelCommand，只覆盖识别与流量两处。
//
// 这里只留别名，调用点与测试原样工作。

// ---- tdtech.MT5700Command 已搬到 modemd/module/tdtech（见 module/tdtech/command.go）；
// kernel 保留同名别名，见 tdtech_alias.go ----

// unknownCommand / UnknownCommand / AdaptiveCommands 的完整实现已搬到
// modemd/module/common（见 module/common/unknown.go 与 adaptive.go）。
//
// 三个名字都保留：`unknownCommand` 给包内 `NewVendorCommand` 用，
// `UnknownCommand` 是历史导出名（引擎侧当默认实现用），
// `AdaptiveCommands` 也是历史导出名（测试里会单独构造）。

// UnknownCommand 导出别名：未识别型号的兜底实现（引擎侧当默认实现用）

// AdaptiveCommands 导出别名：自适应指令集（未识别型号的底座；测试里会单独构造）

// UnisocCommand 的完整实现已搬到 modemd/module/unisoc（见 module/unisoc/command.go）。

// （CommandAMBR 见 module/unisoc/command.go）

// （小区信息 / 候选表 / 流量候选 的覆写见 module/unisoc/command.go）

// （USSDCode / APNCandidates 见 module/unisoc/command.go）

// （USB 网卡模式相关覆写见 module/unisoc/command.go）

// （短信 PDU 三方法见 module/unisoc/command.go）

// DetectVendorFromATI 依次用各厂商的识别规则匹配 ATI 应答。
//
// 注意：不能只用“当前实现”的 DetectVendor —— 初始实现是兜底实现，
// 它只会返回 VendorUnknown，结果广和通/芯讯通/MT5700 永远识别不出来。
func DetectVendorFromATI(resp []string) ModemVendor {
	for _, v := range registry.Vendors {
		if NewVendorCommand(v).DetectVendor(resp) == v {
			return v
		}
	}
	return VendorUnknown
}

// NewVendorCommand 工厂：根据厂商返回对应指令实现。
//
// 实现已改为**从注册表派生**（见 modemd/module/registry）：
// 加一家厂商 = 新建 module/<vendor>/ 目录 + 在注册表加一条，这里一行都不用改。
// 未注册（含未知厂商）由注册表返回自适应指令集兜底（首选指令与移远一致，且带候选回退）。
func NewVendorCommand(vendor ModemVendor) ModemCommand {
	return registry.Command(vendor)
}
