package kernel

import (
	"modemd/module/usbnet"
	"testing"
)

// TestUSBnetFromHostDriver 主机侧网卡驱动 → USB 类型（读 sysfs 拿到的驱动名就靠它翻译）
func TestUSBnetFromHostDriver(t *testing.T) {
	cases := map[string]string{
		"qmi_wwan":       "0", // QMI（wwan0 最常见的就是它）
		"qmi_wwan_q":     "0", // 定制内核给驱动名加了后缀
		"gobinet":        "0",
		"cdc_mbim":       "2",
		"cdc_mbim_qcom":  "2",
		"cdc_ether":      "1",
		"cdc_subset":     "1",
		"rndis_host":     "3",
		"cdc_ncm":        "5",
		"huawei_cdc_ncm": "5",
		// 认不出的驱动不能瞎猜（返回空 = 保持"未识别"）
		"":            "",
		"r8152":       "",
		"e1000e":      "",
		"usbnet":      "",
		"option":      "",
		"cdc_acm_at":  "",
		"ttyUSB0_drv": "",
	}
	for driver, want := range cases {
		if got := usbnet.UsbnetFromHostDriver(driver); got != want {
			t.Errorf("驱动 %q → %q, want %q", driver, got, want)
		}
	}
}
