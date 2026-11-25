package lock

// ====================== 锁频段 / 锁小区 方言一览（按品牌分文件） ======================
//
// 改哪一家就只看哪一行对应的文件：
//
//      品牌 / 平台                文件                        指令族
//      ------------------------  --------------------------  ----------------------------------
//      移远（旧款 4G）            quectel_qcfgband.go         AT+QCFG="band"（LTE 十六进制位图）
//      移远 / 广和通（高通新）      bandlist.go + bandlist_parse.go  AT+QNWPREFCFG / AT+QNWLOCK（"列表式"框架）
//      广和通 FM350-GL（MTK）      fibocom_mtk.go              AT+GTACT / AT+GTCELLLOCK（私有频段编码）
//      芯讯通（旧款 4G）           simcom_cnbp.go              AT+CNBP（十六进制位图）
//      芯讯通（高通）              simcom.go                   AT+CSYSSEL / AT+SCS…
//      展锐                       unisoc.go                   AT+SP…
//      鼎桥（MT5700）              （在 registry / variant 侧的 LockVendorOps）
//      高新兴                     gosuncn.go                  AT+ZBAND / AT+ZLOCKCELL
//      openluat                   openluat.go                 AT+CCED / AT+CBANDCFG…
//      Telit                      telit.go                    AT#BND
//      华为 / Sierra              huawei_sierra_atband.go     AT!BAND（两家同一套写法）
//
// ⚠️ **不要按品牌写死方言**：同一品牌不同固件可能是不同族（芯讯通就是两套），
//    真正的选择发生在 kernel 的方言探测（`lockDialectName`）里；本包只提供实现。
//
// 下面是"列表式"框架的只读访问器。
// ====================== 方言的只读访问器 ======================
//
// 内核（module/kernel/modem_at_plan.go 的 lockDialectName）要在界面上显示"当前用的是哪套方言"。
// 其中 BandListLocker / TelitLocker 的名字存在**未导出字段** `name` 里。
//
// 这里用访问器而不是把字段导出：字段一旦导出，module/lock 里所有
// `BandListLocker{name: …}` 构造点都要跟着改 —— 那会破坏
// "搬迁文件零改动" 这条线（见 deps.go 的说明）。

import "modemd/iface"

// dialectNamer 能自报名字的方言（"列表式"两族都实现了）
type dialectNamer interface{ DialectName() string }

// DialectName 取该方言自己的名字（没实现就返回 ""，由内核按类型名给固定文案）。
//
// 用接口断言而不是 switch 具体类型：方言已经按品牌搬进 module/<品牌>/lock.go，
// 本包不能再引用它们的类型。
func DialectName(l iface.NetLocker) string {
	if n, ok := l.(dialectNamer); ok {
		return n.DialectName()
	}
	return ""
}

// HasParseStateFn 该“列表式”方言是否挂了通用的状态解析器。
//
// 导出它是为了让 kernel 的回归测试能断言“`telit` 已改用自己的解析，不该再接通用解析器”——
// 这属于“实现细节的回归护栏”，用一个只读访问器表达比把字段导出干净
// （字段一旦导出，本包所有 `bandListLocker{parseStateFn: …}` 构造点都要改）。
func HasParseStateFn(b *BandListLocker) bool { return b.parseStateFn != nil }

// HasCapFn 该“列表式”方言是否接上了能力查询。
func HasCapFn(b *BandListLocker) bool { return b.capFn != nil }
