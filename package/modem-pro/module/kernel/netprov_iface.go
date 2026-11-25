//go:build !windows

package kernel

import (
	"context"
	"modemd/module/netprov"
	"net"
	"os"
	"os/exec"
	"strings"

	"go.uber.org/zap"
)

// managedInterfacesNow 读一次 UCI，列出我们管的接口（模组拔掉后要删的就是这些）。
// ⚠️ 分不清"读失败"与"确实没有"（都是 nil）—— 需要分清的场合用 managedInterfacesNowOK。
func managedInterfacesNow() []netprov.ManagedWANIface {
	list, _ := managedInterfacesNowOK()
	return list
}

// managedInterfacesNowOK 同上去，但**能区分**"读了但一个都没有"与"根本没读到"
// （uci 超时/被锁/没装）。第二个返回值 = 这次读取成功。
func managedInterfacesNowOK() ([]netprov.ManagedWANIface, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), netProvTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "uci", "show", "network").Output()
	if err != nil {
		return nil, false
	}
	return ParseManagedWANInterfaces(string(out)), true
}

// removeAutoWANInterface 删接口。（⚠️ 不再动 /etc/config/firewall —— 用户 2026-10-07 要求
// "内部处理这个防火墙的都要去掉"；接口不在任何区里的话由页面上那句提示让人去 LuCI 划。）
func removeAutoWANInterface(it netprov.ManagedWANIface) error {
	// 接口本身：删段 + commit（netifd 会跟着把接口拆掉，选项也一并没了）
	if err := runNetProvCmds([][]string{
		{"uci", "-q", "delete", "network." + it.Name},
		{"uci", "commit", "network"},
	}); err != nil {
		return err
	}
	forgetAutoWAN(it.ModemID)
	return nil
}

// deviceStillThere 接口绑的设备（网卡名或 /dev/cdc-wdmN）现在还存不存在
func deviceStillThere(dev string) bool {
	dev = strings.TrimSpace(dev)
	if dev == "" {
		return false
	}
	if strings.HasPrefix(dev, "/") {
		_, err := os.Stat(dev)
		return err == nil
	}
	_, err := net.InterfaceByName(dev)
	return err == nil
}

// ensureLANRADefault 让 odhcpd 在"看不到默认路由"时也照样宣告自己是默认网关。
//
// 【为什么（2026-10-13 真机定位，H29K + FM160-CN）】
//
//	QMI 拨号器给 wwan0 装的默认路由是**带源前缀限定**的：
//	    default from <prefix>::/64 via fe80::… dev wwan0
//	而 odhcpd 判断"有没有默认路由"时不认这种带 `from` 的 ⇒ 日志刷
//	    odhcpd: No default route present, setting ra_lifetime to 0!
//	⇒ RA 里 lifetime = 0 ⇒ **LAN 客户端拿到的公网前缀立即失效**。
//	症状：PC 上堆了一二十个公网地址**全是 Deprecated**、ping 任何公网 IPv6 都报
//	"传输失败。常见故障"（没有可用源地址），而 ULA（fd32::/60）完全正常。
//	打开 `dhcp.lan.ra_default` 后 PC 立刻拿到 Preferred 地址。
//
// 幂等：已经是 1 就直接返回（不 commit、不重启 odhcpd）。
func ensureLANRADefault() {
	out, err := exec.Command("uci", "-q", "get", "dhcp.lan.ra_default").Output()
	if err == nil && strings.TrimSpace(string(out)) == "1" {
		return
	}
	if err := exec.Command("uci", "set", "dhcp.lan.ra_default=1").Run(); err != nil {
		return
	}
	if err := exec.Command("uci", "commit", "dhcp").Run(); err != nil {
		return
	}
	logger.Info("ipv6: 打开 dhcp.lan.ra_default（odhcpd 不认带 from 的默认路由，" +
		"不加这条它会把 RA lifetime 归零，LAN 的公网 IPv6 全部作废）")
	// 让 odhcpd 重新发一遍 RA（异步，别卡住组网循环）
	go func() {
		_ = exec.Command("/etc/init.d/odhcpd", "reload").Run()
	}()
}

