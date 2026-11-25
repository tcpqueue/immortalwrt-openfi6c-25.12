package kernel

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"modemd/module/common"
	"modemd/module/modedialect"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// scheduleView 组装视图
func scheduleView(mgr *ModemManager) SchedView {
	now := time.Now()
	v := SchedView{Now: now, Kinds: Kinds(), MaxTasks: MaxTasks}

	if mgr != nil {
		for _, m := range mgr.listModems() {
			label := fmt.Sprintf("#%d %s", m.ID, m.ATPort)
			if m.Model != "" {
				if mm := shortModelName(m.Model); mm != "" {
					label += " " + mm
				}
			}
			v.Modems = append(v.Modems, ModemOption{Key: quotaKey(m), Label: label, ID: m.ID})
			if common.NetLockerOf(m.cmdImpl) != nil {
				v.Supported = true
			}
		}
	}

	for _, t := range schedSnapshot() {
		label := t.ModemKey
		ok := false
		var support func(Action) string
		if mod := schedResolve(mgr, t); mod != nil {
			label = fmt.Sprintf("#%d %s", mod.ID, mod.ATPort)
			ok = mod.isRunningNow()
			m := mod
			support = func(a Action) string {
				if err := schedActionSupported(m, a.Kind); err != nil {
					return err.Error()
				}
				return ""
			}
		}
		if !ok {
			label += "（当前不在线）"
		}
		v.Tasks = append(v.Tasks, TaskViewOf(t, now, label, ok, support))
	}
	sort.SliceStable(v.Tasks, func(i, j int) bool {
		if v.Tasks[i].StartTime != v.Tasks[j].StartTime {
			return v.Tasks[i].StartTime < v.Tasks[j].StartTime
		}
		return v.Tasks[i].Name < v.Tasks[j].Name
	})
	return v
}

// pushSchedule 广播定时器状态（前端「定时器」页与概览提示用）
func pushSchedule(mgr *ModemManager) {
	v := scheduleView(mgr)
	broadcast("schedule", nil, v)
}

// schedActionViews 把动作列表转成带文案/支持性的视图（供 kernel 侧使用）。
// 组装本身在 schedule.go 的 ActionViews，这里只是把"模组固件是否支持"接上去；
// mod 为 nil 表示目标不在线，此时不判定支持性。
func schedActionViews(mod *Modem, actions []Action) ([]ActionView, string) {
	if mod == nil {
		return ActionViews(actions, nil)
	}
	return ActionViews(actions, func(a Action) string {
		if err := schedActionSupported(mod, a.Kind); err != nil {
			return err.Error()
		}
		return ""
	})
}

// ---- from: schedule/schedule.go ----

// Package schedule 是「定时器」的纯逻辑：任务/动作模型、参数校验、时间判定
// （到点、补做窗口、接下来几次）与状态落盘。
//
// 需求来源：希望"到点自动锁频段 / 锁基站"，并且**一条任务含两个时间点**：
//
//	开始时间 → 执行「开始动作」（例如锁到 n78）
//	结束时间 → 执行「结束动作」（例如换回 / 解除锁定）
//
// 每个时间点可以带**一组动作**（按顺序依次下发）—— 实际用得上的场景就是
// "锁频段 + 锁基站"要一起做，只有单独一个动作往往驻不进想要的小区：
//
//	08:00  锁频段 NR 78 → 锁基站 NR 627264/PCI 521
//	22:00  解除锁基站 → 解除锁频段
//
// 所以每个任务有两个槽位（phase）：start / end，各自独立判定、独立记录"上次执行"。
// 这样跨天时段也能用：开始 23:00 锁、结束 07:00 解除 —— 两次都在各自的那一天触发。
//
// 判定方式（简单可预期，不引入 cron 解析）：
//
//	每天（或指定星期）的 HH:MM 到点后，在 CatchUp 窗口内执行一次；
//	同一"日期+时间"只执行一次（记录在 slots 里，随状态落盘），所以：
//	  · 进程重启后如果还在窗口内 → 补做（不会漏，也不会重复）；
//	  · 超出窗口才恢复的（例如关机一整天）→ 不补做，避免"一开机就把昨天的动作全跑一遍"。
//
// 动作的执行（锁网 / 飞行模式）要发 AT，留在 kernel（schedule.go）。

