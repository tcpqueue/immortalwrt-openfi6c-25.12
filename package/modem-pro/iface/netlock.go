package iface

// 本文件放「锁网」相关的能力接口。
//
// 锁网指令各家差异很大（移远 QNWPREFCFG/QNWLOCK、广和通与芯讯通另有指令集、
// 鼎桥 MT5700 完全不同的一套），所以做成**可选能力接口**：
// 实现了才支持，未实现时上层返回明确的"不支持"，
// 而不是发出错误指令把模组搞乱。

// NetLocker 可选的"锁网"能力接口。
//
// 锁网指令各家差异很大（移远 QNWPREFCFG/QNWLOCK、广和通与芯讯通另有指令集），
// 因此单独作为可选接口：实现了该接口的厂商才支持锁网，
// 未实现时上层返回明确的"不支持"，而不是发出错误指令把模组搞乱。
type NetLocker interface {
	// CommandQueryBandLock 查询指定技术域的频段锁定
	CommandQueryBandLock(tech CellTech) string
	// CommandSetBandLock 设置频段锁定；bands 为要启用的频段列表。
	// 不接受空列表：解除限制请走 Modem.UnlockBand，不要下发 0
	// （写 0 会把频段清空导致模组脱网，详见 QuectelCommand.CommandSetBandLock）
	CommandSetBandLock(tech CellTech, bands []int) string
	// CommandQueryBandCapability 查询设备能力频段（当前配置 ∩ 允许频段，仅用于展示）
	CommandQueryBandCapability(tech CellTech) string
	// CommandQueryPolicyBand 查询设备"允许使用"的频段（policy）。
	// 该应答不随 lte_band/nr5g_band 的配置变化，是判断是否受限、
	// 以及解除限制时恢复全量频段的可靠来源；返回空串表示固件不支持。
	CommandQueryPolicyBand() string
	// CommandQueryCellLock 查询指定技术域的小区锁定
	CommandQueryCellLock(tech CellTech) string
	// CommandLockCell 锁定/解除小区锁定
	CommandLockCell(req CellLockRequest) string
	// CommandBandLockCaps 探测频段锁定指令支持情况（AT+QNWPREFCFG=?）
	CommandBandLockCaps() string
	// CommandCellLockCaps 探测小区锁定指令支持情况（AT+QNWLOCK=?）
	CommandCellLockCaps() string
	// CommandQueryModePref 查询组网模式偏好
	CommandQueryModePref() string
	// CommandSetModePref 设置组网模式偏好（AUTO/LTE/NR5G/LTE:NR5G）
	CommandSetModePref(mode string) string
}

// LockVendorOps 可选能力：锁网里"各家格式完全不同"的几个环节。
//
// 移远 / 广和通 / 芯讯通用 AT+QNWPREFCFG + AT+QNWLOCK 一套，由 lock.go 的通用流程处理；
// 鳤桥 MT5700 用 AT^LTEFREQLOCK / AT^NRFREQLOCK / AT^SYSCFGEX（实现见 module/tdtech/command.go）：
// 应答是三段式、三种锁共用同一条指令、解锁是 operatetype=0、组网模式必须"先回读再整串改写"。
// 因此把这些环节交给厂商实现 —— 不实现该接口的厂商一点不受影响。
type LockVendorOps interface {
	// ParseBandLockState 解析频段锁定应答 → 被限制的频段列表（未限制 ok=false）
	ParseBandLockState(tech CellTech, resp []string) (bands []int, locked bool)
	// ParseCellLockState 解析小区/频点锁定应答
	ParseCellLockState(tech CellTech, resp []string) (CellLock, bool)
	// CommandUnlockBand 解除该技术域的频段锁定；空串 = 不支持（上层改走 policy 频段恢复）
	CommandUnlockBand(tech CellTech) string
	// ParseModePref 解析组网模式偏好应答；ok=false = 格式不认，走通用解析
	ParseModePref(resp []string) (string, bool)
	// SetModePrefVendor 设置组网模式（需要先回读再改写时用）；
	// handled=false = 没处理，上层走 CommandSetModePref 的通用写法
	SetModePrefVendor(at LockATDoer, mode string) (handled bool, err error)
	// CommandSaveCellLock 小区锁定后持久化的附加指令；空串 = 无需下发（锁定本身就掉电保存）
	CommandSaveCellLock() string
	// QueryBandCapability 该机型"设备支持的频段"（界面上的「设备能力」列）。
	// 需要自己发指令（各家的查询方式不同），返回 nil 表示该机型拿不到
	QueryBandCapability(at LockATDoer) map[CellTech][]int
	// BandCapabilityNote 「设备能力」拿不到时的说明；空串 = 不解释（就显示 -）
	BandCapabilityNote(tech CellTech) string
	// StatusNote 该机型锁网的额外提示（会显示在锁网卡片下方）；空串 = 没有
	StatusNote() string
}
