package kernel

import (
	"fmt"
	"strings"

	"modemd/module/variant"
)

// ====================== 能力缺口表（"为什么没有这个功能"） ======================
//
// `MD/module_support.md` F.2 原来是"生成汇总 + **人手写的逐条原因**"：
// 生成表只能告诉你有几个型号没有某能力，说不清**为什么**；而"为什么"恰恰是
// 最值钱的部分 —— 它是"已经查过了、别重复查"的结论，也是"以后要不要补"的依据。
//
// 手写的问题：① 改代码时没人会想到去改文档里的散文；② 同一件事（比如 AMBR 哪些家没有）
// 会在 AT 计划、README、文档三处各写一遍，口径迟早不一致。
// 所以这里把它变成**结构化数据**：
//   - Reason  = 为什么没有（或为什么刻意不接）
//   - Evidence= 证据 / 线索（脚本路径、手册章节、代码位置、实测结论）
//   - Status  = 未接入 / 已接入（留档）/ 刻意不接 / 待复核
//
// 文档 F.2 的表由 CapabilityGapsMarkdown() 生成（`TestCatalogSyncDoc` 钉住），
// AT 计划里每个"未接入"的能力也会带上这里的 Reason（界面悬停可见），
// 两边共用同一份数据 ⇒ 不会再出现"文档说 A、界面说 B"。

// gapStatus 缺口的处置状态
type gapStatus string

const (
	gapOpen      gapStatus = "未接入"  // 还没做：等证据 / 等真机样本
	gapNotNeeded gapStatus = "刻意不接" // 能做但**故意不做**（冗余、有风险、模型不匹配）—— 附理由
	gapDone      gapStatus = "已接入"  // 以前的缺口，已实现：留档，防止以后被"重新发现"再写一遍
	gapResearch  gapStatus = "待复核"  // 证据互相矛盾（如脚本注释与代码不一致）：先不接
)

// capabilityGap 一条"缺口"记录
type capabilityGap struct {
	// Capability 能力名（与 AT 计划里 ATCmdSpec.Label 对得上的用同名，界面才能把原因带上去）
	Capability string
	Status     gapStatus
	// Models 涉及哪些型号 / 厂商（清单里的写法，逗号分隔；空 = 面向全平台的能力）
	Models string
	// Reason 为什么没有 / 为什么刻意不接
	Reason string
	// Evidence 证据 / 线索（QModem 脚本路径、手册章节、本项目代码位置、实测结论）
	Evidence string
}

