package kernel

import (
	"fmt"
	"modemd/module/config"
	"modemd/module/dial"
	"modemd/module/httpx"
	"net/http"
	"strings"

	"go.uber.org/zap"
)

// ====================== 拨号上网 API ======================
//
//	GET  /api/dial[?modem_id=0][&force=1]  拨号状态 + USB 类型识别 + 可选类型表
//	POST /api/dial/connect                 {"modem_id":0,"cid":1,"apn":"3GNET","pdp_type":"IPV4V6"}
//	POST /api/dial/disconnect              {"modem_id":0,"cid":1}
//
// 页面进「拨号上网」先 GET 一次（状态 + 类型 + 表，一次拉全，不用发三次请求），
// 拨号 / 断开各自 POST。失败时**仍然把状态带回去**（data 非空），
// 页面据此显示"卡在哪一步"，而不是只弹一句错误。
// 引擎侧流程见 dial_engine.go，纯逻辑（取值表 / 状态机 / 网卡匹配）见 dial.go。

// apiDial GET /api/dial?modem_id=0[&force=1]
//
// force=1 表示重新识别 USB 类型（不走 30 秒缓存）—— 页面上的「重新识别」按钮用它。
func apiDial(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		force := r.URL.Query().Get("force")
		usb := mod.USBType(force == "1" || force == "true")
		// 接口类码翻成人话给页面直接用（原生类码放在 usb.descriptor.classes 里备查）
		var classNotes []string
		if usb.Descriptor != nil {
			classNotes = dial.ClassSummaryList(usb.Descriptor.Classes)
		}
		// 自动组网（OpenWrt：自动拨号 + 自动建 WAN 接口）最近一次的结果
		httpx.WriteJSON(w, 0, map[string]any{
			"modem_id":    mod.ID,
			"state":       mod.DialStatus(),
			"usb":         usb,
			"class_notes": classNotes,
			"types":       dial.DialTypes(),
			"auto_wan":    AutoWANPayload(mod.ID),
			// 「拨号上网」页面上那个 IPv6 开关（[net] wan_ipv6）：四种拨号方式都按它来
			"wan_ipv6": WANIPv6Enabled(),
			// 页面上那个**拨号总开关**（[net] dial_enabled，默认开）：关掉后四种方式都不拨
			"dial_enabled": config.DialEnabled(),
			// 「接口模式」（[net] wan_mode，2026-10-13 新增）：modem_pro / dhcp / both。
			// 见 netprov_mode.go —— 决定主接口用哪个 proto、要不要另建 modem<id>_6。
			"wan_mode":       config.WANModeNow().String(),
			"wan_mode_label": config.WANModeNow().Label(),
			"wan_modes":      config.WANModeListPayload(),
		}, "ok")
	}
}

// apiDialConnect POST /api/dial/connect
//
//	{"modem_id":0,"cid":1,"apn":"3GNET","pdp_type":"IPV4V6","reconnect":false,"force":false}
//
// apn 留空 = 沿用模组里已有的配置；reconnect = 已激活的先断开再连（让刚写入的 APN 生效）。
func apiDialConnect(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID   int    `json:"modem_id"`
			CID       int    `json:"cid"`
			APN       string `json:"apn"`
			PDPType   string `json:"pdp_type"`
			Reconnect bool   `json:"reconnect"`
			Force     bool   `json:"force"`
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
		st, err := mod.Dial(dial.DialRequest{
			CID:       req.CID,
			APN:       req.APN,
			PDPType:   req.PDPType,
			Reconnect: req.Reconnect,
			Force:     req.Force,
		})
		if err != nil {
			logger.Warn("dial failed", zap.Int("modem", req.ModemID), zap.Error(err))
			// 状态照旧带回去（data = st），页面显示"卡在哪一步"；错误信息走 msg
			httpx.WriteJSON(w, -1, st, err.Error())
			return
		}
		// 拨号会改链路 / 地址，顺手把「当前网络」卡片也刷一次
		pushNetworkAfterParamChange(mod)
		httpx.WriteJSON(w, 0, st, fmt.Sprintf("ok, 已拨号 cid=%d", st.CID))
	}
}

// apiDialDisconnect POST /api/dial/disconnect  {"modem_id":0,"cid":1}
//
// cid 省略（或 0）时用当前拨号的 cid，再不行用数据默认 cid。
func apiDialDisconnect(mgr *ModemManager) http.HandlerFunc {
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
		st, err := mod.Hangup(req.CID)
		if err != nil {
			logger.Warn("dial hangup failed", zap.Int("modem", req.ModemID), zap.Error(err))
			httpx.WriteJSON(w, -1, st, err.Error())
			return
		}
		pushNetworkAfterParamChange(mod)
		httpx.WriteJSON(w, 0, st, fmt.Sprintf("ok, 已断开 cid=%d", st.CID))
	}
}

