package kernel

import (
	"strings"

	"modemd/module/catalog"
	"modemd/module/fibocom"
	"modemd/module/modedialect"
	"modemd/module/usbdialect"
	"modemd/module/variant"
)

// ====================== USB 网卡模式 / 组网模式偏好：各厂商方言 ======================
//
// 全部依据 QModem 的 `cmds/*.sh` 与 `vendor/*.sh`（2026-10 用 GitHub API 拉整文件核对，
// 每条都注明来源）。**没有证据的部分不臆造**：能读的照读，写入只接受"设备自己报的取值"
// （把原值原样写回），并在界面上写明原因。

// ====================== USB 网卡模式 ======================

// ====================== 「只展示、不允许执行」的档位 ======================

// usbnetFibocomRNDISWhy 广和通 RNDIS 被钉成"只展示"的原因（界面要写清楚，别写成含糊的"不支持"）。
//
// 2026-10-09 FM160-CN 现场：广和通切到 RNDIS 后 USB 组成里**没有 AT 口**
// （`2cb7:010b` = RNDIS+DUN+DIAG+ADB，其中 ttyUSB 是 ff/ff/30，发 AT 不回；ADB 口倒是还在），
// 页面上的 AT 功能（信号/短信/拨号/USB 模式自己）全部失联，最后是从模组内部
// 用 ADB + root 改 `/fibonv/data/mode.cfg` 才救回来的 —— 这种"一改就回不来"的档位
// 不该从页面上给出去。

// usbnetBlockedModes 本机"只展示、不允许执行"的取值 → 原因（空 = 都允许）。
//
// 目前只有广和通的 **RNDIS**（各平台取值不同：高通/lte 是 24、展锐 38、联发科 41），
// 所以不能只按取值钉，得按"档位"认：清单口径用 kind（qmi/ecm/…），厂商方言表没有 kind，
// 就按展示名（RNDIS）认。
// usbnetFibocomRNDISWhy / usbnetBlockedKind / isRNDISModeName 已搬到 modemd/module/fibocom
// （见 module/fibocom/usbnet.go：那是广和通自己的知识）；kernel 保留同名别名，调用点零改动。

// usbnetBlockedModes 本机"只展示、不允许执行"的取值 → 原因（空 = 都允许）。
//
// 目前只有广和通的 **RNDIS**（各平台取值不同：高通/lte 是 24、展锐 38、联发科 41），
// 所以不能只按取值钉，得按"档位"认：清单口径用 kind（qmi/ecm/…），厂商方言表没有 kind，
// 就按展示名（RNDIS）认。**判定本身在 module/fibocom**，这里只负责把两个来源收集齐。
func usbnetBlockedModes(mod *Modem) map[string]string {
	if mod == nil {
		return nil
	}
	var refs []fibocom.USBModeRef
	if e, ok := catalogUSBATFor(mod); ok {
		for _, m := range catalogUSBModeList(e) {
			refs = append(refs, fibocom.USBModeRef{Kind: m.Kind, Value: m.Value})
		}
	}
	for _, m := range usbnetModesUnfiltered(mod) {
		refs = append(refs, fibocom.USBModeRef{Value: m.Value, Name: m.Name})
	}
	return fibocom.USBnetBlockedValues(mod.Vendor, refs)
}

// isRNDISModeName 展示名是不是 RNDIS 那一档

// usbnetBlockedReason 该取值是不是被钉住了（是 → 返回原因，否则空串）。
// 取值大小写不敏感（设备回读可能是大写十六进制/带空格）。
func usbnetBlockedReason(mod *Modem, value string) string {
	value = strings.TrimSpace(value)
	for v, why := range usbnetBlockedModes(mod) {
		if strings.EqualFold(strings.TrimSpace(v), value) {
			return why
		}
	}
	return ""
}

// usbDialectFor 取本机的 USB 网卡模式方言（nil = 不支持）
// usbDialectFor 取本机的 USB 网卡模式方言（nil = 不支持）。
//
// 厂商/机型分发表已搬到 modemd/module/variant（见 module/variant/dispatch.go）：
// 「按厂商选方言」是厂商知识，加一家只改 module 侧，不再碰内核。
func usbDialectFor(mod *Modem) *usbdialect.USBDialect {
	if mod == nil {
		return nil
	}
	return variant.USBDialectFor(mod.Vendor, mod.Model+" "+mod.ModelName)
}

// ====================== 组网模式偏好 ======================

// 语义值（与 QModem set_network_prefer_nr 的分档一致）
// 定义已搬到 modemd/module/common（见 module/common/mode.go）；kernel 保留同名别名，见 lock_alias.go。

// unisoc.UnisocModeFromSPRAT / unisoc.ParseSPRAT 已搬到 modemd/module/unisoc（见 module/unisoc/mode.go）；
// kernel 保留同名别名，见 lock_alias.go。

// simcom.CnmpName / simcom.ParseCNMP 已搬到 modemd/module/simcom（见 module/simcom/cnmp.go，
// 那是芯讯通自己的码表）；kernel 保留同名别名，见 lock_alias.go。

// ====================== 语义档位 ↔ 本机码（码表来自 QModem vendor/*.sh） ======================

// ====================== 各家方言的码表 ======================

// common.NormalizeModePref 把设备回报的 mode_pref 原文归一成语义值 ——
// 实现已搬到 modemd/module/common（NormalizeModePref）；kernel 保留同名别名，见 lock_alias.go。