// capabilityGaps 缺口总表。新增一条就加一行（表结构与"型号是否在清单里"由测试钉住）。
var capabilityGaps = []capabilityGap{
	{
		Capability: "USB 模式切换", Status: gapDone,
		Models: "7 / 90 个有 USB 形态的型号（移远 hisilicon：rg801；Sierra mc7354/mc7355；华为 s10；" +
			"芯讯通 asrmicro：a8200c-m2 / simcom_a8200_serias；有方 N510M）",
		Reason: "✅ **已接入**：USB 网卡模式面板的取值与下发指令取自清单 `usb_at`（`usbnetModes` → " +
			"`catalogUSBModesFromAT`、`usbnetSetCommand` → `catalogUSBSetCommand`）。剩下的这 7 个型号 Modem-Pro " +
			"的 `set_mode()` **没有该平台的 set 分支**（会落到它脚本自己的兜底默认值）、参数值未取证 ⇒ " +
			"面板**只读**（下拉为空 + 显示原因，写入直接拒），**不猜**参数",
		Evidence: "生成工具 `files/tools/catalogmodeat`（数据源 Modem-Pro `cmds_*.sh` + `vendor_*.sh`）；" +
			"这些型号在 `module/kernel/modem_catalog.json` 里的 `usb_at` 只有 `query` + `note`、没有 `modes`；" +
			"回归 `TestUSBNetUnverifiedModelsAreReadOnly`",
	},
	{
		Capability: "PCIe 模式切换", Status: gapNotNeeded,
		Models: "41 个有 PCIe 形态的型号（只有 Foxconn / 芯讯通 / Thales 能查）",
		Reason: "Modem-Pro 里除富士康 `AT^PCIEMODE?`、芯讯通 `AT+CPCIEMODE?`、Thales `AT+MODESWITCH?`（且只有查询）外，" +
			"**没有任何厂商提供 PCIe 形态的切换指令**；广和通 MTK 平台 PCIe 直接走驱动（Modem-Pro 里是 `mtk_pcie`）" +
			"⇒ 不编一条不存在的指令，只把\"为什么没有\"记进清单",
		Evidence: "`module/kernel/modem_catalog.json` 每个 PCIe 型号的 `pcie_at.note`（逐条说明）；" +
			"`docs_support_list.md` 只有形态与模式，没有指令",
	},
	{
		Capability: "供电电压", Status: gapOpen,
		Models: "59 / 91（华为全系、移远大部分 5G、美格、有方、高新兴…）",
		Reason: "供电电压**不是 3GPP 标准能力**：只有部分固件实现了可读电压的 `AT+CBC`" +
			"（三字段 `+CBC: <bcs>,<bcl%>,<mV>`）或厂商私有指令（Sierra/Thales/Foxconn 的 `AT[!^]PCVOLT?`）；" +
			"多数 5G 模组手册里就没有这条，或只回充电状态/电量档",
		Evidence: "本项目 `AT+CBC` 只认能出 mV 的形式（`ParseCBC` 认三种：带单位、三字段、两字段），" +
			"认不出就当未上报 —— **不拿电量档冒充电压**；" +
			"✅ 已接的：移远 RM520N/RM520F/EM05（机型特例 `voltageCmd`，见 `table.go` 的 Table 与 `glue.go` 的电压候选顺序）、" +
			"广和通/芯讯通/Telit `AT+CBC`/`AT#CBC`（`VoltageQuerier`）、Sierra `AT!PCVOLT?`、" +
			"Thales/Foxconn `AT[!^]PCVOLT?`（`vendor_sierra.sh` / `vendor_thales.sh` / `vendor_foxconn.sh`）；" +
			"没接的型号真机上即使有这条指令，也可以在「接口调试」页手工发 `AT+CBC` 确认后再加特例",
	},
	{
		Capability: "签约速率 AMBR", Status: gapOpen,
		Models: "芯讯通 SIM8200/A8200、广和通 FM160/FM350/FM650",
		Reason: "固件没有可用的查询指令（或应答字段口径没取证）",
		Evidence: "手册与实机多轮探测全 ERROR，见 README 各机型实测段；" +
			"✅ **美格 SRM 已接**（`AT^DSAMBR=<cid>`，NR 布局 f9=UL/f10=DL，见 `ParseMeigDSAMBR`）；" +
			"✅ **有方 N510M 已接**（`AT+C5GQOSRDP`，3GPP 5GS QoS：f2=5QI/QCI、f7=DL_SAMBR / f8=UL_SAMBR，kbit/s，见 `ParseC5GQOSRDP`）；" +
			"✅ **展锐 UMS9620（中兴 F50）已接**（同一条 3GPP `AT+C5GQOSRDP`，2026-10-06 真机取证：" +
			"cid 1=数据 1000/1000 Mbps、5QI=9；cid 11=IMS 300/75、5QI=5，与 RM520 的 `AT+QNWCFG=\"nr5g_ambr\"` 对得上）；" +
			"其它 5G 机型也可试同一条 3GPP 的 `AT+C5GQOSRDP`（本项目**未取证前不发**）",
	},
	{
		Capability: "载波聚合 CA", Status: gapOpen,
		Models: "芯讯通、广和通 MTK/展锐、部分高通",
		Reason: "固件只回 `OK` 无内容，或没有查询指令",
		Evidence: "FM350 的 `AT+GTCAINFO?` 在 SA 下只回 OK；SIM8200 手册无 CAINFO；" +
			"✅ **已达的**：移远 `AT+QCAINFO`、鼎桥 `AT^CASCELLINFO?`/`AT^MONSSC`、Telit `AT#CAINFOEXT?`" +
			"（CA 与服务小区同一条指令，见 F.1 的 CA 列）",
	},
	{
		Capability: "流量计数", Status: gapOpen,
		Models: "广和通 FM350、芯讯通、展锐 UMS9620",
		Reason: "只有**瞬时速率**（`AT+GTSTATIS?`）或什么都没有",
		Evidence: "速率式路径走本地积分，界面标「（估算）」。✅ **美格 SRM 已接速率式**：" +
			"`AT^DSFLOWQRY` → `^DSFLOWRPT: <上行 B/s>,<下行 B/s>`（`ParseDSFlowRpt`，" +
			"并覆盖掉继承自移远的 `AT+QGDCNT=1` 使能指令）",
	},
	{
		Capability: "服务小区 / 邻区", Status: gapDone,
		Models: "Telit / Sierra / Foxconn / Thales / 美格 / 高新兴 / 有方",
		Reason: "✅ **已实现**（三轮）：指令与解析都取自 Modem-Pro `vendor/<vendor>.sh` 的 `cell_info()`",
		Evidence: "Telit `AT#CAINFOEXT?`、Sierra `AT!GSTATUS?`（含 MC735x 无前缀键）、" +
			"Foxconn/Thales `AT[!^]DEBUG`、美格 `AT^CELLINFO=<cid>`、高新兴 `AT+ZCELLINFO?`（lte 与 qualcomm 两种形状）、" +
			"有方 `AT+NETDMSGEX`、openluat `AT+CCED=0,1`/`=0,2`（字段名取自**消费端**）；" +
			"解析在 `module/kernel/vendor_cell_extra.go`（带单测样例）",
	},
	{
		Capability: "邻区（上面这些厂商）", Status: gapNotNeeded,
		Models: "Telit / Sierra / Foxconn / Thales / 美格 / 高新兴 / 有方",
		Reason: "Modem-Pro 的 `vendor/*.sh` 里**除 openluat 外都没有邻区查询**（只有 `cell_info()` 服务小区）" +
			"⇒ 这些厂商拿不到邻区，不编一个指令",
		Evidence: "AT 计划里邻区那行的 `Format` 已注明",
	},
	{
		Capability: "AMBR 的 LTE 布局", Status: gapNotNeeded,
		Models:   "美格 SRM 系列",
		Reason:   "`^DSAMBR` 的 LTE 分支在 Modem-Pro 里是“扫成对的非 0 值取最后一个”，口径含糊",
		Evidence: "**不臆造**：只认已取证的 NR 布局（f9=UL/f10=DL，见 `ParseMeigDSAMBR`），LTE 下返回未上报",
	},
	{
		Capability: "Thales 服务小区指令中文名", Status: gapDone,
		Models:   "MV32-W-A/B",
		Reason:   "✅ 已接（`AT^DEBUG?`，手册 §15.33）",
		Evidence: "`vendor_thales.sh` 的 `cell_info()`",
	},
	{
		Capability: "openluat `+CCED` 字段解析", Status: gapDone,
		Models: "Air724UG",
		Reason: "✅ **已实现**：字段名在 Modem-Pro 的**消费端**（`cell_info()` / `get_neighborcell()` 的 " +
			"`IFS='|' read -r …` 按固定顺序命名），与本项目登记的顺序逐一对应；" +
			"信号换算是脚本写明的公式（RSRP=raw-141、RSRQ=raw*0.5-20）",
		Evidence: "`vendor/openluat.sh`（LTE 服务小区 13 段 / GSM 8 段 / LTE 邻区 9 段 / GSM 邻区 6 段）；" +
			"解析 `ParseOpenluatCCED` / `ParseOpenluatCCEDNeighbours`（`module/kernel/vendor_cell_extra.go`，带单测样例）",
	},
	{
		Capability: "双卡槽 / SIM 槽切换", Status: gapNotNeeded,
		Models:   "移远 `AT+QUIMSLOT`、Sierra `AT!UIMS?`、有方 `AT+SIMCROSS?`、鼎桥 `AT^SIMSLOT?`、中移 `AT+SMSIMCFG?`",
		Reason:   "本项目没有「多卡管理」界面，且单卡场景不触发",
		Evidence: "Modem-Pro `cmds_modem_util.sh` 与各厂商脚本；要做的话是独立功能（选卡/切换/状态）",
	},
	{
		Capability: "Telit 温度传感器全表", Status: gapNotNeeded,
		Models:   "FN990A40",
		Reason:   "只按 `#TEMPSENS: TSENS,<值>` 取 TSENS 那一路（Modem-Pro 也只取这一路）",
		Evidence: "`vendor/telit.sh` 的 `grep \"#TEMPSENS: TSENS,\"`",
	},
	{
		Capability: "Telit / Foxconn / Sierra 温压", Status: gapDone,
		Models: "FN990A40 / T77W* / T99W* / EM919x / MC735x",
		Reason: "✅ 已接（Thales/Foxconn `AT^TEMP`、`AT[!^]TEMP?` 的 `TSENS: 33C`；" +
			"Sierra `AT!PCTEMP?` / `AT[!^]PCVOLT?`）",
		Evidence: "`vendor_thales.sh` §15.13、`vendor_foxconn.sh` `_get_temperature/_get_voltage`、" +
			"`vendor_sierra.sh` 同名函数",
	},
	{
		Capability: "短信「文本模式不回正文」", Status: gapDone,
		Models:   "鼎桥 MT5700、展锐 UMS9620",
		Reason:   "已用 PDU 模式绕过（不是缺功能）",
		Evidence: "见 README 相应机型段",
	},
	{
		Capability: "IMEI 改写", Status: gapNotNeeded,
		Models: "Air724UG、Telit FN990A40、Thales MV32-W-A/B（共 4 个）",
		Reason: "Modem-Pro 的 `cmds/openluat.sh` / `cmds/telit.sh` **只有读**（`AT+CGSN`），没有写指令；" +
			"Thales 的 Modem-Pro 实现明说 \"IMEI modification not supported\"",
		Evidence: "有证据才发指令：没取证的写指令一律不下发（界面按钮置灰 + 说明原因）",
	},
	{
		Capability: "openluat `AT+EEMGINFO?`（LTE 服务信息）", Status: gapNotNeeded,
		Models: "Air724UG",
		Reason: "有取证但**冗余**：`vendor/openluat.sh` 用 `AT+EEMOPT=1` 使能后读 `AT+EEMGINFO?`，" +
			"字段表按**段数**分两套（Air720 22 段 / Air720S 50 段），" +
			"给的是 mcc/mnc/tac/pci/earfcn/band/带宽/cell_id/RSRP/RSRQ/SINR/RSSI/CQI/TX 功率",
		Evidence: "**刻意不接**：这些字段已被 `AT+CCED=0,1`（小区）+ `AT+CSQ`/`AT+CESQ`（信号）全覆盖，" +
			"多一条“使能 + 读取”只会多占总线",
	},
	{
		Capability: "`AT[!^]PCIEMODE?` / `AT+CPCIEMODE?`（USB↔PCIe 形态）", Status: gapNotNeeded,
		Models: "Foxconn T77W*/T99W*、Simcom SIM8xxx",
		Reason: "Modem-Pro 用它判断“当前是 PCIe EP 形态”（`PCIEMODE?` 回 1 时禁用 USB 模式按钮）",
		Evidence: "**暂不接**：本项目按“枚举到的是 USB 还是 PCIe”决定用哪套方言，不必再发一条 AT 去问；" +
			"真机若遇到形态判断错，再把这条接进 USB 页的预检",
	},
	{
		Capability: "华为（巴龙）`AT^CSERSSI?`（EN-DC 的 NR 侧信号）", Status: gapOpen,
		Models: "MH5000 系 / 未识别华为机型",
		Reason: "`vendor_huawei.sh` 的 `cell_info()` “LTE-NR”分支：NR 侧 RSRP/RSRQ/SINR 取 CSERSSI 的 " +
			"**f12/f13/f14**（`awk -F, '{print $12/$13/$14}'`）",
		Evidence: "**未接**：要发“服务小区 + 伴随指令”两条再合并，而 CSERSSI 其余字段没有取证" +
			"（只知三个信号、没有 PCI/频点/TAC）⇒ 单独展示会得到一行“只有信号没有身份”的 NR 小区。" +
			"以后要做需要先拿到 CSERSSI 的完整应答样本",
	},
	{
		Capability: "广和通 `AT+GTUSAGEREC?`（累计用量）", Status: gapNotNeeded,
		Models: "FM150/FM160/FM350 等 14 个",
		Reason: "`+GTUSAGEREC: <值>,<单位码>`（0=B 1=KiB 2=MiB 3=GiB 4=TiB，见 `fibocom_usage_to_bytes`），" +
			"是**不区分上下行的总用量**（Modem-Pro 整份塞进 `total_rx_bytes`、tx 记 0）",
		Evidence: "**未接**：本项目的流量/配额是“上下行分开”的模型（`Rate.Sent/Recv` + `quota.json` 的 up/down），" +
			"拿一个总量去填只能把上行记成 0（显示错）或改配额存档格式 —— 宁可不报。" +
			"线索留在这里：将来做“只看总用量”的展示可直接用",
	},
	{
		Capability: "广和通 `AT+PSRAT?` / Telit `AT#CAMETRICS`（制式）", Status: gapNotNeeded,
		Models: "FM 系 / FN990A40",
		Reason: "Modem-Pro 用它们取**网络类型**（`+PSRAT:` 整串展示；`#CAMETRICS:` 取第 3 字段）",
		Evidence: "**未接（冗余）**：制式在本项目已由服务小区指令给出（`+FENG` / `AT^DEBUG` 的 `RAT:`），" +
			"再发一条纯属重复占总线",
	},
	{
		Capability: "移远 `AT+QGDNRCNT?` / `AT+QAUGDCNT=<n>`（另一种流量计数器）", Status: gapResearch,
		Models: "非展锐平台的移远",
		Reason: "⚠️ **Modem-Pro 自己对上下行的赋值互相矛盾**：`+QGDCNT` 取 $2=rx/$3=tx，" +
			"而同脚本的 `+QGDNRCNT` 取 $2=tx/$3=rx",
		Evidence: "**不接 + 待复核**：本项目 `+QGDCNT: <sent>,<received>`（$1=sent/$2=recv）是 RM502Q-GL " +
			"实机核过的，不采纳 Modem-Pro 的赋值；QGDNRCNT 接了万一是反的，上下行就颠倒",
	},
	{
		Capability: "Sierra `AT!SELRAT` 码表", Status: gapResearch,
		Models: "EM919x / MC735x",
		Reason: "**脚本注释与运行代码不一致**：注释写 04=仅LTE、05=仅5G、0E/0F/10=组合；" +
			"但 `get/set_network_prefer()` 的 case 用的是 **00/01/06/20/11/21/22**",
		Evidence: "本项目按**运行代码**那套实现（06=仅4G、20=仅5G、11=3G+4G、21=4G+5G、22=3G+5G）" +
			"⇒ 以后**别照注释“修正”**（注释是旧的）",
	},
}

