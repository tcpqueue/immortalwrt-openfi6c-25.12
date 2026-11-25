package kernel

import (
	"testing"

	"modemd/module/common"
	"modemd/module/fibocom"
	"modemd/module/gosuncn"
	"modemd/module/lock"
	"modemd/module/openluat"
	"modemd/module/platform"
	"modemd/module/quectel"
	"modemd/module/simcom"
	"modemd/module/tdtech"
	"modemd/module/telit"
)

// TestPlatformForModel 平台识别（对照 QModem docs/support_list.md）
func TestPlatformForModel(t *testing.T) {
	cases := []struct {
		model string
		want  platform.ModemPlatform
	}{
		// MTK
		{"FM350-GL", platform.PlatformMediaTek},
		{"Fibocom FM350R-GL", platform.PlatformMediaTek},
		{"RW350R-GL", platform.PlatformMediaTek},
		// 海思巴龙
		{"MT5700M-CN", platform.PlatformHiSilicon},
		{"MT5710_CN", platform.PlatformHiSilicon},
		{"MH5000-31", platform.PlatformHiSilicon},
		{"Quectel RG801", platform.PlatformHiSilicon},
		// 展锐
		{"FM650-CN", platform.PlatformUnisoc},
		{"RM500U-CNV", platform.PlatformUnisoc},
		{"L610-EU", platform.PlatformUnisoc},
		{"SRM821", platform.PlatformUnisoc},
		{"N510M", platform.PlatformUnisoc},
		{"MH5000-82", platform.PlatformUnisoc},
		// ASR
		{"A8200C-M2", platform.PlatformASR},
		// 高通
		{"FM160-EAU", platform.PlatformQualcomm},
		{"RM502Q-GL", platform.PlatformQualcomm},
		{"RM520N-GL", platform.PlatformQualcomm},
		{"SRM815", platform.PlatformQualcomm},
		{"SIM8200EA-M2", platform.PlatformQualcomm},
		{"T99W175", platform.PlatformQualcomm},
		{"EM9190", platform.PlatformQualcomm},
		{"FN990A40", platform.PlatformQualcomm},
		{"GM800", platform.PlatformQualcomm},
		// 认不出
		{"SOME UNKNOWN MODEM", platform.PlatformUnknown},
		{"", platform.PlatformUnknown},
	}
	for _, c := range cases {
		if got := platform.PlatformForModel(c.model); got != c.want {
			t.Errorf("PlatformForModel(%q) = %q, want %q", c.model, got, c.want)
		}
	}
}

// TestS10WordBoundary 短关键字（华为 S10）只按词命中：
// "S10" 要认，但 "S100"/"MS10"/"S10C" 这类串不能误识别。
func TestS10WordBoundary(t *testing.T) {
	for _, m := range []string{"S10", "HUAWEI S10", "Huawei S10-123"} {
		if got := platform.PlatformForModel(m); got != platform.PlatformHiSilicon {
			t.Errorf("PlatformForModel(%q) = %q, want hisilicon", m, got)
		}
	}
	for _, m := range []string{"S100", "MS10", "S10C", "XS10Y"} {
		if got := platform.PlatformForModel(m); got != platform.PlatformUnknown {
			t.Errorf("PlatformForModel(%q) = %q，短关键字不应命中", m, got)
		}
	}
}

