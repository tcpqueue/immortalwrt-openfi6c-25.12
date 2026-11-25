package unisoc

import (
	"fmt"
	"modemd/module/common"
	"modemd/module/lock"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// ====================== 展锐（Unisoc）锁网方言 —— 只做"已取证"的部分 ======================
//
// 取证记录（2026-10-05，中兴 F50 / UMS9620，全部走 `/dev/stty_nr2` 快路，~10ms/条；
// 原始对话归档在 `d:\Go\MD\F50-UMS9620-modem.md`，可用命令手册是 `d:\Go\MD\F50-UMS9620-AT-命令手册.md`）：
//
//	AT+SPCOMMLOCKBAND=? → +SPCOMMLOCKBAND:(0,1),(2,12,13,14,15),(),(),(),(),()  ✅ 指令存在（7 参）
//	AT+SPCOMMLOCKFRQ=?  → +SPCOMMLOCKFRQ:(0,1,2),(12,13,14,15,16),(0,1),(),…   ✅（9 参）
//	AT+SPCELLLOCK=?     → +SPCELLLOCK:(0-10),(0-2),(0-10),()                   ✅（4 参）
//	AT+SPLBAND=?        → +SPLBAND:(0-10),(0-65535)×5                          ✅（6 参）
//	AT+SPBANDCTRL=?     → +SPBANDCTRL:(0-1),(0-255),(0-255),(0-255)            ✅（4 参）
//	AT+SPRAT?           → +SPRAT: LTE 32                                       ✅ 组网模式能读
//	AT+SPCOMMLOCKBAND? / +SPCOMMLOCKFRQ? / +SPCELLLOCK?  → +CME ERROR: 21      ❌ 锁状态读不回来
//	AT+SPLBAND?                                            → +CME ERROR: 4
//	AT+SPRAT="LTE 32" / AT+SPRAT=LTE 32                     → +CME ERROR: 4     ❌ SPRAT 写不可行
//
// 2026-10-06 追加取证（组网模式**有可写路径**了）：
//
//	AT+SP5GRAN?   → +SP5GRAN: 1        ✅ 5G 开关：1 = 5G+4G、0 = 仅 4G（本机唯一可写的组网模式旋钮）
//	AT+SP5GRAN=?  → +SP5GRAN:(0-1)
//	AT+SP5GRAN=0  → OK（回读 0；NR 消失、落到 LTE：+CEREG 制式码变 7 与 13，+SPRAT 由 LTE 32 变 LTE 16）
//	AT+SP5GRAN=1  → OK（回读 1，恢复 5G+4G）
//	AT+SPRAT=32 / AT+SPRAT="LTE 32" / AT+SPRAT="4C5445203332" → +CME ERROR: 4 ❌ 三种写法都被拒
//
// 2026-10-06 追加取证（锁小区：**拿到了可用的“清除/解锁”形态**，设置形态仍不可靠）：
//
//	AT+SPCELLLOCK=0            → +CME ERROR: 3（参数个数不够）
//	AT+SPCELLLOCK=0,0          → +SPCELLLOCK:0,0,0,0  ✅ 4 个数字 = 存下来的状态，全 0 = 未锁
//	AT+SPCELLLOCK=0,0,0        → +SPCELLLOCK:0,0,0,0  ✅ 它同时把锁清掉（已验证：先写 1,0,0,0，再发它就变回全 0）
//	AT+SPCELLLOCK=1,0,0,0      → +SPCELLLOCK:1,0,0,0  ✅ 回显 = 写完之后的状态（p2=0 时才回显）
//	AT+SPCELLLOCK=1,0,3,140    → +SPCELLLOCK:1,0,0,0  ❌ p3/p4 没被存下来（语义未知）
//	AT+SPCELLLOCK=1,1,0,140 / 1,2,0,140 / 1,1,3,140 → 仅 OK（p2!=0 不回显），驻网无变化、也没有 +SPCELLLOCK 上报
//	AT+SPCELLLOCK=3,1,140,1850 → +CME ERROR: 65536014（p3 超 10 就被拒 ⇒ p3 确实是 0-10 的枚举）
//
// ⇒ **只有“解除”可用**：`AT+SPCELLLOCK=0,0,0`。设置形态写下去既看不到状态、也观察不到生效，
// 所以方言里只放解除，锁写继续留空。
//
// 同期其它锁命令的探针（都无状态回读，因此都无法确认含义）：
//
//	AT+SPCOMMLOCKBAND=0 / =1        → +CME ERROR: 50（参数个数不够）
//	AT+SPCOMMLOCKFRQ=2 / =2,12      → +CME ERROR: 50
//	AT+SPCOMMLOCKFRQ=2,12,0         → OK（被接受，但无回显）
//
// ⚠️⚠️ **参数语义没取证**：`(2,12,13,14,15)` 像 RAT 码、`(0-65535)` 像频段位图，
// 但都是"看着像"而不是证据（固件里不存帮助文本，QModem 也没覆盖展锐 5G）。
// 按项目规则**不臆造码表**，因此这条方言：
//
//	✅ 照实上报**能力探测**（界面能看到固件真实接受的参数形式，不再显示"固件不支持锁网"）；
//	✅ 组网模式：回读 `AT+SPRAT?`，写入走 5G 开关 `AT+SP5GRAN`（见上）；
//	✅ 小区锁定**解除**：`AT+SPCELLLOCK=0,0,0`（实测回 `+SPCELLLOCK:0,0,0,0`，不影响驻网）；
//	❌ **不下发任何锁设置指令**（锁频段/锁频点/锁小区的目标值语义未取证，写错会把模组锁出服务）。
//	   要试请在「AT 指令」页手动发 —— 那页已经有这几个 `=?` 预设（分组「锁网（展锐 SP*·参数未取证）」）。
//
// 这三个说明会出现在界面上（`/api/lock/caps` 与锁网卡片下方），
// 让用户知道"不是没做，是固件没给可验证的语义"。

const (
	// 能力探测（`=?` 只读、不改状态）：2026-10-05 真机实测全部有应答
	cmdSPCommLockBandCaps = "AT+SPCOMMLOCKBAND=?"
	CmdSPCellLockCaps     = "AT+SPCELLLOCK=?"
	// 状态回读：实测都是 +CME ERROR: 21（读不回来），仍然发出去，
	// 让「固件支持探测」把**真实应答**摆给用户看，而不是我们口头说"不支持"。
	cmdSPCommLockBandQuery = "AT+SPCOMMLOCKBAND?"
	cmdSPCellLockQuery     = "AT+SPCELLLOCK?"
	// 组网模式（唯一能回读的锁网项）
	cmdSPRATQuery = "AT+SPRAT?"
	// 小区锁定**解除**：实测可用（2026-10-06）。四个参数全 0 = "没指定任何锁"，
	// 固件回 `+SPCELLLOCK:0,0,0,0` 并把锁清掉；发它不影响驻网（前后 CEREG/CSQ 一致）。
	CmdSPCellLockClear = "AT+SPCELLLOCK=0,0,0"

	// ---- 2026-10-06 真机取证：LTE 锁频段的读/写路径 ----
	//
	// `AT^SYSCFGEX` 在本固件是**完整实现**的（不是残留兼容）：读得到、写得回，
	// 5 个字段依次是 组网模式/2G+3G 频带码/LTE 频段位图/漫游/服务域/搜网顺序：
	//
	//	AT^SYSCFGEX?  → ^SYSCFGEX: "00",2000000400000,0,2,1e200000095
	//	AT^SYSCFGEX=? → ^SYSCFGEX: "00","01",...,"99",(-band-),(0-3),(0-2),
	//	                ((LTE BC1/LTE BC3/LTE BC5/LTE BC8/LTE BC34/LTE BC38/LTE BC39/LTE BC40/LTE BC41/),
	//	                 (7FFFFFFFFFFFFFFF, "All bands"))
	//
	// 第 5 个字段就是 LTE 频段位图（bit(频段-1) = LTE B<n>）：
	// 固件自己列的 9 个 LTE 频段 BC1/BC3/BC5/BC8/BC34/BC38/BC39/BC40/BC41
	// 恰好等于 2^0+2^2+2^4+2^7+2^33+2^37+2^38+2^39+2^40 = **0x1E200000095** —— 就是出厂值 ✓
	// （真机验证：写 0x1C200000095（= 去掉 B38）能原样回读，驻网正常；写回 1e200000095 也一致。
	//  而第 2 个字段是 2G/3G 频带码表（鼎桥同型：2000000400380 = GSM900/1800+WCDMA900/2100），
	//  **不是**频段位图 —— 往它上面做"加一位"实验只会磎到一鼻子灰。）
	//
	// ⚠️ NR 频段没有任何可读写来源（`=?` 模板里只有 LTE BC 名单）⇒ NR 侧一律不下发。
	cmdSyscfgEXQuery = "AT^SYSCFGEX?"
	cmdSyscfgEXCaps  = "AT^SYSCFGEX=?"

	// 频段配置的**备用**读源（`=?` 拿不到时才发）：`AT+SPBANDCTRL=0,0,0,0`
	// 是固件认的"读"形态，按 5 组回显（真机：1,0,1,3,4 / 2,A,F / 3,1,8 / 4,34,38,39,40,41 / 5,1,3,5,8），
	// 组 4 ∪ 组 5 与 AT^SYSCFGEX=? 的 LTE 名单逐字相同 ⇒ 4=LTE-TDD、5=LTE-FDD。
	// ⚠️ 别拿它当"写"：`AT+SPBANDCTRL=1,...` 实测 `+CME ERROR: 4`。
	CmdSPBandCtrlQuery = "AT+SPBANDCTRL=0,0,0,0"
)

// UnisocLockCommand 展锐 AT+SP* 锁网方言（见文件头：只做已取证的部分）。
type UnisocLockCommand struct{}

// ---- NetLocker ----

// CommandQueryBandLock 频段锁定状态回读。
//
// LTE：`AT^SYSCFGEX?` 的第 5 个字段就是**当前允许的 LTE 频段位图**（能读、能写）✓
// NR：本机没有可读可写的 NR 频段配置 ⇒ 空串（上层跳过，界面不会编一个假状态）。
//
// ⚠️ `AT+SPCOMMLOCKBAND?` 实测 `+CME ERROR: 21`（读不回来），只留在能力探测/AT 预设里
// 给用户看固件自己的应答，**不当状态回读用**。
func (u *UnisocLockCommand) CommandQueryBandLock(tech lock.CellTech) string {
	if tech == lock.CellTechLTE {
		return cmdSyscfgEXQuery
	}
	return ""
}

// CommandSetBandLock 恒返回空串：这条锁要**先回读再整串改写**（见 SetBandLockAt）
// —— `AT^SYSCFGEX` 5 个字段里第 2 个是 2G/3G 频带码，从零拼会把那个字段写坏。
// 上层会因为本机型实现了 BandLockSetter 而走 SetBandLockAt，不到这里。
func (u *UnisocLockCommand) CommandSetBandLock(lock.CellTech, []int) string { return "" }

// CommandQueryBandCapability 设备能力频段查询：LTE 走 `AT^SYSCFGEX=?`
// （固件自己在模板里列的 `LTE BC1/LTE BC3/...` 名单）；NR 本机不提供 ⇒ 空串。
func (u *UnisocLockCommand) CommandQueryBandCapability(tech lock.CellTech) string {
	if tech == lock.CellTechLTE {
		return cmdSyscfgEXCaps
	}
	return ""
}

// CommandQueryPolicyBand 空串：本机没有"全量 policy 频段"查询。
// 解锁不走这条路（走 UnlockBandAt：写回固件声明的全部频段），也不会下发 0。
func (u *UnisocLockCommand) CommandQueryPolicyBand() string { return "" }

func (u *UnisocLockCommand) CommandQueryCellLock(lock.CellTech) string { return cmdSPCellLockQuery }

// CommandLockCell 只支持**解除**：`AT+SPCELLLOCK=0,0,0` 是实测过的清除形态
// （回 `+SPCELLLOCK:0,0,0,0`，且发它不影响驻网）。
//
// ⚠️ 真正的锁不下发：`(0-10),(0-2),(0-10),()` 四个参数的语义没取证 ——
// 实测 `=1,0,3,140` 只把第 1 个参数存了下来（回显 `1,0,0,0`），
// `=1,1,0,140` / `=1,2,0,140` 这类只回 OK：既无状态回显、也无 `+SPCELLLOCK` 上报，
// 无法确认是否生效。写错目标值会把模组锁出服务，因此留空让界面如实报"不支持"。
func (u *UnisocLockCommand) CommandLockCell(req lock.CellLockRequest) string {
	if req.LockType == lock.CellLockNone {
		return CmdSPCellLockClear
	}
	return ""
}

func (u *UnisocLockCommand) CommandBandLockCaps() string  { return cmdSPCommLockBandCaps }
func (u *UnisocLockCommand) CommandCellLockCaps() string  { return CmdSPCellLockCaps }
func (u *UnisocLockCommand) CommandQueryModePref() string { return cmdSPRATQuery }

// CommandSetModePref 展锐的组网模式写入 = **5G 开关** `AT+SP5GRAN`（不是 `AT+SPRAT`）。
//
// 真机取证（2026-10-06，写入后回读 + 注册状态双向确认）：`AT+SP5GRAN=1` → 5G+4G、
// `=0` → 仅 4G（NR 消失，+CEREG 的制式码变 7 与 13）。本机**没有**「仅 5G」档位 ⇒ 返回空串，
// 让上层如实报"本机不支持该档位"，而不是发一条猜出来的指令。
func (u *UnisocLockCommand) CommandSetModePref(mode string) string {
	switch common.NormalizeModePref(mode) {
	case lock.ModeLTENR5G, lock.ModeAuto:
		return "AT+SP5GRAN=1"
	case lock.ModeLTE:
		return "AT+SP5GRAN=0"
	}
	return ""
}

// ---- LockVendorOps（提供说明文案；不改任何通用流程）----

func (u *UnisocLockCommand) ParseBandLockState(tech lock.CellTech, resp []string) ([]int, bool) {
	if tech != lock.CellTechLTE {
		return nil, false
	}
	cfg, ok := common.ParseSyscfgEX(resp)
	if !ok {
		return nil, false
	}
	bands, ok := ParseUnisocLTEBands(cfg.LTEBand)
	if !ok {
		return nil, false
	}
	// 第二个返回值 = "方言自己声称已锁"。本机没有这样一个标记位，
	// 交给上层用 capable 对比判（允许列表是能力的真子集 = 确实锁了）。
	return bands, false
}
func (u *UnisocLockCommand) ParseCellLockState(lock.CellTech, []string) (lock.CellLock, bool) {
	return lock.CellLock{}, false
}

// CommandUnlockBand 空串：解锁要走 BandLockSetter（盘回读到固件声明的全部频段再写回），
// 单条指令拼不出来（其余字段必须原样带回去）。
func (u *UnisocLockCommand) CommandUnlockBand(lock.CellTech) string { return "" }

// ParseModePref 解析 `+SPRAT: LTE 32`：**只有实测对得上的两个取值**才翻成语义档位
// （真机双向验证：SP5GRAN 1 ↔ `LTE 32`、0 ↔ `LTE 16`），其余原样返回（不猜码表）。
// 这样锁网卡片里的「组网模式」和「组网模式」页面显示的是同一个含义。
func (u *UnisocLockCommand) ParseModePref(resp []string) (string, bool) {
	raw, ok := ParseSPRAT(resp)
	if !ok {
		return "", false
	}
	if sem, ok := UnisocModeFromSPRAT(raw); ok {
		return sem, true
	}
	return raw, true
}

// SetModePrefVendor 不处理 ⇒ 交回上层（上层会因为 CommandSetModePref 为空而报不支持）。
func (u *UnisocLockCommand) SetModePrefVendor(lock.LockATDoer, string) (bool, error) {
	return false, nil
}

// CommandSaveCellLock 空串：本机没有小区锁定，自然也不需要"持久化"这一步。
func (u *UnisocLockCommand) CommandSaveCellLock() string { return "" }

// QueryBandCapability 设备能力频段（界面上的「设备能力」列）。
//
// LTE 取自固件在 `AT^SYSCFGEX=?` 里自己列的 `LTE BC1/LTE BC3/...` 名单；
// 拿不到时退回 `AT+SPBANDCTRL=0,0,0,0`（组 4 = LTE-TDD、组 5 = LTE-FDD，两份名单逐字一致）。
// NR 本机不提供 ⇒ 不返回该键（界面显示 BandCapabilityNote）。
func (u *UnisocLockCommand) QueryBandCapability(at lock.LockATDoer) map[lock.CellTech][]int {
	if at == nil {
		return nil
	}
	if resp, err := at.SendAT(cmdSyscfgEXCaps); err == nil {
		if lte := common.ParseLTEBandNames(resp); len(lte) > 0 {
			return map[lock.CellTech][]int{lock.CellTechLTE: lte}
		}
	} else {
		lock.Logger().Warn("unisoc band capability query failed",
			zap.Int("modem", at.ModemID()), zap.String("cmd", cmdSyscfgEXCaps), zap.Error(err))
	}
	if resp, err := at.SendAT(CmdSPBandCtrlQuery); err == nil {
		if lte := SPBandCtrlLTEBands(resp); len(lte) > 0 {
			return map[lock.CellTech][]int{lock.CellTechLTE: lte}
		}
	}
	return nil
}

// ---------- LTE 锁频段：位图读 / 写（BandLockSetter） ----------

// unisocAllBandsMask 固件 `AT^SYSCFGEX=?` 模板里的 "All bands"（7FFFFFFFFFFFFFFF）
const unisocAllBandsMask = uint64(0x7FFFFFFFFFFFFFFF)

// ParseUnisocLTEBands `AT^SYSCFGEX` 第 5 字段（64 位十六进制位图）→ 频段号列表。
//
// 位图约定：**bit(频段号-1) = LTE B<频段号>**（见文件头：0x1E200000095 ↔ BC1/3/5/8/34/38/39/40/41）。
// "All bands"（7FFFFFFFFFFFFFFF）当成"未限制" ⇒ 返回空清单 + true（而不是"锁了 63 个频段"）。
func ParseUnisocLTEBands(maskHex string) ([]int, bool) {
	s := strings.Trim(strings.TrimSpace(maskHex), `"`)
	if s == "" {
		return nil, false
	}
	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return nil, false
	}
	if v >= unisocAllBandsMask {
		return nil, true
	}
	var out []int
	for b := 1; b <= 64; b++ {
		if v&(uint64(1)<<(b-1)) != 0 {
			out = append(out, b)
		}
	}
	return out, true
}

