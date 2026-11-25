package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"modemd/module/cell"
	"modemd/module/common"
	"modemd/module/fibocom"
	"modemd/module/quectel"
	simcompkg "modemd/module/simcom"
	"modemd/module/tdtech"
)

// ---- from: push_test.go ----

// ---- from: push_test.go ----
// TestPushRoundSkipsWithoutClient 没有客户端在线时整轮跳过，不碰串口。
// 这里故意放一个 isRunning=true 但没有串口的模组：只要这一轮真的去查询，
// 就会卡在 AT 收发上（或报错），所以"轮次不变"就能证明没有触发查询。
// TestPushRoundSkipsWithoutClient 没有客户端在线时整轮跳过，不碰串口。
// 这里故意放一个 isRunning=true 但没有串口的模组：只要这一轮真的去查询，
// 就会卡在 AT 收发上（或报错），所以"轮次不变"就能证明没有触发查询。

func TestPushRoundSkipsWithoutClient(t *testing.T) {
	initTestLogger()
	hub := NewHub()
	wsHubInst = hub
	defer func() { wsHubInst = nil }()

	mgr := NewModemManager()
	mgr.modems[0] = &Modem{ID: 0, isRunning: true}

	if got := pushRound(mgr, 0); got != 0 {
		t.Fatalf("无客户端时轮次不应增加，got %d", got)
	}
	if got := pushRound(mgr, 7); got != 7 {
		t.Fatalf("无客户端时应原样返回轮次，got %d", got)
	}
}

// TestPushRoundWithClient 有客户端时轮次推进（此时没有模组，不涉及串口）
func TestPushRoundWithClient(t *testing.T) {
	initTestLogger()
	hub := NewHub()
	wsHubInst = hub
	defer func() { wsHubInst = nil }()

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	hub.Add(NewConn(c1, bufio.NewReader(c1)))

	mgr := NewModemManager()
	if got := pushRound(mgr, 0); got != 1 {
		t.Fatalf("有客户端时轮次应 +1，got %d", got)
	}
	if got := pushRound(mgr, 1); got != 2 {
		t.Fatalf("有客户端时轮次应继续 +1，got %d", got)
	}
	if n := hub.Count(); n != 1 {
		t.Fatalf("客户端数 = %d, want 1", n)
	}
}

// TestPushRoundSkipsWithOnlyPassiveClient 只有屏幕（被动连接）在线时也整轮跳过。
//
// 屏幕端 7x24 挂在 /display/ws 上，**不该因为它就把模组每秒问一遍**：
// 它只读现有缓存（Conn.passive + Hub.CountActive，见 push.go）。
func TestPushRoundSkipsWithOnlyPassiveClient(t *testing.T) {
	initTestLogger()
	hub := NewHub()
	wsHubInst = hub
	defer func() { wsHubInst = nil }()

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	pc := NewConn(c1, bufio.NewReader(c1))
	pc.passive = true // 屏幕端 /display/ws 走的这条通道就是被动连接
	hub.Add(pc)

	if n := hub.Count(); n != 1 {
		t.Fatalf("Count() = %d, want 1（连接本身当然算“在线”）", n)
	}
	if n := hub.CountActive(); n != 0 {
		t.Fatalf("CountActive() = %d, want 0（被动连接不算“有人在看”）", n)
	}

	// 带一个 isRunning 但没有串口的模组：只要真去查询就会卡住/报错，
	// 所以“轮次不变”足以证明没触发 AT。
	mgr := NewModemManager()
	mgr.modems[0] = &Modem{ID: 0, isRunning: true}
	if got := pushRound(mgr, 5); got != 5 {
		t.Fatalf("只有被动客户端时轮次不应增加，got %d", got)
	}

	// 再来一个主动连接（网页端）就该恢复轮询。这里换一个不带模组的 manager ——
	// 本用例只关心“有没有主动客户端”这一个判据，别真去碰串口。
	a1, a2 := net.Pipe()
	defer a1.Close()
	defer a2.Close()
	hub.Add(NewConn(a1, bufio.NewReader(a1)))
	if n := hub.CountActive(); n != 1 {
		t.Fatalf("CountActive() = %d, want 1", n)
	}
	if got := pushRound(NewModemManager(), 5); got != 6 {
		t.Fatalf("有主动客户端时轮次应 +1，got %d", got)
	}

	// 网页端断开后，又只剩屏幕：回到“不发 AT”的状态
	hub.Drop(pc)
	hub.Add(pc) // 重新加一次，确保它在
	if n := hub.CountActive(); n != 1 {
		t.Fatalf("CountActive() = %d, want 1", n)
	}
}

// ---- from: network_test.go ----

// QNWINFO / CREG / C5GREG / 制式名 / PLMN 拆分这些**纯解析**用例在
// net.go；这里只测需要 NetworkStatus / cell 别名的部分。
// 平时（CS 或 PS 已注册）**不能**去发 AT+C5GREG?/AT+CEREG? ——
// 这是"只对看起来异常的模组兜底"的另一个关键条件。

// QNWINFO / CREG / C5GREG / 制式名 / PLMN 拆分这些**纯解析**用例在
// net.go；这里只测需要 NetworkStatus / cell 别名的部分。

// 平时（CS 或 PS 已注册）**不能**去发 AT+C5GREG?/AT+CEREG? ——
// 这是"只对看起来异常的模组兜底"的另一个关键条件。
func TestNeedRegFallback(t *testing.T) {
	cases := []struct {
		name string
		cs   RegState
		ps   RegState
		want bool
	}{
		{"PS 已注册（如 RM500U 的 +CGREG: 0,1）", RegState{Stat: 3}, RegState{Stat: 1, Registered: true}, false},
		{"CS 已注册", RegState{Stat: 1, Registered: true}, RegState{Stat: 0}, false},
		{"都未注册（5G SA 典型）", RegState{Stat: 0}, RegState{Stat: 0}, true},
		{"两个都没取到", RegState{Stat: 4}, RegState{Stat: 4}, true},
	}
	for _, c := range cases {
		st := &NetworkStatus{CSReg: c.cs, PSReg: c.ps}
		if got := needRegFallback(st); got != c.want {
			t.Errorf("%s: needRegFallback = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestBandLabelText 频段号展示与从频段串提取（实现在 cell.go 段）
func TestBandLabelText(t *testing.T) {
	if got := common.BandLabel(CellTechNR, 78); got != "n78" {
		t.Errorf("BandLabel(NR,78) = %q, want n78", got)
	}
	if got := common.BandLabel(CellTechLTE, 5); got != "B5" {
		t.Errorf("BandLabel(LTE,5) = %q, want B5", got)
	}
	if got := common.BandLabel(CellTechNR, 0); got != "" {
		t.Errorf("BandLabel(NR,0) 应为空，got %q", got)
	}
	cases := map[string]int{"NR5G BAND 78": 78, "LTE BAND 5": 5, "NR5G BAND 1": 1, "": 0}
	for in, want := range cases {
		if got := common.BandNumFromText(in); got != want {
			t.Errorf("BandNumFromText(%q) = %d, want %d", in, got, want)
		}
	}
}

// ---- from: ws_test.go ----

// wsTestKey 测试用的握手 key（base64("0123456789abcdef")）

// wsTestKey 测试用的握手 key（base64("0123456789abcdef")）
const wsTestKey = "MDEyMzQ1Njc4OWFiY2RlZg=="

// ====================== 握手 ======================

// TestWSAcceptKeyRFCExample 用 RFC 6455 的官方示例校验 accept 算法
func TestWSAcceptKeyRFCExample(t *testing.T) {
	sum := sha1.Sum([]byte("dGhlIHNhbXBsZSBub25jZQ==" + GUID))
	if got := base64.StdEncoding.EncodeToString(sum[:]); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("accept = %q, want s3pPLMBiTxaQ9kYGzzhZRbK+xOo=", got)
	}
}

// TestWSRejectsPlainRequest 非 WebSocket 请求应被拒绝（426）
func TestWSRejectsPlainRequest(t *testing.T) {
	initTestLogger()
	hub := NewHub()
	srv := httptest.NewServer(apiWS(NewModemManager(), hub))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/ws")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusUpgradeRequired)
	}
}

// TestWSConnCloseIdempotent close 必须幂等：
// 广播写失败与读循环结束会先后调用 close()，第二次若再关 done 会 panic 并带崩进程。
func TestWSConnCloseIdempotent(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c2.Close()
	conn := NewConn(c1, bufio.NewReader(c1))
	conn.Close()
	conn.Close() // 重复关闭不应 panic
	select {
	case <-conn.Done():
	default:
		t.Error("close 后 done 应已关闭")
	}
}

// ====================== 帧编解码 ======================

// TestWSFrameRoundTrip 服务端写 → 对端读（服务端帧不带掩码）
func TestWSFrameRoundTrip(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	server := NewConn(c1, bufio.NewReader(c1))
	client := NewConn(c2, bufio.NewReader(c2))

	go func() { _ = server.WriteJSON(WSMessage{Type: "signal", Data: map[string]any{"rssi": 21}}) }()

	op, payload, err := client.ReadFrame()
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if op != OpText {
		t.Fatalf("opcode = %#x, want text(%#x)", op, OpText)
	}
	var got WSMessage
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "signal" {
		t.Fatalf("type = %q, want signal", got.Type)
	}
}

// TestWSReadMaskedClientFrame 客户端帧必带掩码，服务端要能正确解掩码
func TestWSReadMaskedClientFrame(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	server := NewConn(c1, bufio.NewReader(c1))

	go func() {
		payload := []byte(`{"action":"snapshot"}`)
		mask := []byte{0x11, 0x22, 0x33, 0x44}
		frame := []byte{0x80 | OpText, 0x80 | byte(len(payload))}
		frame = append(frame, mask...)
		for i, b := range payload {
			frame = append(frame, b^mask[i%4])
		}
		_, _ = c2.Write(frame)
	}()

	op, payload, err := server.ReadFrame()
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if op != OpText {
		t.Fatalf("opcode = %#x", op)
	}
	if string(payload) != `{"action":"snapshot"}` {
		t.Fatalf("payload = %q", string(payload))
	}
}

// TestWSWriteFrameLengths 覆盖 7 位 / 16 位 / 64 位长度前缀三个分支
func TestWSWriteFrameLengths(t *testing.T) {
	for _, n := range []int{5, 200, 70000} {
		c1, c2 := net.Pipe()
		server := NewConn(c1, bufio.NewReader(c1))
		client := NewConn(c2, bufio.NewReader(c2))
		go func() { _ = server.WriteFrame(OpText, make([]byte, n)) }()
		op, payload, err := client.ReadFrame()
		if err != nil {
			t.Fatalf("len=%d readFrame: %v", n, err)
		}
		if op != OpText || len(payload) != n {
			t.Fatalf("len=%d got op=%#x len=%d", n, op, len(payload))
		}
		_ = c1.Close()
		_ = c2.Close()
	}
}

// ====================== 端到端推送 ======================

// wsDial 完成一次真实的 WebSocket 握手，返回连接、读缓冲与响应头
func wsDial(t *testing.T, srv *httptest.Server, path, key string) (net.Conn, *bufio.Reader, string) {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	req := "GET " + path + " HTTP/1.1\r\nHost: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	br := bufio.NewReader(conn)
	head := ""
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read handshake: %v", err)
		}
		head += line
		if line == "\r\n" {
			break
		}
	}
	return conn, br, head
}

