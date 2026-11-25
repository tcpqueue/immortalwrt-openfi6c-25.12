package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"fmt"
	"modemd/module/cell"
	"modemd/module/common"
	"modemd/module/config"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ---- from: changelog_glue.go ----

// signalChanges 比对一次信号采样，返回值得记录的变化。
// 每个信号量都是独立字段，互不影响；有效性判定统一走 validSignal
// （见 signalhistory.go，与前端 renderSignal / 曲线共用同一套规则）。

func signalChanges(mod *Modem, s SignalInfo) []ChangeEntry {
	if mod == nil {
		return nil
	}
	id := mod.ID
	out := make([]ChangeEntry, 0, 4)
	add := func(e *ChangeEntry) {
		if e != nil {
			out = append(out, *e)
		}
	}
	rsrp, rsrq, sinr, rssi := validSignal(s)
	if rsrp != nil {
		add(ChangeGlobal.ObserveNumeric(id, GroupSig, "rsrp", "信号强度 RSRP",
			*rsrp, " dBm", SigThreshold, RsrpGrade))
	}
	if rsrq != nil {
		add(ChangeGlobal.ObserveNumeric(id, GroupSig, "rsrq", "信号质量 RSRQ",
			*rsrq, " dB", SigThreshold, RsrqGrade))
	}
	if sinr != nil {
		add(ChangeGlobal.ObserveNumeric(id, GroupSig, "sinr", "信号质量 SINR",
			*sinr, " dB", SigThreshold, SinrGrade))
	}
	if rssi != nil {
		add(ChangeGlobal.ObserveNumeric(id, GroupSig, "rssi", "RSSI",
			*rssi, " dBm", RSSIThreshold, nil))
	}
	return out
}

// caSummary 把载波聚合列表压成一行可读文本。
// 例：`PCC n78 · 未聚合` / `PCC n78 + SCC n78 · 2CC（已聚合）`。
// 空列表表示模组不报 CA，返回空串（调用方跳过，不记录）。
func caSummary(cc []CCInfo, active bool) string {
	if len(cc) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cc))
	for _, c := range cc {
		role := strings.ToUpper(c.Role)
		if role == "" {
			role = "CC"
		}
		band := c.BandLabel
		if band == "" {
			band = c.BandText
		}
		if band == "" {
			band = strconv.Itoa(c.EARFCN)
		}
		parts = append(parts, role+" "+band)
	}
	s := strings.Join(parts, " + ")
	if active {
		return s + " · " + strconv.Itoa(len(cc)) + "CC（已聚合）"
	}
	return s + " · 未聚合"
}

// cellSummary 服务小区一行摘要：PCI / 频段 / 频点（有的就带上）
func cellSummary(c *cell.CellInfo) string {
	if c == nil {
		return ""
	}
	parts := make([]string, 0, 4)
	if c.PCID > 0 {
		parts = append(parts, "PCI "+strconv.Itoa(c.PCID))
	}
	if c.Band > 0 {
		parts = append(parts, common.BandLabel(c.Tech, c.Band))
	}
	if c.EARFCN > 0 {
		parts = append(parts, "频点 "+strconv.Itoa(c.EARFCN))
	}
	if c.CellID != "" {
		parts = append(parts, "Cell "+c.CellID)
	}
	return strings.Join(parts, " · ")
}

