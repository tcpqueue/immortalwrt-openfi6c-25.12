package kernel

// ====================== 命令口绑定 API（按 USB 设备 VID:PID） ======================
//
//	GET  /api/atport/bind   列出全部绑定（含"这个口现在还在不在"）+ 本机串口
//	POST /api/atport/bind   {"vid":"0e8d","pid":"7126","port":"/dev/ttyUSB2"}
//	                        （port 传空串 = 解绑，恢复自动扫描）
//
// 绑上之后**发现流程直接用这个口当 AT 口，不再扫描/探测哪个口是命令口**（见 modem.go 的 ⓪
// 与 atport_bind.go）。为什么需要：自动探测只认"对 AT 回 OK"，挑错口时指令一条都跑不出去、
// 连 IMEI/型号都读不出来 ⇒ 按"模组身份"指定那套也救不了，只能按 **设备身份** 钉死。
//
// ⚠️ Windows 拿不到 sysfs 的 VID/PID ⇒ `supported=false`，接口照常返回、界面会说明。

import (
	"modemd/module/atport"
	"modemd/module/config"
	"modemd/module/httpx"
	"modemd/module/netdev"
	"net/http"
	"strings"
)

func apiATPortBind(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ports, _ := netdev.ScanTTYUSB()
		switch r.Method {
		case http.MethodGet:
			httpx.WriteJSON(w, 0, map[string]any{
				"supported": atport.ATPortBindSupported(),
				"path":      atport.ATPortBindPath(config.ConfigPath),
				"ports":     ports,
				"binds":     atport.ATPortBindListWithPresence(ports),
			}, "ok")
			return
		case http.MethodPost:
		default:
			httpx.WriteJSON(w, -1, nil, "method not allowed, use GET/POST")
			return
		}
		if !atport.ATPortBindSupported() {
			httpx.WriteJSON(w, -1, nil, "本平台不支持按 VID:PID 绑定命令口（Windows 请在「AT 指令」页的串口下拉里指定）")
			return
		}
		var req struct {
			VID  string `json:"vid"`
			PID  string `json:"pid"`
			Port string `json:"port"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		port := strings.TrimSpace(req.Port)
		if port != "" && !portExists(port) {
			httpx.WriteJSON(w, -1, nil, "串口 "+port+" 不在本机串口列表里")
			return
		}
		key, changed, err := atport.SetATPortBind(req.VID, req.PID, port)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		if !changed {
			httpx.WriteJSON(w, 0, map[string]any{"key": key, "port": port}, "没有变化")
			return
		}
		// 立刻重新发现一轮：1~2 秒内切到新口（切换交给发现流程做，别在模组读写协程上搞关闭）
		DiscoverAndStartAsync(mgr, true)
		if port == "" {
			httpx.WriteJSON(w, 0, map[string]any{"key": key, "port": ""}, "已解绑 "+key+"，恢复自动扫描")
			return
		}
		httpx.WriteJSON(w, 0, map[string]any{"key": key, "port": port},
			"已绑定 "+key+" → "+port+"（这台设备的命令口直接用它，不再自动扫描）")
	}
}