// TestWSHandshakeAndBroadcast 端到端：握手 → 连接注册 → 广播 → 客户端收到
func TestWSHandshakeAndBroadcast(t *testing.T) {
	initTestLogger()
	hub := NewHub()
	mgr := NewModemManager()
	wsHubInst = hub
	defer func() { wsHubInst = nil }()

	srv := httptest.NewServer(apiWS(mgr, hub))
	defer srv.Close()

	conn, br, head := wsDial(t, srv, "/ws", wsTestKey)
	defer conn.Close()

	if !strings.Contains(head, "101") {
		t.Fatalf("handshake status: %q", head)
	}
	sum := sha1.Sum([]byte(wsTestKey + GUID))
	wantAccept := base64.StdEncoding.EncodeToString(sum[:])
	if !strings.Contains(head, "Sec-WebSocket-Accept: "+wantAccept) {
		t.Fatalf("accept 头不对: %q", head)
	}

	deadline := time.Now().Add(2 * time.Second)
	for hub.Count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hub.Count() != 1 {
		t.Fatalf("hub.Count() = %d, want 1", hub.Count())
	}

	// 连上后会先收到一份快照（modems），测试消息排在它后面
	id := 7
	broadcast("signal", &id, map[string]any{"nr_rsrp": -95})
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))

	client := NewConn(conn, br)
	var last WSMessage
	for i := 0; i < 8; i++ {
		op, payload, err := client.ReadFrame()
		if err != nil {
			t.Fatalf("readFrame: %v", err)
		}
		if op == OpPing || op == OpPong {
			continue
		}
		if err := json.Unmarshal(payload, &last); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if last.Type == "signal" {
			break
		}
	}
	if last.Type != "signal" {
		t.Fatalf("没收到 signal 推送，最后一条是 %q", last.Type)
	}
	if last.ModemID == nil || *last.ModemID != 7 {
		t.Fatalf("modem_id = %v, want 7", last.ModemID)
	}
	if last.At == 0 {
		t.Fatalf("推送时间戳为空")
	}
}

// TestWSBroadcastWithoutClient 没有客户端时广播不 panic、不阻塞
func TestWSBroadcastWithoutClient(t *testing.T) {
	hub := NewHub()
	wsHubInst = hub
	defer func() { wsHubInst = nil }()
	if n := hub.Count(); n != 0 {
		t.Fatalf("count = %d", n)
	}
	id := 0
	broadcast("signal", &id, SignalInfo{RSSI: 20})
	hub.Broadcast(WSMessage{Type: "cells"})
}

// TestPushDebounced 去抖：短时间多次调用只执行最后一次
func TestPushDebounced(t *testing.T) {
	done := make(chan struct{}, 8)
	for i := 0; i < 5; i++ {
		pushDebounced("test-key", 50*time.Millisecond, func() { done <- struct{}{} })
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("去抖回调没有执行")
	}
	time.Sleep(120 * time.Millisecond)
	if len(done) != 0 {
		t.Fatalf("该窗口内 5 次调用应只推 1 次，实际多了 %d 次", len(done))
	}
}

// ---- from: modem_signalhistory_test.go ----

// 有效性判定：与前端 renderSignal、波动记录共用同一套规则。
// ⚠️ 这几条要用 SignalInfo（引擎类型），所以在 kernel；纯存储的用例在 history.go。

// 有效性判定：与前端 renderSignal、波动记录共用同一套规则。
// ⚠️ 这几条要用 SignalInfo（引擎类型），所以在 kernel；纯存储的用例在 history.go。
func TestValidSignal(t *testing.T) {
	// 正常一组
	rsrp, rsrq, sinr, rssi := validSignal(SignalInfo{NRRSRP: -100, NRRSRQ: -12, NRSINR: 5, RSSI: 60})
	for name, p := range map[string]*int{"rsrp": rsrp, "rsrq": rsrq, "sinr": sinr, "rssi": rssi} {
		if p == nil {
			t.Errorf("%s 应为有效值", name)
		}
	}
	if *rssi != 60*2-113 {
		t.Errorf("RSSI 应换算成 dBm: %d", *rssi)
	}
	// 哨兵值/超范围 → nil
	rsrp, rsrq, sinr, rssi = validSignal(SignalInfo{NRRSRP: 0, NRRSRQ: 0, NRSINR: -3276, RSSI: 99})
	if rsrp != nil {
		t.Error("RSRP=0 应视为未上报")
	}
	if rsrq != nil {
		t.Error("RSRQ=0 是哨兵值，应视为未上报")
	}
	if sinr != nil {
		t.Error("SINR=-3276 超范围，应视为未上报")
	}
	if rssi != nil {
		t.Error("RSSI=99 应视为未上报")
	}
	// SINR=0 是合法值（解调门限附近），不能当哨兵丢掉
	if _, _, sinr, _ = validSignal(SignalInfo{NRSINR: 0}); sinr == nil || *sinr != 0 {
		t.Error("SINR=0 是合法值")
	}
	// 采样点转换：未上报的字段应为 nil，前端据此断开曲线
	s := sampleFromSignalInfo(SignalInfo{NRRSRP: -101})
	if s.RSRP == nil || *s.RSRP != -101 {
		t.Errorf("RSRP 采样错误: %+v", s.RSRP)
	}
	if s.RSRQ != nil || s.RSSI != nil {
		t.Errorf("RSRQ/RSSI 的哨兵值(0) 应为 nil: %+v", s)
	}
	// 但 SINR 的 0 是**合法读数**（解调门限附近），不能当未上报丢掉
	if s.SINR == nil || *s.SINR != 0 {
		t.Errorf("SINR=0 应保留为合法值: %+v", s.SINR)
	}
	if s.At.IsZero() {
		t.Error("采样点必须带时间")
	}
}

