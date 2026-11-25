package kernel

import (
	"modemd/module/usbhotplug"
	"strings"
	"time"

	"go.uber.org/zap"

	"modemd/module/config"
)

// ensureModemUSBSerials 在扫串口之前跑一遍：
//
//	① 枚举“像模组”的 USB 设备 → 与上轮对比，记“插入 / 拔出”事件（设备级，
//	   即使它一个串口都没交出来也能看见）；
//	② 对“插上了但没有串口”的已知模组，让平台层把 usb-serial 驱动补上
//	   （Linux/OpenWrt：modprobe + new_id；Windows：不支持，只给提示）。
//
// 每台设备只补一次（拔掉后记录清掉，插回来可以再试）。
//
// 返回 USB 层有没有变化（插入 / 拔出 / 补驱动）—— 调用方据此作废“非 AT 口”否定缓存、
// 重新探测（重插同一台模组时端口号可能没变，靠串口指纹看不出变化，只能靠这个信号）。
func ensureModemUSBSerials() bool {
	devs, err := usbhotplug.ListModemUSBDevices()
	if err != nil {
		return false
	}
	usbhotplug.PruneBindAttempts(devs)

	snap := usbhotplug.SnapshotModemUSB(devs)
	added, removed := usbhotplug.DiffModemUSB(usbhotplug.UsbLastSnapshot, snap)
	changed := false
	// 首次只建基线：启动时已经在的设备不是“刚插入”
	if usbhotplug.UsbSnapshotReady {
		for _, d := range added {
			logger.Info("usb modem plugged in",
				zap.String("device", d.VID+":"+d.PID), zap.String("path", d.BusPath),
				zap.Strings("ttys", d.TTYs))
			usbhotplug.NoteUSBEvent(usbhotplug.USBEvent{Kind: usbhotplug.USBEventAdded, Device: d.VID + ":" + d.PID,
				Model: usbhotplug.USBDeviceLabel(d), Ports: d.TTYs})
			changed = true
		}
		for _, d := range removed {
			logger.Info("usb modem unplugged",
				zap.String("device", d.VID+":"+d.PID), zap.String("path", d.BusPath),
				zap.Strings("ttys", d.TTYs))
			usbhotplug.NoteUSBEvent(usbhotplug.USBEvent{Kind: usbhotplug.USBEventRemoved, Device: d.VID + ":" + d.PID,
				Model: usbhotplug.USBDeviceLabel(d), Ports: d.TTYs})
			changed = true
		}
	}
	usbhotplug.UsbSnapshotReady = true
	usbhotplug.UsbLastSnapshot = snap

	autobind := config.Cfg.Serial.USBAutoBind && usbhotplug.USBHotplugCanBind()
	bonded := false
	for _, d := range devs {
		if !usbhotplug.ShouldAttemptBind(d, autobind) {
			continue
		}
		usbhotplug.MarkBindAttempt(d)
		note, err := usbhotplug.EnsureSerialTTY(d)
		if err != nil {
			logger.Warn("usb serial bind failed",
				zap.String("device", d.VID+":"+d.PID), zap.String("path", d.BusPath), zap.Error(err))
			usbhotplug.NoteUSBEvent(usbhotplug.USBEvent{Kind: usbhotplug.USBEventFailed, Device: d.VID + ":" + d.PID,
				Model: usbhotplug.USBDeviceLabel(d), Note: err.Error()})
			changed = true
			continue
		}
		logger.Info("usb serial driver bound",
			zap.String("device", d.VID+":"+d.PID), zap.String("note", note))
		usbhotplug.NoteUSBEvent(usbhotplug.USBEvent{Kind: usbhotplug.USBEventBound, Device: d.VID + ":" + d.PID,
			Model: usbhotplug.USBDeviceLabel(d), Note: note})
		changed = true
		bonded = true
	}
	if bonded {
		// 内核建 tty 要一点时间：等它出现，否则这一轮还是扫不到
		time.Sleep(usbBindSettleDelay)
	}
	// 有插拔 / 有驱动变化 → 推给页面：USB 分区实时刷新，并弹一条提示。
	// （模组列表本身的推送走 discoverAndStart 的拓扑指纹，两条路互不替代：
	//  “插上了但还没串口”的卡不会出现在模组列表里。）
	//
	// ⚠️ 用 pushDebounced 而不是直接 pushUSB()：调用方是 DiscoverModems，
	// 那里**持有 mgr.mu**，而 Hub.Broadcast 是同步写 WS（慢客户端会把锁拖住）。
	// debounce 走 time.AfterFunc，是异步的，而且能把插拔瞬间的密集检测合并成一次。
	if changed {
		pushDebounced("usb", 300*time.Millisecond, pushUSB)
	}
	return changed
}

// usbBindSettleDelay 补完驱动后等内核枚举出 tty 的时间（实测几十毫秒就够，留足余量）
const usbBindSettleDelay = 400 * time.Millisecond

// noteModemGone 记一条“模组被拔掉 / 消失了”的日志与事件。
//
// ⚠️ 必须在 mod.Close() **之前**调用：关掉之后型号 / 厂商这些就读不到了。
// 用户要的就是“知道是哪台被拨了”，所以事件里要带上 id / 厂商型号 / AT 口。
func (mgr *ModemManager) noteModemGone(mod *Modem, reason string) {
	vendor := mod.VendorName
	if vendor == "" {
		vendor = VendorLabel(mod.Vendor)
	}
	model := shortModelName(mod.Model)
	logger.Info("modem disappeared, closing",
		zap.Int("id", mod.ID), zap.String("at_port", mod.ATPort),
		zap.String("vendor", string(mod.Vendor)), zap.String("model", model),
		zap.String("reason", reason))
	id := mod.ID
	usbhotplug.NoteUSBEvent(usbhotplug.USBEvent{
		Kind: usbhotplug.USBEventRemoved, Device: mod.ATPort,
		Model:   strings.TrimSpace(vendor + " " + model),
		Note:    "模组已移除（" + reason + "）",
		ModemID: &id, Ports: mod.TTYList,
	})
	// 模组没了，它占的接口与防火墙条目也该消失（动态配置）。
	// 真拔了才删（内部会再确认网卡已不存在）；Windows 上是空操作。
	cleanupAutoWANForModem(id)
}
