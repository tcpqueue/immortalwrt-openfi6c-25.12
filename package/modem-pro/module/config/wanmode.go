package config

// ============ WAN 接口形态（[net] wan_mode）：三种模式 ============
//
// 【用户 2026-10-13 要求】自动组网生成的 WAN 接口，形态要能选：
//
//	1. modem_pro —— 只用一张接口（proto=modem_pro），IPv4+IPv6 都跑在它上面。**默认**。
//	2. dhcp      —— 传统两张：modem<id>(proto=dhcp) + modem<id>_6(proto=dhcpv6)。
//	3. both      —— 两种都要：modem<id>(proto=modem_pro) + modem<id>_6(proto=dhcpv6)。
//
// ★★ 「不分协议」：模式 1/3 下**不再只认 QMI** —— qmi / ecm / ncm / rndis 以及认不出的类型
// 一律用 modem_pro（见 netprov_proto.go 的 wanProtoForKindMode）。这样 ECM/NCM/RNDIS 也不会
// 再出现 modem0 + modem0_6 两张接口（用户反馈的现象就是"只有 qmi 会切成一张，其它都是两个"）。
//
// ⚠️ **MBIM 是唯一例外**：它的拨号只能在 netifd 侧做（`mbim.sh` / `umbim`），
// 换成 modem_pro 就没有任何东西去建立 MBIM 会话 ⇒ 直接断网。所以 MBIM 一律保持
// `proto=mbim`。它本来就在一张接口上配好 v4+v6，不存在"出现两个"的问题。
//
// ⚠️ 本文件只放**纯策略**（不碰 UCI、不发 AT、不读环境），便于单测。
// 真正的建接口 / 改接口在 netprov_engine.go。

import "strings"

// WANMode 自动组网生成的 WAN 接口形态。
type WANMode string

const (
	// WANModeModemPro 只用一张接口（proto=modem_pro），v4+v6 都在它上面。**默认**。
	WANModeModemPro WANMode = "modem_pro"
	// WANModeDHCP 传统两张：modem<id>(dhcp) + modem<id>_6(dhcpv6)。老行为。
	WANModeDHCP WANMode = "dhcp"
	// WANModeBoth 两种都要：modem<id>(modem_pro) + modem<id>_6(dhcpv6)。
	WANModeBoth WANMode = "both"
)

// AllWANModes 三种模式，顺序就是界面上的显示顺序（默认的排第一）。
var AllWANModes = []WANMode{WANModeModemPro, WANModeDHCP, WANModeBoth}

// ParseWANMode 把配置里的字符串解析成模式。
//
// 认不出的值（含空串、写错）一律回落到**默认 WANModeModemPro** ——
// 配置写错不该让用户的网络变成另一种形态，而默认正是用户点名要的那个。
//
// 顺带接受几个好记的别名（写配置的人不用去翻文档）。
func ParseWANMode(s string) WANMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(WANModeDHCP), "dhcp_v4v6", "dhcpv4v6", "dhcpv6", "v4v6", "v6", "2", "two":
		return WANModeDHCP
	case string(WANModeBoth), "all", "both_modes", "modem_pro_dhcp", "modem_pro+dhcp", "3", "three":
		return WANModeBoth
	case string(WANModeModemPro), "modempro", "modem-pro", "pro", "1", "one":
		return WANModeModemPro
	}
	return WANModeModemPro
}

// WANModeNow 当前生效的接口模式（读 [net] wan_mode）。
func WANModeNow() WANMode { return ParseWANMode(Cfg.Net.WANMode) }

// UsesModemPro 主接口（modem<id>）是不是用 modem_pro 协议 —— 模式 1 / 3。
func (m WANMode) UsesModemPro() bool {
	return m == WANModeModemPro || m == WANModeBoth
}

// WantsDHCPv6Sibling 要不要另建 modem<id>_6（proto=dhcpv6）兄弟接口 —— 模式 2 / 3。
func (m WANMode) WantsDHCPv6Sibling() bool {
	return m == WANModeDHCP || m == WANModeBoth
}

// String 归一化后的配置值（写回界面 / 日志用）。
func (m WANMode) String() string { return string(ParseWANMode(string(m))) }

// Label 中文说明（日志、界面提示用）。
func (m WANMode) Label() string {
	switch ParseWANMode(string(m)) {
	case WANModeDHCP:
		return "DHCP V4 和 V6（两张接口）"
	case WANModeBoth:
		return "Modem-Pro + DHCP V4 和 V6（两种都要）"
	}
	return "Modem-Pro（单接口，推荐）"
}

// WANModeListPayload 三种模式的取值与中文说明（给页面渲染下拉框用）。
func WANModeListPayload() []map[string]string {
	out := make([]map[string]string, 0, len(AllWANModes))
	for _, m := range AllWANModes {
		out = append(out, map[string]string{"value": m.String(), "label": m.Label()})
	}
	return out
}