// UnisocLTEBandMask 频段号列表 → `AT^SYSCFGEX` 第 5 字段的十六进制位图。
// 空列表直接报错：写 0 等于"一个频段都不允许"，实测会让模组直接脱网。
func UnisocLTEBandMask(bands []int) (string, error) {
	if len(bands) == 0 {
		return "", fmt.Errorf("频段列表为空，拒绍写入（写 0 会把 LTE 频段清空导致脱网）")
	}
	var mask uint64
	for _, b := range bands {
		if b < 1 || b > 64 {
			return "", fmt.Errorf("LTE 频段号 %d 超出位图范围（1..64）", b)
		}
		mask |= uint64(1) << (b - 1)
	}
	return fmt.Sprintf("%X", mask), nil
}

// unisocReadSyscfgEX 回读 `AT^SYSCFGEX?` 的 5 个字段（改写前必须先拿到它们）。
func unisocReadSyscfgEX(at lock.LockATDoer) (common.SyscfgEX, error) {
	resp, err := at.SendAT(cmdSyscfgEXQuery)
	if err != nil {
		return common.SyscfgEX{}, err
	}
	cfg, ok := common.ParseSyscfgEX(resp)
	if !ok {
		return common.SyscfgEX{}, fmt.Errorf("AT^SYSCFGEX? 应答无法解析，拒绍改写（避免把 2G/3G 频带码写坏）")
	}
	return cfg, nil
}

