package catalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"modemd/iface"
	"modemd/module/platform"
	"sort"
	"strings"
)

// ====================== 模块支持清单（USB 模式 / PCIe 模式） ======================
//
// 数据来源：**QModem `docs/support_list.md` 全文**（github.com/FUjr/QModem，
// 原文已落地在 `_pdfx/qmodem/docs_support_list.md`，逐行抄录，不做增删）。
//
// 为什么要有这张表：QModem 的支持列表是"**按型号**"写的 ——
//   · USB 列：该型号在 USB 形态下**固件支持哪几种数据协议**（qmi/gobinet/ecm/mbim/rndis/ncm/rmnet/auto）；
//   · PCIe 列：该型号**有没有 PCIe（MHI / wwan）形态**，以及 PCIe 形态支持哪几种（qmi/gobinet/mbim/rmnet）。
//
// 本项目的厂商方言表（`vendor_settings_usb.go`）只解决"**同一条 AT 怎么写**"，
// 解决不了"这个型号到底支不支持这一档" —— 例如：
//   - 移远的通用取值表里有 NCM（值 5），但 EP06 / EM05 / EC25 这一代**没有 ncm**；
//   - 广和通 MTK 平台（FM350 系）只有 rndis，展锐 L610 是 ecm/rndis/ncm，高通 FM160 是六种全有；
//   - Foxconn T99W640 **完全没有 USB 形态**（只有 PCIe mbim）。
//
// 所以这张表是"**按型号**驱动 USB / PCIe 能力"的唯一清单（single source of truth）：
//
//	① USB 网卡模式下拉只列本型号支持的模式（`usbnetModes` → `CatalogUSBModesFor`）；
//	② PCIe 卡片按型号给出支持的模式 + 内核驱动提示（`PCIeInfoFor` / `ListPCIModemDevices`）；
//	③ 打包时整张表写进版本目录的 `支持机型.md`（由本包 OpenWrt Makefile 完成）；
//	④ 文档 `MD/module_support.md` 的 C 节与它逐行对照（`modem_catalog_test.go` 守着，漏一块就报错）。
//
// ⚠️ 清单里的**模式取值一律小写**（qmi/gobinet/ecm/mbim/rndis/ncm/rmnet/auto），
// 与 QModem 原文一致；中文名见 `CatalogKindLabel`。
//
// ⚠️ 认不出型号时（不在清单里）**不猜**：退回厂商方言的取值表（旧行为），
// 只在界面上说明"本型号不在支持清单里"。

// 清单里的模式取值（= QModem `support_list.md` 的小写写法）
const (
	CatModeQMI     = "qmi"     // 高通原生协议（拨号器 / MHI 都走它）
	CatModeGobiNet = "gobinet" // 高通 GobiNet 驱动（QMI 的另一种主机侧形态）
	CatModeECM     = "ecm"     // USB 以太网（CDC-ECM，Linux 免驱）
	CatModeMBIM    = "mbim"    // 微软标准（Windows 推荐）
	CatModeRNDIS   = "rndis"   // 经典 USB 网卡
	CatModeNCM     = "ncm"     // 高速 USB 网络（CDC-NCM）
	CatModeRMNet   = "rmnet"   // 高通 RMNet（Sierra / Foxconn 的口径）
	CatModeAuto    = "auto"    // 固件自动选择（芯讯通旧款 4G 有这一档）
	// 下面两档 QModem 支持列表里**没有出现过**，但它们出现在本项目的厂商取值表里：
	// 高新兴 `AT+ZSWITCH=p`（EAP）、华为展锐平台 `AT^SETMODE=8`（PPP）。
	// 登记它们的目的：清单口径要能"认得出来并该删就删"（否则认不出的档会被无条件保留）。
	CatModeEAP = "eap"
	CatModePPP = "ppp"
)

