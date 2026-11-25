package kernel

import (
	"modemd/module/sms"
	"net/http"
	"time"
)

// ====================== 屏幕端（tftdisplay）专用推送通道 ======================
//
// 设备上那块 320x172 的小屏（tftdisplay）要显示的信息全部来自这里，
// 不再自己读 /proc、netstat、AT —— 屏幕端只管收推送。
//
// 与 /ws（网页端）的区别：
//
//	① 和网页端一起受外层 LuCI 会话保护。屏幕还需要共享钥匙（DisplayWSKey）；
//	② **能收短信正文**（2026-10-07 用户要求：屏幕上要看短信、长短信要合并且左右划逐条读）：
//	   AllowSMS 恒 true。⚠️ 代价：**拿到这把钥匙的人就能读到短信正文** ——
//	   关闭外层 LuCI 会话保护时，必须把屏幕端点限制在可信网络；
//	③ 连上先推一份快照（modems + 每模组的 signal/network/traffic/smscount + 已缓存的短信列表），
//	   免得屏幕"连上了但一片空白"。
//
// 屏幕端实现：tftdisplay 的 src/utils/wsclient.{c,h} —— 端口从
// /etc/modemd/config.toml 的 [server] listen 读（跟本进程监听的是同一个键），
// 然后连 /display/ws?key=…。
//
// ⚠️ 改钥匙必须两边一起改：
//
//	· 服务端 module/kernel/displayws.go 的 DisplayWSKey
//	· 屏幕端 tftdisplay/src/utils/wsclient.h 的 WS_SHARED_KEY
const (
	// DisplayWSPath 屏幕端通道的路径。
	DisplayWSPath = "/display/ws"
	// DisplayWSKey 双方定死的通讯钥匙（不是密码：不带身份、不可枚举、只为"别让随便一个
	// 网页/扫描器连上这个免密通道"）。
	DisplayWSKey = "h29k-tft-9f3c1a7b"
)

// apiDisplayWS GET /display/ws?key=<DisplayWSKey>
//
// 钥匙不对 → HTTP 401：屏幕端日志里会写"钥匙不对（HTTP 401）"，
// 一眼能分出是钥匙配错还是端口/路径不对（404）或没人监听（连不上）。
func apiDisplayWS(mgr *ModemManager, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != DisplayWSKey {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("display key mismatch\n"))
			return
		}
		hub.Serve(w, r, Options{
			// 屏幕要看短信正文（长短信已由 GroupSMSConcat 合并成一条）⇒ 恒 true。
			// ⚠️ 拿到钥匙就能读短信（见文件头的说明）。
			AllowSMS: func(string) bool { return true },
			// ⭐ 被动连接：屏幕 7x24 挂着，**不因为它就发 AT**。
			// 它拿到的就是现有缓存（连上那一刻的快照 + 之后别人/URC 引发的广播）；
			// 想让没人开网页时也保持刷新，用 change_interval 后台采样（config.toml）。
			Passive: true,
			// ⭐ 屏幕连上就：① 先推一份现有缓存（毫秒级，屏幕立刻有东西）
			// ② 再**立刻读一遍短信**把缓存换成卡里最新的（屏幕/网页都只读这份缓存，
			//    而短信平时只在开机 / 换卡 / +CMTI 时读 ⇒ 屏幕刚起来时缓存可能是空的）。
			OnConnect: func() {
				pushDisplaySnapshot(mgr)
				go refreshSMSCacheAll(mgr)
				// 网络缓存也要补一次：屏幕通道是 Passive，没人开网页时 pushRound 早退 ⇒
				// 运营商名/制式/频段会一直是空的（Mobile 页显示 "--------"、
				// 短信「余额查询」屏认不出卡类型）。与短信一样带 15s 去抖。
				go refreshNetworkForDisplay(mgr)
			},
			OnMessage: func(payload []byte) { handleWSCommand(mgr, payload) },
		})
	}
}

