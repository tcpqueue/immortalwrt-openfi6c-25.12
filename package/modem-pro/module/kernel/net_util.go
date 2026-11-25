package kernel

import ()

// ====================== 小工具 ======================

// FirstLine 取应答首行，用于错误信息（避免把整段应答塞进日志/响应）
func FirstLine(resp []string) string {
	if len(resp) == 0 {
		return "(empty)"
	}
	return resp[0]
}
