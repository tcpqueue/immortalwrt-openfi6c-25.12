package kernel

import (
	"testing"

	"modemd/module/foxconn"
	"modemd/module/lock"
	"modemd/module/neoway"
	"modemd/module/telit"
	"modemd/module/thales"
)

// scriptedLockDoer 按指令返回预置应答的假 LockATDoer（同时记录下发的锁网指令）
type scriptedLockDoer struct {
	replies  map[string][]string
	lockCmds []string
}

func (s *scriptedLockDoer) SendAT(cmd string) ([]string, error) {
	if r, ok := s.replies[cmd]; ok {
		return r, nil
	}
	return []string{"ERROR"}, nil
}
func (s *scriptedLockDoer) ModemID() int { return 0 }

func (s *scriptedLockDoer) ModemModel() string { return "FM160-CN" }
func (s *scriptedLockDoer) SendLockCommand(cmd string) error {
	s.lockCmds = append(s.lockCmds, cmd)
	return nil
}

// bandPrefSample QModem `vendor/thales.sh` 注释里的**真机应答样例**（逐字照抄）
var bandPrefSample = []string{
	"WCDMA,Enable Bands :1,2,4,5,8,",
	"WCDMA,Disable Bands:",
	"LTE,Enable Bands : 1,2,3,4,5,7,8,12,",
	"LTE,Disable Bands:",
	"NR5G_NSA,Enable Bands :",
	"NR5G_NSA,Disable Bands:",
	"NR5G_SA,Enable Bands :",
	"NR5G_SA,Disable Bands:",
	"OK",
}

// TestBandPrefParseStateSample 出厂态样例：LTE 列了 8 个频段、Disable 为空 → 判定"未受限"
func TestBandPrefParseStateSample(t *testing.T) {
	bands, locked := lock.BandPrefParseState(CellTechLTE, bandPrefSample)
	if len(bands) != 8 {
		t.Fatalf("LTE 频段数 = %d（%v），want 8", len(bands), bands)
	}
	if bands[0] != 1 || bands[7] != 12 {
		t.Errorf("LTE 频段表 = %v，want 1,2,3,4,5,7,8,12 升序", bands)
	}
	if locked {
		t.Error("Enable 覆盖了全部（Disable 为空）→ 不该判成受限")
	}
	// NR 两组都是空的 → 解析不出来，返回 false（界面显示原始应答）
	if bands, ok := lock.BandPrefParseState(CellTechNR, bandPrefSample); ok || bands != nil {
		t.Errorf("NR 空表应返回 false，实际 %v ok=%v", bands, ok)
	}
}

// TestBandPrefParseStateLocked 锁到 B3：Enable 是 Enable∪Disable 的真子集 → 判成受限
func TestBandPrefParseStateLocked(t *testing.T) {
	resp := []string{
		"LTE,Enable Bands : 3,",
		"LTE,Disable Bands: 1,2,4,5,7,8,12,",
		"OK",
	}
	bands, locked := lock.BandPrefParseState(CellTechLTE, resp)
	if len(bands) != 1 || bands[0] != 3 {
		t.Fatalf("LTE 频段 = %v，want [3]", bands)
	}
	if !locked {
		t.Error("Enable ⊊ Enable∪Disable → 应判成受限")
	}
	// NR 把 NR5G_NSA 与 NR5G_SA 两组合并（QModem 的说明：写入只能给一个 NR5G，读要合）
	respNR := []string{
		"NR5G_NSA,Enable Bands : 1,3,",
		"NR5G_SA,Enable Bands : 78,79,",
		"NR5G_SA,Disable Bands: 41,",
	}
	bands, locked = lock.BandPrefParseState(CellTechNR, respNR)
	if len(bands) != 4 || bands[0] != 1 || bands[1] != 3 || bands[2] != 78 || bands[3] != 79 {
		t.Fatalf("NR 应为 NSA∪SA = 1,3,78,79，实际 %v", bands)
	}
	if !locked {
		t.Error("Enable ⊊ Enable∪Disable → 应判成受限")
	}
	// SA 空、NSA 非空 → 只得到 NSA 那部分（同样不是“退回”，是合并后的结果）
	respNSA := []string{
		"NR5G_NSA,Enable Bands : 41,",
		"NR5G_NSA,Disable Bands: 1,",
		"NR5G_SA,Enable Bands :",
	}
	if bands, _ = lock.BandPrefParseState(CellTechNR, respNSA); len(bands) != 1 || bands[0] != 41 {
		t.Errorf("NSA 单独有值时应得到 41，实际 %v", bands)
	}
}

