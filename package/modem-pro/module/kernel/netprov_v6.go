package kernel

import (
	"net"
	"sort"
	"strconv"
	"strings"
)

// GlobalIPv6PrefixOf 从网卡上的一条全局 IPv6 地址算出它所在的**前缀**（交给内网用）。
//
// "240e:400:600:16e8:9e54:40ff:fe01:c5c0/64" → "240e:400:600:16e8::/64"
//
// ⚠️ 为什么要算这个：模组只给链路上的一个 **/64**（随 RA/SLAAC），**不做前缀委派（PD）**，
// 所以 netifd 不知道该把哪个前缀发给内网（`ipv6-prefix` 是空的，odhcpd 也会说
// "no public prefix on lan"），内网设备就永远只有 ULA。
// 在上联接口上写 `list ip6prefix '<前缀>'` 就解决了 —— 实测内网立刻拿到全局地址。
//
// 丢掉 link-local（fe80::/10）与私有地址（fc00::/7 ULA）—— 内网自己的 ULA 由 odhcpd 发，
// 不该拿它当"公网前缀"。
func GlobalIPv6PrefixOf(addr string) (string, bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", false
	}
	_, ipnet, err := net.ParseCIDR(addr)
	if err != nil || ipnet == nil || ipnet.IP.To4() != nil {
		return "", false
	}
	if ipnet.IP.IsLinkLocalUnicast() || ipnet.IP.IsPrivate() {
		return "", false
	}
	// 委派一个 /128 或 /127 给内网没意义（分不出来）
	if ones, bits := ipnet.Mask.Size(); bits == 0 || ones > 120 {
		return "", false
	}
	return ipnet.String(), true
}

// parseIfInet6Flags 解析 /proc/net/if_inet6，返回 **规范化后的地址字符串 → 内核标志位**。
//
// 那个文件每行长这样（内核 addrconf.c 的 if6_seq_show）：
//
//	<32 位十六进制地址> <ifindex> <prefixlen> <scope> <flags %02x> <网卡名>
//
// ⚠️ 只用它取一件事：**这个地址是不是被内核标成 deprecated**（IFA_F_DEPRECATED = 0x20）——
// 模组重拨换 /64 后，旧地址不会马上消失（SLAAC 生命期还在），但会被标成 deprecated，
// 那时它已经不该再拿去做新连接了。地址要做同一个口径（走 net.IP.String()）才好对。
// 文件读不到/行不对 ⇒ 返回空表（调用方当成"都可用"，保守不动）。
func parseIfInet6Flags(text string) map[string]uint32 {
	out := map[string]uint32{}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 6 {
			continue
		}
		ip := parseHexIPv6(f[0])
		if ip == "" {
			continue
		}
		var flags uint32
		v, err := strconv.ParseUint(f[4], 16, 32)
		if err != nil {
			continue // 格式看不懂就不记（宁可当成"没被废弃"）
		}
		flags = uint32(v)
		out[ip] = flags
	}
	return out
}

// ifaFlagDeprecated IFA_F_DEPRECATED（Linux uapi/linux/if_addr.h）
const ifaFlagDeprecated = 0x20

// parseHexIPv6 把 /proc/net/if_inet6 里的 32 位十六进制地址转成常规写法（转不出来返回空串）
func parseHexIPv6(hexAddr string) string {
	hexAddr = strings.TrimSpace(hexAddr)
	if len(hexAddr) != 32 {
		return ""
	}
	b := make([]byte, 0, 16)
	for i := 0; i < 32; i += 2 {
		v, err := strconv.ParseUint(hexAddr[i:i+2], 16, 8)
		if err != nil {
			return ""
		}
		b = append(b, byte(v))
	}
	if ip := net.IP(b); ip.To4() == nil {
		return ip.String()
	}
	return ""
}

// IPv6AddrDeprecated addr（可以是 "240e:…::c5c0/64"）在内核里是不是被标成 deprecated。
// 查不到（proc 里没有/解析失败）一律当作**没被废弃** —— 保守：宁可不动前缀，也别无谓地改。
func IPv6AddrDeprecated(flags map[string]uint32, addr string) bool {
	if len(flags) == 0 {
		return false
	}
	s := strings.TrimSpace(addr)
	if i := strings.IndexByte(s, '%'); i >= 0 {
		s = s[:i] // 去掉 zone
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	f, ok := flags[ip.String()]
	return ok && f&ifaFlagDeprecated != 0
}

// pickLANIPv6Prefix 决定"交给内网"的前缀（纯逻辑，好单测）：
//
//	① 现有配置 cur 还在**可用**集合里 ⇒ 保持不动（返回 cur）。这是防抖动的关键：
//	   模组重拨后旧 /64 的地址还会在链上留一会儿，而地址枚举顺序本来也不保证稳定，
//	   "取第一个地址"会让前缀在新旧之间来回跳 —— 每跳一次就要 `ifup modem0_6` 一次，
//	   用户看到的就是**IPv6 一会儿好、一会儿不行**（2026-10-07 实测）。
//	② cur 已经不在了/被内核标成 deprecated ⇒ 从可用集合里挑（排序后第一个，保证
//	   同一组地址永远得到同一个选择，不会来回改）。
//	③ 可用集合为空（读不到 deprecated 信息 / 全部 deprecated）⇒ 退回全部里排序第一个；
//	   但若 cur 还在"全部"里，则**不动**（拿不到废弃信息时不冒险）。
func pickLANIPv6Prefix(all, live []string, cur string) string {
	cur = strings.TrimSpace(cur)
	liveU := sortUniquePrefixes(live)
	if cur != "" && containsPrefix(liveU, cur) {
		return cur
	}
	if len(liveU) > 0 {
		return liveU[0]
	}
	allU := sortUniquePrefixes(all)
	if cur != "" && containsPrefix(allU, cur) {
		return cur
	}
	if len(allU) > 0 {
		return allU[0]
	}
	return ""
}

// sortUniquePrefixes 去掉空串/重复项并排序（顺序稳定 ⇒ 同一组地址总是同一个选择）
func sortUniquePrefixes(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// containsPrefix 集合里有没有它
func containsPrefix(list []string, want string) bool {
	for _, p := range list {
		if p == want {
			return true
		}
	}
	return false
}

// WAN6PrefixCommands 给已存在的 v6 接口补上 ip6prefix（把模组的前缀交给内网）。
//
// ⚠️ 位置很关键：必须加在**上联接口**（modem0_6）上。加在 LAN 上时 netifd 会当成
// "要往下发的前缀"并把本地路由黑洞掉，odhcpd 反而报 "no public prefix on lan"（实测）。
func WAN6PrefixCommands(iface, prefix string) [][]string {
	return [][]string{
		{"uci", "add_list", "network." + iface + ".ip6prefix=" + prefix},
		{"uci", "commit", "network"},
		{"ifup", iface},
	}
}

// LAN6RouteSection 我们维护的那个 route6 段名（UCI 命名段 ⇒ 幂等，删除也干净）
const LAN6RouteSection = "modemd_lan6route"

// lan6Item 一台模组 v6 接口的候选信息（纯数据，便于单测）
type lan6Item struct {
	Iface      string // 模组 v6 接口名：模式 1 是 modem<id>，模式 2/3 是 modem<id>_6（见 lan6WantNow）
	Prefix     string // 该接口上"现在在用"的全局 /64（空 = 还没拨上/没有）
	Designated bool   // 这台模组是不是"要专门处理"的型号（见 wan6NonQMIProfileFor）
}
