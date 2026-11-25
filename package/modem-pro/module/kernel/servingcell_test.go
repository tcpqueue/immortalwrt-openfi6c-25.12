package kernel

// “服务小区”从 URC 拼装的回归测试。
//
// ⚠️ 边界必须钉住：URC 里**只有** PCI(`+SPPCI`) / 小区 ID+TAC(`+CxREG`) / 信号(`+CESQ`)。
// 频段、频点、带宽、邻区**没有任何文件来源**（真机取证：QNWINFO/CPSI 都不支持、
// 固件里 EARFCN/SSB 只是内部变量名），所以这些字段必须保持 0 —— 不能被“顺手估一个”。

import (
	"modemd/module/cell"
	"testing"
	"time"
)

// 真机样本：`+SPPCI: 0000009B` → PCI 155；`0000008C` → 140。
func TestURCPCIFromLine(t *testing.T) {
	cases := []struct {
		line string
		pci  int
		ok   bool
	}{
		{"+SPPCI: 0000009B", 155, true},
		{"+SPPCI: 0000008C", 140, true},
		{"+SPPCI: 00000000", 0, true}, // PCI 0 是合法的
		{"+SPPCI: 000003EF", 1007, true},
		{"+SPPCI: 00000400", 0, false}, // 1008 超出 PCI 范围
		{"+SPPCI:", 0, false},
		{"+SPPCI: zzzz", 0, false},
		{"+CEREG: 1,\"CFA102\"", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		pci, ok := urcPCIFromLine(c.line)
		if ok != c.ok || pci != c.pci {
			t.Errorf("urcPCIFromLine(%q) = %d,%v，期望 %d,%v", c.line, pci, ok, c.pci, c.ok)
		}
	}
}

// 端到端：从真机样本拼出服务小区（PCI/小区ID/TAC/信号/制式/运营商）。
func TestServingCellFromURC(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 255,99`)                          // 哨兵，不该污染
	mod.feedURCLine(`+CESQ: 99,99,255,255,255,255,59,54,54`) // NR 扩展槽
	mod.feedURCLine(`+CEREG: 1,"CFA102","0A11A009",11`)      // TAC + CI + AcT=5G
	mod.feedURCLine(`+SPPCI: 0000009B`)                      // PCI 155
	mod.feedURCLine(realSPNWNAME)                            // 460,11 → 中国电信

	ci := mod.servingCellFromURC()
	if ci == nil {
		t.Fatal("有 PCI 和小区 ID 时应能拼出服务小区")
	}
	if !ci.Serving {
		t.Error("必须是服务小区")
	}
	if ci.PCID != 155 {
		t.Errorf("PCI 应为 155，实际 %d", ci.PCID)
	}
	if ci.CellID != "0A11A009" {
		t.Errorf("小区 ID 应原样带上，实际 %q", ci.CellID)
	}
	if ci.TAC != 0xCFA102 {
		t.Errorf("TAC 应按十六进制解，实际 %d", ci.TAC)
	}
	if ci.Tech != CellTechNR {
		t.Errorf("制式应来自 AcT=11（5G），实际 %q", ci.Tech)
	}
	if ci.MCC != "460" || ci.MNC != "11" || ci.Operator != "中国电信" {
		t.Errorf("运营商应来自 +SPNWNAME，实际 %q/%q %q", ci.MCC, ci.MNC, ci.Operator)
	}
	if ci.RSRP == 0 || ci.SINR == 0 {
		t.Errorf("信号应来自 CESQ，实际 rsrp=%d sinr=%d", ci.RSRP, ci.RSRQ)
	}
	// ⚠️ 没有来源的字段必须是 0（不估）
	if ci.Band != 0 || ci.EARFCN != 0 || ci.Bandwidth != 0 {
		t.Errorf("频段/频点/带宽 无文件来源，必须留 0，实际 band=%d earfcn=%d bw=%d",
			ci.Band, ci.EARFCN, ci.Bandwidth)
	}
	// RSSI 也不填：+CSQ 第一个字段在这台机上不是标准索引（34/35/255）
	if ci.RSSI != 0 {
		t.Errorf("RSSI 不该从非标准 CSQ 换算，实际 %d", ci.RSSI)
	}
}

// 什么都没有时返回 nil（宁可不显示也不编）。
func TestServingCellFromURCEmpty(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	if ci := mod.servingCellFromURC(); ci != nil {
		t.Errorf("没有任何来源时应返回 nil，实际 %+v", ci)
	}
	// 只有 PCI、没有注册信息：仍然算有（PCI 本身就能定位小区）
	mod.feedURCLine(`+SPPCI: 0000009B`)
	if ci := mod.servingCellFromURC(); ci == nil || ci.PCID != 155 {
		t.Errorf("只有 PCI 时也该给出小区（PCI 就是小区身份），实际 %+v", ci)
	}
	if ci := mod.servingCellFromURC(); ci != nil && ci.CellID != "" {
		t.Errorf("没有注册上报时小区 ID 应为空，实际 %q", ci.CellID)
	}
}

// 服务小区要真的落到接口返回里；但**不能覆盖**已有的扫描缓存（扫描结果更全）。
func TestNetworkStatusServingCellFromURC(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	mod.feedURCLine(`+CGREG: 1,"0000","0A11A009",11`)
	mod.feedURCLine(`+SPPCI: 0000008C`)

	st := mod.networkStatusFromURC()
	if st.Serving == nil || st.Serving.PCID != 140 || !st.Serving.Serving {
		t.Fatalf("接口应带上服务小区，实际 %+v", st.Serving)
	}
	if st.ServingCount != 1 {
		t.Errorf("服务小区计数应为 1，实际 %d", st.ServingCount)
	}
	if st.ScannedAt.IsZero() {
		t.Error("ScanndeAt 要填（前端据此标注数据新觜度）")
	}

	// 有扫描缓存时：更全的那份优先，不能被 URC 这份部分数据覆盖
	mod.cacheNetworkStatus(&NetworkStatus{
		ModemID:      mod.ID,
		QueriedAt:    time.Now(),
		Serving:      &cell.CellInfo{Serving: true, PCID: 999, Band: 78, EARFCN: 627264},
		ServingCount: 1,
	})
	st = mod.networkStatusFromURC()
	if st.Serving == nil || st.Serving.PCID != 999 || st.Serving.Band != 78 {
		t.Errorf("已有扫描缓存时应优先用它，实际 %+v", st.Serving)
	}
}

// 自己拼的那份必须**能刷新**（小区换了 PCI 要跟着变）；
// 而扫描来的那份绝不能被覆盖 —— 两者靠 Raw 前缀区分。
func TestServingCellFromURCRefreshesItsOwn(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	mod.feedURCLine(`+CGREG: 1,"0000","0A11A009",11`)
	mod.feedURCLine(`+SPPCI: 0000009B`) // PCI 155
	mod.feedURCLine(`+SPCLB: 5`)

	st := mod.networkStatusFromURC()
	if st.Serving == nil || st.Serving.PCID != 155 || st.Serving.Band != 5 {
		t.Fatalf("首次应拼出 PCI=155 band=5，实际 %+v", st.Serving)
	}
	// 接口会把这份写进缓存（pushNetworkState → cacheNetworkStatus）——模拟这一步
	mod.cacheNetworkStatus(st)

	// 小区换了：PCI 155→140、band 5→3 ⇒ 必须跟着刷新，不能被"已有服务小区"挡住
	mod.feedURCLine(`+SPPCI: 0000008C`)
	mod.feedURCLine(`+SPCLB: 3`)
	st = mod.networkStatusFromURC()
	if st.Serving == nil || st.Serving.PCID != 140 {
		t.Errorf("小区换了，PCI 应刷新成 140，实际 %+v", st.Serving)
	}
	if st.Serving.Band != 3 {
		t.Errorf("频段应跟着刷新成 3，实际 %d", st.Serving.Band)
	}

	// 但**扫描来的**那份（Raw 不带 urc: 前缀）绝不能被覆盖
	mod.cacheNetworkStatus(&NetworkStatus{
		ModemID:      mod.ID,
		QueriedAt:    time.Now(),
		Serving:      &cell.CellInfo{Serving: true, PCID: 777, Band: 78, EARFCN: 627264, Raw: "AT+QENG"},
		ServingCount: 1,
	})
	st = mod.networkStatusFromURC()
	if st.Serving == nil || st.Serving.PCID != 777 || st.Serving.EARFCN != 627264 {
		t.Errorf("扫描结果不该被 URC 那份覆盖，实际 %+v", st.Serving)
	}
}
