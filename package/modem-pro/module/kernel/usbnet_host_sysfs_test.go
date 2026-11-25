package kernel

import (
	"modemd/module/sysfs"
	"modemd/module/usbnet"
	"os"
	"path/filepath"
	"testing"
)

// TestReadHostIfaceHint 用假 sysfs 验证"主机侧网卡 → USB 类型 + VID:PID"这条路。
//
// 现场就是这种情况：`AT+QCFG="usbnet"` 不可用、USB 描述符读不到（页面上
// 「主机侧接口 未识别（本机：wwan0）/ VID:PID -」），但主机上那张 wwan0 是由
// qmi_wwan 拉起来的 —— 驱动名本身就说明了协议。
func TestReadHostIfaceHint(t *testing.T) {
	root := t.TempDir()
	netDir := filepath.Join(root, "wwan0")
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// uevent 里的 DRIVER=（内核最省事的告知方式）
	if err := os.WriteFile(filepath.Join(netDir, "uevent"), []byte(
		"DEVTYPE=wwan\nINTERFACE=wwan0\nIFINDEX=7\nDRIVER=qmi_wwan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// device → 假的 USB 设备目录（含 idVendor/idProduct/product 与两个接口）
	devDir := filepath.Join(netDir, "device")
	if err := os.MkdirAll(devDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 真机上 device 是指向 USB 接口的符号链接；假目录里直接把 USB 设备的内容放这儿，
	// 往上找 idVendor 的逻辑照样能跑到（EvalSymlinks 对普通目录也成立）。
	write(filepath.Join(devDir, "idVendor"), "2c7c\n")
	write(filepath.Join(devDir, "idProduct"), "0800\n")
	write(filepath.Join(devDir, "product"), "Quectel RM520N-GL\n")
	for _, sub := range []struct{ name, class string }{
		{"1-1.0", "ff"}, // 厂商自定义接口（AT/QMI）；真机上是 1-1:1.0
		{"1-1.2", "0a"}, // CDC 数据接口
	} {
		d := filepath.Join(devDir, sub.name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(d, "bInterfaceClass"), sub.class+"\n")
		write(filepath.Join(d, "bInterfaceSubClass"), "00\n")
		write(filepath.Join(d, "bInterfaceProtocol"), "00\n")
	}

	old := sysfs.NetClassRoot
	sysfs.NetClassRoot = root
	defer func() { sysfs.NetClassRoot = old }()

	h := usbnet.ReadHostIfaceHint("wwan0")
	if h == nil {
		t.Fatal("应识别出主机侧网卡")
	}
	if h.Driver != "qmi_wwan" {
		t.Errorf("驱动 = %q, want qmi_wwan", h.Driver)
	}
	if h.Value != "0" {
		t.Errorf("usbnet 取值 = %q, want \"0\"（QMI）", h.Value)
	}
	if h.VID != "2c7c" || h.PID != "0800" {
		t.Errorf("VID:PID = %s:%s, want 2c7c:0800（4 位十六进制要读全）", h.VID, h.PID)
	}
	if h.Product == "" {
		t.Error("应读到产品名")
	}
	if len(h.Classes) == 0 {
		t.Error("应读到接口类码（和 VID:PID 一起给页面显示）")
	}

	// 驱动读不到、也没有 USB 信息 → 不能瞎猜（返回 nil，页面保持"未识别"）
	root2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root2, "eth9"), 0o755); err != nil {
		t.Fatal(err)
	}
	write2 := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write2(filepath.Join(root2, "eth9", "uevent"), "INTERFACE=eth9\n")
	sysfs.NetClassRoot = root2
	if h := usbnet.ReadHostIfaceHint("eth9"); h != nil {
		t.Errorf("没有驱动也没有 USB 信息时不该返回结果，得到 %+v", h)
	}
	// 网卡不存在 → nil
	if h := usbnet.ReadHostIfaceHint("wwan7"); h != nil {
		t.Errorf("不存在的网卡应返回 nil，得到 %+v", h)
	}
}
