package kernel

import (
	"fmt"
	bandspkg "modemd/module/bands"
	"modemd/module/catalog"
	"modemd/module/platform"
	"modemd/module/registry"
	"modemd/module/usbdialect"
	"modemd/module/variant"
	"sort"
	"strings"
)

// ====================== 每型号的「AT 指令 + 解析格式」清单 ======================
//
// 目的：把"这张表/这个型号到底发哪几条 AT、由**哪个函数**解析、应答是什么格式"
// 变成**可查询的数据**（API `/api/modules/at` + 界面「支持模块」页），
// 而不是散在十几处代码注释里。
//
// ⚠️ 这张清单**不是手写的愿望表**：每一条的指令都是从**真实实现**里读出来的 ——
//   · 厂商指令集（`ModemCommand`）与可选能力接口（TempQuerier / VoltageQuerier /
//     ICCIDQuerier / CommandCandidates / USBnetConfigurator …）—— 就是设备上真正会发的那条；
//   · 机型特例表（`Quirk`）的 TempCmd / VoltageCmd / ICCIDCmd / ServingCellCmd /
//     NeighbourCellCmd / PDPAddrCmd / SMSReadPDU 覆盖；
//   · 锁网方言（`NetLocker`）、USB 模式方言（`usbDialect`）、IMEI 方言（`imeidialect.ImeiDialect`）、
//     组网模式方言（`modedialect.Dialect`）各自报出的指令。
// 解析格式（Parser / Format 两列）记的是**实际解析函数的落点**，
// 所以"改了指令却忘了改解析"能在页面上直接看出来。
//
// 型号不在支持清单（`modem_catalog.go`）里时也能出计划（按平台/型号谓词推导），
// 只是 `InCatalog=false`。

// ATCmdSpec 一条能力的指令与解析格式
type ATCmdSpec struct {
	Key   string `json:"key"`   // 能力键（serving / neighbour / temp …）
	Label string `json:"label"` // 中文名
	// Cmd 本型号实际会用（或优先尝试）的指令；空 = 本型号不发这条
	Cmd string `json:"cmd,omitempty"`
	// Candidates 运行期候选（按顺序探测，取第一条不被 ERROR 拒的）；
	// 单条指令的机型这里是空的（界面用 Cmd 就够）
	Candidates []string `json:"candidates,omitempty"`
	// Enable 让计数开始工作的指令（如 QGDCNT=1），空 = 不需要
	Enable string `json:"enable_cmd,omitempty"`
	// Parser 解析实现（函数名 + 文件）：这是"解析格式"的落点
	Parser string `json:"parser"`
	// Format 应答格式摘要（字段口径）
	Format string `json:"format,omitempty"`
	// On 本型号是否会发这条（false = 不支持 / 未接入，原因在 Note）
	On bool `json:"on"`
	// Source 指令来源：3GPP 标准 / 厂商指令集 / 机型特例 / 平台方言
	Source string `json:"source,omitempty"`
	Note   string `json:"note,omitempty"`
}

// ModemATPlan 一个型号（或一台在网模组）的 AT 指令面
type ModemATPlan struct {
	Model        string `json:"model"`
	Vendor       string `json:"vendor"`      // 支持清单里的厂商标识
	VendorName   string `json:"vendor_name"` // 中文厂名
	Platform     string `json:"platform"`
	PlatformName string `json:"platform_name"`
	// PlatformVariant 本机命中的「厂商 × 平台」变体（见 vendor_platform.go）。
	// 排查"为什么写出去了错的值"时先看它：与 ATI 型号对不上 = 平台识别要补
	PlatformVariant string `json:"platform_variant,omitempty"`
	// PlatformVariantNote 该变体的依据（哪个 QModem 脚本/手册）
	PlatformVariantNote string `json:"platform_variant_note,omitempty"`
	InCatalog           bool   `json:"in_catalog"` // 型号是否在 support_list 清单里

	// Bands 本型号支持频段（QModem 频段表：LTE / NR-SA / NR-NSA / WCDMA）；nil = 上游没登记
	Bands *bandspkg.ModemBands `json:"bands,omitempty"`
	// BandsNote 频段来源 / 缺数据说明（界面直接显示，含“上游没写”与“不支持”的区别）
	BandsNote string `json:"bands_note,omitempty"`

	CmdImpl     string `json:"cmd_impl"`      // 指令实现（Go 类型）= 设备上真正会用的那套
	CmdImplNote string `json:"cmd_impl_note"` // 为什么是这套（ATI 识别 / 自适应兜底）

	LockDialect string `json:"lock_dialect"`
	USBDialect  string `json:"usb_dialect"`
	IMEDialect  string `json:"imei_dialect"`
	ModeDialect string `json:"mode_dialect"`

	Quirk     string   `json:"quirk,omitempty"` // 命中的机型特例（实测依据）
	USBModes  []string `json:"usb_modes,omitempty"`
	PCIeModes []string `json:"pcie_modes,omitempty"`

	// USBSwitchAT / PCIeSwitchAT 两种形态下"切到某个拨号模式"的 AT 指令。
	// USBModes/PCIeModes 回答"支持哪几档"（QModem support_list），这两个回答"那一档怎么切"
	// （QModem cmds_*.sh + vendor_*.sh，由 files/tools/catalogmodeat 抄进 modem_catalog.json）。
	// 只有说明（未取证/靠驱动）时 Modes 空、Note 里写清为什么。
	USBSwitchAT  *catalog.ModeATSet `json:"usb_switch_at,omitempty"`
	PCIeSwitchAT *catalog.ModeATSet `json:"pcie_switch_at,omitempty"`

	Commands []ATCmdSpec `json:"commands"`
	// Gaps 本型号"没有 / 未接入"的能力摘要（界面直接列出来，不用逐行读 On=false）
	Gaps []string `json:"gaps,omitempty"`
	// GapNotes 与 Gaps **一一对应**的"为什么没有"（来自 capability_gaps.go 的数据表）。
	// 以前只有"没有 AMBR"这种结论，用户看不到"是因为手册里就没有这条指令"，
	// 于是同一个问题会被反复问 / 反复查；现在原因与证据随计划一起下去（界面悬停可见）。
	GapNotes []string `json:"gap_notes,omitempty"`
	// Resolved 在网模组的**运行期实测结果**：哪个功能最终选中了哪条指令
	Resolved map[string]string `json:"resolved,omitempty"`
	// Live=true 表示这份计划来自**在网模组**（指令取自它的真实实现与探测结果）
	Live bool `json:"live,omitempty"`
}

