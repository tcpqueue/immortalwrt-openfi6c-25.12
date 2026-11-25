package kernel

import (
	"modemd/module/config"
	"modemd/module/hostdial"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// hostDialRun 一次 quectel-CM / simcom-cm 运行
type hostDialRun struct {
	cmd     *exec.Cmd
	bin     string
	dev     string
	args    []string
	started time.Time
	// running 进程还在不在（由 Wait 协程置回 false）
	running bool
	// done 进程退出时关闭（停的时候等它）
	done chan struct{}

	// exitedAt / lastErr 上一次退出的时间与原因（退避与日志用）
	exitedAt time.Time
	lastErr  string
	// fastFails 连续"起来就挂"的次数（决定退避多久）
	fastFails int

	// qmuxErr / qmuxErrAt 最近一次"模组拒绝数据呼叫"的原话与时间（从拨号器输出里抓的）。
	// 用来在页面上解释"拨号器一直在跑、但一直没有地址"里最讨厌的那一种：模组的 QMI
	// 数据服务卡住了（见 hostDialStuckHint）。
	qmuxErr   string
	qmuxErrAt time.Time
}

// hostDialNote 正在跑的拨号器的一句话说明（不含运行时长：那样每轮都会"变化"而刷日志）
// hostDialStuckHintAfter 拨号器跑这么久还没拿到地址 → 给一句排查方向
const hostDialStuckHintAfter = 90 * time.Second

// hostDialKillStrayWait killall 掉残留拨号器之后等这么久再拉起新的。
// 留时间让内核把 QMI 控制口（/dev/cdc-wdm0）与网卡地址释放干净。
const hostDialKillStrayWait = 1200 * time.Millisecond

// hostDialHasAddr 这张网卡上有 IPv4 地址了吗（拨号器拿到租约的标志）。
// 纯标准库实现，Windows 上也能编（只是用不到）。可注入是为了好测（开发机上没有 wwan0）。
var hostDialHasAddr = func(dev string) bool {
	in, err := net.InterfaceByName(dev)
	if err != nil {
		return false
	}
	addrs, err := in.Addrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP != nil && ipn.IP.To4() != nil {
			return true
		}
	}
	return false
}

// hostDialStuckHint 拨号器在跑、但网卡上一直没地址时补一句"为什么拨不上"。
//
// 只对 SIMCom 说这句，因为这是 SIMCom 特有的坑（真机实测 2026-10-01）：
// simcom-cm 判断"能不能拨"看的是 **QMI NAS 的 EPS 注册**，日志里就是它一直刷
//
//	requestRegistrationState2 MCC: 0, MNC: 0, PS: Detached, DataCap: UNKNOW
//
// 而这张 SIM8200EA-M2 在**纯 NR5G-SA** 下就是这个状态：AT 侧 `+C5GREG: 0,1`（5G 已注册）、
// `+CGATT: 1`（已附着）、`+CPIN: READY`，但 `+CEREG: 0,0`（没有 EPS 注册）——
// simcom-cm 于是既不拨也不报错，就这么一直空转（页面看着"拨号器正在跑"，就是没地址）。
// 把网络模式锁到 LTE / LTE+NR（`AT+CNMP=38` / `109`）后 `+CEREG` 变 `0,1`，
// 同一张卡、同一套代码立刻拨上（实测 wwan0=10.53.199.114/30 + 默认路由）。
//
// 所以这里只是**提示**，不自动改用户的网络模式（锁网是用户的业务选择，modemd 不替他决定）。
//
// 另一类是 **QMI 数据服务被卡住**（实测 RM502Q-GL + 厂家 qmi_wwan_q，2026-10-03）：
// 拨号器每轮都被模组拒 ——
//
//	requestSetupDataCall QMUXResult = 0x1, QMUXError = 0xe
//	call_end_reason is 1 / type is 2 / verbose is 241（IPv6 那条是 210）
//
// 而同一时刻 **AT 侧拨号完全正常**（`AT+CGACT=1,3` 立刻拿到真实地址）、所有数据上下文
// 都停用了也没用 —— 只有 `AT+CFUN=1,1` 重启模组才恢复。触发点很可能是**拨号器被强杀**：
// 它退出前的收尾（StopNetworkInterface / 停 PDP）被打断，模组那边的会话就半开着，
// 之后所有新的 StartNetworkInterface 都被拒（所以 hostDialStopWait 调到了 10 秒）。
// 这种情况下拨号器自己不会恢复（它只会一直按 40/60 秒退避重试），页面得说清楚该干什么。
func hostDialStuckHint(mod *Modem, r *hostDialRun, dev string) string {
	if mod == nil || r == nil || !r.running || time.Since(r.started) < hostDialStuckHintAfter {
		return ""
	}
	if hostDialHasAddr(dev) {
		return "" // 已经有地址了，不用解释
	}
	// QMI 数据服务卡住：拨号器输出里最近还在报 "模组拒绝数据呼叫"
	if r.qmuxErr != "" && time.Since(r.qmuxErrAt) < 3*time.Minute {
		return "（拨号器一直被模组拒：" + r.qmuxErr + " —— 模组的 QMI 数据服务可能卡住了：" +
			"AT 侧拨号正常也没用（实测 QMUXError 0xe 那种），" +
			"在「模组信息」里点一次「重启模组」（AT+CFUN=1,1）就能恢复）"
	}
	// SIMCom 的 EPS 注册坑（见上）
	if hostdial.SimcomStyle(mod.Vendor) {
		return "（跑了这么久还没地址：simcom-cm 只看 QMI 的 EPS 注册，纯 5G SA（+C5GREG 已注册但没有 +CEREG）" +
			"下它会一直报 PS: Detached、拨不上去；把网络模式锁到 LTE/NSA（AT+CNMP=38 或 109）试试）"
	}
	return ""
}

