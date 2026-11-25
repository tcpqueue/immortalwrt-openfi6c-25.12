package kernel

import (
	"modemd/module/netprov"
	"testing"
)

// TestLan6Candidates 内网 /64 交给谁：v6 挂在哪张接口上。
//
// ★★ 核心断言：模式 1（modem_pro 单接口）下要认**主接口** modem<id> ——
// 老代码只认 modem<id>_6，于是模式 1 下"内网 /64 路由 + odhcpd NDP 代理"
// 整个不会做，内网设备拿得到地址却出不了公网（用户 2026-10-13 实测）。
func TestLan6Candidates(t *testing.T) {
	m0 := netprov.ManagedWANIface{Name: "modem0", ModemID: 0, Device: "usb0"}
	m0_6 := netprov.ManagedWANIface{Name: "modem0_6", ModemID: 0, Device: "usb0"}
	m1 := netprov.ManagedWANIface{Name: "modem1", ModemID: 1, Device: "usb1"}
	other := netprov.ManagedWANIface{Name: "wan", ModemID: -1, Device: "eth1"}

	cases := []struct {
		name  string
		list  []netprov.ManagedWANIface
		want6 bool
		want  []string
	}{
		{
			name: "★模式1：只有 modem0 ⇒ 认它（不能因为不是 _6 就丢掉）",
			list: []netprov.ManagedWANIface{m0}, want6: false,
			want: []string{"modem0"},
		},
		{
			name: "模式1：modem0 + 无关的 wan ⇒ 只认 modem0",
			list: []netprov.ManagedWANIface{m0, other}, want6: false,
			want: []string{"modem0"},
		},
		{
			name: "模式2/3：只看 _6，主接口不参与",
			list: []netprov.ManagedWANIface{m0, m0_6}, want6: true,
			want: []string{"modem0_6"},
		},
		{
			name: "模式1：有多台模组 ⇒ 每台的主接口都认",
			list: []netprov.ManagedWANIface{m0, m1}, want6: false,
			want: []string{"modem0", "modem1"},
		},
		{
			name: "模式2/3：两台模组的 _6 都认",
			list: []netprov.ManagedWANIface{
				m0, m0_6,
				m1, {Name: "modem1_6", ModemID: 1, Device: "usb1"},
			}, want6: true,
			want: []string{"modem0_6", "modem1_6"},
		},
		{
			name: "模式1：没有主接口（只剩 _6）⇒ 空（等主接口建起来）",
			list: []netprov.ManagedWANIface{m0_6}, want6: false,
			want: nil,
		},
		{
			name: "非受管名字一律不要",
			list: []netprov.ManagedWANIface{other}, want6: false,
			want: nil,
		},
	}

	for _, c := range cases {
		got := lan6Candidates(c.list, c.want6)
		if len(got) != len(c.want) {
			t.Errorf("%s：得到 %d 个 %v，想要 %v", c.name, len(got), got, c.want)
			continue
		}
		for i := range got {
			if got[i].Name != c.want[i] {
				t.Errorf("%s：得到 %v，想要 %v", c.name, got, c.want)
				break
			}
		}
	}
}
