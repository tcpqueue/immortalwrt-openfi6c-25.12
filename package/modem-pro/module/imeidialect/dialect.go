package imeidialect

// ====================== IMEI 改写方言库 ======================
//
// 从 module/kernel/vendor_settings_imei.go 搬过来：这些**全是纯函数 + 数据**
// （`set func(imei string) string` + 一段说明文字），只依赖字符串工具，
// 不碰任何内核状态。
//
// 留在 kernel 的是那两个必须读内核的东西：
//
//	imeiDialectFor(mod *Modem) —— 要读 mod.Vendor / mod.Model，还要查机型变体表
//	imeiApply(mod *Modem, cmd) —— 要 mod.SendAT
//
// ⚠️ 数字抽取工具（atNumbers）住在 module/common：品牌包要用它，而品牌包反过来要
// import 本包 ⇒ 工具不能住在会依赖品牌包的 lock 里。

import (
	"regexp"
	"strconv"
	"strings"

	"modemd/module/common"
)

var atNumbers = common.AtNumbers

// ImeiDialect IMEI 改写的方言
type ImeiDialect struct {
	// Set 由 IMEI 生成写入指令；返回 "" 表示不支持
	Set  func(imei string) string
	Note string
}

// ====================== 小工具 ======================

// NumInLine 取一行里第 idx 个（0 起）数字串（实现在 module/common）
var NumInLine = common.NumInLine

// LineHas 应答里是否存在含 sub 的行（忽略大小写）
func LineHas(resp []string, sub string) bool {
	u := strings.ToUpper(sub)
	for _, l := range resp {
		if strings.Contains(strings.ToUpper(l), u) {
			return true
		}
	}
	return false
}

// ReHex4 找 4 位十六进制（foxconn USBSWITCH 的 90d5/9025 这类）
var ReHex4 = regexp.MustCompile(`(?i)\b(9[0-9a-f]{3})\b`)

// RawValueParser 从应答里取"设备自己报的原始取值"（第一个数字串，或第一个十六进制 token）
//
// 给"有指令但取值表未取证"的厂商用：能显示、能把原值写回去，但不假装知道含义。
func RawValueParser(resp []string) (string, bool) {
	for _, l := range resp {
		t := strings.TrimSpace(l)
		if t == "" || strings.EqualFold(t, "OK") {
			continue
		}
		if v, ok := NumInLine(t, 0); ok {
			return strconv.Itoa(v), true
		}
		if m := ReHex4.FindStringSubmatch(t); m != nil {
			return strings.ToLower(m[1]), true
		}
	}
	return "", false
}

// ValidIMEI IMEI 是否为 15 位数字（不校验 Luhn —— 很多模组的"自定义 IMEI"也不合规，
// 但那正是用户想写的值，所以只挡长度/字符这类明显写错的情况）
func ValidIMEI(s string) bool {
	if len(s) != 15 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
