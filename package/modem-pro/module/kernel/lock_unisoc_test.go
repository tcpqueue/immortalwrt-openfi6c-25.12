package kernel

// 展锐（Unisoc）锁网方言的回归测试。
//
// 钉住两条线：
//  1. 已取证的部分照实上报/照实下发（LTE 锁频段 = AT^SYSCFGEX 第 5 字段的位图、5G 开关
//     AT+SP5GRAN、小区解锁 AT+SPCELLLOCK=0,0,0）；
//  2. 未取证的部分绝不下发（AT+SPCOMMLOCKBAND/AT+SPCOMMLOCKFRQ/AT+SPCELLLOCK 的目标值、
//     锁小区的 =1,... 形态、NR 锁频段）。谁要是"顺手"把猜测的写指令填进去，这些测试就会红。

import (
	"reflect"
	"strings"
	"testing"

	"modemd/module/common"
	"modemd/module/unisoc"
	"modemd/module/variant"
)

func TestUnisocLockerCapsCommands(t *testing.T) {
	l := &unisoc.UnisocLockCommand{}
	if got := l.CommandBandLockCaps(); got != "AT+SPCOMMLOCKBAND=?" {
		t.Errorf("频段锁定能力探测 = %q，应为实测可用的 AT+SPCOMMLOCKBAND=?", got)
	}
	if got := l.CommandCellLockCaps(); got != "AT+SPCELLLOCK=?" {
		t.Errorf("小区锁定能力探测 = %q，应为实测可用的 AT+SPCELLLOCK=?", got)
	}
	// 频段状态回读：LTE 走 AT^SYSCFGEX?（真机实测第 5 字段就是 LTE 频段位图，能读能写）；
	// NR 侧没有可读来源 ⇒ 空串（不编一个假状态）。
	if got := l.CommandQueryBandLock(CellTechLTE); got != "AT^SYSCFGEX?" {
		t.Errorf("LTE 频段锁定回读 = %q，应为实测可读的 AT^SYSCFGEX?", got)
	}
	if got := l.CommandQueryBandLock(CellTechNR); got != "" {
		t.Errorf("NR 频段没有可读来源，应为空串，实得 %q", got)
	}
	if got := l.CommandQueryCellLock(CellTechNR); got != "AT+SPCELLLOCK?" {
		t.Errorf("小区锁定回读 = %q", got)
	}
	if got := l.CommandQueryModePref(); got != "AT+SPRAT?" {
		t.Errorf("组网模式回读 = %q，应为唯一能回读的 AT+SPRAT?", got)
	}
	// 设备能力：取自固件在 AT^SYSCFGEX=? 里自己列的 LTE BC 名单（与鼎桥 MT5700 同型）
	if got := l.CommandQueryBandCapability(CellTechLTE); got != "AT^SYSCFGEX=?" {
		t.Errorf("LTE 能力频段查询 = %q，应为 AT^SYSCFGEX=?", got)
	}
	if got := l.CommandQueryBandCapability(CellTechNR); got != "" {
		t.Errorf("NR 能力频段查询应为空串（=? 只列 LTE BC），实得 %q", got)
	}
}