// 曲线与时间线必须用同一次查询：noteSignal 同时写两处
func TestNoteSignalFeedsHistoryAndChanges(t *testing.T) {
	initTestLogger()
	resetChangeLog()
	Global.Clear(-1)
	mod := &Modem{ID: 77}
	noteSignal(mod, SignalInfo{NRRSRP: -99, NRSINR: 6})
	if got := Global.CountFor(77); got != 1 {
		t.Errorf("采样历史段数 = %d, want 1", got)
	}
	if ChangeGlobal.Count() != 0 {
		t.Error("首次观测只建基线，不应有波动记录")
	}
	// 第二次：值变了 → 曲线多一点、时间线多一条
	noteSignal(mod, SignalInfo{NRRSRP: -108, NRSINR: 6})
	if got := Global.CountFor(77); got != 2 {
		t.Errorf("采样历史段数 = %d, want 2", got)
	}
	if ChangeGlobal.CountFor(77) != 1 {
		t.Errorf("波动记录条数 = %d, want 1", ChangeGlobal.CountFor(77))
	}
	Global.Clear(-1)
	resetChangeLog()
}

// ---- from: modem_pdp_test.go ----

// TestPDPAddrCandidate 地址查询走 3GPP 标准指令（各厂商通用）。
// ⚠️ 这条属于"引擎层"（要 *Modem + 运行期候选探测），所以留在 kernel；
// 纯解析的用例在 data.go。

// TestPDPAddrCandidate 地址查询走 3GPP 标准指令（各厂商通用）。
// ⚠️ 这条属于"引擎层"（要 *Modem + 运行期候选探测），所以留在 kernel；
// 纯解析的用例在 data.go。
func TestPDPAddrCandidate(t *testing.T) {
	for _, impl := range []ModemCommand{&quectel.QuectelCommand{}, &fibocom.FibocomCommand{}, &simcompkg.SimcomCommand{}, &tdtech.MT5700Command{}, &common.UnknownCommand{}} {
		mod := &Modem{ID: 0, cmdImpl: impl}
		got := mod.vendorCandidates(KindPDPAddr)
		if len(got) != 1 || got[0] != "AT+CGPADDR" {
			t.Errorf("%T 的 PDP 地址候选 = %v", impl, got)
		}
	}
}

// ---- from: changelog_test.go ----

// 测试用的两个模组号

const (
	chgTestModemA = 0
	chgTestModemB = 1
)

// resetChangeLog 清空全局记录器与基线，保证用例之间互不影响
func resetChangeLog() { ChangeGlobal.Clear(-1) }

// resetThrottle 把某字段的"上一条记录时间"清掉，绕过 MinInterval 节流。
// 真实场景下节流是必要的（信号几秒抖 5 dB）；单元测试要连续制造变化时用它。
func resetThrottle(modemID int, group, field string) {
	ChangeGlobal.Unthrottle(modemID, group, field)
}

// 信号几秒内抖 5 dB 是常态：节流必须把连成串的记录压成一条
func TestChangeThrottle(t *testing.T) {
	resetChangeLog()
	const id = 89
	obs := func(v int) *ChangeEntry {
		return ChangeGlobal.ObserveNumeric(id, GroupSig, "rsrp", "信号强度 RSRP", v, " dBm", SigThreshold, RsrpGrade)
	}
	obs(-100) // 基线
	// 一秒一条地来回跳（实测波形）：首条之后 30 秒内全被压掉
	for _, v := range []int{-105, -100, -105, -100, -105} {
		if e := obs(v); e != nil && ChangeGlobal.Count() > 1 {
			t.Fatalf("节流窗口内不应再记录: %+v", e)
		}
	}
	if got := ChangeGlobal.Count(); got > 1 {
		t.Fatalf("节流后条数 = %d, want <= 1", got)
	}
	// 被压掉的采样仍然要更新"当前值"
	cur := ChangeGlobal.Current(id)
	if len(cur) != 1 || cur[0].Value != "-105 dBm（偏弱）" {
		t.Fatalf("当前值应为最后一次采样值，实际 %+v", cur)
	}
	// 窗口过去后（把上一条记录时间往前拨）再变就能记
	ChangeGlobal.AgeField(id, GroupSig, "rsrp", 0, MinInterval)
	if e := obs(-100); e == nil {
		t.Error("节流窗口过后应能记录")
	}
}

func TestChangeObserveBaselineAndChange(t *testing.T) {
	resetChangeLog()
	const id = 90
	// 首次观测只建立基线，不算"变化"（没有"之前"）
	if e := ChangeGlobal.Observe(id, "网络", "earfcn", "频点", "627264"); e != nil {
		t.Fatalf("首次观测应只建基线，得到 %+v", e)
	}
	// 值没变 → 不记
	if e := ChangeGlobal.Observe(id, "网络", "earfcn", "频点", "627264"); e != nil {
		t.Fatalf("值未变不应记录，得到 %+v", e)
	}
	// 值变了 → 一条记录，From/To 齐全
	e := ChangeGlobal.Observe(id, "网络", "earfcn", "频点", "627188")
	if e == nil {
		t.Fatal("值变化应记录一条")
	}
	if e.From != "627264" || e.To != "627188" {
		t.Errorf("From/To = %q → %q, want 627264 → 627188", e.From, e.To)
	}
	if e.Group != "网络" || e.Label != "频点" || e.ModemID != id {
		t.Errorf("分组/展示名/模组 不对: %+v", e)
	}
	if ChangeGlobal.Count() != 1 {
		t.Errorf("记录条数 = %d, want 1", ChangeGlobal.Count())
	}
}

// 空值（模组未上报 / 查询失败）不参与记录，否则会刷出 "x → 空" 这种噪声
func TestChangeObserveIgnoresEmpty(t *testing.T) {
	resetChangeLog()
	ChangeGlobal.Observe(91, "网络", "plmn", "PLMN", "46011")
	if e := ChangeGlobal.Observe(91, "网络", "plmn", "PLMN", ""); e != nil {
		t.Fatalf("空值不应记录: %+v", e)
	}
	// 需要"空"也算状态时，调用方会先归一化成"未上报"（见 networkChanges）
	e := ChangeGlobal.Observe(91, "网络", "ipv6", "IPv6", NotReported(""))
	if e != nil {
		t.Fatalf("首次出现\"未上报\"仍只建基线: %+v", e)
	}
	e = ChangeGlobal.Observe(91, "网络", "ipv6", "IPv6", "240e:400:618:c4b::1")
	if e == nil || e.From != "未上报" {
		t.Fatalf("IPv6 从无到有应记录: %+v", e)
	}
}

// 文本字段不该有变化量/方向：频段从 n78 变 B5 时若照数值字段相减会出现"▼ 0"
func TestChangeTextFieldHasNoDelta(t *testing.T) {
	resetChangeLog()
	const id = 88
	ChangeGlobal.Observe(id, GroupNet, "band", "频段", "n78（NR5G BAND 78）")
	e := ChangeGlobal.Observe(id, GroupNet, "band", "频段", "B5（LTE BAND 5）")
	if e == nil {
		t.Fatal("频段变化应记录")
	}
	if e.Delta != "" {
		t.Errorf("文本字段 Delta 应为空，实际 %q", e.Delta)
	}
	if e.Up {
		t.Error("文本字段 Up 应为 false（方向无意义）")
	}
	if e.From == "" || e.To == "" {
		t.Errorf("From/To 必须齐全: %+v", e)
	}
}

