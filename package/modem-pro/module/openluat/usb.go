package openluat

import (
	"modemd/module/imeidialect"
	"modemd/module/usbdialect"
)

import ()

// USB openluat Air724UG：AT+SETUSB=<n>
func USB() *usbdialect.USBDialect {
	return &usbdialect.USBDialect{
		Query: "AT+SETUSB?",
		Set:   func(v string) string { return "AT+SETUSB=" + v },
		Parse: imeidialect.RawValueParser,
		// 取值表来自 QModem vendor/openluat.sh 的 get_mode()/set_mode()：
		// `1) mode="rndis"` / `2) mode="ecm"`；set_mode 反向 rndis→1、ecm→2。
		// ⚠️ 以前这里是 nil ⇒ 下拉会落到**通用的移远表**（0/1/2/3/5），
		// 于是“ECM=1 / RNDIS=3”这种**错的值**被写下去（本机 1 其实是 RNDIS）。
		Modes: []usbdialect.Mode{
			{Value: "1", Name: "RNDIS", Note: "经典 USB 网卡（Windows 免驱）"},
			{Value: "2", Name: "ECM", Note: "USB 以太网（Linux 免驱常用）"},
		},
		// 切 USB 组合前先停掉当前的分组数据呼叫（QModem vendor/openluat.sh 的 set_mode()：
		// `stop_response=$(cmd_openluat_rndiscall_stop "$at_port")` 就写在 SETUSB 之前，
		// 脚本注释是 “Stop the current packet-data call before changing USB composition”）。
		// ⚠️ 只能挂在 PreSet（写入路径）上：查询 USB 模式时发它会白断一次数据。
		PreSet: []string{"AT+RNDISCALL=0,0"},
		Note: "openluat Air724UG 用 AT+SETUSB=<取值>（QModem vendor/openluat.sh 的 get_mode/set_mode：1=RNDIS、2=ECM）；" +
			"写入前会先发 AT+RNDISCALL=0,0 停掉当前的分组数据呼叫（QModem 的 set_mode() 就是这么做的，" +
			"否则切 USB 组合可能不生效）—— 这一步会短暂断数据，改完需重启模组生效。",
	}
}
