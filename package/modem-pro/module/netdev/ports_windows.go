//go:build windows

package netdev

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"go.uber.org/zap"

	"modemd/module/config"
)

// ---- from: ports/ports_windows.go ----

// UsbDeviceAge Windows 侧没有 sysfs，拿不到"设备被枚举出来多久"。
// 返回 ok=false，调用方（DiscoverModems 的预热等待）会直接跳过等待，行为与改造前一致。
func UsbDeviceAge(string) (time.Duration, bool) {
	return 0, false
}

var (
	modadvapi32          = syscall.NewLazyDLL("advapi32.dll")
	procRegOpenKeyExW    = modadvapi32.NewProc("RegOpenKeyExW")
	ProcRegCloseKey      = modadvapi32.NewProc("RegCloseKey")
	procRegEnumValueW    = modadvapi32.NewProc("RegEnumValueW")
	procRegEnumKeyExW    = modadvapi32.NewProc("RegEnumKeyExW")
	ProcRegQueryValueExW = modadvapi32.NewProc("RegQueryValueExW")
)

// cfgmgr32：问 PnP 管理器“这个设备实例现在还插着吗”（见 DeviceInstancePresent）
var (
	modcfgmgr32          = syscall.NewLazyDLL("cfgmgr32.dll")
	procCMLocateDevNodeW = modcfgmgr32.NewProc("CM_Locate_DevNodeW")
)

const (
	cmLocateDevnodeNormal = 0x00000000 // 只定位**当前存在**的设备（幽灵实例会返回 CR_NO_SUCH_DEVINST）
	crNoSuchDevInst       = 0x0D       // CR_NO_SUCH_DEVINST：PnP 里已经没有这个实例了
)

// DeviceInstancePresent 问 PnP 管理器：这个设备实例现在还插着吗？
//
// ⚠️ 为什么非问不可（用户实测反馈：“usb 5g 模块拔了，UI 里模块还在”）：
// `HARDWARE\DEVICEMAP\SERIALCOMM` **会留着已经拔掉的端口** —— 只要还有人开着那个 COM 口，
// 驱动实例就不卸载，这条记录也就一直在（本机实测：广和通 FM350 拔掉后 COM5 仍在
// SERIALCOMM 里，而占着它的正是我们自己）。只看 SERIALCOMM 的后果是：模组明明拔了
// 界面里还在、AT 全都不回、面板一直报错，用户只能重启进程。
// 问 PnP 才有准信：设备不在时 CM_Locate_DevNodeW（NORMAL）返回 CR_NO_SUCH_DEVINST。
//
// 其它错误（权限 / 参数非法）一律按“在插着”处理 —— 宁可多留一条，也不要误删用户的模组。
func DeviceInstancePresent(instanceID string) bool {
	if instanceID == "" {
		return true
	}
	p, err := syscall.UTF16PtrFromString(instanceID)
	if err != nil {
		return true
	}
	var DevInst uint32
	ret, _, _ := procCMLocateDevNodeW.Call(
		uintptr(unsafe.Pointer(&DevInst)),
		uintptr(unsafe.Pointer(p)),
		uintptr(cmLocateDevnodeNormal),
	)
	return ret != crNoSuchDevInst
}

// 注册表根键常量
const (
	RegHKEYLocalMachine = 0x80000002
	regKeyRead          = 0x00020019 // KEY_QUERY_VALUE | KEY_ENUMERATE_SUB_KEYS
)

// MaxEnumPorts 单次枚举串口数上限，防止异常环境下死循环
const MaxEnumPorts = 256

// serialcommRegistryPath 注册表路径：记录所有 COM 串口映射
const serialcommRegistryPath = `HARDWARE\DEVICEMAP\SERIALCOMM`

// UsbEnumRegistryPath USB 设备枚举根路径
const UsbEnumRegistryPath = `SYSTEM\CurrentControlSet\Enum\USB`

