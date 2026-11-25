package kernel

import (
	"encoding/json"
	"modemd/module/simcom"
	"strings"
	"testing"
	"time"
)

// ====================== /api/netscan（主动扫网）接口测试 ======================

// newTestManagerWithNetScan 造一个带"假模组"的管理器（无需真实串口），
// 假模组按 script 回 COPS / CNWSEARCH 的应答。
func newTestManagerWithNetScan(script func(cmd string) []string) (*ModemManager, *Modem) {
	mgr := NewModemManager()
	mod := &Modem{
		ID:        0,
		ATPort:    testPortName(3),
		Vendor:    VendorSimcom,
		cmdImpl:   &simcom.SimcomCommand{},
		writeChan: make(chan string, 64),
		readChan:  make(chan string, 64),
	}
	fakeATServe(mod, script)
	mgr.modems[0] = mod
	return mgr, mod
}

// TestAPINetScan 正常路径：code=0，运营商与小区都在 data 里
func TestAPINetScan(t *testing.T) {
	withATTimeout(t)
	oldNR, oldLTE := netScanWaitNR, netScanWaitLTE
	netScanWaitNR, netScanWaitLTE = time.Millisecond, time.Millisecond
	t.Cleanup(func() { netScanWaitNR, netScanWaitLTE = oldNR, oldLTE })

	mgr, _ := newTestManagerWithNetScan(func(cmd string) []string {
		switch {
		case strings.HasPrefix(cmd, "AT+COPS=?"):
			return []string{cmd, `+COPS: (2,"CHN-UNICOM","UNICOM","46001",7),`, "OK"}
		case cmd == `AT+CNWSEARCH="nr5g"`:
			return []string{cmd, "OK"}
		case cmd == `AT+CNWSEARCH="nr5g",3`:
			return []string{cmd, "+NR_NGH_CELL:627264,521,-89,-11,13", "OK"}
		}
		return []string{cmd, "OK"}
	})

	code, data, msg := doRequest(t, apiNetScan(mgr), "/api/netscan?modem_id=0&tech=nr")
	if code != 0 {
		t.Fatalf("code = %d, msg = %s", code, msg)
	}
	var res NetworkScanResult
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatal(err)
	}
	if res.Tech != string(CellTechNR) {
		t.Errorf("tech = %q", res.Tech)
	}
	if len(res.Operators) != 1 || res.Operators[0].Long != "CHN-UNICOM" {
		t.Errorf("运营商 = %+v", res.Operators)
	}
	if len(res.Cells) != 1 || res.Cells[0].PCID != 521 {
		t.Errorf("小区 = %+v", res.Cells)
	}
	if res.CellCmd != `AT+CNWSEARCH="nr5g",3` {
		t.Errorf("cell_cmd = %q", res.CellCmd)
	}
}

// TestAPINetScanModemNotFound 模组不存在 → code=-1（与其它接口一致）
func TestAPINetScanModemNotFound(t *testing.T) {
	mgr, _ := newTestManagerWithNetScan(func(cmd string) []string { return []string{cmd, "OK"} })
	code, _, _ := doRequest(t, apiNetScan(mgr), "/api/netscan?modem_id=99&tech=lte")
	if code != -1 {
		t.Errorf("code = %d，want -1", code)
	}
}

// TestAPINetScanStop 没有扫网在跑时 stopped=false（前端据此不提示）
// 真正"在跑时能停下"由 netscan_test.go 的 TestScanNetworkAbort 覆盖。
func TestAPINetScanStop(t *testing.T) {
	mgr, _ := newTestManagerWithNetScan(func(cmd string) []string { return []string{cmd, "OK"} })
	code, data, msg := doRequest(t, apiNetScanStop(mgr), "/api/netscan/stop")
	if code != 0 {
		t.Fatalf("code = %d, msg = %s", code, msg)
	}
	var r struct {
		Stopped bool `json:"stopped"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	if r.Stopped {
		t.Error("没有扫网在跑时 stopped 应为 false")
	}
}

// TestAPINetScanMinutes 等待时长由 minutes 参数决定；越界/非法一律夹到 3~10 分钟
// （手改 URL 传个天文数字不能把 AT 口占死）。
func TestAPINetScanMinutes(t *testing.T) {
	withATTimeout(t)
	oldNR, oldLTE := netScanWaitNR, netScanWaitLTE
	netScanWaitNR, netScanWaitLTE = time.Millisecond, time.Millisecond
	t.Cleanup(func() { netScanWaitNR, netScanWaitLTE = oldNR, oldLTE })

	mgr, _ := newTestManagerWithNetScan(func(cmd string) []string { return []string{cmd, "ERROR"} })
	for _, tc := range []struct {
		q    string
		want time.Duration
	}{
		{"", 3 * time.Minute},             // 不传 → 默认 3 分钟
		{"&minutes=5", 5 * time.Minute},   // 下拉里的值
		{"&minutes=10", 10 * time.Minute}, // 上限
		{"&minutes=1", 3 * time.Minute},   // 低于下限 → 夹到 3
		{"&minutes=99", 10 * time.Minute}, // 高于上限 → 夹到 10
		{"&minutes=abc", 3 * time.Minute}, // 非法 → 默认 3
	} {
		code, data, msg := doRequest(t, apiNetScan(mgr), "/api/netscan?modem_id=0&tech=lte"+tc.q)
		if code != 0 {
			t.Fatalf("%q: code = %d, msg = %s", tc.q, code, msg)
		}
		var res NetworkScanResult
		if err := json.Unmarshal(data, &res); err != nil {
			t.Fatal(err)
		}
		if res.WaitMs != tc.want.Milliseconds() {
			t.Errorf("%q: wait_ms = %d，want %d", tc.q, res.WaitMs, tc.want.Milliseconds())
		}
	}
}

// TestAPINetScanTechBoth tech=both（也认 all）要原样传给核心并回显；认不出的按 LTE
func TestAPINetScanTechBoth(t *testing.T) {
	withATTimeout(t)
	oldNR, oldLTE := netScanWaitNR, netScanWaitLTE
	netScanWaitNR, netScanWaitLTE = time.Millisecond, time.Millisecond
	t.Cleanup(func() { netScanWaitNR, netScanWaitLTE = oldNR, oldLTE })

	mgr, _ := newTestManagerWithNetScan(func(cmd string) []string { return []string{cmd, "ERROR"} })
	for _, tc := range []struct{ q, want string }{
		{"tech=lte", string(NetScanLTE)},
		{"tech=nr", string(NetScanNR)},
		{"tech=both", string(NetScanBoth)},
		{"tech=all", string(NetScanBoth)},
		{"tech=wcdma", string(NetScanLTE)}, // 认不出 → 按 LTE
	} {
		code, data, msg := doRequest(t, apiNetScan(mgr), "/api/netscan?modem_id=0&"+tc.q)
		if code != 0 {
			t.Fatalf("%q: code = %d, msg = %s", tc.q, code, msg)
		}
		var res NetworkScanResult
		if err := json.Unmarshal(data, &res); err != nil {
			t.Fatal(err)
		}
		if res.Tech != tc.want {
			t.Errorf("%q: tech = %q，want %q", tc.q, res.Tech, tc.want)
		}
	}
}
