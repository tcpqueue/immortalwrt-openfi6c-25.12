package quectel

import (
	"fmt"
	"math/big"
	"strings"

	"modemd/module/common"
	"modemd/module/lock"
)

// ====================== 移远旧款 4G：AT+QCFG="band"（LTE 十六进制位图） ======================
//
// 依据 QModem 的 `cmds/quectel.sh` + `vendor/quectel.sh`：
//
//	cmd_qcfg_band_query()  { at "$1" "AT+QCFG=\"band\""; }
//	cmd_qcfg_band_reset()  { at "$1" "AT+QCFG=\"band\",0,$2,0"; }        # $2 = 十六进制位图
//	set_lockband_lte()     { hex=$(convert2hex $lock_band); cmd_qcfg_band_reset "$at_port" "$hex"; }
//	get_lockband_lte()     { cmd_qcfg_band_query | grep '+QCFG:' | awk -F, '{print $3}' ... }
//
// 两处**互相印证**：读应答 `+QCFG: "band",<p2>,<p3>,<p4>` 按逗号切后 `$3`（= 第 2 个参数）是 LTE 位图；
// 而写命令 `AT+QCFG="band",0,<mask>,0` 里的 `<mask>` 也正好是第 2 个参数 —— 读写同一个位。
// `convert2hex` 的位序是 `2^(band-1)` 累加 → **band N → bit(N-1)**（64 位，十六进制）。
//
// ⚠️ 参数位有**两种口径**，本实现按"段数"区分（都能处理）：
//
//	3 段应答 `+QCFG: "band",<a>,<LTE 位图>,<c>`          → LTE 段 = 第 2 段（QModem 口径）
//	4 段应答 `+QCFG: "band",<GSM>,<UMTS>,<LTE>,<TDSCDMA>` → LTE 段 = 第 3 段（Quectel 官方手册口径）
//
// QModem 只按 3 段写。若那台固件其实是 4 段口径，这个写法会把 **LTE 段写成 0**（LTE 全关），
// 所以本实现除了按段数选位置，还**写后回读校验**：目标段没变成我们写的值就**立即回滚**并报错
// —— 宁可不锁，也不把设备留在"写坏"的状态。
//
// ⚠️ 两处刻意的偏差（都是为了不写坏设备，已在 StatusNote / 文档里说明）：
//  1. QModem 写入时把第 1/3 个参数**硬写成 0**（会把另两个制式的位图清零）；
//     这里改成**回读后原样带回**，只替换 LTE 那一段 —— 读不出就拒发。
//  2. QModem 没有"恢复全量"的写法；这里的解锁 = 把 LTE 位图写成**全 1**（16 位十六进制 F），
//     等价于"不限制"，不会把频段清空。
//
// 现代固件走 QNW（本方言只在 QNW 不被接受时才会被选中）；真机 fixture 里
// RM500U-CNV 对 `AT+QCFG="band"` 直接回 `+CME ERROR: 4`，所以探测阶段就会把它排除。

// QcfgBandLocker 移远 QCFG band 方言：频段走 `AT+QCFG="band"`，
// 锁小区 / 组网模式沿用通用的 QNWLOCK / QNWPREFCFG（旧款上这两条通常还在）。
type QcfgBandLocker struct{ *QuectelCommand }

// ---------- 频段（覆写） ----------

// CommandQueryBandLock 只有 LTE 有（这条指令本来就是 4G 时代的 LTE 频段配置）
func (q *QcfgBandLocker) CommandQueryBandLock(tech lock.CellTech) string {
	if tech == lock.CellTechLTE {
		return `AT+QCFG="band"`
	}
	return ""
}

// CommandSetBandLock 空串：要"回读另两段再整串写回"，单条指令拼不出来
func (q *QcfgBandLocker) CommandSetBandLock(lock.CellTech, []int) string { return "" }

func (q *QcfgBandLocker) CommandQueryBandCapability(lock.CellTech) string { return "" }
func (q *QcfgBandLocker) CommandQueryPolicyBand() string                  { return "" }
func (q *QcfgBandLocker) CommandBandLockCaps() string                     { return "" } // 无 `=?` 形态

// ---------- 解析 ----------

