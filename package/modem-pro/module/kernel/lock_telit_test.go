package kernel

import (
	"math/big"
	"strings"
	"testing"

	"modemd/module/common"
	"modemd/module/lock"
	"modemd/module/telit"
	"modemd/module/variant"
)

// ====================== Telit：AT#BND（十六进制位图） ======================

// TestTelitHexBands 位图 → 频段：bit i → band i+1（低位段）/ band i+65（高位段）
func TestTelitHexBands(t *testing.T) {
	cases := []struct {
		hex    string
		offset int
		want   []int
	}{
		{"15", 0, []int{1, 3, 5}},                // 0b10101 → bit0/2/4
		{"1", 0, []int{1}},                       // 只有 B1
		{"0", 0, nil},                            // 全 0 = 没有频段
		{"2", 64, []int{66}},                     // 高位段：bit1 → 65+1
		{"8000000000000001", 64, []int{65, 128}}, // 首尾位
		{"", 0, nil},                             // 空
		{"ZZ", 0, nil},                           // 非十六进制
		{"(15)", 0, []int{1, 3, 5}},              // 能力应答里的括号
	}
	for _, c := range cases {
		got := common.HexBitmapBands(c.hex, c.offset)
		if lock.JoinInts(got, ",") != lock.JoinInts(c.want, ",") {
			t.Errorf("hexBitmapBands(%q,%d) = %v, want %v", c.hex, c.offset, got, c.want)
		}
	}
}

// TestTelitSplitMask 频段 → 两段位图；越界频段忽略
func TestTelitSplitMask(t *testing.T) {
	low, high := common.SplitBandBitmap([]int{1, 3, 65, 128, 999})
	if common.HexBitmapString(low) != "5" {
		t.Errorf("低位段 = %q, want 5（B1+B3）", common.HexBitmapString(low))
	}
	wantHigh := strings.ToUpper("8000000000000001") // bit0=65, bit63=128
	if common.HexBitmapString(high) != wantHigh {
		t.Errorf("高位段 = %q, want %q", common.HexBitmapString(high), wantHigh)
	}
	// 全 0 时是 "0"（bc 同款输出），不是空串
	if got := common.HexBitmapString(big.NewInt(0)); got != "0" {
		t.Errorf("空位图 = %q, want 0", got)
	}
}

// TestTelitParseBandLockState AT#BND? 只报 LTE 两段 → 只解析 LTE；NR / 异常一律 false
func TestTelitParseBandLockState(t *testing.T) {
	telit := telit.TelitLockCommand()
	bands, locked := telit.ParseBandLockState(CellTechLTE, []string{"AT#BND?", "#BND: 0,22,15,0", "OK"})
	if lock.JoinInts(bands, ",") != "1,3,5" {
		t.Fatalf("LTE 频段 = %v, want 1,3,5", bands)
	}
	if locked {
		t.Error("是否受限要给上游按能力表补判 → 这里必须是 false")
	}
	// 高位段（g4）也要带上：bit1 → band 66
	bands, _ = telit.ParseBandLockState(CellTechLTE, []string{"#BND: 0,22,1,2"})
	if lock.JoinInts(bands, ",") != "1,66" {
		t.Errorf("LTE 频段（含扩展段）= %v, want 1,66", bands)
	}
	// NR 当前值不可查
	if bands, ok := telit.ParseBandLockState(CellTechNR, []string{"#BND: 0,22,15,0"}); ok || bands != nil {
		t.Errorf("NR 应返回 false，实际 %v ok=%v", bands, ok)
	}
	// 格式不符
	if bands, ok := telit.ParseBandLockState(CellTechLTE, []string{"OK", "ERROR"}); ok || bands != nil {
		t.Errorf("格式不符应返回 false，实际 %v ok=%v", bands, ok)
	}
}

// TestTelitCapability AT#BND=? 的八组里只用 g3（LTE）/ g5+g6（NR）
func TestTelitCapability(t *testing.T) {
	telit := telit.TelitLockCommand()
	d := &scriptedLockDoer{replies: map[string][]string{
		"AT#BND=?": {"#BND: (0),(22),(F),(0),(30),(2),(0),(0)", "OK"},
	}}
	caps := telit.QueryBandCapability(d)
	if lock.JoinInts(caps[CellTechLTE], ",") != "1,2,3,4" {
		t.Errorf("LTE 能力 = %v, want 1,2,3,4", caps[CellTechLTE])
	}
	// g5=0x30 → bit4,5 → NR 5,6；g6=0x2 → bit1 → NR 66
	if lock.JoinInts(caps[CellTechNR], ",") != "5,6,66" {
		t.Errorf("NR 能力 = %v, want 5,6,66", caps[CellTechNR])
	}
	// 应答里带回 ERROR → 一律不给
	d2 := &scriptedLockDoer{replies: map[string][]string{"AT#BND=?": {"ERROR"}}}
	if caps := telit.QueryBandCapability(d2); caps != nil {
		t.Errorf("ERROR 应返回 nil，实际 %v", caps)
	}
}