// SetBandLockAt 锁 LTE 频段：**先回读再整串改写**
//
//	AT^SYSCFGEX="<组网模式>",<2G/3G 频带码>,<漫游>,<服务域>,<LTE 频段位图>
//
// 只换最后一个字段（位图），前 4 个字段原样带回去 —— 第一个字段是组网模式、
// 第二个是 2G/3G 频带码，从零构造会把它们写坏（实测：第二字段清零后模组搜不到网，
// 要 AT+CFUN=0/AT+CFUN=1 一轮射频才能恢复）。
func (u *UnisocLockCommand) SetBandLockAt(at lock.LockATDoer, tech lock.CellTech, bands []int) error {
	if tech != lock.CellTechLTE {
		return fmt.Errorf("展锐本机只有 LTE 频段可锁（NR 侧没有取证到可读写的频段配置指令）")
	}
	if at == nil {
		return fmt.Errorf("内部错误：缺少 AT 通道")
	}
	mask, err := UnisocLTEBandMask(bands)
	if err != nil {
		return err
	}
	cfg, err := unisocReadSyscfgEX(at)
	if err != nil {
		return err
	}
	cmd := fmt.Sprintf(`AT^SYSCFGEX="%s",%s,%d,%d,%s`, cfg.AcqOrder, cfg.Band, cfg.Roam, cfg.SrvDomain, mask)
	lock.Logger().Info("unisoc lte band lock apply",
		zap.Int("modem", at.ModemID()), zap.Ints("bands", bands),
		zap.String("lte_mask", mask), zap.String("command", cmd))
	return at.SendLockCommand(cmd)
}

