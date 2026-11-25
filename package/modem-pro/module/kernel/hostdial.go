package kernel

// ====================== 主机侧拨号器（quectel-CM）托管 ======================
//
// 为什么要有这个文件：
//
//	QMI 模组的数据通道不在模组侧 —— AT 侧激活 PDP（AT+CGACT）只说明**模组那一端**就绪，
//	真正把数据链路拉起来的是主机侧拨号器。本项目用模组厂商自己的拨号器
//	（Quectel quectel-CM / SIMCom simcom-cm / Fibocom fibocom-dial / MeiG meig-cm，
//	源码在 third_party/，随 modemd-pro 装到 /usr/lib/modemd/<名字>），它自己就把活干了：
//	    QMI 建 PDN(WDS) → 拉起 wwan0 → 起 udhcpc 拿地址/默认路由/DNS 写进内核。
//	netifd 那侧的接口协议是 **dhcp**（2026-10-03 从 none 改回）：让 netifd 也在 wwan0 上
//	取一遍地址，接口上才看得到 IP（用 none 时 netifd 完全不碰这张网卡，地址只在拨号器
//	内部那份 udhcpc 手里，「网络 → 接口」是空的 —— 实测就是"获取不到 IP"的表现）。
//	接口本身还是要建 —— 把 wwan0 挂进防火墙区靠它（NAT/转发规则按接口的设备下发）。
//	见 WANProtoForKind。
//
// 谁管这个进程：**modemd 自己**。这份代码就是一个幂等的看护器，由自动组网循环
// （每 15 秒一轮，见 AutoWANLoop）调用：
//
//	该跑就拉起 → 跑了就看着 → 退出等一会儿再拉起（指数退避）→ 不该跑就停掉。
//
// 拨号器的输出一行行转发进 modemd 自己的日志（/var/log/modemd.log），排障只看一个文件。
//
// ⚠️ 停的时候要连同**进程组**一起收：quectel-CM 会 popen 一个 `busybox udhcpc`，
// 只杀父进程会把 udhcpc 留在那儿占着网卡的地址（真机踩过：41 个没人管的
// `uqmi --get-pin-status` 把 /dev/cdc-wdm0 占死，新的拨号器打不开控制口）。

import (
	"fmt"
	"modemd/module/config"
	"modemd/module/hostdial"
	"modemd/module/netprov"
	"os"
	"strings"
	"time"
)

const (
	// hostDialRetryDelay 拨号器退出后至少隔这么久才再拉起（别"起-挂-起"刷日志）
	hostDialRetryDelay = 10 * time.Second
	// hostDialMaxRetryDelay 连续"起来就挂"时的最大退避
	hostDialMaxRetryDelay = 60 * time.Second
	// hostDialFastFail 一次运行短于它算"没起来"，退避翻倍
	hostDialFastFail = 8 * time.Second
	// hostDialStopWait 停掉拨号器时等它退出的时间（超时就 SIGKILL 进程组）
	//
	// ⚠️ 别把它调小：拨号器退出前要做**收尾**（quectel-CM 会发
	// StopNetworkInterface / 停掉自己的 PDP），被 SIGKILL 打断会把模组的 QMI 数据服务
	// 留在半开状态 —— 实测（RM502Q-GL，2026-10-03）：之后每一轮 `StartNetworkInterface`
	// 都被模组拒（`QMUXError 0xe`，cause 241/210），而 **AT 侧拨号完全正常**
	// （`AT+CGACT=1,3` 立刻拿到 10.206.20.211）、所有数据上下文都停用了也没用，
	// 只有 `AT+CFUN=1,1` 重启模组才恢复。所以给足时间让它自己走完（10 秒）。
	hostDialStopWait = 10 * time.Second
)

// hostDialBinPathsFor 这个厂商的拨号器候选路径
func hostDialBinPathsFor(vendor ModemVendor) []string { return hostDialSpecFor(vendor).Paths }

// hostDialNameFor 这个厂商的拨号器叫什么（日志与提示文案用）
func hostDialNameFor(vendor ModemVendor) string { return hostDialSpecFor(vendor).Name }

// hostDialKindVendor 该用哪家的拨号器 —— **通常就是模组厂商**，唯一例外是 MBIM。
//
// MBIM 的会话建立（MBIM_CID_CONNECT）**只有 quectel-CM 实现了**：
//
//	· fibocom-dial / simcom-cm 源码里压根没有 MBIM 那套（连 get_driver_type 都没有）；
//	· meig-cm 虽然带了 mbim-cm.c，但没有任何地方把它接上。
//	  实测：第三方源码里只有 quectel-cm 出现 SOFTWARE_MBIM / mbim_request_ops。
//
// 而 quectel-CM 认 MBIM 靠的是**标准 USB CDC 描述符**
// （bInterfaceClass=COMM + bInterfaceSubClass=MBIM，见 device.c 的 get_driver_type 与
// qmidevice_detect），与厂商无关 ⇒ 拿它去拨广和通 / 美格的 MBIM 模组是对路的。
//
// 真机实测（2026-10-13 FM160-CN / 2cb7:0111 MBIM）：quectel-CM 自动找到
// `/dev/cdc-wdm0` + `wwan0`（wwan0 挂在 `2-1:1.0`，正是 `02/0e` 那个 MBIM 控制接口），
// 读完 caps / subscriber / register(Home) / attach 都没问题，就差 connect 那一步。
// hostDialKindVendor 该用哪家的拨号器 —— **通常就是模组厂商**，唯一例外是 MBIM
// （MBIM 的会话建立只有 quectel-CM 实现了，理由见 module/hostdial/spec.go 的 KindVendor）。
func hostDialKindVendor(kind string, vendor ModemVendor) ModemVendor {
	return hostdial.KindVendor(vendor, netprov.IsMBIMKind(kind))
}

