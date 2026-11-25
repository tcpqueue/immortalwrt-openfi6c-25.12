package modedialect

// ====================== 组网模式偏好的方言表 ======================
//
// 从 kernel 搬过来：语义档位（AUTO / LTE / NR5G / …）与各家码表的映射全在这里。
// 派发器 `modeDialectFor` 留在内核（要读 mod 的厂商/型号/清单），由它选一个方言回去。
//
// 字段已导出（Query/Set/Parse/Choices/Codes/Enum/EnumLine/CodeName/RawMatch/
// Rebuild/Note/ReadOnly/Alt）—— 跨包必须导出；kernel 侧由别名与
// fix-field-exports.py 自动对齐。

import (
	"strconv"
	"strings"

	"modemd/iface"
	"modemd/module/common"
)

type Mode = iface.Mode

// ---- 外部契约 / 工具的本包别名 ----

var (
	normalizeModePref = common.NormalizeModePref
	firstLineWith     = common.FirstLineWith
	ParseATInt        = common.ParseATInt
	splitCSVFields    = common.SplitCSVFields
	numInLine         = common.NumInLine
	IsDecimal         = common.IsDecimal
	isHexValue        = common.IsHexValue
)

const (
	ModeAuto     = common.ModeAuto
	ModeWCDMA    = common.ModeWCDMA
	ModeLTE      = common.ModeLTE
	ModeNR5G     = common.ModeNR5G
	ModeWCDMALTE = common.ModeWCDMALTE
	ModeWCDMANR  = common.ModeWCDMANR
	ModeLTENR5G  = common.ModeLTENR5G
)

// Dialect 组网模式偏好的方言。语义值统一为：
//
//	AUTO / WCDMA / LTE / NR5G / WCDMA:LTE / WCDMA:NR5G / LTE:NR5G
//
// 与 QModem `set_network_prefer_nr()` 的分档完全一致。
type Dialect struct {
	Query string
	Set   func(value string) string
	Parse func(resp []string) (string, bool)
	// choices 本机可选的**语义档位**（按 AllModePrefs 顺序；界面下拉就是它，每一档都有中文名）
	Choices []string
	// codes 语义档位 → 本机码（写入时取码；见 ModeCodesDialect。码表来源 QModem vendor/*.sh）
	Codes map[string]string
	// enum 设备枚举指令（如 `AT!SLMODE=?`）：让模组自报它还支持哪些取值，作为下拉的补充。
	// 空 = 不枚举。枚举到的码能翻译成档位就显示中文名，翻不出来就原样显示那个码。
	Enum string
	// enumLine 非空时只解析枚举应答里含该关键字的那一行
	// （移远 `AT+QNWPREFCFG=?` 会把所有配置项一起列出来，要的是 "mode_pref" 那行）
	EnumLine string
	// codeName 把本机码翻译语义档位；认不出返回 ""（界面按原码展示）
	CodeName func(code string) string
	// rawMatch / rebuild：需要**在当前取值基础上改写**的方言（如华为 `AT^SYSCFGEX="<码>",<频段>,…`：
	// 写制式必须把后面的频段/漫游等字段原样带上）。rawMatch 是从查询应答里取原始行的关键字，
	// rebuild 把原始行里的第一个字段换成要写的码，其余原样保留。
	RawMatch string
	Rebuild  func(rawLine, code string) string
	Note     string
	// readOnly 只读方言：能读当前值，但**不提供写入**（取值表 / 写语法未取证）。
	// 置位后 SetModePrefCaps 直接带着 note 报"只能读"，而不是含糊的"本机不支持"。
	ReadOnly bool
	// alt 主写法被模组**拒了**之后的备用写法（返回 "" = 没备用）。
	//
	// 为什么需要它：复合参数指令（`AT^SYSCFGEX` / `AT+GTACT`）各固件**接受的形式不一样** ——
	// 真机 FM160-CN 实测：把回读到的整串（`17,6,3,1,8,101,…`）原样发回去会被回 ERROR
	// （空参数组会被省略 ⇒ 位置对齐有歧义），只发档位号 `AT+GTACT=17` 则 OK。
	// QModem 上游同样是按型号/平台分别处理（`cmds/*.sh` 里是固定模板，
	// `vendor/fibocom.sh` 的 `fibocom_gtact_params` 会按选中的制式拼参数）。
	// 第二个参数是**写入前查询应答的原文行**（备用写法需要其中的频段参数）。
	Alt func(value, rawLine string) string
}

// codeFor 语义值 或 本机码 → 本机码（认不出返回空）；读改写时先转成码再替换字段
func (d *Dialect) CodeFor(v string) string {
	u := strings.ToUpper(strings.TrimSpace(v))
	if c, ok := d.Codes[u]; ok {
		return c
	}
	if LooksLikeModeCode(u) {
		return u // 设备自报的码（枚举出来的 / 用户从设备读到的）原样写回
	}
	return ""
}

