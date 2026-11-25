package netdev

// ====================== 展锐 SIPC（中兴 F50 / UMS9620）通道策略 ======================
//
// 这个文件**刻意不带 build tag**：里面全是纯逻辑（判据 / 参数），在开发机（Windows）
// 上就能跑单测。真正碰设备的是通用的裸字符设备路径（atdev_unix.go 的 openCharATDevice）。
//
// ── 结论：只用**第二条命令通道（/dev/stty_nr2）** ─────────────────────────
// 2026-10-05 真机定位：
//   · `/dev/stty_nr1` 是厂商守护进程 `mu300-atd` 独占的代理口 —— 每条命令前有 1 秒
//     drain 静默窗、要抢 FIFO 锁、还要排厂商自己数据面的队 ⇒ **单条 1~4 秒**；
//   · `/dev/stty_nr2` 由模组**直接服务**，不走 daemon、没 drain、没锁 ⇒ **单条 ~10 ms**
//     （快两个数量级）。
// ⇒ 用户明确要求：**nr2 是唯一的 AT 通道，nr1 那套一律不要**。
// 所以这个文件里再也找不到 FIFO / 锁 / 应答文件 / drain 预算 / “共享慢总线降频”
// 这些东西 —— 它们全部随 nr1 一起删掉了。
//
// ⚠️ nr2 要等模组侧协议栈起来才服务（开机头几分钟一条都不答）。
// “先找不到模组”是正常的早期状态；靠 Mu300DirectRetryNext（固定 30s 重探）
// 在它醒来的第一时间就能认出来。
//
// ⚠️ nr2 是**裸字符设备**（不是 tty：`stty` 在它上面直接失败）⇒ 用
// openCharATDevice 裸 open + O_NONBLOCK，且 ProbeAT 必须把它当“非 tty”
//（IsNonTTYATDevice）：O_NONBLOCK 下第一拍必然 EAGAIN，当成“不应答”
// 就会把好口写进否定缓存（踩过一整轮）。

import (
	"modemd/module/config"
	"strings"
	"time"
)

const (
	// Mu300ATDir 厂商 daemon 的运行目录（URC 日志就写在这里）。
	// ⚠️ 我们**不**往它的 FIFO 写命令（那条路是 nr1 的，已删），只读它的 URC 日志。
	Mu300ATDir = "/run/mu300-at"

	// mu300SIPCPrefix SIPC 通道名前缀（stty_nr0 … stty_nr31）
	mu300SIPCPrefix = "stty_nr"

	// Mu300DirectATChanName **我们用的 AT 命令通道**。
	//
	// ⚠️⚠️ 它是“快路”：由模组直接服务、**不经过厂商 daemon** —— 没有 1 秒 drain、
	// 没有 FIFO / 锁 / 排队。实测同一条 `AT+CGMM`：**~10 ms**（走已删除的 daemon
	// 代理路径是 1.0~4.0 s）。
	// 厂商注释说 nr2~nr5“什么也不说” —— 那是**只读**得出的结论：这些通道平时
	// 确实不主动吐东西，但**写 `AT` 进去是有人答的**（实测 nr2/nr3/nr5 都能答
	// AT / AT+CGMM / AT+CSQ / AT+CGSN / AT+CEREG? / AT+CPIN?），
	// 而且**开-关多次仍然活着**（“close 就哑”那条约束至少对 nr2/nr3 不成立）。
	Mu300DirectATChanName = "stty_nr2"
	// Mu300ATChanName 同义别名（等价于 Mu300DirectATChanName）。
	Mu300ATChanName = Mu300DirectATChanName
	// Mu300DirectATChanAlt 备选快路（nr2 打不开时可以手工指到它；不主动扫，
	// 因为同时扫两个会被 groupPorts 并成同一台模组的两个口）。
	Mu300DirectATChanAlt = "stty_nr3"

	// mu300ATGroupKey 展锐 SIPC 通道的分组键。
	// nr0..nr31 都是**同一台模组**的口：nr2/nr3 = 直连命令口，nr0 = URC 上报口。
	// 共用同一个键，发现流程才会把它们当**一台**模组（否则 groupPorts
	// 会认成两台 —— 0.2.101 踩过）。
	mu300ATGroupKey = "/sipc/mu300"

	// Mu300DirectProbeWindow 直连通道（nr2/nr3）的探测读取窗口。
	//
	// 健康的直连通道 ~10 ms 就回 OK；**不答就是模组侧协议栈还没起来**（不是“慢”）。
	// 所以不听 tty 的 Cfg.Serial.ATProbeTimeout（4 s）—— 那 4 秒全白等。
	Mu300DirectProbeWindow = 1500 * time.Millisecond

	// Mu300DirectReadIdle 直连通道读循环的**空闲休息时长**。
	//
	// ⚠️ 它直接决定指令应答的额外延迟：nr2 是 O_NONBLOCK 裸设备，没数据时**立刻**
	// 返回 EAGAIN，读循环只能“睡一会儿再看”。预设的 100 ms 会把 ~10 ms 的快路
	// 拖成 ~110 ms；换成 10 ms 后平均只多 5 ms，而 CPU 代价是空闲时 100 次/秒的唤醒。
	Mu300DirectReadIdle = 10 * time.Millisecond

	// Mu300DirectRetryInterval 直连通道被判“非 AT 口”后的**固定**重探间隔。
	//
	// ⚠️ 为什么不能让它跟着走指数退避（真机实测，2026-10-05）：
	// nr2 在开机头几分钟不应答 ⇒ 被记为“非 AT 口” ⇒ 10s/20s/…/5min 退避。
	// 等它其实已经能用了（手工 `AT+CGMM` 秒回 V1.0.1-B7，uptime 332 s）时，
	// 退避正好把它压到 5 分钟才重试一次 —— 而它是我们**唯一**的 AT 通道，
	// 等于整台设备 5 分钟没有模组。固定 30 秒重探一次，成本只是每 30 秒一次
	// ≤1.5 s 的探测，却能及时认出来。
	Mu300DirectRetryInterval = 30 * time.Second
)

