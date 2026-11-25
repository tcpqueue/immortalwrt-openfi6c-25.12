package kernel

import (
	"strconv"
	"strings"

	"modemd/module/netprov"
)

// NormalizePDPType 模组上报的 PDP 类型（IPV4V6）→ netifd 协议认的写法（ipv4v6）。
// 认不出的返回空串（调用方就不写这个选项，交给 netifd 默认）。
func NormalizePDPType(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ip", "ipv4":
		return "ip"
	case "ipv6":
		return "ipv6"
	case "ipv4v6", "ipv6v4":
		return "ipv4v6"
	}
	return ""
}

// WANCommands 生成"建接口 + 加防火墙 + 生效"的命令序列（每条是一个 argv）。
//
// ⚠️ 顺序有讲究：先写 UCI 再 commit，再 ifup（netifd 才会去起这张网卡），
// 最后 reload 防火墙 —— fw4 虽然会跟着接口 up 事件自己更新，但 reload 一次更稳
// （只在**第一次**建接口时做，不会反复打断流量）。
//
// ⚠️ 选项按协议分四种（详见 WANPlan / WANProtoForKind）：
//   - dhcp（ECM / NCM / RNDIS，以及 **QMI**）：device / ifname = 主机侧**网卡**（wwan0），
//     外加 peerdns / defaultroute / metric —— 地址/路由/DNS 由 netifd 的 DHCP 取。
//     QMI 与之的区别只在"数据呼叫由 modemd 托管的厂商拨号器做"（见 hostdial.go），
//     接口这一层和 ECM/NCM/RNDIS 走同一条路。
//   - none（**历史写法**，上一版 QMI 用过；现在只在手工配置时才会出现）：
//     device / ifname = 网卡，但 netifd 不配任何东西，地址/路由/DNS 全由拨号器写
//   - qmi / quectel / mbim（历史/手工写法）：device = /dev/cdc-wdmX + apn + pdptype + auth none
func WANCommands(p WANPlan) [][]string {
	iface, proto := p.Iface, strings.ToLower(strings.TrimSpace(p.Proto))
	if proto == "" {
		proto = "dhcp"
	}
	cmds := [][]string{
		// 注意：这里**不**先 `uci -q delete` —— 实测在 OpenWrt 23.05 上它对不存在的键
		// 依然返回 1（-q 只压输出），会被当成失败。同名残留的情况由调用方的
		// "network.<iface>.device 已存在就不动" 兵来将挡。
		{"uci", "set", "network." + iface + "=interface"},
		{"uci", "set", "network." + iface + ".proto=" + proto},
	}
	switch {
	case proto == "none":
		// 不配置协议（历史写法）：netifd 只管"这张接口存在"，地址/路由/DNS 全归拨号器。
		// 现在只有手工这么配才会走到这里（自动组网用 dhcp，见 WANProtoForKind）。
		// device 与 ifname 都写成**网卡**（wwan0）—— 防火墙按它把 wwan0 划进区里。
		if dev := strings.TrimSpace(p.Device); dev != "" {
			cmds = append(cmds,
				[]string{"uci", "set", "network." + iface + ".device=" + dev},
				// legacy ifname：老版 LuCI（Lua）的网络模型只认它（同下面 dhcp 分支的注释）
				[]string{"uci", "set", "network." + iface + ".ifname=" + dev},
			)
		}
	case netprov.WANProtoUsesControlDevice(proto):
		// 主机侧拨号（mbim；qmi / quectel 是历史写法）：由协议脚本在主机侧把数据链路拉起来，
		// 所以必须给**控制口字符设备**与 APN。不给 APN 等于让它去猜运营商，多半上不去。
		// （mbim.sh / quectel.sh 都会自己从 /sys/class/usbmisc/<wdm>/device/net 推出网卡名，
		//   所以这里不要写 ifname）
		cmds = append(cmds, []string{"uci", "set", "network." + iface + ".device=" + p.WDM})
		if apn := strings.TrimSpace(p.APN); apn != "" {
			cmds = append(cmds, []string{"uci", "set", "network." + iface + ".apn=" + apn})
		}
		if pt := NormalizePDPType(p.PDPType); pt != "" {
			cmds = append(cmds, []string{"uci", "set", "network." + iface + ".pdptype=" + pt})
		}
		cmds = append(cmds, []string{"uci", "set", "network." + iface + ".auth=none"})
	case strings.TrimSpace(p.Device) != "":
		dev := strings.TrimSpace(p.Device)
		cmds = append(cmds,
			[]string{"uci", "set", "network." + iface + ".device=" + dev},
			// ⚠️ 同时写一份 legacy 的 option ifname（同一个网卡名）：
			// 老版 LuCI（Lua，还在用）的网络模型**只认 ifname**，光有 device 时它的
			// net:get_interface() 解析不出网卡 → 「网络 → 接口」里这个接口会显示成
			// 「不支持的协议类型」。两者共存 netifd 23.05 实测无冲突（接口照样 up + 拿到地址）。
			[]string{"uci", "set", "network." + iface + ".ifname=" + dev},
		)
	}
	if proto != "none" {
		// 主机侧网卡（eth2 / wwan0 / usb0 …）的接口写 auto + hostname（用户 2026-10-07 定的口径：
		// device / proto / auto / hostname，hostname = 设备名 + "5G"）。
		// ⚠️ mbim 那种 device 是控制口（/dev/cdc-wdm0）的不写：拼出来是 /dev/cdc-wdm05G 这种垃圾。
		if !netprov.WANProtoUsesControlDevice(proto) {
			cmds = append(cmds, []string{"uci", "set", "network." + iface + ".auto=1"})
			if h := WANHostname(p.Device); h != "" {
				cmds = append(cmds, []string{"uci", "set", "network." + iface + ".hostname=" + h})
			}
		}
		// peerdns / defaultroute **不写了**：那两项本来就是 dhcp 的默认值（netifd 里
		// peerdns 缺省 1、defaultroute 缺省 1），写不写一个样 —— 用户要求“不要带任何信息”。
		// ⚠️ metric **保留**：它把模组那条默认路由压到 wan_metric（默认 20），
		// 否则模组 metric 0 的默认路由会抢用户自己那条上联（风险不对称，不敢默默删）。
		if p.Metric > 0 {
			cmds = append(cmds, []string{"uci", "set",
				"network." + iface + ".metric=" + strconv.Itoa(p.Metric)})
		}
	}
	cmds = append(cmds, []string{"uci", "commit", "network"})
	// ⚠️ 不再改 /etc/config/firewall（把接口划进某个区）：用户 2026-10-07 要求
	// "内部处理这个防火墙的都要去掉"。接口不在区里就用不了它，改成**只读检查 + 提示**
	// （见 netprov_engine.go 的 firewallZoneNote）。
	cmds = append(cmds, []string{"ifup", iface})
	return cmds
}

