//go:build !windows

package kernel

// ====================== 两处"重启/重拨后自己恢复"的对账 ======================
//
// ① v6 兄弟接口（`modem<id>_6`）卡在 pending / 没起来 → 自动 `ifup` 一下
// ② `route6 modemd_lan6route` 写进 UCI 了、**内核里却没生效** → 直接 `ip -6 route replace` 补上
//
// 为什么需要（2026-10-08 真机，重启后"IPv6 不能访问"）：
//
//   - `modem0_6`（dhcpv6）会停在 `up:false / pending:true`：这时模组的 /64 交不到内网，
//     内网设备手里只有旧前缀的地址（旧前缀回程没问题，所以看起来"有时候能用"）；
//     手工 `ifup modem0_6` 立刻就好 —— 那就别让人手工做。
//   - `uci commit` + netifd 自己的处理**不保证**把那条 metric 100 的路由写进内核。
//     实测（0.2.281-1，2026-10-08）：`network.modemd_lan6route.target/metric` 完全正确、
//     连做 3 次 `ubus call network reload`，内核里仍然没有这条路由 ⇒
//
//         240e:…::/64 dev wwan0  metric 256    ← netifd 给上联装的
//         240e:…::/64 dev br-lan metric 1024   ← 交给内网那份
//
//     于是 `ip -6 route get <内网设备>` 走 wwan0（应该走 br-lan）：回给内网设备的包
//     被送去模组，症状 = ping 通但延迟几百~几千毫秒、TCP 直接失败。
//     而 `ip -6 route replace <prefix> dev br-lan metric 100` 一敲就生效。
//
// ⚠️ 与"老版本"的关系：老版本（10/06 版）**没有**这两步，但这台真机上的实测是
// "没有它内网 IPv6 就不通"（见 `MD/README.md`）。所以留着，且做成**每轮对账**
// （没事时一个字节都不动）。

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	// wan6UpKickMax 同一台模组最多连续踢几次 `ifup <v6 兄弟接口>`（接口起来后计数清零）
	wan6UpKickMax = 8
	// lan6PinWait 补完路由等内核生效的观测间隔
	lan6PinWait = 1500 * time.Millisecond
)

var (
	wan6UpMu    sync.Mutex
	wan6UpKicks = map[int]int{} // modem id → 连续踢了几次

	// lan6PinMu/lan6PinReloaded 同一前缀只 reload "一轮"（重拨换前缀才再给一轮）：
	// 真问题主要靠 `ip -6 route replace` 直接补（幂等、不动网络），reload 只是兜底。
	lan6PinMu       sync.Mutex
	lan6PinReloaded string
)

// lanDeviceName 把 UCI 接口名（lan）换成真正的内核设备名（br-lan）。
//
// 路由表里写的是设备名，拿接口名去查永远是"没落进内核"的假警报。
func lanDeviceName(iface string) string {
	iface = strings.TrimSpace(iface)
	if iface != "" {
		if d, _ := uciGet("network." + iface + ".device"); strings.TrimSpace(d) != "" {
			return strings.TrimSpace(d)
		}
		if d, _ := uciGet("network." + iface + ".ifname"); strings.TrimSpace(d) != "" {
			return strings.TrimSpace(d)
		}
	}
	return "br-lan" // 老版本 UCI 没写 device 时的默认桥名
}

// wan6IfaceUp 这张 v6 兄弟接口在 netifd 里是不是已经 up。
//
// 读不到（ubus 不认识它）时：**只有 UCI 里确实配了这个接口**才算"该起来却没起来"，
// 免得对一个不存在的接口发 ifup（那只会白报错）。
func wan6IfaceUp(iface string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), netProvTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ubus", "call", "network.interface."+iface, "status").Output()
	if err != nil {
		proto, _ := uciGet("network." + iface + ".proto")
		return strings.TrimSpace(proto) == ""
	}
	return strings.Contains(string(out), `"up": true`)
}

// syncWAN6Up 每轮对账：**该有 v6 的那张兄弟接口没起来就 ifup 一下**。
//
// 判据（缺一不可，避免误动别人的接口）：
//   - 接口名是我们那套 `modem<id>_6`，而且 UCI 里它绑了网卡；
//   - 那台模组正在跑；
//   - 它的网卡上**已经有全局 IPv6 地址**（说明模组确实把 v6 起来了 ⇒ 这接口本该 up）；
//   - netifd 那边它却没 up。
func syncWAN6Up(mgr *ModemManager) {
	if mgr == nil {
		return
	}
	list, ok := managedInterfacesNowOK()
	if !ok {
		return
	}
	live := map[int]bool{}
	for _, mod := range mgr.listModems() {
		if mod != nil && mod.isRunningNow() {
			live[mod.ID] = true
		}
	}
	for _, it := range list {
		if !strings.HasSuffix(it.Name, "_6") || !live[it.ModemID] {
			continue
		}
		dev := strings.TrimSpace(it.Device)
		if dev == "" || !netdevHasGlobalIPv6(dev) {
			continue // 模组自己还没拿到 v6（拨号中/不支持）⇒ 这时候踢接口没意义
		}
		if wan6IfaceUp(it.Name) {
			wan6UpMu.Lock()
			delete(wan6UpKicks, it.ModemID) // 起来了 ⇒ 计数清零
			wan6UpMu.Unlock()
			continue
		}
		wan6UpMu.Lock()
		n := wan6UpKicks[it.ModemID]
		if n >= wan6UpKickMax {
			wan6UpMu.Unlock()
			continue // 踢太多次了，别再打转（之后起来了会清零）
		}
		wan6UpKicks[it.ModemID] = n + 1
		wan6UpMu.Unlock()
		if err := runNetProvCmd([]string{"ifup", it.Name}); err != nil {
			logger.Warn("auto wan6: v6 iface ifup kick failed",
				zap.Int("modem", it.ModemID), zap.String("iface", it.Name),
				zap.String("dev", dev), zap.Error(err))
			continue
		}
		logger.Info("auto wan6: v6 兄弟接口没起来，已 ifup（重启/重拨后自己恢复，不用手工）",
			zap.Int("modem", it.ModemID), zap.String("iface", it.Name),
			zap.String("dev", dev), zap.Int("try", n+1))
	}
}

