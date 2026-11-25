package common

// ====================== AT 应答通用小工具 ======================
//
// 这三个原来分别住在 lock / imeidialect 里，是「谁先用上就放谁那儿」的历史遗留。
// 按品牌拆目录之后必须下移到 common：品牌包（module/quectel 等）要用它们，
// 而 lock / imeidialect 反过来要 import 品牌包 ⇒ 放在 lock 里会形成循环依赖。
// lock / imeidialect 保留同名别名，老代码不用改。

import (
	"strconv"
	"strings"
)

// SplitCSVFields 按逗号切字段并去掉引号/空白
func SplitCSVFields(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(strings.Trim(strings.TrimSpace(p), `"`)))
	}
	return out
}

// AtNumbers 取字符串里的所有整数（用于"尽力解析"未逐字确认的应答）
func AtNumbers(s string) []int {
	var out []int
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() > 0 {
			if n, err := strconv.Atoi(cur.String()); err == nil {
				out = append(out, n)
			}
			cur.Reset()
		}
	}
	for _, r := range s {
		if r >= '0' && r <= '9' {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// NumInLine 取一行里第 idx 个（0 起）数字串
func NumInLine(line string, idx int) (int, bool) {
	nums := AtNumbers(line)
	if idx < 0 || idx >= len(nums) {
		return 0, false
	}
	return nums[idx], true
}
