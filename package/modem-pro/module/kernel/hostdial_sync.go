package kernel

import (
	"bufio"
	"fmt"
	"io"
	"modemd/module/config"
	"modemd/module/dial"
	"modemd/module/hostdial"
	"modemd/module/netdev"
	"modemd/module/netprov"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// hostDialCtxReleased 每台模组"收掉模组侧数据上下文"的记忆（源里还没收过就是没有）。
// 记着是为了别每轮自动组网都发一遍 AT 去停同一个 cid；force（手动拨号）时无视它再收一次。
var hostDialCtxReleased = struct {
	mu   sync.Mutex
	done map[int]bool
}{done: map[int]bool{}}

// releaseHostDialDataContext 主机侧拨号（QMI）时，把**模组侧的数据上下文**收掉。
//
// 为什么：模组只有**一个数据承载**。我们自己的模组侧自动拨号（AT+CGACT=1,<cid>）会先占住它，
// 主机侧拨号器随后请求数据呼叫就会被模组拒 —— 实测（RM502Q-GL + 厂家 qmi_wwan_q，2026-10-03）：
//
//	requestSetupDataCall QMUXResult = 0x1, QMUXError = 0xe
//	call_end_reason is 1 / type is 2 / verbose is 241（IPv6 那条 210/211）
//
// 同一时刻模组的 AT 侧拨号是**好的**（`AT+CGACT=1,3` 立刻拿到真实地址），
// 但驱动那边数据链路一直没起来（`dmesg: net wwan0: link_state 1 -> 0`），
// 于是接口拿不到地址 —— 页面上就是"quectel-CM 不能用"。
// 所以 QMI 这条路上模组侧**不该有我们激活的数据上下文**：只停我们自己那份（记住的 cid，
// 或进程重启后模组上还激活着的那个数据 cid），别人的上下文不碰。
//
// 自动组网循环里每台模组只收一次（免得和拨号器来回抢）；force=true 则**可重复执行** ——
// 手动点「拨号」时用（用户可能刚把同一个 cid 又激活了，见 dialByHostDialer）。
func releaseHostDialDataContext(mod *Modem, st *dial.DialState, force bool) string {
	if mod == nil {
		return ""
	}
	hostDialCtxReleased.mu.Lock()
	if hostDialCtxReleased.done[mod.ID] && !force {
		hostDialCtxReleased.mu.Unlock()
		return ""
	}
	hostDialCtxReleased.done[mod.ID] = true // 记下"已收过"（含"没找到"的情况，别留重试循环）
	hostDialCtxReleased.mu.Unlock()

	// ★【用户 2026-10-13 明确要求】拨号前的清场**只用 PDP 禁用**（`AT+CGACT=0,<cid>`），
	// **绝不用 AT+CFUN**：
	//   · `AT+CFUN=1,1`（重启模组）—— 用户实测**会把模块搞掉线**，掉线后连查都查不到；
	//   · `AT+CFUN=4`（飞行模式）—— 本机实测 AT 口不再响应，`SendAT` 卡好几分钟。
	// 只动数据承载这一层就够：秒级完成、不动射频、不碰 USB。
	//
	// 收**所有**还激活着的非内部数据上下文（不只一个）：FM160-CN 上实测会同时有
	// cid=1（公网 ctnet）与 cid=2（IMS）激活着，只收一个的话主机侧 QMI 拨号器仍可能
	// 因为"唯一的数据承载被占"而被拒（2026-10-08 真机：requestSetupDataCall 被拒）。
	cids := []int{}
	if ps, err := mod.QueryParams(); err == nil && ps != nil {
		for _, c := range ps.Contexts {
			if c.Active && !c.Internal {
				cids = append(cids, c.CID)
			}
		}
	}
	if len(cids) == 0 {
		if cid := preferredDialCID(mod.ID); cid > 0 {
			cids = append(cids, cid) // 进程里记着的那一个（可能已经停了，发一次也无害）
		}
	}
	if len(cids) == 0 {
		return ""
	}

	// 每个 cid 都重试几次：模组可能直接拒（固件还在用它），也可能回了 OK 但回读还没变
	//（停用落后半拍）。2026-10-08 真机 FM160-CN：手工 `AT+CGACT=0,1` 再重拨立刻就好，
	// 所以这一步值得多试几次（每次 ~1.5 秒），能省一次人工介入。
	var bad []string
	for _, cid := range cids {
		var lastErr error
		ok := false
		for i := 1; i <= hostDialReleaseAttempts; i++ {
			_, err := mod.SetPDPActive(cid, false, false)
			if err == nil {
				logger.Info("host dial: module-side data context released",
					zap.Int("modem", mod.ID), zap.Int("cid", cid), zap.Int("attempt", i))
				ok = true
				break
			}
			lastErr = err
			logger.Warn("host dial: release module-side data context failed, will retry",
				zap.Int("modem", mod.ID), zap.Int("cid", cid),
				zap.Int("attempt", i), zap.Int("of", hostDialReleaseAttempts), zap.Error(err))
			if i < hostDialReleaseAttempts {
				time.Sleep(hostDialReleaseGap)
			}
		}
		if !ok {
			bad = append(bad, fmt.Sprintf("cid=%d(%v)", cid, lastErr))
		}
	}
	if len(bad) > 0 {
		return "模组侧数据上下文没能全停掉（" + strings.Join(bad, "、") +
			"）—— 两边抢同一个数据承载时拨号器会被拒"
	}
	return fmt.Sprintf("已停掉模组侧数据上下文 cid=%v（QMI 的数据呼叫归主机侧拨号器，两边抢同一个承载会互相顶掉）", cids)
}

// hostDialReleaseAttempts / hostDialReleaseGap 停"模组侧数据上下文"的尝试次数与间隔。
//
// 为什么需要重试（2026-10-08 真机 FM160-CN）：模组侧 cid=1 还激活着的时，主机侧 QMI 拨号器的
// `requestSetupDataCall` 会被模组拒（日志"拨号器一直被模组拒"），于是 `wwan0` 永远拿不到地址。
// 实测：手工 `AT+CGACT=0,1` 再重拨立刻就好 —— 但 modemd 只试一次就放弃，遇上"固件还在用它"
// 或"停用落后半拍（回读还没变）"就卡住了。多试几次成本很低（每次 ~1.5 秒），却能少一次人工介入。
const (
	hostDialReleaseAttempts = 3
	hostDialReleaseGap      = 1500 * time.Millisecond
)

// hostDialKind 这台模组的 USB 类型（拨号状态里没有就问一次 USB 类型探测，带缓存）
func hostDialKind(mod *Modem, st *dial.DialState) string {
	if st != nil && strings.TrimSpace(st.Kind) != "" {
		return strings.TrimSpace(st.Kind)
	}
	if mod == nil {
		return ""
	}
	if info := mod.USBType(false); info != nil {
		return strings.TrimSpace(info.Kind)
	}
	return ""
}

// modemUsesHostDialer 这台模组当前是不是「数据呼叫归主机侧厂商拨号器」（= QMI）。
//
// QMI 模组的**数据承载只有一份**，归拨号器独占。modemd 这边只要发一条 AT+CGACT=1
// （可写参数页写 APN 后的自动激活、或手动「激活 PDP」）就会把承载占住，拨号器随后
// 请求数据呼叫被拒（实测 RM502Q-GL：QMUXError 0xe）、IPv6 那条 WDS 会话尤其起不来。
// 所以这些"改配置顺手激活"的入口遇到它必须**只写配置、不激活**，把承载留给拨号器。
func modemUsesHostDialer(mod *Modem) bool {
	if mod == nil {
		return false
	}
	return netprov.WANHostDialByDaemon(hostDialKind(mod, nil))
}

// hostDialAPN 拨号器要用的 APN：优先模组侧拨号写下去的那个；没有就现读模组里配好的
// （AT+CGDCONT? 里那条数据上下文）。都取不到就返回空（quectel-CM 会按模组配置来）。
func hostDialAPN(mod *Modem, st *dial.DialState) string {
	if st != nil {
		if apn := strings.TrimSpace(st.APN); apn != "" && ValidAPN(apn) {
			return apn
		}
	}
	if _, apn := pickAutoDialCID(mod); ValidAPN(apn) {
		return strings.TrimSpace(apn)
	}
	return ""
}

// hostDialStopReason 现在**不该**让拨号器跑的原因；返回空串表示该跑。
//
// 这里刻意不看"模组侧拨号成没成功"：QMI 的真拨号本来就归拨号器，AT 侧没连上
// （比如 auto_dial = false，或这个固件不让手动激活 PDP）不该拦住它。
func hostDialStopReason(mod *Modem, st *dial.DialState, dev string) string {
	// 数据漫游开关（「高级配置 → 参数设置」）：**国际漫游中**且开关关着 → 不让拨号器跑。
	// ⚠️ 先算好、在下面的 switch 里按位置用：这台机型是**厂商托管数据面**时
	// 该报"厂商托管"（那条 case 在前），而不是"漫游被禁"。
	// 只管国际漫游（同国异网漫游不拦，那是本地卡的正常状态），见 roaming.go。
	roamWhy := RoamBlockReason(mod)
	switch {
	case mod != nil && netdev.ManagedDataPlane(mod.ATPort):
		// SoC 内置模组（展锐 SIPC 代理 / 中兴 F50）：数据面是厂商的 netifd 接口，
		// 而 modemd 认不出它的数据网卡（会把 usb0 —— LAN 桥成员口 —— 当成模组网卡）。
		// 拉拨号器绑上去就是弄断用户的局域网，所以一律不拉。
		return "SoC 内置模组的数据面由设备厂商托管，不需要主机侧拨号器"
	case !config.DialEnabled():
		// 拨号总开关（页面「拨号上网 → 拨号开关」）：关掉后四种方式都不拨，
		// 主机侧拨号器也不能例外（它是 QMI 下唯一还活着的数据面）。
		return "拨号总开关已关闭（页面「拨号开关」）"
	case !config.Cfg.Net.HostDial:
		return "配置里关掉了（[net] host_dial = false）"
	case roamWhy != "":
		// 关的是"国际漫游时用数据"，不是"用数据"：本地网（`<stat> == 1`）与同国异网漫游时
		// 这里返回空串，开关关着也照样拨号 —— 页面上必须把这个语义写清楚，否则用户以为被断网了。
		return roamWhy
	case mod == nil || !mod.isRunningNow():
		return "模组不在了"
	case mod.AirplaneOn():
		// 飞行模式：射频都关了，数据呼叫自然也没了。
		// 这里管的是**兜底**（用户直接去 AT 页发 AT+CFUN=4、或进程重启后状态丢失），
		// 手动点开关时 SetAirplane 已经立即停过了（见 afterAirplaneChange）。
		return "飞行模式已开（射频关闭），数据链路一起停"
	case st != nil && st.ManualStop:
		return "用户手动断开了"
	case quotaBlocked(mod):
		// 配额超量断网：数据面归拨号器，断网就是把它停掉；不清零/不解除就一直不让它起
		return "配额超量、已断网（清零或「本周期不再限制」后会重新拉起）"
	case strings.TrimSpace(dev) == "":
		return "还没认出模组的数据网卡（wwanN）"
	}
	return ""
}

// hostDialNetdev 主机侧拨号器要用的两张网卡（来源见 netprov_engine.go 的 pickModemNetdev）：
//
//	Dial 给拨号器：`-i`、以及驱动属性（raw_ip）—— QMAP 下是**父口**（wwan0）
//	Data 给内核/看门狗：地址、默认路由 metric 收尾、flush —— QMAP 下是 **mux**（wwan0_1）
//
// 普通模组（ECM/NCM/RNDIS、SIMCom 的 wwan0）两张是同一个名字，传一样即可。
type hostDialNetdev struct {
	Dial string
	Data string
}

// SyncHostDial 每轮自动组网调一次：按当前状态把主机侧拨号器拉起 / 停掉（幂等）。
//
// 返回两句给页面与日志看：第一句是"干了什么"，第二句非空表示出错。
func SyncHostDial(mod *Modem, st *dial.DialState, nd hostDialNetdev) (note string, errMsg string) {
	if mod == nil || !AutoWANEnabled() {
		return "", ""
	}
	id := mod.ID
	dev, dataDev := nd.Dial, nd.Data
	if strings.TrimSpace(dataDev) == "" {
		dataDev = dev // 没单独给数据网卡就按同一张处理（普通模组就是这样）
	}

	if !netprov.WANHostDialByDaemon(hostDialKind(mod, st)) {
		stopHostDial(id, "这台模组不用主机侧拨号器")
		return "", ""
	}
	if reason := hostDialStopReason(mod, st, dev); reason != "" {
		return stopHostDial(id, reason), ""
	}

	// 拨号器按**有效厂商**挑：MBIM 一律用 quectel-CM（只有它实现了 MBIM，见 hostDialKindVendor）
	effVendor := hostDialKindVendor(hostDialKind(mod, st), mod.Vendor)
	bin, err := hostDialBinaryFor(effVendor)
	if err != nil {
		stopHostDial(id, "找不到拨号器")
		return "", err.Error()
	}
	// PCIe（MHI / wwan）模组**不传 `-i`**：MHI 的网卡名各家驱动不一样
	// （wwan0 / mhi_wwan0 / rmnet_mhi0），而 quectel-CM 的 `-i` 是**筛选**用的 ——
	// 名字对不上它就在遍历里 continue 掉，最后报 "network interface '' or qmidev '' is not exist"，
	// 一台设备都认不出来（源码：third_party/quectel-cm/device.c 的 mhidevice_detect /
	// qmidevice_detect 里都有 `if (usbnet_adapter[0] && strcmp(...)) continue;`）。
	// 不传时它自己会扫 /sys/bus/mhi/devices 认出 IP_HW0 网卡与 QMI/QRTR 控制通道
	// （mhidevice_detect 就是在做这件事），所以这里给空串让它自己认。
	// ⚠️ APN `-s`、PDN `-n` 照旧传：那两个是"拨到哪条承载"，与网卡名识别无关。
	// ⚠️ `-n` 不只看数据网卡名：QMAP 驱动（qmap_mode=1）第一次拨号时 mux 口还没被
	// 建出来，名字里没有序号，要按默认承载补一个（见 hostDialPDNFor）。
	argDev := hostDialArgDev(mod, dev)
	args := hostDialArgsFor(effVendor, argDev, hostDialAPN(mod, st), hostDialWantIPv6(), hostDialPDNFor(argDev, dataDev))
	key := strings.Join(args, " ")

	hostDials.mu.Lock()
	run := hostDials.runs[id]
	running := run != nil && run.running
	curKey, lastErr := "", ""
	var exitedAt time.Time
	fastFails := 0
	if run != nil {
		curKey, lastErr = strings.Join(run.args, " "), run.lastErr
		exitedAt, fastFails = run.exitedAt, run.fastFails
	}
	hostDials.mu.Unlock()

	if running {
		if curKey == key {
			// 已经在跑，参数没变 → 只做路由收尾（metric 对齐，幂等）
			// ⚠️ 用**数据网卡**：默认路由是拨号器在 mux 上装的（QMAP 下 ≠ 拨号器父口）
			if hostDialRouteFix != nil {
				hostDialRouteFix(id, dataDev)
			}
			// 掉线自愈（用户 2026-10-09 口径）：拨号器在跑、网卡上却一直没有 IPv4 地址
			// （QMI 掉线的典型表现：carrier=0 / 网卡 DOWN、拨号器却还挂着）⇒ **停掉它，
			// 下一轮按原参数重新拉起**。tested：停→启几秒内 carrier 就回来、DHCP 拿到租约。
			// 这里只停不原地起：控制口要留给旧进程退干净（见上面 stopHostDial 那段）。
			//
			// 另外先看一眼网卡是不是被 netifd 拉到管理 down 了（链路丢失时它会这么干，
			// 而网卡一 down 驱动就不报 link_state ⇒ 载波永远回不来）—— 那就先拉起来。
			// ⚠️ 不看地址：网卡 down 了旧地址可能还挂着（实测）。
			if hostDialDevUpFix != nil && dataDev != "" {
				if up, err := hostDialDevUpFix(dataDev); err != nil {
					logger.Warn("host dial: 数据网卡被拉下来了，拉起来失败",
						zap.Int("modem", id), zap.String("dev", dataDev), zap.Error(err))
				} else if up {
					logger.Info("host dial: 数据网卡被拉下来了（netifd 链路丢失处理），已 ip link set up",
						zap.Int("modem", id), zap.String("dev", dataDev))
				}
			}
			hostDials.mu.Lock()
			started := run.started
			runNote := run.note() + hostDialStuckHint(mod, run, dataDev)
			hostDials.mu.Unlock()
			// “还活着”的判据：网卡上有 IPv4 地址 **且**（能读到载波时）有载波。
			// 只有地址、没载波 = 已经掉线（旧租约还挂着），要走自愈。
			linkOK := hostDialHasAddr(dataDev)
			noCarrier := false
			if linkOK && hostDialHasCarrier != nil && !hostDialHasCarrier(dataDev) {
				linkOK, noCarrier = false, true
			}
			if linkOK {
				resetHostDialRestart(id)
			} else {
				hostDialRestartMu.Lock()
				due, st := hostDialRestartDue(started, hostDialRestarts[id], time.Now())
				hostDialRestarts[id] = st
				hostDialRestartMu.Unlock()
				if due {
					why := "网卡上一直没有地址"
					if noCarrier {
						why = "网卡没有载波（掉线了）"
					}
					logger.Warn("host dial: 掉线自愈 —— 重启拨号器",
						zap.Int("modem", id), zap.String("dev", dataDev), zap.String("why", why),
						zap.Int("restart", st.count), zap.Int("of", hostDialRestartMax))
					return stopHostDial(id, fmt.Sprintf(
						"掉线自愈：%s（第 %d 次重开拨号器，下一轮拉起）", why, st.count)), ""
				}
			}
			return runNote, ""
		}
		// 网卡名 / APN / 双栈变了 → 先停，下一轮按新参数拉起
		// （不原地重启：控制口要留给旧进程退干净，免得两边抢 /dev/cdc-wdm0）
		return stopHostDial(id, "拨号参数变了（"+curKey+" → "+key+"），下一轮按新参数拉起"), ""
	}

	// 不在跑 → 看退避时间到了没
	if wait := hostDialBackoff(exitedAt, fastFails, time.Now()); wait > 0 {
		msg := fmt.Sprintf("%s 上次退出了（%s），%s 后再拉起",
			hostDialNameForModem(mod), orFallback(lastErr, "原因不详"), wait)
		return msg, ""
	}
	// ★【用户 2026-10-13 要求】拉起拨号器之前，先把模组“飞行模式 → AT+CFUN=1,1”复位一次。
	// 这一步是**分轮次推进**的（不阻塞自动组网循环），详见 hostDialResetStep。
	if note, skip := hostDialResetStep(mod, id, fastFails); skip {
		return note, ""
	}
	// ★【用户 2026-10-13 要求】接口刚创建完成时，停 1 秒再拨号。
	// netifd 建完接口还要下发选项、把设备 up 起来，紧接着拨容易拨不上。
	if d := waitWANInterfaceSettled(id); d > 0 {
		logger.Info("host dial: 接口刚建好，停一下再拨号",
			zap.Int("modem", id), zap.Duration("waited", d))
	}
	// SIMCom：拨号前先把数据网卡切到 raw IP 模式（模组厂商的固件做法，见 hostdial_posix.go）
	// ⚠️ 用**拨号器网卡（父口）**：raw_ip 这个驱动属性挂在父口上（主线 qmi_wwan 的 wwan0）
	if bad := hostDialPreStart(mod, dev); bad != "" {
		return "", bad
	}
	// 拉起前**再确认一次**模组侧没占着那条唯一的数据承载（force=true：每次拉起都做）。
	//
	// 为什么不能只靠 autoWANForModem 那一轮一次（`releaseHostDialDataContext(mod, st, false)`）：
	// 那个只在**本进程第一次**执行，而模组的自动连接（/ 模组被复位、换卡、重新枚举）会
	// 自己把默认 PDP 又激活起来 —— 之后就变成“拨号器一直被模组拒”
	//（实测 FM160-CN 2026-10-09：`requestSetupDataCall QMUXResult = 0x1`，
	//  `requestGetIPAddress QMUXResult = 0x1`，wwan0 永远没载波；
	//  模组此时刚被 AT+CFUN=1,1 复位过）。
	if n := releaseHostDialDataContext(mod, st, true); n != "" {
		logger.Info("host dial: 拉开前先让模组侧让出承载", zap.Int("modem", id), zap.String("note", n))
	}
	// 拉起前先把**残留的**旧拨号器 killall 掉（用户 2026-10-09："重新拨号前 要 killall 掉
	// 之前的拨号器再拨号"）。理由见 killStrayHostDials。
	killStrayHostDials(id, bin)
	if err := startHostDial(id, bin, dataDev, args); err != nil {
		return "", "拉起 " + hostDialNameForModem(mod) + " 失败：" + err.Error()
	}
	return "已拉起 " + filepath.Base(bin) + "（" + key + "）", ""
}

// killStrayHostDials 拉起之前把**残留的**拨号器进程收干净（killall 按可执行文件名）。
//
// 为什么必须先 killall（用户 2026-10-09 提的要求，真机上也是这么踩出来的）：
//
//   - 拨号器被 SIGKILL / 进程组收尾没走完时，会留下孤儿进程占着 QMI 控制口
//     （/dev/cdc-wdm0）；再拉起一个新的，两个一起抢控制口，新的那个永远拨不上；
//   - 它 popen 的 busybox udhcpc 也会留下来占着网卡上的地址（真机踩过 **41 个**
//     没人管的 udhcpc）；
//   - 模组那边也可能因此把会话留在半开状态（之后 StartNetworkInterface 一直被拒）。
//
// 只 killall **这一家**拨号器的文件名（fibocom-dial / quectel-CM / simcom-cm / meig-cm），
// 不碰别的进程、也不碰 netifd 自己的 udhcpc（那是 netifd 管的，杀了会让接口抖）。
// 没有 killall（非 busybox 系统）就当没有，静默跳过。
func killStrayHostDials(id int, bin string) {
	if !killallHostDial(bin) {
		return
	}
	logger.Info("host dial: 已 killall 掉残留的旧拨号器，重新拉起",
		zap.Int("modem", id), zap.String("name", filepath.Base(bin)))
	// 让内核把控制口 / 网卡地址释放干净，再拉起新的
	time.Sleep(hostDialKillStrayWait)
}

// 接口刚创建完成 → 拨号前要停够这么久。
//
// 【用户 2026-10-13 要求】“创建网络接口 一定要创建完成了 停 1 秒 再拨号”。
// 为什么：netifd 把接口建出来之后，还要把选项下发、把设备 up 起来；
// 紧接着拉起拨号器容易因为接口还没真就绪而拨不上。
const wanInterfaceSettleDelay = 1 * time.Second

var (
	wanCreatedMu sync.Mutex
	wanCreatedAt = map[int]time.Time{}
)

// markWANInterfaceCreated 记下“WAN 接口刚被创建”的时刻（拨号前停 1 秒用）。
func markWANInterfaceCreated(id int) {
	wanCreatedMu.Lock()
	wanCreatedAt[id] = time.Now()
	wanCreatedMu.Unlock()
}

// waitWANInterfaceSettled 接口刚建好时补足 1 秒再返回；返回实际等了多久（0 = 不用等）。
func waitWANInterfaceSettled(id int) time.Duration {
	wanCreatedMu.Lock()
	at := wanCreatedAt[id]
	wanCreatedMu.Unlock()
	if at.IsZero() {
		return 0
	}
	left := wanInterfaceSettleDelay - time.Since(at)
	if left <= 0 {
		return 0
	}
	time.Sleep(left)
	return left
}

// 拨号前复位（配置 `reset_before_dial`）：飞行模式保持多久、复位后最多等多久。
const (
	// AT+CFUN=4 之后停一下，让射频/承载真的放干净。
	// ⚠️ 实测（2026-10-13）只等 2 秒就发 AT+CFUN=1,1，模组 USB 会“断开但不回来”
	// （dmesg 里 unregister 之后没有 new device），必须手动重新枚举；给足 5 秒就稳。
	hostDialAirplaneHold = 5 * time.Second
	hostDialResetWait    = 150 * time.Second // AT+CFUN=1,1 之后最多等这么久（实测 20~90 秒回来）
	hostDialResetPoll    = 3 * time.Second   // 等 AT 口回来时，多久探一次
)

// hostDialResetState 拨号前复位的状态（一台模组一份）。
type hostDialResetState struct {
	running   bool      // 复位流程正在跑
	startedAt time.Time // 本次复位开始时刻
	doneAt    time.Time // 上次复位完成时刻（节流用）
	note      string    // 当前进度说明（循环读它写状态）
}

var (
	hostDialResetMu  sync.Mutex
	hostDialResetMap = map[int]*hostDialResetState{}
)

// hostDialResetStep 触发/查询“拨号前复位”——**连续失败够多次**才做（兜底），
// 每次拨号前的轻量清场是 PDP 释放（releaseHostDialDataContext）。
//
// 【为什么需要复位】FM160-CN 这类模组会陷入“模组侧自己占着唯一的数据承载 / 内部状态卡死”：
//   - QMI StartNetworkInterface 迟迟不回（实测 6 秒以上、拿不到 handle），wwan0 永远没地址；
//   - 地址/路由/模组侧全正常但**发不出去**（TX 计数一直涨、RX 始终为 0），
//     而且内核**不报** NETDEV WATCHDOG ⇒ TXWedge 那条自动复位路径救不了。
//
// 真机实测（H29K + FM160-CN，2026-10-13）：`AT+CFUN=4` → 停一下 → `AT+CFUN=1,1` → 再拨：
// 60 秒内拨通，连续 150 秒 0% 丢包（不复位时拨上几十秒就发不出去）。
//
// 【⚠️ 为什么整个流程必须跑在独立 goroutine】`AT+CFUN=4` 之后模组不重启、AT 口只是
// 短暂不响应，`SendAT` 会一直等到超时（实测能卡好几分钟）。把它放在自动组网循环里同步执行
// ⇒ **整个循环被卡死**：接口不再重建、拨号器永远拉不起来
// （用户 2026-10-13 实测反馈：“没有拨号啊 进程都没有看到拨号”）。
// 所以这里只做“触发 + 查状态”，真正干活的是 hostDialResetWorker（另起 goroutine）——
// 即使 AT 卡住也只卡那一个 goroutine，循环照常跑、接口照建。
//
// 【返回】note = 说明（写进状态）；skip = true 表示正在复位，这一轮别拉起拨号器。
func hostDialResetStep(mod *Modem, id int, fails int) (note string, skip bool) {
	if mod == nil || !config.Cfg.Net.ResetBeforeDial {
		return "", false
	}
	// 【兜底定位 · 用户 2026-10-13 的质疑】只在“连续失败够多次”时才重启模组。
	// AT+CFUN=1,1 是整机重启（40~90 秒不可用），每次拨号前都做不合理；
	// AT+CFUN=4 更糟（AT 口不响应 → SendAT 卡几分钟 → 循环卡死，实测踩过）。
	// 拨号前的轻量清场交给 PDP 释放（releaseHostDialDataContext，每次拉起前都做）。
	after := config.Cfg.Net.ResetBeforeDialAfterFails
	if after > 0 && fails < after {
		return "", false
	}
	cd := time.Duration(config.Cfg.Net.ResetBeforeDialCooldown) * time.Second
	now := time.Now()

	hostDialResetMu.Lock()
	st := hostDialResetMap[id]
	if st == nil {
		st = &hostDialResetState{}
		hostDialResetMap[id] = st
	}
	if st.running {
		note = st.note
		hostDialResetMu.Unlock()
		if note == "" {
			note = "拨号前复位进行中"
		}
		return note, true
	}
	if cd > 0 && !st.doneAt.IsZero() && now.Sub(st.doneAt) < cd {
		hostDialResetMu.Unlock()
		return "", false // 节流窗口内：正常拨号
	}
	st.running, st.startedAt = true, now
	st.note = "拨号前复位：已开始（飞行模式 → AT+CFUN=1,1）"
	hostDialResetMu.Unlock()

	go hostDialResetWorker(mod, id)
	return "拨号前复位：已开始（飞行模式 → AT+CFUN=1,1）", true
}

// hostDialResetWorker 拨号前复位的实际流程（独立 goroutine，绝不占用自动组网循环）。
func hostDialResetWorker(mod *Modem, id int) {
	setNote := func(s string) {
		hostDialResetMu.Lock()
		if st := hostDialResetMap[id]; st != nil {
			st.note = s
		}
		hostDialResetMu.Unlock()
	}
	defer func() {
		hostDialResetMu.Lock()
		if st := hostDialResetMap[id]; st != nil {
			st.running, st.doneAt, st.note = false, time.Now(), ""
		}
		hostDialResetMu.Unlock()
	}()

	logger.Warn("host dial: 拨号前复位开始（飞行模式 → AT+CFUN=1,1）",
		zap.Int("modem", id), zap.String("dialer", hostDialNameFor(mod.Vendor)))

	// ⚠️ **不做 AT+CFUN=4（飞行模式）**：用户 2026-10-13 的质疑是对的 ——
	// 实测这台 FM160-CN 上 `AT+CFUN=4` 之后 AT 口不再响应，`SendAT` 会一直等到超时
	// （能卡好几分钟），而清场的收益并不比“直接复位”多。所以这里只做复位本身。
	// 而“让模组让出数据承载”这件事由**轻量的 PDP 释放**负责（releaseHostDialDataContext，
	// 每次拉起拨号器前都会执行，见它自己的注释与 2026-10-08 真机教训）。

	// 复位模组（AT+CFUN=1,1）：这条命令让模组立刻重启、AT 口随之消失，
	//    ResetModule 对“没回”也是当成功处理的，所以不会卡。
	setNote("拨号前复位：已发 AT+CFUN=1,1，等模组重新枚举")
	logger.Warn("host dial: 拨号前复位 —— 已发 AT+CFUN=1,1", zap.Int("modem", id))
	if err := mod.ResetModule(); err != nil {
		logger.Warn("host dial: 复位模组失败，仍照常拨号", zap.Int("modem", id), zap.Error(err))
		setNote("复位模组失败，仍照常拨号")
		return
	}

	// ④ 等 AT 口回来
	start := time.Now()
	for time.Since(start) < hostDialResetWait {
		time.Sleep(hostDialResetPoll)
		if _, err := mod.SendAT("AT"); err == nil {
			logger.Info("host dial: 模组复位完毕，下一轮拉起拨号器",
				zap.Int("modem", id), zap.Duration("took", time.Since(start)))
			// 复位后接口要重新就绪 ⇒ 记住时刻，拨号前会停 1 秒（用户 2026-10-13 要求）
			markWANInterfaceCreated(id)
			return
		}
	}
	logger.Warn("host dial: 等模组复位回来超时，仍照常拨号",
		zap.Int("modem", id), zap.Duration("waited", time.Since(start)))
	setNote("复位后等太久（没等到 AT 口），仍照常拨号")
}

// hostDialPreStart 拉起拨号器前的前置动作；返回非空表示前置没成功。
//
// 所有走 QMI 的拨号器（quectel-CM / simcom-cm / fibocom-dial / meig-cm）承载都是
// **raw IP**：数据网卡还在 802.3 模式下，拨号器会一直报 "PS: Detached"，看着在跑、
// 其实永远拿不到地址（真机踩过：模组重新枚举后 raw_ip 被复位成 N，我们写 Y 时内核
// 返回 EBUSY，旧代码记一条 warn 就照拉不误，结果页面显示"正在跑"却十几分钟没有地址）。
// 她们自己只会写驱动私有的 `.../qmi/rawip`（高通/厂商内核才有），主线的 qmi_wwan
// 暴露的是 `.../qmi/raw_ip`，所以这一步得由我们来做。
// 前置失败就**不拉起**，直接让下一轮重试——拨号器不用陪着空转。
func hostDialPreStart(mod *Modem, dev string) string {
	if mod == nil || hostDialRawIPFix == nil {
		return ""
	}
	if err := hostDialRawIPFix(dev); err != nil {
		return fmt.Sprintf("数据网卡 %s 的 raw_ip 切不到 Y（QMI 拨号器的承载是裸 IP，切不过去它就白跑）：%v；这一轮先不拉起 %s，稍后自动重试",
			dev, err, hostDialNameForModem(mod))
	}
	return ""
}

// hostDialBackoff 还要等多久才允许再拉起（0 = 现在就可以）
func hostDialBackoff(exitedAt time.Time, fastFails int, now time.Time) time.Duration {
	if exitedAt.IsZero() {
		return 0
	}
	wait := hostDialRetryDelay
	for i := 0; i < fastFails && wait < hostDialMaxRetryDelay; i++ {
		wait *= 2
	}
	if wait > hostDialMaxRetryDelay {
		wait = hostDialMaxRetryDelay
	}
	if left := wait - now.Sub(exitedAt); left > 0 {
		return left.Round(time.Second)
	}
	return 0
}

// startHostDial 拉起拨号器（调用方已确认该跑）
func startHostDial(id int, bin, dev string, args []string) error {
	cmd := exec.Command(bin, args...)
	hostDialPrepareCmd(cmd) // 单独进程组（停的时候连 udhcpc 一起收）

	// 拨号器的输出按行转进 modemd 自己的日志
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw

	run := &hostDialRun{cmd: cmd, bin: bin, dev: dev, args: args, started: time.Now(),
		running: true, done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return err
	}
	hostDials.mu.Lock()
	hostDials.runs[id] = run
	hostDials.mu.Unlock()

	logger.Info("host dial: started", zap.Int("modem", id), zap.String("bin", bin),
		zap.Strings("args", args), zap.Int("pid", run.pid()))

	go func() {
		name := filepath.Base(bin)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 8192), 64*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			if strings.Contains(line, "QMUXError") {
				// 模组拒绝了数据呼叫（"起来就挂"以外最常见的一种"一直没有地址"）
				hostDials.mu.Lock()
				if hostDials.runs[id] == run {
					run.qmuxErr, run.qmuxErrAt = line, time.Now()
				}
				hostDials.mu.Unlock()
			}
			logger.Info("host dial: "+name, zap.Int("modem", id), zap.String("out", line))
		}
		_ = pr.Close()
	}()

	go func() {
		err := cmd.Wait()
		_ = pw.Close()
		hostDials.mu.Lock()
		if hostDials.runs[id] == run {
			run.running = false
			run.exitedAt = time.Now()
			if err != nil {
				run.lastErr = err.Error()
			} else {
				run.lastErr = "进程正常退出（退出码 0）"
			}
			if run.exitedAt.Sub(run.started) < hostDialFastFail {
				run.fastFails++
			} else {
				run.fastFails = 0
			}
		}
		hostDials.mu.Unlock()
		close(run.done)
		logger.Warn("host dial: exited", zap.Int("modem", id), zap.String("reason", run.lastErr),
			zap.Duration("ran", time.Since(run.started)))
	}()

	// 起来之后先做一次路由收尾：模组当 DHCP 服务器时地址是几秒内下来的
	time.AfterFunc(3*time.Second, func() {
		if hostDialRouteFix != nil {
			// 用**数据网卡**（QMAP 下 ≠ 拨号器 `-i` 的父口）：默认路由装在数据网卡上
			hostDialRouteFix(id, dev)
		}
	})
	return nil
}

