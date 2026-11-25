package common

import "fmt"

// AdaptiveCommands 自适应指令集（未识别型号的底座）。
//
// 标准 3GPP 指令（CSQ/CREG/CGSN/CMGF/CMGL/CMGS/CMGD/CPMS/COPS）各家都通，直接用；
// 厂商扩展指令（NR 信号 / 服务小区 / 邻区 / CA）不同型号甚至不同固件写法不同，
// 因此这里给出**多套候选**，由引擎在运行期试探出可用的那一条并记住。
//
// 解析端同时兼容多种上报格式（+QENG / +FENG / +CPSI / +QCSQ …）。
//
// 谁在用它：`unknownCommand`（未识别厂商兜底）、`UnisocCommand`、`MT5700Command`。
type AdaptiveCommands struct{ StdCommands }

var (
	_ interface{ CommandNRServingCell() string } = (*AdaptiveCommands)(nil)
)

func (a *AdaptiveCommands) CommandNRServingCell() string { return CmdQENGServing }
func (a *AdaptiveCommands) CommandNeighbourCell() string { return `AT+QENG="neighbourcell"` }
func (a *AdaptiveCommands) CommandServingCell() string   { return CmdQENGServing }
func (a *AdaptiveCommands) CommandNetInfo() string       { return "AT+QNWINFO" }

// ServingCellCandidates 服务小区查询候选：按"最可能支持"的顺序试探。
// QENG 在多数 5G 模组上通用，CPSI/QCSQ 是另一批固件的常见写法；
// 最后补上华为/巴龙系的 `AT^MONSC` —— 它的解析器本来就在扫描链路里，
// 只是以前没进候选表，于是**未识别厂商的华为机型**（如 S10）永远扫不到服务小区。
func (a *AdaptiveCommands) ServingCellCandidates() []string {
	return []string{CmdQENGServing, "AT+CPSI?", "AT+QCSQ", "AT^MONSC"}
}

// NeighbourCellCandidates 邻区查询候选（不支持时不影响其他功能）。
// `AT^MONNC` 同理：解析已经在扫描链路里。
func (a *AdaptiveCommands) NeighbourCellCandidates() []string {
	return []string{`AT+QENG="neighbourcell"`, "AT^MONNC"}
}

// TrafficCandidates 流量计数器：移远/展锐系常见 AT+QGDCNT；不支持时会被探测结果直接停用
func (a *AdaptiveCommands) TrafficCandidates() []string { return []string{"AT+QGDCNT?"} }

// TrafficEnableCommand 计数器使能指令
func (a *AdaptiveCommands) TrafficEnableCommand() string { return "AT+QGDCNT=1" }

// CommandQueryUSBnet / CommandSetUSBnet QCFG 系指令在移远/展锐系固件上很常见，
// 自适应指令集也声明它；不支持时查询会报 ERROR，前端会显示"未上报"。
func (a *AdaptiveCommands) CommandQueryUSBnet() string { return `AT+QCFG="usbnet"` }

func (a *AdaptiveCommands) CommandSetUSBnet(value string) string {
	return fmt.Sprintf(`AT+QCFG="usbnet",%s`, value)
}

// ParseNRSignal 宽容解析（策略在 kernel 的解析层，见 hooks.go 的 ParseLooseNRSignal）。
func (a *AdaptiveCommands) ParseNRSignal(line string) (rsrp, rsrq, sinr int, ok bool) {
	return ParseLooseNRSignal(line)
}
