package kernel

import (
	"modemd/module/bands"
	"modemd/module/catalog"
	"modemd/module/platform"
	"modemd/module/usbdialect"
	"os"
	"strings"
	"testing"
)

// ====================== 模块支持清单（USB / PCIe 模式）的回归测试 ======================
//
// 这些测试守三件事：
//  ① 支撑清单**每个型号**都能在 `modem_catalog.go` 里查到，且平台归属与 `PlatformForModel` 一致；
//  ② 清单里的模式取值都是登记过的（qmi/gobinet/ecm/mbim/rndis/ncm/rmnet/auto），没有手滑写的字符串；
//  ③ `MD\module_support.md` 的 C 节表格与代码**逐行一致**（补一块就记录一块，漏了直接报错）。

// catalogPlatformExpect QModem 平台列 → 本项目 ModemPlatform。
//
// `lte` / `lte12` 是 QModem 对**旧款 4G 平台**的写法（高通 MDM9x07 / SDX12 那一代），
// 本项目把这一代归到 `PlatformQualcomm`（依据见 modem_platform.go 的 quectelLegacyLTEModels 注释）。
var catalogPlatformExpect = map[string]platform.ModemPlatform{
	"qualcomm":  platform.PlatformQualcomm,
	"lte":       platform.PlatformQualcomm,
	"lte12":     platform.PlatformQualcomm,
	"unisoc":    platform.PlatformUnisoc,
	"mediatek":  platform.PlatformMediaTek,
	"hisilicon": platform.PlatformHiSilicon,
	"asrmicro":  platform.PlatformASR,
}

// catalogPlatformUnknownOK 平台**故意不猜**的型号（QModem 只给了 "lte"，芯片平台没把握）：
// 与 modem_platform_test.go 的 noFallbackOK、以及支持清单测试里的登记保持一致。
var catalogPlatformUnknownOK = map[string]string{
	"a7605c":   "芯讯通 4G（QModem 只标 \"lte\"）：芯片平台与方言都没把握，不猜",
	"me3630-w": "高新兴 ME3630（QModem 只标 \"lte\"）：平台不猜，锁网走 isGosuncnModel 的 ZBAND 谓词",
}

// TestCatalogCoversSupportList 支持清单（QModem docs/support_list.md）里的**每一个型号**
// 都要在 modem_catalog.go 里登记 —— 少一个就报错（这正是"补一块记录一块"的自动检查）。
func TestCatalogCoversSupportList(t *testing.T) {
	for _, c := range supportedListModels {
		e, ok := catalog.CatalogEntryForModel(c.model)
		if !ok {
			t.Errorf("%s/%s：模块支持清单里没有这个型号（补 module/kernel/modem_catalog.json）",
				c.vendor, c.model)
			continue
		}
		if e.Vendor != c.vendor {
			t.Errorf("%s/%s：清单里记的厂商是 %q，不一致", c.vendor, c.model, e.Vendor)
		}
	}
}

// TestCatalogPlatformAgreesWithPlatformForModel 清单里的型号 → 平台归属必须与
// PlatformForModel（ATI 原文走的那个函数）一致，否则"按平台选方言"会在真机上走错分支。
func TestCatalogPlatformAgreesWithPlatformForModel(t *testing.T) {
	for _, e := range catalog.CatalogEntries() {
		want, ok := catalogPlatformExpect[e.Platform]
		if !ok {
			t.Errorf("%s：清单里的平台列 %q 没登记（补 catalogPlatformExpect）", e.Model, e.Platform)
			continue
		}
		got := platform.PlatformForModel(e.Model)
		if got == platform.PlatformUnknown {
			if why, ok := catalogPlatformUnknownOK[e.Model]; ok {
				_ = why
				continue
			}
			t.Errorf("%s：PlatformForModel 认不出平台（清单写的是 %q）—— 补 platformModelTokens", e.Model, e.Platform)
			continue
		}
		if got != want {
			t.Errorf("%s：PlatformForModel = %q，清单平台 %q 期望 %q", e.Model, got, e.Platform, want)
		}
	}
}

