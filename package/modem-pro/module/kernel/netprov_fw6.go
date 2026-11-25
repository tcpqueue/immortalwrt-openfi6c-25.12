//go:build !windows

package kernel

import (
	"context"
	"modemd/module/common"
	"modemd/module/config"
	"modemd/module/netprov"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// hostV6Watch 每台模组的"主机侧 IPv6 没跟上模组"看门狗状态
// （为什么需要它，见 netprov.go 的 HostV6StaleAfter 那段注释）
type hostV6Watch struct {
	badSince time.Time // 从什么时候开始对不上（对得上就清空）
	lastTry  time.Time // 上次弹 PDP 的时间
	tries    int       // 连续弹了几次
	warned   bool      // 放弃之后只报一次
}

var (
	hostV6Mu   sync.Mutex
	hostV6ByID = map[int]*hostV6Watch{}
)

// dialInProgress 现在有没有正在跑的拨号流程（它连发好几条 AT，不能跟它抢）
func dialInProgress(mod *Modem) bool {
	mod.dialMu.Lock()
	defer mod.dialMu.Unlock()
	return mod.dialBusy
}

// syncHostIPv6Prefix 每轮对账：模组换了 /64 但**主机没跟上** ⇒ 弹一下承载 v6 的 PDP，
// 让模组重新向主机做一次 RA（`renew` / `ifup` 都催不出来，实测见 netprov.go 那段注释）。
func syncHostIPv6Prefix(mgr *ModemManager) {
	if !WANIPv6Enabled() || mgr == nil {
		return
	}
	list, ok := managedInterfacesNowOK()
	if !ok {
		return
	}
	live := map[int]*Modem{}
	for _, mod := range mgr.listModems() {
		if mod != nil && mod.isRunningNow() {
			live[mod.ID] = mod
		}
	}
	now := time.Now()
	for _, it := range list {
		if !strings.HasSuffix(it.Name, "_6") || strings.TrimSpace(it.Device) == "" {
			continue
		}
		mod := live[it.ModemID]
		if mod == nil {
			continue // 模组不在了
		}
		syncHostIPv6For(mod, it.Device, now)
	}
}

// syncHostIPv6For 单台模组的看门狗：主机侧前缀 vs 模组侧那个带 v6 的上下文的前缀
func syncHostIPv6For(mod *Modem, dev string, now time.Time) {
	cmd := mod.resolveVendorCmd(KindPDPAddr)
	if cmd == "" {
		return // 该机型没有可用的地址查询指令
	}
	resp, err := mod.SendAT(cmd)
	if err != nil || common.RespHasError(resp) {
		return
	}
	st := mod.DialStatus()
	cid, want := ModemV6CID(resp, st.CID)
	host := globalPrefixOf(dev)

	hostV6Mu.Lock()
	w := hostV6ByID[mod.ID]
	if w == nil {
		w = &hostV6Watch{}
		hostV6ByID[mod.ID] = w
	}
	// 模组侧没 v6（可能运营商没给 / 用户关了 v6）/ 已经对上了 ⇒ 清零
	if want == "" || want == host {
		w.badSince, w.lastTry, w.tries, w.warned = time.Time{}, time.Time{}, 0, false
		hostV6Mu.Unlock()
		return
	}
	if w.badSince.IsZero() {
		w.badSince = now
	}
	staleFor := now.Sub(w.badSince)
	sinceTry := HostV6RetryAfter // 没弹过 ⇒ 当作"早就过了冷却"
	if !w.lastTry.IsZero() {
		sinceTry = now.Sub(w.lastTry)
	}
	act := HostV6FollowUp(staleFor, sinceTry, w.tries)
	switch act {
	case HostV6Bounce:
		w.lastTry, w.tries, w.warned = now, w.tries+1, false
	case HostV6GiveUp:
		if w.warned {
			act = HostV6Nothing
		} else {
			w.warned = true
		}
	}
	tries := w.tries
	hostV6Mu.Unlock()

	switch act {
	case HostV6Bounce:
		if dialInProgress(mod) {
			return // 拨号中，下一轮再说
		}
		logger.Info("auto wan6: host ipv6 stuck on the old prefix, bouncing the pdp to force a new RA",
			zap.Int("modem", mod.ID), zap.Int("cid", cid),
			zap.String("modem_prefix", want), zap.String("host_prefix", host), zap.Int("try", tries))
		if _, err := mod.SetPDPActive(cid, true, true); err != nil {
			logger.Warn("auto wan6: pdp bounce failed", zap.Int("cid", cid), zap.String("err", err.Error()))
		}
	case HostV6GiveUp:
		logger.Warn("auto wan6: host ipv6 还挂在旧前缀上，已连续弹过几次 PDP —— 请在页面上重拨一次或重启模组",
			zap.Int("modem", mod.ID), zap.Int("cid", cid),
			zap.String("modem_prefix", want), zap.String("host_prefix", host), zap.Int("tries", tries))
	}
}

// firewallZoneNote 只读检查：这张接口在不在配置的防火墙区里。
// 在（或读不到配置）就返回空串；不在就返回一句**给人看**的话（页面上会显示）。
//
// ⚠️ 现在 modemd 会**自己把它加进去**（`syncFirewallZone`，记住哪些是自己加的、只收回自己的），
// 所以这句话只在"加不进去 / 区根本不存在"时才该出现；正常情况会在日志里先看到
// `auto wan: modem interfaces added to firewall zone`。
func firewallZoneNote(zone, iface string) string {
	if strings.TrimSpace(zone) == "" {
		zone = "wan"
	}
	b, err := os.ReadFile(firewallCfgPath)
	if err != nil {
		return "（读不到 " + firewallCfgPath + "，没法确认接口在不在防火墙 " + zone + " 区里）"
	}
	sec, _, ok := FindZone(ParseUCIConfig(string(b)), zone)
	if !ok {
		return "防火墙里没有名为 " + zone + " 的区 —— 这张接口不在任何区里，局域网用不了它" +
			"（要局域网能用它，请到 LuCI「防火墙 → 区域」自己划一下）"
	}
	if ZoneHasNetwork(sec, iface) {
		return ""
	}
	return "接口不在防火墙 " + zone + " 区里 —— 局域网用不了它。modemd 每轮对账会自动把它划进去" +
		"（日志里搜 `firewall zone`）；若一直没加上，请到 LuCI「防火墙 → 区域」把 " + iface + " 自己加进 " + zone + " 区"
}

// syncWAN6CustomRules 把"我们那段规则"同步进 /etc/firewall.user（只碰这个文件），
// 并**只读地**检查这个文件会不会被防火墙 include。
// 返回：changed = 文件真变了（调用方要 reload 防火墙）；warn = 要展示给人的提示；errMsg = 硬错误。
func syncWAN6CustomRules(devs []string, hasIP6tables bool) (changed bool, warn string, errMsg string) {
	devs = netprov.Wan6FirewallDevList(devs)
	cur := readFileText(WAN6CustomRulesPath)
	next, changed := netprov.MergeWAN6CustomRules(cur, netprov.WAN6CustomRulesBlock(devs, hasIP6tables))
	if changed {
		if err := os.WriteFile(WAN6CustomRulesPath, []byte(next), 0o644); err != nil {
			return false, "", "写 " + WAN6CustomRulesPath + " 失败：" + err.Error()
		}
		logger.Info("auto wan6: custom rules updated",
			zap.String("file", WAN6CustomRulesPath), zap.Strings("devices", devs))
	}
	// ⚠️ 只**检查**、不修改 /etc/config/firewall（用户 2026-10-07 要求）。
	// 没有指向它的 include 段 ⇒ 防火墙根本不会执行这文件（规则等于没生效）⇒ 必须让人看见。
	// ⚠️ 但**只有真的写了规则**（devs 非空）时才值得提醒 ——
	// 模式 1（modem_pro 单接口）下这段本来就是空的、没有任何规则要生效，
	// 再刷这条警告纯粹是噪音（实测每 15 秒一次，看着像坏了）。
	if len(devs) > 0 && !firewallUserIncluded() {
		// 只组装文案，**不在这里打日志** —— 去重由调用方（syncWAN6FirewallDevices）做，
		// 否则同一件事每轮会被打两遍。
		warn = "⚠️ /etc/config/firewall 里没有指向 " + WAN6CustomRulesPath +
			" 的 include 段 —— 防火墙不会执行它（规则等于没生效）；请在 LuCI「防火墙 → 自定义规则」保存一次，" +
			"或手动加一段 config include"
	}
	return changed, warn, ""
}

// firewallUserIncluded /etc/config/firewall 里有没有指向 /etc/firewall.user 的 include 段（**只读**）
func firewallUserIncluded() bool {
	b, err := os.ReadFile(firewallCfgPath)
	if err != nil {
		return false
	}
	_, _, ok := FindIncludeByPath(ParseUCIConfig(string(b)), WAN6CustomRulesPath)
	return ok
}

// readFileText 读文本文件；读不到（比如 /etc/firewall.user 本来就没建）返回空串
func readFileText(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// removeWAN6CustomRules 把我们那段从 /etc/firewall.user 里删掉（块外用户写的命令一个字不动）。
// ⚠️ 文件本身**不删**：fw4 对不存在的 include 路径会告警（"Include … failed"），留个空文件最干净。
func removeWAN6CustomRules() bool {
	cur := readFileText(WAN6CustomRulesPath)
	next, changed := netprov.MergeWAN6CustomRules(cur, "")
	if !changed {
		return false
	}
	if err := os.WriteFile(WAN6CustomRulesPath, []byte(next), 0o644); err != nil {
		logger.Warn("auto wan6: remove custom rules failed",
			zap.String("file", WAN6CustomRulesPath), zap.Error(err))
		return false
	}
	logger.Info("auto wan6: custom rules removed", zap.String("file", WAN6CustomRulesPath))
	return true
}

// dropLegacyWAN6OwnInclude 清掉老版本（≤ 0.2.220）那套自建的防火墙挂载：
// `firewall.modemd_wan6` include 段 + /etc/modemd/wan6-firewall.sh + /etc/nftables.d/99-modemd-wan6.nft。
// 现在只维护 /etc/firewall.user 里那一段，这几样留着只会互相打架（同一条规则被加两三遍）。
func dropLegacyWAN6OwnInclude() bool {
	removed := false
	if cur, err := uciGet("firewall.modemd_wan6.path"); err == nil && strings.TrimSpace(cur) != "" {
		if err := runNetProvCmds([][]string{
			{"uci", "-q", "delete", "firewall.modemd_wan6"},
			{"uci", "commit", "firewall"},
		}); err == nil {
			removed = true
			logger.Info("auto wan6: legacy include section removed")
		}
	}
	if p := netprov.WAN6FirewallScriptPath(config.ConfigPath); p != "" {
		if _, err := os.Stat(p); err == nil && os.Remove(p) == nil {
			removed = true
			logger.Info("auto wan6: legacy firewall script removed", zap.String("script", p))
		}
	}
	if dropLegacyWAN6NftInclude() {
		removed = true
	}
	return removed
}

// dropLegacyWAN6NftInclude 删掉旧版写的 /etc/nftables.d/99-modemd-wan6.nft（有才删）。
// 返回 true = 真删了（调用方要 reload，免得内核里还留着它那一份）。
func dropLegacyWAN6NftInclude() bool {
	path := filepath.Join("/etc/nftables.d", wan6LegacyNftIncludeName)
	if _, err := os.Stat(path); err != nil {
		return false
	}
	if err := os.Remove(path); err != nil {
		logger.Warn("auto wan6: remove legacy nft include failed", zap.String("file", path), zap.Error(err))
		return false
	}
	logger.Info("auto wan6: legacy nft include removed", zap.String("file", path))
	return true
}

// removeWAN6Firewall 把我们那套收掉：/etc/firewall.user 里那段 + 老版本残留（自建 include 段 / 脚本 /
// nft 文件）。只在"我们建的 v6 兄弟接口一个都不剩"或"自动添加被关掉"时调。返回是否真收掉了东西。
//
// ⚠️ 用户 2026-10-07 明确要求："模块不在 就去掉自定义（规则）里的配置"，而且**不要再自己 exec iptables**。
// 所以这里只改文件 + 触发一次防火墙 reload，让它按新内容生效（fw3 的 reload 会 flush iptables，
// 旧规则自然就没了；fw4 的 reload 只重建它自己那张表，核里旧规则会留到下次重启/防火墙 flush）。
func removeWAN6Firewall() (removed bool) {
	if removeWAN6CustomRules() {
		removed = true
	}
	if dropLegacyWAN6OwnInclude() {
		removed = true
	}
	if removed {
		_ = runNetProvCmd([]string{"/etc/init.d/firewall", "reload"})
		logger.Info("auto wan6: firewall rules removed", zap.String("file", WAN6CustomRulesPath))
	}
	return removed
}

// anyManagedWAN6Iface 还有没有**我们建的** v6 兄弟接口。id < 0 = 不看模组（任意一台都算）。
func anyManagedWAN6Iface(id int) bool {
	list, ok := managedInterfacesNowOK()
	if !ok {
		// ⚠️ 读不到 UCI 时当成"还在用"：宁可这轮不收，也不能因为一次 uci 超时就
		// 把好规则删掉（那正是"一会儿就不行了"的典型成因）。
		logger.Warn("auto wan6: cannot read uci network, keep firewall rules for now")
		return true
	}
	for _, it := range list {
		if strings.HasSuffix(it.Name, "_6") && (id < 0 || it.ModemID != id) {
			return true
		}
	}
	return false
}

// dropWAN6FirewallIfUnused 已经没有**任何受管的模组接口**时，把那套防火墙规则收掉。
//
// 三个调用点：关 IPv6 / 换成 MBIM（disableModemWAN6）、拔模组（sweepStaleAutoWAN /
// cleanupAutoWANForModem）。返回值 = 这次真的收掉了东西（用于页面提示）。
//
// ⚠️ **5G 只是掉了（模组/网卡还在）不会走到这里** —— 接口没被删，规则留着，
// 链路自己恢复后不用重写文件、不用 reload 防火墙。
//
// ★ 2026-10-13：判据从"还有没有 modem<id>_6 接口"改成"还有没有受管的模组接口（任意）"。
// 原因同 wan6FirewallDevsNow：RM500U 的 ECM 在 wan_mode=modem_pro（单接口、没有 `_6`）下
// 同样需要这套规则，按老判据会在删 `_6` 的瞬间把规则一起收掉 ⇒ 用户实测"modem_pro 下 IPv6 不通"。
// 精确对账交给下一轮 syncWAN6FirewallDevices（它按 wan6FirewallDevsNow 算），这里只做粗筛。
//
// ⚠️ 于是 anyManagedWAN6Iface 现在没有调用点了（保留定义：将来若要"只看 v6 兄弟接口"还能用）。
func dropWAN6FirewallIfUnused() bool {
	if !wan6FirewallAuto {
		return false // 自动添加关着的时候一律不动防火墙（否则会替用户删掉他手写的规则）
	}
	if netprov.Wan6FirewallKeepFor(managedInterfacesNow()) {
		return false // 还有模组接口在用 ⇒ 留着（留不留得对由 syncWAN6FirewallDevices 精确对账）
	}
	return removeWAN6Firewall()
}

// ensureLANIPv6Prefix 把模组拿到的全局 IPv6 前缀（如 240e:400:600:16e8::/64）写进**上联接口**的
// `ip6prefix`，这样 netifd 才知道可以把它分配给内网（odhcpd 就会把前缀 RA 给内网设备）。
//
// ⚠️ 只给 **QMI** 的 v6 接口调（2026-10-07 用户要求：非 QMI 那套配置不带 ip6prefix）。
//
// 三道门：
//
//	① `[net] lan_ipv6 = true`（默认开；不想让内网也走模组的 IPv6 就关掉）；
//	② 接口上**没有** PD 前缀（真做了 DHCPv6-PD 的模组，netifd 自己会委派，不用我们插手）；
//	③ **现有前缀只要还可用就绝不动它**（见下）。
//
// ⚠️⚠️ 第 ③ 条是 2026-10-07 才修对的，也是"IPv6 一会儿好一会儿不行"的真凶之一：
// 以前是"取网卡上**第一个**全局地址的 /64"，而
//
//	· 模组重拨后旧 /64 的地址不会立刻消失（SLAAC 生命期还在），新旧前缀会同时在链上；
//	· `net.Interface.Addrs()` 的枚举顺序**不保证跨轮稳定**。
//
// ⇒ 前缀会在新旧之间来回跳，而每换一次前缀都要 `ifup modem0_6`（**接口整条重来**：
// dhcpv6 重跑、防火墙 hotplug reload），于是用户看到的就是 IPv6 通一下、断一下。
// 现在的规矩：**只要现有前缀还在链路上、而且没被内核标成 deprecated，就不碰它**；
// 只有它确实没了/被废弃了才换（换的时候用排序保证"同一组地址永远同一个选择"，不再抖）。
func ensureLANIPv6Prefix(if6, dev string) (note string, errMsg string) {
	if !config.Cfg.Net.LANIPv6 {
		return "", ""
	}
	all, live := netdevGlobalPrefixes6(dev)
	if len(all) == 0 {
		return "", "" // 还没拿到全局 v6 地址（RA 要一点时间），下轮再看
	}
	cur, _ := uciGet("network." + if6 + ".ip6prefix")
	cur = strings.TrimSpace(cur)
	prefix := pickLANIPv6Prefix(all, live, cur)
	if prefix == "" || prefix == cur {
		return "", "" // 不动：现有前缀还合法（或没得挑）
	}
	if cur == "" && netifHasForeignPrefix(if6, prefix) {
		// 我们从没配过前缀、但这个接口上确实有别的前缀 → 那是模组自己 PD 下来的，别插手
		return "（模组自己做了前缀委派 PD，不重复插手）", ""
	}
	// 走到这说明：要么第一次配，要么**我们配过的前缀过期了**
	// —— 模组重拨后运营商换了 /64（实测：240e:400:600:16e8::/64 → 240e:400:648:1cd::/64），
	// 旧前缀还挂在 br-lan 上、还在往内网发，那些地址已经不通了。所以这里要**替换**掉它。
	var cmds [][]string
	if cur != "" {
		cmds = append(cmds, []string{"uci", "-q", "delete", "network." + if6 + ".ip6prefix"})
	}
	cmds = append(cmds, WAN6PrefixCommands(if6, prefix)...)
	var ran []string
	for _, argv := range cmds {
		if err := runNetProvCmd(argv); err != nil {
			logger.Warn("auto wan6: prefix command failed",
				zap.String("cmd", netprov.WANCommandText(argv)), zap.Error(err))
			return "", "把 IPv6 前缀交给内网没成功：" + netprov.WANCommandText(argv) + " 失败：" + err.Error()
		}
		ran = append(ran, netprov.WANCommandText(argv))
	}
	logger.Info("auto wan6: prefix handed to lan",
		zap.String("iface", if6), zap.String("prefix", prefix),
		zap.String("was", cur), zap.Strings("cmds", ran))
	note = "并把 " + prefix + " 交给内网（上联接口的 ip6prefix），内网设备也能走 IPv6"
	if cur != "" {
		note += "（原来是 " + cur + "，模组重拨换了前缀，已更新）"
	}
	return note, ""
}

// netdevGlobalPrefixes6 这张网卡上的全局 IPv6 前缀：全部 + 其中**内核没标 deprecated** 的那部分。
//
// deprecated 标记来自 /proc/net/if_inet6（IFA_F_DEPRECATED = 0x20，见 parseIfInet6Flags）；
// 读不到/解析不了就当作都可用 —— 保守方向：宁可不动前缀，也别无谓地 `ifup` 一次接口。
//
// 为什么不直接用 net.Interface.Addrs() 的顺序：那个顺序**不保证跨轮稳定**，
// 而"第一个全局地址"就是前缀抖动（IPv6 一会儿好一会儿不行）的来源，见 ensureLANIPv6Prefix。
func netdevGlobalPrefixes6(dev string) (all, live []string) {
	in, err := net.InterfaceByName(dev)
	if err != nil {
		return nil, nil
	}
	addrs, err := in.Addrs()
	if err != nil {
		return nil, nil
	}
	var flags map[string]uint32
	if b, err := os.ReadFile("/proc/net/if_inet6"); err == nil {
		flags = parseIfInet6Flags(string(b))
	}
	for _, a := range addrs {
		s := strings.TrimSpace(a.String())
		p, ok := GlobalIPv6PrefixOf(s)
		if !ok {
			continue
		}
		all = append(all, p)
		if !IPv6AddrDeprecated(flags, s) {
			live = append(live, p)
		}
	}
	return all, live
}

// netifHasForeignPrefix 这个接口上有没有**别的前缀**（= 真的从 DHCPv6-PD 委派下来的）。
//
// 为什么要判断：真做了 PD 的模组，netifd 自己会把前缀分给内网，我们不该再塞一个 ip6prefix
// （两个前缀一起发下去，内网会拿到一半不通的地址）。
//
// ⚠️ 不能只看 "ipv6-prefix 非空"：我们自己写进去的 ip6prefix 也会出现在这里
// （实测：{address: 240e:400:600:16e8::, mask: 64, class: modem0_6, assigned: {lan: …}}），
// 配置删掉后运行态还会残留一会儿，那会把自己误判成 PD。所以只在"出现了不是我们这个的前缀"时才算 PD。
func netifHasForeignPrefix(if6, ourPrefix string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), netProvTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ubus", "call", "network.interface."+if6, "status").Output()
	if err != nil {
		return false
	}
	s := string(out)
	i := strings.Index(s, "\"ipv6-prefix\":")
	if i < 0 {
		return false
	}
	region := s[i:]
	if j := strings.Index(region, "]"); j >= 0 {
		region = region[:j] // 只看这个数组里的内容
	}
	base := strings.TrimSuffix(ourPrefix, "/"+prefixLenText(ourPrefix))
	if base != "" && strings.Contains(region, base) {
		return false // 就是我们自己那个前缀（含运行态残留）
	}
	return strings.Contains(region, "\"address\"")
}

// prefixLenText "240e:400:600:16e8::/64" → "64"（只取斜杠后面的位数）
func prefixLenText(prefix string) string {
	if i := strings.LastIndex(prefix, "/"); i >= 0 {
		return prefix[i+1:]
	}
	return ""
}

// netdevAddrs6 网卡当前的**全局** IPv6 地址（逗号分隔；只要 global，丢开 fe80）
func netdevAddrs6(name string) string {
	in, err := net.InterfaceByName(name)
	if err != nil {
		return ""
	}
	addrs, err := in.Addrs()
	if err != nil {
		return ""
	}
	var out []string
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP == nil || ipn.IP.To4() != nil {
			continue
		}
		if ipn.IP.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, ipn.IP.String())
	}
	return strings.Join(out, ", ")
}

// kindOrUnknown USB 类型没识别出来时的占位（日志/提示用）
func kindOrUnknown(kind string) string {
	if strings.TrimSpace(kind) == "" || strings.EqualFold(kind, "unknown") {
		return "未知类型"
	}
	return strings.ToUpper(kind)
}
