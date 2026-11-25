package kernel

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ====================== 令牌 ======================

func smsTokenIssue() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		logger.Error("sms token rand failed", zap.Error(err))
		return ""
	}
	return hex.EncodeToString(b)
}

// smsTokenValid 校验并续期（滑动过期）。空令牌、未知令牌、过期令牌都算无效。
func smsTokenValid(tok string) bool {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return false
	}
	smsAuthMu.Lock()
	defer smsAuthMu.Unlock()
	last, ok := smsTokens[tok]
	if !ok {
		return false
	}
	if time.Since(last) > smsTokenTTL {
		delete(smsTokens, tok)
		return false
	}
	smsTokens[tok] = time.Now()
	return true
}

func smsTokenRevoke(tok string) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return
	}
	smsAuthMu.Lock()
	delete(smsTokens, tok)
	smsAuthMu.Unlock()
}

// smsTokenFromRequest 取令牌：优先请求头，其次查询参数（WebSocket 握手只能用后者）
func smsTokenFromRequest(r *http.Request) string {
	if t := strings.TrimSpace(r.Header.Get("X-SMS-Token")); t != "" {
		return t
	}
	return strings.TrimSpace(r.URL.Query().Get("sms_token"))
}

// smsUnlocked 当前请求是否有权访问短信内容
func smsUnlocked(r *http.Request) bool {
	if !smsPasswordRequired() {
		return true
	}
	return smsTokenValid(smsTokenFromRequest(r))
}

// smsGate 短信接口的统一闸门：没解锁就回 code=401（前端据此弹密码框）。
// 返回 false 表示已经写过响应，调用方直接 return。
