package kernel

import (
	"modemd/module/catalog"
	"modemd/module/variant"
	"strings"
	"testing"
)

// ====================== 平台变体层：不变量测试 ======================
//
// 这一组钉住"厂商 × 平台"这家事没被改坏。对应的真实坑：
//
//	广和通 USB 模式高通 32/30/18/24、展锐 34/40/38/36、联发科只有 41；
//	美格 AT+SER 高通 2=ECM、非高通 2=NCM（**同数字不同含义**）；
//	华为 AT^SETMODE 海思 0/1/2、展锐 0..8。
//	写错的后果不是报错，而是**静默把模组切到另一个模式** / 写下含义相反的值。

// TestPlatformVariantTableWellFormed 变体表自身的结构约束。
func TestPlatformVariantTableWellFormed(t *testing.T) {
	type key struct {
		fam variant.ModemFamily
		fl  variant.PlatformFlavor
	}
	seen := map[key]int{}
	anyRows := map[variant.ModemFamily]int{}
	for i, v := range variant.PlatformVariants {
		if strings.TrimSpace(string(v.Family)) == "" {
			t.Errorf("第 %d 行没写 family", i)
		}
		if strings.TrimSpace(string(v.Flavor)) == "" {
			t.Errorf("第 %d 行没写 flavor（未识别平台请不要单独立行，用 FlavorAny 兜底）", i)
		}
		if strings.TrimSpace(v.Label) == "" {
			t.Errorf("%s/%s：没写 label（界面/文档里就是这一串）", v.Family, v.Flavor)
		}
		if strings.TrimSpace(v.Evidence) == "" {
			t.Errorf("%s/%s：没写 evidence（依据哪个 QModem 脚本/手册，复核时要看）", v.Family, v.Flavor)
		}
		if v.Usb == nil && v.Imei == nil && v.Mode == nil && v.Locker == nil && strings.TrimSpace(v.Note) == "" {
			t.Errorf("%s/%s：这一行没有任何差异点（空行没有意义）", v.Family, v.Flavor)
		}
		k := key{v.Family, v.Flavor}
		if dup, ok := seen[k]; ok {
			t.Errorf("%s/%s 重复：第 %d 行与第 %d 行（查找只取第一行，后面那行是死代码）", v.Family, v.Flavor, dup, i)
		}
		seen[k] = i
		if v.Flavor == variant.FlavorAny {
			anyRows[v.Family]++
		}
		if v.Safety && v.Flavor != variant.FlavorAny {
			// safety 的本意是"清单里暂时没有型号命中，允许留着当兜底"。
			// 通用家族的平台行（如"通用 · 联发科"）属于这一类：现有 MTK 型号都是广和通的，
			// 但别的厂商的 MTK 模组按旧行为也走这套方言，所以留着 —— 要求写清依据即可。
			if strings.TrimSpace(v.Evidence) == "" {
				t.Errorf("%s/%s：标了 safety 却没写依据", v.Family, v.Flavor)
			}
		}
		if len(v.LockBase) > 0 {
			if len(variant.LockCandidatesByName(v.LockBase)) != len(v.LockBase) {
				t.Errorf("%s/%s：lockBase 里有没登记的名字 %v（见 lockCandidateBuilders）", v.Family, v.Flavor, v.LockBase)
			}
			for _, n := range v.LockBase {
				if _, ok := lockFamilyLabels[n]; !ok {
					t.Errorf("%s/%s：lockBase 里的 %q 没有界面名（lockFamilyLabels）", v.Family, v.Flavor, n)
				}
			}
		}
	}
	for fam, n := range anyRows {
		if n > 1 {
			t.Errorf("%s：有 %d 条兜底行（只允许一条，否则谁生效取决于书写顺序）", fam, n)
		}
	}
}

