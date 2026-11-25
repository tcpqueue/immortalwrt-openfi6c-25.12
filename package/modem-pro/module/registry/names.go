package registry

// 本文件是「厂商实现类型 → 展示名」的两张表，原先写在 module/kernel/modem_at_plan.go 里：
//
//	atCmdImplName(impl interface{})   —— switch 6 个厂商指令类型
//	lockDialectName(l NetLocker)      —— switch 11 个锁网方言类型
//
// 为什么必须搬：那两处是内核里**最后按厂商实现类型分派**的地方 —— 只要它们在内核，
// 「加一家厂商」就得回头改内核，这正是本次重构要消掉的东西。
//
// 为什么放在 registry：本包**已经 import 了全部厂商包**（它就是唯一注册点），
// 搬过来零新增依赖，也不必给 8 个厂商包挨个加方法。
//
// ⚠️ 返回值是与实现里的文案**逐字一致**的：它们会进 `MD/module_support.md`
// （由 TestCatalogSyncDoc 生成并比对），改动会让文档与实现不一致。

import (
	"modemd/iface"
	"modemd/module/common"
	"modemd/module/fibocom"
	"modemd/module/gosuncn"
	"modemd/module/lock"
	"modemd/module/meig"
	"modemd/module/openluat"
	"modemd/module/quectel"
	"modemd/module/simcom"
	"modemd/module/tdtech"
	"modemd/module/telit"
	"modemd/module/unisoc"
)

// CommandName 指令实现的短名（去掉包名与指针）；未知类型返回 "unknown"。
func CommandName(impl interface{}) string {
	switch impl.(type) {
	case *quectel.QuectelCommand:
		return "QuectelCommand"
	case *fibocom.FibocomCommand:
		return "FibocomCommand"
	case *simcom.SimcomCommand:
		return "SimcomCommand"
	case *meig.MeigCommand:
		return "MeigCommand"
	case *tdtech.MT5700Command:
		return "tdtech.MT5700Command"
	case *unisoc.UnisocCommand:
		return "UnisocCommand"
	case *common.UnknownCommand:
		return "unknownCommand（自适应）"
	}
	return "unknown"
}

// DialectNote 锁网方言的「短名 + 一句话说明」（空 = 没有锁网实现）。
//
// 说明里带的是各家指令形态（AT+QNWPREFCFG / AT+GTACT…），属于厂商知识，
// 所以这张表必须住在厂商侧。
func DialectNote(l iface.NetLocker) string {
	switch v := l.(type) {
	case *lock.BandListLocker:
		return lock.DialectName(v) + "（AT+BAND_PREF / AT^BAND_PREF 频段表）"
	case *telit.TelitLocker:
		return lock.DialectName(v) + "（AT#BND 位图）"
	case *lock.AtBandLocker:
		return "atband（AT!BAND 模板 + 位图）"
	case *openluat.OpenluatLockCommand:
		return "openluat（AT*BAND 复合字段）"
	case *gosuncn.GosuncnLockCommand:
		return "gosuncn（AT+ZBAND 两形态 / AT+ZLOCKCELL）"
	case *simcom.SimcomLockCommand:
		return "simcom（AT+CSYSSEL + AT+CCELLCFG/AT+C5GCELLCFG）"
	case *fibocom.FibocomMTKCommand:
		return "mtk（AT+GTACT + AT+GTCELLLOCK）"
	case *tdtech.MT5700Command:
		return "balong（AT^LTEFREQLOCK / AT^NRFREQLOCK）"
	case *quectel.QuectelCommand:
		return "qnw（AT+QNWPREFCFG + AT+QNWLOCK）"
	case *quectel.QcfgBandLocker:
		return "qcfgband（AT+QCFG=\"band\" 十六进制位图，写后回读校验）"
	case *simcom.CnbpLocker:
		return "cnbp（AT+CNBP 位图）"
	}
	return ""
}
