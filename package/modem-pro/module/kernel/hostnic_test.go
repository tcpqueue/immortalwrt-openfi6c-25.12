package kernel

// ====================== 主机网卡（FM350 特例）：纯逻辑用例 ======================
//
// 机型特例判定 / 掩码换算 / 校验 / 建议值 / 命令生成 / 落盘都在这里测；
// 「真的执行命令」只做两条冒烟（echo + 不存在的程序），不去改本机网络（那是实机手工验证的事）。

import (
	"modemd/module/config"
	"modemd/module/hostnic"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 机型特例：只有 FM350 系列要手工配主机网卡
func TestHostNICQuirk(t *testing.T) {
	for _, m := range []string{"FM350-GL", "fm350", " FM350-GL ", "FM350"} {
		need, reason := hostnic.HostNICQuirk(m)
		if !need || reason == "" {
			t.Errorf("%q 应判为需要配主机网卡（理由也要给出来）", m)
		}
	}
	for _, m := range []string{"", "RM502Q-GL", "FM650-CN", "MT5700M-CN", "SIM8200EA-M2"} {
		if need, _ := hostnic.HostNICQuirk(m); need {
			t.Errorf("%q 不该判为需要配主机网卡（别的模块不用）", m)
		}
	}
	// 原始 ATI 串也要能判（没解析出短型号时走这条）
	if need, _ := hostnic.HostNICQuirk("ATI|Manufacturer: Fibocom|Model: FM350-GL|Revision: 81600|OK"); !need {
		t.Error("ATI 原文里带 FM350 也应判为特例")
	}
}

// 掩码 ↔ 前缀：两种写法都要认，非连续掩码要拒掉
func TestPrefixMask(t *testing.T) {
	ok := map[string]int{"255.255.255.0": 24, "24": 24, "255.255.0.0": 16, "255.255.255.252": 30, "0": 0, "255.255.255.255": 32}
	for in, want := range ok {
		got, good := hostnic.PrefixFromMask(in)
		if !good || got != want {
			t.Errorf("PrefixFromMask(%q) = %d,%v want %d", in, got, good, want)
		}
	}
	for _, bad := range []string{"", "33", "-1", "255.0.255.0", "1.2.3.4", "abc", "::1"} {
		if got, good := hostnic.PrefixFromMask(bad); good {
			t.Errorf("PrefixFromMask(%q) 不该通过，得到 %d", bad, got)
		}
	}
	if hostnic.MaskFromPrefix(24) != "255.255.255.0" || hostnic.MaskFromPrefix(30) != "255.255.255.252" {
		t.Error("MaskFromPrefix 结果不对")
	}
	if hostnic.MaskFromPrefix(33) != "" {
		t.Error("非法前缀应返回空串")
	}
}

// 校验：半套配置比没有更糟 —— 缺网卡 / 坏 IP / 坏掩码 / 坏网关都要挡住
func TestValidateHostNICPlan(t *testing.T) {
	good := hostnic.HostNICPlan{Interface: "以太网 3", IP: "192.168.8.2", Mask: "255.255.255.0", Gateway: "192.168.8.1", DNS: []string{"192.168.8.1", "8.8.8.8"}}
	if err := hostnic.ValidateHostNICPlan(good); err != nil {
		t.Fatalf("合法配置被拒：%v", err)
	}
	bad := []hostnic.HostNICPlan{
		{IP: "192.168.8.2", Mask: "24"},                                                // 没选网卡
		{Interface: "eth1", Mask: "24"},                                                // 没 IP
		{Interface: "eth1", IP: "not-an-ip", Mask: "24"},                               // IP 非法
		{Interface: "eth1", IP: "192.168.8.2", Mask: "255.0.255.0"},                    // 非连续掩码
		{Interface: "eth1", IP: "192.168.8.2", Mask: "24", Gateway: "x"},               // 网关非法
		{Interface: "eth1", IP: "192.168.8.2", Mask: "24", DNS: []string{"1.1.1.256"}}, // DNS 非法
	}
	for i, p := range bad {
		if err := hostnic.ValidateHostNICPlan(p); err == nil {
			t.Errorf("第 %d 组非法配置应被拒：%+v", i, p)
		}
	}
}

// 归一化：掩码统一成点分、DNS 去空去重、顺手去空格
func TestNormalizeHostNICPlan(t *testing.T) {
	got := hostnic.NormalizeHostNICPlan(hostnic.HostNICPlan{
		Interface: " 以太网 3 ", IP: " 192.168.8.2 ", Mask: "24 ",
		Gateway: " 192.168.8.1 ", DNS: []string{" 8.8.8.8", "", "8.8.8.8", "1.1.1.1 "},
	})
	if got.Interface != "以太网 3" || got.IP != "192.168.8.2" || got.Mask != "255.255.255.0" || got.Gateway != "192.168.8.1" {
		t.Errorf("归一化结果不对：%+v", got)
	}
	if len(got.DNS) != 2 || got.DNS[0] != "8.8.8.8" || got.DNS[1] != "1.1.1.1" {
		t.Errorf("DNS 应去空去重：%+v", got.DNS)
	}
	if err := hostnic.ValidateHostNICPlan(got); err != nil {
		t.Errorf("归一化后的配置应合法：%v", err)
	}
	// 只写前缀长度也要能归一成点分（命令里统一用点分掩码）
	if m := hostnic.NormalizeHostNICPlan(hostnic.HostNICPlan{Mask: "30"}).Mask; m != "255.255.255.252" {
		t.Errorf("掩码 30 → %q", m)
	}
}

// 建议值：主机取同网段 .2、网关指向模组本身（"IP 对应"的字面做法，仅供参考）
func TestSuggestHostNIC(t *testing.T) {
	p, ok := hostnic.SuggestHostNIC("10.178.25.161")
	if !ok {
		t.Fatal("合法 IP 应能推建议值")
	}
	if p.IP != "10.178.25.2" || p.Mask != "255.255.255.0" || p.Gateway != "10.178.25.161" {
		t.Errorf("建议值 = %+v", p)
	}
	// 模组地址恰好是 .2 时避开自己（.0/.1 是网络号与常见网关，也不用）
	if p, _ := hostnic.SuggestHostNIC("10.0.0.2"); p.IP != "10.0.0.3" {
		t.Errorf("模组是 .2 时主机应取 .3，得到 %q", p.IP)
	}
	for _, bad := range []string{"", "not-ip", "240e::1"} {
		if _, ok := hostnic.SuggestHostNIC(bad); ok {
			t.Errorf("%q 不该推出建议值", bad)
		}
	}
}

// Windows 命令：netsh 的写法与参数要能直接拿去执行（argv 与显示串一致）
func TestHostNICCommandsWindows(t *testing.T) {
	plan := hostnic.HostNICPlan{Interface: "以太网 3", IP: "192.168.8.2", Mask: "24", Gateway: "192.168.8.1", DNS: []string{"192.168.8.1", "8.8.8.8"}}

	static := hostnic.HostNICCommands(hostnic.HostNICStatic, plan, "windows")
	if len(static) != 3 { // 地址 + 主 DNS + 次 DNS
		t.Fatalf("static 命令条数 = %d, want 3", len(static))
	}
	c0 := static[0]
	if c0.Prog != "netsh" || strings.Join(c0.Args, " ") != `interface ip set address name=以太网 3 static 192.168.8.2 255.255.255.0 192.168.8.1` {
		t.Errorf("static argv = %s %v", c0.Prog, c0.Args)
	}
	// 显示串里 netsh 的 name= 要写成 name="x"（复制到 cmd 里才不会把空格当分隔）
	if !strings.Contains(c0.Text, `name="以太网 3"`) || !strings.Contains(c0.Text, "192.168.8.2 255.255.255.0 192.168.8.1") {
		t.Errorf("static 显示串 = %q", c0.Text)
	}
	if !c0.AdminOnly {
		t.Error("改网卡的命令要标出需要管理员权限")
	}
	if static[2].Args[len(static[2].Args)-1] != "index=2" {
		t.Errorf("第二个 DNS 应带 index=2，得到 %v", static[2].Args)
	}

	dhcp := hostnic.HostNICCommands(hostnic.HostNICDhcp, plan, "windows")
	if len(dhcp) != 2 || !strings.HasSuffix(dhcp[0].Args[len(dhcp[0].Args)-1], "source=dhcp") {
		t.Errorf("恢复 DHCP 应有两条且第一条以 source=dhcp 结尾：%+v", dhcp)
	}
	renew := hostnic.HostNICCommands(hostnic.HostNICRenew, plan, "windows")
	if len(renew) != 1 || renew[0].Prog != "ipconfig" || renew[0].Args[0] != "/renew" {
		t.Errorf("renew 应是 ipconfig /renew：%+v", renew)
	}
	// 没给网关时不该拼出空参数
	noGw := hostnic.HostNICCommands(hostnic.HostNICStatic, hostnic.HostNICPlan{Interface: "eth1", IP: "10.0.0.2", Mask: "24"}, "windows")
	if strings.Contains(noGw[0].Text, "static 10.0.0.2 255.255.255.0 ") {
		t.Errorf("没有网关时不该留空尾巴：%q", noGw[0].Text)
	}
}

// Linux / OpenWrt 命令：清旧地址 → 配地址 → 上线 → 默认路由（+ DNS）
func TestHostNICCommandsLinux(t *testing.T) {
	plan := hostnic.HostNICPlan{Interface: "eth1", IP: "192.168.8.2", Mask: "24", Gateway: "192.168.8.1", DNS: []string{"192.168.8.1"}}
	static := hostnic.HostNICCommands(hostnic.HostNICStatic, plan, "linux")
	if len(static) != 5 { // flush + addr add + link up + route + DNS
		t.Fatalf("static 条数 = %d, want 5", len(static))
	}
	if !strings.Contains(static[0].Text, "ip addr flush dev eth1") {
		t.Errorf("第一条应是清旧地址：%q", static[0].Text)
	}
	if !strings.Contains(static[1].Text, "ip addr add 192.168.8.2/24 dev eth1") {
		t.Errorf("第二条应是配地址（带前缀长度）：%q", static[1].Text)
	}
	if !strings.Contains(static[3].Text, "ip route replace default via 192.168.8.1 dev eth1") {
		t.Errorf("第四条应是默认路由（用 replace，重复执行不报已有路由）：%q", static[3].Text)
	}
	// 普通 Linux 上 DNS 还是写 resolv.conf；OpenWrt 不算（它由 dnsmasq 管，见下面的用例）
	if len(static) != 5 || !strings.Contains(static[4].Text, "/etc/resolv.conf") {
		t.Errorf("第五条应是 resolv.conf 里的 DNS：%+v", static)
	}
	// 前缀也能从点分掩码换算出来
	plan.Mask = "255.255.255.0"
	if got := hostnic.HostNICCommands(hostnic.HostNICStatic, plan, "linux")[1].Text; !strings.Contains(got, "/24") {
		t.Errorf("点分掩码也要能换成 /24：%q", got)
	}
	dhcp := hostnic.HostNICCommands(hostnic.HostNICDhcp, plan, "linux")
	if len(dhcp) != 2 || dhcp[0].Prog != "ip" || dhcp[1].Prog != "udhcpc" {
		t.Errorf("恢复 DHCP 应是 flush + udhcpc：%+v", dhcp)
	}
	// 未知平台不乱编命令
	if got := hostnic.HostNICCommands(hostnic.HostNICStatic, plan, "plan9"); len(got) != 0 {
		t.Errorf("未知平台应返回空命令，得到 %+v", got)
	}
}

// OpenWrt：命令形态与普通 Linux 一样，差别在
// ① 默认路由用 replace（netifd 管的表里可能已经有 default）
// ② DNS 不改 resolv.conf（OpenWrt 由 dnsmasq 管），改由下面的 UCI 块承载
func TestHostNICCommandsOpenWrt(t *testing.T) {
	plan := hostnic.HostNICPlan{Interface: "usb0", IP: "192.168.8.2", Mask: "24", Gateway: "192.168.8.1", DNS: []string{"192.168.8.1"}}
	static := hostnic.HostNICCommands(hostnic.HostNICStatic, plan, "openwrt")
	if len(static) != 4 { // flush + addr add + link up + route（没有 resolv.conf 那条）
		t.Fatalf("openwrt static 条数 = %d, want 4：%+v", len(static), static)
	}
	for _, c := range static {
		if strings.Contains(c.Text, "resolv.conf") {
			t.Errorf("OpenWrt 不该改 resolv.conf：%q", c.Text)
		}
	}
	if !strings.Contains(static[3].Text, "ip route replace default via 192.168.8.1 dev usb0") {
		t.Errorf("默认路由：%q", static[3].Text)
	}
	// busybox 自带 udhcpc，路由器上「先试 DHCP」照旧可用
	renew := hostnic.HostNICCommands(hostnic.HostNICRenew, plan, "openwrt")
	if len(renew) != 1 || renew[0].Prog != "udhcpc" {
		t.Errorf("OpenWrt 的 renew 应是 udhcpc：%+v", renew)
	}
	// 恢复自动获取也照旧（flush + udhcpc）
	dhcp := hostnic.HostNICCommands(hostnic.HostNICDhcp, plan, "openwrt")
	if len(dhcp) != 2 || dhcp[1].Prog != "udhcpc" {
		t.Errorf("OpenWrt 的 dhcp 应是 flush + udhcpc：%+v", dhcp)
	}
}

// OpenWrt 持久化：写 UCI（**只显示不执行** —— 改的是固件自己的网络配置，要人核对）
func TestHostNICPersistCommands(t *testing.T) {
	plan := hostnic.HostNICPlan{Interface: "usb0", IP: "192.168.8.2", Mask: "24", Gateway: "192.168.8.1", DNS: []string{"192.168.8.1", "8.8.8.8"}}
	// 非 OpenWrt 不出这一段
	for _, goos := range []string{"linux", "windows", "darwin"} {
		if got := hostnic.HostNICPersistCommands(plan, goos); got != nil {
			t.Errorf("%s 不该有 UCI 持久化块：%+v", goos, got)
		}
	}

	set := hostnic.HostNICPersistCommands(plan, "openwrt")
	if len(set) == 0 {
		t.Fatal("OpenWrt 应给出 UCI 持久化命令")
	}
	all := ""
	for _, c := range set {
		all += c.Text + "\n"
		if !c.Manual || c.Prog != "" {
			t.Errorf("持久化命令只该显示不该执行：%+v", c)
		}
	}
	for _, want := range []string{
		"uci -q delete network.modem",
		"uci set network.modem=interface",
		"uci set network.modem.proto=static",
		"uci set network.modem.device=usb0",
		"uci set network.modem.ipaddr=192.168.8.2",
		"uci set network.modem.netmask=255.255.255.0",
		"uci set network.modem.gateway=192.168.8.1",
		"uci set network.modem.dns=192.168.8.1",
		"uci set network.modem.dns=8.8.8.8",
		"uci commit network",
		"/etc/init.d/network reload",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("UCI 块里缺 %q：\n%s", want, all)
		}
	}

	// 掩码只写前缀长度时也要归一成点分（uci 要的是点分掩码）
	joined := ""
	for _, c := range hostnic.HostNICPersistCommands(hostnic.HostNICPlan{Interface: "eth1", IP: "10.0.0.2", Mask: "24"}, "openwrt") {
		joined += c.Text + "\n"
	}
	if !strings.Contains(joined, "netmask=255.255.255.0") {
		t.Errorf("掩码应归一成点分：\n%s", joined)
	}
	// 没填网关 / DNS 时不拼空值
	joined = ""
	for _, c := range hostnic.HostNICPersistCommands(hostnic.HostNICPlan{Interface: "eth1", IP: "10.0.0.2", Mask: "24"}, "openwrt") {
		joined += c.Text + "\n"
	}
	if strings.Contains(joined, "gateway=") || strings.Contains(joined, "dns=") {
		t.Errorf("没填网关/DNS 不该拼出来：\n%s", joined)
	}
}