// stopHostDial 停掉拨号器（没在跑就是空操作）。返回一句说明。
func stopHostDial(id int, why string) string {
	hostDials.mu.Lock()
	run := hostDials.runs[id]
	if run == nil || !run.running {
		hostDials.mu.Unlock()
		return ""
	}
	netdev := run.dev
	hostDials.mu.Unlock()

	hostDialTerminate(run.pid())
	logger.Info("host dial: stopping", zap.Int("modem", id), zap.String("why", why),
		zap.Int("pid", run.pid()))

	// 给它一点时间自己退；超时就把整个进程组 SIGKILL
	select {
	case <-run.done:
	case <-time.After(hostDialStopWait):
		hostDialKill(run.pid())
		select {
		case <-run.done:
		case <-time.After(hostDialStopWait):
		}
	}
	hostDials.mu.Lock()
	delete(hostDials.runs, id)
	hostDials.mu.Unlock()

	// 兵一道：万一还有同名的孤儿进程（SIGKILL 没打中 / 不是我们拉的），按文件名一并收掉。
	if killallHostDial(run.bin) {
		logger.Info("host dial: 额外收掉了同名的残留进程",
			zap.Int("modem", id), zap.String("name", filepath.Base(run.bin)))
	}

	// 收尾：拨号器留在网卡上的地址与默认路由要清掉 —— 不然那张网卡上还挂着一条默认路由，
	// 流量会继续往一个已经没人管的接口上走（真机见过：手工"断开"后 wwan0 上还留着
	// 10.x/29 + metric 20 的默认路由）。拨号器的优雅退出路径会自己 flush，但被 SIGKILL /
	// 模组被拔 这两种情况它就来不及做了，所以这里统一兜一次。
	if netdev != "" && hostDialFlushNetdev != nil {
		hostDialFlushNetdev(netdev)
	}
	return "已停掉 " + "主机侧拨号器" + "（" + why + "）"
}

