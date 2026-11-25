package atparse

import (
	"modemd/module/common"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// IsInternal 判断是否为内部专用 APN（VoLTE 信令 / 紧急呼叫），
// 它们不是上网用的数据 APN，展示"当前网络"时应排除。
func IsInternal(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ims", "sos", "emergency", "emergency.apn":
		return true
	}
	return false
}

// AMBRRate 运营商下发的签约速率（单位 Mbps，与移远应答一致）
//
// ⚠️ QCI / CQI 与 AMBR 一样，都是**逐承载**的：同一台模组常同时有 IMS 与数据两条，
// 取错就会把 IMS 的档位显示出来。所以 CID 要带上，选取统一走 PickAMBR（只认拨号那条）。
type AMBRRate struct {
	APN       string    `json:"apn,omitempty"`    // 上报该速率的 APN（本机是 IMS）
	CID       int       `json:"cid,omitempty"`    // 该承载的 cid（能取到时；移远 QNWCFG 不报 cid）
	DLMbps    int       `json:"dl_mbps"`          // 签约下行
	ULMbps    int       `json:"ul_mbps"`          // 签约上行
	CQIDL     int       `json:"cqi_dl,omitempty"` // 顺带上报的下行 CQI（信道质量指示）
	CQIUL     int       `json:"cqi_ul,omitempty"` // 上行 CQI
	QCI       int       `json:"qci,omitempty"`    // 承载的 QoS 等级（5QI/QCI）；0 = 未上报（只有有方 C5GQOSRDP 与鼎桥 DSAMBR 会给）
	Source    string    `json:"source,omitempty"` // 实际使用的指令，便于核对
	QueriedAt time.Time `json:"queried_at,omitempty"`
}

// ParseQNWCFG 解析一行 `+QNWCFG: "nr5g_ambr","<apn>",<cqi_dl>,<dl>,<cqi_ul>,<ul>`。
// 只接受实测确认过的 4 个数字字段：字段数不对（例如 LTE 的 `"lte_ambr","<apn>",a,b`
// 只有两个数字、含义未经实测）宁可不显示，也不要把上下行标反。
func ParseQNWCFG(line string) (*AMBRRate, bool) {
	i := strings.Index(line, ":")
	if i < 0 {
		return nil, false
	}
	fields := strings.Split(line[i+1:], ",")
	if len(fields) < 6 {
		return nil, false
	}
	// fields[0] 是 "nr5g_ambr"，fields[1] 是 APN
	name := strings.Trim(strings.TrimSpace(fields[1]), `"`)
	nums := make([]int, 0, 4)
	for _, f := range fields[2:6] {
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(f, "\r")))
		if err != nil {
			return nil, false
		}
		nums = append(nums, n)
	}
	return &AMBRRate{APN: name, CQIDL: nums[0], DLMbps: nums[1], CQIUL: nums[2], ULMbps: nums[3]}, true
}

// reDSAMBR 鼎桥 `^DSAMBR: <cid>,<DlApnAmbr kbps>,<UlApnAmbr kbps>[,"<apn>"[,<QCI>]]`
var reDSAMBR = regexp.MustCompile(`(?i)\^DSAMBR:\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*(?:,\s*"([^"]*)"\s*(?:,\s*(\d+))?)?`)

// ParseDSAMBR 解析鼎桥 AT^DSAMBR=<cid> 的应答（手册 §16.17“签约速率查询”）。
//
// 手册的字段定义是 `<cid>,<DlApnAmbr>,<UlApnAmbr>`，**单位 kbps**；
// 实测应答在末尾还多两格：APN，以及**承载的 QCI**（手册未写，实机确认：
// 数据上下文 `"ctnet",6` / IMS 上下文 `"ims",5`，QCI 5 正是 IMS 信令的标准等级）：
//
//	^DSAMBR: 1,1000000,1000000,"ctnet",6   → 下行 1000 / 上行 1000 Mbps，QCI 6（数据上下文）
//	^DSAMBR: 5,300000,75000,"ims",5        → 下行 300 / 上行 75 Mbps，QCI 5（IMS 上下文）
//
// ⚠️ QCI 是**逐上下文**的（同一个 APN/承载才可比），所以跟 AMBR 归在同一条记录里。
// 与移远路径统一成 Mbps 展示。已交叉验证：同一张卡在 RM520 上用
// `AT+QNWCFG="nr5g_ambr"` 报的也是 ctnet 1000/1000 与 IMS 300/75，两边完全对得上。
func ParseDSAMBR(line string) (*AMBRRate, bool) {
	m := reDSAMBR.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	dlKbps, err1 := strconv.Atoi(m[2])
	ulKbps, err2 := strconv.Atoi(m[3])
	if err1 != nil || err2 != nil || (dlKbps == 0 && ulKbps == 0) {
		return nil, false
	}
	return &AMBRRate{
		APN:    strings.TrimSpace(m[4]),
		CID:    common.AtoiOr0(m[1]), // 该行的 cid：选「拨号那条」时用
		DLMbps: KbpsToMbps(dlKbps),
		ULMbps: KbpsToMbps(ulKbps),
		QCI:    ParseQCI(m[5]), // 只有同时给了 APN 才会捕获到末位；缺失/越界 = 未上报
	}, true
}

