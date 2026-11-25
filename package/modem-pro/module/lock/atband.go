package lock

import (
	"fmt"
	"strconv"
	"strings"
)

// ====================== 华为 / Sierra 的 AT!BAND 锁频段方言 ======================
//
// 指令与参数依据 QModem 的 `cmds/huawei.sh`、`cmds/sierra.sh`、`vendor/*.sh`：
//
//	查询：AT!BAND?           能力：AT!BAND=?
//	下发：AT!BAND=0F,1,"Custom",<class>,<mask>   然后用 AT!BAND=0F 让自定义模板生效
//	复原：AT!BAND=0F          （模板 0F = 全频段，QModem `cmd_band_reset "0F"`）
//
// <class>（QModem `_set_lockband_nr` 的 case）：GW=0、LTE=1、NR=2。
// <mask>：QModem `_band_list_to_mask` 结尾是
//
//	low=$(printf "%016x" $low); high=$(printf "%016x" $high); echo "$low,$high"
//
// 即**两段 16 位十六进制用逗号拼**：band ≤ 64 放 low 的第 (band-1) 位，
// band ≥ 65 放 high 的第 (band-65) 位（n78 → high 的 bit13），最后 echo 成 "<low>,<high>"。
// 只有 GW（class 0）会 `${bandlist:0:16}` 截成前半段；LTE / NR 都是完整两段。
//
// ⚠️ 这是"频段模板 + 位图"：探测 `AT!BAND?` 被接受才会启用；下发失败会如实返回模组错误；
// 一旦锁错，用 `AT!BAND=0F`（本实现的解锁路径 / 页面"解除锁定"）可立即复原全部频段。
//
// <class> 不回写死：`AT!BAND?` 的应答**行号就是 class**
//（`0 - GW:` / `1 - LTE:` / `2 - NR5G:`，见 `vendor/huawei.sh` 的 `_get_lockband_nr`），
// 所以优先从设备读出来的行号取，读不出才回退到静态的 GW=0 / LTE=1 / NR=2。

type AtBandLocker struct{}

// AtBandClass 静态回退：GW=0 / LTE=1 / NR=2（与 AT!BAND? 的行号一致）
func AtBandClass(tech CellTech) int {
	if tech == CellTechNR {
		return 2
	}
	return 1
}

// atBandTypes 该技术域在应答行里的制式标签（按优先级）
func atBandTypes(tech CellTech) []string {
	if tech == CellTechNR {
		return []string{"NR5G", "NR5G_SA", "NR5G_NSA", "SA", "NSA", "NR"}
	}
	return []string{"LTE"}
}

// AtBandState 解析 AT!BAND? 里的一行：`<class> - <TYPE>:<padding><low16>,<high16>`
//
// QModem `_get_lockband_nr` 的读法：`grep '[0-9]* - '` 挑行、`type=$(cut -d' ' -f3)`、
// `low_band=${cfg_line:11:16}`、`high_band=${cfg_line:28:16}`（= 冒号后两段 16 位位图）。
// 这里用"按冒号切"代替固定列宽，效果相同且更宽容。
func AtBandState(line string) (typ string, class int, low, high string, ok bool) {
	dash := strings.Index(line, " - ")
	if dash < 0 {
		return "", 0, "", "", false
	}
	n, err := strconv.Atoi(strings.TrimSpace(line[:dash]))
	if err != nil {
		return "", 0, "", "", false
	}
	colon := strings.Index(line, ":")
	if colon < dash {
		return "", 0, "", "", false
	}
	typ = strings.TrimSpace(line[dash+len(" - ") : colon])
	parts := strings.Split(line[colon+1:], ",")
	if len(parts) < 2 {
		return "", 0, "", "", false
	}
	low, high = cleanHexField(parts[0]), cleanHexField(parts[1])
	if !isHexBitmap(low) || !isHexBitmap(high) {
		return "", 0, "", "", false
	}
	return typ, n, low, high, true
}

// AtBandStateFor 从 AT!BAND? 应答里找出该技术域的那一行
func AtBandStateFor(resp []string, tech CellTech) (class int, low, high string, ok bool) {
	for _, want := range atBandTypes(tech) {
		for _, line := range resp {
			typ, n, lo, hi, fine := AtBandState(line)
			if fine && strings.EqualFold(typ, want) {
				return n, lo, hi, true
			}
		}
	}
	return 0, "", "", false
}

// ---------- NetLocker ----------

func (b *AtBandLocker) CommandQueryBandLock(CellTech) string { return "AT!BAND?" }

