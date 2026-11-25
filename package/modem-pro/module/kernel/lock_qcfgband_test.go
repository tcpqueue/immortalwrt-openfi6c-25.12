package kernel

import (
	"modemd/module/common"
	"modemd/module/lock"
	"modemd/module/quectel"
	"modemd/module/variant"
	"strings"
	"testing"

	"modemd/iface"
)

// ====================== 移远旧款 4G：AT+QCFG="band" ======================

// qcfgFakeFirmware 模拟 EC20/EC25 类固件：保存 `+QCFG: "band",…` 的参数段，
// 写入后更新自身状态 —— 这样才能真的测到"写后回读校验 + 回滚"。
type qcfgFakeFirmware struct {
	fields  []string // 当前参数段
	cmds    []string // 记录下发过什么
	noEcho  bool     // 回读直接报 ERROR（固件不认这条指令）
	ignore  bool     // 假装忽略写入（回读还是旧值）—— 校验应当失败
	layout4 bool     // 写入后回读变成 4 段（模拟"段数口径写错"的固件）
}

func (f *qcfgFakeFirmware) ModemID() int { return 0 }

func (f *qcfgFakeFirmware) ModemModel() string { return "EC25" }

func (f *qcfgFakeFirmware) SendAT(string) ([]string, error) {
	if f.noEcho {
		return []string{"+CME ERROR: 4"}, nil // 真机 RM500U-CNV 就是这么回的
	}
	return []string{`AT+QCFG="band"`, `+QCFG: "band",` + strings.Join(f.fields, ","), "OK"}, nil
}

func (f *qcfgFakeFirmware) SendLockCommand(cmd string) error {
	f.cmds = append(f.cmds, cmd)
	if !strings.HasPrefix(cmd, `AT+QCFG="band",`) {
		return nil
	}
	if f.ignore {
		return nil // 固件没接受
	}
	f.fields = common.SplitCSVFields(strings.TrimPrefix(cmd, `AT+QCFG="band",`))
	if f.layout4 && len(f.fields) == 3 {
		f.fields = append([]string{"0"}, f.fields...) // 变成 4 段口径
	}
	return nil
}

// TestQcfgBandSegmentLayout 按段数定位 LTE 段：3 段 → 第 2 段（QModem 口径）；4 段 → 第 3 段（手册口径）
func TestQcfgBandSegmentLayout(t *testing.T) {
	fs, idx, ok := quectel.QcfgBandLTEIndex([]string{`+QCFG: "band",0,15,0`})
	if !ok || idx != 1 || len(fs) != 3 {
		t.Fatalf("3 段：fields=%v idx=%d ok=%v，want idx=1", fs, idx, ok)
	}
	fs, idx, ok = quectel.QcfgBandLTEIndex([]string{`+QCFG: "band",0x0,0x0,3FFFFFFFFFFFFFFF,0x0`})
	if !ok || idx != 2 || len(fs) != 4 {
		t.Fatalf("4 段：fields=%v idx=%d ok=%v，want idx=2", fs, idx, ok)
	}
	// 段数不足 / 别的 QCFG 子项 → 不认（免探测误判）
	for _, bad := range [][]string{
		{`+QCFG: "band",0,15`},
		{`+QCFG: "usbnet",5`},
		{`+QCFG: "band"`},
		{"+CME ERROR: 4"},
	} {
		if _, _, ok := quectel.QcfgBandLTEIndex(bad); ok {
			t.Errorf("%v 不该被认成 band 参数段", bad)
		}
	}
}

