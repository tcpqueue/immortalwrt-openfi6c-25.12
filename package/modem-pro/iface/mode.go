package iface

// ---- USB 网卡模式（AT+QCFG="usbnet"） ----

// Mode 一种 USB 网卡模式
//
// 清单口径下（型号在 `modem_catalog.json` 里）取值与展示名都来自 `usb_at`，
// 对应的完整切换指令见 `CFUNStatus.ModeCmds`（键就是这里的 Value）。
type Mode struct {
	Value string `json:"value"`          // 配置值，如 "2"
	Name  string `json:"name"`           // 展示名，如 "MBIM"
	Note  string `json:"note,omitempty"` // 说明
}
