package kernel

import (
	"errors"
	"fmt"
	"modemd/module/config"
	"modemd/module/netdev"
	"modemd/module/usbhotplug"
	"strings"
	"time"

	"go.uber.org/zap"
)

func (mod *Modem) SendAT(cmd string) ([]string, error) {
	mod.atMu.Lock()
	defer mod.atMu.Unlock()
	return mod.sendATLocked(cmd)
}

// errATTimeout 指令没在超时内收到最终应答（可能是真的没响应，也可能只是慢）。
// 单独做成哨兵错误：少数"慢指令"（如 FM650 的 AT+CGACT）要在超时后**改走别的判据**，
// 而不是一律当失败 —— 用 errors.Is 区分"超时"和"打开/写入失败"。
var errATTimeout = errors.New("AT command timeout")

// errATStopped 指令等待应答期间被「立即停止」（主动扫网的停止按钮）中断。
// 与 errATTimeout 分开：用户主动叫停不是失败，调用方要区别对待（不能当超时报错）。
var errATStopped = errors.New("AT command stopped")

// errATNoCommand 本机不提供这条指令。
//
// 厂商实现里"本机不提供该能力"是用**空串**表达的（例：展锐 UMS9620 的
// CommandServingCell / CommandNetInfo —— 实测所有小区信息指令都回 +CME ERROR: 4）。
// 若把空串直接丢进 SendAT，会真往通道里发一条空指令，然后超时报
// `AT command timeout: `（冒号后面什么都没有）—— 既白占一个往返
// （共享慢总线上就是 2 秒），又让人看不懂。所以在这里统一拦住，给出明确的哨兵错误。
var errATNoCommand = errors.New("本机不提供该指令")

// SendATTimeout 与 SendAT 相同，但可指定等待应答的超时。
//
// 给"要等网络协商、比默认 cmd_timeout 慢得多"的指令用（见 Quirk.PDPTimeout）。
// 行为与 SendAT 完全一致（同样的 drainStale + atMu 串行化），只是超时可调。
func (mod *Modem) SendATTimeout(cmd string, timeout time.Duration) ([]string, error) {
	if mod.writeChan == nil {
		return nil, fmt.Errorf("modem not started: no AT channel")
	}
	if strings.TrimSpace(cmd) == "" {
		return nil, errATNoCommand
	}
	mod.atMu.Lock()
	defer mod.atMu.Unlock()
	mod.drainStaleLocked()
	mod.writeChan <- cmd
	return mod.readResponseLockedTimeout(cmd, true, timeout)
}

// SendATStop 与 SendATTimeout 相同，但 stop 通道被关闭时立刻中断等待（返回 errATStopped）。
//
// 给"可能要等好几分钟、用户要能随时叫停"的指令用（见 netscan.go 的主动扫网）：
// 普通超时只能等下满 timeout，COPS 这类全网搜索会白占串口几分钟。
// stop 为 nil 时行为与 SendATTimeout 完全一致（那个 case 永不触发）。
func (mod *Modem) SendATStop(cmd string, timeout time.Duration, stop <-chan struct{}) ([]string, error) {
	if mod.writeChan == nil {
		return nil, fmt.Errorf("modem not started: no AT channel")
	}
	if strings.TrimSpace(cmd) == "" {
		return nil, errATNoCommand
	}
	if err := mod.lockATMuStop(stop); err != nil {
		return nil, fmt.Errorf("%w: %s", errATStopped, cmd)
	}
	defer mod.atMu.Unlock()
	// 已叫停：排残留也别等了
	if stopRequested(stop) {
		return nil, fmt.Errorf("%w: %s", errATStopped, cmd)
	}
	mod.drainStaleLockedStop(stop)
	// 再确认一次：排残留期间也可能被叫停，那就别再往模组发了
	if stopRequested(stop) {
		return nil, fmt.Errorf("%w: %s", errATStopped, cmd)
	}
	mod.writeChan <- cmd
	return mod.readResponseLockedTimeoutStop(cmd, true, timeout, stop)
}