// TestQcfgBandParseState 两种口径都能解析：3 段取第 2 段、4 段取第 3 段；NR 一律 false
func TestQcfgBandParseState(t *testing.T) {
	q := quectel.QcfgBandCommand()
	bands, locked := q.ParseBandLockState(CellTechLTE, []string{`+QCFG: "band",0,15,0`})
	if lock.JoinInts(bands, ",") != "1,3,5" {
		t.Fatalf("3 段：LTE 频段 = %v, want 1,3,5（0x15 的 bit0/2/4）", bands)
	}
	if locked {
		t.Error("是否受限要给上游按能力表补判 → 这里必须 false")
	}
	// 4 段：第 3 段才是 LTE（0x0,0x0,0xA,0x0 → band 2,4）
	bands, _ = q.ParseBandLockState(CellTechLTE, []string{`+QCFG: "band",0x0,0x0,A,0x0`})
	if lock.JoinInts(bands, ",") != "2,4" {
		t.Errorf("4 段：LTE 频段 = %v, want 2,4", bands)
	}
	if bands, ok := q.ParseBandLockState(CellTechNR, []string{`+QCFG: "band",0,15,0`}); ok || bands != nil {
		t.Errorf("本方言无 NR，应返回 false，实际 %v ok=%v", bands, ok)
	}
	if bands, ok := q.ParseBandLockState(CellTechLTE, []string{`+QCFG: "band",0,zz,0`}); ok || bands != nil {
		t.Errorf("位图非法应返回 false，实际 %v ok=%v", bands, ok)
	}
}

// TestQcfgBandSet3Segment 3 段固件：只替换第 2 段，其余段原样保留，且回读校验通过
// （QModem 的 cmd_qcfg_band_reset 会硬写 0，那会把另两个制式的位图清掉）
func TestQcfgBandSet3Segment(t *testing.T) {
	q := quectel.QcfgBandCommand()
	f := &qcfgFakeFirmware{fields: []string{"A", "0", "C"}}
	if err := q.SetBandLockAt(f, CellTechLTE, []int{1, 3}); err != nil {
		t.Fatal(err)
	}
	if len(f.cmds) != 1 || f.cmds[0] != `AT+QCFG="band",A,5,C` {
		t.Errorf("下发 = %v, want [AT+QCFG=\"band\",A,5,C]", f.cmds)
	}
	if lock.JoinInts(common.HexBitmapBands(f.fields[1], 0), ",") != "1,3" {
		t.Errorf("固件状态没更新：%v", f.fields)
	}
}

// TestQcfgBandSet4Segment 4 段固件（<GSM>,<UMTS>,<LTE>,<TDSCDMA>）：只动第 3 段
func TestQcfgBandSet4Segment(t *testing.T) {
	q := quectel.QcfgBandCommand()
	f := &qcfgFakeFirmware{fields: []string{"0x1", "0x2", "0", "0x0"}}
	if err := q.SetBandLockAt(f, CellTechLTE, []int{3}); err != nil {
		t.Fatal(err)
	}
	want := `AT+QCFG="band",0x1,0x2,4,0x0` // band 3 → bit(3-1) = 0x4
	if len(f.cmds) != 1 || f.cmds[0] != want {
		t.Errorf("下发 = %v, want [%s]", f.cmds, want)
	}
}

// TestQcfgBandWriteVerifyAndRollback 写后回读校验：固件没接受就**回滚**并报错（不留在写坏的状态）
func TestQcfgBandWriteVerifyAndRollback(t *testing.T) {
	q := quectel.QcfgBandCommand()
	f := &qcfgFakeFirmware{fields: []string{"A", "3C", "C"}, ignore: true}
	err := q.SetBandLockAt(f, CellTechLTE, []int{1})
	if err == nil {
		t.Fatal("固件没接受写入时应当报错")
	}
	if !strings.Contains(err.Error(), "回滚") {
		t.Errorf("报错应说明已回滚，实际 %v", err)
	}
	// 第一条是写入、第二条是回滚（原值）
	if len(f.cmds) != 2 || f.cmds[1] != `AT+QCFG="band",A,3C,C` {
		t.Errorf("应当写入后再回滚，实际 %v", f.cmds)
	}
	// 段数口径写错（回读变成 4 段）同样应当回滚
	f2 := &qcfgFakeFirmware{fields: []string{"A", "3C", "C"}, layout4: true}
	if err := q.SetBandLockAt(f2, CellTechLTE, []int{1}); err == nil {
		t.Error("段数口径变化时应报错")
	} else if !strings.Contains(err.Error(), "口径") {
		t.Errorf("报错应提到口径，实际 %v", err)
	}
	if len(f2.cmds) != 2 {
		t.Errorf("也应当回滚，实际 %v", f2.cmds)
	}
}

