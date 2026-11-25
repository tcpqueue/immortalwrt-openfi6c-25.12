//go:build !windows

package kernel

// usbDeviceAge 的回归测试。
//
// 背景（真机踩到）：模组由路由器 USB 供电，**路由器重启模组也跟着断电重启**。
// 5G 模组内部是一台小 Linux，开机后要 1~2 分钟才把 AT 守护进程拉起来；
// 这期间 ttyUSB 节点已经在、open 也不报错，但 AT 不回话。旧代码此时立刻探测，
// 全超时 → 全写进"非 AT 口"否定缓存 → 模组长时间认不出来（拨号失败、界面无模组）。
//
// 修复用 sysfs 设备目录的 mtime 当作"枚举时刻"来做预热等待，所以这里用
// 一棵假 sysfs 树（真目录 + 真符号链接 + 改过的 mtime）把它验证一遍。

import (
	"modemd/module/netdev"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeSysfs 搭一棵假 sysfs：<ttyRoot>/ttyUSB1/device -> .../usb1/1-1/1-1:2.3
// 返回 (ttyClassRoot, sysBusUSBRoot)。
func fakeSysfs(t *testing.T, usbDirMtime time.Time) (string, string) {
	t.Helper()
	root := t.TempDir()
	ttyRoot := filepath.Join(root, "class", "tty")
	busRoot := filepath.Join(root, "bus", "usb", "devices")

	// 接口目录（USBDevicePath 的 /(usb\d+)/([^/]+)/ 就靠它匹配）
	ifaceDir := filepath.Join(root, "devices", "platform", "usb1", "1-1", "1-1:2.3")
	if err := os.MkdirAll(ifaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// /sys/bus/usb/devices/1-1 = USB 设备目录，mtime 就是"枚举时刻"
	usbDir := filepath.Join(busRoot, "1-1")
	if err := os.MkdirAll(usbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(usbDir, usbDirMtime, usbDirMtime); err != nil {
		t.Fatal(err)
	}
	// /sys/class/tty/ttyUSB1/device -> 接口目录
	linkDir := filepath.Join(ttyRoot, "ttyUSB1")
	if err := os.MkdirAll(linkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ifaceDir, filepath.Join(linkDir, "device")); err != nil {
		t.Fatal(err)
	}
	return ttyRoot, busRoot
}

// TestUSBDeviceAge 老设备 age 大（照常探测）、刚枚举出来的 age 小（预热期不探）、
// 非 USB 串口返回 ok=false（调用方就不做预热等待）。
func TestUSBDeviceAge(t *testing.T) {
	oldTTY, oldBus := netdev.TtyClassRoot, netdev.SysBusUSBRoot
	defer func() { netdev.TtyClassRoot, netdev.SysBusUSBRoot = oldTTY, oldBus }()

	const warmup = 90 * time.Second

	// ① 设备是 5 分钟前枚举出来的：早过预热期，必须照常探测
	ttyRoot, busRoot := fakeSysfs(t, time.Now().Add(-5*time.Minute))
	netdev.TtyClassRoot, netdev.SysBusUSBRoot = ttyRoot, busRoot

	age, ok := netdev.UsbDeviceAge("/dev/ttyUSB1")
	if !ok {
		t.Fatal("usbDeviceAge: ok=false, want true")
	}
	if age < 4*time.Minute || age > 6*time.Minute {
		t.Fatalf("usbDeviceAge = %v, want ~5m", age)
	}
	if age < warmup {
		t.Fatalf("age %v < warmup %v：老设备不该判定为预热中", age, warmup)
	}

	// ② 设备 10 秒前才枚举出来（模组刚跟着路由器上电）：落在预热窗口内
	ttyRoot2, busRoot2 := fakeSysfs(t, time.Now().Add(-10*time.Second))
	netdev.TtyClassRoot, netdev.SysBusUSBRoot = ttyRoot2, busRoot2

	age, ok = netdev.UsbDeviceAge("/dev/ttyUSB1")
	if !ok {
		t.Fatal("usbDeviceAge: ok=false, want true")
	}
	if age > warmup {
		t.Fatalf("age %v > warmup %v：刚枚举出来的设备应判定为预热中", age, warmup)
	}

	// ③ 查不到对应 tty 的串口（主板自带）→ ok=false
	if _, ok := netdev.UsbDeviceAge("/dev/ttyS0"); ok {
		t.Fatal("非 USB 串口应返回 ok=false")
	}
	// ④ 设备路径解析不到 USB 段时也不能 panic
	if _, ok := netdev.UsbDeviceAge(""); ok {
		t.Fatal("空端口名应返回 ok=false")
	}
}