// ⚠️⚠️ 最要紧的一条：**任何锁写指令都不许下发**。
//
// 参数语义未取证（`(2,12,13,14,15)` 到底是什么码没有证据），写错会直接把模组锁出服务。
func TestUnisocLockerNeverWrites(t *testing.T) {
	l := &unisoc.UnisocLockCommand{}
	if got := l.CommandSetBandLock(CellTechLTE, []int{1, 3, 5}); got != "" {
		t.Errorf("未取证却下发了 LTE 锁频段指令：%q", got)
	}
	if got := l.CommandSetBandLock(CellTechNR, []int{78}); got != "" {
		t.Errorf("未取证却下发了 NR 锁频段指令：%q", got)
	}
	if got := l.CommandLockCell(CellLockRequest{Tech: CellTechLTE, ARFCN: 2452, PCID: 155, LockType: CellLockARFCNPCI}); got != "" {
		t.Errorf("未取证却下发了锁小区指令：%q", got)
	}
	if got := l.CommandLockCell(CellLockRequest{Tech: CellTechLTE, ARFCN: 1850, LockType: CellLockARFCN}); got != "" {
		t.Errorf("未取证却下发了按频点锁小区指令：%q", got)
	}
	// 解除是**实测可用**的（回 +SPCELLLOCK:0,0,0,0，且发它不影响驻网）⇒ 必须下发；
	// 这是本机唯一一个可以放心点的锁网按钮（用户自己锁过之后靠它把模组拉回来）。
	if got := l.CommandLockCell(CellLockRequest{Tech: CellTechLTE, LockType: CellLockNone}); got != unisoc.CmdSPCellLockClear {
		t.Errorf("解除小区锁定应下发 %q（真机已验证），实际 %q", unisoc.CmdSPCellLockClear, got)
	}
	if got := l.CommandLockCell(CellLockRequest{Tech: CellTechNR, LockType: CellLockNone}); got != unisoc.CmdSPCellLockClear {
		t.Errorf("NR 侧解除也应下发 %q，实际 %q", unisoc.CmdSPCellLockClear, got)
	}
	if got := l.CommandSetModePref("LTE"); got != "AT+SP5GRAN=0" {
		t.Errorf("仅 4G 应下发 AT+SP5GRAN=0（真机已验证），实际 %q", got)
	}
	if got := l.CommandSetModePref("LTE:NR5G"); got != "AT+SP5GRAN=1" {
		t.Errorf("5G+4G 应下发 AT+SP5GRAN=1（真机已验证），实际 %q", got)
	}
	if got := l.CommandSetModePref("NR5G"); got != "" {
		t.Errorf("本机没有「仅 5G」档位（AT+SP5GRAN 只有开/关），不该下发任何指令：%q", got)
	}
	if got := l.CommandUnlockBand(CellTechLTE); got != "" {
		t.Errorf("解锁写法未取证，不该下发：%q", got)
	}
	if got := l.CommandSaveCellLock(); got != "" {
		t.Errorf("本机没有小区锁定，不该有持久化指令：%q", got)
	}
	if got := l.CommandQueryPolicyBand(); got != "" {
		t.Errorf("没有可验证的 policy 频段查询，不该下发：%q", got)
	}
	if got := l.CommandQueryBandCapability(CellTechNR); got != "" {
		t.Errorf("NR 能力频段查询未取证，不该下发：%q", got)
	}
	if l.QueryBandCapability(nil) != nil {
		t.Error("QueryBandCapability 没有 AT 通道时应返回 nil（拿不到就别编）")
	}
}

// 说明文案必须够用（界面直接显示）：讲清"能读什么、为什么不能写、怎么自己试"。
func TestUnisocLockerNotes(t *testing.T) {
	l := &unisoc.UnisocLockCommand{}
	note := l.StatusNote()
	if note == "" {
		t.Fatal("锁网卡片必须带说明（否则用户只看到一排失败探测、不知道原因）")
	}
	for _, must := range []string{"AT+SPRAT?", "AT+SPCOMMLOCKBAND", "CME ERROR: 21", "AT 指令"} {
		if !strings.Contains(note, must) {
			t.Errorf("说明里应当提到 %q，实际：%s", must, note)
		}
	}
	// 项目约定：这些文案是纯文本渲染的，不能出现 markdown 记号
	if strings.Contains(note, "**") || strings.Contains(note, "`") {
		t.Errorf("说明里不能出现 markdown 记号（界面上会原样显示）：%s", note)
	}
	if got := l.BandCapabilityNote(CellTechLTE); got == "" {
		t.Error("「设备能力」那一列拿不到时也要说明原因")
	}
}

