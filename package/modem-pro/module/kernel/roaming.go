package kernel

import (
	"fmt"
	"strings"

	"modemd/module/atparse"
	"modemd/module/config"
	"modemd/module/netdev"
)

// roaming.go —— 漫游：状态判据（所有模组通用）+ 「数据漫游」开关（[net] allow_roaming）。
//
// ⚠️ 这个文件里**一条厂商 AT 指令都没有**，这是刻意的：
//
//   - 3GPP 里"漫游"只是注册状态（`<stat>`：1=本地、5=漫游），**没有**"允不允许漫游"的标准指令；
//   - `AT+QNWPREFCFG="roam_pref"` 在公开项目里**零命中**（实测只回一个 255，与 Qualcomm
//     QCDM 的 ROAM_PREF(AUTO=0xFF) 对得上，但没有任何可验证的读写写法）；
//   - 华为/鼎桥/展锐那族的 `AT^SYSCFGEX` 第 3 个字段是"漫游"，可公开项目里 0 / 1 / 2
//     都有人写（QModem 的 huawei.sh 写 1、meig.sh 写 0、另一处 C 代码写 2），语义互相矛盾
//     —— 猜一个写下去可能把模组的注册/服务域改坏，得不偿失（项目规矩：未取证不发指令）。
//
// 所以「数据漫游」做成**策略层**的开关（一条厂商私有指令都不发）：
//
//	判据：注册状态里那一位（`<stat> == 5`）—— AT 轮询与 URC 跟随两条路都已解析，所有模组通用；
//	范围：再用「卡归属 MCC ≠ 注册网 MCC」分出**国际漫游**与**同国异网漫游**（见 RoamScope）；
//	落地：modemd 管得到的数据面（自动组网循环 / 主机侧拨号器）不再拨号、并把在跑的拨号器收掉；
//	例外：厂商托管数据面（managedDataPlane，展锐 SIPC / 中兴 F50）如实报"开关不生效 + 原因"，
//	      因为那里的拨号与断开都不归 modemd（见 RoamSupported）。
//
// ⭐ **开关只管国际漫游，本地卡一律不受影响**（用户 2026-10-06 明确要求："漫游是为了国外卡支持，
// 本地卡是不影响的"）。`<stat> == 5` 只说明"注册在非归属网"，它分不出这两种情况：国内运营商之间
// 的异网漫游（本地卡也会这样）虽然也报 5，但流量与本地资费一样 —— 拦了就是把本地卡的数据掐了。
// 判据详情与决策表见 RoamScope / RoamState。
//
// 页面两处入口：概览「信号」卡片显示漫游状态（渲染在 web/index.html 的 renderSignal），
// 开关在「高级配置 → 参数设置」。

// RoamScope 漫游的"范围" —— 「数据漫游」开关**只管国际漫游**，这一层就是它的判据。
//
// 为什么必须分：3GPP 的 `<stat> == 5` 只说明"注册在非归属网"，它**分不出**这两种完全不同的情况：
//
//	国际漫游：卡归属国 ≠ 注册网所在国（MCC 不同）—— 例如国外卡漫游到境内、或境内卡出国。
//	          ⭐ 这才是「数据漫游」开关要管的事（不关就是漫游流量/高昂资费）。
//	同国异网漫游：MCC 相同、MNC 不同（国内运营商之间共享 / 异网漫游）。**本地卡也会这样**，
//	          流量与本地资费一样 ⇒ 绝不能拦（拦了就是把本地卡的数据掐了）。
//
// 判据用的都是**已经缓存的**信息（不发 AT）：卡归属 MCC 取 IMSI 前 3 位
// （sim.go 的 IMSIPLMN；IMSI 由 SIM 热插拔巡检自动读，`[sim] hotswap_interval` 默认 20 秒），
// 注册网 MCC 取网络状态 / URC 里的 PLMN（+QNWINFO / +SPNWNAME）。
type RoamScope string

const (
	// RoamScopeNone 本地网注册（`<stat> == 1`）：开关完全不介入。
	RoamScopeNone RoamScope = "none"
	// RoamScopeDomestic 同国异网漫游（MCC 相同）：**本地卡也会这样**，开关不介入。
	RoamScopeDomestic RoamScope = "domestic"
	// RoamScopeGlobal 国际漫游（卡归属国 ≠ 注册网所在国）：开关只管这一种。
	RoamScopeGlobal RoamScope = "global"
	// RoamScopeUnknown 在漫游，但读不到卡归属 / 注册网 PLMN，比不出国别 ⇒ **不拦**：
	// 宁可"该拦没拦"，也不要把本地卡的数据掐了（页面把原因写出来，见 Note）。
	RoamScopeUnknown RoamScope = "unknown"
)

