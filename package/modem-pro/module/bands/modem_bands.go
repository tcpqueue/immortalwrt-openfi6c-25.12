package bands

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"modemd/iface"
	"modemd/module/catalog"
	"modemd/module/common"
	"modemd/module/variant"
	"strings"
)

// ====================== 型号 → 支持频段（QModem 频段表） ======================
//
// 数据来源：**QModem 仓库的 `application/qmodem/files/usr/share/qmodem/modem_support.json`**
// （`docs/support_list.md` 是它用 `scripts/update_support_list.py` 生成的**摘要**，
// 只保留 Model/Platform/USB/PCIe 四列 —— **频段列不在那份 md 里**，只在 JSON 里）。
//
// 每个型号四组频段：
//
//	wcdma —— WCDMA/3G 频段号（B1/B5/B8…）
//	lte   —— LTE 频段号（B1/B3/B41…）
//	nr_nsa—— NR **NSA**（EN-DC）频段号（n41/n78/n79…）
//	nr_sa —— NR **SA** 频段号
//
// 为什么值得单独记一份（而不是每次去问模组）：
//  1. **锁频段页面的可选频段**：现在是一个空文本框，用户得自己记得 n78 还是 n41；
//     有了这份表就能把"本型号支持的频段"直接列出来点选。
//  2. **与模组自报的能力互相对照**：锁网状态里的「设备能力」是**运行期读**的
//     （policy_band），固件不报时只能显示 `-`；清单这份是**出厂口径**，两者不一致
//     （比如固件被厂商裁过频段）正是排查时要看的信息。
//  3. **上游缺数据也要如实记**：91 个型号里有一批（32 个）QModem 没写频段 ——
//     这种型号查得到记录但频段为空，界面显示"上游没写"，**不能当成"不支持任何频段"**。
//
// ⚠️ 数据是**快照**（逐字抄，不加工）：取值形态 `1/3/5/8/34/38/39/40/41`、`"0"` = 无；
// 抄写时转成 int 数组（`0` 丢弃）。上游更新后重新生成：`MODEMD_WRITE_BANDS_JSON=1` +
// `go test -run TestZZDumpBandsJSON`（见 `_pdfx/` 里的提取脚本说明），
// 或直接照 `modem_bands.json` 的字段手抄新增型号。

// ModemBands 一个型号的支持频段（四组，均按频段号升序；空 = 上游没写/该制式不支持）
type ModemBands struct {
	Model    string `json:"model"`
	Vendor   string `json:"vendor"`   // 上游 manufacturer（quectel/fibocom/huawei…）
	Platform string `json:"platform"` // 上游 platform（qualcomm/unisoc/mediatek/hisilicon/lte/lte12/asrmicro）
	WCDMA    []int  `json:"wcdma,omitempty"`
	LTE      []int  `json:"lte,omitempty"`
	NRNSA    []int  `json:"nr_nsa,omitempty"` // NR NSA（EN-DC）
	NRSA     []int  `json:"nr_sa,omitempty"`  // NR SA
	// Aliases 同一个型号的**其它固件上报名**（厂家/固件写法不同，比如同一台 F50 可能报
	// `f50-mu300` 也可能报 `QogirN6Pro_PS`）。匹配时与 Model 同权（见 BandsForModel）。
	//
	// ⚠️ 全表必须唯一（包括与别人的 Model 不冲突）——`TestModemBandsSchema` 会查。
	Aliases []string `json:"aliases,omitempty"`
}

// HasBands 上游给这个型号写了频段吗（false = 记录存在但没有频段数据）
func (b ModemBands) HasBands() bool {
	return len(b.WCDMA)+len(b.LTE)+len(b.NRNSA)+len(b.NRSA) > 0
}

// HasNR 是否支持 NR（NSA 或 SA 任一有值）
func (b ModemBands) HasNR() bool { return len(b.NRNSA) > 0 || len(b.NRSA) > 0 }

