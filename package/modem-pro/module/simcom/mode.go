package simcom

import (
	"modemd/module/modedialect"
)

import ()

// Mode 芯讯通：AT+CNMP（QModem vendor/simcom.sh 的 get/set_network_prefer_nr 码表，逐行核对）
//
//	2=自动/全部、14=仅3G、38=仅4G、71=仅5G、54=3G+4G、55=3G+5G、109=4G+5G
//
// ⚠️ 以前只接了 2/38/71 三档（**组合档位没接**），用户在 5G 芯讯通模组上想选"4G+5G"就是
// "本机不支持" —— 而 QModem 脚本里这三个组合码（54/55/109）写得明明白白（set_network_prefer_nr
// 的两个/三个制式分支），读侧同一张表（2/54/55 含 3G、38/54/109 含 4G、71/55/109 含 5G）。
func Mode() *modedialect.Dialect {
	return modedialect.ModeCodesDialect("AT+CNMP?", "AT+CNMP=?", "+CNMP:",
		"芯讯通 AT+CNMP=<码>（QModem vendor/simcom.sh：2=自动、14=仅3G、38=仅4G、71=仅5G、"+
			"54=3G+4G、55=3G+5G、109=4G+5G）。",
		map[string]string{
			modedialect.ModeAuto: "2", modedialect.ModeWCDMA: "14", modedialect.ModeLTE: "38", modedialect.ModeNR5G: "71",
			modedialect.ModeWCDMALTE: "54", modedialect.ModeWCDMANR: "55", modedialect.ModeLTENR5G: "109",
		},
		map[string]string{
			"2": modedialect.ModeAuto, "14": modedialect.ModeWCDMA, "38": modedialect.ModeLTE, "71": modedialect.ModeNR5G,
			"54": modedialect.ModeWCDMALTE, "55": modedialect.ModeWCDMANR, "109": modedialect.ModeLTENR5G,
		},
		modedialect.FirstCSVField("+CNMP:"),
		func(code string) string { return "AT+CNMP=" + code })
}