const (
	KindBandLock    = "band_lock"    // 锁频段（指定频段列表）
	KindBandUnlock  = "band_unlock"  // 解除频段锁定（按 policy 恢复全量）
	KindCellLock    = "cell_lock"    // 锁基站（频点 [+PCI]）
	KindCellUnlock  = "cell_unlock"  // 解除基站锁定
	KindUnlockAll   = "unlock_all"   // 一键解除全部锁定（频段+基站）
	KindModePref    = "mode_pref"    // 组网模式偏好
	KindAirplaneOn  = "airplane_on"  // 开飞行模式（AT+CFUN=4）
	KindAirplaneOff = "airplane_off" // 关飞行模式（AT+CFUN=1）

	PhaseStart = "start"
	PhaseEnd   = "end"

	// CatchUp 到点后仍允许补做的窗口。进程重启 / 串口忙导致晚几秒是常态，
	// 但"关机一整天"不应该开机就补跑，所以窗口取 15 分钟。
	CatchUp = 15 * time.Minute
	// MaxTasks 任务数上限（主要是防止前端误批量导入）
	MaxTasks = 50
	// MaxActions 单个时间点最多几个动作。常见组合是"锁频段 + 锁基站"，
	// 留到 4 个足够（再加个组网模式/解除），也不至于让界面失控。
	MaxActions = 4
	// SchedSaveEvery 落盘节流（每次执行都会改状态，但不必秒级写盘）
	SchedSaveEvery = 5 * time.Second
	// NextShow 展示"接下来几次执行"
	NextShow = 3
	// LogEvery 同一个任务执行失败后的日志节流（避免坏任务每 10 秒刷屏）
	LogEvery = 5 * time.Minute
)

// Action 一条要执行的动作（含参数）
type Action struct {
	Kind string `json:"kind"`
	// 锁频段 / 锁基站 / 解锁 用
	Tech  string `json:"tech,omitempty"`  // LTE / NR
	Bands string `json:"bands,omitempty"` // "1:3:5:8" 或 "78"
	// 锁基站用
	ARFCN    int  `json:"earfcn,omitempty"`
	PCID     int  `json:"pcid,omitempty"`
	LockType int  `json:"lock_type,omitempty"` // 1=频点+PCI 2=仅频点
	Persist  bool `json:"persist,omitempty"`   // 写 NV（重启后仍生效）
	// 组网模式偏好
	Mode string `json:"mode,omitempty"` // AUTO / LTE / NR5G / LTE:NR5G
}

// Run 一次执行记录（给界面看"上次执行结果"）
type Run struct {
	Phase string    `json:"phase"` // start / end / manual
	Slot  string    `json:"slot"`  // 触发槽位，如 "2026-09-30 08:00"；手动执行为 "manual"
	At    time.Time `json:"at"`
	OK    bool      `json:"ok"`
	Msg   string    `json:"msg,omitempty"`
}

// Task 一条定时任务
type Task struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Enabled bool   `json:"enabled"`

	// ModemKey 目标模组：`port:COM32`（与配额同一套键，避免 IMEI 相同的两台模组串台）
	ModemKey string `json:"modem_key"`
	Port     string `json:"port,omitempty"` // 仅展示
	IMEI     string `json:"imei,omitempty"` // 仅用于换端口后的迁移

	// Days 生效的星期：0=周日 … 6=周六；空 = 每天
	Days []int `json:"days,omitempty"`

	StartTime    string   `json:"start_time"`            // "HH:MM"
	StartActions []Action `json:"start_actions"`         // 非空（至少 1 个）
	EndTime      string   `json:"end_time,omitempty"`    // 可空（不需要结束动作就留空）
	EndActions   []Action `json:"end_actions,omitempty"` // 可空；EndTime 非空时必须有值

	// Slots 各槽位"最后一次执行的时间戳"（"2026-09-30 08:00"），用于去重与补做判定
	Slots map[string]string `json:"slots,omitempty"`
	// Last 最近一次执行结果
	Last *Run `json:"last,omitempty"`

	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// UnmarshalJSON 兼容早期版本的单动作写法（`start_action` / `end_action` 各一个对象）。
