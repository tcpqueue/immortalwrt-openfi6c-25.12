package netdev

import (
	"path/filepath"
	"strings"
)

// charATPrefixes 非 tty 的 AT 通道前缀（PCIe / MHI / wwan 子系统）
//
// 判据刻意**只认前缀**、不做"打开试试"：这些口在别的进程占用时也要能正确分类，
// 而且探测本身会发 AT，不能拿它当选型手段。
var charATPrefixes = []string{"mhi_", "wwan", "qcqmi", "qrtr-"}

// IsCharATDevice 这个设备名是不是"非 tty 的字符设备"（PCIe/MHI/wwan 口）
func IsCharATDevice(name string) bool {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." {
		return false
	}
	for _, p := range charATPrefixes {
		if strings.HasPrefix(base, p) {
			return true
		}
	}
	return false
}
