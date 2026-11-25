package atparse

// ====================== 厂商私有温度格式的注册表 ======================
//
// 与 module/cell 同一手法：解析器按品牌搬进 module/<品牌>/atparse.go，
// 而它们必须 import 本包（要 Sensor）⇒ 本包不能反过来 import 品牌包。
// 所以品牌包在自己 init() 里登记，本包只按 order 依次试。
//
// ⚠️ order 小的先试：与搬家前 ParseTempResp 的链条顺序一致
// （Telit → Meig → Thales → Sierra）。

import "sort"

// TempParser 温度解析器（认不出返回 nil/空）
type TempParser func(resp []string) []Sensor

type tempEntry struct {
	order int
	fn    TempParser
}

var tempEntries []tempEntry

// RegisterTempParser 登记一个厂商私有的温度解析器（在品牌包的 init() 里调用）。
func RegisterTempParser(order int, fn TempParser) {
	tempEntries = append(tempEntries, tempEntry{order: order, fn: fn})
	sort.SliceStable(tempEntries, func(i, j int) bool {
		return tempEntries[i].order < tempEntries[j].order
	})
}

// RegisteredTempParsers 已登记的数量（单测/自检用）
func RegisteredTempParsers() int { return len(tempEntries) }
