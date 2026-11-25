package kernel

import (
	"modemd/module/usbdialect"
	"strings"
)

// ====================== USB 网卡模式（AT+QCFG="usbnet"） ======================

// Mode 已搬到 modemd/iface（见 iface/mode.go）——厂商的 USB 模式方言也要用它，
// 不能让这个 DTO 住在被依赖的引擎包里。kernel 保留同名别名，见 iface_alias.go。

// CFUNStatus 当前 USB 网卡模式 + 可选列表
type CFUNStatus struct {
	Value      string `json:"value"`                 // 当前配置值
	Name       string `json:"name"`                  // 当前模式名（表外取值为空）
	Modes      []Mode `json:"modes"`                 // 可选模式
	NeedReboot bool   `json:"need_reboot"`           // 需要手动重启模组
	AutoReboot bool   `json:"auto_reboot,omitempty"` // 模组在接受模式切换后自行重启
	// BlockedModes 只展示、不允许执行的取值 → 原因（界面把这一档置灰并写明原因）。
	// 现场由来：广和通切到 RNDIS 后 USB 组成里**没有 AT 口**（实测 FM160-CN），
	// 页面上的 AT 功能会一起失联 —— 见 vendor_settings_usb.go 的 usbnetBlockedModes。
	BlockedModes map[string]string `json:"blocked_modes,omitempty"`
	// 下面三项是方言化的补充信息（不同厂商指令/取值/生效方式都不一样）
	Cmd      string            `json:"cmd,omitempty"`       // 本机实际使用的查询指令（现场核对用）
	ModeCmds map[string]string `json:"mode_cmds,omitempty"` // 取值 → 该档的完整切换指令（清单口径，界面展示用）
	Note     string            `json:"note,omitempty"`      // 本机说明（取值含义 / 需要重启 / 证据来源）
}

// ModeName 取值 → 展示名（表外返回空串）
func ModeName(value string) string {
	return modeNameIn(usbdialect.Modes(), value)
}

// modeNameIn 在给定取值表里找展示名（各家取值表不同，所以不能只用通用表）
func modeNameIn(modes []Mode, value string) string {
	for _, m := range modes {
		if strings.EqualFold(m.Value, value) {
			return m.Name
		}
	}
	return ""
}

// 模组上报：+QCFG: "usbnet",0
// reQCFGUsbnet 已随 ParseUSBnet 搬到 modemd/module/usbdialect（见 module/usbdialect/modes.go）。