// supportedListModels QModem `docs/support_list.md` 里的**全部型号**（按厂商分组）与预期平台。
// "" = 我们**故意不猜**（不是漏了）：平台确实未知或方言待探测。
//
// 这张表的作用：① 看得见哪些型号已覆盖；② 改成 token 列表时一旦漏型号/顺序错，
// 这里立刻报错（这类问题不会在运行时报错，只会“悄悄进了错的方言分支”）。
var supportedListModels = []struct {
	vendor string
	model  string
	want   platform.ModemPlatform
}{
	// openluat
	{"openluat", "air724ug", platform.PlatformUnisoc},
	// huawei
	{"huawei", "s10", platform.PlatformHiSilicon},
	{"huawei", "mt5700m-cn", platform.PlatformHiSilicon},
	{"huawei", "mt5710_cn", platform.PlatformHiSilicon},
	{"huawei", "mh5000-31", platform.PlatformHiSilicon},
	{"huawei", "mh5000-82", platform.PlatformUnisoc},
	{"huawei", "mh5000-82m", platform.PlatformUnisoc},
	// quectel
	{"quectel", "em05", platform.PlatformQualcomm},
	{"quectel", "em05-cn", platform.PlatformQualcomm},
	{"quectel", "ep06", platform.PlatformQualcomm},
	{"quectel", "em12", platform.PlatformQualcomm},
	{"quectel", "em120k", platform.PlatformQualcomm},
	{"quectel", "em160r-gl", platform.PlatformQualcomm},
	{"quectel", "ag598eeu", platform.PlatformQualcomm},
	{"quectel", "rg200u-cn", platform.PlatformUnisoc},
	{"quectel", "rg801", platform.PlatformHiSilicon},
	{"quectel", "rm500u-cn", platform.PlatformUnisoc},
	{"quectel", "rm500u-cnv", platform.PlatformUnisoc},
	{"quectel", "rm500u-ea", platform.PlatformUnisoc},
	{"quectel", "rm500s-ce", platform.PlatformQualcomm},
	{"quectel", "rm500q-cn", platform.PlatformQualcomm},
	{"quectel", "rm500q-ae", platform.PlatformQualcomm},
	{"quectel", "rm500q-gl", platform.PlatformQualcomm},
	{"quectel", "rm502q-ae", platform.PlatformQualcomm},
	{"quectel", "rm502q-gl", platform.PlatformQualcomm},
	{"quectel", "rm505q-ae", platform.PlatformQualcomm},
	{"quectel", "rm510q-gl", platform.PlatformQualcomm},
	{"quectel", "rm510q-glha", platform.PlatformQualcomm},
	{"quectel", "rm520n-cn", platform.PlatformQualcomm},
	{"quectel", "rm520n-eu", platform.PlatformQualcomm},
	{"quectel", "rm520n-gl", platform.PlatformQualcomm},
	{"quectel", "rm520f-gl", platform.PlatformQualcomm},
	{"quectel", "rm520f-bl", platform.PlatformQualcomm},
	{"quectel", "rm521f-gl", platform.PlatformQualcomm},
	{"quectel", "rm530n-gl", platform.PlatformQualcomm},
	{"quectel", "rm551e-gl", platform.PlatformQualcomm},
	{"quectel", "rg500q-ea", platform.PlatformQualcomm},
	{"quectel", "rg501q-eu", platform.PlatformQualcomm},
	{"quectel", "rg520n-eu", platform.PlatformQualcomm},
	{"quectel", "rg520n-eb", platform.PlatformQualcomm},
	{"quectel", "rg520f-eu", platform.PlatformQualcomm},
	{"quectel", "rg520f-eb", platform.PlatformQualcomm},
	{"quectel", "rg520f-jo", platform.PlatformQualcomm},
	{"quectel", "rg530f-na", platform.PlatformQualcomm},
	{"quectel", "rg650v-eu", platform.PlatformQualcomm},
	{"quectel", "mh8001-eu", platform.PlatformQualcomm},
	{"quectel", "ec20f", platform.PlatformQualcomm},
	{"quectel", "ec25", platform.PlatformQualcomm},
	{"quectel", "eg25", platform.PlatformQualcomm},
	{"quectel", "ec21", platform.PlatformQualcomm},
	{"quectel", "ec200a", platform.PlatformQualcomm},
	// fibocom
	{"fibocom", "fm650-cn", platform.PlatformUnisoc},
	{"fibocom", "fm350-gl", platform.PlatformMediaTek},
	{"fibocom", "fm350-gl-00", platform.PlatformMediaTek},
	{"fibocom", "fm350r-gl", platform.PlatformMediaTek},
	{"fibocom", "rw350r-gl", platform.PlatformMediaTek},
	{"fibocom", "fm150-ae", platform.PlatformQualcomm},
	{"fibocom", "fm160-cn", platform.PlatformQualcomm},
	{"fibocom", "fm160-eau", platform.PlatformQualcomm},
	{"fibocom", "fm160-na", platform.PlatformQualcomm},
	{"fibocom", "fm170-eau", platform.PlatformQualcomm},
	{"fibocom", "fm190w-gl", platform.PlatformQualcomm},
	{"fibocom", "l610-eu", platform.PlatformUnisoc},
	{"fibocom", "nl668", platform.PlatformQualcomm},
	{"fibocom", "nl678", platform.PlatformQualcomm},
	// meig
	{"meig", "srm815", platform.PlatformQualcomm},
	{"meig", "srm825", platform.PlatformQualcomm},
	{"meig", "srm825n", platform.PlatformQualcomm},
	{"meig", "srm821", platform.PlatformUnisoc},
	// sierra
	{"sierra", "em9190", platform.PlatformQualcomm},
	{"sierra", "em9291", platform.PlatformQualcomm},
	{"sierra", "mc7354", platform.PlatformQualcomm},
	{"sierra", "mc7355", platform.PlatformQualcomm},
	// telit
	{"telit", "fn990a40", platform.PlatformQualcomm},
	// simcom
	{"simcom", "simcom_a8200_serias", platform.PlatformASR},
	{"simcom", "a8200c-m2", platform.PlatformASR},
	{"simcom", "a7605c", platform.PlatformUnknown}, // 4G 机型，QModem 只标了 "lte"：平台不猜，方言靠运行期探测
	{"simcom", "simcom_d8200g", platform.PlatformQualcomm},
	{"simcom", "simcom_sim8200ea-m2", platform.PlatformQualcomm},
	{"simcom", "simcom_sim8202g-m2", platform.PlatformQualcomm},
	{"simcom", "simcom_sim8380g-m2", platform.PlatformQualcomm},
	// foxconn
	{"foxconn", "t77w968", platform.PlatformQualcomm},
	{"foxconn", "t99w175", platform.PlatformQualcomm},
	{"foxconn", "t99w373", platform.PlatformQualcomm},
	{"foxconn", "t99w368", platform.PlatformQualcomm},
	{"foxconn", "t99w640", platform.PlatformQualcomm},
	// thales
	{"thales", "mv32-w-a", platform.PlatformQualcomm},
	{"thales", "mv32-w-b", platform.PlatformQualcomm},
	// neoway
	{"neoway", "n510m", platform.PlatformUnisoc},
	// gosuncn
	{"gosuncn", "me3630-w", platform.PlatformUnknown}, // 4G 机型：平台不猜（锁网方言由 isGosuncnModel 走 ZBAND）
	{"gosuncn", "gm800", platform.PlatformQualcomm},
}

