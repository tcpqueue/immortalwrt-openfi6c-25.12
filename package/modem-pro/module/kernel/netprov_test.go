package kernel

// 自动组网（OpenWrt：自动拨号 + 自动建 WAN 接口与防火墙）的纯逻辑测试。
// 这里的东西都能在本机跑（不碰 sysfs / uci），真机验证另行用路由器做。

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"modemd/module/config"
	"modemd/module/dial"
	"modemd/module/netdev"
	"modemd/module/netprov"
)

func TestWANInterfaceName(t *testing.T) {
	// 每台模组一个独立的 WAN 接口名：多台同时插着时不能互相覆盖
	if got := WANInterfaceName(0); got != "modem0" {
		t.Errorf("WANInterfaceName(0) = %q, want modem0", got)
	}
	if got := WANInterfaceName(12); got != "modem12" {
		t.Errorf("WANInterfaceName(12) = %q, want modem12", got)
	}
}

// TestMatchNetdevToBusPath 模组 → 主机侧网卡的匹配。
// 实测数据取自 RK3528 上的鼎桥 MT5700M-CN：USB 路径 2-1，NCM 网卡 eth2 的
// /sys/class/net/eth2/device 指向 …/usb2/2-1/2-1:2.0。
func TestMatchNetdevToBusPath(t *testing.T) {
	resolved := map[string]string{
		"eth2":   "/sys/devices/platform/ff100000.usb/usb2/2-1/2-1:2.0",
		"wwan0":  "/sys/devices/platform/ff100000.usb/usb2/2-1/2-1:2.2",
		"eth10":  "/sys/devices/platform/ff100000.usb/usb2/2-10/2-10:2.0",
		"usb0":   "/sys/devices/platform/ff140000.usb/usb3/3-1/3-1:1.0",
		"br-lan": "/sys/devices/platform/ff100000.usb/usb2/2-1/2-1:2.0", // 万一桥也挂在这（真实环境不会）
	}

	got := netdev.MatchNetdevToBusPath("2-1", resolved)
	// wwan 优先于 eth（netdev.NetdevRank）；br- 排最后
	want := []string{"wwan0", "eth2", "br-lan"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("netdev.MatchNetdevToBusPath(2-1) = %v, want %v", got, want)
	}

	// ⚠️ 2-1 不能匹配到 2-10（只做前缀匹配就会张冠李戴）
	if got := netdev.MatchNetdevToBusPath("2-10", resolved); len(got) != 1 || got[0] != "eth10" {
		t.Errorf("netdev.MatchNetdevToBusPath(2-10) = %v, want [eth10]", got)
	}
	if got := netdev.MatchNetdevToBusPath("3-1", resolved); len(got) != 1 || got[0] != "usb0" {
		t.Errorf("netdev.MatchNetdevToBusPath(3-1) = %v, want [usb0]", got)
	}
	// 空路径 / 无匹配：返回 nil，让调用方去报"还没交出网卡"
	if got := netdev.MatchNetdevToBusPath("", resolved); got != nil {
		t.Errorf("空 busPath 应返回 nil，得到 %v", got)
	}
	if got := netdev.MatchNetdevToBusPath("9-9", resolved); got != nil {
		t.Errorf("无匹配应返回 nil，得到 %v", got)
	}
}

// TestZoneSyncPlan "把模组接口划进防火墙区"的纯决策（2026-10-07 被我误删、2026-10-08 恢复）：
// 接口不在区里 ⇒ fw3 不给它生成转发跳/MASQUERADE ⇒ 局域网完全上不了网（RM500U 是 eth1 看不出，
// 别的模组是 wwan0 就全死）。⚠️ 只删**自己加过的**那几张 —— 用户自己配的绝不动。
func TestZoneSyncPlan(t *testing.T) {
	// 区里只有用户自己的 wan/wan6 ⇒ 我们建的两张要加进去
	add, del := ZoneSyncPlan(
		[]string{"modem0_6", "modem0"},
		[]string{"wan", "wan6"},
		nil)
	if len(add) != 2 || add[0] != "modem0" || add[1] != "modem0_6" {
		t.Errorf("要加的是 modem0/modem0_6（排序），got %v", add)
	}
	if len(del) != 0 {
		t.Errorf("不该删任何东西，got %v", del)
	}

	// 已在区里 ⇒ 没事可做
	if add, del := ZoneSyncPlan([]string{"modem0"}, []string{"modem0", "wan"}, []string{"modem0"}); len(add)+len(del) != 0 {
		t.Errorf("已经划好就不该再动，add=%v del=%v", add, del)
	}

	// 模组拔了（want 空）⇒ 只收回**我们加过**的那张；用户自己加的要留着
	add, del = ZoneSyncPlan(nil, []string{"wan", "modem0", "myown"}, []string{"modem0"})
	if len(add) != 0 || len(del) != 1 || del[0] != "modem0" {
		t.Errorf("只该收回 modem0，got add=%v del=%v", add, del)
	}

	// 用户自己把 modem0 划进区里了（我们没加过）⇒ 我们没有权力删它
	add, del = ZoneSyncPlan(nil, []string{"modem0"}, nil)
	if len(add)+len(del) != 0 {
		t.Errorf("不是我们加的就不能删，add=%v del=%v", add, del)
	}

	// 空输入不能 panic
	if add, del := ZoneSyncPlan(nil, nil, nil); len(add)+len(del) != 0 {
		t.Errorf("空输入应无事可做，add=%v del=%v", add, del)
	}
}

// TestFirewallZoneCommands 命令生成：没事可做返回 nil（不白写文件/不白 reload 防火墙）；
// 标记跟着增减，并且**必须以 firewalls reload 收尾**（光 commit 内核里不会有转发跳）。
func TestFirewallZoneCommands(t *testing.T) {
	if got := FirewallZoneCommands("@zone[1]", nil, nil, nil); got != nil {
		t.Errorf("没事可做应返回 nil，got %v", got)
	}
	if got := FirewallZoneCommands("", []string{"modem0"}, nil, nil); got != nil {
		t.Errorf("区名空时应返回 nil，got %v", got)
	}
	cmds := FirewallZoneCommands("@zone[1]", []string{"modem0", "modem0_6"}, nil, nil)
	if len(cmds) == 0 {
		t.Fatal("应生成命令")
	}
	if got := strings.Join(cmds[0], " "); got != "uci add_list firewall.@zone[1].network=modem0" {
		t.Errorf("第一条 = %q", got)
	}
	joined := ""
	for _, c := range cmds {
		joined += strings.Join(c, " ") + "\n"
	}
	if !strings.Contains(joined, "firewall.@zone[1].modemd_networks=modem0 modem0_6") {
		t.Errorf("应写下标记（下次才知道哪些是自己加的）：\n%s", joined)
	}
	if !strings.HasSuffix(strings.TrimSpace(joined), "/etc/init.d/firewall reload") {
		t.Errorf("最后必须是防火墙 reload（否则核里还是老的）：\n%s", joined)
	}

	// 全部收回 ⇒ 标记也删掉（不留垃圾），del_list 用 -q（不存在也不报错）
	cmds = FirewallZoneCommands("@zone[1]", nil, []string{"modem0"}, []string{"modem0"})
	joined = ""
	for _, c := range cmds {
		joined += strings.Join(c, " ") + "\n"
	}
	if !strings.Contains(joined, "uci -q del_list firewall.@zone[1].network=modem0") ||
		!strings.Contains(joined, "uci -q delete firewall.@zone[1].modemd_networks") {
		t.Errorf("收回时该 del_list + 删标记：\n%s", joined)
	}
}

// TestTXWedgeDecision "网卡发送队列卡死（内核 NETDEV WATCHDOG）就自动复位模组"的纯决策。
//
// 真机（2026-10-08，F50-PRO + FM160-CN）：每 5 秒一条 `transmit queue 0 timed out`，
// 地址/路由/模组侧全正常，就是发不出去 = 用户看到的"没网"；重拨没用、只有 AT+CFUN=1,1 才恢复。
func TestTXWedgeDecision(t *testing.T) {
	now := time.Now()

	// 计数没涨（历史残留）⇒ 什么都不做
	if act, why := TXWedgeDecision(7, 7, time.Time{}, nil, now); act || why != "" {
		t.Errorf("计数没涨就不该动：act=%v why=%q", act, why)
	}
	if act, _ := TXWedgeDecision(7, 3, time.Time{}, nil, now); act {
		t.Error("计数变小（dmesg 被清过）也不该动")
	}
	// 第一次发现卡死 ⇒ 复位
	if act, why := TXWedgeDecision(7, 8, time.Time{}, nil, now); !act || why == "" {
		t.Errorf("新故障该复位：act=%v why=%q", act, why)
	}
	// 刚复位过（冷却中）⇒ 先等一等（但仍然要提醒）
	act, why := TXWedgeDecision(7, 9, now.Add(-time.Minute), nil, now)
	if act || why == "" {
		t.Errorf("冷却期内不该再复位，但要说一句：act=%v why=%q", act, why)
	}
	// 冷却够了 ⇒ 再复位
	if act, _ := TXWedgeDecision(7, 9, now.Add(-TXWedgeCooldown-time.Second), nil, now); !act {
		t.Error("冷却结束应该再试一次")
	}
	// 一小时内已复位太多次 ⇒ 停手（让人插手），并且原因要写清楚
	many := make([]time.Time, TXWedgeMaxPerHour)
	for i := range many {
		many[i] = now.Add(-time.Duration(i) * time.Minute)
	}
	if act, why := TXWedgeDecision(0, 99, time.Time{}, many, now); act || why == "" {
		t.Errorf("超过上限就不该再重启：act=%v why=%q", act, why)
	}
}

// TestParseUCIConfigAndFindZone 从真实的防火墙配置片段里找到 wan 区。
func TestParseUCIConfigAndFindZone(t *testing.T) {
	const fw = `
config defaults
	option syn_flood '1'
	option input 'REJECT'

config zone
	option name 'lan'
	option input 'ACCEPT'
	list network 'lan'

config zone
	option name 'wan'
	option input 'REJECT'
	option masq '1'
	list network 'wan'
	list network 'wan6'

config include
	option path '/etc/firewall.user'
`
	secs := ParseUCIConfig(fw)
	if len(secs) != 4 {
		t.Fatalf("应解析出 4 节，实际 %d：%+v", len(secs), secs)
	}
	sec, ref, ok := FindZone(secs, "wan")
	if !ok {
		t.Fatal("没找到 wan 区")
	}
	// 匿名段要用 @zone[i] 引用：wan 是第 2 个 zone（0 基 = 1）
	if ref != "@zone[1]" {
		t.Errorf("wan 区引用 = %q, want @zone[1]", ref)
	}
	if sec.Options["masq"] != "1" {
		t.Errorf("masq = %q, want 1", sec.Options["masq"])
	}
	if !ZoneHasNetwork(sec, "wan") || !ZoneHasNetwork(sec, "wan6") {
		t.Errorf("network 列表解析错：%v", sec.Lists["network"])
	}
	if ZoneHasNetwork(sec, "modem0") {
		t.Error("modem0 还没加进 wan 区，不该判成已加")
	}

	// 命名段就用段名（不是 @zone[n]）
	named := ParseUCIConfig("config zone 'wwanz'\n\toption name 'wan'\n")
	if _, ref, ok := FindZone(named, "wan"); !ok || ref != "wwanz" {
		t.Errorf("命名段的引用 = %q ok=%v, want wwanz", ref, ok)
	}

	// 没有这个区就如实说没有（调用方据此只建接口、不碰防火墙）
	if _, _, ok := FindZone(secs, "guest"); ok {
		t.Error("没有 guest 区，不该返回 ok")
	}
	// 区名为空 → 按默认 wan 处理
	if _, ref, ok := FindZone(secs, ""); !ok || ref != "@zone[1]" {
		t.Errorf("空区名应按 wan 处理，得到 %q ok=%v", ref, ok)
	}
}

