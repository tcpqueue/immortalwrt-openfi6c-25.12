package kernel

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	bandspkg "modemd/module/bands"
	catalogpkg "modemd/module/catalog"
	"modemd/module/common"
	"modemd/module/fibocom"
	"modemd/module/lock"
	"modemd/module/meig"
	"modemd/module/quectel"
	"modemd/module/simcom"
	"modemd/module/tdtech"
	"modemd/module/unisoc"
)

// ====================== 支持频段表（modem_bands.json）不变量测试 ======================
//
// 数据来自上游快照，**编译期不帮我们兜底**：型号名打错一个字母，查表就永远查不到；
// 频段号写错（比如 NR 写成 780）会原样下发到模组。所以这里把值域、升序去重、
// 以及与支持清单的一一对齐都钉住。

// TestModemBandsJSONStrictParse 严格解析：未知字段/类型不符必须报错。
//
// 坑与清单 JSON 同一个：`"lte_band"` 写成 `"lte_bands"` 会被静默忽略 ⇒
// 变成"这个型号没有 LTE 频段"，界面照旧能用但显示错的。
func TestModemBandsJSONStrictParse(t *testing.T) {
	dec := json.NewDecoder(bytes.NewReader(bandspkg.ModemBandsJSON))
	dec.DisallowUnknownFields()
	var out []bandspkg.ModemBands
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("严格解析失败（多半是键名写错了）：%v", err)
	}
	if len(out) != len(bandspkg.ModemBandsImpl) {
		t.Fatalf("严格解析得到 %d 条，运行期用的是 %d 条", len(out), len(bandspkg.ModemBandsImpl))
	}
	if dec.More() {
		t.Error("JSON 顶层数组后面还有多余内容")
	}
}

// TestModemBandsSchema 频段值本身的白名单约束。
func TestModemBandsSchema(t *testing.T) {
	// 频段号合法区间（3GPP）：WCDMA 1~32、LTE 1~71（Rel-8 起最高 B71）、NR 1~512（含毫米波 n257~n262）
	//
	// ⚠️ WCDMA 放到 64：上游给 `a7605c` 写的是 `23/50`，而 3GPP 里 WCDMA 没有 B50。
	// 本项目**逐字抄上游快照、不静默修正**（界面会原样显示“清单声明 WCDMA B23 B50”），
	// 所以这里的区间比严格值域宽一格，只拦“明显是笔误”（如 700）。
	ranges := map[string][2]int{
		"wcdma": [2]int{1, 64}, "lte": [2]int{1, 71}, "nr_nsa": [2]int{1, 512}, "nr_sa": [2]int{1, 512},
	}
	// seenKey 上报名（model 或别名）→ 占了它的型号；全表必须唯一，否则匹配结果依赖表内顺序
	seenKey := map[string]string{}
	for _, b := range bandspkg.ModemBandsImpl {
		seenKey[b.Model] = b.Model
	}
	for _, b := range bandspkg.ModemBandsImpl {
		if strings.TrimSpace(b.Model) == "" {
			t.Error("有一条记录没有 model")
			continue
		}
		if b.Model != strings.ToLower(b.Model) {
			t.Errorf("%s：型号要小写（与上游/清单一致）", b.Model)
		}
		if strings.TrimSpace(b.Vendor) == "" || strings.TrimSpace(b.Platform) == "" {
			t.Errorf("%s：vendor/platform 不能为空（界面要显示哪个平台的口径）", b.Model)
		}
		for name, bands := range map[string][]int{"wcdma": b.WCDMA, "lte": b.LTE, "nr_nsa": b.NRNSA, "nr_sa": b.NRSA} {
			r := ranges[name]
			for i, n := range bands {
				if n < r[0] || n > r[1] {
					t.Errorf("%s %s：频段号 %d 超出合法区间 %d~%d", b.Model, name, n, r[0], r[1])
				}
				if i > 0 && n <= bands[i-1] {
					t.Errorf("%s %s：频段号没有严格升序/有重复（%d 在 %d 之后）", b.Model, name, n, bands[i-1])
				}
			}
		}
		// 别名：小写、非空、**全表唯一**（包括不能撞别人的 model）——
		// 撞了就会"两个型号抢同一个上报名"，匹配结果取决于表内顺序（等于隐式 bug）。
		for _, a := range b.Aliases {
			if strings.TrimSpace(a) == "" {
				t.Errorf("%s：别名不能为空", b.Model)
				continue
			}
			if a != strings.ToLower(a) {
				t.Errorf("%s：别名 %q 要小写（与 model 同一个口径）", b.Model, a)
			}
			if seenKey[a] != "" {
				t.Errorf("上报名 %q 重复（已被 %s 占用）—— 匹配会变得依赖表内顺序", a, seenKey[a])
				continue
			}
			seenKey[a] = b.Model
		}
	}
}

// extraBandOnlyModels 只在**频段表**里、不在 QModem 支持清单（modem_catalog.json）里的型号。
//
// 用途：清单照抄 QModem（91 个模组），但本机也可能要支持 QModem 清单里根本没有的模块
// ——典型是**设备自带 SoC 基带**（如中兴 F50 / 展锐 UMS9620 上报的 `QogirN6Pro_PS`）。
// 这类型号只往 `modem_bands.json` 里加一条频段记录即可，不必也不该混进 QModem 清单。
//
// 为什么要显式登记：频段表里的型号名**打错一个字母**不会报错，界面只会永远显示
// "本型号没有频段记录"，没人会发现 —— 所以"新出现的、不在清单里的型号"必须在这里
// 被人工确认一次；反过来，登记了却不在频段表里（改名/删掉了）也会报错。
var extraBandOnlyModels = []string{
	"f50-mu300", // 中兴 F50 / 展讯 UMS9620（用户补的频段数据；QModem 清单里没有这个型号）
}

