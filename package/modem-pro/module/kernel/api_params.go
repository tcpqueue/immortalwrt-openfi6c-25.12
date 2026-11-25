package kernel

import (
	"fmt"
	"modemd/module/config"
	"modemd/module/httpx"
	"net/http"

	"go.uber.org/zap"
)

// ====================== 可写参数（APN / 选网） ======================

// apiParams GET /api/params?modem_id=0
//
// 一次返回全部可写参数的当前状态：PDP 上下文（含激活状态）、当前在用 APN、选网方式。
func apiParams(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		st, err := mod.paramsStatusOrQuery(r.URL.Query().Get("refresh") == "1")
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, st, "ok")
	}
}

// apiSetAPN POST /api/params/apn  {"modem_id":0,"cid":1,"pdp_type":"IPV4V6","apn":"3GNET"}
//
// 写 AT+CGDCONT 并回读确认。**写完会自动把该 cid 的 PDP 重新激活**（AT+CGACT，先断后连）——
// 光写配置不重新激活等于没生效，以前要用户再点一次「立即重新激活」；
// 顺带还会激活模组的 IMS 上下文（`[dial] activate_ims`，默认开，失败不影响本次写入）。
// 调用方若只想写配置、不要动链路，传 "activate": false。
//
// ⚠️ QMI（主机侧拨号）例外：数据承载归厂商拨号器独占，这里**只写配置、绝不激活**
// （连显式传 activate=true 也不激活）——模组侧一激活，拨号器请求数据呼叫就被拒
// （实测 QMUXError 0xe），IPv6 那条 WDS 会话尤其起不来。见 modemUsesHostDialer。
func apiSetAPN(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID int    `json:"modem_id"`
			CID     int    `json:"cid"`
			PDPType string `json:"pdp_type"`
			APN     string `json:"apn"`
			// Activate 不传 = true（写完自动激活）。显式 false 时只写配置。
			Activate *bool `json:"activate"`
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
		st, err := mod.SetAPN(req.CID, req.PDPType, req.APN)
		if err != nil {
			logger.Warn("set apn failed", zap.Int("modem", req.ModemID), zap.Error(err))
			// 回读不一致时 st 仍然有效（已刷新），一并返回方便前端重绘
			if st != nil {
				pushNetworkAfterParamChange(mod)
				httpx.WriteJSON(w, 0, st, "写入后回读不一致："+err.Error())
				return
			}
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		// 写完自动激活（不传 activate 就是 true）：先断后连，让刚写入的 APN 真正生效；
		// 再顺带把 IMS 上下文激活起来。两步的失败都只写进 msg（不影响"APN 已写入"这个事实）。
		autoNote := ""
		doActivate := req.Activate == nil || *req.Activate
		// 拨号总开关关掉时**不自动激活**：这里"写 APN 顺带激活 PDP"本质上也是一次拨号，
		// 总开关的含义是"所有模式都不拨号"（用户可能只是先把配置写好、等以后再开）。
		if doActivate && !config.DialEnabled() {
			doActivate = false
			autoNote = "；拨号总开关已关闭：只写配置、不激活 PDP（在「拨号上网」页打开开关后才会拨）"
		}
		// QMI / MBIM：数据承载归主机侧拨号器独占，模组侧**不能**激活 PDP（见 modemUsesHostDialer）——
		// 一激活拨号器请求数据呼叫就被拒（QMI 实测 QMUXError 0xe；MBIM 实测
		// MBIM_CID_CONNECT 返回 Status=2 FAILURE、ActivationState 一直是 Deactivated），
		// IPv6 那条会话尤其起不来。这里只写配置，激活交给「拨号上网」时的拨号器。
		if doActivate && modemUsesHostDialer(mod) {
			doActivate = false
			autoNote = fmt.Sprintf("；这是 %s（主机侧拨号）模式：只写配置、不激活 PDP —— 数据承载留给主机侧 %s，模组侧一激活它就被顶掉（IPv6 那路会起不来）",
				hostDialModeLabelOf(mod), hostDialNameForModem(mod))
		}
		if doActivate {
			if st2, aerr := mod.SetPDPActive(req.CID, true, true); aerr != nil {
				logger.Warn("auto activate after APN write failed",
					zap.Int("modem", req.ModemID), zap.Int("cid", req.CID), zap.Error(aerr))
				if st2 != nil {
					st = st2
				}
				autoNote = fmt.Sprintf("；自动激活 cid=%d 失败：%s", req.CID, aerr.Error())
			} else {
				st = st2
				autoNote = fmt.Sprintf("；已重新激活 cid=%d（按新 APN 重建链路）", req.CID)
			}
			if ActivateIMSEnabled() && st != nil {
				res := mod.ActivateIMS(st.Contexts)
				autoNote += "；IMS：" + res.Note
			}
		} else if autoNote == "" {
			autoNote = "；按请求只写配置，未激活（要生效请重新激活 PDP 或重启模组）"
		}
		pushNetworkAfterParamChange(mod)
		httpx.WriteJSON(w, 0, st, fmt.Sprintf("ok, 已写入 cid=%d 的 APN%s", req.CID, autoNote))
	}
}

