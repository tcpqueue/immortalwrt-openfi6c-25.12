package atparse

import (
	"regexp"
	"strconv"
)

// reCBC 3GPP 格式：+CBC: <bcs>,<bcl>，bcl 是毫伏
var reCBC = regexp.MustCompile(`[+#]CBC:\s*\d+\s*,\s*(\d+)`)

// reCBCV 芯讯通格式：+CBC: 3.858V（带单位，直接就是伏）
var reCBCV = regexp.MustCompile(`[+#]CBC:\s*(\d+(?:\.\d+)?)\s*V`)

// reCBCMv 移远格式：+CBC: <bcs>,<bcl%>,<mV>
// 实测 EM05：`+CBC: 0,71,3862` → 3862 mV = 3.862 V
// （⚠️ 不能直接取第二个字段：那是电量百分比 71）
var reCBCMv = regexp.MustCompile(`[+#]CBC:\s*\d+\s*,\s*\d+\s*,\s*(\d+)`)

// ParseCBC 解析 AT+CBC 供电电压，三种格式都认：
//
//	+CBC: 3.858V       ← 实测芯讯通 SIM8200（带单位，直接就是伏）
//	+CBC: 0,71,3862    ← 实测移远 EM05（<bcs>,<bcl%>,<mV>；第三个字段才是电压）
//	+CBC: 0,3858       ← 3GPP TS 27.007 <bcs>,<bcl(mV)>（移远部分型号）
//
// 统一成 "3.862 V"；认不出返回空串（界面显示未上报）。
func ParseCBC(resp []string) string {
	for _, line := range resp {
		if m := reCBCV.FindStringSubmatch(line); m != nil {
			f, err := strconv.ParseFloat(m[1], 64)
			if err != nil || f <= 0 || f > 30 {
				continue
			}
			return strconv.FormatFloat(f, 'f', 3, 64) + " V"
		}
		// 三字段优先（移远）：第二个字段是百分比，不是毫伏
		for _, re := range []*regexp.Regexp{reCBCMv, reCBC} {
			m := re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			mv, err := strconv.Atoi(m[1])
			if err != nil || mv < 2000 || mv > 6000 { // 模组供电不可能低于 2V / 高于 6V
				continue
			}
			return strconv.FormatFloat(float64(mv)/1000, 'f', 3, 64) + " V"
		}
	}
	return ""
}
