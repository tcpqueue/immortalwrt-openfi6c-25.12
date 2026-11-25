package kernel

import (
	"bytes"
	"encoding/json"
	"modemd/module/catalog"
	"strings"
	"testing"
)

// ====================== 清单数据文件（modem_catalog.json）不变量测试 ======================
//
// 清单从 .go 搬到 JSON 以后，**编译期不再帮我们兜底**：键名打错、模式取值拼错、
// 厂商标识写错，Go 都不会报错（只会"这个型号没有 USB 模式"这种看不出来的行为变化）。
// 所以这几条测试就是那份数据的"类型检查"。

// TestCatalogJSONStrictParse 严格解析：未知字段、类型不符都要报错。
//
// 坑：`encoding/json` 默认**静默忽略**不认识的键 —— 把 `"usb"` 写成 `"Usb"` 或
// `"models"`，解析出来的结构体就是"USB 模式为空"，而空 = 该型号不支持 USB，
// 界面上的表现是"下拉框里什么都不给"，没人会想到是 JSON 键名错了。
func TestCatalogJSONStrictParse(t *testing.T) {
	dec := json.NewDecoder(bytes.NewReader(catalog.ModemCatalogJSON))
	dec.DisallowUnknownFields()
	var out []catalog.ModemCatalogEntry
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("严格解析失败（多半是键名或类型写错了）：%v", err)
	}
	if len(out) != len(catalog.ModemCatalog) {
		t.Fatalf("严格解析得到 %d 条，运行期用的是 %d 条", len(out), len(catalog.ModemCatalog))
	}
	// 再确认 decoder 已经把内容读完（后面没有多余的东西）
	if dec.More() {
		t.Error("JSON 里顶层数组后面还有多余内容")
	}
}

// TestCatalogJSONSchema 数据本身的白名单约束。
func TestCatalogJSONSchema(t *testing.T) {
	vendors := map[string]bool{}
	for _, v := range catalog.CatalogVendorOrder {
		vendors[v] = true
	}
	platforms := map[string]bool{
		"qualcomm": true, "unisoc": true, "mediatek": true, "hisilicon": true,
		"lte": true, "lte12": true, "asrmicro": true,
		"": true, // QModem 原文有留空的行（本项目目前没有，留着以防将来补）
	}
	seen := map[string]int{}
	lastVendor := ""
	for i, e := range catalog.ModemCatalog {
		if e.Vendor == "" || e.Model == "" {
			t.Errorf("第 %d 条：厂商/型号不能为空（%+v）", i, e)
			continue
		}
		if !vendors[e.Vendor] {
			t.Errorf("%s/%s：厂商标识 %q 不在 catalogVendorOrder 里（界面排序与中文名都会认不出）",
				e.Vendor, e.Model, e.Vendor)
		}
		if !platforms[e.Platform] {
			t.Errorf("%s/%s：平台列 %q 不在已知集合里（QModem 支持列表只有 qualcomm/unisoc/mediatek/hisilicon/lte/lte12/asrmicro）",
				e.Vendor, e.Model, e.Platform)
		}
		if e.Model != strings.ToLower(e.Model) {
			t.Errorf("%s/%s：型号要小写（与 QModem 原文一致，查找走的是不区分大小写，但显示会不一致）",
				e.Vendor, e.Model)
		}
		key := e.Vendor + "/" + e.Model
		if dup, ok := seen[key]; ok {
			t.Errorf("%s 重复：第 %d 条与第 %d 条（查找只取第一条）", key, dup, i)
		}
		seen[key] = i
		for kind, modes := range map[string][]string{"usb": e.USB, "pcie": e.PCIe} {
			for _, m := range modes {
				if _, ok := catalog.CatalogKindLabel[m]; !ok {
					t.Errorf("%s：%s 里有个不认识的模式取值 %q（取值见 CatMode* 常量，一律小写）",
						key, kind, m)
				}
				if m != strings.ToLower(m) {
					t.Errorf("%s：%s 里的 %q 应该是小写", key, kind, m)
				}
			}
		}
		// 同一厂商的行必须连续：清单是按 QModem 支持列表"按厂商分段"抄的，
		// 插一行到别的段里会让文档表格（按本顺序生成）与原文对不上。
		if e.Vendor != lastVendor {
			if _, back := seen[e.Vendor+"/\x00"]; back {
				t.Errorf("%s：%s 这段被打断了（清单按厂商分段，请插回对应段里）", key, e.Vendor)
			}
			seen[e.Vendor+"/\x00"] = 1
			lastVendor = e.Vendor
		}
	}
	// 模式组合不能有重复项（手改 JSON 时容易复制粘贴出一份重复列表）
	for _, e := range catalog.ModemCatalog {
		for kind, modes := range map[string][]string{"usb": e.USB, "pcie": e.PCIe} {
			u := map[string]bool{}
			for _, m := range modes {
				if u[m] {
					t.Errorf("%s/%s：%s 里 %q 出现两次", e.Vendor, e.Model, kind, m)
				}
				u[m] = true
			}
		}
	}
	// 至少有一个型号没有 USB 形态（Foxconn T99W640 是真实存在的特例，
	// 保证"空数组 = 不支持"这条语义有样本，不会被人当作"数据漏了"而"修掉"）
	noUSB := 0
	for _, e := range catalog.ModemCatalog {
		if !e.SupportsUSB() {
			noUSB++
		}
	}
	if noUSB == 0 {
		t.Error("没有任何型号是「没有 USB 形态」的 —— T99W640 就是这种情况，别把它当漏数据补上")
	}
}

