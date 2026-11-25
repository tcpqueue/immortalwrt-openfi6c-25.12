package kernel

import (
	"time"
)

// ====================== 周期（账单月） ======================

// Period 当前周期标识。
//
// ResetDay = 15 时：9/20 属于 "2026-09-15" 这个周期（9/15 开始），
// 9/10 属于 "2026-08-15"。ResetDay = 0 表示用户自己掌握（只能是 "manual"，
// 用量不会自动清零）。
func Period(now time.Time, resetDay int) string {
	if resetDay <= 0 || resetDay > 28 {
		return "manual"
	}
	start := time.Date(now.Year(), now.Month(), resetDay, 0, 0, 0, 0, now.Location())
	if now.Before(start) {
		start = start.AddDate(0, -1, 0)
	}
	return start.Format("2006-01-02")
}

// PeriodRange 周期起止时间（给前端显示"下次重置"）
func PeriodRange(now time.Time, resetDay int) (time.Time, time.Time) {
	if resetDay <= 0 || resetDay > 28 {
		return time.Time{}, time.Time{}
	}
	start := time.Date(now.Year(), now.Month(), resetDay, 0, 0, 0, 0, now.Location())
	if now.Before(start) {
		start = start.AddDate(0, -1, 0)
	}
	return start, start.AddDate(0, 1, 0)
}
