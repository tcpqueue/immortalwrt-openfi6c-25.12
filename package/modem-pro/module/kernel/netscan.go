package kernel

import (
	"encoding/hex"
	"errors"
	"fmt"
	"go.uber.org/zap"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"modemd/module/cell"
	"modemd/module/common"
)

// ====================== 主动扫网（network scan） ======================
//
// 与「基站扫描」（ScanCells：服务小区 + 邻区，读的是模组**已驻留**时的测量结果）不同，
// 这里做的是**主动扫描**：让模组自己去搜，条目更多但更慢、且会短暂影响业务。
//
// 两条腿，都按已有证据实现，不发未验证指令：
//
//  1. **3GPP 标准** `AT+COPS=?` —— 列出当前可见的运营商（所有 3GPP 模组都支持）。
//     QModem 的 `vendor/*.sh` 里也在用它做"手动选网"的候选列表。
//  2. **芯讯通的主动扫小区** —— QModem `cmds/simcom.sh` + `vendor/simcom.sh` 的唯一主动扫描实现：
//
//     cmd_cnwsearch_query() { at "$1" "AT+CNWSEARCH=\"$2\""; }        # $2 = nr5g / lte
//     cmd_cnwsearch_scan()  { at "$1" "AT+CNWSEARCH=\"$2\",$3"; }     # $3 = 模式（lte→1、nr5g→3）
//
//     脚本里的流程是「先 query → 等待 → 再 scan」：NR 等 10 秒、LTE 等 5 秒
//     （`get_neighborcell_qualcomm()`：`sleep 10` / `sleep 5`）。
//     扫到的行按 `+NR_NGH_CELL:` / `+LTE_CELL:` 解析，字段下标直接取自那段脚本。
//
// ⚠️ 移远**没有**主动扫网指令（QModem 的 `cmds/quectel.sh` 里只有 `AT+QCFG="nwscanmode"`，
// 那是"制式偏好"不是扫描），所以移远机型只做第 1 条（可见运营商），并如实说明原因。

// 「等模组搜完」的最长时长由**调用方给**（界面上的 3~10 分钟下拉就是它），
// 不再写死一个值：`AT+COPS=?` 是"全网搜索"（不是读缓存），实测同一台广和通 Fibocom，
// 数据业务空闲时 8 秒就回、数据业务在跑时能拖到 3 分钟以上再回 ERROR，
// 该等多久只有用户自己知道。调用方传 <=0 时用 netScanCOpsTimeout 兜底。
//
// 主动扫小区的取结果指令（`AT+CNWSEARCH=...,<mode>`）同理，不能用默认的 3s。
// 扫描等待时长按 QModem 实测值（NR 10s / LTE 5s）。
const (
	netScanMinMinutes     = 3  // 下拉下限
	netScanMaxMinutes     = 10 // 下拉上限（再大就是白占串口了）
	netScanDefaultMinutes = 3  // 不传 minutes 时的默认
)

// NetScanScope 主动扫网的**制式范围**：单制式，或「LTE + NR 一起扫」。
//
// 它只影响**步骤 2（扫小区）**：`AT+COPS=?` 列的是"本机能看到的所有运营商"，
// 与制式无关，所以一起扫不会把它跑两遍。
//
// ⚠️ 取值故意用**大写制式名**：结果里的 `Tech` 字段一直就是这个口径
// （以前直接拄 `CellTech`，取值 `LTE`/`NR`），改小写会破坏现有脚本/文档的判断。
type NetScanScope string

const (
	NetScanLTE  NetScanScope = "LTE"
	NetScanNR   NetScanScope = "NR"
	NetScanBoth NetScanScope = "BOTH" // LTE + NR 各扫一遍，小区结果合并
)

// 都是包级变量，便于测试临时调小。
var (
	netScanCOpsTimeout  = netScanDefaultMinutes * time.Minute
	netScanCellTimeout  = 120 * time.Second
	netScanProbeTimeout = 10 * time.Second
	netScanWaitNR       = 10 * time.Second
	netScanWaitLTE      = 5 * time.Second
)

// ---- 「立即停止」：用户点按钮时立刻打断阻塞中的 AT 读 ----
//
// 一次扫网可能占着串口好几分钟，光靠超时等不现实。
// 做法：ScanNetwork 开始时登记一个 stop 通道，停止接口关掉它 →
// readResponseLockedTimeoutStop 的 select 立刻返回 errATStopped。

