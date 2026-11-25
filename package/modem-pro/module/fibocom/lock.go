package fibocom

import (
	"fmt"
	"go.uber.org/zap"
	"strconv"
	"strings"

	"modemd/iface"
	"modemd/module/catalog"
	"modemd/module/lock"
)

// ====================== 广和通 FM350-GL（MTK 平台）锁网 ======================
//
// 指令依据：《FIBOCOM FM350 AT Commands》V2.2
//
//	§11.1.14  AT+GTACT  Select RAT and BAND   ← 锁频段（频段/RAT 一条指令管理）
//	§11.1.12  AT+EPBSEH Band Selection        ← 位图写法，风险大，本实现不用
//
// 与高通那套（AT+QNWPREFCFG / AT+QNWLOCK，移远与广和通高通平台用）**完全不同**：
//   - 一条 AT+GTACT 同时带 <rat>（驻留制式）、<PreferredAct1/2> 与频段列表；
//   - 频段号是私有编码：UMTS 1~100、LTE = 100+band（B1=101…B71=171）、NR = "50"+band（n1=501…n78=5078）；
//   - 锁小区用 **AT+GTCELLLOCK**（手册 V2.2 未收录，但真机固件有；参数格式依据 QModem `cmds/fibocom.sh`）。
//
// ⚠️ 因为 AT+GTACT 里含 <rat>，下发**必须"先回读再整串改写"**：只替换目标制式的频段号，
// <rat>/<PreferredAct> 与另一制式的频段原样带回去（同 MT5700 AT^SYSCFGEX 的保守做法），
// 否则会把驻留制式或另一制式的频段写坏。
//
// ⚠️ 本节依据 FM350 手册；不同固件/版本可能不同 —— 运行期会先探测 `AT+GTACT?`，
// 只有固件接受才会走这条族的读写（探测不通过则整族不启用）。

// FibocomMTKCommand 广和通「GT 方言」锁网（AT+GTACT / AT+GTCELLLOCK）。
// ⚠️ 名字里的 MTK 是历史称呼：这套方言同时用于 **MTK（FM350 系列）与展锐（FM650-CN / L610）**
// 平台的广和通模组（QModem `tests/test_core_cmds.sh` 里 fm650-cn 也走 `fibocom_gtact_lock_params`）；
// 最终由运行期探测决定是否启用（见 lock.go 的 lockCandidates / lockerFor）。
type FibocomMTKCommand struct {
	FibocomCommand
}

// NSABandKey 覆盖成空串：嵌入的 FibocomCommand 是**高通**平台（有独立的 nsa_nr5g_band），
// 而本类型是 MTK 的 GT 方言 —— `AT+GTACT` 里就**一套** NR 频段列表，没有第二条指令。
//
// ⚠️ 这是嵌入陷阱的经典例子：不覆盖的话 FibocomMTKCommand 会静默"继承"独立 NSA 的能力，
// 界面上多出一组镜像勾选框、点下去发的是不存在的指令（TestNSABandIndependenceDeclared 钉住）。
func (f *FibocomMTKCommand) NSABandKey() string { return "" }

const (
	mtkLockQuery = "AT+GTACT?"
	mtkLockCaps  = "AT+GTACT=?"

	// 锁小区（依据 QModem `cmds/fibocom.sh` 的 cmd_gtcelllock_query/set：
	// 手册 V2.2 没写，但真机固件有这条指令）
	mtkCellLockQuery = "AT+GTCELLLOCK?"
	mtkCellLockCaps  = "AT+GTCELLLOCK=?"
)

// ---------- 频段号私有编码 ----------

// MtkBandCode 频段号 → AT+GTACT 私有编码（上游 vendor/fibocom.sh：UMTS 原样、LTE = 100+band、NR = "50"+band）。
//
// ⚠️**UMTS 必须原样（1/5/8）**：以前只有一个 100+band 分支，WCDMA 会被编成 101/108（= LTE B1/B8 的位置）
// —— 锁 NR 时把 UMTS 组写错，轻则被拒、重则改掉另一制式的配置。
func MtkBandCode(tech lock.CellTech, band int) string {
	switch tech {
	case lock.CellTechNR, lock.CellTechNRNSA:
		return "50" + strconv.Itoa(band)
	case lock.CellTechWCDMA:
		return strconv.Itoa(band)
	}
	return strconv.Itoa(100 + band)
}

