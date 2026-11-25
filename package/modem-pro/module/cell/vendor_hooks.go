package cell

// ====================== 厂商私有格式的注册表 ======================
//
// 为什么是"注册表"而不是 switch：解析器已经按品牌搬进 module/<品牌>/cell.go，
// 而它们必须 import 本包（要 CellInfo）⇒ **本包不能反过来 import 品牌包**，
// 否则就是循环依赖。
//
// 所以方向反过来：品牌包在自己的 init() 里把自己登记进来。
// 好处是新增一家厂商 = 只加一个 `module/<品牌>/cell.go`，**本包一行都不用改**。
//
// ⚠️ 注册顺序：order 小的先试。给"同一批标签可能有多个解析器"的场合留出口
// （Sierra 的 `AT!GSTATUS?` 与 Thales/Foxconn 的 `AT^DEBUG` 都认 `RAT:` 这类标签，
// Sierra 必须排在前面 —— 与搬家前的 switch 顺序一致）。
//
// ⚠️ 匹配口径：注册时给的 tags 是**应答行前缀**（大写比较），
// 只有某一行以它开头才会调用该解析器 —— 所以别的机型的应答不会被误吃。

import (
	"sort"
	"strings"
)

// ServingCellsParser 服务小区解析器（认不出返回 false）
type ServingCellsParser func(resp []string) (CellInfo, bool)

// NeighbourCellsParser 邻区解析器（认不出返回 nil）
type NeighbourCellsParser func(resp []string) []CellInfo

type servingEntry struct {
	order int
	tags  []string
	fn    ServingCellsParser
}

type neighbourEntry struct {
	tags []string
	fn   NeighbourCellsParser
}

var (
	servingEntries   []servingEntry
	neighbourEntries []neighbourEntry
)

// RegisterServingCells 登记一个厂商私有的服务小区解析器（在品牌包的 init() 里调用）。
func RegisterServingCells(order int, fn ServingCellsParser, tags ...string) {
	up := make([]string, 0, len(tags))
	for _, t := range tags {
		up = append(up, strings.ToUpper(t))
	}
	servingEntries = append(servingEntries, servingEntry{order: order, tags: up, fn: fn})
	sort.SliceStable(servingEntries, func(i, j int) bool {
		return servingEntries[i].order < servingEntries[j].order
	})
}

// RegisterNeighbourCells 登记一个厂商私有的邻区解析器（在品牌包的 init() 里调用）。
func RegisterNeighbourCells(fn NeighbourCellsParser, tags ...string) {
	up := make([]string, 0, len(tags))
	for _, t := range tags {
		up = append(up, strings.ToUpper(t))
	}
	neighbourEntries = append(neighbourEntries, neighbourEntry{tags: up, fn: fn})
}

// RegisteredServingCells 已登记的服务小区解析器数量（单测/自检用）
func RegisteredServingCells() int { return len(servingEntries) }

// RegisteredNeighbourCells 已登记的邻区解析器数量（单测/自检用）
func RegisteredNeighbourCells() int { return len(neighbourEntries) }