// beginScan 登记一次可中断的主动扫网，返回 stop 通道（关闭它即停止）。
//
// 已有扫网在跑时**先把它的 stop 关掉**：新扫网取代旧的。
// 否则两条扫网会一起在串口上排队 —— 用户点「停止扫描」只能停掉后一条，
// 前一条还在慢慢轮，体感就是"停了也不见快"。
func (mod *Modem) beginScan() chan struct{} {
	mod.scanMu.Lock()
	defer mod.scanMu.Unlock()
	if mod.scanStop != nil {
		select {
		case <-mod.scanStop: // 已经关过了
		default:
			close(mod.scanStop)
		}
	}
	ch := make(chan struct{})
	mod.scanStop = ch
	return ch
}

// endScan 清除登记（必须是本次那个通道，避免清掉别人的）。
func (mod *Modem) endScan(ch chan struct{}) {
	mod.scanMu.Lock()
	defer mod.scanMu.Unlock()
	if mod.scanStop == ch {
		mod.scanStop = nil
	}
}

// AbortNetworkScan 请求立即停止正在跑的主动扫网；没有在跑时返回 false。
// 不碰 atMu（扫网期间 atMu 被占着）—— 等待它就谈不上“立即”了。
func (mod *Modem) AbortNetworkScan() bool {
	mod.scanMu.Lock()
	defer mod.scanMu.Unlock()
	if mod.scanStop == nil {
		return false
	}
	select {
	case <-mod.scanStop: // 已经关过了
	default:
		close(mod.scanStop)
	}
	return true
}

// 单行原始应答 / 总行数的截断上限（应答可能有几十行，全塞给前端会很大）
const (
	netScanRawLineMax = 400
	netScanRawMax     = 60
)

// reCopsGroup 取 `AT+COPS=?` 应答里的 `(...)` 组
var reCopsGroup = regexp.MustCompile(`\(([^)]*)\)`)

// decodeHexOperatorName 把 `+COPS=?` 里**被 HEX 字符集编成十六进制**的运营商名还原成可读文本。
//
// 为什么会有这种形态：`AT+CSCS` 决定 AT 命令里字符串的编码。展锐 UMS9620（中兴 F50）
// 出厂就是 `AT+CSCS="HEX"`，实测同一份扫网应答里**两种形态同时出现**：
//
//	+COPS: (2,"4E2D56FD75354FE1","4E2D56FD75354FE1","46011",11),   ← 中国电信（UTF-16BE 的十六进制）
//	       (3,"CHINA MOBILE","CMCC","46000",7),…                    ← 纯 ASCII 名字原样回
//
// 不处理的话，界面上的运营商名就是一串 `4E2D56FD75354FE1`。
//
// 判据（**宁可不还原，也绝不能把真名字改错**）：
//  1. 长度是偶数、全部是十六进制字符；
//  2. 按 UTF-16BE 解出来的**每一个**字符都是 ASCII 可打印字符或汉字；
//  3. 解出来至少有一个非 ASCII 字符（纯 ASCII 的不动 —— 那种情况下原字符串本来就正常）。
//
// 三条都满足才当作 HEX 形态还原；否则原样返回。
func decodeHexOperatorName(s string) string {
	if len(s) < 4 || len(s)%2 != 0 || len(s) > 64 {
		return s
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return s
		}
	}
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) == 0 || len(raw)%2 != 0 {
		return s
	}
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		units = append(units, uint16(raw[i])<<8|uint16(raw[i+1]))
	}
	var sb strings.Builder
	nonASCII := false
	for _, r := range utf16.Decode(units) {
		if r == '\uFFFD' {
			return s // 解密失败（孤立代理对）
		}
		if r > 0x7F {
			// 只放行汉字：字母型名字（CHN-UNICOM / CMCC…）本来就不是 hex，
			// 而 "BEEF" 这类**凑巧全是十六进制字符**的纯 ASCII 名字会被误解成韩文 —— 挡掉。
			if !unicode.Is(unicode.Han, r) {
				return s
			}
			nonASCII = true
		} else if !unicode.IsPrint(r) {
			return s
		}
		sb.WriteRune(r)
	}
	if !nonASCII {
		return s
	}
	out := strings.TrimSpace(sb.String())
	if out == "" {
		return s
	}
	return out
}

