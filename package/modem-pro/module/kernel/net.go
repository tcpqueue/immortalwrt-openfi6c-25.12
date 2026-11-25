package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"errors"
	"fmt"
	"modemd/module/atparse"
	"modemd/module/cell"
	"modemd/module/common"
	"modemd/module/dial"
	"modemd/module/fibocom"
	"modemd/module/netdev"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ---- from: network.go ----

//
// 裸串 → 结构体/中文的解析在 net.go（纯逻辑，含各条指令的实测坑）；
// 这里只管：发哪条指令、什么时候补查、以及与扫描缓存/流量/AMBR 等模块的汇总。
//
// 几条指令的分工：
//
//	AT+QNWINFO  → +QNWINFO: "TDD NR5G","46011","NR5G BAND 78",627264
//	AT+CREG?    → +CREG: 0,0                      电路域（CS）注册
//	AT+CGREG?   → +CGREG: 0,1                     分组域（PS）注册，数据业务看这个
//	AT+C5GREG?  → +C5GREG: 0,1                    5GS 注册（5G SA 下真正有用的那条）
// NetworkStatus 网络类型 / 接入 / 注册状态汇总

type NetworkStatus struct {
	ModemID   int       `json:"modem_id"`
	QueriedAt time.Time `json:"queried_at"`

	// —— AT+QNWINFO ——
	NetTech   string   `json:"net_tech,omitempty"`   // 模组原始制式，如 "TDD NR5G"
	TechLabel string   `json:"tech_label,omitempty"` // 友好名，如 "5G NR (TDD)"
	Tech      CellTech `json:"tech,omitempty"`       // 归一化接入技术
	Duplex    string   `json:"duplex,omitempty"`     // FDD / TDD
	PLMN      string   `json:"plmn,omitempty"`       // 46011
	MCC       string   `json:"mcc,omitempty"`
	MNC       string   `json:"mnc,omitempty"`
	BandText  string   `json:"band_text,omitempty"` // "NR5G BAND 78"
	Band      int      `json:"band,omitempty"`
	BandLabel string   `json:"band_label,omitempty"` // n78 / B5
	EARFCN    int      `json:"earfcn,omitempty"`

	// —— 附加网络信息（可选能力，目前只有芯讯通 SIM8200 的 AT+CNWINFO、鼎桥 MT5700 的 AT^HFREQINFO）——
	Extra *Extra `json:"extra_net,omitempty"`

	// —— AT+CREG? / AT+CGREG? ——
	CSReg RegState `json:"cs_reg"`
	PSReg RegState `json:"ps_reg"`
	// 5GS / EPS 域注册状态：5G SA 下 CREG/CGREG 会长期停在 0,0（看着像"未注册"），
	// 真正的注册状态在 5GS 里（实测 RM502Q-GL：+C5GREG: 0,1 已注册）。
	// **只在 CS/PS 都报未注册时才补查**，平时正常的模组不会多发指令。
	Reg5G  *RegState `json:"reg_5g,omitempty"`
	RegEPS *RegState `json:"reg_eps,omitempty"`

	// —— 服务小区（取自扫描缓存；无缓存时为空）——
	Operator     string         `json:"operator,omitempty"`
	Serving      *cell.CellInfo `json:"serving,omitempty"`
	ServingCount int            `json:"serving_count"`
	NeighbourCnt int            `json:"neighbour_count"`
	ScannedAt    time.Time      `json:"scanned_at,omitempty"` // 上面这些缓存数据的扫描时间

	// —— 载波聚合（与 QNWINFO 一样是"此刻"的状态，实时查询）——
	CA       []CCInfo `json:"ca,omitempty"` // 分量载波：PCC + SSC
	CAActive bool     `json:"ca_active"`    // 是否真的聚合了（存在 SSC）

	// —— 当前网络：APN / 本机地址 / 模式 ——
	APN      string            `json:"apn,omitempty"`       // 当前 APN（激活的 PDP 上下文）
	IPv4     string            `json:"ipv4,omitempty"`      // PDP 分配的 IPv4 地址
	IPv6     string            `json:"ipv6,omitempty"`      // PDP 分配的 IPv6 地址（双栈时才有）
	ModePref string            `json:"mode_pref,omitempty"` // 模组当前组网模式偏好（可修改）
	Traffic  *Rate             `json:"traffic,omitempty"`   // 上行/下行速度（流量计数器差分）
	AMBR     *atparse.AMBRRate `json:"ambr,omitempty"`      // 签约速率（运营商下发的 AMBR，见 ambr.go）

	// —— 漫游（roaming.go）——
	// Roaming 数据域此刻是不是漫游（3GPP `<stat> == 5`）。**所有模组通用**：
	// 判据只看注册状态（AT 轮询与 URC 跟随两条路都有），与厂商指令无关。
	Roaming bool `json:"roaming"`
	// RoamAllowed 「数据漫游」开关的值（[net] allow_roaming，默认 true = 允许）。
	// 与 Roaming 一起给页面：开关关着 + **国际漫游**（见 RoamScope）= 数据链路该停（页面变红提示）。
	RoamAllowed bool `json:"roam_allowed"`
	// RoamScope 漫游范围（roaming.go 的 RoamScope）：none 本地 / domestic 同国异网 /
	// global 国际 / unknown 判不出。⭐ 开关**只管 global**：同国异网漫游属于本地卡的正常状态。
	RoamScope string `json:"roam_scope,omitempty"`

	// —— 飞行模式（AT+CFUN）——
	CFUN     int  `json:"cfun,omitempty"` // 模组功能模式：1 = 全功能，4 = 飞行模式（见 airplane.go）
	Airplane bool `json:"airplane"`       // CFUN == 4

	RawNetInfo string `json:"raw_net_info,omitempty"`
}

// needRegFallback 是否需要补查 5GS / EPS 注册状态。
//
// 只在"CS 与 PS 都看着没注册"时才为 true —— 这是"只对看起来异常的模组兜底"的关键条件：
// 平时正常的模组（如 RM500U 报 +CGREG: 0,1）根本不会多这两条指令。
func needRegFallback(st *NetworkStatus) bool {
	return !st.CSReg.Registered && !st.PSReg.Registered
}

