package kernel

import (
	"testing"
)

// TestWan6NeedsOnlinkFix 「上游只给 on-link RA」那套补救该给哪些链路类型做。
//
// ★★ 核心断言：**ecm / ncm / rndis / mbim 全都要**。
//
//	· ecm/ncm/rndis —— 用户 2026-10-13：“v6_fix_models 把这个去掉，应该 ecm 都要这么处理的”。
//	  旧口径是“只看型号白名单”（当时只有 rm500u），结果 FM160-CN 切到 ECM 后
//	  内网 IPv6 直接不通 —— 因为 LAN 低 metric 路由和 odhcpd NDP relay 都没做。
//	· mbim —— 2026-10-13 把 MBIM 统一成走主机侧拨号器后真机复现出**同样的症状**
//	  （ULA ping 通、全局地址不通；内网主机只拿到老前缀的 DHCPv6 租约 + ULA），
//	  机理完全同类（模组桥出一张网卡 + 上游只给 on-link /64、不做 PD）。
func TestWan6NeedsOnlinkFix(t *testing.T) {
	cases := []struct {
		kind string
		want bool
	}{
		{"ecm", true},   // ★ FM160-CN / RM500U 的 ECM 都靠这套
		{"ncm", true},   // 同类：模组桥出以太网卡，上游只发 RA
		{"rndis", true}, // 同上
		{"mbim", true},  // ★ MBIM 同类（模组桥出 wwan0 + 上游只给 on-link /64）
		{"MBIM", true},  // 大小写不敏感
		{"ECM", true},   //
		{" ecm ", true}, // 前后空白容忍
		{"qmi", false},  // 数据面归主机侧拨号器、前缀走 PDN 正常路由，不需要
		{"", false},     // 类型还没探到 ⇒ 先不做
		{"unknown", false},
	}
	for _, c := range cases {
		if got := wan6NeedsOnlinkFix(c.kind); got != c.want {
			t.Errorf("wan6NeedsOnlinkFix(%q) = %v，想要 %v", c.kind, got, c.want)
		}
	}
}

// TestWan6OnlinkFixKinds 名单本身必须恰好是这四类（别悄悄改口径）。
func TestWan6OnlinkFixKinds(t *testing.T) {
	want := map[string]bool{"ecm": true, "ncm": true, "rndis": true, "mbim": true}
	if len(wan6OnlinkFixKinds) != len(want) {
		t.Fatalf("wan6OnlinkFixKinds = %v，想要恰好 %d 项", wan6OnlinkFixKinds, len(want))
	}
	for _, k := range wan6OnlinkFixKinds {
		if !want[k] {
			t.Errorf("名单里多了 %q", k)
		}
	}
}
