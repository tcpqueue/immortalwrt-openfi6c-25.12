package foxconn

import (
	"strings"

	"modemd/module/imeidialect"
	"modemd/module/usbdialect"
)

// USB Foxconn：AT[!^]USBSWITCH（QModem vendor/foxconn.sh：9025=RMNet、90D5=MBIM）
func USB() *usbdialect.USBDialect {
	return &usbdialect.USBDialect{
		Query: "AT!USBSWITCH?",
		Set:   func(v string) string { return "AT!USBSWITCH=" + v },
		Parse: func(resp []string) (string, bool) {
			// 应答里带 4 位十六进制（9025 / 90D5）
			for _, line := range resp {
				if m := imeidialect.ReHex4.FindStringSubmatch(line); m != nil {
					return strings.ToLower(m[1]), true
				}
			}
			return "", false
		},
		Modes: []usbdialect.Mode{
			{Value: "90d5", Name: "MBIM", Note: "微软标准"},
			{Value: "9025", Name: "RMNet", Note: "高通 RMNet（配 gobinet/qmi_wwan）"},
		},
		Note: "Foxconn 用 AT+USBSWITCH=<4 位十六进制>（QModem vendor/foxconn.sh：9025=RMNet、90D5=MBIM）。" +
			"⚠️ 该脚本的 AT 前缀随机型是 `AT!` 或 `AT^`；本实现先发 `AT!USBSWITCH?`，被拒时自动改试 `AT^`。",
	}
}
