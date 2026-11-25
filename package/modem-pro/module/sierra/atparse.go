package sierra

import (
	"modemd/module/atparse"
	"regexp"
	"strconv"
	"strings"
)

// 温度：Sierra `AT!PCTEMP?` → `Temperature: 33.5`（vendor/sierra.sh 的 _get_temperature）
var reSierraTemp = regexp.MustCompile(`(?i)^temperature:\s*(-?\d+(?:\.\d+)?)`)

// 电压：Sierra / Foxconn `AT[!^]PCVOLT?` → `Power supply voltage: 3860 mV`
// （vendor/sierra.sh 的 _get_voltage、vendor_foxconn.sh 的 _get_voltage 都是这个 regex 口径）
var rePCVolt = regexp.MustCompile(`(?i)power supply voltage:\s*(\d+)\s*mv`)

// ParsePCTemp 解析 Sierra `AT!PCTEMP?` 的 `Temperature: 33.5`（vendor/sierra.sh 的 _get_temperature）。
// 只有一格整机温度，名字统一叫“模组温度”。
func ParsePCTemp(resp []string) []atparse.Sensor {
	for _, line := range resp {
		m := reSierraTemp.FindStringSubmatch(strings.TrimSpace(line))
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

// ParsePCVolt 解析 Sierra/Foxconn `AT[!^]PCVOLT?` 的 `Power supply voltage: 3860 mV`，
// 统一成 "3.860 V"（与 ParseCBC 的输出口径一致）。认不出返回空串。
func ParsePCVolt(resp []string) string {
	for _, line := range resp {
		m := rePCVolt.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		mv, err := strconv.Atoi(m[1])
		if err != nil || mv < 2000 || mv > 6000 { // 模组供电不可能低于 2V / 高于 6V
			continue
		}
		return strconv.FormatFloat(float64(mv)/1000, 'f', 3, 64) + " V"
	}
	return ""
}

// ParseVoltage 供电电压解析总入口：
//   - 3GPP/移远/芯讯通/Telit 的 `AT+CBC`（三种格式）→ net.go 的 ParseCBC
//   - Sierra / Foxconn 的 `AT[!^]PCVOLT?`（`Power supply voltage: 3860 mV`）→ ParsePCVolt
//
// 两边都认不出来就返回空串（界面显示未上报）。
func ParseVoltage(resp []string) string {
	if v := atparse.ParseCBC(resp); v != "" {
		return v
	}
	return ParsePCVolt(resp)
}

// init 把自己注册进 atparse 的厂商私有温度格式表（atparse 不 import 品牌包，所以用自注册）。
func init() {
	atparse.RegisterTempParser(40, ParsePCTemp)
}