// ParseQCI 把应答里的 QCI / 5QI 字段转成等级；空串、占位 `-`、超出 1~255 一律当未上报(0)。
//
// QCI 与 5QI 是同一件事的两个名字（4G/EPC 叫 QCI、5GS 叫 5QI），量纲一致，所以共用一条校验。
func ParseQCI(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 255 {
		return 0
	}
	return n
}

// KbpsToMbps kbps → 整 Mbps（四舍五入）。
// ⚠️ 只有 < 0.5 Mbps 的签约档位会被舍成 0（5G 上不存在这种档位）；
// 上层看到 0/0 会当“未上报”，而不是显示一个错的数字。
func KbpsToMbps(kbps int) int {
	if kbps <= 0 {
		return 0
	}
	return (kbps + 500) / 1000
}

// PickAMBR 在多个候选承载里挑出**拨号的那一条**（AMBR / CQI / QCI 一整套都只取它）：
//
//	① cid 精确匹配（拨号状态里记着的 cid；C5GQOSRDP / DSAMBR / CGCONTRDP 的行里带 cid）；
//	② APN 精确匹配（拨号状态里记着的 APN；移远 QNWCFG 的行里只有 APN，没有 cid）；
//	③ 退而取“非内部 APN”的第一条（IMS / sos 不是上网用的承载）；
//	④ 最后才是第一条（只有 IMS 可报时的兼容兜底）。
//
// cid<=0 / apn=="" 表示该维度不知道，直接跳过那一档；两维都不给就是 `ParseAMBR` 的旧口径。
// 入参 cands 必须只含“确实报了速率”的行（由调用方过滤）。
func PickAMBR(cands []*AMBRRate, cid int, apn string) (*AMBRRate, bool) {
	if len(cands) == 0 {
		return nil, false
	}
	if cid > 0 {
		for _, r := range cands {
			if r.CID == cid {
				return r, true
			}
		}
	}
	if apn != "" {
		for _, r := range cands {
			if strings.EqualFold(r.APN, apn) {
				return r, true
			}
		}
	}
	for _, r := range cands {
		if !IsInternal(r.APN) {
			return r, true
		}
	}
	return cands[0], true
}

// ParseAMBR 从应答里挑一条最合适的上下文（**不知道拨号上下文**时的旧口径）：
// 优先非内部 APN 的第一条（数据 APN），否则退回第一条带速率的（本机就是 IMS 这条）。
func ParseAMBR(resp []string) (*AMBRRate, bool) {
	return ParseAMBRContext(resp, 0, "")
}

// ParseAMBRContext 同上，但**只取拨号的那条承载**（cid 优先、其次 APN，见 PickAMBR）。
// cid / apn 来自 `Modem.dialedPDP()`；两维都取不到时退回 ParseAMBR 的旧口径。
func ParseAMBRContext(resp []string, cid int, apn string) (*AMBRRate, bool) {
	var cands []*AMBRRate
	for _, line := range resp {
		var r *AMBRRate
		var ok bool
		switch up := strings.ToUpper(line); {
		case strings.Contains(up, "QNWCFG"):
			r, ok = ParseQNWCFG(line)
		case strings.Contains(up, "DSAMBR"):
			r, ok = ParseDSAMBR(line) // 鼎桥 MT5700
		default:
			continue
		}
		// ⚠️ 只收“真报速率”的行：QNWCFG 允许 0/0（占位），旧口径也是把它们丢掉的
		if !ok || (r.DLMbps == 0 && r.ULMbps == 0) {
			continue
		}
		cands = append(cands, r)
	}
	return PickAMBR(cands, cid, apn)
}