// TestWANCommands 生成 uci 命令：建接口 → commit → ifup
// （⚠️ 2026-10-07 起**不再动 /etc/config/firewall**：用户要求"内部处理这个防火墙的都要去掉"）
func TestWANCommands(t *testing.T) {
	plain := WANPlan{Iface: "modem0", Device: "eth2", Metric: 20}
	text := joinCmds(WANCommands(plain))
	for _, want := range []string{
		"uci set network.modem0=interface",
		"uci set network.modem0.proto=dhcp",
		"uci set network.modem0.device=eth2",
		"uci set network.modem0.ifname=eth2", // 老 LuCI（Lua）只认 ifname，见 WANCommands 注释
		"uci set network.modem0.auto=1",
		"uci set network.modem0.hostname=eth25G", // 命名规则：设备名 + "5G"
		"uci set network.modem0.metric=20",
		"uci commit network",
		"ifup modem0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("命令里缺少 %q\n---\n%s", want, text)
		}
	}
	// peerdns / defaultroute 不再写：那两项本来就是 dhcp 的默认值（用户要求“不要带任何信息”）
	for _, bad := range []string{"peerdns", "defaultroute"} {
		if strings.Contains(text, bad) {
			t.Errorf("不该再写 %q（dhcp 的默认值）：\n%s", bad, text)
		}
	}
	// ⚠️ 一个字都不能碰防火墙：接口不在区里由页面提示人去 LuCI 划
	if strings.Contains(text, "firewall") {
		t.Errorf("不该再改 /etc/config/firewall：\n%s", text)
	}
	// ⚠️ 不能带 `uci -q delete`：实测它对不存在的键仍返回 1，会被当成失败
	if strings.Contains(text, "delete") {
		t.Errorf("不应先 delete（不存在的键也返回 1，见注释）：\n%s", text)
	}
	// commit network 必须在 ifup 之前（netifd 才有得可读）
	if strings.Index(text, "uci commit network") > strings.Index(text, "ifup modem0") {
		t.Errorf("commit network 应在 ifup 之前：\n%s", text)
	}

	// metric=0 → 不写 metric（用 netifd 默认）
	if strings.Contains(joinCmds(WANCommands(WANPlan{Iface: "modem1", Device: "usb0"})), "metric") {
		t.Error("metric=0 时不该写 metric 选项")
	}
}

// TestWANProtoForKind 接口协议要跟模组的 USB 类型对上。
//
// ⚠️ 只测**纯函数** wanProtoForKindMode：WANProtoForKind / WANProtoForKindWith 会读全局
// Cfg.Net 的 wan_mode 与 host_dial，直接测它们既依赖执行顺序、又会被别的用例污染
// （同一个测试进程里改 Cfg.Net.* 是全局的）。
//
// 期望（装了 luci-proto-modem-pro、host_dial 开着）：
//
//	模式 1/3 → ncm / ecm / rndis / qmi / mbim 一律 modem_pro（一张接口管 v4+v6）；
//	模式 2   → 一律 dhcp（传统的“主接口 + modem<id>_6”两张那套）。
//
// MBIM 不再例外，原因见 netprov_proto.go 文件头（厂商拨号器自带 mbim-cm.c）。
func TestWANProtoForKind(t *testing.T) {
	kinds := []string{"ncm", "ecm", "rndis", "qmi", "mbim", "QMI", "MBIM", "", "unknown"}
	for _, kind := range kinds {
		if got := netprov.WanProtoForKindMode(kind, config.WANModeModemPro, true, true); got != netprov.ModemProtoName {
			t.Errorf("wanProtoForKindMode(%q, modem_pro, 装了包, host_dial=on) = %q, want %q",
				kind, got, netprov.ModemProtoName)
		}
		if got := netprov.WanProtoForKindMode(kind, config.WANModeDHCP, true, true); got != "dhcp" {
			t.Errorf("wanProtoForKindMode(%q, dhcp, 装了包, host_dial=on) = %q, want dhcp", kind, got)
		}
	}
	// QMI / MBIM 的数据呼叫都由 modemd 自己跑的厂商拨号器做 —— 见 TestWANHostDialByDaemon
	// device 选项指的是控制口字符设备的协议：mbim（及历史 qmi/quectel）
	if !netprov.WANProtoUsesControlDevice("mbim") || !netprov.WANProtoUsesControlDevice("qmi") ||
		!netprov.WANProtoUsesControlDevice("quectel") {
		t.Error("mbim / qmi / quectel 的 device 是控制口（/dev/cdc-wdmN）")
	}
	if netprov.WANProtoUsesControlDevice("none") || netprov.WANProtoUsesControlDevice("dhcp") {
		t.Error("none / dhcp 的 device 是网卡（wwanN / ethN）")
	}
	// 地址/路由由主机侧拨号器写进内核的协议（不建 v6 兄弟接口）
	if !netprov.WANProtoHostDialed("none") || !netprov.WANProtoHostDialed("mbim") ||
		!netprov.WANProtoHostDialed("quectel") || netprov.WANProtoHostDialed("dhcp") {
		t.Error("none / qmi / quectel / mbim 的地址归拨号器，dhcp 归 netifd")
	}
	// 要 netifd 协议脚本的协议：mbim 要 umbim；none / dhcp 不用额外装东西
	if !netprov.WANProtoNeedsScript("mbim") || netprov.WANProtoNeedsScript("none") || netprov.WANProtoNeedsScript("dhcp") {
		t.Error("只有 mbim / qmi / quectel 需要 netifd 协议脚本")
	}
	if netprov.WANProtoPackage("qmi") != "uqmi" || netprov.WANProtoPackage("mbim") != "umbim" ||
		netprov.WANProtoPackage("quectel") != "modemd-pro" || netprov.WANProtoPackage("dhcp") != "" {
		t.Error("包名映射不对（qmi→uqmi、mbim→umbim、quectel→modemd-pro 自带）")
	}
}

// TestWANCommandsQuectel QMI 改成 quectel-CM 之后，命令要与原来 qmi 的**同一套形状**：
// device 是控制口字符设备、必须带 APN、不写 ifname（quectel.sh 自己推网卡名）。
func TestWANCommandsQuectel(t *testing.T) {
	text := joinCmds(WANCommands(WANPlan{
		Iface: "modem0", Device: "wwan0", Proto: "quectel",
		WDM: "/dev/cdc-wdm0", APN: "3gnet", PDPType: "IPV4V6", Metric: 20,
	}))
	for _, want := range []string{
		"uci set network.modem0.proto=quectel",
		"uci set network.modem0.device=/dev/cdc-wdm0",
		"uci set network.modem0.apn=3gnet",
		"uci set network.modem0.pdptype=ipv4v6",
		"uci set network.modem0.auth=none",
		"uci set network.modem0.metric=20",
		"ifup modem0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("quectel 命令里缺少 %q\n---\n%s", want, text)
		}
	}
	if strings.Contains(text, "device=wwan0") {
		t.Errorf("quectel 的 device 必须是控制口，不能是网卡：\n%s", text)
	}
	if strings.Contains(text, "ifname") {
		t.Errorf("quectel 不该写 ifname（quectel.sh 自己推）：\n%s", text)
	}
}

// TestWANCommandsQMIAsDHCP QMI 的接口协议现在是 **dhcp**：地址 / 默认路由 / DNS 由 netifd
// 的 DHCP 在 wwan0 上取（这样「网络 → 接口」看得到 IP），而把数据呼叫拉起来那一步仍归
// modemd 托管的厂商拨号器。这里从 WANProtoForKind("qmi") 一路走到命令序列，测的是真正的接线。
func TestWANCommandsQMIAsDHCP(t *testing.T) {
	if proto := netprov.WANProtoForKind("qmi"); proto != "dhcp" {
		t.Fatalf("QMI 的接口协议应为 dhcp，得到 %q", proto)
	}
	text := joinCmds(WANCommands(WANPlan{
		Iface: "modem0", Device: "wwan0", Proto: netprov.WANProtoForKind("qmi"),
		WDM: "/dev/cdc-wdm0", Metric: 20,
	}))
	for _, want := range []string{
		"uci set network.modem0=interface",
		"uci set network.modem0.proto=dhcp",
		"uci set network.modem0.device=wwan0",
		"uci set network.modem0.ifname=wwan0",
		"uci set network.modem0.auto=1",
		"uci set network.modem0.hostname=wwan05G",
		"uci set network.modem0.metric=20",
		"ifup modem0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("QMI（dhcp）命令里缺少 %q\n---\n%s", want, text)
		}
	}
	// 接口这一层不该出现控制口 / APN 这类选项（那些是 mbim/qmi.sh 那套的）
	for _, bad := range []string{"cdc-wdm", "apn=", "pdptype", "auth="} {
		if strings.Contains(text, bad) {
			t.Errorf("QMI 的 dhcp 接口不该写 %q：\n%s", bad, text)
		}
	}
	// 数据呼叫仍然由 modemd 托管的厂商拨号器做（协议是 dhcp ≠ 不用拨号器）
	if !netprov.WANHostDialByDaemon("qmi") {
		t.Error("QMI 的数据呼叫应由 modemd 托管的厂商拨号器做")
	}
}

// TestWANCommandsQMINone **历史写法**（上一版 QMI 用过，现在只在手工配置时才会遇到）：
// none（不配置协议）+ 网卡
//   - device / ifname 都指向主机侧网卡（wwan0），而不是控制口字符设备；
//   - 不写 peerdns / defaultroute / metric：那几项对 none 没有意义（netifd 不装地址与路由，
//     路由的 metric 由 hostdial 自己对齐），写上去会让人以为 netifd 在管这张网卡。
func TestWANCommandsQMINone(t *testing.T) {
	text := joinCmds(WANCommands(WANPlan{
		Iface: "modem0", Device: "wwan0", Proto: "none",
		WDM: "/dev/cdc-wdm0", Metric: 20,
	}))
	for _, want := range []string{
		"uci set network.modem0=interface",
		"uci set network.modem0.proto=none",
		"uci set network.modem0.device=wwan0",
		"uci set network.modem0.ifname=wwan0",
		"uci commit network",
		"ifup modem0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("none 命令里缺少 %q\n---\n%s", want, text)
		}
	}
	for _, bad := range []string{"cid", "apn", "pdptype", "peerdns", "defaultroute", "metric", "auto=", "hostname"} {
		if strings.Contains(text, bad) {
			t.Errorf("none（不配置协议）不该写 %q：\n%s", bad, text)
		}
	}
}

// TestSwitchProtoCommands 老版本建的接口（proto=qmi / quectel / mbim：device 是控制口；
// 或上一版的 proto=none）要能就地改成现在的 **proto=modem_pro / dhcp + 网卡**：
// 改 proto、改绑网卡并补 ifname、**删掉控制口时代遗留的选项**、commit、ifup。
func TestSwitchProtoCommands(t *testing.T) {
	// 模拟一个老 MBIM 接口在 UCI 里留下的选项
	curOpts := []string{"proto", "device", "apn", "pdptype", "auth", "pincode", "auto", "hostname", "metric"}
	text := joinCmds(SwitchProtoCommands("modem0", "dhcp", "wwan0", curOpts))
	for _, want := range []string{
		"uci set network.modem0.proto=dhcp",
		"uci set network.modem0.device=wwan0",
		"uci set network.modem0.ifname=wwan0",
		"uci commit network",
		"ifup modem0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("迁移命令里缺少 %q\n---\n%s", want, text)
		}
	}
	// 控制口时代的选项必须删掉（否则会一直在 LuCI 里挂着）
	for _, gone := range []string{"apn", "pdptype", "auth", "pincode"} {
		if !strings.Contains(text, "uci delete network.modem0."+gone) {
			t.Errorf("应该删掉旧选项 %q：\n%s", gone, text)
		}
	}
	// 通用选项不能动（比如 metric / auto，那是所有协议共用的）
	for _, keep := range []string{"auto", "hostname", "metric"} {
		if strings.Contains(text, "uci delete network.modem0."+keep) {
			t.Errorf("不该删通用选项 %q：\n%s", keep, text)
		}
	}
	// 目标还是控制口协议（mbim）时一个都不能删：那些选项正是它要用的
	mbimText := joinCmds(SwitchProtoCommands("modem0", "mbim", "/dev/cdc-wdm0", curOpts))
	if strings.Contains(mbimText, "uci delete") {
		t.Errorf("目标仍是 mbim 时不该删任何选项：\n%s", mbimText)
	}
	// ifup 必须在 commit 之后（netifd 才会按新协议重起这张接口）
	if strings.Index(text, "uci commit network") > strings.Index(text, "ifup modem0") {
		t.Errorf("ifup 应在 commit 之后：\n%s", text)
	}
	// 网卡名未知（还没枚举出来）时不要写出空的 device
	if only := joinCmds(SwitchProtoCommands("modem0", "none", "", nil)); strings.Contains(only, ".device=") {
		t.Errorf("网卡名为空时不该写 device：\n%s", only)
	}
	// curOpts 为 nil（读不到）时也不能凭空 delete —— `uci -q delete` 对不存在的键返回 1，
	// 会把后面的 commit / ifup 一起带停。
	if none := joinCmds(SwitchProtoCommands("modem0", "modem_pro", "wwan0", nil)); strings.Contains(none, "uci delete") {
		t.Errorf("读不到现有选项时不该写 delete：\n%s", none)
	}
}

