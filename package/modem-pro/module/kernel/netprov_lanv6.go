//go:build !windows

package kernel

import (
	"context"
	"modemd/module/config"
	"os"
	"os/exec"
	"strings"

	"go.uber.org/zap"
)

// lanInterfaceName 找 LAN 接口名（写 route6 要用它）：优先 `lan`，否则第一个带 ip6assign 的接口
func lanInterfaceName() string {
	if v, err := uciGet("network.lan.proto"); err == nil && strings.TrimSpace(v) != "" {
		return "lan"
	}
	b, err := os.ReadFile(networkCfgPath)
	if err != nil {
		return ""
	}
	for _, sec := range ParseUCIConfig(string(b)) {
		if sec.Type == "interface" && sec.Name != "" && strings.TrimSpace(sec.Options["ip6assign"]) != "" {
			return sec.Name
		}
	}
	return ""
}

// globalPrefixOf 这张网卡上"现在在用"的全局 IPv6 前缀。
//
// ⚠️⚠️ 不能直接取"第一个全局地址"：换前缀后旧地址会以 **deprecated** 留在链上，而
// `net.Interface.Addrs()` 的顺序**不保证稳定** ⇒ 会拿到已经作废的旧前缀（LAN 路由与
// "主机有没有跟上模组"的看门狗都会跟着判错，实测踩过）。
// 规矩（与 pickLANIPv6Prefix 同一个方向）：优先内核**没标 deprecated** 的那个；
// 全都被标了才退回第一个（保守：宁可不动）；两边都排序，保证"同一组地址永远同一个选择"。
func globalPrefixOf(dev string) string {
	all, live := netdevGlobalPrefixes6(dev)
	if p := sortUniquePrefixes(live); len(p) > 0 {
		return p[0]
	}
	if p := sortUniquePrefixes(all); len(p) > 0 {
		return p[0]
	}
	return ""
}

// lan6WantNow 现在"该交给内网的模组 /64"与"对应的模组 v6 接口名"（判据见 netprov.go 的 lan6WantOf）。
//
// `ok=false` = UCI 没读成功（超时/被锁）：调用方应当**什么都不做** ——
// 把"读失败"当成"没有"会误删我们自己的配置（这个坑在防火墙那套上踩过一次）。
func lan6WantNow(mgr *ModemManager) (prefix string, ifaces []string, ok bool) {
	list, ok := managedInterfacesNowOK()
	if !ok {
		return "", nil, false
	}
	live := map[int]*Modem{}
	if mgr != nil {
		for _, mod := range mgr.listModems() {
			if mod != nil && mod.isRunningNow() {
				live[mod.ID] = mod
			}
		}
	}
	// ★★ 2026-10-13（用户实测）：v6 到底挂在哪张接口上，取决于 [net] wan_mode ——
	//
	//	模式 1（modem_pro 单接口）：v6 就在**主接口** modem<id> 上，压根没有 modem<id>_6；
	//	模式 2/3　　　　　　　　　：v6 在 v6 兄弟接口 modem<id>_6 上。
	//
	// 老代码只认 `_6`，于是模式 1 下这台模组的"把 /64 交给内网"那两步
	// （LAN 低 metric 路由 + odhcpd NDP 代理）**整个不会做** ——
	// 内网设备拿得到地址却出不了公网。用户原话：
	// "500U ECM 用 Modem-Pro 不行，dhcpv4+v6 就可以；能拿到 ipv6 是防火墙[那套处理]
	//  没有像那个一样处理"。
	//
	// ⚠️ 模式 2/3 的行为**完全不变**（want6=true ⇒ 仍然只看 `_6`）。
	var items []lan6Item
	for _, it := range lan6Candidates(list, config.WANModeNow().WantsDHCPv6Sibling()) {
		dev := strings.TrimSpace(it.Device)
		mod := live[it.ModemID]
		it6 := lan6Item{Iface: it.Name, Designated: lan6FixProfileFor(mod)}
		if dev != "" && it6.Designated {
			it6.Prefix = globalPrefixOf(dev) // 没拿到全局地址（拨号中/刚断）→ 前缀留空
		}
		items = append(items, it6)
	}
	prefix, ifaces = lan6WantOf(items)
	return prefix, ifaces, true
}