// stopRequested 报告 stop 是否已被关闭（stop 为 nil 时永远 false）。
func stopRequested(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// lockATMuStop 取 AT 串行锁；等待期间 stop 被关闭就立刻返回 errATStopped。
//
// 为什么要它：主动扫网一开始就得先抢 atMu。若此刻串口正被别的指令占着
// （发短信会整段独占十几秒；模组忙时还会连着几条 3s 超时的周期查询在排队），
// 用户点「停止扫描」就得干等这些指令跑完 —— 体感就是"停止按钮也不灵"。
// 普通 atMu.Lock() 没法 select，所以用 TryLock 轮询（15ms 一次，代价可忽略：
// 抢不到锁时我们本来也是在空等）。
func (mod *Modem) lockATMuStop(stop <-chan struct{}) error {
	if stop == nil {
		mod.atMu.Lock()
		return nil
	}
	for {
		if mod.atMu.TryLock() {
			return nil
		}
		select {
		case <-stop:
			return errATStopped
		case <-time.After(15 * time.Millisecond):
		}
	}
}

// sendATLocked 发送并等待应答；调用方必须已持有 atMu
//
// ⚠️ 这里有三道"防错位"的保险 —— 实测广和通 FM650（两条 AT 口 + 1 秒一轮轮询）上
// 通道一旦错位就**不会自愈**：每条指令都在读**上一条**指令的应答，
// 症状是"短信读不出来 / 信号值乱跳 / 大量 AT command timeout"（实测日志里 386 次）。
//  1. 发之前先丢掉残留行（drainStaleLocked）；
//  2. 结束标记必须**整行相等**（老实现用 strings.Contains("OK")，短信正文里出现 OK
//     就会把 AT+CMGL 这种长应答提前截断，被截断的尾巴留在通道里喂给下一条指令）；
//  3. 固件开着回显时，只在**看到本指令自己的回显之后**才开始收应答 —— 这样即使残留
//     只被丢掉一半，也能自动回到同步（回显没验证过的固件不受影响，见 echoSeen）。
func (mod *Modem) sendATLocked(cmd string) ([]string, error) {
	// 没有 AT 通道（还没 Start / 已经 Stop）时直接返回：
	// 否则 `writeChan <- cmd` 会把调用方（含 HTTP 处理器、单元测试）永久挂住
	if mod.writeChan == nil {
		return nil, fmt.Errorf("modem not started: no AT channel")
	}
	// 空指令 = 厂商声明"本机不提供该能力"，别真发出去（见 errATNoCommand）
	if strings.TrimSpace(cmd) == "" {
		return nil, errATNoCommand
	}
	mod.drainStaleLocked()
	mod.writeChan <- cmd
	return mod.readResponseLocked(cmd, true)
}

// readResponseLocked 读取一条指令的应答；expectedEcho 是这条指令在模组上的回显
// （多段写入的指令比如 AT+CMGS 内容，echo 传该指令自己的写法，见 doSendSMS）。
//
// requireEcho=false 用于**本来就没有自己回显**的应答（AT+CMGS 的正文阶段：提示符已经
// 确认过同步了）—— 否则会干等一个永远不来的回显，每条短信白等一个 CmdTimeout。
func (mod *Modem) readResponseLocked(expectedEcho string, requireEcho bool) ([]string, error) {
	return mod.readResponseLockedTimeout(expectedEcho, requireEcho, config.Cfg.Serial.CmdTimeout)
}

// readResponseLockedTimeout 与 readResponseLocked 相同，但可指定超时。
//
// 为什么要单独的超时：短信提交的应答（`+CMGS: <mr>` / `OK`）要等**网络回执**，
// 实测鼎桥 MT5700M-CN 上 3 秒的 CmdTimeout 根本等不到（日志：正文发出后 3.0 秒就报 timeout）；
// 普通查询类指令用 CmdTimeout 反而要短，否则界面会发卡。
func (mod *Modem) readResponseLockedTimeout(expectedEcho string, requireEcho bool, timeout time.Duration) ([]string, error) {
	return mod.readResponseLockedTimeoutStop(expectedEcho, requireEcho, timeout, nil)
}

// readResponseLockedTimeoutStop 与 readResponseLockedTimeout 相同，额外支持"立即停止"：
// stop 被关闭时立刻返回 errATStopped（stop 为 nil 时那个 case 永不触发）。
// 已经收到的 resp 一并返回，方便调用方留作现场（如原始应答）。
func (mod *Modem) readResponseLockedTimeoutStop(expectedEcho string, requireEcho bool, timeout time.Duration, stop <-chan struct{}) ([]string, error) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	var resp, stale []string
	// synced = false 表示"还没看到本指令的回显"，这期间收到的行都属于上一条指令的残留。
	// 只在确认过固件会回显（echoSeen）之后才启用，免得对不回显的固件把应答全丢了。
	synced := !mod.echoSeen || !requireEcho
	for {
		select {
		case <-stop:
			return resp, fmt.Errorf("%w: %s", errATStopped, expectedEcho)
		case <-t.C:
			if !synced && len(stale) > 0 {
				// 一直没等到回显：这块"残留"很可能其实就是我们的应答（固件不回显 / 应答慢到超时）
				// → 关掉回显同步，把攒下的行还回去，免得把好好的应答全丢了。
				mod.echoSeen = false
				logger.Warn("AT echo never seen, disable echo sync",
					zap.Int("modem", mod.ID), zap.String("cmd", expectedEcho),
					zap.Int("lines", len(stale)),
					zap.String("first", stale[0]),
					zap.String("last", stale[len(stale)-1]))
				return stale, nil
			}
			return resp, fmt.Errorf("%w: %s", errATTimeout, expectedEcho)
		case data := <-mod.readChan:
			line := strings.TrimSpace(data)
			if expectedEcho != "" && strings.EqualFold(line, expectedEcho) {
				mod.echoSeen = true
				synced = true
				resp = append(resp, data)
				continue
			}
			if !synced {
				if len(stale) == 0 {
					logger.Debug("AT stale line dropped（等自己的回显）",
						zap.Int("modem", mod.ID), zap.String("cmd", expectedEcho), zap.String("line", data))
				}
				stale = append(stale, data)
				continue
			}
			resp = append(resp, data)
			if isATEndLine(line) {
				return resp, nil
			}
		}
	}
}