func TestChangeObserveNumericThreshold(t *testing.T) {
	resetChangeLog()
	const id = 92
	obs := func(v int) *ChangeEntry {
		return ChangeGlobal.ObserveNumeric(id, "信号", "rsrp", "信号强度 RSRP", v, " dBm", SigThreshold, RsrpGrade)
	}
	// -95 与 -97 同为"一般"（等级边界在 -90 / -100），只差 2 dB → 属于抖动
	if e := obs(-95); e != nil {
		t.Fatalf("首次观测应只建基线: %+v", e)
	}
	if e := obs(-97); e != nil {
		t.Fatalf("阈值内的小波动不应记录: %+v", e)
	}
	// -97 → -100：仍同为"一般"，但差了 3 dB（= 阈值）→ 记一条。
	// 注意基准是**上一条记录**（-95），不是上一次采样：
	// 这样时间线里"之前 → 之后"才是累积变化（-95 → -100 共 5 dB），
	// 而不是把抖动过程一段段碎碎地切开来。
	e := obs(-100)
	if e == nil {
		t.Fatal("达到阈值应记录")
	}
	if e.From != "-95 dBm（一般）" || e.To != "-100 dBm（一般）" {
		t.Errorf("From/To = %q → %q", e.From, e.To)
	}
	if e.Delta != "-5 dBm" {
		t.Errorf("Delta = %q, want -5 dBm（自上次记录累计）", e.Delta)
	}
	if e.Up {
		t.Error("信号变差时 Up 应为 false")
	}
}

// 跨等级即使只差 1 dB 也要记：-101（偏弱）→ -100（一般）意义不同
func TestChangeObserveNumericGradeCrossing(t *testing.T) {
	resetChangeLog()
	const id = 93
	obs := func(v int) *ChangeEntry {
		return ChangeGlobal.ObserveNumeric(id, "信号", "rsrp", "信号强度 RSRP", v, " dBm", SigThreshold, RsrpGrade)
	}
	obs(-101)
	e := obs(-100)
	if e == nil {
		t.Fatal("跨等级应记录")
	}
	if e.To != "-100 dBm（一般）" || e.From != "-101 dBm（偏弱）" {
		t.Errorf("From/To = %q → %q", e.From, e.To)
	}
	if !e.Up {
		t.Error("信号变好时 Up 应为 true")
	}
	if e.Delta != "+1 dBm" {
		t.Errorf("Delta = %q, want +1 dBm", e.Delta)
	}
}

// 环形缓冲：超出上限后只保留最新的 GlobalMax 条
func TestChangeRingBuffer(t *testing.T) {
	resetChangeLog()
	const id = 94
	for i := 0; i < GlobalMax+120; i++ {
		ChangeGlobal.Observe(id, "网络", "earfcn", "频点", "频点"+strconv.Itoa(i))
		resetThrottle(id, "网络", "earfcn")
	}
	if got := ChangeGlobal.Count(); got != GlobalMax {
		t.Fatalf("环形缓冲条数 = %d, want %d", got, GlobalMax)
	}
	list := ChangeGlobal.Snapshot(-1, 0)
	if len(list) != GlobalMax {
		t.Fatalf("snapshot 条数 = %d, want %d", len(list), GlobalMax)
	}
	// 空出 1 条余量再写：确认不会越滚越多
	resetThrottle(id, "网络", "earfcn")
	ChangeGlobal.Observe(id, "网络", "earfcn", "频点", "z")
	if got := ChangeGlobal.Count(); got != GlobalMax {
		t.Fatalf("再次写入后条数 = %d, want %d", got, GlobalMax)
	}
}

func TestChangeSnapshotFilterAndLimit(t *testing.T) {
	resetChangeLog()
	// 模组 0 两条、模组 1 一条
	ChangeGlobal.Observe(chgTestModemA, "网络", "band", "频段", "n78") // 基线
	ChangeGlobal.Observe(chgTestModemA, "网络", "band", "频段", "n1")  // 记 1
	resetThrottle(chgTestModemA, "网络", "band")
	ChangeGlobal.Observe(chgTestModemA, "网络", "band", "频段", "n78") // 记 2
	ChangeGlobal.Observe(chgTestModemB, "网络", "band", "频段", "B3")  // 基线
	ChangeGlobal.Observe(chgTestModemB, "网络", "band", "频段", "B5")  // 记 3

	all := ChangeGlobal.Snapshot(-1, 0)
	if len(all) != 3 {
		t.Fatalf("全部记录 = %d, want 3", len(all))
	}
	// 最新在前
	if all[0].To != "B5" || all[2].To != "n1" {
		t.Errorf("顺序应为最新在前: %q … %q", all[0].To, all[2].To)
	}
	only0 := ChangeGlobal.Snapshot(chgTestModemA, 0)
	if len(only0) != 2 {
		t.Fatalf("模组 0 记录 = %d, want 2", len(only0))
	}
	for _, e := range only0 {
		if e.ModemID != chgTestModemA {
			t.Errorf("筛选后混入了模组 %d", e.ModemID)
		}
	}
	if got := len(ChangeGlobal.Snapshot(-1, 2)); got != 2 {
		t.Errorf("limit=2 得到 %d 条", got)
	}
	if got := len(ChangeGlobal.Snapshot(chgTestModemA, 2)); got != 2 {
		t.Errorf("模组 0 且 limit=2 得到 %d 条", got)
	}
	if got := len(ChangeGlobal.Snapshot(999, 0)); got != 0 {
		t.Errorf("不存在的模组应返回 0 条，得到 %d", got)
	}
}

// 清空要连基线一起清：否则清完立刻又冒出一条"变化"
func TestChangeClear(t *testing.T) {
	resetChangeLog()
	ChangeGlobal.Observe(chgTestModemA, "网络", "band", "频段", "n78") // 基线
	ChangeGlobal.Observe(chgTestModemA, "网络", "band", "频段", "n1")  // 记 1
	resetThrottle(chgTestModemA, "网络", "band")
	ChangeGlobal.Observe(chgTestModemA, "网络", "band", "频段", "n78") // 记 2
	ChangeGlobal.Observe(chgTestModemB, "网络", "band", "频段", "B3")  // 基线
	ChangeGlobal.Observe(chgTestModemB, "网络", "band", "频段", "B5")  // 记 3
	if got := ChangeGlobal.Count(); got != 3 {
		t.Fatalf("准备数据失败，条数 = %d, want 3", got)
	}
	if removed := ChangeGlobal.Clear(chgTestModemA); removed != 2 {
		t.Errorf("清空模组 0 应删除 2 条，实际 %d", removed)
	}
	if got := len(ChangeGlobal.Snapshot(-1, 0)); got != 1 {
		t.Errorf("清空后剩余 %d 条, want 1（模组 1 的）", got)
	}
	// 基线已清 → 再喂同一个值只是重建基线
	if e := ChangeGlobal.Observe(chgTestModemA, "网络", "band", "频段", "n78"); e != nil {
		t.Errorf("清空后应重新建立基线: %+v", e)
	}
	if removed := ChangeGlobal.Clear(-1); removed != 1 {
		t.Errorf("清空全部应删除 1 条，实际 %d", removed)
	}
	if ChangeGlobal.Count() != 0 {
		t.Error("清空全部后应为 0 条")
	}
}

func TestChangeCurrentValues(t *testing.T) {
	resetChangeLog()
	ChangeGlobal.Observe(chgTestModemA, "网络", "band", "频段", "n78")
	ChangeGlobal.Observe(chgTestModemA, "网络", "band", "频段", "n1")
	ChangeGlobal.Observe(chgTestModemB, "网络", "band", "频段", "B3")
	cur := ChangeGlobal.Current(chgTestModemA)
	if len(cur) != 1 {
		t.Fatalf("模组 0 当前值 = %d 项, want 1", len(cur))
	}
	if cur[0].Value != "n1" || cur[0].Label != "频段" {
		t.Errorf("当前值 = %+v", cur[0])
	}
	if got := len(ChangeGlobal.Current(-1)); got != 2 {
		t.Errorf("全部当前值 = %d 项, want 2", got)
	}
	if got := len(ChangeGlobal.Current(999)); got != 0 {
		t.Errorf("不存在的模组当前值应为空，得到 %d", got)
	}
}