// LTE 锁频段：位图 ↔ 频段号（位图约定 bit(频段-1)；固件出厂值 1e200000095 就是那 9 个频段）
func TestUnisocLTEBandMask(t *testing.T) {
	bands := []int{1, 3, 5, 8, 34, 38, 39, 40, 41}
	mask, err := unisoc.UnisocLTEBandMask(bands)
	if err != nil || mask != "1E200000095" {
		t.Fatalf("掩码 = %q err=%v，want 1E200000095（= 真机出厂值，见 AT^SYSCFGEX?）", mask, err)
	}
	got, ok := unisoc.ParseUnisocLTEBands("1e200000095")
	if !ok || !reflect.DeepEqual(got, bands) {
		t.Errorf("位图解析 = %v ok=%v，want %v", got, ok, bands)
	}
	// 去掉 B38（bit 37）= 1C200000095 —— 就是真机实测写下去、能原样回读的那个值
	if m, _ := unisoc.UnisocLTEBandMask([]int{1, 3, 5, 8, 34, 39, 40, 41}); m != "1C200000095" {
		t.Errorf("去 B38 的掩码 = %q，want 1C200000095", m)
	}
	// "All bands"（固件模板里那个值）= 未限制：返回空清单，而不是"锁了 63 个频段"
	if b, ok := unisoc.ParseUnisocLTEBands("7FFFFFFFFFFFFFFF"); !ok || len(b) != 0 {
		t.Errorf("All bands 应解析为未限制（空清单），实得 %v ok=%v", b, ok)
	}
	// 空列表必须拒绝：写 0 = 一个频段都不允许（实测会直接脱网）
	if _, err := unisoc.UnisocLTEBandMask(nil); err == nil {
		t.Error("空频段表必须拒绝下发")
	}
	// 位图是 64 位：71 这类超范围频段号要报错，不能静默丢
	if _, err := unisoc.UnisocLTEBandMask([]int{71}); err == nil {
		t.Error("频段 71 超出 64 位位图，应报错")
	}
	if _, ok := unisoc.ParseUnisocLTEBands("zz"); ok {
		t.Error("非法掩码不该解析成功")
	}
	if _, ok := unisoc.ParseUnisocLTEBands(""); ok {
		t.Error("空掩码不该解析成功")
	}
}

// 写锁必须"先回读再只换位图"：第 2 个字段是 2G/3G 频带码，从零拼会把它写坏（实测会掉网）
func TestUnisocSetBandLockRebuildsSyscfgEX(t *testing.T) {
	d := &scriptedLockDoer{replies: map[string][]string{
		"AT^SYSCFGEX?": {`^SYSCFGEX: "00",2000000400000,0,2,1e200000095`, "OK"},
	}}
	l := &unisoc.UnisocLockCommand{}
	if err := l.SetBandLockAt(d, CellTechLTE, []int{3, 5}); err != nil {
		t.Fatal(err)
	}
	want := `AT^SYSCFGEX="00",2000000400000,0,2,14`
	if len(d.lockCmds) != 1 || d.lockCmds[0] != want {
		t.Fatalf("下发 = %v，want [%s]（只换第 5 个字段，其余原样带回去）", d.lockCmds, want)
	}
	// NR 侧没有可写来源 ⇒ 报错，不发指令
	if err := l.SetBandLockAt(d, CellTechNR, []int{78}); err == nil {
		t.Error("NR 锁频段没有取证，应报错")
	}
	// 空频段表 ⇒ 报错（不下发空清单）
	if err := l.SetBandLockAt(d, CellTechLTE, nil); err == nil {
		t.Error("空频段表应报错")
	}
	if len(d.lockCmds) != 1 {
		t.Errorf("只应下发那一条改写指令，实得 %v", d.lockCmds)
	}
	// 回读不到当前配置 ⇒ 拒绝改写（宁可报错也不把其他字段写坏）
	d2 := &scriptedLockDoer{}
	if err := l.SetBandLockAt(d2, CellTechLTE, []int{3}); err == nil {
		t.Error("AT^SYSCFGEX? 拿不到时应拒绝改写")
	}
	if len(d2.lockCmds) != 0 {
		t.Errorf("拒绝改写时不应下发任何指令：%v", d2.lockCmds)
	}
}

