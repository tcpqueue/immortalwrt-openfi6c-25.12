package quectel

import (
	"fmt"
	"strings"

	"modemd/iface"
	"modemd/module/catalog"
	"modemd/module/common"
	"modemd/module/platform"
)

// ---- 本包用到的「契约」的本地别名 ----
//
// 搬过来的代码**原样可用**，不必满屏写 `iface.` 前缀。
// 这与 kernel 那边的兼容别名是同一手法，只是方向相反。

type CellTech = iface.CellTech

const (
	CellTechLTE   = iface.CellTechLTE
	CellTechNR    = iface.CellTechNR
	CellTechNRNSA = iface.CellTechNRNSA
)

type CellLockRequest = iface.CellLockRequest

const (
	CellLockNone     = iface.CellLockNone
	CellLockARFCNPCI = iface.CellLockARFCNPCI
	CellLockARFCN    = iface.CellLockARFCN
)

// 跨包只读依赖：型号清单 / 平台判定 / 频段列表格式化 / SCS 归一化
var (
	CatalogEntryForModel = catalog.CatalogEntryForModel
	PlatformForModel     = platform.PlatformForModel
	FormatBandList       = common.FormatBandList
	normalizeSCS         = iface.NormalizeSCS
)

const PlatformUnisoc = platform.PlatformUnisoc

// ====================== 高通 QNW 系锁网指令（移远 / 广和通高通平台共用） ======================
//
// 这一套是**高通标准**的锁网指令：移远与广和通（高通平台）**字符级完全一样**
// （实测原来 atcmd.go 里这段被抄了两遍：10 个方法、含 16 行的 CommandLockCell 与 5 行的
// CommandSetBandLock ⇒ 约 50 行逐字重复）。现在只存在一份，两家**嵌入**即可：
//
//	type QuectelCommand struct{ common.StdCommands; quectel.QnwLockCommands }
//
// 引擎侧（kernel lock.go）的通用流程会调用它们，解析用 parseQNWPREFCFG / parseQNWLOCK，
// **不需要** LockVendorOps（没有鼎桥那种三段式应答）。
//
// ⚠️ 广和通的另两个平台固件**不认**这一套（`AT+QNWPREFCFG` 回 ERROR）：
//   - FM350-GL（MTK，固件 81600.…）——手册里只有 +GTACT / +EPBSEH 的频段选择，没有锁基站指令；
//   - FM650-CN（展锐）——同样不认 QNW。
//
// 这些机型下单前会被「探测固件支持」（ProbeLockCaps / GET /api/lock/caps）拦下，
// 不会把未验证的指令写进真实模组。
type QnwLockCommands struct {
	// CellProfile 本机的锁小区平台档（上游 vendor/quectel.sh 的 lockcell_qualcomm/unisoc/lte）。
	// 零值 = 高通（默认）；由 kernel 的 applyCellProfile 在构造时按型号填（见 SetCellProfile）。
	//
	// ⚠️ 原本是未导出字段 `cellProfile`；搬出 kernel 后必须导出，
	// 否则 kernel 的测试读不到它（字段没法用别名救）。
	CellProfile CellLockProfile
}

// CellLockProfile 移远锁小区的平台档
//
// 上游 vendor_quectel.sh 分了三个函数（逐字照抄）：
//
//	lockcell_qualcomm：LTE AT+QNWLOCK="common/4g",1,<arfcn>,<pci>；NR "common/5g",<pci>,<arfcn>,<scs>,<band>
//	lockcell_unisoc  ：LTE "common/lte",1,<arfcn>,<pci>；NR "common/5g",1,<arfcn>,<pci>（参数序不同！）
//	lockcell_lte     ：只有 "common/lte"，1=只锁频点（pci 位写 0）、2=频点+PCI
//
// ⚠️ 以前只有一种（common/4g 那套）⇒ 展锐/旧款 4G 上发的指令固件不认。
type CellLockProfile int

const (
	CellQualcomm  CellLockProfile = iota // 高通（默认）
	CellUnisoc                           // 展锐（RM500U/RG200U…）
	CellLegacyLTE                        // 旧款 4G（EC20/EC25…）
)

