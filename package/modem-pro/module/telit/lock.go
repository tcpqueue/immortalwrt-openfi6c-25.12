package telit

import (
	"fmt"
	"strings"

	"modemd/module/common"
	"modemd/module/lock"
)

// ====================== Telit：AT#BND（尾参是十六进制位图，不是十进制列表） ======================
//
// 依据 QModem 的 `cmds/telit.sh` + `vendor/telit.sh`：
//
//	查询 : AT#BND?  → `#BND: 0,22,<lte1-64>,<lte65-128>`
//	       （脚本用 `awk -F, '{print $3}'` 取 LTE 主段、`$4` 取 LTE 扩展段）
//	能力 : AT#BND=? → `#BND: (g1),(g2),(g3),(g4),(g5),(g6),(g7),(g8)`
//	       g3 = LTE 可用频段、g5 = NSA NR 1-64、g6 = NSA NR 65-128（八组里只用这三组）
//	写入 : AT#BND=0,22,<lte1-64>                                 ← LTE（脚本 lte_bands_to_hex 只出一个字段）
//	       AT#BND=0,22,<lte1-64>,<lte65-128>,<nr1-64>,<nr65-128> ← NR（**必须**把 LTE 两段原样带上）
//
// 位图 → 频段：第 i 位（0 起）对应 band i+1（低位段）/ band i+65（高位段）。
// 位图字符串是 bc `obase=16` 的输出（大写、不补零），回写时原样带上即可。
//
// ⚠️ 因为这个方言的单条指令拼不出来（NR 要带别人的频段、LTE 又要回读扩展段），
// 所以走 BandLockSetter：**先回读、再整串改写**。回读失败时宁可报错，
// 也不下发（否则会把 LTE 的频段表悄悄清掉）。

const telitCapsCmd = "AT#BND=?"

// telitBNDGroups 把 `#BND: a,b,c,…` 切成逗号字段（`?` 与 `=?` 都能用；`=?` 的字段带括号）
func telitBNDGroups(resp []string) ([]string, bool) {
	for _, line := range resp {
		i := strings.Index(strings.ToUpper(line), "#BND:")
		if i < 0 {
			continue
		}
		fields := strings.Split(line[i+len("#BND:"):], ",")
		if len(fields) < 4 {
			continue
		}
		out := make([]string, len(fields))
		for j, f := range fields {
			out[j] = common.CleanHexField(f)
		}
		return out, true
	}
	return nil, false
}

// ---------- NetLocker / LockVendorOps 覆写 ----------

// TelitLocker 在通用「列表式」实现之上覆写：指令形态、状态解析、能力查询、下发方式都不同
type TelitLocker struct{ *lock.BandListLocker }

// ParseBandLockState 解析当前锁定。AT#BND? 只给出 **LTE** 两段位图（NR 当前值不可查），
// 所以 NR 一律返回 false（界面显示原始应答）。
//
// 第二个返回值（是否受限）这里一律给 false —— 需要与"设备能力位图"比较才有意义，
// 上游会用能力表按同一口径（bandLockRestricted）补上。
func (t *TelitLocker) ParseBandLockState(tech lock.CellTech, resp []string) ([]int, bool) {
	if tech != lock.CellTechLTE {
		return nil, false
	}
	g, ok := telitBNDGroups(resp)
	if !ok || !common.IsHexBitmap(g[2]) {
		return nil, false
	}
	bands := lock.UnionInts(common.HexBitmapBands(g[2], 0), common.HexBitmapBands(g[3], 64))
	if len(bands) == 0 {
		return nil, false
	}
	return bands, false
}

// QueryBandCapability 设备能力：AT#BND=? 的 g3（LTE）/ g5+g6（NSA NR）
func (t *TelitLocker) QueryBandCapability(at lock.LockATDoer) map[lock.CellTech][]int {
	resp, err := at.SendAT(telitCapsCmd)
	if err != nil || common.RespHasError(resp) {
		return nil
	}
	g, ok := telitBNDGroups(resp)
	if !ok {
		return nil
	}
	out := make(map[lock.CellTech][]int, 2)
	if lte := common.HexBitmapBands(g[2], 0); len(lte) > 0 {
		out[lock.CellTechLTE] = lte
	}
	if nr := lock.UnionInts(common.HexBitmapBands(g[4], 0), common.HexBitmapBands(g[5], 64)); len(nr) > 0 {
		out[lock.CellTechNR] = nr
	}
	return out
}

