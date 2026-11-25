package common

// ====================== 组网模式偏好的语义档位 ======================
//
// 与 QModem `set_network_prefer_nr` 的分档一致：界面下拉、各家码表映射
// （移远 QNWPREFCFG / 芯讯通 CNMP / 有方 MYSYSINFO / 展锐 SPRAT …）都用这几个值。
//
// 它们只是**字符串词汇表**，与模组状态无关，所以住在厂商层与内核共用的 module/common：
// module/simcom 的 CNMP 码表要用，module/lock 的方言也要用。
// kernel 保留同名别名，见 module/kernel/iface_alias.go。

const (
	ModeAuto     = "AUTO"
	ModeWCDMA    = "WCDMA"
	ModeLTE      = "LTE"
	ModeNR5G     = "NR5G"
	ModeWCDMALTE = "WCDMA:LTE"
	ModeWCDMANR  = "WCDMA:NR5G"
	ModeLTENR5G  = "LTE:NR5G"
)
