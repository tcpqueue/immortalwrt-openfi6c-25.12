package netprov

// ============ v6 兄弟接口的防火墙规则（纯逻辑，可单测） ============
//
// 【为什么需要】v6 兄弟接口（modem<id>_6）起来之后要给它的网卡打这套规则
// （用户 2026-10-06 要求；`usb0` 换成该接口绑的那张网卡名）：
//
//	iptables  -t nat -A PREROUTING  -p udp --dport 53 -j REDIRECT --to-ports 53
//	iptables  -t nat -A PREROUTING  -p tcp --dport 53 -j REDIRECT --to-ports 53
//	ip6tables -t nat -A PREROUTING  -p udp --dport 53 -j REDIRECT --to-ports 53
//	ip6tables -t nat -A PREROUTING  -p tcp --dport 53 -j REDIRECT --to-ports 53
//	ip6tables -t nat -A POSTROUTING -o <网卡> -j MASQUERADE
//
// ★★ 【与 [net] wan_mode 的关系（用户 2026-10-13 补充要求）】
//
//	这套规则**只属于"有 v6 兄弟接口"的形态**：
//	  · 模式 1（modem_pro 单接口，默认）→ 没有 modem<id>_6 ⇒ 这段规则整段不该存在；
//	  · 模式 2/3（dhcp / both）        → 有 modem<id>_6 ⇒ 按实际网卡生成。
//	真正的开关在 netprov_engine.go 的 wan6FirewallDevsNow（模式 1 直接返回空 ⇒
//	MergeWAN6CustomRules 走删除分支，把上一个模式留下的规则清干净）。
//	本文件只负责"给定网卡清单 → 生成那段文本"，是纯函数。
//
// ⚠️⚠️ **往哪儿放（三轮实测后定稿，2026-10-07）**：
//
//	① 手写进「防火墙 → 自定义规则」（= **`/etc/firewall.user`**，LuCI 的 Custom Rules 页就是它）⇒
//	   用户实测**稳定生效**；
//	② 我们自建 include 脚本 ⇒ 有时候不生效（用户报"脚本没有运行"）；
//	③ 写 `/etc/nftables.d/*.nft` ⇒ **完全不生效**（那台机器的 fw4 根本没加载它）。
//
// ⇒ 所以 modemd **只做一件事：维护 `/etc/firewall.user` 里的一段带标记的块**
// （用户在 LuCI 里看得见、也能自己删）—— **自己不再 exec 任何 iptables/ip6tables**：
//
//	· 写入：WAN6CustomRulesBlock 生成那段，MergeWAN6CustomRules 整段替换进去
//	  （块外用户自己写的命令一个字不动）；
//	· 生效：写完 reload 一次防火墙，让它把这文件跑一遍；之后每次 restart/reload 也都会重跑；
//	· 回收：模组都不在了就把那段删掉（MergeWAN6CustomRules(cur, "")）。
//
// ⚠️ 每行都写成 `-C 查 → 没有才 -A`：防火墙每次 reload 都会把这文件重跑一遍，
// 不判一下就会一遍遍重复追加。（那个 `-C` 是**写进文件的文本**，由防火墙执行，不是我们自己跑。）
//
// ⚠️ 用户 2026-10-07 明确要求："内部处理这个防火墙的都要去掉，只走自定义防火墙" ⇒
// 任何"modemd 自己下发/校验/补打/删旧规则"的代码都不该再出现。

import (
	"path/filepath"
	"sort"
	"strings"
)