// altCmd 备用写指令（主写法被模组拒了才用）。rawLine = 写入前查询应答的原文行。
func (d *Dialect) AltCmd(value, rawLine string) string {
	if d == nil || d.Alt == nil || strings.TrimSpace(rawLine) == "" {
		return ""
	}
	return strings.TrimSpace(d.Alt(value, rawLine))
}

// ModeChoice 组网模式偏好的一个可选档位（界面下拉用）
type ModeChoice struct {
	Value    string `json:"value"`            // 语义值（AUTO/LTE…）或**本机原码**
	Name     string `json:"name"`             // 中文名（原码认不出语义时就是原码本身）
	Writable bool   `json:"writable"`         // 本机能不能把它写下去
	Source   string `json:"source,omitempty"` // 取值来源（取证脚本 / 设备枚举 / 语义档位）
	Note     string `json:"note,omitempty"`
}

// ModeCodesDialect 组一个「语义档位 ↔ 本机码」的方言。
//
//	codes     语义值 → 本机码（写的时候用；界面下拉就按 codes 给全部档位）
//	codeNames 本机码 → 语义值（读回来的码翻成中文名；**翻不出来就原样显示那个码**）
//	build     本机码 → 完整设置指令（复合指令用 d.Rebuild 覆盖）
//	readParse 应答 → 本机码
//
// 码表来源全部是 QModem `vendor/*.sh` 的 `get/set_network_prefer*`（逐行核对过），
// 所以下拉里每一档都是直接能用的中文档位，不再有"未取证"这种占位项。
func ModeCodesDialect(query, enum, enumLine, note string,
	codes, codeNames map[string]string,
	readParse func(resp []string) (string, bool),
	build func(code string) string) *Dialect {

	choices := make([]string, 0, len(codes))
	for _, v := range AllModePrefs { // 固定顺序：自动 → 单制式 → 组合
		if _, ok := codes[v]; ok {
			choices = append(choices, v)
		}
	}
	d := &Dialect{
		Query:    query,
		Enum:     enum,
		EnumLine: enumLine,
		Choices:  choices,
		Codes:    codes,
		Note:     note,
	}
	d.CodeName = func(code string) string {
		return codeNames[strings.ToUpper(strings.TrimSpace(code))]
	}
	d.Set = func(v string) string {
		code := d.CodeFor(v)
		if code == "" || build == nil {
			// build == nil：复合指令（SYSCFGEX / GTACT），真正的指令由 d.Rebuild 回读后拼出
			return ""
		}
		return build(code)
	}
	if readParse != nil {
		d.Parse = func(resp []string) (string, bool) {
			code, ok := readParse(resp)
			if !ok || code == "" {
				return "", false
			}
			if sem := d.CodeName(code); sem != "" {
				return sem, true
			}
			return code, true // 固件自定义码：原样显示（匹配不到才显示原码）
		}
	}
	return d
}

// ModeChoiceList 语义值列表（给界面下拉）
func ModeChoiceList(vals ...string) []string { return vals }

// ModeLabel 取语义档位的中文名；不是语义值（= 本机原码）就原样返回
func ModeLabel(v string) string {
	if n, ok := ModePrefLabels[strings.ToUpper(strings.TrimSpace(v))]; ok {
		return n
	}
	return v
}

// IsModePrefSemantic 是不是语义档位（false = 本机原码）
func IsModePrefSemantic(v string) bool {
	_, ok := ModePrefLabels[strings.ToUpper(strings.TrimSpace(v))]
	return ok
}

// LooksLikeModeCode 形态上像“本机上报表的取值”：
//
//	纯数字（00 / 2 / 38）、0x 十六进制、或以字母开头的短码（GSM / TDS / LTE_ONLY…）。
//
// 目的：拦住 "5G"、"foo bar" 这类明显不是码的输入（它们会被原样拼进 AT，模组只会回 ERROR）。
// 真正的原码形态就是这些：各家档位码要么是数字，要么是短码。
func LooksLikeModeCode(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 16 {
		return false
	}
	if IsDecimal(v) || isHexValue(v) {
		return true
	}
	if !((v[0] >= 'A' && v[0] <= 'Z') || (v[0] >= 'a' && v[0] <= 'z')) {
		return false
	}
	for _, r := range v {
		if !strings.ContainsRune("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz:_,-", r) {
			return false
		}
	}
	return true
}

