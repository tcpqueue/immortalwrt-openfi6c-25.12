package kernel

import (
	"strings"
	"testing"

	"modemd/module/common"
	"modemd/module/gosuncn"
	"modemd/module/platform"
)

// ====================== 高新兴 Gosuncn 锁网（AT+ZBAND / AT+ZLOCKCELL） ======================

// TestGosuncnBandCommands 锁频段：AT+ZBAND=<rat>,<num>,<b1,b2,…>（1=LTE、5=NR5G、0=解锁）
func TestGosuncnBandCommands(t *testing.T) {
	g := &gosuncn.GosuncnLockCommand{}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"query band", g.CommandQueryBandLock(CellTechLTE), "AT+ZBAND?"},
		{"set lte", g.CommandSetBandLock(CellTechLTE, []int{1, 3, 5}), "AT+ZBAND=1,3,1,3,5"},
		{"set nr", g.CommandSetBandLock(CellTechNR, []int{78, 41}), "AT+ZBAND=5,2,78,41"},
		{"empty", g.CommandSetBandLock(CellTechNR, nil), ""},
		// 解锁不能拼单条指令：两种形态（AT+ZBAND=0 / AT+ZBAND=all,all,all,all）要先回读判定
		{"unlock", g.CommandUnlockBand(CellTechNR), ""},
		{"caps", g.CommandBandLockCaps(), "AT+ZBAND=?"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestGosuncnCellCommands 锁小区：LTE =1,1,<earfcn>,<pci>；NR =1,2,<narfcn>,<pci>,<scs>,<band>；解锁 =0
func TestGosuncnCellCommands(t *testing.T) {
	g := &gosuncn.GosuncnLockCommand{}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"query", g.CommandQueryCellLock(CellTechNR), "AT+ZLOCKCELL?"},
		{"lock lte", g.CommandLockCell(CellLockRequest{Tech: CellTechLTE, ARFCN: 2452, PCID: 155, LockType: CellLockARFCNPCI}), "AT+ZLOCKCELL=1,1,2452,155"},
		{"lock nr", g.CommandLockCell(CellLockRequest{Tech: CellTechNR, ARFCN: 627264, PCID: 521, LockType: CellLockARFCNPCI}), "AT+ZLOCKCELL=1,2,627264,521,1,78"},
		{"unlock", g.CommandLockCell(CellLockRequest{Tech: CellTechLTE, LockType: CellLockNone}), "AT+ZLOCKCELL=0"},
		// 两种写法都必须带 PCI → 只锁频点不支持
		{"arfcn only unsupported", g.CommandLockCell(CellLockRequest{Tech: CellTechLTE, ARFCN: 2452, LockType: CellLockARFCN}), ""},
		{"caps", g.CommandCellLockCaps(), "AT+ZLOCKCELL?"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestGosuncnParseCellLockState 尽力解析（应答前缀未逐字确认 → 按含 LTE/NR5G 的行 + 行内数字）
func TestGosuncnParseCellLockState(t *testing.T) {
	g := &gosuncn.GosuncnLockCommand{}

	lte, ok := g.ParseCellLockState(CellTechLTE, []string{"AT+ZLOCKCELL?", "+ZLOCKCELL: LTE,2452,155", "OK"})
	if !ok || !lte.Locked || lte.ARFCN != 2452 || lte.PCID != 155 {
		t.Fatalf("LTE 解析 = %+v ok=%v", lte, ok)
	}
	// 同一行对 NR 不成立
	if nr, _ := g.ParseCellLockState(CellTechNR, []string{"+ZLOCKCELL: LTE,2452,155"}); nr.Locked {
		t.Errorf("LTE 行不该判为 NR 已锁：%+v", nr)
	}
	// NR 行
	nr2, _ := g.ParseCellLockState(CellTechNR, []string{"+ZLOCKCELL: NR5G,627264,521,1,78"})
	if !nr2.Locked || nr2.ARFCN != 627264 || nr2.PCID != 521 {
		t.Errorf("NR 解析 = %+v", nr2)
	}
	// unlock
	if l, _ := g.ParseCellLockState(CellTechLTE, []string{"+ZLOCKCELL: LTE,unlock"}); l.Locked {
		t.Errorf("unlock 不该判为已锁：%+v", l)
	}
}

// TestGosuncnLockerShape 形状 + 按机型回退
func TestGosuncnLockerShape(t *testing.T) {
	g := &gosuncn.GosuncnLockCommand{}
	var l NetLocker = g
	if lockVendorOpsOf(l) == nil {
		t.Fatal("gosuncn.GosuncnLockCommand 应实现 LockVendorOps")
	}
	if _, ok := l.(BandLockSetter); !ok {
		t.Error("需要 BandLockSetter：两种 ZBAND 形态靠回读 AT+ZBAND? 判定")
	}
	if !platform.IsGosuncnModel("Gosuncn GM800") || !platform.IsGosuncnModel("ME3630-W") {
		t.Error("GM800 / ME3630 应判为高新兴")
	}
	if platform.IsGosuncnModel("RM502Q-GL") {
		t.Error("RM502Q 不该判为高新兴")
	}
	// 未启动时按机型回退到高新兴实现
	mod := &Modem{ID: 0, Vendor: VendorUnknown, Model: "GM800", ModelName: "GM800", cmdImpl: &common.UnknownCommand{}}
	if _, ok := mod.lockerFor().(*gosuncn.GosuncnLockCommand); !ok {
		t.Errorf("GM800 未启动时应回退到高新兴实现，实际 %T", mod.lockerFor())
	}
}

// TestGosuncnLTEHexForm lte 平台机型：`AT+ZBAND=all,all,all,<hex 位图>`（QModem `cmd_zband_set_lte`）
func TestGosuncnLTEHexForm(t *testing.T) {
	g := &gosuncn.GosuncnLockCommand{}

	// 形态判定：`+ZBAND: all,all,all,15`
	fs, ok := gosuncn.GosuncnZBANDLTEHexFields([]string{"AT+ZBAND?", "+ZBAND: all,all,all,15", "OK"})
	if !ok || len(fs) != 4 {
		t.Fatalf("位图形态判定失败：%v ok=%v", fs, ok)
	}
	// qualcomm 的应答（没有 all 段）不能被当位图形态
	for _, bad := range [][]string{
		{"+ZBAND: WCDMA:1,2,3", "+ZBAND: LTE:1,3,8", "+ZBAND: NR5G:78"},
		{"+ZBAND: 1,3,1,3,8"},
		{"+ZBAND: all,all,all"},    // 段数不足
		{"+ZBAND: all,all,all,zz"}, // 第 4 段既不是 all 也不是十六进制
		{"+ZBAND: 1,all,all,15"},   // 前三段不全是 all
	} {
		if _, ok := gosuncn.GosuncnZBANDLTEHexFields(bad); ok {
			t.Errorf("%v 不该判为位图形态", bad)
		}
	}

	// 状态解析：0x15 = bit0/bit2/bit4 → band 1,3,5；NR 不做
	bands, ok := g.ParseBandLockState(CellTechLTE, []string{"+ZBAND: all,all,all,15"})
	if !ok || len(bands) != 3 || bands[0] != 1 || bands[1] != 3 || bands[2] != 5 {
		t.Fatalf("位图解析 = %v ok=%v，want 1,3,5", bands, ok)
	}
	if _, ok := g.ParseBandLockState(CellTechNR, []string{"+ZBAND: all,all,all,15"}); ok {
		t.Error("位图形态只有 LTE 一段，NR 不该给结果")
	}
	// qualcomm 形态不臆造
	if _, ok := g.ParseBandLockState(CellTechLTE, []string{"+ZBAND: LTE:1,3,8"}); ok {
		t.Error("qualcomm 形态的当前值不该解析（形状未逐字确认）")
	}

	// 下发：回读到 all,all,all,15 → 只替第 4 段（写 1,3 → 2^0+2^2 = 5）
	d := &scriptedLockDoer{replies: map[string][]string{
		"AT+ZBAND?": {"+ZBAND: all,all,all,15", "OK"},
	}}
	if err := g.SetBandLockAt(d, CellTechLTE, []int{1, 3}); err != nil {
		t.Fatal(err)
	}
	if len(d.lockCmds) != 1 || d.lockCmds[0] != "AT+ZBAND=all,all,all,5" {
		t.Errorf("下发 = %v，want [AT+ZBAND=all,all,all,5]", d.lockCmds)
	}
	// 高频段（>64）也照样按位推（bc 的 2^(band-1) 不限于 64 位）
	mask, err := gosuncn.GosuncnBandMaskHex([]int{1, 65})
	if err != nil || mask != "10000000000000001" {
		t.Errorf("mask = %q err=%v，want 10000000000000001", mask, err)
	}
	if _, err := gosuncn.GosuncnBandMaskHex([]int{200}); err == nil {
		t.Error("200 超出范围应报错")
	}
	// 位图形态不接 NR 锁定
	if err := g.SetBandLockAt(d, CellTechNR, []int{78}); err == nil || !strings.Contains(err.Error(), "NR") {
		t.Errorf("位图形态锁 NR 应报错，实际 %v", err)
	}
	// 解锁：位图形态 → all,all,all,all
	d2 := &scriptedLockDoer{replies: map[string][]string{
		"AT+ZBAND?": {"+ZBAND: all,all,all,15", "OK"},
	}}
	if err := g.UnlockBandAt(d2, CellTechLTE); err != nil {
		t.Fatal(err)
	}
	if len(d2.lockCmds) != 1 || d2.lockCmds[0] != "AT+ZBAND=all,all,all,all" {
		t.Errorf("解锁 = %v，want [AT+ZBAND=all,all,all,all]", d2.lockCmds)
	}
}

// TestGosuncnQualcommFormUnchanged 回读不是位图形态（或回读失败）时，行为与本轮之前一致
func TestGosuncnQualcommFormUnchanged(t *testing.T) {
	g := &gosuncn.GosuncnLockCommand{}
	for name, reply := range map[string][]string{
		"qualcomm 多行": {"+ZBAND: LTE:1,3,8", "+ZBAND: NR5G:78", "OK"},
		"回读报错":        {"+CME ERROR: 3"},
		"空应答":         {"OK"},
	} {
		d := &scriptedLockDoer{replies: map[string][]string{"AT+ZBAND?": reply}}
		if err := g.SetBandLockAt(d, CellTechLTE, []int{1, 3, 5}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(d.lockCmds) != 1 || d.lockCmds[0] != "AT+ZBAND=1,3,1,3,5" {
			t.Errorf("%s 下发 = %v，want [AT+ZBAND=1,3,1,3,5]", name, d.lockCmds)
		}
	}
	// 解锁回退到 AT+ZBAND=0
	d := &scriptedLockDoer{replies: map[string][]string{
		"AT+ZBAND?": {"+ZBAND: LTE:1,3,8", "OK"},
	}}
	if err := g.UnlockBandAt(d, CellTechLTE); err != nil {
		t.Fatal(err)
	}
	if len(d.lockCmds) != 1 || d.lockCmds[0] != "AT+ZBAND=0" {
		t.Errorf("解锁 = %v，want [AT+ZBAND=0]", d.lockCmds)
	}
}