// bandCoverageDiff 频段表 / 支持清单的覆满检查（纯函数 ⇒ 规则本身也能用合成数据钉住）。
//
// 返回三组差异：
//
//	notCovered —— 只在频段表里、既不在清单、也没登记到 extraBandOnlyModels 的型号（大概型号名打错）
//	noRecord   —— 在清单里、但频段表没有记录的型号（界面会永远显示“本型号没有频段记录”）
//	missing    —— 登记在 extraBandOnlyModels 里、但频段表里已经不存在的型号（改名/删了忘了同步）
func bandCoverageDiff(bands []bandspkg.ModemBands, catalog []catalogpkg.ModemCatalogEntry, allowed []string) (notCovered, noRecord, missing []string) {
	inCatalog := map[string]bool{}
	for _, e := range catalog {
		inCatalog[e.Model] = true
	}
	inBands := map[string]bool{}
	for _, b := range bands {
		inBands[b.Model] = true
	}
	ok := map[string]bool{}
	for _, m := range allowed {
		ok[m] = true
		if !inBands[m] {
			missing = append(missing, m)
		}
	}
	for _, b := range bands {
		if !inCatalog[b.Model] && !ok[b.Model] {
			notCovered = append(notCovered, b.Model)
		}
	}
	for _, e := range catalog {
		if !inBands[e.Model] {
			noRecord = append(noRecord, e.Model)
		}
	}
	return notCovered, noRecord, missing
}

// TestModemBandsAlignsWithCatalog 真实数据必须覆满（规则见 bandCoverageDiff）。
//
// 清单侧漏记录用一条命令就能补齐（`go run ./files/tools/banddump`）。
func TestModemBandsAlignsWithCatalog(t *testing.T) {
	notCovered, noRecord, missing := bandCoverageDiff(bandspkg.ModemBandsImpl, catalogpkg.ModemCatalog, extraBandOnlyModels)
	if len(notCovered) > 0 {
		t.Errorf("频段表里有 %d 个型号不在支持清单里，也没登记到 extraBandOnlyModels（型号名打错？）：%s",
			len(notCovered), strings.Join(notCovered, ", "))
	}
	if len(noRecord) > 0 {
		t.Errorf("支持清单里有 %d 个型号没有频段记录：%s\n"+
			"在 5g-modem-manager 目录下重新生成：go run ./files/tools/banddump", len(noRecord), strings.Join(noRecord, ", "))
	}
	if len(missing) > 0 {
		t.Errorf("extraBandOnlyModels 里有 %d 个型号在频段表里不存在（改名或删掉了？）：%s",
			len(missing), strings.Join(missing, ", "))
	}
}

// TestBandCoverageRule 合成数据钉住规则本身：登记过的“清单外型号”放行、没登记的拦住、
// 登记了却不在表里的也拦住、清单缺记录照样拦住。
// （真实数据里目前没有“清单外型号”，这条放行分支只有合成数据才走到。）
func TestBandCoverageRule(t *testing.T) {
	bands := []bandspkg.ModemBands{{Model: "rm520n-gl"}, {Model: "qogirn6pro_ps"}, {Model: "typo-model"}}
	catalog := []catalogpkg.ModemCatalogEntry{{Model: "rm520n-gl"}}

	notCovered, noRecord, missing := bandCoverageDiff(bands, catalog, []string{"qogirn6pro_ps"})
	if len(notCovered) != 1 || notCovered[0] != "typo-model" {
		t.Fatalf("未登记的清单外型号应被拦住，实际 notCovered=%v", notCovered)
	}
	if len(noRecord) != 0 || len(missing) != 0 {
		t.Fatalf("不应有 noRecord/missing，实际 noRecord=%v missing=%v", noRecord, missing)
	}

	if _, _, missing = bandCoverageDiff(bands, catalog, []string{"qogirn6pro_ps", "gone-model"}); len(missing) != 1 || missing[0] != "gone-model" {
		t.Fatalf("登记了却不在频段表里应报 missing，实际 %v", missing)
	}

	if _, noRecord, _ = bandCoverageDiff(
		[]bandspkg.ModemBands{{Model: "rm520n-gl"}},
		[]catalogpkg.ModemCatalogEntry{{Model: "rm520n-gl"}, {Model: "em05"}}, nil); len(noRecord) != 1 || noRecord[0] != "em05" {
		t.Fatalf("清单里缺频段记录应报 noRecord，实际 %v", noRecord)
	}
}

