package kernel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"modemd/module/atparse"
	"modemd/module/catalog"
	"modemd/module/common"
	"modemd/module/meig"
	"modemd/module/telit"
	"modemd/module/thales"
)

// ====================== 每型号 AT 指令面的回归测试 ======================

// planRow 取计划里的某条能力
func planRow(t *testing.T, p *ModemATPlan, key string) ATCmdSpec {
	t.Helper()
	if p == nil {
		t.Fatalf("计划为空")
	}
	for _, c := range p.Commands {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("计划里没有 %q 这条能力（有 %d 条）", key, len(p.Commands))
	return ATCmdSpec{}
}

// TestATPlanExposesSwitchAT 接口（/api/modules/at）里要带上"怎么切到某一档"的那条 AT。
//
// 背景：`usb_modes` 只说"本型号支持 qmi/ecm/mbim…"，但**每个型号的取值不一样**
// （移远 usbnet=1/2/3/5、广和通按平台 32/30/18/24、芯讯通是 USBID、Sierra 是接口位图…），
// 界面上选了一档要发哪条指令，唯一的出处就是清单的 `usb_at`。
func TestATPlanExposesSwitchAT(t *testing.T) {
	// 移远高通：USB 有切换指令、PCIe 只有说明
	p := ATPlanForModel("RM520N-GL")
	if p.USBSwitchAT == nil || !p.USBSwitchAT.Known() {
		t.Fatalf("RM520N-GL 的 usb_switch_at 没上来：%+v", p.USBSwitchAT)
	}
	if got := p.USBSwitchAT.ATForMode("rndis"); got != `AT+QCFG="usbnet",3` {
		t.Errorf("rndis 的切换指令不对：%q", got)
	}
	row := planRow(t, p, "usbnet_switch")
	if !row.On || row.Cmd != p.USBSwitchAT.FirstCommand() {
		t.Errorf("计划里的 usbnet_switch 不对：On=%v Cmd=%q（清单第一条是 %q）",
			row.On, row.Cmd, p.USBSwitchAT.FirstCommand())
	}
	if len(row.Candidates) != len(p.USBSwitchAT.Modes) {
		t.Errorf("候选条数 %d ≠ 模式数 %d", len(row.Candidates), len(p.USBSwitchAT.Modes))
	}
	if !strings.Contains(row.Format, "模式 → 指令") {
		t.Errorf("Format 里应该逐档列出「模式 → 指令」：%q", row.Format)
	}
	if row := planRow(t, p, "pcie_switch"); row.On || !strings.Contains(row.Note, "PCIe") {
		t.Errorf("PCIe 切换在本型号只有说明（QModem 没给指令），应该是 On=false + Note：%+v", row)
	}

	// 没有 USB 形态的型号（Foxconn T99W640）：**没有** usb_switch 这条行
	if _, ok := planRowOK(ATPlanForModel("T99W640"), "usbnet_switch"); ok {
		t.Error("T99W640 没有 USB 形态，不该有 usbnet_switch 这条能力")
	}

	// 参数未取证的型号（移远 hisilicon 的 rg801）：On=false，Note 抄清单里那句
	rg := planRow(t, ATPlanForModel("RG801"), "usbnet_switch")
	if rg.On {
		t.Errorf("rg801 的 USB 参数未取证，不该标成已接入：%+v", rg)
	}
	if !strings.Contains(rg.Note, "hisilicon") {
		t.Errorf("说明应该来自清单（写明为什么没有）：%q", rg.Note)
	}
}

// planRowOK 同 planRow，但查不到不算失败（用于"不该存在"的断言）
func planRowOK(p *ModemATPlan, key string) (ATCmdSpec, bool) {
	if p == nil {
		return ATCmdSpec{}, false
	}
	for _, c := range p.Commands {
		if c.Key == key {
			return c, true
		}
	}
	return ATCmdSpec{}, false
}

// TestATPlanEveryCatalogModel 支持清单里的**每个型号**都要能出计划，
// 且每条能力都要写清"解析实现"（否则界面上就是一句"不知道谁解析"）。
func TestATPlanEveryCatalogModel(t *testing.T) {
	for _, e := range catalog.CatalogEntries() {
		p := ATPlanForModel(e.Model)
		if p == nil {
			t.Errorf("%s：出不了 AT 计划", e.Model)
			continue
		}
		if !p.InCatalog || p.Vendor != e.Vendor {
			t.Errorf("%s：计划的厂商/清单标记不对（vendor=%q in_catalog=%v）", e.Model, p.Vendor, p.InCatalog)
		}
		if strings.TrimSpace(p.CmdImpl) == "" {
			t.Errorf("%s：指令实现为空", e.Model)
		}
		on := 0
		for _, c := range p.Commands {
			if strings.TrimSpace(c.Parser) == "" {
				t.Errorf("%s/%s：没有写解析实现", e.Model, c.Key)
			}
			if strings.TrimSpace(c.Label) == "" || strings.TrimSpace(c.Key) == "" {
				t.Errorf("%s：有条能力缺 key/label", e.Model)
			}
			if c.On {
				on++
			}
		}
		if on < 5 {
			t.Errorf("%s：已接入的能力只有 %d 条，太少（是不是指令集没挑对）", e.Model, on)
		}
	}
}

// TestATPlanQuectelBaseline 移远基线：指令集、服务小区、温度、锁网方言、USB 模式
func TestATPlanQuectelBaseline(t *testing.T) {
	p := ATPlanForModel("Quectel RM520N-GL")
	if p.CmdImpl != "QuectelCommand" {
		t.Fatalf("RM520N-GL 指令实现 = %q，want QuectelCommand", p.CmdImpl)
	}
	if got := planRow(t, p, "serving").Cmd; got != common.CmdQENGServing {
		t.Errorf("服务小区 = %q，want %q", got, common.CmdQENGServing)
	}
	if got := planRow(t, p, "temp").Cmd; got != "AT+QTEMP" {
		t.Errorf("温度 = %q，want AT+QTEMP", got)
	}
	if got := planRow(t, p, "voltage").Cmd; got != "AT+CBC" { // RM520 的特例开了电压
		t.Errorf("RM520 电压 = %q，want AT+CBC（机型特例）", got)
	}
	if !strings.Contains(p.LockDialect, "qnw") {
		t.Errorf("锁网方言 = %q，应含 qnw", p.LockDialect)
	}
	if len(p.USBModes) == 0 || len(p.PCIeModes) == 0 {
		t.Errorf("RM520N-GL 应同时有 USB/PCIe 模式：%v / %v", p.USBModes, p.PCIeModes)
	}
	if p.Quirk == "" {
		t.Errorf("RM520N-GL 应命中机型特例（时钟时区 + AT+CBC）")
	}
}

// TestATPlanVendorExtras 本轮补的厂商 AT（全部来自 QModem cmds/vendor 脚本）：
// Telit / Thales / 高新兴 / 美格 / openluat 的指令要出现在**对应型号**的计划里，
// 而且别的型号一条都不能沾。
func TestATPlanVendorExtras(t *testing.T) {
	cases := []struct {
		model string
		key   string
		want  string
	}{
		{"FN990A40", "temp", "AT#TEMPSENS=2"}, // Telit（cmds/telit.sh cmd_tempsens）
		{"FN990A40", "voltage", "AT#CBC"},     // Telit（cmd_cbc）
		{"FN990A40", "iccid", "AT+ICCID"},     // Telit（cmd_iccid）
		{"MV32-W-A", "temp", "AT^TEMP"},       // Thales（手册 §15.13）
		{"MV32-W-B", "iccid", "AT+ICCID"},     // Thales（手册 §15.10）
		{"GM800", "temp", "AT+MTSM=1"},        // 高新兴（cmds/gosuncn.sh cmd_mtsm）
		{"SRM815", "temp", "AT+TEMP"},         // 美格（cmds/meig.sh cmd_temp）
		{"AIR724UG", "serving", "AT+CCED=0,1"},
		{"AIR724UG", "neighbour", "AT+CCED=0,2"},
		{"AIR724UG", "voltage", "AT+CBC"},
	}
	for _, c := range cases {
		p := ATPlanForModel(c.model)
		if got := planRow(t, p, c.key).Cmd; got != c.want {
			t.Errorf("%s/%s = %q，want %q", c.model, c.key, got, c.want)
		}
	}
	// 别的型号不受影响（这是"只对特例型号生效"的兑现）
	if got := planRow(t, ATPlanForModel("RM500U-CNV"), "temp").Cmd; got != "AT+QTEMP" {
		t.Errorf("RM500U-CNV 温度 = %q，want AT+QTEMP（没被特例改到）", got)
	}
	if got := planRow(t, ATPlanForModel("RM500U-CNV"), "voltage").Cmd; got != "" {
		t.Errorf("RM500U-CNV 电压 = %q，want 空（该机型没开电压特例）", got)
	}
}

// TestATPlanSierraEnterCND Sierra 的高级指令要先解锁（QModem cmds/sierra.sh cmd_entercnd）
func TestATPlanSierraEnterCND(t *testing.T) {
	d := usbDialectFor(&Modem{Vendor: VendorUnknown, Model: "Sierra EM9190", ModelName: "EM9190"})
	if d == nil {
		t.Fatal("EM9190 的 USB 方言缺失")
	}
	if len(d.Pre) != 1 || !strings.Contains(d.Pre[0], `AT!ENTERCND="A710"`) {
		t.Fatalf("Sierra 应先发 AT!ENTERCND=\"A710\" 解锁，实得 %v", d.Pre)
	}
	// 别的厂商不该凭空多出一条指令
	if other := usbDialectFor(&Modem{Vendor: VendorQuectel, Model: "RM520N-GL"}); len(other.Pre) != 0 {
		t.Errorf("移远不该有进门指令：%v", other.Pre)
	}
}

// TestVendorExtraTempParsers 本轮新增的三家温度格式（样例取自 QModem 脚本的取值写法）
func TestVendorExtraTempParsers(t *testing.T) {
	telitTemps := telit.ParseTemp([]string{"AT#TEMPSENS=2", "#TEMPSENS: TSENS,43", "OK"})
	if len(telitTemps) != 1 || telitTemps[0].Name != "TSENS" || telitTemps[0].Celsius != 43 {
		t.Errorf("ParseTelitTemp = %+v", telitTemps)
	}
	thalesTemps := thales.ParseTemp([]string{"AT^TEMP", "TSENS: 33C", "OK"})
	if len(thalesTemps) != 1 || thalesTemps[0].Celsius != 33 {
		t.Errorf("ParseThalesTemp = %+v", thalesTemps)
	}
	meigTemps := meig.ParseTemp([]string{`+TEMP: "soc-thmzone",47`, `+TEMP: "cpu0-0-usr",45`})
	if len(meigTemps) != 2 || meigTemps[0].Name != "soc-thmzone" || meigTemps[1].Celsius != 45 {
		t.Errorf("ParseMeigTemp = %+v", meigTemps)
	}
	// ParseTempResp 要把它们串起来（各家固件只会中一条）
	if got := atparse.ParseTempResp([]string{"#TEMPSENS: TSENS,40"}); len(got) != 1 {
		t.Errorf("ParseTempResp 没接上 Telit 格式：%+v", got)
	}
	// 不含温度的行不能被误判
	if got := meig.ParseTemp([]string{"+TEMP: bad"}); len(got) != 0 {
		t.Errorf("ParseMeigTemp 误判：%+v", got)
	}
	if got := thales.ParseTemp([]string{"ERROR"}); len(got) != 0 {
		t.Errorf("ParseThalesTemp 误判：%+v", got)
	}
}

// TestParseCBCAndICCIDExtras Telit 的 `#CBC` 与非 3GPP 拼写的 `+ICCID`
func TestParseCBCAndICCIDExtras(t *testing.T) {
	if got := atparse.ParseCBC([]string{"#CBC: 0,4130"}); got != "4.130 V" {
		t.Errorf("ParseCBC(#CBC) = %q，want 4.130 V", got)
	}
	if got := atparse.ParseCBC([]string{"+CBC: 0,3799"}); got != "3.799 V" {
		t.Errorf("ParseCBC(+CBC 两字段) = %q，want 3.799 V", got)
	}
	iccid := "89860324040255993152"
	if got := atparse.ParseICCID([]string{"+ICCID: " + iccid}); got != iccid {
		t.Errorf("ParseICCID(+ICCID) = %q", got)
	}
	if got := atparse.ParseICCID([]string{"+CCID: " + iccid}); got != iccid {
		t.Errorf("ParseICCID(+CCID) = %q", got)
	}
}

// TestCCEDRowKind 已删：`+CCED` 的"识形"与解析统一在 vendor_cell_extra.go 的
// `openluatCCEDLine()`（见 TestParseOpenluatCCED* / TestOpenluatCCEDRejectsWrongFieldCount）。

// TestATPlanMatrixMarkdown 速查表：表头 + 每个型号一行（文档 F 节靠它生成）
func TestATPlanMatrixMarkdown(t *testing.T) {
	md := ATPlanMatrixMarkdown()
	lines := strings.Split(strings.TrimRight(md, "\n"), "\n")
	if len(lines) != len(catalog.ModemCatalog)+2 {
		t.Fatalf("速查表行数 = %d，want %d（表头 2 行 + %d 个型号）", len(lines), len(catalog.ModemCatalog)+2, len(catalog.ModemCatalog))
	}
	if !strings.Contains(lines[0], "USB 模式方言") {
		t.Errorf("表头不对：%s", lines[0])
	}
	// 每行都要有 15 个分隔符（14 列：含 AMBR / CA）
	for _, l := range lines[2:] {
		if n := strings.Count(l, "|"); n != 15 {
			t.Errorf("行分列不对（%d 个 |）：%s", n, l)
		}
	}
	gaps := ATPlanGapsMarkdown()
	if !strings.Contains(gaps, "| 能力 | 未接入的型号数 |") {
		t.Errorf("未接入清单表头不对：%s", gaps)
	}
}

// TestModulesAPI GET /api/modules 与 /api/modules?model=…
//
// 这两条路都不需要模组（清单是静态数据、按型号出指令面也是纯推导），
// 所以 mgr 传 nil 即可 —— 顺便钉住"没有模组时页面也能看清单"。
func TestModulesAPI(t *testing.T) {
	h := apiModules(nil)

	// ① 全量清单
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodGet, "/api/modules", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("清单接口状态 = %d", rr.Code)
	}
	var list struct {
		Code int `json:"code"`
		Data struct {
			Count     int `json:"count"`
			USBCount  int `json:"usb_count"`
			PCIeCount int `json:"pcie_count"`
			Vendors   []struct {
				Vendor string `json:"vendor"`
			} `json:"vendors"`
			Models []struct {
				Model     string   `json:"model"`
				USBModes  []string `json:"usb_modes"`
				PCIeModes []string `json:"pcie_modes"`
			} `json:"models"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("清单 JSON 解析失败：%v", err)
	}
	if list.Code != 0 || list.Data.Count != len(catalog.ModemCatalog) {
		t.Fatalf("清单条数 = %d（code=%d），want %d", list.Data.Count, list.Code, len(catalog.ModemCatalog))
	}
	if len(list.Data.Vendors) != len(catalog.CatalogVendorOrder) {
		t.Errorf("厂商数 = %d，want %d", len(list.Data.Vendors), len(catalog.CatalogVendorOrder))
	}
	found := false
	for _, m := range list.Data.Models {
		if m.Model == "rm520n-gl" {
			found = true
			if len(m.USBModes) == 0 || len(m.PCIeModes) == 0 {
				t.Errorf("rm520n-gl 的模式不全：%v / %v", m.USBModes, m.PCIeModes)
			}
		}
	}
	if !found {
		t.Errorf("清单里没有 rm520n-gl")
	}

	// ② 按型号出指令面
	rr2 := httptest.NewRecorder()
	h(rr2, httptest.NewRequest(http.MethodGet, "/api/modules?model=FN990A40", nil))
	var one struct {
		Data struct {
			Plan ModemATPlan `json:"plan"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &one); err != nil {
		t.Fatalf("按型号 JSON 解析失败：%v", err)
	}
	if one.Data.Plan.Model == "" || len(one.Data.Plan.Commands) == 0 {
		t.Fatalf("按型号没拿到指令面：%+v", one.Data.Plan)
	}
	if got := planRow(t, &one.Data.Plan, "temp").Cmd; got != "AT#TEMPSENS=2" {
		t.Errorf("FN990A40 温度 = %q，want AT#TEMPSENS=2", got)
	}

	// ③ 非 GET 要拒
	rr3 := httptest.NewRecorder()
	h(rr3, httptest.NewRequest(http.MethodPost, "/api/modules", nil))
	var bad struct {
		Code int `json:"code"`
	}
	_ = json.Unmarshal(rr3.Body.Bytes(), &bad)
	if bad.Code == 0 {
		t.Errorf("POST /api/modules 应该被拒")
	}
}

// TestATPlanMatrixRowsMatchCatalogOrder 表里型号顺序必须与清单一致（文档对照时好找）
func TestATPlanMatrixRowsMatchCatalogOrder(t *testing.T) {
	md := ATPlanMatrixMarkdown()
	idx := 0
	for _, e := range catalog.ModemCatalog {
		i := strings.Index(md[idx:], "| "+e.Model+" |")
		if i < 0 {
			t.Fatalf("速查表里找不到 %s", e.Model)
		}
		idx += i
	}
}
