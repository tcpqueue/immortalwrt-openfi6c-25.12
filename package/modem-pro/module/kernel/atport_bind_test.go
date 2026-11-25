package kernel

// ====================== 命令口绑定（VID:PID）：纯逻辑用例 ======================
//
// 覆盖：vid:pid 归一化、绑定存储（存/读/解绑/落盘）、"绑的口不在这台设备的串口里"的退化、
// "绑定失效"日志的限速。跨平台（atport_bind.go 没有 build tag），Windows 上也能跑。

import (
	"modemd/module/atport"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestATPortBindKey vid:pid 的归一化（大小写、0x、短横线都认；缺一半 ⇒ 空串）
func TestATPortBindKey(t *testing.T) {
	for in, want := range map[string][2]string{
		"0E8D|7126":     {"0e8d", "7126"},
		"0x2cb7|0x0a06": {"2cb7", "0a06"},
		"3466|3301":     {"3466", "3301"},
	} {
		vid, pid, _ := splitTestVidPid(in)
		if got := atport.ATPortBindKey(vid, pid); got != want[0]+":"+want[1] {
			t.Errorf("ATPortBindKey(%q,%q) = %q，期望 %q", vid, pid, got, want[0]+":"+want[1])
		}
	}
	// 缺一半 / 写的是中文 ⇒ 认不出来（返回空串，界面会提示）
	for _, c := range [][2]string{{"0e8d", ""}, {"", "7126"}, {"abc", "7126"}, {"0e8d", "xyz"}} {
		if got := atport.ATPortBindKey(c[0], c[1]); got != "" {
			t.Errorf("ATPortBindKey(%q,%q) = %q，期望空串", c[0], c[1], got)
		}
	}
}

// splitTestVidPid 把 "0E8D|7126" 拆成两半（测试里的小工具，避免用 strings.Split 记错口径）
func splitTestVidPid(s string) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '|' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

// TestATPortBindStoreRoundTrip 绑定存储：存 → 读 → 改 → 解绑，并且真的落盘
func TestATPortBindStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atport_bind.json")
	atport.InitATPortBind(path)
	defer atport.InitATPortBind("") // 别把临时目录留给后面的用例

	if p := atport.ATPortBindOf("0e8d", "7126"); p != "" {
		t.Fatalf("刚载入应该是空，得到 %q", p)
	}
	key, changed, err := atport.SetATPortBind("0E8D", "7126", "/dev/ttyUSB2")
	if err != nil || !changed || key != "0e8d:7126" {
		t.Fatalf("SetATPortBind = (%q,%v,%v)，期望 (0e8d:7126,true,nil)", key, changed, err)
	}
	if p := atport.ATPortBindOf("0e8d", "7126"); p != "/dev/ttyUSB2" {
		t.Errorf("读回 = %q，期望 /dev/ttyUSB2", p)
	}
	// 同一个值再存一次 ⇒ "没变化"（界面据此不必重复提示）
	if _, changed, _ := atport.SetATPortBind("0e8d", "7126", "/dev/ttyUSB2"); changed {
		t.Error("值没变时 changed 应该是 false")
	}
	// 落盘了吗：文件里要有这条
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("绑定文件没写出来：%v", err)
	}
	if !containsBytes(b, []byte(`"0e8d:7126": "/dev/ttyUSB2"`)) {
		t.Errorf("文件里没有这条绑定：\n%s", string(b))
	}
	// 换端口：列表要跟着变，Present 按传入的串口列表判断
	if _, _, err := atport.SetATPortBind("0e8d", "7126", "/dev/ttyUSB3"); err != nil {
		t.Fatalf("改绑定失败：%v", err)
	}
	list := atport.ATPortBindListWithPresence([]string{"/dev/ttyUSB3", "/dev/ttyUSB9"})
	if len(list) != 1 || list[0].Key != "0e8d:7126" || list[0].Port != "/dev/ttyUSB3" || !list[0].Present {
		t.Errorf("列表不对：%+v", list)
	}
	// 不在串口列表里的口 ⇒ Present=false（界面提示"绑的口现在不在"）
	list = atport.ATPortBindListWithPresence([]string{"/dev/ttyUSB9"})
	if len(list) != 1 || list[0].Present {
		t.Errorf("口不在列表里时 Present 应为 false：%+v", list)
	}
	// 解绑（port 为空）
	if _, changed, err := atport.SetATPortBind("0e8d", "7126", ""); err != nil || !changed {
		t.Fatalf("解绑 = (%v,%v)，期望 (true,nil)", changed, err)
	}
	if p := atport.ATPortBindOf("0e8d", "7126"); p != "" {
		t.Errorf("解绑后还读得到：%q", p)
	}
	if len(atport.ATPortBindList()) != 0 {
		t.Errorf("解绑后列表应该空：%+v", atport.ATPortBindList())
	}
}

