package kernel

import (
	"modemd/module/config"
	"modemd/module/dial"
	"strings"
	"testing"
)

// TestAirplaneOnNilSafe：nil 模组读飞行模式不能崩。
// 这个函数会被"每轮组网循环""拨号器该不该跑"这些地方带着可能为 nil 的模组调用。
func TestAirplaneOnNilSafe(t *testing.T) {
	var mod *Modem
	if mod.AirplaneOn() {
		t.Fatalf("nil Modem 不该报飞行模式")
	}
}

// TestNoteFuncMode 验证功能模式 → 飞行模式标记的落法。
// 关键是 CFUN=4 才算飞行模式：0（最小功能）也不是，虽然它同样不注网。
func TestNoteFuncMode(t *testing.T) {
	cases := []struct {
		name string
		v    int
		want bool
	}{
		{"CFUN=4 飞行模式", CFUNAirplane, true},
		{"CFUN=1 全功能", CFUNFull, false},
		{"CFUN=0 最小功能", 0, false},
		{"CFUN=5", 5, false},
	}
	for _, c := range cases {
		mod := &Modem{}
		if got := mod.noteFuncMode(c.v); got != c.want {
			t.Errorf("%s: noteFuncMode(%d) = %v，想要 %v", c.name, c.v, got, c.want)
		}
		if got := mod.AirplaneOn(); got != c.want {
			t.Errorf("%s: AirplaneOn() = %v，想要 %v", c.name, got, c.want)
		}
	}

	// 反复刷新也要跟着变（读回 1 → 4 → 1）
	mod := &Modem{}
	mod.noteFuncMode(CFUNFull)
	if mod.AirplaneOn() {
		t.Fatalf("CFUN=1 之后不该还是飞行模式")
	}
	mod.noteFuncMode(CFUNAirplane)
	if !mod.AirplaneOn() {
		t.Fatalf("CFUN=4 之后应该是飞行模式")
	}
	mod.noteFuncMode(CFUNFull)
	if mod.AirplaneOn() {
		t.Fatalf("改回 CFUN=1 之后应清掉飞行模式标记")
	}
}

// TestHostDialStopReasonAirplane：飞行模式开着时，主机侧拨号器的停止原因要明确是"飞行模式"，
// 且**优先于**"还没认出网卡"这类原因（否则日志里看到的是"没认出 wwan0"，看不出真正原因）。
func TestHostDialStopReasonAirplane(t *testing.T) {
	old := config.Cfg.Net.HostDial
	config.Cfg.Net.HostDial = true
	defer func() { config.Cfg.Net.HostDial = old }()

	mod := &Modem{ID: 4242}
	mod.isRunning = true // 模组在跑，排除"模组不在了"这一条
	st := &dial.DialState{}

	// 飞行模式 + 网卡还没认出来：必须报飞行模式
	mod.noteFuncMode(CFUNAirplane)
	reason := hostDialStopReason(mod, st, "")
	if !strings.Contains(reason, "飞行模式") {
		t.Errorf("飞行模式开着时停止原因应含「飞行模式」，实际 %q", reason)
	}

	// 关掉飞行模式 + 网卡也认出来了 → 不该再拦（返回空串表示该跑）
	mod.noteFuncMode(CFUNFull)
	if reason := hostDialStopReason(mod, st, "wwan0"); reason != "" {
		t.Errorf("飞行模式关掉后不该再拦拨号器，实际原因 %q", reason)
	}

	// 兜底：只把标记打开（不经过 AT），也要能拦住
	mod.airplaneOn.Store(true)
	if reason := hostDialStopReason(mod, st, "wwan0"); !strings.Contains(reason, "飞行模式") {
		t.Errorf("标记开着时停止原因应含「飞行模式」，实际 %q", reason)
	}
}

// TestResetAutoDialTried：关掉飞行模式后要能"立刻"重拨。
// autoDialTried 是 60 秒退避，不清掉的话用户关了飞行模式还要干等一分钟。
func TestResetAutoDialTried(t *testing.T) {
	const id = 4244
	resetAutoDialTried(id) // 先清干净，别受别的用例影响

	if !autoDialAllowed(id) {
		t.Fatalf("第一次应该允许自动拨号")
	}
	if autoDialAllowed(id) {
		t.Fatalf("60 秒内第二次应该被退避挡住")
	}
	resetAutoDialTried(id)
	if !autoDialAllowed(id) {
		t.Fatalf("resetAutoDialTried 之后应该立刻允许")
	}
	resetAutoDialTried(id) // 收尾
}

// ---- 机型禁用飞行模式（F50：整机只有这一路上行，关了连内网/管理通道一起断）----

// newFakeAirplaneModem 造一台只连着"假模组"的机器：CFUN 的应答由 cfun(n) 给，
// n 是第几次 `AT+CFUN?`（从 1 开始数）。返回收到的指令通道，用来断言"到底发没发 AT"。
func newFakeAirplaneModem(port string, cfun func(n int) string) (*Modem, chan string) {
	n := 0
	mod := &Modem{
		ID:        0,
		ATPort:    port,
		writeChan: make(chan string, 64),
		readChan:  make(chan string, 64),
	}
	sent := fakeATServe(mod, func(cmd string) []string {
		switch {
		case strings.HasPrefix(cmd, "AT+CFUN="):
			return []string{cmd, "OK"}
		case cmd == "AT+CFUN?":
			n++
			return []string{cmd, cfun(n), "OK"}
		}
		return []string{cmd, "OK"}
	})
	return mod, sent
}

