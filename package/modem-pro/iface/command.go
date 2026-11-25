package iface

// 本文件放「模组指令能力」的全部接口。
//
// 这些接口都由**厂商实现**（module/<vendor>）、由**引擎调用**（kernel）。
// 拆成许多**窄接口**是刻意的：厂商只实现自己有的，引擎用类型断言探测能力，
// 「这一家没有」不会影响任何别家。
//
// ⚠️ 新增能力时的规矩：先加一个**新接口**，别往 ModemCommand 里塞方法 ——
// ModemCommand 每加一个方法，所有厂商实现都得跟着补，
// 而那正是我们要避免的"改一处牵连一片"。

// ModemCommand 统一指令接口，所有模组厂商实现该接口。
// 上层业务（信号查询、短信收发）只依赖此接口，不关心具体厂商。
type ModemCommand interface {
	// DetectVendor 从 ATI 返回内容识别厂商
	DetectVendor(resp []string) ModemVendor
	// CommandModel 获取型号指令
	CommandModel() string
	// CommandCSQ 基础信号指令
	CommandCSQ() string
	// CommandNRServingCell 5G NR 小区信号指令
	CommandNRServingCell() string
	// CommandSMSTextMode 短信文本模式指令
	CommandSMSTextMode() string
	// CommandReadAllSMS 读取全部短信指令
	CommandReadAllSMS() string
	// CommandSendSMS 发送短信前置指令 AT+CMGS
	CommandSendSMS(phone string) string
	// CommandNetworkReg 查询网络注册状态指令
	CommandNetworkReg() string
	// CommandIMEI 查询 IMEI 指令
	CommandIMEI() string
	// CommandFirmware 查询固件版本指令（AT+CGMR）
	CommandFirmware() string
	// CommandSMSStorage 设置短信存储位置指令（AT+CPMS）
	CommandSMSStorage(mem string) string
	// CommandQuerySMSStorage 查询短信存储用量指令（AT+CPMS?）
	CommandQuerySMSStorage() string
	// CommandSMSNotify 新短信上报配置指令（AT+CNMI）：不开这个收不到 +CMTI URC
	CommandSMSNotify() string
	// CommandQuerySMSNotify 回读新短信上报配置指令（AT+CNMI?），用于确认真的生效
	CommandQuerySMSNotify() string
	// CommandDeleteSMS 删除短信指令；index < 0 表示删除全部
	CommandDeleteSMS(index int) string
	// CommandNeighbourCell 查询邻小区（附近基站）指令
	CommandNeighbourCell() string
	// CommandServingCell 查询当前服务小区指令
	CommandServingCell() string
	// CommandNetInfo 查询网络接入信息指令（可选）
	CommandNetInfo() string
	// CommandOperator 查询当前运营商指令（AT+COPS?）
	CommandOperator() string
	// CommandCAInfo 查询载波聚合（CA）分量载波指令；返回空串表示该厂商未实现
	CommandCAInfo() string
	// ParseNRSignal 解析 5G NR 信号，返回 RSRP, RSRQ, SINR
	ParseNRSignal(line string) (rsrp, rsrq, sinr int, ok bool)
}

// CommandCandidates 可选能力：同一功能有多套写法时给出候选指令。
//
// 用于拿不到官方指令手册、或同型号不同固件差异很大的模组（如 MT5700）：
// 除了 AT+QENG="servingcell"，还有 +CPSI? / +QCSQ 等等。
// Modem 会在运行期按顺序试探，记住第一条能正常应答的指令（见 resolveVendorCmd）。
type CommandCandidates interface {
	// ServingCellCandidates 服务小区查询候选指令（按可能性降序）
	ServingCellCandidates() []string
	// NeighbourCellCandidates 邻区查询候选指令（按可能性降序）
	NeighbourCellCandidates() []string
	// TrafficCandidates 数据流量计数器查询候选指令；返回空表示该厂商不提供
	TrafficCandidates() []string
	// TrafficEnableCommand 让计数器开始计数的指令（空串表示不需要）
	TrafficEnableCommand() string
}

// USBnetConfigurator 可选能力：模块对外输出上网的协议（USB 网卡模式）。
// 只有实现了该接口的厂商才会被查询/设置，未实现的直接报"不支持"，
// 避免向没有这个指令的固件乱发 AT+QCFG。
type USBnetConfigurator interface {
	// CommandQueryUSBnet 查询指令，如 AT+QCFG="usbnet"
	CommandQueryUSBnet() string
	// CommandSetUSBnet 设置指令，如 AT+QCFG="usbnet",2（需重启模组生效）
	CommandSetUSBnet(value string) string
}

