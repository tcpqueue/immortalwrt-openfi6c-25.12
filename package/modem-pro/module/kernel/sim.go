package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"modemd/module/atparse"
	"modemd/module/cell"
	"modemd/module/common"
	"modemd/module/config"
	"modemd/module/sierra"
	"modemd/module/sms"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ---- from: modem_status.go ----

//
// 全靠 3GPP 标准指令，各家固件通用，也不影响当前业务：
//
//	AT+CCID     → +CCID: 89860324040255993152       SIM 卡 ICCID（换卡/查资费最常用）
//	AT+CIMI     → 460115089161379                   IMSI（前 5 位就是 MCC+MNC，可推出运营商）
//	AT+CPIN?    → +CPIN: READY                      SIM 卡状态（READY / SIM PIN / …）
//	AT+CNUM     → +CNUM: ,"",255                    本机号码（很多卡不写，空就空着）
//	        取不到时再试 电话本 "ON" 存储区 与 直读 EF_MSISDN（AT+CRSM），见 queryOwnNumber
//	AT+CPBS/?   → +CPBS: "SM",0,500                 电话本存储区（切 "ON" 就能读本机号码）
//	AT+CRSM     → +CRSM: 144,0,"986830…"           直接读卡上文件（选 EF_MSISDN = 0x6F40）
//	AT+QTEMP    → +QTEMP:"sdx-case-therm-usr","31"  温度传感器（实测 13 个，移远）
//	AT+CPMUTEMP → +CPMUTEMP: 25                     整机温度（实测芯讯通 SIM8200，只有一个值）
//	AT+CCLK?    → +CCLK: "26/09/29,04:31:55"        模组时钟（短信时间戳用的就是它）
//
// 温度是路由器/CPE 场景最容易出问题的地方（散热差 → 降频 → 掉速），
// 而模组时钟能解释"短信时间戳为什么和本地时间差 8 小时"。
// Sensor 一个温度传感器（实现在 parse_test.go）
// —— 别名见 glue.go；下面的解析函数也都在那里。
// ModemStatus SIM 卡与模组自身状态

type ModemStatus struct {
	ModemID   int       `json:"modem_id"`
	QueriedAt time.Time `json:"queried_at"`

	ICCID    string `json:"iccid,omitempty"`
	IMSI     string `json:"imsi,omitempty"`
	Operator string `json:"operator,omitempty"`  // 由 IMSI 前 5 位推出（与网络侧 PLMN 可互相印证）
	SIMState string `json:"sim_state,omitempty"` // AT+CPIN? 原文，如 READY / SIM PIN
	// SIMPresent 卡在不在（由 CPINPresent 判定：拿不到 +CPIN 行 = 未插卡，见 simhotswap.go）。
	// 与 SIMState 分开是因为状态文本五花八门（READY / SIM PIN / SIM PUK…），
	// 界面只需要一个“在不在”的布尔值。
	SIMPresent bool `json:"sim_present"`
	// Number 本机号码（MSISDN）。很多卡只把号码放在运营商侧、卡上不写，所以取三处：
	// AT+CNUM → 电话本的“本机号码”存储区（AT+CPBS="ON" + AT+CPBR）→ 直读卡上的 EF_MSISDN（AT+CRSM）。
	Number string `json:"number,omitempty"`
	// NumberSource 号码是哪儿读到的（便于核对；三处都没有则为空）
	NumberSource string `json:"number_source,omitempty"`

	Temps   []atparse.Sensor `json:"temps,omitempty"`    // 全部传感器
	TempMax *atparse.Sensor  `json:"temp_max,omitempty"` // 最高的那个
	TempKey []atparse.Sensor `json:"temp_key,omitempty"` // 机身 / 5G 基带 / CPU 三个关键点
	// TempNote 温度来源说明（界面直接显示）。目前只有展锐 SIPC（F50）会填：
	// 它的模组就是 SoC，温度走内核温度区（零 AT），见 temp_thermal.go。
	TempNote string `json:"temp_note,omitempty"`

	// SupplyVoltage 供电电压（AT+CBC）。只有声明了 VoltageQuerier 的厂商才会查，
	// 统一成 "3.858 V" 这种文本（各厂商上报格式不同，归一在 ParseCBC 里做）。
	SupplyVoltage string `json:"supply_voltage,omitempty"`

	ModuleTime string `json:"module_time,omitempty"`  // 模组原样返回，如 26/09/29,04:31:55
	ModuleUnix int64  `json:"module_unix,omitempty"`  // 换算成 unix 秒（模组一般按 UTC）
	ClockSkewS int64  `json:"clock_skew_s,omitempty"` // 模组时间 - 主机 UTC 时间（秒）

	// NetSelect 选网方式（AT+COPS?）：概览里要显示，**不是自动选网的要在界面上标红**
	// （用户 2026-10-08 要求）。手动选网模组**不会自动回退**，选错了会一直连不上网，
	// 所以在概览就得一眼看见，不能只藏在「高级配置 → 参数」里。
	NetSelect NetSelect `json:"net_select"`

	// Roam 数据漫游：当前是否在漫游 + 范围（国际 / 同国异网）。概览要显示（用户 2026-10-08 要求）。
	// ⚠️ 只读缓存、**不发 AT**（口径就一份，见 roaming.go 的 RoamState）。
	Roam RoamState `json:"roam"`
	// RoamAllowed [net] allow_roaming 的当前值（概览显示“允许 / 禁用”）。
	RoamAllowed bool `json:"roam_allowed"`
	// RoamApplies 本机型的「数据漫游」开关算不算数（厂商托管数据面的机型不算，见 RoamSupported）。
	RoamApplies bool `json:"roam_applies"`
}