// RoamState 漫游的完整判据结果（闸门、/api/roam、页面都用这一份，口径只有一处）。
type RoamState struct {
	Roaming  bool      `json:"roaming"`             // 数据域是否漫游（3GPP `<stat> == 5`）
	Scope    RoamScope `json:"roam_scope"`          // 漫游范围（决定开关管不管）
	HomeMCC  string    `json:"home_mcc,omitempty"`  // 卡归属 MCC（IMSI 前 3 位）
	VisitMCC string    `json:"visit_mcc,omitempty"` // 当前注册网 MCC
	// Note 判不出国别（Scope == RoamScopeUnknown）时的原因，页面直接显示。
	Note string `json:"roam_note,omitempty"`
}

// RoamState 这台模组此刻的漫游状态（**只读缓存，不发 AT** —— 自动组网循环每轮都会调它）。
func (mod *Modem) RoamState() RoamState {
	st := RoamState{Roaming: mod.DataRoaming()}
	if !st.Roaming {
		st.Scope = RoamScopeNone
		return st
	}
	home, _ := atparse.IMSIPLMN(mod.homeIMSI())
	st.HomeMCC, st.VisitMCC = home, mod.visitedMCC()
	switch {
	case home == "":
		st.Scope = RoamScopeUnknown
		st.Note = "读不到卡的归属（IMSI），判不出是国际漫游还是同国异网漫游 —— 所以不拦；" +
			"到「SIM 卡」页读一次卡（或等 SIM 巡检，默认 20 秒一轮）后再看"
	case st.VisitMCC == "":
		st.Scope = RoamScopeUnknown
		st.Note = "读不到当前注册网的 PLMN（MCC），判不出国别 —— 所以不拦"
	case home == st.VisitMCC:
		// 同国异网漫游：本地卡的正常状态（国内运营商之间共享/异网漫游）
		st.Scope = RoamScopeDomestic
	default:
		st.Scope = RoamScopeGlobal
	}
	return st
}

// homeIMSI 卡的 IMSI（**只读缓存**）：优先 SIM 热插拔巡检自动读的那份（默认 20 秒一轮），
// 其次「SIM 卡」页缓存下来的 ModemStatus。两处都没有就返回空串（按"归属未知"处理）。
func (mod *Modem) homeIMSI() string {
	if mod == nil {
		return ""
	}
	mod.mu.Lock()
	defer mod.mu.Unlock()
	if i := strings.TrimSpace(mod.simIdent.IMSI); i != "" {
		return i
	}
	if mod.ModemStatus != nil {
		return strings.TrimSpace(mod.ModemStatus.IMSI)
	}
	return ""
}

// visitedMCC 当前**注册网**的 MCC（只读缓存）：先看网络状态（+QNWINFO / +SPNWNAME 都写进它），
// 拿不到再退回 URC 跟随器里的运营商快照（展锐 SIPC / F50 只有前者那一份 URC 来源）。
func (mod *Modem) visitedMCC() string {
	if mod == nil {
		return ""
	}
	mod.mu.Lock()
	defer mod.mu.Unlock()
	if mod.networkStatus != nil && strings.TrimSpace(mod.networkStatus.MCC) != "" {
		return strings.TrimSpace(mod.networkStatus.MCC)
	}
	if mod.urcOp.ok() {
		return strings.TrimSpace(mod.urcOp.mcc)
	}
	return ""
}

// DataRoaming 模组**数据域**当前是不是漫游（3GPP `<stat> == 5`）。
//
// 只看数据域：禁止"数据漫游"关心的是数据业务注册在谁那儿，CS（语音）域漫游与流量无关。
// 取用顺序 EPS(CEREG) → 5GS(C5GREG) → PS(CGREG)：模组报哪条用哪条（与 net.go 里
// 挑制式、取信号的口径一致）。
//
// 两个来源都看，覆盖两条完全不同的路：
//
//   - URC 跟随器（urcNet）—— 厂商托管数据面的机器（展锐 SIPC / 中兴 F50）：注册状态是
//     厂商守护打到 AT 口上的 URC，modemd 不发指令也能实时跟随；
//   - 上一次完整查询的缓存（networkStatus）—— 普通 USB 模组的 AT 轮询路径。
//
// ⚠️ 一个注册状态都读不到时返回 false（当"本地"看）：宁可不禁，
// 也不要因为"读不到"就把用户的数据掐了。
func (mod *Modem) DataRoaming() bool {
	if mod == nil {
		return false
	}
	mod.mu.Lock()
	defer mod.mu.Unlock()

	// ① URC 跟随器（优先级同 AT 路径：数据域在前）
	for _, rs := range []*RegState{mod.urcNet.eps, mod.urcNet.fiveG, mod.urcNet.ps, mod.urcNet.cs} {
		if rs != nil {
			return rs.Roaming
		}
	}
	// ② 上一次完整查询的缓存
	if r, ok := roamOfStatus(mod.networkStatus); ok {
		return r
	}
	return false
}

