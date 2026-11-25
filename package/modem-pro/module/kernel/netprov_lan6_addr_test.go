//go:build !windows

package kernel

// ====================== LAN /64 地址"摆正"的纯逻辑用例 ======================
//
// 真机取证（2026-10-13 FM350 + 用户"电脑访问不了 ipv6"）：
//
//	7: br-lan  inet6 240e:400:628:1be::1/64 scope global **deprecated** dynamic
//	（LAN 上就这一条这个前缀的地址；客户端于是只有 ULA，出不了公网 v6）
//
// 修回来之后：客户端 30 秒内拿到 240e:400:628:1be:5437:…，proxy NDP 里出现它的地址。

import (
	"testing"
	"time"
)

func TestLAN6PrefixHextets(t *testing.T) {
	cases := []struct{ in, want string }{
		{"240e:400:628:1be::/64", "240e:400:628:1be"},
		{"240e:400:628:1be::1/64", "240e:400:628:1be"},
		{"240e:400:628:1be", "240e:400:628:1be"},
		{"240e:400:628:1be:5437:7589:87ed:f608", "240e:400:628:1be"},
		{"", ""},
		{"//64", ""},
	}
	for _, c := range cases {
		if got := LAN6PrefixHextets(c.in); got != c.want {
			t.Errorf("LAN6PrefixHextets(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// ULA / 链路本地只要**不与目标 /64 撞车**就行（它们的长度不同，不来参合）
	if got := LAN6PrefixHextets("fd48:dc61:5a6b::1/60"); got == LAN6PrefixHextets("240e:400:628:1be::/64") {
		t.Error("ULA 前缀不该被判成与 240e 那个 /64 是同一个")
	}
	if LAN6PrefixHextets("fe80::1/64") == "" {
		t.Error("fe80::1 应当能提出 hextet（至少不要报错/为空得莫名）")
	}
}

func TestLAN6PrefixAddrState(t *testing.T) {
	prefix := "240e:400:628:1be::/64"
	// ★ 真机那两行
	deprecatedLine := "7: br-lan    inet6 240e:400:628:1be::1/64 scope global deprecated dynamic \\    valid_lft 2400sec preferred_lft 0sec"
	normalLine := "7: br-lan    inet6 240e:400:628:1be::1/64 scope global \\    valid_lft forever preferred_lft forever"
	otherPrefix := "7: br-lan    inet6 fd48:dc61:5a6b::1/60 scope global noprefixroute \\    valid_lft forever"

	if p, d := LAN6PrefixAddrState([]string{deprecatedLine}, prefix); !p || !d {
		t.Errorf("deprecated 行应当 present=true deprecated=true，得到 %v/%v", p, d)
	}
	if p, d := LAN6PrefixAddrState([]string{normalLine}, prefix); !p || d {
		t.Errorf("正常行应当 present=true deprecated=false，得到 %v/%v", p, d)
	}
	if p, _ := LAN6PrefixAddrState([]string{otherPrefix}, prefix); p {
		t.Error("别的前缀（ULA）不算这个前缀的地址")
	}
	if p, d := LAN6PrefixAddrState(nil, prefix); p || d {
		t.Error("读不到任何行时应当 present=false")
	}
}

func TestLAN6PrefixAddrFixNeeded(t *testing.T) {
	prefix := "240e:400:628:1be::/64"
	// 没有这个前缀的地址 → 要修（真机上就是"被删了/没重建"那种）
	if !LAN6PrefixAddrFixNeeded([]string{"7: br-lan    inet6 fd48:dc61:5a6b::1/60 scope global"}, prefix) {
		t.Error("缺地址时必须判为要修")
	}
	// deprecated → 要修（这是用户那个"电脑 v6 不通"的直接原因）
	if !LAN6PrefixAddrFixNeeded([]string{"7: br-lan    inet6 240e:400:628:1be::1/64 scope global deprecated dynamic"}, prefix) {
		t.Error("deprecated 必须判为要修（否则内网设备不会 SLAAC 公网地址）")
	}
	// 正常 → 不要动（每轮都动会把接口打断）
	if LAN6PrefixAddrFixNeeded([]string{"7: br-lan    inet6 240e:400:628:1be::1/64 scope global"}, prefix) {
		t.Error("正常地址不该动手")
	}
	// 前缀为空（还没判出来）→ 不动手
	if LAN6PrefixAddrFixNeeded([]string{"7: br-lan    inet6 240e:400:628:1be::1/64 scope global"}, "") {
		t.Error("前缀为空时不该动手")
	}
}

func TestLAN6PrefixHostAddr(t *testing.T) {
	cases := []struct{ in, want string }{
		{"240e:400:628:1be::/64", "240e:400:628:1be::1/64"},
		{"240e:400:628:1be::", "240e:400:628:1be::1/64"},
		{"240e:400:628:1be", "240e:400:628:1be::1/64"},
		{"", ""},
		{"abc", ""},
	}
	for _, c := range cases {
		if got := LAN6PrefixHostAddr(c.in); got != c.want {
			t.Errorf("LAN6PrefixHostAddr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// 命令形态（真机上跑的就是这条）
	cmds := LAN6PrefixAddrFixCommands("br-lan", "240e:400:628:1be::/64")
	if len(cmds) != 1 {
		t.Fatalf("应当只有一条命令，得到 %d", len(cmds))
	}
	got := cmds[0][0] + " " + cmds[0][1] + " " + cmds[0][2] + " " + cmds[0][3] + " " + cmds[0][4] + " " + cmds[0][5] + " " + cmds[0][6]
	want := "ip -6 addr replace 240e:400:628:1be::1/64 dev br-lan"
	if got != want {
		t.Errorf("命令 = %q, want %q", got, want)
	}
	// 用 replace 不用 del：del 之后一旦重建失败，LAN 上就没这个前缀了（真机踩过）
	for _, c := range cmds[0] {
		if c == "del" {
			t.Error("不能用 del（会把 LAN 的前缀地址弄丢）")
		}
	}
	// 网卡名/前缀为空 → 不发命令
	if LAN6PrefixAddrFixCommands("", "240e:400:628:1be::/64") != nil {
		t.Error("网卡名为空不该发命令")
	}
	if LAN6PrefixAddrFixCommands("br-lan", "") != nil {
		t.Error("前缀为空不该发命令")
	}
}

func TestLAN6FirstGlobalPrefix(t *testing.T) {
	// 真机 WAN 侧（eth1）读到的两行：fe80 + 全局
	lines := []string{
		"9: eth1    inet6 fe80::200:11ff:fe12:1314/64 scope link",
		"9: eth1    inet6 240e:400:628:1be:200:11ff:fe12:1314/64 scope global dynamic mngtmpaddr",
	}
	if got := LAN6FirstGlobalPrefix(lines); got != "240e:400:628:1be::/64" {
		t.Errorf("= %q, want 240e:400:628:1be::/64（要跳过 fe80）", got)
	}
	// 只有 ULA → 不能拿它去配 LAN（出不了公网）
	if got := LAN6FirstGlobalPrefix([]string{"7: br-lan    inet6 fd48:dc61:5a6b::1/60 scope global"}); got != "" {
		t.Errorf("ULA 不该被当成运营商前缀，got %q", got)
	}
	// 只有链路本地
	if got := LAN6FirstGlobalPrefix([]string{"9: eth1    inet6 fe80::1/64 scope link"}); got != "" {
		t.Errorf("只有 fe80 时应当返回空，got %q", got)
	}
	if got := LAN6FirstGlobalPrefix(nil); got != "" {
		t.Errorf("空输入应该返回空，got %q", got)
	}
}

func TestLAN6AddrFixAllowed(t *testing.T) {
	lan6AddrFixMu.Lock()
	lan6AddrFixAt = time.Time{}
	lan6AddrFixMu.Unlock()

	t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	if !lan6AddrFixAllowed(t0) {
		t.Error("第一次应当允许")
	}
	if lan6AddrFixAllowed(t0.Add(10 * time.Second)) {
		t.Error("10 秒内不该再允许（会把接口反复动）")
	}
	if !lan6AddrFixAllowed(t0.Add(2 * lan6AddrFixThrottle)) {
		t.Error("超过限速间隔后应当允许")
	}
}