// 只显示不执行的命令绝不能被代跑（即便带着 Prog 也拦下）
func TestRunHostNICCommandsNeverRunsManual(t *testing.T) {
	prog, args := "sh", []string{"-c", "echo should-not-run"}
	if runtime.GOOS == "windows" {
		prog, args = "cmd", []string{"/c", "echo should-not-run"}
	}
	got := RunHostNICCommands([]hostnic.HostNICCommand{{Label: "UCI", Text: "uci commit network", Manual: true, Prog: prog, Args: args}})
	if len(got) != 1 || got[0].OK || !strings.Contains(got[0].Error, "只显示不执行") {
		t.Errorf("Manual 命令不该被执行：%+v", got)
	}
}

// 平台标识：本机是 windows/linux；真跑在 OpenWrt 上才会是 openwrt
func TestHostNICPlatform(t *testing.T) {
	got := HostNICPlatform()
	switch got {
	case "windows", "linux", "openwrt", "darwin":
	default:
		t.Errorf("平台标识意外：%q", got)
	}
	if hostnic.IsOpenWrt() && got != "openwrt" {
		t.Errorf("IsOpenWrt() = true 时平台标识应是 openwrt，得到 %q", got)
	}
}

// 三组动作：顺序与提示语固定（页面按这个渲染按钮）
func TestHostNICActionSets(t *testing.T) {
	sets := hostnic.HostNICActionSets(hostnic.HostNICPlan{Interface: "eth1", IP: "10.0.0.2", Mask: "24"}, runtime.GOOS)
	if len(sets) != 3 {
		t.Fatalf("动作组数 = %d, want 3", len(sets))
	}
	want := []string{hostnic.HostNICRenew, hostnic.HostNICStatic, hostnic.HostNICDhcp}
	for i, s := range sets {
		if s.Mode != want[i] || s.Label == "" || s.Hint == "" || len(s.Commands) == 0 {
			t.Errorf("第 %d 组不完整：%+v", i, s)
		}
	}
}