// OperatorEntry 一个可见运营商（`AT+COPS=?` 的一项）
type OperatorEntry struct {
	Status  int    `json:"status"`          // 0=未知 1=可用 2=当前 3=禁用
	Long    string `json:"long,omitempty"`  // 长名（如 CHN-UNICOM）
	Short   string `json:"short,omitempty"` // 短名（如 UNICOM）
	Numeric string `json:"numeric,omitempty"`
	ACT     string `json:"act,omitempty"` // 接入技术位掩码（原样文本，不臆造含义）
	Raw     string `json:"raw,omitempty"`
}

// NetworkScanResult 一次主动扫网的结果
type NetworkScanResult struct {
	ModemID    int             `json:"modem_id"`
	ScannedAt  time.Time       `json:"scanned_at"`
	Tech       string          `json:"tech"`
	DurationMs int64           `json:"duration_ms"`
	Operators  []OperatorEntry `json:"operators,omitempty"`
	OperatorCm string          `json:"operator_cmd,omitempty"`
	Cells      []cell.CellInfo `json:"cells,omitempty"`
	CellCmd    string          `json:"cell_cmd,omitempty"`
	CellNote   string          `json:"cell_note,omitempty"`
	// WaitMs 本次实际允许的"等模组搜完"时长（回显给界面/导出，说明这份结果是在多大预算下抓的）
	WaitMs int64    `json:"wait_ms,omitempty"`
	Steps  []string `json:"steps,omitempty"` // 实际发了什么 / 等了多久（现场核对用）
	Raw    []string `json:"raw,omitempty"`   // 原始应答（截断）
	Note   string   `json:"note,omitempty"`
}

// ParseCOPSList 解析 `AT+COPS=?` 应答里的可见运营商列表。
//
// 格式（3GPP 27.007）：
//
//	+COPS: (2,"CHN-UNICOM","UNICOM","46001",7),(1,"CHN-MOBILE","CMCC","46000",7),,(0-1,2)
//
// 末尾那组 `(0-1,2)` 是"本模组支持的技术"，只有数字/范围没有运营商名 → 跳过。
func ParseCOPSList(resp []string) []OperatorEntry {
	// 3GPP 允许这份列表**跨行**（第二行起直接以 `(` 开头，没有 `+COPS:` 前缀），
	// 所以先把 `+COPS:` 行与紧随其后的续行拼在一起再解析。
	var joined []string
	collecting := false
	for _, line := range resp {
		if strings.Contains(strings.ToUpper(line), "+COPS:") {
			joined = append(joined, line)
			collecting = true
			continue
		}
		if !collecting {
			continue
		}
		t := strings.TrimSpace(line)
		up := strings.ToUpper(t)
		if t == "" || up == "OK" || strings.HasPrefix(up, "ERROR") ||
			strings.HasPrefix(up, "+CME") || strings.HasPrefix(up, "+CMS") {
			collecting = false
			continue
		}
		if strings.HasPrefix(t, "(") || strings.HasPrefix(t, ",") {
			joined = append(joined, line)
		}
	}

	var out []OperatorEntry
	seen := map[string]bool{}
	for _, line := range joined {
		for _, m := range reCopsGroup.FindAllStringSubmatch(line, -1) {
			raw := "(" + m[1] + ")"
			f := common.SplitCSVFields(m[1])
			if len(f) < 4 {
				continue // 没有名字/号码的那组（"支持的技术"）→ 不是运营商
			}
			// ⚠️ 末尾那组 `(0,1,2,3,4,5)` 是"本模组支持的技术"，但它有 6 段、
			// 光看段数过得了 len>=4，会被当成一个叫 "1" 的运营商（界面就会多一行
			// 「1 · 3 · ACT 4 · 未知」，实机截图上就看到了）。
			// 判据：真正的运营商项里 <numeric> 是 MCC+MNC（5~6 位数字），
			// 且 long/short 是**带引号**的字符串（raw 里能看到 `"`）。
			if !looksLikePLMN(f[3]) && !strings.Contains(raw, `"`) {
				continue
			}
			status, ok := common.ParseATInt(f[0])
			if !ok {
				continue
			}
			e := OperatorEntry{
				Status:  status,
				Long:    decodeHexOperatorName(strings.TrimSpace(f[1])),
				Short:   decodeHexOperatorName(strings.TrimSpace(f[2])),
				Numeric: strings.TrimSpace(f[3]),
				Raw:     raw,
			}
			if len(f) >= 5 {
				e.ACT = strings.TrimSpace(f[4])
			}
			if e.Long == "" && e.Short == "" && e.Numeric == "" {
				continue
			}
			key := e.Numeric + "|" + e.Long
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, e)
		}
	}
	// 当前在网的排最前，其余按状态、然后按号码
	sortOperatorEntries(out)
	return out
}

