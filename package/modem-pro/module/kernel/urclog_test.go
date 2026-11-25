package kernel

// URC 日志跟随器的回归测试（跨平台：跟随器只碰普通文件，不碰设备节点）。
//
// 这一层钉住的是"能不能拿厂商已经在写的 URC 日志顶掉一部分 AT 轮询"这件事的分寸：
//   · 文件读得到 + 确实收到过 +CSQ → 才允许省掉 AT+CSQ（而且仍有 60 秒兜底）；
//   · 文件不在 / 跟随失败 → **立刻退回**原来的轮询行为（不能因为省流量把状态显示搞死）；
//   · 截断 / 轮转 / 半行 → 不能因此丢行、也不能重复消费。

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newURCTestModem 造一个"只够跟随器用"的模组（不起串口、不起写协程）。
//
// ⚠️ cmdImpl 必须给一个兜底指令集：parseLine 末尾会调
// `mod.cmdImpl.ParseNRSignal(line)` —— 真机上它由 ModemManager 建实例时绑好，
// 单测里不给就会在收到任意一行时空指针（踩过一次）。
func newURCTestModem(port string) *Modem {
	return &Modem{
		ID:       0,
		ATPort:   port,
		readChan: make(chan string, 256),
		cmdImpl:  NewVendorCommand(VendorUnknown),
	}
}

func writeURCLog(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写测试 URC 日志失败：%v", err)
	}
}

// 只认展锐 SIPC 通道：别的模组不该起跟随器（否则会去读一个不存在的文件、
// 白耗一轮 IO，还要在状态里留个假的“跟随失败”）。
func TestURCLogPathOnlyForSIPC(t *testing.T) {
	for _, p := range []string{"/dev/stty_nr2", "/dev/stty_nr2", "/dev/stty_nr3"} {
		if got := mu300URCLogPathFor(p); got != mu300URCLogPath {
			t.Errorf("mu300URCLogPathFor(%q) = %q，应为 %q", p, got, mu300URCLogPath)
		}
	}
	for _, p := range []string{"/dev/ttyUSB0", "/dev/ttyACM1", "/dev/mhi_DUN", "", "/dev/wwan0at0"} {
		if got := mu300URCLogPathFor(p); got != "" {
			t.Errorf("mu300URCLogPathFor(%q) = %q，应为空串（非展锐模组不该跟随）", p, got)
		}
	}
}

// 行分类：信号类 / 注册 PDP 类必须认全（它们决定能省掉哪些轮询）。
func TestURCLineClassification(t *testing.T) {
	signal := []string{
		"+CSQ: 39,25",
		"+CESQ: 99,99,255,255,25,39,255,255,255",
		"+QCSQ: -65,-10",
		"^RSSI: 20",
	}
	for _, l := range signal {
		if !mu300URCLineSaysSignal(l) {
			t.Errorf("%q 应被判为信号类上报", l)
		}
	}
	network := []string{
		`+CREG: 2`,
		`+CGREG: 1,"FFFE","0400E21E",7`,
		`+CEREG: 1,"404A","0400E21E",7`,
		"+CGEV: ME PDN ACT 1",
		"^CONN: 11,2,2",
		"+SPNRINDICATE: 1,1",
		"+SPPCODATA: 1",
	}
	for _, l := range network {
		if !mu300URCLineSaysNetwork(l) {
			t.Errorf("%q 应被判为注册/PDP 类上报", l)
		}
	}
	// 既不是信号也不是网络：不能拿来当"可以省轮询"的依据
	for _, l := range []string{"", "OK", "+SPPCI: 0000009B", `+CTZV: "26/10/05,13:47:03+32,0"`} {
		if mu300URCLineSaysSignal(l) {
			t.Errorf("%q 不该被判为信号类", l)
		}
		if mu300URCLineSaysNetwork(l) {
			t.Errorf("%q 不该被判为注册/PDP 类", l)
		}
	}
}

