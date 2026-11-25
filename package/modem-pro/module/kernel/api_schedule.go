package kernel

import (
	"modemd/module/httpx"
	"net/http"

	"go.uber.org/zap"
)

// ====================== 定时器 API ======================
//
//	GET  /api/schedule          定时列表 + 可选动作 + 目标模组下拉（页面一次拉全）
//	POST /api/schedule/set      新增 / 更新一条（body 就是 Task；id 为空则新建）
//	POST /api/schedule/delete   {"id":"..."}
//	POST /api/schedule/toggle   {"id":"...","enabled":true}
//	POST /api/schedule/run      {"id":"..."}  立即执行一次（不推进槽位记录）

// apiSchedule GET /api/schedule[?modem_id=0]
func apiSchedule(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, 0, scheduleView(mgr), "ok")
	}
}

// apiScheduleSet POST /api/schedule/set
//
// body：Task 的字段（不含 id 即为新增）；另可带 modem_id，用于目标模组留空时的回退。
// ⚠️ Task 内嵌在这里时它的 UnmarshalJSON **不会被调用**（encoding/json 对匿名内嵌字段做扁平化），
// 所以旧版的 start_action / end_action 单动作写法在下面另外接一次。
func apiScheduleSet(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Task
			ModemID int `json:"modem_id"`
			// 兼容早期前端的单动作写法（等同于只有一个元素的动作列表）
			LegacyStart *Action `json:"start_action"`
			LegacyEnd   *Action `json:"end_action"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		t := req.Task
		if len(t.StartActions) == 0 && req.LegacyStart != nil {
			t.StartActions = []Action{*req.LegacyStart}
		}
		if len(t.EndActions) == 0 && req.LegacyEnd != nil {
			t.EndActions = []Action{*req.LegacyEnd}
		}
		if t.ID == "" {
			t.ID = newSchedID()
		}
		schedFillTarget(mgr, &t, req.ModemID)
		if err := schedUpsert(&t); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		logger.Info("schedule task saved",
			zap.String("id", t.ID), zap.String("name", t.Name),
			zap.String("modem", t.ModemKey), zap.Bool("enabled", t.Enabled),
			zap.String("start", t.StartTime), zap.String("start_actions", ActionsLabel(t.StartActions)),
			zap.String("end", t.EndTime), zap.String("end_actions", ActionsLabel(t.EndActions)))

		v := scheduleView(mgr)
		broadcast("schedule", nil, v)
		httpx.WriteJSON(w, 0, v, "ok")
	}
}

// apiScheduleDelete POST /api/schedule/delete
func apiScheduleDelete(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		if !schedDelete(req.ID) {
			httpx.WriteJSON(w, -1, nil, "任务不存在（可能已被删除）")
			return
		}
		logger.Info("schedule task deleted", zap.String("id", req.ID))
		v := scheduleView(mgr)
		broadcast("schedule", nil, v)
		httpx.WriteJSON(w, 0, v, "ok")
	}
}

// apiScheduleToggle POST /api/schedule/toggle  {"id":"...","enabled":false}
func apiScheduleToggle(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		var target *Task
		for _, t := range schedSnapshot() {
			if t.ID == req.ID {
				target = t
				break
			}
		}
		if target == nil {
			httpx.WriteJSON(w, -1, nil, "任务不存在（可能已被删除）")
			return
		}
		target.Enabled = req.Enabled
		if err := schedUpsert(target); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		logger.Info("schedule task toggled", zap.String("id", req.ID), zap.Bool("enabled", req.Enabled))
		v := scheduleView(mgr)
		broadcast("schedule", nil, v)
		httpx.WriteJSON(w, 0, v, "ok")
	}
}

// apiScheduleRun POST /api/schedule/run  立即执行一次开始动作（调试用）
func apiScheduleRun(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		run, err := RunSchedTaskNow(mgr, req.ID)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		if !run.OK {
			// 执行了但模组拒绝：如实返回错误文本（界面直接显示，便于对账）
			httpx.WriteJSON(w, -1, scheduleView(mgr), run.Msg)
			return
		}
		httpx.WriteJSON(w, 0, scheduleView(mgr), "已执行")
	}
}

// ---- from: api_modem.go ----

// ---- from: api_modem.go ----
// apiListModems GET /api/modems 获取所有模组列表（**读缓存**：不枚举串口、不发 AT 探测）。
// 页面上的「刷新模组」走的就是这条：模组列表本来就在内存里，直接回就行。
// 带 ?discover=1 时在**后台**触发一次强制重探，用于排障 / 脚本；
// 前端只在缓存为**空**（什么都刷不出来）时才补这一次。
