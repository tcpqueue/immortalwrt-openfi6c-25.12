//go:build !windows

package netdev

// ====================== 字符设备 AT 通道（Linux / OpenWrt） ======================
//
// MHI / wwan 的 AT 口是字符设备，不是 tty：**不能**走 termios（会 ENOTTY）。
// 这里用裸 open + O_NOCTTY（不要它当控制终端）+ O_NONBLOCK（避免 open 卡在等待载波，
// 也避免"模组还没就绪"时把发现流程堵死 —— 读超时由上层负责）。
//
// ⚠️ O_NONBLOCK 只在 open 那一刻有效；打开后我们**不清掉**它，
// 因为对这些通道来说"读不到就返回 EAGAIN"比"永久阻塞"更安全：
// readLoop / ProbeAT 本来就是"读到没数据就退出这一轮"的写法（见 modem.go / ports.go）。

import (
	"errors"
	"os"
	"strings"
	"syscall"
)

// openCharATDevice 裸打开一个字符设备（不设波特率、不碰 termios）
func openCharATDevice(name string) (AtDev, error) {
	name = strings.TrimSpace(name)
	fd, err := syscall.Open(name, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	return os.NewFile(uintptr(fd), name), nil
}

// IsWouldBlock 这个错误是不是"暂时没数据"（EAGAIN / EWOULDBLOCK / **ENODATA**）。
//
// 字符设备用 O_NONBLOCK 打开，没数据时 read 立刻返回 —— 它**不等于**
// "这个口不应答 AT"，所以判定"不是 AT 口"之前必须一直重试到 deadline（见 ProbeAT）。
//
// ⚠️⚠️ **展锐 SIPC（/dev/stty_nr*）返回的是 ENODATA（errno 61，“no data available”），
// 不是 EAGAIN**。2026-10-05 真机事故：只认 EAGAIN ⇒ 探测 nr2 时第一拍就读到 ENODATA，
// 被当成致命错误立刻 break ⇒ 永远读不到那条 `OK` ⇒ **模组永远发现不了**。
// （当时 nr1 兼底在，只表现为"总是走到慢路"；nr1 一删就变成"完全认不到模组"。）
//
// ENODATA 在 Linux 上的语义就是"没数据"（SIPC 驱动这么报），与 EAGAIN 等价，
// 当成"稍后再试"对**所有**字符设备都安全（真错误如 EIO/EINVAL 仍会熔断）。
func IsWouldBlock(err error) bool {
	if err == nil {
		return false
	}
	// errors.Is 而不是 ==：上层可能把它包了一层
	return errors.Is(err, syscall.EAGAIN) ||
		errors.Is(err, syscall.EWOULDBLOCK) ||
		errors.Is(err, syscall.ENODATA)
}
