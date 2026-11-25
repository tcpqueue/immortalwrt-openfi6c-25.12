package variant

// 本文件是「厂商（+ 型号/平台）→ 设置方言」的分发表，原先散在 kernel 的
// vendor_settings_usb.go / vendor_settings_imei.go 里（那里对*Modem* 直接 switch 厂商）。
//
// 为什么放在 variant：这三个分发要读「厂商 × 平台」变体表（PlatformVariantFor），
// 而 variant 已经 import 了 usbdialect/modedialect/imeidialect；反过来放进那三个包会形成
// 循环依赖（variant → usbdialect 已有）。所以「按厂商选方言」这张表在这里。
//
// 判定层次统一为三层，缺一不可（原因见各函数注释）：
//
//	① 厂商常量（iface.VendorXxx）
//	② 机型关键字（platform.IsXxxModel）
//	③ 平台（platform.PlatformForModel）
//
// kernel 侧只保留一层薄壳（读 mod.Vendor/mod.Model 后调这里），新增厂商/机型
// 只改本文件 + 变体表，不再碰内核。

import (
	"strings"

	"modemd/iface"
	"modemd/module/fibocom"
	"modemd/module/foxconn"
	"modemd/module/gosuncn"
	"modemd/module/huawei"
	"modemd/module/imeidialect"
	"modemd/module/meig"
	"modemd/module/modedialect"
	"modemd/module/neoway"
	"modemd/module/openluat"
	"modemd/module/platform"
	"modemd/module/quectel"
	"modemd/module/sierra"
	"modemd/module/simcom"
	"modemd/module/telit"
	"modemd/module/thales"
	"modemd/module/unisoc"
	"modemd/module/usbdialect"
)

// ====================== USB 网卡模式方言 ======================

// USBDialectFor 取该厂商/型号的 USB 网卡模式方言（nil = 不支持）。
//
// model 传「型号 + 型号名」拼接串（调用方负责，如 mod.Model+" "+mod.ModelName）：
// 变体表与机型谓词都按这个串匹配，只传 mod.Model 会漏掉"型号名里才有关键字"的机型。
func USBDialectFor(vendor iface.ModemVendor, model string) *usbdialect.USBDialect {
	switch vendor {
	case iface.VendorQuectel:
		return quectel.USB()
	case iface.VendorFibocom:
		return fibocom.USB(model)
	case iface.VendorSimcom:
		// 芯讯通一家两套（SIM8200 走高通、SIM7906 走联发科），档位在变体表里
		if v := PlatformVariantFor(iface.VendorSimcom, model); v != nil && v.Usb != nil {
			return v.Usb()
		}
		return simcom.USBLegacy()
	case iface.VendorMeig:
		return meig.USB(model)
	}
	// 以下厂商没有 ModemVendor 常量（都归在 VendorUnknown），靠机型关键字认
	switch {
	case platform.IsGosuncnModel(model):
		return gosuncn.USB(model)
	case platform.IsFoxconnModel(model):
		return foxconn.USB()
	case platform.IsSierraModel(model):
		return sierra.USB()
	case platform.IsThalesModel(model):
		return thales.USB()
	case platform.IsTelitModel(model):
		return telit.USB()
	case platform.IsOpenluatModel(model):
		return openluat.USB()
	case platform.IsHuaweiModel(model),
		vendor == iface.VendorTDTech,
		platform.PlatformForModel(model) == platform.PlatformHiSilicon:
		// 华为/鼎桥/巴龙共用一套（MH5000、MT5700、MT5710…）
		return huawei.USB(model)
	}
	return nil
}

// ====================== 组网模式偏好方言 ======================

