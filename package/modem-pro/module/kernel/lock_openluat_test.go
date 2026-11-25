package kernel

import (
	"strings"
	"testing"

	"modemd/module/lock"
	"modemd/module/openluat"
)

// ====================== openluat（Air724UG）AT*BAND ======================

// openluatBandResp 真机形态的 AT*BAND? 应答（8 段：mode,gsm,umts,lte_high,lte_low,roaming,srv_domain,priority）
// lte_high=130（0b10000010 → band 34 的 bit1 + band 40 的 bit7）、lte_low=1（band 1）；mode=5（仅 LTE，掩码生效）
var openluatBandResp = []string{"AT*BAND?", "*BAND: 5,7,0,130,1,1,2,3", "OK"}

// openluatBandRespAuto 自动模式（mode=4）的应答：该模式下固件**忽略频段掩码**
var openluatBandRespAuto = []string{"AT*BAND?", "*BAND: 4,7,0,130,1,1,2,3", "OK"}

// TestOpenluatMaskRoundTrip 位图 ↔ 频段：low = band1-32 的 bit(band-1)，high = band33-64 的 bit(band-33)
// （位序取自 QModem `vendor/openluat.sh` 的 case：1→1、3→4、5→16、8→128、34→2、38→32、39→64、40→128）
func TestOpenluatMaskRoundTrip(t *testing.T) {
	high, low := openluat.OpenluatSplitLTE([]int{1, 3, 5, 8, 34, 38, 39, 40})
	if low != 1|4|16|128 {
		t.Errorf("low = %d, want %d（1,3,5,8 的位）", low, 1|4|16|128)
	}
	if high != 2|32|64|128 {
		t.Errorf("high = %d, want %d（34,38,39,40 的位）", high, 2|32|64|128)
	}
	// 反向：只报已知的那 9 个频段；越界/保留位不得变成频段
	got := openluat.OpenluatLTEbands(130, 1, openluat.OpenluatAir724LTEBands)
	if lock.JoinInts(got, ",") != "1,34,40" {
		t.Errorf("频段 = %v, want [1 34 40]", got)
	}
	// band 18 不在 Air724UG 的可用表里 → 即使位图置了就也不报
	if got := openluat.OpenluatLTEbands(0, 1<<17, openluat.OpenluatAir724LTEBands); got != nil {
		t.Errorf("保留位不该变成频段，实际 %v", got)
	}
}

// TestOpenluatBandFields 解析 `*BAND:` 行：跳过回显行、要求 8 段数字
func TestOpenluatBandFields(t *testing.T) {
	f, ok := openluat.OpenluatBandFields(openluatBandResp)
	if !ok || len(f) != 8 {
		t.Fatalf("字段 = %v ok=%v，want 8 段", f, ok)
	}
	if f[0] != "5" || f[3] != "130" || f[4] != "1" || f[6] != "2" || f[7] != "3" {
		t.Errorf("字段内容不对：%v", f)
	}
	// 只有回显行 / 字段不足 / 含非数字 → 不认
	for _, bad := range [][]string{
		{"AT*BAND?", "OK"},
		{"*BAND: 5,7,0,130,1,1,2"},           // 只有 7 段（旧实现能过，现在必须拒）
		{"*BAND: 5,7,0,130,1,1,2,3,4"},       // 多于 8 段：只取前 8
		{"at*band? 5,7,0,130,1,1,2,3", "OK"}, // 没有冒号（回显形态）
		{"*BAND: 5,7,0,abc,1,1,2,3", "OK"},   // 有非数字段
	} {
		if f, ok := openluat.OpenluatBandFields(bad); ok && len(bad[0]) > 0 && !strings.Contains(bad[0], "*BAND: 5,7,0,130,1,1,2,3,4") {
			t.Errorf("%v 不该被认成完整字段：%v", bad, f)
		}
	}
}