// TestPlatformForAllSupportedModels 支持清单里的**每一个型号**都要落在预期的平台上。
func TestPlatformForAllSupportedModels(t *testing.T) {
	for _, c := range supportedListModels {
		if got := platform.PlatformForModel(c.model); got != c.want {
			t.Errorf("%s/%s: PlatformForModel = %q, want %q", c.vendor, c.model, got, c.want)
		}
	}
}

// TestSupportedModelsHaveLockPath 支持清单里的每个型号，要么能定出锁网方言（机型谓词 / 平台回退），
// 要么在下面的 noFallbackOK 里**显式**登记为"故意不猜"。
//
// 重点：这条测试把"支持清单漏了型号"变成可见错误 —— 以前靠人工对表，漏了不会报错，
// 只会在真机上悄悄走错方言分支（或直接报"不支持"）。
func TestSupportedModelsHaveLockPath(t *testing.T) {
	// 故意不给回退猜测的型号 → 原因（真机能否锁网交给运行期 10 套候选探测）
	noFallbackOK := map[string]string{
		"a7605c":              "4G 机型，QModem 只标了平台 \"lte\"：芯片平台与方言都没把握，不猜",
		"simcom_a8200_serias": "ASR 平台：锁网方言未确认（QModem 的 simcom 脚本里没有 A8200 分支）",
		"a8200c-m2":           "同上（ASR）",
	}
	vendorOf := map[string]ModemVendor{
		"quectel": VendorQuectel,
		"fibocom": VendorFibocom,
		"simcom":  VendorSimcom,
		"meig":    VendorMeig,
		"huawei":  VendorTDTech,
		"tdtech":  VendorTDTech,
	}
	for _, c := range supportedListModels {
		model := c.model
		l := &Modem{ID: 0, Vendor: vendorOf[c.vendor], Model: model, ModelName: model, cmdImpl: &common.UnknownCommand{}}
		got := l.preferredLocker()
		if got != nil {
			continue
		}
		if _, ok := noFallbackOK[model]; ok {
			continue
		}
		t.Errorf("%s/%s：既没有方言回退、也没登记进 noFallbackOK（补 token/谓词，或写明为什么故意不猜）",
			c.vendor, model)
	}
}

