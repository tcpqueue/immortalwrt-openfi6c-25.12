package kernel

import (
	"modemd/module/config"
	"strings"
)

// v6FixModels 现在生效的"要**特别处理 IPv6**"的型号名单。
//
// ⚠️⚠️ 这套"特别处理"= dhcpv6 用 auto/hostname/extendprefix/extend_prefix（不带 ip6prefix）+
// 写 `/etc/firewall.user` 里那段 DNS 重定向 + IPv6 MASQUERADE + 写 `route6 modemd_lan6route` +
// 配 odhcpd 的 NDP relay。**只对名单里的型号生效**：
//
//	· 名单里（默认只有 rm500u）⇒ 四样都做；
//	· 名单外 ⇒ 四样**都不写**，而且以前写过的会被收掉（见 syncWAN6FirewallDevices /
//	  syncLANPrefixRoute / syncLANNDPProxy 的空清单分支）—— 这是用户 2026-10-08 的硬要求：
//	  "这些修改一点只针对 500U 模块"。
//
// 名单来自配置 `[net] v6_fix_models`（config.toml）：**以后发现哪个新型号也要这套，
// 往配置里加一行就行，不用改代码**。配置留空就用内置默认 `wan6NonQMIProfiles`（见 netprov.go）。
// 匹配忽略 空格 / - / _ / . / /，只看型号关键字。
func v6FixModels() []string {
	if len(config.Cfg.Net.V6FixModels) > 0 {
		return config.Cfg.Net.V6FixModels
	}
	return wan6NonQMIProfiles
}

// wan6OnlinkFixKinds 需要那套"上游只给 on-link RA"补救的**链路类型**。
//
// ★★ 2026-10-13（用户要求，推翻 2026-10-08 的"只看 500U"口径）：
//
//	用户原话："v6_fix_models 把这个去掉，应该 ecm 都要这么处理的，ecm 全部按走这个"。
//
// **为什么按类型就对**：这几类的共同点是 —— 模组在主机侧桥出一张普通以太网卡
// （usb0 / eth1 / wwan0 …），主机自己跑 DHCP/RA；而**上游只发 RA/SLAAC、不做 DHCPv6-PD**。
// 于是内网拿到的那个 /64 必须靠 ①LAN 上一条低 metric 路由 + ②odhcpd NDP relay 才通
// （详见 LAN6RouteMetric 与 syncLANNDPProxy 的注释）。
// QMI 不需要：它的数据面归主机侧拨号器、前缀走 PDN 正常路由，运营商本来就知道往哪送。
//
// ★★ **MBIM 也算（2026-10-13 加）**：把 MBIM 统一成走主机侧拨号器之后，真机
// （H29K + FM160-CN MBIM）复现出与 2026-10-07 ECM/RM500U **一模一样的症状**：
//
//	ULA（fdb0:…）ping 通、全局地址 ping 不通；内网主机（`/tmp/odhcpd.leases` 里那位）
//	只拿到**老前缀**的 DHCPv6 租约 + ULA，当前 /64 一个地址都没有。
//
// 机理与 ECM 完全同类：MBIM 也是「模组桥出一张网卡（wwan0）+ 上游只给 on-link /64、
// 不做 PD」，而这个 /64 又被原样交给了 LAN ⇒ 公网回包时上游要在模组链路上 NS 找内网设备，
// 必须由路由器替它应答。所以 MBIM 要享受和 ECM 一样的两步。
var wan6OnlinkFixKinds = []string{"ecm", "ncm", "rndis", "mbim"}

// wan6NeedsOnlinkFix 纯函数：这个链路类型要不要那套补救（见 wan6OnlinkFixKinds）。
func wan6NeedsOnlinkFix(kind string) bool {
	k := strings.ToLower(strings.TrimSpace(kind))
	if k == "" {
		return false // 类型还没探到 ⇒ 先不做（下一轮探到了自然会补上）
	}
	for _, want := range wan6OnlinkFixKinds {
		if k == want {
			return true
		}
	}
	return false
}

// wan6OnlinkFixFor 这台模组要不要那套补救（按**链路类型**判；型号名单由调用方另行叠加）。
func wan6OnlinkFixFor(mod *Modem) bool {
	if mod == nil {
		return false
	}
	return wan6NeedsOnlinkFix(hostDialKind(mod, nil))
}