// MtkBandFromCode 私有编码 → (制式, 频段号)；认不出 ok=false。
//
//	UMTS：1~100（裸数字，如 1/5/8）
//	LTE ：101~199（B1→101、B71→171）
//	NR  ："50"+band 的十进制串（n1→501、n78→5078、n257→50257）
//
// ⚠️ 真机抓包（QModem `fibocom_gtact_lock_params`）示例 `20,6,3,1,5,8,101,105,5041,5078`
// 同时含 UMTS(1,5,8) / LTE(101,105) / NR(5041,5078) —— 所以 UMTS 频段也要认，
// 改写时原样保留，绝不能丢（丢了会把 WCDMA 频段清空）。
func MtkBandFromCode(code int) (lock.CellTech, int, bool) {
	switch {
	case code >= 1 && code <= 100:
		return lock.CellTechWCDMA, code, true
	case code >= 101 && code <= 199:
		return lock.CellTechLTE, code - 100, true
	case code >= 501:
		s := strconv.Itoa(code)
		if strings.HasPrefix(s, "50") {
			if n, err := strconv.Atoi(s[2:]); err == nil && n > 0 {
				return lock.CellTechNR, n, true
			}
		}
	}
	return "", 0, false
}

// MtkGtact 解析后的 AT+GTACT? 状态
type MtkGtact struct {
	Rat   int    // 驻留制式（1 UMTS / 2 LTE / 10 自动 / 14 NR-RAN / 17 NR-LTE / 20 NR-LTE-UMTS…）
	Act1  string // PreferredAct1（空 = 未设）
	Act2  string // PreferredAct2（空 = 未设）
	Bands []int  // 频段私有编码，原样保留
	Raw   string
}

// BandsFor 取某制式当前配置的频段号
func (g MtkGtact) BandsFor(tech lock.CellTech) []int {
	var out []int
	for _, c := range g.Bands {
		if t, b, ok := MtkBandFromCode(c); ok && t == tech {
			out = append(out, b)
		}
	}
	return out
}

// ParseMTKGTACT 解析 `+GTACT: <rat>,<act1>,<act2>,<band1>,<band2>…`。
//
// ⚠️ <PreferredAct1/2> 与频段共用同一个列表，可能为空（如 `20,,,5078`）。
// 判别办法：PreferredAct 取值只有 2/3/6（都 <101），频段编码都 ≥101 —— 据此分流，
// 不写死下标（空字段会被 SplitATFields 原样保留）。
func ParseMTKGTACT(resp []string) (MtkGtact, bool) {
	for _, line := range resp {
		i := strings.Index(strings.ToUpper(line), "+GTACT:")
		if i < 0 {
			continue
		}
		f := lock.SplitATFields(line[i+len("+GTACT:"):])
		if len(f) == 0 {
			continue
		}
		var g MtkGtact
		g.Rat, _ = strconv.Atoi(f[0])
		g.Raw = strings.Join(f, ",")
		for i, s := range f[1:] {
			if s == "" {
				continue
			}
			n, err := strconv.Atoi(s)
			if err != nil {
				continue
			}
			// <PreferredAct> 取值只有 2/3/6，且只可能出现在最前两格；
			// 其余数字都是频段编码（UMTS 1~100 / LTE 101~199 / NR ≥501）。
			if i < 2 && (n == 2 || n == 3 || n == 6) {
				if g.Act1 == "" {
					g.Act1 = s
				} else if g.Act2 == "" {
					g.Act2 = s
				}
				continue
			}
			g.Bands = append(g.Bands, n)
		}
		return g, true
	}
	return MtkGtact{}, false
}

// ---------- NetLocker 的指令实现 ----------

func (f *FibocomMTKCommand) CommandQueryBandLock(lock.CellTech) string { return mtkLockQuery }
func (f *FibocomMTKCommand) CommandBandLockCaps() string               { return mtkLockCaps }

// CommandSetBandLock 恒返回空串：AT+GTACT 要先回读再整串改写（见 BandLockSetter），
// 单条指令拼不出来 —— 上层看到空串会走 SetBandLockAt。
func (f *FibocomMTKCommand) CommandSetBandLock(lock.CellTech, []int) string { return "" }

// CommandQueryBandCapability 该固件无「设备能力频段」查询（=? 只列支持的指令签名）。
func (f *FibocomMTKCommand) CommandQueryBandCapability(lock.CellTech) string { return "" }

// CommandQueryPolicyBand 无 policy 频段概念（解锁靠 AT+GTACT，不靠写回）。
func (f *FibocomMTKCommand) CommandQueryPolicyBand() string { return "" }

// CommandQueryCellLock 锁小区查询：AT+GTCELLLOCK?（LTE/NR 共用一条，按应答里的 cell_type 区分）
func (f *FibocomMTKCommand) CommandQueryCellLock(lock.CellTech) string { return mtkCellLockQuery }

