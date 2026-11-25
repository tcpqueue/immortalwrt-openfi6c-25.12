package lock

import (
	"strconv"
	"strings"
)

// ====================== 通用「列表式」锁频段方言 ======================
//
// telit / thales / foxconn / neoway 的锁频段都是"给某制式一个频段列表"，只是指令名与参数位置不同
// （依据 QModem 的 `cmds/*.sh`）：
//
//	telit   : AT#BND=0,22,<b1,b2,…>              查询 AT#BND?
//	thales  : AT+BAND_PREF=<RAT>,2,<b1,b2,…>      查询 AT+BAND_PREF?   （RAT：LTE / NR5G）
//	foxconn : AT^BAND_PREF=<RAT>,2,<b1,b2,…>      查询 AT^BAND_PREF?   （foxconn 前缀 AT+/AT^ 因机型而异，这里用 AT^）
//	neoway  : AT+NWSETBAND=<act>,<个数>,<b1,b2,…> 查询 AT+NWSETBAND?   复位 AT+NWSETBAND=0
//
// ⚠️ 共同限制（QModem 脚本里也一样）：
//   - 这批厂商**没有锁小区指令** → 锁小区不可用（空串 = 上层明确报"不支持"，不下发）；
//   - 没有 policy 频段 → 无法用 QNW 那套“当前值 vs policy”判是否受限；
//   - 解锁格式未确认 → 返回空串，由上层如实报"未提供解锁指令"，不瞎发。
//
// 状态解析（2026-10 补）：thales / foxconn / neoway 的应答格式已从 QModem 的
// vendor/*.sh 里拿到**逐字样例**，因此这三家现在能解析；telit 的 AT#BND? 字段含义
// （哪个字段才是十六进制位图）仍未确认，继续保持保守（只显示原始应答）。
//
// 只有探测被固件接受（查询返回带 `:` 的应答头）时才会被选中；否则退回厂商/平台默认。

type BandListLocker struct {
	name    string
	capsCmd string
	note    string
	queryFn func(tech CellTech) string
	setFn   func(tech CellTech, bands []int) string
	// parseStateFn 解析"当前锁定"应答（各家格式不同）；nil = 格式未确认，保守不解析
	parseStateFn func(tech CellTech, resp []string) ([]int, bool)
	// capFn 读"固件认可的频段池"（界面「设备能力」列）；nil = 拿不到
	capFn func(at LockATDoer) map[CellTech][]int
}

// NewBandListLocker 构造一个"列表式"锁频段方言。
//
// 为什么要有构造函数：本结构体的字段**故意不导出**（导出之后本包所有
// `BandListLocker{name: …}` 构造点都得跟着改，见 dialect.go 的说明）。
// 方言实现已经按品牌搬进 module/<品牌>/lock.go，跨包构造只能走这里。
func NewBandListLocker(name, capsCmd string,
	queryFn func(CellTech) string,
	setFn func(CellTech, []int) string,
	parseStateFn func(CellTech, []string) ([]int, bool),
	capFn func(LockATDoer) map[CellTech][]int,
	note string) *BandListLocker {
	return &BandListLocker{
		name:         name,
		capsCmd:      capsCmd,
		note:         note,
		queryFn:      queryFn,
		setFn:        setFn,
		parseStateFn: parseStateFn,
		capFn:        capFn,
	}
}

// DialectName 该方言的名字（界面显示"当前用的是哪套方言"）
func (b *BandListLocker) DialectName() string { return b.name }

// ---------- NetLocker ----------

func (b *BandListLocker) CommandQueryBandLock(tech CellTech) string { return b.queryFn(tech) }

func (b *BandListLocker) CommandSetBandLock(tech CellTech, bands []int) string {
	if len(bands) == 0 || b.setFn == nil || tech == CellTechNRNSA {
		return "" // 这批厂商只有一套 NR 列表（见 NSAIndependent）
	}
	return b.setFn(tech, bands)
}

func (b *BandListLocker) CommandQueryBandCapability(CellTech) string { return "" }
func (b *BandListLocker) CommandQueryPolicyBand() string             { return "" }

// CommandQueryCellLock / CommandLockCell 这批厂商无锁小区指令
func (b *BandListLocker) CommandQueryCellLock(CellTech) string   { return "" }
func (b *BandListLocker) CommandLockCell(CellLockRequest) string { return "" }
func (b *BandListLocker) CommandBandLockCaps() string            { return b.capsCmd }
func (b *BandListLocker) CommandCellLockCaps() string            { return "" }
func (b *BandListLocker) CommandQueryModePref() string           { return "" }
func (b *BandListLocker) CommandSetModePref(string) string       { return "" }

// ---------- LockVendorOps ----------

// ParseBandLockState 解析当前锁定；该方言未确认格式时保守返回 false（界面显示原始应答）
func (b *BandListLocker) ParseBandLockState(tech CellTech, resp []string) ([]int, bool) {
	if b.parseStateFn == nil {
		return nil, false
	}
	return b.parseStateFn(tech, resp)
}

// ParseCellLockState 无锁小区 → 永远"未锁定"
func (b *BandListLocker) ParseCellLockState(CellTech, []string) (CellLock, bool) {
	return CellLock{}, true
}

// CommandUnlockBand 解锁格式未确认 → 空串（上层报"未提供解锁指令"，不发未验证指令）
func (b *BandListLocker) CommandUnlockBand(CellTech) string { return "" }

func (b *BandListLocker) ParseModePref([]string) (string, bool) { return "", false }
func (b *BandListLocker) SetModePrefVendor(LockATDoer, string) (bool, error) {
	return false, nil
}
func (b *BandListLocker) CommandSaveCellLock() string { return "" }

// QueryBandCapability 读"固件认可的频段池"（thales/foxconn 从 AT+BAND_PREF? 的
// Enable ∪ Disable 汇总，neoway 从 AT+NWSETBAND=?），拿不到时返回 nil。
func (b *BandListLocker) QueryBandCapability(at LockATDoer) map[CellTech][]int {
	if b.capFn == nil {
		return nil
	}
	return b.capFn(at)
}

func (b *BandListLocker) BandCapabilityNote(CellTech) string {
	if b.capFn == nil {
		return "该固件不上报设备能力频段"
	}
	return "该固件的设备能力频段要靠 AT 查询自报（拿不到时这里显示 -）"
}

func (b *BandListLocker) StatusNote() string { return b.note }

// ---------- 各厂商实例 ----------

func JoinInts(bands []int, sep string) string {
	parts := make([]string, 0, len(bands))
	for _, b := range bands {
		parts = append(parts, strconv.Itoa(b))
	}
	return strings.Join(parts, sep)
}

// telit 已单独立（lock_telit.go）：它的尾参是**十六进制位图**、且 NR 写入要带 LTE 两段，
// 用不了这里的"给个十进制列表"模式。
