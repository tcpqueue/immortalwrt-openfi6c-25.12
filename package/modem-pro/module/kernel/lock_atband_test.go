package kernel

import (
	"modemd/module/common"
	"modemd/module/lock"
	"strings"
	"testing"
)

// recLockDoer 记录锁网指令的假 LockATDoer
type recLockDoer struct{ cmds []string }

func (r *recLockDoer) SendAT(string) ([]string, error) { return []string{"OK"}, nil }
func (r *recLockDoer) ModemID() int                    { return 0 }
func (r *recLockDoer) ModemModel() string              { return "FM160-CN" }
func (r *recLockDoer) SendLockCommand(cmd string) error {
	r.cmds = append(r.cmds, cmd)
	return nil
}

// TestATBandClass class 取值（与 AT!BAND? 的行号一致：GW=0 / LTE=1 / NR=2）
func TestATBandClass(t *testing.T) {
	if got := lock.AtBandClass(CellTechLTE); got != 1 {
		t.Errorf("LTE class = %d, want 1", got)
	}
	if got := lock.AtBandClass(CellTechNR); got != 2 {
		t.Errorf("NR class = %d, want 2", got)
	}
}

// atBandSample QModem `_get_lockband_nr` 读的那种行：`<行号> - <制式>:<位图低位>,<位图高位>`
// （行号 = class；grep '[0-9]* - '，type 取第 3 个空白字段，掩码从第 11/28 字符起各 16 位）
var atBandSample = []string{
	"AT!BAND?",
	"0 - GW:    0000000000000000,0000000000000000",
	"1 - LTE:   0000000000000015,0000000000000000",
	"2 - NR5G:  0000000000000000,0000000000002000",
	"OK",
}

// TestATBandStateLine 行解析：行号即 class；掩码按冒号切出两段
func TestATBandStateLine(t *testing.T) {
	typ, class, low, high, ok := lock.AtBandState("1 - LTE:   0000000000000015,0000000000000000")
	if !ok || typ != "LTE" || class != 1 || low != "0000000000000015" || high != "0000000000000000" {
		t.Fatalf("解析 = %q %d %q %q ok=%v", typ, class, low, high, ok)
	}
	// 列宽不固定（多个空格）也要能认
	if _, class, _, _, ok := lock.AtBandState("2 - NR5G:        0000000000000000,0000000000002000"); !ok || class != 2 {
		t.Errorf("宽松列宽应能解析，class=%d ok=%v", class, ok)
	}
	// 不是这种行 → 一律不认
	for _, bad := range []string{"OK", "Index, Name", "1 - LTE: 15", "1 - LTE: ,", "x - LTE: 1,2"} {
		if _, _, _, _, ok := lock.AtBandState(bad); ok {
			t.Errorf("%q 不该被认成状态行", bad)
		}
	}
	// 0x 前缀是刻意宽容的（个别固件这么回）
	if _, _, low, _, ok := lock.AtBandState("1 - LTE: 0x15,0"); !ok || low != "15" {
		t.Errorf("0x 前缀应被宽容处理：low=%q ok=%v", low, ok)
	}
}

// TestATBandStateFor 技式标签匹配（NR 有多个可能标签）
func TestATBandStateFor(t *testing.T) {
	if _, low, high, ok := lock.AtBandStateFor(atBandSample, CellTechLTE); !ok || low == "" || high == "" {
		t.Errorf("LTE 行应能取到：ok=%v", ok)
	}
	class, low, _, ok := lock.AtBandStateFor(atBandSample, CellTechNR)
	if !ok || class != 2 || low != "0000000000000000" {
		t.Errorf("NR 行应能取到：class=%d ok=%v", class, ok)
	}
	// 没那一行（例如只报 GW/LTE 的固件）→ 不认
	if _, _, _, ok := lock.AtBandStateFor([]string{"0 - GW: 1,2"}, CellTechNR); ok {
		t.Error("没有 NR 行时不该认")
	}
}

// TestATBandParseBandLockState 状态解析：位图 → 频段（LTE 只低位段，NR 落在高位段）
func TestATBandParseBandLockState(t *testing.T) {
	b := &lock.AtBandLocker{}
	bands, locked := b.ParseBandLockState(CellTechLTE, atBandSample)
	if lock.JoinInts(bands, ",") != "1,3,5" {
		t.Fatalf("LTE 频段 = %v, want 1,3,5（0x15 的 bit0/2/4）", bands)
	}
	if locked {
		t.Error("是否受限需能力表才能判 → 这里必须是 false")
	}
	bands, _ = b.ParseBandLockState(CellTechNR, atBandSample)
	if lock.JoinInts(bands, ",") != "78" {
		t.Errorf("NR 频段 = %v, want 78（高位段 bit13）", bands)
	}
	// 全 0（没选任何频段）不该当成"解析成功"
	if bands, ok := b.ParseBandLockState(CellTechLTE, []string{"1 - LTE:   0000000000000000,0000000000000000"}); ok || bands != nil {
		t.Errorf("全 0 位图应返回 false，实际 %v ok=%v", bands, ok)
	}
}

