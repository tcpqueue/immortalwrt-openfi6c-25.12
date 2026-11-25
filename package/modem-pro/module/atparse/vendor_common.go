package atparse

import (
	"regexp"
	"strconv"
	"strings"
)

// ParseNamedTemps 共用：带名字的温度行 → []Sensor（名字去重、值要在合理区间）
func ParseNamedTemps(resp []string, re *regexp.Regexp) []Sensor {
	var out []Sensor
	seen := map[string]bool{}
	for _, line := range resp {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v, err := strconv.ParseFloat(m[2], 64)
		if err != nil || v < -40 || v > 150 {
			continue
		}
		name := strings.TrimSpace(m[1])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, Sensor{Name: name, Celsius: int(v)})
	}
	return out
}

// PrependUnique 把 head 插到 list 最前面（去重、跳过空串）。
//
// 用途：机型特例给的指令要**优先**试，但失败后仍要能退回原来的候选 ——
// 这样"只对这些型号生效"就不会把别的机型的既有行为改坏。
func PrependUnique(head string, list ...string) []string {
	out := make([]string, 0, len(list)+1)
	seen := map[string]bool{}
	for _, c := range append([]string{head}, list...) {
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}