// queryReg 查一条注册状态指令；失败 / 不支持 / 解析不出来都返回 ok=false。
func (mod *Modem) queryReg(cmd string) (*RegState, bool) {
	resp, err := mod.SendAT(cmd)
	if err != nil || common.RespHasError(resp) {
		return nil, false
	}
	rs, ok := ParseReg(resp)
	if !ok {
		return nil, false
	}
	return &rs, true
}

// BandLabel / BandNumFromText 已搬到 cell.go（BandLabel / BandNumFromText），
// 它们只跟“制式 + 频段号”有关，与模组无关；kernel 里保留了老名字的别名。

// applyServingNetInfo 用服务小区应答补网络类型信息。
//
// 用于 AT+QNWINFO 不被支持的模组（未验证型号/MT5700 这类）：
// 从 +QENG/+FENG 或 +CPSI 的 servingcell 行里取出制式/频段/PLMN，
// 让「网络类型」面板至少不空白。指令同样是运行期探测出来的那条。
func applyServingNetInfo(st *NetworkStatus, mod *Modem) {
	cmd := mod.resolveVendorCmd(KindServing)
	if cmd == "" {
		return
	}
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return
	}
	ci, ok := FirstServingCell(resp)
	if !ok {
		return
	}
	fillNetFromServingCell(st, ci)
	// ⚠️ 带上实际用的指令：界面上这一行是按「AT+QNWINFO → 原始应答」渲染的，
	// 而这里其实是用服务小区指令兜的底（QNWINFO 回了 ERROR），
	// 不写清楚就会出现"AT+QNWINFO → +GTCCINFO:…"这种张冠李戴的显示。
	st.RawNetInfo = cmd + "|" + strings.Join(resp, "|")
}

// ensureServingCell 在“还没扫过基站”时补一次服务小区查询。
//
// 目的：让「网络」卡片立刻能看到 Cell ID / PCI / TAC / 服务小区数，
// 不必先去「基站扫描」页手动扫一次（以前只有 QNWINFO 不被支持的机型才有这个待遇）。
//
// ⚠️ 只补**小区详情**，不覆盖 QNWINFO 已经给出的制式/频段/双工：
// QNWINFO 的 net_tech 带 TDD/FDD（"TDD NR5G"），比服务小区行更完整。
func (mod *Modem) ensureServingCell(st *NetworkStatus) {
	if st.Serving != nil {
		return
	}
	cmd := mod.resolveVendorCmd(KindServing)
	if cmd == "" {
		return
	}
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return
	}
	if ci, ok := FirstServingCell(resp); ok {
		st.Serving = &ci
		st.ServingCount = 1
	}
}

// fillNetFromServingCell 用一条服务小区解析结果补全网络状态。
// 用于 AT+QNWINFO 不被支持的模组：制式 / 频段 / PLMN / Cell ID / PCI / TAC 全从小区行取。
func fillNetFromServingCell(st *NetworkStatus, ci cell.CellInfo) {
	st.NetTech = string(ci.Tech)
	st.Tech = ci.Tech
	st.TechLabel = TechLabelFromCellTech(ci.Tech)
	st.EARFCN = ci.EARFCN
	st.Band = ci.Band
	st.BandLabel = common.BandLabel(ci.Tech, ci.Band)
	// 界面「频段」一栏用的是 band_text（QNWINFO 那一路才会填），不补这一下
	// 就算频段号拿到了，页面上还是显示 `-`
	st.BandText = BandTextFor(ci.Tech, ci.Band)
	// 顺手把服务小区详情也拉上（Cell ID / PCI / TAC）：
	// 没扫过基站时应先也能在「网络」卡片里看到，而不是一片 `-`
	st.Serving = &ci
	if ci.MCC != "" && ci.MNC != "" {
		st.MCC, st.MNC = ci.MCC, ci.MNC
		st.PLMN = ci.MCC + ci.MNC
	}
}

const networkStatusCacheTTL = 30 * time.Second

func (mod *Modem) cacheNetworkStatus(st *NetworkStatus) {
	if st == nil {
		return
	}
	copy := cloneNetworkStatus(st)
	mod.mu.Lock()
	mod.networkStatus = copy
	mod.mu.Unlock()
}

func (mod *Modem) networkStatusSnapshot() *NetworkStatus {
	mod.mu.Lock()
	st := cloneNetworkStatus(mod.networkStatus)
	mod.mu.Unlock()
	if st == nil || st.QueriedAt.IsZero() || time.Since(st.QueriedAt) > networkStatusCacheTTL {
		return nil
	}
	return st
}

func cloneNetworkStatus(st *NetworkStatus) *NetworkStatus {
	if st == nil {
		return nil
	}
	copy := *st
	copy.CA = append([]CCInfo(nil), st.CA...)
	if st.Serving != nil {
		serving := *st.Serving
		copy.Serving = &serving
	}
	if st.Reg5G != nil {
		reg := *st.Reg5G
		copy.Reg5G = &reg
	}
	if st.RegEPS != nil {
		reg := *st.RegEPS
		copy.RegEPS = &reg
	}
	if st.Extra != nil {
		extra := *st.Extra
		copy.Extra = &extra
	}
	if st.Traffic != nil {
		traffic := *st.Traffic
		copy.Traffic = &traffic
	}
	if st.AMBR != nil {
		ambr := *st.AMBR
		copy.AMBR = &ambr
	}
	return &copy
}

// networkQueryPlan 这次网络状态该怎么取。
//
// 抽成纯函数是为了让"接口路径到底会不会发 AT"这件事**可以被单测钉住** ——
// 以前只靠 url 参数和缓存 TTL，很容易被后续改动无意中改回 AT 轮询。
type networkQueryPlan int

const (
	// planFullAT 跑一遍完整 AT 查询（其它模组的原有行为，一条不改）
	planFullAT networkQueryPlan = iota
	// planURC 只用 URC 日志 + 内核 + 缓存（展锐 SIPC / F50：接口路径零 AT）
	planURC
)

// networkQueryPlan 选本次网络状态的来源。
//
// ⚠️ 展锐 SIPC（F50）进 planURC 后，**任何** HTTP 请求都不会等 AT：
// 完整查询在这条总线上要 8~10 条秒级指令，放请求路径上就是"接口卡 60 秒"。
func (mod *Modem) networkQueryPlan() networkQueryPlan {
	if mu300FileBackedState(mod) {
		return planURC
	}
	return planFullAT
}

