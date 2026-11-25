package kernel

// 「跟随 LuCI 登录」中间件的单测。
//
// 为什么这些用例重要：这道闸门一旦判断错方向，两种后果都不小 ——
//   · 判宽了：没登录的人能直接调 /api/at、复位模组；
//   · 判严了：用户被挡在自己的设备中心外面。
// 所以判定逻辑（会话解析、白名单、失败处理、缓存）都单独锁一遍。

import (
	"encoding/json"
	"errors"
	"modemd/module/config"
	"modemd/module/luciauth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testSID 形状合法的假会话 ID（32 位十六进制，跟 LuCI 真发的一样长）
const testSID = "0123456789abcdef0123456789abcdef"

// withLuciAuth 把闸门打开，并把「这台机器有 ubus」与「会话校验结果」换成可控的假实现，
// 这样在 Windows 上也能测到闸门本身。返回会话校验的调用次数。
func withLuciAuth(t *testing.T, valid func(sid string) bool) *int {
	t.Helper()
	oldReq := config.Cfg.UI.RequireLuciLogin
	oldLook, oldPath := luciauth.LuciUbusLookPath, luciauth.LuciUBusPath
	oldCheck := luciauth.LuciSessionCheck
	config.Cfg.UI.RequireLuciLogin = true
	luciauth.LuciUbusLookPath = func() string { return "/bin/ubus" }
	luciauth.LuciUBusOnce.Do(func() {}) // 先消耗掉 Once，下面直接写缓存值
	luciauth.LuciUBusPath = "/bin/ubus"
	calls := 0
	luciauth.LuciSessionCheck = func(sid string) (bool, error) { calls++; return valid(sid), nil }
	luciauth.ResetLuciSessionCache()
	luciauth.ResetLoginLimiter()
	t.Cleanup(func() {
		config.Cfg.UI.RequireLuciLogin = oldReq
		luciauth.LuciUbusLookPath, luciauth.LuciUBusPath = oldLook, oldPath
		luciauth.LuciSessionCheck = oldCheck
		luciauth.ResetLuciSessionCache()
		luciauth.ResetLoginLimiter()
	})
	return &calls
}

// TestLuciSessionGetOK 解析 `ubus call session get` 的应答。
// 关键区分：**空的 values = 这个会话没了**（要拒绝）；**解析不了 = 字段名变了**（应报告服务不可用）。
func TestLuciSessionGetOK(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name  string
		out   string
		want  bool
		known bool
	}{
		{"登录中的会话", `{"values":{"username":"root","uid":0},"acls":{}}`, true, true},
		{"没有这个会话（rpcd 对未知 sid 回空 values）", `{"values":{},"acls":{}}`, false, true},
		{"空对象", `{}`, false, false},
		{"带 access:false 但没 username", `{"values":{},"acls":{},"access":false}`, false, true},
		{"username 为空串", `{"values":{"username":"  "}}`, false, true},
		{"已过期", `{"values":{"username":"root"},"expires":1699999999}`, false, true},
		{"还没过期", `{"values":{"username":"root"},"expires":1700000001}`, true, true},
		{"不是 JSON", `ubus: not found`, false, false},
	}
	for _, c := range cases {
		got, known := luciauth.LuciSessionGetOK([]byte(c.out), now)
		if got != c.want || known != c.known {
			t.Errorf("%s: luciSessionGetOK(%s) = (%v,%v)，想要 (%v,%v)",
				c.name, c.out, got, known, c.want, c.known)
		}
	}
}

func TestLuciSessionIDLooksValid(t *testing.T) {
	ok := []string{testSID, strings.Repeat("a", 16), strings.Repeat("0", 128), "abcdef0123456789-_"}
	bad := []string{"", "short", strings.Repeat("a", 129), `abcdef0123456789"`, "abcdef0123456789;rm -rf /", "abcdef 0123456789"}
	for _, sid := range ok {
		if !luciauth.LuciSessionIDLooksValid(sid) {
			t.Errorf("%q 应该算合法会话 ID", sid)
		}
	}
	for _, sid := range bad {
		if luciauth.LuciSessionIDLooksValid(sid) {
			t.Errorf("%q 不该算合法会话 ID", sid)
		}
	}
}