// ATPlanForModel 按型号（ATI 原文 / 短名 / QModem 写法）出 AT 指令面。
func ATPlanForModel(model string) *ModemATPlan {
	e, inCat := catalog.CatalogEntryForModel(model)
	mod := planModemForModel(model, e, inCat)
	return buildATPlan(mod, model, e, inCat, nil)
}

// ATPlanForModem 出**在网模组**的 AT 指令面（真实厂商/实现 + 运行期已选中的指令）。
func ATPlanForModem(mod *Modem) *ModemATPlan {
	if mod == nil {
		return nil
	}
	mod.mu.Lock()
	model, name := mod.Model, mod.ModelName
	mod.mu.Unlock()
	if strings.TrimSpace(model) == "" {
		model = name
	}
	e, inCat := catalog.CatalogEntryForModel(model + " " + name)
	resolved := mod.vendorCmdSnapshot()
	return buildATPlan(mod, model, e, inCat, resolved)
}

// planModemForModel 造一台"离线模组"：只为了读出它的指令（**不发任何 AT**）。
//
// 厂商推导与运行期一致：能靠 ATI 特征串识别的那 6 家（见 DetectVendorFromATI）按厂商取，
// 其余一律 VendorUnknown（自适应指令集 + 候选探测）—— 这样计划里显示的
// 就是设备上真正会走的那套，而不是我们"希望"的那套。
// planModemForModel 造一台"离线模组"：只为了读出它的指令（**不发任何 AT**）。
//
// 厂商推导与运行期一致：能靠 ATI 特征串识别的那 6 家（见 DetectVendorFromATI）按厂商取，
// 其余一律 VendorUnknown（自适应指令集 + 候选探测）—— 这样计划里显示的就是设备上真正会走的那套。
// 「清单厂商串 → 厂商常量」的映射在 modemd/module/platform（见 platform.ModemVendorForCatalog）。
func planModemForModel(model string, e catalog.ModemCatalogEntry, inCat bool) *Modem {
	v := VendorUnknown
	if inCat {
		v = platform.ModemVendorForCatalog(e.Vendor, model)
	}
	return &Modem{Model: model, ModelName: model, Vendor: v, cmdImpl: NewVendorCommand(v)}
}

// buildATPlan 组装计划（mod 可以是离线造的，也可以是真实在网模组）
func buildATPlan(mod *Modem, model string, e catalog.ModemCatalogEntry, inCat bool, resolved map[string]string) *ModemATPlan {
	mc, _ := mod.cmdImpl.(ModemCommand)
	p := &ModemATPlan{
		Model:        strings.TrimSpace(model),
		Platform:     string(platform.PlatformForModel(model)),
		PlatformName: platform.PlatformLabel(platform.PlatformForModel(model)),
		// 厂商 × 平台 变体（取值表档位）：界面/文档展示，也是排查"写错值"的第一线索
		PlatformVariant:     variant.PlatformVariantName(mod.Vendor, model),
		PlatformVariantNote: variant.PlatformVariantNote(mod.Vendor, model),
		InCatalog:           inCat,
		CmdImpl:             atCmdImplName(mod.cmdImpl),
		CmdImplNote:         atCmdImplNote(mod, inCat),
		Resolved:            resolved,
		Live:                resolved != nil,
	}
	if inCat {
		p.Vendor, p.VendorName = e.Vendor, e.VendorLabel()
		p.USBModes, p.PCIeModes = e.USB, e.PCIe
		p.USBSwitchAT, p.PCIeSwitchAT = e.USBModeAT, e.PCIeModeAT
	} else {
		p.Vendor, p.VendorName = string(mod.Vendor), VendorLabel(mod.Vendor)
	}
	if q := mod.Quirk(); q.Match != "" {
		p.Quirk = q.Match + "：" + q.Note
	}
	// 支持频段（QModem 频段表）：锁频段页的"可选频段"就从这里来；上游没登记/没写要如实说明
	if bands, ok := bandspkg.BandsForModel(model); ok {
		b := bands
		p.Bands = &b
		p.BandsNote = bands.Note()
	} else {
		p.BandsNote = "Modem-Pro 频段表里没登记这个型号 —— 以设备自报的「设备能力」为准"
	}
	p.LockDialect = lockDialectName(mod.preferredLocker())
	if d := usbDialectFor(mod); d != nil {
		p.USBDialect = d.Query
	}
	if d := imeiDialectFor(mod); d != nil {
		p.IMEDialect = "AT+CGSN 读 / " + planFirstNonEmpty(d.Set("<IMEI>"), "(不支持改写)") + " 写"
	} else {
		p.IMEDialect = "AT+CGSN 读（不支持改写）"
	}
	if d := modeDialectFor(mod); d != nil {
		p.ModeDialect = d.Query
	}
	p.Commands = atPlanCommands(mod, mc)
	for _, c := range p.Commands {
		if !c.On {
			p.Gaps = append(p.Gaps, c.Label)
		}
	}
	// 缺口原因与 Gaps 同序：查不到就留空串（保持下标对齐，界面按位取用）
	for _, g := range p.Gaps {
		p.GapNotes = append(p.GapNotes, GapReasonFor(g))
	}
	return p
}

