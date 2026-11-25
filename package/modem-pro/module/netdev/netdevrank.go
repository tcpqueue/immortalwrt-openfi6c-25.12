package netdev

import (
	"sort"
	"strings"
)

// MatchNetdevToBusPath 从"网卡名 → /sys/class/net/<if>/device 解析后的路径"里，
// 挑出**属于这台模组**的网卡（按 USB 拓扑路径，如 2-1）。
//
// cdc_ncm / rndis_host / cdc_ether 这类驱动的网卡，其 device 软链指向**接口目录**
// （/sys/.../usb2/2-1/2-1:2.0），所以判据是路径里出现 "/<busPath>/"。
//
// ⚠️ 必须带两边的斜杠：busPath=2-1 不能匹配到 2-10（只做前缀匹配会张冠李戴）。
func MatchNetdevToBusPath(busPath string, resolved map[string]string) []string {
	busPath = strings.Trim(strings.TrimSpace(busPath), "/")
	if busPath == "" {
		return nil
	}
	needle := "/" + busPath + "/"
	var out []string
	for name, path := range resolved {
		p := strings.TrimRight(path, "/")
		// 设备目录本身也可能正好是链路末端（少见），两种情况都算
		if !strings.Contains(p+"/", needle) {
			continue
		}
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := NetdevRank(out[i]), NetdevRank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}

// NetdevRank 一台模组同时有多张网卡时的优先级（移远/鼎桥这类"wwan/usb/eth"口最常见）
//
// ⚠️ QMAP 的 **mux（数据）口**要排在它的父口前面（-2）：地址、默认路由、IPv6 都在 mux 上，
// 父口只是 QMAP 聚合链路、**没有任何 IP**。判错的后果（实测 RM502Q-GL + 厂家 qmi_wwan_q、
// qmap_mode=1）：UCI 接口绑到 wwan0，netifd 的 udhcpc 在 wwan0 上一直拿不到租约（进程卡在 D），
// 页面上就是"QMI 拨号器不能用/拿不到 IP" —— 其实地址早就在 wwan0_1 上了。
func NetdevRank(name string) int {
	n := strings.ToLower(name)
	switch {
	case IsQMAPMux(n):
		return -2
	case strings.HasPrefix(n, "wwan"):
		return 0
	case strings.HasPrefix(n, "usb"), strings.HasPrefix(n, "rndis"):
		return 1
	case strings.HasPrefix(n, "eth"):
		return 2
	case strings.HasPrefix(n, "br-"), strings.HasPrefix(n, "bond"):
		return 8 // 桥接/聚合：多半是用户自己组的，别选它
	default:
		return 5
	}
}

// IsQMAPMux 这张网卡是不是 QMAP 的 **mux（数据）口**：
//
//	qmimux0   主线 qmi_wwan 的 QMAP mux（quectel-CM 自己用 add_mux 建出来的）
//	wwan0_1   厂家 qmi_wwan_q（qmap_mode=1）的 mux 口，命名规则是"父名_muxid"
//
// 判据：以 `qmimux` 开头，或以 `_<数字>` 结尾。只在"这台模组名下的网卡"里用
// （MatchNetdevToBusPath 的候选集），所以不会误伤别人的接口。
func IsQMAPMux(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	if strings.HasPrefix(n, "qmimux") {
		return true
	}
	i := strings.LastIndex(n, "_")
	if i <= 0 || i == len(n)-1 {
		return false
	}
	for _, c := range n[i+1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
