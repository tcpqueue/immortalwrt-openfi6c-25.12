package main

import (
	"embed"
	"modemd/module/httpx"
	"net/http"
)

// uiHTML 内置调试台页面。
// 通过 go:embed 打包进二进制，路由器上无需额外部署静态文件。
//
//go:embed files/web/index.html
var uiHTML []byte

//go:embed files/web/layouts/*.css
var uiLayouts embed.FS

// miniHTML 悬浮小窗的**内容页**（单独一页，只画运营商 / 基本信号 / 短信未读角标）。
//
// 为什么单独一页（而不是把整份设备中心塞进小窗）：
//   - 小窗只要一眼能看到的东西，整份调试台又大又吵、还带自己的一套页签；
//   - 这一页跑在 modemd 自己的源上，能直接连 /ws 收推送，不必给 modemd 加 CORS。
//
//go:embed files/web/mini.html
var miniHTML []byte

// apiUI GET /  内置调试台页面（管理/调试所有接口的 Web UI）
//
//	GET /mini  悬浮小窗的内容页（LuCI 侧的小窗 iframe 用它）
//	GET /mini.html  同上（便于直接打开调试）
func apiUI() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/", "/ui":
			writeHTML(w, uiHTML)
		case "/mini", "/mini.html", "/mini/":
			writeHTML(w, miniHTML)
		case "/layouts/standard.css", "/layouts/outline.css", "/layouts/soft.css", "/layouts/panel.css":
			// embed 的路径前缀就是仓库里的 files/web/（见上面的 go:embed）
			css, err := uiLayouts.ReadFile("files/web" + r.URL.Path)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(css)
		default:
			// "/" 这条是兜底路由：其余未注册路径一律按 404 处理
			w.WriteHeader(http.StatusNotFound)
			httpx.WriteJSON(w, -1, nil, "not found")
		}
	}
}

// writeHTML 输出一页内置页面：不缓存（页面随二进制走，改完重启就该看到新的）
func writeHTML(w http.ResponseWriter, page []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(page)
}
