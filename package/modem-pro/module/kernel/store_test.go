package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"io"
	"modemd/module/cell"
	"modemd/module/common"
	"modemd/module/config"
	"modemd/module/sms"
	"modemd/module/tdtech"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// ---- from: history/signalhistory_test.go ----

func intPtr(v int) *int { return &v }

func TestSignalHistoryAddAndExtend(t *testing.T) {
	h := NewHistoryStore()
	t0 := time.Now().Add(-10 * time.Second)
	// 第一个点
	h.Add(0, Sample{At: t0, RSRP: intPtr(-100), SINR: intPtr(5)})
	if got := h.CountFor(0); got != 1 {
		t.Fatalf("段数 = %d, want 1", got)
	}
	// 值相同 → 不新增点，只把时间往后推（曲线要一直画到"现在"）
	h.Add(0, Sample{At: t0.Add(3 * time.Second), RSRP: intPtr(-100), SINR: intPtr(5)})
	if got := h.CountFor(0); got != 1 {
		t.Fatalf("相同值不应新增点，段数 = %d", got)
	}
	list := h.Snapshot(0, time.Time{}, 0)
	if !list[0].At.Equal(t0.Add(3 * time.Second)) {
		t.Errorf("末点时间应被推进到最新采样时刻，实际 %v", list[0].At)
	}
	// 值变了 → 新增一个点
	h.Add(0, Sample{At: t0.Add(4 * time.Second), RSRP: intPtr(-104), SINR: intPtr(5)})
	if got := h.CountFor(0); got != 2 {
		t.Fatalf("值变化应新增点，段数 = %d", got)
	}
	// 时间不能倒流（乱序采样不应该把曲线画回去）
	h.Add(0, Sample{At: t0, RSRP: intPtr(-104), SINR: intPtr(5)})
	list = h.Snapshot(0, time.Time{}, 0)
	if !list[1].At.Equal(t0.Add(4 * time.Second)) {
		t.Errorf("旧时间戳不应覆盖末点时间，实际 %v", list[1].At)
	}
	// 另一个模组互不影响
	h.Add(1, Sample{At: t0.Add(5 * time.Second), RSRP: intPtr(-90)})
	if h.CountFor(0) != 2 || h.CountFor(1) != 1 || h.CountFor(-1) != 3 {
		t.Errorf("分段计数不对: 0=%d 1=%d all=%d", h.CountFor(0), h.CountFor(1), h.CountFor(-1))
	}
}

func TestSignalHistoryRingAndClear(t *testing.T) {
	h := NewHistoryStore()
	// 让最后一个采样正好落在"现在"，便于后面按时间窗过滤
	base := time.Now().Add(-time.Duration(SampleMax+50) * time.Second)
	for i := 0; i < SampleMax+50; i++ {
		h.Add(0, Sample{At: base.Add(time.Duration(i) * time.Second), RSRP: intPtr(-100 - i%7)})
	}
	if got := h.CountFor(0); got != SampleMax {
		t.Fatalf("环形缓冲段数 = %d, want %d", got, SampleMax)
	}
	// 过滤时间窗：只要最近 60 秒
	list := h.Snapshot(0, time.Now().Add(-60*time.Second), 0)
	if len(list) == 0 || len(list) > 60 {
		t.Fatalf("时间窗过滤结果 = %d 条", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i].At.Before(list[i-1].At) {
			t.Fatal("采样点必须按时间升序")
		}
	}
	// limit 取最新的若干条
	if got := len(h.Snapshot(0, time.Time{}, 5)); got != 5 {
		t.Errorf("limit=5 得到 %d 条", got)
	}
	// 全模组查询按时间归并
	h.Add(1, Sample{At: time.Now(), RSRP: intPtr(-88)})
	all := h.Snapshot(-1, time.Time{}, 0)
	for i := 1; i < len(all); i++ {
		if all[i].At.Before(all[i-1].At) {
			t.Fatal("多模组归并后也必须有序")
		}
	}
	// 清空
	if removed := h.Clear(0); removed != SampleMax {
		t.Errorf("Clear(0) = %d, want %d", removed, SampleMax)
	}
	if h.CountFor(0) != 0 || h.CountFor(1) != 1 {
		t.Errorf("清空模组 0 后不应影响模组 1: 0=%d 1=%d", h.CountFor(0), h.CountFor(1))
	}
	if removed := h.Clear(-1); removed != 1 {
		t.Errorf("Clear(-1) = %d, want 1", removed)
	}
	if h.CountFor(-1) != 0 {
		t.Error("全部清空后应为 0")
	}
}

// ---- from: logfile/logfile_test.go ----

// writeAppend 往文件尾部追加内容（模拟 daemon 一直在写日志）

func writeAppend(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestLogPusherReadNew(t *testing.T) {
	path := filepath.Join(t.TempDir(), "modemd.log")
	writeAppend(t, path, "old-1\nold-2\n")

	p := &config.Pusher{Path: path}
	// 首次只建基线：历史行不补推（页面进来自会拉尾部）
	if got := p.ReadNew(); got != nil {
		t.Fatalf("首次应只建基线，却返回 %q", got)
	}
	// 没有新增
	if got := p.ReadNew(); got != nil {
		t.Fatalf("没有新增时不该返回内容，got %q", got)
	}
	// 新增两行（CRLF 也要能吃，日志在 Windows 上可能是 CRLF）
	writeAppend(t, path, "new-1\r\nnew-2\r\n")
	if got := p.ReadNew(); !reflect.DeepEqual(got, []string{"new-1", "new-2"}) {
		t.Fatalf("新增行解析错误：%q", got)
	}
	// 半行：只有完整行之前的能推，尾部片段留到下一轮
	writeAppend(t, path, "half")
	if got := p.ReadNew(); got != nil {
		t.Fatalf("尾部半行不该推出去，got %q", got)
	}
	// 补上换行后，那一行应该能拿到
	writeAppend(t, path, "-done\n")
	if got := p.ReadNew(); !reflect.DeepEqual(got, []string{"half-done"}) {
		t.Fatalf("半行补齐后应能推出完整行，got %q", got)
	}
	// 空行不推（避免页面出现空 span）
	writeAppend(t, path, "\n")
	if got := p.ReadNew(); got != nil {
		t.Fatalf("纯空行不该推，got %q", got)
	}
}

func TestLogPusherRotateAndMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "modemd.log")

	// 文件不存在：不 panic、不返回内容
	p := &config.Pusher{Path: path}
	if got := p.ReadNew(); got != nil {
		t.Fatalf("文件不存在应返回空，got %q", got)
	}

	writeAppend(t, path, "a\n")
	if got := p.ReadNew(); got != nil { // 首次还是建基线
		t.Fatalf("首次建基线失败：%q", got)
	}
	writeAppend(t, path, "b\n")
	if got := p.ReadNew(); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("正常追加解析错误：%q", got)
	}

	// 日志被轮转：文件被截断后重写，长度**可能比旧 offset 还长** ——
	// 只比长度是不够的，要靠 offset 之前那段字节的变化识别。
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	writeAppend(t, path, "after-rotate\n")
	if got := p.ReadNew(); !reflect.DeepEqual(got, []string{"after-rotate"}) {
		t.Fatalf("清空后应从 0 重新读（不能从旧 offset 接着读），got %q", got)
	}

	// 轮转后比旧 offset 更长的情形：换一个更长的假日志重写
	writeAppend(t, path, "tail\n")
	if got := p.ReadNew(); !reflect.DeepEqual(got, []string{"tail"}) {
		t.Fatalf("轮转后新增解析错误：%q", got)
	}
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	writeAppend(t, path, strings.Repeat("long-after-rotate\n", 8))
	got := p.ReadNew()
	if len(got) != 8 || got[0] != "long-after-rotate" {
		t.Fatalf("轮转后写得更长时应整份重读，got %d 行（首行 %q）", len(got), firstOf(got))
	}
}