// TestPlatformVariantCoverage 清单里**每个型号**用到的平台档位都必须有精确的行。
//
// 这是防"静默降级"的那一条：广和通出了个新平台的型号，如果表里没有对应行，
// 它会悄悄用"广和通 · 其它平台（兜底 = 高通语义）"那张表 —— 码表是错的，但不会报错。
// 有了这条测试，加型号时会被强制补一行（或写明"就要用高通语义"）。
func TestPlatformVariantCoverage(t *testing.T) {
	for _, e := range catalog.ModemCatalog {
		v := catalogVendorToModem(e.Vendor)
		fam, fl := variant.FamilyFor(v, e.Model), variant.FlavorFor(v, e.Model)
		row := variant.PlatformVariantFor(v, e.Model)
		if row == nil {
			t.Errorf("%s/%s：查不到任何变体行（family=%s flavor=%s）", e.Vendor, e.Model, fam, fl)
			continue
		}
		// 平台档位认得出的时候，必须命中同档位的行（禁止退化成兜底行/别家的表）
		if fl != variant.FlavorUnknown && fl != variant.FlavorAny && row.Flavor != fl {
			t.Errorf("%s/%s：档位是 %q，却落到 %q（%s）—— 补一行精确变体，别让它用别的平台的码表",
				e.Vendor, e.Model, variant.FlavorLabel(fl), variant.FlavorLabel(row.Flavor), row.Label)
		}
		if strings.TrimSpace(variant.PlatformVariantName(v, e.Model)) == "" {
			t.Errorf("%s/%s：变体名为空（界面/文档要用）", e.Vendor, e.Model)
		}
		if strings.TrimSpace(variant.PlatformVariantNote(v, e.Model)) == "" {
			t.Errorf("%s/%s：变体没有依据说明（界面/文档要用）", e.Vendor, e.Model)
		}
	}
}

// TestPlatformVariantReachable 反向：表里**每一行**都得有型号真的会用到。
//
// 留着一行永远不会命中的变体，会让人以为"这家在这个平台上是这么处理的"，
// 实际上代码走的是别的分支。清单里确实暂时没有的（新平台兜底）必须显式标 safety。
func TestPlatformVariantReachable(t *testing.T) {
	hits := map[string]int{}
	for _, e := range catalog.ModemCatalog {
		if row := variant.PlatformVariantFor(catalogVendorToModem(e.Vendor), e.Model); row != nil {
			hits[variantKey(row)]++
		}
	}
	for i := range variant.PlatformVariants {
		v := &variant.PlatformVariants[i]
		n := hits[variantKey(v)]
		if n == 0 && !v.Safety {
			t.Errorf("%s/%s（%s）：清单里没有任何型号命中这一行 —— 删掉，或标 safety=true 并说明留给什么机型",
				v.Family, v.Flavor, v.Label)
		}
		if n > 0 && v.Safety {
			t.Errorf("%s/%s（%s）：标了 safety 但清单里有 %d 个型号在用 —— 去掉 safety 标记",
				v.Family, v.Flavor, v.Label, n)
		}
	}
}

func variantKey(v *variant.PlatformVariant) string {
	return string(v.Family) + "|" + string(v.Flavor)
}

