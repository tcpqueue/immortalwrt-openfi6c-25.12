package quectel

import (
	"strconv"
	"strings"

	"modemd/module/common"
	"modemd/module/modedialect"
)

// ModeNR 现代移远：AT+QNWPREFCFG="mode_pref"
func ModePrefNR() *modedialect.Dialect {
	return &modedialect.Dialect{
		Query: `AT+QNWPREFCFG="mode_pref"`,
		Set: func(v string) string {
			return `AT+QNWPREFCFG="mode_pref",` + v
		},
		Parse: func(resp []string) (string, bool) {
			line, ok := common.FirstLineWith(resp, "+QNWPREFCFG:")
			if !ok {
				return "", false
			}
			f := common.SplitCSVFields(line[strings.Index(strings.ToUpper(line), "+QNWPREFCFG:")+len("+QNWPREFCFG:"):])
			if len(f) == 0 {
				return "", false
			}
			raw := strings.ToUpper(strings.TrimSpace(f[len(f)-1]))
			return common.NormalizeModePref(raw), raw != ""
		},
		Choices: modedialect.ModeChoiceList(modedialect.ModeAuto, modedialect.ModeWCDMA, modedialect.ModeLTE, modedialect.ModeNR5G, modedialect.ModeWCDMALTE, modedialect.ModeWCDMANR, modedialect.ModeLTENR5G),
		// `AT+QNWPREFCFG=?` 会把支持的 mode_pref 全列出来（带引号），界面因此能"显示所有模式"
		Enum:     `AT+QNWPREFCFG=?`,
		EnumLine: "mode_pref",
		CodeName: common.NormalizeModePref,
		Note:     "移远 AT+QNWPREFCFG=\"mode_pref\",<取值>（QModem vendor/quectel.sh 的 set_network_prefer_nr）；改完可能短暂断网。",
	}
}

// ModePrefLTE 旧款移远（platform=lte）：AT+QCFG="nwscanmode"（QModem 只用 0=自动(3G+4G)、3=仅4G）
func ModePrefLTE() *modedialect.Dialect {
	return &modedialect.Dialect{
		Query: `AT+QCFG="nwscanmode"`,
		Set: func(v string) string {
			switch v {
			case modedialect.ModeLTE:
				return `AT+QCFG="nwscanmode",3`
			case modedialect.ModeAuto, modedialect.ModeWCDMALTE:
				return `AT+QCFG="nwscanmode",0`
			}
			return ""
		},
		Parse: func(resp []string) (string, bool) {
			line, ok := common.FirstLineWith(resp, `"NWSCANMODE"`)
			if !ok {
				return "", false
			}
			n, ok := common.NumInLine(line, 0)
			if !ok {
				return "", false
			}
			switch n {
			case 0:
				return modedialect.ModeAuto, true
			case 3:
				return modedialect.ModeLTE, true
			}
			return strconv.Itoa(n), true
		},
		Choices: modedialect.ModeChoiceList(modedialect.ModeAuto, modedialect.ModeLTE),
		CodeName: func(code string) string {
			switch strings.TrimSpace(code) {
			case "0":
				return modedialect.ModeAuto
			case "3":
				return modedialect.ModeLTE
			}
			return ""
		},
		Note: "移远旧款 4G 用 AT+QCFG=\"nwscanmode\"（QModem vendor/quectel.sh 的 set_network_prefer_lte：" +
			"自动/3G+4G→0、仅 4G→3）—— 该固件没有 5G，可选值只有这两档。",
	}
}