// SupportedBands 该制式下**可锁**的频段号（升序去重）。
//
//	LTE → lte
//	NR  → nr_nsa ∪ nr_sa（同一家模组这两个列表常常一样，但不假设 —— 取并集才是"支持"）
//	其它（WCDMA / 空）→ wcdma
func (b ModemBands) SupportedBands(tech iface.CellTech) []int {
	switch tech {
	case iface.CellTechLTE:
		return append([]int(nil), b.LTE...)
	case iface.CellTechNR:
		return common.MergeBands(b.NRNSA, b.NRSA)
	case iface.CellTechNRNSA:
		// NSA **只取清单的 nr_nsa 那一栏**（不并 SA）：能分开设的方言才走到这里
		return append([]int(nil), b.NRNSA...)
	}
	return append([]int(nil), b.WCDMA...)
}

// Keys 该制式下界面上显示的频段标签（3GPP 口径：LTE 用 `B1`、NR 用 `n78`、WCDMA 用 `B1`）
func (b ModemBands) Keys(tech iface.CellTech) []string {
	bands := b.SupportedBands(tech)
	out := make([]string, 0, len(bands))
	for _, n := range bands {
		out = append(out, BandKey(tech, n))
	}
	return out
}

// BandKey 频段号 → 3GPP 标签（LTE `B3`、NR `n78`、WCDMA `B1`）
func BandKey(tech iface.CellTech, band int) string {
	if tech == iface.CellTechNR {
		return "n" + fmt.Sprint(band)
	}
	return "B" + fmt.Sprint(band)
}

// Note 给界面/接口用的一句话（含来源与四组数量；上游没写频段时明确说"上游没写"）
func (b ModemBands) Note() string {
	if !b.HasBands() {
		return "Modem-Pro 频段表（modem_support.json）里这个型号没写频段（上游缺数据，不代表不支持）" +
			" —— 以设备自报的「设备能力」为准"
	}
	return fmt.Sprintf("Modem-Pro 频段表（modem_support.json）：LTE %d 个 · NR-SA %d 个 · NR-NSA %d 个 · WCDMA %d 个",
		len(b.LTE), len(b.NRSA), len(b.NRNSA), len(b.WCDMA))
}

// BandsText 四组频段的紧凑展示（`LTE B1 B3 …` / `NR n41 n78`），空的不显示
func (b ModemBands) BandsText() string {
	var parts []string
	if len(b.WCDMA) > 0 {
		parts = append(parts, "WCDMA "+bandLabelList(b.WCDMA, "B"))
	}
	if len(b.LTE) > 0 {
		parts = append(parts, "LTE "+bandLabelList(b.LTE, "B"))
	}
	if len(b.NRNSA) > 0 {
		parts = append(parts, "NR-NSA "+bandLabelList(b.NRNSA, "n"))
	}
	if len(b.NRSA) > 0 {
		parts = append(parts, "NR-SA "+bandLabelList(b.NRSA, "n"))
	}
	return strings.Join(parts, " · ")
}

// ApplyDeclaredBands 把「清单声明支持的频段」贴到某个制式的锁网状态上（锁网状态/接口共用）。
//
// 三种"没有频段"要有三种说法，别都显示成 `-`：
//
//	型号不在频段表里  → 上游没登记这个型号
//	在表里但没写频段  → 上游没写（不代表不支持）
//	在表里有频段、但该制式是空的 → 该制式没有记录（如纯 LTE 老模组没有 NR）
func ApplyDeclaredBands(bl *BandLock, model string) {
	b, ok := BandsForModel(model)
	if !ok {
		bl.DeclaredNote = "Modem-Pro 频段表里没登记这个型号 —— 以设备自报的「设备能力」为准"
		return
	}
	if !b.HasBands() {
		bl.DeclaredNote = b.Note()
		return
	}
	bl.Declared = b.SupportedBands(bl.Tech)
	if len(bl.Declared) == 0 {
		bl.DeclaredNote = fmt.Sprintf("Modem-Pro 频段表里这个型号没有 %s 频段记录", strings.ToUpper(string(bl.Tech)))
		return
	}
	tech := strings.ToUpper(string(bl.Tech))
	switch bl.Tech {
	case iface.CellTechNR:
		tech = "NR（NSA/SA 并集）"
	case iface.CellTechNRNSA:
		tech = "NR-NSA"
	}
	bl.DeclaredNote = fmt.Sprintf("Modem-Pro 频段表（出厂口径）：支持 %d 个 %s 频段，可与上方「设备能力」对照",
		len(bl.Declared), tech)
}

