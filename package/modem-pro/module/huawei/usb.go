package huawei

import (
	"modemd/iface"
	"modemd/module/imeidialect"
	"modemd/module/usbdialect"
)

import ()

// USB 华为/巴龙：AT^SETMODE? / AT^SETMODE=<n>
//
// 取值按平台分派（QModem vendor/huawei.sh get_mode/set_mode）：
//
//	hisilicon: 0=rndis 1=ecm 2=ncm
//	unisoc   : 0|2=ecm 1|3|4|5=ncm 6=rndis 7=mbim 8=ppp
//
// ⚠️ 展锐档里同一个数字在不同机型上含义不同，所以两个档的码表必须分开给（见 platformVariants）。
func USB(model string) *usbdialect.USBDialect {
	if d, ok := usbdialect.LookupVariant(iface.VendorTDTech, model); ok {
		return d
	}
	return USBHisilicon()
}

// USBHisilicon 海思巴龙档（= QModem vendor/huawei.sh 的 **default 分支**）。
//
// ⚠️ 脚本只特判 `unisoc`，其余平台（hisilicon / qualcomm…）全走 default：
//
//	set_mode()：ecm→0、ncm→4
//	get_mode()：0|2=ecm、1|3|4|5=ncm、6=rndis、7=mbim、8=ppp
//
// 以前这两张表**写反了**（他当成了 unisoc 的 0=rndis/1=ecm/2=ncm）——
// 后果是往巴龙模组下 AT^SETMODE=1（自以为是 ECM，实际是 NCM）。
func USBHisilicon() *usbdialect.USBDialect {
	return &usbdialect.USBDialect{
		Query: "AT^SETMODE?",
		Set:   func(v string) string { return "AT^SETMODE=" + v },
		Parse: imeidialect.RawValueParser,
		Modes: []usbdialect.Mode{
			{Value: "0", Name: "ECM", Note: "巴龙：USB 以太网（2 同义）"},
			{Value: "2", Name: "ECM", Note: "巴龙：USB 以太网"},
			{Value: "1", Name: "NCM", Note: "巴龙：高速 USB 网络（3/4/5 同义）"},
			{Value: "3", Name: "NCM", Note: "巴龙：高速 USB 网络"},
			{Value: "4", Name: "NCM", Note: "巴龙：高速 USB 网络（QModem 写的 ncm 参数就是它）"},
			{Value: "5", Name: "NCM", Note: "巴龙：高速 USB 网络"},
			{Value: "6", Name: "RNDIS", Note: "巴龙：经典 USB 网卡"},
			{Value: "7", Name: "MBIM", Note: "巴龙：微软标准"},
			{Value: "8", Name: "PPP", Note: "巴龙：PPP"},
		},
		Note: "华为/巴龙用 AT^SETMODE=<取值>（QModem vendor/huawei.sh 的 set_mode default 分支：ecm=0、ncm=4；" +
			"get_mode 还把 0|2 读成 ECM、1|3|4|5 读成 NCM、6=RNDIS、7=MBIM、8=PPP）；改完需重启模组生效。",
	}
}

// USBUnisoc 展锐平台档（MH5000-82 那批）—— QModem 只特判了这一支
func USBUnisoc() *usbdialect.USBDialect {
	return &usbdialect.USBDialect{
		Query: "AT^SETMODE?",
		Set:   func(v string) string { return "AT^SETMODE=" + v },
		Parse: imeidialect.RawValueParser,
		Modes: []usbdialect.Mode{
			{Value: "0", Name: "RNDIS", Note: "展锐平台：经典 USB 网卡"},
			{Value: "1", Name: "ECM", Note: "展锐平台：USB 以太网"},
			{Value: "2", Name: "NCM", Note: "展锐平台：高速 USB 网络"},
		},
		Note: "华为/巴龙（展锐平台）用 AT^SETMODE=<取值>（QModem vendor/huawei.sh 的 unisoc 分支：" +
			"0=RNDIS、1=ECM、2=NCM）；改完需重启模组生效。",
	}
}