// TestCatalogModesAreKnown 清单里的模式取值必须是登记过的那 8 个（防手滑写 "qmi "、"ncmn" 这类）。
func TestCatalogModesAreKnown(t *testing.T) {
	known := map[string]bool{
		catalog.CatModeQMI: true, catalog.CatModeGobiNet: true, catalog.CatModeECM: true, catalog.CatModeMBIM: true,
		catalog.CatModeRNDIS: true, catalog.CatModeNCM: true, catalog.CatModeRMNet: true, catalog.CatModeAuto: true,
	}
	for _, e := range catalog.CatalogEntries() {
		for _, m := range append(append([]string{}, e.USB...), e.PCIe...) {
			if !known[m] {
				t.Errorf("%s：清单里的模式 %q 不认识", e.Model, m)
			}
		}
		if e.Platform == "" {
			t.Errorf("%s：平台列为空", e.Model)
		}
	}
}

// TestCatalogStats 清单统计（文档里的"合计"就是它）。
func TestCatalogStats(t *testing.T) {
	models, usbModels, pcieModels, vendors := catalog.CatalogStats()
	if models != len(catalog.ModemCatalog) || models == 0 {
		t.Fatalf("型号数 = %d，modemCatalog = %d", models, len(catalog.ModemCatalog))
	}
	if vendors != len(catalog.CatalogVendorOrder) {
		t.Errorf("厂商数 = %d，catalogVendorOrder = %d", vendors, len(catalog.CatalogVendorOrder))
	}
	if usbModels != models-1 {
		// 唯一没有 USB 形态的是 Foxconn T99W640（QModem 支持列表的 USB 列就是 ✘）
		t.Errorf("有 USB 形态的 = %d，期望 %d（= 全部 - T99W640）", usbModels, models-1)
	}
	if pcieModels == 0 || pcieModels >= models {
		t.Errorf("有 PCIe 形态的 = %d（既不该为 0，也不该等于全部）", pcieModels)
	}
}