// ---- 掉线自愈：把拨号器重开一遍 ----
//
// 用户 2026-10-09 定的口径（QMI 掉线怎么恢复）：
//
//	"qmi 模式掉线后 就重新关闭软件 在重启执行 拨号软件就行啊"
//
// 也就是：**别去动 netifd 的配置（不改 proto、不反复 ifup），把拨号器停掉再拉起来**。
// 实测 2026-10-09 FM160-CN：掉线后 wwan0 carrier=0 / 网卡 DOWN、Dialer 还在跑
// （日志停在 requestSetupDataCall / call_end_reason 2509），一直等也不会自己好；
// 手动停→启一次 Dialer，几秒内 carrier=1、DHCP 拿到 10.54.29.212/29 + 默认路由。
//
// 判据：**拨号器在跑、网卡上却一直没有 IPv4 地址**，且已经跑过 hostDialRestartAfter；
// 限速 / 限次，免得链路上真没信号时在那儿一次次重开。拿到地址就清零。
const (
	hostDialRestartAfter = 60 * time.Second  // 拨号器跑了这么久还没地址（含刚启动的拨号时间）
	hostDialRestartGap   = 120 * time.Second // 两次重启之间至少隔这么久
	hostDialRestartMax   = 10                // 连续重启这么多次还没好就只在日志里说，不再重开
)

// hostDialRestartState 一台模组的"重启拨号器"计数
type hostDialRestartState struct {
	last  time.Time
	count int
}

var (
	hostDialRestartMu sync.Mutex
	hostDialRestarts  = map[int]hostDialRestartState{}
)

// resetHostDialRestart 拿到地址（或模组没了）⇒ 计数清零
func resetHostDialRestart(id int) {
	hostDialRestartMu.Lock()
	delete(hostDialRestarts, id)
	hostDialRestartMu.Unlock()
}

// hostDialRestartDue 纯决策：现在该不该把拨号器重启一遍（先调方自己确认过"没地址"）。
//
//	started 这一轮拨号器的启动时间（刚拉起来先给它时间拨，别刚起就杀）
//	st      这台模组的历史
func hostDialRestartDue(started time.Time, st hostDialRestartState, now time.Time) (bool, hostDialRestartState) {
	if st.count >= hostDialRestartMax {
		return false, st
	}
	if !st.last.IsZero() && now.Sub(st.last) < hostDialRestartGap {
		return false, st
	}
	if started.IsZero() || now.Sub(started) < hostDialRestartAfter {
		return false, st
	}
	st.last, st.count = now, st.count+1
	return true, st
}

func (r *hostDialRun) note() string {
	if r == nil {
		return ""
	}
	if !r.running {
		return ""
	}
	name := "quectel-CM"
	if r.bin != "" {
		name = filepath.Base(r.bin)
	}
	return name + " 正在跑（" + strings.Join(r.args, " ") + "，pid " +
		strconv.Itoa(r.pid()) + "）"
}

func (r *hostDialRun) pid() int {
	if r == nil || r.cmd == nil || r.cmd.Process == nil {
		return 0
	}
	return r.cmd.Process.Pid
}

var hostDials = struct {
	mu   sync.Mutex
	runs map[int]*hostDialRun
}{runs: map[int]*hostDialRun{}}

