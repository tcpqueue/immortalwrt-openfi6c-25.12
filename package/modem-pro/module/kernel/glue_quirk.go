package kernel

import (
	"modemd/module/modelquirk"
)

// ====================== 机型特例查询（引擎侧胶水） ======================
//
// 特例表本身在 atcmd.go（纯数据 + 纯匹配）；
// 这里只负责“拿到本模组的型号串再查表”，因为型号存在 Modem 里、要加锁读。

// Quirk 返回当前模组的特例（没命中就返回零值）。
//
// ⚠️ 内部会取 mod.mu，**不要在已经持有 mod.mu 的地方调用**。
func (mod *Modem) Quirk() modelquirk.Quirk {
	mod.mu.Lock()
	model := mod.Model
	mod.mu.Unlock()
	return modelquirk.Lookup(model)
}

// ---- from: modem_signalhistory.go ----

//
// 存储本身在 history.go（纯存储、只认 modemID + 数值）；这里负责两件
// 与模组强相关的事：① 判定"哪些值算有效"（要 SignalInfo 的量纲与哨兵值规则）；
// ② 把一次信号查询结果转成采样点喂给存储。
// validSignal 取出**有效**的信号量（指针为 nil 表示该次未上报）。
//
// 判定规则与前端 renderSignal 的校验完全一致，也与波动记录共用：
//   - 超出物理范围（实测见过 SINR=-3276）一律视为未上报；
//   - RSRQ=0、RSSI=0/99 是模组的"未上报"哨兵值，同样视为无效；
//   - RSSI 由 CSQ 的 0..31 换算成 dBm（×2-113），否则纵轴没法与其它值并列。
