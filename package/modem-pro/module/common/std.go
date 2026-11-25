// Package common 放「所有厂商都一样的指令底座」。
//
// 这是 module/ 的最底层：厂商实现（module/quectel、module/fibocom…）**嵌入**这里的
// 类型来复用标准指令，各自的包只写"与标准不同"的部分。
//
// 依赖方向：module/common ──> iface。**不允许**反向依赖 kernel。
package common

import "fmt"

// ====================== 3GPP 标准指令常量 ======================

// CmdCNMINotify 新短信上报配置（3GPP TS 27.005 标准指令，各家写法一致）。
//
//	mode=2 链路空闲时直接上报、忙时先缓冲；mt=1 新短信用 +CMTI 上报（本程序只解析 +CMTI）；
//	bm=0 不上报广播消息、ds=0 不上报状态报告、bfr=0 不额外清缓冲。
//
// 不显式下发就只能听模组自己的值：一旦它是 0,0,0,0,0（或 mt=2/3 改用 +CMT），
// 新短信推送会**静默失效** —— 页面看不出异常，只是永远不弹窗。
const CmdCNMINotify = "AT+CNMI=2,1,0,0,0"

// CmdQueryCNMINotify 回读新短信上报配置
const CmdQueryCNMINotify = "AT+CNMI?"

// ====================== 3GPP 标准指令底座（StdCommands） ======================
//
// 下面这 14 条是 **3GPP 标准**（TS 27.007 / 27.005），各家固件的写法完全一致 ——
// 以前**每个厂商类型都各写一遍**（实测 module/kernel/atcmd.go 里有 41 组、约 136 行逐字重复：
// `AT+CSQ` 被 4 个类型各写一遍、`CommandNetInfo` 6 遍、`TrafficEnableCommand` 7 遍、
// 锁网那 10 个方法在移远/广和通之间**字符级完全相同**），改一条要改 4~6 处，漏改还不报错。
//
// 现在它们只存在一份，厂商类型**嵌入**即可：
//
//	type QuectelCommand struct{ common.StdCommands }
//	func (q *QuectelCommand) CommandServingCell() string { return `AT+QENG="servingcell"` } // 只写不一样的
//
// ⚠️ 三条规矩（"嵌入"这种复用方式的护栏，别破坏）：
//
//  1. **只放"标准且无歧义"的**：不带任何厂商私有语义。QENG/FENG/CPSI/MONSC 那类
//     一定不放进来（它们是"某厂商/某平台才有"的）。
//  2. **有厂商差异的（CommandNetInfo / CommandServingCell / CommandCAInfo / ParseNRSignal…）
//     留在各厂商类型里**，底座**不提供默认值** —— 因为"默认给错指令"比"默认不给"危险得多
//     （本项目口径：没取证就不发）。
//  3. Go 的嵌入是**编译期**决定、**没有虚函数**：子类型忘了覆盖不会报错，
//     只会静默用父类的行为。所以底座只放"标准且不会错"的东西。
//
// 历史事故（都是这一条的变体，记在这里避免重演）：
//   - `MeigCommand` 嵌 `QuectelCommand` ⇒ 静默继承 `AT+QNWCFG="nr5g_ambr"`（美格不认）；
//   - 同上 ⇒ 静默继承 `TrafficCandidates()=AT+QGDCNT?` 与使能指令 `AT+QGDCNT=1`（美格是速率式）。
type StdCommands struct{}

// CommandModel 型号/身份：ATI（展锐例外 —— 它不支持 ATI，见 module/unisoc 的 UnisocCommand）。
func (StdCommands) CommandModel() string { return "ATI" }

// CommandCSQ 基础信号：AT+CSQ（3GPP TS 27.007 §8.5）。
func (StdCommands) CommandCSQ() string { return "AT+CSQ" }

// CommandSMSTextMode 短信文本模式：AT+CMGF=1。
func (StdCommands) CommandSMSTextMode() string { return "AT+CMGF=1" }

// CommandReadAllSMS 读取全部短信（文本模式）：AT+CMGL="ALL"。
func (StdCommands) CommandReadAllSMS() string { return `AT+CMGL="ALL"` }

// CommandSendSMS 发送短信第一步：AT+CMGS="<号码>"（随后等 `>` 再发正文）。
func (StdCommands) CommandSendSMS(phone string) string {
	return fmt.Sprintf(`AT+CMGS="%s"`, phone)
}

// CommandNetworkReg 网络注册状态：AT+CREG?（PS 域另有 CGREG?，见各调用点）。
func (StdCommands) CommandNetworkReg() string { return "AT+CREG?" }

// CommandIMEI 读 IMEI：AT+CGSN。
func (StdCommands) CommandIMEI() string { return "AT+CGSN" }

// CommandFirmware 固件版本：AT+CGMR。
func (StdCommands) CommandFirmware() string { return "AT+CGMR" }

// CommandSMSStorage 设置短信存储位置：AT+CPMS（三个存储都用同一个值）。
func (StdCommands) CommandSMSStorage(mem string) string {
	return fmt.Sprintf(`AT+CPMS="%s","%s","%s"`, mem, mem, mem)
}

// CommandQuerySMSStorage 查询短信存储用量：AT+CPMS?。
func (StdCommands) CommandQuerySMSStorage() string { return "AT+CPMS?" }

// CommandSMSNotify 新短信上报配置：不开这个收不到 +CMTI URC（见 CmdCNMINotify）。
func (StdCommands) CommandSMSNotify() string { return CmdCNMINotify }

// CommandQuerySMSNotify 回读新短信上报配置：AT+CNMI?。
func (StdCommands) CommandQuerySMSNotify() string { return CmdQueryCNMINotify }

// CommandDeleteSMS 删除短信；index < 0 表示删除全部（delflag=4）。
func (StdCommands) CommandDeleteSMS(index int) string {
	if index < 0 {
		return "AT+CMGD=1,4" // delflag=4：删除首选存储中的全部短信
	}
	return fmt.Sprintf("AT+CMGD=%d", index)
}

// CommandOperator 当前运营商：AT+COPS?。
func (StdCommands) CommandOperator() string { return "AT+COPS?" }
