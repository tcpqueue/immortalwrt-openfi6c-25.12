package kernel

import (
	"modemd/module/catalog"
	"modemd/module/modelquirk"
	"os"
	"regexp"
	"strings"
	"testing"
)

// ====================== 架构护栏（不变量测试） ======================
//
// 这一组不验证"功能对不对"，而是验证**结构没被改坏**。
// 每一条都对应一次真实踩过的坑（见注释），加厂商/加型号时漏了就会在这里变红 ——
// 比在真机上发现"写出了错的值"便宜得多。

// catalogVendorToModem 清单里的厂商标识 → 运行期 ModemVendor。
//
// 只有这 6 家有专门的指令集类型（其余厂商走"自适应 + 机型特例"，见 vendorcmd.go）；
// 华为只有 MT5700*/MT5710* 算鼎桥，其余走自适应 —— 所以这里映射成 VendorUnknown。
func catalogVendorToModem(v string) ModemVendor {
	switch v {
	case "quectel":
		return VendorQuectel
	case "fibocom":
		return VendorFibocom
	case "simcom":
		return VendorSimcom
	case "meig":
		return VendorMeig
	}
	return VendorUnknown
}

// TestQuirkTableInvariants 机型特例表的结构约束。
//
// 坑（踩过两次）：`Lookup` 取的是**第一条命中的**，所以
//   - 同一个 Match 写两条 ⇒ 后面那条永远命中不到（静默失效）；
//   - 一条特例的 Match 是另一条的**后缀/子串** ⇒ 谁的顺序变了行为就跟着变。
//
// 这里钉住"Match 唯一 + 每个清单型号最多被一条命中 + 每条都得写 Note（为什么要有它）"。
func TestQuirkTableInvariants(t *testing.T) {
	seen := map[string]int{}
	for i, q := range modelquirk.Table {
		if strings.TrimSpace(q.Match) == "" {
			t.Errorf("第 %d 条特例没有 Match", i)
			continue
		}
		if _, dup := seen[strings.ToUpper(q.Match)]; dup {
			t.Errorf("Match %q 重复（Lookup 只取第一条，后面那条是死代码）", q.Match)
		}
		seen[strings.ToUpper(q.Match)]++
		if strings.TrimSpace(q.Note) == "" {
			t.Errorf("Match %q 没写 Note（为什么要有这条特例、实测依据是什么）", q.Match)
		}
	}
	// 每个清单型号最多只能被一条命中：多余的那条不会生效，只会让人误以为改了它有用
	for _, e := range catalog.ModemCatalog {
		key := strings.ToUpper(e.Model)
		var hits []string
		for _, q := range modelquirk.Table {
			if q.Match != "" && strings.Contains(key, strings.ToUpper(q.Match)) {
				hits = append(hits, q.Match)
			}
		}
		if len(hits) > 1 {
			t.Errorf("型号 %s 被 %d 条特例命中（%v）：只有第一条生效，其余是死代码",
				e.Model, len(hits), hits)
		}
	}
}

// TestVendorDialectsForEveryCatalogModel 对全部 91 个型号做一次"方言体检"：
//
//	a) USB 方言**必须给出取值表**（modes 非空）。
//	   坑：以前 Telit/openluat 的 modes 是 nil ⇒ `usbnetModes()` 回退到**通用移远表**，
//	   界面里出现 "ECM=1 / RNDIS=3" 这种**错的值**，点一下就把错的码写进模组。
//	b) 方言的查询指令不能是空串（不然界面会显示一个发不出去的指令）。
//	c) 组网模式方言必须有档位（choices 非空），否则下拉是空的。
//	d) 选择方言的函数不能 panic（型号里有连字符/空格/大写等写法）。
func TestVendorDialectsForEveryCatalogModel(t *testing.T) {
	for _, e := range catalog.ModemCatalog {
		mod := modFor(catalogVendorToModem(e.Vendor), e.Model, NewVendorCommand(catalogVendorToModem(e.Vendor)))

		if d := usbDialectFor(mod); d != nil {
			if strings.TrimSpace(d.Query) == "" {
				t.Errorf("%s：USB 方言没有查询指令", e.Model)
			}
			if len(d.Modes) == 0 {
				t.Errorf("%s：USB 方言没有取值表（会回退到通用移远表 ⇒ 写出错的值）", e.Model)
			}
			if d.Set == nil || d.Parse == nil {
				t.Errorf("%s：USB 方言缺 set/parse", e.Model)
			}
		}
		if d := modeDialectFor(mod); d != nil {
			if strings.TrimSpace(d.Query) == "" || d.Set == nil || len(d.Choices) == 0 {
				t.Errorf("%s：组网模式方言不完整（query/set/choices）", e.Model)
			}
		}
		if d := imeiDialectFor(mod); d != nil && d.Set == nil {
			t.Errorf("%s：IMEI 方言缺 set", e.Model)
		}
		// 锁网方言首选（可能为 nil = 未识别平台，允许）；只要求不 panic
		_ = mod.preferredLocker()
	}
}

