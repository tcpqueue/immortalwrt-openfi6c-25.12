package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"encoding/json"
	"modemd/module/bands"
	"modemd/module/cell"
	"modemd/module/usbdialect"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ---- from: api_test.go ----

// ---- from: api_cells_test.go ----
// newTestManagerWithCells 构造一个带基站扫描缓存的模组管理器（无需真实串口）
// newTestManagerWithCells 构造一个带基站扫描缓存的模组管理器（无需真实串口）

func newTestManagerWithCells() (*ModemManager, *Modem) {
	mgr := NewModemManager()
	mod := &Modem{
		ID:         0,
		ATPort:     testPortName(3),
		Vendor:     VendorQuectel,
		cmdImpl:    NewVendorCommand(VendorQuectel),
		CellScanAt: time.Now(),
		Cells: []cell.CellInfo{
			{
				Tech: CellTechLTE, State: "CONNECTED", MCC: "460", MNC: "11",
				CellID: "400E21E", TAC: 155, EARFCN: 2452, Band: 5,
				RSRP: -104, RSRQ: -16, RSSI: -69, SINR: 7,
				Serving: true, Operator: "中国电信",
			},
			{Tech: CellTechLTE, EARFCN: 2452, PCID: 155, Band: 5, RSRP: -103, RSRQ: -15, IntraCell: true},
			{Tech: CellTechLTE, EARFCN: 1850, Band: 3, RSRP: -115},
		},
	}
	mgr.modems[0] = mod
	return mgr, mod
}