// scanSerialPorts 通过注册表 SERIALCOMM 键枚举 Windows 中的 COM 串口。
// 返回按编号排序的端口名列表。
func ScanSerialPorts() ([]string, error) {
	Hkey, err := OpenRegistryKey(RegHKEYLocalMachine, serialcommRegistryPath)
	if err != nil {
		return nil, err
	}
	defer ProcRegCloseKey.Call(Hkey)

	var ports []string
	valueName := make([]uint16, 256)
	for index := 0; index < MaxEnumPorts; index++ {
		nameLen := uint32(len(valueName))
		var data [64]byte
		DataLen := uint32(len(data))
		ret, _, _ := procRegEnumValueW.Call(
			Hkey,
			uintptr(index),
			uintptr(unsafe.Pointer(&valueName[0])),
			uintptr(unsafe.Pointer(&nameLen)),
			0,
			0,
			uintptr(unsafe.Pointer(&data[0])),
			uintptr(unsafe.Pointer(&DataLen)),
		)
		if ret != 0 { // ERROR_NO_MORE_ITEMS
			break
		}
		// 值名称是设备路径如 "\Device\Serial0"；值数据才是 COM 端口名（UTF-16LE）
		devName := syscall.UTF16ToString(valueName[:nameLen])
		comNum := ExtractComPort(devName)
		if comNum <= 0 {
			dataStr := utf16LEBytesToString(data[:DataLen])
			comNum = ExtractComPort(dataStr)
		}
		if comNum > 0 {
			ports = append(ports, fmt.Sprintf("COM%d", comNum))
		}
	}

	// 去重排序
	ports = uniqueStrings(ports)
	sort.Slice(ports, func(i, j int) bool {
		return SerialPortIndex(ports[i]) < SerialPortIndex(ports[j])
	})

	// ⚠️ 丢掉“设备已经拔掉、但 SERIALCOMM 记录还在”的幽灵端口（见 DeviceInstancePresent）。
	// 不丢的话拔掉的模组会一直留在模组列表里（用户实测反馈就是这个）。
	// 查不到 USB 实例的口（主板自带 COM / 蓝牙虚拟串口）保留 —— 没依据就别动它。
	insts := UsbInstanceByPort()
	live := make([]string, 0, len(ports))
	for _, p := range ports {
		inst := insts[strings.ToUpper(p)]
		if inst != "" && !DeviceInstancePresent(inst) {
			config.Logger.Debug("skip phantom com port (device not present)",
				zap.String("port", p), zap.String("instance", inst))
			continue
		}
		live = append(live, p)
	}
	return live, nil
}

