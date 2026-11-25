package kernel

import (
	"sort"
	"strconv"
	"strings"
)

// ParseUCIConfig 极简 UCI 解析：只认 config / option / list 三种行。
// 不追求完整语法（不支持 ${} 变量与续行）—— 我们只是读配置判断"有没有 / 在哪个区"，
// 真写配置还是交给 `uci` 命令本身。
func ParseUCIConfig(text string) []UCISection {
	var sections []UCISection
	counts := map[string]int{}
	for _, raw := range strings.Split(text, "\n") {
		toks := uciTokens(strings.TrimSpace(raw))
		if len(toks) == 0 || strings.HasPrefix(toks[0], "#") {
			continue
		}
		switch toks[0] {
		case "config":
			if len(toks) < 2 {
				continue
			}
			sec := UCISection{
				Type: toks[1], Index: counts[toks[1]],
				Options: map[string]string{}, Lists: map[string][]string{},
			}
			if len(toks) >= 3 {
				sec.Name = toks[2]
			}
			counts[toks[1]]++
			sections = append(sections, sec)
		case "option", "list":
			if len(sections) == 0 || len(toks) < 3 {
				continue
			}
			last := &sections[len(sections)-1]
			if toks[0] == "option" {
				last.Options[toks[1]] = toks[2]
			} else {
				last.Lists[toks[1]] = append(last.Lists[toks[1]], toks[2])
			}
		}
	}
	return sections
}

// FindZone 找防火墙里名为 zoneName 的区，返回（该节, 可直接给 uci 的段名, 是否找到）。
// 命名段返回段名；匿名段返回 @zone[i]（uci 支持这种写法，LuCI 默认生成的也是匿名段）。
func FindZone(sections []UCISection, zoneName string) (UCISection, string, bool) {
	if strings.TrimSpace(zoneName) == "" {
		zoneName = "wan"
	}
	for _, s := range sections {
		if s.Type != "zone" || s.Options["name"] != zoneName {
			continue
		}
		if s.Name != "" {
			return s, s.Name, true
		}
		return s, "@zone[" + strconv.Itoa(s.Index) + "]", true
	}
	return UCISection{}, "", false
}

// ZoneHasNetwork 这个区的 network 列表里有没有某张接口（幂等判断）
func ZoneHasNetwork(sec UCISection, iface string) bool {
	for _, n := range sec.Lists["network"] {
		if n == iface {
			return true
		}
	}
	return false
}

// ---- 把我们建的模组接口划进防火墙区（局域网才能经它上网） ----
//
// ⚠️⚠️ 2026-10-07 解决 RM500U 那轮，用户说"内部处理这个防火墙的都要去掉"——
// 我**多删了**：把这一整步（把接口划进 wan 区）也删了，结果 2026-10-08 用户报
// "换别的模组就上不了网、只有 500U 正常"。原因：
//
//	RM500U 是 ECM ⇒ 网卡叫 eth1，用户自己的 `/etc/config/firewall` 里 eth1 本来就在 wan 区 ⇒ 没事；
//	dhm 其它模组是 QMI/MBIM ⇒ 网卡叫 wwan0/wwan1，**不在任何区** ⇒
//	FORWARD 落到最后那条 `reject`、也没有 MASQUERADE ⇒ 局域网完全出不去（真机计数：reject 3069 个包）。
//
// 所以现在恢复成**每轮对账**（同 syncWAN6FirewallDevices 的做法），但更小心：
//   · 只加**我们建的**接口（`modem<id>` / `modem<id>_6`，且那台模组还在跑）；
//   · 加进去的记在标记 `modemd_networks` 上，之后只删**自己加过的**那几张 —— 用户自己配的网卡名一个不动。

// FirewallZoneAutoOption zone 段上留的标记：**我们替它加进 network 列表的那几张接口**
// （空格分隔）。没有它就分不清"用户自己划进来的"与"我们加的"。
const FirewallZoneAutoOption = "modemd_networks"

// ZoneSyncPlan 防火墙区对账的纯决策（不碰文件/命令，见 syncFirewallZone）。
//
//	want 现在该在区里的模组接口（modem<id> / modem<id>_6）
//	cur  区里 network 列表里现有全部接口（含用户自己配的）
//	ours 上次我们加进去的（标记里记着）
//
// 返回要 add 的、要 del 的。⚠️ del 只从 ours 里选 —— 用户自己配的接口即使不再需要也不动。
func ZoneSyncPlan(want, cur, ours []string) (add, del []string) {
	inWant := map[string]bool{}
	for _, w := range sortUniqueIfaces(want) {
		inWant[w] = true
	}
	inCur := map[string]bool{}
	for _, c := range cur {
		if s := strings.TrimSpace(c); s != "" {
			inCur[s] = true
		}
	}
	for w := range inWant {
		if !inCur[w] {
			add = append(add, w)
		}
	}
	sort.Strings(add)
	for _, o := range sortUniqueIfaces(ours) {
		if !inWant[o] {
			del = append(del, o)
		}
	}
	return add, del
}