// ModeDialectForVendor 按「厂商常量 + 机型族谓词」选组网模式方言（第一层，不查清单、不猜型号头）。
func ModeDialectForVendor(vendor iface.ModemVendor, model string) *modedialect.Dialect {
	switch vendor {
	case iface.VendorQuectel:
		// lte 旧款（EC20/EC25/EG05…）走 lte 档码表，其余走 NR 档 —— 档位判定在变体表里
		if v := PlatformVariantFor(iface.VendorQuectel, model); v != nil && v.Mode != nil {
			return v.Mode()
		}
		return quectel.ModePrefNR()
	case iface.VendorSimcom:
		return simcom.Mode()
	case iface.VendorMeig:
		return meig.Mode()
	case iface.VendorFibocom:
		return fibocom.Mode()
	case iface.VendorUnisoc:
		// 展锐（中兴 F50）：组网模式 = 5G 开关 AT+SP5GRAN（已取证）
		return unisoc.Mode()
	}
	switch {
	case platform.IsNeowayModel(model):
		return neoway.Mode()
	case platform.IsSierraModel(model):
		return sierra.Mode()
	case platform.IsThalesModel(model):
		return thales.Mode()
	case platform.IsGosuncnModel(model):
		// 高新兴：字符含义按平台不同（QModem cmds/gosuncn.sh），档位在变体表里
		if v := PlatformVariantFor(vendor, model); v != nil && v.Mode != nil {
			return v.Mode()
		}
		return gosuncn.Mode(false)
	case platform.IsTelitModel(model):
		return telit.Mode()
	case platform.IsFoxconnModel(model):
		return foxconn.Mode()
	case platform.IsOpenluatModel(model):
		return openluat.Mode()
	case platform.IsHuaweiModel(model),
		vendor == iface.VendorTDTech,
		platform.PlatformForModel(model) == platform.PlatformHiSilicon:
		return huawei.Mode()
	}
	return nil
}

// ModeDialectForCatalogVendor 清单里的厂商字符串（+ 平台）→ 组网模式方言。
//
// 平台只对两处有意义（都是 QModem vendor/*.sh 里的 case 分支）：
//
//	移远的 `lte` 档用 AT+QCFG="nwscanmode"（其余含 unisoc/hisilicon 都走 QNWPREFCFG）；
//	高新兴的 `qualcomm` 平台才有 5G 档（lte 平台只有自动/仅3G/仅4G）。
func ModeDialectForCatalogVendor(vendor, platform string) *modedialect.Dialect {
	switch strings.ToLower(strings.TrimSpace(vendor)) {
	case "quectel":
		if strings.EqualFold(strings.TrimSpace(platform), "lte") {
			return quectel.ModePrefLTE()
		}
		return quectel.ModePrefNR()
	case "fibocom":
		return fibocom.Mode()
	case "simcom":
		return simcom.Mode()
	case "meig":
		return meig.Mode()
	case "huawei":
		return huawei.Mode() // 华为/鼎桥/巴龙共用一套（MH5000、MT5700…）
	case "foxconn":
		return foxconn.Mode()
	case "sierra":
		return sierra.Mode()
	case "telit":
		return telit.Mode()
	case "thales":
		return thales.Mode()
	case "neoway":
		return neoway.Mode()
	case "gosuncn":
		if strings.EqualFold(strings.TrimSpace(platform), "qualcomm") {
			return gosuncn.Mode(true)
		}
		return gosuncn.Mode(false)
	case "openluat":
		return openluat.Mode()
	}
	return nil
}

// ====================== IMEI 改写方言 ======================