// CommandSetBandLock 空串：AT!BAND 要先建自定义模板再激活（多步），交给 BandLockSetter。
func (b *AtBandLocker) CommandSetBandLock(CellTech, []int) string { return "" }

func (b *AtBandLocker) CommandQueryBandCapability(CellTech) string { return "" }
func (b *AtBandLocker) CommandQueryPolicyBand() string             { return "" }

// 无锁小区指令
func (b *AtBandLocker) CommandQueryCellLock(CellTech) string   { return "" }
func (b *AtBandLocker) CommandLockCell(CellLockRequest) string { return "" }

func (b *AtBandLocker) CommandBandLockCaps() string { return "AT!BAND=?" }
func (b *AtBandLocker) CommandCellLockCaps() string { return "" }

func (b *AtBandLocker) CommandQueryModePref() string     { return "" }
func (b *AtBandLocker) CommandSetModePref(string) string { return "" }

// ---------- BandLockSetter ----------

// SetBandLockAt 建自定义频段模板（0F）并激活
func (b *AtBandLocker) SetBandLockAt(at LockATDoer, tech CellTech, bands []int) error {
	low, high := splitBandBitmap(bands)
	mask := hexBitmapPadded(low) + "," + hexBitmapPadded(high)
	cmd := fmt.Sprintf(`AT!BAND=0F,1,"Custom",%d,%s`, b.bandClassFor(at, tech), mask)
	if err := at.SendLockCommand(cmd); err != nil {
		return err
	}
	return at.SendLockCommand("AT!BAND=0F") // 让模板 0F 生效
}

// AtBandName 一个频段名 token（B1 / N78）解析出的制式与频段号
type AtBandName struct {
	tech CellTech
	band int
}

// AtBandNameOf 构造一个频段名 token。
//
// 为什么需要它：字段故意不导出（外部只能通过 AtBandNameTokens 拿到），
// 但 kernel 的回归测试要自己拼 hoped 值做比较 ⇒ 给一个构造器，
// 比把字段导出（导出会连带改掉本文件里所有 `tech:` / `band:` 写法）安全。
func AtBandNameOf(tech CellTech, band int) AtBandName {
	return AtBandName{tech: tech, band: band}
}

// AtBandNameTokens 从一行里扫出频段名（`B1` / `N78` 这种）。
// 取法照 QModem `vendor/huawei.sh` 的 `*)` 分支：`grep -o '^[BbNn][0-9]*'` 再取数字 ——
// 前缀字母自带制式（B=LTE、N=NR），所以不依赖它在应答里的位置。
func AtBandNameTokens(line string) []AtBandName {
	var out []AtBandName
	isAlnum := func(r rune) bool {
		return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
	}
	for _, tok := range strings.FieldsFunc(line, func(r rune) bool { return !isAlnum(r) }) {
		if len(tok) < 2 {
			continue
		}
		prefix := tok[0]
		if prefix != 'B' && prefix != 'b' && prefix != 'N' && prefix != 'n' {
			continue
		}
		n, err := strconv.Atoi(tok[1:])
		if err != nil || n < 1 || n > 261 {
			continue
		}
		tech := CellTechLTE
		if prefix == 'N' || prefix == 'n' {
			tech = CellTechNR
		}
		out = append(out, AtBandName{tech: tech, band: n})
	}
	return out
}

// atBandCapabilityFromCaps 从 AT!BAND=? 应答里扫出设备能力（只读）。
// `=?` 会列出模板 + 一段 `Available:` 频段名（`B1` / `N78`），按前缀归到 LTE / NR。
func atBandCapabilityFromCaps(resp []string) map[CellTech][]int {
	var lte, nr []int
	for _, line := range resp {
		for _, t := range AtBandNameTokens(line) {
			if t.tech == CellTechNR {
				nr = append(nr, t.band)
			} else {
				lte = append(lte, t.band)
			}
		}
	}
	out := make(map[CellTech][]int, 2)
	if v := UnionInts(lte); len(v) > 0 {
		out[CellTechLTE] = v
	}
	if v := UnionInts(nr); len(v) > 0 {
		out[CellTechNR] = v
	}
	return out
}

// atBandCapsCmd 能力查询（与 QModem `cmd_band_list_query` 同一条）
const atBandCapsCmd = "AT!BAND=?"