// shouldFullQueryNetwork 后台现在该不该真去总线把网络状态**完整**查一遍。
//
// ⚠️ 展锐 SIPC（F50）**永远返回 false** —— 用户明确要求"接口只读文件、不发 AT"，
// 所以连后台的一次性补查也不再做（以前是启动后补 3 次）。
// 代价很明确：URC 里没有的字段（制式/频段/APN/模式偏好/CFUN/AMBR）会一直空着，
// 要靠用户主动的"动作"（写组网模式 / 切飞行模式）才有值。
func (mod *Modem) shouldFullQueryNetwork(now time.Time) bool {
	if mu300FileBackedState(mod) {
		return false
	}
	return true
}

// ratFromACT 把 `<AcT>` 换成制式（网络类型）。
//
// ⚠️⚠️ 码表**照抄设备厂商自己的脚本**（`/opt/mu300/bin/mobile-data` 的 `signal()`），
// 不是我们猜的：
//
//  7. rat="LTE"
//  13. rat="5G NSA (LTE + NR)"
//     11|12)   rat="5G"
//     2|4|5|6) rat="3G"
//     0|1|3)   rat="2G"
//     *)       rat="unknown (AcT $act)"
//
// 展锐这套与 3GPP 的 0~7 兼容（7 = E-UTRAN），8 以上是它的扩展
// （11/12 = NR，13 = EN-DC 双连接）。
// 认不出的码一律返回 ok=false —— **宁可不显示，也不臆造**。
func ratFromACT(act int) (netTech string, tech CellTech, label string, ok bool) {
	switch act {
	case 7:
		return "LTE", CellTechLTE, "4G LTE", true
	case 13:
		// NSA：LTE 锚点 + NR 承载 ⇒ 按 5G 展示，但名字里说清是 NSA
		return "NR5G NSA", CellTechNR, "5G NR (NSA)", true
	case 11, 12:
		return "NR5G", CellTechNR, "5G NR", true
	case 2, 4, 5, 6:
		return "WCDMA", CellTechWCDMA, "3G WCDMA", true
	case 0, 1, 3:
		return "GSM", CellTechGSM, "2G GSM", true
	}
	return "", "", "", false
}

// acTForRAT 挑一个用于判制式的 `<AcT>`（展锐：数据域最贴近“现在跑的是什么网”）。
//
// 优先级：EPS(CEREG) → PS(CGREG) → 5GS(C5GREG) → CS(CREG)。
// 用 `RegState.Raw` 回去看“那一行里到底有没有 AcT 字段”—— 字段不存在与 AcT=0
// 是两回事（后者是合法的 2G）。
func (mod *Modem) acTForRAT() (int, bool) {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	for _, rs := range []*RegState{mod.urcNet.eps, mod.urcNet.ps, mod.urcNet.fiveG, mod.urcNet.cs} {
		if rs == nil {
			continue
		}
		if act, ok := urcACTFromLine(rs.Raw); ok {
			return act, true
		}
	}
	return 0, false
}

