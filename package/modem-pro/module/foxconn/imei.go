package foxconn

import (
	"strings"
)

// IMEINV550 生成 Foxconn 的 NV 550 写入指令序列（编码规则逐行照抄 QModem vendor/foxconn.sh set_imei）
//
//	extended = "80A" + imei；每两个字符交换顺序；再每两位加逗号、转小写
//	例如 869999000000001 → 80A869999000000001 → 08 a8 68 99 … → 08,a8,68,99,…
//
// 脚本里是先 `cmd_nv_550_clear`（NV 清 0）再写；这里用 `;` 把两条串起来，由内核的 imeiApply 拆开发。
func IMEINV550(imei string) string {
	extended := "80A" + imei
	var swapped strings.Builder
	for i := 0; i < len(extended); i += 2 {
		if i+1 < len(extended) {
			swapped.WriteByte(extended[i+1])
			swapped.WriteByte(extended[i])
		} else {
			swapped.WriteByte(extended[i])
		}
	}
	pairs := strings.ToLower(swapped.String())
	var withCommas []string
	for i := 0; i < len(pairs); i += 2 {
		if i+1 < len(pairs) {
			withCommas = append(withCommas, pairs[i:i+2])
		} else {
			withCommas = append(withCommas, pairs[i:i+1])
		}
	}
	return "AT!nv=550,\"0\";AT!nv=550,9,\"" + strings.Join(withCommas, ",") + "\""
}
