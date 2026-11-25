package kernel

import (
	"modemd/module/httpx"
	"net/http"
)

// ====================== 微信机器人 API ======================
//
//	GET  /api/wechat          面板数据（配置 + 登录状态 + 最近发送结果）
//	POST /api/wechat/set      保存配置（enabled / to_user_id / template / listen / …）
//	POST /api/wechat/login    申请二维码，开始扫码登录
//	POST /api/wechat/verify   提交配对码 {"code":"1234"}
//	POST /api/wechat/logout   退出登录（清掉凭据）
//	POST /api/wechat/test     发一条测试消息
//
// ⚠️ 全部走短信密码闸门（smsGate）：机器人凭据比短信本身还敏感 ——
// 能改推送目标就等于能把短信转发到任意微信，所以未解锁一律 401。

func apiWeChatGate(w http.ResponseWriter, r *http.Request) bool {
	return smsGate(w, r)
}

// apiWeChat GET /api/wechat
func apiWeChat(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiWeChatGate(w, r) {
			return
		}
		httpx.WriteJSON(w, 0, wechatView(), "ok")
	}
}

// apiWeChatSet POST /api/wechat/set
//
// body 里只传要改的字段（指针语义：缺省 = 不动）。bot_token 可以手填
// （比如从别的机器上的 C# 客户端抄过来），一般不用 —— 扫码登录会自动写进去。
func apiWeChatSet(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiWeChatGate(w, r) {
			return
		}
		var req struct {
			Enabled   *bool   `json:"enabled"`
			BaseURL   *string `json:"base_url"`
			BotToken  *string `json:"bot_token"`
			BotID     *string `json:"bot_id"`
			BotUserID *string `json:"bot_user_id"`
			ToUserID  *string `json:"to_user_id"`
			Template  *string `json:"template"`
			Listen    *bool   `json:"listen"`
			Allowed   *string `json:"allowed_senders"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		if req.Enabled != nil && *req.Enabled {
			// 打开开关前必须已登录，否则"看着开着其实发不出去"
			if !HasToken() {
				httpx.WriteJSON(w, -1, wechatView(), "还没登录微信机器人：先扫码登录再打开推送开关")
				return
			}
		}
		err := ApplyWeChatConfig(WeChatConfigPatch{
			Enabled: req.Enabled, BaseURL: req.BaseURL, BotToken: req.BotToken,
			BotID: req.BotID, BotUserID: req.BotUserID,
			ToUserID: req.ToUserID, Template: req.Template, Listen: req.Listen, Allowed: req.Allowed,
		})
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		logger.Info("wechat config saved")
		httpx.WriteJSON(w, 0, wechatView(), "ok")
	}
}

// apiWeChatLogin POST /api/wechat/login
func apiWeChatLogin(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiWeChatGate(w, r) {
			return
		}
		if err := StartWeChatLogin(); err != nil {
			httpx.WriteJSON(w, -1, wechatView(), err.Error())
			return
		}
		httpx.WriteJSON(w, 0, wechatView(), "二维码已生成，请用手机微信扫码")
	}
}

// apiWeChatVerify POST /api/wechat/verify  {"code":"1234"}
func apiWeChatVerify(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiWeChatGate(w, r) {
			return
		}
		var req struct {
			Code string `json:"code"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		if err := SubmitWeChatVerifyCode(req.Code); err != nil {
			httpx.WriteJSON(w, -1, wechatView(), err.Error())
			return
		}
		logger.Info("wechat verify code submitted")
		httpx.WriteJSON(w, 0, wechatView(), "配对码已提交")
	}
}

// apiWeChatLogout POST /api/wechat/logout
func apiWeChatLogout(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiWeChatGate(w, r) {
			return
		}
		if err := LogoutWeChat(); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		logger.Info("wechat logged out")
		httpx.WriteJSON(w, 0, wechatView(), "已退出登录")
	}
}

// apiWeChatTest POST /api/wechat/test
func apiWeChatTest(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !apiWeChatGate(w, r) {
			return
		}
		if err := SendWeChatTest(); err != nil {
			httpx.WriteJSON(w, -1, wechatView(), err.Error())
			return
		}
		httpx.WriteJSON(w, 0, wechatView(), "测试消息已发出")
	}
}
