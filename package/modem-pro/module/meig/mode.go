package meig

import (
	"modemd/module/modedialect"
)

import ()

// Mode 美格：AT^SYSCFGEX="<acqorder>",all,0,2,all,all,all,all,1
//
// 码表（QModem vendor/meig.sh 的 get/set_network_prefer）：`00` 自动、`02` 仅 3G、`03` 仅 4G、
// `04` 仅 5G；组合**串接**（脚本就是按 5G→4G→3G 顺序拼的）：`0403` 4G+5G、`0402` 3G+5G、`0302` 3G+4G。
// 注意 5G 的码与华为不同（美格 `04`、华为 `08`）。
func Mode() *modedialect.Dialect {
	d := modedialect.ModeCodesDialect(`AT^SYSCFGEX?`, `AT^SYSCFGEX=?`, "^SYSCFGEX:",
		"美格 AT^SYSCFGEX（QModem vendor/meig.sh：00 自动、02 仅3G、03 仅4G、04 仅5G，组合串接）。"+
			"写入是**回读整串后只换第 1 个字段**，其余参数原样保留。",
		map[string]string{
			modedialect.ModeAuto: "00", modedialect.ModeWCDMA: "02", modedialect.ModeLTE: "03", modedialect.ModeNR5G: "04",
			modedialect.ModeWCDMALTE: "0302", modedialect.ModeWCDMANR: "0402", modedialect.ModeLTENR5G: "0403",
		},
		map[string]string{
			"00": modedialect.ModeAuto, "02": modedialect.ModeWCDMA, "03": modedialect.ModeLTE, "04": modedialect.ModeNR5G,
			"0302": modedialect.ModeWCDMALTE, "0402": modedialect.ModeWCDMANR, "0403": modedialect.ModeLTENR5G,
			"040302": modedialect.ModeAuto,
		},
		modedialect.FirstCSVField("^SYSCFGEX:"),
		// 主写法 = **上游 QModem cmds/meig.sh 的固定模板**（上游为准）
		func(code string) string { return `AT^SYSCFGEX="` + code + `",all,0,2,all,all,all,all,1` })
	d.RawMatch = "^SYSCFGEX:"
	d.Alt = func(v, rawLine string) string {
		return modedialect.RebuildSetFromQuery(`AT^SYSCFGEX="`, rawLine, d.CodeFor(v))
	}
	return d
}