// ====================== 锁频段页面的「按模块支持自动排版」 ======================

// BandGroup 锁频段页面的一组可选频段。
//
// 界面**不用下拉选制式**：本型号支持哪几组（WCDMA / LTE / NR-NSA / NR-SA），
// 就排几组，每组下面把频段列成勾选项（见 `LockStatus.BandGroups`）。
//
//	tech  —— 下发锁定时用哪个技术域（LTE / NR / NRNSA）；空 = 本组不可锁（WCDMA /
//	          一套 NR 列表的方言下的 NSA 组都是这种）
//	label —— 界面摘要里的短名（LTE / NR / NR-NSA / NR-SA / WCDMA）
//	bands —— 本组要展示的频段（清单 ∪ 设备上报，升序去重）
//	declared —— 其中来自清单的部分（界面上可标"出厂口径"）
type BandGroup struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Label    string `json:"label,omitempty"`
	Tech     string `json:"tech"`
	Lockable bool   `json:"lockable"`
	Bands    []int  `json:"bands"`
	Declared []int  `json:"declared,omitempty"`
	Note     string `json:"note,omitempty"`
}

// BandGroups 把本机的支持频段整理成"可勾选的组"，供锁频段页直接渲染。
//
// 规则（"按模块支持自动排版"）：
//
//	① 只出**有频段**的组 —— 纯 LTE 老模组不会出现空的 NR 组；
//	② 频段列表 = 清单（QModem 频段表，即"模块支持的全部频段"）
//	   ∪ 设备能力（capable）∪ **设备当前锁定的频段** ——
//	   三样都要算：清单是"能勾什么"的全集（少了它，取消勾选下发后那个频段就从列表消失），
//	   后两样是"固件实际认什么"（清单没数据、固件又没"能力查询"的机型，只剩当前锁定值可依据）；
//	③ NR **只出一组**：设备侧只有一条锁频段指令、一套 NR 频段列表（清单的 NSA / SA 两栏只是
//	   上游的口径，下发时合并）。以前拆成 NSA / SA 两组时，一边勾另一边跟着变
//	   （用户 2026-10-06 报的"NR·NSA 和 NR·SA 两边同时选上"），看着像 bug —— 一组才与设备一致；
//	④ WCDMA 只展示、不可锁 —— QModem 各厂商脚本里都没有 3G 锁频指令，本项目不编。
func (st *LockStatus) BandGroups() []BandGroup {
	if st == nil {
		return nil
	}
	var declared *ModemBands
	if st.Bands != nil {
		declared = st.Bands
	}
	var (
		lte, nsa, sa, wcdma []int
	)
	if declared != nil {
		lte, nsa, sa, wcdma = declared.LTE, declared.NRNSA, declared.NRSA, declared.WCDMA
	}
	var out []BandGroup
	if len(wcdma) > 0 {
		out = append(out, BandGroup{
			Key: "wcdma", Title: "WCDMA（3G）", Label: "WCDMA", Tech: "", Lockable: false,
			Bands: append([]int(nil), wcdma...), Declared: append([]int(nil), wcdma...),
			Note: "只展示：Modem-Pro 各厂商脚本里都没有 3G 锁频指令，本项目不编",
		})
	}
	if all := common.MergeBands(lte, st.BandLTE.Capable, st.BandLTE.Bands); len(all) > 0 {
		note := ""
		if len(subtractBands(all, lte)) > 0 {
			note = "含设备上报的频段（当前锁定 / 固件自报能力），Modem-Pro 清单里没有这几档"
		}
		out = append(out, BandGroup{
			Key: "lte", Title: "LTE（4G）", Label: "LTE", Tech: string(iface.CellTechLTE), Lockable: true,
			Bands: all, Declared: common.MergeBands(lte), Note: note,
		})
	}
	nrDeclared := common.MergeBands(nsa, sa)
	if st.SeparateNSA {
		// 本机方言的 NR 是**两套**列表（移远/高通 QNWPREFCFG、芯讯通 AT+CSYSSEL）：
		// NSA 与 SA 各一组、**各自独立**勾选与下发（用户 2026-10-06 明确要求）。
		nsaTxt, saTxt := bandLabelList(nsa, "n"), bandLabelList(sa, "n")
		differ := nsaTxt != saTxt
		if all := common.MergeBands(nsa, st.BandNRNSA.Capable, st.BandNRNSA.Bands); len(all) > 0 {
			note := "本机 NR 分 NSA / SA 两套列表，本组只管 NSA"
			if differ && nsaTxt != "" {
				note += "（出厂口径 " + nsaTxt + "）"
			}
			out = append(out, BandGroup{
				Key: "nr_nsa", Title: "NR · NSA（EN-DC）", Label: "NR-NSA",
				Tech: string(iface.CellTechNRNSA), Lockable: true,
				Bands: all, Declared: common.MergeBands(nsa), Note: note,
			})
		}
		if all := common.MergeBands(sa, st.BandNR.Capable, st.BandNR.Bands); len(all) > 0 {
			note := ""
			if differ && saTxt != "" {
				note = "出厂口径 " + saTxt
			}
			out = append(out, BandGroup{
				Key: "nr_sa", Title: "NR · SA", Label: "NR-SA",
				Tech: string(iface.CellTechNR), Lockable: true,
				Bands: all, Declared: common.MergeBands(sa), Note: note,
			})
		}
		return out
	}
	if all := common.MergeBands(nrDeclared, st.BandNR.Capable, st.BandNR.Bands); len(all) > 0 {
		note := "NSA / SA 在设备侧是同一套频段（只有一条锁频段指令）"
		if nsaTxt, saTxt := bandLabelList(nsa, "n"), bandLabelList(sa, "n"); nsaTxt != "" && saTxt != "" && nsaTxt != saTxt {
			note = "NSA " + nsaTxt + " / SA " + saTxt + " ⇒ 设备侧共用一套，这里取并集"
		}
		if len(subtractBands(all, nrDeclared)) > 0 {
			note += "；含设备上报的频段（当前锁定 / 固件自报能力）"
		}
		out = append(out, BandGroup{
			Key: "nr", Title: "NR（5G）", Label: "NR", Tech: string(iface.CellTechNR), Lockable: true,
			Bands: all, Declared: nrDeclared, Note: note,
		})
	}
	return out
}