// CommandLockCell 锁小区下发。格式（QModem `vendor/fibocom.sh` 实机验证）：
//
//	AT+GTCELLLOCK=<lockcell_status>,<cell_type>,<lock_type>,<arfcn>[,<pci>[,<scs>,<nr_band>]]
//	  lockcell_status：1=锁、0=解锁
//	  cell_type      ：1=NR、0=LTE
//	  lock_type      ：1=仅锁频点、0=锁频点+PCI
//
// 例：NR 频点+PCI `AT+GTCELLLOCK=1,1,0,627264,521,1,78`；LTE 仅频点 `AT+GTCELLLOCK=1,0,1,2452`；解锁 `AT+GTCELLLOCK=0`。
func (f *FibocomMTKCommand) CommandLockCell(req lock.CellLockRequest) string {
	cellType := 0 // 0=LTE
	if req.Tech == lock.CellTechNR {
		cellType = 1
	}
	switch req.LockType {
	case lock.CellLockNone:
		return "AT+GTCELLLOCK=0"
	case lock.CellLockARFCN:
		return fmt.Sprintf("AT+GTCELLLOCK=1,%d,1,%d", cellType, req.ARFCN)
	default:
		if req.Tech == lock.CellTechNR {
			return fmt.Sprintf("AT+GTCELLLOCK=1,%d,0,%d,%d,%d,%d",
				cellType, req.ARFCN, req.PCID, iface.NormalizeSCS(req.SCS), req.Band())
		}
		return fmt.Sprintf("AT+GTCELLLOCK=1,%d,0,%d,%d", cellType, req.ARFCN, req.PCID)
	}
}

func (f *FibocomMTKCommand) CommandCellLockCaps() string { return mtkCellLockCaps }

// CommandQueryModePref / CommandSetModePref 组网模式随 AT+GTACT 的 <rat> 一起管，
// 单摘出来风险大（会改驻留制式），本实现不提供。
func (f *FibocomMTKCommand) CommandQueryModePref() string     { return "" }
func (f *FibocomMTKCommand) CommandSetModePref(string) string { return "" }

// ---------- BandLockSetter：先回读再按上游形式整串改写 ----------

// FibocomGTACTForm 本机固件要求的 AT+GTACT 参数形式。
//
// 上游 `fibocom_gtact_lock_params()` 按平台/型号分三种写法：
//
//	mediatek               → "17,6,6,<lte>,<nr>"（锁 LTE/NR 时；**固定清掉 UMTS 组**）
//	qualcomm（非 FM150）   → "17,,,<lte>,<nr>"  （fibocom_uses_empty_gtact_fields：空字段形式）
//	其余（展锐/lte/FM150） → "<act>,6,3,<umts>,<lte>,<nr>"，单制式时 "<act>,,,<bands>"
//
// ⚠️ 空参数组会被模组**省略** ⇒ 位置对齐有歧义：形式写错就会被回 ERROR
// （真机 FM160-CN 实测：把回读到的整串原样发回去会被拒）。
type FibocomGTACTForm int

const (
	GtactFormAct63       FibocomGTACTForm = iota // 默认：<act>,6,3,…
	GtactFormEmptyFields                         // 高通（非 FM150）：17,,,…
	GtactFormMediaTek                            // 联发科：17,6,6,…
)

// FibocomGTACTFormForModel 判形式（纯函数，输入是型号串）。
//
// ⚠️ 平台先查**模块清单**（modem_catalog.json 里每个型号都写了平台，如 NL668=lte），
// 清单没有这个型号才回退 PlatformForModel —— 后者是型号名前缀表，没收录的型号会误判成高通。
// 上游的判定就是 `[ "$platform" = "qualcomm" ]`（lte/unisoc/mediatek 全走默认形式）。
//
// ⚠️ **不能只在 locker 构造时算**：锁网候选表是“无参构造器”造的（拿不到型号），
// 构造时才算会一直落到默认形式（真机 FM160-CN 就这样发成了 17,6,3,… 被拒）。
func FibocomGTACTFormForModel(model string) FibocomGTACTForm {
	model = strings.TrimSpace(model)
	if model == "" {
		return GtactFormAct63
	}
	isFM150 := strings.HasPrefix(catalog.CatalogFlatten(model), "FM150")
	if e, ok := lock.CatalogEntryForModel(model); ok {
		switch strings.ToLower(strings.TrimSpace(e.Platform)) {
		case "mediatek":
			return GtactFormMediaTek
		case "qualcomm":
			if !isFM150 {
				return GtactFormEmptyFields
			}
			return GtactFormAct63
		default:
			return GtactFormAct63 // unisoc / lte / lte12 / asrmicro…
		}
	}
	switch lock.PlatformForModel(model) {
	case lock.PlatformMediaTek:
		return GtactFormMediaTek
	case lock.PlatformQualcomm:
		if !isFM150 {
			return GtactFormEmptyFields
		}
	}
	return GtactFormAct63
}

