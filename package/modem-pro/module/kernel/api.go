package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"fmt"
	"modemd/module/cell"
	"modemd/module/httpx"
	"modemd/module/luciauth"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ---- from: api.go ----

// ---- from: routes.go ----
// HTTP 路由集中在这里：main 只负责"装配"（读配置 / 初始化日志 / 启动循环），
// 具体接口挂在哪个路径属于领域知识，跟 handler 放在一起更好维护。
// ui 是内置调试台页面（由 main 传进来 —— 页面是 go:embed 的，
// 而 embed 只能引用当前目录及子目录，所以 files/web 与 web.go 必须留在根目录）。
// RegisterRoutes 注册全部接口并返回 http.Handler。
//
// 最外层套了「跟随 LuCI 登录」中间件（见 luciauth.go）：[ui] require_luci_login 打开后，
// 除了静态页与 /api/version，其它接口都要求请求里带着**有效的 LuCI 会话 cookie**。
// 没打开（默认）时它是一层纯透传，没有任何行为变化。
func RegisterRoutes(mgr *ModemManager, ui http.HandlerFunc) http.Handler {
	mux := http.NewServeMux()

	// WebSocket 实时推送
	mux.HandleFunc("/ws", apiWS(mgr, wsHubInst))
	// 屏幕端（tftdisplay）专用：免密、只认双方定死的钥匙，见 displayws.go
	mux.HandleFunc(DisplayWSPath, apiDisplayWS(mgr, wsHubInst))

	// 模组列表 / 信号 / 网络
	mux.HandleFunc("/api/modems", apiListModems(mgr))
	mux.HandleFunc("/api/signal", apiQuerySignal(mgr))
	mux.HandleFunc("/api/network", apiQueryNetwork(mgr))
	mux.HandleFunc("/api/network/status", apiNetworkStatus(mgr))
	mux.HandleFunc("/api/netinfo", apiNetInfo(mgr))
	mux.HandleFunc("/api/vendor/cmds", apiVendorCmds(mgr))
	// 支持模块清单（USB / PCIe 模式）+ 每型号 AT 指令与解析格式
	// —— 数据源是 module/kernel/modem_catalog.go + modem_at_plan.go，见 api_modules.go
	mux.HandleFunc("/api/modules", apiModules(mgr))

	// 基站扫描
	mux.HandleFunc("/api/cells", apiScanCells(mgr))
	mux.HandleFunc("/api/cells/all", apiCellsAll(mgr))
	mux.HandleFunc("/api/netscan", apiNetScan(mgr))
	mux.HandleFunc("/api/netscan/stop", apiNetScanStop(mgr))

	// 短信
	mux.HandleFunc("/api/sms/send", apiSendSMS(mgr))
	mux.HandleFunc("/api/sms/read", apiReadSMS(mgr))
	mux.HandleFunc("/api/sms/storage", apiSMSStorage(mgr))
	mux.HandleFunc("/api/sms/delete", apiDeleteSMS(mgr))
	// 短信访问密码（解锁/锁定/查状态）—— 上面四个接口都要令牌，见 smsauth.go
	mux.HandleFunc("/api/sms/auth", apiSMSAuth(mgr))
	mux.HandleFunc("/api/sms/unlock", apiSMSUnlock(mgr))
	mux.HandleFunc("/api/sms/lock", apiSMSLock(mgr))
	// 页面上改密码（写回 config.toml 的 [ui] 段，只动那两行）
	mux.HandleFunc("/api/sms/password", apiSMSPasswordSet(mgr))

	// 模组信息 / 状态 / 重启
	mux.HandleFunc("/api/modem/info", apiModemInfo(mgr))
	mux.HandleFunc("/api/modem/status", apiModemStatus(mgr))
	mux.HandleFunc("/api/modem/clock", apiSetModemClock(mgr))
	mux.HandleFunc("/api/modem/reset", apiResetModem(mgr))
	// SIM 热插拔：当前卡身份 + 最近一次拔卡/插卡/换卡事件（见 simhotswap.go）
	mux.HandleFunc("/api/sim", apiSIMState(mgr))

	// USB 热插拔：模组设备清单（含插上了但没串口的）+ 插拔事件 + 手动补驱动
	mux.HandleFunc("/api/usb", apiUSBList(mgr))
	mux.HandleFunc("/api/usb/clear", apiUSBClear(mgr))
	mux.HandleFunc("/api/usb/bind", apiUSBBind(mgr))
	// 命令口绑定（按 USB 设备 VID:PID）：绑上就直接用那个串口，不再自动扫描/探测
	mux.HandleFunc("/api/atport/bind", apiATPortBind(mgr))

	// PCIe（MHI / wwan）模组：设备清单（含没绑驱动的）+ 软复位 + 手动绑驱动
	mux.HandleFunc("/api/pcie", apiPCIeList(mgr))
	mux.HandleFunc("/api/pcie/reset", apiPCIeReset(mgr))
	mux.HandleFunc("/api/pcie/bind", apiPCIeBind(mgr))

	// USB 网卡模式（模块对外上网协议）+ 重启模组使其生效
	mux.HandleFunc("/api/usbnet", apiUSBnet(mgr))
	mux.HandleFunc("/api/usbnet/set", apiSetUSBnet(mgr))
	mux.HandleFunc("/api/imei", apiIMEI(mgr))
	mux.HandleFunc("/api/imei/set", apiSetIMEI(mgr))
	mux.HandleFunc("/api/imei/restore", apiRestoreIMEI(mgr))
	mux.HandleFunc("/api/mode", apiModePref(mgr))
	mux.HandleFunc("/api/mode/set", apiSetModePref(mgr))
	// 版本 + 运行时信息（页脚显示）：核对“装的是哪个包 / 是不是旧页面”用
	mux.HandleFunc("/api/version", apiVersion())

	// 拨号上网（按 USB 类型选择拨号方式；见 dial.go / dial_engine.go / api_dial.go）
	mux.HandleFunc("/api/dial", apiDial(mgr))
	mux.HandleFunc("/api/dial/connect", apiDialConnect(mgr))
	mux.HandleFunc("/api/dial/disconnect", apiDialDisconnect(mgr))
	// 「拨号上网」页面上的 IPv6 开关（[net] wan_ipv6，四种拨号方式都按它来）
	mux.HandleFunc("/api/dial/ipv6", apiDialIPv6(mgr))
	mux.HandleFunc("/api/dial/mode", apiDialMode(mgr))
	// 「拨号上网」页面最上面那个**拨号总开关**（[net] dial_enabled，默认开）
	mux.HandleFunc("/api/dial/enable", apiDialEnable(mgr))

	// 主机网卡（FM350 特例：模组侧不发 DHCP，要手工把网卡 IP 配成与模组对应）
	mux.HandleFunc("/api/host/nic", apiHostNIC(mgr))
	mux.HandleFunc("/api/host/nic/save", apiHostNICSave(mgr))
	mux.HandleFunc("/api/host/nic/apply", apiHostNICApply(mgr))

	// 波动记录（网络/信号变化时间线）+ 信号采样历史（曲线）
	mux.HandleFunc("/api/changes", apiChanges(mgr))
	mux.HandleFunc("/api/changes/clear", apiClearChanges(mgr))
	mux.HandleFunc("/api/signal/history", apiSignalHistory(mgr))

	// 可写参数（APN / PDP 激活 / 选网方式）
	mux.HandleFunc("/api/params", apiParams(mgr))
	mux.HandleFunc("/api/params/apn", apiSetAPN(mgr))
	mux.HandleFunc("/api/params/apn/delete", apiDeleteAPN(mgr))
	mux.HandleFunc("/api/params/pdp", apiSetPDPActive(mgr))
	mux.HandleFunc("/api/params/netselect", apiSetNetSelect(mgr))

	// 流量清零 / USSD / 运行日志
	mux.HandleFunc("/api/traffic/reset", apiResetTraffic(mgr))
	mux.HandleFunc("/api/ussd", apiUSSD(mgr))
	mux.HandleFunc("/api/logs", apiLogs(mgr))

	// 调试用：直发 AT 指令（核对厂商/固件差异）
	mux.HandleFunc("/api/at", apiSendAT(mgr))
	// AT 命令口手动指定（界面上的下拉；自动探测只要求"回个 OK"，不够准，见 apiSetATPort）
	mux.HandleFunc("/api/at/port", apiSetATPort(mgr))
	// 本模组支持的常用 AT 指令清单（界面下拉按它渲染；见 atpresets.go）
	// —— 这个模块只给这个模块的指令，别的厂商的一条都不给。
	mux.HandleFunc("/api/at/presets", apiATPresets(mgr))

	// 锁网（锁频段 / 锁基站）
	mux.HandleFunc("/api/lock/status", apiLockStatus(mgr))
	mux.HandleFunc("/api/lock/caps", apiLockCaps(mgr))
	mux.HandleFunc("/api/lock/band", apiLockBand(mgr))
	mux.HandleFunc("/api/lock/cell", apiLockCell(mgr))
	mux.HandleFunc("/api/lock/mode", apiLockMode(mgr))
	mux.HandleFunc("/api/lock/unlock", apiLockUnlock(mgr))

	// 飞行模式（AT+CFUN：关/开模组射频）
	mux.HandleFunc("/api/airplane", apiAirplane(mgr))
	mux.HandleFunc("/api/airplane/set", apiSetAirplane(mgr))

	// 漫游：状态（读）+ 「数据漫游」开关（写，[net] allow_roaming）
	// 关掉后漫游注册时不拨号 / 保持数据链路，本地注册不受影响（见 roaming.go）
	mux.HandleFunc("/api/roam", apiRoam(mgr))
	mux.HandleFunc("/api/roam/set", apiRoamSet(mgr))
	// 登录（跟随路由器/LuCI 账号，见 luciauth.go）：三只里前两个在"放行名单"里
	// （一个是发会话的、一个是销会话的），/api/session 只报登录状态
	mux.HandleFunc("/api/login", luciauth.ApiLogin())
	mux.HandleFunc("/api/logout", luciauth.ApiLogout())
	mux.HandleFunc("/api/session", luciauth.ApiSession())

	// 流量配额（超出后限制联网：AT+CGACT 停用数据连接）
	mux.HandleFunc("/api/quota", apiQuota(mgr))
	mux.HandleFunc("/api/quota/set", apiSetQuota(mgr))
	mux.HandleFunc("/api/quota/reset", apiResetQuota(mgr))
	mux.HandleFunc("/api/quota/unblock", apiUnblockQuota(mgr))

	// 定时器（到点自动锁频段 / 锁基站 / 飞行模式…）
	mux.HandleFunc("/api/schedule", apiSchedule(mgr))
	mux.HandleFunc("/api/schedule/set", apiScheduleSet(mgr))
	mux.HandleFunc("/api/schedule/delete", apiScheduleDelete(mgr))
	mux.HandleFunc("/api/schedule/toggle", apiScheduleToggle(mgr))
	mux.HandleFunc("/api/schedule/run", apiScheduleRun(mgr))

	// 微信机器人（收到新短信推送到微信；协议见 ilink.go）
	// ⚠️ 都过短信密码闸门：能改推送目标 = 能把短信转发到任意微信
	mux.HandleFunc("/api/wechat", apiWeChat(mgr))
	mux.HandleFunc("/api/wechat/set", apiWeChatSet(mgr))
	mux.HandleFunc("/api/wechat/login", apiWeChatLogin(mgr))
	mux.HandleFunc("/api/wechat/verify", apiWeChatVerify(mgr))
	mux.HandleFunc("/api/wechat/logout", apiWeChatLogout(mgr))
	mux.HandleFunc("/api/wechat/test", apiWeChatTest(mgr))

	// 内置调试台页面（兜底路由，必须最后注册）
	mux.HandleFunc("/", ui)
	return luciauth.WrapLuciAuth(mux)
}

