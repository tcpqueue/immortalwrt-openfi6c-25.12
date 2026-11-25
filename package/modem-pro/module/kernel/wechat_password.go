package kernel

import (
	"fmt"
	"modemd/module/config"
	"modemd/module/httpx"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// ====================== 在页面上改密码 ======================

// validSMSPassword 密码字符集校验。
//
// 只禁三类：`"` 与 `\`（要写进 toml 的 TOML 字符串，留着就得转义，容易出坑）、
// 控制字符（含换行/制表，会把配置文件写乱）。其余随便（中文也可以）。
func validSMSPassword(p string) error {
	n := len([]rune(p))
	if n < 4 || n > 64 {
		return fmt.Errorf("密码长度要 4~64 个字符（当前 %d）", n)
	}
	for _, r := range p {
		switch {
		case r == '"' || r == '\\':
			return fmt.Errorf("密码里不能有双引号或反斜杠（要写进配置文件）")
		case r < 0x20 || r == 0x7f:
			return fmt.Errorf("密码里不能有控制字符/换行")
		}
	}
	return nil
}

// apiSMSPasswordSet POST /api/sms/password
//
//	{"current":"旧密码","new_password":"新密码","enabled":true}
//
// 三种用法（字段都可缺省）：
//   - 只传 new_password：改密码（需要 current 或已经解锁）
//   - 只传 enabled：开/关密码校验
//   - 都传：改密码 + 开关
//
// 安全约束：**必须先解锁**（或本来就没启用密码）；要动密码/关保护时还要再验一次 current ——
// 页面上虽然已经解锁了，但"改密码"这种动作值得二次确认，也顺手挡住借用未锁定会话的场景。
// 成功后：**吊销全部旧会话**（旧令牌一律失效），并给当前这个页面补发一个新令牌。
func APIPasswordSet() http.HandlerFunc {
	type reqBody struct {
		Current     string `json:"current"`
		NewPassword string `json:"new_password"`
		Enabled     *bool  `json:"enabled"`
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
		required := smsPasswordRequired()
		// 没解锁就什么都别想改（关闭密码时 unlocked 恒为 true，不需要令牌）
		if !smsUnlocked(r) {
			httpx.WriteJSON(w, 401, map[string]any{"locked": true, "required": required}, "短信需要密码，请先解锁")
			return
		}
		wantChangePwd := req.NewPassword != ""
		wantToggle := req.Enabled != nil && *req.Enabled != required
		if !wantChangePwd && !wantToggle {
			httpx.WriteJSON(w, -1, nil, "没有要修改的内容")
			return
		}
		// 已经在用密码保护时，改密码 / 关保护都要再验一次旧密码
		if required {
			ip := smsClientIP(r)
			if wait, ok := smsUnlockAllowed(ip); !ok {
				secs := int(wait.Seconds()) + 1
				httpx.WriteJSON(w, 429, map[string]any{"retry_after_s": secs},
					fmt.Sprintf("密码错误次数过多，请 %d 秒后再试", secs))
				return
			}
			if !smsPasswordMatch(req.Current) {
				n, locked := smsUnlockFailed(ip)
				logger.Warn("sms password change rejected", zap.String("remote", ip), zap.Int("tries", n))
				if locked {
					httpx.WriteJSON(w, 429, nil, fmt.Sprintf("密码错误次数过多，已锁定 %d 秒", int(smsFailLockFor.Seconds())))
					return
				}
				httpx.WriteJSON(w, 403, map[string]any{"tries": n, "left": smsFailMax - n},
					fmt.Sprintf("当前密码不正确（还可尝试 %d 次）", smsFailMax-n))
				return
			}
			smsUnlockSucceeded(ip)
		}
		if wantChangePwd {
			if err := validSMSPassword(req.NewPassword); err != nil {
				httpx.WriteJSON(w, -1, nil, err.Error())
				return
			}
		}

		// 算最终落盘的值：关闭保护时把密码留着（下次开启不用重输），只是不再校验
		newPwd := smsPasswordText()
		if wantChangePwd {
			newPwd = req.NewPassword
		}
		newRequired := required
		if req.Enabled != nil {
			newRequired = *req.Enabled
		}
		if err := config.SetUISMSAuth(newPwd, newRequired); err != nil {
			logger.Error("write sms password to config failed", zap.Error(err))
			httpx.WriteJSON(w, -1, nil, "写配置文件失败: "+err.Error())
			return
		}
		// 密码变了（或开关变了）→ 旧令牌全部作废，避免“改完密码旧会话还能用”
		smsAuthMu.Lock()
		smsTokens = map[string]time.Time{}
		smsAuthMu.Unlock()
		tok := ""
		if newRequired {
			tok = smsTokenIssue()
			if tok != "" {
				smsAuthMu.Lock()
				smsTokens[tok] = time.Now()
				smsAuthMu.Unlock()
			}
		}
		logger.Info("sms password updated",
			zap.Bool("required", newRequired), zap.Bool("changed", wantChangePwd))
		httpx.WriteJSON(w, 0, map[string]any{
			"required": newRequired,
			"changed":  wantChangePwd,
			"token":    tok, // 密码关掉时为空串
			"ttl_s":    int(smsTokenTTL.Seconds()),
		}, "ok")
	}
}
