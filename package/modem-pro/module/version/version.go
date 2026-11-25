package version

import (
	"modemd/module/luciauth"
	"runtime"
)

// ====================== 版本信息 ======================
//
// BuildVersion 由打包脚本用 `-ldflags "-X modemd/module/version.BuildVersion=x.y.z-1"` 注入
// （由本包 OpenWrt Makefile 注入版本）；直接 `go build` / `go run` 时是 "dev"。
//
// 为什么要在页面上显示它：**排查"界面怎么没有某个功能"的第一步就是核对版本** ——
// 升了包但浏览器还开着旧页面、或者装的还是上一个包时，一眼就能看出来，
// 不用靠"你点的是哪个菜单"来回猜。

// BuildVersion 构建版本（打包时注入）
var BuildVersion = "dev"

// VersionInfo 版本 + 运行时信息（GET /api/version）
type VersionInfo struct {
	Version  string `json:"version"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
	// LuciAuth 是否启用了「跟随 LuCI 登录」（[ui] require_luci_login + 本机有 ubus）。
	// 放在这里是因为 /api/version 是**唯一**不需要会话就能看的接口：
	// 被 401 拦住时，第一个想知道的就是“这个闸门到底开没开”。
	LuciAuth bool `json:"luci_auth"`
}

// CurrentVersion 当前二进制的版本信息
func CurrentVersion() VersionInfo {
	return VersionInfo{
		Version:  BuildVersion,
		Go:       runtime.Version(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
		LuciAuth: luciauth.LuciAuthEnabled(),
	}
}