// 解锁 = 写回**固件声明的全部频段**（不用没验证过的 "All bands"）
func TestUnisocUnlockBandWritesDeclaredBands(t *testing.T) {
	syscfgEXCaps := `^SYSCFGEX: "00","01","02","03","04","07","08","99",(-band-),(0-3),(0-2),` +
		`((LTE BC1/LTE BC3/LTE BC5/LTE BC8/LTE BC34/LTE BC38/LTE BC39/LTE BC40/LTE BC41/),(7FFFFFFFFFFFFFFF, "All bands"))`
	d := &scriptedLockDoer{replies: map[string][]string{
		"AT^SYSCFGEX=?": {syscfgEXCaps, "OK"},
		"AT^SYSCFGEX?":  {`^SYSCFGEX: "00",2000000400000,0,2,14`, "OK"},
	}}
	l := &unisoc.UnisocLockCommand{}
	if err := l.UnlockBandAt(d, CellTechLTE); err != nil {
		t.Fatal(err)
	}
	want := `AT^SYSCFGEX="00",2000000400000,0,2,1E200000095`
	if len(d.lockCmds) != 1 || d.lockCmds[0] != want {
		t.Fatalf("解锁下发 = %v，want [%s]", d.lockCmds, want)
	}
	// 拿不到声明名单 ⇒ 拒绝解锁（不能拿 7FFFFFFFFFFFFFFF 或 0 去试）
	d2 := &scriptedLockDoer{}
	if err := l.UnlockBandAt(d2, CellTechLTE); err == nil {
		t.Error("拿不到声明的频段名单时应拒绝解锁")
	}
	if len(d2.lockCmds) != 0 {
		t.Errorf("拒绝解锁时不应下发任何指令：%v", d2.lockCmds)
	}
	if err := l.UnlockBandAt(d2, CellTechNR); err == nil {
		t.Error("NR 本来就没锁，不应下发")
	}
}

// 设备能力：主来源 AT^SYSCFGEX=? 的 LTE BC 名单；备来源 AT+SPBANDCTRL=0,0,0,0（组 4 ∪ 组 5）
func TestUnisocBandCapabilitySources(t *testing.T) {
	syscfgEXCaps := `^SYSCFGEX: "00","01","02","03","04","07","08","99",(-band-),(0-3),(0-2),` +
		`((LTE BC1/LTE BC3/LTE BC5/LTE BC8/LTE BC34/LTE BC38/LTE BC39/LTE BC40/LTE BC41/),(7FFFFFFFFFFFFFFF, "All bands"))`
	want := []int{1, 3, 5, 8, 34, 38, 39, 40, 41}
	l := &unisoc.UnisocLockCommand{}

	d := &scriptedLockDoer{replies: map[string][]string{"AT^SYSCFGEX=?": {syscfgEXCaps, "OK"}}}
	caps := l.QueryBandCapability(d)
	if !reflect.DeepEqual(caps[CellTechLTE], want) {
		t.Errorf("LTE 能力 = %v，want %v", caps[CellTechLTE], want)
	}
	if _, ok := caps[CellTechNR]; ok {
		t.Error("NR 能力不该凭空出现（=? 只列 LTE）")
	}

	// =? 拿不到（回 ERROR）→ 退回 SPBANDCTRL 的组 4 ∪ 组 5（真机应答逐字照抄）
	d2 := &scriptedLockDoer{replies: map[string][]string{
		unisoc.CmdSPBandCtrlQuery: {
			"+SPBANDCTRL: 1,0,1,3,4", "+SPBANDCTRL: 2,A,F", "+SPBANDCTRL: 3,1,8",
			"+SPBANDCTRL: 4,34,38,39,40,41", "+SPBANDCTRL: 5,1,3,5,8", "OK",
		},
	}}
	if caps2 := l.QueryBandCapability(d2); !reflect.DeepEqual(caps2[CellTechLTE], want) {
		t.Errorf("备用来源的 LTE 能力 = %v，want %v", caps2[CellTechLTE], want)
	}

	// 两个来源都拿不到 → nil（界面显示 BandCapabilityNote）
	if caps3 := l.QueryBandCapability(&scriptedLockDoer{}); caps3 != nil {
		t.Errorf("两个来源都拿不到时应返回 nil，实得 %v", caps3)
	}

	// 解析器：组 2 的 A/F 是十六进制（TD-SCDMA），不能把整行丢掉，也不该影响 LTE
	g := unisoc.ParseSPBandCtrl([]string{"+SPBANDCTRL: 2,A,F", "+SPBANDCTRL: 4,34,38", "OK"})
	if !reflect.DeepEqual(g[2], []int{10, 15}) {
		t.Errorf("组 2 = %v，want [10 15]（A/F 按十六进制解析）", g[2])
	}
	if gotLT := unisoc.SPBandCtrlLTEBands([]string{"+SPBANDCTRL: 4,34,38", "+SPBANDCTRL: 5,5,1,3"}); !reflect.DeepEqual(gotLT, []int{1, 3, 5, 34, 38}) {
		t.Errorf("LTE = %v，want [1 3 5 34 38]（组 4 ∪ 组 5，升序去重）", gotLT)
	}
	if gotGarbage := unisoc.ParseSPBandCtrl([]string{"ERROR", "+SPBANDCTRL:", "OK"}); len(gotGarbage) != 0 {
		t.Errorf("垃圾应答不该解析出内容：%v", gotGarbage)
	}
}