// isATEndLine 判断一行是否表示本条应答结束。
//
// ⚠️ 必须**整行相等**，不能用 strings.Contains：
//   - 短信正文里出现 "OK" 会把 AT+CMGL 的长应答提前截断（截断的尾巴污染下一条指令）；
//   - "+CME ERROR" / "+CMS ERROR" 靠 Contains("ERROR") 只是碰巧命中，这里写清楚。
func isATEndLine(line string) bool {
	u := strings.ToUpper(strings.TrimSpace(line))
	switch {
	case u == "OK", u == "ERROR":
		return true
	case strings.HasPrefix(u, "+CME ERROR"), strings.HasPrefix(u, "+CMS ERROR"):
		return true
	}
	return false
}

// drainStaleLocked 丢掉上一次指令残留的应答行。
//
// 判定"排空了"不能只看"缓冲为空"：长应答是**分批到达**的（FM650 的 AT+GTCCINFO? 有 14 行），
// 停下来时可能正好在中间 → 剩下的半块还是会被下一条指令读走。
// 所以以"最后丢掉的是一行结束标记"为整块结束，再加一次空轮兜底。
//
// 两条实测教训（鼎桥 MT5700 上踩到）：
//   - 该模组每秒主动上报 ^RSSI / ^CERSSI / ^HCSQ 好几行，**URC 不是"应答没读完"**：
//     不把它排除掉，pending 永远是 true，循环要空转到 200 次 × 20ms 才退出，
//     表现为"每条 AT 指令都白白多花 4 秒"（界面一片卡顿）；
//   - 所以再加一道时间上限兜底：URC 洪水下最多等 drainStaleMax 就返回。
func (mod *Modem) drainStaleLocked() {
	mod.drainStaleLockedStop(nil)
}