// 模组偶尔回无效哨兵值（实测 SINR=-3276）：超出物理范围的一律当"未上报"，不记录
func TestSignalChangesSkipInvalidSentinel(t *testing.T) {
	resetChangeLog()
	mod := &Modem{ID: 95}
	// 第一次正常采样：只建基线
	if got := signalChanges(mod, SignalInfo{NRRSRP: -100, NRSINR: -3276, NRRSRQ: 0, RSSI: 99}); len(got) != 0 {
		t.Fatalf("首次采样应只建基线: %+v", got)
	}
	// 哨兵值不应被当成一次变化记录下来
	if got := signalChanges(mod, SignalInfo{NRRSRP: -100, NRSINR: -3276, NRRSRQ: 0, RSSI: 99}); len(got) != 0 {
		t.Fatalf("哨兵值不应参与记录: %+v", got)
	}
	// RSRP 掉了 5 dB → 一条记录
	got := signalChanges(mod, SignalInfo{NRRSRP: -105, NRSINR: -3276, NRRSRQ: 0, RSSI: 99})
	if len(got) != 1 {
		t.Fatalf("应记录 1 条，实际 %d: %+v", len(got), got)
	}
	if got[0].Field != "rsrp" || got[0].Group != GroupSig || got[0].To != "-105 dBm（偏弱）" {
		t.Errorf("记录内容不对: %+v", got[0])
	}
	// CSQ 的 0/99 是"不可用"，不换算成 dBm
	if got := signalChanges(mod, SignalInfo{RSSI: 0}); len(got) != 0 {
		t.Errorf("RSSI 0 不应记录: %+v", got)
	}
}

// 网络状态：制式/频段/频点/CA/PLMN/运营商/注册/APN/IP/服务小区/邻区 都要能记到
func TestNetworkChangesFields(t *testing.T) {
	resetChangeLog()
	mod := &Modem{ID: 96}
	base := &NetworkStatus{
		ModemID: 96, NetTech: "TDD NR5G", BandText: "NR5G BAND 78", BandLabel: "n78",
		EARFCN: 627264, PLMN: "46011", MCC: "460", MNC: "11", Operator: "中国电信",
		CSReg: RegState{Text: "未注册，未在搜索"}, PSReg: RegState{Text: "已注册，本地网"},
		APN: "3GNET", IPv4: "10.149.102.75", IPv6: "240e:400:618:c4b::1", ModePref: "AUTO",
		CA: []CCInfo{{Role: "PCC", BandLabel: "n78", BandText: "NR5G BAND 78", EARFCN: 627264}},
	}
	if got := networkChanges(mod, base); len(got) != 0 {
		t.Fatalf("首次观测应只建基线，实际 %d 条: %+v", len(got), got)
	}
	// 换频段 + 换频点 + CA 出现辅载波 + 掉一次 IPv6
	next := &NetworkStatus{
		ModemID: 96, NetTech: "TDD NR5G", BandText: "NR5G BAND 1", BandLabel: "n1",
		EARFCN: 428000, PLMN: "46011", MCC: "460", MNC: "11", Operator: "中国电信",
		CSReg: base.CSReg, PSReg: base.PSReg,
		APN: "3GNET", IPv4: "10.149.102.76", ModePref: "AUTO",
		CA: []CCInfo{
			{Role: "PCC", BandLabel: "n1", EARFCN: 428000},
			{Role: "SCC", BandLabel: "n78", EARFCN: 627264},
		},
	}
	got := networkChanges(mod, next)
	fields := map[string]string{}
	for _, e := range got {
		fields[e.Field] = e.From + " → " + e.To
	}
	want := []string{"band", "earfcn", "ca", "ipv4", "ipv6"}
	for _, f := range want {
		if _, ok := fields[f]; !ok {
			t.Errorf("应记录字段 %s，实际记录: %v", f, fields)
		}
	}
	// 没变的字段不该出现（运营商/PLMN/APN/组网模式/注册状态）
	for _, f := range []string{"operator", "plmn", "apn", "mode_pref", "cs_reg", "ps_reg", "tech"} {
		if _, ok := fields[f]; ok {
			t.Errorf("字段 %s 未变化，不应记录（%s）", f, fields[f])
		}
	}
	if fields["ca"] == "" {
		t.Error("CA 汇总不应为空")
	}
	if fields["ipv6"] != "240e:400:618:c4b::1 → 未上报" {
		t.Errorf("IPv6 消失应记为\"未上报\": %q", fields["ipv6"])
	}
}

func TestCASummary(t *testing.T) {
	if got := caSummary(nil, false); got != "" {
		t.Errorf("无 CA 信息应返回空串，得到 %q", got)
	}
	pcc := []CCInfo{{Role: "PCC", BandLabel: "n78"}}
	if got := caSummary(pcc, false); got != "PCC n78 · 未聚合" {
		t.Errorf("单载波汇总 = %q", got)
	}
	two := []CCInfo{{Role: "PCC", BandLabel: "n1"}, {Role: "SCC", BandLabel: "n78"}}
	if got := caSummary(two, true); got != "PCC n1 + SCC n78 · 2CC（已聚合）" {
		t.Errorf("双载波汇总 = %q", got)
	}
	// 没有 BandLabel 时退回 BandText / 频点
	raw := []CCInfo{{Role: "PCC", BandText: "LTE BAND 3"}, {Role: "SCC", EARFCN: 1650}}
	if got := caSummary(raw, true); got != "PCC LTE BAND 3 + SCC 1650 · 2CC（已聚合）" {
		t.Errorf("兜底汇总 = %q", got)
	}
	// 角色为空时显示 CC
	if got := caSummary([]CCInfo{{BandLabel: "B5"}}, false); got != "CC B5 · 未聚合" {
		t.Errorf("角色缺失汇总 = %q", got)
	}
}

func TestCellSummary(t *testing.T) {
	if got := cellSummary(nil); got != "" {
		t.Errorf("空小区应返回空串，得到 %q", got)
	}
	c := &cell.CellInfo{Tech: CellTechNR, PCID: 521, Band: 78, EARFCN: 627264, CellID: "1234ABC"}
	got := cellSummary(c)
	for _, want := range []string{"PCI 521", "n78", "频点 627264", "Cell 1234ABC"} {
		if !strings.Contains(got, want) {
			t.Errorf("摘要 %q 缺少 %q", got, want)
		}
	}
}

// 基站扫描与网络状态分工：扫描负责服务小区/邻区，网络状态负责实时类字段
func TestCellScanChangesNoDuplicate(t *testing.T) {
	resetChangeLog()
	mod := &Modem{ID: 97}
	res := &cell.CellScanResult{
		ModemID:      97,
		Serving:      []cell.CellInfo{{Tech: CellTechNR, PCID: 521, Band: 78, EARFCN: 627264, Serving: true}},
		NeighbourCnt: 4,
		CA:           []CCInfo{{Role: "PCC", BandLabel: "n78"}},
	}
	// 第一次：只建基线
	if got := cellScanChanges(mod, res); len(got) != 0 {
		t.Fatalf("首次扫描应只建基线: %+v", got)
	}
	// 换小区 → 服务小区一条，邻区数不变不记
	next := &cell.CellScanResult{
		ModemID:      97,
		Serving:      []cell.CellInfo{{Tech: CellTechNR, PCID: 522, Band: 78, EARFCN: 627264, Serving: true}},
		NeighbourCnt: 4,
		CA:           res.CA,
	}
	got := cellScanChanges(mod, next)
	if len(got) != 1 || got[0].Field != "serving" {
		t.Fatalf("换小区应记录 1 条 serving，实际 %+v", got)
	}
	// 邻区数也变了 → 再记一条
	next.NeighbourCnt = 6
	if got := cellScanChanges(mod, next); len(got) != 1 || got[0].Field != "neighbour" {
		t.Fatalf("邻区数变化应记录 1 条，实际 %+v", got)
	}
	// 网络状态里读到同样的服务小区/邻区数 → 不应再记（它不负责这两个字段）
	st := &NetworkStatus{
		ModemID: 97, Serving: &next.Serving[0], NeighbourCnt: 6, CA: res.CA,
	}
	for _, e := range networkChanges(mod, st) {
		if e.Field == "serving" || e.Field == "neighbour" {
			t.Errorf("服务小区/邻区应由扫描钩子记录，实际重复: %+v", e)
		}
	}
}