// TestSetATPortBindBadKey vid/pid 写错 ⇒ 报错、不落盘
func TestSetATPortBindBadKey(t *testing.T) {
	atport.InitATPortBind(filepath.Join(t.TempDir(), "atport_bind.json"))
	defer atport.InitATPortBind("")
	if _, _, err := atport.SetATPortBind("0e8d", "", "/dev/ttyUSB2"); err == nil {
		t.Error("pid 空应该报错")
	}
	if len(atport.ATPortBindList()) != 0 {
		t.Error("报错时不该存下任何东西")
	}
}

// TestBoundATPortPick 绑的口必须**确实在这台设备的串口里**才算数
func TestBoundATPortPick(t *testing.T) {
	group := []string{"/dev/ttyUSB0", "/dev/ttyUSB2", "/dev/ttyUSB3"}
	if p, ok := atport.BoundATPortPick("/dev/ttyUSB2", group); !ok || p != "/dev/ttyUSB2" {
		t.Errorf("boundATPortPick = (%q,%v)，期望 (/dev/ttyUSB2,true)", p, ok)
	}
	// 口名变了（换了固件/USB 模式）⇒ 不生效，退回自动探测
	if p, ok := atport.BoundATPortPick("/dev/ttyUSB1", group); ok || p != "" {
		t.Errorf("绑的口不在本组时应退回扫描，得到 (%q,%v)", p, ok)
	}
	if p, ok := atport.BoundATPortPick("", group); ok || p != "" {
		t.Errorf("没绑过 ⇒ (%q,%v)", p, ok)
	}
}

// TestATPortBindWarnThrottle 绑定失效日志限速：同一台设备 60 秒只允许说一次
func TestATPortBindWarnThrottle(t *testing.T) {
	atport.AtPortBindWarnMu.Lock()
	atport.AtPortBindWarnedAt = map[string]time.Time{} // 清掉上一条用例的痕迹
	atport.AtPortBindWarnMu.Unlock()
	now := time.Now()
	if !atport.AtPortBindWarnAllowed("0e8d", "7126", now) {
		t.Error("第一次应该允许")
	}
	if atport.AtPortBindWarnAllowed("0e8d", "7126", now.Add(10*time.Second)) {
		t.Error("10 秒内不该再打印")
	}
	if !atport.AtPortBindWarnAllowed("0e8d", "7126", now.Add(2*time.Minute)) {
		t.Error("超过 60 秒后应该允许")
	}
	// 另一台设备互不影响
	if !atport.AtPortBindWarnAllowed("2c7c", "0800", now) {
		t.Error("另一台设备应该允许")
	}
}

// containsBytes 小工具（不引 bytes 包，保持这个文件的依赖干净）
func containsBytes(hay, needle []byte) bool {
	if len(needle) == 0 || len(hay) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		if string(hay[i:i+len(needle)]) == string(needle) {
			return true
		}
	}
	return false
}
