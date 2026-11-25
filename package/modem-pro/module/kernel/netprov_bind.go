package kernel

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"modemd/module/netprov"
)

// HostV6FollowUp 看门狗决策（纯逻辑，好单测）。
//
//	staleFor 主机侧与模组侧前缀一直对不上已经多久（对得上传 0）
//	sinceTry 距离上次弹 PDP 过了多久（没弹过传 ≥ 冷却的值）
//	tries    已经连续弹过几次
func HostV6FollowUp(staleFor, sinceTry time.Duration, tries int) HostV6Action {
	if staleFor < HostV6StaleAfter {
		return HostV6Nothing
	}
	if tries >= MaxHostV6Bounces {
		return HostV6GiveUp
	}
	if sinceTry < HostV6RetryAfter {
		return HostV6Nothing
	}
	return HostV6Bounce
}

// PrefixOfV6Addr 把模组报的单个 IPv6 地址折算成它所在的 /64 前缀。
//
//	"240e:478:620:163a:18dc:9cc:9f91:8826" → "240e:478:620:163a::/64"
//
// 只认公网（link-local / ULA / 私有都不算），复用 GlobalIPv6PrefixOf 的判据。
func PrefixOfV6Addr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if !strings.Contains(addr, "/") {
		addr += "/64" // 模组只报地址，补个长度才能算出前缀
	}
	p, _ := GlobalIPv6PrefixOf(addr)
	return p
}

// ModemV6CID 从 `AT+CGPADDR` 的应答里挑出**带公网 IPv6**的那个 PDP 上下文（cid + 它所在的 /64）。
//
// ⚠️ 为什么要"挑"：多上下文的模组里带 v6 的往往只有一个 —— 实测 RM500U-CNV 同时有
// `cid 1 IPV4V6（有公网 v6）` 和 `cid 3 IPV4V6（只有 v4，v6 全 0）`，而自动拨号那次落在 **cid 3**
// 上 ⇒ 光看"当前拨号的 cid"会去弹一个根本没有 v6 的上下文（弹了也不管用）。
//
// 挑法：preferCID（当前拨号的 cid）有公网 v6 就用它；否则按上报顺序取第一个有的
// （IMS 那类上下文的前缀通常是全 0，已被 SplitAddrs 的 HasZeroPrefix 过滤掉）。
func ModemV6CID(resp []string, preferCID int) (int, string) {
	type hit struct {
		cid    int
		prefix string
	}
	var hits []hit
	for _, line := range resp {
		m := reCGPADDR.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		cid, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		_, v6 := SplitAddrs(m[2])
		if p := PrefixOfV6Addr(v6); p != "" {
			hits = append(hits, hit{cid: cid, prefix: p})
		}
	}
	for _, h := range hits {
		if h.cid == preferCID {
			return h.cid, h.prefix
		}
	}
	if len(hits) > 0 {
		return hits[0].cid, hits[0].prefix
	}
	return 0, ""
}

