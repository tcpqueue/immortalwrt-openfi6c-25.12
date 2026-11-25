package kernel

// 本文件是**契约门面**：kernel 引用 iface（契约层）的唯一入口。
//
// 它曾经是「迁移期的兼容层」，P1~P3 把厂商知识全部搬进 modemd/module/* 之后，
// 其余 13 个别名文件都已删除（kernel 现在直接写 `usbdialect.X` / `lock.Y` / `variant.Z`），
// **只保留这一个**，理由有两条：
//
//  1. 内容全是**契约**（类型别名 / 常量 / 纯转发函数），不含任何厂商知识 ——
//     厂商知识在 P1/P2 已全部进入 modemd/module/*；
//  2. 技术原因：kernel 有若干文件的局部变量就叫 `iface`（网络接口名，如
//     netprov_engine.go 的 `iface := WANInterfaceName(mod.ID)`）。一旦这些文件
//     `import "modemd/iface"`，局部变量就会遮蔽包名，报错形如
//     `iface.VendorLabel undefined (type string has no field or method VendorLabel)`；
//     硬展开只能被迫写成 `ifacepkg.VendorLabel`，可读性反而更差。
//
// 新增契约名就往对应块里加一条；**不要在这里放厂商实现**。

import (
	"modemd/iface"
	"modemd/module/common"
	"modemd/module/quectel"
)

// ---- QNW 锁网方言（module/quectel）与频段列表格式化（module/common） ----
//
// 这些名字原来是 kernel 定义并导出的，引擎（lock.go / lock_simcom.go / schedule.go）
// 与测试都在用。接成 var 之后调用点一行不用改。

var (
	QnwBandKey     = quectel.BandKey
	QnwLockKind    = quectel.LockKind
	FormatBandList = common.FormatBandList
)

// ---- 厂商指令底座（module/common） ----
//
// 各厂商类型的 `struct{ stdCommands }` 靠这个别名一行不改地继续工作。
// 注意：**不能再给它定义方法**（Go 不允许给外部类型定义方法）。
//
// ⚠️ cmdCNMINotify / cmdQueryCNMINotify 仍留在 atcmd.go（它们已被广泛引用，
// 且注释里写明取值原因），本层不再重复声明。

type stdCommands = common.StdCommands

// ---- 厂商枚举 ----

type ModemVendor = iface.ModemVendor

const (
	VendorUnknown = iface.VendorUnknown
	VendorQuectel = iface.VendorQuectel
	VendorFibocom = iface.VendorFibocom
	VendorSimcom  = iface.VendorSimcom
	VendorMeig    = iface.VendorMeig
	VendorTDTech  = iface.VendorTDTech
	VendorUnisoc  = iface.VendorUnisoc
)

// ---- 小区接入技术 ----

type CellTech = iface.CellTech

const (
	CellTechLTE   = iface.CellTechLTE
	CellTechNR    = iface.CellTechNR
	CellTechWCDMA = iface.CellTechWCDMA
	CellTechGSM   = iface.CellTechGSM
	CellTechNRNSA = iface.CellTechNRNSA
)

// ---- AT 能力接口 ----

type ATDoer = iface.ATDoer

type LockATDoer = iface.LockATDoer

// ---- 锁网：类型与能力接口 ----

type CellLock = iface.CellLock

const (
	CellLockNone     = iface.CellLockNone
	CellLockARFCNPCI = iface.CellLockARFCNPCI
	CellLockARFCN    = iface.CellLockARFCN
)

// CellLockRequest 带 Band() 方法，别名会把方法一并带过来（这是别名相对新类型
// 的另一个好处：方法集不变，调用点无感）。
type CellLockRequest = iface.CellLockRequest

type NSAIndependent = iface.NSAIndependent

type BandLockSetter = iface.BandLockSetter

type NetLocker = iface.NetLocker

type LockVendorOps = iface.LockVendorOps

// NormalizeSCS / normalizeSCS 原本就是 var（func 值），这里照样用 var 接。
var (
	NormalizeSCS = iface.NormalizeSCS
)

// ---- USB 网卡模式 ----

type Mode = iface.Mode

// ---- 载波聚合（CA）----

type CCInfo = iface.CCInfo

type CAVendorOps = iface.CAVendorOps

// ---- 指令能力接口 ----

type ModemCommand = iface.ModemCommand

type CommandCandidates = iface.CommandCandidates

type USBnetConfigurator = iface.USBnetConfigurator

type AMBRQuerier = iface.AMBRQuerier

type PDUSMSReader = iface.PDUSMSReader

type PDUSMSSender = iface.PDUSMSSender

// StandardPDUReader 在 kernel 里是作为「型号特例用的标准 PDU 读法」被引用的，
// 别名会把它的两个方法一起带过来。
type StandardPDUReader = iface.StandardPDUReader

type TempQuerier = iface.TempQuerier

type ExtraNetInfoQuerier = iface.ExtraNetInfoQuerier

type VoltageQuerier = iface.VoltageQuerier

type ICCIDQuerier = iface.ICCIDQuerier

type USSDFormatter = iface.USSDFormatter

type APNCandidatesProvider = iface.APNCandidatesProvider

// ---- 函数转发 ----
//
// Go 不能给函数起别名，只能包一层。这几个是纯转发，没有额外语义。

// VendorLabel 厂商可读名（实现已搬到 iface，这里保留同名入口供迁移期使用）。
func VendorLabel(v ModemVendor) string { return iface.VendorLabel(v) }

// BandFromEARFCN 由频点号推算频段号（实现已搬到 iface）。
func BandFromEARFCN(tech CellTech, earfcn int) int { return iface.BandFromEARFCN(tech, earfcn) }

// ---- 移远 QNW 系（module/quectel）----
//
// 注意：函数**可以**用 `var F = pkg.F` 直接别名（同一份实现，签名不合立即编译失败）；
// 只有历史上写成"包装函数"的那几个是上面那种写法。

var parseQNWLOCK = quectel.ParseQNWLOCK

// cmdQNWLockSave 移远的小区锁定持久化指令（旧名保留）
const cmdQNWLockSave = quectel.QNWLockSaveCmd