const modemStatusCacheTTL = time.Minute

func cloneModemStatus(st *ModemStatus) *ModemStatus {
	if st == nil {
		return nil
	}
	copy := *st
	copy.Temps = append([]atparse.Sensor(nil), st.Temps...)
	copy.TempKey = append([]atparse.Sensor(nil), st.TempKey...)
	if st.TempMax != nil {
		temp := *st.TempMax
		copy.TempMax = &temp
	}
	return &copy
}

// ICCID / SIM 状态 / 本机号码 / 温度 / 时钟的**解析**都在 parse_test.go
// （ParseICCID / ParseIMSI / ParseCPIN / ParseCNUM / ParseCRSM / BCDDigits / DecodeEFMSISDN /
//  ParseQTEMP / ParseGTLADC / ParseGTSTemp / ParseCHIPTEMP / ParseCCLK …）；
// 它们都在同一个包里，直接调用。这里只留“发哪条指令 + 汇总”。

// queryICCID 读 ICCID：厂商声明了 ICCIDQuerier 就先用它的指令（鼎桥 MT5700 用 `AT^ICCID?`），
// 再退回 3GPP 标准 `AT+CCID`。两条都试是有意为之：本固件实测 `AT+CCID` 回 ERROR，
// 而支持标准写法的固件又不认 `AT^ICCID?` —— 反正各只有一条只读指令，试一遍成本很低。
func (mod *Modem) queryICCID() string {
	cmds := []string{"AT+CCID"}
	// 机型特例：本型号的 ICCID 指令（如 Air724UG / Thales / Telit 都是 `AT+ICCID`）
	if c := mod.Quirk().ICCIDCmd; c != "" {
		cmds = append([]string{c}, cmds...)
	}
	if iq, ok := mod.cmdImpl.(ICCIDQuerier); ok {
		if c := iq.CommandICCID(); c != "" && c != "AT+CCID" {
			cmds = append([]string{c}, cmds...)
		}
	}
	// 去重后依次试（都是只读指令，成本很低）
	seen := map[string]bool{}
	for _, c := range cmds {
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		if resp, err := mod.SendAT(c); err == nil && !common.RespHasError(resp) {
			if v := atparse.ParseICCID(resp); v != "" {
				return v
			}
		}
	}
	logger.Debug("query ICCID failed", zap.Int("modem", mod.ID))
	return ""
}

// ParseIMSI / ParseCPIN / ParseCNUM / ParseCPBRNumber / ParseCPBSStorage / ParseCRSM /
// BCDDigits / DecodeEFMSISDN / IMSIPLMN 均在 parse_test.go（同包，直接用）。