// telitCurrentLTE 回读当前 LTE 两段位图（NR 锁定时要原样带上，避免清掉 LTE）
func (t *TelitLocker) telitCurrentLTE(at lock.LockATDoer) (low, high string, ok bool) {
	resp, err := at.SendAT("AT#BND?")
	if err != nil {
		return "", "", false
	}
	g, ok := telitBNDGroups(resp)
	if !ok || !common.IsHexBitmap(g[2]) || !common.IsHexBitmap(g[3]) {
		return "", "", false
	}
	return g[2], g[3], true
}

// SetBandLockAt 锁定频段（先回读、再整串改写）
func (t *TelitLocker) SetBandLockAt(at lock.LockATDoer, tech lock.CellTech, bands []int) error {
	low, high := common.SplitBandBitmap(bands)
	if tech == lock.CellTechLTE {
		// LTE：脚本只用 lte_bands_to_hex 的第一段（LTE 扩展段现实中用不到）
		return at.SendLockCommand(fmt.Sprintf("AT#BND=0,22,%s", common.HexBitmapString(low)))
	}
	// NR：必须把当前 LTE 两段带上
	lteLow, lteHigh, ok := t.telitCurrentLTE(at)
	if !ok {
		return fmt.Errorf("读不出当前 LTE 频段位图（AT#BND? 应答格式不符），" +
			"拒绝下发 NR 锁频 —— 否则会把 LTE 的频段表一起清掉")
	}
	return at.SendLockCommand(fmt.Sprintf("AT#BND=0,22,%s,%s,%s,%s",
		lteLow, lteHigh, common.HexBitmapString(low), common.HexBitmapString(high)))
}

// UnlockBandAt 解除锁定：把该制式写回"设备能力"的那几段位图（即全部可用频段）。
// 能力读不到就报错，不瞎发。
func (t *TelitLocker) UnlockBandAt(at lock.LockATDoer, tech lock.CellTech) error {
	resp, err := at.SendAT(telitCapsCmd)
	if err != nil {
		return err
	}
	g, ok := telitBNDGroups(resp)
	if !ok {
		return fmt.Errorf("AT#BND=? 应答格式不符，无法确定全量频段位图，拒绝下发解锁指令")
	}
	if tech == lock.CellTechLTE {
		if !common.IsHexBitmap(g[2]) {
			return fmt.Errorf("AT#BND=? 里没有可用的 LTE 频段位图，拒绝下发解锁指令")
		}
		return at.SendLockCommand(fmt.Sprintf("AT#BND=0,22,%s", g[2]))
	}
	lteLow, lteHigh, ok := t.telitCurrentLTE(at)
	if !ok {
		lteLow, lteHigh = g[2], g[3] // 读不到就用能力位图顶（LTE 段保持全量）
	}
	if !common.IsHexBitmap(g[4]) || !common.IsHexBitmap(g[5]) {
		return fmt.Errorf("AT#BND=? 里没有可用的 NR 频段位图，拒绝下发解锁指令")
	}
	return at.SendLockCommand(fmt.Sprintf("AT#BND=0,22,%s,%s,%s,%s", lteLow, lteHigh, g[4], g[5]))
}

// StatusNote Telit 的操作提示
func (t *TelitLocker) StatusNote() string {
	return "Telit：锁频段走 AT#BND=0,22,<十六进制位图>（低位段 1-64 / 高位段 65-128，两段逗号分隔）；" +
		"NR 锁定会先回读 AT#BND? 把 LTE 两段原样带上，读不到就拒绝下发（免得清掉 LTE 频段）；" +
		"AT#BND? 只上报 LTE 的当前值，NR 当前值不可查。“是否受限”是与 AT#BND=? 的能力位图比较得出的。"
}

// TelitLockCommand Telit 方言实例（候选探测：AT#BND? / #BND:）
func TelitLockCommand() *TelitLocker {
	return &TelitLocker{lock.NewBandListLocker(
		"telit", telitCapsCmd,
		func(lock.CellTech) string { return "AT#BND?" },
		// setFn 留空：单条指令拼不出来 → CommandSetBandLock 返回空串，由 BandLockSetter 负责
		nil, nil, nil,
		"Telit：锁频段用 AT#BND=0,22,<十六进制位图>；本机型无锁小区指令。",
	)}
}
