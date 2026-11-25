package simcom

import (
	"strconv"
	"strings"

	"modemd/module/common"
	"modemd/module/usbdialect"
)

// USBQualcomm 芯讯通（高通平台）：AT+CUSBCFG? 的 `USBID: 0X1E0E,0X<xxxx>`
func USBQualcomm() *usbdialect.USBDialect {
	return &usbdialect.USBDialect{
		Query: "AT+CUSBCFG?",
		Set:   func(v string) string { return "AT+CUSBCFG=usbid,1e0e," + v },
		Parse: func(resp []string) (string, bool) {
			line, ok := common.FirstLineWith(resp, "USBID:")
			if !ok {
				return "", false
			}
			// USBID: 0X1E0E,0X9011 → 取第 2 段的十六进制
			if i := strings.LastIndex(line, ","); i >= 0 {
				h := strings.ToUpper(common.CleanHexField(line[i+1:]))
				if h != "" {
					return h, true
				}
			}
			return "", false
		},
		Modes: []usbdialect.Mode{
			{Value: "9001", Name: "QMI", Note: "高通原生协议（配 qmi_wwan 驱动）"},
			{Value: "9011", Name: "RNDIS", Note: "经典 USB 网卡"},
		},
		Note: "芯讯通（高通平台）用 AT+CUSBCFG 改 USB 产品 ID（9001=QMI、9011=RNDIS，QModem vendor/simcom.sh）；" +
			"改完需重启模组生效。若模组是 PCIe EP 模式（AT+CPCIEMODE? 回 EP），固件会自己把 902B 当 QMI 处理。",
	}
}

// USBLegacy 芯讯通（lte 平台）：AT$MYCONFIG? 里的 USBNETMODE 字段（0=rndis / 1=ecm / 2=auto）
func USBLegacy() *usbdialect.USBDialect {
	return &usbdialect.USBDialect{
		Query: `AT$MYCONFIG?`,
		Set:   func(v string) string { return `AT$MYCONFIG="USBNETMODE",` + v + `,1` },
		Parse: func(resp []string) (string, bool) {
			line, ok := common.FirstLineWith(resp, "MYCONFIG:")
			if !ok {
				return "", false
			}
			// MYCONFIG: <x>,<USBNETMODE>,<y> → 第 2 个逗号字段
			f := common.SplitCSVFields(line[strings.Index(strings.ToUpper(line), "MYCONFIG:")+len("MYCONFIG:"):])
			if len(f) >= 2 {
				if n, ok := common.ParseATInt(f[1]); ok {
					return strconv.Itoa(n), true
				}
			}
			return "", false
		},
		Modes: []usbdialect.Mode{
			{Value: "1", Name: "ECM", Note: "USB 以太网（Linux 免驱常用）"},
			{Value: "0", Name: "RNDIS", Note: "经典 USB 网卡"},
			{Value: "2", Name: "AUTO", Note: "固件自动选择"},
		},
		Note: "芯讯通（lte 平台）用 AT$MYCONFIG=\"USBNETMODE\",<0|1|2>,1（QModem vendor/simcom.sh）；" +
			"改完需重启模组生效。",
	}
}
