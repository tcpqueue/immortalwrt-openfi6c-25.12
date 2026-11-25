package common

// 本文件是「厂商层需要、但实现还在 kernel」的**桥接钩子**。
//
// 为什么需要它：厂商指令类型（QuectelCommand / FibocomCommand…）都要实现
// `ParseNRSignal`，而它们的统一实现是 module/kernel/cell.go 的 `ParseServingNRSignal` ——
// 那套解析依赖 cell.go 的整条链路（`ParseCellLine` → `CellInfo` → 一堆字段解析），
// 整体搬过来会带出上千行与厂商无关的代码。
//
// 所以先**显式注入**：kernel 在 init 里把实现灌进来（见 module/kernel/parse_bridge.go）。
// 这和"隐藏的全局单例"不同 —— 注入点只有一处、名字只有一个、默认值是安全的空实现。
//
// ⏭ 将来把 cell.go 的解析层整体搬成 `module/parse` 之后，
// 本文件整个删掉，厂商包直接 import 那个包即可。

// ParseServingNRSignal 从 NR servingcell 行解析 RSRP/RSRQ/SINR。
// 默认实现恒返回 false —— 万一没注入，厂商侧只是"解析不出来"，不会 panic。
var ParseServingNRSignal = func(line string) (rsrp, rsrq, sinr int, ok bool) {
	return 0, 0, 0, false
}

// ParseCPSISignal 从 `+CPSI:` 行解析 RSRP/RSRQ/SINR（芯讯通的 ParseNRSignal 用它）。
//
// 注意签名只暴露**信号三元组**，不暴露 kernel 的 CellInfo ——
// 厂商层只需要这三个数，没必要（也不应该）看到内核的类型。
var ParseCPSISignal = func(line string) (rsrp, rsrq, sinr int, ok bool) {
	return 0, 0, 0, false
}

// ParseLooseNRSignal 宽容解析：先按已实机验证的 +QENG/+FENG 格式，
// 再对 +CPSI/+QCSQ 这类"没有固定文档"的格式按信号量纲定位。
//
// 自适应指令集（AdaptiveCommands）用它 —— 未识别厂商什么格式都可能碰上。
// "怎么宽容"属于解析策略，留在 kernel 的解析层，厂商层只说"我要一个宽容解析"。
var ParseLooseNRSignal = func(line string) (rsrp, rsrq, sinr int, ok bool) {
	return 0, 0, 0, false
}
