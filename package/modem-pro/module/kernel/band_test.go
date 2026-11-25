package kernel

// 频段（`+SPCLB`）来源的回归测试 —— 文件里唯一与频段相关的东西。
//
// 取证只做到"它是当前服务小区的频段"（随小区变：`+SPCLB: 5` 跟 `+SPPCI: …9B`、
// `+SPCLB: 3` 跟 `+SPPCI: …8C`），**没有**文档级证据说明它是 LTE 频段还是 NR 频段。
// 所以这一层钉两条：
//  1. 数值照收（用户要求“文件里有就写上”）；
//  2. **不许加 `B3`/`n78` 这种前缀**（分不清就别编），也不许拿八字段形式当真值。

import (
	"testing"
)

func TestURCBandFromLine(t *testing.T) {
	cases := []struct {
		line string
		band int
		ok   bool
	}{
		{"+SPCLB: 5", 5, true}, // 真机样本
		{"+SPCLB: 3", 3, true}, // 真机样本
		{"+SPCLB: 1", 1, true},
		{"+SPCLB: 78", 78, true},
		{"+SPCLB: 261", 261, true},
		{"+SPCLB: 0", 0, false}, // 没有 0 号频段
		{"+SPCLB: -1", 0, false},
		{"+SPCLB: 9999", 0, false},             // 超出任何频段表
		{"+SPCLB: 3,4,5,6,7,8,9,10", 0, false}, // 固件里那个八字段形式：不当单值用
		{"+SPCLB:", 0, false},
		{"+SPPCI: 0000009B", 0, false},
		{"+CEREG: 1,\"404A\"", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		band, ok := urcBandFromLine(c.line)
		if ok != c.ok || band != c.band {
			t.Errorf("urcBandFromLine(%q) = %d,%v，期望 %d,%v", c.line, band, ok, c.band, c.ok)
		}
	}
}

// 端到端：频段照收，但**不许**填 band_label（LTE/NR 分不清）。
func TestBandFromURCFillsValueOnlyNoPrefix(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	mod.feedURCLine(`+CGREG: 1,"0000","0A11A009",11`)
	mod.feedURCLine(`+SPCLB: 3`)

	st := mod.networkStatusFromURC()
	if st.Band != 3 {
		t.Errorf("频段应为 3，实际 %d", st.Band)
	}
	if st.BandText != "BAND 3" {
		t.Errorf("只按原值写 BAND <n>，实际 %q", st.BandText)
	}
	if st.BandLabel != "" {
		t.Errorf("不许编 B3/n3 前后缀（分不清 LTE/NR），实际 %q", st.BandLabel)
	}

	// 服务小区也要带上同一个值
	ci := mod.servingCellFromURC()
	if ci == nil || ci.Band != 3 {
		t.Fatalf("服务小区应带频段 3，实际 %+v", ci)
	}

	// 没收到 +SPCLB 时保持 0（不猜）
	mod2 := newURCTestModem("/dev/stty_nr2")
	mod2.noteURCFeedState(true, nil)
	mod2.feedURCLine(`+CSQ: 20,1`)
	mod2.feedURCLine(`+CGREG: 1,"0000","0A11A009",11`)
	if st := mod2.networkStatusFromURC(); st.Band != 0 || st.BandText != "" {
		t.Errorf("没有 +SPCLB 时应留空，实际 band=%d text=%q", st.Band, st.BandText)
	}

	// 已有更精确的缓存（扫描来的，带 band_label）时不被覆盖
	mod.cacheNetworkStatus(&NetworkStatus{
		ModemID:   mod.ID,
		Band:      78,
		BandText:  "NR5G BAND 78",
		BandLabel: "n78",
	})
	st = mod.networkStatusFromURC()
	if st.Band != 78 || st.BandText != "NR5G BAND 78" {
		t.Errorf("扫描来的频段更精确，不该被 SPCLB 覆盖，实际 band=%d text=%q", st.Band, st.BandText)
	}
}

// 自己写进去的那份必须**能刷新**（SPCLB 变了要跟着变）——
// 它会随接口返回进缓存，若判据写成 `base.Band == 0` 就会第一次填完永远冻住（实测踩到）。
func TestBandFromURCRefreshesItsOwn(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	mod.feedURCLine(`+CGREG: 1,"0000","0A11A009",11`)
	mod.feedURCLine(`+SPCLB: 5`)

	st := mod.networkStatusFromURC()
	if st.Band != 5 {
		t.Fatalf("首次频段应为 5，实际 %d", st.Band)
	}
	mod.cacheNetworkStatus(st) // 接口会把它写进缓存

	// 小区换了：频段跟着变，不能被"缓存里已经有频段"挡住
	mod.feedURCLine(`+SPCLB: 3`)
	st = mod.networkStatusFromURC()
	if st.Band != 3 || st.BandText != "BAND 3" {
		t.Errorf("频段应刷新成 3/BAND 3，实际 band=%d text=%q", st.Band, st.BandText)
	}
}