// doRequest 执行一次请求并解析统一响应体
func doRequest(t *testing.T, h http.HandlerFunc, target string) (int, json.RawMessage, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("http status = %d", rec.Code)
	}
	var body struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
		Msg  string          `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json response: %v (%s)", err, rec.Body.String())
	}
	return body.Code, body.Data, body.Msg
}

// TestAPICellsCache 验证 /api/cells?cache=1 返回缓存结果且正确区分服务/邻小区
func TestAPICellsCache(t *testing.T) {
	mgr, _ := newTestManagerWithCells()
	code, data, msg := doRequest(t, apiScanCells(mgr), "/api/cells?modem_id=0&cache=1")
	if code != 0 {
		t.Fatalf("code = %d, msg = %s", code, msg)
	}
	var res cell.CellScanResult
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if res.Count != 3 || res.ServingCount != 1 || res.NeighbourCnt != 2 {
		t.Fatalf("counts wrong: %+v", res)
	}
	if len(res.Serving) != 1 || res.Serving[0].Tech != CellTechLTE {
		t.Fatalf("serving wrong: %+v", res.Serving)
	}
	if res.Serving[0].Operator != "中国电信" {
		t.Fatalf("operator wrong: %q", res.Serving[0].Operator)
	}
	if res.Cells[0].OperatorName() != "中国电信" {
		t.Fatalf("operator chinese name wrong: %q", res.Cells[0].OperatorName())
	}
}

// TestAPICellsAll 验证 /api/cells/all 汇总所有模组缓存
func TestAPICellsAll(t *testing.T) {
	mgr, _ := newTestManagerWithCells()
	code, data, msg := doRequest(t, apiCellsAll(mgr), "/api/cells/all")
	if code != 0 {
		t.Fatalf("code = %d, msg = %s", code, msg)
	}
	var list []cell.CellScanResult
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expect 1 modem result, got %d", len(list))
	}
	if list[0].ModemID != 0 || list[0].Count != 3 {
		t.Fatalf("result wrong: %+v", list[0])
	}
}

// TestAPICellsModemNotFound 未知 modem_id 应返回业务错误码而非崩溃
func TestAPICellsModemNotFound(t *testing.T) {
	mgr, _ := newTestManagerWithCells()
	code, _, _ := doRequest(t, apiScanCells(mgr), "/api/cells?modem_id=99&cache=1")
	if code == 0 {
		t.Fatal("expected non-zero code for missing modem")
	}
}

func TestAPINetworkStatusUsesFreshCache(t *testing.T) {
	mgr := NewModemManager()
	mod := &Modem{ID: 0}
	mod.cacheNetworkStatus(&NetworkStatus{
		ModemID:   0,
		QueriedAt: time.Now(),
		Operator:  "中国电信",
		MCC:       "460",
		MNC:       "11",
	})
	mgr.modems[0] = mod

	code, data, msg := doRequest(t, apiNetworkStatus(mgr), "/api/network/status?modem_id=0")
	if code != 0 {
		t.Fatalf("code = %d, msg = %s", code, msg)
	}
	var st NetworkStatus
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if st.Operator != "中国电信" || st.MCC != "460" || st.MNC != "11" {
		t.Fatalf("cached network status = %+v", st)
	}
}

func TestNetworkStatusDueUsesConfiguredInterval(t *testing.T) {
	queriedAt := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	mod := &Modem{networkStatus: &NetworkStatus{QueriedAt: queriedAt}}
	interval := 3 * time.Second
	if mod.networkStatusDue(queriedAt.Add(2*time.Second), interval) {
		t.Fatal("network status due before configured interval")
	}
	if !mod.networkStatusDue(queriedAt.Add(interval), interval) {
		t.Fatal("network status not due at configured interval")
	}
	if mod.networkStatusDue(queriedAt.Add(time.Hour), 0) {
		t.Fatal("network status should be disabled for non-positive interval")
	}
}

func TestAPIParamsUsesFreshCache(t *testing.T) {
	mgr := NewModemManager()
	mod := &Modem{ID: 0, paramsStatus: &Status{
		ModemID: 0, QueriedAt: time.Now(), APN: "ctnet",
		Contexts: []Context{}, NetSelect: NetSelect{ModeText: "自动"},
	}}
	mgr.modems[0] = mod

	code, data, msg := doRequest(t, apiParams(mgr), "/api/params?modem_id=0")
	if code != 0 {
		t.Fatalf("code = %d, msg = %s", code, msg)
	}
	var st Status
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if st.APN != "ctnet" {
		t.Fatalf("cached APN = %q, want ctnet", st.APN)
	}
}

func TestAPIUSBnetUsesFreshCache(t *testing.T) {
	mgr := NewModemManager()
	mod := &Modem{ID: 0, usbNetStatus: &CFUNStatus{Value: "5", Name: "NCM", Modes: usbdialect.Modes()}, usbNetStatusAt: time.Now()}
	mgr.modems[0] = mod

	code, data, msg := doRequest(t, apiUSBnet(mgr), "/api/usbnet?modem_id=0")
	if code != 0 {
		t.Fatalf("code = %d, msg = %s", code, msg)
	}
	var st CFUNStatus
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if st.Value != "5" || st.Name != "NCM" {
		t.Fatalf("cached USB net mode = %+v", st)
	}
}

func TestAPIModemStatusUsesFreshCache(t *testing.T) {
	mgr := NewModemManager()
	mod := &Modem{ID: 0, ModemStatus: &ModemStatus{
		ModemID: 0, QueriedAt: time.Now(), Operator: "中国电信", SIMState: "READY",
	}}
	mgr.modems[0] = mod

	code, data, msg := doRequest(t, apiModemStatus(mgr), "/api/modem/status?modem_id=0")
	if code != 0 {
		t.Fatalf("code = %d, msg = %s", code, msg)
	}
	var st ModemStatus
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if st.Operator != "中国电信" || st.SIMState != "READY" {
		t.Fatalf("cached modem status = %+v", st)
	}
}

func TestAPILockStatusUsesFreshCache(t *testing.T) {
	mgr := NewModemManager()
	mod := &Modem{ID: 0, lockStatus: &bands.LockStatus{
		ModemID: 0, QueriedAt: time.Now(), ModePref: "AUTO",
		BandLTE: bands.BandLock{Tech: CellTechLTE, Bands: []int{1, 3}},
	}}
	mgr.modems[0] = mod

	code, data, msg := doRequest(t, apiLockStatus(mgr), "/api/lock/status?modem_id=0")
	if code != 0 {
		t.Fatalf("code = %d, msg = %s", code, msg)
	}
	var st bands.LockStatus
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if st.ModePref != "AUTO" || len(st.BandLTE.Bands) != 2 {
		t.Fatalf("cached lock status = %+v", st)
	}
}

// TestAPICellsCacheCarriesCAHint 缓存路径也要带 CA 提示（界面首屏读的就是缓存）。
// 之前漏了：首屏会显示"该厂商未提供 CA 查询指令"，等实时扫描回来才变对。
func TestAPICellsCacheCarriesCAHint(t *testing.T) {
	mgr, mod := newTestManagerWithCells()

	// 移远：通用指令 AT+QCAINFO，无额外说明
	code, data, msg := doRequest(t, apiScanCells(mgr), "/api/cells?modem_id=0&cache=1")
	if code != 0 {
		t.Fatalf("code = %d, msg = %s", code, msg)
	}
	var res cell.CellScanResult
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(res.CACmds) != 1 || res.CACmds[0] != "AT+QCAINFO" {
		t.Fatalf("移远缓存 ca_cmds = %v, want [AT+QCAINFO]", res.CACmds)
	}

	// 换成 MT5700：提示应变成它自己的两条指令 + 空状态说明
	mod.cmdImpl = mt5700()
	code, data, _ = doRequest(t, apiScanCells(mgr), "/api/cells?modem_id=0&cache=1")
	if code != 0 {
		t.Fatal("code != 0")
	}
	res = cell.CellScanResult{}
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(res.CACmds) != 2 || res.CACmds[0] != "AT^CASCELLINFO?" || res.CACmds[1] != "AT^MONSSC" {
		t.Fatalf("MT5700 缓存 ca_cmds = %v", res.CACmds)
	}
	if res.CANote == "" {
		t.Fatal("MT5700 缓存应带 CA 空状态说明")
	}
}

// TestAPIModemsExposesCells 验证 /api/modems 暴露最近一次扫描缓存
func TestAPIModemsExposesCells(t *testing.T) {
	mgr, _ := newTestManagerWithCells()
	code, data, _ := doRequest(t, apiListModems(mgr), "/api/modems")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	var modems []struct {
		Cells []cell.CellInfo `json:"cells"`
	}
	if err := json.Unmarshal(data, &modems); err != nil {
		t.Fatalf("decode modems: %v", err)
	}
	if len(modems) != 1 || len(modems[0].Cells) != 3 {
		t.Fatalf("modems cells wrong: %+v", modems)
	}
}
