package kernel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ====================== 信号采样历史（引擎侧的胶水） ======================
//
// 存储本身在 history.go（纯存储、只认 modemID + 数值）；这里负责两件
// 与模组强相关的事：① 判定"哪些值算有效"（要 SignalInfo 的量纲与哨兵值规则）；
// ② 把一次信号查询结果转成采样点喂给存储。

// validSignal 取出**有效**的信号量（指针为 nil 表示该次未上报）。
//
// 判定规则与前端 renderSignal 的校验完全一致，也与波动记录共用：
//   - 超出物理范围（实测见过 SINR=-3276）一律视为未上报；
//   - RSRQ=0、RSSI=0/99 是模组的"未上报"哨兵值，同样视为无效；
//   - RSSI 由 CSQ 的 0..31 换算成 dBm（×2-113），否则纵轴没法与其它值并列。
func validSignal(s SignalInfo) (rsrp, rsrq, sinr, rssi *int) {
	take := func(v, lo, hi int) *int {
		if !IntInRange(v, lo, hi) {
			return nil
		}
		x := v
		return &x
	}
	rsrp = take(s.NRRSRP, RSRPLo, RSRPHi)
	if s.NRRSRQ != 0 {
		rsrq = take(s.NRRSRQ, RSRQLo, RSRQHi)
	}
	sinr = take(s.NRSINR, SINRLo, SINRHi)
	// 0 / 99 是 3GPP 的“未上报”哨兵；255 是展锐 URC 里的“测不出来”（实测 `+CSQ: 255,99`）——
	// 后者不排掉会被当成 0..31 的索引换算出 +397 dBm 这种值。
	if s.RSSI != 0 && s.RSSI != 99 && s.RSSI != 255 {
		x := s.RSSI*2 - 113
		rssi = &x
	}
	return rsrp, rsrq, sinr, rssi
}

// sampleFromSignalInfo 把一次信号查询结果转成采样点
func sampleFromSignalInfo(s SignalInfo) Sample {
	rsrp, rsrq, sinr, rssi := validSignal(s)
	return Sample{At: time.Now(), RSRP: rsrp, RSRQ: rsrq, SINR: sinr, RSSI: rssi}
}

// ---- from: glue.go ----

// ---- from: glue.go ----
// ---- from: ws_glue.go ----
//
// 协议与广播中心在 push.go（纯技术层：握手 / 帧 / hub，不认识 Modem）；
// 这里只把三件事接上去：
//
//	① 短信推送的密码闸门（配了密码就要令牌有效，见 smsauth.go）；
//	② 连上先推一份快照（免得前端“连上了但一片空白”）；
//	③ 前端发来的指令（目前只有“再来一份快照”）。
// apiWS GET /ws：升级为 WebSocket 并保持连接，用于服务端主动推送。
//
// 协议与广播中心在 push.go（纯技术层：握手 / 帧 / hub，不认识 Modem）；
// 这里只把三件事接上去：
//
//	① 短信推送的密码闸门（配了密码就要令牌有效，见 smsauth.go）；
//	② 连上先推一份快照（免得前端“连上了但一片空白”）；
//	③ 前端发来的指令（目前只有“再来一份快照”）。
// apiWS GET /ws：升级为 WebSocket 并保持连接，用于服务端主动推送。
// ---- from: ws_glue.go ----
//
// 协议与广播中心在 push.go（纯技术层：握手 / 帧 / hub，不认识 Modem）；
// 这里只把三件事接上去：
//
//	① 短信推送的密码闸门（配了密码就要令牌有效，见 smsauth.go）；
//	② 连上先推一份快照（免得前端“连上了但一片空白”）；
//	③ 前端发来的指令（目前只有“再来一份快照”）。
// apiWS GET /ws：升级为 WebSocket 并保持连接，用于服务端主动推送。
//
// 协议与广播中心在 push.go（纯技术层：握手 / 帧 / hub，不认识 Modem）；
// 这里只把三件事接上去：
//
//	① 短信推送的密码闸门（配了密码就要令牌有效，见 smsauth.go）；
//	② 连上先推一份快照（免得前端“连上了但一片空白”）；
//	③ 前端发来的指令（目前只有“再来一份快照”）。
// apiWS GET /ws：升级为 WebSocket 并保持连接，用于服务端主动推送。

