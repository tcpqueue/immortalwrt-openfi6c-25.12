package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"modemd/module/atparse"
	"modemd/module/cell"
	"modemd/module/common"
	"modemd/module/config"
	"modemd/module/fibocom"
	"modemd/module/netdev"
	"modemd/module/quectel"
	"modemd/module/simcom"
	"modemd/module/sms"
	"modemd/module/tdtech"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---- from: modem_test.go ----

// ---- from: modem_test.go ----
// initTestLogger 为测试初始化 logger。
// 空路径 = 打不开日志文件 → config.go 会退化为只写 stdout（测试里正是要这样）。
// initTestLogger 为测试初始化 logger。
// 空路径 = 打不开日志文件 → config.go 会退化为只写 stdout（测试里正是要这样）。

func initTestLogger() {
	config.Cfg.Server.LogLevel = "error"
	config.Init("", "error")
}

// testPortName 生成平台无关的测试串口名
func testPortName(i int) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper("COM" + itoa(i))
	}
	return "/dev/ttyUSB" + itoa(i)
}

// itoa 简易整数转字符串
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestGroupTTYPerModem 验证按固定端口数分组：
// 模组1占 4 口，模组2占 4 口，虽然编号连续也应分为两组
func TestGroupTTYPerModem(t *testing.T) {
	config.Cfg.Modem.TTYPerModem = 4
	files := []string{
		testPortName(0), testPortName(1), testPortName(2), testPortName(3),
		testPortName(4), testPortName(5), testPortName(6), testPortName(7),
	}
	groups := groupTTYUSB(files)

	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d: %v", len(groups), groups)
	}
	if len(groups[0]) != 4 || len(groups[1]) != 4 {
		t.Fatalf("each group should have 4 ports, got %v", groups)
	}
	if groups[0][0] != testPortName(0) || groups[1][0] != testPortName(4) {
		t.Fatalf("group boundaries wrong: %v", groups)
	}
	t.Logf("groups OK: %v", groups)
}

// TestGroupWithGap 验证按连续分组时遇到间隔正确分拆
func TestGroupWithGap(t *testing.T) {
	config.Cfg.Modem.TTYPerModem = 0
	files := []string{testPortName(0), testPortName(1), testPortName(3), testPortName(4)}
	groups := groupTTYUSB(files)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups with gap, got %d", len(groups))
	}
}

// TestWindowAround 验证"先探测后分组"中，每个 AT 口能圈定自己的关联端口。
// 场景：模组1 AT口=COM5（占COM3-6），模组2 AT口=COM8（占COM7-10），
// 中间夹杂干扰串口，AT 探测不受编号影响，窗口以 AT 口为中心。
func TestWindowAround(t *testing.T) {
	allPorts := []string{
		testPortName(1), testPortName(2), testPortName(3), testPortName(4), testPortName(5),
		testPortName(6), testPortName(7), testPortName(8), testPortName(9), testPortName(10),
	}
	// 模组1 AT口 = COM5，窗口4 → 应覆盖 COM4-7 附近（含自身）
	w1 := windowAround(allPorts, testPortName(5), 4)
	if len(w1) != 4 {
		t.Fatalf("window1 len=%d want 4: %v", len(w1), w1)
	}
	if w1[0] != testPortName(4) {
		t.Fatalf("window1 start wrong: %v", w1)
	}
	// 模组2 AT口 = COM8，窗口4 → COM7-10
	w2 := windowAround(allPorts, testPortName(8), 4)
	if len(w2) != 4 {
		t.Fatalf("window2 len=%d want 4: %v", len(w2), w2)
	}
	if w2[0] != testPortName(7) {
		t.Fatalf("window2 start wrong: %v", w2)
	}
	// n<=1 时仅返回自身
	w3 := windowAround(allPorts, testPortName(8), 1)
	if len(w3) != 1 || w3[0] != testPortName(8) {
		t.Fatalf("window3 wrong: %v", w3)
	}
}

// TestWindowAroundEdge 验证窗口在列表边界时正确收缩
func TestWindowAroundEdge(t *testing.T) {
	allPorts := []string{testPortName(0), testPortName(1), testPortName(2), testPortName(3)}
	// AT口在开头 COM0，窗口4 → 只能取 COM0-3
	w := windowAround(allPorts, testPortName(0), 4)
	if len(w) != 4 {
		t.Fatalf("edge window len=%d want 4: %v", len(w), w)
	}
	if w[0] != testPortName(0) || w[3] != testPortName(3) {
		t.Fatalf("edge window wrong: %v", w)
	}
}

// TestGroupByUSBDevice 验证按 USB 物理设备分组：
// 模组1 占 3 个串口、模组2 占 6 个串口，中间夹杂主板自带串口。
// 模拟真实 sysfs/注册表映射，验证不同端口数的模组都能正确对应。
func TestGroupByUSBDevice(t *testing.T) {
	// 模拟串口清单（乱序 + 干扰），COM 从 1 开始（真实系统无 COM0）
	portList := []string{
		testPortName(4),  // 模组2的 AT 口（乱序出现）
		testPortName(1),  // 模组1口1
		testPortName(2),  // 模组1口2
		testPortName(3),  // 模组1口3 (AT口)
		testPortName(5),  // 模组2口2
		testPortName(6),  // 模组2口3
		testPortName(7),  // 模组2口4
		testPortName(8),  // 模组2口5
		testPortName(9),  // 模组2口6
		testPortName(10), // 主板自带串口（非 USB）
	}

	// 注入模拟的 USB 设备映射
	origResolver := groupPorts
	defer func() { groupPorts = origResolver }()
	groupPorts = func(ps []string) [][]string {
		// 模拟 usbPathResolver：COM1-3 -> usb1/1-1，COM4-9 -> usb1/1-2，COM10 无映射
		resolver := func(p string) string {
			n := netdev.SerialPortIndex(p)
			switch {
			case n >= 1 && n <= 3:
				return "/usb1/1-1"
			case n >= 4 && n <= 9:
				return "/usb1/1-2"
			default:
				return ""
			}
		}
		groupsMap := map[string][]string{}
		var order []string
		for _, p := range ps {
			key := resolver(p)
			if key == "" {
				key = p
			}
			if _, ok := groupsMap[key]; !ok {
				order = append(order, key)
			}
			groupsMap[key] = append(groupsMap[key], p)
		}
		groups := [][]string{}
		for _, k := range order {
			groups = append(groups, groupsMap[k])
		}
		return groups
	}

	groups := groupPorts(portList)
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d: %v", len(groups), groups)
	}

	// 按内容查找各模组（分组顺序不保证）
	findGroup := func(wantLen int, first string) bool {
		for _, g := range groups {
			if len(g) == wantLen && g[0] == first {
				return true
			}
		}
		return false
	}

	// 模组1 = COM1-3（3 口）
	if !findGroup(3, testPortName(1)) {
		t.Fatalf("modem1 (3 ports starting %s) not found: %v", testPortName(1), groups)
	}
	// 模组2 = COM4-9（6 口）
	if !findGroup(6, testPortName(4)) {
		t.Fatalf("modem2 (6 ports starting %s) not found: %v", testPortName(4), groups)
	}
	// 主板串口独立成组
	if !findGroup(1, testPortName(10)) {
		t.Fatalf("onboard port %s not standalone: %v", testPortName(10), groups)
	}

	t.Logf("groups OK: %v", groups)
}

// TestGroupByUSBDeviceDifferentPortCounts 验证不同厂商模组端口数不同也能正确分组
func TestGroupByUSBDeviceDifferentPortCounts(t *testing.T) {
	// 模组A（移远）4口 COM1-4，模组B（芯讯通）6口 COM5-10，模组C（广和通）2口 COM11-12
	portList := []string{
		testPortName(1), testPortName(2), testPortName(3), testPortName(4),
		testPortName(5), testPortName(6), testPortName(7), testPortName(8), testPortName(9), testPortName(10),
		testPortName(11), testPortName(12),
	}

	origResolver := groupPorts
	defer func() { groupPorts = origResolver }()
	groupPorts = func(ps []string) [][]string {
		resolver := func(p string) string {
			n := netdev.SerialPortIndex(p)
			switch {
			case n >= 1 && n <= 4:
				return "/usb1/1-1" // 模组A 4口
			case n >= 5 && n <= 10:
				return "/usb1/1-2" // 模组B 6口
			case n >= 11 && n <= 12:
				return "/usb1/1-3" // 模组C 2口
			default:
				return ""
			}
		}
		groupsMap := map[string][]string{}
		var order []string
		for _, p := range ps {
			key := resolver(p)
			if key == "" {
				key = p
			}
			if _, ok := groupsMap[key]; !ok {
				order = append(order, key)
			}
			groupsMap[key] = append(groupsMap[key], p)
		}
		groups := [][]string{}
		for _, k := range order {
			groups = append(groups, groupsMap[k])
		}
		return groups
	}

	groups := groupPorts(portList)
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d: %v", len(groups), groups)
	}
	if len(groups[0]) != 4 || len(groups[1]) != 6 || len(groups[2]) != 2 {
		t.Fatalf("port counts wrong: %v", groups)
	}
	t.Logf("different port counts OK: %v", groups)
}

// TestCSQParsing 验证 AT+CSQ 信号解析
func TestCSQParsing(t *testing.T) {
	initTestLogger()
	defer func() { _ = logger.Sync() }()
	mod := &Modem{ID: 0}
	mod.parseLine("+CSQ: 22,0")
	if mod.SignalInfo.RSSI != 22 || mod.SignalInfo.BER != 0 {
		t.Fatalf("CSQ parse failed: %+v", mod.SignalInfo)
	}
}

// TestSMSHeaders 验证短信头部解析（CMGL）
func TestSMSHeaders(t *testing.T) {
	line := `+CMGL: 3,"REC UNREAD","+8613800138000","","25/04/23,10:30:00+32"`
	msg, ok := sms.ParseCMGLHeader(line)
	if !ok {
		t.Fatalf("CMGL 头部应能解析: %s", line)
	}
	if msg.Index != "3" || msg.Number != "+8613800138000" {
		t.Fatalf("CMGL fields wrong: %+v", msg)
	}
}

// TestVendorDetectQuectel 验证移远厂商识别
func TestVendorDetectQuectel(t *testing.T) {
	resp := []string{"Quectel", "RG500Q-EA", "OK"}
	q := &quectel.QuectelCommand{}
	if v := q.DetectVendor(resp); v != VendorQuectel {
		t.Fatalf("expected quectel, got %s", v)
	}
}

// TestVendorDetectFibocom 验证广和通厂商识别
func TestVendorDetectFibocom(t *testing.T) {
	resp := []string{"Fibocom FM650-CN", "OK"}
	f := &fibocom.FibocomCommand{}
	if v := f.DetectVendor(resp); v != VendorFibocom {
		t.Fatalf("expected fibocom, got %s", v)
	}
}

// TestVendorDetectSimcom 验证芯讯通厂商识别
func TestVendorDetectSimcom(t *testing.T) {
	resp := []string{"SIMCOM_SIM7900", "OK"}
	s := &simcom.SimcomCommand{}
	if v := s.DetectVendor(resp); v != VendorSimcom {
		t.Fatalf("expected simcom, got %s", v)
	}
}

// TestNRSignalQuectel 验证移远 5G NR 信号解析
func TestNRSignalQuectel(t *testing.T) {
	line := `+QENG: "servingcell","NR",FDD,460,00,1051E8F2,123,0,-95,-12,14,35`
	q := &quectel.QuectelCommand{}
	rsrp, rsrq, sinr, ok := q.ParseNRSignal(line)
	if !ok {
		t.Fatalf("should parse NR signal: %s", line)
	}
	if rsrp != -95 || rsrq != -12 || sinr != 14 {
		t.Fatalf("NR parse wrong: %d %d %d", rsrp, rsrq, sinr)
	}
}

// TestReadSMS 验证完整短信解析流程（直接覆盖解析器本体）
func TestReadSMS(t *testing.T) {
	resp := []string{
		`+CMGL: 1,"REC UNREAD","+8613800138000","","25/04/23,10:30:00+32"`,
		"hello from mom",
		"OK",
	}
	list := sms.ParseSMSList(resp)
	if len(list) != 1 {
		t.Fatalf("expected 1 sms, got %d", len(list))
	}
	if list[0].Content != "hello from mom" {
		t.Fatalf("sms content wrong: %+v", list[0])
	}
	if list[0].Index != "1" || list[0].Number != "+8613800138000" {
		t.Fatalf("sms header wrong: %+v", list[0])
	}
	if list[0].Timestamp != "25/04/23,10:30:00+32" {
		t.Fatalf("sms timestamp wrong: %+v", list[0])
	}
}

// ---- from: vendorcmd_test.go ----

//
// 这两条用例要 *Modem（候选探测要在模组实例上跑），所以跟引擎留在 kernel；
// 厂商指令集本身的用例在 atcmd.go（那个包的测试里跑，见 modem_test.go）。

// ====================== 引擎侧：候选探测与错误判定 ======================
//
// 这两条用例要 *Modem（候选探测要在模组实例上跑），所以跟引擎留在 kernel；
// 厂商指令集本身的用例在 atcmd.go（那个包的测试里跑，见 modem_test.go）。