// CatalogKindLabel 模式取值 → 界面/文档里的显示名
var CatalogKindLabel = map[string]string{
	CatModeQMI:     "QMI",
	CatModeGobiNet: "GobiNet",
	CatModeECM:     "ECM",
	CatModeMBIM:    "MBIM",
	CatModeRNDIS:   "RNDIS",
	CatModeNCM:     "NCM",
	CatModeRMNet:   "RMNet",
	CatModeAuto:    "自动",
	CatModeEAP:     "EAP",
	CatModePPP:     "PPP",
}

// CatalogVendorLabel 厂商标识 → 中文名（清单/文档用）
var CatalogVendorLabel = map[string]string{
	"openluat": "合宙 openluat",
	"huawei":   "华为 Huawei",
	"quectel":  "移远 Quectel",
	"fibocom":  "广和通 Fibocom",
	"meig":     "美格 MeiG",
	"sierra":   "Sierra Wireless",
	"telit":    "Telit",
	"simcom":   "芯讯通 SIMCom",
	"foxconn":  "富士康 Foxconn",
	"thales":   "Thales",
	"neoway":   "有方 Neoway",
	"gosuncn":  "高新兴 Gosuncn",
}

// CatalogVendorOrder 厂商在清单/文档里的顺序（= QModem support_list.md 的排列）
var CatalogVendorOrder = []string{
	"openluat", "huawei", "quectel", "fibocom", "meig",
	"sierra", "telit", "simcom", "foxconn", "thales", "neoway", "gosuncn",
}

// ModemCatalogEntry 清单里的一行：一个型号在 USB / PCIe 两种形态下支持的模式。
type ModemCatalogEntry struct {
	Vendor   string   `json:"vendor"`         // 厂商标识（quectel/fibocom/…）
	Model    string   `json:"model"`          // 型号（QModem 支持列表的写法，小写）
	Platform string   `json:"platform"`       // QModem 平台列原文（qualcomm/unisoc/mediatek/hisilicon/lte/lte12/asrmicro）
	USB      []string `json:"usb,omitempty"`  // USB 形态支持的数据协议；空 = 该型号没有 USB 形态
	PCIe     []string `json:"pcie,omitempty"` // PCIe 形态支持的数据协议；空 = 不支持 PCIe
	// USBModeAT / PCIeModeAT 两种形态下"切到某个拨号模式"的 AT 指令（数据源：QModem 的指令脚本）。
	// 支持列表只说"支持哪几档"，这里补的是"那一档到底怎么切" —— 见 ModeATSet。
	USBModeAT  *ModeATSet `json:"usb_at,omitempty"`
	PCIeModeAT *ModeATSet `json:"pcie_at,omitempty"`
}

// ModeATSet 一种传输形态（USB / PCIe）切换"拨号（数据）模式"用的 AT 指令。
//
// 数据源同样是 QModem，但取自它的**指令脚本**而不是支持列表：
//
//	application/qmodem/files/usr/share/qmodem/cmds/<vendor>.sh    指令本体（cmd_* 函数里的 at "$1" "..."）
//	application/qmodem/files/usr/share/qmodem/vendor/<vendor>.sh  模式名 ↔ 参数值 的映射（get_mode / set_mode）
//
// 例（移远高通平台 USB）：Query = `AT+QCFG="usbnet"`，
// Modes = {"qmi": `AT+QCFG="usbnet",0`, "ecm": `AT+QCFG="usbnet",1`, …}。
//
// ⚠️ 只收"**有出处**"的内容（生成工具见 files/tools/catalogmodeat）：
//   - QModem 没给该平台的 set 分支（会落到它脚本里的默认值）⇒ Modes 空 + Note 说明，**不猜**参数；
//   - QModem 里被注释掉的那行（gobinet、MTK 平台的 mbim）⇒ 不收；
//   - 大多数厂商**根本没有** PCIe 形态的 AT 切换（只能查询，或者靠驱动）⇒ Modes 空 + Note；
//   - Note 一律写清"为什么没有"，界面/文档照抄 —— 免得用户以为是本项目漏了。
//
// ⚠️ Modes 的键是**清单口径**（qmi/ecm/mbim/rndis/ncm/rmnet/auto，见 CatMode* 常量），
// 与 mode.Name 的对应关系走 CatalogKindOfModeName；值是**完整**指令（可直接下发）。
type ModeATSet struct {
	Query string            `json:"query,omitempty"` // 查询当前模式 / 该形态切换用的指令（不带参数）
	Modes map[string]string `json:"modes,omitempty"` // 拨号模式 → 完整切换指令
	Note  string            `json:"note,omitempty"`  // 未取证 / 只支持查询 / 靠驱动 等说明
}