func firstOf(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func TestLogPusherLongLine(t *testing.T) {
	// 比推送窗口（64KB）还长的单行：**经不允许推出去半行**，
	// 宁可跳过（页面自己的尾部拉取能显示完整行，tailLogFile 的扫描缓冲有 1MB）。
	path := filepath.Join(t.TempDir(), "modemd.log")
	long := strings.Repeat("x", 300*1024)
	p := &config.Pusher{Path: path}
	writeAppend(t, path, "first\n")
	_ = p.ReadNew() // 建基线

	writeAppend(t, path, long+"\n")
	for _, l := range p.ReadNew() {
		if strings.Contains(l, "xxx") && len(l) != len(long) {
			t.Fatalf("推了半行（len=%d）", len(l))
		}
	}
	// 后面紧跟的正常行必须先能推到完整行
	writeAppend(t, path, "after-long\n")
	got := p.ReadNew()
	if len(got) != 1 || got[0] != "after-long" {
		t.Fatalf("超长行之后的行应能推出，got %q", got)
	}
}

// ---- from: quota/quota_test.go ----

func TestStateUsedAndOver(t *testing.T) {
	st := State{
		QuotaConfig: QuotaConfig{LimitBytes: 1000},
		UsedUp:      300,
		UsedDown:    400,
	}
	// 默认两个方向都算
	st.CountUp, st.CountDown = true, true
	if got := st.Used(); got != 700 {
		t.Errorf("Used() = %d, want 700", got)
	}
	if st.Over() {
		t.Error("700 < 1000 不该算超限")
	}
	// 只算下行
	st.CountUp, st.CountDown = false, true
	if got := st.Used(); got != 400 {
		t.Errorf("只算下行 Used() = %d, want 400", got)
	}
	// 到上限即算超限（>=）
	st.UsedDown = 1000
	if !st.Over() {
		t.Error("正好等于上限就该算超限（否则永远断不了网）")
	}
	// 不限量（0）永远不超限
	st.LimitBytes = 0
	if st.Over() {
		t.Error("未设上限时不该算超限")
	}
}

func TestBlockedAction(t *testing.T) {
	// 有记录就用当时的动作（用户中途换动作时，恢复得按旧动作走）
	st := State{QuotaConfig: QuotaConfig{Action: ActionBlock}, BlockedBy: ActionBlockRF}
	if got := st.BlockedAction(); got != ActionBlockRF {
		t.Errorf("BlockedAction() = %q, want %q", got, ActionBlockRF)
	}
	// 没记录（旧数据）时用当前配置
	st = State{QuotaConfig: QuotaConfig{Action: ActionWarn}}
	if got := st.BlockedAction(); got != ActionWarn {
		t.Errorf("BlockedAction() = %q, want %q", got, ActionWarn)
	}
}

// ====================== 周期（账单月） ======================

// 重置日 15：9/20 属于 "2026-09-15"，9/10 属于 "2026-08-15"
func TestPeriod(t *testing.T) {
	loc := time.Local
	cases := []struct {
		now  time.Time
		day  int
		want string
	}{
		{time.Date(2026, 9, 20, 12, 0, 0, 0, loc), 15, "2026-09-15"},
		{time.Date(2026, 9, 15, 0, 0, 0, 0, loc), 15, "2026-09-15"},
		{time.Date(2026, 9, 10, 23, 59, 0, 0, loc), 15, "2026-08-15"},
		{time.Date(2026, 1, 3, 0, 0, 0, 0, loc), 15, "2025-12-15"},
		{time.Date(2026, 9, 20, 12, 0, 0, 0, loc), 0, "manual"},
		{time.Date(2026, 9, 20, 12, 0, 0, 0, loc), 29, "manual"},
		{time.Date(2026, 9, 20, 12, 0, 0, 0, loc), 1, "2026-09-01"},
	}
	for _, c := range cases {
		if got := Period(c.now, c.day); got != c.want {
			t.Errorf("Period(%s, %d) = %q, want %q", c.now.Format("2006-01-02"), c.day, got, c.want)
		}
	}
}

func TestPeriodRange(t *testing.T) {
	start, end := PeriodRange(time.Date(2026, 9, 20, 12, 0, 0, 0, time.Local), 15)
	if start.Format("2006-01-02") != "2026-09-15" || end.Format("2006-01-02") != "2026-10-15" {
		t.Errorf("周期区间 = %s ~ %s, want 2026-09-15 ~ 2026-10-15",
			start.Format("2006-01-02"), end.Format("2006-01-02"))
	}
	// 不自动重置时没有区间
	if s, e := PeriodRange(time.Now(), 0); !s.IsZero() || !e.IsZero() {
		t.Error("ResetDay=0 时不该有周期区间")
	}
}

// ====================== 视图 ======================

func TestBuildView(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.Local)
	st := State{
		QuotaConfig: QuotaConfig{Enabled: true, LimitBytes: 1000, CountUp: true, CountDown: true, ResetDay: 15},
		UsedUp:      300,
		UsedDown:    200,
	}
	v := BuildView(st, 7, true, now)
	if v.ModemID != 7 || !v.Supported || !v.Enabled {
		t.Errorf("基本字段错误: %+v", v)
	}
	if v.UsedBytes != 500 || v.UsedText != "500 B" {
		t.Errorf("用量 = %d (%q), want 500 (500 B)", v.UsedBytes, v.UsedText)
	}
	if v.RemainBytes != 500 || v.RemainText != "500 B" {
		t.Errorf("剩余 = %d (%q), want 500", v.RemainBytes, v.RemainText)
	}
	if v.Percent != 50 {
		t.Errorf("百分比 = %v, want 50", v.Percent)
	}
	if v.Period != "2026-09-15" {
		t.Errorf("周期 = %q, want 2026-09-15", v.Period)
	}
	if v.OverLimit || v.Blocked {
		t.Error("没超限不该标成超限/已断网")
	}

	// 未设上限：RemainText = "不限"，百分比 0
	v = BuildView(State{QuotaConfig: QuotaConfig{CountUp: true, CountDown: true}}, 0, false, now)
	if v.RemainText != "不限" || v.Percent != 0 {
		t.Errorf("不限量视图错误: %+v", v)
	}
	if v.Action != ActionBlock {
		t.Errorf("默认动作 = %q, want %q", v.Action, ActionBlock)
	}
	if !v.PeriodStart.IsZero() {
		t.Error("ResetDay=0 时不该有周期起止")
	}

	// 超限：百分比封顶 100、剩余归零
	st.UsedUp, st.UsedDown = 2000, 0
	v = BuildView(st, 0, true, now)
	if !v.OverLimit || v.Percent != 100 || v.RemainBytes != 0 {
		t.Errorf("超限视图错误: %+v", v)
	}
	if v.PeriodEnd.Format("2006-01-02") != "2026-10-15" {
		t.Errorf("PeriodEnd = %s, want 2026-10-15", v.PeriodEnd.Format("2006-01-02"))
	}
}

func TestFormatBytes(t *testing.T) {
	cases := map[int64]string{
		0:                      "0 B",
		-1:                     "0 B",
		512:                    "512 B",
		1024:                   "1.0 KB",
		1024 * 1024:            "1.0 MB",
		1024 * 1024 * 1024:     "1.00 GB",
		5 * 1024 * 1024 * 1024: "5.00 GB",
	}
	for in, want := range cases {
		if got := FormatBytes(in); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

// ====================== 键 / 存储 ======================

// 键以端口为主：实测两块模组 IMEI 相同，按 IMEI 存会串台
func TestKey(t *testing.T) {
	if got := Key("COM11", 0); got != "port:COM11" {
		t.Errorf("Key(COM11,0) = %q, want port:COM11", got)
	}
	if got := Key("  ", 3); got != "id:3" {
		t.Errorf("无端口时应退回 id: %q", got)
	}
}

func TestStoreAddAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quota.json")

	q := NewQuotaStore()
	q.Init(path)

	// 新记录默认值：两个方向都算、动作为断数据连接
	st := q.Get("port:COM1", "111111111111111")
	if !st.CountUp || !st.CountDown || st.Action != ActionBlock {
		t.Fatalf("新记录默认值不对: %+v", st)
	}
	if st.IMEI != "111111111111111" {
		t.Errorf("IMEI 应记下: %q", st.IMEI)
	}

	q.AddBytes("port:COM1", "111111111111111", 100, 200)
	q.AddBytes("port:COM1", "111111111111111", 0, 300)
	st = q.Update("port:COM1", "111111111111111", func(s *State) {
		s.Enabled = true
		s.LimitBytes = 1024
		s.Period = "2026-09-15"
	})
	if st.UsedUp != 100 || st.UsedDown != 500 || !st.Enabled {
		t.Fatalf("累计/更新错误: %+v", st)
	}
	q.Flush()

	// 重新载入：数据要还在
	q2 := NewQuotaStore()
	q2.Init(path)
	got := q2.Get("port:COM1", "111111111111111")
	if got.UsedUp != 100 || got.UsedDown != 500 || got.LimitBytes != 1024 || !got.Enabled {
		t.Fatalf("落盘/载入丢数据: %+v", got)
	}
	if got.Period != "2026-09-15" {
		t.Errorf("周期没存下: %q", got.Period)
	}
}

func TestStoreKeepsQuotaSeparateForSameIMEI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.json")
	q := NewQuotaStore()
	q.Init(path)
	const imei = "SAME"
	q.Update("port:COM1", imei, func(s *State) {
		s.Enabled, s.Blocked = true, true
		s.UsedDown = 4096
	})
	q.AddBytes("port:COM2", imei, 10, 20)
	q.Flush()

	reloaded := NewQuotaStore()
	reloaded.Init(path)
	for _, store := range []*QuotaStore{q, reloaded} {
		other := store.Get("port:COM2", imei)
		if other.Enabled || other.Blocked || other.UsedUp != 10 || other.UsedDown != 20 {
			t.Fatalf("第二端口继承了第一端口的配额状态：%+v", other)
		}
		original := store.Get("port:COM1", imei)
		if !original.Enabled || !original.Blocked || original.UsedDown != 4096 {
			t.Fatalf("第一端口的原记录发生变化：%+v", original)
		}
	}
}

