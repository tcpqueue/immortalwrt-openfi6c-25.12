//go:build !windows

package kernel

import (
	"fmt"
	"go.uber.org/zap"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"modemd/module/config"
	"modemd/module/dial"
	"modemd/module/netdev"
	"modemd/module/netprov"
	"modemd/module/sysfs"
	"modemd/module/usbdesc"
)

// autoWANForModem 单台模组：拨号（可选）→ 网卡 → WAN 接口 → 主机侧拨号器。
func autoWANForModem(mod *Modem) {
	if mod == nil {
		return
	}
	// SoC 内置模组（共享慢总线）的数据面由**设备厂商**托管，modemd 一律不碰：
	// 本机是 netifd 的 `proto=mu300cell` 绑在 `sipa_eth0` 上（联网本来就通），
	// 而模组没有自己的 USB 网卡，modemd 会把 **`usb0`（LAN 桥的成员口）**当成它的数据网卡 ——
	// 照那个走会去建 WAN 接口/写 APN/激活 PDP，直接把用户的局域网弄断。
	// 详见 module/kernel/atdev_mu300_policy.go 的 managedDataPlane。
	if netdev.ManagedDataPlane(mod.ATPort) {
		stopHostDial(mod.ID, "SoC 内置模组的数据面由设备厂商托管，modemd 不组网")
		return
	}
	// 拨号总开关（页面「拨号上网 → 拨号开关」）关掉时，整台模组这一轮什么都不做：
	// 不激活 PDP、不拉主机侧拨号器、也不建 WAN 接口，并把已经在跑的拨号器收掉
	// —— 这才是"所有模式都不拨号"的样子（只跳过其中一种方式就不叫总开关了）。
	if !config.DialEnabled() {
		stopHostDial(mod.ID, "拨号总开关已关闭（页面「拨号开关」）")
		return
	}
	// ★【用户 2026-10-13 要求】一发现模组（数据网卡出现）就把 WAN 接口建出来，
	// 别等拨号/复位流程走完。
	//
	// 为什么：QMI 模组拨号前要做“飞行模式 → AT+CFUN=1,1”复位（配置 reset_before_dial），
	// 模组 USB 会消失 40~90 秒；这期间下面的 `!isRunningNow()`（AT 不在了）与
	// `AirplaneOn()`（复位期间的中间态）都会提前 return，把“建接口”整段跳过 ——
	// 用户看到的就是「网络 → 接口」里空等一两分钟（实测反馈：
	// “会创建 就是好慢创建，网络接口能不能放在找到模块就创建”）。
	// 接口只是个 dhcp 客户端（地址由主机侧拨号器/模组给），先建好在那儿等着完全无害。
	ensureWANInterfaceEarly(mod)
	// ★ IPv6（2026-10-13 真机定位）：odhcpd 不认带 `from` 的默认路由，会把 RA lifetime
	// 归零 ⇒ LAN 侧公网前缀全部作废。这条与"谁来拨号"无关（ECM/MBIM 的公网前缀走 RA
	// 一样需要它），所以放在通用位置；而"删 WAN 网卡上的重复前缀路由"是 QMI 特有的，
	// 收窄到主机侧拨号器分支里做，**不碰 ECM / MBIM / RNDIS 的规则**（用户 2026-10-13 要求）。
	ensureLANRADefault()
	if !mod.isRunningNow() {
		// 模组不在了：接口留给 sweepStaleAutoWAN 按"设备还在不在"判（USB 抖动时网卡可能还在），
		// 但拨号器必须现在收掉 —— 它占着 /dev/cdc-wdm0，别的模组/下次插上都打不开。
		stopHostDial(mod.ID, "模组不在了")
		return
	}
	// 飞行模式：别拨号、别组网（拨号器在 SetAirplane 里已经立即收掉了）。
	// 关了以后下一轮就会回到正常流程自动重拉（并把退避清掉，见 afterAirplaneChange）。
	if mod.AirplaneOn() {
		stopHostDial(mod.ID, "飞行模式已开（射频关闭），数据链路一起停")
		return
	}
	// 数据漫游开关（「高级配置 → 参数设置」）：**国际漫游中**（卡归属 MCC ≠ 注册网 MCC）且开关关着时，
	// 这一轮整台模组不拨号，并把在跑的主机侧拨号器收掉。
	//
	// ⚠️ 只管国际漫游（`<stat> == 5` 只是前置条件）：同国异网漫游（国内运营商之间）与本地网一样
	// 照常上网 —— 用户 2026-10-06 明确要求"漫游是为了国外卡支持，本地卡是不影响的"。
	// 判据只看注册状态 + 卡归属，不看厂商指令 ⇒ 所有模组通用，见 roaming.go。
	//
	// 顺手把"自动拨号失败退避"清掉：漫游结束后要**马上**恢复，
	// 不能因为进入漫游前那次失败还在退避里（最长 60 秒）而多等一轮。
	if why := RoamBlockReason(mod); why != "" {
		stopHostDial(mod.ID, why)
		resetAutoDialTried(mod.ID)
		return
	}
	if switched, err := mod.prepareTDTechWANUSB(); switched || err != nil {
		info := AutoWANInfo{At: time.Now(), Note: "已启用 MT5700M-CN 的 USB 数据配置，等待内核重新枚举"}
		if err != nil {
			info.Note, info.Error = "", err.Error()
		}
		noteAutoWAN(mod, info)
		return
	}
	st := mod.DialStatus()
	if config.Cfg.Net.AutoDial && mod.refreshTDTechNDIS(st) {
		st = mod.DialStatus()
	}
	// USB 类型（决定主机侧怎么拨）：拨号状态里有就用，没有（比如没做过模组侧拨号）就问一次探测
	kind := st.Kind
	if strings.TrimSpace(kind) == "" {
		if info := mod.USBType(false); info != nil {
			kind = info.Kind
		}
	}
	proto := netprov.WANProtoForKindWith(kind, ModemProtoAvailable())
	hostDialed := netprov.WANHostDialByDaemon(kind)

	// 用户手动「断开」过就别再自动拨，尊重人的意思（重新插拔或手动拨号会清掉这个标记）
	//
	// ⚠️ QMI（hostDialed）**不做模组侧自动拨号**：数据呼叫归主机侧拨号器，模组侧再激活一个
	// 上下文会占住模组**唯一的数据承载**，拨号器随后请求数据呼叫就被拒（实测 RM502Q-GL：
	// `QMUXError 0xe`、`wwan0 link_state 0`）。所以这里反过来 —— 把我们自己激活过的
	// 数据上下文收掉，让拨号器独占（见 releaseHostDialDataContext）。
	dialNote := ""
	dialErr := ""
	if hostDialed {
		dialNote = releaseHostDialDataContext(mod, st, false)
	} else if config.Cfg.Net.AutoDial && !st.ManualStop &&
		st.State != dial.DialConnected && st.State != dial.DialDialing {
		// 失败后 60 秒内不重试（避免每轮循环都去写 APN + 激活，扰模组也刷日志）
		if !autoDialAllowed(mod.ID) {
			// 退避中：这一轮不去动模组，但**接口照建**（用户 2026-10-09：退避时
			// 「网络 → 接口」里也不能是空的）
			dialNote = "模组侧自动拨号在退避中（上次失败，60 秒内不重试），接口先建好"
		} else if _, err := autoDialTry(mod); err != nil {
			// 模组侧拨号没成 —— **不能因此不建接口**（用户 2026-10-09 报的 ECM 那个问题：
			// 切到 ECM 后模组刚复位、AT+CGACT 被拒，于是「网络 → 接口」里**什么都没有**；
			// 而 ECM/NCM/RNDIS 的接口只是个 DHCP 客户端，建好在那儿等着才对 ——
			// 模组自己激活 PDP（很多 ECM 固件会自动连）或下一轮重试成功时，地址立刻就有。
			dialErr = "自动拨号失败：" + err.Error()
			if hostDialed {
				// QMI：模组侧的 AT 激活失败不影响主机侧拨号器（真正拨号的是它）
				dialNote = "模组侧拨号没成（" + err.Error() + "），主机侧拨号器不受影响"
			} else {
				dialNote = "模组侧还没拨上（" + err.Error() + "），接口先建好等它"
			}
		}
		st = mod.DialStatus()
		if k := strings.TrimSpace(st.Kind); k != "" {
			kind = k
			proto = netprov.WANProtoForKindWith(kind, ModemProtoAvailable())
			hostDialed = netprov.WANHostDialByDaemon(kind)
		}
	}
	// 模组侧没连上：**接口照样要建**。
	//
	// 以前这里对非 QMI 直接 `return`（"模组这一端都没连上，主机侧建了也白建"），
	// 后果是用户在「网络 → 接口」里看不到任何东西（2026-10-09 ECM 那个问题就是这个）：
	// ECM / NCM / RNDIS 的接口只是个 **DHCP 客户端**，模组自己的 PDP 一起来地址就有，
	// 没建的话连“等”的机会都没有；模组侧到底连没连上由下面那句 note 如实说明。
	// QMI 本来就不看这个（真正拨号的是主机侧厂商拨号器）。
	if st.State != dial.DialConnected && !hostDialed {
		why := "模组侧还没连上（" + dial.DialStateText(st.State) + "）"
		if dialErr != "" {
			why = "模组侧还没连上（" + dialErr + "）"
		} else if dialNote != "" {
			why = dialNote
		}
		dialNote = strings.TrimSpace(why + "；接口先建好，等它拨上就自动拿地址")
	}

	// 主机侧网卡：所有类型都必须有它
	// （netifd 的 DHCP 要在**数据网卡**上取地址；厂商拨号器的 `-i` 要的就是这张/它的父口）；
	// ★ 2026-10-13：MBIM 也不再例外 —— 它的会话改成由 modemd 托管的 quectel-CM（mbim-cm.c）建，
	// 所以同样要拿到 wwan0，拿不到就先不建接口（建了也没人拨）。
	// 只有"proto 的 device 是控制口字符设备"（= host_dial 关着、退回 proto=mbim）时才允许网卡缺席。
	dataDev, dialDev, netErr := pickModemNetdev(mod)
	if netErr != nil && !netprov.WANProtoUsesControlDevice(proto) {
		stopHostDial(mod.ID, netErr.Error())
		noteAutoWAN(mod, AutoWANInfo{At: time.Now(), Device: "", Proto: proto, Error: netErr.Error()})
		return
	}
	info := ensureModemWAN(mod, dataDev, st)
	if info.Created {
		// 【用户 2026-10-13 要求】接口刚建好 → 拨号前要停 1 秒（见 waitWANInterfaceSettled）
		markWANInterfaceCreated(mod.ID)
	}
	if dialNote != "" {
		info.Note = strings.TrimSpace(info.Note + "；" + dialNote)
	}
	// 模组侧拨号失败：接口已经建好了，但页面上要如实报出来（只有接口也没建成功时才被覆盖）
	if dialErr != "" && info.Error == "" {
		info.Error = dialErr
	}
	// 数据网卡与拨号器网卡不是同一张时（QMAP）在页面上说一句：出问题时这两个名字最容易被搞混
	if dataDev != "" && dialDev != "" && dataDev != dialDev {
		info.Note = strings.TrimSpace(info.Note +
			"；数据网卡是 " + dataDev + "（QMAP mux，地址在它上面），父口 " + dialDev + " 给拨号器用")
	}
	// v6 兄弟接口只该出现在"该建"的模组上（判据见 modemV6SiblingWanted，统一一处）：
	//  · MBIM：v6 由 netifd 的 umbim 在同一张接口上配 ⇒ 不建（会和它抢前缀）
	//  · QMI（主机侧拨号器）：地址/路由归拨号器，但 netifd/LAN 侧还要这么一张 v6 上联 ⇒
	//    **默认建**（老行为），想收掉就写 [net] qmi_v6_sibling = false。
	// 该收的已经建过的要收掉 —— ≤15 秒内生效。判据只看配置层面（modemV6SiblingWanted），
	// **不**看"模组此刻报没报 v6"：那是随链路变化的，拿它当"要不要删接口"的依据会把好接口拆了重建。
	// ★ IPv6 路由修复（**只对 QMI**，不碰 ECM / MBIM / RNDIS 的规则）：
	// 同前缀在 wwan0(metric 20) 与 br-lan(metric 1024) 上打架时，路由器往 LAN 回包会走
	// wwan0 丢到 5G 上行，LAN 侧收不到。详见 fixWANPrefixRouteOnWAN 的注释。
	if modemUsesHostDialer(mod) && config.Cfg.Net.HostDial && dataDev != "" {
		fixWANPrefixRouteOnWAN(dataDev)
	}
	hostDialOwnsV6 := modemUsesHostDialer(mod) && config.Cfg.Net.HostDial
	if !modemV6SiblingWanted(mod, proto) {
		why := wan6SiblingSkipReason(proto, hostDialOwnsV6)
		if n, e := disableModemWAN6(mod.ID, why); e != "" {
			if info.Error == "" {
				info.Error = e
			}
		} else if n != "" {
			info.Iface6 = ""
			info.Note = strings.TrimSpace(info.Note + "；" + n)
		}
	}
	// 主机侧拨号器（QMI）：接口建好之后拉起 / 看着它（幂等，每轮都调）
	if note, errMsg := SyncHostDial(mod, st, hostDialNetdev{Dial: dialDev, Data: dataDev}); note != "" || errMsg != "" {
		if errMsg != "" {
			info.Error = errMsg
		} else {
			info.HostDial = note
		}
	}
	noteAutoWAN(mod, info)
}

