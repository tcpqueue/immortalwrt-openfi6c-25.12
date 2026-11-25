package kernel

import (
	"testing"
	"time"
)

// 回归：读路径（模组列表 / 按 ID 取模组）必须走**无锁快照**。
//
// 背景（用户实测）：5G 模组通常交 4~6 个串口、且冷启动慢，发现流程会对未知串口
// 逐个发 AT 探测（每口可达数秒）。以前读接口也要 mgr.mu，于是"扫描到 5G 模块时
// 整个页面卡住"。这里模拟"发现流程持着 mgr.mu 做慢探测"，读路径必须照样秒回。
func TestModemsReadIsLockFreeAfterPublish(t *testing.T) {
	mgr := NewModemManager()
	mod := &Modem{ID: 3, ATPort: "COM3"}

	mgr.mu.Lock()
	mgr.modems[3] = mod
	mgr.publishModemsLocked()
	mgr.mu.Unlock()

	// 占住 mgr.mu，等价于"发现流程正在持锁探测串口"。
	mgr.mu.Lock()

	listCh := make(chan []*Modem, 1)
	go func() { listCh <- mgr.listModems() }()
	select {
	case list := <-listCh:
		if len(list) != 1 || list[0].ID != 3 {
			mgr.mu.Unlock()
			t.Fatalf("listModems = %v, want 单个 id=3", list)
		}
	case <-time.After(2 * time.Second):
		mgr.mu.Unlock()
		t.Fatal("listModems 被 mgr.mu 堵住：读路径没走无锁快照（会导致扫描 5G 时页面卡）")
	}

	byIDCh := make(chan bool, 1)
	go func() {
		_, ok := mgr.modemByID(3)
		byIDCh <- ok
	}()
	select {
	case ok := <-byIDCh:
		mgr.mu.Unlock()
		if !ok {
			t.Fatal("modemByID(3) 没找到")
		}
	case <-time.After(2 * time.Second):
		mgr.mu.Unlock()
		t.Fatal("modemByID 被 mgr.mu 堵住：读路径没走无锁快照")
	}
}

// 快照是**不可变副本**：发布后即使底层 map 被改，已取出的快照不受影响
// （否则读协程会在遍历时看到 map 被改，属于数据竞争）。
func TestModemsSnapshotIsImmutableCopy(t *testing.T) {
	mgr := NewModemManager()
	mgr.mu.Lock()
	mgr.modems[0] = &Modem{ID: 0}
	mgr.publishModemsLocked()
	mgr.mu.Unlock()

	snap := mgr.listModems()
	if len(snap) != 1 {
		t.Fatalf("snapshot len=%d, want 1", len(snap))
	}
	// 改动底层 map（不重新发布）不应影响已取出的快照。
	mgr.mu.Lock()
	mgr.modems[1] = &Modem{ID: 1}
	mgr.mu.Unlock()

	if len(snap) != 1 {
		t.Fatalf("已取出的快照被后续改动影响：len=%d, want 1", len(snap))
	}
	// 重新发布后才应看到新成员。
	mgr.mu.Lock()
	mgr.publishModemsLocked()
	mgr.mu.Unlock()
	if got := mgr.listModems(); len(got) != 2 {
		t.Fatalf("重新发布后 len=%d, want 2", len(got))
	}
}