// 已有的端口记录按原键载入，新端口单独计量。
func TestStoreRetainsLegacyPortRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quota.json")
	seed := `{"version":1,"modems":{
		"port:COM1":{"imei":"SAME","used_up":10,"count_up":true},
		"port:COM2":{"imei":"SAME","used_up":20,"count_up":true}}}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	q := NewQuotaStore()
	q.Init(path)
	got := q.Get("port:COM9", "SAME")
	if got.UsedUp != 0 {
		t.Fatalf("IMEI 不唯一时不该迁移（会串台）: %+v", got)
	}
	if a := q.Get("port:COM1", "SAME"); a.UsedUp != 10 {
		t.Errorf("原记录被动了: %+v", a)
	}
	if b := q.Get("port:COM2", "SAME"); b.UsedUp != 20 {
		t.Errorf("原记录被动了: %+v", b)
	}
}

func TestStoreBrokenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quota.json")
	if err := os.WriteFile(path, []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	q := NewQuotaStore()
	q.Init(path) // 不应 panic，按空库处理
	if st := q.Get("port:COM1", ""); st.UsedUp != 0 {
		t.Errorf("坏文件应退回空库: %+v", st)
	}
}

// 推送 / 断网尝试 / 复查的节流：同一模组在窗口内只放行一次
func TestStoreThrottles(t *testing.T) {
	q := NewQuotaStore()
	if !q.PushAllowed(1) {
		t.Error("首次推送应放行")
	}
	if q.PushAllowed(1) {
		t.Error("窗口内第二次推送应被节流")
	}
	if !q.PushAllowed(2) {
		t.Error("节流按模组分开")
	}
	if !q.TryAllowed(1) {
		t.Error("首次尝试应放行")
	}
	if q.TryAllowed(1) {
		t.Error("窗口内第二次尝试应被节流")
	}
	if !q.CheckAllowed(1) {
		t.Error("首次复查应放行")
	}
	if q.CheckAllowed(1) {
		t.Error("窗口内第二次复查应被节流")
	}
}

func TestQuotaPath(t *testing.T) {
	if got := QuotaPath("/etc/modemd/config.toml"); got != filepath.Join("/etc/modemd", "quota.json") {
		t.Errorf("QuotaPath = %q", got)
	}
	if got := QuotaPath("  "); got != "quota.json" {
		t.Errorf("无配置路径时应退回当前目录: %q", got)
	}
}

// ---- from: schedule/schedule_test.go ----

func TestSchedule_ParseHHMM(t *testing.T) {
	good := map[string][2]int{"08:00": {8, 0}, "8:5": {8, 5}, " 23:59 ": {23, 59}, "00:00": {0, 0}}
	for in, want := range good {
		h, m, err := ParseHHMM(in)
		if err != nil || h != want[0] || m != want[1] {
			t.Errorf("ParseHHMM(%q) = %d,%d,%v，期望 %d,%d", in, h, m, err, want[0], want[1])
		}
	}
	for _, bad := range []string{"", "8", "24:00", "08:60", "-1:00", "08:00:00", "ab:cd"} {
		if _, _, err := ParseHHMM(bad); err == nil {
			t.Errorf("ParseHHMM(%q) 应当报错", bad)
		}
	}
}

func TestValidateTaskOK(t *testing.T) {
	task := &Task{
		ID: "t1", ModemKey: "port:COM32", Enabled: true,
		StartTime: "8:0",
		StartActions: []Action{
			{Kind: KindBandLock, Tech: "nr", Bands: "78:1:78"},
		},
		EndTime:    "20:00",
		EndActions: []Action{{Kind: KindBandUnlock, Tech: "NR"}},
		Days:       []int{1, 1, 5, 0},
	}
	if err := ValidateTask(task); err != nil {
		t.Fatalf("合法任务不该报错：%v", err)
	}
	// 时间补成规范写法；动作参数归一（制式大写、频段排序去重）
	if task.StartTime != "08:00" || task.EndTime != "20:00" {
		t.Errorf("时间未归一：%q / %q", task.StartTime, task.EndTime)
	}
	if task.StartActions[0].Tech != "NR" || task.StartActions[0].Bands != "1:78" {
		t.Errorf("动作参数未归一：%+v", task.StartActions[0])
	}
	if len(task.Days) != 3 || task.Days[0] != 0 {
		t.Errorf("星期应去重并排序：%v", task.Days)
	}
	if task.Name == "" {
		t.Error("没给名字时应自动生成")
	}
}

func TestValidateTaskMultiActions(t *testing.T) {
	task := &Task{
		ID: "t1", ModemKey: "port:COM1", StartTime: "08:00",
		StartActions: []Action{
			{Kind: KindBandLock, Tech: "NR", Bands: "78"},
			{Kind: KindModePref, Mode: "LTE"},
		},
	}
	if err := ValidateTask(task); err != nil {
		t.Fatalf("两个动作应当合法：%v", err)
	}
	if got := ActionsLabel(task.StartActions); got != "锁频段 NR 78 + 组网模式 → 仅 4G（LTE）" {
		t.Errorf("动作文案 = %q", got)
	}
	// 至少要有一个动作
	if err := ValidateTask(&Task{ID: "t2", ModemKey: "port:COM1", StartTime: "08:00"}); err == nil ||
		!strings.Contains(err.Error(), "至少") {
		t.Errorf("空动作列表应当报错，实际 %v", err)
	}
	// 超过上限
	many := make([]Action, 0, MaxActions+1)
	for i := 0; i <= MaxActions; i++ {
		many = append(many, Action{Kind: KindUnlockAll})
	}
	if err := ValidateTask(&Task{ID: "t3", ModemKey: "port:COM1", StartTime: "08:00", StartActions: many}); err == nil ||
		!strings.Contains(err.Error(), "最多") {
		t.Errorf("超过动作上限应当报错，实际 %v", err)
	}
}

func TestValidateTaskRejects(t *testing.T) {
	cases := []struct {
		name string
		task *Task
		want string
	}{
		{"缺 id", &Task{ModemKey: "port:COM1", StartTime: "08:00", StartActions: []Action{{Kind: KindUnlockAll}}}, "id"},
		{"缺目标", &Task{ID: "t", StartTime: "08:00", StartActions: []Action{{Kind: KindUnlockAll}}}, "目标模组"},
		{"时间非法", &Task{ID: "t", ModemKey: "port:COM1", StartTime: "25:00", StartActions: []Action{{Kind: KindUnlockAll}}}, "开始时间"},
		{"锁频段缺制式", &Task{ID: "t", ModemKey: "port:COM1", StartTime: "08:00",
			StartActions: []Action{{Kind: KindBandLock, Bands: "78"}}}, "制式"},
		{"锁频段缺频段", &Task{ID: "t", ModemKey: "port:COM1", StartTime: "08:00",
			StartActions: []Action{{Kind: KindBandLock, Tech: "NR"}}}, "频段号"},
		{"锁基站缺方式", &Task{ID: "t", ModemKey: "port:COM1", StartTime: "08:00",
			StartActions: []Action{{Kind: KindCellLock, Tech: "NR", ARFCN: 627264}}}, "锁定方式"},
		{"频点越界", &Task{ID: "t", ModemKey: "port:COM1", StartTime: "08:00",
			StartActions: []Action{{Kind: KindCellLock, Tech: "LTE", LockType: CellLockARFCN, ARFCN: 99999999}}}, "频点号"},
		{"PCI 越界", &Task{ID: "t", ModemKey: "port:COM1", StartTime: "08:00",
			StartActions: []Action{{Kind: KindCellLock, Tech: "NR", LockType: CellLockARFCNPCI, ARFCN: 627264, PCID: 2000}}}, "PCI"},
		{"模式非法", &Task{ID: "t", ModemKey: "port:COM1", StartTime: "08:00",
			StartActions: []Action{{Kind: KindModePref, Mode: "5G"}}}, "组网模式"},
		{"未知动作", &Task{ID: "t", ModemKey: "port:COM1", StartTime: "08:00",
			StartActions: []Action{{Kind: "nope"}}}, "未知动作"},
		{"结束时间与开始相同", &Task{ID: "t", ModemKey: "port:COM1", StartTime: "08:00",
			StartActions: []Action{{Kind: KindUnlockAll}}, EndTime: "8:00", EndActions: []Action{{Kind: KindUnlockAll}}}, "不能与开始时间相同"},
		{"有结束时间却没动作", &Task{ID: "t", ModemKey: "port:COM1", StartTime: "08:00",
			StartActions: []Action{{Kind: KindUnlockAll}}, EndTime: "20:00"}, "结束动作"},
		{"星期越界", &Task{ID: "t", ModemKey: "port:COM1", StartTime: "08:00",
			StartActions: []Action{{Kind: KindUnlockAll}}, Days: []int{7}}, "星期"},
	}
	for _, c := range cases {
		err := ValidateTask(c.task)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：期望错误包含 %q，实际 %v", c.name, c.want, err)
		}
	}
}

// 填了结束时间就必须有结束动作；反过来没有结束时间时结束动作要被丢掉，
// 避免"看起来会执行其实不会"
func TestValidateTaskDropsOrphanEndAction(t *testing.T) {
	task := &Task{
		ID: "t", ModemKey: "port:COM1", StartTime: "08:00",
		StartActions: []Action{{Kind: KindUnlockAll}},
		EndActions:   []Action{{Kind: KindUnlockAll}}, // 没有 EndTime
	}
	if err := ValidateTask(task); err != nil {
		t.Fatal(err)
	}
	if len(task.EndActions) != 0 {
		t.Errorf("没有结束时间时结束动作应被清掉：%+v", task.EndActions)
	}
	// 也没有 end 槽位
	if len(task.Phases()) != 1 {
		t.Errorf("应只有 start 槽位：%+v", task.Phases())
	}
}

// ====================== 时间判定 ======================

func at(hh, mm int) time.Time {
	return time.Date(2026, 9, 30, hh, mm, 0, 0, time.Local)
}

func testTask(start, end string) *Task {
	return &Task{
		ID: "t", ModemKey: "port:COM1", Enabled: true,
		StartTime:    start,
		StartActions: []Action{{Kind: KindBandLock, Tech: "NR", Bands: "78"}},
		EndTime:      end,
		EndActions:   []Action{{Kind: KindBandUnlock, Tech: "NR"}},
	}
}

func TestDuePhases(t *testing.T) {
	task := testTask("08:00", "20:00")
	// 没到点
	if got := task.DuePhases(at(7, 59)); len(got) != 0 {
		t.Errorf("没到点不该执行：%v", got)
	}
	// 正好到点
	if got := task.DuePhases(at(8, 0)); len(got) != 1 || got[0] != PhaseStart {
		t.Errorf("到点应执行 start：%v", got)
	}
	// 补做窗口内（15 分钟内）
	if got := task.DuePhases(at(8, 10)); len(got) != 1 || got[0] != PhaseStart {
		t.Errorf("窗口内应补做：%v", got)
	}
	// 超出窗口
	if got := task.DuePhases(at(9, 0)); len(got) != 0 {
		t.Errorf("超出窗口不该补做：%v", got)
	}
	// 结束槽位独立判定
	if got := task.DuePhases(at(20, 1)); len(got) != 1 || got[0] != PhaseEnd {
		t.Errorf("结束槽位应触发 end：%v", got)
	}
}

func TestDuePhasesDedup(t *testing.T) {
	task := testTask("08:00", "")
	task.StartActions = []Action{{Kind: KindUnlockAll}}
	task.EndActions = nil
	now := at(8, 1)
	// 第一次：没执行过 → 该跑
	if got := task.DuePhases(now); len(got) != 1 {
		t.Fatalf("首次应执行：%v", got)
	}
	// 记下槽位后再问 → 不重复
	task.Slots = map[string]string{PhaseStart: Slot(now, "08:00")}
	if got := task.DuePhases(now); len(got) != 0 {
		t.Errorf("同一槽位不该重复执行：%v", got)
	}
	// 第二天到点 → 又是新槽位
	tomorrow := time.Date(2026, 10, 1, 8, 1, 0, 0, time.Local)
	if got := task.DuePhases(tomorrow); len(got) != 1 {
		t.Errorf("第二天应再次执行：%v", got)
	}
}

func TestDuePhasesWeekday(t *testing.T) {
	task := testTask("08:00", "")
	task.EndActions = nil
	task.Days = []int{1}                                  // 周一
	mon := time.Date(2026, 10, 5, 8, 1, 0, 0, time.Local) // 周一
	wed := time.Date(2026, 10, 7, 8, 1, 0, 0, time.Local) // 周三
	if got := task.DuePhases(wed); len(got) != 0 {
		t.Errorf("非指定星期不该执行：%v", got)
	}
	if got := task.DuePhases(mon); len(got) != 1 {
		t.Errorf("指定星期应执行：%v", got)
	}
	// 停用后一律不执行
	task.Enabled = false
	if got := task.DuePhases(mon); len(got) != 0 {
		t.Errorf("停用的任务不该执行：%v", got)
	}
}

// 跨天时段：开始 23:00 / 结束 07:00 两次都在各自的那一天触发
func TestDuePhasesCrossDay(t *testing.T) {
	task := testTask("23:00", "07:00")
	now := time.Date(2026, 9, 30, 23, 5, 0, 0, time.Local)
	if got := task.DuePhases(now); len(got) != 1 || got[0] != PhaseStart {
		t.Errorf("当晚应执行 start：%v", got)
	}
	next := time.Date(2026, 10, 1, 7, 5, 0, 0, time.Local)
	if got := task.DuePhases(next); len(got) != 1 || got[0] != PhaseEnd {
		t.Errorf("第二天早上应执行 end：%v", got)
	}
}

func TestNextRuns(t *testing.T) {
	task := testTask("08:00", "20:00")
	next := task.NextRuns(at(7, 0), 4)
	if len(next) != 4 {
		t.Fatalf("应给出 4 次：%v", next)
	}
	// 升序，且第一个就是今天的 08:00
	for i := 1; i < len(next); i++ {
		if !next[i].After(next[i-1]) {
			t.Errorf("应升序：%v", next)
		}
	}
	if next[0].Hour() != 8 || next[0].Day() != 30 {
		t.Errorf("第一次应是今天 08:00：%v", next[0])
	}
	// 停用的任务没有后续执行
	task.Enabled = false
	if got := task.NextRuns(at(7, 0), 3); len(got) != 0 {
		t.Errorf("停用任务不该有下一次：%v", got)
	}
}

// ====================== 动作文案 ======================

func TestActionText(t *testing.T) {
	cases := []struct {
		a    Action
		want string
	}{
		{Action{Kind: KindBandLock, Tech: "NR", Bands: "78"}, "锁频段 NR 78"},
		{Action{Kind: KindBandUnlock, Tech: "LTE"}, "解除锁频段 LTE"},
		{Action{Kind: KindCellLock, Tech: "NR", LockType: CellLockARFCNPCI, ARFCN: 627264, PCID: 521}, "锁基站 NR 627264/PCI 521"},
		{Action{Kind: KindCellLock, Tech: "LTE", LockType: CellLockARFCN, ARFCN: 2452}, "锁基站 LTE 频点 2452"},
		{Action{Kind: KindCellUnlock, Tech: "NR"}, "解除锁基站 NR"},
		{Action{Kind: KindUnlockAll}, "解除全部锁定"},
		{Action{Kind: KindModePref, Mode: "AUTO"}, "组网模式 → 自动（2G/3G/4G/5G）"},
		{Action{Kind: KindAirplaneOn}, "开飞行模式"},
		{Action{Kind: KindAirplaneOff}, "关飞行模式"},
		{Action{Kind: "nope"}, "nope"},
	}
	for _, c := range cases {
		if got := ActionText(c.a); got != c.want {
			t.Errorf("ActionText(%+v) = %q，期望 %q", c.a, got, c.want)
		}
	}
	if got := ActionsLabel(nil); got != "-" {
		t.Errorf("空动作列表 = %q, want -", got)
	}
}

// ====================== 持久化 ======================

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")

	s := NewSchedStore()
	s.Init(path)

	task := testTask("08:00", "20:00")
	task.ID = "task-1"
	task.Name = "上班锁 n78"
	task.Last = &Run{Phase: PhaseStart, Slot: "2026-09-30 08:00", At: time.Now(), OK: true, Msg: "已执行"}
	if err := s.Upsert(task); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("文件应已写出：%v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("不应残留 .tmp 文件")
	}

	// 重新载入
	s2 := NewSchedStore()
	s2.Init(path)
	loaded := s2.Snapshot()
	if len(loaded) != 1 {
		t.Fatalf("应载入 1 条，实际 %d", len(loaded))
	}
	if loaded[0].Name != "上班锁 n78" || loaded[0].StartTime != "08:00" || loaded[0].EndTime != "20:00" {
		t.Errorf("载入的任务不对：%+v", loaded[0])
	}
	if loaded[0].Last == nil || !loaded[0].Last.OK {
		t.Error("上次执行结果应一起载入")
	}

	// 更新时保留创建时间与槽位记录（前端不该覆盖它们）
	s2.Record(loaded[0].ID, Run{Phase: PhaseStart, Slot: "2026-09-30 08:00", At: time.Now(), OK: true})
	st := s2.Snapshot()[0]
	before := st.CreatedAt
	st.Name = "改个名字"
	st.Slots = map[string]string{PhaseStart: "前端瞎传的值"} // 不该生效
	if err := s2.Upsert(st); err != nil {
		t.Fatal(err)
	}
	st2 := s2.Snapshot()[0]
	if !st2.CreatedAt.Equal(before) {
		t.Error("更新不该改创建时间")
	}
	if st2.Name != "改个名字" {
		t.Errorf("正常字段应更新：%q", st2.Name)
	}
	if st2.Slots[PhaseStart] != "2026-09-30 08:00" {
		t.Errorf("槽位记录应保留存储里的那份，实际 %q", st2.Slots[PhaseStart])
	}
}

// 文件里的坏任务被跳过，好任务照常载入（顺便验证旧格式 start_action 兼容）
func TestLoadFileSkipsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")
	body := `{"version":1,"tasks":[
	  {"id":"bad1","modem_key":"port:COM1","start_time":"99:99","start_action":{"kind":"band_lock"}},
	  {"id":"bad2","modem_key":"port:COM1","start_time":"08:00","start_actions":[]},
	  {"id":"good","modem_key":"port:COM32","start_time":"08:00","start_action":{"kind":"unlock_all"},"enabled":true},
	  {"id":"good2","modem_key":"port:COM32","start_time":"09:00","enabled":true,
	   "start_actions":[{"kind":"band_lock","tech":"NR","bands":"78"},{"kind":"cell_lock","tech":"NR","lock_type":1,"earfcn":627264,"pcid":521}]}
	]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewSchedStore()
	s.Init(path)
	loaded := s.Snapshot()
	if len(loaded) != 2 {
		t.Fatalf("应只载入 2 条合法任务，实际 %d：%+v", len(loaded), loaded)
	}
	if loaded[0].ID != "good" || loaded[1].ID != "good2" {
		t.Fatalf("载入的任务不对：%+v", loaded)
	}
	if len(loaded[1].StartActions) != 2 || loaded[1].StartActions[1].Kind != KindCellLock {
		t.Errorf("多动作任务应完整载入：%+v", loaded[1].StartActions)
	}
	// 旧格式 start_action 被升级成列表
	if len(loaded[0].StartActions) != 1 || loaded[0].StartActions[0].Kind != KindUnlockAll {
		t.Errorf("旧格式应升级成动作列表：%+v", loaded[0].StartActions)
	}
}

