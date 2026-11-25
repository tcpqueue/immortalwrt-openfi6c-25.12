package iface

// 本文件放「锁网指令的入参与结果」这类纯类型，以及锁网相关的两个可选能力接口。
//
// 它们必须住在契约层：厂商实现（各家的锁频方言）与引擎（module/kernel/lock.go）都要用，
// 而厂商实现不能反向依赖引擎。

// ---- 小区锁定：与界面的三个选项一一对应 ----

const (
	CellLockNone     = 0 // 解除锁定
	CellLockARFCNPCI = 1 // 同时锁定频点与 PCI（钉死某一个小区的唯一组合）
	CellLockARFCN    = 2 // 仅锁定频点（该频点上的任意小区均可驻留）
)

// CellLock 小区/频点锁定状态
type CellLock struct {
	Tech     CellTech `json:"tech"`      // LTE / NR
	LockType int      `json:"lock_type"` // 0=未锁定 1=锁频点+PCI 2=仅锁频点
	ARFCN    int      `json:"earfcn"`    // 频点号（LTE EARFCN / NR ARFCN）
	PCID     int      `json:"pcid"`      // 物理小区 ID（仅 lock_type=1 有效）
	Locked   bool     `json:"locked"`
	RawValue string   `json:"raw_value"` // 模组原始上报值，便于人工核对
}

// CellLockRequest 一次小区锁定请求（由 HTTP API 传入）
type CellLockRequest struct {
	Tech     CellTech // LTE / NR
	ARFCN    int      // 频点号；LockType=CellLockNone 时忽略
	PCID     int      // 物理小区 ID（LockType=CellLockARFCNPCI 时必填）
	LockType int      // CellLockNone / CellLockARFCNPCI / CellLockARFCN
	SCS      int      // NR 子载波间隔，可填 kHz(15/30/60) 或索引(0/1/2)；0 表示默认 30kHz
}

// Band 由频点推算频段（写指令时要用频段号，例如鼎桥的 `=2,0,1,"78",627264,1,277`）
func (r CellLockRequest) Band() int {
	return BandFromEARFCN(r.Tech, r.ARFCN)
}

// NormalizeSCS 归一化 NR 子载波间隔：接受 kHz(15/30/60/120) 或索引(0/1/2)，
// 返回手册里的索引写法（0=15kHz 1=30kHz 2=60kHz）。非法/未填按 30kHz。
func NormalizeSCS(v int) int {
	switch v {
	case 0:
		return 1 // 未填 → 默认 30kHz
	case 15, 30, 60, 120:
		return map[int]int{15: 0, 30: 1, 60: 2, 120: 3}[v]
	}
	if v >= 1 && v <= 3 {
		return v
	}
	return 1
}

// BandFromEARFCN 由频点号推算频段号（模组未直接上报 band 时使用），
// 依 3GPP TS 36.101（LTE）/ TS 38.104（NR）常用频段范围。未知返回 0。
func BandFromEARFCN(tech CellTech, earfcn int) int {
	if earfcn <= 0 {
		return 0
	}
	if tech == CellTechNR {
		switch {
		case earfcn >= 422000 && earfcn <= 431999:
			return 1 // n1 2100MHz
		case earfcn >= 361000 && earfcn <= 376000:
			return 3 // n3 1800MHz
		case earfcn >= 173800 && earfcn <= 178800:
			return 5 // n5 850MHz
		case earfcn >= 185000 && earfcn <= 192000:
			return 8 // n8 900MHz
		case earfcn >= 151600 && earfcn <= 160600:
			return 28 // n28 700MHz
		case earfcn >= 499200 && earfcn <= 537999:
			return 41 // n41 2.6GHz
		case earfcn >= 620000 && earfcn <= 653333:
			return 78 // n78 3.5GHz
		case earfcn >= 620000 && earfcn <= 680000:
			return 77 // n77 3.3-4.2GHz
		case earfcn >= 693334 && earfcn <= 733333:
			return 79 // n79 4.9GHz
		}
		return 0
	}
	switch {
	case earfcn >= 0 && earfcn <= 599:
		return 1
	case earfcn >= 600 && earfcn <= 1199:
		return 2
	case earfcn >= 1200 && earfcn <= 1949:
		return 3
	case earfcn >= 1950 && earfcn <= 2399:
		return 4
	case earfcn >= 2400 && earfcn <= 2649:
		return 5
	case earfcn >= 2750 && earfcn <= 3449:
		return 7
	case earfcn >= 3450 && earfcn <= 3799:
		return 8
	case earfcn >= 6150 && earfcn <= 6449:
		return 20
	case earfcn >= 9210 && earfcn <= 9659:
		return 28
	case earfcn >= 36200 && earfcn <= 36349:
		return 34
	case earfcn >= 37750 && earfcn <= 38249:
		return 38
	case earfcn >= 38250 && earfcn <= 38649:
		return 39
	case earfcn >= 38650 && earfcn <= 39649:
		return 40
	case earfcn >= 39650 && earfcn <= 41589:
		return 41
	case earfcn >= 41590 && earfcn <= 43589:
		return 42
	case earfcn >= 43590 && earfcn <= 45589:
		return 43
	}
	return 0
}

// NSAIndependent 可选能力：本方言的 NR 是否有**独立的 NSA 频段列表**。
//
// 只有"NR 分成 NSA / SA 两套列表"的方言（移远/高通 QNW：`AT+QNWPREFCFG="nsa_nr5g_band"`）
// 才认 CellTechNRNSA。一套列表的方言（广和通 GT / 展锐 SP* / 鼎桥 ^LTEFREQLOCK·^NRFREQLOCK /
// Sierra AT!BAND…）下界面上 NR 只出一组，并且发动机层直接拦住对 NRNSA 的查询/下发
// （见 kernel lock.go 的 nsaBandIndependent 调用点），
// 免得把一条不存在的第二条指令写进真实模组。
//
// ⚠️ 用"返回值"而不是"标记方法"是故意的：`fibocomMTKCommand` 嵌了 `FibocomCommand`，
// 标记方法没法被"取消继承"，而返回值可以直接覆盖成空串。
type NSAIndependent interface {
	// NSABandKey 本方言独立的 NR-NSA 频段 key（如 `nsa_nr5g_band`）；空串 = 本机只有一套 NR 频段
	NSABandKey() string
}

// BandLockSetter 可选能力：锁频段需要"先回读再整串改写"（单条指令拼不出来）的机型。
//
// 典型是广和通 MTK 平台的 AT+GTACT：命令里含驻留制式 <rat> 与另一制式的频段，
// 只能回读后整串改写。这类机型的 CommandSetBandLock 返回空串，由本接口自己完成下发。
type BandLockSetter interface {
	// SetBandLockAt 锁定频段（自己回读、自己拼整串、自己下发）
	SetBandLockAt(at LockATDoer, tech CellTech, bands []int) error
	// UnlockBandAt 解除该技术域的频段锁定
	UnlockBandAt(at LockATDoer, tech CellTech) error
}