// modeDialectFor 取本机的组网模式方言（nil = 这家真的没有）。
//
// 三步找：
//
//	① 厂商标识实时识别出来了 → 按厂商（+ 型号族前缀）选；
//	② 厂商标识认不出来（ATI 里没有厂商关键字 / 写法没覆盖）→ 查模块清单（每个型号都带厂商与平台）；
//	③ 清单里也没这个型号 → 按型号名的族前缀猜（RM5xx=移远、FM1xx=广和通…）。
//
// ⚠️ 为什么要有 ②③（用户报“好多模块不支持”）：以前只有 ①，**厂商标识一旦没认出来，
// 清单 91 个型号里有 68 个直接变成“本机未提供组网模式偏好指令”** —— 移远/广和通/芯讯通/美格
// 全家的方言都挂在 mod.Vendor 上，而另 8 家挂在型号谓词上。清单里明明写着厂商，别让用户吃这个亏。
//
// ⚠️ 猜错厂商的代价是“写出错的取值”，所以 ③ 只做**族前缀**（型号头几个字符）、宁可不给也不乱猜；
// 真的猜不出来就走运行期的“挨个问”（见 vendor_settings_engine.go 的 probeModeDialect）。
func modeDialectFor(mod *Modem) *modedialect.Dialect {
	if mod == nil {
		return nil
	}
	model := mod.Model + " " + mod.ModelName
	if d := modeDialectForVendor(mod.Vendor, mod, model); d != nil {
		return d
	}
	if d := modeDialectForCatalogModel(model, mod); d != nil {
		return d
	}
	return nil
}

// modeDialectForCatalogModel 清单 / 型号名兜底（见 modeDialectFor 的 ②③）
func modeDialectForCatalogModel(model string, mod *Modem) *modedialect.Dialect {
	if e, ok := catalog.CatalogEntryForModel(model); ok {
		if d := modeDialectForCatalogVendor(e.Vendor, e.Platform, mod); d != nil {
			return d
		}
	}
	if fam := vendorFamilyOfModel(model); fam != "" {
		return modeDialectForCatalogVendor(fam, "", mod)
	}
	return nil
}

// modeDialectForCatalogVendor 清单里的厂商字符串（+ 平台）→ 方言。
//
// 平台只对两处有意义（都是 QModem vendor/*.sh 里的 case 分支）：
//
//	移远的 `lte` 档用 AT+QCFG="nwscanmode"（其余含 unisoc/hisilicon 都走 QNWPREFCFG）；
//	高新兴的 `qualcomm` 平台才有 5G 档（lte 平台只有自动/仅3G/仅4G）。
//
// modeDialectForCatalogVendor 清单里的厂商字符串（+ 平台）→ 方言。
//
// 分发表已搬到 modemd/module/variant（见 module/variant/dispatch.go）。
func modeDialectForCatalogVendor(vendor, platform string, mod *Modem) *modedialect.Dialect {
	return variant.ModeDialectForCatalogVendor(vendor, platform)
}

// vendorFamilyOfModel 光看型号名猜厂商（返回清单口径的厂商标识；猜不出返回 ""）。
//
// 纯字符串函数（不发指令）：给“清单里也没有这个型号”的模块用。全部是**族前缀**，
// 几个易混的地方特意拎出来：EM05/EM12/EM16 是移远，而 EM91/EM92 是 Sierra；
// MT5700/MT5710 是鼎桥（走华为巴龙那套）；SIM7xx/SIM8xx/SIM9xx/A76xx/A82xx 是芯讯通。
func vendorFamilyOfModel(model string) string {
	m := catalog.CatalogFlatten(model)
	if m == "" {
		return ""
	}
	fam := func(prefixes ...string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(m, p) {
				return true
			}
		}
		return false
	}
	switch {
	case fam("AIR72", "AIR724"): // 合宙（openluat）
		return "openluat"
	case fam("MT570", "MT571"): // 鼎桥 = 华为巴龙那套
		return "huawei"
	case fam("GM800", "GM801"): // 高新兴
		return "gosuncn"
	case fam("MC73", "EM91", "EM92"): // Sierra
		return "sierra"
	case fam("FN990", "FN980"): // Telit
		return "telit"
	case fam("MV32", "MV31"): // Thales
		return "thales"
	case fam("T99W", "T77W"): // Foxconn
		return "foxconn"
	case fam("ME363", "ME362", "N510", "N720"): // 有方
		return "neoway"
	case fam("RM5", "RG5", "RM2", "RG2", "EC2", "EG2", "EM05", "EM12", "EM16",
		"EP06", "AG5", "AG8", "MH8001", "BG95"):
		return "quectel"
	case fam("FM1", "FM3", "FM6", "RW3", "NL6", "L610", "L616", "L860", "G510"):
		return "fibocom"
	case fam("SIM7", "SIM8", "SIM9", "A760", "A767", "A820", "A790"):
		return "simcom"
	case fam("SRM8", "SRM9"):
		return "meig"
	}
	return ""
}

// modeDialectForVendor 按“厂商 + 型号族谓词”选方言（第一条路，不查清单、不猜型号头）
// modeDialectForVendor 按“厂商 + 型号族谓词”选方言（第一条路，不查清单、不猜型号头）。
//
// 分发表已搬到 modemd/module/variant（见 module/variant/dispatch.go）。
func modeDialectForVendor(v ModemVendor, mod *Modem, model string) *modedialect.Dialect {
	if mod == nil {
		return nil
	}
	return variant.ModeDialectForVendor(v, model)
}
