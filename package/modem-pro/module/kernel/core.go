package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"modemd/module/atparse"
	"modemd/module/bands"
	"modemd/module/cell"
	"modemd/module/dial"
	"modemd/module/modedialect"
	"modemd/module/netdev"
	"modemd/module/sms"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
)

// ---- from: modem.go ----

// SignalInfo 信号信息（含 5G NR 小区信号）

type SignalInfo struct {
	RSSI   int `json:"rssi"`
	BER    int `json:"ber"`
	NRRSRP int `json:"nr_rsrp,omitempty"`
	NRRSRQ int `json:"nr_rsrq,omitempty"`
	NRSINR int `json:"nr_sinr,omitempty"`
}

// SMSJob 短信发送任务
type SMSJob struct {
	Phone    string
	Content  string
	RespChan chan error
}

// Modem 单个 5G 模组实例
type Modem struct {
	ID      int         `json:"id"`
	TTYList []string    `json:"tty_list"`
	ATPort  string      `json:"at_port"`
	Vendor  ModemVendor `json:"vendor"`
	// ATPortManual 这台模组的 AT 命令口**是用户手动指定的**（界面下拉里标"手动"）
	// —— 只用于展示（在 /api/modems 里算好填上），不影响探测逻辑。
	ATPortManual bool `json:"at_port_manual,omitempty"`
	// VendorName 厂商可读名（"移远 Quectel" / "鼎桥 TD Tech"…），界面「厂商」一栏用它；
	// 以前界面直接把 vendor 标识拄上去，鼎桥会显示成 "mt5700"（那是型号不是厂商）。
	VendorName string `json:"vendor_name,omitempty"`
	// Model 是 ATI **原始**应答（机型特例匹配、排查问题用）；
	// ModelName 是从里面解析出的**型号短名**，对外（界面）就显示这个。
	Model      string     `json:"model_raw,omitempty"`
	ModelName  string     `json:"model,omitempty"`
	IMEI       string     `json:"imei,omitempty"`     // 模组 IMEI（AT+CGSN）
	Firmware   string     `json:"firmware,omitempty"` // 固件版本（AT+CGMR）
	SignalInfo SignalInfo `json:"signal"`
	SMSList    []sms.SMS  `json:"sms_list"`
	// SMSTotal/SMSUnread 短信总数与**未读**数（悬浮小窗的角标用）。
	// 只统计条数、**不含任何正文**，所以不过短信密码闸门（见 pushSMSCount）。
	// 未读 = 最近一次读到的未读条数（ReadSMS 重置）+ 之后新到并上报 +CMTI 的条数。
	SMSTotal  int `json:"sms_total"`
	SMSUnread int `json:"sms_unread"`
	// 最近一次基站扫描缓存（ScanCells 成功后写入）
	Cells      []cell.CellInfo `json:"cells,omitempty"`
	CellScanAt time.Time       `json:"cell_scan_at,omitempty"`
	CAInfo     []CCInfo        `json:"ca_info,omitempty"` // 载波聚合分量载波（与服务小区同一缓存）

	// SIM 卡与模组自身状态（ICCID / 温度 / 模组时钟），由 QueryModemStatus 写入
	ModemStatus *ModemStatus `json:"modem_status,omitempty"`
	// 最近一次 USSD 应答（+CUSD 是 URC，异步到达，由读协程写入；见 ussd.go）
	USSD *Result `json:"ussd,omitempty"`
	// 最近一条展锐 `+SPERROR` 上报（CS 类业务失败时固件会报，如 USSD 被网络拒绝）。
	// ⚠️ 只在 USSD 下发后的一小段时间内当作"这次会话的线索"用，见 recordUSSD。
	lastSPError   string
	lastSPErrorAt time.Time

	serialPort     netdev.AtDev
	readChan       chan string
	writeChan      chan string
	closeChan      chan struct{}
	mu             sync.Mutex
	networkQueryMu sync.Mutex
	networkStatus  *NetworkStatus
	paramsQueryMu  sync.Mutex
	paramsStatus   *Status
	usbNetQueryMu  sync.Mutex
	usbNetStatus   *CFUNStatus
	usbNetStatusAt time.Time
	// 组网模式偏好的"设备枚举"结果（`AT+XXX=?` 只问一次，失败也不再问）：
	// 已取证的码表不够用时（如 Foxconn 只知 10），用设备自报的取值把下拉补全
	modeEnumMu   sync.Mutex
	modeEnumDone bool
	modeEnum     []string
	// 组网模式方言的"挨个问"结果（见 vendor_settings_engine.go 的 probeModeDialect）：
	// 厂商标识认不出来、或推出来的那条指令被模组拒了时，挨个试各家的查询指令，
	// 命中/全不认都缓存 modeProbeTTL —— 一组探测要发十几条指令，不能每次读都来一遍。
	modeProbeMu    sync.Mutex
	modeProbeAt    time.Time
	modeProbeD     *modedialect.Dialect // nil = 试过一圈都不认
	modeProbeTried []string             // 试过的查询指令（报错时列给用户看，排查用）
	// airplaneOn 当前是不是飞行模式（AT+CFUN=4）。
	// 用 atomic 而不是 mod.mu：它会被"自动组网循环"和"状态查询"两条链同时读写，
	// 拿普通锁容易和 AT 发送路径绕进去。由 QueryFuncMode 顺带刷新，见 net.go。
	airplaneOn atomic.Bool
	// cfunLast 上一次读到/设置过的功能模式（AT+CFUN 的值：1=全功能 4=飞行）。
	//
	// ⚠️⚠️ 为什么要有这个缓存 —— 用户 2026-10-13 实测：在 FM160-CN 上**发 `AT+CFUN?`
	// 会把模块搞掉线**（USB 掉、之后什么都查不到）。而"查飞行模式状态"这件事原先被放在
	// **每轮状态刷新**里（net.go 的 QueryNetStatus）⇒ 等于周期性把模组打掉线。
	// 所以自动/周期性路径**一律不发 AT+CFUN?**，只读这个缓存；
	// 缓存由 SetAirplane（用户开关）与 QueryFuncMode（只在用户手动点开时才走）刷新。
	cfunLast      atomic.Int32
	statusQueryMu sync.Mutex
	lockQueryMu   sync.Mutex
	lockStatus    *bands.LockStatus
	// 锁网方言：运行期只读探测一次后缓存（见 lock.go 的 lockerFor）；
	// 换厂商/重新识别后要清（见 modem.go detectVendor）
	lockProbed bool
	lockChosen NetLocker
	// lockFamily 命中的方言技术名（qnw/mtk/simcom/balong/gosuncn/telit/thales/foxconn/neoway/atband）；
	// 空串 = 探测没命中（走了机型猜测的回退）
	lockFamily string
	// lockTried 探测记录（“方言名:接受/拒绝”），现场排查时看的就是它
	lockTried []string
	atMu      sync.Mutex // 串行化 AT 指令：保证同一时刻只有一条指令在途
	// smsMu 串行化**整段“读短信”序列**（CMGF 切换 + CMGL + 恢复模式）。
	// ⚠️ atMu 只保证单条指令不撞车：读短信是多条指令的序列，另一次读从中间插进来
	// （屏幕连上读 + 开机读 + 新短信 URC 读）会让 CMGF 与 CMGL 的模式对不上 ——
	// 实测现象是 `AT+CMGL=4` 超时、甚至把空列表当成“卡里没短信”缓存下来。
	smsMu sync.Mutex
	// smsReadAt 上一次**成功**读到短信的时间（屏幕重连时用它去抖，见 refreshSMSCache）
	smsReadAt    time.Time
	lastReadTime time.Time // 最后收到数据的时间（统计用）
	lastReadTick time.Time // 读循环心跳：每次读迭代更新（判断读是否卡死）

	// ---- 厂商 URC 日志跟随（展锐 SIPC：/run/mu300-at/urc/stty_nr0.log，见 urclog.go）----
	// parseMu 串行化 parseLine：它现在有两个调用方（串口读循环、URC 跟随协程），
	// 而 parseLine 里有**不带锁**的小状态机（如 GTCCINFO 的 gtccRat）。
	parseMu sync.Mutex
	// urcFeedOK 上一轮跟随 URC 日志是否成功（文件在、读得到）。
	// 只有它为真时才敢拿 URC 的数据去替掉 AT 轮询。
	urcFeedOK bool
	// urcSawSignal / urcSawNet URC 里是否出现过信号类 / 注册 PDP 类上报。
	// 用途：判断“这类数据以后能不能靠 URC 拿”（见 urcCoversSignal / urcCoversNetwork）。
	urcSawSignal bool
	urcSawNet    bool
	// urcLastLine 最后收到 URC 行的时刻；urcFeedErr 跟随失败的原因（排查用）。
	urcLastLine time.Time
	urcFeedErr  string
	// urcSignalAt 最后一条**信号类**上报的时刻。
	// 用途：判断 URC 是不是“跟丢了”（daemon 停了/跟随器卡了）——
	// 跟丢了就别再拿 URC 当权威，自动退回 AT 轮询（见 urcSignalStaleLimit）。
	urcSignalAt time.Time
	// urcOp URC 里解析出来的运营商（`+SPNWNAME`：PLMN + 名字，名字是 UCS2 hex）。
	// 这台机器的 QNWINFO 不支持（回 +CME ERROR: 4），所以它是**唯一**在线来源。
	urcOp    urcOpSnapshot
	urcSawOp bool
	// urcPCI / urcSawPCI：URC 里最后的 `+SPPCI`（服务小区 PCI，固件格式 `%08X`）。
	// 频段/频点 URC 里没有，但 PCI + `+CxREG` 的 CI/TAC + CESQ 的信号
	// 已经够拼出一个“部分但真实”的服务小区（见 servingCellFromURC）。
	urcPCI    int
	urcSawPCI bool
	// urcBand / urcSawBand：URC 里最后的 `+SPCLB`（**文件里唯一**的频段来源）。
	// ⚠️ 只按原值用（`BAND <n>`）：分不清它是 LTE 频段还是 NR 频段，不猜前缀。
	urcBand    int
	urcSawBand bool
	// urcNet URC 里解析出来的各域注册状态（CS/PS/EPS/5GS）。
	// 用途：直接顶掉 AT+CREG?/CGREG?/CEREG? 那一组轮询（见 net.go 的 shouldFullQueryNetwork）。
	urcNet urcNetSnapshot
	// netFullAt / 完整查询相关字段已废弃：展锐 SIPC 上**不再做任何后台 AT 补查**
	//（接口只读文件），其它模组每次照旧真查（见 net.go 的 shouldFullQueryNetwork）。
	// signalPolledAt 上一次真去总线问 `AT+CSQ` 的时刻（URC 供信号时的兜底节拍）。
	signalPolledAt time.Time
	isRunning      bool
	smsSendQueue   chan SMSJob
	cmdImpl        ModemCommand // 绑定的厂商指令实现
	// probedCmds 运行期探测出的厂商指令（同一功能多套写法时实际用哪条），
	// key 见 KindServing / KindNeighbour，见 modem_at.go resolveVendorCmd
	probedCmds map[string]string
	// 流量统计：上一次采样、最近一次算出的速率、使能指令是否已下发过
	trafficPrev  *TrafficSample
	trafficRate  Rate
	trafficReady bool
	// 速率式模组（如广和通 AT+GTSTATIS? 只报瞬时速率、没有累计计数器）的
	// 本地积分量：界面上的"累计上行/下行"与流量配额计量用它（Estimated=true）
	trafficIntegUp   uint64
	trafficIntegDown uint64
	// 签约速率（AMBR）缓存：只在 PDU 会话重建时才会变，按 TTL 复用（见 ambr.go）
	ambrCache   *atparse.AMBRRate
	ambrCacheAt time.Time
	// ambrFallbackOff = 该固件不认 AT+CGCONTRDP（回 ERROR）→ 停用备用取值路径，
	// 免得每分钟白发一条无效指令
	ambrFallbackOff bool
	// ownNumCRSMOff = 该卡/固件读不了 EF_MSISDN（AT+CRSM 报 0x94xx/0x6Axx）→
	// 停用本机号码的 CRSM 回退（见 modem_status.go queryOwnNumber）
	ownNumCRSMOff bool

	// 拨号上网（见 dial.go / dial_engine.go）：
	//   dialMu    拨号状态与 USB 类型缓存的锁
	//   dialBusy  同时只允许一个拨号流程（拨号连发几条 AT，并发会互相干扰）
	//   dialState 当前拨号状态（页面「拨号上网」显示它，WS 推送类型 dial）
	//   dialType  最近一次识别出的 USB 类型 + 识别时间（TTL 缓存，见 USBType）
	dialMu     sync.Mutex
	dialBusy   bool
	ndisMu     sync.Mutex // 串行化 MT5700M-CN 的 NDIS 数据会话操作
	dialState  dial.DialState
	dialType   *DialTypeInfo
	dialTypeAt time.Time

	// 广和通 FM650（展锐平台）AT+GTCCINFO? 的解析状态：应答是“标签行 + 数据行”，
	// 数据行（`1,9,460,11,...`）本身认不出是哪家的小区，必须先记住上一条标签行。
	// 只在读协程（parseLine）里读写，不参与加锁。
	gtccRat     string // 当前标签的制式（NR / LTE / LTE-NR ...）
	gtccServing bool   // 标签是服务小区还是邻区
	gtccRow     int    // 同一标签下第几条数据行（EN-DC 第二行才是 NR）

	// SIM 热插拔（见 simhotswap.go）：上一次读到的卡身份 + 轮询节流 + 动作冷却。
	// ⚠️ 这四个字段由 SIMWatchLoop / parseLine / API 共同访问，统一走 mod.mu。
	simIdent         SIMIdentity
	simIdentKnown    bool      // 至少成功读到过一次确定答复（之前才敢谈"变化"）
	simCheckedAt     time.Time // 上次真去读 SIM 的时刻（轮询节流用）
	simDirty         bool      // 收到过 `+CPIN:` 变化 ⇒ 立刻核实，不等周期
	simCooldownUntil time.Time // 换卡动作的冷却截止时刻
	// simLastEvent / simLastEventAt 最近一次热插拔事件（给 /api/sim 与界面看）
	simLastEvent   string
	simLastEventAt time.Time

	// echoSeen 固件是否会回显 AT 指令（第一次收自己指令的回显时置位）。
	// 置位后 readResponseLocked 会用"先看到本指令回显"来隔开上一条指令的残留应答。
	// ⚠️ 只在持 atMu 时访问（SendAT / readResponseLocked），所以不加锁。
	echoSeen bool

	// 主动扫网「立即停止」（见 netscan.go）：ScanNetwork 开始时建一个 stop 通道并登记，
	// 停止接口（POST /api/netscan/stop）关闭它 → 阻塞中的 AT 读立刻返回 errATStopped。
	// ⚠️ 用独立的 scanMu：扫网期间 atMu 一直被占着（可能就是几分钟），
	// 停止请求不能先去等 atMu，否则"立即停止"根本停不下来。
	scanMu   sync.Mutex
	scanStop chan struct{}

	// ---- 屏幕端（tftdisplay）的组网模式偏好缓存，见 displaymode.go ----
	// 读一次组网模式要占串口（F50 那种总线上一条 10 秒），所以带 TTL：
	// 屏幕重连/频繁切页不会把串口刷满（与 web 端 MODE_CAPS_TTL_MS 同一个坑）。
	// displayModeBusy 单飞：同一时刻只允许一次真查询（防止并发 push 重复占串口）。
	displayModeMu   sync.Mutex
	displayMode     *DisplayModeInfo
	displayModeAt   time.Time
	displayModeBusy bool
}

