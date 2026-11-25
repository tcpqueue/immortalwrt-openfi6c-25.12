//go:build !windows

package kernel

// ============ 「主机网卡 + 静态 IP 绑定」这一类模组（FM350 系列）============
//
// 【用户 2026-10-13 口径】FM350 是**静态 IP 绑定**的，**不能走 modem_pro**：
//
//	它的 USB 网卡是 RNDIS，**模组侧不发 DHCP** —— 主机上那张网卡拿不到任何地址，
//	而 netifd 的 dhcp 与我们自己的 modem_pro（v4 同样交给 udhcpc）都只能"等租约"，
//	等不到 ⇒ 接口永远是空的（这正是"没有创建网络接口 / 建了也没地址"的根因）。
//	地址只有一个来源：**问模组要**：
//
//	    AT+CGPADDR=<cid>    → 本机 IPv4（就是主机网卡要用的那个地址**本身**）
//	    AT+CGCONTRDP=<cid>  → <primary_dns> / <secondary_dns>
//
//	再按配方补齐另两项（2026-10-13 真机 FM350-GL 固件 81600.0000.00.29.24.02 实测：
//	`AT+CGPADDR=3` → 10.181.85.121，`AT+CGCONTRDP=3` → 218.2.2.2/218.4.4.4）：
//
//	    掩码 = 255.255.255.0（/24）
//	    网关 = 地址的前三段 + ".1"       ← 实测 10.181.85.121 ⇒ 10.181.85.1
//
// ⚠️ 网关**不是**模组报的地址。hostnic.go 里 SuggestHostNIC() 那套（主机取同网段
// .2、网关 = 模组 IP）是早期按 Windows 脚本**猜**的，对 FM350 是**错的** ——
// 那份只留给"人手填一次"的老面板，新的自动路径一律走本文件。
//
// ⚠️ 判据**同源**：模组侧不发 DHCP ⇔ 主机侧只能静态绑定，所以"要不要静态"直接复用
// hostnic.go 的机型特例表（HostNICQuirk）—— 一张表一处维护，不再抄第二份型号清单。
//
// ⚠️ 地址每次重拨都可能变（运营商 CGNAT 池），所以静态值必须**每轮对齐**；
// 但只有**真的变了**才动接口（HostStaticSame）—— 否则每轮 ifup 会把接口打断。

import (
	"modemd/module/common"
	"modemd/module/config"
	"modemd/module/dial"
	"modemd/module/hostnic"
	"modemd/module/netprov"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// WANProtoHostStatic 静态绑定类模组在 UCI 上用的协议名（netifd 自带的 static）。
//
// 不进 WANProtoNeedsScript / WANProtoPackage 的名单：static 是 netifd 内置的，
// 不需要任何额外脚本或包。
const WANProtoHostStatic = "static"

// HostStaticMask 静态绑定的掩码。配方里固定 /24（实测三个不同 CGNAT 段都是 /24）。
const HostStaticMask = "255.255.255.0"

// HostStaticNeeded 这台模组是不是"主机网卡 + 静态 IP 绑定"那一类。
//
// 判据与"要不要手工配主机网卡"完全同源（hostnic.go 的机型特例表）：模组侧不发
// DHCP ⇔ 主机侧只能静态绑定。目前只有 FM350 系列命中（实测 FM350-GL）。
func HostStaticNeeded(model string) bool {
	need, _ := hostnic.HostNICQuirk(model)
	return need
}

// modNeedsHostStatic 这台模组对象要不要走静态绑定（型号名与识别串都试一遍，
// 与 hostnic_engine.go 的取法一致：不同固件下 ModelName 可能空着）。
func modNeedsHostStatic(mod *Modem) bool {
	if mod == nil {
		return false
	}
	return HostStaticNeeded(mod.ModelName) || HostStaticNeeded(mod.Model)
}

// wanProtoForModemWAN 主接口协议决策：静态绑定类一律 static，其余按 USB 类型 + 模式
// （原逻辑 wanProtoForKindMode 一行没改，见 netprov_proto.go）。
//
// 为什么单独包一层而不是往 wanProtoForKindMode 里塞：那个函数是**纯类型决策**
// （kind + 模式），而"要不要静态"看的是**型号**，两者维度不同；拆开之后
// 原来的决策表与它的单测完全不受影响。
func wanProtoForModemWAN(mod *Modem, kind string, modemProtoOK bool) string {
	if modNeedsHostStatic(mod) {
		return WANProtoHostStatic
	}
	return netprov.WanProtoForKindMode(kind, config.WANModeNow(), modemProtoOK, config.Cfg.Net.HostDial)
}

// HostStaticGateway 地址 → 网关（前三段 + ".1"）。不是 IPv4 就返回空串（调用方据此放弃）。
func HostStaticGateway(ip string) string {
	ip = strings.TrimSpace(strings.Split(strings.TrimSpace(ip), "/")[0])
	if !LooksLikeIP(ip) || strings.Contains(ip, ":") {
		return ""
	}
	i := strings.LastIndex(ip, ".")
	if i <= 0 {
		return ""
	}
	return ip[:i] + ".1"
}

// CleanHostStaticDNS 归一化 DNS 列表：去引号/空白、只留 IPv4、去重、丢掉 0.0.0.0。
//
// 为什么只留 IPv4：静态接口上我们只配 IPv4 地址，塞个 IPv6 DNS 进去 dnsmasq
// 会去问一个根本不存在的 v6 出口（实测 FM350 的 CGCONTRDP 第二行就是 v6 DNS 的
// 十进制怪写法 `36.14.0.90.0.0.…`，绝不能当成 IPv4 用）。
func CleanHostStaticDNS(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), `"`))
		if s == "" || strings.Contains(s, ":") || !LooksLikeIP(s) {
			continue
		}
		if s == "0.0.0.0" || s == "255.255.255.255" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

