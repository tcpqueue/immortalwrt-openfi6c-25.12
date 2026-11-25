package atparse

import (
	"regexp"
	"strconv"
	"strings"

	"modemd/module/common"
)

var reQTEMP = regexp.MustCompile(`\+QTEMP:\s*"([^"]+)"\s*,\s*"?(-?\d+)"?`)

var reCPMUTEMP = regexp.MustCompile(`\+CPMUTEMP:\s*(-?\d+)`)

var reMTSM = regexp.MustCompile(`(?i)\+MTSM:\s*(-?\d+)`)

var reGTLADCInput = regexp.MustCompile(`(?i)^in_temp_(\S+?)(?:_input)?:\s*(-?\d+)\s*$`)

var reGTLADCZone = regexp.MustCompile(`(?i)^\[zone\d+\]:(\S+)\s+(-?\d+)\s*$`)

var reGTSTemp = regexp.MustCompile(`(?i)^\+GT(SENRDTEMP|ZONERDMAXTEMP):\s*(\d+)\s*,\s*(-?\d+)\s*$`)

var reCHIPTEMP = regexp.MustCompile(`(?i)\^CHIPTEMP:\s*(.+)$`)

// ParseQTEMP 解析全部温度传感器。两种格式都认：
//
//	+QTEMP:"sdx-case-therm-usr","31"   移远 5G 模组：带名字，实测 13 路
//	+QTEMP: 30,26,25                     移远 EM05（LTE Cat4）：**不带名字**，只给值
//
// 第二种没有传感器名，不猜“哪个是 CPU”，统一叫“传感器 N”（界面照样能显示最高温/全部）。
func ParseQTEMP(resp []string) []Sensor {
	out := make([]Sensor, 0, 8)
	for _, line := range resp {
		m := reQTEMP.FindStringSubmatch(line)
		if m == nil {
			// 无名写法：+QTEMP: 30,26,25（只认这一种形状，免得把别的行的数字当成温度）
			if f := ParseQTEMPValues(line); len(f) > 0 {
				out = append(out, f...)
			}
			continue
		}
		c, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		out = append(out, Sensor{Name: m[1], Celsius: c})
	}
	return out
}

// ParseQTEMPValues 解析无名字的 `+QTEMP: 30,26,25`，返回“传感器 N”列表。
// 每项都要在模组合理温度区间（-40~125 ℃），否则整行丢弃。
func ParseQTEMPValues(line string) []Sensor {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(strings.ToUpper(s), "+QTEMP:") {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(s[len("+QTEMP:"):], " "), ",")
	if len(parts) == 0 {
		return nil
	}
	out := make([]Sensor, 0, len(parts))
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(strings.Trim(p, `"`)))
		if err != nil || v < -40 || v > 125 {
			return nil
		}
		out = append(out, Sensor{Name: "传感器 " + strconv.Itoa(i+1), Celsius: v})
	}
	return out
}

// ParseCPMUTEMP 解析芯讯通 AT+CPMUTEMP（+CPMUTEMP: 25）。
// 只有一个整机温度，给个中性名字：界面上的三个“关键测温点”会退回最高温兜底。
func ParseCPMUTEMP(resp []string) []Sensor {
	for _, line := range resp {
		m := reCPMUTEMP.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		c, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		return []Sensor{{Name: "模组温度", Celsius: c}}
	}
	return nil
}

// ParseMTSM 解析广和通 +MTSM（实测 FM650：AT+MTSM=1,6 → +MTSM: 60，单位 ℃）。
// 只有一个整机温度，给个中性名字：界面上的三个"关键测温点"会退回最高温兜底。
func ParseMTSM(resp []string) []Sensor {
	for _, line := range resp {
		m := reMTSM.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		c, err := strconv.Atoi(m[1])
		if err != nil || c < -40 || c > 125 {
			continue
		}
		return []Sensor{{Name: "模组温度", Celsius: c}}
	}
	return nil
}

