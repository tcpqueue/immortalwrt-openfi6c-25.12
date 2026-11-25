package kernel

import (
	"modemd/module/sms"
)

// ====================== 微信机器人：引擎侧接线 ======================
//
// 微信客户端（扫码登录 / 长轮询 / 发消息）与 iLink Bot 协议都在 wechat.go 段，
// 那边是纯业务：不认识 *Modem，也不认识 WebSocket。这里只做三件接线：
//
//	① 状态变化 → 通过 WebSocket 广播给页面（OnChange）；
//	② 短信钩子：把 *Modem 组装成 ModemInfo 再交给它；
//	③ 兼容别名：老调用点（api_sms.go / main.go）不用改。

type ()

var (
// wechatView 面板数据（老名字，实现在 wechat.go）
)

// InitWeChat 初始化微信机器人（main 里调用）并接上推送回调。
func InitWeChat() {
	OnChange = func(v WeChatView) { broadcast("wechat", nil, v) }
	InitWeChatRuntime()
}

// modemInfo 组装微信侧需要的模组信息（引擎里唯一知道 *Modem 的地方）
func modemInfo(mod *Modem) ModemInfo {
	if mod == nil {
		return ModemInfo{}
	}
	return ModemInfo{
		ID:        mod.ID,
		ATPort:    mod.ATPort,
		Model:     mod.Model,
		ModelName: mod.ModelName,
		IMEI:      mod.IMEI,
	}
}

// wechatOnSMS 收到短信列表后的转发钩子（push.go 在广播之前调用）
func wechatOnSMS(mod *Modem, list []sms.SMS) {
	OnSMS(modemInfo(mod), list)
}

// WeChatRenderSMS 渲染一条短信（接口层与单测用）
func WeChatRenderSMS(mod *Modem, s sms.SMS, tpl string) string {
	return RenderSMS(modemInfo(mod), s, tpl)
}
