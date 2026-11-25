package thales

import (
	"regexp"
	"strconv"

	"modemd/module/atparse"
)

// 温度：Thales `+TEMP` / Foxconn `AT[!^]TEMP?` 应答里的 `TSENS: 33C`（手册 §15.13，带 C 单位）
var reThalesTemp = regexp.MustCompile(`(?i)TSENS:\s*(-?\d+(?:\.\d+)?)\s*C`)

// ParseTemp 解析 Thales `AT^TEMP`（以及 Foxconn `AT[!^]TEMP?`）应答里的 `TSENS: <摄氏度>C`。
//
// 两个厂商用的是同一个 `TSENS: 33C` 写法（QModem 的 vendor/thales.sh 与 vendor/foxconn.sh
// 各有一段一模一样的 `sed -n 's/.*TSENS: \([0-9]*\)C.*/\1/p'`），所以共用一个解析。
// 与上面两个不同：这条**没有传感器名**（手册只给整机温度），所以统一叫"模组温度"。
func ParseTemp(resp []string) []atparse.Sensor {
	for _, line := range resp {
		m := reThalesTemp.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil || v < -40 || v > 150 {
			continue
		}
		return []atparse.Sensor{{Name: "模组温度", Celsius: int(v)}}
	}
	return nil
}

// init 把自己注册进 atparse 的厂商私有温度格式表（atparse 不 import 品牌包，所以用自注册）。
func init() {
	atparse.RegisterTempParser(30, ParseTemp)
}