// networkStatusFromURC 用 URC 供的状态 + 内核 + 上次完整查询的缓存（+ 必要时补一条 AT）拼出网络状态。
//
// 为什么能省：注册（CS/PS/EPS/5GS）、运营商（`+SPNWNAME`）、制式（`+CxREG` 的 `<AcT>`）、
// 频段（`+SPCLB`）、服务小区（`+SPPCI`/`+CxREG`/`+CESQ`）**全都在 URC 里**，
// 它们正是网络卡片上最显眼的那几项。
//
// URC 里没有的（**2026-10-06 起按"可以用 AT 补"处理**，用户明确要求）：
//
//	APN         → `/api/params` 那条已验证的路（`AT+CGDCONT?` + `AT+CGCONTRDP=<cid>`），
//	              这里是**复用它的缓存**（TTL 20s），绝大多数调用一次 AT 都不发；
//	本机地址     → 内核（`HostDataPlane`），比任何 AT 查询都准（展锐的 CGPADDR/CGCONTRDP 都回 ERROR）；
//	模式偏好/CFUN→ 用户主动看/改那几个页面时才查（不在推送路径上发）。
//
// ⚠️ 仍然**补不出来**的（穷举验证过，不是没做）：**频点(EARFCN) / 载波聚合 / 邻区** ——
// 这台固件的 `AT+CPSI` / `+SPPHYCHINFO` / `+SPNRCFGINFO` / `+SPFDDRATNCELL` / `+SPSCSQ` / `+SPBARINFO`
// 全部不支持；`+SPNWINFO` / `+SPQ4GNCELL` 存在但需要未知参数；`AT+SPENGMD`（工程模式）
// 会把模组 AT 队列堵死（2026-10-05 两次实测，第二次连数据面一起掉）⇒ **永久拉黑**。
func (mod *Modem) networkStatusFromURC() *NetworkStatus {
	mod.mu.Lock()
	base := cloneNetworkStatus(mod.networkStatus)
	urc := mod.urcNet
	op := mod.urcOp
	sawOp := mod.urcSawOp
	mod.mu.Unlock()

	if base == nil {
		base = &NetworkStatus{ModemID: mod.ID}
	}
	base.ModemID = mod.ID
	// ⚠️ QueriedAt 要刷新：它管 networkStatusDue / TTL，也就是界面刷新节拍。
	// 这个快照确实是“刚产出的”，只是数据来源换成了 URC —— 不能因为降频就让界面不动。
	base.QueriedAt = time.Now()

	// ⚠️ 拷值再赋指针：urc 里那几个指针会被后续上报直接改写，
	// 共享出去就是数据竞争（push 与跟随协程同时读写）。
	if urc.cs != nil {
		base.CSReg = *urc.cs
	}
	if urc.ps != nil {
		base.PSReg = *urc.ps
	}
	if urc.eps != nil {
		rs := *urc.eps
		base.RegEPS = &rs
	}
	if urc.fiveG != nil {
		rs := *urc.fiveG
		base.Reg5G = &rs
	}

	// 制式（5G/4G）：URC 的 `+CxREG` 里本来就带 `<AcT>`，用厂商自己的码表换算。
	// ⚠️ 这是 F50 上制式的**唯一**在线来源：`AT+QNWINFO` 在这台机上回 +CME ERROR: 4。
	// 频段/频点 URC 里没有 ⇒ 只能留空（不臆造）。
	if act, ok := mod.acTForRAT(); ok {
		if netTech, tech, label, ok2 := ratFromACT(act); ok2 {
			base.NetTech, base.Tech, base.TechLabel = netTech, tech, label
		}
	}
	// 频段：`+SPCLB`（文件里唯一来源）。只按原值写 `BAND <n>`，
	// **不填 band_label**（`B3`/`n78` 那种前缀要分 LTE/NR，而分不清 —— 不猜）。
	//
	// ⚠️ 两条规则缺一不可（与上面的服务小区同一个坑）：
	//   · 本机 URC 拼出来的值**要能刷新** —— 它会随接口返回进缓存，
	//     若只看 `base.Band == 0` 就会“第一次填完就永远冻住”（实测踩到：SPCLB 已变 3，
	//     顶层还显示 BAND 5）；
	//   · 更精确的来源（扇描 / QNWINFO / Extra）不被覆盖。
	//     识别方式：**我们自己写的那份一定没有 band_label**，
	//     而 QNWINFO/扇描/Extra 那条路一定会填 band_label（`n78`/`B3`）。
	if mod.urcSawBand && base.BandLabel == "" {
		base.Band = mod.urcBand
		base.BandText = "BAND " + strconv.Itoa(mod.urcBand)
	}

	// 运营商：`+SPNWNAME` 同时给了 PLMN 与名字（引号里是 UCS2 hex）。
	// 它比“由 PLMN 推中文名”还准 —— 名字是**网络/模组自己报的**。
	// ⚠️ 这是 F50 上运营商**唯一**的在线来源（AT+QNWINFO 回 +CME ERROR: 4）。
	if sawOp && op.ok() {
		base.MCC, base.MNC = op.mcc, op.mnc
		base.PLMN = op.mcc + op.mnc
		if n := opNamePreferred(op); n != "" {
			base.Operator = n
		}
	} else if base.MCC != "" && base.Operator == "" {
		// 还没等到 SPNWNAME 时，拿已有的 PLMN 推一个（与原来那条兜底一致）
		base.Operator = cell.OperatorName(base.MCC, base.MNC)
	}

	// 本机地址：直接读内核（展锐的 AT+CGPADDR / AT+CGCONTRDP 都是 +CME ERROR，问了也白问）。
	// 这是**真实数据面地址**（厂商 netifd 接口 sipa_eth0），比 PDP 查询还准。
	// 只针对展锐 SIPC（见 mu300FileBackedState）—— 其它模组的地址必须从自己的 AT 拿。
	if mu300FileBackedState(mod) {
		if iface, v4, v6 := mod.HostDataPlane(); iface != "" {
			if v4 != "" {
				base.IPv4 = v4
			}
			if v6 != "" {
				base.IPv6 = v6
			}
		}
	}

	// APN（当前**在用**的那个）：URC 里没有这一项 —— `+CGEV: ME PDN ACT` 只报 cid、
	// 不带 APN ⇒ 只能走 AT。这条路 `/api/params` 早就通了（`AT+CGDCONT?` +
	// `AT+CGCONTRDP=<cid>`，展锐只认带 cid 的写法，见 UnisocCommand.APNCandidates），
	// 这里直接**复用它的缓存**（TTL 20s）：
	//   · 有缓存 ⇒ 一次 AT 都不发（绝大多数调用走这里）；
	//   · 过期了 ⇒ 才发一轮（nr2 上 3 条 ≈ 120ms，20 秒摊下来可以忽略）。
	// 2026-10-06：用户报「当前网络 APN 是空的」——以前这里是硬编码留空（"接口只读文件"），
	// 现在按"URC 没有的可以用 AT 补"处理。
	// ⚠️ 只在没有更精确来源时填（与频段/服务小区同一条规则）。
	if base.APN == "" {
		if ps, err := mod.paramsStatusOrQuery(false); err == nil && ps != nil && ps.APN != "" {
			base.APN = ps.APN
		}
	}

	// 签约速率（AMBR）+ QCI：URC 里同样**没有**这一项（`+CGEV` 只报 cid），只能走 AT。
	// 与 APN 同一条规矩：**复用 QueryAMBR 自己的 TTL 缓存**（60s，见 data.go）——
	// 有缓存就一条 AT 都不发，过期才发一轮（本机一条 `AT+C5GQOSRDP`，毫秒级）。
	//
	// 2026-10-06 真机回归：用户报「F50 UI 上不显示签约速率 / QCI」——
	// 以前这条 URC 路径**根本没调过 QueryAMBR**（只有 AT 路径在 `queryNetworkStatus` 末尾调），
	// 所以指令接了、缓存也有，界面上却永远是“未上报”。
	//
	// ⚠️ 不能写成 `if base.AMBR == nil` —— base 是上一轮的克隆，那样“第一次填完就永远冻住”
	// （与频段/服务小区同一个坑）；QueryAMBR 自带 TTL，每轮覆盖是安全的。
	// 查不到时**保留上一次的值**（与下面的 Traffic 同一个取舍）：AMBR 只在 PDP 重建时才变，
	// 偶发一次 AT 超时不该把卡片闪成“未上报”——那句文案会误导成“该固件不支持”。
	if r := mod.QueryAMBR(); r != nil {
		ambr := *r
		base.AMBR = &ambr
	}

	// 服务小区：URC 里能拼的字段（PCI / 小区 ID / TAC / 信号 / 制式）。
	//
	// ⚠️ 两条规则缺一不可：
	//   · **扫描来的结果优先**：那份更全（带频段/频点/邻区），绝不被这份部分数据覆盖；
	//   · **本机 URC 拼出来的那份要能刷新**：它会随 pushNetworkState 进缓存，
	//     若只看 `base.Serving == nil` 就会“第一次拼完就永远冻住”（小区换了 PCI 不变）。
	//     识别方式：自己拼的那份 `Raw` 以 `urc:` 开头（见 servingCellFromURC）。
	urcOwnedServing := base.Serving != nil && strings.HasPrefix(base.Serving.Raw, cellRawFromURC)
	if base.Serving == nil || urcOwnedServing {
		if ci := mod.servingCellFromURC(); ci != nil {
			base.Serving = ci
			base.ServingCount = 1
			base.ScannedAt = time.Now()
		}
	}

	// ⚠️⚠️ 速度**必须**带上：前端收到网络推送时会用它重建卡片
	//（`renderCurNet(st.traffic)`），而流量推送是**另一条**路 ——
	// 这边给 null、那边给值，界面上那行速度就会一隐一显地闪
	//（用户实测报的“网络速度老是隐藏在显示”）。
	if r := mod.TrafficSnapshot(); r.Supported {
		rate := r
		base.Traffic = &rate
	}

	// 漫游状态 + 「数据漫游」开关（roaming.go）：与 AT 路径同一个口径、同一个落点，
	// 页面「概览 → 信号」那一行在**两条路上都能看到**（F50 走的就是这条 URC 路径）。
	// ⚠️ 每轮覆盖（同 AMBR）：注册域从本地变漫游后页面必须跟着动。
	mod.fillRoam(base)
	return base
}

