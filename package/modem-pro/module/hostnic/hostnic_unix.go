//go:build !windows

package hostnic

// ====================== 命令输出的编码转换（Linux / OpenWrt） ======================
//
// unix 上 `ip` / `udhcpc` 的输出就是 UTF-8（跟 locale 走），不必转换；
// 但为了别把非法字节原样吐给前端，这里仍然过一遍 ToValidUTF8。

import "strings"

// DecodeHostNICOutput 把命令输出转成合法的 UTF-8 字符串
func DecodeHostNICOutput(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return strings.ToValidUTF8(string(b), "?")
}
