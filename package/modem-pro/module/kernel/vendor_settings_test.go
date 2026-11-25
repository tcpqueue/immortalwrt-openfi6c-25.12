package kernel

import (
	"strings"
	"testing"

	"modemd/module/catalog"
	"modemd/module/common"
	"modemd/module/fibocom"
	"modemd/module/gosuncn"
	"modemd/module/huawei"
	"modemd/module/imeidialect"
	"modemd/module/meig"
	"modemd/module/modedialect"
	"modemd/module/quectel"
	"modemd/module/simcom"
	"modemd/module/tdtech"
)

// ====================== 厂商设置方言表：IMEI / USB 网卡模式 / 组网模式偏好 ======================

// modFor 造一台"只有型号和厂商"的模组（不发 AT，只测方言选择与指令拼装）
func modFor(vendor ModemVendor, model string, cmd ModemCommand) *Modem {
	return &Modem{ID: 0, Vendor: vendor, Model: model, ModelName: model, cmdImpl: cmd}
}

// TestIMEIDialects 每家的 IMEI 改写指令（QModem cmds/*.sh 原文）
func TestIMEIDialects(t *testing.T) {
	const imei = "861234567890123"
	cases := []struct {
		name string
		mod  *Modem
		want string // 空串 = 本机不支持
	}{
		{"移远", modFor(VendorQuectel, "RM520N-GL", &quectel.QuectelCommand{}), `AT+EGMR=1,7,"861234567890123"`},
		{"广和通 联发科", modFor(VendorFibocom, "FM350-GL", &fibocom.FibocomCommand{}), `AT+EGMREXT=1,7,"861234567890123"`},
		{"广和通 旧款4G", modFor(VendorFibocom, "NL668", &fibocom.FibocomCommand{}), `AT+LCTSN=1,7,"861234567890123"`},
		{"广和通 高通", modFor(VendorFibocom, "FM160-CN", &fibocom.FibocomCommand{}), `AT+GTSN=1,7,"861234567890123"`},
		{"芯讯通", modFor(VendorSimcom, "SIM8200EA-M2", &simcom.SimcomCommand{}), "AT+SIMEI=861234567890123"},
		{"美格", modFor(VendorMeig, "SRM815", &meig.MeigCommand{}), `AT+LCTSN=1,7,"861234567890123"`},
		{"高新兴", modFor(VendorUnknown, "GM800", &common.UnknownCommand{}), `AT+EGMR=1,7,"861234567890123"`},
		{"有方", modFor(VendorUnknown, "N510M", &common.UnknownCommand{}), `AT+SPIMEI=0,"861234567890123"`},
		{"Sierra", modFor(VendorUnknown, "EM9190", &common.UnknownCommand{}), `AT+EGMR=1,7,"861234567890123"`},
		{"华为/巴龙", modFor(VendorTDTech, "MT5700M-CN", &tdtech.MT5700Command{}), "at^phynum=IMEI,861234567890123"},
		{"Thales 不支持", modFor(VendorUnknown, "MV32-W-A", &common.UnknownCommand{}), ""},
	}
	for _, c := range cases {
		d := imeiDialectFor(c.mod)
		if c.want == "" {
			if d != nil {
				t.Errorf("%s：不该有 IMEI 方言", c.name)
			}
			continue
		}
		if d == nil {
			t.Errorf("%s：应有 IMEI 方言", c.name)
			continue
		}
		if got := d.Set(imei); got != c.want {
			t.Errorf("%s：指令 = %q, want %q", c.name, got, c.want)
		}
		if d.Note == "" {
			t.Errorf("%s：缺说明文案", c.name)
		}
	}
}

// TestFoxconnIMEINV550 Foxconn 的 NV 550 编码：80A 前缀 → 两两交换 → 小写逗号分隔
// （逐行照抄 QModem vendor/foxconn.sh set_imei）
func TestFoxconnIMEINV550(t *testing.T) {
	got := imeiDialectFor(modFor(VendorUnknown, "T99W175", &common.UnknownCommand{})).Set("862231040000001")
	// extended = 80A862231040000001 → 交换每对 → 08 8a 26 32 01 04 00 00 10
	want := `AT!nv=550,"0";AT!nv=550,9,"08,8a,26,32,01,04,00,00,10"`
	if got != want {
		t.Fatalf("编码 = %q\n     want %q", got, want)
	}
	// 两条指令（清 0 + 写入）要能被拆开发
	if parts := strings.Split(got, ";"); len(parts) != 2 {
		t.Errorf("应拆成 2 条，实际 %d", len(parts))
	}
}

// TestValidIMEI 只挡长度/字符（不校验 Luhn —— 用户要写的"自定义 IMEI"本来就不合规）
func TestValidIMEI(t *testing.T) {
	if !imeidialect.ValidIMEI("861234567890123") {
		t.Error("15 位数字应通过")
	}
	for _, bad := range []string{"", "86123456789012", "8612345678901234", "86123456789012a", "86123 4567890123"} {
		if imeidialect.ValidIMEI(bad) {
			t.Errorf("%q 不该通过", bad)
		}
	}
}

// TestQueryIMEIParse 各厂商 AT+CGSN 的包装不同，统一找 15 位数字
func TestQueryIMEIParse(t *testing.T) {
	for _, resp := range [][]string{
		{"AT+CGSN", "861234567890123", "OK"},                            // 裸 IMEI
		{"+CGSN: \"861234567890123\"", "OK"},                            // 引号
		{"+CGSN: 861234567890123,1", "OK"},                              // 带前缀与尾巴
		{"ATI", "Manufacturer: Fibocom", "IMEI: 861234567890123", "OK"}, // 混在 ATI 里
	} {
		if v, ok := ParseIMEI(resp); !ok || v != "861234567890123" {
			t.Errorf("%v → %q ok=%v", resp, v, ok)
		}
	}
	if _, ok := ParseIMEI([]string{"OK", "ERROR"}); ok {
		t.Error("没有 IMEI 时不该给结果")
	}
}

// TestIMEIReadCommands 读取指令序列：厂商指令 + 老写法兜底（去重/丢空串）
func TestIMEIReadCommands(t *testing.T) {
	if got := imeiReadCommands("AT+CGSN"); len(got) != 2 || got[0] != "AT+CGSN" || got[1] != cmdIMEILegacy {
		t.Errorf("标准写法应补一条兜底，got %v", got)
	}
	if got := imeiReadCommands(cmdIMEILegacy); len(got) != 1 || got[0] != cmdIMEILegacy {
		t.Errorf("本来就是老写法 → 不该重复，got %v", got)
	}
	if got := imeiReadCommands(""); len(got) != 1 || got[0] != cmdIMEILegacy {
		t.Errorf("厂商没给指令 → 只用兜底，got %v", got)
	}
}

// TestSetIMEIEngine 引擎流程：校验 → 下发 → **回读确认**（不一致要报错，不假报成功）
func TestSetIMEIEngine(t *testing.T) {
	withATTimeout(t)

	// 成功：写入后回读变成新值
	mod := &Modem{ID: 0, Vendor: VendorQuectel, Model: "RM520N-GL", ModelName: "RM520N-GL",
		cmdImpl: &quectel.QuectelCommand{}, writeChan: make(chan string, 32), readChan: make(chan string, 32)}
	cur := "860000000000001"
	fakeATServe(mod, func(cmd string) []string {
		switch {
		case strings.HasPrefix(cmd, "AT+CGSN"):
			return []string{cmd, cur, "OK"}
		case strings.HasPrefix(cmd, "AT+EGMR=1,7"):
			cur = strings.Trim(strings.SplitN(cmd, `"`, 3)[1], `"`)
			return []string{cmd, "OK"}
		}
		return []string{cmd, "OK"}
	})
	got, err := mod.SetIMEI("861234567890123")
	if err != nil {
		t.Fatalf("应当成功：%v", err)
	}
	if got != "861234567890123" {
		t.Errorf("回读 = %q", got)
	}

	// 位数不对 → 直接拒（不发 AT）
	bad := &Modem{ID: 0, Vendor: VendorQuectel, Model: "RM520N-GL", cmdImpl: &quectel.QuectelCommand{},
		writeChan: make(chan string, 32), readChan: make(chan string, 32)}
	sent := fakeATServe(bad, func(cmd string) []string { return []string{cmd, "OK"} })
	if _, err := bad.SetIMEI("12345"); err == nil {
		t.Error("位数不足应报错")
	}
	if len(drainSent(sent)) != 0 {
		t.Error("校验不过时不该发 AT")
	}

	// 固件"接受"但没生效（回读还是旧值）→ 必须报错并带上回读值
	stale := &Modem{ID: 0, Vendor: VendorQuectel, Model: "RM520N-GL", cmdImpl: &quectel.QuectelCommand{},
		writeChan: make(chan string, 32), readChan: make(chan string, 32)}
	fakeATServe(stale, func(cmd string) []string {
		if strings.HasPrefix(cmd, "AT+CGSN") {
			return []string{cmd, "860000000000001", "OK"}
		}
		return []string{cmd, "OK"} // 写入"成功"但没生效
	})
	got, err = stale.SetIMEI("861234567890123")
	if err == nil {
		t.Fatal("回读不一致必须报错")
	}
	if got != "860000000000001" {
		t.Errorf("报错时应带回读值，实际 %q", got)
	}

	// 不支持的机型（Thales）→ 明确报错
	if _, err := modFor(VendorUnknown, "MV32-W-A", &common.UnknownCommand{}).SetIMEI("861234567890123"); err == nil {
		t.Error("不支持改 IMEI 的机型应报错")
	}
}

// ====================== USB 网卡模式方言 ======================

