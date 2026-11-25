//go:build !windows

package kernel

// ====================== 自动组网（执行侧：uci / ifup + 周期循环） ======================
//
// 纯逻辑（网卡匹配 / UCI 解析 / 命令生成）在 netprov.go，这里只做两件事：
//
//	① 周期检查每台模组：先保证"模组侧拨号好了"（可关），再保证"主机侧有 WAN 接口"；
//	② 真正执行 uci / ifup（唯一会改主机网络配置的地方）。
//
// ⚠️ 只在 Linux + OpenWrt 上启用（AutoWANEnabled），Windows 上是空操作（netprov_windows.go）。

import (
	"fmt"
	"modemd/module/config"
	"modemd/module/dial"
	"modemd/module/hostnic"
	"runtime"
	"time"

	"go.uber.org/zap"
)

// netProvTimeout 单条 uci / ifup 命令的超时
const netProvTimeout = 15 * time.Second

// 可注入路径：单测里换成临时文件（生产永远是系统路径）
// （netClassRoot 已经挪到跨平台的 sysfs_read.go —— “主机侧网卡→USB 类型”那条路也要用它）
var (
	firewallCfgPath = "/etc/config/firewall"
	networkCfgPath  = "/etc/config/network"
	// QMI / MBIM 的控制口（/dev/cdc-wdm0）在 /sys/class/usbmisc 下
	usbmiscRoot = "/sys/class/usbmisc"
	// netifd 的协议脚本：qmi → qmi.sh（包 uqmi）、mbim → mbim.sh（包 umbim）
	protoScriptRoot = "/lib/netifd/proto"
)

// AutoWANEnabled 要不要跑自动组网：配置开着 + Linux + OpenWrt。
// （桌面 Linux 上不碰：那边的网卡管理是 NetworkManager / systemd-networkd，写 UCI 没意义）
func AutoWANEnabled() bool {
	return config.Cfg.Net.AutoWAN && runtime.GOOS == "linux" && hostnic.IsOpenWrt()
}

// AutoWANLoop 周期检查（由 main 起一个 goroutine）。
func AutoWANLoop(mgr *ModemManager, every time.Duration) {
	if every <= 0 {
		every = 15 * time.Second
	}
	logger.Info("auto wan provisioning enabled",
		zap.Duration("every", every), zap.Bool("auto_dial", config.Cfg.Net.AutoDial),
		zap.Int("metric", config.Cfg.Net.WANMetric), zap.String("zone", config.Cfg.Net.WANZone),
		zap.Bool("host_dial", config.Cfg.Net.HostDial), zap.Bool("wan_ipv6", config.Cfg.Net.WANIPv6))
	// 系统启动时**先把自定义防火墙那段清空**（用户 2026-10-07 要求：开机清空、拨号后才写）。
	// 不等第一个 tick：开机这一刻必然没有"在用的模组网卡"（模组还没拨上），所以这里就是清空；
	// 万一 modemd 只是重启、链路还在，它会把当前该有的清单原样对回来（不会误删）。
	syncWAN6FirewallDevices(mgr)
	syncLANPrefixRoute(mgr)
	syncLANNDPProxy(mgr)
	// ⑧ 内核里那条把 /64 钉在 LAN 的路由真的落下去没有（netifd 的 route6 实测不可靠，见 netprov_v6up.go）
	syncLAN6PinLanded(mgr)
	// ⑨ 该起来的 v6 兄弟接口没起来就踢一下 ifup
	syncWAN6Up(mgr)
	// 2026-10-08 用户要求（和老版本一致）：不再调用 syncHostIPv6Prefix —— 老版本没有这个看门狗；
	// 真机上它表现为每 2~3 分钟换一次前缀（日志 host ipv6 stuck on the old prefix, bouncing the pdp）
	// + pdp bounce failed，把 v6 搞得时通时不通。函数与单测保留，只是不再调用。
	t := time.NewTicker(every)
	defer t.Stop()
	for range t.C {
		autoWANOnce(mgr)
	}
}

