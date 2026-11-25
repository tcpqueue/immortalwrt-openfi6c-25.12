package kernel

// ====================== USB 热插拔：身份判定 / 事件 / 驱动补齐的纯逻辑 ======================
//
// 这些用例钉住的是"别把无关设备当模组"（误判会去动它的驱动绑定）与"事件能说清是谁被拔了"。

import (
	"modemd/module/usbhotplug"
	"modemd/module/usbids"
	"strings"
	"testing"
	"time"
)

func TestIsKnownModemUSB(t *testing.T) {
	cases := []struct {
		name    string
		vid     string
		pid     string
		classes []string
		want    bool
		vendor  ModemVendor
	}{
		{"移远 RM500U（VID+PID 命中）", "2c7c", "0800", []string{"ff/ff/ff"}, true, VendorQuectel},
		{"广和通 FM350（MTK 平台）", "0e8d", "7126", []string{"ff/00/00"}, true, VendorFibocom},
		// FM650-CN（展锐）是广和通**自有 VID** 2cb7（实测 2cb7:0a06）—— 不是 MTK 的 0e8d
		{"广和通 FM650（自有 VID）", "2cb7", "0a06", nil, true, VendorFibocom},
		{"2cb7 但不在表里的 PID", "2cb7", "9999", nil, false, ""},
		// ⚠️ 关键回归：0e8d 是联发科通用 VID，手机也用 — 只认表里的 PID
		{"MTK 手机（同 VID 不同 PID）", "0e8d", "2000", []string{"ff/42/01"}, false, ""},
		{"鼎桥 TD Tech（实测本机 MT5700）", "3466", "3301", []string{"06/12/00"}, true, VendorTDTech},
		{"鼎桥 + 厂商自定义类码", "2deb", "1234", []string{"ff/ff/ff"}, false, ""}, // 未验证的 VID 不放行
		{"鼎桥 + MBIM 类码", "2deb", "1234", []string{"02/0e/00"}, false, ""},
		{"移远 EC25（PID 0127）", "2c7c", "0127", nil, true, VendorQuectel},
		// ⚠️ 2026-10-09 真机（H29K，RM500U-CNV）：USB 上就是 2c7c:0900，**展锐平台**。
		// 表里原来只有高通那批 PID ⇒ 它不算“已知模组”：USB 热插拔页没有它、
		// 「命令口绑定」的设备下拉是空的（而顶部模组列表里正常显示）。这两条守住这个回归。
		{"移远 RM500U-CNV（展锐 0900）", "2c7c", "0900", []string{"ff/ff/ff"}, true, VendorQuectel},
		{"移远 EC200U（展锐 0901）", "2c7c", "0901", []string{"ff/ff/ff"}, true, VendorQuectel},
		{"移远 MTK 平台（7001）", "2c7c", "7001", nil, true, VendorQuectel},
		// 但 2c7c 仍然只认表里的 PID：表外的一律不放行（保守口径不能因为补了 PID 就放开）
		{"2c7c 表外 PID 仍不放行", "2c7c", "9999", []string{"ff/ff/ff"}, false, ""},
		// 鼎桥 / 芯讯通的 VID 是厂商独占的：拿不到类码（Windows 上很常见）也得能认出来，
		// 否则插着模组却一直显示"没有模组 USB 设备"（实测踩到过）。
		{"鼎桥无类码", "3466", "3301", nil, true, VendorTDTech},
		{"芯讯通（VID 独占）", "1e0e", "9001", nil, true, VendorSimcom},
		// ⚠️ 代价：同 VID 的其它形态（模组自己的 U 盘模式）也会算模组 —— 这是故意的：
		// 那确实就是它，只是要 usb_modeswitch 切到串口模式才会出 ttyUSB。
		{"鼎桥 U 盘模式", "3466", "3301", []string{"08/06/50"}, true, VendorTDTech},
		{"未知 VID", "1234", "5678", []string{"ff/ff/ff"}, false, ""},
		{"大写 / 带前缀的写法", "VID_2C7C", "PID_0800", nil, true, VendorQuectel},
	}
	for _, c := range cases {
		fam, ok := usbids.IsKnownModemUSB(c.vid, c.pid, c.classes)
		if ok != c.want {
			t.Errorf("%s：命中=%v, want %v", c.name, ok, c.want)
			continue
		}
		if ok && fam.Vendor != c.vendor {
			t.Errorf("%s：厂商=%q, want %q", c.name, fam.Vendor, c.vendor)
		}
	}
}