// 现在的模型是动作列表：同一个时间点可以"锁频段 + 锁基站"一起下发。
// ⚠️ 内嵌在别的结构体里（例如 API 的请求体）时这个方法**不会被调用** —— encoding/json 对
// 匿名内嵌字段做扁平化处理，所以那边另外用旧字段接了一次（见 api_data.go）。
func (t *Task) UnmarshalJSON(b []byte) error {
	type legacy struct {
		ID       string `json:"id"`
		Name     string `json:"name,omitempty"`
		Enabled  bool   `json:"enabled"`
		ModemKey string `json:"modem_key"`
		Port     string `json:"port,omitempty"`
		IMEI     string `json:"imei,omitempty"`
		Days     []int  `json:"days,omitempty"`

		StartTime    string   `json:"start_time"`
		StartActions []Action `json:"start_actions,omitempty"`
		StartAction  *Action  `json:"start_action,omitempty"` // 旧字段（单动作）

		EndTime    string   `json:"end_time,omitempty"`
		EndActions []Action `json:"end_actions,omitempty"`
		EndAction  *Action  `json:"end_action,omitempty"` // 旧字段（单动作）

		Slots     map[string]string `json:"slots,omitempty"`
		Last      *Run              `json:"last,omitempty"`
		CreatedAt time.Time         `json:"created_at,omitempty"`
		UpdatedAt time.Time         `json:"updated_at,omitempty"`
	}
	var r legacy
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	*t = Task{
		ID: r.ID, Name: r.Name, Enabled: r.Enabled,
		ModemKey: r.ModemKey, Port: r.Port, IMEI: r.IMEI, Days: r.Days,
		StartTime: r.StartTime, StartActions: r.StartActions,
		EndTime: r.EndTime, EndActions: r.EndActions,
		Slots: r.Slots, Last: r.Last, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if len(t.StartActions) == 0 && r.StartAction != nil {
		t.StartActions = []Action{*r.StartAction}
	}
	if len(t.EndActions) == 0 && r.EndAction != nil {
		t.EndActions = []Action{*r.EndAction}
	}
	return nil
}

// ------------------------------------------------------------------ 动作元信息 ---

// KindInfo 动作类型的展示信息（前端下拉用）
type KindInfo struct {
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	NeedsLocker bool   `json:"needs_locker"` // 需要模组支持锁网（NetLocker）
	HasTech     bool   `json:"has_tech"`     // 需要选择制式
	NeedsBands  bool   `json:"needs_bands"`
	NeedsCell   bool   `json:"needs_cell"`
	NeedsMode   bool   `json:"needs_mode"`
}

// Kinds 支持的动作类型（顺序即界面下拉顺序）
func Kinds() []KindInfo {
	return []KindInfo{
		{Kind: KindBandLock, Label: "锁频段", NeedsLocker: true, HasTech: true, NeedsBands: true},
		{Kind: KindBandUnlock, Label: "解除锁频段", NeedsLocker: true, HasTech: true},
		{Kind: KindCellLock, Label: "锁基站（频点/PCI）", NeedsLocker: true, HasTech: true, NeedsCell: true},
		{Kind: KindCellUnlock, Label: "解除锁基站", NeedsLocker: true, HasTech: true},
		{Kind: KindUnlockAll, Label: "解除全部锁定", NeedsLocker: true},
		{Kind: KindModePref, Label: "组网模式偏好", NeedsLocker: true, NeedsMode: true},
		{Kind: KindAirplaneOn, Label: "开飞行模式"},
		{Kind: KindAirplaneOff, Label: "关飞行模式"},
	}
}

// KindLabel 动作类型的展示名（找不到时原样返回 kind）
func KindLabel(kind string) string {
	for _, k := range Kinds() {
		if k.Kind == kind {
			return k.Label
		}
	}
	return kind
}

// KindInfoFor 按类型取元信息
func KindInfoFor(kind string) (KindInfo, bool) {
	for _, k := range Kinds() {
		if k.Kind == kind {
			return k, true
		}
	}
	return KindInfo{}, false
}

// ActionText 动作的可读描述（列表里显示"做了什么"）
func ActionText(a Action) string {
	switch a.Kind {
	case KindBandLock:
		return fmt.Sprintf("锁频段 %s %s", orDash(a.Tech), orDash(a.Bands))
	case KindBandUnlock:
		return fmt.Sprintf("解除锁频段 %s", orDash(a.Tech))
	case KindCellLock:
		if a.LockType == CellLockARFCN {
			return fmt.Sprintf("锁基站 %s 频点 %d", orDash(a.Tech), a.ARFCN)
		}
		return fmt.Sprintf("锁基站 %s %d/PCI %d", orDash(a.Tech), a.ARFCN, a.PCID)
	case KindCellUnlock:
		return fmt.Sprintf("解除锁基站 %s", orDash(a.Tech))
	case KindUnlockAll:
		return "解除全部锁定"
	case KindModePref:
		return "组网模式 → " + orDash(modedialect.ModeLabel(a.Mode))
	case KindAirplaneOn:
		return "开飞行模式"
	case KindAirplaneOff:
		return "关飞行模式"
	}
	return orDash(a.Kind)
}

// ActionsLabel 一组动作的可读描述（多个动作按执行顺序用 " + " 连接）
func ActionsLabel(actions []Action) string {
	if len(actions) == 0 {
		return "-"
	}
	if len(actions) == 1 {
		return ActionText(actions[0])
	}
	parts := make([]string, 0, len(actions))
	for _, a := range actions {
		parts = append(parts, ActionText(a))
	}
	return strings.Join(parts, " + ")
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// DedupeActions 去掉完全重复的动作（保留第一次出现的位置）；顺序保持不变
func DedupeActions(in []Action) []Action {
	out := make([]Action, 0, len(in))
	seen := map[string]bool{}
	for _, a := range in {
		key := fmt.Sprintf("%s|%s|%s|%d|%d|%d|%t|%s",
			a.Kind, a.Tech, a.Bands, a.ARFCN, a.PCID, a.LockType, a.Persist, a.Mode)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, a)
	}
	return out
}

// ------------------------------------------------------------------ 校验 ---

// ParseHHMM 解析 "HH:MM"（容忍 "8:00" / 前后空格）
func ParseHHMM(s string) (int, int, error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("时间格式应为 HH:MM，得到 %q", s)
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("时间格式应为 HH:MM（00:00~23:59），得到 %q", s)
	}
	return h, m, nil
}

