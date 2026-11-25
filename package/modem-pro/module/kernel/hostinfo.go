package kernel

// ====================== 主机资源占用（推给屏幕的 INFO 页）======================
//
// 屏幕端**不读本地 /proc**（用户要求「屏幕的信息都需要使用 ws 去读取解析接口返回的信息」），
// 所以 CPU / 内存 这两个数只能由服务端读 /proc 再推过去。
//
// 报文：{"type":"sys","data":{"Cpu":37,"Mem":52}}
// 对面屏幕的 kernel 对应实现在 tftdisplay 的 src/utils/wsdata.c —— apply_host_info()
// 已经在消费 Cpu / Mem（大小写别名都认），出现在**任何**一条推送的 data 里都算数。
//
// 为什么单独一条循环、而不是塞进 PushLoop：
//  1. 这两个数**与模组无关**（是主机自己的），不该跟着模组的推送节拍走；
//  2. 读 /proc 不占串口、几乎不花代价，所以**不看有没有客户端**（没人看时 broadcast 自己早退）；
//  3. 温度不在这里推：INFO 页的温度用模组的 temp_max（modems 推送里带），
//     两边都推会互相覆盖，屏幕上的数就会一会儿模组、一会儿主机。
//
// 上机验收（192.168.1.1，modemd-pro 0.2.241-1，2026-10-07）：
// 屏幕 INFO 页 CPU/MEM 从 `0` 变成真实百分比（实测 3% / 52%），
// `tools/wsmock/probe.js` 能看到每 5 秒一条 `{"type":"sys",...}`。

import (
	"modemd/module/atparse"
	"modemd/module/common"
	"modemd/module/hostdisk"
	"os"
	"strconv"
	"strings"
	"time"
)

// sysPushInterval 主机占用的推送间隔（默认 1 秒：用户要求每秒一条，读数只花 /proc，代价可忽略）
const sysPushInterval = 1 * time.Second

// sysTempEvery 每多少拍刷一次模组温度（60 × 1s = 60 秒）。
//
// 为什么要它：模组温度平时**只在网页端打开「模组状态」时**才查（QueryModemStatus），
// 没人开网页时缓存在 mod.ModemStatus 里就是空的 ⇒ 屏幕 INFO 页的 TEMP 恒为 0。
// 这里按分钟级单独读一次（一条 AT+QTEMP；展锐 F50 那类直接读内核温度区，零 AT），
// ⚠️ **不写回 mod.ModemStatus** —— 那张卡片是整包读出来的，
// 只塞一个温度进去会让网页端拿到一份"看着新鲜但缺字段"的状态。
const sysTempEvery = 60

// cpuTimes 一次 /proc/stat 的 CPU 累计节拍（jiffies）
type cpuTimes struct {
	idle  uint64 // idle + iowait
	total uint64 // 各字段之和
}

// readCPUTimes 读 /proc/stat 的汇总行（第一行 `cpu  user nice system idle iowait …`）。
//
// ⚠️ 这里拿到的是**开机以来的累计值**，单次采样算不出占用率 —— 必须两次采样做差
// （见 cpuPercent / SysLoop 里那个 prev 基线）。
func readCPUTimes() (cpuTimes, bool) {
	var ct cpuTimes

	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return ct, false
	}
	line := string(b)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return ct, false
	}
	for i, s := range f[1:] {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			break
		}
		ct.total += v
		if i == 3 || i == 4 { // 第 4、5 项 = idle / iowait
			ct.idle += v
		}
	}
	return ct, ct.total > 0
}

// cpuPercent 用两次采样算 CPU 占用率（0~100）。首次调用只建立基线，返回 0。
func cpuPercent(prev *cpuTimes) int {
	now, ok := readCPUTimes()
	if !ok {
		return 0
	}
	if prev.total == 0 {
		*prev = now
		return 0
	}
	dTotal := now.total - prev.total
	dIdle := now.idle - prev.idle
	*prev = now
	if dTotal == 0 || dIdle > dTotal {
		return 0
	}
	p := int((dTotal - dIdle) * 100 / dTotal)
	if p > 100 {
		p = 100
	}
	return p
}

// memPercent 内存占用率 =（MemTotal - MemAvailable）/ MemTotal × 100。
//
// ⚠️ 用 **MemAvailable** 而不是 MemFree：Linux 会把 page cache 算作"已用"，
// 只看 MemFree 在路由器上会长期显示 90%+，看着像内存要爆了。
func memPercent() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	var total, avail uint64

	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			avail = v
		}
	}
	if total == 0 || avail >= total {
		return 0
	}
	p := int((total - avail) * 100 / total)
	if p > 100 {
		p = 100
	}
	return p
}

// HostSysInfo 主机资源占用 + 极值（屏幕 INFO 页与它右边三张趋势页要的数）。
//
// JSON 字段名用**大写**（与屏幕 wsdata.c 的键名表首选别名一致）；屏幕那边还认小写。
//
// Min/Max 是**自 modemd 启动起算**的极值，由这里累计后随每一拍一起推 ——
// 屏幕上"历史最高/最低"就不会因为屏幕自己重启而清零。
// ⚠️ CPU/内存的极值**不加 omitempty**：0% 是合法最小值，省掉它屏幕就会拿旧值；
//
//	温度那对用 omitempty（0 = 还没读到过温度，屏幕显示 `-`）。
//
// Temp 是**模组温度**（不是主机温度）：服务端自己有就直接带上，
// 没有（读不到 / 模组没起来）就留 0 —— omitempty 会让它整个不出现在报文里，
// 屏幕那边就不会拿一个 0 去覆盖已有的温度。
type HostSysInfo struct {
	Cpu     int `json:"Cpu"`
	Mem     int `json:"Mem"`
	Temp    int `json:"Temp,omitempty"`
	CpuMin  int `json:"CpuMin"`
	CpuMax  int `json:"CpuMax"`
	MemMin  int `json:"MemMin"`
	MemMax  int `json:"MemMax"`
	TempMin int `json:"TempMin,omitempty"`
	TempMax int `json:"TempMax,omitempty"`

	// ---- 下面三个给屏幕的 SYSTEM 页（INFO 后面那一页）----
	// Uptime 主机已运行秒数（/proc/uptime）。
	Uptime int `json:"Uptime,omitempty"`
	// Load 1 分钟平均负载 ×100（0.77 → 77），用整数避免浮点 JSON 在两端各存一份。
	Load int `json:"Load,omitempty"`
	// Disk 根分区（overlay / 根文件系统）使用率 %。
	Disk int `json:"Disk,omitempty"`
}