// 等级映射（阈值必须与前端 renderSignal 一致，否则"跨等级"两边理解不同）
func TestSignalGrades(t *testing.T) {
	if RsrpGrade(-80) != "极好" || RsrpGrade(-100) != "一般" || RsrpGrade(-121) != "无信号" {
		t.Errorf("RSRP 等级: %s/%s/%s", RsrpGrade(-80), RsrpGrade(-100), RsrpGrade(-121))
	}
	if SinrGrade(21) != "优秀" || SinrGrade(7) != "一般" || SinrGrade(-4) != "无数据" {
		t.Errorf("SINR 等级: %s/%s/%s", SinrGrade(21), SinrGrade(7), SinrGrade(-4))
	}
	if RsrqGrade(-9) != "优秀" || RsrqGrade(-18) != "一般" || RsrqGrade(-30) != "很差" {
		t.Errorf("RSRQ 等级: %s/%s/%s", RsrqGrade(-9), RsrqGrade(-18), RsrqGrade(-30))
	}
	// 物理范围校验（模组哨兵值）
	if IntInRange(-3276, SINRLo, SINRHi) {
		t.Error("SINR=-3276 应判为范围外")
	}
	if !IntInRange(-100, RSRPLo, RSRPHi) {
		t.Error("RSRP=-100 应在范围内")
	}
}

func TestFmtHeld(t *testing.T) {
	cases := map[int]string{0: "", 5: "5s", 60: "1m0s", 125: "2m5s", 3600: "1h0m"}
	for sec, want := range cases {
		if got := fmtHeld(sec); got != want {
			t.Errorf("fmtHeld(%d) = %q, want %q", sec, got, want)
		}
	}
}

// HeldS：记下"旧值维持了多久"（距上一条记录；首条以开始观测为基准）
func TestChangeEntryHeldSeconds(t *testing.T) {
	resetChangeLog()
	const id = 98
	ChangeGlobal.Observe(id, "网络", "band", "频段", "n78")
	// 手动把基线时间往前拨 90 秒，模拟"已经观测了 90 秒"
	ChangeGlobal.AgeField(id, "网络", "band", 90*time.Second, 0)
	e := ChangeGlobal.Observe(id, "网络", "band", "频段", "n1")
	if e == nil {
		t.Fatal("值变化应记录")
	}
	if e.HeldS < 90 || e.HeldS > 95 {
		t.Errorf("HeldS = %d, want ≈90", e.HeldS)
	}
	// 第二条：基准变成上一条记录的时间
	ChangeGlobal.AgeField(id, "网络", "band", 0, 40*time.Second)
	e = ChangeGlobal.Observe(id, "网络", "band", "频段", "n78")
	if e == nil {
		t.Fatal("值变化应记录")
	}
	if e.From != "n1" || e.To != "n78" {
		t.Errorf("From/To = %q → %q", e.From, e.To)
	}
	if e.HeldS < 40 || e.HeldS > 45 {
		t.Errorf("HeldS = %d, want ≈40", e.HeldS)
	}
}

// ---- from: schedule_test.go ----

func TestParseHHMM(t *testing.T) {
	good := []struct {
		in   string
		h, m int
	}{{"08:00", 8, 0}, {"8:5", 8, 5}, {" 23:59 ", 23, 59}, {"00:00", 0, 0}}
	for _, c := range good {
		h, m, err := ParseHHMM(c.in)
		if err != nil || h != c.h || m != c.m {
			t.Errorf("ParseHHMM(%q) = %d,%d,%v，期望 %d,%d", c.in, h, m, err, c.h, c.m)
		}
	}
	bad := []string{"", "8", "24:00", "08:60", "-1:00", "08:00:00", "aa:bb"}
	for _, c := range bad {
		if _, _, err := ParseHHMM(c); err == nil {
			t.Errorf("ParseHHMM(%q) 应当报错", c)
		}
	}
}

// 校验：合法任务 + 时间与频段被归一化；多个动作按提交顺序保留
func TestValidateSchedTaskOK(t *testing.T) {
	task := &Task{
		ID: "t1", ModemKey: "port:COM32",
		StartTime: "8:5",
		StartActions: []Action{
			{Kind: KindBandLock, Tech: "nr", Bands: "78:41:41"},
			{Kind: KindCellLock, Tech: "nr", LockType: CellLockARFCNPCI, ARFCN: 627264, PCID: 521},
		},
		EndTime: "22:00",
		EndActions: []Action{
			{Kind: KindCellUnlock, Tech: "NR"},
			{Kind: KindBandUnlock, Tech: "NR"},
		},
		Days: []int{5, 1, 1, 3},
	}
	if err := ValidateTask(task); err != nil {
		t.Fatalf("合法任务被拒：%v", err)
	}
	if task.StartTime != "08:05" {
		t.Errorf("开始时间应归一化为 08:05，实际 %q", task.StartTime)
	}
	if len(task.StartActions) != 2 {
		t.Fatalf("应保留 2 个开始动作，实际 %d", len(task.StartActions))
	}
	if task.StartActions[0].Tech != "NR" {
		t.Errorf("制式应归一化为 NR，实际 %q", task.StartActions[0].Tech)
	}
	if task.StartActions[0].Bands != "41:78" {
		t.Errorf("频段应去重+排序为 41:78，实际 %q", task.StartActions[0].Bands)
	}
	if task.StartActions[1].Kind != KindCellLock || task.StartActions[1].PCID != 521 {
		t.Errorf("第二个动作（锁基站）应原样保留：%+v", task.StartActions[1])
	}
	if len(task.EndActions) != 2 || task.EndActions[0].Kind != KindCellUnlock {
		t.Errorf("结束动作顺序应保留：%+v", task.EndActions)
	}
	if len(task.Days) != 3 || task.Days[0] != 1 || task.Days[1] != 3 || task.Days[2] != 5 {
		t.Errorf("星期应去重+排序为 [1 3 5]，实际 %v", task.Days)
	}
	if task.Name == "" {
		t.Error("没填名字时应自动生成一个")
	}
}

// 多个动作：顺序保留、完全重复的去重、个数上限
func TestValidateSchedTaskMultiActions(t *testing.T) {
	task := &Task{
		ID: "t1", ModemKey: "port:COM32", StartTime: "08:00",
		StartActions: []Action{
			{Kind: KindBandLock, Tech: "NR", Bands: "78"},
			{Kind: KindBandLock, Tech: "NR", Bands: "78"}, // 重复 → 丢掉
			{Kind: KindModePref, Mode: "LTE"},
		},
	}
	if err := ValidateTask(task); err != nil {
		t.Fatalf("合法任务被拒：%v", err)
	}
	if len(task.StartActions) != 2 {
		t.Fatalf("重复动作应被去掉，期望 2 个，实际 %d：%+v", len(task.StartActions), task.StartActions)
	}
	if task.StartActions[1].Kind != KindModePref {
		t.Errorf("去重后应保持原顺序：%+v", task.StartActions)
	}
	if got := ActionsLabel(task.StartActions); got != "锁频段 NR 78 + 组网模式 → 仅 4G（LTE）" {
		t.Errorf("多动作文案不对：%q", got)
	}

	// 空动作列表
	if err := ValidateTask(&Task{ID: "t2", ModemKey: "port:COM1", StartTime: "08:00"}); err == nil ||
		!strings.Contains(err.Error(), "至少要有一个") {
		t.Errorf("没有开始动作应报错，实际 %v", err)
	}
	// 超过上限
	many := make([]Action, 0, MaxActions+1)
	for i := 0; i <= MaxActions; i++ {
		many = append(many, Action{Kind: KindBandLock, Tech: "NR", Bands: strconv.Itoa(i + 1)})
	}
	if err := ValidateTask(&Task{ID: "t3", ModemKey: "port:COM1", StartTime: "08:00", StartActions: many}); err == nil ||
		!strings.Contains(err.Error(), "最多") {
		t.Errorf("超过动作数上限应报错，实际 %v", err)
	}
	// 错误信息要指出是第几个动作（方便定位）
	err := ValidateTask(&Task{ID: "t4", ModemKey: "port:COM1", StartTime: "08:00", StartActions: []Action{
		{Kind: KindBandLock, Tech: "NR", Bands: "78"},
		{Kind: KindBandLock, Tech: "NR", Bands: ""},
	}})
	if err == nil || !strings.Contains(err.Error(), "开始动作 2") {
		t.Errorf("应指出第 2 个动作有问题，实际 %v", err)
	}
}

