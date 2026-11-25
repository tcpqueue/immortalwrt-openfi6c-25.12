//go:build !windows

package atport

// ====================== 命令口绑定：Linux / OpenWrt 侧的设备身份解析 ======================
//
// 把"这组串口来自哪台 USB 设备"解出来（sysfs：/sys/class/tty/<tty>/device → 往上找到
// 带 idVendor/idProduct 的那层）。绑定就是按这个 vid:pid 存的。
//
// ⚠️ 只做**只读**解析：不改驱动、不动 USB（补驱动那件事在 usbhotplug_unix.go）。

import (
	"modemd/module/config"
	"modemd/module/sysfs"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// sysfsTTYClass 串口在 sysfs 里的类目录（路径变量，单测可注入）
var sysfsTTYClass = "/sys/class/tty"

// ATPortBindSupported 本平台支持"按 VID:PID 绑定命令口"吗（Windows 上拿不到 sysfs）
func ATPortBindSupported() bool { return true }

// USBVIDPIDForTTY 从串口名反查它所属 USB 设备的 VID/PID（读不到 ⇒ ok=false）。
//
// 做法：/sys/class/tty/ttyUSB2/device 是个软链（指向 .../2-1/2-1:1.2/ttyUSB2），
// 先解到**接口层**，再一层层往上找到带 idVendor/idProduct 的**设备层**。
func USBVIDPIDForTTY(tty string) (vid, pid string, ok bool) {
	tty = strings.TrimSpace(filepath.Base(strings.TrimSpace(tty)))
	if tty == "" {
		return "", "", false
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(sysfsTTYClass, tty, "device"))
	if err != nil || strings.TrimSpace(dir) == "" {
		return "", "", false
	}
	// 最多往上找 8 层（接口 → 设备 → 父 hub…），够用又不会绕太远
	for i := 0; i < 8; i++ {
		v := strings.ToLower(sysfs.ReadSysfsID(filepath.Join(dir, "idVendor")))
		p := strings.ToLower(sysfs.ReadSysfsID(filepath.Join(dir, "idProduct")))
		if v != "" && p != "" {
			return v, p, true
		}
		parent := filepath.Dir(dir)
		if parent == dir || parent == "/" || parent == "." {
			break
		}
		dir = parent
	}
	return "", "", false
}

// USBVIDPIDForGroupTTYs 这组串口属于哪台 USB 设备（第一台能解出来的为准；同组本来就同设备）
func USBVIDPIDForGroupTTYs(group []string) (vid, pid string, ok bool) {
	for _, tty := range group {
		if vid, pid, ok = USBVIDPIDForTTY(tty); ok {
			return vid, pid, true
		}
	}
	return "", "", false
}

// BoundATPortForGroup 这一组串口的设备有没有绑定命令口。
//
// 返回：
//
// port != "" ⇒ 用这个口（调用方**直接采信、不再扫描**）
// why  != "" ⇒ 绑过但这个口不在本组串口里（退回自动探测，日志里说一声）
func BoundATPortForGroup(group []string) (port, why string) {
	if len(group) == 0 || !ATPortBindSupported() {
		return "", ""
	}
	vid, pid, ok := USBVIDPIDForGroupTTYs(group)
	if !ok {
		return "", ""
	}
	bound := ATPortBindOf(vid, pid)
	if bound == "" {
		return "", ""
	}
	if p, hit := BoundATPortPick(bound, group); hit {
		// 只在"换了一个口"时打一条（每轮巡检都打就把日志刷满了）
		if atPortBindAppliedPort(ATPortBindKey(vid, pid), p) {
			config.Logger.Info("at port bind applied: 命令口按 VID:PID 绑定直接使用（不扫描）",
				zap.String("device", ATPortBindKey(vid, pid)), zap.String("port", p))
		}
		return p, ""
	}
	if AtPortBindWarnAllowed(vid, pid, time.Now()) {
		config.Logger.Warn("at port bind ignored: bound port is not on this device",
			zap.String("device", ATPortBindKey(vid, pid)), zap.String("bound", bound),
			zap.Strings("ports", group))
	}
	return "", "绑定的 " + bound + " 不在这台设备的串口里（现在是 " + strings.Join(group, " ") + "）"
}
