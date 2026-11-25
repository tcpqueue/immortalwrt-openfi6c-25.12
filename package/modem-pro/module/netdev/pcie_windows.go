//go:build windows

package netdev

import "fmt"

// PCIeResetSupported Windows 上不支持（PCIe 模组在 Windows 上由厂商驱动管理，
// 复位要走设备管理器/厂商工具，不是我们能写 sysfs 的事）。
func PCIeResetSupported() bool { return false }

// ResetPCIDevice Windows 上如实报错，不做假动作。
func ResetPCIDevice(bdf string) error {
	return fmt.Errorf("Windows 上不支持 PCIe 软复位（%s）：请在设备管理器里禁用/启用设备", bdf)
}

// BindPCIDriver Windows 上由驱动安装程序负责。
func BindPCIDriver(bdf, driver string) error {
	return fmt.Errorf("Windows 上不支持手动绑定 PCIe 驱动（%s → %s）", bdf, driver)
}

// EnsurePCIeModemDrivers 非 Linux 平台没有 sysfs，恒无变化。
func EnsurePCIeModemDrivers() bool { return false }
