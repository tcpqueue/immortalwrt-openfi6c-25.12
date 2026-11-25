package kernel

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// lan6WantOf 纯决策：从候选里挑出"要交给内网的 /64"与对应的接口名。
//
// ⚠️⚠️ **只认 designated（= `wan6NonQMIProfiles` 名单里的型号，当前只有 RM500U）**：
// 用户 2026-10-08 明确要求 —— "`route6 modemd_lan6route` 这个地方也是只要有 500u 才加的
// `/etc/config/network` 里面"。也就是：别的模组在 `/etc/config/network` 里**一个字都不能加**
// （route6 段与 NDP 那两个 dhcp 段都用这一份判断），不是名单里的模组 ⇒ 返回空 ⇒
// 已经有我们加的段会被 `LAN6RouteCommands` / `LAN6NDPCommands` 主动删掉。
//
// ⚠️ 放在**本文件**（无构建标签）而不是 `netprov_engine.go`（带 `//go:build !windows`）：
// 否则 Windows 上根本编译不到它，单测没法跑（踩过）。
func lan6WantOf(items []lan6Item) (prefix string, ifaces []string) {
	for _, it := range items {
		if !it.Designated || strings.TrimSpace(it.Prefix) == "" || strings.TrimSpace(it.Iface) == "" {
			continue
		}
		ifaces = append(ifaces, it.Iface)
		if prefix == "" {
			prefix = it.Prefix
		}
	}
	sort.Strings(ifaces) // 顺序稳定 ⇒ 同一组接口每轮生成的命令逐字节一致
	return prefix, ifaces
}

// LAN6RouteMetric 给 LAN 那条 /64 路由的 metric。
//
// ⚠️⚠️ 必须 < 256：同一个 /64 会**同时**挂在两张网卡上 ——
//
//	240e:…::/64 dev usb0    proto kernel metric 256   （netifd 给上游网卡配地址时自动生成）
//	240e:…::/64 dev br-lan  proto static metric 1024  （LAN 前缀分配 `ip6assign` 生成）
//
// 内核碰到同前缀**只按 metric 选路** ⇒ 路由器会把回给内网设备的包从 usb0 丢给模组，包直接消失。
// 2026-10-07 真机定案（用户报「IPv6 开始能用、过一段时间电脑就不能访问了」）：
// 内网设备的全局地址都在、寿命正常，但 `ip -6 route get <内网设备>` 显示 `dev usb0`、
// `ping6 <内网设备>` 100% 丢包；而 ULA（只有 br-lan 一条路由）0% 丢包、`ping6 2400:3200::1` 也通。
// 加这条低 metric 的 LAN 路由后立刻恢复（`dev br-lan … metric 100`，0% 丢包）。
const LAN6RouteMetric = 100

// LAN6RouteCommands 每轮对账：把「内网那个 /64」钉在 LAN 上（原因见 LAN6RouteMetric）。
//
// ⚠️⚠️ **只有"要专门处理的型号"（`wan6NonQMIProfiles`，当前 = RM500U）才会走到这里**：
// 用户 2026-10-08 明确要求 —— "`route6 modemd_lan6route` 这个地方也是只要有 500u 才加的
// `/etc/config/network` 里面"；别的模组 want 永远是空串 ⇒ 我们加的段会被下面第一个分支删掉。
// 判据在 `lan6WantOf`（`netprov_engine.go`），**不在本函数**。
//
//	want   现在该用的前缀（指定模组的 /64；模组不在/不是名单里的型号/没有全局地址 ⇒ 空串）
//	cur    现在 UCI 里那个 route6 段的 target（读不到 ⇒ 空串）
//	iface  LAN 接口名（空 ⇒ 不知道怎么钉，不动）
//
// 返回要在设备上执行的命令；**没事可做时返回 nil**——
// ⚠️ 写一次网络配置会让 netifd 重新读一遍（可能打断正在用的链路），只在真变了时才写。
//
// ⚠️⚠️ **`uci commit network` 自己不会生效**（2026-10-07 真机实测：commit 完等 3 秒，
// 新 target 对应的路由压根没进内核）⇒ 每次写完/删掉后都必须跟着一条 network reload。
// 模组重拨换 /64 时全靠这一条把 LAN 路由改成新前缀（用户明确要求「必须动态」）。
func LAN6RouteCommands(want, cur, iface string) [][]string {
	want, cur = strings.TrimSpace(want), strings.TrimSpace(cur)
	sec := "network." + LAN6RouteSection
	switch {
	case want == "" && cur == "":
		return nil // 谁都不需要它
	case want == "":
		// 模组不在了 / 没拿到全局地址 ⇒ 把我们加的那段收掉
		return [][]string{{"uci", "-q", "delete", sec}, {"uci", "commit", "network"}, networkReloadCmd()}
	case cur == want:
		return nil // 已经对了
	case iface == "":
		return nil
	}
	return [][]string{
		{"uci", "set", sec + "=route6"},
		{"uci", "set", sec + ".interface=" + iface},
		{"uci", "set", sec + ".target=" + want},
		{"uci", "set", sec + ".metric=" + strconv.Itoa(LAN6RouteMetric)},
		{"uci", "commit", "network"},
		networkReloadCmd(),
	}
}