func TestLoadFileMissing(t *testing.T) {
	s := NewSchedStore()
	s.Init(filepath.Join(t.TempDir(), "nope.json")) // 文件不存在不该报错
	if got := s.Snapshot(); len(got) != 0 {
		t.Errorf("文件不存在时应返回空，实际 %v", got)
	}
}

// 删除 / 记录 / 上限
func TestStoreDeleteRecordAndLimit(t *testing.T) {
	s := NewSchedStore()
	s.Init("") // 不落盘

	task := testTask("08:00", "")
	task.EndActions = nil
	task.ID = "a"
	if err := s.Upsert(task); err != nil {
		t.Fatal(err)
	}
	s.Record("a", Run{Phase: PhaseStart, Slot: "2026-09-30 08:00", At: time.Now(), OK: true})

	st := s.Snapshot()[0]
	if st.Slots[PhaseStart] != "2026-09-30 08:00" {
		t.Errorf("槽位记录没写上：%+v", st.Slots)
	}
	if st.Last == nil || !st.Last.OK {
		t.Errorf("上次执行结果没写上：%+v", st.Last)
	}
	// 手动执行不推进槽位
	s.Record("a", Run{Phase: PhaseStart, Slot: "manual", At: time.Now(), OK: true})
	if got := s.Snapshot()[0].Slots[PhaseStart]; got != "2026-09-30 08:00" {
		t.Errorf("手动执行不该改槽位：%q", got)
	}

	if !s.Delete("a") {
		t.Error("删除应成功")
	}
	if s.Delete("a") {
		t.Error("重复删除应返回 false")
	}

	// 任务数上限
	for i := 0; i < MaxTasks; i++ {
		tk := testTask("08:00", "")
		tk.EndActions = nil
		tk.ID = NewID()
		if err := s.Upsert(tk); err != nil {
			t.Fatalf("第 %d 条不该失败：%v", i, err)
		}
	}
	extra := testTask("09:00", "")
	extra.EndActions = nil
	extra.ID = "extra"
	if err := s.Upsert(extra); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Errorf("超过上限应当报错，实际 %v", err)
	}
}

// 日志节流：同一任务在窗口内只放行一次
func TestStoreLogThrottle(t *testing.T) {
	s := NewSchedStore()
	if !s.LogAllowed("a") {
		t.Error("首次应放行")
	}
	if s.LogAllowed("a") {
		t.Error("窗口内第二次应被节流")
	}
	if !s.LogAllowed("b") {
		t.Error("节流按任务分开")
	}
}

// ====================== 视图 ======================

