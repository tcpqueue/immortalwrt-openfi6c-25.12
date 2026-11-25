package kernel

import (
	"modemd/module/netprov"
	"testing"
)

// TestQMIWantProto QMI 接口 proto 的决策（**始终保留 dhcp**，不再降级 none）。
//
// 背景：2026-10-09 先是发现“链路上没有 DHCP 时 netifd 每秒重建接口把 CPU 打满”，
// 加了“自动降级 none”；随后用户明确要求去掉（none = netifd 不跑协议 = 上不了网），
// 这组用例锁住“默认就是 dhcp、不会再自己变成 none”这条口径。
func TestQMIWantProto(t *testing.T) {
	cases := []struct {
		name       string
		cfg        string
		cur        string
		modemProto bool
		want       string
	}{
		{"默认（未配）: dhcp 保持 dhcp", "", "dhcp", false, "dhcp"},
		{"auto: dhcp 保持 dhcp", "auto", "dhcp", false, "dhcp"},
		{"auto: 接口还不存在（cur 空）→ dhcp", "auto", "", false, "dhcp"},
		{"auto: 旧版遗留 none → 迁回 dhcp", "auto", "none", false, "dhcp"},
		{"auto: 旧版遗留 qmi → 迁回 dhcp", "auto", "qmi", false, "dhcp"},
		{"auto: 旧版遗留 quectel → 迁回 dhcp", "auto", "quectel", false, "dhcp"},
		{"显式 dhcp: 永远 dhcp", "dhcp", "dhcp", false, "dhcp"},
		{"显式 dhcp: 把 none 迁回 dhcp", "dhcp", "none", false, "dhcp"},
		{"显式 none: 直接 none（不跑协议）", "none", "dhcp", false, "none"},
		{"显式 none + 已经是 none：保持", "none", "none", false, "none"},
		{"大小写/空格容错", " NONE ", "dhcp", false, "none"},
		{"写错的值 → 按默认 dhcp", "banana", "dhcp", false, "dhcp"},
		// 装了 luci-proto-modem-pro（modemProto=true）：QMI 用合并接口 modem_pro
		{"装了合并协议: 默认 → modem_pro", "", "dhcp", true, "modem_pro"},
		{"装了合并协议: auto → modem_pro", "auto", "dhcp", true, "modem_pro"},
		{"装了合并协议: 接口还不存在 → modem_pro", "auto", "", true, "modem_pro"},
		{"装了合并协议: 老 dhcp 迁到 modem_pro", "auto", "dhcp", true, "modem_pro"},
		{"装了合并协议: 旧版遗留 qmi → modem_pro", "auto", "qmi", true, "modem_pro"},
		{"装了合并协议: 已是 modem_pro 保持", "auto", "modem_pro", true, "modem_pro"},
		{"装了合并协议但显式 none: 仍然 none", "none", "dhcp", true, "none"},
	}

	for _, c := range cases {
		got := netprov.QmiWantProto(c.cfg, c.cur, c.modemProto)
		if got != c.want {
			t.Errorf("%s: qmiWantProto(%q, %q, %v) = %q, want %q", c.name, c.cfg, c.cur, c.modemProto, got, c.want)
		}
	}
}

// TestQMIStuckCounter 计数器的累加/清零。
func TestQMIStuckCounter(t *testing.T) {
	const id = 42
	netprov.QmiStuckReset(id)
	if n := netprov.QmiStuckRoundsFor(id); n != 0 {
		t.Fatalf("清零后应为 0，得到 %d", n)
	}
	for i := 1; i <= 3; i++ {
		if n := netprov.QmiStuckBump(id); n != i {
			t.Fatalf("第 %d 次累加应为 %d，得到 %d", i, i, n)
		}
	}
	netprov.QmiStuckReset(id)
	if n := netprov.QmiStuckRoundsFor(id); n != 0 {
		t.Fatalf("reset 后应为 0，得到 %d", n)
	}
}