// apiDeleteAPN POST /api/params/apn/delete  {"modem_id":0,"cid":3}
//
// 删除一个 PDP 上下文的 APN 配置（AT+CGDCONT=<cid>）并回读确认。
// 已激活的 cid 会被拒（响应里说明要先停用），不会顺手把链路断掉。
func apiDeleteAPN(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID int `json:"modem_id"`
			CID     int `json:"cid"`
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
		st, err := mod.DeleteAPN(req.CID)
		if err != nil {
			logger.Warn("delete apn failed", zap.Int("modem", req.ModemID),
				zap.Int("cid", req.CID), zap.Error(err))
			// 回读不一致时 st 仍然有效（已刷新），一并返回方便前端重绘
			if st != nil {
				pushNetworkAfterParamChange(mod)
				httpx.WriteJSON(w, 0, st, "删除后回读不一致："+err.Error())
				return
			}
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		pushNetworkAfterParamChange(mod)
		httpx.WriteJSON(w, 0, st, fmt.Sprintf("ok, 已删除 cid=%d 的 APN 配置", req.CID))
	}
}

// apiSetPDPActive POST /api/params/pdp
//
//	{"modem_id":0,"cid":1,"active":true}              激活 / 停用该上下文（AT+CGACT）
//	{"modem_id":0,"cid":1,"reconnect":true}           先断后连，让刚写入的 APN 立即生效
//
// 激活是异步的：后端会回读 AT+CGACT? 确认（最多约 4 秒），确认不到就如实报错，
// 但响应里仍带最新上下文列表（st 非 nil），方便前端重绘。
//
// ⚠️ QMI（主机侧拨号）只能**停用**：数据承载归厂商拨号器独占，模组侧激活会被拒
// （见 modemUsesHostDialer）。想上网用「拨号上网」，那里拨号器会自己建承载。
func apiSetPDPActive(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID   int   `json:"modem_id"`
			CID       int   `json:"cid"`
			Active    *bool `json:"active"`    // 不传按 true 处理
			Reconnect bool  `json:"reconnect"` // true 时忽略 active，按"重新激活"走
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
		active := true
		if req.Active != nil {
			active = *req.Active
		}
		// QMI：数据承载归主机侧拨号器独占 —— 模组侧激活会把承载占住，拨号器随后被拒、
		// IPv6 起不来（见 modemUsesHostDialer）。这里只允许**停用**（放掉承载）。
		if (active || req.Reconnect) && modemUsesHostDialer(mod) {
			httpx.WriteJSON(w, -1, nil, fmt.Sprintf(
				"这是 %s（主机侧拨号）模式：数据呼叫由主机侧 %s 激活，模组侧不要手动激活（会占住唯一数据承载，拨号器随后被拒、IPv6 起不来）；要上网请用「拨号上网」",
				hostDialModeLabelOf(mod), hostDialNameForModem(mod)))
			return
		}
		st, err := mod.SetPDPActive(req.CID, active, req.Reconnect)
		if err != nil {
			logger.Warn("set pdp active failed", zap.Int("modem", req.ModemID),
				zap.Int("cid", req.CID), zap.Bool("reconnect", req.Reconnect), zap.Error(err))
			// 回读不一致时 st 仍然有效（已刷新），一并返回方便前端重绘
			if st != nil {
				pushNetworkAfterParamChange(mod)
				httpx.WriteJSON(w, 0, st, "回读不一致："+err.Error())
				return
			}
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		pushNetworkAfterParamChange(mod)
		msg := fmt.Sprintf("ok, 已%s cid=%d", ActiveWord(active || req.Reconnect), req.CID)
		if req.Reconnect {
			msg = fmt.Sprintf("ok, 已重新激活 cid=%d（按当前 APN 重建链路）", req.CID)
		}
		httpx.WriteJSON(w, 0, st, msg)
	}
}

// apiSetNetSelect POST /api/params/netselect  {"modem_id":0,"mode":"manual","plmn":"46011"}
//
// mode 为 auto / manual。手动选网会触发一次重新选网（可能短暂断网）且不会自动回退。
func apiSetNetSelect(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID int    `json:"modem_id"`
			Mode    string `json:"mode"`
			PLMN    string `json:"plmn"`
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
		st, err := mod.SetNetSelect(req.Mode, req.PLMN)
		if err != nil {
			logger.Warn("set net select failed", zap.Int("modem", req.ModemID), zap.Error(err))
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		// 手动选网会重新选网（PLMN / 注册状态会变），立刻推一次，别让别的页面
		// 和「当前网络」卡片干等到下一轮周期推送
		pushNetworkAfterParamChange(mod)
		httpx.WriteJSON(w, 0, st, "ok")
	}
}

// ---- from: schedule_api.go ----

//
//	GET  /api/schedule          定时列表 + 可选动作 + 目标模组下拉（页面一次拉全）
//	POST /api/schedule/set      新增 / 更新一条（body 就是 Task；id 为空则新建）
//	POST /api/schedule/delete   {"id":"..."}
//	POST /api/schedule/toggle   {"id":"...","enabled":true}
//	POST /api/schedule/run      {"id":"..."}  立即执行一次（不推进槽位记录）
// apiSchedule GET /api/schedule[?modem_id=0]