// TestQcfgBandRefusesWhenUnreadable 回读不出参数段就拒发（不发任何写指令）
func TestQcfgBandRefusesWhenUnreadable(t *testing.T) {
	q := quectel.QcfgBandCommand()
	f := &qcfgFakeFirmware{noEcho: true}
	if err := q.SetBandLockAt(f, CellTechLTE, []int{1}); err == nil {
		t.Error("回读报 ERROR 时应拒发")
	}
	if len(f.cmds) != 0 {
		t.Errorf("不该下发，实际 %v", f.cmds)
	}
	// 频段全在 LTE 范围外 → 也拒发（别写一个空位图）
	f2 := &qcfgFakeFirmware{fields: []string{"0", "0", "0"}}
	if err := q.SetBandLockAt(f2, CellTechLTE, []int{78}); err == nil {
		t.Error("78 不是 LTE 频段（1-64），应当报错")
	}
	if len(f2.cmds) != 0 {
		t.Errorf("不该下发，实际 %v", f2.cmds)
	}
	// NR → 明确报"本方言没有 NR 写法"
	if err := q.SetBandLockAt(f2, CellTechNR, []int{78}); err == nil || !strings.Contains(err.Error(), "LTE") {
		t.Errorf("NR 应报本方言只支持 LTE，实际 %v", err)
	}
}

// TestQcfgBandUnlock 解锁 = LTE 段写全 1（不限制），其余段保留（两种口径各测一次）
func TestQcfgBandUnlock(t *testing.T) {
	q := quectel.QcfgBandCommand()
	f := &qcfgFakeFirmware{fields: []string{"A", "15", "C"}}
	if err := q.UnlockBandAt(f, CellTechLTE); err != nil {
		t.Fatal(err)
	}
	want := `AT+QCFG="band",A,FFFFFFFFFFFFFFFF,C`
	if len(f.cmds) != 1 || f.cmds[0] != want {
		t.Errorf("3 段解锁 = %v, want [%s]", f.cmds, want)
	}
	f4 := &qcfgFakeFirmware{fields: []string{"0x1", "0x2", "8", "0x4"}}
	if err := q.UnlockBandAt(f4, CellTechLTE); err != nil {
		t.Fatal(err)
	}
	want4 := `AT+QCFG="band",0x1,0x2,FFFFFFFFFFFFFFFF,0x4`
	if len(f4.cmds) != 1 || f4.cmds[0] != want4 {
		t.Errorf("4 段解锁 = %v, want [%s]", f4.cmds, want4)
	}
	// 读不出来 → 拒发
	bad := &qcfgFakeFirmware{noEcho: true}
	if err := q.UnlockBandAt(bad, CellTechLTE); err == nil {
		t.Error("读不出参数段时应报错")
	}
	if len(bad.cmds) != 0 {
		t.Errorf("不该下发，实际 %v", bad.cmds)
	}
	// NR 解锁 → 报错
	if err := q.UnlockBandAt(f, CellTechNR); err == nil {
		t.Error("NR 解锁应报错")
	}
}

