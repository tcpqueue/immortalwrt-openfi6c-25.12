//go:build !windows

package kernel

import (
	"context"
	"encoding/json"
	"fmt"
	"modemd/module/netprov"
	"net"
	"os/exec"
	"strings"
	"sync"

	"go.uber.org/zap"
)

// noteAutoWAN 记录结果；**只有结论变了**才写日志与拨号步骤（循环每十几秒一次，别刷屏）。
func noteAutoWAN(mod *Modem, info AutoWANInfo) {
	prev, had := AutoWANInfoFor(mod.ID)
	setAutoWANInfo(mod.ID, info)
	if had && prev.Note == info.Note && prev.Error == info.Error &&
		prev.Created == info.Created && prev.Device == info.Device {
		return
	}
	if info.Error != "" {
		logger.Warn("auto wan", zap.Int("modem", mod.ID), zap.String("error", info.Error))
		mod.dialStep("自动组网（WAN 接口）", false, info.Error)
	} else {
		logger.Info("auto wan", zap.Int("modem", mod.ID), zap.String("note", info.Note))
		mod.dialStep("自动组网（WAN 接口）", true, info.Note)
	}
	pushDial(mod) // 让页面立刻看到这一步
}

// ---- 执行与查询 ----

// runNetProvCmd 跑一条外部命令（uci / ifup / init.d 脚本），失败时把输出带出来。
//
// ⚠️ 全程拿 netProvMu：uci 是"读配置文件 → 改 → 写回"，两个 uci 进程同时跑会**丢更新**。
// 现在有两条路径会改 UCI —— 自动组网循环（建/改绑）与模组消失时的清理（可能来自扫描 goroutine），
// 所以所有外部命令都从这一个口子出去（不嵌套加锁，runNetProvCmds 也是一条条串着调这里）。
func runNetProvCmd(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("空命令")
	}
	netProvMu.Lock()
	defer netProvMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), netProvTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%v: %s", err, msg)
		}
		return err
	}
	return nil
}

// netProvMu 串行化所有 uci / ifup 调用（原因见 runNetProvCmd 注释）
var netProvMu sync.Mutex

// runNetProvCmds 按顺序跑一串命令，遇到第一处失败就停（把那条命令与输出带出来）
func runNetProvCmds(cmds [][]string) error {
	for _, argv := range cmds {
		if err := runNetProvCmd(argv); err != nil {
			return fmt.Errorf("%s 失败：%v", netprov.WANCommandText(argv), err)
		}
	}
	return nil
}

// dhcpProtoStuck netifd 说这张接口没 up 而且还在 pending ⇒ 协议起不来。
//
// 链路上**没有 DHCP 服务**时就是它，哪怕网卡上已经有拨号器写进去的地址：
//
//	{"up":false,"pending":true,"proto":"dhcp","device":"wwan0","data":{}}
//
// 既然可能是个“永远起不来”的 proto（每次重建都跑一套 hotplug + procd，CPU 直接打满），
// 自动组网会数着这种轮数，够了就把 proto 降级成 none（见 qmiWantProto）。
func dhcpProtoStuck(iface string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), netProvTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ubus", "call", "network.interface."+iface, "status").Output()
	if err != nil {
		return false
	}
	var st struct {
		Up      bool `json:"up"`
		Pending bool `json:"pending"`
	}
	if json.Unmarshal(out, &st) != nil {
		return false
	}
	return !st.Up || st.Pending
}

// uciGet 读一个 UCI 值（不存在返回空串 + nil；命令本身失败才返回错误）
func uciGet(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), netProvTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "uci", "-q", "get", path).Output()
	if err != nil {
		// uci -q get 对不存在的键返回非 0；这属于"没有"，不是错误
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

// uciOptionNames 列出某个 section 上**已经存在**的选项名（不包含 section 名本身）。
//
// 用途：SwitchProtoCommands 改协议时要清掉旧协议特有的选项，而 `uci -q delete` 对不存在的键
// 也会返回 1（真机实测），所以必须先知道哪些真的在。
//
// 输入 `uci -q show network.modem0` 的输出，形如：
//
//	network.modem0=interface
//	network.modem0.proto='mbim'
//	network.modem0.device='/dev/cdc-wdm0'
//
// 读失败（section 不存在 / uci 报错）⇒ 返回 nil，调用方就当"没有可清的"。
func uciOptionNames(section string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), netProvTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "uci", "-q", "show", section).Output()
	if err != nil {
		return nil
	}
	prefix := section + "."
	var names []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := line[len(prefix):]
		if rest == "" || strings.ContainsAny(rest, "=.") {
			continue // section 自身那行 / 列表项 network.x.y... 之类
		}
		if !seen[rest] {
			seen[rest] = true
			names = append(names, rest)
		}
	}
	return names
}

// netdevAddrs 网卡当前的 IPv4 地址（逗号分隔；没有返回空串）
func netdevAddrs(name string) string {
	in, err := net.InterfaceByName(name)
	if err != nil {
		return ""
	}
	addrs, err := in.Addrs()
	if err != nil {
		return ""
	}
	var out []string
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.To4() == nil {
			continue
		}
		out = append(out, ipn.IP.String())
	}
	return strings.Join(out, ", ")
}

// netdevUp 网卡是否已 up（拿不到接口信息按"没起来"处理）
func netdevUp(name string) bool {
	in, err := net.InterfaceByName(name)
	if err != nil {
		return false
	}
	return in.Flags&net.FlagUp != 0
}