// TestOpenluatParseBandLockState 状态解析：只解析 LTE；NR 一律 false
func TestOpenluatParseBandLockState(t *testing.T) {
	o := &openluat.OpenluatLockCommand{}
	bands, locked := o.ParseBandLockState(CellTechLTE, openluatBandResp)
	if lock.JoinInts(bands, ",") != "1,34,40" {
		t.Fatalf("频段 = %v, want 1,34,40", bands)
	}
	if locked {
		t.Error("是否受限要给上游按能力表补判 → 这里必须 false")
	}
	if bands, ok := o.ParseBandLockState(CellTechNR, openluatBandResp); ok || bands != nil {
		t.Errorf("Air724UG 无 NR，应返回 false，实际 %v ok=%v", bands, ok)
	}
	if bands, ok := o.ParseBandLockState(CellTechLTE, []string{"*BAND: 5,7,0,abc,1,1,2,3"}); ok || bands != nil {
		t.Errorf("位图非法应返回 false，实际 %v ok=%v", bands, ok)
	}
	// mode≠5（自动/双模）：固件忽略掩码 → 不算“锁了频段”
	if bands, ok := o.ParseBandLockState(CellTechLTE, openluatBandRespAuto); ok || bands != nil {
		t.Errorf("mode=4 时掩码不生效，应返回 false，实际 %v ok=%v", bands, ok)
	}
}

// TestOpenluatSetBand 锁定 = 回读 → 写成 mode 5（否则掩码被忽略）→ 整串写回（其余字段原样）
func TestOpenluatSetBand(t *testing.T) {
	o := &openluat.OpenluatLockCommand{}
	d := &scriptedLockDoer{replies: map[string][]string{"AT*BAND?": openluatBandRespAuto}}
	if err := o.SetBandLockAt(d, CellTechLTE, []int{1, 40}); err != nil {
		t.Fatal(err)
	}
	// band1 → low bit0 = 1；band40 → high bit7 = 128；mode 强制写 5，其余（gsm/umts/roaming/srv/priority）原样
	want := "AT*BAND=5,7,0,128,1,1,2,3"
	if len(d.lockCmds) != 1 || d.lockCmds[0] != want {
		t.Errorf("下发 = %v, want [%s]", d.lockCmds, want)
	}
	// mode 不在 0-15 → 拒发
	dBadMode := &scriptedLockDoer{replies: map[string][]string{"AT*BAND?": {"*BAND: 99,7,0,130,1,1,2,3"}}}
	if err := o.SetBandLockAt(dBadMode, CellTechLTE, []int{1}); err == nil {
		t.Error("mode=99 应报错")
	}
}

// TestOpenluatSetRefusesWithoutReadback 回读不出来就**不许下发** ——
// 否则会把漫游/服务域等字段一起改掉。
func TestOpenluatSetRefusesWithoutReadback(t *testing.T) {
	o := &openluat.OpenluatLockCommand{}
	for _, bad := range map[string][]string{
		"回 ERROR": {"ERROR"},
		"字段不足":    {"*BAND: 4,7,0,130,1,1,2"},
		"位图不是数字":  {"*BAND: 4,7,0,abc,1,1,2,3"},
	} {
		d := &scriptedLockDoer{replies: map[string][]string{"AT*BAND?": bad}}
		err := o.SetBandLockAt(d, CellTechLTE, []int{1})
		if err == nil {
			t.Errorf("%s：应当报错", bad)
		}
		if len(d.lockCmds) != 0 {
			t.Errorf("%s：不该下发任何指令，实际 %v", bad, d.lockCmds)
		}
	}
	// 频段全落在 Air724UG 不支持的范围 → 也拒发（别写一个空的位图）
	d := &scriptedLockDoer{replies: map[string][]string{"AT*BAND?": openluatBandResp}}
	if err := o.SetBandLockAt(d, CellTechLTE, []int{65, 66}); err == nil {
		t.Error("不支持的频段应当报错")
	}
	if len(d.lockCmds) != 0 {
		t.Errorf("不该下发，实际 %v", d.lockCmds)
	}
	// NR → 明确报"该机型没有 NR"
	if err := o.SetBandLockAt(d, CellTechNR, []int{78}); err == nil || !strings.Contains(err.Error(), "NR") {
		t.Errorf("NR 应报该机型无 NR，实际 %v", err)
	}
}