// TestBandsForModelLookup 型号匹配：ATI 原文 / 短名 / 大小写 / 连写都要认，且长 token 优先。
func TestBandsForModelLookup(t *testing.T) {
	// 命中：多种写法都要能查到同一个型号
	hits := []struct{ query, want string }{
		{"RM520N-GL", "rm520n-gl"},
		{"rm520n-gl", "rm520n-gl"},
		{"RM520N-GL_GL", "rm520n-gl"},
		{"Quectel RM520N-GL", "rm520n-gl"},   // ATI 原文（厂商名 + 型号）
		{"RM520NGLAAR03A03M4G", "rm520n-gl"}, // 连写（ATI 不带连字符）
		{"SIMCOMSIM8200EAM2", "simcom_sim8200ea-m2"},
		{"MT5700M-CN", "mt5700m-cn"},
		// 别名：同一个型号的其它固件上报名（用户填的 F50 记录要认设备实际报的 QogirN6Pro_PS）
		{"QogirN6Pro_PS", "f50-mu300"},
		{"qogirn6pro", "f50-mu300"},
		{"Platform Version: MOCORTM_V2_22C_W25.39.4_Debug | Project Version:   QogirN6Pro_PS | HW Version: ums9620_modem", "f50-mu300"},
	}
	for _, c := range hits {
		b, ok := bandspkg.BandsForModel(c.query)
		if !ok {
			t.Errorf("BandsForModel(%q) 没查到，期望 %s", c.query, c.want)
			continue
		}
		if b.Model != c.want {
			t.Errorf("BandsForModel(%q) = %s，期望 %s", c.query, b.Model, c.want)
		}
	}
	// ⚠️ 长 token 优先：em120k 不能被 em12 抢走（清单里踩过同样的坑）
	if b, ok := bandspkg.BandsForModel("EM120K-GL"); !ok || b.Model != "em120k" {
		t.Errorf("BandsForModel(EM120K-GL) = %v/%v，期望 em120k", b.Model, ok)
	}
	// 词边界：S100 不该命中 s10
	if b, ok := bandspkg.BandsForModel("S100"); ok && b.Model == "s10" {
		t.Error("BandsForModel(S100) 命中了 s10 —— 短 token 必须要求词边界")
	}
	// 查不到：空串与不存在的型号
	if _, ok := bandspkg.BandsForModel(""); ok {
		t.Error("空型号不该命中")
	}
	if _, ok := bandspkg.BandsForModel("NOT-A-REAL-MODEM-XYZZY"); ok {
		t.Error("不存在的型号不该命中")
	}
}

// TestModemBandsSupportedBands 各制式下的"可锁频段"。
func TestModemBandsSupportedBands(t *testing.T) {
	b := bandspkg.ModemBands{
		LTE:   []int{1, 3, 5},
		NRNSA: []int{41, 78},
		NRSA:  []int{1, 78, 79},
		WCDMA: []int{1, 8},
	}
	if got := b.SupportedBands(CellTechLTE); !equalInts(got, []int{1, 3, 5}) {
		t.Errorf("LTE 频段 = %v", got)
	}
	// NR = NSA ∪ SA（升序去重）：41 只在 NSA、79 只在 SA、78 两边都有
	if got := b.SupportedBands(CellTechNR); !equalInts(got, []int{1, 41, 78, 79}) {
		t.Errorf("NR 频段 = %v，期望 NSA∪SA = [1 41 78 79]", got)
	}
	if got := b.SupportedBands("WCDMA"); !equalInts(got, []int{1, 8}) {
		t.Errorf("WCDMA 频段 = %v", got)
	}
	// 标签用 3GPP 口径
	if got := strings.Join(b.Keys(CellTechNR), " "); got != "n1 n41 n78 n79" {
		t.Errorf("NR 标签 = %q", got)
	}
	if got := strings.Join(b.Keys(CellTechLTE), " "); got != "B1 B3 B5" {
		t.Errorf("LTE 标签 = %q", got)
	}
	// 上游没写频段的型号：查得到记录，但没有频段。
	// ⚠️ 别写死具体型号 —— 上游（或用户自己）随时会往 modem_bands.json 补数据，
	// 写死就会出现"数据补全了反倒把测试搞红"（2026-10-06 踩过：fm350-gl 被补上了频段）。
	var empty bandspkg.ModemBands
	foundEmpty := false
	for _, mb := range bandspkg.ModemBandsImpl {
		if !mb.HasBands() {
			empty, foundEmpty = mb, true
			break
		}
	}
	if !foundEmpty {
		t.Skip("当前频段表里每个型号都有数据，没有「没写频段」的样本可测")
	}
	if len(empty.SupportedBands(CellTechNR)) != 0 {
		t.Error("上游没写频段时不该编出频段")
	}
	if !strings.Contains(empty.Note(), "没写频段") {
		t.Errorf("上游没写频段时说明要写清楚，实际：%s", empty.Note())
	}
}

// TestModemBandsText 展示文案（界面/接口直接用）。
func TestModemBandsText(t *testing.T) {
	b, ok := bandspkg.BandsForModel("rm520n-gl")
	if !ok || !b.HasBands() {
		t.Fatal("rm520n-gl 应该有频段数据")
	}
	txt := b.BandsText()
	// 四组各自以「制式+前缀」开头（数值很多，只断言段头与关键频段存在）
	for _, want := range []string{"LTE B1 ", "NR-SA n1 ", "NR-NSA n1 ", "WCDMA B1 "} {
		if !strings.Contains(txt, want) {
			t.Errorf("BandsText 里缺 %q：%s", want, txt)
		}
	}
	if !strings.Contains(txt, " n78") && !strings.HasSuffix(txt, "n78") {
		t.Errorf("BandsText 里应该有 n78：%s", txt)
	}
	if !strings.Contains(b.Note(), "Modem-Pro 频段表") {
		t.Errorf("Note 要写清来源：%s", b.Note())
	}
	// BandKey 的前缀口径
	if bandspkg.BandKey(CellTechNR, 78) != "n78" || bandspkg.BandKey(CellTechLTE, 3) != "B3" {
		t.Error("BandKey 前缀不对（LTE=B / NR=n）")
	}
	// mergeBands：去重 + 升序 + 丢掉 0/负数
	if got := common.MergeBands([]int{5, 0, 3, 5, -1}, []int{3, 8}); !equalInts(got, []int{3, 5, 8}) {
		t.Errorf("mergeBands = %v", got)
	}
	// 统计
	models, withBands, withNR := bandspkg.ModemBandsStats()
	if models != len(bandspkg.ModemBandsImpl) || withBands == 0 || withNR == 0 || withBands > models {
		t.Errorf("统计异常：models=%d withBands=%d withNR=%d（总 %d）", models, withBands, withNR, len(bandspkg.ModemBandsImpl))
	}
}