// ATForMode 取切到某个模式（清单口径，如 "ecm"）的完整 AT 指令；没有登记返回空串
func (s *ModeATSet) ATForMode(kind string) string {
	if s == nil {
		return ""
	}
	return s.Modes[strings.ToLower(strings.TrimSpace(kind))]
}

// Known 有没有任何可下发内容（只有 Note 的条目不算"能切"）
func (s *ModeATSet) Known() bool { return s != nil && len(s.Modes) > 0 }

// Commands 全部切换指令，按模式名升序（**稳定顺序**：界面/文档/测试都用它）
func (s *ModeATSet) Commands() []string {
	if s == nil || len(s.Modes) == 0 {
		return nil
	}
	keys := make([]string, 0, len(s.Modes))
	for k := range s.Modes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, s.Modes[k])
	}
	return out
}

// FirstCommand 取一条代表性的切换指令（模式名排序的第一个）；没有返回空串
func (s *ModeATSet) FirstCommand() string {
	if c := s.Commands(); len(c) > 0 {
		return c[0]
	}
	return ""
}

// ModesText 模式 → 指令 的一行摘要（按模式名排序，文档/界面直接用）
func (s *ModeATSet) ModesText() string {
	if s == nil || len(s.Modes) == 0 {
		return ""
	}
	keys := make([]string, 0, len(s.Modes))
	for k := range s.Modes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"=`"+s.Modes[k]+"`")
	}
	return strings.Join(parts, "、")
}

// SupportsUSB 有没有 USB 形态（Foxconn T99W640 是唯一一个没有的）
func (e ModemCatalogEntry) SupportsUSB() bool { return len(e.USB) > 0 }

// SupportsPCIe 有没有 PCIe（MHI / wwan）形态
func (e ModemCatalogEntry) SupportsPCIe() bool { return len(e.PCIe) > 0 }

// USBCommandFor / PCIeCommandFor 切到某个拨号模式的完整 AT 指令（没有登记返回空串）
func (e ModemCatalogEntry) USBCommandFor(kind string) string  { return e.USBModeAT.ATForMode(kind) }
func (e ModemCatalogEntry) PCIeCommandFor(kind string) string { return e.PCIeModeAT.ATForMode(kind) }

// VendorLabel 中文厂名（认不出就原样返回标识）
func (e ModemCatalogEntry) VendorLabel() string {
	if s, ok := CatalogVendorLabel[e.Vendor]; ok {
		return s
	}
	return e.Vendor
}

// Title 显示名，如 "移远 Quectel RM520N-GL"
func (e ModemCatalogEntry) Title() string { return e.VendorLabel() + " " + strings.ToUpper(e.Model) }

// USBText / PCIeText 支持的模式串（空 = ✘）
func (e ModemCatalogEntry) USBText() string  { return CatalogKindsText(e.USB) }
func (e ModemCatalogEntry) PCIeText() string { return CatalogKindsText(e.PCIe) }

// CatalogKindsText 模式列表 → "ecm,mbim,rndis,ncm"；空 → "✘"（与 QModem 支持列表的写法一致）
func CatalogKindsText(kinds []string) string {
	if len(kinds) == 0 {
		return "✘"
	}
	return strings.Join(kinds, ",")
}

// ---- 清单里复用的模式组合（**只读**，绝不要原地修改） ----