// ParseGTLADC 解析广和通 AT+GTLADC（FM160 及以后机型的温度）。
//
// 实测 FM160-EAU（固件 89611.1000.00.04.01.07）：`AT+QTEMP` / `AT+CPMUTEMP` / `AT+MTSM`
// **全部回 ERROR**，温度只有它自己的 `AT+GTLADC`：
//
//	AT+GTLADC
//	+GTLADC:
//	in_temp_pmx65_ambient_therm_input: 30562
//	[zone32]:cpuss-0-usr 31800
//	[zone42]:modem-beamer-usr -40960      ← 该路没接，是无效哨兵值
//	OK
//
// 两种写法（冒号分隔的 `in_temp_*_input:`、空格分隔的 `[zoneN]:名字 值`），
// **数值都是 1/1000 ℃**：31800 → 31.8 ℃；0 = 未上报、负数 = 无效，都要丢掉。
// 官方集成脚本（luci-app-modem → fibocom.sh）只用其中一行当"CPU 温度"，这里全部收下，
// 让界面的「最高温 + 机身/5G/CPU」和「全部 N 个传感器」都有数据。
func ParseGTLADC(resp []string) []Sensor {
	out := make([]Sensor, 0, 32)
	add := func(name string, raw string) {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 || v > 125000 {
			return // 0 = 未上报，负数（如 -40960）= 无效哨兵
		}
		out = append(out, Sensor{Name: name, Celsius: (v + 500) / 1000}) // 1/1000 ℃ → 四舍五入
	}
	for _, line := range resp {
		if m := reGTLADCZone.FindStringSubmatch(line); m != nil {
			add(m[1], m[2])
			continue
		}
		if m := reGTLADCInput.FindStringSubmatch(line); m != nil {
			add(m[1], m[2])
		}
	}
	if len(out) == 0 {
		return nil
	}
	// 高温在前：界面「全部 N 个传感器」直接照这个顺序渲染，最值得看的排最前
	SortDesc(out)
	return out
}

// SortDesc 按温度从高到低排序（传感器数量很少，简单插入排序足够）。
func SortDesc(temps []Sensor) {
	for i := 1; i < len(temps); i++ {
		for j := i; j > 0 && temps[j].Celsius > temps[j-1].Celsius; j-- {
			temps[j], temps[j-1] = temps[j-1], temps[j]
		}
	}
}

// ParseGTSTemp 解析广和通 +GTSENRDTEMP / +GTZONERDMAXTEMP（FM350 的温度，认不出返回空）。
//
// 实测 FM350-GL：`AT+QTEMP` / `AT+GTLADC` / `AT+MTSM=1,6` / `AT+CPMUTEMP` **全部回 ERROR**，
// 温度要用手册 §18.3 / §18.4 的这两条（都**必须带参数**，查询写法会回 ERROR）：
//
//	AT+GTSENRDTEMP=0   → +GTSENRDTEMP: 1,31773 （实测 23 路；某路没接会回 0）
//	AT+GTZONERDMAXTEMP=0 → +GTZONERDMAXTEMP: 1,31605
//
// 数值口径与 FM160 的 AT+GTLADC 一致：**1/1000 ℃**（31773 → 31.8 ℃），0 = 未上报/没接。
// 手册只给了传感器/热区编号、没有名字，所以统一命名成「传感器 N」「热区 N」。
func ParseGTSTemp(resp []string) []Sensor {
	out := make([]Sensor, 0, 32)
	for _, line := range resp {
		m := reGTSTemp.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		v, err := strconv.Atoi(m[3])
		if err != nil || v <= 0 || v > 125000 {
			continue // 0 = 未上报/该路没接，负值 = 无效哨兵
		}
		prefix := "传感器 "
		if strings.EqualFold(m[1], "ZONERDMAXTEMP") {
			prefix = "热区 "
		}
		out = append(out, Sensor{Name: prefix + m[2], Celsius: (v + 500) / 1000})
	}
	if len(out) == 0 {
		return nil
	}
	SortDesc(out)
	return out
}

// chipTempNames 鼎桥手册 §17.1.3 的传感器顺序（名字直接用来展示，比"传感器 N"好认）
var chipTempNames = []string{
	"sub3G PA", "sub6G PA", "MIMO PA", "TCXO",
	"peri1", "peri2", "ap1", "ap2",
	"modem1", "modem2", "bbp1", "bbp2",
}