// isManagedIface 这个名字是不是"我们建的"（modem0 / modem0_6 / modem12 / modem12_6 …）。
//
// ⚠️ 为什么要判：主机侧网卡名**不是固定的** —— cdc_ncm/cdc_ether 拿到的是 ethN，
// qmi_wwan 是 wwanN，N 由枚举顺序决定，插拔一次就可能从 eth2 变成 eth3。
// 接口名是我们自己的（modem<id>）不变，底下的设备名会变，所以只有"我们这套名字"的接口
// 才允许跟着改绑；别人手工建的接口只提醒、不动。
func isManagedIface(name string) bool {
	n, ok := strings.CutPrefix(strings.TrimSpace(name), "modem")
	if !ok {
		return false
	}
	n = strings.TrimSuffix(n, "_6")
	if n == "" {
		return false
	}
	for _, r := range n {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// RebindInterfaceCommands 把接口改绑到新的网卡/控制口（同名接口，只是底层设备换了名字）。
//
//	controlDev=true（mbim；qmi / quectel 历史写法）：device 指的是控制口字符设备
//	  （/dev/cdc-wdmN），**不要**写 ifname。
//	controlDev=false（ecm/ncm/rndis / none）：device 与 legacy ifname 都写同一张网卡。
func RebindInterfaceCommands(iface, dev string, controlDev bool) [][]string {
	cmds := [][]string{{"uci", "set", "network." + iface + ".device=" + dev}}
	if !controlDev {
		cmds = append(cmds, []string{"uci", "set", "network." + iface + ".ifname=" + dev})
	}
	cmds = append(cmds, []string{"uci", "commit", "network"}, []string{"ifup", iface})
	return cmds
}

// controlDeviceProtoOptions 只有"device 是控制口字符设备"那几种协议（mbim / qmi / quectel）
// 才认识的选项（见 mbim.sh / quectel.sh / qmi.sh 的 proto_config_add_* 清单）。
//
// 协议从这些换成 modem_pro / dhcp 时要顺手删掉：不删的话它们会一直留在 UCI 里 ——
// netifd 的 modem_pro 不认这些，但 LuCI / `uci show` 会照实显示，用户看到的就是
// "改了协议怎么还挂着 APN / pdptype 这些旧字段"（2026-10-13 MBIM 改成走 modem_pro 时加）。
var controlDeviceProtoOptions = []string{
	"apn", "pdptype", "auth", "pincode", "username", "password",
	"allow_roaming", "allow_partner", "dhcp", "dhcpv6", "delay", "devpath",
}

// staleControlDeviceOptions 纯函数：从"这个 section 上**已经存在**的选项名"里挑出该删的。
//
// ⚠️ 必须先知道哪些选项真的存在再删：`uci -q delete` 对**不存在的键依然返回 1**
// （真机实测 2026-10-13，OpenWrt 23.05 的 uci），而 runNetProvCmds 遇到第一处失败就停
// ⇒ 盲删会把后面的 `uci commit` 与 `ifup` 一起带停（接口既没改成、也不重拨）。
//
// ⚠️ 结果**去重**：同一台机器上 `apn` 与 `Apn` 会归一成同一个选项名，重复删第二次必然失败
// （还是上面那个"delete 不存在的键返回 1"）。
func staleControlDeviceOptions(curOpts []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, opt := range curOpts {
		o := strings.ToLower(strings.TrimSpace(opt))
		if seen[o] {
			continue
		}
		for _, want := range controlDeviceProtoOptions {
			if o == want {
				seen[o] = true
				out = append(out, want)
				break
			}
		}
	}
	return out
}

// SwitchProtoCommands 把接口就地换个协议（改 proto / 重新绑设备，并清掉旧协议特有的选项）。
//
// 用途：老版本自动建的接口写过 proto=qmi（uqmi 拨）/ quectel（netifd 跑 quectel-CM）/
// mbim（netifd 跑 umbim），上一版又试过 none（不配置协议）；现在的口径是
// **proto=modem_pro（模式 1/3）或 dhcp（模式 2）+ 网卡**（地址/路由由 netifd 取，
// 数据呼叫归 modemd 自己托管的厂商拨号器，见 hostdial.go）。
// 这些都是我们**自己建**的接口（modem<id>），就地改掉最省事 ——
// 不用让用户去 LuCI 里删了重建。
//
// device 要从**控制口字符设备**（/dev/cdc-wdm0）改绑到**网卡**（wwan0），所以 ifname 也要写。
// ifname 这个 legacy 键是给老版 LuCI（Lua）看的：它的网络模型只认 ifname，光有 device 时
// 「网络 → 接口」会显示成「不支持的协议类型」（真机踩过）。
//
// curOpts = 这个接口**当前已有的选项名**（来源见 netprov_engine.go 的 uciOptionNames）；
// 只有里面确实存在的控制口时代选项才会被删（原因见 staleControlDeviceOptions）。
//
// ifup 会让 netifd 重起这张接口：老协议脚本拉起来的拨号器（quectel.sh 的 quectel-CM、
// mbim.sh 的 umbim、qmi.sh 的 uqmi）随之被 netifd 收掉。
func SwitchProtoCommands(iface, proto, dev string, curOpts []string) [][]string {
	cmds := [][]string{
		{"uci", "set", "network." + iface + ".proto=" + proto},
	}
	if dev = strings.TrimSpace(dev); dev != "" {
		cmds = append(cmds,
			[]string{"uci", "set", "network." + iface + ".device=" + dev},
			[]string{"uci", "set", "network." + iface + ".ifname=" + dev},
		)
	}
	// 目标协议不吃"控制口"那套选项 ⇒ 把旧的清掉（否则会在 LuCI 里一直挂着）
	if !netprov.WANProtoUsesControlDevice(proto) {
		for _, opt := range staleControlDeviceOptions(curOpts) {
			cmds = append(cmds, []string{"uci", "delete", "network." + iface + "." + opt})
		}
	}
	cmds = append(cmds, []string{"uci", "commit", "network"}, []string{"ifup", iface})
	return cmds
}

// modemIDFromIface "modem3" / "modem3_6" → 3（不是我们这套名字就 false）
func modemIDFromIface(name string) (int, bool) {
	if !isManagedIface(name) {
		return 0, false
	}
	n := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(name), "modem"), "_6")
	id, err := strconv.Atoi(n)
	if err != nil {
		return 0, false
	}
	return id, true
}

