package kernel

import (
	"context"
	"encoding/base64"
	"fmt"
	"modemd/module/config"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"go.uber.org/zap"
)

// wechatQRDataURI 把二维码内容渲染成 PNG 的 data URI（离线用 go-qrcode，不联网）
func wechatQRDataURI(content string) (string, error) {
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("二维码内容为空")
	}
	png, err := qrcode.Encode(content, qrcode.Medium, wechatQRWidth)
	if err != nil {
		return "", fmt.Errorf("生成二维码失败: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

// StartWeChatLogin 申请二维码并开始轮询扫码状态
func StartWeChatLogin() error {
	r := wechatInst
	r.stopLogin()

	r.mu.Lock()
	cfg := r.cfg
	r.stage = wechatStageWait
	r.stageMsg = "正在申请二维码…"
	r.refresh = 0
	r.verify = ""
	base := cfg.BaseURL
	tokens := []string{}
	if cfg.BotToken != "" {
		tokens = append(tokens, cfg.BotToken)
	}
	r.mu.Unlock()
	pushWeChat()

	// 未登录客户端（只有二维码两个接口能在无 token 时调用）
	anon := newILinkClient(base, "")
	ctx, cancel := context.WithCancel(context.Background())
	sess, err := anon.fetchQrCode(ctx, tokens)
	if err != nil {
		cancel()
		r.mu.Lock()
		r.stage = wechatStageFailed
		r.stageMsg = err.Error()
		r.mu.Unlock()
		pushWeChat()
		return err
	}
	pngURI, qrErr := wechatQRDataURI(sess.Content)
	if qrErr != nil {
		logger.Warn("wechat qr render failed", zap.Error(qrErr))
	}

	r.mu.Lock()
	r.loginClient = anon
	r.qrKey = sess.Qrcode
	r.qrPNG = pngURI
	r.qrAt = time.Now()
	r.pollBase = sess.BaseURL
	r.stage = wechatStageWait
	r.stageMsg = "请用手机微信扫码，并在手机上点击确认"
	r.loginCancel = cancel
	r.mu.Unlock()
	pushWeChat()

	logger.Info("wechat qr fetched")
	go wechatLoginLoop(ctx)
	return nil
}

// SubmitWeChatVerifyCode 提交配对码（由轮询线程带上）
func SubmitWeChatVerifyCode(code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return fmt.Errorf("配对码不能为空")
	}
	r := wechatInst
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stage != wechatStageVerify {
		return fmt.Errorf("当前不需要配对码")
	}
	r.verify = code
	return nil
}

// stopLogin 取消登录轮询
func (r *wechatRuntime) stopLogin() {
	r.mu.Lock()
	cancel := r.loginCancel
	r.loginCancel = nil
	r.loginClient = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// wechatLoginLoop 轮询扫码状态直到登录成功 / 超时
func wechatLoginLoop(ctx context.Context) {
	r := wechatInst
	deadline := time.Now().Add(wechatLoginWait)

	for ctx.Err() == nil {
		if time.Now().After(deadline) {
			wechatSetStage(wechatStageExpired, "等待扫码超时，请重新点击「扫码登录」")
			return
		}

		r.mu.Lock()
		cl := r.loginClient
		qk, pb := r.qrKey, r.pollBase
		vc := r.verify
		r.verify = ""
		r.mu.Unlock()
		if cl == nil || qk == "" {
			return
		}

		st := cl.pollQrStatus(ctx, pb, qk, vc)
		if ctx.Err() != nil {
			return
		}

		switch {
		case st.confirmed():
			base := st.BaseURL
			if base == "" {
				base = config.DefaultWeChatBaseURL
			}
			if st.BotToken == "" || st.BotID == "" {
				wechatSetStage(wechatStageFailed, "登录响应缺少凭据，请重试")
				return
			}
			if err := wechatSetAccount(base, st.BotToken, st.BotID, st.UserID); err != nil {
				wechatSetStage(wechatStageFailed, "保存凭据失败: "+err.Error())
				return
			}
			logger.Info("wechat login confirmed",
				zap.String("bot_id", st.BotID), zap.String("user_id", st.UserID))
			if uid := strings.TrimSpace(st.UserID); uid != "" {
				// 扫码的人自己就是要推送的目标（除非用户手填了别的）
				r.mu.Lock()
				empty := r.cfg.ToUserID == ""
				if empty {
					r.lastSender = uid
				}
				r.saveLocked()
				r.mu.Unlock()
			}
			wechatStartListen()
			pushWeChat()
			return

		case st.binded():
			// 该微信已绑定过本机器人：本地凭据仍然有效
			r.mu.Lock()
			has := r.cfg.BotToken != ""
			r.mu.Unlock()
			if !has {
				wechatSetStage(wechatStageFailed, "该微信已绑定过，但本机没有凭据：请在机器人面板里删掉旧账号后重试")
				return
			}
			wechatSetStage(wechatStageReady, "该微信已绑定过，沿用本机凭据")
			wechatStartListen()
			return

		case st.needVerify():
			wechatSetStage(wechatStageVerify, "服务端要求配对码：请把手机微信上显示的数字填进来后提交")

		case strings.EqualFold(st.Status, ilinkStatusVerifyBlock):
			wechatSetStage(wechatStageFailed, "配对码错误次数过多，请重新扫码登录")
			return

		case strings.EqualFold(st.Status, ilinkStatusRedirect) && st.RedirectHost != "":
			host := strings.TrimLeft(strings.TrimSpace(st.RedirectHost), "/")
			if !strings.HasPrefix(strings.ToLower(host), "http") {
				host = "https://" + host
			}
			r.mu.Lock()
			r.pollBase = strings.TrimRight(host, "/")
			r.stageMsg = "已按服务端指示切换轮询主机"
			r.mu.Unlock()
			pushWeChat()

		case strings.EqualFold(st.Status, ilinkStatusScanned):
			wechatSetStage(wechatStageScan, "已扫码：请在手机微信上点击确认")

		case strings.EqualFold(st.Status, ilinkStatusExpired):
			if !wechatRefreshQR(ctx) {
				return
			}

		case strings.EqualFold(st.Status, ilinkStatusWait):
			r.mu.Lock()
			if r.stage != wechatStageWait {
				r.stage = wechatStageWait
				r.stageMsg = "请用手机微信扫码，并在手机上点击确认"
			}
			r.mu.Unlock()
		}

		// 二维码超过有效期也主动刷新（服务端有时不回 expired）
		r.mu.Lock()
		stale := time.Since(r.qrAt) > wechatQRTTL
		r.mu.Unlock()
		if stale && !wechatRefreshQR(ctx) {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// wechatRefreshQR 刷新二维码（最多 wechatQRRefreshMax 次）
func wechatRefreshQR(ctx context.Context) bool {
	r := wechatInst
	r.mu.Lock()
	cl, base := r.loginClient, r.cfg.BaseURL
	r.refresh++
	n := r.refresh
	r.mu.Unlock()
	if n > wechatQRRefreshMax {
		wechatSetStage(wechatStageExpired, "二维码多次失效，请重新点击「扫码登录」")
		return false
	}
	if cl == nil {
		cl = newILinkClient(base, "")
	}
	sess, err := cl.fetchQrCode(ctx, nil)
	if err != nil {
		wechatSetStage(wechatStageExpired, "刷新二维码失败: "+err.Error())
		return false
	}
	png, _ := wechatQRDataURI(sess.Content)
	r.mu.Lock()
	r.qrKey = sess.Qrcode
	r.qrPNG = png
	r.qrAt = time.Now()
	r.pollBase = sess.BaseURL
	r.stage = wechatStageWait
	r.stageMsg = fmt.Sprintf("二维码已更新（%d/%d）：请重新扫描", n, wechatQRRefreshMax)
	r.mu.Unlock()
	pushWeChat()
	return true
}

func wechatSetStage(stage, msg string) {
	r := wechatInst
	r.mu.Lock()
	r.stage = stage
	r.stageMsg = msg
	switch stage {
	case wechatStageExpired, wechatStageFailed, wechatStageIdle:
		r.qrPNG = "" // 失效/失败就别再显示旧二维码了
	}
	r.mu.Unlock()
	pushWeChat()
}

// ------------------------------------------------------------ 收消息（学习推送目标） ---
