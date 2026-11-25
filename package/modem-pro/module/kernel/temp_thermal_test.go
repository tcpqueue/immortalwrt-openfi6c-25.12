package kernel

// 内核温度区（展锐 SIPC / F50 温度源）与模组校时的回归测试。
//
// 这两块都是"真机上取证过"的行为，钉住它们是为了防止以后有人把温度源改回 AT、
// 或者把校时的时间格式改成本地时间（那会让短信时间戳偏移一个时区）。

import (
	"modemd/module/atparse"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeZone 造一个假温度区：sysfs 里**目录**叫 thermal_zoneN，type 文件里才是 zone 类型
// （真机：thermal_zone17/nr0-thmzone）。typeText 空串 = 不写 type 文件。
func writeZone(t *testing.T, root, dir, typeText, temp string) {
	t.Helper()
	d := filepath.Join(root, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if typeText != "" {
		if err := os.WriteFile(filepath.Join(d, "type"), []byte(typeText+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(d, "temp"), []byte(temp+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestThermalZoneTempsF50(t *testing.T) {
	root := t.TempDir()
	// 真机实测的四个基带区 + 两个 AP 区（毫摄氏度）
	writeZone(t, root, "thermal_zone16", "lte-thmzone", "46655")
	writeZone(t, root, "thermal_zone17", "nr0-thmzone", "46898")
	writeZone(t, root, "thermal_zone18", "nr1-thmzone", "46169")
	writeZone(t, root, "thermal_zone15", "mm-thmzone", "46391")
	writeZone(t, root, "thermal_zone6", "apcpu0-thmzone", "47785")
	writeZone(t, root, "thermal_zone11", "gpu-thmzone", "47546")

	temps := thermalZoneTemps(root)
	if len(temps) != 6 {
		t.Fatalf("应读出 6 个传感器，实际 %d：%+v", len(temps), temps)
	}
	byName := map[string]int{}
	for _, s := range temps {
		byName[s.Name] = s.Celsius
	}
	if byName["5G NR0"] != 47 || byName["LTE 基带"] != 47 || byName["AP CPU0"] != 48 {
		t.Errorf("毫摄氏度换算不对：%+v", byName)
	}
	// 排序：从高到低
	for i := 1; i < len(temps); i++ {
		if temps[i].Celsius > temps[i-1].Celsius {
			t.Errorf("温度没有按降序排：%+v", temps)
			break
		}
	}
	// 关键点：5G 基带（含 5g 关键字）与 CPU（含 cpu 关键字）必须被 PickSummary 挑中
	hot, keys := atparse.PickSummary(temps)
	if hot == nil || hot.Celsius != 48 {
		t.Errorf("最高温 = %+v，应为 AP CPU0 48℃", hot)
	}
	names := map[string]bool{}
	for _, k := range keys {
		names[k.Name] = true
	}
	if !names["5G NR0"] || !names["AP CPU0"] {
		t.Errorf("关键点应含 5G NR0 与 AP CPU0，实际 %+v", keys)
	}
}

// 坏数据（非数字、超出合理区间、缺文件）一律丢弃，不能报出 -273℃ 这种鬼值
func TestThermalZoneTempsSkipsGarbage(t *testing.T) {
	root := t.TempDir()
	writeZone(t, root, "thermal_zone17", "nr0-thmzone", "46898")
	writeZone(t, root, "thermal_zone1", "bad-thmzone", "not-a-number")
	writeZone(t, root, "thermal_zone2", "cold-thmzone", "-40000") // -40℃
	writeZone(t, root, "thermal_zone3", "hot-thmzone", "999000")  // 999℃
	writeZone(t, root, "thermal_zone4", "empty-thmzone", "")      // 空
	writeZone(t, root, "thermal_zone99", "", "46898")             // 没有 type 文件
	temps := thermalZoneTemps(root)
	if len(temps) != 1 || temps[0].Name != "5G NR0" {
		t.Fatalf("只应留下 nr0 一条，实际 %+v", temps)
	}
	if got := thermalZoneTemps(filepath.Join(root, "nope")); got != nil {
		t.Errorf("目录不存在应返回 nil，实际 %+v", got)
	}
}

// 只有展锐 SIPC（模组 = SoC）才能拿内核温度区当模组温度；别的通道一个字都不能变
func TestThermalTempsOnlyForUnisocSIPC(t *testing.T) {
	root := t.TempDir()
	writeZone(t, root, "thermal_zone17", "nr0-thmzone", "46898")
	old := thermalClassRoot
	thermalClassRoot = root
	defer func() { thermalClassRoot = old }()

	if got := (&Modem{ATPort: "/dev/stty_nr2"}).thermalTemps(); len(got) != 1 {
		t.Errorf("SIPC 通道应读到温度区，实际 %+v", got)
	}
	for _, port := range []string{"/dev/ttyUSB0", "/dev/ttyACM1", "/dev/mhi_DUN", "COM3", ""} {
		if got := (&Modem{ATPort: port}).thermalTemps(); got != nil {
			t.Errorf("%q 不是展锐 SIPC，不该借用路由器自己的温度区：%+v", port, got)
		}
	}
}

func TestThermalZoneLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"nr0-thmzone", "5G NR0"},
		{"nr1-thmzone", "5G NR1"},
		{"lte-thmzone", "LTE 基带"},
		{"mm-thmzone", "模组多媒体"},
		{"apcpu0-thmzone", "AP CPU0"},
		{"gpu-thmzone", "GPU"},
		{"big7-thmzone", "大核7"},
		{"lit3-thmzone", "小核3"},
		{"mid4-thmzone", "中核4"},
		{"ai0-thmzone", "AI0"},
		{"weird-zone", "weird-zone"}, // 认不出就照实显示
	}
	for _, c := range cases {
		if got := thermalZoneLabel(c.in); got != c.want {
			t.Errorf("thermalZoneLabel(%q) = %q，想要 %q", c.in, got, c.want)
		}
	}
	// 说明文案会被界面原样显示：不能出现 markdown 记号
	if strings.Contains(thermalTempNote, "**") || strings.Contains(thermalTempNote, "`") {
		t.Errorf("温度说明不能带 markdown 记号：%s", thermalTempNote)
	}
}

// ---- 模组校时 ----

func TestFormatCCLK(t *testing.T) {
	utc := time.Date(2026, 10, 5, 17, 30, 5, 0, time.UTC)
	if got := formatCCLK(utc); got != "26/10/05,17:30:05+00" {
		t.Errorf("formatCCLK(UTC) = %q", got)
	}
	if got := ModuleClockCommand(utc); got != `AT+CCLK="26/10/05,17:30:05+00"` {
		t.Errorf("ModuleClockCommand = %q", got)
	}
	// 带时区的输入必须先换成 UTC：否则模组时间会整体偏一个时区（短信时间戳跟着错）
	plus3 := time.Date(2026, 10, 5, 20, 30, 5, 0, time.FixedZone("EAT", 3*3600))
	if got := formatCCLK(plus3); got != "26/10/05,17:30:05+00" {
		t.Errorf("带时区输入应换算成 UTC，得到 %q", got)
	}
	// 时区后缀固定 +00（模组内部按 UTC 存时间，项目里的 ParseCCLK 也是这么读的）
	if got := formatCCLK(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)); !strings.HasSuffix(got, "+00") {
		t.Errorf("时区后缀应为 +00，实际 %q", got)
	}
	if got := formatCCLK(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)); got != "26/01/02,03:04:05+00" {
		t.Errorf("个位数月/日/时应该补零：%q", got)
	}
}
