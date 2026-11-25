package kernel

import (
	"strings"
)

// ====================== 校验（写完 AT 就改不回来了，先卡住非法值） ======================

// ValidCID PDP 上下文编号。实测 RM502Q-GL 的 AT+CGDCONT=? 报 (1-42)；
// ⚠️ 广和通 FM650-CN 报 **(0-11)** 且 `AT+CGDCONT?` 里真的有 cid 0（默认承载，带 IP）——
// 页面会把它列出来，所以下限放宽到 0（3GPP 本就允许 0）。
func ValidCID(cid int) bool { return cid >= 0 && cid <= 42 }

// ValidPDPType 只允许实测枚举出来的三种（+CGDCONT=? 里的 IP / IPV6 / IPV4V6，PPP 不用于数据）。
func ValidPDPType(t string) bool {
	switch strings.ToUpper(strings.TrimSpace(t)) {
	case "IP", "IPV6", "IPV4V6":
		return true
	}
	return false
}

// ValidAPN APN 字符串校验。
//
// 只允许字母/数字与 `.` `-` `_`：# 与 , " 会破坏 AT 指令语法（相当于把用户输入
// 拼进命令行），必须挡掉；长度按 3GPP 上限 100 字节。
func ValidAPN(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '-' || r == '_':
		default:
			return false
		}
	}
	return true
}

// ValidPLMN 手选运营商号：MCC+MNC 共 5~6 位数字
func ValidPLMN(s string) bool {
	if len(s) != 5 && len(s) != 6 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