func apiWS(mgr *ModemManager, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hub.Serve(w, r, Options{
			// 没配密码 → 谁都能收短信推送；配了 → 令牌要有效
			AllowSMS: func(tok string) bool {
				return !smsPasswordRequired() || smsTokenValid(tok)
			},
			OnConnect: func() { pushSnapshot(mgr) },
			OnMessage: func(payload []byte) { handleWSCommand(mgr, payload) },
		})
	}
}

// wsCommand 客户端 → 服务端的指令（快照 / 标记短信已读 / 切组网模式 / 发短信）
type wsCommand struct {
	Action  string `json:"action"`
	ModemID int    `json:"modem_id"`
	Index   string `json:"index"` // sms_read：短信的**存储位置**（SMS.Index，字符串）
	// Value：mode_set 的取值（语义档位 LTE / NR5G / LTE:NR5G，屏幕上是 4G / 5G / 4G+5G）
	Value string `json:"value"`
	// Number / Text：sms_send 的目标号码与正文（屏幕端「余额查询」页用）
	Number string `json:"number"`
	Text   string `json:"text"`
}

// handleWSCommand 处理前端发来的指令。
// 前端不需要用 WS 触发读取：手动刷新仍走 HTTP 接口，这些接口完成后会主动广播，
// 于是多个页面能同时更新。这里只保留轻量指令：
//
//	{"action":"snapshot"}                              再来一份快照
//	{"action":"sms_read","modem_id":0,"index":"3"}     这条看过了 ⇒ 标已读
//	{"action":"sms_delete","modem_id":0,"index":"3"}   删掉这条（屏幕确认删除后发）
//	{"action":"mode_get","modem_id":0}                  读一次组网模式偏好（带 TTL 缓存）
//	{"action":"mode_set","modem_id":0,"value":"LTE"}  切组网模式（4G / 5G / 4G+5G）
//	{"action":"sms_send","modem_id":0,"number":"10086","text":"CXHF"}  发一条短信（屏幕「余额查询」页）
//	{"action":"net_get","modem_id":0}                    读一次网络状态（屏幕要运营商/频段）
//
// ⚠️ 屏幕的钥匙（DisplayWSKey）能读也能删短信 —— 这是用户要的功能（TFT 上长按删除），
// 拿到钥匙的人本来就跑在设备上/同一网段，口径与 AllowSMS 一致，见 displayws.go 文件头。
func handleWSCommand(mgr *ModemManager, payload []byte) {
	var cmd wsCommand
	if err := json.Unmarshal(payload, &cmd); err != nil {
		logger.Debug("websocket bad command", zap.Error(err))
		return
	}
	switch cmd.Action {
	case "snapshot":
		pushSnapshot(mgr)
	case "sms_read":
		// 屏幕的短信详情页停留够了就发这条（TFT 触摸屏那边，见 tftdisplay/pages/smspage.c）。
		// index 用存储位置而不是列表下标：列表是按时间倒序排的。
		idx, err := strconv.Atoi(strings.TrimSpace(cmd.Index))
		if err != nil || idx < 0 {
			logger.Debug("websocket sms_read bad index", zap.String("index", cmd.Index))
			return
		}
		mod, ok := mgr.modemByID(cmd.ModemID)
		if !ok {
			return
		}
		if err := mod.MarkSMSRead(idx); err != nil {
			logger.Warn("mark sms read failed",
				zap.Int("modem", mod.ID), zap.Int("index", idx), zap.Error(err))
		}
	case "sms_delete":
		// 屏幕双击 → 弹"删除/取消" → 点删除发这条。删完必须重读一遍：
		// 列表/条数/未读数（角标）都要跟着变，屏幕也是从这份缓存拿数据的。
		idx, err := strconv.Atoi(strings.TrimSpace(cmd.Index))
		if err != nil || idx < 0 {
			logger.Debug("websocket sms_delete bad index", zap.String("index", cmd.Index))
			return
		}
		mod, ok := mgr.modemByID(cmd.ModemID)
		if !ok {
			return
		}
		if err := mod.DeleteSMS(idx); err != nil {
			logger.Warn("websocket sms_delete failed",
				zap.Int("modem", mod.ID), zap.Int("index", idx), zap.Error(err))
			return
		}
		go mod.refreshSMSCache(0, 0) /* 删完立刻重读：列表/条数/角标一起更新 */
	case "mode_get":
		// 屏幕的「组网模式」页打开时来问一次。⚠️ 走 ModePrefForDisplay 的 TTL 缓存：
		// 缓存新鲜 ⇒ 一条 AT 都不发（只是把现成的那份再推一次）。
		mod, ok := mgr.modemByID(cmd.ModemID)
		if !ok {
			return
		}
		go pushDisplayMode(mod)
	case "net_get":
		// 屏幕要网络状态（运营商名/制式/频段）时来问一次 —— Mobile/NET 页与短信「余额查询」屏要用。
		// ⚠️ 为什么屏幕得自己开口：屏幕通道是 **Passive**（不算“有人在看”），
		//   没人开网页时 pushRound 直接早退 ⇒ 网络缓存是冷的，运营商名会一直是空的
		//   （2026-10-07 实机踩到：屏幕 Mobile 页显示 "--------"、余额查询页认不出卡类型）。
		// ⚠️ 走 goroutine：缓存过期时这一条真要发 AT。
		mod, ok := mgr.modemByID(cmd.ModemID)
		if !ok {
			return
		}
		go pushNetwork(mod)
	case "mode_set":
		// 屏幕点 4G / 5G / 4G+5G ⇒ 切组网模式。
		// ⚠️ 写模式要"下发 + 回读确认"（模组异步生效，见 SetModePrefCaps），可能占串口几秒，
		// 所以丢到 goroutine 里做，别卡住 WS 读循环；完成后无论成败都推一份给屏幕。
		mod, ok := mgr.modemByID(cmd.ModemID)
		if !ok {
			return
		}
		value := strings.TrimSpace(cmd.Value)
		if value == "" {
			return
		}
		go func() {
			if err := mod.SetModePrefForDisplay(value); err != nil {
				logger.Warn("websocket mode_set failed",
					zap.Int("modem", mod.ID), zap.String("value", value), zap.Error(err))
				mod.noteDisplayModeMsg("设置失败：" + err.Error())
			}
			pushDisplayMode(mod)
		}()
	case "sms_send":
		// 屏幕的「余额查询」页：按卡类型发一条查询短信（号码/正文由屏幕那边按运营商选好）。
		// ⚠️ 发短信要占串口好几秒（等提示符 + 等提交确认，见 SendSMS 的注释）
		//    ⇒ 必须丢 goroutine，不能卡住 WS 读循环。
		mod, ok := mgr.modemByID(cmd.ModemID)
		if !ok {
			return
		}
		number := strings.TrimSpace(cmd.Number)
		text := cmd.Text
		if !validSMSPhone(number) {
			pushDisplaySMSResult(mod, number, text, fmt.Errorf("号码不合法（只能是数字，可带开头的 +）"))
			return
		}
		if !validSMSContent(text) {
			pushDisplaySMSResult(mod, number, text, fmt.Errorf("内容不合法（不能为空/含控制字符）"))
			return
		}
		go func() {
			err := mod.SendSMS(number, text)
			if err != nil {
				logger.Warn("websocket sms_send failed",
					zap.Int("modem", mod.ID), zap.String("number", number), zap.Error(err))
			} else {
				logger.Info("websocket sms_send ok",
					zap.Int("modem", mod.ID), zap.String("number", number))
			}
			pushDisplaySMSResult(mod, number, text, err)
		}()
	}
}

// ---- from: smsauth_glue.go ----

//
// 令牌、密码校验、失败节流与 4 个接口都在 wechat.go；
// 这里只做两件引擎才有的事：
//
//	① 把统一 JSON 响应 WriteJSON 注入给它（它不该知道 kernel 的响应格式）；
//	② 保留 smsGate：闸门要往外写 code=401 的响应，那属于引擎的响应约定。
