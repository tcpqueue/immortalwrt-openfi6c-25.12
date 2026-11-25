package kernel

// 「数据漫游」开关 + 漫游状态的单测（[net] allow_roaming；开关在「高级配置 → 参数设置」，
// 状态显示在「概览 → 信号」）。
//
// 关注五点：
//  ① 默认是**允许**（不写配置算允许）—— 与 dial_enabled 同一个坑：bool 零值是 false，
//     漏了 AllowRoamingSet 这一层就会把"没写"当成"禁止漫游"，升级上来的设备一到漫游
//     （含异网漫游）就把自己的数据掐了；
//  ② 判据只看注册状态（`<stat> == 5`），两条来源（AT 缓存 / URC 跟随）都要认，
//     且数据域优先（EPS → 5GS → PS → CS）；
//  ③ **本地注册时开关关着也照常拨号** —— 关的是"漫游时用数据"，不是"用数据"；
//  ④ 厂商托管数据面（展锐 SIPC / 中兴 F50 那族）如实报"不生效 + 原因"，不装样子；
//  ⑤ 写回只动 allow_roaming 一个键（config.toml 的注释是给用户看的说明书，不能被吃掉）。

import (
	"modemd/module/config"
	"modemd/module/dial"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDataRoamingFromATCache AT 轮询那条路（上一次完整查询的缓存）。
func TestDataRoamingFromATCache(t *testing.T) {
	mod := &Modem{ID: 0}

	// PS 报漫游（stat 5）
	mod.networkStatus = &NetworkStatus{PSReg: RegState{Stat: 5, Registered: true, Roaming: true}}
	if !mod.DataRoaming() {
		t.Error("PS 报 stat=5（漫游）时 DataRoaming 应为 true")
	}
	// PS 报本地（stat 1）
	mod.networkStatus = &NetworkStatus{PSReg: RegState{Stat: 1, Registered: true}}
	if mod.DataRoaming() {
		t.Error("PS 报 stat=1（本地）时 DataRoaming 应为 false")
	}

	// ⭐ 数据域优先：EPS 说本地、PS 说漫游 → 以 EPS 为准（5G SA 下 CGREG 会停在 0,0）
	mod.networkStatus = &NetworkStatus{
		RegEPS: &RegState{Stat: 1, Registered: true},
		Reg5G:  &RegState{Stat: 5, Registered: true, Roaming: true},
		PSReg:  RegState{Stat: 5, Registered: true, Roaming: true},
	}
	if mod.DataRoaming() {
		t.Error("EPS 说本地时不该因为 5GS/PS 报漫游就判成漫游（数据域优先级：EPS → 5GS → PS → CS）")
	}

	// 5G SA：PS 停在 0（未注册未搜索，不算结论），真正状态在 C5GREG 里
	mod.networkStatus = &NetworkStatus{
		PSReg: RegState{Stat: 0},
		Reg5G: &RegState{Stat: 5, Registered: true, Roaming: true},
	}
	if !mod.DataRoaming() {
		t.Error("PS 停在 0 时该看 5GS（C5GREG 报漫游）")
	}

	// PS 的 0 不是结论 → 退回 CS
	mod.networkStatus = &NetworkStatus{
		PSReg: RegState{Stat: 0},
		CSReg: RegState{Stat: 5, Registered: true, Roaming: true},
	}
	if !mod.DataRoaming() {
		t.Error("PS 无结论时该退回 CS 看")
	}

	// 一份注册信息都没有：当「本地」（宁可不禁，也不误断）
	mod.networkStatus = &NetworkStatus{}
	if mod.DataRoaming() {
		t.Error("读不到任何注册状态时该当「本地」处理（不能把用户的数据掐了）")
	}
	mod.networkStatus = nil
	if mod.DataRoaming() {
		t.Error("没有缓存时同上")
	}
	if (*Modem)(nil).DataRoaming() {
		t.Error("nil 模组应返回 false")
	}
}

// TestDataRoamingFromURC URC 跟随那条路（展锐 SIPC / F50 只有这条）。
func TestDataRoamingFromURC(t *testing.T) {
	mod := &Modem{ID: 7, ATPort: "/dev/stty_nr2"}
	mod.urcNet.eps = &RegState{Stat: 5, Registered: true, Roaming: true}
	if !mod.DataRoaming() {
		t.Error("URC 的 EPS 报漫游时 DataRoaming 应为 true")
	}
	// URC 有值就以 URC 为准（它比缓存新）
	mod.networkStatus = &NetworkStatus{PSReg: RegState{Stat: 1, Registered: true}}
	if !mod.DataRoaming() {
		t.Error("URC 已经报漫游时不该被旧的 AT 缓存盖回「本地」")
	}
	mod.urcNet.eps = &RegState{Stat: 1, Registered: true}
	if mod.DataRoaming() {
		t.Error("URC 报本地时应为 false")
	}
}

// roamingTestModem 造一台"普通 USB 模组 + 指定卡归属 / 注册网"的模组（漫游类用例共用）。
//
// stat 用 5（漫游）/ 1（本地）；imsi 空串 = 模拟读不到卡归属；mcc 空串 = 模拟读不到注册网 PLMN。
func roamingTestModem(t *testing.T, stat int, imsi, mcc string) *Modem {
	t.Helper()
	mod := &Modem{ID: 3, ATPort: "/dev/ttyUSB2"} // 普通 USB 模组（非厂商托管数据面）
	mod.isRunning = true
	mod.networkStatus = &NetworkStatus{
		PSReg: RegState{Stat: stat, Registered: stat == 1, Roaming: stat == 5},
		MCC:   mcc,
	}
	if imsi != "" {
		mod.simIdent = SIMIdentity{Present: true, IMSI: imsi}
	}
	return mod
}

// TestRoamScopeDecisionTable ⭐ 核心判据：**开关只管国际漫游，本地卡（含同国异网漫游）不受影响**。
//
// 用户 2026-10-06 明确要求"漫游是为了国外卡支持，本地卡是不影响的"：
// 3GPP 的 `<stat> == 5` 只说明"注册在非归属网"，国内运营商之间的异网漫游也会报 5，
// 那种情况下流量与本地资费一样，属于本地卡的正常状态 —— 拦了就是把本地卡的数据掐了。
func TestRoamScopeDecisionTable(t *testing.T) {
	const (
		chinaIMSI = "460115089161379" // 中国电信：MCC 460（IMSI 前 3 位）
		usIMSI    = "310150123456789" // 美国：MCC 310（3 位 MNC 的 MCC，前 3 位仍是 MCC）
	)
	cases := []struct {
		name    string
		stat    int
		imsi    string
		mcc     string
		want    RoamScope
		blocked bool // 关闭数据漫游时该不该拦
	}{
		{"本地网注册", 1, chinaIMSI, "460", RoamScopeNone, false},
		{"本地网注册（没读到卡）", 1, "", "", RoamScopeNone, false},
		{"同国异网漫游（本地卡）", 5, chinaIMSI, "460", RoamScopeDomestic, false},
		{"国内卡漫游到国外", 5, chinaIMSI, "440", RoamScopeGlobal, true},
		{"国外卡漫游到国内", 5, usIMSI, "460", RoamScopeGlobal, true},
		{"漫游但读不到卡归属", 5, "", "460", RoamScopeUnknown, false},
		{"漫游但读不到注册网 PLMN", 5, chinaIMSI, "", RoamScopeUnknown, false},
	}
	oldVal, oldSet := config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet
	defer func() { config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet = oldVal, oldSet }()
	config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet = false, true // 关掉开关，看拦不拦

	for _, c := range cases {
		mod := roamingTestModem(t, c.stat, c.imsi, c.mcc)
		st := mod.RoamState()
		if st.Scope != c.want {
			t.Errorf("%s：scope 该是 %q，实际 %q（home_mcc=%q visit_mcc=%q）",
				c.name, c.want, st.Scope, st.HomeMCC, st.VisitMCC)
		}
		if st.Roaming != (c.stat == 5) {
			t.Errorf("%s：roaming 该是 %v", c.name, c.stat == 5)
		}
		why := RoamBlockReason(mod)
		if (why != "") != c.blocked {
			t.Errorf("%s：关掉开关时该不该拦 = %v，实际原因 %q", c.name, c.blocked, why)
		}
		// 判不出国别时必须给一句原因（页面直接显示，否则用户只看到"没拦"）
		if st.Scope == RoamScopeUnknown && st.Note == "" {
			t.Errorf("%s：scope=unknown 时必须带 Note", c.name)
		}
		if st.Scope != RoamScopeUnknown && st.Note != "" {
			t.Errorf("%s：scope=%s 不该带 Note（实际 %q）", c.name, st.Scope, st.Note)
		}
	}
}

// TestRoamBlockReasonSemantics 拦截原因的四个组合（只有"关掉 + **国际**漫游"才拦）。
func TestRoamBlockReasonSemantics(t *testing.T) {
	oldVal, oldSet := config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet
	defer func() { config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet = oldVal, oldSet }()

	const chinaIMSI = "460115089161379"
	// 国际漫游：中国卡跑到日本网（MCC 440）
	mod := roamingTestModem(t, 5, chinaIMSI, "440")

	// 允许（默认）→ 不拦
	config.Cfg.Net.AllowRoamingSet = false
	if why := RoamBlockReason(mod); why != "" {
		t.Errorf("允许漫游时不该拦，实际 %q", why)
	}
	// 关闭 + 国际漫游 → 拦，且说明里要写清是"国际漫游"与两侧 MCC
	config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet = false, true
	why := RoamBlockReason(mod)
	if !strings.Contains(why, "国际漫游") || !strings.Contains(why, "460") || !strings.Contains(why, "440") {
		t.Errorf("关闭 + 国际漫游该拦，且要说清两侧 MCC，实际 %q", why)
	}
	// 关闭 + 同国异网漫游 → **不拦**（本地卡！）
	domestic := roamingTestModem(t, 5, chinaIMSI, "460")
	if why := RoamBlockReason(domestic); why != "" {
		t.Errorf("同国异网漫游属于本地卡的正常状态，不该拦，实际 %q", why)
	}
	// 关闭 + 本地 → **不拦**（关的是"国际漫游时用数据"，不是"用数据"）
	local := roamingTestModem(t, 1, chinaIMSI, "460")
	if why := RoamBlockReason(local); why != "" {
		t.Errorf("本地网注册时不该拦（否则等于把数据一起关了），实际 %q", why)
	}
	if why := RoamBlockReason(nil); why != "" {
		t.Errorf("nil 模组应返回空串，实际 %q", why)
	}
}

// TestHostDialStopReasonRoaming 主机侧拨号器的闸门：只拦"国际漫游 + 关掉"这一种。
func TestHostDialStopReasonRoaming(t *testing.T) {
	oldVal, oldSet, oldHostDial := config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet, config.Cfg.Net.HostDial
	defer func() {
		config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet, config.Cfg.Net.HostDial = oldVal, oldSet, oldHostDial
	}()
	config.Cfg.Net.HostDial = true
	config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet = false, true

	const chinaIMSI = "460115089161379"
	mod := roamingTestModem(t, 5, chinaIMSI, "440") // 国际漫游
	reason := hostDialStopReason(mod, &dial.DialState{Kind: "qmi"}, "wwan0")
	if !strings.Contains(reason, "国际漫游") {
		t.Errorf("国际漫游 + 已关闭数据漫游 → 该拦，实际 %q", reason)
	}
	// 同国异网漫游（本地卡）：开关关着也要放行
	if got := hostDialStopReason(roamingTestModem(t, 5, chinaIMSI, "460"), &dial.DialState{Kind: "qmi"}, "wwan0"); got != "" {
		t.Errorf("同国异网漫游时开关关着也该放行（本地卡不受影响），实际 %q", got)
	}
	// 本地网注册：开关关着也要放行（不然"关漫游"变成了"关数据"）
	if got := hostDialStopReason(roamingTestModem(t, 1, chinaIMSI, "460"), &dial.DialState{Kind: "qmi"}, "wwan0"); got != "" {
		t.Errorf("本地网注册时开关关着也该放行，实际 %q", got)
	}
	// 开关打开：国际漫游也放行
	config.Cfg.Net.AllowRoaming = true
	if got := hostDialStopReason(roamingTestModem(t, 5, chinaIMSI, "440"), &dial.DialState{Kind: "qmi"}, "wwan0"); got != "" {
		t.Errorf("允许漫游时不该拦，实际 %q", got)
	}
}

// TestRoamSupportedManagedDataPlane 厂商托管数据面（F50 那族）必须如实报"不生效 + 原因"。
func TestRoamSupportedManagedDataPlane(t *testing.T) {
	if ok, why := RoamSupported(nil); ok || why == "" {
		t.Errorf("nil 模组该报不支持并给原因，实际 ok=%v why=%q", ok, why)
	}
	// F50：数据面归厂商的 netifd（proto=mu300cell），modemd 插不进去。
	// ⚠️ 用 nr2/nr3（SIPC 命令通道）—— nr0 是 URC 上报口，永远不会当 at_port，
	// 也就不算"厂商托管数据面"（见 atdev_mu300_policy.go 的 managedDataPlane）。
	for _, port := range []string{"/dev/stty_nr2", "stty_nr2", "/dev/stty_nr3"} {
		ok, why := RoamSupported(&Modem{ID: 0, ATPort: port})
		if ok {
			t.Errorf("%s 是厂商托管数据面，开关不该算数", port)
		}
		if !strings.Contains(why, "厂商托管") {
			t.Errorf("%s 的原因要说清是厂商托管，实际 %q", port, why)
		}
	}
	// 普通 USB 模组：开关算数
	for _, port := range []string{"/dev/ttyUSB2", "COM3", "/dev/mhi_DUN"} {
		if ok, why := RoamSupported(&Modem{ID: 1, ATPort: port}); !ok {
			t.Errorf("%s 该支持，实际 ok=%v why=%q", port, ok, why)
		}
	}
}

// TestRoamStatusPayload /api/roam 的字段语义（页面照它画，字段名不能随便改）。
func TestRoamStatusPayload(t *testing.T) {
	oldVal, oldSet := config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet
	defer func() { config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet = oldVal, oldSet }()

	mod := &Modem{ID: 2, ATPort: "/dev/ttyUSB2"}
	mod.isRunning = true
	// 国际漫游（中国卡在日本网）
	mod.networkStatus = &NetworkStatus{PSReg: RegState{Stat: 5, Registered: true, Roaming: true}, MCC: "440"}
	mod.simIdent = SIMIdentity{Present: true, IMSI: "460115089161379"}

	config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet = false, true
	st := RoamStatus(mod)
	if st["allow_roaming"] != false || st["roaming"] != true || st["blocked"] != true || st["effective"] != true {
		t.Errorf("关闭漫游 + 国际漫游：allow=false roaming=true blocked=true effective=true，实际 %v", st)
	}
	if st["roam_scope"] != "global" || st["home_mcc"] != "460" || st["visit_mcc"] != "440" {
		t.Errorf("该报 global + 两侧 MCC，实际 %v", st)
	}

	// 允许漫游时 blocked 必须是 false（页面据此决定要不要变红）
	config.Cfg.Net.AllowRoaming = true
	if st := RoamStatus(mod); st["blocked"] != false {
		t.Errorf("允许漫游时 blocked 该是 false，实际 %v", st)
	}

	// ⭐ 同国异网漫游（本地卡）：开关关着也**不算 blocked**，但要把原因说清楚
	config.Cfg.Net.AllowRoaming = false
	domestic := roamingTestModem(t, 5, "460115089161379", "460")
	st = RoamStatus(domestic)
	if st["blocked"] != false || st["effective"] != true {
		t.Errorf("同国异网漫游不该标 blocked（本地卡不受影响），实际 %v", st)
	}
	if !strings.Contains(st["reason"].(string), "同国异网") {
		t.Errorf("该在 reason 里说明是同国异网漫游，实际 %v", st)
	}

	// 判不出国别：不拦，但 reason / roam_note 要说清
	unknown := roamingTestModem(t, 5, "", "460")
	st = RoamStatus(unknown)
	if st["blocked"] != false || st["roam_scope"] != "unknown" || st["roam_note"] == "" {
		t.Errorf("判不出国别时不拦 + 带 roam_note，实际 %v", st)
	}

	// 厂商托管数据面：即使"关了 + 国际漫游"也不该标 blocked（本来就没断），但要给原因
	config.Cfg.Net.AllowRoaming = false
	f50 := &Modem{ID: 0, ATPort: "/dev/stty_nr2"}
	f50.urcNet.eps = &RegState{Stat: 5, Registered: true, Roaming: true}
	f50.networkStatus = &NetworkStatus{MCC: "460"}
	f50.simIdent = SIMIdentity{Present: true, IMSI: "310150123456789"}
	st = RoamStatus(f50)
	if st["blocked"] != false {
		t.Errorf("厂商托管数据面下不该标 blocked（没断就是没断），实际 %v", st)
	}
	if st["effective"] != false || !strings.Contains(st["reason"].(string), "厂商托管") {
		t.Errorf("该报 effective=false + 原因，实际 %v", st)
	}
}

// TestFillRoamIntoNetworkStatus 两条路都要把漫游状态写进 NetworkStatus（页面「信号」那一行）。
func TestFillRoamIntoNetworkStatus(t *testing.T) {
	oldVal, oldSet := config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet
	defer func() { config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet = oldVal, oldSet }()
	config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet = false, true

	mod := &Modem{ID: 5, ATPort: "/dev/ttyUSB2"}
	st := &NetworkStatus{PSReg: RegState{Stat: 5, Registered: true, Roaming: true}}
	mod.fillRoam(st)
	if !st.Roaming || st.RoamAllowed {
		t.Errorf("该填 roaming=true + roam_allowed=false，实际 %+v", st)
	}
	// ⚠️ 每轮都覆盖：注册域从漫游变本地后必须跟着变（不能"第一次填完就冻住"）
	st.PSReg = RegState{Stat: 1, Registered: true}
	mod.fillRoam(st)
	if st.Roaming {
		t.Error("变回本地后必须重写 roaming=false（每轮覆盖，不能只在空时填）")
	}
	// 这份状态里没有注册字段时退回全局判据（URC 跟随器）
	mod.urcNet.eps = &RegState{Stat: 5, Registered: true, Roaming: true}
	empty := &NetworkStatus{}
	mod.fillRoam(empty)
	if !empty.Roaming {
		t.Error("状态里没有注册字段时该退回 DataRoaming（URC 跟随器）")
	}
	// nil 不该 panic
	mod.fillRoam(nil)
}

// TestSetAllowRoamingWriteBack 写回只动 allow_roaming 一个键，注释与别的段原样保留。
func TestSetAllowRoamingWriteBack(t *testing.T) {
	const orig = "# 顶部注释（别动我）\n" +
		"[server]\n" +
		"listen = \":8080\"\n" +
		"\n" +
		"[net]\n" +
		"auto_wan = true\n" +
		"# 数据漫游（页面上「高级配置 → 参数设置」）\n" +
		"wan_metric = 20\n" +
		"\n" +
		"[ui]\n" +
		"sms_password = \"p@ss\"\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	// 配置路径与开关都是全局变量：测完还原（AllowRoamingSet 也要还原，它参与 AllowRoaming() 的判定）
	oldPath, oldVal, oldSet := config.ConfigPath, config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet
	defer func() {
		config.ConfigPath, config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet = oldPath, oldVal, oldSet
	}()
	config.ConfigPath = path

	if err := config.SetAllowRoaming(false); err != nil {
		t.Fatalf("写回 false 失败: %v", err)
	}
	if config.Cfg.Net.AllowRoaming {
		t.Error("写回 false 之后内存里的 Cfg 也该是 false（不然要等进程重启）")
	}
	if config.AllowRoaming() {
		t.Error("写回 false 之后 AllowRoaming() 该是 false")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "allow_roaming = false") {
		t.Errorf("文件里没有 allow_roaming = false：\n%s", got)
	}
	if n := strings.Count(got, "allow_roaming"); n != 1 {
		t.Errorf("allow_roaming 该只有一行，实际 %d 行：\n%s", n, got)
	}
	for _, keep := range []string{
		"# 顶部注释（别动我）",
		"# 数据漫游（页面上「高级配置 → 参数设置」）",
		"sms_password = \"p@ss\"",
		"listen = \":8080\"",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("写回把 %q 弄丢了：\n%s", keep, got)
		}
	}

	// 再开回来：文件里不该还留着 false
	if err := config.SetAllowRoaming(true); err != nil {
		t.Fatalf("写回 true 失败: %v", err)
	}
	if !config.AllowRoaming() {
		t.Error("写回 true 之后 AllowRoaming() 该是 true")
	}
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), "allow_roaming = true") {
		t.Errorf("文件里没有 allow_roaming = true：\n%s", string(b))
	}
}