// HostStaticDNSFromCGCONTRDP 从 `AT+CGCONTRDP` 的应答里取 DNS。
//
// **按字段位取**而不是正则：3GPP 把位置定死了 ——
//
//	0=cid 1=bearer 2=APN 3=local_addr 4=gw_addr 5=primary_dns 6=secondary_dns
//
// 于是三种写法一把过：
//
//	FM350： `+CGCONTRDP: 3,,"ctnet","","","218.2.2.2","218.4.4.4",…`      → 5/6 ✔
//	标准：  `+CGCONTRDP: 1,6,ctnet,10.194.190.206.255.0.0.0,,218.2.2.2,218.4.4.4`
//	           （local_addr 是"地址+掩码"拼成一格，一样不影响 5/6）
//
// 取不到就返回 nil（调用方退回"只有 IP、没有 DNS"）。
func HostStaticDNSFromCGCONTRDP(lines []string) []string {
	for _, line := range lines {
		i := strings.Index(strings.ToUpper(line), "+CGCONTRDP:")
		if i < 0 {
			continue
		}
		f := common.SplitATFields(line[i+len("+CGCONTRDP:"):])
		if len(f) < 7 {
			continue
		}
		if dns := CleanHostStaticDNS([]string{f[5], f[6]}); len(dns) > 0 {
			return dns
		}
	}
	return nil
}

// HostStaticFirstV4 从 `AT+CGPADDR=<cid>` 的应答里取第一个可用的 IPv4。
//
// 一行应答里有三样东西：cid（裸数字）、IPv4、以及 IPv6 的**十进制怪写法**
// （`"0.0.0.0.0.0.0.0.24.222.22.200.92.213.69.99"`，FM350 实测）。
// 只认"正好四段、不是 0 开头、不含冒号"的那个，怪写法自动被排掉。
func HostStaticFirstV4(lines []string) string {
	for _, line := range lines {
		for _, f := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ',' || r == '"' || r == ' ' || r == '\t' || r == '\r'
		}) {
			if !LooksLikeIP(f) || strings.Contains(f, ":") || strings.HasPrefix(f, "0.") {
				continue
			}
			return f
		}
	}
	return ""
}

