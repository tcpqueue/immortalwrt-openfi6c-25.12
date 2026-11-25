package kernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestIMEIBackupStore 建一个落到临时目录的备份存储（不碰全局实例）
func newTestIMEIBackupStore(t *testing.T) (*IMEIBackupStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "imei.json")
	s := NewIMEIBackupStore()
	s.Init(path)
	return s, path
}

// TestIMEIBackupRememberedEvenWhenReadbackFails 「下发**之前**先记原始值」这条不能丢。
//
// 2026-10-05 真机事故（展锐 UMS9620 / 中兴 F50）：写完 IMEI 后 `AT+CGSN` 会先回一阵
// `+CME ERROR: 22`，回读必然失败；而旧实现把记录放在"回读成功之后" ⇒ 原始 IMEI
// 永久丢失（真机上 imei.json 根本不存在，界面上连「恢复原始 IMEI」都不显示）。
func TestIMEIBackupRememberedEvenWhenReadbackFails(t *testing.T) {
	s, path := newTestIMEIBackupStore(t)
	mod := &Modem{ID: 0, ATPort: "stty_nr2", IMEI: "867306061176601",
		VendorName: "展锐 Unisoc", ModelName: "QogirN6Pro_PS"}

	// 下发前记一笔；之后**故意不再调 record()** —— 模拟"回读失败"的真实流程
	s.rememberBefore(mod, "867306061176601")

	got, ok := s.get(mod)
	if !ok {
		t.Fatal("即使回读失败，也必须留下原始值备份（否则原 IMEI 永久丢失）")
	}
	if got.Original != "867306061176601" {
		t.Errorf("Original = %q，想要 867306061176601", got.Original)
	}
	if got.Writes != 0 {
		t.Errorf("rememberBefore 不该计 Writes（那是 record 的事），实际 %d", got.Writes)
	}
	if got.SavedAt.IsZero() {
		t.Error("SavedAt 应被记下")
	}

	// "原始值"只有一个：第二次调用不得覆盖
	s.rememberBefore(mod, "999999999999999")
	if g2, _ := s.get(mod); g2.Original != "867306061176601" {
		t.Errorf("第二次 rememberBefore 覆盖了原始值：%q", g2.Original)
	}

	// 读不到合法 IMEI 时不该留下记录（否则会把错误串当"原始值"）
	s2, _ := newTestIMEIBackupStore(t)
	mod2 := &Modem{ID: 1, ATPort: "stty_nr2"}
	s2.rememberBefore(mod2, "")
	s2.rememberBefore(mod2, "+CME ERROR: 22")
	if _, ok := s2.get(mod2); ok {
		t.Error("读不到合法 IMEI 时不该留下备份记录")
	}

	// 必须落盘（重启后还在，否则"能恢复"只是幻觉）
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "867306061176601") {
		t.Errorf("备份没落盘：err=%v content=%s", err, string(b))
	}
}

