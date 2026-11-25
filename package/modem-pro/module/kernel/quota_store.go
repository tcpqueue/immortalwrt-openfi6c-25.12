package kernel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ====================== 存储 ======================

// Key 记录的键：**以端口为主**。
//
// ⚠️ 为什么不用 IMEI 当主键：实测本机两块 RM502Q-GL（COM14 / COM16）回报的 IMEI
// 一模一样（克隆 IMEI 或同一模组的两个 AT 口），按 IMEI 存会让两个模组**共用一份配额**——
// 给模组 0 设上限，模组 1 跟着被断网。配额始终绑定端口，换端口后使用该端口自己的记录。
func Key(atPort string, modemID int) string {
	if p := strings.TrimSpace(atPort); p != "" {
		return "port:" + p
	}
	return fmt.Sprintf("id:%d", modemID)
}

// QuotaStore 配额状态与落盘
type QuotaStore struct {
	mu       sync.Mutex
	path     string
	data     map[string]*State
	dirty    bool
	savedAt  time.Time
	lastPush map[int]time.Time
	lastTry  map[int]time.Time // 上一次尝试断网的时间（节流用）
	lastChk  map[int]time.Time // 上一次复查“是不是又被拨上了”的时间
}

// NewQuotaStore 建一个空存储（路径由 Init 设置）
func NewQuotaStore() *QuotaStore {
	return &QuotaStore{
		data:     map[string]*State{},
		lastPush: map[int]time.Time{},
		lastTry:  map[int]time.Time{},
		lastChk:  map[int]time.Time{},
	}
}

// Init 载入配额文件（main 启动时调用一次）。文件放在配置文件同目录，
// 与日志/PID 一样属于"运行时状态"，不是 config.toml 的一部分（页面能改，且含用量）。
func (q *QuotaStore) Init(path string) {
	st := load(path)

	q.mu.Lock()
	q.path = path
	q.data = st
	q.mu.Unlock()

	enabled := 0
	for _, v := range st {
		if v.Enabled {
			enabled++
		}
	}
	logger.Info("quota store loaded",
		zap.String("path", path), zap.Int("records", len(st)), zap.Int("enabled", enabled))
}

func load(path string) map[string]*State {
	out := map[string]*State{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out // 首次运行没有这个文件，正常
	}
	var f struct {
		Modems map[string]*State `json:"modems"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		logger.Warn("quota file broken, start from scratch", zap.String("path", path), zap.Error(err))
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
func (q *QuotaStore) saveLocked() {
	if q.path == "" {
		return
	}
	payload := struct {
		Version int               `json:"version"`
		SavedAt time.Time         `json:"saved_at"`
		Modems  map[string]*State `json:"modems"`
	}{Version: 1, SavedAt: time.Now(), Modems: q.data}

	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		logger.Warn("quota marshal failed", zap.Error(err))
		return
	}
	tmp := q.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		logger.Warn("quota write failed", zap.String("path", tmp), zap.Error(err))
		return
	}
	if err := os.Rename(tmp, q.path); err != nil {
		logger.Warn("quota rename failed", zap.String("path", q.path), zap.Error(err))
		return
	}
	q.dirty = false
	q.savedAt = time.Now()
}

// stateLocked 按端口取（或创建）状态记录，IMEI 只作记录。
func (q *QuotaStore) stateLocked(key, imei string) *State {
	imei = strings.TrimSpace(imei)
	if st, ok := q.data[key]; ok {
		if imei != "" && st.IMEI != imei {
			st.IMEI = imei
			q.dirty = true
		}
		return st
	}
	st := &State{QuotaConfig: QuotaConfig{CountUp: true, CountDown: true, Action: ActionBlock}, IMEI: imei}
	q.data[key] = st
	return st
}

// Get 只读快照
func (q *QuotaStore) Get(key, imei string) State {
	q.mu.Lock()
	defer q.mu.Unlock()
	return *q.stateLocked(key, imei)
}

// Update 在锁内改状态（改完立即落盘）
func (q *QuotaStore) Update(key, imei string, fn func(st *State)) State {
	q.mu.Lock()
	defer q.mu.Unlock()
	st := q.stateLocked(key, imei)
	fn(st)
	q.dirty = true
	q.saveLocked()
	return *st
}

// AddBytes 累计一次采样增量（由 data.go 的 applyTrafficSample 调用 ——
// 全进程只有那一处在算"计数器差分"，所以不会重复计数）
func (q *QuotaStore) AddBytes(key, imei string, up, down uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if up == 0 && down == 0 {
		return
	}
	st := q.stateLocked(key, imei)
	st.UsedUp += int64(up)
	st.UsedDown += int64(down)
	q.dirty = true
	// 落盘节流：用量每秒都在变，没必要每次都写
	if time.Since(q.savedAt) >= SaveEvery {
		q.saveLocked()
	}
}

// Flush 强制落盘（状态变化后 / 进程退出前调用）
func (q *QuotaStore) Flush() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.dirty {
		q.saveLocked()
	}
}

// PushAllowed 推送节流：同一个模组最快每 every 推一次（force 时由调用方跳过本判断）
func (q *QuotaStore) PushAllowed(modemID int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if last, ok := q.lastPush[modemID]; ok && time.Since(last) < PushEvery {
		return false
	}
	q.lastPush[modemID] = time.Now()
	return true
}

// TryAllowed 断网尝试的节流：失败后别秒级重试（例如某些 cid 模组不接受），
// 但也不能不重试（模组/主板会自动重新拨号）。
func (q *QuotaStore) TryAllowed(modemID int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if last, ok := q.lastTry[modemID]; ok && time.Since(last) < TryEvery {
		return false
	}
	q.lastTry[modemID] = time.Now()
	return true
}

// CheckAllowed 已断网后的复查节流（确认没被重新拨上来）
func (q *QuotaStore) CheckAllowed(modemID int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if last, ok := q.lastChk[modemID]; ok && time.Since(last) < CheckEvery {
		return false
	}
	q.lastChk[modemID] = time.Now()
	return true
}

// QuotaPath 配额文件默认位置（与配置文件同目录；configPath 为空时退回当前目录）
func QuotaPath(configPath string) string {
	if p := strings.TrimSpace(configPath); p != "" {
		return filepath.Join(filepath.Dir(p), "quota.json")
	}
	return "quota.json"
}
