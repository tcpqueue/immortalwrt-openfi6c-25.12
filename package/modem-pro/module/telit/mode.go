package telit

import (
	"modemd/module/modedialect"
)

import ()

// Mode Telit：AT+WS46=<n>（QModem vendor/telit.sh 的 get/set_network_prefer）
//
// 22 仅3G、28 仅4G、36 仅5G、31 3G+4G、40 3G+5G、37 4G+5G、38 全部。
func Mode() *modedialect.Dialect {
	return modedialect.ModeCodesDialect("AT+WS46?", "AT+WS46=?", "+WS46:",
		"Telit AT+WS46=<n>（QModem vendor/telit.sh：38 全部、22 仅3G、28 仅4G、36 仅5G、31 3G+4G、40 3G+5G、37 4G+5G）。",
		map[string]string{
			modedialect.ModeAuto: "38", modedialect.ModeWCDMA: "22", modedialect.ModeLTE: "28", modedialect.ModeNR5G: "36",
			modedialect.ModeWCDMALTE: "31", modedialect.ModeWCDMANR: "40", modedialect.ModeLTENR5G: "37",
		},
		map[string]string{
			"38": modedialect.ModeAuto, "22": modedialect.ModeWCDMA, "28": modedialect.ModeLTE, "36": modedialect.ModeNR5G,
			"31": modedialect.ModeWCDMALTE, "40": modedialect.ModeWCDMANR, "37": modedialect.ModeLTENR5G,
		},
		modedialect.FirstCSVField("+WS46:"),
		func(code string) string { return "AT+WS46=" + code })
}