// CapabilityGaps 缺口表只读副本（测试/文档用）
func CapabilityGaps() []capabilityGap {
	out := make([]capabilityGap, len(capabilityGaps))
	copy(out, capabilityGaps)
	return out
}

// GapReasonFor 按能力名查"为什么没有"（供 AT 计划给每个未接入能力带上原因）。
//
// 用**子串**匹配：AT 计划里的 Label 是短名（"签约速率 AMBR"），表里可能写成
// "签约速率 AMBR" 或 "AMBR（签约速率）"，两边都要能对上；查不到返回空串。
func GapReasonFor(capability string) string {
	c := strings.TrimSpace(capability)
	if c == "" {
		return ""
	}
	key := strings.ToUpper(c)
	for _, g := range capabilityGaps {
		gk := strings.ToUpper(g.Capability)
		if gk == key || strings.Contains(gk, key) || strings.Contains(key, gk) {
			return string(g.Status) + "：" + g.Reason
		}
	}
	// 再退一步：按关键词（AMBR / CA / ICCID…）粗匹配
	for _, g := range capabilityGaps {
		if kw := gapKeyword(g.Capability); kw != "" && strings.Contains(key, kw) {
			return string(g.Status) + "：" + g.Reason
		}
	}
	return ""
}

// gapKeyword 能力名里的英文关键字（"签约速率 AMBR" → "AMBR"）
func gapKeyword(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			break
		}
	}
	return strings.ToUpper(b.String())
}

// CapabilityGapsMarkdown 缺口表 → Markdown（文档 F.2 的"逐条原因"表由它生成，勿手改）。
func CapabilityGapsMarkdown() string {
	var b strings.Builder
	b.WriteString("| 能力 | 状态 | 哪些型号 | 为什么没有 / 为什么不接 | 证据 / 线索 |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, g := range capabilityGaps {
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n",
			variant.MdCell(g.Capability), variant.MdCell(string(g.Status)), variant.MdCell(g.Models),
			variant.MdCell(g.Reason), variant.MdCell(g.Evidence)))
	}
	return b.String()
}

// CapabilityGapsStats 按状态统计（写入文档开头的一句话）
func CapabilityGapsStats() string {
	n := map[gapStatus]int{}
	for _, g := range capabilityGaps {
		n[g.Status]++
	}
	return fmt.Sprintf("共 %d 条：未接入 %d、刻意不接 %d、已接入（留档）%d、待复核 %d",
		len(capabilityGaps), n[gapOpen], n[gapNotNeeded], n[gapDone], n[gapResearch])
}