// networkReloadCmd 让 netifd 重新读一遍网络配置。
//
// `/sbin/reload_config` 就是这一条（`ubus call network reload`）：**只应用配置差异**，
// 配置没变的接口不会被重启 ⇒ 比 `ifup <iface>` 轻得多，不会把在用的模组链路抖掉。
func networkReloadCmd() []string { return []string{"ubus", "call", "network", "reload"} }

// ---- NDP 代理：内网设备能不能出公网的那一环 ----
//
// 2026-10-07 真机定案（H29K + RM500U）：模组给的是「on-link 的 RA /64」而**不是前缀委派（PD）**，
// 而这个 /64 又被我们原样发给了 LAN ⇒ **同一个 /64 同时在上游和 LAN 上**。于是从公网回来的包，
// 上游要在链路上发 NS 找内网设备的地址，**必须由路由器替它应答**（proxy NDP）。缺这一环的症状
// 特别迷惑人（看起来完全不像路由器的问题）：
//
//	· 路由器自己 `ping6 2400:3200::1` **0% 丢包**（它的地址本来就在上游网卡上）；
//	· 内网设备 `ping6` 任何公网 v6 地址 **100% 丢包**（ping 路由器内网口却是通的）；
//	· 路由器 `Ip6OutForwDatagrams` 在涨（包确实被转出去了）、`ip -6 neigh show proxy` **却是空的**、
//	  `/proc/sys/net/ipv6/conf/<上游网卡>/proxy_ndp` **= 0**。
//	  手工 `ip -6 neigh add proxy <内网设备地址> dev <上游网卡>` ⇒ 立刻 0% 丢包（根因确认）。
//
// 实现在 odhcpd 的 "NDP relay" 里：它自己会把相关接口的 `proxy_ndp` 打开，并按**邻居事件**
// 自动增删代理条目（实测：手动清零 sysctl + `ip -6 neigh flush` + 重启 odhcpd ⇒ 几秒后条目全回来
// ⇒ 扛重启，不需要我们自己写脚本维护）。我们要做的只是把配置摆好：
//
//	① 给**模组的 v6 接口**加一个 dhcp 段：`ignore=1` + `master=1` + `ndp=relay`
//	   （`master` 是"上游那侧"的标记 —— ⚠️ 实测只设 LAN 那一半**一条代理条目都不会产生**）；
//	② 把 LAN 的 `ndp` 设成 relay。
//
// ⚠️ 只碰"我们建的段（名字带 LAN6NDPSectionPrefix）"与 `dhcp.lan.ndp`（且要用标记记着是我们写的），
// 别人的 dhcp 段一根都不动。

// LAN6NDPSectionPrefix 我们建的 dhcp 段名前缀（段名 = 前缀 + 接口名，如 modemd_ndp_modem0_6）
const LAN6NDPSectionPrefix = "modemd_ndp_"

// LAN6NDPOursOption 我们在 `dhcp.lan` 上留的标记 option（=1 ⇒ 那条 `ndp` 是我们写的）。
//
// ⚠️ 只用于 LAN 那一段：我们建的 master 段靠**段名前缀**就能认出来，不需要标记。
// 有了它才能在"模组不用了"的时候只收自己加的那条，不碰用户自己配的 `ndp`。
const LAN6NDPOursOption = "modemd"

// LAN6NDPMode odhcpd 的 NDP relay 模式（relay / hybrid 都能用，取 relay）
const LAN6NDPMode = "relay"

// LAN6NDPSection 现在 dhcp 配置里与"把哪个接口挂成 NDP relay master"有关的段
type LAN6NDPSection struct {
	Name   string // 段名
	Iface  string // option interface
	Ignore string
	Master string
	NDP    string
}

// satisfied 这一段是不是已经把 iface 按我们要的样子配好了（"已经对了就一个字节都不写"）
func (s LAN6NDPSection) satisfied(iface string) bool {
	return s.Iface == iface && s.Ignore == "1" && s.Master == "1" &&
		(s.NDP == "relay" || s.NDP == "hybrid")
}

