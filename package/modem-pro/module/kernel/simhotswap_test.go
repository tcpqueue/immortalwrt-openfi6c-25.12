package kernel

// ====================== SIM 热插拔：纯逻辑用例 ======================
//
// 覆盖三件事：卡在不在的判定、变化分类（拔/插/换）、以及"读卡失败不能误判成换卡"。

import (
	"testing"
	"time"
)

func TestCPINPresent(t *testing.T) {
	present := []string{"READY", "ready", " SIM PIN", "SIM PUK", "PH-SIM PIN", "SIM PIN2", "SIM PUK2"}
	for _, s := range present {
		if !CPINPresent(s) {
			t.Errorf("CPINPresent(%q) 应为 true（卡在位）", s)
		}
	}
	absent := []string{"", "   ", "SIM REMOVED", "NOT INSERTED", "ABSENT", "NO SIM"}
	for _, s := range absent {
		if CPINPresent(s) {
			t.Errorf("CPINPresent(%q) 应为 false（卡不在位）", s)
		}
	}
}

func TestSIMHotswapEvent(t *testing.T) {
	cardA := SIMIdentity{Present: true, State: "READY", ICCID: "89860324040255993152"}
	cardA2 := SIMIdentity{Present: true, State: "READY", ICCID: "89860324040255993152"}
	cardB := SIMIdentity{Present: true, State: "READY", ICCID: "89860324740250209831"}
	locked := SIMIdentity{Present: true, State: "SIM PIN", ICCID: "89860324040255993152"}
	gone := SIMIdentity{}

	cases := []struct {
		name string
		prev SIMIdentity
		cur  SIMIdentity
		want string
	}{
		{"同一张卡（两次都读到同样内容）", cardA, cardA2, SIMUnchanged},
		{"同一张卡但状态从 READY 变成锁卡", cardA, locked, SIMUnchanged},
		{"拔卡", cardA, gone, SIMRemoved},
		// ⚠️ 拔卡前记住的身份会保留（见 checkSIMHotswap）⇒ 同一张卡回来时这里比得出"一样"，
		// 只有真正另一张卡才算换卡。真机事故：把它一律当"换卡"→ 每次复位模组 → 无限循环。
		{"之前没读到过身份，现在有卡", gone, cardA, SIMInserted},
		{"拔卡前记住的是同一张卡，现在回来了", SIMIdentity{Present: false, ICCID: cardA.ICCID}, cardA, SIMInserted},
		{"拔卡前记住的是另一张卡", SIMIdentity{Present: false, ICCID: cardA.ICCID}, cardB, SIMSwapped},
		{"换卡", cardA, cardB, SIMSwapped},
		{"一直没卡", gone, gone, SIMUnchanged},
	}
	for _, c := range cases {
		if got := simHotswapEvent(c.prev, c.cur); got != c.want {
			t.Errorf("%s: simHotswapEvent = %q, want %q", c.name, got, c.want)
		}
	}
}

// 该不该为热插拔事件复位模组（AT+CFUN=1,1）：**只有确认换了另一张卡 + 用户显式打开**才行。
// 真机事故（2026-10-01）：旧逻辑对"插卡"也复位，而模组刚重新枚举时 CPIN 必然读不到 →
// 每次重连都当成"插卡"→ 复位 → 掉线 → 再重连……模块就再也"出不来"了。
func TestShouldResetModem(t *testing.T) {
	if shouldResetModem(SIMInserted, true) {
		t.Error("插卡绝不能复位模组（会把模块搞成反复掉线）")
	}
	if shouldResetModem(SIMRemoved, true) {
		t.Error("拔卡不该复位模组")
	}
	if shouldResetModem(SIMUnchanged, true) {
		t.Error("没变化不该复位模组")
	}
	if shouldResetModem(SIMSwapped, false) {
		t.Error("用户没打开 hotswap_reset 就不能动模组")
	}
	if !shouldResetModem(SIMSwapped, true) {
		t.Error("确认换卡 + 用户打开开关时应该复位")
	}
}

