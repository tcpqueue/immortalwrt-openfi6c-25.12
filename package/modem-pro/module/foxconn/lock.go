package foxconn

// ====================== Foxconn 锁频段（AT^BAND_PREF） ======================
//
// 依据 QModem `cmds/foxconn.sh`：与 Thales 同一套 `BAND_PREF` 写法，只是指令前缀
// 随机型是 `AT+` 或 `AT^`。这里取 `AT^`（与搬迁前 `lock.BandPrefLockCommand("Foxconn", "AT^")`
// 完全一致 —— 改前缀只需要改这一个常量）。
//
// 本机型无锁小区指令、也无解锁指令。

import "modemd/module/lock"

const bandPrefPrefix = "AT^"

// BandPrefLockCommand Foxconn：AT^BAND_PREF=<RAT>,2,<频段表>
func BandPrefLockCommand() *lock.BandListLocker {
	query := bandPrefPrefix + "BAND_PREF?"
	return lock.NewBandListLocker(
		"foxconn",
		bandPrefPrefix+"BAND_PREF=?",
		func(lock.CellTech) string { return query },
		func(tech lock.CellTech, b []int) string {
			rat := "LTE"
			if tech == lock.CellTechNR {
				rat = "NR5G"
			}
			return bandPrefPrefix + "BAND_PREF=" + rat + ",2," + lock.JoinInts(b, ",")
		},
		lock.BandPrefParseState,
		lock.BandPrefCapability(query),
		"Foxconn：锁频段用 "+bandPrefPrefix+"BAND_PREF=<RAT>,2,<频段表>（RAT：LTE / NR5G）；"+
			"查询应答是 `LTE,Enable Bands : 1,2,3,` 这种逐制式两行（Enable/Disable），"+
			"“是否受限”按 Enable 是否为 Enable∪Disable 的真子集判定；本机型无锁小区指令、也无解锁指令。",
	)
}
