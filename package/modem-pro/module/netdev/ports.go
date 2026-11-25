package netdev

// ====================== kernel：按功能分文件 ======================

import (
	"io"
	"modemd/module/config"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ---- from: ports/atprobe.go ----

// 本文件是「AT 口探测」：扫串口 + 判断哪个口是模组的 AT 口。
//
// 原先与「基站扫描」放在同一个文件里（现已并入 query.go），与串口枚举（ports_unix.go / ports_windows.go）
// 同属「串口层」，所以收到 ports.go 里；探测需要串口超时与波特率配置，
// 于是这里的探测直接读 config.Cfg（同包，没有循环依赖）。
// ScanTTYUSB 扫描系统中的串口设备。
// Linux 返回 /dev/ttyUSB*，Windows 返回 COMx，按编号升序。

func ScanTTYUSB() ([]string, error) {
	return ScanSerialPorts()
}

// ProbeAT 探测单个串口是否是有效的 AT 串口：发送 AT 并检查是否回 OK。
//
// 多数模组默认开启指令回显，且"回显"与"OK"可能分两次到达（例如先读到
// "AT\r"，再读到 "\r\nOK\r\n"）。因此这里必须累计读取直到出现 OK，
// 只读一次会导致探测随机失败、模组无法被发现。
func ProbeAT(portName string) (bool, error) {
	// tty 走 tarm/serial、PCIe 的 MHI/wwan 字符设备走裸 open（见 atdev.go）——
	// 以前一律按串口打开，PCIe 模组因此一个都认不出来（termios 对字符设备直接 ENOTTY）。
	port, err := OpenATDevice(portName)
	if err != nil {
		return false, err
	}
	defer port.Close()

	// 清空可能残留的输入：最多清 3 次，防止无数据时无限循环
	// （tarm/serial 在 Windows 上超时可能返回 n=0, err=nil）
	for i := 0; i < 3; i++ {
		buf := make([]byte, 256)
		if n, err := port.Read(buf); err != nil || n == 0 {
			break
		}
	}

	if _, err := port.Write([]byte("AT\r\n")); err != nil {
		return false, err
	}

	// 累计读取：回显 + OK 可能被拆成多次 Read 返回。
	//
	// ⚠️ 不能只用"读 3 次"：有的模组会**持续主动上报**（实测鼎桥 MT5700 每秒刷
	// ^RSSI / ^CERSSI / ^HCSQ 好几行），前几包全能是 URC —— 读到 OK 之前就把次数用完，
	// 于是明明能应答的 AT 口被判成"非 AT 口"，模组永远发现不了。
	// 现在改成"读到 OK 或探测超时为止"，并限制总字节数避免内存被灌爆。
	//
	// 两种口的行为刻意不一样：
	//
	//	tty 串口：读到 0 字节 = 没应答 → 提前退出（非 AT 口不白等，现场调过，别动）
	//	字符设备（MHI/wwan）：EAGAIN / 读超时只是"暂时没数据"，不是"不应答" ——
	//	                      必须等到 deadline 再判，否则会把能应答的口判死
	//	                      （MHI 的应答本来就比串口晚一拍）
	charDev := IsNonTTYATDevice(portName)
	var got strings.Builder
	buf := make([]byte, 256)
	// 等多久由通道类型决定：直连的展锐快路（nr2）健康时 ~10 ms 就答，
	// 不答就是模组侧还没起来，不陪它等满 tty 的 4 秒（见 AtProbeWindow）。
	deadline := time.Now().Add(AtProbeWindow(portName))
	for time.Now().Before(deadline) && got.Len() < 8192 {
		n, err := port.Read(buf)
		if n > 0 {
			got.Write(buf[:n])
			if strings.Contains(got.String(), "OK") {
				return true, nil
			}
			continue
		}
		if charDev {
			if err != nil && !IsWouldBlock(err) && err != io.EOF {
				break
			}
			// 轮询间隔：直连快路（nr2/nr3）健康时 ~10 ms 就答，用 30 ms 只会白拖探测时间；
			// 其余字符设备保持 30 ms（MHI 的应答本来就晚一拍）。
			d := 30 * time.Millisecond
			if IsMu300DirectATDevice(portName) {
				d = Mu300DirectReadIdle
			}
			time.Sleep(d)
			continue
		}
		if err != nil && err != io.EOF {
			break
		}
		break // n == 0：这一轮没读到东西（端口不应答），不必再等
	}
	// 真正的 AT 串口会应答 OK
	return strings.Contains(got.String(), "OK"), nil
}

// ProbeATTimed 给 AT 探测加一层**硬超时**，超时就当"不是 AT 口"。
//
// ⚠️ 为什么必须有（实测踩到，换了展锐模组后必然复现）：
// tarm/serial 的 ReadTimeout 靠 Windows 的 SetCommTimeouts，而某些驱动的虚串口
// 根本不遵守它 —— 一读就永远不返回。后果不是这一个端口慢，而是**整个发现流程卡死**：
// 进程活着、日志停在 `serial ports grouped by USB device` 之后、HTTP 端口永远起不来。
//
// 超时后返回 (false, nil) 是故意的：让调用方把它当成"非 AT 口"**写进否定缓存**，
// 否则巡检（scan_interval=1 时每秒一轮）会不停为同一个坏端口起 goroutine、泄漏句柄。
// 拓扑变化（插拔）时否定缓存会作废，所以不会永久误判。
// 正常情况下 AT 口几十毫秒就回了，这个超时只是兜底。
func ProbeATTimed(portName string) (bool, error) {
	type probeRes struct {
		ok  bool
		err error
	}
	ch := make(chan probeRes, 1)
	go func() {
		ok, err := ProbeAT(portName)
		ch <- probeRes{ok: ok, err: err}
	}()

	d := config.Cfg.Serial.ATProbeTimeout*4 + 2*time.Second
	if d < 5*time.Second {
		d = 5 * time.Second
	}
	select {
	case r := <-ch:
		return r.ok, r.err
	case <-time.After(d):
		config.Logger.Warn("probe serial port timeout, treat as non-AT",
			zap.String("port", portName), zap.Duration("timeout", d))
		return false, nil
	}
}

// atIdentityCmds 用来"验明正身"的指令，**按顺序试**：型号 → 厂商 → ATI。
//
// 为什么要试多条（2026-10-13 真机 FM350-GL）：它 **不认 `AT+CGMM`**（直接回 ERROR），
// 但 `AT+CGMI` / `ATI` 答得很好（Manufacturer / Model / Revision / IMEI 全有）。
// 只发 CGMM 的后果是：这个口被判成"不是命令口" ⇒ 整个自动探测失败 ⇒ modemd 退回
// 串口组的第一个口（实测那个口连 `AT` 都不答）⇒ 界面里"模组控制不了"。
//
// ⚠️ 顺序把 CGMM 放第一位、不动老行为：能答 CGMM 的模组**一条指令都不会多发**。
var atIdentityCmds = []string{"AT+CGMM", "AT+CGMI", "ATI"}

// isIdentityEchoLine 这行是不是我们发出去的指令的回显（多条指令都要认）。
func isIdentityEchoLine(line string) bool {
	s := strings.ToUpper(strings.TrimSpace(line))
	if s == "AT" {
		return true
	}
	for _, c := range atIdentityCmds {
		if s == c {
			return true
		}
	}
	return false
}

// ProbeATIdentity 判断这个口是不是**能真正执行指令**的命令口（不只是回了个 OK）。
//
// 做法：发一条身份指令（`AT+CGMM`），要求应答里出现**非 ERROR 的非空内容**。
//
// ⚠️ 为什么不能只看 `AT` 的 OK（2026-10-07 真机事故）：模组切到 MBIM 后，
// `/dev/ttyUSB1` 会对 `AT` "回一下 OK"（固件的杂散输出里带 OK），但发真指令全部超时 ——
// 把它当命令口，界面就是"控制不了"。加一条身份指令就能把这种口分辨出来。
func ProbeATIdentity(portName string) bool {
	port, err := OpenATDevice(portName)
	if err != nil {
		return false
	}
	defer port.Close()
	// 清残留（同 ProbeAT）
	for i := 0; i < 3; i++ {
		buf := make([]byte, 256)
		if n, err := port.Read(buf); err != nil || n == 0 {
			break
		}
	}
	charDev := IsNonTTYATDevice(portName)
	// 一条一条试：前面那条被固件拒绝（ERROR）不代表这个口不是命令口，换一条身份指令再问
	for _, cmd := range atIdentityCmds {
		if _, err := port.Write([]byte(cmd + "\r\n")); err != nil {
			return false
		}
		var got strings.Builder
		buf := make([]byte, 256)
		deadline := time.Now().Add(AtProbeWindow(portName))
		for time.Now().Before(deadline) && got.Len() < 4096 {
			n, err := port.Read(buf)
			if n > 0 {
				got.Write(buf[:n])
				s := got.String()
				if strings.Contains(s, "OK") || strings.Contains(strings.ToUpper(s), "ERROR") {
					break
				}
				continue
			}
			if charDev {
				if err != nil && !IsWouldBlock(err) && err != io.EOF {
					break
				}
				d := 30 * time.Millisecond
				if IsMu300DirectATDevice(portName) {
					d = Mu300DirectReadIdle
				}
				time.Sleep(d)
				continue
			}
			if err != nil && err != io.EOF {
				break
			}
			break
		}
		if ATIdentityLooksReal(got.String()) {
			return true
		}
	}
	return false
}

// ATIdentityLooksReal `AT+CGMM` 之类的应答"像不像一个真命令口"（纯逻辑，好单测）：
// 剔掉回显行与 OK/ERROR 这些协议行之后，**还得有一行带字母或数字的内容**（型号名）。
//
// 只有 OK、什么都没有 ⇒ 不足以证明它是命令口（MBIM 下那个假口就是这样）。
func ATIdentityLooksReal(resp string) bool {
	up := strings.ToUpper(resp)
	if strings.Contains(up, "ERROR") {
		return false // 明确的拒绝：不是我们要的口
	}
	for _, raw := range strings.Split(resp, "\n") {
		line := strings.TrimSpace(strings.Trim(raw, "\r"))
		if line == "" {
			continue
		}
		u := strings.ToUpper(line)
		if u == "OK" || isIdentityEchoLine(line) {
			continue // 回显 / 结束标记
		}
		for _, r := range line {
			if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				return true
			}
		}
	}
	return false
}
