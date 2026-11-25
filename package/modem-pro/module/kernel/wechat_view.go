package kernel

import (
	"modemd/module/config"
	"strings"
	"time"
)

// WeChatView 微信机器人面板数据（⚠️ 不回传 bot_token，只给掩码）
type WeChatView struct {
	Enabled  bool   `json:"enabled"`
	Stage    string `json:"stage"`
	StageMsg string `json:"stage_msg,omitempty"`
	StageCN  string `json:"stage_cn,omitempty"`
	QRPNG    string `json:"qr_png,omitempty"`
	QRAge    int    `json:"qr_age,omitempty"`  // 二维码已生成多少秒
	QRLeft   int    `json:"qr_left,omitempty"` // 还剩多少秒有效
	Verify   bool   `json:"verify_required"`   // 需要配对码

	BaseURL   string `json:"base_url"`
	BotID     string `json:"bot_id,omitempty"`
	BotUserID string `json:"bot_user_id,omitempty"`
	TokenMask string `json:"token_mask,omitempty"`
	HasToken  bool   `json:"has_token"`

	ToUserID   string `json:"to_user_id,omitempty"`
	LastSender string `json:"last_sender,omitempty"`
	// SessionReady 能不能主动发消息：**真的收到过用户消息**（或拿到了会话令牌）才算。
	// ⚠️ 实测：没跟机器人说过话时主动发消息会被服务端拒（`ret=-2 prepare failed`），
	// 用户发过一条之后就正常了。
	// ⚠️ 不能用 last_sender 判断：它在扫码登录时就被填成扫码用户了。
	SessionReady bool   `json:"session_ready"`
	HeardMessage bool   `json:"heard_message"`
	HasContext   bool   `json:"has_context"`
	ListenOn     bool   `json:"listen_on"`
	LastIncoming string `json:"last_incoming,omitempty"`
	// LastIncomingAt 最近一次收到用户消息的时间（没有就不发这个字段）
	LastIncomingAt *time.Time `json:"last_incoming_at,omitempty"`
	Template       string     `json:"template"`
	Listen         bool       `json:"listen"`
	AllowedSent    string     `json:"allowed_senders,omitempty"`

	LastSend *WeChatSendRecord `json:"last_send,omitempty"`
	SentOK   int               `json:"sent_ok"`
	SentFail int               `json:"sent_fail"`
	Seen     int               `json:"seen"`

	DefaultTemplate string `json:"default_template"`
	DefaultBaseURL  string `json:"default_base_url"`
	Queued          int    `json:"queued"`
}

// wechatView 组装面板数据
func wechatView() WeChatView {
	r := wechatInst
	r.mu.Lock()
	defer r.mu.Unlock()
	v := WeChatView{
		Enabled:         r.cfg.Enabled,
		Stage:           r.stage,
		StageMsg:        r.stageMsg,
		StageCN:         wechatStageText(r.stage),
		BaseURL:         r.cfg.BaseURL,
		BotID:           r.cfg.BotID,
		BotUserID:       r.cfg.BotUserID,
		TokenMask:       maskToken(r.cfg.BotToken),
		HasToken:        r.cfg.BotToken != "",
		ToUserID:        r.cfg.ToUserID,
		LastSender:      r.lastSender,
		SessionReady:    r.heardUser || r.lastContext != "",
		HeardMessage:    r.heardUser,
		HasContext:      r.lastContext != "",
		ListenOn:        r.listenOn,
		LastIncoming:    r.lastIncoming,
		Template:        r.cfg.Template,
		Listen:          r.cfg.Listen,
		AllowedSent:     r.cfg.Allowed,
		LastSend:        r.lastSend,
		SentOK:          r.sentOK,
		SentFail:        r.sentFail,
		Seen:            len(r.seen),
		DefaultTemplate: config.DefaultWeChatTemplate,
		DefaultBaseURL:  config.DefaultWeChatBaseURL,
		Queued:          len(r.queue),
	}
	if !r.lastIncomingAt.IsZero() {
		at := r.lastIncomingAt
		v.LastIncomingAt = &at
	}
	if r.qrPNG != "" && r.stage != wechatStageReady {
		v.QRPNG = r.qrPNG
		v.QRAge = int(time.Since(r.qrAt).Seconds())
		left := int((wechatQRTTL - time.Since(r.qrAt)).Seconds())
		if left < 0 {
			left = 0
		}
		v.QRLeft = left
	}
	v.Verify = r.stage == wechatStageVerify
	return v
}

func wechatStageText(stage string) string {
	switch stage {
	case wechatStageIdle:
		return "未登录"
	case wechatStageWait:
		return "等待扫码"
	case wechatStageScan:
		return "已扫码，待手机确认"
	case wechatStageVerify:
		return "需要配对码"
	case wechatStageReady:
		return "已登录"
	case wechatStageExpired:
		return "二维码已失效"
	case wechatStageFailed:
		return "登录失败"
	}
	return stage
}

// maskToken 只露出首尾几位，避免面板泄露完整凭据
func maskToken(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return ""
	}
	if len(t) <= 10 {
		return strings.Repeat("*", len(t))
	}
	return t[:6] + "…" + t[len(t)-4:]
}

// pushWeChat 广播面板状态（前端「短信 → 微信机器人」卡片）
func pushWeChat() {
	notifyChange()
}

// ------------------------------------------------------------ 配置 ---
