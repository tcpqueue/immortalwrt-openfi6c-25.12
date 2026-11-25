package kernel

import (
	"testing"

	"modemd/module/common"
	"modemd/module/foxconn"
	"modemd/module/lock"
	"modemd/module/neoway"
	"modemd/module/telit"
	"modemd/module/thales"
)

// ====================== 列表式锁频段方言（telit / thales / foxconn / neoway） ======================

// TestBandListLockerCommands 指令拼装（依据 QModem cmds/*.sh）
func TestBandListLockerCommands(t *testing.T) {
	telit := telit.TelitLockCommand()
	thales := thales.BandPrefLockCommand()
	foxconn := foxconn.BandPrefLockCommand()
	neoway := neoway.NeowayLockCommand()

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"telit query", telit.CommandQueryBandLock(CellTechLTE), "AT#BND?"},
		// telit 的写入是"先回读再整串改写"（走 BandLockSetter），单条指令拼不出来
		{"telit set（多步）", telit.CommandSetBandLock(CellTechLTE, []int{1, 3, 5}), ""},
		{"telit caps", telit.CommandBandLockCaps(), "AT#BND=?"},
		{"telit empty", telit.CommandSetBandLock(CellTechNR, nil), ""},

		{"thales query", thales.CommandQueryBandLock(CellTechNR), "AT+BAND_PREF?"},
		{"thales set lte", thales.CommandSetBandLock(CellTechLTE, []int{1, 3}), "AT+BAND_PREF=LTE,2,1,3"},
		{"thales set nr", thales.CommandSetBandLock(CellTechNR, []int{78}), "AT+BAND_PREF=NR5G,2,78"},

		{"foxconn query", foxconn.CommandQueryBandLock(CellTechLTE), "AT^BAND_PREF?"},
		{"foxconn set nr", foxconn.CommandSetBandLock(CellTechNR, []int{41, 78}), "AT^BAND_PREF=NR5G,2,41,78"},

		{"neoway query", neoway.CommandQueryBandLock(CellTechLTE), "AT+NWSETBAND?"},
		// act 是**数字**（QModem vendor/neoway.sh 的 set_lockband：UMTS=2 / LTE=3 / NR=6）
		{"neoway set lte", neoway.CommandSetBandLock(CellTechLTE, []int{1}), "AT+NWSETBAND=3,1,1"},
		{"neoway set nr", neoway.CommandSetBandLock(CellTechNR, []int{78, 41}), "AT+NWSETBAND=6,2,78,41"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestBandListLockerShape 共同的限制：无锁小区、无 policy、解锁格式未确认（空串=不发）
func TestBandListLockerShape(t *testing.T) {
	for _, b := range []*lock.BandListLocker{thales.BandPrefLockCommand(), neoway.NeowayLockCommand()} {
		var l NetLocker = b
		name := lock.DialectName(b)
		if lockVendorOpsOf(l) == nil {
			t.Errorf("%s: 应实现 LockVendorOps", name)
		}
		if got := b.CommandQueryCellLock(CellTechNR); got != "" {
			t.Errorf("%s: 无锁小区指令，应为空，got %q", name, got)
		}
		if got := b.CommandLockCell(CellLockRequest{Tech: CellTechLTE, ARFCN: 2452, PCID: 155, LockType: CellLockARFCNPCI}); got != "" {
			t.Errorf("%s: 锁小区应为空，got %q", name, got)
		}
		if got := b.CommandCellLockCaps(); got != "" {
			t.Errorf("%s: 锁小区能力探测应为空，got %q", name, got)
		}
		if got := b.CommandQueryPolicyBand(); got != "" {
			t.Errorf("%s: 无 policy 频段，应为空，got %q", name, got)
		}
		// 解锁格式未确认 → 空串（上层会如实报"未提供解锁指令"，绡不瞎发）
		if got := b.CommandUnlockBand(CellTechNR); got != "" {
			t.Errorf("%s: 解锁应为空（格式未确认），got %q", name, got)
		}
	}
	// Telit 同属"无锁小区 / 无 policy"一类，但它的写/解锁走 BandLockSetter
	telit := telit.TelitLockCommand()
	var tl NetLocker = telit
	if lockVendorOpsOf(tl) == nil {
		t.Error("telit: 应实现 LockVendorOps")
	}
	if _, ok := tl.(BandLockSetter); !ok {
		t.Error("telit: 应实现 BandLockSetter（AT#BND 要回读后整串改写）")
	}
	for _, c := range []string{telit.CommandQueryCellLock(CellTechNR), telit.CommandCellLockCaps(), telit.CommandQueryPolicyBand(), telit.CommandUnlockBand(CellTechNR)} {
		if c != "" {
			t.Errorf("telit: 这几项应为空，got %q", c)
		}
	}
}

// TestPreferredLockerBandListVendors 按机型回退到各自方言
func TestPreferredLockerBandListVendors(t *testing.T) {
	cases := []struct {
		model string
		want  string // lock.BandListLocker.name / "" 表示不是 lock.BandListLocker
	}{
		{"Telit FN990A40", "telit"},
		{"Thales MV32-W-A", "thales"},
		{"Foxconn T99W175", "foxconn"},
		{"Neoway N510M", "neoway"},
	}
	for _, c := range cases {
		m := &Modem{ID: 0, Vendor: VendorUnknown, Model: c.model, ModelName: c.model, cmdImpl: &common.UnknownCommand{}}
		l := m.lockerFor()
		name := lock.DialectName(l)
		if name == "" {
			t.Errorf("%s: 应为列表式方言，实际 %T", c.model, l)
			continue
		}
		if name != c.want {
			t.Errorf("%s: 方言 = %q, want %q", c.model, name, c.want)
		}
	}
}