// networkChanges 比对一次网络状态查询，返回值得记录的变化。
//
// 覆盖用户最关心的那些字段：制式 / 频段 / 频点 / 载波聚合 / PLMN / 运营商 /
// 注册状态 / APN / IPv4 / IPv6 / 组网模式 / 服务小区 / 邻区数量。
func networkChanges(mod *Modem, st *NetworkStatus) []ChangeEntry {
	if mod == nil || st == nil {
		return nil
	}
	id := mod.ID
	out := make([]ChangeEntry, 0, 12)
	add := func(e *ChangeEntry) {
		if e != nil {
			out = append(out, *e)
		}
	}
	observe := func(field, label, value string) {
		add(ChangeGlobal.Observe(id, GroupNet, field, label, value))
	}
	observeIf := func(field, label, value string) {
		if strings.TrimSpace(value) != "" {
			observe(field, label, value)
		}
	}

	observeIf("tech", "网络类型", st.NetTech)
	switch {
	case st.BandLabel != "" && st.BandText != "":
		observe("band", "频段", st.BandLabel+"（"+st.BandText+"）")
	case st.BandLabel != "":
		observe("band", "频段", st.BandLabel)
	default:
		observeIf("band", "频段", st.BandText)
	}
	if st.EARFCN > 0 {
		observe("earfcn", "频点", strconv.Itoa(st.EARFCN))
	}
	if s := caSummary(st.CA, st.CAActive); s != "" {
		observe("ca", "载波聚合", s)
	}
	observeIf("plmn", "PLMN", st.PLMN)
	observeIf("operator", "运营商", st.Operator)
	observeIf("cs_reg", "CS 域", st.CSReg.Text)
	observeIf("ps_reg", "数据域", st.PSReg.Text)
	// APN / IP 用"未上报"归一化：地址消失（掉线）也是一次重要变化
	observe("apn", "APN", NotReported(st.APN))
	observe("ipv4", "IPv4", NotReported(st.IPv4))
	observe("ipv6", "IPv6", NotReported(st.IPv6))
	observeIf("mode_pref", "组网模式", st.ModePref)
	// 服务小区 / 邻区数量来自**扫描缓存**，不在这里记：
	// 缓存为空时会把"未上报/0"当成基线，第一次扫描就冒出一条没有意义的"变化"。
	// 这两项由 cellScanChanges（扫描钩子）负责，见下面的说明。
	return out
}

// cellScanChanges 基站扫描完成后的变化：服务小区 / 邻区数量。
//
// 这两项的内容都来自扫描结果（网络状态里读的是同一份缓存），所以由扫描钩子
// （pushCells）负责记录，网络状态那边不再重复记。
// 载波聚合不在这里：它是实时查询的（AT+QCAINFO），由 networkChanges 负责，
// 两个来源都写同一个字段会造成无意义的前后翻转。
func cellScanChanges(mod *Modem, res *cell.CellScanResult) []ChangeEntry {
	if mod == nil || res == nil {
		return nil
	}
	id := mod.ID
	out := make([]ChangeEntry, 0, 2)
	add := func(e *ChangeEntry) {
		if e != nil {
			out = append(out, *e)
		}
	}
	var serving *cell.CellInfo
	if len(res.Serving) > 0 {
		serving = &res.Serving[0]
	}
	if s := cellSummary(serving); s != "" {
		add(ChangeGlobal.Observe(id, GroupNet, "serving", "服务小区", s))
	}
	add(ChangeGlobal.Observe(id, GroupNet, "neighbour", "邻区数量", strconv.Itoa(res.NeighbourCnt)))
	return out
}

// ====================== 记录 + 推送（供查询点调用） ======================

// noteSignal 记录一次信号观测：
//   - 存入采样历史（供曲线图）
//   - 比对出值得记录的变化并推给前端
//
// 两者用同一次查询的数据，不额外发 AT。
func noteSignal(mod *Modem, s SignalInfo) []ChangeEntry {
	if mod == nil {
		return nil
	}
	Global.Add(mod.ID, sampleFromSignalInfo(s))
	added := signalChanges(mod, s)
	pushChanges(mod, added)
	return added
}

// noteNetwork 记录一次网络状态观测（新增的波动会推给前端）
func noteNetwork(mod *Modem, st *NetworkStatus) []ChangeEntry {
	added := networkChanges(mod, st)
	pushChanges(mod, added)
	return added
}

// noteCells 记录一次基站扫描结果（新增的波动会推给前端）
func noteCells(mod *Modem, res *cell.CellScanResult) []ChangeEntry {
	added := cellScanChanges(mod, res)
	pushChanges(mod, added)
	return added
}

// pushChanges 推新增的波动记录。没有新增时什么都不做（省掉序列化）。
func pushChanges(mod *Modem, entries []ChangeEntry) {
	if mod == nil || len(entries) == 0 {
		return
	}
	id := mod.ID
	broadcast("changes", &id, entries)
	// logger 现在是门面值（见 config.go），永远非 nil；未初始化时内部是 no-op
	{
		for _, e := range entries {
			logger.Debug("change recorded",
				zap.Int("modem", e.ModemID), zap.String("group", e.Group),
				zap.String("field", e.Field), zap.String("from", e.From),
				zap.String("to", e.To), zap.Int("held_s", e.HeldS))
		}
	}
}

