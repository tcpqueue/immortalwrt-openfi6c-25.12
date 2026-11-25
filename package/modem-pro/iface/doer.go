package iface

// 这里放的是「内核必须向厂商层提供的最小能力」。
//
// Go 要求"方法与类型同包"：厂商指令类型（QuectelCommand / MT5700Command…）
// 的方法必须和类型住在一起。为了让它能不依赖内核，内核就以这两个窄接口的形式
// 把能力递进去 —— 厂商层只认接口，不认 *Modem。
//
// 引擎侧的调用点仍然原样传 *Modem，因为它实现了这两个接口。

// ATDoer 指令层需要的最小引擎能力：发一条 AT 指令、拿回应答行；外加日志用的模组号。
// 由 kernel 的 *Modem 实现（SendAT / ModemID / ModemModel）。
type ATDoer interface {
	// SendAT 发一条 AT 指令，返回应答行（已去 \r\n）
	SendAT(cmd string) ([]string, error)
	// ModemID 模组序号（日志里用来区分是哪个模组）
	ModemID() int
	// ModemModel 型号串（ATI 原文 + 短名）—— 少数方言要按**平台/型号**选参数形式
	// （如广和通 AT+GTACT 的三种写法，见 fibocomGTACTFormForModel）
	ModemModel() string
}

// LockATDoer 锁网类指令需要的引擎能力：在 ATDoer 之外，还要能"下发锁网指令"。
//
// 为什么单独一个接口：锁网指令在 kernel 里要先串行化、去抖、必要时确认回读
// （见 lock.go 的 sendLockCommand），不是裸的 SendAT。
type LockATDoer interface {
	ATDoer
	// SendLockCommand 下发一条锁网指令（kernel 侧负责框架性处理）
	SendLockCommand(cmd string) error
}
