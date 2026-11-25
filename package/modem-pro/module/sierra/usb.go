package sierra

import (
	"modemd/module/common"
	"modemd/module/usbdialect"
	"strings"
)

// USB Sierra：AT!USBCOMP?（看 Interface bitmask）→ AT!USBCOMP=1,4,<mask>
func USB() *usbdialect.USBDialect {
	return &usbdialect.USBDialect{
		Query: "AT!USBCOMP?",
		Set:   func(v string) string { return "AT!USBCOMP=1,4," + v },
		Parse: func(resp []string) (string, bool) {
			line, ok := common.FirstLineWith(resp, "Interface bitmask:")
			if !ok {
				return "", false
			}
			mask := strings.ToUpper(common.CleanHexField(line[strings.Index(strings.ToUpper(line), "INTERFACE BITMASK:")+len("Interface bitmask:"):]))
			switch {
			case strings.HasSuffix(mask, "1009"):
				return "0x00001009", true
			case strings.HasSuffix(mask, "109"):
				return "0x00000109", true
			}
			return "", false
		},
		Modes: []usbdialect.Mode{
			{Value: "0x00001009", Name: "MBIM", Note: "微软标准（Windows 推荐）"},
			{Value: "0x00000109", Name: "RMNet", Note: "高通 RMNet"},
		},
		// Sierra 的高级指令（AT!USBCOMP）要先解锁：QModem cmds/sierra.sh 的
		// `cmd_entercnd` 就是它，vendor/sierra.sh 用的默认密码是 A710。
		// 解锁失败（有的固件不需要、或密码不同）不影响后面照样试 —— 所以不报错。
		Pre: []string{`AT!ENTERCND="A710"`},
		Note: "Sierra 用 AT!USBCOMP=1,4,<接口位图>（QModem vendor/sierra.sh：MBIM=0x00001009、RMNet=0x00000109）；" +
			"⚠️ 发之前会先 `AT!ENTERCND=\"A710\"` 解锁高级指令（QModem cmds/sierra.sh 的 cmd_entercnd），" +
			"否则部分固件对 AT!USBCOMP 直接回 ERROR；改完需重启模组生效。",
	}
}