// ValidateTask 校验任务；会顺手把时间补成 "HH:MM" 规范写法。
func ValidateTask(t *Task) error {
	if t.ID == "" {
		return fmt.Errorf("缺少任务 id")
	}
	if strings.TrimSpace(t.ModemKey) == "" {
		return fmt.Errorf("请选择目标模组（或直接填串口名，如 COM32）")
	}
	// 开始
	h, m, err := ParseHHMM(t.StartTime)
	if err != nil {
		return fmt.Errorf("开始时间：%w", err)
	}
	t.StartTime = fmt.Sprintf("%02d:%02d", h, m)
	if err := validateActions(t.StartActions, "开始"); err != nil {
		return err
	}
	t.StartActions = DedupeActions(t.StartActions)
	// 结束（可选）
	if strings.TrimSpace(t.EndTime) != "" {
		h2, m2, err := ParseHHMM(t.EndTime)
		if err != nil {
			return fmt.Errorf("结束时间：%w", err)
		}
		t.EndTime = fmt.Sprintf("%02d:%02d", h2, m2)
		if t.EndTime == t.StartTime {
			return fmt.Errorf("结束时间不能与开始时间相同")
		}
		if err := validateActions(t.EndActions, "结束"); err != nil {
			return err
		}
		t.EndActions = DedupeActions(t.EndActions)
	} else {
		t.EndActions = nil // 没结束时间就不保留结束动作，避免"看起来会执行其实不会"
	}
	// 星期
	seen := map[int]bool{}
	days := make([]int, 0, len(t.Days))
	for _, d := range t.Days {
		if d < 0 || d > 6 {
			return fmt.Errorf("星期取值应为 0(周日)~6(周六)，得到 %d", d)
		}
		if !seen[d] {
			seen[d] = true
			days = append(days, d)
		}
	}
	sort.Ints(days)
	t.Days = days
	if t.Name == "" {
		t.Name = ActionsLabel(t.StartActions) + " " + t.StartTime
	}
	return nil
}