// InitWS 创建全局广播中心（没有客户端时不会发任何 AT 指令）
func InitWS() { wsHubInst = NewHub() }

// DiscoverAndStart 扫描并启动所有模组（同步）。
// 启动时调用一次，之后由后台周期巡检（main 按 scan_interval 决定）调用。
// ⚠️ 页面的「刷新模组」**不**走这里 —— 它只读缓存（见 apiListModems）。
// force=false：沿用「上次探测没应答」的否定缓存（周期巡检用，避免每秒重探）。
func DiscoverAndStart(mgr *ModemManager) { discoverAndStart(mgr, false) }

// ForceDiscoverAndStart 先清掉「非 AT 口」否定缓存再扫描一次（强制重探，同步）。
// 给排障 / 脚本用；HTTP 接口请用 DiscoverAndStartAsync（不阻塞请求）。
//
// 为什么需要强制重探：模组刚上电、或上一个进程被强杀（串口没关干净）时，
// AT 口会有一小段时间不应答；这段时间里探测结果被记成"非 AT 口"，
// 而否定缓存只在**串口集合变化**时才失效 —— 于是界面上怎么刷都刷不出来。
func ForceDiscoverAndStart(mgr *ModemManager) { discoverAndStart(mgr, true) }

// DiscoverAndStartAsync 在后台触发一轮发现（单飞去重），**立即返回**。
//
// 供 GET /api/modems?discover=1 用：那个请求以前会同步等完整轮探测
// （逐个串口发 AT，每口可达数秒），用户点一次界面就跟着卡一次。
// 现在接口瞬间返回；发现完成后由 discoverAndStart 的拓扑指纹推送 modems（WS），页面自动更新。
//
// 返回 false 表示已有一轮发现排队/在跑，这次被合并掉了（避免前端反复点导致 goroutine 堆积）。
func DiscoverAndStartAsync(mgr *ModemManager, force bool) bool {
	if !mgr.discBusy.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		defer mgr.discBusy.Store(false)
		discoverAndStart(mgr, force)
	}()
	return true
}

