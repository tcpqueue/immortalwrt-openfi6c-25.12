package kernel

import (
	"modemd/module/netdev"
	"os"
	"path/filepath"
	"testing"
)

// TestPickATPort 挑 AT 命令口：**"回了 OK"只是必要条件**，能答身份指令的优先。
//
// 背景（2026-10-07 真机）：模组切到 MBIM 后 /dev/ttyUSB1 会对 `AT` 回一个 OK
// （固件的杂散输出里带 OK），但发真指令全部超时 —— 用户看到的现象是"控制不了"。
// 所以"有身份证据"的口必须优先于"只是回了个 OK"的口。
func TestPickATPort(t *testing.T) {
	cases := []struct {
		name  string
		cands []ATCandidate
		want  string
	}{
		{"没人应答", nil, ""},
		{"只有一个回 OK", []ATCandidate{{Port: "p1", OK: true}}, "p1"},
		{"第一个只有 OK、第二个有身份证据", []ATCandidate{{Port: "p1", OK: true}, {Port: "p2", OK: true, Identity: true}}, "p2"},
		{"有身份证据的排在后面也选它", []ATCandidate{{Port: "p1", OK: true}, {Port: "p2"}, {Port: "p3", OK: true, Identity: true}}, "p3"},
		{"全都没身份证据 ⇒ 退回第一个（与老行为一致）", []ATCandidate{{Port: "p1", OK: true}, {Port: "p2", OK: true}}, "p1"},
		{"没回 OK 的不算", []ATCandidate{{Port: "p1"}, {Port: "p2", OK: true, Identity: true}}, "p2"},
		{"Identity 但没有 OK ⇒ 不算（顺序上不可能，防手误）", []ATCandidate{{Port: "p1", Identity: true}}, ""},
	}
	for _, c := range cases {
		if got := PickATPort(c.cands); got != c.want {
			t.Errorf("%s: PickATPort = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestATPortOptions 下拉候选：系统探测到的那个排最前，其余按原顺序（去空去重）
func TestATPortOptions(t *testing.T) {
	got := ATPortOptions([]string{"/dev/ttyUSB0", "/dev/ttyUSB2", "", "/dev/ttyUSB2", "/dev/ttyUSB3"}, "/dev/ttyUSB2")
	want := []string{"/dev/ttyUSB2", "/dev/ttyUSB0", "/dev/ttyUSB3"}
	if len(got) != len(want) {
		t.Fatalf("ATPortOptions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ATPortOptions = %v, want %v", got, want)
		}
	}
	if got := ATPortOptions(nil, ""); len(got) != 0 {
		t.Errorf("没有候选时应该是空的，得到 %v", got)
	}
}

// TestATPortKey 手动指定的键用"模组身份"：IMEI 优先；都没有就**不记**（宁可不记，别串台）
func TestATPortKey(t *testing.T) {
	if got := ATPortKey(&Modem{IMEI: " 869955063166602 ", ModelName: "RM500U-CNV"}); got != "imei:869955063166602" {
		t.Errorf("有 IMEI 时用 IMEI，得到 %q", got)
	}
	if got := ATPortKey(&Modem{ModelName: "RM500U-CNV"}); got != "model:RM500U-CNV" {
		t.Errorf("没 IMEI 时退到型号，得到 %q", got)
	}
	if got := ATPortKey(&Modem{}); got != "" {
		t.Errorf("身份都没有时不该给键，得到 %q", got)
	}
	if got := ATPortKey(nil); got != "" {
		t.Errorf("nil 该返回空串，得到 %q", got)
	}
}

// TestATPortFallbackKey 身份读不出来时的兜底键 = "串口组"（未识别模组全靠它才存得下设置）。
//
// 顺序必须稳定（分组顺序不保证），否则同一个模组每轮算出不同的键、设置就"丢"了。
func TestATPortFallbackKey(t *testing.T) {
	got := ATPortFallbackKey([]string{"/dev/ttyUSB2", "/dev/ttyUSB0", "/dev/ttyUSB2", " /dev/ttyUSB1 "})
	want := "ports:/dev/ttyUSB0,/dev/ttyUSB1,/dev/ttyUSB2"
	if got != want {
		t.Errorf("ATPortFallbackKey = %q, want %q", got, want)
	}
	if got := ATPortFallbackKey([]string{"", "   "}); got != "" {
		t.Errorf("没有串口时不该给键，得到 %q", got)
	}
	if got := ATPortFallbackKey(nil); got != "" {
		t.Errorf("nil 该返回空串，得到 %q", got)
	}
}

// TestATPortSavedKey 保存时用哪个键：身份优先，读不出身份就退到串口组
func TestATPortSavedKey(t *testing.T) {
	ports := []string{"/dev/ttyUSB0", "/dev/ttyUSB1", "/dev/ttyUSB2"}
	if got := ATPortSavedKey(&Modem{IMEI: "123", TTYList: ports}); got != "imei:123" {
		t.Errorf("有 IMEI 时用 IMEI，得到 %q", got)
	}
	if got := ATPortSavedKey(&Modem{ModelName: "RM500U-CNV", TTYList: ports}); got != "model:RM500U-CNV" {
		t.Errorf("没 IMEI 时退到型号，得到 %q", got)
	}
	// ⚠️ 未识别模组（没有 IMEI / 型号）：以前这里给不出键 ⇒ 界面存不了 ⇒ 用户修不回来
	if got := ATPortSavedKey(&Modem{TTYList: ports}); got != ATPortFallbackKey(ports) {
		t.Errorf("未识别模组该退到串口组键，得到 %q", got)
	}
	if got := ATPortSavedKey(nil); got != "" {
		t.Errorf("nil 该返回空串，得到 %q", got)
	}
}

// TestSavedATPortForUnidentifiedModem 未识别模组存下的指定：读得回来，且身份读出来之后仍然认。
func TestSavedATPortForUnidentifiedModem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atport.json")
	old := atPortStoreInst
	store := &atPortStore{data: map[string]string{}}
	store.Init(path)
	atPortStoreInst = store
	defer func() { atPortStoreInst = old }()

	ports := []string{"/dev/ttyUSB0", "/dev/ttyUSB1", "/dev/ttyUSB2"}
	mod := &Modem{ID: 0, ATPort: "/dev/ttyUSB1", TTYList: ports} // 自动探测挑中的是"错"的那个口
	if p := savedATPort(mod); p != "" {
		t.Fatalf("还没存过时应该没有指定，得到 %q", p)
	}
	key := ATPortSavedKey(mod)
	if !atPortStoreInst.Set(key, "/dev/ttyUSB2") {
		t.Fatal("未识别模组也该能存下来")
	}
	if p := savedATPort(mod); p != "/dev/ttyUSB2" {
		t.Fatalf("存的指定应该读得回来，得到 %q", p)
	}
	// 换个顺序、带重复口的分组 = 同一台设备 ⇒ 同一个键，指定照样有效
	same := &Modem{ATPort: "/dev/ttyUSB1", TTYList: []string{"/dev/ttyUSB2", "/dev/ttyUSB2", "/dev/ttyUSB0", "/dev/ttyUSB1"}}
	if p := savedATPort(same); p != "/dev/ttyUSB2" {
		t.Errorf("同一台设备的串口顺序变了也要认得出来，得到 %q", p)
	}
	// 身份读出来之后：**保存**写到身份键（更精确），但"未识别时期"存的那条照样读得回来
	known := &Modem{IMEI: "869955063166602", TTYList: ports}
	if p := savedATPort(known); p != "/dev/ttyUSB2" {
		t.Errorf("身份读出来之后也要沿用之前的指定，得到 %q", p)
	}
	if k := ATPortSavedKey(known); k != "imei:869955063166602" {
		t.Errorf("身份读出来了就该写身份键，得到 %q", k)
	}
	// 命中哪个键也要给对（发现流程判定失效时按它删）
	if p, k := savedATPortKeyed(known); p != "/dev/ttyUSB2" || k != ATPortFallbackKey(ports) {
		t.Errorf("savedATPortKeyed = (%q,%q)，期望 (%q,%q)", p, k, "/dev/ttyUSB2", ATPortFallbackKey(ports))
	}
}

// TestATIdentityLooksReal 身份应答"像不像真命令口"：剔掉回显与 OK/ERROR 后还得有内容
func TestATIdentityLooksReal(t *testing.T) {
	yes := []string{
		"AT+CGMM\r\r\nRM500U-CNV\r\nOK\r\n",
		"RM500U-CNV",
		"AT+CGMR\r\r\nM1250_RM500UCNVAA_BR0_R03.01.001\r\nOK\r\n",
	}
	for _, s := range yes {
		if !netdev.ATIdentityLooksReal(s) {
			t.Errorf("应该认成真命令口：%q", s)
		}
	}
	no := []string{
		"",
		"OK\r\n",            // ⚠️ 只回个 OK：不足以证明（MBIM 下那个假口就是这样）
		"AT+CGMM\r\nOK\r\n", // 回显 + OK，没有型号
		"ERROR\r\n",         // 明确拒绝
		"+CME ERROR: 4\r\n",
		"   \r\n",
	}
	for _, s := range no {
		if netdev.ATIdentityLooksReal(s) {
			t.Errorf("不该认成真命令口：%q", s)
		}
	}
}

// TestATPortStore 手动指定的命令口：写盘 / 读回（等价重启）/ 清除
func TestATPortStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atport.json")

	var s atPortStore
	s.Init(path)
	if got := s.Get("imei:1"); got != "" {
		t.Fatalf("初次应该没有记录，得到 %q", got)
	}
	if !s.Set("imei:1", "/dev/ttyUSB2") {
		t.Error("第一次设置应该算变化")
	}
	if s.Set("imei:1", "/dev/ttyUSB2") {
		t.Error("重复设同一个值不该算变化（免得反复写盘）")
	}
	if s.Set("", "/dev/ttyUSB2") {
		t.Error("没有键时不该记")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("应该已经落盘：%v", err)
	}

	// 换一个实例读回来 = 模拟重启
	var s2 atPortStore
	s2.Init(path)
	if got := s2.Get("imei:1"); got != "/dev/ttyUSB2" {
		t.Errorf("重启后应该还记得，得到 %q", got)
	}
	// 清除 = 恢复自动探测
	if !s2.Set("imei:1", "") {
		t.Error("清除应该算变化")
	}
	var s3 atPortStore
	s3.Init(path)
	if got := s3.Get("imei:1"); got != "" {
		t.Errorf("清掉后不该还有记录，得到 %q", got)
	}
	// 文件坏掉时不该崩，也不能把别的键读坏
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	var s4 atPortStore
	s4.Init(path)
	if got := s4.Get("imei:1"); got != "" {
		t.Errorf("坏文件应该当成没指定过，得到 %q", got)
	}
}
