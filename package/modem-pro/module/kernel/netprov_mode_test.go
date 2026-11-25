package kernel

// ============ [net] wan_mode 三种模式的行为（用户 2026-10-13 要求） ============
//
// 这个文件只测**纯决策**：模式怎么解析、每种模式下主接口用哪个 proto、
// 要不要建 v6 兄弟接口。不碰 UCI、不发 AT。

import (
	"modemd/module/config"
	"modemd/module/netprov"
	"testing"
)

// withWANMode 在用例内临时切换模式（并恢复原值 + 顺带恢复 modem_pro 可用性探针）。
func withWANMode(t *testing.T, m config.WANMode) {
	t.Helper()
	old := config.Cfg.Net.WANMode
	config.Cfg.Net.WANMode = string(m)
	t.Cleanup(func() { config.Cfg.Net.WANMode = old })
}

// TestParseWANMode 三种模式 + 别名 + 认不出的值全都要落到默认。
func TestParseWANMode(t *testing.T) {
	cases := []struct {
		in   string
		want config.WANMode
	}{
		{"", config.WANModeModemPro},          // 没写 → 默认
		{"   ", config.WANModeModemPro},       // 空白 → 默认
		{"modem_pro", config.WANModeModemPro}, //
		{"Modem_Pro", config.WANModeModemPro}, // 大小写不敏感
		{"modem-pro", config.WANModeModemPro}, // 别名
		{"1", config.WANModeModemPro},         // 界面上的序号
		{"dhcp", config.WANModeDHCP},          //
		{"DHCP", config.WANModeDHCP},          //
		{"v4v6", config.WANModeDHCP},          // 别名
		{"2", config.WANModeDHCP},             //
		{"both", config.WANModeBoth},          //
		{"all", config.WANModeBoth},           // 别名
		{"3", config.WANModeBoth},             //
		{"写错了", config.WANModeModemPro},       // 认不出 → 默认（配置写错不该换形态）
		{"yes", config.WANModeModemPro},       //
	}
	for _, c := range cases {
		if got := config.ParseWANMode(c.in); got != c.want {
			t.Errorf("ParseWANMode(%q) = %q，想要 %q", c.in, got, c.want)
		}
	}
}

// TestWANModePredicates 三种模式的两个开关。
func TestWANModePredicates(t *testing.T) {
	cases := []struct {
		mode          config.WANMode
		wantModemPro  bool
		wantDHCPv6Sib bool
	}{
		{config.WANModeModemPro, true, false}, // 一张接口，v4+v6 都在它上面
		{config.WANModeDHCP, false, true},     // 传统两张
		{config.WANModeBoth, true, true},      // 两种都要
	}
	for _, c := range cases {
		if got := c.mode.UsesModemPro(); got != c.wantModemPro {
			t.Errorf("%s.UsesModemPro() = %v，想要 %v", c.mode, got, c.wantModemPro)
		}
		if got := c.mode.WantsDHCPv6Sibling(); got != c.wantDHCPv6Sib {
			t.Errorf("%s.WantsDHCPv6Sibling() = %v，想要 %v", c.mode, got, c.wantDHCPv6Sib)
		}
	}
}

// TestWANProtoByMode ★ 核心：「不分协议」—— 模式 1/3 下**所有**类型都用 modem_pro。
//
// 旧代码只有一个 case "qmi"，所以 ECM/NCM/RNDIS 全落在 dhcp 上、再被配上 modem<id>_6，
// 用户看到的就是"只有 qmi 会切成一张，其它都是两个"。MBIM 后来也并进这一套（2026-10-13）。
func TestWANProtoByMode(t *testing.T) {
	kinds := []string{"qmi", "ecm", "ncm", "rndis", "mbim", "QMI", "MBIM", "ECM", "", "unknown"}
	for _, k := range kinds {
		// 模式 1（默认）：装了协议包 + 主机侧拨号开着 → 一律 modem_pro
		if got := netprov.WanProtoForKindMode(k, config.WANModeModemPro, true, true); got != netprov.ModemProtoName {
			t.Errorf("模式 modem_pro + kind=%q + 装了包 → 想要 %q，得到 %q", k, netprov.ModemProtoName, got)
		}
		// 模式 3：同样一律 modem_pro
		if got := netprov.WanProtoForKindMode(k, config.WANModeBoth, true, true); got != netprov.ModemProtoName {
			t.Errorf("模式 both + kind=%q + 装了包 → 想要 %q，得到 %q", k, netprov.ModemProtoName, got)
		}
		// 模式 2：一律 dhcp
		if got := netprov.WanProtoForKindMode(k, config.WANModeDHCP, true, true); got != "dhcp" {
			t.Errorf("模式 dhcp + kind=%q → 想要 dhcp，得到 %q", k, got)
		}
		// 没装协议包：退回落后的 dhcp（不认 modem_pro 的 netifd 起不来）
		if got := netprov.WanProtoForKindMode(k, config.WANModeModemPro, false, true); got != "dhcp" {
			t.Errorf("模式 modem_pro + kind=%q + 没装包 → 想要 dhcp，得到 %q", k, got)
		}
	}
}