func discoverAndStart(mgr *ModemManager, force bool) {
	if err := mgr.discoverModems(force); err != nil {
		logger.Error("discover modems error", zap.Error(err))
		return
	}
	// 启动新模组：**不要**在 mgr.mu 内做 —— Start() 会 open 串口（阻塞），
	// 而它要等到自己完成才返回，会把所有写路径一起堵住。
	for _, mod := range mgr.listModems() {
		if !mod.isRunningNow() {
			if err := mod.Start(); err != nil {
				logger.Error("start modem failed", zap.Int("id", mod.ID), zap.Error(err))
			}
		}
	}
	// 拓扑指纹：只有模组增减时才推送列表（周期巡检每轮都推没意义）
	mgr.mu.Lock()
	sig := ""
	for _, mod := range mgr.listLocked() {
		sig += fmt.Sprintf("%d:%s;", mod.ID, mod.ATPort)
	}
	changed := sig != mgr.topoSig
	mgr.topoSig = sig
	mgr.mu.Unlock()

	if changed {
		pushModems(mgr)
	}
}

// ---- from: api.go ----

//
// 处理器按域分文件：api_net.go（信号/网络/小区/额外信息）、api_lock.go（锁网/选网模式）、
// api_net.go（信号/网络/小区/波动记录）、api_lock.go（锁网）、api_data.go（配额/参数/定时器）、
// api_modem.go（模组/射频设备/调试）、api_sms.go（短信/微信）、
// 各域处理器都是 package kernel，路由与公共件见本文件的 routes 段。
// 本文件只放公共件：WriteJSON、模组取用、POST body 解码、状态推送。
// writeJSON 统一 JSON 响应