// QueryPDPAddrAny 挨个 cid 问 `AT+CGPADDR=<cid>`，返回第一个拿到 IPv4 的 cid。
//
// 为什么要挨个问：**cid 不是固定的** —— Modem-Pro 的机型特例表把 FM350 的地址
// 指令钉成 `AT+CGPADDR=2`，而 2026-10-13 这台真机上网用的是 **cid 3**
// （`AT+CGCONTRDP=3` 才回 ctnet）。只认一个 cid 的话，型号对上、地址却拿不到，
// 静态接口就永远建不出来。cids 传 nil 时按"拨号状态里的 cid → 1..4"试。
func (m *Modem) QueryPDPAddrAny(cids []int) (ip string, cid int, ok bool) {
	if m == nil {
		return "", 0, false
	}
	if len(cids) == 0 {
		if c := m.DialStatus().CID; c > 0 {
			cids = append(cids, c)
		}
		cids = append(cids, 1, 2, 3, 4)
	}
	seen := map[int]bool{}
	for _, c := range cids {
		if c <= 0 || seen[c] {
			continue
		}
		seen[c] = true
		resp, err := m.SendAT("AT+CGPADDR=" + strconv.Itoa(c))
		if err != nil || len(resp) == 0 {
			continue
		}
		if v4 := HostStaticFirstV4(resp); v4 != "" {
			return v4, c, true
		}
	}
	return "", 0, false
}

// QueryPDPDNSFor 问一次模组：某个 cid 的数据承载下发了哪些 DNS（`AT+CGCONTRDP=<cid>`）。
//
// 只给静态绑定类机型用（别的机型的 DNS 由 DHCP/RA/拨号器给，没必要多花一条 AT）。
func (m *Modem) QueryPDPDNSFor(cid int) []string {
	if m == nil {
		return nil
	}
	if cid <= 0 {
		cid = m.DialStatus().CID
	}
	if cid <= 0 {
		return nil
	}
	resp, err := m.SendAT("AT+CGCONTRDP=" + strconv.Itoa(cid))
	if err != nil || len(resp) == 0 {
		return nil
	}
	return HostStaticDNSFromCGCONTRDP(resp)
}

// HostStaticPlanFor 用模组报的地址 + DNS 组装一份静态计划（掩码固定 /24）。
// 地址不可用（空 / 不是 IPv4 / IPv6）时返回 false —— 静态绑定没有地址就**不能建接口**，
// 建了 netifd 也是 pending，页面上只是多一张空接口。
func HostStaticPlanFor(iface, ip string, dns []string) (hostnic.HostNICPlan, bool) {
	ip = strings.TrimSpace(strings.Split(strings.TrimSpace(ip), "/")[0])
	if ip == "" || strings.Contains(ip, ":") || !LooksLikeIP(ip) {
		return hostnic.HostNICPlan{}, false
	}
	gw := HostStaticGateway(ip)
	if gw == "" {
		return hostnic.HostNICPlan{}, false
	}
	return hostnic.NormalizeHostNICPlan(hostnic.HostNICPlan{
		Interface: strings.TrimSpace(iface),
		IP:        ip,
		Mask:      HostStaticMask,
		Gateway:   gw,
		DNS:       CleanHostStaticDNS(dns),
	}), true
}

// HostStaticPlanFromDial 组装计划：**现场挨个 cid 去问**，拨号用的那个 cid 排在第一位。
//
// ⚠️ 为什么不用 st.IPv4（真机踩到的坑）：它走的是机型特例表的"本机地址指令"，而 FM350 被
// 钉成 `AT+CGPADDR=2` —— 实测线上这台 FM350 的 **cid 2 是残留的旧 PDP 上下文**
// （`10.177.251.79`），cid 3 才是当前上网的（`10.170.14.193`）。照 st.IPv4 写 UCI 的后果：
//
//	① 接口拿到一个**不通**的地址（v4 直接断）；
//	② 更阴的是它会**每轮把正确的值覆盖回去** —— 手工把 UCI 改对了，下一个轮次又被写回旧地址。
//
// 所以静态路径一律现问，并把 `3` 排在 1/2/4 前面（FM350 实测上网就是 cid 3）。
func HostStaticPlanFromDial(iface string, st *dial.DialState, mod *Modem) (hostnic.HostNICPlan, bool) {
	var ip string
	cid := 0
	cids := []int{}
	if st != nil && st.CID > 0 {
		cids = append(cids, st.CID)
	}
	cids = append(cids, 3, 1, 2, 4)
	if mod != nil {
		if v, c, ok := mod.QueryPDPAddrAny(cids); ok {
			ip, cid = v, c
		}
	}
	if !hostStaticV4(ip) {
		return hostnic.HostNICPlan{}, false
	}
	var dns []string
	if mod != nil {
		dns = mod.QueryPDPDNSFor(cid)
	}
	return HostStaticPlanFor(iface, ip, dns)
}

// hostStaticV4 是不是能直接当静态地址用的 IPv4（空 / IPv6 / 0.x.x.x 都不行）。
func hostStaticV4(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && !strings.Contains(s, ":") && LooksLikeIP(s) && !strings.HasPrefix(s, "0.")
}

