package kernel

import (
	"crypto/sha1"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ====================== HTTP 升级入口 ======================

// Options 是业务侧注入的回调（本包不认识 Modem，也不认识消息格式）。
type Options struct {
	// AllowSMS 决定某个连接能不能收短信推送（传 nil = 不限制）。
	// kernel 把"没配密码都能收 / 配了就要令牌有效"的逻辑放这里。
	AllowSMS func(token string) bool
	// OnConnect 握手完成、连接已注册后调用（kernel 在这里推一份当前状态快照，
	// 免得前端"连上了但一片空白"）。
	OnConnect func()
	// OnMessage 收到一个文本帧时调用（kernel 在这里处理前端的"再来一份快照"等指令）。
	OnMessage func(payload []byte)
	// Passive 被动连接：只读缓存，不计入"要不要发 AT"（见 Conn.passive / CountActive）。
	// 屏幕端（/display/ws）置 true。
	Passive bool
}

// Serve 处理 GET /ws：升级为 WebSocket 并保持连接，用于服务端主动推送。
//
// 用法见 glue.go：hub.Serve(w, r, ws.Options{AllowSMS: …, OnConnect: …, OnMessage: …})
func (h *Hub) Serve(w http.ResponseWriter, r *http.Request, opt Options) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		!strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		http.Error(w, "this endpoint speaks websocket only", http.StatusUpgradeRequired)
		return
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		logger.Warn("websocket hijack failed", zap.Error(err))
		return
	}

	// RFC 6455 握手：accept = base64(sha1(key + GUID))
	sum := sha1.Sum([]byte(key + GUID))
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n"
	if _, err := conn.Write([]byte(resp)); err != nil {
		_ = conn.Close()
		return
	}

	c := NewConn(conn, brw.Reader)
	c.allowSMS = opt.AllowSMS
	c.passive = opt.Passive
	// 短信令牌走查询参数：HTTP 头在浏览器里没法自己设到 WebSocket 握手上
	c.setSMSToken(r.URL.Query().Get("sms_token"))
	h.Add(c)
	remote := conn.RemoteAddr().String()
	logger.Info("websocket connected",
		zap.String("remote", remote), zap.Int("clients", h.Count()))
	defer func() {
		h.Drop(c)
		c.close()
		logger.Info("websocket disconnected",
			zap.String("remote", remote), zap.Int("clients", h.Count()))
	}()

	// 周期 ping：既是保活，也让读侧的 deadline 能判断连接是否真的断了
	go func() {
		t := time.NewTicker(wsPingInterval)
		defer t.Stop()
		for {
			select {
			case <-c.done:
				return
			case <-t.C:
				if err := c.WriteFrame(OpPing, nil); err != nil {
					c.close()
					return
				}
			}
		}
	}()

	// 连上先给一份当前状态，避免前端"连上了但一片空白"
	if opt.OnConnect != nil {
		opt.OnConnect()
	}

	for {
		_ = conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		op, payload, err := c.ReadFrame()
		if err != nil {
			return
		}
		switch op {
		case OpClose:
			_ = c.WriteFrame(OpClose, nil)
			return
		case OpPing:
			_ = c.WriteFrame(OpPong, payload)
		case OpText:
			if opt.OnMessage != nil {
				opt.OnMessage(payload)
			}
		}
	}
}
