//go:build windows

package usbhotplug

// ====================== USB 热插拔（Windows：注册表枚举，只报告不绑驱动） ======================
//
// Windows 上"插上了但没 COM 口"的原因几乎都是**没装厂商/系统驱动**（设备管理器里带黄叹号），
// 这一步没法由程序代劳（要 inf 安装/管理员操作），所以这里只做两件事：
//
//	① 枚举注册表里命中已知模组的 USB 设备 → 页面上能看见"插上了但还没串口"；
//	② 把 COM 口归属到设备（有口的正常参与发现流程）。
//
// 插拔检测本身与平台无关：设备级快照对比（usbhotplug.go）+ 串口集合指纹（modem.go）。
//
// ⚠️ COM 口归属用**设备实例段前缀**匹配，不要按 "VID_x&PID_y" 拼 key：
// 实测鼎桥（TD Tech）的接口键写成 `VID_3466&SUBCLASS_06&PROT_12`（没有 PID 段），
// USBDevicePath 返回的就是这种"接口键\实例"，按 VID:PID 拼会一个口都对不上。

import (
	"fmt"
	"modemd/iface"
	"modemd/module/dial"
	"modemd/module/netdev"
	"modemd/module/usbdesc"
	"modemd/module/usbids"
	"sort"
	"strings"
)

// USBHotplugPlatform 平台标识
func USBHotplugPlatform() string { return "windows" }

// USBHotplugCanBind Windows 不能自动补驱动
func USBHotplugCanBind() bool { return false }

// USBRescueSupported Windows 上没有 sysfs 那套（不能靠 authorized 重新枚举设备）
func USBRescueSupported() bool { return false }

// ReauthorizeUSBDevice Windows 上不支持（设备重枚举得靠厂商驱动/设备管理器）
func ReauthorizeUSBDevice(busPath string) error {
	return fmt.Errorf("Windows 上不支持 USB 软重插")
}

// USBHotplugBindNote 给页面解释为什么不能补
func USBHotplugBindNote() string {
	return "Windows 的串口由驱动 inf 决定：装了厂商/系统驱动才会出现 COM 口，" +
		"程序不做自动绑定（设备管理器里「其他设备」带黄叹号时，先装厂商驱动）"
}

// EnsureSerialTTY Windows 上不支持（保留同一个接口，让上层逻辑跨平台）
func EnsureSerialTTY(d ModemUSBDevice) (string, error) {
	return "", fmt.Errorf("Windows 不支持自动绑定串口驱动；%s", USBHotplugBindNote())
}

