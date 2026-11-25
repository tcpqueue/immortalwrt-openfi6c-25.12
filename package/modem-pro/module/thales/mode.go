package thales

import (
	"strconv"
	"strings"

	"modemd/module/common"
	"modemd/module/modedialect"
)

// Mode Thales：AT^SLMODE=1,<n>（QModem cmds/thales.sh 注释 15.24 的取值表）
func Mode() *modedialect.Dialect {
	return &modedialect.Dialect{
		Query: "AT^SLMODE?",
		Set: func(v string) string {
			code := ModeCode(v)
			if code == "" {
				return ""
			}
			return "AT^SLMODE=1," + code
		},
		Parse: func(resp []string) (string, bool) {
			line, ok := common.FirstLineWith(resp, "^SLMODE:")
			if !ok {
				return "", false
			}
			f := common.SplitCSVFields(line[strings.Index(strings.ToUpper(line), "^SLMODE:")+len("^SLMODE:"):])
			if len(f) < 2 {
				return "", false
			}
			n, err := strconv.Atoi(strings.TrimSpace(f[1]))
			if err != nil {
				return "", false
			}
			return ModeName(n), true
		},
		Choices:  modedialect.ModeChoiceList(modedialect.ModeAuto, modedialect.ModeWCDMA, modedialect.ModeLTE, modedialect.ModeNR5G, modedialect.ModeWCDMALTE, modedialect.ModeWCDMANR, modedialect.ModeLTENR5G),
		Enum:     "AT^SLMODE=?",
		CodeName: ModeNameStr,
		Note: "Thales AT^SLMODE=1,<n>（手册 15.24 / QModem cmds/thales.sh 注释：" +
			"0=自动 1=仅3G 2=仅4G 3=3G+4G 4=仅5G 5=3G+5G 6=4G+5G 7=3G+4G+5G）。",
	}
}

func ModeCode(v string) string {
	switch strings.ToUpper(v) {
	case modedialect.ModeAuto:
		return "0"
	case modedialect.ModeWCDMA:
		return "1"
	case modedialect.ModeLTE:
		return "2"
	case modedialect.ModeWCDMALTE:
		return "3"
	case modedialect.ModeNR5G:
		return "4"
	case modedialect.ModeWCDMANR:
		return "5"
	case modedialect.ModeLTENR5G:
		return "6"
	}
	return ""
}

func ModeName(n int) string {
	switch n {
	case 0:
		return modedialect.ModeAuto
	case 1:
		return modedialect.ModeWCDMA
	case 2:
		return modedialect.ModeLTE
	case 3:
		return modedialect.ModeWCDMALTE
	case 4:
		return modedialect.ModeNR5G
	case 5:
		return modedialect.ModeWCDMANR
	case 6:
		return modedialect.ModeLTENR5G
	case 7:
		return modedialect.ModeAuto
	}
	return strconv.Itoa(n)
}

// ModeNameStr 同上，入参是应答里的字符串
func ModeNameStr(code string) string {
	n, err := strconv.Atoi(strings.TrimSpace(code))
	if err != nil {
		return ""
	}
	return ModeName(n)
}
