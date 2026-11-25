package kernel

// 用户实测报的三个界面问题，各一条回归钉子（2026-10-05）：
//
//	① 信号卡片里**运营商不显示**（QNWINFO 在这台机上不支持 ⇒ 以前只能靠 AT；
//	   现在从 URC 的 `+SPNWNAME` 拿，名字是 UCS2 hex）；
//	② **信号都不动**（URC 供的 `+CESQ` 被“只补空”规则挡住，第一条之后就永不刷新）；
//	③ **网络速度一隐一显地闪**（网络推送里 traffic=null，流量推送里又有值）。

import (
	"testing"
	"time"
)

// 真机样本（展锐 URC，实测抓到的原文）。
const realSPNWNAME = `+SPNWNAME: 460,11,"4E2D56FD75354FE1","4E2D56FD75354FE1",1,1`

// ① 运营商：从 `+SPNWNAME` 解析出 PLMN 与中文名（UCS2 hex）。
func TestUrcOperatorFromSPNWNAME(t *testing.T) {
	op, ok := urcOpFromLine(realSPNWNAME)
	if !ok {
		t.Fatal("真机样本应能解析出运营商")
	}
	if op.mcc != "460" || op.mnc != "11" {
		t.Errorf("PLMN 应为 460/11，实际 %q/%q", op.mcc, op.mnc)
	}
	// 4E2D=中 56FD=国 7535=电 4FE1=信
	if op.full != "中国电信" {
		t.Errorf("全称应为“中国电信”（UCS2 hex 解码），实际 %q", op.full)
	}
	if op.shrt != "中国电信" {
		t.Errorf("简称应为“中国电信”，实际 %q", op.shrt)
	}
	if got := opNamePreferred(op); got == "" {
		t.Error("应给出一个可显示的运营商名")
	}

	// 不是这行 / 脏数据一律不认（不能把别的东西当运营商）
	for _, bad := range []string{
		"+SPNWNAME:",
		"+SPNWNAME: 46,11,\"4E2D\"",
		"+SPNWNAME: abc,11,\"4E2D56FD\"",
		"+CEREG: 1,\"CFA102\"",
		"",
	} {
		if _, ok := urcOpFromLine(bad); ok {
			t.Errorf("%q 不该被解析成运营商", bad)
		}
	}

	// 有些固件直接给明文（不 hex）：也要能用
	op, ok = urcOpFromLine(`+SPNWNAME: 460,11,"China Telecom","CT",1,1`)
	if !ok || op.full != "China Telecom" {
		t.Errorf("明文形式的 SPNWNAME 也该认，实际 %+v ok=%v", op, ok)
	}
}