// modemByID 取指定 ID 的模组（读无锁快照，见 listModems）。
func modemByID(mgr *ModemManager, id int) (*Modem, error) {
	mod, ok := mgr.modemByID(id)
	if !ok {
		return nil, fmt.Errorf("modem not found: %d", id)
	}
	return mod, nil
}

// modemFromQuery 从 ?modem_id= 取模组
func modemFromQuery(mgr *ModemManager, r *http.Request) (*Modem, error) {
	id, err := strconv.Atoi(r.URL.Query().Get("modem_id"))
	if err != nil {
		return nil, fmt.Errorf("modem_id must be an integer")
	}
	return modemByID(mgr, id)
}

// pushNetworkAfterParamChange 参数 / 射频类改动之后立刻推一次网络状态。
//
// 这类改动（写 APN、选网方式、飞行模式开关、基站扫描）都会把「当前网络 / 网络」卡片里的
// 东西变掉。改动的那个页面自己会重绘，但**其他开着的页面**与概览卡片只能等下一轮周期推送
// （数秒），所以改完顺手查一次并广播 —— 都是用户手动触发的操作，多一次只读查询可以承受。
func pushNetworkAfterParamChange(mod *Modem) {
	if st, err := mod.QueryNetworkStatus(); err == nil {
		pushNetworkState(mod, st)
	}
}

// ---- from: api_net.go ----

// ---- from: api_net.go ----
// apiQuerySignal GET /api/signal?modem_id=0 查询信号
// apiQuerySignal GET /api/signal?modem_id=0 查询信号