// TestOpenluatUnlock 解锁（QModem set_lockband 的空列表分支）：
// mode≠5 发 `AT*BAND=<mode>`；mode=5 则写全部可用频段（重复发 mode 5 会把锁定值写回去）。
func TestOpenluatUnlock(t *testing.T) {
	o := &openluat.OpenluatLockCommand{}
	// mode=4（自动）→ 只发 mode
	dAuto := &scriptedLockDoer{replies: map[string][]string{"AT*BAND?": openluatBandRespAuto}}
	if err := o.UnlockBandAt(dAuto, CellTechLTE); err != nil {
		t.Fatal(err)
	}
	if len(dAuto.lockCmds) != 1 || dAuto.lockCmds[0] != "AT*BAND=4" {
		t.Errorf("解锁 = %v, want [AT*BAND=4]", dAuto.lockCmds)
	}
	// mode=5 → 写全部可用频段：low=1|4|16|128=149、high=2|32|64|128|256=482
	d := &scriptedLockDoer{replies: map[string][]string{"AT*BAND?": openluatBandResp}}
	if err := o.UnlockBandAt(d, CellTechLTE); err != nil {
		t.Fatal(err)
	}
	want := "AT*BAND=5,7,0,482,149,1,2,3"
	if len(d.lockCmds) != 1 || d.lockCmds[0] != want {
		t.Errorf("解锁 = %v, want [%s]", d.lockCmds, want)
	}
	// 读不出来 → 拒发
	d2 := &scriptedLockDoer{}
	if err := o.UnlockBandAt(d2, CellTechLTE); err == nil {
		t.Error("读不出 current_mode 时应报错")
	}
	if len(d2.lockCmds) != 0 {
		t.Errorf("不该下发，实际 %v", d2.lockCmds)
	}
}

// TestOpenluatShape 接口形状：NetLocker + LockVendorOps + BandLockSetter；无锁小区 / 无 NR
func TestOpenluatShape(t *testing.T) {
	o := &openluat.OpenluatLockCommand{}
	var l NetLocker = o
	if lockVendorOpsOf(l) == nil {
		t.Error("应实现 LockVendorOps")
	}
	if _, ok := l.(BandLockSetter); !ok {
		t.Error("应实现 BandLockSetter（AT*BAND 要回读后整串改写）")
	}
	if got := o.CommandQueryBandLock(CellTechLTE); got != "AT*BAND?" {
		t.Errorf("LTE 查询 = %q", got)
	}
	if got := o.CommandQueryBandLock(CellTechNR); got != "" {
		t.Errorf("NR 查询应为空，got %q", got)
	}
	for _, c := range []string{
		o.CommandQueryCellLock(CellTechLTE), o.CommandCellLockCaps(),
		o.CommandQueryPolicyBand(), o.CommandUnlockBand(CellTechLTE),
		o.CommandBandLockCaps(),
	} {
		if c != "" {
			t.Errorf("这几项应为空，got %q", c)
		}
	}
	caps := o.QueryBandCapability(&scriptedLockDoer{})
	if lock.JoinInts(caps[CellTechLTE], ",") != lock.JoinInts(openluat.OpenluatAir724LTEBands, ",") {
		t.Errorf("能力表 = %v, want %v", caps[CellTechLTE], openluat.OpenluatAir724LTEBands)
	}
	if len(caps[CellTechNR]) != 0 {
		t.Errorf("Air724UG 无 NR，不该给 NR 能力：%v", caps[CellTechNR])
	}
	if lockFamilyLabels["openluat"] == "" {
		t.Error("openluat 缺界面显示名")
	}
}
