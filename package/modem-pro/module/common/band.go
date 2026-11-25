package common

import (
	"fmt"
	"strings"
)

// FormatBandList 把频段列表拼成指令里的写法："1:3:5"
//
// 去重并升序（模组对重复项可能报错，乱序也没意义），空列表返回空串。
//
// 为什么放在 module/common：各家锁频段方言（移远 QNWPREFCFG、芯讯通 CSYSSEL…）
// 与引擎侧（定时任务的锁频段动作）都要用这个写法，属于"厂商侧公共小工具"。
func FormatBandList(bands []int) string {
	uniq := make([]int, 0, len(bands))
	seen := make(map[int]bool)
	for _, b := range bands {
		if b <= 0 || seen[b] {
			continue
		}
		seen[b] = true
		uniq = append(uniq, b)
	}
	// 插入排序：频段数量很小（个位数），不值得引 sort
	for i := 1; i < len(uniq); i++ {
		for j := i; j > 0 && uniq[j] < uniq[j-1]; j-- {
			uniq[j], uniq[j-1] = uniq[j-1], uniq[j]
		}
	}
	parts := make([]string, 0, len(uniq))
	for _, b := range uniq {
		parts = append(parts, fmt.Sprint(b))
	}
	return strings.Join(parts, ":")
}
