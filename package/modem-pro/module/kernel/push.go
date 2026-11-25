package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"sort"
	"time"
)

// ---- from: push.go ----

// ---- from: push.go ----
// wsHubInst 全局广播中心（main 中创建；nil 表示不启用推送）
// wsHubInst 全局广播中心（main 中创建；nil 表示不启用推送）

var wsHubInst *Hub

// WSMessage 推送消息统一信封。
//
// 前端按 Type 分发到对应面板；ModemID 用于过滤（当前选中的模组才更新）。
// At 是推送时间（unix 毫秒），前端可据此显示"刚刚更新"。
type WSMessage struct {
	Type    string `json:"type"`               // modems | usb | signal | network | cells | lock | sms | traffic | changes
	ModemID *int   `json:"modem_id,omitempty"` // 该消息属于哪个模组
	At      int64  `json:"at"`
	Data    any    `json:"data,omitempty"`
}

// broadcast 广播一条消息。没有客户端订阅时直接返回（省掉序列化开销）。
func broadcast(kind string, modemID *int, data any) {
	if wsHubInst == nil || wsHubInst.Count() == 0 {
		return
	}
	wsHubInst.Broadcast(WSMessage{
		Type:    kind,
		ModemID: modemID,
		At:      time.Now().UnixMilli(),
		Data:    data,
	})
}

// ====================== 取数辅助 ======================

// listLocked 取模组列表（**调用方必须已持有 mgr.mu**）
func (mgr *ModemManager) listLocked() []*Modem {
	list := make([]*Modem, 0, len(mgr.modems))
	for _, m := range mgr.modems {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

// publishModemsLocked 重建无锁读快照（**调用方必须已持有 mgr.mu**）。
// 任何改动 mgr.modems（增/删模组）的地方，都要在提交后调一次。
func (mgr *ModemManager) publishModemsLocked() {
	list := mgr.listLocked()
	mgr.snap.Store(&list)
}

// listModems 返回按 ID 排序的模组快照（**无锁**）。
// map 迭代顺序随机，排序后前端下拉框不会每次刷新都跳位置。
//
// ⚠️ 这里刻意**不取 mgr.mu**：发现流程会在持锁期间对未知串口逐个发 AT 探测
// （每个口可达数秒），一旦读列表也要这把锁，扫描到慢启动的 5G 模块时所有接口都会被堵住。
// 快照在 mgr.mu 保护下重建（publishModemsLocked），读到的始终是一份不可变副本。
func (mgr *ModemManager) listModems() []*Modem {
	if p := mgr.snap.Load(); p != nil {
		return *p
	}
	// 快照还没发布过（进程刚起、或单测直接塞 mgr.modems）：退回加锁从 map 读。
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	return mgr.listLocked()
}

// modemByID 无锁按 ID 取模组（读快照；找不到返回 false）。
func (mgr *ModemManager) modemByID(id int) (*Modem, bool) {
	for _, m := range mgr.listModems() {
		if m.ID == id {
			return m, true
		}
	}
	return nil, false
}

// signalSnapshot 读取信号缓存。
// parseLine 在读协程里更新 SignalInfo，接口与推送在别的 goroutine 读，
// 必须走锁（否则是数据竞争）。
func (mod *Modem) signalSnapshot() SignalInfo {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	return mod.SignalInfo
}

// isRunningNow 读运行状态（加锁）
func (mod *Modem) isRunningNow() bool {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	return mod.isRunning
}