// FirewallZoneCommands 生成"把接口划进区"的 uci 命令（幂等；没事可做返回 nil）。
//
// ⚠️ 最后跟着一条防火墙 reload：不 reload 内核里不会多出那张接口的 MASQUERADE/转发跳，
// 局域网还是上不了网（只改配置的话得等下次重启）。
func FirewallZoneCommands(zoneRef string, add, del, ours []string) [][]string {
	if strings.TrimSpace(zoneRef) == "" || (len(add) == 0 && len(del) == 0) {
		return nil
	}
	sec := "firewall." + strings.TrimSpace(zoneRef)
	var cmds [][]string
	for _, a := range add {
		cmds = append(cmds, []string{"uci", "add_list", sec + ".network=" + a})
	}
	for _, d := range del {
		cmds = append(cmds, []string{"uci", "-q", "del_list", sec + ".network=" + d})
	}
	// 标记 = (旧标记 ∪ 新加的) − 删掉的
	keep := map[string]bool{}
	for _, o := range sortUniqueIfaces(ours) {
		keep[o] = true
	}
	for _, a := range add {
		keep[a] = true
	}
	for _, d := range del {
		delete(keep, d)
	}
	var left []string
	for k := range keep {
		left = append(left, k)
	}
	sort.Strings(left)
	if len(left) == 0 {
		cmds = append(cmds, []string{"uci", "-q", "delete", sec + "." + FirewallZoneAutoOption})
	} else {
		cmds = append(cmds, []string{"uci", "set", sec + "." + FirewallZoneAutoOption + "=" + strings.Join(left, " ")})
	}
	cmds = append(cmds, []string{"uci", "commit", "firewall"})
	cmds = append(cmds, []string{"/etc/init.d/firewall", "reload"})
	return cmds
}

// FindIncludeByPath 找指向某个脚本文件的防火墙 include 段，返回（该节, 可直接给 uci 的段名, 是否找到）。
// 命名段返回段名；匿名段返回 @include[i]（uci 支持这种写法，LuCI 生成的也常是匿名段）。
func FindIncludeByPath(sections []UCISection, path string) (UCISection, string, bool) {
	want := strings.TrimSpace(path)
	for _, s := range sections {
		if s.Type != "include" || strings.TrimSpace(s.Options["path"]) != want {
			continue
		}
		if s.Name != "" {
			return s, s.Name, true
		}
		return s, "@include[" + strconv.Itoa(s.Index) + "]", true
	}
	return UCISection{}, "", false
}

// ---- 生成要执行的命令 ----

// WANPlan 一台模组该有的 WAN 接口配置
//
// ⚠️ Proto 不能写死 dhcp 之外的想当然值：**协议要跟模组的 USB 类型对上**。
// ECM / NCM / RNDIS 是模组自己发 DHCP（主机只要取地址）；
// QMI 也是 **dhcp** —— 地址/路由/DNS 由 netifd 取，但**数据呼叫**要主机侧拨号器
// （见 hostdial.go）接管，两者分工（见 WANProtoForKind 的注释）；上一版这里是
// none（不配置协议），那样 netifd 不碰网卡、接口上看不到 IP；
// MBIM 则交给 netifd 的 mbim.sh（umbim）—— 这种协议下 UCI 里的 device 指的是
// **控制口字符设备**（/dev/cdc-wdm0），不是网卡。
type WANPlan struct {
	ModemID int
	Iface   string // modem0
	Device  string // 主机侧网卡，如 eth2 / wwan0（dhcp 与 none 用它；也是给页面看的）
	// Proto dhcp / none（历史写法，手工配置时可能遇到）/ mbim（空 = dhcp）
	Proto   string
	WDM     string // qmi / quectel / mbim（手工写法）：/dev/cdc-wdm0（这几种协议的 device 选项指的是它）
	APN     string // qmi / quectel / mbim：主机侧拨号要自己带 APN
	PDPType string // 可选：IPV4V6 / IP / IPV6（写进 pdptype）
	Metric  int    // 默认路由 metric：越小越优先（默认 20，不抢路由器原有 WAN）；none 不用它
}
