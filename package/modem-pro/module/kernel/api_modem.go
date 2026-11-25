package kernel

import (
	"modemd/module/httpx"
	"modemd/module/netdev"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

func apiListModems(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if v := r.URL.Query().Get("discover"); v == "1" || v == "true" {
			// 后台触发一轮强制重探并**立即返回**：探测要逐个串口发 AT（每口可达数秒），
			// 以前在这里同步跑 —— 请求干等十几秒，整轮发现还持着 mgr.mu 把其它接口一起堵住。
			// 现在读列表走无锁快照，重探结果完成后由 WS 推 modems 通知页面。
			DiscoverAndStartAsync(mgr, true)
		}
		list := mgr.listModems()
		// 标出"命令口是用户手动指定的"（界面下栏显示"手动 / 自动"；只用于展示）
		for _, m := range list {
			if m == nil {
				continue
			}
			m.ATPortManual = savedATPort(m) != ""
		}
		httpx.WriteJSON(w, 0, list, "ok")
	}
}

// apiSetATPort POST /api/at/port  {"modem_id":0,"port":"/dev/ttyUSB2"}
//
// 手动指定这台模组的 AT 命令口（`port` 传空串 = 恢复自动探测）。
// 为什么要手动：自动探测只要求"对 AT 回个 OK"，而**回了 OK 不等于它就是命令口** ——
// 实测 MBIM 模式下程序挑中 /dev/ttyUSB1（回 OK 但发指令全超时），真正能用的是 ttyUSB2，
// 用户看到的现象就是"控制不了"。
//
// 选择会写进 atport.json（键 = 模组 IMEI；**未识别模组退到"串口组"键**，见 ATPortSavedKey），
// 重启后仍生效；写入后立刻触发一轮发现，1~2 秒内切换到新口
// （切换动作交给发现流程做，避免在模组读写协程上搞关闭/重启）。
func apiSetATPort(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID int    `json:"modem_id"`
			Port    string `json:"port"`
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
		key := ATPortSavedKey(mod)
		if key == "" {
			// 连串口组都没有（理论上不该发生）⇒ 只能靠端口名反查着清记录，保证能回到自动探测
			if strings.TrimSpace(req.Port) == "" && atPortStoreInst.ClearByPort(mod.ATPort) > 0 {
				DiscoverAndStartAsync(mgr, true)
				httpx.WriteJSON(w, 0, map[string]any{"modem_id": mod.ID, "port": ""}, "已清掉这台模组的手动指定，恢复自动探测")
				return
			}
			httpx.WriteJSON(w, -1, nil, "这台模组还没有串口信息，等探测完再选")
			return
		}
		port := strings.TrimSpace(req.Port)
		verified := true
		if port != "" {
			// 允许指定本模组候选之外的口（分组不一定准），但必须是系统里真实存在的串口
			known := false
			for _, p := range mod.TTYList {
				if p == port {
					known = true
					break
				}
			}
			if !known && !portExists(port) {
				httpx.WriteJSON(w, -1, nil, "串口 "+port+" 不在本机串口列表里")
				return
			}
			// ⚠️ 保存前先验一下它真能答 AT（不是当前正在用的口才验，免得把自己占用中的口判死）：
			// 存一个不应答的口 = 这台模组下次重启后就认不出来了（连下拉都没了）。
			//
			// ⚠️ 例外：**未识别的模组**（IEMI/型号都读不出来 —— 通常正是"AT 口挑错了"造成的）
			// 一律放行：那时的现状本来就是坏的，硬拒 = 用户永远修不回来（他比我们清楚哪个口能用）；
			// 存下来让发现流程按它试，不行再换一个（见 discoverModems 里保留记录的那一支）。
			if port != mod.ATPort {
				if ok, _ := netdev.ProbeATTimed(port); !ok {
					if ATPortKey(mod) != "" {
						httpx.WriteJSON(w, -1, nil, "串口 "+port+" 不应答 AT，不能当命令口用（换成能答的那个，或选自动探测）")
						return
					}
					verified = false
				}
			}
		}
		changed := atPortStoreInst.Set(key, port)
		// 身份读出来了就以身份键为准：顺手清掉"未识别时期"存的串口组记录（免得两份记录打架）
		if fk := ATPortFallbackKey(mod.TTYList); fk != "" && fk != key {
			atPortStoreInst.Set(fk, "")
		}
		DiscoverAndStartAsync(mgr, true) // 立刻切（发现流程里完成），不等下一次巡检
		msg := "已切到 " + port + "（已记住，重启后仍用它）"
		if port == "" {
			msg = "已恢复自动探测（发现流程会重新挑一个命令口）"
		} else if !verified {
			msg = "已记住 " + port + "（这个口现在没答上 AT —— 发现流程会按它试；不行再换一个口）"
		}
		httpx.WriteJSON(w, 0, map[string]any{
			"modem_id": mod.ID, "port": port, "changed": changed, "key": key,
		}, msg)
	}
}

