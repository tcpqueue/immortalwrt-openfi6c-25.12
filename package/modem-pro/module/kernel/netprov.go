package kernel

// ====================== OpenWrt：模组上线自动组网（自动拨号 + 自动建 WAN） ======================
//
// 用户需求（路由器上）：模组一插上就**自动拨号**；拨号成功后看**这个模组的网卡**在 UCI 里
// 有没有对应的接口，没有就自动建一个 WAN 接口（绑到那张网卡）+ 加进防火墙的 wan 区 ——
// 否则新插上的模组拿不到地址、上不了网（每次都要去 LuCI 手工建接口）。
//
// 分层（与 hostnic 一致）：
//   - 本文件：**纯逻辑**（网卡匹配 / UCI 解析 / 命令生成 / 结果记录），可单测、跨平台；
//   - netprov_engine.go（!windows）：真正执行（uci / ifup）与周期循环，只在 Linux + OpenWrt 上跑。
//
// ⚠️ 三条安全约定（都是"别把用户的网络搞坏"）：
//  1. 只认**这台模组自己**的网卡（按 USB 拓扑路径匹配 sysfs，不猜名字）；
//  2. 同名接口已经存在就**不动它**（手工配置优先），只有真的没有才建；
//  3. 找不到防火墙区就只建接口、跳过防火墙那一步（不瞎猜分区）。