// gtactActFromBands 上游 fibocom_gtact_network_prefer_from_bands()：按"哪些制式有频段"定 <act>
func gtactActFromBands(umts, lte, nr []int) string {
	switch {
	case len(umts) > 0 && len(lte) > 0 && len(nr) > 0:
		return "20"
	case len(umts) > 0 && len(lte) > 0:
		return "4"
	case len(umts) > 0 && len(nr) > 0:
		return "16"
	case len(lte) > 0 && len(nr) > 0:
		return "17"
	case len(nr) > 0:
		return "14"
	case len(lte) > 0:
		return "2"
	case len(umts) > 0:
		return "1"
	}
	return "20"
}

// GtactLockCommand 按上游形式拼 AT+GTACT（锁频段与解锁共用）。
//
// tech = 本次要改的制式（bands 是它的新频段，空 = 解开）；另外两个制式的频段从
// 回读到的 g 里原样带回去。
func GtactLockCommand(form FibocomGTACTForm, g MtkGtact, tech lock.CellTech, bands []int) string {
	umts, lte, nr := g.BandsFor(lock.CellTechWCDMA), g.BandsFor(lock.CellTechLTE), g.BandsFor(lock.CellTechNR)
	switch tech {
	case lock.CellTechWCDMA:
		umts = bands
	case lock.CellTechLTE:
		lte = bands
	case lock.CellTechNR, lock.CellTechNRNSA:
		nr = bands
	}
	enc := func(t lock.CellTech, list []int) []string {
		out := make([]string, 0, len(list))
		for _, b := range list {
			out = append(out, MtkBandCode(t, b))
		}
		return out
	}
	umtsC, lteC, nrC := enc(lock.CellTechWCDMA, umts), enc(lock.CellTechLTE, lte), enc(lock.CellTechNR, nr)
	join := func(groups ...[]string) string {
		var out []string
		for _, grp := range groups {
			out = append(out, grp...)
		}
		return strings.Join(out, ",")
	}
	switch form {
	case GtactFormMediaTek:
		// 上游：umts_bands="" + act 固定 17 + 两个 act 字段都是 6
		if s := join(lteC, nrC); s != "" {
			return "AT+GTACT=17,6,6," + s
		}
		return "AT+GTACT=17"
	case GtactFormEmptyFields:
		if s := join(lteC, nrC); s != "" {
			return "AT+GTACT=17,,," + s
		}
		return "AT+GTACT=17"
	}
	act := gtactActFromBands(umts, lte, nr)
	all := join(umtsC, lteC, nrC)
	if all == "" {
		return "AT+GTACT=" + act
	}
	groups := 0
	for _, grp := range [][]string{umtsC, lteC, nrC} {
		if len(grp) > 0 {
			groups++
		}
	}
	if groups > 1 {
		return "AT+GTACT=" + act + ",6,3," + all
	}
	return "AT+GTACT=" + act + ",,," + all
}

// KeepOtherTech 保留**非** tech 制式的频段编码（原样，避免动到另一制式）
func (g MtkGtact) KeepOtherTech(tech lock.CellTech) []string {
	var out []string
	for _, c := range g.Bands {
		if t, _, ok := MtkBandFromCode(c); ok && t != tech {
			out = append(out, strconv.Itoa(c))
		}
	}
	return out
}

// SetBandLockAt 锁频段：回读 → 按上游形式整串下发
func (f *FibocomMTKCommand) SetBandLockAt(at lock.LockATDoer, tech lock.CellTech, bands []int) error {
	resp, err := at.SendAT(mtkLockQuery)
	if err != nil {
		return err
	}
	g, ok := ParseMTKGTACT(resp)
	if !ok {
		return fmt.Errorf("AT+GTACT? 应答无法解析，拒绝改写（避免把驻留制式/频段写坏）")
	}
	cmd := GtactLockCommand(FibocomGTACTFormForModel(at.ModemModel()), g, tech, bands)
	lock.Logger().Info("fibocom mtk band lock apply",
		zap.Int("modem", at.ModemID()), zap.String("tech", string(tech)),
		zap.Ints("bands", bands), zap.String("command", cmd))
	return at.SendLockCommand(cmd)
}