// UsbInstanceByPort 遍历一次注册表 Enum\USB，得到 “COM 口 → PnP 设备实例 ID” 的映射
// （例如 COM5 → `USB\VID_0E8D&PID_7126&MI_04\6&341C7B3&0&0004`）。
//
// 做成“一次遍历得到整张表”是因为两个调用方都要用：扫串口时要按它过滤幽灵端口，
// 分组时要按它把同一设备的多个口归到一起（以前每个口都单独走一遍注册表）。
// 表里**包含**已经拔掉的实例 —— 在不在插由 DeviceInstancePresent 判定，这里只负责查。
func UsbInstanceByPort() map[string]string {
	insts := make(map[string]string)
	rootKey, err := OpenRegistryKey(RegHKEYLocalMachine, UsbEnumRegistryPath)
	if err != nil {
		return insts
	}
	defer ProcRegCloseKey.Call(rootKey)

	for _, vendorKey := range EnumSubKeys(rootKey, MaxEnumPorts) {
		vk, err := OpenRegistryKey(RegHKEYLocalMachine, UsbEnumRegistryPath+`\`+vendorKey)
		if err != nil {
			continue
		}
		for _, instID := range EnumSubKeys(vk, MaxEnumPorts) {
			name := queryPortName(UsbEnumRegistryPath + `\` + vendorKey + `\` + instID)
			if name != "" {
				insts[name] = `USB\` + vendorKey + `\` + instID
			}
		}
		ProcRegCloseKey.Call(vk)
	}
	return insts
}

// USBDevicePath 返回某个 COM 口对应的 USB 设备实例段（分组 key），例如
// `VID_3466&SUBCLASS_06&PROT_12\6&8e1cfcb&0&0001`（不带 `USB\` 前缀，调用方拿它当分组名）。
// 同一 USB 设备的多个接口共享同一实例段，故可正确分组。
// 返回空串表示非 USB 串口（主板自带 COM、蓝牙虚拟串口等）。
//
// 注意：**不看设备在不在插**（幽灵实例也返回实例段）—— “在不在”由 DeviceInstancePresent 判定，
// 过滤发生在 ScanSerialPorts（幽灵口根本不会进到这里的 ports 里）。
func USBDevicePath(portName string) string {
	upper := strings.ToUpper(portName)
	if !strings.HasPrefix(upper, "COM") {
		return ""
	}
	return strings.TrimPrefix(UsbInstanceByPort()[upper], `USB\`)
}

// groupByUSBDevice 将串口按 USB 设备实例分组。
// 同一个 USB 设备的所有 COM 口属于同一个模组（模组占 2/4/6 口都能正确对应）。
// 无法关联到 USB 设备的串口（主板 COM）各自独立成组。
func GroupByUSBDevice(ports []string) [][]string {
	insts := UsbInstanceByPort() // 走一遍注册表拿到映射，别每个口都走一遍
	groupsMap := make(map[string][]string)
	var order []string
	for _, p := range ports {
		key := strings.TrimPrefix(insts[strings.ToUpper(p)], `USB\`)
		if key == "" {
			key = p // 独立成组
		}
		if _, ok := groupsMap[key]; !ok {
			order = append(order, key)
		}
		groupsMap[key] = append(groupsMap[key], p)
	}
	groups := make([][]string, 0, len(order))
	for _, k := range order {
		groups = append(groups, groupsMap[k])
	}
	return groups
}

// queryPortName 读取设备实例的 Device Parameters\PortName 值
func queryPortName(instPath string) string {
	paramsPath := instPath + `\Device Parameters`
	Hkey, err := OpenRegistryKey(RegHKEYLocalMachine, paramsPath)
	if err != nil {
		return ""
	}
	defer ProcRegCloseKey.Call(Hkey)

	var buf [64]byte
	var DataLen uint32 = uint32(len(buf))
	ret, _, _ := ProcRegQueryValueExW.Call(
		Hkey,
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("PortName"))),
		0,
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&DataLen)),
	)
	if ret != 0 {
		return ""
	}
	return strings.ToUpper(strings.TrimRight(utf16LEBytesToString(buf[:DataLen]), "\x00"))
}

// OpenRegistryKey 打开注册表键并返回句柄
func OpenRegistryKey(root uintptr, path string) (uintptr, error) {
	var Hkey uintptr
	ret, _, err := procRegOpenKeyExW.Call(
		root,
		uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(path))),
		0,
		regKeyRead,
		uintptr(unsafe.Pointer(&Hkey)),
	)
	if ret != 0 { // ERROR_SUCCESS
		return 0, fmt.Errorf("RegOpenKeyExW %s failed: %w", path, err)
	}
	return Hkey, nil
}

// EnumSubKeys 枚举指定注册表键的所有子键名
func EnumSubKeys(Hkey uintptr, max int) []string {
	var names []string
	buf := make([]uint16, 512)
	for i := 0; i < max; i++ {
		nameLen := uint32(len(buf))
		ret, _, _ := procRegEnumKeyExW.Call(
			Hkey,
			uintptr(i),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&nameLen)),
			0,
			0,
			0,
			0,
		)
		if ret != 0 { // ERROR_NO_MORE_ITEMS
			break
		}
		names = append(names, syscall.UTF16ToString(buf[:nameLen]))
	}
	return names
}

// ExtractComPort 从字符串中提取 COM 端口号。
// 例如 "USB-SERIAL CH340 (COM3)" -> 3，"COM10" -> 10；解析失败返回 -1
func ExtractComPort(name string) int {
	upper := strings.ToUpper(name)
	// 优先匹配 "(COMx)" 格式
	if idx := strings.Index(upper, "(COM"); idx >= 0 {
		rest := upper[idx+4:]
		if n := ParseComNumber(rest); n > 0 {
			return n
		}
	}
	// 回退匹配 "COMx" 格式
	if idx := strings.Index(upper, "COM"); idx >= 0 {
		return ParseComNumber(upper[idx+3:])
	}
	return -1
}

// utf16LEBytesToString 将 UTF-16LE 字节序列转换为 Go 字符串
func utf16LEBytesToString(b []byte) string {
	n := len(b) - len(b)%2
	u := make([]uint16, 0, n/2)
	for i := 0; i < n; i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return strings.TrimRight(syscall.UTF16ToString(u), "\x00")
}

// parseComNumber 从字符串开头解析端口编号，直到遇到非数字字符。
// "10)" -> 10，"3" -> 3；无数字或为 0 返回 -1。
func ParseComNumber(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return -1
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil || n <= 0 {
		return -1
	}
	return n
}

// serialPortIndex 提取串口编号，供分组排序使用。COM10 编号为 10。
func SerialPortIndex(name string) int {
	upper := strings.ToUpper(name)
	if !strings.HasPrefix(upper, "COM") {
		return -1
	}
	return ParseComNumber(upper[3:])
}

// uniqueStrings 去重并保持顺序
func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
