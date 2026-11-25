package kernel

// 信号来源的三条边界（展锐 SIPC / F50）：
//   · 跟丢（URC 长时间没有信号上报）⇒ 自动退回 AT，不能一直显示冻结值；
//   · `+CSQ: 255,99` 是“测不出来”的哨兵，不能当有效信号算；
//   · 非展锐模组一律走原来的 AT 路径。

import (
	"testing"
	"time"
)

// URC 跟丢 ⇒ 立刻不再把它当权威（mu300FileBackedState 变假，接口退回 AT）。
func TestURCSignalStaleFallsBack(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	mod.feedURCLine(`+CGREG: 1,"FFFE"`)

	if !mu300FileBackedState(mod) {
		t.Fatal("前提：刚收到信号+注册上报时应走本机文件路径")
	}
	if mod.urcSignalStale() {
		t.Error("刚收到信号上报时不该算跟丢")
	}

	// 把"最后一条信号上报"挪到很久以前 ⇒ 视为跟丢
	mod.mu.Lock()
	mod.urcSignalAt = time.Now().Add(-urcSignalStaleLimit - time.Second)
	mod.mu.Unlock()
	if !mod.urcSignalStale() {
		t.Error("超过 urcSignalStaleLimit 没信号上报应算跟丢")
	}
	if mu300FileBackedState(mod) {
		t.Error("跟丢之后不得再走本机文件路径（会显示冻结值）")
	}
	if got := mod.networkQueryPlan(); got != planFullAT {
		t.Errorf("跟丢之后网络状态也要退回完整 AT 查询，实际 %v", got)
	}

	// 来了一条新的信号上报 ⇒ 立刻恢复
	mod.feedURCLine(`+CSQ: 21,1`)
	if !mu300FileBackedState(mod) {
		t.Error("新的信号上报到达后应恢复本机文件路径")
	}
	if mod.urcSignalStale() {
		t.Error("刚收到信号上报时不该算跟丢")
	}
}

// 从未收到过信号上报（urcSignalAt 为零）也算跟丢 —— 否则会在启动瞬间
// 拿着空信号值去顶掉 AT 查询。
func TestURCSignalStaleWhenNeverSeen(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CGREG: 1,"FFFE"`) // 只有注册，没有信号
	if !mod.urcSignalStale() {
		t.Error("从未见过信号上报时应算跟丢")
	}
	if mu300FileBackedState(mod) {
		t.Error("没有信号来源时不得走本机文件路径")
	}
}

// `+CSQ: 255,99`（展锐的“测不出来”）不能覆盖已有的有效值。
func TestURCCSQ255IsSentinel(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")

	mod.feedURCLine(`+CSQ: 20,1`)
	if got := mod.signalSnapshot(); got.RSSI != 20 || got.BER != 1 {
		t.Fatalf("正常值应写入：%+v", got)
	}

	// 255 = 未上报 ⇒ 不能覆盖
	mod.feedURCLine(`+CSQ: 255,99`)
	if got := mod.signalSnapshot(); got.RSSI != 20 {
		t.Errorf("255 是哨兵，不该覆盖已有 RSSI，实际 %d", got.RSSI)
	}

	// 99/99 是 3GPP 的未上报哨兵：会被存下来（既有行为），但 validSignal 必须排除它
	mod.feedURCLine(`+CSQ: 99,99`)
	if _, _, _, rssi := validSignal(mod.signalSnapshot()); rssi != nil {
		t.Errorf("RSSI=99 应被视为未上报，实际 %d", *rssi)
	}
	// 255 万一从别的路径进来了（老固件/别的指令），也必须被排除
	if _, _, _, rssi := validSignal(SignalInfo{RSSI: 255}); rssi != nil {
		t.Errorf("RSSI=255 应被视为未上报，实际 %d", *rssi)
	}
	// 正常值仍要算得出来：20 → -113+2×20 = -73 dBm
	if _, _, _, rssi := validSignal(SignalInfo{RSSI: 20}); rssi == nil || *rssi != -73 {
		t.Errorf("RSSI=20 应换算成 -73 dBm，实际 %v", rssi)
	}
}