// TestUSBDialectQueries 各家的查询指令 / 设置指令
func TestUSBDialectQueries(t *testing.T) {
	cases := []struct {
		name     string
		mod      *Modem
		query    string
		value    string
		set      string
		hasModes bool
	}{
		{"移远", modFor(VendorQuectel, "RM520N-GL", &quectel.QuectelCommand{}), `AT+QCFG="usbnet"`, "2", `AT+QCFG="usbnet",2`, true},
		{"广和通 高通", modFor(VendorFibocom, "FM160-CN", &fibocom.FibocomCommand{}), "AT+GTUSBMODE?", "32", "AT+GTUSBMODE=32", true},
		{"广和通 展锐", modFor(VendorFibocom, "FM650-CN", &fibocom.FibocomCommand{}), "AT+GTUSBMODE?", "40", "AT+GTUSBMODE=40", true},
		{"芯讯通 高通", modFor(VendorSimcom, "SIM8200EA-M2", &simcom.SimcomCommand{}), "AT+CUSBCFG?", "9011", "AT+CUSBCFG=usbid,1e0e,9011", true},
		{"芯讯通 lte", modFor(VendorSimcom, "A7605C", &simcom.SimcomCommand{}), `AT$MYCONFIG?`, "1", `AT$MYCONFIG="USBNETMODE",1,1`, true},
		{"高新兴", modFor(VendorUnknown, "GM800", &common.UnknownCommand{}), "AT+ZSWITCH?", "8", "AT+ZSWITCH=8", true},
		{"Foxconn", modFor(VendorUnknown, "T99W175", &common.UnknownCommand{}), "AT!USBSWITCH?", "90d5", "AT!USBSWITCH=90d5", true},
		{"Sierra", modFor(VendorUnknown, "EM9190", &common.UnknownCommand{}), "AT!USBCOMP?", "0x00000109", "AT!USBCOMP=1,4,0x00000109", true},
		{"Thales", modFor(VendorUnknown, "MV32-W-A", &common.UnknownCommand{}), "AT+SETCONFIG?", "1", "AT+SETCONFIG=1", true},
		{"Telit", modFor(VendorUnknown, "FN990A40", &common.UnknownCommand{}), "AT#USBCFG?", "3", "AT#USBCFG=3", true},
		{"openluat", modFor(VendorUnknown, "Air724UG", &common.UnknownCommand{}), "AT+SETUSB?", "1", "AT+SETUSB=1", true},
		{"华为 海思", modFor(VendorTDTech, "MT5700M-CN", &tdtech.MT5700Command{}), "AT^SETMODE?", "1", "AT^SETMODE=1", true},
		{"华为 展锐", modFor(VendorUnknown, "MH5000-82", &common.UnknownCommand{}), "AT^SETMODE?", "7", "AT^SETMODE=7", true},
		{"美格", modFor(VendorMeig, "SRM815", &meig.MeigCommand{}), "AT+SER?", "2", "AT+SER=2,1", true},
	}
	for _, c := range cases {
		d := usbDialectFor(c.mod)
		if d == nil {
			t.Errorf("%s：应有 USB 方言", c.name)
			continue
		}
		if d.Query != c.query {
			t.Errorf("%s：查询 = %q, want %q", c.name, d.Query, c.query)
		}
		if got := d.Set(c.value); got != c.set {
			t.Errorf("%s：设置（%s）= %q, want %q", c.name, c.value, got, c.set)
		}
		if (len(d.Modes) > 0) != c.hasModes {
			t.Errorf("%s：取值表 = %v（want hasModes=%v）", c.name, d.Modes, c.hasModes)
		}
		if d.Note == "" {
			t.Errorf("%s：缺说明", c.name)
		}
	}
}

// TestUSBDialectParse 各家应答 → 取值
func TestUSBDialectParse(t *testing.T) {
	cases := []struct {
		name string
		mod  *Modem
		resp []string
		want string
	}{
		{"移远", modFor(VendorQuectel, "RM520N-GL", &quectel.QuectelCommand{}),
			[]string{`+QCFG: "usbnet",5`, "OK"}, "5"},
		{"广和通", modFor(VendorFibocom, "FM160-CN", &fibocom.FibocomCommand{}),
			[]string{"+GTUSBMODE: 30", "OK"}, "30"},
		{"芯讯通 高通", modFor(VendorSimcom, "SIM8200EA-M2", &simcom.SimcomCommand{}),
			[]string{"USBID: 0X1E0E,0X9011", "OK"}, "9011"},
		{"芯讯通 lte", modFor(VendorSimcom, "A7605C", &simcom.SimcomCommand{}),
			[]string{"MYCONFIG: 1,2,0", "OK"}, "2"},
		{"高新兴", modFor(VendorUnknown, "GM800", &common.UnknownCommand{}),
			[]string{"+ZSWITCH: x", "OK"}, "x"},
		{"Foxconn", modFor(VendorUnknown, "T99W175", &common.UnknownCommand{}),
			[]string{"USBSWITCH: 90D5", "OK"}, "90d5"},
		{"Sierra", modFor(VendorUnknown, "EM9190", &common.UnknownCommand{}),
			[]string{"Interface bitmask: 00001009", "OK"}, "0x00001009"},
		{"Thales", modFor(VendorUnknown, "MV32-W-A", &common.UnknownCommand{}),
			[]string{"+SETCONFIG: 1", "OK"}, "1"},
	}
	for _, c := range cases {
		d := usbDialectFor(c.mod)
		if d == nil {
			t.Fatalf("%s：方言缺失", c.name)
		}
		got, ok := d.Parse(c.resp)
		if !ok || got != c.want {
			t.Errorf("%s：解析 %v → %q ok=%v, want %q", c.name, c.resp, got, ok, c.want)
		}
		// 名字要能在本机取值表里查到（否则界面显示原始值也没关系，但不能乱认）
		if len(d.Modes) > 0 && modeNameIn(d.Modes, got) == "" {
			t.Errorf("%s：解析出的取值 %q 不在取值表里 %v", c.name, got, d.Modes)
		}
	}
}

// ====================== 组网模式偏好方言 ======================

// TestModeDialectQueries 各家的查询/设置指令与语义值映射
func TestModeDialectQueries(t *testing.T) {
	cases := []struct {
		name  string
		mod   *Modem
		query string
		value string
		set   string
	}{
		{"移远 现代", modFor(VendorQuectel, "RM520N-GL", &quectel.QuectelCommand{}),
			`AT+QNWPREFCFG="mode_pref"`, common.ModeLTE, `AT+QNWPREFCFG="mode_pref",LTE`},
		{"移远 现代 组合", modFor(VendorQuectel, "RM520N-GL", &quectel.QuectelCommand{}),
			`AT+QNWPREFCFG="mode_pref"`, common.ModeLTENR5G, `AT+QNWPREFCFG="mode_pref",LTE:NR5G`},
		{"移远 旧款4G", modFor(VendorQuectel, "EC25", &quectel.QuectelCommand{}),
			`AT+QCFG="nwscanmode"`, common.ModeLTE, `AT+QCFG="nwscanmode",3`},
		{"移远 旧款4G 自动", modFor(VendorQuectel, "EC25", &quectel.QuectelCommand{}),
			`AT+QCFG="nwscanmode"`, common.ModeAuto, `AT+QCFG="nwscanmode",0`},
		{"芯讯通", modFor(VendorSimcom, "SIM8200EA-M2", &simcom.SimcomCommand{}),
			"AT+CNMP?", common.ModeNR5G, "AT+CNMP=71"},
		{"有方", modFor(VendorUnknown, "N510M", &common.UnknownCommand{}),
			`AT$MYSYSINFO`, common.ModeWCDMALTE, "AT$MYSYSINFO=7"},
		{"Sierra", modFor(VendorUnknown, "EM9190", &common.UnknownCommand{}),
			"AT!SELRAT?", common.ModeAuto, "AT!SELRAT=00"},
		{"Thales", modFor(VendorUnknown, "MV32-W-A", &common.UnknownCommand{}),
			"AT^SLMODE?", common.ModeLTENR5G, "AT^SLMODE=1,6"},
	}
	for _, c := range cases {
		d := modeDialectFor(c.mod)
		if d == nil {
			t.Errorf("%s：应有组网模式方言", c.name)
			continue
		}
		if d.Query != c.query {
			t.Errorf("%s：查询 = %q, want %q", c.name, d.Query, c.query)
		}
		if got := d.Set(c.value); got != c.set {
			t.Errorf("%s：设置（%s）= %q, want %q", c.name, c.value, got, c.set)
		}
		if len(d.Choices) == 0 {
			t.Errorf("%s：应有可选值表", c.name)
		}
		if d.Note == "" {
			t.Errorf("%s：缺说明", c.name)
		}
	}
}

// TestModeDialectParse 各家应答 → 语义值
func TestModeDialectParse(t *testing.T) {
	cases := []struct {
		name string
		mod  *Modem
		resp []string
		want string
	}{
		{"移远 AUTO", modFor(VendorQuectel, "RM520N-GL", &quectel.QuectelCommand{}),
			[]string{`+QNWPREFCFG: "mode_pref",AUTO`, "OK"}, common.ModeAuto},
		{"移远 组合", modFor(VendorQuectel, "RM520N-GL", &quectel.QuectelCommand{}),
			[]string{`+QNWPREFCFG: "mode_pref",LTE:NR5G`, "OK"}, common.ModeLTENR5G},
		{"移远 旧款 3", modFor(VendorQuectel, "EC25", &quectel.QuectelCommand{}),
			[]string{`+QCFG: "nwscanmode",3`, "OK"}, common.ModeLTE},
		{"芯讯通 38", modFor(VendorSimcom, "SIM8200EA-M2", &simcom.SimcomCommand{}),
			[]string{"+CNMP: 38", "OK"}, common.ModeLTE},
		{"有方 14", modFor(VendorUnknown, "N510M", &common.UnknownCommand{}),
			[]string{"$MYSYSINFO: 14,0", "OK"}, common.ModeAuto},
		{"有方 12", modFor(VendorUnknown, "N510M", &common.UnknownCommand{}),
			[]string{"$MYSYSINFO: 12,0", "OK"}, common.ModeLTENR5G},
		{"Sierra 20", modFor(VendorUnknown, "EM9190", &common.UnknownCommand{}),
			[]string{"!SELRAT: 20", "OK"}, common.ModeNR5G},
		{"Thales 7", modFor(VendorUnknown, "MV32-W-A", &common.UnknownCommand{}),
			[]string{"^SLMODE:1,7", "OK"}, common.ModeAuto},
	}
	for _, c := range cases {
		d := modeDialectFor(c.mod)
		if d == nil {
			t.Fatalf("%s：方言缺失", c.name)
		}
		got, ok := d.Parse(c.resp)
		if !ok || got != c.want {
			t.Errorf("%s：解析 %v → %q ok=%v, want %q", c.name, c.resp, got, ok, c.want)
		}
	}
}

// TestModeDialectResolvesForEveryCatalogModel 清单里**每一个**型号都必须能拿到组网模式方言 ——
// 哪怕厂商标识没认出来（`mod.Vendor == VendorUnknown`，运行时很常见：ATI 里没有厂商关键字、
// 或者是我们没覆盖的写法）。
//
// 坑（2026-10-07 用户报"好多模块不支持"）：以前只看 mod.Vendor，厂商一没认出来，
// 清单 91 个型号里有 **68 个**直接变成"本机未提供组网模式偏好指令" ——
// 移远/广和通/芯讯通/美格全家的方言都挂在 Vendor 上，另 8 家挂在型号谓词上。
// 现在两级兜底：② 查模块清单（每个型号都带厂商+平台）③ 按型号名族前缀猜。
func TestModeDialectResolvesForEveryCatalogModel(t *testing.T) {
	for _, e := range catalog.ModemCatalog {
		mod := modFor(VendorUnknown, e.Model, &common.UnknownCommand{})
		d := modeDialectFor(mod)
		if d == nil {
			t.Errorf("%s/%s：厂商标识认不出来时也该能推出组网模式方言", e.Vendor, e.Model)
			continue
		}
		if strings.TrimSpace(d.Query) == "" || d.Set == nil || len(d.Choices) == 0 {
			t.Errorf("%s/%s：方言不完整（query/set/choices）", e.Vendor, e.Model)
		}
	}
}

