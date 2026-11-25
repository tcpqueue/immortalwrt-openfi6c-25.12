//go:build windows

package kernel

import (
	"modemd/module/common"
	"modemd/module/netdev"
	"strings"
	"testing"
)

// ---- from: ports/ports_windows_test.go ----

func TestScanSerialPortsWindows(t *testing.T) {
	ports, err := netdev.ScanSerialPorts()
	if err != nil {
		t.Fatalf("scanSerialPorts error: %v", err)
	}
	t.Logf("enumerated ports: %v", ports)
	for _, p := range ports {
		if !strings.HasPrefix(strings.ToUpper(p), "COM") {
			t.Errorf("port name should be COMx, got %q", p)
		}
	}
}

// TestScanSerialPortsSkipsPhantom 验证扫出来的每个 USB 串口，其设备实例**确实还插着**。
//
// 背景（用户实测反馈“usb 5g 模块拔了，UI 里模块还在”）：SERIALCOMM 会留着已经拔掉的
// 端口（只要有人开着那个口，驱动实例就不卸载），不滤掉就会一直以为模组在。
// 这个断言与具体机器无关：把扫到的口再问一遍 PnP，结论必须一致。
func TestScanSerialPortsSkipsPhantom(t *testing.T) {
	ports, err := netdev.ScanSerialPorts()
	if err != nil {
		t.Fatalf("ScanSerialPorts error: %v", err)
	}
	insts := netdev.UsbInstanceByPort()
	for _, p := range ports {
		if inst := insts[strings.ToUpper(p)]; inst != "" && !netdev.DeviceInstancePresent(inst) {
			t.Errorf("幽灵端口 %s（实例 %s）不该出现在扫描结果里", p, inst)
		}
	}
	t.Logf("live ports: %v（实例映射 %v）", ports, insts)
}

// TestDeviceInstancePresent 验证 PnP 在场检查本身的语义。
func TestDeviceInstancePresent(t *testing.T) {
	// 没有实例信息时按“在”处理：宁可不删，也不要误删用户的模组
	if !netdev.DeviceInstancePresent("") {
		t.Error(`DeviceInstancePresent("") 应为 true`)
	}
	// 格式合法但不存在的实例 → 必须判定为“拔了”
	if netdev.DeviceInstancePresent(`USB\VID_FFFF&PID_FFFF\0`) {
		t.Error("不存在的设备实例应判定为 false（否则拔掉的模组会一直留在 UI 里）")
	}
}

// TestExtractComPort 验证 COM 端口号解析
func TestExtractComPort(t *testing.T) {
	cases := map[string]int{
		"USB-SERIAL CH340 (COM3)": 3,
		"Intel SOL (COM3)":        3,
		"COM10":                   10,
		"com25":                   25,
		"anything without com":    -1,
		"Prolific USB-to-Serial Comm Port (COM8)": 8,
	}
	for input, want := range cases {
		got := netdev.ExtractComPort(input)
		if got != want {
			t.Errorf("extractComPort(%q) = %d, want %d", input, got, want)
		}
	}
}

// TestNormalizePortName 验证 COM10+ 加前缀
func TestNormalizePortName(t *testing.T) {
	if got := common.NormalizePortName("COM3"); got != `\\.\COM3` {
		t.Errorf("common.NormalizePortName(COM3) = %q", got)
	}
	if got := common.NormalizePortName("COM10"); got != `\\.\COM10` {
		t.Errorf("common.NormalizePortName(COM10) = %q", got)
	}
}

// TestSerialPortIndex 验证编号解析
func TestSerialPortIndex(t *testing.T) {
	if got := netdev.SerialPortIndex("COM10"); got != 10 {
		t.Errorf("SerialPortIndex(COM10) = %d", got)
	}
	if got := netdev.SerialPortIndex("/dev/ttyUSB3"); got != -1 {
		t.Errorf("SerialPortIndex(/dev/ttyUSB3) = %d, want -1", got)
	}
}
