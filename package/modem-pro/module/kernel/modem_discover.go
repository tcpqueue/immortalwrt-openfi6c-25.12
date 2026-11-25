package kernel

import (
	"modemd/module/atport"
	"modemd/module/config"
	"modemd/module/netdev"
	"strings"
	"time"

	"go.uber.org/zap"
)

// DiscoverModems 扫描并识别所有模组（增量协调）。
// 核心策略：按 USB 物理设备分组 + AT 探测确认。
//  1. 扫描全部串口并按编号排序
//  2. 按 USB 设备实例分组：同一个 USB 设备下的所有串口 = 同一个模组
//     （模组占 2/4/6 口都能正确对应，不依赖固定端口数）
//  3. 每组内逐个发送 AT 探测，找出该模组的控制口（AT 主串口）
//  4. 增量协调：已存在的模组保持运行状态，只新增/关闭变化的模组
//     （避免周期性扫描反复关闭重启已运行的模组）
func (mgr *ModemManager) DiscoverModems() error { return mgr.discoverModems(false) }

// discoverModems 是 DiscoverModems 的实现。force=true 时先清掉「非 AT 口」否定缓存强制重探
// （USB/PCIe 层有插拔/补驱动变化时也会自动走这条）。
//
// 分三段，目的都是**别让慢操作占着 mgr.mu**（否则读接口会被堵住，见 listModems 的注释）：
//  1. 锁外：补驱动 + 枚举串口 + 分组（modprobe / 写 new_id / 补完驱动 sleep 400ms / 扫串口，全慢）；
//  2. 持锁：读否定缓存与已知模组、逐口探测、协调模组；
//  3. 提交后重建读快照（publishModemsLocked）。
//
// 发现流程本身用 discMu 单飞，所以发现状态字段只有一个写者。
func (mgr *ModemManager) discoverModems(force bool) error {
	// 单飞：同一时刻只允许一轮发现（周期巡检 / 手动重探 / 启动发现可能并发触发）。
	mgr.discMu.Lock()
	defer mgr.discMu.Unlock()

	// ⚠️ 先过一遍 USB 层（**锁外**做，它是慢操作）：
	//  · 记“哪台模组插了 / 拔了”的事件（设备级：即使它一个串口都没交出来也能看见）；
	//  · 已知模组插上了却没有串口（OpenWrt 上很常见）→ 补 usb-serial 驱动把 ttyUSB 变出来。
	// 必须放在扫串口之前，否则这一轮还是看不到那个设备的串口。
	//
	// USB 层有插拔 / 补驱动变化 ⇒ 否定缓存作废、重新探测。
	// ⚠️ 不能只靠"串口集合指纹"，因为**重插同一台模组时端口号可能完全没变**，
	// 指纹看不出变化 —— 那样旧结论（含退避计划）会被沿用，模块迟迟认不出来。
	if ensureModemUSBSerials() {
		force = true
	}
	// PCIe 模组同样要先保证驱动/内核认领了设备：没绑 mhi_pci_generic / mtk_t7xx
	// 就不会有 /dev/mhi_* 与 wwan 口，也就没有 AT 可探（对应 USB 那边补 ttyUSB 驱动）。
	if netdev.EnsurePCIeModemDrivers() {
		force = true
	}

	allPorts, err := netdev.ScanTTYUSB()
	if err != nil {
		logger.Error("scan serial ports failed", zap.Error(err))
		return err
	}
	// 按 USB 物理设备分组（同一模组的所有串口归一组）
	groups := groupPorts(allPorts)

	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	if force {
		mgr.nonATPorts = nil // 强制重探：清掉否定缓存（下面按需重建）
	}

	if len(allPorts) == 0 {
		// 无串口：关闭所有现有模组（设备全部拔出）
		for _, mod := range mgr.modems {
			mgr.noteModemGone(mod, "no serial ports")
			mod.Close()
		}
		mgr.modems = make(map[int]*Modem)
		mgr.publishModemsLocked()
		mgr.noModemSince = time.Time{} // 没插模组，不算"认不出来"
		return nil
	}

	// 串口集合指纹：只有拓扑变化时才重新探测 / 打印日志。
	// 非 AT 口（本机如 COM3/COM15/COM17）每轮探测要等应答超时（实测约 4s），
	// 之前每轮都会重探一遍，导致后台几乎一直在探测、日志不断刷。
	now := time.Now()
	fingerprint := strings.Join(allPorts, ",")
	if fingerprint != mgr.portFingerprint {
		logger.Info("serial ports grouped by USB device",
			zap.Int("group_count", len(groups)),
			zap.Any("groups", groups))
		// 拓扑变了：清空否定缓存，重新探测（可能是新插入的设备复用了旧端口号）
		mgr.nonATPorts = make(map[string]nonATRecord)
		mgr.portFingerprint = fingerprint
	}
	if mgr.nonATPorts == nil {
		mgr.nonATPorts = make(map[string]nonATRecord)
	}
	// ⚠️ 一台模组都没认出来时，别信"非 AT 口"的否定缓存：
	// 实测重启后 modemd 可能比模组先就绪（option 刚 new_id 绑上、AT 口还没应答），
	// 第一轮探测全失败会被缓存成"非 AT 口"，之后每轮都跳过 → 永远认不出模组。
	// 所以这种情况下每隔 nonATRetry 秒清一次缓存、重探一遍。
	if shouldReProbeNonAT(len(mgr.modems), mgr.nonATAt, now) {
		logger.Debug("no modem found, re-probing previously rejected ports")
		mgr.nonATPorts = make(map[string]nonATRecord)
	}

	// 已存在模组占用的 AT 口：跳过探测（它们已被占用且已知有效）
	knownAT := make(map[string]bool)
	for _, mod := range mgr.modems {
		knownAT[mod.ATPort] = true
	}

	// 找出当前所有有效 AT 口（跳过已知模组的串口，避免探测被占用端口失败）
	var atPorts []string
	for _, group := range groups {
		// ⓪ 用户按 **USB 设备（VID:PID）** 绑定的命令口：**直接采信，不再扫描/探测**。
		//
		// 用户 2026-10-08 要的就是这个：自动探测只认"对 AT 回 OK"，挑错口时指令一条都跑不出去、
		// 连 IMEI/型号都读不出来（那时按模组身份绑定的那套也用不了）⇒ 只能按设备身份人工钉死。
		// 绑定了就以它为准（优先于下面①按模组身份的指定）；绑的口不在这台设备的串口里
		// （换了固件/USB 模式把口名变了）⇒ 记一条日志、退回自动探测，不把用户锁死在错口上。
		if port, why := atport.BoundATPortForGroup(group); port != "" {
			atPorts = append(atPorts, port)
			continue
		} else if why != "" {
			logger.Debug("at port bind not applicable", zap.String("why", why))
		}
		// ① 用户手动指定过命令口 ⇒ 以它为准，**但它自己必须先答上 AT**：
		//
		// ⚠️ 为什么还要验一次：万一口不对（换了固件/USB 模式后口变了），把它当命令口用 =
		// 这台模组直接认不出来（界面上连下拉都没了，用户再也切不回去）。验不过就清掉记录、
		// 退回自动探测 —— 顺手把这条失效的指定也删了，不留后患。
		//
		// ⚠️ 键是"模组身份"（IMEI 优先），所以要等启动探测把 IMEI 读出来之后才认得出这台模组：
		// 认出来之前这一轮先按自动探测走，下一次巡检（1~2 秒）就切过去了。
		// ⚠️ 键：身份读得出来用身份键（IMEI 优先）；**读不出身份（未识别模组）用"串口组"兜底键** ——
		// 否则这类模组根本存不下"我要用哪个口"，而它们恰恰是最需要人工指定的（自动探测挑错口 ⇒
		// 指令跑不出去 ⇒ IMEI/型号全读不出来）。
		// ⚠️ 身份键要等启动探测把 IMEI 读出来之后才认得出这台模组：认出来之前这一轮先按自动探测走，
		// 下一次巡检（1~2 秒）就切过去了。
		if owner := groupOwner(mgr.modems, group); owner != nil {
			want, key := savedATPortKeyed(owner)
			if want != "" && contains(group, want) && want != owner.ATPort {
				if !atPortProbeAllowed(want) {
					// 这一分钟内刚验过：本轮先按自动探测（下轮再试）
				} else if ok, _ := netdev.ProbeATTimed(want); ok {
					logger.Info("at port override applied",
						zap.Int("modem", owner.ID), zap.String("port", want),
						zap.String("auto_detected", owner.ATPort))
					atPorts = append(atPorts, want)
					continue
				} else if key == ATPortKey(owner) {
					logger.Warn("saved at port does not answer AT, drop the override",
						zap.String("port", want), zap.String("auto_detected", owner.ATPort))
					atPortStoreInst.Set(key, "")
				} else {
					// ⚠️ 未识别模组（没有身份键，靠"串口组"兜底键存的）**不能删**：
					// 口不应答可能只是模组还在启动，删了用户就白选一次（这类模组本来就靠人指定）。
					// 本轮按自动探测走，下一分钟再验它一次。
					logger.Warn("saved at port does not answer AT, keep the override (modem unidentified)",
						zap.String("port", want), zap.String("auto_detected", owner.ATPort))
				}
			}
		}
		var cands []ATCandidate // 本组候选口的证据（见 PickATPort）
		probed := false         // 本组这轮是否真的做过探测
		for _, port := range group {
			if knownAT[port] {
				cands = append(cands, ATCandidate{Port: port, OK: true, Identity: true})
				break
			}
			// 之前判过"不是 AT 口"：退避期内跳过探测（到点会自动重探 ——
			// 慢启动的模组第一轮会超时，不能永久写死，见 nonATBackoff）
			if mgr.skipNonAT(port, now) {
				continue
			}
			// 设备刚枚举出来（模组跟着路由器断电重启、或**拔了再插**）：**先探一次**。
			// 已就绪的模组能立刻发现；要是模组内部 Linux 还没把 AT 拉起来，
			// 下面失败时会按 warmupProbeEvery 继续短间隔重探（不会干等到预热结束）。
			// 注：已认出的模组走上面的 knownAT 分支，不受预热影响。
			warmupEnd := time.Time{}
			if d, ok := netdev.UsbDeviceAge(port); ok && d < config.Cfg.Serial.ProbeWarmup {
				warmupEnd = time.Now().Add(config.Cfg.Serial.ProbeWarmup - d)
			}
			probed = true
			ok, err := netdev.ProbeATTimed(port)
			if err != nil {
				// 打开/读写失败多为瞬时问题（被占用等），不写入否定缓存，下轮重试
				logger.Debug("probe serial port failed",
					zap.String("port", port), zap.Error(err))
				continue
			}
			if ok {
				delete(mgr.nonATPorts, port) // 之前否定过、现在能应答了：清掉
				// ⚠️ "回了 OK"只能当必要条件：再发一条身份指令确认它是**能执行指令**的命令口。
				// 实测 MBIM 下 /dev/ttyUSB1 会"回一下 OK"但发真指令全超时（用户看到的就是"控制不了"）。
				c := ATCandidate{Port: port, OK: true, Identity: netdev.ProbeATIdentity(port)}
				cands = append(cands, c)
				if c.Identity {
					break // 拿到"真命令口"了，后面的口不必再探
				}
				continue
			}
			// 能打开但没应答 AT：别永久写死 —— 可能是**还在启动**的模组。
			// 年轻期（预热内）固定短间隔重探；出了预热期按端口指数退避
			// （10s/20s/40s… 封顶 5 分钟），真正没应答的口不用频繁打扰。
			rec := mgr.nonATPorts[port]
			failAt := time.Now()
			if warmupEnd.After(failAt) {
				rec.Tries = 0 // 年轻期用固定间隔，出了预热再从退避基值开始数
			} else {
				rec.Tries++
			}
			rec.Next = nonATNextProbe(rec.Tries, failAt, warmupEnd)
			// 展锐直连快路（nr2/nr3）：**不许进长退避**。
			//
			// 真机实测（2026-10-05，F50）：nr2 在开机头几分钟不应答，于是进了
			// 10s/20s/…/5min 的退避；等它其实已经能用了（手工 `AT+CGMM` 秒回
			// V1.0.1-B7，uptime 332 s）时，退避正好把它压到 5 分钟才重试一次 ⇒
			// 明明守着 10 ms 的快路，却一直走 1 s 的代理口。固定间隔重探即可。
			if netdev.IsMu300DirectATDevice(port) {
				rec.Next = netdev.Mu300DirectRetryNext(failAt)
			}
			mgr.nonATPorts[port] = rec
			mgr.nonATAt = failAt
		}
		atPort := PickATPort(cands)
		if atPort == "" {
			// 只有本轮真探测过才打印：否则每秒一次巡检会把日志刷满
			if probed {
				logger.Debug("group has no AT-capable port, skip",
					zap.Strings("group", group))
			}
			continue
		}
		if len(cands) > 1 {
			logger.Debug("multiple AT-capable ports in one group, picked",
				zap.String("picked", atPort), zap.Int("candidates", len(cands)))
		}
		atPorts = append(atPorts, atPort)
	}

	// "一个 AT 口都找不到"开始计时；久了（usbRescueAfter）就猜模组固件卡死 → USB 软重插一次。
	// 真机背景：模组冷启动后卡死时，USB 枚举正常、tty 也在、没人占口，就是 AT 不应答；
	// 写一次 authorized 0→1（真重新枚举）能救回来 —— 重启路由器是没用的（不断模组的电）。
	if len(atPorts) == 0 {
		if mgr.noATSince.IsZero() {
			mgr.noATSince = time.Now()
		}
		// 先试 USB 软重插（authorized 0→1）；场上没有 USB 模组时试 PCIe 软复位
		// （remove + rescan）—— 两条路都是"不断模组电、只重做枚举"，够安全。
		if !mgr.rescueStuckModemUSB() {
			mgr.rescueStuckModemPCIe()
		}
	} else {
		// AT 口回来了：计时/计数清零，下次卡死重新开始数
		mgr.noATSince = time.Time{}
		mgr.usbRescueCount = 0
		mgr.usbRescueGaveUp = false
	}

	// 用 AT 口做 key 协调模组状态
	newModems := make(map[int]*Modem)
	nextID := 0
	usedIDs := make(map[string]bool) // atPort -> 已占用

	// 先保留旧模组：其 AT 口仍在时，保留原对象（不重启）
	for id, mod := range mgr.modems {
		if contains(atPorts, mod.ATPort) {
			newModems[id] = mod
			usedIDs[mod.ATPort] = true
			if id+1 > nextID {
				nextID = id + 1
			}
		} else {
			mgr.noteModemGone(mod, "at port gone")
			mod.Close()
		}
	}

	// 新增的 AT 口创建新模组
	for _, atPort := range atPorts {
		if usedIDs[atPort] {
			continue
		}
		// 找到该 AT 口所属的分组
		var ttyList []string
		for _, group := range groups {
			if contains(group, atPort) {
				ttyList = group
				break
			}
		}
		mod := &Modem{
			ID:           nextID,
			TTYList:      ttyList,
			ATPort:       atPort,
			Vendor:       VendorUnknown,
			VendorName:   VendorLabel(VendorUnknown),
			readChan:     make(chan string, 256),
			writeChan:    make(chan string, 32),
			closeChan:    make(chan struct{}),
			lastReadTime: time.Now(),
			lastReadTick: time.Now(),
			isRunning:    false,
			smsSendQueue: make(chan SMSJob, config.Cfg.SMS.SendQueueSize),
			cmdImpl:      NewVendorCommand(VendorUnknown),
		}
		newModems[nextID] = mod
		usedIDs[atPort] = true
		logger.Info("found modem",
			zap.Int("id", nextID),
			zap.Strings("ports", ttyList),
			zap.String("at_port", atPort))
		nextID++
	}

	mgr.modems = newModems
	mgr.publishModemsLocked() // 重建无锁读快照（读接口据此更新模组列表）
	mgr.warnIfNoModem(allPorts)
	return nil
}

