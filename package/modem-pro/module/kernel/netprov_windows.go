//go:build windows

package kernel

import (
	"time"

	"modemd/module/dial"
)

// Windows 上不做自动组网：
// 那边的"接口 + 防火墙"是 netsh / 驱动那套（而且 USB 网卡的 DHCP 由厂商驱动决定），
// 与 OpenWrt 的 UCI 完全不是一回事。配置开着也只是空操作，不会去动主机网络。

// AutoWANEnabled Windows 恒为 false
func AutoWANEnabled() bool { return false }

// AutoWANLoop Windows 上空转（保留同名接口，main 里不用分平台写）
func AutoWANLoop(mgr *ModemManager, every time.Duration) {}

// dialHostSideOnce Windows 上没有这套主机侧拨号器（那是 Linux 的 QMI 拨号器 quectel-CM），
// 保留同名接口只为跨平台编译；手动拨号走「请自行运行主机侧拨号器」的提示。
func dialHostSideOnce(mod *Modem, st *dial.DialState) (string, string) { return "", "" }

// cleanupAutoWANForModem Windows 上无事可做（没有 UCI 那套接口/防火墙）
func cleanupAutoWANForModem(id int) {}