// 视图里动作列表要带文案，且 JSON 里 start_actions 只出现一次（内嵌字段被外层覆盖）
func TestTaskViewJSONActions(t *testing.T) {
	task := &Task{
		ID: "t1", ModemKey: "port:COM32", Enabled: true, StartTime: "08:00",
		StartActions: []Action{
			{Kind: KindBandLock, Tech: "NR", Bands: "78"},
			{Kind: KindCellLock, Tech: "NR", LockType: CellLockARFCNPCI, ARFCN: 627264, PCID: 521},
		},
	}
	item := TaskViewOf(task, time.Now(), "#0 COM32", true, nil)
	b, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), `"start_actions"`); n != 1 {
		t.Errorf("start_actions 应只出现一次，实际 %d 次：%s", n, b)
	}
	var got struct {
		Actions []struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		} `json:"start_actions"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Actions) != 2 {
		t.Fatalf("应输出 2 个动作：%s", b)
	}
	if got.Actions[1].Text != "锁基站 NR 627264/PCI 521" {
		t.Errorf("第二个动作文案不对：%q", got.Actions[1].Text)
	}
	if item.StartUnsupported != "" {
		t.Errorf("没给支持性判断函数时不该报不支持：%q", item.StartUnsupported)
	}
	// 目标不在线时不判定支持性
	if item.TargetLabel != "#0 COM32" || !item.TargetOK {
		t.Errorf("目标信息没带上：%+v", item)
	}
}

func TestTaskViewUnsupportedReason(t *testing.T) {
	task := &Task{
		ID: "t1", ModemKey: "port:COM32", StartTime: "08:00",
		StartActions: []Action{{Kind: KindBandLock, Tech: "NR", Bands: "78"}},
	}
	item := TaskViewOf(task, time.Now(), "#0 COM32", true, func(a Action) string {
		if a.Kind == KindBandLock {
			return "固件不支持锁网"
		}
		return ""
	})
	if item.StartActions[0].Unsupported != "固件不支持锁网" {
		t.Errorf("不支持原因没带上：%+v", item.StartActions[0])
	}
	if item.StartUnsupported != "固件不支持锁网" {
		t.Errorf("第一个不支持原因 = %q", item.StartUnsupported)
	}
}

func TestNewIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id := NewID()
		if id == "" || seen[id] {
			t.Fatalf("id 应唯一且非空：%q", id)
		}
		seen[id] = true
	}
}

func TestPath(t *testing.T) {
	if got := Path("/etc/modemd/config.toml"); got != filepath.Join("/etc/modemd", "schedule.json") {
		t.Errorf("Path = %q", got)
	}
	if got := Path("  "); got != "schedule.json" {
		t.Errorf("无配置路径时应退回当前目录：%q", got)
	}
}

// ---- from: atcmd/vendorcmd_test.go ----

// TestDetectVendorFromATI 各厂商 ATI 应答都能识别出来。
// 以前是拿"当前指令实现"的 DetectVendor 去判断，初始实现是兜底实现，
// 它只返回 VendorUnknown → 广和通/芯讯通/MT5700 永远识别不出来。

func TestDetectVendorFromATI(t *testing.T) {
	cases := []struct {
		name string
		ati  []string
		want ModemVendor
	}{
		{"quectel", []string{"Quectel", "RM502Q-GL", "Revision: RM502QGLAAR11A02M4G", "OK"}, VendorQuectel},
		{"fibocom", []string{"Fibocom", "FM650", "OK"}, VendorFibocom},
		{"simcom", []string{"SIMCOM", "SIM8200EA", "OK"}, VendorSimcom},
		{"meig", []string{"Manufacturer: MeiG", "Model: SRM815", "OK"}, VendorMeig},
		{"meiglink", []string{"MeigLink SLM750", "OK"}, VendorMeig},
		{"meig-sl-mid", []string{"SLM770A", "OK"}, VendorMeig},
		{"mt5700", []string{"MT5700", "Revision: MT5700_V1.0", "OK"}, VendorTDTech},
		{"mt5700-lower", []string{"mt5700a", "OK"}, VendorTDTech},
		{"unknown", []string{"SomeUnknownModule", "OK"}, VendorUnknown},
	}
	for _, c := range cases {
		if got := DetectVendorFromATI(c.ati); got != c.want {
			t.Errorf("%s: DetectVendorFromATI = %q, want %q", c.name, got, c.want)
		}
		// 各厂商自己的 DetectVendor 也要认得出自己
		if c.want != VendorUnknown {
			if got := NewVendorCommand(c.want).DetectVendor(c.ati); got != c.want {
				t.Errorf("%s: DetectVendor = %q, want %q", c.name, got, c.want)
			}
		}
	}
}

// TestUnknownVendorFallsBackToAdaptive 未识别型号用自适应指令集兜底：
// 首选指令与移远完全一致（保持向后兼容），但额外具备候选回退能力。
func TestUnknownVendorFallsBackToAdaptive(t *testing.T) {
	cmd := NewVendorCommand(VendorUnknown)
	if _, ok := cmd.(*common.UnknownCommand); !ok {
		t.Fatalf("未知厂商应使用 unknownCommand，实际 %T", cmd)
	}
	if cmd.CommandServingCell() != common.CmdQENGServing {
		t.Fatalf("CommandServingCell = %q, want %q", cmd.CommandServingCell(), common.CmdQENGServing)
	}
	if cmd.CommandCSQ() != "AT+CSQ" {
		t.Fatalf("CommandCSQ = %q", cmd.CommandCSQ())
	}
	cc, ok := cmd.(CommandCandidates)
	if !ok {
		t.Fatal("未知厂商应支持候选指令探测")
	}
	if len(cc.ServingCellCandidates()) < 2 {
		t.Fatalf("候选指令过少: %v", cc.ServingCellCandidates())
	}
	if cmd.CommandCAInfo() != "AT+QCAINFO" {
		t.Fatalf("未知厂商沿用移远 CA 指令，实际 %q", cmd.CommandCAInfo())
	}
}

// ====================== MT5700 ======================

func TestMT5700CommandSet(t *testing.T) {
	cmd := &tdtech.MT5700Command{}
	// 标准 3GPP 指令
	for _, c := range []struct {
		name string
		got  string
		want string
	}{
		{"模型", cmd.CommandModel(), "ATI"},
		{"信号", cmd.CommandCSQ(), "AT+CSQ"},
		{"文本模式", cmd.CommandSMSTextMode(), "AT+CMGF=1"},
		{"读短信", cmd.CommandReadAllSMS(), `AT+CMGL="ALL"`},
		{"注册", cmd.CommandNetworkReg(), "AT+CREG?"},
		{"IMEI", cmd.CommandIMEI(), "AT+CGSN"},
		{"固件", cmd.CommandFirmware(), "AT+CGMR"},
		{"存储用量", cmd.CommandQuerySMSStorage(), "AT+CPMS?"},
		{"存储位置", cmd.CommandSMSStorage("SM"), `AT+CPMS="SM","SM","SM"`},
		{"删全部", cmd.CommandDeleteSMS(-1), "AT+CMGD=1,4"},
		{"删单条", cmd.CommandDeleteSMS(3), "AT+CMGD=3"},
		{"运营商", cmd.CommandOperator(), "AT+COPS?"},
		{"发送", cmd.CommandSendSMS("10086"), `AT+CMGS="10086"`},
		// 鼎桥有自家的网络信息指令；AT+QNWINFO 实测回 ERROR
		{"网络信息", cmd.CommandNetInfo(), "AT^SYSINFOEX"},
	} {
		if c.got != c.want {
			t.Errorf("%s: %q, want %q", c.name, c.got, c.want)
		}
	}
	// MT5700 的 CA 指令未验证 —— 不发未经验证的指令
	if got := cmd.CommandCAInfo(); got != "" {
		t.Fatalf("CommandCAInfo = %q, want 空（未验证不发）", got)
	}
}

// TestMT5700ServingCellCandidates 候选指令按可能性降序；
// ⚠️ 首选必须是鼎桥自家的 AT^MONSC：实测 AT+QENG/AT+CPSI?/AT+QCSQ 在该模组上全回 ERROR，
// 它们是留给“同一固件家族的其它型号”的兜底候选（探测到 ERROR 会自动顺延下一个）。
func TestMT5700ServingCellCandidates(t *testing.T) {
	cmd := &tdtech.MT5700Command{}
	cands := cmd.ServingCellCandidates()
	if len(cands) != 4 {
		t.Fatalf("候选数 = %d, want 4（%v）", len(cands), cands)
	}
	if cands[0] != "AT^MONSC" {
		t.Fatalf("首选应为 %q，实际 %q", "AT^MONSC", cands[0])
	}
	joined := strings.Join(cands, " ")
	for _, want := range []string{common.CmdQENGServing, "AT+CPSI?", "AT+QCSQ"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("候选里缺少 %s: %v", want, cands)
		}
	}
	if got := cmd.NeighbourCellCandidates(); len(got) == 0 || got[0] != "AT^MONNC" {
		t.Fatalf("邻区候选应以 AT^MONNC 开头：%v", got)
	}
}

// ====================== 自适应信号解析 ======================
// ⚠️ 这几条测的是"未识别型号"的自适应兜底解析（量纲定位）。
// MT5700 现在有自己的 ParseNRSignal（^MONSC/^HCSQ），所以这里改用 unknownCommand。

// TestAdaptiveParseNRFromQENG +QENG/+FENG 的 servingcell（已实机验证的格式）
func TestAdaptiveParseNRFromQENG(t *testing.T) {
	cmd := &common.UnknownCommand{}
	for _, c := range []struct {
		line             string
		rsrp, rsrq, sinr int
	}{
		{`+QENG: "servingcell","NOCONN","NR5G-SA","TDD",460,11,CFA11A009,521,CFA102,627264,78,3,-113,-12,6,1,7`, -113, -12, 6},
		{`+QENG: "servingcell","NR","FDD",460,00,1051E8F2,123,0,-95,-12,14,35`, -95, -12, 14},
	} {
		rsrp, rsrq, sinr, ok := cmd.ParseNRSignal(c.line)
		if !ok {
			t.Fatalf("解析失败: %s", c.line)
		}
		if rsrp != c.rsrp || rsrq != c.rsrq || sinr != c.sinr {
			t.Errorf("%s → (%d,%d,%d), want (%d,%d,%d)", c.line, rsrp, rsrq, sinr, c.rsrp, c.rsrq, c.sinr)
		}
	}
}

// TestAdaptiveParseNRFromCPSI +CPSI 这类没有固定文档的格式：按量纲定位
func TestAdaptiveParseNRFromCPSI(t *testing.T) {
	cmd := &common.UnknownCommand{}
	line := `+CPSI: NR5G,Online,460-11,0x1051E8F2,627264,504,78,100,-,-95,-12,-7,13`
	rsrp, rsrq, sinr, ok := cmd.ParseNRSignal(line)
	if !ok {
		t.Fatalf("解析失败: %s", line)
	}
	if rsrp != -95 || rsrq != -12 {
		t.Fatalf("RSRP/RSRQ = %d/%d, want -95/-12", rsrp, rsrq)
	}
	// -7 不在 RSSI 量纲（-113 ~ -25）里，因此按量纲规则归为 SINR
	if sinr != -7 {
		t.Fatalf("SINR = %d, want -7", sinr)
	}
}

// TestAdaptiveParseNRFromQCSQ +QCSQ: "NR5G",<rsrp>,<rsrq>,<sinr>,<rssi>
func TestAdaptiveParseNRFromQCSQ(t *testing.T) {
	cmd := &common.UnknownCommand{}
	rsrp, rsrq, sinr, ok := cmd.ParseNRSignal(`+QCSQ: "NR5G",-95,-12,14,-40`)
	if !ok {
		t.Fatal("+QCSQ 解析失败")
	}
	if rsrp != -95 || rsrq != -12 || sinr != 14 {
		t.Fatalf("(%d,%d,%d), want (-95,-12,14)", rsrp, rsrq, sinr)
	}
}

// TestAdaptiveParseNRSignalRejectsNoise 不该被误判成服务小区信号的行。
// 尤其是邻区行 —— 以前只有 servingcell 会解析，引入量纲兜底后必须仍然拒绝。
func TestAdaptiveParseNRSignalRejectsNoise(t *testing.T) {
	cmd := &tdtech.MT5700Command{}
	for _, line := range []string{
		// 邻区行：以前只有 servingcell 会解析，引入量纲兜底后必须仍然拒绝
		`+QENG: "neighbourcell inter","NR",636666,15,-91,-11,16`,
		`+QENG: "neighbourcell intra","LTE",2452,155,-15,-103,-70,-,21,3`,
		// 短信行（含时间戳里的负数）
		`+CMGL: 0,"REC UNREAD","13520309333",,"26/09/28,22:21:40+32"`,
		`+CSQ: 99,99`, // CSQ 只有 RSSI/BER，不带 RSRP
		`+CPSI: NR5G,Online,460-11,0x1051E8F2,627264,504,78,100,-`,
		`+QNWINFO: "TDD NR5G",46011,"NR5G BAND 78",627264`,
		`OK`,
	} {
		if rsrp, _, _, ok := cmd.ParseNRSignal(line); ok {
			t.Errorf("不应解析出信号值(%d): %s", rsrp, line)
		}
	}
}

// TestIsSignalReportLine 只有 +CPSI/+QCSQ 才允许走量纲兜底
func TestIsSignalReportLine(t *testing.T) {
	for _, c := range []struct {
		line string
		want bool
	}{
		{`+CPSI: NR5G,Online`, true},
		{`+QCSQ: "NR5G",-95,-12,14`, true},
		{`  +cpsi: LTE,Online`, true},
		{`+QENG: "servingcell","NR"`, false},
		{`+CMGL: 0,"REC UNREAD"`, false},
	} {
		if got := cell.IsSignalReportLine(c.line); got != c.want {
			t.Errorf("isSignalReportLine(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

// ====================== +CPSI 服务小区解析 ======================

func TestParseCPSICell(t *testing.T) {
	line := `+CPSI: NR5G,Online,460-11,0x1051E8F2,627264,504,78,100,-,-95,-12,-7,13`
	ci, ok := cell.ParseCPSICell(line)
	if !ok {
		t.Fatalf("解析失败: %s", line)
	}
	if ci.Tech != CellTechNR {
		t.Errorf("tech = %q, want NR", ci.Tech)
	}
	if ci.CellID != "1051E8F2" {
		t.Errorf("cell_id = %q, want 1051E8F2（去掉 0x 前缀）", ci.CellID)
	}
	if ci.EARFCN != 627264 {
		t.Errorf("earfcn = %d", ci.EARFCN)
	}
	if ci.PCID != 504 {
		t.Errorf("pcid = %d", ci.PCID)
	}
	if ci.Band != 78 {
		t.Errorf("band = %d", ci.Band)
	}
	if ci.RSRP != -95 || ci.RSRQ != -12 {
		t.Errorf("rsrp/rsrq = %d/%d", ci.RSRP, ci.RSRQ)
	}
	if ci.MCC != "460" || ci.MNC != "11" {
		t.Errorf("plmn = %s/%s", ci.MCC, ci.MNC)
	}
	if !ci.Serving {
		t.Error("应标记为服务小区")
	}
	if ci.Operator != "中国电信" {
		t.Errorf("operator = %q", ci.Operator)
	}
}

// TestParseCPSICellLTE LTE 形态（PLMN 无分隔符）
func TestParseCPSICellLTE(t *testing.T) {
	line := `+CPSI: LTE,Online,46011,0x1F2A,2452,175,5,20,-,-104,-16,-69,7`
	ci, ok := cell.ParseCPSICell(line)
	if !ok {
		t.Fatalf("解析失败: %s", line)
	}
	if ci.Tech != CellTechLTE || ci.Band != 5 || ci.PCID != 175 {
		t.Fatalf("解析结果异常: %+v", ci)
	}
	if ci.MCC != "460" || ci.MNC != "11" {
		t.Errorf("plmn = %s/%s", ci.MCC, ci.MNC)
	}
}

// TestParseCPSICellInvalid 任何一项不合法就整体放弃（宁可不显示也不显示错的数据）
func TestParseCPSICellInvalid(t *testing.T) {
	for _, line := range []string{
		// 字段不够
		`+CPSI: NR5G,Online`,
		// 非 LTE/NR 制式
		`+CPSI: GSM,Online,460-11,0x1F2A,2452,175,5,20`,
		// PCI 越界
		`+CPSI: NR5G,Online,460-11,0x1051E8F2,627264,9999,78,100,-,-95,-12`,
		// band 越界
		`+CPSI: NR5G,Online,460-11,0x1051E8F2,627264,504,999,100,-,-95,-12`,
		// 频点为 0
		`+CPSI: NR5G,Online,460-11,0x1051E8F2,0,504,78,100,-,-95,-12`,
		// 不是 CPSI 行
		`+QENG: "servingcell","NR","FDD",460,00,1051E8F2,123,0,-95,-12,14,35`,
	} {
		if ci, ok := cell.ParseCPSICell(line); ok {
			t.Errorf("不应解析成功: %s → %+v", line, ci)
		}
	}
}

// ====================== 其他 ======================
//
// respHasError / Modem.vendorCandidates 属于引擎侧（要 *Modem），
// 这两条用例在 modem_test.go。

// ---- from: wechat/wechat_test.go ----

// 发送消息必须完全按官方字段：路径、请求头、msg 结构（否则微信侧直接拒）

func TestILinkSendTextRequest(t *testing.T) {
	var gotPath, gotAuth, gotAuthType, gotAppID, gotVer, gotUIN, gotUA string
	var body map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAuthType = r.Header.Get("AuthorizationType")
		gotAppID = r.Header.Get("iLink-App-Id")
		gotVer = r.Header.Get("iLink-App-ClientVersion")
		gotUIN = r.Header.Get("X-WECHAT-UIN")
		gotUA = r.Header.Get("User-Agent")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"ret":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	cl := newILinkClient(srv.URL, "TOKEN-abc")
	if err := cl.sendText(context.Background(), "u123@im.wechat", "你好", "", ""); err != nil {
		t.Fatalf("发送应成功：%v", err)
	}

	if gotPath != "/ilink/bot/sendmessage" {
		t.Errorf("路径 = %q", gotPath)
	}
	if gotAuth != "Bearer TOKEN-abc" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAuthType != "ilink_bot_token" {
		t.Errorf("AuthorizationType = %q", gotAuthType)
	}
	if gotAppID != ilinkAppID {
		t.Errorf("iLink-App-Id = %q", gotAppID)
	}
	if gotVer != "132104" { // 0x020408 = 2<<16 | 4<<8 | 8
		t.Errorf("iLink-App-ClientVersion = %q，期望 132104", gotVer)
	}
	if gotUA != ilinkBotAgent {
		t.Errorf("User-Agent = %q", gotUA)
	}
	if dec, err := base64.StdEncoding.DecodeString(gotUIN); err != nil {
		t.Errorf("X-WECHAT-UIN 不是合法 base64：%q", gotUIN)
	} else if _, err := json.Marshal(string(dec)); err != nil || len(dec) == 0 {
		t.Errorf("X-WECHAT-UIN 解出来是空的：%q", gotUIN)
	}

	msg, _ := body["msg"].(map[string]any)
	if msg == nil {
		t.Fatalf("请求体缺少 msg：%v", body)
	}
	if msg["to_user_id"] != "u123@im.wechat" {
		t.Errorf("to_user_id = %v", msg["to_user_id"])
	}
	if msg["message_type"].(float64) != 2 || msg["message_state"].(float64) != 2 {
		t.Errorf("message_type/state 应为 2/2：%v", msg)
	}
	if id, _ := msg["client_id"].(string); len(id) != 32 {
		t.Errorf("client_id 应为 32 位 hex，实际 %q", id)
	}
	items, _ := msg["item_list"].([]any)
	if len(items) != 1 {
		t.Fatalf("item_list 应只有一项：%v", msg["item_list"])
	}
	item, _ := items[0].(map[string]any)
	if item["type"].(float64) != 1 {
		t.Errorf("文本消息 type 应为 1：%v", item)
	}
	ti, _ := item["text_item"].(map[string]any)
	if ti["text"] != "你好" {
		t.Errorf("text = %v", ti["text"])
	}
	bi, _ := body["base_info"].(map[string]any)
	if bi["channel_version"] != ilinkChannelVersion || bi["bot_agent"] != ilinkBotAgent {
		t.Errorf("base_info 不对：%v", bi)
	}
}

// ret != 0 必须报错（不能当成功）
func TestILinkSendTextRejectsRetNonZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":-14,"errmsg":"session expired"}`))
	}))
	defer srv.Close()
	cl := newILinkClient(srv.URL, "T")
	err := cl.sendText(context.Background(), "u@im.wechat", "hi", "", "")
	if err == nil || !strings.Contains(err.Error(), "session expired") {
		t.Fatalf("ret=-14 应报错并带 errmsg，实际 %v", err)
	}
	// 目标为空也要拦住，不要白发请求
	if err := cl.sendText(context.Background(), "  ", "hi", "", ""); err == nil {
		t.Error("推送目标为空时应直接报错")
	}
}