// TestLuciAuthAllowPath：静态页与 /api/version 放行；所有 /api/* 与 /ws 要会话。
// 放行静态页是必须的 —— 打不开页面就没地方提示"请先登录路由器"。
func TestLuciAuthAllowPath(t *testing.T) {
	allow := []string{"/", "/index.html", "/mini", "/layouts/standard.css", "/api/version",
		"/api/login", "/api/logout", "/api/session"}
	deny := []string{"/api/modems", "/api/at", "/api/version/x", "/ws", "/display/ws", "/api/sms/read", "/api/login/x"}
	for _, p := range allow {
		if !luciauth.LuciAuthAllowPath(p) {
			t.Errorf("%s 应放行", p)
		}
	}
	for _, p := range deny {
		if luciauth.LuciAuthAllowPath(p) {
			t.Errorf("%s 应要求会话", p)
		}
	}
}

// TestLuciLoginURL 登录跳转地址要去掉端口（LuCI 在 80/8080，不是设备中心的端口）
func TestLuciLoginURL(t *testing.T) {
	cases := []struct{ host, want string }{
		{"192.168.77.1:8899", "http://192.168.77.1/cgi-bin/luci/"},
		{"192.168.77.1", "http://192.168.77.1/cgi-bin/luci/"},
		{"openwrt.lan:9527", "http://openwrt.lan/cgi-bin/luci/"},
		{"", "/cgi-bin/luci/"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/api/modems", nil)
		r.Host = c.host
		if got := luciauth.LuciLoginURL(r); got != c.want {
			t.Errorf("Host=%q: luciLoginURL = %q，想要 %q", c.host, got, c.want)
		}
	}
}

// gateHandler 是个只回 200 的内层处理器：中间件放行与否看状态码就够了。
func gateHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("inner"))
	})
}

func doGate(t *testing.T, path, cookieName, cookieVal, remote string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "https://example.test"+path, nil)
	if remote != "" {
		r.RemoteAddr = remote
	}
	if cookieName != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookieVal})
	}
	w := httptest.NewRecorder()
	luciauth.WrapLuciAuth(gateHandler()).ServeHTTP(w, r)
	return w
}

