//go:build !windows

package kernel

import (
	"context"
	"modemd/module/config"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// netdevTXStuckCount 内核日志里这张网卡的 `NETDEV WATCHDOG … transmit queue … timed out` 条数。
// 读不到（没 dmesg / 没权限）返回 -1 ⇒ 调用方什么也不做。
func netdevTXStuckCount(dev string) int {
	dev = strings.TrimSpace(dev)
	if dev == "" {
		return -1
	}
	ctx, cancel := context.WithTimeout(context.Background(), netProvTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "dmesg").Output()
	if err != nil {
		return -1
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "NETDEV WATCHDOG") && strings.Contains(line, dev) {
			n++
		}
	}
	return n
}

// txWedgeState 一台模组的卡死计数与自愈历史
type txWedgeState struct {
	seen  int         // 上一轮看到的 NETDEV WATCHDOG 条数
	last  time.Time   // 上次自动复位的时间
	tries []time.Time // 最近一小时内的自动复位时间
}

var (
	txWedgeMu   sync.Mutex
	txWedgeByID = map[int]*txWedgeState{}
)

// syncTXWedged 每轮对账：网卡发送队列卡死（驱动/固件把 TX 队列堵死，包发不出去）就自动复位模组。
//
// 为什么值得自动做：这个故障的**现象是"没网"**，而模组侧、接口地址、默认路由、防火墙全都正常，
// 用户自己根本判断不出来，只能重启设备 —— 而实际上 `AT+CFUN=1,1` 一下就好（真机验证）。
func syncTXWedged(mgr *ModemManager) {
	if mgr == nil {
		return
	}
	list, ok := managedInterfacesNowOK()
	if !ok {
		return
	}
	devByID := map[int]string{}
	for _, it := range list {
		if strings.HasSuffix(it.Name, "_6") {
			continue
		}
		if d := strings.TrimSpace(it.Device); d != "" {
			devByID[it.ModemID] = d
		}
	}
	for _, mod := range mgr.listModems() {
		if mod == nil || !mod.isRunningNow() {
			continue
		}
		dev := devByID[mod.ID]
		if dev == "" {
			continue
		}
		n := netdevTXStuckCount(dev)
		if n < 0 {
			continue
		}
		now := time.Now()
		txWedgeMu.Lock()
		st := txWedgeByID[mod.ID]
		if st == nil {
			txWedgeByID[mod.ID] = &txWedgeState{seen: n} // 第一轮只记基线，不把历史旧账当新故障
			txWedgeMu.Unlock()
			continue
		}
		prev := st.seen
		// 只留一小时内的记录（限流用）
		fresh := st.tries[:0]
		for _, t := range st.tries {
			if now.Sub(t) < time.Hour {
				fresh = append(fresh, t)
			}
		}
		st.tries = fresh
		act, why := TXWedgeDecision(prev, n, st.last, st.tries, now)
		if n > prev {
			st.seen = n
		}
		if act {
			st.last = now
			st.tries = append(st.tries, now)
		}
		tries := len(st.tries)
		txWedgeMu.Unlock()
		if why != "" {
			logger.Warn("auto wan: "+why, zap.Int("modem", mod.ID), zap.String("dev", dev),
				zap.Int("watchdog", n), zap.Int("resets_last_hour", tries))
		}
		if !act {
			continue
		}
		logger.Warn("auto wan: 网卡发送队列卡死，自动复位模组（AT+CFUN=1,1）—— 不用再手动重启设备",
			zap.Int("modem", mod.ID), zap.String("dev", dev), zap.Int("watchdog", n))
		if err := mod.ResetModule(); err != nil {
			logger.Warn("auto wan: 自动复位模组失败", zap.Int("modem", mod.ID), zap.Error(err))
		}
	}
}