// dialHostSideOnce 手动拨号（QMI）时**立即**同步一次主机侧拨号器：找两张网卡 → SyncHostDial。
//
// 与自动组网循环里那段（autoWANForModem 末尾）是同一个动作，只是这里"马上做一次"，
// 让页面上点「拨号」就能连上，不用等到下一轮循环（≤15 秒）。
//
// 返回 (note, errMsg)：note 是"干了什么/现在什么状态"，errMsg 非空表示拨号器起不来。
// 非 OpenWrt（modemd 不托管主机侧拨号器）时返回 ("", "")，由调用方给出手动指引。
func dialHostSideOnce(mod *Modem, st *dial.DialState) (note, errMsg string) {
	if mod == nil || !AutoWANEnabled() {
		return "", ""
	}
	dataDev, dialDev, err := pickModemNetdev(mod)
	if err != nil {
		return "", err.Error()
	}
	return SyncHostDial(mod, st, hostDialNetdev{Dial: dialDev, Data: dataDev})
}

// pickModemNetdev 找这台模组在主机侧的两张网卡：
//
//	dataDev 数据网卡（地址 / 默认路由 / IPv6 都在这张上）
//	dialDev 拨号器要用的那张（`-i`、驱动属性如 raw_ip；QMAP 下是**父口**）
//
// 做法：AT 口 → 所属 USB 设备目录（/sys/.../usb2/2-1 → "2-1"）→ 枚举 /sys/class/net，
// 谁的 device 软链解析出来落在 "/2-1/" 下面就是它（NCM/ECM/RNDIS 的网卡都这样挂在接口目录下）。
//
// ⚠️ QMAP 模组上这两张**不是同一张**（实测 RM502Q-GL + 厂家 qmi_wwan_q、qmap_mode=1）：
//
//	wwan0    父口（QMAP 聚合链路，**没有 IP**）
//	wwan0_1  mux 口（数据口，udhcpc 拿到的 10.206.153.100/29 在它上面）
//
// 把 UCI 接口绑到父口就会出现"接口一直拿不到地址" —— netifd 的 udhcpc 在 wwan0 上
// 一直卡着（进程状态 D），页面上看着就是"QMI 拨号器不能用/拿不到 IP"。
// 而 quectel-CM 的 `-i` 要的恰恰是父口，所以两个值都得拿（见 ModemDialNetdev、模组拨号器那段注释）。
func pickModemNetdev(mod *Modem) (dataDev, dialDev string, err error) {
	bus := modemBusPath(mod)
	if bus == "" {
		return "", "", fmt.Errorf("认不出模组的总线拓扑路径（USB / PCIe 都没查回），无法确定它对应哪张网卡")
	}
	entries, err := os.ReadDir(sysfs.NetClassRoot)
	if err != nil {
		return "", "", fmt.Errorf("枚举 %s 失败：%v", sysfs.NetClassRoot, err)
	}
	resolved := make(map[string]string, len(entries))
	for _, e := range entries {
		real, err := filepath.EvalSymlinks(filepath.Join(sysfs.NetClassRoot, e.Name(), "device"))
		if err != nil {
			continue // 不是物理网卡（lo / 桥 / veth 没有 device 软链）
		}
		resolved[e.Name()] = real
	}
	names := netdev.MatchNetdevToBusPath(bus, resolved)
	if len(names) == 0 {
		// PCIe（MHI/rmnet）的网卡，device 软链可能不指向含 BDF 的路径（各家驱动写法不一）。
		// 那就退回"按名字认"：这台模组交出来的网卡叫 wwan*/rmnet*/mhi*，
		// 且**只有一块 PCIe 模组**时才认（多块时不猜，免得拨错人家的网卡）。
		names = pcieNetdevFallback(mod, resolved)
	}
	if len(names) == 0 {
		return "", "", fmt.Errorf("模组 %s 还没交出主机侧网卡（内核枚举要一点时间，稍后会自动重试）", bus)
	}
	return names[0], ModemDialNetdev(names), nil
}