// TestBandPrefCapability 设备能力 = Enable ∪ Disable（与 QModem vendor/*.sh 同一算法）
func TestBandPrefCapability(t *testing.T) {
	d := &scriptedLockDoer{replies: map[string][]string{"AT+BAND_PREF?": bandPrefSample}}
	caps := lock.BandPrefCapability("AT+BAND_PREF?")(d)
	if len(caps[CellTechLTE]) != 8 {
		t.Errorf("LTE 能力 = %v，want 样例里的 8 个", caps[CellTechLTE])
	}
	if len(caps[CellTechNR]) != 0 {
		t.Errorf("样例里 NR 两组都空 → 不该给出 NR 能力，实际 %v", caps[CellTechNR])
	}
	// 应答带 ERROR → 一律不给（宁可不显示，也不报错的能力表）
	d2 := &scriptedLockDoer{replies: map[string][]string{"AT+Q": {"ERROR"}}}
	if caps := lock.BandPrefCapability("AT+Q")(d2); len(caps) != 0 {
		t.Errorf("ERROR 应答应返回空，实际 %v", caps)
	}
}

// TestParseNWSETBAND 有方：状态行 `<act>,<个数>,<B3>,<N78>…`（act：UMTS=2/LTE=3/NR=6），
// 且这些状态行**不带** `+NWSETBAND:` 标记（QModem 用 grep -v 把它们挑出来）。
func TestParseNWSETBAND(t *testing.T) {
	queryResp := []string{
		"AT+NWSETBAND?",
		"+NWSETBAND: 2", // 汇总行（组数），要被忽略
		"3,2,B1,B3",
		"6,1,N78",
		"OK",
	}
	cases := []struct {
		name string
		resp []string
		tech CellTech
		want []int
		ok   bool
	}{
		{"LTE 带前缀", queryResp, CellTechLTE, []int{1, 3}, true},
		{"NR 带前缀", queryResp, CellTechNR, []int{78}, true},
		{"标记与状态同一行", []string{"+NWSETBAND: 3,2,B1,B3"}, CellTechLTE, []int{1, 3}, true},
		{"裸数字 + 计数", []string{"+NWSETBAND: 3,3,1,3,5"}, CellTechLTE, []int{1, 3, 5}, true},
		{"裸数字无计数", []string{"+NWSETBAND: 3,1,3,5"}, CellTechLTE, []int{1, 3, 5}, true},
		{"act 写成制式名（宽容）", []string{"+NWSETBAND: NR5G,78"}, CellTechNR, []int{78}, true},
		{"制式对不上", []string{"3,1,B1"}, CellTechNR, nil, false},
		{"只有汇总行", []string{"+NWSETBAND: 0", "OK"}, CellTechLTE, nil, false},
		{"没有内容", []string{"OK"}, CellTechLTE, nil, false},
	}
	for _, c := range cases {
		got, ok := neoway.ParseNWSETBAND(c.tech, c.resp)
		if ok != c.ok {
			t.Errorf("%s: ok = %v, want %v（got %v）", c.name, ok, c.ok, got)
			continue
		}
		if !c.ok {
			continue
		}
		if lock.JoinInts(got, ",") != lock.JoinInts(c.want, ",") {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	// 同一份应答里，LTE 不会把 NR 的 N78 当成 78 挂到自己名下
	if bands, _ := neoway.ParseNWSETBAND(CellTechLTE, queryResp); len(bands) != 2 {
		t.Errorf("LTE = %v，不应包含 N78", bands)
	}
}

// TestNeowayCapabilityByAct 能力表：`=?` 里带前缀的频段名（B= LTE / N= NR），裸数字靠本行 act 归类
func TestNeowayCapabilityByAct(t *testing.T) {
	d := &scriptedLockDoer{replies: map[string][]string{
		"AT+NWSETBAND=?": {
			"AT+NWSETBAND=?",
			"+NWSETBAND: LTE,5,B1,B3,B5,B8,B38",
			"+NWSETBAND: NR,2,N41,N78",
			"OK",
		},
	}}
	caps := neoway.NeowayCapability("AT+NWSETBAND=?")(d)
	if len(caps[CellTechLTE]) != 5 || len(caps[CellTechNR]) != 2 {
		t.Errorf("能力表不对：LTE=%v NR=%v", caps[CellTechLTE], caps[CellTechNR])
	}
	// WB 前缀（UMTS）不进 LTE/NR
	d2 := &scriptedLockDoer{replies: map[string][]string{
		"AT+NWSETBAND=?": {"+NWSETBAND: 3,3,B1,B3,B8", "+NWSETBAND: 2,2,WB1,WB8"},
	}}
	caps2 := neoway.NeowayCapability("AT+NWSETBAND=?")(d2)
	if len(caps2[CellTechLTE]) != 3 || len(caps2[CellTechNR]) != 0 {
		t.Errorf("能力表不对：LTE=%v NR=%v", caps2[CellTechLTE], caps2[CellTechNR])
	}
}

// TestParseCSVIntsAndUnion 基础工具：非数字项跳过、合并去重升序
func TestParseCSVIntsAndUnion(t *testing.T) {
	got := lock.ParseCSVInts(" 1, 2 ,,x,3,")
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("parseCSVInts = %v", got)
	}
	if v := lock.UnionInts([]int{3, 1}, []int{1, 5}); len(v) != 3 || v[0] != 1 || v[1] != 3 || v[2] != 5 {
		t.Errorf("unionInts = %v, want [1 3 5]", v)
	}
	if v := lock.UnionInts(nil); v != nil {
		t.Errorf("unionInts(nil) = %v, want nil", v)
	}
}

// TestBandListStateParseWiring 方言实例上确实挂了（或没挂）解析器
func TestBandListStateParseWiring(t *testing.T) {
	thales := thales.BandPrefLockCommand()
	foxconn := foxconn.BandPrefLockCommand()
	neoway := neoway.NeowayLockCommand()
	telit := telit.TelitLockCommand()
	// 挂了解析器的：给它**对得上**的应答必须解析出频段表
	// （注意 locked 是另一个返回值：样例是出厂态，频段表非空但判定"未受限"）
	for _, c := range []struct {
		b    *lock.BandListLocker
		resp []string
	}{
		{thales, bandPrefSample},
		{foxconn, bandPrefSample},
		{neoway, []string{"3,2,B1,B3"}},
	} {
		if bands, _ := c.b.ParseBandLockState(CellTechLTE, c.resp); len(bands) == 0 {
			t.Errorf("%s: 对得上的应答应解析出频段，实际 %v", lock.DialectName(c.b), bands)
		}
	}
	// 对不上的应答一律 false（不臆造）
	for _, b := range []*lock.BandListLocker{thales, foxconn, neoway, telit.BandListLocker} {
		if bands, ok := b.ParseBandLockState(CellTechLTE, []string{"+X: 1,2,3"}); ok || bands != nil {
			t.Errorf("%s: 对不上的应答 ok = %v（%v），want false", lock.DialectName(b), ok, bands)
		}
	}
	// Telit 的 AT#BND? 只上报 LTE，且格式已确认 → 挂了解析器与能力查询（NR 仍不解析）
	if lock.HasParseStateFn(telit.BandListLocker) {
		t.Error("telit 的指令形态与列表式不同 → 不该再用通用 parseStateFn")
	}
	if _, ok := telit.ParseBandLockState(CellTechNR, []string{"#BND: 0,22,3FFFFFFFFFFFFFFF,0"}); ok {
		t.Error("telit 的 NR 当前值不可查 → 应返回 false")
	}
	if !lock.HasCapFn(thales) || !lock.HasCapFn(foxconn) || !lock.HasCapFn(neoway) {
		t.Error("thales / foxconn / neoway 应已接上能力查询")
	}
}