// changeLoop 后台连续记录波动（server.change_interval > 0 时启用）。
//
// 有**主动**客户端在线时由推送循环负责记录（同一次查询顺便比对，不额外占串口），
// 所以这里只在“没人在看”的时候跑：既让历史连续，又不重复查询。
//
// ⚠️ 判据是 **CountActive()**：屏幕端（/display/ws，被动连接）一直挂着不算“有人看” ——
// 否则那块屏一插上，这里就永远不采样，缓存会一直是空的（屏幕什么都不显示）。
// 采样完用 pushSignal / pushNetworkState 而不是 note*：这两个会**连带广播**，
// 被动客户端（屏幕）才看得到新值。
func ChangeLoop(mgr *ModemManager, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		if wsHubInst != nil && wsHubInst.CountActive() > 0 {
			continue // 有主动客户端：交给推送循环记，避免重复占串口
		}
		for _, mod := range mgr.listModems() {
			if !mod.isRunningNow() {
				continue
			}
			if _, err := mod.QuerySignal(); err == nil {
				pushSignal(mod) // 采样 + 记录波动 + 广播
			}
			// 网络状态要发多条 AT，使用独立周期避免跟随每轮信号查询。
			if mod.networkStatusDue(time.Now(), time.Duration(config.Cfg.Server.NetworkInterval)*time.Second) {
				if st, err := mod.QueryNetworkStatus(); err == nil {
					pushNetworkState(mod, st) // 同上：记录 + 广播
				}
			}
		}
	}
}

// fmtHeld 把"保持了多久"写成人类可读的短文本（信号时间线用）
func fmtHeld(sec int) string {
	switch {
	case sec <= 0:
		return ""
	case sec < 60:
		return strconv.Itoa(sec) + "s"
	case sec < 3600:
		return strconv.Itoa(sec/60) + "m" + strconv.Itoa(sec%60) + "s"
	default:
		return strconv.Itoa(sec/3600) + "h" + strconv.Itoa((sec%3600)/60) + "m"
	}
}

// fmtChangeLine 一行文本摘要（日志 / 排查用）
func fmtChangeLine(e ChangeEntry) string {
	return fmt.Sprintf("#%d %s %s: %s → %s（保持 %s）",
		e.ModemID, e.Label, e.Group, e.From, e.To, fmtHeld(e.HeldS))
}

// ---- from: history/signalhistory.go ----

// Package history 存信号采样历史（给界面画曲线用）。
//
// 与「波动记录」（history.go）的区别：波动记录只存**变化**，适合当时间线读；
// 这里存**连续采样**，适合看趋势。两者共用同一个挂钩点（kernel 的 noteSignal），
// 一次查询喂两个用途，**不额外发 AT**。
//
// 本包是纯存储：只知道 modemID 与采样值，不认识 `*Modem`（所以能单独测试）。
// 把一次信号查询结果转成采样点、以及"哪些值算有效"的判定，都在 kernel 里
// （见 glue.go，那里要用 SignalInfo）。
// 存储上的关键技巧：**值没变时只把最后一个点的时间往后推，不新增点**。
// 这样稳定信号（长时间不动）不会把缓冲塞满，曲线上依然是一条一直画到"现在"的平线
// （若改成"没变就不记"，曲线会在最后一次变化处断掉，看起来像数据过期）。
// 于是 SampleMax 条 = 最多 1800 段"变化"，1 秒采样下能覆盖很久。
// Sample 一次信号采样。空值用 nil 表示"该次未上报"，
// 让前端把曲线断开，而不是把"未上报"当成 0 画出来。

type Sample struct {
	ModemID int       `json:"modem_id,omitempty"`
	At      time.Time `json:"at"`
	RSRP    *int      `json:"rsrp,omitempty"` // dBm
	RSRQ    *int      `json:"rsrq,omitempty"` // dB
	SINR    *int      `json:"sinr,omitempty"` // dB
	RSSI    *int      `json:"rssi,omitempty"` // dBm（由 CSQ 换算）
}

// SampleMax 每个模组保留的采样段数上限（≈ 1800 次变化，足够画几小时）
const SampleMax = 1800

// HistoryStore 采样历史（按 modemID 分组）
type HistoryStore struct {
	mu  sync.Mutex
	per map[int][]Sample // modemID → 时间升序
}

// NewHistoryStore 建一个空的历史
func NewHistoryStore() *HistoryStore {
	return &HistoryStore{per: map[int][]Sample{}}
}

// Global 全局采样历史（进程内唯一；重启即清空）
var Global = NewHistoryStore()