// qcfgBandFields 解析 `+QCFG: "band",…` 行，返回**参数段**（去掉开头的 `"band"`；≥ 3 段才算认出来）
func qcfgBandFields(resp []string) ([]string, bool) {
	const mark = `+QCFG:`
	for _, line := range resp {
		u := strings.ToUpper(line)
		if !strings.Contains(u, mark) || !strings.Contains(u, `"BAND"`) {
			continue
		}
		i := strings.Index(u, mark)
		fields := SplitCSVFields(line[i+len(mark):])
		// fields[0] = `"band"`，后面是各参数段
		if len(fields) < 4 {
			continue
		}
		return fields[1:], true
	}
	return nil, false
}

// QcfgBandLTEIndex 取"LTE 位图"那一段的下标：
//
//	3 段 → 1（QModem 口径：<a>,<LTE>,<c>）
//	4 段及以上 → 2（Quectel 手册口径：<GSM>,<UMTS>,<LTE>[,<TDSCDMA>]）
func QcfgBandLTEIndex(resp []string) ([]string, int, bool) {
	fs, ok := qcfgBandFields(resp)
	if !ok {
		return nil, -1, false
	}
	if len(fs) == 3 {
		return fs, 1, true
	}
	return fs, 2, true
}

// qcfgBandLayoutName 段数 → 口径名（报错文案用）
func qcfgBandLayoutName(n int) string {
	if n == 3 {
		return "Modem-Pro 三段"
	}
	return "Quectel 手册四段"
}

// SplitCSVFields 按逗号切字段并去掉引号/空白（实现在 module/common，本包留别名不破坏老调用）
var SplitCSVFields = common.SplitCSVFields

// ParseBandLockState 解析 LTE 段位图 → 频段（band N → bit(N-1)）；是否受限交给上游按能力表判定
func (q *QcfgBandLocker) ParseBandLockState(tech lock.CellTech, resp []string) ([]int, bool) {
	if tech != lock.CellTechLTE {
		return nil, false
	}
	fs, idx, ok := QcfgBandLTEIndex(resp)
	if !ok {
		return nil, false
	}
	bands := common.HexBitmapBands(fs[idx], 0) // band N → bit(N-1)
	if len(bands) == 0 {
		return nil, false
	}
	return bands, false
}

// ParseCellLockState 锁小区沿用 QNWLOCK（旧款上通常还在）
func (q *QcfgBandLocker) ParseCellLockState(tech lock.CellTech, resp []string) (lock.CellLock, bool) {
	return ParseQNWLOCK(resp, LockKind(tech))
}

// CommandUnlockBand 空串：解锁要"回读保留 + 写全 1 位图"，走 BandLockSetter
func (q *QcfgBandLocker) CommandUnlockBand(lock.CellTech) string { return "" }

func (q *QcfgBandLocker) ParseModePref([]string) (string, bool) { return "", false }
func (q *QcfgBandLocker) SetModePrefVendor(lock.LockATDoer, string) (bool, error) {
	return false, nil
}

// CommandSaveCellLock 小区锁定后的持久化：沿用移远那条（本方言只换了频段指令）
func (q *QcfgBandLocker) CommandSaveCellLock() string { return QNWLockSaveCmd }

// QueryBandCapability 这条指令没有"能力查询"形态 → 拿不到能力表（界面显示 BandCapabilityNote）
func (q *QcfgBandLocker) QueryBandCapability(lock.LockATDoer) map[lock.CellTech][]int { return nil }

func (q *QcfgBandLocker) BandCapabilityNote(lock.CellTech) string {
	return "该固件不提供频段能力查询（AT+QCFG=\"band\" 没有 ? 形态），“是否受限”不判定"
}

func (q *QcfgBandLocker) StatusNote() string {
	return "移远旧款 4G：锁频段走 AT+QCFG=\"band\"（LTE 位图，band N → bit(N-1)，十六进制）；" +
		"本实现先回读，按**段数**定位 LTE 段（3 段 → 第 2 段；4 段 → 第 3 段），只替换那一段，" +
		"下发后回读校验，对不上立刻回滚并报错；解除锁定 = LTE 段写全 1（不限制）。" +
		"锁小区 / 组网模式沿用 QNWLOCK / QNWPREFCFG。"
}

// ---------- BandLockSetter ----------

// qcfgCurrent 回读：段数够、LTE 段是合法位图才认
func (q *QcfgBandLocker) qcfgCurrent(at lock.LockATDoer) (fields []string, idx int, ok bool) {
	resp, err := at.SendAT(`AT+QCFG="band"`)
	if err != nil || common.RespHasError(resp) {
		return nil, -1, false
	}
	fs, ix, ok := QcfgBandLTEIndex(resp)
	if !ok || !common.IsHexBitmap(common.CleanHexField(fs[ix])) {
		return nil, -1, false
	}
	return fs, ix, true
}