// ParseManagedWANInterfaces 从 `uci show network` 的输出里挑出**我们管的**接口。
//
// 用途：模组拔掉后要把它建的接口与防火墙条目一起删干净（动态配置）。
// 只认我们那套名字，别人手工建的接口一律不碰。
//
// 认两种行（uci show 的输出就是这个形状）：
//
//	network.modem0=interface
//	network.modem0.device='eth2'
func ParseManagedWANInterfaces(uciShow string) []netprov.ManagedWANIface {
	declared := map[string]bool{}
	byName := map[string]*netprov.ManagedWANIface{}
	for _, raw := range strings.Split(uciShow, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "network.") {
			continue
		}
		body := strings.TrimPrefix(line, "network.")
		eq := strings.Index(body, "=")
		if eq < 0 {
			continue
		}
		key, val := body[:eq], unquoteUCIValue(body[eq+1:])
		// 段声明：network.<name>=interface
		if !strings.Contains(key, ".") {
			if val == "interface" && isManagedIface(key) {
				declared[key] = true
			}
			continue
		}
		// 选项：network.<name>.<opt>=<val>
		dot := strings.LastIndex(key, ".")
		name, opt := key[:dot], key[dot+1:]
		if !isManagedIface(name) {
			continue
		}
		it, ok := byName[name]
		if !ok {
			it = &netprov.ManagedWANIface{Name: name}
			it.ModemID, _ = modemIDFromIface(name)
			byName[name] = it
		}
		switch opt {
		case "device":
			it.Device = val
		case "proto":
			it.Proto = val
		}
	}
	out := make([]netprov.ManagedWANIface, 0, len(declared))
	for name := range declared {
		it := netprov.ManagedWANIface{Name: name}
		it.ModemID, _ = modemIDFromIface(name)
		if got, ok := byName[name]; ok {
			it.Device, it.Proto = got.Device, got.Proto
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// unquoteUCIValue 去掉 uci show 里值两边的引号
func unquoteUCIValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 {
		if (v[0] == '\'' && v[len(v)-1] == '\'') || (v[0] == '"' && v[len(v)-1] == '"') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// forgetAutoWAN 模组拔掉后把内存里的记账清掉（同一个 id 以后可能被别的模组复用）
func forgetAutoWAN(id int) {
	autoWANMu.Lock()
	delete(autoWANByID, id)
	delete(goodDialCID, id)
	autoWANMu.Unlock()
	autoDialMu.Lock()
	delete(autoDialTried, id)
	autoDialMu.Unlock()
}

// autoDialRetry 自动拨号失败后的重试间隔（别每 15 秒就去戳一次模组）
const autoDialRetry = 60 * time.Second

var (
	autoDialMu    sync.Mutex
	autoDialTried = map[int]time.Time{}
)

// autoDialAllowed 距离上次尝试够久了吗（同一个模组 60 秒里只试一次）
func autoDialAllowed(id int) bool {
	autoDialMu.Lock()
	defer autoDialMu.Unlock()
	if last, ok := autoDialTried[id]; ok && time.Since(last) < autoDialRetry {
		return false
	}
	autoDialTried[id] = time.Now()
	return true
}

// resetAutoDialTried 清掉"自动拨号刚试过"的退避（关飞行模式后要立刻能重拨）
func resetAutoDialTried(id int) {
	autoDialMu.Lock()
	delete(autoDialTried, id)
	autoDialMu.Unlock()
}

// sysctlRoot 内核参数根目录（单测里换成临时目录；生产就是 /proc/sys）。
// 放在这个跨平台文件里，是为了让 setNetdevAcceptRA 的单测在 Windows 上也能跑。
var sysctlRoot = "/proc/sys"

// setNetdevAcceptRA 改网卡的 accept_ra（2 = 即使本机在转发也接受 RA）。
//
// 为什么必须改：OpenWrt 默认 accept_ra=0，而路由器又在转发（forwarding=1），
// 这时内核**收不到 RA**，模组这种"用 RA（SLAAC）发 IPv6"的上联就永远只有 link-local；
// 而 netifd 的 dhcpv6 脚本并不管这个参数（实测 /lib/netifd/proto/dhcpv6.sh 里没有它）。
// 返回是否真的改了（已经是目标值就不动，循环每 15 秒会跑一次）。
func setNetdevAcceptRA(dev string, val int) (bool, error) {
	if !netprov.ValidNetdevName(dev) {
		return false, fmt.Errorf("网卡名 %q 不合法", dev)
	}
	p := filepath.Join(sysctlRoot, "net/ipv6/conf", dev, "accept_ra")
	cur, err := os.ReadFile(p)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(string(cur)) == strconv.Itoa(val) {
		return false, nil
	}
	if err := os.WriteFile(p, []byte(strconv.Itoa(val)), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
