package registry

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

// ====================== 3GPP 标准指令底座（stdCommands） ======================
//
// 为什么有这个文件：下面这 14 条是 **3GPP 标准**（TS 27.007 / 27.005），
// 各家固件的写法完全一致 —— 以前**每个厂商类型都各写一遍**
// （实测 go 里有 41 组、约 136 行逐字重复：`AT+CSQ` 被 4 个类型各写一遍、
// `CommandNetInfo` 6 遍、`TrafficEnableCommand` 7 遍、锁网那 10 个方法在移远/广和通
// 之间**字符级完全相同**），改一条要改 4~6 处，漏改还不报错。
//
// 现在它们只存在一份，厂商类型**嵌入**即可：
//
//	type QuectelCommand struct{ stdCommands }
//	func (q *QuectelCommand) CommandServingCell() string { return `AT+QENG="servingcell"` }  // 只写不一样的
//
// ⚠️ 三条规矩（这条是"嵌入"这种复用方式的护栏，别破坏）：
//
//  1. **只放"标准且无歧义"的**：不带任何厂商私有语义。QENG/FENG/CPSI/MONSC 那类
//     一定不放进来（它们是"某厂商/某平台才有"的）。
//  2. **有厂商差异的（CommandNetInfo / CommandServingCell / CommandCAInfo / ParseNRSignal…）
//     留在各厂商类型里**，底座**不提供默认值** —— 因为"默认给错指令"比"默认不给"危险得多
//     （本项目口径：没取证就不发，见 vendorcmd.go）。
//  3. Go 的嵌入是**编译期**决定、**没有虚函数**：子类型忘了覆盖不会报错，
//     只会静默用父类的行为。所以底座只放"标准且不会错"的东西，
//     并由 `atcmd_invariants_test.go` 断言"厂商类型不得重抄底座已有的实现"。
//
// 历史事故（都是这一条的变体，记在这里避免重演）：
//   - `MeigCommand` 嵌 `QuectelCommand` ⇒ 静默继承 `AT+QNWCFG="nr5g_ambr"`（美格不认）；
//   - 同上 ⇒ 静默继承 `TrafficCandidates()=AT+QGDCNT?` 与使能指令 `AT+QGDCNT=1`（美格是速率式）。
// StdCommands 与上面两条 CmdCNMINotify / CmdQueryCNMINotify 已搬到
// modemd/module/common（厂商实现要嵌入它，不能让底座住在被依赖的引擎包里）。
// kernel 保留同名别名 `stdCommands`，见 iface_alias.go ——
// 所以各厂商类型的 `struct{ stdCommands }` 一行都不用改。

// 编译期断言：每个厂商类型都满足 iface.ModemCommand。
//
// 嵌入 stdCommands / qnwLockCommands 后，少写一个方法（或写错签名）都会**在这里编译失败** ——
// 比等到运行期 type switch 静默失败早得多。新增厂商类型时把它加进来。
var (
	_ iface.ModemCommand = (*quectel.QuectelCommand)(nil)
	_ iface.ModemCommand = (*fibocom.FibocomCommand)(nil)
	_ iface.ModemCommand = (*simcom.SimcomCommand)(nil)
	_ iface.ModemCommand = (*meig.MeigCommand)(nil)
	_ iface.ModemCommand = (*tdtech.MT5700Command)(nil)
	_ iface.ModemCommand = (*unisoc.UnisocCommand)(nil)
	_ iface.ModemCommand = (*common.UnknownCommand)(nil)
)

// 可选能力接口的断言：这一类"实现了才算声明"。
// ⚠️ 别把它们下沉到 stdCommands —— 一旦谁都"实现"了，
// `mod.cmdImpl.(iface.TempQuerier)` 这类**类型断言**就永远为真，
// 整个项目"未实现的厂商零影响"的设计就失效了（会向没这条指令的固件乱发 AT）。
var (
	_ iface.CommandCandidates     = (*quectel.QuectelCommand)(nil)
	_ iface.CommandCandidates     = (*fibocom.FibocomCommand)(nil)
	_ iface.CommandCandidates     = (*simcom.SimcomCommand)(nil)
	_ iface.CommandCandidates     = (*common.AdaptiveCommands)(nil)
	_ iface.CommandCandidates     = (*tdtech.MT5700Command)(nil)
	_ iface.NetLocker             = (*quectel.QuectelCommand)(nil)
	_ iface.NetLocker             = (*fibocom.FibocomCommand)(nil)
	_ iface.USBnetConfigurator    = (*quectel.QuectelCommand)(nil)
	_ iface.USBnetConfigurator    = (*common.AdaptiveCommands)(nil)
	_ iface.AMBRQuerier           = (*quectel.QuectelCommand)(nil)
	_ iface.AMBRQuerier           = (*tdtech.MT5700Command)(nil)
	_ iface.AMBRQuerier           = (*unisoc.UnisocCommand)(nil)
	_ iface.TempQuerier           = (*quectel.QuectelCommand)(nil)
	_ iface.TempQuerier           = (*simcom.SimcomCommand)(nil)
	_ iface.TempQuerier           = (*tdtech.MT5700Command)(nil)
	_ iface.VoltageQuerier        = (*simcom.SimcomCommand)(nil)
	_ iface.ICCIDQuerier          = (*tdtech.MT5700Command)(nil)
	_ iface.ExtraNetInfoQuerier   = (*simcom.SimcomCommand)(nil)
	_ iface.ExtraNetInfoQuerier   = (*tdtech.MT5700Command)(nil)
	_ iface.PDUSMSReader          = (*tdtech.MT5700Command)(nil)
	_ iface.PDUSMSReader          = iface.StandardPDUReader{}
	_ iface.PDUSMSSender          = (*unisoc.UnisocCommand)(nil)
	_ iface.USSDFormatter         = (*unisoc.UnisocCommand)(nil)
	_ iface.APNCandidatesProvider = (*unisoc.UnisocCommand)(nil)
)
