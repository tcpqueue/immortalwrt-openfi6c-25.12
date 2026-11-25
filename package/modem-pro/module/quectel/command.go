package quectel

import (
	"fmt"
	"strings"

	"modemd/iface"
	"modemd/module/common"
)

// QENGServing 服务小区查询的首选写法（移远/广和通/芯讯通都用它，只有指令头不同）。
// 源头在 module/common（多厂商共用同一个字面量），这里只做转发。
const QENGServing = common.CmdQENGServing

// ====================== 移远 Quectel ======================

// QuectelCommand 移远 Quectel。
//
// 嵌入 common.StdCommands（3GPP 标准那 14 条）与 QnwLockCommands（QNW 系锁网 10 条）；
// 这里只写**与标准不同**的：
// 服务小区/邻区/信号走 AT+QENG、接入信息 AT+QNWINFO、载波聚合 AT+QCAINFO。
//
// ⚠️ 新增厂商时照着这个结构来：一个包、一个 Command 类型、只写差异，其余靠嵌入。
type QuectelCommand struct {
	common.StdCommands
	QnwLockCommands
}

var _ iface.ModemCommand = (*QuectelCommand)(nil)

func (q *QuectelCommand) DetectVendor(resp []string) iface.ModemVendor {
	for _, s := range resp {
		if strings.Contains(strings.ToUpper(s), "QUECTEL") {
			return iface.VendorQuectel
		}
	}
	return iface.VendorUnknown
}

func (q *QuectelCommand) CommandNRServingCell() string { return `AT+QENG="servingcell"` }
func (q *QuectelCommand) CommandNeighbourCell() string { return `AT+QENG="neighbourcell"` }
func (q *QuectelCommand) CommandServingCell() string   { return `AT+QENG="servingcell"` }
func (q *QuectelCommand) CommandNetInfo() string       { return "AT+QNWINFO" }
func (q *QuectelCommand) CommandCAInfo() string        { return "AT+QCAINFO" }

// NSABandKey 移远的 NR 分 NSA / SA 两套列表（AT+QNWPREFCFG="nsa_nr5g_band" / "nr5g_band"）
// ⇒ 界面里两组可**分别**勾选、分别下发（见 kernel lock.go 的 NSAIndependent 用法）。
//
// ⚠️ 嵌入它的方言（美格 MeigCommand）会**自动继承**这条声明 —— 同为 QNW 系是有意的；
// 而嵌了 FibocomCommand 的 `fibocomMTKCommand`（GT 方言）必须**覆盖成空串**，
// 因为它只有一套 NR 频段。kernel 的 TestNSABandIndependenceDeclared 把名单钉住。
func (q *QuectelCommand) NSABandKey() string { return "nsa_nr5g_band" }

// ===== 移远的可选能力：候选指令 / 流量计数器 =====

func (q *QuectelCommand) ServingCellCandidates() []string { return []string{QENGServing} }

func (q *QuectelCommand) NeighbourCellCandidates() []string {
	return []string{`AT+QENG="neighbourcell"`}
}

// TrafficCandidates 移远数据计数器（已在 RM502Q-GL 实测：AT+QGDCNT? → +QGDCNT: <sent>,<received>）
func (q *QuectelCommand) TrafficCandidates() []string { return []string{"AT+QGDCNT?"} }

// TrafficEnableCommand 让计数器开始计数；AT+QGDCNT=0 会停止并清零
func (q *QuectelCommand) TrafficEnableCommand() string { return "AT+QGDCNT=1" }

// ===== 移远的可选能力：USB 网卡模式（实测 RM502Q-GL 支持） =====

func (q *QuectelCommand) CommandQueryUSBnet() string { return `AT+QCFG="usbnet"` }
func (q *QuectelCommand) CommandSetUSBnet(value string) string {
	return fmt.Sprintf(`AT+QCFG="usbnet",%s`, value)
}

// CommandAMBR 移远签约速率（实测 RM502Q-GL / 中国电信 5G SA）：
//
//	+QNWCFG: "nr5g_ambr","IMS",6,300,6,75
//	                                    ← CQI DL / AMBR DL / CQI UL / AMBR UL（单位 Mbps）
func (q *QuectelCommand) CommandAMBR() string { return `AT+QNWCFG="nr5g_ambr"` }

// CommandTemp 移远温度：AT+QTEMP 一次回全部传感器
func (q *QuectelCommand) CommandTemp() string { return "AT+QTEMP" }

// ParseNRSignal 统一走 kernel 注入的解析实现（见 module/common/hooks.go 的说明）。
func (q *QuectelCommand) ParseNRSignal(line string) (rsrp, rsrq, sinr int, ok bool) {
	// 移远 5G NR servingcell 格式（详见 module/kernel/cell.go 的 parseServingCell）：
	//   +QENG: "servingcell","NOCONN","NR","TDD",460,11,400E21E,155,2452,5,30,-95,-12,14,...
	//   +QENG: "servingcell","NR","FDD",460,00,1051E8F2,123,0,-95,-12,14,35
	return common.ParseServingNRSignal(line)
}

// 编译期断言：移远声明了哪些可选能力（别的厂商不受影响）。
var (
	_ iface.CommandCandidates  = (*QuectelCommand)(nil)
	_ iface.NetLocker          = (*QuectelCommand)(nil)
	_ iface.USBnetConfigurator = (*QuectelCommand)(nil)
	_ iface.AMBRQuerier        = (*QuectelCommand)(nil)
	_ iface.TempQuerier        = (*QuectelCommand)(nil)
	_ iface.NSAIndependent     = (*QuectelCommand)(nil)
)
