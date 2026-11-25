package iface

// ---- 小区接入技术 ----

// CellTech 小区接入技术
type CellTech string

const (
	CellTechLTE   CellTech = "LTE"
	CellTechNR    CellTech = "NR" // 5G（含模组上报的 NR5G-SA / NR5G-NSA / NR5G）
	CellTechWCDMA CellTech = "WCDMA"
	CellTechGSM   CellTech = "GSM"
	// CellTechNRNSA NR **NSA**（EN-DC）的独立频段列表。
	//
	// 只有"NR 分成 NSA / SA 两套列表"的方言才认它（移远/高通 QNW：
	// `AT+QNWPREFCFG="nsa_nr5g_band"`；芯讯通：`AT+CSYSSEL="nsa_nr5g_band"`）。
	// 广和通 GT（AT+GTACT）/ 展锐 SP* / 鼎桥 ^LTEFREQLOCK·^NRFREQLOCK / Sierra AT!BAND
	// 只有**一套** NR 频段，它们的查询/设置对本技术域必须不产生指令 ——
	// 由 `NSAIndependent` 接口声明（见 lock.go），发动机层统一拦截。
	CellTechNRNSA CellTech = "NRNSA"
)

// AllCellTechs 全部技术域（LTE / NR / NRNSA / WCDMA / GSM）。
// ⚠️ 顺序即界面顺序，别随意调整。
var AllCellTechs = []CellTech{
	CellTechLTE,
	CellTechNR,
	CellTechNRNSA,
	CellTechWCDMA,
	CellTechGSM,
}

// ---- 载波聚合（CA）相关 ----

// CCInfo 一个分量载波（Component Carrier）
type CCInfo struct {
	Role      string   `json:"role"`                 // PCC（主载波）/ SCC（辅载波）
	Tech      CellTech `json:"tech,omitempty"`       // LTE / NR
	Band      int      `json:"band,omitempty"`       // 频段号
	BandText  string   `json:"band_text,omitempty"`  // 模组原始串 "NR5G BAND 78"
	BandLabel string   `json:"band_label,omitempty"` // n78 / B3
	Bandwidth int      `json:"bandwidth,omitempty"`  // 带宽（模组上报值；LTE 为 MHz）
	EARFCN    int      `json:"earfcn,omitempty"`     // 频点（LTE EARFCN / NR ARFCN）
	PCID      int      `json:"pcid,omitempty"`       // 物理小区 ID
	RSRP      int      `json:"rsrp,omitempty"`       // 以下信号值可能缺失
	RSRQ      int      `json:"rsrq,omitempty"`
	RSSI      int      `json:"rssi,omitempty"`
	SINR      int      `json:"sinr,omitempty"`
	Raw       string   `json:"raw,omitempty"` // 原始 AT 行，便于核对
}
