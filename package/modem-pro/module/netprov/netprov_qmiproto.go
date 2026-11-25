package netprov

// ============ QMI 接口 proto 的决策（跨平台纯逻辑，便于单测） ============
//
// 结论（用户 2026-10-09 定的口径）：**QMI 的 netifd 接口一直用 dhcp**。
// 地址/路由/DNS 由 netifd 的 udhcpc 取（链路上有 DHCP 服务时就靠它上网），
// 数据呼叫由 modemd 托管的厂商拨号器（fibocom-dial / quectel-CM …）拉起来。
//
// 中间试过“连续几轮拿不到租约就自动降级成 none”，**已经去掉**：
// none 一写下去 netifd 就不跑协议了，链路上就算有 DHCP 服务也拿不到地址，
// 等于把上网的路自己掐掉。只想让拨号器管地址的就显式写 [net] qmi_proto = "none"。
//
// 本文件保留的东西：
//   - QmiWantProto：该用哪个 proto（auto/dhcp → dhcp；none → none；旧版遗留写法 → 迁回 dhcp）；
//   - qmiStuck* 计数器：接口在 dhcp 上但网卡一直没地址时，**只用来按节奏提醒一次**
//     （见 QmiStuckWarnEvery），不再改配置。

import (
	"modemd/module/config"
	"strings"
	"sync"
)

// QmiStuckWarnEvery 连续多少轮“接口在 dhcp 上、网卡却没地址”就提醒一次日志。
// **只提醒，不动配置**（一轮 = 组网循环间隔，默认 15 秒 ⇒ 约 5 分钟一次）。
const QmiStuckWarnEvery = 20

var (
	qmiStuckMu     sync.Mutex
	qmiStuckRounds = map[int]int{}
)

// qmiNoAddrCount 这台模组已经连续多少轮"接口在、网卡上却没地址"。
func QmiStuckRoundsFor(id int) int {
	qmiStuckMu.Lock()
	defer qmiStuckMu.Unlock()
	return qmiStuckRounds[id]
}

// qmiNoAddrBump 记一轮"没地址"，返回累计轮数。
func QmiStuckBump(id int) int {
	qmiStuckMu.Lock()
	defer qmiStuckMu.Unlock()
	qmiStuckRounds[id]++
	return qmiStuckRounds[id]
}

// qmiNoAddrReset 拿到地址（或模组没了）⇒ 清零，以后重新算。
func QmiStuckReset(id int) {
	qmiStuckMu.Lock()
	defer qmiStuckMu.Unlock()
	delete(qmiStuckRounds, id)
}

// QmiWantProto 纯决策：QMI 类模组的 netifd 接口该用哪个 proto。
//
//	cfg = [net] qmi_proto：
//	      "" / "auto"（默认）/ "dhcp" / 写错的值 → dhcp（netifd 取地址/路由/DNS）
//	      "none"                                → none（显式要求：不跑协议）
//	cur = 接口当前 proto（"qmi" / "quectel" / "dhcp" / "none" / 空）
//
// 返回值只可能是 "dhcp" 或 "none"。cur 只影响**旧版遗留写法**的迁移：
// 老版本自己建的接口写过 qmi / quectel / none（语义已经变了）⇒ 一律改回 dhcp，
// 用户手工写的 none 也一样改；要一直保持 none 就显式配 qmi_proto = "none"。
//
// ⚠️ 这里**不再有"拿不到租约就降级成 none"那条**（用户 2026-10-09 要求去掉）：
// none 会让 netifd 完全不跑协议，链路上有 DHCP 都拿不到地址 = 自己把上网掐掉。
func QmiWantProto(cfg, cur string, modemProtoOK bool) string {
	if strings.EqualFold(strings.TrimSpace(cfg), "none") {
		return "none"
	}
	// 用户没显式要求 none 时：装了合并接口协议（luci-proto-modem-pro）就用它 ——
	// 一张接口同时拿 v4+v6，不用再建 modem<id>_6 那张 v6 兄弟接口。
	// 没装则保持老行为 dhcp（地址/路由/DNS 由 netifd 的 DHCP 取）。
	if modemProtoOK && config.WANModeNow().UsesModemPro() {
		return ModemProtoName
	}
	return "dhcp"
}
