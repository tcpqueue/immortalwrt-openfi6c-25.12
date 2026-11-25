package kernel

import (
	"strings"
	"testing"

	"modemd/module/common"
	"modemd/module/simcom"
)

// ====================== 芯讯通旧款 4G：AT+CNBP ======================

// TestCNBPParse 解析 `+CNBP:<mode>,<lte16>,<ext4>[,<saveMode>]`
func TestCNBPParse(t *testing.T) {
	// bit0 + bit2 = band 1,3；ext bit0 = band 33
	resp := []string{"AT+CNBP?", "+CNBP:0x0000000C00000000,0x0000000000000005,0x0001,0", "OK"}
	bands, ok := simcom.CnbpBandsFromResp(resp)
	if !ok || len(bands) != 3 {
		t.Fatalf("解析 = %v ok=%v，want 1,3,33", bands, ok)
	}
	if bands[0] != 1 || bands[1] != 3 || bands[2] != 33 {
		t.Errorf("解析 = %v，want 1,3,33", bands)
	}
	// 一段都没有（全 0）→ 不返回结果（宁可空白）
	if b, ok := simcom.CnbpBandsFromResp([]string{"+CNBP:0x0,0x0,0x0,0"}); ok || b != nil {
		t.Errorf("全 0 不该给结果：%v ok=%v", b, ok)
	}
	// 形状不对 → 不认
	if _, ok := simcom.CnbpBandsFromResp([]string{"+CSYSSEL: \"lte_band\",1:3"}); ok {
		t.Error("别的应答不该被当成 CNBP")
	}
	if _, ok := simcom.CnbpBandsFromResp([]string{"+CNBP:0x1"}); ok {
		t.Error("只有 1 段时不该认（缺 LTE 段）")
	}
	// 只有两段（没有 ext）也能读
	if b, ok := simcom.CnbpBandsFromResp([]string{"+CNBP:0x1,0x4"}); !ok || len(b) != 1 || b[0] != 3 {
		t.Errorf("两段应答 = %v ok=%v，want [3]", b, ok)
	}
}

// TestCNBPWrite 写入：只替换 LTE 两段，mode 与 saveMode 保留（QModem 把 saveMode 写死 0）
func TestCNBPWrite(t *testing.T) {
	c := simcom.CnbpCommand()
	d := &scriptedLockDoer{replies: map[string][]string{
		"AT+CNBP?": {"+CNBP:0x0000000C00000000,0x0000000000000005,0x0001,1", "OK"},
	}}
	if err := c.SetBandLockAt(d, CellTechLTE, []int{1, 3, 33}); err != nil {
		t.Fatal(err)
	}
	want := "AT+CNBP=0x0000000C00000000,0x0000000000000005,0x0001,1"
	if len(d.lockCmds) != 1 || d.lockCmds[0] != want {
		t.Errorf("下发 = %v，want [%s]", d.lockCmds, want)
	}
	// mode 段补零到 16 位（QModem 的 normalize_hex_width）
	d2 := &scriptedLockDoer{replies: map[string][]string{
		"AT+CNBP?": {"+CNBP:0x5,0x0", "OK"},
	}}
	if err := c.SetBandLockAt(d2, CellTechLTE, []int{2}); err != nil {
		t.Fatal(err)
	}
	want2 := "AT+CNBP=0x0000000000000005,0x0000000000000002,0x0000,0"
	if len(d2.lockCmds) != 1 || d2.lockCmds[0] != want2 {
		t.Errorf("下发 = %v，want [%s]", d2.lockCmds, want2)
	}
}

// TestCNBPRefuse 读不出来 / 越界 / NR 都要拒发
func TestCNBPRefuse(t *testing.T) {
	c := simcom.CnbpCommand()
	for name, reply := range map[string][]string{
		"ERROR": {"+CME ERROR: 3"},
		"字段不足":  {"+CNBP:0x1"},
		"非十六进制": {"+CNBP:zz,0x1"},
	} {
		d := &scriptedLockDoer{replies: map[string][]string{"AT+CNBP?": reply}}
		if err := c.SetBandLockAt(d, CellTechLTE, []int{1}); err == nil {
			t.Errorf("%s：应当拒发", name)
		}
		if len(d.lockCmds) != 0 {
			t.Errorf("%s：不该下发，实际 %v", name, d.lockCmds)
		}
	}
	d := &scriptedLockDoer{replies: map[string][]string{
		"AT+CNBP?": {"+CNBP:0x0,0x0,0x0,0", "OK"},
	}}
	if err := c.SetBandLockAt(d, CellTechLTE, []int{78}); err == nil || !strings.Contains(err.Error(), "78") {
		t.Errorf("78 不在 CNBP 范围（1-32/33-48）应报错，实际 %v", err)
	}
	if err := c.SetBandLockAt(d, CellTechLTE, []int{49}); err == nil {
		t.Error("49 超出扩展段（33-48）应报错")
	}
	if err := c.SetBandLockAt(d, CellTechNR, []int{78}); err == nil || !strings.Contains(err.Error(), "LTE") {
		t.Errorf("NR 应报本方言只支持 LTE，实际 %v", err)
	}
}

