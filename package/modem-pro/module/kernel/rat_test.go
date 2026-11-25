package kernel

// 制式（5G / 4G）来源的回归测试。
//
// ⚠️ 码表是**照抄设备厂商自己的脚本**取证的（`/opt/mu300/bin/mobile-data` 的 `signal()`）：
//
//	7)       rat="LTE"
//	13)      rat="5G NSA (LTE + NR)"
//	11|12)   rat="5G"
//	2|4|5|6) rat="3G"
//	0|1|3)   rat="2G"
//
// 而 AcT 就在我们已在读的 URC 的 `+CxREG` 行里 —— 所以不需要发 AT。

import (
	"testing"
)

// AcT → 制式：与厂商脚本逐条对齐；认不出的码必须返回 ok=false（不臆造）。
func TestRatFromACTVendorTable(t *testing.T) {
	cases := []struct {
		act   int
		tech  CellTech
		label string
		ok    bool
	}{
		{7, CellTechLTE, "4G LTE", true},      // 厂商：LTE
		{13, CellTechNR, "5G NR (NSA)", true}, // 厂商：5G NSA (LTE + NR)
		{11, CellTechNR, "5G NR", true},       // 厂商：5G
		{12, CellTechNR, "5G NR", true},       // 厂商：5G
		{2, CellTechWCDMA, "3G WCDMA", true},  // 厂商：3G
		{4, CellTechWCDMA, "3G WCDMA", true},
		{5, CellTechWCDMA, "3G WCDMA", true},
		{6, CellTechWCDMA, "3G WCDMA", true},
		{0, CellTechGSM, "2G GSM", true}, // 厂商：2G
		{1, CellTechGSM, "2G GSM", true},
		{3, CellTechGSM, "2G GSM", true},
		{9, "", "", false},  // 展锐扩展里没取证的码
		{14, "", "", false}, // 越界
		{99, "", "", false},
	}
	for _, c := range cases {
		netTech, tech, label, ok := ratFromACT(c.act)
		if ok != c.ok {
			t.Errorf("ratFromACT(%d) ok=%v，期望 %v", c.act, ok, c.ok)
			continue
		}
		if !ok {
			if netTech != "" || tech != "" || label != "" {
				t.Errorf("ratFromACT(%d) 不认识时必须全空，实际 %q/%q/%q", c.act, netTech, tech, label)
			}
			continue
		}
		if tech != c.tech || label != c.label {
			t.Errorf("ratFromACT(%d) = %q/%q/%q，期望 tech=%q label=%q", c.act, netTech, tech, label, c.tech, c.label)
		}
	}
}

// AcT 字段的提取：两种形式（应答多一个 <n>）都要对，且"字段不存在"与"AcT=0"要分清。
func TestURCACTFromLine(t *testing.T) {
	cases := []struct {
		line string
		act  int
		ok   bool
	}{
		// 真机样本（上报形式：没有 <n>）
		{`+CEREG: 1,"CFA102","0A11A009",11`, 11, true}, // → 5G
		{`+CGREG: 1,"0000","0A11A009",11`, 11, true},
		{`+CREG: 8,"0000","0A11A009",11`, 11, true},
		{`+CEREG: 1,"404A","0400E21E",7`, 7, true}, // → LTE
		// 应答形式（前面多一个 <n>）
		{`+CEREG: 2,1,"404A","0400E232",13`, 13, true}, // → 5G NSA
		{`+CGREG: 2,1,"FFFE","0400E232",13`, 13, true},
		// 没有 AcT 字段：不能返回 0
		{`+CEREG: 1,"404A","0400E21E"`, 0, false},
		{`+CREG: 0,1`, 0, false},
		{`+CREG: 2`, 0, false},
		{`+CGREG: 1,"FFFE"`, 0, false},
		// 不是注册行
		{`+CESQ: 99,99,255,255,255,255,59,54,54`, 0, false},
		{`+SPNWNAME: 460,11,"4E2D","4E2D",1,1`, 0, false},
		{``, 0, false},
	}
	for _, c := range cases {
		act, ok := urcACTFromLine(c.line)
		if ok != c.ok || act != c.act {
			t.Errorf("urcACTFromLine(%q) = %d,%v，期望 %d,%v", c.line, act, ok, c.act, c.ok)
		}
	}
}

// 端到端：URC 的 +CEREG 带 AcT=11 ⇒ 网络状态里应显示 5G；换成 7 ⇒ 4G。
func TestNetworkStatusTechFromURC(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	mod.feedURCLine(`+CGREG: 1,"0000","0A11A009",11`) // 5G
	mod.feedURCLine(realSPNWNAME)

	st := mod.networkStatusFromURC()
	if st.Tech != CellTechNR {
		t.Errorf("AcT=11 应判成 5G NR，实际 tech=%q label=%q", st.Tech, st.TechLabel)
	}
	if st.TechLabel != "5G NR" {
		t.Errorf("展示名应为 5G NR，实际 %q", st.TechLabel)
	}

	// 网络切回 LTE（真实会变：+CxREG 会重新上报）
	mod.feedURCLine(`+CEREG: 1,"404A","0400E21E",7`)
	st = mod.networkStatusFromURC()
	if st.Tech != CellTechLTE || st.TechLabel != "4G LTE" {
		t.Errorf("AcT=7 应判成 4G LTE，实际 tech=%q label=%q", st.Tech, st.TechLabel)
	}
}

// 优先级：EPS(CEREG) 比 CS(CREG) 更能代表“现在跑的是什么网”。
func TestACTPreferencePrefersEPS(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.feedURCLine(`+CREG: 0,1,"0000","0A11A009",11`) // CS: 5G
	mod.feedURCLine(`+CEREG: 1,"404A","0400E21E",7`)   // EPS: LTE
	if act, ok := mod.acTForRAT(); !ok || act != 7 {
		t.Errorf("应优先 EPS 的 AcT=7，实际 %d ok=%v", act, ok)
	}
	// 只有 CS 时退到 CS
	mod2 := newURCTestModem("/dev/stty_nr2")
	mod2.feedURCLine(`+CREG: 0,1,"0000","0A11A009",11`)
	if act, ok := mod2.acTForRAT(); !ok || act != 11 {
		t.Errorf("没有 EPS 时应退到 CS 的 AcT=11，实际 %d ok=%v", act, ok)
	}
	// 一个 AcT 都没有
	mod3 := newURCTestModem("/dev/stty_nr2")
	if _, ok := mod3.acTForRAT(); ok {
		t.Error("没有任何注册上报时不该给出制式")
	}
}

// 制式不该覆盖已有缓存里的频段/APN 等字段（只填制式三个字段）。
func TestTechDoesNotClobberOtherFields(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	mod.feedURCLine(`+CGREG: 1,"0000","0A11A009",11`)
	mod.cacheNetworkStatus(&NetworkStatus{
		ModemID:  mod.ID,
		APN:      "ctnet",
		Band:     78,
		BandText: "NR5G BAND 78",
	})
	st := mod.networkStatusFromURC()
	if st.APN != "ctnet" || st.Band != 78 || st.BandText != "NR5G BAND 78" {
		t.Errorf("缓存字段不该被动，实际 apn=%q band=%d %q", st.APN, st.Band, st.BandText)
	}
	if st.Tech != CellTechNR {
		t.Errorf("制式应被 AcT 填上，实际 %q", st.Tech)
	}
}
