package kernel

import (
	"testing"

	"modemd/module/common"
	"modemd/module/fibocom"
	"modemd/module/platform"
)

// ====================== 广和通 FM350-GL（MTK, AT+GTACT）锁网 ======================

// TestMTKBandCode 频段号 ↔ AT+GTACT 私有编码：
//
//	LTE = 100+band（B1→101、B71→171）；NR = "50"+band（n1→501、n78→5078、n512→50512）
func TestMTKBandCode(t *testing.T) {
	cases := []struct {
		tech CellTech
		band int
		code string
	}{
		{CellTechLTE, 1, "101"},
		{CellTechLTE, 3, "103"},
		{CellTechLTE, 71, "171"},
		{CellTechNR, 1, "501"},
		{CellTechNR, 9, "509"},
		{CellTechNR, 10, "5010"},
		{CellTechNR, 41, "5041"},
		{CellTechNR, 78, "5078"},
		{CellTechNR, 257, "50257"},
		{CellTechNR, 512, "50512"},
	}
	for _, c := range cases {
		if got := fibocom.MtkBandCode(c.tech, c.band); got != c.code {
			t.Errorf("mtkBandCode(%s,%d) = %q, want %q", c.tech, c.band, got, c.code)
		}
	}
}

// TestMTKBandFromCode 编码反解（认不出的编码要老实返回 false，不能乱猜）
func TestMTKBandFromCode(t *testing.T) {
	cases := []struct {
		code int
		tech CellTech
		band int
		ok   bool
	}{
		{1, CellTechWCDMA, 1, true}, // UMTS B1（裸数字）
		{8, CellTechWCDMA, 8, true},
		{101, CellTechLTE, 1, true},
		{171, CellTechLTE, 71, true},
		{501, CellTechNR, 1, true},
		{5078, CellTechNR, 78, true},
		{50512, CellTechNR, 512, true},
		{200, "", 0, false},   // 200~500 之间没有定义
		{99999, "", 0, false}, // 不以 50 开头
	}
	for _, c := range cases {
		tech, band, ok := fibocom.MtkBandFromCode(c.code)
		if ok != c.ok || (ok && (tech != c.tech || band != c.band)) {
			t.Errorf("mtkBandFromCode(%d) = (%s,%d,%v), want (%s,%d,%v)",
				c.code, tech, band, ok, c.tech, c.band, c.ok)
		}
	}
}

// TestParseMTKGTACT 解析 `+GTACT: <rat>,<act1>,<act2>,<band…>`：
// PreferredAct 取值只有 2/3/6（<101），频段编码都 ≥101，按值分流，不写死下标。
func TestParseMTKGTACT(t *testing.T) {
	// 空 PreferredAct + 两个 NR 频段
	g, ok := fibocom.ParseMTKGTACT([]string{"AT+GTACT?", "+GTACT: 20,,,5041,5078", "OK"})
	if !ok {
		t.Fatal("应解析成功")
	}
	if g.Rat != 20 || g.Act1 != "" || g.Act2 != "" {
		t.Fatalf("rat/act 解析不对：%+v", g)
	}
	if got := g.BandsFor(CellTechNR); len(got) != 2 || got[0] != 41 || got[1] != 78 {
		t.Fatalf("NR 频段 = %v, want [41 78]", got)
	}
	if got := g.BandsFor(CellTechLTE); len(got) != 0 {
		t.Fatalf("LTE 频段应为空：%v", got)
	}

	// 带 PreferredAct1 + LTE 频段
	g2, ok := fibocom.ParseMTKGTACT([]string{"+GTACT: 2,3,,101,103"})
	if !ok || g2.Rat != 2 || g2.Act1 != "3" {
		t.Fatalf("解析不对：%+v ok=%v", g2, ok)
	}
	if got := g2.BandsFor(CellTechLTE); len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("LTE 频段 = %v, want [1 3]", got)
	}

	// 没有 +GTACT 行 → false
	if _, ok := fibocom.ParseMTKGTACT([]string{"OK"}); ok {
		t.Error("没有 +GTACT 行时不该 ok")
	}

	// 真机样例（QModem `fibocom_gtact_lock_params`）：20,6,3,1,5,8,101,105,5041,5078
	// rat=20、PreferredAct1=6、PreferredAct2=3，频段含 UMTS(1,5,8)+LTE(101,105)+NR(5041,5078)
	g3, ok := fibocom.ParseMTKGTACT([]string{"+GTACT: 20,6,3,1,5,8,101,105,5041,5078"})
	if !ok || g3.Rat != 20 || g3.Act1 != "6" || g3.Act2 != "3" {
		t.Fatalf("真机样例 rat/act 解析不对：%+v ok=%v", g3, ok)
	}
	if got := g3.BandsFor(CellTechWCDMA); len(got) != 3 || got[0] != 1 || got[2] != 8 {
		t.Errorf("UMTS 频段 = %v, want [1 5 8]", got)
	}
	if got := g3.BandsFor(CellTechLTE); len(got) != 2 || got[0] != 1 || got[1] != 5 {
		t.Errorf("LTE 频段 = %v, want [1 5]", got)
	}
	if got := g3.BandsFor(CellTechNR); len(got) != 2 || got[0] != 41 || got[1] != 78 {
		t.Errorf("NR 频段 = %v, want [41 78]", got)
	}
	// 改写 NR 时必须保留 UMTS + LTE 的编码（不能把 WCDMA 频段清空）
	kept := g3.KeepOtherTech(CellTechNR)
	if len(kept) != 5 {
		t.Errorf("非 NR 频段应保留 5 个（UMTS 1/5/8 + LTE 101/105），实际 %v", kept)
	}
}