// nonATRecord 一个被判"不是 AT 口"的串口的重探计划。
type nonATRecord struct {
	// Next 到这个时刻之后才允许再探测（指数退避，避免每轮都白等一次探测超时）。
	Next time.Time
	// Tries 连续被判否定的次数（退避用；探测成功或拓扑变化时清零）。
	Tries int
}

// ModemManager 模组管理器，管理多个 5G 模块
type ModemManager struct {
	modems map[int]*Modem
	mu     sync.Mutex

	// discMu 发现流程的**单飞锁**：同一时刻只允许一轮发现。
	// 周期巡检 / 手动重探 / 启动发现可能并发触发，串行化后：
	//  ① 发现状态字段（否定缓存、救援计数…）只有一个写者；
	//  ② 不会为同一个坏端口反复起探测 goroutine。
	// ⚠️ 与 mu 分开：mu 只保护 modems 与发现状态字段的读写（临界区要短）；
	// discMu 覆盖整轮发现（含逐口 AT 探测那种可达数秒的慢操作）。
	discMu sync.Mutex
	// discBusy 是否已有一轮发现排队/在跑（供异步触发去重，见 DiscoverAndStartAsync）。
	discBusy atomic.Bool
	// snap 模组列表的**无锁读快照**（不可变副本，按 ID 排序）。
	// 读路径（HTTP / 推送循环）直接读它，不碰 mu —— 否则发现流程里那段"逐个串口 AT 探测"
	// （每个口可达数秒）会把读请求全堵住，表现为"扫描到 5G 模块时整个页面卡住"。
	// 写路径在持 mu 时重建并 Store（见 publishModemsLocked）。
	snap atomic.Pointer[[]*Modem]

	// nonATPorts 记录"被判为不是 AT 控制口"的串口及其**重探计划**。
	// 探测一个口需要发送 AT 并等待应答（本机实测约 4s），
	// 稳定拓扑下每轮重复探测纯属浪费，因此把否定结果缓存下来。
	// ⚠️ 但**不能永久写死**：慢启动的模组（第二台 5G 很常见：模组内部 Linux 要 1~2 分钟
	// 才把 AT 拉起来）第一轮探测会超时；若永久缓存，只要场上已经有另一台模组
	// （knownModems>0），就再也不会重探 → **永远找不到它**（用户实测的"5G 模块出不来"）。
	// 所以改成按端口指数退避重探（见 nonATBackoff）。
	// 端口拓扑发生变化（增删串口）时整体作废重探，避免漏掉热插拔。
	nonATPorts map[string]nonATRecord
	// nonATAt 上次写入否定结果的时刻。
	// ⚠️ 一台模组都没认出来时，否定缓存要定期作废重探 —— 实测重启后 modemd 比模组先就绪：
	// 那时 option 刚被 new_id 绑上、AT 口还没应答，第一轮探测全失败就被缓存成"非 AT 口"，
	// 之后每轮都跳过，**永远认不出这台模组**（重启 modemd 才好）。
	nonATAt time.Time
	// noATSince 从什么时候开始"一个 AT 口都找不到"（用于判断模组是不是卡死了）
	noATSince time.Time
	// lastUSBRescue 上次做 USB 软重插的时刻（冷却用，避免变成重启循环）
	lastUSBRescue time.Time
	// usbRescueCount 本次"卡死周期"里已经软重插了几次（AT 口回来后清零）
	usbRescueCount int
	// usbRescueGaveUp 救到上限后只告警一次（别每轮扫描都刷屏）
	usbRescueGaveUp bool
	// noModemSince / noModemWarnAt：“有串口却一个模组都认不出来”的起始时刻 / 上次告警时刻。
	// 用于在 AT 口长时间不答时给一条**带处置建议**的 WARN（模组还在启动 vs 固件卡死，
	// 用户从零散的 "AT command timeout" 里看不出来）。
	noModemSince  time.Time
	noModemWarnAt time.Time
	// portFingerprint 当前串口集合的指纹，用于判断拓扑是否变化
	portFingerprint string
	// topoSig 当前模组拓扑指纹（id + AT 口），用于只在拓扑变化时推送列表
	topoSig string
}

func NewModemManager() *ModemManager {
	return &ModemManager{
		modems:     make(map[int]*Modem),
		nonATPorts: make(map[string]nonATRecord),
	}
}

// AT 应答结束标记
// atEndMarks 已废弃：改由 isATEndLine 做**整行**判定（见 modem_at.go）。
// 用 Contains 匹配 "OK" 会被短信正文里的 OK 骗到，把长应答提前截断。

var reCSQ = regexp.MustCompile(`\+CSQ:\s*(\d+),(\d+)`)
var reCMTI = regexp.MustCompile(`\+CMTI:`)
