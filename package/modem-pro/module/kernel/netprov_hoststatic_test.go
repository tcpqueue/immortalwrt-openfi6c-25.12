//go:build !windows

package kernel

// ====================== 静态绑定类（FM350）纯逻辑用例 ======================
//
// 这一组用例钉的是 2026-10-13 真机（FM350-GL / 固件 81600.0000.00.29.24.02）实测口径：
//
//	AT+CGPADDR=3   → 10.181.85.121      ← 主机网卡要用的地址**本身**
//	AT+CGCONTRDP=3 → 218.2.2.2/218.4.4.4 ← DNS（本固件的 local/gw 两栏是空的）
//	掩码 /24，网关 = 前三段 + ".1"        ← 实测网关是 10.181.85.1，不是模组地址
//
// 任何一条判据被改坏，用例就红。

import (
	"modemd/module/config"
	"modemd/module/hostnic"
	"modemd/module/netprov"
	"strings"
	"testing"
)

// fm350StaticATI 真机 ATI 应答（摘录），用来验证"按型号判静态绑定"这条路
// （名字带 Static：cell_test.go 里已经有一个 fm350ATI，同一个包不能重名）
const fm350StaticATI = "ATI|Manufacturer: Fibocom Wireless Inc.|Model: FM350-GL|" +
	"Revision: 81600.0000.00.29.24.02|SVN: 10|IMEI: 867144030977985|OK"

func TestHostStaticNeeded(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"FM350-GL", true},
		{"Fibocom FM350-GL", true},
		{"FM350R-GL", true},
		{fm350StaticATI, true},
		{"FM160-CN", false},  // 高通平台，地址走 DHCP / MBIM，不该被静态绑定接管
		{"FM650-CN", false},  // 展锐平台，ECM/NCM 有 DHCP
		{"RM500Q-GL", false}, // 移远 QMI
		{"", false},
	}
	for _, c := range cases {
		if got := HostStaticNeeded(c.model); got != c.want {
			t.Errorf("HostStaticNeeded(%q) = %v, want %v", c.model, got, c.want)
		}
	}
	// 走 Modem 对象那条路（型号名/识别串都能命中）
	if !modNeedsHostStatic(&Modem{Model: fm350StaticATI}) {
		t.Error("modNeedsHostStatic(FM350 ATI) = false, want true")
	}
	if modNeedsHostStatic(&Modem{Model: "ATI|Model: FM160-CN|OK"}) {
		t.Error("modNeedsHostStatic(FM160) = true, want false（不能把别的机型也拖进静态路径）")
	}
	if modNeedsHostStatic(nil) {
		t.Error("modNeedsHostStatic(nil) = true, want false")
	}
}

func TestHostStaticGateway(t *testing.T) {
	cases := []struct{ ip, want string }{
		{"10.181.85.121", "10.181.85.1"},  // ★ 真机实测
		{"10.177.251.79", "10.177.251.1"}, // 另一张卡的实测值
		{"10.2.98.236", "10.2.98.1"},
		{"10.181.85.121/24", "10.181.85.1"}, // 带掩码也要认（CGPADDR 有固件带 /24）
		{" 10.181.85.121 ", "10.181.85.1"},
		{"192.168.1.1", "192.168.1.1"}, // 已经是 .1，结果还是 .1（幂等）
		{"77", ""},                     // 不是 IPv4
		{"", ""},
		{"not-an-ip", ""},
		{"240e:5a::6666", ""},               // IPv6 不走这条
		{"10.181.85.121.255.255.255.0", ""}, // 模组那种"地址+掩码"拼一格的写法：不是纯 IPv4
	}
	for _, c := range cases {
		if got := HostStaticGateway(c.ip); got != c.want {
			t.Errorf("HostStaticGateway(%q) = %q, want %q", c.ip, got, c.want)
		}
	}
}

