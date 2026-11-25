package kernel

import (
	"fmt"
	"modemd/module/hostnic"
	"modemd/module/httpx"
	"net/http"
	"strings"
)

// ====================== 主机网卡（FM350 特例） API ======================
//
//	GET  /api/host/nic[?modem_id=0]   当前网卡列表 + 已保存/建议的配置 + 三组命令
//	POST /api/host/nic/save           {"modem_id":0,"iface":"以太网 3","ip":"…","mask":"…","gateway":"…","dns":["…"]}
//	POST /api/host/nic/apply          {"modem_id":0,"mode":"renew|static|dhcp","execute":false,…同上…}
//
// 为什么单开这一块：绝大多数机型的主机侧地址由模组 DHCP 或主机拨号器给，
// **FM350-GL 不发 DHCP**，必须手工把网卡 IP 配成与模组对应才行（见 hostnic.go 的机型特例表）。
// 这里的命令既能显示出来让人复制（默认），也能由后端直接执行（execute=true）——
// 执行需要管理员 / root，没权限就把原始错误原样返回，页面会提示改用手工执行。

// apiHostNIC GET /api/host/nic?modem_id=0
func apiHostNIC(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, mod.HostNICView(), "ok")
	}
}

// apiHostNICSave POST /api/host/nic/save —— 保存这份配置（写 hostnic.json，重启不丢）
func apiHostNICSave(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID int `json:"modem_id"`
			hostnic.HostNICPlan
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
		p, err := hostnic.SaveHostNICPlan(req.HostNICPlan)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, mod.HostNICView(), fmt.Sprintf("ok, 已保存 %s 的配置", p.Interface))
	}
}

// apiHostNICApply POST /api/host/nic/apply —— 生成命令；execute=true 时直接执行
func apiHostNICApply(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID int    `json:"modem_id"`
			Mode    string `json:"mode"`
			Execute bool   `json:"execute"`
			hostnic.HostNICPlan
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
		plan := req.HostNICPlan
		// 配静态 / 恢复 DHCP 都先把参数存下来：否则下次进来又变回建议值，等于没配。
		// 「先试 DHCP」只是一次性动作，不改配置。
		if strings.ToLower(strings.TrimSpace(req.Mode)) != hostnic.HostNICRenew {
			if p, err := hostnic.SaveHostNICPlan(plan); err == nil {
				plan = p
			}
		}
		res, err := HostNICApply(req.Mode, plan, req.Execute)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		msg := "命令已生成，复制出去执行即可"
		if res.Executed {
			msg = "已执行（没权限的命令会在结果里报错）"
		}
		httpx.WriteJSON(w, 0, map[string]any{
			"apply": res,
			"view":  mod.HostNICView(),
		}, msg)
	}
}