// ====================== iLink 协议：登录 ======================

func TestILinkQRCodeFlow(t *testing.T) {
	var qrBody map[string]any
	var statusPath string
	confirmed := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/ilink/bot/get_bot_qrcode"):
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &qrBody)
			if r.URL.Query().Get("bot_type") != "3" {
				t.Errorf("bot_type 应为 3，实际 %q", r.URL.Query().Get("bot_type"))
			}
			_, _ = w.Write([]byte(`{"qrcode":"QK-1","qrcode_img_content":"https://weixin.qq.com/x/abc"}`))
		case strings.HasPrefix(r.URL.Path, "/ilink/bot/get_qrcode_status"):
			statusPath = r.URL.Path + "?" + r.URL.RawQuery
			if !confirmed {
				_, _ = w.Write([]byte(`{"status":"scaned"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"confirmed","bot_token":"BT-1","ilink_bot_id":"b1@im.bot",
				"baseurl":"https://ilinkai.weixin.qq.com","ilink_user_id":"u1@im.wechat"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cl := newILinkClient(srv.URL, "")
	sess, err := cl.fetchQrCode(context.Background(), []string{"old-token"})
	if err != nil {
		t.Fatalf("申请二维码失败：%v", err)
	}
	if sess.Qrcode != "QK-1" || sess.Content != "https://weixin.qq.com/x/abc" {
		t.Errorf("二维码字段解析不对：%+v", sess)
	}
	tokens, _ := qrBody["local_token_list"].([]any)
	if len(tokens) != 1 || tokens[0] != "old-token" {
		t.Errorf("local_token_list 应带上本地 token（用于判断已绑定）：%v", qrBody)
	}

	st := cl.pollQrStatus(context.Background(), srv.URL, sess.Qrcode, "")
	if !strings.EqualFold(st.Status, ilinkStatusScanned) {
		t.Errorf("状态应为 scaned，实际 %q", st.Status)
	}
	if !strings.Contains(statusPath, "qrcode=QK-1") {
		t.Errorf("轮询没带上 qrcode：%q", statusPath)
	}

	confirmed = true
	st = cl.pollQrStatus(context.Background(), srv.URL, sess.Qrcode, "8899")
	if !st.confirmed() || st.BotToken != "BT-1" || st.BotID != "b1@im.bot" || st.UserID != "u1@im.wechat" {
		t.Fatalf("confirmed 响应解析不对：%+v", st)
	}
	if !strings.Contains(statusPath, "verify_code=8899") {
		t.Errorf("配对码没带上：%q", statusPath)
	}
}

// 网络异常 / 网关 5xx / 超时都当 wait（官方 SDK 口径），不能当登录失败
func TestILinkPollStatusTolerant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	cl := newILinkClient(srv.URL, "")
	if st := cl.pollQrStatus(context.Background(), srv.URL, "Q", ""); st.Status != ilinkStatusWait {
		t.Errorf("5xx 应退化为 wait，实际 %q", st.Status)
	}
	// 连不上的地址也一样
	dead := newILinkClient("http://127.0.0.1:1", "")
	if st := dead.pollQrStatus(context.Background(), "http://127.0.0.1:1", "Q", ""); st.Status != ilinkStatusWait {
		t.Errorf("连不上应退化为 wait，实际 %q", st.Status)
	}
	// 响应缺少字段要报错（不能让上层拿着空二维码去轮询）
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"qrcode":""}`))
	}))
	defer srv2.Close()
	if _, err := newILinkClient(srv2.URL, "").fetchQrCode(context.Background(), nil); err == nil {
		t.Error("二维码响应缺字段应报错")
	}
}

