package fibocom

import (
	"modemd/module/atpreset"
)

// ====================== 广和通 Fibocom ======================
func ATPresets() []atpreset.ATPreset {
	return []atpreset.ATPreset{
		atpreset.Of("广和通（本模块）", "ATI", "模组型号（广和通支持 ATI）"),
		atpreset.Of("广和通（本模块）", "AT+QNWINFO", "网络接入信息"),
		atpreset.Of("广和通（本模块）", `AT+FENG="servingcell"`, "服务小区（FM150/FM160 高通平台）"),
		atpreset.Of("广和通（本模块）", `AT+FENG="neighbourcell"`, "邻区（FM150/FM160 高通平台）"),
		atpreset.Of("广和通（本模块）", "AT+GTCCINFO?", "服务小区+邻区+信号（FM650-CN 展锐平台机型）"),
		atpreset.Of("广和通（本模块）", "AT+GTSTATIS?", "流量统计（瞬时速率 Byte/s）"),
		atpreset.Of("锁网（广和通）", `AT+QNWPREFCFG="mode_pref"`, "组网模式偏好（当前值）"),
		atpreset.Of("锁网（广和通）", `AT+QNWPREFCFG="policy_band"`, "允许使用的频段（解锁时的还原依据）"),
		atpreset.Of("锁网（广和通）", `AT+QNWPREFCFG="lte_band"`, "LTE 锁频段（当前值）"),
		atpreset.Of("锁网（广和通）", `AT+QNWPREFCFG="nr5g_band"`, "NR 锁频段（当前值）"),
		atpreset.Of("锁网（广和通）", `AT+QNWLOCK="common/4g"`, "LTE 锁小区状态"),
		atpreset.Of("锁网（广和通）", `AT+QNWLOCK="common/5g"`, "NR 锁小区状态"),
		atpreset.Of("锁网（广和通）", `AT+QNWLOCK="common/4g",0`, "解锁 LTE 小区"),
		atpreset.Of("锁网（广和通）", `AT+QNWLOCK="common/5g",0`, "解锁 NR 小区"),
	}
}