// 落盘：保存后能读回，进程重启（重置内存缓存）后仍在
func TestHostNICStore(t *testing.T) {
	dir := t.TempDir()
	old := config.ConfigPath
	config.ConfigPath = filepath.Join(dir, "config.toml")
	defer func() { config.ConfigPath = old }()
	resetHostNICStore()

	plan := hostnic.HostNICPlan{Interface: "以太网 3", IP: "192.168.8.2", Mask: "24", Gateway: "192.168.8.1"}
	if _, err := hostnic.SaveHostNICPlan(plan); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hostnic.json")); err != nil {
		t.Fatalf("应落在配置文件同目录：%v", err)
	}
	got, ok := hostnic.GetHostNICPlan("以太网 3")
	if !ok || got.IP != "192.168.8.2" || got.Mask != "255.255.255.0" {
		t.Errorf("读回 = %+v (ok=%v)", got, ok)
	}
	if names := hostnic.HostNICPlanNames(); len(names) != 1 || names[0] != "以太网 3" {
		t.Errorf("已保存网卡列表 = %v", names)
	}
	// 校验不过的不许落盘（否则下次生成半套命令）
	if _, err := hostnic.SaveHostNICPlan(hostnic.HostNICPlan{Interface: "eth1", IP: "bad", Mask: "24"}); err == nil {
		t.Error("非法配置不该保存成功")
	}
	// 模拟进程重启：清内存缓存后再读
	resetHostNICStore()
	if got, ok := hostnic.GetHostNICPlan("以太网 3"); !ok || got.Gateway != "192.168.8.1" {
		t.Errorf("重启后应还能读回：%+v (ok=%v)", got, ok)
	}
	if p := hostnic.HostNICPath("/etc/modemd/config.toml"); p != filepath.Join("/etc/modemd", "hostnic.json") {
		t.Errorf("HostNICPath = %q", p)
	}
	resetHostNICStore()
}