// apiDialIPv6 POST /api/dial/ipv6  {"on":true}
//
// 「拨号上网」页面上那个 **IPv6 开关**：写回 [net] wan_ipv6（config.toml，重启不丢），
// 内存里同步生效。**四种拨号方式都按它来**：
//
//	QMI            不给拨号器加 -6（不再要 v6 承载）
//	ECM/NCM/RNDIS  不建 modem<id>_6（dhcpv6 兄弟接口），已经建过的会被删掉
//	MBIM           给 umbim 的 pdp_type 只写 ip
//
// 真正动手的是组网循环（每 15 秒一轮）：所以这里只改配置 + 回状态，页面提示"≤15 秒生效"。
func apiDialIPv6(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			// On 用指针：必须显式给值，免得"body 里没写"被当成 false 把开关关掉
			On *bool `json:"on"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		if req.On == nil {
			httpx.WriteJSON(w, -1, nil, `缺少 on（要 true 或 false）`)
			return
		}
		if err := config.SetWANIPv6(*req.On); err != nil {
			httpx.WriteJSON(w, -1, nil, "写回配置失败："+err.Error())
			return
		}
		word := "已开启"
		if !*req.On {
			word = "已关闭"
		}
		logger.Info("dial: wan ipv6 toggled", zap.Bool("on", *req.On))
		httpx.WriteJSON(w, 0, map[string]any{"wan_ipv6": WANIPv6Enabled()},
			word+"模组侧 IPv6（[net] wan_ipv6 已写回配置，组网循环 ≤15 秒生效）")
	}
}

// apiDialMode POST /api/dial/mode  {"mode":"modem_pro"}
//
// 「拨号上网」页面上的**接口模式**下拉（[net] wan_mode，2026-10-13 用户要求）：
//
//	modem_pro —— 只用一张接口（proto=modem_pro），IPv4+IPv6 都在上面。**默认**
//	dhcp      —— 传统两张：modem<id>(dhcp) + modem<id>_6(dhcpv6)
//	both      —— 两种都要：modem<id>(modem_pro) + modem<id>_6(dhcpv6)
//
// ★ 模式 1/3 下**不再只认 QMI**：qmi / ecm / ncm / rndis 一律 modem_pro
// （见 netprov_proto.go 的 wanProtoForKindMode）。MBIM 是唯一例外（拨号在 netifd 侧）。
//
// 写回配置后由组网循环（≤15 秒一轮）接手：会就地改主接口 proto、按需补/删
// modem<id>_6，防火墙那段（/etc/firewall.user 里的标记块）也跟着对账 ——
// 切模式不会留下上一个模式的残规则。
func apiDialMode(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Mode string `json:"mode"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		if strings.TrimSpace(req.Mode) == "" {
			httpx.WriteJSON(w, -1, nil, `缺少 mode（modem_pro / dhcp / both）`)
			return
		}
		mode := config.ParseWANMode(req.Mode)
		if err := config.SetWANMode(mode); err != nil {
			httpx.WriteJSON(w, -1, nil, "写回配置失败："+err.Error())
			return
		}
		logger.Info("dial: wan mode changed", zap.String("mode", mode.String()))
		httpx.WriteJSON(w, 0, map[string]any{
			"wan_mode":       mode.String(),
			"wan_mode_label": mode.Label(),
			"wan_modes":      config.WANModeListPayload(),
		}, "已切到「"+mode.Label()+"」（[net] wan_mode 已写回配置，组网循环 ≤15 秒生效）")
	}
}

// apiDialEnable POST /api/dial/enable  {"on":false}
//
// 「拨号上网」**最上面那个拨号总开关**：写回 [net] dial_enabled（config.toml，重启不丢），
// 内存里同步生效。关掉后**所有拨号方式都不拨号**：
//
//	自动组网循环   整台模组跳过（不激活 PDP、不拉主机侧拨号器、不建 WAN 接口）
//	主机侧拨号器   **立刻**停掉（QMI 下那是唯一还活着的数据面，不等下一轮循环）
//	手动拨号       直接拒（dial_engine.go 的 Dial 入口）
//
// 已经建过的 WAN 接口不动（只是拿不到地址），要拆就自己去网络设置里删。
func apiDialEnable(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			// On 用指针：必须显式给值，免得"body 里没写"被当成 false 把拨号关掉
			On *bool `json:"on"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		if req.On == nil {
			httpx.WriteJSON(w, -1, nil, `缺少 on（要 true 或 false）`)
			return
		}
		if err := config.SetDialEnabled(*req.On); err != nil {
			httpx.WriteJSON(w, -1, nil, "写回配置失败："+err.Error())
			return
		}
		logger.Info("dial: master switch toggled", zap.Bool("on", *req.On))
		msg := "已开启拨号（[net] dial_enabled 已写回配置；自动组网 ≤15 秒按新值走）"
		if !*req.On {
			msg = "已关闭拨号（[net] dial_enabled 已写回配置）：所有拨号方式都不再拨号，" +
				"主机侧拨号器已停掉；要恢复打开这个开关即可"
		}
		httpx.WriteJSON(w, 0, map[string]any{"dial_enabled": config.DialEnabled()}, msg)
	}
}