// roamOfStatus 从一份网络状态里取"数据域是不是漫游"；第二个返回值表示"这份状态里有没有注册信息"。
//
// 优先级 EPS(CEREG) → 5GS(C5GREG) → PS(CGREG) → CS(CREG)：数据域优先。
// ⚠️ PS 的 0（未注册未搜索）/ 4（未知）**不是结论**，这时退回 CS 看 ——
// 5G SA 下 CGREG 长期停在 0,0、真正状态在 C5GREG 里（见 net.go 的 Reg5G 注释），
// 把"0"当成"本地"会把漫游判错。
func roamOfStatus(st *NetworkStatus) (bool, bool) {
	if st == nil {
		return false, false
	}
	switch {
	case st.RegEPS != nil:
		return st.RegEPS.Roaming, true
	case st.Reg5G != nil:
		return st.Reg5G.Roaming, true
	case st.PSReg.Stat != 0 || st.CSReg.Stat != 0:
		if st.PSReg.Stat != 0 && st.PSReg.Stat != 4 {
			return st.PSReg.Roaming, true
		}
		return st.CSReg.Roaming, true
	}
	return false, false
}

// fillRoam 把漫游状态与开关值补进一份网络状态（AT 路径与 URC 路径都要调）。
//
// 为什么放进 NetworkStatus 而不是让页面自己算：页面手上只有 cs_reg / ps_reg / reg_5g /
// reg_eps 四份注册状态，"哪一份算数据域、谁是权威"这套口径只该在 Go 这边有一份
// （与 DataRoaming 同一个函数）—— 前端各算一遍迟早对不上。
//
// ⚠️ 与 AMBR / Traffic 一样**每轮都覆盖**，不能写成"只在空时填"（缓存冻结坑：
// 一次填完就永远不动了）。读不到注册信息时按"本地"（false）写：宁可不禁，也不误断。
func (mod *Modem) fillRoam(st *NetworkStatus) {
	if st == nil {
		return
	}
	if r, ok := roamOfStatus(st); ok {
		st.Roaming = r
	} else {
		st.Roaming = mod.DataRoaming() // 这份状态里没有注册字段：退回全局判据（URC 跟随器）
	}
	st.RoamAllowed = config.AllowRoaming()
	// 漫游范围（国际 / 同国异网）也一起给页面：只写"漫游中"会让本地卡用户困惑
	//（他的卡在国内异网漫游，开关关着也不该有影响 —— 页面要能把这件事说清楚）。
	st.RoamScope = string(mod.RoamState().Scope)
}

// RoamBlockReason 因为「关掉了数据漫游 + **正处在国际漫游**」而**不该用数据**的原因；空串 = 该照常。
//
// ⚠️ 只有**国际漫游**（卡归属国 ≠ 注册网所在国）才拦 —— 用户 2026-10-06 明确要求：
// 「漫游是为了国外卡支持，本地卡是不影响的」。同国异网漫游（国内运营商之间）虽然模组也报
// `<stat> == 5`，但流量与本地资费一样、属于**本地卡**的正常状态 ⇒ 一律放行；
// 判不出国别时（RoamScopeUnknown）也放行 —— 宁可该拦没拦，也不要把本地卡的数据掐了。
//
// 两个调用点（都是幂等的"停着"）：
//
//	autoWANForModem（自动组网循环）  → 这一轮整台模组不拨号，并把在跑的拨号器收掉
//	hostDialStopReason（拨号器看护） → 把在跑的主机侧拨号器收掉并报这句话
func RoamBlockReason(mod *Modem) string {
	if mod == nil || config.AllowRoaming() {
		return ""
	}
	st := mod.RoamState()
	if st.Scope != RoamScopeGlobal {
		return ""
	}
	return fmt.Sprintf("国际漫游中（卡归属 MCC %s ≠ 注册网 MCC %s）且已关闭数据漫游 ——"+
		"「高级配置 → 参数设置」里打开「数据漫游」即恢复", st.HomeMCC, st.VisitMCC)
}

