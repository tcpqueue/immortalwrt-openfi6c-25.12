package kernel

// "本机文件/内核供状态"这一层的回归测试（跨平台：解析与分流都是纯逻辑，
// /proc/net/dev 的路径在测试里被换成临时文件）。
//
// ⚠️ 这一层最重要的一条是**边界**：它只针对展锐 SIPC（F50）这一族。
// 别的模组必须照旧走 AT —— 否则会把正常设备的刷新路径改道、让它们的地址/速率变空。

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const procNetDevHeader = "Inter-|   Receive                                                |  Transmit\n" +
	" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n"

// procNetDevLine 造一行 /proc/net/dev 记录（只把 bytes 填成真实值，其余给 0）。
func procNetDevLine(name string, rx, tx uint64) string {
	return fmt.Sprintf(" %s: %d 0 0 0 0 0 0 0 %d 0 0 0 0 0 0 0\n", name, rx, tx)
}

// 真机样本（展锐 SIPC：sipa_eth0 是 PDP 主承载，br-lan 与 lo 都在同一份表里）。
func TestProcNetDevCounters(t *testing.T) {
	// 用真机同时刻抓到的数字：sipa_eth0 rx=21747 tx=15597
	sample := procNetDevHeader +
		procNetDevLine("lo", 1234, 5678) +
		procNetDevLine("sipa_eth0", 21747, 15597) +
		procNetDevLine("sipa_eth1", 0, 0) +
		procNetDevLine("br-lan", 999, 111)
	got := procNetDevCounters(sample)

	if c, ok := got["sipa_eth0"]; !ok || c.rx != 21747 || c.tx != 15597 {
		t.Errorf("sipa_eth0 解析错：%+v ok=%v", c, ok)
	}
	if c := got["br-lan"]; c.rx != 999 || c.tx != 111 {
		t.Errorf("带连字符的接口名要能解析：%+v", c)
	}
	if c := got["lo"]; c.rx != 1234 || c.tx != 5678 {
		t.Errorf("lo 解析错：%+v", c)
	}
	if len(got) != 4 {
		t.Errorf("应解析出 4 个接口，实际 %d：%v", len(got), got)
	}

	// 脏数据不能把解析搞崩，也不能产出假接口
	bad := procNetDevHeader +
		"  broken no colon\n" +
		"  weird: 1 2 3\n" + // 列数不足
		"  signed: -1 0 0 0 0 0 0 0 5 0 0 0 0 0 0 0\n" + // 负数不是合法计数
		procNetDevLine("sipa_eth0", 10, 20)
	got = procNetDevCounters(bad)
	if _, ok := got["broken"]; ok {
		t.Error("没有冒号的行不该产出接口")
	}
	if _, ok := got["weird"]; ok {
		t.Error("列数不足的行不该产出接口")
	}
	if _, ok := got["signed"]; ok {
		t.Error("负数计数不该被当成合法值")
	}
	if c := got["sipa_eth0"]; c.rx != 10 || c.tx != 20 {
		t.Errorf("脏行不该影响正常行：%+v", c)
	}
	if _, ok := procNetDevCounters("")[""]; ok {
		t.Error("空内容不该产出接口")
	}
}

// 挑数据网卡：必须有厂商前缀，且优先"流量最大"的那条（PDP 主承载）。
func TestPickMu300DataIface(t *testing.T) {
	// 只有 sipa_eth1 有流量 ⇒ 选它（哪怕 sipa_eth0 名字更小）
	got := pickMu300DataIface(map[string]ifaceCounters{
		"sipa_eth0": {rx: 0, tx: 0},
		"sipa_eth1": {rx: 100, tx: 200},
		"br-lan":    {rx: 99999, tx: 99999},
		"lo":        {rx: 1, tx: 1},
	})
	if got != "sipa_eth1" {
		t.Errorf("应选流量最大的 sipa_eth1，实际 %q", got)
	}

	// 全都没流量（设备刚起来）⇒ 按名字取第一个，不能判成"没有数据面"
	got = pickMu300DataIface(map[string]ifaceCounters{
		"sipa_eth2": {rx: 0, tx: 0},
		"sipa_eth0": {rx: 0, tx: 0},
	})
	if got != "sipa_eth0" {
		t.Errorf("全零时应按名字取第一个，实际 %q", got)
	}

	// 没有任何厂商接口 ⇒ 空串（普通设备/开发机就是这种情况）
	if got := pickMu300DataIface(map[string]ifaceCounters{"eth0": {rx: 1, tx: 1}}); got != "" {
		t.Errorf("没有 sipa_ethN 时应返回空串，实际 %q", got)
	}
	if got := pickMu300DataIface(nil); got != "" {
		t.Errorf("空表应返回空串，实际 %q", got)
	}
}