// TestApplyDeclaredBands 清单声明的频段贴到锁网状态上（三种"没有"要有三种说法）。
func TestApplyDeclaredBands(t *testing.T) {
	// 有数据：NR 取 NSA/SA 并集
	nr := bandspkg.BandLock{Tech: CellTechNR}
	bandspkg.ApplyDeclaredBands(&nr, "RM520N-GL")
	if len(nr.Declared) == 0 {
		t.Fatal("RM520N-GL 的 NR 频段应该有值")
	}
	if !equalInts(nr.Declared, nr.Declared) || nr.Declared[0] > nr.Declared[len(nr.Declared)-1] {
		t.Error("Declared 应当升序")
	}
	if !strings.Contains(nr.DeclaredNote, "Modem-Pro 频段表") {
		t.Errorf("说明要写清来源：%s", nr.DeclaredNote)
	}
	lte := bandspkg.BandLock{Tech: CellTechLTE}
	bandspkg.ApplyDeclaredBands(&lte, "RM520N-GL")
	if len(lte.Declared) == 0 {
		t.Fatal("RM520N-GL 的 LTE 频段应该有值")
	}

	// 上游没写频段的型号（⚠️ 不写死型号：上游/用户会陆续补数据，写死就会"数据补全了反倒把测试搞红"）
	if m := modelWithoutBands(); m != "" {
		blank := bandspkg.BandLock{Tech: CellTechLTE}
		bandspkg.ApplyDeclaredBands(&blank, m)
		if len(blank.Declared) != 0 {
			t.Errorf("%s 上游没写频段，不该编出频段：%v", m, blank.Declared)
		}
		if !strings.Contains(blank.DeclaredNote, "没写频段") {
			t.Errorf("%s 要说明「上游没写」：%s", m, blank.DeclaredNote)
		}
	} else {
		t.Log("当前频段表里每个型号都有数据，跳过「上游没写频段」分支")
	}

	// 型号不在频段表里
	unknown := bandspkg.BandLock{Tech: CellTechNR}
	bandspkg.ApplyDeclaredBands(&unknown, "NOT-A-REAL-MODEM-XYZZY")
	if len(unknown.Declared) != 0 || !strings.Contains(unknown.DeclaredNote, "没登记") {
		t.Errorf("型号不在表里时要说清：%v / %s", unknown.Declared, unknown.DeclaredNote)
	}

	// 在表里、有 LTE，但该型号没有 NR 记录（纯 LTE 老模组）
	if m := modelWithoutNR(); m != "" {
		onlyLTE := bandspkg.BandLock{Tech: CellTechNR}
		bandspkg.ApplyDeclaredBands(&onlyLTE, m)
		if len(onlyLTE.Declared) != 0 || !strings.Contains(onlyLTE.DeclaredNote, "没有 NR 频段记录") {
			t.Errorf("%s 该制式没记录时要说清：%v / %s", m, onlyLTE.Declared, onlyLTE.DeclaredNote)
		}
	} else {
		t.Log("当前频段表里每个型号都有 NR 数据，跳过「该制式没记录」分支")
	}
}

// TestATPlanCarriesBands AT 指令面（/api/modules?model=）要带上支持频段。
func TestATPlanCarriesBands(t *testing.T) {
	p := ATPlanForModel("RM520N-GL")
	if p.Bands == nil || len(p.Bands.LTE) == 0 || len(p.Bands.NRSA) == 0 {
		t.Fatalf("RM520N-GL 的指令面没带上频段：%+v", p.Bands)
	}
	if !strings.Contains(p.BandsNote, "Modem-Pro 频段表") {
		t.Errorf("频段来源说明缺失：%s", p.BandsNote)
	}
	// 上游没写频段的型号：记录在、频段为空、说明写清"上游没写"
	if m := modelWithoutBands(); m != "" {
		p2 := ATPlanForModel(m)
		if p2.Bands == nil {
			t.Fatalf("%s 应该有频段记录（上游只是没写频段）", m)
		}
		if p2.Bands.HasBands() || !strings.Contains(p2.BandsNote, "没写频段") {
			t.Errorf("%s 的频段说明不对：%+v / %s", m, p2.Bands, p2.BandsNote)
		}
	} else {
		t.Log("当前频段表里每个型号都有数据，跳过「上游没写频段」分支")
	}
}

// modelWithoutBands 找一个「在频段表里、但上游没写频段」的型号（找不到返回 ""）。
// 测试里**不要写死型号**：上游快照或用户自己随时会把频段补上，写死就会变成"数据变好了测试却红了"。
func modelWithoutBands() string {
	for _, b := range bandspkg.ModemBandsImpl {
		if !b.HasBands() {
			return b.Model
		}
	}
	return ""
}

