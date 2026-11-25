package telit

import (
	"regexp"

	"modemd/module/atparse"
)

// 温度：Telit `#TEMPSENS: TSENS,33`
//
// `vendor/telit.sh` 只认 TSENS 那一路：`grep "#TEMPSENS: TSENS,"`，
// 其余传感器名（各家固件不同）原样显示，反正界面是按"最高温 + 关键点"展示的。
var reTelitTemp = regexp.MustCompile(`(?i)#TEMPSENS:\s*([A-Za-z0-9_.\-]+)\s*,\s*(-?\d+(?:\.\d+)?)`)

// ParseTemp 解析 Telit `AT#TEMPSENS=2`：`#TEMPSENS: <名字>,<摄氏度>`。
func ParseTemp(resp []string) []atparse.Sensor {
	return atparse.ParseNamedTemps(resp, reTelitTemp)
}

// init 把自己注册进 atparse 的厂商私有温度格式表（atparse 不 import 品牌包，所以用自注册）。
func init() {
	atparse.RegisterTempParser(10, ParseTemp)
}
