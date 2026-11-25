package netprov

// ============ 主机侧 UCI 协议的选择（纯逻辑，便于单测） ============
//
// 【2026-10-13 起的口径】选哪个协议**不再只看模组类型**，而是看 [net] wan_mode
// （见 netprov_mode.go）：
//
//	模式 modem_pro（默认）→ 主接口 modem<id> 用 ModemProtoName（一张接口跑 v4+v6）
//	模式 dhcp            → 主接口用 dhcp（地址/路由/DNS 由 netifd 的 DHCP 取）
//	模式 both            → 主接口用 ModemProtoName
//
// ★★ 「不分协议」：模式 1/3 下 **qmi / ecm / ncm / rndis / mbim / 未知类型 一律 modem_pro**，
// 不再只认 QMI，也**不再有 MBIM 例外**。用户反馈的"只有 qmi 会切成一张，其它都是两个"
// 就是这么来的 —— 旧代码只有一个 `case "qmi"` 会返回 modem_pro，其它类型全落到 dhcp，
// 然后再被配上 modem<id>_6 那张 v6 兄弟接口。
//
// ★★ MBIM 为什么也能走 modem_pro（2026-10-13 用户要求「MBIM 统一成 modem_pro」）：
//
// MBIM 的活分成两段 ——
//
//	① 建会话：MBIM CID 的 caps → pin → registration → attach → **connect(APN)**
//	② 拿地址：模组在网卡上当 DHCP 服务器，或者 MBIM CID 里直接给静态地址
//
// 主机侧的**协议**（dhcp / modem_pro）只管 ②。① 必须有人做，否则 wwan0 上什么都没有、
// 直接断网 —— 这就是当初把 MBIM 设成例外的原因（只有 netifd 的 mbim.sh / umbim 会做 ①）。
//
// 现在不用例外了：**我们打进去的厂商拨号器自己就实现了 ①**：
//
//	third_party/quectel-cm/main.c   software_interface == SOFTWARE_MBIM
//	                                → request_ops = &mbim_request_ops（mbim-cm.c，完整的 MBIM 实现）
//	third_party/quectel-cm/device.c 判据是**标准 USB CDC 描述符**
//	                                （bInterfaceClass=COMM + bInterfaceSubClass=MBIM），
//	                                与厂商无关 ⇒ 非移远的 MBIM 模组同样走这条；
//	                                meig-cm 也编了同一份 mbim-cm.c。
//
// 于是 MBIM 跟 QMI 一样交给 modemd 托管的拨号器，接口就能收敛成一张 modem<id>。
// ⚠️ 唯一的坑：MBIM 模式下 quectel-CM **必须有 APN**（main.c 里没 `-s` 直接 exit(-404)），
// 所以 hostDialAPN 必须能取到（读模组侧 AT+CGDCONT?，或用页面里配过的 APN）。
//
// ⚠️ [net] host_dial 关掉时**退回 proto=mbim**：那时没有主机侧拨号器在跑，只剩 netifd 的
// mbim.sh(umbim) 能把会话建起来 —— 强行换成 modem_pro 就没人拨号，等于断网。
// `host_dial` 由 WanProtoForKindMode 的 hostDialOn 参数传进来（不读全局，见下）。

import (
	"strings"

	"modemd/module/config"
)

// ModemProtoName 本项目自带的「单接口双栈」协议名（luci-proto-modem-pro 包提供）。
//
// 它把 IPv4(DHCP) 与 IPv6(SLAAC/DHCPv6) 跑在**同一张**接口上，于是不用再建
// modem<id>_6 那张 v6 兄弟接口；IPv6 前缀也由它自己从 RA 跟随（RFC 7278），
// 不需要 modemd 再往 ip6prefix 里写值。
//
// ⚠️ 名字用**下划线**不是连字符：netifd 按 proto_${名}_setup 调函数，而 `-`
// 在 shell 函数名里非法（会报 `proto_modem-pro_setup: not found`）。
const ModemProtoName = "modem_pro"

// WANProtoForKind 模组的 USB 类型 → 主机侧 UCI 协议（netifd 的协议名）。
//
// 保留这个签名是为了让调用点不必关心模式 —— 它内部按**当前** [net] wan_mode 走。
// 真正做决定的纯函数是 WanProtoForKindMode（可以直接单测）。
func WANProtoForKind(kind string) string {
	return WanProtoForKindMode(kind, config.WANModeNow(), false, config.Cfg.Net.HostDial)
}

// WANProtoForKindWith 同 WANProtoForKind，但可以告诉它"路由器上装了 modem_pro 协议"。
//
// modemProtoOK 为真 + 模式是 1/3 时，**所有**非 MBIM 类型都返回 ModemProtoName；
// 否则沿用旧行为 dhcp（地址/路由/DNS 由 netifd 取）+ modem<id>_6 那张 v6 兄弟接口。
//
// 拆成两个函数是为了让本文件（纯逻辑层）不依赖运行环境 —— 真正的探测在
// ModemProtoAvailable（在 netprov_engine.go，只在 Linux/OpenWrt 上有意义）。
func WANProtoForKindWith(kind string, modemProtoOK bool) string {
	return WanProtoForKindMode(kind, config.WANModeNow(), modemProtoOK, config.Cfg.Net.HostDial)
}

