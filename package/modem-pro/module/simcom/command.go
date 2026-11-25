// Package simcom 芯讯通（SIMCom）。
package simcom

import (
	"strings"

	"modemd/iface"
	"modemd/module/common"
)

// SimcomCommand 芯讯通。嵌入 common.StdCommands；服务小区/信号用自家的 AT+CPSI?。
type SimcomCommand struct{ common.StdCommands }

var _ iface.ModemCommand = (*SimcomCommand)(nil)

func (s *SimcomCommand) DetectVendor(resp []string) iface.ModemVendor {
	for _, line := range resp {
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "SIMCOM") || strings.Contains(upper, "SIM7900") {
			return iface.VendorSimcom
		}
	}
	return iface.VendorUnknown
}

// CommandNRServingCell 芯讯通的服务小区指令是 AT+CPSI?（不是移远的 QENG）
func (s *SimcomCommand) CommandNRServingCell() string { return common.CmdCPSI }
func (s *SimcomCommand) CommandNeighbourCell() string { return `AT+QENG="neighbourcell"` }
func (s *SimcomCommand) CommandServingCell() string   { return common.CmdCPSI }
func (s *SimcomCommand) CommandNetInfo() string       { return "AT+QNWINFO" }

// CommandCAInfo 芯讯通未验证 AT+QCAINFO，宁可不报也不发未经验证的指令。
func (s *SimcomCommand) CommandCAInfo() string { return "" }

// ===== 芯讯通的可选能力（实测 SIM8200EA-M2，2026-09-29）=====
//
// 服务小区 / 信号：
//
//	AT+CPSI? → +CPSI: NR5G_SA,Online,460-11,0xCFA102,55735066633,52,NR5G_BAND78,627264,-1000,-120,25
//
// 邻区：AT+CENG / AT+CNETSCAN 均回 ERROR（该固件不提供邻区查询）；
// 流量计数器：AT+QGDCNT? 回 ERROR，芯讯通也没有等价的标准指令
// → 都声明为"不提供"，不会每轮白发一条无效指令。
func (s *SimcomCommand) ServingCellCandidates() []string {
	// CPSI 优先；部分型号/固件确实复用移远的 QENG 写法，留作第二候选
	return []string{common.CmdCPSI, common.CmdQENGServing}
}

// NeighbourCellCandidates 返回 nil = 没有更可靠的候选，上层退回单条指令（QENG）
func (s *SimcomCommand) NeighbourCellCandidates() []string { return nil }

// TrafficCandidates 返回 nil = 不提供流量计数器（配额/累计流量在该模组上显示不支持）
func (s *SimcomCommand) TrafficCandidates() []string { return nil }

func (s *SimcomCommand) TrafficEnableCommand() string { return "" }

// CommandTemp 芯讯通温度：AT+CPMUTEMP 回一个整机温度（实测 SIM8200EA-M2 → +CPMUTEMP: 25）。
// ⚠️ 注意不是 AT+CTEMP / AT+SIMTEMP / AT+QTEMP —— 那三个在 SIM8200 上都回 ERROR。
func (s *SimcomCommand) CommandTemp() string { return "AT+CPMUTEMP" }

// CommandExtraNetInfo 芯讯通附加网络信息（手册 §4.2.18）：
//
//	AT+CNWINFO? → +CNWINFO: NR5G,4601155735066633,0xCFA11A0,0,UNKOWN,UNKOWN,0,0,100M
//	                       制式  EGCI(PLMN+CellID) eNBID(hex) MCS DL·UL 调制 CQI 上行功率 带宽
func (s *SimcomCommand) CommandExtraNetInfo() string { return "AT+CNWINFO?" }

// CommandVoltage 芯讯通供电电压（实测 +CBC: 3.858V）
func (s *SimcomCommand) CommandVoltage() string { return "AT+CBC" }

// ParseNRSignal +CPSI 走芯讯通自己的解析（含 SIM8200 的"频率名 + ×10 信号值"写法）；
// 复用移远 QENG 写法的型号格式与移远一致。
func (s *SimcomCommand) ParseNRSignal(line string) (rsrp, rsrq, sinr int, ok bool) {
	if strings.Contains(strings.ToUpper(line), "CPSI:") {
		return common.ParseCPSISignal(line)
	}
	return common.ParseServingNRSignal(line)
}

// 编译期断言：芯讯通声明了哪些可选能力。
var (
	_ iface.CommandCandidates   = (*SimcomCommand)(nil)
	_ iface.TempQuerier         = (*SimcomCommand)(nil)
	_ iface.VoltageQuerier      = (*SimcomCommand)(nil)
	_ iface.ExtraNetInfoQuerier = (*SimcomCommand)(nil)
)