// TestIMEIBackupSavesOriginalOnce 验证「改写前保存原始 IMEI」：
// 第一次改写时的**当时值**是原始值，之后无论再改多少次都不能被覆盖 ——
// 这是"能恢复回去"的唯一依据（改 IMEI 是写 NV，改写后模组不会告诉你原值）。
func TestIMEIBackupSavesOriginalOnce(t *testing.T) {
	s, _ := newTestIMEIBackupStore(t)

	mod := &Modem{ID: 0, ATPort: "COM3", IMEI: "111111111111111", VendorName: "移远 Quectel", ModelName: "RM502Q-GL"}

	// 第一次改写：111… → 222…
	s.record(mod, "111111111111111", "222222222222222")
	mod.IMEI = "222222222222222" // 真实流程：写完接着回读，缓存就是这个新值
	got, ok := s.get(mod)
	if !ok {
		t.Fatalf("第一次改写后应该有备份记录")
	}
	if got.Original != "111111111111111" {
		t.Errorf("Original = %q，想要 %q", got.Original, "111111111111111")
	}
	if got.Current != "222222222222222" {
		t.Errorf("Current = %q，想要 %q", got.Current, "222222222222222")
	}
	if got.Writes != 1 {
		t.Errorf("Writes = %d，想要 1", got.Writes)
	}
	if got.Vendor != "移远 Quectel" || got.Model != "RM502Q-GL" {
		t.Errorf("机型信息没记上：%q / %q", got.Vendor, got.Model)
	}
	if !got.Modified() {
		t.Errorf("当前值 ≠ 原始值时应报 Modified")
	}

	// 第二次改写：222… → 333…，Original 必须还是 111…
	mod.IMEI = "222222222222222"
	s.record(mod, "222222222222222", "333333333333333")
	mod.IMEI = "333333333333333"
	got2, _ := s.get(mod)
	if got2.Original != "111111111111111" {
		t.Errorf("第二次改写把原始值覆盖成了 %q —— 原始值只能记一次", got2.Original)
	}
	if got2.Current != "333333333333333" || got2.Writes != 2 {
		t.Errorf("Current/Writes = %q / %d，想要 333… / 2", got2.Current, got2.Writes)
	}

	// 写回原始值后再看：不再是 Modified
	s.record(mod, "333333333333333", "111111111111111")
	s.markRestored(mod, "111111111111111")
	mod.IMEI = "111111111111111"
	got3, _ := s.get(mod)
	if got3.Modified() {
		t.Errorf("已恢复原始值，不该再报 Modified")
	}
	if got3.RestoredAt.IsZero() {
		t.Errorf("恢复时间没记上")
	}
}

// TestIMEIBackupPerModem 验证多模块：两台模组各有各的原始值，互不干扰
func TestIMEIBackupPerModem(t *testing.T) {
	s, _ := newTestIMEIBackupStore(t)

	a := &Modem{ID: 0, ATPort: "COM3", IMEI: "111111111111111"}
	b := &Modem{ID: 1, ATPort: "COM7", IMEI: "444444444444444"}

	s.record(a, "111111111111111", "222222222222222")
	s.record(b, "444444444444444", "555555555555555")
	a.IMEI = "222222222222222"
	b.IMEI = "555555555555555"

	ga, okA := s.get(a)
	gb, okB := s.get(b)
	if !okA || !okB {
		t.Fatalf("两台模组都该有各自的记录（okA=%v okB=%v）", okA, okB)
	}
	if ga.Original != "111111111111111" {
		t.Errorf("模组 A 的原始值 = %q，想要 111…（不能拿到 B 的）", ga.Original)
	}
	if gb.Original != "444444444444444" {
		t.Errorf("模组 B 的原始值 = %q，想要 444…（不能拿到 A 的）", gb.Original)
	}
}

// TestIMEIBackupSameIMEITwoModems 两台模组 IMEI 相同（克隆机 / 都还没改过）时，
// 各自的记录必须完全独立 —— 绝不能靠 IMEI 去匹配，否则点“恢复”会把别人的 IMEI 写上去。
func TestIMEIBackupSameIMEITwoModems(t *testing.T) {
	s, _ := newTestIMEIBackupStore(t)

	a := &Modem{ID: 0, ATPort: "COM3", IMEI: "111111111111111"}
	b := &Modem{ID: 1, ATPort: "COM7", IMEI: "111111111111111"} // 同 IMEI
	s.record(a, "111111111111111", "222222222222222")
	s.record(b, "111111111111111", "333333333333333")

	a.IMEI, b.IMEI = "222222222222222", "333333333333333"

	ga, okA := s.get(a)
	gb, okB := s.get(b)
	if !okA || !okB {
		t.Fatalf("两台都该有各自的记录（okA=%v okB=%v）", okA, okB)
	}
	// 两台都在未改写时读到 111…，所以 Original 相同是正常的；关键是 Current 不能串
	if ga.Current != "222222222222222" {
		t.Errorf("A 的 Current = %q，想要 222…", ga.Current)
	}
	if gb.Current != "333333333333333" {
		t.Errorf("B 的 Current = %q，想要 333…（被 A 串了？）", gb.Current)
	}

	// 新口上来的第三台（同 IMEI）：不该拿到任何一台的备份
	fresh := &Modem{ID: 2, ATPort: "COM9", IMEI: "222222222222222"}
	if _, ok := s.get(fresh); ok {
		t.Fatalf("没记录过的口不该凭空得到备份（靠 IMEI 匹配会串台）")
	}
}

