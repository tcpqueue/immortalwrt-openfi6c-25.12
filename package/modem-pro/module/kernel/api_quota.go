package kernel

import (
	"modemd/module/httpx"
	"net/http"
	"time"

	"go.uber.org/zap"
)

func apiQuota(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, quotaView(mod), "ok")
	}
}

// apiSetQuota POST /api/quota/set
//
//	{"modem_id":0,"enabled":true,"limit_gb":30,"action":"block","reset_day":1}
//
// 字段都用指针 = 支持"只改其中一项"；limit_* / used_* 三选一（GB / MB / 字节）。
func apiSetQuota(mgr *ModemManager) http.HandlerFunc {
	type reqBody struct {
		ModemID    int      `json:"modem_id"`
		Enabled    *bool    `json:"enabled"`
		LimitBytes *int64   `json:"limit_bytes"`
		LimitMB    *float64 `json:"limit_mb"`
		LimitGB    *float64 `json:"limit_gb"`
		CountUp    *bool    `json:"count_up"`
		CountDown  *bool    `json:"count_down"`
		Action     *string  `json:"action"`
		ResetDay   *int     `json:"reset_day"`
		UsedBytes  *int64   `json:"used_bytes"`
		UsedMB     *float64 `json:"used_mb"`
		UsedGB     *float64 `json:"used_gb"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var req reqBody
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		mod, err := modemByID(mgr, req.ModemID)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}

		// ---- 先校验（写完 AT 就改不回来的东西一律挡在前面）----
		limit := int64(-1)
		switch {
		case req.LimitBytes != nil:
			limit = *req.LimitBytes
		case req.LimitMB != nil:
			limit = int64(*req.LimitMB * 1024 * 1024)
		case req.LimitGB != nil:
			limit = int64(*req.LimitGB * 1024 * 1024 * 1024)
		}
		if limit >= 0 && limit > 0 && limit < 1024*1024 {
			httpx.WriteJSON(w, -1, nil, "上限太小（至少 1 MB），要关闭限制请填 0")
			return
		}
		if limit > 1<<60 { // 1 EB：明显是填错了
			httpx.WriteJSON(w, -1, nil, "上限过大（最多 1024 TB）")
			return
		}
		if req.Action != nil {
			switch *req.Action {
			case ActionBlock, ActionBlockRF, ActionWarn:
			default:
				httpx.WriteJSON(w, -1, nil, "action 只能是 block（断数据）/ block_rf（关射频）/ warn（只提醒）")
				return
			}
			// 「关射频」动作用的是同一个开关：机型不给开飞行模式的（F50）不许选，
			// 否则超限时会直接把用户的管理页/内网通道一起弄断。
			if *req.Action == ActionBlockRF {
				if why := mod.AirplaneBlockedReason(); why != "" {
					httpx.WriteJSON(w, -1, nil, why+"，请选「停用数据连接」或「只提醒」")
					return
				}
			}
		}
		if req.ResetDay != nil && (*req.ResetDay < 0 || *req.ResetDay > 28) {
			httpx.WriteJSON(w, -1, nil, "重置日只能是 1~28（0 = 不自动重置）")
			return
		}
		cur := quotaState(mod)
		countUp, countDown := cur.CountUp, cur.CountDown
		if req.CountUp != nil {
			countUp = *req.CountUp
		}
		if req.CountDown != nil {
			countDown = *req.CountDown
		}
		if !countUp && !countDown {
			httpx.WriteJSON(w, -1, nil, "上下行至少要计入一个方向")
			return
		}
		used := int64(-1)
		switch {
		case req.UsedBytes != nil:
			used = *req.UsedBytes
		case req.UsedMB != nil:
			used = int64(*req.UsedMB * 1024 * 1024)
		case req.UsedGB != nil:
			used = int64(*req.UsedGB * 1024 * 1024 * 1024)
		}
		if used < -1 {
			httpx.WriteJSON(w, -1, nil, "已用流量不能是负数")
			return
		}

		quotaUpdate(mod, func(s *State) {
			if req.Enabled != nil {
				s.Enabled = *req.Enabled
			}
			if limit >= 0 {
				s.LimitBytes = limit
			}
			if req.CountUp != nil {
				s.CountUp = *req.CountUp
			}
			if req.CountDown != nil {
				s.CountDown = *req.CountDown
			}
			if req.Action != nil {
				s.Action = *req.Action
			}
			if req.ResetDay != nil {
				s.ResetDay = *req.ResetDay
				s.Period = Period(time.Now(), s.ResetDay) // 改周期立即重算
			}
			if used >= 0 {
				// 手动校正用量：按比例摊到上下行（页面只让填一个总数）
				if s.CountUp && !s.CountDown {
					s.UsedUp, s.UsedDown = used, 0
				} else if s.CountDown && !s.CountUp {
					s.UsedUp, s.UsedDown = 0, used
				} else {
					s.UsedUp, s.UsedDown = used/2, used-used/2
				}
			}
			// 改了上限/动作/周期，等于重新表态：把"本周期不再限制"解除
			s.Suspended = false
		})

		// 改完立刻评估一次：把上限调到低于已用量时应该马上断
		changed := mod.quotaEvaluate()
		FlushQuota()
		if !changed {
			pushQuota(mod, true)
		}
		httpx.WriteJSON(w, 0, quotaView(mod), "ok")
	}
}

// apiResetQuota POST /api/quota/reset {"modem_id":0}
// 手动清零并开始新周期（换卡/换套餐/对账之后用）。
func apiResetQuota(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID int `json:"modem_id"`
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
		quotaUpdate(mod, func(s *State) {
			s.UsedUp, s.UsedDown = 0, 0
			s.Period = Period(time.Now(), s.ResetDay)
			s.Suspended = false
		})
		// 清零后不再超限 → 评估会自动把数据连接恢复回来
		changed := mod.quotaEvaluate()
		FlushQuota()
		if !changed {
			pushQuota(mod, true)
		}
		httpx.WriteJSON(w, 0, quotaView(mod), "ok")
	}
}

// apiUnblockQuota POST /api/quota/unblock {"modem_id":0}
// 「本周期不再限制」：立刻恢复数据连接，并把本周期剩下的时间豁免掉
// （不加这一步的话，因为用量仍在上限之上，下一轮评估会立刻又断开）。
func apiUnblockQuota(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID int `json:"modem_id"`
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
		// 恢复方式随“当时断网的动作”变：block = 激活 PDP，block_rf = 关掉飞行模式
		quotaUpdate(mod, func(s *State) { s.Suspended = true })
		if err := mod.setQuotaBlock(false, quotaState(mod).BlockedAction()); err != nil {
			logger.Warn("quota manual unblock failed", zap.Int("modem", mod.ID), zap.Error(err))
			httpx.WriteJSON(w, -1, quotaView(mod), "恢复联网失败: "+err.Error())
			return
		}
		quotaUpdate(mod, func(s *State) { s.Blocked = false; s.BlockedBy = "" })
		FlushQuota()
		pushQuota(mod, true)
		httpx.WriteJSON(w, 0, quotaView(mod), "ok")
	}
}

// ---- from: api_params.go ----

// apiParams GET /api/params?modem_id=0
//
// 一次返回全部可写参数的当前状态：PDP 上下文（含激活状态）、当前在用 APN、选网方式。
