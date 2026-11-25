package kernel

import (
	"context"
	"fmt"
	"hash/fnv"
	"modemd/module/config"
	"modemd/module/luciauth"
	"modemd/module/sms"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// wechatHandleIncoming 处理一条收到的微信消息：只记录发送者（用于确定推送目标）
func wechatHandleIncoming(m ilinkMessage) {
	if !m.fromUser() || m.MessageState == 1 {
		return
	}
	from := strings.TrimSpace(m.FromUserID)
	if from == "" {
		return
	}
	// 原始报文记一条（含 context_token 是否存在）：主动发消息必须带它，
	// 排查 "prepare failed" 时就靠这行日志。
	logger.Info("wechat incoming message",
		zap.String("from", from),
		zap.Int("message_type", m.MessageType),
		zap.Int("state", m.MessageState),
		zap.Bool("has_context", m.ContextToken != ""),
		zap.String("text", truncateLog(m.effectiveText(), 60)),
		zap.String("raw", truncateLog(m.Raw, 600)))
	r := wechatInst
	r.mu.Lock()
	allowed := wechatSenderAllowedLocked(r.cfg.Allowed, from)
	changed := r.lastSender != from || (m.ContextToken != "" && r.lastContext != m.ContextToken)
	if allowed && changed {
		r.lastSender = from
		if m.ContextToken != "" {
			r.lastContext = m.ContextToken
		}
		r.saveMaybeLocked()
	}
	if allowed && (!r.heardUser || r.lastIncoming != from) {
		// “真的收到过消息”单独记：能不能主动发消息看的是它
		//（lastSender 在扫码登录时就被填成扫码用户了，不能当“会话已建立”）。
		r.heardUser = true
		r.lastIncoming = from
		r.lastIncomingAt = time.Now()
		r.saveMaybeLocked()
	}
	r.mu.Unlock()
	if !allowed {
		logger.Warn("wechat message ignored (sender not allowed)", zap.String("from", from))
		return
	}
	if changed {
		logger.Info("wechat sender learned (push target candidate)",
			zap.String("from", from), zap.String("text", truncateLog(m.effectiveText(), 60)))
		pushWeChat()
	}
}

// wechatSenderAllowedLocked 发送者白名单（留空 = 不限制；按子串匹配，同 C# 版口径）
func wechatSenderAllowedLocked(cfgAllowed, from string) bool {
	list := strings.TrimSpace(cfgAllowed)
	if list == "" {
		return true
	}
	for _, part := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if part != "" && strings.Contains(strings.ToLower(from), strings.ToLower(part)) {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------ 发送 ---

// wechatEnqueue 把一条待发文本放进队列（队列满则丢弃，绝不阻塞调用方）
func wechatEnqueue(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	r := wechatInst
	select {
	case r.queue <- text:
	default:
		logger.Warn("wechat send queue full, message dropped")
	}
}

// wechatSendLoop 串行发送（微信接口不宜并发），限速 + 重试
func wechatSendLoop(ctx context.Context) {
	last := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case text := <-wechatInst.queue:
			if gap := wechatSendGap - time.Since(last); gap > 0 {
				wechatSleepCtx(ctx, gap)
			}
			_, _ = wechatSendText(ctx, text, false)
			last = time.Now()
		}
	}
}

// wechatSendText 发一条文本（带重试）。target 为空时用配置的目标 → 最近发消息的人。
func wechatSendText(ctx context.Context, text string, isTest bool) (string, error) {
	r := wechatInst
	r.mu.Lock()
	cfg := r.cfg
	cl := r.client
	target := cfg.ToUserID
	if target == "" {
		target = r.lastSender
	}
	// 会话令牌：没有它服务端会回 ret=-2 prepare failed（需要先在微信里给机器人发过消息）
	context := r.lastContext
	heard := r.heardUser
	enabled := cfg.Enabled
	r.mu.Unlock()

	rec := &WeChatSendRecord{At: time.Now(), Text: truncateLog(text, 80), Test: isTest}
	if cl == nil || !cl.authenticated() {
		rec.Msg = "未登录（请先扫码登录）"
		wechatRecordSend(rec)
		return "", fmt.Errorf("%s", rec.Msg)
	}
	if target == "" {
		rec.Msg = "还没有推送目标：先在微信里给机器人发一条消息，或手填推送目标"
		wechatRecordSend(rec)
		return "", fmt.Errorf("%s", rec.Msg)
	}
	if !isTest && !enabled {
		// 用测试消息做联调时不受开关限制
		return "", fmt.Errorf("微信推送未启用")
	}

	var err error
	for i := 0; i < wechatSendRetry; i++ {
		if i > 0 {
			wechatSleepCtx(ctx, wechatSendBackoff)
		}
		if err = cl.sendText(ctx, target, text, context, ""); err == nil {
			rec.OK = true
			rec.Msg = "已发送"
			wechatRecordSend(rec)
			logger.Info("wechat message sent", zap.String("to", target), zap.Int("len", len(text)))
			return target, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	rec.Msg = err.Error()
	if strings.Contains(rec.Msg, "prepare failed") {
		// 服务端的原话不好懂：直接告诉用户该怎么做。
		// 收到过消息还报这个，多半是会话过期了（iLink 侧的会话有窗口期）。
		if heard {
			rec.Msg = "服务端说会话不可用（prepare failed）：会话可能已过期，再给机器人发一条消息即可继续"
		} else {
			rec.Msg = "还没有会话（服务端返回 prepare failed）：先在微信里给机器人发一条消息（例如「在」），再试一次"
		}
	}
	wechatRecordSend(rec)
	logger.Warn("wechat send failed", zap.String("to", target), zap.Error(err),
		zap.Bool("heard_user", heard), zap.Bool("has_context", context != ""))
	return target, err
}

func wechatRecordSend(rec *WeChatSendRecord) {
	r := wechatInst
	r.mu.Lock()
	if rec.OK {
		r.sentOK++
	} else {
		r.sentFail++
	}
	r.lastSend = rec
	r.mu.Unlock()
	pushWeChat()
}

// ------------------------------------------------------------ 新短信推送 ---

// wechatSMSKey 短信指纹：号码|时间|分条|内容哈希（与存储位置无关）
func wechatSMSKey(s sms.SMS) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s.Content))
	part := ""
	if s.PartTotal > 1 {
		part = fmt.Sprintf("%d/%d", s.PartSeq, s.PartTotal)
	}
	return fmt.Sprintf("%s|%s|%s|%08x", s.Number, s.Timestamp, part, h.Sum32())
}

