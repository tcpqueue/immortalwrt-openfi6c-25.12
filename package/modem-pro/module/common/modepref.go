package common

// ====================== 组网模式偏好的归一化 / AT 应答小工具 ======================
//
// 从 module/kernel/vendor_settings_usb.go 与 vendor_settings_imei.go 搬过来：
// 厂商包（module/simcom、module/unisoc、module/lock）都要用，不能反向依赖内核。
// kernel 保留搬家前的老名字（normalizeModePref / firstLineWith）作为别名。

import "strings"

// NormalizeModePref 把设备回报的 mode_pref 原文归一成语义值。
//
// 移远回报可能是 `AUTO` / `LTE:NR5G` / `WCDMA:LTE` 这类；不同固件分隔符可能是 `:` 或 `_`。
// **认不出来的原文原样返回**（宁可显示原码，也不编一个档位）。
func NormalizeModePref(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	s = strings.NewReplacer("_", ":", ",", ":", " ", "", "/", ":").Replace(s)
	var has3g, has4g, has5g bool
	for _, p := range strings.Split(s, ":") {
		switch {
		case p == "AUTO" || p == "AUTOMATIC" || p == "ALL":
			return ModeAuto
		case strings.Contains(p, "WCDMA") || strings.Contains(p, "UMTS") || p == "3G":
			has3g = true
		case strings.Contains(p, "LTE") || p == "4G":
			has4g = true
		case strings.Contains(p, "NR") || p == "5G":
			has5g = true
		case p == "GSM" || p == "GW":
			// 2G 不单独成档（QModem 也只分 3G/4G/5G 三档）
		default:
			return raw
		}
	}
	switch {
	case has3g && has4g && has5g:
		return ModeAuto
	case has3g && has4g:
		return ModeWCDMALTE
	case has3g && has5g:
		return ModeWCDMANR
	case has4g && has5g:
		return ModeLTENR5G
	case has3g:
		return ModeWCDMA
	case has4g:
		return ModeLTE
	case has5g:
		return ModeNR5G
	}
	return raw
}

// FirstLineWith 取第一条含 sub 的应答行（忽略大小写）
func FirstLineWith(resp []string, sub string) (string, bool) {
	u := strings.ToUpper(sub)
	for _, l := range resp {
		if strings.Contains(strings.ToUpper(l), u) {
			return l, true
		}
	}
	return "", false
}