// TestStdCommandsNotRedeclared 源码级护栏：厂商类型**不得重抄** stdCommands 的实现。
//
// 为什么用源码扫描而不是反射：反射只能看出"方法存在"，看不出
// "存在的那份是抄来的还是嵌入来的"。而重复的代价恰恰在后者
// （改一条标准指令要改 4~6 处，漏改不报错）。
//
// 只扫 atcmd.go（厂商指令类型的集中地），例外要写在这里并说明理由。
func TestStdCommandsNotRedeclared(t *testing.T) {
	src, err := os.ReadFile("atcmd.go")
	if err != nil {
		t.Fatalf("读 atcmd.go 失败：%v", err)
	}
	// stdCommands 里那 14 个方法：厂商类型只应**嵌入**，不该自己再写一遍
	stdMethods := []string{
		"CommandModel", "CommandCSQ", "CommandSMSTextMode", "CommandReadAllSMS",
		"CommandSendSMS", "CommandNetworkReg", "CommandIMEI", "CommandFirmware",
		"CommandSMSStorage", "CommandQuerySMSStorage", "CommandSMSNotify",
		"CommandQuerySMSNotify", "CommandDeleteSMS", "CommandOperator",
	}
	// 允许的例外：类型.方法 → 理由
	allow := map[string]string{
		"UnisocCommand.CommandModel": "展锐不支持 ATI（实测 +CME ERROR: 4），改用 AT+CGMR",
	}
	re := regexp.MustCompile(`func \(\w+ \*?(\w+)\) (\w+)\(`)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		recv, name := m[1], m[2]
		for _, std := range stdMethods {
			if name != std {
				continue
			}
			key := recv + "." + name
			if why, ok := allow[key]; ok {
				_ = why
				continue
			}
			t.Errorf("atcmd.go 里 %s 重抄了 stdCommands 的实现：删掉它（用嵌入），或在 allow 里写明理由", key)
		}
	}
}

// TestQNWLockCommandsNotRedeclared 同一个坑的另一半：QNW 系锁网那 10 条
// 在移远/广和通之间**字符级相同**（已抽到 atcmd_qnw.go 的 qnwLockCommands）。
// 这里看住"别又抄回去"。
func TestQNWLockCommandsNotRedeclared(t *testing.T) {
	src, err := os.ReadFile("atcmd.go")
	if err != nil {
		t.Fatalf("读 atcmd.go 失败：%v", err)
	}
	qnwMethods := []string{
		"CommandQueryBandLock", "CommandQueryBandCapability", "CommandQueryPolicyBand",
		"CommandSetBandLock", "CommandQueryCellLock", "CommandLockCell",
		"CommandBandLockCaps", "CommandCellLockCaps", "CommandQueryModePref", "CommandSetModePref",
	}
	// 允许的例外：鼎桥 MT5700 是**另一套**指令（AT^NRFREQLOCK / AT^SYSCFGEX），语义不同，不是重抄
	allow := map[string]bool{"tdtech.MT5700Command": true}
	re := regexp.MustCompile(`func \(\w+ \*?(\w+)\) (\w+)\(`)
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		recv, name := m[1], m[2]
		if allow[recv] {
			continue
		}
		for _, q := range qnwMethods {
			if name == q {
				t.Errorf("atcmd.go 里 %s.%s 重抄了 qnwLockCommands 的实现（移远/广和通应共用那一份）", recv, name)
			}
		}
	}
}