// resetHostNICStore 清掉内存缓存，等价于"进程刚起来还没读盘"
func resetHostNICStore() {
	hostnic.HostNICMu.Lock()
	hostnic.HostNICLoaded = false
	hostnic.HostNICPlans = map[string]hostnic.HostNICPlan{}
	hostnic.HostNICMu.Unlock()
}

// 执行：只做两条冒烟（真跑命令的路径要能走通，成功/失败都要如实回传）
func TestRunHostNICCommands(t *testing.T) {
	prog, args := "sh", []string{"-c", "echo hostnic-ok"}
	if runtime.GOOS == "windows" {
		prog, args = "cmd", []string{"/c", "echo hostnic-ok"}
	}
	cmds := []hostnic.HostNICCommand{
		{Label: "冒烟", Text: prog + " " + strings.Join(args, " "), Prog: prog, Args: args},
		{Label: "不存在的程序", Text: "no-such-prog-xyz", Prog: "no-such-prog-xyz", Args: []string{"x"}},
	}
	got := RunHostNICCommands(cmds)
	if len(got) != 2 {
		t.Fatalf("结果条数 = %d, want 2", len(got))
	}
	if !got[0].OK || !strings.Contains(got[0].Output, "hostnic-ok") {
		t.Errorf("第一条应成功并带回输出：%+v", got[0])
	}
	if got[1].OK || got[1].Error == "" {
		t.Errorf("第二条应失败并带回错误：%+v", got[1])
	}
}

// apply 的入参校验：mode 非法、静态配置缺参数都要在生成命令前挡住
func TestHostNICApply(t *testing.T) {
	plan := hostnic.HostNICPlan{Interface: "eth1", IP: "10.0.0.2", Mask: "24", Gateway: "10.0.0.1"}
	if _, err := HostNICApply("bogus", plan, false); err == nil {
		t.Error("非法 mode 应报错")
	}
	if _, err := HostNICApply(hostnic.HostNICStatic, hostnic.HostNICPlan{Interface: "eth1", Mask: "24"}, false); err == nil {
		t.Error("静态配置缺 IP 应报错")
	}
	if _, err := HostNICApply(hostnic.HostNICRenew, hostnic.HostNICPlan{}, false); err == nil {
		t.Error("renew 也得先选网卡")
	}
	res, err := HostNICApply(hostnic.HostNICStatic, plan, false)
	if err != nil {
		t.Fatalf("合法请求不该报错：%v", err)
	}
	if res.Executed || len(res.Commands) == 0 || res.Label == "" {
		t.Errorf("默认只生成命令不执行：%+v", res)
	}
}