// TestCNBPUnlock 解锁：LTE 两段写全 1，mode/saveMode 保留
func TestCNBPUnlock(t *testing.T) {
	c := simcom.CnbpCommand()
	d := &scriptedLockDoer{replies: map[string][]string{
		"AT+CNBP?": {"+CNBP:0x0000000C00000000,0x5,0x1,1", "OK"},
	}}
	if err := c.UnlockBandAt(d, CellTechLTE); err != nil {
		t.Fatal(err)
	}
	want := "AT+CNBP=0x0000000C00000000,0xFFFFFFFFFFFFFFFF,0xFFFF,1"
	if len(d.lockCmds) != 1 || d.lockCmds[0] != want {
		t.Errorf("解锁下发 = %v，want [%s]", d.lockCmds, want)
	}
	d2 := &scriptedLockDoer{}
	if err := c.UnlockBandAt(d2, CellTechLTE); err == nil {
		t.Error("读不出来时应拒发")
	}
	if err := c.UnlockBandAt(d, CellTechNR); err == nil {
		t.Error("NR 解锁应报错")
	}
}

// TestCNBPShape 形状 + 候选注册 + 状态解析
func TestCNBPShape(t *testing.T) {
	c := simcom.CnbpCommand()
	var l NetLocker = c
	if lockVendorOpsOf(l) == nil {
		t.Fatal("simcom.CnbpLocker 应实现 LockVendorOps")
	}
	if _, ok := l.(BandLockSetter); !ok {
		t.Error("simcom.CnbpLocker 应实现 BandLockSetter")
	}
	if got := c.CommandQueryBandLock(CellTechLTE); got != "AT+CNBP?" {
		t.Errorf("LTE 查询 = %q", got)
	}
	if got := c.CommandQueryBandLock(CellTechNR); got != "" {
		t.Errorf("NR 查询应为空（CNBP 没有 NR 段），got %q", got)
	}
	if got := c.CommandSetBandLock(CellTechLTE, []int{1}); got != "" {
		t.Errorf("单条指令拼不出来 → 应为空，got %q", got)
	}
	if got := c.CommandUnlockBand(CellTechLTE); got != "" {
		t.Errorf("解锁走 setter → 应为空，got %q", got)
	}
	// 本方言没有锁小区
	if got := c.CommandQueryCellLock(CellTechLTE); got != "" {
		t.Errorf("无锁小区指令，got %q", got)
	}
	if got := c.CommandLockCell(CellLockRequest{Tech: CellTechLTE, ARFCN: 2452, PCID: 155, LockType: CellLockARFCNPCI}); got != "" {
		t.Errorf("无锁小区指令，got %q", got)
	}
	// 状态解析走同一套
	bands, ok := c.ParseBandLockState(CellTechLTE, []string{"+CNBP:0x0,0x5,0x0,0"})
	if !ok || len(bands) != 2 || bands[0] != 1 || bands[1] != 3 {
		t.Errorf("状态解析 = %v ok=%v，want 1,3", bands, ok)
	}
	if _, ok := c.ParseBandLockState(CellTechNR, []string{"+CNBP:0x0,0x5,0x0,0"}); ok {
		t.Error("NR 不该给结果")
	}
	// 候选表：探针/标记正确（别的 SIMCom 应答不会误命中）
	m := &Modem{ID: 0, Model: "SIM7600", ModelName: "SIM7600", cmdImpl: &common.UnknownCommand{}}
	var found bool
	for _, cd := range m.lockCandidates() {
		if cd.Name == "cnbp" {
			found = true
			if cd.Probe != "AT+CNBP?" || cd.Mark != "+CNBP:" {
				t.Errorf("候选 = %+v", cd)
			}
		}
	}
	if !found {
		t.Fatal("候选里应有 cnbp")
	}
	if lockFamilyLabels["cnbp"] == "" {
		t.Error("缺界面显示名")
	}
	// 候选顺序：CSYSSEL 那套（simcom）必须排在 cnbp 前面，否则 SIM8200 会被抢走
	order := map[string]int{}
	for i, cd := range m.lockCandidates() {
		order[cd.Name] = i
	}
	if order["simcom"] > order["cnbp"] {
		t.Errorf("simcom(%d) 应排在 cnbp(%d) 之前", order["simcom"], order["cnbp"])
	}
}