func TestHostStaticPlanFor(t *testing.T) {
	p, ok := HostStaticPlanFor("modem0", "10.181.85.121", []string{"218.2.2.2", "218.4.4.4"})
	if !ok {
		t.Fatal("HostStaticPlanFor 应当成功")
	}
	if p.IP != "10.181.85.121" || p.Gateway != "10.181.85.1" || p.Mask != HostStaticMask {
		t.Errorf("plan = %+v，想要 10.181.85.121 / 255.255.255.0 / 10.181.85.1", p)
	}
	if len(p.DNS) != 2 || p.DNS[0] != "218.2.2.2" {
		t.Errorf("DNS = %v, want [218.2.2.2 218.4.4.4]", p.DNS)
	}

	// 没地址 / 地址不是 IPv4 → 一律不建（建了也是 pending）
	for _, ip := range []string{"", "0.0.0.0", "240e:5a::6666", "abc"} {
		if _, ok := HostStaticPlanFor("modem0", ip, nil); ok {
			t.Errorf("地址 %q 不该给出计划（静态绑定必须要有地址）", ip)
		}
	}
}

func TestHostStaticDNSFromCGCONTRDP(t *testing.T) {
	// 真机应答（FM350：local_addr / gw_addr 两栏是空的，DNS 是前两个 IP）
	fm350 := []string{
		"AT+CGCONTRDP=3",
		`+CGCONTRDP: 3,,"ctnet","","","218.2.2.2","218.4.4.4","","",0,,0,,,,,,,6,1,0,"1,0",0,65535`,
		"OK",
	}
	got := HostStaticDNSFromCGCONTRDP(fm350)
	if len(got) != 2 || got[0] != "218.2.2.2" || got[1] != "218.4.4.4" {
		t.Errorf("FM350 DNS = %v, want [218.2.2.2 218.4.4.4]", got)
	}

	// 标准 3GPP 写法（local_addr 是"地址+掩码"拼成一格，不带引号）
	std := []string{`+CGCONTRDP: 1,6,ctnet,10.194.190.206.255.0.0.0,,218.2.2.2,218.4.4.4,`}
	if got := HostStaticDNSFromCGCONTRDP(std); len(got) != 2 || got[0] != "218.2.2.2" {
		t.Errorf("标准写法 DNS = %v, want [218.2.2.2 218.4.4.4]", got)
	}

	// 只有 IPv6 DNS（FM350 的第二行就是这种十进制怪写法）→ 不能当成 IPv4 用
	v6 := []string{`+CGCONTRDP: 3,,"ctnet","","","36.14.0.90.0.0.0.0.0.0.0.0.0.0.102.102","36.14.0.91.0.0.0.0.0.0.0.0.0.0.102.102","","",0`}
	if got := HostStaticDNSFromCGCONTRDP(v6); len(got) != 0 {
		t.Errorf("v6 DNS 不该被当成 IPv4：%v", got)
	}

	// 没这一行（固件不认这条指令）→ nil，调用方退回"只有 IP"
	if got := HostStaticDNSFromCGCONTRDP([]string{"AT+CGCONTRDP=3", "ERROR"}); got != nil {
		t.Errorf("取不到应当是 nil，got %v", got)
	}
}

func TestCleanHostStaticDNS(t *testing.T) {
	got := CleanHostStaticDNS([]string{`"218.2.2.2"`, "218.2.2.2", " 218.4.4.4 ", "", "0.0.0.0", "240e:5a::6666", "garbage"})
	want := []string{"218.2.2.2", "218.4.4.4"}
	if len(got) != len(want) {
		t.Fatalf("= %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("= %v, want %v", got, want)
		}
	}
}

