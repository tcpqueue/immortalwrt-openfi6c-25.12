//go:build !windows

package kernel

import (
	"modemd/module/config"
	"modemd/module/netdev"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResetPCIDeviceFakeSysfs PCIe 软复位 = remove + rescan。
// 用假 sysfs 树把这两次写操作抓下来（与 USB 那套 TestReauthorizeUSBDeviceFakeSysfs 同一思路）：
// 真机上这两个文件一写就真的把模组摘下来了，不能拿真机做单测。
func TestResetPCIDeviceFakeSysfs(t *testing.T) {
	dir := t.TempDir()
	devDir := filepath.Join(dir, "bus", "pci", "devices", "0000-01-00.0")
	pcieMkdir(t, devDir)
	removePath := filepath.Join(devDir, "remove")
	pcieWriteFile(t, removePath, "")
	rescanPath := filepath.Join(dir, "bus", "pci", "rescan")
	pcieWriteFile(t, rescanPath, "")

	oldDev, oldRescan, oldSettle := netdev.PciDevicesRoot, netdev.PciRescanPath, netdev.PcieRemoveSettle
	netdev.PciDevicesRoot, netdev.PciRescanPath = filepath.Join(dir, "bus", "pci", "devices"), rescanPath
	netdev.PcieRemoveSettle = 0 // 单测不等 2 秒
	t.Cleanup(func() {
		netdev.PciDevicesRoot, netdev.PciRescanPath, netdev.PcieRemoveSettle = oldDev, oldRescan, oldSettle
	})

	if err := netdev.ResetPCIDevice("0000-01-00.0"); err != nil {
		t.Fatalf("软复位失败：%v", err)
	}
	if b, err := os.ReadFile(removePath); err != nil || strings.TrimSpace(string(b)) != "1" {
		t.Errorf("应先往 remove 写 1（读到 %q, err=%v）", string(b), err)
	}
	if b, err := os.ReadFile(rescanPath); err != nil || strings.TrimSpace(string(b)) != "1" {
		t.Errorf("再往 rescan 写 1（读到 %q, err=%v）", string(b), err)
	}
}

// TestResetPCIDeviceRejectsBadBDF 非法 BDF 必须拒（防路径穿越：bdf 会拼进 sysfs 路径）
func TestResetPCIDeviceRejectsBadBDF(t *testing.T) {
	bad := []string{"", "../../etc", "0000:01:00.0/../../..", "not-a-bdf", "0000:01:00.0; rm -rf /"}
	for _, b := range bad {
		if err := netdev.ResetPCIDevice(b); err == nil {
			t.Errorf("%q 应该被拒", b)
		}
	}
	// 合法写法（含 Windows 上测试用的退化写法）不该被判成非法
	for _, ok := range []string{"0000:01:00.0", "0000-01-00.0"} {
		if !netdev.LooksLikeBDF(ok) {
			t.Errorf("%q 应算合法 BDF", ok)
		}
	}
}

// TestBindPCIDriverMissingDriver 驱动没装时如实报错（不要假装绑上了）
func TestBindPCIDriverMissingDriver(t *testing.T) {
	dir := t.TempDir()
	old := netdev.PciDriversRoot
	netdev.PciDriversRoot = filepath.Join(dir, "drivers")
	t.Cleanup(func() { netdev.PciDriversRoot = old })

	err := netdev.BindPCIDriver("0000-01-00.0", "mhi_pci_generic")
	if err == nil {
		t.Fatalf("驱动没装时应该报错")
	}
	if !strings.Contains(err.Error(), "没装") {
		t.Errorf("错误信息应说清是驱动没装：%v", err)
	}
}

// TestEnsurePCIeModemDriversBindsUnbound 没绑驱动的模组要能自动绑上
// （对应 USB 那边的"自动补 ttyUSB 驱动"）。
func TestEnsurePCIeModemDriversBindsUnbound(t *testing.T) {
	withPCIeFakeSysfs(t, "", "17cb", "0b40")

	dir := t.TempDir()
	oldDrivers := netdev.PciDriversRoot
	oldBind := config.Cfg.Serial.USBAutoBind
	netdev.PciDriversRoot = filepath.Join(dir, "drivers")
	config.Cfg.Serial.USBAutoBind = true
	t.Cleanup(func() { netdev.PciDriversRoot, config.Cfg.Serial.USBAutoBind = oldDrivers, oldBind })

	// 装上 mhi_pci_generic 的 bind 文件（假的内核驱动接口）
	bindPath := filepath.Join(netdev.PciDriversRoot, "mhi_pci_generic", "bind")
	pcieWriteFile(t, bindPath, "")

	if !netdev.EnsurePCIeModemDrivers() {
		t.Fatalf("应该绑上一次（返回 true 表示有变化，发现流程据此重探）")
	}
	b, err := os.ReadFile(bindPath)
	if err != nil {
		t.Fatalf("读 bind 文件失败：%v", err)
	}
	if strings.TrimSpace(string(b)) != testBDF {
		t.Errorf("bind 文件里应写入 BDF %q，实际 %q", testBDF, strings.TrimSpace(string(b)))
	}

	// 关掉自动绑定时不动作
	config.Cfg.Serial.USBAutoBind = false
	if netdev.EnsurePCIeModemDrivers() {
		t.Errorf("[serial] usb_auto_bind=false 时不该去绑驱动")
	}
}

// TestPCIeCtrlDeviceForModem 自动组网/拨号器要拿"这台模组的控制口"：
// 有 QMI 优先 QMI（主机侧拨号器走它），没有才退 MBIM。
func TestPCIeCtrlDeviceForModem(t *testing.T) {
	withPCIeFakeSysfs(t, "mhi_pci_generic", "17cb", "0b40")

	mod := &Modem{ID: 0, ATPort: "/dev/mhi_DUN"}
	got := pcieCtrlDeviceForModem(mod)
	if got == "" {
		t.Fatalf("应认出一个控制口（假树里有 mhi_QMI0 / wwan0qmi0）")
	}
	if !strings.Contains(got, "qmi") {
		t.Errorf("有 QMI 时优先 QMI，实际 %q", got)
	}
}
