package thales

import (
	"modemd/module/imeidialect"
	"modemd/module/usbdialect"
)

import ()

// USB Thales：AT+SETCONFIG?（0=MBIM / 1=RmNet，QModem cmds/thales.sh 注释 15.50）
func USB() *usbdialect.USBDialect {
	return &usbdialect.USBDialect{
		Query: "AT+SETCONFIG?",
		Set:   func(v string) string { return "AT+SETCONFIG=" + v },
		Parse: func(resp []string) (string, bool) { return imeidialect.RawValueParser(resp) },
		Modes: []usbdialect.Mode{
			{Value: "1", Name: "RmNet", Note: "高通数据模式（Linux 常用）"},
			{Value: "0", Name: "MBIM", Note: "微软标准"},
		},
		Note: "Thales 用 AT+SETCONFIG=<0|1>（手册 15.50，QModem cmds/thales.sh 注释：0=MBIM、1=RmNet）；" +
			"另有 AT+MODESWITCH 切 USB/PCIe 形态，本实现不动它。",
	}
}
