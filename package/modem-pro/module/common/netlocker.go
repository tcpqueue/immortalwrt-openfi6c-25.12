package common

// NetLockerOf 取指令集的锁网能力实现；未实现该接口时返回 nil。
// 从 module/kernel/lock.go 搬过来（纯类型断言，厂商层与内核都要用）。

import "modemd/iface"

// NetLockerOf 取指令集的锁网能力实现；未实现该接口时返回 nil。
// 从 module/kernel/lock.go 搬过来（纯类型断言，厂商层与内核都要用）。
func NetLockerOf(cmd iface.ModemCommand) iface.NetLocker {
	l, _ := cmd.(iface.NetLocker)
	return l
}