// drainStaleLockedStop 与 drainStaleLocked 相同，但 stop 被关闭时立刻返回
// （给「立即停止」用：别为了排残留再多等最多 drainStaleMax）。
// stop 为 nil 时那两个 `<-stop` 永不就绪，行为与 drainStaleLocked 完全一致。
func (mod *Modem) drainStaleLockedStop(stop <-chan struct{}) {
	pending := false
	empty := 0
	deadline := time.Now().Add(drainStaleMax)
	for i := 0; i < 200; i++ {
		if stopRequested(stop) {
			return
		}
		select {
		case line := <-mod.readChan:
			empty = 0
			if isAsyncReport(line) {
				continue // 异步上报：丢掉，但不算"还有半块应答在排队"
			}
			pending = !isATEndLine(line)
			continue
		default:
		}
		if !pending && empty >= 1 {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		empty++
		select {
		case <-stop:
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// drainStaleMax 排残留的最长等待时间（正常情况下几十毫秒就排空了）。
const drainStaleMax = 400 * time.Millisecond

// nonATRetry 一台模组都没认出来时，多久把"非 AT 口"的否定缓存作废重探一次。
// 60 秒：既能让"模组刚绑上驱动还没就绪"的情况自己恢复，又不会每轮都去探一遍（每口约 4s）。
const nonATRetry = 60 * time.Second

// 被否定的口**按端口重探**的指数退避参数（**有模组时也照样重探**）。
//
// 背景（用户反馈）：本来就插着一台模组（比如 4G），启动后另一台**慢启动的 5G 模组**
// 第一轮 AT 探测超时、被记成"非 AT 口"；旧逻辑是"场上有模组就不再清否定缓存"，
// 于是它永远不再被探测 → 5G 模组一直出不来。改成按端口退避重探后：
// 10s / 20s / 40s / 80s… 封顶 5 分钟，成本可忽略，慢模组总能等到它把 AT 拉起来。
const (
	nonATBackoffBase = 10 * time.Second
	nonATBackoffMax  = 5 * time.Minute
)

// warmupProbeEvery 设备还"年轻"（刚枚举出来 / 刚重新插上，还在 probe_warmup 里）时的重探间隔。
//
// 实测（用户反馈）：拔了再插**同一台**模组，第一轮 AT 探测常"抢跑"失败（tty 刚出现、模组还在启动）；
// 若失败后一路等到预热结束（默认 90s）才再探，用户看到的就是"好久都不出来"。
// 改成年轻期固定短间隔重探：模组一就绪，下一轮（≤ 这个间隔）就能认出来。
const warmupProbeEvery = 15 * time.Second

// nonATBackoff 连续被判否定 tries 次后，距下次重探要等多久（指数退避、封顶）。
func nonATBackoff(tries int) time.Duration {
	if tries < 1 {
		tries = 1
	}
	d := nonATBackoffBase
	for i := 1; i < tries; i++ {
		d *= 2
		if d >= nonATBackoffMax {
			return nonATBackoffMax
		}
	}
	return d
}

// skipNonAT 这个口现在要不要跳过探测（在否定缓存里、且还没到重探时刻）。
func (mgr *ModemManager) skipNonAT(port string, now time.Time) bool {
	rec, ok := mgr.nonATPorts[port]
	return ok && now.Before(rec.Next)
}

// nonATNextProbe 一次否定后距下次重探的时刻。
//
//   - 设备还"年轻"（now 还在预热期内）：固定 warmupProbeEvery 重探一次。
//     重插同一台模组时第一轮常"抢跑"失败，固定短间隔能尽快等到它就绪。
//   - 设备已"成年"：指数退避（真正没应答的口不用频繁打扰）。
//
// 调用处会在年轻期把 Tries 清零，所以出了预热期是从退避基值重新开始数的。
func nonATNextProbe(tries int, now, warmupEnd time.Time) time.Time {
	if warmupEnd.After(now) {
		next := now.Add(warmupProbeEvery)
		if warmupEnd.Before(next) {
			next = warmupEnd // 预热马上就要结束：等到那会儿再探
		}
		return next
	}
	return now.Add(nonATBackoff(tries))
}

// USB 软重插的参数：一个 AT 口都找不到这么久就动手；两次之间至少隔这么久；一个"卡死周期"里最多救这么多次。
//
// ⚠️ 为什么要等 150 秒（而不是一发现就救）：这类模组内部跑着一个 Linux，上电后要 1~2 分钟
// 才能应答 AT。真机日志里就见过一次：重启路由器后模组 AT 不应答，我们一直等到 102 秒时
// 它自己就好了（随后拨号 OK）——如果 90 秒就动手软重插，反而会把正在启动的模组**打断**，
// 而且重插后重新计时、又被打断，就成了永远起不来的死循环。
// 上限 3 次是为了兜底：真卡死时救两次就够了，救不到就说明得物理断电，不能无限折腾。
const (
	usbRescueAfter   = 150 * time.Second
	usbRescueCooling = 5 * time.Minute
	usbRescueMax     = 3
)

// shouldRescueUSB 该不该对模组做一次"USB 软重插"（authorized 0→1）。
//
// 四个条件（缺一不可）：
//
//	① 场上有**已知模组**的 USB 设备（knownDevices>0）—— 只动模组，绝不碰 U 盘/鼠标这类别的 USB 设备；
//	② 已经有一个 AT 口都找不到且持续了 usbRescueAfter（给模组内部系统留启动时间）；
//	③ 距上次软重插超过 usbRescueCooling（别把它变成重启循环）；
//	④ 这一类"卡死周期"里救的次数还没到 usbRescueMax（到了就认输，只告警）。
func shouldRescueUSB(knownDevices, rescued int, noATSince, lastRescue, now time.Time) bool {
	if knownDevices <= 0 || noATSince.IsZero() {
		return false
	}
	if rescued >= usbRescueMax {
		return false
	}
	if now.Sub(noATSince) < usbRescueAfter {
		return false
	}
	if !lastRescue.IsZero() && now.Sub(lastRescue) < usbRescueCooling {
		return false
	}
	return true
}

// rescueStuckModemUSB "AT 口一直不应答"时的自动恢复：对场上的已知模组做一次 USB 软重插。
//
// 真机背景（RK3528 + 鼎桥 MT5700M-CN）：模组**冷启动固件卡死** —— USB 枚举正常、ttyUSB 也在、
// 没人占着口，就是 AT 不应答（重启路由器没用，因为路由器重启不断模组的电）。
// 实测写一次 authorized 0→1（真重新枚举）就恢复正常。
func (mgr *ModemManager) rescueStuckModemUSB() bool {
	if !usbhotplug.USBRescueSupported() {
		return false
	}
	devs, err := usbhotplug.ListModemUSBDevices()
	if err != nil {
		return false
	}
	known := make([]usbhotplug.ModemUSBDevice, 0, len(devs))
	for _, d := range devs {
		if d.Known {
			known = append(known, d)
		}
	}
	if !shouldRescueUSB(len(known), mgr.usbRescueCount, mgr.noATSince, mgr.lastUSBRescue, time.Now()) {
		// 救到上限了：只告警一次，之后别再刷屏（AT 口回来会自己清零，下次卡死重新开始数）
		if len(known) > 0 && mgr.usbRescueCount >= usbRescueMax && !mgr.usbRescueGaveUp {
			mgr.usbRescueGaveUp = true
			logger.Warn("modem still answers nothing after several USB re-plugs: giving up automatic rescue",
				zap.Int("rescues", mgr.usbRescueCount), zap.Duration("noAT", time.Since(mgr.noATSince)),
				zap.String("hint", "模组固件真的卡死了（软重插救不回来的那种），需要把模组从 USB 口拔下、等几秒再插回——必须断它的电"))
		}
		return false
	}
	noATDur := time.Since(mgr.noATSince)
	mgr.lastUSBRescue = time.Now()
	mgr.noATSince = time.Now() // 重新计时，别连续触发
	mgr.usbRescueCount++
	mgr.nonATPorts = make(map[string]nonATRecord) // 口号会变，否定缓存作废
	for _, d := range known {
		if err := usbhotplug.ReauthorizeUSBDevice(d.BusPath); err != nil {
			logger.Warn("usb re-plug failed",
				zap.String("bus", d.BusPath), zap.String("model", d.Product), zap.Error(err))
			continue
		}
		logger.Warn("no AT port for a while: re-plugging the modem over USB (authorized 0->1)",
			zap.String("bus", d.BusPath), zap.String("model", d.Product),
			zap.Duration("noAT", noATDur), zap.Int("attempt", mgr.usbRescueCount),
			zap.String("hint", "模组冷启动后固件卡死时这样做能救回来；tty 名会变，程序会重新分组"))
	}
	return true
}

// rescueStuckModemPCIe "AT 口一直不应答"时的 PCIe 版恢复：remove → rescan（见 pcie_unix.go）。
//
// 与 USB 那条路共用同一套节流参数与计数器（usbRescueAfter / usbRescueCooling / usbRescueMax）：
// 它们描述的是"一个 AT 口都找不到"这个事件，而不是哪条总线 —— 两块模组（一块 USB、一块 PCIe）
// 同时卡死时也只能算一次救援，否则就成了互相催着的重启循环。
func (mgr *ModemManager) rescueStuckModemPCIe() bool {
	if !netdev.PCIeResetSupported() {
		return false
	}
	targets := pcieRescueTargets()
	if !shouldRescueUSB(len(targets), mgr.usbRescueCount, mgr.noATSince, mgr.lastUSBRescue, time.Now()) {
		if len(targets) > 0 && mgr.usbRescueCount >= usbRescueMax && !mgr.usbRescueGaveUp {
			mgr.usbRescueGaveUp = true
			logger.Warn("pcie modem still answers nothing after several resets: giving up automatic rescue",
				zap.Int("resets", mgr.usbRescueCount), zap.Duration("noAT", time.Since(mgr.noATSince)),
				zap.String("hint", "模组固件真的卡死了，需要断电重启（或重启路由器）"))
		}
		return false
	}
	noATDur := time.Since(mgr.noATSince)
	mgr.lastUSBRescue = time.Now()
	mgr.noATSince = time.Now() // 重新计时，别连续触发
	mgr.usbRescueCount++
	mgr.nonATPorts = make(map[string]nonATRecord) // 设备节点会重建，否定缓存作废
	for _, d := range targets {
		if err := netdev.ResetPCIDevice(d.BDF); err != nil {
			logger.Warn("pcie reset failed", zap.String("bdf", d.BDF), zap.Error(err))
			continue
		}
		logger.Warn("no AT port for a while: resetting the PCIe modem (remove + rescan)",
			zap.String("bdf", d.BDF), zap.String("driver", d.Driver),
			zap.Duration("noAT", noATDur), zap.Int("attempt", mgr.usbRescueCount),
			zap.String("hint", "PCIe 模组卡死时用 PCIe 层重新枚举救回来（不断电）；节点名会变，程序会重新分组"))
	}
	return true
}

// shouldReProbeNonAT 该不该把整份"非 AT 口"否定缓存作废、全部重探一遍。
//
// 条件：**当前一台模组都没认出来**（这时值得快一点：每 60s 全量重探一次），
// 且上次写入否定结果已超过 nonATRetry。
// ⚠️ 有模组时**不再**靠这里全量重探（那样每 60s 浪费 4s×N），改由**按端口退避**
// 各自重探（见 skipNonAT / nonATBackoff）—— 所以"场上有模组"不再等于"被否定的口
// 永远不重探"（那正是 5G 慢模组一直出不来的原因）。
func shouldReProbeNonAT(knownModems int, lastNegAt, now time.Time) bool {
	if knownModems > 0 || lastNegAt.IsZero() {
		return false
	}
	return now.Sub(lastNegAt) > nonATRetry
}

// isAsyncReport 判断一行是不是模组的**主动上报**（URC），而不是某条指令的应答。
//
// 鼎桥/Telit 系的私有上报都以 `^` 开头（实测 MT5700：^RSSI / ^CERSSI / ^HCSQ / ^SRVST），
// 标准上报里挑几条"只可能是上报、不可能是查询应答"的（+CMTI / +CMT / +CDS / +CUSD）。
// ⚠️ 故意不认 +CREG/+CGREG/+CEREG —— 它们既是上报也是 `AT+CREG?` 的应答，
// 混进"异步上报"会让排残留提前结束、把半块应答漏给下一条指令。
func isAsyncReport(line string) bool {
	u := strings.ToUpper(strings.TrimSpace(line))
	if strings.HasPrefix(u, "^") {
		return true
	}
	switch {
	case strings.HasPrefix(u, "+CMTI:"), strings.HasPrefix(u, "+CMT:"),
		strings.HasPrefix(u, "+CDS:"), strings.HasPrefix(u, "+CUSD:"):
		return true
	}
	return false
}
