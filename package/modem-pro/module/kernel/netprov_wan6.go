//go:build !windows

package kernel

import (
	"fmt"
	"modemd/module/config"
	"modemd/module/netprov"
	"net"
	"os/exec"
	"strings"

	"go.uber.org/zap"
)

// wan6SiblingSkipReason 「这台模组不该有 v6 兄弟接口」的原因（一句给人看的话）。
//
// ⚠️ 必须与 modemV6SiblingWanted 的判断**逐条对应**。原来这里是三支 if-else 手写的，
// 加了 [net] wan_mode 之后没跟上 —— mode=modem_pro 时 WANIPv6Enabled() 仍为 true，
// 于是错误地落到 "mbim 的…" 那一支（真机日志里就是这么错的）。抽成一个函数之后
// 两边只会在同一处改，不会再漏。
//
// hostDialOwnsV6 目前不参与判断（调用点顺手传进来），保留是为了将来要细分
// "主机侧拨号" 与 "netifd 拨号脚本" 两种情形时不必再改调用点。
func wan6SiblingSkipReason(proto string, hostDialOwnsV6 bool) string {
	_ = hostDialOwnsV6
	switch {
	case !WANIPv6Enabled():
		return "（[net] wan_ipv6 已关闭，只走 IPv4）"
	case netprov.WANProtoHostDialed(proto):
		return "（proto=" + proto + "：它的 IPv4/IPv6 在同一张接口上就配好了，兄弟接口会和它抢前缀）"
	case !config.WANModeNow().WantsDHCPv6Sibling():
		return "（[net] wan_mode = " + config.WANModeNow().String() + "：只用一张 " + netprov.ModemProtoName + " 接口，不另建兄弟接口）"
	}
	return ""
}

// disableModemWAN6 把某个模组的 v6 兄弟接口（modem<id>_6）删掉。
//
// 用途：这台模组**不该有** v6 兄弟接口时把它收掉，让配置立刻生效 ——
//
//	· [net] wan_ipv6 关掉（页面上「拨号上网」那个开关）→ 只走 IPv4；
//	· QMI / MBIM 这种主机侧拨号的 → v6 由拨号器一起带回来，兄弟接口会跟它抢前缀
//	  （老版本留下的那张也要收掉，不然它会一直挂着）。
//
// 光是不再新建它，已经建过的那张会一直挂着（netifd 还在上面跑 dhcpv6、防火墙区里也有它）。
// 只动我们自己那套名字（isManagedIface 认的 modem<id>_6），别人手工建的接口不碰。
// why 是给人看的原因（写进页面/日志）。返回的 note 非空表示真删了东西；errMsg 非空表示删失败。
func disableModemWAN6(id int, why string) (note, errMsg string) {
	want := WAN6InterfaceName(id)
	for _, it := range managedInterfacesNow() {
		if it.Name != want {
			continue
		}
		// removeAutoWANInterface 会顺手把这个模组的组网记账清掉，删兄弟接口不该把主接口的
		// 结果也一起抹了 —— 先存下来，删完再放回去。
		prev, had := AutoWANInfoFor(id)
		if err := removeAutoWANInterface(it); err != nil {
			return "", "关掉 IPv6 时删 " + want + " 失败：" + err.Error()
		}
		if had {
			setAutoWANInfo(id, prev)
		}
		// 这套 v6 链路没了，那套防火墙规则（DNS 重定向 + IPv6 MASQUERADE）也该收掉 ——
		// 但只有"我们建的 v6 接口一个都不剩"时才收（多台模组时别删别人的）。
		extra := ""
		if dropWAN6FirewallIfUnused() {
			extra = "，并收掉了 IPv6 的 DNS 重定向/MASQUERADE 规则"
		}
		logger.Info("auto wan: v6 sibling removed",
			zap.Int("modem", id), zap.String("iface", want), zap.String("device", it.Device),
			zap.String("why", why))
		return "已删掉 " + want + why + extra, ""
	}
	return "", ""
}

