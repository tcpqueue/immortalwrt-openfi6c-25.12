package kernel

// 「常用 AT 指令」清单的回归测试（跨平台）。
//
// 核心不变量只有一条，但它是**用户明确提的需求**：
//   这个模块的下拉里**只能出现它自己支持的指令**，别的厂商的一条都不许有。
// 以前是个写死的大杂烩（移远 AT+Q* 和广和通 AT+GT* 混在一起），
// 插别的模组时下拉里大半是注定回 ERROR 的指令 —— 这些测试就是防止它回退。

import (
	"modemd/module/atpreset"
	"modemd/module/unisoc"
	"strings"
	"testing"
)

func presetCmds(list []atpreset.ATPreset) []string {
	out := make([]string, 0, len(list))
	for _, p := range list {
		out = append(out, p.Cmd)
	}
	return out
}

func containsCmd(list []atpreset.ATPreset, cmd string) bool {
	for _, p := range list {
		if strings.EqualFold(p.Cmd, cmd) {
			return true
		}
	}
	return false
}

// 一份清单里**不许**出现的子串（跨厂商泄漏）
func TestATPresetsNoCrossVendorLeak(t *testing.T) {
	cases := []struct {
		vendor    ModemVendor
		forbidden []string
		// allow 命中 forbidden 但**确实是本机固件的指令**的例外（写全名，不写前缀）
		allow []string
	}{
		// 未识别厂商：只能有 3GPP 标准指令，一条厂商私有的都不许有
		{VendorUnknown, []string{"AT+Q", "AT+FENG", "AT+GT", "AT+SP", "AT^"}, nil},
		// 移远：不能混进广和通/展锐/鼎桥/芯讯通的私有指令
		{VendorQuectel, []string{"AT+FENG", "AT+GTCCINFO", "AT+GTSTATIS", "AT+SPRAT", "AT+SFUN",
			"AT^", "AT+CNWINFO", "AT+CPMUTEMP", "AT+CPSI"}, nil},
		// 广和通：不能混进移远的 QENG/QTEMP/QGDCNT（它自己的 QNWPREFCFG/QNWLOCK 是允许的）
		{VendorFibocom, []string{"AT+QENG", "AT+QGDCNT", "AT+QTEMP", "AT+QNWCFG", "AT+QCAINFO",
			"AT^", "AT+SP", "AT+CPMUTEMP"}, nil},
		// 芯讯通：不能混进移远/广和通的锁网与信号指令
		{VendorSimcom, []string{"AT+QENG", "AT+QNWPREFCFG", "AT+QNWLOCK", "AT+FENG", "AT+GT",
			"AT^", "AT+SP", "AT+QTEMP"}, nil},
		// 美格：复用移远那一套，所以不能混进别家的
		{VendorMeig, []string{"AT+FENG", "AT+GT", "AT^", "AT+SP", "AT+CNWINFO", "AT+CPMUTEMP"}, nil},
		// 鼎桥：全在 ^ 私有命令里，一条 AT+Q* 都不许有
		{VendorTDTech, []string{"AT+Q", "AT+FENG", "AT+GT", "AT+SP", "AT+CPMUTEMP", "AT+CNWINFO"}, nil},
		// 展锐：ATI 是**不支持**的（回 +CME ERROR: 4），也不许混进别家私有指令。
		// ⚠️ 例外：本机固件（中兴 F50 / UMS9620）**真的实现了** `AT^SYSCFGEX`
		// —— 2026-10-06 真机取证：读得到 5 个字段、写得回、=? 里列了 LTE 频段名单，
		// 它是本机唯一能读能写的锁频段指令（详见 lock_unisoc.go 文件头）。
		{VendorUnisoc, []string{"AT+Q", "AT+FENG", "AT+GT", "AT^", "AT+CNWINFO", "AT+CPMUTEMP", "AT+QTEMP"},
			[]string{"AT^SYSCFGEX?", "AT^SYSCFGEX=?"}},
	}
	for _, c := range cases {
		t.Run(string(c.vendor), func(t *testing.T) {
			list := ATPresetsFor(c.vendor)
			if len(list) == 0 {
				t.Fatal("清单不该为空")
			}
			for _, cmd := range presetCmds(list) {
				up := strings.ToUpper(cmd)
				allowed := false
				for _, a := range c.allow {
					if up == strings.ToUpper(a) {
						allowed = true
						break
					}
				}
				if allowed {
					continue
				}
				for _, bad := range c.forbidden {
					if strings.Contains(up, strings.ToUpper(bad)) {
						t.Errorf("厂商 %s 的清单里出现了不该有的指令 %q（命中 %q）", c.vendor, cmd, bad)
					}
				}
			}
		})
	}
}

