package kernel

import (
	"encoding/json"
	"modemd/module/httpx"
	"net/http"
	"strconv"

	"go.uber.org/zap"
)

// ====================== 模组信息 / 短信存储管理 ======================

// ---- from: api_sms.go ----

// ---- from: api_sms.go ----
// apiSendSMS POST /api/sms/send 发送短信
// apiSendSMS POST /api/sms/send 发送短信

func apiSendSMS(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpx.WriteJSON(w, -1, nil, "method not allowed, use POST")
			return
		}
		if !smsGate(w, r) {
			return
		}
		var req struct {
			ModemID int    `json:"modem_id"`
			Phone   string `json:"phone"`
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteJSON(w, -1, nil, "bad request: "+err.Error())
			return
		}
		if req.ModemID < 0 || req.Phone == "" || req.Content == "" {
			httpx.WriteJSON(w, -1, nil, "modem_id / phone / content required")
			return
		}
		// 号码/内容先校验再下发：非法号码会被模组拒绝，以前却仍回 ok（界面提示"发送成功"），
		// 用户完全对不上 —— 这里给出明确原因，前端直接就能看懂
		if !validSMSPhone(req.Phone) {
			httpx.WriteJSON(w, -1, nil, "收信号码不合法：只能是数字，可带开头的 +，长度 5~21")
			return
		}
		if !validSMSContent(req.Content) {
			httpx.WriteJSON(w, -1, nil, "短信内容不合法：不能为空、不能含 Ctrl+Z、长度不超过 2000 字符")
			return
		}
		mod, ok := mgr.modemByID(req.ModemID)
		if !ok {
			httpx.WriteJSON(w, -1, nil, "modem not found")
			return
		}
		if err := mod.SendSMS(req.Phone, req.Content); err != nil {
			logger.Warn("send sms failed", zap.Int("modem", req.ModemID), zap.Error(err))
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, nil, "ok")
	}
}

// apiReadSMS GET /api/sms/read?modem_id=0 读取短信
func apiReadSMS(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !smsGate(w, r) {
			return
		}
		modemID, _ := strconv.Atoi(r.URL.Query().Get("modem_id"))
		mod, ok := mgr.modemByID(modemID)
		if !ok {
			httpx.WriteJSON(w, -1, nil, "modem not found")
			return
		}
		sms, err := mod.ReadSMS()
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, sms, "ok")
	}
}

// apiSMSStorage 短信存储用量与存储区切换（同时会把未读标记成已读，属于敏感操作）
//
//	GET  /api/sms/storage?modem_id=0                 读用量 + 当前存储区 + 支持的存储区
//	POST /api/sms/storage {"modem_id":0,"mem":"SM"}  切换存储区（AT+CPMS，带回读确认 + 写回 config.toml）
func apiSMSStorage(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !smsGate(w, r) {
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				ModemID int    `json:"modem_id"`
				Mem     string `json:"mem"`
			}
			if err := httpx.DecodeBody(r, &req); err != nil {
				httpx.WriteJSON(w, -1, nil, err.Error())
				return
			}
			mod, err := modemByID(mgr, req.ModemID)
			if err != nil {
				httpx.WriteJSON(w, -1, nil, err.Error())
				return
			}
			st, err := mod.SetSMSStorage(req.Mem)
			if err != nil {
				logger.Warn("switch sms storage failed",
					zap.Int("modem", req.ModemID), zap.String("mem", req.Mem), zap.Error(err))
				// 回读不一致时 st 仍然有效（已刷新），一并返回方便前端重绘
				if st != nil {
					httpx.WriteJSON(w, 0, st, err.Error())
					return
				}
				httpx.WriteJSON(w, -1, nil, err.Error())
				return
			}
			httpx.WriteJSON(w, 0, st, "ok, 已切换到 "+st.Current+" 存储区")
			return
		}
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		st, err := mod.QuerySMSStorage()
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, st, "ok")
	}
}

// apiDeleteSMS POST /api/sms/delete
//
//	{"modem_id":0,"index":3}   删除指定位置的短信
//	{"modem_id":0,"all":true}  删除全部短信
func apiDeleteSMS(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !smsGate(w, r) {
			return
		}
		var req struct {
			ModemID int  `json:"modem_id"`
			Index   int  `json:"index"`
			All     bool `json:"all"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		mod, err := modemByID(mgr, req.ModemID)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		index := req.Index
		if req.All {
			index = -1
		} else if index <= 0 {
			httpx.WriteJSON(w, -1, nil, "index must be >= 1, or set all=true")
			return
		}
		if err := mod.DeleteSMS(index); err != nil {
			logger.Warn("delete sms failed", zap.Int("modem", req.ModemID), zap.Error(err))
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		// 重读一遍并广播（ReadSMS 末尾会 pushSMS）：存储用量与列表在**别的页面**
		// 也得跟着变，不能只靠发请求的那个页面自己刷。异步出去，别让删一条短信也等一次 CMGL。
		go mod.ReadSMS()
		httpx.WriteJSON(w, 0, nil, "ok")
	}
}

// ---- from: wechat_api.go ----

//
//	GET  /api/wechat          面板数据（配置 + 登录状态 + 最近发送结果）
//	POST /api/wechat/set      保存配置（enabled / to_user_id / template / listen / …）
//	POST /api/wechat/login    申请二维码，开始扫码登录
//	POST /api/wechat/verify   提交配对码 {"code":"1234"}
//	POST /api/wechat/logout   退出登录（清掉凭据）
//	POST /api/wechat/test     发一条测试消息
//
// ⚠️ 全部走短信密码闸门（smsGate）：机器人凭据比短信本身还敏感 ——
// 能改推送目标就等于能把短信转发到任意微信，所以未解锁一律 401。
