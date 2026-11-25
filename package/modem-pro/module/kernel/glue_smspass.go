package kernel

import (
	"modemd/module/config"
	"modemd/module/httpx"
	"net/http"
)

// ====================== 短信访问密码：引擎侧接线 ======================
//
// 令牌、密码校验、失败节流与 4 个接口都在 wechat.go；
// 这里只做两件引擎才有的事：
//
//	① 把统一 JSON 响应 WriteJSON 注入给它（它不该知道 kernel 的响应格式）；
//	② 保留 smsGate：闸门要往外写 code=401 的响应，那属于引擎的响应约定。

// 兼容别名：老调用点（api.go / glue.go / main.go）按老名字用
var ()

// apiSMSAuth / apiSMSUnlock / apiSMSLock / apiSMSPasswordSet 的兼容包装：
// 处理器本身不需要 ModemManager，签名保持一致是为了路由注册处不用改。
var (
	apiSMSAuth        = func(*ModemManager) http.HandlerFunc { return APIAuth() }
	apiSMSUnlock      = func(*ModemManager) http.HandlerFunc { return APIUnlock() }
	apiSMSLock        = func(*ModemManager) http.HandlerFunc { return APILock() }
	apiSMSPasswordSet = func(*ModemManager) http.HandlerFunc { return APIPasswordSet() }
)

// smsGate 短信接口的统一闸门：没解锁就回 code=401（前端据此弹密码框）。
// 返回 false 表示已经写过响应，调用方直接 return。
func smsGate(w http.ResponseWriter, r *http.Request) bool {
	if smsUnlocked(r) {
		return true
	}
	httpx.WriteJSON(w, 401, map[string]any{"locked": true, "required": true}, "短信需要密码，请先解锁")
	return false
}

// ---- from: log.go ----

// logger 日志门面：真正的 *zap.Logger 由 main 里 Init 设置。
// 写成门面值是为了让各文件继续用 logger.Info(...) 这种写法，不必每处都带包名。
var logger = config.Logger

// ---- from: wechat_glue.go ----

//
// 微信客户端（扫码登录 / 长轮询 / 发消息）与 iLink Bot 协议都在 wechat.go 段，
// 那边是纯业务：不认识 *Modem，也不认识 WebSocket。这里只做三件接线：
//
//	① 状态变化 → 通过 WebSocket 广播给页面（OnChange）；
//	② 短信钩子：把 *Modem 组装成 ModemInfo 再交给它；
//	③ 兼容别名：老调用点（api_sms.go / main.go）不用改。
