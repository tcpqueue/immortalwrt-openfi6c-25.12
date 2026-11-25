package kernel

// 「拨号总开关」的单测（[net] dial_enabled，页面上「拨号上网 → 拨号开关」）。
//
// 关注四点：
//  ① 写回只动 dial_enabled 一个键（config.toml 的注释是给用户看的说明书，不能被吃掉）；
//  ② 写回后内存里的 Cfg 同步更新（不然要等进程重启才生效）；
//  ③ **关了之后真的一个拨号入口都不放行**：手动拨号拒、主机侧拨号器报"总开关已关"、
//     写 APN 时不自动激活 —— 这就是页面那句"所有模式都不需要拨号"的落地；
//  ④ 默认是**开**（不写配置算开）—— 否则升级上来的设备会静默不拨号。

import (
	"modemd/module/config"
	"modemd/module/dial"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetDialEnabledWriteBack(t *testing.T) {
	const orig = "# 顶部注释（别动我）\n" +
		"[server]\n" +
		"listen = \":8080\"\n" +
		"\n" +
		"[net]\n" +
		"auto_wan = true\n" +
		"# 拨号总开关（页面上「拨号上网 → 拨号开关」）\n" +
		"wan_metric = 20\n" +
		"\n" +
		"[ui]\n" +
		"sms_password = \"p@ss\"\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	// 配置路径与开关都是全局变量：测完还原，别影响同包里别的用例
	// ⚠️ DialEnabledSet 也必须一起还原：它参与 DialEnabled() 的判定，漏掉会让后面的
	// 用例拿到"显式关掉"的假状态（实测把它泄漏出去，hostdial / dial 的用例全红）。
	oldPath, oldVal, oldSet := config.ConfigPath, config.Cfg.Net.DialEnabled, config.Cfg.Net.DialEnabledSet
	defer func() {
		config.ConfigPath, config.Cfg.Net.DialEnabled, config.Cfg.Net.DialEnabledSet = oldPath, oldVal, oldSet
	}()
	config.ConfigPath = path

	if err := config.SetDialEnabled(false); err != nil {
		t.Fatalf("写回 false 失败: %v", err)
	}
	if config.Cfg.Net.DialEnabled {
		t.Error("写回 false 之后内存里的 Cfg 也该是 false（不然要等进程重启）")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "dial_enabled = false") {
		t.Errorf("文件里没有 dial_enabled = false：\n%s", got)
	}
	if n := strings.Count(got, "dial_enabled"); n != 1 {
		t.Errorf("dial_enabled 该只有一行，实际 %d 行：\n%s", n, got)
	}
	// 注释与别的段必须原样保留
	for _, keep := range []string{
		"# 顶部注释（别动我）",
		"# 拨号总开关（页面上「拨号上网 → 拨号开关」）",
		"sms_password = \"p@ss\"",
		"listen = \":8080\"",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("写回把 %q 弄丢了：\n%s", keep, got)
		}
	}

	// 再开回来：文件里不应还留着 false
	if err := config.SetDialEnabled(true); err != nil {
		t.Fatalf("写回 true 失败: %v", err)
	}
	if !config.Cfg.Net.DialEnabled {
		t.Error("写回 true 之后内存里的 Cfg 该是 true")
	}
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), "dial_enabled = true") {
		t.Errorf("文件里没有 dial_enabled = true：\n%s", string(b))
	}
}

// TestDialEnabledDefaultTrue 不写 dial_enabled 时默认是开。
// ⚠️ 这条很关键：bool 的零值是 false，忘了 IsDefined/DialEnabledSet 这一层判断就会把
// "没写"当成"关掉"，升级上来的设备一启动就永远不拨号（页面上还会显示"已关闭"）。
func TestDialEnabledDefaultTrue(t *testing.T) {
	const orig = "[server]\nlisten = \":8080\"\n\n[net]\nauto_wan = true\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath, oldVal, oldSet := config.ConfigPath, config.Cfg.Net.DialEnabled, config.Cfg.Net.DialEnabledSet
	defer func() {
		config.ConfigPath, config.Cfg.Net.DialEnabled, config.Cfg.Net.DialEnabledSet = oldPath, oldVal, oldSet
	}()
	config.ConfigPath = path

	if err := config.Load(); err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if config.Cfg.Net.DialEnabledSet {
		t.Error(`配置里没写 dial_enabled 时不该被当成"显式写过"`)
	}
	if !config.DialEnabled() {
		t.Error("配置里没写 dial_enabled 时应该是开的（默认开）")
	}
}

// TestDialRefusedWhenDisabled 总开关关掉时，手动拨号必须在**入口**就被拒（不下发任何 AT）。
func TestDialRefusedWhenDisabled(t *testing.T) {
	old, oldSet := config.Cfg.Net.DialEnabled, config.Cfg.Net.DialEnabledSet
	defer func() { config.Cfg.Net.DialEnabled, config.Cfg.Net.DialEnabledSet = old, oldSet }()
	config.Cfg.Net.DialEnabled, config.Cfg.Net.DialEnabledSet = false, true

	mod := &Modem{ID: 7}
	_, err := mod.Dial(dial.DialRequest{CID: 1})
	if err == nil {
		t.Fatalf("总开关关掉时手动拨号应该被拒")
	}
	if !strings.Contains(err.Error(), "拨号总开关") {
		t.Errorf("错误信息该说清是总开关：%v", err)
	}
}

// TestHostDialStopReasonMasterSwitch 总开关关掉时，主机侧拨号器（QMI）的停止原因
// 要写成"总开关已关"，而且**优先于**别的理由（否则日志里看到的是"配置关掉了"，
// 用户会去翻 config.toml 找 host_dial，找不到真原因）。
func TestHostDialStopReasonMasterSwitch(t *testing.T) {
	oldDial, oldSet, oldHostDial := config.Cfg.Net.DialEnabled, config.Cfg.Net.DialEnabledSet, config.Cfg.Net.HostDial
	defer func() {
		config.Cfg.Net.DialEnabled, config.Cfg.Net.DialEnabledSet, config.Cfg.Net.HostDial = oldDial, oldSet, oldHostDial
	}()
	config.Cfg.Net.HostDial = true // 配置是开的，只有总开关关着

	config.Cfg.Net.DialEnabled, config.Cfg.Net.DialEnabledSet = false, true
	mod := &Modem{ID: 8}
	mod.isRunning = true
	reason := hostDialStopReason(mod, &dial.DialState{}, "wwan0")
	if !strings.Contains(reason, "总开关") {
		t.Errorf("停止原因该是总开关：%q", reason)
	}

	config.Cfg.Net.DialEnabled = true
	if reason := hostDialStopReason(mod, &dial.DialState{}, "wwan0"); reason != "" {
		t.Errorf("总开关打开后不该再拦拨号器，实际 %q", reason)
	}
}