// （真机双向验证：SP5GRAN 1 ↔ LTE 32、0 ↔ LTE 16），没取证的数值原样返回。
// 组网模式：`+SPRAT` 回读的两个**实测对得上**的取值要翻成语义档位
// （真机双向验证：SP5GRAN 1 ↔ LTE 32、0 ↔ LTE 16），没取证的数值原样返回。
func TestUnisocLockerModePrefParse(t *testing.T) {
	l := &unisoc.UnisocLockCommand{}
	v, ok := l.ParseModePref([]string{"+SPRAT: LTE 32", "OK"})
	if !ok || v != common.ModeLTENR5G {
		t.Errorf("ParseModePref(LTE 32) = %q ok=%v，应为 %q/true（5G 已启用）", v, ok, common.ModeLTENR5G)
	}
	if v, ok := l.ParseModePref([]string{"+SPRAT: LTE 16", "OK"}); !ok || v != common.ModeLTE {
		t.Errorf("ParseModePref(LTE 16) = %q ok=%v，应为 %q/true（仅 4G）", v, ok, common.ModeLTE)
	}
	// 没取证的数值：原样显示，不猜含义
	if v, ok := l.ParseModePref([]string{"+SPRAT: LTE 64", "OK"}); !ok || v != "LTE 64" {
		t.Errorf("ParseModePref(LTE 64) = %q ok=%v，应原样返回 LTE 64/true", v, ok)
	}
	if _, ok := l.ParseModePref([]string{"+SPRAT:()", "OK"}); ok {
		t.Error("设备不枚举（+SPRAT:()）时不该当成读到值")
	}
	// 不实现 SetModePrefVendor：交回上层，上层走 CommandSetModePref（AT+SP5GRAN）
	if handled, err := l.SetModePrefVendor(nil, "LTE"); handled || err != nil {
		t.Errorf("SetModePrefVendor 应为 (false, nil)，实际 (%v, %v)", handled, err)
	}
}

// 接线：展锐 SIPC 通道上，候选表必须把 unisoc 排在**第一个**（别白试 13 套方言）；
// 其它通道一个字都不能变。
func TestUnisocLockCandidateOrder(t *testing.T) {
	mod := &Modem{ATPort: "/dev/stty_nr2", cmdImpl: NewVendorCommand(VendorUnisoc)}
	list := mod.lockCandidates()
	if len(list) == 0 || list[0].Name != "unisoc" {
		t.Fatalf("展锐 SIPC 上第一候选必须是 unisoc，实际 %+v", names(list))
	}
	if list[0].Probe != unisoc.CmdSPCellLockCaps {
		t.Errorf("候选探针 = %q，应为实测可用的 %q", list[0].Probe, unisoc.CmdSPCellLockCaps)
	}
	for _, c := range list[1:] {
		if c.Name == "unisoc" {
			t.Error("unisoc 不该在候选表里重复出现")
		}
	}

	// 普通串口：不许出现 unisoc 这一套（它们的方言表里没有它，多试一条 = 白等一轮）
	for _, port := range []string{"/dev/ttyUSB0", "/dev/ttyACM1", "/dev/mhi_DUN", "COM3", ""} {
		other := &Modem{ATPort: port, cmdImpl: NewVendorCommand(VendorUnknown)}
		for _, c := range other.lockCandidates() {
			if c.Name == "unisoc" {
				t.Errorf("%q 不是展锐 SIPC，不该有 unisoc 候选", port)
			}
		}
		// 回退猜测也不能落到展锐方言上
		if _, ok := other.preferredLocker().(*unisoc.UnisocLockCommand); ok {
			t.Errorf("%q 的回退方言不该是展锐 SP*", port)
		}
	}

	// 展锐 SIPC 的回退方言就是它
	if _, ok := mod.preferredLocker().(*unisoc.UnisocLockCommand); !ok {
		t.Error("展锐 SIPC 的回退方言应为 unisoc.UnisocLockCommand")
	}
}

func names(list []variant.LockCandidate) []string {
	out := make([]string, 0, len(list))
	for _, c := range list {
		out = append(out, c.Name)
	}
	return out
}
