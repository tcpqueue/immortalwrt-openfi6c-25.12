// Package meig 美格智能（MeiG / MeigLink）。
//
// 这是**跨包嵌入**的样板：美格的指令面直接复用移远那份，
// 所以本包只写"与移远不同"的极少几个地方。
package meig

import (
	"strings"

	"modemd/iface"
	"modemd/module/quectel"
)

// MeigCommand 美格模组（ATI 含 "MEIG"/"MeigLink"，或型号是 SRM8xx / SLM7xx）。
//
// 指令面直接**复用移远那份**：美格自己的 meig-cm 就是 quectel-CM 的分支
// （源码里 MPQMUX.c / QMIThread.c / QmiWwanCM.c 与移远同名同源，见 third_party/meig-cm），
// 其 AT 面也走同一套 Qualcomm 风格命令（AT+QENG / AT+QNWINFO / AT+QCAINFO 这种），
// 而且是**候选式**探测（QuectelCommand 的 ServingCellCandidates 里带了 AT+QCSQ / AT+CPSI
// 这类通用写法回退），所以换成"某型号不认"时不会一发就死。
// 只有厂商识别必须独立（否则 ATI 里有 MEIG 也会被当成移远）。
//
// ⚠️ 嵌入是**编译期**决定、没有虚函数：忘了覆盖不会报错，只会静默用父类的行为。
// 历史事故就是这一类（见 module/kernel/atcmd_std.go 顶部注释）：
// 静默继承 `AT+QNWCFG="nr5g_ambr"`（美格不认）、静默继承 `AT+QGDCNT=1`（美格是速率式）。
type MeigCommand struct{ quectel.QuectelCommand }

var _ iface.ModemCommand = (*MeigCommand)(nil)

func (m *MeigCommand) DetectVendor(resp []string) iface.ModemVendor {
	for _, s := range resp {
		upper := strings.ToUpper(s)
		if strings.Contains(upper, "MEIG") || strings.Contains(upper, "SRM8") || strings.Contains(upper, "SLM7") {
			return iface.VendorMeig
		}
	}
	return iface.VendorUnknown
}

// TrafficCandidates 流量统计：美格自己的 `AT^DSFLOWQRY`（QModem cmds/meig.sh 的 cmd_dsflowqry），
// 应答是 `^DSFLOWRPT: <上行 B/s>,<下行 B/s>,…` 的**瞬时速率** ⇒ 走速率式（本地积分，界面标「（估算）」）。
// 移远的 `AT+QGDCNT?` 排在后面兜底（部分美格固件的 AT 面就是移远那套）。
// ⚠️ 不能直接用嵌进来的 QuectelCommand.TrafficCandidates（那就是 AT+QGDCNT?）。
func (m *MeigCommand) TrafficCandidates() []string {
	return []string{"AT^DSFLOWQRY", "AT+QGDCNT?"}
}

// TrafficEnableCommand 美格主用的是**速率式**（模组自己报速率），没有"开始计数"这一步；
// 嵌进来的 QuectelCommand 会回 AT+QGDCNT=1（那是计数式的使能指令），必须覆盖掉。
func (m *MeigCommand) TrafficEnableCommand() string { return "" }
