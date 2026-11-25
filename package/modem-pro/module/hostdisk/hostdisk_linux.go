//go:build linux

package hostdisk

import "syscall"

// HostDiskPercent 根文件系统使用率 %（statfs，不占串口、不写盘）。
//
// 屏上显示的是 overlay（可写层）/ 根分区 —— 路由器上它满了比 CPU 高更要命。
//
// ⚠️ 单开一个 linux 文件（不是塞进 hostinfo.go）：syscall.Statfs 只有 unix 有，
// 放一起 Windows 上就编译不过（本工程要在 Windows 上跑单测，见 hostdisk_other.go）。
func HostDiskPercent() int {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return 0
	}
	total := st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	if total == 0 || free >= total {
		return 0
	}
	p := int((total - free) * 100 / total)
	if p > 100 {
		p = 100
	}
	return p
}
