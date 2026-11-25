package kernel

import (
	"fmt"
	"strings"

	"modemd/module/common"
	"modemd/module/imeidialect"
	"modemd/module/variant"
)

// ====================== 厂商设置方言表（USB 网卡模式 / 组网模式偏好 / IMEI 改写） ======================
//
// 这三件事**每家指令都不同**，而且同一厂商不同平台也常不同（广和通/芯讯通/移远都是一家多套）。
// 本文件把它们做成"按厂商（+平台/机型）选方言"的**数据表**，而不是散在各 Command 类型里，
// 这样新增机型只要加一条表项。
//
// 全部依据 QModem 的 `cmds/*.sh` + `vendor/*.sh` 原文（2026-10 用 GitHub API 拉整文件核对），
// 每条表项都注明来源行号；**没有证据的取值一律不臆造**：能读的照读，写入只接受"原码"
// （把设备自己报的值原样写回），并在界面上说明。
//
// ⚠️ 未列出的厂商 = 这三项都不支持（界面按钮隐藏 + 说明原因），不会乱发 AT。

// imeidialect.ImeiDialect / imeidialect.NumInLine / imeidialect.LineHas / imeidialect.ReHex4 / imeidialect.RawValueParser 已搬到
// modemd/module/imeidialect（见 module/imeidialect/dialect.go）；
// kernel 保留同名别名，见 imeidialect_alias.go。

// common.FirstLineWith 已搬到 modemd/module/common（FirstLineWith）；
// kernel 保留同名别名，见 lock_alias.go。

// imeidialect.ReHex4 / imeidialect.RawValueParser 已搬到 modemd/module/imeidialect；kernel 保留同名别名。

// ====================== IMEI：各家改写指令 ======================

// imeiDialectFor 取厂商的 IMEI 改写方言（nil = 本机不支持改 IMEI）
//
// 判定顺序：厂商常量（VendorXxx）→ 机型关键字（isXxxModel）→ 平台（PlatformForModel）。
// 三层的存在原因：项目里只有 Quectel/Fibocom/Simcom/Meig/TDTech 五个厂商常量，
// 高新兴/有方/Telit/Thales/Foxconn/Sierra/openluat 都归在 VendorUnknown 靠机型认。
// imeiDialectFor 取厂商的 IMEI 改写方言（nil = 本机不支持改 IMEI）。
//
// 分发表已搬到 modemd/module/variant（见 module/variant/dispatch.go）。
func imeiDialectFor(mod *Modem) *imeidialect.ImeiDialect {
	if mod == nil {
		return nil
	}
	return variant.IMEIDialectFor(mod.Vendor, mod.Model+" "+mod.ModelName)
}

// ---------- 广和通：三档 IMEI 指令（QModem vendor/fibocom.sh set_imei 的 case $platform）----------

// 四家 IMEI 方言构造函数已搬到 modemd/module/imeidialect
// （FibocomIMEIGTSN / FibocomIMEIEGMREXT / FibocomIMEILCTSN / HuaweiIMEIPhynum）；
// kernel 保留原名别名，见 imeidialect_alias.go。

// foxconn.IMEINV550 / imeidialect.ValidIMEI 已搬到 modemd/module/imeidialect；kernel 保留同名别名。
func imeiApply(mod *Modem, cmd string) error {
	for _, one := range strings.Split(cmd, ";") {
		one = strings.TrimSpace(one)
		if one == "" {
			continue
		}
		resp, err := mod.SendAT(one)
		if err != nil {
			return err
		}
		if common.RespHasError(resp) {
			return fmt.Errorf("模组拒绝 %s：%s", one, FirstLine(resp))
		}
	}
	return nil
}

// imeidialect.ValidIMEI 已搬到 modemd/module/imeidialect（见 module/imeidialect/dialect.go）；
// kernel 保留同名别名，见 imeidialect_alias.go。