// subtractBands a 里去掉 b 里有的（升序去重）
func subtractBands(a, b []int) []int {
	if len(a) == 0 {
		return nil
	}
	drop := map[int]bool{}
	for _, n := range b {
		drop[n] = true
	}
	var out []int
	for _, n := range common.MergeBands(a) {
		if !drop[n] {
			out = append(out, n)
		}
	}
	return out
}

// mergeBands 已搬到 modemd/module/common（MergeBands）；kernel 保留同名别名，见 atutil_alias.go。

// bandLabelList 频段号列表 → `B1 B3 B41` / `n41 n78`（带 3GPP 前缀）
func bandLabelList(bands []int, prefix string) string {
	out := make([]string, 0, len(bands))
	for _, n := range bands {
		out = append(out, prefix+fmt.Sprint(n))
	}
	return strings.Join(out, " ")
}

// ---------- 数据（go:embed） ----------

//go:embed modem_bands.json
var ModemBandsJSON []byte

// ModemBandsImpl 全部型号的支持频段（顺序与上游 JSON 一致）
var ModemBandsImpl = mustParseModemBands(ModemBandsJSON)

// mustParseModemBands 解析内置频段表。
//
// 与清单 JSON 同样用**严格解析**（DisallowUnknownFields）：频段数据的键名打错
// （`"lte_bands"` / `"nrsa"`）会被 encoding/json 静默忽略，结果是"这个型号没有 LTE 频段"
// —— 界面照旧能用，但显示的是错的。宁可启动即 panic。
func mustParseModemBands(b []byte) []ModemBands {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var out []ModemBands
	if err := dec.Decode(&out); err != nil {
		panic("module/kernel/modem_bands.json 解析失败：" + err.Error())
	}
	if len(out) == 0 {
		panic("module/kernel/modem_bands.json 里没有任何型号")
	}
	for i := range out {
		out[i].WCDMA = common.MergeBands(out[i].WCDMA)
		out[i].LTE = common.MergeBands(out[i].LTE)
		out[i].NRNSA = common.MergeBands(out[i].NRNSA)
		out[i].NRSA = common.MergeBands(out[i].NRSA)
	}
	return out
}