// bandClassFor class 取值：**优先用设备自己报的行号**（`AT!BAND?` 的 `1 - LTE:` 这类），
// 读不出（应答格式不符 / 模组不响应）才回退到静态映射 GW=0 / LTE=1 / NR=2。
// 这样即使某个固件把 NR 排在别的行，也不需要改代码。
func (b *AtBandLocker) bandClassFor(at LockATDoer, tech CellTech) int {
	if resp, err := at.SendAT("AT!BAND?"); err == nil {
		if class, _, _, ok := AtBandStateFor(resp, tech); ok {
			return class
		}
	}
	return AtBandClass(tech)
}

// UnlockBandAt 解除限制：**写回“设备能力”里的全部频段**再激活模板 0F。
//
// ⚠️ 不能只发 `AT!BAND=0F` —— 模板 0F 就是锁定那一刻被我们覆盖成 Custom 的那个，
// 再激活它等于把锁定值又写回去（QModem 也没有 AT!BAND 的“恢复全量”写法，
// 它的 reset 只是切模板）。所以这里用 `AT!BAND=?` 拿到的可用频段拼一张全量位图写回去；
// **能力表读不出来就拒发**（宁可不解锁，也不能把频段清空）。
func (b *AtBandLocker) UnlockBandAt(at LockATDoer, tech CellTech) error {
	caps := b.QueryBandCapability(at)
	bands := caps[tech]
	if len(bands) == 0 {
		return fmt.Errorf("AT!BAND=? 里读不出 %s 的可用频段，无法拼出全量位图，拒绝下发解锁指令"+
			"（只发 AT!BAND=0F 会把锁定时的模板值原样切回来，解锁不了）", tech)
	}
	return b.SetBandLockAt(at, tech, bands)
}

// ---------- LockVendorOps ----------

// ParseBandLockState 解析 AT!BAND? 里该制式那一行的两段位图（低段 1-64 / 高段 65-128）。
//
// 第二个返回值（是否受限）一律给 false："是否受限"要跟该固件支持的频段比才知道，
// 而 AT!BAND=? 的 `Available:` 段落格式未确认 → 上游拿不到能力表时保持不臆断。
func (b *AtBandLocker) ParseBandLockState(tech CellTech, resp []string) ([]int, bool) {
	_, low, high, ok := AtBandStateFor(resp, tech)
	if !ok {
		return nil, false
	}
	bands := UnionInts(hexBitmapBands(low, 0), hexBitmapBands(high, 64))
	if len(bands) == 0 {
		return nil, false
	}
	return bands, false
}

func (b *AtBandLocker) ParseCellLockState(CellTech, []string) (CellLock, bool) {
	return CellLock{}, true
}

// CommandUnlockBand 空串：AT!BAND 的解锁要“拼全量位图再写回”，单条指令做不到
// （返回 "AT!BAND=0F" 会被上层优先发出去，而 0F 就是锁定后的 Custom 模板，解不了）
func (b *AtBandLocker) CommandUnlockBand(CellTech) string { return "" }

func (b *AtBandLocker) ParseModePref([]string) (string, bool) { return "", false }
func (b *AtBandLocker) SetModePrefVendor(LockATDoer, string) (bool, error) {
	return false, nil
}
func (b *AtBandLocker) CommandSaveCellLock() string { return "" }

// QueryBandCapability 设备能力：扫 AT!BAND=? 里的频段名（B1/N78），按前缀分 LTE / NR
func (b *AtBandLocker) QueryBandCapability(at LockATDoer) map[CellTech][]int {
	resp, err := at.SendAT(atBandCapsCmd)
	if err != nil || respHasError(resp) {
		return nil
	}
	return atBandCapabilityFromCaps(resp)
}

// BandCapabilityNote 拿不到能力表时的说明
func (b *AtBandLocker) BandCapabilityNote(CellTech) string {
	return "该固件不上报设备能力频段（AT!BAND=? 里扫不到 B1/N78 这类频段名）"
}
func (b *AtBandLocker) StatusNote() string {
	return "华为 / Sierra：锁频段用 AT!BAND=0F,1,\"Custom\",<class>,<mask> 建模板后再 AT!BAND=0F 激活" +
		"（class 直接从 AT!BAND? 的行号读：0=GW / 1=LTE / 2=NR5G；mask 是 <low16>,<high16> 两段十六进制，" +
		"band≤64 进 low、band≥65 进 high）；解除锁定=用 AT!BAND=? 里的可用频段拼全量位图写回（不是只发 AT!BAND=0F）。" +
		"位图格式取自 Modem-Pro `_band_list_to_mask` 的 %016x 两段拼接；本机型无锁小区指令。"
}