// LAN6NDPMasterSection 接口 iface 对应的我们那个 dhcp 段名
func LAN6NDPMasterSection(iface string) string { return LAN6NDPSectionPrefix + iface }

// LAN6NDPCommands 决定要跑的 uci 命令（**没事可做时返回 nil**）。纯逻辑，见上面那段注释。
//
//	ifaces     在用、且 /64 已经交给内网 的模组 v6 接口名（顺序不敏感，内部会排序）
//	sections   现在 dhcp 里相关的段
//	curLanNDP  现在 `dhcp.lan.ndp` 的值
//	lanNDPOurs 现在 `dhcp.lan.modemd` 的值是不是 1（= 那条 ndp 是我们写的）
func LAN6NDPCommands(ifaces []string, sections []LAN6NDPSection, curLanNDP string, lanNDPOurs bool) [][]string {
	want := sortUniqueIfaces(ifaces)
	inWant := func(iface string) bool {
		for _, w := range want {
			if w == iface {
				return true
			}
		}
		return false
	}

	var cmds [][]string

	// ① 收掉"我们建的、但已经不用的"段（按段名前缀认，别人的段不碰）
	var stale []string
	for _, s := range sections {
		if strings.HasPrefix(s.Name, LAN6NDPSectionPrefix) && !inWant(s.Iface) {
			stale = append(stale, s.Name)
		}
	}
	sort.Strings(stale)
	for _, name := range stale {
		cmds = append(cmds, []string{"uci", "-q", "delete", "dhcp." + name})
	}

	if len(want) > 0 {
		// ② 每一台在用的模组：把它的 v6 接口挂成 NDP relay master
		for _, iface := range want {
			done := false
			for _, s := range sections {
				if s.satisfied(iface) {
					done = true // 已经配好了（不管是谁配的、叫什么名字）
					break
				}
			}
			if done {
				continue
			}
			sec := "dhcp." + LAN6NDPMasterSection(iface)
			cmds = append(cmds,
				[]string{"uci", "set", sec + "=dhcp"},
				[]string{"uci", "set", sec + ".interface=" + iface},
				[]string{"uci", "set", sec + ".ignore=1"},
				[]string{"uci", "set", sec + ".master=1"},
				[]string{"uci", "set", sec + ".ndp=" + LAN6NDPMode},
			)
		}
		// ③ LAN 那一半：内网要能被上游"找到"
		if curLanNDP != LAN6NDPMode || !lanNDPOurs {
			cmds = append(cmds,
				[]string{"uci", "set", "dhcp.lan.ndp=" + LAN6NDPMode},
				[]string{"uci", "set", "dhcp.lan." + LAN6NDPOursOption + "=1"},
			)
		}
	} else if lanNDPOurs {
		// 模组都不用这套了 ⇒ 把我们加的那条收回（用户自己配的 ndp 不动）
		cmds = append(cmds,
			[]string{"uci", "-q", "delete", "dhcp.lan.ndp"},
			[]string{"uci", "-q", "delete", "dhcp.lan." + LAN6NDPOursOption},
		)
	}

	if len(cmds) == 0 {
		return nil
	}
	// `uci commit dhcp` 会让 procd 触发 odhcpd 的 reload（它的 init 里 procd_add_reload_trigger "dhcp"），
	// 这里显式再来一次：改完配置必须让 odhcpd 重新装配 ndp relay，否则核里不会有代理条目。
	return append(cmds,
		[]string{"uci", "commit", "dhcp"},
		[]string{"/etc/init.d/odhcpd", "reload"},
	)
}

// ParseUCIShow 解析 `uci show <包>` 的输出，只收指定段类型的段（secType 为空 = 全收）。
//
// ⚠️⚠️ 别跟 ParseUCIConfig 混：**那一个**读的是**配置文件原文**（`config`/`option`/`list` 行，
// 比如 `/etc/config/network`），而 `uci show` 打出来的是 `包.段.选项='值'`（还有 `包.段=类型`）。
// 两种语法不是一回事 —— 用错就是"永远当成没配过"（ndp 对账那轮真的踩到了，每轮都在重写）。
func ParseUCIShow(pkg, secType, uciShow string) []UCISection {
	prefix := strings.TrimSuffix(strings.TrimSpace(pkg), ".") + "."
	if prefix == "." {
		return nil
	}
	idx := map[string]int{}
	var out []UCISection
	for _, raw := range strings.Split(uciShow, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		body := line[len(prefix):]
		eq := strings.Index(body, "=")
		if eq < 0 {
			continue
		}
		key, val := body[:eq], unquoteUCIValue(body[eq+1:])
		if !strings.Contains(key, ".") {
			// 段声明：<pkg>.<name>=<type>
			if secType != "" && val != secType {
				continue
			}
			if _, dup := idx[key]; dup {
				continue
			}
			idx[key] = len(out)
			out = append(out, UCISection{
				Type: val, Name: key,
				Options: map[string]string{}, Lists: map[string][]string{},
			})
			continue
		}
		// 选项：<pkg>.<name>.<opt>=<val>
		dot := strings.LastIndex(key, ".")
		name, opt := key[:dot], key[dot+1:]
		i, ok := idx[name]
		if !ok {
			continue // 段没声明过 / 不是我们要的类型
		}
		out[i].Options[opt] = val
	}
	return out
}