// cellRawFromURC 标记“这份服务小区是本机文件（URC）拼的”。
//
// 用途：下一次重建时能认出它是**自己人**（可刷新），而扫描来的那份不能碰
// （扫描结果更全：带频段/频点/邻区）。
const cellRawFromURC = "urc:+SPPCI/+CxREG/+CESQ"

// servingCellFromURC 用 URC + 内核里**确实有**的字段拼出“服务小区”。
//
// 有来源的：制式（`<AcT>`）/ PCI（`+SPPCI`）/ 小区 ID+TAC（`+CxREG`）/ RSRP·RSRQ·SINR（`+CESQ`）/
// 运营商与 PLMN（`+SPNWNAME`）。
//
// ⚠️⚠️ **没有来源、只能留 0 的**（真机取证过，不是懒得写）：
//
//	· 频段 / 频点(EARFCN) / 带宽：这台机的 `AT+QNWINFO` 与 `AT+CPSI` 都回 `+CME ERROR: 4`，
//	  URC 里也**没有任何** band/EARFCN 上报（固件里 EARFCN/SSB 只是内部变量名，不是上报格式）；
//	· 邻区：只能靠基站扇描（AT）—— 那属于“动作”，不在只读文件的路径里。
//
// 拼不出任何东西（既无 PCI 也无小区 ID）时返回 nil，宁可不显示也不编。
func (mod *Modem) servingCellFromURC() *cell.CellInfo {
	mod.mu.Lock()
	pci, hasPCI := mod.urcPCI, mod.urcSawPCI
	sig := mod.SignalInfo
	op := mod.urcOp
	sawOp := mod.urcSawOp
	// 数据域优先：EPS(CEREG，带 TAC) → PS(CGREG，带 LAC) → CS(CREG)
	var cellID, tacHex string
	for _, rs := range []*RegState{mod.urcNet.eps, mod.urcNet.ps, mod.urcNet.cs} {
		if rs == nil {
			continue
		}
		if cellID == "" {
			cellID = rs.CellID
		}
		if tacHex == "" {
			tacHex = rs.LAC
		}
	}
	mod.mu.Unlock()

	if !hasPCI && cellID == "" {
		return nil
	}
	ci := &cell.CellInfo{Serving: true, CellID: cellID, Raw: cellRawFromURC}
	if hasPCI {
		ci.PCID = pci
	}
	if tacHex != "" {
		if v, err := strconv.ParseInt(tacHex, 16, 32); err == nil {
			ci.TAC = int(v)
		}
	}
	if act, ok := mod.acTForRAT(); ok {
		if _, tech, _, ok2 := ratFromACT(act); ok2 {
			ci.Tech = tech
		}
	}
	// 频段同样用 `+SPCLB`（数值本身，前缀不猜 —— 见 urcBandFromLine）
	if band, saw := mod.urcBand, mod.urcSawBand; saw {
		ci.Band = band
	}
	if sawOp && op.ok() {
		ci.MCC, ci.MNC = op.mcc, op.mnc
		ci.Operator = opNamePreferred(op)
	}
	ci.RSRP, ci.RSRQ, ci.SINR = sig.NRRSRP, sig.NRRSRQ, sig.NRSINR
	// RSSI 故意不填：这台机的 `+CSQ` 第一个字段不是标准 0..31 索引
	//（实测 34/35/255），换算成 dBm 会出一个假值。留 0 = 未上报。
	return ci
}

// opNamePreferred 运营商显示名的优先级：全称 → 简称 → 按 PLMN 推。
func opNamePreferred(op urcOpSnapshot) string {
	if s := strings.TrimSpace(op.full); s != "" {
		return s
	}
	if s := strings.TrimSpace(op.shrt); s != "" {
		return s
	}
	return cell.OperatorName(op.mcc, op.mnc)
}

// networkStatusOrQuery 取网络状态：TTL 内查过就直接用缓存，否则真查。
func (mod *Modem) networkStatusOrQuery() (*NetworkStatus, error) {
	mod.networkQueryMu.Lock()
	defer mod.networkQueryMu.Unlock()
	if st := mod.networkStatusSnapshot(); st != nil {
		return st, nil
	}
	return mod.queryNetworkStatus()
}

func (mod *Modem) networkStatusDue(now time.Time, interval time.Duration) bool {
	if interval <= 0 {
		return false
	}
	mod.mu.Lock()
	st := mod.networkStatus
	var queriedAt time.Time
	if st != nil {
		queriedAt = st.QueriedAt
	}
	mod.mu.Unlock()
	return queriedAt.IsZero() || now.Sub(queriedAt) >= interval
}

// QueryNetworkStatus 汇总网络类型 / 接入 / 注册状态（只读，不触发小区扫描）。
// 服务小区详情取自上一次扫描的缓存；没有缓存时相应字段留空。
func (mod *Modem) QueryNetworkStatus() (*NetworkStatus, error) {
	mod.networkQueryMu.Lock()
	defer mod.networkQueryMu.Unlock()
	return mod.queryNetworkStatus()
}