func TestSplitUSBVidPid(t *testing.T) {
	cases := []struct{ in, vid, pid string }{
		{"VID_0E8D&PID_7126", "0e8d", "7126"},
		{"2c7c:0800", "2c7c", "0800"},
		{"2C7C", "2c7c", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		vid, pid := usbids.SplitUSBVidPid(c.in)
		if vid != c.vid || pid != c.pid {
			t.Errorf("usbids.SplitUSBVidPid(%q) = %q/%q, want %q/%q", c.in, vid, pid, c.vid, c.pid)
		}
	}
}

func TestShouldAttemptBind(t *testing.T) {
	base := usbhotplug.ModemUSBDevice{
		VID: "0e8d", PID: "7126", BusPath: "1-1.2", Known: true,
	}
	cases := []struct {
		name     string
		dev      usbhotplug.ModemUSBDevice
		autobind bool
		want     bool
	}{
		{"已知模组 + 没串口 + 没驱动 → 补", base, true, true},
		{"配置关了就不补", base, false, false},
		{"已经有串口 → 不动", usbhotplug.ModemUSBDevice{VID: "0e8d", PID: "7126", BusPath: "1-1.3", Known: true, TTYs: []string{"/dev/ttyUSB3"}}, true, false},
		{"已经绑过 usb-serial → 不动", usbhotplug.ModemUSBDevice{VID: "0e8d", PID: "7126", BusPath: "1-1.4", Known: true, HasSerialDriver: true}, true, false},
		{"不是已知模组 → 不动（别碰别人设备）", usbhotplug.ModemUSBDevice{VID: "1234", PID: "5678", BusPath: "1-1.5"}, true, false},
	}
	for _, c := range cases {
		key := c.dev.BusPath + "|" + c.dev.VID + ":" + c.dev.PID
		delete(usbhotplug.UsbAttemptSet, key)
		if got := usbhotplug.ShouldAttemptBind(c.dev, c.autobind); got != c.want {
			t.Errorf("%s：shouldAttemptBind=%v, want %v", c.name, got, c.want)
		}
	}
	// 同一个设备只试一次（否则每轮巡检都去 modprobe）
	delete(usbhotplug.UsbAttemptSet, "1-1.2|0e8d:7126")
	if !usbhotplug.ShouldAttemptBind(base, true) {
		t.Fatal("第一次应允许尝试")
	}
	usbhotplug.MarkBindAttempt(base)
	if usbhotplug.ShouldAttemptBind(base, true) {
		t.Error("同一个设备第二次不该再试")
	}
	// 拔掉后记录要清掉，插回来还能再试
	usbhotplug.PruneBindAttempts(nil)
	if !usbhotplug.ShouldAttemptBind(base, true) {
		t.Error("设备消失后应能重新尝试")
	}
	delete(usbhotplug.UsbAttemptSet, "1-1.2|0e8d:7126")
}

func TestDiffModemUSB(t *testing.T) {
	prev := usbhotplug.SnapshotModemUSB([]usbhotplug.ModemUSBDevice{
		{VID: "2c7c", PID: "0800", BusPath: "1-1", TTYs: []string{"/dev/ttyUSB0"}},
		{VID: "0e8d", PID: "7126", BusPath: "1-2"},
	})
	next := usbhotplug.SnapshotModemUSB([]usbhotplug.ModemUSBDevice{
		{VID: "2c7c", PID: "0800", BusPath: "1-1", TTYs: []string{"/dev/ttyUSB0"}},
		{VID: "2deb", PID: "1234", BusPath: "1-3", TTYs: []string{"/dev/ttyUSB4"}},
	})
	added, removed := usbhotplug.DiffModemUSB(prev, next)
	if len(added) != 1 || added[0].BusPath != "1-3" {
		t.Errorf("新增识别错：%+v", added)
	}
	if len(removed) != 1 || removed[0].BusPath != "1-2" {
		t.Errorf("移除识别错：%+v", removed)
	}
	// 没变化时两边都空
	if a, r := usbhotplug.DiffModemUSB(next, next); len(a) != 0 || len(r) != 0 {
		t.Errorf("无变化时不该有差异：added=%v removed=%v", a, r)
	}
}

func TestUSBDeviceLabel(t *testing.T) {
	known := usbhotplug.USBDeviceLabel(usbhotplug.ModemUSBDevice{
		VID: "0e8d", PID: "7126", Known: true, VendorCN: "广和通 Fibocom",
		Product: "FM350-GL", TTYs: []string{"/dev/ttyUSB0", "/dev/ttyUSB1"},
	})
	for _, want := range []string{"广和通", "FM350-GL", "0e8d:7126", "ttyUSB0"} {
		if !strings.Contains(known, want) {
			t.Errorf("标签应含 %q，实际 %q", want, known)
		}
	}
	if got := usbhotplug.USBDeviceLabel(usbhotplug.ModemUSBDevice{VID: "1234", PID: "5678"}); !strings.Contains(got, "未知") {
		t.Errorf("未知设备应标“未知”：%q", got)
	}
}

func TestUSBEventRing(t *testing.T) {
	usbhotplug.ClearUSBEvents()
	defer usbhotplug.ClearUSBEvents()

	for i := 0; i < usbhotplug.UsbEventKeep+5; i++ {
		usbhotplug.NoteUSBEvent(usbhotplug.USBEvent{Kind: usbhotplug.USBEventAdded, Device: "0e8d:7126", Note: string(rune('a' + i%26))})
	}
	evs := usbhotplug.USBEvents()
	if len(evs) != usbhotplug.UsbEventKeep {
		t.Fatalf("事件应只保留 %d 条，实际 %d", usbhotplug.UsbEventKeep, len(evs))
	}
	if !evs[0].At.After(evs[len(evs)-1].At) && evs[0].Note == evs[len(evs)-1].Note {
		t.Error("事件应按“新的在前”返回")
	}
	usbhotplug.ClearUSBEvents()
	if len(usbhotplug.USBEvents()) != 0 {
		t.Error("清空后不该还有事件")
	}
}

// "谁被拔了"：事件里要能带上是哪台模组（id / 厂商型号）
func TestNoteModemGoneEvent(t *testing.T) {
	usbhotplug.ClearUSBEvents()
	defer usbhotplug.ClearUSBEvents()

	mgr := &ModemManager{modems: map[int]*Modem{}}
	mod := &Modem{
		ID: 3, ATPort: "/dev/ttyUSB3",
		Vendor: VendorFibocom, VendorName: "广和通 Fibocom",
		Model:   "ATI|Manufacturer: Fibocom Wireless Inc.|Model: FM350-GL|OK",
		TTYList: []string{"/dev/ttyUSB3", "/dev/ttyUSB4"},
	}
	mgr.noteModemGone(mod, "at port gone")

	evs := usbhotplug.USBEvents()
	if len(evs) != 1 {
		t.Fatalf("应记录 1 条事件，实际 %d", len(evs))
	}
	e := evs[0]
	if e.Kind != usbhotplug.USBEventRemoved {
		t.Errorf("事件类型 = %q, want %q", e.Kind, usbhotplug.USBEventRemoved)
	}
	if e.ModemID == nil || *e.ModemID != 3 {
		t.Errorf("事件应带上模组 id=3：%+v", e.ModemID)
	}
	if !strings.Contains(e.Model, "FM350-GL") {
		t.Errorf("事件应带上型号：%q", e.Model)
	}
	if len(e.Ports) != 2 {
		t.Errorf("事件应带上该模组的串口：%v", e.Ports)
	}
	if e.At.IsZero() || time.Since(e.At) > time.Minute {
		t.Errorf("事件时间不对：%v", e.At)
	}
}