// TestQnwCellLockProfiles 移远锁小区的三种平台形态（上游 vendor/quectel.sh 原文）
//
//	lockcell_qualcomm：LTE "common/4g",1,<arfcn>,<pci>；NR "common/5g",<pci>,<arfcn>,<scs>,<band>
//	lockcell_unisoc  ：LTE/NR 都是 "common/lte"|"common/5g",1,<arfcn>,<pci>（**频点在前**）
//	lockcell_lte     ：只有 "common/lte"，1=只锁频点（pci 写 0）、2=频点+PCI
func TestQnwCellLockProfiles(t *testing.T) {
	lte := CellLockRequest{Tech: CellTechLTE, ARFCN: 2452, PCID: 155, LockType: CellLockARFCNPCI}
	nr := CellLockRequest{Tech: CellTechNR, ARFCN: 627264, PCID: 521, LockType: CellLockARFCNPCI}

	qual := quectel.QnwLockCommands{} // 零值 = 高通（默认必须保持不变）
	if got := qual.CommandQueryCellLock(CellTechLTE); got != `AT+QNWLOCK="common/4g"` {
		t.Errorf("高通 LTE 查询 = %q", got)
	}
	if got := qual.CommandLockCell(lte); got != `AT+QNWLOCK="common/4g",1,2452,155` {
		t.Errorf("高通 LTE 锁 = %q", got)
	}
	if got := qual.CommandLockCell(nr); got != `AT+QNWLOCK="common/5g",521,627264,1,78` {
		t.Errorf("高通 NR 锁 = %q（band 由 arfcn 推算，627264 → n78）", got)
	}
	if got := qual.CommandLockCell(CellLockRequest{Tech: CellTechNR, LockType: CellLockNone}); got != `AT+QNWLOCK="common/5g",0` {
		t.Errorf("高通 NR 解锁 = %q", got)
	}

	uni := quectel.QnwLockCommands{CellProfile: quectel.CellUnisoc}
	if got := uni.CommandQueryCellLock(CellTechLTE); got != `AT+QNWLOCK="common/lte"` {
		t.Errorf("展锐 LTE 查询 = %q", got)
	}
	if got := uni.CommandLockCell(lte); got != `AT+QNWLOCK="common/lte",1,2452,155` {
		t.Errorf("展锐 LTE 锁 = %q（频点在前）", got)
	}
	if got := uni.CommandLockCell(nr); got != `AT+QNWLOCK="common/5g",1,627264,521` {
		t.Errorf("展锐 NR 锁 = %q（频点在前，无 scs/band）", got)
	}

	old := quectel.QnwLockCommands{CellProfile: quectel.CellLegacyLTE}
	if got := old.CommandLockCell(lte); got != `AT+QNWLOCK="common/lte",2,2452,155` {
		t.Errorf("旧款 LTE 频点+PCI = %q", got)
	}
	if got := old.CommandLockCell(CellLockRequest{Tech: CellTechLTE, ARFCN: 2452, LockType: CellLockARFCN}); got != `AT+QNWLOCK="common/lte",1,2452,0` {
		t.Errorf("旧款 LTE 只锁频点 = %q（pci 位写 0）", got)
	}

	// 清单优先的平台判档
	for _, c := range []struct {
		model string
		want  quectel.CellLockProfile
	}{
		{"RM500U-CNV", quectel.CellUnisoc},
		{"RM520N-GL", quectel.CellQualcomm},
		{"EC25-AF", quectel.CellLegacyLTE},
	} {
		if got := quectel.CellLockProfileForModel(c.model); got != c.want {
			t.Errorf("qnwCellLockProfileForModel(%q) = %v, want %v", c.model, got, c.want)
		}
	}

	// applyCellProfile：无参构造器造出来的 locker（探测候选表），靠这里补平台档
	// ⚠️ 形参已从 *Modem 改成 iface.DeviceInfo（见 iface/device.go）——
	// 方言表只该拿到"型号"这种只读信息，不该握着整个内核对象。
	q := &quectel.QuectelCommand{}
	variant.ApplyCellProfile(q, iface.DeviceInfo{Model: "RM500U-CNV"})
	if got := q.CommandQueryCellLock(CellTechLTE); got != `AT+QNWLOCK="common/lte"` {
		t.Errorf("applyCellProfile 展锐 = %q", got)
	}
	if got := variant.ApplyCellProfile(&quectel.QuectelCommand{}, iface.DeviceInfo{}); got == nil {
		t.Error("零值 DeviceInfo 时不应返回 nil")
	}
	if qq, ok := variant.ApplyCellProfile(&quectel.QuectelCommand{}, iface.DeviceInfo{Model: "RM520N-GL"}).(*quectel.QuectelCommand); !ok || qq.CellProfile != quectel.CellQualcomm {
		t.Error("高通型号应保持默认档")
	}
}