// ---- IPv6 兄弟接口（modem0_6） ----

// WANHostname 接口的 DHCP hostname：**设备名 + "5G"**（用户 2026-10-07 定的命名规则）。
//
//	usb0 → usb05G      wwan0 → wwan05G      eth2 → eth25G
//
// ⚠️ 只给**主机侧网卡**用（eth2 / wwan0 / usb0 …）。mbim 那种 `device` 是控制口字符设备的
// （/dev/cdc-wdm0），拼出来是 `/dev/cdc-wdm05G` 这种垃圾 ⇒ 调用方自己别给控制口用。
// 设备名不合法/为空时返回空串（调用方就不写这个选项）。
func WANHostname(dev string) string {
	dev = strings.TrimSpace(dev)
	if dev == "" || !netprov.ValidNetdevName(dev) {
		return ""
	}
	return dev + "5G"
}

// ---- 哪些型号要用"非 QMI 那套" ----

// wan6NonQMIProfiles 需要用"非 QMI 那套"的**模组型号关键字**，发现新的就往这里加一行。
//
// 用户 2026-10-07 定：
//
//	· 名单里的型号（小写关键字，匹配时忽略 空格 / - / _ / . / /）⇒ 走"非 QMI 那套"：
//	  dhcpv6 用 auto/hostname/extendprefix/extend_prefix（见 WAN6DialOptions），
//	  不带 ip6prefix，并且会给它写 `/etc/firewall.user` 里那段 DNS 重定向 + MASQUERADE；
//	· 其它 dhcp / dhcpv6 **一律走 QMI 那套**（reqaddress=try + ip6prefix + 不碰自定义防火墙）。
//
// ⚠️ 判据**只看型号，不看探测到的 USB 类型**：用户发现 RM500U 在 ECM 模式下就是靠这套才拿到
// IPv6，而探测到的类型在不同固件/模式下不一定靠得住。
// ⚠️ 2026-10-13（用户要求）：**默认清空** —— 判据已改成"按链路类型"（见 netprov_v6fix.go 的
// wan6OnlinkFixKinds：ecm / ncm / rndis 全都要），不再需要在这里逐个列型号。
// 这个变量只在 `[net] v6_fix_models` 留空时兜底；要**额外**照顾某个 QMI 型号就写配置。
var wan6NonQMIProfiles = []string{}

