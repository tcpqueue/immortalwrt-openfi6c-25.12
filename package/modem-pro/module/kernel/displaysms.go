package kernel

// 屏幕端（tftdisplay）「余额查询」页专用的发送结果推送。
//
// 背景（2026-10-07 用户要求）：SMS 页最后再一屏是个"按卡类型发查询余额短信"的页面，
// 屏幕把 `{"action":"sms_send","number":…,"text":…}` 发过来，这边**发完**（成功或失败）
// 都要回推一条，屏幕好把那行状态写出来（"已发送" / "发送失败：+CMS ERROR…"）。
//
// 消息形如：
//
//	{"type":"sendsms","modem_id":0,"at":…,
//	 "data":{"number":"10086","text":"CXHF","ok":true,"msg":"已发送"}}
//
// ⚠️ 与 mode_set 的 displayMode.Msg 一个套路（先回一条结果消息，屏幕只认最近一条）；
// 屏幕端的解析在 tftdisplay 的 src/utils/wsdata.c（case "sendsms"）。
type DisplaySendSMSInfo struct {
	Number string `json:"number"` // 目标号码（屏幕显示用）
	Text   string `json:"text"`   // 正文（屏幕显示用）
	OK     bool   `json:"ok"`     // true = 模组已受理
	Msg    string `json:"msg"`    // 给人看的一句话
}

// pushDisplaySMSResult 把一次 sms_send 的结果推给屏幕（没人连着时 broadcast 自己早退）。
func pushDisplaySMSResult(mod *Modem, number, text string, err error) {
	info := DisplaySendSMSInfo{
		Number: number,
		Text:   text,
		OK:     err == nil,
	}
	if err != nil {
		info.Msg = "发送失败：" + err.Error()
	} else {
		info.Msg = "已发送，等运营商回复"
	}
	id := mod.ID
	broadcast("sendsms", &id, info)
}