// 展锐（中兴 F50）的几条硬事实，任何一条回退都会让界面骗人。
func TestUnisocPresetsFacts(t *testing.T) {
	list := ATPresetsFor(VendorUnisoc)

	// ⚠️ ATI 在这台机器上会回 `+CME ERROR: 4`，绝不能列出来
	for _, p := range list {
		if strings.EqualFold(strings.TrimSpace(p.Cmd), "ATI") {
			t.Fatal("展锐清单里不许有 ATI（F50 实机回 +CME ERROR: 4）")
		}
	}
	// 必须有的几条：型号要用 CGMR；IMEI 要能给到 SPIMEI；
	// 短信必须 PDU 模式；组网模式是唯一能回读的锁网项；5G 开关是本机唯一可写的组网模式旋钮
	for _, want := range []string{"AT+CGMR", "AT+SPIMEI?", "AT+CMGF=0", "AT+CMGL=4", "AT+SPRAT?", "AT+SP5GRAN?"} {
		if !containsCmd(list, want) {
			t.Errorf("展锐清单里缺少 %q", want)
		}
	}
	// 锁网指令的参数码表**没有取证**，所以只给 `=?`（查本机接受什么），
	// 不能给"看起来像真的"的写死取值 —— 那会把用户导向错误的锁网。
	// 例外是本机**实测可用**的写法（真机验过、能安全回滚）：
	//   · 小区锁定解除 `AT+SPCELLLOCK=0,0,0`（回 +SPCELLLOCK:0,0,0,0，不影响驻网）
	//   · 5G 开关 `AT+SP5GRAN=0/1`（组网模式，不属于这条前缀判定）
	verified := map[string]bool{"AT+SPCELLLOCK=0,0,0": true}
	for _, p := range list {
		if strings.HasPrefix(strings.ToUpper(p.Cmd), "AT+SPCOMMLOCKBAND") ||
			strings.HasPrefix(strings.ToUpper(p.Cmd), "AT+SPCELLLOCK") ||
			strings.HasPrefix(strings.ToUpper(p.Cmd), "AT+SPCOMMLOCKFRQ") {
			if p.Cmd != "AT+SPCOMMLOCKBAND=?" && p.Cmd != "AT+SPCELLLOCK=?" && p.Cmd != "AT+SPCOMMLOCKFRQ=?" &&
				!verified[p.Cmd] {
				t.Errorf("展锐锁网项只该给 `=?` 形式（参数码表未取证）或已实测的解除形态，实际 %q", p.Cmd)
			}
			if p.Tpl {
				t.Errorf("展锐锁网项不该是模板（不能推荐未验证的写法）：%q", p.Cmd)
			}
		}
		// 写组网模式的 SPRAT 语法**没有取证**（AT+SPRAT 只能读，三种写法都被拒），不许列出来；
		// 5G 开关 AT+SP5GRAN 是实测过的，允许（上面 wait 列表里钉着它存在）。
		if strings.HasPrefix(strings.ToUpper(p.Cmd), `AT+SPRAT=`) {
			t.Errorf("写组网模式的 SPRAT 写法未取证，不该出现在清单里：%q", p.Cmd)
		}
	}
}

// ⚠️⚠️ 两次真机事故（2026-10-05 / 10-06）：`AT+SPENGMD`（任何 type ≥ 1）与**带参数**的
// `AT+SPFREQSCAN` 会把模组 AT 队列堵死十几分钟，第二次连厂商数据面一起掉（外网不通）。
//
// 预设是用户在「AT 指令」页**点一下就会发出去**的东西 ⇒ 这类指令绝不能出现在预设里。
func TestNoWedgingPresets(t *testing.T) {
	vendors := []ModemVendor{VendorUnisoc, VendorUnknown, VendorQuectel, VendorFibocom,
		VendorSimcom, VendorMeig, VendorTDTech}
	for _, v := range vendors {
		for _, p := range ATPresetsFor(v) {
			up := strings.ToUpper(strings.TrimSpace(p.Cmd))
			if strings.HasPrefix(up, "AT+SPENGMD") {
				t.Errorf("厂商 %s 的预设里有 AT+SPENGMD（会把模组 AT 队列堵死）：%q", v, p.Cmd)
			}
			if strings.HasPrefix(up, "AT+SPFREQSCAN=") && !strings.HasSuffix(up, "=?") {
				t.Errorf("厂商 %s 的预设里有带参数的 SPFREQSCAN（会堵死通道）：%q", v, p.Cmd)
			}
		}
	}
}

