package unisoc

import (
	"modemd/module/common"
	"modemd/module/modedialect"
	"strings"
)

// ====================== 展锐的组网模式（AT+SPRAT） ======================
//
// 从 module/kernel/vendor_settings_usb.go 搬过来：这两件事都只认展锐的 `+SPRAT:` 应答，
// 属于厂商知识；kernel 与 module/lock 保留搬家前的老名字（parseSPRAT /
// unisocModeFromSPRAT）作为别名。

// UnisocModeFromSPRAT 把 `+SPRAT: <原串>` 翻成语义档位。
// **只有实测对得上的两个取值才翻**（真机双向验证：AT+SP5GRAN 1 ↔ LTE 32、0 ↔ LTE 16；
// 切换时 +CEREG 的制式码同步从 5G 变 LTE），其余原样返回 —— 不猜码表。
func UnisocModeFromSPRAT(raw string) (string, bool) {
	switch strings.ToUpper(strings.Join(strings.Fields(raw), " ")) {
	case "LTE 32":
		return common.ModeLTENR5G, true
	case "LTE 16":
		return common.ModeLTE, true
	}
	return "", false
}

// ParseSPRAT 从 `+SPRAT: <原串>` 里取设备自己报的取值。
// 设备不枚举时回的是 `+SPRAT:()` —— 那不是取值，要当成没读到。
func ParseSPRAT(resp []string) (string, bool) {
	line, ok := common.FirstLineWith(resp, "SPRAT:")
	if !ok {
		return "", false
	}
	i := strings.Index(strings.ToUpper(line), "SPRAT:")
	v := strings.TrimSpace(line[i+len("SPRAT:"):])
	v = strings.Trim(strings.TrimSpace(v), `"`)
	if v == "" || v == "()" {
		return "", false
	}
	return v, true
}

// Mode 展锐（Unisoc）组网模式：**5G 开关** `AT+SP5GRAN`。
//
// 真机取证（中兴 F50 / UMS9620，2026-10-06，全部走 /dev/stty_nr2 快路；写入都做了回读 + 注册状态双向确认）：
//
//	AT+SP5GRAN?   → +SP5GRAN: 1        ← 当前值（1=启用 5G，0=只用 4G）
//	AT+SP5GRAN=?  → +SP5GRAN:(0-1)
//	AT+SP5GRAN=0  → OK → 回读 0；URC 时间线：+CREG: 8,…,"0A850608",7 → +CEREG: 1,…,"0A850608",7
//	                     → +CEREG: 1,"404A","03FA6836",13（NR 消失、落到 LTE；+SPRAT 由 LTE 32 变 LTE 16）
//	AT+SP5GRAN=1  → OK → 回读 1（5G+4G 恢复）
//
// ⚠️ `AT+SPRAT` **只能读**：整数式（=32）、引号串（="LTE 32"）、HEX 串（="4C5445203332"）
// 三种写法全部 +CME ERROR: 4 ⇒ 本固件不提供 SPRAT 写入。所以档位表只列本机真能做到的两档，
// 不给用户一个点了必定失败的「仅 5G」（`AT+SP5GRAN` 只有开/关，没有 NR-only）。
//
// ⚠️ 同族还有两个实测可写的开关（`AT+SPFIVEMODE` 0-1、`AT+SPUMTSRAT` 0-1），但**语义未取证**，
// 不接进来（取值含义没证据就写下去 = 拿用户的网做实验，见 lock_unisoc.go 的口径）。
func Mode() *modedialect.Dialect {
	return &modedialect.Dialect{
		Query: "AT+SP5GRAN?",
		Parse: ParseSP5GRAN,
		Set: func(v string) string {
			switch common.NormalizeModePref(v) {
			case modedialect.ModeLTENR5G, modedialect.ModeAuto:
				return "AT+SP5GRAN=1"
			case modedialect.ModeLTE:
				return "AT+SP5GRAN=0"
			}
			return "" // 仅 5G / 3G 等档位：本机做不到，返回空串让上层如实报"不支持"
		},
		Choices:  modedialect.ModeChoiceList(modedialect.ModeLTE, modedialect.ModeLTENR5G),
		Codes:    map[string]string{modedialect.ModeLTE: "0", modedialect.ModeLTENR5G: "1"},
		Enum:     "AT+SP5GRAN=?",
		EnumLine: "SP5GRAN",
		CodeName: SP5GRANName,
		// ⚠️ note 会**原样显示在页面上**，所以不写 markdown 记号（项目约定）。
		Note: "展锐 AT+SP5GRAN（5G 开关）：1 = 5G+4G（自动）、0 = 仅 4G。" +
			"真机取证（写入后回读 + 注册状态双向确认）：AT+SP5GRAN=0 之后 NR 消失、模组落到 LTE" +
			"（+CEREG 的制式码变 7 与 13，+SPRAT 从 LTE 32 变成 LTE 16）；=1 恢复 5G。" +
			"本固件没有「仅 5G」档位：AT+SPRAT 只能读，整数式、引号串、HEX 串三种写法都被 +CME ERROR: 4 拒绝。",
	}
}

// SP5GRANName 5G 开关取值 → 语义档位（认不出返回 ""，界面按原码展示）
func SP5GRANName(code string) string {
	switch strings.TrimSpace(code) {
	case "1":
		return modedialect.ModeLTENR5G
	case "0":
		return modedialect.ModeLTE
	}
	return ""
}

// ParseSP5GRAN 解析 `+SP5GRAN: 0|1` → 语义档位。
// 没见过的取值原样返回（固件自定义码也要能显示 + 原样写回），不臆造含义。
func ParseSP5GRAN(resp []string) (string, bool) {
	line, ok := common.FirstLineWith(resp, "SP5GRAN:")
	if !ok {
		return "", false
	}
	i := strings.Index(strings.ToUpper(line), "SP5GRAN:")
	v := strings.Trim(strings.TrimSpace(line[i+len("SP5GRAN:"):]), `"`)
	if v == "" || v == "()" {
		return "", false
	}
	if sem := SP5GRANName(v); sem != "" {
		return sem, true
	}
	return v, true
}