// UnlockBandAt 解除频段锁定：目标制式频段传空，其余按上游形式拼。
func (f *FibocomMTKCommand) UnlockBandAt(at lock.LockATDoer, tech lock.CellTech) error {
	resp, err := at.SendAT(mtkLockQuery)
	if err != nil {
		return err
	}
	g, ok := ParseMTKGTACT(resp)
	if !ok {
		return fmt.Errorf("AT+GTACT? 应答无法解析，拒绝改写（避免把驻留制式/频段写坏）")
	}
	cmd := GtactLockCommand(FibocomGTACTFormForModel(at.ModemModel()), g, tech, nil)
	lock.Logger().Info("fibocom mtk band unlock apply",
		zap.Int("modem", at.ModemID()), zap.String("tech", string(tech)), zap.String("command", cmd))
	return at.SendLockCommand(cmd)
}

// ---------- LockVendorOps：解析 + 说明（应答格式与移远完全不同） ----------

// ParseBandLockState 解析当前制式的频段配置。
// ⚠️ 本固件无「设备能力频段」，判断不了"是否真的受限"，故 locked 仅表示"配了具体频段列表"。
func (f *FibocomMTKCommand) ParseBandLockState(tech lock.CellTech, resp []string) ([]int, bool) {
	g, ok := ParseMTKGTACT(resp)
	if !ok {
		return nil, false
	}
	bands := g.BandsFor(tech)
	return bands, len(bands) > 0
}

// ParseCellLockState 解析 `+GTCELLLOCK: <lockcell_status>,<cell_type>,<lock_type>,<arfcn>,<pci>,<scs>,<nr_band>`。
// cell_type：1=NR、0=LTE；lock_type：1=仅频点、0=频点+PCI。
func (f *FibocomMTKCommand) ParseCellLockState(tech lock.CellTech, resp []string) (lock.CellLock, bool) {
	cl := lock.CellLock{Tech: tech}
	for _, line := range resp {
		i := strings.Index(strings.ToUpper(line), "+GTCELLLOCK:")
		if i < 0 {
			continue
		}
		f := lock.SplitATFields(line[i+len("+GTCELLLOCK:"):])
		if len(f) < 2 {
			continue
		}
		cl.RawValue = strings.Join(f, ",")
		if lock.AtIntOr0(f, 0) == 0 {
			continue // 未锁定
		}
		isNR := lock.AtIntOr0(f, 1) == 1
		if isNR != (tech == lock.CellTechNR) {
			continue // 这是另一技术域的锁
		}
		cl.Locked = true
		cl.ARFCN = lock.AtIntOr0(f, 3)
		if lock.AtIntOr0(f, 2) == 0 {
			cl.PCID = lock.AtIntOr0(f, 4)
			cl.LockType = lock.CellLockARFCNPCI
		} else {
			cl.LockType = lock.CellLockARFCN
		}
	}
	return cl, true
}

// CommandUnlockBand 返回空串：解锁走 BandLockSetter.UnlockBandAt（要回读再改写）。
func (f *FibocomMTKCommand) CommandUnlockBand(lock.CellTech) string { return "" }

// ParseModePref 不提供（组网模式随 AT+GTACT 管理）。
func (f *FibocomMTKCommand) ParseModePref([]string) (string, bool) { return "", false }

// SetModePrefVendor 不处理，交回上层（上层会因 CommandSetModePref 为空而报不支持）。
func (f *FibocomMTKCommand) SetModePrefVendor(lock.LockATDoer, string) (bool, error) {
	return false, nil
}

// CommandSaveCellLock 无锁小区，无需持久化。
func (f *FibocomMTKCommand) CommandSaveCellLock() string { return "" }

// QueryBandCapability 无设备能力频段查询。
func (f *FibocomMTKCommand) QueryBandCapability(lock.LockATDoer) map[lock.CellTech][]int { return nil }

// BandCapabilityNote 「设备能力」拿不到时的说明（界面显示这句而不是光一个 -）。
func (f *FibocomMTKCommand) BandCapabilityNote(lock.CellTech) string {
	return "该固件（广和通 MTK 平台）不上报设备能力频段"
}

// StatusNote 锁网页下方的机型提示。
func (f *FibocomMTKCommand) StatusNote() string {
	return "广和通（MTK/展锐平台，如 FM350-GL / FM650-CN）：锁频段用 AT+GTACT" +
		"（命令含驻留制式与另一制式频段，下发会先回读再整串改写）；" +
		"锁小区用 AT+GTCELLLOCK（cell_type 1=NR/0=LTE，lock_type 1=仅频点/0=频点+PCI）。"
}
