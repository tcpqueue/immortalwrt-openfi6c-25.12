package kernel

import (
	"modemd/module/sms"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ====================== 按时间排序 ======================

// reSmsTime 匹配 3GPP 时间戳 `YY/MM/DD,HH:MM:SS±ZZ`（时区可选）。
// 例：25/04/29,10:56:44+32
var reSmsTime = regexp.MustCompile(`^(\d{2})/(\d{2})/(\d{2}),(\d{2}):(\d{2}):(\d{2})`)

// smsTimeKey 把短信时间戳转成可比较的整数（YY 视为 20YY）。
//
// 只取日期与时间，时区（±ZZ，以 15 分钟为单位）不参与比较：
// 同一张卡收到的短信时区一致，而负偏移在 3GPP 里是 2 的补码表示、容易误读，
// 强行解析反而可能把顺序搞反。解析失败返回 ok=false。
func TimeKey(ts string) (int64, bool) {
	m := reSmsTime.FindStringSubmatch(strings.TrimSpace(ts))
	if m == nil {
		return 0, false
	}
	n := func(i int) int64 {
		v, _ := strconv.ParseInt(m[i], 10, 64)
		return v
	}
	y, mo, d, h, mi, s := n(1), n(2), n(3), n(4), n(5), n(6)
	if mo < 1 || mo > 12 || d < 1 || d > 31 || h > 23 || mi > 59 || s > 59 {
		return 0, false // 模组偶尔会吐出全 0 或越界的时间，视为无效
	}
	// 各字段按固定权重拼成一个可直接比大小的数（YY 视为 20YY）
	return (2000+y)*10000000000 + mo*100000000 + d*1000000 + h*10000 + mi*100 + s, true
}

// sortSMSNewestFirst 按时间倒序排列短信（**最新的在最前面**）。
//
// 模组的存储位置顺序既不代表到达时间，也不代表会话顺序（新短信会占用被删除
// 记录空出来的位置），直接按位置展示会把最新一条夹在旧短信中间。
// 时间无法解析（未上报/格式异常）的条目排在最后，并保持它们原有的相对顺序。
func SortSMSNewestFirst(list []sms.SMS) []sms.SMS {
	// 注意：key 必须在比较函数里现算。sort 会就地重排切片，
	// 预先按原下标算好的 key 会和新位置张冠李戴。
	sort.SliceStable(list, func(a, b int) bool {
		ka, oka := TimeKey(list[a].Timestamp)
		kb, okb := TimeKey(list[b].Timestamp)
		switch {
		case oka && okb:
			return ka > kb // 时间大的（新的）排前面
		case oka:
			return true // 有效的排前面
		case okb:
			return false
		}
		return false
	})
	return list
}
