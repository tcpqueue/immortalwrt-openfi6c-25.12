package kernel

// 展锐 SIPC（中兴 F50 / UMS9620）通道策略回归测试（跨平台，Windows 上也能跑）。
//
// ⚠️⚠️ 2026-10-05 用户明确要求：**只用 /dev/stty_nr2，nr1 那套一律不要**。
// 所以这里钉住的是"nr2 是唯一通道"、"别再把它当慢总线"、
// 以及"nr1 不能再被当成命令通道 / 厂商托管数据面"。
// 原来那一整套代理协议（FIFO / 锁目录 / 应答文件 / 超时分档 / 降频放大）
// 的测试已随实现一起删除 —— 那些东西在代码里也不存在了。

import (
	"modemd/module/dial"
	"modemd/module/netdev"
	"strings"
	"testing"
	"time"
)

// 第二条命令通道（nr2/nr3）必须被认出来：它是绕过厂商 daemon 那 1 秒 drain 的"快路"。
//
// 真机实测（2026-10-05）：同一条 `AT+CGMM`
//
//	/dev/stty_nr2 直连    ~0.01 s
//	厂商 daemon 路径      1.0~4.0 s
//
// 所以它不能被当成 tty（否则 O_NONBLOCK 第一拍被当"不应答"），
// 也不能被当成"共享慢总线"——否则会白白降频、放大超时。
func TestMu300DirectATChannel(t *testing.T) {
	for _, n := range []string{"/dev/stty_nr2", "stty_nr2", "stty_nr3", "/dev/stty_nr3", "/dev/stty_nr2/"} {
		if !netdev.IsMu300DirectATDevice(n) {
			t.Errorf("isMu300DirectATDevice(%q) 应为 true", n)
		}
		// 裸字符设备：读会 EAGAIN，ProbeAT 必须等到 deadline 才判死。
		// 这一条一旦漏，好口会被永久写进"非 AT 口"否定缓存（2026-10-05 踩了一整轮）。
		if !netdev.IsNonTTYATDevice(n) {
			t.Errorf("isNonTTYATDevice(%q) 必须为 true（否则探测第一拍就被判死）", n)
		}
		// 但**数据面仍然是厂商托管的** —— 这是模组的属性，与走哪条口无关。
		// 漏了这条：一旦 nr2 被选成 at_port，managedDataPlane 翻成 false，
		// modemd 就会去写 APN / 拉拨号器（认出的 host_iface 是 br-lan 成员口 usb0）⇒ 断网。
		if !netdev.ManagedDataPlane(n) {
			t.Errorf("%q 的数据面仍由厂商托管，managedDataPlane 必须为 true", n)
		}
	}
	// 其余 SIPC 通道（含 nr1）不得被误认成命令通道
	for _, n := range []string{"/dev/stty_nr0", "/dev/stty_nr1", "/dev/stty_nr4", "/dev/stty_nr31", "/dev/ttyUSB0", ""} {
		if netdev.IsMu300DirectATDevice(n) {
			t.Errorf("isMu300DirectATDevice(%q) 应为 false", n)
		}
	}

	// ⭐ F50 **只用第二条命令通道（nr2）**（用户要求：它是串口、速度很快）。
	// 扫描列表里有且只有 nr2。
	globs := netdev.Mu300ATScanGlobs()
	if len(globs) != 1 || globs[0] != netdev.Mu300DirectATChanName {
		t.Errorf("扫描通道应只有 %s（直连快路），实际 %v", netdev.Mu300DirectATChanName, globs)
	}
	if r := netdev.Mu300ATChanRank(netdev.Mu300DirectATChanName); r != 0 {
		t.Errorf("nr2 的优先序应为 0（最先），实际 %d", r)
	}
	if r := netdev.Mu300ATChanRank(netdev.Mu300DirectATChanAlt); r != 1 {
		t.Errorf("nr3 的优先序应为 1（备选），实际 %d", r)
	}

	// 两条 SIPC 口仍必须归到**同一台模组**（否则 groupPorts 会认成两台 —— 0.2.101 踩过）
	k1, ok1 := netdev.Mu300SIPCGroupKey("/dev/stty_nr0")
	k2, ok2 := netdev.Mu300SIPCGroupKey("/dev/stty_nr2")
	if !ok1 || !ok2 || k1 != k2 {
		t.Errorf("nr0/nr2 必须同组，实际 %q/%q ok=%v/%v", k1, k2, ok1, ok2)
	}
	if _, ok := netdev.Mu300SIPCGroupKey("/dev/ttyUSB0"); ok {
		t.Error("普通 tty 不该被归到展锐 SIPC 组")
	}

	// 直连口的探测窗口要**短**：不答就是没起来，不该陪它等满 tty 那档。
	// 这里不比对 Cfg（单测里没加载配置），只钉"必须明显短"。
	if w := netdev.AtProbeWindow("/dev/stty_nr2"); w != netdev.Mu300DirectProbeWindow {
		t.Errorf("直连口探测窗口 = %v，应为 %v", w, netdev.Mu300DirectProbeWindow)
	}
	if netdev.Mu300DirectProbeWindow >= 2*time.Second {
		t.Errorf("直连口探测窗口 %v 太长：健康时 ~10ms 就答，不答就是模组没起来", netdev.Mu300DirectProbeWindow)
	}

	// ⭐ 直连口读循环的空闲节拍必须**明显短于**预设的 100 ms：
	// O_NONBLOCK 下它就是应答延迟的下限，100 ms 会把 ~10 ms 的快路拖成 ~110 ms。
	if d := netdev.ReadIdleDelay("/dev/stty_nr2"); d != netdev.Mu300DirectReadIdle || d >= 100*time.Millisecond {
		t.Errorf("直连口读循环节拍 = %v，应为 %v 且 < 100ms", d, netdev.Mu300DirectReadIdle)
	}
	// 其它通道行为不变（含 nr1 与 tty）：保持 100 ms
	for _, p := range []string{"/dev/ttyUSB0", "/dev/stty_nr1", "/dev/mhi_DUN"} {
		if d := netdev.ReadIdleDelay(p); d != 100*time.Millisecond {
			t.Errorf("非直连通道的读循环节拍应保持 100ms，实际 %v（%s）", d, p)
		}
	}

	// 直连快路的重探间隔必须远小于指数退避封顶（5 分钟），
	// 否则模组醒来了也认不出来（真机踩到过）。
	if netdev.Mu300DirectRetryInterval >= nonATBackoffMax {
		t.Errorf("直连快路重探间隔 %v 应远小于退避封顶 %v",
			netdev.Mu300DirectRetryInterval, nonATBackoffMax)
	}
	base := time.Unix(1000, 0)
	if got := netdev.Mu300DirectRetryNext(base); !got.Equal(base.Add(netdev.Mu300DirectRetryInterval)) {
		t.Errorf("mu300DirectRetryNext = %v，应为 +%v", got, netdev.Mu300DirectRetryInterval)
	}

	// 展锐 SIPC 通道判据要盖住整族（含命令口与 URC 口），否则分组会碎掉
	for _, n := range []string{"/dev/stty_nr0", "/dev/stty_nr1", "/dev/stty_nr2", "/dev/stty_nr3"} {
		if !netdev.IsMu300SIPCChan(n) {
			t.Errorf("isMu300SIPCChan(%q) 应为 true", n)
		}
	}
	if netdev.IsMu300SIPCChan("/dev/ttyUSB0") {
		t.Error("普通 tty 不该被判成展锐 SIPC 通道")
	}
}

