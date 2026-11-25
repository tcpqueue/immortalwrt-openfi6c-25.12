package kernel

// ====================== 本机文件 / 内核状态（厂商托管数据面） ======================
//
// 目标：**接口只读文件，不打 AT**。
//
// 展锐 SIPC 这类 SoC 内置模组（中兴 F50）的 AT 走共享慢总线（实测单条 8~10 秒），
// 而 modemd 要显示的状态其实**本机就有**：
//
//	信号 / NR            ← 厂商 daemon 写的 URC 日志（urclog.go）
//	注册 CS/PS/EPS/5GS   ← 同上（URC 里本来就有 `+CxREG` 上报）
//	本机地址 / 网卡状态   ← 内核（net.InterfaceByName + Addrs，标准库读）
//	上/下行速率          ← /proc/net/dev 的接口计数器（纯文件）
//
// AT 只留给**动作**：发短信、改 IMEI、锁频/改组网模式、基站扫描、手动 AT 控制台，
// 外加"启动后补几次"的身份读取（型号/IMEI 这种东西本机没有文件来源）。
//
// ⚠️ 为什么读本机计数器**比读模组 AT 计数器更对**：
// 厂商托管的数据面把 PDP 承载直接做成了以太网接口（展锐 SIPC：sipa_eth0 = 主承载），
// 它的收发字节就是这台设备真正进出的流量（含二层开销）；而 `AT+GTSTATIS?` 这类
// 指令在很多固件上**根本没有**（展锐就是空指令 ⇒ 以前界面上速率一直是"未上报"）。

import (
	"modemd/module/cell"
	"modemd/module/netdev"
	"net"
	"os"
	"strings"
	"time"
)

const (
	// mu300DataIfacePrefix 厂商托管数据面的网卡名前缀（展锐 SIPC：sipa_eth0..15）。
	mu300DataIfacePrefix = "sipa_eth"
)

// procNetDevPath 内核网卡计数器（所有接口一次读完，纯文件）。
//
// ⚠️ 是**变量**：单测会把它换成临时文件 —— 否则测试会去认真机的 /proc/net/dev，
// 读出来的速率取决于开发机上真实流量（不可复现）。生产代码永远是系统路径。
var procNetDevPath = "/proc/net/dev"

// ifaceCounters 一个网卡的累计收发字节。
type ifaceCounters struct {
	rx, tx uint64
}

// procNetDevCounters 解析 /proc/net/dev 的内容。
//
// 格式（两行标题，之后每行一个接口，"接口名:" 之后是 16 列计数）：
//
//	eth0: 21747 134 0 0 0 0 0 0 15597 159 0 0 0 0 0 0
//	      ↑ 收字节（第 1 列）        ↑ 发字节（第 9 列）
//
// 只取 bytes；认不出的行直接跳过（内核格式变了也不能把解析搞崩）。
func procNetDevCounters(data string) map[string]ifaceCounters {
	out := make(map[string]ifaceCounters)
	for _, line := range strings.Split(data, "\n") {
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(line[:i])
		if name == "" || name == "Inter-|" || strings.HasPrefix(name, "face") {
			continue
		}
		fields := strings.Fields(line[i+1:])
		if len(fields) < 9 {
			continue
		}
		rx, ok1 := parseDecUint(fields[0])
		tx, ok2 := parseDecUint(fields[8])
		if !ok1 || !ok2 {
			continue
		}
		out[name] = ifaceCounters{rx: rx, tx: tx}
	}
	return out
}

// parseDecUint 十进制无符号解析（内核这些列永远是十进制；脏数据当失败）。
func parseDecUint(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	var v uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + uint64(c-'0')
	}
	return v, true
}

// readProcNetDev 读内核网卡计数器；读不到返回 nil（非 Linux / 权限不足都算）。
func readProcNetDev() map[string]ifaceCounters {
	b, err := os.ReadFile(procNetDevPath)
	if err != nil {
		return nil
	}
	return procNetDevCounters(string(b))
}

// pickMu300DataIface 从计数器表里挑出厂商数据网卡。
//
// 判据：名字带厂商前缀 + **收发字节最多**（PDP 主承载那条；设备刚起来时全都是 0，
// 那时退回按名字取第一个，保证不会因为"还没流量"而判成没有数据面）。挑不到返回空串。
func pickMu300DataIface(counters map[string]ifaceCounters) string {
	best, bestTotal, fallback := "", uint64(0), ""
	for name, c := range counters {
		if !strings.HasPrefix(name, mu300DataIfacePrefix) {
			continue
		}
		if fallback == "" || name < fallback {
			fallback = name
		}
		if total := c.rx + c.tx; total > bestTotal {
			best, bestTotal = name, total
		}
	}
	if best != "" {
		return best
	}
	return fallback
}

// mu300DataIface 当前在用的厂商数据网卡（每次读一遍计数器，读不到返回空串）。
func mu300DataIface() string {
	return pickMu300DataIface(readProcNetDev())
}

