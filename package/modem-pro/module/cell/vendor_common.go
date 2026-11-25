package cell

// ====================== 本地别名（实现在 module/common） ======================
//
// 这些解析助手原本住在本包，按品牌拆目录后搬到了 module/common/atkv.go：
// 品牌包（module/thales、module/sierra …）要用它们，而它们反过来要 import 本包
// 才能拿到 CellInfo —— 住在 cell 里会形成循环依赖。
//
// 这里保留同名别名，cell.go 与既有单测一个字符都不用改。

import "modemd/module/common"

var (
	parseLabeledKV   = common.ParseLabeledKV
	firstNumber      = common.FirstNumber
	avgNumber        = common.AvgNumber
	atNumbersSigned  = common.AtNumbersSigned
	kvInt            = common.KVInt
	kvStr            = common.KVStr
	kvAvgInt         = common.KVAvgInt
	parseInlinePairs = common.ParseInlinePairs
	csvAfterPrefix   = common.CSVAfterPrefix
	normalizePLMN    = common.NormalizePLMN
	validPLMN        = common.ValidPLMN
)
