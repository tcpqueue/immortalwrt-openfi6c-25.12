//go:build !windows

package netdev

// ====================== PCIe 复位 / 绑定驱动（Linux / OpenWrt） ======================
//
// USB 模组卡死时我们有"USB 软重插"（写 /sys/bus/usb/devices/<dev>/authorized 0→1，
// 见 usbhotplug_unix.go）。PCIe 模组没有 authorized 这个开关，等价物是**在 PCI 层
// 把它摘下来再重新枚举**：
//
//	echo 1 > /sys/bus/pci/devices/<bdf>/remove     # 摘掉（驱动 unbind + 设备消失）
//	sleep 1~2s                                      # 模组内部要缓一下
//	echo 1 > /sys/bus/pci/rescan                   # 总线重新扫描 → 设备回来、驱动自动绑
//
// 这比"重启路由器"轻得多（不断模组的电，只重做 PCIe 链路枚举），现场实测是能救回
// 卡死在 MHI 初始化的模组的同一类手段（与 USB 那条路一一对应）。
//
// 另外提供"绑驱动"：设备在、但没绑 mhi_pci_generic / mtk_t7xx 时（内核模块后装、
// 或启动时顺序不对），把 BDF 写进驱动的 bind 文件即可 —— 对应 USB 那边的补 ttyUSB 驱动。

import (
	"fmt"
	"modemd/module/config"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// PcieRemoveSettle 摘下来之后等多久再 rescan（太快模组还没缓过来，会枚举失败）
var PcieRemoveSettle = 2 * time.Second

// PCIeResetSupported 本平台支不支持 PCIe 复位（Windows 上不支持）
func PCIeResetSupported() bool { return true }

// ResetPCIDevice PCIe 软复位：remove → 等一会儿 → rescan。失败如实返回错误。
func ResetPCIDevice(bdf string) error {
	bdf = strings.TrimSpace(bdf)
	if !LooksLikeBDF(bdf) {
		return fmt.Errorf("PCI 地址不合法：%q", bdf)
	}
	devDir := filepath.Join(PciDevicesRoot, bdf)
	if _, err := os.Stat(devDir); err != nil {
		return fmt.Errorf("没有这块 PCIe 设备（%s）：%v", bdf, err)
	}
	if err := writeSysfs(filepath.Join(devDir, "remove"), "1"); err != nil {
		return fmt.Errorf("摘除 %s 失败：%w", bdf, err)
	}
	config.Logger.Info("pcie device removed, waiting before rescan",
		zap.String("bdf", bdf), zap.Duration("settle", PcieRemoveSettle))
	time.Sleep(PcieRemoveSettle)
	if err := writeSysfs(PciRescanPath, "1"); err != nil {
		return fmt.Errorf("重新扫描 PCIe 总线失败（设备可能没回来，必要时重启路由器）：%w", err)
	}
	config.Logger.Info("pcie rescan done", zap.String("bdf", bdf))
	return nil
}

// BindPCIDriver 把某块 PCIe 设备绑到指定驱动（驱动目录里得有 bind 文件）
func BindPCIDriver(bdf, driver string) error {
	bdf = strings.TrimSpace(bdf)
	driver = strings.TrimSpace(driver)
	if !LooksLikeBDF(bdf) || driver == "" {
		return fmt.Errorf("参数不合法：%q / %q", bdf, driver)
	}
	bind := filepath.Join(PciDriversRoot, driver, "bind")
	if _, err := os.Stat(bind); err != nil {
		return fmt.Errorf("驱动 %s 没装（找不到 %s）", driver, bind)
	}
	if err := writeSysfs(bind, bdf); err != nil {
		return fmt.Errorf("绑定 %s → %s 失败：%w", bdf, driver, err)
	}
	config.Logger.Info("pcie driver bound", zap.String("bdf", bdf), zap.String("driver", driver))
	return nil
}

// EnsurePCIeModemDrivers 扫一遍 PCIe 模组，把"没绑驱动"的绑上。
//
// 与 USB 那边的 ensureModemUSBSerials 一一对应（那个负责把 ttyUSB 变出来），
// 这里负责让 mhi_pci_generic / mtk_t7xx 认领设备 —— 没它们就不会有 /dev/mhi_*、
// 不会有 wwan 口、也就没有 AT 与网卡。开了 [serial] usb_auto_bind 才动手（默认开）。
//
// 返回值 = "这一轮有没有变化"（发现流程据此清否定缓存并重探）。
func EnsurePCIeModemDrivers() bool {
	if !config.Cfg.Serial.USBAutoBind {
		return false
	}
	devs := ListPCIModemDevices()
	changed := false
	for _, d := range devs {
		if d.Bound {
			continue
		}
		for _, drv := range PcieModemDrivers {
			if err := BindPCIDriver(d.BDF, drv); err == nil {
				changed = true
				break
			}
		}
	}
	return changed
}

// writeSysfs 往 sysfs 写一个值（sysfs 多数文件只接受一次写，用 os.WriteFile 即可）
func writeSysfs(path, val string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(val); err != nil {
		return err
	}
	return nil
}
