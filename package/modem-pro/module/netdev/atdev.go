package netdev

// ====================== AT 通道（tty 串口 / 字符设备） ======================
//
// 以前 AT 口一律当"串口"打开（tarm/serial：termios 设波特率 + 原始模式，见 ports.go / modem.go）。
// 这对 PCIe 模组是**致命**的 —— PCIe 模组的 AT 口不是 tty：
//
//	/dev/mhi_DUN     高通 MHI（mhi_uci 驱动）的 AT 通道
//	/dev/wwan0at0    wwan 子系统的 AT 口（联发科 T7xx / 部分高通固件）
//	/dev/qcqmi0      GobiNet 的 QMI 口（不是 AT 口，但同样是字符设备）
//
// 对这类字符设备调 termios 会 ENOTTY，**打开必然失败** → PCIe 模组一个都认不出来。
// 所以这里把"AT 通道"抽成一个小接口：tty 继续走 tarm/serial，字符设备走裸 open，
// 上层（探测 / 模组读写协程）只认 Read/Write/Close，不关心底下是什么。

import (
	"fmt"
	"modemd/module/common"
	"modemd/module/config"
	"path/filepath"
	"strings"
	"time"

	"github.com/tarm/serial"
)

// AtDev 一条 AT 通道（串口或 MHI/wwan 字符设备）
type AtDev interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
}

// IsSIPCChannelName 这条通道是不是展锐 SIPC（/dev/stty_nr*）系列。
//
// ⚠️ 这些通道是**禁区**：SIPC 的 sbuf 环"关掉再开"会**永久弄哑**它
// （中兴 F50 的厂商注释实测：模组活着时 close+open，通道从此静默，
// 只有重启模组或整机才救），而 nr0/nr1 又是设备**数据面**（mobile-data）的命脉。
// 所以除了下面这一条例外，一律拒绝直接打开：
//
//	· nr2/nr3 → 是**命令通道**，已实测能直连（见 IsMu300DirectATDevice）；
//	· nr1 → 厂商 daemon 独占的代理口，**我们不碰**（那套 FIFO 代理实现已删除）。
func IsSIPCChannelName(name string) bool {
	base := filepath.Base(strings.TrimSpace(name))
	if !strings.HasPrefix(base, "stty_nr") {
		return false
	}
	return len(base) > len("stty_nr")
}

// IsNonTTYATDevice 这条 AT 通道的读会**返回 EAGAIN**（不是阻塞式 tty）——
// MHI / wwan 裸开的字符设备、以及展锐 SIPC 直连通道（nr2/nr3，同样是裸字符设备）。
//
// 用处就一处，但那处很关键：ProbeAT 对这类通道必须等到 deadline 才判死。
// tty 串口读到 0 字节 = 不应答；而这些通道读到 0 字节只代表"暂时没数据" ——
// 拿它当"不是 AT 口"会把好好的口永久写进否定缓存。
//
// ⚠️ 新增一种裸字符设备时**必须**加到这里。2026-10-05 就踩过：展锐 nr2 直连快路
// 没进这个判据，被 ProbeAT 当成 tty，第一拍 EAGAIN 就判死 ⇒ 模组一直发现不了。
func IsNonTTYATDevice(name string) bool {
	return IsCharATDevice(name) || IsMu300DirectATDevice(name)
}

// openSpecialATDevice 处理"平台特有的 AT 通道"与"禁区"。
// 返回 handled=true 表示已经定论（成功或失败都不要再往下走通用逻辑）。
func openSpecialATDevice(name string, probe bool) (AtDev, error, bool) {
	// ① 展锐 SIPC 的**命令通道**（nr2/nr3）：模组直接服务、不走厂商 daemon，
	//    实测单条 AT ~10 ms（走 daemon 代理的 nr1 要 1~4 s，那套实现已删）。
	//
	//    ⚠️ 它是**裸字符设备**（不是 tty：`stty` 在它上面直接失败），所以用 openCharATDevice
	//    裸 open + O_NONBLOCK，和 MHI/wwan 同一条路径。
	//    ⚠️⚠️ 因此 ProbeAT 必须把它当"非 tty"（见 IsNonTTYATDevice）：O_NONBLOCK 下
	//    第一拍必然读到 EAGAIN，当成"不应答"就会把好好的口写进否定缓存
	//    （2026-10-05 踩了一整轮）。
	//
	//    ⚠️ **它是 F50 上唯一的扇描目标**：nr1（daemon 代理）单条要 1~4 s，
	//    已从扇描列表里拿掉（见 Mu300ATScanGlobs）；nr2 的代价是“要等模组侧
	//    协议栈起来才服务”（开机头几分钟完全不答）——
	//    靠 Mu300DirectRetryNext 固定 30s 重探在它醒来的第一时间认出来。
	if IsMu300DirectATDevice(name) {
		d, err := openCharATDevice(name)
		return d, err, true
	}
	if IsSIPCChannelName(name) {
		return nil, fmt.Errorf("拒绝直接打开 SIPC 通道 %s：会永久弄哑通道，且会抢掉数据面", name), true
	}
	return nil, nil, false
}

// OpenATDevice 打开一条 AT 通道。
//
//	tty（/dev/ttyUSB* /dev/ttyACM* / COMx）→ tarm/serial（要设波特率、原始模式）
//	字符设备（/dev/mhi_DUN、/dev/wwan0at0 …）→ 裸 open，不碰 termios
//
// readTimeout 只对 tty 有意义（字符设备由内核按请求排队，读不到就一直等，
// 所以上层必须有硬超时 —— ProbeATTimed / readLoop 的超时逻辑照旧管用）。
func OpenATDevice(name string) (AtDev, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("空的 AT 设备名")
	}
	if d, err, handled := openSpecialATDevice(name, true); handled {
		return d, err
	}
	if IsCharATDevice(name) {
		return openCharATDevice(name)
	}
	c := &serial.Config{
		Name:        common.NormalizePortName(name),
		Baud:        config.Cfg.Serial.Baud,
		ReadTimeout: config.Cfg.Serial.ATProbeTimeout,
	}
	port, err := serial.OpenPort(c)
	if err != nil {
		return nil, err
	}
	return port, nil
}

// OpenATDeviceRun 与 OpenATDevice 相同，但用"运行时"那套超时
// （模组读写协程要的是 1 秒读超时，探测要的是 ATProbeTimeout，两者不能混）。
func OpenATDeviceRun(name string) (AtDev, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("空的 AT 设备名")
	}
	if d, err, handled := openSpecialATDevice(name, false); handled {
		return d, err
	}
	if IsCharATDevice(name) {
		return openCharATDevice(name)
	}
	c := &serial.Config{
		Name:        common.NormalizePortName(name),
		Baud:        config.Cfg.Serial.Baud,
		ReadTimeout: time.Second,
	}
	port, err := serial.OpenPort(c)
	if err != nil {
		return nil, err
	}
	return port, nil
}
