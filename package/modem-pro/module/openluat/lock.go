package openluat

import (
	"fmt"
	"strconv"
	"strings"

	"modemd/module/lock"
)

// ====================== openluat（Air724UG）锁频段：AT*BAND ======================
//
// 依据 QModem 的 `cmds/openluat.sh` + `vendor/openluat.sh`（逐行照抄其语义）：
//
//	查询：AT*BAND?    写入：AT*BAND=<tail>    复位：AT*BAND=<current_mode>
//
// `AT*BAND?` 的应答是一行 `*BAND: <current_mode>,<gsm_band>,<umts_band>,<lte_high>,<lte_low>,<roaming_config>,<srv_domain>`
// （`openluat_band_values` 用 awk 只认 `/^[[:space:]]*[*]BAND[[:space:]]*:/` 这种行；
//  随后 `set -- $band_values; current_mode=$1 gsm_band=$2 umts_band=$3 roaming_config=$6 srv_domain=$7`
//  —— 第 4/5 个字段正是它单独接出来的 lte_high / lte_low）。
//
// LTE 位图（Band → 位）取自它的 case 分支，**已逐条核对**：
//
//	1→low|=1(bit0)  3→low|=4(bit2)  5→low|=16(bit4)  8→low|=128(bit7)
//	34→high|=2(bit1) 38→high|=32(bit5) 39→high|=64(bit6) 40→high|=128(bit7)
//
// 即：**low = band 1..32 的 bit(band-1)**，**high = band 33..64 的 bit(band-33)**；
// 输出顺序是 `printf '%s,%s\n' "$lte_high" "$lte_low"` → **先 high 后 low**（十进制、去前导零）。
//
// ⚠️ 因此本实现走"**回读 → 原样保留其余字段 → 只替换第 4/5 个字段 → 整串写回**"，
// 与 Telit 那套同样思路：其余字段（含 roaming / srv_domain）**一律用设备自己上报的原值**，
// 绝不自己拼默认值 —— 写回的东西要么是设备给的，要么是我们算的 LTE 位图。
// 回读失败（字段不足 7 个 / 关键位图不是数字）就**拒发**。
//
// 该机型（Air724UG）只有 LTE，**没有 NR、也没有锁小区**（QModem 明言 `AT*CELL` 不支持）。

// OpenluatAir724LTEBands Air724UG 的可用 LTE 频段。
// 两处独立证据：① `vendor/openluat.sh` 里 `for band in 1 3 5 8 34 38 39 40 41`；
// ② QModem 真机 testcase `testcases/openluat/unisoc/air724ug-*/expected/get_lockband.json`
// 的 available_band 正是这 9 个。
var OpenluatAir724LTEBands = []int{1, 3, 5, 8, 34, 38, 39, 40, 41}

type OpenluatLockCommand struct{}

// ---------- 位图与频段 ----------

// OpenluatSplitLTE 频段 → (high, low)；band 1..32 进 low、band 33..64 进 high
func OpenluatSplitLTE(bands []int) (high, low uint64) {
	for _, b := range bands {
		switch {
		case b >= 1 && b <= 32:
			low |= 1 << uint(b-1)
		case b >= 33 && b <= 64:
			high |= 1 << uint(b-33)
		}
	}
	return
}

// OpenluatLTEbands (high, low) → 频段；只保留 known 里列出的（免得把保留位解读成频段）
func OpenluatLTEbands(high, low uint64, known []int) []int {
	var out []int
	for _, b := range known {
		var set bool
		switch {
		case b >= 1 && b <= 32:
			set = low&(1<<uint(b-1)) != 0
		case b >= 33 && b <= 64:
			set = high&(1<<uint(b-33)) != 0
		}
		if set {
			out = append(out, b)
		}
	}
	return out
}

