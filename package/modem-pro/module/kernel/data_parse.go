package kernel

import (
	"modemd/module/atparse"
	"strconv"
	"strings"
)

// ====================== 解析 ======================

// ParseCGDCONT 解析 AT+CGDCONT? 应答。
//
// ⚠️ 两种形态都要认（第二种是 2026-10-05 在中兴 F50 / 展锐 UMS9620 上实测到的原文）：
//
//	标准（带引号）	+CGDCONT: 1,"IP","ctnet","10.0.0.1",0,0
//	展锐（不带引号，APN 可能是空串）
//	                +CGDCONT:1,IPV4V6,,0.0.0.0,0,0,0,2,,,,,,0,,,0,,0,0,0,0
//	                +CGDCONT:11,IPV4V6,IMS,0.0.0.0,0,0,0,2,,,,,,0,,,0,,0,0,0,0
//
// 只认带引号那条正则时，展锐的应答**一行都解不出来** ⇒ `/api/params` 的 contexts
// 恒为空（apn 只能靠 AT+CGCONTRDP 兜底拿到）—— 真机就是这个现象。
//
// ⚠️ 兜底解析刻意"严"：cid 必须合法、PDP 类型必须在 3GPP 已知集合里、APN 里不许有逗号
// —— 免得把其它厂商的畸形行当成真上下文（那会让界面多出假条目）。
func ParseCGDCONT(resp []string) []Context {
	out := make([]Context, 0, 4)
	for _, line := range resp {
		cid, pdp, apnName, ok := parseCGDCONTLine(line)
		if !ok {
			continue
		}
		out = append(out, Context{
			CID:      cid,
			PDPType:  pdp,
			APN:      apnName,
			Internal: atparse.IsInternal(apnName),
		})
	}
	return out
}

// validPDPType 3GPP TS 27.007 `<PDP_type>` 的已知取值（含少数厂商扩展）。
// 只给"无引号形态"的兜底解析把关，不参与其它地方。
func validPDPType(s string) bool {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "IP", "PPP", "IPV4", "IPV6", "IPV4V6", "NON-IP", "NONIP",
		"X.25", "ETHERNET", "CDMA2000":
		return true
	}
	return false
}

// parseCGDCONTLine 解析一行 AT+CGDCONT? 应答，兼容带引号与不带引号两种形态。
func parseCGDCONTLine(line string) (cid int, pdpType, apnName string, ok bool) {
	if m := ReCGDCONT.FindStringSubmatch(line); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, "", "", false
		}
		return n, strings.ToUpper(strings.TrimSpace(m[2])), strings.TrimSpace(m[3]), true
	}
	// 无引号形态：<cid>,<PDP_type>,<APN>,…
	i := strings.Index(line, "+CGDCONT:")
	if i < 0 {
		return 0, "", "", false
	}
	fields := strings.Split(line[i+len("+CGDCONT:"):], ",")
	if len(fields) < 3 {
		return 0, "", "", false
	}
	n, err := strconv.Atoi(strings.TrimSpace(fields[0]))
	if err != nil || !ValidCID(n) {
		return 0, "", "", false
	}
	pdp := strings.ToUpper(strings.TrimSpace(fields[1]))
	if !validPDPType(pdp) {
		return 0, "", "", false
	}
	rawAPN := strings.TrimSpace(fields[2])
	// 无引号形态里不该出现引号；出现了就要求**成对**（如 `"a,b"` 被逗号切开后
	// 会变成 `"a` + `b"`，那种行本来就不是这个形态，直接拒掉）。
	if strings.Contains(rawAPN, `"`) {
		if len(rawAPN) < 2 || !strings.HasPrefix(rawAPN, `"`) || !strings.HasSuffix(rawAPN, `"`) {
			return 0, "", "", false
		}
		rawAPN = rawAPN[1 : len(rawAPN)-1]
	}
	apn := strings.TrimSpace(rawAPN)
	if strings.ContainsAny(apn, `",;`) {
		return 0, "", "", false
	}
	return n, pdp, apn, true
}

// ParseCGACT 解析 AT+CGACT? 应答 → cid → 是否激活
func ParseCGACT(resp []string) map[int]bool {
	out := map[int]bool{}
	for _, line := range resp {
		m := reCGACT.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		cid, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		out[cid] = m[2] == "1"
	}
	return out
}

// ParseCOPS 解析 AT+COPS? 应答。
//
// ⚠️ <oper> 字段的含义随 <format> 变：format 0 是**运营商名**（实测自动模式下
// RM502Q-GL 回 "????"），format 2 才是数字 PLMN。混着当 PLMN 显示会误导，
// 所以这里只把纯数字的值存进 PLMN。
func ParseCOPS(resp []string) (NetSelect, bool) {
	for _, line := range resp {
		if !strings.Contains(line, "+COPS:") {
			continue
		}
		m := reCOPSMode.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		mode, _ := strconv.Atoi(m[1])
		ns := NetSelect{
			Mode:     mode,
			ModeText: CopsModeText(mode),
			Raw:      strings.TrimSpace(line),
		}
		if oper := m[3]; oper != "" {
			// IsAllDigits（空串也会返回 true，所以外面还要判一次非空）
			if atparse.IsAllDigits(oper) {
				ns.PLMN = oper
			} else {
				ns.Operator = oper
			}
		}
		if m[4] != "" {
			ns.AcT, _ = strconv.Atoi(m[4])
		}
		return ns, true
	}
	return NetSelect{}, false
}

// CopsModeText 选网方式编号 → 中文（3GPP TS 27.007 <mode>）
func CopsModeText(mode int) string {
	switch mode {
	case 0:
		return "自动选网"
	case 1:
		return "手动选网"
	case 2:
		return "已注销（去注册）"
	case 3:
		return "仅设置格式"
	case 4:
		return "手动转自动"
	case 5:
		return "自动转手动"
	}
	return "未知模式"
}

// APNInContexts 回读确认：某个 cid 的 APN 是否已经等于目标值
func APNInContexts(list []Context, cid int, apnName string) bool {
	for _, c := range list {
		if c.CID == cid {
			return strings.EqualFold(c.APN, apnName)
		}
	}
	return false
}
