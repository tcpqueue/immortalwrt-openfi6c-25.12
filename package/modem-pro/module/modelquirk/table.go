// Package modelquirk 机型特例表：**同一指令在不同机型上语义不同**时，在这里按型号子串
// 声明差异（时钟时区后缀、电压/温度指令、PDP 超时、短信走 PDU…）。
//
// 为什么单独成包：这张表是机型知识，增长最快、最常改，而它被内核的解析层通过
// `Modem.Quirk()` 读。放在内核里意味着「加一个机型要动内核」；放在这里则
// 「加一个机型 = 只改此文件」，与模块化目标一致。
//
// 表里每一条都必须带**实测依据**（固件版本 + 原始应答），没有证据的不要臆造 ——
// atcmd_invariants_test.go 的 TestQuirkTableInvariants 会钉住这个约定。
package modelquirk

// 本文件由 extract-decls.py 从 kernel 抽出（按名字表 + 括号配平），
// 内容逐字保留，仅把需要跨包使用的名字首字母大写。

import (
	"strings"
	"time"
)

type Quirk struct {
	// Match 型号子串（大小写不敏感）
	Match string
	// Note 为什么要有这条特例（写实测现象，方便后人核对）
	Note string
	// IgnoreCCLKTZ AT+CCLK? 的时区后缀不可信：带了后缀但报的值本身就是 UTC。
	IgnoreCCLKTZ bool
	// VoltageCmd 供电电压指令（空串 = 沿用厂商默认，即没声明就不查）。
	// 用于「同一厂商只有部分型号支持」的情况（如 EM05 支持 AT+CBC，而上层
	// 不敢给整个 Quectel 家族都开）。
	VoltageCmd string
	// TempCmd 模块温度指令（空串 = 沿用厂商默认 / 兜底 AT+QTEMP）。
	// 同样是「同厂只有部分型号支持」的差异：广和通老机型不认 AT+MTSM。
	TempCmd string
	// PDPAddrCmd 本机 IPv4/IPv6 查询指令（空串 = 3GPP 标准写法 AT+CGPADDR）。
	// 用于「固件不认不带 cid 的标准写法」的型号（实测 FM350-GL 直接回 ERROR）。
	// 只是把它插到候选列表最前面，失败仍会退回 AT+CGPADDR，所以别的机型不受影响。
	PDPAddrCmd string
	// ICCIDCmd 本机 ICCID 查询指令（空串 = 标准写法 AT+CCID）。
	// 用于「标准写法不被认」的厂商（证据：QModem `cmds/openluat.sh`、`cmds/thales.sh`
	// 都是 `AT+ICCID`；`cmds/telit.sh` 的 `cmd_iccid` 同样是 `AT+ICCID`）。
	ICCIDCmd string
	// AMBRCmd 本机签约速率（AMBR）查询指令（空串 = 用厂商声明的 AMBRQuerier）。
	// 用于「厂商没有专门实现、但 QModem 脚本里取证了」的机型：
	// 美格 SRM 系列用 `AT^DSAMBR=<cid>`（`vendor/meig.sh` 的 cmd_dsambr）。
	AMBRCmd string
	// ServingCellCmds / NeighbourCellCmds 本机服务小区 / 邻区查询指令（可多选，按顺序试）。
	// 插到候选列表**最前面**（失败仍退回原候选，所以别的机型不受影响）。
	// 证据：
	//   · QModem `cmds/openluat.sh` —— `AT+CCED=0,1` / `AT+CCED=0,2`
	//   · `cmds/thales.sh` §15.33 `AT^DEBUG`、`cmds/foxconn.sh` 同形（前缀 `AT!` 或 `AT^`，两种都试）
	//   · `cmds/sierra.sh` `AT!GSTATUS?`；`cmds/meig.sh` `AT^CELLINFO=<cid>`
	//   · `cmds/gosuncn.sh`（lte 平台）`AT+ZCELLINFO?`；`cmds/telit.sh` `AT#CAINFOEXT?`
	// 解析在 vendor_cell_extra.go。
	ServingCellCmds   []string
	NeighbourCellCmds []string
	// SMSReadPDU 读短信必须走 PDU 模式（AT+CMGF=0 + AT+CMGL=4）并自行解码。
	// 用于「文本模式回的“正文”其实是十六进制 PDU」的型号（实测 FM350-GL）：
	// 那种应答按 UCS2 文本去解，正文开头会多出 UDH 解出来的怪字符，看着就是“读取不正常”。
	SMSReadPDU bool
	// PDPTimeout PDP 相关指令（AT+CGDCONT? / AT+CGACT? / AT+CGACT=）的专用等待超时
	// （0 = 用配置的 cmd_timeout）。实测 FM650-CN 的 AT 通道会"错位/变慢"：这些指令
	// 常要 5~20s 才回，默认 3s 会先超时，把好好的操作判成失败。
	PDPTimeout time.Duration
	// NoReplyOnPDPActivate AT+CGACT 可能**不给最终结果**（既不是 OK 也不是 ERROR），
	// 超时不能当成失败 —— 改由回读 AT+CGACT? 判定（实测 FM650-CN）。
	NoReplyOnPDPActivate bool
}