// hostUptimeSec 读 /proc/uptime 的秒数（读不到返 0）。
func hostUptimeSec() int {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v < 0 {
		return 0
	}
	return int(v)
}

// hostLoad1 读 /proc/loadavg 的 1 分钟平均负载 ×100。
func hostLoad1() int {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v < 0 {
		return 0
	}
	return int(v*100 + 0.5)
}

// hostDiskPercent 根文件系统使用率 % —— 实现按平台分文件：
// linux 用 statfs（见 hostdisk_linux.go），其它平台恒 0（hostdisk_other.go）。
// ⚠️ syscall.Statfs 只有 unix 有，写在 hostinfo.go 里 Windows 上就编译不过
// （本工程要在 Windows 上跑单测）。

// sysRange 自进程启动起的极值（CPU/内存一定会有；温度可能一直没有）。
type sysRange struct {
	cpuMin, cpuMax   int
	memMin, memMax   int
	tempMin, tempMax int
	have             bool // 第一拍只用来初始化，不参与比较
}

// apply 并入一轮采样（首抽直接当基准）。
func (r *sysRange) apply(cpu, mem, temp int) {
	if !r.have {
		r.cpuMin, r.cpuMax = cpu, cpu
		r.memMin, r.memMax = mem, mem
		r.tempMin, r.tempMax = temp, temp
		r.have = true
		return
	}
	if cpu < r.cpuMin {
		r.cpuMin = cpu
	}
	if cpu > r.cpuMax {
		r.cpuMax = cpu
	}
	if mem < r.memMin {
		r.memMin = mem
	}
	if mem > r.memMax {
		r.memMax = mem
	}
	// 温度只在"真的读到"时才参与（0 = 这次没读到）
	if temp > 0 {
		if r.tempMin == 0 || temp < r.tempMin {
			r.tempMin = temp
		}
		if temp > r.tempMax {
			r.tempMax = temp
		}
	}
}

// ReadHostSysInfo 采一次主机占用（prev 传上一轮的 CPU 节拍，用于差分）。
func ReadHostSysInfo(prev *cpuTimes) HostSysInfo {
	return HostSysInfo{Cpu: cpuPercent(prev), Mem: memPercent()}
}

// readModemTemp 只读一次模组温度（最高那一个），读不到返回 0。
//
// 零 AT 的那条路先试（thermalTemps：展锐 SIPC / 本机就是模组的情况）。
func readModemTemp(mgr *ModemManager) int {
	if mgr == nil {
		return 0
	}
	for _, mod := range mgr.listModems() {
		if !mod.isRunningNow() {
			continue
		}
		temps := mod.thermalTemps()
		if len(temps) == 0 {
			if cmd := mod.tempQueryCommand(); cmd != "" {
				if resp, err := mod.SendAT(cmd); err == nil && !common.RespHasError(resp) {
					temps = atparse.ParseTempResp(resp)
				}
			}
		}
		if maxT, _ := atparse.PickSummary(temps); maxT != nil && maxT.Celsius > 0 {
			return maxT.Celsius
		}
	}
	return 0
}

// pushSys 推一条主机占用（type = "sys"）。
//
// 走 broadcast 而不是直接 Hub：没人连时它会早退（连序列化都省了），
// 但**被动连接**（屏幕的 /display/ws）也算"有人"，屏幕收得到。
func pushSys(info HostSysInfo) {
	broadcast("sys", nil, info)
}

// SysLoop 周期推主机占用 + 模组温度。传入 interval <= 0 时用默认 1 秒。
func SysLoop(mgr *ModemManager, interval time.Duration) {
	if interval <= 0 {
		interval = sysPushInterval
	}
	var prev cpuTimes

	// 先采一次基线：CPU 占用率靠两次采样做差，本次必然是 0，下一拍才是真数字
	_ = ReadHostSysInfo(&prev)

	t := time.NewTicker(interval)
	defer t.Stop()
	temp := 0
	var rng sysRange
	for i := 1; ; i++ {
		<-t.C
		if i == 1 || i%sysTempEvery == 0 {
			if v := readModemTemp(mgr); v > 0 {
				temp = v
			}
		}
		info := ReadHostSysInfo(&prev)
		info.Temp = temp
		// 运行时间/负载/存储：都是读 /proc 或 statfs，成本可忽略，每秒带上（SYSTEM 页要）。
		info.Uptime = hostUptimeSec()
		info.Load = hostLoad1()
		info.Disk = hostdisk.HostDiskPercent()
		rng.apply(info.Cpu, info.Mem, temp)
		info.CpuMin, info.CpuMax = rng.cpuMin, rng.cpuMax
		info.MemMin, info.MemMax = rng.memMin, rng.memMax
		info.TempMin, info.TempMax = rng.tempMin, rng.tempMax
		pushSys(info)
	}
}
