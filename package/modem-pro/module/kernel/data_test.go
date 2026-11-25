package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"strings"
	"testing"
	"time"

	"modemd/module/common"
	"modemd/module/config"
	"modemd/module/fibocom"
	"modemd/module/meig"
	"modemd/module/quectel"
	simcompkg "modemd/module/simcom"
	"modemd/module/sms"
	"modemd/module/tdtech"
)

// ---- from: traffic_test.go ----

// ---- from: traffic_test.go ----
// 解析 / 格式化的纯逻辑用例在 data.go；
// 这里只测差分、积分、厂商指令这几个需要 *Modem 的部分。
// TestTrafficRateDelta 速率 = 计数差值 ÷ 时间差
// 解析 / 格式化的纯逻辑用例在 data.go；
// 这里只测差分、积分、厂商指令这几个需要 *Modem 的部分。
// TestTrafficRateDelta 速率 = 计数差值 ÷ 时间差

func TestTrafficRateDelta(t *testing.T) {
	mod := &Modem{ID: 0}
	t0 := time.Now()

	first := mod.applyTrafficSample(1000, 2000, t0)
	if !first.Supported {
		t.Fatal("首次采样应标记为支持")
	}
	if first.UpBps != 0 || first.DownBps != 0 {
		t.Fatalf("首次采样没有基线，速率应为 0，实际 %v/%v", first.UpBps, first.DownBps)
	}
	if first.UpText != "0 B/s" {
		t.Fatalf("首次采样文本 = %q", first.UpText)
	}

	// 10 秒内上行 +20480 字节、下行 +10240 字节 → 2 KB/s、1 KB/s
	second := mod.applyTrafficSample(1000+20480, 2000+10240, t0.Add(10*time.Second))
	if second.UpBps != 2048 || second.DownBps != 1024 {
		t.Fatalf("速率 = %.0f/%.0f B/s, want 2048/1024", second.UpBps, second.DownBps)
	}
	if second.UpText != "2.0 KB/s" || second.DownText != "1.0 KB/s" {
		t.Fatalf("文本 = %q / %q", second.UpText, second.DownText)
	}
	if second.Interval != 10 {
		t.Fatalf("采样窗 = %v, want 10", second.Interval)
	}
	if s := mod.TrafficSnapshot(); s.UpBps != 2048 {
		t.Fatalf("快照应返回最近一次速率，实际 %.0f", s.UpBps)
	}
}

// TestTrafficRateCounterReset 计数器回退（模组侧 PDP 重建）不能算出负速度
func TestTrafficRateCounterReset(t *testing.T) {
	mod := &Modem{ID: 0}
	t0 := time.Now()
	mod.applyTrafficSample(100000, 200000, t0)
	reset := mod.applyTrafficSample(10, 20, t0.Add(5*time.Second))
	if reset.UpBps != 0 || reset.DownBps != 0 {
		t.Fatalf("计数器回退后速率应为 0，实际 %.0f/%.0f", reset.UpBps, reset.DownBps)
	}
	if reset.Sent != 10 || reset.Recv != 20 {
		t.Fatalf("基线应重置到新值，实际 %d/%d", reset.Sent, reset.Recv)
	}
	// 回退后的下一次采样应基于新基线正常计算
	next := mod.applyTrafficSample(10+1024, 20, t0.Add(6*time.Second))
	if next.UpBps != 1024 {
		t.Fatalf("新基线后速率 = %.0f, want 1024", next.UpBps)
	}
}

// TestTrafficRateShortInterval 采样窗过短时沿用上一次速率（避免计数器粒度放大误差）
func TestTrafficRateShortInterval(t *testing.T) {
	mod := &Modem{ID: 0}
	t0 := time.Now()
	mod.applyTrafficSample(0, 0, t0)
	mod.applyTrafficSample(20480, 0, t0.Add(10*time.Second)) // 2 KB/s
	quick := mod.applyTrafficSample(20480, 0, t0.Add(10*time.Second+100*time.Millisecond))
	if quick.UpBps != 2048 {
		t.Fatalf("采样窗过短时应沿用上次速率，实际 %.0f", quick.UpBps)
	}
}

// TestSampleTrafficUnsupportedVendor 未声明流量指令的厂商：直接返回不支持，不发指令
func TestSampleTrafficUnsupportedVendor(t *testing.T) {
	mod := &Modem{ID: 0, cmdImpl: &simcompkg.SimcomCommand{}} // 芯讯通实测 QGDCNT 回 ERROR
	if r := mod.SampleTraffic(); r.Supported {
		t.Fatalf("芯讯通未声明流量指令，不应上报：%+v", r)
	}
}

// ====================== 瞬时速率式（广和通 AT+GTSTATIS?） ======================

// TestTrafficRateMode 速率式模组：速率直接用，累计靠“速率 × 时间窗”积分（标为估算）
func TestTrafficRateMode(t *testing.T) {
	mod := &Modem{ID: 0}
	t0 := time.Now()

	// 首次采样：没有时间窗，速率照报（模组自己测的），累计从 0 开始
	first := mod.applyTrafficRates(1024, 2048, t0)
	if !first.Supported || !first.Estimated {
		t.Fatalf("应支持并标记为估算：%+v", first)
	}
	if first.UpBps != 1024 || first.DownBps != 2048 {
		t.Fatalf("速率应直接采用模组上报值，实际 %.0f/%.0f", first.UpBps, first.DownBps)
	}
	if first.Sent != 0 || first.Recv != 0 {
		t.Fatalf("首次采样没有时间窗，累计应为 0，实际 %d/%d", first.Sent, first.Recv)
	}

	// 10 秒后：累计 = 速率 × 10
	second := mod.applyTrafficRates(1024, 2048, t0.Add(10*time.Second))
	if second.Sent != 10240 || second.Recv != 20480 {
		t.Fatalf("累计积分 = %d/%d, want 10240/20480", second.Sent, second.Recv)
	}
	if second.Interval != 10 {
		t.Fatalf("采样窗 = %v, want 10", second.Interval)
	}

	// 采样窗过短：速率照用（模组直接给的，不需要分母），但不再积分新字节
	quick := mod.applyTrafficRates(4096, 0, t0.Add(10*time.Second+100*time.Millisecond))
	if quick.UpBps != 4096 {
		t.Fatalf("瞬时速率应直接采用新值，实际 %.0f", quick.UpBps)
	}
	if quick.Sent != 10240 {
		t.Fatalf("过短的采样窗不应积分，实际 %d", quick.Sent)
	}

	// 清零后回到起点
	clearTrafficBaseline(mod)
	zero := mod.applyTrafficRates(0, 0, t0.Add(20*time.Second))
	if zero.Sent != 0 || zero.Recv != 0 {
		t.Fatalf("清零后累计应为 0，实际 %d/%d", zero.Sent, zero.Recv)
	}
}

// TestTrafficSampleShapes 采样时按**应答形状**分流：GTSTATIS 走速率模式，QGDCNT 走差分模式
func TestTrafficSampleShapes(t *testing.T) {
	if up, down, ok := ParseRates([]string{"+GTSTATIS: 100,200,0,0"}); !ok || up != 200 || down != 100 {
		t.Fatalf("GTSTATIS 应走速率模式：%.0f/%.0f/%v", up, down, ok)
	}
	if _, _, ok := ParseRates([]string{"+QGDCNT: 100,200"}); ok {
		t.Fatal("QGDCNT 应答不应被当成 GTSTATIS")
	}
	if s, r, ok := ParseCounters([]string{"+QGDCNT: 100,200"}); !ok || s != 100 || r != 200 {
		t.Fatalf("QGDCNT 应走差分模式：%d/%d/%v", s, r, ok)
	}
}