// TestStaleControlDeviceOptions 纯函数：只挑出“确实存在”且属于控制口那一套的选项名。
func TestStaleControlDeviceOptions(t *testing.T) {
	got := staleControlDeviceOptions([]string{"PROTO", " apn ", "Apn", "metric", "dhcp", "unknown_x", ""})
	want := map[string]bool{"apn": true, "dhcp": true}
	if len(got) != len(want) {
		t.Fatalf("staleControlDeviceOptions = %v，想要 %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("多挑出了 %q（%v）", g, got)
		}
	}
	// 空输入 / 全是不相关选项 → 空
	if len(staleControlDeviceOptions(nil)) != 0 {
		t.Errorf("nil 输入应该返回空")
	}
	if len(staleControlDeviceOptions([]string{"proto", "metric", "auto"})) != 0 {
		t.Errorf("没有旧的选项时应该返回空")
	}
}

// TestWANCommandsQMI QMI / MBIM 的命令与 dhcp 不同：device 是**控制口字符设备**，
// 而且必须带 APN（拨号在主机侧做）—— 拿 dhcp + 网卡名去糊弄这两种模组就是"协议不对"。
func TestWANCommandsQMI(t *testing.T) {
	text := joinCmds(WANCommands(WANPlan{
		Iface: "modem0", Device: "wwan0", Proto: "qmi",
		WDM: "/dev/cdc-wdm0", APN: "3gnet", PDPType: "IPV4V6", Metric: 20,
	}))
	for _, want := range []string{
		"uci set network.modem0.proto=qmi",
		"uci set network.modem0.device=/dev/cdc-wdm0",
		"uci set network.modem0.apn=3gnet",
		"uci set network.modem0.pdptype=ipv4v6",
		"uci set network.modem0.metric=20",
		"ifup modem0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("QMI 命令里缺少 %q\n---\n%s", want, text)
		}
	}
	// 不能把网卡名写成 device：对 qmi 来说 device 是控制口，写 wwan0 会打架
	if strings.Contains(text, "device=wwan0") {
		t.Errorf("QMI 的 device 必须是控制口，不能是网卡：\n%s", text)
	}
	// 也不能写 ifname：qmi.sh 自己从 sysfs 推网卡名（no_device=1）
	if strings.Contains(text, "ifname") {
		t.Errorf("QMI 不该写 ifname（qmi.sh 自己推）：\n%s", text)
	}
	// 更不该写 auto / hostname：device 是控制口，拼 hostname 会得到 /dev/cdc-wdm05G 那种垃圾
	for _, bad := range []string{"auto=", "hostname"} {
		if strings.Contains(text, bad) {
			t.Errorf("控制口（mbim/qmi）不该写 %q：\n%s", bad, text)
		}
	}
	// PDP 类型认不出时就不写这个选项（交给 netifd 默认）
	if got := joinCmds(WANCommands(WANPlan{Iface: "modem1", Proto: "mbim", WDM: "/dev/cdc-wdm1"})); strings.Contains(got, "pdptype") {
		t.Errorf("没给 PDP 类型时不该写 pdptype：\n%s", got)
	}
	// MBIM 同理
	if got := joinCmds(WANCommands(WANPlan{Iface: "modem1", Proto: "mbim", WDM: "/dev/cdc-wdm1", APN: "ctnet"})); !strings.Contains(got, "proto=mbim") {
		t.Errorf("MBIM 该用 proto=mbim：\n%s", got)
	}
	// NormalizePDPType：模组上报的是大写 IPV4V6
	for in, want := range map[string]string{
		"IPV4V6": "ipv4v6", "ip": "ip", "IPv6": "ipv6", "IPV4": "ip", "??": "",
	} {
		if got := NormalizePDPType(in); got != want {
			t.Errorf("NormalizePDPType(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestHasIPv6Address 模组侧有没有 IPv6（决定要不要建 v6 兄弟接口）
func TestHasIPv6Address(t *testing.T) {
	cases := []struct {
		name string
		st   *dial.DialState
		want bool
	}{
		{"没拨号", nil, false},
		{"纯 IPv4", &dial.DialState{PDPType: "IP", IPv4: "10.0.0.2"}, false},
		{"地址里有 v6", &dial.DialState{PDPType: "IP", IPv6: "240e::1"}, true},
		{"PDP 双栈", &dial.DialState{PDPType: "IPV4V6", IPv4: "10.0.0.2", IPv6: "240e::1"}, true},
		{"PDP 只有 v6", &dial.DialState{PDPType: "IPV6"}, true},
		{"双栈但地址还没拿到", &dial.DialState{PDPType: "IPV4V6"}, true},
	}
	for _, c := range cases {
		if got := HasIPv6Address(c.st); got != c.want {
			t.Errorf("%s: HasIPv6Address = %v, want %v", c.name, got, c.want)
		}
	}
	// 命名：v6 兄弟接口 = v4 接口名 + _6（和官方固件 wan / wan6 一个套路）
	if got := WAN6InterfaceName(0); got != "modem0_6" {
		t.Errorf("WAN6InterfaceName(0) = %q, want modem0_6", got)
	}
}

// TestWAN6Commands IPv6 兄弟接口的命令：dhcpv6 + RA/SLAAC 友好（reqprefix 不写）。
//
// 选项按 QMI / 非 QMI 分两套（用户 2026-10-06 要求）：
//   - QMI    → reqaddress=try（旧写法，保持不动）
//   - 非 QMI → auto=1 + extendprefix=1 + extend_prefix=1，**不写** reqaddress
func TestWAN6Commands(t *testing.T) {
	common := []string{
		"uci set network.modem0_6=interface",
		"uci set network.modem0_6.proto=dhcpv6",
		"uci set network.modem0_6.device=eth2",
		"uci set network.modem0_6.ifname=eth2", // 老 LuCI 只认 ifname
		// reqprefix 不写（留给 netifd 默认 auto）：写 no 时只有 RA 的模组会一直 pending
		"uci set network.modem0_6.metric=20",
		"uci commit network",
		"ifup modem0_6",
	}
	plan := func(qmi bool) WAN6Plan {
		return WAN6Plan{Iface: "modem0_6", Device: "eth2", Metric: 20, QMI: qmi}
	}

	// ---- QMI：保持旧写法 ----
	qmi := joinCmds(WAN6Commands(plan(true)))
	for _, want := range append(append([]string{}, common...), "uci set network.modem0_6.reqaddress=try") {
		if !strings.Contains(qmi, want) {
			t.Errorf("QMI 的 v6 命令里缺少 %q\n---\n%s", want, qmi)
		}
	}
	for _, no := range []string{".auto=", "extendprefix", "extend_prefix", "hostname"} {
		if strings.Contains(qmi, no) {
			t.Errorf("QMI 不该出现 %q：\n%s", no, qmi)
		}
	}

	// ---- 非 QMI：auto + hostname + extendprefix/extend_prefix，且不能有 reqaddress ----
	sta := joinCmds(WAN6Commands(plan(false)))
	for _, want := range append(append([]string{}, common...),
		"uci set network.modem0_6.auto=1",
		"uci set network.modem0_6.hostname=eth25G", // 命名规则：设备名 + "5G"
		"uci set network.modem0_6.extendprefix=1",
		"uci set network.modem0_6.extend_prefix=1",
	) {
		if !strings.Contains(sta, want) {
			t.Errorf("非 QMI 的 v6 命令里缺少 %q\n---\n%s", want, sta)
		}
	}
	if strings.Contains(sta, "reqaddress") {
		t.Errorf("非 QMI 不该写 reqaddress：\n%s", sta)
	}

	// 顺序：先 commit network 再 ifup，最后 reload；且不能出现 delete
	if strings.Index(sta, "uci commit network") > strings.Index(sta, "ifup modem0_6") {
		t.Errorf("commit network 应在 ifup 之前：\n%s", sta)
	}
	if strings.Contains(sta, "delete") {
		t.Errorf("不该出现 delete：\n%s", sta)
	}
	if strings.Contains(joinCmds(WAN6Commands(WAN6Plan{Iface: "modem1_6", Device: "usb0"})), "metric") {
		t.Error("metric=0 时不该写 metric")
	}
}

// TestWAN6OptionSyncCommands 已经存在的 v6 接口要把选项对齐（升级上来的设备带着旧选项）。
func TestWAN6OptionSyncCommands(t *testing.T) {
	// 非 QMI + 旧的 reqaddress=try ⇒ 删 reqaddress、补 auto/hostname/extendprefix/extend_prefix
	text := joinCmds(WAN6OptionSyncCommands("modem0_6", "eth2", false, map[string]string{"reqaddress": "try"}))
	for _, want := range []string{
		"uci set network.modem0_6.auto=1",
		"uci set network.modem0_6.hostname=eth25G",
		"uci set network.modem0_6.extendprefix=1",
		"uci set network.modem0_6.extend_prefix=1",
		"uci -q delete network.modem0_6.reqaddress",
		"uci commit network",
		"ifup modem0_6",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("对齐命令里缺少 %q\n---\n%s", want, text)
		}
	}
	// 已经是目标状态 ⇒ 一条命令都不产生
	if got := WAN6OptionSyncCommands("modem0_6", "eth2", false, map[string]string{
		"auto": "1", "hostname": "eth25G", "extendprefix": "1", "extend_prefix": "1",
	}); got != nil {
		t.Errorf("已经对齐时不该产生命令：%v", got)
	}
	// 设备名换了 ⇒ hostname 要跟着改
	if got := joinCmds(WAN6OptionSyncCommands("modem0_6", "wwan0", false, map[string]string{
		"auto": "1", "hostname": "eth25G", "extendprefix": "1", "extend_prefix": "1",
	})); !strings.Contains(got, "hostname=wwan05G") {
		t.Errorf("设备名变了应该改 hostname：%q", got)
	}
	// 残留的 ip6prefix 也要清掉（用户 2026-10-07：非 QMI 不带这个）
	if got := joinCmds(WAN6OptionSyncCommands("modem0_6", "eth2", false, map[string]string{
		"auto": "1", "hostname": "eth25G", "extendprefix": "1", "extend_prefix": "1",
		"ip6prefix": "240e:400:600:16e8::/64",
	})); !strings.Contains(got, "uci -q delete network.modem0_6.ip6prefix") {
		t.Errorf("非 QMI 应该清掉残留的 ip6prefix：%q", got)
	}
	// QMI：只补 reqaddress=try（不加 auto / hostname），也**不碰** ip6prefix（QMI 照旧要它）
	if got := joinCmds(WAN6OptionSyncCommands("modem0_6", "wwan0", true, map[string]string{})); !strings.Contains(got, "reqaddress=try") {
		t.Errorf("QMI 对齐命令不对：%q", got)
	}
	// QMI 且已经有 reqaddress=try ⇒ 不动
	if got := WAN6OptionSyncCommands("modem0_6", "wwan0", true, map[string]string{"reqaddress": "try"}); got != nil {
		t.Errorf("QMI 已经对时不该产生命令：%v", got)
	}
	// QMI 的 ip6prefix 不能动（QMI 照旧要把模组前缀交给内网）
	if got := WAN6OptionSyncCommands("modem0_6", "wwan0", true, map[string]string{
		"reqaddress": "try", "ip6prefix": "240e:400:600:16e8::/64",
	}); got != nil {
		t.Errorf("QMI 不该碰 ip6prefix：%v", got)
		// QMI + 早期版本留下的「非 QMI 选项」残留 ⇒ 全删掉（2026-10-08），但 ip6prefix 不能动
		if got := joinCmds(WAN6OptionSyncCommands("modem0_6", "wwan0", true, map[string]string{
			"reqaddress": "try", "auto": "1", "hostname": "wwan05G",
			"extendprefix": "1", "extend_prefix": "1", "ip6prefix": "240e:400:600:16e8::/64",
		})); !strings.Contains(got, "uci -q delete network.modem0_6.auto") ||
			!strings.Contains(got, "uci -q delete network.modem0_6.hostname") ||
			!strings.Contains(got, "uci -q delete network.modem0_6.extendprefix") ||
			!strings.Contains(got, "uci -q delete network.modem0_6.extend_prefix") ||
			strings.Contains(got, "ip6prefix=") {
			t.Errorf("QMI 应删掉非 QMI 那套残留、且不碰 ip6prefix：%q", got)
		}
	}
}

// TestWANHostname hostname 命名规则：**设备名 + "5G"**（用户 2026-10-07 定）。
func TestWANHostname(t *testing.T) {
	for dev, want := range map[string]string{
		"usb0": "usb05G", "wwan0": "wwan05G", "eth2": "eth25G",
		"": "", "   ": "",
		"eth0; rm -rf /": "", // 非法网卡名不拼（不能拿来构造字符串）
	} {
		if got := WANHostname(dev); got != want {
			t.Errorf("WANHostname(%q) = %q, want %q", dev, got, want)
		}
	}
}

// TestWAN6FirewallIPTablesRules 要写进自定义规则文件的那几条命令（用户给的那 5 条，
// usb0 → 接口绑的网卡名；ip6tables 不在时只出前两条 IPv4）。
func TestWAN6FirewallIPTablesRules(t *testing.T) {
	text := joinCmds(netprov.WAN6FirewallIPTablesRules("usb0", true))
	for _, want := range []string{
		"iptables -t nat -A PREROUTING -p udp --dport 53 -j REDIRECT --to-ports 53",
		"iptables -t nat -A PREROUTING -p tcp --dport 53 -j REDIRECT --to-ports 53",
		"ip6tables -t nat -A PREROUTING -p udp --dport 53 -j REDIRECT --to-ports 53",
		"ip6tables -t nat -A PREROUTING -p tcp --dport 53 -j REDIRECT --to-ports 53",
		"ip6tables -t nat -A POSTROUTING -o usb0 -j MASQUERADE",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("命令里缺少 %q\n---\n%s", want, text)
		}
	}
	// 幂等判据（`-C`）是从 `-A` 那条派生出来的，不是另写一份
	if got := netprov.WANCommandText(netprov.WAN6FirewallCheckOf(netprov.Wan6MasqArgv("eth2"))); got != "ip6tables -t nat -C POSTROUTING -o eth2 -j MASQUERADE" {
		t.Errorf("-C 形态不对：%q", got)
	}
	// 没有 ip6tables ⇒ 只出 IPv4 两条（对应用户手写时那句 command -v 判断）
	if got := netprov.WAN6FirewallIPTablesRules("usb0", false); len(got) != 2 {
		t.Errorf("没有 ip6tables 时应只出 2 条，实际 %d：%v", len(got), got)
	}
}

// TestWAN6CustomRulesBlock 写进 /etc/firewall.user（= LuCI 的「防火墙 → 自定义规则」）的那一段：
// 内容就是用户给的那几条，每条 `-C 查 → 没有才 -A`（防火墙每次 restart/reload 都会把这文件重跑一遍），
// 网卡名按实际网卡来（多台模组就多条 MASQUERADE）。
//
// ⚠️ 2026-10-07 用户实测：手写进「自定义规则」**稳定生效** ⇒ 现在 modemd 直接维护这个文件里的一段，
// 而不是另外造脚本（自建脚本"有时候不运行"，写 /etc/nftables.d/*.nft 更是完全不生效）。
func TestWAN6CustomRulesBlock(t *testing.T) {
	text := netprov.WAN6CustomRulesBlock([]string{"eth2", "usb0", "usb0", "  ", "bad;name"}, true)
	for _, want := range []string{
		netprov.Wan6RulesBegin,
		netprov.Wan6RulesEnd,
		// 5 条本体（-A 形态）
		"iptables -t nat -A PREROUTING -p udp --dport 53 -j REDIRECT --to-ports 53",
		"iptables -t nat -A PREROUTING -p tcp --dport 53 -j REDIRECT --to-ports 53",
		"ip6tables -t nat -A PREROUTING -p udp --dport 53 -j REDIRECT --to-ports 53",
		"ip6tables -t nat -A PREROUTING -p tcp --dport 53 -j REDIRECT --to-ports 53",
		// 每个网卡一条（去重 + 排序 + 丢掉非法名）
		"ip6tables -t nat -A POSTROUTING -o eth2 -j MASQUERADE",
		"ip6tables -t nat -A POSTROUTING -o usb0 -j MASQUERADE",
		// 幂等：每条都先 -C 查
		"ip6tables -t nat -C POSTROUTING -o usb0 -j MASQUERADE 2>/dev/null || ip6tables -t nat -A POSTROUTING -o usb0 -j MASQUERADE",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("规则块里缺少 %q\n---\n%s", want, text)
		}
	}
	if strings.Contains(text, "bad;name") {
		t.Errorf("非法网卡名不该写进去：\n%s", text)
	}
	// ⚠️ 防火墙（fw3 source / fw4 `sh -c ". 文件"`）都是**source**这个文件的：
	// 里面出现 exit / set -e 会把防火墙自己那套也一起结束掉
	for _, bad := range []string{"exit", "set -e"} {
		if strings.Contains(text, bad) {
			t.Errorf("规则块里不该出现 %q（防火墙是 source 它的）：\n%s", bad, text)
		}
	}
	// 没有 ip6tables 的机器只出 IPv4 那两条（注释里出现 "MASQUERADE" 不算）
	if got := netprov.WAN6CustomRulesBlock([]string{"usb0"}, false); strings.Contains(got, "ip6tables -t nat") || strings.Contains(got, "-j MASQUERADE") {
		t.Errorf("没有 ip6tables 时不该出 ip6tables 命令：\n%s", got)
	}
	// 顺序稳定 ⇒ 同一组设备（顺序不同）必须生成一模一样的内容（否则每轮都重写文件 + reload 防火墙）
	if a, b := netprov.WAN6CustomRulesBlock([]string{"usb0", "eth2"}, true), netprov.WAN6CustomRulesBlock([]string{"eth2", "usb0"}, true); a != b {
		t.Error("设备顺序不同不该产生不同的规则块")
	}
	// ⚠️⚠️ 空清单（= 没有"该处理的"模组，比如换成了别的型号）⇒ **整段收掉**，
	// 绝不能留下一段只有 4 条 DNS 重定向的块：那几条没有接口限定，会把整个路由器的
	// IPv4/IPv6 DNS 都劫持到本机 53，别的模组的链路/电脑上网就都不正常了（2026-10-08 真机事故）。
	for _, has6 := range []bool{true, false} {
		if got := netprov.WAN6CustomRulesBlock(nil, has6); got != "" {
			t.Errorf("空网卡清单应返回空串（hasIP6tables=%v），实际：\n%s", has6, got)
		}
		if got := netprov.WAN6CustomRulesBlock([]string{"  ", "bad;name"}, has6); got != "" {
			t.Errorf("网卡名全非法应返回空串（hasIP6tables=%v），实际：\n%s", has6, got)
		}
	}
}