// ensureModemWAN6 把主机侧 IPv6 接上（返回页面上要看的一句说明；第二个返回值非空表示出错）。
//
// 三件事：
//
//	① 打开网卡的 accept_ra=2：OpenWrt 默认是 0，而路由器又在转发（forwarding=1），
//	   accept_ra 不到 2 就**永远收不到 RA**，也就永远只有 link-local —— 实测这台模组的
//	   IPv6 就是随 RA（SLAAC）下来的，不开这个什么都拿不到（netifd 的 dhcpv6 脚本也不管它）；
//	② 建一张 v6 兄弟接口（modem0_6，proto=dhcpv6）：netifd 的 odhcp6c 会接手 RA
//	   里的前缀与默认路由（装出一条带 metric 的 "from <前缀>" 默认路由），
//	   模组真跑 DHCPv6 时也能直接拿到地址；
//	③ **非 QMI 那套**（只有 wan6NonQMIProfiles 名单里的型号，如 RM500U 的 ECM）把 DHCPv6 选项对齐
//	   （auto/hostname/extendprefix/extend_prefix，并清掉 reqaddress / ip6prefix，见 WAN6DialOptions）；
//	   其它型号一律走 **QMI 那套**（reqaddress=try）。
//	   自定义防火墙那段规则由每轮对账维护（同步拨号后才写、断网就清，见 syncWAN6FirewallDevices）；
//	④ “把模组的前缀交给内网”（上联接口的 `ip6prefix`）**只给 QMI 那套的接口做** ——
//	   用户 2026-10-07 要求非 QMI 那套配置不要带它。
//
// qmiStyle：这台模组用不用“QMI 那套”（= `!wan6NonQMIProfileFor(mod)`）。
// ⚠️ 判据是**型号**（名单），不是探测到的 USB 类型也不是 `WANHostDialByDaemon` ——
// 用户 2026-10-07 发现 RM500U 在 ECM 模式下就是靠非 QMI 那套才拿到 IPv6，
// 而“其它 dhcp/dhcpv6 一律走 QMI 那套”。
func ensureModemWAN6(mod *Modem, dev string, qmiStyle bool) (note string, errMsg string) {
	if6 := WAN6InterfaceName(mod.ID)
	if cur, err := uciGet("network." + if6 + ".proto"); err == nil && strings.TrimSpace(cur) != "" {
		// 已经有了：先把网卡名对了（模组的 ethN/wwanN 会变），再把 DHCPv6 选项对齐、
		// 把 accept_ra 补上，并报一下当前 v6 地址
		if curDev, _ := uciGet("network." + if6 + ".device"); strings.TrimSpace(curDev) != strings.TrimSpace(dev) && dev != "" {
			if err := runNetProvCmds(RebindInterfaceCommands(if6, dev, false)); err != nil {
				errMsg = "IPv6 接口改绑到 " + dev + " 失败：" + err.Error()
			} else {
				logger.Info("auto wan6: interface rebound",
					zap.String("iface", if6), zap.String("from", strings.TrimSpace(curDev)), zap.String("to", dev))
				note = "IPv6 接口 " + if6 + " 已改绑到 " + dev
			}
		}
		setNetdevAcceptRA(dev, 2)
		if note != "" {
			note += "，"
		} else {
			note = "IPv6 接口 " + if6 + "（proto=dhcpv6，" + dev + "）"
		}
		note += "已存在"
		// 升级上来的设备：这张接口是旧版本按老选项建的（QMI 那套带 reqaddress=try）⇒ 对齐一下
		if n := syncWAN6DialOptions(if6, dev, qmiStyle); n != "" {
			note += "；" + n
		}
		// 老版本建的这张接口没有 force_link ⇒ 载波一丢 netifd 就把它（连网卡）拉下来，
		// 而网卡 down 之后驱动不再上报 link_state（载波永远回不来）⇒ 补上（见 WAN6Commands）。
		if v, _ := uciGet("network." + if6 + ".force_link"); strings.TrimSpace(v) != "1" {
			if err := runNetProvCmd([]string{"uci", "set", "network." + if6 + ".force_link=1"}); err == nil {
				if err := runNetProvCmd([]string{"uci", "commit", "network"}); err == nil {
					note += "；给 " + if6 + " 补了 force_link=1（载波丢失时不把接口拉下来）"
					logger.Info("auto wan6: force_link enabled (keep interface up on link loss)",
						zap.Int("modem", mod.ID), zap.String("iface", if6), zap.String("dev", dev))
				}
			}
		}
		if a := netdevAddrs6(dev); a != "" {
			note += "，已拿到 " + a
		}
		if qmiStyle {
			if n, e := ensureLANIPv6Prefix(if6, dev); e != "" {
				errMsg = e
			} else if n != "" {
				note += "；" + n
			}
		} else {
			note += "；非 QMI 那套（自定义防火墙那段由每轮对账维护：拿到 IPv6 地址才写、断网就清）"
		}
		return note, errMsg
	}

	raNote := ""
	if changed, err := setNetdevAcceptRA(dev, 2); err != nil {
		raNote = "（accept_ra 没设上：" + err.Error() + "）"
	} else if changed {
		raNote = "，并把 " + dev + " 的 accept_ra 打开（OpenWrt 默认 0，不收 RA 就拿不到 IPv6）"
	}

	// 防火墙区：**只看不改**（同 v4；用户 2026-10-07 要求内部处理防火墙的都要去掉）
	zoneNote := firewallZoneNote(config.Cfg.Net.WANZone, if6)

	plan := WAN6Plan{Iface: if6, Device: dev, Metric: config.Cfg.Net.WANMetric, QMI: qmiStyle}
	var ran []string
	for _, argv := range WAN6Commands(plan) {
		if err := runNetProvCmd(argv); err != nil {
			logger.Warn("auto wan6: command failed",
				zap.Int("modem", mod.ID), zap.String("cmd", netprov.WANCommandText(argv)), zap.Error(err))
			return "", "自动建 IPv6 接口没成功：" + netprov.WANCommandText(argv) + " 失败：" + err.Error()
		}
		ran = append(ran, netprov.WANCommandText(argv))
	}
	note = "已建 IPv6 接口 " + if6 + "（proto=dhcpv6，绑 " + dev + fmt.Sprintf("，metric %d）", config.Cfg.Net.WANMetric)
	if zoneNote != "" {
		note += "；" + zoneNote
	}
	note += raNote
	if a := netdevAddrs6(dev); a != "" {
		note += "，已拿到 " + a
	}
	logger.Info("auto wan: ipv6 interface provisioned",
		zap.Int("modem", mod.ID), zap.String("iface", if6), zap.String("device", dev),
		zap.Strings("cmds", ran))
	// 把模组的前缀交给内网（模组只给链路上的 /64、不做 PD，不写这个内网就只有 ULA）。
	//
	// ⚠️ 用户 2026-10-07 要求：**非 QMI 那套的接口不要这个** —— 那套配置就只留 device / proto /
	// auto / hostname / extendprefix / extend_prefix，多一个 ip6prefix 都不带（“不要带任何信息”）。
	// 残留的旧 ip6prefix 由 syncWAN6DialOptions 清掉。
	if qmiStyle {
		prefixNote, prefixErr := ensureLANIPv6Prefix(if6, dev)
		if prefixErr != "" {
			errMsg = prefixErr
		} else {
			note += prefixNote
		}
	} else {
		note += "；非 QMI 那套（自定义防火墙那段由每轮对账维护：拿到 IPv6 地址才写、断网就清）"
	}
	// 自定义防火墙那段**不在这里写**：改由 autoWANOnce 每轮对账（syncWAN6FirewallDevices）——
	// 那样"开机先清空、拨号成功才写、断网清掉自己那条"三件事都落在同一个地方，
	// 不会出现"这里写了、那里又删"的互相打架。
	return note, errMsg
}