// TestATBandMask 位图格式：`<low16>,<high16>` 两段十六进制（QModem `_band_list_to_mask`）
func TestATBandMask(t *testing.T) {
	mask := func(bands []int) string {
		low, high := common.SplitBandBitmap(bands)
		return common.HexBitmapPadded(low) + "," + common.HexBitmapPadded(high)
	}
	// LTE B1+B3+B5：全在 low 段 → high 全 0
	if got := mask([]int{1, 3, 5}); got != "0000000000000015,0000000000000000" {
		t.Errorf("LTE 位图 = %q，want 0000000000000015,0000000000000000（2^0+2^2+2^4）", got)
	}
	// NR n78：78 ≥ 65 → high 的 bit(78-65)=bit13 → 0x2000；low 全 0
	if got := mask([]int{78}); got != "0000000000000000,0000000000002000" {
		t.Errorf("n78 位图 = %q，want 0000000000000000,0000000000002000", got)
	}
	// NR n41（≤64）进 low 的 bit40 → 2^40 = 0x10000000000；n1 → bit0
	if got := mask([]int{1, 41}); got != "0000010000000001,0000000000000000" {
		t.Errorf("n1+n41 位图 = %q", got)
	}
	// 两段各 16 位
	for _, b := range []int{1, 64, 65, 128, 999} {
		s := strings.Split(mask([]int{b}), ",")
		if len(s) != 2 || len(s[0]) != 16 || len(s[1]) != 16 {
			t.Errorf("band %d：形状不对 %q", b, mask([]int{b}))
		}
	}
	// 越界频段（>128）直接忽略，不能溢出到下一段
	if got := mask([]int{999}); got != "0000000000000000,0000000000000000" {
		t.Errorf("越界频段应被忽略，实际 %q", got)
	}
}

// TestATBandCommands 指令形状
func TestATBandCommands(t *testing.T) {
	b := &lock.AtBandLocker{}
	if got := b.CommandQueryBandLock(CellTechLTE); got != "AT!BAND?" {
		t.Errorf("查询 = %q", got)
	}
	if got := b.CommandBandLockCaps(); got != "AT!BAND=?" {
		t.Errorf("能力 = %q", got)
	}
	// 多步下发 → 单条为空串（走 BandLockSetter）
	if got := b.CommandSetBandLock(CellTechLTE, []int{1, 3}); got != "" {
		t.Errorf("CommandSetBandLock 应为空（多步），got %q", got)
	}
	if got := b.CommandUnlockBand(CellTechLTE); got != "" {
		// 解锁要"拼全量位图再写回"，单条指令做不到；非空会被上层优先发出去（而 0F 就是锁定后的 Custom 模板）
		t.Errorf("CommandUnlockBand 应为空（走 BandLockSetter），got %q", got)
	}
	if got := b.CommandQueryCellLock(CellTechNR); got != "" {
		t.Errorf("无锁小区指令，应为空，got %q", got)
	}
	// 形状：NetLocker + LockVendorOps + BandLockSetter
	var l NetLocker = b
	if lockVendorOpsOf(l) == nil {
		t.Error("应实现 LockVendorOps")
	}
	if _, ok := l.(BandLockSetter); !ok {
		t.Error("应实现 BandLockSetter（建模板 + 激活）")
	}
	// 状态解析保守
	if bands, ok := b.ParseBandLockState(CellTechNR, []string{"+X"}); ok || bands != nil {
		t.Error("频段解析应保守返回 false")
	}
}

// TestATBandSetSteps 锁定 = 先建自定义模板，再 AT!BAND=0F 激活（两步）
func TestATBandSetSteps(t *testing.T) {
	b := &lock.AtBandLocker{}
	d := &recLockDoer{}
	if err := b.SetBandLockAt(d, CellTechLTE, []int{1, 3}); err != nil {
		t.Fatal(err)
	}
	if len(d.cmds) != 2 {
		t.Fatalf("应下发 2 条，实际 %v", d.cmds)
	}
	low, high := common.SplitBandBitmap([]int{1, 3})
	want := `AT!BAND=0F,1,"Custom",1,` + common.HexBitmapPadded(low) + "," + common.HexBitmapPadded(high)
	if d.cmds[0] != want {
		t.Errorf("第 1 条 = %q, want %q", d.cmds[0], want)
	}
	if d.cmds[1] != "AT!BAND=0F" {
		t.Errorf("第 2 条 = %q, want AT!BAND=0F", d.cmds[1])
	}

	d2 := &recLockDoer{}
	if err := b.SetBandLockAt(d2, CellTechNR, []int{78}); err != nil {
		t.Fatal(err)
	}
	if len(d2.cmds) != 2 || d2.cmds[0] != `AT!BAND=0F,1,"Custom",2,0000000000000000,0000000000002000` {
		t.Errorf("NR 下发 = %v", d2.cmds)
	}
}