func TestLuciAuthGate(t *testing.T) {
	validSID := testSID
	calls := withLuciAuth(t, func(sid string) bool { return sid == validSID })
	_ = calls

	cases := []struct {
		name   string
		path   string
		cookie string // cookie 名
		value  string // cookie 值
		remote string // RemoteAddr（空 = 默认非本机）
		want   int
	}{
		{"没带 cookie 调接口 → 401", "/api/modems", "", "", "", http.StatusUnauthorized},
		{"带了有效会话 → 放行", "/api/modems", luciauth.LuciCookieHTTP, testSID, "", http.StatusOK},
		{"https 那份 cookie 也算数", "/api/modems", luciauth.LuciCookieHTTPS, testSID, "", http.StatusOK},
		{"老版 LuCI 的 sysauth 也算数", "/api/modems", luciauth.LuciCookieOld, testSID, "", http.StatusOK},
		{"形状不对的 cookie → 401（连 ubus 都不问）", "/api/modems", luciauth.LuciCookieHTTP, "abc", "", http.StatusUnauthorized},
		{"形状对但会话无效 → 401", "/api/modems", luciauth.LuciCookieHTTP,
			"ffffffffffffffffffffffffffffffff", "", http.StatusUnauthorized},
		{"AT 直发接口同样要会话", "/api/at", "", "", "", http.StatusUnauthorized},
		{"WebSocket 握手也要会话（否则推送成后门）", "/ws", "", "", "", http.StatusUnauthorized},
		{"版本接口放行", "/api/version", "", "", "", http.StatusOK},
		{"静态页面放行（要能打开才有地方提示登录）", "/", "", "", "", http.StatusOK},
		{"/mini 放行（LuCI 小组件的 iframe）", "/mini", "", "", "", http.StatusOK},
		{"本机也要登录", "/api/modems", "", "", "127.0.0.1:5555", http.StatusUnauthorized},
		{"IPv6 回环也要登录", "/api/at", "", "", "[::1]:4444", http.StatusUnauthorized},
		{"屏幕推送也要会话", "/display/ws", "", "", "", http.StatusUnauthorized},
	}
	for _, c := range cases {
		w := doGate(t, c.path, c.cookie, c.value, c.remote)
		if w.Code != c.want {
			t.Errorf("%s: 状态码 %d，想要 %d（body=%s）", c.name, w.Code, c.want, w.Body.String())
			continue
		}
		if c.want == http.StatusUnauthorized {
			var body struct {
				NeedLogin bool   `json:"need_login"`
				LoginURL  string `json:"login_url"`
				Msg       string `json:"msg"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Errorf("%s: 401 应答不是 JSON: %v", c.name, err)
				continue
			}
			if !body.NeedLogin || body.LoginURL == "" || body.Msg == "" {
				t.Errorf("%s: 401 应答要带 need_login/login_url/msg，实际 %s", c.name, w.Body.String())
			}
		}
	}
}

// TestLuciAuthGateLuciSIDParam：**免登**的两条口子 —— `?luci_sid=` 查询参数与 `X-Luci-Sid` 请求头。
//
// 场景（用户 2026-10-06 要求：「在嵌入页面那边获取到登录的 cookie，拼装传入参数，
// go 接收到这些参数来实现免登」）：LuCI 页面里的悬浮小窗（widget.js）从 `L.env.sessionid`
// 拿到当前 LuCI 会话，拼进 iframe 的 `?luci_sid=` / 请求头 `X-Luci-Sid`，
// 这样**不用再登一次设备中心**。所以这两条路径必须真的能放行，而且：
//
//	· 形状不对的（太短/带非法字符）连 ubus 都不该问；
//	· 会话无效的照样 401（免登 ≠ 免鉴权）；
//	· 它与 cookie 是"任意一个有效就放行"，混着用不受顺序影响。
func TestLuciAuthGateLuciSIDParam(t *testing.T) {
	var asked []string
	withLuciAuth(t, func(sid string) bool {
		asked = append(asked, sid)
		return sid == testSID
	})

	// 带参数的请求：path 里已经含 ?luci_sid=…（httptest.NewRequest 会把 query 解出来）
	doGateURL := func(path string, header string, cookies ...[2]string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", "https://example.test"+path, nil)
		if header != "" {
			r.Header.Set(luciauth.LuciSIDHeader, header)
		}
		for _, c := range cookies {
			r.AddCookie(&http.Cookie{Name: c[0], Value: c[1]})
		}
		w := httptest.NewRecorder()
		luciauth.WrapLuciAuth(gateHandler()).ServeHTTP(w, r)
		return w
	}

	if w := doGateURL("/api/modems?"+luciauth.LuciSIDParam+"="+testSID, ""); w.Code != http.StatusOK {
		t.Errorf("?luci_sid=<有效会话> 应放行（嵌页免登就靠它），实际 %d body=%s", w.Code, w.Body.String())
	}
	if w := doGateURL("/api/modems", testSID); w.Code != http.StatusOK {
		t.Errorf("X-Luci-Sid=<有效会话> 应放行，实际 %d body=%s", w.Code, w.Body.String())
	}
	if w := doGateURL("/ws?"+luciauth.LuciSIDParam+"="+testSID, ""); w.Code != http.StatusOK {
		t.Errorf("WebSocket 握手也要认 luci_sid（小窗的实时推送），实际 %d", w.Code)
	}
	// 混着用：cookie 是坏的、参数是好的 ⇒ 好那个算数
	if w := doGateURL("/api/modems?"+luciauth.LuciSIDParam+"="+testSID, "", [2]string{luciauth.ModemdSessCookie, "ffffffffffffffffffffffffffffffff"}); w.Code != http.StatusOK {
		t.Errorf("cookie 无效但参数有效时应放行，实际 %d", w.Code)
	}
	// widget cookie（widget.js 直接写的那份）也要认
	if w := doGateURL("/api/modems", "", [2]string{luciauth.ModemdWidgetCookie, testSID}); w.Code != http.StatusOK {
		t.Errorf("%s cookie 应放行（LuCI 嵌页同源写进来的那份），实际 %d", luciauth.ModemdWidgetCookie, w.Code)
	}

	// 形状不对：连 ubus 都不该问
	asked = nil
	if w := doGateURL("/api/modems?"+luciauth.LuciSIDParam+"=abc", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("形状不对的 luci_sid 应 401，实际 %d", w.Code)
	}
	if len(asked) != 0 {
		t.Errorf("形状不对的会话不该去问 ubus，实际问了 %v", asked)
	}

	// 形状对但会话无效 ⇒ 401（免登不是免鉴权）
	if w := doGateURL("/api/modems?"+luciauth.LuciSIDParam+"=ffffffffffffffffffffffffffffffff", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("无效会话应 401，实际 %d", w.Code)
	}
	// 传了空的 luci_sid（前端拿不到会话时就是这么拼的）⇒ 和其它没带凭据一样 401
	if w := doGateURL("/api/modems?"+luciauth.LuciSIDParam+"=", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("luci_sid 为空应 401，实际 %d", w.Code)
	}
}

// TestRegisterRoutesWrappedByLuciAuth：中间件必须**真的接在路由上**。
// 这类"写好了但没接线"的漏洞编译期和 lint 都拦不住（本项目的 band_groups 就踩过一次）。
func TestRegisterRoutesWrappedByLuciAuth(t *testing.T) {
	withLuciAuth(t, func(sid string) bool { return sid == testSID })

	ui := func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ui")) }
	h := RegisterRoutes(NewModemManager(), ui)

	// 版本接口放行（未登录时页脚与排障要用）
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, httptest.NewRequest("GET", "/api/version", nil))
	if w1.Code != http.StatusOK {
		t.Errorf("/api/version 应放行，实际 %d", w1.Code)
	}

	// 业务接口要会话
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, httptest.NewRequest("GET", "https://example.test/api/modems", nil))
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("RegisterRoutes 必须套上中间件（未登录应 401），实际 %d body=%s", w2.Code, w2.Body.String())
	}
}

// TestLuciAuthDisabledWhenConfigOff：显式关掉（[ui] require_luci_login = false）时必须是纯透传。
// ⚠️ 注意口径："没写这一行"算**开**（不是关）—— 那是 config_lucilogin_test.go 的用例；
// 这里只管"用户/开关明确关掉"这一种。
func TestLuciAuthDisabledWhenConfigOff(t *testing.T) {
	old := config.Cfg.UI.RequireLuciLogin
	config.Cfg.UI.RequireLuciLogin = false
	defer func() { config.Cfg.UI.RequireLuciLogin = old }()

	if luciauth.LuciAuthEnabled() {
		t.Fatal("显式关掉后不该启用")
	}
	if w := doGate(t, "/api/at", "", "", ""); w.Code != http.StatusOK {
		t.Errorf("闸门关着时不该拦任何请求，实际 %d", w.Code)
	}
}

func TestLuciAuthUnavailableWithoutUbus(t *testing.T) {
	calls := withLuciAuth(t, func(string) bool { return true })
	luciauth.LuciUBusPath = ""

	if !luciauth.LuciAuthEnabled() {
		t.Fatal("缺少 ubus 时仍应要求登录")
	}
	for _, sid := range []string{"", testSID} {
		w := doGate(t, "/api/modems", luciauth.ModemdSessCookie, sid, "")
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("缺少 ubus 时应返回 503，实际 %d", w.Code)
		}
	}
	if *calls != 0 {
		t.Fatalf("缺少 ubus 时仍尝试了 %d 次会话查询", *calls)
	}
}

func TestLuciAuthDoesNotCacheBackendErrors(t *testing.T) {
	withLuciAuth(t, func(string) bool { return true })
	calls := 0
	luciauth.LuciSessionCheck = func(string) (bool, error) {
		calls++
		if calls == 1 {
			return false, errors.New("backend timeout")
		}
		return true, nil
	}
	for _, want := range []int{http.StatusServiceUnavailable, http.StatusOK} {
		if w := doGate(t, "/api/modems", luciauth.ModemdSessCookie, testSID, ""); w.Code != want {
			t.Fatalf("HTTP %d，想要 %d", w.Code, want)
		}
	}
}

// TestLuciSessionValidCaches：同一个 sid 在 TTL 内只问一次 rpcd
// （页面一开就是十几条 /api 请求，每条都 fork 一次 ubus 太浪费）。
func TestLuciSessionValidCaches(t *testing.T) {
	calls := withLuciAuth(t, func(sid string) bool { return sid == testSID })

	for i := 0; i < 2; i++ {
		if ok, err := luciauth.LuciSessionValid(testSID); !ok || err != nil {
			t.Fatalf("有效会话校验失败：ok=%v, err=%v", ok, err)
		}
	}
	if *calls != 1 {
		t.Errorf("有效结果应被缓存，实际问了 %d 次 rpcd", *calls)
	}
	bad := "ffffffffffffffffffffffffffffffff"
	for i := 0; i < 2; i++ {
		if ok, err := luciauth.LuciSessionValid(bad); ok || err != nil {
			t.Fatalf("无效会话校验结果：ok=%v, err=%v", ok, err)
		}
	}
	if *calls != 2 {
		t.Errorf("无效结果也要缓存（不能让无效 cookie 猛刷 ubus），实际问了 %d 次", *calls)
	}
}

// ====================== 登录 / 退出 ======================

// findCookie 取响应里下发的某个 cookie
func findCookie(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func postJSONBody(h http.Handler, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "https://example.test"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func getReq(h http.Handler, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "https://example.test"+path, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// loginStub 假的 rpcd：只认一个密码，并记下销毁过哪些会话
type loginStub struct {
	wantPass  string
	logins    int
	destroyed []string
}

func (s *loginStub) login(user, pass string, timeoutSec int) (string, error) {
	s.logins++
	if pass != s.wantPass {
		return "", errors.New("用户名或密码不对")
	}
	return testSID, nil
}

func (s *loginStub) destroy(sid string) error {
	s.destroyed = append(s.destroyed, sid)
	return nil
}

// withLoginStub 打开闸门并把 rpcd 的登录/销毁换成假实现（真设备不在手边也能验收）
func withLoginStub(t *testing.T, wantPass string) *loginStub {
	t.Helper()
	withLuciAuth(t, func(sid string) bool { return sid == testSID })
	st := &loginStub{wantPass: wantPass}
	oldLogin, oldDestroy := luciauth.RpcdLogin, luciauth.RpcdDestroy
	luciauth.RpcdLogin, luciauth.RpcdDestroy = st.login, st.destroy
	t.Cleanup(func() { luciauth.RpcdLogin, luciauth.RpcdDestroy = oldLogin, oldDestroy })
	return st
}

// TestParseUbusRPCBody 解析 ubus 的 HTTP JSON-RPC 应答（真机抓到的形状）。
func TestParseUbusRPCBody(t *testing.T) {
	data, err := luciauth.ParseUbusRPCBody([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"abc"}]}`))
	if err != nil {
		t.Errorf("状态 0 应该成功：%v", err)
	} else if !strings.Contains(string(data), "ubus_rpc_session") {
		t.Errorf("应把数据段返回，实际 %s", data)
	}
	if _, err := luciauth.ParseUbusRPCBody([]byte(`{"jsonrpc":"2.0","id":1,"result":[6]}`)); err == nil ||
		!strings.Contains(err.Error(), "密码") {
		t.Errorf("状态 6 应翻成「用户名或密码不对」，实际 %v", err)
	}
	if _, err := luciauth.ParseUbusRPCBody([]byte(`{"jsonrpc":"2.0","id":1,"result":[4]}`)); err == nil ||
		!strings.Contains(err.Error(), "会话") {
		t.Errorf("状态 4 应翻成「会话不存在或已过期」，实际 %v", err)
	}
	if data, err := luciauth.ParseUbusRPCBody([]byte(`{"jsonrpc":"2.0","id":1,"result":[0]}`)); err != nil || data != nil {
		t.Errorf("状态 0 且无数据（如 destroy）应返回 nil,nil，实际 %s,%v", data, err)
	}
	for _, bad := range []string{"", "not json", `{"result":[]}`, `{"error":{"code":-32700,"message":"Parse error"}}`} {
		if _, err := luciauth.ParseUbusRPCBody([]byte(bad)); err == nil {
			t.Errorf("%q 应该报错", bad)
		}
	}
}

// TestParseSessionLogin 从登录应答里取会话 ID（真机形状见下）。
func TestParseSessionLogin(t *testing.T) {
	good := `{"ubus_rpc_session":"8f6790c94b2cd9531e03f1d6d23567a2","timeout":3600,"expires":3599}`
	if sid, ok := luciauth.ParseSessionLogin(json.RawMessage(good)); !ok || sid != "8f6790c94b2cd9531e03f1d6d23567a2" {
		t.Errorf("应取出会话 ID，实际 %q ok=%v", sid, ok)
	}
	for _, bad := range []string{
		`{}`, `{"ubus_rpc_session":""}`, `{"ubus_rpc_session":"short"}`,
		`{"ubus_rpc_session":"00000000000000000000000000000000"}`, `nope`,
	} {
		if sid, ok := luciauth.ParseSessionLogin(json.RawMessage(bad)); ok {
			t.Errorf("%s 不该算有效会话（得到 %q）", bad, sid)
		}
	}
}

// TestApiLogin 登录成功要下发会话 cookie（HttpOnly + Path=/，否则跨页面用不了）。
func TestApiLogin(t *testing.T) {
	withLoginStub(t, "s3cret")
	h := luciauth.ApiLogin()

	w := postJSONBody(h, "/api/login", `{"username":"root","password":"s3cret"}`, nil)
	var body struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Code != 0 {
		t.Fatalf("密码对就该成功：HTTP %d %s", w.Code, w.Body.String())
	}
	ck := findCookie(w, luciauth.ModemdSessCookie)
	if ck == nil || ck.Value != testSID {
		t.Fatalf("应下发 %s=<sid> 的 cookie，实际 %+v", luciauth.ModemdSessCookie, ck)
	}
	if !ck.Secure || !ck.HttpOnly || ck.Path != "/" {
		t.Errorf("会话 cookie 必须设置 Secure、HttpOnly 和 Path=/：%+v", ck)
	}

	w2 := postJSONBody(h, "/api/login", `{"username":"root","password":"wrong"}`, nil)
	if findCookie(w2, luciauth.ModemdSessCookie) != nil {
		t.Error("登录失败不能下发 cookie")
	}
	if !strings.Contains(w2.Body.String(), "登录失败") {
		t.Errorf("应给出失败提示，实际 %s", w2.Body.String())
	}
}

func TestLuciAuthPlainHTTPRequiresSession(t *testing.T) {
	stub := withLoginStub(t, "secret")
	api := luciauth.WrapLuciAuth(gateHandler())
	request := httptest.NewRequest(http.MethodGet, "/api/modems", nil)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, request)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("HTTP without a session returned %d", w.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"secret"}`))
	w = httptest.NewRecorder()
	luciauth.ApiLogin().ServeHTTP(w, request)
	cookie := findCookie(w, luciauth.ModemdSessCookie)
	if stub.logins != 1 || cookie == nil || cookie.Secure || !cookie.HttpOnly {
		t.Fatalf("HTTP login returned %d, login calls %d, cookie %+v", w.Code, stub.logins, cookie)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/modems", nil)
	request.AddCookie(cookie)
	w = httptest.NewRecorder()
	api.ServeHTTP(w, request)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP with a valid session returned %d", w.Code)
	}
}

// TestApiLoginRateLimited 超过限流次数后必须挡住（连密码正确也一样）——
// 登录接口在放行名单里，这是它唯一的防线。
func TestApiLoginRateLimited(t *testing.T) {
	withLoginStub(t, "s3cret")
	h := luciauth.ApiLogin()
	for i := 0; i < luciauth.LuciLoginMaxTries; i++ {
		postJSONBody(h, "/api/login", `{"username":"root","password":"wrong"}`, nil)
	}
	w := postJSONBody(h, "/api/login", `{"username":"root","password":"s3cret"}`, nil)
	if !strings.Contains(w.Body.String(), "尝试太频繁") {
		t.Errorf("超过 %d 次后应被挡（密码正确也一样），实际 %s", luciauth.LuciLoginMaxTries, w.Body.String())
	}
	if findCookie(w, luciauth.ModemdSessCookie) != nil {
		t.Error("被限流时不能下发 cookie")
	}
}

// TestApiLoginDisabledWhenAuthOff：闸门关着时登录接口要说清"不需要登录"，
// 免得有人以为这里能改密码。
func TestApiLoginDisabledWhenAuthOff(t *testing.T) {
	old := config.Cfg.UI.RequireLuciLogin
	config.Cfg.UI.RequireLuciLogin = false
	defer func() { config.Cfg.UI.RequireLuciLogin = old }()

	w := postJSONBody(luciauth.ApiLogin(), "/api/login", `{"username":"root","password":"x"}`, nil)
	if !strings.Contains(w.Body.String(), "未启用登录校验") {
		t.Errorf("关掉闸门时应明确告知，实际 %s", w.Body.String())
	}
}

// TestApiLogout 退出要**顺手销毁 rpcd 会话**（与路由器管理页同步退出）+ 清掉 cookie。
func TestApiLogout(t *testing.T) {
	st := withLoginStub(t, "s3cret")
	w := postJSONBody(luciauth.ApiLogout(), "/api/logout", `{}`,
		&http.Cookie{Name: luciauth.ModemdSessCookie, Value: testSID})
	if len(st.destroyed) != 1 || st.destroyed[0] != testSID {
		t.Errorf("退出应销毁 rpcd 会话 %s，实际 %v", testSID, st.destroyed)
	}
	ck := findCookie(w, luciauth.ModemdSessCookie)
	if ck == nil || ck.MaxAge >= 0 {
		t.Errorf("退出应把会话 cookie 清掉（MaxAge<0），实际 %+v", ck)
	}
}

// TestApiSession 前端靠它决定要不要弹登录框（未登录也必须能调，所以它在放行名单里）。
func TestApiSession(t *testing.T) {
	withLoginStub(t, "s3cret")
	h := luciauth.ApiSession()

	if w := getReq(h, "/api/session", nil); !strings.Contains(w.Body.String(), `"logged_in":false`) {
		t.Errorf("没 cookie 时应报未登录，实际 %s", w.Body.String())
	}
	w := getReq(h, "/api/session", &http.Cookie{Name: luciauth.ModemdSessCookie, Value: testSID})
	if !strings.Contains(w.Body.String(), `"logged_in":true`) {
		t.Errorf("有效会话应报已登录，实际 %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"auth":true`) {
		t.Errorf("应同时说明闸门是开着的，实际 %s", w.Body.String())
	}

	// 回环地址也需要会话。
	r := httptest.NewRequest("GET", "https://example.test/api/session", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, r)
	if !strings.Contains(w3.Body.String(), `"logged_in":false`) {
		t.Errorf("本机无会话时应报未登录，实际 %s", w3.Body.String())
	}
}