// killallHostDial 按可执行文件名把残留的拨号器进程收掉；返回 true = 确实杀掉了至少一个。
//
// 只认**文件名**（fibocom-dial / quectel-CM / simcom-cm / meig-cm），不按通配符杀，
// 免得误伤别的进程；没有 killall（非 busybox 系统）或没有匹配都算正常（返回 false）。
func killallHostDial(bin string) bool {
	name := filepath.Base(strings.TrimSpace(bin))
	if name == "" || name == "." || name == "/" || name == ".exe" {
		return false
	}
	if err := exec.Command("killall", "-q", name).Run(); err != nil {
		return false
	}
	return true
}

// StopHostDials 退出前把所有拨号器收干净（main 里收到 SIGTERM/SIGINT 时调）。
//
// 为什么必须收：modemd 一退（升级 / 重启服务），留着的 quectel-CM 会一直占着
// /dev/cdc-wdm0，下一次拨号就打不开控制口了。
func StopHostDials() { StopHostDialsFor("modemd 退出") }

// StopHostDialsFor 把现在在跑的主机侧拨号器全部停掉（带一个写给日志/页面的原因）。
// 除了退出，另一个调用点是「拨号总开关」被关掉（见 SetDialEnabled）。
func StopHostDialsFor(why string) {
	hostDials.mu.Lock()
	ids := make([]int, 0, len(hostDials.runs))
	for id, run := range hostDials.runs {
		if run != nil && run.running {
			ids = append(ids, id)
		}
	}
	hostDials.mu.Unlock()
	for _, id := range ids {
		stopHostDial(id, why)
	}
}

// lastArgAfter 取 `-x value` 形式参数里 -x 后面的那个值（路由收尾要知道网卡名）
func lastArgAfter(args []string, flag string) string {
	for i := len(args) - 2; i >= 0; i-- {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

// orFallback 空串时给个兜底文案
func orFallback(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// hostDialSpecFor 该厂商的拨号器规格（程序名 / 候选路径 / 给用户看的说明）。
//
// 规格表已搬到 modemd/module/hostdial（见 module/hostdial/spec.go）：
// 「这家要用哪个拨号器、叫什么名字、为什么」都是厂商知识。
func hostDialSpecFor(vendor ModemVendor) hostdial.Spec { return hostdial.SpecFor(vendor) }
