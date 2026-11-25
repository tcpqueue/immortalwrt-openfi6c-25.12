package kernel

import (
	"fmt"
	"modemd/module/config"
	"modemd/module/httpx"
	"net/http"

	"go.uber.org/zap"
)

// ====================== 漫游（状态 + 「数据漫游」开关） API ======================
//
//	GET  /api/roam[?modem_id=0]   漫游状态 + 开关值 + 本机算不算数
//	POST /api/roam/set            {"on":true|false}   写回 [net] allow_roaming
//
// 语义与手机上的「数据漫游」一致：**关掉后国际漫游时不用数据**。
// ⭐ 只管国际漫游（卡归属 MCC ≠ 注册网 MCC）：**本地卡不受影响** —— 本地网、以及国内运营商之间的
// 同国异网漫游都照常上网（用户 2026-10-06 明确要求"漫游是为了国外卡支持，本地卡是不影响的"）。
// 判据只用 3GPP 注册状态 + 卡归属（IMSI 前 3 位），不看厂商私有指令 ⇒ 所有模组通用；
// 落地在 modemd 管得到的数据面（自动组网循环 / 主机侧拨号器），逻辑见 roaming.go。
//
// 为什么不做成"给模组发 AT 打开漫游开关"：AT 侧没有标准指令，各家私有写法
// （`AT^SYSCFGEX` 的漫游字段、`AT+QNWPREFCFG="roam_pref"`）取值语义互相矛盾/无法确认，
// 猜一个写下去可能把注册与服务域改坏 —— 详见 module/kernel/roaming.go 顶部。

// apiRoam GET /api/roam?modem_id=0
func apiRoam(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		out := RoamStatus(mod)
		out["modem_id"] = mod.ID
		httpx.WriteJSON(w, 0, out, "ok")
	}
}

// apiRoamSet POST /api/roam/set {"modem_id":0,"on":false}
//
// 只写配置 [net] allow_roaming（重启不丢）+ 同步内存，**不给模组发任何 AT**。
// 关掉时立刻把**正在漫游**那几台的主机侧拨号器收掉：组网循环最长 15 秒才轮一次，
// 而漫游流量按兆计费，等不起（本地注册的不动 —— 关的是"漫游时用数据"）。
func apiRoamSet(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			// On 用指针：必须显式给值，免得"body 里没写"被当成 false 把漫游关掉
			On      *bool `json:"on"`
			ModemID int   `json:"modem_id"`
		}
		if err := httpx.DecodeBody(r, &req); err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		if req.On == nil {
			httpx.WriteJSON(w, -1, nil, `缺少 on（要 true 或 false）`)
			return
		}
		if err := config.SetAllowRoaming(*req.On); err != nil {
			httpx.WriteJSON(w, -1, nil, "写回配置失败："+err.Error())
			return
		}
		logger.Info("roam: data roaming switch toggled", zap.Bool("on", *req.On))

		stopped := []int{}
		if !*req.On {
			stopped = stopHostDialIfRoaming(mgr, "漫游中且已关闭数据漫游（「高级配置 → 参数设置」）")
		}
		out := map[string]any{"allow_roaming": config.AllowRoaming(), "stopped_modems": stopped}
		msg := "已允许数据漫游（[net] allow_roaming 已写回配置；组网循环 ≤15 秒按新值走）"
		if !*req.On {
			msg = "已关闭数据漫游：**国际漫游**（卡归属国 ≠ 注册网所在国）时不再拨号 / 保持数据链路；" +
				"**本地卡不受影响** —— 本地网与同国异网漫游照常上网。" +
				"正在国际漫游的拨号器已立即收掉，要恢复打开这个开关即可"
			if len(stopped) > 0 {
				msg += fmt.Sprintf("（已停掉模组 %v 的主机侧拨号器）", stopped)
			}
		}
		if mod, err := modemFromQuery(mgr, r); err == nil && mod != nil {
			// 顺手把这台机型的"算不算数"一起带回去：厂商托管数据面的机器要能立刻看到原因
			if ok, why := RoamSupported(mod); !ok {
				out["effective"], out["reason"] = false, why
				msg += "；⚠️ " + why
			}
		}
		httpx.WriteJSON(w, 0, out, msg)
	}
}