// ====================== APN ======================
// APN 解析（CGCONTRDP / CGDCONT）与内部 APN 判定的用例在 data.go

func TestLooksLikeIP(t *testing.T) {
	for _, s := range []string{"10.149.102.75", "192.168.1.1"} {
		if !LooksLikeIP(s) {
			t.Errorf("%q 应判为 IP", s)
		}
	}
	for _, s := range []string{
		"", "-", "abc", "1.2.3.999",
		"0.0.0.0",        // 未分配地址
		"36.14.4.0.6.24", // 6 段：模组的掩码/DNS 编码，不是 IP
		"10.149.102.75.1",
	} {
		if LooksLikeIP(s) {
			t.Errorf("%q 不应判为 IP", s)
		}
	}
}

// ====================== 候选指令 ======================

// TestVendorCandidatesAPNTraffic APN 是标准 3GPP 指令（所有厂商一致）；
// 流量计数器是厂商私有指令，只有声明的厂商才会用。
func TestVendorCandidatesAPNTraffic(t *testing.T) {
	q := &Modem{ID: 0, cmdImpl: &quectel.QuectelCommand{}}
	if got := q.vendorCandidates(KindAPN); len(got) != 2 ||
		got[0] != "AT+CGCONTRDP" || got[1] != "AT+CGDCONT?" {
		t.Fatalf("APN 候选 = %v", got)
	}
	if got := q.vendorCandidates(KindTraffic); len(got) != 1 || got[0] != "AT+QGDCNT?" {
		t.Fatalf("移远流量计数器候选 = %v", got)
	}

	// 广和通：只有瞬时速率指令（没有累计计数器），解析走速率模式
	f := &Modem{ID: 0, cmdImpl: &fibocom.FibocomCommand{}}
	if got := f.vendorCandidates(KindTraffic); len(got) != 1 || got[0] != common.CmdGTSTATIS {
		t.Fatalf("广和通流量候选 = %v", got)
	}
	if got := f.vendorCandidates(KindAPN); len(got) != 2 {
		t.Fatalf("APN 候选应为通用两条：%v", got)
	}

	// 芯讯通：实测 QGDCNT 回 ERROR，不声明 → 不发未验证指令
	sc := &Modem{ID: 0, cmdImpl: &simcompkg.SimcomCommand{}}
	if got := sc.vendorCandidates(KindTraffic); len(got) != 0 {
		t.Fatalf("芯讯通不应有流量候选：%v", got)
	}

	// MT5700：鼎桥自家的 ^DSFLOWQRY 排第一（实测可用），QGDCNT? 作为兜底
	mt := &Modem{ID: 0, cmdImpl: &tdtech.MT5700Command{}}
	if got := mt.vendorCandidates(KindTraffic); len(got) != 2 || got[0] != "AT^DSFLOWQRY" {
		t.Fatalf("MT5700 流量计数器候选 = %v", got)
	}
	u := &Modem{ID: 0, cmdImpl: &common.UnknownCommand{}}
	if got := u.vendorCandidates(KindTraffic); len(got) != 1 {
		t.Fatalf("未识别型号流量计数器候选 = %v", got)
	}

	// 美格：自家的 AT^DSFLOWQRY（回 ^DSFLOWRPT: <上行>,<下行> 的**瞬时速率**），QGDCNT? 兜底；
	// ⚠️ 不能直接继承 QuectelCommand 的 AT+QGDCNT?。
	mg := &Modem{ID: 0, cmdImpl: &meig.MeigCommand{}}
	if got := mg.vendorCandidates(KindTraffic); len(got) != 2 || got[0] != "AT^DSFLOWQRY" || got[1] != "AT+QGDCNT?" {
		t.Fatalf("美格流量候选 = %v，want [AT^DSFLOWQRY AT+QGDCNT?]", got)
	}
}

// TestTrafficEnableCommands 计数器使能指令只在"计数式"厂商上发送
func TestTrafficEnableCommands(t *testing.T) {
	for _, impl := range []ModemCommand{&quectel.QuectelCommand{}, &common.UnknownCommand{}} {
		cc, ok := impl.(CommandCandidates)
		if !ok {
			t.Fatalf("%T 应实现 CommandCandidates", impl)
		}
		if got := cc.TrafficEnableCommand(); got != "AT+QGDCNT=1" {
			t.Errorf("%T 使能指令 = %q", impl, got)
		}
	}
	// MT5700：^DSFLOWQRY 是模组自己累计的，不需要"开始计数"（且 AT+QGDCNT=1 实测 ERROR）
	if got := any(&tdtech.MT5700Command{}).(CommandCandidates).TrafficEnableCommand(); got != "" {
		t.Errorf("MT5700 不应有使能指令：%q", got)
	}
	// 广和通是**速率式**（AT+GTSTATIS?）：模组自己报速率，没有"开始计数"这一步
	if got := any(&fibocom.FibocomCommand{}).(CommandCandidates).TrafficEnableCommand(); got != "" {
		t.Errorf("广和通不应有使能指令：%q", got)
	}
	// 美格同样是速率式（AT^DSFLOWQRY → ^DSFLOWRPT），不能继承移远的 AT+QGDCNT=1
	if got := any(&meig.MeigCommand{}).(CommandCandidates).TrafficEnableCommand(); got != "" {
		t.Errorf("美格不应有使能指令（速率式）：%q", got)
	}
	// 芯讯通没声明流量能力，也没有使能指令
	if got := any(&simcompkg.SimcomCommand{}).(CommandCandidates).TrafficEnableCommand(); got != "" {
		t.Errorf("芯讯通不应有使能指令：%q", got)
	}
}

// ---- from: ambr_test.go ----

// AMBR 解析（移远 / 鼎桥 / CGCONTRDP 三条路径）的用例在 data.go；
// 这里只测需要 *Modem 的缓存与备用路径停用。
// 未实现 AMBRQuerier 的厂商：QueryAMBR 返回 nil 且不发指令

// AMBR 解析（移远 / 鼎桥 / CGCONTRDP 三条路径）的用例在 data.go；
// 这里只测需要 *Modem 的缓存与备用路径停用。

// 未实现 AMBRQuerier 的厂商：QueryAMBR 返回 nil 且不发指令
func TestQueryAMBRUnsupportedVendor(t *testing.T) {
	mod := &Modem{ID: 7, cmdImpl: &common.UnknownCommand{}}
	if r := mod.QueryAMBR(); r != nil {
		t.Fatalf("未知厂商不应有签约速率，实际 %+v", r)
	}
}

// 不认 CGCONTRDP 的固件：回 ERROR 后停用备用路径，不再重复发指令
func TestDisableAMBRFallback(t *testing.T) {
	mod := &Modem{ID: 9}
	if mod.ambrFallbackOff {
		t.Fatal("初始应未停用")
	}
	mod.disableAMBRFallback()
	if !mod.ambrFallbackOff {
		t.Fatal("应记住已停用")
	}
}

// ---- from: extra_netinfo_test.go ----

//
// CNWINFO / HFREQINFO / CBC 的**解析**用例在 net.go；
// 这里只留厂商隔离：这两个能力都是“可选接口”，只有声明了的厂商才会被发指令。
//
// 这两个能力都是"可选接口"：只有声明了的厂商才会被发指令。
// 这里把它钉住 —— 移远 / 广和通 / 未识别型号都**不能**实现，
// 一旦谁"顺手"加上了，这条测试会立刻失败（避免给别的固件发未验证指令）。