// HostDialStatusFor 给页面/接口看的拨号器状态（没跑就是空串）
func HostDialStatusFor(id int) string {
	hostDials.mu.Lock()
	defer hostDials.mu.Unlock()
	return hostDials.runs[id].note()
}

// hostDialArgs quectel-CM 的参数（广和通 fibocom-dial / 美格 meig-cm 同源，参数一致）。
//
//	-i <网卡>   指定数据网卡（QMAP 模组给**父口** wwan0：它自己按 qmap_mode 去 add_mux /
//	            算 mux 名字与数据口。名字拿不到时**整个 -i 都不传**，让它自己认 ——
//	            传空值会变成 `-i -4`，那是把 "-4" 当网卡名，比不传还糟）
//	-4 / -6     要 IPv4 / IPv6（[net] wan_ipv6 开着时两个都要）
//	-s <apn>    运营商 APN（模组里配好的那个）；模组里有配置时不传也行
//	-n <pdn>    QMAP 模组要指定用哪条 PDN —— 就是 mux 网卡 `wwan0_<n>` 里的那个 <n>
//	            （见 hostDialPDN）。不传时 quectel-CM 自己默认 pdn=1；显式传上更稳，
//	            免得驱动的 mux 没被它自动认出来时拨到父口上去。
//
// 故意不用 -d（"用 QMI 直接取 IP，不跑 DHCP"）：默认这条 DHCP 路子是 quectel-CM 的老
// 套路，各模组兼容性最好（模组自己当数据网卡上的 DHCP 服务器）。
func hostDialArgs(dev, apn string, v6 bool, pdn int) []string {
	args := []string{}
	if dev = strings.TrimSpace(dev); dev != "" {
		args = append(args, "-i", dev)
	}
	args = append(args, "-4")
	if v6 {
		args = append(args, "-6")
	}
	if apn = strings.TrimSpace(apn); apn != "" {
		args = append(args, "-s", apn)
	}
	if pdn > 0 {
		args = append(args, "-n", strconv.Itoa(pdn))
	}
	return args
}