// planFirstNonEmpty 取第一个非空串
func planFirstNonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// atCmdImplName 指令实现的短名（去掉包名与指针）。
//
// 「实现类型 → 名字」这张表已搬到 modemd/module/registry（见 names.go）：
// 那是唯一 import 了全部厂商包的注册点，内核不必再按厂商实现类型分派 ——
// 否则「加一家厂商」又得回头改内核。
// atCmdImplName 指令实现的短名（去掉包名与指针）。
//
// 「实现类型 → 名字」这张表已搬到 modemd/module/registry（见 names.go）：
// 那是唯一 import 了全部厂商包的注册点，内核不必再按厂商实现类型分派 ——
// 否则「加一家厂商」又得回头改内核。
func atCmdImplName(impl interface{}) string { return registry.CommandName(impl) }

// atCmdImplNote 为什么用这套指令集
func atCmdImplNote(mod *Modem, inCat bool) string {
	if mod.Vendor == VendorUnknown {
		return "未识别厂商 → 自适应指令集：标准 3GPP 指令直接用，厂商私有指令**运行期候选探测**" +
			"（谁不被 ERROR 拒就用谁，见 modem.go 的 resolveVendorCmd）；巴龙系 ^MONSC/^MONNC 在候选末尾"
	}
	if !inCat {
		return "ATI 特征串识别出的厂商（" + VendorLabel(mod.Vendor) + "），该型号不在 Modem-Pro 支持清单里"
	}
	return "ATI 特征串识别出的厂商（" + VendorLabel(mod.Vendor) + "），指令集按厂商固定"
}

// lockDialectName 锁网方言名（探测/回退选出来的那套；空 = 没有锁网实现）
// lockDialectName 锁网方言名（探测/回退选出来的那套；空 = 没有锁网实现）。
//
// 「方言类型 → 名字 + 指令形态说明」已搬到 modemd/module/registry（见 names.go）。
// lockDialectName 锁网方言名（探测/回退选出来的那套；空 = 没有锁网实现）。
//
// 「方言类型 → 名字 + 指令形态说明」已搬到 modemd/module/registry（见 names.go）。
func lockDialectName(l NetLocker) string { return registry.DialectNote(l) }

// ====================== 文档用：每型号「关键指令」速查表 ======================

// ATPlanMatrixMarkdown 生成"每型号 → 关键指令"速查表。
//
// 与 `modem_catalog.go` 的清单表一样，这张表是**从代码生成的**：
// 改了厂商指令集 / 特例表 / 方言就重新生成（`TestCatalogSyncDoc`），
// 所以 `MD\module_support.md` 的 F 节永远不会和实现脱节。
// 列表格里的 `✘` = 本型号不发这条（原因见 F.2 的未接入清单）。
func ATPlanMatrixMarkdown() string {
	var b strings.Builder
	b.WriteString("| 厂商 | 型号 | 指令集 | 服务小区 | 邻区 | 温度 | 电压 | 流量 | AMBR | CA | ICCID | 锁网方言 | USB 模式方言 | 组网模式 |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, e := range catalog.ModemCatalog {
		p := ATPlanForModel(e.Model)
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			e.Vendor, e.Model, shortCmdImpl(p.CmdImpl),
			planCmdCell(p, "serving"), planCmdCell(p, "neighbour"), planCmdCell(p, "temp"),
			planCmdCell(p, "voltage"), planCmdCell(p, "traffic"), planCmdCell(p, "ambr"),
			planCmdCell(p, "ca"), planCmdCell(p, "iccid"),
			planDialectShort(p.LockDialect), planDialectShort(p.USBDialect), planDialectShort(p.ModeDialect))
	}
	return b.String()
}

// planCmdCell 取某条能力的指令（带反引号），没有则 ✘
func planCmdCell(p *ModemATPlan, key string) string {
	for _, c := range p.Commands {
		if c.Key != key {
			continue
		}
		if !c.On || strings.TrimSpace(c.Cmd) == "" {
			return "✘"
		}
		return "`" + c.Cmd + "`"
	}
	return "✘"
}

// shortCmdImpl 指令集短名（表里要窄）
func shortCmdImpl(s string) string {
	switch {
	case strings.HasPrefix(s, "unknownCommand"):
		return "自适应"
	case s == "":
		return "-"
	}
	return s
}

// planDialectShort 方言列只取"名字"部分（去掉括号里的说明与反引号）
func planDialectShort(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "无"
	}
	if i := strings.IndexAny(s, "（("); i > 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// ATPlanGapsMarkdown 未接入清单（按型号汇总"哪些能力没有"），文档 F.2 用它。