// 跟随器主体：增量消费、喂给 parseLine（而不是 readChan）、并维护覆盖标志。
func TestURCLogTailFeedsParseLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stty_nr0.log")

	mod := newURCTestModem("/dev/stty_nr2")
	if mod.urcCoversSignal() {
		t.Fatal("还没读到任何 URC，不该声称覆盖信号")
	}

	// 第一批：CSQ（parseLine 会写 RSSI/BER）
	writeURCLog(t, path, "+CSQ: 39,25\n")
	var off int64
	mod.tailURCLogOnce(path, &off)

	if !mod.urcCoversSignal() {
		t.Error("收到 +CSQ 之后应声称覆盖信号（可以省 AT+CSQ 轮询）")
	}
	if mod.urcCoversNetwork() {
		t.Error("还没见到注册/PDP 类上报，不该声称覆盖网络状态")
	}
	if got := mod.signalSnapshot(); got.RSSI != 39 || got.BER != 25 {
		t.Errorf("parseLine 应把 +CSQ 写进信号缓存，实际 RSSI=%d BER=%d", got.RSSI, got.BER)
	}
	// ⚠️ 关键：不能往 readChan 塞 —— 那是 AT 应答队列，塞进去会串台
	select {
	case l := <-mod.readChan:
		t.Errorf("URC 行不该进 readChan（会被在途指令当成应答），却收到 %q", l)
	default:
	}

	// 增量：只消费新增部分，不重复解析旧行
	writeURCLog(t, path, "+CSQ: 39,25\n+CEREG: 1,\"404A\"\n")
	mod.tailURCLogOnce(path, &off)
	if !mod.urcCoversNetwork() {
		t.Error("收到 +CEREG 之后应声称覆盖网络状态")
	}
	if off != int64(len("+CSQ: 39,25\n+CEREG: 1,\"404A\"\n")) {
		t.Errorf("offset 应停在文件末尾，实际 %d", off)
	}

	// 半行：解析器不消费不完整的行，offset 也不能越过它（否则那行就丢了）
	writeURCLog(t, path, "+CSQ: 39,25\n+CEREG: 1,\"404A\"\n+CSQ: 7,")
	mod.tailURCLogOnce(path, &off)
	if want := int64(len("+CSQ: 39,25\n+CEREG: 1,\"404A\"\n")); off != want {
		t.Errorf("半行不该被消费：offset = %d，应为 %d", off, want)
	}

	// 补全那一行后应被正常消费
	writeURCLog(t, path, "+CSQ: 39,25\n+CEREG: 1,\"404A\"\n+CSQ: 7,3\n")
	mod.tailURCLogOnce(path, &off)
	if got := mod.signalSnapshot(); got.RSSI != 7 || got.BER != 3 {
		t.Errorf("补全后的行应被解析：RSSI=%d BER=%d，期望 7/3", got.RSSI, got.BER)
	}
}

// 截断 / 轮转（daemon 重启会把文件重写）：必须从头跟，不能因为 offset 比文件大就卡死。
func TestURCLogTailHandlesTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stty_nr0.log")
	mod := newURCTestModem("/dev/stty_nr2")
	var off int64

	writeURCLog(t, path, "+CSQ: 11,1\n+CEREG: 2\n")
	mod.tailURCLogOnce(path, &off)
	if got := mod.signalSnapshot(); got.RSSI != 11 {
		t.Fatalf("首批应解析出 RSSI=1 1，实际 %d", got.RSSI)
	}

	// 截断重写（新内容更短）
	writeURCLog(t, path, "+CSQ: 22,2\n")
	mod.tailURCLogOnce(path, &off)
	if got := mod.signalSnapshot(); got.RSSI != 22 || got.BER != 2 {
		t.Errorf("截断后应从新内容重新跟：RSSI=%d BER=%d，期望 22/2", got.RSSI, got.BER)
	}
}