func TestHostStaticCommands(t *testing.T) {
	cmds := HostStaticCommands("modem0", "eth1",
		hostnic.HostNICPlan{Interface: "modem0", IP: "10.181.85.121", Mask: HostStaticMask,
			Gateway: "10.181.85.1", DNS: []string{"218.2.2.2", "218.4.4.4"}},
		[]string{"apn", "pdptype", "dhcp"})

	joined := make([]string, 0, len(cmds))
	for _, c := range cmds {
		joined = append(joined, strings.Join(c, " "))
	}
	all := strings.Join(joined, "\n")

	for _, want := range []string{
		"uci set network.modem0.proto=static",
		"uci set network.modem0.device=eth1",
		"uci set network.modem0.ipaddr=10.181.85.121",
		"uci set network.modem0.netmask=255.255.255.0",
		"uci set network.modem0.gateway=10.181.85.1",
		"uci add_list network.modem0.dns=218.2.2.2",
		"uci add_list network.modem0.dns=218.4.4.4",
		"uci commit network",
		"ifup modem0",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("命令里缺 %q\n实际：\n%s", want, all)
		}
	}
	// DNS 必须先清后写（否则旧运营商的 DNS 会留着）
	if !strings.Contains(all, "uci -q delete network.modem0.dns") {
		t.Errorf("DNS 没有先清：\n%s", all)
	}
	// 控制口时代的选项要清掉
	if !strings.Contains(all, "uci delete network.modem0.apn") {
		t.Errorf("没清掉控制口选项：\n%s", all)
	}
	// force_link：IPv6 拉起时要重起链路发 RS，不打开 netifd 会顺势把接口拉下来
	if !strings.Contains(all, "uci set network.modem0.force_link=1") {
		t.Errorf("没打开 force_link（链路重起时 netifd 会把接口拉下来）：\n%s", all)
	}
	// 没给掩码时补默认 /24
	cmds2 := HostStaticCommands("modem0", "eth1", hostnic.HostNICPlan{IP: "10.1.2.3", Gateway: "10.1.2.1"}, nil)
	found := false
	for _, c := range cmds2 {
		if strings.Join(c, " ") == "uci set network.modem0.netmask="+HostStaticMask {
			found = true
		}
	}
	if !found {
		t.Error("没给掩码时应当补 255.255.255.0")
	}
}

func TestHostStaticSame(t *testing.T) {
	base := hostnic.HostNICPlan{IP: "10.181.85.121", Mask: HostStaticMask, Gateway: "10.181.85.1",
		DNS: []string{"218.2.2.2", "218.4.4.4"}}
	// DNS 顺序不同算一样（LuCI 改 list 顺序是常事）
	same := hostnic.HostNICPlan{IP: "10.181.85.121", Mask: HostStaticMask, Gateway: "10.181.85.1",
		DNS: []string{"218.4.4.4", "218.2.2.2"}}
	if !HostStaticSame(base, same) {
		t.Error("DNS 顺序不同应当算一致（不该为此 ifup 一次）")
	}
	// 地址变了必须认出来（重拨换了 CGNAT 地址）
	moved := base
	moved.IP = "10.181.85.200"
	moved.Gateway = "10.181.85.1"
	if HostStaticSame(base, moved) {
		t.Error("地址变了却判成一致 ⇒ 接口会一直指着旧地址")
	}
	// 网关变了
	gw := base
	gw.Gateway = "10.181.85.99"
	if HostStaticSame(base, gw) {
		t.Error("网关变了却判成一致")
	}
	// DNS 少一个
	less := base
	less.DNS = []string{"218.2.2.2"}
	if HostStaticSame(base, less) {
		t.Error("DNS 数量不同却判成一致")
	}
	// 现有值全空（接口刚建/被清过）→ 必须算不一致，好让它写一遍
	if HostStaticSame(hostnic.HostNICPlan{}, base) {
		t.Error("接口上什么都没有时应当算不一致")
	}
}

func TestHostStaticFirstV4(t *testing.T) {
	// 真机应答：cid + IPv4 + IPv6 的**十进制怪写法**（后者绝不能被当成 IPv4）
	resp := []string{
		"AT+CGPADDR=3",
		`+CGPADDR: 3,"10.181.85.121","0.0.0.0.0.0.0.0.24.222.22.200.92.213.69.99"`,
		"OK",
	}
	if got := HostStaticFirstV4(resp); got != "10.181.85.121" {
		t.Errorf("= %q, want 10.181.85.121", got)
	}
	// 只有 IPv6（没地址的 cid）→ 空
	only6 := []string{`+CGPADDR: 1,"","240e:5a::6666"`}
	if got := HostStaticFirstV4(only6); got != "" {
		t.Errorf("只有 v6 时应为空，got %q", got)
	}
	// ERROR / 空应答
	if got := HostStaticFirstV4([]string{"AT+CGPADDR=2", "ERROR"}); got != "" {
		t.Errorf("ERROR 时应为空，got %q", got)
	}
}

