package kernel

// 「接口登录保护」的单测（[ui] require_luci_login，LuCI「服务控制」页那个开关）。
//
// 用户 2026-10-06 定的口径是**默认打开、没有配置也是打开**：
//   · 老设备升级上来时 config.toml 里**根本没有这一行**（config.toml 是 conffile，
//     升级不会覆盖）—— 那就是"装了新版却没有登录框"的根因；
//   · 只有显式写 false（LuCI 开关写回的那一行）才是关。
// 所以这里盯四件事：
//  ① 不写这一行 → 默认开（bool 零值 false 不能当"没写"，必须靠 IsDefined）；
//  ② 显式 false → 关；
//  ③ 开关写回只动 [ui] 段的这一个键、注释原样保留、不产生第二行；
//  ④ 内存里的 Cfg 同步更新（闸门是每请求现读的，改完不必重启进程）。

import (
	"modemd/module/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLuciLoginDefaultTrue 不写 require_luci_login 时默认是**开**。
func TestLuciLoginDefaultTrue(t *testing.T) {
	const orig = "[server]\nlisten = \":8080\"\n\n[ui]\nsms_password = \"p@ss\"\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath, oldVal, oldSet := config.ConfigPath, config.Cfg.UI.RequireLuciLogin, config.Cfg.UI.RequireLuciLoginSet
	defer func() {
		config.ConfigPath, config.Cfg.UI.RequireLuciLogin, config.Cfg.UI.RequireLuciLoginSet = oldPath, oldVal, oldSet
	}()
	config.ConfigPath = path
	config.Cfg.UI.RequireLuciLogin, config.Cfg.UI.RequireLuciLoginSet = false, false

	if err := config.Load(); err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if config.Cfg.UI.RequireLuciLoginSet {
		t.Error(`配置里没写 require_luci_login 时不该被当成"显式写过"`)
	}
	if !config.Cfg.UI.RequireLuciLogin {
		t.Error("配置里没写 require_luci_login 时应该是开的（默认开：没有配置也是打开）")
	}
}

// TestLuciLoginExplicitFalse 显式写 false 才是关。
func TestLuciLoginExplicitFalse(t *testing.T) {
	const orig = "[server]\nlisten = \":8080\"\n\n[ui]\nrequire_luci_login = false\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath, oldVal, oldSet := config.ConfigPath, config.Cfg.UI.RequireLuciLogin, config.Cfg.UI.RequireLuciLoginSet
	defer func() {
		config.ConfigPath, config.Cfg.UI.RequireLuciLogin, config.Cfg.UI.RequireLuciLoginSet = oldPath, oldVal, oldSet
	}()
	config.ConfigPath = path
	config.Cfg.UI.RequireLuciLogin, config.Cfg.UI.RequireLuciLoginSet = true, false

	if err := config.Load(); err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if !config.Cfg.UI.RequireLuciLoginSet {
		t.Error("配置里写了 require_luci_login，就该被当成显式配置")
	}
	if config.Cfg.UI.RequireLuciLogin {
		t.Error("显式写 false 就是关")
	}
}

// TestSetUILoginWriteBack 写回：只动 [ui] 的 require_luci_login，注释与别的段原样保留。
func TestSetUILoginWriteBack(t *testing.T) {
	const orig = "# 顶部注释（别动我）\n" +
		"[server]\n" +
		"listen = \":8080\"\n" +
		"\n" +
		"[net]\n" +
		"auto_wan = true\n" +
		"\n" +
		"[ui]\n" +
		"# 短信访问密码\n" +
		"sms_password = \"p@ss\"\n" +
		"sms_password_required = true\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath, oldVal, oldSet := config.ConfigPath, config.Cfg.UI.RequireLuciLogin, config.Cfg.UI.RequireLuciLoginSet
	defer func() {
		config.ConfigPath, config.Cfg.UI.RequireLuciLogin, config.Cfg.UI.RequireLuciLoginSet = oldPath, oldVal, oldSet
	}()
	config.ConfigPath = path
	// 模拟"这台机器上装的是最新版、但配置里从来没有这一行"
	config.Cfg.UI.RequireLuciLogin, config.Cfg.UI.RequireLuciLoginSet = true, false

	// 关掉：键不存在 → 插进 [ui] 段
	if err := config.SetUILogin(false); err != nil {
		t.Fatalf("写回 false 失败: %v", err)
	}
	if config.Cfg.UI.RequireLuciLogin {
		t.Error("写回 false 之后内存里的 Cfg 也该是 false（闸门是每请求现读的）")
	}
	if !config.Cfg.UI.RequireLuciLoginSet {
		t.Error("写回之后就算「显式写过」了")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "require_luci_login = false") {
		t.Errorf("文件里没有 require_luci_login = false：\n%s", got)
	}
	if n := strings.Count(got, "require_luci_login"); n != 1 {
		t.Errorf("require_luci_login 该只有一行，实际 %d 行：\n%s", n, got)
	}
	// 必须落在 [ui] 段里（插错段会让配置读不出来）
	if uiIdx, keyIdx := strings.Index(got, "[ui]"), strings.Index(got, "require_luci_login"); uiIdx < 0 || keyIdx < uiIdx {
		t.Errorf("require_luci_login 没落在 [ui] 段里：\n%s", got)
	}
	for _, keep := range []string{
		"# 顶部注释（别动我）",
		"# 短信访问密码",
		"sms_password = \"p@ss\"",
		"sms_password_required = true",
		"listen = \":8080\"",
		"auto_wan = true",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("写回把 %q 弄丢了：\n%s", keep, got)
		}
	}

	// 再开回来：不能出现第二行
	if err := config.SetUILogin(true); err != nil {
		t.Fatalf("写回 true 失败: %v", err)
	}
	if !config.Cfg.UI.RequireLuciLogin {
		t.Error("写回 true 之后内存里的 Cfg 该是 true")
	}
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), "require_luci_login = true") {
		t.Errorf("文件里没有 require_luci_login = true：\n%s", string(b))
	}
	if n := strings.Count(string(b), "require_luci_login"); n != 1 {
		t.Errorf("写回两次后 require_luci_login 仍该只有一行，实际 %d 行：\n%s", n, string(b))
	}
}

// TestSetUILoginCreatesSection 整个文件没有 [ui] 段时要能新建一节（老配置就是这么升上来的）。
func TestSetUILoginCreatesSection(t *testing.T) {
	const orig = "[server]\nlisten = \":8080\"\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath, oldVal, oldSet := config.ConfigPath, config.Cfg.UI.RequireLuciLogin, config.Cfg.UI.RequireLuciLoginSet
	defer func() {
		config.ConfigPath, config.Cfg.UI.RequireLuciLogin, config.Cfg.UI.RequireLuciLoginSet = oldPath, oldVal, oldSet
	}()
	config.ConfigPath = path

	if err := config.SetUILogin(false); err != nil {
		t.Fatalf("写回失败: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "[ui]") || !strings.Contains(got, "require_luci_login = false") {
		t.Errorf("没建出 [ui] 段或没写进键：\n%s", got)
	}
	// 写完还要能被真正读出来（Load 用同一套 TOML 解析）
	config.Cfg.UI.RequireLuciLogin, config.Cfg.UI.RequireLuciLoginSet = true, false
	if err := config.Load(); err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if config.Cfg.UI.RequireLuciLogin {
		t.Error("写进去的 false 应该能读回来")
	}
}