// sortUniqueIfaces 去空、去重、排序（顺序稳定 ⇒ 同一组接口生成的命令逐字节一致，不会反复重写）
func sortUniqueIfaces(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// lan6NDPSectionsFrom 从 `uci show dhcp` 的输出里挑出**与 NDP relay 有关**的段（纯逻辑，好单测）：
// 我们建的（名字带 LAN6NDPSectionPrefix）、或者声明了 master / ndp 的。其余段一眼都不看。
func lan6NDPSectionsFrom(uciShow string) []LAN6NDPSection {
	var list []LAN6NDPSection
	for _, sec := range ParseUCIShow("dhcp", "dhcp", uciShow) {
		s := LAN6NDPSection{
			Name:   sec.Name,
			Iface:  strings.TrimSpace(sec.Options["interface"]),
			Ignore: strings.TrimSpace(sec.Options["ignore"]),
			Master: strings.TrimSpace(sec.Options["master"]),
			NDP:    strings.TrimSpace(sec.Options["ndp"]),
		}
		if !strings.HasPrefix(s.Name, LAN6NDPSectionPrefix) && s.Master != "1" && s.NDP == "" {
			continue
		}
		list = append(list, s)
	}
	return list
}

// ---- 主机侧 IPv6 有没有跟上模组（「重拨一下 IPv6 就没了」的自愈） ----
//
// 2026-10-07 真机定案（用户报「只要重新拨号就会出问题」）：
// 模组每次重拨/重新激活 PDP 都会拿到**新的 /64**，但 **RM500U(ECM) 不会主动把新前缀再 RA 给主机**：
//
//	模组侧 +CGPADDR: 240e:478:620:163a:18dc:…    ← 已经换成新前缀了
//	主机侧 usb0:     240e:478:610:1e16:…         ← 还挂在旧前缀上（旧前缀上游已经不管了）
//
// 后果是**连路由器自己都出不了 v6**（它的地址也在旧前缀里），内网更不用说 —— 用户看到的就是
// 「重新拨号之后 IPv6 就没了」。实测（同一台机、同一个状态）：
//
//	· `ubus call network.interface.modem0_6 renew`      ❌ 没用（主机重问了一次，模组照样不理）
//	· `ifup modem0_6`（让主机重发 RS）                  ❌ 没用
//	· **把承载 v6 的那个 PDP 停掉再激活**                ✅ 立刻催出新 RA：
//	    RA 计数 +2、usb0 换成新前缀、默认路由跟着换、
//	    随后 modemd 的 /64 路由与 odhcpd 的 NDP 代理都会自己跟上（各 ≤ 15 s），内网立刻恢复。
//
// 所以这里做一层看门狗：主机侧与模组侧前缀一直对不上就弹一次 PDP，带冷却与次数上限。
// ⚠️ 阈值不能太小：模组自己也会慢吞吞地补一次 RA（实测 1~2 分钟），太急会把好链路弹断。

const (
	// HostV6StaleAfter 主机侧前缀与模组侧不一致持续多久才算"卡死"
	HostV6StaleAfter = 75 * time.Second
	// HostV6RetryAfter 两次弹 PDP 之间的冷却
	HostV6RetryAfter = 150 * time.Second
	// MaxHostV6Bounces 连续弹几次还不行就放弃（改成报错，别一直把用户的网弹来弹去）
	MaxHostV6Bounces = 3
)

// HostV6Action 看门狗该干什么
type HostV6Action int

const (
	HostV6Nothing HostV6Action = iota // 对得上 / 还没到动手的时候
	HostV6Bounce                      // 弹一下承载 v6 的 PDP（先断后连，让模组重发 RA）
	HostV6GiveUp                      // 弹够了还不行 ⇒ 报错收手
)