// UnlockBandAt 解除 LTE 频段锁定：写回**固件自己声明的全部频段**（`AT^SYSCFGEX=?` 的名单）。
//
// 不用 7FFFFFFFFFFFFFFF（"All bands"）：它只是固件模板里的一个广告值，本项目没实测过；
// 而声明名单算出来的位图与真机出厂值（1e200000095）一致，改回去已真机验证安全（驻网不受影响）。
func (u *UnisocLockCommand) UnlockBandAt(at lock.LockATDoer, tech lock.CellTech) error {
	if tech != lock.CellTechLTE {
		return fmt.Errorf("展锐本机没有 NR 频段锁，无需解除")
	}
	if at == nil {
		return fmt.Errorf("内部错误：缺少 AT 通道")
	}
	caps := u.QueryBandCapability(at)
	bands := caps[lock.CellTechLTE]
	if len(bands) == 0 {
		return fmt.Errorf("拿不到固件声明的 LTE 频段名单（AT^SYSCFGEX=?），拒绍解锁以免把频段写坏")
	}
	return u.SetBandLockAt(at, tech, bands)
}

// ParseSPBandCtrl `AT+SPBANDCTRL=0,0,0,0` 应答 → 组号 → 频段号（升序去重）。
//
//	+SPBANDCTRL: 4,34,38,39,40,41
//	+SPBANDCTRL: 5,1,3,5,8
//
// 频段字段可能是十六进制字符（组 2 是 TD-SCDMA 的 A/F）⇒ 先按十进制试、再按十六进制试。
func ParseSPBandCtrl(resp []string) map[int][]int {
	out := map[int][]int{}
	for _, line := range resp {
		i := strings.Index(strings.ToUpper(line), "+SPBANDCTRL:")
		if i < 0 {
			continue
		}
		f := common.SplitCSVFields(line[i+len("+SPBANDCTRL:"):])
		if len(f) < 2 {
			continue
		}
		group, ok := lock.ParseATInt(f[0])
		if !ok || group <= 0 {
			continue
		}
		var bands []int
		for _, x := range f[1:] {
			t := strings.TrimSpace(x)
			if t == "" {
				continue
			}
			if v, ok := lock.ParseATInt(t); ok {
				bands = append(bands, v)
				continue
			}
			if v, err := strconv.ParseInt(t, 16, 32); err == nil {
				bands = append(bands, int(v))
			}
		}
		if len(bands) > 0 {
			out[group] = common.MergeBands(out[group], bands)
		}
	}
	return out
}

