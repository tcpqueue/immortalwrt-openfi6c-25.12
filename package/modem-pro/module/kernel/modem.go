package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"modemd/module/config"
	"modemd/module/netdev"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ---- from: modem_lifecycle.go ----

// ---- from: modem_lifecycle.go ----
// Start 启动模组串口读写协程
// Start 启动模组串口读写协程

func (mod *Modem) Start() error {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	if mod.isRunning {
		return nil
	}
	// tty（ttyUSB/ttyACM/COMx）走 tarm/serial，PCIe 的 MHI/wwan 字符设备走裸 open ——
	// 两条路各自带自己的读超时（运行时 1 秒，见 atdev.go）。
	port, err := netdev.OpenATDeviceRun(mod.ATPort)
	if err != nil {
		logger.Error("open serial failed",
			zap.Int("modem", mod.ID), zap.String("port", mod.ATPort), zap.Error(err))
		return err
	}
	mod.serialPort = port
	mod.closeChan = make(chan struct{})
	mod.isRunning = true
	// 重新探测厂商指令（重启/换指令集后可能不同），流量计数器基线也一并重置
	mod.probedCmds = nil
	mod.trafficPrev = nil
	mod.trafficReady = false

	go mod.readLoop()
	go mod.writeLoop()
	go mod.stuckCheckLoop()
	go mod.smsWorkerLoop()
	// 厂商 URC 日志跟随（展锐 SIPC）：把 daemon 已经在写的上报日志当第二条 URC 来源。
	// 非展锐模组这里什么都不做（见 startURCLogTail）。
	go mod.startURCLogTail()

	// 启动后按顺序完成：厂商探测 → 硬件身份（IMEI/固件）→ 短信存储配置。
	// 放在同一个 goroutine 内串行执行，避免并发 AT 指令互相串台。
	go mod.startupProbe()

	logger.Info("modem started", zap.Int("id", mod.ID), zap.String("port", mod.ATPort))
	return nil
}

// startupProbe 模组启动后的初始化探测（串行执行，避免并发占用 AT 通道）
func (mod *Modem) startupProbe() {
	if config.Cfg.Server.AutoDetectVendor {
		mod.detectVendor()
	}
	if _, err := mod.QueryIdentity(); err != nil {
		logger.Warn("query identity failed", zap.Int("modem", mod.ID), zap.Error(err))
	}
	mod.applySMSStorage()
	// 新短信上报：不显式开就只能听模组自己的值，新短信推送会静默失效
	mod.applySMSNotify()
	// 短信缓存：开机读一遍（全量替换）。屏幕/网页都只读这份缓存，
	// 不先读的话它们连"卡里到底有哪些短信"都不知道（屏幕 SMS 页会是空的）。
	mod.refreshSMSCache(0, 0)
}

// detectVendor 自动探测模组厂商与型号
func (mod *Modem) detectVendor() {
	resp, err := mod.SendAT(mod.cmdImpl.CommandModel())
	if err != nil {
		logger.Warn("detect vendor ATI fail", zap.Int("modem", mod.ID), zap.Error(err))
		return
	}
	// 必须遍历全部厂商识别：初始指令实现是兜底实现，
	// 它自己只认得 VendorUnknown，用它判断永远换不了指令集。
	vendor := DetectVendorFromATI(resp)
	raw := strings.Join(resp, "|")
	// ⚠️ 有的模组**根本不支持 ATI**（实测：中兴 F50 / 展锐 UMS9620 直接回
	// `+CME ERROR: 4`）。这时 `raw` 只是一行错误码，既认不出厂商，界面上
	// "型号"一栏也毫无意义（显示成 "+CME ERROR: 4"）。用 3GPP 标准指令兜底：
	// AT+CGMR（固件/硬件版本）优先，再退到 AT+CGMM（型号）。两者展锐都有应答，
	// 且对别的厂商也安全 —— 只在"ATI 明确报错"时才走这条路。
	if vendor == VendorUnknown && atRespIsError(resp) {
		for _, alt := range []string{"AT+CGMR", "AT+CGMM"} {
			lines := mod.queryIdentityLines(alt)
			if len(lines) == 0 {
				continue
			}
			// 关键：用兜底应答**重跑一遍厂商识别** —— 展锐就是靠 AT+CGMR 里的
			// `ums9620_modem` 认出来的，ATI 那条路（上面）根本没有它的线索。
			if v := DetectVendorFromATI(lines); v != VendorUnknown {
				vendor, raw = v, strings.Join(lines, " | ")
				break
			}
			if raw == "" || atRespIsError(strings.Split(raw, "|")) {
				raw = strings.Join(lines, " | ")
			}
		}
	}
	modelName := shortModelName(raw)
	mod.mu.Lock()
	mod.Vendor = vendor
	mod.VendorName = VendorLabel(vendor)
	mod.cmdImpl = NewVendorCommand(vendor)
	mod.Model = raw           // ATI 原文（机型特例匹配 / 排查用）
	mod.ModelName = modelName // 界面显示的型号短名（如 MT5700M-CN）
	mod.probedCmds = nil      // 指令集变了，重新探测
	mod.lockProbed = false    // 锁网方言也要重新探测
	mod.lockChosen = nil
	mod.lockFamily = "" // 方言命中的名字与探测记录一并清掉（否则界面上会显示上一次的结果）
	mod.lockTried = nil
	// 组网模式的"设备枚举"也要重来：换了固件/厂商后 `AT+XXX=?` 支持的取值可能不同
	mod.modeEnumDone = false
	mod.modeEnum = nil
	mod.mu.Unlock()
	logger.Info("detect modem vendor success",
		zap.Int("modem", mod.ID),
		zap.String("vendor", string(vendor)),
		zap.String("model", modelName),
		zap.String("ati", raw))
}

