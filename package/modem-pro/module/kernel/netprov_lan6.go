package kernel

import (
	"modemd/module/config"
	"modemd/module/netprov"
	"strings"
)

// lan6FixModels 生效的"要给 LAN 补 /64 路由 + NDP relay"的型号名单。
//
// ⚠️ 这一项和 `v6FixModels()`（= `[net] v6_fix_models`，管 dhcpv6 用哪套选项 + 要不要写
// `/etc/firewall.user` 那段）**是两件事**，所以分开两个配置：
//
//	· 谁能拿"非 QMI 那套 dhcpv6 选项 + 自定义防火墙那段" → `[net] v6_fix_models`（ECM 类，如 RM500U）
//	· 谁需要"把模组的 /64 以低 metric 钉在 LAN + 配 odhcpd NDP relay" → `[net] lan6_fix_models`
//
// 为什么必须分开：**只要模组的 /64 同时被发给了内网**（我们写 `ip6prefix` 的机型都这样），
// 内核里就会同时出现
//
//	240e:…::/64 dev wwan0  proto static metric 256    ← 上游（模组）
//	240e:…::/64 dev br-lan proto static metric 1024   ← LAN 分配
//
// 同前缀只按 metric 选路 ⇒ **回给内网设备的包会被送去模组**，包直接消失。
// 症状就是用户 2026-10-08 报的："IPv6 能访问，有时候就那一次不能访问" / Windows 上
// 「IPv6 连接：无 Internet 访问权限」（NCSI 的 v6 探测时通时不通）。
// 真机取证：`ip -6 route get <内网设备>` 显示 `dev wwan0`（应该是 `dev br-lan`）。
//
// 所以这一项**跟 ECM/QMI 无关**：QMI 的机型（比如 FM160-CN）一样需要它。
// 名单同样是配置：`[net] lan6_fix_models`；**留空 = 跟 `v6_fix_models` 用同一份名单**（保持原行为）。
func lan6FixModels() []string {
	if len(config.Cfg.Net.Lan6FixModels) > 0 {
		return config.Cfg.Net.Lan6FixModels
	}
	return v6FixModels()
}

// lan6FixProfileFor 这台模组要不要"补 /64 路由 + NDP relay"（只看型号在不在名单里）。
//
// ⚠️ 取**并集**：`v6_fix_models` 名单里的型号（如 RM500U）**一直**享有 ③④ —— 用户把
// `lan6_fix_models` 设成别的型号时，不该把 RM500U 那条也一起弄丢（那是它的内网 v6 命根子）。
func lan6FixProfileFor(mod *Modem) bool {
	if mod == nil {
		return false
	}
	// ★ 2026-10-13：先按**链路类型**判（ecm / ncm / rndis 都要这套 —— 上游只发 RA、不做 PD）；
	// 再看型号名单（`v6_fix_models` 与 `lan6_fix_models`，留给"额外的"型号）。三者取并集。
	if wan6OnlinkFixFor(mod) {
		return true
	}
	if wan6NonQMIProfile(mod.Model, mod.ModelName) {
		return true
	}
	return modelInList(lan6FixModels(), mod)
}

// modelInList 型号关键字匹配（忽略 空格 / - / _ / . / /，只看关键字在不在型号串里）
func modelInList(list []string, mod *Modem) bool {
	if mod == nil {
		return false
	}
	return wan6ModelInList(list, mod.Model, mod.ModelName) // 同一套匹配，见 netprov.go
}

// lan6Candidates 纯函数：从受管接口里挑出"**v6 现在挂在哪张接口上**"的那些。
//
// ★★ 2026-10-13（用户实测）：取决于 [net] wan_mode ——
//
//	want6=false（模式 1 modem_pro 单接口）：v6 在**主接口** modem<id> 上，没有 modem<id>_6；
//	want6=true （模式 2/3）　　　　　　　　：v6 在 v6 兄弟接口 modem<id>_6 上。
//
// 老代码只认 `_6`，于是模式 1 下"把 /64 交给内网"那两步（LAN 低 metric 路由 + odhcpd NDP
// 代理）整个不会做 ⇒ 内网设备拿得到地址却出不了公网。用户原话：
// "500U ECM 用 Modem-Pro 不行，dhcpv4+v6 就可以；能拿到 ipv6 是防火墙[那套处理]
//
//	没有像那个一样处理"。
//
// ⚠️ 模式 2/3 的行为**完全不变**（want6=true ⇒ 仍然只看 `_6`）。
func lan6Candidates(list []netprov.ManagedWANIface, want6 bool) []netprov.ManagedWANIface {
	var out []netprov.ManagedWANIface
	seen := map[string]bool{}
	for _, it := range list {
		if !isManagedIface(it.Name) {
			continue
		}
		if want6 != strings.HasSuffix(it.Name, "_6") {
			continue // 模式 2/3 只看 `_6`；模式 1 只看主接口
		}
		if seen[it.Name] {
			continue
		}
		seen[it.Name] = true
		out = append(out, it)
	}
	return out
}
