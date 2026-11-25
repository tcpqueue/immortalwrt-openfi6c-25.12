package lock

// ====================== 本包用到的「外部契约」的本地别名 ======================
//
// 与 module/quectel 同一手法：把 iface / module/common / platform / catalog 等
// 被依赖的名字在本包重新声明一遍，于是**搬过来的函数体一个字符都不用改**，
// 也不必满屏写 `iface.` `common.` 前缀。
//
// ⚠️ 这些是**别名**（=）而不是新类型：与外部是同一个类型，不需要显式转换。
// ⚠️ 新增依赖时**只在这里加一行**，不要去改方言文件 —— 那会破坏"搬迁零改动"这条线。

import (
	"modemd/iface"
	"modemd/module/catalog"
	"modemd/module/common"
	"modemd/module/platform"

	"go.uber.org/zap"
)

// ---- 契约（iface）----

type (
	CellTech        = iface.CellTech
	CellLock        = iface.CellLock
	CellLockRequest = iface.CellLockRequest
	LockATDoer      = iface.LockATDoer
	NSAIndependent  = iface.NSAIndependent
	BandLockSetter  = iface.BandLockSetter
	NetLocker       = iface.NetLocker
	LockVendorOps   = iface.LockVendorOps
	ModemCommand    = iface.ModemCommand
)

const (
	CellTechLTE   = iface.CellTechLTE
	CellTechNR    = iface.CellTechNR
	CellTechWCDMA = iface.CellTechWCDMA
	CellTechNRNSA = iface.CellTechNRNSA

	CellLockNone     = iface.CellLockNone
	CellLockARFCN    = iface.CellLockARFCN
	CellLockARFCNPCI = iface.CellLockARFCNPCI
)

var normalizeSCS = iface.NormalizeSCS

// ---- 共用工具（module/common）----

var (
	SplitATFields  = common.SplitATFields
	ParseATInt     = common.ParseATInt
	AtIntOr0       = common.AtIntOr0
	RespHasError   = common.RespHasError
	MergeBands     = common.MergeBands
	FormatBandList = common.FormatBandList

	cleanHexField   = common.CleanHexField
	isHexBitmap     = common.IsHexBitmap
	hexBitmapBands  = common.HexBitmapBands
	splitBandBitmap = common.SplitBandBitmap
	hexBitmapString = common.HexBitmapString
	hexBitmapPadded = common.HexBitmapPadded
)

// ---- 型号清单 / 平台判定 ----

var (
	CatalogEntryForModel = catalog.CatalogEntryForModel
	catalogFlatten       = catalog.CatalogFlatten
	PlatformForModel     = platform.PlatformForModel
)

const (
	PlatformQualcomm = platform.PlatformQualcomm
	PlatformMediaTek = platform.PlatformMediaTek
)

// respHasError 判断应答里是不是 ERROR —— 锁网方言用它决定“这条固件不支持”
var respHasError = common.RespHasError

// ---- 日志 ----
//
// 与 module/tdtech 同一套手法：厂商包声明自己只需要 Info/Warn 两个方法，
// 内核在启动时用 SetLogger 注入（注入点在 module/kernel/parse_bridge.go）。
// 未注入时是安全默认（什么都不打），不会 panic。

var logger FieldLogger = nopLogger{}

// FieldLogger 厂商包需要的日志能力（方法集与内核的日志代理一致）
type FieldLogger interface {
	Info(msg string, fields ...zap.Field)
	Warn(msg string, fields ...zap.Field)
}

// nopLogger 未注入时的安全默认值
type nopLogger struct{}

func (nopLogger) Info(string, ...zap.Field) {}
func (nopLogger) Warn(string, ...zap.Field) {}

// SetLogger 注入内核的日志实现
func SetLogger(l FieldLogger) {
	if l != nil {
		logger = l
	}
}

// ---- 模式方言小件（P1.5a 解耦后由 common 提供）----

var (
	normalizeModePref   = common.NormalizeModePref
	parseMT5700LTEBands = common.ParseLTEBandNames
	mergeBands          = common.MergeBands
	ParseBandList       = common.ParseBandList
)

const (
	ModeAuto     = common.ModeAuto
	ModeWCDMA    = common.ModeWCDMA
	ModeLTE      = common.ModeLTE
	ModeNR5G     = common.ModeNR5G
	ModeWCDMALTE = common.ModeWCDMALTE
	ModeWCDMANR  = common.ModeWCDMANR
	ModeLTENR5G  = common.ModeLTENR5G
)

type mt5700Syscfg = common.SyscfgEX

var parseSyscfgEX = common.ParseSyscfgEX

// Logger 取注入的日志实现（品牌包里要打日志时用 lock.Logger()）。
//
// 原来品牌文件与本包同处一个包，直接写 logger.Warn(...)；搬进 module/<品牌>/ 之后
// 变成跨包访问，用这个只读访问器代替（仍然是"未注入 = 什么都不打"）。
func Logger() FieldLogger { return logger }
