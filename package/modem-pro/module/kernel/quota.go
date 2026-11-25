package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"modemd/module/config"
	"time"
)

// ---- from: modem_quota.go ----

// ---- from: modem_quota.go ----
//
// 配置/状态结构、周期计算、用量落盘、视图组装在 quota.go（纯逻辑）；
// 这里管"要发哪些 AT、什么时候断/恢复、怎么推给前端"。
//
// ⚠️ 与其它循环不同：配额**必须在前端没连推送时也生效**（没人看也得按量断网），
// 所以它单独有一个 QuotaLoop，不依赖 Hub 有没有客户端。
//
// 配置/状态结构、周期计算、用量落盘、视图组装在 quota.go（纯逻辑）；
// 这里管"要发哪些 AT、什么时候断/恢复、怎么推给前端"。
//
// ⚠️ 与其它循环不同：配额**必须在前端没连推送时也生效**（没人看也得按量断网），
// 所以它单独有一个 QuotaLoop，不依赖 Hub 有没有客户端。

var quotaStoreInst = NewQuotaStore()

// InitQuota 载入配额文件（main 启动时调用一次）。
// 文件放在配置文件同目录，与日志/PID 一样属于"运行时状态"，不是 toml 的一部分。
func InitQuota() {
	quotaStoreInst.Init(QuotaPath(config.ConfigPath))
}

// quotaKey 记录的键（端口为主；见 quota.go 的 Key 说明）
func quotaKey(mod *Modem) string { return Key(mod.ATPort, mod.ID) }

// quotaState 只读快照
func quotaState(mod *Modem) State {
	return quotaStoreInst.Get(quotaKey(mod), mod.IMEI)
}

// quotaUpdate 在锁内改状态
func quotaUpdate(mod *Modem, fn func(st *State)) State {
	return quotaStoreInst.Update(quotaKey(mod), mod.IMEI, fn)
}

// quotaAddBytes 累计一次采样增量（由 applyTrafficSample 调用 —— 全进程只有那一处
// 在算"计数器差分"，所以不会重复计数）
func quotaAddBytes(mod *Modem, up, down uint64) {
	quotaStoreInst.AddBytes(quotaKey(mod), mod.IMEI, up, down)
}

// FlushQuota 强制落盘（状态变化后 / 进程退出前调用）
func FlushQuota() { quotaStoreInst.Flush() }

// trafficSupported 模组是否能统计流量（没声明计数器指令、也没有本机接口计数器的就没法计量）
func (mod *Modem) trafficSupported() bool {
	// 展锐 SIPC（F50）：厂商托管数据面本机就是一张网卡，读写计数在 /proc/net/dev，
	// 不依赖模组任何 AT 指令 ⇒ 配额照样能计量（以前这里恒为 false）。
	if mu300FileBackedState(mod) && mu300DataIface() != "" {
		return true
	}
	return mod.resolveVendorCmd(KindTraffic) != ""
}

// quotaView 组装给前端的快照（组装逻辑在 quota.go 的 BuildView）
func quotaView(mod *Modem) View {
	return BuildView(quotaState(mod), mod.ID, mod.trafficSupported(), time.Now())
}