// ====================== iLink 协议：收消息 ======================

func TestILinkGetUpdates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/getupdates" {
			t.Errorf("路径 = %q", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if body["get_updates_buf"] != "BUF-0" {
			t.Errorf("应带上次的游标：%v", body)
		}
		_, _ = w.Write([]byte(`{"ret":0,"errcode":0,"get_updates_buf":"BUF-1","longpolling_timeout_ms":35000,
		  "msgs":[
		    {"seq":1,"message_id":99,"from_user_id":"u1@im.wechat","to_user_id":"b1@im.bot",
		     "context_token":"CT-1","message_type":1,"message_state":2,
		     "item_list":[{"type":1,"text_item":{"text":"你好"}}]},
		    {"seq":2,"from_user_id":"b1@im.bot","message_type":2,"message_state":2,
		     "item_list":[{"type":3,"voice_item":{"text":"语音转写"}}]}
		  ]}`))
	}))
	defer srv.Close()

	cl := newILinkClient(srv.URL, "T")
	up, err := cl.getUpdates(context.Background(), "BUF-0")
	if err != nil {
		t.Fatalf("getupdates 失败：%v", err)
	}
	if up.Buf != "BUF-1" || len(up.Msgs) != 2 {
		t.Fatalf("解析不对：%+v", up)
	}
	m := up.Msgs[0]
	if !m.fromUser() || m.Text != "你好" || m.FromUserID != "u1@im.wechat" || m.ContextToken != "CT-1" {
		t.Errorf("用户消息解析不对：%+v", m)
	}
	if up.Msgs[1].fromUser() {
		t.Error("机器人自己发的消息不应算用户消息")
	}
	if up.Msgs[1].effectiveText() != "语音转写" {
		t.Errorf("语音转写应作为可读文本：%+v", up.Msgs[1])
	}
}

// 会话过期：errcode -14 要能识别出来（调用方据此清游标）
func TestILinkGetUpdatesSessionExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ret":-1,"errcode":-14,"errmsg":"session expired"}`))
	}))
	defer srv.Close()
	up, err := newILinkClient(srv.URL, "T").getUpdates(context.Background(), "B")
	if err != nil {
		t.Fatalf("不该报错：%v", err)
	}
	if up.ErrCode != -14 {
		t.Errorf("errcode = %d，期望 -14", up.ErrCode)
	}
}

// ====================== 推送：指纹与去重 ======================