// WAN6FirewallScriptPath 老版本那个自建脚本落在哪 ——
// **现在只用于升级清理**（`dropLegacyWAN6OwnInclude` 要把它删掉）。
//
// 规则 = **与配置文件同目录**（和 imei.json / quota.json 一个套路），而且**永远绝对路径**。
// ⚠️ 生产是 Linux，所以这里用 POSIX 的拼法（不走 filepath.Join —— 它在 Windows 上会把
// "/etc/modemd" 拼成 "\etc\modemd"，单测没法比字符串）。配置路径为空/相对时报到 /etc/modemd 下。
func WAN6FirewallScriptPath(configPath string) string {
	dir := ""
	if p := strings.TrimSpace(configPath); p != "" {
		dir = filepath.ToSlash(filepath.Dir(p))
	}
	if dir == "" || dir == "." {
		dir = "/etc/modemd"
	}
	if !strings.HasPrefix(dir, "/") {
		// 相对路径：先转绝对（Linux 上 = cwd + dir），转不出来就退回默认目录
		if abs, err := filepath.Abs(dir); err == nil {
			dir = filepath.ToSlash(abs)
		}
		if !strings.HasPrefix(dir, "/") {
			dir = "/etc/modemd"
		}
	}
	return strings.TrimRight(dir, "/") + "/" + WAN6FirewallScriptName
}

// WAN6CustomRulesBlock 生成要写进 `/etc/firewall.user` 的那一段。
//
// 命令本体直接取 WAN6FirewallIPTablesRules —— 与单测钉住的那 5 条文本是同一份，不会跑偏。
// 每行写成 `-C 查 → 没有才 -A`：防火墙每次 restart/reload 都会把这文件重跑一遍，不判就会重复追加。
//
// devs = 需要 IPv6 MASQUERADE 的**模组网卡清单**（多台模组就多个 —— 用户要的"设备名当参数"）。
//
// ⚠️⚠️ **devs 空 ⇒ 返回空串（整段收掉），绝不能只留那 4 条 DNS 重定向**
// （2026-10-08 真机事故）：以前空清单也会生成一段"只有 DNS 重定向"的块，而
// `MergeWAN6CustomRules(cur, "")` 才是"删掉"的意思 —— 非空块会被写进 `/etc/firewall.user`
// 并**永远留着**（下次还是非空）。于是用户换成别的模组后，那段无接口限定的
// `iptables/ip6tables -t nat PREROUTING --dport 53 -j REDIRECT` 还在，把**整个路由器**的
// IPv4/IPv6 DNS 都劫持到本机 53 —— 别的模组的链路和电脑的上网就都不正常了。
// 用户的话："只给在定义需要处理的模组处理，没有定义的都不要管"。
//
// ⚠️ 这段会被 fw4 **source**（main.uc 的 run_includes：`sh -c "…; . '<path>'"`）⇒
// 里面不能出现 `exit` / `set -e` / `config()`（fw4 把 config 定义成"报错退出"）。
func WAN6CustomRulesBlock(devs []string, hasIP6tables bool) string {
	list := Wan6FirewallDevList(devs)
	if len(list) == 0 {
		return "" // 没有"该处理的"模组 ⇒ 整段收掉（只靠 MergeWAN6CustomRules 的删除分支）
	}
	var b strings.Builder
	b.WriteString(Wan6RulesBegin + "\n")
	b.WriteString("# 模组 WAN6：DNS 重定向 + IPv6 MASQUERADE。由 5G终端（modemd）按实际网卡自动维护，\n")
	b.WriteString("# 每次整段重写 ⇒ 段落内手工改动会被覆盖；模组不在/不是要处理的型号时整段自动消失。\n")
	for _, argv := range WAN6FirewallRuleArgs(list, hasIP6tables) {
		b.WriteString(wan6CustomRulesLine(argv))
	}
	b.WriteString(Wan6RulesEnd + "\n")
	return b.String()
}

// Wan6FirewallDevList 网卡清单：去重 + 只留合法网卡名 + 排序。
// 排序是为了"内容稳定 ⇒ 变了才重写文件"（否则每轮都写一次、多一次防火墙 reload）。
func Wan6FirewallDevList(devs []string) []string {
	seen := map[string]bool{}
	var list []string
	for _, d := range devs {
		d = strings.TrimSpace(d)
		if d == "" || !ValidNetdevName(d) || seen[d] {
			continue
		}
		seen[d] = true
		list = append(list, d)
	}
	sort.Strings(list)
	return list
}