// ====================== 附加网络信息 / 供电电压 ======================
//
// CNWINFO / HFREQINFO / CBC 的**解析**用例在 net.go；
// 这里只留厂商隔离：这两个能力都是“可选接口”，只有声明了的厂商才会被发指令。

// ====================== 厂商隔离（用户要求：其它模块不受影响） ======================
//
// 这两个能力都是"可选接口"：只有声明了的厂商才会被发指令。
// 这里把它钉住 —— 移远 / 广和通 / 未识别型号都**不能**实现，
// 一旦谁"顺手"加上了，这条测试会立刻失败（避免给别的固件发未验证指令）。
func TestOptionalCapabilitiesVendorIsolation(t *testing.T) {
	simcom := &simcompkg.SimcomCommand{}
	if got := any(simcom).(ExtraNetInfoQuerier).CommandExtraNetInfo(); got != "AT+CNWINFO?" {
		t.Errorf("芯讯通附加网络信息指令 = %q, want AT+CNWINFO?", got)
	}
	if got := any(simcom).(VoltageQuerier).CommandVoltage(); got != "AT+CBC" {
		t.Errorf("芯讯通电压指令 = %q, want AT+CBC", got)
	}
	// MT5700 是**有手册依据**的例外：§13.16 ^HFREQINFO 明确给了频段/带宽。
	// 它仍然不实现 VoltageQuerier（实测 AT+CBC 无有效数据，见 mt5700_test.go）。
	mt := &tdtech.MT5700Command{}
	if got := any(mt).(ExtraNetInfoQuerier).CommandExtraNetInfo(); got != "AT^HFREQINFO?" {
		t.Errorf("MT5700 附加网络信息指令 = %q, want AT^HFREQINFO?", got)
	}
	if _, ok := any(mt).(VoltageQuerier); ok {
		t.Error("MT5700 不该实现 VoltageQuerier（AT+CBC 实测无有效数据）")
	}
	for _, c := range []struct {
		name string
		impl any
	}{
		{"移远", &quectel.QuectelCommand{}},
		{"广和通", &fibocom.FibocomCommand{}},
		{"未知型号", &common.AdaptiveCommands{}},
	} {
		if _, ok := c.impl.(ExtraNetInfoQuerier); ok {
			t.Errorf("%s 不该实现 ExtraNetInfoQuerier（会给它发未验证的 AT+CNWINFO）", c.name)
		}
		if _, ok := c.impl.(VoltageQuerier); ok {
			t.Errorf("%s 不该实现 VoltageQuerier（会给它发未验证的 AT+CBC）", c.name)
		}
	}
}

// 最直接的兑现：其它厂商取到的指令是**空串** → 上层连指令都不会发，
// 接口返回的 JSON 也因 omitempty 而完全不出现这两个字段（与改动前逐字节一致）。
func TestOptionalCapabilitiesNoCommandForOthers(t *testing.T) {
	simcom := &Modem{ID: 0, cmdImpl: &simcompkg.SimcomCommand{}}
	if got := simcom.extraNetInfoCommand(); got != "AT+CNWINFO?" {
		t.Errorf("芯讯通 extraNetInfoCommand = %q, want AT+CNWINFO?", got)
	}
	if got := simcom.voltageCommand(); got != "AT+CBC" {
		t.Errorf("芯讯通 voltageCommand = %q, want AT+CBC", got)
	}
	// MT5700：附加网络信息有手册依据 → 发 ^HFREQINFO?；电压仍不发
	mt := &Modem{ID: 2, cmdImpl: &tdtech.MT5700Command{}}
	if got := mt.extraNetInfoCommand(); got != "AT^HFREQINFO?" {
		t.Errorf("MT5700 extraNetInfoCommand = %q, want AT^HFREQINFO?", got)
	}
	if got := mt.voltageCommand(); got != "" {
		t.Errorf("MT5700 voltageCommand = %q, want 空（不发指令）", got)
	}
	for _, c := range []struct {
		name string
		impl ModemCommand
	}{
		{"移远", &quectel.QuectelCommand{}},
		{"广和通", &fibocom.FibocomCommand{}},
		{"未知厂商", &common.UnknownCommand{}},
	} {
		mod := &Modem{ID: 1, cmdImpl: c.impl}
		if got := mod.extraNetInfoCommand(); got != "" {
			t.Errorf("%s extraNetInfoCommand = %q, want 空（不发指令）", c.name, got)
		}
		if got := mod.voltageCommand(); got != "" {
			t.Errorf("%s voltageCommand = %q, want 空（不发指令）", c.name, got)
		}
	}
}

// 温度指令的变化必须对其它厂商"零影响"：
// 移远自己声明 AT+QTEMP（与原硬编码一致）；没声明的厂商仍退回 AT+QTEMP（同改动前的行为）。
func TestTempQueryCommandUnchangedForOthers(t *testing.T) {
	if got := any(&quectel.QuectelCommand{}).(TempQuerier).CommandTemp(); got != "AT+QTEMP" {
		t.Errorf("移远温度指令 = %q, want AT+QTEMP", got)
	}
	// MT5700 也自己声明（手册 §13.12 ^CHIPTEMP，实测 12 路 0.1℃ 整数）
	if got := any(&tdtech.MT5700Command{}).(TempQuerier).CommandTemp(); got != "AT^CHIPTEMP?" {
		t.Errorf("MT5700 温度指令 = %q, want AT^CHIPTEMP?", got)
	}
	for _, c := range []struct {
		name string
		impl any
	}{
		{"广和通", &fibocom.FibocomCommand{}},
		{"未知厂商", &common.UnknownCommand{}},
	} {
		mod := &Modem{ID: 1, cmdImpl: c.impl.(ModemCommand)}
		if got := mod.tempQueryCommand(); got != "AT+QTEMP" {
			t.Errorf("%s 温度指令 = %q, want AT+QTEMP（与改动前一致）", c.name, got)
		}
	}
}

// ---- from: sms_test.go ----

// ---- from: sms_send_test.go ----
//
// 号码会直接拼进 AT+CMGS="..."，非法字符能改掉 AT 语法；
// 而且以前只判"非空"，`phone: "abc"` 也会真的发出去（模组回 ERROR 却仍返回 ok）。
//
// 号码会直接拼进 AT+CMGS="..."，非法字符能改掉 AT 语法；
// 而且以前只判"非空"，`phone: "abc"` 也会真的发出去（模组回 ERROR 却仍返回 ok）。

func TestValidSMSPhone(t *testing.T) {
	good := []string{
		"13800138000", "10086", "+8613800138000", " 13800138000 ", "+123456",
		// 前后空白（含换行）会先被 TrimSpace 掉，然后才拼进 AT —— 粘贴号码时的常见情况
		"13800138000\n", "\t13900139000",
	}
	for _, p := range good {
		if !validSMSPhone(p) {
			t.Errorf("%q 应通过校验", p)
		}
	}
	bad := []string{
		"", "1234", // 太短
		"abc", "1380013800a", // 非数字
		"138,001", "138\"001\"", // 逗号/引号会破坏 AT 语法
		"138 001",                 // 中间空格
		"++8613800",               // 多个 +
		"86138001380001234567890", // 太长
		"138\n001",                // 中间换行
	}
	for _, p := range bad {
		if validSMSPhone(p) {
			t.Errorf("%q 应被拒绝", p)
		}
	}
}

func TestValidSMSContent(t *testing.T) {
	if !validSMSContent("hello") || !validSMSContent("你好，世界") {
		t.Error("正常内容应通过")
	}
	if validSMSContent("") || validSMSContent("   ") {
		t.Error("空内容应被拒绝")
	}
	if validSMSContent("abc" + string(byte(26)) + "def") {
		t.Error("含 Ctrl+Z（0x1A 发送结束符）应被拒绝")
	}
	if validSMSContent(strings.Repeat("a", 2001)) {
		t.Error("超过 2000 字符应被拒绝")
	}
	if !validSMSContent(strings.Repeat("a", 2000)) {
		t.Error("正好 2000 字符应通过")
	}
}

