package usbhotplug

// ====================== USB 热插拔：身份判定 / 事件 / 驱动补齐（跨平台逻辑） ======================
//
// 目标（用户在 OpenWrt 上的实际场景）：
//
//	插入 USB → ① 先判断“这是不是一个模组”（VID/PID 比对已知表，别去动无关设备的驱动）
//	         → ② Linux/OpenWrt 上如果它**没有**交出 /dev/ttyUSB*，主动加载 option 驱动并登记
//	              VID/PID（写 new_id），把串口“变”出来
//	         → ③ 交给原有的发现流程（分组 + AT 探测）找出 AT 口
//	拔出 → 拓扑指纹变化 → 原有流程关掉对应模组（Close），这里补一条“是哪台被拔了”的事件
//
// 平台差异在 usbhotplug_unix.go（sysfs + Modprobe）与 usbhotplug_windows.go（注册表，只报告）。
// 本文件只放**纯逻辑**：身份表、设备结构、事件环形缓冲、需要补驱动的判定。

import (
	"modemd/module/config"
	"modemd/module/hostnic"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---- 身份：哪些 USB 设备算“模组” ----

// ---- 设备与事件 ----

// ModemUSBDevice 系统里的一个“模组 USB 设备”（可能还没交出串口）。
type ModemUSBDevice struct {
	VID      string   `json:"vid"`
	PID      string   `json:"pid"`
	Family   string   `json:"family,omitempty"` // 厂商标识（命中身份表时）
	VendorCN string   `json:"vendor_cn,omitempty"`
	Product  string   `json:"product,omitempty"`
	Serial   string   `json:"serial,omitempty"`
	BusPath  string   `json:"bus_path,omitempty"` // 1-1（sysfs 里的拓扑路径 / 注册表实例）
	Classes  []string `json:"classes,omitempty"`
	// TTYs 这个设备当前交出的串口（Linux: /dev/ttyUSB3；Windows: COM5）
	TTYs []string `json:"ttys,omitempty"`
	// Drivers 各接口绑定的内核驱动名（Linux；Windows 为空）
	Drivers []string `json:"drivers,omitempty"`
	// HasSerialDriver 是否已经有接口绑在 usb-serial 驱动上（Windows 恒 true：有 TTYs 就算）
	HasSerialDriver bool `json:"has_serial_driver"`
	// Known 是否命中已知模组身份表
	Known bool `json:"known"`
	// BindNote 平台对“能不能补驱动”的说明（Windows 上说明要装厂商驱动）
	BindNote string `json:"bind_note,omitempty"`
}

// 事件类型
const (
	USBEventAdded   = "added"
	USBEventRemoved = "removed"
	USBEventBound   = "bound"   // 补了驱动（new_id）
	USBEventFailed  = "failed"  // 补驱动失败
	USBEventIgnored = "ignored" // 不是模组 / 不需要处理
)

// USBEvent 一条 USB 热插拔事件（给页面看“刚刚发生了什么”）。
type USBEvent struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Device  string    `json:"device"`             // "0e8d:7126"，未知时尽量给 VID/PID
	Model   string    `json:"model,omitempty"`    // 面向人：厂商 + 型号/串口
	Note    string    `json:"note,omitempty"`     // 说明（含失败的原始错误）
	ModemID *int      `json:"modem_id,omitempty"` // 关联到的模组（拔出时是“被移除的那个”）
	Ports   []string  `json:"ports,omitempty"`
}

const UsbEventKeep = 60

var (
	usbEventMu    sync.Mutex
	usbEventList  []USBEvent
	UsbAttemptSet = map[string]time.Time{} // 已经尝试补过驱动的设备（key=busPath|vid:pid），避免反复 Modprobe
)

// NoteUSBEvent 记一条热插拔事件（内存环形缓冲，最近 UsbEventKeep 条）。
func NoteUSBEvent(e USBEvent) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	usbEventMu.Lock()
	defer usbEventMu.Unlock()
	usbEventList = append(usbEventList, e)
	if len(usbEventList) > UsbEventKeep {
		usbEventList = usbEventList[len(usbEventList)-UsbEventKeep:]
	}
}

// USBEvents 最近的事件（新的在前）。
func USBEvents() []USBEvent {
	usbEventMu.Lock()
	defer usbEventMu.Unlock()
	out := make([]USBEvent, 0, len(usbEventList))
	for i := len(usbEventList) - 1; i >= 0; i-- {
		out = append(out, usbEventList[i])
	}
	return out
}

// ClearUSBEvents 清空事件（页面上的「清空」）。
func ClearUSBEvents() {
	usbEventMu.Lock()
	defer usbEventMu.Unlock()
	usbEventList = nil
}

// ShouldAttemptBind 这台设备要不要尝试补驱动。
//
// 只对“已知模组 + 一个串口都没有 + 没绑过 usb-serial 驱动”的设备动手；
// 同一个设备只尝试一次（成功/失败都记下来），免得每轮巡检都去 Modprobe。
func ShouldAttemptBind(d ModemUSBDevice, autobind bool) bool {
	if !autobind || !d.Known || len(d.TTYs) > 0 || d.HasSerialDriver {
		return false
	}
	key := d.BusPath + "|" + d.VID + ":" + d.PID
	if _, done := UsbAttemptSet[key]; done {
		return false
	}
	return true
}

