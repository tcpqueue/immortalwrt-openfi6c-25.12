//go:build !windows

package kernel

// ====================== 主机侧拨号器：平台相关部分（Linux / OpenWrt） ======================
//
// 两件事：
//
//	① 进程组：quectel-CM 会 popen 一个 `busybox udhcpc`，父进程被杀时它要跟着死，
//	   否则那个 udhcpc 会一直占着网卡的地址（真机踩过残留进程把控制口占死的坑）；
//	② 路由收尾：模组当 DHCP 服务器时，地址与默认路由是 busybox udhcpc 的
//	   /usr/share/udhcpc/default.script 装的，那条 `route add default gw <gw> dev wwan0`
//	   **没有 metric**（= 0），会跟路由器自己的上联 WAN 抢默认路由。

import (
	"context"
	"fmt"
	"go.uber.org/zap"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"modemd/module/config"
	"modemd/module/netprov"
)

// hostDialPrepareCmd 让拨号器跑在自己的进程组里（停的时候按 -pid 整组收）
func hostDialPrepareCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// hostDialTerminate 先礼后兵：给整个进程组 SIGTERM
func hostDialTerminate(pid int) {
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
	}
}

// hostDialKill 收不掉就整组 SIGKILL
func hostDialKill(pid int) {
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}

func init() {
	hostDialRouteFix = reconcileHostDialRoute
	hostDialRawIPFix = prepareQMIWwanRawIP
	hostDialFlushNetdev = flushHostDialNetdev
	hostDialDevUpFix = ensureHostDialNetdevUp
	hostDialHasCarrier = hostDialNetdevHasCarrier
	hostDialQMAPModeOn = qmapModeOn
}

