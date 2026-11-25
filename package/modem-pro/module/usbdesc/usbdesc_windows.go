//go:build windows

package usbdesc

// ====================== USB 描述符（Windows：设备枚举注册表） ======================
//
// 为什么走注册表而不是 SetupAPI：本机是纯 Go + CGO_ENABLED=0，注册表只读几个值就够，
// 而且 ports_windows.go 已经有整套 RegOpenKeyExW/RegEnumKeyExW 的封装（AT 口分组在用）。
//
// 设备树形状（以移远 RM500U 为例）：
//
//	SYSTEM\CurrentControlSet\Enum\USB\VID_2C7C&PID_0800\6&1a2b3c&0&0000     ← 复合设备（父）
//	SYSTEM\CurrentControlSet\Enum\USB\VID_2C7C&PID_0800&MI_00\6&...&0&0000  ← 各接口（子）
//
// 真正常用 AT 口那条实例里只有 "USB\Class_ff&SubClass_ff&Prot_ff"（厂商自定义），
// **拨号方式要看各 MI_xx 接口的类码**，所以这里把同 VID/PID 的父节点与所有 MI 子节点一起读。

import (
	"modemd/module/dial"
	"modemd/module/netdev"
	"modemd/module/usbids"
	"regexp"
	"strings"
	"syscall"
	"unsafe"
)

// ReUSBClassID 从 CompatibleIDs / HardwareID 里抠出"类/子类/协议"。
//
// 两种写法都要认（实测同一台设备上两种同时存在）：
//
//	USB\Class_02&SubClass_0e&Prot_00        接口节点（&MI_xx，普通接口）
//	USB\DevClass_00&SubClass_00&Prot_00     设备节点（复合设备用 DevClass_ 前缀）
//
// 前缀可选（有的写 Class_ 而没有 SubClass_/Prot_），所以后两段也是可选的。
var ReUSBClassID = regexp.MustCompile(`(?i)(?:Dev)?Class_([0-9a-f]{2})(?:&SubClass_([0-9a-f]{2}))?(?:&Prot_([0-9a-f]{2}))?`)

// USBDescriptor 读模组 USB 侧的实际描述（VID/PID + 各接口类码）。
// 拿不到（不是 USB 串口 / 注册表读不到）时返回 nil。
func USBDescriptor(portName string) *dial.USBDesc {
	key := netdev.USBDevicePath(portName) // "VID_0E8D&PID_7126&MI_02\6&341c7b3&0&0002"
	if key == "" {
		return nil
	}
	vidPid := key
	if i := strings.Index(key, `\`); i > 0 {
		vidPid = key[:i]
	}
	// ⚠️ 这个键可能是**接口节点**（VID_x&PID_y&MI_zz）—— 要去掉 &MI_ 后缀才是设备本身。
	// 不去掉就只能看到 AT 口自己那一个接口（实测 FM350-GL：只读到 ff/00/00，
	// 漏了同设备的蓝牙接口 e0/01/03 与另一个厂商接口 ff/42/01）。
	if i := strings.Index(strings.ToUpper(vidPid), "&MI_"); i > 0 {
		vidPid = vidPid[:i]
	}
	vid, pid := usbids.SplitVidPid(vidPid)
	if vid == "" {
		return nil
	}
	d := &dial.USBDesc{VID: vid, PID: pid, Source: "windows-registry"}

	root, err := netdev.OpenRegistryKey(netdev.RegHKEYLocalMachine, netdev.UsbEnumRegistryPath)
	if err != nil {
		return nil
	}
	defer netdev.ProcRegCloseKey.Call(root)

	var classes []string
	for _, sub := range netdev.EnumSubKeys(root, netdev.MaxEnumPorts) {
		// 同一设备：设备节点（VID_x&PID_y）或它的接口节点（VID_x&PID_y&MI_zz）
		if sub != vidPid && !strings.HasPrefix(sub, vidPid+"&MI_") {
			continue
		}
		instRoot, err := netdev.OpenRegistryKey(netdev.RegHKEYLocalMachine, netdev.UsbEnumRegistryPath+`\`+sub)
		if err != nil {
			continue
		}
		for _, inst := range netdev.EnumSubKeys(instRoot, netdev.MaxEnumPorts) {
			instPath := netdev.UsbEnumRegistryPath + `\` + sub + `\` + inst
			for _, v := range []string{"CompatibleIDs", "HardwareID"} {
				for _, line := range RegReadStrings(instPath, v) {
					m := ReUSBClassID.FindStringSubmatch(line)
					// 类码 00 = "在接口级别定义"（复合设备的父节点就是这个），没有信息量
					if m == nil || m[1] == "00" {
						continue
					}
					classes = append(classes, strings.ToLower(m[1]+"/"+m[2]+"/"+m[3]))
				}
			}
			if d.Product == "" {
				d.Product = CleanDeviceDesc(RegReadStrings(instPath, "DeviceDesc"))
			}
		}
		netdev.ProcRegCloseKey.Call(instRoot)
	}
	// 同一接口会给 "类/子类/协议" 与它的两种简写，收敛成最具体的那条（见 ReduceUSBCodes）
	d.Classes = dial.ReduceUSBCodes(classes)
	if len(d.Classes) == 0 && d.Product == "" {
		return nil
	}
	return d
}

// RegReadStrings 读一个 REG_SZ / REG_MULTI_SZ 值（按 UTF-16 解，NUL 分隔）。
// 读不到（键/值不存在、类型不是字符串）返回 nil。
func RegReadStrings(keyPath, valueName string) []string {
	hkey, err := netdev.OpenRegistryKey(netdev.RegHKEYLocalMachine, keyPath)
	if err != nil {
		return nil
	}
	defer netdev.ProcRegCloseKey.Call(hkey)

	buf := make([]byte, 8192)
	dataLen := uint32(len(buf))
	ret, _, _ := netdev.ProcRegQueryValueExW.Call(
		hkey,
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(valueName))),
		0,
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&dataLen)),
	)
	if ret != 0 || dataLen == 0 || int(dataLen) > len(buf) {
		return nil
	}
	return splitUTF16MultiString(buf[:dataLen])
}

// splitUTF16MultiString REG_MULTI_SZ 的字节 → 字符串列表（REG_SZ 就是单元素）
func splitUTF16MultiString(b []byte) []string {
	n := len(b) - len(b)%2
	u := make([]uint16, 0, n/2)
	for i := 0; i < n; i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	var out []string
	start := 0
	for i, c := range u {
		if c == 0 {
			if i > start {
				out = append(out, syscall.UTF16ToString(u[start:i]))
			}
			start = i + 1
		}
	}
	return out
}

// CleanDeviceDesc 设备描述去掉 INF 引用前缀：
// "@oem12.inf,%qcusbser%;Quectel USB AT Port" → "Quectel USB AT Port"
func CleanDeviceDesc(list []string) string {
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if i := strings.LastIndex(s, ";"); i >= 0 && strings.HasPrefix(s, "@") {
			s = strings.TrimSpace(s[i+1:])
		}
		if s != "" {
			return s
		}
	}
	return ""
}
