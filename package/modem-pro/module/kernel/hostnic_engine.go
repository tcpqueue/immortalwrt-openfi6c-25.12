package kernel

// ====================== 主机网卡（引擎侧的胶水） ======================
//
// 纯逻辑（机型特例 / 命令生成 / 校验 / 落盘）在 hostnic.go；这里负责：
//
//	① 列主机网卡（net.Interfaces，纯本机、不占串口）
//	② 组装页面要的视图（当前配置 + 建议值 + 三组命令）
//	③ **执行**命令（os/exec）—— 唯一会改主机网络的地方
//
// ⚠️ 执行是有前提的：改网卡要管理员 / root。进程没权限时命令会失败，
// 这时把原始错误原样返回（页面提示改用手工复制命令）——**不静默失败、不自动提权**。
// 在路由器上（ipk 的 procd 默认以 root 跑）这一步是直接可用的。

import (
	"context"
	"fmt"
	"modemd/module/config"
	"modemd/module/dial"
	"modemd/module/hostnic"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

// hostNICExecTimeout 单条命令的超时（netsh/ip 都是秒级操作，20 秒足够）
const hostNICExecTimeout = 20 * time.Second

// HostIface 主机上的一张网卡（页面下拉框用）
type HostIface struct {
	Name   string   `json:"name"`
	Up     bool     `json:"up"`
	Addrs  []string `json:"addrs,omitempty"` // "10.0.0.2/24"
	Strong bool     `json:"strong"`          // 名字本身像 USB / WWAN 口
	Note   string   `json:"note,omitempty"`
}

// isVirtualIface 明显与模组数据口无关的网卡（虚拟 / 无线 / 蓝牙 / 环回）
func isVirtualIface(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, bad := range dial.DialIfaceReject {
		if strings.Contains(n, bad) {
			return true
		}
	}
	return false
}

// HostInterfaces 列出主机上网卡（去掉环回与明显的虚拟网卡），模块口排在前面。
func HostInterfaces() []HostIface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]HostIface, 0, len(ifaces))
	for _, in := range ifaces {
		if in.Flags&net.FlagLoopback != 0 || isVirtualIface(in.Name) {
			continue
		}
		_, strong := dial.MatchDialIface(in.Name)
		item := HostIface{Name: in.Name, Up: in.Flags&net.FlagUp != 0, Strong: strong}
		hasV4, hasOther4 := false, false
		if addrs, err := in.Addrs(); err == nil {
			for _, a := range addrs {
				ipn, ok := a.(*net.IPNet)
				if !ok || ipn.IP == nil {
					continue
				}
				item.Addrs = append(item.Addrs, fmt.Sprintf("%s/%d", ipn.IP.String(), onesOf(ipn.Mask)))
				if ipn.IP.To4() == nil {
					continue
				}
				hasV4 = true
				if !strings.HasPrefix(ipn.IP.To4().String(), "169.254.") {
					hasOther4 = true
				}
			}
		}
		switch {
		case !hasV4:
			item.Note = "没有 IPv4 地址"
		case !hasOther4:
			// 只有 169.254 = 没拿到 DHCP。只有**像模组口**的那张才提示 FM350 那件事，
			// 否则蓝牙 / 手机网络这些也挂 169.254，全写一遍会让人以为每张网卡都要配。
			if strong {
				item.Note = "只有 169.254.x.x（没拿到 DHCP —— 这个机型要手工配一次）"
			} else {
				item.Note = "只有 169.254.x.x（没拿到 DHCP）"
			}
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Strong != out[j].Strong {
			return out[i].Strong
		}
		if out[i].Up != out[j].Up {
			return out[i].Up
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// onesOf 掩码 → 前缀长度（拿不到就 0）
func onesOf(m net.IPMask) int {
	if n, bits := m.Size(); bits > 0 {
		return n
	}
	return 0
}

// HostNICPlatform 当前平台标识：windows / linux / openwrt（后者决定要不要出 UCI 持久化块）
func HostNICPlatform() string {
	if runtime.GOOS == "linux" && hostnic.IsOpenWrt() {
		return "openwrt"
	}
	return runtime.GOOS
}

// HostNICElevated 本进程是否具备改主机网卡的权限。
// Windows 用"能不能打开物理磁盘句柄"判断（管理员专属），Linux 看 uid 是不是 0
// （ipk 的 procd 以 root 跑 → 路由器上这里为 true，可以直接点「执行」）。
func HostNICElevated() bool {
	if runtime.GOOS == "windows" {
		f, err := os.Open(`\\.\PHYSICALDRIVE0`)
		if err != nil {
			return false
		}
		_ = f.Close()
		return true
	}
	return os.Geteuid() == 0
}

// HostNICView 页面「主机网卡」卡片要的全部数据
type HostNICView struct {
	ModemID int    `json:"modem_id"`
	Model   string `json:"model,omitempty"`

	Required bool   `json:"required"`
	Reason   string `json:"reason,omitempty"`

	OS       string `json:"os"`
	Elevated bool   `json:"elevated"`

	Interfaces []HostIface `json:"interfaces"`

	Plan  hostnic.HostNICPlan `json:"plan"`
	Saved bool                `json:"saved"`
	// SavedPlans 已保存过配置的网卡（页面切换下拉时把对应参数带出来，不用重新发明一套）
	SavedPlans map[string]hostnic.HostNICPlan `json:"saved_plans,omitempty"`
	Suggested  *hostnic.HostNICPlan           `json:"suggested,omitempty"`
	ModemIP    string                         `json:"modem_ip,omitempty"`

	Actions []hostnic.HostNICActionSet `json:"actions"`
	Note    string                     `json:"note,omitempty"`

	// Platform windows / linux / openwrt；OpenWrt 上额外给一份 UCI 持久化命令
	// （运行期的 ip addr/route 会被 netifd 的 reload 抹掉，见 HostNICPersistCommands）
	Platform    string                   `json:"platform"`
	OpenWrt     bool                     `json:"openwrt"`
	Persist     []hostnic.HostNICCommand `json:"persist,omitempty"`
	PersistHint string                   `json:"persist_hint,omitempty"`
}

// HostNICView 组装当前模组的「主机网卡」视图。
// 只在需要时（FM350）才多发那条读地址的 AT：其余机型视图里的 ModemIP 直接用拨号状态里的值。
func (mod *Modem) HostNICView() *HostNICView {
	v := &HostNICView{
		ModemID:    mod.ID,
		Model:      mod.ModelName,
		OS:         hostnic.HostNICOS(),
		Elevated:   HostNICElevated(),
		Interfaces: HostInterfaces(),
	}
	required, reason := hostnic.HostNICQuirk(mod.ModelName)
	if !required {
		// ModelName 可能为空（还没读到 ATI）→ 用原始 ATI 串再判一次
		required, reason = hostnic.HostNICQuirk(mod.Model)
	}
	v.Required, v.Reason = required, reason

	// 模组上报的本机地址：优先拨号状态（不发 AT），没有再看要不要补一条查询
	dial := mod.DialStatus()
	v.ModemIP = dial.IPv4
	if v.ModemIP == "" {
		cid := dial.CID
		if cid <= 0 {
			cid = DefaultDataCID
		}
		if ipv4, _, err := mod.queryPDPAddrs(cid); err == nil {
			v.ModemIP = ipv4
		}
	}

	// 选哪张网卡：已保存配置的那张 → 否则第一个强候选 → 否则第一张
	v.SavedPlans = map[string]hostnic.HostNICPlan{}
	for _, name := range hostnic.HostNICPlanNames() {
		if p, ok := hostnic.GetHostNICPlan(name); ok {
			v.SavedPlans[name] = p
		}
	}
	selected := ""
	if names := hostnic.HostNICPlanNames(); len(names) > 0 {
		selected = names[0]
		for _, it := range v.Interfaces {
			if _, ok := hostnic.GetHostNICPlan(it.Name); ok {
				selected = it.Name
				break
			}
		}
	}
	if selected == "" && len(v.Interfaces) > 0 {
		selected = v.Interfaces[0].Name
	}

	if p, ok := hostnic.GetHostNICPlan(selected); ok {
		p.Interface = selected
		v.Plan, v.Saved = p, true
	}
	if s, ok := hostnic.SuggestHostNIC(v.ModemIP); ok {
		s.Interface = selected
		v.Suggested = &s
		if !v.Saved {
			v.Plan = s
		}
	}
	if v.Plan.Interface == "" {
		v.Plan.Interface = selected
	}
	v.Actions = hostnic.HostNICActionSets(v.Plan, v.Platform)
	// OpenWrt：额外给一份 UCI 持久化命令（只显示不执行，见 HostNICPersistCommands）
	v.Persist = hostnic.HostNICPersistCommands(v.Plan, v.Platform)
	if len(v.Persist) > 0 {
		v.PersistHint = "上面那几组是运行期配置：OpenWrt 上 netifd 按 /etc/config/network 管接口，" +
			"/etc/init.d/network reload、重启、从 LuCI 改任何网络设置都会把它抹掉；" +
			"而且 busybox 的 ip 是精简版（`ip: not found` 或子命令不支持时就配不成）。" +
			"所以要持久（也最稳）就写 UCI —— 下面这段自己看一眼再贴：接口名与防火墙分区要按你的固件调，" +
			"模组口别并进 br-lan；要做上行 WAN 还要把这个接口划进 firewall 的 wan 区，" +
			"否则只有路由器自己能用它，局域网不会走这条出口。"
	}

	// 页面上的说明：把"为什么"和"这份值从哪来"讲清楚，别让人以为程序知道模组的网段
	notes := []string{}
	if required {
		notes = append(notes, "这份 IP/掩码/网关是按模组上报的地址推算的起始值，请按你实际能通的那套参数改；"+
			"改完点「保存参数」，会记进 hostnic.json（重启不丢）。")
		notes = append(notes, "模组每次重新拨号，运营商给的地址会变 —— 网卡这边也要跟着重新取一次（这就是该机型"+
			"要\"配 IP 对应\"的由来）。")
	} else {
		notes = append(notes, "当前机型不在这份特例表里（FM350 才需要手工配主机网卡）；"+
			"如果实测确实要手工配，这里同样可以用。")
	}
	if !v.Elevated {
		notes = append(notes, "本进程没有管理员 / root 权限：命令可以复制出去自己执行；"+
			"在路由器上（ipk 的 procd 以 root 跑）可以直接点「执行」。")
	}
	v.Note = strings.Join(notes, " ")
	return v
}

// HostNICResult 一条命令的执行结果
type HostNICResult struct {
	Label  string `json:"label"`
	Text   string `json:"text"`
	OK     bool   `json:"ok"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// RunHostNICCommands 依次执行命令，逐条返回结果（不再拼 shell：直接用 argv）。
// 一条失败不影响后面的 —— 例如 DNS 那条失败不该挡住配地址。
func RunHostNICCommands(cmds []hostnic.HostNICCommand) []HostNICResult {
	out := make([]HostNICResult, 0, len(cmds))
	for _, c := range cmds {
		res := HostNICResult{Label: c.Label, Text: c.Text}
		if c.Manual {
			// 防御：Manual 的命令（如 UCI 持久化）只该显示给用户，绝不能由我们代发
			res.Error = "这条只显示不执行（改的是固件自己的网络配置，请人工核对后手工执行）"
			out = append(out, res)
			continue
		}
		if c.Prog == "" || len(c.Args) == 0 {
			res.Error = "没有可执行的子命令"
			out = append(out, res)
			continue
		}
		timeout := c.Timeout
		if timeout <= 0 {
			timeout = hostNICExecTimeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		cmd := exec.CommandContext(ctx, c.Prog, c.Args...)
		b, err := cmd.CombinedOutput()
		cancel()
		// ⚠️ Windows 上 netsh / ipconfig 的中文输出是 GBK，必须先转码（见 hostnic_windows.go）
		res.Output = strings.TrimSpace(config.Truncate(hostnic.DecodeHostNICOutput(b), 1000))
		res.OK = err == nil
		if err != nil {
			res.Error = err.Error()
			if ctx.Err() == context.DeadlineExceeded {
				res.Error = "命令超时（" + timeout.String() + "）"
				if c.Prog == "ipconfig" || c.Prog == "udhcpc" {
					res.Error += "：这次没等到 DHCP 应答（该模组的 DHCP 实测很慢，几十秒到一两分钟；" +
						"不等它也行）—— 看一下上方网卡地址有没有变；一直没有就用「应用静态配置」手工配"
				}
			}
			logger.Warn("host nic command failed",
				zap.String("cmd", c.Text), zap.String("err", res.Error))
		} else {
			logger.Info("host nic command ok", zap.String("cmd", c.Text))
		}
		out = append(out, res)
	}
	return out
}

// HostNICApplyResult 一次动作的完整结果（命令 + 执行结果）
type HostNICApplyResult struct {
	ModemID  int                      `json:"modem_id"`
	Mode     string                   `json:"mode"`
	Label    string                   `json:"label"`
	Plan     hostnic.HostNICPlan      `json:"plan"`
	Executed bool                     `json:"executed"`
	Results  []HostNICResult          `json:"results,omitempty"`
	Commands []hostnic.HostNICCommand `json:"commands"`
}

// HostNICApply 生成某组动作的命令；execute=true 时真的去执行。
//
// 除"先试 DHCP"外，静态配置 / 恢复 DHCP 都要求先把参数存下来
// （否则下次进来又是建议值，等于没配）。
func HostNICApply(mode string, plan hostnic.HostNICPlan, execute bool) (*HostNICApplyResult, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != hostnic.HostNICRenew && mode != hostnic.HostNICStatic && mode != hostnic.HostNICDhcp {
		return nil, fmt.Errorf("mode 只支持 renew / static / dhcp，当前 %q", mode)
	}
	plan = hostnic.NormalizeHostNICPlan(plan)
	if mode != hostnic.HostNICRenew {
		if err := hostnic.ValidateHostNICPlan(plan); err != nil {
			return nil, err
		}
	} else if plan.Interface == "" {
		return nil, fmt.Errorf("先选一张网卡")
	}

	cmds := hostnic.HostNICCommands(mode, plan, hostnic.HostNICOS())
	label := map[string]string{hostnic.HostNICRenew: "先试 DHCP", hostnic.HostNICStatic: "应用静态配置", hostnic.HostNICDhcp: "恢复自动获取"}[mode]
	res := &HostNICApplyResult{Mode: mode, Label: label, Plan: plan, Commands: cmds}

	if execute {
		res.Executed = true
		res.Results = RunHostNICCommands(cmds)
	}
	return res, nil
}
