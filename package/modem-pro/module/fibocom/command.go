// Package fibocom 广和通（Fibocom）。
//
// 与移远的差异**只有三处**（服务小区 / 邻区 / 接入信息），
// 所以本包嵌 quectel 的 std + QNW 锁网，只覆盖差异。
package fibocom

import (
	"strings"

	"modemd/iface"
	"modemd/module/common"
	"modemd/module/quectel"
)

// FibocomCommand 广和通。嵌入 common.StdCommands 与 quectel.QnwLockCommands；
// 私有部分只有服务小区/邻区/信号（FENG）与接入信息（QNWINFO）—— 实测与移远**仅此三处不同**。
type FibocomCommand struct {
	common.StdCommands
	quectel.QnwLockCommands
}

var _ iface.ModemCommand = (*FibocomCommand)(nil)

func (f *FibocomCommand) DetectVendor(resp []string) iface.ModemVendor {
	for _, s := range resp {
		upper := strings.ToUpper(s)
		if strings.Contains(upper, "FIBOCOM") || strings.Contains(upper, "FM650") {
			return iface.VendorFibocom
		}
	}
	return iface.VendorUnknown
}

func (f *FibocomCommand) CommandNRServingCell() string { return `AT+FENG="servingcell"` }
func (f *FibocomCommand) CommandNeighbourCell() string { return `AT+FENG="neighbourcell"` }
func (f *FibocomCommand) CommandServingCell() string   { return `AT+FENG="servingcell"` }
func (f *FibocomCommand) CommandNetInfo() string       { return "AT+QNWINFO" }

// NSABandKey 广和通**高通平台**（FM150/FM160-EAU 等）与移远同一条 QNW 指令族，
// 同样有独立的 `AT+QNWPREFCFG="nsa_nr5g_band"` ⇒ NR 的 NSA / SA 可分别锁。
//
// ⚠️ 非高通平台的广和通不走这条路径：`fibocomMTKCommand`（GT 方言，AT+GTACT）
// 嵌了本类型但**覆盖成本空串**；FM650-CN（展锐）由探测出的展锐方言接管。
func (f *FibocomCommand) NSABandKey() string { return "nsa_nr5g_band" }

// ServingCellCandidates / NeighbourCellCandidates 广和通两套方言：
//
//	FM150 / FM160（高通平台）: AT+FENG="servingcell"，格式同移远 +QENG；
//	FM650-CN（展锐平台）     : **不认 FENG**（回 ERROR），只认自家 AT+GTCCINFO?，
//	                           它一次就把服务小区 + 全部邻区 + 信号一起报回来。
//
// 所以按顺序探测：老机型命中第一条、FM650 落到第二条，两边都不用改解析。
func (f *FibocomCommand) ServingCellCandidates() []string {
	return []string{`AT+FENG="servingcell"`, common.CmdGTCCINFO}
}

func (f *FibocomCommand) NeighbourCellCandidates() []string {
	return []string{`AT+FENG="neighbourcell"`, common.CmdGTCCINFO}
}

// TrafficCandidates 广和通数据流量统计：只有 AT+GTSTATIS?。
//
// 形状与移远不同：它回的是**瞬时速率**（`+GTSTATIS: <下载 B/s>,<上传 B/s>,..`），
// 没有累计计数器 —— 所以解析走"速率式"（本地积分估算累计），
// 而 TrafficEnableCommand 返回空串（没有"开始计数"这一步要发）。
// 老机型（FM150 一类）若不认这条指令，候选探测会直接把它停用，接口照旧报"未上报"。
func (f *FibocomCommand) TrafficCandidates() []string { return []string{common.CmdGTSTATIS} }

// TrafficEnableCommand 见 TrafficCandidates：速率式模组没有使能指令。
func (f *FibocomCommand) TrafficEnableCommand() string { return "" }

// CommandCAInfo 广和通未验证 AT+QCAINFO，宁可不报也不发未经验证的指令。
func (f *FibocomCommand) CommandCAInfo() string { return "" }

// ParseNRSignal 广和通 5G NR 返回格式与移远类似，仅指令头为 +FENG。
func (f *FibocomCommand) ParseNRSignal(line string) (rsrp, rsrq, sinr int, ok bool) {
	return common.ParseServingNRSignal(line)
}

// 编译期断言：广和通声明了哪些可选能力。
var (
	_ iface.CommandCandidates = (*FibocomCommand)(nil)
	_ iface.NetLocker         = (*FibocomCommand)(nil)
)
