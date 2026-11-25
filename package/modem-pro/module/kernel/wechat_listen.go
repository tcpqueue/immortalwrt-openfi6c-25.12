package kernel

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// wechatStartListen 启动长轮询监听（登录后自动调用）
func wechatStartListen() {
	r := wechatInst
	r.mu.Lock()
	cfg := r.cfg
	cl := r.client
	already := r.listenOn
	r.mu.Unlock()
	if !cfg.Listen || cl == nil || !cl.authenticated() || already {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.listenCancel = cancel
	r.listenOn = true
	r.mu.Unlock()
	go wechatListenLoop(ctx, cl)
}

// wechatSyncListen 配置变化后对齐监听的开/关
func wechatSyncListen() {
	r := wechatInst
	r.mu.Lock()
	cfg := r.cfg
	on := r.listenOn
	hasToken := r.cfg.BotToken != ""
	r.mu.Unlock()
	switch {
	case cfg.Listen && hasToken && !on:
		wechatStartListen()
	case !cfg.Listen && on:
		r.stopListen()
	}
}

func (r *wechatRuntime) stopListen() {
	r.mu.Lock()
	cancel := r.listenCancel
	r.listenCancel = nil
	r.listenOn = false
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// wechatListenLoop 长轮询 getupdates：记录"谁给机器人发过消息"，作为默认推送目标
func wechatListenLoop(ctx context.Context, cl *ilinkClient) {
	r := wechatInst
	defer func() {
		_ = cl.notify(context.Background(), "/ilink/bot/msg/notifystop")
		r.mu.Lock()
		r.listenOn = false
		r.mu.Unlock()
	}()
	if err := cl.notify(ctx, "/ilink/bot/msg/notifystart"); err != nil {
		logger.Warn("wechat notifystart failed", zap.Error(err))
	}

	fails := 0
	for ctx.Err() == nil {
		r.mu.Lock()
		buf := r.buf
		r.mu.Unlock()

		up, err := cl.getUpdates(ctx, buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			fails++
			logger.Warn("wechat getupdates failed", zap.Int("fails", fails), zap.Error(err))
			wechatSleepCtx(ctx, wechatBackoff(fails))
			continue
		}

		// 会话过期（-14）：清游标重来；游标本来就是空的说明 token 已失效
		if up.ErrCode == -14 {
			r.mu.Lock()
			hasBuf := r.buf != ""
			if hasBuf {
				r.buf = ""
				r.saveLocked()
			}
			r.mu.Unlock()
			if !hasBuf {
				wechatSetStage(wechatStageFailed, "Bot 登录已失效，请重新扫码登录")
				return
			}
			wechatSleepCtx(ctx, 5*time.Second)
			continue
		}
		if up.Ret != 0 && up.ErrCode != 0 {
			fails++
			logger.Warn("wechat getupdates error",
				zap.Int("ret", up.Ret), zap.Int("errcode", up.ErrCode), zap.String("errmsg", up.ErrMsg))
			wechatSleepCtx(ctx, wechatBackoff(fails))
			continue
		}

		fails = 0
		if up.Buf != "" {
			r.mu.Lock()
			r.buf = up.Buf
			r.saveMaybeLocked()
			r.mu.Unlock()
		}
		for _, m := range up.Msgs {
			wechatHandleIncoming(m)
		}
	}
}

func wechatBackoff(fails int) time.Duration {
	if fails >= 3 {
		return 30 * time.Second
	}
	return 2 * time.Second
}

func wechatSleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