// TestPlatformFlavorForKnownModels 档位识别的对外约定：
// 平台能认出来的型号 → 档位就是那个平台；两个"lte 旧款"谓词必须继续生效。
func TestPlatformFlavorForKnownModels(t *testing.T) {
	cases := []struct {
		vendor ModemVendor
		model  string
		want   variant.PlatformFlavor
	}{
		{VendorQuectel, "RM520N-GL", variant.FlavorQualcomm},
		{VendorQuectel, "EC25", variant.FlavorLegacyLTE}, // 旧款 4G：QModem 平台=lte
		{VendorQuectel, "EM05-CN", variant.FlavorLegacyLTE},
		{VendorQuectel, "EM120K-GL", variant.FlavorQualcomm}, // lte12 ≠ lte（不能当旧款）
		{VendorQuectel, "RG801", variant.FlavorHiSilicon},    // 移远型号但是巴龙平台
		{VendorFibocom, "FM350-GL", variant.FlavorMediaTek},
		{VendorFibocom, "FM650-CN", variant.FlavorUnisoc},
		{VendorFibocom, "FM150-AE", variant.FlavorQualcomm},
		{VendorFibocom, "NL668", variant.FlavorLegacyLTE}, // 广和通 lte 款
		{VendorMeig, "SRM821", variant.FlavorUnisoc},
		{VendorMeig, "SRM825", variant.FlavorQualcomm},
		{VendorSimcom, "simcom_sim8200ea-m2", variant.FlavorQualcomm},
		{VendorSimcom, "simcom_a8200_serias", variant.FlavorUnknown}, // ASR：不猜
		{VendorUnknown, "MT5700M-CN", variant.FlavorHiSilicon},
		{VendorUnknown, "MH5000-82", variant.FlavorUnisoc},
		{VendorUnknown, "AIR724UG", variant.FlavorUnisoc},
		{VendorUnknown, "GM800", variant.FlavorQualcomm},
		{VendorUnknown, "ME3630-W", variant.FlavorUnknown},
	}
	for _, c := range cases {
		if got := variant.FlavorFor(c.vendor, c.model); got != c.want {
			t.Errorf("flavorFor(%s, %q) = %q, want %q", c.vendor, c.model, got, c.want)
		}
	}
}

// TestPlatformLockBaseOrder 探针方言顺序：原来写在 lockCandidates 里的四条 switch 分支，
// 现在必须由变体表还原出一模一样的顺序（顺序变了 = 一台只认某套方言的设备要多白试几条指令）。
func TestPlatformLockBaseOrder(t *testing.T) {
	check := func(vendor ModemVendor, model string, want []string) {
		t.Helper()
		mod := &Modem{Model: model, ModelName: model, Vendor: vendor}
		got := mod.lockCandidates()
		if len(got) < len(want) {
			t.Fatalf("%s/%s：候选太少（%d）", vendor, model, len(got))
		}
		for i, name := range want {
			if got[i].Name != name {
				t.Errorf("%s/%s：第 %d 个候选是 %q，期望 %q（顺序与改动前必须一致）", vendor, model, i, got[i].Name, name)
			}
		}
	}
	check(VendorFibocom, "FM350-GL", variant.LockBaseMediaTek)
	check(VendorFibocom, "FM650-CN", variant.LockBaseDefault)  // 展锐：改动前也走 default
	check(VendorFibocom, "FM150-AE", variant.LockBaseQualcomm) // 高通
	check(VendorUnknown, "MT5700M-CN", variant.LockBaseHiSilicon)
	check(VendorUnknown, "USR-420", variant.LockBaseDefault)
}

// TestPlatformVariantMarkdownStable 变体表 → Markdown 的基本形状（M.1 章节要贴它）。
func TestPlatformVariantMarkdownStable(t *testing.T) {
	md := variant.PlatformVariantMarkdown()
	if !strings.HasPrefix(md, "| 家族 | 平台档位 | 变体 | 平台差异点 | 依据 |") {
		t.Fatalf("变体表的表头变了：%q", strings.SplitN(md, "\n", 2)[0])
	}
	lines := strings.Split(strings.TrimRight(md, "\n"), "\n")
	if len(lines) != len(variant.PlatformVariants)+2 {
		t.Fatalf("表格行数 %d ≠ 变体行数 %d + 2", len(lines), len(variant.PlatformVariants))
	}
	for _, l := range lines[2:] {
		// 单元格里的竖线会被转义成 \|，所以数“未被转义的竖线”
		pipes := 0
		for i := 0; i < len(l); i++ {
			if l[i] == '|' && (i == 0 || l[i-1] != '\\') {
				pipes++
			}
		}
		if pipes != 6 {
			t.Errorf("列数不对（表里有未转义的 | 会破坏 Markdown）：%s", l)
		}
	}
}