// 同一个厂商的不同平台/固件经常「命令一样、语义不一样」，甚至同一型号不同固件版本
// 也不一致。与其把这些差异散在解析代码里 if/else，不如集中登记在这里：
//
//	· 每条都写清「为什么」+「实测依据」，后面适配新模组就往这张表里加一行；
//	· 匹配的是模组型号串（ATI / AT+CGMM 的原始应答），大小写不敏感的子串匹配；
//	· 只放「解析语义」的差异。指令本身的支持差异走 vendorcmd.go 的候选探测，
//	  不要混进来。
//
// 新增模组的推荐流程（详见 README「适配新模组」）：
//  1. 用「AT 指令」页把型号/能力跑一遍，看「高级配置 → 接口调试」里
//     vendor command resolved 的实际取值；
//  2. 能靠候选探测解决的（换一条指令）→ 加/改用 vendorcmd.go 的 Candidate 列表；
//  3. 靠候选解决不了的（同一指令语义不同，例如时钟时区后缀）→ 在这里加一条 Quirk，
//     并补一个单元测试固定住实测样例。
var Table = []Quirk{
	{
		Match:        "RM500U",
		IgnoreCCLKTZ: true,
		Note: "RM500U-CNV 实测 `AT+CCLK?` 回 `26/09/29,10:03:23+32`，但 10:03 就是 UTC" +
			"（主机本地 18:03）；按 3GPP 的写法减掉 +32（8 小时）会净差 8 小时。",
	},
	{
		Match:        "RM520",
		IgnoreCCLKTZ: true,
		VoltageCmd:   "AT+CBC",
		Note: "RM520N-GL（固件 RM520NGLAAR01A07M4G）实测两件事：" +
			"① `AT+CCLK?` 回 `26/09/29,13:49:23+32`，但 13:49 就是 UTC（主机本地 21:49）" +
			"—— 与 RM500U / EM05 / FM650 同一个毛病（clock_skew 会显示 -28801s），不能减这 8 小时；" +
			"② `AT+CBC` 回 `+CBC: 0,71,3861`（<bcs>,<bcl%>,<mV>，与 EM05 同款三字段，" +
			"第二个是电量百分比、第三个才是电压）→ 3.861 V。" +
			"该型号其余部分与 RM502Q-GL 基线一致（QENG/QNWINFO/QCAINFO/QGDCNT/QNWPREFCFG/usbnet 都可用），" +
			"另有两点实测差异不必改代码：5G SA 下 `AT+CREG?` 会回 `0,3`（注册被拒）而 `+C5GREG: 0,1` 已注册" +
			"（已有 5GS 兜底）；`AT+QENG=\"neighbourcell\"` 在 SA 下只回 OK（无邻区，与 RM502Q 一致）。",
	},
	{
		Match:        "EM05",
		IgnoreCCLKTZ: true,
		VoltageCmd:   "AT+CBC",
		Note: "EM05（4G Cat4）实测两件事：① `AT+CCLK?` 回 `26/09/29,12:36:30+32`，" +
			"而 12:36 就是 UTC（主机本地 20:36）——和 RM500U 同一个毛病，不减 8 小时；" +
			"② `AT+CBC` 回 `+CBC: 0,71,3862`（<bcs>,<bcl%>,<mV>），3862 mV = 3.862 V。" +
			"电压只给这个型号开：同厂其他型号没逐个验过，不敢给整个 Quectel 家族发。",
	},
	{
		Match:      "FM160",
		VoltageCmd: "AT+CBC",
		TempCmd:    "AT+GTLADC",
		Note: "广和通 FM160-EAU（固件 89611.1000.00.04.01.07）实测：" +
			"① 温度走 **AT+GTLADC**（`AT+QTEMP`/`AT+CPMUTEMP`/`AT+MTSM` 全 ERROR）——" +
			"应答是 `in_temp_pmx65_mdm_case_therm_input: 30843` 与 `[zone32]:cpuss-0-usr 31800` 两种写法，" +
			"数值都是 **1/1000 ℃**（31800 → 31.8 ℃），0 = 未上报、-40960 = 没接那路传感器，都要丢；" +
			"② `AT+CBC` 回 3GPP 两字段 `+CBC: 0,3816` → 3.816 V（与 FM650 同形）；" +
			"③ ⚠️ 它 `AT+CCLK?` 报的是**本地时间**而且 +32 后缀是对的（`26/09/29,22:25:46+32`，主机本地 22:25）" +
			"→ **不能**照 FM650/RM500U 那样忽略时区，标准语义就对（skew ≈ -1s）；" +
			"④ 无签约速率（`AT+CGEQNEG=1` 回 ERROR、没有 QNWCFG），" +
			"流量只有 `AT+GTSTATIS?`（4 个字段，官方脚本把前两个当瞬时速率 Byte/s，未能确认累计语义）→ 仍显示未上报。",
	},
	{
		Match:        "MT5700",
		IgnoreCCLKTZ: true,
		Note: "鼎桥 MT5700M-CN（MTK 平台，固件 V200R001C20B016）实测：" +
			"① `AT+CCLK?` 回 `26/09/29,17:57:06+32`，但 17:57 就是 UTC（主机本地 01:57）" +
			"—— 与 RM500U/EM05/FM650 同一个毛病（clock_skew 会显示 -28800s），不减这 8 小时；" +
			"② 电压：`AT+CBC` 回 ERROR（`AT^ADCREADEX?` 是 ADC 原始值、不是电压）→ 不显示；" +
			"③ 温度走鼎桥私有的 `AT^CHIPTEMP?`（12 路，0.1 ℃；见 modem_status.go）；" +
			"④ ICCID 要用 `AT^ICCID?`（`AT+CCID` 回 ERROR）；" +
			"⑤ 信号/服务小区/邻区/频率/流量全是 `^` 私有命令（AT^HCSQ? / AT^MONSC / AT^MONNC / " +
			"AT^HFREQINFO? / AT^DSFLOWQRY），通用的 AT+QENG/CPSI/QCSQ/QNWINFO/QGDCNT 全回 ERROR；" +
			"⑥ 它**每秒主动上报** ^RSSI/^CERSSI/^HCSQ 好几行（AT^CURC 默认全开），" +
			"这对「探测 AT 口」和「排残留」都有影响，已在 modem_query.go / modem_at.go 里处理。",
	},
	{
		Match:        "FM350",
		IgnoreCCLKTZ: true,
		VoltageCmd:   "AT+CBC",
		TempCmd:      "AT+GTSENRDTEMP=0",
		PDPAddrCmd:   "AT+CGPADDR=2",
		SMSReadPDU:   true,
		Note: "广和通 FM350-GL（MTK 平台，固件 81600.0000.00.29.22.40）实测：" +
			"① `AT+CCLK?` 回 `26/09/29,17:20:14+32`，但 17:20 就是 UTC（主机本地 01:20）" +
			"—— 与 FM650/RM500U/EM05 同一个毛病（clock_skew 会显示 -28800s），不减这 8 小时；" +
			"② `AT+CBC` 回 3GPP 两字段 `+CBC: 0,4130` → 4.130 V；" +
			"③ 温度：`AT+QTEMP`/`AT+GTLADC`/`AT+MTSM=1,6`/`AT+CPMUTEMP` 全 ERROR，" +
			"走手册 §18.3 的 `AT+GTSENRDTEMP=0`（**必须带参数**，`AT+GTSENRDTEMP?` 回 ERROR）" +
			"→ 实测 23 路，数值与 FM160 的 AT+GTLADC 同口径（1/1000 ℃）；" +
			"④ `AT+CGPADDR`（不带 cid）直接回 ERROR，只有 `AT+CGPADDR=<cid>` 可用：" +
			"该模块 cid 1 是 IMS、cid 2 才是上网用的 PDP（`AT+CGPADDR=2` → `10.2.98.236`）；" +
			"⑤ 不出流量统计：`AT+GTSTATIS?`/`AT+QGDCNT?` 全 ERROR，手册里也没有字节计数器指令 → 界面显示未上报；" +
			"⑥ 服务小区用 `AT+GTCCINFO?` 但不带标签行、ARFCN/PCI 是十进制（见 cell.go 文件头）；" +
			"⑦ `AT+QNWINFO` 回 ERROR，制式/频段靠平台自家的 `AT+GTCAINFO?`（该固件在 SA 下只回 OK、无内容）；" +
			"⑧ ⚠️ **短信：文本模式下回的“正文”其实是十六进制 PDU**（实测 `AT+CMGF=1` + `AT+CMGL=\"ALL\"` 回 " +
			"`+CMGL: 1,\"REC READ\",\"106591146189\",\"\",\"2026/07/17 10:42:45+32\"` 加一行 `050003B204015C0A656C…`）——" +
			"按 UCS2 文本解会把 UDH 解成怪字符（页面看着就是“短信读取不正常”）；切 `AT+CMGF=0` + `AT+CMGL=4` 回的" +
			"是完整 PDU（带 SMSC 头/UCTS2），`ParsePDUList` 能正确解出号码/时间/正文/分条（见 parse_test.go 的 FM350 用例）" +
			"→ 故在特例表里标 `SMSReadPDU`（sms.go 会改走 PDU 读法）。",
	},
	{
		Match:                "FM650",
		IgnoreCCLKTZ:         true,
		VoltageCmd:           "AT+CBC",
		TempCmd:              "AT+MTSM=1,6",
		PDPTimeout:           20 * time.Second,
		NoReplyOnPDPActivate: true,
		Note: "广和通 FM650-CN（展锐平台，固件 86600.1000.00.05.17.11）实测：" +
			"① `AT+CCLK?` 回 `26/09/29,13:14:53+32`，但 13:14 就是 UTC（主机本地 21:14）" +
			"—— 与 RM500U/EM05 同一个毛病（clock_skew 会显示 -28800s），不减这 8 小时；" +
			"② `AT+CBC` 回 `+CBC: 0,3799`（3GPP 两字段写法，第二个就是 mV）→ 3.799 V；" +
			"③ 温度：`AT+QTEMP` / `AT+GTLADC` / `AT+GTSENRDTEMP` / `AT+CPMUTEMP` 全 ERROR，" +
			"只有 `AT+MTSM=1,6` 回 `+MTSM: 60`（官方集成脚本也是用这条）；" +
			"④ ⚠️ **激活 PDP 时 `AT+CGACT=1,<cid>` 可能迟迟不给最终结果**（实测：真机点「激活」时" +
			"发出去 10s 内既没有 `OK` 也没有 `ERROR`，默认 3s 的 cmd_timeout 会先超时）——" +
			"更糟的是这条没答完的指令会**把 AT 通道带错位**，紧接着的 `AT+CGDCONT?` 也跟着超时" +
			"（用户看到的就是「AT command timeout: AT+CGDCONT?」，看着像查询不支持，其实是被上一条拖累）。" +
			"所以这里把 PDP 相关指令（`AT+CGDCONT?` / `AT+CGACT?` / `AT+CGACT=`）统一放宽到 20s，" +
			"并且 AT+CGACT **超时不当失败**：改由回读 `AT+CGACT?` 判定" +
			"（见 data.go 的 SetPDPActive / NoReplyOnPDPActivate）。只对 FM650 生效，别的型号不受影响。",
	},
	// ---------- 下面这段：把 QModem 各厂商脚本里**有取证**、而本项目原来缺的 AT 补上 ----------
	//
	// 为什么放在**机型特例**而不是厂商指令集：这些厂商（telit / thales / gosuncn / meig /
	// sierra / openluat）在本项目里走的是「未识别厂商 + 自适应指令集 + 候选探测」，
	// 没有各自的 Command 实现；用特例表就能做到「只对这些型号发这些指令」，
	// 别的型号一个字节都不变（同 TempCmd / VoltageCmd 的老做法）。
	//
	// 字段与证据（全部来自 QModem `cmds/*.sh` + `vendor/*.sh` 原文）：
	//   VoltageCmd / TempCmd / ICCIDCmd / ServingCellCmd / NeighbourCellCmd
	//   解析实现在 vendor_at_extra.go（ParseTelitTemp / ParseThalesTemp / ParseMeigTemp）。
	{
		Match:           "FN990", // Telit FN990A40
		VoltageCmd:      "AT#CBC",
		TempCmd:         "AT#TEMPSENS=2",
		ICCIDCmd:        "AT+ICCID",
		ServingCellCmds: []string{"AT#CAINFOEXT?"},
		Note: "Telit FN990A40（Modem-Pro cmds/telit.sh + vendor/telit.sh）：① 电压 `cmd_cbc` → **AT#CBC**（不是 3GPP 的 AT+CBC）；" +
			"② 温度 `cmd_tempsens` → **AT#TEMPSENS=2**，`vendor/telit.sh` 从应答里取 `#TEMPSENS: TSENS,<值>`；" +
			"③ ICCID `cmd_iccid` → **AT+ICCID**；" +
			"④ 服务小区/CA `cmd_cainfoext_query` → **AT#CAINFOEXT?**（vendor/telit.sh 的 cell_info()）：" +
			"`#CAINFOEXT: <载波数>,<制式>` + 主载波行 `PCC-` 内的 `BandClass:/BW:/CH:/PCI:/RSRP:/RSRQ:/RSSI:]/SINR:(dB=-20+raw*0.2)/TAC:`；" +
			"辅载波 `SCCn-` 行一并接成 CA。解析：ParseTelitTemp / ParseCBC / ParseICCID / ParseCAInfoExt / ParseVoltage。",
	},
	{
		Match:           "MV32", // Thales MV32-W-A / MV32-W-B
		TempCmd:         "AT^TEMP",
		ICCIDCmd:        "AT+ICCID",
		ServingCellCmds: []string{"AT^DEBUG?", "AT!DEBUG?"},
		Note: "Thales MV32-W（Modem-Pro cmds/thales.sh 的手册对照表，手册 TC_MV32-W_AT_Command_Reference_Guide_r2）：" +
			"① 温度 §15.13 `AT^TEMP`，`vendor/thales.sh` 用 `sed -n 's/.*TSENS: \\([0-9]*\\)C.*/\\1/p'` 取值（应答里带 `TSENS: 33C`）；" +
			"② ICCID §15.10 `AT+ICCID`；" +
			"③ 服务小区/信号 §15.33 `AT^DEBUG`（vendor/thales.sh 的 cell_info() 逐字段解析）：`RAT:` 给制式（WCDMA/LTE/LTE+NR/NR5G_SA），" +
			"LTE 用 `lte_band:/lte_rsrp:/lte_rssi:/lte_snr:/lte_tac:/pci:/channel:`，NR 用 `nr_band:/rsrp:/rsrq:/nr_snr:/nr_tac:`；" +
			"`AT^` / `AT!` 两种前缀都试；解析 ParseDebugCell（信号多读数与 Modem-Pro 一样取平均）；" +
			"④ USB 数据模式 `AT+SETCONFIG`、组网偏好 `AT^SLMODE`、锁频段 `AT+BAND_PREF`、SIM 槽 `AT+SWITCH_SLOT` 本项目均已接入。",
	},
	{
		Match:           "N510M", // 有方 N510M（展锐平台）
		ServingCellCmds: []string{"AT+NETDMSGEX"},
		ICCIDCmd:        "AT$MYCCID",
		AMBRCmd:         "AT+C5GQOSRDP",
		Note: "有方 N510M 服务小区/信号走 `AT+NETDMSGEX`（vendor/neoway.sh 的 cell_info()，逐格 awk 取证）：" +
			"f1=制式串、f2=`MCC+MNC`（脚本用 `cut -d'+'`，真机形如 `460+11`）、f3=band、f4=频点；" +
			"NR 用 f5/f6/f7/f8/f9=cell_id/PCI/SS-RSRP/RSRQ/SINR（均÷10），LTE 用 f5=TAC、f6/f7=cell_id/PCI、" +
			"f10~f13=RSRP/RSRQ/SINR/RSSI（÷10），WCDMA 用 f5=LAC、f6/f7=cell_id/PSC；解析 ParseNetDMsgEx。" +
			"ICCID 走 `AT$MYCCID`（脚本 cmd_myccid，本项目 ParseICCID 已兼容 $MYCCID 拼写）。" +
			"AMBR 走 `AT+C5GQOSRDP`（vendor/neoway.sh 的 network_info()：3GPP 5GS QoS 布局，" +
			"`+C5GQOSRDP: <cid>,<5QI>[,<DL_GFBR>,<UL_GFBR>[,<DL_MFBR>,<UL_MFBR>[,<DL_SAMBR>,<UL_SAMBR>]]]]`，" +
			"脚本取 $7=DL_SAMBR、$8=UL_SAMBR（kbit/s）⇒ 本项目 ParseC5GQOSRDP 按同一口径取 f7/f8，不猜别的下标）。",
	},
	{
		Match:           "GM800", // 高新兴 GM800（qualcomm 平台）
		TempCmd:         "AT+MTSM=1",
		ServingCellCmds: []string{"AT+ZCELLINFO?"},
		Note: "高新兴 GM800（qualcomm 平台）服务小区/信号也走 `AT+ZCELLINFO?`，但字段是**标签形**" +
			"（vendor/gosuncn.sh 的 cell_info_qualcomm() 逐键 grep）：`+ZCELLINFO:<制式>:…tac:N,cell_id:N,pci:N," +
			"band:N,freq:N,mcc:N,mnc:N,rsrp:N,rsrq:N,rssi:N,sinr:N,rscp:N,ecio:N`；NSA 另有一段带 " +
			"`narfcn:/nr5g_pci:/5g_rsrp:/5g_rsrq:/5g_sinr:` 的辅小区行（**本项目只取主小区**，辅小区不猜）。" +
			"解析 ParseZCellInfo（两种平台形状都认）；温度 AT+MTSM=1。",
	},
	{
		Match:             "AIR724", // openluat Air724UG
		ServingCellCmds:   []string{"AT+CCED=0,1"},
		NeighbourCellCmds: []string{"AT+CCED=0,2"},
		ICCIDCmd:          "AT+ICCID",
		VoltageCmd:        "AT+CBC",
		Note: "合宙 Air724UG（Modem-Pro cmds/openluat.sh）：① 服务小区 `AT+CCED=0,1`、邻区 `AT+CCED=0,2`；" +
			"② ICCID `AT+ICCID`；③ 电压 `AT+CBC`；④ 组网模式 `AT+CTEC`、锁频段 `AT*BAND`、USB `AT+SETUSB` 本项目已接入。" +
			"`+CCED:` 的字段表取自 vendor/openluat.sh 的**消费端读取顺序**（解析函数只按字段数切分，" +
			"cell_info()/get_neighborcell() 用 `IFS='|' read -r …` 给每个字段命名）：" +
			"LTE 服务小区 13 段（mcc,mnc,imsi,roaming,band,bandwidth,dl_earfcn,cell_id,rsrp_raw,rsrq_raw,tac,srxlev,pci）、" +
			"GSM 服务小区 8 段、LTE 邻区 9 段、GSM 邻区 6 段；信号换算是脚本写明的公式（RSRP=raw-141、RSRQ=raw*0.5-20）。" +
			"解析 ParseOpenluatCCED / ParseOpenluatCCEDNeighbours（module/kernel/vendor_cell_extra.go）。" +
			"⚠️ 刻意不填：`srxlev`（小区选择电平**索引**，不是 dBm）、GSM 的 `bsic`/`rxlev`（不是 PCI/RSRP 口径）。",
	},
	{
		Match:           "SRM", // 美格 SRM815 / SRM825 / SRM825N / SRM821
		TempCmd:         "AT+TEMP",
		ServingCellCmds: []string{"AT^CELLINFO=1"},
		ICCIDCmd:        "AT+ICCID",
		AMBRCmd:         "AT^DSAMBR=1",
		Note: "美格 SRM 系列（Modem-Pro cmds/meig.sh + vendor/meig.sh）：① 温度 **AT+TEMP**（按传感器名取 " +
			"`TEMP: \"soc-thmzone\"` / `TEMP: \"cpu0-0-usr\"`，解析 ParseMeigTemp）；" +
			"② 服务小区/信号 **AT^CELLINFO=<cid>**（CSV 逐格编号：NR 取 f5=cell_id/f6=PCI/f7=TAC/f8=band/f9=带宽/f15,16,17=" +
			"RSRP,RSRQ,SINR÷10；EN-DC 取 f6=PCI/f8=cell_id/f9=TAC/f10=band/f15,16,17 信号，解析 ParseMeigCellInfo）；" +
			"③ ICCID **AT+ICCID**（vendor/meig.sh，本项目 ParseICCID 认 +ICCID 拼写）；" +
			"④ USB AT+SER、组网 AT^SYSCFGEX、IMEI AT+LCTSN 均已接入。" +
			"⚠️ EN-DC 的 NR 辅小区字段在脚本里自相矛盾（$30 既当 PCI 又当 RSRP）⇒ 不猜；" +
			"美格没有锁网指令（Modem-Pro 只有 AT^SYSCFGEX 组网与 AT^CELLINFO 小区信息），本项目已确认不支持。",
	},
	// ---------- 服务小区/信号：未识别厂商靠**自己的**私有指令（vendor/*.sh 取证） ----------
	// 解析实现在 vendor_cell_extra.go（ParseDebugCell / ParseSierraGStatus / ParseMeigCellInfo /
	// ParseZCellInfo / ParseCAInfoExt），信号按行抽取在 VendorServingSignal。
	// ⚠️ **同一个 Match 只能出现一次**：Lookup 取的是第一条命中的（重复的会被前面的遮住）。
	{
		Match:           "T77W", // Foxconn T77W968
		ServingCellCmds: []string{"AT!DEBUG?", "AT^DEBUG?"},
		VoltageCmd:      "AT!PCVOLT?",
		TempCmd:         "AT!TEMP?",
		Note: "Foxconn 与 Thales 同形：服务小区/信号 `AT[!^]DEBUG`（vendor/foxconn.sh 的 cell_info() 与 Thales 逐字段相同）、" +
			"电压 `AT[!^]PCVOLT?`（应答里 `Power supply voltage: 3860 mV`）、温度 `AT[!^]TEMP?`（应答里 `TSENS: 33C`）。" +
			"`AT!` / `AT^` 两种前缀都试（Modem-Pro 用 `at_pre` 变量按型号选）。",
	},
	{
		Match:           "T99W", // Foxconn T99W175/373/368/640
		ServingCellCmds: []string{"AT!DEBUG?", "AT^DEBUG?"},
		VoltageCmd:      "AT!PCVOLT?",
		TempCmd:         "AT!TEMP?",
		Note: "同 T77W968（Foxconn 同一套 `AT[!^]DEBUG` / `PCVOLT` / `TEMP`）；这三个型号都是 PCIe/USB 双形态，" +
			"PCIe 形态下 AT 口是 /dev/mhi_DUN 这类字符设备，指令与解析完全一样。",
	},
	{
		Match:           "EM919", // Sierra EM9190 / EM9291
		ServingCellCmds: []string{"AT!GSTATUS?"},
		VoltageCmd:      "AT!PCVOLT?",
		TempCmd:         "AT!PCTEMP?",
		Note: "Sierra `AT!GSTATUS?`（vendor/sierra.sh 的 network_info() → _parse_gstatus）：应答是逐行 `key: value`，" +
			"键里含 RSRP/RSRQ/RSSI/SINR 的算信号，其余当普通信息；电压 `AT!PCVOLT?`（`Power supply voltage: 3860 mV`）、" +
			"温度 `AT!PCTEMP?`（`Temperature: 33.5`）。高级指令前需 `AT!ENTERCND=\"A710\"`（已接在 USB 方言的 pre 里）。",
	},
	{
		Match:           "MC735", // Sierra MC7354 / MC7355
		ServingCellCmds: []string{"AT!GSTATUS?"},
		VoltageCmd:      "AT!PCVOLT?",
		TempCmd:         "AT!PCTEMP?",
		Note: "Sierra MC7354/MC7355（老 4G 模块）：服务小区/信号与 EM919x 同为 `AT!GSTATUS?`（同一套 key:value 解析）——" +
			"⚠️ 老固件的键**没有 `LTE `/`NR5G ` 前缀**，所以 ParseSierraGStatus 里每项都兼带了无前缀候选；" +
			"电压 `AT!PCVOLT?`、温度 `AT!PCTEMP?` 同 EM919x。若某固件回的是单行 CSV 形式，则解析不到（只保留原始应答，不猜）。",
	},
	{
		Match:           "EM9291", // Sierra EM9291（与 EM9190 同套 AT，只是型号串里没有 "EM919"）
		ServingCellCmds: []string{"AT!GSTATUS?"},
		VoltageCmd:      "AT!PCVOLT?",
		TempCmd:         "AT!PCTEMP?",
		Note:            "同 EM9190（Sierra 同一套 `AT!GSTATUS?` / `AT!PCTEMP?` / `AT!PCVOLT?` + `AT!ENTERCND=\"A710\"` 解锁）。",
	},
	// ⚠️ 美格 SRM 的特例**只有上面那一条**（第 13 条，含 TempCmd/AMBRCmd 的更全的那条）。
	// 这里曾经又写了一条 `Match: "SRM"`（只有 ServingCellCmds + ICCIDCmd）——
	// 因为 Lookup 取的是**第一条**命中的，第二条永远命中不到（静默失效），已删。
	// 这就是 atcmd_invariants_test.go 里 TestQuirkTableInvariants 要钉住的坑。
	{
		Match:           "ME3630", // 高新兴 ME3630-W（lte 平台）
		ServingCellCmds: []string{"AT+ZCELLINFO?"},
		Note: "高新兴 lte 平台（ME3630-W）服务小区 `AT+ZCELLINFO?` → `+ZCELLINFO: <TAC>,cellid:<CellID>,pci:<PCI>,band:<Band>`" +
			"（vendor/gosuncn.sh 的 cell_info_lte()，注释原文就是这个形状）；信号另发 `AT+CESQ`（本项目已有 ParseCESQ）。" +
			"解析 ParseZCellInfo（与 qualcomm 平台的标签形共用一个入口）。",
	},
}

// Lookup 按模组型号串（ATI 原文）查特例；没命中就返回零值。
//
// 匹配用的是型号串；型号为空（还没读身份）时不会命中任何特例，
// 上层拿到的就是标准语义 —— 所以不影响启动早期。
func Lookup(model string) Quirk {
	hay := strings.ToUpper(model)
	if hay == "" {
		return Quirk{}
	}
	for _, q := range Table {
		if q.Match != "" && strings.Contains(hay, strings.ToUpper(q.Match)) {
			return q
		}
	}
	return Quirk{}
}