// refreshSMSCacheAll 把所有模组的短信缓存读一遍（全量替换）。
//
// 由屏幕连上来时触发（见 OnConnect）：屏幕/网页都只读缓存，而短信平时只在
// 开机、换卡、+CMTI 新短信这三种时机读 —— 屏幕刚起来时缓存可能是空的/旧的，
// 连上就读一遍最省心（**没有周期轮询**：用户明说"定时器那个读取太慢，可以去掉"）。
//
// ⚠️ 带 15 秒去抖：屏幕断线重连很频繁，每次连上都读既不必要，又容易和
// 开机读 / URC 读撞在一起（见 core.go 的 smsMu）。
func refreshSMSCacheAll(mgr *ModemManager) {
	for _, mod := range mgr.listModems() {
		mod.refreshSMSCache(0, 15*time.Second)
	}
}

// refreshNetworkForDisplay 屏幕连上时补查一次网络状态（以及时拿到运营商名/制式/频段）。
//
// ⚠️ 背景：屏幕这条通道是 **Passive**（不算“有人在看”），而周期推送 pushRound 在
// `CountActive() == 0` 时直接早退 ⇒ 没人开网页时谁也不会去查网络，缓存永远是冷的，
// 屏幕端的 `op_name`/`cops` 就是空的（2026-10-07 实机：Mobile 页运营商显示 "--------"、
// 短信「余额查询」屏认不出卡类型，只能一直显示「未识别」）。
//
// ⚠️ 带 15 秒去抖（与 refreshSMSCacheAll 同一个理由）：屏幕断线重连很频繁，
// 每次连上都真发一条 AT 没必要。屏幕端在看「余额查询」屏时也会用 `{"action":"net_get"}`
// 再主动要一次（那个入口没有去抖，但屏幕自己限了 30s 一次）。
func refreshNetworkForDisplay(mgr *ModemManager) {
	for _, mod := range mgr.listModems() {
		if mod.networkStatusSnapshot() != nil {
			continue /* 缓存还新鲜（30s TTL 内）⇒ 一条 AT 都不发，推现成的 */
		}
		pushNetwork(mod)
	}
}

// pushDisplaySnapshot 屏幕刚连上时推一份现状。
//
// 复用网页端那份快照（模组列表 + 每模组的信号 / 网络 / 速率缓存，**都不额外发 AT**），
// 再补：短信条数（角标）+ **已缓存的短信列表**（屏幕的短信页要看正文）。
// ⭐ 这份快照就是“从缓存取数”的实现：不查串口、不打扰别人。
//
// ⚠️ 短信列表读的是 `mod.SMSList`（ReadSMS 时缓存的），**这份快照本身不发 AT** ——
// 但 OnConnect 紧接着会让 refreshSMSCacheAll 读一遍（屏幕刚起来时缓存可能是空的/旧的），
// 读完那次 ReadSMS 会把最新列表再广播一次（微信转发按指纹去重，不会重复推）。
func pushDisplaySnapshot(mgr *ModemManager) {
	pushSnapshot(mgr)
	for _, mod := range mgr.listModems() {
		pushSMSCount(mod)
		// 组网模式偏好（屏幕 Mobile 页第 3 屏的 4G / 5G / 4G+5G 按钮）。
		// ⚠️ 丢 goroutine：它可能真要发一条 AT（TTL 过期时），
		// 别拖慢"屏幕一连上就该立刻有东西"的那份快照（展锐慢总线上尤其明显）。
		go pushDisplayMode(mod)
		mod.mu.Lock()
		list := append([]sms.SMS(nil), mod.SMSList...)
		mod.mu.Unlock()
		if len(list) == 0 || wsHubInst == nil {
			continue
		}
		id := mod.ID
		// 直接 BroadcastSMS（**不**走 pushSMS）：缓存的旧短信不该再触发一次微信转发
		wsHubInst.BroadcastSMS(WSMessage{
			Type:    "sms",
			ModemID: &id,
			At:      time.Now().UnixMilli(),
			Data:    list,
		})
	}
}