func ATPlanGapsMarkdown() string {
	counts := map[string]int{}
	for _, e := range catalog.ModemCatalog {
		p := ATPlanForModel(e.Model)
		for _, g := range p.Gaps {
			counts[g]++
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	var b strings.Builder
	b.WriteString("| 能力 | 未接入的型号数 | 说明 |\n| --- | --- | --- |\n")
	for _, k := range keys {
		// 说明列直接给出"为什么"的**状态**（数据源 capability_gaps.go），
		// 细节在下一张表：这里有状态就能一眼看出"是没做还是故意不做"。
		note := "见下节（逐条原因 + 证据）"
		if reason := GapReasonFor(k); reason != "" {
			if i := strings.Index(reason, "："); i > 0 {
				note = reason[:i] + "：" + "见下节（逐条原因 + 证据）"
			}
		}
		fmt.Fprintf(&b, "| %s | %d / %d | %s |\n", k, counts[k], len(catalog.ModemCatalog), note)
	}
	return b.String()
}

// ====================== 逐条能力 ======================

// atPlanCommands 组装全部能力行。
//
// 每一条的指令都从**真实实现**读（见文件头注释），所以这里出现什么，设备上就会发什么。
func atPlanCommands(mod *Modem, mc ModemCommand) []ATCmdSpec {
	var out []ATCmdSpec
	add := func(s ATCmdSpec) { out = append(out, s) }

	// ---- 身份 ----
	if mc != nil {
		add(ATCmdSpec{Key: "identity", Label: "型号 / 固件", Cmd: mc.CommandModel(),
			Candidates: []string{mc.CommandFirmware()}, On: true,
			Parser: "query.go 的 shortModelName / labeledVersionValue",
			Format: "ATI 多行「标签: 值」块；AT+CGMR 同上（展锐没有 ATI，靠 CGMR 的 Project/Model 行）"})
	}

	// ---- 服务小区 / 邻区（候选探测） ----
	q0 := mod.Quirk()
	servSrc, nbSrc := "自适应候选探测", "自适应候选探测"
	if len(q0.ServingCellCmds) > 0 {
		servSrc = "机型特例（Quirk.ServingCellCmds）"
	}
	if len(q0.NeighbourCellCmds) > 0 {
		nbSrc = "机型特例（Quirk.NeighbourCellCmds）"
	}
	add(ATCmdSpec{Key: "serving", Label: "服务小区", Source: servSrc,
		Cmd: atFirstCmd(mod, KindServing), Candidates: atCands(mod, KindServing),
		On: atFirstCmd(mod, KindServing) != "",
		Parser: "cell.go 的 ParseCellLine / parseServingCell / parseServingDataLine / ParseCPSICell / ParseGTCCINFO / ParseMONSC；" +
			"厂商私有格式在 vendor_cell_extra.go：ParseDebugCell（Thales/Foxconn AT^DEBUG）/ ParseSierraGStatus（AT!GSTATUS?）/" +
			"ParseMeigCellInfo（美格 ^CELLINFO）/ ParseZCellInfo（高新兴 +ZCELLINFO）/ ParseCAInfoExt（Telit #CAINFOEXT）/" +
			"ParseOpenluatCCED（openluat +CCED=0,1）",
		Format: "AT+QENG=\"servingcell\"（可能拆两行）/ AT+CPSI?（另一套字段序）/ AT+GTCCINFO?（标签行+数据行，FM650 十六进制、FM350 十进制）/ AT^MONSC（巴龙，TAC/PCI 十六进制）"})
	add(ATCmdSpec{Key: "neighbour", Label: "邻区", Source: nbSrc,
		Cmd: atFirstCmd(mod, KindNeighbour), Candidates: atCands(mod, KindNeighbour),
		On: atFirstCmd(mod, KindNeighbour) != "",
		Parser: "cell.go 的 ParseCellLines / ParseMONNCLines / ParseGTCCINFO；" +
			"厂商私有格式在 vendor_cell_extra.go：VendorNeighbourCells → ParseOpenluatCCEDNeighbours（openluat +CCED=0,2，LTE 9 段 / GSM 6 段）",
		Format: "AT+QENG=\"neighbourcell\" / AT^MONNC（一行一个）/ AT+GTCCINFO? 的 neighbor 行 / AT+CCED=0,2（openluat）；" +
			"⚠️ Thales/Foxconn/Sierra/Telit/美格/高新兴的 Modem-Pro 脚本里**没有邻区查询**取证 ⇒ 这些型号实际拿不到邻区"})

	// ---- 信号 ----
	if mc != nil {
		add(ATCmdSpec{Key: "signal", Label: "信号（CSQ/CESQ）", Cmd: mc.CommandCSQ(),
			Candidates: []string{"AT+CESQ"}, On: true,
			Parser: "cell.go 的 ParseCESQ / ParseCESQSignal / ParseServingCellSignal / ParseServingNRSignal",
			Format: "AT+CSQ → +CSQ: <rssi 0..31>,<ber>（换算 -113+2rssi dBm）；AT+CESQ → 6~9 格（广和通 9 格扩展）"})
		add(ATCmdSpec{Key: "netinfo", Label: "网络接入信息", Cmd: mc.CommandNetInfo(),
			On:     strings.TrimSpace(mc.CommandNetInfo()) != "",
			Parser: "net.go 的 ParseQNWINFO / ParseHFREQINFO（鼎桥）",
			Format: "+QNWINFO: \"<act>\",\"<plmn>\",\"<band>\",<arfcn>；认不出时界面显示服务小区指令的原始应答"})
		add(ATCmdSpec{Key: "reg", Label: "注册状态", Cmd: mc.CommandNetworkReg(),
			Candidates: []string{"AT+CGREG?", "AT+C5GREG?"}, On: true,
			Parser: "net.go 的 ParseReg；5GS 兜底在 netinfo 包",
			Format: "AT+CREG? → +CREG: <n>,<stat>；5G SA 下 CREG 常为 0,0，要看 +C5GREG: 0,1"})
		add(ATCmdSpec{Key: "operator", Label: "运营商", Cmd: mc.CommandOperator(),
			On:     strings.TrimSpace(mc.CommandOperator()) != "",
			Parser: "data.go 的 ParseCOPS（当前）/ netscan.go 的 ParseCOPSList（扫描列表）",
			Format: "AT+COPS? → +COPS: <mode>,<format>,\"<name>\",<act>；扫描列表末尾那组 (0,1,2,3,4,5) 不是运营商"})
		// CA 的来源与 QueryCAInfo 完全一致，两层：
		//  ① 厂商 CA 扩展（CAVendorOps）：鼎桥 `AT^CASCELLINFO?`+`AT^MONSSC`、Telit `AT#CAINFOEXT?`；
		//  ② 通用 `CommandCAInfo()`（移远 `AT+QCAINFO`）。
		// ⚠️ 别只写 mc.CommandCAInfo()：Telit / 鼎桥 的 CA 不在这条路上，会显示成"不发"。
		caCmds := []string{}
		caNote := ""
		if ops := caOpsFor(mod); ops != nil {
			caCmds, caNote = ops.CACmds(), ops.CAEmptyNote()
		} else if c := strings.TrimSpace(mc.CommandCAInfo()); c != "" {
			caCmds = []string{c}
		}
		caCmd := ""
		if len(caCmds) > 0 {
			caCmd = caCmds[0]
		}
		add(ATCmdSpec{Key: "ca", Label: "载波聚合 CA", Cmd: caCmd,
			Candidates: caCmds,
			On:         caCmd != "",
			Parser:     "cell.go 的 ParseQCAINFO / tdtech.ParseCASCellInfo / tdtech.ParseMONSSC / ParseCAInfoExt",
			Format: "AT+QCAINFO（LTE/NR 分量载波）；鼎桥另用 AT^CASCELLINFO? / AT^MONSSC；" +
				"Telit 用 AT#CAINFOEXT?（CA 与服务小区同一条指令，PCC-/SCCn- 行）",
			Note: caNote})
	}

	// ---- 温度 / 电压 ----
	q := mod.Quirk()
	tempSrc, voltSrc, iccidSrc := "厂商指令集（TempQuerier）", "厂商指令集（VoltageQuerier）", "3GPP 标准 AT+CCID"
	if q.TempCmd != "" {
		tempSrc = "机型特例（Quirk.TempCmd）"
	}
	if q.VoltageCmd != "" {
		voltSrc = "机型特例（Quirk.VoltageCmd）"
	}
	if q.ICCIDCmd != "" {
		iccidSrc = "机型特例（Quirk.ICCIDCmd）"
	}
	add(ATCmdSpec{Key: "temp", Label: "模组温度", Cmd: mod.tempQueryCommand(), Source: tempSrc,
		On:     strings.TrimSpace(mod.tempQueryCommand()) != "",
		Parser: "sim.go 的 ParseTempResp（依次试 ParseQTEMP / ParseCPMUTEMP / ParseMTSM / ParseGTLADC / ParseGTSTemp / ParseTelitTemp / ParseMeigTemp / ParseThalesTemp / ParseCHIPTEMP）",
		Format: "各家 8 种格式：+QTEMP:\"<名>\",<℃>（另有无名写法）/ +CPMUTEMP: <℃> / +MTSM: <℃> / in_temp_<名>: <1/1000℃> / +GTSENRDTEMP: <n>,<1/1000℃> / #TEMPSENS: TSENS,<℃> / +TEMP: \"<名>\",<℃> / TSENS: <℃>C / ^CHIPTEMP: <0.1℃ 12 路>"})
	add(ATCmdSpec{Key: "voltage", Label: "供电电压", Cmd: mod.voltageCommand(), Source: voltSrc,
		On:     strings.TrimSpace(mod.voltageCommand()) != "",
		Parser: "net.go 的 ParseCBC（三种格式）",
		Format: "AT+CBC / AT#CBC（Telit）→ +CBC: 3.858V（带单位）或 <bcs>,<bcl%>,<mV> 或 <bcs>,<mV>"})

	// ---- 流量 ----
	add(ATCmdSpec{Key: "traffic", Label: "流量计数 / 速率",
		Cmd: atFirstCmd(mod, KindTraffic), Candidates: atCands(mod, KindTraffic),
		Enable: atTrafficEnable(mod),
		On:     atFirstCmd(mod, KindTraffic) != "",
		Parser: "data.go 的 ParseCounters / ParseHexCounters / ParseRates / ParseDSFlowRpt / ParseTraffic",
		Format: "十/十六进制累计字节（AT+QGDCNT? / 鼎桥 AT^DSFLOWQRY）或**瞬时速率**（广和通 AT+GTSTATIS? / 美格 AT^DSFLOWQRY→^DSFLOWRPT，速率式走本地积分，界面标“估算”）"})

	// ---- PDP / APN / AMBR ----
	add(ATCmdSpec{Key: "apn", Label: "APN / 激活的 PDP",
		Cmd: atFirstCmd(mod, KindAPN), Candidates: atCands(mod, KindAPN),
		On:     atFirstCmd(mod, KindAPN) != "",
		Parser: "data.go 的 ParseResp / cgcontrdpLocalAddr（3GPP 的 \"<ip>.<掩码>\" 拼写）/ ParseHandledAPN",
		Format: "AT+CGCONTRDP（不带 cid 列全部；展锐只认带 cid）→ +CGCONTRDP: <cid>,<bearer>,<apn>,<ip>…；退回 AT+CGDCONT?"})
	add(ATCmdSpec{Key: "pdp_addr", Label: "PDP 本机地址",
		Cmd: atFirstCmd(mod, KindPDPAddr), Candidates: atCands(mod, KindPDPAddr),
		On:     atFirstCmd(mod, KindPDPAddr) != "",
		Parser: "data.go 的 ParseCGPADDR",
		Format: "AT+CGPADDR[=<cid>] → +CGPADDR: <cid>,\"<ip4>\"[,\"<ip6>\"]；IPv6 有十进制 16 字节与 ::xx 两种写法"})
	if cmd := mod.ambrCommand(); cmd != "" {
		add(ATCmdSpec{Key: "ambr", Label: "签约速率 AMBR", Cmd: cmd,
			On:     strings.TrimSpace(cmd) != "",
			Parser: "data.go 的 ParseQNWCFG / ParseDSAMBR / parseAMBRFromCGCONTRDP；vendor_at_extra.go 的 ParseMeigDSAMBR（美格）/ ParseC5GQOSRDP（有方）",
			Format: "AT+QNWCFG=\"nr5g_ambr\" → 两行（IMS 与数据 APN，取数据那条），f3/f5 顺带给出 CQI（信道质量 0~15）；或 AT^DSAMBR（鼎桥 <cid>,<DL>,<UL>,\"<apn>\",<QCI> / 美格 NR 用 f9/f10）；或 AT+C5GQOSRDP（3GPP 5GS QoS，f2=5QI/QCI，f7/f8=DL/UL SAMBR，kbps）；或 CGCONTRDP 行尾扩展",
			Note:   "未声明该能力的厂商连指令都不发，界面显示“未上报”；CQI 随移远 QNWCFG 一起给；QCI（5QI）来自有方 N510M 与展锐 UMS9620（中兴 F50）的 3GPP AT+C5GQOSRDP，以及鼎桥 AT^DSAMBR 的末位（逐上下文）；美格 DSAMBR 与移远 QNWCFG 没有这个字段，不猜"})
	} else {
		add(ATCmdSpec{Key: "ambr", Label: "签约速率 AMBR", On: false,
			Parser: "data.go 的 ParseQNWCFG / ParseDSAMBR / parseAMBRFromCGCONTRDP",
			Note:   "本型号的指令集与机型特例都没声明 AMBR 查询"})
	}

	// ---- SIM / 时钟 / 短信 ----
	add(ATCmdSpec{Key: "iccid", Label: "ICCID", Cmd: mod.iccidPlanCommand(), Source: iccidSrc,
		On:     mod.iccidPlanCommand() != "",
		Parser: "sim.go 的 ParseICCID（认 +CCID 与 +ICCID 两种拼写）",
		Format: "AT+CCID / AT+ICCID / AT^ICCID? → +CCID: <20 位数字>"})
	if mc != nil {
		add(ATCmdSpec{Key: "imsi_number", Label: "IMSI / 本机号码",
			Cmd: "AT+CIMI", Candidates: []string{"AT+CNUM", "AT+CRSM(EF_MSISDN)"}, On: true,
			Parser: "sim.go 的 ParseIMSI / ParseCNUM / ParseCPBRNumber / ParseCRSM + DecodeEFMSISDN / BCDDigits",
			Format: "AT+CIMI 裸数字；AT+CNUM → +CNUM: \"\",\"<号>\",<type>；读不到就走 EF_MSISDN 透明文件"})
		add(ATCmdSpec{Key: "clock", Label: "模组时钟", Cmd: "AT+CCLK?", On: true,
			Parser: "sim.go 的 ParseCCLK（Quirk.IgnoreCCLKTZ 决定要不要减时区）",
			Format: "AT+CCLK? → +CCLK: \"yy/mm/dd,hh:mm:ss±zz\"；⚠️ 多家固件带了后缀却报 UTC（特例表明示）"})
	}
	// 短信：读 / 发 两种模式
	add(ATCmdSpec{Key: "sms_read", Label: "短信读取", Cmd: atSMSReadPlan(mod, mc),
		On:     atSMSReadPlan(mod, mc) != "",
		Parser: "sms.go 的 parseSMSCharset / ParsePDUList（PDU 模式）/ parseSMSStorage / parseCPMSSupported",
		Format: "文本：AT+CMGF=1 + AT+CMGL=\"ALL\"（+CMGL 头 + 正文行）；PDU：AT+CMGF=0 + AT+CMGL=4（xxd 语义的十六进制 PDU，自行解码）",
		Note:   atSMSReadNote(mod)})
	add(ATCmdSpec{Key: "sms_send", Label: "短信发送", Cmd: atSMSSendPlan(mod, mc),
		On:     atSMSSendPlan(mod, mc) != "",
		Parser: "sms.go 的 smsSendBody / doSendSMS / sms_pdu_encode.go 的编码（金标准=厂商在 F50 上生成的 3 条 PDU）",
		Format: "文本：AT+CMGS=\"<号码>\" + 正文 + Ctrl+Z；UCS2 下号码要问序试（SIM8200 只收十六进制号码）；PDU：AT+CMGS=<len> + PDU + Ctrl+Z"})

	// ---- 主动扫网 ----
	add(ATCmdSpec{Key: "netscan", Label: "主动扫网", Cmd: "AT+COPS=?",
		Candidates: []string{"AT+CNWSEARCH=\"<lte|nr5g>\"（芯讯通）"}, On: true,
		Parser: "netscan.go 的 ParseCOPSList（运营商）/ cell.go 的 `+LTE_CELL:` / `+NR_NGH_CELL:` 解析",
		Format: "AT+COPS=? 尾部的 (0,1,2,3,4,5) 不是运营商项（靠 looksLikePLMN 过滤）；芯讯通扫小区要 sleep 后回读"})

	// ---- 锁网 ----
	if l := mod.preferredLocker(); l != nil {
		add(ATCmdSpec{Key: "lock_band", Label: "锁频段",
			Cmd: l.CommandQueryBandLock(CellTechLTE), Candidates: atLockCands(l), On: true,
			Parser: "lock.go / lock_*.go 各家 parse（qcfgband 写后回读校验、cnbp/atband 位图、bandlist 频段表）",
			Format: "各家不同：QNWPREFCFG 冒号分隔频段表 / QCFG band 十六进制位图（band N→bit N-1）/ GTACT 位图 / CSYSSEL / ZBAND / BAND_PREF 频段表 / #BND 位图 / *BAND 复合字段"})
		// 组网模式偏好：**优先用 modedialect.Dialect**（界面下拉走的就是它，中文档位 + 本机码），
		// 没有 modedialect.Dialect 的才退回锁网方言里的只读查询 —— 别让页面显示一条"其实不用"的旧指令。
		if d := modeDialectFor(mod); d != nil {
			add(ATCmdSpec{Key: "lock_mode", Label: "组网模式偏好", Cmd: d.Query,
				On:     strings.TrimSpace(d.Query) != "",
				Source: "组网模式方言（vendor_settings_usb.go）",
				Parser: "vendor_settings_usb.go 的 modedialect.Dialect.parse + codeName（语义档位 ↔ 本机码）；`AT+XXX=?` 设备枚举作为补充",
				Format: "中文档位 ↔ 本机码（如 QNWPREFCFG mode_pref=LTE:NR5G、CNMP=71、^SYSCFGEX=08、GTACT=17、!SELRAT=06、WS46=28）"})
		} else {
			add(ATCmdSpec{Key: "lock_mode", Label: "组网模式偏好", Cmd: l.CommandQueryModePref(),
				On:     strings.TrimSpace(l.CommandQueryModePref()) != "",
				Parser: "lock_*.go 的 ParseModePref（只读）",
				Format: "各家码（语义档位 ↔ 本机码的表未取证 ⇒ 只读原码）"})
		}
	}

	// ---- 厂商设置：USB 模式 / IMEI 改写 ----
	if d := usbDialectFor(mod); d != nil {
		add(ATCmdSpec{Key: "usbnet", Label: "USB 网卡模式", Cmd: d.Query,
			Enable:     atUSBSetExample(d),
			Candidates: d.Pre, On: true,
			Parser: "vendor_settings_usb.go 的 d.parse（各家取值表）+ net.go 的 usbnetModes（按支持清单过滤）",
			Format: "各家取值不同（移远 0/1/2/3/5、广和通按平台 32/30/18/24…、芯讯通 USBID、Sierra 接口位图、华为 ^SETMODE…）"})
	}
	// ---- 形态切换指令（按型号；数据源：支持清单的 usb_at / pcie_at）----
	// 只在**该型号有这个形态**时才加这一行：T99W640 根本没有 USB 形态，
	// 给它挂一条"USB 模式切换（未取证）"只会误导。
	if e, ok := catalog.CatalogEntryForModel(mod.Model); ok {
		if e.SupportsUSB() {
			addModeATSpec(add, "usbnet_switch", "USB 模式切换", e.USBModeAT,
				"usb_at（Modem-Pro cmds_*.sh 的切换指令 + vendor_*.sh 的模式 → 参数映射）")
		}
		if e.SupportsPCIe() {
			addModeATSpec(add, "pcie_switch", "PCIe 模式切换", e.PCIeModeAT,
				"pcie_at（Modem-Pro 里只有查询的厂商就只给查询，靠驱动的写清靠驱动）")
		}
	}
	if d := imeiDialectFor(mod); d != nil {
		add(ATCmdSpec{Key: "imei_set", Label: "IMEI 改写", Cmd: d.Set("<15 位>"),
			On:     strings.TrimSpace(d.Set("<15 位>")) != "",
			Parser: "imei.go 的 SetIMEI（下发前备份原始值 → 下发 → 回读 AT+CGSN 确认）/ ParseIMEI",
			Format: "各家不同：AT+EGMR=1,7,\"<imei>\" / AT+SIMEI=<imei> / AT+LCTSN / AT+GTSN / AT+SPIMEI / at^phynum=IMEI,<imei> / AT!nv=550（NV 编码）"})
	} else {
		add(ATCmdSpec{Key: "imei_set", Label: "IMEI 改写", On: false,
			Parser: "imei.go 的 SetIMEI",
			Note:   "本型号未取证到写入指令（Modem-Pro 也没有）⇒ 只读不改"})
	}

	// ---- 其它 ----
	add(ATCmdSpec{Key: "ussd", Label: "USSD", Cmd: "AT+CUSD=1,\"<码>\",15",
		On:     true,
		Parser: "net.go 的 ParseUSSD（展锐需要先按 CSCS 做 hex 编码，见 USSDFormatter）",
		Format: "+CUSD: <n>,\"<正文>\",<dcs>；展锐固件默认 CSCS=HEX，码要 hex 编码后才能发"})
	add(ATCmdSpec{Key: "airplane", Label: "飞行模式", Cmd: "AT+CFUN?",
		Candidates: []string{"AT+CFUN=0", "AT+CFUN=1"}, On: true,
		Parser: "airplane.go 的 ParseCFUN（Quectel 的 4 级/0 级语义差异已处理）",
		Format: "+CFUN: <fun>；开飞行模式后要停主机侧拨号器"})
	if eq, ok := mod.cmdImpl.(ExtraNetInfoQuerier); ok {
		cmd := eq.CommandExtraNetInfo()
		add(ATCmdSpec{Key: "extra_net", Label: "附加网络信息（带宽/MCS/CQI/功率）", Cmd: cmd,
			On:     strings.TrimSpace(cmd) != "",
			Parser: "net.go 的 ParseExtraNetInfo",
			Format: "AT+CNWINFO? → 带宽/MCS/调制/CQI/上行功率/eNB ID/EGCI（芯讯通手册 §4.2.18）"})
	}

	return out
}

// iccidPlanCommand 本型号读 ICCID 实际会用的指令（机型特例 → 厂商能力 → 3GPP 标准）
func (mod *Modem) iccidPlanCommand() string {
	if c := mod.Quirk().ICCIDCmd; c != "" {
		return c
	}
	if iq, ok := mod.cmdImpl.(ICCIDQuerier); ok {
		if c := iq.CommandICCID(); c != "" {
			return c
		}
	}
	return "AT+CCID"
}

// atFirstCmd 候选表的第一条（= 本型号优先发的指令）
//
// ⚠️ 不能复用 atCands：那个函数把"只有一条"的情况会略掉（界面上不需要重复显示），
// 而这里要的就是那一条（否则单指令的型号会全被当成"本型号不发"）。
func atFirstCmd(mod *Modem, kind string) string {
	c := mod.vendorCandidates(kind)
	if len(c) == 0 {
		return ""
	}
	return c[0]
}

// atCands 取候选表（运行期可能选中的全部）
func atCands(mod *Modem, kind string) []string {
	c := mod.vendorCandidates(kind)
	if len(c) <= 1 {
		return nil
	}
	return c
}

// atTrafficEnable 计数开始指令（如 AT+QGDCNT=1）
func atTrafficEnable(mod *Modem) string {
	if cc, ok := mod.cmdImpl.(CommandCandidates); ok {
		return cc.TrafficEnableCommand()
	}
	return ""
}

// atLockCands 锁网的能力查询候选（现场用"固件支持探测"那一栏）
func atLockCands(l NetLocker) []string {
	var out []string
	for _, c := range []string{l.CommandBandLockCaps(), l.CommandCellLockCaps(), l.CommandQueryPolicyBand()} {
		if strings.TrimSpace(c) != "" {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// atUSBSetExample 设置指令的示例（用当前取值表的第一档拼出来，界面展示用）
func atUSBSetExample(d *usbdialect.USBDialect) string {
	if d == nil || d.Set == nil || len(d.Modes) == 0 {
		return ""
	}
	return d.Set(d.Modes[0].Value)
}

// addModeATSpec 加一条"形态切换（USB / PCIe）"能力行。
//
// 三种情况分得很清楚（这是"为什么没有"能被回答的前提）：
//   - 清单一里登记了切换指令      → On=true，Cmd 给一条代表，Candidates 给全部；
//   - 只登记了说明（未取证/靠驱动）→ On=false + Note（文档里逐字来自 modem_catalog.json）；
//   - 清单里根本没这个形态        → On=false + 一句兜底说明。
func addModeATSpec(add func(ATCmdSpec), key, label string, s *catalog.ModeATSet, source string) {
	parser := "module/kernel/modem_catalog.go 的 ModeATSet.ATForMode（清单口径的模式 → 完整指令）"
	if s != nil && s.Known() {
		format := "模式 → 指令：" + s.ModesText()
		if strings.TrimSpace(s.Note) != "" {
			format += "；" + s.Note
		}
		if strings.TrimSpace(s.Query) != "" {
			format = "查询 `" + s.Query + "`；" + format
		}
		add(ATCmdSpec{Key: key, Label: label, Cmd: s.FirstCommand(), Candidates: s.Commands(),
			On: true, Source: source, Parser: parser, Format: format})
		return
	}
	note := "支持清单里没有登记该形态的切换指令"
	if s != nil && strings.TrimSpace(s.Note) != "" {
		note = s.Note
	}
	add(ATCmdSpec{Key: key, Label: label, On: false, Source: source, Parser: parser, Note: note})
}

// atSMSReadPlan 短信读取实际走的模式
func atSMSReadPlan(mod *Modem, mc ModemCommand) string {
	if mod.Quirk().SMSReadPDU {
		return "AT+CMGF=0 + AT+CMGL=4（机型特例标了 SMSReadPDU）"
	}
	if r, ok := mod.cmdImpl.(PDUSMSReader); ok {
		if p, ok2 := mod.cmdImpl.(PDUSMSSender); ok2 && p.CommandSMSModePDU() == r.CommandSMSModePDU() && mc != nil {
			// 收发都只能 PDU（展锐）：读也走 PDU
			return r.CommandSMSModePDU() + " + " + r.CommandReadAllSMSPDU()
		}
		return r.CommandSMSModePDU() + " + " + r.CommandReadAllSMSPDU()
	}
	if mc != nil {
		return mc.CommandSMSTextMode() + " + " + mc.CommandReadAllSMS()
	}
	return ""
}

// atSMSReadNote 读短信的模式说明
func atSMSReadNote(mod *Modem) string {
	if mod.Quirk().SMSReadPDU {
		return "本型号文本模式回的“正文”其实是十六进制 PDU（实测 FM350-GL）⇒ 自动切 PDU 模式读"
	}
	if _, ok := mod.cmdImpl.(PDUSMSReader); ok {
		return "本固件文本模式不回正文（鼎桥 MT5700 / 展锐 UMS9620）⇒ 走厂商声明的 PDU 读法"
	}
	return "标准文本模式（3GPP TS 27.005）"
}

// atSMSSendPlan 短信发送实际走的方式
func atSMSSendPlan(mod *Modem, mc ModemCommand) string {
	if p, ok := mod.cmdImpl.(PDUSMSSender); ok {
		return p.CommandSMSModePDU() + " + " + p.CommandSendSMSPDULength(0)
	}
	if mc != nil {
		return mc.CommandSendSMS("<号码>") + " + 正文 + Ctrl+Z"
	}
	return ""
}