// AMBRQuerier 可选能力：查询签约速率（运营商下发的 AMBR，单位 Mbps，见 ambr.go）。
// 3GPP 标准指令在本固件上不带速率，属于厂商私有指令，所以同样只在实现该接口的
// 厂商上查询；未实现的模组前端显示"未上报"。
type AMBRQuerier interface {
	// CommandAMBR 查询签约速率指令；返回空串表示该厂商不提供
	CommandAMBR() string
}

// PDUSMSReader 可选能力：读短信必须走 PDU 模式并自行解码的固件。
//
// 实测鼎桥 MT5700（固件 V200R001C20B016）：**文本模式下模组不回正文** ——
// `AT+CMGL="ALL"` / `AT+CMGR=<idx>` 只给 `+CMGL: <位置>,"REC READ","号码",,"时间"` 头，
// 正文一行都没有（单条/长短信都一样），于是界面短信列表有条目、内容全空；
// 切到 PDU 模式（AT+CMGF=0 + AT+CMGL=4）后正文完整，只能自己解码（见 sms/pdu.go）。
//
// 只声明指令，解码/分条逻辑共用一套，其它厂商不受影响（不实现该接口就走原来的文本模式）。
type PDUSMSReader interface {
	// CommandSMSModePDU 切到 PDU 模式（如 AT+CMGF=0）
	CommandSMSModePDU() string
	// CommandReadAllSMSPDU 读取全部短信（PDU 模式，如 AT+CMGL=4）
	CommandReadAllSMSPDU() string
}

// PDUSMSSender 可选能力：**发**短信也只能走 PDU 模式的固件。
//
// 与 PDUSMSReader 分开是有意的：有的固件只是"读"要 PDU（实测鼎桥 MT5700：
// 文本模式不回正文），但"发"仍然能用文本模式；中兴 F50（展锐 UMS9620）则是
// 两边都只能走 PDU —— `AT+CSCS?` 是 `"HEX"`、`AT+CMGF?` 是 0，
// 厂商自己的 `sms send` 脚本也是"`AT+CMGS=<len>` + PDU + Ctrl+Z"两步走。
//
// 没实现该接口的厂商**一个字节都不会受影响**：doSendSMS 里是早退分支。
type PDUSMSSender interface {
	// CommandSMSModePDU 切到 PDU 模式（与 PDUSMSReader 同一条，如 AT+CMGF=0）
	CommandSMSModePDU() string
	// CommandSendSMSPDULength 提交 PDU 的第一步（如 AT+CMGS=<len>），只为了要那个 `>` 提示符
	CommandSendSMSPDULength(tpduLen int) string
}

// StandardPDUReader 3GPP 标准 PDU 读法（AT+CMGF=0 + AT+CMGL=4）。
//
// 给「机型特例表标了 SMSReadPDU」的固件用（实测 FM350-GL）：这类固件在文本模式下回的
// "正文"其实是一整串十六进制 PDU，必须切 PDU 模式、把完整 PDU 拿回来自己解码。
// 与厂商级实现（MT5700Command）的区别：那套是"整个厂商的固件都不回正文"，
// 这套只是"个别机型文本模式不能信"，所以由特例表按型号开关，不动其他机型。
type StandardPDUReader struct{}

func (StandardPDUReader) CommandSMSModePDU() string    { return "AT+CMGF=0" }
func (StandardPDUReader) CommandReadAllSMSPDU() string { return "AT+CMGL=4" }

// TempQuerier 可选能力：模块温度查询（各家写法不同，见 modem_status.go）。
//
// 移远是多路传感器（AT+QTEMP 回十几条 +QTEMP:"名字",温度），
// 芯讯通 SIM8200 只有一路整机温度（AT+CPMUTEMP → +CPMUTEMP: 25，
// 手册 V1.01 §20.2.8；同章节的 AT+CCPUTEMP 在本固件回 ERROR）。
type TempQuerier interface {
	// CommandTemp 温度查询指令；返回空串表示该厂商不提供
	CommandTemp() string
}

// ExtraNetInfoQuerier 可选能力：附加网络信息（带宽 / 调制 / CQI / 上行发射功率）。
//
// 目前只有芯讯通 SIM8200 声明（手册 §4.2.18 的 AT+CNWINFO）。
// ⚠️ 做成"可选能力"而不是所有人共用：**没实现该接口的厂商（移远 / 广和通 / MT5700）
// 连这条指令都不会发**，接口与界面行为跟以前完全一样。
type ExtraNetInfoQuerier interface {
	// CommandExtraNetInfo 附加网络信息查询指令；返回空串表示该厂商不提供
	CommandExtraNetInfo() string
}