// ====================== 短信发送：应答判定 ======================
//
// 关键回归：模组回 ERROR 时**不能**再当成成功（实测 AT+CMGS="abc" 就是这种情形）。

func TestClassifySMSSendResult(t *testing.T) {
	cases := []struct {
		name    string
		resp    []string
		wantErr bool
	}{
		{"正常：+CMGS 与 OK", []string{"AT+CMGS=\"10086\"", "+CMGS: 12", "OK"}, false},
		{"只有 OK（部分固件不回 +CMGS）", []string{"OK"}, false},
		{"模组拒绝（ERROR）", []string{"AT+CMGS=\"abc\"", "ERROR"}, true},
		{"CME 错误", []string{"+CME ERROR: 500"}, true},
		{"什么都没回（超时后空应答）", nil, true},
		{"只有回显，没有确认", []string{"AT+CMGS=\"10086\""}, true},
	}
	for _, c := range cases {
		err := classifySMSSendResult(c.resp)
		if c.wantErr && err == nil {
			t.Errorf("%s: 应该报错，实际通过", c.name)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: 不该报错，实际 %v", c.name, err)
		}
	}
	// 错误信息要能让人看懂原因
	err := classifySMSSendResult([]string{"ERROR"})
	if err == nil || !strings.Contains(err.Error(), "拒绝") {
		t.Errorf("ERROR 的错误文案不够清楚: %v", err)
	}
}

// 错误信息要把模组的**原始错误行**带上（`AT+CMEE=2` 后是 `+CMS ERROR: <code>`）——
// 排查时靠它区分“卡/短信中心的问题”和“号码格式的问题”，但不要把正文回显塞进去。
func TestSMSSendErrorCarriesModemErrorLine(t *testing.T) {
	err := classifySMSSendResult([]string{"006E0069" + string(byte(26)), "+CMS ERROR: 500"})
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), "+CMS ERROR: 500") {
		t.Errorf("错误信息应带上模组原始错误行：%v", err)
	}
	if strings.Contains(err.Error(), "006E0069") {
		t.Errorf("不该把正文回显塞进错误信息：%v", err)
	}
	if got := sms.SmsErrorLine([]string{"AT+CMGS=\"10086\"", ">", "ERROR"}); got != "ERROR" {
		t.Errorf("smsErrorLine = %q, want ERROR", got)
	}
	if got := sms.SmsErrorLine([]string{"OK"}); got != "ERROR" {
		t.Errorf("没有错误行时应回退成 ERROR，实际 %q", got)
	}
}

// 错误码要能分类给出建议：实测 FM350 + 电信卡发短信，网络回 `#27 Missing or unknown APN` ——
// 那是**卡/网络侧**不支持短信发送（换普通卡才行），不能让文案把用户引到“号码格式不对”上去。
func TestSMSSendErrorHintByCode(t *testing.T) {
	err := classifySMSSendResult([]string{"+CME ERROR: Missing or unknown APN (#27)"})
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), "#27") {
		t.Errorf("应带上模组原始错误码：%v", err)
	}
	if !strings.Contains(err.Error(), "卡") {
		t.Errorf("#27 的建议应指向卡/网络：%v", err)
	}
	if strings.Contains(err.Error(), "号码格式不对") {
		t.Errorf("#27 不该再用“号码格式不对”这套通用说法：%v", err)
	}

	// 短信中心类
	err = classifySMSSendResult([]string{"+CMS ERROR: 330"})
	if err == nil || !strings.Contains(err.Error(), "CSCA") {
		t.Errorf("330 应提示查短信中心：%v", err)
	}
	// 号码类
	err = classifySMSSendResult([]string{"+CMS ERROR: 21"})
	if err == nil || !strings.Contains(err.Error(), "国家码") {
		t.Errorf("21 应提示号码写法：%v", err)
	}
	// 光一个 ERROR：还是通用建议
	err = classifySMSSendResult([]string{"ERROR"})
	if err == nil || !strings.Contains(err.Error(), "常见原因") {
		t.Errorf("裸 ERROR 应回通用建议：%v", err)
	}

	// SIM 侧拒绝：实测中兴 F50 / 展锐 UMS9620 + 电信流量卡，收短信正常、发短信毫秒级被本地拒。
	// 文案必须指向**卡**（不能又回到“号码格式不对”那套，否则用户会一直改号码）。
	for _, line := range []string{"+CMS ERROR: 313", "+CMS ERROR: SIM failure"} {
		err = classifySMSSendResult([]string{line})
		if err == nil {
			t.Fatalf("%s 应报错", line)
		}
		if !strings.Contains(err.Error(), "SIM") {
			t.Errorf("%s 的建议应指向 SIM/卡侧：%v", line, err)
		}
		if strings.Contains(err.Error(), "常见原因") {
			t.Errorf("%s 不该落到通用建议：%v", line, err)
		}
	}
}

// 文本模式与 PDU 模式（中兴 F50 / 展锐走 PDU）必须共用同一条拒绝文案：
// 只抛原始 `+CMS ERROR: 313` 的写法在界面上看着像“程序不支持的怪错”，用户看不出是卡的问题。
func TestSMSSendRejectErrorKeepsRawLineAndHint(t *testing.T) {
	err := smsSendRejectError([]string{"006E0069" + string(byte(26)), "+CMS ERROR: 313"})
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), "+CMS ERROR: 313") {
		t.Errorf("原始错误行不能丢：%v", err)
	}
	if !strings.Contains(err.Error(), "SIM") {
		t.Errorf("应带上按码分类的建议：%v", err)
	}
	if strings.Contains(err.Error(), "006E0069") {
		t.Errorf("正文回显不该进错误信息：%v", err)
	}
}

// ---- from: sms_charset_test.go ----
//
// ====================== 短信发送：字符集（AT+CSCS）处理 ======================
//
// 关键回归（实测 FM350-GL）：出厂 `AT+CSCS?` = "UCS2" 时，**正文**必须写成 UCS2 十六进制；
// 号码则看固件 —— FM350 照样接受 ASCII 号码（本用例走的这条路），
// 而芯讯通 SIM8200 会拒收 ASCII 号码、要换 UCS2 十六进制重试（见
// TestSMSSendUCS2NumberRetryWhenModemRejectsASCII）。

// fakeATServe 起一个极简"假模组"：从 writeChan 收指令、按 script 产生应答写 readChan，
// 并把收到的指令按顺序放进返回的通道（测试用它断言"到底发了哪些指令"）。
func fakeATServe(mod *Modem, script func(cmd string) []string) chan string {
	sent := make(chan string, 64)
	go func() {
		for cmd := range mod.writeChan {
			sent <- cmd
			for _, line := range script(cmd) {
				mod.readChan <- line
			}
		}
	}()
	return sent
}

// drainSent 取走假模组收到的全部指令（发送流程返回时它们都已入队）
func drainSent(sent chan string) []string {
	var out []string
	for {
		select {
		case c := <-sent:
			out = append(out, c)
		default:
			return out
		}
	}
}

func hasCmd(cmds []string, want string) bool {
	for _, c := range cmds {
		if c == want {
			return true
		}
	}
	return false
}

