package tdtech

// ====================== 鼎桥 MT5700 的私有应答解析 ======================
//
// 手册：《MT5700M-CN 5G 系列模组 AT 命令手册》
//
//	§13.18 AT^CASCELLINFO?  LTE CA 状态下 SCELL 信息（最多 4 个 SCell）
//	§13.27 AT^MONSSC        NSA(EN-DC) 情况下查询辅连接服务小区（PSCell + SCell，可多行）
//
//	^CASCELLINFO: <index>,<pci>,<rssi>,<rsrp>,<rsrq>,<band_class>,
//	              <ulfreqpoint>,<dlfreqpoint>,<ulfreq>,<dlfreq>,<ulbw>,<dlbw>
//	^MONSSC: NR,<ARFCNNR>,<PCI 十六进制>,<RSRP>,<RSRQ>,<SINR>,<MEASTYPE>
//	^MONSSC: NONE                     ← 当前不支持/没有辅连接
//
// 与移远（AT+QCAINFO）最大的不同：
//  1. **只有辅小区**，没有 PCC 行（主载波要看服务小区），所以这里全部按 SCC 填；
//  2. band 是十进制数字（`band_class`），不是 "LTE BAND 3" 这种字符串；
//  3. **"没有聚合"用 ERROR / NONE 表示**（手册明确），不是空应答 ——
//     所以拿到 ERROR 要当成"当前没有 CA"，而不是"指令不支持"；
//  4. 本固件**没有 NR SA 的载波聚合查询**：CASCELLINFO 只覆盖 LTE CA，
//     MONSSC 只在 NSA(EN-DC) 下有内容。

import (
	"regexp"
	"strconv"
	"strings"

	"modemd/iface"
	"modemd/module/common"
)

var (
	reCASCellInfo = regexp.MustCompile(`(?i)\^CASCELLINFO:\s*(.+)$`)
	reMonssc      = regexp.MustCompile(`(?i)\^MONSSC:\s*(.+)$`)
)

// lteBandwidthMHz 手册 §13.18.3 的带宽代号：0=1.4M 1=3M 2=5M 3=10M 4=15M 5=20M。
// CCInfo.Bandwidth 全项目统一用 MHz（移远 QCAINFO 就是直接给 MHz），所以这里换算；
// 1.4MHz 这种 LTE 老带宽按 1 存（字段是 int，且 5G 模组上不会出现）。
var lteBandwidthMHz = [...]int{1, 3, 5, 10, 15, 20}

// ParseCASCellInfo 解析 `AT^CASCELLINFO?`（LTE CA 的 SCell）。
//
// 手册样例（2CC，一个 SCell）：
//
//	^CASCELLINFO: 1,417,-60,-80,-5,18,23925,5925,8225,8675,5,5
//
// 字段：index,pci,rssi,rsrp,rsrq,band_class,ulfreqpoint,dlfreqpoint,ulfreq,dlfreq,ulbw,dlbw
// ⚠️ 注意手册里 `ulfreqpoint`(23925) 比 `dlfreqpoint`(5925) 大、而 `ulfreq`(8225) 比 `dlfreq`(8675) 小，
// 所以**频点必须按名字取下标，不能按"上行在前"的顺序猜**。频段用 band_class 直接拿，
// 不用频点反推（LTE 频点 5925 反推是 B3，与 band_class 18 不同 —— 手册例子本身不一致，以 band_class 为准）。
func ParseCASCellInfo(resp []string) []iface.CCInfo {
	var out []iface.CCInfo
	for _, line := range resp {
		m := reCASCellInfo.FindStringSubmatch(line)
		if m == nil {
			continue // ERROR / OK / URC：没有 CA
		}
		f := common.SplitATFields(m[1])
		if len(f) < 12 {
			continue
		}
		cc := iface.CCInfo{
			Role:   "SCC", // 这条指令只报辅小区，主载波在服务小区里
			Tech:   iface.CellTechLTE,
			Raw:    strings.TrimSpace(line),
			PCID:   common.AtoiOr0(f[1]),
			RSSI:   common.AtoiOr0(f[2]),
			RSRP:   common.AtoiOr0(f[3]),
			RSRQ:   common.AtoiOr0(f[4]),
			Band:   common.AtoiOr0(f[5]),
			EARFCN: common.AtoiOr0(f[7]), // dlfreqpoint：下行频点
		}
		if cc.Band > 0 {
			cc.BandLabel = common.BandLabel(cc.Tech, cc.Band)
		}
		if bw := common.AtoiOr0(f[11]); bw >= 0 && bw < len(lteBandwidthMHz) {
			cc.Bandwidth = lteBandwidthMHz[bw]
		}
		out = append(out, cc)
	}
	return out
}

// ParseMONSSC 解析 `AT^MONSSC`（NSA 辅连接小区，可能多行）。
//
// 手册样例（2CC）：
//
//	^MONSSC: NR,633988,1,-70,-20,-10,0
//	^MONSSC: NR,633400,2,-68,-10,1,0
//
// 字段：RAT,ARFCN,PCI(十六进制),RSRP,RSRQ,SINR,MEASTYPE
// `^MONSSC: NONE` = 没有辅连接；信号是 -1256/-348/-188 这种哨兵值时表示"还没测到"。
func ParseMONSSC(resp []string) []iface.CCInfo {
	var out []iface.CCInfo
	for _, line := range resp {
		m := reMonssc.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		f := common.SplitATFields(m[1])
		rat := strings.ToUpper(f[0])
		if rat != "NR" || len(f) < 7 {
			continue // NONE / 未支持的制式（LTE 侧手册说"暂不支持"）
		}
		cc := iface.CCInfo{
			Role:   "SCC", // NSA 下这些是 SCG 的辅小区（PSCell 也是辅连接侧）
			Tech:   iface.CellTechNR,
			Raw:    strings.TrimSpace(line),
			EARFCN: common.AtoiOr0(f[1]),
		}
		// PCI 是十六进制（手册 §13.27.3）
		if v, err := strconv.ParseInt(f[2], 16, 32); err == nil {
			cc.PCID = int(v)
		}
		cc.RSRP = common.DropSentinel(common.AtoiOr0(f[3]), -1250, -156, -25)
		cc.RSRQ = common.DropSentinel(common.AtoiOr0(f[4]), -340, -45, 25)
		cc.SINR = common.DropSentinel(common.AtoiOr0(f[5]), -185, -25, 45)
		if cc.EARFCN > 0 {
			cc.Band = iface.BandFromEARFCN(cc.Tech, cc.EARFCN)
			if cc.Band > 0 {
				cc.BandLabel = common.BandLabel(cc.Tech, cc.Band)
			}
		}
		out = append(out, cc)
	}
	return out
}