// syncFirewallZone 每轮对账：把我们建的模组接口（`modem<id>` / `modem<id>_6`，且那台模组还在跑）
// 划进配置的防火墙区（`[net] wan_zone`，默认 `wan`）。
//
// 为什么必须有：接口不在任何区里 ⇒ “fw3` 不会给它生成转发跳与 MASQUERADE ⇒ 路由器自己能上网，
// **局域网完全上不了网**（真机：`iptables -S | grep -c wwan0` = 0、FORWARD 最后那条 reject 计数 3069）。
// RM500U 是 ECM（eth1，用户配置里本来就在 wan 区）所以看不出问题，换成 QMI/MBIM 的模组就全死 ——
// 这就是用户 2026-10-08 报的"换别的模组不能正常联网"。
//
// ⚠️ 只碰自己加的那几张：加进去的记在 `modemd_networks` 标记上，只从标记里删（见 ZoneSyncPlan）。
func syncFirewallZone(mgr *ModemManager) {
	zone := strings.TrimSpace(config.Cfg.Net.WANZone)
	if zone == "" {
		zone = "wan"
	}
	b, err := os.ReadFile(firewallCfgPath)
	if err != nil {
		return // 读不到配置 ⇒ 什么也不做（宁可不管，也不能凭猜改防火墙）
	}
	sec, ref, ok := FindZone(ParseUCIConfig(string(b)), zone)
	if !ok {
		logger.Warn("auto wan: no firewall zone with that name, modem interfaces stay usable only by the router itself",
			zap.String("zone", zone))
		return
	}
	list, ok := managedInterfacesNowOK()
	if !ok {
		logger.Warn("auto wan: cannot read uci network, skip the firewall zone sync")
		return
	}
	live := map[int]bool{}
	if mgr != nil {
		for _, mod := range mgr.listModems() {
			if mod != nil && mod.isRunningNow() {
				live[mod.ID] = true
			}
		}
	}
	var want []string
	for _, it := range list {
		if live[it.ModemID] {
			want = append(want, it.Name)
		}
	}
	ours := strings.Fields(sec.Options[FirewallZoneAutoOption])
	add, del := ZoneSyncPlan(want, sec.Lists["network"], ours)
	cmds := FirewallZoneCommands(ref, add, del, ours)
	if len(cmds) == 0 {
		return // 已经对了（或一台模组都没有）⇒ 一个字节都不写、也不 reload 防火墙
	}
	if err := runNetProvCmds(cmds); err != nil {
		logger.Warn("auto wan: firewall zone update failed", zap.String("err", err.Error()))
		return
	}
	if len(add) > 0 {
		logger.Info("auto wan: modem interfaces added to firewall zone（局域网才能经它上网）",
			zap.String("zone", zone), zap.Strings("added", add))
	}
	if len(del) > 0 {
		logger.Info("auto wan: modem interfaces removed from firewall zone",
			zap.String("zone", zone), zap.Strings("removed", del))
	}
}

// sweepStaleAutoWAN 周期性清理：**只有当我们建的接口没有对应的运行中模组、而且它绑的设备也不在了**
// 才删（两道保险，免得把正在用的或别人手工建的东西删掉）。
//
// 为什么用轮询而不是只在"模组消失"事件里删：事件与轮询可能错开（一轮循环正在建接口时模组被拔了），
// 轮询能自己愈合，不会留下一张永远没人管的接口。实测延迟就是一轮（≤15 秒）。
func sweepStaleAutoWAN(mgr *ModemManager) {
	live := map[int]bool{}
	if mgr != nil {
		for _, mod := range mgr.listModems() {
			if mod != nil && mod.isRunningNow() {
				live[mod.ID] = true
			}
		}
	}
	removedV6 := false
	for _, it := range managedInterfacesNow() {
		if live[it.ModemID] {
			continue // 这台模组还在（接口就是它该有的）
		}
		if deviceStillThere(it.Device) {
			continue // 设备还在（可能已被别的模组/手工接管）→ 不动
		}
		if err := removeAutoWANInterface(it); err != nil {
			logger.Warn("auto wan: cleanup failed",
				zap.String("iface", it.Name), zap.String("device", it.Device), zap.Error(err))
			continue
		}
		logger.Info("auto wan: stale interface removed",
			zap.Int("modem", it.ModemID), zap.String("iface", it.Name), zap.String("device", it.Device))
		if strings.HasSuffix(it.Name, "_6") {
			removedV6 = true
		}
	}
	// 模组拔了、v6 兄弟接口被删干净了 ⇒ 那套防火墙规则（全局 DNS 重定向！）也别留着。
	// ⚠️ 只在这种情况下收：链路只是掉了（模组/网卡还在）时接口不会被删，走不到这里。
	if removedV6 {
		dropWAN6FirewallIfUnused()
	}
}

// cleanupAutoWANForModem 模组消失时立刻清理它的接口（不用等下一轮轮询）。
//
// ⚠️ 同样带 deviceStillThere 这道闸：USB 抖动 / 驱动重载时网卡可能还在，那就别删
// （下一轮轮询会再看一次，真拔了才删）。Windows 上是空操作（netprov_windows.go）。
func cleanupAutoWANForModem(id int) {
	if id < 0 {
		return
	}
	// 主机侧拨号器（QMI 的 quectel-CM）先收掉：它占着 /dev/cdc-wdm0，
	// 不清掉的话别的模组 / 下次插上都打不开控制口。
	stopHostDial(id, "模组不在了")
	if !AutoWANEnabled() {
		return
	}
	removedV6 := false
	for _, it := range managedInterfacesNow() {
		if it.ModemID != id || deviceStillThere(it.Device) {
			continue
		}
		if err := removeAutoWANInterface(it); err != nil {
			logger.Warn("auto wan: cleanup failed",
				zap.String("iface", it.Name), zap.String("device", it.Device), zap.Error(err))
			continue
		}
		logger.Info("auto wan: interface removed (modem gone)",
			zap.Int("modem", id), zap.String("iface", it.Name), zap.String("device", it.Device))
		if strings.HasSuffix(it.Name, "_6") {
			removedV6 = true
		}
	}
	// 模组没了：v6 兄弟接口一并删干净，那套防火墙规则（含**全局 DNS 重定向**）也一起收掉。
	if removedV6 {
		dropWAN6FirewallIfUnused()
	}
}
