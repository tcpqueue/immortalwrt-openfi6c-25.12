package foxconn

import (
	"modemd/module/modedialect"
)

import ()

// Mode Foxconn：AT!SLMODE=1,<n>（QModem vendor/foxconn.sh 把 "1n" 拆成 "1,n" 下发）
//
// 0 自动、1 仅 3G、2 仅 4G、3 3G+4G、4 仅 5G、5 3G+5G、6 4G+5G、7 3G+4G+5G。
// ⚠️ 少数机型 AT 前缀是 `AT^` 而不是 `AT!`（QModem 用 `$at_pre` 变量）；读不到时可在「接口调试」页核对。
func Mode() *modedialect.Dialect {
	return modedialect.ModeCodesDialect("AT!SLMODE?", "AT!SLMODE=?", "SLMODE:",
		"Foxconn AT!SLMODE=1,<n>（QModem vendor/foxconn.sh：0 自动、1 仅3G、2 仅4G、3 3G+4G、4 仅5G、5 3G+5G、6 4G+5G、7 三档）。",
		map[string]string{
			modedialect.ModeAuto: "1,0", modedialect.ModeWCDMA: "1,1", modedialect.ModeLTE: "1,2", modedialect.ModeNR5G: "1,4",
			modedialect.ModeWCDMALTE: "1,3", modedialect.ModeWCDMANR: "1,5", modedialect.ModeLTENR5G: "1,6",
		},
		map[string]string{
			"1,0": modedialect.ModeAuto, "1,1": modedialect.ModeWCDMA, "1,2": modedialect.ModeLTE, "1,3": modedialect.ModeWCDMALTE,
			"1,4": modedialect.ModeNR5G, "1,5": modedialect.ModeWCDMANR, "1,6": modedialect.ModeLTENR5G, "1,7": modedialect.ModeAuto,
		},
		func(resp []string) (string, bool) {
			v, ok := modedialect.NthCSVField("SLMODE:", 1)(resp)
			if !ok {
				return "", false
			}
			return "1," + v, true
		},
		func(code string) string { return "AT!SLMODE=" + code })
}
