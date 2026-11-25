package kernel

// ====================== IMEI 原始值备份（按模块记录，可一键恢复） ======================
//
// 为什么要存：改 IMEI 是"写 NV"，改完模组自己**不会**告诉你原值是多少 —— 想反悔只能靠事先记下来。
// 所以每次下发写入指令**之前**先把当时读到的值存一份（第一次改写时那个值就是原始 IMEI），
// 之后界面上「恢复原始 IMEI」一键写回去。
//
// 按模块分开记（多台模组各有各的原始值，不能串台）：键用 Key(ATPort, ID)（`port:COM3` / `id:0`），
// 与流量配额同一套规则。
//
// ⚠️ 刻意**不**按 IMEI 做“换 USB 口后自动迁移”（quota 那边有，这里不能有）：
// 改 IMEI 的场景下“两台模组 IMEI 相同”很常见（克隆机、都没改过），
// 一迁移就可能把 B 台的原始值当成 A 台的 —— 用户一点“恢复”就把别人的 IMEI 写上去了。
// 换口后看不到旧记录可以接受（重新记一份就行），串台不行。
//
// 落盘 imei.json（与 quota.json / schedule.json 同目录、同一套原子写）；属于"运行时状态"，
// 不是 config.toml 的一部分。

import (
	"encoding/json"
	"modemd/module/config"
	"modemd/module/imeidialect"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// IMEIBackup 一台模组的 IMEI 备份记录
type IMEIBackup struct {
	// Key 记录键（`port:COM3` / `id:0`）—— 只为了人工看文件时知道这条是谁的
	Key string `json:"key,omitempty"`
	// Original 第一次改写**之前**读到的值，也就是这台模组的原始 IMEI。
	// 只在还没有值时写入，之后任何改写都不会覆盖它（否则"原始值"就丢了）。
	Original string `json:"original"`
	// Current 最近一次回读到的值（用户可能在「AT 指令」页手改过，读盘时会对齐一次）
	Current string `json:"current,omitempty"`
	// Vendor/Model 机型信息：多台模组时便于人工辨认这条记录属于谁
	Vendor string `json:"vendor,omitempty"`
	Model  string `json:"model,omitempty"`
	// SavedAt 原始值保存时间；Writes 通过本程序改写了几次
	SavedAt     time.Time `json:"saved_at"`
	Writes      int       `json:"writes,omitempty"`
	LastWriteAt time.Time `json:"last_write_at,omitempty"`
	// RestoredAt 最近一次「恢复原始 IMEI」的时间
	RestoredAt time.Time `json:"restored_at,omitempty"`
}

// Modified 当前值是否已经和原始值不一样（界面据此显示"已改写 / 可恢复"）
func (b *IMEIBackup) Modified() bool {
	return b != nil && imeidialect.ValidIMEI(b.Original) && strings.TrimSpace(b.Current) != b.Original
}

// ====================== 存储 ======================

// IMEIBackupStore 备份落盘（内存 + imei.json 原子写）
type IMEIBackupStore struct {
	mu      sync.Mutex
	path    string
	data    map[string]*IMEIBackup
	dirty   bool
	savedAt time.Time
}

// NewIMEIBackupStore 建一个空存储（路径由 Init 设置）
func NewIMEIBackupStore() *IMEIBackupStore {
	return &IMEIBackupStore{data: map[string]*IMEIBackup{}}
}

var imeiBackupInst = NewIMEIBackupStore()

// IMEIBackupPath 备份文件默认位置（与配置文件同目录；configPath 为空时退回当前目录）
func IMEIBackupPath(configPath string) string {
	if p := strings.TrimSpace(configPath); p != "" {
		return filepath.Join(filepath.Dir(p), "imei.json")
	}
	return "imei.json"
}

// InitIMEIBackup 载入 imei.json（main 启动时调用一次）
func InitIMEIBackup() {
	imeiBackupInst.Init(IMEIBackupPath(config.ConfigPath))
}

// FlushIMEIBackup 强制落盘（进程退出前调用）
func FlushIMEIBackup() { imeiBackupInst.Flush() }

// Init 载入文件（main 启动时调用一次）
func (s *IMEIBackupStore) Init(path string) {
	data := loadIMEIBackup(path)

	s.mu.Lock()
	s.path, s.data = path, data
	s.mu.Unlock()

	logger.Info("imei backup loaded", zap.String("path", path), zap.Int("records", len(data)))
}

func loadIMEIBackup(path string) map[string]*IMEIBackup {
	out := map[string]*IMEIBackup{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out // 首次运行没有这个文件，正常
	}
	var f struct {
		Modems map[string]*IMEIBackup `json:"modems"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		logger.Warn("imei backup file broken, start from scratch", zap.String("path", path), zap.Error(err))
		return out
	}
	for k, v := range f.Modems {
		if v != nil {
			out[k] = v
		}
	}
	return out
}

// saveLocked 落盘（原子写：先写临时文件再 rename，避免断电写坏）
func (s *IMEIBackupStore) saveLocked() {
	if s.path == "" {
		return
	}
	// 空记录（还没保存过原始值）不写盘：那只是 entryLocked 建的占位
	keep := map[string]*IMEIBackup{}
	for k, v := range s.data {
		if v != nil && imeidialect.ValidIMEI(v.Original) {
			keep[k] = v
		}
	}
	payload := struct {
		Version int                    `json:"version"`
		SavedAt time.Time              `json:"saved_at"`
		Modems  map[string]*IMEIBackup `json:"modems"`
	}{Version: 1, SavedAt: time.Now(), Modems: keep}

	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		logger.Warn("imei backup marshal failed", zap.Error(err))
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		logger.Warn("imei backup write failed", zap.String("path", tmp), zap.Error(err))
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		logger.Warn("imei backup rename failed", zap.String("path", s.path), zap.Error(err))
		return
	}
	s.dirty = false
	s.savedAt = time.Now()
}

// Flush 落盘（状态变化后 / 进程退出前调用）
func (s *IMEIBackupStore) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dirty {
		s.saveLocked()
	}
}

// entryLocked 取（或创建）一台模组的记录。create=false 时只查不建（读接口不该往表里塞占位）。
func (s *IMEIBackupStore) entryLocked(mod *Modem, create bool) (string, *IMEIBackup) {
	key := Key(mod.ATPort, mod.ID)
	if b, ok := s.data[key]; ok {
		return key, b
	}
	if !create {
		return key, nil
	}
	b := &IMEIBackup{Key: key}
	s.data[key] = b
	return key, b
}

// record 记一次改写：before = 下发前读到的值，after = 回读到的值。
//
// Original 只在还没有值时写入（首次改写那一刻模组还是原始 IMEI）；
// 之后无论怎么改都不会再动它 —— 这是"能恢复回去"的唯一依据。
func (s *IMEIBackupStore) record(mod *Modem, before, after string) {
	if mod == nil {
		return
	}
	before, after = strings.TrimSpace(before), strings.TrimSpace(after)

	s.mu.Lock()
	defer s.mu.Unlock()

	key, b := s.entryLocked(mod, true)
	if b.Key == "" {
		b.Key = key
	}
	if !imeidialect.ValidIMEI(b.Original) && imeidialect.ValidIMEI(before) {
		b.Original, b.SavedAt = before, time.Now()
		b.Vendor, b.Model = mod.VendorName, mod.ModelName
		logger.Info("imei original saved",
			zap.Int("modem", mod.ID), zap.String("key", key), zap.String("original", before))
	}
	if after != "" {
		b.Current = after
	}
	b.Writes++
	b.LastWriteAt = time.Now()
	s.dirty = true
	s.saveLocked()
}

// rememberBefore 在下发写入指令**之前**先把"现在是什么"存下来。
//
// ⚠️ 为什么必须在下发之前（2026-10-05 真机踩到真实数据丢失）：
// 展锐 UMS9620（中兴 F50）写完 IMEI 后，`AT+CGSN` 会先回一阵子 `+CME ERROR: 22`
// （模组在重写 NV）⇒ **回读必然失败**。而调用方以前只在"回读成功之后"才 record()，
// 于是这一笔永远记不下 ⇒ 原始 IMEI 永久丢失（真机：值已从 …601 变成 …602，
// 但 imei.json 不存在，界面上连「恢复原始 IMEI」按钮都不出现）。
// 本文件的文件头注释写的本来就是"下发写入指令之前先把当时读到的值存一份"，
// 这里把实现补齐。
//
// 只做"把 Original 记下来"这一件事：不动 Writes、不动 LastWriteAt
// （那些等回读成功后的 record() 去记）—— 所以同一次改写里先调它、再调 record() 是安全的。
func (s *IMEIBackupStore) rememberBefore(mod *Modem, before string) {
	if mod == nil {
		return
	}
	before = strings.TrimSpace(before)
	if !imeidialect.ValidIMEI(before) {
		return // 读不到原值就别把垃圾写进记录
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key, b := s.entryLocked(mod, true)
	if b.Key == "" {
		b.Key = key
	}
	if imeidialect.ValidIMEI(b.Original) {
		return // 已记过："原始值"只有一个，绝不覆盖
	}
	b.Original, b.SavedAt = before, time.Now()
	b.Vendor, b.Model = mod.VendorName, mod.ModelName
	if !imeidialect.ValidIMEI(b.Current) {
		b.Current = before // 还不知道新值，先按旧值记；回读成功后会更新
	}
	s.dirty = true
	s.saveLocked()
	logger.Info("imei original saved before write",
		zap.Int("modem", mod.ID), zap.String("key", key), zap.String("original", before))
}

// markRestored 记一次"恢复原始值"
func (s *IMEIBackupStore) markRestored(mod *Modem, after string) {
	if mod == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, b := s.entryLocked(mod, true)
	if b.Key == "" {
		b.Key = key
	}
	if v := strings.TrimSpace(after); v != "" {
		b.Current = v
	}
	b.RestoredAt = time.Now()
	s.dirty = true
	s.saveLocked()
}

// get 读这台模组的备份（没有备份时 ok=false）。
//
// 只读，**不**拿 mod.IMEI 去回写 Current —— 那个字段是查询缓存（可能陈旧几十秒），
// 拿它盖掉记录反而会把真实历史弄脏；界面判断“能不能恢复”用的是自己刚读到的实时值。
func (s *IMEIBackupStore) get(mod *Modem) (IMEIBackup, bool) {
	if mod == nil {
		return IMEIBackup{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, b := s.entryLocked(mod, false)
	if b == nil {
		return IMEIBackup{}, false
	}
	if b.Key == "" {
		b.Key = key
	}
	if !imeidialect.ValidIMEI(b.Original) {
		return IMEIBackup{}, false
	}
	return *b, true
}

// ====================== 模块内的便捷入口 ======================

// imeiBackupRecord 记一次改写（before = 下发前读到的值）
func imeiBackupRecord(mod *Modem, before, after string) { imeiBackupInst.record(mod, before, after) }

// imeiBackupRememberBefore 下发写入指令**之前**先把原始值存下来（见 rememberBefore）。
func imeiBackupRememberBefore(mod *Modem, before string) { imeiBackupInst.rememberBefore(mod, before) }

// imeiBackupMarkRestored 记一次恢复
func imeiBackupMarkRestored(mod *Modem, after string) { imeiBackupInst.markRestored(mod, after) }

// imeiBackupFor 读这台模组的备份（没有则 ok=false）
func imeiBackupFor(mod *Modem) (IMEIBackup, bool) { return imeiBackupInst.get(mod) }