// freshSMSLocked 挑出"没推送过"的短信（首次只建基线，不推历史短信）
func (r *wechatRuntime) freshSMSLocked(list []sms.SMS) []sms.SMS {
	first := !r.ready
	fresh := make([]sms.SMS, 0, len(list))
	for _, s := range list {
		k := wechatSMSKey(s)
		if r.seenSet[k] {
			continue
		}
		r.seenSet[k] = true
		r.seen = append(r.seen, k)
		if !first {
			fresh = append(fresh, s)
		}
	}
	// 指纹表裁剪（只留最近 wechatSeenMax 条）
	if len(r.seen) > wechatSeenMax {
		drop := r.seen[:len(r.seen)-wechatSeenMax]
		for _, k := range drop {
			delete(r.seenSet, k)
		}
		r.seen = append([]string(nil), r.seen[len(r.seen)-wechatSeenMax:]...)
	}
	if first {
		r.ready = true
	}
	if len(fresh) > 0 || first {
		r.saveMaybeLocked()
	}
	// 列表是"最新在前"（后端排序），推送按时间正序更自然
	for i, j := 0, len(fresh)-1; i < j; i, j = i+1, j-1 {
		fresh[i], fresh[j] = fresh[j], fresh[i]
	}
	return fresh
}

// OnSMS 收到短信列表后的钩子（由 kernel 的 pushSMS 调用）。
//
// ⚠️ 不依赖"有没有客户端开着页面"：短信转发是后台能力（和定时器一样）。
func OnSMS(mod ModemInfo, list []sms.SMS) {
	if len(list) == 0 {
		return
	}
	r := wechatInst
	r.mu.Lock()
	enabled := r.cfg.Enabled && r.client != nil && r.cfg.BotToken != ""
	// 无论开没开都推进指纹基线（freshSMSLocked 里首次只建基线不返回新短信），
	// 否则用户一打开开关就会把卡里的历史短信全推一遍。
	fresh := r.freshSMSLocked(list)
	tpl := r.cfg.Template
	r.mu.Unlock()

	if !enabled || len(fresh) == 0 {
		return
	}
	for _, s := range fresh {
		wechatEnqueue(renderSMS(mod, s, tpl))
	}
}

// RenderSMS 渲染一条短信（导出给单测与接口层用）
func RenderSMS(mod ModemInfo, s sms.SMS, tpl string) string {
	return renderSMS(mod, s, tpl)
}

// renderSMS 按模板渲染一条短信
//
// 占位符：{modem} {number} {time} {content} {parts} {imei}
func renderSMS(mod ModemInfo, s sms.SMS, tpl string) string {
	if strings.TrimSpace(tpl) == "" {
		tpl = config.DefaultWeChatTemplate
	}
	label := fmt.Sprintf("#%d %s", mod.ID, mod.ATPort)
	if mod.ModelName != "" {
		label += " " + mod.ModelName
	}
	parts := ""
	if s.PartTotal > 1 {
		parts = fmt.Sprintf("（%d/%d）", s.PartSeq, s.PartTotal)
	}
	return strings.NewReplacer(
		"{modem}", label,
		"{number}", s.Number,
		"{time}", s.Timestamp,
		"{content}", s.Content,
		"{parts}", parts,
		"{imei}", mod.IMEI,
	).Replace(tpl)
}

