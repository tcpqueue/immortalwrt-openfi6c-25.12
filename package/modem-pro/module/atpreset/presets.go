package atpreset

// ====================== 厂商级 AT 预设表 ======================
//
// 从 module/kernel/atpresets.go 搬过来：**纯数据 + 纯函数**，只依赖 iface 的厂商枚举，
// 不碰任何内核状态。两个要读 `mod.Vendor` 的方法留在 kernel（atpreset_bridge.go）。
//
// 加一家厂商 = 这里加一个 `XxxATPresets()` + 在 VendorATPresets 里加一个 case。

import (
	"strings"

	"modemd/iface"
)

type ModemVendor = iface.ModemVendor

var (
	VendorQuectel = iface.VendorQuectel
	VendorFibocom = iface.VendorFibocom
	VendorSimcom  = iface.VendorSimcom
	VendorMeig    = iface.VendorMeig
	VendorTDTech  = iface.VendorTDTech
	VendorUnisoc  = iface.VendorUnisoc
)

type ATPreset struct {
	// Group 分组名（界面上是 <optgroup>）
	Group string `json:"group"`
	// Cmd 指令原文（选中后填进输入框，用户可再改）
	Cmd string `json:"cmd"`
	// Desc 中文说明
	Desc string `json:"desc"`
	// Tpl 是否是带占位符的模板（选中后必须先把 `<…>` 换掉）
	Tpl bool `json:"tpl,omitempty"`
}

// Of 造一条预设（缩写只为让下面的表格能对齐着读）
func Of(group, cmd, desc string) ATPreset {
	return ATPreset{Group: group, Cmd: cmd, Desc: desc}
}

// Tpl 造一条**模板**预设（Cmd 里有 `<…>` 占位符，不能直接发）
func Tpl(group, cmd, desc string) ATPreset {
	return ATPreset{Group: group, Cmd: cmd, Desc: desc, Tpl: true}
}

// CommonATPresets 3GPP 标准指令：任何规规矩矩的模组都该支持。
//
// 清单里只放"3GPP 有定义"的（TS 27.007 / TS 27.005），一条厂商私有的都不放 ——
// 厂商私有的都在各自的 VendorATPresets 里。
func CommonATPresets() []ATPreset {
	return []ATPreset{
		Of("查询", "AT+CGSN", "IMEI"),
		Of("查询", "AT+CGMM", "模组型号"),
		Of("查询", "AT+CGMR", "固件版本"),
		Of("查询", "AT+CPIN?", "SIM 卡状态"),
		Of("查询", "AT+CCID", "ICCID（卡号）"),
		Of("查询", "AT+CIMI", "IMSI"),
		Of("查询", "AT+CNUM", "本机号码（数据卡多为空）"),
		Of("查询", "AT+CSQ", "信号强度 CSQ"),
		Of("查询", "AT+CESQ", "扩展信号质量（RSSI/RSRP/RSRQ/SINR）"),
		Of("查询", "AT+CGATT?", "PS 域附着状态"),
		Of("查询", "AT+CREG?", "CS 域注册状态"),
		Of("查询", "AT+CGREG?", "PS 域注册状态"),
		Of("查询", "AT+CEREG?", "EPS/LTE 注册状态"),
		Of("查询", "AT+COPS?", "当前运营商"),
		Of("查询", "AT+CGDCONT?", "PDP 上下文（APN 配置）"),
		Of("查询", "AT+CGCONTRDP", "已激活的 PDP 承载（含本机 IP）"),
		Of("查询", "AT+CGACT?", "PDP 激活状态"),
		Of("查询", "AT+CGPADDR", "本机 IP 地址"),
		Of("查询", "AT+CCLK?", "模组时钟"),
		Of("查询", "AT+CFUN?", "功能模式"),
		Of("查询", "AT+CMEE?", "错误码详细程度"),
		Of("查询", "AT+CMGF?", "短信模式（0=PDU / 1=文本）"),
		Of("查询", "AT+CPMS?", "短信存储用量"),
		Of("查询", "AT+CSCS?", "当前字符集"),
		Of("查询", "AT+CUSD=?", "USSD 支持情况"),
		Of("设置（谨慎）", "AT+CMEE=2", "打开详细错误码（出问题时报得更清楚）"),
		Of("设置（谨慎）", "AT+COPS=0", "自动选网"),
		Of("设置（谨慎）", "AT+CFUN=1,1", "重启模组（会断网，好几分钟才回来）"),
	}
}

// Combine 把「通用清单」和「本厂商私有清单」合并成界面要的那份：
// 厂商清单里同名（不分大小写）的项覆盖通用项，suppressed 里的通用项直接摘掉。
//
// 合并规则只此一份；**哪家有哪家的清单**在各自的品牌包里（见 module/kernel/atpreset_bridge.go）。
func Combine(vendorOnly []ATPreset, suppressed []string) []ATPreset {
	common := CommonATPresets()
	if len(vendorOnly) == 0 && len(suppressed) == 0 {
		return common
	}
	override := make(map[string]bool, len(vendorOnly))
	for _, p := range vendorOnly {
		override[strings.ToUpper(strings.TrimSpace(p.Cmd))] = true
	}
	drop := make(map[string]bool, len(suppressed))
	for _, c := range suppressed {
		drop[strings.ToUpper(strings.TrimSpace(c))] = true
	}
	out := make([]ATPreset, 0, len(common)+len(vendorOnly))
	for _, p := range common {
		k := strings.ToUpper(strings.TrimSpace(p.Cmd))
		if override[k] || drop[k] {
			continue
		}
		out = append(out, p)
	}
	return append(out, vendorOnly...)
}
