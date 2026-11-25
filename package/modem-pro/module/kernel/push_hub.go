package kernel

import (
	"sync"
)

// ====================== 广播中心 ======================

// Hub 维护所有在线连接，负责把消息广播给全部客户端。
// 没有任何客户端时 Count()==0 —— 推送循环据此跳过 AT 查询，避免空转占串口。
type Hub struct {
	mu    sync.Mutex
	conns map[*Conn]struct{}
}

// NewHub 建一个空的广播中心
func NewHub() *Hub {
	return &Hub{conns: make(map[*Conn]struct{})}
}

// Add 注册一条连接（Serve 内部与测试用）
func (h *Hub) Add(c *Conn) {
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
}

// Drop 摘除一条连接（Serve 内部与测试用）
func (h *Hub) Drop(c *Conn) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
}

// Count 当前在线客户端数
func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

// CountActive 在线**主动**客户端数（不含屏幕这类被动、只读缓存的连接）。
//
// 周期推送环（pushRound / TrafficLoop）用它决定要不要发 AT：
// 只有屏幕挂着的时候**一条 AT 都不发**，屏幕拿的就是现有缓存；
// 想让没人看网页时也保持刷新，用 modemd 自己的 change_interval 后台采样（见配置里的说明）。
func (h *Hub) CountActive() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for c := range h.conns {
		if !c.passive {
			n++
		}
	}
	return n
}

// snapshotConns 复制一份当前连接列表（持锁时间要短）
func (h *Hub) snapshotConns() []*Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Conn, 0, len(h.conns))
	for c := range h.conns {
		out = append(out, c)
	}
	return out
}

// Broadcast 把消息推给所有客户端。
// 单个客户端写失败不会影响其他客户端，只把它摘掉。
func (h *Hub) Broadcast(v any) {
	conns := h.snapshotConns()
	var dead []*Conn
	for _, c := range conns {
		if err := c.WriteJSON(v); err != nil {
			dead = append(dead, c)
		}
	}
	for _, c := range dead {
		h.Drop(c)
		c.close()
	}
}

// BroadcastSMS 只把消息推给"已解锁短信"的连接（见 Conn.smsPushesAllowed）。
func (h *Hub) BroadcastSMS(v any) {
	conns := h.snapshotConns()
	var dead []*Conn
	for _, c := range conns {
		if !c.smsPushesAllowed() {
			continue
		}
		if err := c.WriteJSON(v); err != nil {
			dead = append(dead, c)
		}
	}
	for _, c := range dead {
		h.Drop(c)
		c.close()
	}
}