// 通用那份要**覆盖所有厂商**（可能被厂商同名项换了说明/归类，但不能缺），
// 而且通用那份里一条厂商私有的都不能有。
func TestATPresetsCommonCoverage(t *testing.T) {
	common := atpreset.CommonATPresets()
	if len(common) == 0 {
		t.Fatal("通用清单不该为空")
	}
	for _, p := range common {
		if p.Group != "查询" && p.Group != "设置（谨慎）" {
			t.Errorf("通用清单只该有「查询」和「设置（谨慎）」两组，实际 %q", p.Group)
		}
	}
	for _, v := range []ModemVendor{VendorQuectel, VendorFibocom, VendorSimcom, VendorMeig, VendorTDTech, VendorUnisoc} {
		list := ATPresetsFor(v)
		suppressed := map[string]bool{}
		for _, c := range unisocSuppressedForTest(v) {
			suppressed[strings.ToUpper(c)] = true
		}
		for _, p := range common {
			if suppressed[strings.ToUpper(p.Cmd)] {
				// 厂商明确声明不支持的通用项：必须**摘掉**，而且不能换个写法偷偷又出现
				if containsCmd(list, p.Cmd) {
					t.Errorf("厂商 %s 声明不支持 %q，清单里不该还有它", v, p.Cmd)
				}
				continue
			}
			if !containsCmd(list, p.Cmd) {
				t.Errorf("厂商 %s 的清单里缺了通用指令 %q", v, p.Cmd)
			}
		}
		// 摘掉之后仍然必须比通用清单多（否则等于白挂了个厂商名）
		if len(list) <= len(common)-len(unisocSuppressedForTest(v)) {
			t.Errorf("厂商 %s 应该比通用清单多出一些自己的指令（通用 %d 条，实际 %d 条）",
				v, len(common), len(list))
		}
	}
	// 未识别厂商 = 通用那份（一个字都不多）
	if got := ATPresetsFor(VendorUnknown); len(got) != len(common) {
		t.Errorf("未识别厂商应只给通用清单（%d 条），实际 %d 条", len(common), len(got))
	}
}

// 清单里的每一项都要能被 `/api/at` 真的执行：
// 那个接口只收**单条、≤128 字符、不含换行**的指令（api.go 里写死的限制）。
// 又：**模板（Tpl）与非模板必须分清楚** —— 非模板项点下去就得能跑，
// “点了一下就报错”正是这次要治的毛病。
func TestATPresetsAreAPIRunnable(t *testing.T) {
	for _, v := range []ModemVendor{VendorUnknown, VendorQuectel, VendorFibocom, VendorSimcom, VendorMeig, VendorTDTech, VendorUnisoc} {
		for _, p := range ATPresetsFor(v) {
			if p.Group == "" || p.Cmd == "" || p.Desc == "" {
				t.Errorf("厂商 %s 有残缺项：%+v", v, p)
				continue
			}
			if len(p.Cmd) > 128 {
				t.Errorf("厂商 %s 的 %q 超过 128 字符（/api/at 会拒）", v, p.Cmd)
			}
			if strings.ContainsAny(p.Cmd, "\r\n") {
				t.Errorf("厂商 %s 的 %q 含换行（/api/at 会拒）", v, p.Cmd)
			}
			// 指令原文一律大写起头（和真机输入习惯一致）
			if !strings.HasPrefix(p.Cmd, "AT") {
				t.Errorf("厂商 %s 的 %q 不是 AT 指令", v, p.Cmd)
			}
			hasTpl := strings.ContainsAny(p.Cmd, "<>")
			if p.Tpl != hasTpl {
				t.Errorf("厂商 %s 的 %q：Tpl=%v 与占位符 %v 不一致（标错了界面就会骗人）",
					v, p.Cmd, p.Tpl, hasTpl)
			}
		}
		// 同一份清单里不该有重复指令（同一条挂两个中文名 = 用户会以为有两种写法）
		seen := map[string]string{}
		for _, p := range ATPresetsFor(v) {
			k := strings.ToUpper(p.Cmd)
			if g, dup := seen[k]; dup {
				t.Errorf("厂商 %s 的清单里 %q 重复出现（组 %q 与 %q）", v, p.Cmd, g, p.Group)
			}
			seen[k] = p.Group
		}
	}
}

// unisocSuppressedForTest 测试用：取某厂商「不该显示的通用项」（实现在品牌包）
func unisocSuppressedForTest(v ModemVendor) []string {
	if v == VendorUnisoc {
		return unisoc.SuppressedCommonPresets()
	}
	return nil
}
