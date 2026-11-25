package kernel

import (
	"errors"
	"modemd/module/cell"
	"modemd/module/config"
	"modemd/module/simcom"
	"strings"
	"testing"
	"time"
)

// ====================== 主动扫网（AT+COPS=? + 芯讯通 AT+CNWSEARCH） ======================

// TestParseCOPSList 解析 `AT+COPS=?` 的可见运营商列表
func TestParseCOPSList(t *testing.T) {
	// 真机形态：每项 (状态,长名,短名,号码[,ACT])；末尾那组是"本机支持的技术"，不是运营商
	resp := []string{
		"AT+COPS=?",
		`+COPS: (2,"CHN-UNICOM","UNICOM","46001",7),(1,"CHN-MOBILE","CMCC","46000",7),,(0-1,2)`,
		"OK",
	}
	ops := ParseCOPSList(resp)
	if len(ops) != 2 {
		t.Fatalf("运营商数 = %d（%+v），want 2", len(ops), ops)
	}
	// 当前在网(2) 排最前
	if ops[0].Long != "CHN-UNICOM" || ops[0].Status != 2 || ops[0].Numeric != "46001" || ops[0].ACT != "7" {
		t.Errorf("第一项 = %+v", ops[0])
	}
	if ops[1].Long != "CHN-MOBILE" || ops[1].Short != "CMCC" {
		t.Errorf("第二项 = %+v", ops[1])
	}
	// 多行 / 空项 / 垃圾不应崩，也不该造出假运营商
	multi := []string{
		`+COPS: (1,"A","AA","11111"),`,
		`(2,"B","BB","22222",7)`,
		"OK",
	}
	if ops := ParseCOPSList(multi); len(ops) != 2 || ops[0].Long != "B" {
		t.Errorf("多行解析 = %+v", ops)
	}
	// 真机形态（广和通 Fibocom）：末尾的 `(0,1,2,3,4,5)` / `(0,1,2)` 是"支持的技术"，
	// 不能变成一行运营商 —— 实测界面会多出「1 · 3 · ACT 4 · 未知」。
	real := []string{
		`+COPS: (1,"????","????","46011",12),(1,"CHN-CT","CT","46011",7),,(0,1,2,3,4,5),(0,1,2)`,
		"OK",
	}
	if ops := ParseCOPSList(real); len(ops) != 2 {
		t.Errorf("真机形态应只解析出 2 个运营商，实际 %d：%+v", len(ops), ops)
	}
	for _, bad := range [][]string{
		{"OK"},
		{"+COPS: ,"},
		{`+COPS: (0-1,2)`},          // 只有"支持的技术"
		{`+COPS: (2,"","","")`},     // 全是空
		{`+COPS: (x,"A","B","12")`}, // 状态不是数字
	} {
		if ops := ParseCOPSList(bad); len(ops) != 0 {
			t.Errorf("%v 不该解析出运营商：%+v", bad, ops)
		}
	}
}