// withATTimeout 临时把 AT 超时设成一个正常值。
// ⚠️ 测试进程没有加载配置文件，`Cfg.Serial.CmdTimeout` 默认是 0 —— 那意味着
// `time.NewTimer(0)` 一创建就到期，每条指令都“立即超时”。
func withATTimeout(t *testing.T) {
	t.Helper()
	old := config.Cfg.Serial.CmdTimeout
	config.Cfg.Serial.CmdTimeout = 2 * time.Second
	t.Cleanup(func() { config.Cfg.Serial.CmdTimeout = old })
}

// newFakeSMSModem 造一台只连着"假模组"的 FM350，charset 是假模组 ACSII 上报的字符集
func newFakeSMSModem(charset string) (*Modem, chan string) {
	script := func(cmd string) []string {
		switch {
		case strings.HasPrefix(cmd, "AT+CSCS?"):
			return []string{cmd, `+CSCS: "` + charset + `"`, "OK"}
		case strings.HasPrefix(cmd, "AT+CSMP?"):
			return []string{cmd, "+CSMP: 0, 71, 0, 0", "OK"}
		case strings.HasPrefix(cmd, "AT+CMGS="):
			return []string{cmd, ">"} // 号码被接受 → 等正文（注意：这一步**没有** OK）
		case strings.ContainsRune(cmd, 0x1A): // 正文 + Ctrl+Z
			return []string{"+CMGS: 7", "OK"}
		}
		return []string{cmd, "OK"}
	}
	mod := &Modem{
		ID:        0,
		Model:     "ATI|Manufacturer: Fibocom Wireless Inc.|Model: FM350-GL|OK",
		cmdImpl:   &fibocom.FibocomCommand{},
		writeChan: make(chan string, 64),
		readChan:  make(chan string, 64),
	}
	return mod, fakeATServe(mod, script)
}

// FM350 实测场景：CSCS=UCS2 → 正文要编成十六进制（号码仍原样），DCS 也要跟着变成 UCS2
func TestSMSSendFM350UCS2Charset(t *testing.T) {
	withATTimeout(t)
	mod, sent := newFakeSMSModem("UCS2")
	defer close(mod.writeChan)

	if err := mod.doSendSMS("13520309333", "nihao"); err != nil {
		t.Fatalf("发送应成功，实际 %v", err)
	}
	cmds := drainSent(sent)
	// 号码保持 ASCII（实测 FM350 在 UCS2 下也接受这种写法；十六进制号码反倒拿不到 `>`）
	if !hasCmd(cmds, `AT+CMGS="13520309333"`) {
		t.Errorf("号码应原样（ASCII）发送，实发：%q", cmds)
	}
	if !hasCmd(cmds, "006E006900680061006F"+string(byte(26))) {
		t.Errorf("正文必须按 UCS2 十六进制发送，实发：%q", cmds)
	}
	if !hasCmd(cmds, "AT+CSMP=0,71,0,8") {
		t.Errorf("UCS2 正文要配 DCS=8（否则对方收到乱码），实发：%q", cmds)
	}
	// 发完必须把 CSMP 写回原值：不去改模组的长期状态
	if n := len(cmds); n == 0 || cmds[n-1] != "AT+CSMP=0,71,0,0" {
		t.Errorf("最后应恢复 CSMP 原值，实发：%q", cmds)
	}
}

// 非 UCS2 字符集（移远这类默认 "GSM"）+ 纯 ASCII 内容：原样发，一个字节都不动模组状态
func TestSMSSendGSMASCIIKeepsModemState(t *testing.T) {
	withATTimeout(t)
	mod, sent := newFakeSMSModem("GSM")
	defer close(mod.writeChan)

	if err := mod.doSendSMS("10086", "hello"); err != nil {
		t.Fatalf("发送应成功，实际 %v", err)
	}
	cmds := drainSent(sent)
	if !hasCmd(cmds, `AT+CMGS="10086"`) || !hasCmd(cmds, "hello"+string(byte(26))) {
		t.Errorf("纯 ASCII 内容应原样发送，实发：%q", cmds)
	}
	for _, c := range cmds {
		if strings.HasPrefix(c, "AT+CSCS=") && c != "AT+CSCS?" {
			t.Errorf("纯 ASCII 内容不该切字符集，却发了 %q", c)
		}
		if strings.HasPrefix(c, "AT+CSMP=") {
			t.Errorf("纯 ASCII 内容不该改 CSMP，却发了 %q", c)
		}
	}
}

// 非 UCS2 字符集 + 中文内容：临时切 UCS2（并改 DCS），发完恢复原字符集
func TestSMSSendGSMChineseSwitchesUCS2AndRestores(t *testing.T) {
	withATTimeout(t)
	mod, sent := newFakeSMSModem("GSM")
	defer close(mod.writeChan)

	if err := mod.doSendSMS("10086", "你好"); err != nil {
		t.Fatalf("发送应成功，实际 %v", err)
	}
	cmds := drainSent(sent)
	if !hasCmd(cmds, `AT+CSCS="UCS2"`) {
		t.Errorf("发中文前应切到 UCS2，实发：%q", cmds)
	}
	if !hasCmd(cmds, `AT+CMGS="10086"`) {
		t.Errorf("号码应原样发送，实发：%q", cmds)
	}
	if !hasCmd(cmds, "4F60597D"+string(byte(26))) {
		t.Errorf("正文应按 UCS2 编码（UTF-16BE），实发：%q", cmds)
	}
	// undo 逆序恢复：CSMP 在前、CSCS 在后
	if n := len(cmds); n < 2 || cmds[n-1] != `AT+CSCS="GSM"` || cmds[n-2] != "AT+CSMP=0,71,0,0" {
		t.Errorf("发完应把 CSCS/CSMP 恢复原值，实发：%q", cmds)
	}
}