// atRespIsError 应答里"只有错误行"（没有一条像样的数据）⇒ 这条指令不被支持。
// 用于 ATI：展锐 UMS9620 回 `+CME ERROR: 4`，别的厂商正常回 ATI 原文。
func atRespIsError(resp []string) bool {
	seen := false
	for _, l := range resp {
		l = strings.TrimSpace(l)
		if l == "" || strings.EqualFold(l, "OK") {
			continue
		}
		seen = true
		if !strings.Contains(strings.ToUpper(l), "ERROR") {
			return false
		}
	}
	return seen
}

// queryIdentityLines 发一条查询并返回去噪后的应答行（给"ATI 不支持"的模组兜底识别用）。
// 失败或空应答返回 nil，调用方自己退到下一条候选。
func (mod *Modem) queryIdentityLines(cmd string) []string {
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range resp {
		l = strings.TrimSpace(l)
		if l == "" || strings.EqualFold(l, "OK") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// readLoop 读协程：独立接收串口上报，解析信号与短信通知。
// 使用裸 Read + 手动行缓冲，确保每次 ReadTimeout 超时都返回并更新心跳，
// 避免 bufio 缓冲导致的无限阻塞（tarm/serial 在 Windows 上的已知行为）。
func (mod *Modem) readLoop() {
	var lineBuf []byte
	buf := make([]byte, 256)
	for {
		select {
		case <-mod.closeChan:
			return
		default:
		}

		n, err := mod.serialPort.Read(buf)
		// 心跳：每次读返回即更新，表明读循环活着（即使超时无数据）
		mod.mu.Lock()
		mod.lastReadTick = time.Now()
		mod.mu.Unlock()

		if n > 0 {
			lineBuf = append(lineBuf, buf[:n]...)
			// 按行处理
			lines, rest := splitATLines(lineBuf)
			lineBuf = rest
			for _, line := range lines {
				mod.mu.Lock()
				mod.lastReadTime = time.Now()
				mod.mu.Unlock()
				mod.readChan <- line
				// parseMu：parseLine 另有 URC 日志跟随协程这一个调用方（见 urclog.go），
				// 而它里面有**不带锁**的小状态机（如 GTCCINFO 的 gtccRat）—— 必须互斥。
				mod.parseMu.Lock()
				mod.parseLine(line)
				mod.parseMu.Unlock()
			}
			// 防止无换行的长数据无限累积
			if len(lineBuf) > 8192 {
				lineBuf = lineBuf[len(lineBuf)-512:]
			}
		}

		if err != nil {
			// 读错误/超时：正常情况，短暂休息后继续。
			//
			// ⚠️ 这个休息时长就是“应答的额外延迟”：展锐直连通道（nr2）是 O_NONBLOCK 裸设备，
			// 没数据时立刻回 EAGAIN，只能睡一会儿再看 —— 用 100 ms 会把 ~10 ms 的快路
			// 拖成 ~110 ms，所以直连通道单独一档（见 readIdleDelay）。
			d := netdev.ReadIdleDelay(mod.ATPort)
			select {
			case <-mod.closeChan:
				return
			case <-time.After(d):
			}
		}
	}
}

// splitATLines 从读缓冲里切出可上报的行，返回剩余缓冲。
//
// 两个要点：
//  1. 常规：按 `\n` 切、去首尾空白、丢掉空行；
//  2. ⚠️ `>` 提示符（AT+CMGS / AT+CMGL 等输入态）**多数固件不带换行**，按行切不出来
//     —— 它会一直留在缓冲里，让“等提示符”的实现和模组互相干等
//     （实测 FM350：`AT+CMGS` 之后永远等不到 `>`，最后模组一直等正文，
//     后续每条 AT 指令都被当正文吞掉）。所以缓冲里**只剩一个 `>`** 时立即单独上报。
func splitATLines(buf []byte) (lines []string, rest []byte) {
	for {
		idx := indexByte(buf, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSpace(string(buf[:idx]))
		buf = buf[idx+1:]
		if line != "" {
			lines = append(lines, line)
		}
	}
	if strings.TrimSpace(string(buf)) == ">" {
		lines = append(lines, ">")
		buf = buf[:0]
	}
	return lines, buf
}

// indexByte 在字节切片中查找目标字节，返回下标；未找到返回 -1
func indexByte(b []byte, target byte) int {
	for i, c := range b {
		if c == target {
			return i
		}
	}
	return -1
}

// writeLoop 写协程：独立发送 AT 指令，不阻塞读
func (mod *Modem) writeLoop() {
	for {
		select {
		case <-mod.closeChan:
			return
		case cmd := <-mod.writeChan:
			mod.mu.Lock()
			if mod.serialPort == nil {
				mod.mu.Unlock()
				continue
			}
			_, err := mod.serialPort.Write([]byte(cmd + "\r\n"))
			mod.mu.Unlock()
			if err != nil {
				logger.Error("write serial error",
					zap.Int("modem", mod.ID), zap.String("cmd", cmd), zap.Error(err))
			}
		}
	}
}

// stuckCheckLoop 串口卡死检测：读循环心跳停滞（读阻塞）则重启模组。
// 注意：健康模组可能长时间无数据上报，所以不按"无数据"判定，
// 而是按"读循环心跳是否还在跳"判定——真卡死是读阻塞导致心跳停止。
func (mod *Modem) stuckCheckLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-mod.closeChan:
			return
		case <-ticker.C:
			mod.mu.Lock()
			stuck := time.Since(mod.lastReadTick) > config.Cfg.Serial.ReadStuckThreshold
			mod.mu.Unlock()
			if stuck {
				logger.Warn("modem serial read stuck (heartbeat stopped), restart",
					zap.Int("id", mod.ID),
					zap.Duration("idle", time.Since(mod.lastReadTick)))
				mod.Restart()
				return
			}
		}
	}
}