// OpenluatBandFields 从 `AT*BAND?` 应答里取出 `*BAND:` 行的字段。
//
// ⚠️ QModem 要求**正好 8 段且全为数字**（mode / gsm / umts / lte_high / lte_low / roaming / srv_domain / priority），
// 少于 8 段就整个放弃 —— 因为写回也要 8 段。跳过回显行（以 AT 开头）。
func OpenluatBandFields(resp []string) ([]string, bool) {
	const mark = "*BAND"
	for _, line := range resp {
		u := strings.ToUpper(strings.TrimSpace(line))
		if u == "" || strings.HasPrefix(u, "AT") {
			continue
		}
		i := strings.Index(u, mark)
		if i < 0 {
			continue
		}
		rest := strings.TrimSpace(line[i+len(mark):])
		if !strings.HasPrefix(rest, ":") {
			continue
		}
		fields := strings.FieldsFunc(rest[1:], func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\r'
		})
		if len(fields) < 8 {
			continue
		}
		fields = fields[:8]
		ok := true
		for _, f := range fields {
			if _, err := strconv.ParseUint(strings.TrimSpace(f), 10, 64); err != nil {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		return fields, true
	}
	return nil, false
}

// openluatParseUint 位图字段 → uint64（无符号十进制；空/非法 → false）
func openluatParseUint(s string) (uint64, bool) {
	s = strings.TrimSpace(strings.Trim(s, `"`))
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// openluatCurrent 回读当前 8 个字段（全部得是数字）；顺带校验 lte_high/lte_low 可解析
func (o *OpenluatLockCommand) openluatCurrent(at lock.LockATDoer) ([]string, uint64, uint64, bool) {
	resp, err := at.SendAT("AT*BAND?")
	if err != nil {
		return nil, 0, 0, false
	}
	fields, ok := OpenluatBandFields(resp)
	if !ok {
		return nil, 0, 0, false
	}
	high, okH := openluatParseUint(fields[3])
	low, okL := openluatParseUint(fields[4])
	if !okH || !okL {
		return nil, 0, 0, false
	}
	return fields, high, low, true
}

// openluatModeIsLTEOnly 当前 mode 是不是"只跑 LTE"那个值 —— 只有它固件才理会频段掩码。
// QModem `vendor/openluat.sh`：`if [ "$mode" = "5" ]; then selected=...`，
// 并注明 "In dual/triple modes the manual says band masks are ignored"。
func openluatModeIsLTEOnly(fields []string) bool {
	return len(fields) > 0 && strings.TrimSpace(fields[0]) == openluatLTEOnlyMode
}

// openluatLTEOnlyMode 固件里"仅 LTE"的 mode 值（只有这个模式下频段掩码生效）
const openluatLTEOnlyMode = "5"

// ---------- NetLocker ----------

// CommandQueryBandLock 只有 LTE 有（Air724UG 无 NR）
func (o *OpenluatLockCommand) CommandQueryBandLock(tech lock.CellTech) string {
	if tech == lock.CellTechLTE {
		return "AT*BAND?"
	}
	return ""
}

// CommandSetBandLock 空串：AT*BAND 要"回读其余字段再整串写回"，单条指令拼不出来
func (o *OpenluatLockCommand) CommandSetBandLock(lock.CellTech, []int) string { return "" }

func (o *OpenluatLockCommand) CommandQueryBandCapability(lock.CellTech) string { return "" }
func (o *OpenluatLockCommand) CommandQueryPolicyBand() string                  { return "" }

// 无锁小区（AT*CELL 不支持）
func (o *OpenluatLockCommand) CommandQueryCellLock(lock.CellTech) string   { return "" }
func (o *OpenluatLockCommand) CommandLockCell(lock.CellLockRequest) string { return "" }

func (o *OpenluatLockCommand) CommandBandLockCaps() string { return "" } // 无 `=?` 形态
func (o *OpenluatLockCommand) CommandCellLockCaps() string { return "" }

func (o *OpenluatLockCommand) CommandQueryModePref() string     { return "" }
func (o *OpenluatLockCommand) CommandSetModePref(string) string { return "" }

// ---------- LockVendorOps ----------

// ParseBandLockState 解析 `*BAND:` 的 lte_high / lte_low → 频段（只报已知的那 9 个）。
//
// ⚠️ 只有 mode = 5（仅 LTE）时掩码才生效，其它模式（自动/双模）固件**忽略掩码**，
// 所以这时返回 false —— 否则界面会显示一个根本不生效的"锁定"。
// 第二个返回值（是否受限）给 false：与设备能力比较的口径交给上游统一处理。
func (o *OpenluatLockCommand) ParseBandLockState(tech lock.CellTech, resp []string) ([]int, bool) {
	if tech != lock.CellTechLTE {
		return nil, false
	}
	fields, ok := OpenluatBandFields(resp)
	if !ok || !openluatModeIsLTEOnly(fields) {
		return nil, false
	}
	high, okH := openluatParseUint(fields[3])
	low, okL := openluatParseUint(fields[4])
	if !okH || !okL {
		return nil, false
	}
	bands := OpenluatLTEbands(high, low, OpenluatAir724LTEBands)
	if len(bands) == 0 {
		return nil, false
	}
	return bands, false
}

func (o *OpenluatLockCommand) ParseCellLockState(lock.CellTech, []string) (lock.CellLock, bool) {
	return lock.CellLock{}, true
}

// CommandUnlockBand 空串：复位要走 BandLockSetter（`AT*BAND=<current_mode>`）
func (o *OpenluatLockCommand) CommandUnlockBand(lock.CellTech) string { return "" }

func (o *OpenluatLockCommand) ParseModePref([]string) (string, bool) { return "", false }
func (o *OpenluatLockCommand) SetModePrefVendor(lock.LockATDoer, string) (bool, error) {
	return false, nil
}
func (o *OpenluatLockCommand) CommandSaveCellLock() string { return "" }

// QueryBandCapability Air724UG 的可用 LTE 频段（无 NR）。来源见 OpenluatAir724LTEBands 注释。
func (o *OpenluatLockCommand) QueryBandCapability(lock.LockATDoer) map[lock.CellTech][]int {
	return map[lock.CellTech][]int{lock.CellTechLTE: append([]int(nil), OpenluatAir724LTEBands...)}
}

func (o *OpenluatLockCommand) BandCapabilityNote(lock.CellTech) string {
	return "按 Air724UG 的可用频段表显示（1,3,5,8,34,38,39,40,41）"
}

func (o *OpenluatLockCommand) StatusNote() string {
	return "openluat（Air724UG）：锁频段走 AT*BAND=<mode>,<gsm>,<umts>,<lte_high>,<lte_low>,<roaming>,<srv_domain>,<priority>；" +
		"本实现先回读 AT*BAND?，其余字段原样写回（不动漫游/服务域），**mode 写成 5** —— " +
		"固件只在 mode=5（仅 LTE）时理会频段掩码（Modem-Pro 原注：in dual/triple modes the manual says band masks are ignored）；" +
		"解除锁定：mode≠5 时发 AT*BAND=<当前 mode>，mode=5 时改发全部可用频段（不限制）。" +
		"本机型只有 LTE，无 NR、无锁小区。"
}

// ---------- BandLockSetter ----------

// SetBandLockAt 锁定 LTE 频段：
//
//	回读 AT*BAND? → 校验 mode ∈ 0..15 → 写成 `AT*BAND=5,<gsm>,<umts>,<high>,<low>,<roaming>,<srv>,<priority>`
//
// ⚠️ mode 必须写成 **5**：其它模式（自动/双模）下固件会**忽略**频段掩码，
// 保持原 mode 的话"锁了等于没锁"（这一点与 QModem 的 set_lockband 一致）。
func (o *OpenluatLockCommand) SetBandLockAt(at lock.LockATDoer, tech lock.CellTech, bands []int) error {
	if tech != lock.CellTechLTE {
		return fmt.Errorf("openluat Air724UG 是 LTE 单模，没有 %s 频段可锁", tech)
	}
	fields, _, _, ok := o.openluatCurrent(at)
	if !ok {
		return fmt.Errorf("AT*BAND? 应答不完整（需要 `*BAND: <mode>,<gsm>,<umts>,<lte_high>,<lte_low>,<roaming>,<srv_domain>,<priority>` 共 8 段数字），" +
			"拒绝下发 —— 否则会把漫游/服务域等字段一起改掉")
	}
	if !openluatModeValid(fields[0]) {
		return fmt.Errorf("AT*BAND? 报的 mode = %q 不在 0-15，拒绝下发", fields[0])
	}
	high, low := OpenluatSplitLTE(bands)
	if high == 0 && low == 0 {
		return fmt.Errorf("频段表 %v 里没有 Air724UG 支持的 LTE 频段（可用：%s）",
			bands, lock.JoinInts(OpenluatAir724LTEBands, ","))
	}
	out := append([]string(nil), fields...)
	out[0] = openluatLTEOnlyMode
	out[3] = strconv.FormatUint(high, 10)
	out[4] = strconv.FormatUint(low, 10)
	return at.SendLockCommand("AT*BAND=" + strings.Join(out, ","))
}

// UnlockBandAt 解除锁定（QModem set_lockband 的空列表分支）：
//
//	mode ≠ 5：直接发 `AT*BAND=<当前 mode>` —— 该模式下掩码本就被忽略，重发即恢复默认。
//	mode = 5：发全部可用频段（否则重复发 mode 5 会把锁住的那几个频段又写回去）。
func (o *OpenluatLockCommand) UnlockBandAt(at lock.LockATDoer, tech lock.CellTech) error {
	if tech != lock.CellTechLTE {
		return fmt.Errorf("openluat Air724UG 是 LTE 单模，没有 %s 频段可解锁", tech)
	}
	fields, _, _, ok := o.openluatCurrent(at)
	if !ok {
		return fmt.Errorf("AT*BAND? 应答不完整，读不出 current_mode，拒绝下发解锁指令")
	}
	if !openluatModeIsLTEOnly(fields) {
		return at.SendLockCommand("AT*BAND=" + strings.TrimSpace(fields[0]))
	}
	high, low := OpenluatSplitLTE(OpenluatAir724LTEBands)
	out := append([]string(nil), fields...)
	out[3] = strconv.FormatUint(high, 10)
	out[4] = strconv.FormatUint(low, 10)
	return at.SendLockCommand("AT*BAND=" + strings.Join(out, ","))
}

// openluatModeValid mode 是否在固件认可的 0-15 范围内（QModem 的 case 分支）
func openluatModeValid(s string) bool {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	return err == nil && n >= 0 && n <= 15
}