func apiQuerySignal(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		modemID, _ := strconv.Atoi(r.URL.Query().Get("modem_id"))
		mod, ok := mgr.modemByID(modemID)
		if !ok {
			httpx.WriteJSON(w, -1, nil, "modem not found")
			return
		}
		sig, err := mod.QuerySignal()
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		// 手动刷新也算一次观测：顺手记一条波动
		noteSignal(mod, sig)
		httpx.WriteJSON(w, 0, sig, "ok")
	}
}

// apiQueryNetwork GET /api/network?modem_id=0 查询网络注册状态
func apiQueryNetwork(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		modemID, _ := strconv.Atoi(r.URL.Query().Get("modem_id"))
		mod, ok := mgr.modemByID(modemID)
		if !ok {
			httpx.WriteJSON(w, -1, nil, "modem not found")
			return
		}
		reg, err := mod.QueryNetworkReg()
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, reg, "ok")
	}
}

// apiScanCells GET /api/cells?modem_id=0[&cache=1]
// 基站扫描：当前服务小区 + 附近基站（邻小区）+ 网络/运营商信息。
// cache=1 时直接返回上次扫描缓存，不占用串口。
func apiScanCells(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		modemID, _ := strconv.Atoi(r.URL.Query().Get("modem_id"))
		mod, ok := mgr.modemByID(modemID)
		if !ok {
			httpx.WriteJSON(w, -1, nil, "modem not found")
			return
		}
		if r.URL.Query().Get("cache") == "1" {
			httpx.WriteJSON(w, 0, cachedScanResult(mod), "ok")
			return
		}
		res, err := mod.ScanCells()
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		// 扫描顺带改掉了别处也在显示的两样东西，一并推出去，不然它们要等下一轮周期推送：
		//   · mod.CellScanAt → 「模组信息」里的「最近扫描」（属于模组列表字段，所以要推 modems）
		//   · 服务小区详情 → 「概览 → 网络」卡片（读的就是这份扫描缓存）
		pushModems(mgr)
		pushNetworkAfterParamChange(mod)
		httpx.WriteJSON(w, 0, res, "ok")
	}
}

// apiNetScan GET /api/netscan?modem_id=0&tech=lte|nr|both[&minutes=3..10]
//
// 主动扫网：3GPP 可见运营商（AT+COPS=?）+ 主动扫小区（芯讯通 AT+CNWSEARCH）。
// tech=both（也认 all）= LTE 与 NR **各扫一遍再合并**（各扫只影响扫小区那段）。
// minutes = 等模组搜完的最长时长（界面下拉 3~10 分钟，默认 3）——
// ⚠️ 越界一律夹到范围内：手改 URL 传个天文数字会把 AT 口占死。
// ⚠️ 这是慢接口：AT+COPS=? 是全网搜索，实测可能跑 3 分钟以上（扫小区还要再等 5~10 秒）。
// 它和「实时扫描」（/api/cells）的区别：后者读的是模组**已驻留**的测量结果（快），
// 这个让模组自己去搜（慢，但能列出没驻留的运营商/小区）。
func apiNetScan(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		modemID, _ := strconv.Atoi(r.URL.Query().Get("modem_id"))
		mod, ok := mgr.modemByID(modemID)
		if !ok {
			httpx.WriteJSON(w, -1, nil, "modem not found")
			return
		}
		// tech=lte|nr|both（both/all = 一起扫）；认不出一律按 LTE
		scope := NetScanLTE
		switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tech"))) {
		case "nr":
			scope = NetScanNR
		case "both", "all":
			scope = NetScanBoth
		}
		minutes := netScanDefaultMinutes
		if v := r.URL.Query().Get("minutes"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				minutes = n
			}
		}
		if minutes < netScanMinMinutes {
			minutes = netScanMinMinutes
		}
		if minutes > netScanMaxMinutes {
			minutes = netScanMaxMinutes
		}
		res, err := mod.ScanNetwork(scope, time.Duration(minutes)*time.Minute)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, res, "ok")
	}
}

// apiNetScanStop POST /api/netscan/stop {"modem_id":0}
//
// 立即停止正在跑的主动扫网：关掉 stop 通道 → 阻塞中的 AT 读立刻返回，
// 正在等待的 GET /api/netscan 会带着「已手动停止扫描」的说明返回（不是报错）。
// 没有扫网在跑时 stopped=false（前端据此可以不提示）。
func apiNetScanStop(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ModemID int `json:"modem_id"`
		}
		_ = httpx.DecodeBody(r, &req)
		mod, ok := mgr.modemByID(req.ModemID)
		if !ok {
			httpx.WriteJSON(w, -1, nil, "modem not found")
			return
		}
		httpx.WriteJSON(w, 0, map[string]any{"stopped": mod.AbortNetworkScan()}, "ok")
	}
}