// queryOwnNumber 读本机号码（MSISDN）。三条路按“代价从低到高”试，都拿不到就返回空串：
//
//  1. AT+CNUM —— 3GPP 标准，最快；很多卡返回空
//  2. 电话本的“本机号码”存储区：AT+CPBS="ON" → AT+CPBR=1,<n>（读完恢复原存储区）
//  3. 直读卡上的 EF_MSISDN（AT+CRSM：先 SELECT 再 READ）
//
// ⚠️ 实测中国电信 5G 数据卡（ICCID 898603404…）三路都为空：AT+CNUM 回空、
// `AT+CPBS="ON"` 报 0/2 条、EF_MSISDN 读回 SW=0x9408 —— 号码只存在运营商侧，
// 卡上确实没写入。CRSM 文件级错误会记住，下一轮不再重复试。
func (mod *Modem) queryOwnNumber() (number, source string) {
	if resp, err := mod.SendAT("AT+CNUM"); err == nil && !common.RespHasError(resp) {
		if n := atparse.ParseCNUM(resp); n != "" {
			return n, "AT+CNUM"
		}
	}

	mod.mu.Lock()
	off := mod.ownNumCRSMOff
	mod.mu.Unlock()
	if off {
		return "", ""
	}

	// 电话本的“本机号码”存储区（EF_MSISDN 的电话本接口，部分模组只给这一条路）
	prev := ""
	if resp, err := mod.SendAT("AT+CPBS?"); err == nil {
		prev = atparse.ParseCPBSStorage(resp)
	}
	if resp, err := mod.SendAT(`AT+CPBS="ON"`); err == nil && !common.RespHasError(resp) {
		if resp2, err2 := mod.SendAT("AT+CPBR=1,2"); err2 == nil && !common.RespHasError(resp2) {
			if n := atparse.ParseCPBRNumber(resp2); n != "" {
				mod.restoreCPBS(prev)
				return n, "电话本 ON（EF_MSISDN）"
			}
		}
		mod.restoreCPBS(prev)
	}

	// 直读 EF_MSISDN：先 SELECT（带 ADF_USIM 路径）再 READ
	if resp, err := mod.SendAT(`AT+CRSM=192,` + strconv.Itoa(EFMSISDN) + `,0,0,15,"3F007FFF"`); err != nil {
		logger.Debug("crsm select msisdn failed", zap.Int("modem", mod.ID), zap.Error(err))
	} else if sw1, sw2, _, ok := atparse.ParseCRSM(resp); !ok || !atparse.CRSMOK(sw1, sw2) {
		if ok && atparse.CRSMFileUnavailable(sw1, sw2) {
			mod.disableOwnNumCRSM()
		}
	} else if resp2, err2 := mod.SendAT("AT+CRSM=176," + strconv.Itoa(EFMSISDN) + ",0,0,15"); err2 != nil {
		logger.Debug("crsm read msisdn failed", zap.Int("modem", mod.ID), zap.Error(err2))
	} else if sw1, sw2, data, ok2 := atparse.ParseCRSM(resp2); !ok2 || !atparse.CRSMOK(sw1, sw2) {
		if ok2 && atparse.CRSMFileUnavailable(sw1, sw2) {
			mod.disableOwnNumCRSM()
		}
	} else if n := atparse.DecodeEFMSISDN(data); n != "" {
		return n, "EF_MSISDN（AT+CRSM）"
	}
	return "", ""
}

// restoreCPBS 把电话本存储区恢复回原值（读“本机号码”时要临时切到 "ON"）
func (mod *Modem) restoreCPBS(prev string) {
	if prev == "" || prev == "ON" {
		return
	}
	if _, err := mod.SendAT(`AT+CPBS="` + prev + `"`); err != nil {
		logger.Debug("restore CPBS failed", zap.Int("modem", mod.ID), zap.String("want", prev))
	}
}

// disableOwnNumCRSM 记住“这张卡/这个固件读不了 EF_MSISDN”，不再每轮重复试 APDU
func (mod *Modem) disableOwnNumCRSM() {
	mod.mu.Lock()
	first := !mod.ownNumCRSMOff
	mod.ownNumCRSMOff = true
	mod.mu.Unlock()
	if first {
		logger.Info("sim EF_MSISDN unreadable, own-number fallback disabled", zap.Int("modem", mod.ID))
	}
}