// CellLockProfileForModel 按模块清单的平台判档（清单优先，其次型号前缀表）
func CellLockProfileForModel(model string) CellLockProfile {
	model = strings.TrimSpace(model)
	if model != "" {
		if e, ok := CatalogEntryForModel(model); ok {
			switch strings.ToLower(strings.TrimSpace(e.Platform)) {
			case "unisoc":
				return CellUnisoc
			case "lte", "lte12":
				return CellLegacyLTE
			}
			return CellQualcomm
		}
	}
	if PlatformForModel(model) == PlatformUnisoc {
		return CellUnisoc
	}
	return CellQualcomm
}

// SetCellProfile 由 kernel 的 applyCellProfile 调用；内嵌 QnwLockCommands 的 locker
// （移远 QNW、旧款 QCFG band…）会自动获得这个方法
func (q *QnwLockCommands) SetCellProfile(p CellLockProfile) { q.CellProfile = p }

// BandKey 频段锁定的 key 名（QNWPREFCFG 用 lte_band / nsa_nr5g_band / nr5g_band）
func BandKey(tech CellTech) string {
	switch tech {
	case CellTechNR:
		return "nr5g_band"
	case CellTechNRNSA:
		return "nsa_nr5g_band"
	}
	return "lte_band"
}

// LockKind 小区锁定子域（NSA 与 SA 共用同一套 5G 锁小区指令）。
//
// ⚠️ 与下面的方法 lockKind 不同：这个是"纯按技术域"的通用版，
// 方法版还会按 CellProfile 换成 common/lte。
func LockKind(tech CellTech) string {
	if tech == CellTechNR || tech == CellTechNRNSA {
		return "common/5g"
	}
	return "common/4g"
}

// lockKind 本机锁小区子域（NSA 与 SA 共用 5G 那一套，并考虑平台档）
func (q QnwLockCommands) lockKind(tech CellTech) string {
	isNR := tech == CellTechNR || tech == CellTechNRNSA
	if q.CellProfile == CellUnisoc || q.CellProfile == CellLegacyLTE {
		if isNR {
			return "common/5g"
		}
		return "common/lte"
	}
	if isNR {
		return "common/5g"
	}
	return "common/4g"
}

// CommandQueryBandLock 查询频段锁定：AT+QNWPREFCFG="lte_band"/"nr5g_band"。
func (QnwLockCommands) CommandQueryBandLock(tech CellTech) string {
	return fmt.Sprintf(`AT+QNWPREFCFG="%s"`, BandKey(tech))
}

// CommandQueryBandCapability 设备能力频段（当前配置 ∩ 允许频段）。
// 该指令一次返回 gw/lte/nsa_nr5g/nr5g 多个 key 的值，按 tech 取对应那一行。
// 仅用于展示；判断"是否受限"必须用 policy_band，见 CommandQueryPolicyBand。
func (QnwLockCommands) CommandQueryBandCapability(tech CellTech) string {
	return `AT+QNWPREFCFG="ue_capability_band"`
}

// CommandQueryPolicyBand 查询设备"允许使用"的频段（policy_band）。
// 实测该应答与 lte_band/nr5g_band 的当前配置无关（把配置清成 0 后仍返回全量），
// 因此可以安全地作为"解除限制"时写回的目标值。
func (QnwLockCommands) CommandQueryPolicyBand() string {
	return `AT+QNWPREFCFG="policy_band"`
}

// CommandSetBandLock 设置频段锁定。bands 为空时返回空串（不产生任何指令）。
//
// 千万不要用 AT+QNWPREFCFG="lte_band",0 / "nr5g_band",0 来"解锁"：
// 实测 RM502Q-GL（fw RM502QGLAAR11A02M4G）写入 0 后并不会恢复全量频段，
// 而是把频段列表清空（= 禁用全部频段），模组随即彻底脱网：
//
//	AT+QNWPREFCFG="lte_band",0  → 读回 0，ue_capability_band 的 lte_band 也变 0
//	AT+QCSQ                      → +QCSQ: "NOSERVICE"
//	AT+CREG?                     → +CREG: 0,2（一直搜网但搜不到）
//	AT+QENG="servingcell"        → 恒为 "SEARCH"
//
// 现象就是基站扫描（/api/cells）永远返回空列表。
// 解除限制请用 Modem.UnlockBand：先查 policy_band，再把该列表写回。
func (QnwLockCommands) CommandSetBandLock(tech CellTech, bands []int) string {
	if len(bands) == 0 {
		return ""
	}
	return fmt.Sprintf(`AT+QNWPREFCFG="%s",%s`, BandKey(tech), FormatBandList(bands))
}

