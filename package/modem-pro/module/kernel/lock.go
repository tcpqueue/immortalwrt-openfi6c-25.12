package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"fmt"
	"strings"
)

// ---- from: lock.go ----

// ---- from: lock.go ----
//
// 锁网属于射频级操作：把模组限制在指定频段或指定小区上，常用于
//   1. 现场勘察/验收：把模组钉在待测小区，避免乒乓切换；
//   2. 信号比对：逐个频段轮询，比较 RSRP/SINR；
//   3. 排除干扰：屏蔽掉表现差的频段。
//
// 注意：锁定后模组可能短暂脱网重新注册，属于正常现象。
// 锁小区（AT+QNWLOCK）的 lock_type 取值、CellLock / CellLockRequest 等
// 指令层共用的数据结构在 atcmd.go 段（from: atcmd/locktypes.go）里，同包直接用。
// BandLock 频段锁定状态（对应 AT+QNWPREFCFG="lte_band" / "nr5g_band"）
//
// 判据是与设备"允许使用"的 policy 频段（AT+QNWPREFCFG="policy_band"，
// 实测不随 lte_band/nr5g_band 配置变化）比较：
//   - 配置上报 0 = 频段列表为空（禁用全部频段，模组必然脱网）→ 必须判为受限；
//   - policy 中有频段没被启用 → 受限。
//
// 不要拿 ue_capability_band 当基准：它是"当前配置 ∩ policy"，配置成 0 时
// 它同样返回 0，会把"频段被清空、模组脱网"误判成"未受限"。
//
// 锁网属于射频级操作：把模组限制在指定频段或指定小区上，常用于
//   1. 现场勘察/验收：把模组钉在待测小区，避免乒乓切换；
//   2. 信号比对：逐个频段轮询，比较 RSRP/SINR；
//   3. 排除干扰：屏蔽掉表现差的频段。
//
// 注意：锁定后模组可能短暂脱网重新注册，属于正常现象。
// 锁小区（AT+QNWLOCK）的 lock_type 取值、CellLock / CellLockRequest 等
// 指令层共用的数据结构在 atcmd.go 段（from: atcmd/locktypes.go）里，同包直接用。
// BandLock 频段锁定状态（对应 AT+QNWPREFCFG="lte_band" / "nr5g_band"）
//
// 判据是与设备"允许使用"的 policy 频段（AT+QNWPREFCFG="policy_band"，
// 实测不随 lte_band/nr5g_band 配置变化）比较：
//   - 配置上报 0 = 频段列表为空（禁用全部频段，模组必然脱网）→ 必须判为受限；
//   - policy 中有频段没被启用 → 受限。
//
// 不要拿 ue_capability_band 当基准：它是"当前配置 ∩ policy"，配置成 0 时
// 它同样返回 0，会把"频段被清空、模组脱网"误判成"未受限"。

// NSAIndependent 可选能力接口：本方言的 NR **NSA** 有独立的频段列表（返回它的 key；空串 = 没有）。
//
// 已取证的实现（都是"一个 key 一个列表"的写法）：
//   - 移远 / 广和通高通 / 美格等 QNW 系：`AT+QNWPREFCFG="nsa_nr5g_band"`
//   - 芯讯通：`AT+CSYSSEL="nsa_nr5g_band"`（手册 §4.2.20 明列该 key）
//
// **没实现**（或返回空串）的方言只有**一套** NR 频段（广和通 GT 的 AT+GTACT、展锐 SP*、
// 鼎桥 ^LTEFREQLOCK/^NRFREQLOCK、Sierra AT!BAND…）——那些方言下界面上 NR 只出一组，
// 并且发动机层直接拦住对 NRNSA 的查询/下发（见 nsaBandIndependent 的调用点），
// 免得把一条不存在的第二条指令写进真实模组。
//
// NSAIndependent 已搬到 modemd/iface（见 iface/lock.go）；kernel 保留同名别名。

// nsaBandIndependent 本机方言能不能单独锁 NR-NSA
func nsaBandIndependent(l NetLocker) bool {
	if l == nil {
		return false
	}
	x, ok := l.(NSAIndependent)
	return ok && strings.TrimSpace(x.NSABandKey()) != ""
}

// errNoSeparateNSA 一套 NR 列表的方言上，试图单独操作 NSA 时报的错
func errNoSeparateNSA(vendor ModemVendor) error {
	return fmt.Errorf("vendor %s 的 NR 只有一套频段列表（NSA/SA 共用），不能单独设置 NR-NSA；请用 NR", vendor)
}

// LockCapProbe 单条锁网指令的能力探测结果
type LockCapProbe struct {
	Name     string   `json:"name"`
	Command  string   `json:"command"`
	OK       bool     `json:"ok"`                 // 模组是否接受该指令（未回 ERROR）
	Response []string `json:"response,omitempty"` // 模组原始应答
}

// LockCaps 锁网能力探测结果。
// 不同固件对 QNWPREFCFG / QNWLOCK 的支持程度不同，先探测再操作更稳妥。
type LockCaps struct {
	ModemID   int            `json:"modem_id"`
	Vendor    ModemVendor    `json:"vendor"`
	Supported bool           `json:"supported"` // 厂商实现是否支持锁网
	Probes    []LockCapProbe `json:"probes"`
	// Dialect 本机实际选中的锁网方言（运行期探测结果）——
	// 现场排查的第一手信息：同一台设备报 ERROR 时，先看它到底选了哪套方言。
	Dialect      string   `json:"dialect,omitempty"`       // 技术名（qnw/mtk/…）
	DialectName  string   `json:"dialect_name,omitempty"`  // 中文显示名
	DialectNote  string   `json:"dialect_note,omitempty"`  // 选中理由 / 回退说明
	DialectTried []string `json:"dialect_tried,omitempty"` // 探测记录（方言: 接受/拒绝）
}