// TestTelitSetBand LTE：单段位图；NR：先回读 AT#BND? 再把 LTE 两段原样带上
func TestTelitSetBand(t *testing.T) {
	telit := telit.TelitLockCommand()

	d := &scriptedLockDoer{}
	if err := telit.SetBandLockAt(d, CellTechLTE, []int{1, 3}); err != nil {
		t.Fatal(err)
	}
	if len(d.lockCmds) != 1 || d.lockCmds[0] != "AT#BND=0,22,5" {
		t.Errorf("LTE 下发 = %v, want [AT#BND=0,22,5]", d.lockCmds)
	}

	d2 := &scriptedLockDoer{replies: map[string][]string{"AT#BND?": {"#BND: 0,22,15,0"}}}
	if err := telit.SetBandLockAt(d2, CellTechNR, []int{78}); err != nil {
		t.Fatal(err)
	}
	// n78 ≥ 65 → 高位段 bit13 = 0x2000；低位段 0
	if len(d2.lockCmds) != 1 || d2.lockCmds[0] != "AT#BND=0,22,15,0,0,2000" {
		t.Errorf("NR 下发 = %v, want [AT#BND=0,22,15,0,0,2000]", d2.lockCmds)
	}
}

// TestTelitSetBandNRRefusesWithoutReadback 回读不出来就**不许下发** ——
// 否则 AT#BND=0,22,0,0,<nr>… 会把 LTE 的频段表悄悄清掉。
func TestTelitSetBandNRRefusesWithoutReadback(t *testing.T) {
	telit := telit.TelitLockCommand()
	d := &scriptedLockDoer{} // AT#BND? 回 ERROR
	err := telit.SetBandLockAt(d, CellTechNR, []int{78})
	if err == nil {
		t.Fatal("回读失败时应当报错")
	}
	if len(d.lockCmds) != 0 {
		t.Errorf("回读失败时绝不该下发任何指令，实际 %v", d.lockCmds)
	}
	if !strings.Contains(err.Error(), "LTE") {
		t.Errorf("错误信息要点明原因（会清掉 LTE 频段），实际 %q", err.Error())
	}
}

// TestTelitUnlock 解锁 = 写回能力位图（全量可用频段）；读不到能力就报错，不瞎发
func TestTelitUnlock(t *testing.T) {
	telit := telit.TelitLockCommand()
	d := &scriptedLockDoer{replies: map[string][]string{
		"AT#BND=?": {"#BND: (F),(0),(F),(0),(30),(0),(0),(0)"},
		"AT#BND?":  {"#BND: 0,22,1,0"},
	}}
	if err := telit.UnlockBandAt(d, CellTechLTE); err != nil {
		t.Fatal(err)
	}
	if len(d.lockCmds) != 1 || d.lockCmds[0] != "AT#BND=0,22,F" {
		t.Errorf("LTE 解锁 = %v, want [AT#BND=0,22,F]", d.lockCmds)
	}

	d2 := &scriptedLockDoer{replies: map[string][]string{
		"AT#BND=?": {"#BND: (F),(0),(F),(0),(30),(0),(0),(0)"},
		"AT#BND?":  {"#BND: 0,22,1,0"},
	}}
	if err := telit.UnlockBandAt(d2, CellTechNR); err != nil {
		t.Fatal(err)
	}
	if len(d2.lockCmds) != 1 || d2.lockCmds[0] != "AT#BND=0,22,1,0,30,0" {
		t.Errorf("NR 解锁 = %v, want [AT#BND=0,22,1,0,30,0]", d2.lockCmds)
	}

	d3 := &scriptedLockDoer{} // 能力读不到
	if err := telit.UnlockBandAt(d3, CellTechLTE); err == nil {
		t.Error("能力读不到时应报错")
	}
	if len(d3.lockCmds) != 0 {
		t.Errorf("不该下发任何指令，实际 %v", d3.lockCmds)
	}
}

// TestTelitShape Telit 挂在候选里、且探测指令/标记与其它方言不冲突
func TestTelitShape(t *testing.T) {
	m := &Modem{ID: 0, Model: "Telit FN990A40", ModelName: "FN990A40", cmdImpl: &common.UnknownCommand{}}
	var found *variant.LockCandidate
	for i := range m.lockCandidates() {
		if c := m.lockCandidates()[i]; c.Name == "telit" {
			cc := c
			found = &cc
			break
		}
	}
	if found == nil {
		t.Fatal("候选里应有 telit")
	}
	if found.Probe != "AT#BND?" || found.Mark != "#BND:" {
		t.Errorf("telit 候选 = %+v", *found)
	}
	if lockFamilyLabels["telit"] == "" {
		t.Error("telit 缺界面显示名")
	}
}
