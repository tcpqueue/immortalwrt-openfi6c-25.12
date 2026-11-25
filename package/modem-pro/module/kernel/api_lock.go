package kernel

import (
	"modemd/module/httpx"
	"net/http"

	"go.uber.org/zap"
)

func apiLockStatus(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		st, err := mod.lockStatusOrQuery(r.URL.Query().Get("refresh") == "1")
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, st, "ok")
	}
}

// apiLockCaps GET /api/lock/caps?modem_id=0
// 探测模组实际支持哪些锁网指令（全部只读），用于确认不同固件的指令差异
func apiLockCaps(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		caps, err := mod.ProbeLockCaps()
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		msg := "ok"
		if !caps.Supported {
			msg = "该模组固件不支持锁网（锁频段 / 锁基站）"
		}
		httpx.WriteJSON(w, 0, caps, msg)
	}
}

// apiLockBand POST /api/lock/band
//
//	{"modem_id":0,"tech":"NR","bands":[78]}      锁定 NR n78
//	{"modem_id":0,"tech":"LTE","bands":[1,3,5]}  锁定 LTE B1/B3/B5
//	{"modem_id":0,"tech":"NR","unlock":true}     解除 NR 频段锁定
func apiLockBand(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID int    `json:"modem_id"`
			Tech    string `json:"tech"`
			Bands   []int  `json:"bands"`
			Unlock  bool   `json:"unlock"`
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
		tech, err := ParseLockTech(req.Tech)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		bands := req.Bands
		if req.Unlock {
			bands = nil
		}
		st, err := mod.SetBandLock(tech, bands)
		if err != nil {
			logger.Warn("set band lock failed", zap.Int("modem", req.ModemID), zap.Error(err))
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		pushLockState(mod, st)
		httpx.WriteJSON(w, 0, st, "ok")
	}
}

// apiLockCell POST /api/lock/cell
//
//	{"modem_id":0,"tech":"NR","earfcn":627264,"pcid":521}        钉死 n78 小区（需固件支持）
//	{"modem_id":0,"tech":"LTE","earfcn":2452,"pcid":155}         钉死 LTE 小区
//	{"modem_id":0,"tech":"LTE","earfcn":2452,"lock_type":2}      仅锁 LTE 频点
//	{"modem_id":0,"tech":"NR","lock_type":0}                     解除 NR 小区锁定
//
// lock_type: 0=解锁 1=锁频点+PCI 2=仅锁频点（不传时按 earfcn/pcid 自动推断）；
// scs: 仅 NR 使用，子载波间隔，可填 kHz(15/30/60) 或索引(0/1/2)，缺省 30kHz；
// persist=true 时额外写入 NV，使锁定在模组重启后仍生效。
func apiLockCell(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID  int    `json:"modem_id"`
			Tech     string `json:"tech"`
			EARFCN   int    `json:"earfcn"`
			PCID     *int   `json:"pcid"`
			LockType *int   `json:"lock_type"`
			SCS      int    `json:"scs"`
			Persist  bool   `json:"persist"`
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
		tech, err := ParseLockTech(req.Tech)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		// PCI 用指针区分"未提供"与"PCI=0"（0 是合法 PCI）
		pcid := 0
		if req.PCID != nil {
			pcid = *req.PCID
		}
		lockType := CellLockNone
		switch {
		case req.LockType != nil:
			lockType = *req.LockType
		case req.EARFCN > 0 && req.PCID != nil:
			lockType = CellLockARFCNPCI
		case req.EARFCN > 0:
			lockType = CellLockARFCN
		}
		lockReq := CellLockRequest{
			Tech:     tech,
			ARFCN:    req.EARFCN,
			PCID:     pcid,
			LockType: lockType,
			SCS:      req.SCS,
		}
		st, err := mod.LockCell(lockReq, req.Persist)
		if err != nil {
			logger.Warn("cell lock failed", zap.Int("modem", req.ModemID), zap.Error(err))
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		pushLockState(mod, st)
		httpx.WriteJSON(w, 0, st, "ok")
	}
}

// apiLockMode POST /api/lock/mode  {"modem_id":0,"mode":"LTE"}
// 组网模式偏好：AUTO / LTE / NR5G / LTE:NR5G
func apiLockMode(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID int    `json:"modem_id"`
			Mode    string `json:"mode"`
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
		st, err := mod.SetModePref(req.Mode)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		pushLockState(mod, st)
		httpx.WriteJSON(w, 0, st, "ok")
	}
}

// apiLockUnlock POST /api/lock/unlock  {"modem_id":0}
// 一键解除全部锁定：LTE/NR 频段 + LTE/NR 小区
func apiLockUnlock(mgr *ModemManager) http.HandlerFunc {
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
		st, err := mod.UnlockAll()
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		pushLockState(mod, st)
		httpx.WriteJSON(w, 0, st, "ok")
	}
}

// ---- from: api_data.go ----

// ---- from: api_quota.go ----
// apiQuota GET /api/quota?modem_id=0
// apiQuota GET /api/quota?modem_id=0