import (
	"modemd/module/config"
	"modemd/module/dial"
	"modemd/module/netdev"
	"modemd/module/netprov"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- 网卡"发送队列卡死"自愈（纯决策） ----
//
// 真机（2026-10-08，F50-PRO + FM160-CN，驱动 `qmi_wwan_f`）：`dmesg` 里每 5 秒一条
//
//	qmi_wwan_f 1-1:1.4 wwan0: NETDEV WATCHDOG: CPU: 1: transmit queue 0 timed out 5112 ms
//
// 而地址、默认路由都在（netifd `up=true`），模组侧 `+CEREG: 0,1` + PDP 激活也正常 ——
// **就是包根本发不出去**，用户那边看到的就是"没有网"。当天实测：**重新拨号不管用**，
// 只有 `AT+CFUN=1,1` 复位模组才恢复（复位后 IP 从 10.211.116.197 换成 10.64.212.93、
// ping 0% 丢包）。
//
// 所以拿内核这个计数当"卡死"的判据：每轮记一次，**涨了**才算新故障（历史残留不算）。
const (
	TXWedgeCooldown   = 3 * time.Minute // 两次自动复位之间至少隔这么久
	TXWedgeMaxPerHour = 4               // 一小时内最多自动复位几次（超过就不再重启，让人插手）
)

// TXWedgeDecision 在"内核的发送队列超时计数又涨了"时，决定要不要自动复位模组。
//
//	prev/cur 上一轮 / 这一轮内核里那张网卡的 NETDEV WATCHDOG 条数
//	last     上次自动复位的时间（零值 = 从没做过）
//	tries    最近一小时内的自动复位时间（限流用）
//
// 返回 (act, reason)：act=true ⇒ 该复位模组；reason 非空 ⇒ 给人看的一句话（日志/页面）。
func TXWedgeDecision(prev, cur int, last time.Time, tries []time.Time, now time.Time) (bool, string) {
	if cur <= prev {
		return false, "" // 没有新故障
	}
	if len(tries) >= TXWedgeMaxPerHour {
		return false, "网卡发送队列反复卡死，一小时内已自动复位过多次 —— 不再重启，请人工看一下模组/天线/散热"
	}
	if !last.IsZero() && now.Sub(last) < TXWedgeCooldown {
		return false, "网卡发送队列卡死，但刚复位过（还在冷却），先等一等"
	}
	return true, "网卡发送队列卡死（内核 NETDEV WATCHDOG），自动复位模组"
}

// ---- 自动组网结果（给页面看：/api/dial 的 auto_wan 字段） ----

// AutoWANInfo 一台模组最近一次"自动组网"的结果。
type AutoWANInfo struct {
	At     time.Time `json:"at"`
	Iface  string    `json:"iface,omitempty"`  // UCI 接口名，如 modem0
	Device string    `json:"device,omitempty"` // 主机侧网卡，如 eth2 / wwan0
	// Proto 接口协议：dhcp / none（QMI，拨号由 modemd 托管的 quectel-CM 做）/ mbim
	Proto   string `json:"proto,omitempty"`
	Iface6  string `json:"iface6,omitempty"` // IPv6 兄弟接口，如 modem0_6（只对 dhcp 类协议有）
	Created bool   `json:"created,omitempty"`
	// HostDial 主机侧拨号器（quectel-CM）的状态：跑没跑、跑在哪个网卡、什么参数
	HostDial string `json:"host_dial,omitempty"`
	Note     string `json:"note,omitempty"`  // 干了什么 / 为什么没干
	Error    string `json:"error,omitempty"` // 失败原因（原始错误）
}

var (
	autoWANMu   sync.Mutex
	autoWANByID = map[int]AutoWANInfo{}
	// goodDialCID 每台模组"上次拨成功的 cid"
	goodDialCID = map[int]int{}
)

// setAutoWANInfo 记下最近一次结果
func setAutoWANInfo(id int, info AutoWANInfo) {
	autoWANMu.Lock()
	autoWANByID[id] = info
	autoWANMu.Unlock()
}

// rememberGoodDialCID 记住"这台模组哪个 cid 真的拨通过"，下次自动拨号先试它。
// 内存级即可：拔插/重启后重试一遍候选列表，代价很小。
func rememberGoodDialCID(id, cid int) {
	autoWANMu.Lock()
	goodDialCID[id] = cid
	autoWANMu.Unlock()
}

// preferredDialCID 上次拨成功的 cid（0 = 还没记录过）
func preferredDialCID(id int) int {
	autoWANMu.Lock()
	defer autoWANMu.Unlock()
	return goodDialCID[id]
}

// AutoWANInfoFor 取最近一次结果（ok=false 表示还没跑过 / 平台不支持）
func AutoWANInfoFor(id int) (AutoWANInfo, bool) {
	autoWANMu.Lock()
	defer autoWANMu.Unlock()
	info, ok := autoWANByID[id]
	return info, ok
}

// AutoWANPayload 给接口用的载荷：没跑过就返回 nil（页面上就是"没有这一块"）。
// 用 any 是为了让 JSON 里直接是对象或 null，而不是空结构体。
func AutoWANPayload(id int) any {
	if info, ok := AutoWANInfoFor(id); ok {
		return info
	}
	return nil
}

// WANInterfaceName 自动建的 UCI 接口名（每台模组一个）：modem0 / modem1 …
// 为什么不用固定名 modem：多台模组同时插着时，各自要有一张独立的 WAN 接口。
func WANInterfaceName(id int) string { return "modem" + strconv.Itoa(id) }

// WAN6InterfaceName IPv6 兄弟接口名：modem0_6。
//
// 为什么要有兄弟接口：netifd 的一个 interface 只能对应一条协议（v4 一条、v6 一条），
// 官方固件自己的 WAN 就是 wan + wan6 两张，这里照那个套路来。
func WAN6InterfaceName(id int) string { return WANInterfaceName(id) + "_6" }

// HasIPv6Address 模组侧是不是有 IPv6（地址里带 v6，或者 PDP 类型是 IPV4V6 / IPV6）。
// 有的话才值得去建 v6 兄弟接口 —— 纯 IPv4 的 PDP 建了也是空的。
func HasIPv6Address(st *dial.DialState) bool {
	if st == nil {
		return false
	}
	if strings.TrimSpace(st.IPv6) != "" {
		return true
	}
	return strings.Contains(strings.ToUpper(st.PDPType), "V6")
}

// WANIPv6Enabled 模组这一侧要不要用 IPv6 —— [net] wan_ipv6，页面上「拨号上网」那个开关
// （见 SetWANIPv6）。关掉就整条链路只走 IPv4。
func WANIPv6Enabled() bool { return config.Cfg.Net.WANIPv6 }

// modemWAN6Allowed 这台模组**该不该有** v6 兄弟接口（modem<id>_6）——只看"配置层面"：
//
//	① 开关开着（[net] wan_ipv6）；
//	② 这种协议不是"netifd 自己在同一张接口上就把 v6 配好"的 —— 目前只有 mbim
//	   （mbim.sh / umbim 一次会话里 v4+v6 都配，另建一张会跟它抢同一个前缀）；
//	   历史手工写法（proto=qmi / quectel / none）同理不动。
//	   ⚠️ 2026-10-13 起 MBIM 默认走 modem_pro（见 netprov_proto.go），只会在
//	   `[net] host_dial = false` 时才退回 proto=mbim —— 那时这里才命中。
//
// ⚠️ **只看这两条，不看"模组此刻报没报 v6"**（`HasIPv6Address`）：
//
//	· 那是随链路变化的状态，拿它当"要不要删掉已有接口"的依据，模组重拨的瞬间就会把好接口拆了重建；
//	· QMI 上模组侧现在是**一直 idle**（数据呼叫归主机侧拨号器，我们不再做模组侧自动拨号，
//	  见 releaseHostDialDataContext）—— 再拿它当条件就永远建不出来（实测：用户在
//	  「拨号上网」里打开 IPv6 后，接口列表里连 IPv6 都看不到，就是这么来的）。
//
// 建出来之后它会不会有地址，取决于模组/网络是否真给 v6：给就立刻接上（dhcpv6 / RA），
// 不给就一直 pending（和 OpenWrt 自己的 wan6 一样，无害）。调用方：该建就建；
// 不该有就把已经建过的删掉（disableModemWAN6）。
func modemWAN6Allowed(proto string) bool {
	if !WANIPv6Enabled() {
		return false
	}
	// modem_pro 已经把 v4+v6 跑在**同一张**接口上（IPv6 前缀由它自己从 RA 跟随），
	// 再建一张 modem<id>_6 只会跟它抢同一张网卡。这条与 modemV6SiblingWanted 口径一致。
	if strings.EqualFold(strings.TrimSpace(proto), netprov.ModemProtoName) {
		return false
	}
	// ★ 按 [net] wan_mode：只有模式 2/3 才该有 v6 兄弟接口。
	// 模式 1（默认）一个都不建；模式 2/3 才轮到下面按协议细判。
	if !config.WANModeNow().WantsDHCPv6Sibling() {
		return false
	}
	return !netprov.WANProtoHostDialed(proto)
}

// modemV6SiblingWanted 该不该给这台模组另建 v6 兄弟接口（modem<id>_6）。
//
// 三条约定：
//
//	· [net] wan_ipv6 关掉 ⇒ 一律不建（整条链路只走 IPv4）；
//	· mbim（只在 host_dial 关着时才用这个 proto）：umbim 在同一张接口上就配好 v4+v6 ⇒ 不建；
//	· QMI / MBIM（主机侧拨号器）：wwan0 上的 v4/v6 地址与路由本来都由拨号器给，
//	  但 netifd / 局域网侧那几项 v6 对账（`ip6prefix` 交给内网、LAN 的 v6 路由）
//	  都假定有这么一张 v6 上联，所以模式 2/3 下还是建。
func modemV6SiblingWanted(mod *Modem, proto string) bool {
	if !WANIPv6Enabled() {
		return false
	}
	// MBIM（host_dial 关着）/ 历史手工写法（qmi / quectel / none）：数据面归 netifd 的 mbim.sh 或
	// 主机侧拨号器，它们本来就在一张接口上搞定 v4+v6，再建兄弟接口只会抢同一个前缀。
	if netprov.WANProtoHostDialed(proto) {
		return false
	}
	// ★ 按 [net] wan_mode 决定 —— 取代了原来的 [net] qmi_v6_sibling（那个只看 QMI）：
	//	mode=modem_pro（默认）→ 不建（modem_pro 一张接口管 v4+v6）；
	//	mode=dhcp / both      → 建 modem<id>_6（dhcpv6）。
	// 这样 ECM / NCM / RNDIS 在默认模式下也不会再多出一张接口。
	return config.WANModeNow().WantsDHCPv6Sibling()
}

// wanPlanPDPType 给 netifd 协议脚本（mbim 那条路）用的 PDP 类型：
// 开关关掉时**只写 ip**，免得 umbim 去要 v6 承载。开关开着就按模组上报的来。
func wanPlanPDPType(pdp string) string {
	if !WANIPv6Enabled() {
		return "ip"
	}
	return NormalizePDPType(pdp)
}

// AutoDialCandidates 自动拨号的候选 cid（纯逻辑，按优先级排好）。
//
// 为什么要一串候选：实测模组对 cid 很挑食 —— 鼎桥 MT5700M-CN 把 cid 0/1/2/3 都报成
// “已激活”（AT+CGACT? 全是 1），可真去 AT+CGACT=1,1 却被 ERROR 拒掉，只有 **cid 3** 能真的激活。
// 所以顺序是：
//
//  1. preferred：上次拨成功的那条（见 rememberGoodDialCID）；
//  2. 已激活、非内部的上下文，cid 大的排前面；
//  3. 有 APN 的非内部上下文，cid 大的排前面；
//  4. 兑底 DefaultDataCID。
func AutoDialCandidates(ctxs []Context, preferred int) []int {
	var out []int
	seen := map[int]bool{}
	add := func(cid int) {
		if cid > 0 && !seen[cid] {
			seen[cid] = true
			out = append(out, cid)
		}
	}
	add(preferred)

	byCIDDesc := func(pick func(Context) bool) {
		cands := make([]Context, 0, len(ctxs))
		for _, c := range ctxs {
			if c.Internal || strings.TrimSpace(c.APN) == "" || !pick(c) {
				continue
			}
			cands = append(cands, c)
		}
		sort.Slice(cands, func(i, j int) bool { return cands[i].CID > cands[j].CID })
		for _, c := range cands {
			add(c.CID)
		}
	}
	byCIDDesc(func(c Context) bool { return c.Active })
	byCIDDesc(func(Context) bool { return true })
	add(DefaultDataCID)
	return out
}

// APNForCID 取某个 cid 在模组里配的 APN（找不到返回空串 = 让拨号沿用模组配置）
func APNForCID(ctxs []Context, cid int) string {
	for _, c := range ctxs {
		if c.CID == cid {
			return strings.TrimSpace(c.APN)
		}
	}
	return ""
}

// pickAutoDialCID 自动拨号该用哪条 PDP 上下文（仅用于日志/首次候选），顺带把它配的 APN 带出来。
//
// 跨平台（不碰 uci / 网卡），所以放在这里：自动组的网循环与主机侧拨号器都要用它拿 APN。
func pickAutoDialCID(mod *Modem) (int, string) {
	if mod == nil {
		return DefaultDataCID, ""
	}
	st, err := mod.QueryParams()
	if err != nil || st == nil {
		return DefaultDataCID, ""
	}
	cands := AutoDialCandidates(st.Contexts, preferredDialCID(mod.ID))
	if len(cands) == 0 {
		return DefaultDataCID, ""
	}
	return cands[0], APNForCID(st.Contexts, cands[0])
}

// ---- 模组 → 主机侧网卡 ----

// QMAPParentOf 从 mux 网卡名反推**父**网卡名（拨号器的 `-i` 与驱动属性如 raw_ip 都在父口上）：
//
//	wwan0_1 → wwan0   厂家 qmi_wwan_q 的命名是"父名_muxid"，能直接反推
//	qmimux0 → ""      主线那种是 quectel-CM 自己 add_mux 建的，名字里没有父名 ——
//	                  调用方要从同一台模组的其它网卡里找非 mux 的那张（见 ModemDialNetdev）
func QMAPParentOf(name string) string {
	trimmed := strings.TrimSpace(name)
	if !netdev.IsQMAPMux(trimmed) || strings.HasPrefix(strings.ToLower(trimmed), "qmimux") {
		return ""
	}
	return trimmed[:strings.LastIndex(trimmed, "_")]
}

// ModemDialNetdev 从这台模组名下的网卡里挑出**给拨号器用的那张**（QMAP 下 = 父口）。
//
// 为什么不是"数据网卡"：QMAP 下地址在 mux 上，但 quectel-CM 的 `-i` 要的是**父口** ——
// 它自己按 qmap_mode 去 add_mux / 算 mux 名字（实测：传 `-i wwan0`，它自己去
// `ip addr flush dev wwan0_1` / `ip link set dev wwan0_1 down`，地址也落在 wwan0_1）；
// raw_ip 之类的驱动属性同样挂在父口上。
// names 是 netdev.MatchNetdevToBusPath 的返回值（已按"数据口优先"排序）。
func ModemDialNetdev(names []string) string {
	for _, n := range names {
		if !netdev.IsQMAPMux(n) {
			return n
		}
	}
	if len(names) > 0 {
		// 全是 mux（少见）：能反推就反推，推不出来返回空串 —— 调用方不加 `-i`，
		// 让拨号器自己去认（总比把 mux 名当父口传进去强）。
		return QMAPParentOf(names[0])
	}
	return ""
}

// ---- UCI 配置（只要够找防火墙的区、判断接口有没有） ----

// UCISection UCI 配置里的一节
type UCISection struct {
	Type    string              `json:"type"`
	Name    string              `json:"name,omitempty"`    // 匿名段为空（用 Index 定位）
	Index   int                 `json:"index"`             // 同类型里的序号（0 基）
	Options map[string]string   `json:"options,omitempty"` // option k 'v'
	Lists   map[string][]string `json:"lists,omitempty"`   // list k 'v'（可多次）
}

// uciTokens 把一行拆成 token（处理 '…' / "…" 两种引号；够用就好）
func uciTokens(line string) []string {
	var out []string
	var cur strings.Builder
	var quote byte
	inTok := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, inTok = c, true
		case c == ' ' || c == '\t' || c == '\r':
			if inTok {
				out = append(out, cur.String())
				cur.Reset()
				inTok = false
			}
		default:
			cur.WriteByte(c)
			inTok = true
		}
	}
	if inTok {
		out = append(out, cur.String())
	}
	return out
}