// SPBandCtrlLTEBands `AT+SPBANDCTRL=0,0,0,0` → LTE 频段（组 4 = LTE-TDD、组 5 = LTE-FDD）。
// 组号依据见文件头（与 AT^SYSCFGEX=? 的 LTE 名单、+SPLOCKBAND 的 TD-SCDMA A/F 交叉印证）。
func SPBandCtrlLTEBands(resp []string) []int {
	g := ParseSPBandCtrl(resp)
	return common.MergeBands(g[4], g[5])
}

// BandCapabilityNote 「设备能力」列拿不到时的说明（界面直接显示）。
func (u *UnisocLockCommand) BandCapabilityNote(tech lock.CellTech) string {
	switch tech {
	case lock.CellTechLTE:
		return "设备能力取自固件在 AT^SYSCFGEX=? 里自己列的 LTE 频段名单（同一份名单也是频段位图的来源）。"
	case lock.CellTechNR, lock.CellTechNRNSA:
		return "NR 频段没有可读来源：AT^SYSCFGEX 只管 LTE，+SPCOMMLOCKBAND / +SPLBAND / +SPBANDCTRL 都读不回来 ⇒ NR 这一列留空。"
	}
	return "展锐固件没有这条技术域的设备能力频段查询 ⇒ 这一列留空。"
}