func (mod *Modem) queryNetworkStatus() (*NetworkStatus, error) {
	// 展锐 SIPC（F50）：接口路径**从不**走完整 AT 查询 ——
	// 字段全部来自 URC 日志 / 内核 / 上一次缓存（见 networkStatusFromURC）。
	// AT-only 字段（制式/频段/APN/模式偏好/CFUN）在展锐 SIPC 上**不再补查** ——
	// 用户明确要求“接口只读文件、不发 AT”。
	if mod.networkQueryPlan() == planURC {
		return mod.networkStatusFromURC(), nil
	}
	st := &NetworkStatus{ModemID: mod.ID, QueriedAt: time.Now()}

	// AT+QNWINFO
	resp, err := mod.SendAT(mod.cmdImpl.CommandNetInfo())
	if err != nil {
		// 厂商明确声明"本机不提供网络接入信息"（实现里返回空串）时，这里**不该**
		// 把整个网络状态判为失败 —— 后面还有"从服务小区应答里补制式/频段"的兜底。
		// 实测展锐 UMS9620（中兴 F50）就是这种：AT+QNWINFO 回 +CME ERROR: 4。
		if !errors.Is(err, errATNoCommand) {
			return nil, err
		}
	}
	st.RawNetInfo = strings.Join(resp, "|")
	if qi, ok := ParseQNWINFO(resp); ok {
		st.NetTech, st.PLMN, st.BandText, st.EARFCN = qi.NetTech, qi.PLMN, qi.BandText, qi.EARFCN
		st.Tech, st.TechLabel = TechLabel(qi.NetTech)
		if d := DuplexOf(qi.NetTech); d != "" {
			st.Duplex = d
		}
		st.MCC, st.MNC = SplitPLMN(qi.PLMN)
		st.Band = common.BandNumFromText(qi.BandText)
		st.BandLabel = common.BandLabel(st.Tech, st.Band)
	}
	// QNWINFO 不是所有模组都支持（MT5700 这类未验证型号）：
	// 拿不到就退回服务小区应答，从 +QENG/+FENG 或 +CPSI 行里补制式/频段/PLMN，
	// 避免网络面板整片空白。
	if st.NetTech == "" {
		applyServingNetInfo(st, mod)
	}

	// CS / PS 注册状态：两条都查，避免误读（5G SA 下 CREG 常常是 0,0）
	if resp, err := mod.SendAT(mod.cmdImpl.CommandNetworkReg()); err == nil {
		st.CSReg, _ = ParseReg(resp)
	} else {
		logger.Warn("query CREG failed", zap.Int("modem", mod.ID), zap.Error(err))
	}
	if resp, err := mod.SendAT("AT+CGREG?"); err == nil {
		st.PSReg, _ = ParseReg(resp)
	} else {
		logger.Warn("query CGREG failed", zap.Int("modem", mod.ID), zap.Error(err))
	}

	// ⚠️ 5G SA 的坑：模块注册在 5GS 上时，CREG / CGREG / CEREG 会**长期停在 0,0**，
	// 界面就会显示"数据域未注册"，但数据明明是通的（IPv6 都在）。
	// 实测 RM502Q-GL：+C5GREG: 0,1（已注册，本地网）而上面三条全是 0,0。
	// 所以只在"CS 与 PS 都看着没注册"时补查：先 5GS，不支持再退 EPS。
	// 平时正常的模组（如 RM500U 报 CGREG 0,1）连这条指令都不会发。
	if needRegFallback(st) {
		if rs, ok := mod.queryReg("AT+C5GREG?"); ok {
			st.Reg5G = rs
		} else if rs, ok := mod.queryReg("AT+CEREG?"); ok {
			st.RegEPS = rs
		}
	}
	// 载波聚合：属于"此刻"的状态，实时查询（AT+QCAINFO）。
	// 不能复用扫描缓存 —— 否则会出现"频段是实时的 LTE B5、CA 却是几分钟前扫描到的
	// n78"这种自相矛盾的展示。模组报 ERROR/不支持的厂商都会落到空列表。
	if cc, err := mod.QueryCAInfo(); err == nil {
		st.CA = cc
		st.CAActive = CCsAggregated(cc)
	} else {
		logger.Warn("query CA info failed", zap.Int("modem", mod.ID), zap.Error(err))
	}

	// 当前网络：APN（优先取激活的 PDP 上下文，含本机 IP）
	if apn, _, err := mod.QueryAPN(); err == nil {
		st.APN = apn
	} else {
		logger.Debug("query APN failed", zap.Int("modem", mod.ID), zap.Error(err))
	}

	// 当前网络：本机 IPv4 / IPv6（AT+CGPADDR，双栈时两个都有）
	if ipv4, ipv6, err := mod.QueryPDPAddrs(); err == nil {
		st.IPv4, st.IPv6 = ipv4, ipv6
	} else {
		logger.Debug("query PDP address failed", zap.Int("modem", mod.ID), zap.Error(err))
	}

	// 当前网络：模组当前使用的组网模式偏好（可改，见 POST /api/lock/mode）
	if mode, err := mod.QueryModePref(); err == nil {
		st.ModePref = mode
	} else {
		logger.Debug("query mode_pref failed", zap.Int("modem", mod.ID), zap.Error(err))
	}

	// 当前网络：上下行速度。3GPP 没有"速率"指令，只能采样流量计数器做差分；
	// 模组不支持（或未声明计数器指令）时 Traffic 留空，前端显示"未上报"。
	if tr := mod.SampleTraffic(); tr.Supported {
		st.Traffic = &tr
	}

	// 当前网络：签约速率（AMBR）。与上面的**实测**速度是两码事：
	// 这是网络在建立 PDU 会话时下发的速率上限，模组不支持时留空。
	st.AMBR = mod.QueryAMBR()

	// 飞行模式状态：**读缓存，绝不发 AT+CFUN?**。
	//
	// ⚠️⚠️ 用户 2026-10-13 实测：在 FM160-CN 上发 `AT+CFUN?` 会让**模块掉线**
	// （而这里在每轮状态刷新路径上 ⇒ 等于周期性地把模组打掉，掉线之后连状态都查不到，
	//  表现就是"页面一直是空的 / 模组不见了"）。
	// 缓存由 SetAirplane（用户开关飞行模式）与 QueryFuncMode（只在用户手动查时走）刷新。
	if fun := mod.CFUNCached(); fun != 0 {
		st.CFUN = fun
		st.Airplane = fun == CFUNAirplane
	} else {
		st.Airplane = mod.AirplaneOn()
	}

	// 附加网络信息（带宽 / 调制 / CQI / 上行发射功率）：只有声明了该能力的厂商才会发指令 ——
	// 目前只有芯讯通 SIM8200（AT+CNWINFO?），移远 / 广和通 / MT5700 完全不受影响。
	if cmd := mod.extraNetInfoCommand(); cmd != "" {
		if resp, err := mod.SendAT(cmd); err == nil && !common.RespHasError(resp) {
			// 两种方言：芯讯通 AT+CNWINFO?（调制/带宽/功率）、鼎桥 AT^HFREQINFO?（频段/带宽）
			if ei, ok := ParseExtraNetInfo(resp); ok {
				st.Extra = ei
			} else if ei, ok := ParseHFREQINFO(resp); ok {
				st.Extra = ei
			}
			// 频段：QNWINFO 与服务小区都给不出时，用额外信息里的 band 补上。
			// 实测 MT5700 的 AT+QNWINFO 回 ERROR、^MONSC 里也没有 band，
			// 只有 AT^HFREQINFO? 带 n78 —— 不补这一下「频段」永远是 `-`。
			if st.Band == 0 && st.Extra != nil && st.Extra.Band > 0 {
				st.Band = st.Extra.Band
				st.BandText = BandTextFor(st.Tech, st.Band)
				st.BandLabel = common.BandLabel(st.Tech, st.Band)
			}
		} else {
			logger.Debug("query extra net info failed", zap.Int("modem", mod.ID))
		}
	}

	// 服务小区详情来自扫描缓存（不占串口），带上扫描时间供前端标注数据新鲜度
	mod.mu.Lock()
	cells := append([]cell.CellInfo{}, mod.Cells...)
	st.ScannedAt = mod.CellScanAt
	mod.mu.Unlock()
	for i := range cells {
		if cells[i].Serving {
			c := cells[i]
			st.Serving = &c
			st.ServingCount++
		} else {
			st.NeighbourCnt++
		}
	}
	// 没有扫描缓存时实时补一次服务小区，让「网络」卡片立刻能看到 Cell ID / PCI / TAC，
	// 不必先去基站扫描页手动扫一次（只补小区详情，不覆盖上面的制式/频段/双工）
	mod.ensureServingCell(st)
	if st.ServingCount == 0 && st.Serving != nil {
		st.ServingCount = 1
	}

	// 运营商：优先用服务小区（已按 PLMN 推断中文名），否则用 PLMN 兜底
	if st.Serving != nil && st.Serving.Operator != "" {
		st.Operator = st.Serving.Operator
	} else if st.MCC != "" {
		st.Operator = cell.OperatorName(st.MCC, st.MNC)
	}

	// 漫游状态 + 「数据漫游」开关（roaming.go）：页面「概览 → 信号」那一行照它画。
	// 每轮覆盖（同 AMBR / Traffic），不从缓存里继承 —— 否则注册域一变（本地↔漫游）页面不动。
	mod.fillRoam(st)

	mod.cacheNetworkStatus(st)
	return st, nil
}