// Mu300ATChanRank 展锐 SIPC 命令通道的**优先顺序**（小 = 先探）；-1 = 不是命令通道。
// 现在只有 nr2/nr3 两条直连口在列（nr1 已删）。
func Mu300ATChanRank(name string) int {
	switch baseName(name) {
	case Mu300DirectATChanName: // nr2：~10 ms
		return 0
	case Mu300DirectATChanAlt: // nr3 备选
		return 1
	}
	return -1
}

// Mu300ATScanGlobs 扫描要列出的展锐通道。
//
// ⚠️⚠️ **只有 nr2 这一条**（用户明确要求：F50 只用它，它是串口、速度很快）：
//
//	· 不列 nr1 —— daemon 代理口（1~4 s/条）的实现已删除；
//	· 不列 nr3 —— 同时扫两个会被 groupPorts 并成同一台模组的两个口，
//	  白白多一次打开（需要时把 Mu300DirectATChanAlt 加进来即可）。
//
// ⚠️ nr2 要等模组侧协议栈起来才服务（开机头几分钟一条都不答）⇒ “找不到模组”是正常
// 早期状态；靠 Mu300DirectRetryNext（固定 30s 重探）在它醒来的第一时间就能认出来。
func Mu300ATScanGlobs() []string {
	return []string{Mu300DirectATChanName}
}

// Mu300SIPCGroupKey 展锐 SIPC 通道的物理归属键：同一台模组的所有 SIPC 口同键。
// 用途：让命令口与 URC 口（nr0/nr2）被并成**一台**模组（见 ports_unix.go 的 portGroupKey）。
func Mu300SIPCGroupKey(name string) (string, bool) {
	if strings.HasPrefix(baseName(name), mu300SIPCPrefix) {
		return mu300ATGroupKey, true
	}
	return "", false
}

// IsMu300SIPCChan 是不是展锐 SIPC 的通道（stty_nr0 … stty_nr31）——
// 含义是"挂在展锐 SoC 内置模组上的口"，与它是命令口 / 直连口 / 上报口无关。
func IsMu300SIPCChan(name string) bool {
	_, ok := Mu300SIPCGroupKey(name)
	return ok
}

// Mu300DirectRetryNext 直连通道的下次重探时刻（固定间隔，不进指数退避）。
func Mu300DirectRetryNext(failAt time.Time) time.Time {
	return failAt.Add(Mu300DirectRetryInterval)
}