// portExists 本机串口列表里有没有这个名字（ScanSerialPorts 是 sysfs 扫描，很快）
func portExists(port string) bool {
	port = strings.TrimSpace(port)
	if port == "" {
		return false
	}
	list, err := netdev.ScanSerialPorts()
	if err != nil {
		return false
	}
	for _, p := range list {
		if p == port {
			return true
		}
	}
	return false
}

// apiResetModem POST /api/modem/reset  {"modem_id":0}
//
// 重启模组（AT+CFUN=1,1），用于让 usbnet / 频段类配置生效。
// 模组会重新注网、USB 可能重新枚举，AT 口短时间会断。
func apiResetModem(mgr *ModemManager) http.HandlerFunc {
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
		if err := mod.ResetModule(); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, nil, "ok, 模组正在重启")
	}
}

// apiModemStatus GET /api/modem/status?modem_id=0
//
// SIM 卡与模组自身状态：ICCID / IMSI（含推出运营商）/ SIM 状态 / 本机号码 /
// 温度传感器（含最高温与机身·5G·CPU 三个关键点）/ 模组时钟（含与主机 UTC 的偏差）。
// 每项独立容错，固件不支持的那项留空。
func apiModemStatus(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		mod.statusQueryMu.Lock()
		defer mod.statusQueryMu.Unlock()
		if r.URL.Query().Get("refresh") != "1" {
			mod.mu.Lock()
			st := cloneModemStatus(mod.ModemStatus)
			mod.mu.Unlock()
			if st != nil && !st.QueriedAt.IsZero() && time.Since(st.QueriedAt) <= modemStatusCacheTTL {
				httpx.WriteJSON(w, 0, st, "ok (cache)")
				return
			}
		}
		st := mod.QueryModemStatus()
		mod.mu.Lock()
		mod.ModemStatus = cloneModemStatus(st)
		mod.mu.Unlock()
		httpx.WriteJSON(w, 0, st, "ok")
	}
}

// apiSetModemClock POST /api/modem/clock  {"modem_id":0[,"unix":1696523400]}
//
// 把模组时钟校到指定时刻（不传 unix = 主机当前 UTC），写完**回读确认**再返回刷新过的状态。
//
// 为什么要有这个动作：本机 RTC 出厂未校时（中兴 F50 实测长期回 `13/01/01,00:00:00+00`），
// 而**短信时间戳用的就是模组时钟** —— 不校时，收件箱里每条短信的时间都是错的。
func apiSetModemClock(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID int   `json:"modem_id"`
			Unix    int64 `json:"unix"` // 0 = 主机当前时刻
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
		var t time.Time
		if req.Unix > 0 {
			t = time.Unix(req.Unix, 0)
		}
		raw, err := mod.SetModuleClock(t)
		if err != nil {
			// 把模组回读到的原文一起给出去：界面要能显示"它其实是什么"，不能只说一句失败
			httpx.WriteJSON(w, -1, map[string]string{"module_time": raw}, err.Error())
			return
		}
		mod.statusQueryMu.Lock()
		st := mod.QueryModemStatus()
		mod.statusQueryMu.Unlock()
		mod.mu.Lock()
		mod.ModemStatus = cloneModemStatus(st)
		mod.mu.Unlock()
		httpx.WriteJSON(w, 0, st, "已校时并回读："+raw)
	}
}

// apiResetTraffic POST /api/traffic/reset  {"modem_id":0}
//
// 清零累计流量并重新开始计数（AT+QGDCNT=0 → 使能）。返回清零后的新采样。
func apiResetTraffic(mgr *ModemManager) http.HandlerFunc {
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
		rate, err := mod.ResetTraffic()
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		pushTraffic(mod, rate)
		httpx.WriteJSON(w, 0, rate, "ok, 累计已清零")
	}
}

// apiUSSD GET  /api/ussd?modem_id=0   读最近一次 USSD 应答
//
//	POST /api/ussd              {"modem_id":0,"code":"*100#"}  下发 USSD 码
//
// ⚠️ 应答是异步 URC：POST 只代表"已下发"，真正的回复通过 WebSocket 的 ussd 消息推送
// （或稍后 GET 一次）。
func apiUSSD(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			mod, err := modemFromQuery(mgr, r)
			if err != nil {
				httpx.WriteJSON(w, -1, nil, err.Error())
				return
			}
			httpx.WriteJSON(w, 0, map[string]any{"modem_id": mod.ID, "result": mod.USSDLast()}, "ok")
			return
		}
		var req struct {
			ModemID int    `json:"modem_id"`
			Code    string `json:"code"`
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
		if err := mod.SendUSSD(req.Code); err != nil {
			logger.Warn("send ussd failed", zap.Int("modem", req.ModemID), zap.Error(err))
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, nil, "已下发，等待模组应答（异步推送）")
	}
}