// validateActions 校验一组动作（数量上限 + 逐个校验）
// what 用于错误前缀（"开始"/"结束"）
func validateActions(actions []Action, what string) error {
	if len(actions) == 0 {
		if what == "结束" {
			return fmt.Errorf("填了结束时间就要选结束动作（不需要结束动作请把结束时间留空）")
		}
		return fmt.Errorf("%s动作至少要有一个", what)
	}
	if len(actions) > MaxActions {
		return fmt.Errorf("一个时间点最多 %d 个动作（%s动作给的是 %d 个）", MaxActions, what, len(actions))
	}
	for i := range actions {
		if err := ValidateAction(&actions[i]); err != nil {
			if len(actions) == 1 {
				return fmt.Errorf("%s动作：%w", what, err)
			}
			return fmt.Errorf("%s动作 %d（%s）：%w", what, i+1, ActionText(actions[i]), err)
		}
	}
	return nil
}

// ValidateAction 校验单个动作，并顺手归一化参数（频段写法、制式大小写）。
// ⚠️ 用指针：归一化的结果必须写回调用方（值参数只改到副本）。
func ValidateAction(a *Action) error {
	switch a.Kind {
	case KindBandLock:
		tech, err := ParseLockTech(a.Tech)
		if err != nil {
			return fmt.Errorf("锁频段需要选择制式（LTE / NR）")
		}
		a.Tech = string(tech)
		bands, ok := common.ParseBandList(a.Bands)
		if !ok {
			return fmt.Errorf("锁频段需要填写频段号，如 78 或 1:3:5:8")
		}
		if len(bands) > 32 {
			return fmt.Errorf("频段最多 32 个")
		}
		a.Bands = FormatBandList(bands)
	case KindBandUnlock, KindCellUnlock:
		tech, err := ParseLockTech(a.Tech)
		if err != nil {
			return fmt.Errorf("需要选择制式（LTE / NR）")
		}
		a.Tech = string(tech)
	case KindCellLock:
		tech, err := ParseLockTech(a.Tech)
		if err != nil {
			return fmt.Errorf("锁基站需要选择制式（LTE / NR）")
		}
		a.Tech = string(tech)
		if a.LockType != CellLockARFCN && a.LockType != CellLockARFCNPCI {
			return fmt.Errorf("锁基站需要选择锁定方式（频点+PCI / 仅频点）")
		}
		if !EarfcnValid(tech, a.ARFCN) {
			return fmt.Errorf("频点号 %d 不合法", a.ARFCN)
		}
		if a.LockType == CellLockARFCNPCI && (a.PCID < 0 || a.PCID > 1007) {
			return fmt.Errorf("PCI 应为 0~1007")
		}
	case KindUnlockAll:
		// 无参数
	case KindModePref:
		// 语义档位（全 7 档）或**本机原码**（未取证厂商靠它）；具体能不能写由方言决定
		m := strings.ToUpper(strings.TrimSpace(a.Mode))
		if !modedialect.IsModePrefSemantic(m) && !modedialect.LooksLikeModeCode(m) {
			return fmt.Errorf("组网模式应为 %s 之一，或本机原码（数字/十六进制/自报短码，见锁网页下拉）",
				strings.Join(modedialect.AllModePrefs, " / "))
		}
		a.Mode = m
	case KindAirplaneOn, KindAirplaneOff:
		// 无参数
	default:
		return fmt.Errorf("未知动作类型 %q", a.Kind)
	}
	return nil
}

// ------------------------------------------------------------------ 时间判定 ---

// Slot 槽位标识：把"某天某个 HH:MM"写成一个稳定字符串，用于去重。
func Slot(day time.Time, hhmm string) string {
	return day.Format("2006-01-02") + " " + hhmm
}

