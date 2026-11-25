package atport

// ====================== AT 命令口：按 USB 设备（VID:PID）手动绑定 ======================
//
// 用户 2026-10-08 要求：
//
//在「USB 热插拔」里按 **设备 VID:PID** 手动绑定串口；**绑上之后就直接用那个口**，
//不再去扫描/探测"哪个串口是 AT 口"。
//
// 与 `go`（按"模组身份"指定命令口）的分工：
//
//go   键 = IMEI/型号（或串口组兜底），**要等模组被认出来**才认得出，
//            而且用它之前还会验一遍"这个口答不答 AT"（挑错口就整个认不出来）。
//本文件      键 = USB 设备身份 `vid:pid`，**不认识模组也能用**（这正是用户要的场景：
//            自动探测挑错口 ⇒ 指令跑不出去 ⇒ IMEI/型号都读不出来 ⇒ 只能人工指定）。
//
// 约定：
//
//   - 记录写在 `atport_bind.json`（与配置文件同目录，和 json / quota.json 一个套路）；
//   - 绑定的口**不在这台设备的串口里**（换了固件/USB 模式把口名变了）⇒ 不生效、退回自动探测，
//     并记一条日志（限速，别每轮巡检刷屏），绝不把用户锁死在错口上；
//   - 绑定是"设备级"的（同一台模组插在哪个 USB 口都一样），所以它优先于按模组身份的指定。

import (
	"encoding/json"
	"modemd/module/config"
	"modemd/module/usbids"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ATPortBindPath 命令口绑定的记录文件（与配置文件同目录；为空时退回当前目录）
func ATPortBindPath(configPath string) string {
	if p := strings.TrimSpace(configPath); p != "" {
		return filepath.Join(filepath.Dir(p), "atport_bind.json")
	}
	return "atport_bind.json"
}

// ATPortBindKey 绑定的键：`vid:pid`，归一成**小写十六进制**（"0E8D:7126" / "0x2cb7:0x0a06" 都认；
// 也接受把 "0e8d:7126" 整体传进来）。
//
// ⚠️ 每一段都要求**正好 4 位十六进制**：VID/PID 就是这个长度，卡死它才能挡住手抖
// （"abc"、"0e8d1" 这种一律当没填，界面会提示）。
func ATPortBindKey(vid, pid string) string {
	// 允许只给一个 "vid:pid"（界面上手工粘进来的情况）
	if strings.TrimSpace(pid) == "" && strings.Contains(vid, ":") {
		vid, pid = usbids.SplitUSBVidPid(vid)
	}
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.TrimPrefix(s, "0x")
		if len(s) != 4 {
			return ""
		}
		for _, r := range s {
			if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
				return ""
			}
		}
		return s
	}
	v, p := norm(vid), norm(pid)
	if v == "" || p == "" {
		return ""
	}
	return v + ":" + p
}

// ATPortBind 一条绑定（给 API / 界面用）
type ATPortBind struct {
	Key  string `json:"key"`  // "0e8d:7126"
	VID  string `json:"vid"`  // "0e8d"
	PID  string `json:"pid"`  // "7126"
	Port string `json:"port"` // "/dev/ttyUSB2"
	// Present 这台设备**当前**有没有这个端口（界面用来提示"绑的口现在不在"）
	Present bool `json:"present"`
}

type atPortBindStore struct {
	mu   sync.Mutex
	path string
	data map[string]string // "vid:pid" → "/dev/ttyUSB2"
}

var atPortBindInst = &atPortBindStore{data: map[string]string{}}

// InitATPortBinds 载入 atport_bind.json（main 启动时调用一次）
func InitATPortBinds() { InitATPortBind(ATPortBindPath(config.ConfigPath)) }

// InitATPortBind 载入文件（没有/坏了都当"没绑过"，不报错）
func InitATPortBind(path string) {
	data := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		var f struct {
			Binds map[string]string `json:"binds"`
		}
		if err := json.Unmarshal(b, &f); err != nil {
			config.Logger.Warn("at port bind file broken, start from scratch",
				zap.String("path", path), zap.Error(err))
		}
		for k, v := range f.Binds {
			key := strings.TrimSpace(k)
			port := strings.TrimSpace(v)
			if key != "" && port != "" {
				data[key] = port
			}
		}
	}
	atPortBindInst.mu.Lock()
	atPortBindInst.path, atPortBindInst.data = path, data
	atPortBindInst.mu.Unlock()
}

// ATPortBindOf 这台 USB 设备（vid:pid）绑的命令口（没绑 ⇒ 空串）
func ATPortBindOf(vid, pid string) string {
	key := ATPortBindKey(vid, pid)
	if key == "" {
		return ""
	}
	atPortBindInst.mu.Lock()
	defer atPortBindInst.mu.Unlock()
	return atPortBindInst.data[key]
}

