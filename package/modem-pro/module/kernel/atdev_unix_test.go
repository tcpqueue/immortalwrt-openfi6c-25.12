//go:build !windows

package kernel

// AT 字符设备层（Linux / OpenWrt）的回归测试。
// 这一层的每一条都是拿真机事故换来的，别"顺手简化"。

import (
	"fmt"
	"modemd/module/netdev"
	"syscall"
	"testing"
)

// ⚠️⚠️ 真机事故回归（2026-10-05，中兴 F50 / 展锐 SIPC）：
// `/dev/stty_nr2` 在"暂无数据"时返回的是 **ENODATA（errno 61，"no data available"）**，
// **不是** EAGAIN。只认 EAGAIN 的后果是一连串的：ProbeAT 第一拍读到 ENODATA ⇒
// 被当致命错误立刻 break ⇒ 永远读不到那条 `OK` ⇒ **模组永远发现不了**
// （当时 nr1 兜底还在，只表现为"总是落到 1~4 秒的慢路"；nr1 一删就变成完全认不到模组）。
func TestIsWouldBlockCoversEnodata(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil 不是 would-block", nil, false},
		{"EAGAIN（标准 O_NONBLOCK 行为）", syscall.EAGAIN, true},
		{"EWOULDBLOCK", syscall.EWOULDBLOCK, true},
		{"ENODATA（展锐 SIPC 的“暂无数据”）", syscall.ENODATA, true},
		{"EIO 是真错误，不能当无数据", syscall.EIO, false},
		{"EINVAL 是真错误", syscall.EINVAL, false},
		{"关闭的 fd（EBADF）也不能当无数据", syscall.EBADF, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := netdev.IsWouldBlock(c.err); got != c.want {
				t.Errorf("isWouldBlock(%v) = %v，想要 %v", c.err, got, c.want)
			}
		})
	}

	// 上层可能把错误包一层（os.PathError / fmt.Errorf %w）⇒ 必须用 errors.Is 语义
	if !netdev.IsWouldBlock(fmt.Errorf("read /dev/stty_nr2: %w", syscall.ENODATA)) {
		t.Error("被包装过的 ENODATA 也必须认得（要用 errors.Is，不能用 ==）")
	}
}