func TestHostStaticV6(t *testing.T) {
	// 要不要拉起：只在"开了 v6 但还没全局地址"时动手
	if !HostStaticV6BringUpNeeded(true, false) {
		t.Error("开了 v6 且没有地址时应当拉起")
	}
	if HostStaticV6BringUpNeeded(true, true) {
		t.Error("已经有全局 v6 地址了就不该再动（否则每轮 ifdown/ifup 会把链路打断）")
	}
	if HostStaticV6BringUpNeeded(false, false) {
		t.Error("v6 开关关着时不该动")
	}

	// accept_ra 必须是 2：转发开着时 1 等于什么都不做（实测收不到 RA）
	cmds := HostStaticV6SysctlCommands("eth1")
	all := ""
	for _, c := range cmds {
		all += strings.Join(c, " ") + "\n"
	}
	if !strings.Contains(all, "net.ipv6.conf.eth1.accept_ra=2") {
		t.Errorf("accept_ra 必须是 2（转发开着时）：\n%s", all)
	}
	if !strings.Contains(all, "net.ipv6.conf.eth1.autoconf=1") {
		t.Errorf("少了 autoconf=1：\n%s", all)
	}
	// 空网卡名不发命令
	if got := HostStaticV6SysctlCommands("  "); got != nil {
		t.Errorf("空网卡名应当不发命令，got %v", got)
	}

	// 必须重起链路才发 RS（实测：不重起 25 秒 0 个 RA，重起后 10 秒内就有）
	cycle := HostStaticV6LinkCycleCommands("modem0", "eth1")
	joined := make([]string, 0, len(cycle))
	for _, c := range cycle {
		joined = append(joined, strings.Join(c, " "))
	}
	cy := strings.Join(joined, "\n")
	for _, want := range []string{"ifdown modem0", "ip link set dev eth1 down", "ip link set dev eth1 up", "ifup modem0"} {
		if !strings.Contains(cy, want) {
			t.Errorf("链路重起命令里缺 %q：\n%s", want, cy)
		}
	}

	// v6 默认路由进 UCI（手工 ip route 会被 netifd 重配时抹掉）
	r := HostStaticV6RouteCommands("modem0")
	ra := ""
	for _, c := range r {
		ra += strings.Join(c, " ") + "\n"
	}
	for _, want := range []string{
		"uci set network.modem0_v6=route6",
		"uci set network.modem0_v6.interface=modem0",
		"uci set network.modem0_v6.target=::/0",
		"uci set network.modem0_v6.gateway=" + HostStaticV6Gateway,
		"uci commit network",
	} {
		if !strings.Contains(ra, want) {
			t.Errorf("route6 命令里缺 %q：\n%s", want, ra)
		}
	}
	if HostStaticV6Gateway != "fe80::1" {
		t.Errorf("实测网关 = fe80::1（模组自己），got %q", HostStaticV6Gateway)
	}
}

func TestWANProtoForModemWAN(t *testing.T) {
	oldMode, oldDial := config.Cfg.Net.WANMode, config.Cfg.Net.HostDial
	defer func() { config.Cfg.Net.WANMode, config.Cfg.Net.HostDial = oldMode, oldDial }()

	fm350 := &Modem{Model: fm350StaticATI}
	fm160 := &Modem{Model: "ATI|Manufacturer: Fibocom Wireless Inc.|Model: FM160-CN|OK"}

	// FM350 三种模式、host_dial 开关都不影响：**一律 static**（模式与它无关）
	for _, mode := range []string{"modem_pro", "dhcp", "both"} {
		for _, dial := range []bool{true, false} {
			config.Cfg.Net.WANMode, config.Cfg.Net.HostDial = mode, dial
			if got := wanProtoForModemWAN(fm350, "rndis", true); got != WANProtoHostStatic {
				t.Errorf("FM350 mode=%s host_dial=%v ⇒ %q, want %q", mode, dial, got, WANProtoHostStatic)
			}
			// 其它机型一行都不能变。FM160 MBIM 的口径（见 netprov_proto.go）：
			//   host_dial 关 → mbim（只有 netifd 的 mbim.sh 会建会话）
			//   模式 1/3     → modem_pro
			//   模式 2       → dhcp
			var want160 string
			switch {
			case !dial:
				want160 = "mbim"
			case mode == "dhcp":
				want160 = "dhcp"
			default:
				want160 = netprov.ModemProtoName
			}
			if got := wanProtoForModemWAN(fm160, "mbim", true); got != want160 {
				t.Errorf("FM160 MBIM mode=%s host_dial=%v ⇒ %q, want %q（不能受 FM350 影响）",
					mode, dial, got, want160)
			}
		}
	}
}