// RoamSupported 这台机型的「数据漫游」开关**算不算数**；不算数时给一句原因。
//
// 不算数的只有厂商托管数据面那一族（managedDataPlane：展锐 SIPC / 中兴 F50）：
// 它们的数据面是设备厂商的守护在拨（F50 = netifd 的 proto=mu300cell + mobile-data watch），
// modemd 既插不进去、也不能去断它 —— 那台设备的厂商守护会把状态改回去，实测整机失联过一次
// （见 net.go 的 AirplaneBlockedReason 与 atdev_mu300_policy.go）。
// 所以：开关能存进配置、页面也能显示，但如实标"本机不生效 + 原因"，不装样子。
//
// 「漫游状态」本身与机型无关，照常显示（DataRoaming 读 URC/缓存，不发指令）。
func RoamSupported(mod *Modem) (bool, string) {
	if mod == nil {
		return false, "没有模组"
	}
	if netdev.ManagedDataPlane(mod.ATPort) {
		return false, "本机型的数据面由设备厂商托管（拨号与断开都不归 modemd），" +
			"「数据漫游」开关不生效；漫游状态照常显示，要在漫游时禁止上网请用设备厂商的界面" +
			"（或让运营商侧关掉漫游）"
	}
	return true, ""
}

// RoamStatus 「数据漫游」开关与漫游状态的快照（/api/roam 用它，页面照它画）。
//
// 字段含义（都写清楚，免得前端各猜一套）：
//
//	allow_roaming  开关本身的值（配置 [net] allow_roaming，默认 true = 允许）
//	roaming        数据域**此刻**是不是漫游（所有模组通用，只看 `<stat> == 5`）
//	roam_scope     漫游范围：none 本地 / domestic 同国异网 / global 国际 / unknown 判不出
//	home_mcc       卡归属 MCC（IMSI 前 3 位）；visit_mcc 当前注册网 MCC
//	roam_note      scope == unknown 时的原因（页面直接显示）
//	blocked        "该断网"的状态（开关关闭 + **国际漫游**）—— 页面该变红提示
//	effective      这台机型上开关算不算数（false 见 reason）
//	reason         上面几项为什么不生效 / 为什么不拦
func RoamStatus(mod *Modem) map[string]any {
	allowed := config.AllowRoaming()
	rs := mod.RoamState()
	effective, why := RoamSupported(mod)
	blocked := !allowed && rs.Scope == RoamScopeGlobal
	if !effective && why != "" {
		if rs.Roaming {
			// 开关关着但本机不生效：说明清楚"为什么没断"
			why = "目前处于漫游，但本机不生效：" + why
		}
	} else if rs.Scope == RoamScopeDomestic && why == "" {
		// 本地卡的同国异网漫游：把"为什么不拦"说清楚（用户会看到漫游标识却没断网）
		why = fmt.Sprintf("当前是同国异网漫游（MCC 同为 %s）：属于本地卡的正常状态，"+
			"「数据漫游」开关不介入，数据照常使用", rs.HomeMCC)
	}
	return map[string]any{
		"allow_roaming": allowed,
		"roaming":       rs.Roaming,
		"roam_scope":    string(rs.Scope),
		"home_mcc":      rs.HomeMCC,
		"visit_mcc":     rs.VisitMCC,
		"roam_note":     rs.Note,
		"blocked":       blocked && effective,
		"effective":     effective,
		"reason":        why,
	}
}

// stopHostDialIfRoaming 切到「禁止数据漫游」时**立刻**把**正在国际漫游**那几台的主机侧拨号器收掉。
//
// 只看该拦的那几台（RoamBlockReason 非空）—— 也就是**国际漫游**：本地网、同国异网漫游都不碰
// （见 RoamBlockReason 的说明）。没在拦的不用管 —— 下一轮组网循环会照常把它们拉起来。
//
// 返回被停掉的模组 ID（调用方拿去写页面提示）。为什么需要它：SetAllowRoaming 只写配置，
// 而组网循环最长 15 秒才轮一次 —— 国际漫游流量是**按兆计费**的，不能等。
func stopHostDialIfRoaming(mgr *ModemManager, why string) []int {
	if mgr == nil {
		return nil
	}
	hostDials.mu.Lock()
	ids := make([]int, 0, len(hostDials.runs))
	for id, run := range hostDials.runs {
		if run != nil && run.running {
			ids = append(ids, id)
		}
	}
	hostDials.mu.Unlock()

	var stopped []int
	for _, id := range ids {
		mod, err := modemByID(mgr, id)
		if err != nil || mod == nil {
			continue
		}
		// 只有"国际漫游 + 开关关着"才收（本地网 / 同国异网漫游一律不碰）
		if RoamBlockReason(mod) == "" {
			continue
		}
		if supported, _ := RoamSupported(mod); !supported {
			continue // 厂商托管数据面：拨号器本来就不归我们（正常也不会在跑）
		}
		if note := stopHostDial(id, why); note != "" {
			stopped = append(stopped, id)
		}
	}
	return stopped
}