// noModemWarnAfter 有串口却认不出模组多久后开始提醒。
// 2 分钟：模组跟着路由器重启时，内部 Linux 启动 + 预热等待差不多就是这个量级，
// 少于这个时间就告警会变成误报。
const noModemWarnAfter = 2 * time.Minute

// noModemWarnEvery 重复提醒间隔（模组一直回不来时别刷屏）。
const noModemWarnEvery = 5 * time.Minute

// warnIfNoModem 长时间"有串口但认不出模组"时给一条带处置建议的告警。
//
// 真机实测：重启路由器后模组 AT 口只答了第一条指令就再也不回，日志里只有零散的
// "AT command timeout"，用户根本不知道该干什么（当时的现象是拨号失败 + 界面无模组）。
// 这里把"我看到了什么"和"你该怎么办"一起写进去。
func (mgr *ModemManager) warnIfNoModem(ports []string) {
	now := time.Now()
	if len(mgr.modems) > 0 {
		if !mgr.noModemSince.IsZero() {
			logger.Info("modem detection recovered",
				zap.Int("modems", len(mgr.modems)),
				zap.Duration("took", now.Sub(mgr.noModemSince)))
			mgr.noModemSince = time.Time{}
			mgr.noModemWarnAt = time.Time{}
		}
		return
	}
	if len(ports) == 0 { // 真的没有串口 = 没插模组，不告警
		mgr.noModemSince = time.Time{}
		return
	}
	if mgr.noModemSince.IsZero() {
		mgr.noModemSince = now
		return
	}
	if now.Sub(mgr.noModemSince) < noModemWarnAfter || now.Sub(mgr.noModemWarnAt) < noModemWarnEvery {
		return
	}
	mgr.noModemWarnAt = now
	logger.Warn("serial ports present but no modem detected",
		zap.Strings("ports", ports),
		zap.Duration("for", now.Sub(mgr.noModemSince)),
		zap.String("hint", "模组 AT 口无响应：上电后 1~2 分钟内属正常（内部系统在启动）；"+
			"持续不应答时程序会自动做一次 USB 软重插（authorized 0→1，等于拔插一次）；"+
			"若仍不行就把模组拔下、等几秒再插回（必须断模组的电）"))
}