// usbSerialPackageHint 没驱动时该装什么（OpenWrt 与桌面 Linux 说法不同）。
func USBSerialPackageHint() string {
	if hostnic.IsOpenWrt() {
		return "OpenWrt 上先装驱动：opkg update && opkg install kmod-usb-serial-option"
	}
	return "把内核的 usb-serial 模块装上（kmod-usb-serial-option / usbserial）"
}

// MarkBindAttempt 记下“这台已经试过了”（拔掉后由 PruneBindAttempts 清掉）
func MarkBindAttempt(d ModemUSBDevice) {
	UsbAttemptSet[d.BusPath+"|"+d.VID+":"+d.PID] = time.Now()
}

// PruneBindAttempts 设备拔掉后清掉它的记录（插回来要能再试一次）。
func PruneBindAttempts(devices []ModemUSBDevice) {
	alive := map[string]bool{}
	for _, d := range devices {
		alive[d.BusPath+"|"+d.VID+":"+d.PID] = true
	}
	for k := range UsbAttemptSet {
		if !alive[k] {
			delete(UsbAttemptSet, k)
		}
	}
}

// ModemUSBSnapshot 一次枚举的结果，供对比（谁插了/谁拔了）。
type ModemUSBSnapshot map[string]ModemUSBDevice // key = busPath（没有就 VID:PID）

// SnapshotModemUSB 把设备列表转成快照（key 用拓扑路径，最稳定）。
func SnapshotModemUSB(list []ModemUSBDevice) ModemUSBSnapshot {
	snap := make(ModemUSBSnapshot, len(list))
	for _, d := range list {
		snap[usbDeviceKey(d)] = d
	}
	return snap
}

func usbDeviceKey(d ModemUSBDevice) string {
	if d.BusPath != "" {
		return d.BusPath
	}
	return d.VID + ":" + d.PID
}

// DiffModemUSB 对比两次快照，返回新增与消失的设备（都按 key 排序，稳定输出）。
func DiffModemUSB(prev, next ModemUSBSnapshot) (added, removed []ModemUSBDevice) {
	for k, d := range next {
		if _, ok := prev[k]; !ok {
			added = append(added, d)
		}
	}
	for k, d := range prev {
		if _, ok := next[k]; !ok {
			removed = append(removed, d)
		}
	}
	sort.Slice(added, func(i, j int) bool { return usbDeviceKey(added[i]) < usbDeviceKey(added[j]) })
	sort.Slice(removed, func(i, j int) bool { return usbDeviceKey(removed[i]) < usbDeviceKey(removed[j]) })
	return added, removed
}

// USBDeviceLabel 给设备一个面向人的名字（事件日志/页面都用它）。
func USBDeviceLabel(d ModemUSBDevice) string {
	parts := []string{}
	if d.VendorCN != "" {
		parts = append(parts, d.VendorCN)
	} else if d.Known {
		parts = append(parts, "已知模组")
	} else {
		parts = append(parts, "未知 USB 设备")
	}
	name := strings.TrimSpace(d.Product)
	if name != "" {
		parts = append(parts, name)
	}
	parts = append(parts, d.VID+":"+d.PID)
	if len(d.TTYs) > 0 {
		parts = append(parts, strings.Join(d.TTYs, "/"))
	}
	return strings.Join(parts, " ")
}

// USBHotplugView 页面用的总视图。
type USBHotplugView struct {
	Platform string `json:"platform"` // windows / linux / openwrt
	// AutoBind 是否会自动补驱动（Linux 才有效）；来自配置
	AutoBind bool `json:"auto_bind"`
	// CanBind 当前平台能不能补驱动（Windows 上是 false，解释见 BindNote）
	CanBind  bool             `json:"can_bind"`
	BindNote string           `json:"bind_note,omitempty"`
	Devices  []ModemUSBDevice `json:"devices"`
	Events   []USBEvent       `json:"events"`
	// Advice 给用户的下一步提示（例如 OpenWrt 缺 kmod 时提示装哪个包）
	Advice string `json:"advice,omitempty"`
}

// USBHotplugInfo 组装视图（设备枚举失败也要能返回，页面得有东西看）。
func USBHotplugInfo() USBHotplugView {
	v := USBHotplugView{
		Platform: USBHotplugPlatform(),
		AutoBind: config.Cfg.Serial.USBAutoBind,
		CanBind:  USBHotplugCanBind(),
		BindNote: USBHotplugBindNote(),
	}
	if devs, err := ListModemUSBDevices(); err == nil {
		v.Devices = devs
		// 只在**能自动补驱动**的平台上提醒“插上了却没串口”：
		// Windows 的注册表里会留着“插过但已拔掉”的记录，按它提醒会误报。
		if USBHotplugCanBind() {
			for _, d := range devs {
				if d.Known && len(d.TTYs) == 0 && !d.HasSerialDriver {
					v.Advice = "有模组插上了但还没交出串口：" + USBSerialPackageHint()
					break
				}
			}
		}
	}
	v.Events = USBEvents()
	return v
}

// ---- 设备插拔的调度（扫串口之前跑一遍） ----

// UsbLastSnapshot 上一轮看到过的模组设备（对比用，得出"谁插了/谁拔了"）
var UsbLastSnapshot = ModemUSBSnapshot{}

// UsbSnapshotReady 是否已经建过基线。
// ⚠️ 进程刚起来时设备本来就在（注册表/sysfs 里早已列出），那不是“刚刚插入”——
// 不建基线的话，每次启动都会先报一堆“插入”事件，把真正的插拔淹没。
var UsbSnapshotReady = false
