package openluat

import (
	"modemd/module/modedialect"
)

import ()

// Mode openluat Air724UG：AT+CTEC=<mode>,<mode>（QModem vendor/openluat.sh，两个参数写同一个值）
//
// 0 自动（2G+4G）、2 仅 2G、4 仅 4G —— 该模组是 4G Cat.1，没有 3G/5G 档，
// 所以下拉只给"自动"和"仅 4G"（要 2G 可在「接口调试」页发 AT+CTEC=2,2）。
func Mode() *modedialect.Dialect {
	return modedialect.ModeCodesDialect("AT+CTEC?", "AT+CTEC=?", "+CTEC:",
		"openluat Air724UG AT+CTEC=<mode>,<mode>（QModem vendor/openluat.sh：0 自动、2 仅2G、4 仅4G）。"+
			"该模组没有 3G/5G，所以只有这两档。",
		map[string]string{modedialect.ModeAuto: "0", modedialect.ModeLTE: "4"},
		map[string]string{"0": modedialect.ModeAuto, "4": modedialect.ModeLTE},
		modedialect.FirstCSVField("+CTEC:"),
		func(code string) string { return "AT+CTEC=" + code + "," + code })
}
