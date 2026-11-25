package kernel

import (
	"testing"

	"modemd/module/simcom"
)

// ====================== 芯讯通 SIMCom 锁网（AT+CSYSSEL / CCELLCFG / C5GCELLCFG） ======================

// TestSimcomBandCommands 锁频段指令拼装（手册 §4.2.20）
func TestSimcomBandCommands(t *testing.T) {
	s := &simcom.SimcomLockCommand{}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"query band lte", s.CommandQueryBandLock(CellTechLTE), `AT+CSYSSEL="lte_band"`},
		{"query band nr", s.CommandQueryBandLock(CellTechNR), `AT+CSYSSEL="nr5g_band"`},
		{"set band nr", s.CommandSetBandLock(CellTechNR, []int{78, 41, 41}), `AT+CSYSSEL="nr5g_band",41:78`},
		{"set band lte", s.CommandSetBandLock(CellTechLTE, []int{3, 1, 5}), `AT+CSYSSEL="lte_band",1:3:5`},
		// 空列表绝不拼出 ",0"
		{"empty nr", s.CommandSetBandLock(CellTechNR, nil), ""},
		{"unlock band", s.CommandUnlockBand(CellTechNR), "AT+CSYSSEL"},
		{"caps band", s.CommandBandLockCaps(), "AT+CSYSSEL=?"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestSimcomCellCommands 锁小区指令拼装（手册 §4.2.21 / §4.2.22）
func TestSimcomCellCommands(t *testing.T) {
	s := &simcom.SimcomLockCommand{}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"query cell lte", s.CommandQueryCellLock(CellTechLTE), "AT+CCELLCFG?"},
		{"query cell nr", s.CommandQueryCellLock(CellTechNR), "AT+C5GCELLCFG?"},
		// ⚠️ 锁小区的指令尾巴 `;+CNMP=38|71` 是**上游 vendor/simcom.sh 原文**：
		// 锁小区要先切到对应制式才生效；解锁不带（不该顺手改用户的制式偏好）
		{"lock cell lte", s.CommandLockCell(CellLockRequest{Tech: CellTechLTE, ARFCN: 2452, PCID: 155, LockType: CellLockARFCNPCI}), "AT+CCELLCFG=1,155,2452;+CNMP=38"},
		{"unlock cell lte", s.CommandLockCell(CellLockRequest{Tech: CellTechLTE, LockType: CellLockNone}), "AT+CCELLCFG=0"},
		// LTE 的 CCELLCFG 必须带 pci → 只锁频点不支持（空串 = 上层会明确报错，不发空指令）
		{"arfcn only lte unsupported", s.CommandLockCell(CellLockRequest{Tech: CellTechLTE, ARFCN: 2452, LockType: CellLockARFCN}), ""},
		{"lock cell nr", s.CommandLockCell(CellLockRequest{Tech: CellTechNR, ARFCN: 627264, PCID: 521, LockType: CellLockARFCNPCI}), `AT+C5GCELLCFG="pci",521,627264,1,78;+CNMP=71`},
		{"lock arfcn nr", s.CommandLockCell(CellLockRequest{Tech: CellTechNR, ARFCN: 627264, LockType: CellLockARFCN}), `AT+C5GCELLCFG="arfcn",1,1:627264;+CNMP=71`},
		{"unlock cell nr", s.CommandLockCell(CellLockRequest{Tech: CellTechNR, LockType: CellLockNone}), `AT+C5GCELLCFG="unlock"`},
		{"caps cell", s.CommandCellLockCaps(), "AT+CCELLCFG?"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestParseCSYSSEL 频段状态解析：`+CSYSSEL: "<key>",1:28:41:78`
func TestParseCSYSSEL(t *testing.T) {
	resp := []string{
		`AT+CSYSSEL="nr5g_band"`,
		`+CSYSSEL: "lte_band",1:3:5:8`,
		`+CSYSSEL: "nr5g_band",1:28:41:78`,
		"OK",
	}
	s := &simcom.SimcomLockCommand{}
	bands, locked := s.ParseBandLockState(CellTechNR, resp)
	if !locked || len(bands) != 4 || bands[0] != 1 || bands[3] != 78 {
		t.Fatalf("NR 解析 = %v locked=%v", bands, locked)
	}
	lte, locked := s.ParseBandLockState(CellTechLTE, resp)
	if !locked || len(lte) != 4 || lte[0] != 1 || lte[3] != 8 {
		t.Fatalf("LTE 解析 = %v locked=%v", lte, locked)
	}
}

// TestSimcomParseCellLockState 锁小区状态解析（LTE 两格 / NR "pci" 与 "arfcn" 两种）
func TestSimcomParseCellLockState(t *testing.T) {
	s := &simcom.SimcomLockCommand{}

	lte, ok := s.ParseCellLockState(CellTechLTE, []string{"AT+CCELLCFG?", "+CCELLCFG: 255,1850", "OK"})
	if !ok || !lte.Locked || lte.PCID != 255 || lte.ARFCN != 1850 {
		t.Fatalf("LTE 解析 = %+v ok=%v", lte, ok)
	}

	nr, ok := s.ParseCellLockState(CellTechNR, []string{`+C5GCELLCFG: "pci",100, 518670,1, 41`, "OK"})
	if !ok || !nr.Locked || nr.PCID != 100 || nr.ARFCN != 518670 || nr.LockType != CellLockARFCNPCI {
		t.Fatalf("NR pci 解析 = %+v ok=%v", nr, ok)
	}

	nr2, ok := s.ParseCellLockState(CellTechNR, []string{`+C5GCELLCFG: "arfcn",1,100:518670`})
	if !ok || !nr2.Locked || nr2.ARFCN != 518670 || nr2.LockType != CellLockARFCN {
		t.Fatalf("NR arfcn 解析 = %+v ok=%v", nr2, ok)
	}
}

// TestSimcomLockerShape 芯讯通锁网族形状
func TestSimcomLockerShape(t *testing.T) {
	s := &simcom.SimcomLockCommand{}
	var l NetLocker = s
	if l == nil {
		t.Fatal("simcom.SimcomLockCommand 应实现 NetLocker")
	}
	if lockVendorOpsOf(l) == nil {
		t.Fatal("simcom.SimcomLockCommand 应实现 LockVendorOps")
	}
	if _, ok := l.(BandLockSetter); ok {
		t.Error("芯讯通不需要多步下发，不该实现 BandLockSetter")
	}
	// 组网模式：AT+CNMP（2=自动 / 38=LTE / 71=NR5G）
	if got := s.CommandQueryModePref(); got != "AT+CNMP?" {
		t.Errorf("组网模式查询 = %q, want AT+CNMP?", got)
	}
	if got := s.CommandSetModePref("LTE"); got != "AT+CNMP=38" {
		t.Errorf("LTE 模式设置 = %q, want AT+CNMP=38", got)
	}
	if got := s.CommandSetModePref("NR5G"); got != "AT+CNMP=71" {
		t.Errorf("NR5G 模式设置 = %q, want AT+CNMP=71", got)
	}
	if got := s.CommandSetModePref("AUTO"); got != "AT+CNMP=2" {
		t.Errorf("AUTO 模式设置 = %q, want AT+CNMP=2", got)
	}
	if got := s.CommandSetModePref("LTE:NR5G"); got != "" {
		t.Errorf("不支持的组合模式应返回空串，got %q", got)
	}
	if v, ok := s.ParseModePref([]string{"AT+CNMP?", "+CNMP: 38", "OK"}); !ok || v != "LTE" {
		t.Errorf("ParseModePref = %q ok=%v, want LTE", v, ok)
	}
	// 无 policy 频段概念
	if got := s.CommandQueryPolicyBand(); got != "" {
		t.Errorf("policy 频段查询应为空，got %q", got)
	}
}
