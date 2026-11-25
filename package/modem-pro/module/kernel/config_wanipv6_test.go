package kernel

// 「模组侧 IPv6」开关写回配置文件的单测（[net] wan_ipv6，页面上「拨号上网」那个开关）。
//
// 关注三点：
//  ① 只动 wan_ipv6 一个键 —— config.toml 里的注释是给用户看的说明书，不能被工具吃掉；
//  ② 写回之后**内存里的 Cfg 同步更新**（不然要等进程重启才生效）；
//  ③ CRLF 文件不能凭空多出空行（setTomlKey 的老坑，见 config.go 里的说明）。

import (
	"modemd/module/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetWANIPv6WriteBack(t *testing.T) {
	const orig = "# 顶部注释（别动我）\n" +
		"[server]\n" +
		"listen = \":8080\"\n" +
		"\n" +
		"[net]\n" +
		"auto_wan = true\n" +
		"# 模组侧要不要用 IPv6（页面上「拨号上网」那个开关）\n" +
		"wan_metric = 20\n" +
		"\n" +
		"[ui]\n" +
		"sms_password = \"p@ss\"\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	// 配置路径是全局变量：测完还原，别影响同包里别的用例
	oldPath, oldVal := config.ConfigPath, config.Cfg.Net.WANIPv6
	defer func() { config.ConfigPath, config.Cfg.Net.WANIPv6 = oldPath, oldVal }()
	config.ConfigPath = path

	if err := config.SetWANIPv6(false); err != nil {
		t.Fatalf("写回 false 失败: %v", err)
	}
	if config.Cfg.Net.WANIPv6 {
		t.Error("写回 false 之后内存里的 Cfg 也该是 false（不然要等进程重启）")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "wan_ipv6 = false") {
		t.Errorf("文件里没有 wan_ipv6 = false：\n%s", got)
	}
	if strings.Contains(got, "wan_ipv6 = true") {
		t.Errorf("写到 false 之后不该还留着 true：\n%s", got)
	}
	if n := strings.Count(got, "wan_ipv6"); n != 1 {
		t.Errorf("wan_ipv6 该只有一行，实际 %d 行：\n%s", n, got)
	}
	// 注释与别的段必须原样保留
	for _, keep := range []string{
		"# 顶部注释（别动我）",
		"# 模组侧要不要用 IPv6（页面上「拨号上网」那个开关）",
		"sms_password = \"p@ss\"",
		"listen = \":8080\"",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("写回把 %q 弄丢了：\n%s", keep, got)
		}
	}
	// 备份：上一版要留着
	if bak, err := os.ReadFile(path + ".bak"); err != nil || string(bak) != orig {
		t.Errorf("config.toml.bak 该是上一版内容（err=%v）：\n%s", err, string(bak))
	}
	// 不留临时文件
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("写回之后不该留下 config.toml.tmp")
	}

	// 再切回 true：还是只动那一行
	if err := config.SetWANIPv6(true); err != nil {
		t.Fatalf("写回 true 失败: %v", err)
	}
	if !config.Cfg.Net.WANIPv6 {
		t.Error("写回 true 之后内存里的 Cfg 也该是 true")
	}
	b, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got = string(b)
	if !strings.Contains(got, "wan_ipv6 = true") || strings.Contains(got, "wan_ipv6 = false") {
		t.Errorf("切回 true 之后内容不对：\n%s", got)
	}
	if n := strings.Count(got, "wan_ipv6"); n != 1 {
		t.Errorf("来回切之后 wan_ipv6 还是该只有一行，实际 %d 行：\n%s", n, got)
	}
}

// TestSetWANIPv6WriteBackCRLF CRLF 的文件写回之后不能出现 "\r\n\n"（凭空多空行）
func TestSetWANIPv6WriteBackCRLF(t *testing.T) {
	lines := []string{
		"[net]",
		"auto_wan = true",
		"wan_metric = 20",
		"",
		"[net_extra]",
		"wan_ipv6 = true",
	}
	orig := strings.Join(lines, "\r\n") + "\r\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath, oldVal := config.ConfigPath, config.Cfg.Net.WANIPv6
	defer func() { config.ConfigPath, config.Cfg.Net.WANIPv6 = oldPath, oldVal }()
	config.ConfigPath = path

	if err := config.SetWANIPv6(false); err != nil {
		t.Fatalf("写回失败: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "wan_ipv6 = false\r\n") {
		t.Errorf("插入的新行该保持 CRLF：\n%q", got)
	}
	if strings.Contains(got, "\r\n\n") {
		t.Errorf("CRLF 文件写回后多出了空行：\n%q", got)
	}
	// [net_extra] 段里的同名键不能被误改（段名是前缀匹配的经典坑）
	if !strings.Contains(got, "wan_ipv6 = true") {
		t.Errorf("别的段里的同名键不该被动：\n%q", got)
	}
	// 行数不变（插入一行 +1）
	if n := len(strings.Split(strings.TrimSuffix(got, "\n"), "\n")); n != len(lines)+1 {
		t.Errorf("行数 = %d, want %d：\n%q", n, len(lines)+1, got)
	}
}
