package kernel

// ====================== 拨号上网：纯逻辑用例 ======================
//
// 只测纯逻辑（类型表 / 类码解析 / 状态机 / 主机网卡匹配）：
// 发指令那层要串口，靠实机冒烟（页面点「拨号」）验证，见 dial_engine.go。

import (
	"modemd/module/config"
	"modemd/module/dial"
	"modemd/module/fibocom"
	"modemd/module/quectel"
	"modemd/module/simcom"
	"modemd/module/usbids"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 取值表：五个已知取值都在、Kind 归一正确、每种类型都说清了主机侧怎么拨
func TestDialTypeTable(t *testing.T) {
	want := map[string]string{"0": "qmi", "1": "ecm", "2": "mbim", "3": "rndis", "5": "ncm"}
	types := dial.DialTypes()
	if len(types) != len(want) {
		t.Fatalf("类型表条目 = %d, want %d", len(types), len(want))
	}
	for _, dt := range types {
		if want[dt.Value] != dt.Kind {
			t.Errorf("取值 %s 的 kind = %q, want %q", dt.Value, dt.Kind, want[dt.Value])
		}
		if dt.Name == "" || dt.HostDriver == "" || dt.HostSetup == "" {
			t.Errorf("取值 %s 的类型说明不完整: %+v", dt.Value, dt)
		}
		// 与 USB 网卡模式那张表（net.go 的 Modes）口径必须一致，否则页面两个地方对不上
		if ModeName(dt.Value) != dt.Name {
			t.Errorf("取值 %s 的名字 = %q, Modes() 里是 %q", dt.Value, dt.Name, ModeName(dt.Value))
		}
	}
	// 表外取值：名字空、判定 unknown
	if dial.DialKindOf("9") != "unknown" || dial.DialKindOf("") != "unknown" {
		t.Error("表外取值应为 unknown")
	}
	if _, ok := dial.DialTypeByValue("9"); ok {
		t.Error("表外取值不该命中类型表")
	}
	// 归一化后的 kind 必须与取值一一对应（状态里存的是 kind）
	if dial.DialKindOf("2") != "mbim" {
		t.Errorf("取值 2 的 kind = %q, want mbim", dial.DialKindOf("2"))
	}
}

// 类码三种写法都要认（sysfs 拼接 / 连字符 / Windows 注册表 CompatibleIDs 原文）
func TestSplitUSBClassCode(t *testing.T) {
	ok := map[string][3]string{
		"02/06/00":                         {"02", "06", "00"},
		"02-0e-00":                         {"02", "0e", "00"},
		`USB\Class_02&SubClass_0e&Prot_00`: {"02", "0e", "00"},
		"2/6":                              {"02", "06", ""},
		"ff":                               {"ff", "", ""},
		`USB\Class_EF&SubClass_04&Prot_01`: {"ef", "04", "01"},
	}
	for in, want := range ok {
		class, sub, proto, good := usbids.SplitUSBClassCode(in)
		if !good {
			t.Errorf("%q 应该能解析", in)
			continue
		}
		if class != want[0] || sub != want[1] || proto != want[2] {
			t.Errorf("%q → %s/%s/%s, want %s/%s/%s", in, class, sub, proto, want[0], want[1], want[2])
		}
	}
	for _, bad := range []string{"", "zz/06/00", "\\", "USB\\Foo"} {
		if _, _, _, good := usbids.SplitUSBClassCode(bad); good {
			t.Errorf("%q 不该解析成功", bad)
		}
	}
}

// 接口类码 → usbnet 取值：MBIM 与 ECM 同属 02 类，必须靠子类区分
func TestClassifyUSBClasses(t *testing.T) {
	cases := []struct {
		classes []string
		want    string
	}{
		{[]string{"02/0e/00"}, "2"},             // MBIM
		{[]string{"02/06/00"}, "1"},             // CDC-ECM
		{[]string{"02/06/00", "02/0e/00"}, "2"}, // 两个都在时以 MBIM 为准（更具体优先）
		{[]string{"02/0d/00"}, "5"},             // NCM（靠控制口子类 0d 认，不靠数据口）
		{[]string{"ef/04/01"}, "3"},             // RNDIS
		{[]string{"02/02/01", "ff/ff/ff"}, ""},  // 只有 ACM + 厂商自定义 → 认不出
		// ⚠️ 实测鼎桥 MT5700M-CN（NCM）：02/0d 控制口 + 0a/00/01 数据口 → NCM
		{[]string{"02/0d/00", "0a/00/01", "ff/06/12", "ff/06/13", "ff/06/14", "ff/06/1c"}, "5"},
		// ⚠️ 曾经判反的一类：ECM 模组是 02/06 控制口 + **0a 数据口**，
		// 旧代码第一轮就把 0a 当成 NCM → 整台 ECM 模组被当成 NCM
		{[]string{"02/06/00", "0a/00/00"}, "1"},
		// 只剩数据口（认不出控制口）→ 按 NCM 兜底，而不是“未知”
		{[]string{"0a/00/00"}, "5"},
	}
	for _, c := range cases {
		got, ok := usbids.ClassifyUSBClasses(c.classes)
		if c.want == "" {
			if ok {
				t.Errorf("%v 不该认出来，得到 %q", c.classes, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("%v → %q(ok=%v), want %q", c.classes, got, ok, c.want)
		}
	}

	// 没有标准数据类、但有厂商自定义接口：按 QMI/NDIS 推断
	// （实测 FM350-GL：ff/00/00 + ff/42/01 + 蓝牙 e0/01/03 → 蓝牙接口不该挡住推断）
	if !dial.HasVendorInterface([]string{"ff/00/00", "ff/42/01", "e0/01/03"}) {
		t.Error("有厂商自定义接口就该判为 QMI/NDIS 候选")
	}
	if dial.HasVendorInterface([]string{"02/06/00", "e0/01/03"}) {
		t.Error("没有 ff 接口就不该按 QMI/NDIS 推断")
	}
	if dial.HasVendorInterface(nil) {
		t.Error("空列表不该判为厂商自定义")
	}
}

// 类码简写收敛：同一个接口会被报成三行（实测 Windows CompatibleIDs），只留最具体的那条
func TestReduceUSBCodes(t *testing.T) {
	// 实测 FM350-GL 的设备枚举注册表原文（同一接口的三种写法）
	in := []string{"e0//", "e0/01/", "e0/01/03", "ff//", "ff/00/", "ff/00/00", "ff/42/", "ff/42/01"}
	got := dial.ReduceUSBCodes(in)
	want := []string{"e0/01/03", "ff/00/00", "ff/42/01"}
	if len(got) != len(want) {
		t.Fatalf("收敛后 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("收敛后 = %v, want %v", got, want)
		}
	}
	// 同一类下只有一个简写时（没有更具体的）要保留下来，不能全丢
	if got := dial.ReduceUSBCodes([]string{"ff//", "ff/00/00"}); len(got) != 1 || got[0] != "ff/00/00" {
		t.Errorf("应只留 ff/00/00，得到 %v", got)
	}
	if got := dial.ReduceUSBCodes([]string{"02/06"}); len(got) != 1 || got[0] != "02/06" {
		t.Errorf("只有两段式时原样保留，得到 %v", got)
	}
	if got := dial.ReduceUSBCodes(nil); len(got) != 0 {
		t.Errorf("空输入应返回空，得到 %v", got)
	}
	// 收敛后仍要能被类码判定认出来（端到端串一下）
	if v, ok := usbids.ClassifyUSBClasses(dial.ReduceUSBCodes([]string{"02/0e/00", "02/0e/"})); !ok || v != "2" {
		t.Errorf("MBIM 应认出来，得到 %q(ok=%v)", v, ok)
	}
}

// 类码翻人话：常见几种要有可读名字，认不出的原样回传
func TestClassSummary(t *testing.T) {
	cases := map[string]string{
		"02/0e/00": "02/0e/00 CDC-MBIM 控制接口",
		"02/0d/00": "02/0d/00 CDC-NCM 控制接口",
		"02/06/00": "02/06/00 CDC-ECM 控制接口",
		"0a/00/01": "0a/00/01 CDC 数据接口（ECM / NCM / MBIM 共用）",
		"ef/04/01": "ef/04/01 RNDIS",
		"ff/06/12": "ff/06/12 厂商自定义串口（AT / 诊断口）",
		"ff/ff/ff": "ff/ff/ff 厂商自定义（AT / QMI / 诊断口）",
		"weird":    "weird",
	}
	for in, want := range cases {
		if got := dial.ClassSummary(in); got != want {
			t.Errorf("ClassSummary(%q) = %q, want %q", in, got, want)
		}
	}
	// 去重：同一类码出现两次只留一条
	if got := dial.ClassSummaryList([]string{"02/06/00", "02/06/00", "ff/ff/ff"}); len(got) != 2 {
		t.Errorf("ClassSummaryList 去重后 = %v", got)
	}
}

// 主机网卡匹配：USB/WWAN 名算强候选，通用以太网名只作参考，虚拟/无线一律排除
func TestMatchDialIface(t *testing.T) {
	strong := []string{"wwan0", "rmnet_data0", "usb0", "enx001122334455", "wwx9a1b"}
	for _, n := range strong {
		ok, s := dial.MatchDialIface(n)
		if !ok || !s {
			t.Errorf("%q 应为强候选，得到 ok=%v strong=%v", n, ok, s)
		}
	}
	weak := []string{"eth0", "enp3s0", "以太网", "Ethernet"}
	for _, n := range weak {
		ok, s := dial.MatchDialIface(n)
		if !ok || s {
			t.Errorf("%q 应为弱候选，得到 ok=%v strong=%v", n, ok, s)
		}
	}
	// 这些名字里含 eth/usb 字样，但都不是模组数据口
	for _, n := range []string{"Loopback", "docker0", "veth1a2b", "vEthernet (WSL)", "vEthernet (Default Switch)",
		"VirtualBox Host-Only Ethernet Adapter", "VMware Network Adapter VMnet1", "Bluetooth PAN",
		"WLAN", "Wi-Fi", "br-lan", "tun0", "Tailscale", "以太网 3 (VPN)"} {
		if ok, _ := dial.MatchDialIface(n); ok {
			t.Errorf("%q 不该当候选网卡", n)
		}
	}
}

// 候选排序：优先"已 up 且有地址的强候选"，弱候选垫底
func TestPickDialIface(t *testing.T) {
	if _, ok := dial.PickDialIface(nil); ok {
		t.Error("空列表不该挑出网卡")
	}
	list := []dial.IfaceInfo{
		{Name: "eth0", Up: true, Addr: []string{"192.168.1.2"}},
		{Name: "wwan0", Up: false},
		{Name: "usb0", Up: true, Addr: []string{"10.2.98.236"}, Strong: true},
	}
	got, ok := dial.PickDialIface(list)
	if !ok || got.Name != "usb0" {
		t.Errorf("应挑出 usb0（强候选 + 已 up + 有地址），得到 %+v", got)
	}
	// 没有强候选时退而求其次：已 up 有地址的弱候选
	got, _ = dial.PickDialIface([]dial.IfaceInfo{
		{Name: "wwan0", Up: false},
		{Name: "eth0", Up: true, Addr: []string{"192.168.1.2"}},
	})
	if got.Name != "eth0" {
		t.Errorf("没有可用强候选时应挑 eth0，得到 %+v", got)
	}
}

// VID/PID 的几种写法
func TestSplitVidPid(t *testing.T) {
	cases := map[string][2]string{
		"VID_2C7C&PID_0800": {"2c7c", "0800"},
		"2c7c:0800":         {"2c7c", "0800"},
		"vid_2c7c":          {"2c7c", ""},
		"":                  {"", ""},
		"notahex":           {"", ""},
	}
	for in, want := range cases {
		v, p := usbids.SplitVidPid(in)
		if v != want[0] || p != want[1] {
			t.Errorf("usbids.SplitVidPid(%q) = %q/%q, want %q/%q", in, v, p, want[0], want[1])
		}
	}
	if got := dial.FormatVidPid("2c7c", "0800"); got != "2c7c:0800" {
		t.Errorf("FormatVidPid = %q", got)
	}
	if got := dial.FormatVidPid("", ""); got != "" {
		t.Errorf("空 VID/PID 应返回空串，得到 %q", got)
	}
}

// 状态快照要深拷贝切片，否则前端序列化时会读到拨号协程正在 append 的半截数据
func TestDialStateSnapshot(t *testing.T) {
	orig := dial.DialState{
		State: dial.DialConnected, HostIPs: []string{"10.0.0.2"},
		Steps:       []dial.DialStep{{Name: "第一步", OK: true}},
		ConnectedAt: time.Now().Add(-90 * time.Second),
	}
	cp := orig.Snapshot()
	cp.HostIPs[0] = "changed"
	cp.Steps[0].Name = "changed"
	if orig.HostIPs[0] != "10.0.0.2" || orig.Steps[0].Name != "第一步" {
		t.Error("快照必须深拷贝切片")
	}
	if e := cp.WithElapsed(time.Now()).ElapsedSec; e < 89 || e > 91 {
		t.Errorf("已连接时长 ≈90s，得到 %d", e)
	}
	// 没连接过（或没拨号）不该算时长
	if got := (dial.DialState{State: dial.DialConnected}).WithElapsed(time.Now()).ElapsedSec; got != 0 {
		t.Errorf("没有连接时间点时时长应为 0，得到 %d", got)
	}
	if dial.DialStateText("") != "未拨号" || dial.DialStateText(dial.DialFailed) != "拨号失败" {
		t.Error("状态文案不对")
	}
}

// TestUSBTypeAcrossVendors 跨厂商取值：广和通 `AT+GTUSBMODE` 的 32 也是 QMI。
//
// 现场反馈：模组报 `QMI 固件上报（AT+QCFG="usbnet"）· 取值 32`，但下面四项全是"未识别" ——
// 因为拨号方式表是按**移远取值**（0/1/2/3/5）做 key 的，32 查不到。
// 现在按"本机取值表里的名字"先把协议认出来，再用协议查表。
func TestUSBTypeAcrossVendors(t *testing.T) {
	initTestLogger()
	withATTimeout(t)

	cases := []struct {
		name    string
		mod     *Modem
		reply   []string
		wantVal string
		wantNam string
		wantKnd string
	}{
		{
			name: "广和通 32 → QMI",
			mod: &Modem{ID: 0, Vendor: VendorFibocom, Model: "FM160-CN", ModelName: "FM160-CN",
				ATPort: "COM98", cmdImpl: &fibocom.FibocomCommand{}},
			reply:   []string{"AT+GTUSBMODE?", "+GTUSBMODE: 32", "OK"},
			wantVal: "32", wantNam: "QMI", wantKnd: "qmi",
		},
		{
			name: "广和通 40 → MBIM",
			mod: &Modem{ID: 0, Vendor: VendorFibocom, Model: "FM650-CN", ModelName: "FM650-CN",
				ATPort: "COM98", cmdImpl: &fibocom.FibocomCommand{}},
			reply:   []string{"AT+GTUSBMODE?", "+GTUSBMODE: 40", "OK"},
			wantVal: "40", wantNam: "MBIM", wantKnd: "mbim",
		},
		{
			name: "移远 2 → MBIM（老口径不变）",
			mod: &Modem{ID: 0, Vendor: VendorQuectel, Model: "RM520N-GL", ModelName: "RM520N-GL",
				ATPort: "COM98", cmdImpl: &quectel.QuectelCommand{}},
			reply:   []string{`AT+QCFG="usbnet"`, `+QCFG: "usbnet",2`, "OK"},
			wantVal: "2", wantKnd: "mbim",
		},
	}
	for _, c := range cases {
		c.mod.writeChan = make(chan string, 16)
		c.mod.readChan = make(chan string, 16)
		fakeATServe(c.mod, func(cmd string) []string { return c.reply })
		info := c.mod.USBType(true)
		if info.Value != c.wantVal || info.Kind != c.wantKnd {
			t.Errorf("%s：value=%q kind=%q, want %q / %q", c.name, info.Value, info.Kind, c.wantVal, c.wantKnd)
		}
		if c.wantNam != "" && info.Name != c.wantNam {
			t.Errorf("%s：展示名 = %q, want %q", c.name, info.Name, c.wantNam)
		}
		// 关键：跨厂商也要能查到拨号方式（主机侧接口/驱动就是它渲染的）
		if info.Dial == nil {
			t.Errorf("%s：应带拨号方式（否则页面「主机侧接口/驱动」又是未识别）", c.name)
			continue
		}
		if info.Dial.Kind != c.wantKnd || info.Dial.Protocol == "" || info.Dial.HostDriver == "" {
			t.Errorf("%s：拨号方式不对 %+v", c.name, *info.Dial)
		}
	}
}

// TestDialKindHelpers 协议名归一 / 按协议查表
func TestDialKindHelpers(t *testing.T) {
	for name, want := range map[string]string{
		"QMI": "qmi", "QMI/NDIS": "qmi", "NDIS": "qmi",
		"MBIM": "mbim", "ECM": "ecm", "RNDIS": "rndis", "NCM": "ncm",
		"": "", "PPP": "",
	} {
		if got := dial.DialKindFromName(name); got != want {
			t.Errorf("DialKindFromName(%q) = %q, want %q", name, got, want)
		}
	}
	if _, ok := dial.DialTypeByKind("qmi"); !ok {
		t.Error("按协议应能查到 QMI 的拨号方式")
	}
	if _, ok := dial.DialTypeByKind("unknown"); ok {
		t.Error("unknown 不该查到拨号方式")
	}
	if _, ok := dial.DialTypeByValue("32"); ok {
		t.Error("32 不在（移远口径的）取值表里 —— 所以必须按 kind 查")
	}
}

// USB 类型识别：问不到固件、也读不到 USB 描述符时要给出"未知 + 原因"，而不是报错
func TestUSBTypeUnknownFallback(t *testing.T) {
	initTestLogger()
	// SimcomCommand 不声明 USBnetConfigurator（不发未验证的 QCFG），
	// COM99 是个不存在的串口 → 注册表/sysfs 都查不到 → 落到 unknown
	mod := &Modem{ID: 7, ATPort: "COM99", cmdImpl: &simcom.SimcomCommand{}}
	info := mod.USBType(true)
	if info.Kind != "unknown" || info.Value != "" {
		t.Errorf("应为未知类型，得到 %+v", info)
	}
	if info.Note == "" {
		t.Error("识别不出来时必须给出原因（页面要显示）")
	}
	if info.Dial != nil {
		t.Error("未知类型不该带拨号方式")
	}
	// 缓存：第二次不走 force 应直接命中缓存（ProbedAt 不变）
	again := mod.USBType(false)
	if !again.ProbedAt.Equal(info.ProbedAt) {
		t.Error("第二次识别应命中缓存")
	}
	// force 会重新识别（时间戳刷新）
	forced := mod.USBType(true)
	if forced.ProbedAt.Before(info.ProbedAt) {
		t.Error("force 应重新识别")
	}
}

// 没拨过号时 DialStatus 要是干净的 idle（页面初次进入就是这么显示的）
func TestDialStatusIdle(t *testing.T) {
	mod := &Modem{ID: 3}
	st := mod.DialStatus()
	if st.State != dial.DialIdle || st.StateText != "未拨号" || st.ElapsedSec != 0 {
		t.Errorf("初始状态应为 idle/未拨号，得到 %+v", st)
	}
}

// 周期推送里的那次网络查询顺手校准拨号状态（不额外发 AT）
func TestSyncDialState(t *testing.T) {
	mod := &Modem{ID: 4}
	// 没拨号：网络数据再多也不动拨号状态
	mod.syncDialState(&NetworkStatus{APN: "3gnet", IPv4: "10.0.0.1"})
	if st := mod.DialStatus(); st.State != dial.DialIdle || st.APN != "" {
		t.Errorf("未拨号时不该同步网络数据，得到 %+v", st)
	}

	// 已连接：APN / 本机地址跟着网络查询走
	mod.dialState = dial.DialState{ModemID: 4, State: dial.DialConnected, CID: 1, APN: "ctnet", IPv4: "1.2.3.4"}
	mod.syncDialState(&NetworkStatus{APN: "ctnet2", IPv4: "5.6.7.8", IPv6: "240e::1"})
	st := mod.DialStatus()
	if st.APN != "ctnet2" || st.IPv4 != "5.6.7.8" || st.IPv6 != "240e::1" {
		t.Errorf("已连接时应同步 APN/地址，得到 %+v", st)
	}
	// 模组没上报的字段不能被空值冲掉
	mod.syncDialState(&NetworkStatus{APN: "ctnet2"})
	if st := mod.DialStatus(); st.IPv4 != "5.6.7.8" {
		t.Errorf("空字段不该覆盖已有值，得到 %+v", st)
	}
}

// QMI 的「拨号」必须**不发** AT+CGACT=1：模组只有一个数据承载，模组侧一激活，
// 主机侧厂商拨号器的数据呼叫就会被拒（实测 RM502Q-GL：QMUXError 0xe）。
// 这里用假模组驱动一遍 Dial，断言"走的是主机侧分派"且发出的指令里没有激活 PDP。
func TestDialQMIDoesNotActivateModemSide(t *testing.T) {
	initTestLogger()
	withATTimeout(t)

	script := func(cmd string) []string {
		switch {
		case strings.HasPrefix(cmd, `AT+QCFG="usbnet"`):
			return []string{cmd, `+QCFG: "usbnet",0`, "OK"}
		case strings.HasPrefix(cmd, "AT+CGDCONT?"):
			return []string{cmd, `+CGDCONT: 1,"IPV4V6","ctnet","0.0.0.0",0,0`, "OK"}
		case strings.HasPrefix(cmd, "AT+CGACT?"):
			return []string{cmd, "+CGACT: 1,0", "OK"}
		}
		return []string{cmd, "OK"}
	}
	// ID 取一个测试专用的值：hostDialCtxReleased 是包级状态，别和别的用例串
	mod := &Modem{ID: 9001, cmdImpl: &quectel.QuectelCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	defer close(mod.writeChan)
	sent := fakeATServe(mod, script)

	st, err := mod.Dial(dial.DialRequest{})
	if err != nil {
		t.Fatalf("QMI 拨号不该报错（本机不托管拨号器时应停在「拨号中」）：%v", err)
	}
	for _, c := range drainSent(sent) {
		if strings.HasPrefix(c, "AT+CGACT=1") {
			t.Fatalf("QMI 不该在模组侧激活 PDP（会占住唯一数据承载）：%q", c)
		}
	}
	if st.Kind != "qmi" {
		t.Fatalf("USB 类型应识别为 qmi，得到 %q", st.Kind)
	}
	if st.State != dial.DialDialing {
		t.Errorf("本机不托管主机侧拨号器时状态应为「拨号中」，得到 %q", st.State)
	}
	var names []string
	for _, s := range st.Steps {
		names = append(names, s.Name)
	}
	joined := strings.Join(names, "|")
	if !strings.Contains(joined, "释放模组侧数据承载") {
		t.Errorf("QMI 拨号步骤应包含「释放模组侧数据承载」，得到 %v", names)
	}
	if strings.Contains(joined, "激活 PDP") {
		t.Errorf("QMI 拨号步骤不该出现模组侧「激活 PDP」，得到 %v", names)
	}
}

// qmiFakeScript 「假模组」脚本：USB 类型报 QMI（usbnet=0），带一条 IPV4V6 的数据上下文。
func qmiFakeScript() func(string) []string {
	return func(cmd string) []string {
		switch {
		case strings.HasPrefix(cmd, `AT+QCFG="usbnet"`):
			return []string{cmd, `+QCFG: "usbnet",0`, "OK"}
		case strings.HasPrefix(cmd, "AT+CGDCONT?"):
			return []string{cmd, `+CGDCONT: 1,"IPV4V6","ctnet","0.0.0.0",0,0`, "OK"}
		case strings.HasPrefix(cmd, "AT+CGACT?"):
			return []string{cmd, "+CGACT: 1,0", "OK"}
		}
		return []string{cmd, "OK"}
	}
}

// newQMIFakeModem 造一台 QMI 假模组并挂进管理器（ID 用测试专用值，别和别的用例串）。
func newQMIFakeModem(t *testing.T, mgr *ModemManager, id int) chan string {
	t.Helper()
	mod := &Modem{ID: id, Vendor: VendorQuectel, cmdImpl: &quectel.QuectelCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	t.Cleanup(func() { close(mod.writeChan) })
	mgr.modems[id] = mod
	return fakeATServe(mod, qmiFakeScript())
}

// QMI（主机侧拨号）时「可写参数」写 APN **不能**顺手激活 PDP：模组只有一个数据承载，
// 模组侧一激活，主机侧拨号器的数据呼叫就被拒（实测 QMUXError 0xe），IPv6 那条 WDS 会话
// 尤其起不来 —— 承载要留给拨号器。这里断言这条写配置的路上没有 AT+CGACT=1。
func TestSetAPNQMIDoesNotActivatePDP(t *testing.T) {
	initTestLogger()
	withATTimeout(t)

	mgr := NewModemManager()
	sent := newQMIFakeModem(t, mgr, 9101)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/params/apn",
		strings.NewReader(`{"modem_id":9101,"cid":1,"pdp_type":"IPV4V6","apn":"ctnet"}`))
	apiSetAPN(mgr)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("http status = %d, body=%s", rec.Code, rec.Body.String())
	}
	for _, c := range drainSent(sent) {
		if strings.HasPrefix(c, "AT+CGACT=1") {
			t.Fatalf("QMI 写 APN 不该激活 PDP（会占住唯一数据承载）：%q", c)
		}
	}
	if !strings.Contains(rec.Body.String(), "不激活 PDP") {
		t.Errorf("响应该说明「不激活 PDP」，得到 %s", rec.Body.String())
	}

	// 显式传 activate=true 也一样：QMI 一律不激活（承载归拨号器）
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/params/apn",
		strings.NewReader(`{"modem_id":9101,"cid":1,"pdp_type":"IPV4V6","apn":"ctnet","activate":true}`))
	apiSetAPN(mgr)(rec2, req2)
	for _, c := range drainSent(sent) {
		if strings.HasPrefix(c, "AT+CGACT=1") {
			t.Fatalf("QMI 显式 activate=true 也不该激活 PDP：%q", c)
		}
	}
}

// 配额断网（[配额] 页 ActionBlock）在 QMI 上也**不能**去动 AT+CGACT：数据面在主机侧
// 拨号器手里，模组侧断不掉；而恢复时的 AT+CGACT=1 反而会占住唯一承载，把拨号器（含 IPv6）
// 顶掉。QMI 上断网 = 停掉拨号器；恢复交给组网循环。
func TestSetDataBlockedQMIDoesNotTouchAT(t *testing.T) {
	initTestLogger()
	withATTimeout(t)

	mod := &Modem{ID: 9201, Vendor: VendorQuectel, cmdImpl: &quectel.QuectelCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	defer close(mod.writeChan)
	sent := fakeATServe(mod, qmiFakeScript())

	if err := mod.SetDataBlocked(true); err != nil {
		t.Fatalf("QMI 配额断网不该报错：%v", err)
	}
	if err := mod.SetDataBlocked(false); err != nil {
		t.Fatalf("QMI 配额恢复不该报错：%v", err)
	}
	for _, c := range drainSent(sent) {
		if strings.HasPrefix(c, "AT+CGACT") {
			t.Fatalf("QMI 走主机侧拨号器，配额断/恢复不该动 AT+CGACT：%q", c)
		}
	}
}

// 配额断网期间，组网循环不该再把主机侧拨号器拉起来（数据面归它，拉起就等于没断网）。
func TestHostDialStopReasonQuotaBlocked(t *testing.T) {
	initTestLogger()

	oldHost := config.Cfg.Net.HostDial
	config.Cfg.Net.HostDial = true
	defer func() { config.Cfg.Net.HostDial = oldHost }()

	mod := &Modem{ID: 9202, Vendor: VendorQuectel}
	mod.isRunning = true
	quotaUpdate(mod, func(s *State) { s.Action = ActionBlock; s.Blocked = true })
	defer quotaUpdate(mod, func(s *State) { s.Blocked = false })

	if reason := hostDialStopReason(mod, nil, "wwan0"); !strings.Contains(reason, "配额") {
		t.Fatalf("配额断网期间不该让拨号器跑，得到原因 %q", reason)
	}

	quotaUpdate(mod, func(s *State) { s.Blocked = false })
	if reason := hostDialStopReason(mod, nil, "wwan0"); reason != "" {
		t.Fatalf("解除限制后该让拨号器跑，得到原因 %q", reason)
	}
}

// QMI 手动「激活 PDP」要被拒（只允许停用），而且**不发** AT+CGACT=1 —— 不然会占住承载，
// 主机侧拨号器（IPv6 也靠它）随后被拒。
func TestSetPDPActiveQMIRefused(t *testing.T) {
	initTestLogger()
	withATTimeout(t)

	mgr := NewModemManager()
	sent := newQMIFakeModem(t, mgr, 9102)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/params/pdp",
		strings.NewReader(`{"modem_id":9102,"cid":1,"active":true}`))
	apiSetPDPActive(mgr)(rec, req)

	if !strings.Contains(rec.Body.String(), `"code":-1`) {
		t.Errorf("QMI 手动激活应被拒（code=-1），得到 %s", rec.Body.String())
	}
	for _, c := range drainSent(sent) {
		if strings.HasPrefix(c, "AT+CGACT=1") {
			t.Fatalf("QMI 被拒时不该发出 AT+CGACT=1：%q", c)
		}
	}
}