// groupOwner 这一组串口属于哪台**已知**模组（按"它的 AT 口在本组里"认）。
//
// 用途：用户手动指定命令口时，要先认出"这台模组是谁"，再拿它的身份键（IMEI）去查记录。
// 取不到就返回 nil（那就不干预，按自动探测走）。
func groupOwner(modems map[int]*Modem, group []string) *Modem {
	for _, mod := range modems {
		if mod != nil && contains(group, mod.ATPort) {
			return mod
		}
	}
	return nil
}

// contains 判断字符串切片中是否包含目标
func contains(list []string, target string) bool {
	for _, s := range list {
		if s == target {
			return true
		}
	}
	return false
}

// ---- from: modem_at.go ----

// ---- from: modem_at.go ----
// SendAT 发送 AT 指令并等待应答（带超时）。
//
// 用 atMu 串行化：同一时刻只允许一条指令在途。否则两条并发指令
// （例如 API 查询与短信到达触发的读取）会在 readChan 上互相串台，
// 导致某条指令解析到另一条指令的应答。
// SendAT 发送 AT 指令并等待应答（带超时）。
//
// 用 atMu 串行化：同一时刻只允许一条指令在途。否则两条并发指令
// （例如 API 查询与短信到达触发的读取）会在 readChan 上互相串台，
// 导致某条指令解析到另一条指令的应答。
