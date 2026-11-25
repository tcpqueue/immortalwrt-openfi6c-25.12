package kernel

import (
	"modemd/module/config"
	"modemd/module/httpx"
	"modemd/module/netdev"
	"modemd/module/usbhotplug"
	"net/http"
	"strconv"
	"strings"
)

// ====================== PCIe（MHI / wwan）模组 API ======================
//
//	GET  /api/pcie         像 5G 模组的 PCIe 设备（含没绑驱动的）+ 能力/说明
//	POST /api/pcie/reset   软复位（remove + rescan）{"bdf":"0000:01:00.0"}
//	POST /api/pcie/bind    手动绑驱动 {"bdf":"...","driver":"mhi_pci_generic"}
//
// PCIe 模组没有"USB 插拔"这件事，所以设备清单与复位走单独一组接口；
// 它的 AT/拨号/自动组网都在同一套流程里（见 pcie.go / atdev.go / hostdial.go）。
func apiPCIeList(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		devs := netdev.ListPCIModemDevices()
		httpx.WriteJSON(w, 0, map[string]any{
			"supported": netdev.PCIeResetSupported(),
			"drivers":   netdev.PcieModemDrivers,
			"devices":   devs,
			"at_ports":  netdev.PcieATPortCandidates(),
			"qmi_ctrl":  netdev.PcieQMIControlCandidates(),
			"mbim_dev":  netdev.PcieMBIMCandidates(),
			"rescan":    netdev.PciRescanPath,
		}, "ok")
	}
}

// apiPCIeReset POST /api/pcie/reset {"bdf":"0000:01:00.0"}
//
// 与 USB 软重插对应的动作：PCIe 层 remove + rescan（不断模组的电）。
func apiPCIeReset(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpx.WriteJSON(w, -1, nil, "method not allowed, use POST")
			return
		}
		var req struct {
			BDF string `json:"bdf"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		if !netdev.PCIeResetSupported() {
			httpx.WriteJSON(w, -1, nil, "本平台不支持 PCIe 软复位")
			return
		}
		if err := netdev.ResetPCIDevice(req.BDF); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, map[string]any{"bdf": req.BDF}, "已重新枚举 PCIe 设备（驱动会自动重新绑定）")
	}
}

// apiPCIeBind POST /api/pcie/bind {"bdf":"0000:01:00.0","driver":"mhi_pci_generic"}
//
// 设备在但没绑驱动（内核模块后装 / 启动顺序不对）时的退路，对应 USB 那边的手动补驱动。
func apiPCIeBind(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpx.WriteJSON(w, -1, nil, "method not allowed, use POST")
			return
		}
		var req struct {
			BDF    string `json:"bdf"`
			Driver string `json:"driver"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		drv := strings.TrimSpace(req.Driver)
		if drv == "" {
			// 不填就按已知的模组驱动挨个试（哪个装了就绑哪个）
			var lastErr error
			for _, d := range netdev.PcieModemDrivers {
				if err := netdev.BindPCIDriver(req.BDF, d); err == nil {
					httpx.WriteJSON(w, 0, map[string]any{"bdf": req.BDF, "driver": d}, "驱动已绑定")
					return
				} else {
					lastErr = err
				}
			}
			httpx.WriteJSON(w, -1, nil, "没有可用的 PCIe 模组驱动："+orFallback(errText(lastErr), "未知错误"))
			return
		}
		if err := netdev.BindPCIDriver(req.BDF, drv); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, map[string]any{"bdf": req.BDF, "driver": drv}, "驱动已绑定")
	}
}

// errText 把 error 转成字符串（nil → 空串）
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// apiUSBClear POST /api/usb/clear 清空事件（不动设备）
func apiUSBClear(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpx.WriteJSON(w, -1, nil, "method not allowed, use POST")
			return
		}
		usbhotplug.ClearUSBEvents()
		pushUSB() // 其它开着的页面也跟着清空
		httpx.WriteJSON(w, 0, nil, "ok")
	}
}

// apiUSBBind POST /api/usb/bind {"vid":"0e8d","pid":"7126"}
//
// 给“插上了但没串口”的模组手动补一次驱动（自动补失败时的退路，或设备没被自动认出时手填）。
func apiUSBBind(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			VID string `json:"vid"`
			PID string `json:"pid"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		vid := strings.ToLower(strings.TrimSpace(req.VID))
		pid := strings.ToLower(strings.TrimSpace(req.PID))
		if len(vid) != 4 || len(pid) != 4 {
			httpx.WriteJSON(w, -1, nil, `vid / pid 要写成四位十六进制，例如 {"vid":"0e8d","pid":"7126"}`)
			return
		}
		if !usbhotplug.USBHotplugCanBind() {
			httpx.WriteJSON(w, -1, nil, usbhotplug.USBHotplugBindNote())
			return
		}
		note, err := usbhotplug.EnsureSerialTTY(usbhotplug.ModemUSBDevice{VID: vid, PID: pid})
		if err != nil {
			usbhotplug.NoteUSBEvent(usbhotplug.USBEvent{Kind: usbhotplug.USBEventFailed, Device: vid + ":" + pid,
				Note: "手动补驱动失败：" + err.Error()})
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		usbhotplug.NoteUSBEvent(usbhotplug.USBEvent{Kind: usbhotplug.USBEventBound, Device: vid + ":" + pid, Note: note + "（手动）"})
		pushUSB()
		httpx.WriteJSON(w, 0, map[string]any{"note": note}, "ok")
	}
}

// apiSendAT POST /api/at  {"modem_id":0,"cmd":"AT+QGDCNT?"}
//
// 调试用：直接向模组发一条 AT 指令并回显原始应答。
// 用途是核对厂商/固件差异（例如未验证型号该用哪条指令查信号、APN、流量计数器），
// 与调试台「原始接口」页的其他接口一样不做鉴权 —— 请只在可信网络里开放。
func apiSendAT(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID int    `json:"modem_id"`
			Cmd     string `json:"cmd"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		cmd := strings.TrimSpace(req.Cmd)
		if cmd == "" {
			httpx.WriteJSON(w, -1, nil, "cmd required")
			return
		}
		// 只允许单条指令：换行会拼出多条指令，容易误改模组状态
		if len(cmd) > 128 || strings.ContainsAny(cmd, "\r\n") {
			httpx.WriteJSON(w, -1, nil, "cmd must be a single line (<=128 chars)")
			return
		}
		mod, err := modemByID(mgr, req.ModemID)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		resp, err := mod.SendAT(cmd)
		if err != nil {
			httpx.WriteJSON(w, -1, map[string]any{"cmd": cmd, "resp": resp}, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, map[string]any{"cmd": cmd, "resp": resp}, "ok")
	}
}

// apiLogs GET /api/logs?lines=200  读 daemon 日志尾部（不占串口）
func apiLogs(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lines := 200
		if v := r.URL.Query().Get("lines"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				httpx.WriteJSON(w, -1, nil, "lines must be an integer")
				return
			}
			lines = n
		}
		rows, err := config.TailFile(config.Cfg.Server.LogFile, lines)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, map[string]any{
			"file":  config.Cfg.Server.LogFile,
			"lines": rows,
			"count": len(rows),
		}, "ok")
	}
}
