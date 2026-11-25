package tdtech

import (
	"modemd/module/atpreset"
)

// 该模组的网络/信号/温度/流量**全在 `^` 私有命令里**，移远那套全回 ERROR
// （2026-09-30 实机核对），所以这份清单里一条 `AT+Q*` 都没有。
func ATPresets() []atpreset.ATPreset {
	return []atpreset.ATPreset{
		atpreset.Of("鼎桥（本模块）", "ATI", "模组型号（鼎桥支持 ATI）"),
		atpreset.Of("鼎桥（本模块）", "AT^MONSC", "服务小区（NR/LTE，直接给 dB 值）"),
		atpreset.Of("鼎桥（本模块）", "AT^MONNC", "邻区列表"),
		atpreset.Of("鼎桥（本模块）", "AT^HCSQ?", "信号强度（索引形式）"),
		atpreset.Of("鼎桥（本模块）", "AT^SYSINFOEX", "链路制式/系统信息"),
		atpreset.Of("鼎桥（本模块）", "AT^HFREQINFO?", "工作频率（频段/ARFCN/带宽）"),
		atpreset.Of("鼎桥（本模块）", "AT^ICCID?", "ICCID（AT+CCID 在本固件回 ERROR）"),
		atpreset.Of("鼎桥（本模块）", "AT^CHIPTEMP?", "温度（12 路传感器，0.1 ℃）"),
		atpreset.Of("鼎桥（本模块）", "AT^DSFLOWQRY", "流量统计（累计收发字节，十六进制）"),
		atpreset.Of("鼎桥（本模块）", "AT^DSAMBR=1", "签约速率 AMBR（kbps，只支持 cid 1）"),
	}
}