// StatusNote 锁网卡片下方的额外说明。
//
// ⚠️ 这段文字是**纯文本渲染**的（项目约定）：不能出现 markdown 记号、反引号、星号。
func (u *UnisocLockCommand) StatusNote() string {
	return "展锐说明（真机实测）：锁频段能读能写 —— 读 AT^SYSCFGEX? 的第 5 个字段（LTE 频段位图，bit = 频段号-1，" +
		"例如 1e200000095 = 固件自己声明的 BC1/3/5/8/34/38/39/40/41），写入时先回读整串再只换那一个字段" +
		"（AT^SYSCFGEX=\"组网模式\",2G/3G频带码,漫游,服务域,LTE位图）—— 这条指令的前两个字段分别是组网模式与 2G/3G 频带码，" +
		"硬拼会把它们写坏（实测第二字段清零后模组搜不到网，要 AT+CFUN=0 再 AT+CFUN=1 一轮射频才能恢复）。" +
		"NR 频段没有可读写的来源，所以只做 LTE。" +
		"组网模式也能读能写：回读 AT+SPRAT?，写入走 5G 开关 AT+SP5GRAN=（1 = 5G+4G、0 = 仅 4G）。" +
		"小区锁定只能解除：AT+SPCELLLOCK=0,0,0 是实测过的清除形态（回 +SPCELLLOCK:0,0,0,0，不影响驻网）；" +
		"锁小区的目标值（=1,... 的四个参数）实测存不住（写 1,0,3,140 只回显 1,0,0,0），所以不下发。" +
		"AT+SPCOMMLOCKBAND 那一族（锁频段/锁频点）参数语义没取证，状态回读一律 +CME ERROR: 21，而实测很危险：" +
		"=1,12,0,0,0,0,0 会被固件接受（回 OK）但把频段配置清空 ⇒ 立刻掉网（+CEREG 2,0 / +COPS 0），" +
		"而 =0,... 与 =1,12,频段号,... 全被拒（+CME ERROR: 0）⇒ 本项目一律不下发。" +
		"要自己试请在「AT 指令」页手动发（那里有这几个 =? 预设），并在无人值守时避开。"
}