// apiCellsAll GET /api/cells/all 返回所有模组最近一次基站扫描缓存（不触发扫描）
func apiCellsAll(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		modems := mgr.listModems()

		list := make([]*cell.CellScanResult, 0, len(modems))
		for _, mod := range modems {
			list = append(list, cachedScanResult(mod))
		}
		httpx.WriteJSON(w, 0, list, "ok")
	}
}

// cachedScanResult 用模组缓存的单元格构造扫描结果（按服务/邻小区分类）
func cachedScanResult(mod *Modem) *cell.CellScanResult {
	mod.mu.Lock()
	cells := append([]cell.CellInfo{}, mod.Cells...)
	ca := append([]CCInfo{}, mod.CAInfo...)
	at := mod.CellScanAt
	mod.mu.Unlock()
	res := &cell.CellScanResult{
		ModemID:    mod.ID,
		ScannedAt:  at,
		Cells:      cells,
		CA:         ca,
		CAActive:   CCsAggregated(ca),
		Serving:    []cell.CellInfo{},
		Neighbours: []cell.CellInfo{},
	}
	for _, c := range cells {
		if c.Serving {
			res.Serving = append(res.Serving, c)
		} else {
			res.Neighbours = append(res.Neighbours, c)
		}
	}
	res.Count = len(cells)
	res.ServingCount = len(res.Serving)
	res.NeighbourCnt = len(res.Neighbours)
	// 展锐 SIPC（F50）：扫描缓存里没有服务小区、而 URC 里有时把服务小区补上 —— 否则
	// 「基站扫描」页首屏（读 cache=1）永远是空的（本机没有扫描指令，缓存永远填不起来）。
	// ⚠️ 只补服务小区，**不写回缓存**（mod.Cells 不动），也不编邻区。
	if len(res.Serving) == 0 {
		res.Serving = append(res.Serving, mod.urcServingCells()...)
		res.ServingCount = len(res.Serving)
		res.Count = res.ServingCount + res.NeighbourCnt
	}
	// 缓存只保留小区列表，运营商名从服务小区还原，保证与实时扫描结果一致
	if len(res.Serving) > 0 {
		res.Operator = res.Serving[0].Operator
	}
	// CA 提示（查询指令/空状态说明）是「按机型」的，不是扫描结果本身，
	// 所以缓存路径也要带上：否则界面首屏读缓存时只能显示"该厂商未提供 CA 查询指令"。
	res.CACmds, res.CANote = mod.caHint()
	return res
}

// apiNetInfo GET /api/netinfo?modem_id=0 查询网络接入信息
func apiNetInfo(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		modemID, _ := strconv.Atoi(r.URL.Query().Get("modem_id"))
		mod, ok := mgr.modemByID(modemID)
		if !ok {
			httpx.WriteJSON(w, -1, nil, "modem not found")
			return
		}
		info, err := mod.QueryNetInfo()
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, info, "ok")
	}
}

// apiNetworkStatus GET /api/network/status?modem_id=0
//
// 汇总网络类型信息（制式/双工/PLMN/频段/频点）+ CS/PS 注册状态 + 服务小区详情。
// 只发只读指令，不触发小区扫描；服务小区取自上次扫描缓存。
func apiNetworkStatus(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		var st *NetworkStatus
		if r.URL.Query().Get("refresh") == "1" {
			st, err = mod.QueryNetworkStatus()
		} else {
			st, err = mod.networkStatusOrQuery()
		}
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		// 推给其他在线的客户端，多开页面时一起变
		pushNetworkState(mod, st)
		httpx.WriteJSON(w, 0, st, "ok")
	}
}

// ---- from: api_changes.go ----

// apiChanges GET /api/changes[?modem_id=0][&limit=200]
//
// 返回波动记录（最新在前）+ 各字段当前值。
// modem_id 省略/为负时返回全部模组的记录（多模组时前端自己筛选）。
// limit 默认 200，上限 1000（环形缓冲最多 500 条，所以超过 500 无意义）。
