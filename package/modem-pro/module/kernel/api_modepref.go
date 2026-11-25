package kernel

import (
	"modemd/module/httpx"
	"net/http"

	"go.uber.org/zap"
)

// ====================== 组网模式偏好 API ======================
//
//	GET  /api/mode?modem_id=0            当前取值 + 本机可选值 + 指令/说明
//	POST /api/mode/set                   {"modem_id":0,"value":"LTE"}
//
// 语义值统一为 AUTO/WCDMA/LTE/NR5G/WCDMA:LTE/WCDMA:NR5G/LTE:NR5G（与 QModem 的分档一致）；
// 没取到码表的厂商（如华为 SYSCFGEX）会返回 freeform=true，界面改成"填设备原码"。

// apiModePref GET /api/mode?modem_id=0[&refresh=1]
func apiModePref(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		// refresh=1：重新向模组枚举一次可选值（`AT+XXX=?`），不是只重读当前值
		if r.URL.Query().Get("refresh") == "1" {
			mod.ResetModeEnum()
		}
		caps, err := mod.QueryModePrefCaps()
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, caps, "ok")
	}
}

// apiSetModePref POST /api/mode/set {"modem_id":0,"value":"LTE"}
func apiSetModePref(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID int    `json:"modem_id"`
			Value   string `json:"value"`
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
		caps, err := mod.SetModePrefCaps(req.Value)
		if err != nil {
			logger.Warn("set mode pref failed", zap.Int("modem", req.ModemID), zap.Error(err))
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, caps, "已下发并回读")
	}
}