// TestMBIMProtoByMode MBIM 2026-10-13 起不再是例外：
//
//	host_dial 开着 —— 会话由 modemd 托管的厂商拨号器建（quectel-CM 自带的 mbim-cm.c，
//	                判据是标准 USB CDC-MBIM 描述符、与厂商无关）⇒ 跟其它类型一模一样；
//	host_dial 关着 —— 没人拨号，只能靠 netifd 的 mbim.sh(umbim) ⇒ **必须**保持 proto=mbim，
//	                换成 modem_pro / dhcp 就是无人建会话 = 直接断网。
func TestMBIMProtoByMode(t *testing.T) {
	// host_dial = off：三种模式、包在不在，都要保持 mbim
	for _, m := range config.AllWANModes {
		for _, ok := range []bool{true, false} {
			if got := netprov.WanProtoForKindMode("mbim", m, ok, false); got != "mbim" {
				t.Errorf("host_dial=off + 模式 %s + MBIM(包=%v) → 想要 mbim，得到 %q", m, ok, got)
			}
		}
	}
	// host_dial = on：跟其它类型完全一致
	if got := netprov.WanProtoForKindMode("mbim", config.WANModeModemPro, true, true); got != netprov.ModemProtoName {
		t.Errorf("host_dial=on + 模式 modem_pro → 想要 %q，得到 %q", netprov.ModemProtoName, got)
	}
	if got := netprov.WanProtoForKindMode("mbim", config.WANModeDHCP, true, true); got != "dhcp" {
		t.Errorf("host_dial=on + 模式 dhcp → 想要 dhcp，得到 %q", got)
	}
	if got := netprov.WanProtoForKindMode("mbim", config.WANModeModemPro, false, true); got != "dhcp" {
		t.Errorf("host_dial=on + 没装协议包 → 想要 dhcp，得到 %q", got)
	}
}

// TestWANHostDialByDaemon 主机侧拨号器要管的类型：QMI 与 MBIM。
func TestWANHostDialByDaemon(t *testing.T) {
	yes := []string{"qmi", "QMI", " qmi ", "mbim", "MBIM", " Mbim "}
	no := []string{"", "ecm", "ncm", "rndis", "unknown", "mbim2"}
	for _, k := range yes {
		if !netprov.WANHostDialByDaemon(k) {
			t.Errorf("WANHostDialByDaemon(%q) 应该为 true", k)
		}
	}
	for _, k := range no {
		if netprov.WANHostDialByDaemon(k) {
			t.Errorf("WANHostDialByDaemon(%q) 应该为 false", k)
		}
	}
}

// TestV6SiblingByMode 三种模式下"要不要建 modem<id>_6"。
func TestV6SiblingByMode(t *testing.T) {
	defer func(v bool) { config.Cfg.Net.WANIPv6 = v }(config.Cfg.Net.WANIPv6)
	config.Cfg.Net.WANIPv6 = true

	cases := []struct {
		mode  config.WANMode
		proto string
		want  bool
	}{
		// 模式 1（默认）：主接口是 modem_pro，一张就够；连 dhcp 也不建（模式 1 不该出现 _6）
		{config.WANModeModemPro, netprov.ModemProtoName, false},
		{config.WANModeModemPro, "dhcp", false},
		// 模式 2：dhcp 要建；modem_pro 不该在这一档出现（但还是不建更安全）
		{config.WANModeDHCP, "dhcp", true},
		{config.WANModeDHCP, netprov.ModemProtoName, false},
		// 模式 3：两种都要 ⇒ dhcp 要建
		{config.WANModeBoth, "dhcp", true},
		{config.WANModeBoth, netprov.ModemProtoName, false},
		// 主机侧拨号的历史写法：数据面归拨号器，一律不建
		{config.WANModeDHCP, "none", false},
		{config.WANModeDHCP, "mbim", false},
		{config.WANModeDHCP, "qmi", false},
	}
	for _, c := range cases {
		withWANMode(t, c.mode)
		if got := modemWAN6Allowed(c.proto); got != c.want {
			t.Errorf("模式 %s + proto=%q：modemWAN6Allowed() = %v，想要 %v", c.mode, c.proto, got, c.want)
		}
	}

	// wan_ipv6 关掉时：任何模式、任何协议都不建（整条链路只走 IPv4）
	config.Cfg.Net.WANIPv6 = false
	for _, m := range config.AllWANModes {
		withWANMode(t, m)
		if modemWAN6Allowed("dhcp") {
			t.Errorf("模式 %s + wan_ipv6=off：不该建 v6 兄弟接口", m)
		}
	}
}

// TestQmiWantProtoByMode QMI 接口就地迁移（老配置升级上来的那条路）也要按模式走。
func TestQmiWantProtoByMode(t *testing.T) {
	// 显式 none 永远优先（用户明确要求不跑协议）
	withWANMode(t, config.WANModeModemPro)
	if got := netprov.QmiWantProto("none", "dhcp", true); got != "none" {
		t.Errorf("显式 none 应保持 none，得到 %q", got)
	}
	// 模式 1/3 + 装了包 → modem_pro
	for _, m := range []config.WANMode{config.WANModeModemPro, config.WANModeBoth} {
		withWANMode(t, m)
		if got := netprov.QmiWantProto("", "dhcp", true); got != netprov.ModemProtoName {
			t.Errorf("模式 %s：想要 %q，得到 %q", m, netprov.ModemProtoName, got)
		}
	}
	// 模式 2 → dhcp
	withWANMode(t, config.WANModeDHCP)
	if got := netprov.QmiWantProto("", netprov.ModemProtoName, true); got != "dhcp" {
		t.Errorf("模式 dhcp：想要 dhcp，得到 %q", got)
	}
	// 没装包 → dhcp
	withWANMode(t, config.WANModeModemPro)
	if got := netprov.QmiWantProto("", "qmi", false); got != "dhcp" {
		t.Errorf("没装协议包：想要 dhcp，得到 %q", got)
	}
}