// syncWAN6DialOptions 把已经存在的 v6 接口的 dhcpv6 选项对齐到 WAN6DialOptions（见 netprov.go）。
//
// 为什么要单独有它：升级上来的设备上，这张接口是旧版本按老选项建的（非 QMI 的还带着
// reqaddress=try）—— 只按新选项建新接口、不管老接口的话，那些机器永远用不上新选项。
// 已经是目标状态（或读不到）就返回空串，一条 uci 命令都不产生。
func syncWAN6DialOptions(if6, dev string, qmi bool) string {
	set, del := WAN6DialOptions(dev, qmi)
	cur := map[string]string{}
	for _, kv := range set {
		if v, err := uciGet("network." + if6 + "." + kv[0]); err == nil {
			cur[kv[0]] = v
		}
	}
	for _, k := range del {
		if v, err := uciGet("network." + if6 + "." + k); err == nil {
			cur[k] = v
		}
	}
	cmds := WAN6OptionSyncCommands(if6, dev, qmi, cur)
	if len(cmds) == 0 {
		return ""
	}
	if err := runNetProvCmds(cmds); err != nil {
		logger.Warn("auto wan6: dhcpv6 option sync failed",
			zap.String("iface", if6), zap.Bool("qmi", qmi), zap.Error(err))
		return "对齐 DHCPv6 选项没成功：" + err.Error()
	}
	logger.Info("auto wan6: dhcpv6 options synced", zap.String("iface", if6), zap.Bool("qmi", qmi))
	if qmi {
		return "DHCPv6 选项已对齐（reqaddress=try）"
	}
	return "DHCPv6 选项已对齐（非 QMI：auto/hostname/extendprefix/extend_prefix；已清掉 reqaddress/ip6prefix）"
}