// SameAs 判断两次采样是否有相同的值（时间不算）
func (s Sample) SameAs(o Sample) bool {
	eq := func(a, b *int) bool {
		if a == nil || b == nil {
			return a == nil && b == nil
		}
		return *a == *b
	}
	return eq(s.RSRP, o.RSRP) && eq(s.RSRQ, o.RSRQ) && eq(s.SINR, o.SINR) && eq(s.RSSI, o.RSSI)
}

// Add 记录一次采样。与上一点的值相同则只把它的时间推到 now（见包注释）。
func (h *HistoryStore) Add(modemID int, s Sample) {
	s.ModemID = modemID
	if s.At.IsZero() {
		s.At = time.Now()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.per[modemID]
	if n := len(list); n > 0 {
		last := list[n-1]
		if last.SameAs(s) {
			if !s.At.Before(last.At) {
				list[n-1].At = s.At // 平线一直画到"现在"
			}
			return
		}
	}
	list = append(list, s)
	if len(list) > SampleMax {
		// 丢掉最老的（重新切片，避免底层数组一直增长）
		list = append([]Sample(nil), list[len(list)-SampleMax:]...)
	}
	h.per[modemID] = list
}

// Snapshot 取某个模组的采样点（时间升序）。
// modemID < 0 表示所有模组（按时间归并）；since 为零值表示不限起始时间；limit <= 0 表示不限条数。
func (h *HistoryStore) Snapshot(modemID int, since time.Time, limit int) []Sample {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Sample
	if modemID >= 0 {
		for _, s := range h.per[modemID] {
			if !since.IsZero() && s.At.Before(since) {
				continue
			}
			out = append(out, s)
		}
	} else {
		for _, list := range h.per {
			for _, s := range list {
				if !since.IsZero() && s.At.Before(since) {
					continue
				}
				out = append(out, s)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	}
	// 只保留最新的 limit 条（时间窗可能很长，前端一次画不了那么多点）
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// CountFor 某模组的采样段数（modemID < 0 表示全部）
func (h *HistoryStore) CountFor(modemID int) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if modemID >= 0 {
		return len(h.per[modemID])
	}
	n := 0
	for _, list := range h.per {
		n += len(list)
	}
	return n
}

// Clear 清空采样历史（modemID < 0 表示全部），返回清掉的段数
func (h *HistoryStore) Clear(modemID int) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if modemID < 0 {
		n := 0
		for _, list := range h.per {
			n += len(list)
		}
		h.per = map[int][]Sample{}
		return n
	}
	n := len(h.per[modemID])
	delete(h.per, modemID)
	return n
}

// ---- from: changelog/changelog.go ----

// Package changelog 是「波动记录」：把每次查询的结果与上一次比对，
// 把变化记成一条时间线（时间 · 项目 · 之前 → 之后 · 变化量 · 旧值保持多久）。
//
// 分工：本包只管**存储 + 比对 + 阈值/节流 + 品级文案**（纯逻辑，不认识 Modem）；
// “从服务小区/网络状态里取出哪些字段”那部分要用引擎的类型与缓存，
// 所以留在 history.go（那边只调用本包的 ObserveXxx / Snapshot / …）。
//
// 网络与信号都是"状态量"：平时稳定，只有变化才携带信息 ——
// 换频段、切小区、CA 聚合/去聚合、换运营商、信号跨等级……
// 这里把这些事件记成一条时间线，每条回答"之前是什么 → 之后是什么、什么时候变的、旧值维持了多久"。
//
// 三条设计原则：
//
//  1. **不额外发 AT**：记录点挂在已有的查询上（推送循环 / HTTP 接口 / 基站扫描），
//     查询拿到数据顺手比对一次，零额外串口开销。代价是"没人在看时就不记"，
//     要 7×24 连续记录请开 server.change_interval（见 changeLoop）。
//  2. **阈值**：数值型信号量（RSRP/RSRQ/SINR）只有变化超过阈值、或跨了等级才记一条，
//     否则 -100 → -101 → -100 这种抖动会把时间线刷满，反而看不出真正的事件。
//  3. **只在内存里环形缓冲**（GlobalMax 条，不落盘）：写文件会引入 I/O 抖动，
//     而且这些数据本来就是"最近发生了什么"，重启后清空即可。
// ChangeEntry 一条波动记录

type ChangeEntry struct {
	At      time.Time `json:"at"` // 变化发生时间
	ModemID int       `json:"modem_id"`
	Group   string    `json:"group"`           // 网络 / 信号
	Field   string    `json:"field"`           // 机器可读字段名（前端筛选用）
	Label   string    `json:"label"`           // 展示名（频点 / 频段 / 信号强度 RSRP…）
	From    string    `json:"from"`            // 变化前的值
	To      string    `json:"to"`              // 变化后的值
	Delta   string    `json:"delta,omitempty"` // 数值型的变化量（+4 dBm）
	Up      bool      `json:"up,omitempty"`    // 数值是否朝"更好"的方向变（正的 dB 就是变好）
	HeldS   int       `json:"held_s"`          // 旧值维持了多久（距上一条记录；首条以开始观测为基准）
}

// ChangeState 某字段的当前值（对应 GET /api/changes 的 current 列表）
type ChangeState struct {
	Group string    `json:"group"`
	Field string    `json:"field"`
	Label string    `json:"label"`
	Value string    `json:"value"`
	Since time.Time `json:"since"`  // 该值是从什么时候开始的
	HeldS int       `json:"held_s"` // 已保持了多久（秒）
}

// fieldState 内部状态：除了展示用的值，还留着数值与等级用于阈值判断
// fieldState 内部状态。要区分两组值：
//
//	value/num/grade/since —— 最近一次**采样**值（前端"当前值"面板用它）
//	recValue/recNum/recGrade/recAt —— 最近一条**记录**的内容
//
// 为什么要分开：真实信号几秒内就能来回抖 5 dB（实测 RM502Q-GL 在 -102/-97/-99 之间跳），
// 若拿"上一条记录"和"当前采样"直接比，1 秒采样就会一秒记一条。时间线要的是
// "从上次记录的值变成了现在的值"，所以阈值与 From 都以 rec* 为基准，
// 再叠加 chgMinInterval 节流；被压掉的采样只更新 value，不影响时间线连续性。
type fieldState struct {
	modemID int
	group   string
	field   string
	label   string
	// 当前采样值
	value  string
	num    int
	grade  string
	since  time.Time // 当前采样值从什么时候开始
	baseAt time.Time // 首次观测时间（第一条记录的"保持时长"以它为基准）
	// 上一条记录
	recValue string
	recNum   int
	recGrade string
	recAt    time.Time // 零值 = 还没记过（此时不做节流）
}

// GlobalMax 环形缓冲上限。每条记录约 200 字节，500 条 ≈ 100 KB，足够回顾最近的情况。
const GlobalMax = 500

// MinInterval 同一字段两条记录之间的最小间隔。
// 信号在几秒内抖 5 dB 是常态（实测），没有这个节流会"一秒一条"把时间线刷满；
// 不同字段各自计时，所以换频段/换小区这类事件不会被拖慢。
const MinInterval = 30 * time.Second

// Tracker 波动记录器（并发安全）
type Tracker struct {
	mu      sync.Mutex
	entries []ChangeEntry          // 时间升序，最新在末尾
	states  map[string]*fieldState // key = modem|group|field
}

func NewTracker() *Tracker {
	return &Tracker{states: map[string]*fieldState{}}
}

// Unthrottle 清掉某字段的节流时间戳（测试辅助：绕过 MinInterval 连续制造变化）。
func (t *Tracker) Unthrottle(modemID int, group, field string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if st := t.states[changeKey(modemID, group, field)]; st != nil {
		st.recAt = time.Time{}
	}
}

// AgeField 把某字段的时间戳往前拨（测试辅助：模拟时间流逝）。
// base 作用于首次观测时间，rec 作用于上一条记录时间；传 0 表示不动。
func (t *Tracker) AgeField(modemID int, group, field string, base, rec time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.states[changeKey(modemID, group, field)]
	if st == nil {
		return
	}
	if base != 0 {
		st.baseAt = st.baseAt.Add(-base)
	}
	if rec != 0 {
		st.recAt = st.recAt.Add(-rec)
	}
}

// ChangeGlobal 全局记录器（进程内唯一；重启即清空）
var ChangeGlobal = NewTracker()

func changeKey(modemID int, group, field string) string {
	return strconv.Itoa(modemID) + "|" + group + "|" + field
}

// appendLocked 追加一条记录并维持环形缓冲（调用方须持锁）
func (c *Tracker) appendLocked(e ChangeEntry) {
	c.entries = append(c.entries, e)
	if len(c.entries) > GlobalMax {
		// 丢掉最老的若干条（重新切片，别让底层数组一直增长）
		c.entries = append([]ChangeEntry(nil), c.entries[len(c.entries)-GlobalMax:]...)
	}
}

// observe 观测一个非数值字段；与上一条记录不同时记一条。
//
// 首次观测只建立基线（没有"之前"，不算变化），空值一律不参与
// —— 空值通常代表"模组没上报/查询失败"，不是真的状态变化。
// 需要把"空"当成有意义状态（如 IPv6 消失）时，调用方先把它格式化成
// "未上报" 之类的文本再传进来。
func (c *Tracker) Observe(modemID int, group, field, label, value string) *ChangeEntry {
	if value == "" {
		return nil
	}
	return c.ObserveValue(modemID, group, field, label, value, 0, "", "", 0)
}

// observeNumeric 观测一个数值型字段（信号量）。
// threshold 是"值得记一条"的最小变化量；grade 为 nil 时只用阈值判断。
func (c *Tracker) ObserveNumeric(modemID int, group, field, label string, num int, unit string,
	threshold int, grade func(int) string) *ChangeEntry {

	value := strconv.Itoa(num) + unit
	g := ""
	if grade != nil {
		g = grade(num)
		if g != "" {
			value += "（" + g + "）"
		}
	}
	return c.ObserveValue(modemID, group, field, label, value, num, unit, g, threshold)
}

// observeValue 记录器的统一入口：先更新"当前采样值"，再判断是否值得记一条。
//
// 两道闸门防止刷屏：
//  1. threshold：数值字段要跟**上一条记录**差够多才记；跨等级一定要记
//     （-101 偏弱 → -100 一般 只差 1 dB，但意义完全不同）。
//  2. chgMinInterval：同字段最快每 30 秒记一条。
func (c *Tracker) ObserveValue(modemID int, group, field, label, value string,
	num int, unit, grade string, threshold int) *ChangeEntry {

	key := changeKey(modemID, group, field)
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	st := c.states[key]
	if st == nil {
		// 首次观测：建立基线（它既是"当前值"，也是"上一条记录"的基准）
		c.states[key] = &fieldState{
			modemID: modemID, group: group, field: field, label: label,
			value: value, num: num, grade: grade, since: now, baseAt: now,
			recValue: value, recNum: num, recGrade: grade,
		}
		return nil
	}
	if st.value == value && st.grade == grade {
		return nil // 采样值没变
	}
	// 采样值变了：先更新"当前值"（即使这轮不记录，前端也该看到最新值）
	st.value, st.num, st.grade, st.since = value, num, grade, now

	if st.recValue == value {
		return nil // 又抖回上一条记录的值，不算新事件
	}
	gradeChanged := st.recGrade != grade
	diff := num - st.recNum
	if diff < 0 {
		diff = -diff
	}
	if !gradeChanged && threshold > 0 && diff < threshold {
		return nil // 阈值内的小抖动
	}
	if !st.recAt.IsZero() && now.Sub(st.recAt) < MinInterval {
		return nil // 节流：同一字段记太密（时间线以已记录的值为基准，不受影响）
	}
	heldFrom := st.recAt
	if heldFrom.IsZero() {
		heldFrom = st.baseAt // 第一条记录：以开始观测的时间为基准
	}
	// 只有数值字段（带单位）才给出变化量与方向：
	// 文本字段（频段/网络类型/APN…）的 num 恒为 0，直接相减会得到"▼ 0"这种假变化量。
	delta, up := "", false
	if unit != "" {
		sign := ""
		if num > st.recNum {
			sign = "+"
		}
		delta = sign + strconv.Itoa(num-st.recNum) + unit
		up = num > st.recNum // 信号量（dB/dBm）都是越大越好
	}
	e := ChangeEntry{
		At: now, ModemID: modemID, Group: group, Field: field, Label: label,
		From: st.recValue, To: value,
		Delta: delta,
		Up:    up,
		HeldS: int(now.Sub(heldFrom).Seconds()),
	}
	st.recValue, st.recNum, st.recGrade, st.recAt = value, num, grade, now
	c.appendLocked(e)
	return &e
}

// snapshot 取记录（最新在前）。modemID < 0 表示不限模组；limit <= 0 表示不限条数。
func (c *Tracker) Snapshot(modemID, limit int) []ChangeEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ChangeEntry, 0, GlobalMax)
	for i := len(c.entries) - 1; i >= 0; i-- {
		if modemID >= 0 && c.entries[i].ModemID != modemID {
			continue
		}
		out = append(out, c.entries[i])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// current 取各字段的当前值（按分组 + 展示名排序，便于前端成组展示）
func (c *Tracker) Current(modemID int) []ChangeState {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ChangeState, 0, len(c.states))
	for _, st := range c.states {
		if modemID >= 0 && st.modemID != modemID {
			continue
		}
		out = append(out, ChangeState{
			Group: st.group, Field: st.field, Label: st.label, Value: st.value,
			Since: st.since, HeldS: int(now.Sub(st.since).Seconds()),
		})
	}
	// 固定顺序：先组后字段名，前端表格不会随 map 迭代顺序跳来跳去
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Label < out[j].Label
	})
	return out
}