// Restart 重启模组串口（卡死恢复 / 掉线重连）
func (mod *Modem) Restart() {
	mod.Close()
	time.Sleep(1 * time.Second)
	if err := mod.Start(); err != nil {
		logger.Error("modem restart failed", zap.Int("id", mod.ID), zap.Error(err))
	}
}

// Close 关闭模组。关闭后 closeChan 由下次 Start 重建，可安全重启。
func (mod *Modem) Close() {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	if !mod.isRunning {
		return
	}
	close(mod.closeChan)
	if mod.serialPort != nil {
		_ = mod.serialPort.Close()
		mod.serialPort = nil
	}
	mod.isRunning = false
	logger.Info("modem closed", zap.Int("id", mod.ID))
}

// ---- from: modem_discover.go ----

// groupTTYUSB 将串口列表分组为模组（兼容旧逻辑：按固定端口数或连续编号）。
// 注意：这是"先分组后探测"模式，在 Windows 多模组 + 干扰串口场景可能错位。
// 推荐使用 DiscoverModems 中的"先探测后分组"模式。

// groupTTYUSB 将串口列表分组为模组（兼容旧逻辑：按固定端口数或连续编号）。
// 注意：这是"先分组后探测"模式，在 Windows 多模组 + 干扰串口场景可能错位。
// 推荐使用 DiscoverModems 中的"先探测后分组"模式。
func groupTTYUSB(allPorts []string) [][]string {
	var groups [][]string
	if config.Cfg.Modem.TTYPerModem > 0 {
		for i := 0; i < len(allPorts); i += config.Cfg.Modem.TTYPerModem {
			end := i + config.Cfg.Modem.TTYPerModem
			if end > len(allPorts) {
				end = len(allPorts)
			}
			groups = append(groups, allPorts[i:end])
		}
		return groups
	}
	var cur []string
	lastNum := -1
	for _, t := range allPorts {
		num := netdev.SerialPortIndex(t)
		if num < 0 {
			continue
		}
		if lastNum == -1 || num == lastNum+1 {
			cur = append(cur, t)
			lastNum = num
		} else {
			groups = append(groups, cur)
			cur = []string{t}
			lastNum = num
		}
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	return groups
}

// windowAround 保留兼容（旧"按 AT 口圈定关联端口"策略，已不用于主流程）
func windowAround(allPorts []string, atPort string, n int) []string {
	if n <= 1 {
		return []string{atPort}
	}
	idx := -1
	for i, p := range allPorts {
		if p == atPort {
			idx = i
			break
		}
	}
	if idx < 0 {
		return []string{atPort}
	}
	half := (n - 1) / 2
	start := idx - half
	if start < 0 {
		start = 0
	}
	end := start + n
	if end > len(allPorts) {
		end = len(allPorts)
		start = end - n
		if start < 0 {
			start = 0
		}
	}
	return allPorts[start:end]
}

// groupPorts 将串口列表按物理设备分组。
// 默认调用平台实现（Linux sysfs / Windows 注册表），测试时替换以注入模拟数据。
var groupPorts = netdev.GroupByUSBDevice
