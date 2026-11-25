package lock

import (
	"strconv"
	"strings"
)

// ====================== 列表式方言的应答解析 ======================
//
// 依据：QModem 的 `vendor/thales.sh` / `vendor/foxconn.sh` / `vendor/neoway.sh`
// （脚本里给了**逐字样例**，不是靠猜）。
//
// ⚠️ 共同原则：**解析不出来就返回 false**，由上层显示原始应答 ——
// 宁可界面上少一列数据，也不能把错误解读当真（比如把"计数"当频段号）。

// ParseCSVInts 逗号分隔的整数表；非数字项（"LTE" 这类记号、空项、尾逗号）直接跳过。
func ParseCSVInts(s string) []int {
	var out []int
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if n, err := strconv.Atoi(f); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// UnionInts 合并去重并升序（保留入参顺序无关，方便比较/展示）
func UnionInts(lists ...[]int) []int {
	seen := map[int]bool{}
	var out []int
	for _, l := range lists {
		for _, v := range l {
			if seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	sortIntsAsc(out)
	return out
}

func sortIntsAsc(a []int) {
	for i := 1; i < len(a); i++ { // 插入排序：频段表很小，不值得引 sort 包
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// ---------- Thales / Foxconn：AT[+^]BAND_PREF? ----------
//
// QModem `vendor/thales.sh` 注释里给了真机应答样例（一行一个制式，靠逗号分组）：
//
//	WCDMA,Enable Bands :1,2,4,5,8,
//	WCDMA,Disable Bands:
//	LTE,Enable Bands : 1,2,3,4,5,7,8,12,
//	LTE,Disable Bands:
//	NR5G_NSA,Enable Bands :
//	NR5G_NSA,Disable Bands:
//	NR5G_SA,Enable Bands :
//	NR5G_SA,Disable Bands:
//
// 读法（与 QModem 一致）：按 `grep "<RAT>,Enable Bands"` / `"<RAT>,Disable Bands"`
// 分别取出冒号后的逗号表。

// bandPrefRatKeys 该技术域在应答里的 RAT 标签。
//
// NR 会同时给出 `NR5G_NSA` 与 `NR5G_SA` 两组，而写入只有一条 `AT+BAND_PREF=NR5G,2,<表>`
// （QModem `vendor/thales.sh` 原注：AT+BAND_PREF can only set a single NR5G RAT,
// so merge the NR5G_NSA and NR5G_SA band lists into one NR class）
// —— 所以读取时也必须**两组一起合并**，否则 SA 有值时会把 NSA 那部分丢掉。
func bandPrefRatKeys(tech CellTech) []string {
	if tech == CellTechNR {
		return []string{"NR5G_NSA", "NR5G_SA"}
	}
	return []string{"LTE"}
}

// bandPrefLineKind 判断一行属于哪个 RAT 的 Enable / Disable，并取出频段表。
func bandPrefLineKind(line, rat string) (kind string, bands []int, ok bool) {
	i := strings.Index(line, ":")
	if i < 0 {
		return "", nil, false
	}
	head := strings.ToUpper(line[:i])
	if !strings.Contains(head, rat) {
		return "", nil, false
	}
	switch {
	case strings.Contains(head, "ENABLE BANDS"):
		kind = "enable"
	case strings.Contains(head, "DISABLE BANDS"):
		kind = "disable"
	default:
		return "", nil, false
	}
	return kind, ParseCSVInts(line[i+1:]), true
}

// bandPrefCollect 汇总指定 RAT 的 Enable / Disable 频段表
func bandPrefCollect(resp []string, rat string) (enable, disable []int, found bool) {
	for _, line := range resp {
		kind, bands, ok := bandPrefLineKind(line, rat)
		if !ok {
			continue
		}
		found = true
		if kind == "enable" {
			enable = append(enable, bands...)
		} else {
			disable = append(disable, bands...)
		}
	}
	return
}

// BandPrefParseState 解析当前锁定。
//
// "是否受限"的判据：**Enable 是 Disable ∪ Enable 的真子集** —— 固件把一部分频段
// 列为 Enable、其余列为 Disable 时，说明确实只开了那几个；两边加起来就是它认可的频段池。
// （样例里 LTE 的 Disable 为空、Enable 列了 12 个 → 判定"未受限"，符合出厂态。）
func BandPrefParseState(tech CellTech, resp []string) ([]int, bool) {
	var enables, disables [][]int
	found := false
	for _, rat := range bandPrefRatKeys(tech) {
		enable, disable, f := bandPrefCollect(resp, rat)
		if !f {
			continue
		}
		found = true
		if len(enable) > 0 {
			enables = append(enables, enable)
		}
		if len(disable) > 0 {
			disables = append(disables, disable)
		}
	}
	if !found {
		return nil, false
	}
	enable := UnionInts(enables...)
	if len(enable) == 0 {
		return nil, false // 一组 Enable 都是空的（样例里的 NR5G_NSA/SA）→ 不臆造
	}
	all := UnionInts(UnionInts(enables...), UnionInts(disables...))
	return enable, len(enable) < len(all)
}

// BandPrefCapability 设备能力（固件认可的频段池 = Enable ∪ Disable）。
// 用与状态查询同一条指令（`?`）—— 形状已知，不赌 `=?` 的格式。
func BandPrefCapability(queryCmd string) func(at LockATDoer) map[CellTech][]int {
	return func(at LockATDoer) map[CellTech][]int {
		resp, err := at.SendAT(queryCmd)
		if err != nil || respHasError(resp) {
			return nil
		}
		out := make(map[CellTech][]int, 2)
		for _, tech := range []CellTech{CellTechLTE, CellTechNR} {
			var pools [][]int
			for _, rat := range bandPrefRatKeys(tech) {
				enable, disable, found := bandPrefCollect(resp, rat)
				if !found {
					continue
				}
				pools = append(pools, enable, disable)
			}
			if all := UnionInts(pools...); len(all) > 0 {
				out[tech] = all
			}
		}
		return out
	}
}

// ---------- Neoway：AT+NWSETBAND? / AT+NWSETBAND=? ----------
//
// 依据 QModem `vendor/neoway.sh`（写与读都是它）：
//
//	写入：`set_lockband()` 把 band_class 换成**数字** act：UMTS=2 / LTE=3 / NR=6，
//	      再发 `AT+NWSETBAND=<act>,<个数>,<b1,b2,…>`；复位 `AT+NWSETBAND=0`。
//	读取：`get_lockband()` 先拿 `?` 的汇总行（`grep "+NWSETBAND:" | awk '{print $2}'` = 组数，
//	      为 0 表示没锁），再对**其余行** `tr ',' ' '` 后取 `$1` 当 act、`$2` 当个数、
//	      剩下的当频段记号，按 act 分到 UMTS/LTE/NR。
//	能力：`AT+NWSETBAND=?` 里的频段名带前缀（**WB**=UMTS / **B**=LTE / **N**=NR），
//	      脚本从第 2 个逗号字段起扫（`awk -F',' '{for(i=2;i<=NF;i++) print $i}'`）。
//
// ⚠️ 以前本仓库把 act 当成 "LTE"/"NR5G" 文本（写下去固件会回 ERROR），现按脚本改成数字。
// 同时保留对"act 写成制式名"的宽容——有些固件/固件版本确实这么回。