// ---- 清单数据：module/kernel/modem_catalog.json（go:embed，**单一数据源**） ----
//
// 为什么把 91 行数据挪出 .go：
//
//	① 加一个型号是**改数据**，不该动代码 —— 挪出去以后 diff 就是一条 JSON 对象，
//	   不会在 Go 里留下一堆只被引用一次的组合变量（catUSBxxx / catPCIexx）。
//	② 数据文件能被别的工具直接读（打包脚本、前端、外部脚本），不必再 grep Go 源码。
//	③ 结构约束交给测试盯（`modem_catalog_json_test.go`）：厂商、平台、模式取值都有白名单，
//	   手改 JSON 写错一个字母会当场变红，而不是运行期"认不出型号就退回厂商取值表猜"。
//
// ⚠️ 模式取值一律**小写**（见 CatMode* 常量）；`usb` / `pcie` 为空数组或省略 = 该形态不支持
// （QModem `support_list.md` 原文里的 ✘）。顺序**必须与 QModem 支持列表一致**（按厂商分组）。
//
//go:embed modem_catalog.json
var ModemCatalogJSON []byte

// ModemCatalog 全部型号的支持清单（由上面的内置 JSON 解析而来）
var ModemCatalog = MustParseModemCatalog(ModemCatalogJSON)

// MustParseModemCatalog 解析内置清单。JSON 是编译期就打进二进制里的，
// 坏了没有"降级"可言 —— 直接 panic（比"清单悄悄变空"好：那种表现是界面里没有任何型号）。
//
// ⚠️ 用 DisallowUnknownFields：JSON 数据的**唯一**风险是键名打错（`"Usb"` / `"models"`），
// 而 encoding/json 默认会**静默忽略**不认识的键 —— 那会导致"某型号没有 USB 模式"这种
// 看不出来的错。严格解析把这类错变成启动即 panic。
func MustParseModemCatalog(b []byte) []ModemCatalogEntry {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var out []ModemCatalogEntry
	if err := dec.Decode(&out); err != nil {
		panic("module/kernel/modem_catalog.json 解析失败：" + err.Error())
	}
	if len(out) == 0 {
		panic("module/kernel/modem_catalog.json 里没有任何型号")
	}
	return out
}

// CatalogPCIEVIDVendor PCI VID → 清单厂商标识（只有**厂商独占**的 VID 才列；
// 高通 17cb / 联发科 14c3 这些芯片厂 VID 不在这里，见 CatalogPCIEVIDPlatform）
var CatalogPCIEVIDVendor = map[string]string{
	"2c7c": "quectel",
	"2cb7": "fibocom",
	"1508": "fibocom",
	"1e0e": "simcom",
	"2dee": "meig",
	"105b": "foxconn",
}

// CatalogPCIEVIDPlatform 芯片厂 VID → QModem 平台列（用它筛"可能是哪些 PCIe 机型"）
var CatalogPCIEVIDPlatform = map[string]string{
	"17cb": "qualcomm",
	"05c6": "qualcomm",
	"14c3": "mediatek",
	"0e8d": "mediatek",
}

// ====================== 查询 ======================

// CatalogMatchOrder 型号匹配顺序：**长 token 优先**。
//
// 关键（踩过的坑）：`em12` 是 `em120k` 的前缀、`em05` 是 `em05-cn` 的前缀、
// `fm350-gl` 是 `fm350-gl-00` 的前缀 —— 先命中短的会把平台/模式都认错。
var CatalogModelKeys = func() []string {
	out := make([]string, len(ModemCatalog))
	for i, e := range ModemCatalog {
		out[i] = strings.ToUpper(e.Model)
	}
	return out
}()

var CatalogMatchOrder = LongestFirstOrder(CatalogModelKeys)

// LongestFirstOrder keys 的下标按"串更长优先"排序（稳定排序，等长保持原顺序）。
//
// 型号匹配是**多张表共用**的规则（支持清单 `modem_catalog.json`、频段表 `modem_bands.json`），
// 所以抽在这里 —— 两张表必须用同一套匹配，否则会出现"清单里查得到、频段查不到"的怪事。
func LongestFirstOrder(keys []string) []int {
	idx := make([]int, len(keys))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return len(keys[idx[a]]) > len(keys[idx[b]])
	})
	return idx
}

