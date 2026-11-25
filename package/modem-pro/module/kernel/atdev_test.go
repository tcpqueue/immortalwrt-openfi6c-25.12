package kernel

// AT 通道层的回归测试（跨平台，Windows 上也能跑）。
//
// 展锐 SIPC 的传输是平台通用的裸字符设备路径，这里只需钉住一条不变量：
// **SIPC 通道必须被拒绝裸开**（nr2/nr3 走 isMu300DirectATDevice 的直连分支）
// —— 这是保护设备数据面的硬拦，退化不得。

import (
	"modemd/module/netdev"
	"testing"
)

func TestIsSIPCChannelName(t *testing.T) {
	// 判据刻意"宽"：只要名字是 stty_nr + 至少一个字符就归类为 SIPC。
	// 宁可多认（拒绝打开），也不能漏认 —— 漏一个就是拿设备的数据面冒险。
	yes := []string{"/dev/stty_nr0", "/dev/stty_nr1", "/dev/stty_nr31", "stty_nr7", "/dev/stty_nrX"}
	for _, n := range yes {
		if !netdev.IsSIPCChannelName(n) {
			t.Errorf("isSIPCChannelName(%q) 应为 true", n)
		}
	}
	no := []string{"/dev/ttyUSB0", "/dev/ttyACM1", "/dev/mhi_DUN", "/dev/stty_nr", "stty_n"}
	for _, n := range no {
		if netdev.IsSIPCChannelName(n) {
			t.Errorf("isSIPCChannelName(%q) 应为 false", n)
		}
	}
}

// ⚠️ 这条是"保护数据面"的硬拦：除了两条例外，绝不允许裸开 /dev/stty_nr*。
// 未被取证就打开，会和厂商守护进程抢通道、甚至永久弄哑它（只有重启模组能救）。
//
//	nr0       URC 上报口（厂商 drainer 在抽）→ 拒绝
//	nr1       mu300-atd 独占的代理口（单条 1~4s）→ 拒绝；**它已彻底不用**（走 nr2）
//	nr2/nr3   **命令通道**：实测能直连、单条 ~10ms（2026-10-05）→ 允许裸开
//	nr4 及以后 未取证 → 拒绝
func TestOpenATDeviceRefusesSIPCChannels(t *testing.T) {
	for _, name := range []string{"/dev/stty_nr0", "/dev/stty_nr4", "/dev/stty_nr31"} {
		if d, err := netdev.OpenATDevice(name); err == nil {
			_ = d.Close()
			t.Errorf("openATDevice(%q) 必须失败（未取证的 SIPC 通道是禁区）", name)
		}
	}
	// 快路必须被认出来（否则会被那条"拒绝一切 SIPC 通道"的守卫拦掉，白白退回 1~4 秒的慢路）。
	// ⚠️ 不要断言"能被打开" —— 本机（Windows/开发机）根本没有这些节点。
	for _, name := range []string{"/dev/stty_nr2", "stty_nr2", "/dev/stty_nr3"} {
		if !netdev.IsMu300DirectATDevice(name) {
			t.Errorf("isMu300DirectATDevice(%q) 应为 true（nr2/nr3 是取证过的直连快路）", name)
		}
	}
}