// pcieNetdevFallback PCIe 模组的网卡傅退挑选（按名字，见 pickModemNetdev 里的说明）。
func pcieNetdevFallback(mod *Modem, resolved map[string]string) []string {
	if mod == nil || !netdev.IsCharATDevice(mod.ATPort) {
		return nil
	}
	devs := netdev.ListPCIModemDevices()
	if len(devs) != 1 {
		return nil // 多块 PCIe 模组：不靠名字猜
	}
	var out []string
	for name, path := range resolved {
		if !netdev.IsLikelyMHIface(name) {
			continue
		}
		if len(devs[0].Netdevs) > 0 && !contains(devs[0].Netdevs, name) {
			continue
		}
		_ = path
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool {
		return netdev.NetdevRank(out[i]) < netdev.NetdevRank(out[j]) || (netdev.NetdevRank(out[i]) == netdev.NetdevRank(out[j]) && out[i] < out[j])
	})
	return out
}

// modemBusPath 从 AT 口（或它的兄弟串口）反推总线拓扑路径：
//
//	USB 模组 → USB 设备路径，如 "2-1"
//	PCIe 模组 → PCI 地址（BDF），如 "0000:01:00.0"
//
// 两者都能被 netdev.MatchNetdevToBusPath 当成"路径里的一段"去匹配（BDF 在 sysfs 路径里是完整一段）。
func modemBusPath(mod *Modem) string {
	ports := make([]string, 0, len(mod.TTYList)+1)
	if mod.ATPort != "" {
		ports = append(ports, mod.ATPort)
	}
	ports = append(ports, mod.TTYList...)
	for _, p := range ports {
		if dir := usbdesc.UsbDeviceDir(p); dir != "" {
			return filepath.Base(dir)
		}
	}
	for _, p := range ports {
		if dev, ok := netdev.PcieDeviceForPort(p); ok {
			return dev.BDF
		}
	}
	return ""
}

