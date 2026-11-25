package kernel

import (
	"fmt"
	"time"
)

// ====================== 视图 ======================

// FormatBytes 把字节数格式化成人读文本
func FormatBytes(n int64) string {
	f := float64(n)
	switch {
	case n <= 0:
		return "0 B"
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", f/1024)
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", f/(1024*1024))
	case n < 1024*1024*1024*1024:
		return fmt.Sprintf("%.2f GB", f/(1024*1024*1024))
	default:
		return fmt.Sprintf("%.2f TB", f/(1024*1024*1024*1024))
	}
}

// BuildView 组装给前端的快照。supported 由调用方给出
// （模组是否声明了流量计数器指令，没声明就没法计量）。
func BuildView(st State, modemID int, supported bool, now time.Time) View {
	used := st.Used()
	v := View{
		ModemID:   modemID,
		Supported: supported,
		Enabled:   st.Enabled,
		CountUp:   st.CountUp,
		CountDown: st.CountDown,
		Action:    st.Action,
		ResetDay:  st.ResetDay,

		LimitBytes: st.LimitBytes,
		LimitText:  FormatBytes(st.LimitBytes),
		UsedBytes:  used,
		UsedText:   FormatBytes(used),
		UsedUp:     st.UsedUp,
		UsedDown:   st.UsedDown,
		Period:     st.Period,
		OverLimit:  st.Over(),
		Blocked:    st.Blocked,
		BlockedAt:  st.BlockedAt,
		Suspended:  st.Suspended,
	}
	if st.Period == "" {
		v.Period = Period(now, st.ResetDay)
	}
	if st.LimitBytes > 0 {
		v.Percent = float64(used) / float64(st.LimitBytes) * 100
		if v.Percent > 100 {
			v.Percent = 100
		}
		if used < st.LimitBytes {
			v.RemainBytes = st.LimitBytes - used
		}
		v.RemainText = FormatBytes(v.RemainBytes)
	} else {
		v.RemainText = "不限"
	}
	if v.Action == "" {
		v.Action = ActionBlock
	}
	v.PeriodStart, v.PeriodEnd = PeriodRange(now, st.ResetDay)
	return v
}