// MatchModelKey 在 keys（**已大写**的型号串）里按公共规则找命中的下标；-1 = 没命中。
//
// 两遍匹配：
//
//	① 原样（带 `-`/`_`，如 "RM520N-GL"）—— ≤4 字符的短 token 要求**词边界**（S10 vs S100）；
//	② 去掉分隔符后再试（ATI 常见连写：`RM520NGLAAR03A03M4G`、`SIMCOMSIM8200EAM2`），
//	   ≥5 字符才允许连写匹配。
func MatchModelKey(model string, keys []string, order []int) int {
	u := strings.ToUpper(strings.TrimSpace(model))
	if u == "" {
		return -1
	}
	for _, i := range order {
		if CatalogTokenHit(u, keys[i]) {
			return i
		}
	}
	flat := CatalogFlatten(u)
	for _, i := range order {
		tok := CatalogFlatten(keys[i])
		if len(tok) >= 5 && strings.Contains(flat, tok) {
			return i
		}
	}
	return -1
}

// CatalogEntryForModel 按型号串（ATI 原文 / 短名 / QModem 写法）取清单条目。
//
// 匹配规则见 MatchModelKey（与频段表 `BandsForModel` 完全一致）。
func CatalogEntryForModel(model string) (ModemCatalogEntry, bool) {
	if i := MatchModelKey(model, CatalogModelKeys, CatalogMatchOrder); i >= 0 {
		return ModemCatalog[i], true
	}
	return ModemCatalogEntry{}, false
}

// CatalogTokenHit token 是否出现在 u（已大写）里；≤4 字符要求词边界
func CatalogTokenHit(u, tok string) bool {
	if tok == "" {
		return false
	}
	if len(tok) <= 4 {
		return platform.ModelTokenAtWord(u, tok)
	}
	return strings.Contains(u, tok)
}

// CatalogFlatten 只留字母数字（去掉 `-` `_` 空格）
func CatalogFlatten(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32)
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// CatalogEntries 整张清单（深拷贝，按 QModem 支持列表顺序）
func CatalogEntries() []ModemCatalogEntry {
	out := make([]ModemCatalogEntry, len(ModemCatalog))
	for i, e := range ModemCatalog {
		e.USB = append([]string(nil), e.USB...)
		e.PCIe = append([]string(nil), e.PCIe...)
		out[i] = e
	}
	return out
}

// CatalogUSBKindsForModel 本型号 USB 形态支持的模式（第二返回值 = 型号在清单里）。
//
//	known=false → 不在清单里（调用方应退回厂商方言的取值表，别当成"不支持"）
//	known=true, kinds 为空 → 清单明确写了"没有 USB 形态"（如 T99W640）
func CatalogUSBKindsForModel(model string) ([]string, bool) {
	e, ok := CatalogEntryForModel(model)
	if !ok {
		return nil, false
	}
	return append([]string(nil), e.USB...), true
}

// CatalogPCIeKindsForModel 本型号 PCIe 形态支持的模式（第二个返回值 = 型号在清单里）
func CatalogPCIeKindsForModel(model string) ([]string, bool) {
	e, ok := CatalogEntryForModel(model)
	if !ok {
		return nil, false
	}
	return append([]string(nil), e.PCIe...), true
}

// CatalogSupportsPCIe 本型号在清单里是否标了 PCIe 形态（型号不在清单里 → false）
func CatalogSupportsPCIe(model string) bool {
	e, ok := CatalogEntryForModel(model)
	return ok && e.SupportsPCIe()
}

// CatalogSupportsUSB 本型号在清单里是否标了 USB 形态（型号不在清单里 → false）
func CatalogSupportsUSB(model string) bool {
	e, ok := CatalogEntryForModel(model)
	return ok && e.SupportsUSB()
}