// HostStaticSame 现在写在接口上的静态值，跟模组刚报的一致吗？
//
// 一致就**什么都不做**：地址是每轮都去问的，而 ifup 会打断接口（几十毫秒到几秒的
// 断流）。DNS 比的是**集合**（顺序无关）：UCI 里 list 的顺序可能被 LuCI 改过。
func HostStaticSame(cur, want hostnic.HostNICPlan) bool {
	if strings.TrimSpace(cur.IP) != strings.TrimSpace(want.IP) {
		return false
	}
	if strings.TrimSpace(cur.Gateway) != strings.TrimSpace(want.Gateway) {
		return false
	}
	if m := strings.TrimSpace(want.Mask); m != "" && strings.TrimSpace(cur.Mask) != m {
		return false
	}
	a := CleanHostStaticDNS(cur.DNS)
	b := CleanHostStaticDNS(want.DNS)
	if len(a) != len(b) {
		return false
	}
	has := map[string]bool{}
	for _, x := range a {
		has[x] = true
	}
	for _, x := range b {
		if !has[x] {
			return false
		}
	}
	return true
}

// HostStaticCommands 把接口就地写成静态（可反复执行，幂等）。
//
// 用 `uci set` 直接写 network.<iface>：**节不存在时会新建**，所以建接口与改接口
// 是同一套命令，不用分两路（也就不存在"建的时候漏了网关"这种半边配置）。
//
// 顺序有意为之：先 proto/device，再地址，最后 DNS（list，必须先 delete 再 add，
// 否则旧运营商的 DNS 会跟新的一起留着，解析时会先问那个不通的）。
//
// curOpts = 该接口当前已有的选项名（见 netprov_engine.go 的 uciOptionNames），
// 用来清掉历史遗留的控制口选项（原因见 staleControlDeviceOptions）。
func HostStaticCommands(iface, dev string, p hostnic.HostNICPlan, curOpts []string) [][]string {
	iface = strings.TrimSpace(iface)
	p = hostnic.NormalizeHostNICPlan(p)
	cmds := [][]string{
		{"uci", "set", "network." + iface + ".proto=" + WANProtoHostStatic},
	}
	if dev = strings.TrimSpace(dev); dev != "" {
		cmds = append(cmds,
			[]string{"uci", "set", "network." + iface + ".device=" + dev},
			[]string{"uci", "set", "network." + iface + ".ifname=" + dev},
		)
	}
	if p.IP != "" {
		cmds = append(cmds, []string{"uci", "set", "network." + iface + ".ipaddr=" + p.IP})
	}
	mask := strings.TrimSpace(p.Mask)
	if mask == "" {
		mask = HostStaticMask
	}
	cmds = append(cmds, []string{"uci", "set", "network." + iface + ".netmask=" + mask})
	if p.Gateway != "" {
		cmds = append(cmds, []string{"uci", "set", "network." + iface + ".gateway=" + p.Gateway})
	}
	cmds = append(cmds, []string{"uci", "-q", "delete", "network." + iface + ".dns"})
	for _, d := range CleanHostStaticDNS(p.DNS) {
		cmds = append(cmds, []string{"uci", "add_list", "network." + iface + ".dns=" + d})
	}
	// force_link：IPv6 拉起时要 down/up 一次链路（发 RS），RNDIS 网卡会短暂掉载波。
	// 不打开 force_link 的话 netifd 会顺势把接口拉下来 —— 连带 IPv4 一起断。
	cmds = append(cmds, []string{"uci", "set", "network." + iface + ".force_link=1"})
	// static 不吃"控制口"那套选项，留着会在 LuCI 里挂一堆没用的格子
	for _, opt := range staleControlDeviceOptions(curOpts) {
		cmds = append(cmds, []string{"uci", "delete", "network." + iface + "." + opt})
	}
	cmds = append(cmds, []string{"uci", "commit", "network"}, []string{"ifup", iface})
	return cmds
}

