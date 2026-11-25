package bands

import (
	"modemd/iface"
	"time"
)

type BandLock struct {
	Tech     iface.CellTech `json:"tech"`      // LTE / NR
	Bands    []int          `json:"bands"`     // 模组当前配置的频段列表
	Capable  []int          `json:"capable"`   // 设备允许使用的频段 policy（判断是否受限的基准）
	Locked   bool           `json:"locked"`    // 是否确实屏蔽了设备支持的频段
	RawValue string         `json:"raw_value"` // 模组原始上报值，便于人工核对
	// CapableNote 「设备能力」拿不到时的说明（如鼎桥 MT5700 不上报 NR 频段能力）。
	// 有它时界面显示这句说明，而不是光一个 `-` —— 用户至少知道“不是没查，是固件不报”。
	CapableNote string `json:"capable_note,omitempty"`

	// Declared 清单（QModem 频段表，见 modem_bands.go）声明本型号支持的该制式频段。
	//
	// 与 Capable 的区别：Capable 是**运行期问模组**（policy_band，固件不报就只有 -），
	// Declared 是**出厂口径**（型号级快照）。两者不一致（固件被裁频段、或上游写错）
	// 正是排查时最想看的信息，所以两个都展示，不互相覆盖。
	Declared []int `json:"declared,omitempty"`
	// DeclaredNote 清单口径的说明（为空时写清楚是“上游没这个东西”还是“上游没写频段”）
	DeclaredNote string `json:"declared_note,omitempty"`
}

// LockStatus 锁频段 / 锁基站 / 组网模式 的完整状态
type LockStatus struct {
	ModemID   int            `json:"modem_id"`
	QueriedAt time.Time      `json:"queried_at"`
	ModePref  string         `json:"mode_pref,omitempty"` // 组网模式偏好：AUTO / LTE / NR5G / LTE:NR5G
	BandLTE   BandLock       `json:"band_lte"`
	BandNR    BandLock       `json:"band_nr"`
	CellLTE   iface.CellLock `json:"cell_lte"`
	CellNR    iface.CellLock `json:"cell_nr"`
	// Note 该机型特有的操作提示（如“锁下发后需切一次飞行模式才生效”），只有厂商提供时才有
	Note string `json:"note,omitempty"`
	// Cmds 该机型**实际使用**的锁网指令（键：band_lte / band_nr / cell_lte / cell_nr / mode /
	// unlock_lte / unlock_nr / save）。界面上的提示按它渲染 —— 以前把移远的
	// AT+QNWPREFCFG / AT+QNWLOCK 写死在页面文案里，换成鼎桥 MT5700 后那些说明就是错的。
	Cmds map[string]string `json:"cmds,omitempty"`
	// Dialect 本机实际选中的锁网方言（技术名 / 显示名 / 说明）。
	// 现场排查"这台为什么回 ERROR"时，先看这里。——同一平台不同厂商、甚至同一型号不同固件，
	// 方言都可能不同，所以这一项只能由运行期探测得出。
	Dialect     string `json:"dialect,omitempty"`
	DialectName string `json:"dialect_name,omitempty"`
	// Bands 本型号的四组支持频段（清单口径：WCDMA / LTE / NR-NSA / NR-SA），
	// 界面上的「按模块支持自动排版 + 勾选」就是按它（+ 运行期的 capable）分组的。
	Bands       *ModemBands `json:"bands,omitempty"`
	DialectNote string      `json:"dialect_note,omitempty"`
	// Groups 锁频段页要渲染的分组（清单 ∪ 设备上报，自动排版；规则见 modem_bands.go 的 BandGroups）。
	//
	// ⚠️ 字段名不能叫 BandGroups —— 与 LockStatus.BandGroups() 方法同名会编译不过；
	// ⚠️ 这个字段**必须由 queryLockStatus 赋值**：前端只读 JSON 的 `band_groups`，
	// 漏掉赋值不会编译报错，只表现为"所有机型的锁频段页永远没有勾选框"（2026-10-06 真机踩过）。
	Groups []BandGroup `json:"band_groups,omitempty"`
	// BandNRNSA NR **NSA**（EN-DC）的独立频段状态。
	// 只有"NR 分 NSA / SA 两套列表"的方言才填（见 NSAIndependent / SeparateNSA）——
	// 一套列表的方言（广和通 GT / 展锐 / 鼎桥…）留空，界面只出 NR 一组。
	BandNRNSA BandLock `json:"band_nr_nsa,omitempty"`
	// SeparateNSA 本机方言的 NR 是**两套**列表（NSA / SA 可分别锁）还是**一套**（NSA＝SA 共用）。
	SeparateNSA bool `json:"separate_nsa,omitempty"`
}