// ParseCHIPTEMP 解析鼎桥 AT^CHIPTEMP?（MT5700，认不出返回空；单位 0.1 ℃ → 整数 ℃）。
//
//	AT^CHIPTEMP?
//	^CHIPTEMP: 341,338,330,344,330,330,340,340,340,360,330,330
//
// 12 个传感器，顺序固定（手册 §17.1.3）；实测 `AT+QTEMP` / `AT+CPMUTEMP` / `AT+MTSM` /
// `AT+GTLADC` / `AT+GTSENRDTEMP` 在这个固件上**全部回 ERROR**。
// ⚠️ 单位是 **0.1 ℃**（341 → 34.1 ℃），读失败的那一路回 **65535**（要丢掉）。
func ParseCHIPTEMP(resp []string) []Sensor {
	for _, line := range resp {
		m := reCHIPTEMP.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		f := common.SplitATFields(m[1])
		out := make([]Sensor, 0, len(f))
		for i, tok := range f {
			v, err := strconv.Atoi(strings.TrimSpace(tok))
			// 65535 = 该路读取失败；越界/负值也丢掉
			if err != nil || v >= 65535 || v <= 0 || v > 1250 {
				continue
			}
			name := "传感器 " + strconv.Itoa(i+1)
			if i < len(chipTempNames) {
				name = chipTempNames[i]
			}
			out = append(out, Sensor{Name: name, Celsius: (v + 5) / 10}) // 0.1 ℃ → ℃（四舍五入）
		}
		if len(out) == 0 {
			continue
		}
		SortDesc(out)
		return out
	}
	return nil
}

// ParseTempResp 解析温度应答（各厂商格式依次试）：
// 移远多传感器 → 芯讯通单值 → 广和通 +MTSM → 广和通 AT+GTLADC（FM160+）
// → 广和通 +GTSENRDTEMP（FM350）→ 鼎桥 +CHIPTEMP（MT5700）。
func ParseTempResp(resp []string) []Sensor {
	if out := ParseQTEMP(resp); len(out) > 0 {
		return out
	}
	if out := ParseCPMUTEMP(resp); len(out) > 0 {
		return out
	}
	if out := ParseMTSM(resp); len(out) > 0 {
		return out
	}
	if out := ParseGTLADC(resp); len(out) > 0 {
		return out
	}
	if out := ParseGTSTemp(resp); len(out) > 0 {
		return out
	}
	// 厂商私有格式：解析器住在 module/<品牌>/atparse.go，各自 init() 登记（见 temp_hooks.go）
	for _, e := range tempEntries {
		if out := e.fn(resp); len(out) > 0 {
			return out
		}
	}
	return ParseCHIPTEMP(resp)
}

// PickSummary 挑出"最高温"与三个关键测温点（机身 / 5G 基带 / CPU）。
// 命名依厂商而异（实测移远是 sdx-case-therm-usr / mdm-5g-usr / cpu0-a7-usr），
// 所以按关键词匹配，匹配不到就用温度最高的几个兜底。
func PickSummary(temps []Sensor) (*Sensor, []Sensor) {
	if len(temps) == 0 {
		return nil, nil
	}
	sorted := append([]Sensor(nil), temps...)
	SortDesc(sorted)
	hot := sorted[0]
	keys := make([]Sensor, 0, 3)
	take := func(pred func(string) bool, dup map[string]bool) {
		for _, t := range sorted {
			if dup[t.Name] {
				continue
			}
			if pred(strings.ToLower(t.Name)) {
				keys = append(keys, t)
				dup[t.Name] = true
				return
			}
		}
	}
	dup := map[string]bool{}
	take(func(n string) bool { return strings.Contains(n, "case") }, dup) // 机身壳温
	take(func(n string) bool { return strings.Contains(n, "5g") }, dup)   // 5G 基带
	take(func(n string) bool { return strings.Contains(n, "cpu") }, dup)  // 应用处理器
	return &hot, keys
}