// hostStaticOrSwitchCommands “切协议”命令：静态绑定类要一次把 ipaddr/netmask/gateway/dns
// 都带上（static 没地址 = netifd 把它挂成 pending），其余走原来的 SwitchProtoCommands。
//
// 万一地址还没拿到（接口刚建、模组刚重启），退回原来的命令 —— 至少 proto 先切过去，
// 下一轮拿到地址时再补全，不会原地卡住。
func hostStaticOrSwitchCommands(mod *Modem, hostStatic bool, iface, wantProto, dev string, curOpts []string, st *dial.DialState) [][]string {
	if hostStatic {
		if p, ok := HostStaticPlanFromDial(iface, st, mod); ok {
			return HostStaticCommands(iface, dev, p, curOpts)
		}
	}
	return SwitchProtoCommands(iface, wantProto, dev, curOpts)
}

// HostStaticV6Gateway 静态绑定类机型的 **IPv6 网关**：模组自己（fe80::1）。
//
// 2026-10-13 真机实测（FM350-GL / RNDIS，用户问"为什么这张 IPv6 不行"）：
//
//	· 网卡上**收得到 RA** ⇒ 地址是 SLAAC 来的：`240e:400:628:1be:200:11ff:fe12:1314/64
//	  proto kernel_ra`（前缀运营商给 /64，接口标识是 MAC 的 EUI-64）；
//	· 但那个 RA **不带 R 标志**（`accept_ra_defrtr=1` 也一样）⇒ 内核不装默认路由，
//	  于是"有地址却出不去"（ping6 全部 Network unreachable）；
//	· 手工 `ip -6 route add default via fe80::1 dev eth1` 之后**立刻全通**：
//	  ping6 240e:5a::6666 / 2400:3200::1 都是 0% 丢包（21~35ms）；
//	· fe80::1 自己**不回 ICMPv6**（100% 丢包是正常的）—— 邻居表里它的 MAC 是
//	  `00:00:88:ff:00:00`（模组），跟 IPv4 那个 `.1` 网关一样不响应探测，但路由有效。
//
// 所以 v6 跟 v4 是**同一个套路**：地址让模组给（v4 = AT+CGPADDR，v6 = RA），
// 默认路由指向"链路上的 .1"（v4 = x.y.z.1，v6 = fe80::1）。
const HostStaticV6Gateway = "fe80::1"

// HostStaticV6BringUpNeeded 现在要不要做一次 IPv6 拉起（纯判据，便于单测）。
//
// 只在"开了 IPv6 但网卡上还没全局 v6 地址"时动手 —— 拿到地址就别再碰，
// 否则每轮的 ifdown/ifup 会把刚通的链路反复打断。地址过期/被清掉时会自动再来一次。
func HostStaticV6BringUpNeeded(wanV6On, hasGlobalV6 bool) bool {
	return wanV6On && !hasGlobalV6
}

// HostStaticV6SysctlCommands 让内核收 RA。
//
// **转发开着时 accept_ra 必须 = 2**：OpenWrt 路由器上
// `net.ipv6.conf.all.forwarding=1`，此时 accept_ra=1 语义是"什么都不做"，
// 这正是"参数看着都对、一个 RA 都收不到"的原因（实测白等 25 秒）。
func HostStaticV6SysctlCommands(dev string) [][]string {
	dev = strings.TrimSpace(dev)
	if dev == "" {
		return nil
	}
	return [][]string{
		{"sysctl", "-w", "net.ipv6.conf." + dev + ".disable_ipv6=0"},
		{"sysctl", "-w", "net.ipv6.conf." + dev + ".accept_ra=2"},
		{"sysctl", "-w", "net.ipv6.conf." + dev + ".autoconf=1"},
	}
}

// HostStaticV6LinkCycleCommands 把链路重起一次。
//
// 为什么必须重起：内核是在**链路 up 时发 RS**（Router Solicitation）才换回 RA 的；
// 参数设好了但链路一直 up，就永远不会重新 Solicitation。
// 实测对照：不重起链路 → 25 秒 0 个 RA、一个 inet6 都没有；
//
//	`ip link set down; up` 之后 → **10 秒内**就拿到 240e:.../64。
//
// ifdown/ifup 一起上，是为了让 netifd 也认这次状态变化（否则它会按载波丢失处理）。
func HostStaticV6LinkCycleCommands(iface, dev string) [][]string {
	dev = strings.TrimSpace(dev)
	if dev == "" {
		return nil
	}
	iface = strings.TrimSpace(iface)
	var cmds [][]string
	if iface != "" {
		cmds = append(cmds, []string{"ifdown", iface})
	}
	cmds = append(cmds,
		[]string{"ip", "link", "set", "dev", dev, "down"},
		[]string{"ip", "link", "set", "dev", dev, "up"},
	)
	if iface != "" {
		cmds = append(cmds, []string{"ifup", iface})
	}
	return cmds
}