// CatalogNoteForModel 一句话说明本型号在支持清单里的模式（型号不在清单里 → 空串）
func CatalogNoteForModel(model string) string {
	e, ok := CatalogEntryForModel(model)
	if !ok {
		return ""
	}
	return fmt.Sprintf("Modem-Pro 支持列表（docs/support_list.md）：%s · 平台 %s · USB %s · PCIe %s",
		strings.ToUpper(e.Model), e.Platform, e.USBText(), e.PCIeText())
}

// CatalogPlatformFamily 把 QModem 平台列归到"芯片家族"
// （`lte` / `lte12` 是旧款 4G 平台的口径，归高通；`asrmicro` → `asr`）。
func CatalogPlatformFamily(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "qualcomm", "lte", "lte12":
		return "qualcomm"
	case "mediatek", "mtk":
		return "mediatek"
	case "asrmicro", "asr":
		return "asr"
	}
	return strings.ToLower(strings.TrimSpace(p))
}

// CatalogPCIeModelsForVID 由 PCI VID 推"可能是哪些**支持 PCIe** 的机型"（提示用，不做判定）。
//
// 厂商独占 VID 直接按厂商筛；芯片厂 VID（17cb 高通 / 14c3 联发科）按 QModem 平台列筛
// （`lte` / `lte12` 也归高通 —— 它们是高通的老平台口径）。
func CatalogPCIeModelsForVID(vid string) []string {
	v := strings.ToLower(strings.TrimSpace(vid))
	v = strings.TrimPrefix(v, "0x")
	var want func(ModemCatalogEntry) bool
	if vendor, ok := CatalogPCIEVIDVendor[v]; ok {
		want = func(e ModemCatalogEntry) bool { return e.Vendor == vendor && e.SupportsPCIe() }
	} else if fam, ok := CatalogPCIEVIDPlatform[v]; ok {
		want = func(e ModemCatalogEntry) bool {
			return CatalogPlatformFamily(e.Platform) == fam && e.SupportsPCIe()
		}
	} else {
		return nil
	}
	var out []string
	for _, e := range ModemCatalog {
		if want(e) {
			out = append(out, e.Model)
		}
	}
	return out
}

// CatalogPCIeModelsForVendor 某个厂商名下**支持 PCIe** 的机型（型号写法，按清单顺序）
func CatalogPCIeModelsForVendor(vendor string) []string {
	var out []string
	for _, e := range ModemCatalog {
		if e.Vendor == vendor && e.SupportsPCIe() {
			out = append(out, e.Model)
		}
	}
	return out
}

// CatalogStats 统计：型号数 / 有 USB 的型号数 / 有 PCIe 的型号数 / 厂商数
func CatalogStats() (models, usbModels, pcieModels, vendors int) {
	seen := map[string]bool{}
	for _, e := range ModemCatalog {
		models++
		if e.SupportsUSB() {
			usbModels++
		}
		if e.SupportsPCIe() {
			pcieModels++
		}
		seen[e.Vendor] = true
	}
	return models, usbModels, pcieModels, len(seen)
}

// ====================== 模式取值 ↔ 清单口径 ======================

// CatalogKindOfModeName 把本项目界面上的模式名（Mode.Name，如 "QMI/NDIS"、"RMNet"、"ECM"）
// 翻成清单口径（qmi/rmnet/ecm…）；认不出返回空串（= 本档不参与按型号过滤）。
func CatalogKindOfModeName(name string) string {
	u := strings.ToUpper(strings.TrimSpace(name))
	if u == "" {
		return ""
	}
	// 先认**展示名原文**（CatalogKindLabel 的反查）：界面/接口回传的可能是 "自动" 这种中文名，
	// 而下面那串 contains 判断只认英文写法。
	for k, label := range CatalogKindLabel {
		if strings.EqualFold(label, u) {
			return k
		}
	}
	switch {
	case strings.Contains(u, "GOBINET"):
		return CatModeGobiNet
	case strings.Contains(u, "QMI"):
		return CatModeQMI
	case strings.Contains(u, "RMNET"):
		return CatModeRMNet
	case strings.Contains(u, "MBIM"):
		return CatModeMBIM
	case strings.Contains(u, "RNDIS"):
		return CatModeRNDIS
	case strings.Contains(u, "NCM"):
		return CatModeNCM
	case strings.Contains(u, "ECM"):
		return CatModeECM
	case strings.Contains(u, "AUTO"):
		return CatModeAuto
	case strings.Contains(u, "EAP"):
		return CatModeEAP
	case strings.Contains(u, "PPP"):
		return CatModePPP
	}
	return ""
}