// SendWeChatTest 发一条测试消息（界面上的「发送测试」）
func SendWeChatTest() error {
	r := wechatInst
	r.mu.Lock()
	stage := r.stage
	target := r.cfg.ToUserID
	if target == "" {
		target = r.lastSender
	}
	label := target
	if label == "" {
		label = "(未确定)"
	}
	r.mu.Unlock()
	if stage != wechatStageReady {
		return fmt.Errorf("还没登录，请先扫码登录")
	}
	text := fmt.Sprintf("✅ modemd 微信机器人已连通\n时间：%s\n推送目标：%s\n（这条是测试消息，收到即说明链路正常）",
		time.Now().Format("2006-01-02 15:04:05"), label)
	_, err := wechatSendText(context.Background(), text, true)
	return err
}

// ---- from: smsauth/smsauth.go ----

// Package smsauth 是「短信访问密码」：解锁令牌、密码校验、失败节流与相关接口。
//
// 为什么做在后端而不是"前端加个岔口"：接口本来就是公开的 HTTP，
// 前端拦一下只能防"随手点开"，curl 一下就绕过去了。所以短信相关的
// 接口全部要令牌，短信的 WebSocket 推送也只发给已解锁的连接。
//
// ⚠️ 这是"给局域网内的调试台加一道门"，不是强认证 —— 走的是明文 HTTP，
// 令牌与密码都在同一条链路上，别当成公网可用的鉴权。
//
// 本包不认识 Modem：唯一的引擎依赖是一个注入点（见文件末尾的 WriteJSON），
// kernel 在 init 里接上（glue.go）。
//
// 为什么做在后端而不是"前端加个岔口"：接口本来就是公开的 HTTP，
// 前端拦一下只能防"随手点开"，curl 一下就绕过去了。所以短信相关的
// 接口全部要令牌，短信的 WebSocket 推送也只发给已解锁的连接。
//
// 机制：
//
//	POST /api/sms/unlock {"password":"…"} → 换一个随机 token（内存态、滑动 30 分钟过期）
//	之后的请求带 X-SMS-Token（或 ?sms_token= 查询参数，WS 握手只能走这个）
//	POST /api/sms/lock  → 主动丢掉当前 token（页面上的「锁定」按钮）
//	GET  /api/sms/auth  → 只回"要不要密码 / 现在解锁了没"，给前端决定显示锁屏还是列表
//
// 密码对比用 crypto/subtle 常量时间比较（避免按字符耗时侧信道）；
// 同一来源 IP 连续错 5 次锁 60 秒，防止在线穷举。
//
// 配置（config.toml）：
//
//	[ui]
//	sms_password_required = true       # 关掉就完全不校验
//	sms_password          = "password" # 默认 password
//
// ⚠️ 定位说明：这是"给局域网内的调试台加一道门"，不是强认证 ——
// 走的是明文 HTTP，令牌与密码都在同一条链路上，别当成公网可用的鉴权。

const (
	// smsDefaultPasswd 默认密码（配置里没写就是它，用户要求如此）
	smsDefaultPasswd = "password"
	// smsTokenTTL 令牌滑动过期时间：每次使用续期，闲置超过它就失效
	smsTokenTTL = 30 * time.Minute
	// smsTokenMax 最多同时保留的会话数（防止令牌表被刷爆）
	smsTokenMax = 32
	// smsFailMax 同一 IP 连续失败多少次后锁定
	smsFailMax = 5
	// smsFailLockFor 锁定时长
	smsFailLockFor = 60 * time.Second
)

var (
	smsAuthMu    sync.Mutex
	smsTokens    = map[string]time.Time{} // token → 最后使用时间
	smsFailCount = map[string]int{}       // IP → 连续失败次数
	smsFailUntil = map[string]time.Time{} // IP → 锁到什么时候
)

// smsPasswordText 当前生效的密码（空配置回落到默认值）
func smsPasswordText() string {
	if p := config.Cfg.UI.SMSPassword; p != "" {
		return p
	}
	return smsDefaultPasswd
}

// smsPasswordRequired 是否需要密码。
// 三个条件任意一个成立就不需要：
//
//	· 显式 sms_password_required = false；
//	· 显式把 sms_password 写成空串（"我知道自己在干什么"）；
//	· **外面已经开了登录闸门**（[ui] require_luci_login，给 LuCI/路由器账号）——
//	  那时能调到这些接口的人本来就已经登录过了（含 WebSocket 握手也是要会话的），
//	  再要一道短信密码纯属重复（用户 2026-10-06 明确要求去掉）。
func smsPasswordRequired() bool {
	if !config.Cfg.UI.SMSPasswordRequired {
		return false
	}
	if strings.TrimSpace(config.Cfg.UI.SMSPassword) == "" && config.Cfg.UI.SMSPasswordSet {
		return false // 显式配了空密码 = 不用密码
	}
	if luciauth.LuciAuthEnabled() {
		return false // 外层登录已经在管了（见上面注释）
	}
	return true
}

// smsPasswordIsDefault 是否还在用出厂默认密码（启动时提示用户改掉）
func smsPasswordIsDefault() bool {
	return smsPasswordRequired() && smsPasswordText() == smsDefaultPasswd
}