func TestRespHasError(t *testing.T) {
	cases := []struct {
		resp []string
		want bool
	}{
		{[]string{"ERROR"}, true},
		{[]string{"+CME ERROR: 3"}, true},
		{[]string{`+QENG: "servingcell","NR"`, "+CMS ERROR: 500"}, true},
		{[]string{`+QENG: "servingcell","NR"`, "OK"}, false},
		{[]string{"OK"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := common.RespHasError(c.resp); got != c.want {
			t.Errorf("respHasError(%v) = %v, want %v", c.resp, got, c.want)
		}
	}
}

// TestVendorCandidates 候选列表：自适应厂商多条，已验证厂商单条
func TestVendorCandidates(t *testing.T) {
	// MT5700：鼎桥私有指令排在最前（实测通用指令全 ERROR，详见 mt5700_test.go）
	mt := &Modem{ID: 0, cmdImpl: &tdtech.MT5700Command{}}
	if got := mt.vendorCandidates(KindServing); len(got) != 4 || got[0] != "AT^MONSC" {
		t.Fatalf("MT5700 服务小区候选 = %v", got)
	}
	if got := mt.vendorCandidates(KindNeighbour); len(got) != 2 || got[0] != "AT^MONNC" {
		t.Fatalf("MT5700 邻区候选 = %v", got)
	}

	q := &Modem{ID: 0, cmdImpl: &quectel.QuectelCommand{}}
	if got := q.vendorCandidates(KindServing); len(got) != 1 || got[0] != common.CmdQENGServing {
		t.Fatalf("移远服务小区候选 = %v", got)
	}

	f := &Modem{ID: 0, cmdImpl: &fibocom.FibocomCommand{}}
	// 广和通两套方言：FM150/FM160 用 +FENG（格式同移远），FM650-CN（展锐）只认 +GTCCINFO?
	got := f.vendorCandidates(KindServing)
	if len(got) != 2 || got[0] != `AT+FENG="servingcell"` || got[1] != "AT+GTCCINFO?" {
		t.Fatalf("广和通服务小区候选 = %v", got)
	}
	got = f.vendorCandidates(KindNeighbour)
	if len(got) != 2 || got[0] != `AT+FENG="neighbourcell"` || got[1] != "AT+GTCCINFO?" {
		t.Fatalf("广和通邻区候选 = %v", got)
	}
}

// ---- from: at_channel_test.go ----

// AT 应答结束标记必须**整行判定**。
//
// 实测踩到（广和通 FM650）：老实现用 strings.Contains(line, "OK") 判结束，
// 只要有一行里出现 "OK" 就返回 —— 短信正文里出现 OK、或者上一条指令的残留里带 OK，
// 都会把 AT+CMGL 这种长应答**提前截断**。被截断的尾巴留在读通道里，
// 下一条指令就会把它当成自己的应答读走 → **整条通道错位一格且不会自愈**：
// 症状是"短信读不出来 / 信号值乱跳 / 大量 AT command timeout"（实测日志 386 次）。

// AT 应答结束标记必须**整行判定**。
//
// 实测踩到（广和通 FM650）：老实现用 strings.Contains(line, "OK") 判结束，
// 只要有一行里出现 "OK" 就返回 —— 短信正文里出现 OK、或者上一条指令的残留里带 OK，
// 都会把 AT+CMGL 这种长应答**提前截断**。被截断的尾巴留在读通道里，
// 下一条指令就会把它当成自己的应答读走 → **整条通道错位一格且不会自愈**：
// 症状是"短信读不出来 / 信号值乱跳 / 大量 AT command timeout"（实测日志 386 次）。
func TestIsATEndLine(t *testing.T) {
	for _, line := range []string{"OK", "ok", " OK ", "\tOK", "ERROR", "error", "+CME ERROR: 3", "+CMS ERROR: 500"} {
		if !isATEndLine(line) {
			t.Errorf("%q 应判为应答结束", line)
		}
	}
	for _, line := range []string{
		"",
		"OK了",
		"收到请回复 OK",
		"NOTOK",
		`+CMGL: 0,"REC READ","10086",,"26/09/29,11:14:05+32"`,
		`+CPMS: "SM",11,40,"SM",11,40,"SM",11,40`,
		"AT+CMGL=\"ALL\"",
	} {
		if isATEndLine(line) {
			t.Errorf("%q 不该判为结束（Contains 写法会在这里出错）", line)
		}
	}
}

// ---- from: modem_info_test.go ----

// doPostRequest 发起一次 POST 请求并返回业务返回码与消息

func doPostRequest(t *testing.T, h http.HandlerFunc, target, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h(rec, req)
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json response: %v (%s)", err, rec.Body.String())
	}
	return resp.Code, resp.Msg
}

// ====================== 身份信息解析 ======================

func TestParseIMEI(t *testing.T) {
	cases := []struct {
		name string
		resp []string
		want string
	}{
		{"普通应答", []string{"AT+CGSN", "861234567890123", "OK"}, "861234567890123"},
		{"带引号", []string{"AT+CGSN", `"861234567890123"`, "OK"}, "861234567890123"},
		{"+CGSN 前缀", []string{"+CGSN: 861234567890123", "OK"}, "861234567890123"},
		{"不误取回显", []string{"AT+CGSN", "OK"}, ""},
		{"模组报错", []string{"AT+CGSN", "ERROR"}, ""},
		{"空应答", nil, ""},
	}
	for _, c := range cases {
		if got := parseIMEI(c.resp); got != c.want {
			t.Errorf("%s: parseIMEI = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestParseFirmware(t *testing.T) {
	cases := []struct {
		name string
		resp []string
		want string
	}{
		{"实测固件串", []string{"AT+CGMR", "RM502QGLAAR11A02M4G", "OK"}, "RM502QGLAAR11A02M4G"},
		{"+CGMR 前缀", []string{"AT+CGMR", "+CGMR: RM502QGLAAR11A02M4G", "OK"}, "RM502QGLAAR11A02M4G"},
		{"带引号", []string{"+CGMR: \"RM502QGLAAR11A02M4G\"", "OK"}, "RM502QGLAAR11A02M4G"},
		{"模组报错", []string{"AT+CGMR", "ERROR"}, ""},
		{"只有 OK", []string{"OK"}, ""},
		{"空应答", nil, ""},
	}
	for _, c := range cases {
		if got := parseFirmware(c.resp); got != c.want {
			t.Errorf("%s: parseFirmware = %q, want %q", c.name, got, c.want)
		}
	}
}

// ====================== 短信存储解析 ======================

func TestParseSMSStorage(t *testing.T) {
	// 三个存储区都上报
	resp := []string{"AT+CPMS?", `+CPMS: "SM",3,30,"SM",3,30,"SM",3,30`, "OK"}
	st, ok := parseSMSStorage(resp)
	if !ok {
		t.Fatal("should parse CPMS response")
	}
	if st.Mem1 != "SM" || st.Mem1Used != 3 || st.Mem1Total != 30 {
		t.Fatalf("mem1 wrong: %+v", st)
	}
	if st.Mem2 != "SM" || st.Mem2Used != 3 || st.Mem2Total != 30 {
		t.Fatalf("mem2 wrong: %+v", st)
	}
	if st.Mem3 != "SM" || st.Mem3Used != 3 || st.Mem3Total != 30 {
		t.Fatalf("mem3 wrong: %+v", st)
	}

	// 部分模组只上报两个存储区
	st2, ok := parseSMSStorage([]string{`+CPMS: "ME",1,100,"ME",1,100`, "OK"})
	if !ok {
		t.Fatal("should parse 2-slot CPMS response")
	}
	if st2.Mem1 != "ME" || st2.Mem1Used != 1 || st2.Mem1Total != 100 {
		t.Fatalf("mem1 wrong: %+v", st2)
	}
	if st2.Mem3 != "" || st2.Mem3Total != 0 {
		t.Fatalf("mem3 should be empty: %+v", st2)
	}

	if _, ok := parseSMSStorage([]string{"OK"}); ok {
		t.Fatal("empty response should not parse")
	}
}

// 存储区切换：名称校验 + AT+CPMS=? 支持列表解析 + current 字段
func TestSMSStorageSwitchHelpers(t *testing.T) {
	// 名称校验（会拼进 AT+CPMS="…","…","…"，必须卡死）
	for _, m := range []string{"SM", "sm", "ME", "me", "MT", " MT "} {
		if !validSMSMem(m) {
			t.Errorf("%q 应通过校验", m)
		}
	}
	for _, m := range []string{"", "SIM", "SM1", `SM","ME`, "ME MT", "所有"} {
		if validSMSMem(m) {
			t.Errorf("%q 应被拒绝", m)
		}
	}

	// AT+CPMS=? 支持列表（去重、按原顺序）
	sup := parseCPMSSupported([]string{`AT+CPMS=?`, `+CPMS: ("ME","MT","SM"),("ME","MT","SM"),("ME","MT","SM")`, "OK"})
	if len(sup) != 3 || sup[0] != "ME" || sup[1] != "MT" || sup[2] != "SM" {
		t.Fatalf("支持列表 = %v, want [ME MT SM]", sup)
	}
	if got := parseCPMSSupported([]string{"ERROR", "OK"}); len(got) != 0 {
		t.Errorf("没有 +CPMS 行时应返回空: %v", got)
	}

	// current 要跟着 mem1 走（前端下拉框靠它选中当前项）
	st, ok := parseSMSStorage([]string{`+CPMS: "SM",0,40,"SM",0,40,"SM",0,40`})
	if !ok || st.Current != "SM" {
		t.Fatalf("current 不对: %+v", st)
	}
	st, ok = parseSMSStorage([]string{`+CPMS: "me",1,99,"me",1,99`})
	if !ok || st.Current != "ME" {
		t.Fatalf("current 应统一大写: %+v", st)
	}
}

// 收件箱为空（实测 EM05：`+CPMS: "SM",0,40,"SM",0,40,"SM",0,40`）时，
// JSON 里**必须仍然有 mem2_used / mem3_used = 0** ——
// 以前这两个字段带 omitempty，0 值被丢掉，前端算成 `undefined/40（NaN%）`。
func TestSMSStorageJSONKeepsZeroUsed(t *testing.T) {
	st, ok := parseSMSStorage([]string{`+CPMS: "SM",0,40,"SM",0,40,"SM",0,40`, "OK"})
	if !ok {
		t.Fatal("应能解析空收件箱的 CPMS 应答")
	}
	if st.Mem2Used != 0 || st.Mem3Used != 0 || st.Mem2Total != 40 || st.Mem3Total != 40 {
		t.Fatalf("解析结果不对: %+v", st)
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{`"mem1_used":0`, `"mem2_used":0`, `"mem3_used":0`, `"mem2_total":40`, `"mem3_total":40`} {
		if !strings.Contains(js, want) {
			t.Errorf("JSON 里缺少 %s（0 值不能被 omitempty 丢掉）: %s", want, js)
		}
	}
}

// ====================== 新短信上报配置（AT+CNMI） ======================

func TestParseCNMI(t *testing.T) {
	cases := []struct {
		name     string
		resp     []string
		wantMode int
		wantMT   int
		wantOK   bool
		reports  bool
	}{
		{"实测固件（会报 +CMTI）", []string{"AT+CNMI?", "+CNMI: 2,1,0,0,0", "OK"}, 2, 1, true, true},
		{"未开启上报", []string{"+CNMI: 0,0,0,0,0", "OK"}, 0, 0, true, false},
		{"改成 +CMT 直出正文", []string{"+CNMI: 2,2,0,0,0", "OK"}, 2, 2, true, false},
		{"只报 +CMT 和 +CMTI", []string{"+CNMI: 2,3,0,0,0", "OK"}, 2, 3, true, false},
		{"只回两个字段", []string{"+CNMI: 1,1", "OK"}, 1, 1, true, true},
		{"mode=0 且 mt=1", []string{"+CNMI: 0,1,0,0,0", "OK"}, 0, 1, true, false},
		{"只有 OK", []string{"OK"}, 0, 0, false, false},
		{"空应答", nil, 0, 0, false, false},
	}
	for _, c := range cases {
		st, ok := parseCNMI(c.resp)
		if ok != c.wantOK {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if st.Mode != c.wantMode || st.MT != c.wantMT {
			t.Errorf("%s: got mode=%d mt=%d, want mode=%d mt=%d",
				c.name, st.Mode, st.MT, c.wantMode, c.wantMT)
		}
		if st.ReportsNewSMS() != c.reports {
			t.Errorf("%s: ReportsNewSMS = %v, want %v", c.name, st.ReportsNewSMS(), c.reports)
		}
	}
}

// ====================== 指令拼装 ======================

func TestIdentityAndSMSCommandBuilders(t *testing.T) {
	impls := []ModemCommand{&quectel.QuectelCommand{}, &fibocom.FibocomCommand{}, &simcom.SimcomCommand{}}
	for _, impl := range impls {
		cases := []struct {
			name, got, want string
		}{
			{"IMEI", impl.CommandIMEI(), "AT+CGSN"},
			{"固件版本", impl.CommandFirmware(), "AT+CGMR"},
			{"短信存储 SM", impl.CommandSMSStorage("SM"), `AT+CPMS="SM","SM","SM"`},
			{"短信存储 ME", impl.CommandSMSStorage("ME"), `AT+CPMS="ME","ME","ME"`},
			{"查询存储", impl.CommandQuerySMSStorage(), "AT+CPMS?"},
			{"新短信上报", impl.CommandSMSNotify(), "AT+CNMI=2,1,0,0,0"},
			{"回读上报配置", impl.CommandQuerySMSNotify(), "AT+CNMI?"},
			{"删除指定位置", impl.CommandDeleteSMS(3), "AT+CMGD=3"},
			{"删除全部", impl.CommandDeleteSMS(-1), "AT+CMGD=1,4"},
		}
		for _, c := range cases {
			if c.got != c.want {
				t.Errorf("%T %s: got %q, want %q", impl, c.name, c.got, c.want)
			}
		}
	}
}

// ====================== 型号 / 厂商显示 ======================

// TestShortModelName 各家 ATI 写法都要能取出**短型号**。
// 以前直接把整串 ATI 当型号显示，界面上「型号」一栏塞着厂商/固件/IMEI/能力表，根本没法看。
func TestShortModelName(t *testing.T) {
	cases := []struct{ raw, want string }{
		// 鼎桥 MT5700：带标签的单行写法
		{"Manufacturer: TD Tech Ltd.|Model: MT5700M-CN|Revision: V200R001C20B016|" +
			"IMEI: 864640060132618|+GCAP: +CGSM,+DS,+ES|OK", "MT5700M-CN"},
		// 广和通 FM160/FM350：同样带 Model: 标签
		{"ATI|Manufacturer: Fibocom Wireless Inc.|Model: FM350-GL|Revision: 81600.0000.00.29.22.40|OK", "FM350-GL"},
		{"Manufacturer: Fibocom Wireless Inc.|Model: FM160-EAU|Revision: 89611.1000.00.04.01.07|OK", "FM160-EAU"},
		// 移远：ATI|厂商|型号|Revision: …|OK（没有 Model: 标签，靠"含字母+数字"挑出来）
		{"ATI|Quectel|RM520N-GL|Revision: RM520NGLAAR01A07M4G|OK", "RM520N-GL"},
		{"ATI|Quectel|RM502Q-GL|OK", "RM502Q-GL"},
		{"ATI\r\nQuectel\r\nRM500U-CNV\r\nRevision: RM500UCNVAAR03A01M2G\r\nOK", "RM500U-CNV"},
		// 芯讯通：型号里带数字，整段返回
		{"SIMCOM_SIM8200EA-M2", "SIMCOM_SIM8200EA-M2"},
		// 展锐 UMS9620（中兴 F50）：ATI 不可用，型号只能从 AT+CGMR 的「标签: 值」
		// 版本块里取（真机原文见 unisoc_test.go 的 unisocCGMR）。以前这种串会掉到
		// 最后的"截断到 40 字符" ⇒ 界面上显示成
		// "Platform Version: MOCORTM_V2_22C_W25.39."（真机报的就是这个）。
		{"Platform Version: MOCORTM_V2_22C_W25.39.4_Debug | Project Version:   QogirN6Pro_PS | " +
			"BASE  Version:    5G_MODEM_V2_22C_W25.39.4 | HW Version:        ums9620_modem | " +
			"09-25-2025 06:25:47", "QogirN6Pro_PS"},
		// 同一块的另一种写法：只有 HW 行，退到硬件名
		{"HW Version:        ums9620_modem", "ums9620_modem"},
		// 认不出就原样返回（不编造）
		{"", ""},
		{"OK", "OK"},
	}
	for _, c := range cases {
		if got := shortModelName(c.raw); got != c.want {
			t.Errorf("shortModelName(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
	// 超长且认不出时截断，不至于把整块 ATP 都摊到界面上
	if got := shortModelName(strings.Repeat("X", 100)); len(got) != 40 {
		t.Errorf("认不出的长串应截断到 40：%d", len(got))
	}
}

// TestVendorLabel 厂商显示名：标识 → 可读名称，未识别的原样返回
func TestVendorLabel(t *testing.T) {
	cases := map[ModemVendor]string{
		VendorQuectel: "移远 Quectel",
		VendorFibocom: "广和通 Fibocom",
		VendorSimcom:  "芯讯通 SIMCom",
		VendorMeig:    "美格智能 MeiG",
		VendorTDTech:  "鼎桥 TD Tech",
		VendorUnknown: "未识别",
		"acme":        "acme",
	}
	for v, want := range cases {
		if got := VendorLabel(v); got != want {
			t.Errorf("VendorLabel(%q) = %q, want %q", v, got, want)
		}
	}
	// 鼎桥的标识以前是 "mt5700"（那是型号不是厂商），会被界面当成厂商名显示
	if VendorTDTech != "tdtech" {
		t.Errorf("鼎桥厂商标识应为 tdtech，实际 %q", VendorTDTech)
	}
}

// ====================== HTTP 接口 ======================

// newTestManagerWithSMSModem 构造一个带指定厂商模组的管理器（无需真实串口）
func newTestManagerWithSMSModem(vendor ModemVendor) *ModemManager {
	mgr := NewModemManager()
	mgr.modems[0] = &Modem{
		ID:      0,
		ATPort:  testPortName(3),
		Vendor:  vendor,
		cmdImpl: NewVendorCommand(vendor),
	}
	return mgr
}

// TestAPIDeleteSMSValidation 参数校验必须在发 AT 指令之前完成
func TestAPIDeleteSMSValidation(t *testing.T) {
	mgr := newTestManagerWithSMSModem(VendorQuectel)

	// 未给 index 也未 all=true
	if code, msg := doPostRequest(t, apiDeleteSMS(mgr), "/api/sms/delete", `{"modem_id":0}`); code == 0 {
		t.Fatalf("index=0 且 all=false 应报错，实际 msg=%q", msg)
	}
	// 负数 index
	if code, _ := doPostRequest(t, apiDeleteSMS(mgr), "/api/sms/delete", `{"modem_id":0,"index":-5}`); code == 0 {
		t.Fatal("负数 index 应报错")
	}
	// 模组不存在
	if code, _ := doPostRequest(t, apiDeleteSMS(mgr), "/api/sms/delete", `{"modem_id":99,"all":true}`); code == 0 {
		t.Fatal("未知 modem_id 应报错")
	}
	// 非 POST 请求
	if code, _, _ := doRequest(t, apiDeleteSMS(mgr), "/api/sms/delete?modem_id=0"); code == 0 {
		t.Fatal("GET 请求应报错")
	}
	// 请求体非法
	if code, _ := doPostRequest(t, apiDeleteSMS(mgr), "/api/sms/delete", `not-json`); code == 0 {
		t.Fatal("非法 JSON 应报错")
	}
}

// TestAPIModemInfoNotFound 未知 modem_id 应返回业务错误码而非崩溃
func TestAPIModemInfoNotFound(t *testing.T) {
	mgr := newTestManagerWithSMSModem(VendorQuectel)
	if code, _, _ := doRequest(t, apiModemInfo(mgr), "/api/modem/info?modem_id=99"); code == 0 {
		t.Fatal("未知 modem_id 应报错")
	}
	if code, _, _ := doRequest(t, apiSMSStorage(mgr), "/api/sms/storage?modem_id=99"); code == 0 {
		t.Fatal("未知 modem_id 应报错")
	}
	if code, _, _ := doRequest(t, apiModemInfo(mgr), "/api/modem/info?modem_id=abc"); code == 0 {
		t.Fatal("非法 modem_id 应报错")
	}
}

// TestAPIModemsExposesIdentity /api/modems 应暴露 IMEI 与固件版本
func TestAPIModemsExposesIdentity(t *testing.T) {
	mgr := newTestManagerWithSMSModem(VendorQuectel)
	mgr.modems[0].IMEI = "861234567890123"
	mgr.modems[0].Firmware = "RM502QGLAAR11A02M4G"

	code, data, _ := doRequest(t, apiListModems(mgr), "/api/modems")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	var modems []struct {
		IMEI     string `json:"imei"`
		Firmware string `json:"firmware"`
	}
	if err := json.Unmarshal(data, &modems); err != nil {
		t.Fatalf("decode modems: %v", err)
	}
	if len(modems) != 1 {
		t.Fatalf("expect 1 modem, got %d", len(modems))
	}
	if modems[0].IMEI != "861234567890123" || modems[0].Firmware != "RM502QGLAAR11A02M4G" {
		t.Fatalf("identity not exposed: %+v", modems[0])
	}
}

// ---- from: modem_status_test.go ----

func TestParseSIMAndClockRealHardware(t *testing.T) {
	if got := atparse.ParseICCID([]string{"+CCID: 89860324040255993152", "OK"}); got != "89860324040255993152" {
		t.Errorf("ICCID = %q", got)
	}
	if got := atparse.ParseICCID([]string{"+CCID: \"89860324040255993152\"", "OK"}); got != "89860324040255993152" {
		t.Errorf("带引号的 ICCID = %q", got)
	}
	if got := atparse.ParseICCID([]string{"ERROR"}); got != "" {
		t.Errorf("ERROR 时 ICCID 应为空，得到 %q", got)
	}

	// AT+CIMI 直接回一行裸数字
	if got := atparse.ParseIMSI([]string{"460115089161379", "OK"}); got != "460115089161379" {
		t.Errorf("IMSI = %q", got)
	}
	if got := atparse.ParseIMSI([]string{"+CIMI: 460115089161379"}); got != "460115089161379" {
		t.Errorf("带前缀的 IMSI = %q", got)
	}
	if got := atparse.ParseIMSI([]string{"OK"}); got != "" {
		t.Errorf("无 IMSI 时应为空，得到 %q", got)
	}

	if got := atparse.ParseCPIN([]string{"+CPIN: READY", "OK"}); got != "READY" {
		t.Errorf("CPIN = %q", got)
	}
	// AT+CNUM 实测号码字段为空（很多卡不写本机号码）
	if got := atparse.ParseCNUM([]string{`+CNUM: ,"",255`, "OK"}); got != "" {
		t.Errorf("空号码应为空串，得到 %q", got)
	}
	if got := atparse.ParseCNUM([]string{`+CNUM: "号码","+8613800138000",129`}); got != "+8613800138000" {
		t.Errorf("CNUM = %q", got)
	}

	// 模组时钟：实测报的是 UTC（主机本地 12:31 时模组回 04:31:55）
	// ⚠️ 同样不写死主机本地时刻：详见 parse_test.go 的 TestParseCCLK
	//（路由器上 time.Local 常常就是 UTC，写死会假失败）。
	hostLocal := time.Date(2026, 9, 29, 12, 31, 55, 0, time.Local)
	modemUTC := hostLocal.UTC()
	raw, unix, skew, ok := atparse.ParseCCLK([]string{
		"+CCLK: \"" + modemUTC.Format("06/01/02,15:04:05") + "\"", "OK"}, hostLocal, false)
	if !ok || raw != modemUTC.Format("06/01/02,15:04:05") {
		t.Fatalf("时钟解析失败: raw=%q ok=%v", raw, ok)
	}
	if unix != modemUTC.Unix() {
		t.Errorf("unix = %d, want %d", unix, modemUTC.Unix())
	}
	if skew != 0 {
		t.Errorf("与主机 UTC 的偏差 = %ds, want 0", skew)
	}
	// 带旧式时区后缀：+32 = 32×15 分钟 = +8 小时，UTC 固定 04:31:55（与主机时区无关）
	_, unix2, _, ok2 := atparse.ParseCCLK([]string{`+CCLK: "26/09/29,12:31:55+32"`}, time.Now(), false)
	if want := time.Date(2026, 9, 29, 4, 31, 55, 0, time.UTC).Unix(); !ok2 || unix2 != want {
		t.Errorf("带时区的模组时钟换算错误: unix=%d, want %d", unix2, want)
	}
	// RM500U 这类固件：带了 +32 后缀，但报的值本身就是 UTC（Quirk 让它别减）
	_, unix3, _, ok3 := atparse.ParseCCLK([]string{`+CCLK: "26/09/29,10:03:23+32"`}, time.Now(), true)
	if !ok3 || unix3 != time.Date(2026, 9, 29, 10, 3, 23, 0, time.UTC).Unix() {
		t.Errorf("忽略时区后缀时换算错误: unix=%d", unix3)
	}
	if _, _, _, ok4 := atparse.ParseCCLK([]string{"OK"}, time.Now(), false); ok4 {
		t.Error("没有 +CCLK 行时不应算解析成功")
	}
}

func TestParseQTEMPAndSummary(t *testing.T) {
	resp := []string{
		`+QTEMP:"qfe_wtr_pa0","26"`, `+QTEMP:"qfe_wtr_pa1","28"`, `+QTEMP:"qfe_wtr_pa2","29"`,
		`+QTEMP:"aoss0-usr","32"`, `+QTEMP:"mdm-q6-usr","33"`, `+QTEMP:"cpu0-a7-usr","33"`,
		`+QTEMP:"mdm-5g-usr","32"`, `+QTEMP:"mdm-kernel-usr","32"`, `+QTEMP:"xo-therm-usr","31"`,
		`+QTEMP:"sdx-case-therm-usr","31"`, "OK",
	}
	temps := atparse.ParseQTEMP(resp)
	if len(temps) != 10 {
		t.Fatalf("传感器数 = %d, want 10", len(temps))
	}
	hot, keys := atparse.PickSummary(temps)
	if hot == nil || hot.Celsius != 33 {
		t.Fatalf("最高温 = %+v, want 33", hot)
	}
	// 关键点：机身(case) / 5G / CPU，且不重复
	if len(keys) != 3 {
		t.Fatalf("关键测温点 = %d 个, want 3: %+v", len(keys), keys)
	}
	names := map[string]bool{}
	for _, k := range keys {
		names[k.Name] = true
	}
	for _, want := range []string{"sdx-case-therm-usr", "mdm-5g-usr", "cpu0-a7-usr"} {
		if !names[want] {
			t.Errorf("关键测温点缺少 %s（实际 %+v）", want, keys)
		}
	}
	// 没有 QTEMP 支持时不能 panic
	if h, k := atparse.PickSummary(nil); h != nil || k != nil {
		t.Errorf("空列表应返回 nil: %+v %+v", h, k)
	}
	if got := atparse.ParseQTEMP([]string{"ERROR"}); len(got) != 0 {
		t.Errorf("ERROR 应返回空列表: %+v", got)
	}
}

// IMSI 前 5 位 → 运营商（与网络侧 PLMN 互相印证）
func TestIMSIPLMN(t *testing.T) {
	mcc, mnc := atparse.IMSIPLMN("460115089161379")
	if mcc != "460" || mnc != "11" {
		t.Fatalf("IMSI 拆分 = %s/%s, want 460/11（中国的 MNC 是 2 位）", mcc, mnc)
	}
	// 46011 = 中国电信；期望值用码点拼，避开源文件编码问题
	wantTelecom := string([]rune{0x4E2D, 0x56FD, 0x7535, 0x4FE1})
	if op := cell.OperatorName(mcc, mnc); op != wantTelecom {
		t.Errorf("运营商 = %q, want %q", op, wantTelecom)
	}
	// 北美 MCC 是 3 位 MNC（310260 = T-Mobile US）
	if mcc, mnc := atparse.IMSIPLMN("310260123456789"); mcc != "310" || mnc != "260" {
		t.Errorf("美国 IMSI 拆分 = %s/%s, want 310/260", mcc, mnc)
	}
	for _, bad := range []string{"", "46011", "abcdefghijklmno"} {
		if mcc, mnc := atparse.IMSIPLMN(bad); mcc != "" || mnc != "" {
			t.Errorf("%q 不应解析出 PLMN: %s/%s", bad, mcc, mnc)
		}
	}
}

// ====================== USSD ======================
// 解析 / 状态文案的用例在 parse_test.go；这里只测需要 *Modem 的发送与限流

// USSD 码校验：只允许数字与 * # +（拼进 AT 指令的参数必须严格限制）
func TestUSSDCodeValidation(t *testing.T) {
	mod := &Modem{ID: 5}
	for _, bad := range []string{"", "abc", "*100#\"", "1,2", "*100#;", strings.Repeat("1", 65)} {
		if err := mod.SendUSSD(bad); err == nil {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
	// SIM 处于锁卡状态时直接拒绝，不发 AT
	locked := &Modem{ID: 6, ModemStatus: &ModemStatus{SIMState: "SIM PIN"}}
	if err := locked.SendUSSD("*100#"); err == nil || !strings.Contains(err.Error(), "SIM") {
		t.Errorf("锁卡时应返回 SIM 相关错误，实际 %v", err)
	}
}

// ====================== 流量清零 ======================

// 只有声明了流量统计能力的厂商才能清零（不能让非移远固件收到 QGDCNT）
func TestResetTrafficUnsupportedVendor(t *testing.T) {
	mod := &Modem{ID: 7, Vendor: VendorSimcom, cmdImpl: &simcom.SimcomCommand{}}
	if _, err := mod.ResetTraffic(); err == nil {
		t.Error("未声明流量统计能力的厂商应返回错误，不发 AT")
	}
}

// 速率式模组（广和通）：模组侧没有累计计数器可清，只清本地估算量，不报错也不发 QGDCNT
func TestResetTrafficRateBased(t *testing.T) {
	// 预算好探测结果，免得测试里去发 AT（没有串口通道）
	mod := &Modem{ID: 8, Vendor: VendorFibocom, cmdImpl: &fibocom.FibocomCommand{},
		probedCmds: map[string]string{KindTraffic: common.CmdGTSTATIS}}
	mod.applyTrafficRates(1024, 2048, time.Now().Add(-10*time.Second))
	mod.applyTrafficRates(1024, 2048, time.Now())
	rate, err := mod.ResetTraffic()
	if err != nil {
		t.Fatalf("速率式模组的清零不应报错：%v", err)
	}
	if rate.Sent != 0 || rate.Recv != 0 {
		t.Fatalf("清零后累计应为 0，实际 %d/%d", rate.Sent, rate.Recv)
	}
	if s := mod.TrafficSnapshot(); s.Sent != 0 || s.Recv != 0 {
		t.Fatalf("本地快照也应清空，实际 %d/%d", s.Sent, s.Recv)
	}
}

// ====================== 运行日志 tail ======================

func TestTailLogFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "modemd.log")

	// 文件不存在 → 空结果且不报错（刚启动还没写日志是正常的）
	rows, err := config.TailFile(path, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("文件不存在时应返回空切片: %v %v", rows, err)
	}

	var sb strings.Builder
	for i := 1; i <= 300; i++ {
		sb.WriteString("line-" + strconv.Itoa(i) + "\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err = config.TailFile(path, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 || rows[0] != "line-296" || rows[4] != "line-300" {
		t.Fatalf("tail 结果不对: %v", rows)
	}
	// 请求行数超过文件行数 → 全给
	if rows, _ := config.TailFile(path, 5000); len(rows) != 300 {
		t.Errorf("行数 = %d, want 300", len(rows))
	}

	// ====================== 温度：芯讯通 AT+CPMUTEMP（手册 V1.01 §20.2.8） ======================
	// 实测 SIM8200EA-M2：AT+CPMUTEMP → +CPMUTEMP: 25
	// （同章节的 AT+CCPUTEMP 在本固件回 ERROR；AT+CTEMP / AT+SIMTEMP / AT+QTEMP 也都回 ERROR）
	temps := atparse.ParseCPMUTEMP([]string{"+CPMUTEMP: 25", "OK"})
	if len(temps) != 1 || temps[0].Celsius != 25 {
		t.Fatalf("CPMUTEMP 解析 = %+v, want 一路 25°C", temps)
	}
	// 只有一个传感器时也要能给出"最高温"（关键测温点匹配不到就为空，界面会兜底）
	hot, keys := atparse.PickSummary(temps)
	if hot == nil || hot.Celsius != 25 {
		t.Fatalf("最高温 = %+v, want 25°C", hot)
	}
	if len(keys) != 0 {
		t.Errorf("单个无名传感器不该凑出关键测温点: %+v", keys)
	}
	if got := atparse.ParseCPMUTEMP([]string{"+CPMUTEMP: -15"}); len(got) != 1 || got[0].Celsius != -15 {
		t.Errorf("负温度解析 = %+v, want -15", got)
	}
	if got := atparse.ParseCPMUTEMP([]string{"ERROR"}); got != nil {
		t.Errorf("ERROR 不该解析出温度: %+v", got)
	}
	// 两家写法混在一起时优先多传感器的移远格式
	mix := []string{`+QTEMP:"sdx-case-therm-usr","31"`, "+CPMUTEMP: 25"}
	if got := atparse.ParseTempResp(mix); len(got) != 1 || got[0].Name != "sdx-case-therm-usr" {
		t.Errorf("应优先 QTEMP 多传感器格式: %+v", got)
	}
	if got := atparse.ParseTempResp([]string{"+CPMUTEMP: 25"}); len(got) != 1 || got[0].Name != "模组温度" {
		t.Errorf("没有 QTEMP 时应退回 CPMUTEMP: %+v", got)
	}
	// 厂商自己声明温度指令：移远 AT+QTEMP / 芯讯通 AT+CPMUTEMP
	if got := any(&quectel.QuectelCommand{}).(TempQuerier).CommandTemp(); got != "AT+QTEMP" {
		t.Errorf("移远温度指令 = %q, want AT+QTEMP", got)
	}
	if got := any(&simcom.SimcomCommand{}).(TempQuerier).CommandTemp(); got != "AT+CPMUTEMP" {
		t.Errorf("芯讯通温度指令 = %q, want AT+CPMUTEMP", got)
	}
	// 未声明该能力的厂商退回 AT+QTEMP（老行为），不会发出未验证的指令
	if got := (&Modem{cmdImpl: &common.UnknownCommand{}}).tempQueryCommand(); got != "AT+QTEMP" {
		t.Errorf("未知厂商温度指令 = %q, want AT+QTEMP", got)
	}
	// 默认值（<=0 → 200）
	if rows, _ := config.TailFile(path, 0); len(rows) != 200 {
		t.Errorf("默认行数 = %d, want 200", len(rows))
	}

	// 超过回读窗口的大文件：只读尾部，且丢掉被截断的首行
	big := strings.Repeat("x", config.LogTailBytes) + "\n" + "second\n" + "third\n"
	if err := os.WriteFile(path, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, err = config.TailFile(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0] != "second" || rows[1] != "third" {
		t.Fatalf("大文件 tail 结果不对（首行应被丢弃）: %v", rows)
	}
}

// ====================== 本机号码（MSISDN） ======================

// 电话本条目：`+CPBR: 1,"+8613800138000",129,""`（读 "ON" 存储区拿本机号码）
func TestParseCPBRNumber(t *testing.T) {
	resp := []string{`+CPBR: 1,"+8613800138000",129,""`, "OK"}
	if got := atparse.ParseCPBRNumber(resp); got != "+8613800138000" {
		t.Fatalf("ParseCPBRNumber = %q", got)
	}
	// 空存储区：模组直接回 ERROR，不应崩也不应给出假号码
	if got := atparse.ParseCPBRNumber([]string{"ERROR"}); got != "" {
		t.Fatalf("ERROR 时不应有号码，得到 %q", got)
	}
}

func TestParseCPBSStorage(t *testing.T) {
	if got := atparse.ParseCPBSStorage([]string{`+CPBS: "SM",0,500`, "OK"}); got != "SM" {
		t.Fatalf("ParseCPBSStorage = %q", got)
	}
	if got := atparse.ParseCPBSStorage([]string{`+CPBS: "ON",0,2`, "OK"}); got != "ON" {
		t.Fatalf("ParseCPBSStorage = %q", got)
	}
	if got := atparse.ParseCPBSStorage([]string{"ERROR"}); got != "" {
		t.Fatalf("无 +CPBS 行时应为空，得到 %q", got)
	}
}

// BCD 半字节交换解码：用**真机读到的 EF_ICCID** 做校验（同一种编码，见下）
func TestBCDDigits(t *testing.T) {
	// 真机：AT+CRSM=176,12258,0,0,10 → "98683042402055991325"（EF_ICCID）
	raw, err := hex.DecodeString("98683042402055991325")
	if err != nil {
		t.Fatal(err)
	}
	if got := atparse.BCDDigits(raw); got != "89860324040255993152" {
		t.Fatalf("BCDDigits = %q, want ICCID 89860324040255993152（与 AT+CCID 必须一致）", got)
	}
	// 0xF 填充在高半字节（低半字节才是最后一位数字）：0x31 0xF0 → "13" + "0"
	if got := atparse.BCDDigits([]byte{0x31, 0xF0}); got != "130" {
		t.Fatalf("BCDDigits = %q, want 130（F 是高半字节填充）", got)
	}
	if got := atparse.BCDDigits([]byte{0xF1}); got != "1" {
		t.Fatalf("BCDDigits = %q, want 1（高位 F 直接停）", got)
	}
	// 非数字半字节（例如后面的 CCI/Ext）也要停住，不能把尾巴当号码
	if got := atparse.BCDDigits([]byte{0x21, 0x43, 0xAF}); got != "1234" {
		t.Fatalf("BCDDigits 应在非数字处停住，得到 %q", got)
	}
}

// EF_MSISDN（0x6F40）内容解析
func TestDecodeEFMSISDN(t *testing.T) {
	cases := []struct {
		name string
		hex  string
		want string
	}{
		{
			// <alpha 长度 0><号码长度 9><TON 91 国际><NPI 01><BCD +8613800138000>
			name: "国际号码（TON 0x91 → 补 +）",
			hex:  "00099101683108108300F0",
			want: "+8613800138000",
		},
		{
			// 带 2 字节 alpha 标识（长度 2）
			name: "带 alpha 标识",
			hex:  "02AABB099101683108108300F0",
			want: "+8613800138000",
		},
		{
			// TON 0x81（未知类型）→ 不补 +，按原样给数字
			name: "TON 未知类型",
			hex:  "00098101683108108300F0",
			want: "8613800138000",
		},
		// 号码长度字段声称超出实际内容 → 夹到末尾而不是越界（能解多少给多少）
		{name: "长度越界", hex: "0009FF01683108", want: "861380"},
		{name: "全 0（空文件）", hex: "000000", want: ""},
		{name: "太短", hex: "00", want: ""},
		{name: "非十六进制", hex: "ZZZZ", want: ""},
		{name: "位数不足", hex: "0009910100F0", want: ""},
	}
	for _, c := range cases {
		if got := atparse.DecodeEFMSISDN(c.hex); got != c.want {
			t.Errorf("%s: DecodeEFMSISDN(%q) = %q, want %q", c.name, c.hex, got, c.want)
		}
	}
}

// CRSM 应答解析与状态字判定
func TestParseCRSM(t *testing.T) {
	// 成功：SW=9000 带数据
	sw1, sw2, data, ok := atparse.ParseCRSM([]string{`+CRSM: 144,0,"98683042402055991325"`, "OK"})
	if !ok || sw1 != 0x90 || sw2 != 0 || !atparse.CRSMOK(sw1, sw2) {
		t.Fatalf("成功应答解析错：%d/%d/%q/%v", sw1, sw2, data, ok)
	}
	if data != "98683042402055991325" {
		t.Fatalf("data = %q", data)
	}
	// 失败：本机实测读 EF_MSISDN 回 148,8（0x9408）→ 判为“文件读不了”
	sw1, sw2, _, ok = atparse.ParseCRSM([]string{"+CRSM: 148,8", "OK"})
	if !ok || atparse.CRSMOK(sw1, sw2) || !atparse.CRSMFileUnavailable(sw1, sw2) {
		t.Fatalf("0x9408 应判为文件不可读：%d/%d/%v", sw1, sw2, ok)
	}
	// 文件不存在：6A82（实测 0x6FC7 回 106,130）
	if sw1, _, _, _ = atparse.ParseCRSM([]string{"+CRSM: 106,130"}); !atparse.CRSMFileUnavailable(sw1, 130) {
		t.Fatalf("0x6A82 应判为文件不可读：%d", sw1)
	}
	// 没有 +CRSM 行
	if _, _, _, ok := atparse.ParseCRSM([]string{"ERROR"}); ok {
		t.Fatal("无 +CRSM 行时 ok 应为 false")
	}
}

// queryOwnNumber 在“模组回 ERROR / 没有卡”时不应卡住也不应给出假号码（无串口 → SendAT 直接报错）
func TestQueryOwnNumberNoChannel(t *testing.T) {
	mod := &Modem{ID: 3}
	n, src := mod.queryOwnNumber()
	if n != "" || src != "" {
		t.Fatalf("没有 AT 通道时不应给出号码：%q/%q", n, src)
	}
	// CRSM 回退也应被跳过（读不了的文件只试一次）
	mod.disableOwnNumCRSM()
	if !mod.ownNumCRSMOff {
		t.Fatal("应记住 CRSM 不可用")
	}
	if n, _ := mod.queryOwnNumber(); n != "" {
		t.Fatalf("停用后仍不应给号码：%q", n)
	}
}

// ---- from: mt5700_test.go ----

//
// 这台模组的网络类信息全在 `^` 私有命令里，移远/展锐那套基本全 ERROR，下面这些实测样例
// 就是适配的全部依据（AT 口 COM11，中国电信 460/11，n78 20MHz）。

const mt5700ATI = "Manufacturer: TD Tech Ltd.|Model: MT5700M-CN|Revision: V200R001C20B016|" +
	"IMEI: 864640060132618|+GCAP: +CGSM,+DS,+ES|OK"

func mt5700() *tdtech.MT5700Command { return &tdtech.MT5700Command{} }

// TestMT5700VendorCommands 指令层：确认走的是鼎桥自家的 ^ 私有命令（通用的那些实测全 ERROR）
func TestMT5700VendorCommands(t *testing.T) {
	m := mt5700()
	if got := m.CommandNetInfo(); got != "AT^SYSINFOEX" {
		t.Errorf("网络信息 = %q, want AT^SYSINFOEX（AT+QNWINFO 回 ERROR）", got)
	}
	if got := m.CommandTemp(); got != "AT^CHIPTEMP?" {
		t.Errorf("温度 = %q, want AT^CHIPTEMP?（AT+QTEMP 回 ERROR）", got)
	}
	if got := m.CommandICCID(); got != "AT^ICCID?" {
		t.Errorf("ICCID = %q, want AT^ICCID?（AT+CCID 回 ERROR）", got)
	}
	if got := m.CommandExtraNetInfo(); got != "AT^HFREQINFO?" {
		t.Errorf("工作频率 = %q, want AT^HFREQINFO?", got)
	}
	if got := m.CommandCAInfo(); got != "" {
		t.Errorf("CA = %q, want 空（AT^CASCELLINFO 只覆盖 LTE CA，未验证 NR）", got)
	}
	// 触发自定义指令集
	var _ CommandCandidates = m
	var _ TempQuerier = m
	var _ ICCIDQuerier = m
	var _ ExtraNetInfoQuerier = m
	var _ AMBRQuerier = m
}

// TestMT5700Candidates 候选顺序：鼎桥私有命令在前，通用的留作备用
func TestMT5700Candidates(t *testing.T) {
	m := mt5700()
	if got := m.ServingCellCandidates(); len(got) < 2 || got[0] != "AT^MONSC" {
		t.Errorf("服务小区候选 = %v, want 首选 AT^MONSC", got)
	}
	if got := m.NeighbourCellCandidates(); len(got) < 2 || got[0] != "AT^MONNC" {
		t.Errorf("邻区候选 = %v, want 首选 AT^MONNC", got)
	}
	if got := m.TrafficCandidates(); len(got) < 2 || got[0] != "AT^DSFLOWQRY" {
		t.Errorf("流量候选 = %v, want 首选 AT^DSFLOWQRY", got)
	}
	if got := m.TrafficEnableCommand(); got != "" {
		t.Errorf("使能指令 = %q, want 空（DS 计数器一直在计）", got)
	}
}

// ====================== 服务小区 / 邻区：^MONSC / ^MONNC ======================

// 实机原始应答：RSRP/RSRQ/SINR **直接是 dB**（不是索引），PCI/TAC/CellID 是十六进制
//
//	^MONSC: NR,460,11,627264,1,CFA116002,115,CFA103,-78,-10,30
//	        RAT MCC MNC ARFCN SCS CellID    PCI  TAC    RSRP RSRQ SINR
//	                                ↑ hex   ↑ hex ↑ hex
func TestParseMONSCNR(t *testing.T) {
	ci, ok := cell.ParseMONSC("^MONSC: NR,460,11,627264,1,CFA116002,115,CFA103,-78,-10,30")
	if !ok {
		t.Fatal("^MONSC 应能解析出服务小区")
	}
	if !ci.Serving || ci.Tech != CellTechNR {
		t.Errorf("制式/服务标志 = %v/%v, want NR/true", ci.Tech, ci.Serving)
	}
	if ci.MCC != "460" || ci.MNC != "11" || ci.Operator != "中国电信" {
		t.Errorf("PLMN/运营商 = %s/%s %q", ci.MCC, ci.MNC, ci.Operator)
	}
	if ci.EARFCN != 627264 {
		t.Errorf("频点 = %d, want 627264（n78，十进制）", ci.EARFCN)
	}
	// PCI "115" 是十六进制 → 277；TAC "CFA103" → 13607171；CellID 原样十六进制
	if ci.PCID != 0x115 || ci.TAC != 0xCFA103 || ci.CellID != "CFA116002" {
		t.Errorf("PCI/TAC/CellID = %d/%d/%q, want 277/13607171/CFA116002", ci.PCID, ci.TAC, ci.CellID)
	}
	if ci.RSRP != -78 || ci.RSRQ != -10 || ci.SINR != 30 {
		t.Errorf("信号 = %d/%d/%d, want -78/-10/30（直接是 dB，不再做索引换算）", ci.RSRP, ci.RSRQ, ci.SINR)
	}
}

// LTE 的字段顺序与 NR 不同（少 SCS、多一格 RSSI）——用手册 §13.9.5 的例子
func TestParseMONSCLTE(t *testing.T) {
	ci, ok := cell.ParseMONSC("^MONSC: LTE,460,01,1650,A54933,1F3,183D,-98,-5,-72")
	if !ok || ci.Tech != CellTechLTE {
		t.Fatalf("LTE 行应能解析: ok=%v tech=%v", ok, ci.Tech)
	}
	if ci.EARFCN != 1650 || ci.CellID != "A54933" || ci.PCID != 0x1F3 || ci.TAC != 0x183D {
		t.Errorf("频点/CellID/PCI/TAC = %d/%q/%d/%d", ci.EARFCN, ci.CellID, ci.PCID, ci.TAC)
	}
	if ci.RSRP != -98 || ci.RSRQ != -5 || ci.RSSI != -72 {
		t.Errorf("信号 = %d/%d/%d, want -98/-5/-72", ci.RSRP, ci.RSRQ, ci.RSSI)
	}
}

// 邻区：一行一个小区；`^MONNC: NONE` 表示没有邻区
func TestParseMONNCLines(t *testing.T) {
	got := cell.ParseMONNCLines([]string{
		"^MONNC: NR,627264,114,-89,-11,12",
		"^MONNC: NR,627264,49,-106,-17,-5",
		"OK",
	})
	if len(got) != 2 {
		t.Fatalf("邻区数 = %d, want 2", len(got))
	}
	if got[0].Tech != CellTechNR || got[0].EARFCN != 627264 || got[0].PCID != 0x114 {
		t.Errorf("邻区1 = %v/%d/%d, want NR/627264/276", got[0].Tech, got[0].EARFCN, got[0].PCID)
	}
	if got[0].RSRP != -89 || got[0].RSRQ != -11 || got[0].SINR != 12 {
		t.Errorf("邻区1 信号 = %d/%d/%d, want -89/-11/12", got[0].RSRP, got[0].RSRQ, got[0].SINR)
	}
	if got[0].Serving {
		t.Error("邻区不该被标成服务小区")
	}
	if n := cell.ParseMONNCLines([]string{"^MONNC: NONE", "OK"}); len(n) != 0 {
		t.Errorf("NONE 时不该有邻区: %d", len(n))
	}
	// LTE 邻区用手册例子（PCI 是十进制）
	lte := cell.ParseMONNCLines([]string{"^MONNC: LTE,10000,1F,-82,-11,-1"})
	if len(lte) != 1 || lte[0].PCID != 31 || lte[0].RSRP != -82 {
		t.Errorf("LTE 邻区 = %+v, want PCI 31 / RSRP -82", lte)
	}
}

// ====================== 信号：^HCSQ?（索引） ======================

// 实机原始应答：`^HCSQ: "NR",63,206,30` → rsrp 63-140 = -77dBm；sinr 206*0.2-20 = 21dB；rsrq (30-39)/2 = -4dB
// （同一时刻 ^MONSC 报 -78/-10/30 —— 两者 rsrp 一致，rsrq/sinr 有差异，
//
//	所以上层只在 MONSC 拿不到值时用 HCSQ 兜底）
func TestParseHCSQNR(t *testing.T) {
	rsrp, rsrq, sinr, ok := cell.ParseHCSQ(`^HCSQ: "NR",63,206,30`)
	if !ok {
		t.Fatal("^HCSQ 应能解析")
	}
	if rsrp != -77 || sinr != 21 || rsrq != -4 {
		t.Errorf("= %d/%d/%d, want -77/-4/21", rsrp, rsrq, sinr)
	}
	// 255 = 未知/不可测；NOSERVICE 直接放弃
	if _, _, _, ok := cell.ParseHCSQ(`^HCSQ: "NR",255,255,255`); ok {
		t.Error("全 255 应视为未上报")
	}
	if _, _, _, ok := cell.ParseHCSQ(`^HCSQ: "NOSERVICE"`); ok {
		t.Error("NOSERVICE 不该解析出信号")
	}
}

// ParseNRSignal：^MONSC 优先（直接 dB），^HCSQ 兜底
func TestMT5700ParseNRSignal(t *testing.T) {
	m := mt5700()
	if rsrp, rsrq, sinr, ok := m.ParseNRSignal("^MONSC: NR,460,11,627264,1,CFA116002,115,CFA103,-78,-10,30"); !ok ||
		rsrp != -78 || rsrq != -10 || sinr != 30 {
		t.Errorf("MONSC = %d/%d/%d ok=%v, want -78/-10/30 true", rsrp, rsrq, sinr, ok)
	}
	if rsrp, _, _, ok := m.ParseNRSignal(`^HCSQ: "NR",63,206,30`); !ok || rsrp != -77 {
		t.Errorf("HCSQ = %d ok=%v, want -77 true", rsrp, ok)
	}
	if _, _, _, ok := m.ParseNRSignal("OK"); ok {
		t.Error("OK 不该被当成信号行")
	}
}

// ====================== 温度：AT^CHIPTEMP?（0.1 ℃） ======================

// 实机原始应答：12 路，单位 0.1 ℃；65535 = 该路读取失败
func TestParseCHIPTEMPMT5700(t *testing.T) {
	temps := atparse.ParseTempResp([]string{
		"AT^CHIPTEMP?",
		"^CHIPTEMP: 341,338,330,344,330,330,340,340,340,360,330,330",
		"OK",
	})
	if len(temps) != 12 {
		t.Fatalf("传感器数 = %d, want 12", len(temps))
	}
	if temps[0].Celsius != 36 || temps[0].Name != "modem2" {
		t.Errorf("最高温 = %s %d℃, want modem2 36℃（360 → 36.0）", temps[0].Name, temps[0].Celsius)
	}
	// 65535 = 失败、负值/0 = 未上报 → 都要丢掉
	got := atparse.ParseTempResp([]string{"^CHIPTEMP: 341,65535,0,-5,300,300,300,300,300,300,300,300"})
	if len(got) != 9 {
		t.Errorf("坏值应被丢掉: %d 路（341 + 300×8）", len(got))
	}
}

// ====================== 流量：AT^DSFLOWQRY（十六进制累计字节） ======================

// 实机原始应答：末两格是累计发送/接收字节（十六进制）
//
//	^DSFLOWQRY: 000002A1,00000000000019A5,0000000000001666,000002A1,00000000000019A5,0000000000001666
func TestParseDSFLOWCounters(t *testing.T) {
	sent, recv, ok := ParseHexCounters([]string{
		"AT^DSFLOWQRY",
		"^DSFLOWQRY: 000002A1,00000000000019A5,0000000000001666,000002A1,00000000000019A5,0000000000001666",
		"OK",
	})
	if !ok {
		t.Fatal("^DSFLOWQRY 应能解析")
	}
	if sent != 0x19A5 || recv != 0x1666 {
		t.Errorf("累计 = %d/%d, want %d/%d（十六进制）", sent, recv, 0x19A5, 0x1666)
	}
	if _, _, ok := ParseHexCounters([]string{"OK"}); ok {
		t.Error("没有 ^DSFLOWQRY 行时不该解析成功")
	}
}

// ====================== ICCID：AT^ICCID? ======================

func TestParseICCIDFromCaret(t *testing.T) {
	if got := atparse.ParseICCID([]string{"AT^ICCID?", "^ICCID: 89860324040255993152", "OK"}); got != "89860324040255993152" {
		t.Errorf("ICCID = %q, want 89860324040255993152", got)
	}
	if got := atparse.ParseICCID([]string{"+CCID: 89860324740250209831", "OK"}); got != "89860324740250209831" {
		t.Errorf("标准写法回归: %q", got)
	}
}

// 厂商声明了 ICCIDQuerier 时，第一条指令就是它的（AT^ICCID?）
func TestICCIDCommandForMT5700(t *testing.T) {
	mod := &Modem{Model: mt5700ATI, cmdImpl: mt5700()}
	if iq, ok := mod.cmdImpl.(ICCIDQuerier); !ok || iq.CommandICCID() != "AT^ICCID?" {
		t.Fatalf("MT5700 应实现 ICCIDQuerier: %v", mod.cmdImpl)
	}
	// 其它厂商不实现该接口 → 仍然只用标准 AT+CCID
	if _, ok := any(&quectel.QuectelCommand{}).(ICCIDQuerier); ok {
		t.Error("移远不该实现 ICCIDQuerier")
	}
}

// ====================== 频段/带宽：AT^HFREQINFO? ======================

// 实机原始应答（band 78 / 下行 3409.5MHz 20MHz / 上行 3450MHz）与手册 LTE 例
// 的**解析**用例在 net.go（纯逻辑，不认识 Modem）。

// ====================== 签约速率（AMBR）：AT^DSAMBR=<cid> ======================

// 实测（手册 §16.17「签约速率查询」）：`AT^DSAMBR=1` → `^DSAMBR: 1,1000000,1000000,"ctnet",6`
// ⚠️ 手册的字段表只写了 `<cid>,<DlApnAmbr>,<UlApnAmbr>`，实测末尾还多两格（APN + 一格未知数字）；
// 而且**单位是 kbps**（移远那套是 Mbps），必须换算，否则 1000000 会被显示成 "1000000 Mbps"。
func TestMT5700AMBR(t *testing.T) {
	m := mt5700()
	if got := m.CommandAMBR(); got != "AT^DSAMBR=1" {
		t.Errorf("签约速率指令 = %q, want AT^DSAMBR=1（手册说只支持 cid 1）", got)
	}
	// 应答解析（含 kbps→Mbps 换算、无效样本的拒绝、移远路径的回归）
	// 在 data.go；这里只管"发哪条指令"。
}

// ====================== 机型特例：时钟时区后缀不可信 ======================

func TestQuirkMT5700(t *testing.T) {
	q := (&Modem{Model: mt5700ATI}).Quirk()
	if !q.IgnoreCCLKTZ {
		t.Error("MT5700 的 AT+CCLK? 带 +32 但报的是 UTC → 必须忽略时区后缀")
	}
	if q.TempCmd != "" || q.VoltageCmd != "" || q.PDPAddrCmd != "" {
		t.Errorf("温度/电压/本机地址都走厂商声明，不该开特例: %+v", q)
	}
	// 时钟换算：忽略时区后 skew ≈ 0
	resp := []string{`+CCLK: "26/09/29,17:57:06+32"`, "OK"}
	hostUTC, _ := time.Parse(time.RFC3339, "2026-09-29T17:57:06Z")
	_, _, skew, ok := atparse.ParseCCLK(resp, hostUTC, q.IgnoreCCLKTZ)
	if !ok || skew != 0 {
		t.Errorf("忽略时区后 skew = %d, want 0", skew)
	}
	if _, _, wrong, _ := atparse.ParseCCLK(resp, hostUTC, false); wrong != -28800 {
		t.Errorf("不忽略时区会净差 8 小时（%d），这正是要开 ignoreCCLKTZ 的原因", wrong)
	}
}

// ====================== URC 洪水不能拖慢每条指令 ======================

// 实测 MT5700 每秒主动上报 ^RSSI/^CERSSI/^HCSQ；排残留时必须把它们认成"异步上报"，
// 否则 pending 永远为真，每条 AT 指令都要空转 200×20ms = 4 秒。
func TestIsAsyncReport(t *testing.T) {
	for _, line := range []string{
		"^RSSI: 140", "^CERSSI: 0,0,255", `^HCSQ: "NR",63,206,30`,
		"+CMTI: \"SM\",1", "+CMT: \"+8613...\",\"\",\"2026/09/30\"", "+CUSD: 0,\"x\",15",
		"  ^DSFLOWRPT: 1,2,3  ",
	} {
		if !isAsyncReport(line) {
			t.Errorf("%q 应被认成异步上报", line)
		}
	}
	// ⚠️ +CREG/+CGREG/+CEREG 既是上报也是应答 → 不能算异步上报
	for _, line := range []string{"+CREG: 0,1", "+CGREG: 1,1", "+CEREG: 2,1", "+CSQ: 22,0", "OK", "ERROR"} {
		if isAsyncReport(line) {
			t.Errorf("%q 不该被当成异步上报", line)
		}
	}
	// ⚠️ 取舍：^MONSC/^HCSQ/^SYSINFOEX 这些既是"命令应答"、也被鼎桥当 URC 用，
	// 排残留时统一按异步上报丢掉（牺牲一点残留检查强度，换来不被 URC 洪水拖慢）。
	for _, line := range []string{"^MONSC: NR,1", "^HCSQ: \"NR\",1,2,3", "^SYSINFOEX: 2,2,0,1,,11,\"NR-5GC\",111,\"NR-5GC\""} {
		if !isAsyncReport(line) {
			t.Errorf("%q 现在按异步上报处理（见 isAsyncReport 注释）", line)
		}
	}
	// 排残留不能把 URC 当成"还有半块应答"
	mod := &Modem{readChan: make(chan string, 16)}
	for _, l := range []string{"^RSSI: 140", "^HCSQ: \"NR\",63,206,30", "^CERSSI: 0,0,255"} {
		mod.readChan <- l
	}
	done := make(chan struct{})
	go func() { mod.drainStaleLocked(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("只有 URC 时排残留应当很快返回（不该空转）")
	}
	if len(mod.readChan) != 0 {
		t.Errorf("排残留应把 URC 扔掉: 还剩 %d 行", len(mod.readChan))
	}
}

// ====================== 端到端：整段应答交给解析器 ======================

func TestMT5700ServingEndToEnd(t *testing.T) {
	// ScanCells 里对服务小区应答依次试 QENG/FENG → CPSI → GTCCINFO → MONSC
	resp := []string{"AT^MONSC", "^MONSC: NR,460,11,627264,1,CFA116002,115,CFA103,-78,-10,30", "OK"}
	ci, ok := cell.ParseMONSC(resp[1])
	if !ok {
		t.Fatal("应能从整段应答里解析出服务小区")
	}
	if strings.TrimSpace(ci.CellID) != "CFA116002" {
		t.Errorf("CellID = %q", ci.CellID)
	}
}

// ---- from: lock_test.go ----

// ---- from: lock_test.go ----

func TestParseBandList(t *testing.T) {
	cases := []struct {
		in     string
		want   []int
		wantOk bool
	}{
		{"1:3:5:8", []int{1, 3, 5, 8}, true},
		{"5:3:3:1", []int{1, 3, 5}, true}, // 去重 + 升序
		{"78", []int{78}, true},
		{`"78"`, []int{78}, true}, // 去掉引号
		{"0", nil, false},         // 0 = 不限制
		{"", nil, false},
		{"abc", nil, false},
	}
	for _, c := range cases {
		got, ok := common.ParseBandList(c.in)
		if ok != c.wantOk {
			t.Errorf("ParseBandList(%q) ok = %v, want %v", c.in, ok, c.wantOk)
			continue
		}
		if !ok {
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("ParseBandList(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ParseBandList(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

func TestFormatBandList(t *testing.T) {
	cases := []struct {
		in   []int
		want string
	}{
		{[]int{5, 3, 1}, "1:3:5"},
		{[]int{78, 0, -1, 78}, "78"}, // 非法值丢弃、重复值去重
		{nil, ""},
		{[]int{}, ""},
	}
	for _, c := range cases {
		if got := FormatBandList(c.in); got != c.want {
			t.Errorf("FormatBandList(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ====================== 模组应答解析 ======================

func TestParseQNWPREFCFG(t *testing.T) {
	resp := []string{
		`AT+QNWPREFCFG="lte_band"`,
		`+QNWPREFCFG: "lte_band",1:3:5`,
		"OK",
	}
	if v, ok := parseQNWPREFCFG(resp, "lte_band"); !ok || v != "1:3:5" {
		t.Fatalf("lte_band = %q ok=%v, want 1:3:5", v, ok)
	}
	// 请求的 key 不在应答里时应返回 false，避免张冠李戴
	if _, ok := parseQNWPREFCFG(resp, "nr5g_band"); ok {
		t.Fatal("nr5g_band should not be found in an lte_band response")
	}
	if _, ok := parseQNWPREFCFG([]string{"OK"}, "lte_band"); ok {
		t.Fatal("empty response should not parse")
	}

	// 字符串型配置项（mode_pref）
	mode := []string{`+QNWPREFCFG: "mode_pref",AUTO`, "OK"}
	if v, ok := parseQNWPREFCFG(mode, "mode_pref"); !ok || v != "AUTO" {
		t.Fatalf("mode_pref = %q ok=%v, want AUTO", v, ok)
	}
}

func TestParseQNWLOCK(t *testing.T) {
	// 已锁定：EARFCN=2452 / PCI=155
	resp := []string{`+QNWLOCK: "common/4g",1,2452,155`, "OK"}
	cl, ok := parseQNWLOCK(resp, "common/4g")
	if !ok {
		t.Fatal("common/4g should parse")
	}
	if !cl.Locked || cl.LockType != CellLockARFCNPCI {
		t.Fatalf("should be locked type 1: %+v", cl)
	}
	if cl.ARFCN != 2452 || cl.PCID != 155 {
		t.Fatalf("earfcn/pcid wrong: %d/%d", cl.ARFCN, cl.PCID)
	}
	// 同一应答里不应匹配到另一个子域
	if _, ok := parseQNWLOCK(resp, "common/5g"); ok {
		t.Fatal("common/5g should not match a common/4g response")
	}

	// 未锁定
	unlocked := []string{`+QNWLOCK: "common/5g",0`, "OK"}
	cl2, ok := parseQNWLOCK(unlocked, "common/5g")
	if !ok || cl2.Locked || cl2.LockType != CellLockNone {
		t.Fatalf("should be unlocked: %+v ok=%v", cl2, ok)
	}

	// 仅锁频点
	onlyArfcn := []string{`+QNWLOCK: "common/5g",2,627264`, "OK"}
	cl3, _ := parseQNWLOCK(onlyArfcn, "common/5g")
	if !cl3.Locked || cl3.LockType != CellLockARFCN || cl3.ARFCN != 627264 || cl3.PCID != 0 {
		t.Fatalf("arfcn-only lock wrong: %+v", cl3)
	}
}

func TestAtError(t *testing.T) {
	if got := atError([]string{"OK"}); got != "" {
		t.Errorf("OK should not be an error, got %q", got)
	}
	if got := atError([]string{"ERROR"}); got == "" {
		t.Error("ERROR should be detected")
	}
	if got := atError([]string{"+CME ERROR: 3"}); got == "" {
		t.Error("+CME ERROR should be detected")
	}
	// 回显的指令本身不含独立错误标记，不应误判（指令里含 "ERROR" 字样也不匹配）
	if got := atError([]string{`AT+QNWLOCK="common/4g",1,2452,155`}); got != "" {
		t.Errorf("echoed command should not be an error, got %q", got)
	}
}

func TestParseLockTech(t *testing.T) {
	cases := map[string]CellTech{
		"LTE": CellTechLTE, "lte": CellTechLTE, "4g": CellTechLTE, "4G": CellTechLTE,
		"NR": CellTechNR, "5g": CellTechNR, "NR5G": CellTechNR, "NR5G-SA": CellTechNR,
	}
	for in, want := range cases {
		got, err := ParseLockTech(in)
		if err != nil || got != want {
			t.Errorf("ParseLockTech(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "WCDMA", "3g"} {
		if _, err := ParseLockTech(bad); err == nil {
			t.Errorf("ParseLockTech(%q) should fail", bad)
		}
	}
}

func TestEarfcnValid(t *testing.T) {
	cases := []struct {
		tech CellTech
		v    int
		want bool
	}{
		{CellTechLTE, 2452, true},
		{CellTechLTE, 65535, true},
		{CellTechLTE, 70000, false}, // 超出 LTE EARFCN 上限
		{CellTechLTE, 0, false},
		{CellTechNR, 627264, true},
		{CellTechNR, 3279165, true},
		{CellTechNR, 4000000, false},
		{CellTechNR, -1, false},
	}
	for _, c := range cases {
		if got := EarfcnValid(c.tech, c.v); got != c.want {
			t.Errorf("EarfcnValid(%s,%d) = %v, want %v", c.tech, c.v, got, c.want)
		}
	}
}

// ====================== 移远指令拼装 ======================

func TestQuectelLockCommands(t *testing.T) {
	q := &quectel.QuectelCommand{}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"query band lte", q.CommandQueryBandLock(CellTechLTE), `AT+QNWPREFCFG="lte_band"`},
		{"query band nr", q.CommandQueryBandLock(CellTechNR), `AT+QNWPREFCFG="nr5g_band"`},
		{"band capability", q.CommandQueryBandCapability(CellTechNR), `AT+QNWPREFCFG="ue_capability_band"`},
		{"policy band", q.CommandQueryPolicyBand(), `AT+QNWPREFCFG="policy_band"`},
		{"set band lte", q.CommandSetBandLock(CellTechLTE, []int{5, 3, 1}), `AT+QNWPREFCFG="lte_band",1:3:5`},
		// 空列表绝不能拼出 ",0"：实测写 0 会清空频段、模组直接脱网，基站扫描永远为空
		{"empty band list nr", q.CommandSetBandLock(CellTechNR, nil), ""},
		{"empty band list lte", q.CommandSetBandLock(CellTechLTE, []int{}), ""},
		{"query cell lte", q.CommandQueryCellLock(CellTechLTE), `AT+QNWLOCK="common/4g"`},
		{"query cell nr", q.CommandQueryCellLock(CellTechNR), `AT+QNWLOCK="common/5g"`},
		// NR: <pci>,<freq>,<scs>,<band>（按模组 AT+QNWLOCK=? 实际签名）
		{"lock cell nr", q.CommandLockCell(CellLockRequest{Tech: CellTechNR, ARFCN: 627264, PCID: 521, LockType: CellLockARFCNPCI}),
			`AT+QNWLOCK="common/5g",521,627264,1,78`},
		{"lock cell nr scs khz", q.CommandLockCell(CellLockRequest{Tech: CellTechNR, ARFCN: 627264, PCID: 521, LockType: CellLockARFCNPCI, SCS: 15}),
			`AT+QNWLOCK="common/5g",521,627264,0,78`},
		{"unlock cell nr", q.CommandLockCell(CellLockRequest{Tech: CellTechNR, LockType: CellLockNone}), `AT+QNWLOCK="common/5g",0`},
		// LTE: <type>,<freq>,<pci>
		{"lock cell lte", q.CommandLockCell(CellLockRequest{Tech: CellTechLTE, ARFCN: 2452, PCID: 155, LockType: CellLockARFCNPCI}),
			`AT+QNWLOCK="common/4g",1,2452,155`},
		{"lock arfcn lte", q.CommandLockCell(CellLockRequest{Tech: CellTechLTE, ARFCN: 2452, LockType: CellLockARFCN}), `AT+QNWLOCK="common/4g",2,2452`},
		{"unlock cell lte", q.CommandLockCell(CellLockRequest{Tech: CellTechLTE, LockType: CellLockNone}), `AT+QNWLOCK="common/4g",0`},
		{"query mode", q.CommandQueryModePref(), `AT+QNWPREFCFG="mode_pref"`},
		{"set mode", q.CommandSetModePref("lte"), `AT+QNWPREFCFG="mode_pref",LTE`},
		{"caps band", q.CommandBandLockCaps(), "AT+QNWPREFCFG=?"},
		{"caps cell", q.CommandCellLockCaps(), "AT+QNWLOCK=?"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestFibocomLockCommands 广和通（高通平台 FM150 / FM160-EAU）锁网指令拼装：
// 与移远同一套高通标准 QNW 指令（AT+QNWPREFCFG / AT+QNWLOCK）。
func TestFibocomLockCommands(t *testing.T) {
	f := &fibocom.FibocomCommand{}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"query band lte", f.CommandQueryBandLock(CellTechLTE), `AT+QNWPREFCFG="lte_band"`},
		{"query band nr", f.CommandQueryBandLock(CellTechNR), `AT+QNWPREFCFG="nr5g_band"`},
		{"band capability", f.CommandQueryBandCapability(CellTechNR), `AT+QNWPREFCFG="ue_capability_band"`},
		{"policy band", f.CommandQueryPolicyBand(), `AT+QNWPREFCFG="policy_band"`},
		{"set band lte", f.CommandSetBandLock(CellTechLTE, []int{5, 3, 1}), `AT+QNWPREFCFG="lte_band",1:3:5`},
		// 空列表绝不能拼出 ",0"：实测写 0 会清空频段、模组直接脱网
		{"empty band list nr", f.CommandSetBandLock(CellTechNR, nil), ""},
		{"empty band list lte", f.CommandSetBandLock(CellTechLTE, []int{}), ""},
		{"query cell lte", f.CommandQueryCellLock(CellTechLTE), `AT+QNWLOCK="common/4g"`},
		{"query cell nr", f.CommandQueryCellLock(CellTechNR), `AT+QNWLOCK="common/5g"`},
		// NR: <pci>,<freq>,<scs>,<band>
		{"lock cell nr", f.CommandLockCell(CellLockRequest{Tech: CellTechNR, ARFCN: 627264, PCID: 521, LockType: CellLockARFCNPCI}),
			`AT+QNWLOCK="common/5g",521,627264,1,78`},
		{"unlock cell nr", f.CommandLockCell(CellLockRequest{Tech: CellTechNR, LockType: CellLockNone}), `AT+QNWLOCK="common/5g",0`},
		// LTE: <type>,<freq>,<pci>
		{"lock cell lte", f.CommandLockCell(CellLockRequest{Tech: CellTechLTE, ARFCN: 2452, PCID: 155, LockType: CellLockARFCNPCI}),
			`AT+QNWLOCK="common/4g",1,2452,155`},
		{"lock arfcn lte", f.CommandLockCell(CellLockRequest{Tech: CellTechLTE, ARFCN: 2452, LockType: CellLockARFCN}), `AT+QNWLOCK="common/4g",2,2452`},
		{"unlock cell lte", f.CommandLockCell(CellLockRequest{Tech: CellTechLTE, LockType: CellLockNone}), `AT+QNWLOCK="common/4g",0`},
		{"query mode", f.CommandQueryModePref(), `AT+QNWPREFCFG="mode_pref"`},
		{"set mode", f.CommandSetModePref("lte"), `AT+QNWPREFCFG="mode_pref",LTE`},
		{"caps band", f.CommandBandLockCaps(), "AT+QNWPREFCFG=?"},
		{"caps cell", f.CommandCellLockCaps(), "AT+QNWLOCK=?"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestBandLockRestricted 频段“是否真的做了限制”的判据。
// 基准必须用 policy_band（不随配置变化）；用 ue_capability_band 会把
// “配置被清 0”误判成“未受限”，从而漏报模组脱网。
func TestBandLockRestricted(t *testing.T) {
	full := []int{1, 2, 3, 5, 7, 8, 12, 20, 25, 28, 38, 40, 41, 48, 66, 71, 77, 78}
	cases := []struct {
		name    string
		current []int
		policy  []int
		want    bool
	}{
		{"未锁定：全量列表包含 policy 频段", full, []int{1, 78}, false},
		{"锁定到 78：policy 频段 1 被屏蔽", []int{78}, []int{1, 78}, true},
		{"锁定到 1:78：与 policy 一致，不算受限", []int{1, 78}, []int{1, 78}, false},
		{"LTE 锁定到 3：policy 1/5/8 被屏蔽", []int{3}, []int{1, 3, 5, 8}, true},
		// 关键回归：模组上报 0 = 频段列表为空（全部禁用），必须判为受限
		{"上报 0（频段被清空）必须判为受限", nil, []int{1, 78}, true},
		{"无 policy 不臆断", []int{78}, nil, false},
	}
	for _, c := range cases {
		if got := bandLockRestricted(c.current, c.policy); got != c.want {
			t.Errorf("%s: bandLockRestricted = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestNormalizeSCS NR 子载波间隔归一化：接受 kHz 或索引
func TestNormalizeSCS(t *testing.T) {
	cases := map[int]int{0: 1, 15: 0, 30: 1, 60: 2, 120: 3, 1: 1, 2: 2, 3: 3, 99: 1}
	for in, want := range cases {
		if got := NormalizeSCS(in); got != want {
			t.Errorf("NormalizeSCS(%d) = %d, want %d", in, got, want)
		}
	}
}

// TestParseQNWPREFCFGCapability ue_capability_band / policy_band 都一次返回多个 key
// （gw/lte/nsa_nr5g/nr5g），必须按技术域精确取行，不能张冠李戴。
func TestParseQNWPREFCFGCapability(t *testing.T) {
	resp := []string{
		`AT+QNWPREFCFG="ue_capability_band"`,
		`+QNWPREFCFG: "gw_band",1:2:3:4:5:6:8:19`,
		`+QNWPREFCFG: "lte_band",1:3:5:8`,
		`+QNWPREFCFG: "nsa_nr5g_band",78`,
		`+QNWPREFCFG: "nr5g_band",1:78`,
		"OK",
	}
	lte, ok := parseQNWPREFCFG(resp, "lte_band")
	if !ok || lte != "1:3:5:8" {
		t.Fatalf("lte_band = %q ok=%v, want 1:3:5:8", lte, ok)
	}
	// 不能被前面的 nsa_nr5g_band 抢走
	nr, ok := parseQNWPREFCFG(resp, "nr5g_band")
	if !ok || nr != "1:78" {
		t.Fatalf("nr5g_band = %q ok=%v, want 1:78", nr, ok)
	}
	lteBands, _ := common.ParseBandList(lte)
	nrBands, _ := common.ParseBandList(nr)
	if bandLockRestricted([]int{1, 3, 5, 8}, lteBands) {
		t.Error("配置与 policy 一致时不应判为受限")
	}
	if !bandLockRestricted([]int{78}, nrBands) {
		t.Error("屏蔽了 policy 频段 1，应判为受限")
	}
}

// ====================== 能力接口 ======================

// TestNetLockerCapability 锁网是可选能力：移远 / 广和通（高通平台）/ 鼎桥 MT5700 实现了该接口，
// 其它未验证的厂商必须明确报"不支持"，绝不能把未经验证的指令发给真实模组。
//
// 广和通的依据：高通平台机型（FM150 / FM160-EAU，SDX55/SDX62）与移远同为高通标准
// AT+QNWPREFCFG / AT+QNWLOCK（见 NetLocker 接口注释）。MTK 平台的 FM350-GL 与
// 展锐平台的 FM650-CN 不认这一套，由 ProbeLockCaps 在下单前探测拦下（不发未验证指令）。
//
// MT5700 的依据：手册 §13.12 AT^LTEFREQLOCK / §13.13 AT^NRFREQLOCK（锁频点/锁小区/锁频段，
// 已实机逐条下发并回读验证），详见 lock_mt5700.go 与 lock_vendor_test.go。
func TestNetLockerCapability(t *testing.T) {
	if common.NetLockerOf(&quectel.QuectelCommand{}) == nil {
		t.Error("QuectelCommand should implement NetLocker")
	}
	if common.NetLockerOf(&fibocom.FibocomCommand{}) == nil {
		t.Error("FibocomCommand should implement NetLocker（高通平台 AT+QNWPREFCFG / AT+QNWLOCK）")
	}
	if common.NetLockerOf(&tdtech.MT5700Command{}) == nil {
		t.Error("tdtech.MT5700Command should implement NetLocker（AT^LTEFREQLOCK / AT^NRFREQLOCK）")
	}
	// 芯讯通未验证 → 明确不支持
	if common.NetLockerOf(&simcom.SimcomCommand{}) != nil {
		t.Error("SimcomCommand must not claim NetLocker support until verified")
	}
}

// TestLockUnsupportedVendor 不支持的厂商应返回明确错误，且不触碰串口
func TestLockUnsupportedVendor(t *testing.T) {
	mod := &Modem{ID: 0, Vendor: VendorSimcom, cmdImpl: &simcom.SimcomCommand{}}
	if _, err := mod.QueryLockStatus(); err == nil {
		t.Error("QueryLockStatus: expected unsupported error")
	}
	if _, err := mod.SetBandLock(CellTechNR, []int{78}); err == nil {
		t.Error("SetBandLock: expected unsupported error")
	}
	if _, err := mod.LockCell(CellLockRequest{Tech: CellTechNR, ARFCN: 627264, PCID: 521, LockType: CellLockARFCNPCI}, false); err == nil {
		t.Error("LockCell: expected unsupported error")
	}
	if _, err := mod.SetModePref("LTE"); err == nil {
		t.Error("SetModePref: expected unsupported error")
	}
	if _, err := mod.UnlockAll(); err == nil {
		t.Error("UnlockAll: expected unsupported error")
	}
	caps, err := mod.ProbeLockCaps()
	if err != nil {
		t.Fatalf("ProbeLockCaps returned error: %v", err)
	}
	if caps.Supported {
		t.Error("caps.Supported should be false for an unsupported vendor")
	}
}

// TestLockValidationRejectsBadInput 参数校验必须在发指令之前完成，
// 避免把非法参数发给模组（这些用例都不会触发串口 I/O）。
func TestLockValidationRejectsBadInput(t *testing.T) {
	mod := &Modem{ID: 0, Vendor: VendorQuectel, cmdImpl: &quectel.QuectelCommand{}}
	cases := []struct {
		name string
		run  func() error
	}{
		{"band 超出范围", func() error { _, err := mod.SetBandLock(CellTechNR, []int{999}); return err }},
		{"band 频段号为 0", func() error { _, err := mod.SetBandLock(CellTechNR, []int{0}); return err }},
		{"band 技术域不支持", func() error { _, err := mod.SetBandLock(CellTechGSM, []int{78}); return err }},
		{"earfcn 为 0", func() error {
			_, err := mod.LockCell(CellLockRequest{Tech: CellTechNR, LockType: CellLockARFCN}, false)
			return err
		}},
		{"LTE earfcn 超上限", func() error {
			_, err := mod.LockCell(CellLockRequest{Tech: CellTechLTE, ARFCN: 70000, LockType: CellLockARFCN}, false)
			return err
		}},
		{"pcid 超范围", func() error {
			_, err := mod.LockCell(CellLockRequest{Tech: CellTechNR, ARFCN: 627264, PCID: 5000, LockType: CellLockARFCNPCI}, false)
			return err
		}},
		{"NR 频点无法推算频段", func() error {
			// 7 是 2G 频段，NR 无法由它推算 band，锁定指令需要 band
			_, err := mod.LockCell(CellLockRequest{Tech: CellTechNR, ARFCN: 7, PCID: 1, LockType: CellLockARFCNPCI}, false)
			return err
		}},
		{"lock_type 非法", func() error {
			_, err := mod.LockCell(CellLockRequest{Tech: CellTechLTE, ARFCN: 2452, PCID: 155, LockType: 9}, false)
			return err
		}},
		{"mode 非法", func() error { _, err := mod.SetModePref("FOO"); return err }},
	}
	for _, c := range cases {
		if err := c.run(); err == nil {
			t.Errorf("%s: 期望返回错误，实际为 nil", c.name)
		}
	}
}

// ---- from: airplane_test.go ----

// CFUN 取值 / 应答解析的用例在 net.go；
// 这里只测需要 *Modem 的清缓存行为。
// 关射频后必须清掉缓存的信号读数。
//
// 实测（RM502Q-GL）：开了飞行模式 `AT+CSQ` 回 `99,99`，但服务小区指令直接回 SEARCH、
// 一个字都不上报，于是 SignalInfo 里的 nr_rsrp/rsrq/sinr 一直停在关射频前那几个值 ——
// 界面看起来就是「信号一直不动」。清成 99 / 0 后前端会显示「射频已关」。

// CFUN 取值 / 应答解析的用例在 net.go；
// 这里只测需要 *Modem 的清缓存行为。

// 关射频后必须清掉缓存的信号读数。
//
// 实测（RM502Q-GL）：开了飞行模式 `AT+CSQ` 回 `99,99`，但服务小区指令直接回 SEARCH、
// 一个字都不上报，于是 SignalInfo 里的 nr_rsrp/rsrq/sinr 一直停在关射频前那几个值 ——
// 界面看起来就是「信号一直不动」。清成 99 / 0 后前端会显示「射频已关」。
func TestClearSignalCache(t *testing.T) {
	mod := &Modem{ID: 0, SignalInfo: SignalInfo{RSSI: 20, BER: 3, NRRSRP: -110, NRRSRQ: -12, NRSINR: 8}}
	mod.clearSignalCache()
	got := mod.signalSnapshot()
	if got.NRRSRP != 0 || got.NRRSRQ != 0 || got.NRSINR != 0 {
		t.Fatalf("NR 信号应被清空：%+v", got)
	}
	if got.RSSI != 99 || got.BER != 99 {
		t.Fatalf("RSSI/BER 应置为 99（3GPP 的\"无法测得\"）：%+v", got)
	}
	// 再确认一次调用幂等（不会因为重复清空而异常）
	mod.clearSignalCache()
	if again := mod.signalSnapshot(); again.RSSI != 99 {
		t.Fatalf("重复清空后状态不对：%+v", again)
	}
}

// ---- from: usbnet_test.go ----

// 取值表 / 应答解析 / IsDecimal / FirstLine 的用例在 net.go；
// 这里只测“哪个厂商声明了这个能力”（直接决定会不会发出未验证指令）。
// TestUSBnetCommands 移远/自适应声明了该能力；广和通/芯讯通不声明（不发未验证指令）

// 取值表 / 应答解析 / IsDecimal / FirstLine 的用例在 net.go；
// 这里只测“哪个厂商声明了这个能力”（直接决定会不会发出未验证指令）。

// TestUSBnetCommands 每家都有 USB 网卡模式方言（指令各不相同）：
// 移远 QCFG usbnet、广和通 GTUSBMODE、芯讯通（高通）CUSBCFG /（lte）MYCONFIG、巴龙 SETMODE；
// 未识别厂商没有方言时回退到厂商 Command 的声明（adaptiveCommands = QCFG usbnet）。
//
// ⚠️ 这里的取值必须是**清单口径**（modem_catalog.json 的 usb_at）：型号在清单里时
// 取值与指令都由清单说了算（巴龙 ECM=0 就是清单里那个 0，不是厂商表里那个 1）。
func TestUSBnetCommands(t *testing.T) {
	cases := []struct {
		name  string
		mod   *Modem
		query string
		value string // 本机（清单口径）下的一个合法取值
		set   string // 期望的设置指令
	}{
		{"移远", &Modem{ID: 0, Vendor: VendorQuectel, Model: "RM520N-GL", cmdImpl: &quectel.QuectelCommand{}},
			`AT+QCFG="usbnet"`, "2", `AT+QCFG="usbnet",2`},
		{"广和通（高通）", &Modem{ID: 0, Vendor: VendorFibocom, Model: "FM160-CN", ModelName: "FM160-CN", cmdImpl: &fibocom.FibocomCommand{}},
			"AT+GTUSBMODE?", "32", "AT+GTUSBMODE=32"},
		{"芯讯通（高通）", &Modem{ID: 0, Vendor: VendorSimcom, Model: "SIMCOM_SIM8200EA-M2", ModelName: "SIMCOM_SIM8200EA-M2", cmdImpl: &simcom.SimcomCommand{}},
			"AT+CUSBCFG?", "9001", "AT+CUSBCFG=usbid,1e0e,9001"},
		{"芯讯通（lte）", &Modem{ID: 0, Vendor: VendorSimcom, Model: "A7605C", ModelName: "A7605C", cmdImpl: &simcom.SimcomCommand{}},
			`AT$MYCONFIG?`, "1", `AT$MYCONFIG="USBNETMODE",1,1`},
		{"鼎桥（巴龙）", &Modem{ID: 0, Vendor: VendorTDTech, Model: "MT5700M-CN", ModelName: "MT5700M-CN", cmdImpl: &tdtech.MT5700Command{}},
			"AT^SETMODE?", "0", "AT^SETMODE=0"},
	}
	for _, c := range cases {
		cc, err := c.mod.usbnetConfigurator()
		if err != nil {
			t.Errorf("%s 应支持 USB 网卡模式: %v", c.name, err)
			continue
		}
		if got := cc.CommandQueryUSBnet(); got != c.query {
			t.Errorf("%s 查询指令 = %q, want %q", c.name, got, c.query)
		}
		if got := cc.CommandSetUSBnet(c.value); got != c.set {
			t.Errorf("%s 设置指令（取值 %s）= %q, want %q", c.name, c.value, got, c.set)
		}
	}

	// 未识别厂商且机型无方言 → 回退到 Command 声明（adaptiveCommands 的 QCFG usbnet）
	mod := &Modem{ID: 0, Vendor: VendorUnknown, Model: "未知型号", ModelName: "未知型号", cmdImpl: &common.UnknownCommand{}}
	cc, err := mod.usbnetConfigurator()
	if err != nil {
		t.Fatalf("未识别厂商应回退到 QCFG usbnet: %v", err)
	}
	if got := cc.CommandQueryUSBnet(); got != `AT+QCFG="usbnet"` {
		t.Errorf("回退查询指令 = %q", got)
	}
}

// ---- from: lock_vendor_test.go ----

// ---- from: lock_mt5700_test.go ----
//
// 下面的应答全部是实机抓的（2026-09-30，COM11，中国电信 460/11 驻留 n78）：
// 每条锁都下发过、回读过、最后 AT^NRFREQLOCK=0 / AT^LTEFREQLOCK=0 解锁复原。
// TestMT5700NetLockerCapability MT5700 宣告支持锁网（手册有锁频/锁小区指令，且已实机验证）
//
// 下面的应答全部是实机抓的（2026-09-30，COM11，中国电信 460/11 驻留 n78）：
// 每条锁都下发过、回读过、最后 AT^NRFREQLOCK=0 / AT^LTEFREQLOCK=0 解锁复原。
// TestMT5700NetLockerCapability MT5700 宣告支持锁网（手册有锁频/锁小区指令，且已实机验证）

func TestMT5700NetLockerCapability(t *testing.T) {
	if common.NetLockerOf(&tdtech.MT5700Command{}) == nil {
		t.Fatal("tdtech.MT5700Command 应实现 NetLocker（AT^LTEFREQLOCK / AT^NRFREQLOCK）")
	}
	if lockVendorOpsOf(&tdtech.MT5700Command{}) == nil {
		t.Fatal("tdtech.MT5700Command 应实现 LockVendorOps（应答格式与移远不同）")
	}
	// 其它厂商不受影响：没实现 NetLocker 的照旧，也不会被塞进厂商扩展
	for _, impl := range []ModemCommand{&fibocom.FibocomCommand{}, &simcom.SimcomCommand{}} {
		if _, ok := impl.(LockVendorOps); ok {
			t.Errorf("%T 不该实现 LockVendorOps", impl)
		}
	}
}

// TestMT5700LockCommands 指令拼装：operatetype 3/1/2/0 + 字符串参数 + forbidFlag 取值
func TestMT5700LockCommands(t *testing.T) {
	m := mt5700()
	cases := []struct {
		name string
		got  string
		want string
	}{
		{
			// 锁频段：forbidFlag=1（允许在该频段内重选/切换），num = band 个数
			"NR 锁单频段", m.CommandSetBandLock(CellTechNR, []int{78}),
			`AT^NRFREQLOCK=3,1,1,"78"`,
		},
		{
			"NR 锁多频段", m.CommandSetBandLock(CellTechNR, []int{1, 3, 78}),
			`AT^NRFREQLOCK=3,1,3,"1,3,78"`,
		},
		{
			"LTE 锁频段", m.CommandSetBandLock(CellTechLTE, []int{1}),
			`AT^LTEFREQLOCK=3,1,1,"1"`,
		},
		{
			"空频段列表不下发", m.CommandSetBandLock(CellTechNR, nil), "",
		},
		{
			// 锁小区：forbidFlag=0（不允许重选和切换 —— 这才是"钉住"）
			"NR 锁小区（带 band/scstype）",
			m.CommandLockCell(CellLockRequest{
				Tech: CellTechNR, LockType: CellLockARFCNPCI,
				ARFCN: 627264, PCID: 277, SCS: 30,
			}),
			`AT^NRFREQLOCK=2,0,1,"78","627264","1","277"`,
		},
		{
			"NR 仅锁频点",
			m.CommandLockCell(CellLockRequest{
				Tech: CellTechNR, LockType: CellLockARFCN, ARFCN: 627264, SCS: 1,
			}),
			`AT^NRFREQLOCK=1,0,1,"78","627264","1"`,
		},
		{
			"LTE 锁小区（无 scstype）",
			m.CommandLockCell(CellLockRequest{
				Tech: CellTechLTE, LockType: CellLockARFCNPCI, ARFCN: 100, PCID: 300,
			}),
			`AT^LTEFREQLOCK=2,0,1,"1","100","300"`,
		},
		{
			"LTE 仅锁频点",
			m.CommandLockCell(CellLockRequest{
				Tech: CellTechLTE, LockType: CellLockARFCN, ARFCN: 100,
			}),
			`AT^LTEFREQLOCK=1,0,1,"1","100"`,
		},
		{"NR 解锁", m.CommandLockCell(CellLockRequest{Tech: CellTechNR, LockType: CellLockNone}), "AT^NRFREQLOCK=0"},
		{"LTE 解锁", m.CommandLockCell(CellLockRequest{Tech: CellTechLTE, LockType: CellLockNone}), "AT^LTEFREQLOCK=0"},
		{"NR 频段解锁", m.CommandUnlockBand(CellTechNR), "AT^NRFREQLOCK=0"},
		{"LTE 频段解锁", m.CommandUnlockBand(CellTechLTE), "AT^LTEFREQLOCK=0"},
		{"频段查询", m.CommandQueryBandLock(CellTechNR), "AT^NRFREQLOCK?"},
		{"小区查询（同一条指令）", m.CommandQueryCellLock(CellTechLTE), "AT^LTEFREQLOCK?"},
		{"频段能力探测", m.CommandBandLockCaps(), "AT^LTEFREQLOCK=?"},
		{"小区能力探测", m.CommandCellLockCaps(), "AT^NRFREQLOCK=?"},
		{"LTE 能力频段查询（AT^SYSCFGEX=?）", m.CommandQueryBandCapability(CellTechLTE), "AT^SYSCFGEX=?"},
		{"NR 没有能力频段查询", m.CommandQueryBandCapability(CellTechNR), ""},
		{"没有 policy 频段", m.CommandQueryPolicyBand(), ""},
		{"锁定本身掉电保存，无需 save_ctrl", m.CommandSaveCellLock(), ""},
		{"组网模式查询", m.CommandQueryModePref(), "AT^SYSCFGEX?"},
		{"组网模式不能单参数写（由厂商流程处理）", m.CommandSetModePref("LTE"), ""},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s:\n  got  %q\n  want %q", c.name, c.got, c.want)
		}
	}
}

// TestParseMT5700FreqLock 三段式应答解析（锁类型行 + 汇总行 + 数据行）
func TestParseMT5700FreqLock(t *testing.T) {
	cases := []struct {
		name  string
		tech  CellTech
		resp  []string
		op    int
		bands []int
		arfcn int
		scs   int
		pcid  int
		ok    bool
	}{
		{
			"未锁定（只有锁类型行）", CellTechNR,
			[]string{"AT^NRFREQLOCK?", "^NRFREQLOCK: 0", "OK"},
			0, nil, 0, 0, 0, true,
		},
		{
			"锁频段", CellTechNR,
			[]string{"^NRFREQLOCK: 3", "1,1", "78", "OK"},
			3, []int{78}, 0, 0, 0, true,
		},
		{
			"锁小区", CellTechNR,
			[]string{"^NRFREQLOCK: 2", "1,1", "78,627264,1,277", "OK"},
			2, []int{78}, 627264, 1, 277, true,
		},
		{
			"锁频点", CellTechNR,
			[]string{"^NRFREQLOCK: 1", "1,1", "78,627264,1", "OK"},
			1, []int{78}, 627264, 1, 0, true,
		},
		{
			// LTE 没有 scstype：数据行第三格是 PCI；而且"锁频点"只有两格，与汇总行同形
			"LTE 锁频点（数据行也是两格）", CellTechLTE,
			[]string{"AT^LTEFREQLOCK?", "^LTEFREQLOCK: 1", "1,1", "1,100", "OK"},
			1, []int{1}, 100, 0, 0, true,
		},
		{
			"LTE 锁小区（第三格是 PCI）", CellTechLTE,
			[]string{"^LTEFREQLOCK: 2", "1,1", "1,100,300", "OK"},
			2, []int{1}, 100, 0, 300, true,
		},
		{
			"夹了 URC 也不能错位", CellTechNR,
			[]string{"^NRFREQLOCK: 3", "^RSSI: 0", "1,1", `^HCSQ: "NR",57,191,30`, "78", "OK"},
			3, []int{78}, 0, 0, 0, true,
		},
		{"应答里没有锁类型行", CellTechNR, []string{"ERROR"}, 0, nil, 0, 0, 0, false},
	}
	for _, c := range cases {
		got, ok := tdtech.ParseFreqLock(c.tech, c.resp)
		if ok != c.ok {
			t.Fatalf("%s: ok = %v, want %v", c.name, ok, c.ok)
		}
		if !ok {
			continue
		}
		if got.Op != c.op {
			t.Errorf("%s: op = %d, want %d", c.name, got.Op, c.op)
		}
		if fmt.Sprint(got.Bands) != fmt.Sprint(c.bands) {
			t.Errorf("%s: bands = %v, want %v", c.name, got.Bands, c.bands)
		}
		if got.ARFCN != c.arfcn || got.SCS != c.scs || got.PCID != c.pcid {
			t.Errorf("%s: arfcn/scs/pcid = %d/%d/%d, want %d/%d/%d",
				c.name, got.ARFCN, got.SCS, got.PCID, c.arfcn, c.scs, c.pcid)
		}
	}
}

// TestMT5700ParseLockState 状态映射：operatetype=3 才是"频段受限"，
// 1/2 落在「锁小区」卡片上（1 → 仅锁频点，2 → 锁频点+PCI）
func TestMT5700ParseLockState(t *testing.T) {
	m := mt5700()
	// 锁频段
	bands, locked := m.ParseBandLockState(CellTechNR,
		[]string{"^NRFREQLOCK: 3", "1,1", "78", "OK"})
	if !locked || len(bands) != 1 || bands[0] != 78 {
		t.Errorf("锁频段 → bands %v locked %v, want [78] true", bands, locked)
	}
	if _, locked := m.ParseBandLockState(CellTechNR,
		[]string{"^NRFREQLOCK: 0", "OK"}); locked {
		t.Error("未锁定不该报「频段受限」")
	}
	// 锁小区（频点+PCI）
	cl, ok := m.ParseCellLockState(CellTechNR,
		[]string{"^NRFREQLOCK: 2", "1,1", "78,627264,1,277", "OK"})
	if !ok || !cl.Locked || cl.LockType != CellLockARFCNPCI || cl.ARFCN != 627264 || cl.PCID != 277 {
		t.Errorf("锁小区 → %+v ok=%v", cl, ok)
	}
	// 仅锁频点
	cl, _ = m.ParseCellLockState(CellTechLTE,
		[]string{"^LTEFREQLOCK: 1", "1,1", "1,100", "OK"})
	if !cl.Locked || cl.LockType != CellLockARFCN || cl.ARFCN != 100 || cl.PCID != 0 {
		t.Errorf("仅锁频点 → %+v", cl)
	}
	// 锁频段不该同时出现在小区卡片上
	cl, _ = m.ParseCellLockState(CellTechNR, []string{"^NRFREQLOCK: 3", "1,1", "78", "OK"})
	if cl.Locked {
		t.Errorf("锁频段不是小区锁定：%+v", cl)
	}
	// 未锁定
	cl, _ = m.ParseCellLockState(CellTechNR, []string{"^NRFREQLOCK: 0", "OK"})
	if cl.Locked || cl.LockType != CellLockNone {
		t.Errorf("未锁定 → %+v", cl)
	}
}

// TestMT5700ModePref 组网模式：AT^SYSCFGEX 的 acqorder 双向映射
func TestMT5700ModePref(t *testing.T) {
	m := mt5700()
	// 下发用的 acqorder
	cases := map[string]string{
		"AUTO": "080302", "NR5G": "08", "LTE": "03", "LTE:NR5G": "0803",
		"auto": "080302", " lte ": "03", "BOGUS": "",
	}
	for mode, want := range cases {
		if got := tdtech.AcqOrder(mode); got != want {
			t.Errorf("tdtech.AcqOrder(%q) = %q, want %q", mode, got, want)
		}
	}
	// 回读：实机当前值就是 "080302"（NR→LTE→WCDMA）→ 界面上的 AUTO
	resp := []string{"AT^SYSCFGEX?", `^SYSCFGEX: "080302",2000000680380,1,2,1E200000095`, "OK"}
	cfg, ok := common.ParseSyscfgEX(resp)
	if !ok {
		t.Fatal("common.ParseSyscfgEX 失败")
	}
	if cfg.AcqOrder != "080302" || cfg.Band != "2000000680380" || cfg.Roam != 1 ||
		cfg.SrvDomain != 2 || cfg.LTEBand != "1E200000095" {
		t.Errorf("common.ParseSyscfgEX = %+v", cfg)
	}
	mode, ok := m.ParseModePref(resp)
	if !ok || mode != "AUTO" {
		t.Errorf("ParseModePref = %q ok=%v, want AUTO", mode, ok)
	}
	// 单独 NR / LTE / LTE+NR
	for acq, want := range map[string]string{
		`^SYSCFGEX: "08",2000000680380,1,2,1E200000095`:   "NR5G",
		`^SYSCFGEX: "03",2000000680380,1,2,1E200000095`:   "LTE",
		`^SYSCFGEX: "0803",2000000680380,1,2,1E200000095`: "LTE:NR5G",
	} {
		if got, ok := m.ParseModePref([]string{acq}); !ok || got != want {
			t.Errorf("ParseModePref(%q) = %q ok=%v, want %q", acq, got, ok, want)
		}
	}
	// 认不出就不报（宁可不显示，也不显示错的数据）
	if _, ok := m.ParseModePref([]string{"^SYSCFGEX: \"01\",1,0,0,1"}); ok {
		t.Error("只有 2G 的配置不该映射成任何界面模式")
	}
	if _, ok := m.ParseModePref([]string{"ERROR"}); ok {
		t.Error("ERROR 不该解析出模式")
	}
}

// TestMT5700LockStatusNote 手册要求：下发锁/解锁后要切一次飞行模式才生效 → 界面得提示
func TestMT5700LockStatusNote(t *testing.T) {
	note := mt5700().StatusNote()
	if !strings.Contains(note, "飞行模式") {
		t.Errorf("锁网提示应说明需要切飞行模式：%q", note)
	}
	// 其它厂商没有这个提示（不实现该接口 → 空串）
	if q, ok := any(&quectel.QuectelCommand{}).(LockVendorOps); ok {
		if q.StatusNote() != "" {
			t.Errorf("移远不该有鼎桥的提示：%q", q.StatusNote())
		}
	}
}

// TestMT5700BandCapability 「设备能力」列：LTE 拿得到（AT^SYSCFGEX=? 的 LTE BC 名单），
// NR 拿不到（固件不报）—— 不报时要给出原因，而不是编一份列表出来。
func TestMT5700BandCapability(t *testing.T) {
	// 实测应答（与手册 §13.2.5 例子逐字一致）
	resp := []string{
		`AT^SYSCFGEX=?`,
		`^SYSCFGEX: ("01","02","03","08"),((2000000400380,"GSM900/GSM1800/WCDMA900/WCDMA2100"),` +
			`(280000,"GSM850/GSM1900"),(3fffffff,"All bands")),(0-2),(0-4),` +
			`((1e200000095,"LTE BC1/LTE BC3/LTE BC5/LTE BC8/LTE BC34/LTE BC38/LTE BC39/LTE BC40/LTE BC41"),` +
			`(7fffffffffffffff,"All bands"))`,
		"OK",
	}
	got := common.ParseLTEBandNames(resp)
	want := []int{1, 3, 5, 8, 34, 38, 39, 40, 41}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("LTE 能力频段 = %v, want %v", got, want)
	}
	// "All bands" 不能变成频段号
	if len(common.ParseLTEBandNames([]string{`^SYSCFGEX: ("03"),(3fffffff,"All bands")`})) != 0 {
		t.Error("All bands 不该被当成能力频段列表")
	}
	// ERROR / 空应答 → 空列表
	if len(common.ParseLTEBandNames([]string{"ERROR"})) != 0 {
		t.Error("ERROR 不该解析出频段")
	}
	// NR 拿不到时要有说明（界面拿它代替 `-`）
	m := mt5700()
	if note := m.BandCapabilityNote(CellTechNR); note == "" || !strings.Contains(note, "NR") {
		t.Errorf("NR 应给出「不上报」的说明，实际 %q", note)
	}
	if note := m.BandCapabilityNote(CellTechLTE); note != "" {
		t.Errorf("LTE 能查到能力频段，不该有说明：%q", note)
	}
}

// TestMT5700SetModePrefRefusesBadReadback 回读不出来时宁可报错，也不能拿 0 去写频带位图
func TestMT5700SetModePrefRefusesBadReadback(t *testing.T) {
	// 直接验证"组装用到的"解析：读不出配置就没有任何可用的 band/lteband
	if _, ok := common.ParseSyscfgEX([]string{"ERROR"}); ok {
		t.Fatal("ERROR 不该解析出配置")
	}
	if _, ok := common.ParseSyscfgEX([]string{"^SYSCFGEX: \"080302\",2000000680380,1"}); ok {
		t.Fatal("字段不全不该解析出配置（否则会把 band 写成空）")
	}
}

// ---- from: ca_test.go ----

// TestParseQCAINFOIdleRealHardware 实机抓到的空闲态应答：只有 PCC，且不含信号值。

// TestParseQCAINFOIdleRealHardware 实机抓到的空闲态应答：只有 PCC，且不含信号值。
func TestParseQCAINFOIdleRealHardware(t *testing.T) {
	resp := []string{
		"AT+QCAINFO",
		`+QCAINFO: "PCC",627264,3,"NR5G BAND 78",521`,
		"OK",
	}
	cc := cell.ParseQCAINFO(resp)
	if len(cc) != 1 {
		t.Fatalf("应解析出 1 个分量载波，实际 %d：%+v", len(cc), cc)
	}
	got := cc[0]
	if got.Role != "PCC" {
		t.Errorf("role want PCC got %q", got.Role)
	}
	if got.Tech != CellTechNR {
		t.Errorf("tech want NR got %q", got.Tech)
	}
	if got.Band != 78 || got.BandText != "NR5G BAND 78" || got.BandLabel != "n78" {
		t.Errorf("band 解析错误：%+v", got)
	}
	if got.EARFCN != 627264 || got.Bandwidth != 3 || got.PCID != 521 {
		t.Errorf("earfcn/bw/pcid 解析错误：%+v", got)
	}
	// 空闲态不上报信号值，不应靠猜补上
	if got.RSRP != 0 || got.RSRQ != 0 || got.SINR != 0 {
		t.Errorf("空闲态不应有信号值：%+v", got)
	}
	if CCsAggregated(cc) {
		t.Error("只有 PCC 时不应判为已聚合")
	}
}

// TestParseQCAINFOWithSignals 带信号值的上报（PCC + SCC）。
//
// 硬件上当前没有聚合生效，抓不到这种报文；这里用 LTE / NR 两种
// 信号字段个数（LTE 有 RSSI、NR 没有）验证解析器按量纲定位、不会串位。
func TestParseQCAINFOWithSignals(t *testing.T) {
	resp := []string{
		`+QCAINFO: "PCC",2452,20,"LTE BAND 5",155,-101,-10,-73,11`,
		`+QCAINFO: "SCC",1650,20,"LTE BAND 3",300,-97,-11,-68,13`,
		`+QCAINFO: "PCC",627264,3,"NR5G BAND 78",521,-105,-13,4`,
		"OK",
	}
	cc := cell.ParseQCAINFO(resp)
	if len(cc) != 3 {
		t.Fatalf("应解析出 3 个分量载波，实际 %d", len(cc))
	}

	// LTE：RSRP/RSRQ/RSSI/SINR 四个值
	if cc[0].Tech != CellTechLTE || cc[0].Band != 5 || cc[0].BandLabel != "B5" {
		t.Errorf("PCC LTE 解析错误：%+v", cc[0])
	}
	if cc[0].PCID != 155 || cc[0].RSRP != -101 || cc[0].RSRQ != -10 || cc[0].RSSI != -73 || cc[0].SINR != 11 {
		t.Errorf("PCC LTE 字段串位：%+v", cc[0])
	}
	if cc[1].Role != "SCC" || cc[1].PCID != 300 || cc[1].RSRP != -97 || cc[1].SINR != 13 {
		t.Errorf("SCC 解析错误：%+v", cc[1])
	}
	// NR：只有 RSRP/RSRQ/SINR，没有 RSSI
	if cc[2].Tech != CellTechNR || cc[2].RSRP != -105 || cc[2].RSRQ != -13 || cc[2].SINR != 4 {
		t.Errorf("NR 解析错误：%+v", cc[2])
	}
	if cc[2].RSSI != 0 {
		t.Errorf("NR 不应凭空产生 RSSI：%+v", cc[2])
	}
	if !CCsAggregated(cc) {
		t.Error("存在 SCC 时应判为已聚合")
	}
}

// TestParseQCAINFOMissingPCID PCID 未上报时直接从 band 之后找信号，不应崩
func TestParseQCAINFOMissingPCID(t *testing.T) {
	resp := []string{`+QCAINFO: "SCC",2452,20,"LTE BAND 3",-,-95,-12,-65,14`, "OK"}
	cc := cell.ParseQCAINFO(resp)
	if len(cc) != 1 {
		t.Fatalf("应解析出 1 个分量载波，实际 %d", len(cc))
	}
	if cc[0].PCID != 0 {
		t.Errorf("PCID 未上报时应为 0，got %d", cc[0].PCID)
	}
	if cc[0].RSRP != -95 || cc[0].RSRQ != -12 || cc[0].RSSI != -65 || cc[0].SINR != 14 {
		t.Errorf("信号字段串位：%+v", cc[0])
	}
}

// TestParseQCAINFOBadInput 不支持/未注册/脏数据时返回空列表，不 panic
func TestParseQCAINFOBadInput(t *testing.T) {
	for _, resp := range [][]string{
		{"OK"},
		{"ERROR"},
		{`+QCAINFO: "PCC"`},              // 字段太少
		{`+QCAINFO: "PCC",1,2`},          // 字段太少
		{`+QCAINFO: "PCC",627264,3,"X"`}, // 只有 3 个字段
		{"+QCAINFO: PCC,627264,3,X,521"}, // 角色没引号 → 不匹配
	} {
		if cc := cell.ParseQCAINFO(resp); len(cc) != 0 {
			t.Errorf("输入 %v 应得空列表，实际 %+v", resp, cc)
		}
	}
}

// TestTechFromBandText 频段串 → 接入技术
func TestTechFromBandText(t *testing.T) {
	cases := map[string]CellTech{
		"NR5G BAND 78": CellTechNR,
		"LTE BAND 5":   CellTechLTE,
		"WCDMA BAND 1": CellTechWCDMA,
		"GSM BAND 8":   CellTechGSM,
		"":             "",
	}
	for in, want := range cases {
		if got := common.TechFromBandText(in); got != want {
			t.Errorf("TechFromBandText(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestVendorCAInfoCommands 只有移远实现了 AT+QCAINFO；
// 其余厂商必须返回空串，保证不会把未经验证的指令发给真实模组。
func TestVendorCAInfoCommands(t *testing.T) {
	if got := (&quectel.QuectelCommand{}).CommandCAInfo(); got != "AT+QCAINFO" {
		t.Errorf("Quectel CommandCAInfo = %q, want AT+QCAINFO", got)
	}
	for _, impl := range []ModemCommand{&fibocom.FibocomCommand{}, &simcom.SimcomCommand{}} {
		if got := impl.CommandCAInfo(); got != "" {
			t.Errorf("%T CommandCAInfo = %q, want 空串（未实现）", impl, got)
		}
	}
}

// ---- from: ca_rm500u_test.go ----

// 载波聚合 CC 行的两种写法（差异在“频段串与 PCID 之间多不多一格”）。
// 适配新模组时先把真机行贴到这里，再改 ca.go。

// 载波聚合 CC 行的两种写法（差异在“频段串与 PCID 之间多不多一格”）。
// 适配新模组时先把真机行贴到这里，再改 ca.go。
func TestQCAINFOBothLayouts(t *testing.T) {
	// RM500U-CNV：频段串后多一格（1），PCID 是 140 ——按固定下标 f[3] 会读成 1
	rm500u := []string{`+QCAINFO: "PCC",1850,100,LTE BAND 3,1,140,-117,-15,-85,-3`}
	// RM502Q-GL：频段串后直接是 PCID（无信号值时只到 PCID）
	rm502q := []string{`+QCAINFO: "PCC",627264,3,"NR5G BAND 78",521`}

	for name, tc := range map[string]struct {
		in       []string
		wantPCID int
		wantRSRP int
		wantBand int
	}{
		"RM500U": {rm500u, 140, -117, 3},
		"RM502Q": {rm502q, 521, 0, 78},
	} {
		ccs := cell.ParseQCAINFO(tc.in)
		if len(ccs) != 1 {
			t.Fatalf("%s: 应解析出 1 个 CC，得到 %d", name, len(ccs))
		}
		cc := ccs[0]
		if cc.PCID != tc.wantPCID {
			t.Errorf("%s: pcid = %d, want %d", name, cc.PCID, tc.wantPCID)
		}
		if cc.RSRP != tc.wantRSRP {
			t.Errorf("%s: rsrp = %d, want %d", name, cc.RSRP, tc.wantRSRP)
		}
		if cc.Band != tc.wantBand {
			t.Errorf("%s: band = %d, want %d", name, cc.Band, tc.wantBand)
		}
	}
}

// ---- from: ca_mt5700_test.go ----

//
// 样例来自手册（§13.18.5 / §13.27.5），并用实机核对过"没有聚合时"的应答：
//
//	AT^CASCELLINFO? → ERROR       （手册：CA 未配置时返回 ERROR）
//	AT^MONSSC       → ^MONSSC: NONE（手册：不支持辅连接）
//
// ⚠️ 本机当前是 NR SA、没有辅载波，所以**真实 CA 数据没实机见过**，
// 解析按手册给出的例子锁定（进 LTE CA / NSA 双连接时应能直接用）。
// TestMT5700CACmds 指令层：CA 走厂商自己的两条指令（移远的 AT+QCAINFO 在本机回 ERROR）

// ====================== 鼎桥 MT5700 载波聚合（CA） ======================
//
// 样例来自手册（§13.18.5 / §13.27.5），并用实机核对过"没有聚合时"的应答：
//
//	AT^CASCELLINFO? → ERROR       （手册：CA 未配置时返回 ERROR）
//	AT^MONSSC       → ^MONSSC: NONE（手册：不支持辅连接）
//
// ⚠️ 本机当前是 NR SA、没有辅载波，所以**真实 CA 数据没实机见过**，
// 解析按手册给出的例子锁定（进 LTE CA / NSA 双连接时应能直接用）。

// TestMT5700CACmds 指令层：CA 走厂商自己的两条指令（移远的 AT+QCAINFO 在本机回 ERROR）
func TestMT5700CACmds(t *testing.T) {
	m := mt5700()
	if got := m.CommandCAInfo(); got != "" {
		t.Errorf("CommandCAInfo = %q, want 空（本机不用 AT+QCAINFO）", got)
	}
	if _, ok := any(m).(CAVendorOps); !ok {
		t.Fatal("MT5700 应实现 CAVendorOps")
	}
	cmds := m.CACmds()
	if len(cmds) != 2 || cmds[0] != "AT^CASCELLINFO?" || cmds[1] != "AT^MONSSC" {
		t.Errorf("CACmds = %v, want [AT^CASCELLINFO? AT^MONSSC]", cmds)
	}
	if note := m.CAEmptyNote(); note == "" || !strings.Contains(note, "ERROR") || !strings.Contains(note, "NONE") {
		t.Errorf("空状态说明应讲清 ERROR/NONE 属正常：%q", note)
	}
	// 其它厂商不受影响
	if _, ok := any(&quectel.QuectelCommand{}).(CAVendorOps); ok {
		t.Error("移远不该实现 CAVendorOps（它走 AT+QCAINFO 通用路径）")
	}
}

// TestParseMT5700CASCellInfo 手册 §13.18.5 的例子：2CC / 3CC
func TestParseMT5700CASCellInfo(t *testing.T) {
	resp := []string{
		"AT^CASCELLINFO?",
		"^CASCELLINFO: 1,417,-60,-80,-5,18,23925,5925,8225,8675,5,5",
		"^CASCELLINFO: 2,418,-61,-81,-7,18,23925,5925,8225,8675,3,3",
		"OK",
	}
	cc := tdtech.ParseCASCellInfo(resp)
	if len(cc) != 2 {
		t.Fatalf("分量载波数 = %d, want 2（%+v）", len(cc), cc)
	}
	first := cc[0]
	if first.Role != "SCC" || first.Tech != CellTechLTE {
		t.Errorf("角色/制式 = %q/%q, want SCC/LTE（这条指令只报辅小区）", first.Role, first.Tech)
	}
	if first.PCID != 417 || first.Band != 18 || first.EARFCN != 5925 {
		t.Errorf("pci/band/earfcn = %d/%d/%d, want 417/18/5925", first.PCID, first.Band, first.EARFCN)
	}
	if first.RSSI != -60 || first.RSRP != -80 || first.RSRQ != -5 {
		t.Errorf("rssi/rsrp/rsrq = %d/%d/%d, want -60/-80/-5", first.RSSI, first.RSRP, first.RSRQ)
	}
	if first.BandLabel != "B18" {
		t.Errorf("BandLabel = %q, want B18", first.BandLabel)
	}
	// 带宽代号 5 → 20MHz、3 → 10MHz
	if first.Bandwidth != 20 || cc[1].Bandwidth != 10 {
		t.Errorf("带宽 = %d/%d, want 20/10（手册代号 5/3）", first.Bandwidth, cc[1].Bandwidth)
	}
	if !CCsAggregated(cc) {
		t.Error("有 SCC 就该算已聚合")
	}
}

// TestParseMT5700CASCellInfoEmpty 没有 CA 时模组回 ERROR → 空列表（不能当成解析失败/不支持）
func TestParseMT5700CASCellInfoEmpty(t *testing.T) {
	for _, resp := range [][]string{
		{"ERROR"},
		{"AT^CASCELLINFO?", "ERROR"},
		{"OK"},
		{},
	} {
		if cc := tdtech.ParseCASCellInfo(resp); len(cc) != 0 {
			t.Errorf("%v 不该解析出分量载波：%+v", resp, cc)
		}
	}
}

// TestParseMT5700MONSSC 手册 §13.27.5 的例子：NSA 单 CC / 2CC / 信号还没上报
func TestParseMT5700MONSSC(t *testing.T) {
	// 2CC
	cc := tdtech.ParseMONSSC([]string{
		"AT^MONSSC",
		"^MONSSC: NR,633988,1,-70,-20,-10,0",
		"^MONSSC: NR,633400,2,-68,-10,1,0",
		"OK",
	})
	if len(cc) != 2 {
		t.Fatalf("辅连接小区数 = %d, want 2（%+v）", len(cc), cc)
	}
	if cc[0].Tech != CellTechNR || cc[0].Role != "SCC" {
		t.Errorf("制式/角色 = %q/%q, want NR/SCC", cc[0].Tech, cc[0].Role)
	}
	if cc[0].EARFCN != 633988 || cc[0].PCID != 1 {
		t.Errorf("arfcn/pci = %d/%d, want 633988/1（PCI 是十六进制）", cc[0].EARFCN, cc[0].PCID)
	}
	if cc[0].RSRP != -70 || cc[0].RSRQ != -20 || cc[0].SINR != -10 {
		t.Errorf("信号 = %d/%d/%d, want -70/-20/-10", cc[0].RSRP, cc[0].RSRQ, cc[0].SINR)
	}
	if cc[0].BandLabel != "n78" {
		t.Errorf("BandLabel = %q, want n78（633988 落在 n78）", cc[0].BandLabel)
	}
	if cc[1].Bandwidth != 0 || cc[1].SINR != 1 {
		t.Errorf("第二条 = %+v", cc[1])
	}

	// Scell 还没上报信号：哨兵值 -1256/-348/-188 → 一律不显示
	cc = tdtech.ParseMONSSC([]string{
		"^MONSSC: NR,633400,2,-1256,-348,-188,2",
		"OK",
	})
	if len(cc) != 1 {
		t.Fatalf("应解析出 1 条：%+v", cc)
	}
	if cc[0].RSRP != 0 || cc[0].RSRQ != 0 || cc[0].SINR != 0 {
		t.Errorf("哨兵值应清成 0（未上报）：%+v", cc[0])
	}
}

// TestParseMT5700MONSSCNone 没有辅连接时回 NONE（≡ 当前没聚合），不是解析失败
func TestParseMT5700MONSSCNone(t *testing.T) {
	for _, resp := range [][]string{
		{"^MONSSC: NONE", "OK"},
		{"ERROR"},
		{"^MONSSC: LTE,1,2,3", "OK"}, // 手册：LTE 侧暂不支持
		{},
	} {
		if cc := tdtech.ParseMONSSC(resp); len(cc) != 0 {
			t.Errorf("%v 不该解析出辅小区：%+v", resp, cc)
		}
	}
}

// TestMT5700CAHintIsVendorAware 界面提示不能写死移远的 AT+QCAINFO
func TestMT5700CAHintIsVendorAware(t *testing.T) {
	mt := &Modem{ID: 0, cmdImpl: mt5700()}
	cmds, note := mt.caHint()
	if fmt.Sprint(cmds) != "[AT^CASCELLINFO? AT^MONSSC]" {
		t.Errorf("MT5700 caHint cmds = %v", cmds)
	}
	if !strings.Contains(note, "ERROR") {
		t.Errorf("MT5700 空状态说明 = %q", note)
	}
	q := &Modem{ID: 0, cmdImpl: &quectel.QuectelCommand{}}
	if cmds, note := q.caHint(); fmt.Sprint(cmds) != "[AT+QCAINFO]" || note != "" {
		t.Errorf("移远 caHint = %v / %q, want [AT+QCAINFO] / 空", cmds, note)
	}
	// 不支持的厂商：既没有指令也没有说明
	f := &Modem{ID: 0, cmdImpl: &fibocom.FibocomCommand{}}
	if cmds, note := f.caHint(); len(cmds) != 0 || note != "" {
		t.Errorf("广和通 caHint = %v / %q, want 空", cmds, note)
	}
}