// CatalogKindSupported 这一档（按名字）是否在清单给的模式里
func CatalogKindSupported(kinds []string, modeName string) bool {
	k := CatalogKindOfModeName(modeName)
	if k == "" {
		// 认不出的档位（各家固件自定义的写法）：**保留** ——
		// 清单是按 QModem 的口径写的，不能拿它去删自己根本不认识的档。
		// （认得出的（qmi/ecm/…/eap/ppp）一律按清单判，见 CatalogKindOfModeName。）
		return true
	}
	return CatalogKindInList(kinds, k)
}

// CatalogKindInList kinds 里有没有这一档
func CatalogKindInList(kinds []string, kind string) bool {
	for _, want := range kinds {
		if want == kind {
			return true
		}
	}
	return false
}

// CatalogUSBModesFor 按清单过滤本机的 USB 模式取值表。
//
// 返回值：
//
//	modes 过滤后的取值表（型号不在清单里 → 原样返回；清单写了"没有 USB" → 返回 nil）
//	note  追加说明（型号不在清单里 → 空串）
func CatalogUSBModesFor(model string, modes []iface.Mode) ([]iface.Mode, string) {
	e, ok := CatalogEntryForModel(model)
	if !ok {
		return modes, ""
	}
	note := "按 Modem-Pro 支持列表（docs/support_list.md）过滤：本型号 USB 支持 " +
		CatalogKindsText(e.USB) + "、PCIe " + e.PCIeText() + "。"
	if !e.SupportsUSB() {
		return nil, note + "该型号**只有 PCIe 形态**（USB 列就是 ✘），USB 侧不提供数据协议。"
	}
	if len(modes) == 0 {
		return modes, note
	}
	out := make([]iface.Mode, 0, len(modes))
	for _, m := range modes {
		if CatalogKindSupported(e.USB, m.Name) {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		// 取值表里的档位与清单口径一个都对不上（厂商自定义写法）→ 不删，原样给出
		return modes, note
	}
	return out, note
}

// ====================== 文档 / 打包用输出 ======================

// CatalogMarkdown 生成清单表格（5 列，**与 MD\module_support.md 的 C 节、打包出的
// 支持机型.md 里的「支持模块」表逐字一致**）。
func CatalogMarkdown() string {
	var b strings.Builder
	b.WriteString("| 厂商 | 型号 | 平台 | USB 模式 | PCIe 模式 |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, e := range ModemCatalog {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
			e.Vendor, e.Model, e.Platform, CatalogKindsText(e.USB), CatalogKindsText(e.PCIe))
	}
	return b.String()
}

// CatalogProgressMarkdown 生成"补齐进度"表（按厂商汇总；每块补完就打勾）。
func CatalogProgressMarkdown() string {
	var b strings.Builder
	b.WriteString("| 厂商 | 型号数 | 其中支持 PCIe | 状态 |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, v := range CatalogVendorOrder {
		n, p := 0, 0
		for _, e := range ModemCatalog {
			if e.Vendor != v {
				continue
			}
			n++
			if e.SupportsPCIe() {
				p++
			}
		}
		fmt.Fprintf(&b, "| %s | %d | %d | ✅ 已完成（USB 模式 + PCIe 模式已接入代码） |\n",
			v, n, p)
	}
	models, usbModels, pcieModels, vendors := CatalogStats()
	fmt.Fprintf(&b, "| **合计** | **%d**（%d 个厂商） | **%d** | ✅ 全部完成（有 USB 形态 %d 个） |\n",
		models, vendors, pcieModels, usbModels)
	return b.String()
}

