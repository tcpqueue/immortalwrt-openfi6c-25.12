package kernel

// 主机侧拨号器（quectel-CM）的看护逻辑单测。
//
// ⚠️ 只测"纯逻辑"那几块（参数拼装 / 退避 / 该不该跑 / 找可执行文件）：
// SyncHostDial 真正拉起进程那条路要有 Linux+OpenWrt 才走（AutoWANEnabled），
// 在开发机上跑不到 —— 真机验证见 README「QMI 模式」那一节。

import (
	"errors"
	"modemd/module/config"
	"modemd/module/dial"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostDialArgs(t *testing.T) {
	cases := []struct {
		dev, apn string
		v6       bool
		pdn      int
		want     string
	}{
		{"wwan0", "3gnet", false, 0, "-i wwan0 -4 -s 3gnet"},
		{"wwan0", "3gnet", true, 0, "-i wwan0 -4 -6 -s 3gnet"},
		{"wwan1", "", false, 0, "-i wwan1 -4"},
		{"wwan1", "   ", false, 0, "-i wwan1 -4"}, // 只有空白的 APN 不传
		// 网卡名还认不出来时不传 -i（传空值会变成 `-i -4`，那是把 -4 当网卡名）
		{"", "3gnet", true, 0, "-4 -6 -s 3gnet"},
		// QMAP：显式 -n <mux序号>（用户实测移远的正确用法）
		{"wwan0", "3gnet", true, 1, "-i wwan0 -4 -6 -s 3gnet -n 1"},
	}
	for _, c := range cases {
		if got := strings.Join(hostDialArgs(c.dev, c.apn, c.v6, c.pdn), " "); got != c.want {
			t.Errorf("hostDialArgs(%q,%q,%v,%d) = %q, want %q", c.dev, c.apn, c.v6, c.pdn, got, c.want)
		}
	}
	// 参数里出现的第一个 -i 后面必须是网卡名（startHostDial 的路由收尾靠它反推）
	args := hostDialArgs("wwan0", "3gnet", true, 1)
	if got := lastArgAfter(args, "-i"); got != "wwan0" {
		t.Errorf("lastArgAfter(-i) = %q, want wwan0", got)
	}
	if got := lastArgAfter(args, "-n"); got != "1" {
		t.Errorf("lastArgAfter(-n) = %q, want 1", got)
	}
	if lastArgAfter([]string{"-4"}, "-i") != "" {
		t.Error("没有 -i 时应该返回空串")
	}
}

// TestHostDialPDN 从数据网卡名反推 `-n` 的 PDN：厂家 qmi_wwan_q 的 mux 口是 `<父名>_<序号>`，
// 那个序号就是 quectel-CM / fibocom-dial / meig-cm 的 `-n`。
func TestHostDialPDN(t *testing.T) {
	yes := map[string]int{
		"wwan0_1": 1, "wwan1_2": 2, "WWAN0_1": 1, "wwan0_10": 10,
	}
	for name, want := range yes {
		if got := hostDialPDN(name); got != want {
			t.Errorf("hostDialPDN(%q) = %d, want %d", name, got, want)
		}
	}
	// 认不出的（父口 / 主线 qmimux / 空）→ 0（不传 -n）
	for _, name := range []string{"wwan0", "qmimux0", "usb0", "", "wwan0_", "wwan0_x", "eth1"} {
		if got := hostDialPDN(name); got != 0 {
			t.Errorf("hostDialPDN(%q) = %d, want 0", name, got)
		}
	}
}

// TestHostDialPDNForQMAP qmap_mode=1 的驱动（FM160 / qmi_wwan_f 实测 2026-10-09）
// 第一次拨号时 mux 口还不存在，名字里没有序号 → 必须按默认承载补 `-n 1`，
// 否则拨号器不 add_mux、父口永远 NO-CARRIER、udhcpc 一直 "Network is down"。
func TestHostDialPDNForQMAP(t *testing.T) {
	orig := hostDialQMAPModeOn
	defer func() { hostDialQMAPModeOn = orig }()

	// 非 QMAP 驱动：行为与改动前一模一样（mux 名能给就给，给不出来就不传 -n）
	hostDialQMAPModeOn = func(string) bool { return false }
	if got := hostDialPDNFor("wwan0", "wwan0_2"); got != 2 {
		t.Errorf("mux 名可反推时 hostDialPDNFor = %d, want 2", got)
	}
	if got := hostDialPDNFor("wwan0", "wwan0"); got != 0 {
		t.Errorf("非 QMAP 且反推不到时 hostDialPDNFor = %d, want 0", got)
	}

	// QMAP 驱动：反推不到就补 1；能反推到时仍以网卡名为准
	hostDialQMAPModeOn = func(string) bool { return true }
	if got := hostDialPDNFor("wwan0", "wwan0"); got != 1 {
		t.Errorf("QMAP 且反推不到时 hostDialPDNFor = %d, want 1", got)
	}
	if got := hostDialPDNFor("wwan0", "wwan0_3"); got != 3 {
		t.Errorf("QMAP 且 mux 名可反推时 hostDialPDNFor = %d, want 3", got)
	}

	// 平台没注入（非 Linux / 网卡名空）→ 不能凭空补 -n
	hostDialQMAPModeOn = nil
	if got := hostDialPDNFor("wwan0", "wwan0"); got != 0 {
		t.Errorf("没有 qmap 钩子时 hostDialPDNFor = %d, want 0", got)
	}
	// PCIe 模组：`-i` 传空串，钩子必须自己挡掉空网卡名（这里用 nil 之外的实现验证）
	hostDialQMAPModeOn = func(dev string) bool { return strings.TrimSpace(dev) != "" }
	if got := hostDialPDNFor("", ""); got != 0 {
		t.Errorf("PCIe（网卡名空）时 hostDialPDNFor = %d, want 0（不能凭空传 -n）", got)
	}

	// 配置覆盖（[net] host_dial_pdn）：>0 强制、<0 一律不传，优先于上面全部自动判断
	origCfg := config.Cfg.Net.HostDialPDN
	defer func() { config.Cfg.Net.HostDialPDN = origCfg }()

	hostDialQMAPModeOn = func(string) bool { return true }
	config.Cfg.Net.HostDialPDN = 3
	if got := hostDialPDNFor("wwan0", "wwan0"); got != 3 {
		t.Errorf("host_dial_pdn=3 时 hostDialPDNFor = %d, want 3", got)
	}
	if got := hostDialPDNFor("wwan0", "wwan0_1"); got != 3 {
		t.Errorf("host_dial_pdn=3 要压过网卡名反推，hostDialPDNFor = %d, want 3", got)
	}
	config.Cfg.Net.HostDialPDN = -1
	if got := hostDialPDNFor("wwan0", "wwan0_1"); got != 0 {
		t.Errorf("host_dial_pdn=-1 时不该传 -n，hostDialPDNFor = %d, want 0", got)
	}
	config.Cfg.Net.HostDialPDN = 0
}

// TestHostDialArgsForSimcom SIMCom 的 simcom-cm：源码里的 getopt（third_party/simcom-cm/src/main.c）
// 认 `-s -p -n -f -i -v -l -6`，**唯独不认 `-4`** —— 那个会落到 default 分支打印 usage 并立刻退出。
// 早先的实测（`-s 3GNET -4 -6` → `Usage: …`）就是被 `-4` 打发的，不是 `-6`。
//
//	-6  →  要 IPv6 承载（IsDualIPSupported |= 1<<IpFamilyV6，去建 QMUX_TYPE_WDS_IPV6 会话）
//
// 所以：IPv6 开关开着且模组报了 v6 时要带上 -6，其余情况只给 APN；`-4` 与 `-i`（网卡名）都不传。
func TestHostDialArgsForSimcom(t *testing.T) {
	cases := []struct {
		apn  string
		v6   bool
		want string
	}{
		{"3GNET", false, "-s 3GNET"},
		{"3GNET", true, "-s 3GNET -6"}, // -6 = 要 v6 承载（IsDualIPSupported）；它不认 -4，别传
		{"", false, ""},
		{"   ", true, "-6"},
	}
	for _, c := range cases {
		got := strings.Join(hostDialArgsFor(VendorSimcom, "wwan0", c.apn, c.v6, 0), " ")
		if got != c.want {
			t.Errorf("hostDialArgsFor(simcom,%q,v6=%v) = %q, want %q", c.apn, c.v6, got, c.want)
		}
	}
	for _, bad := range []string{"-4", "wwan0"} {
		got := strings.Join(hostDialArgsFor(VendorSimcom, "wwan0", "3GNET", true, 0), " ")
		if strings.Contains(got, bad) {
			t.Errorf("simcom-cm 的参数里不该出现 %q（-4 会直接 usage 退出）：%q", bad, got)
		}
	}
	// 移远/广和通/美格同源：`-4 -6` 都在，QMAP 时按数据网卡名带上 `-n <mux序号>`。
	// （用户实测移远的正确用法：quectel-CM -i wwan0 -4 -6 -s <apn> -n 1）
	if got := strings.Join(hostDialArgsFor(VendorQuectel, "wwan0", "3gnet", true, 1), " "); got != "-i wwan0 -4 -6 -s 3gnet -n 1" {
		t.Errorf("移远参数 = %q, want %q", got, "-i wwan0 -4 -6 -s 3gnet -n 1")
	}
	if got := strings.Join(hostDialArgsFor(VendorFibocom, "wwan0", "3gnet", true, 1), " "); got != "-i wwan0 -4 -6 -s 3gnet -n 1" {
		t.Errorf("广和通参数 = %q, want %q", got, "-i wwan0 -4 -6 -s 3gnet -n 1")
	}
	if got := strings.Join(hostDialArgsFor(VendorUnknown, "wwan0", "3gnet", false, 0), " "); got != "-i wwan0 -4 -s 3gnet" {
		t.Errorf("未识别厂商参数 = %q, want %q", got, "-i wwan0 -4 -s 3gnet")
	}
}

func TestHostDialBackoff(t *testing.T) {
	now := time.Now()
	if d := hostDialBackoff(time.Time{}, 0, now); d != 0 {
		t.Errorf("从没跑过 → 不该退避，得到 %v", d)
	}
	if d := hostDialBackoff(now.Add(-time.Second), 0, now); d <= 0 || d > 10*time.Second {
		t.Errorf("刚挂 1 秒 → 应还剩 <10s 退避，得到 %v", d)
	}
	if d := hostDialBackoff(now.Add(-time.Hour), 0, now); d != 0 {
		t.Errorf("挂了一小时 → 可以直接重试，得到 %v", d)
	}
	// 连续"起来就挂"要翻倍退避，但有上限
	if d := hostDialBackoff(now, 1, now); d < 19*time.Second || d > 21*time.Second {
		t.Errorf("第 1 次快挂 → 约 20s 退避，得到 %v", d)
	}
	if d := hostDialBackoff(now, 2, now); d < 39*time.Second || d > 41*time.Second {
		t.Errorf("第 2 次快挂 → 约 40s 退避，得到 %v", d)
	}
	if d := hostDialBackoff(now, 9, now); d != hostDialMaxRetryDelay {
		t.Errorf("退避要有上限 %v，得到 %v", hostDialMaxRetryDelay, d)
	}
}

// TestHostDialStopReason 该不该让拨号器跑：只有"QMI + 模组在 + 没被手动断开 + 认出网卡"才跑。
func TestHostDialStopReason(t *testing.T) {
	orig := config.Cfg.Net.HostDial
	defer func() { config.Cfg.Net.HostDial = orig }()
	config.Cfg.Net.HostDial = true

	mod := &Modem{ID: 1}
	mod.isRunning = true
	st := &dial.DialState{Kind: "qmi"}

	if got := hostDialStopReason(mod, st, "wwan0"); got != "" {
		t.Errorf("模组在 + QMI + 有网卡 → 该跑，却给了：%q", got)
	}
	if got := hostDialStopReason(mod, st, ""); !strings.Contains(got, "网卡") {
		t.Errorf("还没认出网卡 → 不该跑，却给了：%q", got)
	}
	if got := hostDialStopReason(mod, &dial.DialState{Kind: "qmi", ManualStop: true}, "wwan0"); !strings.Contains(got, "手动") {
		t.Errorf("用户手动断开 → 不该跑，却给了：%q", got)
	}
	off := &Modem{ID: 2}
	if got := hostDialStopReason(off, st, "wwan0"); !strings.Contains(got, "不在") {
		t.Errorf("模组不在了 → 不该跑，却给了：%q", got)
	}
	config.Cfg.Net.HostDial = false
	if got := hostDialStopReason(mod, st, "wwan0"); !strings.Contains(got, "host_dial") {
		t.Errorf("配置关掉了 → 不该跑，却给了：%q", got)
	}
	config.Cfg.Net.HostDial = true
	// 模组侧没连接**不是**理由：QMI 的真拨号本来就归拨号器（AT 侧只是模组那一端就绪）
	if got := hostDialStopReason(mod, &dial.DialState{Kind: "qmi", State: dial.DialIdle}, "wwan0"); got != "" {
		t.Errorf("模组侧未连接不该拦住主机侧拨号器，却给了：%q", got)
	}
}

// TestHostDialBinaryFor 找拨号器：配置优先（且要存在），其次**按厂商**的候选路径。
//
// TestHostDialBinaryFor 拨号器按厂商挑（见 hostDialSpecFor）。
//
// 重点：每家模组必须找**自家**那份，不能拿 quectel-CM 顶上
// （SIMCom 真机实测拨不通：QMIType 11 has no clientID / QMUXError 0xe）。
func TestHostDialBinaryFor(t *testing.T) {
	// 候选路径要按厂商分：
	for _, p := range hostDialBinPathsFor(VendorSimcom) {
		if !strings.HasSuffix(p, "/simcom-cm") {
			t.Errorf("SIMCom 模组的拨号器候选里出现了 %q（只该有 simcom-cm）", p)
		}
	}
	// 广和通 / 美格各自一家一份（源码 vendored 在 third_party，随包打到 /usr/lib/modemd/）
	for v, want := range map[ModemVendor]string{
		VendorFibocom: "fibocom-dial",
		VendorMeig:    "meig-cm",
	} {
		paths := hostDialBinPathsFor(v)
		if len(paths) != 3 || !strings.HasSuffix(paths[0], "/usr/lib/modemd/"+want) {
			t.Errorf("%s 的候选路径不对：%v（首选应是 /usr/lib/modemd/%s）", v, paths, want)
		}
		for _, p := range paths {
			if !strings.HasSuffix(p, "/"+want) {
				t.Errorf("%s 的候选里出现了 %q（只该有 %s）", v, p, want)
			}
		}
		if got := hostDialNameFor(v); got != want {
			t.Errorf("hostDialNameFor(%s) = %q, want %q", v, got, want)
		}
		if h := hostDialHintFor(v); !strings.Contains(h, want) {
			t.Errorf("%s 的提示要说清用 %s：%q", v, want, h)
		}
	}
	for _, p := range hostDialBinPathsFor(VendorQuectel) {
		if !strings.HasSuffix(p, "/quectel-CM") {
			t.Errorf("移远模组的拨号器候选里出现了 %q（只该有 quectel-CM）", p)
		}
	}
	// 未识别厂商按 quectel-CM 走（不会去猜 simcom）
	for _, p := range hostDialBinPathsFor(VendorUnknown) {
		if !strings.HasSuffix(p, "/quectel-CM") {
			t.Errorf("未识别厂商的候选里出现了 %q（应按 quectel-CM 走）", p)
		}
	}
	// 名字与提示文案要说对家
	if hostDialNameFor(VendorSimcom) != "simcom-cm" || hostDialNameFor(VendorQuectel) != "quectel-CM" {
		t.Error("拨号器名字按厂商取错了")
	}
	if h := hostDialHintFor(VendorSimcom); !strings.Contains(h, "simcom-cm") {
		t.Errorf("SIMCom 的提示要说清用 simcom-cm：%q", h)
	}

	// 配置指定了路径 → 以它为准
	dir := t.TempDir()
	mine := filepath.Join(dir, "my-cm")
	if err := os.WriteFile(mine, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	origBin := config.Cfg.Net.HostDialBin
	defer func() { config.Cfg.Net.HostDialBin = origBin }()

	config.Cfg.Net.HostDialBin = mine
	for _, v := range []ModemVendor{VendorSimcom, VendorQuectel, VendorUnknown} {
		if got, err := hostDialBinaryFor(v); err != nil || got != mine {
			t.Errorf("配置指定时应以配置为准（vendor=%s），得到 %q（err=%v）", v, got, err)
		}
	}
	// 配置指错了要明确报错，别静默退回别的
	config.Cfg.Net.HostDialBin = filepath.Join(dir, "配错了")
	if _, err := hostDialBinaryFor(VendorQuectel); err == nil ||
		!strings.Contains(err.Error(), "host_dial_bin") {
		t.Errorf("配置指定的路径不存在时要明确报错，得到 %v", err)
	}

	// 没配置、候选也都不存在时：错误里要有"找过哪儿 + 怎么办"（页面直接显示这句）
	config.Cfg.Net.HostDialBin = ""
	for _, v := range []ModemVendor{VendorSimcom, VendorQuectel} {
		exists := false
		for _, p := range hostDialBinPathsFor(v) {
			if _, err := os.Stat(p); err == nil {
				exists = true
			}
		}
		if exists {
			continue // 这台机器上真装了（开发机/路由器），跳过这一段
		}
		_, err := hostDialBinaryFor(v)
		if err == nil {
			t.Fatalf("候选都不存在时应该报错（vendor=%s）", v)
		}
		if !strings.Contains(err.Error(), hostDialNameFor(v)) {
			t.Errorf("错误里要提到 %s：%v", hostDialNameFor(v), err)
		}
		if !strings.Contains(err.Error(), hostDialBinPathsFor(v)[0]) {
			t.Errorf("错误里要写清找过哪些路径：%v", err)
		}
	}
}

// TestHostDialKindVendor MBIM 一律用 quectel-CM —— **只有它实现了 MBIM**。
//
// 依据（第三方源码实测，2026-10-13）：fibocom-dial / simcom-cm 里**压根没有** MBIM 那套
// （连 get_driver_type 都没有）；meig-cm 带了 mbim-cm.c 但没有任何地方把它接上。
// 而 quectel-CM 认 MBIM 靠**标准 USB CDC 描述符**（与厂商无关）⇒
// 拿它去拨广和通 / 美格的 MBIM 模组是对路的。
func TestHostDialKindVendor(t *testing.T) {
	if got := hostDialKindVendor("mbim", VendorFibocom); got != VendorQuectel {
		t.Errorf("MBIM + 广和通 → 应该用 quectel-CM（VendorQuectel），得到 %v", got)
	}
	for _, k := range []string{"MBIM", " mbim ", "Mbim"} {
		if got := hostDialKindVendor(k, VendorMeig); got != VendorQuectel {
			t.Errorf("%q + 美格 → 应该用 quectel-CM，得到 %v", k, got)
		}
	}
	// 其它类型：老老实实用自家那份（别把 SIMCom 那种"拿 quectel-CM 顶上拨不通"的坑再踩一遍）
	for _, c := range []struct {
		kind   string
		vendor ModemVendor
	}{
		{"qmi", VendorFibocom},
		{"qmi", VendorMeig},
		{"qmi", VendorSimcom},
		{"ecm", VendorFibocom},
		{"ncm", VendorSimcom},
		{"", VendorMeig},
	} {
		if got := hostDialKindVendor(c.kind, c.vendor); got != c.vendor {
			t.Errorf("kind=%q vendor=%v → 应该保持原厂商，得到 %v", c.kind, c.vendor, got)
		}
	}
	// 提示文案里的名字也要跟着走（否则 MBIM 模组会被说成在跑 fibocom-dial）
	if got := hostDialNameFor(hostDialKindVendor("mbim", VendorFibocom)); got != "quectel-CM" {
		t.Errorf("MBIM 的拨号器名应该是 quectel-CM，得到 %q", got)
	}
	if got := hostDialModeLabel("mbim"); got != "MBIM" {
		t.Errorf("hostDialModeLabel(mbim) = %q，想要 MBIM", got)
	}
	if got := hostDialModeLabel("qmi"); got != "QMI" {
		t.Errorf("hostDialModeLabel(qmi) = %q，想要 QMI", got)
	}
}

// TestHostDialPreStart 前置动作（把数据网卡切成 raw IP）失败时必须**拦住**拨号器。
//
// 真机踩过：模组重新枚举后 raw_ip 复位成 N，写 Y 时内核返回 EBUSY；旧代码只记一条
// warn 就照拉 simcom-cm，拨号器一直报 "PS: Detached" 空转十几分钟拿不到地址，
// 页面还显示"拨号器正在跑"。
//
// ⚠️ 这一步**不是 SIMCom 专属**：quectel-CM / fibocom-dial / meig-cm 的 QMIThread 里
// 同样把会话设成裸 IP（各自的 qmap_bridge_mode.c 只写驱动私有的 `qmi/rawip`，
// 主线的 qmi_wwan 暴露的是 `qmi/raw_ip`），所以四家都要做。
func TestHostDialPreStart(t *testing.T) {
	orig := hostDialRawIPFix
	defer func() { hostDialRawIPFix = orig }()

	simcom := &Modem{Vendor: VendorSimcom}

	// 没注入平台实现（非 Linux）：不拦
	hostDialRawIPFix = nil
	if got := hostDialPreStart(simcom, "wwan0"); got != "" {
		t.Errorf("没有平台实现时不该拦：%q", got)
	}

	// 各厂商都要走这一步（都是 QMI 拨号器，都要裸 IP）
	touched := 0
	hostDialRawIPFix = func(string) error { touched++; return nil }
	for _, v := range []ModemVendor{VendorQuectel, VendorSimcom, VendorFibocom, VendorMeig} {
		m := &Modem{Vendor: v}
		if got := hostDialPreStart(m, "wwan0"); got != "" {
			t.Errorf("%s 切换成功应放行：%q", v, got)
		}
	}
	if touched != 4 {
		t.Errorf("四家都要做 raw_ip 前置，实际只调了 %d 次", touched)
	}
	// nil 模组：不拦（不知道是谁就什么都不做）
	if got := hostDialPreStart(nil, "wwan0"); got != "" || touched != 4 {
		t.Errorf("modem 为 nil 时不该拦（got=%q touched=%d）", got, touched)
	}

	// 切换失败：拦住，且提示要能看懂（网卡名 + 拨号器名 + 会重试）
	hostDialRawIPFix = func(string) error { return errors.New("device or resource busy") }
	got := hostDialPreStart(simcom, "wwan0")
	if got == "" {
		t.Fatal("raw_ip 切不过去时必须拦住拨号器")
	}
	for _, want := range []string{"wwan0", "raw_ip", "device or resource busy", hostDialNameFor(VendorSimcom), "重试"} {
		if !strings.Contains(got, want) {
			t.Errorf("提示里缺 %q：%q", want, got)
		}
	}
}

// TestHostDialStuckHint 拨号器空转（跑着但网卡一直没地址）时，SIMCom 要给出 EPS 注册这句提示。
//
// 真机实测：纯 5G SA 下 +C5GREG: 0,1、+CGATT: 1，但 +CEREG: 0,0，
// simcom-cm 就一直 requestRegistrationState2 "PS: Detached"，不拨也不报错。
func TestHostDialStuckHint(t *testing.T) {
	origHas := hostDialHasAddr
	defer func() { hostDialHasAddr = origHas }()

	simcom := &Modem{Vendor: VendorSimcom}
	run := func(age time.Duration) *hostDialRun {
		return &hostDialRun{running: true, started: time.Now().Add(-age),
			bin: "/usr/lib/modemd/simcom-cm", dev: "wwan0", args: []string{"-s", "3GNET"}}
	}

	// 刚起来：不给提示（还没到该怀疑的时候）
	hostDialHasAddr = func(string) bool { return false }
	if got := hostDialStuckHint(simcom, run(time.Second), "wwan0"); got != "" {
		t.Errorf("刚拉起不该提示：%q", got)
	}

	// 跑很久 + 没地址：提示里要有"怎么改"的可操作信息
	got := hostDialStuckHint(simcom, run(10*time.Minute), "wwan0")
	for _, want := range []string{"EPS", "+CEREG", "AT+CNMP=38", "109"} {
		if !strings.Contains(got, want) {
			t.Errorf("提示里缺 %q：%q", want, got)
		}
	}

	// 已经拿到地址：不提示
	hostDialHasAddr = func(string) bool { return true }
	if got := hostDialStuckHint(simcom, run(10*time.Minute), "wwan0"); got != "" {
		t.Errorf("拿到地址了不该提示：%q", got)
	}

	hostDialHasAddr = func(string) bool { return false }
	// 别的厂商：只有在**抓到过"模组拒绝数据呼叫"（QMUXError）**时才提示（见下），否则不提示
	if got := hostDialStuckHint(&Modem{Vendor: VendorQuectel}, run(10*time.Minute), "wwan0"); got != "" {
		t.Errorf("非 SIMCom 且没有 QMUXError 时不该提示：%q", got)
	}
	// 没在跑 / 模组没了：不提示
	if got := hostDialStuckHint(simcom, &hostDialRun{}, "wwan0"); got != "" {
		t.Errorf("不在跑不该提示：%q", got)
	}
	if got := hostDialStuckHint(nil, run(10*time.Minute), "wwan0"); got != "" {
		t.Errorf("modem 为 nil 不该提示：%q", got)
	}
}

// TestHostDialStuckHintQMUXError 另一类"跑着但没有地址"：**模组的 QMI 数据服务卡住了**。
//
// 真机实测（RM502Q-GL + 厂家 qmi_wwan_q，2026-10-03）：拨号器每轮都被拒 ——
//
//	requestSetupDataCall QMUXResult = 0x1, QMUXError = 0xe
//	call_end_reason is 1 / type is 2 / verbose is 241（IPv6 那条是 210）
//
// 而同一时刻 AT 侧拨号完全正常（`AT+CGACT=1,3` 立刻拿到真实地址）、数据上下文全停也没用，
// 只有 `AT+CFUN=1,1` 重启模组才恢复 —— 所以提示必须把原话和处置办法都写出来。
func TestHostDialStuckHintQMUXError(t *testing.T) {
	origHas := hostDialHasAddr
	defer func() { hostDialHasAddr = origHas }()
	hostDialHasAddr = func(string) bool { return false }

	run := func() *hostDialRun {
		return &hostDialRun{running: true, started: time.Now().Add(-10 * time.Minute),
			bin: "/usr/lib/modemd/quectel-CM", dev: "wwan0",
			args: []string{"-i", "wwan0", "-4", "-6", "-s", "ctwap"}}
	}
	mod := &Modem{Vendor: VendorQuectel}

	// 抓到过 QMUXError（还新鲜）→ 提示里要有原话 + 处置办法
	r := run()
	r.qmuxErr, r.qmuxErrAt = "requestSetupDataCall QMUXResult = 0x1, QMUXError = 0xe", time.Now()
	got := hostDialStuckHint(mod, r, "wwan0")
	for _, want := range []string{"QMUXError", "重启模组", "AT+CFUN=1,1"} {
		if !strings.Contains(got, want) {
			t.Errorf("提示里缺 %q：%q", want, got)
		}
	}

	// 那个错误已经过时（>3 分钟）→ 不再拿它当原因（模组可能早恢复了）
	r = run()
	r.qmuxErr, r.qmuxErrAt = "QMUXError = 0xe", time.Now().Add(-10*time.Minute)
	if got := hostDialStuckHint(mod, r, "wwan0"); got != "" {
		t.Errorf("陈旧的 QMUXError 不该再提示：%q", got)
	}
}

// TestHostDialStopWaitIsGentle 强杀拨号器会把模组的 QMI 数据服务留在半开状态
// （实测 RM502Q：之后每轮 StartNetworkInterface 都被拒 `QMUXError 0xe`，只能重启模组恢复）——
// 所以停它时要留足时间让它自己收尾（发完 StopNetworkInterface / 停掉 PDP）。
func TestHostDialStopWaitIsGentle(t *testing.T) {
	if hostDialStopWait < 10*time.Second {
		t.Errorf("hostDialStopWait = %v 太短：拨号器来不及收尾，模组的 QMI 会话会半开着（见其注释）",
			hostDialStopWait)
	}
}

// TestHostDialRestartDue 掉线自愈的判定：拨号器在跑、网卡上却一直没有地址 ⇒ 重开一次。
//
// 用户 2026-10-09 的口径：“qmi 模式掉线后 就重新关闭软件 在重启执行 拨号软件就行啊”。
// 这里锁住三件事：① 刚拉起来不许立刻杀（拨号本身要时间）；② 两次重启之间有间隔；
// ③ 重开很多次还没好的话别再无限重开（没信号时别打转）。
func TestHostDialRestartDue(t *testing.T) {
	now := time.Now()

	// ① 刚起来 10 秒：不重启（正常拨号要几秒到几十秒）
	if due, st := hostDialRestartDue(now.Add(-10*time.Second), hostDialRestartState{}, now); due {
		t.Errorf("刚拉起 10 秒就重启了（st=%+v）", st)
	}
	// ② 跑满阈值、没重启过：该重启，计数 +1
	due, st := hostDialRestartDue(now.Add(-hostDialRestartAfter-time.Second), hostDialRestartState{}, now)
	if !due || st.count != 1 {
		t.Errorf("跑满 %v 还没地址应当重启：due=%v st=%+v", hostDialRestartAfter, due, st)
	}
	// ③ 刚重启过（30 秒前）：间隔没到，不再重启
	if due, st2 := hostDialRestartDue(now.Add(-10*time.Minute),
		hostDialRestartState{last: now.Add(-30 * time.Second), count: 1}, now); due {
		t.Errorf("间隔没到就重启了（st=%+v）", st2)
	}
	// ④ 上次重启已经超过间隔：可以再来一次，计数 +1
	due, st = hostDialRestartDue(now.Add(-10*time.Minute),
		hostDialRestartState{last: now.Add(-hostDialRestartGap - time.Second), count: 1}, now)
	if !due || st.count != 2 {
		t.Errorf("超过 %v 应当再重启一次：due=%v st=%+v", hostDialRestartGap, due, st)
	}
	// ⑤ 已经到上限：停手（只由日志提醒）
	if due, _ := hostDialRestartDue(now.Add(-time.Hour),
		hostDialRestartState{last: now.Add(-time.Hour), count: hostDialRestartMax}, now); due {
		t.Errorf("重启次数到上限（%d）还在重开", hostDialRestartMax)
	}
	// ⑥ 启动时间未知（异常情形）：不重启
	if due, _ := hostDialRestartDue(time.Time{}, hostDialRestartState{}, now); due {
		t.Errorf("启动时间未知时不该重启")
	}
}

// TestHostDialRestartReset 拿到地址后计数要清零（下次掉线重新算）
func TestHostDialRestartReset(t *testing.T) {
	const id = 7
	now := time.Now()
	hostDialRestartMu.Lock()
	hostDialRestarts[id] = hostDialRestartState{last: now, count: 3}
	hostDialRestartMu.Unlock()

	resetHostDialRestart(id)

	hostDialRestartMu.Lock()
	st, ok := hostDialRestarts[id]
	hostDialRestartMu.Unlock()
	if ok {
		t.Errorf("reset 后不该再有记录：%+v", st)
	}
}
