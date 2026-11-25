package kernel

import (
	"modemd/module/config"
	"time"

	"go.uber.org/zap"
)

// ====================== 周期推送 ======================

// ---- from: push_loop.go ----

// pushLoop 周期性把状态推给已连接的客户端。
//
// 关键点：**没有客户端在线时直接跳过**，完全不碰串口 ——
// 也就是"有人在看才刷新"，不装 WS 客户端时后台依旧零 AT 流量。
func PushLoop(mgr *ModemManager, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	round := 0
	for range ticker.C {
		round = pushRound(mgr, round)
	}
}

// pushRound 执行一轮周期推送，返回累计轮次。
// 返回轮次而不是藏在闭包里，是为了让单元测试能验证"没有客户端时整轮跳过"。
//
// ⚠️ 判据是 **CountActive()**：屏幕端（被动连接）挂着不算"有人在看" ——
// 那块屏 7x24 在线，不该因为它就让模组每秒被问一遗。
func pushRound(mgr *ModemManager, round int) int {
	if wsHubInst == nil || wsHubInst.CountActive() == 0 {
		return round
	}
	round++
	for _, mod := range mgr.listModems() {
		if !mod.isRunningNow() {
			continue
		}
		// ⚠️ 展锐 SIPC 上“状态全在本机文件里”时（mu300FileBackedState）
		// **这一轮一条 AT 都不发**：信号取自 URC 跟随器维护的缓存，
		// 网络状态走 URC 路径（见 net.go 的 networkStatusFromURC）。
		fileBacked := mu300FileBackedState(mod)
		// 展锐 SIPC：**一条 AT 都不发**（用户要求接口只读文件）。
		// 信号取自 URC 跟随器维护的缓存，网络状态走 URC + 内核 + 缓存。
		if !fileBacked {
			if _, err := mod.QuerySignal(); err != nil {
				logger.Debug("push query signal failed", zap.Int("modem", mod.ID), zap.Error(err))
				continue
			}
		}
		pushSignal(mod)
		// 流量采样不在这里：它按 traffic_interval（默认 1s）单独跑，见 trafficLoop
		if mod.networkStatusDue(time.Now(), time.Duration(config.Cfg.Server.NetworkInterval)*time.Second) {
			pushNetwork(mod)
		}
	}
	return round
}

// trafficLoop 按 traffic_interval（默认 1s）采样流量计数器并推送上行/下行速度。
//
// 速度是"计数器差分"算出来的：采样越规律、间隔越短，读数越实时 ——
// 所以它不和信号/网络挤同一个节拍，而是单独用一个更短的节拍（默认 1 秒一次）。
// 同样只在有客户端在线时才采样：没人看就不占串口。
func TrafficLoop(mgr *ModemManager, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		if wsHubInst == nil || wsHubInst.CountActive() == 0 {
			continue
		}
		for _, mod := range mgr.listModems() {
			if !mod.isRunningNow() {
				continue
			}
			// ⚠️ 展锐 SIPC（F50）走的是本机网卡计数器（/proc/net/dev），
			// 不占总线，所以它和其它模组一样按配置节拍采样。
			if rate := mod.SampleTraffic(); rate.Supported {
				pushTraffic(mod, rate)
			}
			// 有人看的时候顺带评估一次配额：超限能立刻断，不用等 60 秒的 QuotaLoop
			mod.quotaAfterSample()
		}
	}
}

// logPushLoop 周期把日志文件的新行推给在线页面。
// 文件读取在 config.go（纯 IO、不依赖推送），这里只负责广播。
// 没有客户端时整轮跳过 —— 和其它推送一个原则：有人在看才干活。
func LogPushLoop(path string) {
	p := config.NewPusher(path)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		if wsHubInst == nil || wsHubInst.Count() == 0 {
			continue
		}
		lines := p.ReadNew()
		if len(lines) > 0 {
			broadcast("logs", nil, map[string]any{"file": path, "lines": lines})
		}
	}
}

// ---- from: ws/ws.go ----

// Package ws 是 WebSocket 推送的最小实现（服务端）与广播中心。
//
// 不引第三方库：本机是离线开发（GOPROXY=off），模块缓存里没有 gorilla/websocket，
// 而"把状态推给浏览器"只需要握手 + 文本帧 + ping/pong + close 这几件事。
// 不支持：客户端分片帧、扩展协商（压缩）、子协议 —— 浏览器的小消息也不会用到。
//
// 本包是纯技术层：不认识 Modem，也不认识业务消息格式 ——
// 连上要发什么快照、收到消息怎么处理、短信推送要不要密码，都由调用方通过
// Options 的三个回调决定（接线在 glue.go）。

const (
	// GUID 握手用的固定串（RFC 6455）；导出是为了让上层的握手测试能自行校验 accept 值
	GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	wsMaxFrame     = 1 << 20 // 单个入站帧上限 1MB（防滥用）
	wsWriteTimeout = 5 * time.Second
	wsPingInterval = 30 * time.Second
	wsReadTimeout  = 75 * time.Second // 必须 > ping 间隔：浏览器收到 ping 会自动回 pong
)

// 帧类型（导出：上层要做心跳过滤）
const (
	OpText  = 0x1
	OpBin   = 0x2
	OpClose = 0x8
	OpPing  = 0x9
	OpPong  = 0xA
)