// HostStaticV6RouteCommands 把 v6 默认路由写进 UCI（route6 节）。
//
// 为什么不直接 `ip -6 route add`：netifd 每次重配这张接口都会清掉"自己名下"的路由，
// 手工加的那条会被抹掉；写成 UCI 的 route6 才会被 netifd 一直维持（重启也在）。
// 节名用 `<iface>_v6`，跟我们 modem<id> / modem<id>_6 的命名习惯一致。
func HostStaticV6RouteCommands(iface string) [][]string {
	iface = strings.TrimSpace(iface)
	if iface == "" {
		return nil
	}
	sec := iface + "_v6"
	return [][]string{
		{"uci", "set", "network." + sec + "=route6"},
		{"uci", "set", "network." + sec + ".interface=" + iface},
		{"uci", "set", "network." + sec + ".target=::/0"},
		{"uci", "set", "network." + sec + ".gateway=" + HostStaticV6Gateway},
		{"uci", "commit", "network"},
	}
}

// hostStaticV6Ensure 静态绑定类机型的 IPv6 拉起（幂等；失败只写 Note/Error，绝不影响 v4）。
func hostStaticV6Ensure(mod *Modem, iface, dev string, st *dial.DialState, info *AutoWANInfo) {
	if info == nil || !HostStaticV6BringUpNeeded(WANIPv6Enabled(), netdevHasGlobalIPv6(dev)) {
		return
	}
	if err := runNetProvCmds(HostStaticV6SysctlCommands(dev)); err != nil {
		info.Error = "IPv6：开 accept_ra 失败：" + err.Error()
		return
	}
	if err := runNetProvCmds(HostStaticV6LinkCycleCommands(iface, dev)); err != nil {
		info.Error = "IPv6：重起链路发 RS 失败：" + err.Error()
		return
	}
	// 再设一次 accept_ra：重起链路（尤其 ifdown/ifup）有可能把它重置掉，那时内核**不处理 RA**
	// ⇒ 永远等不到 SLAAC 地址（刷机后第一次开机就踩到过：accept_ra 又变成 0、eth1 上没 v6 地址）。
	if err := runNetProvCmds(HostStaticV6SysctlCommands(dev)); err != nil {
		info.Error = "IPv6：重起链路后补设 accept_ra 失败：" + err.Error()
		return
	}
	if err := runNetProvCmds(HostStaticV6RouteCommands(iface)); err != nil {
		info.Error = "IPv6：写默认路由（" + HostStaticV6Gateway + "）失败：" + err.Error()
		return
	}
	id := 0
	if mod != nil {
		id = mod.ID
	}
	logger.Info("auto wan: host-static IPv6 brought up (RA + default via "+HostStaticV6Gateway+")",
		zap.Int("modem", id), zap.String("iface", iface), zap.String("dev", dev))
	// v4 兜底：上面重起链路是为了发 RS，但**链路 down 会把 IPv4 默认路由清掉**
	// （实测：地址留着、默认路由没了 ⇒ "v6 通了 v4 反倒 Network unreachable"）。
	// 所以这里按 UCI 再写一遍 v4，保证两边同时在。
	if p, ok := HostStaticPlanFromDial(iface, st, mod); ok {
		if err := runNetProvCmds(HostStaticCommands(iface, dev, p, nil)); err != nil {
			info.Error = "IPv6 拉起后补写 v4 失败：" + err.Error()
			return
		}
		info.Note += "（已补回 v4 " + p.IP + "）"
	}
	info.Note += "；IPv6：等 RA 拿地址，默认路由走 " + HostStaticV6Gateway
}

// HostStaticCurrentPlan 现在接口上写着的静态值（刷新前比对用）。
func HostStaticCurrentPlan(iface string) hostnic.HostNICPlan {
	p := hostnic.HostNICPlan{Interface: strings.TrimSpace(iface)}
	get := func(opt string) string {
		v, err := uciGet("network." + p.Interface + "." + opt)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(v)
	}
	p.IP = get("ipaddr")
	p.Mask = get("netmask")
	p.Gateway = get("gateway")
	if v := get("dns"); v != "" {
		p.DNS = strings.Fields(v)
	}
	return p
}
