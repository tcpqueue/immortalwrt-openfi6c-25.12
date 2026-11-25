package usbdialect

// ====================== USB 网卡模式的方言表 ======================
//
// 从 kernel 搬过来：**纯数据 + 闭包**，只依赖字符串与 iface。
// 派发器 `usbDialectFor` 留在内核（要读 mod 的型号/厂商），
// 由它按 iface.DeviceInfo 选一个方言回去。
//
// 字段已导出（Set/Parse/Modes/Note/Pre/PreSet）—— 跨包访问必须导出；
// kernel 侧靠别名与 fix-field-exports.py 自动对齐。

import (
	"modemd/iface"
	"modemd/module/common"
	"modemd/module/imeidialect"
)

// ====================== 本包用到的外部契约的本地别名 ======================
//
// 与 module/quectel / module/lock 同一手法：把被依赖的名字在本包重新声明一遍，
// 搬过来的函数体就**一个字符都不用改**。
// ⚠️ 这些都是别名（=），与外部是同一个类型，不需要显式转换。

type Mode = iface.Mode

const (
	VendorQuectel = iface.VendorQuectel
	VendorFibocom = iface.VendorFibocom
	VendorSimcom  = iface.VendorSimcom
	VendorMeig    = iface.VendorMeig
	VendorTDTech  = iface.VendorTDTech
	VendorUnisoc  = iface.VendorUnisoc
	VendorUnknown = iface.VendorUnknown
)

var (
	firstLineWith  = common.FirstLineWith
	cleanHexField  = common.CleanHexField
	numInLine      = imeidialect.NumInLine
	ParseATInt     = common.ParseATInt
	splitCSVFields = common.SplitCSVFields
	rawValueParser = imeidialect.RawValueParser
	reHex4         = imeidialect.ReHex4
)

// USBDialect USB 网卡模式的方言
type USBDialect struct {
	Query string                             // 查询指令
	Set   func(value string) string          // 取值 → 设置指令
	Parse func(resp []string) (string, bool) // 应答 → 取值
	Modes []Mode                             // 本机可选取值（value 就是本机取值本身）
	Note  string                             // 界面说明（生效方式 / 取值含义未取证等）
	// Pre 发主指令前的“进门”指令（逐条发，失败不报错）。
	// 目前只有 Sierra 用到：QModem `cmds/sierra.sh` 的注释写明高级指令（AT!USBCOMP）
	// 要先 `AT!ENTERCND="A710"` 解锁（vendor/sierra.sh 默认密码 A710）。
	Pre []string
	// PreSet **只在写入前**发的指令（读取时不发）。
	// 与 Pre 分开的理由：这类指令有副作用 —— openluat 切 USB 组合前要
	// `AT+RNDISCALL=0,0` 停掉当前的分组数据呼叫（QModem vendor/openluat.sh 的 set_mode()），
	// 那是会**断数据**的动作，绝不能跟着“查询”一起发。
	PreSet []string
}
