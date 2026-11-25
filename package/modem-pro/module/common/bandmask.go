package common

// ====================== 「64 位两段位图」共享工具 ======================
//
// 华为 / Sierra 的 `AT!BAND`（模板 + 位图）与 Telit 的 `AT#BND`（尾参是位图）用的是
// **同一套口径**，依据都是 QModem 的实现：
//
//	_band_list_to_mask:  band ≤ 64 → low 的第 (band-1) 位；band ≥ 65 → high 的第 (band-65) 位；
//	                     输出 `printf "%016x" $low` 与 `printf "%016x" $high`，再 echo "$low,$high"
//	telit *_to_hex:      `echo "obase=16; $decimal_value" | bc`（大写、不补零）
//
// 所以这里提供两套输出：AT!BAND 用**补零 16 位小写**（与 %016x 一致），
// Telit 用**不补零大写**（与 bc 一致）。输入解析则统一宽容处理（括号 / 引号 / 0x 前缀）。
//
// 这几件事与模组状态无关，纯字符串/大整数运算 ⇒ 住在厂商层与内核共用的 module/common。
// kernel 保留搬家前的老名字（cleanHexField / splitBandBitmap …）作为别名。

import (
	"math/big"
	"strings"
)

// CleanHexField 去掉括号、引号与空白（能力查询的组是 `(3FFFFFFFFF)` 这种）
func CleanHexField(s string) string {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), `"`))
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimSuffix(s, ")")
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	return strings.TrimSpace(s)
}

// IsHexBitmap 判断是不是一个十六进制位图（非空，且全是 0-9A-F）
func IsHexBitmap(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range strings.ToUpper(s) {
		if !strings.ContainsRune("0123456789ABCDEF", r) {
			return false
		}
	}
	return true
}

// HexBitmapBands 十六进制位图 → 频段列表。
// offset=0 时 band = bit+1（低位段 1..64）；offset=64 时 band = bit+65（高位段 65..128）。
func HexBitmapBands(hexStr string, offset int) []int {
	s := CleanHexField(hexStr)
	if !IsHexBitmap(s) {
		return nil
	}
	v, ok := new(big.Int).SetString(s, 16)
	if !ok {
		return nil
	}
	var out []int
	for bit := 0; bit < 64; bit++ { // 每段只覆盖 64 个频段
		if v.Bit(bit) == 1 {
			out = append(out, bit+1+offset)
		}
	}
	return out
}

// SplitBandBitmap 频段列表 → 两段位图（低位 1..64 / 高位 65..128）；越界频段忽略
func SplitBandBitmap(bands []int) (low, high *big.Int) {
	low, high = new(big.Int), new(big.Int)
	for _, b := range bands {
		switch {
		case b >= 1 && b <= 64:
			low.SetBit(low, b-1, 1)
		case b >= 65 && b <= 128:
			high.SetBit(high, b-65, 1)
		}
	}
	return
}

// HexBitmapString 位图 → bc 风格串（大写、不补零）；0 输出 "0"
func HexBitmapString(v *big.Int) string {
	return strings.ToUpper(v.Text(16))
}

// HexBitmapPadded 位图 → 补足 16 位的小写十六进制（与 QModem 的 `printf "%016x"` 一致）
func HexBitmapPadded(v *big.Int) string {
	h := v.Text(16)
	if len(h) < 16 {
		h = strings.Repeat("0", 16-len(h)) + h
	}
	return h
}
