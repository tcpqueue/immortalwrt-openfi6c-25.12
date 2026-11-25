//go:build windows

package hostnic

// ====================== 命令输出的编码转换（Windows） ======================
//
// ⚠️ 实测踩到的坑：Windows 上 `netsh` / `ipconfig` 的中文输出走的是**系统 ANSI/OEM 代码页**
// （中文 Windows = GBK/936），不是 UTF-8。原样塞进 Go 字符串 → JSON → 页面就是乱码
// （实机看到 `Windows IP ����`）。所以这里把非 UTF-8 的字节按系统代码页解一次。
//
// 用 MultiByteToWideChar(CP_ACP)：它就是"系统 ANSI 代码页"，与这些程序重定向输出时的编码一致。
// 已经是合法 UTF-8 的（程序自己输出了 UTF-8）原样返回，不做二次转换。

import (
	"strings"
	"syscall"
	"unicode/utf8"
	"unsafe"
)

var (
	modkernel32             = syscall.NewLazyDLL("kernel32.dll")
	procMultiByteToWideChar = modkernel32.NewProc("MultiByteToWideChar")
)

const cpACP = 0 // CP_ACP：系统 ANSI 代码页

// DecodeHostNICOutput 把命令输出转成合法的 UTF-8 字符串
func DecodeHostNICOutput(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if utf8.Valid(b) {
		return string(b)
	}
	if s := ansiToUTF8(b); s != "" {
		return s
	}
	// 兜底：至少别把非法字节原样吐给前端
	return strings.ToValidUTF8(string(b), "?")
}

// ansiToUTF8 按系统 ANSI 代码页解字节（失败返回空串）
func ansiToUTF8(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	// 先问需要多少个 UTF-16 码元
	n, _, _ := procMultiByteToWideChar.Call(
		cpACP, 0,
		uintptr(unsafe.Pointer(&b[0])),
		uintptr(int32(len(b))),
		0, 0,
	)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n)
	ret, _, _ := procMultiByteToWideChar.Call(
		cpACP, 0,
		uintptr(unsafe.Pointer(&b[0])),
		uintptr(int32(len(b))),
		uintptr(unsafe.Pointer(&buf[0])),
		n,
	)
	if ret == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}
