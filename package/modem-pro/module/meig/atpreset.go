package meig

import (
	"modemd/module/atpreset"
)

// ====================== 美格智能 MeiG ======================
// MeigCommand 直接复用移远那一套（美格的 meig-cm 就是 quectel-CM 的分支），
// 所以清单与移远同源，但额外把候选里用到的那两条通用写法列出来。
func ATPresets() []atpreset.ATPreset {
	return []atpreset.ATPreset{
		atpreset.Of("美格（本模块）", "ATI", "模组型号（美格支持 ATI）"),
		atpreset.Of("美格（本模块）", "AT+QNWINFO", "网络接入信息"),
		atpreset.Of("美格（本模块）", `AT+QENG="servingcell"`, "服务小区（含 NR 信号）"),
		atpreset.Of("美格（本模块）", `AT+QENG="neighbourcell"`, "邻区列表"),
		atpreset.Of("美格（本模块）", "AT+QCAINFO", "载波聚合分量载波"),
		atpreset.Of("美格（本模块）", "AT+QCSQ", "信号质量（部分固件用它替代 QENG）"),
		atpreset.Of("美格（本模块）", "AT+CPSI?", "服务小区（部分固件用它替代 QENG）"),
	}
}