// ---- from: modem_device.go ----

// ---- from: modem_airplane.go ----
//
// 取值常量与解析在 net.go（CFUNFull / CFUNAirplane / ParseCFUN）；
// 这里负责"下发 + 回读确认 + 清缓存"。
// QueryFuncMode 读当前功能模式（AT+CFUN?），返回 (模式值, 是否成功)
//
// 顺带把"当前是不是飞行模式"记到 mod.airplaneOn 上：自动组网循环、"主机侧拨号器该不该跑"
// 每轮都要判断它，不能每轮都发一条 AT。所有调过这个函数的地方都会刷新它。
func (mod *Modem) QueryFuncMode() (int, bool) {
	resp, err := mod.SendAT("AT+CFUN?")
	if err != nil {
		logger.Debug("query CFUN failed", zap.Int("modem", mod.ID), zap.Error(err))
		return 0, false
	}
	v, ok := ParseCFUN(resp)
	if ok {
		mod.noteFuncMode(v)
	}
	return v, ok
}

// noteFuncMode 记下刚读到的功能模式，返回是不是飞行模式。
//
// 单独抽出来是为了能单测（QueryFuncMode 自己要走串口，测不了）。
func (mod *Modem) noteFuncMode(v int) bool {
	on := v == CFUNAirplane
	mod.airplaneOn.Store(on)
	mod.cfunLast.Store(int32(v))
	return on
}

// CFUNCached 上一次读到/设置过的功能模式（**不发 AT**）。
//
// 周期性路径（状态刷新、配额评估）只能用它 —— 实测发 `AT+CFUN?` 会把 FM160-CN 打掉线
// （用户 2026-10-13）。返回 0 表示"还没读到过"。
func (mod *Modem) CFUNCached() int {
	if mod == nil {
		return 0
	}
	return int(mod.cfunLast.Load())
}

// AirplaneOn 当前是不是飞行模式（不发 AT，读 QueryFuncMode 刷新的标记）。
// 「主机侧拨号器该不该跑」与自动组网循环都看它，见 hostDialStopReason / autoWANForModem。
func (mod *Modem) AirplaneOn() bool {
	if mod == nil {
		return false
	}
	return mod.airplaneOn.Load()
}