// DayOK 今天是否该执行（Days 为空 = 每天）
func (t *Task) DayOK(d time.Time) bool {
	if len(t.Days) == 0 {
		return true
	}
	w := int(d.Weekday()) // 0=Sunday
	for _, x := range t.Days {
		if x == w {
			return true
		}
	}
	return false
}

// Phase 一个时间槽
type Phase struct {
	Name string // PhaseStart / PhaseEnd
	HHMM string
}

// Phases 任务的所有时间槽（start 一定有，end 可能没有）
func (t *Task) Phases() []Phase {
	out := []Phase{{PhaseStart, t.StartTime}}
	if strings.TrimSpace(t.EndTime) != "" && len(t.EndActions) > 0 {
		out = append(out, Phase{PhaseEnd, t.EndTime})
	}
	return out
}

// ActionsOf 取某个槽位的动作列表（按执行顺序）
func ActionsOf(t *Task, phase string) []Action {
	if phase == PhaseEnd {
		return t.EndActions
	}
	return t.StartActions
}

// TimeOfPhase 取该槽位的 "HH:MM"（记录槽位标识用）
func TimeOfPhase(t *Task, phase string) string {
	if phase == PhaseEnd {
		return t.EndTime
	}
	return t.StartTime
}

// DuePhases 返回现在该执行的槽位（已去重、已考虑补做窗口）。
//
// 判定：槽位时间 <= now < 槽位时间 + CatchUp，且今天这个星期匹配，
// 且 slots[phase] != 本槽位（上次执行记的就是它）。
func (t *Task) DuePhases(now time.Time) []string {
	if !t.Enabled {
		return nil
	}
	var out []string
	for _, p := range t.Phases() {
		h, m, err := ParseHHMM(p.HHMM)
		if err != nil {
			continue
		}
		at := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
		if now.Before(at) || !now.Before(at.Add(CatchUp)) {
			continue
		}
		if !t.DayOK(at) {
			continue
		}
		if t.Slots[p.Name] == Slot(at, p.HHMM) {
			continue // 这个槽位已经跑过了
		}
		out = append(out, p.Name)
	}
	return out
}

// NextRuns 计算接下来 n 次执行时间（展示用，两个槽位都算）
func (t *Task) NextRuns(now time.Time, n int) []time.Time {
	if !t.Enabled || n <= 0 {
		return nil
	}
	out := make([]time.Time, 0, n)
	for day := 0; day < 8 && len(out) < n; day++ {
		d := now.AddDate(0, 0, day)
		for _, p := range t.Phases() {
			h, m, err := ParseHHMM(p.HHMM)
			if err != nil {
				continue
			}
			at := time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, now.Location())
			if !at.After(now) || !t.DayOK(at) {
				continue
			}
			out = append(out, at)
			if len(out) >= n {
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// NewID 任务 id（时间戳 + 随机后缀，足够唯一且可读）
func NewID() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("t%d%s", time.Now().Unix(), hex.EncodeToString(b[:]))
}

// WeekdayCN 星期几的中文缩写
func WeekdayCN(w time.Weekday) string {
	return [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}[int(w)%7]
}

// TimesText 把时间列表格式化成 "09-30 08:00 周三"
func TimesText(ts []time.Time) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Format("01-02 15:04")+" "+WeekdayCN(t.Weekday()))
	}
	return out
}

// ------------------------------------------------------------------ 视图 ---

// ActionView 单个动作的展示视图：原始参数 + 可读文案 + 该模组是否支持
// （把 Action 内嵌进来，JSON 里参数字段与提交格式完全一致，前端可直接回传）
type ActionView struct {
	Action
	Text string `json:"text"`
	// Unsupported 空串 = 支持；非空是原因（例如固件不支持锁网）
	Unsupported string `json:"unsupported,omitempty"`
}

