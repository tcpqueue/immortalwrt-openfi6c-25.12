//go:build !windows

package kernel

import (
	"testing"
	"time"
)

// TestHostDialKickDue "拨号器起来了但接口一直没地址 ⇒ 每轮对账踢一次 ifup"：
// 用户 2026-10-08 报的"QMI 拨号后接口拿不到 IP、要手工重启网络接口才拿到"那个时序坑。
func TestHostDialKickDue(t *testing.T) {
	now := time.Now()

	// 刚发现没地址（还在宽限期内）⇒ 先等一会儿（拨号器自己那份 udhcpc 可能马上就装地址）
	if hostDialKickDue(false, true, hostDialKickState{}, now) {
		t.Error("刚发现没地址不该立刻踢 ifup")
	}
	// 等够了 ⇒ 该替用户踢一次
	waited := hostDialKickState{since: now.Add(-hostDialKickGrace - time.Second)}
	if !hostDialKickDue(false, true, waited, now) {
		t.Error("等够了就该踢一次 ifup")
	}
	// 已经有地址 / 网卡还没 up ⇒ 一律不碰
	if hostDialKickDue(true, true, waited, now) {
		t.Error("已经有地址就不该动接口")
	}
	if hostDialKickDue(false, false, waited, now) {
		t.Error("网卡还没 up 时不该踢")
	}
	// 刚踢过（还在间隔内）⇒ 不重复踢
	justKicked := hostDialKickState{since: now.Add(-time.Minute), last: now.Add(-time.Second), kicks: 1}
	if hostDialKickDue(false, true, justKicked, now) {
		t.Error("两次 ifup 之间要留间隔")
	}
	// 间隔到了、也没超次数 ⇒ 再踢
	okGap := hostDialKickState{since: now.Add(-time.Minute), last: now.Add(-hostDialKickGap - time.Second), kicks: 1}
	if !hostDialKickDue(false, true, okGap, now) {
		t.Error("间隔到了应该再踢一次")
	}
	// 踢满次数 ⇒ 不再打转（链路真坏了，剩下的交给日志/页面提示）
	maxed := hostDialKickState{since: now.Add(-time.Hour), last: now.Add(-time.Hour), kicks: hostDialKickMax}
	if hostDialKickDue(false, true, maxed, now) {
		t.Error("踢满次数后不该无限重试")
	}
}