// hostDialNameForModem 这台模组**实际会用**哪个拨号器（按有效厂商，见 hostDialKindVendor）。
// 日志 / 提示文案一律用它，否则 MBIM 模组会被说成在跑 fibocom-dial（而其实是 quectel-CM）。
func hostDialNameForModem(mod *Modem) string {
	if mod == nil {
		return ""
	}
	return hostDialNameFor(hostDialKindVendor(hostDialKind(mod, nil), mod.Vendor))
}

// hostDialModeLabel 给用户看的类型名（"QMI" / "MBIM"），用在"数据承载归主机侧拨号器"那类提示里。
func hostDialModeLabel(kind string) string {
	if netprov.IsMBIMKind(kind) {
		return "MBIM"
	}
	return "QMI"
}

// hostDialModeLabelOf 同上，直接从模组取（拿不到类型就写"主机侧拨号"）。
func hostDialModeLabelOf(mod *Modem) string {
	if strings.TrimSpace(hostDialKind(mod, nil)) == "" {
		return "主机侧拨号"
	}
	return hostDialModeLabel(hostDialKind(mod, nil))
}

// hostDialHintFor 拨号器找不到时的补救提示（写进页面/日志，告诉用户怎么办）
func hostDialHintFor(vendor ModemVendor) string { return hostDialSpecFor(vendor).Hint }

// hostDialBinaryFor 找拨号器可执行文件（配置优先，其次按厂商的候选路径）
func hostDialBinaryFor(vendor ModemVendor) (string, error) {
	if p := strings.TrimSpace(config.Cfg.Net.HostDialBin); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("配置 [net] host_dial_bin=%s 不可用：%v", p, err)
		}
		return p, nil
	}
	paths := hostDialBinPathsFor(vendor)
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("没找到 %s（找过 %s）—— %s",
		hostDialNameFor(vendor), strings.Join(paths, "、"), hostDialHintFor(vendor))
}

// hostDialRouteFix 主机侧拨号把默认路由装上来之后的"收尾"（对齐 metric）。
// 平台差异挡在这里：Linux 上真的去改路由（见 hostdial_posix.go），其它平台是 nil。
var hostDialRouteFix func(id int, dev string)

// hostDialRawIPFix 拨号前把数据网卡切到 raw IP 模式（SIMCom 要，见 hostdial_posix.go）。
// 同样是平台注入：Linux 上写 /sys/class/net/<dev>/qmi/raw_ip，其它平台是 nil。
// 返回错误表示**没切成功**——调用方必须放弃这一轮（没切成 = 拨号器白跑，见 hostDialPreStart）。
var hostDialRawIPFix func(dev string) error

// hostDialFlushNetdev 拨号器停了以后清掉它留在网卡上的地址与默认路由
// （不然后面路由/地址还挂着，流量继续往一张已经没人管的网卡上走）。Linux 上真做，其它平台 nil。
var hostDialFlushNetdev func(dev string)

// hostDialDevUpFix 把数据网卡从"管理 down"拉回起来（`ip link set <dev> up`）。
//
// 为什么要这一步：主机侧拨号的模组，网卡被 netifd 因为"链路丢失"拉下来之后，
// 驱动就不再上报 link_state —— 载波永远不会回来，接口就一直卡在 down。
// 接口配置里已经开了 force_link（见 netprov_engine.go），这里再兜一道：
// 防止配置没写进去（老接口 / 用户手改）时链路上不来。Linux 上真做，其它平台 nil。
// 返回 true = 确实拉了一次（调用方据此记日志，别每轮都刷）。
var hostDialDevUpFix func(dev string) (bool, error)

// hostDialHasCarrier 数据网卡现在有载波吗。
//
// 为什么需要它：QMI 掉线时网卡上**很可能还留着上一次的地址**（旧租约不会被自己清掉），
// 只看“有没有地址”会把掉线当成正常（实测 2026-10-09：手动把 wwan0 down 后地址还在，
// 自愈就不触发了）。Linux 上读 /sys/class/net/<dev>/carrier；其它平台为 nil
// （= 判不出来，按“有载波”处理，不因此去重开拨号器）。
var hostDialHasCarrier func(dev string) bool

// hostDialQMAPModeOn 驱动是不是按 QMAP 复用在工作（读 /sys/class/net/<dev>/qmap_mode == 1）。
//
// 为什么拨号器要看这个（实测 2026-10-09 FM160-CN / 厂家 qmi_wwan_f，qmap_mode=1）：
//
//	不带 `-n`  → 数据呼叫在模组侧建起来了（日志有 `requestSetupDataCall
//	             WdsConnectionIPv4Handle: 0x…`），但拨号器**从不 add_mux**；
//	             父口 wwan0 于是永远 `NO-CARRIER`、`link_state=0x0`，netifd 的
//	             udhcpc 一直 “Network is down”，modem0 永远没地址。
//	带 `-n 1`  → 立刻 `usbnet_link_change :link:1` / `if_link_up wwan0` 并读回
//	             `IPv4 Address: 10.34.218.155/29`（中国电信 LTE）。
//
// 而这个 `qmap_mode` 是**只读**的（`echo 0 > qmap_mode` 得到 Permission denied，
// 由驱动在模组枚举时按模组侧配置定死），所以只能顺着它，给拨号器补上 PDN。
// 非 Linux 上为 nil（= 当作不是 QMAP，行为与改动前一致）。
var hostDialQMAPModeOn func(dev string) bool