// TaskView 给前端的任务视图（含展示用派生字段）
type TaskView struct {
	*Task
	TargetLabel string `json:"target_label"`
	TargetOK    bool   `json:"target_ok"` // 目标模组当前在线且运行中
	// StartActions / EndActions 名称与内嵌的原始字段同名（深度更浅，会覆盖它），
	// 目的是让前端拿到的就是"带文案的动作列表"，且提交时格式一致。
	StartActions []ActionView `json:"start_actions"`
	EndActions   []ActionView `json:"end_actions,omitempty"`
	NextRuns     []string     `json:"next_runs,omitempty"`
	// Unsupported 该侧第一个不支持的动作原因（空串 = 全支持）；列表状态胶囊用它
	StartUnsupported string `json:"start_unsupported,omitempty"`
	EndUnsupported   string `json:"end_unsupported,omitempty"`
}

// ModemOption 目标模组下拉项
type ModemOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	ID    int    `json:"id"`
}

// SchedView 定时器页面的完整数据
type SchedView struct {
	Now       time.Time     `json:"now"`
	Tasks     []TaskView    `json:"tasks"`
	Modems    []ModemOption `json:"modems"`
	Kinds     []KindInfo    `json:"kinds"`
	MaxTasks  int           `json:"max_tasks"`
	Supported bool          `json:"supported"` // 至少有一台模组支持锁网（否则页面顶部提示）
}

// TaskViewOf 组装单个任务的视图。
//
// targetLabel / targetOK 与 support 都由 kernel 提供：前者是"目标模组"的展示信息，
// 后者判断某个动作在当前固件上是否支持（不支持时返回原因，空串表示支持）——
// 这两件事都要看 *Modem 的能力，所以留在 kernel。
func TaskViewOf(t *Task, now time.Time, targetLabel string, targetOK bool, support func(Action) string) TaskView {
	item := TaskView{
		Task:        t,
		TargetLabel: targetLabel,
		TargetOK:    targetOK,
		NextRuns:    TimesText(t.NextRuns(now, NextShow)),
	}
	item.StartActions, item.StartUnsupported = ActionViews(t.StartActions, support)
	item.EndActions, item.EndUnsupported = ActionViews(t.EndActions, support)
	return item
}

// ActionViews 把动作列表转成带文案/支持性的视图；
// 同时返回第一个"不支持"的原因（供列表胶囊与按钮 title 使用）。
func ActionViews(actions []Action, support func(Action) string) ([]ActionView, string) {
	out := make([]ActionView, 0, len(actions))
	first := ""
	for _, a := range actions {
		v := ActionView{Action: a, Text: ActionText(a)}
		if support != nil {
			if why := support(a); why != "" {
				v.Unsupported = why
				if first == "" {
					first = why
				}
			}
		}
		out = append(out, v)
	}
	return out, first
}

// TechOf 把动作里的制式字符串解析成 cell 类型（执行时用）
func TechOf(a Action) (CellTech, error) { return ParseLockTech(a.Tech) }

// ---- from: schedule/store.go ----

// SchedStore 任务存储（内存 + schedule.json 原子落盘）
type SchedStore struct {
	mu      sync.Mutex
	path    string
	tasks   []*Task
	dirty   bool
	savedAt time.Time
	// lastLog 每个任务一条日志节流（失败别刷屏）
	lastLog map[string]time.Time
}

// NewSchedStore 建空存储（路径由 Init 设置）
func NewSchedStore() *SchedStore {
	return &SchedStore{lastLog: map[string]time.Time{}}
}

// Path 定时器文件默认位置（与配置文件同目录；configPath 为空时退回当前目录）
func Path(configPath string) string {
	if p := strings.TrimSpace(configPath); p != "" {
		return filepath.Join(filepath.Dir(p), "schedule.json")
	}
	return "schedule.json"
}

// Init 载入 schedule.json（main 启动时调用一次）
func (s *SchedStore) Init(path string) {
	tasks := s.loadFile(path)

	s.mu.Lock()
	s.path = path
	s.tasks = tasks
	s.mu.Unlock()

	on := 0
	for _, t := range tasks {
		if t.Enabled {
			on++
		}
	}
	logger.Info("schedule store loaded",
		zap.String("path", path), zap.Int("tasks", len(tasks)), zap.Int("enabled", on))
}