// WanProtoForKindMode **纯决策**：模组类型 + 接口模式 + 协议包在不在 + 主机侧拨号开不开
// → 主接口用哪个 proto。
//
// 决策表（mode 见 netprov_mode.go）：
//
//	kind                          host_dial=on             host_dial=off
//	----------------------------  -----------------------  -------------------------
//	mbim                          modem_pro* / dhcp        mbim（netifd 的 mbim.sh 拨）
//	qmi/ecm/ncm/rndis/未知/其它    modem_pro* / dhcp        modem_pro* / dhcp
//
//	* 仅当 modemProtoOK（路由器上装了 luci-proto-modem-pro）；没装则退回 dhcp。
//
// ⚠️ hostDialOn **显式传进来、不读全局 Cfg**：这个函数是纯决策，单测直接钉它。
// 读全局会让用例之间互相污染（同进程改 Cfg.Net.* 是全局的）—— 这个坑在 v6 那套判据上踩过一次。
//
// ⚠️ 为什么 QMI/ECM 等退回 dhcp 而不是 none（2026-10-03 真机定下的口径）：
//
//	· 用 none 时 netifd 完全不管这张网卡 —— 地址是拨号器内部的 udhcpc 写进内核的，
//	  netifd/LuCI 那边看不到地址（「网络 → 接口」显示没有 IP）；
//	· 数据呼叫本身仍然必须由 modemd 托管的**厂商拨号器**来做（QMI 建 PDN(WDS)，
//	  MBIM 走 MBIM_CID_CONNECT），netifd 的 DHCP 只负责在 wwan0 上取地址 ——
//	  两者分工，不是二选一；
//	· 接口照样把 wwan0 挂进防火墙区（NAT/转发规则按接口的设备下发）。
func WanProtoForKindMode(kind string, mode config.WANMode, modemProtoOK, hostDialOn bool) string {
	// MBIM：会话建立必须先有人做。主机侧拨号器关着 = 只剩 netifd 的 mbim.sh ⇒ 保持 mbim。
	if IsMBIMKind(kind) && !hostDialOn {
		return "mbim"
	}
	if mode.UsesModemPro() && modemProtoOK {
		return ModemProtoName
	}
	return "dhcp"
}

// IsMBIMKind 纯匹配：这个 USB 类型是不是 MBIM（大小写 / 空白容忍）。
func IsMBIMKind(kind string) bool {
	return strings.EqualFold(strings.TrimSpace(kind), "mbim")
}

// WANHostDialByDaemon 这类模组的**数据呼叫**由 modemd 自己跑的拨号器做（见 hostdial.go）；
// netifd 那侧的接口是 modem_pro（模式 1/3）或 dhcp（模式 2），只负责取地址。
//
//	qmi  —— quectel-CM / simcom-cm / fibocom-dial / meig-cm 走 QMI（PDN/WDS）
//	mbim —— 同一个 quectel-CM 里的 mbim-cm.c（MBIM CID_CONNECT）；2026-10-13 起不再例外
//
// ⚠️ 这里**只看类型**，不看 [net] host_dial：host_dial 关掉时是"不许拨号器跑"，
// 由 hostDialStopReason 负责拦住（那时 MBIM 的协议也会退回 mbim，见 WanProtoForKindMode）。
func WANHostDialByDaemon(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "qmi", "mbim":
		return true
	}
	return false
}

// WANProtoUsesControlDevice 这个协议的 UCI `device` 指的是**控制口字符设备**（/dev/cdc-wdmN），
// 而不是主机侧网卡。
//
// 现在只有 mbim（umbim 拨号，且仅当 [net] host_dial 关着时才用）还这么写；
// qmi / quectel 是历史写法（老版本自动建的接口就是这么写的，迁移时会改成 modem_pro / dhcp）。
func WANProtoUsesControlDevice(proto string) bool {
	switch strings.ToLower(strings.TrimSpace(proto)) {
	case "mbim", "qmi", "quectel":
		return true
	}
	return false
}

// WANProtoHostDialed 这个协议的地址 / 默认路由 / DNS 由**主机侧拨号器**写进内核，netifd 不管。
// 这类接口不建 v6 兄弟接口（v6 也归拨号器管）。
//
// none 是 QMI 的新写法；qmi / quectel 是历史写法（当时靠 quectel.sh 拉起拨号器）—— 都算。
func WANProtoHostDialed(proto string) bool {
	switch strings.ToLower(strings.TrimSpace(proto)) {
	case "none", "qmi", "quectel", "mbim":
		return true
	}
	return false
}

// WANProtoNeedsScript netifd 要不要为这个协议找一个协议脚本（/lib/netifd/proto/<proto>.sh）。
// none / dhcp 不需要额外装东西；mbim 要 umbim；qmi / quectel 是历史写法。
func WANProtoNeedsScript(proto string) bool {
	switch strings.ToLower(strings.TrimSpace(proto)) {
	case "qmi", "quectel", "mbim", ModemProtoName:
		return true
	}
	return false
}

// WANProtoPackage 这种协议需要装的 OpenWrt 包（写进提示里，告诉用户 opkg install 什么）
//
// qmi（老写法）要 uqmi、mbim 要 umbim；quectel 是**本项目自带的**（quectel.sh +
// quectel-CM 都在 modemd-pro 包里），所以写包名本身 —— 看不到 quectel.sh 基本就是
// 包没装全 / 装的是老版本，重装一次即可。
func WANProtoPackage(proto string) string {
	switch strings.ToLower(strings.TrimSpace(proto)) {
	case "qmi":
		return "uqmi"
	case "quectel":
		return "modemd-pro"
	case "mbim":
		return "umbim"
	case ModemProtoName:
		return "luci-proto-modem-pro"
	}
	return ""
}