// ifaceIPAddrs 取一个网卡上的第一个 IPv4 与第一个全球单播 IPv6。
//
// ⚠️ 用 Go 标准库读内核（net.Interfaces），**不 exec `ip`、不碰 AT**：
// Windows 上通常没有同名接口，自然返回空，不会误报。
// 链路本地（fe80::）与回环都排除 —— 界面上显示它们没有意义。
func ifaceIPAddrs(name string) (v4, v6 string, ok bool) {
	if name == "" {
		return "", "", false
	}
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return "", "", false
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return "", "", false
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsUnspecified() || ip.IsLoopback() {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			if v4 == "" {
				v4 = ip4.String()
			}
			continue
		}
		if v6 == "" && !ip.IsLinkLocalUnicast() {
			v6 = ip.String()
		}
	}
	return v4, v6, v4 != "" || v6 != ""
}

// mu300FileBackedState 这台模组的状态是否可以**完全从本机文件/内核拿到**（接口路径上零 AT）。
//
// ⚠️⚠️ 这条**只针对展锐 SIPC 这一族**（中兴 F50 / UMS9620：SoC 内置模组 +
// 厂商托管数据面 + 厂商 daemon 写 URC 日志）。**别的模组一律不走这条路** ——
// 它们既没有 `/run/mu300-at/urc/stty_nr0.log`，也没有 `sipa_ethN`。
//
// 三道门，缺一不可：
//
//	① 通道名必须是展锐 SIPC（`/dev/stty_nr*`）—— 先把整族非展锐模组拦在外面；
//	② URC 跟随器真的在供**信号 + 注册** —— 跟随器不可用时要能自动退回 AT 轮询；
//	③ URC **没跟丢**（信号类上报不能超时）—— 见 urcSignalStaleLimit。
//
// 写在一起是为了让以后的人看到名字就知道“这是展锐专用的通道”，
// 不会顺手把它扩展到普通 USB 模组上去（那会把正常设备的刷新路径改道）。
func mu300FileBackedState(mod *Modem) bool {
	if mod == nil || !netdev.IsMu300SIPCChan(mod.ATPort) {
		return false
	}
	if !mod.urcCoversSignal() || !mod.urcCoversNetwork() {
		return false
	}
	return !mod.urcSignalStale()
}

// urcServingCells 取"用本机文件（URC）拼出来的服务小区"，只在展锐 SIPC 且 URC 在供状态时给。
//
// 为什么要它：这台模组**没有**小区扫描指令（`AT+QENG` / `AT+CPSI` / `AT+QNWINFO` 在本机都回
// `+CME ERROR: 4`，见 UnisocCommand），但 URC 日志里已经有 PCI / 小区ID+TAC / RSRP·RSRQ·SINR /
// 制式 / 运营商 / 频段 ⇒「基站扫描」页不该是空的（用户要求"文件里有就写上"）。
//
// ⚠️ 调用方只在**没有扫描缓存**时用它（扫描结果更全，不能被这份"少几个字段"的覆盖）——
// 与 networkStatusFromURC 里用的是同一条优先级规则。
// ⚠️ 邻区仍然为空：本机没有任何邻区来源（扇描属于"动作"，不在只读文件的路径里）。
// ⚠️ 非展锐模组一律返回 nil（mu300FileBackedState 先把它们拦住），行为一个字不变。
func (mod *Modem) urcServingCells() []cell.CellInfo {
	if !mu300FileBackedState(mod) {
		return nil
	}
	ci := mod.servingCellFromURC()
	if ci == nil {
		return nil
	}
	return []cell.CellInfo{*ci}
}

// urcSignalStale URC 里已经太久没有信号类上报（daemon 停了 / 跟随器卡了）。
//
// 真机实测展锐的 `+CSQ`/`+CESQ` 大约每 20 秒就来一条（信号变化即报），
// 所以超过 urcSignalStaleLimit 没有 = 异常。
func (mod *Modem) urcSignalStale() bool {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	if mod.urcSignalAt.IsZero() {
		return true
	}
	return time.Since(mod.urcSignalAt) >= urcSignalStaleLimit
}

// HostDataPlane 当前厂商数据网卡与它上面的地址（读内核，零 AT）。
func (mod *Modem) HostDataPlane() (iface, ipv4, ipv6 string) {
	iface = mu300DataIface()
	if iface == "" {
		return "", "", ""
	}
	ipv4, ipv6, _ = ifaceIPAddrs(iface)
	return iface, ipv4, ipv6
}

// SampleTrafficFromHost 用本机网卡计数器采样一次速率（零 AT）。
//
// 计数式差分走的是与模组 AT 计数器**完全相同**的那条路径（applyTrafficSample），
// 所以配额计量、界面速率、累计量都不用改。
func (mod *Modem) SampleTrafficFromHost() (Rate, bool) {
	iface := mu300DataIface()
	if iface == "" {
		return Rate{}, false
	}
	c, ok := readProcNetDev()[iface]
	if !ok {
		return Rate{}, false
	}
	return mod.applyTrafficSample(c.tx, c.rx, time.Now()), true
}
