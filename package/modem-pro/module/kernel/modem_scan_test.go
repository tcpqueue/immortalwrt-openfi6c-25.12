package kernel

// "否定缓存要不要作废重探" + "该不该自动 USB 软重插" 的测试。
//
// 背景（真机踩到的）：
//   - 重启后 modemd 比模组先就绪 —— option 刚被 new_id 绑上、AT 口还没应答，
//     第一轮探测全失败就被缓存成"非 AT 口"，之后每轮都跳过，**永远认不出这台模组**（重启 modemd 才好）。
//   - 模组冷启动后固件卡死时 AT 完全不应答（USB 枚举/驱动都正常、没人占口），
//     重启路由器没用（并不断模组的电），而把 authorized 写 0→1（真重新枚举）能救回来。

import (
	"testing"
	"time"
)

func TestShouldRescueUSB(t *testing.T) {
	now := time.Now()
	long := now.Add(-30 * time.Minute) // 早就没有 AT 口了
	cases := []struct {
		name    string
		known   int
		rescued int
		noAT    time.Time
		last    time.Time
		want    bool
	}{
		{"没 AT 口很久 + 有已知模组 + 从没救过 → 救", 1, 0, long, time.Time{}, true},
		{"刚上电 1 分钟 → 再等等（模组内部系统还在起，别打断它）", 1, 0, now.Add(-time.Minute), time.Time{}, false},
		{"场上没有已知模组设备 → 不碰（万一是 U 盘/鼠标）", 0, 0, long, time.Time{}, false},
		{"从没出现过「一个 AT 口都没有」的情况 → 不救", 1, 0, time.Time{}, time.Time{}, false},
		{"刚救过 1 分钟 → 冷却中", 1, 1, long, now.Add(-time.Minute), false},
		{"救过 6 分钟还没好、次数没到上限 → 再救一次", 1, 1, long, now.Add(-6 * time.Minute), true},
		{"已经救了 3 次 → 认输（只告警一次，等拔插断电）", 1, usbRescueMax, long, now.Add(-time.Hour), false},
	}
	for _, c := range cases {
		if got := shouldRescueUSB(c.known, c.rescued, c.noAT, c.last, now); got != c.want {
			t.Errorf("%s: shouldRescueUSB = %v, want %v", c.name, got, c.want)
		}
	}
	// 参数：150 秒等待 / 5 分钟冷却 / 最多 3 次（改了要同步注释与文档）
	if usbRescueAfter != 150*time.Second || usbRescueCooling != 5*time.Minute || usbRescueMax != 3 {
		t.Errorf("usbRescueAfter=%v usbRescueCooling=%v usbRescueMax=%d，与注释/文档不一致",
			usbRescueAfter, usbRescueCooling, usbRescueMax)
	}
	// 真机日志：重启路由器后模组 AT 不应答，一直等到 **102 秒**时它自己就好了（随后拨号 OK）。
	// 所以救早了会把启动中的模组打断，而且重插后重新计时 → 永远起不来的死循环。
	if usbRescueAfter <= 2*time.Minute {
		t.Errorf("usbRescueAfter=%v 太短：比实测的模组启动时间（~102s）还短，会打断正在启动的模组", usbRescueAfter)
	}
}

func TestShouldReProbeNonAT(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		modems  int
		lastNeg time.Time
		want    bool
	}{
		{"没有模组 + 否定结果已过期 → 重探", 0, now.Add(-70 * time.Second), true},
		{"没有模组 + 否定结果还新鲜 → 别急", 0, now.Add(-10 * time.Second), false},
		{"没有模组 + 从没写过否定结果 → 不用清", 0, time.Time{}, false},
		{"已经有模组了 → 不清整份缓存（改由按端口退避重探）", 1, now.Add(-10 * time.Minute), false},
	}
	for _, c := range cases {
		if got := shouldReProbeNonAT(c.modems, c.lastNeg, now); got != c.want {
			t.Errorf("%s: shouldReProbeNonAT(%d, %v) = %v, want %v", c.name, c.modems, c.lastNeg, got, c.want)
		}
	}
}

