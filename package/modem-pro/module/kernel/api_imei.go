package kernel

import (
	"modemd/module/httpx"
	"modemd/module/version"
	"net/http"

	"go.uber.org/zap"
)

// ====================== IMEI 管理 API ======================
//
//	GET  /api/imei?modem_id=0           当前 IMEI + 本机是否支持改写 + 本机指令与说明
//	POST /api/imei/set                  {"modem_id":0,"imei":"86…"} → 写完回读确认
//
// 引擎侧见 imei.go；**改写指令每家不同**（方言表在 vendor_settings_imei.go）。
// ⚠️ 改 IMEI 在多数国家/地区受法律限制，界面必须带提示。

// apiVersion GET /api/version —— 版本 + 运行时（页脚显示，排查“功能怎么没有”的第一步）
func apiVersion() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, 0, version.CurrentVersion(), "ok")
	}
}

// apiIMEI GET /api/imei?modem_id=0
func apiIMEI(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, mod.IMEICapsOf(), "ok")
	}
}

// apiSetIMEI POST /api/imei/set {"modem_id":0,"imei":"…"}
func apiSetIMEI(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID int    `json:"modem_id"`
			IMEI    string `json:"imei"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		mod, err := modemByID(mgr, req.ModemID)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		got, err := mod.SetIMEI(req.IMEI)
		if err != nil {
			logger.Warn("set imei failed", zap.Int("modem", req.ModemID), zap.Error(err))
			// 失败也把回读到的值带回去（可能还是旧值）
			httpx.WriteJSON(w, -1, map[string]any{"imei": got}, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, map[string]any{"imei": got}, "IMEI 已写入并回读确认")
	}
}

// apiRestoreIMEI POST /api/imei/restore {"modem_id":0}
//
// 把 IMEI 写回"第一次改写前自动保存的原始值"（见 imei_backup.go）；
// 没有备份就明确报错，不会猜一个值写下去。
func apiRestoreIMEI(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID int `json:"modem_id"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		mod, err := modemByID(mgr, req.ModemID)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		got, err := mod.RestoreIMEI()
		if err != nil {
			logger.Warn("restore imei failed", zap.Int("modem", req.ModemID), zap.Error(err))
			httpx.WriteJSON(w, -1, map[string]any{"imei": got}, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, map[string]any{"imei": got}, "IMEI 已恢复为原始值并回读确认")
	}
}