// TestMTKGtactCommand AT+GTACT 的三种参数形式（上游 vendor/fibocom.sh 逐字）：
//
//	联发科（FM350）           → 17,6,6,<lte>,<nr>（清掉 UMTS 组）
//	高通非 FM150（FM160 等）  → 17,,,<lte>,<nr>
//	其余（展锐 FM650 / lte）  → <act>,6,3,<umts>,<lte>,<nr>，单制式时 <act>,,,<bands>
func TestMTKGtactCommand(t *testing.T) {
	g := fibocom.MtkGtact{Rat: 20, Act1: "", Act2: "", Bands: []int{1, 8, 101, 103, 5078}} // UMTS B1/B8 + LTE B1/B3 + NR n78

	// ① 联发科：锁 NR 时带上 LTE+NR，UMTS 被清掉
	got := fibocom.GtactLockCommand(fibocom.GtactFormMediaTek, g, CellTechNR, []int{41, 78})
	if got != "AT+GTACT=17,6,6,101,103,5041,5078" {
		t.Errorf("联发科形式 = %q", got)
	}
	// ② 高通（非 FM150，FM160-CN 就是这种）：空字段形式
	got = fibocom.GtactLockCommand(fibocom.GtactFormEmptyFields, g, CellTechNR, []int{41})
	if got != "AT+GTACT=17,,,101,103,5041" {
		t.Errorf("高通空字段形式 = %q", got)
	}
	// ③ 其余（展锐/lte）：<act>,6,3,… —— act 由"哪些制式有频段"算（这里三个都有 = 20）
	got = fibocom.GtactLockCommand(fibocom.GtactFormAct63, g, CellTechNR, []int{41})
	if got != "AT+GTACT=20,6,3,1,8,101,103,5041" {
		t.Errorf("默认形式 = %q", got)
	}
	// ④ 默认形式、只剩一个制式有频段 → 用 <act>,,, 空段形式
	onlyLTE := fibocom.MtkGtact{Rat: 20, Bands: []int{101}}
	got = fibocom.GtactLockCommand(fibocom.GtactFormAct63, onlyLTE, CellTechLTE, []int{3})
	if got != "AT+GTACT=2,,,103" {
		t.Errorf("单制式形式 = %q, want AT+GTACT=2,,,103", got)
	}
	// ⑤ 解锁（该制式频段传空）也要是合法形式
	got = fibocom.GtactLockCommand(fibocom.GtactFormEmptyFields, g, CellTechLTE, nil)
	if got != "AT+GTACT=17,,,5078" {
		t.Errorf("解锁形式 = %q", got)
	}
	if got = fibocom.GtactLockCommand(fibocom.GtactFormAct63, fibocom.MtkGtact{}, CellTechLTE, nil); got != "AT+GTACT=20" {
		t.Errorf("全空时应只发档位号，得到 %q", got)
	}
}