// pickModemWDM 找这台模组的数据控制口（QMI / MBIM 的字符设备）。
//
// USB 模组：/dev/cdc-wdm0（按 USB 拓扑路径匹配 —— /sys/class/usbmisc/cdc-wdm0/device 落在 /2-1/ 下）
// PCIe 模组：/dev/mhi_QMI0、/dev/qcqmi0、/dev/wwan0qmi0、/dev/wwan0mbim0（见 pcie.go）
func pickModemWDM(mod *Modem) string {
	if dev := pcieCtrlDeviceForModem(mod); dev != "" {
		return dev
	}
	bus := modemBusPath(mod)
	if bus == "" {
		return ""
	}
	entries, err := os.ReadDir(usbmiscRoot)
	if err != nil {
		return ""
	}
	resolved := make(map[string]string, len(entries))
	for _, e := range entries {
		dir := filepath.Join(usbmiscRoot, e.Name())
		real, err := filepath.EvalSymlinks(filepath.Join(dir, "device"))
		if err != nil {
			// 有些内核版本在 usbmisc 条目下不放 device 软链
			real, err = filepath.EvalSymlinks(dir)
			if err != nil {
				continue
			}
		}
		resolved[e.Name()] = real
	}
	names := netdev.MatchNetdevToBusPath(bus, resolved)
	if len(names) == 0 {
		return ""
	}
	return "/dev/" + names[0]
}

// protoScriptInstalled netifd 的协议脚本在不在。
// 本包的 quectel.sh、固件/feeds 的 mbim.sh（要 umbim）、老写法的 qmi.sh（要 uqmi）都从这儿找，
// 找不到基本就是包没装全 / 装的是老版本 —— 照样如实报出来，别硬建一个起不来的接口。
func protoScriptInstalled(proto string) bool {
	if !netprov.WANProtoNeedsScript(proto) {
		return true
	}
	_, err := os.Stat(filepath.Join(protoScriptRoot, strings.ToLower(proto)+".sh"))
	return err == nil
}

// ModemProtoAvailable 路由器上装没装"合并接口"协议（ModemProtoName，luci-proto-modem-pro 包）。
//
// 装了：QMI 类模组的 WAN 接口就用它 —— 一张接口同时拿 IPv4(udhcpc) + IPv6(odhcp6c)，
// 而 IPv6 前缀由协议自己从 RA 跟随（RFC 7278），于是**不用**再建 modem<id>_6 那张
// v6 兄弟接口，也不用 modemd 往 ip6prefix 里写值。
// 没装：保持老行为（proto=dhcp + 另建 modem<id>_6），功能不受影响 ——
// 这样同一个 modemd 二进制在装了/没装 luci-proto-modem-pro 的路由器上都能工作。
//
// 判据就是协议脚本在不在（netifd 认的就是 /lib/netifd/proto/<proto>.sh）。
// 非 Linux / 非 OpenWrt 上 AutoWANEnabled() 为假 ⇒ 恒 false。
func ModemProtoAvailable() bool {
	if !AutoWANEnabled() {
		return false
	}
	_, err := os.Stat(filepath.Join(protoScriptRoot, netprov.ModemProtoName+".sh"))
	return err == nil
}