// count 记录总条数
func (c *Tracker) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// countFor 统计某个模组的记录条数（modemID < 0 表示全部）
func (c *Tracker) CountFor(modemID int) int {
	if modemID < 0 {
		return c.Count()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.entries {
		if e.ModemID == modemID {
			n++
		}
	}
	return n
}

// clear 清空记录（modemID < 0 表示全部模组），返回清掉的条数。
// 同时清掉基线，这样清空后第一轮采样只是重建基线、不会立刻冒出"变化"。
func (c *Tracker) Clear(modemID int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := c.entries[:0]
	removed := 0
	for _, e := range c.entries {
		if modemID >= 0 && e.ModemID != modemID {
			kept = append(kept, e)
			continue
		}
		removed++
	}
	c.entries = kept
	prefix := strconv.Itoa(modemID) + "|"
	for k := range c.states {
		if modemID >= 0 && !strings.HasPrefix(k, prefix) {
			continue
		}
		delete(c.states, k)
	}
	return removed
}

// ====================== 观测：把一次查询的结果喂给记录器 ======================

// 分组名（前端按这个分栏）
const (
	GroupNet = "网络"
	GroupSig = "信号"
)

// 数值型信号量的记录阈值：差了这么多才值得记一条。
// RSRP/RSRQ/SINR 用 3 dB，RSSI（CSQ 换算，步进 2 dB）用 5 dB。
const (
	SigThreshold  = 3
	RSSIThreshold = 5
)

// 物理范围校验（与前端 renderSignal 的校验一致）：
// 模组偶尔回无效哨兵值（实测见过 SINR=-3276），超出范围一律当"未上报"。
const (
	RSRPLo, RSRPHi = -156, -44
	RSRQLo, RSRQHi = -43, 20
	SINRLo, SINRHi = -23, 40
)

func IntInRange(v, lo, hi int) bool { return v >= lo && v <= hi }

// rsrpGrade RSRP → 等级（阈值与前端 rsrpLevel 一致）
func RsrpGrade(v int) string {
	switch {
	case v >= -80:
		return "极好"
	case v >= -90:
		return "良好"
	case v >= -100:
		return "一般"
	case v >= -110:
		return "偏弱"
	case v >= -120:
		return "很弱"
	}
	return "无信号"
}

// sinrGrade SINR → 等级（与前端 sinrLevel 一致）
func SinrGrade(v int) string {
	switch {
	case v >= 20:
		return "优秀"
	case v >= 13:
		return "良好"
	case v >= 7:
		return "一般"
	case v >= 0:
		return "偏差"
	case v >= -3:
		return "很差"
	}
	return "无数据"
}

// rsrqGrade RSRQ → 等级（与前端 rsrqLevel 一致）。
// 注意 0 在前端是"无数据"拉，坐实了 RSRQ=0 就是未上报的哨兵值。
func RsrqGrade(v int) string {
	switch {
	case v == 0:
		return "无数据"
	case v >= -10:
		return "优秀"
	case v >= -15:
		return "良好"
	case v >= -20:
		return "一般"
	case v >= -25:
		return "偏差"
	}
	return "很差"
}

// chgNotReported 把空值归一化成"未上报"。
// 对 IPv6 / APN 这类字段，"由有到无"本身就是一次有意义的变化，
// 不能像其它空值那样直接丢弃。
func NotReported(v string) string {
	if strings.TrimSpace(v) == "" {
		return "未上报"
	}
	return v
}
