package kernel

// "用 URC 顶掉网络状态轮询"这一层的回归测试。
//
// 这一层省掉的是一条**慢总线上最粗的流量**：一次 queryNetworkStatus 要发 8~10 条指令，
// 而 push_interval 默认 3 秒 —— 就是它把总线占满、把每条 AT 拖到 8~10 秒。
// 所以这里的每一条都不能被"顺手简化"掉。

import (
	"modemd/module/atparse"
	"modemd/module/unisoc"
	"testing"
	"time"
)

// URC 行 → 注册域的正确归属 + 复用 ParseReg 的解析结果。
func TestURCRegFromLine(t *testing.T) {
	cases := []struct {
		line   string
		dom    urcRegDomain
		stat   int
		reg    bool
		ok     bool
		lac    string
		cellID string
		act    int
	}{
		{line: `+CREG: 0,1`, dom: urcRegCS, stat: 1, reg: true, ok: true},
		{line: `+CGREG: 1,"FFFE","0400E21E",7`, dom: urcRegPS, stat: 1, reg: true, ok: true,
			lac: "FFFE", cellID: "0400E21E", act: 7},
		{line: `+CEREG: 1,"404A","0400E21E",7`, dom: urcRegEPS, stat: 1, reg: true, ok: true,
			lac: "404A", cellID: "0400E21E", act: 7},
		// 上报形式可以只有 lac、没有 ci（3GPP 允许），必须认得出来
		{line: `+CEREG: 1,"404A"`, dom: urcRegEPS, stat: 1, reg: true, ok: true, lac: "404A"},
		{line: `+C5GREG: 0,5`, dom: urcReg5G, stat: 5, reg: true, ok: true},
		// ⚠️ 上报形式**没有 <n>**：`+CREG: 2` 是 stat=2（未注册、正在搜索）
		{line: `+CREG: 2`, dom: urcRegCS, stat: 2, reg: false, ok: true},
		{line: `+CEREG: 2`, dom: urcRegEPS, stat: 2, reg: false, ok: true},
		{line: `+CGREG: 0`, dom: urcRegPS, stat: 0, reg: false, ok: true},
		// ⚠️ 3GPP 只定义 0~5：展锐扩展码（实测 +CREG: 8,"FFFE","0400E21E",7）含义未知，
		// 宁可不覆盖，也不能把“已注册”写成“未知状态”
		{line: `+CREG: 8,"FFFE","0400E21E",7`, ok: false},
		// 完全不是注册行
		{line: `+CGEV: ME PDN ACT 1`, ok: false},
		{line: `^CONN: 11,2,2`, ok: false},
		{line: `+CSQ: 39,26`, ok: false},
		{line: ``, ok: false},
	}
	for _, c := range cases {
		dom, rs, ok := urcRegFromLine(c.line)
		if ok != c.ok {
			t.Errorf("urcRegFromLine(%q) ok=%v，期望 %v", c.line, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if dom != c.dom {
			t.Errorf("urcRegFromLine(%q) 域=%v，期望 %v", c.line, dom, c.dom)
		}
		if rs.Stat != c.stat || rs.Registered != c.reg {
			t.Errorf("urcRegFromLine(%q) stat=%d reg=%v，期望 %d/%v",
				c.line, rs.Stat, rs.Registered, c.stat, c.reg)
		}
		if rs.LAC != c.lac || rs.CellID != c.cellID || rs.ACT != c.act {
			t.Errorf("urcRegFromLine(%q) lac=%q ci=%q act=%d，期望 %q/%q/%d",
				c.line, rs.LAC, rs.CellID, rs.ACT, c.lac, c.cellID, c.act)
		}
	}
}

// 走完整跟随路径：注册上报要落到 urcNet 对应域上，并且能顶掉网络轮询。
func TestURCFeedCapturesRegistration(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")

	// 只有 PDP 事件（没有注册状态）时**不能**声称覆盖网络状态
	mod.feedURCLine("+CGEV: ME PDN ACT 1")
	mod.noteURCFeedState(true, nil)
	if mod.urcCoversNetwork() {
		t.Fatal("只见到 +CGEV 时不得声称覆盖注册状态（那行里没有注册状态）")
	}
	if !mod.shouldFullQueryNetwork(time.Now()) {
		t.Fatal("没有 URC 注册状态时必须照旧走完整 AT 查询")
	}

	// 来了注册上报：各域分开存，CS 的状态不能串到 PS 上
	mod.feedURCLine(`+CREG: 0,2`) // 应答形式（两个裸数字）
	mod.feedURCLine(`+CGREG: 1,"FFFE","0400E21E",7`)
	mod.feedURCLine(`+CEREG: 1,"404A","0400E21E",7`)
	if !mod.urcCoversNetwork() {
		t.Fatal("收到 +CxREG 之后应声称覆盖注册状态")
	}

	mod.mu.Lock()
	cs, ps, eps := mod.urcNet.cs, mod.urcNet.ps, mod.urcNet.eps
	mod.mu.Unlock()
	if cs == nil || cs.Stat != 2 {
		t.Errorf("CS 域应为 stat=2，实际 %+v", cs)
	}
	if ps == nil || !ps.Registered || ps.LAC != "FFFE" {
		t.Errorf("PS 域应为已注册 LAC=FFFE，实际 %+v", ps)
	}
	if eps == nil || eps.LAC != "404A" {
		t.Errorf("EPS 域 LAC 应为 404A（不能把 CS/PS 的值串过来），实际 %+v", eps)
	}

	// 同一份 URC 来源，信号与网络互不干扰
	if mod.urcCoversSignal() {
		t.Error("只见过注册上报时不得声称覆盖信号")
	}
}

// ⭐ 接口路径"不依赖串口"的**端到端**钉子：走本机文件路径时，网络查询**不需要**串口
// （注册/制式/运营商/频段全来自 URC 日志），测试里的 Modem 没有串口也应当成功返回。
//
// ⚠️注意口径：这不是"一条 AT 都不发" —— URC 里**没有**的字段（APN / AMBR）各走自己的
// TTL 缓存（20s / 60s），过期时各发一轮。"缓存新鲜 ⇒ 零 AT"由下面的
// `TestNetworkStatusFromURCNoATWhenCached` 钉住。
func TestQueryNetworkStatusZeroATForF50(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	mod.feedURCLine(`+CGREG: 1,"FFFE"`)
	mod.feedURCLine(realSPNWNAME)

	st, err := mod.queryNetworkStatus()
	if err != nil {
		t.Fatalf("文件路径不该失败（它不需要串口）：%v", err)
	}
	if !st.PSReg.Registered {
		t.Errorf("注册状态应来自 URC，实际 %+v", st.PSReg)
	}
	if st.Operator != "中国电信" || st.PLMN != "46011" {
		t.Errorf("运营商/PLMN 应来自 URC，实际 %q/%q", st.Operator, st.PLMN)
	}
	if st.QueriedAt.IsZero() {
		t.Error("QueriedAt 必须刷新（它管界面刷新节拍）")
	}
}

// 跟随器掉了（文件读不到）⇒ 该退回去就去查（不能拿旧值装新鲜）。
func TestNetworkPlanFallsBackWhenURCUnavailable(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	mod.feedURCLine(`+CGREG: 1,"FFFE"`)
	if got := mod.networkQueryPlan(); got != planURC {
		t.Fatalf("前提：应走文件路径，实际 %v", got)
	}
	mod.noteURCFeedState(false, errTestURCUnavailable)
	if got := mod.networkQueryPlan(); got != planFullAT {
		t.Errorf("跟随器不可用时必须退回完整 AT 查询，实际 %v", got)
	}
}

// 跟随时器不可用时，网络状态也必须退回完整查询（不能拿空注册状态糊弄界面）。
func TestNetworkFallbackWhenURCUnavailable(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.feedURCLine(`+CGREG: 1,"FFFE"`)
	mod.noteURCFeedState(false, errTestURCUnavailable) // 文件读不到了
	if mod.urcCoversNetwork() {
		t.Error("跟随器不可用时不得声称覆盖注册状态")
	}
	if !mod.shouldFullQueryNetwork(time.Now()) {
		t.Error("跟随器不可用时**必须**照旧走完整 AT 查询")
	}
}

var errTestURCUnavailable = &testErr{"urc log unreadable"}

type testErr struct{ s string }

func (e *testErr) Error() string { return e.s }

// 拼装出的网络状态：URC 的注册字段优先，AT-only 字段沿用缓存，QueriedAt 必须刷新。
func TestNetworkStatusFromURC(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")

	// ① 还没有任何完整查询缓存：不能崩，返回一个带 URC 注册字段的对象
	mod.feedURCLine(`+CGREG: 1,"FFFE","0400E21E",7`)
	mod.noteURCFeedState(true, nil)
	st := mod.networkStatusFromURC()
	if st == nil {
		t.Fatal("缓存为空时也必须返回对象（不能返回 nil 让调用方崩）")
	}
	if !st.PSReg.Registered {
		t.Errorf("PS 注册应来自 URC，实际 %+v", st.PSReg)
	}
	if st.QueriedAt.IsZero() || time.Since(st.QueriedAt) > time.Minute {
		t.Errorf("QueriedAt 必须刷新成现在（它管界面刷新节拍），实际 %v", st.QueriedAt)
	}

	// ② 有完整查询缓存：AT-only 字段（APN / 本机地址 / CFUN / CA）必须保留下来
	mod.cacheNetworkStatus(&NetworkStatus{
		ModemID:   mod.ID,
		QueriedAt: time.Now(),
		APN:       "ctnet",
		IPv4:      "10.3.1.2",
		IPv6:      "240e::1",
		CFUN:      1,
		ModePref:  "LTE 32",
		CSReg:     RegState{Stat: 1, Registered: true}, // 旧的 AT 值
		CA:        []CCInfo{{Band: 78}},
		CAActive:  true,
	})
	// URC 后来报了不一样的 CS（例如 CS 掉出注册）→ 必须以 URC 为准
	mod.feedURCLine(`+CREG: 0,2`)
	st = mod.networkStatusFromURC()
	if st.APN != "ctnet" || st.IPv4 != "10.3.1.2" || st.IPv6 != "240e::1" || st.CFUN != 1 || st.ModePref != "LTE 32" {
		t.Errorf("AT-only 字段应沿用缓存，实际 APN=%q IP=%q/%q CFUN=%d pref=%q",
			st.APN, st.IPv4, st.IPv6, st.CFUN, st.ModePref)
	}
	if !st.CAActive || len(st.CA) != 1 {
		t.Errorf("CA 应沿用缓存，实际 active=%v n=%d", st.CAActive, len(st.CA))
	}
	if st.CSReg.Stat != 2 || st.CSReg.Registered {
		t.Errorf("CS 注册必须以 URC 为准（stat=2），实际 %+v", st.CSReg)
	}
	if !st.PSReg.Registered {
		t.Errorf("PS 注册仍应来自 URC，实际 %+v", st.PSReg)
	}

	// ③ 共享指针必须避开：改写 URC 快照不能影响已经发出去的那份状态
	mod.feedURCLine(`+CGREG: 0,5`)
	if st.PSReg.Roaming || st.PSReg.Stat == 5 {
		t.Error("networkStatusFromURC 必须拷值 —— 不能与 urcNet 共享 *RegState 指针")
	}
}

// 覆盖判据的两道门槛：文件可读 + 真的解析到注册行。
func TestURCCoverageNeedsRegistrationLine(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	if mod.urcCoversNetwork() {
		t.Error("什么行都没收到时不得覆盖注册状态")
	}
	// PDP / 连接类上报不算注册状态
	mod.feedURCLine("+CGEV: NW PDN ACT 1")
	mod.feedURCLine("^CONN: 11,2,2")
	mod.feedURCLine("+SPPCODATA: 1")
	if mod.urcCoversNetwork() {
		t.Error("PDP / 连接类上报里没有注册状态，不得据此省掉 CREG 轮询")
	}
	// 真正的注册行才算
	mod.feedURCLine(`+C5GREG: 0,1`)
	if !mod.urcCoversNetwork() {
		t.Error("收到 +C5GREG 后应覆盖注册状态（5G SA 下这是最准的那条）")
	}
}

// ⚠️ 真机回归（2026-10-06，用户报「当前网络 APN 是空的」）。
//
// 展锐的 URC 里**没有** APN（`+CGEV: ME PDN ACT` 只报 cid），所以这一项必须走 AT；
// 而 `/api/params` 那条路早就通了（`AT+CGDCONT?` + `AT+CGCONTRDP=<cid>`），
// 网络状态直接**复用它的缓存**（TTL 20s）即可 —— 不是每轮都发 AT。
func TestNetworkStatusFromURCFillsAPN(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 35,11`)
	mod.feedURCLine(`+CGREG: 1,"0000","0A11A009",11`)
	if !mu300FileBackedState(mod) {
		t.Fatal("前提：应走本机文件路径")
	}

	// ① 缓存里有 APN（= 用户看过 APN 页，或刚被 TTL 刷新过）⇒ 直接带上
	mod.mu.Lock()
	mod.paramsStatus = &Status{ModemID: mod.ID, APN: "ctnet", QueriedAt: time.Now()}
	mod.mu.Unlock()
	if st := mod.networkStatusFromURC(); st.APN != "ctnet" {
		t.Errorf("APN 应为 ctnet（复用 /api/params 的缓存），实际 %q", st.APN)
	}

	// ② 没有缓存 + 单测里没有串口 ⇒ 留空，**不许编**
	mod.mu.Lock()
	mod.paramsStatus = nil
	mod.mu.Unlock()
	if st := mod.networkStatusFromURC(); st.APN != "" {
		t.Errorf("拿不到 APN 时必须留空，实际 %q", st.APN)
	}
}

// ⚠️ 真机回归（2026-10-06，用户报「F50 UI 上不显示签约速率 / QCI」）。
//
// 展锐的 URC 里**没有** AMBR，而这条 URC 路径以前**根本没调过 QueryAMBR**（只有 AT 路径调）
// ⇒ 指令接了、缓存也有，界面上却永远是“未上报”。
// 修法与 APN 同一套：复用 QueryAMBR 自己的 TTL 缓存（60s），有缓存就一条 AT 都不发。
func TestNetworkStatusFromURCFillsAMBR(t *testing.T) {
	mod := newURCTestModem("/dev/stty_nr2")
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 35,11`)
	mod.feedURCLine(`+CGREG: 1,"0000","0A11A009",11`)
	if !mu300FileBackedState(mod) {
		t.Fatal("前提：应走本机文件路径")
	}

	// ① 缓存里有 AMBR（= 刚被 TTL 刷新过）⇒ 直接带上，QCI / CID / 来源一并保留
	mod.mu.Lock()
	mod.ambrCache = &atparse.AMBRRate{APN: "ctnet", CID: 1, DLMbps: 1000, ULMbps: 1000, QCI: 9,
		Source: "AT+C5GQOSRDP"}
	mod.ambrCacheAt = time.Now()
	mod.mu.Unlock()
	st := mod.networkStatusFromURC()
	if st.AMBR == nil {
		t.Fatal("URC 路径必须带上 AMBR（否则 F50 界面永远显示未上报）")
	}
	if st.AMBR.QCI != 9 || st.AMBR.CID != 1 || st.AMBR.DLMbps != 1000 || st.AMBR.ULMbps != 1000 {
		t.Errorf("AMBR/QCI 丢了：%+v", st.AMBR)
	}
	if st.AMBR.Source != "AT+C5GQOSRDP" {
		t.Errorf("AMBR 来源应带上实际指令，实际 %q", st.AMBR.Source)
	}
	// 不与缓存共享指针（否则后面有人改缓存会带着快照一起变）
	if st.AMBR == mod.ambrCache {
		t.Error("AMBR 必须拷值，不能与缓存共享同一个结构体")
	}

	// ② 查不到（无缓存 + 单测里无串口）⇒ 保持空，**不许编**
	mod.mu.Lock()
	mod.ambrCache, mod.ambrCacheAt = nil, time.Time{}
	mod.mu.Unlock()
	if st := mod.networkStatusFromURC(); st.AMBR != nil {
		t.Errorf("拿不到 AMBR 时必须留空，实际 %+v", st.AMBR)
	}
}

// ⭐ 契约钉子：URC 路径对「URC 里没有的字段」复用 TTL 缓存 —— **缓存都新鲜时一条 AT 都不发**。
//
// 展锐 SIPC 的既定目标是"接口不占总线"：APN 20s / AMBR 60s，只有过期了才各发一轮
// （AMBR 就是一条 `AT+C5GQOSRDP`，nr2 上毫秒级）。这里用假模组**数指令**，
// 防以后有人把缓存去掉、把它改成每轮都查。
func TestNetworkStatusFromURCNoATWhenCached(t *testing.T) {
	withATTimeout(t)
	mod := &Modem{
		ID:        0,
		ATPort:    "/dev/stty_nr2",
		cmdImpl:   &unisoc.UnisocCommand{}, // 本机有 AMBR 指令（AT+C5GQOSRDP）
		writeChan: make(chan string, 64),
		readChan:  make(chan string, 64),
	}
	defer close(mod.writeChan)
	sent := fakeATServe(mod, func(cmd string) []string { return []string{cmd, "OK"} })
	mod.noteURCFeedState(true, nil)
	mod.feedURCLine(`+CSQ: 20,1`)
	mod.feedURCLine(`+CGREG: 1,"FFFE"`)
	if !mu300FileBackedState(mod) {
		t.Fatal("前提：应走本机文件路径")
	}

	// 两份缓存都新鲜 ⇒ 重建网络状态应当一条 AT 都不发
	mod.mu.Lock()
	mod.ambrCache = &atparse.AMBRRate{CID: 1, DLMbps: 1000, ULMbps: 1000, QCI: 9, Source: "AT+C5GQOSRDP"}
	mod.ambrCacheAt = time.Now()
	mod.paramsStatus = &Status{ModemID: mod.ID, APN: "ctnet", QueriedAt: time.Now()}
	mod.mu.Unlock()

	st := mod.networkStatusFromURC()
	if st.AMBR == nil || st.AMBR.QCI != 9 {
		t.Fatalf("缓存里的 AMBR / QCI 没带上：%+v", st.AMBR)
	}
	if st.APN != "ctnet" {
		t.Fatalf("缓存里的 APN 没带上：%q", st.APN)
	}
	if got := drainSent(sent); len(got) != 0 {
		t.Errorf("APN / AMBR 缓存都新鲜时不该发任何 AT，实际发了 %d 条：%v", len(got), got)
	}
}