// modelWithoutNR 找一个「有频段、但没有 NR 频段」的型号（纯 LTE 老模组；找不到返回 ""）
func modelWithoutNR() string {
	for _, b := range bandspkg.ModemBandsImpl {
		if b.HasBands() && len(b.NRNSA) == 0 && len(b.NRSA) == 0 {
			return b.Model
		}
	}
	return ""
}

func TestModemBandsMarkdownStable(t *testing.T) {
	md := bandspkg.ModemBandsMarkdown()
	if !strings.HasPrefix(md, "| 厂商 | 型号 | 平台 | LTE 频段 | NR SA | NR NSA | WCDMA |") {
		t.Fatalf("频段表的表头变了：%q", strings.SplitN(md, "\n", 2)[0])
	}
	lines := strings.Split(strings.TrimRight(md, "\n"), "\n")
	if len(lines) != len(bandspkg.ModemBandsImpl)+2 {
		t.Fatalf("表格行数 %d ≠ 型号数 %d + 2", len(lines), len(bandspkg.ModemBandsImpl))
	}
	for _, l := range lines[2:] {
		pipes := 0
		for i := 0; i < len(l); i++ {
			if l[i] == '|' && (i == 0 || l[i-1] != '\\') {
				pipes++
			}
		}
		if pipes != 8 {
			t.Errorf("列数不对（未转义的竖线会破坏 Markdown）：%s", l)
		}
	}
}

// TestLockStatusBandGroups 锁频段页的"按模块支持自动排版"（分组规则）。
//
// 界面不再用下拉选制式：本型号支持哪几组就排几组，每组下面勾选频段。
func TestLockStatusBandGroups(t *testing.T) {
	// 只支持 LTE 的老模组（air724ug）：只出 LTE 一组，不该冒出空的 NR/WCDMA 组
	lteOnly, ok := bandspkg.BandsForModel("air724ug")
	if !ok {
		t.Fatal("air724ug 应该在频段表里")
	}
	st := &bandspkg.LockStatus{Bands: &lteOnly}
	gs := st.BandGroups()
	if len(gs) != 1 || gs[0].Key != "lte" || !gs[0].Lockable || gs[0].Tech != "LTE" {
		t.Fatalf("纯 LTE 型号应只有一组可锁的 LTE：%+v", gs)
	}

	// 4G/5G 全频模组（rm520n-gl）：WCDMA（只读）+ LTE + **NR（只一组）**
	// ⚠️ NR 不再拆 NSA / SA 两组：设备侧只有一条锁频段指令、一套 NR 频段列表，
	// 拆两组就是两边镜像联动（用户 2026-10-06 报的"两边同时选上"）。
	full, ok := bandspkg.BandsForModel("rm520n-gl")
	if !ok {
		t.Fatal("rm520n-gl 应该在频段表里")
	}
	st2 := &bandspkg.LockStatus{Bands: &full}
	gs2 := st2.BandGroups()
	keys := make([]string, 0, len(gs2))
	for _, g := range gs2 {
		keys = append(keys, g.Key)
	}
	want := []string{"wcdma", "lte", "nr"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("分组 = %v，期望 %v", keys, want)
	}
	for _, g := range gs2 {
		if g.Title == "" {
			t.Errorf("%s：没有标题（界面直接显示）", g.Key)
		}
		if len(g.Bands) == 0 {
			t.Errorf("%s：空组不该出现", g.Key)
		}
		if g.Key == "wcdma" {
			if g.Lockable || g.Tech != "" {
				t.Error("WCDMA 组应当只读（没有 3G 锁频指令）")
			}
			if !strings.Contains(g.Note, "3G 锁频") {
				t.Errorf("WCDMA 组要说明为什么不可选：%s", g.Note)
			}
		} else if !g.Lockable || g.Tech == "" {
			t.Errorf("%s：应当可锁（tech=%s lockable=%v）", g.Key, g.Tech, g.Lockable)
		}
	}
	// NR 组 = 清单 NSA ∪ SA（两栏不一致时也要并集，不能只取一栏）
	for _, g := range gs2 {
		if g.Key == "nr" && !equalInts(g.Bands, common.MergeBands(full.NRNSA, full.NRSA)) {
			t.Errorf("NR 组应 = 清单 NSA ∪ SA：%v", g.Bands)
		}
	}

	// 设备上报（capable）里多出来的频段也要能选：并进各自的组（rm520n-gl 的清单 LTE 里没有 B9，
	// 用它验证"设备上报的也能勾"；500 是清单里没有的 NR 频段）
	const devLTE = 9
	st3 := &bandspkg.LockStatus{
		Bands:   &full,
		BandLTE: bandspkg.BandLock{Tech: CellTechLTE, Capable: []int{devLTE}},
		BandNR:  bandspkg.BandLock{Tech: CellTechNR, Capable: []int{78, 500}},
	}
	gs3 := st3.BandGroups()
	var lteBands, nrBands []int
	for _, g := range gs3 {
		switch g.Key {
		case "lte":
			lteBands = g.Bands
		case "nr":
			nrBands = g.Bands
		}
	}
	if !equalInts(lteBands, common.MergeBands(full.LTE, []int{devLTE})) {
		t.Errorf("LTE 组应 = 清单 ∪ 设备上报：%v", lteBands)
	}
	if !equalInts(nrBands, common.MergeBands(full.NRNSA, full.NRSA, []int{78, 500})) {
		t.Errorf("NR 组应 = 清单 NSA∪SA ∪ 设备上报（含 500）：%v", nrBands)
	}

	// ⭐ 真机回归（2026-10-06，广和通 FM160-CN）：**清单里没有这个型号**、固件也不给"能力查询"，
	// 只剩 AT+GTACT? 报的**当前锁定**频段 ⇒ 必须照样出组，否则界面上一个勾选框都没有
	//（用户报的"锁频段页什么都没有"就是这个）。
	// 刻意用 Bands = nil 而不是某个具体型号：上游/用户随时会给型号补频段，写死型号就会失效。
	locked9 := []int{1, 3, 5, 8, 34, 38, 39, 40, 41}
	st4 := &bandspkg.LockStatus{
		Bands:   nil,
		BandLTE: bandspkg.BandLock{Tech: CellTechLTE, Bands: locked9, Locked: true},
		BandNR:  bandspkg.BandLock{Tech: CellTechNR, Bands: []int{78}, Locked: true},
	}
	var g4lte, g4nr []int
	for _, g := range st4.BandGroups() {
		switch {
		case g.Key == "lte":
			g4lte = g.Bands
		case g.Tech == "NR":
			g4nr = common.MergeBands(g4nr, g.Bands)
		}
	}
	if !equalInts(g4lte, locked9) {
		t.Errorf("设备当前锁定的 LTE 频段必须出现在 LTE 组里：%v", g4lte)
	}
	if !equalInts(g4nr, []int{78}) {
		t.Errorf("设备当前锁定的 NR 频段必须可勾选：%v", g4nr)
	}
	// 型号名大小写无关（用户曾怀疑是这个）：FM160-CN 能查到 fm160-cn 的记录
	if _, ok := bandspkg.BandsForModel("FM160-CN"); !ok {
		t.Error("FM160-CN 应能匹配到频段表里的 fm160-cn（大小写无关）")
	}

	// 清单里没写频段、设备也没报 → 没有组（界面显示"没有频段记录"的说明）。
	// 同样不写死型号（上游/用户会陆续补数据）：用一条"只有型号、没有频段"的合成记录。
	blank := bandspkg.ModemBands{Model: "no-bands-model"}
	if gs := (&bandspkg.LockStatus{Bands: &blank}).BandGroups(); len(gs) != 0 {
		t.Errorf("没有频段数据时不该编出组：%+v", gs)
	}
	if gs := (*bandspkg.LockStatus)(nil).BandGroups(); gs != nil {
		t.Error("nil 状态应返回 nil")
	}
}