// VoltageQuerier 可选能力：供电电压（AT+CBC）。
//
// 各家应答格式不同：芯讯通 SIM8200 回 `+CBC: 3.858V`（带单位），
// 3GPP/移远 回 `+CBC: <bcs>,<bcl>`（毫伏）；解析两种都认，
// 同样只有声明了该能力的厂商才会被查询。
type VoltageQuerier interface {
	// CommandVoltage 供电电压查询指令；返回空串表示该厂商不提供
	CommandVoltage() string
}

// ICCIDQuerier 可选能力：ICCID 查询指令（默认是 3GPP 的 `AT+CCID`）。
// 个别固件不认标准写法（实测鼎桥 MT5700 的 `AT+CCID` 回 ERROR，要用 `AT^ICCID?`），
// 只有声明了该能力的厂商才会改用它的指令（见 modem_status.go 的 queryICCID）。
type ICCIDQuerier interface {
	// CommandICCID ICCID 查询指令；返回空串表示沿用标准写法
	CommandICCID() string
}

// USSDFormatter 可选能力：USSD 码要按本机字符集**编码**之后才能发。
//
// 为什么需要：`AT+CUSD` 的字符串参数是按 `AT+CSCS` 解释的。实测中兴 F50
// （展锐 UMS9620）出厂 `AT+CSCS?` 就是 `"HEX"`，直接发
// `AT+CUSD=1,"*101#",15` 会回 `+CME ERROR: 3`，必须发 hex 形态
// `AT+CUSD=1,"2A31303123",15` 才被接受。
//
// 没实现该接口的厂商**一个字节都不变**（SendUSSD 直接把原码拼进去）。
type USSDFormatter interface {
	// USSDCode 把用户输入的 USSD 码（如 `*101#`）编成要放进 AT 命令的形态
	USSDCode(code string) string
}

// APNCandidatesProvider 可选能力：APN / 激活承载要用厂商自己的写法查。
//
// 为什么需要：3GPP 的 `AT+CGCONTRDP`（**不带 cid**）在多数固件上能一次列出全部
// 激活承载，但展锐 UMS9620（中兴 F50）实测**只认带 cid 的写法**：
//
//	AT+CGCONTRDP    → +CME ERROR: 65536014
//	AT+CGCONTRDP=?  → +CGCONTRDP: (1,11)        ← 支持的 cid 就是这两个
//	AT+CGCONTRDP=1  → +CGCONTRDP: 1,6,ctnet,10.194.190.206.255.0.0.0,,218.2.2.2,218.4.4.4,…
//
// 不带 cid 被拒后原来的实现会退回 `AT+CGDCONT?`，而那只说明"配置了哪些 APN"、
// 拿不到**当前激活**的 APN / 本机 IP / DNS。
//
// 没实现该接口的厂商**一个字节都不变**（走原来的 `AT+CGCONTRDP` → `AT+CGDCONT?`）。
type APNCandidatesProvider interface {
	// APNCandidates APN 查询候选指令（按可能性降序）；返回空 = 用默认那两条
	APNCandidates() []string
}

// CAVendorOps 可选能力：CA / 辅小区的查询由厂商自己实现。
//
// 移远用 AT+QCAINFO（一行一个 CC，带 "PCC"/"SCC" 角色、<band_text> 是 "LTE BAND 3" 这种字符串）；
// 鼎桥 MT5700 完全是另一套：
//
//	§13.18 AT^CASCELLINFO?  LTE CA 的 SCell（最多 4 个；**未配置 CA 时返回 ERROR**）
//	§13.27 AT^MONSSC        NSA(EN-DC) 的辅连接小区（**无辅连接时回 ^MONSSC: NONE**）
//
// 两家的"字段含义"、"没有聚合时怎么表示"都不一样，所以整块交给厂商；
// 不实现该接口的厂商一点不受影响（照旧走 CommandCAInfo + 通用解析）。
type CAVendorOps interface {
	// QueryCA 查询分量载波列表（自己发指令、自己解析）；返回空列表 = 当前没有聚合
	QueryCA(at ATDoer) ([]CCInfo, error)
	// CACmds 该机型实际使用的 CA 查询指令（界面按它渲染提示文案）；空 = 不支持
	CACmds() []string
	// CAEmptyNote 列表为空时的补充说明（例如"无 CA 时模组回 ERROR，属正常"）；空 = 没有
	CAEmptyNote() string
}