// ParseEnumValues 从 `AT+XXX=?` 的应答里取候选值（各家语法不同，取不到返回 nil，调用方忽略即可）。
//
// match 非空时只解析**含该关键字的那一行**（如移远要的是 `"mode_pref"` 那行，
// 因为它的 `AT+QNWPREFCFG=?` 会把所有配置项一起列出来）。支持三种写法：
//
//	+CNMP: (2,13,14,38,71)                        数字枚举
//	^SLMODE: (0-7)                                区间
//	+QNWPREFCFG: "mode_pref",("AUTO","LTE","NR5G") 字符串枚举
func ParseEnumValues(resp []string, match string) []string {
	if match != "" {
		l, ok := firstLineWith(resp, match)
		if !ok {
			return nil
		}
		resp = []string{l}
	}
	for _, line := range resp {
		open := strings.IndexByte(line, '(')
		if open < 0 {
			continue
		}
		end := strings.IndexByte(line[open:], ')')
		if end < 0 {
			continue
		}
		var out []string
		for _, f := range splitCSVFields(line[open+1 : open+end]) {
			f = strings.TrimSpace(strings.Trim(f, `"'`))
			if f == "" {
				continue
			}
			if lo, hi, ok := EnumRange(f); ok {
				for i := lo; i <= hi && i < lo+64; i++ { // 区间一般只有几档，加个上限防爆
					out = append(out, strconv.Itoa(i))
				}
				continue
			}
			out = append(out, f)
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// EnumRange 解析 `0-7` 这种区间写法
func EnumRange(s string) (int, int, bool) {
	i := strings.IndexByte(s, '-')
	if i <= 0 || i == len(s)-1 {
		return 0, 0, false
	}
	lo, err1 := strconv.Atoi(strings.TrimSpace(s[:i]))
	hi, err2 := strconv.Atoi(strings.TrimSpace(s[i+1:]))
	if err1 != nil || err2 != nil || hi < lo {
		return 0, 0, false
	}
	return lo, hi, true
}

// FirstCSVField 取应答里 marker 之后的第一段（去引号/空格）
func FirstCSVField(marker string) func(resp []string) (string, bool) {
	return func(resp []string) (string, bool) {
		line, ok := firstLineWith(resp, marker)
		if !ok {
			return "", false
		}
		body := line[strings.Index(strings.ToUpper(line), strings.ToUpper(marker))+len(marker):]
		f := splitCSVFields(body)
		if len(f) == 0 {
			return "", false
		}
		v := strings.ToUpper(strings.TrimSpace(strings.Trim(f[0], `"'`)))
		return v, v != ""
	}
}

// NthCSVField 取应答里 marker 之后的第 n 段（0 起）；不够长就退回最后一段
func NthCSVField(marker string, n int) func(resp []string) (string, bool) {
	return func(resp []string) (string, bool) {
		line, ok := firstLineWith(resp, marker)
		if !ok {
			return "", false
		}
		body := line[strings.Index(strings.ToUpper(line), strings.ToUpper(marker))+len(marker):]
		f := splitCSVFields(body)
		if len(f) == 0 {
			return "", false
		}
		if n >= len(f) {
			n = len(f) - 1
		}
		v := strings.ToUpper(strings.TrimSpace(strings.Trim(f[n], `"'`)))
		return v, v != ""
	}
}

// AllModePrefs 全部语义档位（顺序：自动 → 单制式 → 组合）。
// 界面的下拉就是按它排的；每一档在具体方言里都有对应码（见各家 xxxxMode()）。
var AllModePrefs = []string{ModeAuto, ModeWCDMA, ModeLTE, ModeNR5G, ModeWCDMALTE, ModeWCDMANR, ModeLTENR5G}

// ModePrefLabels 语义值 → 中文名（下拉里只显示 AUTO/LTE:NR5G 会看不懂）
var ModePrefLabels = map[string]string{
	ModeAuto:     "自动（2G/3G/4G/5G）",
	ModeWCDMA:    "仅 3G（WCDMA）",
	ModeLTE:      "仅 4G（LTE）",
	ModeNR5G:     "仅 5G（NR）",
	ModeWCDMALTE: "3G + 4G",
	ModeWCDMANR:  "3G + 5G",
	ModeLTENR5G:  "4G + 5G（NSA/SA）",
}

// RebuildSetFromQuery 把查询应答里的第一个字段换成 value，拼回一条**完整**的设置指令。
//
// 支持两种回显形式（各家不同，先试引号再试逗号）：
//
//	^SYSCFGEX: "00",40000000,1,2,…   → AT^SYSCFGEX="<new>",40000000,1,2,…
//	^SLMODE: 1,2,3                    → <prefix><new>,2,3
func RebuildSetFromQuery(prefix, rawLine, value string) string {
	// 去掉 "^XXX:" 前缀，只留参数体
	body := rawLine
	if i := strings.IndexByte(body, ':'); i >= 0 {
		body = body[i+1:]
	}
	body = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(body), "OK"))
	if body == "" {
		return ""
	}
	if strings.HasPrefix(body, `"`) {
		tail := body[1:]
		if j := strings.IndexByte(tail, '"'); j >= 0 {
			return prefix + value + tail[j:]
		}
		return ""
	}
	fields := splitCSVFields(body)
	if len(fields) == 0 {
		return ""
	}
	rest := strings.TrimSpace(strings.TrimPrefix(body, fields[0]))
	return prefix + value + rest
}
