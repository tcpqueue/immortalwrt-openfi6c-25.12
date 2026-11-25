package kernel

import (
	"modemd/module/common"
	"regexp"
	"strconv"
	"strings"
)

// ====================== 鼎桥 MT5700 的工作频率（可选能力） ======================

// reHFREQINFO 匹配 `^HFREQINFO:` 行（手册 §13.16）
var reHFREQINFO = regexp.MustCompile(`(?i)\^HFREQINFO:\s*(.+)$`)

// ParseHFREQINFO 解析鼎桥 AT^HFREQINFO?（工作频率 / 频段 / 带宽）。
//
// 实测 MT5700M-CN（NR n78，20MHz）：
//
//	^HFREQINFO: 0,7,78,627300,3409500,20000,630000,3450000,100000
//	            前两格含义不明（实测 NR=7 / LTE=6，像是制式码）
//	            band=78  dl_fcn=627300  dl_freq=3409500kHz(3409.5MHz)  dl_bw=20000kHz
//	            ul_fcn=630000  ul_freq=3450000kHz  ul_bw=100000kHz
//
// 手册例（LTE）：`^HFREQINFO: 0,6,18,5925,8675,20000,23925,8225,20000`
// → band 18 / dl 867.5MHz（LTE 的频率单位是 100kHz，NR 是 kHz）。
//
// ⚠️ 前两格含义未公开（第二个在 NR 上是 7、LTE 上是 6），所以**从行尾往前取**：
// 末六格依次是 dl_fcn,dl_freq,dl_bw,ul_fcn,ul_freq,ul_bw，再往前一格才是 band。
func ParseHFREQINFO(resp []string) (*Extra, bool) {
	for _, line := range resp {
		m := reHFREQINFO.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		f := common.SplitATFields(m[1])
		if len(f) < 8 {
			continue
		}
		n := len(f)
		// 从行尾往前：ul_bw=f[n-1], ul_freq=f[n-2], ul_fcn=f[n-3],
		//             dl_bw=f[n-4], dl_freq=f[n-5], dl_fcn=f[n-6], band=f[n-7]
		band, _ := strconv.Atoi(strings.TrimSpace(f[n-7]))
		dlFreq, _ := strconv.Atoi(strings.TrimSpace(f[n-5]))
		dlBW, _ := strconv.Atoi(strings.TrimSpace(f[n-4]))
		if band <= 0 || band > 512 {
			continue
		}
		out := &Extra{
			MCS:     -1,
			CQI:     -1,
			TxPower: -99,
			Band:    band,
			Raw:     strings.TrimSpace(line),
		}
		// 带宽：NR 单位 kHz（20000 → 20MHz），LTE 也是 kHz（手册：1400…20000）
		if dlBW > 0 && dlBW <= 1000000 {
			out.BWmHz = dlBW / 1000
		}
		// 频率：NR kHz（3409500 → 3409500kHz）；LTE 100kHz（8675 → 867.5MHz）→ 单位换算后只作日志
		_ = dlFreq
		return out, true
	}
	return nil, false
}