// TestMergeWAN6CustomRules 合并 / 替换 / 删除那段标记块 —— **块外用户自己写的命令一个字都不能动**
// （这个文件是用户的地盘，LuCI 自定义规则页会整文件读写）。
func TestMergeWAN6CustomRules(t *testing.T) {
	const mine = "# 我自己写的\nip6tables -A FORWARD -j ACCEPT\n"
	block := netprov.WAN6CustomRulesBlock([]string{"usb0"}, true)

	// ① 文件里本来没有我们的块 ⇒ 追加，且不动用户的内容
	got, changed := netprov.MergeWAN6CustomRules(mine, block)
	if !changed || !strings.Contains(got, "我自己写的") || !strings.Contains(got, netprov.Wan6RulesBegin) {
		t.Fatalf("追加失败：changed=%v\n---\n%s", changed, got)
	}
	// ② 再合一次必须"没变化"（否则每轮都要重写文件 + reload 防火墙）
	if got2, again := netprov.MergeWAN6CustomRules(got, block); again || got2 != got {
		t.Errorf("内容没变时不该报告 changed：%v\n---\n%s", again, got2)
	}
	// ③ 换一批网卡 ⇒ 整段替换，用户那段还在、我们的块只有一段
	got3, changed3 := netprov.MergeWAN6CustomRules(got, netprov.WAN6CustomRulesBlock([]string{"eth2"}, true))
	if !changed3 || !strings.Contains(got3, "我自己写的") || strings.Contains(got3, "-o usb0") ||
		!strings.Contains(got3, "-o eth2") || strings.Count(got3, netprov.Wan6RulesBegin) != 1 {
		t.Errorf("替换失败：\n%s", got3)
	}
	// ④ block="" ⇒ 把块删掉，用户那段原样保留（"模块不在就去掉自定义里的配置"）
	got4, changed4 := netprov.MergeWAN6CustomRules(got3, "")
	if !changed4 || strings.Contains(got4, "modemd wan6") || !strings.Contains(got4, "我自己写的") {
		t.Errorf("删除失败：\n%s", got4)
	}
	// ⑤ 删除之后再删 = 什么都不做
	if got5, changed5 := netprov.MergeWAN6CustomRules(got4, ""); changed5 || got5 != got4 {
		t.Errorf("没有我们的块时删除不该改文件：\n%s", got5)
	}
	// ⑥ 空文件 + 空块 = 空文件
	if got6, changed6 := netprov.MergeWAN6CustomRules("", ""); changed6 || got6 != "" {
		t.Errorf("空文件空块应原样返回：%q", got6)
	}
	// ⑦ 文件里只有我们那段时，删掉后应该变成空文件（而不是留一堆空行）
	only, _ := netprov.MergeWAN6CustomRules("", block)
	if got7, _ := netprov.MergeWAN6CustomRules(only, ""); strings.TrimSpace(got7) != "" {
		t.Errorf("删掉后应只剩空白：%q", got7)
	}
}

// TestWAN6FirewallScriptPath 脚本路径：与配置文件同目录，而且**永远绝对**
// （相对路径会让 fw4 找不到脚本 ⇒ 永不被执行）。⚠️ 这是给 Linux 用的路径，按 POSIX 拼法比。
func TestWAN6FirewallScriptPath(t *testing.T) {
	const want = "/etc/modemd/wan6-firewall.sh"
	if got := netprov.WAN6FirewallScriptPath("/etc/modemd/config.toml"); got != want {
		t.Errorf("脚本路径 = %q, want %q", got, want)
	}
	// 配置路径为空/相对 ⇒ 退到 /etc/modemd（绝不能是当前目录）
	if got := netprov.WAN6FirewallScriptPath(""); got != want {
		t.Errorf("配置路径为空时应退到 %q：%q", want, got)
	}
	if got := netprov.WAN6FirewallScriptPath("config.toml"); !strings.HasPrefix(got, "/") {
		t.Errorf("相对配置路径也要给出绝对脚本路径：%q", got)
	}
}

// TestLAN6WantOf "只有指定型号（wan6NonQMIProfiles）才往 /etc/config/network 加东西"：
// 用户 2026-10-08 明确要求 —— `route6 modemd_lan6route` 与配套的 NDP 段"也是只要有 500u 才加的"，
// 别的模组一个字节都不加（want 为空 ⇒ 已有段会被删掉）。
func TestLAN6WantOf(t *testing.T) {
	items := []lan6Item{
		{Iface: "modem1_6", Prefix: "240e:478:2c20:3660::/64", Designated: false}, // 别的模组：再好的前缀也不要
		{Iface: "modem0_6", Prefix: "240e:479:8a10:1e94::/64", Designated: true},  // RM500U
		{Iface: "modem2_6", Prefix: "", Designated: true},                         // 指定型号但还没拨上
	}
	p, ifs := lan6WantOf(items)
	if p != "240e:479:8a10:1e94::/64" {
		t.Errorf("前缀应取指定型号那个，got %q", p)
	}
	if len(ifs) != 1 || ifs[0] != "modem0_6" {
		t.Errorf("接口只该有指定型号那一个，got %v", ifs)
	}
	// 一台指定型号都没有 ⇒ 全空（调用方会因此删掉我们加的段）
	if p, ifs := lan6WantOf([]lan6Item{{Iface: "modem1_6", Prefix: "240e:478:2c20:3660::/64"}}); p != "" || len(ifs) != 0 {
		t.Errorf("不是指定型号 ⇒ 必须全空，got %q %v", p, ifs)
	}
	// 空输入不能 panic
	if p, ifs := lan6WantOf(nil); p != "" || len(ifs) != 0 {
		t.Errorf("空输入应全空，got %q %v", p, ifs)
	}
}

