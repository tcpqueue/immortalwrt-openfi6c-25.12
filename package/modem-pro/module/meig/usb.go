package meig

import (
	"modemd/iface"
	"modemd/module/imeidialect"
	"modemd/module/usbdialect"
)

import ()

// USB 美格：AT+SER? / AT+SER=<n>,1（QModem cmds/meig.sh：AT+SER=$2,1）
//
// ⚠️ 高通 2=ECM、非高通 2=NCM —— **同一个数字含义相反**，所以档位必须分开（见 platformVariants）。
func USB(model string) *usbdialect.USBDialect {
	if d, ok := usbdialect.LookupVariant(iface.VendorMeig, model); ok {
		return d
	}
	return USBOther()
}

// USBQualcomm 高通平台档
func USBQualcomm() *usbdialect.USBDialect {
	d := USBBase()
	d.Modes = []usbdialect.Mode{
		{Value: "2", Name: "ECM", Note: "高通平台：USB 以太网"},
		{Value: "3", Name: "RNDIS", Note: "高通平台：经典 USB 网卡"},
	}
	d.Note = "美格（高通平台）用 AT+SER=<取值>,1（QModem vendor/meig.sh：2=ECM、3=RNDIS）；改完需重启模组生效。"
	return d
}

// USBOther 非高通平台档
func USBOther() *usbdialect.USBDialect {
	d := USBBase()
	d.Modes = []usbdialect.Mode{
		{Value: "2", Name: "NCM", Note: "非高通平台：高速 USB 网络"},
		{Value: "3", Name: "RNDIS", Note: "经典 USB 网卡"},
	}
	d.Note = "美格用 AT+SER=<取值>,1（QModem vendor/meig.sh 各平台映射；本机按非高通平台给出取值）；改完需重启模组生效。"
	return d
}

// USBBase 两档共用的指令骨架
func USBBase() *usbdialect.USBDialect {
	return &usbdialect.USBDialect{
		Query: "AT+SER?",
		Set:   func(v string) string { return "AT+SER=" + v + ",1" },
		Parse: imeidialect.RawValueParser,
	}
}