// TestLockDialectForEveryVendorFamily 支持清单里每家厂商至少有一个型号能定出**专属**方言，
// 即“全部厂商都能锁网”不是空话。
func TestLockDialectForEveryVendorFamily(t *testing.T) {
	familyOf := func(l NetLocker) string {
		switch l.(type) {
		case *lock.BandListLocker:
			return lock.DialectName(l)
		case *telit.TelitLocker:
			return lock.DialectName(l)
		case *lock.AtBandLocker:
			return "atband"
		case *openluat.OpenluatLockCommand:
			return "openluat"
		case *gosuncn.GosuncnLockCommand:
			return "gosuncn"
		case *simcom.SimcomLockCommand:
			return "simcom"
		case *fibocom.FibocomMTKCommand:
			return "mtk"
		case *tdtech.MT5700Command:
			return "balong"
		case *quectel.QuectelCommand:
			return "qnw"
		}
		return ""
	}
	// 厂商 → 代表性型号（能定位到专属方言的）
	cases := map[string]string{
		"quectel":  "rm520n-gl",
		"fibocom":  "fm350-gl",
		"simcom":   "simcom_sim8200ea-m2",
		"huawei":   "mt5700m-cn",
		"tdtech":   "MT5700M-CN",
		"gosuncn":  "gm800",
		"telit":    "fn990a40",
		"thales":   "mv32-w-a",
		"foxconn":  "t99w175",
		"neoway":   "n510m",
		"openluat": "air724ug",
	}
	for vendor, model := range cases {
		l := &Modem{ID: 0, Vendor: VendorUnknown, Model: model, ModelName: model, cmdImpl: &common.UnknownCommand{}}
		if familyOf(l.preferredLocker()) == "" {
			t.Errorf("%s（%s）：没定出锁网方言", vendor, model)
		}
	}
}

// TestIsSimcomLockModel 芯讯通自家锁网型号
func TestIsSimcomLockModel(t *testing.T) {
	for _, m := range []string{"SIM8200EA-M2", "simcom_sim8202g-m2", "SIM8380G-M2", "D8200G"} {
		if !platform.IsSimcomLockModel(m) {
			t.Errorf("%q 应判为芯讯通锁网型号", m)
		}
	}
	for _, m := range []string{"RM502Q-GL", "FM350-GL", ""} {
		if platform.IsSimcomLockModel(m) {
			t.Errorf("%q 不该判为芯讯通锁网型号", m)
		}
	}
}