// hasIPv6Tables 机器上有没有 ip6tables。
//
// ⚠️ 现在只用来决定**写进自定义规则文件的文本**要不要带 ip6tables 那几条
// （对应手写时那句 `[ -n "$(command -v ip6tables)" ]`）—— modemd 自己不再执行任何规则。
func hasIPv6Tables() bool {
	_, err := exec.LookPath("ip6tables")
	return err == nil
}

// wan6NonQMIProfileFor 这台模组走不走"非 QMI 那套"（判据是型号，见 netprov.go 的 wan6NonQMIProfiles）。
func wan6NonQMIProfileFor(mod *Modem) bool {
	if mod == nil {
		return false
	}
	// ★ 2026-10-13：先按**链路类型**判（ecm / ncm / rndis 全都要，见 wan6OnlinkFixKinds）；
	// 类型没命中再看型号名单（`v6_fix_models`，留给"额外的"型号）。两者取并集。
	if wan6OnlinkFixFor(mod) {
		return true
	}
	return wan6NonQMIProfile(mod.Model, mod.ModelName)
}

// netdevHasGlobalIPv6 这张网卡上有没有**全局** IPv6 地址（= 拨号真的成功了、IPv6 链路起来了）
func netdevHasGlobalIPv6(dev string) bool {
	in, err := net.InterfaceByName(dev)
	if err != nil {
		return false
	}
	addrs, err := in.Addrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if _, ok := GlobalIPv6PrefixOf(a.String()); ok {
			return true
		}
	}
	return false
}

// wan6FirewallDevsNow 现在**该有**那段自定义防火墙规则的网卡清单（纯计算，不碰任何文件）：
//
//	· 模组型号在 wan6NonQMIProfiles 名单里（只有列出的型号需要这套 NAT66 + DNS 重定向）；
//	· 它的**数据网卡**上已经拿到全局 IPv6 地址（= 拨号成功）。
//
// ★★ 2026-10-13 用户实测澄清：**这套规则和"用几张接口"无关**。
// RM500U 的 ECM 在 wan_mode=modem_pro（单接口）下**同样需要**它，否则内网拿不到 IPv6
// （用户原话："能拿到 ipv6 是防火墙 没有像那个一样处理"）。
//
// 所以这里**不看 wan_mode、也不要求接口名带 `_6`** —— 遍历受管接口给出的所有数据网卡
// （modem<id> 与 modem<id>_6 通常绑同一张，按网卡去重）。换模式时网卡还是同一张
// ⇒ 规则原样保留：既不会误删（模式 1 也留着），也不会残留（模组拔了自然消失）。
func wan6FirewallDevsNow(mgr *ModemManager) []string {
	if !wan6FirewallAuto {
		return nil // 开关关掉 ⇒ 整段收掉
	}
	inProfile := func(id int) bool {
		if mgr == nil {
			return false
		}
		for _, mod := range mgr.listModems() {
			if mod != nil && mod.ID == id {
				return mod.isRunningNow() && wan6NonQMIProfileFor(mod)
			}
		}
		return false
	}
	return netprov.Wan6FirewallDevsFor(managedInterfacesNow(), inProfile, netdevHasGlobalIPv6)
}

