// Package unisoc 展锐（Unisoc）平台方言。
//
// 主要服务于**中兴 F50（UMS9620 SoC 内置 5G 基带）**，它的三个特点决定了这里的写法：
//  1. AT 口是 SIPC 虚拟串口 `/dev/stty_nr2`（模组直连，~10ms/条），
//     **不是 tty**、也不走厂商 daemon —— 策略见 module/kernel/atdev_mu300_policy.go；
//  2. **ATI 不被支持**（回 `+CME ERROR: 4`），只能靠 `AT+CGMR` 里的 `ums9620_modem` 认；
//  3. 标准 3GPP 指令都能用，所以基础部分直接复用自适应指令集，只覆写下面几处。
//
// 厂商私有族是 `AT+SP*`（SPIMEI / SPCOMMLOCKBAND / SPCOMMLOCKFRQ / SPCELLLOCK / SPRAT）。
//
// ⚠️ 只把**已验证**的差异写在这里；没取证的取值一律不臆造（项目规则）。
package unisoc

import (
	"encoding/hex"
	"fmt"
	"strings"

	"modemd/iface"
	"modemd/module/common"
)

type UnisocCommand struct{ common.AdaptiveCommands }

var _ iface.ModemCommand = (*UnisocCommand)(nil)

// DetectVendor 从应答里认展锐。
// ATI 不可用时调用方会用 AT+CGMR 的原文重跑一遍识别 —— 所以这里的
// 关键字要能命中 `HW Version: ums9620_modem` / `MOCORTM_V2_…` 这类固件串。
// ⚠️ `SPRD` 放在靠后：它太短，别的厂商也可能巧合命中（本函数在
// DetectVendorFromATI 里排在移远/广和通/芯讯通/美格/鼎桥**之后**）。
func (u *UnisocCommand) DetectVendor(resp []string) iface.ModemVendor {
	joined := strings.ToUpper(strings.Join(resp, " "))
	for _, k := range []string{"UMS9620", "UNISOC", "MOCORTM", "QOGIRN", "SPRD"} {
		if strings.Contains(joined, k) {
			return iface.VendorUnisoc
		}
	}
	return iface.VendorUnknown
}

// CommandModel 展锐不支持 ATI（实测 `+CME ERROR: 4`），改用 AT+CGMR：
// 它一次给出平台/工程/BASE/硬件版本，界面「型号」一栏可用度比 ATI 高。
func (u *UnisocCommand) CommandModel() string { return "AT+CGMR" }

// CommandCAInfo 展锐没有 `AT+QCAINFO`（那是移远的私有指令）。
// 返回空串 = "本厂商未实现，不要去发" —— 免得界面上多两条注定 ERROR 的指令。
func (u *UnisocCommand) CommandCAInfo() string { return "" }

// CommandAMBR 签约速率（AMBR）+ QCI：展锐固件**原生实现了 3GPP 的 `AT+C5GQOSRDP`**
// （5GS QoS：Read Dynamic Parameters），这是本机唯一能同时给出 AMBR 与 QCI 的指令。
//
// 真机取证（2026-10-06，中兴 F50 / UMS9620，走 modemd 的 nr2 快路）：
//
//	AT+C5GQOSRDP    → +C5GQOSRDP:1,9,0,0,0,0,1000000,1000000,0   cid 1（数据 ctnet）：5QI=9、SAMBR 1000/1000 Mbps
//	                  +C5GQOSRDP:11,5,0,0,0,0,300000,75000,0     cid 11（IMS）：5QI=5、300/75 Mbps
//	AT+C5GQOSRDP=?  → +C5GQOSRDP:()     不带参数（"=1"这种写法不被接受）
//
// 布局与有方 N510M **逐字一致**（`<cid>,<5QI>,GFBR,MFBR,<DL_SAMBR>,<UL_SAMBR>`，kbit/s）⇒ 复用
// 引擎侧的 `ParseC5GQOSRDP`；不带 cid 查询时每条 PDP 上下文各回一行，解析按**拨号那条 cid** 选。
func (u *UnisocCommand) CommandAMBR() string { return "AT+C5GQOSRDP" }

// ---- 小区信息：本机**不通过 AT 上报**（2026-10-05 真机全面取证）----
//
// 逐个试过、**全部 `+CME ERROR: 4`**（即"不认识的指令"）：
//
//	AT+QENG="servingcell" / AT+QENG="neighbourcell"   移远
//	AT+CPSI? / AT+QCSQ                                  芯讯通 / MTK 系
//	AT+QNWINFO / AT+CNWINFO? / AT+GTCCINFO?              移远 / 芯讯通 / 广和通
//	AT^MONSC                                            鼎桥
//	AT+SPCELLINFO=? / AT+SPENGINEERMODE=? / AT+SPATCELLINFO=? /
//	AT+CGCELLINFO=? / AT+SPNETINFO=? / AT+SPCFGINFO=?   展锐可能的名字（同样全部拒绝）
//
// 厂商自己的 UI 是从模组日志通道（`/dev/slog_*`）与 SIPC 侧拿小区信息的，**没走 AT**。
// ⇒ 与其每轮白发几条注定失败的指令（在共享慢总线上每条要 2 秒），
// 不如直接声明"本机不提供"：界面显示"未上报"，提示也能说实话。
// 信号值不受影响（走 3GPP 标准的 `AT+CSQ` / `AT+CESQ`，本机都支持）。
func (u *UnisocCommand) CommandServingCell() string   { return "" }
func (u *UnisocCommand) CommandNeighbourCell() string { return "" }
func (u *UnisocCommand) CommandNetInfo() string       { return "" }