// wan6NonQMIProfile 这台模组要不要走"非 QMI 那套"（只看型号，见 wan6NonQMIProfiles）
func wan6NonQMIProfile(model, modelName string) bool {
	return wan6ModelInList(v6FixModels(), model, modelName)
}

// wan6ModelInList 纯函数：型号在不在给定名单里（不读全局配置 ⇒ 好测、也不怕测试间互相污染）。
//
// 匹配前两边都过 normModelKey（转小写 + 去掉 空格/-/_/./ ），只看关键字在不在型号串里；
// 空关键字一律忽略 —— 否则一行写错（比如 `v6_fix_models = [""]`）会把**所有**型号都命中。
func wan6ModelInList(list []string, model, modelName string) bool {
	hay := normModelKey(model + " " + modelName)
	if hay == "" {
		return false
	}
	for _, want := range list {
		if k := normModelKey(want); k != "" && strings.Contains(hay, k) {
			return true
		}
	}
	return false
}

// normModelKey 型号比对用：转小写 + 去掉空格 / - / _ / . / /（"RM500U-CN" → "rm500ucn"）
func normModelKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch r {
		case ' ', '-', '_', '.', '/':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// WAN6DialOptions v6 兄弟接口的 dhcpv6 选项："QMI 那套"与"非 QMI 那套"**不一样**。
//
//	· QMI 那套（默认；只 reqaddress=try，其它一口不加）；
//	· 非 QMI 那套（只有 wan6NonQMIProfiles 名单里的型号，如 RM500U 的 ECM）
//	  → auto=1 + hostname=<设备名+5G> + extendprefix=1 + extend_prefix=1，
//	  并且**不再带任何其它东西**：reqaddress 与 ip6prefix 都要清掉
//	  （用户原话：“不要带任何信息了”、“list ip6prefix 这个也不要”）。
//	  靠 RA/SLAAC 取地址，并接受模组在 RA 里带来的前缀；extend_prefix/extendprefix 两种
//	  拼写都写一份 —— 不同 netifd/odhcp6c 版本认的不一样。
//
// ⚠️ 用户 2026-10-07 改口径：以前是“凡非 QMI 类型都走这套”，现在是**按型号名单**
// （`wan6NonQMIProfile`）；参数名还叫 qmi（= 用 QMI 那套）是为了少改调用点。
//
// 返回值：set 是要写的「键 = 值」（顺序按用户给的清单，便于单测与日志），del 是要删的键。
// 建新接口（WAN6Commands）与对齐已有接口（WAN6OptionSyncCommands）都用这一份口径，
// 免得两处各写一套再慢慢跑偏。
func WAN6DialOptions(dev string, qmi bool) (set [][2]string, del []string) {
	if qmi {
		// 2026-10-08 用户确认：QMI 按老版本口径只写 reqaddress=try，并把「非 QMI 那套」的残留删掉
		// （升级上来的机器早期被写过 auto/hostname/extendprefix/extend_prefix，只加不删会一直留着）。
		// ip6prefix 是 QMI 把模组前缀交给内网的命根子，绝不能碰。
		return [][2]string{{"reqaddress", "try"}},
			[]string{"auto", "hostname", "extendprefix", "extend_prefix"}
	}
	set = [][2]string{{"auto", "1"}}
	if h := WANHostname(dev); h != "" {
		set = append(set, [2]string{"hostname", h})
	}
	return append(set,
		[2]string{"extendprefix", "1"},
		[2]string{"extend_prefix", "1"},
	), []string{"reqaddress", "ip6prefix"}
}

// WAN6OptionSyncCommands 把**已经存在**的 v6 接口的 dhcpv6 选项对齐到 WAN6DialOptions。
//
// 为什么要单独有它：升级上来的设备上，这张接口是旧版本按老的选项建的（非 QMI 的还带着
// reqaddress=try）——只建新接口、不管老接口的话，那些机器永远用不上新选项。
//
// cur = 当前值（键 → 值，缺的当空串）。已经对上就返回 nil（一条 uci 命令都不产生）。
func WAN6OptionSyncCommands(iface, dev string, qmi bool, cur map[string]string) [][]string {
	set, del := WAN6DialOptions(dev, qmi)
	var cmds [][]string
	for _, kv := range set {
		if strings.TrimSpace(cur[kv[0]]) == kv[1] {
			continue
		}
		cmds = append(cmds, []string{"uci", "set", "network." + iface + "." + kv[0] + "=" + kv[1]})
	}
	for _, k := range del {
		if strings.TrimSpace(cur[k]) == "" {
			continue // 本来就没有，不用删
		}
		cmds = append(cmds, []string{"uci", "-q", "delete", "network." + iface + "." + k})
	}
	if len(cmds) == 0 {
		return nil
	}
	return append(cmds,
		[]string{"uci", "commit", "network"},
		[]string{"ifup", iface},
	)
}

// WAN6Plan IPv6 兄弟接口的配置
//
// 实测（RK3528 + 鼎桥 MT5700M-CN，NCM）：模组**不发 DHCPv6**，IPv6 是随 RA（SLAAC）下来的，
// 而且 odhcp6c 会把 RA 里的前缀/默认路由接管过去（装出一条带 metric 的 "from <前缀>" 默认路由）。
// 所以这里用 netifd 官方的 dhcpv6 客户端来管 v6：既接 RA，模组真跑 DHCPv6 时也能拿地址。
type WAN6Plan struct {
	Iface  string // modem0_6
	Device string // 主机侧网卡（按模组的 USB 拓扑找出来的，如 eth2 / wwan0，**不是写死的**）
	Metric int    // 默认路由 metric：跟 v4 保持一致
	// QMI 这台模组的数据呼叫是不是归主机侧拨号器（QMI，见 WANHostDialByDaemon）。
	// QMI 与非 QMI 的 dhcpv6 选项**不一样**（用户 2026-10-06 明确要求）：
	//   · QMI    → reqaddress=try（沿用旧写法）
	//   · 非 QMI → auto=1 + extendprefix=1 + extend_prefix=1，**不写** reqaddress
	// ⚠️ 它**只管 dhcpv6 选项**：防火墙规则（DNS 重定向 + MASQUERADE）QMI / 非 QMI 都打。
	QMI bool
}

// WAN6Commands 生成建 v6 兄弟接口的命令序列
func WAN6Commands(p WAN6Plan) [][]string {
	iface, dev := p.Iface, strings.TrimSpace(p.Device)
	cmds := [][]string{
		{"uci", "set", "network." + iface + "=interface"},
		{"uci", "set", "network." + iface + ".proto=dhcpv6"},
		{"uci", "set", "network." + iface + ".device=" + dev},
		// legacy ifname：老版 LuCI（Lua）的网络模型只认它（同 WANCommands 里的注释）
		{"uci", "set", "network." + iface + ".ifname=" + dev},
		// force_link：这张接口与 `modem<id>` **共用同一张网卡**，载波一丢（QMI 数据呼叫掉了）
		// netifd 就会按"链路丢失"把接口拉下来 —— 而网卡一 down，驱动就不再上报 link_state，
		// 载波永远回不来，拨号器重拨也白搭（2026-10-09 真机实测）。两张接口都要开。
		{"uci", "set", "network." + iface + ".force_link=1"},
	}
	// 选项按 QMI / 非 QMI 分两套（见 WAN6DialOptions）：
	//   QMI     → reqaddress=try
	//   非 QMI  → auto=1 + hostname=<设备名+5G> + extendprefix=1 + extend_prefix=1
	set, _ := WAN6DialOptions(dev, p.QMI)
	for _, kv := range set {
		cmds = append(cmds, []string{"uci", "set", "network." + iface + "." + kv[0] + "=" + kv[1]})
	}
	// reqprefix 故意不写 = netifd 默认的 auto。
	// 实测（鼎桥 MT5700M-CN）：模组**不给 PD**，写死 reqprefix=no 时接口会一直卡在
	// pending（因为没 DHCPv6 服务器可问）；用默认的 auto，odhcp6c 会把 RA 里的前缀接管过来，
	// 接口正常 up + 拿到地址，而且 LAN（ip6assign 60）不受影响（不会把 WAN 的 /64 发下去）。
	// 真碰上会发 PD 的模组，auto 也是对的。
	if p.Metric > 0 {
		cmds = append(cmds, []string{"uci", "set",
			"network." + iface + ".metric=" + strconv.Itoa(p.Metric)})
	}
	cmds = append(cmds, []string{"uci", "commit", "network"})
	// ⚠️ 不再改 /etc/config/firewall（把接口划进某个区）：用户 2026-10-07 要求
	// "内部处理这个防火墙的都要去掉"。接口不在区里就用不了它，改成**只读检查 + 提示**
	// （见 netprov_engine.go 的 firewallZoneNote）。
	cmds = append(cmds, []string{"ifup", iface})
	return cmds
}

// ---- WAN6 的防火墙规则：DNS 重定向 + IPv6 MASQUERADE ----

// 用户 2026-10-06 要求：v6 兄弟接口建好之后，把这套规则打上
// （`usb0` 换成该接口绑的那张网卡名；QMI / 非 QMI 都打 —— 2026-10-07 更正）：
//
//	iptables  -t nat -A PREROUTING  -p udp --dport 53 -j REDIRECT --to-ports 53
//	iptables  -t nat -A PREROUTING  -p tcp --dport 53 -j REDIRECT --to-ports 53
//	ip6tables -t nat -A PREROUTING  -p udp --dport 53 -j REDIRECT --to-ports 53
//	ip6tables -t nat -A PREROUTING  -p tcp --dport 53 -j REDIRECT --to-ports 53
//	ip6tables -t nat -A POSTROUTING -o <网卡> -j MASQUERADE
//
// ⚠️⚠️ **往哪儿放（三轮实测后定稿，2026-10-07）**：
//
//	① 手写进「防火墙 → 自定义规则」（= **`/etc/firewall.user`**，LuCI 的 Custom Rules 页就是它）⇒
//	   用户实测**稳定生效**；
//	② 我们自建 include 脚本 ⇒ 有时候不生效（用户报"脚本没有运行"）；
//	③ 写 `/etc/nftables.d/*.nft` ⇒ **完全不生效**（那台机器的 fw4 根本没加载它）。
//
// ⇒ 所以现在 modemd **只做一件事：维护 `/etc/firewall.user` 里的一段带标记的块**
// （用户在 LuCI 里看得见、也能自己删）—— **自己不再 exec 任何 iptables/ip6tables**：
//
//	· 写入：`WAN6CustomRulesBlock` 生成那段，`MergeWAN6CustomRules` 整段替换进去
//	  （块外用户自己写的命令一个字不动）；
//	· 生效：写完 reload 一次防火墙，让它把这文件跑一遍；之后每次 restart/reload 也都会重跑
//	  （fw3 source / fw4 `sh -c ". 文件"`）；
//	· 回收：模组都不在了就把那段删掉（`MergeWAN6CustomRules(cur, "")`）。
//
// ⚠️ 每行都写成 `-C 查 → 没有才 -A`：防火墙每次 reload 都会把这文件重跑一遍，
// 不判一下就会一遍遍重复追加。（那个 `-C` 是**写进文件的文本**，由防火墙执行，不是我们自己跑。）
//
// ⚠️ 用户 2026-10-07 明确要求："内部处理这个防火墙的都要去掉，只走自定义防火墙" ⇒
// 任何"modemd 自己下发/校验/补打/删旧规则"的代码都不该再出现在 netprov_engine.go 里。

// wan6LegacyNftIncludeName 旧版（2026-10-06~07）自动写下的 fw4 include 文件名。
// 只留给"升级时把它清掉"用：那台机器没加载它，但不能保证别的机器不加载 —— 留着会跟脚本里的规则重复。
const wan6LegacyNftIncludeName = "99-modemd-wan6.nft"

// wan6FirewallAuto 要不要**自动**给 v6 链路添加/回收这套防火墙规则（DNS 重定向 + IPv6 MASQUERADE）。
//
// ⚠️ 2026-10-07 中途曾临时置 false（用户要手工写规则验证"没生效"是不是这条自动路的问题），
// 同日用户要求恢复 ⇒ 现在 = true。置 false 时会把自己那套（include 段 + 脚本）收掉。
const wan6FirewallAuto = true

// WAN6CustomRulesPath 我们自动维护的那份"自定义规则"文件。
//
// ⚠️ 就用 OpenWrt/LuCI 的老地方：**LuCI 的「防火墙 → 自定义规则」页读写的就是这个文件**
// （luci-app-firewall 的 custom.js 里写死了 `fs.read/write('/etc/firewall.user')`），
// 所以用户能在页面上看见我们写了什么，也随时能自己清掉。
const WAN6CustomRulesPath = "/etc/firewall.user"

// 我们那段规则的起止标记。合并/删除**只认这一对**，块外用户自己写的命令一个字不动。