// TestCatalogJSONPlatformFamily 平台列必须能被 catalogPlatformFamily 归类（界面按它分组）。
func TestCatalogJSONPlatformFamily(t *testing.T) {
	for _, e := range catalog.ModemCatalog {
		fam := catalog.CatalogPlatformFamily(e.Platform)
		if strings.TrimSpace(fam) == "" {
			t.Errorf("%s/%s：平台 %q 归不了类（catalogPlatformFamily）", e.Vendor, e.Model, e.Platform)
		}
	}
}

// ====================== usb_at / pcie_at（形态切换指令）不变量 ======================
//
// 这两个字段是**手改 JSON 的高危区**（91 个型号、每个好几条 AT），
// 而键名写错（`"usb_at"` → `"usbAt"`）在 DisallowUnknownFields 下会 panic、
// 值里少个引号却只是"指令发出去被拒" —— 所以这里把口径钉死：
//
//	① 模式键必须是**清单口径**（qmi/ecm/mbim/…，小写）—— 与 usb/pcie 列表同一套词；
//	② modes 的键必须在该型号的 usb/pcie 列表里（生成工具就是按"映射 ∩ 支持列表"取的）；
//	③ 值必须**像一条 AT 指令**（`AT` 开头）—— 挡住"把参数值 18 抄成指令"这种错；
//	④ 有指令就必须有查询指令；没有指令就必须有说明 ——
//	   **不许留空**：空着会让下一个人以为"漏了"，而真相是"QModem 里没有 / 没取证"。
func TestCatalogJSONATSchema(t *testing.T) {
	usbWithModes, pcieWithModes := 0, 0
	for _, e := range catalog.ModemCatalog {
		key := e.Vendor + "/" + e.Model
		if len(e.USB) > 0 && e.USBModeAT == nil {
			t.Errorf("%s：有 USB 形态却没登记 usb_at"+
				"（要么给切换指令，要么写清为什么没有 —— 生成工具 files/tools/catalogmodeat）", key)
		}
		if e.USBModeAT != nil && len(e.USB) == 0 {
			t.Errorf("%s：登记了 usb_at，但 USB 形态是空的（T99W640 那种只有 PCIe 的型号不该有 usb_at）", key)
		}
		if e.PCIeModeAT != nil && len(e.PCIe) == 0 {
			t.Errorf("%s：登记了 pcie_at，但 PCIe 形态是空的", key)
		}
		for _, c := range []struct {
			kind string
			list []string
			set  *catalog.ModeATSet
		}{{"usb_at", e.USB, e.USBModeAT}, {"pcie_at", e.PCIe, e.PCIeModeAT}} {
			s := c.set
			if s == nil {
				continue
			}
			if s.Query != "" && !catalogLooksLikeAT(s.Query) {
				t.Errorf("%s：%s.query = %q 不像一条 AT 指令", key, c.kind, s.Query)
			}
			if len(s.Modes) > 0 {
				if c.kind == "usb_at" {
					usbWithModes++
				} else {
					pcieWithModes++
				}
				if strings.TrimSpace(s.Query) == "" {
					t.Errorf("%s：%s 有切换指令却没有查询指令（先能查再能切）", key, c.kind)
				}
			} else if strings.TrimSpace(s.Note) == "" {
				t.Errorf("%s：%s 既没有切换指令又没有说明 —— 空着会被当成「漏了」（真相通常是 QModem 没给/没取证）",
					key, c.kind)
			}
			for mode, cmd := range s.Modes {
				if _, ok := catalog.CatalogKindLabel[mode]; !ok {
					t.Errorf("%s：%s 的模式键 %q 不是清单口径（见 CatMode* 常量）", key, c.kind, mode)
				}
				if mode != strings.ToLower(mode) {
					t.Errorf("%s：%s 的模式键 %q 应该小写", key, c.kind, mode)
				}
				if !catalog.CatalogKindInList(c.list, mode) {
					t.Errorf("%s：%s 里有 %q 的切换指令，但该型号的 %s 支持列表里没有这一档"+
						"（生成工具按「映射 ∩ 支持列表」取，手补时也请对齐）",
						key, c.kind, mode, strings.TrimSuffix(c.kind, "_at"))
				}
				if !catalogLooksLikeAT(cmd) {
					t.Errorf("%s：%s 里 %q → %q 不像 AT 指令（是不是把参数值当指令抄进来了？）",
						key, c.kind, mode, cmd)
				}
			}
		}
	}
	// 覆盖度门槛：不许「悄悄退回去」（现在 83 个型号有 USB 切换指令、41 个有 PCIe 说明）
	if usbWithModes < 80 {
		t.Errorf("只有 %d 个型号登记了 USB 切换指令（应该有 80+）—— 是不是生成工具没跑完/被覆盖了？",
			usbWithModes)
	}
	if pcieWithModes == 0 {
		t.Log("提示：目前没有任何型号登记 PCIe 切换指令（QModem 里确实只有查询）—— 这条不是错误")
	}
}

// catalogLooksLikeAT 值像不像一条 AT 指令（富士康的前缀是 AT+ / AT^，都算）
func catalogLooksLikeAT(s string) bool {
	u := strings.ToUpper(strings.TrimSpace(s))
	return strings.HasPrefix(u, "AT") && len(u) > 2
}