// ListModemUSBDevices 枚举"像模组"的 USB 设备，并把 COM 口关联上。
//
// 两条来源合起来看：
//   - 串口 → 注册表反查（ports_windows.go 的 USBDevicePath）：有 COM 口的模组，以及它的口；
//   - Enum\USB 下的设备节点：**包括还没拿到 COM 口的**（未装驱动），页面上要能看见。
func ListModemUSBDevices() ([]ModemUSBDevice, error) {
	// COM 口 → 设备实例段（小写，例如 "6&8e1cfcb&0&0001"）
	instComs := map[string][]string{}
	for _, p := range scanSerialPortsSafe() {
		key := netdev.USBDevicePath(p)
		if key == "" {
			continue
		}
		i := strings.Index(key, `\`)
		if i < 0 || i+1 >= len(key) {
			continue
		}
		inst := strings.ToLower(key[i+1:])
		instComs[inst] = append(instComs[inst], p)
	}

	root, err := netdev.OpenRegistryKey(netdev.RegHKEYLocalMachine, netdev.UsbEnumRegistryPath)
	if err != nil {
		return nil, err
	}
	defer netdev.ProcRegCloseKey.Call(root)

	var out []ModemUSBDevice
	for _, sub := range netdev.EnumSubKeys(root, netdev.MaxEnumPorts) {
		up := strings.ToUpper(sub)
		// 接口节点（VID_x&PID_y&MI_zz）与 TD Tech 那种写法（VID_3466&SUBCLASS_06&PROT_12）
		// 都不是“设备”，设备节点一定带 PID_ —— 不滤掉会在表里重复出现好几行。
		if strings.Contains(up, "&MI_") || !strings.Contains(up, "PID_") {
			continue
		}
		vid, pid := usbids.SplitUSBVidPid(sub)
		if vid == "" || pid == "" {
			continue
		}
		var classes []string
		product := ""
		devInst := ""
		instRoot, err := netdev.OpenRegistryKey(netdev.RegHKEYLocalMachine, netdev.UsbEnumRegistryPath+`\`+sub)
		if err != nil {
			continue
		}
		for _, inst := range netdev.EnumSubKeys(instRoot, 4) {
			// ⚠️ 注册表里会把**已经拔掉**的设备实例一直留着（历史记录）。不问 PnP 就分不清
			// 它在不在插 —— 不过滤的后果就是用户反馈的“拔了但 UI 里模块还在”。
			// CM_Locate_DevNodeW（NORMAL）对不在插的实例返回 CR_NO_SUCH_DEVINST。
			if !netdev.DeviceInstancePresent(`USB\` + sub + `\` + inst) {
				continue
			}
			instPath := netdev.UsbEnumRegistryPath + `\` + sub + `\` + inst
			if devInst == "" {
				// 归属前缀：优先 ParentIdPrefix（父设备给的前缀，子接口/COM 口的实例名以它开头），
				// 没有就用实例名本身。
				// ⚠️ 实测 TD Tech（MT5700）：设备实例是**序列号**（57050M5…），
				// 而接口实例是 6&8e1cfcb&0&0001 —— 两者没有前缀关系，
				// 只有 ParentIdPrefix（6&8e1cfcb&0）能把它们连起来。
				// （取第一个“还插着”的实例：同一型号插两台时会有多个实例）
				devInst = strings.ToLower(inst)
				if p := firstNonEmpty(usbdesc.RegReadStrings(instPath, "ParentIdPrefix")); p != "" {
					devInst = strings.ToLower(p)
				}
			}
			for _, v := range []string{"CompatibleIDs", "HardwareID"} {
				for _, line := range usbdesc.RegReadStrings(instPath, v) {
					m := usbdesc.ReUSBClassID.FindStringSubmatch(line)
					if m == nil || m[1] == "00" {
						continue
					}
					classes = append(classes, strings.ToLower(m[1]+"/"+m[2]+"/"+m[3]))
				}
			}
			if product == "" {
				product = usbdesc.CleanDeviceDesc(usbdesc.RegReadStrings(instPath, "DeviceDesc"))
			}
		}
		netdev.ProcRegCloseKey.Call(instRoot)
		if devInst == "" {
			// 这个 VID/PID 下的实例全是历史记录（都没插着）→ 不进页面列表。
			// 以前这里会把它当成“插上了但没串口”报出来，用户看到的却是已经拔掉的模组。
			continue
		}
		classes = dial.ReduceUSBCodes(classes)

		fam, known := usbids.IsKnownModemUSB(vid, pid, classes)
		if !known {
			continue // 只关心模组，别的 USB 设备不进列表
		}
		dev := ModemUSBDevice{
			VID: vid, PID: pid, BusPath: sub,
			Product:  product,
			Classes:  classes,
			Known:    true,
			Family:   string(fam.Vendor),
			VendorCN: iface.VendorLabel(fam.Vendor),
		}
		if devInst != "" {
			for inst, coms := range instComs {
				if strings.HasPrefix(inst, devInst) {
					dev.TTYs = append(dev.TTYs, coms...)
				}
			}
			sort.Strings(dev.TTYs)
		}
		dev.HasSerialDriver = len(dev.TTYs) > 0
		if !dev.HasSerialDriver {
			dev.BindNote = USBHotplugBindNote()
		} else if len(dev.TTYs) > 0 {
			dev.BindNote = ""
		}
		out = append(out, dev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BusPath < out[j].BusPath })
	return out, nil
}

// scanSerialPortsSafe 枚举串口（失败返回空，不让视图整体失败）
func scanSerialPortsSafe() []string {
	ports, err := netdev.ScanSerialPorts()
	if err != nil {
		return nil
	}
	return ports
}

// firstNonEmpty 取第一个非空字符串（注册表多字符串值按行返回，取首个有效行）
func firstNonEmpty(in []string) string {
	for _, s := range in {
		if v := strings.TrimSpace(s); v != "" {
			return v
		}
	}
	return ""
}
