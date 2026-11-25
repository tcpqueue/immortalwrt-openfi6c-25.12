package kernel

import (
	"modemd/module/httpx"
	"net/http"
	"strconv"
	"time"
)

// ====================== 波动记录（网络 / 信号变化时间线） ======================

// apiChanges GET /api/changes[?modem_id=0][&limit=200]
//
// 返回波动记录（最新在前）+ 各字段当前值。
// modem_id 省略/为负时返回全部模组的记录（多模组时前端自己筛选）。
// limit 默认 200，上限 1000（环形缓冲最多 500 条，所以超过 500 无意义）。
func apiChanges(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		modemID := -1
		if v := r.URL.Query().Get("modem_id"); v != "" {
			id, err := strconv.Atoi(v)
			if err != nil {
				httpx.WriteJSON(w, -1, nil, "modem_id must be an integer")
				return
			}
			modemID = id
		}
		limit := 200
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				httpx.WriteJSON(w, -1, nil, "limit must be an integer")
				return
			}
			limit = n
		}
		if limit <= 0 {
			limit = 200
		}
		if limit > 1000 {
			limit = 1000
		}
		entries := ChangeGlobal.Snapshot(modemID, limit)
		httpx.WriteJSON(w, 0, map[string]any{
			"modem_id": modemID,
			"total":    ChangeGlobal.CountFor(modemID),
			"limit":    limit,
			"max":      GlobalMax,
			"entries":  entries,
			"current":  ChangeGlobal.Current(modemID),
		}, "ok")
	}
}

// apiClearChanges POST /api/changes/clear  {"modem_id":0}
//
// modem_id 省略/为负时清空全部模组。清空会连同基线一并清掉，
// 所以下一轮采样只是重建基线，不会立刻冒出"变化"。
// 信号采样历史（曲线数据）一并清掉，否则图表与时间线会对不上。
func apiClearChanges(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID *int `json:"modem_id"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		modemID := -1
		if req.ModemID != nil {
			modemID = *req.ModemID
		}
		removed := ChangeGlobal.Clear(modemID)
		samples := Global.Clear(modemID)
		// 清空要通知所有页面：否则别的标签页还挂着已经删掉的流水。
		// 不复用 "changes"（那是“新增条目数组”的语义），单独一个事件更清楚。
		broadcast("changes_clear", nil, map[string]any{"modem_id": modemID})
		httpx.WriteJSON(w, 0, map[string]any{
			"removed": removed, "samples": samples, "modem_id": modemID,
		}, "ok")
	}
}

// apiSignalHistory GET /api/signal/history?modem_id=0&minutes=30[&limit=2000]
//
// 信号采样序列（时间升序），供前端画曲线。空值字段缺省（nil）表示该次未上报，
// 前端据此断开曲线，而不是当成 0 画出一条假线。
// modem_id 省略/为负时返回全部模组（每条采样带 modem_id），
// minutes 默认 30，limit 默认 2000（只保留最新的若干条）。
func apiSignalHistory(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		modemID := -1
		if v := r.URL.Query().Get("modem_id"); v != "" {
			id, err := strconv.Atoi(v)
			if err != nil {
				httpx.WriteJSON(w, -1, nil, "modem_id must be an integer")
				return
			}
			modemID = id
		}
		minutes := 30
		if v := r.URL.Query().Get("minutes"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				httpx.WriteJSON(w, -1, nil, "minutes must be an integer")
				return
			}
			minutes = n
		}
		if minutes <= 0 {
			minutes = 30
		}
		if minutes > 24*60 {
			minutes = 24 * 60
		}
		limit := 2000
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				httpx.WriteJSON(w, -1, nil, "limit must be an integer")
				return
			}
			limit = n
		}
		if limit <= 0 {
			limit = 2000
		}
		if limit > 5000 {
			limit = 5000
		}
		since := time.Now().Add(-time.Duration(minutes) * time.Minute)
		samples := Global.Snapshot(modemID, since, limit)
		httpx.WriteJSON(w, 0, map[string]any{
			"modem_id": modemID,
			"minutes":  minutes,
			"count":    len(samples),
			"stored":   Global.CountFor(modemID),
			"max":      SampleMax,
			"samples":  samples,
		}, "ok")
	}
}

// ---- from: api_lock.go ----

// apiLockStatus GET /api/lock/status?modem_id=0
// 查询锁频段 / 锁基站 / 组网模式状态（只读，不影响驻网）
