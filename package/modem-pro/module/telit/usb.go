package telit

import (
	"modemd/module/imeidialect"
	"modemd/module/usbdialect"
)

import ()

// USB Telit：AT#USBCFG? / AT#USBCFG=<n>
//
// 取值表来自 QModem `vendor/telit.sh` 的 set_mode()：`rndis→0`、`qmi→1`、`mbim→2`、`ecm→3`
// （脚本里没有 get_mode，所以查询侧仍以设备回读的原码为准）。
func USB() *usbdialect.USBDialect {
	return &usbdialect.USBDialect{
		Query: "AT#USBCFG?",
		Set:   func(v string) string { return "AT#USBCFG=" + v },
		// ⚠️ Parse 必须给：`AT#USBCFG?` 回的是裸取值（不是 +QCFG 那种带 key 的写法），
		// 少了它 usbnetParse 会落到通用的 ParseUSBnet 上 ⇒ 读出来永远是"未上报"
		//（这个坑由 atcmd_invariants_test.go 的 TestVendorDialectsForEveryCatalogModel 拦住）。
		Parse: imeidialect.RawValueParser,
		Modes: []usbdialect.Mode{
			{Value: "0", Name: "RNDIS", Note: "经典 USB 网卡"},
			{Value: "1", Name: "QMI", Note: "高通原生协议（需 qmi_wwan 驱动）"},
			{Value: "2", Name: "MBIM", Note: "微软标准（Windows 推荐）"},
			{Value: "3", Name: "ECM", Note: "USB 以太网（Linux 免驱常用）"},
		},
		Note: "Telit 用 AT#USBCFG=<取值>（QModem vendor/telit.sh 的 set_mode：0=RNDIS、1=QMI、2=MBIM、3=ECM）；" +
			"改完需重启模组生效。",
	}
}
