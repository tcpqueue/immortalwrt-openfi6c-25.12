package tdtech

// ====================== 内核注入点 ======================
//
// 厂商包不能反向依赖内核，但 MT5700 的两处实现又必须用内核里的**通用解析器**：
//
//	^MONSC / ^HCSQ —— 这两个上报行不止 MT5700 一家在发，解析器（含 CellInfo 这种
//	内核类型）住在 module/kernel/cell.go，搬过来会把内核的数据模型拖进厂商包。
//
// 所以这里留两个**显式注入点**：默认是"什么都不认"的安全实现（返回 false），
// 由 module/kernel/parse_bridge.go 的 init() 注入真实实现。
//
// 为什么不用直接 import：kernel 会 import module/tdtech（引擎要按机型造指令类型），
// 双向 import 在 Go 里就是循环依赖，编译不过。
//
// ⚠️ 新增注入点时**必须同时**在 module/kernel/parse_bridge.go 的 init() 里注入，
// 否则该能力会**静默失效**（ParseNRSignal 永远返回 false，而不会报错）——
// 这类“忘了接上”的 bug 只能靠 kernel 侧的单测（Modem 层的行为测试）拦住。

import (
	"go.uber.org/zap"
)

// ParseMONSCSignal 解析 `^MONSC: …` 并只在"是服务小区且 RSRP 有效"时返回 true。
//
// 语义等价于内核里的：
//
//	ci, ok := ParseMONSC(line); ok && ci.Serving && ci.RSRP != 0
//
// 由 kernel 注入；未注入时返回 false（宁可不报，也不猜）。
var ParseMONSCSignal = func(line string) (rsrp, rsrq, sinr int, ok bool) { return 0, 0, 0, false }

// ParseHCSQSignal 解析 `^HCSQ: "NR",<rsrp 索引>,<sinr 索引>,<rsrq 索引>`（索引 → dB）。
// 由 kernel 注入；未注入时返回 false。
var ParseHCSQSignal = func(line string) (rsrp, rsrq, sinr int, ok bool) { return 0, 0, 0, false }

// logger 日志：默认丢弃（厂商包不持有内核的 logger 实例）。
// 由 kernel 在启动时用 SetLogger 注入真实的日志实现。
var logger FieldLogger = nopLogger{}

// FieldLogger 厂商包需要的日志能力：方法集与内核的日志代理（config.Proxy）一致。
// 用接口而不是 *zap.Logger：内核那边是带运行时可重配的代理，
// 厂商包只声明“我要 Info/Warn 这两个”，两边的类型不用互相认识。
type FieldLogger interface {
	Info(msg string, fields ...zap.Field)
	Warn(msg string, fields ...zap.Field)
}

// nopLogger 未注入时的安全默认值：什么都不打。
type nopLogger struct{}

func (nopLogger) Info(string, ...zap.Field) {}
func (nopLogger) Warn(string, ...zap.Field) {}

// SetLogger 注入内核的日志实现（module/kernel/parse_bridge.go 的 init() 调用）。
func SetLogger(l FieldLogger) {
	if l != nil {
		logger = l
	}
}
