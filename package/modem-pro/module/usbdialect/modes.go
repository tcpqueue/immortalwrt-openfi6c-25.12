package usbdialect

// ====================== USB 网卡的通用码表与应答解析 ======================
//
// 从 module/kernel/net.go 搬过来：**纯数据 + 纯解析**，只依赖 mode 与 strings/regexp。

import "regexp"

// reQCFGUsbnet 解析 `+QCFG: "usbnet",<n>`
var reQCFGUsbnet = regexp.MustCompile(`\+QCFG:\s*"usbnet"\s*,\s*(\d+)`)

// Modes 返回文档列出的常用取值（顺序按数值，前端会自动选中当前值）
func Modes() []Mode {
	return []Mode{
		{Value: "0", Name: "QMI/NDIS", Note: "高通原生协议，需专用驱动（多数 5G 模组出厂默认）"},
		{Value: "1", Name: "ECM", Note: "USB 以太网，Linux 免驱常用"},
		{Value: "2", Name: "MBIM", Note: "微软标准，Windows 推荐、稳定性好"},
		{Value: "3", Name: "RNDIS", Note: "经典 USB 网卡，兼容性广但性能一般"},
		{Value: "5", Name: "NCM", Note: "高速 USB 网络，Linux 常用、带宽更高"},
	}
}

// ParseUSBnet 从应答里取 usbnet 值
func ParseUSBnet(resp []string) (string, bool) {
	for _, line := range resp {
		if m := reQCFGUsbnet.FindStringSubmatch(line); m != nil {
			return m[1], true
		}
	}
	return "", false
}