// UCS2 hex 解码的边界：奇数长度 / 非 hex / 全是控制字符都要干净失败。
func TestDecodeUCS2Hex(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"4E2D56FD75354FE1", "中国电信", true},
		{"0041", "A", true},
		{"4E2D56F", "", false},  // 奇数长度
		{"4E2D56G1", "", false}, // 非 hex
		{"", "", false},         // 空
		{"0000", "", false},     // 控制字符
		{"4E2D56FD", "中国", true},
	} {
		got, ok := decodeUCS2Hex(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("decodeUCS2Hex(%q) = %q,%v，期望 %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// 运营商要经 networkStatusFromURC 落到接口返回的字段上（前端读 operator/mcc/mnc）。
func TestNetworkStatusCarriesOperatorFromURC(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	mod.feedURCLine(`+CGREG: 1,"FFFE"`)
	mod.feedURCLine(realSPNWNAME)

	st := mod.networkStatusFromURC()
	if st.Operator != "中国电信" {
		t.Errorf("接口应带上运营商，实际 %q", st.Operator)
	}
	if st.MCC != "460" || st.MNC != "11" || st.PLMN != "46011" {
		t.Errorf("PLMN 应一并带上，实际 mcc=%q mnc=%q plmn=%q", st.MCC, st.MNC, st.PLMN)
	}
}

// ② 信号要**跟着 URC 动**：同一条 CESQ 行反复来也要能刷新（覆盖式），
// 而 AT 应答路径仍保持“只补空”（不能被 CESQ 反过来覆盖更精确的来源）。
func TestURCCESQRefreshesSignal(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")

	// URC 来的第一条：填上
	mod.feedURCLine("+CESQ: 99,99,255,255,255,255,60,50,49")
	first := mod.signalSnapshot()
	if first.NRRSRP == 0 && first.NRRSRQ == 0 && first.NRSINR == 0 {
		t.Fatalf("URC 的 CESQ 应能填出 NR 信号，实际 %+v", first)
	}

	// URC 来一条**不同**的：必须刷新（这就是“信号不动”的修复点）
	mod.feedURCLine("+CESQ: 99,99,255,255,255,255,57,52,48")
	second := mod.signalSnapshot()
	if second == first {
		t.Fatalf("第二条 URC CESQ 应刷新信号，却和第一条一模一样：%+v", second)
	}

	// AT 应答路径：只补空（先清掉 NR 值再验证"有值就不被覆盖"）
	atMod := newURCTestModem("/dev/stty_nr2")
	atMod.parseLine("+CESQ: 99,99,255,255,255,255,60,50,49")
	base := atMod.signalSnapshot()
	atMod.parseLine("+CESQ: 99,99,255,255,255,255,57,52,48")
	if got := atMod.signalSnapshot(); got != base {
		t.Errorf("AT 应答路径的 CESQ 不该覆盖已有值（只补空）：%+v -> %+v", base, got)
	}

	// 值为 0 = 本次未上报，两条路径都不该把已有值冲成 0
	before := mod.signalSnapshot()
	mod.feedURCLine("+CESQ: 0,0,0,0,0,0,0,0,0")
	if got := mod.signalSnapshot(); got.NRRSRP == 0 && before.NRRSRP != 0 {
		t.Error("CESQ 里的 0 是“未上报”，不该把已有 RSRP 冲成 0")
	}
}

// ③ 速度不闪：网络状态里必须带上当前速率（前端拿网络推送重建卡片时会用它）。
func TestNetworkStatusCarriesTraffic(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	mod.feedURCLine(`+CGREG: 1,"FFFE"`)

	// 还没有速率数据时：不塞一个假的 rate（前端就会显示“未上报”，与以前一致）
	if st := mod.networkStatusFromURC(); st.Traffic != nil {
		t.Errorf("没有速率数据时 Traffic 应为 nil，实际 %+v", st.Traffic)
	}

	// 有速率数据（由本机计数器采样得到）⇒ 必须带上，否则界面会一隐一显地闪
	mod.mu.Lock()
	mod.trafficRate = Rate{Supported: true, UpBps: 1234, DownBps: 5678, UpText: "1.2 KB/s", DownText: "5.5 KB/s"}
	mod.mu.Unlock()
	st := mod.networkStatusFromURC()
	if st.Traffic == nil || !st.Traffic.Supported {
		t.Fatalf("有速率时必须带上，实际 %+v", st.Traffic)
	}
	if st.Traffic.UpBps != 1234 || st.Traffic.DownBps != 5678 {
		t.Errorf("速率值应原样带上，实际 %+v", st.Traffic)
	}
	// 拷值：后续速率变化不该回头改写已经发出去的那份
	mod.mu.Lock()
	mod.trafficRate = Rate{Supported: true, UpBps: 9999}
	mod.mu.Unlock()
	if st.Traffic.UpBps != 1234 {
		t.Error("Traffic 必须拷值，不能与 mod.trafficRate 共享")
	}
	if r := (Rate{}); r.Supported {
		t.Error("零值 Rate 必须是不支持（前端据此隐藏速度）")
	}
	_ = time.Now
}