// autoDialTry 自动拨号：按候选顺序试（最多 3 个 cid），成功就记住它。
//
// ⚠️ 为什么要"试"：模组对 cid 挑食（鼎桥 MT5700M-CN 上 AT+CGACT=1,1 会被拒，cid 3 才通），
// 而且它把不该激活的 cid 也报成 active —— 只能真发一条才知道行不行。
func autoDialTry(mod *Modem) (*dial.DialState, error) {
	var ctxs []Context
	if st, err := mod.QueryParams(); err == nil && st != nil {
		ctxs = st.Contexts
	}
	cands := AutoDialCandidates(ctxs, preferredDialCID(mod.ID))
	var lastErr error
	for i, cid := range cands {
		if i >= 3 {
			break // 一轮最多试 3 个，别把循环拖太久
		}
		apn := APNForCID(ctxs, cid)
		if !ValidAPN(apn) {
			apn = "" // 模组上报的 APN 不合法就别往配置里写
		}
		logger.Info("auto dial", zap.Int("modem", mod.ID), zap.Int("cid", cid),
			zap.String("apn", apn), zap.Int("try", i+1), zap.Int("candidates", len(cands)))
		st, err := mod.Dial(dial.DialRequest{CID: cid, APN: apn})
		if err == nil {
			rememberGoodDialCID(mod.ID, cid)
			return st, nil
		}
		lastErr = err
		logger.Debug("auto dial candidate failed",
			zap.Int("modem", mod.ID), zap.Int("cid", cid), zap.Error(err))
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没找到可用的 PDP 上下文")
	}
	return mod.DialStatus(), lastErr
}

// autoWANOnce 把所有模组过一遍（串行：uci 都是毫秒级，拨号有 dialBusy 兜底）
func autoWANOnce(mgr *ModemManager) {
	// ① 先对一次账：自定义防火墙那段只反映"现在真的在用"的模组网卡——
	// **开机第一轮这里就是空的 ⇒ 系统启动时自动清空**（用户 2026-10-07 要求）；
	// 之后拨号成功（链路上拿到全局 v6 地址）才会写进去，断网/拔掉自动清掉自己那条。
	syncWAN6FirewallDevices(mgr)
	// ② 再对一次账：保证"内网那个 /64"在 LAN 上有条低 metric 路由 ——
	// 否则回给内网设备的包会被送去模组（"IPv6 地址都在、就是访问不了"，见 syncLANPrefixRoute）。
	syncLANPrefixRoute(mgr)
	// ③ 还有 NDP 代理那一环：上游那个 /64 没有 PD，公网回包得我们替内网设备应答（见 syncLANNDPProxy）
	syncLANNDPProxy(mgr)
	// ⑧ 内核里那条把 /64 钉在 LAN 的路由真的落下去没有（netifd 的 route6 实测不可靠，见 netprov_v6up.go）
	syncLAN6PinLanded(mgr)
	// ⑨ 该起来的 v6 兄弟接口没起来就踢一下 ifup
	syncWAN6Up(mgr)
	// ④ 还有主机侧有没有跟上模组（重拨换 /64 后模组不主动重发 RA ⇒ 主机卡在旧前缀，见 syncHostIPv6Prefix）
	// 2026-10-08 用户要求（和老版本一致）：不再调用 syncHostIPv6Prefix —— 老版本没有这个看门狗；
	// 真机上它表现为每 2~3 分钟换一次前缀（日志 host ipv6 stuck on the old prefix, bouncing the pdp）
	// + pdp bounce failed，把 v6 搞得时通时不通。函数与单测保留，只是不再调用。
	// ⑤ 再清掉"模组已经不在了、接口还留着"的（插拔即动态增删，用户要的就是这个）
	sweepStaleAutoWAN(mgr)
	for _, mod := range mgr.listModems() {
		autoWANForModem(mod)
	}
	// ⑥ 最后一步（必须在建完接口之后）：**把这些接口划进防火墙区**。
	// 不划进区 ⇒ 转发落到 FORWARD 最后那条 reject、也没有 MASQUERADE ⇒ 局域网完全上不了网。
	// ⚠️ 这一步 2026-10-07 被我误删过（详见 netprov.go 那段注释），2026-10-08 恢复。
	syncFirewallZone(mgr)
	// ⑦ 还有一个“假上网”：地址/路由都在，但网卡发送队列卡死（内核 NETDEV WATCHDOG）——
}