// TestParseCNWSEARCHCells 解析芯讯通主动扫描输出（字段下标取自 QModem vendor/simcom.sh）
func TestParseCNWSEARCHCells(t *testing.T) {
	resp := []string{
		`AT+CNWSEARCH="nr5g",3`,
		"+NR_NGH_CELL:627264,521,-89,-11,13",
		"+NR_NGH_CELL:633984,397,-95,-12,9",
		"+LTE_CELL:00,1,2,3,1650,155,-98,-9,60",
		"OK",
	}
	cells := ParseCNWSEARCHCells(resp)
	if len(cells) != 3 {
		t.Fatalf("小区数 = %d（%+v），want 3", len(cells), cells)
	}
	nr := cells[0]
	if nr.Tech != CellTechNR || nr.EARFCN != 627264 || nr.PCID != 521 || nr.RSRP != -89 || nr.RSRQ != -11 {
		t.Errorf("NR 小区解析 = %+v", nr)
	}
	if nr.Raw == "" {
		t.Error("应保留原始行（界面核对用）")
	}
	// LTE 行：f[3]=band, f[4]=arfcn, f[5]=pci, f[6]=rsrp, f[7]=rsrq
	var lte cell.CellInfo
	for _, c := range cells {
		if c.Tech == CellTechLTE {
			lte = c
		}
	}
	if lte.Band != 3 || lte.EARFCN != 1650 || lte.PCID != 155 || lte.RSRP != -98 || lte.RSRQ != -9 {
		t.Errorf("LTE 小区解析 = %+v", lte)
	}
	// 越界/非法值一律留空（宁可空白也不显示错数据）
	junk := ParseCNWSEARCHCells([]string{
		"+NR_NGH_CELL:99999999,9999,-1,-999",   // 全部越界
		"+LTE_CELL:00,1,2,3,99999,9999,-10,-1", // arfcn/pci 越界
		"+NR_NGH_CELL:",                        // 空
		"OK",
	})
	for _, c := range junk {
		if c.Tech == CellTechNR && (c.EARFCN != 0 || c.PCID != 0) {
			t.Errorf("NR 越界值应留空：%+v", c)
		}
		if c.Tech == CellTechLTE && (c.EARFCN != 0 || c.PCID != 0 || c.Band != 0) {
			t.Errorf("LTE 越界值应留空：%+v", c)
		}
	}
}