// IsMu300DirectATDevice 是不是展锐 SIPC 的**命令通道**（stty_nr2 / stty_nr3）。
//
// 这类通道可以当**普通字符设备直接 open**（见 atdev_unix.go 的 openCharATDevice），
// 既不需要厂商 daemon，也没有任何“慢总线”策略 —— 它们本来就不慢（实测单条 ~10 ms）。
//
// 放在无 tag 文件里，让两侧平台共用同一份判据（Windows 上不存在这种通道，
// 但 openSpecialATDevice 会报“打不开”，不会误开别的东西）。
func IsMu300DirectATDevice(name string) bool {
	switch baseName(name) {
	case Mu300DirectATChanName, Mu300DirectATChanAlt:
		return true
	}
	return false
}

// ManagedDataPlane 这台模组的**数据面是不是由设备厂商自己托管**（不由 modemd 组网）。
//
// 目前只有一种：SoC 内置模组（展锐 SIPC，如中兴 F50）。
//
// ⚠️⚠️ 证据与后果（2026-10-05 实测，改之前先把这段读完）：
//   - 这台设备的数据面是**厂商的 netifd 接口** `proto=mu300cell`、绑在 `sipa_eth0` 上，
//     联网本来就是好的（默认路由 + 外网 ping 0% 丢包）；
//   - 模组**没有自己的 USB 网卡**（AT 走 SIPC 虚拟串口），所以 modemd 认不出它的数据网卡：
//     实测 `host_iface` 被认成了 **`usb0`**，而那是 **LAN 桥 br-lan 的成员口**；
//   - 一旦按这个走，modemd 会去写 APN、激活 PDP，并尝试建 WAN 接口 / 拉拨号器绑到 usb0 ——
//     那是**直接动用户的局域网与厂商托管的数据面**，弄坏就是断网。
//
// ⇒ 这类模组一律不碰组网：不写 APN、不激活 PDP、不建 WAN 接口、不拉主机侧拨号器。
//
// ⚠️ 判据要盖住**每一条能当 at_port 的 SIPC 命令通道**（nr2/nr3）：走哪条口，
// 数据面都还是厂商的 mobile-data 在管。漏了就会去动用户的局域网。
// ⚠️ 但也**不要**按 `stty_nr*` 前缀一网打尽：nr0 是 URC 上报口，永远不会是 at_port；
// 而“把正常端口误判成厂商托管”会让那台模组彻底不组网
// （TestManagedDataPlaneSkipsAutoWAN 就是钉这一条的护栏）。
func ManagedDataPlane(port string) bool {
	return IsMu300DirectATDevice(port)
}

// AtProbeWindow 探一个口“是不是 AT 口”时，愿意为等应答花多久。
//
// 直连通道单独一档（健康 ~10 ms，不答就是没起来），其余走配置里的 tty 值。
func AtProbeWindow(portName string) time.Duration {
	if IsMu300DirectATDevice(portName) {
		return Mu300DirectProbeWindow
	}
	return config.Cfg.Serial.ATProbeTimeout
}

// ReadIdleDelay 读循环“没读到数据”时的休息时长。
//
// 对 O_NONBLOCK 的直连通道（nr2）来说它就是应答延迟的**下限**，所以单独一档；
// 其余通道（tty / MHI / wwan）保持原来的 100 ms（行为不变）。
func ReadIdleDelay(portName string) time.Duration {
	if IsMu300DirectATDevice(portName) {
		return Mu300DirectReadIdle
	}
	return 100 * time.Millisecond
}

// baseName 取路径尾段（等价 filepath.Base，但**去掉平台差异**：
// Windows 上 filepath.Base 认反斜杠，而通道名永远是 Linux 的写法）。
// 单独写一份是为了让 device 判据在开发机上单测时行为一致。
// 顺带处理尾斜杠（filepath.Base 也会去掉它），免得 "/dev/stty_nr2/" 被判成不是 nr2。
func baseName(p string) string {
	p = strings.TrimSpace(p)
	for len(p) > 0 && p[len(p)-1] == '/' {
		p = p[:len(p)-1]
	}
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		p = p[i+1:]
	}
	return p
}