// MergeWAN6CustomRules 把我们的规则块并进 /etc/firewall.user 的现有内容：
//
//	· 已经有标记块 ⇒ 整段替换（内容一模一样时返回 changed=false，不会白发一次 reload）；
//	· 没有 ⇒ 追加到末尾（前面留一个空行）；
//	· block 为空 ⇒ 把块整段删掉（模组都不在了就走这条，见 removeWAN6Firewall）。
//
// ⚠️ **只动我们那对标记之间的东西**：这个文件是用户的地盘（LuCI 自定义规则页会整文件读写），
// 块外一个字符都不能碰。
func MergeWAN6CustomRules(cur, block string) (string, bool) {
	block = strings.TrimRight(block, "\n")
	if i := strings.Index(cur, Wan6RulesBegin); i >= 0 {
		if j := strings.Index(cur[i:], Wan6RulesEnd); j >= 0 {
			end := i + j + len(Wan6RulesEnd)
			if end < len(cur) && cur[end] == '\n' {
				end++
			}
			if block == "" {
				out := strings.TrimRight(cur[:i]+cur[end:], "\n")
				if out != "" {
					out += "\n"
				}
				return out, out != cur
			}
			out := cur[:i] + block + "\n" + cur[end:]
			return out, out != cur
		}
	}
	if block == "" {
		return cur, false
	}
	out := cur
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if strings.TrimSpace(out) != "" {
		out += "\n"
	}
	out += block + "\n"
	return out, out != cur
}

// Wan6MasqArgv 一条 `ip6tables … -o <dev> -j MASQUERADE`（`-A` 形态；要 `-C` 用 WAN6FirewallCheckOf）
func Wan6MasqArgv(dev string) []string {
	return []string{"ip6tables", "-t", "nat", "-A", "POSTROUTING", "-o", dev, "-j", "MASQUERADE"}
}

// WAN6FirewallWithOp 把规则里的 `-A` 换成 op（`-C` 判在不在）。
// 只换**第一个** `-A`（就是那个动作位）。
//
// ⚠️ 这不是"我们自己查一遍"：那个 `-C` 是写进 `/etc/firewall.user` 的**文本**，
// 由防火墙每次 reload 自己执行（用户手工写的自定义规则就是这么写的）。
func WAN6FirewallWithOp(argv []string, op string) []string {
	out := append([]string{}, argv...)
	for i, a := range out {
		if a == "-A" {
			out[i] = op
			break
		}
	}
	return out
}

// WAN6FirewallCheckOf 一条 `-A` 规则的 `-C` 形态（判"在不在"，不改内核）
func WAN6FirewallCheckOf(argv []string) []string { return WAN6FirewallWithOp(argv, "-C") }

// WAN6FirewallRuleArgs 这套规则的完整 argv 列表（**只用来生成写进自定义规则的文本**）：
// DNS 重定向（与网卡无关；机器上没有 ip6tables 就只出 IPv4 那两条）+ 每个网卡一条 IPv6 MASQUERADE。
func WAN6FirewallRuleArgs(devs []string, hasIP6tables bool) [][]string {
	rules := WAN6FirewallIPTablesRules("", hasIP6tables)
	for _, d := range Wan6FirewallDevList(devs) {
		if hasIP6tables {
			rules = append(rules, Wan6MasqArgv(d))
		}
	}
	return rules
}

// wan6CustomRulesLine 一条规则 → 自定义规则文件里的一行"`-C` 查过、没有才 `-A`"
// （幂等：防火墙每次 reload 都会把这文件重跑一遍，不判会一遍遍重复追加）
func wan6CustomRulesLine(argv []string) string {
	return WANCommandText(WAN6FirewallCheckOf(argv)) + " 2>/dev/null || " + WANCommandText(argv) + "\n"
}