// 文件不在 / 跟随失败：覆盖标志必须立刻为假 ⇒ 轮询退回原来的行为。
// 这是"省流量不能把状态搞死"的保险。
func TestURCLogMissingFallsBackToPolling(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.log")
	mod := newURCTestModem("/dev/stty_nr2")
	var off int64

	mod.tailURCLogOnce(path, &off)
	if mod.urcFeedOK {
		t.Error("文件不存在时跟随器不该报告正常")
	}
	if mod.urcCoversSignal() {
		t.Error("跟随器不可用时不得声称覆盖信号")
	}
	if !mod.shouldPollSignal(time.Now()) {
		t.Error("跟随器不可用时**必须**照旧轮询 AT+CSQ")
	}

	// 文件后来出现：恢复覆盖，并且兜底节拍开始生效
	writeURCLog(t, path, "+CSQ: 5,0\n")
	mod.tailURCLogOnce(path, &off)
	if !mod.urcCoversSignal() {
		t.Fatal("文件出现且含 +CSQ 后应恢复覆盖信号")
	}
	now := time.Now()
	if !mod.shouldPollSignal(now) {
		t.Error("第一次判定应放行（做一次真查询，确认 URC 与总线一致）")
	}
	if mod.shouldPollSignal(now.Add(time.Second)) {
		t.Error("刚刚问过，1 秒后不该再问（这就是省下来的总线流量）")
	}
	if !mod.shouldPollSignal(now.Add(urcSignalPollInterval)) {
		t.Error("到了兜底间隔必须再问一次（URC 停了也能自愈）")
	}
}

// 覆盖判据的两道门槛都必须生效：文件可读 **且** 真的见过该类上报。
func TestURCCoverageRequiresBothFeedAndKind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stty_nr0.log")
	// 只有注册类上报，没有信号类
	writeURCLog(t, path, "+CEREG: 1,\"404A\"\n")
	mod := newURCTestModem("/dev/stty_nr2")
	var off int64
	mod.tailURCLogOnce(path, &off)

	if !mod.urcCoversNetwork() {
		t.Error("见过 +CEREG 应覆盖网络状态")
	}
	if mod.urcCoversSignal() {
		t.Error("没见过信号类上报时不得省 AT+CSQ")
	}
	if !mod.shouldPollSignal(time.Now()) {
		t.Error("信号未被 URC 覆盖时必须照旧轮询")
	}
}

// 跟随器不会因为日志文件里塞了这个模组不认识的厂商行而崩掉或改动 AT 应答队列。
func TestURCLogIgnoresUnknownVendorLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stty_nr0.log")
	mod := newURCTestModem("/dev/stty_nr2")
	var off int64

	var sb []byte
	for i := 0; i < 200; i++ {
		sb = append(sb, []byte(fmt.Sprintf("+GTCCINFO: / 1,9,460,11,CFA103,%d\n", i))...)
	}
	sb = append(sb, []byte("+SPERRLOG: deadbeef\n")...)
	writeURCLog(t, path, string(sb))
	mod.tailURCLogOnce(path, &off)

	select {
	case l := <-mod.readChan:
		t.Errorf("任何 URC 行都不该进 readChan，却收到 %q", l)
	default:
	}
}

// closeChan 关闭后跟随协程必须退出（Start 会重建 closeChan，别把协程漏在那）。
func TestURCLogLoopStopsOnClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stty_nr0.log")
	writeURCLog(t, path, "+CSQ: 1,1\n")

	mod := newURCTestModem("/dev/stty_nr2")
	closeChan := make(chan struct{})
	done := make(chan struct{})
	go func() {
		mod.urcLogTailLoop(path, closeChan)
		close(done)
	}()

	// 给它至少一轮的时间把文件读进来
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !mod.urcCoversSignal() {
		time.Sleep(20 * time.Millisecond)
	}
	if !mod.urcCoversSignal() {
		t.Error("跟随协程应在 3 秒内读到测试文件")
	}
	close(closeChan)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("closeChan 关闭后跟随协程应立刻退出")
	}
}