// TestQcfgBandShape 接口形状：频段走 QCFG，锁小区/组网模式**沿用 QNW**（旧款上通常还在）
func TestQcfgBandShape(t *testing.T) {
	q := quectel.QcfgBandCommand()
	var l NetLocker = q
	if lockVendorOpsOf(l) == nil {
		t.Error("应实现 LockVendorOps")
	}
	if _, ok := l.(BandLockSetter); !ok {
		t.Error("应实现 BandLockSetter（QCFG band 要回读后整串改写）")
	}
	if got := q.CommandQueryBandLock(CellTechLTE); got != `AT+QCFG="band"` {
		t.Errorf("LTE 查询 = %q", got)
	}
	if got := q.CommandQueryBandLock(CellTechNR); got != "" {
		t.Errorf("NR 查询应为空，got %q", got)
	}
	if got := q.CommandSetBandLock(CellTechLTE, []int{1}); got != "" {
		t.Errorf("单条指令拼不出来 → 应为空，got %q", got)
	}
	// 锁小区 / 模式：继承自移远那套
	if got := q.CommandQueryCellLock(CellTechLTE); got != `AT+QNWLOCK="common/4g"` {
		t.Errorf("LTE 小区查询 = %q（应沿用 QNWLOCK）", got)
	}
	if got := q.CommandCellLockCaps(); got == "" {
		t.Error("小区能力探测应沿用 QNWLOCK=?")
	}
	if got := q.CommandQueryModePref(); got == "" {
		t.Error("组网模式应沿用 QNWPREFCFG=mode_pref")
	}
	if got := q.CommandSaveCellLock(); got != cmdQNWLockSave {
		t.Errorf("小区持久化 = %q, want %q", got, cmdQNWLockSave)
	}
	// 小区状态解析也用 QNWLOCK 的格式
	cl, ok := q.ParseCellLockState(CellTechLTE, []string{`+QNWLOCK: "common/4g",1,2452,155`, "OK"})
	if !ok || !cl.Locked || cl.ARFCN != 2452 || cl.PCID != 155 {
		t.Errorf("小区状态解析失败：%+v ok=%v", cl, ok)
	}
	// 没有能力查询形态
	if q.QueryBandCapability(&scriptedLockDoer{}) != nil {
		t.Error("无能力查询形态 → 应返回 nil")
	}
	if q.BandCapabilityNote(CellTechLTE) == "" {
		t.Error("拿不到能力时应有说明文案")
	}
}

// TestQcfgBandCandidate 候选表：探测指令/标记正确，且标记不会撞上别的 QCFG 子项
func TestQcfgBandCandidate(t *testing.T) {
	m := &Modem{ID: 0, Model: "EC25", ModelName: "EC25", cmdImpl: &common.UnknownCommand{}}
	var found *variant.LockCandidate
	for _, c := range m.lockCandidates() {
		if c.Name == "qcfgband" {
			cc := c
			found = &cc
		}
	}
	if found == nil {
		t.Fatal("候选里应有 qcfgband")
	}
	if found.Probe != `AT+QCFG="band"` || !strings.EqualFold(found.Mark, `+QCFG: "band"`) {
		t.Errorf("候选 = %+v", *found)
	}
	// 标记要够具体：`+QCFG: "usbnet",5` 这种不能被当成"接受 band 方言"
	if strings.Contains(strings.ToUpper(`+QCFG: "usbnet",5`), strings.ToUpper(found.Mark)) {
		t.Error("标记太宽，会撞上别的 QCFG 子项")
	}
	if lockFamilyLabels["qcfgband"] == "" {
		t.Error("缺界面显示名")
	}
}
