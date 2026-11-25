package quectel

import (
	"modemd/module/usbdialect"
)

import ()

// USB 移远：AT+QCFG="usbnet"（+QCFG: "usbnet",<n>）
func USB() *usbdialect.USBDialect {
	return &usbdialect.USBDialect{
		Query: `AT+QCFG="usbnet"`,
		Set:   func(v string) string { return `AT+QCFG="usbnet",` + v },
		Parse: usbdialect.ParseUSBnet,
		Modes: usbdialect.Modes(),
		Note:  "移远 AT+QCFG=\"usbnet\"（QModem cmds/quectel.sh）；改完**需重启模组**才生效。",
	}
}
