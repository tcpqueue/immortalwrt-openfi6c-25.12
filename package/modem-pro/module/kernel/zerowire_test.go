package kernel

// "接口路径零 AT"这条不改动的护栏测试。
//
// 背景（2026-10-05 真机）：展锐 SIPC 的 AT 共享慢总线，一条往返 8~10 秒；
// 而 modemd 的接口与周期推送以前会把 8~10 条的完整网络查询按 3 秒一轮地打上去，
// 直接把总线占满 —— 结果就是"点哪个接口都卡"。
// 所以这里必须钉住两件事：
//  1. **展锐 SIPC 的接口路径永远不会走完整 AT 查询**（只读 URC 日志 / 内核 / 缓存）；
//  2. **别的模组一点都不受影响**（照旧走完整 AT 查询）。

import (
	"testing"
)

// 接口取数计划：只有"URC 真的在供状态"的展锐 SIPC 才允许走文件路径。
func TestNetworkQueryPlan(t *testing.T) {
	// ① 普通模组：即使（错误地）置了 URC 标志，也必须走完整 AT 查询
	for _, port := range []string{"/dev/ttyUSB0", "/dev/ttyACM1", "/dev/mhi_DUN", "/dev/wwan0at0", "COM3", ""} {
		mod := newURCTestModem(port)
		mod.noteURCFeedState(true, nil)
		mod.feedURCLine(`+CSQ: 20,1`)
		mod.feedURCLine(`+CGREG: 1,"FFFE"`)
		if got := mod.networkQueryPlan(); got != planFullAT {
			t.Errorf("%q 不是展锐 SIPC，网络状态必须照旧走完整 AT 查询，实际 %v", port, got)
		}
	}

	// ② 展锐 SIPC，但跟随器还没起来 ⇒ 也不能走文件路径（会拿空注册状态糊弄界面）
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	if got := mod.networkQueryPlan(); got != planFullAT {
		t.Errorf("只有信号来源时仍应走完整 AT 查询（注册会变空），实际 %v", got)
	}

	// ③ 展锐 SIPC + 信号和注册都有 URC 来源 ⇒ 接口路径零 AT
	mod.feedURCLine(`+CGREG: 1,"FFFE"`)
	if got := mod.networkQueryPlan(); got != planURC {
		t.Errorf("展锐 SIPC 且 URC 供着信号+注册时应走文件路径，实际 %v", got)
	}

	// ④ 跟随器掉了（文件读不到）⇒ 立刻退回完整 AT 查询，不能拿旧值装新鲜
	mod.noteURCFeedState(false, errTestURCUnavailable)
	if got := mod.networkQueryPlan(); got != planFullAT {
		t.Errorf("跟随器不可用时必须退回完整 AT 查询，实际 %v", got)
	}
}

// 注册查询接口（`/api/network`）：展锐 SIPC 上要返回**模组自己那行原文**，且不发 AT。
func TestQueryNetworkRegUsesURCRaw(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	mod.feedURCLine(`+CGREG: 1,"FFFE","0400E21E",7`)

	if !mu300FileBackedState(mod) {
		t.Fatal("前提：应已进入本机文件供状态的路径")
	}
	// 这里**没有串口**：一旦真去发 AT 就会暴露出来（nil 端口 / 直接失败）
	reg, err := mod.QueryNetworkReg()
	if err != nil {
		t.Fatalf("展锐 SIPC 上这个接口不该失败（它只读 URC）：%v", err)
	}
	if reg != `+CGREG: 1,"FFFE","0400E21E",7` {
		t.Errorf("应原样返回模组上报的那一行，实际 %q", reg)
	}
}

// urcRegRaw 的优先级：先 CS，再 PS，再 EPS/5GS —— 与 `AT+CREG?` 那条老路径一致。
func TestURCRegRawPreference(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	if got := mod.urcRegRaw(); got != "" {
		t.Errorf("什么都没收到时应返回空串，实际 %q", got)
	}
	mod.feedURCLine(`+CGREG: 1,"FFFE"`)
	if got := mod.urcRegRaw(); got != `+CGREG: 1,"FFFE"` {
		t.Errorf("只有 PS 时应返回 PS 那行，实际 %q", got)
	}
	mod.feedURCLine(`+CREG: 0,1`)
	if got := mod.urcRegRaw(); got != `+CREG: 0,1` {
		t.Errorf("CS 来了之后应优先给 CS，实际 %q", got)
	}
	// 扩展码（展锐 +CREG: 8) 含义未知 ⇒ 不该被采信、也不该把上面那行挤掉
	mod.feedURCLine(`+CREG: 8,"FFFE","0400E21E",7`)
	if got := mod.urcRegRaw(); got != `+CREG: 0,1` {
		t.Errorf("不认识的 stat 不该覆盖已知值，实际 %q", got)
	}
}

// 展锐 SIPC 上**没有任何后台 AT 补查**（用户要求：接口只读文件）。
// 这条钉子防的是“以后顺手加一个后台刷新” —— 那正是以前把总线占满的原因。
func TestNoBackgroundATFillForF50(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	mod.feedURCLine(`+CGREG: 1,"FFFE"`)
	if !mu300FileBackedState(mod) {
		t.Fatal("前提：应已进入本机文件供状态的路径")
	}
	if got := mod.networkQueryPlan(); got != planURC {
		t.Errorf("展锐 SIPC 上网络状态只允许走文件路径，实际 %v", got)
	}
}

// 普通模组不受影响：照旧每次都真查。
func TestNetworkQueryPlanFullATForOthers(t *testing.T) {
	mod := newURCTestModem("/dev/ttyUSB0")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	mod.feedURCLine(`+CGREG: 1,"FFFE"`)
	for i := 0; i < 5; i++ {
		if got := mod.networkQueryPlan(); got != planFullAT {
			t.Fatalf("非展锐模组必须照旧真查（现有设备行为不能变），实际 %v", got)
		}
	}
}