// hostDialPDN 从**数据网卡**名反推拨号器该用的 PDN（= QMAP 的 mux 序号）。
//
// 厂家 qmi_wwan_q 的 mux 口命名规则是 `<父名>_<序号>`（实测 RM502Q-GL：wwan0 父口 + wwan0_1 数据口），
// 而这个序号正是 quectel-CM / fibocom-dial / meig-cm 的 `-n <pdn>`：
//
//	wwan0_1 → 1，wwan1_2 → 2
//
// 认不出（wwan0 / qmimux0 / usb0 / 空）→ 0，调用方就不传 `-n`（拨号器按自己的默认来）。
func hostDialPDN(dataDev string) int {
	name := strings.ToLower(strings.TrimSpace(dataDev))
	i := strings.LastIndexByte(name, '_')
	if i <= 0 || i == len(name)-1 {
		return 0
	}
	n, err := strconv.Atoi(name[i+1:])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// hostDialPDNFor 决定给拨号器传的 `-n <pdn>`。
//
// 两步：先按老规矩从**数据网卡名**反推（`wwan0_1` → 1）；认不出来时再看驱动是不是
// QMAP（见 hostDialQMAPModeOn）—— 是就按**默认承载 PDN 1** 传（模组的 cid 1，
// 就是 APN 配的那条，见 `AT+CGDCONT?`）。
//
// 为什么非补这一手不可：QMAP 驱动下 mux 口是**拨号器自己 add_mux 建出来的**，
// 第一次拨号前它根本不存在，名字里自然没有序号 —— 光看名字永远反推不到，
// 拨号器就一直不建 mux、父口一直没载波（FM160 实测，见 hostDialQMAPModeOn）。
//
// 配置 [net] host_dial_pdn 可以压过上面这两步：>0 强制用那个序号，<0 一律不传。
func hostDialPDNFor(dialDev, dataDev string) int {
	if p := config.Cfg.Net.HostDialPDN; p > 0 {
		return p
	} else if p < 0 {
		return 0
	}
	if n := hostDialPDN(dataDev); n > 0 {
		return n
	}
	if hostDialQMAPModeOn != nil && hostDialQMAPModeOn(dialDev) {
		return 1
	}
	return 0
}

// hostDialWantIPv6 要不要给拨号器加 -6（IPv6 承载）：**只看 [net] wan_ipv6 那个开关**。
//
// ⚠️ 不再看"模组报没报 v6"：QMI 上模组侧现在**一直 idle**（数据呼叫归主机侧拨号器，
// 我们不再做模组侧自动拨号，见 releaseHostDialDataContext），拿那个当条件就永远拿不到 v6
// （实测 RM502Q-GL 上正是如此）。模组/网络到底给不给 v6 由拨号器自己判断：
// quectel-CM 拿不到 v6 承载时 IPv4 照常工作，不会把 v4 带坏。
func hostDialWantIPv6() bool {
	return WANIPv6Enabled()
}

// hostDialArgsFor 按厂商拼参数。
//
// ⚠️ simcom-cm 的参数**比 quectel-CM 少**，它的 usage 串也是**过期的**：
//
//	Usage: simcom-cm [-s [apn [user password auth]]] [-p pincode] [-f logfilename]
//
// 但源码里的 getopt 实际认：`-s` `-p` `-n`（pdp）`-f` `-i`（网卡）`-v` `-l` `-6`
// （third_party/simcom-cm/src/main.c 的 switch）。**唯独没有 `-4`** —— 它落到 default
// 分支直接打印 usage 并**立刻退出**。我们早先踩的坑就是这么来的：
// `simcom-cm -s 3GNET -4 -6` 打印 usage 后退出（日志里几秒刷一次 usage），
// 当时误判成"它不认 -i/-4/-6"，其实是 **`-4` 这一个**把它打发了（IPv4 本来就是默认）。
//
//	-6  →  profile.IsDualIPSupported |= (1 << IpFamilyV6)
//	       它于是去要**IPv6 的 WDS 会话**（QMUX_TYPE_WDS_IPV6 = 0x11）、并读回 v6 地址
//	       （QMIThread.c 的 requestSetupDataCall / requestGetIPAddress）。
//	       地址最后落到网卡那一步它并不自己做：udhcpc.c 里是
//	       `route -A inet6 add default <if>` + **`dibbler-client run`**（第三方 DHCPv6 客户端，
//	       要自己交叉编译 + /etc/dibbler/client.conf，本包不带）。所以只靠拨号器，
//	       IPv6 地址往往还是上不了网卡 —— 这就是为什么 **modemd 还要给 QMI 也建
//	       v6 兄弟接口（modem0_6，dhcpv6）+ 打开网卡 accept_ra**：模组只要在链路上
//	       发了 RA/DHCPv6，netifd 就能把地址与默认路由接上（见 netprov.go 的 modemWAN6Allowed）。
//
// 网卡名它自己认（日志：`Find qmichannel = /dev/cdc-wdm0` / `Find usbnet_adapter = wwan0`），
// 所以这里**不传 `-i`**（虽然它认这个开关）—— 保持和实测跑通的那套一致。
// 代价：多张 SIMCom 模组时无法指定用哪一张（quectel-CM 有 `-i` 没这个问题）。
//
// 广和通 fibocom-dial 与美格 meig-cm 的参数与 quectel-CM **完全一致**
// （各自 usage 里都有 `-s [apn [user password auth]]`、`-i interface`、`-4`、`-6`、`-n pdn`，
// 源码：third_party/fibocom-dial/src/main.c、third_party/meig-cm/src/main.c），
// 所以她们走同一条参数拼装路径。
//
// pdn 来自数据网卡名（hostDialPDN）：QMAP 模组要显式 `-n <mux序号>` 才会去拨
// `wwan0_<n>` 那条 mux，否则可能拨到没有 IP 的父口上（用户实测移远的正确用法：
// `quectel-CM -i wwan0 -4 -6 -s <apn> -n 1`）。
func hostDialArgsFor(vendor ModemVendor, dev, apn string, v6 bool, pdn int) []string {
	if hostdial.SimcomStyle(vendor) {
		args := []string{}
		if apn = strings.TrimSpace(apn); apn != "" {
			args = append(args, "-s", apn)
		}
		// 开关开着时才要 v6 承载（不传 -4：它不认）
		if v6 {
			args = append(args, "-6")
		}
		return args
	}
	return hostDialArgs(dev, apn, v6, pdn)
}

// hostDialArgDev 拨号器的 `-i` 该传哪张网卡：
//
//	USB 模组 → 传我们按拓扑找到的那张（QMAP 下是父口，见 ModemDialNetdev）
//	PCIe（MHI / wwan）模组 → **传空串**（= 不加 `-i`，让拨号器自己认）
//
// 抽成函数是为了能被单测钉住：这个"不传 -i"的决定是从 vendored 的 quectel-CM 源码里
// 得出的（-i 是筛选语义，传错名字直接找不到设备），改错了会让所有 PCIe 模组拨不上号。
func hostDialArgDev(mod *Modem, dev string) string {
	if PCIeInfoFor(mod) != nil {
		return ""
	}
	return dev
}
