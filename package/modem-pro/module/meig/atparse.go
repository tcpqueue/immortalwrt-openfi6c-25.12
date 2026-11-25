package meig

import (
	"regexp"
	"strconv"
	"strings"

	"modemd/module/atparse"
	"modemd/module/common"
)

// 温度：美格 `+TEMP: "soc-thmzone",33`（名字 + 值；也可能不带引号）
var reMeigTemp = regexp.MustCompile(`(?i)[+]?TEMP:\s*"?([A-Za-z0-9_.\-]+)"?\s*,\s*(-?\d+(?:\.\d+)?)`)

// ParseTemp 解析美格 `AT+TEMP`：`+TEMP: "<名字>",<摄氏度>`。
//
// `vendor/meig.sh` 关心的是 `soc-thmzone`（SoC 热区）与 `cpu0-0-usr`（CPU），
// 但名字随固件不同，所以全部收下、由 PickSummary 按关键词挑关键点。
func ParseTemp(resp []string) []atparse.Sensor {
	return atparse.ParseNamedTemps(resp, reMeigTemp)
}

// ParseDSAMBR 美格 `AT^DSAMBR=<cid>` 的解析（vendor/meig.sh 的 network_info()）。
//
// ⚠️ 美格的应答布局与鼎桥**不同**：
//   - NR 制式：脚本取 `$9` 当 UL、`$10` 当 DL（就是本函数认的那种），单位 kbps（脚本再 ÷1024 换成 Mbps）；
//   - LTE 制式：脚本是“扫成对的非 0 值、取最后一个”，口径含糊 ⇒ 本项目**不猜**，
//     字段数不够就返回未上报（界面显示“未上报”，不会编一个数）。
func ParseDSAMBR(resp []string) (*atparse.AMBRRate, bool) {
	for _, line := range resp {
		s := strings.TrimSpace(line)
		if !strings.Contains(strings.ToUpper(s), "^DSAMBR") {
			continue
		}
		i := strings.Index(s, ":")
		if i < 0 {
			continue
		}
		f := strings.Split(s[i+1:], ",")
		if len(f) < 10 {
			continue
		}
		ul, e1 := strconv.Atoi(strings.TrimSpace(f[8]))
		dl, e2 := strconv.Atoi(strings.TrimSpace(f[9]))
		if e1 != nil || e2 != nil || (ul == 0 && dl == 0) {
			continue
		}
		return &atparse.AMBRRate{
			CID:    common.AtoiOr0(f[0]), // 命令里带的 cid 会原样回在首格（选「拨号那条」时用）
			DLMbps: atparse.KbpsToMbps(dl), ULMbps: atparse.KbpsToMbps(ul),
		}, true
	}
	return nil, false
}

// init 把自己注册进 atparse 的厂商私有温度格式表（atparse 不 import 品牌包，所以用自注册）。
func init() {
	atparse.RegisterTempParser(20, ParseTemp)
}
