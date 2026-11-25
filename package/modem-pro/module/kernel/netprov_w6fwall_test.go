package kernel

import (
	"modemd/module/netprov"
	"testing"
)

// TestWAN6FirewallDevsFor 该不该给某张网卡写那套 v6 防火墙规则
// （DNS 重定向 + ip6tables MASQUERADE = NAT66）。
//
// ★★ 核心断言：**与 wan_mode 无关、也不要求接口名带 `_6`**。
// 这正是用户 2026-10-13 报的那个问题：RM500U 的 ECM 在 wan_mode=modem_pro
// （单接口，没有 modem<id>_6）下也必须拿到这套规则，否则内网没有 IPv6。
// 用户原话："能拿到 ipv6 是防火墙 没有像那个一样处理"。
func TestWAN6FirewallDevsFor(t *testing.T) {
	inList := func(id int) bool { return id == 0 }
	hasV6 := func(dev string) bool { return dev == "usb0" }
	always := func(int) bool { return true }
	anyV6 := func(string) bool { return true }
	never := func(int) bool { return false }
	noV6 := func(string) bool { return false }

	cases := []struct {
		name string
		ifs  []netprov.ManagedWANIface
		prof func(int) bool
		v6   func(string) bool
		want []string
	}{
		{
			name: "★模式1：只有 modem0（没有 _6），名单内 + 有 v6 ⇒ 要写",
			ifs:  []netprov.ManagedWANIface{{Name: "modem0", ModemID: 0, Device: "usb0"}},
			prof: inList, v6: hasV6,
			want: []string{"usb0"},
		},
		{
			name: "模式2：modem0 + modem0_6 绑同一张网卡 ⇒ 去重，只出一条",
			ifs: []netprov.ManagedWANIface{
				{Name: "modem0", ModemID: 0, Device: "usb0"},
				{Name: "modem0_6", ModemID: 0, Device: "usb0"},
			},
			prof: inList, v6: hasV6,
			want: []string{"usb0"},
		},
		{
			name: "不在名单里的型号（如 FM160-CN）⇒ 一条都不写",
			ifs:  []netprov.ManagedWANIface{{Name: "modem0", ModemID: 0, Device: "usb0"}},
			prof: never, v6: hasV6,
			want: nil,
		},
		{
			name: "还没拿到全局 IPv6 ⇒ 不写（写了也是摆设）",
			ifs:  []netprov.ManagedWANIface{{Name: "modem0", ModemID: 0, Device: "usb0"}},
			prof: inList, v6: noV6,
			want: nil,
		},
		{
			name: "没绑网卡（空 / 只有空格）⇒ 跳过",
			ifs: []netprov.ManagedWANIface{
				{Name: "modem0", ModemID: 0, Device: "  "},
				{Name: "modem1", ModemID: 1, Device: ""},
			},
			prof: always, v6: anyV6,
			want: nil,
		},
		{
			name: "两台模组各一张网卡 ⇒ 各出一条",
			ifs: []netprov.ManagedWANIface{
				{Name: "modem0", ModemID: 0, Device: "usb0"},
				{Name: "modem1", ModemID: 1, Device: "usb1"},
			},
			prof: always, v6: anyV6,
			want: []string{"usb0", "usb1"},
		},
		{
			name: "模组没在跑（inProfile=false）但接口段还在 ⇒ 不写",
			ifs:  []netprov.ManagedWANIface{{Name: "modem0", ModemID: 0, Device: "usb0"}},
			prof: never, v6: anyV6,
			want: nil,
		},
	}

	for _, c := range cases {
		got := netprov.Wan6FirewallDevsFor(c.ifs, c.prof, c.v6)
		if len(got) != len(c.want) {
			t.Errorf("%s：得到 %v，想要 %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s：得到 %v，想要 %v", c.name, got, c.want)
				break
			}
		}
	}
}

// TestWAN6FirewallKeepFor 什么时候**不该**把那套规则收掉。
//
// ★★ 核心断言：只要有**任意**模组接口（哪怕没有 `_6`）就保留。
// 老判据是"没有 modem<id>_6 就收掉"，而 wan_mode=modem_pro 下压根不建 `_6`，
// 于是删接口时会把规则一起收掉 —— 这就是 500U ECM 在 Modem-Pro 下 IPv6 不通的成因之一。
func TestWAN6FirewallKeepFor(t *testing.T) {
	cases := []struct {
		name string
		ifs  []netprov.ManagedWANIface
		want bool
	}{
		{
			name: "★模式1：只有 modem0（没有 _6）⇒ 必须保留",
			ifs:  []netprov.ManagedWANIface{{Name: "modem0", ModemID: 0, Device: "usb0"}},
			want: true,
		},
		{
			name: "模式2：modem0 + modem0_6 ⇒ 保留",
			ifs: []netprov.ManagedWANIface{
				{Name: "modem0", ModemID: 0, Device: "usb0"},
				{Name: "modem0_6", ModemID: 0, Device: "usb0"},
			},
			want: true,
		},
		{
			name: "模组拔了（一张受管接口都没有）⇒ 收掉",
			ifs:  nil,
			want: false,
		},
		{
			name: "有接口段但没绑网卡 ⇒ 收掉",
			ifs:  []netprov.ManagedWANIface{{Name: "modem0", ModemID: 0, Device: ""}},
			want: false,
		},
		{
			name: "两台模组，其中一台还在 ⇒ 保留",
			ifs: []netprov.ManagedWANIface{
				{Name: "modem0", ModemID: 0, Device: ""},
				{Name: "modem1", ModemID: 1, Device: "usb1"},
			},
			want: true,
		},
	}

	for _, c := range cases {
		if got := netprov.Wan6FirewallKeepFor(c.ifs); got != c.want {
			t.Errorf("%s：wan6FirewallKeepFor = %v，想要 %v", c.name, got, c.want)
		}
	}
}
