//go:build !windows

package kernel

// Linux / OpenWrt 侧 USB 枚举的回归测试。
//
// 这两个 bug 都是在真机（Hlink H28K / RK3528 + 鼎桥 MT5700M-CN）上才暴露的，
// 表现是：模组插在路由器上、接口也都在 sysfs 里，但程序一个设备都认不出来 ——
//
//	① readSysfsHex 是给“一个字节”的属性写的（len>2 就返回空串），
//	   却被拿来读 idVendor/idProduct（4 位十六进制）→ 永远是空串；
//	② /sys/bus/usb/devices 下的条目全是**符号链接**，
//	   而 Go 的 DirEntry.IsDir() 对符号链接返回 false → !e.IsDir() 把所有设备都跳过了。
//
// 后来又发现同类的一处：拨号页走的是另一条路（USBDescriptor），它仍在用 readSysfsHex
// 读 4 位的 VID/PID → 页面上「VID:PID」永远显示 "-"（/api/usb 那条路修过了，所以两边不一致）。
//
// 所以这里用一棵“假 sysfs 树”（真目录 + 真符号链接）把整条枚举跑一遍。

import (
	"modemd/module/dial"
	"modemd/module/netdev"
	"modemd/module/sysfs"
	"modemd/module/usbdesc"
	"modemd/module/usbhotplug"
	"modemd/module/usbids"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFile 写一个假 sysfs 属性文件（内容按内核风格带换行）
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestReadSysfsID VID/PID 是 4 位十六进制，必须用 readSysfsID 读
// （readSysfsHex 只接受 1~2 位，拿它读 VID 会返回空串 —— 那就是当初枚举一直为空的根因之一）
func TestReadSysfsID(t *testing.T) {
	dir := t.TempDir()
	cases := []struct{ content, want string }{
		{"3466\n", "3466"}, // 鼎桥 MT5700M-CN
		{"3301\n", "3301"},
		{"2C7C\n", "2c7c"}, // 大写要归一
		{"0e8d", "0e8d"},   // 没有换行也要认
		{"zzzz\n", ""},     // 不是十六进制
		{"\n", ""},
	}
	for i, c := range cases {
		p := filepath.Join(dir, "v"+string(rune('a'+i)))
		writeFile(t, p, c.content)
		if got := sysfs.ReadSysfsID(p); got != c.want {
			t.Errorf("readSysfsID(%q) = %q, want %q", c.content, got, c.want)
		}
	}

	// 单字节属性走 readSysfsHex（补零成两位）：两者不能混用
	p1 := filepath.Join(dir, "cls")
	writeFile(t, p1, "2\n")
	if got := sysfs.ReadSysfsHex(p1); got != "02" {
		t.Errorf("readSysfsHex(\"2\") = %q, want 02", got)
	}
	p2 := filepath.Join(dir, "vid4")
	writeFile(t, p2, "3466\n")
	if got := sysfs.ReadSysfsHex(p2); got != "" {
		t.Errorf("readSysfsHex 读 4 位的 VID 应返回空串（这正是当初的 bug），got %q", got)
	}
}

// TestIsUSBDeviceEntry 接口目录（带冒号）不能被当成设备；
// 设备条目本身既可能是目录也可能是符号链接，所以判断里不能出现 IsDir()。
func TestIsUSBDeviceEntry(t *testing.T) {
	for _, name := range []string{"1-1", "2-1", "usb1", "3-1.2"} {
		if !usbhotplug.IsUSBDeviceEntry(name) {
			t.Errorf("isUSBDeviceEntry(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"1-1:1.0", "2-1:2.5", "1-0:1.0"} {
		if usbhotplug.IsUSBDeviceEntry(name) {
			t.Errorf("isUSBDeviceEntry(%q) = true, want false（接口目录）", name)
		}
	}
}

// TestListModemUSBDevicesFakeSysfs 用假 sysfs 树跑完整枚举：
// 顶层用**符号链接**指向真实设备目录（和 /sys/bus/usb/devices 一样），
// 设备里放 6 个接口（其中 2 个被 cdc_ncm 占用、4 个厂商自定义没人管）。
func TestListModemUSBDevicesFakeSysfs(t *testing.T) {
	root := t.TempDir()
	realDev := filepath.Join(root, "devices", "platform", "ff100000.usb", "usb2", "2-1")
	writeFile(t, filepath.Join(realDev, "idVendor"), "3466\n")
	writeFile(t, filepath.Join(realDev, "idProduct"), "3301\n")
	writeFile(t, filepath.Join(realDev, "product"), "TDTECH MT5700M-CN\n")
	writeFile(t, filepath.Join(realDev, "serial"), "57050M5222800737\n")
	// 接口：2.0/2.1 = CDC-NCM（有驱动），2.2~2.5 = 厂商自定义（没驱动，就是缺串口的那种）
	ifaces := []struct{ name, class, sub, proto, drv string }{
		{"2-1:2.0", "02", "0d", "00", "cdc_ncm"},
		{"2-1:2.1", "0a", "00", "01", "cdc_ncm"},
		{"2-1:2.2", "ff", "06", "13", ""},
		{"2-1:2.3", "ff", "06", "12", ""},
		{"2-1:2.4", "ff", "06", "1c", ""},
		{"2-1:2.5", "ff", "06", "14", ""},
	}
	for _, i := range ifaces {
		d := filepath.Join(realDev, i.name)
		writeFile(t, filepath.Join(d, "bInterfaceClass"), i.class+"\n")
		writeFile(t, filepath.Join(d, "bInterfaceSubClass"), i.sub+"\n")
		writeFile(t, filepath.Join(d, "bInterfaceProtocol"), i.proto+"\n")
		if i.drv != "" { // driver 是指向驱动的符号链接
			drvDir := filepath.Join(root, "bus", "usb", "drivers", i.drv)
			if err := os.MkdirAll(drvDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(drvDir, filepath.Join(d, "driver")); err != nil {
				t.Skipf("本机不允许建符号链接（Windows 需要开发者模式）: %v", err)
			}
		}
	}

	// 顶层：设备是符号链接（关键！）+ 一个接口链接 + 一个不带 idVendor 的根 hub
	sysRoot := filepath.Join(root, "bus", "usb", "devices")
	if err := os.MkdirAll(sysRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDev, filepath.Join(sysRoot, "2-1")); err != nil {
		t.Skipf("建符号链接失败: %v", err)
	}
	writeFile(t, filepath.Join(sysRoot, "usb2", "idVendor"), "") // 根 hub：读不到 VID
	writeFile(t, filepath.Join(root, "iface"), "x")

	// 串口侧也走假树：一个 /dev/ttyUSB0（在假设备的第 3 个接口下面），
	// /sys/class/tty/ttyUSB0/device 指向它 —— 和真 sysfs 一样，从那里往上能找到 idVendor。
	// 这样测试就完全不看真机 /dev 了（路由器上真插着模组时踩过这个坑）。
	fakeDev := filepath.Join(root, "dev")
	fakeTTY := filepath.Join(realDev, "2-1:2.2", "ttyUSB0")
	writeFile(t, fakeTTY, "")
	writeFile(t, filepath.Join(fakeDev, "ttyUSB0"), "")
	ttyClass := filepath.Join(root, "sys", "class", "tty")
	if err := os.MkdirAll(filepath.Join(ttyClass, "ttyUSB0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fakeTTY, filepath.Join(ttyClass, "ttyUSB0", "device")); err != nil {
		t.Skipf("建符号链接失败: %v", err)
	}

	oldRoot, oldDev, oldTty := usbhotplug.UsbDevicesRoot, sysfs.DevRoot, netdev.TtyClassRoot
	usbhotplug.UsbDevicesRoot, sysfs.DevRoot, netdev.TtyClassRoot = sysRoot, fakeDev, ttyClass
	defer func() { usbhotplug.UsbDevicesRoot, sysfs.DevRoot, netdev.TtyClassRoot = oldRoot, oldDev, oldTty }()

	devs, err := usbhotplug.ListModemUSBDevices()
	if err != nil {
		t.Fatalf("ListModemUSBDevices: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("期望认出 1 台模组，实际 %d：%+v", len(devs), devs)
	}
	d := devs[0]
	if d.VID != "3466" || d.PID != "3301" {
		t.Errorf("VID/PID = %s:%s, want 3466:3301", d.VID, d.PID)
	}
	if !d.Known || d.Family != string(VendorTDTech) {
		t.Errorf("应命中鼎桥身份表：Known=%v family=%q", d.Known, d.Family)
	}
	if d.BusPath != "2-1" {
		t.Errorf("BusPath = %q, want 2-1", d.BusPath)
	}
	if d.Serial != "57050M5222800737" {
		t.Errorf("Serial = %q", d.Serial)
	}
	if len(d.Classes) != 6 {
		t.Errorf("应读到 6 个接口类码，实际 %d: %v", len(d.Classes), d.Classes)
	}
	if !contains(d.Drivers, "cdc_ncm") {
		t.Errorf("Drivers 应含 cdc_ncm，实际 %v", d.Drivers)
	}
	// 这个假设备已经有串口了（ttyUSB0）→ 不该再去补驱动
	if len(d.TTYs) != 1 || !d.HasSerialDriver {
		t.Errorf("假 tty 应归到设备上：TTYs=%v HasSerialDriver=%v", d.TTYs, d.HasSerialDriver)
	}
	if usbhotplug.ShouldAttemptBind(d, true) {
		t.Error("已经有串口的设备不该再补驱动")
	}

	// “该补驱动”的判定本身（已知模组 + 一个串口都没有）—— 用一台合成设备测，
	// 不受真机 /dev 影响。
	if !usbhotplug.ShouldAttemptBind(usbhotplug.ModemUSBDevice{VID: "3466", PID: "3301", BusPath: "9-9", Known: true}, true) {
		t.Error("已知模组但没有串口，应该尝试补驱动（new_id）")
	}
	if usbhotplug.ShouldAttemptBind(usbhotplug.ModemUSBDevice{VID: "3466", PID: "3301", BusPath: "9-8", Known: false}, true) {
		t.Error("不是已知模组就不该动它的驱动")
	}
}

// TestReauthorizeUSBDeviceFakeSysfs USB 软重插只做一件事：把 authorized 写 0、等一下、再写 1。
// 用假 sysfs 树验证「路径校验 + 写 0 + 写 1」这条动作链（不碰真设备）。
func TestReauthorizeUSBDeviceFakeSysfs(t *testing.T) {
	root := t.TempDir()
	auth := filepath.Join(root, "2-1", "authorized")
	writeFile(t, auth, "1\n")

	old := usbhotplug.UsbDevicesRoot
	usbhotplug.UsbDevicesRoot = root
	defer func() { usbhotplug.UsbDevicesRoot = old }()

	// 非法路径必须先被挡下来（绝不能拿它去拼 sysfs 路径）
	// ⚠️ 前/后空白不算非法：sysfs 读出来的字符串常带换行，函数内部会 TrimSpace。
	for _, bad := range []string{"", "  ", "/", "2-1/..", "2 1", "../2-1", "2-1:2.0/x"} {
		if err := usbhotplug.ReauthorizeUSBDevice(bad); err == nil {
			t.Errorf("ReauthorizeUSBDevice(%q) 应该报错（路径不合法）", bad)
		}
	}
	// 设备上没有 authorized 属性也要报错，而不是默默成功
	if err := usbhotplug.ReauthorizeUSBDevice("9-9"); err == nil {
		t.Error("不存在的设备应该报错")
	}

	// 带前后空白的合法路径要能用（只有一个合法调用 → 只睡一次 usbReauthorizeGap）
	if err := usbhotplug.ReauthorizeUSBDevice(" 2-1 "); err != nil {
		t.Fatalf("软重插失败：%v", err)
	}
	b, err := os.ReadFile(auth)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != "1" {
		t.Errorf("软重插后 authorized = %q, want \"1\"（必须恢复成 1，否则设备就永远掉了）", got)
	}
	if usbhotplug.USBRescueSupported() != true {
		t.Error("Linux 上 USBRescueSupported() 应该是 true")
	}
	if usbhotplug.UsbReauthorizeGap <= 0 || usbhotplug.UsbReauthorizeGap > 5*time.Second {
		t.Errorf("usbReauthorizeGap=%v，太长会把扫描循环堵住", usbhotplug.UsbReauthorizeGap)
	}
}

// usbDevStamp 读 sysfs 设备目录的 mtime = **内核枚举该设备的时刻**
// （与 `[serial] probe_warmup` 用的是同一个信号）。
//
// ⚠️ 别用 `devnum` 当判据：它是每个总线依次分配的，**设备被摘掉后那个号会被复用回来** ——
// 单设备总线（这条 2-1）上重枚举前后都是 2，看起来像“没变”（真机实测被骗过：
// 明明 dmesg 里整套重新注册了，用例却报“devnum 还是 2”）。目录 mtime 则一定变新（旧目录删了重建）。
func usbDevStamp(busPath string) time.Time {
	st, err := os.Stat(filepath.Join(usbhotplug.UsbDevicesRoot, busPath))
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// rescueTestEnabled 真机用例的开关：默认跳过（会打断模组约 1 分钟），
// 要跑就 MODEMD_USB_RESCUE_TEST=1 ./kernel.test -test.run 'USB.*Real|RescueStuck' -test.v
func rescueTestEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("MODEMD_USB_RESCUE_TEST") != "1" {
		t.Skip("默认跳过：需要真机 + 会打断模组，设 MODEMD_USB_RESCUE_TEST=1 才跑")
	}
}

// firstKnownModem 取场上第一台"已知模组"设备（真机用例的目标）
func firstKnownModem(t *testing.T) usbhotplug.ModemUSBDevice {
	t.Helper()
	devs, err := usbhotplug.ListModemUSBDevices()
	if err != nil {
		t.Fatalf("枚举 USB 设备失败：%v", err)
	}
	for _, d := range devs {
		if d.Known {
			return d
		}
	}
	t.Skipf("场上没有已知模组设备（%d 个 USB 设备），跳过", len(devs))
	return usbhotplug.ModemUSBDevice{}
}

// waitUSBReenumerated 等设备重新枚举回来。
// 判据：**枚举时刻变新**，或者串口编号变了（重枚举后编号常常会变）。
// 只看“设备在不在”是不够的 —— 写 authorized 之前它本来就在。
func waitUSBReenumerated(t *testing.T, busPath string, stampBefore time.Time, ttyBefore string) {
	t.Helper()
	const limit = 60 * time.Second
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if devs, err := usbhotplug.ListModemUSBDevices(); err == nil {
			for _, d := range devs {
				after := usbDevStamp(busPath)
				ttys := strings.Join(d.TTYs, ",")
				if d.BusPath == busPath && d.Known &&
					(after.After(stampBefore) || (ttys != "" && ttys != ttyBefore)) {
					t.Logf("模组回来了：bus=%s %s:%s tty=%v（枚举 %v → %v，用时 %v）",
						d.BusPath, d.VID, d.PID, d.TTYs,
						stampBefore.Format("15:04:05"), after.Format("15:04:05"),
						(limit - time.Until(deadline)).Truncate(time.Second))
					return
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("软重插后 %s 在 %v 内没有重新枚举回来（枚举时刻仍是 %v、串口仍为 %q）——真机上要么不支持 authorized，要么模组彻底卡死",
		busPath, limit, stampBefore.Format("15:04:05"), ttyBefore)
}

// TestReauthorizeUSBDeviceRealHardware 真机验证（默认跳过）：直接做一次 USB 软重插，
// 验证「模组冷启动后 AT 不应答，软重插能救回来」这条路的**动作**部分。
func TestReauthorizeUSBDeviceRealHardware(t *testing.T) {
	rescueTestEnabled(t)
	target := firstKnownModem(t)
	t.Logf("对模组 %s（%s:%s bus=%s）做软重插…", target.Product, target.VID, target.PID, target.BusPath)
	stampBefore, ttyBefore := usbDevStamp(target.BusPath), strings.Join(target.TTYs, ",")
	if err := usbhotplug.ReauthorizeUSBDevice(target.BusPath); err != nil {
		t.Fatalf("软重插失败：%v", err)
	}
	waitUSBReenumerated(t, target.BusPath, stampBefore, ttyBefore)
}

// TestRescueStuckModemUSBE2E 真机端到端验证（默认跳过）：走**扫描循环真正会走的那条路**
// （ModemManager.rescueStuckModemUSB），把"已经 150 秒没有 AT 口"这个条件造出来，
// 看它会不会自己动手软重插、并把记账（次数/否定缓存/计时）更新对。
func TestRescueStuckModemUSBE2E(t *testing.T) {
	rescueTestEnabled(t)
	target := firstKnownModem(t)
	stampBefore, ttyBefore := usbDevStamp(target.BusPath), strings.Join(target.TTYs, ",")

	mgr := &ModemManager{}
	mgr.noATSince = time.Now().Add(-10 * time.Minute) // 假装已经超时
	mgr.nonATPorts = map[string]nonATRecord{"/dev/ttyUSB0": {Next: time.Now().Add(time.Hour), Tries: 3}}
	if !mgr.rescueStuckModemUSB() {
		t.Fatalf("条件都满足了却没触发救援（known=%s bus=%s）", target.Product, target.BusPath)
	}
	if mgr.usbRescueCount != 1 {
		t.Errorf("救援次数 = %d, want 1", mgr.usbRescueCount)
	}
	if mgr.lastUSBRescue.IsZero() {
		t.Error("lastUSBRescue 没记上（冷却会失效，会变成连续重插）")
	}
	if !mgr.noATSince.After(time.Now().Add(-time.Minute)) {
		t.Errorf("noATSince 没重新计时（%v），下一次会立刻又触发", mgr.noATSince)
	}
	if len(mgr.nonATPorts) != 0 {
		t.Errorf("否定缓存没清空（%v）—— 重插后串口名会变，旧缓存会阻止重新探测", mgr.nonATPorts)
	}
	// 冷却期内不许再动手（否则模组会被反复拔插，永远起不来）
	if mgr.rescueStuckModemUSB() {
		t.Error("刚救过一次，冷却期（5 分钟）内不该再救")
	}
	t.Logf("触发成功，等模组 %s 回来…", target.BusPath)
	waitUSBReenumerated(t, target.BusPath, stampBefore, ttyBefore)
}

// TestUSBDescriptorFakeSysfs 拨号页「模块 USB 类型」卡片 = USBDescriptor（AT 口 → sysfs）。
// ⚠️ 它和 ListModemUSBDevices 是两条独立的路：当初只修了后者，前者还在用 readSysfsHex
// 读 4 位的 idVendor/idProduct，于是路由器上「VID:PID」一直显示 "-"（实测就是这样）。
func TestUSBDescriptorFakeSysfs(t *testing.T) {
	root := t.TempDir()
	dev := filepath.Join(root, "devices", "platform", "ff100000.usb", "usb2", "2-1")
	writeFile(t, filepath.Join(dev, "idVendor"), "3466\n")
	writeFile(t, filepath.Join(dev, "idProduct"), "3301\n")
	writeFile(t, filepath.Join(dev, "product"), "TDTECH MT5700M-CN\n")
	// 真机实测：2.0 = 02/0d/00（NCM 控制口）、2.1 = 0a/00/01（数据口）
	for _, i := range []struct{ name, class, sub, proto string }{
		{"2-1:2.0", "02", "0d", "00"},
		{"2-1:2.1", "0a", "00", "01"},
	} {
		d := filepath.Join(dev, i.name)
		writeFile(t, filepath.Join(d, "bInterfaceClass"), i.class+"\n")
		writeFile(t, filepath.Join(d, "bInterfaceSubClass"), i.sub+"\n")
		writeFile(t, filepath.Join(d, "bInterfaceProtocol"), i.proto+"\n")
	}
	// /sys/class/tty/ttyUSB1/device → 设备下某个接口里的 ttyUSB1（和真 sysfs 一样是符号链接）
	tty := filepath.Join(dev, "2-1:2.2", "ttyUSB1")
	writeFile(t, tty, "")
	ttyClass := filepath.Join(root, "sys", "class", "tty")
	if err := os.MkdirAll(filepath.Join(ttyClass, "ttyUSB1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(tty, filepath.Join(ttyClass, "ttyUSB1", "device")); err != nil {
		t.Skipf("建符号链接失败: %v", err)
	}
	old := netdev.TtyClassRoot
	netdev.TtyClassRoot = ttyClass
	defer func() { netdev.TtyClassRoot = old }()

	d := usbdesc.USBDescriptor("/dev/ttyUSB1")
	if d == nil {
		t.Fatal("USBDescriptor 不该是 nil（读不到 VID 就算了，但类码该读到）")
	}
	if d.VID != "3466" || d.PID != "3301" {
		t.Errorf("VID:PID = %s:%s, want 3466:3301（4 位十六进制要用 readSysfsID 读）", d.VID, d.PID)
	}
	if d.Product != "TDTECH MT5700M-CN" {
		t.Errorf("Product = %q", d.Product)
	}
	want := []string{"02/0d/00", "0a/00/01"}
	if len(d.Classes) != len(want) || d.Classes[0] != want[0] || d.Classes[1] != want[1] {
		t.Errorf("Classes = %v, want %v", d.Classes, want)
	}
	// 端到端：这组类码要能认出 NCM（取值 5），并且标成人话时控制口/数据口不能反
	if v, ok := usbids.ClassifyUSBClasses(d.Classes); !ok || v != "5" {
		t.Errorf("该认出 NCM（取值 5），得到 %q(ok=%v)", v, ok)
	}
	if got := dial.ClassSummary("02/0d/00"); got != "02/0d/00 CDC-NCM 控制接口" {
		t.Errorf("控制口标注 = %q", got)
	}
	if got := dial.ClassSummary("0a/00/01"); got == "0a/00/01 CDC-NCM" {
		t.Errorf("数据口 0a 不能说是 NCM：%q", got)
	}
}