// TestLockerForPlatform 锁网实现按**平台**分派：
//
//	MTK(FM350) → AT+GTACT；海思(MT5700) → ^FREQLOCK；芯讯通 → CSYSSEL；
//	高通 → 厂商自带（移远/广和通…）或通用 QNW；认不出平台 → 厂商默认（可能不支持）。
func TestLockerForPlatform(t *testing.T) {
	cases := []struct {
		name    string
		mod     *Modem
		wantNil bool
		check   func(*testing.T, NetLocker)
	}{
		{
			name: "MTK FM350",
			mod:  &Modem{Vendor: VendorFibocom, Model: "Fibocom FM350-GL", ModelName: "FM350-GL", cmdImpl: &fibocom.FibocomCommand{}},
			check: func(t *testing.T, l NetLocker) {
				if _, ok := l.(*fibocom.FibocomMTKCommand); !ok {
					t.Errorf("应为 MTK 实现，实际 %T", l)
				}
			},
		},
		{
			name: "展锐 广和通 FM650",
			mod:  &Modem{Vendor: VendorFibocom, Model: "Fibocom FM650-CN", ModelName: "FM650-CN", cmdImpl: &fibocom.FibocomCommand{}},
			check: func(t *testing.T, l NetLocker) {
				if _, ok := l.(*fibocom.FibocomMTKCommand); !ok {
					t.Errorf("FM650（展锐）也应走广和通 GT 方言，实际 %T", l)
				}
				if got := l.CommandQueryCellLock(CellTechNR); got != "AT+GTCELLLOCK?" {
					t.Errorf("FM650 锁小区查询 = %q", got)
				}
			},
		},
		{
			name: "海思 MT5700",
			mod:  &Modem{Vendor: VendorTDTech, Model: "MT5700M-CN", ModelName: "MT5700M-CN", cmdImpl: &tdtech.MT5700Command{}},
			check: func(t *testing.T, l NetLocker) {
				if _, ok := l.(*tdtech.MT5700Command); !ok {
					t.Errorf("应为 MT5700 实现，实际 %T", l)
				}
			},
		},
		{
			name: "芯讯通 SIM8200",
			mod:  &Modem{Vendor: VendorSimcom, Model: "SIM8200EA-M2", ModelName: "SIM8200EA-M2", cmdImpl: &simcom.SimcomCommand{}},
			check: func(t *testing.T, l NetLocker) {
				if _, ok := l.(*simcom.SimcomLockCommand); !ok {
					t.Errorf("应为芯讯通实现，实际 %T", l)
				}
			},
		},
		{
			name: "高通 移远 RM502Q",
			mod:  &Modem{Vendor: VendorQuectel, Model: "RM502Q-GL", ModelName: "RM502Q-GL", cmdImpl: &quectel.QuectelCommand{}},
			check: func(t *testing.T, l NetLocker) {
				if got := l.CommandQueryBandLock(CellTechNR); got != `AT+QNWPREFCFG="nr5g_band"` {
					t.Errorf("应为 QNW，实际 %q", got)
				}
			},
		},
		{
			name: "高新兴 GM800",
			mod:  &Modem{Vendor: VendorUnknown, Model: "GM800", ModelName: "GM800", cmdImpl: &common.UnknownCommand{}},
			check: func(t *testing.T, l NetLocker) {
				if _, ok := l.(*gosuncn.GosuncnLockCommand); !ok {
					t.Errorf("GM800 应为高新兴实现，实际 %T", l)
				}
			},
		},
		{
			name: "高通 其它厂商（Sierra EM9190）回退通用 QNW",
			mod:  &Modem{Vendor: VendorUnknown, Model: "EM9190", ModelName: "EM9190", cmdImpl: &common.UnknownCommand{}},
			check: func(t *testing.T, l NetLocker) {
				if l == nil {
					t.Fatal("高通平台应拿到通用 QNW 实现")
				}
				if got := l.CommandQueryBandLock(CellTechLTE); got != `AT+QNWPREFCFG="lte_band"` {
					t.Errorf("应为通用 QNW，实际 %q", got)
				}
			},
		},
		{
			name:    "认不出平台 + 未实现锁网的厂商",
			mod:     &Modem{Vendor: VendorSimcom, Model: "SOME MODEM", cmdImpl: &simcom.SimcomCommand{}},
			wantNil: true,
		},
	}
	for _, c := range cases {
		l := c.mod.lockerFor()
		if c.wantNil {
			if l != nil {
				t.Errorf("%s: 期望 nil，实际 %T", c.name, l)
			}
			continue
		}
		if l == nil {
			t.Fatalf("%s: 不该为 nil", c.name)
		}
		if c.check != nil {
			c.check(t, l)
		}
	}
}

// TestPlatformLabel 平台中文名（未识别不编名字）
func TestPlatformLabel(t *testing.T) {
	if platform.PlatformLabel(platform.PlatformMediaTek) == "" || platform.PlatformLabel(platform.PlatformUnknown) == "" {
		t.Error("平台名不该为空")
	}
}