// 温度解析（各厂商 6 种格式）全在 parse_test.go：
// ParseQTEMP / ParseQTEMPValues / ParseCPMUTEMP / ParseMTSM / ParseGTLADC /
// ParseGTSTemp / ParseCHIPTEMP / ParseTempResp / PickSummary / SortDesc。

// tempQueryCommand 温度查询指令：
//  1. 机型特例表的 tempCmd（如 FM650 的 AT+MTSM=1,6）—— 用于“同厂只有部分型号支持”；
//  2. 否则看厂商有没有声明 TempQuerier（移远 AT+QTEMP / 芯讯通 AT+CPMUTEMP）；
//  3. 都没有退回 AT+QTEMP（历史上唯一实现的写法）。
//
// ⚠️ 内部会取 mod.mu（机型特例），不要在已持锁的地方调。
func (mod *Modem) tempQueryCommand() string {
	if c := mod.Quirk().TempCmd; c != "" {
		return c
	}
	if tq, ok := mod.cmdImpl.(TempQuerier); ok {
		if c := tq.CommandTemp(); c != "" {
			return c
		}
	}
	return "AT+QTEMP"
}

// voltageCommand 供电电压指令：
//  1. 机型特例表的 voltageCmd（如 EM05 的 AT+CBC）—— 用于“同厂只有部分型号支持”；
//  2. 否则看厂商有没有声明 VoltageQuerier（如芯讯通）；
//  3. 都没有返回空串 → 上层连指令都不发，其它模组行为不变。
//
// 实现见 extra_netinfo.go（与其它可选能力一起）。

// QueryModemStatus 读一遍 SIM 卡与模组自身状态。
//
// 每项独立容错：某项不支持（如 CNUM 空、某些固件没有 QTEMP）只让该字段留空，
// 不影响其它项 —— 这组信息本来就是"能给多少给多少"。
func (mod *Modem) QueryModemStatus() *ModemStatus {
	st := &ModemStatus{ModemID: mod.ID, QueriedAt: time.Now()}

	if iccid := mod.queryICCID(); iccid != "" {
		st.ICCID = iccid
	}
	if resp, err := mod.SendAT("AT+CIMI"); err == nil && !common.RespHasError(resp) {
		st.IMSI = atparse.ParseIMSI(resp)
		if mcc, mnc := atparse.IMSIPLMN(st.IMSI); mcc != "" {
			st.Operator = cell.OperatorName(mcc, mnc)
		}
	} else {
		logger.Debug("query IMSI failed", zap.Int("modem", mod.ID))
	}
	if resp, err := mod.SendAT("AT+CPIN?"); err == nil && !common.RespHasError(resp) {
		st.SIMState = atparse.ParseCPIN(resp)
	}
	st.SIMPresent = CPINPresent(st.SIMState)
	// 选网方式（AT+COPS?）：概览要显示（不是自动选网就标红）—— 手动选网模组**不会自动回退**，
	// 选错了会一直连不上，用户得在概览上就能一眼看出来。
	if resp, err := mod.SendAT("AT+COPS?"); err == nil && !common.RespHasError(resp) {
		if ns, ok := ParseCOPS(resp); ok {
			st.NetSelect = ns
		}
	}
	// 数据漫游：**只读缓存、不发 AT**（概览要显示；RoamState 的判据只有一处，见 roaming.go）
	st.Roam = mod.RoamState()
	st.RoamAllowed = config.AllowRoaming()
	st.RoamApplies, _ = RoamSupported(mod)
	// 本机号码：AT+CNUM → 电话本 ON → EF_MSISDN（见 queryOwnNumber）
	st.Number, st.NumberSource = mod.queryOwnNumber()
	// 温度：展锐 SIPC（F50）的模组就是 SoC，固件**没有**任何温度 AT 指令，
	// 直接读内核温度区（零 AT，实测 lte / nr0 / nr1 / mm 四个基带传感器，见 temp_thermal.go）；
	// 其它机型（USB 模组）照旧走 AT —— 那种情况下 /sys/class/thermal 是路由器自己的温度，不能借用。
	if temps := mod.thermalTemps(); len(temps) > 0 {
		st.Temps = temps
		st.TempMax, st.TempKey = atparse.PickSummary(st.Temps)
		st.TempNote = thermalTempNote
		logger.Debug("temperature from thermal zones",
			zap.Int("modem", mod.ID), zap.Int("sensors", len(temps)))
	} else if resp, err := mod.SendAT(mod.tempQueryCommand()); err == nil && !common.RespHasError(resp) {
		st.Temps = atparse.ParseTempResp(resp)
		st.TempMax, st.TempKey = atparse.PickSummary(st.Temps)
	} else {
		logger.Debug("query temperature failed", zap.Int("modem", mod.ID))
	}
	// 供电电压：同样只问声明了能力的厂商（芯讯通 AT+CBC），其它模组连指令都不发
	if cmd := mod.voltageCommand(); cmd != "" {
		if resp, err := mod.SendAT(cmd); err == nil && !common.RespHasError(resp) {
			st.SupplyVoltage = sierra.ParseVoltage(resp)
		}
	}
	if resp, err := mod.SendAT("AT+CCLK?"); err == nil && !common.RespHasError(resp) {
		if raw, unix, skew, ok := atparse.ParseCCLK(resp, time.Now(), mod.Quirk().IgnoreCCLKTZ); ok {
			st.ModuleTime, st.ModuleUnix, st.ClockSkewS = raw, unix, skew
		}
	}
	return st
}