// TestLuCIEnvValues 记下真机实测的 3 个"外部事实"，谁改实现前先看这里：
//  1. LuCI 的会话 cookie 是 `sysauth_http`，**path=/cgi-bin/luci/**（+ SameSite=strict + HttpOnly）
//     ⇒ 浏览器不会把它带到 :8899（所以主力必须是 modemd_sess）；
//  2. `session get` 对未知会话**非零退出**（"Command failed: … (Not found)"）⇒
//     非零退出要当"没登录"，不能当"查不了"（否则闸门形同虚设）；
//  3. `session login` 错密码回 ubus 状态码 **6**（Permission denied）⇒ 文案才写"用户名或密码不对"。
func TestLuCIEnvValues(t *testing.T) {
	if luciauth.LuciCookieHTTP != "sysauth_http" || luciauth.ModemdSessCookie != "modemd_sess" {
		t.Errorf("cookie 名变了：sysauth_http=%q modemd_sess=%q", luciauth.LuciCookieHTTP, luciauth.ModemdSessCookie)
	}
	// 候选 cookie 顺序：先看我们自己的（主力）
	r := httptest.NewRequest("GET", "/api/modems", nil)
	r.AddCookie(&http.Cookie{Name: luciauth.ModemdSessCookie, Value: testSID})
	r.AddCookie(&http.Cookie{Name: luciauth.LuciCookieHTTP, Value: "ffffffffffffffffffffffffffffffff"})
	ids := luciauth.LuciSessionIDs(r)
	if len(ids) != 2 || ids[0] != testSID {
		t.Errorf("候选会话应先取 %s，实际 %v", luciauth.ModemdSessCookie, ids)
	}
}

