package gosuncn

import (
	"modemd/module/modedialect"
)

import ()

// Mode 高新兴：AT+ZSNT=<cm_mode>,<roam>,<srvdomain>（QModem vendor/gosuncn.sh 的 set_network_prefer）
//
//	高通平台：0 自动、2 仅3G、6 仅4G、7 仅5G、8 4G+5G
//	LTE 平台：只有 0 自动、2 仅3G、6 仅4G（脚本里 3G/4G 之外的选择一律回落 "0,0,0"）
func Mode(qualcomm bool) *modedialect.Dialect {
	codes := map[string]string{modedialect.ModeAuto: "0", modedialect.ModeWCDMA: "2", modedialect.ModeLTE: "6"}
	names := map[string]string{"0": modedialect.ModeAuto, "2": modedialect.ModeWCDMA, "6": modedialect.ModeLTE}
	note := "高新兴 AT+ZSNT=<cm_mode>,0,0（QModem vendor/gosuncn.sh：0 自动、2 仅3G、6 仅4G）。"
	if qualcomm {
		codes[modedialect.ModeNR5G] = "7"
		codes[modedialect.ModeLTENR5G] = "8"
		names["7"] = modedialect.ModeNR5G
		names["8"] = modedialect.ModeLTENR5G
		note = "高新兴 AT+ZSNT=<cm_mode>,0,0（QModem vendor/gosuncn.sh 高通平台：0 自动、2 仅3G、6 仅4G、7 仅5G、8 4G+5G）。"
	}
	return modedialect.ModeCodesDialect("AT+ZSNT?", "AT+ZSNT=?", "+ZSNT:",
		note, codes, names,
		modedialect.FirstCSVField("+ZSNT:"),
		func(code string) string { return "AT+ZSNT=" + code + ",0,0" })
}
