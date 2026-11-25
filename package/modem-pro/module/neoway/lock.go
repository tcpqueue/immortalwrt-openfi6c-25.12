package neoway

// ====================== 有方锁频段（AT+NWSETBAND） ======================
//
// 命令层原文（QModem `cmds/neoway.sh`）：
//
//	cmd_nwsetband_set()   { at "$1" "AT+NWSETBAND=$2,$3$4"; }   # $2=act  $3=个数  $4=",b1,b2,…"
//	cmd_nwsetband_reset() { at "$1" "AT+NWSETBAND=0"; }
//	cmd_nwsetband_query() { at "$1" "AT+NWSETBAND?"; }
//
// 状态能力解析见 ParseNWSETBAND / NeowayCapability（module/neoway/cell.go 之前就在本包，
// 这两个仍在 lock 的 bandlist_parse.go 里，按"列表式"框架的公共解析保留）。

import (
	"fmt"
	"strconv"
	"strings"

	"modemd/module/common"
	"modemd/module/lock"
)

// NeowayLockCommand Neoway：AT+NWSETBAND=<act>,<个数>,<频段表>
func NeowayLockCommand() *lock.BandListLocker {
	return lock.NewBandListLocker(
		"neoway",
		"AT+NWSETBAND=?",
		func(lock.CellTech) string { return "AT+NWSETBAND?" },
		func(tech lock.CellTech, b []int) string {
			return fmt.Sprintf("AT+NWSETBAND=%s,%d,%s", neowayActNum(tech), len(b), lock.JoinInts(b, ","))
		},
		func(tech lock.CellTech, resp []string) ([]int, bool) {
			// 注意：第二个返回值是 **locked**（是否受限），不是"解析成功"。
			// 有方的应答只给出"当前选了哪些频段"，是否受限要看它是不是全部可用频段的子集，
			// 所以这里一律给 false，由上游拿能力表按同一口径补判。
			bands, ok := ParseNWSETBAND(tech, resp)
			if !ok {
				return nil, false
			}
			return bands, false
		},
		NeowayCapability("AT+NWSETBAND=?"),
		"Neoway：锁频段用 AT+NWSETBAND=<act>,<个数>,<频段表>（act：UMTS=2 / LTE=3 / NR=6）；"+
			"复位 AT+NWSETBAND=0；状态从 `?` 的状态行（`<act>,<个数>,<B3>,<N78>…`）解析，"+
			"能力从 `=?` 的带前缀频段名（B=N LTE / N= NR）解析；本机型无锁小区指令。",
	)
}

// neowayActNum 技术域 → <act> 数字（QModem `vendor/neoway.sh` 的 set_lockband 分派）
func neowayActNum(tech lock.CellTech) string {
	if tech == lock.CellTechNR {
		return "6"
	}
	return "3"
}

// ParseNWSETBAND 解析 `AT+NWSETBAND?` 应答里指定技术域的频段表。
//
// 两种行都要认：带 `+NWSETBAND:` 标记的状态行，以及**不带标记**的裸状态行
// （QModem 自己就是用 `grep -v "+NWSETBAND:"` 把状态行挑出来的）；
// 汇总行（`+NWSETBAND: <组数>`，只有一段）会被字段数过滤掉。
func ParseNWSETBAND(tech lock.CellTech, resp []string) ([]int, bool) {
	for _, line := range resp {
		body := stripNeowayMark(line)
		if body == "" || isATFillerLine(body) {
			continue
		}
		fields := common.SplitCSVFields(body)
		if len(fields) < 2 {
			continue // 汇总行 / 应答回显
		}
		lineTech, ok := neowayLineTech(fields)
		if !ok || lineTech != tech {
			continue
		}
		// 优先用带前缀的频段记号（B3/N78/WB1）；全是裸数字时才按"计数 + 表"推
		var bands []int
		for _, f := range fields[1:] {
			if t, n, ok := neowayBandToken(f); ok && t == tech {
				bands = append(bands, n)
			}
		}
		if len(bands) == 0 {
			nums := lock.ParseCSVInts(strings.Join(fields[1:], ","))
			if len(nums) >= 2 && nums[0] == len(nums)-1 { // 首个数字恰是计数
				nums = nums[1:]
			}
			bands = nums
		}
		if len(bands) == 0 {
			continue
		}
		return bands, true
	}
	return nil, false
}