// CatalogSwitchMarkdown 生成"USB / PCIe 拨号模式切换指令"方言表（文档 C.3 用它）。
//
// 一个型号一行会太吵（91 行、每行好几条长指令），所以**指令完全相同**的型号合并成一行：
// 合并的粒度是「厂商 + 平台 + 指令签名」，富士康那种"前缀随型号变"的自然会分成两行
// （t99w640/t99w368 走 AT+，其余走 AT^）。
func CatalogSwitchMarkdown() string {
	type group struct {
		vendor, platform string
		usbQuery         string
		usbModes         string
		pcieQuery        string
		note             string
		models           []string
	}
	var order []string
	byKey := map[string]*group{}
	for _, e := range ModemCatalog {
		sig := e.Vendor + "|" + e.Platform
		if e.USBModeAT != nil {
			sig += "|" + e.USBModeAT.Query + "|" + e.USBModeAT.ModesText()
		}
		if e.PCIeModeAT != nil {
			sig += "|" + e.PCIeModeAT.Query
		}
		g, ok := byKey[sig]
		if !ok {
			g = &group{vendor: e.Vendor, platform: e.Platform}
			if e.USBModeAT != nil {
				g.usbQuery, g.usbModes = e.USBModeAT.Query, e.USBModeAT.ModesText()
			}
			if e.PCIeModeAT != nil {
				g.pcieQuery = e.PCIeModeAT.Query
			}
			byKey[sig] = g
			order = append(order, sig)
		}
		g.models = append(g.models, e.Model)
		// 说明（去重）：USB 与 PCIe 两侧的 Note 合到一列，避免表格再宽两列
		for _, n := range []string{ModelATNote(e.USBModeAT), ModelATNote(e.PCIeModeAT)} {
			if n == "" || strings.Contains(g.note, n) {
				continue
			}
			if g.note != "" {
				g.note += "；"
			}
			g.note += n
		}
	}
	// 厂商顺序按清单（= QModem 支持列表）的排列，保证文档稳定
	vendorRank := map[string]int{}
	for i, v := range CatalogVendorOrder {
		vendorRank[v] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := byKey[order[i]], byKey[order[j]]
		if vendorRank[a.vendor] != vendorRank[b.vendor] {
			return vendorRank[a.vendor] < vendorRank[b.vendor]
		}
		if a.platform != b.platform {
			return a.platform < b.platform
		}
		if a.usbQuery != b.usbQuery {
			return a.usbQuery < b.usbQuery
		}
		return a.usbModes < b.usbModes
	})

	var b strings.Builder
	b.WriteString("| 厂商 | 平台 | 型号 | USB 查询 | USB：模式 → 切换指令 | PCIe 查询 | 说明 |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, sig := range order {
		g := byKey[sig]
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s |\n",
			g.vendor, PlanDashIfEmpty(g.platform),
			PlanModelListText(g.models),
			PlanCodeOrDash(g.usbQuery), PlanDashIfEmpty(g.usbModes),
			PlanCodeOrDash(g.pcieQuery), PlanDashIfEmpty(g.note))
	}
	return b.String()
}

// ModelATNote 取一条 ModeATSet 的说明（nil 安全）
func ModelATNote(s *ModeATSet) string {
	if s == nil {
		return ""
	}
	return s.Note
}

// PlanCodeOrDash 有内容就套反引号（指令），没有给 —
func PlanCodeOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return "`" + s + "`"
}

// PlanDashIfEmpty 空串给 —（避免表格单元格空白）
func PlanDashIfEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// PlanModelListText 型号列：全列（超过 6 个就缩写成"前 2 个 等 N 个"）
func PlanModelListText(models []string) string {
	if len(models) == 0 {
		return "—"
	}
	if len(models) <= 6 {
		return strings.Join(models, "、")
	}
	return models[0] + "、" + models[1] + " 等 " + fmt.Sprint(len(models)) + " 个"
}