// TestModeDialectCatalogPlatform 清单里的**平台**差异（都是 QModem vendor/*.sh 的 case 分支）：
//
//	移远：`lte` 档用 AT+QCFG="nwscanmode"，其余（qualcomm/unisoc/hisilicon）都走 QNWPREFCFG；
//	高新兴：只有 qualcomm 平台有 5G 档，lte 平台只有自动/仅3G/仅4G。
func TestModeDialectCatalogPlatform(t *testing.T) {
	mod := modFor(VendorUnknown, "", &common.UnknownCommand{})
	cases := []struct {
		vendor, platform, wantQuery string
		wantNR                      bool
	}{
		{"quectel", "lte", `AT+QCFG="nwscanmode"`, false},
		{"quectel", "lte12", `AT+QNWPREFCFG="mode_pref"`, true},
		{"quectel", "unisoc", `AT+QNWPREFCFG="mode_pref"`, true},
		{"quectel", "qualcomm", `AT+QNWPREFCFG="mode_pref"`, true},
		{"gosuncn", "qualcomm", "AT+ZSNT?", true},
		{"gosuncn", "lte", "AT+ZSNT?", false},
		{"fibocom", "mediatek", "AT+GTACT?", true},
		{"simcom", "lte", "AT+CNMP?", true},
	}
	for _, c := range cases {
		d := modeDialectForCatalogVendor(c.vendor, c.platform, mod)
		if d == nil {
			t.Errorf("%s/%s：应有方言", c.vendor, c.platform)
			continue
		}
		if d.Query != c.wantQuery {
			t.Errorf("%s/%s：查询 = %q, want %q", c.vendor, c.platform, d.Query, c.wantQuery)
		}
		hasNR := false
		for _, v := range d.Choices {
			if v == common.ModeNR5G {
				hasNR = true
			}
		}
		if hasNR != c.wantNR {
			t.Errorf("%s/%s：有 5G 档 = %v, want %v（档位 %v）", c.vendor, c.platform, hasNR, c.wantNR, d.Choices)
		}
	}
}

// TestVendorFamilyOfModel 型号名族前缀（清单里也没有的型号，靠它猜一家）。
// ⚠️ 几个易混的：EM05/EM12/EM16 是移远，而 EM91/EM92 是 Sierra；MT57xx 是鼎桥（走华为那套）。
func TestVendorFamilyOfModel(t *testing.T) {
	cases := map[string]string{
		"RM500U-CNV":   "quectel",
		"EC200A":       "quectel",
		"EM05-CE":      "quectel",
		"RM520N-GL":    "quectel",
		"EM9190":       "sierra",
		"EM9291":       "sierra",
		"MC7354":       "sierra",
		"FM160-CN":     "fibocom",
		"FM350-GL":     "fibocom",
		"NL668":        "fibocom",
		"SIM8200EA-M2": "simcom",
		"A7600C":       "simcom",
		"A8200C-M2":    "simcom",
		"SRM815":       "meig",
		"MT5700M-CN":   "huawei",
		"MT5710_CN":    "huawei",
		"MV32-W-A":     "thales",
		"T99W175":      "foxconn",
		"T77W968":      "foxconn",
		"FN990A40":     "telit",
		"GM800":        "gosuncn",
		"N510M":        "neoway",
		"ME3630-W":     "neoway",
		"AIR724UG":     "openluat",
		"XYZ-9000":     "", // 认不出来就别给（猜错会写出错的取值）
		"":             "",
	}
	for model, want := range cases {
		if got := vendorFamilyOfModel(model); got != want {
			t.Errorf("vendorFamilyOfModel(%q) = %q, want %q", model, got, want)
		}
	}
}

// TestSimcomModeFullTable 芯讯通 CNMP 的**完整**码表（QModem vendor/simcom.sh 的
// get/set_network_prefer_nr 逐行核对）：2/14/38/71/54/55/109。
//
// 坑：以前只接了 2/38/71 三档，5G 模组上想选"4G+5G"（109）就是"本机不支持"。
func TestSimcomModeFullTable(t *testing.T) {
	d := modeDialectFor(modFor(VendorSimcom, "SIM8200EA-M2", &simcom.SimcomCommand{}))
	if d == nil {
		t.Fatal("芯讯通应有组网模式方言")
	}
	want := map[string]string{
		common.ModeAuto: "2", common.ModeWCDMA: "14", common.ModeLTE: "38", common.ModeNR5G: "71",
		common.ModeWCDMALTE: "54", common.ModeWCDMANR: "55", common.ModeLTENR5G: "109",
	}
	if len(d.Choices) != len(want) {
		t.Errorf("档位 = %v（%d 个），want %d 个", d.Choices, len(d.Choices), len(want))
	}
	for sem, code := range want {
		if got := d.Codes[sem]; got != code {
			t.Errorf("%s → %q, want %q", sem, got, code)
		}
		if got := d.Set(sem); got != "AT+CNMP="+code {
			t.Errorf("set(%s) = %q, want %q", sem, got, "AT+CNMP="+code)
		}
		if got := d.CodeName(code); got != sem {
			t.Errorf("codeName(%s) = %q, want %q", code, got, sem)
		}
		// 读回来也要能翻成同一个档位（读侧同一张表）
		got, ok := d.Parse([]string{"+CNMP: " + code, "OK"})
		if !ok || got != sem {
			t.Errorf("parse(%s) = %q ok=%v, want %q", code, got, ok, sem)
		}
	}
}