// IMSIPLMN 见 parse_test.go（IMSItoPLMN，按 ITU 的 3 位 MNC 名单区分）。

// ---- from: modstat/clock.go ----

// +CCLK: "26/09/29,04:31:55"（秒后有可选的时区，如 +32 表示 8 小时步长）

// ---- from: modstat/sim.go ----

// Package modstat 是「SIM 卡 / 模组自身状态」的纯解析：ICCID、IMSI、卡状态、
// 本机号码（MSISDN）、温度传感器、模组时钟。
//
// 为什么放一起：这几项都是"读卡与读模组自身"的应答解析，彼此没有依赖，
// 共用一份正则与字节级解码（BCD / EF_MSISDN）；发指令与状态汇总留在
// kernel（见 sim.go），因为要碰串口与 *Modem。

var (
// ICCID：3GPP 是 `+CCID`，但**多家固件只回 `+ICCID`**（QModem 的 openluat/thales/telit
// 脚本就用 AT+ICCID，见 vendor_at_extra.go）⇒ 两种拼写都要认。
// +CPBS: "SM",0,500（电话本当前存储区；"ON" = 本机号码）
// +CPBR: 1,"+8613800138000",129,""（电话本条目：位置,号码,类型,文本）
// +CRSM: 144,0,"986830..."（sw1,sw2[,应答数据]）
)

// EFMSISDN SIM/USIM 里存本机号码的透明文件 ID（0x6F40）
const EFMSISDN = 0x6F40

// ---- from: modstat/temp.go ----

//
// 各家固件的温度指令与格式都不一样（实测全部互不兼容）：
//
//	移远 5G     AT+QTEMP    → +QTEMP:"sdx-case-therm-usr","31"      13 路，带名字
//	移远 EM05   AT+QTEMP    → +QTEMP: 30,26,25                       不带名字，只有值
//	芯讯通      AT+CPMUTEMP → +CPMUTEMP: 25                          整机一个值
//	广和通 FM650 AT+MTSM=1,6 → +MTSM: 60                             整机一个值
//	广和通 FM160 AT+GTLADC  → in_temp_*: 30562 / [zone42]:name 31800  1/1000 ℃
//	广和通 FM350 AT+GTSENRDTEMP=0 → +GTSENRDTEMP: 1,31773            1/1000 ℃，23 路
//	鼎桥 MT5700 AT^CHIPTEMP? → ^CHIPTEMP: 341,338,…                  0.1 ℃，12 路
// Sensor 一个温度传感器

var (
// +CPMUTEMP: 25（芯讯通：整机温度，℃；实测 SIM8200EA-M2）
// +MTSM: 60（广和通 FM650：整机温度，℃；实测只有 AT+MTSM=1,<type> 这一种写法）
// 广和通 AT+GTLADC：`in_temp_pmx65_ambient_therm_input: 30562` / `[zone42]:modem-beamer-usr -40960`
// 广和通 +GTSENRDTEMP / +GTZONERDMAXTEMP：`+GTSENRDTEMP: 1,31773`
// 鼎桥 AT^CHIPTEMP?：`^CHIPTEMP: 341,338,…`
)