// AirplaneBlockedReason 这台机型为什么**不能**开飞行模式（能用则返回空串）。
//
// ⚠️⚠️ 中兴 F50（展锐 UMS9620 / SIPC 一族）**不给开**，用户 2026-10-06 明确要求去掉：
// 这台设备的模组射频就是整机**唯一**的上行 —— 关掉射频后连设备自己的管理页/SSH/内网
// 通道一起断（用户原话“不然网络内网也断了”）；而且实测（2026-10-06 现场）在 F50 上
// 这东西还稳不住：厂商自己的数据面守护（/etc/init.d/mu300-post 的 `mobile-data watch`
// 实例，procd 会把它拉回来）把“射频必须开着”当硬规则，6 秒内就把 CFUN 改回 1，
// 必要时还 `AT+SFUN=4` 重启模组 —— 现场整机失联过一次。
//
// 只挡“开”的方向：万一射频已被别的东西关了，“关飞行模式”仍然是唯一的恢复手段，
// 而且它要的是“射频开”，跟厂商守护的方向一致，不存在被改回去的问题。
func (mod *Modem) AirplaneBlockedReason() string {
	if mod == nil {
		return ""
	}
	if netdev.IsMu300SIPCChan(mod.ATPort) {
		return "本机型不支持飞行模式：模组射频就是整机唯一上行，关掉射频会把设备自己的管理页/内网通道一起断掉"
	}
	// 【2026-10-13 真机实测 · H29K + FM160-CN】广和通这类**数据呼叫归主机侧拨号器**的模组
	// 也不能"开"飞行模式：
	//
	//	`AT+CFUN=4` 之后这台模组的 AT 口**不再响应** —— 串口读心跳停止 ⇒ isRunning 掉成 false
	//	⇒ 自动组网循环每一轮都在 `!isRunningNow()` 那里提前 return ⇒ 主机侧拨号器**永远拉不起来**。
	//	用户看到的现象就是「IPv6 掉了、整机没网，必须重启 modemd 才恢复」（2026-10-13 现场日志：
	//	`set airplane ok {enabled:true,cfun:4}` → `airplane on: host dialer stopped` → 之后 5 分钟
	//	wwan0 一直是 DOWN、没有任何拨号器进程）。
	//
	// 关的方向（AT+CFUN=1）没有这个问题（模组正常枚举、循环照常），所以**只挡"开"**。
	// 要主动断网请用页面上的「断开」（ManualStop）—— 它不碰射频，随时可恢复。
	if fibocom.AirplaneBlocked(mod.Vendor) {
		return "本机型不支持开飞行模式：实测 AT+CFUN=4 后本模组 AT 口不再响应，会让自动组网卡死、必须重启 modemd 才能恢复。要断网请用「断开」"
	}
	return ""
}

// SetAirplane 开关飞行模式，返回设置后回读到的功能模式值。
// 写完带一次回读确认（最多等 ~3 秒）：本固件是立刻生效，但不同固件生效时间不一样，
// 不看回读的话界面可能显示"已关闭飞行模式"而模组其实还没恢复。
//
// 开之前先过 AirplaneBlockedReason：机型不给开的（F50 这类整机只有一路上行的设备）
// 直接报错，**连 AT 都不发** —— 否则下场就是把用户的内网/管理通道一起弄断。
func (mod *Modem) SetAirplane(on bool) (int, error) {
	if on {
		if why := mod.AirplaneBlockedReason(); why != "" {
			return 0, errors.New(why)
		}
	}
	cmd := AirplaneCommand(on)
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return 0, err
	}
	if msg := atError(resp); msg != "" {
		return 0, fmt.Errorf("%s: %s", cmd, msg)
	}

	want := func(v int) bool {
		if on {
			return v == CFUNAirplane
		}
		return v == CFUNFull
	}
	cur, got := -1, false
	deadline := time.Now().Add(3 * time.Second)
	for {
		if v, ok := mod.QueryFuncMode(); ok {
			cur, got = v, true
			if want(v) {
				mod.afterAirplaneChange(on)
				return v, nil
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !got {
		return 0, fmt.Errorf("设置后读不回 AT+CFUN? 状态")
	}
	return cur, fmt.Errorf("设置未生效：模组仍报 CFUN=%d", cur)
}

// afterAirplaneChange 飞行模式状态确实变了之后的收尾（QMI 的数据面归主机侧拨号器）。
//
// 打开：**立即**把它结束掉 —— 只发 AT+CFUN=4 的话，主机侧那个 quectel-CM/fibocom-dial
// 进程还在跑（占着 /dev/cdc-wdm0、wwan0 可能还留着地址），看着就像"没断网"。
// 关闭：把"自动拨号 60 秒才试一次"的退避清掉，让循环下一轮就把链路重新拉起来。
func (mod *Modem) afterAirplaneChange(on bool) {
	if on {
		mod.clearSignalCache()
		if note := stopHostDial(mod.ID, "飞行模式已开（射频关闭），数据链路一起停"); note != "" {
			logger.Info("airplane on: host dialer stopped",
				zap.Int("modem", mod.ID), zap.String("note", note))
		}
		// 拨号状态也要跟着改：否则页面还显示"已连接"
		mod.dialUpdate(func(s *dial.DialState) {
			if s.State == dial.DialConnected || s.State == dial.DialDialing {
				s.State, s.StateText = dial.DialIdle, dial.DialStateText(dial.DialIdle)
				s.IPv4, s.IPv6, s.HostIface, s.HostIPs = "", "", "", nil
				s.ConnectedAt = time.Time{}
			}
		})
		pushDial(mod)
		return
	}
	// 关闭飞行模式：别让上一轮的失败退避把恢复拖成 60 秒
	resetAutoDialTried(mod.ID)
}

// clearSignalCache 关掉射频后清掉缓存的信号读数，并推一次空读数。
//
// ⚠️ 必须清：射频关掉后模组**不会把信号值改成 0**，而是继续回上一次的读数
// （实测：开了飞行模式 `AT+CSQ` 回 `99,99`，但 `+QENG:"servingcell"` 直接回 SEARCH、
// 一个字都不上报，于是 SignalInfo 里的 nr_rsrp/rsrq/sinr 一直是关射频前那几个值）——
// 界面就会「信号一直不动」，看着像坏了。
func (mod *Modem) clearSignalCache() {
	mod.mu.Lock()
	mod.SignalInfo = SignalInfo{RSSI: 99, BER: 99} // 99 = 3GPP 的"无法测得"
	mod.mu.Unlock()
	pushSignal(mod)
}

// ---- from: modem_usbnet.go ----

//
// 取值表与解析在 net.go（Mode / Status / Modes / ParseUSBnet）；
// 这里负责"向厂商要指令 + 下发 + 回读"，以及让配置生效的复位。
// usbnetConfigurator 取当前厂商的 USB 网卡模式指令；未实现该能力的厂商返回 nil