// ServingCellCandidates / NeighbourCellCandidates 返回 nil。
//
// ⚠️ 必须连候选一起清掉：`vendorCandidates()` 会**先问候选表**，
// 而嵌入式自适应指令集的候选表里带着 QENG/CPSI/QCSQ/MONSC 四条，
// 只覆写 CommandServingCell() 是拦不住它们的。
func (u *UnisocCommand) ServingCellCandidates() []string   { return nil }
func (u *UnisocCommand) NeighbourCellCandidates() []string { return nil }

// TrafficCandidates 流量计数器：展锐 UMS9620 也没有可用的 AT 计数器
// （AT+QGDCNT? / AT+^DSFLOWQRY / AT+GTSTATIS? / AT+SPDCNT? 实测全回 +CME ERROR: 4）。
// 返回 nil ⇒ 该能力被停用，不再每轮白发一条无效指令。
func (u *UnisocCommand) TrafficCandidates() []string  { return nil }
func (u *UnisocCommand) TrafficEnableCommand() string { return "" }

// USSDCode 展锐固件默认 `AT+CSCS="HEX"`：USSD 码必须 hex 编码。
//
// 实测：`AT+CUSD=1,"*101#",15` → `+CME ERROR: 3`；
// 发 `AT+CUSD=1,"2A31303123",15` 才被接受。
func (u *UnisocCommand) USSDCode(code string) string {
	return strings.ToUpper(hex.EncodeToString([]byte(code)))
}

// APNCandidates 展锐只认**带 cid** 的 CGCONTRDP（见 iface.APNCandidatesProvider 的实测记录）。
// cid 1 是数据上下文（cid 11 是 IMS），这里只查数据那条；
// 前两条失败时依然退回标准写法（探测不到就取第一条，行为与以前一致）。
func (u *UnisocCommand) APNCandidates() []string {
	return []string{"AT+CGCONTRDP=1", common.CmdCGCONTRDP, "AT+CGDCONT?"}
}

// ---- USB 网卡模式：本机**没有这个概念** ----
//
// `AT+QCFG` 是移远私有指令，本机实测 `AT+QCFG=?` → `+CME ERROR: 4`。
// 而且这台机器本来就不靠 USB 模组形态上网（5G 基带在 SoC 内部，AT 走 SIPC 虚拟串口；
// `/sys/bus/usb/devices` 是空的，USB 侧只有个给 LAN 用的 NCM gadget，由厂商配置）。
//
// ⚠️ 原来自适应指令集把这两个方法也继承了，于是 modemd 每轮都会发一条
// `AT+QCFG="usbnet"` —— 在共享慢总线上白花 2 秒，界面还多一个注定为空的
// 「USB 网卡模式」面板。返回空串 = "本厂商未实现，不要去发"（同 CommandCAInfo 的口径）。
func (u *UnisocCommand) CommandQueryUSBnet() string { return "" }

func (u *UnisocCommand) CommandSetUSBnet(string) string { return "" }

// ---- 短信（PDUSMSReader / PDUSMSSender）：这台机器**只能**走 PDU 模式 ----
//
// 真机取证（UMS9620 固件）：
//
//	AT+CSCS? → "HEX"      —— 默认字符集是 HEX，文本模式的号码/正文都会变成十六进制
//	AT+CMGF? → 0          —— 默认就是 PDU
//	AT+CNMI? → 0,0,0,1,0  —— **不发 +CMTI**，新短信只能靠轮询 AT+CMGL=4
//	AT+CPMS? → "SM",10,40
//
// 所以读与发都声明走 PDU。

// CommandSMSModePDU 切到 PDU 模式（本机默认就是 0，显式再设一次更保险）
func (u *UnisocCommand) CommandSMSModePDU() string { return "AT+CMGF=0" }

// CommandReadAllSMSPDU PDU 模式下 4 = 全部短信（对应文本模式的 "ALL"）
func (u *UnisocCommand) CommandReadAllSMSPDU() string { return "AT+CMGL=4" }

// CommandSendSMSPDULength 提交 PDU 的第一步：`AT+CMGS=<TPDU 字节数>`。
// 它只回一个 `>` 提示符（没有最终结果码），正文随后单独发。
func (u *UnisocCommand) CommandSendSMSPDULength(tpduLen int) string {
	return fmt.Sprintf("AT+CMGS=%d", tpduLen)
}

// 编译期断言：展锐声明了哪些可选能力。
var (
	_ iface.AMBRQuerier           = (*UnisocCommand)(nil)
	_ iface.USSDFormatter         = (*UnisocCommand)(nil)
	_ iface.APNCandidatesProvider = (*UnisocCommand)(nil)
	_ iface.PDUSMSReader          = (*UnisocCommand)(nil)
	_ iface.PDUSMSSender          = (*UnisocCommand)(nil)
	_ iface.CommandCandidates     = (*UnisocCommand)(nil)
)