// modemBandsKeys / modemBandsKeyEntry 型号匹配用的预计算表（与清单同一套规则）。
//
// 每个**别名**也占一行 key，但都指向同一条记录 —— 所以匹配结果要过 modemBandsKeyEntry 换回条目下标。
var (
	modemBandsKeys = func() []string {
		var out []string
		for _, b := range ModemBandsImpl {
			out = append(out, strings.ToUpper(b.Model))
			for _, a := range b.Aliases {
				out = append(out, strings.ToUpper(a))
			}
		}
		return out
	}()
	modemBandsKeyEntry = func() []int {
		var out []int
		for i, b := range ModemBandsImpl {
			out = append(out, i)
			for range b.Aliases {
				out = append(out, i)
			}
		}
		return out
	}()
	modemBandsMatchOrder = catalog.LongestFirstOrder(modemBandsKeys)
)

// BandsForModel 按型号串取支持频段（ATI 原文 / 短名 / QModem 写法 / 别名都认）。
//
//	ok=false      → 型号不在频段表里（既不知道支持什么，也不知道不支持什么）
//	ok=true 且 !HasBands() → 型号在表里，但**上游没写频段**
func BandsForModel(model string) (ModemBands, bool) {
	if i := catalog.MatchModelKey(model, modemBandsKeys, modemBandsMatchOrder); i >= 0 {
		return ModemBandsImpl[modemBandsKeyEntry[i]], true
	}
	return ModemBands{}, false
}

// ModemBandsAll 整张频段表（深拷贝）
func ModemBandsAll() []ModemBands {
	out := make([]ModemBands, len(ModemBandsImpl))
	for i, b := range ModemBandsImpl {
		out[i] = ModemBands{
			Model: b.Model, Vendor: b.Vendor, Platform: b.Platform,
			WCDMA: append([]int(nil), b.WCDMA...), LTE: append([]int(nil), b.LTE...),
			NRNSA: append([]int(nil), b.NRNSA...), NRSA: append([]int(nil), b.NRSA...),
			Aliases: append([]string(nil), b.Aliases...),
		}
	}
	return out
}

// ModemBandsStats 统计：型号总数 / 有频段数据的型号数 / 支持 NR 的型号数
func ModemBandsStats() (models, withBands, withNR int) {
	for _, b := range ModemBandsImpl {
		models++
		if b.HasBands() {
			withBands++
		}
		if b.HasNR() {
			withNR++
		}
	}
	return models, withBands, withNR
}

// ModemBandsMarkdown 频段表 → Markdown（文档 E.4 由它生成，**别手改文档里那张表**）
func ModemBandsMarkdown() string {
	var b strings.Builder
	b.WriteString("| 厂商 | 型号 | 平台 | LTE 频段 | NR SA | NR NSA | WCDMA |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, m := range ModemBandsImpl {
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s | %s |\n",
			variant.MdCell(m.Vendor), variant.MdCell(m.Model), variant.MdCell(m.Platform),
			variant.MdCell(bandCells(m.LTE, "B")), variant.MdCell(bandCells(m.NRSA, "n")),
			variant.MdCell(bandCells(m.NRNSA, "n")), variant.MdCell(bandCells(m.WCDMA, "B"))))
	}
	return b.String()
}

// bandCells 频段列表 → 表格单元（空 = `-`，不让单元格塌掉）
func bandCells(bands []int, prefix string) string {
	if len(bands) == 0 {
		return "-"
	}
	return bandLabelList(bands, prefix)
}