// TestScanNetworkFull 主动扫网全流程（假模组）：运营商 + 扫小区 + 步骤/原始应答
func TestScanNetworkFull(t *testing.T) {
	withATTimeout(t)
	// 等待时长改小，避免测试真的睡 10 秒
	oldNR, oldLTE := netScanWaitNR, netScanWaitLTE
	netScanWaitNR, netScanWaitLTE = time.Millisecond, time.Millisecond
	t.Cleanup(func() { netScanWaitNR, netScanWaitLTE = oldNR, oldLTE })

	script := func(cmd string) []string {
		switch {
		case strings.HasPrefix(cmd, "AT+COPS=?"):
			return []string{cmd, `+COPS: (2,"CHN-UNICOM","UNICOM","46001",7),(1,"CHN-MOBILE","CMCC","46000",7),,(0-1,2)`, "OK"}
		case cmd == `AT+CNWSEARCH="lte"`:
			return []string{cmd, "OK"}
		case cmd == `AT+CNWSEARCH="lte",1`:
			return []string{cmd, "+LTE_CELL:00,1,2,3,1650,155,-98,-9,60", "OK"}
		}
		return []string{cmd, "OK"}
	}
	mod := &Modem{ID: 0, cmdImpl: &simcom.SimcomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	sent := fakeATServe(mod, script)

	res, err := mod.ScanNetwork(NetScanLTE, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Operators) != 2 {
		t.Errorf("运营商 = %+v", res.Operators)
	}
	if len(res.Cells) != 1 || res.Cells[0].PCID != 155 || res.Cells[0].Band != 3 {
		t.Errorf("扫到的小区 = %+v", res.Cells)
	}
	if res.OperatorCm != "AT+COPS=?" || res.CellCmd != `AT+CNWSEARCH="lte",1` {
		t.Errorf("指令记录不对：%q / %q", res.OperatorCm, res.CellCmd)
	}
	if len(res.Steps) < 3 || len(res.Raw) == 0 {
		t.Errorf("应记录步骤与原始应答：steps=%v raw=%v", res.Steps, res.Raw)
	}
	// 真的按 QModem 的流程发了：先 query 再 scan
	cmds := drainSent(sent)
	wantSeq := []string{"AT+COPS=?", `AT+CNWSEARCH="lte"`, `AT+CNWSEARCH="lte",1`}
	for i, w := range wantSeq {
		if i >= len(cmds) || cmds[i] != w {
			t.Fatalf("下发顺序 = %v，want %v", cmds, wantSeq)
		}
	}
}

// TestScanNetworkUnsupportedCellScan 固件不认 AT+CNWSEARCH：只给运营商 + 如实说明原因
func TestScanNetworkUnsupportedCellScan(t *testing.T) {
	withATTimeout(t)
	oldNR, oldLTE := netScanWaitNR, netScanWaitLTE
	netScanWaitNR, netScanWaitLTE = time.Millisecond, time.Millisecond
	t.Cleanup(func() { netScanWaitNR, netScanWaitLTE = oldNR, oldLTE })

	script := func(cmd string) []string {
		switch {
		case strings.HasPrefix(cmd, "AT+COPS=?"):
			return []string{cmd, `+COPS: (2,"A","AA","11111"),`, "OK"}
		case strings.HasPrefix(cmd, "AT+CNWSEARCH"):
			return []string{cmd, "ERROR"}
		}
		return []string{cmd, "OK"}
	}
	mod := &Modem{ID: 0, cmdImpl: &simcom.SimcomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	sent := fakeATServe(mod, script)

	res, err := mod.ScanNetwork(NetScanNR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Operators) != 1 {
		t.Errorf("运营商 = %+v", res.Operators)
	}
	if len(res.Cells) != 0 || res.CellCmd != "" {
		t.Errorf("不支持扫小区时不该有小区结果：%+v cmd=%q", res.Cells, res.CellCmd)
	}
	if !strings.Contains(res.CellNote, "实时扫描") {
		t.Errorf("应指路到「实时扫描」，实际 %q", res.CellNote)
	}
	// 不支持时**不该再往下发**带模式的扫描指令（省掉 5~10 秒白等）
	for _, c := range drainSent(sent) {
		if strings.Contains(c, `AT+CNWSEARCH="nr5g",`) {
			t.Errorf("不该发 %q", c)
		}
	}
}

// TestScanNetworkCOpsFailure COPS=? 超时/无结果：不致命，返回结构仍完整
func TestScanNetworkCOpsFailure(t *testing.T) {
	oldTO := netScanCOpsTimeout
	netScanCOpsTimeout = 30 * time.Millisecond
	t.Cleanup(func() { netScanCOpsTimeout = oldTO })
	oldNR, oldLTE := netScanWaitNR, netScanWaitLTE
	netScanWaitNR, netScanWaitLTE = time.Millisecond, time.Millisecond
	t.Cleanup(func() { netScanWaitNR, netScanWaitLTE = oldNR, oldLTE })
	withATTimeout(t)
	config.Cfg.Serial.CmdTimeout = 20 * time.Millisecond

	script := func(cmd string) []string {
		if strings.HasPrefix(cmd, "AT+COPS=?") {
			return []string{cmd} // 不给最终应答 → 超时
		}
		return []string{cmd, "ERROR"}
	}
	mod := &Modem{ID: 0, cmdImpl: &simcom.SimcomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	fakeATServe(mod, script)

	res, err := mod.ScanNetwork(NetScanLTE, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Operators) != 0 {
		t.Errorf("超时不该有运营商结果：%+v", res.Operators)
	}
	if res.Note == "" {
		t.Error("失败要给用户一句说明")
	}
	if res.DurationMs < 0 {
		t.Errorf("耗时应非负，实际 %d", res.DurationMs)
	}
}

// TestScanNetworkTechNormalize 只认 LTE / NR；其它值按 LTE 处理（前端下拉只有这两项）
func TestScanNetworkTechNormalize(t *testing.T) {
	withATTimeout(t)
	oldNR, oldLTE := netScanWaitNR, netScanWaitLTE
	netScanWaitNR, netScanWaitLTE = time.Millisecond, time.Millisecond
	t.Cleanup(func() { netScanWaitNR, netScanWaitLTE = oldNR, oldLTE })

	mod := &Modem{ID: 0, cmdImpl: &simcom.SimcomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	fakeATServe(mod, func(cmd string) []string { return []string{cmd, "ERROR"} })
	res, err := mod.ScanNetwork(NetScanScope("wcdma"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Tech != string(NetScanLTE) {
		t.Errorf("tech = %q，want LTE", res.Tech)
	}
	if res.CellCmd != "" && !strings.Contains(res.CellCmd, "lte") {
		t.Errorf("非 NR 应扫 LTE，实际 %q", res.CellCmd)
	}
}

// TestScanNetworkAbort 「立即停止」：COPS 迟迟不应答时，AbortNetworkScan 应让 ScanNetwork 立刻返回
func TestScanNetworkAbort(t *testing.T) {
	withATTimeout(t)
	// COPS 超时设得很大：确保是“被叫停”而不是“等超时”返回的
	oldTO := netScanCOpsTimeout
	netScanCOpsTimeout = 30 * time.Second
	t.Cleanup(func() { netScanCOpsTimeout = oldTO })

	mod := &Modem{ID: 0, cmdImpl: &simcom.SimcomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	// COPS 只回显、不给最终应答 → 会一直等
	fakeATServe(mod, func(cmd string) []string { return []string{cmd} })

	done := make(chan *NetworkScanResult, 1)
	go func() {
		res, _ := mod.ScanNetwork(NetScanLTE, 0)
		done <- res
	}()

	// 等它真的在等 COPS 应答（beginScan 已登记）后叫停
	aborted := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if mod.AbortNetworkScan() {
			aborted = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !aborted {
		t.Fatal("AbortNetworkScan 一直返回 false，扫网没被登记？")
	}

	select {
	case res := <-done:
		if !strings.Contains(res.Note, "停止") {
			t.Errorf("note = %q，应说明已手动停止", res.Note)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("AbortNetworkScan 之后 ScanNetwork 没有立刻返回")
	}

	// 扫网结束后登记已清除 → 再叫停应返回 false
	if mod.AbortNetworkScan() {
		t.Error("没有扫网在跑时不该返回 true")
	}
}

// TestSendATStopAbortsWhileWaitingForLock 关键回归：串口被别的指令占着时，
// 「停止扫描」也要立刻返回 —— 不能干等锁（发短信会整段独占十几秒，模组忙时
// 还会连着几条 3s 超时的周期查询在排队，用户体感就是"停止按钮也不灵"）。
func TestSendATStopAbortsWhileWaitingForLock(t *testing.T) {
	withATTimeout(t)
	mod := &Modem{ID: 0, cmdImpl: &simcom.SimcomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}

	// 占住 AT 串行锁，模拟"串口正被别的指令 / 发短信占着"
	mod.atMu.Lock()
	locked := true
	defer func() {
		if locked {
			mod.atMu.Unlock()
		}
	}()

	stop := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		_, err := mod.SendATStop("AT+COPS=?", 5*time.Second, stop)
		errCh <- err
	}()
	time.Sleep(80 * time.Millisecond) // 让它进到"等锁"里
	close(stop)

	select {
	case err := <-errCh:
		if !errors.Is(err, errATStopped) {
			t.Fatalf("err = %v，want errATStopped", err)
		}
	case <-time.After(time.Second):
		t.Fatal("等锁期间叫停没有立刻返回（停止按钮会卡住）")
	}

	locked = false
	mod.atMu.Unlock()
}

// TestBeginScanAbortsPrevious 起第二次扫网时，上一次要被自动叫停。
// 否则两条扫网会在串口上排队：点「停止扫描」只停掉后一条，前一条继续占着端口。
func TestBeginScanAbortsPrevious(t *testing.T) {
	mod := &Modem{}
	first := mod.beginScan()
	second := mod.beginScan()
	if !stopRequested(first) {
		t.Error("起第二次扫网时，上一次的 stop 应被关闭")
	}
	if stopRequested(second) {
		t.Error("新扫网的 stop 不该是已关闭状态")
	}
	mod.endScan(second)
	if mod.AbortNetworkScan() {
		t.Error("endScan 之后不该还有扫网在跑")
	}
}

// TestScanNetworkBothScansBothRats 「一起扫」：LTE 与 NR 各走一遍，小区结果合并。
// 顺序必须是 COPS → LTE 探测/扫描 → NR 探测/扫描（模组一次只认一个 rat）。
func TestScanNetworkBothScansBothRats(t *testing.T) {
	withATTimeout(t)
	oldNR, oldLTE := netScanWaitNR, netScanWaitLTE
	netScanWaitNR, netScanWaitLTE = time.Millisecond, time.Millisecond
	t.Cleanup(func() { netScanWaitNR, netScanWaitLTE = oldNR, oldLTE })

	script := func(cmd string) []string {
		switch cmd {
		case `AT+CNWSEARCH="lte",1`:
			return []string{cmd, "+LTE_CELL:00,1,2,3,1650,155,-98,-9,60", "OK"}
		case `AT+CNWSEARCH="nr5g",3`:
			return []string{cmd, "+NR_NGH_CELL:627264,521,-89,-11,13", "OK"}
		}
		if strings.HasPrefix(cmd, "AT+COPS=?") {
			return []string{cmd, `+COPS: (2,"CHN-UNICOM","UNICOM","46001",7),`, "OK"}
		}
		return []string{cmd, "OK"} // 两个探测都回 OK
	}
	mod := &Modem{ID: 0, cmdImpl: &simcom.SimcomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	sent := fakeATServe(mod, script)

	res, err := mod.ScanNetwork(NetScanBoth, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Tech != string(NetScanBoth) {
		t.Errorf("tech = %q，want both", res.Tech)
	}
	if len(res.Cells) != 2 {
		t.Fatalf("小区数 = %d（%+v），want 2（LTE + NR）", len(res.Cells), res.Cells)
	}
	var haveLTE, haveNR bool
	for _, c := range res.Cells {
		switch c.Tech {
		case CellTechLTE:
			haveLTE = true
		case CellTechNR:
			haveNR = true
		}
	}
	if !haveLTE || !haveNR {
		t.Errorf("应同时有 LTE 和 NR 小区：%+v", res.Cells)
	}
	if !strings.Contains(res.CellCmd, `"lte"`) || !strings.Contains(res.CellCmd, `"nr5g"`) {
		t.Errorf("cell_cmd 应记录两条指令，实际 %q", res.CellCmd)
	}
	if res.CellNote != "" {
		t.Errorf("两边都扫到小区时不该有说明，实际 %q", res.CellNote)
	}
	want := []string{
		"AT+COPS=?",
		`AT+CNWSEARCH="lte"`, `AT+CNWSEARCH="lte",1`,
		`AT+CNWSEARCH="nr5g"`, `AT+CNWSEARCH="nr5g",3`,
	}
	cmds := drainSent(sent)
	for i, w := range want {
		if i >= len(cmds) || cmds[i] != w {
			t.Fatalf("下发顺序 = %v，want %v", cmds, want)
		}
	}
}

// TestScanNetworkBothPartialSupport 一起扫时只有一个制式能用：
// 能用的那个照常出小区，不支持的那个只在说明里提一句（不能整句变成"本固件不支持"）。
func TestScanNetworkBothPartialSupport(t *testing.T) {
	withATTimeout(t)
	oldNR, oldLTE := netScanWaitNR, netScanWaitLTE
	netScanWaitNR, netScanWaitLTE = time.Millisecond, time.Millisecond
	t.Cleanup(func() { netScanWaitNR, netScanWaitLTE = oldNR, oldLTE })

	script := func(cmd string) []string {
		switch cmd {
		case `AT+CNWSEARCH="lte",1`:
			return []string{cmd, "+LTE_CELL:00,1,2,3,1650,155,-98,-9,60", "OK"}
		}
		if cmd == `AT+CNWSEARCH="nr5g"` {
			return []string{cmd, "ERROR"} // NR 不支持
		}
		return []string{cmd, "OK"}
	}
	mod := &Modem{ID: 0, cmdImpl: &simcom.SimcomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	fakeATServe(mod, script)

	res, err := mod.ScanNetwork(NetScanBoth, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Cells) != 1 || res.Cells[0].Tech != CellTechLTE {
		t.Errorf("应只扫到 LTE 小区：%+v", res.Cells)
	}
	if !strings.Contains(res.CellNote, "NR") {
		t.Errorf("说明里应提到 NR 不支持，实际 %q", res.CellNote)
	}
	if strings.Contains(res.CellNote, "本固件不支持") {
		t.Errorf("只有一半不支持时不该说整机不支持，实际 %q", res.CellNote)
	}
	if strings.Contains(res.CellCmd, `"nr5g"`) {
		t.Errorf("NR 被拒后不该再发 NR 扫描指令，实际 %q", res.CellCmd)
	}
}