// TestSMSPasswordOffWhenOuterAuthOn 外层登录闸门开着时，短信密码自动停用
// （用户 2026-10-06 要求：外面已经要登录了，短信再要一次密码是重复保护）。
func TestSMSPasswordOffWhenOuterAuthOn(t *testing.T) {
	oldReq := config.Cfg.UI.SMSPasswordRequired
	oldLuci := config.Cfg.UI.RequireLuciLogin
	config.Cfg.UI.SMSPasswordRequired = true
	config.Cfg.UI.RequireLuciLogin = false
	t.Cleanup(func() {
		config.Cfg.UI.SMSPasswordRequired = oldReq
		config.Cfg.UI.RequireLuciLogin = oldLuci
	})

	// 闸门关着（默认）⇒ 短信密码照旧生效
	if !smsPasswordRequired() {
		t.Fatal("外层闸门没开时，短信密码应该照旧生效")
	}

	withLuciAuth(t, func(sid string) bool { return sid == testSID })
	if smsPasswordRequired() {
		t.Error("外层登录开着时不该再要短信密码")
	}
	if smsPasswordIsDefault() {
		t.Error("外层登录开着时不该再提示“还在用默认短信密码”")
	}
}

// TestSMSPAuthReportsOuterAuth：/api/sms/auth 要如实告诉前端"是外层登录在管"
// （界面据此把「访问密码」卡说成“已由路由器登录保护”，而不是“谁都能看”）。
func TestSMSPAuthReportsOuterAuth(t *testing.T) {
	oldReq := config.Cfg.UI.SMSPasswordRequired
	oldLuci := config.Cfg.UI.RequireLuciLogin
	config.Cfg.UI.SMSPasswordRequired = true
	config.Cfg.UI.RequireLuciLogin = false
	t.Cleanup(func() {
		config.Cfg.UI.SMSPasswordRequired = oldReq
		config.Cfg.UI.RequireLuciLogin = oldLuci
	})

	w := getReq(APIAuth(), "/api/sms/auth", nil)
	if !strings.Contains(w.Body.String(), `"required":true`) ||
		!strings.Contains(w.Body.String(), `"outer_auth":false`) {
		t.Errorf("闸门关着时应要密码且 outer_auth=false，实际 %s", w.Body.String())
	}

	withLuciAuth(t, func(sid string) bool { return sid == testSID })
	w2 := getReq(APIAuth(), "/api/sms/auth", nil)
	if !strings.Contains(w2.Body.String(), `"required":false`) ||
		!strings.Contains(w2.Body.String(), `"outer_auth":true`) {
		t.Errorf("外层登录开着时应免密码且 outer_auth=true，实际 %s", w2.Body.String())
	}
}
