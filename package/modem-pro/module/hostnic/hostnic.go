package hostnic

// ====================== 主机网卡（FM350 机型特例） ======================
//
// 背景：绝大多数机型的拨号都是"模组侧激活 PDP + 主机侧自动拿地址"
// （ECM/NCM/RNDIS 由模组或主机 DHCP 发地址，MBIM/QMI 由主机拨号器配地址），
// **本程序不用管主机网卡**。
//
// **FM350-GL 是例外**（实测）：它的 USB 网卡（RNDIS）**不发 DHCP** —— Windows 上一直挂着
// 169.254.x.x 的 APIPA 地址、没有默认路由，所以"模组侧拨号成功"也上不了网，
// 必须手工把网卡 IP 配成跟模组**对应**（同网段的地址 + 网关指向模组）才行。
//
// 本文件只放纯逻辑（可单独测试）：
//
//	① HostNICQuirk    这个机型是不是需要配主机网卡（机型特例表）
//	② 掩码/前缀换算、配置校验、按模组 IP 推建议值
//	③ HostNICCommands 把一份配置翻译成**要执行的命令**（Windows netsh / Linux ip）
//	④ hostnic.json 落盘（配置由用户在页面上填，记住，不写死任何"猜到"的值）
//
// 「真的去执行命令」在 hostnic_engine.go（那里才有 os/exec 与权限判断）。

