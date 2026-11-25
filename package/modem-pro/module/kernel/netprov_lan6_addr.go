//go:build !windows

package kernel

// ====================== LAN 侧：把模组那个 /64 的地址"摆正" ======================
//
// 为什么单开一条（2026-10-13 用户实测"电脑访问不了 ipv6"，真机取证）：
//
//	`ip -6 addr show dev br-lan`
//	    inet6 240e:400:628:1be::1/64 scope global **deprecated** dynamic   ← 就这一条
//	    inet6 fd48:dc61:5a6b::1/60 scope global noprefixroute
//
//	LAN 上的前缀地址一旦被标 **deprecated**，内网设备就**不会**再从该前缀 SLAAC 出公网
//	地址（只剩 ULA）⇒ 客户端没有公网 v6 地址 ⇒ 我们那套"替内网设备做 NDP 代理"也无从代理
//	（没有地址可代理）。用户看到的就是"电脑 v6 访问不了"，而路由器自己 v6 是通的
//	（路由器用的是 WAN 侧那张网卡上的地址）。
//
//	修回来之后当场恢复：客户端 30 秒内拿到 `240e:…:5437:…`，`ip -6 neigh show proxy dev <wan>`
//	里出现它的地址，电脑就能出公网 v6 了。
//
// 这个地址是谁加的、为什么会 deprecated：
//
//	上游（模组）只发 RA、不做 PD，所以 LAN 的前缀地址是 odhcpd/netifd 按 relay 学来的前缀
//	加到 br-lan 上的（`dynamic`）。odhcpd 重启、前缀变更、或有人手工动过地址之后，它可能
//	**没被重建**或**带着 deprecated 标志重建** —— 两种都会让内网 v6 断掉。
//
// 所以这一条跟"低 metric 路由"、"odhcpd NDP relay"是**第三件**必须每轮对账的事：
// 前面两件（`syncLANPrefixRoute` / `syncLANNDPProxy`）保证**路由和代理**对，
// 这一件保证 **LAN 上真有这个前缀的可用地址**。