// TestIMEIBackupNoMigrateOnPortChange 换 USB 口（AT 口名变了）时**不做**自动迁移：
// 新口看不到旧记录（重新记一份即可），也绝不会把别的模组的原始值拿过来。
func TestIMEIBackupNoMigrateOnPortChange(t *testing.T) {
	s, _ := newTestIMEIBackupStore(t)

	old := &Modem{ID: 0, ATPort: "COM3", IMEI: "111111111111111"}
	s.record(old, "111111111111111", "222222222222222")

	moved := &Modem{ID: 0, ATPort: "COM7", IMEI: "222222222222222"}
	if _, ok := s.get(moved); ok {
		t.Fatalf("换口后是按口分别记录的（不迁移），不该拿到旧口的备份")
	}

	s.mu.Lock()
	_, stillOld := s.data["port:COM3"]
	_, createdNew := s.data["port:COM7"]
	s.mu.Unlock()
	if !stillOld {
		t.Errorf("旧口 port:COM3 的记录应该原样留着")
	}
	if createdNew {
		t.Errorf("只读应该不建占位记录")
	}
}

// TestIMEIBackupPersistsAndSkipsEmpty 落盘与"不写空记录"：
// 记录能跨进程读回；只查过、还从来没保存过原始值的模组不该出现在文件里。
func TestIMEIBackupPersistsAndSkipsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "imei.json")
	s := NewIMEIBackupStore()
	s.Init(path)

	mod := &Modem{ID: 0, ATPort: "COM3", IMEI: "111111111111111"}
	s.record(mod, "111111111111111", "222222222222222")

	// 只 get 一台没记录过的模组（不该建占位记录）
	empty := &Modem{ID: 1, ATPort: "COM7", IMEI: "999999999999999"}
	if _, ok := s.get(empty); ok {
		t.Fatalf("没记录过的模组不该有备份")
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 imei.json 失败：%v", err)
	}
	text := string(b)
	if !strings.Contains(text, "111111111111111") {
		t.Errorf("原始值没落盘：%s", text)
	}
	if strings.Contains(text, "999999999999999") {
		t.Errorf("没记录过的模组不该被写进文件：%s", text)
	}

	// 新实例读回（模拟重启）
	s2 := NewIMEIBackupStore()
	s2.Init(path)
	mod.IMEI = "222222222222222"
	got, ok := s2.get(mod)
	if !ok || got.Original != "111111111111111" || got.Current != "222222222222222" {
		t.Fatalf("重启后没读回记录：ok=%v original=%q current=%q", ok, got.Original, got.Current)
	}
}

// TestRestoreIMEIWithoutBackup 没有备份时必须明确报错（不能猜一个值写下去）
func TestRestoreIMEIWithoutBackup(t *testing.T) {
	old := imeiBackupInst
	imeiBackupInst = NewIMEIBackupStore()
	defer func() { imeiBackupInst = old }()

	mod := &Modem{ID: 42, ATPort: "COM_NOPE", IMEI: "111111111111111"}
	if _, err := mod.RestoreIMEI(); err == nil {
		t.Fatalf("没有备份时 RestoreIMEI 应该报错")
	} else if !strings.Contains(err.Error(), "还没有保存过原始 IMEI") {
		t.Errorf("错误信息应说清原因，实际：%v", err)
	}
}

// TestIMEIBackupPathWithConfig 备份文件与配置文件同目录（与 quota.json 同一套规则）
func TestIMEIBackupPathWithConfig(t *testing.T) {
	if got := IMEIBackupPath("/etc/modemd/config.toml"); got != filepath.Join("/etc/modemd", "imei.json") {
		t.Errorf("IMEIBackupPath = %q", got)
	}
	if got := IMEIBackupPath("  "); got != "imei.json" {
		t.Errorf("空路径应退回当前目录，实际 %q", got)
	}
}