// TestQueryLockStatusFillsBandGroups ⭐ 真机回归（2026-10-06，广和通 FM160-CN / OpenWrt 24.10）。
//
// 两个曾经同时踩到的坑，这里一起钉住：
//
//	① `band_groups` 字段与 `BandGroups()` 都写好了，但**生产代码里没人调用它**
//	   ⇒ 所有机型的锁频段页永远显示"本型号没有频段记录"（字段被 omitempty 藏掉，前端只看到 0 组）；
//	② 该机型清单里上游没写频段、固件又不给"能力"查询，唯一的频段来源是
//	   `AT+GTACT?` 报的**当前锁定**频段 ⇒ 分组必须把它算进去（见 BandGroups 的 ②④）。
//
// 夹具是那台真机的原始应答（解出来 = LTE B1/3/5/8/34/38/39/40/41 + n78）。
func TestQueryLockStatusFillsBandGroups(t *testing.T) {
	withATTimeout(t)
	const gtact = "+GTACT: 20,6,3,1,8,101,103,105,108,134,138,139,140,141,5078"

	mod := &Modem{
		ID:        0,
		Vendor:    VendorFibocom,
		Model:     "FM160-CN",
		cmdImpl:   &fibocom.FibocomMTKCommand{},
		writeChan: make(chan string, 64),
		readChan:  make(chan string, 64),
	}
	// 方言固定成真机实测选中的那套（GT/GTACT），跳过探测——本用例测的是状态组装，不是探测
	mod.lockProbed, mod.lockChosen = true, &fibocom.FibocomMTKCommand{}
	defer close(mod.writeChan)
	fakeATServe(mod, func(cmd string) []string {
		if strings.HasPrefix(cmd, "AT+GTACT?") {
			return []string{cmd, gtact, "OK"}
		}
		return []string{"OK"}
	})

	st, err := mod.queryLockStatus()
	if err != nil {
		t.Fatalf("queryLockStatus: %v", err)
	}
	if !st.BandLTE.Locked || !st.BandNR.Locked {
		t.Fatalf("夹具是「已锁定」状态，locked 应都为 true：LTE=%v NR=%v", st.BandLTE.Locked, st.BandNR.Locked)
	}
	if len(st.Groups) == 0 {
		t.Fatal("band_groups 是空的：queryLockStatus 里必须调 st.BandGroups()（前端只认这个字段）")
	}
	var lte, nr []int
	for _, g := range st.Groups {
		switch {
		case g.Key == "lte":
			lte = g.Bands
		case g.Tech == "NR":
			nr = common.MergeBands(nr, g.Bands)
		}
	}
	// 设备当前锁定的值必须都能勾选（清单有数据时是并集，没数据时就靠设备这几个）
	for _, n := range []int{1, 3, 5, 8, 34, 38, 39, 40, 41} {
		if !containsInt(lte, n) {
			t.Errorf("设备当前锁定的 B%d 必须出现在 LTE 组里：%v", n, lte)
		}
	}
	if !containsInt(nr, 78) {
		t.Errorf("设备当前锁定的 n78 必须出现在某个 NR 组里：%v", nr)
	}
	// JSON 契约：字段名就是前端读的 band_groups
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"band_groups"`) {
		t.Error("JSON 里必须有 band_groups（前端 renderBandGroups 只读它）")
	}
}

// TestCloneLockStatusCopiesGroups 缓存/推送共享的锁网状态必须深拷贝 Groups，
// 否则调用方改一下克隆体就把缓存里的分组也改了。
func TestCloneLockStatusCopiesGroups(t *testing.T) {
	st := &bandspkg.LockStatus{
		Groups: []bandspkg.BandGroup{{
			Key: "lte", Title: "LTE（4G）", Tech: "LTE", Lockable: true,
			Bands: []int{1, 3}, Declared: []int{1},
		}},
	}
	c := cloneLockStatus(st)
	if len(c.Groups) != 1 || !equalInts(c.Groups[0].Bands, []int{1, 3}) {
		t.Fatalf("克隆体丢了分组：%+v", c.Groups)
	}
	c.Groups[0].Bands[0] = 99
	c.Groups[0].Declared = append(c.Groups[0].Declared, 7)
	if !equalInts(st.Groups[0].Bands, []int{1, 3}) || len(st.Groups[0].Declared) != 1 {
		t.Errorf("改克隆体不应影响到原对象：%+v", st.Groups[0])
	}
}

// TestBandGroupsSeparateNSA NR 分**两套**列表的方言（移远/高通 QNW、芯讯通）必须出两组
// **各自独立**的 NSA / SA（用户 2026-10-06 明确要求：像 rg801 那种两栏频段不一样的模块，
// 得能分别勾、分别下发）。
//
// 关键不变量：两组的 `Tech` 必须不同（`NRNSA` vs `NR`）—— 前端就是按 tech 分选择集的，
// 相同的话勾一边另一边会跟着变（用户报的"两边同时选上"）。
func TestBandGroupsSeparateNSA(t *testing.T) {
	rg801, ok := bandspkg.BandsForModel("rg801")
	if !ok {
		t.Fatal("rg801 应在频段表里")
	}
	if !rg801.HasNR() || equalInts(rg801.NRNSA, rg801.NRSA) {
		t.Fatal("前提变了：rg801 的 NSA / SA 两栏现在一样/为空，这条回归要改用别的型号")
	}

	gs := (&bandspkg.LockStatus{Bands: &rg801, SeparateNSA: true}).BandGroups()
	var nsa, sa *bandspkg.BandGroup
	keys := make([]string, 0, len(gs))
	for i := range gs {
		keys = append(keys, gs[i].Key)
		switch gs[i].Key {
		case "nr_nsa":
			nsa = &gs[i]
		case "nr_sa":
			sa = &gs[i]
		}
	}
	if strings.Join(keys, ",") != "wcdma,lte,nr_nsa,nr_sa" {
		t.Fatalf("两套列表的方言应出 WCDMA + LTE + NR·NSA + NR·SA：%v", keys)
	}
	if nsa.Tech != string(CellTechNRNSA) || sa.Tech != string(CellTechNR) {
		t.Fatalf("两组的 tech 必须不同（否则前端共用一套选择）：nsa=%q sa=%q", nsa.Tech, sa.Tech)
	}
	if !equalInts(nsa.Bands, common.MergeBands(rg801.NRNSA)) {
		t.Errorf("NSA 组只能取清单 nr_nsa（别把 SA 的并进来）：%v", nsa.Bands)
	}
	if !equalInts(sa.Bands, common.MergeBands(rg801.NRSA)) {
		t.Errorf("SA 组 = 清单 nr_sa：%v", sa.Bands)
	}
	if nsa.Label == "" || sa.Label == "" || nsa.Label == sa.Label {
		t.Errorf("两组要有不同的短名（摘要里区分）：%q / %q", nsa.Label, sa.Label)
	}
	if !strings.Contains(nsa.Note, "两套") {
		t.Errorf("NSA 组要说明本机是两套列表：%s", nsa.Note)
	}

	// 一套列表的方言（SeparateNSA=false，如广和通 GT / 展锐）仍只出一组 NR，且 = NSA ∪ SA
	keys = keys[:0]
	for _, g := range (&bandspkg.LockStatus{Bands: &rg801}).BandGroups() {
		keys = append(keys, g.Key)
	}
	if strings.Join(keys, ",") != "wcdma,lte,nr" {
		t.Fatalf("一套列表时只该出一组 NR：%v", keys)
	}
}

// TestNSABandIndependenceDeclared 钉住"谁声称有独立 NSA 列表"。
//
// 声明错了的后果：界面多出一组镜像勾选框（勾一边另一边跟着变），而下发时那条
// `nsa_nr5g_band` 指令在只有一套列表的固件上根本不存在 —— 等于给用户一个假开关。
// ⚠️ 嵌入会**自动继承**（`MeigCommand` 嵌 `QuectelCommand`）⇒ 新增嵌 QuectelCommand 的
// 方言时必须回来确认这一条。
func TestNSABandIndependenceDeclared(t *testing.T) {
	support := []struct {
		name string
		l    NetLocker
	}{
		{"quectel（AT+QNWPREFCFG=\"nsa_nr5g_band\"）", &quectel.QuectelCommand{}},
		{"fibocom-qnw（同 QNW 系）", &fibocom.FibocomCommand{}},
		{"simcom（AT+CSYSSEL=\"nsa_nr5g_band\"）", &simcom.SimcomLockCommand{}},
		{"meig（嵌 QuectelCommand，QNW 系）", &meig.MeigCommand{}},
	}
	for _, c := range support {
		if !nsaBandIndependent(c.l) {
			t.Errorf("%s 应当声明「独立的 NSA 列表」", c.name)
		}
	}
	single := []struct {
		name string
		l    NetLocker
	}{
		{"unisoc/展锐（AT+SPCOMMLOCKBAND 只有一套）", &unisoc.UnisocLockCommand{}},
		{"fibocom-gtact（AT+GTACT 只有一套）", &fibocom.FibocomMTKCommand{}},
		{"mt5700/鼎桥（^LTEFREQLOCK/^NRFREQLOCK 只有一套）", &tdtech.MT5700Command{}},
		{"sierra（AT!BAND 只有一套）", &lock.AtBandLocker{}},
	}
	for _, c := range single {
		if nsaBandIndependent(c.l) {
			t.Errorf("%s 的 NR 只有一套列表，不得声明 NSA 独立", c.name)
		}
	}
}

// TestNSABandCommands NSA 技术域要落到方言自己的 key 上（移远 nsa_nr5g_band / 芯讯通同 key）
func TestNSABandCommands(t *testing.T) {
	q := &quectel.QuectelCommand{}
	if got := q.CommandQueryBandLock(CellTechNRNSA); got != `AT+QNWPREFCFG="nsa_nr5g_band"` {
		t.Errorf("移远 NSA 查询 = %q", got)
	}
	if got := q.CommandSetBandLock(CellTechNRNSA, []int{78, 41}); got != `AT+QNWPREFCFG="nsa_nr5g_band",41:78` {
		t.Errorf("移远 NSA 下发 = %q（要升序去重）", got)
	}
	// SA / LTE 不能被带偏
	if got := q.CommandQueryBandLock(CellTechNR); got != `AT+QNWPREFCFG="nr5g_band"` {
		t.Errorf("SA 查询 = %q", got)
	}
	if got := q.CommandQueryBandLock(CellTechLTE); got != `AT+QNWPREFCFG="lte_band"` {
		t.Errorf("LTE 查询 = %q", got)
	}
	s := &simcom.SimcomLockCommand{}
	if got := s.CommandSetBandLock(CellTechNRNSA, []int{1, 78}); got != `AT+CSYSSEL="nsa_nr5g_band",1:78` {
		t.Errorf("芯讯通 NSA 下发 = %q", got)
	}
	// 一套列表的方言：NSA 技术域**不得生成设置指令**（查询方法对任何 tech 都返回同一条常量，
	// 那些由发动机层的 NSAIndependent 拦截，见 TestNSABandIndependenceDeclared）。
	for _, l := range []NetLocker{&unisoc.UnisocLockCommand{}, &fibocom.FibocomMTKCommand{}, &tdtech.MT5700Command{}} {
		if cmd := l.CommandSetBandLock(CellTechNRNSA, []int{78}); cmd != "" {
			t.Errorf("%T 不得为 NSA 生成设置指令：%q", l, cmd)
		}
	}
}

// TestParseLockTechNSA API/定时任务传进来的 tech 要认 NSA，且不能和 SA 混为一谈
func TestParseLockTechNSA(t *testing.T) {
	for _, s := range []string{"NRNSA", "nr-nsa", "nsa", "5G-NSA", "NR5G-NSA", " nrnsa "} {
		tech, err := ParseLockTech(s)
		if err != nil || tech != CellTechNRNSA {
			t.Errorf("ParseLockTech(%q) = %q, %v；期望 %q", s, tech, err, CellTechNRNSA)
		}
	}
	for _, s := range []string{"NR", "5G", "NR5G-SA"} {
		if tech, err := ParseLockTech(s); err != nil || tech != CellTechNR {
			t.Errorf("ParseLockTech(%q) = %q, %v；期望 %q", s, tech, err, CellTechNR)
		}
	}
	if _, err := ParseLockTech("bogus"); err == nil {
		t.Error("乱写的 tech 应当报错")
	}
}

// TestSetBandLockNRNSARejectedOnSingleListDialect 一套 NR 列表的方言上单独锁 NSA：
// 必须**明确报错**，而不是把一条猜出来的指令发进模组。
func TestSetBandLockNRNSARejectedOnSingleListDialect(t *testing.T) {
	mod := &Modem{
		ID: 0, Vendor: VendorFibocom, Model: "FM160-CN",
		cmdImpl:   &fibocom.FibocomMTKCommand{},
		writeChan: make(chan string, 8),
		readChan:  make(chan string, 8),
	}
	mod.lockProbed, mod.lockChosen = true, &fibocom.FibocomMTKCommand{}
	defer close(mod.writeChan)

	if _, err := mod.SetBandLock(CellTechNRNSA, []int{78}); err == nil || !strings.Contains(err.Error(), "一套频段列表") {
		t.Fatalf("应当明确拒绝：%v", err)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// containsInt 列表里有没有这个数（测试用：频段集合断言，避免依赖顺序）
func containsInt(list []int, n int) bool {
	for _, v := range list {
		if v == n {
			return true
		}
	}
	return false
}