// lan6PinLanded 内核里这条"把 /64 钉在 LAN"的路由到底生效没有。
//
// 判据：路由表里有**这个前缀**、走 **LAN 网卡**、metric = 我们写进去的那个值。
func lan6PinLanded(prefix, lanDev string) bool {
	prefix, lanDev = strings.TrimSpace(prefix), strings.TrimSpace(lanDev)
	if prefix == "" || lanDev == "" {
		return true // 没什么可查的，别报假警
	}
	ctx, cancel := context.WithTimeout(context.Background(), netProvTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ip", "-6", "route", "show").Output()
	if err != nil {
		return true // 查不到就当"没问题"：宁可不折腾网络，也不要因为读不到就乱 reload
	}
	metric := "metric " + strconv.Itoa(LAN6RouteMetric)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, prefix) || !strings.Contains(line, "dev "+lanDev) {
			continue
		}
		// 同一前缀在 wwan0 和 br-lan 上都有 ⇒ 必须看 metric 才能分清是不是我们钉的那条
		if strings.Contains(line, metric) {
			return true
		}
	}
	return false
}

// syncLAN6PinLanded 每轮对账：UCI 里那条 route6 已经写对（target = 现在的 /64），
// 但内核里没落到 LAN ⇒ **直接 `ip -6 route replace` 补进去**（幂等、不动别的接口）。
//
// 为什么不能只信 netifd 的 route6：见本文件头部注释（真机实测 reload 也不落）。
// UCI 那份留着做持久化 / LuCI 可见，内核这份由我们对账补；netifd 以后把它冲掉也没关系，
// 下一轮对账会再补上。
func syncLAN6PinLanded(mgr *ModemManager) {
	want, _, ok := lan6WantNow(mgr)
	if !ok || want == "" {
		return
	}
	iface := lanInterfaceName()
	if iface == "" {
		return
	}
	lanDev := lanDeviceName(iface)
	cur, _ := uciGet("network." + LAN6RouteSection + ".target")
	if strings.TrimSpace(cur) != want {
		return // 还没写对（syncLANPrefixRoute 会写），这里不插手
	}
	if lan6PinLanded(want, lanDev) {
		lan6PinMu.Lock()
		lan6PinReloaded = "" // 已生效 ⇒ 下次真被冲掉还能再救
		lan6PinMu.Unlock()
		return
	}
	metric := strconv.Itoa(LAN6RouteMetric)
	if err := runNetProvCmd([]string{"ip", "-6", "route", "replace", want, "dev", lanDev, "metric", metric}); err != nil {
		logger.Warn("auto wan6: 补钉 /64 的路由失败",
			zap.String("prefix", want), zap.String("dev", lanDev), zap.Error(err))
	} else {
		time.Sleep(lan6PinWait)
		if lan6PinLanded(want, lanDev) {
			logger.Info("auto wan6: 钉 /64 的路由 netifd 没落进内核，已用 ip route replace 补上（内网回程才不会跑到模组去）",
				zap.String("prefix", want), zap.String("iface", iface), zap.String("dev", lanDev), zap.String("metric", metric))
			lan6PinMu.Lock()
			lan6PinReloaded = ""
			lan6PinMu.Unlock()
			return
		}
	}
	// 兜底：再 reload 一次并复查（同一前缀只做一次，不把网络一直刷）
	lan6PinMu.Lock()
	if lan6PinReloaded == want {
		lan6PinMu.Unlock()
		return // 这个前缀已经 reload 过了，下一轮再看
	}
	lan6PinReloaded = want
	lan6PinMu.Unlock()
	logger.Warn("auto wan6: 钉 /64 的路由没落进内核，再 reload 一次兜底",
		zap.String("prefix", want), zap.String("iface", iface), zap.String("dev", lanDev))
	if err := runNetProvCmd(networkReloadCmd()); err != nil {
		logger.Warn("auto wan6: network reload failed", zap.Error(err))
		return
	}
	time.Sleep(lan6PinWait)
	if err := runNetProvCmd([]string{"ip", "-6", "route", "replace", want, "dev", lanDev, "metric", metric}); err == nil {
		time.Sleep(lan6PinWait)
	}
	if lan6PinLanded(want, lanDev) {
		logger.Info("auto wan6: 钉 /64 的路由已生效（reload + replace 后复查通过）",
			zap.String("prefix", want), zap.String("iface", iface), zap.String("dev", lanDev))
		return
	}
	logger.Warn("auto wan6: 钉 /64 的路由反复没能落进内核 —— 请到 LuCI 看一眼「网络 → 接口」与路由表",
		zap.String("prefix", want), zap.String("iface", iface), zap.String("dev", lanDev))
}
