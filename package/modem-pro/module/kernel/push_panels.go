package kernel

import (
	"fmt"
	"modemd/module/cell"
	"modemd/module/sms"
	"modemd/module/usbhotplug"
	"time"

	"go.uber.org/zap"
)

// ====================== 各面板的推送 ======================

// pushSIMEvent 推 SIM 热插拔事件（拔卡 / 插卡 / 换卡）。
//
// 前端据此弹提示、并让缓存的 SIM/模组状态失效重读；不看那一页的客户端也会收到
// （用户可能正停在别处，换卡这种事得让他知道）。
func pushSIMEvent(mod *Modem, kind string, id SIMIdentity) {
	modID := mod.ID
	broadcast("sim", &modID, map[string]any{
		"kind": kind,
		"sim":  id,
	})
}

// pushModems 推模组列表（热插拔、启动、身份信息刷新后调用）
func pushModems(mgr *ModemManager) {
	broadcast("modems", nil, mgr.listModems())
}

// pushUSB 推 USB 热插拔视图（设备插拔 / 自动补驱动 / 拔出时调用）。
//
// ⚠️ 与 pushModems 是**两条独立的路**：模组列表只反映“探测出 AT 口的模组”，
// 而“插上了但还没交出串口”的卡在列表里根本看不到 —— 那种情况靠这条推给页面。
func pushUSB() {
	broadcast("usb", nil, usbhotplug.USBHotplugInfo())
}

// pushSignal 推信号。
// 顺手把信号波动记一条（阈值 + 等级判断见 changelog.go）——
// 记录复用同一次采样，不额外发 AT。
func pushSignal(mod *Modem) {
	sig := mod.signalSnapshot()
	noteSignal(mod, sig)
	id := mod.ID
	broadcast("signal", &id, sig)
}

// pushSignalDebounced 合并短时间内的多次信号变化。
// 一次查询会连着回好几行（+CSQ、+QCSQ/+QENG），逐行推送会把前端刷爆。
func pushSignalDebounced(mod *Modem) {
	pushDebounced(fmt.Sprintf("signal-%d", mod.ID), 400*time.Millisecond, func() {
		pushSignal(mod)
	})
}

// pushNetwork 推网络类型信息（实时查 AT+QNWINFO / CREG / CGREG / CA）
func pushNetwork(mod *Modem) {
	st, err := mod.QueryNetworkStatus()
	if err != nil {
		logger.Debug("push network status failed", zap.Int("modem", mod.ID), zap.Error(err))
		return
	}
	pushNetworkState(mod, st)
}

// pushNetworkState 推已经查好的网络状态（接口查到就顺手广播，避免重复查询）。
// 同样顺手记录网络波动：制式/频段/频点/CA/PLMN/运营商/注册/APN/IP/服务小区。
func pushNetworkState(mod *Modem, st *NetworkStatus) {
	if st == nil {
		return
	}
	mod.cacheNetworkStatus(st)
	noteNetwork(mod, st)
	// 拨号卡片要的"在用 APN / 本机地址 / 主机网卡"和「网络」卡片是同一批数据，
	// 顺手同步过去（不额外发 AT，见 dial_engine.go 的 syncDialState）
	mod.syncDialState(st)
	id := mod.ID
	broadcast("network", &id, st)
}

// pushTraffic 推上行/下行速度（每轮周期推送都会采样，所以前端能持续看到实时速度）
func pushTraffic(mod *Modem, rate Rate) {
	id := mod.ID
	broadcast("traffic", &id, rate)
}

// pushCells 推基站扫描结果（扫描完成后调用，参数就是刚扫出来的结果）。
// 扫描会刷新服务小区 / 邻区，所以顺便记录这几项波动。
func pushCells(mod *Modem, res *cell.CellScanResult) {
	if res == nil {
		res = cachedScanResult(mod)
	}
	noteCells(mod, res)
	id := mod.ID
	broadcast("cells", &id, res)
}

// pushLockState 推锁网状态（锁频段/锁基站/模式设置成功后调用）
func pushLockState(mod *Modem, st any) {
	id := mod.ID
	broadcast("lock", &id, st)
}

// pushSMS 推短信列表。
// ⚠️ 走 BroadcastSMS 而不是 broadcast：短信正文只发给已解锁的页面。
func pushSMS(mod *Modem, list []sms.SMS) {
	// 微信机器人转发：**不看有没有人开页面**（后台能力），所以放在早退之前
	wechatOnSMS(mod, list)

	if wsHubInst == nil || wsHubInst.Count() == 0 {
		return
	}
	id := mod.ID
	wsHubInst.BroadcastSMS(WSMessage{
		Type:    "sms",
		ModemID: &id,
		At:      time.Now().UnixMilli(),
		Data:    list,
	})
}

// pushSMSCount 推短信条数（总数 / 未读），**只有数字、没有任何短信内容**。
//
// 为什么不和 pushSMS 一样过短信密码闸门：这是给"角标"用的（悬浮小窗要显示未读条数），
// 拿到条数也读不到正文与号码。⚠️ 正文/号码仍只发给已解锁的页面（BroadcastSMS）。
func pushSMSCount(mod *Modem) {
	mod.mu.Lock()
	total, unread := mod.SMSTotal, mod.SMSUnread
	mod.mu.Unlock()
	id := mod.ID
	broadcast("smscount", &id, map[string]any{"total": total, "unread": unread})
}

// pushSnapshot 推一份"当前状态"：模组列表 + 各模组信号与速率缓存。
// 故意不查 AT：刚连上就占串口反而会卡住别人，实时值仍由页面按钮或周期推送提供。
func pushSnapshot(mgr *ModemManager) {
	list := mgr.listModems()
	broadcast("modems", nil, list)
	for _, mod := range list {
		pushSignal(mod)
		if st := mod.networkStatusSnapshot(); st != nil {
			id := mod.ID
			broadcast("network", &id, st)
		}
		if rate := mod.TrafficSnapshot(); rate.Supported {
			pushTraffic(mod, rate)
		}
	}
}