func (s *SchedStore) loadFile(path string) []*Task {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil // 首次运行没有该文件，正常
	}
	var f struct {
		Tasks []*Task `json:"tasks"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		logger.Warn("schedule file broken, start from scratch", zap.String("path", path), zap.Error(err))
		return nil
	}
	out := make([]*Task, 0, len(f.Tasks))
	for _, t := range f.Tasks {
		if t == nil || t.ID == "" {
			continue
		}
		if err := ValidateTask(t); err != nil {
			logger.Warn("schedule task skipped (invalid)", zap.String("id", t.ID), zap.Error(err))
			continue
		}
		out = append(out, t)
	}
	return out
}

// saveLocked 原子写盘
func (s *SchedStore) saveLocked() {
	if s.path == "" {
		return
	}
	payload := struct {
		Version int       `json:"version"`
		SavedAt time.Time `json:"saved_at"`
		Tasks   []*Task   `json:"tasks"`
	}{Version: 1, SavedAt: time.Now(), Tasks: s.tasks}

	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		logger.Warn("schedule marshal failed", zap.Error(err))
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		logger.Warn("schedule write failed", zap.String("path", tmp), zap.Error(err))
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		logger.Warn("schedule rename failed", zap.String("path", s.path), zap.Error(err))
		return
	}
	s.dirty = false
	s.savedAt = time.Now()
}

// Flush 强制落盘（进程退出前 / 状态变化后调用）
func (s *SchedStore) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dirty {
		s.saveLocked()
	}
}

// Snapshot 任务快照（加锁拷贝，供 API / 循环使用）
func (s *SchedStore) Snapshot() []*Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		cp := *t
		if t.Days != nil {
			cp.Days = append([]int(nil), t.Days...)
		}
		if t.Slots != nil {
			cp.Slots = make(map[string]string, len(t.Slots))
			for k, v := range t.Slots {
				cp.Slots[k] = v
			}
		}
		out = append(out, &cp)
	}
	return out
}

// Upsert 新增/更新一条任务（也用于启用开关：整体覆盖）。
// 更新时会保留不该被前端覆盖的字段（创建时间、槽位去重记录、上次执行结果）。
func (s *SchedStore) Upsert(t *Task) error {
	if err := ValidateTask(t); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for i, old := range s.tasks {
		if old.ID != t.ID {
			continue
		}
		t.CreatedAt = old.CreatedAt
		t.Slots = old.Slots
		t.Last = old.Last
		t.UpdatedAt = now
		s.tasks[i] = t
		s.dirty = true
		s.saveLocked()
		return nil
	}
	if len(s.tasks) >= MaxTasks {
		return fmt.Errorf("任务数已达上限（%d 条）", MaxTasks)
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	s.tasks = append(s.tasks, t)
	s.dirty = true
	s.saveLocked()
	return nil
}

// Delete 删除任务
func (s *SchedStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.tasks {
		if t.ID != id {
			continue
		}
		s.tasks = append(s.tasks[:i], s.tasks[i+1:]...)
		s.dirty = true
		s.saveLocked()
		return true
	}
	return false
}

// Retarget 改任务的模组键（换 USB 口后迁移用）；顺带落盘
func (s *SchedStore) Retarget(id, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tasks {
		if t.ID == id {
			t.ModemKey = key
			t.UpdatedAt = time.Now()
			s.dirty = true
			s.saveLocked()
			return
		}
	}
}

// Record 记录一次执行结果（并推进槽位去重标记）
func (s *SchedStore) Record(id string, run Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tasks {
		if t.ID != id {
			continue
		}
		if t.Slots == nil {
			t.Slots = map[string]string{}
		}
		if run.Slot != "manual" {
			t.Slots[run.Phase] = run.Slot
		}
		r := run
		t.Last = &r
		s.dirty = true
		if time.Since(s.savedAt) >= SchedSaveEvery {
			s.saveLocked()
		}
		return
	}
}

// LogAllowed 日志节流（同一任务 LogEvery 内只记一条）
func (s *SchedStore) LogAllowed(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if at, ok := s.lastLog[id]; ok && time.Since(at) < LogEvery {
		return false
	}
	s.lastLog[id] = time.Now()
	return true
}
