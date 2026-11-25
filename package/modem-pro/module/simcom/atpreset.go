package simcom

import (
	"modemd/module/atpreset"
)

// ====================== 芯讯通 SIMCom ======================
func ATPresets() []atpreset.ATPreset {
	return []atpreset.ATPreset{
		atpreset.Of("芯讯通（本模块）", "ATI", "模组型号（芯讯通支持 ATI）"),
		atpreset.Of("芯讯通（本模块）", "AT+CPSI?", "服务小区（含 NR 信号）"),
		atpreset.Of("芯讯通（本模块）", "AT+CNWINFO?", "附加网络信息（带宽/调制/CQI/上行功率）"),
		atpreset.Of("芯讯通（本模块）", "AT+CPMUTEMP", "模组温度（不是 AT+QTEMP，那个在本固件回 ERROR）"),
		atpreset.Of("芯讯通（本模块）", "AT+CBC", "供电电压"),
		atpreset.Of("芯讯通（本模块）", "AT+QNWINFO", "网络接入信息"),
	}
}
