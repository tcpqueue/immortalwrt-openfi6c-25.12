package fibocom

import (
	"strconv"

	"modemd/iface"
	"modemd/module/common"
	"modemd/module/imeidialect"
	"modemd/module/usbdialect"
)

// USB 广和通：AT+GTUSBMODE=<n>（+GTUSBMODE: <n>）。
//
// 取值**按平台分派**（QModem vendor/fibocom.sh get_mode/set_mode 的 case $platform，
// 写映射原文：qualcomm qmi=32/ecm=18/mbim=30/rndis=24；unisoc ecm=34/mbim=40/rndis=38/ncm=36；
// mediatek rndis=41；lte qmi=17/ecm=18/rndis=24）。
//
// 具体选哪张表由 platformVariants 决定（见 vendor_platform.go）—— 这里只保留指令骨架。
func USB(model string) *usbdialect.USBDialect {
	if d, ok := usbdialect.LookupVariant(iface.VendorFibocom, model); ok {
		return d
	}
	return USBQualcomm()
}

// USBQualcomm 高通（含 lte 平台：脚本里 lte 与 qualcomm 同一张码表）
func USBQualcomm() *usbdialect.USBDialect {
	d := USBBase()
	d.Modes = []usbdialect.Mode{
		{Value: "32", Name: "QMI", Note: "高通原生协议（需 qmi_wwan 驱动）"},
		{Value: "30", Name: "MBIM", Note: "微软标准（Windows 推荐）"},
		{Value: "18", Name: "ECM", Note: "USB 以太网（Linux 免驱常用，NCM 也用这个值）"},
		{Value: "24", Name: "RNDIS", Note: "经典 USB 网卡"},
	}
	d.Note = "广和通 AT+GTUSBMODE=<取值>（QModem vendor/fibocom.sh 的平台映射，本机已按平台给出取值表）；" +
		"改完需重启模组生效。"
	return d
}

// USBUnisoc 展锐平台
func USBUnisoc() *usbdialect.USBDialect {
	d := USBBase()
	d.Modes = []usbdialect.Mode{
		{Value: "34", Name: "ECM", Note: "USB 以太网（展锐平台常用）"},
		{Value: "40", Name: "MBIM", Note: "微软标准（Windows 推荐）"},
		{Value: "38", Name: "RNDIS", Note: "经典 USB 网卡"},
		{Value: "36", Name: "NCM", Note: "高速 USB 网络（Linux）"},
	}
	d.Note = "广和通 AT+GTUSBMODE=<取值>（QModem vendor/fibocom.sh 的平台映射，本机已按平台给出取值表）；" +
		"改完需重启模组生效。"
	return d
}

// USBMediaTek 联发科平台：脚本里只有 rndis=41 一个取值
func USBMediaTek() *usbdialect.USBDialect {
	d := USBBase()
	d.Modes = []usbdialect.Mode{{Value: "41", Name: "RNDIS", Note: "联发科平台只有这一个取值（QModem 写死 41）"}}
	d.Note = "广和通 AT+GTUSBMODE=<取值>（QModem vendor/fibocom.sh 的平台映射，本机已按平台给出取值表）；" +
		"改完需重启模组生效。"
	return d
}

// USBBase 三家共用的指令骨架（查询/写入/解析完全一样，只有码表与说明不同）
func USBBase() *usbdialect.USBDialect {
	return &usbdialect.USBDialect{
		Query: "AT+GTUSBMODE?",
		Set:   func(v string) string { return "AT+GTUSBMODE=" + v },
		Parse: func(resp []string) (string, bool) {
			line, ok := common.FirstLineWith(resp, "+GTUSBMODE:")
			if !ok {
				return "", false
			}
			if n, ok := imeidialect.NumInLine(line, 0); ok {
				return strconv.Itoa(n), true
			}
			return "", false
		},
	}
}