func TestValidateSchedTaskRejects(t *testing.T) {
	base := func() *Task {
		return &Task{
			ID: "t1", ModemKey: "port:COM32", StartTime: "08:00",
			StartActions: []Action{{Kind: KindBandLock, Tech: "NR", Bands: "78"}},
		}
	}
	cases := []struct {
		name   string
		mutate func(*Task)
		want   string
	}{
		{"没目标模组", func(t *Task) { t.ModemKey = "" }, "目标模组"},
		{"开始时间非法", func(t *Task) { t.StartTime = "25:00" }, "开始时间"},
		{"未知动作", func(t *Task) { t.StartActions[0].Kind = "reboot" }, "未知动作"},
		{"锁频段缺频段", func(t *Task) { t.StartActions[0].Bands = "" }, "频段"},
		{"锁频段频段非法", func(t *Task) { t.StartActions[0].Bands = "0" }, "频段"},
		{"锁频段缺制式", func(t *Task) { t.StartActions[0].Tech = "" }, "制式"},
		{"锁基站缺频点", func(t *Task) {
			t.StartActions[0] = Action{Kind: KindCellLock, Tech: "LTE", LockType: CellLockARFCNPCI, ARFCN: 0, PCID: 1}
		}, "频点"},
		{"锁基站PCI越界", func(t *Task) {
			t.StartActions[0] = Action{Kind: KindCellLock, Tech: "LTE", LockType: CellLockARFCNPCI, ARFCN: 2452, PCID: 9999}
		}, "PCI"},
		{"锁基站缺锁定方式", func(t *Task) {
			t.StartActions[0] = Action{Kind: KindCellLock, Tech: "LTE", ARFCN: 2452}
		}, "锁定方式"},
		{"组网模式非法", func(t *Task) {
			t.StartActions[0] = Action{Kind: KindModePref, Mode: "5G"}
		}, "组网模式"},
		{"结束时间与开始相同", func(t *Task) {
			t.EndTime = "08:00"
			t.EndActions = []Action{{Kind: KindUnlockAll}}
		}, "不能与开始时间相同"},
		{"有结束时间没结束动作", func(t *Task) { t.EndTime = "20:00" }, "结束动作"},
		{"结束动作非法", func(t *Task) {
			t.EndTime = "20:00"
			t.EndActions = []Action{{Kind: KindBandLock, Tech: "NR", Bands: ""}}
		}, "结束动作"},
		{"星期越界", func(t *Task) { t.Days = []int{7} }, "星期"},
	}
	for _, c := range cases {
		task := base()
		c.mutate(task)
		err := ValidateTask(task)
		if err == nil {
			t.Errorf("%s：应当报错", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：错误信息应包含 %q，实际 %q", c.name, c.want, err.Error())
		}
	}
}

// 只填了结束动作但没填结束时间 → 结束动作被丢掉（避免"看着会执行其实不会"）
func TestValidateSchedTaskDropsOrphanEndAction(t *testing.T) {
	task := &Task{
		ID: "t1", ModemKey: "port:COM32", StartTime: "08:00",
		StartActions: []Action{{Kind: KindUnlockAll}},
		EndActions:   []Action{{Kind: KindAirplaneOn}},
	}
	if err := ValidateTask(task); err != nil {
		t.Fatal(err)
	}
	if len(task.EndActions) != 0 {
		t.Errorf("没有结束时间时不应保留结束动作：%+v", task.EndActions)
	}
}

// 旧格式（单动作 start_action / end_action）要能读进新模型
func TestSchedTaskLegacySingleActionJSON(t *testing.T) {
	body := `{"id":"t1","modem_key":"port:COM32","enabled":true,"start_time":"08:00",
	  "start_action":{"kind":"band_lock","tech":"NR","bands":"78"},
	  "end_time":"20:00","end_action":{"kind":"band_unlock","tech":"NR"}}`
	var task Task
	if err := json.Unmarshal([]byte(body), &task); err != nil {
		t.Fatal(err)
	}
	if len(task.StartActions) != 1 || task.StartActions[0].Kind != KindBandLock {
		t.Fatalf("旧格式 start_action 应转成一个动作：%+v", task.StartActions)
	}
	if len(task.EndActions) != 1 || task.EndActions[0].Kind != KindBandUnlock {
		t.Fatalf("旧格式 end_action 应转成一个动作：%+v", task.EndActions)
	}
	if err := ValidateTask(&task); err != nil {
		t.Fatalf("旧格式任务应校验通过：%v", err)
	}
	// 新格式里已经有列表时不应被旧字段覆盖
	both := `{"id":"t2","modem_key":"port:COM1","start_time":"08:00",
	  "start_actions":[{"kind":"unlock_all"}],"start_action":{"kind":"airplane_on"}}`
	var t2 Task
	if err := json.Unmarshal([]byte(both), &t2); err != nil {
		t.Fatal(err)
	}
	if len(t2.StartActions) != 1 || t2.StartActions[0].Kind != KindUnlockAll {
		t.Errorf("有 start_actions 时忽略旧字段：%+v", t2.StartActions)
	}
}

// ====================== 到点判定 ======================

func schedTestTask(start, end string, days ...int) *Task {
	t := &Task{
		ID: "t1", Name: "n", Enabled: true, ModemKey: "port:COM32",
		StartTime: start, StartActions: []Action{{Kind: KindUnlockAll}},
		Days: days,
	}
	if end != "" {
		t.EndTime = end
		t.EndActions = []Action{{Kind: KindAirplaneOff}}
	}
	return t
}

func TestSchedDuePhases(t *testing.T) {
	// 2026-09-30 是周三（Weekday=3）
	at := func(hh, mm int) time.Time {
		return time.Date(2026, 9, 30, hh, mm, 0, 0, time.Local)
	}
	task := schedTestTask("08:00", "20:00")

	if got := task.DuePhases(at(7, 59)); len(got) != 0 {
		t.Errorf("还没到点不应触发：%v", got)
	}
	if got := task.DuePhases(at(8, 0)); len(got) != 1 || got[0] != PhaseStart {
		t.Errorf("8:00 应触发 start：%v", got)
	}
	// 补做窗口内（10 分钟后）仍可触发
	if got := task.DuePhases(at(8, 10)); len(got) != 1 || got[0] != PhaseStart {
		t.Errorf("窗口内应补做：%v", got)
	}
	// 超过窗口不补做（避免开机补跑一整天）
	if got := task.DuePhases(at(9, 0)); len(got) != 0 {
		t.Errorf("超过补做窗口不应触发：%v", got)
	}
	// 20:00 触发 end，且两个槽位各自独立
	if got := task.DuePhases(at(20, 1)); len(got) != 1 || got[0] != PhaseEnd {
		t.Errorf("20:01 应触发 end：%v", got)
	}
}

func TestSchedDuePhasesDedup(t *testing.T) {
	now := time.Date(2026, 9, 30, 8, 0, 30, 0, time.Local)
	task := schedTestTask("08:00", "")
	task.Slots = map[string]string{PhaseStart: "2026-09-30 08:00"}
	if got := task.DuePhases(now); len(got) != 0 {
		t.Errorf("已执行过的槽位不应重复触发：%v", got)
	}
	// 第二天同一时间应再触发（槽位变了）
	tomorrow := now.AddDate(0, 0, 1)
	if got := task.DuePhases(tomorrow); len(got) != 1 {
		t.Errorf("第二天应再次触发：%v", got)
	}
}

func TestSchedDuePhasesWeekday(t *testing.T) {
	// 只在周一（1）执行；2026-09-30 是周三 → 不触发，2026-10-05 是周一 → 触发
	task := schedTestTask("08:00", "", 1)
	wed := time.Date(2026, 9, 30, 8, 1, 0, 0, time.Local)
	mon := time.Date(2026, 10, 5, 8, 1, 0, 0, time.Local)
	if got := task.DuePhases(wed); len(got) != 0 {
		t.Errorf("非指定星期不应触发：%v", got)
	}
	if got := task.DuePhases(mon); len(got) != 1 {
		t.Errorf("指定星期应触发：%v", got)
	}
}

// 关闭的任务永不触发
func TestSchedDuePhasesDisabled(t *testing.T) {
	task := schedTestTask("08:00", "")
	task.Enabled = false
	if got := task.DuePhases(time.Date(2026, 9, 30, 8, 1, 0, 0, time.Local)); len(got) != 0 {
		t.Errorf("停用的任务不应触发：%v", got)
	}
}

// 跨天时段：23:00 开始、07:00 结束，两次各自在当天触发
func TestSchedOvernight(t *testing.T) {
	task := schedTestTask("23:00", "07:00")
	if got := task.DuePhases(time.Date(2026, 9, 30, 23, 5, 0, 0, time.Local)); len(got) != 1 || got[0] != PhaseStart {
		t.Errorf("23:05 应触发 start：%v", got)
	}
	if got := task.DuePhases(time.Date(2026, 10, 1, 7, 5, 0, 0, time.Local)); len(got) != 1 || got[0] != PhaseEnd {
		t.Errorf("次日 07:05 应触发 end：%v", got)
	}
}

func TestSchedNextRuns(t *testing.T) {
	now := time.Date(2026, 9, 30, 9, 0, 0, 0, time.Local)
	task := schedTestTask("08:00", "20:00")
	next := task.NextRuns(now, 4)
	if len(next) != 4 {
		t.Fatalf("应给出 4 个时间点，实际 %d：%v", len(next), next)
	}
	// 从 9/30 09:00 看：今天 20:00、明天 08:00、明天 20:00、后天 08:00
	want := []string{"09-30 20:00", "10-01 08:00", "10-01 20:00", "10-02 08:00"}
	for i, w := range want {
		if got := next[i].Format("01-02 15:04"); got != w {
			t.Errorf("第 %d 个下次执行 = %s，期望 %s", i+1, got, w)
		}
	}
	// 停用后不返回
	task.Enabled = false
	if next := task.NextRuns(now, 3); len(next) != 0 {
		t.Errorf("停用的任务不应给出下次执行：%v", next)
	}
}

// ====================== 动作描述与支持性 ======================

func TestSchedActionText(t *testing.T) {
	cases := []struct {
		a    Action
		want string
	}{
		{Action{Kind: KindBandLock, Tech: "NR", Bands: "78"}, "锁频段 NR 78"},
		{Action{Kind: KindBandUnlock, Tech: "LTE"}, "解除锁频段 LTE"},
		{Action{Kind: KindCellLock, Tech: "LTE", ARFCN: 2452, PCID: 155, LockType: CellLockARFCNPCI}, "锁基站 LTE 2452/PCI 155"},
		{Action{Kind: KindCellLock, Tech: "NR", ARFCN: 627264, LockType: CellLockARFCN}, "锁基站 NR 频点 627264"},
		{Action{Kind: KindCellUnlock, Tech: "NR"}, "解除锁基站 NR"},
		{Action{Kind: KindUnlockAll}, "解除全部锁定"},
		{Action{Kind: KindModePref, Mode: "LTE"}, "组网模式 → 仅 4G（LTE）"},
		{Action{Kind: KindAirplaneOn}, "开飞行模式"},
		{Action{Kind: KindAirplaneOff}, "关飞行模式"},
	}
	for _, c := range cases {
		if got := ActionText(c.a); got != c.want {
			t.Errorf("ActionText(%+v) = %q，期望 %q", c.a, got, c.want)
		}
	}
}

// 不支持锁网的厂商（芯讯通）：锁频段/锁基站要在执行前被拦下并给出明确原因
func TestSchedActionSupportedVendor(t *testing.T) {
	simcom := &Modem{ID: 0, Vendor: VendorSimcom, cmdImpl: &simcompkg.SimcomCommand{}}
	if err := schedActionSupported(simcom, KindBandLock); err == nil {
		t.Error("芯讯通不支持锁网，锁频段应被拦下")
	} else if !strings.Contains(err.Error(), "不支持锁网指令") {
		t.Errorf("错误信息应说明不支持锁网，实际 %q", err.Error())
	}
	// 飞行模式是 3GPP 标准指令，任何厂商都能排
	if err := schedActionSupported(simcom, KindAirplaneOn); err != nil {
		t.Errorf("飞行模式不应被拦：%v", err)
	}
	// 未知类型
	if err := schedActionSupported(simcom, "nope"); err == nil {
		t.Error("未知动作类型应报错")
	}
	// 移远 / 广和通（高通平台）都支持锁网
	for _, c := range []struct {
		name string
		mod  *Modem
	}{
		{"移远", &Modem{ID: 0, Vendor: VendorQuectel, cmdImpl: &quectel.QuectelCommand{}}},
		{"广和通", &Modem{ID: 0, Vendor: VendorFibocom, cmdImpl: &fibocom.FibocomCommand{}}},
	} {
		if err := schedActionSupported(c.mod, KindBandLock); err != nil {
			t.Errorf("%s 应支持锁频段：%v", c.name, err)
		}
	}
}

// 执行动作时厂商不支持 → 直接返回错误，不去发未验证的指令
func TestApplySchedActionUnsupported(t *testing.T) {
	mod := &Modem{ID: 0, Vendor: VendorSimcom, cmdImpl: &simcompkg.SimcomCommand{}}
	err := mod.ApplySchedAction(Action{Kind: KindBandLock, Tech: "NR", Bands: "78"})
	if err == nil {
		t.Fatal("芯讯通锁频段应报错")
	}
	if !strings.Contains(err.Error(), "不支持") {
		t.Errorf("错误信息应说明不支持，实际 %q", err.Error())
	}
	if err := mod.ApplySchedAction(Action{Kind: "nope"}); err == nil {
		t.Error("未知动作类型应报错")
	}
	// 参数错误（制式非法）也要报错而不是静默
	if err := mod.ApplySchedAction(Action{Kind: KindBandLock, Tech: "XYZ", Bands: "78"}); err == nil {
		t.Error("非法制式应报错")
	}
}

// 多动作执行：逐个下发；全部失败时结果要写清**每个**动作的原因
func TestRunSchedPhaseMultiActionAggregate(t *testing.T) {
	mod := &Modem{ID: 0, Vendor: VendorSimcom, cmdImpl: &simcompkg.SimcomCommand{}}
	task := &Task{
		ID: "t1", Name: "n", Enabled: true, ModemKey: "port:COM1",
		StartTime: "08:00",
		StartActions: []Action{
			{Kind: KindBandLock, Tech: "NR", Bands: "78"},
			{Kind: KindCellLock, Tech: "NR", LockType: CellLockARFCNPCI, ARFCN: 627264, PCID: 521},
		},
	}
	run := runSchedPhase(mod, task, PhaseStart)
	if run.OK {
		t.Fatalf("芯讯通两个动作都该失败：%q", run.Msg)
	}
	for _, want := range []string{"锁频段 NR 78", "锁基站 NR 627264/PCI 521"} {
		if !strings.Contains(run.Msg, want) {
			t.Errorf("结果里应写明「%s」的原因，实际 %q", want, run.Msg)
		}
	}
	if strings.Contains(run.Msg, "已执行") {
		t.Errorf("一个动作都没成功时不该说「已执行」，实际 %q", run.Msg)
	}
	if run.Phase != PhaseStart || run.Slot == "" {
		t.Errorf("执行记录应带 phase/slot：%+v", run)
	}
}

// ====================== 持久化 ======================
// 存储往返 / 坏文件跳过 / 任务上限 / 日志节流的用例在 schedule.go。

// ====================== 视图 ======================

// 没有模组管理器时也应给出动作类型与上限（页面先渲染出来）
func TestScheduleViewNoManager(t *testing.T) {
	v := scheduleView(nil)
	if len(v.Tasks) != 0 {
		t.Errorf("没有模组时任务列表应为空，实际 %v", v.Tasks)
	}
	if len(v.Kinds) == 0 {
		t.Error("应返回可选动作类型")
	}
	if v.MaxTasks <= 0 {
		t.Error("应返回任务数上限")
	}
}

// 视图里动作列表要带文案与支持性（组装逻辑在 schedule.go，这里验证 kernel 侧的接线）
func TestSchedActionViewsCore(t *testing.T) {
	mod := &Modem{ID: 0, Vendor: VendorSimcom, cmdImpl: &simcompkg.SimcomCommand{}}
	actions := []Action{{Kind: KindBandLock, Tech: "NR", Bands: "78"}}
	views, first := schedActionViews(mod, actions)
	if len(views) != 1 || views[0].Text == "" {
		t.Fatalf("动作视图不对：%+v", views)
	}
	if first == "" || views[0].Unsupported == "" {
		t.Errorf("固件不支持锁网时应报出来：%+v first=%q", views[0], first)
	}
	// 目标不在线（mod=nil）时不判定支持性
	views, first = schedActionViews(nil, actions)
	if first != "" || views[0].Unsupported != "" {
		t.Errorf("没有目标模组时不该报不支持：%+v", views)
	}
}
