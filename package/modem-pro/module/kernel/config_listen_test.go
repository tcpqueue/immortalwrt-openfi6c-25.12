package kernel

// Web 界面端口（`[server] listen`）的单测。
//
// 为什么要有：端口是**可配置**的（默认 9527），而它散落在 Go 默认值、LuCI 控制器、前端 JS、
// postinst 打印、打包文档里 —— 任何一处退回写死 8080，都会变成"改了配置但界面指错端口"。
// 这里钉住 Go 侧两件事：
//  ① 兜底值就是 DefaultListenAddr；**空串绝不能落到 http.Server 的 `:http`（= 80 端口）**；
//  ② 配置里写了就以配置为准（含 127.0.0.1:xxxx 这种只监听本机的写法）。

import (
	"modemd/module/config"
	"os"
	"path/filepath"
	"testing"
)

func TestListenPortDefaultAndOverride(t *testing.T) {
	if config.DefaultListenAddr != "127.0.0.1:9527" {
		t.Fatalf("DefaultListenAddr = %q，want 127.0.0.1:9527",
			config.DefaultListenAddr)
	}

	cases := []struct {
		name string
		cfg  string
		want string
	}{
		{"不写 listen 用默认", "[server]\nscan_interval = 3\n", config.DefaultListenAddr},
		{"显式空串也用默认", "[server]\nlisten = \"\"\n", config.DefaultListenAddr},
		{"写了就照写的用", "[server]\nlisten = \"127.0.0.1:18080\"\n", "127.0.0.1:18080"},
	}

	oldPath, oldListen := config.ConfigPath, config.Cfg.Server.Listen
	defer func() { config.ConfigPath, config.Cfg.Server.Listen = oldPath, oldListen }()

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			if err := os.WriteFile(path, []byte(c.cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			config.ConfigPath = path
			config.Cfg.Server.Listen = ""
			if err := config.Load(); err != nil {
				t.Fatalf("Load: %v", err)
			}
			if config.Cfg.Server.Listen != c.want {
				t.Fatalf("Server.Listen = %q, want %q", config.Cfg.Server.Listen, c.want)
			}
		})
	}
}