// syncLANPrefixRoute 每轮对账：保证"内网那个 /64"在 LAN 上有一条低 metric 路由。
// 决策（要不要写/写什么/要不要删）全在纯函数 LAN6RouteCommands 里，见它的注释。
func syncLANPrefixRoute(mgr *ModemManager) {
	want, _, ok := lan6WantNow(mgr)
	if !ok {
		logger.Warn("auto wan6: read network config failed, skip the lan /64 route sync")
		return
	}
	cur, _ := uciGet("network." + LAN6RouteSection + ".target")
	// 段不存在 / 类型不是 route6 ⇒ 当成"没有"（否则类型被改坏后再也不会自愈）
	if t, _ := uciGet("network." + LAN6RouteSection); strings.TrimSpace(t) != "route6" {
		cur = ""
	}
	iface := lanInterfaceName()
	if want != "" && iface == "" {
		logger.Warn("auto wan6: no lan interface found, cannot pin the /64 route to lan")
		return
	}
	cmds := LAN6RouteCommands(want, cur, iface)
	if len(cmds) == 0 {
		return // 已经对了（或谁都不需要它）
	}
	if err := runNetProvCmds(cmds); err != nil {
		logger.Warn("auto wan6: lan /64 route write failed", zap.String("err", err.Error()))
		return
	}
	if want == "" {
		logger.Info("auto wan6: lan /64 route removed")
		return
	}
	logger.Info("auto wan6: lan /64 route pinned to lan",
		zap.String("prefix", want), zap.Int("metric", LAN6RouteMetric), zap.String("was", cur))
}

// lan6NDPSectionsNow 读 /etc/config/dhcp，把与 NDP relay 有关的段挑出来
// （第二个返回值 = 这次读取成功；读不到时调用方应当**什么都不做**）。
// ⚠️ 解析用 ParseUCIShow（`uci show` 的语法），**不是** ParseUCIConfig（那是配置文件语法）。
func lan6NDPSectionsNow() ([]LAN6NDPSection, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), netProvTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "uci", "show", "dhcp").Output()
	if err != nil {
		return nil, false
	}
	return lan6NDPSectionsFrom(string(out)), true
}

// syncLANNDPProxy 每轮对账：让内网设备能出公网 —— 上游那个 /64 是"on-link + RA"（没有 PD），
// 公网回包要靠路由器替内网设备应答 NS（proxy NDP，由 odhcpd 的 ndp relay 实现）。
// 决策全在纯函数 LAN6NDPCommands 里，见 netprov.go 那段长注释。
func syncLANNDPProxy(mgr *ModemManager) {
	_, ifaces, ok := lan6WantNow(mgr)
	if !ok {
		logger.Warn("auto wan6: read network config failed, skip the ndp proxy sync")
		return
	}
	sections, ok := lan6NDPSectionsNow()
	if !ok {
		logger.Warn("auto wan6: read dhcp config failed, skip the ndp proxy sync")
		return
	}
	curLanNDP, _ := uciGet("dhcp.lan.ndp")
	ours, _ := uciGet("dhcp.lan." + LAN6NDPOursOption)
	cmds := LAN6NDPCommands(ifaces, sections, curLanNDP, ours == "1")
	if len(cmds) == 0 {
		return // 已经对了（或这台机器压根不需要）
	}
	if err := runNetProvCmds(cmds); err != nil {
		logger.Warn("auto wan6: ndp proxy update failed", zap.String("err", err.Error()))
		return
	}
	if len(ifaces) == 0 {
		logger.Info("auto wan6: ndp proxy removed")
		return
	}
	logger.Info("auto wan6: ndp proxy enabled（内网设备才能出公网 v6）", zap.Strings("ifaces", ifaces))
}