// syncWAN6FirewallDevices 每轮对账一次：把 /etc/firewall.user 里我们那段同步成"现在在用的网卡"。
//
// 这就是用户 2026-10-07 要的三件事，全落在这一个地方（不会再出现"这里写了那里又删"）：
//
//	① **系统启动时清空**：开机第一轮 `wan6FirewallDevsNow` 必然是空的 ⇒ 那段被删掉；
//	② **网络拨号后加入**：链路上拿到全局 IPv6 地址后，下一轮自动写进去；
//	③ **断网清掉自己对应的配置**：网卡那条（或者一条都不剩时的整段）自动消失。
//
// 不 exec 任何 iptables；只有在文件内容**真的变了**时才 reload 一次防火墙，让它按新内容执行。
// wan6FwIncludeWarned 上一次是否已经为"firewall.user 没被 include"打过警告。
// 只在状态翻转时打：这条每轮都成立，不去重会把日志刷爆。
// （AutoWANLoop 是单 goroutine 调用，不需要加锁。）
var wan6FwIncludeWarned bool

func syncWAN6FirewallDevices(mgr *ModemManager) {
	// 老版本（≤ 0.2.220）留下的那套自建挂载（include 段 + 脚本 + nft 文件）顺手清掉：
	// 现在只维护 /etc/firewall.user，留着它们只会互相打架（同一条规则被加两三遍）。
	if dropLegacyWAN6OwnInclude() {
		logger.Info("auto wan6: legacy firewall include cleaned up")
	}
	devs := netprov.Wan6FirewallDevList(wan6FirewallDevsNow(mgr))
	changed, warn, errMsg := syncWAN6CustomRules(devs, hasIPv6Tables())
	if errMsg != "" {
		logger.Warn("auto wan6: custom rules sync failed", zap.String("err", errMsg))
		return
	}
	if changed {
		if len(devs) == 0 {
			logger.Info("auto wan6: custom rules cleared (no modem link is up)")
		} else {
			logger.Info("auto wan6: custom rules applied", zap.Strings("devices", devs))
		}
		if err := runNetProvCmd([]string{"/etc/init.d/firewall", "reload"}); err != nil {
			logger.Warn("auto wan6: firewall reload failed", zap.Error(err))
		}
	}
	// include 缺失这件事**每轮都会成立** —— 每 15 秒刷两行会把日志淹掉（实测），
	// 所以只在"状态翻转"时各打一条：缺了打一次警告、恢复了打一次解除。
	if warn != "" {
		if !wan6FwIncludeWarned {
			wan6FwIncludeWarned = true
			logger.Warn("auto wan6: " + warn)
		}
	} else if wan6FwIncludeWarned {
		wan6FwIncludeWarned = false
		logger.Info("auto wan6: firewall.user 已被防火墙 include，之前的警告解除")
	}
}

// ---- 内网那条 /64 的路由：必须走 LAN，不能走模组 ----
//
// 2026-10-07 真机实测（H29K + RM500U ECM）：`extendprefix` 会把上游那个 /64 **原样**派给 LAN
// （内网设备因此拿到原生全局地址），于是**同一个 /64 同时挂在两张网卡上**：
//
//	240e:...:45a::/64 dev usb0   metric 256    ← netifd 给上游的设备路由
//	240e:...:45a::/64 dev br-lan metric 1024   ← LAN 的前缀分配路由
//
// 内核同前缀取 metric 小的 ⇒ **回给内网设备的包会被送去模组**（`ip -6 route get <内网设备>`
// → dev usb0），表现就是用户说的"IPv6 地址都在、就是访问不了"（ULA 只有一条路由，所以一直正常）。
// 解法：给 LAN 那条 /64 再放一条 **metric 100** 的路由（比 256 小）⇒ 回包走 br-lan。
// 实测：加上它 `ping6 <内网设备全局地址>` 立刻 0% 丢包。