// QMI 拿不到 DHCP 租约就降级成 none 的决策与计数在 netprov_qmiproto.go（跨平台，可单测）。

// ensureModemWAN 检查 UCI：这台模组的网卡有没有对应的 WAN 接口；没有就建（含防火墙）。
//
// 接口的**协议按模组的 USB 类型选**（WANProtoForKind）：
//
//	ecm / ncm / rndis → dhcp（device = 主机侧网卡）
//	qmi               → dhcp（device = 主机侧网卡 wwan0；地址/路由由 netifd 的 DHCP 取，
//	                      数据呼叫由 modemd 自己托管的厂商拨号器做，见 hostdial.go）
//	mbim              → mbim（device = /dev/cdc-wdmX + APN；由 umbim 拨号）
//
// 四种情况都会写进 Note/Error 让页面看得到：已存在（不动）/ 刚建 / 类型不匹配的提醒 / 建失败。
func ensureModemWAN(mod *Modem, dev string, st *dial.DialState) AutoWANInfo {
	iface := WANInterfaceName(mod.ID)
	kind := ""
	apn, pdp := "", ""
	if st != nil {
		kind, apn, pdp = st.Kind, st.APN, st.PDPType
	}
	proto := wanProtoForModemWAN(mod, kind, ModemProtoAvailable())
	// 静态绑定类机型（FM350 系列）：proto 必须是 static —— 它的网卡上没有 DHCP 服务器，
	// dhcp 与 modem_pro 都只会永远等租约（见 netprov_hoststatic.go）。
	hostStatic := proto == WANProtoHostStatic
	info := AutoWANInfo{At: time.Now(), Iface: iface, Device: dev, Proto: proto}

	// ① 同名接口已经存在 → 不动它（手工配置优先）。
	// 看的是 proto 而不是 device：mbim 的 device 是控制口字符设备，
	// 拿它跟网卡比对会误判成"接口不存在"进而把别人的配置盖掉。
	if cur, err := uciGet("network." + iface + ".proto"); err == nil && strings.TrimSpace(cur) != "" {
		curProto := strings.TrimSpace(cur)
		curDev, _ := uciGet("network." + iface + ".device")
		info.Note = "WAN 接口 " + iface + "（proto=" + curProto
		if d := strings.TrimSpace(curDev); d != "" {
			info.Note += "，device=" + d
		}
		info.Note += "）已存在"
		// 旧版本自动建的 QMI 接口写的是 OpenWrt 自带的 qmi（uqmi 拨）或 quectel
		// （netifd 跑 quectel-CM），上一版又试过 none（不配置协议），MBIM 则一直是 mbim
		// （netifd 跑 umbim）。现在的口径统一了：**都用 modem_pro（模式 1/3）或 dhcp（模式 2）**，
		// 数据呼叫归 modemd 托管的厂商拨号器（MBIM 由 quectel-CM 的 mbim-cm.c 做，见 netprov_proto.go），
		// netifd 那侧只负责取地址/路由。
		// 这些都是我们**自己建**的接口（modem<id>），直接就地改，不劳用户去 LuCI 里删了重建。
		//
		// ⚠️ 只会在 dhcp / none / modem_pro 之间调，而 none **只有显式配 [net] qmi_proto = "none" 才会用**：
		// “拿不到租约就自动降级成 none”那条已经去掉（用户 2026-10-09：none 就是不上网）。
		wantProto := proto
		if !hostStatic && modemUsesHostDialer(mod) && config.Cfg.Net.HostDial {
			wantProto = netprov.QmiWantProto(config.Cfg.Net.QMIProto, curProto, ModemProtoAvailable())
		}
		// mbim 也算"我们建的"：2026-10-13 起它要被就地改成 modem_pro（真机 FM160-CN MBIM 实测）。
		// 不在这个集合里的话它就永远不会迁移，一直停在 proto=mbim。
		ourProto := curProto == "qmi" || curProto == "quectel" || curProto == "none" ||
			curProto == "dhcp" || curProto == "mbim" || curProto == netprov.ModemProtoName ||
			curProto == WANProtoHostStatic
		if isManagedIface(iface) && curProto != wantProto && ourProto {
			if err := runNetProvCmds(hostStaticOrSwitchCommands(mod, hostStatic, iface, wantProto, dev, uciOptionNames(iface), st)); err != nil {
				info.Error = "把 " + iface + " 的 proto 从 " + curProto + " 调成 " + wantProto + " 失败：" + err.Error()
			} else {
				logger.Info("auto wan: migrated legacy qmi proto",
					zap.Int("modem", mod.ID), zap.String("iface", iface),
					zap.String("from", curProto), zap.String("proto", wantProto), zap.String("device", dev))
				info.Note += "；已从旧版 " + curProto + " 切成 " + wantProto +
					"（地址/路由由 netifd 取，数据呼叫仍由 modemd 托管的" +
					hostDialNameForModem(mod) + "做）"
				curProto, curDev = wantProto, strings.TrimSpace(dev)
			}
		}
		// 静态绑定类机型（FM350）：IPv6 走 RA（SLAAC）+ 手工默认路由 via fe80::1。
		// 每轮检查一次；已经拿到全局 v6 地址就什么都不做
		// （见 HostStaticV6BringUpNeeded）—— 否则每轮 ifdown/ifup 会把链路反复打断。
		if hostStatic && strings.TrimSpace(dev) != "" {
			hostStaticV6Ensure(mod, iface, dev, st, &info)
			// LAN 侧：模组那个 /64 在 br-lan 上的地址若不在、或被标 deprecated，内网设备就
			// **不会**再从这个前缀 SLAAC 出公网地址（只剩 ULA）⇒ 电脑 v6 不通
			// （2026-10-13 用户实测，取证见 netprov_lan6_addr.go 顶部）。
			// ⚠️ 只对 FM350 这类"主机网卡 + 静态绑定"的机型生效（函数内部判）。
			lan6PrefixAddrRepairFor(mod, dev, &info)
		}
		// 静态绑定类机型（FM350）：地址/掩码/网关/DNS 每轮对齐。
		//
		// 地址是运营商 CGNAT 池里分的，**每次重拨都会变**；不跟着改就会出现
		//「接口还指着上一个地址」—— ping 得通是假象，实际不通。
		// 只有**真的变了**才动（HostStaticSame）：地址每轮都去问，而每次都 ifup
		// 会把接口反复打断（几十毫秒到几秒断流）。
		if hostStatic && isManagedIface(iface) && strings.TrimSpace(dev) != "" {
			if p, ok := HostStaticPlanFromDial(iface, st, mod); ok {
				cur := HostStaticCurrentPlan(iface)
				if curProto != WANProtoHostStatic || !HostStaticSame(cur, p) {
					if err := runNetProvCmds(HostStaticCommands(iface, dev, p, uciOptionNames(iface))); err != nil {
						info.Error = "刷新静态地址失败：" + err.Error()
					} else {
						logger.Info("auto wan: host-static refreshed",
							zap.Int("modem", mod.ID), zap.String("iface", iface),
							zap.String("from", strings.TrimSpace(cur.IP)), zap.String("to", p.IP))
						info.Note += "；静态地址已刷新为 " + p.IP + "（网关 " + p.Gateway + "）"
						curProto, curDev = WANProtoHostStatic, strings.TrimSpace(dev)
					}
				}
			}
		}
		if curProto != proto && wantProto == proto {
			// 不擅自动别人的配置，但要如实提醒：按这台模组的 USB 类型，协议应该不是这个
			info.Note += fmt.Sprintf("；这台模组是 %s，按类型建议用 proto=%s（没帮你改，你确认后再动）",
				kindOrUnknown(kind), proto)
		}
		// 主机侧拨号（QMI）的模组：把 force_link 打开。
		//
		// 为什么必须开（2026-10-09 真机 FM160-CN 实测）：这类模组的 wwan0 要等拨号器把
		// QMI 数据呼叫建起来才有载波；而链路一断，netifd 就按“链路丢失”把接口（连同网卡）
		// 拉下来 —— 网卡一 down，驱动就不再上报 link_state，于是**永远等不到载波回来**：
		// 接口卡在 down、地址/路由全没，日志里只有 `link is down` / `is now down`。
		// 打开 force_link 之后 netifd 不为载波变化把接口拉下来，拨号器重拨上来时
		// udhcpc 立刻就能拿到租约（实测：手动 ip link set up 后 3 秒 carrier=1、
		// 几十秒内拿到 10.54.29.212/29 + 默认路由）。
		if isManagedIface(iface) && modemUsesHostDialer(mod) && config.Cfg.Net.HostDial {
			if v, _ := uciGet("network." + iface + ".force_link"); strings.TrimSpace(v) != "1" {
				if err := runNetProvCmd([]string{"uci", "set", "network." + iface + ".force_link=1"}); err == nil {
					if err := runNetProvCmd([]string{"uci", "commit", "network"}); err == nil {
						info.Note += "；已给 " + iface + " 打开 force_link（载波丢失时不让 netifd 把接口拉下来）"
						logger.Info("auto wan: force_link enabled (keep interface up on link loss)",
							zap.Int("modem", mod.ID), zap.String("iface", iface), zap.String("dev", curDev))
					}
				}
			}
		}
		// 老版本建出来的接口可能只有 option device、没有 option ifname —— 老版 LuCI（Lua）
		// 的网络模型只认 ifname，这种接口在「网络 → 接口」里会显示成「不支持的协议类型」。
		// 这里只给**同一张网卡**补一个同名 ifname（语义不变），且绝不给控制口那类接口补。
		curDev = strings.TrimSpace(curDev)
		// 网卡名**不是固定的**：cdc_ncm/cdc_ether 是 ethN、qmi_wwan 是 wwanN，N 由枚举顺序决定，
		// 插拔一次就可能 eth2 → eth3。我们建的接口要跟着改绑，否则接口会指着已经不存在的网卡。
		// 只动我们自己那套名字（modem<id> / modem<id>_6）；别人手工建的接口不碰。
		if isManagedIface(iface) {
			controlDev := netprov.WANProtoUsesControlDevice(curProto)
			want := dev
			if controlDev {
				want = pickModemWDM(mod) // mbim（及历史 qmi/quectel）的 device 是控制口（/dev/cdc-wdmN）
			}
			if want != "" && curDev != "" && curDev != want {
				if err := runNetProvCmds(RebindInterfaceCommands(iface, want, controlDev)); err != nil {
					info.Error = "改绑到 " + want + " 失败：" + err.Error()
				} else {
					logger.Info("auto wan: interface rebound", zap.Int("modem", mod.ID),
						zap.String("iface", iface), zap.String("from", curDev), zap.String("to", want))
					info.Note += fmt.Sprintf("；网卡名从 %s 变成 %s，已改绑", curDev, want)
					curDev = want
				}
			}
		}
		// 改绑之后再补 legacy ifname（改绑时已经一起写了）
		if curDev != "" && !netprov.WANProtoUsesControlDevice(curProto) {
			if curIf, _ := uciGet("network." + iface + ".ifname"); strings.TrimSpace(curIf) == "" {
				if err := runNetProvCmd([]string{"uci", "set", "network." + iface + ".ifname=" + curDev}); err == nil {
					if err := runNetProvCmd([]string{"uci", "commit", "network"}); err == nil {
						info.Note += "；补了 legacy ifname=" + curDev + "（老版 LuCI 只认它）"
						logger.Info("auto wan: legacy ifname added",
							zap.Int("modem", mod.ID), zap.String("iface", iface), zap.String("ifname", curDev))
					}
				}
			}
		}
		// QMI 的 dhcp 到底行不行？netifd 说这张接口没 up、还在 pending（或网卡上根本没地址）
		// ⇒ 算一轮“起不来”。**只记着，不降级**（用户 2026-10-09：interface 要保留 dhcp），
		// 连续 qmiStuckWarnEvery 轮才在日志里提醒一句：地址/路由可能得靠主机侧拨号器。
		if dev != "" && modemUsesHostDialer(mod) && config.Cfg.Net.HostDial && strings.EqualFold(curProto, "dhcp") {
			if dhcpProtoStuck(iface) || netdevAddrs(dev) == "" {
				if n := netprov.QmiStuckBump(mod.ID); n%netprov.QmiStuckWarnEvery == 0 {
					logger.Warn("auto wan: QMI 接口还是 dhcp 但一直没有租约（接口保持 dhcp 不动；地址/路由也可能由主机侧拨号器给）",
						zap.Int("modem", mod.ID), zap.String("iface", iface), zap.String("dev", dev),
						zap.Int("no_addr_rounds", n), zap.Int("warn_every", netprov.QmiStuckWarnEvery))
				}
			} else {
				netprov.QmiStuckReset(mod.ID)
			}
		}
		if dev != "" {
			if addr := netdevAddrs(dev); addr != "" {
				info.Note += "，已拿到 " + addr
			} else if !netdevUp(dev) {
				// 接口存在但没起来（重启过网络 / 手工 down 过）→ 拉一下，幂等
				if err := runNetProvCmd([]string{"ifup", iface}); err != nil {
					info.Error = "ifup " + iface + " 失败：" + err.Error()
				} else {
					info.Note += "；之前没起来，已 ifup"
				}
			} else if netprov.WANProtoHostDialed(curProto) {
				info.Note += "，还没拿到地址（主机侧拨号器还在拨：起 wwan0 + 取地址要几秒到几十秒）"
			} else {
				info.Note += "，但还没拿到地址（DHCP 没回：这类模组要在主机侧手工配一次网卡 IP）"
			}
		}
		// IPv6：除 mbim（只在 host_dial 关着时用；umbim 在同一张接口上就配好 v4+v6）
		// 之外都要 v6 兄弟接口 —— **但主机侧拨号（QMI / MBIM）例外**：它们的 IPv4/IPv6 地址、
		// 路由、DNS（包括 wwan0 上的全局 v6 地址）**都由主机侧拨号器给**（实测那些地址是
		// 拨号器加的，不是模组的 RA 来的），再建一张 dhcpv6 兄弟接口只会跟拨号器抢同一张网卡
		// （两张 netifd 接口共用一个 device ⇒ 链路一上来就互相重建，把 CPU 打满）。
		// 开关（[net] wan_ipv6）关掉时整条链路只走 IPv4（见 modemWAN6Allowed）。
		if !hostStatic && modemV6SiblingWanted(mod, curProto) {
			if n, e := ensureModemWAN6(mod, dev, !wan6NonQMIProfileFor(mod)); e != "" {
				info.Error = e
			} else if n != "" {
				info.Iface6 = WAN6InterfaceName(mod.ID)
				info.Note += "；" + n
			}
		}
		return info
	}

	// ② 防火墙区**只看不改**（用户 2026-10-07 要求：内部处理防火墙的都要去掉）
	// ④ 静态绑定类机型（FM350）：**不走 WANPlan**。
	//
	// WANPlan 那套是给 dhcp / modem_pro 用的（地址由 netifd 自己取），而这类模组的
	// 网卡上**永远不会来 DHCP 租约** —— 用那套的结果就是"接口建了、一直 pending、
	// 页面上没有地址"。这里协议/地址/掩码/网关/DNS 全部按模组当场报的值写死。
	//
	// 地址没拿到就先不建：static 没有地址，netifd 只会把它挂成 pending，
	// 页面上多一张空接口反而更难查。下一轮拿到地址就会建出来。
	if hostStatic && strings.TrimSpace(dev) != "" {
		// ⚠️ 顺序要紧：v6 拉起会**重起链路**（发 RS 拿 RA），而链路 down 会清掉 IPv4 默认路由
		// ⇒ 必须放在写 v4 之前，否则建完就会出现"v6 通、v4 不通"。
		hostStaticV6Ensure(mod, iface, dev, st, &info)
		// LAN 侧：模组那个 /64 在 br-lan 上的地址若不在、或被标 deprecated，内网设备就
		// **不会**再从这个前缀 SLAAC 出公网地址（只剩 ULA）⇒ 电脑 v6 不通
		// （2026-10-13 用户实测，取证见 netprov_lan6_addr.go 顶部）。
		// ⚠️ 只对 FM350 这类"主机网卡 + 静态绑定"的机型生效（函数内部判）。
		lan6PrefixAddrRepairFor(mod, dev, &info)
		p, ok := HostStaticPlanFromDial(iface, st, mod)
		if !ok {
			info.Note += "；静态绑定类模组还没报地址（AT+CGPADDR），接口先不建"
			return info
		}
		if err := runNetProvCmds(HostStaticCommands(iface, dev, p, nil)); err != nil {
			info.Error = "建静态接口 " + iface + " 失败：" + err.Error()
			return info
		}
		logger.Info("auto wan: host-static interface ready",
			zap.Int("modem", mod.ID), zap.String("iface", iface), zap.String("dev", dev),
			zap.String("ip", p.IP), zap.String("gw", p.Gateway), zap.Strings("dns", p.DNS))
		info.Note += "；静态绑定：" + p.IP + "/24 网关 " + p.Gateway
		if len(p.DNS) > 0 {
			info.Note += " DNS " + strings.Join(p.DNS, "/")
		}
		return info
	}
	zoneNote := firewallZoneNote(config.Cfg.Net.WANZone, iface)

	// ③ 按协议把该带的选项备齐（mbim 要先确认 umbim 装了；QMI 要先确认拨号器在）
	plan := WANPlan{
		ModemID: mod.ID, Iface: iface, Device: dev, Proto: proto,
		Metric: config.Cfg.Net.WANMetric,
	}
	switch {
	case netprov.WANHostDialByDaemon(kind):
		// QMI / MBIM：**把数据呼叫拉起来**那一步归 modemd 托管的厂商拨号器
		// （quectel-CM 管 QMI 与 MBIM；simcom-cm / fibocom-dial / meig-cm 管各自厂商的 QMI），
		// netifd 那侧只负责取地址（proto=modem_pro 或 dhcp）。
		// 拨号器不在就别建这张空接口 —— 建了也上不了网，先说清楚让人去装。
		// （[net] host_dial = false 时不检查：那时用户自己有别的拨号器。）
		if config.Cfg.Net.HostDial {
			if _, err := hostDialBinaryFor(hostDialKindVendor(kind, mod.Vendor)); err != nil {
				info.Error = fmt.Sprintf("这台模组是 %s（%s）：数据呼叫要用主机侧拨号器（%s），但%s。接口这次不建",
					VendorLabel(mod.Vendor), kindOrUnknown(kind), hostDialNameForModem(mod), err.Error())
				return info
			}
		}
	case netprov.WANProtoUsesControlDevice(proto):
		if !protoScriptInstalled(proto) {
			info.Error = fmt.Sprintf("这台模组的 USB 类型是 %s：接口协议要用 %s，但路由器上没装 %s（找不到 %s/%s.sh）—— 先 opkg install %s，接口这次不建",
				kindOrUnknown(kind), proto, netprov.WANProtoPackage(proto), protoScriptRoot, proto, netprov.WANProtoPackage(proto))
			return info
		}
		wdm := pickModemWDM(mod)
		if wdm == "" {
			info.Error = fmt.Sprintf("这台模组的 USB 类型是 %s：要主机侧（%s）拨号，但没找到控制口 /dev/cdc-wdm*（模组的 MBIM 控制口没枚举出来）",
				kindOrUnknown(kind), proto)
			return info
		}
		plan.WDM, plan.APN, plan.PDPType = wdm, apn, wanPlanPDPType(pdp)
	}

	// ④ 建接口
	var ran []string
	for _, argv := range WANCommands(plan) {
		if err := runNetProvCmd(argv); err != nil {
			info.Error = netprov.WANCommandText(argv) + " 失败：" + err.Error()
			info.Note = "自动建 WAN 接口没成功"
			if zoneNote != "" {
				info.Note += "（" + zoneNote + "）"
			}
			logger.Warn("auto wan: command failed",
				zap.Int("modem", mod.ID), zap.String("cmd", netprov.WANCommandText(argv)), zap.Error(err))
			return info
		}
		ran = append(ran, netprov.WANCommandText(argv))
	}
	info.Created = true
	info.Note = "已创建 WAN 接口 " + iface + "（proto=" + proto
	switch {
	case netprov.WANHostDialByDaemon(kind):
		// QMI / MBIM：netifd 用 modem_pro / dhcp 取地址/路由/DNS，数据呼叫由主机侧厂商拨号器做
		if dev != "" {
			info.Note += "，绑 " + dev + "；地址/路由由 netifd 的 " + proto + " 取，" +
				"数据呼叫由主机侧 " + hostDialNameForModem(mod) + " 做"
		}
	case netprov.WANProtoUsesControlDevice(proto):
		info.Note += "，控制口 " + plan.WDM
		if strings.TrimSpace(apn) != "" {
			info.Note += "，APN " + strings.TrimSpace(apn)
		}
	case dev != "":
		info.Note += "，绑 " + dev
	}
	if proto != "none" {
		info.Note += fmt.Sprintf("，metric %d", config.Cfg.Net.WANMetric)
	}
	info.Note += "）"
	if zoneNote != "" {
		info.Note += "；" + zoneNote
	}
	logger.Info("auto wan: interface provisioned",
		zap.Int("modem", mod.ID), zap.String("iface", iface), zap.String("proto", proto),
		zap.String("device", dev), zap.Strings("cmds", ran))

	// IPv6：除 mbim（只在 host_dial 关着时用）之外都要另建 v6 兄弟接口 ——
	// **主机侧拨号（QMI / MBIM）也不需要**：v4/v6 地址与路由都由主机侧拨号器给
	// （见 modemV6SiblingWanted），另建一张只会和拨号器抢同一张网卡。
	// 开关（[net] wan_ipv6）关掉时一律不建。
	if modemV6SiblingWanted(mod, proto) {
		if n, e := ensureModemWAN6(mod, dev, !wan6NonQMIProfileFor(mod)); e != "" {
			info.Error = e
		} else if n != "" {
			info.Iface6 = WAN6InterfaceName(mod.ID)
			info.Note += "；" + n
		}
	}
	return info
}