func TestParseDecUint(t *testing.T) {
	for _, c := range []struct {
		in   string
		want uint64
		ok   bool
	}{
		{"0", 0, true},
		{"21747", 21747, true},
		{"", 0, false},
		{"-1", 0, false},
		{"1a", 0, false},
		{" 1", 0, false},
	} {
		got, ok := parseDecUint(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseDecUint(%q) = %d,%v，期望 %d,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// 地址读取：接口不存在就只能空手而归（Windows 开发机 / 数据面没起来都走这条）。
func TestIfaceIPAddrsMissing(t *testing.T) {
	if _, _, ok := ifaceIPAddrs("no-such-iface-xyz"); ok {
		t.Error("不存在的接口不该返回地址")
	}
	if _, _, ok := ifaceIPAddrs(""); ok {
		t.Error("空接口名不该返回地址")
	}
}

// 本机计数器 → 与模组 AT 计数器**完全相同**的那条速率路径（含配额累计）。
func TestSampleTrafficFromHost(t *testing.T) {
	oldPath := procNetDevPath
	dir := t.TempDir()
	path := filepath.Join(dir, "dev")
	procNetDevPath = path
	defer func() { procNetDevPath = oldPath }()

	write := func(rx, tx uint64) {
		content := procNetDevHeader +
			procNetDevLine("sipa_eth0", rx, tx) +
			procNetDevLine("br-lan", 1, 1)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("写测试 /proc/net/dev 失败：%v", err)
		}
	}

	mod := newURCTestModem("/dev/stty_nr2")
	write(1000, 500)
	// 第一次采样：有累计量、还没有速率基线
	r, ok := mod.SampleTrafficFromHost()
	if !ok {
		t.Fatal("应能从 sipa_eth0 的计数器采样")
	}
	if !r.Supported || r.Recv != 1000 || r.Sent != 500 {
		t.Errorf("首采样应给出累计量（收=1000/发=500）：%+v", r)
	}

	// 装一个 1 秒前的基线，再给新计数 ⇒ 速率 = 差分/1s，且方向不能反（发=tx、收=rx）
	mod.mu.Lock()
	mod.trafficPrev = &TrafficSample{Sent: 500, Recv: 1000, At: time.Now().Add(-time.Second)}
	mod.mu.Unlock()
	write(3000, 1500)
	r, ok = mod.SampleTrafficFromHost()
	if !ok {
		t.Fatal("第二次采样也应成功")
	}
	// ⚠️ dt 取自真实时钟（比 1 秒略大），所以只能按量级断言；
	// 方向不能反：上行 = tx 差分、下行 = rx 差分。
	if r.UpBps < 900 || r.UpBps > 1100 {
		t.Errorf("上行速率应约为 1000 B/s（来自 tx），实际 %v", r.UpBps)
	}
	if r.DownBps < 1800 || r.DownBps > 2200 {
		t.Errorf("下行速率应约为 2000 B/s（来自 rx），实际 %v", r.DownBps)
	}

	// 没有厂商网卡（普通机器）⇒ 明确返回 false，由调用方退回 AT
	write2 := procNetDevHeader + procNetDevLine("eth0", 100, 100)
	if err := os.WriteFile(path, []byte(write2), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := mod.SampleTrafficFromHost(); ok {
		t.Error("没有 sipa_ethN 时不该声称采样成功")
	}

	// 文件读不到（非 Linux / 权限）⇒ 也不能崩、不能声称成功
	procNetDevPath = filepath.Join(dir, "missing")
	if _, ok := mod.SampleTrafficFromHost(); ok {
		t.Error("计数器文件读不到时不该声称采样成功")
	}
}

// ⚠️⚠️ 边界：这条"零 AT 读文件"的路**只针对展锐 SIPC（F50）**。
func TestMu300FileBackedStateIsF50Only(t *testing.T) {
	// 普通 USB / PCIe 模组：即使 URC 标志被（错误地）置起来，也必须返回 false
	for _, port := range []string{"/dev/ttyUSB0", "/dev/ttyACM1", "/dev/mhi_DUN", "/dev/wwan0at0", "COM3", ""} {
		mod := newURCTestModem(port)
		mod.noteURCFeedState(true, nil)
		mod.feedURCLine(`+CSQ: 20,1`)
		mod.feedURCLine(`+CGREG: 1,"FFFE"`)
		if !mod.urcCoversSignal() || !mod.urcCoversNetwork() {
			t.Fatalf("前提不成立：%q 的 URC 覆盖标志没起来", port)
		}
		if mu300FileBackedState(mod) {
			t.Errorf("%q 不是展锐 SIPC，不得走“零 AT 读文件”这条路", port)
		}
	}

	// 展锐 SIPC：两道门都要过
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	if mu300FileBackedState(mod) {
		t.Error("只有信号、没有注册来源时不该走本机路径（注册会被清空）")
	}
	mod.feedURCLine(`+CGREG: 1,"FFFE"`)
	if !mu300FileBackedState(mod) {
		t.Error("信号 + 注册都有 URC 来源时应走本机路径")
	}
	// 跟随器不可用 ⇒ 立刻退回 AT 轮询
	mod.noteURCFeedState(false, errTestURCUnavailable)
	if mu300FileBackedState(mod) {
		t.Error("跟随器不可用时必须退回 AT 轮询")
	}
	// 空模组不能崩
	if mu300FileBackedState(nil) {
		t.Error("nil 模组必须返回 false")
	}
}

// 展锐 SIPC 的配额计量：靠本机计数器就能算，不再依赖模组 AT 计数器指令
// （这台机器的 AT+GTSTATIS? 根本没声明 ⇒ 以前配额恒为"不支持"）。
func TestTrafficSupportedViaHostCounters(t *testing.T) {
	oldPath := procNetDevPath
	dir := t.TempDir()
	path := filepath.Join(dir, "dev")
	procNetDevPath = path
	defer func() { procNetDevPath = oldPath }()
	if err := os.WriteFile(path, []byte(procNetDevHeader+procNetDevLine("sipa_eth0", 5, 5)), 0o644); err != nil {
		t.Fatal(err)
	}

	mod := newURCTestModem("/dev/stty_nr2")
	mod.cmdImpl = NewVendorCommand(VendorUnisoc)
	// 没有 URC 供应 ⇒ 照旧按厂商指令能力判断（这里展锐没声明流量指令 ⇒ 不支持）
	if mod.trafficSupported() {
		t.Error("没有本机文件供状态时，展锐不该被当成支持流量统计")
	}
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	mod.feedURCLine(`+CGREG: 1,"FFFE"`)
	if !mod.trafficSupported() {
		t.Error("本机计数器可用时，配额计量应视为支持")
	}
}
