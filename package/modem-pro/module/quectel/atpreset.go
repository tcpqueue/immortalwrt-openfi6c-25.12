package quectel

import (
	"modemd/module/atpreset"
)

// ====================== 移远 Quectel ======================
func ATPresets() []atpreset.ATPreset {
	return []atpreset.ATPreset{
		atpreset.Of("移远（本模块）", "ATI", "模组型号（移远支持 ATI）"),
		atpreset.Of("移远（本模块）", "AT+QNWINFO", "网络接入信息（制式/频段/信道）"),
		atpreset.Of("移远（本模块）", `AT+QENG="servingcell"`, "服务小区（含 NR 信号）"),
		atpreset.Of("移远（本模块）", `AT+QENG="neighbourcell"`, "邻区列表"),
		atpreset.Of("移远（本模块）", "AT+QCAINFO", "载波聚合分量载波"),
		atpreset.Of("移远（本模块）", "AT+QGDCNT?", "流量计数器（累计字节）"),
		atpreset.Of("移远（本模块）", `AT+QNWCFG="nr5g_ambr"`, "签约速率 AMBR + CQI"),
		atpreset.Of("移远（本模块）", "AT+QTEMP", "温度传感器"),
		atpreset.Of("移远（本模块）", `AT+QCFG="usbnet"`, "USB 网卡模式"),
		atpreset.Of("锁网（移远）", `AT+QNWPREFCFG="mode_pref"`, "组网模式偏好（当前值）"),
		atpreset.Of("锁网（移远）", `AT+QNWPREFCFG="policy_band"`, "允许使用的频段（解锁时的还原依据）"),
		atpreset.Of("锁网（移远）", `AT+QNWPREFCFG="lte_band"`, "LTE 锁频段（当前值）"),
		atpreset.Of("锁网（移远）", `AT+QNWPREFCFG="nr5g_band"`, "NR 锁频段（当前值）"),
		atpreset.Of("锁网（移远）", `AT+QNWLOCK="common/4g"`, "LTE 锁小区状态"),
		atpreset.Of("锁网（移远）", `AT+QNWLOCK="common/5g"`, "NR 锁小区状态"),
		atpreset.Of("锁网（移远）", `AT+QNWLOCK="common/4g",0`, "解锁 LTE 小区"),
		atpreset.Of("锁网（移远）", `AT+QNWLOCK="common/5g",0`, "解锁 NR 小区"),
		atpreset.Of("设置（谨慎）", `AT+QCFG="usbnet",2`, "切 MBIM 网卡模式（需重启模组）"),
	}
}