// TestATBandCapability 设备能力 = 扫 AT!BAND=? 里的频段名（B1/N78），按前缀分 LTE / NR
func TestATBandCapability(t *testing.T) {
	b := &lock.AtBandLocker{}
	d := &scriptedLockDoer{replies: map[string][]string{
		"AT!BAND=?": {"Index, Name", "1 - LTE: 0000000000000015,0000000000000000", "Available:", "B1", "B3", "B5", "N78", "OK"},
	}}
	caps := b.QueryBandCapability(d)
	if lock.JoinInts(caps[CellTechLTE], ",") != "1,3,5" {
		t.Errorf("LTE 能力 = %v, want 1,3,5", caps[CellTechLTE])
	}
	if lock.JoinInts(caps[CellTechNR], ",") != "78" {
		t.Errorf("NR 能力 = %v, want 78", caps[CellTechNR])
	}
	// ERROR / 扫不到频段名 → 不给能力（上游会显示 BandCapabilityNote）
	if caps := b.QueryBandCapability(&scriptedLockDoer{replies: map[string][]string{"AT!BAND=?": {"ERROR"}}}); caps != nil {
		t.Errorf("ERROR 应返回 nil，实际 %v", caps)
	}
	if caps := b.QueryBandCapability(&scriptedLockDoer{replies: map[string][]string{"AT!BAND=?": {"Index, Name", "OK"}}}); len(caps) != 0 {
		t.Errorf("没有频段名时不该给出能力，实际 %v", caps)
	}
}

// TestATBandNameTokens 频段名 token 扫描的边界：前缀字母自带制式，但别把 NR5G/4G 当频段
func TestATBandNameTokens(t *testing.T) {
	got := lock.AtBandNameTokens("LTE B3, B41 / n78; NR5G 4G B0 X7")
	want := []lock.AtBandName{lock.AtBandNameOf(CellTechLTE, 3), lock.AtBandNameOf(CellTechLTE, 41), lock.AtBandNameOf(CellTechNR, 78)}
	if len(got) != len(want) {
		t.Fatalf("扫出 %v，want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 个 = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestATBandUnlock 解锁 = 用能力表拼全量位图写回（**不是**只发 AT!BAND=0F）
func TestATBandUnlock(t *testing.T) {
	b := &lock.AtBandLocker{}
	d := &scriptedLockDoer{replies: map[string][]string{
		"AT!BAND=?": {"Available:", "B1", "B3", "B5", "N78", "OK"},
	}}
	if err := b.UnlockBandAt(d, CellTechLTE); err != nil {
		t.Fatal(err)
	}
	// B1+B3+B5 → low 的 bit0/bit2/bit4 = 1+4+16 = 0x15
	want := `AT!BAND=0F,1,"Custom",1,0000000000000015,0000000000000000`
	if len(d.lockCmds) != 2 || d.lockCmds[0] != want || d.lockCmds[1] != "AT!BAND=0F" {
		t.Errorf("LTE 解锁下发 = %v, want [%s AT!BAND=0F]", d.lockCmds, want)
	}

	d2 := &scriptedLockDoer{replies: map[string][]string{
		"AT!BAND=?": {"Available:", "B1", "N78", "OK"},
	}}
	if err := b.UnlockBandAt(d2, CellTechNR); err != nil {
		t.Fatal(err)
	}
	// n78 → high 的 bit13 = 0x2000；class 回退静态值 2
	wantNR := `AT!BAND=0F,1,"Custom",2,0000000000000000,0000000000002000`
	if len(d2.lockCmds) != 2 || d2.lockCmds[0] != wantNR {
		t.Errorf("NR 解锁下发 = %v, want [%s …]", d2.lockCmds, wantNR)
	}

	// 能力表读不出来 → 拒发（绝不能只发 AT!BAND=0F 交差）
	d3 := &scriptedLockDoer{}
	if err := b.UnlockBandAt(d3, CellTechLTE); err == nil {
		t.Error("读不出能力时应报错")
	}
	if len(d3.lockCmds) != 0 {
		t.Errorf("不该下发任何指令，实际 %v", d3.lockCmds)
	}
}
