package kernel

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// ====================== 单条连接 ======================

// Conn 一条已完成握手的连接。
// 写操作必须走 wmu：广播（pushLoop goroutine）与 pong（读循环）可能同时写。
type Conn struct {
	conn   net.Conn
	br     *bufio.Reader
	done   chan struct{}
	wmu    sync.Mutex
	closed bool

	// allowSMS 短信推送闸门（nil = 不限制）；由调用方注入（kernel 的密码校验）
	allowSMS func(token string) bool

	// passive 被动连接：只“看”，**不应该因为它连着就发 AT 指令**。
	// 目前只有屏幕端（tftdisplay 走 /display/ws）会置 true —— 那块屏 7x24 挂着，
	// 让它每到点就把模组问一遗（信号/网络/流量）既没必要、也干扰别人。
	// 它照样能收到所有广播（包括周期性推送）；只是“没人主动看时要不要开轮询”
	// 这件事不计入它（见 CountActive）。
	passive bool

	// smsMu/smsToken 短信解锁令牌（握手时从 ?sms_token= 带上）。
	// 短信推送只发给令牌当时有效的连接 —— 没解锁的页面（或随手连上的客户端）
	// 不该因为"连着 WS"就收到短信正文。
	smsMu    sync.Mutex
	smsToken string
}

// NewConn 包一层已建立的连接（上层测试也用它做客户端解帧）
func NewConn(c net.Conn, br *bufio.Reader) *Conn {
	return &Conn{conn: c, br: br, done: make(chan struct{})}
}

// Done 关闭信号（测试用来确认 close 生效）
func (c *Conn) Done() <-chan struct{} { return c.done }

func (c *Conn) setSMSToken(tok string) {
	c.smsMu.Lock()
	c.smsToken = tok
	c.smsMu.Unlock()
}

// smsPushesAllowed 能否接收短信推送：没配闸门就都能收；配了就要令牌有效
func (c *Conn) smsPushesAllowed() bool {
	if c.allowSMS == nil {
		return true
	}
	c.smsMu.Lock()
	tok := c.smsToken
	c.smsMu.Unlock()
	return c.allowSMS(tok)
}

// WriteFrame 发送一帧（服务端发出的帧不需要掩码）
func (c *Conn) WriteFrame(op byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return errors.New("websocket closed")
	}
	head := make([]byte, 0, 10)
	head = append(head, 0x80|op) // FIN + opcode
	switch n := len(payload); {
	case n < 126:
		head = append(head, byte(n))
	case n < 65536:
		head = append(head, 126, byte(n>>8), byte(n))
	default:
		head = append(head, 127)
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		head = append(head, b[:]...)
	}
	// 写超时防呆：对端不读时不能把广播 goroutine 卡死
	_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	if _, err := c.conn.Write(head); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := c.conn.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// WriteJSON 发送一条 JSON 文本帧（上层测试也用它）
func (c *Conn) WriteJSON(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.WriteFrame(OpText, payload)
}

// ReadFrame 读取一帧，自动按掩码位解掩码（客户端帧必带掩码）
func (c *Conn) ReadFrame() (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(c.br, h[:]); err != nil {
		return 0, nil, err
	}
	op := h[0] & 0x0F
	masked := h[1]&0x80 != 0
	n := uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return 0, nil, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if n > wsMaxFrame {
		return 0, nil, errors.New("websocket frame too large")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(c.br, payload); err != nil {
			return 0, nil, err
		}
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return op, payload, nil
}

// Close 幂等关闭（广播与读循环都可能触发），Serve 内部与测试用。
func (c *Conn) Close() { c.close() }

// close 幂等关闭（广播与读循环都可能触发）。
//
// ⚠️ done 只能关一次：广播写失败与读循环断开会先后调用 close，
// 之前这里无条件 close(c.done)，第二次直接 panic（关闭已关闭的 channel），
// 会把整个进程带走 —— 所以判断要放在 close(done) 之前。
func (c *Conn) close() {
	c.wmu.Lock()
	already := c.closed
	c.closed = true
	c.wmu.Unlock()
	if already {
		return
	}
	close(c.done)
	_ = c.conn.Close()
}