// TestFibocomGTACTFormFor 形式按平台/型号选（上游 fibocom_uses_empty_gtact_fields：qualcomm 且非 FM150）
func TestFibocomGTACTFormFor(t *testing.T) {
	cases := []struct {
		model string
		plat  platform.ModemPlatform
		want  fibocom.FibocomGTACTForm
	}{
		{"FM350-GL", platform.PlatformMediaTek, fibocom.GtactFormMediaTek},
		{"FM160-CN", platform.PlatformQualcomm, fibocom.GtactFormEmptyFields},
		{"FM190W-GL", platform.PlatformQualcomm, fibocom.GtactFormEmptyFields},
		{"FM150-AE", platform.PlatformQualcomm, fibocom.GtactFormAct63}, // 上游唯一点名的例外
		{"FM650-CN", platform.PlatformUnisoc, fibocom.GtactFormAct63},
		{"NL668", platform.PlatformUnknown, fibocom.GtactFormAct63}, // lte 平台（旧款 4G）→ 默认形式
	}
	for _, c := range cases {
		mod := &Modem{Model: c.model, ModelName: c.model}
		if got := fibocom.FibocomGTACTFormForModel(mod.Model + " " + mod.ModelName); got != c.want {
			t.Errorf("%s（%s）：形式 = %d, want %d", c.model, c.plat, got, c.want)
		}
	}
}

// TestMTKLockerShape MTK 锁网族的接口形状：有锁频段、无锁小区、无组网模式查询。
func TestMTKLockerShape(t *testing.T) {
	f := &fibocom.FibocomMTKCommand{}
	if common.NetLockerOf(f) == nil {
		t.Fatal("fibocom.FibocomMTKCommand 应实现 NetLocker")
	}
	if lockVendorOpsOf(f) == nil {
		t.Fatal("fibocom.FibocomMTKCommand 应实现 LockVendorOps（应答格式与移远不同）")
	}
	if _, ok := common.NetLockerOf(f).(BandLockSetter); !ok {
		t.Fatal("fibocom.FibocomMTKCommand 应实现 BandLockSetter（先回读再整串改写）")
	}
	if got := f.CommandQueryBandLock(CellTechNR); got != "AT+GTACT?" {
		t.Errorf("频段查询 = %q, want AT+GTACT?", got)
	}
	if got := f.CommandBandLockCaps(); got != "AT+GTACT=?" {
		t.Errorf("频段能力探测 = %q, want AT+GTACT=?", got)
	}
	// 锁小区：AT+GTCELLLOCK（手册没写、真机固件有；依据 QModem cmds/fibocom.sh）
	if got := f.CommandQueryCellLock(CellTechNR); got != "AT+GTCELLLOCK?" {
		t.Errorf("锁小区查询 = %q, want AT+GTCELLLOCK?", got)
	}
	if got := f.CommandCellLockCaps(); got != "AT+GTCELLLOCK=?" {
		t.Errorf("锁小区能力探测 = %q, want AT+GTCELLLOCK=?", got)
	}
	if got := f.CommandQueryModePref(); got != "" {
		t.Errorf("组网模式查询应为空（随 AT+GTACT 管理），got %q", got)
	}
	// 单条拼不出来 → 空串，交给 BandLockSetter
	if got := f.CommandSetBandLock(CellTechNR, []int{78}); got != "" {
		t.Errorf("CommandSetBandLock 应为空（走 SetBandLockAt），got %q", got)
	}
}

// TestLockerForFibocom 广和通按机型分派：FM350（MTK）→ AT+GTACT；FM160（高通）→ QNW。
// 这是用户实测"AT+QNWPREFCFG 在 FM350 回 ERROR"的修复点：FM350 不该再收到 QNW 指令。
func TestLockerForFibocom(t *testing.T) {
	mtkMod := &Modem{ID: 0, Vendor: VendorFibocom,
		Model: "ATI|Manufacturer: Fibocom Wireless Inc.|Model: FM350-GL|OK", ModelName: "FM350-GL",
		cmdImpl: &fibocom.FibocomCommand{}}
	l := mtkMod.lockerFor()
	if _, ok := l.(*fibocom.FibocomMTKCommand); !ok {
		t.Fatalf("FM350 应分派到 MTK(AT+GTACT) 实现，实际 %T", l)
	}
	if got := l.CommandQueryBandLock(CellTechNR); got != "AT+GTACT?" {
		t.Errorf("FM350 频段查询 = %q, want AT+GTACT?", got)
	}

	qcMod := &Modem{ID: 0, Vendor: VendorFibocom, Model: "FM160-EAU", ModelName: "FM160-EAU", cmdImpl: &fibocom.FibocomCommand{}}
	lq := qcMod.lockerFor()
	if _, ok := lq.(*fibocom.FibocomMTKCommand); ok {
		t.Fatal("FM160（高通）不该分派到 MTK 实现")
	}
	if got := lq.CommandQueryBandLock(CellTechLTE); got != `AT+QNWPREFCFG="lte_band"` {
		t.Errorf("FM160 频段查询 = %q, want QNW", got)
	}
}