// ⚠️⚠️ 用户要求"nr1 都换了，不要保留" —— 这条护栏专门钉它别悄悄回来。
//
// nr1 是厂商 daemon（mu300-atd）独占的代理口：单条 AT 1~4 秒，
// 它会抢走设备数据面的队。任何把它重新当成命令通道的改动都会红。
func TestNR1IsNotAChannel(t *testing.T) {
	const nr1 = "/dev/stty_nr1"
	if netdev.Mu300ATChanName != netdev.Mu300DirectATChanName {
		t.Errorf("mu300ATChanName(%q) 必须就是直连通道 %q —— nr1 已彻底移除",
			netdev.Mu300ATChanName, netdev.Mu300DirectATChanName)
	}
	if netdev.IsMu300DirectATDevice(nr1) {
		t.Error("nr1 不能再被当成 AT 命令通道")
	}
	if netdev.Mu300ATChanRank(nr1) != -1 {
		t.Error("nr1 不该还有优先序")
	}
	if netdev.ManagedDataPlane(nr1) {
		t.Error("nr1 不再是 at_port，不该按'厂商托管'跳过组网")
	}
	for _, g := range netdev.Mu300ATScanGlobs() {
		if g == nr1 {
			t.Error("nr1 不该出现在扫描列表里")
		}
	}
	// 但它仍是 SIPC 族的成员（同一台模组的 URC/数据口），分组判据要认
	if !netdev.IsMu300SIPCChan(nr1) {
		t.Error("nr1 仍是展锐 SIPC 的通道（只是不当命令口用）")
	}
}