import (
	"os/exec"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// LAN6PrefixHextets 取地址/前缀的前 4 段（`240e:400:628:1be`）。
//
// 判"是不是同一个 /64"就靠它：/64 的前 64 位正好是前 4 段，比字符串比较稳
// （`240e:400:628:1be::/64`、`240e:400:628:1be::1/64`、带不带 `::` 都能对上）。
func LAN6PrefixHextets(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	// ★ 先滤掉空段：`240e:400:628:1be::1` 直接 split 会得到
	// [240e 400 628 1be "" 1]，取前 4 段就会把 "" 当成一段（真机用例抓出来的）。
	var parts []string
	for _, p := range strings.Split(s, ":") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	if len(parts) > 4 {
		parts = parts[:4]
	}
	return strings.Join(parts, ":")
}

// LAN6PrefixAddrState 纯解析：给 `ip -6 -o addr show dev <lan> scope global` 的行，
// 判断这个前缀的地址**在不在**、在的话**是不是被标了 deprecated**。
//
// 认的行长这样（busybox iproute2 的 -o 输出）：
//
//	7: br-lan    inet6 240e:400:628:1be::1/64 scope global deprecated dynamic \    valid_lft ...
func LAN6PrefixAddrState(lines []string, prefix string) (present, deprecated bool) {
	want := LAN6PrefixHextets(prefix)
	if want == "" {
		return false, false
	}
	for _, line := range lines {
		i := strings.Index(line, "inet6 ")
		if i < 0 {
			continue
		}
		rest := strings.TrimSpace(line[i+len("inet6 "):])
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		if LAN6PrefixHextets(fields[0]) != want {
			continue
		}
		present = true
		for _, f := range fields[1:] {
			if f == "deprecated" {
				deprecated = true
				break
			}
			// 后面是 valid_lft 之类的，没有 deprecated 就说明是正常地址
			if strings.Contains(f, "lft") {
				break
			}
		}
		return present, deprecated
	}
	return false, false
}

// LAN6PrefixAddrFixNeeded 要不要动手（纯判据）：地址不在，或在但是 deprecated。
//
// 前缀判不出来（空串）时**一律不动手** —— 否则会在“还没拿到前缀”的阶段乱改 LAN。
func LAN6PrefixAddrFixNeeded(lines []string, prefix string) bool {
	if LAN6PrefixHextets(prefix) == "" {
		return false
	}
	present, deprecated := LAN6PrefixAddrState(lines, prefix)
	return !present || deprecated
}

// LAN6PrefixHostAddr 前缀 → 我们给 LAN 用的那个网关地址（`<prefix>::1/64`）。
//
// 用 `::1` 是 OpenWrt 的惯例（桥上的第一个地址），跟 netifd 之前加的那条一致，
// 所以"重建"出来的地址跟原来那个是同一个，不会多出一堆地址。
func LAN6PrefixHostAddr(prefix string) string {
	p := strings.TrimSpace(prefix)
	if i := strings.IndexByte(p, '/'); i >= 0 {
		p = p[:i]
	}
	if p == "" || !strings.Contains(p, ":") {
		return ""
	}
	// ★ 拼接陷：`240e:400:628:1be::` 结尾已经是双冒号了，再拼 `::1/64` 会变成
	// 三个冒号（`…1be:::1/64`）—— 真机用例抓出来的。
	if strings.HasSuffix(p, ":") {
		return p + "1/64"
	}
	return p + "::1/64"
}

// LAN6PrefixAddrFixCommands 把 LAN 上这个前缀的地址重建回来（幂等，可每轮跑）。
//
//   - 用 `ip -6 addr replace` 而不是 del+add：**不留空窗**（del 之后如果重建失败，
//     LAN 上就彻底没这个前缀了 —— 我在真机上踩过一次，把路由器自己的 v6 也带断了）；
func LAN6PrefixAddrFixCommands(lan, prefix string) [][]string {
	lan = strings.TrimSpace(lan)
	addr := LAN6PrefixHostAddr(prefix)
	if lan == "" || addr == "" {
		return nil
	}
	return [][]string{
		{"ip", "-6", "addr", "replace", addr, "dev", lan},
	}
}

// LAN6FirstGlobalPrefix 纯解析：从某张网卡的全局 v6 地址里取第一个可用前缀（`xxxx:xxxx:xxxx:xxxx::/64`）。
//
// 跳过 fe80（链路本地）与 fd/ fc（ULA）：UL A 不是运营商给的那个 /64，拿它去配 LAN
// 只会把内网设备带进一条出不了公网的地址。
func LAN6FirstGlobalPrefix(lines []string) string {
	for _, line := range lines {
		i := strings.Index(line, "inet6 ")
		if i < 0 {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(line[i+len("inet6 "):]))
		if len(fields) == 0 {
			continue
		}
		h := LAN6PrefixHextets(fields[0])
		if h == "" {
			continue
		}
		low := strings.ToLower(h)
		if strings.HasPrefix(low, "fe8") || strings.HasPrefix(low, "fe9") ||
			strings.HasPrefix(low, "fea") || strings.HasPrefix(low, "feb") {
			continue // fe80::/10 链路本地
		}
		if strings.HasPrefix(low, "fd") || strings.HasPrefix(low, "fc") {
			continue // ULA
		}
		return h + "::/64"
	}
	return ""
}

// lan6AddrLinesOfDev 读某张网卡上的全局 v6 地址行（`-o` 单行格式，好解析）。
//
// 只读、失败就当"没读到"（下轮再看），绝不因为读不到就动手删/改东西。
func lan6AddrLinesOfDev(dev string) []string {
	dev = strings.TrimSpace(dev)
	if dev == "" {
		return nil
	}
	out, err := exec.Command("ip", "-6", "-o", "addr", "show", "dev", dev, "scope", "global").Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n")
}

// lan6LANIface LAN 桥的名字（默认 br-lan；跟着 UCI 走，不写死）。
func lan6LANIface() string {
	for _, opt := range []string{"network.lan.device", "network.lan.ifname"} {
		if v, err := uciGet(opt); err == nil {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return "br-lan"
}

// lan6AddrFixThrottle 两次修复之间的最短间隔（别因为 replace 没把 deprecated 清掉
// 就每轮都去动一次 —— 15 秒一轮的话日志会很脏）。
const lan6AddrFixThrottle = 60 * time.Second

var (
	lan6AddrFixMu sync.Mutex
	lan6AddrFixAt time.Time
)

// lan6AddrFixAllowed 现在允许动手吗（同一分钟内最多一次）。
func lan6AddrFixAllowed(now time.Time) bool {
	lan6AddrFixMu.Lock()
	defer lan6AddrFixMu.Unlock()
	if !lan6AddrFixAt.IsZero() && now.Sub(lan6AddrFixAt) < lan6AddrFixThrottle {
		return false
	}
	lan6AddrFixAt = now
	return true
}

// lan6PrefixAddrRepairFor **只针对 FM350 这类"主机网卡 + 静态绑定"的机型**
// （用户 2026-10-13 明确要求：这条只针对 FM350）。
//
// 干的事：LAN 上那个 /64 的地址如果不在/被标 deprecated，就把它重建回来。
// 为什么必须管、以及真机取证，见本文件顶部那段注释。
//
// 前置条件都是"读得到才动手"：
//
//	WAN 侧（<dev>）没拿到全局 v6 地址  ⇒ 没前缀可摆，直接返回；
//	LAN 上这个前缀的地址存在且不带 deprecated ⇒ 不用动。
func lan6PrefixAddrRepairFor(mod *Modem, dev string, info *AutoWANInfo) {
	if !modNeedsHostStatic(mod) || !WANIPv6Enabled() {
		return
	}
	prefix := LAN6FirstGlobalPrefix(lan6AddrLinesOfDev(dev))
	if prefix == "" {
		return // WAN 侧还没 v6
	}
	lan := lan6LANIface()
	if lan == "" || lan == strings.TrimSpace(dev) {
		return
	}
	if !LAN6PrefixAddrFixNeeded(lan6AddrLinesOfDev(lan), prefix) {
		return
	}
	if !lan6AddrFixAllowed(time.Now()) {
		return
	}
	if err := runNetProvCmds(LAN6PrefixAddrFixCommands(lan, prefix)); err != nil {
		info.Error = "修 LAN 上的模组 /64 地址失败：" + err.Error()
		return
	}
	id := 0
	if mod != nil {
		id = mod.ID
	}
	logger.Info("auto lan6: repaired LAN prefix address (FM350 静态绑定)",
		zap.Int("modem", id), zap.String("lan", lan), zap.String("prefix", prefix),
		zap.String("wan", dev))
	info.Note += "；已把模组 /64（" + prefix + "）在 " + lan + " 上的地址摆正（内网设备才能拿到公网 v6）"
}