// ---- from: ussd/ussd.go ----

// Package ussd 是 USSD（查余额 / 查套餐这类交互）的纯逻辑部分：
// 解析 +CUSD 应答、状态码文案、下发码的合法性校验。
//
// USSD 与普通 AT 查询最大的区别：**应答不是同步返回的**。
//
//	发：AT+CUSD=1,"*100#",15 → 立刻回 OK（只是"已下发"）
//	收：+CUSD: 0,"您的余额为 10.00 元",72   ← 网络侧算完才作为 URC 异步到达
//
// 所以接收要走 URC 通道（读协程捕获 +CUSD → 存进 Modem.USSD → WebSocket 推送），
// 那部分要碰 Modem/串口，留在 kernel（见 glue.go）。
//
// 编码：DCS 表明是 UCS2 时，模组在文本模式下会把中文以"UTF-16BE 十六进制串"
// 发过来（与短信一模一样），复用 sms.DecodeSMSText 还原。
// Result 一次 USSD 应答

type Result struct {
	At     time.Time `json:"at"`
	Status int       `json:"status"`        // 0 无需后续操作 / 1 需要继续 / 2 会话被网络结束 / 4 不支持 / 5 超时
	Text   string    `json:"text"`          // 已尽力解码的可读文本
	Raw    string    `json:"raw,omitempty"` // 原始报文，便于核对编码问题
	// SPError 是**紧跟在这次 USSD 前面**的展锐 `+SPERROR` 上报（如 `+SPERROR: 5,41,"46011"`）。
	// 实测（中兴 F50 / 展锐 UMS9620 + 电信数据卡）：USSD 被网络拒的序列就是
	// `+SPERROR: 5,41,"46011"` → `+CUSD: 2`（会话被网络结束，**一个字符都没有**）。
	// 只显示"会话被网络结束"用户看不懂，把它带上才能看出是网络侧拒的。
	SPError string `json:"sperror,omitempty"`
}

// SPErrorFreshWindow 哪个窗口内的 `+SPERROR` 算"这次 USSD 的线索"。
// 展锐固件是先报 +SPERROR、紧接着才报 +CUSD，所以窗口给得宽容一点。
const SPErrorFreshWindow = 60 * time.Second

// AttachSPError 把"刚发生的" +SPERROR 记进 USSD 结果（纯函数，便于测试）。
// 返回是否真的附上了（nil 结果 / 空行 / 时间窗过期都算不附）。
func AttachSPError(res *Result, line string, at, now time.Time) bool {
	line = strings.TrimSpace(line)
	if res == nil || line == "" || at.IsZero() {
		return false
	}
	if now.Sub(at) > SPErrorFreshWindow {
		return false
	}
	res.SPError = line
	return true
}

var (
	// +CUSD: 0,"text",72 / +CUSD: 2 / +CUSD: 1,"请选择：1 ...",72
	reCUSD = regexp.MustCompile(`\+CUSD:\s*(-?\d+)\s*(?:,\s*"([^"]*)"\s*(?:,\s*(-?\d+))?)?`)
	// USSD 码允许的字符：数字、* # +（如 *#100#、*123*1#）
)

// ParseCUSD 解析一条 +CUSD 应答行。
//
// 文本解码策略：DCS=72(0x48) 或文本看起来像 UTF-16BE 十六进制时调用
// DecodeSMSText（它自带"是否像真实文本"的判据，纯数字串不会被误解码）；
// ASCII 应答（英文/数字）原样返回。
func ParseCUSD(line string, now time.Time) (Result, bool) {
	m := reCUSD.FindStringSubmatch(line)
	if m == nil {
		return Result{}, false
	}
	status, _ := strconv.Atoi(m[1])
	text := m[2]
	dcs := 0
	if m[3] != "" {
		dcs, _ = strconv.Atoi(m[3])
	}
	res := Result{At: now, Status: status, Raw: strings.TrimSpace(line)}
	if text != "" {
		// DCS 0x48（含 bit6/7 为 1）表示 UCS2；其余情况交给 DecodeSMSText 的判据决定
		if dcs == 72 || dcs == 0x48 || (dcs&0x0C) == 0x08 {
			res.Text = sms.DecodeSMSText(text)
		} else {
			res.Text = text
		}
	}
	return res, true
}