// TestAllowRoamingDefaultTrue 不写 allow_roaming 时默认**允许**。
//
// ⚠️ 这条和 TestDialEnabledDefaultTrue 一样关键：bool 零值是 false，忘了 IsDefined/
// AllowRoamingSet 这一层就会把"没写"当成"禁止漫游" —— 升级上来的设备一到漫游就断网，
// 而且用户找不到原因（页面上那个开关看起来还是"开"的）。
func TestAllowRoamingDefaultTrue(t *testing.T) {
	const orig = "[server]\nlisten = \":8080\"\n\n[net]\nauto_wan = true\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath, oldVal, oldSet := config.ConfigPath, config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet
	defer func() {
		config.ConfigPath, config.Cfg.Net.AllowRoaming, config.Cfg.Net.AllowRoamingSet = oldPath, oldVal, oldSet
	}()
	config.ConfigPath = path

	if err := config.Load(); err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if config.Cfg.Net.AllowRoamingSet {
		t.Error(`配置里没写 allow_roaming 时不该被当成"显式写过"`)
	}
	if !config.AllowRoaming() {
		t.Error("配置里没写 allow_roaming 时应该**允许**（默认允许，升级不能悄悄改行为）")
	}
	// 显式写 false 才禁止
	if err := os.WriteFile(path, []byte(orig+"allow_roaming = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.Load(); err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if !config.Cfg.Net.AllowRoamingSet {
		t.Error("显式写了 allow_roaming 之后 AllowRoamingSet 该是 true")
	}
	if config.AllowRoaming() {
		t.Error("显式写 allow_roaming = false 之后该禁止漫游")
	}
}
