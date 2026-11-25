package kernel

import (
	"fmt"
	"modemd/module/httpx"
	"modemd/module/luciauth"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// ====================== HTTP 接口 ======================

// apiSMSAuth GET /api/sms/auth
//
// 只回答"要不要密码、现在解锁了没"——**不泄露任何短信内容**，
// 所以前端进「短信」页时可以无脑先问它。
func APIAuth() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		need := smsPasswordRequired()
		httpx.WriteJSON(w, 0, map[string]any{
			"required": need,
			"unlocked": !need || smsTokenValid(smsTokenFromRequest(r)),
			"default":  need && smsPasswordIsDefault(), // 还在用默认密码？页面据此提醒改掉
			"ttl_s":    int(smsTokenTTL.Seconds()),
			// OuterAuth 外层登录闸门开着时，短信密码会被自动停用（见 smsPasswordRequired）：
			// 界面据此把「访问密码」卡说成"已由路由器登录保护"，而不是"任何人打开本页都能看短信"。
			"outer_auth": luciauth.LuciAuthEnabled(),
		}, "ok")
	}
}

// apiSMSUnlock POST /api/sms/unlock {"password":"password"}
func APIUnlock() http.HandlerFunc {
	type reqBody struct {
		Password string `json:"password"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpx.WriteJSON(w, -1, nil, "method not allowed, use POST")
			return
		}
		var req reqBody
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		if !smsPasswordRequired() {
			httpx.WriteJSON(w, 0, map[string]any{"unlocked": true, "token": ""}, "ok")
			return
		}
		ip := smsClientIP(r)
		if wait, ok := smsUnlockAllowed(ip); !ok {
			secs := int(wait.Seconds()) + 1
			httpx.WriteJSON(w, 429, map[string]any{"retry_after_s": secs},
				fmt.Sprintf("密码错误次数过多，请 %d 秒后再试", secs))
			return
		}
		if !smsPasswordMatch(req.Password) {
			n, locked := smsUnlockFailed(ip)
			logger.Warn("sms unlock failed", zap.String("remote", ip), zap.Int("tries", n))
			if locked {
				httpx.WriteJSON(w, 429, map[string]any{"retry_after_s": int(smsFailLockFor.Seconds())},
					fmt.Sprintf("密码错误次数过多，已锁定 %d 秒", int(smsFailLockFor.Seconds())))
				return
			}
			httpx.WriteJSON(w, 403, map[string]any{"tries": n, "left": smsFailMax - n},
				fmt.Sprintf("密码不正确（还可尝试 %d 次）", smsFailMax-n))
			return
		}
		tok := smsTokenIssue()
		if tok == "" {
			httpx.WriteJSON(w, -1, nil, "生成访问令牌失败")
			return
		}
		smsAuthMu.Lock()
		smsTokens[tok] = time.Now()
		smsPruneTokensLocked()
		smsAuthMu.Unlock()
		smsUnlockSucceeded(ip)
		logger.Info("sms unlocked", zap.String("remote", ip), zap.Int("sessions", len(smsTokens)))
		httpx.WriteJSON(w, 0, map[string]any{
			"unlocked": true,
			"token":    tok,
			"ttl_s":    int(smsTokenTTL.Seconds()),
		}, "ok")
	}
}

// apiSMSLock POST /api/sms/lock 主动锁定（丢掉当前令牌）
func APILock() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpx.WriteJSON(w, -1, nil, "method not allowed, use POST")
			return
		}
		tok := smsTokenFromRequest(r)
		smsTokenRevoke(tok)
		logger.Info("sms locked", zap.String("remote", smsClientIP(r)))
		httpx.WriteJSON(w, 0, map[string]any{"unlocked": false}, "ok")
	}
}

// smsPruneTokensLocked 清理过期令牌，并按需裁剪到上限（调用方持锁）
func smsPruneTokensLocked() {
	now := time.Now()
	for t, last := range smsTokens {
		if now.Sub(last) > smsTokenTTL {
			delete(smsTokens, t)
		}
	}
	for len(smsTokens) > smsTokenMax {
		oldest, oldestAt := "", now
		for t, last := range smsTokens {
			if last.Before(oldestAt) {
				oldest, oldestAt = t, last
			}
		}
		if oldest == "" {
			break
		}
		delete(smsTokens, oldest)
	}
}

// LogSMSPasswordHint 启动时提示“还在用默认密码”，否则用户以为自己已经保护上了
func LogSMSPasswordHint() {
	if !smsPasswordRequired() {
		logger.Warn("sms password is disabled (ui.sms_password empty / sms_password_required=false)")
		return
	}
	if smsPasswordIsDefault() {
		logger.Warn("sms password is the default value; change ui.sms_password in toml")
		return
	}
	logger.Info("sms password protection enabled")
}