// TestWAN6ModelInList 「**额外**指定的型号」匹配（`[net] v6_fix_models`）。
//
// ⚠️ 2026-10-13 口径变了：**主判据是"链路类型"**（ecm / ncm / rndis，见
// TestWan6NeedsOnlinkFix 与 wan6OnlinkFixKinds），型号名单降级成**补充**通路 ——
// 用户原话："v6_fix_models 把这个去掉，应该 ecm 都要这么处理的，ecm 全部按走这个"。
//
// ⚠️ 这里刻意测**纯函数** wan6ModelInList 而不是 wan6NonQMIProfile：
// 后者要读全局 Cfg.Net.V6FixModels，而全量跑时别的测试可能已经把它填成非空的了，
// 断言"默认名单为空"就会偶发失败（踩过）。
func TestWAN6ModelInList(t *testing.T) {
	// ① 空名单 ⇒ 任何型号都不命中（"RM500U 要处理"现在由类型判据保证，不靠这里）
	for _, c := range []struct{ model, modelName string }{
		{"RM500U", "RM500U-CN"},
		{"FM160-CN", ""},
		{"", ""},
	} {
		if wan6ModelInList(nil, c.model, c.modelName) {
			t.Errorf("空名单时 %q/%q 不该命中（类型判据在 wan6NeedsOnlinkFix）", c.model, c.modelName)
		}
	}

	// ② 配了名单 ⇒ 按关键字匹配（忽略 空格/-/_/.），且不能前缀命中错型号
	list := []string{"rm500u", "fm160"}
	for _, c := range []struct {
		model, modelName string
		want             bool
	}{
		{"RM500U", "RM500U-CN", true},
		{"rm500u_cn", "", true},
		{"", "Quectel RM500U-EA", true},
		{"RM500U", "", true},
		{"FM160-CN", "", true},
		{"RM500", "", false}, // 不能前缀命中错型号
		{"RM520N-GL", "", false},
		{"", "", false},
	} {
		if got := wan6ModelInList(list, c.model, c.modelName); got != c.want {
			t.Errorf("wan6ModelInList(%q, %q) = %v, want %v", c.model, c.modelName, got, c.want)
		}
	}

	// ③ 名单里有空关键字 ⇒ 忽略它，不能变成"全都命中"
	if wan6ModelInList([]string{"", "   "}, "RM520N-GL", "") {
		t.Error("空关键字必须被忽略，否则一行写错会把所有型号都命中")
	}

	// ④ 内置默认名单里不许有空关键字
	for _, m := range wan6NonQMIProfiles {
		if normModelKey(m) == "" {
			t.Errorf("wan6NonQMIProfiles 里有空关键字：%q", m)
		}
	}
}

// **现有前缀还在链路上（且没被内核标 deprecated）就绝不能动它**，因为每换一次都要
// `ifup modem0_6`（接口整条重来），地址枚举顺序又不稳定 ⇒ 会来回跳。
// TestPickLANIPv6Prefix "交给内网的前缀"怎么选 —— 这是"IPv6 一会儿好一会儿不行"的防抖关键：
// **现有前缀还在链路上（且没被内核标 deprecated）就绝不能动它**，因为每换一次都要
// `ifup modem0_6`（接口整条重来），地址枚举顺序又不稳定 ⇒ 会来回跳。
func TestPickLANIPv6Prefix(t *testing.T) {
	const a, b = "240e:400:600:16e8::/64", "240e:400:648:1cd::/64"
	cases := []struct {
		name      string
		all, live []string
		cur, want string
		why       string
	}{
		{"现有前缀还在链路上 ⇒ 不动", []string{a, b}, []string{a, b}, a, a, "防抖：绝不因为'第一个是 b'就改"},
		{"现有前缀是 b 也不动", []string{a, b}, []string{a, b}, b, b, ""},
		{"现有前缀没了 ⇒ 换成可用的那个", []string{b}, []string{b}, a, b, "真·过期"},
		{"现有前缀被标 deprecated ⇒ 换成可用的", []string{a, b}, []string{b}, a, b, "SLAAC 生命期还在但已废弃"},
		{"拿不到 deprecated 信息且现有前缀还在 ⇒ 保守不动", []string{a, b}, nil, a, a, "宁可不动"},
		{"拿不到信息、现有前缀也没了 ⇒ 退到全部里排序第一个", []string{a, b}, nil, "240e:400:9::/64", a, "排序保证确定性"},
		{"第一次配", []string{b, a}, []string{b, a}, "", a, "排序后取第一个"},
		{"链路上一个全局地址都没有 ⇒ 不动", nil, nil, a, "", ""},
		{"cur 为空且没有地址", nil, nil, "", "", ""},
	}
	for _, c := range cases {
		if got := pickLANIPv6Prefix(c.all, c.live, c.cur); got != c.want {
			t.Errorf("%s: pickLANIPv6Prefix(%v, %v, %q) = %q, want %q %s", c.name, c.all, c.live, c.cur, got, c.want, c.why)
		}
	}
	// 同一组地址（顺序不同）必须得到同一个结果 —— 不然每轮都会来回改
	if x, y := pickLANIPv6Prefix([]string{a, b}, []string{a, b}, ""), pickLANIPv6Prefix([]string{b, a}, []string{b, a}, ""); x != y {
		t.Errorf("地址顺序不同不该给出不同选择：%q vs %q", x, y)
	}
}

// TestParseIfInet6Flags 解析 /proc/net/if_inet6 的 deprecated 标记（拿真机那种格式的样本）。
func TestParseIfInet6Flags(t *testing.T) {
	// 字段：<32 位十六进制地址> <ifindex> <prefixlen> <scope> <flags> <网卡名>
	const proc = `00000000000000000000000000000001 01 80 10 80       lo
fe800000000000000000000000000001 02 40 20 80     br-lan
240e0400060016e89e5440fffe01c5c0 02 40 00 00     usb0
240e0400064801cd9e5440fffe01c5c0 05 40 00 20     usb0
这是一行垃圾 02 40 00 00 usb0
`
	flags := parseIfInet6Flags(proc)
	if got := flags["240e:400:600:16e8:9e54:40ff:fe01:c5c0"]; got != 0 {
		t.Errorf("普通全局地址的标志位 = %#x, want 0", got)
	}
	if got := flags["240e:400:648:1cd:9e54:40ff:fe01:c5c0"]; got != ifaFlagDeprecated {
		t.Errorf("被废弃地址的标志位 = %#x, want %#x", got, ifaFlagDeprecated)
	}
	// 查询口径：带 /64 或不带都认；查不到/格式不对 ⇒ 一律当作"没被废弃"
	if IPv6AddrDeprecated(flags, "240e:400:600:16e8:9e54:40ff:fe01:c5c0/64") {
		t.Error("普通地址不该判成 deprecated")
	}
	if !IPv6AddrDeprecated(flags, "240e:400:648:1cd:9e54:40ff:fe01:c5c0/64") {
		t.Error("0x20 的地址应判成 deprecated")
	}
	for _, bad := range []string{"", "garbage", "240e:400:600:16e8::1"} {
		if IPv6AddrDeprecated(flags, bad) {
			t.Errorf("%q 不该判成 deprecated", bad)
		}
	}
	if IPv6AddrDeprecated(nil, "240e:400:648:1cd:9e54:40ff:fe01:c5c0") {
		t.Error("没有 flags 表（proc 读不到）时应一律当作没废弃")
	}
}

