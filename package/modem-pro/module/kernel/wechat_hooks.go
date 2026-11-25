package kernel

import ()

// ====================== 引擎注入点 + 导出别名 ======================

// WriteJSON 统一 JSON 响应（由 kernel 注入，见 glue.go 的 init）。
// 默认实现只回一个 500 —— 正常启动时一定会被替换掉。

// decodeBody 解析 POST JSON 请求体（与 api.go 的同名函数同语义；
// 本包自带一份，免得到处注入小工具）

// 导出别名：引擎侧（glue.go 里桥回老名字）
var (
	PasswordRequired  = smsPasswordRequired
	PasswordIsDefault = smsPasswordIsDefault
	TokenValid        = smsTokenValid
	Unlocked          = smsUnlocked
	TokenFromRequest  = smsTokenFromRequest
	PasswordMatch     = smsPasswordMatch
	ValidPassword     = validSMSPassword
	ClientIP          = smsClientIP
)