func smsListOf(n ...string) []sms.SMS {
	out := make([]sms.SMS, 0, len(n))
	for i, c := range n {
		out = append(out, sms.SMS{Index: string(rune('0' + i)), Number: "10086", Timestamp: "26/09/30,10:0" + string(rune('0'+i)) + ":00+32", Content: c})
	}
	// ReadSMS 返回的是"最新在前"
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// 第一次只建基线（不能把历史短信全推一遍），之后新增才推；顺序按时间正序
func TestWeChatFreshSMSDedup(t *testing.T) {
	r := &wechatRuntime{seenSet: map[string]bool{}}

	list := smsListOf("旧一", "旧二")
	if got := r.freshSMSLocked(list); len(got) != 0 {
		t.Fatalf("首次应只建基线、不返回新短信，实际 %d 条", len(got))
	}
	if !r.ready || len(r.seen) != 2 {
		t.Fatalf("基线与指纹表没建立：ready=%v seen=%d", r.ready, len(r.seen))
	}

	// 同一批再来一次：没有新短信
	if got := r.freshSMSLocked(list); len(got) != 0 {
		t.Fatalf("重复读取不该产生新短信，实际 %v", got)
	}

	// 来了新短信（最新在前）：只推新的，且按时间正序
	list2 := append(smsListOf("新一", "新二"), list...)
	got := r.freshSMSLocked(list2)
	if len(got) != 2 {
		t.Fatalf("应识别出 2 条新短信，实际 %d：%+v", len(got), got)
	}
	if got[0].Content != "新一" || got[1].Content != "新二" {
		t.Errorf("顺序应为旧→新，实际 %q → %q", got[0].Content, got[1].Content)
	}

	// 指纹与存储位置无关：换一个 index 的老短信不该被当成新的
	list3 := []sms.SMS{{Index: "7", Number: "10086", Timestamp: list[0].Timestamp, Content: list[0].Content}}
	if got := r.freshSMSLocked(list3); len(got) != 0 {
		t.Errorf("同一内容的短信换位置后不应重推：%+v", got)
	}
}

// 分条短信按段指纹推（每段带序号），内容相同但段号不同的两条短信互不干扰
func TestWeChatSMSKeyParts(t *testing.T) {
	a := sms.SMS{Number: "10086", Timestamp: "T", Content: "段落", PartRef: 1, PartTotal: 2, PartSeq: 1}
	b := a
	b.PartSeq = 2
	if wechatSMSKey(a) == wechatSMSKey(b) {
		t.Error("不同分条段应有不同指纹")
	}
	c := a
	c.Index = "9"
	if wechatSMSKey(a) != wechatSMSKey(c) {
		t.Error("指纹不应包含存储位置")
	}
}

// ====================== 推送：模板 ======================

func TestWeChatRenderSMS(t *testing.T) {
	mod := ModemInfo{ID: 0, ATPort: "COM32", Model: "Manufacturer: x|Model: FM160-EAU|Firmware: y",
		ModelName: "FM160-EAU", IMEI: "869814060966904"}
	s := sms.SMS{Number: "+8613800138000", Timestamp: "26/09/30,10:01:02+32", Content: "验证码 123456"}

	got := renderSMS(mod, s, "号码={number} 内容={content}")
	if got != "号码=+8613800138000 内容=验证码 123456" {
		t.Errorf("自定义模板渲染不对：%q", got)
	}

	// 默认模板：带模组标签与分条标记
	s.PartTotal = 2
	s.PartSeq = 2
	got = renderSMS(mod, s, "")
	for _, want := range []string{"新短信", "#0 COM32 FM160-EAU", "验证码 123456", "（2/2）"} {
		if !strings.Contains(got, want) {
			t.Errorf("默认模板应包含 %q，实际 %q", want, got)
		}
	}
	// 没填制式时不带分条后缀
	s.PartTotal, s.PartSeq = 1, 0
	if got = renderSMS(mod, s, ""); strings.Contains(got, "（") {
		t.Errorf("单条短信不该有分条标记：%q", got)
	}
}

// ====================== 二维码渲染 ======================

// 后端自己把二维码内容画成 PNG（离线，走 go-qrcode），前端直接放 <img>
func TestWeChatQRDataURI(t *testing.T) {
	uri, err := wechatQRDataURI("https://weixin.qq.com/x/abcdefg")
	if err != nil {
		t.Fatalf("生成二维码失败：%v", err)
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(uri, prefix) {
		t.Fatalf("应是 PNG data URI，实际 %q", truncateLog(uri, 40))
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, prefix))
	if err != nil {
		t.Fatalf("base64 解码失败：%v", err)
	}
	img, err := png.Decode(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("解不出 PNG：%v", err)
	}
	if img.Bounds().Dx() != wechatQRWidth {
		t.Errorf("二维码边长应为 %d，实际 %d", wechatQRWidth, img.Bounds().Dx())
	}
	if _, err := wechatQRDataURI("   "); err == nil {
		t.Error("空内容应报错")
	}
}

// ====================== 端到端链路：新短信 → 模板 → sendmessage ======================

// 整条链路（不依赖真实微信服务器）：读到新短信 → 指纹判定 → 模板渲染 → 队列 → sendmessage 请求
func TestWeChatSMSForwardPipeline(t *testing.T) {
	var gotText string
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			t.Errorf("路径 = %q", r.URL.Path)
		}
		hits++
		var body struct {
			Msg struct {
				ItemList []struct {
					TextItem struct {
						Text string `json:"text"`
					} `json:"text_item"`
				} `json:"item_list"`
			} `json:"msg"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if len(body.Msg.ItemList) > 0 {
			gotText = body.Msg.ItemList[0].TextItem.Text
		}
		_, _ = w.Write([]byte(`{"ret":0,"errmsg":"ok"}`))
	}))
	defer srv.Close()

	// 这个用例会改全局单例，用完还原（别影响别的测试）
	old := wechatInst
	wechatInst = &wechatRuntime{seenSet: map[string]bool{}, queue: make(chan string, 8), stage: wechatStageReady}
	defer func() { wechatInst = old }()
	r := wechatInst
	r.cfg = wechatCfgSnapshot{
		Enabled: true, BotToken: "T", BotID: "b@im.bot", ToUserID: "u@im.wechat",
		Template: "SMS {number}: {content}", BaseURL: srv.URL,
	}
	r.client = newILinkClient(srv.URL, "T")

	mod := ModemInfo{ID: 0, ATPort: "COM32", IMEI: "123"}
	base := smsListOf("历史短信")

	// 第一次读：只建基线，不推历史短信
	OnSMS(mod, base)
	if n := len(r.queue); n != 0 {
		t.Fatalf("首次不该推送历史短信，队列里有 %d 条", n)
	}

	// 来了新短信 → 入队一条
	fresh := append([]sms.SMS{{
		Number: "10086", Timestamp: "26/09/30,11:00:00+32", Content: "验证码 999",
	}}, base...)
	OnSMS(mod, fresh)
	if n := len(r.queue); n != 1 {
		t.Fatalf("应入队 1 条，实际 %d 条", n)
	}
	text := <-r.queue
	if _, err := wechatSendText(context.Background(), text, false); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if hits != 1 {
		t.Errorf("应发出 1 次 sendmessage，实际 %d 次", hits)
	}
	if gotText != "SMS 10086: 验证码 999" {
		t.Errorf("模板渲染内容不对：%q", gotText)
	}

	// 关掉推送开关 → 不再入队
	r.cfg.Enabled = false
	OnSMS(mod, append([]sms.SMS{{
		Number: "10086", Timestamp: "26/09/30,11:01:00+32", Content: "第二条",
	}}, fresh...))
	if n := len(r.queue); n != 0 {
		t.Errorf("关掉开关后不该入队，实际 %d 条", n)
	}

	// 没登录（没有客户端）→ 也不入队
	r.cfg.Enabled = true
	r.client = nil
	OnSMS(mod, append([]sms.SMS{{
		Number: "10086", Timestamp: "26/09/30,11:02:00+32", Content: "第三条",
	}}, fresh...))
	if n := len(r.queue); n != 0 {
		t.Errorf("未登录时不该入队，实际 %d 条", n)
	}

	// 没登录时发测试消息要明确报错（而不是静默失败）
	if err := SendWeChatTest(); err == nil {
		t.Error("未登录时发测试消息应报错")
	}
}

// 退出登录只清凭据、**保留推送开关**：否则用户「重新登录」之后开关其实是关的，
// 现象就是「收到短信不推送」（2026-10-01 实机踩到）。
func TestLogoutWeChatKeepsPushSwitch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path,
		[]byte("[wechat]\nenabled = true\nbot_token = \"T\"\nbot_id = \"b@im.bot\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath, oldCfg := config.ConfigPath, config.Cfg
	config.ConfigPath = path
	defer func() { config.ConfigPath = oldPath; config.Cfg = oldCfg }()

	old := wechatInst
	wechatInst = &wechatRuntime{seenSet: map[string]bool{}, queue: make(chan string, 8)}
	defer func() { wechatInst = old }()
	r := wechatInst
	r.cfg = wechatCfgSnapshot{
		Enabled: true, BotToken: "T", BotID: "b@im.bot", BaseURL: config.DefaultWeChatBaseURL,
	}
	r.client = newILinkClient(config.DefaultWeChatBaseURL, "T")
	r.stage = wechatStageReady

	if err := LogoutWeChat(); err != nil {
		t.Fatalf("退出登录失败：%v", err)
	}

	r.mu.Lock()
	cfg, stage, msg, client := r.cfg, r.stage, r.stageMsg, r.client
	r.mu.Unlock()
	if !cfg.Enabled {
		t.Error("退出登录不该关掉推送开关（重新登录后会变成「收到短信不推送」）")
	}
	if cfg.BotToken != "" || cfg.BotID != "" {
		t.Errorf("凭据应被清掉：token=%q bot_id=%q", cfg.BotToken, cfg.BotID)
	}
	if client != nil {
		t.Error("退出后客户端应清空")
	}
	if stage != wechatStageIdle || msg == "" {
		t.Errorf("应回到 idle 并带说明：stage=%q msg=%q", stage, msg)
	}

	// 落盘：enabled 保留、token 清掉
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "enabled = true") {
		t.Errorf("写回的配置应保留 enabled = true：\n%s", raw)
	}
	if strings.Contains(string(raw), `bot_token = "T"`) {
		t.Errorf("写回的配置应清掉 bot_token：\n%s", raw)
	}

	// 没登录时（client == nil）开关即使是开的也不推
	OnSMS(ModemInfo{ID: 0, ATPort: "COM32"}, smsListOf("退出登录期间的短信"))
	if n := len(r.queue); n != 0 {
		t.Errorf("未登录时不该入队，实际 %d 条", n)
	}
}

// bot_token / bot_id / bot_user_id 都能从接口写回，并且落进 config.toml。
// ⚠️ 三个必须齐全：启动时只有 `bot_token != "" && bot_id != ""` 才会重建客户端并进入
// ready（见 InitWeChatRuntime），少一个就是"重启后又变回未登录"（2026-10-01 实机踩到）。
func TestApplyWeChatConfigBotCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path,
		[]byte("[wechat]\nenabled = false\nbot_token = \"\"\nbot_id = \"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath, oldCfg := config.ConfigPath, config.Cfg
	config.ConfigPath = path
	defer func() { config.ConfigPath = oldPath; config.Cfg = oldCfg }()

	old := wechatInst
	wechatInst = &wechatRuntime{seenSet: map[string]bool{}, queue: make(chan string, 8)}
	defer func() { wechatInst = old }()
	r := wechatInst

	token, botID, userID := "TK-0123456789abcdef", "8b0a3480170e@im.bot", "o9cq802s@im.wechat"
	patch := WeChatConfigPatch{BotToken: &token, BotID: &botID, BotUserID: &userID}
	if err := ApplyWeChatConfig(patch); err != nil {
		t.Fatalf("写回凭据失败：%v", err)
	}

	r.mu.Lock()
	cfg, stage, client := r.cfg, r.stage, r.client
	r.mu.Unlock()
	// ⚠️ 报错信息里不要带 token 值
	if cfg.BotID != botID {
		t.Errorf("cfg.BotID = %q，期望 %q", cfg.BotID, botID)
	}
	if cfg.BotUserID != userID {
		t.Errorf("cfg.BotUserID = %q，期望 %q", cfg.BotUserID, userID)
	}
	if cfg.BotToken == "" {
		t.Error("cfg.BotToken 应为非空")
	}
	if client == nil {
		t.Error("拿到凭据后应重建客户端")
	}
	if stage != wechatStageReady {
		t.Errorf("拿到凭据后 stage 应为 ready，实际 %q", stage)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `bot_id = "8b0a3480170e@im.bot"`) {
		t.Errorf("bot_id 没写进 config.toml：\n%s", s)
	}
	if !strings.Contains(s, `bot_user_id = "o9cq802s@im.wechat"`) {
		t.Errorf("bot_user_id 没写进 config.toml：\n%s", s)
	}
}

// 只有 token 不算"已登录"（启动时要求 token + bot_id 都有）；**后补 bot_id 要立刻生效**，
// 不能等下次重启 —— 从备份恢复凭据时就是这个顺序（2026-10-01 实机验证）。
func TestApplyWeChatConfigNeedsBotID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[wechat]\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath, oldCfg := config.ConfigPath, config.Cfg
	config.ConfigPath = path
	defer func() { config.ConfigPath = oldPath; config.Cfg = oldCfg }()

	old := wechatInst
	wechatInst = &wechatRuntime{seenSet: map[string]bool{}, queue: make(chan string, 8)}
	defer func() { wechatInst = old }()
	r := wechatInst

	token := "TK-0123456789abcdef"
	if err := ApplyWeChatConfig(WeChatConfigPatch{BotToken: &token}); err != nil {
		t.Fatalf("写 token 失败：%v", err)
	}
	r.mu.Lock()
	client, stage := r.client, r.stage
	r.mu.Unlock()
	if client != nil {
		t.Error("只有 token 时不该建客户端（协议还需要 bot_id）")
	}
	if stage != wechatStageIdle {
		t.Errorf("只有 token 时应为 idle，实际 %q", stage)
	}

	botID := "8b0a3480170e@im.bot"
	if err := ApplyWeChatConfig(WeChatConfigPatch{BotID: &botID}); err != nil {
		t.Fatalf("补 bot_id 失败：%v", err)
	}
	r.mu.Lock()
	client, stage = r.client, r.stage
	r.mu.Unlock()
	if client == nil {
		t.Error("补上 bot_id 后应立刻建好客户端（不用等重启）")
	}
	if stage != wechatStageReady {
		t.Errorf("补上 bot_id 后 stage 应为 ready，实际 %q", stage)
	}
}

// ====================== 配置写回 ======================

func TestWeChatConfigWriteBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "toml")
	// 模拟用户已有的配置文件（含注释，写回时不能被吃掉）
	body := "# 顶部注释\n[server]\nlisten = \"127.0.0.1:18080\"\n\n[ui]\nsms_password = \"pw\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := config.ConfigPath
	oldCfg := config.Cfg
	config.ConfigPath = path
	defer func() { config.ConfigPath = old; config.Cfg = oldCfg }()

	tpl := "第一行\n第二行 {content}"
	if err := config.SetWeChat(config.WeChatSettings{
		Enabled: true, BaseURL: config.DefaultWeChatBaseURL, BotToken: "TK", BotID: "b@im.bot",
		BotUserID: "u@im.wechat", ToUserID: "u@im.wechat", Template: tpl, Listen: true, Allowed: "u1,u2",
	}); err != nil {
		t.Fatalf("写回失败：%v", err)
	}

	text, _ := os.ReadFile(path)
	s := string(text)
	if !strings.Contains(s, "# 顶部注释") {
		t.Error("写回不能吃掉注释")
	}
	if !strings.Contains(s, "[wechat]") {
		t.Error("应新建 [wechat] 段")
	}
	// 换行必须转义成 \n（TOML 基本字符串不能跨行），否则文件会被写坏
	if !strings.Contains(s, `template = "第一行\n第二行 {content}"`) {
		t.Errorf("模板应转义为 \\n：\n%s", s)
	}
	if strings.Contains(s, "第一行\n第二行") {
		t.Error("模板里的真实换行不应出现在文件里")
	}

	// 再读回来应完全一致（TOML 会还原换行）
	var got struct {
		WeChat struct {
			Enabled  bool   `toml:"enabled"`
			BotToken string `toml:"bot_token"`
			BotID    string `toml:"bot_id"`
			ToUserID string `toml:"to_user_id"`
			Template string `toml:"template"`
			Listen   bool   `toml:"listen"`
			Allowed  string `toml:"allowed_senders"`
		} `toml:"wechat"`
	}
	if _, err := toml.DecodeFile(path, &got); err != nil {
		t.Fatalf("重新解析失败：%v", err)
	}
	if !got.WeChat.Enabled || got.WeChat.BotToken != "TK" || got.WeChat.BotID != "b@im.bot" ||
		got.WeChat.ToUserID != "u@im.wechat" || !got.WeChat.Listen || got.WeChat.Allowed != "u1,u2" {
		t.Errorf("读回来的配置不对：%+v", got.WeChat)
	}
	if got.WeChat.Template != tpl {
		t.Errorf("模板往返不一致：%q != %q", got.WeChat.Template, tpl)
	}
	// 备份文件应存在（和改密码同一套做法）
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Error("应留下 toml.bak 备份")
	}
}

// ====================== 小工具 ======================

func TestWeChatSenderAllowed(t *testing.T) {
	cases := []struct {
		allowed, from string
		want          bool
	}{
		{"", "u1@im.wechat", true},
		{"u1", "u1@im.wechat", true},
		{"u2,u1", "u1@im.wechat", true},
		{"u2;u3 u4", "u4@im.wechat", true},
		{"u2,U9", "u1@im.wechat", false},
	}
	for _, c := range cases {
		if got := wechatSenderAllowedLocked(c.allowed, c.from); got != c.want {
			t.Errorf("allowed=%q from=%q → %v，期望 %v", c.allowed, c.from, got, c.want)
		}
	}
}

func TestWeChatMaskToken(t *testing.T) {
	if maskToken("") != "" {
		t.Error("空 token 应返回空串（面板不显示）")
	}
	if got := maskToken("abcdefghijklmn"); !strings.HasPrefix(got, "abcdef") || strings.Contains(got, "ghij") {
		t.Errorf("掩码应只露首尾：%q", got)
	}
	if got := maskToken("short"); strings.Contains(got, "short") {
		t.Errorf("短 token 也不应原样显示：%q", got)
	}
}

func TestWeChatStageText(t *testing.T) {
	if wechatStageText(wechatStageReady) != "已登录" {
		t.Error("阶段文案不对")
	}
	if wechatStageText("whatever") != "whatever" {
		t.Error("未知阶段应原样返回")
	}
}