// hostDialNetdevHasCarrier 读 /sys/class/net/<dev>/carrier（1 = 有载波）。
//
// QMI 掉线时 wwan0 的 carrier 会变 0（网卡可能还是 UP、旧地址也还挂着），
// 光看“有没有地址”会漏掉这种情况。读不到（网卡没了）也算没载波。
func hostDialNetdevHasCarrier(dev string) bool {
	dev = strings.TrimSpace(dev)
	if dev == "" {
		return false
	}
	b, err := os.ReadFile(filepath.Join("/sys/class/net", dev, "carrier"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(b)) == "1"
}

// qmapModeOn 驱动是不是按 QMAP 复用在工作：`/sys/class/net/<dev>/qmap_mode` == 1。
//
// ⚠️ 这个属性是**只读**的（实测 FM160：`echo 0 > qmap_mode` → Permission denied），
// 由厂家驱动（qmi_wwan_f / qmi_wwan_q）在模组枚举时按模组侧配置定死 —— 所以不能
// "关掉 QMAP"，只能顺着它给拨号器补 `-n <pdn>`（见 hostdial.go 的 hostDialQMAPModeOn）。
// 读不到（主线 qmi_wwan 没这个属性 / 网卡名空）→ false。
func qmapModeOn(dev string) bool {
	dev = strings.TrimSpace(dev)
	if dev == "" {
		return false
	}
	b, err := os.ReadFile(filepath.Join("/sys/class/net", dev, "qmap_mode"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(b)) == "1"
}

// ensureHostDialNetdevUp 数据网卡被拉到管理 down 时把它拉起来（幂等）。
//
// 使用场景：QMI 掉线时 netifd 会把接口连同网卡一起 down 掉，而网卡一 down
// 驱动就不上报 link_state ⇒ 载波永远回不来（实测 2026-10-09 FM160-CN：
// `ip link set wwan0 up` 之后 3 秒 carrier=1，几十秒内拿到 DHCP 租约）。
func ensureHostDialNetdevUp(dev string) (bool, error) {
	dev = strings.TrimSpace(dev)
	if dev == "" || netdevUp(dev) {
		return false, nil
	}
	return true, runNetProvCmd([]string{"ip", "link", "set", dev, "up"})
}

// flushHostDialNetdev 拨号器停了以后，把它留在网卡上的地址与默认路由清掉。
//
// 为什么必须清：拨号器（quectel-CM / simcom-cm）里的 udhcpc 会把地址与默认路由写进内核，
// 进程被 SIGKILL、或者模组直接被拔掉时它来不及做自己的 flush —— 那张网卡上就留着
// "10.x/29 + 一条默认路由（我们还会把它标成 metric 20）"，之后流量会一直往一个
// 没人管的接口上走。实测踩过：手工"断开"后 wwan0 上还有默认路由。
//
// 只动这一张网卡（我们自己的数据网卡），别的接口不碰。
func flushHostDialNetdev(dev string) {
	dev = strings.TrimSpace(dev)
	if dev == "" {
		return
	}
	routes, _ := defaultRoutesOn(dev)
	for range routes {
		if err := runNetProvCmd([]string{"ip", "-4", "route", "del", "default", "dev", dev}); err != nil {
			break
		}
	}
	if err := runNetProvCmd([]string{"ip", "-4", "addr", "flush", "dev", dev}); err != nil {
		// 网卡已经不在了（模组被拔）算正常
		if _, statErr := net.InterfaceByName(dev); statErr != nil {
			return
		}
		logger.Warn("host dial: flush netdev failed", zap.String("dev", dev), zap.Error(err))
		return
	}
	if len(routes) > 0 {
		logger.Info("host dial: netdev cleaned", zap.String("dev", dev),
			zap.Int("routes_removed", len(routes)))
	}
}

// raw_ip 切换要重试的原因：网卡刚被模组重新枚举出来（拔插、模组自己复位）的头几秒，
// 内核 qmi_wwan 的 raw_ip 属性还会返回 EBUSY（netif_running 还没落下去），
// 真机踩过——写一次失败就不再试，网卡就永远是 N，拨号器空转到天荒地老。
const (
	rawIPAttempts = 5
	rawIPRetryGap = 800 * time.Millisecond
)

// prepareQMIWwanRawIP 拨号前先把数据网卡切到 **raw IP** 模式。
//
// 为什么：走 QMI 的这套拨号器（quectel-CM / simcom-cm / fibocom-dial / meig-cm）
// 承载都是裸 IP，各自源码里也只写**驱动私有**的那条属性
// （`/sys/class/net/<if>/qmi/rawip`，见各自 qmap_bridge_mode.c 的
// xxx_enable_qmi_wwan_rawip_mode），那是高通/厂商内核（GobiNet、qmi_wwan_q 之类）才有的；
// 主线 qmi_wwan 暴露的是 `.../qmi/raw_ip`，所以这一步只能由我们来做。
//
// SIMCom 自己的固件也是这么做的（他们的热插拔脚本）：
//
//	insmod qmi_wwan_simcom.ko
//	echo Y > /sys/class/net/wwan0/qmi/raw_ip
//	echo 80 > /sys/class/net/wwan0/qmi/add_mux
//
// 实测这台 SIM8200EA-M2（跑的是通用 qmi_wwan，不是 SIMCom 那个 kmod）：
// raw_ip=Y 时 simcom-cm 拨号成功、udhcpc 拿到地址；raw_ip=N（802.3 模式）拨不上。
// 模组侧给的是 raw IP 承载，以太网头那套对不上，所以必须 Y。
//
// 幂等且不折腾：读 sysfs 发现已经是 Y 就什么都不做；没有这个属性（老内核 / 别的驱动）也不插手。
// raw_ip 只能在网卡 down 时改，所以只在真需要改时才 down → 写 → up，并且每次尝试后都把网卡恢复起来
// （哪怕这次没写成功）——网卡被留在 down 状态比切换失败更糟。
// 返回错误 = 这次没切成（调用方本轮不要拉起拨号器）。
func prepareQMIWwanRawIP(dev string) error {
	dev = strings.TrimSpace(dev)
	if dev == "" {
		return nil
	}
	rawIP := filepath.Join("/sys/class/net", dev, "qmi", "raw_ip")
	if _, err := os.Stat(rawIP); err != nil {
		return nil // 没有这个属性：不插手（可能是不带 mux/raw_ip 扩展的驱动）
	}
	var lastErr error
	for i := 0; i < rawIPAttempts; i++ {
		if i > 0 {
			time.Sleep(rawIPRetryGap)
		}
		b, err := os.ReadFile(rawIP)
		if err != nil {
			lastErr = err
			continue
		}
		if strings.EqualFold(strings.TrimSpace(string(b)), "Y") {
			return nil
		}
		downErr := runNetProvCmd([]string{"ip", "link", "set", dev, "down"})
		writeErr := os.WriteFile(rawIP, []byte("Y\n"), 0o644)
		_ = runNetProvCmd([]string{"ip", "link", "set", dev, "up"})
		if writeErr == nil {
			logger.Info("host dial: raw_ip switched to Y (QMI bearer is raw IP)",
				zap.String("dev", dev), zap.Int("attempt", i+1))
			return nil
		}
		lastErr = writeErr
		if downErr != nil {
			lastErr = fmt.Errorf("%v（先把网卡 down 也没成功：%v）", writeErr, downErr)
		}
		logger.Warn("host dial: set raw_ip failed, will retry", zap.String("dev", dev),
			zap.Int("attempt", i+1), zap.Int("of", rawIPAttempts), zap.Error(lastErr))
	}
	return lastErr
}

// ---- 拨号器起来了、接口却没拿到地址 ⇒ 踢一次 ifup（QMI 的经典坑） ----
//
// 用户 2026-10-08 报："qmi 拨号后接口那边不能获取到 IP，要重启网络接口才能获取到"。
//
// 原因是一个**时序**问题，不是配置错：QMI 上先由拨号器（quectel-CM / fibocom-dial 等）建 PDN、
// 拉起 `wwan0`，而 netifd 的 dhcp 客户端是在**接口第一次 ifup 时**就已经跑过一轮的 —— 那会儿链路还没起，
// 一次 DISCOVER 没人应就不再重试 ⇒ 界面上一直"没有地址"，**手工重启一次接口**才拿到。
// 所以每轮对账时看一眼：链路在 up、拨号器在跑，但网卡上就是没有 IPv4 ⇒ 替用户跑一次 `ifup modem<N>`
// （他手工做的那件事），并且限次限速，免得链路真坏了在那儿打转。
const (
	// 2026-10-09：宽限期从 20 s 改成 90 s —— 掉线自愈现在以**重开拨号器**为主
	// （hostDialRestart*：跑满 60 s 没地址就先停掉、下一轮重拉），
	// ifup 这一脚留作“重开也没用”之后的最后手段，不要先去捅 netifd。
	hostDialKickGrace = 90 * time.Second // 刚拨上先等一会儿（拨号器自己的 udhcpc 可能马上就装地址）
	hostDialKickGap   = 30 * time.Second // 两次 ifup 之间至少隔这么久
	hostDialKickMax   = 6                // 最多踢几次（之后只在日志里提醒，不再打转）
)

// hostDialKickState 一台模组的"踢 ifup"计数（since = 从什么时候开始看不见地址）
type hostDialKickState struct {
	since time.Time
	last  time.Time
	kicks int
}

// hostDialKickDue 纯决策：现在该不该替用户踢一次 `ifup`。
//
//	hasAddr  网卡上已经有 IPv4 地址（有就别碰，一个字节都不动）
//	devUp    网卡存在且在 up（还没起来就等下一轮）
//	st       这台模组的历史（见 hostDialKickState）
func hostDialKickDue(hasAddr, devUp bool, st hostDialKickState, now time.Time) bool {
	if hasAddr || !devUp {
		return false
	}
	if !st.since.IsZero() && now.Sub(st.since) < hostDialKickGrace {
		return false
	}
	if st.kicks >= hostDialKickMax {
		return false
	}
	if !st.last.IsZero() && now.Sub(st.last) < hostDialKickGap {
		return false
	}
	return true
}

var (
	hostDialKickMu sync.Mutex
	hostDialKicks  = map[int]hostDialKickState{}
)

// resetHostDialKick 拿到地址（或模组没了）⇒ 计数清零
func resetHostDialKick(id int) {
	hostDialKickMu.Lock()
	delete(hostDialKicks, id)
	hostDialKickMu.Unlock()
}

// hostDialUciProto 读一张 netifd 接口的 proto（不存在返回空串）。
// 用来判断“这张卡现在归不归 netifd 管”：proto=none 时 netifd 不跑任何协议，
// 这时去 `ifup` 只会把网卡重配一遍，反而把拨号器的数据链路抖掉。
func hostDialUciProto(iface string) string {
	if strings.TrimSpace(iface) == "" {
		return ""
	}
	p, err := uciGet("network." + iface + ".proto")
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(p))
}

// kickHostDialIfNoAddr 每轮对账：地址一直不来就替用户 `ifup modem<N>`（幂等、限次）。
//
// ⚠️ 2026-10-09：**保留但不再调用**（原来由 reconcileHostDialRoute 每轮调）。
// 原因：`ifup` 会让 netifd 把接口（连同网卡）down→up 走一遍，而 QMI 的数据呼叫
// **只要网卡 down 就断**（实测 dmesg `net wwan0: link_state 1 -> 0`、拨号器立刻
// `usbnet_link_change :link:0`、call_end_reason 2509）—— 于是"接口没地址"这件事
// 被越修越坏，形成载波抖动（carrier_changes 一路涨）。
// 用户 2026-10-09 定的口径是 **掉线就重开拨号器**（见 hostdial.go 的 hostDialRestart*），
// 那条路不动 netifd、不抖网卡，就不需要这一脚了。留着是因为它的判据（限次限速的
// 纯决策函数 hostDialKickDue）还有参考价值。
func kickHostDialIfNoAddr(id int, dev string) {
	iface := WANInterfaceName(id)
	dev = strings.TrimSpace(dev)
	if dev == "" || iface == "" {
		return
	}
	if netdevAddrs(dev) != "" {
		resetHostDialKick(id) // 地址有了 ⇒ 以后重新算
		return
	}
	// 这张接口的 proto 已经不是 dhcp（QMI 被自动降级成 none 之后就是这种状态）⇒
	// netifd 在这张卡上不跑任何协议，`ifup` 只会让它把网卡 down/up 重配一遍，
	// 而 QMI 的数据链路是拨号器的，被这么抖一下就得重新拨
	// （2026-10-09 实测：wwan0 变成 NO-CARRIER、地址/路由全没）。
	// 地址本来也不归 netifd 管 ⇒ 不踢，交给拨号器。
	if proto := hostDialUciProto(iface); proto != "" && proto != "dhcp" {
		return
	}
	now := time.Now()
	hostDialKickMu.Lock()
	st := hostDialKicks[id]
	if st.since.IsZero() {
		st.since = now
	}
	due := hostDialKickDue(false, netdevUp(dev), st, now)
	if due {
		st.last, st.kicks = now, st.kicks+1
	}
	hostDialKicks[id] = st
	kicks := st.kicks
	hostDialKickMu.Unlock()
	if !due {
		return
	}
	if err := runNetProvCmd([]string{"ifup", iface}); err != nil {
		logger.Warn("host dial: ifup kick failed",
			zap.Int("modem", id), zap.String("iface", iface), zap.String("dev", dev), zap.Error(err))
		return
	}
	logger.Info("host dial: interface had no address, kicked ifup to pull a lease（QMI 时序坑，用户不用再手动重启接口）",
		zap.Int("modem", id), zap.String("iface", iface), zap.String("dev", dev), zap.Int("try", kicks))
}

// hostDialDefaultRoute 一条默认路由（只取收尾要用的两个字段）
type hostDialDefaultRoute struct {
	gw     string
	metric int
}

// reconcileHostDialRoute 把拨号器装上来的默认路由的 metric 对齐配置值。
//
// 为什么必须管：[net] wan_metric 的承诺是"路由器本来就有 WAN 时不会被模组抢走默认路由"。
// 以前协议脚本（quectel.sh / qmi.sh / dhcp）会把 metric 交给 netifd，netifd 装路由时带上它；
// 现在 QMI 的协议是 none，netifd 不碰这张网卡 —— 路由由 udhcpc 的默认脚本装，metric 0，
// 跟路由器自己那条（通常也是 0）平起平坐，谁赢不好说。所以按原来的语义补这一步。
//
// 只做一件事：wwanN 上有默认路由、且 metric 不是期望值 → 删掉、按期望 metric 加回来。
// 想让 quectel-CM 自己那套原样生效（完全不干预）就把 wan_metric 设成 0 或负数。
// hostDialRouteFix 主机侧拨号把默认路由装上来之后的"收尾"（对齐 metric）。
//
// ⚠️ 2026-10-09 起**不再顺手踢 ifup**（`kickHostDialIfNoAddr` 保留但不再调用，见其注释）：
// 实测 netifd 被 `ifup` 一叫就会把接口/网卡 down→up 走一遍，而 QMI 的数据呼叫
// 只要网卡 down 就断（dmesg `net wwan0: link_state 1 -> 0`，拨号器随即报 link:0）。
// 现在的自愈口径是**重开拨号器**（见 hostdial.go 的 hostDialRestart*）。
func reconcileHostDialRoute(id int, dev string) {
	if strings.TrimSpace(dev) == "" || config.Cfg.Net.WANMetric <= 0 {
		return
	}
	if netdevAddrs(dev) == "" {
		return // 地址还没下来（刚拉起那几秒很正常），下一轮再来
	}
	routes, err := defaultRoutesOn(dev)
	if err != nil || len(routes) == 0 {
		return
	}
	gw, needFix := "", false
	for _, r := range routes {
		if r.gw != "" {
			gw = r.gw
		}
		if r.metric != config.Cfg.Net.WANMetric {
			needFix = true
		}
	}
	if !needFix {
		return
	}
	// 先把这个网卡上的默认路由删干净（来源/优先级不同的话可能不止一条）
	for range routes {
		if err := runNetProvCmd([]string{"ip", "-4", "route", "del", "default", "dev", dev}); err != nil {
			break
		}
	}
	argv := []string{"ip", "-4", "route", "add", "default"}
	if gw != "" {
		argv = append(argv, "via", gw)
	}
	argv = append(argv, "dev", dev, "metric", strconv.Itoa(config.Cfg.Net.WANMetric))
	if err := runNetProvCmd(argv); err != nil {
		logger.Warn("host dial: default route metric fix failed", zap.Int("modem", id),
			zap.String("dev", dev), zap.String("cmd", netprov.WANCommandText(argv)), zap.Error(err))
		return
	}
	logger.Info("host dial: default route metric aligned", zap.Int("modem", id),
		zap.String("dev", dev), zap.String("gw", gw), zap.Int("metric", config.Cfg.Net.WANMetric))
}

// defaultRoutesOn 读 `ip -4 route show default dev <dev>`（busybox 的 ip 也认这个写法）。
//
// 输出形如：`default via 10.1.1.1 dev wwan0` / `default dev wwan0 scope link metric 20`
func defaultRoutesOn(dev string) ([]hostDialDefaultRoute, error) {
	ctx, cancel := context.WithTimeout(context.Background(), netProvTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ip", "-4", "route", "show", "default", "dev", dev).Output()
	if err != nil {
		return nil, err
	}
	var routes []hostDialDefaultRoute
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 || fields[0] != "default" {
			continue
		}
		r := hostDialDefaultRoute{}
		for i := 1; i < len(fields); i++ {
			switch fields[i] {
			case "via":
				if i+1 < len(fields) {
					r.gw = fields[i+1]
				}
			case "metric":
				if i+1 < len(fields) {
					r.metric, _ = strconv.Atoi(fields[i+1])
				}
			}
		}
		routes = append(routes, r)
	}
	return routes, nil
}