// TestIsFibocomMTK 机型判定
func TestIsFibocomMTK(t *testing.T) {
	if !isFibocomMTK("Manufacturer: Fibocom|Model: FM350-GL|OK") {
		t.Error("FM350 应判为 MTK")
	}
	for _, m := range []string{"FM160-EAU", "FM150", "FM650-CN", ""} {
		if isFibocomMTK(m) {
			t.Errorf("%q 不该判为 MTK", m)
		}
	}
}

// TestMTKCellLockCommands 锁小区指令拼装（格式依据 QModem `vendor/fibocom.sh` 实机验证）：
//
//	AT+GTCELLLOCK=<状态>,<cell_type 1NR/0LTE>,<lock_type 1仅频点/0频点+PCI>,<arfcn>[,<pci>[,<scs>,<band>]]
func TestMTKCellLockCommands(t *testing.T) {
	f := &fibocom.FibocomMTKCommand{}
	cases := []struct {
		name string
		req  CellLockRequest
		want string
	}{
		{"解锁", CellLockRequest{Tech: CellTechNR, LockType: CellLockNone}, "AT+GTCELLLOCK=0"},
		{"NR 仅频点", CellLockRequest{Tech: CellTechNR, ARFCN: 627264, LockType: CellLockARFCN}, "AT+GTCELLLOCK=1,1,1,627264"},
		{"LTE 仅频点", CellLockRequest{Tech: CellTechLTE, ARFCN: 2452, LockType: CellLockARFCN}, "AT+GTCELLLOCK=1,0,1,2452"},
		{"NR 频点+PCI", CellLockRequest{Tech: CellTechNR, ARFCN: 627264, PCID: 521, LockType: CellLockARFCNPCI}, "AT+GTCELLLOCK=1,1,0,627264,521,1,78"},
		{"LTE 频点+PCI", CellLockRequest{Tech: CellTechLTE, ARFCN: 2452, PCID: 155, LockType: CellLockARFCNPCI}, "AT+GTCELLLOCK=1,0,0,2452,155"},
	}
	for _, c := range cases {
		if got := f.CommandLockCell(c.req); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}

	// 状态解析：NR 频点+PCI
	cl, ok := f.ParseCellLockState(CellTechNR, []string{"AT+GTCELLLOCK?", "+GTCELLLOCK: 1,1,0,627264,521,1,78", "OK"})
	if !ok || !cl.Locked || cl.ARFCN != 627264 || cl.PCID != 521 || cl.LockType != CellLockARFCNPCI {
		t.Fatalf("NR 解析 = %+v ok=%v", cl, ok)
	}
	// cell_type=1 是 NR → 对 LTE 应判为未锁定
	if lte, _ := f.ParseCellLockState(CellTechLTE, []string{"+GTCELLLOCK: 1,1,0,627264,521,1,78"}); lte.Locked {
		t.Errorf("cell_type=1 是 NR，LTE 不该判为已锁：%+v", lte)
	}
	// 仅频点
	if cl2, _ := f.ParseCellLockState(CellTechLTE, []string{"+GTCELLLOCK: 1,0,1,2452"}); !cl2.Locked || cl2.LockType != CellLockARFCN || cl2.ARFCN != 2452 {
		t.Errorf("LTE 仅频点解析 = %+v", cl2)
	}
	// 未锁定
	if cl3, _ := f.ParseCellLockState(CellTechNR, []string{"+GTCELLLOCK: 0"}); cl3.Locked {
		t.Errorf("status=0 不该判为已锁：%+v", cl3)
	}
}
