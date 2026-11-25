package kernel

import (
	"time"
)

// ====================== 推送 + 循环 ======================

func pushQuota(mod *Modem, force bool) {
	if !force && !quotaStoreInst.PushAllowed(mod.ID) {
		return
	}
	view := quotaView(mod)
	id := mod.ID
	broadcast("quota", &id, view)
}

// QuotaLoop 配额采样循环。
//
// ⚠️ 与 PushLoop/TrafficLoop 不同：**不看有没有客户端在线** ——
// 流量配额是"24 小时都得生效"的东西，没人开页面也得按量断网。
// 代价是每轮一条 AT+QGDCNT?（默认 60 秒一次，占用可以忽略）。
// 启动时先跑一轮，把上次退出时"已限制"的状态恢复上（模组可能已经重新拨上了）。
func QuotaLoop(mgr *ModemManager, interval time.Duration) {
	quotaTickAll(mgr)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		quotaTickAll(mgr)
	}
}

func quotaTickAll(mgr *ModemManager) {
	for _, mod := range mgr.listModems() {
		if !mod.isRunningNow() {
			continue
		}
		mod.QuotaTick()
	}
}

// ---- from: quota/quota.go ----

// Package quota 是「流量配额」的纯逻辑与持久化：配置/状态结构、周期（账单月）计算、
// 用量累计与落盘、给前端的视图组装。
//
// 目的：给套餐带流量上限的卡上一道闸 —— 用超了就自动把**数据连接**停掉，
// 避免继续跑流量把套餐跑爆（或降速/限速产生额外费用）。
//
// 计量口径：用模组的累计字节计数器（AT+QGDCNT?，见 data.go）× 我们自己按
// 周期累计。为什么不用模组计数器的原始值当"已用"：
//
//	AT+QGDCNT 只从"使能那一刻"开始计，模组重启/复位就回零，而且没法按"账单周期"对齐；
//	所以用量由本进程累加并**落盘**（quota.json），跨重启不丢。
//
// 断网手段（在 kernel 侧执行）：
//   - block    → 停用 PDP 上下文（AT+CGACT=0,<cid>，3GPP 标准指令）。比 AT+CFUN=4 温和得多：
//     只断数据面，AT 口、短信、注册状态都不动，`AT+CGACT=1,<cid>` 就能恢复。
//   - block_rf → 关射频（AT+CFUN=4，最彻底，但短信也一起停）。
//   - warn     → 只提醒，不动网络。
//
// 发指令、回读确认、循环调度留在 kernel（见 quota.go）。

const (
	ActionBlock   = "block"    // 超出后停用数据连接（QMI：停主机侧拨号器；其它：AT+CGACT=0,<cid>）
	ActionBlockRF = "block_rf" // 超出后直接关射频（AT+CFUN=4，最彻底）
	ActionWarn    = "warn"     // 只提醒，不动网络
)

const (
	// PushEvery 用量推送节流（进度条每 5 秒刷一次足够；状态变化时强制推）
	PushEvery = 5 * time.Second
	// SaveEvery 落盘节流（用量变化很频繁，没必要每次都写文件）
	SaveEvery = 20 * time.Second
	// TryEvery 断网尝试节流：失败后别秒级重试（例如某些 cid 模组不接受），
	// 但也不能不重试（模组/主板会自动重新拨号）。
	TryEvery = 20 * time.Second
	// CheckEvery 已断网后的复查节流：确认没被重新拨上来。
	CheckEvery = 30 * time.Second
)

// QuotaConfig 配额配置（页面可改，落盘保存）
type QuotaConfig struct {
	Enabled    bool   `json:"enabled"`     // 是否启用
	LimitBytes int64  `json:"limit_bytes"` // 上限（字节，0 = 不限量）
	CountUp    bool   `json:"count_up"`    // 计入上行
	CountDown  bool   `json:"count_down"`  // 计入下行
	Action     string `json:"action"`      // ActionBlock / ActionWarn
	ResetDay   int    `json:"reset_day"`   // 每月重置日 1~28；0 = 不自动重置
}

// State 配额运行状态（含配置，一起落盘）
type State struct {
	QuotaConfig

	UsedUp   int64  `json:"used_up"`   // 本周期累计上行（字节）
	UsedDown int64  `json:"used_down"` // 本周期累计下行（字节）
	Period   string `json:"period"`    // 当前周期标识（"2026-09-15" / "manual"）

	Blocked   bool      `json:"blocked"` // 是否已因超量停掉数据连接
	BlockedAt time.Time `json:"blocked_at,omitempty"`
	// BlockedBy 当时用的是哪种断网动作（用户中途换动作时，恢复得按旧动作走）
	BlockedBy   string `json:"blocked_by,omitempty"`
	BlockedCIDs []int  `json:"blocked_cids,omitempty"` // 被我们停用过的 cid（恢复时用）
	// IMEI 仅作参考/迁移用，**不是主键**：实测本机两块 RM502Q-GL 回报的 IMEI 完全一样，
	// 按 IMEI 存会让两个模组共用一份配额（一个超限两个都断）。
	IMEI string `json:"imei,omitempty"`
	// Suspended 本周期内不再限制（用户手动"我知道了，先别断"）。
	// 下一个周期开始 / 手动清零 / 改上限时会自动清掉。
	Suspended bool `json:"suspended,omitempty"`
}

// Used 本周期已用（按配置的方向求和；两个方向分别记，切换方向不丢数据）
func (s *State) Used() int64 {
	var n int64
	if s.CountUp {
		n += s.UsedUp
	}
	if s.CountDown {
		n += s.UsedDown
	}
	return n
}

// Over 是否已达/超上限（未设上限时永远为 false）
func (s *State) Over() bool {
	return s.LimitBytes > 0 && s.Used() >= s.LimitBytes
}

// BlockedAction 恢复时该按哪种动作解除：优先用“当时断网”的那个动作，
// 没有记录（旧数据/手动改过配置）时用当前配置。
func (s State) BlockedAction() string {
	if s.BlockedBy != "" {
		return s.BlockedBy
	}
	return s.Action
}

// View 给前端/推送用的快照（不含内部 cid 细节）
type View struct {
	ModemID   int    `json:"modem_id"`
	Supported bool   `json:"supported"` // 模组是否上报流量计数器（否则配额没法计量）
	Enabled   bool   `json:"enabled"`
	CountUp   bool   `json:"count_up"`
	CountDown bool   `json:"count_down"`
	Action    string `json:"action"`
	ResetDay  int    `json:"reset_day"`

	LimitBytes  int64   `json:"limit_bytes"`
	LimitText   string  `json:"limit_text"`
	UsedBytes   int64   `json:"used_bytes"`
	UsedText    string  `json:"used_text"`
	UsedUp      int64   `json:"used_up"`
	UsedDown    int64   `json:"used_down"`
	RemainBytes int64   `json:"remain_bytes"`
	RemainText  string  `json:"remain_text"`
	Percent     float64 `json:"percent"` // 0~100（未设上限时 0）

	Period      string    `json:"period"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`

	OverLimit bool      `json:"over_limit"`
	Blocked   bool      `json:"blocked"`
	BlockedAt time.Time `json:"blocked_at,omitempty"`
	Suspended bool      `json:"suspended,omitempty"`
}