// fixWANPrefixRouteOnWAN 把"交给内网的公网 IPv6 前缀"在 WAN 网卡上的重复路由删掉。
//
// 【为什么（2026-10-13 真机定位）】
//
//	拨号器给 wwan0 配上公网地址后，netifd 会在 wwan0 上也装一条
//	    <prefix>::/64 dev wwan0 proto static metric 20
//	而 odhcpd 又把同一个前缀当 ip6prefix 交给 br-lan：
//	    <prefix>::/64 dev br-lan proto static metric 1024
//	同一前缀两条路由 ⇒ **metric 小的（wwan0，20）赢** ⇒ 路由器往 LAN 侧主机回包时
//	全被丢到 5G 上行 ⇒ LAN 侧永远收不到。
//	症状：PC ping 路由器**自己的公网地址**都超时、ping 公网也超时，但 ULA 一切正常，
//	而且**路由器自己**上 IPv6 毫无问题（它走 wwan0）。
//	实测删掉 wwan0 那条后：回程走 br-lan，PC 立刻通（0.86ms），
//	路由器出网仍 0% 丢包（出网走的是 `default from <prefix> …`，与这条无关）。
//
// 幂等：没有可删的路由时直接返回；每轮自动组网都会调，模组重拨后新装的路由也会被清掉。
func fixWANPrefixRouteOnWAN(wanDev string) {
	if strings.TrimSpace(wanDev) == "" {
		return
	}
	// 1) 取 LAN 侧由 odhcpd 装的公网前缀（proto static 的那几条）
	out, err := exec.Command("ip", "-6", "route", "show", "dev", "br-lan").Output()
	if err != nil {
		return
	}
	lan := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || !strings.HasSuffix(f[0], "::/64") {
			continue
		}
		if !strings.Contains(line, "proto static") {
			continue
		}
		lan[f[0]] = true
	}
	if len(lan) == 0 {
		return
	}
	// 2) 把 WAN 网卡上的同名路由删掉
	out, err = exec.Command("ip", "-6", "route", "show", "dev", wanDev).Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || !lan[f[0]] {
			continue
		}
		if err := exec.Command("ip", "-6", "route", "del", f[0], "dev", wanDev).Run(); err == nil {
			logger.Info("ipv6: 删掉 WAN 网卡上的 LAN 前缀路由（同前缀 metric 更小时，"+
				"路由器往 LAN 回包会走错接口，PC 收不到）",
				zap.String("prefix", f[0]), zap.String("dev", wanDev))
		}
	}
}

// ensureWANInterfaceEarly 尽早把 WAN 接口建出来：只看“模组的数据网卡在不在”，
// 不看模组 AT 在不在、也不看飞行模式/漫游这些会让 autoWANForModem 提前 return 的状态。
//
// 【用户 2026-10-13 要求】“网络接口能不能放在找到模块就创建”。
// 背景见调用点注释：拨号前的模组复位会让 USB 消失 40~90 秒，
// 期间 autoWANForModem 的早退分支会把“建接口”整段跳过，用户在「网络 → 接口」里空等。
//
// 只在**认得出拨号类型**时才建（否则会按错的协议建接口）；
// “总开关关闭”和“SoC 内置模组”这两种明确不组网的情况，调用方在前面已经 return 了。
func ensureWANInterfaceEarly(mod *Modem) {
	if mod == nil {
		return
	}
	dataDev, _, err := pickModemNetdev(mod)
	if err != nil || strings.TrimSpace(dataDev) == "" {
		return // 网卡还没枚举出来，等下一轮
	}
	st := mod.DialStatus()
	kind := strings.TrimSpace(st.Kind)
	if kind == "" {
		if info := mod.USBType(false); info != nil {
			kind = info.Kind
		}
	}
	if netprov.WANProtoForKindWith(kind, ModemProtoAvailable()) == "" {
		return // 类型还没认出来 → 建了也是错协议，等下一轮
	}
	info := ensureModemWAN(mod, dataDev, st)
	if info.Iface != "" {
		// 只在**真的新建**时打日志：autoWANForModem 每 15 秒一轮都会走到这里，
		// 无条件打日志会把 modemd.log 刷爆（第一版就是这么干的）。
		if info.Created {
			markWANInterfaceCreated(mod.ID)
			logger.Info("auto wan: 趁早建接口（不等拨号/复位走完）",
				zap.Int("modem", mod.ID), zap.String("dev", dataDev),
				zap.String("iface", info.Iface), zap.String("proto", info.Proto))
		}
		noteAutoWAN(mod, info)
	}
}