// TestGlobalIPv6PrefixOf 从网卡地址算出「交给内网的前缀」。
//
// 实测背景（RK3528 + 鼎桥 MT5700M-CN）：模组只在链路上发一个 /64（RA/SLAAC），**不做 PD**，
// netifd 的 ipv6-prefix 是空的 → odhcpd 报 "no public prefix on lan" → 内网只有 ULA。
// 把这个 /64 写进上联接口的 ip6prefix 后，br-lan 立刻拿到 240e:400:600:16e8::1/64，
// 内网 PC 拿到全局地址并能 ping6 出去（实测 224 ms）。
func TestGlobalIPv6PrefixOf(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"240e:400:600:16e8:9e54:40ff:fe01:c5c0/64", "240e:400:600:16e8::/64", true},
		{"240e:400:600:16e8::1/64", "240e:400:600:16e8::/64", true},
		{"2001:db8:1:2::5/60", "2001:db8:1::/60", true}, // /60：第 4 组整个被掩掉
		{"fd3f:e5ea:127a::1/64", "", false},             // ULA：内网自己的，不该当公网前缀
		{"fe80::1/64", "", false},                       // link-local
		{"240e::1/128", "", false},                      // 委派 /128 没意义
		{"10.0.0.2/24", "", false},                      // 不是 v6
		{"", "", false},
		{"garbage", "", false},
	}
	for _, c := range cases {
		got, ok := GlobalIPv6PrefixOf(c.in)
		if ok != c.wantOK || got != c.want {
			t.Errorf("GlobalIPv6PrefixOf(%q) = %q/%v, want %q/%v", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

// TestWAN6PrefixCommands 交前缀的命令：必须 add_list 在**上联接口**上（放 LAN 上会被黑洞掉）
func TestWAN6PrefixCommands(t *testing.T) {
	text := joinCmds(WAN6PrefixCommands("modem0_6", "240e:400:600:16e8::/64"))
	for _, want := range []string{
		"uci add_list network.modem0_6.ip6prefix=240e:400:600:16e8::/64",
		"uci commit network",
		"ifup modem0_6",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("缺少 %q\n---\n%s", want, text)
		}
	}
	if strings.Contains(text, "firewall") {
		t.Error("补前缀不该再去动防火墙")
	}
}

// TestLAN6RouteCommands 把「内网那个 /64」钉在 LAN 上的对账命令。
//
// 背景（2026-10-07 真机定案）：模组的 /64 会**同时**出现在 usb0（metric 256，netifd 给上游网卡配的）
// 与 br-lan（metric 1024，LAN 前缀分配生成的）上，内核同前缀只按 metric 选路 ⇒ 回给内网设备的包
// 被送去模组。所以要在 LAN 上补一条 metric 更小的路由（LAN6RouteMetric）。
//
// 这里钉三件事：① 没事可做时必须返回 nil（`uci commit network` 会重载网络，不能每轮都写）；
// ② metric 必须 < 256（否则上游那条永远赢）；③ 删/写两种形态都要 commit（不 commit 等于没写）。
func TestLAN6RouteCommands(t *testing.T) {
	const pfx = "240e:400:600:45a::/64"

	if got := LAN6RouteCommands("", "", "lan"); got != nil {
		t.Errorf("谁都不需要它时不该有任何命令，得到 %v", got)
	}
	if got := LAN6RouteCommands(pfx, pfx, "lan"); got != nil {
		t.Errorf("已经一致时不该重写（每轮 commit 会重载网络），得到 %v", got)
	}
	if got := LAN6RouteCommands(pfx, "", ""); got != nil {
		t.Errorf("连 LAN 接口名都不知道时不该乱写，得到 %v", got)
	}

	set := joinCmds(LAN6RouteCommands(pfx, "", "lan"))
	for _, want := range []string{
		"uci set network." + LAN6RouteSection + "=route6",
		"uci set network." + LAN6RouteSection + ".interface=lan",
		"uci set network." + LAN6RouteSection + ".target=" + pfx,
		"uci set network." + LAN6RouteSection + ".metric=" + strconv.Itoa(LAN6RouteMetric),
		"uci commit network",
		// ⚠️ **commit 自己不会生效**（真机实测）⇒ 必须跟着一条 network reload
		"ubus call network reload",
	} {
		if !strings.Contains(set, want) {
			t.Errorf("钉前缀时缺少 %q\n---\n%s", want, set)
		}
	}

	// 模组不在了/没有全局地址 ⇒ 只收掉自己那一段（同样要 reload，否则内核里那条路由还在）
	del := joinCmds(LAN6RouteCommands("", pfx, "lan"))
	if !strings.Contains(del, "uci -q delete network."+LAN6RouteSection) {
		t.Errorf("应该删掉自己那段\n---\n%s", del)
	}
	if !strings.Contains(del, "uci commit network") {
		t.Errorf("删了之后必须 commit（否则重启前还生效）\n---\n%s", del)
	}
	if !strings.Contains(del, "ubus call network reload") {
		t.Errorf("删了之后必须 reload（否则内核里那条路由不会消）\n---\n%s", del)
	}
	if strings.Contains(del, "network.lan") {
		t.Errorf("回收时不该碰 lan 接口本身\n---\n%s", del)
	}

	// 前缀换了（模组重拨）⇒ 跟着改，且用新值
	chg := joinCmds(LAN6RouteCommands("240e:400:648:1cd::/64", pfx, "lan"))
	if !strings.Contains(chg, ".target=240e:400:648:1cd::/64") {
		t.Errorf("前缀变了要跟着改\n---\n%s", chg)
	}
	if !strings.Contains(chg, "ubus call network reload") {
		t.Errorf("换了前缀必须 reload 才真的生效\n---\n%s", chg)
	}
	if strings.Contains(chg, ".target="+pfx+"\n") {
		t.Errorf("不该留下旧前缀\n---\n%s", chg)
	}

	// 上游网卡那条是 netifd 自动生成的 metric 256 ⇒ 我们必须更小
	if LAN6RouteMetric >= 256 {
		t.Fatalf("LAN6RouteMetric 必须 < 256（上游那条是 256），现在是 %d", LAN6RouteMetric)
	}
}

// TestLAN6NDPCommands 让内网设备能上 IPv6 的「NDP 代理」那一环（odhcpd 的 ndp relay）。
//
// 背景（2026-10-07 真机定案）：模组给的是 **on-link 的 RA /64（没有前缀委派 PD）**，而这个 /64
// 我们又原样发给了 LAN ⇒ 公网回来的包要由路由器在上游链路上替内网设备应答 NS。缺这一环的症状是
// 「路由器自己能上 v6、内网设备 100% 丢包」（`ip -6 neigh show proxy` 空、`proxy_ndp=0`，
// 但 `Ip6OutForwDatagrams` 在涨 = 包确实转出去了）。实测必须给**模组的 v6 接口**建一个
// `ignore=1 + master=1 + ndp=relay` 的 dhcp 段；只设 LAN 那一半**一条代理条目都不会产生**。
func TestLAN6NDPCommands(t *testing.T) {
	const iface = "modem0_6"
	master := LAN6NDPMasterSection(iface)

	// 谁都不需要、也没有我们的标记 ⇒ 一根手指都不动
	if got := LAN6NDPCommands(nil, nil, "", false); got != nil {
		t.Errorf("没人用的时候不该有任何命令，得到 %v", got)
	}
	// 别人自己配的段（名字不带我们的前缀）不碰
	if got := LAN6NDPCommands(nil, []LAN6NDPSection{{Name: "wan6", Iface: "wan6", NDP: "relay"}}, "relay", false); got != nil {
		t.Errorf("回收时不该碰别人的段，得到 %v", got)
	}

	// ① 有模组在用 ⇒ 建 master 段 + 给 LAN 开 relay
	set := joinCmds(LAN6NDPCommands([]string{iface}, nil, "", false))
	for _, want := range []string{
		"uci set dhcp." + master + "=dhcp",
		"uci set dhcp." + master + ".interface=" + iface,
		"uci set dhcp." + master + ".ignore=1",
		"uci set dhcp." + master + ".master=1", // ⚠️ master 是关键（标出"上游那侧"）
		"uci set dhcp." + master + ".ndp=relay",
		"uci set dhcp.lan.ndp=relay",
		"uci set dhcp.lan." + LAN6NDPOursOption + "=1", // 标记：这条是我们写的
		"uci commit dhcp",
		"/etc/init.d/odhcpd reload",
	} {
		if !strings.Contains(set, want) {
			t.Errorf("配 NDP 代理时缺少 %q\n---\n%s", want, set)
		}
	}

	// ② 已经对了 ⇒ 一个字节都不写（每轮都 commit+reload 会把 odhcpd 抖个不停）
	done := []LAN6NDPSection{{Name: master, Iface: iface, Ignore: "1", Master: "1", NDP: "relay"}}
	if got := LAN6NDPCommands([]string{iface}, done, "relay", true); got != nil {
		t.Errorf("已经配好了不该重写，得到 %v", got)
	}
	// 别人的段只要配对了同样算"已满足"（省得跟用户手工配的那段打架、变成两段）
	if got := LAN6NDPCommands([]string{iface},
		[]LAN6NDPSection{{Name: iface, Iface: iface, Ignore: "1", Master: "1", NDP: "relay"}}, "relay", true); got != nil {
		t.Errorf("别名的段只要配对了也不该重写，得到 %v", got)
	}
	// hybrid 也算够用（odhcpd 两种都会做代理）
	if got := LAN6NDPCommands([]string{iface},
		[]LAN6NDPSection{{Name: master, Iface: iface, Ignore: "1", Master: "1", NDP: "hybrid"}}, "relay", true); got != nil {
		t.Errorf("hybrid 也算配好了，得到 %v", got)
	}
	// 段在、但被改坏了（少了 ndp）⇒ 要能自愈
	broken := joinCmds(LAN6NDPCommands([]string{iface},
		[]LAN6NDPSection{{Name: master, Iface: iface, Ignore: "1", Master: "1"}}, "relay", true))
	if !strings.Contains(broken, "uci set dhcp."+master+".ndp=relay") {
		t.Errorf("段被改坏后要能自愈\n---\n%s", broken)
	}

	// ③ 换了接口名（换模组/换 id）⇒ 收掉旧段、建新段
	chg := joinCmds(LAN6NDPCommands([]string{"modem2_6"},
		[]LAN6NDPSection{{Name: LAN6NDPMasterSection("modem0_6"), Iface: "modem0_6", Ignore: "1", Master: "1", NDP: "relay"}},
		"relay", true))
	if !strings.Contains(chg, "uci -q delete dhcp."+LAN6NDPMasterSection("modem0_6")) {
		t.Errorf("旧接口的段要收掉\n---\n%s", chg)
	}
	if !strings.Contains(chg, "uci set dhcp."+LAN6NDPMasterSection("modem2_6")+".master=1") {
		t.Errorf("新接口要挂上\n---\n%s", chg)
	}

	// ④ 模组不用了 ⇒ 收掉我们的段 + 只收回我们自己加的那条 lan.ndp
	del := joinCmds(LAN6NDPCommands(nil, done, "relay", true))
	if !strings.Contains(del, "uci -q delete dhcp."+master) {
		t.Errorf("不用了要收掉 master 段\n---\n%s", del)
	}
	if !strings.Contains(del, "uci -q delete dhcp.lan.ndp") {
		t.Errorf("要收回我们自己加的 lan.ndp\n---\n%s", del)
	}
	if strings.Contains(del, "delete dhcp.lan\n") || strings.Contains(del, "delete dhcp.lan ") {
		t.Errorf("绝不能删 dhcp.lan 段本身\n---\n%s", del)
	}
	if !strings.Contains(del, "uci -q delete dhcp.lan."+LAN6NDPOursOption) {
		t.Errorf("标记也要一起收掉\n---\n%s", del)
	}
	if !strings.Contains(del, "/etc/init.d/odhcpd reload") {
		t.Errorf("回收后也要让 odhcpd 重新装配\n---\n%s", del)
	}
	// 用户自己配的 ndp（没有我们的标记）⇒ 一个字都不动
	if got := LAN6NDPCommands(nil, nil, "relay", false); got != nil {
		t.Errorf("用户自己配的 ndp 不能删，得到 %v", got)
	}
}

// TestParseUCIShow `uci show` 输出的解析。
//
// ⚠️ 它跟 ParseUCIConfig **不是一个语法**：那个读的是配置文件原文（config/option/list 行）。
// 2026-10-07 就是用错了它，导致 ndp 对账把"已经配好"看成"没配过"——每 15 秒重写一次配置。
func TestParseUCIShow(t *testing.T) {
	const show = `dhcp.lan=dhcp
dhcp.lan.interface='lan'
dhcp.lan.ra_flags='managed-config' 'other-config'
dhcp.lan.ndp='relay'
network.lan=interface
network.lan.ip6assign='60'
dhcp.@dnsmasq[0]=dnsmasq
dhcp.@dnsmasq[0].domainneeded='1'
dhcp.wan=dhcp
dhcp.wan.interface="wan"
dhcp.wan.ignore='1'`

	secs := ParseUCIShow("dhcp", "dhcp", show)
	// ⚠️ `dhcp.@dnsmasq[0]=dnsmasq` 的**段类型是 dnsmasq**（只是在 dhcp 包里）⇒ 按类型过滤后只剩 lan / wan
	if len(secs) != 2 {
		t.Fatalf("应该只有 dhcp 包里的 2 个 dhcp 段（lan/wan），得到 %d 个：%+v", len(secs), secs)
	}
	if secs[0].Name != "lan" || secs[0].Options["interface"] != "lan" || secs[0].Options["ndp"] != "relay" {
		t.Errorf("lan 段解析错了：%+v", secs[0])
	}
	if secs[1].Options["interface"] != "wan" || secs[1].Options["ignore"] != "1" {
		t.Errorf("双引号的值没去掉：%+v", secs[1])
	}
	// 别的段类型（dnsmasq）与别的包（network）一根都不能混进来
	// （以前把 ParseUCIConfig 用在这里，结果连段都读不出来）
	for _, s := range secs {
		if s.Name == "@dnsmasq[0]" || s.Options["ip6assign"] != "" {
			t.Errorf("不该收进这个段：%+v", s)
		}
	}
	// 段声明之前出现的选项行要丢掉（还没有段可以挂上去）
	only := ParseUCIShow("dhcp", "dhcp", "dhcp.x.opt='1'\ndhcp.x=dhcp\n")
	if len(only) != 1 || only[0].Options["opt"] != "" {
		t.Errorf("段声明之前的选项行不该被收：%+v", only)
	}
	// 类型过滤
	if got := ParseUCIShow("dhcp", "host", show); len(got) != 0 {
		t.Errorf("只要 host 段时不该返回 %d 个", len(got))
	}
	// 包名写错/为空 ⇒ 一个都不给（宁可"没有"，也别把别人的配置读错）
	if got := ParseUCIShow("", "dhcp", show); got != nil {
		t.Errorf("包名为空应该什么都不返回，得到 %+v", got)
	}
}

// TestLAN6NDPSectionsFrom 从 `uci show dhcp` 里挑出与 NDP relay 有关的段。
//
// ⚠️ 这里带着 2026-10-07 那次真 bug 的回归护栏：**已经配好的段必须被认成"已满足"**，
// 否则 modemd 每轮都会重写一次 dhcp 配置 + reload odhcpd（真机上就是这样抖的）。
func TestLAN6NDPSectionsFrom(t *testing.T) {
	// 真机（H29K，0.2.232-1 跑完一轮后）的实际样子，删掉无关行
	const show = `dhcp.@dnsmasq[0]=dnsmasq
dhcp.@dnsmasq[0].domainneeded='1'
dhcp.lan=dhcp
dhcp.lan.interface='lan'
dhcp.lan.dhcpv6='server'
dhcp.lan.ra_flags='managed-config' 'other-config'
dhcp.lan.ndp='relay'
dhcp.lan.modemd='1'
dhcp.wan=dhcp
dhcp.wan.interface='wan'
dhcp.wan.ignore='1'
dhcp.modemd_ndp_modem0_6=dhcp
dhcp.modemd_ndp_modem0_6.interface='modem0_6'
dhcp.modemd_ndp_modem0_6.ignore='1'
dhcp.modemd_ndp_modem0_6.master='1'
dhcp.modemd_ndp_modem0_6.ndp='relay'`

	secs := lan6NDPSectionsFrom(show)
	var got []string
	for _, s := range secs {
		got = append(got, s.Name)
	}
	// lan（有 ndp）与 modemd_ndp_modem0_6（我们建的）要被收进来；@dnsmasq[0]/wan 不该进来
	if len(secs) != 2 {
		t.Fatalf("应该只挑出 lan 与 modemd_ndp_modem0_6，得到 %v", got)
	}
	if secs[0].Name != "lan" || secs[0].NDP != "relay" {
		t.Errorf("lan 段解析错了：%+v", secs[0])
	}
	m := secs[1]
	if m.Name != LAN6NDPMasterSection("modem0_6") || m.Iface != "modem0_6" ||
		m.Ignore != "1" || m.Master != "1" || m.NDP != "relay" {
		t.Errorf("我们的 master 段解析错了：%+v", m)
	}

	// ⭐ 回归护栏：这份"真机现状"喂给对账函数，必须**什么都不用做**
	if cmds := LAN6NDPCommands([]string{"modem0_6"}, secs, "relay", true); cmds != nil {
		t.Errorf("真机现在已经是配好的状态，不该再写任何东西（每轮重写会把 odhcpd 抖个不停），得到 %v", cmds)
	}
}

// TestPrefixOfV6Addr 模组报的单个地址 → 它所在的 /64（主机侧要不要"跟上"就是比这个前缀）
func TestPrefixOfV6Addr(t *testing.T) {
	cases := []struct{ in, want string }{
		{"240e:478:620:163a:18dc:9cc:9f91:8826", "240e:478:620:163a::/64"},
		{"240e:0478:0620:163a:18dc:09cc:9f91:8826", "240e:478:620:163a::/64"}, // 模组常带前导 0
		{"240e:478:620:163a::/64", "240e:478:620:163a::/64"},                  // 本来就是 CIDR
		{"fe80::1", ""},           // link-local 不算
		{"fd07:4d11:62a9::5", ""}, // ULA 不算
		{"", ""},
	}
	for _, c := range cases {
		if got := PrefixOfV6Addr(c.in); got != c.want {
			t.Errorf("PrefixOfV6Addr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestModemV6CID 挑出**带公网 IPv6**的那个 PDP 上下文。
//
// ⚠️ 这条是 2026-10-07 真机踩出来的坑：自动拨号落在 cid 3（只有 v4）上，而带 v6 的是 cid 1。
// 如果自愈逻辑只看"当前拨号的 cid"，就会去弹一个没有 v6 的上下文 —— 弹了也白弹，
// 用户那边就是"重拨之后 IPv6 再也没回来"。
func TestModemV6CID(t *testing.T) {
	// 真机（RM500U-CNV，2026-10-07 04:12）原样应答：cid 1 双栈、cid 3 只有 v4、cid 11(IMS) 前缀全 0
	real := []string{
		"AT+CGPADDR",
		`+CGPADDR: 1,"10.39.194.160","240e:0478:0620:163a:18dc:09cc:9f91:8826"`,
		`+CGPADDR: 3,"10.5.21.202","0000:0000:0000:0000:18dc:0959:716f:9bf4"`,
		`+CGPADDR: 11,"0000:0000:0000:0000:18dc:0959:75ea:3a33"`,
		"OK",
	}

	if cid, p := ModemV6CID(real, 3); cid != 1 || p != "240e:478:620:163a::/64" {
		t.Errorf("ModemV6CID(真机应答, prefer=3) = (%d,%q)，应该挑出带 v6 的 cid 1", cid, p)
	}
	if cid, p := ModemV6CID(real, 1); cid != 1 || p != "240e:478:620:163a::/64" {
		t.Errorf("prefer 自己有 v6 时就用它，得到 (%d,%q)", cid, p)
	}
	// 一台 v6-only 的上下文（IMS 那种只报一个字段）
	v6only := []string{`+CGPADDR: 5,"240e:5a::6666"`, "OK"}
	if cid, p := ModemV6CID(v6only, 1); cid != 5 || p != "240e:5a::/64" {
		t.Errorf("单字段的 v6 也要认，得到 (%d,%q)", cid, p)
	}
	// 全都没 v6 / 空应答 ⇒ 什么都不挑（千万别去弹一个没有 v6 的上下文）
	if cid, p := ModemV6CID([]string{`+CGPADDR: 3,"10.5.21.202"`, "OK"}, 3); cid != 0 || p != "" {
		t.Errorf("没有 v6 时不该挑出东西，得到 (%d,%q)", cid, p)
	}
	if cid, p := ModemV6CID(nil, 1); cid != 0 || p != "" {
		t.Errorf("空应答不该挑出东西，得到 (%d,%q)", cid, p)
	}
}

// TestHostV6FollowUp "主机侧 v6 没跟上模组"的看门狗决策。
//
// 背景（2026-10-07 真机定案，用户报「只要重新拨号就会出问题」）：模组换了 /64 之后**不会主动**
// 把新前缀再 RA 给主机，主机就一直挂在旧前缀上（上游已经不管那个前缀了）⇒ 连路由器自己都出不了
// v6。`renew` / `ifup` 都催不出来，只有**把承载 v6 的 PDP 停掉再激活**才行。
// 但它会把链路弹一下，所以必须有阈值/冷却/次数上限。
func TestHostV6FollowUp(t *testing.T) {
	// 刚发现不一致：先等模组自己补 RA（实测它有时会慢吞吞地补一次，1~2 分钟）
	if got := HostV6FollowUp(0, HostV6RetryAfter, 0); got != HostV6Nothing {
		t.Errorf("刚发现就对不上时不该立刻弹，得到 %v", got)
	}
	if got := HostV6FollowUp(HostV6StaleAfter-time.Second, HostV6RetryAfter, 0); got != HostV6Nothing {
		t.Errorf("还没到阈值不该弹，得到 %v", got)
	}
	// 持续对不上、且过了冷却 ⇒ 弹
	if got := HostV6FollowUp(HostV6StaleAfter, HostV6RetryAfter, 0); got != HostV6Bounce {
		t.Errorf("持续对不上、过了冷却就该弹，得到 %v", got)
	}
	// 刚弹过 ⇒ 等冷却（别连着弹）
	if got := HostV6FollowUp(10*time.Minute, HostV6RetryAfter-time.Second, 1); got != HostV6Nothing {
		t.Errorf("冷却期里不该再弹，得到 %v", got)
	}
	// 弹够了 ⇒ 收手报错（别一直把用户的网弹来弹去）
	if got := HostV6FollowUp(10*time.Minute, HostV6RetryAfter, MaxHostV6Bounces); got != HostV6GiveUp {
		t.Errorf("弹够次数后应该收手，得到 %v", got)
	}
	// 阈值与冷却的关系：冷却 ≥ 阈值，否则会在同一段时间里连弹
	if HostV6RetryAfter < HostV6StaleAfter {
		t.Fatalf("冷却(%v) 必须 ≥ 阈值(%v)", HostV6RetryAfter, HostV6StaleAfter)
	}
	if MaxHostV6Bounces < 1 {
		t.Fatalf("次数上限至少要能弹一次，现在 %d", MaxHostV6Bounces)
	}
}

// TestWANIPv6SwitchWiring 「模组侧 IPv6」开关（[net] wan_ipv6，页面上「拨号上网」那个开关）：
// 关掉之后四种拨号方式都只走 IPv4 ——
//
//	① ECM / NCM / RNDIS / **QMI**：不再另建 v6 兄弟接口 modem<id>_6（调用方会把已建的删掉）
//	② 各厂商的主机侧拨号器：不给它 -6（QMI 那条路）
//	③ MBIM：给 umbim 的 pdp_type 只写 ip
//
// 开着时各按本来的做法走。两个判据都走同一份配置，所以这里直接改 Cfg 再还原。
//
// ⚠️ 两个判据都**只看配置**（开关 + 协议），不看"模组此刻报没报 v6"：
// QMI 上模组侧一直 idle（数据呼叫归主机侧拨号器），拿那个当条件就永远建不出 v6 接口、
// 也永远不给拨号器 -6 —— 实测用户就是这么发现"开了 IPv6 但接口里什么都没有"的。
func TestWANIPv6SwitchWiring(t *testing.T) {
	defer func(v bool) { config.Cfg.Net.WANIPv6 = v }(config.Cfg.Net.WANIPv6)
	// ★ 2026-10-13：本用例断言的是**模式 dhcp/both**下 的接线 ——
	// v6 兄弟接口现在由 [net] wan_mode 决定（模式 1 modem_pro 不建它）。
	// 三种模式各自的完整行为见 netprov_mode_test.go 的 TestV6SiblingByMode。
	defer func(m string) { config.Cfg.Net.WANMode = m }(config.Cfg.Net.WANMode)
	config.Cfg.Net.WANMode = string(config.WANModeDHCP)

	config.Cfg.Net.WANIPv6 = true
	if !WANIPv6Enabled() {
		t.Error("开关开着时 WANIPv6Enabled() 该是 true")
	}
	// dhcp 类（ECM/NCM/RNDIS，以及 proto 也是 dhcp 的 QMI）都要建 v6 兄弟接口：
	// 拨号器只拉起数据链路，IPv6 地址得靠模组在链路上发 RA，而收 RA 需要
	// accept_ra=2 + 一张 dhcpv6 接口。少了它，「网络 → 接口」里 IPv6 一片空白。
	if !modemWAN6Allowed("dhcp") {
		t.Error("开关开着 + dhcp 类（含 QMI）：要建 v6 兄弟接口")
	}
	if modemWAN6Allowed("mbim") {
		t.Error("mbim 的 v6 由 netifd 的 umbim 在同一张接口上配，不该有兄弟接口")
	}
	if !hostDialWantIPv6() {
		t.Error("开关开着：拨号器要带 -6")
	}
	if got := strings.Join(hostDialArgsFor(VendorQuectel, "wwan0", "3gnet", hostDialWantIPv6(), 0), " "); got != "-i wwan0 -4 -6 -s 3gnet" {
		t.Errorf("移远 QMI：IPv6 开关开着要带 -4 -6，得到 %q", got)
	}
	// SIMCom 也认 -6（源码 getopt），开着时同样要带上；-4 绝不能传（它不认，会 usage 退出）
	if got := strings.Join(hostDialArgsFor(VendorSimcom, "wwan0", "3gnet", hostDialWantIPv6(), 0), " "); got != "-s 3gnet -6" {
		t.Errorf("开关开着：SIMCom 参数该是 %q，得到 %q", "-s 3gnet -6", got)
	}

	config.Cfg.Net.WANIPv6 = false
	if WANIPv6Enabled() {
		t.Error("开关关掉时 WANIPv6Enabled() 该是 false")
	}
	if modemWAN6Allowed("dhcp") {
		t.Error("开关关掉：任何协议都不该有 v6 兄弟接口（已建的要收掉）")
	}
	if hostDialWantIPv6() {
		t.Error("开关关掉：不该给拨号器带 -6")
	}
	if got := strings.Join(hostDialArgsFor(VendorQuectel, "wwan0", "3gnet", hostDialWantIPv6(), 0), " "); got != "-i wwan0 -4 -s 3gnet" {
		t.Errorf("开关关掉时移远参数该是纯 v4，得到 %q", got)
	}
	if got := strings.Join(hostDialArgsFor(VendorSimcom, "wwan0", "3gnet", hostDialWantIPv6(), 0), " "); got != "-s 3gnet" {
		t.Errorf("开关关掉时不带 -6，SIMCom 参数该是 %q，得到 %q", "-s 3gnet", got)
	}
	// MBIM：pdp_type 只写 ip（不然 umbim 还会去要 v6 承载）
	for _, in := range []string{"IPV4V6", "IPV6", "ipv4v6", "IP", ""} {
		if got := wanPlanPDPType(in); got != "ip" {
			t.Errorf("开关关掉时 wanPlanPDPType(%q) = %q, want ip", in, got)
		}
	}

	config.Cfg.Net.WANIPv6 = true
	for in, want := range map[string]string{"IPV4V6": "ipv4v6", "IPV6": "ipv6", "IP": "ip"} {
		if got := wanPlanPDPType(in); got != want {
			t.Errorf("开关开着时 wanPlanPDPType(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestQMAPMuxNetdev QMAP 模组的"数据网卡 ≠ 拨号器网卡"（2026-10-03 用户实测 RM502Q-GL 报的
// "quectel-CM 不能用"就是这条）：
//
//	wwan0    父口（QMAP 聚合链路，**没有 IP**）—— quectel-CM 的 `-i`、raw_ip 用它
//	wwan0_1  mux 口（数据口，udhcpc 拿到的地址在它上面）—— UCI 接口 / netifd DHCP / v6 用它
//
// 判错的后果：UCI 接口绑到 wwan0，netifd 的 udhcpc 在它上面永远拿不到租约（卡在 D 状态），
// 页面上看着就是"拨号器不能用/接口没 IP"。
func TestQMAPMuxNetdev(t *testing.T) {
	// netdev.IsQMAPMux：厂家的"父名_<muxid>"与主线的 qmimuxN 都算
	for _, yes := range []string{"wwan0_1", "wwan1_2", "qmimux0", "qmimux1", "WWAN0_1"} {
		if !netdev.IsQMAPMux(yes) {
			t.Errorf("netdev.IsQMAPMux(%q) = false, want true", yes)
		}
	}
	for _, no := range []string{"", "wwan0", "wwan", "usb0", "eth2", "br-lan", "wwan0_", "wwan0_x", "wwan0_1x"} {
		if netdev.IsQMAPMux(no) {
			t.Errorf("netdev.IsQMAPMux(%q) = true, want false", no)
		}
	}
	// 父名反推：只有"父名_<muxid>"这种能推；qmimuxN 推不出来（返回空，别瞎猜）
	if got := QMAPParentOf("wwan0_1"); got != "wwan0" {
		t.Errorf("QMAPParentOf(wwan0_1) = %q, want wwan0", got)
	}
	if got := QMAPParentOf("qmimux0"); got != "" {
		t.Errorf("QMAPParentOf(qmimux0) = %q, want 空串", got)
	}
	if got := QMAPParentOf("wwan0"); got != "" {
		t.Errorf("QMAPParentOf(wwan0) = %q, want 空串（它不是 mux）", got)
	}
	// 拨号器要的是父口：同类里挑非 mux 的那张
	if got := ModemDialNetdev([]string{"wwan0_1", "wwan0"}); got != "wwan0" {
		t.Errorf("ModemDialNetdev = %q, want wwan0（父口）", got)
	}
	if got := ModemDialNetdev([]string{"qmimux0", "wwan0"}); got != "wwan0" {
		t.Errorf("ModemDialNetdev = %q, want wwan0", got)
	}
	// 只有 qmimux（推不出父名）→ 空串（调用方就不传 -i，让拨号器自己认）
	if got := ModemDialNetdev([]string{"qmimux0"}); got != "" {
		t.Errorf("ModemDialNetdev = %q, want 空串", got)
	}
	// 普通模组就一张，原样返回
	if got := ModemDialNetdev([]string{"wwan0"}); got != "wwan0" {
		t.Errorf("ModemDialNetdev = %q, want wwan0", got)
	}
	// 排序：数据网卡（mux）必须排在父口前面（netdev.MatchNetdevToBusPath 用它）
	resolved := map[string]string{
		"wwan0":   "/sys/devices/platform/soc/usb2/2-1/2-1:1.4",
		"wwan0_1": "/sys/devices/platform/soc/usb2/2-1/2-1:1.4",
		"eth0":    "/sys/devices/platform/soc/eth0",
	}
	names := netdev.MatchNetdevToBusPath("2-1", resolved)
	if len(names) != 2 || names[0] != "wwan0_1" || names[1] != "wwan0" {
		t.Errorf("netdev.MatchNetdevToBusPath(2-1) = %v, want [wwan0_1 wwan0]（mux 优先）", names)
	}
	// ⚠️ busPath=2-1 不能匹配到 2-10（只做前缀匹配会张冠李戴）
	names = netdev.MatchNetdevToBusPath("2-1", map[string]string{"wwan9": "/sys/devices/usb/2-10/2-10:1.0"})
	if len(names) != 0 {
		t.Errorf("netdev.MatchNetdevToBusPath(2-1) 不该匹配 2-10，得到 %v", names)
	}
}

// TestIsManagedIface 只有"我们建的那套名字"才允许自动改绑（网卡名会变：eth2→eth3、wwan0→wwan1）
func TestIsManagedIface(t *testing.T) {
	for _, ok := range []string{"modem0", "modem1", "modem12", "modem0_6", "modem12_6"} {
		if !isManagedIface(ok) {
			t.Errorf("isManagedIface(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "modem", "modem_6", "modemx", "wan", "modem0_6x", "modem-1", "LAN", "modem0_66"} {
		if isManagedIface(bad) {
			t.Errorf("isManagedIface(%q) = true, want false", bad)
		}
	}
}

// TestRebindInterfaceCommands 改绑：dhcp 类写 device+ifname；qmi/mbim 只写 device（那是控制口）
func TestRebindInterfaceCommands(t *testing.T) {
	text := joinCmds(RebindInterfaceCommands("modem0", "eth3", false))
	for _, want := range []string{
		"uci set network.modem0.device=eth3",
		"uci set network.modem0.ifname=eth3",
		"uci commit network",
		"ifup modem0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("改绑命令缺少 %q\n---\n%s", want, text)
		}
	}
	qmi := joinCmds(RebindInterfaceCommands("modem0", "/dev/cdc-wdm1", true))
	if strings.Contains(qmi, "ifname") {
		t.Errorf("QMI/MBIM 改绑不该写 ifname（device 是控制口）：\n%s", qmi)
	}
	if !strings.Contains(qmi, "device=/dev/cdc-wdm1") {
		t.Errorf("QMI/MBIM 改绑要写控制口：\n%s", qmi)
	}
}

// TestParseManagedWANInterfaces 从 `uci show network` 里挑出"我们建的"接口（模组拔掉后要删它们）。
// 真实样本取自 RK3528（modem0 + modem0_6，都是我们建的），混着用户自己的 lan/wan 等干扰项。
func TestParseManagedWANInterfaces(t *testing.T) {
	show := `network.loopback=interface
network.loopback.proto='static'
network.lan=interface
network.lan.type='bridge'
network.lan.ip6assign='60'
network.wan=interface
network.wan.ifname='eth1'
network.modem0=interface
network.modem0.proto='dhcp'
network.modem0.device='eth2'
network.modem0.ifname='eth2'
network.modem0.metric='20'
network.modem0_6=interface
network.modem0_6.proto='dhcpv6'
network.modem0_6.device='eth2'
network.modem0_6.ip6prefix='240e:400:600:16e8::/64'
network.modemx=interface
network.modemx.proto='dhcp'
network.modem1_helper.proto='dhcp'
`
	got := ParseManagedWANInterfaces(show)
	if len(got) != 2 {
		t.Fatalf("只该认出 modem0 / modem0_6，实际 %+v", got)
	}
	if got[0].Name != "modem0" || got[0].Proto != "dhcp" || got[0].Device != "eth2" || got[0].ModemID != 0 {
		t.Errorf("modem0 解析不对：%+v", got[0])
	}
	if got[1].Name != "modem0_6" || got[1].Proto != "dhcpv6" || got[1].ModemID != 0 {
		t.Errorf("modem0_6 解析不对：%+v", got[1])
	}
	// 空输入 / 没有我们的接口都不该炸
	if n := len(ParseManagedWANInterfaces("")); n != 0 {
		t.Errorf("空输入应返回 0 条，实际 %d", n)
	}
	if n := len(ParseManagedWANInterfaces("network.lan=interface\nnetwork.lan.proto='static'\n")); n != 0 {
		t.Errorf("没有我们的接口时应返回 0 条，实际 %d", n)
	}
}

// TestModemIDFromIface 接口名 → 模组 id
func TestModemIDFromIface(t *testing.T) {
	for name, want := range map[string]int{"modem0": 0, "modem7": 7, "modem12": 12, "modem3_6": 3} {
		got, ok := modemIDFromIface(name)
		if !ok || got != want {
			t.Errorf("modemIDFromIface(%q) = %d/%v, want %d/true", name, got, ok, want)
		}
	}
	for _, bad := range []string{"", "modem", "modem_6", "modemx", "modem0_6x", "lan"} {
		if _, ok := modemIDFromIface(bad); ok {
			t.Errorf("modemIDFromIface(%q) 不该认出来", bad)
		}
	}
}

// TestForgetAutoWAN 模组拔掉后内存记账要清干净（同一个 id 以后可能被别的模组复用）
func TestForgetAutoWAN(t *testing.T) {
	setAutoWANInfo(42, AutoWANInfo{At: time.Now(), Iface: "modem42", Created: true})
	rememberGoodDialCID(42, 3)
	autoDialTried[42] = time.Now()
	forgetAutoWAN(42)
	if _, ok := AutoWANInfoFor(42); ok {
		t.Error("接口结果该被清掉")
	}
	if preferredDialCID(42) != 0 {
		t.Error("记住的 cid 该被清掉")
	}
	autoDialMu.Lock()
	_, tried := autoDialTried[42]
	autoDialMu.Unlock()
	if tried {
		t.Error("重试计时该被清掉")
	}
}

// TestValidNetdevName 网卡名白名单（写 /proc/sys 前挡路径穿越）
func TestValidNetdevName(t *testing.T) {
	for _, ok := range []string{"eth2", "wwan0", "usb0", "br-lan", "enp1s0.100", "eth_0"} {
		if !netprov.ValidNetdevName(ok) {
			t.Errorf("validNetdevName(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "../../etc/passwd", "eth2/../..", "eth 2", "eth2\n", "a b", "eth2;rm -rf /", "非常长的网卡名字超过十六个字符了"} {
		if netprov.ValidNetdevName(bad) {
			t.Errorf("validNetdevName(%q) = true, want false", bad)
		}
	}
}

// TestSetNetdevAcceptRA accept_ra 的读写（用假 /proc/sys，不动真内核参数）
func TestSetNetdevAcceptRA(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "net", "ipv6", "conf", "eth2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "accept_ra")
	if err := os.WriteFile(p, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := sysctlRoot
	sysctlRoot = root
	defer func() { sysctlRoot = old }()

	changed, err := setNetdevAcceptRA("eth2", 2)
	if err != nil || !changed {
		t.Fatalf("第一次应改成 2：changed=%v err=%v", changed, err)
	}
	if b, _ := os.ReadFile(p); strings.TrimSpace(string(b)) != "2" {
		t.Errorf("accept_ra 写后 = %q, want 2", strings.TrimSpace(string(b)))
	}
	// 已经是目标值 → 不动它（循环每 15 秒跑一次，别反复写）
	if changed, err := setNetdevAcceptRA("eth2", 2); err != nil || changed {
		t.Errorf("已经是 2 就该返回 changed=false, got %v/%v", changed, err)
	}
	// 不存在的网卡 → 报错，但不该 panic
	if _, err := setNetdevAcceptRA("eth9", 2); err == nil {
		t.Error("不存在的网卡应该报错")
	}
	// 非法名字直接挡掉（不拼路径出去）
	if _, err := setNetdevAcceptRA("../eth2", 2); err == nil {
		t.Error("非法网卡名应该报错")
	}
}

// TestAutoDialCandidates 自动拨号的候选 cid 顺序。
//
// 实测背景（RK3528 + 鼎桥 MT5700M-CN）：模组把 cid 0/1/2/3 都报成 active（APN 都是 3gnet），
// 但真发 AT+CGACT=1,1 会被 ERROR 拒掉，只有 **cid 3** 能真的激活 —— 所以得按候选一个个试，
// 并且把试成功的那条记住（下次先试它）。
func TestAutoDialCandidates(t *testing.T) {
	// 真机上报的那一组上下文
	ctxs := []Context{
		{CID: 0, APN: "3gnet"},
		{CID: 1, APN: "3gnet", Active: true},
		{CID: 2, APN: "3gnet", Active: true},
		{CID: 3, APN: "3gnet", Active: true},
		{CID: 5, APN: "ims", Active: true, Internal: true},
		{CID: 6, APN: ""},
		{CID: 8, APN: ""},
	}

	// 没记录过好 cid：已激活且非内部的按 cid 从大到小 → 3 先试
	got := AutoDialCandidates(ctxs, 0)
	if len(got) == 0 || got[0] != 3 {
		t.Errorf("首个候选应是 cid 3，得到 %v", got)
	}
	// 并且只包含有 APN 的非内部上下文（+ 兼底默认 cid），内外部的 ims / 无 APN 的不该出现
	for _, cid := range got {
		if cid == 5 || cid == 6 || cid == 8 {
			t.Errorf("候选里不该有 cid %d（内部上下文 / 没配 APN）：%v", cid, got)
		}
	}

	// 上次拨通的是 cid 2：下次先试 2
	got2 := AutoDialCandidates(ctxs, 2)
	if len(got2) == 0 || got2[0] != 2 {
		t.Errorf("记住的 cid 应排第一，得到 %v", got2)
	}
	// 同一 cid 不能重复出现
	seen := map[int]bool{}
	for _, cid := range got2 {
		if seen[cid] {
			t.Errorf("候选 cid %d 重复：%v", cid, got2)
		}
		seen[cid] = true
	}

	// 什么上下文都没有 → 只剩兼底默认 cid
	if got := AutoDialCandidates(nil, 0); len(got) != 1 || got[0] != DefaultDataCID {
		t.Errorf("空上下文应只剩默认 cid，得到 %v", got)
	}

	// APNForCID：取模组里配的 APN，找不到就是空串（拨号时留空 = 沿用模组配置）
	if apn := APNForCID(ctxs, 3); apn != "3gnet" {
		t.Errorf("APNForCID(3) = %q, want 3gnet", apn)
	}
	if apn := APNForCID(ctxs, 42); apn != "" {
		t.Errorf("APNForCID(42) = %q, want 空", apn)
	}
}

// TestRememberGoodDialCID 记住"哪条 cid 真的拨通过"
func TestRememberGoodDialCID(t *testing.T) {
	const id = 777
	if preferredDialCID(id) != 0 {
		t.Fatal("还没拨通过，不该有记录")
	}
	rememberGoodDialCID(id, 3)
	if got := preferredDialCID(id); got != 3 {
		t.Errorf("preferredDialCID = %d, want 3", got)
	}
}

func joinCmds(cmds [][]string) string {
	var b strings.Builder
	for _, c := range cmds {
		b.WriteString(netprov.WANCommandText(c))
		b.WriteString("\n")
	}
	return b.String()
}

// TestAutoWANInfoStore 结果记录：没跑过是 nil（页面上就不显示这块）
func TestAutoWANInfoStore(t *testing.T) {
	const id = 4242
	if _, ok := AutoWANInfoFor(id); ok {
		t.Fatal("还没记录过，不该有结果")
	}
	setAutoWANInfo(id, AutoWANInfo{At: time.Now(), Iface: "modem0", Device: "eth2", Created: true, Note: "已创建"})
	info, ok := AutoWANInfoFor(id)
	if !ok || !info.Created || info.Iface != "modem0" {
		t.Fatalf("记录读回不对：%+v ok=%v", info, ok)
	}
	if AutoWANPayload(id) == nil {
		t.Error("有结果时载荷不该是 nil")
	}
	if AutoWANPayload(id+1) != nil {
		t.Error("没结果的模组载荷应是 nil")
	}
}