// looksLikePLMN 判断 `+COPS=?` 里的 <numeric> 是不是 MCC+MNC（5 或 6 位纯数字）。 用来把末尾"支持的技术"那组（`(0,1,2,3,4,5)`，字段都是裸数字）与真正的运营商项区分开。
func looksLikePLMN(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 5 && len(s) != 6 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sortOperatorEntries 排序：当前(2) → 可用(1) → 其它；同档按 Numeric
func sortOperatorEntries(list []OperatorEntry) {
	rank := func(s int) int {
		switch s {
		case 2:
			return 0
		case 1:
			return 1
		case 3:
			return 2
		default:
			return 3
		}
	}
	for i := 1; i < len(list); i++ { // 列表很小，插入排序足够
		for j := i; j > 0; j-- {
			a, b := list[j-1], list[j]
			ra, rb := rank(a.Status), rank(b.Status)
			if ra < rb || (ra == rb && a.Numeric <= b.Numeric) {
				break
			}
			list[j-1], list[j] = list[j], list[j-1]
		}
	}
}

// ParseCNWSEARCHCells 解析芯讯通主动扫描的输出（`+NR_NGH_CELL:` / `+LTE_CELL:`）。
//
// 字段下标取自 QModem `vendor/simcom.sh` 的 `get_neighborcell_qualcomm()`：
//
//	+NR_NGH_CELL:<arfcn>,<pci>,<rsrp>,<rsrq>,…
//	+LTE_CELL:<mnc>,<?>,<?>,<band>,<arfcn>,<pci>,<rsrp>,<rsrq>,…
//
// ⚠️ 该脚本对 LTE 行的取值是 `$5=band $6=arfcn $7=pci $8=rsrp $9=rsrq`，
// 而 `$1` 是"前缀+第一个值"（`+LTE_CELL:<v>`）—— 换算成"去掉前缀后从 0 开始"就是
// f[3]=band、f[4]=arfcn、f[5]=pci、f[6]=rsrp、f[7]=rsrq，本实现按这个来。
// 其余字段（MNC 等）语义没把握 → **不填**，只在 Raw 里保留整行，避免显示错数据；
// 每一格都做范围校验，不合法就留空。
func ParseCNWSEARCHCells(resp []string) []cell.CellInfo {
	var out []cell.CellInfo
	for _, line := range resp {
		u := strings.ToUpper(line)
		switch {
		case strings.Contains(u, "+NR_NGH_CELL:"):
			f := common.SplitCSVFields(line[strings.Index(u, "+NR_NGH_CELL:")+len("+NR_NGH_CELL:"):])
			if len(f) < 2 {
				continue
			}
			ci := cell.CellInfo{Tech: CellTechNR, Raw: strings.TrimSpace(line)}
			if v, ok := scanIntIn(f[0], 1, 2016667); ok {
				ci.EARFCN = v
			}
			if v, ok := scanIntIn(f[1], 0, cell.PcidMax(CellTechNR)); ok {
				ci.PCID = v
			}
			if len(f) > 2 {
				if v, ok := scanIntIn(f[2], -156, -25); ok {
					ci.RSRP = v
				}
			}
			if len(f) > 3 {
				if v, ok := scanIntIn(f[3], -45, 25); ok {
					ci.RSRQ = v
				}
			}
			if ci.EARFCN == 0 && ci.PCID == 0 {
				continue
			}
			out = append(out, ci)
		case strings.Contains(u, "+LTE_CELL:"):
			f := common.SplitCSVFields(line[strings.Index(u, "+LTE_CELL:")+len("+LTE_CELL:"):])
			if len(f) < 6 {
				continue
			}
			ci := cell.CellInfo{Tech: CellTechLTE, Raw: strings.TrimSpace(line)}
			if v, ok := scanIntIn(f[3], 1, 261); ok {
				ci.Band = v
			}
			if v, ok := scanIntIn(f[4], 1, 65535); ok {
				ci.EARFCN = v
			}
			if v, ok := scanIntIn(f[5], 0, cell.PcidMax(CellTechLTE)); ok {
				ci.PCID = v
			}
			if len(f) > 6 {
				if v, ok := scanIntIn(f[6], -156, -25); ok {
					ci.RSRP = v
				}
			}
			if len(f) > 7 {
				if v, ok := scanIntIn(f[7], -45, 25); ok {
					ci.RSRQ = v
				}
			}
			if ci.EARFCN == 0 && ci.PCID == 0 {
				continue
			}
			out = append(out, ci)
		}
	}
	return out
}

// scanIntIn 解析整数并要求落在 [lo,hi]（判定不了就 ok=false，宁可留空）
func scanIntIn(s string, lo, hi int) (int, bool) {
	v, ok := common.ParseATInt(s)
	if !ok || v < lo || v > hi {
		return 0, false
	}
	return v, true
}

// ratLabel 把内部 rat 名（lte / nr5g）翻成说明里用的制式名。
func ratLabel(rat string) string {
	if rat == "nr5g" {
		return "NR"
	}
	return "LTE"
}

// joinNotes 把若干句说明拼成一行（空串丢掉，分号分隔）；返回空串 = 这一步一切正常。
func joinNotes(notes []string) string {
	kept := make([]string, 0, len(notes))
	for _, n := range notes {
		if strings.TrimSpace(n) != "" {
			kept = append(kept, n)
		}
	}
	return strings.Join(kept, "；")
}

// ScanNetwork 主动扫网：可见运营商（3GPP）+ 主动扫小区（芯讯通 AT+CNWSEARCH）。
//
// scope 决定扫小区扫哪些制式：LTE / NR / **BOTH（LTE 与 NR 各扫一遍、结果合并）**。
// scanTimeout 是"等模组搜完"的最长时长（界面上 3~10 分钟的下拉就是它；<=0 用默认值）。
// 后面"扫小区"那几步另有几秒的固定预算，不含在这个值里（both 走两遍，多花十几秒）。
//
// ⚠️ 会占用 AT 口较久，接口层要提示用户；随时可被 AbortNetworkScan 叫停。
// 任何一步失败都只记日志 / 写进 Note，不影响另一部分结果。
func (mod *Modem) ScanNetwork(scope NetScanScope, scanTimeout time.Duration) (*NetworkScanResult, error) {
	if scanTimeout <= 0 {
		scanTimeout = netScanCOpsTimeout
	}
	start := time.Now()
	// 只认 lte / nr / both；其它（空串、老调用方传的未知制式）一律按 LTE
	switch scope {
	case NetScanLTE, NetScanNR, NetScanBoth:
	default:
		scope = NetScanLTE
	}
	res := &NetworkScanResult{
		ModemID:   mod.ID,
		ScannedAt: start,
		Tech:      string(scope),
		WaitMs:    scanTimeout.Milliseconds(),
	}

	// 登记本次扫网可中断（页面上的「停止扫描」）：因为要跑几分钟，用户得能随时叫停。
	stop := mod.beginScan()
	defer mod.endScan(stop)

	// ---- 步骤 1：3GPP 可见运营商 ----
	res.OperatorCm = "AT+COPS=?"
	res.Steps = append(res.Steps, res.OperatorCm)
	if resp, err := mod.SendATStop(res.OperatorCm, scanTimeout, stop); err != nil {
		if errors.Is(err, errATStopped) {
			res.Note = "已手动停止扫描（模组内部可能仍在完成本次搜索，稍后几条查询可能偏慢）"
			res.DurationMs = time.Since(start).Milliseconds()
			return res, nil
		}
		logger.Warn("network scan: AT+COPS=? failed",
			zap.Int("modem", mod.ID), zap.Error(err))
		res.Note = "可见运营商查询没拿到结果（无卡 / 未注册 / 固件不支持时如此），可稍后重试"
	} else {
		res.Operators = ParseCOPSList(resp)
		res.Raw = appendNetScanRaw(res.Raw, res.OperatorCm, resp)
		if len(res.Operators) == 0 {
			res.Note = "模组没有上报可见运营商（AT+COPS=? 应答里没有可解析的运营商项）"
		}
	}

	// ---- 步骤 2：主动扫小区（QModem 里只有芯讯通这一条） ----
	// 「一起扫」= 两个制式**各走一遍**流程再合并 —— 模组一次只认一个 rat 参数。
	type ratPlan struct {
		rat, mode string
		wait      time.Duration
	}
	var plans []ratPlan
	switch scope {
	case NetScanNR:
		plans = []ratPlan{{"nr5g", "3", netScanWaitNR}}
	case NetScanBoth:
		plans = []ratPlan{{"lte", "1", netScanWaitLTE}, {"nr5g", "3", netScanWaitNR}}
	default: // LTE（含归一化后的默认）
		plans = []ratPlan{{"lte", "1", netScanWaitLTE}}
	}
	var (
		cmds      []string // 实际发出去的扫描指令（both 时两条）
		probes    []string // 被拒的探测指令（用来拼"不支持"说明）
		notes     []string // 逐制式的说明（正常为空）
		unsupport int
	)
	for _, p := range plans {
		probe := fmt.Sprintf(`AT+CNWSEARCH="%s"`, p.rat)
		resp, err := mod.SendATStop(probe, netScanProbeTimeout, stop)
		if errors.Is(err, errATStopped) {
			res.CellNote = joinNotes(append(notes, "已手动停止扫描"))
			res.DurationMs = time.Since(start).Milliseconds()
			return res, nil
		}
		if err != nil || common.RespHasError(resp) {
			// 不支持就如实说明（不是错误，只是这个制式本机没有）
			unsupport++
			probes = append(probes, probe)
			notes = append(notes, ratLabel(p.rat)+" 不支持主动扫小区")
			continue
		}
		res.Steps = append(res.Steps,
			fmt.Sprintf("探测 %s 可用，等待 %s 让模组完成 %s 扫描", probe, p.wait, p.rat))
		// 等待期间也要能被「停止扫描」打断（NR 要等 10 秒）
		select {
		case <-time.After(p.wait):
		case <-stop:
			res.CellNote = joinNotes(append(notes, "已手动停止扫描"))
			res.DurationMs = time.Since(start).Milliseconds()
			return res, nil
		}
		cmd := fmt.Sprintf(`AT+CNWSEARCH="%s",%s`, p.rat, p.mode)
		res.Steps = append(res.Steps, cmd)
		cmds = append(cmds, cmd)
		resp2, err := mod.SendATStop(cmd, netScanCellTimeout, stop)
		if errors.Is(err, errATStopped) {
			res.CellNote = joinNotes(append(notes, "已手动停止扫描"))
			res.DurationMs = time.Since(start).Milliseconds()
			return res, nil
		}
		if err != nil {
			logger.Warn("network scan: cell scan failed",
				zap.Int("modem", mod.ID), zap.String("cmd", cmd), zap.Error(err))
			notes = append(notes, ratLabel(p.rat)+" 扫小区超时（扫描可能要更久），可稍后再试")
			continue
		}
		cells := ParseCNWSEARCHCells(resp2)
		res.Raw = appendNetScanRaw(res.Raw, cmd, resp2)
		if len(cells) == 0 {
			notes = append(notes,
				ratLabel(p.rat)+" 扫到 0 个小区（该制式下可能确实没信号，或扫描还没结束，原始应答见下方）")
			continue
		}
		res.Cells = append(res.Cells, cells...)
	}
	res.CellCmd = strings.Join(cmds, "  ")
	if unsupport == len(plans) {
		// 一个都没成：明确说"本固件不支持"并指路到实时扫描（不是错误，只是本机没这功能）
		res.CellNote = "本固件不支持主动扫小区（`" + strings.Join(probes, "` / `") + "` 被拒 / 无应答）：" +
			"小区列表请用「实时扫描」（服务小区 + 邻区）"
	} else {
		res.CellNote = joinNotes(notes)
	}
	cell.SortCellsBySignal(res.Cells)

	res.DurationMs = time.Since(start).Milliseconds()
	return res, nil
}

// appendNetScanRaw 追加原始应答（带截断保护）
func appendNetScanRaw(dst []string, cmd string, resp []string) []string {
	if len(dst) >= netScanRawMax {
		return dst
	}
	dst = append(dst, cmd+" ->")
	for _, line := range resp {
		if len(dst) >= netScanRawMax {
			dst = append(dst, "…（应答过长，已截断）")
			return dst
		}
		s := strings.TrimSpace(line)
		if len(s) > netScanRawLineMax {
			s = s[:netScanRawLineMax] + "…"
		}
		if s != "" {
			dst = append(dst, s)
		}
	}
	return dst
}
