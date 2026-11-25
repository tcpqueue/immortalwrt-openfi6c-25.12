package platform

// 本文件是「清单里的厂商字符串 → 厂商常量」的映射，原先写在 module/kernel/modem_at_plan.go
// 的 planModemForModel 里（对 *Modem 无关的纯字符串映射，本就不该留在内核）。
//
// 为什么要按型号再分一次（huawei 一档）：清单里 huawei 这个键同时包含两家 ——
// 鼎桥自有型号（MT5700/MT5710）ATI 里带 "TD Tech" 会被识别成 TDTech，指令集按 TDTech 取；
// 华为自有型号（MH5000/S10）ATI 报华为却落不到任何厂商常量 → 走自适应指令集 + 候选探测。
// 所以这一档不能简单一一对应。

import (
	"strings"

	"modemd/iface"
)

// ModemVendorForCatalog 清单里的厂商字符串（+ 型号）→ 厂商常量（认不出返回 VendorUnknown）。
func ModemVendorForCatalog(vendor, model string) iface.ModemVendor {
	switch strings.ToLower(strings.TrimSpace(vendor)) {
	case "quectel":
		return iface.VendorQuectel
	case "fibocom":
		return iface.VendorFibocom
	case "simcom":
		return iface.VendorSimcom
	case "meig":
		return iface.VendorMeig
	case "huawei":
		u := strings.ToUpper(model)
		if strings.Contains(u, "MT5700") || strings.Contains(u, "MT5710") {
			return iface.VendorTDTech
		}
		return iface.VendorUnknown
	}
	return iface.VendorUnknown
}
