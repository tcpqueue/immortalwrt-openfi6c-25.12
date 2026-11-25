package neoway

import (
	"strconv"
	"strings"

	"modemd/module/common"
	"modemd/module/modedialect"
)

// Mode 有方：AT$MYSYSINFO=<码>（QModem vendor/neoway.sh 的取值表，上游为准）
//
// 写：3=仅3G 4=仅4G **9=仅5G** 7=3G+4G 11=3G+5G 12=4G+5G 14=三档全开 1=AUTO
//
// ⚠️ 上游脚本里的注释写得很清楚（`config_mode="9" # 仅5G`、`config_mode="1" # AUTO`）；
// 而我们以前把「仅 5G」写成 **5**、把「自动」写成 **14** —— 5 是旧码（上游的**读**侧两种都认），
// 但写侧得用 9。
func Mode() *modedialect.Dialect {
	return &modedialect.Dialect{
		Query: `AT$MYSYSINFO`,
		Set: func(v string) string {
			code := ModeCode(v)
			if code == "" {
				return ""
			}
			return "AT$MYSYSINFO=" + code
		},
		Parse: func(resp []string) (string, bool) {
			line, ok := common.FirstLineWith(resp, "$MYSYSINFO:")
			if !ok {
				return "", false
			}
			f := common.SplitCSVFields(line[strings.Index(strings.ToUpper(line), "$MYSYSINFO:")+len("$MYSYSINFO:"):])
			if len(f) == 0 {
				return "", false
			}
			n, err := strconv.Atoi(strings.TrimSpace(f[0]))
			if err != nil {
				return "", false
			}
			return ModeName(n), true
		},
		Choices:  modedialect.ModeChoiceList(modedialect.ModeAuto, modedialect.ModeWCDMA, modedialect.ModeLTE, modedialect.ModeNR5G, modedialect.ModeWCDMALTE, modedialect.ModeWCDMANR, modedialect.ModeLTENR5G),
		Enum:     `AT$MYSYSINFO=?`,
		CodeName: ModeNameStr,
		Note: "有方 AT$MYSYSINFO=<码>（QModem vendor/neoway.sh：3=仅3G 4=仅4G 9=仅5G 7=3G+4G " +
			"11=3G+5G 12=4G+5G 14=三档全开 1=AUTO）。",
	}
}

func ModeCode(v string) string {
	switch strings.ToUpper(v) {
	case modedialect.ModeWCDMA:
		return "3"
	case modedialect.ModeLTE:
		return "4"
	case modedialect.ModeNR5G:
		return "9"
	case modedialect.ModeWCDMALTE:
		return "7"
	case modedialect.ModeWCDMANR:
		return "11"
	case modedialect.ModeLTENR5G:
		return "12"
	case modedialect.ModeAuto:
		return "14"
	}
	return ""
}

func ModeName(n int) string {
	switch n {
	case 3:
		return modedialect.ModeWCDMA
	case 4:
		return modedialect.ModeLTE
	case 5, 9: // 5 是旧码：上游的读侧两种都认
		return modedialect.ModeNR5G
	case 7:
		return modedialect.ModeWCDMALTE
	case 11:
		return modedialect.ModeWCDMANR
	case 12:
		return modedialect.ModeLTENR5G
	case 14:
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
