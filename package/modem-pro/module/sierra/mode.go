package sierra

import (
	"modemd/module/common"
	"modemd/module/modedialect"
	"strings"
)

// Mode Sierra：AT!SELRAT? / AT!SELRAT=<码>（QModem vendor/sierra.sh 的码表）
func Mode() *modedialect.Dialect {
	return &modedialect.Dialect{
		Query: "AT!SELRAT?",
		Set: func(v string) string {
			code := ModeCode(v)
			if code == "" {
				return ""
			}
			return "AT!SELRAT=" + code
		},
		Parse: func(resp []string) (string, bool) {
			line, ok := common.FirstLineWith(resp, "!SELRAT:")
			if !ok {
				return "", false
			}
			f := common.SplitCSVFields(line[strings.Index(strings.ToUpper(line), "!SELRAT:")+len("!SELRAT:"):])
			if len(f) == 0 {
				return "", false
			}
			code := strings.TrimSpace(f[0])
			return ModeName(code), code != ""
		},
		Choices:  modedialect.ModeChoiceList(modedialect.ModeAuto, modedialect.ModeWCDMA, modedialect.ModeLTE, modedialect.ModeNR5G, modedialect.ModeWCDMALTE, modedialect.ModeWCDMANR, modedialect.ModeLTENR5G),
		Enum:     "AT!SELRAT=?",
		CodeName: ModeName,
		Note: "Sierra 用 AT!SELRAT=<码>（QModem vendor/sierra.sh：00=自动 01=仅3G 06=仅4G 20=仅5G " +
			"11=3G+4G 21=4G+5G 22=3G+5G）。",
	}
}

func ModeCode(v string) string {
	switch strings.ToUpper(v) {
	case modedialect.ModeAuto:
		return "00"
	case modedialect.ModeWCDMA:
		return "01"
	case modedialect.ModeLTE:
		return "06"
	case modedialect.ModeNR5G:
		return "20"
	case modedialect.ModeWCDMALTE:
		return "11"
	case modedialect.ModeLTENR5G:
		return "21"
	case modedialect.ModeWCDMANR:
		return "22"
	}
	return ""
}

func ModeName(code string) string {
	switch strings.TrimSpace(code) {
	case "00":
		return modedialect.ModeAuto
	case "01":
		return modedialect.ModeWCDMA
	case "06":
		return modedialect.ModeLTE
	case "20":
		return modedialect.ModeNR5G
	case "11":
		return modedialect.ModeWCDMALTE
	case "21":
		return modedialect.ModeLTENR5G
	case "22":
		return modedialect.ModeWCDMANR
	}
	return strings.TrimSpace(code)
}