// CommandQueryCellLock 查询小区锁定：AT+QNWLOCK="common/4g"/"common/5g"/"common/lte"（按平台档）。
func (q QnwLockCommands) CommandQueryCellLock(tech CellTech) string {
	return fmt.Sprintf(`AT+QNWLOCK="%s"`, q.lockKind(tech))
}

// CommandLockCell 构造小区锁定指令。
//
// 实测 RM502Q-GL（fw RM502QGLAAR11A02M4G）的 AT+QNWLOCK=? 签名：
//
//	+QNWLOCK: "common/4g",(0-10),<freq>,<pci>
//	+QNWLOCK: "common/5g",<pci>,<freq>,<scs>,<band>
//
// 展锐/旧款 4G 平台的形式不同，见 CellLockProfile（上游 lockcell_unisoc / lockcell_lte）。
func (q QnwLockCommands) CommandLockCell(req CellLockRequest) string {
	isNR := req.Tech == CellTechNR || req.Tech == CellTechNRNSA
	kind := q.lockKind(req.Tech)
	if req.LockType == CellLockNone {
		return fmt.Sprintf(`AT+QNWLOCK="%s",0`, kind)
	}
	switch q.CellProfile {
	case CellLegacyLTE:
		// 旧款 4G（上游 lockcell_lte）：1=只锁频点（pci 位写 0）、2=频点+PCI
		if req.LockType == CellLockARFCN || req.PCID == 0 {
			return fmt.Sprintf(`AT+QNWLOCK="common/lte",1,%d,0`, req.ARFCN)
		}
		return fmt.Sprintf(`AT+QNWLOCK="common/lte",2,%d,%d`, req.ARFCN, req.PCID)
	case CellUnisoc:
		// 展锐（上游 lockcell_unisoc）：LTE 与 NR 都是 <1>,<arfcn>,<pci>（**频点在前**）
		return fmt.Sprintf(`AT+QNWLOCK=%q,1,%d,%d`, kind, req.ARFCN, req.PCID)
	}
	if isNR {
		return fmt.Sprintf(`AT+QNWLOCK="common/5g",%d,%d,%d,%d`,
			req.PCID, req.ARFCN, normalizeSCS(req.SCS), req.Band())
	}
	switch req.LockType {
	case CellLockARFCN:
		return fmt.Sprintf(`AT+QNWLOCK="common/4g",2,%d`, req.ARFCN)
	default:
		return fmt.Sprintf(`AT+QNWLOCK="common/4g",1,%d,%d`, req.ARFCN, req.PCID)
	}
}

// CommandBandLockCaps 频段锁定能力探测（只读）：AT+QNWPREFCFG=?。
func (QnwLockCommands) CommandBandLockCaps() string { return "AT+QNWPREFCFG=?" }

// CommandCellLockCaps 小区锁定能力探测（只读）：AT+QNWLOCK=?。
func (QnwLockCommands) CommandCellLockCaps() string { return "AT+QNWLOCK=?" }

// CommandQueryModePref 组网模式偏好查询：AT+QNWPREFCFG="mode_pref"。
func (QnwLockCommands) CommandQueryModePref() string {
	return `AT+QNWPREFCFG="mode_pref"`
}

// CommandSetModePref 组网模式偏好写入（取值大写，界面的语义档位就是它）。
func (QnwLockCommands) CommandSetModePref(mode string) string {
	return fmt.Sprintf(`AT+QNWPREFCFG="mode_pref",%s`, strings.ToUpper(strings.TrimSpace(mode)))
}

// 编译期断言：QNW 这一套满足锁网能力（NetLocker）。
var _ iface.NetLocker = (*QnwLockCommands)(nil)
