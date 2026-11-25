package gosuncn

import (
	"strings"

	"modemd/iface"
	"modemd/module/common"
	"modemd/module/usbdialect"
)

// USB 高新兴：AT+ZSWITCH? / AT+ZSWITCH=<c>（QModem cmds/gosuncn.sh 注释里的取值表）
func USB(model string) *usbdialect.USBDialect {
	d := &usbdialect.USBDialect{
		Query: "AT+ZSWITCH?",
		Set:   func(v string) string { return "AT+ZSWITCH=" + v },
		Parse: func(resp []string) (string, bool) {
			line, ok := common.FirstLineWith(resp, "+ZSWITCH:")
			if !ok {
				return "", false
			}
			f := common.SplitCSVFields(line[strings.Index(strings.ToUpper(line), "+ZSWITCH:")+len("+ZSWITCH:"):])
			if len(f) == 0 {
				return "", false
			}
			c := strings.TrimSpace(f[0])
			if c == "" {
				return "", false
			}
			return strings.ToLower(c), true
		},
		Modes: []usbdialect.Mode{
			{Value: "8", Name: "MBIM", Note: "微软标准"},
			{Value: "e", Name: "ECM", Note: "USB 以太网（各平台含义可能不同，见说明）"},
			{Value: "r", Name: "RNDIS", Note: "经典 USB 网卡"},
			{Value: "x", Name: "QMI", Note: "高通原生协议（q/n 也归到这里）"},
			{Value: "p", Name: "EAP", Note: "EAP 模式"},
		},
		Note: "高新兴 AT+ZSWITCH=<值>（QModem cmds/gosuncn.sh 注释：8=MBIM、e=ECM、r=RNDIS、x/q/n=QMI、p=EAP）。" +
			"⚠️ 该脚本按平台微调过字符含义（如 lte 平台的 e 被读成 MBIM），所以这里**以设备回读的字符为准**：" +
			"改完请回读确认。",
	}
	// 平台提示（高通 / 其它平台）来自变体表，口径与锁网、组网模式一致
	if s := usbdialect.VariantNote(iface.VendorUnknown, model); s != "" {
		d.Note += s
	}
	return d
}