import (
	"encoding/json"
	"fmt"
	"modemd/module/config"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ---------- 机型特例 ----------

// HostNICQuirk 该机型是否需要"手工配主机网卡"。第二个返回值是给页面看的解释（不需要时为空）。
//
// 目前只有 FM350 系列（实测 FM350-GL，固件 81600.0000.00.29.22.40）：
// RNDIS 网卡不提供 DHCP，主机侧只有 169.254 链路本地地址 → 必须手工配同网段 IP。
// ⚠️ 别的机型别往这里加：ECM/NCM/RNDIS 正常机型、MBIM/QMI 机型的主机侧地址
// 由模组 DHCP 或主机拨号器负责，加进来只会让人以为"每次都要手工配"。
func HostNICQuirk(model string) (bool, string) {
	m := strings.ToUpper(strings.TrimSpace(model))
	// 判据用 Contains 而不是 HasPrefix：调用方可能传的是 ATI **原文**
	// （"ATI|Manufacturer: Fibocom|Model: FM350-GL|Revision: …"），前缀判会漏
	if strings.Contains(m, "FM350") {
		return true, "FM350 系列（实测 FM350-GL）的 USB 网卡 DHCP 很慢/不稳：实测要么几十秒到一两分钟才回，" +
			"要么干脆不回 —— 主机侧就长期挂在 169.254.x.x 上、没有默认路由，模组侧拨号成功也上不了网。" +
			"两条路：等它 DHCP（「先试 DHCP」），或把网卡 IP 手工配成与模组对应（同网段 + 网关指向模组）。"
	}
	return false, ""
}

// ---------- 掩码 / 前缀 ----------

// PrefixFromMask 掩码 → 前缀长度。两种写法都接受："255.255.255.0" 或 "24"。
func PrefixFromMask(mask string) (int, bool) {
	m := strings.TrimSpace(mask)
	if m == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(m); err == nil {
		if n >= 0 && n <= 32 {
			return n, true
		}
		return 0, false
	}
	ip := net.ParseIP(m)
	if ip == nil || ip.To4() == nil {
		return 0, false
	}
	ones, bits := net.IPMask(ip.To4()).Size()
	if bits != 32 {
		return 0, false
	}
	// Size() 对非连续掩码会回 0 —— 用点分掩码回比一次，确认是合法（连续）掩码
	if MaskFromPrefix(ones) != ip.To4().String() {
		return 0, false
	}
	return ones, true
}

// MaskFromPrefix 前缀长度 → 点分掩码
func MaskFromPrefix(n int) string {
	if n < 0 || n > 32 {
		return ""
	}
	return net.IP(net.CIDRMask(n, 32)).String()
}

// HostNICPlan 一份主机网卡配置
type HostNICPlan struct {
	Interface string   `json:"iface,omitempty"`   // 网卡名（Windows："以太网 3"；Linux："eth1"）
	IP        string   `json:"ip,omitempty"`      // 主机侧要用的 IP
	Mask      string   `json:"mask,omitempty"`    // 掩码（255.255.255.0）或前缀长度（24）
	Gateway   string   `json:"gateway,omitempty"` // 网关（指向模组的 LAN 地址）
	DNS       []string `json:"dns,omitempty"`     // DNS 服务器（可空）
}

// ValidateHostNICPlan 校验一份配置：不合法就别拿去生成命令（半套命令比没有更糟）
func ValidateHostNICPlan(p HostNICPlan) error {
	if strings.TrimSpace(p.Interface) == "" {
		return fmt.Errorf("先选一张网卡")
	}
	if ip := net.ParseIP(strings.TrimSpace(p.IP)); ip == nil || ip.To4() == nil {
		return fmt.Errorf("IP 不是合法的 IPv4 地址：%q", p.IP)
	}
	if _, ok := PrefixFromMask(p.Mask); !ok {
		return fmt.Errorf("掩码要么写点分格式（255.255.255.0），要么写前缀长度（24）")
	}
	if g := strings.TrimSpace(p.Gateway); g != "" {
		if ip := net.ParseIP(g); ip == nil || ip.To4() == nil {
			return fmt.Errorf("网关不是合法的 IPv4 地址：%q", p.Gateway)
		}
	}
	for _, d := range p.DNS {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if ip := net.ParseIP(d); ip == nil || ip.To4() == nil {
			return fmt.Errorf("DNS 不是合法的 IPv4 地址：%q", d)
		}
	}
	return nil
}

// NormalizeHostNICPlan 去掉首尾空格、掩码归一成点分格式、DNS 去空去重
func NormalizeHostNICPlan(p HostNICPlan) HostNICPlan {
	out := HostNICPlan{
		Interface: strings.TrimSpace(p.Interface),
		IP:        strings.TrimSpace(p.IP),
		Mask:      strings.TrimSpace(p.Mask),
		Gateway:   strings.TrimSpace(p.Gateway),
	}
	if n, ok := PrefixFromMask(out.Mask); ok {
		out.Mask = MaskFromPrefix(n)
	}
	seen := map[string]bool{}
	for _, d := range p.DNS {
		d = strings.TrimSpace(d)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out.DNS = append(out.DNS, d)
	}
	return out
}

// SuggestHostNIC 按模组上报的本机 IP 推一份"建议值"（页面预填用，**仅供参考**）。
//
// 思路就是用户说的"IP 对应"：主机取同网段里模组的邻居地址、网关指向模组本身。
//
//	模组 10.178.25.161 → 若按 /24 同网段：主机 10.178.25.2、网关 10.178.25.161
//
// 真正的网段/网关只有模组那份配置知道，所以页面上这份值是**可改**的，别当真理用。
func SuggestHostNIC(modemIP string) (HostNICPlan, bool) {
	ip := net.ParseIP(strings.TrimSpace(modemIP))
	if ip == nil || ip.To4() == nil {
		return HostNICPlan{}, false
	}
	v4 := ip.To4()
	// 取同网段里"最后一个字节 = 2"的地址：避开 .0/.1（网络号与常见网关）
	host := net.IPv4(v4[0], v4[1], v4[2], 2)
	if host.Equal(v4) {
		host = net.IPv4(v4[0], v4[1], v4[2], 3)
	}
	return HostNICPlan{
		IP:      host.String(),
		Mask:    MaskFromPrefix(24),
		Gateway: v4.String(),
	}, true
}

// ---------- 命令生成 ----------

// 三组动作
const (
	HostNICRenew  = "renew"  // 先试 DHCP：重新请求租约（模块本身发地址的话就不用配静态了）
	HostNICStatic = "static" // 配成静态 IP（FM350 要的就是这个）
	HostNICDhcp   = "dhcp"   // 恢复成自动获取（撤销手工配置）
)

// hostNICRenewTimeout DHCP 请求的超时。
// ⚠️ 实测（FM350-GL + Windows，2026-09-30）：它对 DHCP **会应答，但极慢/不稳** ——
// 前几次 `ipconfig /renew` 在 20 / 45 / 90 秒内都没回来（输出只到 “Windows IP 配置” 就卡住），
// 但过后网卡上确实出现了 **DHCP 下发的默认网关（= 模组上报的那个地址）**。
// 所以超时给 90 秒：能应答时几十秒内就回，等满也只说明“这次没等到”，不能下“没 DHCP”的结论。
const hostNICRenewTimeout = 90 * time.Second

// HostNICCommand 一条要执行的命令。
// Text 是给人看/复制的完整命令行，Prog/Args 是后端真正执行时用的（**不拼 shell**）。
type HostNICCommand struct {
	Label string   `json:"label"`
	Text  string   `json:"text"`
	Prog  string   `json:"-"`
	Args  []string `json:"-"`
	// AdminOnly 表示这条命令需要管理员 / root（页面会把「直接执行」按钮标出来）
	AdminOnly bool `json:"admin_only,omitempty"`
	// Manual 只显示不执行：这类命令会改固件自己的配置（如 OpenWrt 的 UCI），
	// 接口名/防火墙分区要人判断，不该由按钮一键下发。
	Manual bool `json:"manual,omitempty"`
	// timeout 单条命令的超时（不导出、不进 JSON）：DHCP 请求要等路由器/模组应答，
	// 实测 Windows 上没 DHCP 服务器时 `ipconfig /renew` 要磨菇一分钟才回来
	Timeout time.Duration
}

// IsOpenWrt 本机是不是 OpenWrt / LEDE（用它决定「持久化」那一块要不要出）。
// 判据取两个都存在就够
func IsOpenWrt() bool {
	for _, p := range []string{"/etc/openwrt_release", "/etc/config/network"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// HostNICActionSet 一组动作 + 该组要执行的命令
type HostNICActionSet struct {
	Mode     string           `json:"mode"`
	Label    string           `json:"label"`
	Hint     string           `json:"hint,omitempty"`
	Commands []HostNICCommand `json:"commands"`
}

// quoteArg 命令行**展示**时的引号处理（执行走 Args，不拼串）
func quoteArg(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\"") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

func cmdText(prog string, args ...string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, prog)
	for _, a := range args {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

// netshName / netshNameText 是同一件事的两种写法：
//
//	argv：整个 "name=xxx" 作为一个参数（网卡名里的空格不会拆开）
//	命令行：netsh 自己的语法要求把值引起来 —— name="以太网 3"
//
// 所以执行用前者、展示/复制用后者，不能混。
func netshName(name string) string     { return "name=" + name }
func netshNameText(name string) string { return `name="` + name + `"` }

func hostNICCmd(label, text, prog string, args ...string) HostNICCommand {
	return HostNICCommand{Label: label, Text: text, Prog: prog, Args: args}
}

// HostNICCommands 生成某组动作要执行的命令（goos 只是为了可测试才当参数传进来）。
func HostNICCommands(mode string, p HostNICPlan, goos string) []HostNICCommand {
	p = NormalizeHostNICPlan(p)
	prefix, _ := PrefixFromMask(p.Mask)
	iface := p.Interface
	switch strings.ToLower(goos) {
	case "windows":
		switch mode {
		case HostNICRenew:
			c := HostNICCommand{
				Label: "向模组重新请求 DHCP 租约（最多等 90 秒，这个模组的 DHCP 很慢）", Text: cmdText("ipconfig", "/renew", iface),
				Prog: "ipconfig", Args: []string{"/renew", iface}, AdminOnly: true,
			}
			c.Timeout = hostNICRenewTimeout
			return []HostNICCommand{c}
		case HostNICStatic:
			// 地址（没填网关就不拼那一段 —— netsh 允许不带网关，拼个空串反而报错）
			addrArgs := []string{"interface", "ip", "set", "address", netshName(iface), "static", p.IP, p.Mask}
			addrText := "netsh interface ip set address " + netshNameText(iface) + " static " + p.IP + " " + p.Mask
			if p.Gateway != "" {
				addrArgs = append(addrArgs, p.Gateway)
				addrText += " " + p.Gateway
			}
			out := []HostNICCommand{hostNICCmd(
				"把网卡配成静态 IP（会自动关掉该网卡的 DHCP）", addrText, "netsh", addrArgs...)}
			out[0].AdminOnly = true
			for i, d := range p.DNS {
				if i == 0 {
					c := hostNICCmd("DNS（主）",
						"netsh interface ip set dns "+netshNameText(iface)+" static "+d,
						"netsh", "interface", "ip", "set", "dns", netshName(iface), "static", d)
					c.AdminOnly = true
					out = append(out, c)
					continue
				}
				idx := strconv.Itoa(i + 1)
				c := hostNICCmd(fmt.Sprintf("DNS（第 %d 个）", i+1),
					"netsh interface ip add dns "+netshNameText(iface)+" "+d+" index="+idx,
					"netsh", "interface", "ip", "add", "dns", netshName(iface), d, "index="+idx)
				c.AdminOnly = true
				out = append(out, c)
			}
			return out
		case HostNICDhcp:
			a := hostNICCmd("恢复成自动获取 IP",
				"netsh interface ip set address "+netshNameText(iface)+" source=dhcp",
				"netsh", "interface", "ip", "set", "address", netshName(iface), "source=dhcp")
			a.AdminOnly = true
			b := hostNICCmd("恢复成自动获取 DNS",
				"netsh interface ip set dns "+netshNameText(iface)+" source=dhcp",
				"netsh", "interface", "ip", "set", "dns", netshName(iface), "source=dhcp")
			b.AdminOnly = true
			return []HostNICCommand{a, b}
		}
		return nil
	case "linux", "openwrt": // Linux / OpenWrt（openwrt 只是多一层：DNS 不走 resolv.conf，改用 UCI）
		openwrt := strings.ToLower(goos) == "openwrt"
		switch mode {
		case HostNICRenew:
			c := HostNICCommand{
				Label: "向模组重新请求 DHCP 租约（最多等 90 秒，这个模组的 DHCP 很慢）", Text: cmdText("udhcpc", "-i", iface),
				Prog: "udhcpc", Args: []string{"-i", iface}, AdminOnly: true,
			}
			c.Timeout = hostNICRenewTimeout
			return []HostNICCommand{c}
		case HostNICStatic:
			cidr := p.IP
			if prefix > 0 {
				cidr = fmt.Sprintf("%s/%d", p.IP, prefix)
			}
			out := []HostNICCommand{
				{
					Label: "先清掉旧地址（含 169.254 那条）", Text: cmdText("ip", "addr", "flush", "dev", iface),
					Prog: "ip", Args: []string{"addr", "flush", "dev", iface}, AdminOnly: true,
				},
				{
					Label: "配静态地址", Text: cmdText("ip", "addr", "add", cidr, "dev", iface),
					Prog: "ip", Args: []string{"addr", "add", cidr, "dev", iface}, AdminOnly: true,
				},
				{
					Label: "网卡上线", Text: cmdText("ip", "link", "set", iface, "up"),
					Prog: "ip", Args: []string{"link", "set", iface, "up"}, AdminOnly: true,
				},
			}
			if p.Gateway != "" {
				// 用 replace 而不是 add：重复点「应用」时 add 会报 File exists，
				// 而 replace 不管有没有旧默认路由都能成（对 OpenWrt netifd 管的表也安全）
				out = append(out, HostNICCommand{
					Label: "默认路由指向模组（replace；报 File exists 就是 busybox 的 ip 不支持 replace，改用 UCI 块）",
					Text:  cmdText("ip", "route", "replace", "default", "via", p.Gateway, "dev", iface),
					Prog:  "ip", Args: []string{"route", "replace", "default", "via", p.Gateway, "dev", iface},
					AdminOnly: true,
				})
			}
			// OpenWrt 不吃 /etc/resolv.conf（DNS 由 dnsmasq 管）→ DNS 放到下面的 UCI 块里
			if !openwrt {
				for _, d := range p.DNS {
					out = append(out, HostNICCommand{
						Label: "DNS（写进 resolv.conf；其它发行版可能该用 netplan/networkd）",
						Text:  fmt.Sprintf("echo 'nameserver %s' >> /etc/resolv.conf", d),
						Prog:  "sh", Args: []string{"-c", fmt.Sprintf("echo 'nameserver %s' >> /etc/resolv.conf", d)},
						AdminOnly: true,
					})
				}
			}
			return out
		case HostNICDhcp:
			return []HostNICCommand{
				{
					Label: "清掉静态地址", Text: cmdText("ip", "addr", "flush", "dev", iface),
					Prog: "ip", Args: []string{"addr", "flush", "dev", iface}, AdminOnly: true,
				},
				{
					Label: "重新用 DHCP 拿地址", Text: cmdText("udhcpc", "-i", iface),
					Prog: "udhcpc", Args: []string{"-i", iface}, AdminOnly: true,
				},
			}
		}
		return nil
	}
	// 其它平台（darwin 等）不乱编命令：页面只会显示「本平台没有现成命令」
	return nil
}

// HostNICPersistCommands 把这份配置写进固件自己的网络配置（目前只做 OpenWrt 的 UCI）。
//
// ⚠️ 为什么需要它：上面的 `ip addr` / `ip route` 是**运行期**配置 ——
// OpenWrt 上 `/etc/init.d/network reload`（或重启、从 LuCI 改任何网络设置）都会把它抹掉，
// 因为这些接口归 netifd 按 `/etc/config/network` 管。所以路由器上要真正持久，就写 UCI。
//
// 这些命令**只显示不执行**（Manual=true）：接口名/防火墙分区要人判断，
// 一键改 /etc/config/network 风险太大（得自己看一眼再贴）。
func HostNICPersistCommands(p HostNICPlan, goos string) []HostNICCommand {
	if strings.ToLower(goos) != "openwrt" {
		return nil
	}
	p = NormalizeHostNICPlan(p)
	iface, ip := p.Interface, p.IP
	cmd := func(label string, args ...string) HostNICCommand {
		return HostNICCommand{Label: label, Text: cmdText(args[0], args[1:]...), Manual: true}
	}
	out := []HostNICCommand{
		cmd("先删同名的旧接口（没有也不报错）", "uci", "-q", "delete", "network.modem"),
		cmd("建一个独立接口（别把模组口并进 br-lan）", "uci", "set", "network.modem=interface"),
		cmd("静态 IP 协议", "uci", "set", "network.modem.proto=static"),
		cmd("绑到模组那张网卡", "uci", "set", "network.modem.device="+iface),
	}
	if ip != "" {
		out = append(out, cmd("主机侧地址", "uci", "set", "network.modem.ipaddr="+ip))
	}
	if p.Mask != "" {
		out = append(out, cmd("掩码", "uci", "set", "network.modem.netmask="+p.Mask))
	}
	if p.Gateway != "" {
		out = append(out, cmd("网关指向模组", "uci", "set", "network.modem.gateway="+p.Gateway))
	}
	for _, d := range p.DNS {
		out = append(out, cmd("DNS（交给 dnsmasq）", "uci", "set", "network.modem.dns="+d))
	}
	out = append(out,
		// 默认路由冲突：WAN 也有一条 default，metric 小的优先
		cmd("（可选）想让模组这条默认路由优先就调小 metric", "uci", "set", "network.modem.metric=10"),
		cmd("提交", "uci", "commit", "network"),
		cmd("重载生效", "/etc/init.d/network", "reload"),
	)
	return out
}

// HostNICActionSets 三组动作（含提示语），页面按这个顺序排按钮
func HostNICActionSets(plan HostNICPlan, goos string) []HostNICActionSet {
	return []HostNICActionSet{
		{
			Mode: HostNICRenew, Label: "先试 DHCP",
			Hint: "让网卡重新请求一次租约：能拿到地址就说明模组发 DHCP，不用配静态。" +
				"最多等 90 秒 —— 这个模组的 DHCP 实测很慢（几十秒到一两分钟），等满只代表“这次没等到”，" +
				"过一会儿点「刷新」看网卡地址变没变；一直没有就手工配静态。",
			Commands: HostNICCommands(HostNICRenew, plan, goos),
		},
		{
			Mode: HostNICStatic, Label: "应用静态配置",
			Hint:     "FM350 要的就是这一步：把网卡配成与模组同网段、网关指向模组。",
			Commands: HostNICCommands(HostNICStatic, plan, goos),
		},
		{
			Mode: HostNICDhcp, Label: "恢复自动获取",
			Hint:     "撤销手工配置，恢复成 DHCP（换机型 / 还原现场时用）。",
			Commands: HostNICCommands(HostNICDhcp, plan, goos),
		},
	}
}

// ---------- 落盘（hostnic.json，与 quota.json / schedule.json 同一套路） ----------

type hostNICFile struct {
	Version int                    `json:"version"`
	SavedAt time.Time              `json:"saved_at"`
	Plans   map[string]HostNICPlan `json:"plans,omitempty"` // 网卡名 → 配置
}

var (
	HostNICMu     sync.Mutex
	HostNICLoaded bool
	HostNICPlans  = map[string]HostNICPlan{}
)

// HostNICPath 主机网卡配置文件（与配置文件同目录；configPath 为空时退回当前目录）
func HostNICPath(configPath string) string {
	if p := strings.TrimSpace(configPath); p != "" {
		return filepath.Join(filepath.Dir(p), "hostnic.json")
	}
	return "hostnic.json"
}

// hostNICLoadLocked 首次访问时读盘（调用方须持锁）
func hostNICLoadLocked() {
	if HostNICLoaded {
		return
	}
	HostNICLoaded = true
	path := HostNICPath(config.ConfigPath)
	b, err := os.ReadFile(path)
	if err != nil {
		return // 首次运行没有该文件，正常
	}
	var f hostNICFile
	if err := json.Unmarshal(b, &f); err != nil {
		config.Logger.Warn("hostnic.json broken, ignore it", zap.String("path", path), zap.Error(err))
		return
	}
	for k, v := range f.Plans {
		HostNICPlans[k] = NormalizeHostNICPlan(v)
	}
}

// GetHostNICPlan 取某张网卡保存过的配置
func GetHostNICPlan(iface string) (HostNICPlan, bool) {
	HostNICMu.Lock()
	defer HostNICMu.Unlock()
	hostNICLoadLocked()
	p, ok := HostNICPlans[strings.TrimSpace(iface)]
	return p, ok
}

// HostNICPlanNames 已保存过配置的网卡名（按字母序）
func HostNICPlanNames() []string {
	HostNICMu.Lock()
	defer HostNICMu.Unlock()
	hostNICLoadLocked()
	names := make([]string, 0, len(HostNICPlans))
	for k := range HostNICPlans {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// SaveHostNICPlan 保存一张网卡的配置（立即落盘；校验不通过直接报错）
func SaveHostNICPlan(p HostNICPlan) (HostNICPlan, error) {
	p = NormalizeHostNICPlan(p)
	if err := ValidateHostNICPlan(p); err != nil {
		return p, err
	}
	HostNICMu.Lock()
	defer HostNICMu.Unlock()
	hostNICLoadLocked()
	HostNICPlans[p.Interface] = p
	f := hostNICFile{Version: 1, SavedAt: time.Now(), Plans: HostNICPlans}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return p, err
	}
	path := HostNICPath(config.ConfigPath)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return p, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return p, err
	}
	config.Logger.Info("host nic plan saved", zap.String("iface", p.Interface), zap.String("ip", p.IP))
	return p, nil
}

// HostNICOS 当前平台（决定命令形态）
func HostNICOS() string { return runtime.GOOS }
