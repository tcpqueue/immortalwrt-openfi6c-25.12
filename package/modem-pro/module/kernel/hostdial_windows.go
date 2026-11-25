//go:build windows

package kernel

// Windows 上没有这套主机侧拨号器（那是 Linux 的 QMI 拨号器 quectel-CM）。
//
// 保留同名接口只是为了**跨平台编译**：SyncHostDial 在 Windows 上因为
// AutoWANEnabled() 恒为 false 而直接返回，真正被调到的只有 StopHostDials（main 里退出前）。

import (
	"os"
	"os/exec"
)

// hostDialPrepareCmd Windows 上不设进程组（用不到）
func hostDialPrepareCmd(cmd *exec.Cmd) {}

// hostDialTerminate Windows 上直接 Kill（没有 SIGTERM / SIGKILL 之分）
func hostDialTerminate(pid int) { hostDialKill(pid) }

// hostDialKill 杀掉进程
func hostDialKill(pid int) {
	if pid <= 0 {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