// WAN6FirewallIPTablesRules 写进自定义规则文件的命令（`-A` 形态）。
//
// hasIP6tables=false 时只给前两条 IPv4 —— 对应用户手写时那句
// `[ -n "$(command -v ip6tables)" ]` 判断（机器上没装 ip6tables 就别写那几条）。
// dev 为空时不给 MASQUERADE。
func WAN6FirewallIPTablesRules(dev string, hasIP6tables bool) [][]string {
	rules := [][]string{
		{"iptables", "-t", "nat", "-A", "PREROUTING", "-p", "udp", "--dport", "53", "-j", "REDIRECT", "--to-ports", "53"},
		{"iptables", "-t", "nat", "-A", "PREROUTING", "-p", "tcp", "--dport", "53", "-j", "REDIRECT", "--to-ports", "53"},
	}
	if !hasIP6tables {
		return rules
	}
	rules = append(rules,
		[]string{"ip6tables", "-t", "nat", "-A", "PREROUTING", "-p", "udp", "--dport", "53", "-j", "REDIRECT", "--to-ports", "53"},
		[]string{"ip6tables", "-t", "nat", "-A", "PREROUTING", "-p", "tcp", "--dport", "53", "-j", "REDIRECT", "--to-ports", "53"},
	)
	if dev = strings.TrimSpace(dev); dev != "" {
		rules = append(rules, Wan6MasqArgv(dev))
	}
	return rules
}

// Wan6FirewallDevsFor 纯计算："该有那套自定义防火墙规则的网卡清单"。
//
// ★★ 2026-10-13（用户实测澄清）：判据**与 wan_mode / 接口张数无关**。
// RM500U 的 ECM 在 wan_mode=modem_pro（单接口、没有 modem<id>_6）下**同样需要**这套规则
// （DNS 重定向 + ip6tables MASQUERADE = NAT66），否则内网拿不到 IPv6。
// 用户原话："能拿到 ipv6 是防火墙 没有像那个一样处理"。
//
//	devList   —— 受管接口（modem<id> 与 modem<id>_6 都可能在里面，通常绑同一张网卡）
//	inProfile —— 这台模组（按 ModemID）要不要这套规则（型号在 wan6NonQMIProfiles 名单里）
//	hasV6     —— 这张网卡上现在有没有全局 IPv6 地址（= 拨号成功）
//
// 抽成纯函数是为了**能单测**：真机上只有名单里的型号才走这条路，本机（FM160-CN）永远
// 看不到效果 —— 光靠真机验不了"模式 1 下这套规则不该被误删"这一条。
func Wan6FirewallDevsFor(devList []ManagedWANIface, inProfile func(int) bool, hasV6 func(string) bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, it := range devList {
		dev := strings.TrimSpace(it.Device)
		if dev == "" || seen[dev] {
			continue // 同一张网卡被主接口和 v6 兄弟接口同时指着 ⇒ 只记一次
		}
		if !inProfile(it.ModemID) {
			continue // 这台模组不吃这套规则
		}
		if !hasV6(dev) {
			continue // 还没拨上 / 没拿到 IPv6 ⇒ 这时候写进去也是摆设
		}
		seen[dev] = true
		out = append(out, dev)
	}
	return out
}

// Wan6FirewallKeepFor 纯计算：还有没有任何受管接口在用模组网卡。
//
// 决定"要不要把那套规则收掉"。★ 判据是"还有没有模组接口"，**不是**"还有没有
// modem<id>_6" —— 后者会让 wan_mode=modem_pro（单接口）在删 `_6` 的瞬间把规则一起收掉，
// 那正是"500U ECM 在 Modem-Pro 下 IPv6 不通"的成因之一。
func Wan6FirewallKeepFor(devList []ManagedWANIface) bool {
	for _, it := range devList {
		if strings.TrimSpace(it.Device) != "" {
			return true
		}
	}
	return false
}
