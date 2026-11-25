package huawei

import (
	"modemd/module/modedialect"
)

import ()

// Mode 华为 / 巴龙：AT^SYSCFGEX="<acqorder>",<band>,<roam>,<srvdomain>,…
//
// 它是**复合**指令：制式码在第 1 个字段，后面还跟着频段/漫游等参数 ——
// 所以写的时候**先回读当前整串、只替换第 1 个字段**，其余原样保留（不会顺手把频段改掉）。
//
// acqorder 码表（QModem vendor/huawei.sh 的 get/set_network_prefer，逐行核对）：
// `00` 自动、`02` 仅 3G、`03` 仅 4G、`08` 仅 5G；组合是**串接**（新制式在前）——
// 脚本里三档写死 `080302`（5G+4G+3G），两档按同一规则拼：`0803` 4G+5G、`0802` 3G+5G、`0302` 3G+4G。
func Mode() *modedialect.Dialect {
	d := modedialect.ModeCodesDialect(`AT^SYSCFGEX?`, `AT^SYSCFGEX=?`, "^SYSCFGEX:",
		"华为/巴龙 AT^SYSCFGEX（QModem vendor/huawei.sh：00 自动、02 仅3G、03 仅4G、08 仅5G，"+
			"组合串接，如 0803=4G+5G）。写入是**回读整串后只换第 1 个字段**，后面的频段/漫游参数原样保留。",
		map[string]string{
			modedialect.ModeAuto: "00", modedialect.ModeWCDMA: "02", modedialect.ModeLTE: "03", modedialect.ModeNR5G: "08",
			modedialect.ModeWCDMALTE: "0302", modedialect.ModeWCDMANR: "0802", modedialect.ModeLTENR5G: "0803",
		},
		map[string]string{
			"00": modedialect.ModeAuto, "02": modedialect.ModeWCDMA, "03": modedialect.ModeLTE, "08": modedialect.ModeNR5G,
			"0302": modedialect.ModeWCDMALTE, "0802": modedialect.ModeWCDMANR, "0803": modedialect.ModeLTENR5G,
			"080302": modedialect.ModeAuto, // 脚本里的三档写法（5G+4G+3G）
		},
		modedialect.FirstCSVField("^SYSCFGEX:"),
		// 主写法 = **上游 QModem cmds/huawei.sh 的固定模板**（上游为准）
		func(code string) string { return `AT^SYSCFGEX="` + code + `",40000000,1,2,40000000,,` })
	d.RawMatch = "^SYSCFGEX:"
	// 备用写法 = 回读整串只换第 1 个字段（保留用户当前的频段/漫游参数，
	// 免得固定模板里的 40000000 把频段配置改掉）；只有固定模板被拒时才用。
	d.Alt = func(v, rawLine string) string {
		return modedialect.RebuildSetFromQuery(`AT^SYSCFGEX="`, rawLine, d.CodeFor(v))
	}
	return d
}