// ⚠️⚠️ 这条是"别把用户网络弄断"的护栏，比别的都重要。
//
// 实测背景：展锐 SIPC 这类 SoC 内置模组没有自己的 USB 网卡，modemd 会把
// **`usb0`（LAN 桥 br-lan 的成员口）**当成它的数据网卡（实机 /api/dial 里
// `host_iface: "usb0"`）。一旦让自动组网按这个走，就会去写 APN / 激活 PDP /
// 建 WAN 接口把 usb0 拉进去 —— 那是直接动用户的局域网。
func TestManagedDataPlaneSkipsAutoWAN(t *testing.T) {
	if !netdev.ManagedDataPlane(netdev.Mu300DirectATChanName) {
		t.Fatal("展锐 SIPC 命令通道（nr2）必须被认成\"厂商托管数据面\"")
	}
	// nr3 也是同一台 SIPC 模组的命令口 —— 一旦被选成 at_port，
	// 它必须仍然是“厂商托管数据面”，否则 modemd 会去拉 user 的 br-lan 成员口 usb0。
	for _, p := range []string{"/dev/stty_nr3"} {
		if !netdev.ManagedDataPlane(p) {
			t.Errorf("%q 是同一台 SIPC 模组的命令通道，必须被认成厂商托管数据面", p)
		}
	}
	for _, p := range []string{"/dev/ttyUSB0", "/dev/ttyACM1", "/dev/mhi_DUN", "", "/dev/stty_nr0", "/dev/stty_nr1"} {
		if netdev.ManagedDataPlane(p) {
			t.Errorf("%q 不该被当成厂商托管数据面（会让正常模组不再组网）", p)
		}
	}

	// 主机侧拨号器：这类模组的停用原因要说清楚，不能是空串（空串 = 该跑）。
	// ⚠️ 这里刻意用**没启动**的 Modem：它证明这一条排在"模组不在了"**之前** ——
	// 否则运行时会被后面那条抢走，界面上就看不到真正的原因。
	mod := &Modem{ATPort: netdev.Mu300DirectATChanName}
	reason := hostDialStopReason(mod, &dial.DialState{}, "usb0")
	if reason == "" {
		t.Fatal("SoC 内置模组不该拉主机侧拨号器（会把 usb0/LAN 当模组网卡）")
	}
	if !strings.Contains(reason, "厂商托管") {
		t.Errorf("停用原因该说清是厂商托管数据面，实际 %q（是不是被后面的分支抢了？）", reason)
	}
}