// apiModemInfo GET /api/modem/info?modem_id=0
// 查询模组硬件信息（IMEI / 固件版本 / 型号 / 厂商），并回写缓存供 /api/modems 展示
func apiModemInfo(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		info, err := mod.QueryIdentity()
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		// 身份信息已回写到模组对象，推一份新列表给其他客户端
		pushModems(mgr)
		httpx.WriteJSON(w, 0, info, "ok")
	}
}

// ---- from: api_device.go ----

// apiAirplane GET /api/airplane?modem_id=0
// 读飞行模式状态（AT+CFUN?）：cfun 1 = 全功能，4 = 飞行模式。
// 另带 supported/reason：机型本来就不给开飞行模式的（F50 这类整机只有一路上行的设备，
// 见 Modem.AirplaneBlockedReason）界面靠它把开关藏掉。
func apiAirplane(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		if why := mod.AirplaneBlockedReason(); why != "" {
			// 不给开，但状态照读：万一射频真被别处关了（老版本 / 定时任务留下的），
			// 界面还得把开关露出来让用户点「关」把它恢复。读不回来就当不是飞行模式。
			fun, _ := mod.QueryFuncMode()
			httpx.WriteJSON(w, 0, airplaneState(mod, fun), "ok")
			return
		}
		fun, ok := mod.QueryFuncMode()
		if !ok {
			httpx.WriteJSON(w, -1, nil, "模组未响应 AT+CFUN?（该固件可能不支持）")
			return
		}
		httpx.WriteJSON(w, 0, airplaneState(mod, fun), "ok")
	}
}

// apiSetAirplane POST /api/airplane  {"modem_id":0,"enabled":true}
// 开/关飞行模式（AT+CFUN=4 / AT+CFUN=1）。开了之后模组不注网、不测信号，
// 数据业务断开，直到关掉为止。
func apiSetAirplane(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID int  `json:"modem_id"`
			Enabled bool `json:"enabled"`
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
		fun, err := mod.SetAirplane(req.Enabled)
		if err != nil {
			// 失败留痕：这类“下了指令但没生效 / 机型根本不给开”的毛病光看界面看不出来，
			// 日志里带上模组、目标状态、报错和回读到的 CFUN 才能事后对账。
			logger.Warn("set airplane failed",
				zap.Int("modem", mod.ID), zap.Bool("enabled", req.Enabled),
				zap.Int("cfun", fun), zap.Error(err))
			// 失败也把当前状态带回去，前端好回滚开关
			httpx.WriteJSON(w, -1, airplaneState(mod, fun), err.Error())
			return
		}
		// 飞行模式一开，注册/信号/服务小区全都变了，立刻广播一次（见 helper 注释）
		pushNetworkAfterParamChange(mod)
		logger.Info("set airplane ok",
			zap.Int("modem", mod.ID), zap.Bool("enabled", req.Enabled), zap.Int("cfun", fun))
		httpx.WriteJSON(w, 0, airplaneState(mod, fun), "ok")
	}
}

// airplaneState 统一的飞行模式应答体。
// supported=false + reason 表示“这台机型不给开”（界面把开关藏掉），见 AirplaneBlockedReason。
func airplaneState(mod *Modem, fun int) map[string]any {
	st := map[string]any{
		"modem_id":  mod.ID,
		"cfun":      fun,
		"enabled":   fun == CFUNAirplane,
		"supported": true,
	}
	if why := mod.AirplaneBlockedReason(); why != "" {
		st["supported"] = false
		st["reason"] = why
	}
	return st
}

// apiUSBnet GET /api/usbnet?modem_id=0
//
// 查询模块对外输出上网的协议（USB 网卡模式，AT+QCFG="usbnet"）：
// 0=QMI/NDIS 1=ECM 2=MBIM 3=RNDIS 5=NCM。
func apiUSBnet(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		st, err := mod.usbNetStatusOrQuery(r.URL.Query().Get("refresh") == "1")
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, st, "ok")
	}
}

// apiSetUSBnet POST /api/usbnet/set  {"modem_id":0,"value":"2"}
func apiSetUSBnet(mgr *ModemManager) http.HandlerFunc {
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
		st, err := mod.SetUSBnet(strings.TrimSpace(req.Value))
		if err != nil {
			logger.Warn("set usbnet failed", zap.Int("modem", req.ModemID), zap.Error(err))
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		if st.AutoReboot {
			httpx.WriteJSON(w, 0, st, "模组已接受设置，等待自动重启并重新识别")
			return
		}
		httpx.WriteJSON(w, 0, st, "ok, 需重启模组生效")
	}
}