// 用户实测场景：本来就插着一台模组（4G），启动后另一台**慢启动的 5G 模组**第一轮
// AT 探测超时被记成"非 AT 口"。旧逻辑"场上有模组就永不重探"会让 5G 永远出不来；
// 现在必须有重探计划，且到点后允许再探（退避期间才跳过）。
func TestNonATPortBackoff(t *testing.T) {
	now := time.Now()
	mgr := &ModemManager{
		modems:     map[int]*Modem{0: {}}, // 场上已有一台模组
		nonATPorts: map[string]nonATRecord{},
	}
	// 模拟第一轮探测超时被否定
	rec := mgr.nonATPorts["/dev/ttyUSB4"]
	rec.Tries++
	rec.Next = now.Add(nonATBackoff(rec.Tries))
	mgr.nonATPorts["/dev/ttyUSB4"] = rec

	if !mgr.skipNonAT("/dev/ttyUSB4", now) {
		t.Error("退避期内应跳过（别每轮都白等探测超时）")
	}
	if mgr.skipNonAT("/dev/ttyUSB4", now.Add(nonATBackoff(1)+time.Millisecond)) {
		t.Error("到点后必须允许重探 —— 否则慢启动的 5G 模组永远找不到")
	}
	if mgr.skipNonAT("/dev/ttyUSB9", now) {
		t.Error("没有记录的口不该跳过")
	}
	// 探测成功后要清掉记录
	delete(mgr.nonATPorts, "/dev/ttyUSB4")
	if mgr.skipNonAT("/dev/ttyUSB4", now) {
		t.Error("清除后不该再跳过")
	}
}

func TestNonATBackoff(t *testing.T) {
	if got := nonATBackoff(0); got != nonATBackoffBase {
		t.Errorf("nonATBackoff(0) = %v, want %v（0/负数当 1 次）", got, nonATBackoffBase)
	}
	if got := nonATBackoff(1); got != nonATBackoffBase {
		t.Errorf("nonATBackoff(1) = %v, want %v", got, nonATBackoffBase)
	}
	if got := nonATBackoff(2); got != 2*nonATBackoffBase {
		t.Errorf("nonATBackoff(2) = %v, want %v", got, 2*nonATBackoffBase)
	}
	if got := nonATBackoff(1000); got != nonATBackoffMax {
		t.Errorf("nonATBackoff 应封顶 %v，实际 %v", nonATBackoffMax, got)
	}
	prev := time.Duration(0)
	for i := 1; i <= 30; i++ {
		d := nonATBackoff(i)
		if d < prev {
			t.Fatalf("nonATBackoff 应单调不减：nonATBackoff(%d)=%v < %v", i, d, prev)
		}
		if d > nonATBackoffMax {
			t.Fatalf("nonATBackoff(%d)=%v 超过上限 %v", i, d, nonATBackoffMax)
		}
		prev = d
	}
}

func TestNonATNextProbe(t *testing.T) {
	now := time.Now()
	// 没有预热（Windows / 已过预热）：纯退避
	if got := nonATNextProbe(1, now, time.Time{}); !got.Equal(now.Add(nonATBackoffBase)) {
		t.Errorf("无预热时应取退避：got %v, want %v", got, now.Add(nonATBackoffBase))
	}
	// 预热已经过去（warmupEnd 在过去）：按退避
	if got := nonATNextProbe(1, now, now.Add(-time.Second)); !got.Equal(now.Add(nonATBackoffBase)) {
		t.Errorf("已过预热时应取退避：got %v, want %v", got, now.Add(nonATBackoffBase))
	}
	// 还在预热期：固定短间隔重探 —— 别干等到预热结束（重插同一台模组的场景）
	warm := now.Add(2 * time.Minute)
	got := nonATNextProbe(99, now, warm)
	if !got.Equal(now.Add(warmupProbeEvery)) {
		t.Errorf("预热期内应固定 %v 重探：got %v", warmupProbeEvery, got)
	}
	if !got.Before(warm) {
		t.Errorf("预热期内不该等到预热结束才重探：got %v, warmupEnd %v", got, warm)
	}
	// 预热马上就结束：等到预热结束那一刻，而不是再往后拖一个 warmupProbeEvery
	soon := now.Add(time.Second)
	if got := nonATNextProbe(1, now, soon); !got.Equal(soon) {
		t.Errorf("预热快结束时应在预热结束点重探：got %v, want %v", got, soon)
	}
}