// IMEIDialectFor 取厂商的 IMEI 改写方言（nil = 本机不支持改 IMEI）。
//
// 判定顺序：厂商常量（VendorXxx）→ 机型关键字（IsXxxModel）→ 平台（PlatformForModel）。
// 三层的存在原因：项目里只有 Quectel/Fibocom/Simcom/Meig/TDTech 五个厂商常量，
// 高新兴/有方/Telit/Thales/Foxconn/Sierra/openluat 都归在 VendorUnknown 靠机型认。
func IMEIDialectFor(vendor iface.ModemVendor, model string) *imeidialect.ImeiDialect {
	switch vendor {
	case iface.VendorQuectel:
		return &imeidialect.ImeiDialect{
			Set: func(imei string) string { return `AT+EGMR=1,7,"` + imei + `"` },
			Note: "移远 AT+EGMR=1,7,\"<IMEI>\"（Modem-Pro cmds/quectel.sh）；写前校验 15 位数字，" +
				"写完回读 AT+CGSN 确认；若回 +CME ERROR: 4 说明该固件锁了改 IMEI。",
		}
	case iface.VendorSimcom:
		return &imeidialect.ImeiDialect{
			Set:  func(imei string) string { return "AT+SIMEI=" + imei },
			Note: "芯讯通 AT+SIMEI=<IMEI>（Modem-Pro cmds/simcom.sh，**不带引号**）；写完回读 AT+CGSN 确认。",
		}
	case iface.VendorMeig:
		return &imeidialect.ImeiDialect{
			Set:  func(imei string) string { return `AT+LCTSN=1,7,"` + imei + `"` },
			Note: "美格 AT+LCTSN=1,7,\"<IMEI>\"（Modem-Pro cmds/meig.sh）；写完回读 AT+CGSN 确认。",
		}
	case iface.VendorFibocom:
		// 平台分派（QModem vendor/fibocom.sh set_imei 的 case $platform）：
		//	mediatek → EGMREXT；lte（NL668/NL678）→ LCTSN；其余 → GTSN
		// 具体走哪条由变体表决定
		if v := PlatformVariantFor(iface.VendorFibocom, model); v != nil && v.Imei != nil {
			return v.Imei()
		}
		return fibocom.IMEIGTSN()
	case iface.VendorUnisoc:
		// 展锐（F50 / UMS9620 实机取证）：只有 SPIMEI 被接受。
		// 逐个试过且全部回 `+CME ERROR: 4` 的：AT+EGMR / AT+EGMREXT / AT+LCTSN / AT+SIMEI / AT+GTSN / AT+MODIMEI。
		return &imeidialect.ImeiDialect{
			Set: func(imei string) string { return `AT+SPIMEI=0,"` + imei + `"` },
			Note: "展锐用 AT+SPIMEI=0,\"<IMEI>\"（SP = Spreadtrum 私有前缀）。" +
				"读用 AT+CGSN，AT+SPIMEI? 能回读出同一个值；" +
				"AT+EGMR / AT+EGMREXT / AT+LCTSN / AT+SIMEI / AT+GTSN / AT+MODIMEI 在展锐固件上一律 +CME ERROR: 4。" +
				"写完必须回读 AT+CGSN 确认。",
		}
	}

	// 以下都是 VendorUnknown → 靠机型/平台认
	switch {
	case platform.IsGosuncnModel(model):
		return &imeidialect.ImeiDialect{
			Set:  func(imei string) string { return `AT+EGMR=1,7,"` + imei + `"` },
			Note: "高新兴 AT+EGMR=1,7,\"<IMEI>\"（Modem-Pro cmds/gosuncn.sh）；写完回读 AT+CGSN 确认。",
		}
	case platform.IsNeowayModel(model):
		return &imeidialect.ImeiDialect{
			Set:  func(imei string) string { return `AT+SPIMEI=0,"` + imei + `"` },
			Note: "有方 AT+SPIMEI=0,\"<IMEI>\"（Modem-Pro cmds/neoway.sh）；写完回读 AT+CGSN 确认。",
		}
	case platform.IsSierraModel(model):
		return &imeidialect.ImeiDialect{
			Set:  func(imei string) string { return `AT+EGMR=1,7,"` + imei + `"` },
			Note: "Sierra 用 AT+EGMR=1,7,\"<IMEI>\"（Modem-Pro cmds/sierra.sh）；写完回读 AT+CGSN 确认。",
		}
	case platform.IsFoxconnModel(model):
		return &imeidialect.ImeiDialect{
			Set: func(imei string) string { return foxconn.IMEINV550(imei) },
			Note: "Foxconn 走 NV 550 直写（Modem-Pro vendor/foxconn.sh）：先给 IMEI 加 80A 前缀、两两字节交换、" +
				"转小写逗号分隔，再 NV 清 0 + 写入；编码规则与本实现逐字一致，" +
				"但 **NV 直写风险最高**，写完务必回读 AT+CGSN 确认。",
		}
	case platform.IsThalesModel(model):
		return nil // QModem vendor/thales.sh：ERROR: IMEI modification not supported on MV32-W
	case platform.IsHuaweiModel(model),
		vendor == iface.VendorTDTech,
		platform.PlatformForModel(model) == platform.PlatformHiSilicon:
		if v := PlatformVariantFor(iface.VendorTDTech, model); v != nil && v.Imei != nil {
			return v.Imei()
		}
		return huawei.IMEIPhynum()
	}
	return nil
}