// NeowayCapability 设备能力：`AT+NWSETBAND=?` 里带前缀的频段名（B= LITE / N= NR / WB= UMTS）
func NeowayCapability(capsCmd string) func(at lock.LockATDoer) map[lock.CellTech][]int {
	return func(at lock.LockATDoer) map[lock.CellTech][]int {
		resp, err := at.SendAT(capsCmd)
		if err != nil || common.RespHasError(resp) {
			return nil
		}
		probe := map[lock.CellTech][]int{}
		for _, line := range resp {
			body := stripNeowayMark(line)
			if body == "" || isATFillerLine(body) {
				continue
			}
			fields := common.SplitCSVFields(body)
			lineTech, hasAct := neowayLineTech(fields)
			if hasAct {
				fields = fields[1:]
			}
			// 紧跟 act 的 `<个数>`：恰等于后面剩下的段数时才当计数丢掉（自校验，不瞎猜）
			if len(fields) >= 2 {
				if n, err := strconv.Atoi(strings.TrimSpace(fields[0])); err == nil && n == len(fields)-1 {
					fields = fields[1:]
				}
			}
			for _, f := range fields {
				if t, n, ok := neowayBandToken(f); ok {
					if t == lock.CellTechLTE || t == lock.CellTechNR {
						probe[t] = append(probe[t], n)
					}
					continue
				}
				// 裸数字：只能靠本行的 act 归类
				if hasAct && (lineTech == lock.CellTechLTE || lineTech == lock.CellTechNR) {
					if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil && n > 0 {
						probe[lineTech] = append(probe[lineTech], n)
					}
				}
			}
		}
		out := make(map[lock.CellTech][]int, 2)
		for tech, bands := range probe {
			if v := lock.UnionInts(bands); len(v) > 0 {
				out[tech] = v
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
}

// neowayBandToken 解析带前缀的频段记号：`B3` / `N78` / `WB1`（返回技术域与频段号）
func neowayBandToken(tok string) (lock.CellTech, int, bool) {
	t := strings.ToUpper(strings.TrimSpace(tok))
	if t == "" {
		return "", 0, false
	}
	var tech lock.CellTech
	switch {
	case strings.HasPrefix(t, "WB"):
		t = t[2:]
		tech = lock.CellTechWCDMA
	case strings.HasPrefix(t, "B"):
		t = t[1:]
		tech = lock.CellTechLTE
	case strings.HasPrefix(t, "N"):
		t = t[1:]
		tech = lock.CellTechNR
	default:
		return "", 0, false
	}
	n, err := strconv.Atoi(t)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return tech, n, true
}

// neowayLineTech 取一行的 act 对应的技术域（act 在每行第一个字段）
func neowayLineTech(fields []string) (lock.CellTech, bool) {
	if len(fields) == 0 {
		return "", false
	}
	s := strings.TrimSpace(fields[0])
	// 有的固件把标记连在第一字段上（`+NWSETBAND:3,...`）
	if i := strings.Index(strings.ToUpper(s), neowayMark); i >= 0 {
		s = s[i+len(neowayMark):]
	}
	return neowayActTech(s)
}

// stripNeowayMark 去掉行首的 `+NWSETBAND:` 标记（同时容忍 `+NWSETBAND:3,…` 这种无空格写法）
func stripNeowayMark(line string) string {
	s := strings.TrimSpace(line)
	if i := strings.Index(strings.ToUpper(s), neowayMark); i >= 0 {
		s = s[i+len(neowayMark):]
	}
	return strings.TrimSpace(s)
}

// isATFillerLine 是否是无意义行（OK / ERROR / AT 回显）
func isATFillerLine(s string) bool {
	u := strings.ToUpper(strings.TrimSpace(s))
	switch u {
	case "", "OK", "ERROR", "AT", "AT+NWSETBAND?", "AT+NWSETBAND=?":
		return true
	}
	return strings.HasPrefix(u, "+CME ERROR") || strings.HasPrefix(u, "+CMS ERROR")
}

const neowayMark = "+NWSETBAND:"

// neowayActTech 反查：act 记号（数字或制式名）→ 技术域
func neowayActTech(act string) (lock.CellTech, bool) {
	a := strings.ToUpper(strings.Trim(strings.TrimSpace(act), `"`))
	switch a {
	case "3", "LTE", "4G":
		return lock.CellTechLTE, true
	case "6", "NR", "NR5G":
		return lock.CellTechNR, true
	}
	return "", false
}
