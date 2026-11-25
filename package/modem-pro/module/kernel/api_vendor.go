package kernel

import (
	"modemd/module/catalog"
	"modemd/module/httpx"
	"modemd/module/usbhotplug"
	"net/http"
	"strings"
)

// ---- from: api_at.go ----
// apiVendorCmds GET /api/vendor/cmds?modem_id=0
//
// 返回运行期探测出的厂商指令（同一功能有多套写法时，实际用的是哪一条）。
// 用于核对未验证型号（如 MT5700）到底走的是 +QENG 还是 +CPSI/+QCSQ。
func apiVendorCmds(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		vendor, vendorName := mod.VendorSnapshot()
		httpx.WriteJSON(w, 0, map[string]any{
			"modem_id":    mod.ID,
			"vendor":      vendor,
			"vendor_name": vendorName,
			"model":       mod.Model,
			"commands":    mod.vendorCmdSnapshot(),
		}, "ok")
	}
}

// ---- from: api_modules.go ----
// apiModules GET /api/modules[?model=<型号>][?modem_id=<n>]
//
// 「支持模块」接口。数据源是 `module/kernel/modem_catalog.json`（逐行对齐 QModem
// `docs/support_list.md`）与 `module/kernel/modem_at_plan.go`（**从真实实现读出来的**
// 每型号 AT 指令 + 解析格式）。三种用法：
//
//	不带参数            → 全部型号（厂商/型号/平台/USB 模式/PCIe 模式）+ 统计
//	?model=RM520N-GL    → 该型号的 AT 指令面（每条能力的指令、候选、解析实现、应答格式）
//	?modem_id=0         → **在网模组**的 AT 指令面（真实厂商/实现 + 运行期已选中的指令）
//
// 为什么按型号也能出（离线推导）：见 modem_at_plan.go 的 planModemForModel ——
// 厂商按 ATI 特征串的同一套规则推，推不出就是"自适应 + 候选探测"，
// 所以页面上的指令就是设备上真正会走的那套。
func apiModules(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpx.WriteJSON(w, -1, nil, "method not allowed")
			return
		}
		q := r.URL.Query()

		// ① 在网模组：真实实现 + 运行期已选中的指令
		if strings.TrimSpace(q.Get("modem_id")) != "" {
			mod, err := modemFromQuery(mgr, r)
			if err != nil {
				httpx.WriteJSON(w, -1, nil, err.Error())
				return
			}
			httpx.WriteJSON(w, 0, map[string]any{"plan": ATPlanForModem(mod)}, "ok")
			return
		}

		// ② 按型号：离线推导的 AT 指令面
		if model := strings.TrimSpace(q.Get("model")); model != "" {
			httpx.WriteJSON(w, 0, map[string]any{"plan": ATPlanForModel(model)}, "ok")
			return
		}

		// ③ 全量清单（不放 AT 明细：91 个型号的计划太大，界面点开哪台再单独拉 ②）
		models, usbCount, pcieCount, vendorCount := catalog.CatalogStats()
		entries := catalog.CatalogEntries()
		rows := make([]map[string]any, 0, len(entries))
		byVendor := map[string]int{}
		for _, e := range entries {
			byVendor[e.Vendor]++
			rows = append(rows, map[string]any{
				"vendor":      e.Vendor,
				"vendor_name": e.VendorLabel(),
				"model":       e.Model,
				"platform":    e.Platform,
				"usb_modes":   e.USB,
				"pcie_modes":  e.PCIe,
				"usb_text":    e.USBText(),
				"pcie_text":   e.PCIeText(),
			})
		}
		vendors := make([]map[string]any, 0, len(catalog.CatalogVendorOrder))
		for _, v := range catalog.CatalogVendorOrder {
			vendors = append(vendors, map[string]any{
				"vendor": v,
				"name":   catalog.ModemCatalogEntry{Vendor: v}.VendorLabel(),
				"count":  byVendor[v],
			})
		}
		httpx.WriteJSON(w, 0, map[string]any{
			"count":        models,
			"usb_count":    usbCount,
			"pcie_count":   pcieCount,
			"vendor_count": vendorCount,
			"vendors":      vendors,
			"models":       rows,
		}, "ok")
	}
}

// apiATPresets GET /api/at/presets?modem_id=0
//
// 返回**本模组**的「常用 AT 指令」清单（界面上的下拉按它渲染）。
// 设计意图见 atpresets.go：这个模块只显示它自己支持的指令，别的厂商的一条都不给。
//
// 模组不存在时**不报错**：返回通用（3GPP）那一份 + 说明。
// 页面在没有模组、或模组还没识别出厂商时都要能正常渲染下拉，不该弹错误框。
func apiATPresets(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, 0, map[string]any{
				"modem_id":    -1,
				"vendor":      VendorUnknown,
				"vendor_name": VendorLabel(VendorUnknown),
				"presets":     ATPresetsFor(VendorUnknown),
			}, err.Error())
			return
		}
		vendor, vendorName := mod.VendorSnapshot()
		httpx.WriteJSON(w, 0, map[string]any{
			"modem_id":    mod.ID,
			"vendor":      vendor,
			"vendor_name": vendorName,
			"presets":     ATPresetsFor(vendor),
		}, "ok")
	}
}

// ---- from: api_usb.go ----

// USB 热插拔：设备清单 / 事件 / 手动补驱动
//
//	GET  /api/usb         当前“像模组”的 USB 设备（含还没交出串口的）+ 最近插拔事件
//	POST /api/usb/clear   清空事件列表
//	POST /api/usb/bind    手动让 usb-serial 驱动接管某个 VID/PID（Linux/OpenWrt）
//
// ⚠️ 自动检测不靠这些接口：周期巡检（server.scan_interval）每轮都会在扫串口之前
// 跑一遍 USB 层（记事件 + 必要时补驱动），接口只是给页面看的窗口。
func apiUSBList(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, 0, usbhotplug.USBHotplugInfo(), "ok")
	}
}
