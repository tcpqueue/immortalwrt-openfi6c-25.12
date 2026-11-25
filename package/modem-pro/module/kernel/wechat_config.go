package kernel

import (
	"modemd/module/config"
	"strings"
)

// ApplyWeChatConfig 保存配置（写回 toml），并按需重建客户端 / 启停监听。
//
// 只传要改的字段（指针为 nil 表示不动），便于界面上一项一项改。
type WeChatConfigPatch struct {
	Enabled   *bool
	BaseURL   *string
	BotToken  *string
	BotID     *string
	BotUserID *string
	ToUserID  *string
	Template  *string
	Listen    *bool
	Allowed   *string
}

// ApplyWeChatConfig 应用一份配置补丁
func ApplyWeChatConfig(p WeChatConfigPatch) error {
	r := wechatInst
	r.mu.Lock()
	cfg := r.cfg
	r.mu.Unlock()

	if p.Enabled != nil {
		cfg.Enabled = *p.Enabled
	}
	if p.BaseURL != nil {
		cfg.BaseURL = strings.TrimSpace(*p.BaseURL)
	}
	if p.BotToken != nil {
		cfg.BotToken = strings.TrimSpace(*p.BotToken)
	}
	// bot_id / bot_user_id：扫码时由服务端下发；也能从外部填回来（从备份恢复凭据、脚本化导入）。
	// ⚠️ 它们必须一起有值：启动时只有 token + bot_id 都在才会重建客户端（见 InitWeChatRuntime）。
	if p.BotID != nil {
		cfg.BotID = strings.TrimSpace(*p.BotID)
	}
	if p.BotUserID != nil {
		cfg.BotUserID = strings.TrimSpace(*p.BotUserID)
	}
	if p.ToUserID != nil {
		cfg.ToUserID = strings.TrimSpace(*p.ToUserID)
	}
	if p.Template != nil {
		cfg.Template = strings.TrimSpace(*p.Template)
	}
	if p.Listen != nil {
		cfg.Listen = *p.Listen
	}
	if p.Allowed != nil {
		cfg.Allowed = strings.TrimSpace(*p.Allowed)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = config.DefaultWeChatBaseURL
	}
	if cfg.Template == "" {
		cfg.Template = config.DefaultWeChatTemplate
	}

	// 写回配置文件（凭据由用户填时也一样走这条路，写进去才能跨重启）
	err := config.SetWeChat(config.WeChatSettings{
		Enabled:   cfg.Enabled,
		BaseURL:   cfg.BaseURL,
		BotToken:  cfg.BotToken,
		BotID:     cfg.BotID,
		BotUserID: cfg.BotUserID,
		ToUserID:  cfg.ToUserID,
		Template:  cfg.Template,
		Listen:    cfg.Listen,
		Allowed:   cfg.Allowed,
	})
	if err != nil {
		return err
	}

	r.mu.Lock()
	old := r.cfg
	r.cfg = cfg
	// 凭据有变化就重建客户端（扫码登录成功后由 wechatSetAccount 调）。
	// ⚠️ bot_id 也要一起看：启动时要求 token + bot_id 都在才建客户端（InitWeChatRuntime），
	// 所以"只补上 bot_id"也必须立刻生效，否则界面会一直停在未登录（要从备份恢复凭据时就会遇到）。
	if cfg.BotToken != old.BotToken || cfg.BotID != old.BotID {
		switch {
		case cfg.BotToken == "":
			r.client = nil
			r.stage = wechatStageIdle
			r.stageMsg = ""
		case cfg.BotID == "":
			// 有 token 但没有 bot_id：协议用不了，按未登录处理
			r.client = nil
			r.stage = wechatStageIdle
			r.stageMsg = "凭据不完整（缺 bot_id）：重新扫码登录，或把 bot_id 一起填回来"
		default:
			r.client = newILinkClient(cfg.BaseURL, cfg.BotToken)
			if r.stage != wechatStageReady {
				r.stage = wechatStageReady
			}
		}
	}
	r.mu.Unlock()

	wechatSyncListen()
	pushWeChat()
	return nil
}

// wechatSetAccount 扫码登录成功后保存凭据（写回 toml）
func wechatSetAccount(baseURL, token, botID, userID string) error {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = config.DefaultWeChatBaseURL
	}
	r := wechatInst
	r.mu.Lock()
	cfg := r.cfg
	r.mu.Unlock()
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	cfg.BotToken = strings.TrimSpace(token)
	cfg.BotID = strings.TrimSpace(botID)
	cfg.BotUserID = strings.TrimSpace(userID)
	cfg.Listen = true

	if err := config.SetWeChat(config.WeChatSettings{
		Enabled:   cfg.Enabled,
		BaseURL:   cfg.BaseURL,
		BotToken:  cfg.BotToken,
		BotID:     cfg.BotID,
		BotUserID: cfg.BotUserID,
		ToUserID:  cfg.ToUserID,
		Template:  cfg.Template,
		Listen:    cfg.Listen,
		Allowed:   cfg.Allowed,
	}); err != nil {
		return err
	}
	r.mu.Lock()
	r.cfg = cfg
	r.client = newILinkClient(cfg.BaseURL, cfg.BotToken)
	r.stage = wechatStageReady
	r.stageMsg = "登录成功"
	r.qrPNG = ""
	r.qrKey = ""
	r.mu.Unlock()
	return nil
}

// LogoutWeChat 退出登录：取消登录/监听、清掉凭据（配置文件同步更新）
//
// ⚠️ 只清凭据，**保留推送开关 `enabled`**：以前这里顺手把开关置 false 并写回 toml，
// 于是"退出后重新扫码登录"的用户会发现开关其实是关的 —— 现象就是「收到短信不推送」
// （2026-10-01 实机踩到）。没登录时 OnSMS 本来就因为 client == nil 不推，
// 留着这个标记只是记住用户的意愿，登录回来就能接着推。
func LogoutWeChat() error {
	r := wechatInst
	r.stopLogin()
	r.stopListen()

	r.mu.Lock()
	cfg := r.cfg
	cfg.BotToken = ""
	cfg.BotID = ""
	cfg.BotUserID = ""
	r.cfg = cfg
	r.client = nil
	r.loginClient = nil
	r.stage = wechatStageIdle
	r.stageMsg = "已退出登录：凭据已清除，推送已停止"
	r.qrPNG = ""
	r.qrKey = ""
	r.buf = ""
	r.lastSend = nil
	r.mu.Unlock()

	err := config.SetWeChat(config.WeChatSettings{
		Enabled:   cfg.Enabled,
		BaseURL:   cfg.BaseURL,
		BotToken:  "",
		BotID:     "",
		BotUserID: "",
		ToUserID:  cfg.ToUserID,
		Template:  cfg.Template,
		Listen:    cfg.Listen,
		Allowed:   cfg.Allowed,
	})
	pushWeChat()
	return err
}

// ------------------------------------------------------------ 扫码登录 ---