// TestAirplaneBlockedReasonOnlyForSIPC：只有展锐 SIPC 那一族（F50 用 /dev/stty_nr2）
// 被禁；普通串口（移远 FM160 的 COM3 之类）照旧能用。
func TestAirplaneBlockedReasonOnlyForSIPC(t *testing.T) {
	var nilMod *Modem
	if why := nilMod.AirplaneBlockedReason(); why != "" {
		t.Errorf("nil 模组应返回空串，实际 %q", why)
	}
	for _, port := range []string{"/dev/stty_nr2", "stty_nr2", "/dev/stty_nr0"} {
		if why := (&Modem{ATPort: port}).AirplaneBlockedReason(); why == "" {
			t.Errorf("%s 属展锐 SIPC，应被禁用", port)
		}
	}
	for _, port := range []string{"COM3", "/dev/ttyUSB2", "/dev/mhi_DUN"} {
		if why := (&Modem{ATPort: port}).AirplaneBlockedReason(); why != "" {
			t.Errorf("%s 不该被禁，实际 %q", port, why)
		}
	}
}

// TestSetAirplaneOnBlockedForF50：F50 上点"开飞行模式"必须**根本不发 AT** 就报错。
// 这是用户明确要求（"去掉这个飞行模式，不然网络内网也断了"）：真发下去的那一刻，
// 他自己的管理页/SSH 就可能跟着射频一起没了。
func TestSetAirplaneOnBlockedForF50(t *testing.T) {
	withATTimeout(t)

	mod, sent := newFakeAirplaneModem("/dev/stty_nr2", func(int) string { return "+CFUN: 1" })
	defer close(mod.writeChan)

	fun, err := mod.SetAirplane(true)
	if err == nil {
		t.Fatalf("F50 不该允许开飞行模式，实际 fun=%d err=nil", fun)
	}
	if !strings.Contains(err.Error(), "不支持飞行模式") {
		t.Errorf("报错要说清机型不支持，实际 %q", err.Error())
	}
	if cmds := drainSent(sent); len(cmds) != 0 {
		t.Errorf("禁止的机型一条 AT 都不该发，实际发了 %q", cmds)
	}
	if mod.AirplaneOn() {
		t.Error("被拦住之后不该把 airplaneOn 标记打开")
	}
}

// TestSetAirplaneOffStillWorksOnF50：只挡"开"。万一射频已被别处关了
// （老版本、遗留的定时任务），"关飞行模式"是唯一的恢复手段，必须还能用。
func TestSetAirplaneOffStillWorksOnF50(t *testing.T) {
	withATTimeout(t)

	mod, sent := newFakeAirplaneModem("/dev/stty_nr2", func(int) string { return "+CFUN: 1" })
	defer close(mod.writeChan)

	fun, err := mod.SetAirplane(false)
	if err != nil {
		t.Fatalf("关飞行模式必须仍可用（恢复手段）：%v", err)
	}
	if fun != CFUNFull {
		t.Errorf("应返回 CFUN=%d，实际 %d", CFUNFull, fun)
	}
	if !hasCmd(drainSent(sent), "AT+CFUN=1") {
		t.Error("关飞行模式应真的下发 AT+CFUN=1")
	}
}

// TestSetAirplaneOnAllowedForOtherVendors：其它机型不受影响（照旧下发 + 回读确认）。
func TestSetAirplaneOnAllowedForOtherVendors(t *testing.T) {
	withATTimeout(t)

	mod, sent := newFakeAirplaneModem("COM3", func(int) string { return "+CFUN: 4" })
	defer close(mod.writeChan)

	fun, err := mod.SetAirplane(true)
	if err != nil {
		t.Fatalf("普通机型应能开飞行模式：%v", err)
	}
	if fun != CFUNAirplane {
		t.Errorf("应返回 CFUN=%d，实际 %d", CFUNAirplane, fun)
	}
	cmds := drainSent(sent)
	if !hasCmd(cmds, "AT+CFUN=4") || !hasCmd(cmds, "AT+CFUN?") {
		t.Errorf("应下发 CFUN=4 并回读确认，实发 %q", cmds)
	}
}

// TestSchedActionSupportedBlocksAirplaneOnForF50：定时任务那条路也要挡住 ——
// 否则用户排一条"22:00 开飞行模式"，到点就把自己的内网掐了。
// "关飞行模式"仍要允许（恢复手段）。
func TestSchedActionSupportedBlocksAirplaneOnForF50(t *testing.T) {
	f50 := &Modem{ATPort: "/dev/stty_nr2", Vendor: "展锐 Unisoc"}
	if err := schedActionSupported(f50, KindAirplaneOn); err == nil {
		t.Error("F50 上「开飞行模式」应被标为机型不支持")
	} else if !strings.Contains(err.Error(), "不支持飞行模式") {
		t.Errorf("原因要说清是不支持飞行模式，实际 %q", err.Error())
	}
	if err := schedActionSupported(f50, KindAirplaneOff); err != nil {
		t.Errorf("F50 上「关飞行模式」应仍可用（恢复手段），实际 %v", err)
	}
	other := &Modem{ATPort: "COM3", Vendor: "移远 Quectel"}
	if err := schedActionSupported(other, KindAirplaneOn); err != nil {
		t.Errorf("其它机型「开飞行模式」不该被拦，实际 %v", err)
	}
}