// TestCatalogLookupEdgeCases 型号匹配的坑：前缀互相包含、短名撞车、连写写法。
func TestCatalogLookupEdgeCases(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// em12 是 em120k 的前缀：必须命中最长的那个
		{"Quectel EM12", "em12"},
		{"EM120K", "em120k"},
		{"EC200A", "ec200a"},
		// em05 / em05-cn
		{"EM05", "em05"},
		{"EM05-CN", "em05-cn"},
		// fm350-gl / fm350-gl-00 / fm350r-gl
		{"FM350-GL", "fm350-gl"},
		{"FM350-GL-00", "fm350-gl-00"},
		{"FM350R-GL", "fm350r-gl"},
		// ATI 原文常把型号连写（去掉 `-` 也要认）
		{"RM520NGLAAR03A03M4G", "rm520n-gl"},
		{"SIMCOMSIM8200EAM2", "simcom_sim8200ea-m2"},
		{"RM500UCNV", "rm500u-cnv"},
		// 华为 S10 只有 3 个字符：必须按词边界，别把 S100 认成它
		{"HUAWEI S10", "s10"},
		{"S100", ""},
		{"MS10", ""},
		// 不在清单里的型号：不猜
		{"ZTE UMS9620", ""},
		{"", ""},
	}
	for _, c := range cases {
		e, ok := catalog.CatalogEntryForModel(c.in)
		got := ""
		if ok {
			got = e.Model
		}
		if got != c.want {
			t.Errorf("CatalogEntryForModel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCatalogUSBModeFilter USB 取值表按型号过滤：
//
//	EP06（QModem: qmi,ecm,mbim,rndis）→ 通用表里的 NCM 要被去掉；
//	RM500U-CNV（unisoc: ecm,mbim,rndis,ncm）→ QMI/NDIS 要被去掉；
//	T99W640（USB ✘）→ 一个都不留；
//	不在清单里的型号 → 原样返回（退回旧行为，不猜）。
func TestCatalogUSBModeFilter(t *testing.T) {
	generic := usbdialect.Modes() // QMI/NDIS 0 / ECM 1 / MBIM 2 / RNDIS 3 / NCM 5

	names := func(modes []Mode) string {
		var out []string
		for _, m := range modes {
			out = append(out, m.Name)
		}
		return strings.Join(out, "/")
	}

	got, note := catalog.CatalogUSBModesFor("EP06", generic)
	if names(got) != "QMI/NDIS/ECM/MBIM/RNDIS" {
		t.Errorf("EP06 过滤后 = %s（NCM 应被去掉）", names(got))
	}
	if !strings.Contains(note, "qmi,ecm,mbim,rndis") {
		t.Errorf("EP06 说明里应带上清单原文，实得：%s", note)
	}

	got, _ = catalog.CatalogUSBModesFor("RM500U-CNV", generic)
	if names(got) != "ECM/MBIM/RNDIS/NCM" {
		t.Errorf("RM500U-CNV 过滤后 = %s（QMI/NDIS 应被去掉）", names(got))
	}

	got, note = catalog.CatalogUSBModesFor("T99W640", generic)
	if len(got) != 0 || !strings.Contains(note, "只有 PCIe 形态") {
		t.Errorf("T99W640 应一个模式都不留并说明原因，实得 %s / %s", names(got), note)
	}

	got, note = catalog.CatalogUSBModesFor("ZTE UMS9620", generic)
	if names(got) != names(generic) || note != "" {
		t.Errorf("不在清单里的型号要原样返回、不加说明，实得 %s / %s", names(got), note)
	}
}

// TestCatalogKindOfModeName 界面模式名 → 清单口径（各家写法都要认得）。
func TestCatalogKindOfModeName(t *testing.T) {
	cases := map[string]string{
		"QMI/NDIS":   catalog.CatModeQMI, // 移远通用表的写法
		"QMI":        catalog.CatModeQMI,
		"ECM":        catalog.CatModeECM,
		"MBIM":       catalog.CatModeMBIM,
		"RNDIS":      catalog.CatModeRNDIS,
		"NCM":        catalog.CatModeNCM,
		"RMNet":      catalog.CatModeRMNet, // Sierra / Foxconn 的写法
		"GobiNet":    catalog.CatModeGobiNet,
		"AUTO":       catalog.CatModeAuto, // 芯讯通旧款 4G
		"EAP":        catalog.CatModeEAP,  // 高新兴自定义档（清单里没有 → 不在清单的型号会被过滤掉）
		"PPP":        catalog.CatModePPP,  // 华为展锐平台
		"MODESWITCH": "",                  // 真的认不出的写法 → 不参与过滤（保留）
		"":           "",
	}
	for in, want := range cases {
		if got := catalog.CatalogKindOfModeName(in); got != want {
			t.Errorf("CatalogKindOfModeName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCatalogPCIeModelsForVID 由 PCI VID 推"可能是哪些支持 PCIe 的机型"。
func TestCatalogPCIeModelsForVID(t *testing.T) {
	// 厂商独占 VID：只列该厂商、且**支持 PCIe** 的型号
	quectel := catalog.CatalogPCIeModelsForVID("0x2c7c")
	if !catalogContains(quectel, "rm520n-gl") {
		t.Errorf("2c7c 应含 rm520n-gl，实得 %v", quectel)
	}
	if catalogContains(quectel, "rm520n-cn") {
		t.Errorf("2c7c 不该含 rm520n-cn（QModem 支持列表里它没有 PCIe）：%v", quectel)
	}
	fibocom := catalog.CatalogPCIeModelsForVID("2cb7")
	if !catalogContains(fibocom, "fm350-gl") || catalogContains(fibocom, "fm650-cn") {
		t.Errorf("2cb7 应含 fm350-gl、不含 fm650-cn，实得 %v", fibocom)
	}
	simcom := catalog.CatalogPCIeModelsForVID("1e0e")
	if len(simcom) != 1 || simcom[0] != "simcom_sim8380g-m2" {
		t.Errorf("1e0e 应只有 simcom_sim8380g-m2，实得 %v", simcom)
	}
	// 芯片厂 VID：按 QModem 平台列筛
	qc := catalog.CatalogPCIeModelsForVID("17cb")
	if len(qc) < 10 {
		t.Errorf("17cb（高通 MHI）应能列出十几款 PCIe 机型，实得 %d", len(qc))
	}
	mtk := catalog.CatalogPCIeModelsForVID("14c3")
	if !catalogContains(mtk, "fm350-gl") || catalogContains(mtk, "fm160-eau") {
		t.Errorf("14c3（联发科 T7xx）只该列 MTK 平台，实得 %v", mtk)
	}
	if got := catalog.CatalogPCIeModelsForVID("ffff"); got != nil {
		t.Errorf("未知 VID 应返回 nil，实得 %v", got)
	}
}

func catalogContains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ====================== 形态切换指令（usb_at / pcie_at） ======================

// TestCatalogUSBCommandFor 按模式取切换指令（USB 网卡模式面板/拨号流程用的就是它）。
//
// 三个关键点各取一个真实型号钉住：
//   - 移远高通：一条 `AT+QCFG="usbnet",<n>` 覆盖全部档位；PCIe 侧未取证 ⇒ 空；
//   - 富士康：**前缀随型号变**（t99w368 走 AT+、t99w175 走 AT^）—— 抄错前缀就是发出去被拒；
//   - T99W640：根本没有 USB 形态 ⇒ 一条都没有（不能给它造出切换指令来）。
func TestCatalogUSBCommandFor(t *testing.T) {
	q, ok := catalog.CatalogEntryForModel("RM520N-GL")
	if !ok {
		t.Fatal("RM520N-GL 不在清单里")
	}
	if got := q.USBCommandFor("ecm"); got != `AT+QCFG="usbnet",1` {
		t.Errorf("移远 ECM 的切换指令不对：%q", got)
	}
	if got := q.USBCommandFor("ECM"); got != `AT+QCFG="usbnet",1` {
		t.Errorf("模式名应该大小写不敏感：%q", got)
	}
	if got := q.PCIeCommandFor("mbim"); got != "" {
		t.Errorf("移远的 PCIe 切换指令未取证，应该是空的，实际 %q", got)
	}
	if q.PCIeModeAT == nil || !strings.Contains(q.PCIeModeAT.Note, "PCIe") {
		t.Errorf("PCIe 侧未取证也要留说明：%+v", q.PCIeModeAT)
	}

	t368, _ := catalog.CatalogEntryForModel("T99W368")
	t175, _ := catalog.CatalogEntryForModel("T99W175")
	if got := t368.USBCommandFor("mbim"); got != "AT+USBSWITCH=90d5" {
		t.Errorf("T99W368 的前缀应该是 AT+：%q", got)
	}
	if got := t175.USBCommandFor("mbim"); got != "AT^USBSWITCH=90d5" {
		t.Errorf("T99W175 的前缀应该是 AT^：%q", got)
	}
	if got := t368.PCIeModeAT.Query; got != "AT+PCIEMODE?" {
		t.Errorf("T99W368 的 PCIe 查询指令不对：%q", got)
	}

	t640, ok := catalog.CatalogEntryForModel("T99W640")
	if !ok {
		t.Fatal("T99W640 不在清单里")
	}
	if t640.USBModeAT != nil || t640.USBCommandFor("mbim") != "" {
		t.Errorf("T99W640 没有 USB 形态，不该有 usb_at：%+v", t640.USBModeAT)
	}

	// 参数未取证：只有查询 + 说明，一条指令都没有（**不猜**）
	rg, _ := catalog.CatalogEntryForModel("RG801")
	if rg.USBModeAT == nil || rg.USBModeAT.Known() {
		t.Fatalf("rg801 的 USB 参数未取证，不该有指令：%+v", rg.USBModeAT)
	}
	if rg.USBModeAT.Query != `AT+QCFG="usbnet"` || !strings.Contains(rg.USBModeAT.Note, "hisilicon") {
		t.Errorf("rg801 应该给查询指令 + 写明 why：%+v", rg.USBModeAT)
	}
}

// TestCatalogSwitchMarkdown 文档 E.4 的表格：表头、关键指令、列数都要对。
func TestCatalogSwitchMarkdown(t *testing.T) {
	md := catalog.CatalogSwitchMarkdown()
	if !strings.HasPrefix(md, "| 厂商 | 平台 | 型号 | USB 查询 | USB：模式 → 切换指令 | PCIe 查询 | 说明 |") {
		t.Fatalf("表头不对：\n%s", firstLines(md, 2))
	}
	for _, want := range []string{
		"`AT+QCFG=\"usbnet\"`",           // 移远查询
		`AT+QCFG="usbnet",1`,             // 移远 ecm
		"`AT^SETMODE?`",                  // 华为查询
		"ncm=`AT^SETMODE=4`",             // 华为 hisilicon 档位
		"AT+GTUSBMODE=40",                // 广和通展锐 mbim
		"AT+ZSWITCH=x",                   // 高新兴（**字符**参数，别被"规范化"成数字）
		"AT!USBCOMP=1,4,0x00001009",      // Sierra 接口位图
		"AT+CUSBCFG=usbid,1e0e,9001",     // 芯讯通 USBID
		"AT$MYCONFIG=\"USBNETMODE\",2,1", // 芯讯通 lte 自动档
		"AT+SER=3,1",                     // 美格 rndis
		"`AT^PCIEMODE?`",                 // 富士康 PCIe 查询
		"`AT+CPCIEMODE?`",                // 芯讯通 PCIe 查询（EP）
		"AT+SETCONFIG=0",                 // Thales：USB 数据面 MBIM
		"mtk_pcie",                       // 广和通 MTK：靠驱动的说明
	} {
		if !strings.Contains(md, want) {
			t.Errorf("表里应该有 %q", want)
		}
	}
	// 富士康两种前缀都要出现（前缀随型号）—— 至少两条 USBSWITCH 行
	if n := strings.Count(md, "USBSWITCH=90d5"); n < 2 {
		t.Errorf("富士康 AT+ 与 AT^ 两种前缀都该有，实际只出现 %d 次", n)
	}
	// 列数：每行 8 个 |（单元格里不会出现 |）
	for i, line := range strings.Split(md, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if n := strings.Count(line, "|"); n != 8 {
			t.Errorf("第 %d 行有 %d 个 |（应为 8，说明列没对齐）：%s", i+1, n, line)
		}
	}
}

// firstLines 取前 n 行（报错信息用）
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// ====================== 文档同步（补一块就记录一块） ======================

const (
	catalogDocPath = "../../MD/module_support.md"
)

var catalogDocBlocks = []struct {
	name  string
	begin string
	end   string
	body  func() string
}{
	{"清单表格", "<!-- catalog-table:begin -->", "<!-- catalog-table:end -->", catalog.CatalogMarkdown},
	{"补齐进度", "<!-- catalog-progress:begin -->", "<!-- catalog-progress:end -->", catalog.CatalogProgressMarkdown},
	{"每型号指令速查", "<!-- atplan-matrix:begin -->", "<!-- atplan-matrix:end -->", ATPlanMatrixMarkdown},
	{"未接入能力汇总", "<!-- atplan-gaps:begin -->", "<!-- atplan-gaps:end -->", ATPlanGapsMarkdown},
	// "为什么没有"的表：数据源 module/kernel/capability_gaps.go（原来是手写在文档里的散文表，
	// 改代码时没人会想起来去改它 ⇒ 搬进代码，由这条测试钉住）
	{"能力缺口原因", "<!-- atplan-reasons:begin -->", "<!-- atplan-reasons:end -->", CapabilityGapsMarkdown},
	// 型号 → 支持频段：数据源 module/kernel/modem_bands.json（上游 QModem 频段表快照，见 files/tools/banddump）
	{"型号支持频段", "<!-- bands-table:begin -->", "<!-- bands-table:end -->", bands.ModemBandsMarkdown},
	// 型号 → USB/PCIe 拨号模式切换指令：数据源 module/kernel/modem_catalog.json 的 usb_at / pcie_at
	// （QModem cmds_*.sh 的指令 + vendor_*.sh 的模式 → 参数映射，生成工具 files/tools/catalogmodeat）
	{"形态切换指令", "<!-- catalog-switch:begin -->", "<!-- catalog-switch:end -->", catalog.CatalogSwitchMarkdown},
}

// TestCatalogSyncDoc `MD\module_support.md` 的 C 节必须与 modem_catalog.json **逐行一致**。
//
// 补一个型号（改 modemCatalog）后如果忘了更新文档，这条测试会直接失败，
// 并告诉你怎么重新生成 —— 这就是"补一块就记录一块"的落地方式。
//
// 重新生成（在 5g-modem-manager 目录下）：
//
//	$env:MODEMD_WRITE_CATALOG_DOC="1"; go test -C . ./module/kernel/ -run TestCatalogSyncDoc -count=1
func TestCatalogSyncDoc(t *testing.T) {
	raw, err := os.ReadFile(catalogDocPath)
	if err != nil {
		t.Skipf("跳过：读不到 %s（%v）", catalogDocPath, err)
	}
	src := string(raw)
	nl := "\n"
	if strings.Contains(src, "\r\n") {
		nl = "\r\n"
	}
	want := src
	for _, b := range catalogDocBlocks {
		body := strings.ReplaceAll(strings.TrimRight(b.body(), "\n"), "\n", nl)
		next, ok := replaceCatalogBlock(want, b.begin, b.end, body, nl)
		if !ok {
			t.Fatalf("文档里找不到 %s / %s 这对标记（%s）", b.begin, b.end, b.name)
		}
		want = next
	}
	if want == src {
		return
	}
	if os.Getenv("MODEMD_WRITE_CATALOG_DOC") == "1" {
		if err := os.WriteFile(catalogDocPath, []byte(want), 0o644); err != nil {
			t.Fatalf("写回文档失败：%v", err)
		}
		t.Logf("已把清单表/补齐进度同步进 %s", catalogDocPath)
		return
	}
	t.Fatalf("%s 的「清单表格 / 补齐进度」与 modem_catalog.json 不一致。\n"+
		"在 5g-modem-manager 目录下重新生成：\n"+
		"  $env:MODEMD_WRITE_CATALOG_DOC=\"1\"; go test -C . ./module/kernel/ -run TestCatalogSyncDoc -count=1", catalogDocPath)
}

// replaceCatalogBlock 把 src 里 begin…end 之间的内容换成 body（返回替换后的全文）。
// nl = 文档的行尾风格，替换时连分隔换行一起用，避免混入 LF。
func replaceCatalogBlock(src, begin, end, body, nl string) (string, bool) {
	i := strings.Index(src, begin)
	if i < 0 {
		return src, false
	}
	rest := src[i+len(begin):]
	j := strings.Index(rest, end)
	if j < 0 {
		return src, false
	}
	return src[:i+len(begin)] + nl + body + nl + rest[j:], true
}