// ATPortBindList 全部绑定（按键排序，界面直接用）
func ATPortBindList() []ATPortBind {
	atPortBindInst.mu.Lock()
	out := make([]ATPortBind, 0, len(atPortBindInst.data))
	for k, v := range atPortBindInst.data {
		vid, pid, _ := strings.Cut(k, ":")
		out = append(out, ATPortBind{Key: k, VID: vid, PID: pid, Port: v})
	}
	atPortBindInst.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// ATPortBindListWithPresence 同 ATPortBindList，但把"这个口现在还在不在"标出来（界面要提示）
func ATPortBindListWithPresence(ports []string) []ATPortBind {
	list := ATPortBindList()
	for i := range list {
		for _, p := range ports {
			if strings.TrimSpace(p) == list[i].Port {
				list[i].Present = true
				break
			}
		}
	}
	return list
}

// SetATPortBind 绑定 / 解绑（port 为空 = 删掉这条），返回归一化后的键。
//
// 键非法（vid/pid 读不出来）⇒ 返回空串 + 错误说明，调用方直接报给界面。
func SetATPortBind(vid, pid, port string) (key string, changed bool, err error) {
	key = ATPortBindKey(vid, pid)
	if key == "" {
		return "", false, errATPortBindKey{vid: vid, pid: pid}
	}
	port = strings.TrimSpace(port)
	atPortBindInst.mu.Lock()
	defer atPortBindInst.mu.Unlock()
	if atPortBindInst.data[key] == port {
		return key, false, nil // 没变化
	}
	if port == "" {
		delete(atPortBindInst.data, key)
	} else {
		atPortBindInst.data[key] = port
	}
	atPortBindInst.saveLocked()
	return key, true, nil
}

// errATPortBindKey vid/pid 写不出来时的错误（界面上要好懂）
type errATPortBindKey struct{ vid, pid string }

func (e errATPortBindKey) Error() string {
	return "VID/PID 认不出来（要像 0e8d / 7126 这样的四位十六进制）：" + e.vid + ":" + e.pid
}

// saveLocked 落盘（原子写：先写临时文件再 rename，避免断电写坏）
func (s *atPortBindStore) saveLocked() {
	if s.path == "" {
		return
	}
	payload := struct {
		Version int               `json:"version"`
		SavedAt time.Time         `json:"saved_at"`
		Binds   map[string]string `json:"binds"`
	}{Version: 1, SavedAt: time.Now(), Binds: s.data}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		config.Logger.Warn("at port bind marshal failed", zap.Error(err))
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		config.Logger.Warn("at port bind save failed", zap.String("path", s.path), zap.Error(err))
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		config.Logger.Warn("at port bind rename failed", zap.String("path", s.path), zap.Error(err))
	}
}

// BoundATPortPick 纯逻辑：绑定的口能不能用在"这组串口"上。
//
// 只有绑的口**确实出现在这台设备的串口里**才认（否则说明口名变了 / 设备换了），
// 这时返回 ("", false)，调用方退回自动探测 —— 宁可多扫一次，也别把用户锁在错口上。
func BoundATPortPick(bound string, group []string) (string, bool) {
	bound = strings.TrimSpace(bound)
	if bound == "" {
		return "", false
	}
	for _, p := range group {
		if p == bound {
			return bound, true
		}
	}
	return "", false
}

// atPortBindWarnGap 绑定失效日志的限速（同一台设备 60 秒最多说一次）
const atPortBindWarnGap = time.Minute

var (
	AtPortBindWarnMu   sync.Mutex
	AtPortBindWarnedAt = map[string]time.Time{}
	// atPortBindAppliedAt 上次"绑定生效"打日志时用的端口（只在变化时打，别每轮巡检刷屏）
	atPortBindAppliedAt = map[string]string{}
)

// atPortBindAppliedPort 记下"这台设备这次用的是哪个绑定口"，返回 true = 与上次不同（该打日志）
func atPortBindAppliedPort(key, port string) bool {
	if key == "" {
		return false
	}
	AtPortBindWarnMu.Lock()
	defer AtPortBindWarnMu.Unlock()
	if atPortBindAppliedAt[key] == port {
		return false
	}
	atPortBindAppliedAt[key] = port
	return true
}

// AtPortBindWarnAllowed 这台设备现在允许打"绑定失效"日志吗
func AtPortBindWarnAllowed(vid, pid string, now time.Time) bool {
	key := ATPortBindKey(vid, pid)
	if key == "" {
		return true
	}
	AtPortBindWarnMu.Lock()
	defer AtPortBindWarnMu.Unlock()
	if t, ok := AtPortBindWarnedAt[key]; ok && now.Sub(t) < atPortBindWarnGap {
		return false
	}
	AtPortBindWarnedAt[key] = now
	return true
}
