package kernel

import (
	"modemd/module/fibocom"
	"modemd/module/sysfs"
	"modemd/module/usbnet"
	"os"
	"path/filepath"
	"testing"
)

// TestUSBTypeFillsLocalInfo 现场反馈的原样复现（两种）：
//
//	① 广和通报 `AT+GTUSBMODE: 32`（QMI），但页面「主机侧接口/驱动」是"未识别" ——
//	   因为拨号方式表是按移远取值做的 key，32 查不到；现在按协议名查。
//	② AT 一旦成功就跳过后面两条探测 → VID:PID 与「USB 设备」永远是 `-`；
//	   现在三条路都跑，AT 成功时照样补本地信息。
func TestUSBTypeFillsLocalInfo(t *testing.T) {
	initTestLogger()
	withATTimeout(t)

	// 假 sysfs：wwan0 ← qmi_wwan，USB 设备上有 VID/PID/产品名
	root := t.TempDir()
	netDir := filepath.Join(root, "wwan0")
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(netDir, "uevent"), "INTERFACE=wwan0\nDRIVER=qmi_wwan\n")
	if err := os.MkdirAll(filepath.Join(netDir, "device"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(netDir, "device", "idVendor"), "2cb7\n")
	write(filepath.Join(netDir, "device", "idProduct"), "0107\n")
	write(filepath.Join(netDir, "device", "product"), "Fibocom FM160-CN\n")

	oldRoot := sysfs.NetClassRoot
	oldNames := usbnet.HostIfaceNames
	sysfs.NetClassRoot = root
	usbnet.HostIfaceNames = func() []string { return []string{"wwan0"} }
	t.Cleanup(func() { sysfs.NetClassRoot = oldRoot; usbnet.HostIfaceNames = oldNames })

	// 广和通固件报 32（QMI）
	mod := &Modem{ID: 0, Vendor: VendorFibocom, Model: "FM160-CN", ModelName: "FM160-CN",
		ATPort: "COM97", cmdImpl: &fibocom.FibocomCommand{},
		writeChan: make(chan string, 16), readChan: make(chan string, 16)}
	fakeATServe(mod, func(cmd string) []string { return []string{"AT+GTUSBMODE?", "+GTUSBMODE: 32", "OK"} })

	info := mod.USBType(true)

	// ① 协议认出来 → 页面「主机侧接口 / 驱动」有值
	if info.Value != "32" || info.Kind != "qmi" {
		t.Errorf("value/kind = %q/%q, want 32/qmi", info.Value, info.Kind)
	}
	if info.Dial == nil {
		t.Fatal("应能查到拨号方式（否则「主机侧接口/驱动」又是未识别）")
	}
	if info.Dial.Protocol == "" || info.Dial.HostDriver == "" {
		t.Errorf("拨号方式缺 protocol/host_driver：%+v", *info.Dial)
	}
	if info.Dial.Kind != "qmi" {
		t.Errorf("拨号方式 kind = %q, want qmi", info.Dial.Kind)
	}

	// ② AT 成功时也要把本地信息补上：VID:PID、USB 设备、主机侧网卡
	if info.Descriptor == nil {
		t.Fatal("AT 成功时也要补 USB 描述（VID:PID 就是靠它）")
	}
	if info.Descriptor.VID != "2cb7" || info.Descriptor.PID != "0107" {
		t.Errorf("VID:PID = %s:%s, want 2cb7:0107", info.Descriptor.VID, info.Descriptor.PID)
	}
	if info.Descriptor.Product == "" {
		t.Error("应带上产品名（页面「USB 设备」那一格）")
	}
	if info.HostIface == nil || info.HostIface.Driver != "qmi_wwan" {
		t.Errorf("主机侧网卡没读到：%+v", info.HostIface)
	}
}
