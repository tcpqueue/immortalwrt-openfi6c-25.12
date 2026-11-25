package usbnet

import "strings"

// ====================== 从「主机侧网卡」反推 USB 类型 ======================
//
// 为什么需要这第三条路：识别 USB 类型原本只有两条 ——
//
//	① 问固件 `AT+QCFG="usbnet"`（只有部分固件认这条）
//	② 读 USB 描述符的接口类码（容器里没挂 sysfs、AT 口不是 USB 串口时就读不到）
//
// 两条都拿不到时，页面上就是「主机侧接口 未识别 / VID:PID -」。
// 而**主机上已经出现的那张网卡用的是什么驱动**，本身就是最直接的证据：
//
//	qmi_wwan → QMI、cdc_mbim → MBIM、cdc_ether → ECM、rndis_host → RNDIS、cdc_ncm → NCM
//
// 而且顺着 `/sys/class/net/<iface>/device` 往上还能找到 USB 设备目录，
// 顺手把 VID:PID 与产品名补上（就是页面那两个一直显示 `-` 的格子）。
//
// 平台差异：只有 Linux/OpenWrt 有 sysfs（见 usbnet_host_unix.go）；
// 其它平台返回 nil，行为与以前一致（不会乱猜）。

// hostDriverUSBnet 主机侧网卡**驱动名** → usbnet 取值（取值即 DialTypes 的 Value）
//
// 驱动名是内核给的、与厂商无关，所以可以据此断定协议：
// QMI 只有 qmi_wwan（或老 GobiNet）在管，MBIM 只有 cdc_mbim，ECM 只有 cdc_ether。
var hostDriverUSBnet = map[string]string{
	"qmi_wwan":       "0", // 高通 QMI（接口名通常就是 wwan0）
	"gobinet":        "0", // 老 GobiNet（QMI 类，某些高通固件用）
	"gobi":           "0",
	"cdc_mbim":       "2", // MBIM
	"cdc_ether":      "1", // ECM
	"cdc_subset":     "1", // ECM 的简化变体（少数老模组）
	"rndis_host":     "3", // RNDIS
	"cdc_ncm":        "5", // NCM
	"huawei_cdc_ncm": "5", // 华为的 NCM 变体
}

// HostIfaceHint 主机侧网卡探测结果（Linux 读 sysfs 才有值）
type HostIfaceHint struct {
	Iface   string   `json:"iface,omitempty"`
	Driver  string   `json:"driver,omitempty"`
	Value   string   `json:"value,omitempty"` // 据驱动推断出的 usbnet 取值
	VID     string   `json:"vid,omitempty"`
	PID     string   `json:"pid,omitempty"`
	Product string   `json:"product,omitempty"`
	Classes []string `json:"classes,omitempty"`
}

// UsbnetFromHostDriver 驱动名 → usbnet 取值（认不出返回空串 = 不猜）
func UsbnetFromHostDriver(driver string) string {
	d := strings.ToLower(strings.TrimSpace(driver))
	if d == "" {
		return ""
	}
	if v, ok := hostDriverUSBnet[d]; ok {
		return v
	}
	// 有些内核/定制固件给驱动名加后缀（如 qmi_wwan_q / cdc_mbim_qcom）
	switch {
	case strings.HasPrefix(d, "qmi_wwan"), strings.HasPrefix(d, "gobinet"):
		return "0"
	case strings.HasPrefix(d, "cdc_mbim"):
		return "2"
	case strings.HasPrefix(d, "cdc_ncm"), strings.HasSuffix(d, "_cdc_ncm"):
		return "5"
	case strings.HasPrefix(d, "cdc_ether"), strings.HasPrefix(d, "cdc_subset"):
		return "1"
	case strings.HasPrefix(d, "rndis"):
		return "3"
	}
	return ""
}