// qcfgBandEdit 把 LTE 段换成 lteMask，其余段原样保留 → 完整指令
func qcfgBandEdit(fields []string, idx int, lteMask string) string {
	out := append([]string(nil), fields...)
	out[idx] = strings.ToUpper(common.CleanHexField(lteMask))
	return `AT+QCFG="band",` + strings.Join(out, ",")
}

// qcfgBandApply 下发 → **回读校验** → 对不上就回滚。
//
// 校验是这条方言的关键：3/4 段两种口径如果猜错位置，会把 LTE 段写成 0（LTE 全关）。
// 所以写完必须回读确认"目标段 == 我们写的值"，否则把原值整串写回并报错。
func (q *QcfgBandLocker) qcfgBandApply(at lock.LockATDoer, fields []string, idx int, lteMask string) error {
	orig := `AT+QCFG="band",` + strings.Join(fields, ",")
	if err := at.SendLockCommand(qcfgBandEdit(fields, idx, lteMask)); err != nil {
		return err
	}
	got, gotIdx, ok := q.qcfgCurrent(at)
	if ok && gotIdx == idx && strings.EqualFold(common.CleanHexField(got[idx]), common.CleanHexField(lteMask)) {
		return nil
	}
	// 回滚（尽力而为）：把原始整串写回去
	_ = at.SendLockCommand(orig)
	detail := "回读不到应答"
	if ok {
		detail = fmt.Sprintf("回读第 %d 段 = %q（下发的是 %q）", idx+1, strings.TrimSpace(got[idx]), lteMask)
	}
	return fmt.Errorf("AT+QCFG=\"band\" 写入未被固件接受（%s），已回滚为原值；"+
		"本机 %d 段应答 → 按%s口径取第 %d 段，若该固件是另一种口径请报修",
		detail, len(fields), qcfgBandLayoutName(len(fields)), idx+1)
}

// qcfgBandMaskHex 频段表 → 64 位十六进制位图（band N → bit(N-1)），与 QModem `convert2hex` 同口径
func qcfgBandMaskHex(bands []int) (string, error) {
	v := new(big.Int)
	for _, b := range bands {
		if b >= 1 && b <= 64 {
			v.SetBit(v, b-1, 1)
		}
	}
	if v.Sign() == 0 {
		return "", fmt.Errorf("频段表 %v 里没有 LTE 频段（1-64）", bands)
	}
	return strings.ToUpper(v.Text(16)), nil
}

// SetBandLockAt 锁定 LTE：回读 → 只替换 LTE 段 → 整串写回 → 校验（失败回滚）
func (q *QcfgBandLocker) SetBandLockAt(at lock.LockATDoer, tech lock.CellTech, bands []int) error {
	if tech != lock.CellTechLTE {
		return fmt.Errorf("AT+QCFG=\"band\" 只支持 LTE 频段（本方言没有 NR 写法）")
	}
	fields, idx, ok := q.qcfgCurrent(at)
	if !ok {
		return fmt.Errorf(`AT+QCFG="band" 应答读不出参数段（需要 +QCFG: "band",… 且 LTE 段是十六进制位图），` +
			`拒绝下发 —— 否则会把别的制式位图一起改掉`)
	}
	mask, err := qcfgBandMaskHex(bands)
	if err != nil {
		return err
	}
	return q.qcfgBandApply(at, fields, idx, mask)
}

// UnlockBandAt 解除：LTE 段写全 1（16 个 F = 不限制），其余段原样保留 → 校验（失败回滚）
func (q *QcfgBandLocker) UnlockBandAt(at lock.LockATDoer, tech lock.CellTech) error {
	if tech != lock.CellTechLTE {
		return fmt.Errorf("AT+QCFG=\"band\" 只支持 LTE 频段（本方言没有 NR 写法）")
	}
	fields, idx, ok := q.qcfgCurrent(at)
	if !ok {
		return fmt.Errorf(`AT+QCFG="band" 应答读不出参数段，拒绝下发解锁指令`)
	}
	return q.qcfgBandApply(at, fields, idx, strings.Repeat("F", 16))
}

// QcfgBandCommand 方言实例
func QcfgBandCommand() *QcfgBandLocker {
	return &QcfgBandLocker{QuectelCommand: &QuectelCommand{}}
}
