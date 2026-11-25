package simcom

// ====================== 芯讯通的组网模式码表（AT+CNMP） ======================
//
// 从 kernel 搬过来：芯讯通**所有方言共用**这一份码表
// （含旧款走 `AT+CNBP` 的机型 —— 那套的 mode pref 也是同一条 `+CNMP:`）。
//
// ⚠️ 别再在别处另写一份"只认识单制式"的 switch：那是旧版本，5G 组合档位会被当成"读不到"。
// kernel 与 module/lock 都保留搬家前的老名字（cnmpName / parseCNMP）作为别名。

import (
	"strings"

	"modemd/module/common"
)

// CnmpName CNMP 取值 → 语义值（认不出返回 ""，界面按原码展示）
//
// 码表来源：QModem `vendor/simcom.sh` 的 get/set_network_prefer_nr，逐行核对：
//
//	2=自动/全部、14=仅3G、38=仅4G、71=仅5G、54=3G+4G、55=3G+5G、109=4G+5G
func CnmpName(code string) string {
	switch strings.TrimSpace(code) {
	case "2":
		return common.ModeAuto
	case "14":
		return common.ModeWCDMA
	case "38":
		return common.ModeLTE
	case "71":
		return common.ModeNR5G
	case "54":
		return common.ModeWCDMALTE
	case "55":
		return common.ModeWCDMANR
	case "109":
		return common.ModeLTENR5G
	}
	return ""
}

// ParseCNMP 解析 `+CNMP: <n>`（芯讯通所有方言共用；旧款 AT+CNBP 那套也是同一套 CNMP）
func ParseCNMP(resp []string) (string, bool) {
	for _, line := range resp {
		i := strings.Index(strings.ToUpper(line), "+CNMP:")
		if i < 0 {
			continue
		}
		fs := common.SplitATFields(line[i+len("+CNMP:"):])
		if len(fs) == 0 {
			continue
		}
		if v := CnmpName(fs[0]); v != "" {
			return v, true
		}
	}
	return "", false
}