// 实测（芯讯通 SIM8200EA-M2）：`AT+CSCS="UCS2"` 时模组**拒收 ASCII 号码** ——
// `AT+CMGS="13800138000"` 秒回 `ERROR`（11 位是奇数长度的十六进制串，根本解不出来），
// 把号码写成 UCS2 十六进制（`00310033…0030`）才回 `>`。
// 回归点：第一次被明确拒绝后要**换 UCS2 号码重试**，且正文/号码/DCS 都对。
func TestSMSSendUCS2NumberRetryWhenModemRejectsASCII(t *testing.T) {
	withATTimeout(t)
	script := func(cmd string) []string {
		switch {
		case strings.HasPrefix(cmd, "AT+CSCS?"):
			return []string{cmd, `+CSCS: "UCS2"`, "OK"}
		case strings.HasPrefix(cmd, "AT+CSMP?"):
			return []string{cmd, "+CSMP: 0, 71, 0, 0", "OK"}
		case cmd == `AT+CMGS="13800138000"`:
			return []string{cmd, "ERROR"} // 芯讯通实测：UCS2 下 ASCII 号码被拒
		case strings.HasPrefix(cmd, "AT+CMGS="):
			return []string{cmd, ">"} // UCS2 号码被接受 → 等正文
		case strings.ContainsRune(cmd, 0x1A):
			return []string{"+CMGS: 7", "OK"}
		}
		return []string{cmd, "OK"}
	}
	mod := &Modem{ID: 0, Model: "SIM8200EA-M2", cmdImpl: &simcompkg.SimcomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	sent := fakeATServe(mod, script)
	defer close(mod.writeChan)

	if err := mod.doSendSMS("13800138000", "你好"); err != nil {
		t.Fatalf("被拒后应用 UCS2 号码重试并成功，实际 %v", err)
	}
	cmds := drainSent(sent)
	if !hasCmd(cmds, `AT+CMGS="13800138000"`) {
		t.Errorf("应先用 ASCII 号码试一次，实发：%q", cmds)
	}
	if !hasCmd(cmds, `AT+CMGS="`+encodeUCS2Hex("13800138000")+`"`) {
		t.Errorf("被拒后应换成 UCS2 十六进制号码重试，实发：%q", cmds)
	}
	if !hasCmd(cmds, "4F60597D"+string(byte(26))) {
		t.Errorf("正文应按 UCS2 编码（UTF-16BE），实发：%q", cmds)
	}
	if !hasCmd(cmds, "AT+CSMP=0,71,0,8") {
		t.Errorf("UCS2 正文要配 DCS=8，实发：%q", cmds)
	}
}

// 非 UCS2 字符集（只有一种号码写法）：被拒就如实报错，**不白发**第二条 CMGS
// （号码写成十六进制只对 UCS2 字符集有意义）
func TestSMSSendNoNumberRetryForASCIIContent(t *testing.T) {
	withATTimeout(t)
	script := func(cmd string) []string {
		switch {
		case strings.HasPrefix(cmd, "AT+CSCS?"):
			return []string{cmd, `+CSCS: "IRA"`, "OK"}
		case strings.HasPrefix(cmd, "AT+CMGS="):
			return []string{cmd, "ERROR"}
		}
		return []string{cmd, "OK"}
	}
	mod := &Modem{ID: 0, Model: "SIM8200EA-M2", cmdImpl: &simcompkg.SimcomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	sent := fakeATServe(mod, script)
	defer close(mod.writeChan)

	err := mod.doSendSMS("13800138000", "hello")
	if err == nil || !strings.Contains(err.Error(), "模组拒绝了 AT+CMGS") {
		t.Fatalf("应如实报被拒，得到 %v", err)
	}
	n := 0
	for _, c := range drainSent(sent) {
		if strings.HasPrefix(c, "AT+CMGS=") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("非 UCS2 下没有第二种号码写法，不该重发 CMGS，实际发了 %d 条", n)
	}
}

// 超时（不是明确拒绝）时**绝不能**重发 CMGS：模组可能已经在等正文，
// 再发一条会被当成正文内容吞掉（AT 通道就歪了）。
// 把 AT 超时调到比"宽限时间"还短，逼出 waitSendPromptLocked 的超时分支。
func TestSMSSendNoNumberRetryOnPromptTimeout(t *testing.T) {
	withATTimeout(t)
	defer func(old time.Duration) { config.Cfg.Serial.CmdTimeout = old }(config.Cfg.Serial.CmdTimeout)
	config.Cfg.Serial.CmdTimeout = 100 * time.Millisecond
	script := func(cmd string) []string {
		switch {
		case strings.HasPrefix(cmd, "AT+CSCS?"):
			return []string{cmd, `+CSCS: "UCS2"`, "OK"}
		case strings.HasPrefix(cmd, "AT+CMGS="):
			return []string{cmd} // 既不回 `>` 也不回 ERROR → 走超时
		}
		return []string{cmd, "OK"}
	}
	mod := &Modem{ID: 0, Model: "SIM8200EA-M2", cmdImpl: &simcompkg.SimcomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	sent := fakeATServe(mod, script)
	defer close(mod.writeChan)

	if err := mod.doSendSMS("13800138000", "你好"); err == nil {
		t.Fatal("没有任何提示符/错误应答时应报失败")
	}
	n := 0
	for _, c := range drainSent(sent) {
		if strings.HasPrefix(c, "AT+CMGS=") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("超时时不该重发 CMGS（可能已被当成正文），实际发了 %d 条", n)
	}
}

func TestParseSMSCharsetAndCSMP(t *testing.T) {
	if got := parseSMSCharset([]string{`AT+CSCS?`, `+CSCS: "UCS2"`, "OK"}); got != "UCS2" {
		t.Errorf("CSCS 解析 = %q, want UCS2", got)
	}
	if got := parseSMSCharset([]string{`+CSCS: "gsm"`}); got != "GSM" {
		t.Errorf("CSCS 应统一成大写，实际 %q", got)
	}
	if got := parseSMSCharset([]string{"OK"}); got != "" {
		t.Errorf("没读到 CSCS 行时应返回空串，实际 %q", got)
	}

	params, dcs, ok := parseSMSCSMP([]string{"AT+CSMP?", "+CSMP: 0, 71, 0, 0", "OK"})
	if !ok || params != "0,71,0,0" || dcs != 0 {
		t.Fatalf("CSMP 解析 = %q/%d/%v", params, dcs, ok)
	}
	if got := smsCSMPWithDCS(params, 8); got != "0,71,0,8" {
		t.Errorf("换 DCS 后 = %q, want 0,71,0,8", got)
	}
	if _, _, ok := parseSMSCSMP([]string{"OK"}); ok {
		t.Error("没有 CSMP 行时不该报 ok")
	}
	if _, _, ok := parseSMSCSMP([]string{"+CSMP: 0,0"}); ok {
		t.Error("参数不足 4 个时不该报 ok")
	}
}

func TestEncodeUCS2HexAndASCIIOnly(t *testing.T) {
	cases := []struct{ in, want string }{
		{"13520309333", "00310033003500320030003300300039003300330033"},
		{"10086", "00310030003000380036"},
		{"+86", "002B00380036"}, // 国际前缀的 + 编成 002B
		{"nihao", "006E006900680061006F"},
		{"你好", "4F60597D"},
		{"", ""},
	}
	for _, c := range cases {
		if got := encodeUCS2Hex(c.in); got != c.want {
			t.Errorf("encodeUCS2Hex(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if !smsASCIIOnly("hello 123 +") {
		t.Error("纯 ASCII 应判 true")
	}
	if smsASCIIOnly("你好") || smsASCIIOnly("a\nb") {
		t.Error("非 ASCII 与不可打印控制符都应判 false")
	}
}

// 关键回归：正文必须在模组回 `>` **之后**才发出去。
//
// 实测 FM350：以前发完 AT+CMGS 固定 sleep(200ms) 就发正文，提示符还没来 ——
// 正文被模组丢掉，AT+CMGS 一路超时，之后每条 AT 指令都被当成正文吞掉（通道“卡死”）。
func TestSMSSendWaitsForPromptBeforeBody(t *testing.T) {
	withATTimeout(t)
	sent := make(chan string, 64)
	mod := &Modem{
		ID:        0,
		Model:     "ATI|Manufacturer: Fibocom Wireless Inc.|Model: FM350-GL|OK",
		cmdImpl:   &fibocom.FibocomCommand{},
		writeChan: make(chan string, 64),
		readChan:  make(chan string, 64),
	}
	go func() {
		for cmd := range mod.writeChan {
			switch {
			case strings.HasPrefix(cmd, "AT+CSCS?"):
				sent <- cmd
				mod.readChan <- cmd
				mod.readChan <- `+CSCS: "GSM"` // 走“原样发送”分支，排除字符集干扰
				mod.readChan <- "OK"
			case strings.HasPrefix(cmd, "AT+CMGS="):
				sent <- cmd
				mod.readChan <- cmd
				// 提示符比老的固定 200ms 慢得多 —— 专门用来抓“抢跑”
				time.Sleep(600 * time.Millisecond)
				sent <- "<<prompt>>"
				mod.readChan <- ">"
			case strings.ContainsRune(cmd, 0x1A):
				sent <- "<<body>>"
				mod.readChan <- "+CMGS: 9"
				mod.readChan <- "OK"
			default:
				sent <- cmd
				mod.readChan <- cmd
				mod.readChan <- "OK"
			}
		}
	}()
	defer close(mod.writeChan)

	if err := mod.doSendSMS("10086", "hello"); err != nil {
		t.Fatalf("发送应成功，实际 %v", err)
	}
	seq := drainSent(sent)
	pi, bi := -1, -1
	for i, c := range seq {
		if c == "<<prompt>>" && pi < 0 {
			pi = i
		}
		if c == "<<body>>" && bi < 0 {
			bi = i
		}
	}
	if pi < 0 || bi < 0 {
		t.Fatalf("没看到提示符/正文事件：%q", seq)
	}
	if bi < pi {
		t.Errorf("正文在模组回 `>` 之前就发出去了（会被丢掉）：%q", seq)
	}
}

// 关键回归（0.1.32-1）：**不回 `>` 的固件**（实测鼎桥 MT5700M-CN）也必须能发出去。
//
// 停掉 modemd 直接读串口得到的应答是：`AT+CMGS="10086" 1a 0d 0a` —— 0x1A 结尾、
// 没有 `>`，之后再无输出（其它指令都没回显，所以这行是模组自己发的）。
// 改成“死等 `>`”之后这台机器上发短信永久失败（用户：“之前是正常的”）。
func TestSMSSendPromptlessModem(t *testing.T) {
	withATTimeout(t)
	sent := make(chan string, 64)
	mod := &Modem{
		ID:        0,
		Model:     "ATI|Manufacturer: TD Tech Ltd.|Model: MT5700M-CN|OK",
		cmdImpl:   &tdtech.MT5700Command{},
		writeChan: make(chan string, 64),
		readChan:  make(chan string, 64),
	}
	go func() {
		for cmd := range mod.writeChan {
			switch {
			case strings.HasPrefix(cmd, "AT+CSCS?"):
				sent <- cmd
				mod.readChan <- cmd
				mod.readChan <- `+CSCS: "IRA"`
				mod.readChan <- "OK"
			case strings.HasPrefix(cmd, "AT+CMGS="):
				sent <- cmd
				// 实测：不回 `>`，只回一行以 0x1A 结尾的内容
				mod.readChan <- cmd + string(rune(0x1A))
			case strings.ContainsRune(cmd, 0x1A):
				sent <- "<<body>>"
				mod.readChan <- "+CMGS: 3"
				mod.readChan <- "OK"
			default:
				sent <- cmd
				mod.readChan <- cmd
				mod.readChan <- "OK"
			}
		}
	}()
	defer close(mod.writeChan)

	start := time.Now()
	if err := mod.doSendSMS("10086", "hello"); err != nil {
		t.Fatalf("不回 `>` 的模组也应能发送，实际 %v", err)
	}
	if d := time.Since(start); d > smsPromptGrace+time.Second {
		t.Errorf("不该等满 CmdTimeout，耗时 %v", d)
	}
	found := false
	for _, c := range drainSent(sent) {
		if c == "<<body>>" {
			found = true
		}
	}
	if !found {
		t.Fatal("正文没发出去")
	}
}

// 同样是不回 `>` 的固件，而且**也不回 +CMGS 确认**（真实 MT5700 的行为）：
//
// 关键回归（0.1.61-1）：这种固件必须**如实报失败**。
//
// 以前这里是「按已提交处理、返回成功」，理由是“本地无法核实、别把发出去的报成失败”；
// 但真机复核发现 MT5700M-CN 的正文输入态**根本不完成**（本地存储测试 `AT+CMGW` 也一样，
// 存储里不新增条目），网络侧也收不到短信 —— 返回成功只会让界面显示“发送成功”、
// 用户白等。现在改成把模组原话带上、报失败（其它厂商有 `>` 提示符，走不到这条分支）。
func TestSMSSendPromptlessWithoutConfirm(t *testing.T) {
	withATTimeout(t)
	oldSub, oldWait := smsSubmitTimeout, smsPromptlessConfirmWait
	smsSubmitTimeout, smsPromptlessConfirmWait = 400*time.Millisecond, 400*time.Millisecond
	defer func() { smsSubmitTimeout, smsPromptlessConfirmWait = oldSub, oldWait }()
	mod := &Modem{
		ID:        0,
		Model:     "ATI|Manufacturer: TD Tech Ltd.|Model: MT5700M-CN|OK",
		cmdImpl:   &tdtech.MT5700Command{},
		writeChan: make(chan string, 64),
		readChan:  make(chan string, 64),
	}
	go func() {
		for cmd := range mod.writeChan {
			switch {
			case strings.HasPrefix(cmd, "AT+CSCS?"):
				mod.readChan <- cmd
				mod.readChan <- `+CSCS: "IRA"`
				mod.readChan <- "OK"
			case strings.HasPrefix(cmd, "AT+CMGS="):
				mod.readChan <- cmd + string(rune(0x1A)) // 不回 `>`
			case strings.ContainsRune(cmd, 0x1A):
				// 正文收到，但模组什么也不回
			default:
				mod.readChan <- cmd
				mod.readChan <- "OK"
			}
		}
	}()
	defer close(mod.writeChan)

	err := mod.doSendSMS("10086", "hello")
	if err == nil {
		t.Fatal("模组没回确认时必须报失败，不能谎报成功")
	}
	if !strings.Contains(err.Error(), "没有任何回应") {
		t.Errorf("错误信息应说明模组没有任何回应，实际 %v", err)
	}
}

// 关键回归（0.1.36-1）：出厂 `AT+CSMP: ,,0,0`（<fo>/<vp> 为空）时，鼎桥 MT5700 会把
// `AT+CMGS` **静默吞掉**（只回一行 0x1A，之后既不报错也不回 +CMGS）。
// 手册 §9.7 的例子是 `AT+CSMP=17,255,0,0` ⇒ 发送前必须先补上合法 CSMP（抓串口实测过）。
// ---- from: sms_service_test.go ----
//
// ====================== 短信错误详情（AT+CMEE） ======================

// 关键回归（2026-10-01，FM350-GL）：模组的 AT+CMEE 是 0（而且**不持久**，会被重置），
// 此时网络拒绝短信只会回一个**裸 `ERROR`**，界面只能给"号码格式 / SIM / 短信中心"
// 这类通用猜测。发送前必须确保 CMEE=2，错误才会带上真正的错误码
// （同一次发送实测是 `+CME ERROR: Missing or unknown APN (#27)`）。
func TestSMSSendEnablesCMEEBeforeSend(t *testing.T) {
	withATTimeout(t)
	sent := make(chan string, 64)
	mod := &Modem{
		ID:        0,
		Model:     "ATI|Manufacturer: Fibocom Wireless Inc.|Model: FM350-GL|OK",
		cmdImpl:   &fibocom.FibocomCommand{},
		writeChan: make(chan string, 64),
		readChan:  make(chan string, 64),
	}
	go func() {
		for cmd := range mod.writeChan {
			switch {
			case strings.HasPrefix(cmd, "AT+CSCS?"):
				sent <- cmd
				mod.readChan <- cmd
				mod.readChan <- `+CSCS: "UCS2"`
				mod.readChan <- "OK"
			case strings.HasPrefix(cmd, "AT+CMEE?"):
				sent <- cmd
				mod.readChan <- cmd
				mod.readChan <- "+CMEE: 0" // 出厂 / 被重置的默认值
				mod.readChan <- "OK"
			case strings.HasPrefix(cmd, "AT+CMGS="):
				sent <- cmd
				mod.readChan <- cmd
				mod.readChan <- ">"
			case strings.ContainsRune(cmd, 0x1A):
				sent <- "<<body>>"
				mod.readChan <- "+CMGS: 2"
				mod.readChan <- "OK"
			default:
				sent <- cmd
				mod.readChan <- cmd
				mod.readChan <- "OK"
			}
		}
	}()
	defer close(mod.writeChan)

	if err := mod.doSendSMS("10000", "1"); err != nil {
		t.Fatalf("发送应成功，实际 %v", err)
	}
	seq := drainSent(sent)
	on, cmgs := -1, -1
	for i, c := range seq {
		if c == "AT+CMEE=2" && on < 0 {
			on = i
		}
		if strings.HasPrefix(c, "AT+CMGS=") && cmgs < 0 {
			cmgs = i
		}
	}
	if on < 0 {
		t.Fatalf("发送前应把 CMEE 设成 2：%q", seq)
	}
	if cmgs < 0 || on > cmgs {
		t.Errorf("CMEE=2 必须在 AT+CMGS 之前设好：%q", seq)
	}
	// 发完写回原值：不去改模组的长期状态
	if n := len(seq); n == 0 || seq[n-1] != "AT+CMEE=0" {
		t.Errorf("最后应恢复 CMEE 原值，实发：%q", seq)
	}
}

// 已经是 CMEE=2 时不该多发任何 CMEE 指令（也别来回改写模组状态）。
func TestSMSSendKeepsCMEEWhenAlreadyDetailed(t *testing.T) {
	withATTimeout(t)
	sent := make(chan string, 64)
	mod := &Modem{
		ID:        0,
		Model:     "ATI|Manufacturer: Fibocom Wireless Inc.|Model: FM350-GL|OK",
		cmdImpl:   &fibocom.FibocomCommand{},
		writeChan: make(chan string, 64),
		readChan:  make(chan string, 64),
	}
	go func() {
		for cmd := range mod.writeChan {
			switch {
			case strings.HasPrefix(cmd, "AT+CSCS?"):
				sent <- cmd
				mod.readChan <- cmd
				mod.readChan <- `+CSCS: "UCS2"`
				mod.readChan <- "OK"
			case strings.HasPrefix(cmd, "AT+CMEE?"):
				sent <- cmd
				mod.readChan <- cmd
				mod.readChan <- "+CMEE: 2"
				mod.readChan <- "OK"
			case strings.HasPrefix(cmd, "AT+CMGS="):
				sent <- cmd
				mod.readChan <- cmd
				mod.readChan <- ">"
			case strings.ContainsRune(cmd, 0x1A):
				sent <- "<<body>>"
				mod.readChan <- "+CMGS: 2"
				mod.readChan <- "OK"
			default:
				sent <- cmd
				mod.readChan <- cmd
				mod.readChan <- "OK"
			}
		}
	}()
	defer close(mod.writeChan)

	if err := mod.doSendSMS("10000", "1"); err != nil {
		t.Fatalf("发送应成功，实际 %v", err)
	}
	for _, c := range drainSent(sent) {
		if strings.HasPrefix(c, "AT+CMEE=") {
			t.Errorf("已经是 CMEE=2 时不该再写 CMEE，却发了 %q", c)
		}
	}
}

func TestParseCMEE(t *testing.T) {
	cases := []struct {
		resp []string
		want string
	}{
		{[]string{"AT+CMEE?", "+CMEE: 0", "OK"}, "0"},
		{[]string{"+CMEE: 2", "OK"}, "2"},
		{[]string{"OK"}, ""},
		{[]string{"AT+CMEE?"}, ""},
	}
	for _, c := range cases {
		if got := parseCMEE(c.resp); got != c.want {
			t.Errorf("parseCMEE(%q) = %q, want %q", c.resp, got, c.want)
		}
	}
}

func TestSMSSendFixesEmptyCSMP(t *testing.T) {
	withATTimeout(t)
	sent := make(chan string, 64)
	mod := &Modem{
		ID:        0,
		Model:     "ATI|Manufacturer: TD Tech Ltd.|Model: MT5700M-CN|OK",
		cmdImpl:   &tdtech.MT5700Command{},
		writeChan: make(chan string, 64),
		readChan:  make(chan string, 64),
	}
	go func() {
		for cmd := range mod.writeChan {
			switch {
			case strings.HasPrefix(cmd, "AT+CSCS?"):
				sent <- cmd
				mod.readChan <- cmd
				mod.readChan <- `+CSCS: "IRA"`
				mod.readChan <- "OK"
			case cmd == "AT+CSMP?":
				sent <- cmd
				mod.readChan <- cmd
				mod.readChan <- "+CSMP: ,,0,0" // 出厂的空值（本次要修的）
				mod.readChan <- "OK"
			case strings.HasPrefix(cmd, "AT+CMGS="):
				sent <- cmd
				mod.readChan <- cmd + string(rune(0x1A))
				mod.readChan <- "OK"
			case strings.ContainsRune(cmd, 0x1A):
				sent <- "<<body>>"
				mod.readChan <- "+CMGS: 5"
				mod.readChan <- "OK"
			default:
				sent <- cmd
				mod.readChan <- cmd
				mod.readChan <- "OK"
			}
		}
	}()
	defer close(mod.writeChan)

	if err := mod.doSendSMS("10086", "hello"); err != nil {
		t.Fatalf("发送应成功，实际 %v", err)
	}
	seq := drainSent(sent)
	csmp, cmgs := -1, -1
	for i, c := range seq {
		if strings.HasPrefix(c, "AT+CSMP=17,167,0,0") && csmp < 0 {
			csmp = i
		}
		if strings.HasPrefix(c, "AT+CMGS=") && cmgs < 0 {
			cmgs = i
		}
	}
	if csmp < 0 {
		t.Fatalf("没有补上合法的 CSMP：%q", seq)
	}
	if cmgs < 0 || csmp > cmgs {
		t.Errorf("CSMP 必须在 AT+CMGS 之前设置：%q", seq)
	}
}

// ====================== 读循环的行切分（含 `>` 提示符） ======================
//
// ⚠️ 实测坑：FM350 的 `>` 提示符**不带换行**，按 `\n` 切永远切不出来 ——
// “等提示符再发正文”的实现会和模组互相干等，最后 AT 通道被正文状态吞掉。
func TestSplitATLines(t *testing.T) {
	// ① 常规：按 \n 切、去空白、丢空行
	lines, rest := splitATLines([]byte("AT+CSQ\r\n+CSQ: 20,99\r\n\r\nOK\r\n"))
	want := []string{"AT+CSQ", "+CSQ: 20,99", "OK"}
	if len(lines) != len(want) {
		t.Fatalf("常规切分 = %q, want %q", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("行 %d = %q, want %q", i, lines[i], want[i])
		}
	}
	if len(rest) != 0 {
		t.Errorf("不该有剩余缓冲：%q", rest)
	}

	// ② `>` 不带换行：必须立刻单独上报，否则发送流程等不到它
	lines, rest = splitATLines([]byte("AT+CMGS=\"10086\"\r\n>"))
	if len(lines) != 2 || lines[1] != ">" {
		t.Errorf("提示符应单独成行：%q", lines)
	}
	if len(rest) != 0 {
		t.Errorf("提示符上报后不该留缓冲：%q", rest)
	}

	// ③ `>` 后面已经有数据（`> 正文回显`）：不能提前上报，等换行
	lines, rest = splitATLines([]byte("> 006E0069"))
	if len(lines) != 0 {
		t.Errorf("半行数据不该上报：%q", lines)
	}
	if string(rest) != "> 006E0069" {
		t.Errorf("半行数据要留在缓冲里：%q", rest)
	}

	// ④ 一行没切完的普通数据也留着（URC 分片到达）
	lines, rest = splitATLines([]byte("+CMTI: \"SM\""))
	if len(lines) != 0 || string(rest) != "+CMTI: \"SM\"" {
		t.Errorf("未换行的数据不该上报：%q / %q", lines, rest)
	}
}