// TestModeProbeFindsDialect 挨个问：厂商认不出来、清单里也没有的型号，
// 直接把各家的查询指令发一遍，**第一条能答上来的就用它**（并缓存、且写也能用）。
func TestModeProbeFindsDialect(t *testing.T) {
	withATTimeout(t)
	st := "109"
	mod := &Modem{ID: 0, Vendor: VendorUnknown, Model: "XYZ-9000", ModelName: "XYZ-9000",
		cmdImpl: &common.UnknownCommand{}, writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	sent := fakeATServe(mod, func(cmd string) []string {
		switch {
		case cmd == "AT+CNMP?":
			return []string{cmd, "+CNMP: " + st, "OK"}
		case cmd == "AT+CNMP=?":
			return []string{cmd, "+CNMP: (2,13,14,38,71,54,55,109)", "OK"}
		case strings.HasPrefix(cmd, "AT+CNMP="):
			st = strings.TrimPrefix(cmd, "AT+CNMP=")
			return []string{cmd, "OK"}
		}
		return []string{cmd, "ERROR"} // 别家的指令一律拒
	})

	caps, err := mod.QueryModePrefCaps()
	if err != nil {
		t.Fatal(err)
	}
	if !caps.Auto {
		t.Error("探测出来的方言要标成自动识别（caps.Auto）")
	}
	if caps.Value != common.ModeLTENR5G || caps.Cmd != "AT+CNMP?" {
		t.Errorf("caps = %+v（want value=%s cmd=AT+CNMP?）", caps, common.ModeLTENR5G)
	}
	if len(caps.Choices) < 7 {
		t.Errorf("档位不全：%d", len(caps.Choices))
	}
	// 探测过程中确实试过别家（否则说明候选顺序/逻辑不对）
	if all := drainSent(sent); !hasCmd(all, `AT+QNWPREFCFG="mode_pref"`) {
		t.Errorf("应该先试过移远那条，实际发了 %v", all)
	}

	// ② 缓存：再读一次只发一条 AT+CNMP?，不再把十几条探测指令重发一遍
	drainSent(sent)
	if _, err := mod.QueryModePrefCaps(); err != nil {
		t.Fatal(err)
	}
	second := drainSent(sent)
	for _, c := range second {
		if c != "AT+CNMP?" {
			t.Errorf("缓存没生效，又发了 %q（本轮 %v）", c, second)
		}
	}

	// ③ 写入也要用探测出来的方言（并且带不带的住：写后回读）
	after, err := mod.SetModePrefCaps(common.ModeLTE)
	if err != nil {
		t.Fatal(err)
	}
	if after.Value != common.ModeLTE {
		t.Errorf("写后回读 = %q, want %q", after.Value, common.ModeLTE)
	}
}

// TestModeProbeRejectsWrongCodeTable 同为 `AT^SYSCFGEX` 的华为/美格两家：**靠码表交叉核对**挑对。
//
// 按"有应答就算它家的"会挑错（两家格式一样）：美格的 5G 码是 04、华为是 08，
// 用错码表写下去 = 拿用户的网做实验。
func TestModeProbeRejectsWrongCodeTable(t *testing.T) {
	withATTimeout(t)
	st := "04" // 美格的"仅 5G"
	mod := &Modem{ID: 0, Vendor: VendorUnknown, Model: "ZZZ-1", ModelName: "ZZZ-1",
		cmdImpl: &common.UnknownCommand{}, writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	sent := fakeATServe(mod, func(cmd string) []string {
		switch {
		case cmd == "AT^SYSCFGEX?":
			return []string{cmd, `^SYSCFGEX: "` + st + `",all,0,2,all,all,all,all,1`, "OK"}
		case strings.HasPrefix(cmd, `AT^SYSCFGEX="`):
			st = strings.SplitN(cmd, `"`, 3)[1]
			return []string{cmd, "OK"}
		}
		return []string{cmd, "ERROR"}
	})

	caps, err := mod.QueryModePrefCaps()
	if err != nil {
		t.Fatal(err)
	}
	if caps.Value != common.ModeNR5G {
		t.Errorf("04 只有美格那张表认（= 仅5G），实际 value=%q name=%q", caps.Value, caps.Name)
	}
	if _, err := mod.SetModePrefCaps(common.ModeNR5G); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range drainSent(sent) {
		if strings.HasPrefix(c, `AT^SYSCFGEX="04"`) {
			found = true
		}
		if strings.HasPrefix(c, `AT^SYSCFGEX="08"`) {
			t.Errorf("用了华为的 5G 码（08），实测这台是美格：%q", c)
		}
	}
	if !found {
		t.Error("没看到美格那套 5G 写入指令（AT^SYSCFGEX=\"04\",…）")
	}
}

// TestModeProbeAllFail 一圈都不认时：报错要把**试过的指令**列出来（排查用），
// 而且不能无限重试（缓存住"都不认"）。
func TestModeProbeAllFail(t *testing.T) {
	withATTimeout(t)
	mod := &Modem{ID: 0, Vendor: VendorUnknown, Model: "QQQ-1", ModelName: "QQQ-1",
		cmdImpl: &common.UnknownCommand{}, writeChan: make(chan string, 128), readChan: make(chan string, 128)}
	sent := fakeATServe(mod, func(cmd string) []string { return []string{cmd, "ERROR"} })

	_, err := mod.QueryModePrefCaps()
	if err == nil {
		t.Fatal("全都回 ERROR 时应报错")
	}
	if !strings.Contains(err.Error(), "已试过") {
		t.Errorf("报错要列出试过的指令，实际：%v", err)
	}
	first := drainSent(sent)
	if len(first) < 3 {
		t.Errorf("应该试过多家的指令，实际只发了 %v", first)
	}
	// 第二次：缓存生效（不再重复挨个问）
	if _, err := mod.QueryModePrefCaps(); err == nil {
		t.Error("第二次也该报错")
	}
	if again := drainSent(sent); len(again) > len(first) {
		t.Errorf("缓存没生效：第二次又发了 %d 条", len(again))
	}
}

// TestModeCodeTables 各家的「语义档位 ↔ 本机码」表（码表逐行核对 QModem vendor/*.sh）
//
// 界面下拉就是按这些表列出来的，所以每一档必须：① 中文名 ② 能拼出正确指令 ③ 读回来能翻译回档位。
func TestModeCodeTables(t *testing.T) {
	cases := []struct {
		name string
		mod  *Modem
		// want 语义档位 → 本机码（缺的档位表示本机没有这一档）
		want  map[string]string
		query string
	}{
		{"华为/巴龙", modFor(VendorTDTech, "MT5700M-CN", &tdtech.MT5700Command{}), map[string]string{
			common.ModeAuto: "00", common.ModeWCDMA: "02", common.ModeLTE: "03", common.ModeNR5G: "08",
			common.ModeWCDMALTE: "0302", common.ModeWCDMANR: "0802", common.ModeLTENR5G: "0803",
		}, "AT^SYSCFGEX?"},
		{"美格", modFor(VendorMeig, "SRM815", &meig.MeigCommand{}), map[string]string{
			common.ModeAuto: "00", common.ModeWCDMA: "02", common.ModeLTE: "03", common.ModeNR5G: "04",
			common.ModeWCDMALTE: "0302", common.ModeWCDMANR: "0402", common.ModeLTENR5G: "0403",
		}, "AT^SYSCFGEX?"},
		{"Foxconn", modFor(VendorUnknown, "T99W175", &common.UnknownCommand{}), map[string]string{
			common.ModeAuto: "1,0", common.ModeWCDMA: "1,1", common.ModeLTE: "1,2", common.ModeNR5G: "1,4",
			common.ModeWCDMALTE: "1,3", common.ModeWCDMANR: "1,5", common.ModeLTENR5G: "1,6",
		}, "AT!SLMODE?"},
		{"广和通", modFor(VendorFibocom, "FM160-CN", &fibocom.FibocomCommand{}), map[string]string{
			common.ModeAuto: "20", common.ModeWCDMA: "1", common.ModeLTE: "2", common.ModeNR5G: "14",
			common.ModeWCDMALTE: "4", common.ModeWCDMANR: "16", common.ModeLTENR5G: "17",
		}, "AT+GTACT?"},
		{"高新兴（高通 GM800）", modFor(VendorUnknown, "GM800", &common.UnknownCommand{}), map[string]string{
			common.ModeAuto: "0", common.ModeWCDMA: "2", common.ModeLTE: "6", common.ModeNR5G: "7", common.ModeLTENR5G: "8",
		}, "AT+ZSNT?"},
		{"高新兴（LTE ME3630）", modFor(VendorUnknown, "ME3630", &common.UnknownCommand{}), map[string]string{
			common.ModeAuto: "0", common.ModeWCDMA: "2", common.ModeLTE: "6",
		}, "AT+ZSNT?"},
		{"Telit", modFor(VendorUnknown, "FN990A40", &common.UnknownCommand{}), map[string]string{
			common.ModeAuto: "38", common.ModeWCDMA: "22", common.ModeLTE: "28", common.ModeNR5G: "36",
			common.ModeWCDMALTE: "31", common.ModeWCDMANR: "40", common.ModeLTENR5G: "37",
		}, "AT+WS46?"},
		{"openluat", modFor(VendorUnknown, "Air724UG", &common.UnknownCommand{}), map[string]string{
			common.ModeAuto: "0", common.ModeLTE: "4",
		}, "AT+CTEC?"},
	}
	for _, c := range cases {
		d := modeDialectFor(c.mod)
		if d == nil {
			t.Errorf("%s：方言缺失", c.name)
			continue
		}
		if d.Query != c.query {
			t.Errorf("%s：查询 = %q, want %q", c.name, d.Query, c.query)
		}
		if d.Enum == "" {
			t.Errorf("%s：应能问模组要取值（AT+XXX=?）", c.name)
		}
		if len(d.Choices) != len(c.want) {
			t.Errorf("%s：档位数 %d, want %d（%v）", c.name, len(d.Choices), len(c.want), d.Choices)
		}
		for sem, wantCode := range c.want {
			if !modedialect.IsModePrefSemantic(sem) {
				t.Fatalf("测试用例写错了：%q 不是语义档位", sem)
			}
			code := d.Codes[sem]
			if code != wantCode {
				t.Errorf("%s：%s 的码 = %q, want %q", c.name, sem, code, wantCode)
				continue
			}
			// 码要能反查回这一档（界面显示中文名靠它）
			if back := d.CodeName(code); back != sem {
				t.Errorf("%s：码 %q 反查成 %q, want %q", c.name, code, back, sem)
			}
		}
	}

	// 高新兴高通平台多两档，LTE 平台只有三档
	qcom := gosuncn.Mode(true)
	if qcom.Codes[common.ModeNR5G] != "7" || qcom.Codes[common.ModeLTENR5G] != "8" {
		t.Errorf("高新兴高通表 = %v", qcom.Codes)
	}
	if qcom.Set(common.ModeNR5G) != "AT+ZSNT=7,0,0" {
		t.Errorf("高新兴高通仅5G = %q", qcom.Set(common.ModeNR5G))
	}
	lteOnly := gosuncn.Mode(false)
	if _, ok := lteOnly.Codes[common.ModeNR5G]; ok {
		t.Errorf("高新兴 LTE 平台不该有 5G 档：%v", lteOnly.Codes)
	}

	// 单参数方言：d.set 直接给出完整指令（复合指令方言只在 rebuild 后拼，见下）
	for _, c := range []struct {
		name string
		mod  *Modem
		sem  string
		want string
	}{
		{"Foxconn 仅5G", modFor(VendorUnknown, "T99W175", &common.UnknownCommand{}), common.ModeNR5G, "AT!SLMODE=1,4"},
		{"Telit 4G+5G", modFor(VendorUnknown, "FN990A40", &common.UnknownCommand{}), common.ModeLTENR5G, "AT+WS46=37"},
		{"高新兴 仅4G", modFor(VendorUnknown, "GM800", &common.UnknownCommand{}), common.ModeLTE, "AT+ZSNT=6,0,0"},
		{"openluat 自动", modFor(VendorUnknown, "Air724UG", &common.UnknownCommand{}), common.ModeAuto, "AT+CTEC=0,0"},
	} {
		d := modeDialectFor(c.mod)
		if got := d.Set(c.sem); got != c.want {
			t.Errorf("%s：%s → %q, want %q", c.name, c.sem, got, c.want)
		}
	}

	// 华为/美格：主写法 = **上游 QModem cmds/*.sh 的固定模板**（上游为准）；
	// 备用写法 = 回读整串只换第 1 个字段（保留用户当前的频段参数）
	hw := modeDialectFor(modFor(VendorTDTech, "MT5700M-CN", &tdtech.MT5700Command{}))
	if hw.Rebuild != nil {
		t.Error("华为主写法应是上游固定模板，不该再走回读改写")
	}
	if got := hw.Set(common.ModeLTENR5G); got != `AT^SYSCFGEX="0803",40000000,1,2,40000000,,` {
		t.Errorf("华为 4G+5G 主写法 = %q", got)
	}
	if got := hw.AltCmd(common.ModeLTENR5G, `^SYSCFGEX: "00",3FFFFFFF,1,2,3FFFFFFF,,`); got != `AT^SYSCFGEX="0803",3FFFFFFF,1,2,3FFFFFFF,,` {
		t.Errorf("华为备用写法（保留频段参数）= %q", got)
	}
	mg := modeDialectFor(modFor(VendorMeig, "SRM815", &meig.MeigCommand{}))
	if got := mg.Set(common.ModeLTENR5G); got != `AT^SYSCFGEX="0403",all,0,2,all,all,all,all,1` {
		t.Errorf("美格 4G+5G 主写法 = %q", got)
	}
	if got := mg.AltCmd(common.ModeLTENR5G, `^SYSCFGEX: "00",all,0,2,all,all,all,all,1`); got != `AT^SYSCFGEX="0403",all,0,2,all,all,all,all,1` {
		t.Errorf("美格备用写法 = %q", got)
	}
	fb := modeDialectFor(modFor(VendorFibocom, "FM160-CN", &fibocom.FibocomCommand{}))
	// 广和通**不**用回读改写：真机 FM160-CN 实测带完整频段表会被 ERROR 拒
	// ⇒ 主写法就发裸档位号，回读整串只当**备用**写法（少数固件可能坚持要完整参数表）
	if fb.Rebuild != nil {
		t.Error("广和通不该走回读改写（真机实测绘的写法会被拒）")
	}
	if got := fb.Set(common.ModeLTENR5G); got != "AT+GTACT=17" {
		t.Errorf("广和通 4G+5G 主写法 = %q, want AT+GTACT=17", got)
	}
	if got := fb.AltCmd(common.ModeLTENR5G, "+GTACT: 1,2,3,4"); got != "AT+GTACT=17,2,3,4" {
		t.Errorf("广和通备用写法（带频段表）= %q", got)
	}
	// 有方：仅 5G 的**写**码按上游用 9（5 是旧码，读侧两种都认）
	nw := modeDialectFor(modFor(VendorUnknown, "N510M", &common.UnknownCommand{}))
	if got := nw.Set(common.ModeNR5G); got != "AT$MYSYSINFO=9" {
		t.Errorf("有方仅5G = %q, want AT$MYSYSINFO=9（上游 config_mode=9）", got)
	}
	if v, ok := nw.Parse([]string{"$MYSYSINFO: 5,0", "OK"}); !ok || v != common.ModeNR5G {
		t.Errorf("有方旧码 5 也要认成仅5G，得到 %q ok=%v", v, ok)
	}
	if v, ok := nw.Parse([]string{"$MYSYSINFO: 9,0", "OK"}); !ok || v != common.ModeNR5G {
		t.Errorf("有方新码 9 要认成仅5G，得到 %q ok=%v", v, ok)
	}

	// 形态不像码的输入必须拒（"5G" 这种既不是档位也不是码）
	if got := hw.Set("5G"); got != "" {
		t.Errorf("华为不该把 \"5G\" 当码发下去，实际 %q", got)
	}
}

// TestFibocomModePrefBareActWrite 真机 FM160-CN（2026-10-07）的镜像：
//
//	AT+GTACT=17,6,3,1,8,101,…  → ERROR（把回读到的整串原样发回去会被拒：
//	                              空参数组被省略 ⇒ 位置对齐有歧义）
//	AT+GTACT=17                → OK（模组自己把频段参数按新档位重组）
//
// 所以主写法必须是**裸档位号**。
func TestFibocomModePrefBareActWrite(t *testing.T) {
	withATTimeout(t)
	st := "20"
	bands := ",6,3,1,8,101,103,105,108,134,138,139,140,141,501,5028,5041,5078,5079"
	mod := &Modem{ID: 0, Vendor: VendorFibocom, Model: "FM160-CN", ModelName: "FM160-CN",
		cmdImpl: &fibocom.FibocomCommand{}, writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	sent := fakeATServe(mod, func(cmd string) []string {
		switch {
		case cmd == "AT+GTACT?":
			return []string{cmd, "+GTACT: " + st + bands, "OK"}
		case cmd == "AT+GTACT=?":
			return []string{cmd, "+GTACT: (1,2,4,10,14,16,17,20),(2,3,6),(2,3,6),(),(1,8)," +
				"(101,103,105,108,134,138,139,140,141),(),(),(501,5028,5041,5078,5079)", "OK"}
		case strings.HasPrefix(cmd, "AT+GTACT="):
			v := strings.TrimPrefix(cmd, "AT+GTACT=")
			if strings.Contains(v, ",") {
				return []string{cmd, "ERROR"} // 真机就是拒带频段表的写法
			}
			st = v
			return []string{cmd, "OK"}
		}
		return []string{cmd, "ERROR"}
	})
	caps, err := mod.SetModePrefCaps(common.ModeLTENR5G)
	if err != nil {
		t.Fatal(err)
	}
	if caps.Value != common.ModeLTENR5G {
		t.Errorf("写后回读 = %q（%q）", caps.Value, caps.Name)
	}
	all := drainSent(sent)
	if !hasCmd(all, "AT+GTACT=17") {
		t.Errorf("应该发裸档位号 AT+GTACT=17，实际发了 %v", all)
	}
	for _, c := range all {
		if strings.HasPrefix(c, "AT+GTACT=17,") {
			t.Errorf("不该带频段表（真机会拒）：%q", c)
		}
	}
}

// TestModePrefFallbackWriteForm 主写法被拒 ⇒ 自动改试备用写法（QModem 上游 cmds/*.sh 的固定模板）
func TestModePrefFallbackWriteForm(t *testing.T) {
	withATTimeout(t)
	// 当前频段参数特意与模板不同（3FFFFFFF），才能分辨发的是哪个写法
	st := "00"
	h := &Modem{ID: 0, Vendor: VendorTDTech, Model: "MT5700M-CN", ModelName: "MT5700M-CN",
		cmdImpl: &tdtech.MT5700Command{}, writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	sent := fakeATServe(h, func(cmd string) []string {
		switch {
		case cmd == "AT^SYSCFGEX?":
			return []string{cmd, `^SYSCFGEX: "` + st + `",3FFFFFFF,1,2,3FFFFFFF,,`, "OK"}
		case cmd == "AT^SYSCFGEX=?":
			return []string{cmd, "^SYSCFGEX: (00,02,03,08)", "OK"}
		case strings.HasPrefix(cmd, `AT^SYSCFGEX="`):
			// 只认"回读改写"那条（模拟"上游固定模板被固件拒"⇒ 得走备用写法）
			if strings.Contains(cmd, "3FFFFFFF,1,2,3FFFFFFF,,") {
				st = strings.SplitN(strings.TrimPrefix(cmd, `AT^SYSCFGEX="`), `"`, 2)[0]
				return []string{cmd, "OK"}
			}
			return []string{cmd, "ERROR"}
		}
		return []string{cmd, "ERROR"}
	})
	caps, err := h.SetModePrefCaps(common.ModeLTENR5G)
	if err != nil {
		t.Fatal(err)
	}
	if caps.Value != common.ModeLTENR5G {
		t.Errorf("写后回读 = %q", caps.Value)
	}
	all := drainSent(sent)
	if !hasCmd(all, `AT^SYSCFGEX="0803",40000000,1,2,40000000,,`) {
		t.Errorf("应先用上游固定模板，实际 %v", all)
	}
	if !hasCmd(all, `AT^SYSCFGEX="0803",3FFFFFFF,1,2,3FFFFFFF,,`) {
		t.Errorf("被拒后应改用回读改写的写法兑底（保留频段参数），实际 %v", all)
	}
}

// TestModeCodeTablesParse 读回来的码要翻译成中文档位（翻不出来才显示原码）
func TestModeCodeTablesParse(t *testing.T) {
	cases := []struct {
		name string
		mod  *Modem
		resp []string
		want string
	}{
		{"华为 00", modFor(VendorTDTech, "MT5700M-CN", &tdtech.MT5700Command{}),
			[]string{`^SYSCFGEX: "00",40000000,1,2,40000000,,`, "OK"}, common.ModeAuto},
		{"华为 0803", modFor(VendorTDTech, "MT5700M-CN", &tdtech.MT5700Command{}),
			[]string{`^SYSCFGEX: "0803",40000000,1,2,40000000,,`, "OK"}, common.ModeLTENR5G},
		{"华为 三档串", modFor(VendorTDTech, "MT5700M-CN", &tdtech.MT5700Command{}),
			[]string{`^SYSCFGEX: "080302",all,0,2,all,,,,1`, "OK"}, common.ModeAuto},
		{"美格 04 是 5G", modFor(VendorMeig, "SRM815", &meig.MeigCommand{}),
			[]string{`^SYSCFGEX: "04",all,0,2,all,all,all,all,1`, "OK"}, common.ModeNR5G},
		{"Foxconn 1,4", modFor(VendorUnknown, "T99W175", &common.UnknownCommand{}),
			[]string{"!SLMODE: 1,4", "OK"}, common.ModeNR5G},
		{"广和通 17", modFor(VendorFibocom, "FM160-CN", &fibocom.FibocomCommand{}),
			[]string{"+GTACT: 17,lte,1,2", "OK"}, common.ModeLTENR5G},
		{"高新兴 6", modFor(VendorUnknown, "GM800", &common.UnknownCommand{}),
			[]string{"+ZSNT: 6,0,0", "OK"}, common.ModeLTE},
		{"Telit 38", modFor(VendorUnknown, "FN990A40", &common.UnknownCommand{}),
			[]string{"+WS46: 38", "OK"}, common.ModeAuto},
		{"openluat 4", modFor(VendorUnknown, "Air724UG", &common.UnknownCommand{}),
			[]string{"+CTEC: 4", "OK"}, common.ModeLTE},
		{"未知码原样显示", modFor(VendorUnknown, "FN990A40", &common.UnknownCommand{}),
			[]string{"+WS46: 99", "OK"}, "99"},
	}
	for _, c := range cases {
		d := modeDialectFor(c.mod)
		if d == nil {
			t.Fatalf("%s：方言缺失", c.name)
		}
		got, ok := d.Parse(c.resp)
		if !ok || got != c.want {
			t.Errorf("%s：解析 %v → %q ok=%v, want %q", c.name, c.resp, got, ok, c.want)
		}
	}
}

// TestModePrefLabels 语义档位的中文名（界面下拉里不能只显示 AUTO/LTE:NR5G）
func TestModePrefLabels(t *testing.T) {
	for _, v := range modedialect.AllModePrefs {
		if got := modedialect.ModeLabel(v); got == v || got == "" {
			t.Errorf("%s 缺中文名（得到 %q）", v, got)
		}
		if !modedialect.IsModePrefSemantic(v) {
			t.Errorf("%s 应被认成语义档位", v)
		}
	}
	if len(modedialect.AllModePrefs) != 7 {
		t.Errorf("语义档位应是 7 档，实际 %d", len(modedialect.AllModePrefs))
	}
	// 原码不能被认成语义值（否则会被当成档位拒写）
	for _, code := range []string{"0", "2", "00", "10", "38", "71"} {
		if modedialect.IsModePrefSemantic(code) {
			t.Errorf("%q 不该被认成语义档位", code)
		}
		if modedialect.ModeLabel(code) != code {
			t.Errorf("原码 %q 应原样展示", code)
		}
	}
}

// TestParseEnumValues `AT+XXX=?` 应答的取值提取（三家语法）
func TestParseEnumValues(t *testing.T) {
	cases := []struct {
		name  string
		resp  []string
		match string
		want  []string
	}{
		{"数字枚举", []string{"AT+CNMP=?", "+CNMP: (2,13,14,38,71)", "OK"}, "",
			[]string{"2", "13", "14", "38", "71"}},
		{"区间", []string{"AT^SLMODE=?", "^SLMODE: (0-7)", "OK"}, "",
			[]string{"0", "1", "2", "3", "4", "5", "6", "7"}},
		{"字符串枚举（移远）",
			[]string{"AT+QNWPREFCFG=?", `+QNWPREFCFG: "mode_pref",("AUTO","LTE","NR5G")`, "OK"}, "",
			[]string{"AUTO", "LTE", "NR5G"}},
		{"移远只取 mode_pref 那一行",
			[]string{`+QNWPREFCFG: "lte_band",(1-64)`, `+QNWPREFCFG: "mode_pref",("AUTO","LTE")`, "OK"},
			"mode_pref", []string{"AUTO", "LTE"}},
		{"没有括号 → 空", []string{"ERROR"}, "", nil},
	}
	for _, c := range cases {
		got := modedialect.ParseEnumValues(c.resp, c.match)
		if len(got) != len(c.want) {
			t.Errorf("%s：%v vs %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s：第 %d 项 %q, want %q", c.name, i, got[i], c.want[i])
			}
		}
	}
}

// TestRebuildSetFromQuery 复合指令（SYSCFGEX / WS46）的"回读→只替换第一个字段→原样写回"
func TestRebuildSetFromQuery(t *testing.T) {
	// 华为风格：带引号 + 后面跟着频段/漫游/服务域
	got := modedialect.RebuildSetFromQuery(`AT^SYSCFGEX="`, `^SYSCFGEX: "00",40000000,1,2,40000000,,`, "02")
	want := `AT^SYSCFGEX="02",40000000,1,2,40000000,,`
	if got != want {
		t.Errorf("重建指令 = %q\n        want %q", got, want)
	}
	// 不带引号的形态：第一个逗号字段要能被替换，其余保持
	got = modedialect.RebuildSetFromQuery("AT+WS46=", "!SELRAT: 06,1,", "20")
	if got != "AT+WS46=20,1," {
		t.Errorf("无引号重建 = %q", got)
	}
	// 认不出的格式 → 空（调用方据此报错，不拿半条指令去发）
	if got := modedialect.RebuildSetFromQuery("AT^X=", "^X: ", "1"); got != "" {
		t.Errorf("应返回空，实际 %q", got)
	}
}

// TestNormalizeModePref 归一化：认不出来的原文原样保留
func TestNormalizeModePref(t *testing.T) {
	cases := map[string]string{
		"AUTO":           common.ModeAuto,
		"auto":           common.ModeAuto,
		"LTE":            common.ModeLTE,
		"NR5G":           common.ModeNR5G,
		"WCDMA":          common.ModeWCDMA,
		"WCDMA:LTE":      common.ModeWCDMALTE,
		"LTE_NR5G":       common.ModeLTENR5G,
		"WCDMA:NR5G":     common.ModeWCDMANR,
		"LTE:NR5G:WCDMA": common.ModeAuto,
		"XYZ":            "XYZ", // 认不出来 → 原样
	}
	for in, want := range cases {
		if got := common.NormalizeModePref(in); got != want {
			t.Errorf("归一化 %q = %q, want %q", in, got, want)
		}
	}
}

// TestModePrefEngineCaps 引擎层：读能力 + 设置（含回读）
func TestModePrefEngineCaps(t *testing.T) {
	withATTimeout(t)
	state := "LTE"
	mod := &Modem{ID: 0, Vendor: VendorQuectel, Model: "RM520N-GL", ModelName: "RM520N-GL",
		cmdImpl: &quectel.QuectelCommand{}, writeChan: make(chan string, 32), readChan: make(chan string, 32)}
	fakeATServe(mod, func(cmd string) []string {
		switch {
		case strings.Contains(cmd, "mode_pref") && strings.Contains(cmd, ","):
			state = strings.TrimPrefix(cmd, `AT+QNWPREFCFG="mode_pref",`)
			return []string{cmd, "OK"}
		case strings.Contains(cmd, "mode_pref"):
			return []string{cmd, `+QNWPREFCFG: "mode_pref",` + state, "OK"}
		}
		return []string{cmd, "OK"}
	})

	caps, err := mod.QueryModePrefCaps()
	if err != nil {
		t.Fatal(err)
	}
	if caps.Value != common.ModeLTE || len(caps.Choices) == 0 {
		t.Errorf("caps = %+v", caps)
	}
	after, err := mod.SetModePrefCaps("lte:nr5g") // 小写也要认
	if err != nil {
		t.Fatal(err)
	}
	if after.Value != common.ModeLTENR5G {
		t.Errorf("设置后回读 = %q，want %q", after.Value, common.ModeLTENR5G)
	}
	// 不支持的取值 → 报错且不发 AT
	if _, err := mod.SetModePrefCaps("5G-ONLY-XX"); err == nil {
		t.Error("不支持的取值应报错")
	}
}

// TestModePrefCapsFullListAndChinese 引擎侧：档位必须带**中文名**且**全量列出**（不能只有一个）
func TestModePrefCapsFullListAndChinese(t *testing.T) {
	withATTimeout(t)

	// ① 有取证码表（移远现代）：7 档中文名 + 可写；设备枚举出来的额外取值也能选
	q := &Modem{ID: 0, Vendor: VendorQuectel, Model: "RM520N-GL", ModelName: "RM520N-GL",
		cmdImpl: &quectel.QuectelCommand{}, writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	fakeATServe(q, func(cmd string) []string {
		switch {
		case cmd == `AT+QNWPREFCFG=?`:
			return []string{cmd, `+QNWPREFCFG: "mode_pref",("AUTO","WCDMA","LTE","NR5G","LTE:NR5G","TDS")`, "OK"}
		case strings.Contains(cmd, "mode_pref"):
			return []string{cmd, `+QNWPREFCFG: "mode_pref",LTE:NR5G`, "OK"}
		}
		return []string{cmd, "OK"}
	})
	caps, err := q.QueryModePrefCaps()
	if err != nil {
		t.Fatal(err)
	}
	if caps.Name != "4G + 5G（NSA/SA）" {
		t.Errorf("当前值中文名 = %q", caps.Name)
	}
	if caps.Freeform {
		t.Error("有取证码表时不该是 freeform")
	}
	sem := map[string]modedialect.ModeChoice{}
	var enumerated []string
	for _, ch := range caps.Choices {
		sem[ch.Value] = ch
		if ch.Source != "" && strings.Contains(ch.Source, "设备枚举") {
			enumerated = append(enumerated, ch.Value)
		}
	}
	if len(sem) < len(modedialect.AllModePrefs) {
		t.Errorf("档位不全：%d < %d", len(sem), len(modedialect.AllModePrefs))
	}
	for _, v := range modedialect.AllModePrefs {
		ch, ok := sem[v]
		if !ok {
			t.Errorf("缺档位 %s", v)
			continue
		}
		if ch.Name == ch.Value || ch.Name == "" {
			t.Errorf("%s 没有中文名（%q）", v, ch.Name)
		}
		if !ch.Writable {
			t.Errorf("%s 在取证码表里应可写", v)
		}
	}
	// 枚举出来的 "TDS" 不是语义档位 → 原样显示那个码，仍可写（设备自己认）
	if ch, ok := sem["TDS"]; !ok || !ch.Writable || ch.Name != "TDS" {
		t.Errorf("枚举档位处理不对：%+v（枚举到 %v）", ch, enumerated)
	}

	// ② 华为/巴龙（复合指令 + 完整码表）：档位全部可写且有中文名；枚举出的额外码原样显示；
	//    写入必须**带上后面的频段/漫游参数**（回读→只换第 1 个字段）
	st := "00"
	h := &Modem{ID: 0, Vendor: VendorTDTech, Model: "MT5700M-CN", ModelName: "MT5700M-CN",
		cmdImpl: &tdtech.MT5700Command{}, writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	sent := fakeATServe(h, func(cmd string) []string {
		switch {
		case cmd == "AT^SYSCFGEX=?":
			return []string{cmd, "^SYSCFGEX: (00,01,02,03,08)", "OK"} // 01 不在我们表里 → 应原样列成 "01"
		case cmd == "AT^SYSCFGEX?":
			return []string{cmd, `^SYSCFGEX: "` + st + `",40000000,1,2,40000000,,`, "OK"}
		case strings.HasPrefix(cmd, `AT^SYSCFGEX="`):
			st = strings.SplitN(cmd, `"`, 3)[1]
			return []string{cmd, "OK"}
		}
		return []string{cmd, "OK"}
	})
	caps2, err := h.QueryModePrefCaps()
	if err != nil {
		t.Fatal(err)
	}
	if caps2.Value != common.ModeAuto || caps2.Freeform {
		t.Errorf("华为 00 应翻译成「%s」且非 freeform，实际 value=%q name=%q freeform=%v",
			common.ModeAuto, caps2.Value, caps2.Name, caps2.Freeform)
	}
	if caps2.Name == caps2.Value || caps2.Name == "" {
		t.Errorf("当前值要给中文名，实际 %q", caps2.Name)
	}
	byValue := map[string]modedialect.ModeChoice{}
	for _, ch := range caps2.Choices {
		byValue[ch.Value] = ch
	}
	// 7 档全部在、全部可写、全部有中文名
	for _, v := range modedialect.AllModePrefs {
		ch, ok := byValue[v]
		if !ok {
			t.Errorf("华为缺档位 %s", v)
			continue
		}
		if !ch.Writable || ch.Name == ch.Value {
			t.Errorf("华为 %s 应可写且有中文名：%+v", v, ch)
		}
	}
	// 枚举出但翻不出中文名的码：原样显示、可写
	if ch, ok := byValue["01"]; !ok || !ch.Writable || ch.Name != "01" {
		t.Errorf("枚举额外码应原样显示：%+v（choices=%d 项）", ch, len(caps2.Choices))
	}
	// 语义档位可写，且指令要带上后面的参数（回读→只替换第 1 个字段）
	drainSent(sent)
	after, err := h.SetModePrefCaps(common.ModeLTENR5G)
	if err != nil {
		t.Fatal(err)
	}
	if after.Value != common.ModeLTENR5G {
		t.Errorf("回读 = %q, want %q", after.Value, common.ModeLTENR5G)
	}
	want := `AT^SYSCFGEX="0803",40000000,1,2,40000000,,`
	if !hasCmd(drainSent(sent), want) {
		t.Errorf("应下发带完整参数的指令 %q", want)
	}
}

// TestLooksLikeModeCode 「原码形态」判定：拦住 5G 这类无意义输入，放行真实码形态
func TestLooksLikeModeCode(t *testing.T) {
	ok := []string{"00", "0", "2", "10", "38", "71", "0x00000109", "GSM", "TDS", "LTE_ONLY", "NR5G:SA"}
	for _, v := range ok {
		if !modedialect.LooksLikeModeCode(v) {
			t.Errorf("%q 应被当成原码", v)
		}
	}
	bad := []string{"", "5G", "4g", "1 2", "=1", "超长的根本不像码的字符串太多啦", "LTE!"}
	for _, v := range bad {
		if modedialect.LooksLikeModeCode(v) {
			t.Errorf("%q 不该被当成原码", v)
		}
	}
}

// TestSetUSBnetRejectsUnknownValue USB 网卡取值校验必须在发 AT 之前完成。
// 这两个模组都没有串口（writeChan 为空）—— 只要真的去发 AT 就会永久阻塞，测试自然失败。
func TestSetUSBnetRejectsUnknownValue(t *testing.T) {
	// 有取值表（移远：0/1/2/3/5）：表外取值必须被拒
	q := modFor(VendorQuectel, "RM520N-GL", &quectel.QuectelCommand{})
	if _, err := q.SetUSBnet("99"); err == nil {
		t.Error("移远应拒绝取值表外的 99")
	}
	if _, err := q.SetUSBnet("abc"); err == nil {
		t.Error("移远应拒绝非数字取值")
	}
	// 取值表未取证（高新兴之外没表的机型已补齐；这里用一个**没有 USB 方言**的型号不出 AT）
	telit := modFor(VendorUnknown, "N510M", &common.UnknownCommand{})
	if _, err := telit.SetUSBnet("lte"); err == nil {
		t.Error("没有 USB 方言的机型不该接受任何取值")
	}
}

// TestUSBNetDialectEnabledInEngine 引擎侧：方言表覆盖后，QueryUSBnet 用本机取值表与说明
func TestUSBNetDialectEnabledInEngine(t *testing.T) {
	withATTimeout(t)
	mod := &Modem{ID: 0, Vendor: VendorSimcom, Model: "SIM8200EA-M2", ModelName: "SIM8200EA-M2",
		cmdImpl: &simcom.SimcomCommand{}, writeChan: make(chan string, 32), readChan: make(chan string, 32)}
	fakeATServe(mod, func(cmd string) []string {
		if strings.HasPrefix(cmd, "AT+CUSBCFG?") {
			return []string{cmd, "USBID: 0X1E0E,0X9001", "OK"}
		}
		return []string{cmd, "OK"}
	})
	st, err := mod.QueryUSBnet()
	if err != nil {
		t.Fatal(err)
	}
	if st.Value != "9001" || st.Name != "QMI" {
		t.Errorf("状态 = %+v", st)
	}
	if len(st.Modes) != 2 || st.Note == "" || st.Cmd != "AT+CUSBCFG?" {
		t.Errorf("取值表/说明/指令没带上：%+v", st)
	}
}

// TestFibocomRNDISDisplayOnly 广和通的 RNDIS **只展示、不允许执行**：
//
// 现场（2026-10-09 FM160-CN）：切到 RNDIS（`2cb7:010b`）后 USB 组成里没有 AT 口，
// 页面上的 AT 功能全失联，最后靠 ADB + root 改 /fibonv/data/mode.cfg 才救回来。
func TestFibocomRNDISDisplayOnly(t *testing.T) {
	fm := modFor(VendorFibocom, "FM160-CN", &fibocom.FibocomCommand{})
	blocked := usbnetBlockedModes(fm)
	if len(blocked) == 0 {
		t.Fatalf("广和通 FM160-CN 应该有被钉住的档位，实际：%v", usbnetModes(fm))
	}
	// 表里确实有 RNDIS 这一档，而且它被钉住（原因不能是空话）
	var rndis string
	for _, m := range usbnetModes(fm) {
		if fibocom.IsRNDISModeName(m.Name) {
			rndis = m.Value
			break
		}
	}
	if rndis == "" {
		t.Fatalf("FM160-CN 的取值表里应有 RNDIS 档：%v", usbnetModes(fm))
	}
	reason := usbnetBlockedReason(fm, rndis)
	if reason == "" {
		t.Errorf("RNDIS（%s）应该被钉住", rndis)
	}
	if reason != "" && !strings.Contains(reason, "AT") {
		t.Errorf("钉住的原因要说清没有 AT 口这件事：%q", reason)
	}
	if usbnetBlockedReason(fm, "  "+strings.ToUpper(rndis)+"  ") == "" {
		t.Errorf("取值比对要大小写/空格不敏感")
	}
	// 同一台机的其它档位（QMI/ECM/MBIM…）不能被误伤
	for _, m := range usbnetModes(fm) {
		if fibocom.IsRNDISModeName(m.Name) {
			continue
		}
		if why := usbnetBlockedReason(fm, m.Value); why != "" {
			t.Errorf("%s（%s）不该被钉住：%s", m.Name, m.Value, why)
		}
	}
	// 其它厂商不受影响（移远/芯讯通 RNDIS 仍然可选）
	for _, other := range []*Modem{
		modFor(VendorQuectel, "RM500Q-GL", &quectel.QuectelCommand{}),
		modFor(VendorSimcom, "SIM8200EA-M2", &simcom.SimcomCommand{}),
	} {
		if b := usbnetBlockedModes(other); len(b) != 0 {
			t.Errorf("%s 不该有被钉住的档位：%v", other.Vendor, b)
		}
	}
}

// TestUSBModesFilteredPerModel 下拉里给出的取值必须是**本型号真能用**的那些：
// 方言取值表 × 模块支持清单。以前 openluat / Telit 的方言表是空的 ⇒ 落到通用移远表
// （0/1/2/3/5），界面上会出现"ECM=1 / RNDIS=3"这种错的值。
//
// ⚠️ 型号在清单里时取值表走**清单口径**（usb_at）：
// 顺序 = 支持列表的 usb 列顺序，取值 = 指令里那一段（见 TestCatalogUSBModeParams）。
func TestUSBModesFilteredPerModel(t *testing.T) {
	air := modFor(VendorUnknown, "Air724UG", &common.UnknownCommand{})
	airModes := usbnetModes(air)
	// 清单 usb 列 = ecm,rndis；指令 AT+SETUSB=2 / =1（QModem vendor/openluat.sh）
	if len(airModes) != 2 || airModes[0].Value != "2" || airModes[0].Name != "ECM" ||
		airModes[1].Value != "1" || airModes[1].Name != "RNDIS" {
		t.Errorf("Air724UG 取值 = %+v，want [2=ECM 1=RNDIS]（vendor/openluat.sh）", airModes)
	}
	fn := modFor(VendorUnknown, "FN990A40", &common.UnknownCommand{})
	got := map[string]string{}
	for _, m := range usbnetModes(fn) {
		got[m.Value] = m.Name
	}
	want := map[string]string{"0": "RNDIS", "1": "QMI", "2": "MBIM", "3": "ECM"}
	if len(got) != len(want) {
		t.Fatalf("FN990A40 取值 = %+v，want %+v（vendor/telit.sh 的 set_mode）", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("FN990A40 的取值 %s = %q，want %q", k, got[k], v)
		}
	}
}

// TestUSBNetOpenluatHangupBeforeSet openluat 切 USB 组合前要**先停分组数据呼叫**
// （QModem vendor/openluat.sh 的 set_mode()：`AT+RNDISCALL=0,0` 写在 `AT+SETUSB=<n>` 之前）。
//
// ⚠️ 这条指令有副作用（会断数据）⇒ 只能出现在**写入**路径上，
// 查询 USB 模式（QueryUSBnet）绝不能发它。
func TestUSBNetOpenluatHangupBeforeSet(t *testing.T) {
	withATTimeout(t)
	if d := usbDialectFor(&Modem{Model: "Air724UG", ModelName: "Air724UG"}); len(d.PreSet) != 1 || d.PreSet[0] != "AT+RNDISCALL=0,0" {
		t.Fatalf("openluat 的 preSet = %v，want [AT+RNDISCALL=0,0]", d.PreSet)
	}

	newMod := func() (*Modem, chan string) {
		m := &Modem{ID: 0, Vendor: VendorUnknown, Model: "Air724UG", ModelName: "Air724UG",
			cmdImpl: NewVendorCommand(VendorUnknown), writeChan: make(chan string, 32), readChan: make(chan string, 32)}
		sent := fakeATServe(m, func(cmd string) []string {
			if strings.HasPrefix(cmd, "AT+SETUSB?") {
				return []string{cmd, "Mode: 1", "OK"}
			}
			return []string{cmd, "OK"}
		})
		return m, sent
	}

	// ① 写入：先 RNDISCALL、再 SETUSB
	m, sent := newMod()
	if _, err := m.SetUSBnet("2"); err != nil {
		t.Fatalf("SetUSBnet: %v", err)
	}
	cmds := drainSent(sent)
	if !hasCmd(cmds, "AT+RNDISCALL=0,0") || !hasCmd(cmds, "AT+SETUSB=2") {
		t.Fatalf("发的指令 = %v，want 含 AT+RNDISCALL=0,0 与 AT+SETUSB=2", cmds)
	}
	hi, si := -1, -1
	for i, c := range cmds {
		switch c {
		case "AT+RNDISCALL=0,0":
			hi = i
		case "AT+SETUSB=2":
			si = i
		}
	}
	if hi >= si {
		t.Errorf("顺序不对：hangup 在第 %d 条、SETUSB 在第 %d 条（必须先 hangup）", hi, si)
	}

	// ② 查询：一条 RNDISCALL 都不能有
	m2, sent2 := newMod()
	if _, err := m2.QueryUSBnet(); err != nil {
		t.Fatalf("QueryUSBnet: %v", err)
	}
	if hasCmd(drainSent(sent2), "AT+RNDISCALL=0,0") {
		t.Error("查询 USB 模式不该停数据呼叫（会白断一次网）")
	}
}

// ====================== USB 网卡模式：清单口径（modem_catalog.json 的 usb_at） ======================
//
// 「USB 网卡模式」**显示哪个取值、下发哪条指令**都以模块清单为准（数据源 QModem 的
// cmds_*.sh / vendor_*.sh，取证工具 files/tools/catalogmodeat）。这里盯住三件事：
//
//	· 取值是**从指令里取出来的那一段**（清单里的十几种指令形状都要取对）；
//	· 写入时**原样照抄**清单里的指令（不再由厂商方言表拼字符串）；
//	· 清单没取证参数的型号（RG801 / A8200C-M2 / N510M…）只读 —— **不猜参数**。

// catalogModeKindOf 清单条目里"展示名 → 档位"（用于测试里回查指令）
func catalogModeKindOf(e catalog.ModemCatalogEntry, name string) string {
	for k := range e.USBModeAT.Modes {
		if catalogKindName(k) == name {
			return k
		}
	}
	return ""
}

// TestCatalogUSBModeParams 取值提取：覆盖清单里出现的每一种指令形状
func TestCatalogUSBModeParams(t *testing.T) {
	cases := []struct {
		model string
		// want 取值表（按清单 usb 列顺序）：取值:展示名
		want []string
	}{
		// 移远：AT+QCFG="usbnet",<n>（gobinet 在 QModem 里被注释 → 没有切换指令）
		{"RM520N-GL", []string{"0:QMI", "1:ECM", "2:MBIM", "3:RNDIS", "5:NCM"}},
		// 广和通高通：ECM 与 NCM 同为 18（同一条指令）→ 只留 ECM
		{"FM160-CN", []string{"32:QMI", "18:ECM", "30:MBIM", "24:RNDIS"}},
		// 广和通 MTK：只有 rndis=41
		{"FM350-GL", []string{"41:RNDIS"}},
		// 美格：AT+SER=<n>,1 —— 尾巴 ",1" 固定，取值是 2/3（**不是** 1）
		{"SRM815", []string{"2:ECM", "3:RNDIS"}},
		// Sierra：AT!USBCOMP=1,4,<位图>（两个位图共享 0x000… 前缀与 109/1009 尾巴）
		{"EM9190", []string{"0x00001009:MBIM", "0x00000109:RMNet"}},
		// 芯讯通高通：AT+CUSBCFG=usbid,1e0e,<pid>（9001/9011 共享 "90" 前缀与 "1" 尾巴）
		{"SIMCOM_SIM8200EA-M2", []string{"9001:QMI", "9011:RNDIS"}},
		// 芯讯通 lte：AT$MYCONFIG="USBNETMODE",<n>,1
		{"A7605C", []string{"1:ECM", "0:RNDIS", "2:自动"}},
		// 富士康：前缀随型号（t99w175 → AT^），取值是 4 位十六进制
		{"T99W175", []string{"90d5:MBIM", "9025:RMNet"}},
		// 高新兴：取值是**字符**不是数字
		{"GM800", []string{"x:QMI", "e:ECM", "8:MBIM", "r:RNDIS"}},
		{"ME3630-W", []string{"l:ECM"}},
		// 华为/巴龙：hisilicon 走 default 分支（ecm=0、ncm=4）；展锐分支才是 1/2/0
		{"MT5700M-CN", []string{"0:ECM", "4:NCM"}},
		{"MH5000-82", []string{"1:ECM", "2:NCM", "0:RNDIS"}},
		// openluat / Telit
		{"Air724UG", []string{"2:ECM", "1:RNDIS"}},
		{"FN990A40", []string{"2:MBIM", "0:RNDIS", "1:QMI", "3:ECM"}},
	}
	for _, c := range cases {
		e, ok := catalog.CatalogEntryForModel(c.model)
		if !ok {
			t.Errorf("%s 不在清单里", c.model)
			continue
		}
		mod := modFor(catalogVendorToModem(e.Vendor), c.model, NewVendorCommand(catalogVendorToModem(e.Vendor)))
		var got []string
		for _, m := range usbnetModes(mod) {
			got = append(got, m.Value+":"+m.Name)
		}
		if strings.Join(got, "/") != strings.Join(c.want, "/") {
			t.Errorf("%s 取值表 = %s，want %s", c.model, strings.Join(got, "/"), strings.Join(c.want, "/"))
		}
		// 每个取值都要能拼出**清单里那一条**指令（写入路径就是靠它）
		for _, m := range usbnetModes(mod) {
			cmd, err := mod.usbnetSetCommand(m.Value)
			if err != nil || cmd == "" {
				t.Errorf("%s：取值 %s 拼不出指令（%v）", c.model, m.Value, err)
				continue
			}
			if want := e.USBModeAT.Modes[catalogModeKindOf(e, m.Name)]; want != "" && want != cmd {
				t.Errorf("%s：取值 %s 的指令 = %q，清单里写的是 %q", c.model, m.Value, cmd, want)
			}
		}
	}
}

// TestCatalogUSBModesInvariants 全清单体检：取值/指令必须自洽（清单改坏了要在这里就炸）。
func TestCatalogUSBModesInvariants(t *testing.T) {
	withModes := 0
	for _, e := range catalog.ModemCatalog {
		if e.USBModeAT == nil || len(e.USBModeAT.Modes) == 0 {
			continue
		}
		withModes++
		list := catalogUSBModeList(e)
		if len(list) == 0 {
			t.Errorf("%s：有 usb_at.modes 却一个取值都取不出来（指令形状没认出来）", e.Model)
			continue
		}
		seen := map[string]string{} // 取值 → 指令
		for _, m := range list {
			if m.Value == "" || m.Cmd == "" {
				t.Errorf("%s：%s 档取值/指令为空（%+v）", e.Model, m.Kind, m)
				continue
			}
			if !strings.Contains(m.Cmd, m.Value) {
				t.Errorf("%s：取值 %q 不在指令 %q 里", e.Model, m.Value, m.Cmd)
			}
			if prev, dup := seen[m.Value]; dup && prev != m.Cmd {
				t.Errorf("%s：取值 %q 对应两条不同指令（%q / %q）", e.Model, m.Value, prev, m.Cmd)
			}
			seen[m.Value] = m.Cmd
			// 三种写法（档位名 / 界面展示名 / 本机取值）都要能写出同一条指令
			for _, w := range []string{m.Kind, catalogKindName(m.Kind), m.Value} {
				if got := catalogUSBSetCommand(e, w); got != m.Cmd {
					t.Errorf("%s：用 %q 取指令 = %q，want %q", e.Model, w, got, m.Cmd)
				}
			}
		}
		// 取值表里的取值必须都能在 ModeCmds 里查到（界面要显示"这一档会发什么"）
		cmds := catalogUSBModeCmds(e)
		for _, m := range catalogUSBModesFromAT(e, e.USB) {
			if cmds[m.Value] == "" {
				t.Errorf("%s：取值 %s 的指令没进 ModeCmds", e.Model, m.Value)
			}
		}
	}
	if withModes < 70 {
		t.Fatalf("带 usb_at.modes 的型号只剩 %d 个，清单是不是坏了", withModes)
	}
}

// TestUSBNetUnverifiedModelsAreReadOnly 清单里只有说明、没有切换指令的型号 → **只读**：
// 取值表为空（界面点不到"应用"）＋ 写入被拒且说明原因，绝不猜参数。
func TestUSBNetUnverifiedModelsAreReadOnly(t *testing.T) {
	for _, model := range []string{"RG801", "A8200C-M2", "N510M", "SIMCOM_A8200_SERIAS"} {
		e, ok := catalog.CatalogEntryForModel(model)
		if !ok || e.USBModeAT == nil {
			t.Fatalf("%s 不在清单里（或没有 usb_at）", model)
		}
		if e.USBModeAT.Known() {
			t.Fatalf("%s 不该有可下发的切换指令（清单数据变了？）", model)
		}
		mod := modFor(catalogVendorToModem(e.Vendor), model, NewVendorCommand(catalogVendorToModem(e.Vendor)))
		if modes := usbnetModes(mod); len(modes) != 0 {
			t.Errorf("%s 不该列出取值：%+v", model, modes)
		}
		// 这台"模组"没有串口（writeChan 为空）：真去发 AT 会永久阻塞，测试自然失败
		_, err := mod.SetUSBnet("2")
		if err == nil {
			t.Errorf("%s 的参数未取证，写入必须被拒", model)
		} else if !strings.Contains(err.Error(), "没有可下发的 USB 切换指令") {
			t.Errorf("%s 的报错要说清原因，实得：%v", model, err)
		}
	}

	// 有查询指令的（RG801）仍然可读；连查询都没有的（N510M）连读都不行（**不能发空指令**）
	rg, _ := catalog.CatalogEntryForModel("RG801")
	rgMod := modFor(catalogVendorToModem(rg.Vendor), "RG801", NewVendorCommand(catalogVendorToModem(rg.Vendor)))
	if q, err := rgMod.usbnetQueryCommand(); err != nil || q != `AT+QCFG="usbnet"` {
		t.Errorf("RG801 的查询指令 = %q/%v，want AT+QCFG=\"usbnet\"", q, err)
	}
	// RG801 的档位（ecm/rndis/ncm）没有切换指令 → 回显也不假装认得（不猜含义）
	if got := usbnetModeName(rgMod, "2"); got != "" {
		t.Errorf("RG801 取值 2 不该有展示名（参数未取证），实得 %q", got)
	}

	n5, _ := catalog.CatalogEntryForModel("N510M")
	n5Mod := modFor(catalogVendorToModem(n5.Vendor), "N510M", NewVendorCommand(catalogVendorToModem(n5.Vendor)))
	if q, err := n5Mod.usbnetQueryCommand(); err == nil {
		t.Errorf("N510M 清单里没有查询指令，应当报错（实得 %q）", q)
	}
	if _, err := n5Mod.QueryUSBnet(); err == nil {
		t.Error("N510M 查询应当报错，而且不能发空指令出去")
	}
}

// TestUSBNetCatalogDrivenInEngine 引擎侧：显示与下发都以清单为准（广和通 FM160-CN）。
//
// 具体到这台：清单 usb 列 = qmi/gobinet/ecm/mbim/rndis/ncm，其中
// gobinet 在 QModem 脚本里被注释（没有切换指令）→ 不列；ncm 与 ecm 同为 18 → 只留 ECM。
func TestUSBNetCatalogDrivenInEngine(t *testing.T) {
	withATTimeout(t)
	newMod := func() (*Modem, chan string) {
		m := &Modem{ID: 0, Vendor: VendorFibocom, Model: "FM160-CN", ModelName: "FM160-CN",
			cmdImpl: &fibocom.FibocomCommand{}, writeChan: make(chan string, 32), readChan: make(chan string, 32)}
		sent := fakeATServe(m, func(cmd string) []string {
			if strings.HasPrefix(cmd, "AT+GTUSBMODE?") {
				return []string{cmd, "+GTUSBMODE: 30", "OK"}
			}
			return []string{cmd, "OK"}
		})
		return m, sent
	}

	m, _ := newMod()
	st, err := m.QueryUSBnet()
	if err != nil {
		t.Fatal(err)
	}
	if st.Value != "30" || st.Name != "MBIM" {
		t.Errorf("当前值 = %+v，want 30/MBIM", st)
	}
	if st.Cmd != "AT+GTUSBMODE?" {
		t.Errorf("查询指令 = %q，want AT+GTUSBMODE?（清单 usb_at.query）", st.Cmd)
	}
	var got []string
	for _, x := range st.Modes {
		got = append(got, x.Value+":"+x.Name)
	}
	if strings.Join(got, "/") != "32:QMI/18:ECM/30:MBIM/24:RNDIS" {
		t.Errorf("取值表 = %v", got)
	}
	if st.ModeCmds["32"] != "AT+GTUSBMODE=32" || st.ModeCmds["30"] != "AT+GTUSBMODE=30" {
		t.Errorf("ModeCmds = %v", st.ModeCmds)
	}
	if !strings.Contains(st.Note, "usb_at") || !strings.Contains(st.Note, "gobinet") {
		t.Errorf("说明要写明指令来自清单、且 gobinet 没有切换指令：%s", st.Note)
	}

	// 写入：清单里的指令**原样**发出，发完回读
	m2, sent := newMod()
	st2, err := m2.SetUSBnet("30")
	if err != nil {
		t.Fatal(err)
	}
	if !hasCmd(drainSent(sent), "AT+GTUSBMODE=30") {
		t.Errorf("应原样发 AT+GTUSBMODE=30")
	}
	if st2.Value != "30" {
		t.Errorf("回读 = %+v，want 30", st2)
	}
	// 清单里没有取证的档位（gobinet）→ 拒
	if _, err := m2.SetUSBnet("gobinet"); err == nil {
		t.Error("gobinet 没有切换指令，应当被拒")
	}
}

// TestUSBNetFallbackOutsideCatalog 型号不在清单里时走老的厂商方言路径（不回归）
func TestUSBNetFallbackOutsideCatalog(t *testing.T) {
	m := modFor(VendorQuectel, "ZZZ-9999", &quectel.QuectelCommand{})
	if _, ok := catalogUSBATFor(m); ok {
		t.Fatal("ZZZ-9999 不该在清单里")
	}
	if q, err := m.usbnetQueryCommand(); err != nil || q != `AT+QCFG="usbnet"` {
		t.Errorf("查询指令 = %q/%v", q, err)
	}
	if cmd, err := m.usbnetSetCommand("2"); err != nil || cmd != `AT+QCFG="usbnet",2` {
		t.Errorf("设置指令 = %q/%v", cmd, err)
	}
	if got := usbnetModeName(m, "2"); got != "MBIM" {
		t.Errorf("回显名 = %q，want MBIM", got)
	}
}

// TestUSBNetHuaweiPlatformTables 巴龙/展锐两张码表不能搞反。
//
// 以前 vendor_settings_usb.go 把两张表**写反了**（巴龙拿了展锐的 0=rndis/1=ecm/2=ncm），
// 于是往巴龙模组下 AT^SETMODE=1 想设 ECM、实际设成了 NCM。QModem vendor/huawei.sh 里
// 只特判 unisoc，其余平台（hisilicon 等）全走 default 分支：ecm=0、ncm=4。
func TestUSBNetHuaweiPlatformTables(t *testing.T) {
	hisilicon := huawei.USBHisilicon().Modes
	for v, want := range map[string]string{"0": "ECM", "2": "ECM", "4": "NCM", "6": "RNDIS", "7": "MBIM", "8": "PPP"} {
		if got := modeNameIn(hisilicon, v); got != want {
			t.Errorf("巴龙码表 %s = %q，want %q", v, got, want)
		}
	}
	unisoc := huawei.USBUnisoc().Modes
	for v, want := range map[string]string{"0": "RNDIS", "1": "ECM", "2": "NCM"} {
		if got := modeNameIn(unisoc, v); got != want {
			t.Errorf("展锐码表 %s = %q，want %q", v, got, want)
		}
	}
	// 型号分派：MT5700M-CN（巴龙）与 MH5000-82（展锐）不能拿到同一张表
	hi := usbDialectFor(modFor(VendorTDTech, "MT5700M-CN", &tdtech.MT5700Command{}))
	if hi == nil || modeNameIn(hi.Modes, "4") != "NCM" {
		t.Errorf("MT5700M-CN 应走巴龙码表（4=NCM），实得 %+v", hi)
	}
	un := usbDialectFor(modFor(VendorUnknown, "MH5000-82", &common.UnknownCommand{}))
	if un == nil || modeNameIn(un.Modes, "0") != "RNDIS" {
		t.Errorf("MH5000-82 应走展锐码表（0=RNDIS），实得 %+v", un)
	}
}