// ⚠️ 最容易踩的误报：读卡偶发失败会回空串 —— 拿空值去比就成了"换卡"，
// 于是每次读失败都复位一次模组。所以身份比对要求**两边都非空**。
func TestSIMIdentityDiffersIgnoresEmpty(t *testing.T) {
	full := SIMIdentity{Present: true, ICCID: "111111", IMSI: "460110000000001"}
	noICCID := SIMIdentity{Present: true, ICCID: "", IMSI: "460110000000001"}
	noIMSI := SIMIdentity{Present: true, ICCID: "111111", IMSI: ""}
	// 只把 ICCID 换掉、IMSI 保持一致：用来验证"一边 ICCID 为空时不去比 ICCID"
	sameIMSIotherICCID := SIMIdentity{Present: true, ICCID: "222222", IMSI: "460110000000001"}
	other := SIMIdentity{Present: true, ICCID: "222222", IMSI: "460110000000002"}

	if simIdentityDiffers(full, noICCID) {
		t.Error("ICCID 读失败（回空）不该判成换卡")
	}
	if simIdentityDiffers(noICCID, sameIMSIotherICCID) {
		t.Error("ICCID 一边为空、IMSI 相同：不该判成换卡")
	}
	if simIdentityDiffers(full, noIMSI) {
		t.Error("IMSI 读失败（回空）不该判成换卡")
	}
	if !simIdentityDiffers(full, other) {
		t.Error("ICCID 与 IMSI 都变了，应判成换卡")
	}
	if !simIdentityDiffers(full, sameIMSIotherICCID) {
		t.Error("ICCID 两边都非空且不同，应判成换卡")
	}
	if simIdentityDiffers(full, SIMIdentity{Present: true, ICCID: "111111", IMSI: "460110000000001"}) {
		t.Error("完全一样不该判成换卡")
	}
}

// 没收到 URC 时按周期轮询；收到 URC（dirty）时立刻读；周期为 0 表示关掉检查。
func TestShouldCheckSIM(t *testing.T) {
	now := time.Now()
	if !shouldCheckSIM(time.Time{}, 20*time.Second, false, now) {
		t.Error("第一次（还没读过）应该去读")
	}
	if shouldCheckSIM(now.Add(-5*time.Second), 20*time.Second, false, now) {
		t.Error("离上次只过了 5 秒，不该读")
	}
	if !shouldCheckSIM(now.Add(-21*time.Second), 20*time.Second, false, now) {
		t.Error("超过一个周期了，应该读")
	}
	if !shouldCheckSIM(now, 20*time.Second, true, now) {
		t.Error("收到过 +CPIN URC（dirty）就该立刻读，不等周期")
	}
	if shouldCheckSIM(now.Add(-time.Hour), 0, false, now) {
		t.Error("周期 0 = 关掉检查，不该读")
	}
	if !shouldCheckSIM(now.Add(-time.Hour), 0, true, now) {
		t.Error("周期 0 时 URC 仍然应该立刻核实")
	}
}

// `+CPIN:` 只有在**与已知状态不同**时才置 dirty ——
// 否则 `AT+CPIN?` 自己的应答会一直把自己标脏，变成 2 秒一次的自我轮询。
func TestNoteCPINLineMarksDirtyOnlyOnChange(t *testing.T) {
	mod := &Modem{ID: 0}
	mod.simIdent = SIMIdentity{Present: true, State: "READY"}

	mod.noteCPINLine("+CPIN: READY") // 查询应答，和已知一样
	if mod.simDirty {
		t.Error("状态没变不该置 dirty（会变成自我轮询）")
	}
	mod.noteCPINLine("AT+CPIN?") // 回显
	if mod.simDirty {
		t.Error("回显行不该置 dirty")
	}
	mod.noteCPINLine("+CPIN: SIM REMOVED") // 拔卡 URC
	if !mod.simDirty {
		t.Error("状态变了（拔卡）应置 dirty")
	}

	mod.simDirty = false
	mod.noteCPINLine("+CPIN: SIM PIN") // 插了一张带锁的卡
	if !mod.simDirty {
		t.Error("插卡（READY → SIM PIN）应置 dirty")
	}

	mod.simDirty = false
	mod.noteCPINLine("+CMTI: \"SM\",3") // 无关的 URC
	if mod.simDirty {
		t.Error("无关行不该置 dirty")
	}
}
