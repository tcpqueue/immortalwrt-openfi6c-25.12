package kernel

// ====================== AT 命令口：自动探测 + 手动指定 ======================
//
// 为什么要有"手动指定"（2026-10-07 真机）：
//
//   - 自动探测只要求候选口对 `AT` 回一个 "OK"，而**回了 OK 不等于它就是模组的命令口** ——
//     实测模组切到 MBIM 模式后，本程序挑中 `/dev/ttyUSB1`（它对 AT 会"回一下 OK"），
//     但发真指令全部超时；真正能执行指令的 `/dev/ttyUSB2` 就在同一台设备的候选列表里。
//     用户看到的现象是"模组控制不了"。
//   - 多口模组（ttyUSB0..4）里"哪个是命令口"各家固件都不一样，换 USB 模式还会变，
//     靠猜总有猜错的时候 ⇒ 界面上给一个下拉，让用户自己定。
//
// 约定：
//
//   - 默认 = 系统探测到的那个（`at_port`）；用户改过就**记住**（写 atport.json，与 quota.json 同目录）；
//   - 键用"模组身份"（IMEI 优先）而不是端口号：端口号正是我们要换的那个东西（见 Key 的注释）；
//   - 记录的是"这台模组的命令口是哪个"，所以换 USB 口也仍然有效。

import (
	"encoding/json"
	"modemd/module/config"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ATPortPath 手动指定命令口的记录文件（与配置文件同目录；为空时退回当前目录）
func ATPortPath(configPath string) string {
	if p := strings.TrimSpace(configPath); p != "" {
		return filepath.Join(filepath.Dir(p), "atport.json")
	}
	return "atport.json"
}

// InitATPorts 载入 atport.json（main 启动时调用一次）
func InitATPorts() { InitATPortStore(ATPortPath(config.ConfigPath)) }

// ATPortAuto 下拉里的"自动探测"（= 清掉手动指定）
const ATPortAuto = ""

// ATPortKey 手动指定 AT 口时用的"模组身份"键。
//
// ⚠️ 不能用端口号当键 —— 端口号正是我们要改的那个；用 IMEI → 型号，
// 都取不到就返回空串（宁可不记，也别把两台模组的设置串在一起）。
func ATPortKey(mod *Modem) string {
	if mod == nil {
		return ""
	}
	if v := strings.TrimSpace(mod.IMEI); v != "" {
		return "imei:" + v
	}
	if v := strings.TrimSpace(mod.ModelName); v != "" {
		return "model:" + v
	}
	return ""
}

// ATPortFallbackKey 身份读不出来时的兜底键：按"这台设备有哪些串口"（同一个 USB 设备分组）认人。
//
// 为什么需要它（2026-10-07 用户报）：自动探测挑错口时（挑中的口能回 OK、但不是命令口），
// 指令一个都跑不出去 ⇒ IMEI 与型号都读不出来 ⇒ ATPortKey 给不出键 ⇒ 界面**存不了**
// "我要用哪个口" ⇒ 这类"未识别模组"永远修不回来。分组（TTYList）在同一个 USB 设备上
// 是稳定的，所以拿它当兜底键；等身份读出来之后，身份键（更精确）优先。
func ATPortFallbackKey(ttyList []string) string {
	ports := make([]string, 0, len(ttyList))
	seen := map[string]bool{}
	for _, p := range ttyList {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		ports = append(ports, p)
	}
	if len(ports) == 0 {
		return ""
	}
	// ⚠️ 顺序必须稳定：同一个分组每轮算出来必须是同一个键（分组的顺序不保证稳定）
	sort.Strings(ports)
	return "ports:" + strings.Join(ports, ",")
}

// ATPortSavedKey 保存手动指定时实际用的键：模组身份键优先；身份读不出来（未识别模组）
// 就退回"串口组"兜底键。
func ATPortSavedKey(mod *Modem) string {
	if mod == nil {
		return ""
	}
	if k := ATPortKey(mod); k != "" {
		return k
	}
	return ATPortFallbackKey(mod.TTYList)
}

// savedATPort 这台模组手动指定的命令口（读：身份键优先，其次串口组兜底键）。
//
// ⚠️ 读的时候两个键都要看：用户可能是"未识别"时存的（串口组键），后来身份读出来了
// （那时ATPortSavedKey 会给出身份键）—— 只看一个键会把人家的设置"弄丢"。
func savedATPort(mod *Modem) string {
	p, _ := savedATPortKeyed(mod)
	return p
}

// savedATPortKeyed 同上，但连"命中哪个键"一起给出来：发现流程判定这条指定已失效时，
// 要按**命中的那个键**删（身份键删不动串口组键存的记录）。
func savedATPortKeyed(mod *Modem) (port, key string) {
	if mod == nil {
		return "", ""
	}
	if k := ATPortKey(mod); k != "" {
		if v := atPortStoreInst.Get(k); v != "" {
			return v, k
		}
	}
	key = ATPortFallbackKey(mod.TTYList)
	return atPortStoreInst.Get(key), key
}

// ATPortOptions 给界面下拉的候选：系统探测到的那个排最前，其余按原顺序（去空去重）。
func ATPortOptions(ttyList []string, at string) []string {
	at = strings.TrimSpace(at)
	var out []string
	seen := map[string]bool{}
	if at != "" {
		out = append(out, at)
		seen[at] = true
	}
	for _, p := range ttyList {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// ATCandidate 一个候选命令口的证据（见 PickATPort）
type ATCandidate struct {
	Port     string
	OK       bool // 对 `AT` 回过 OK
	Identity bool // 还能答上一条"身份/能力"指令（AT+CGMM 之类）—— 真命令口才做得到
}

// PickATPort 从同一台模组的候选口里挑一个命令口。**"回了 OK"只是必要条件**：
//
//	① 能答"身份"指令的优先（`AT+CGMM` 有正常应答 = 真能执行指令的口）；
//	② 只有 OK、没有身份证据的，退回第一个（与老行为一致，别把原本能用的设备搞坏）；
//	③ 一个 OK 都没有 ⇒ 空串。
//
// ⚠️ 这条规则的由来就是上面那段真机事故：只按"第一个回 OK 的口"挑，MBIM 下会挑到
// 一个"会回 OK 但发指令全超时"的口，用户界面直接失控。
func PickATPort(cands []ATCandidate) string {
	first := ""
	for _, c := range cands {
		if !c.OK {
			continue
		}
		if first == "" {
			first = c.Port
		}
		if c.Identity {
			return c.Port
		}
	}
	return first
}

// atPortStore 手动指定的命令口（跨重启记住）
type atPortStore struct {
	mu   sync.Mutex
	path string
	data map[string]string // ATPortKey → 端口名（空串 = 用自动探测）
}

// ⚠️ 变量名别叫 atPorts：discoverModems 里有个同名局部变量（本组挑出的 AT 口列表），
// 会把它遮住（编译期就报 "type []string has no field Get"）。
var atPortStoreInst = &atPortStore{data: map[string]string{}}

// InitATPortStore 载入（main 启动时调用一次；path 与 quota.json 同目录）
func InitATPortStore(path string) { atPortStoreInst.Init(path) }

// Init 载入文件（没有/坏了都当"没指定过"，不报错）
func (s *atPortStore) Init(path string) {
	data := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		var f struct {
			Modems map[string]string `json:"modems"`
		}
		if err := json.Unmarshal(b, &f); err != nil {
			logger.Warn("at port file broken, start from scratch",
				zap.String("path", path), zap.Error(err))
		}
		for k, v := range f.Modems {
			if k = strings.TrimSpace(k); k != "" {
				data[k] = strings.TrimSpace(v)
			}
		}
	}
	s.mu.Lock()
	s.path, s.data = path, data
	s.mu.Unlock()
}

// Get 取这台模组手动指定的命令口（没指定过 / 指定为空 ⇒ 空串 = 用自动探测）
func (s *atPortStore) Get(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[key]
}

// ClearByPort 把"值等于这个端口"的记录全部清掉（返回清了几条）。
//
// 用途：万一模组的身份读不出来（换了错口之后 IMEI 也就读不到了），界面拿不到键，
// 就靠端口名反查着把记录清掉 —— 保证用户永远能回到"自动探测"。
func (s *atPortStore) ClearByPort(port string) int {
	port = strings.TrimSpace(port)
	if port == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, v := range s.data {
		if v == port {
			delete(s.data, k)
			n++
		}
	}
	if n > 0 {
		s.saveLocked()
	}
	return n
}

// atPortProbeThrottle 同一个端口的"自证"探测最短间隔（别每轮巡检都去戳）
const atPortProbeThrottle = time.Minute

var (
	atPortProbeMu  sync.Mutex
	atPortProbedAt = map[string]time.Time{}
)

// atPortProbeAllowed 允许现在探测这个端口吗（同一个口每分钟最多一次）
func atPortProbeAllowed(port string) bool {
	atPortProbeMu.Lock()
	defer atPortProbeMu.Unlock()
	now := time.Now()
	if t, ok := atPortProbedAt[port]; ok && now.Sub(t) < atPortProbeThrottle {
		return false
	}
	atPortProbedAt[port] = now
	return true
}

// Set 记下这台模组的命令口（port 为空 = 恢复自动探测）。返回"有没有变化"。
func (s *atPortStore) Set(key, port string) bool {
	key, port = strings.TrimSpace(key), strings.TrimSpace(port)
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[key] == port {
		return false
	}
	if port == "" {
		delete(s.data, key)
	} else {
		s.data[key] = port
	}
	s.saveLocked()
	return true
}

// saveLocked 落盘（原子写：先写临时文件再 rename，避免断电写坏）
func (s *atPortStore) saveLocked() {
	if s.path == "" {
		return
	}
	payload := struct {
		Version int               `json:"version"`
		SavedAt time.Time         `json:"saved_at"`
		Modems  map[string]string `json:"modems"`
	}{Version: 1, SavedAt: time.Now(), Modems: s.data}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		logger.Warn("at port marshal failed", zap.Error(err))
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		logger.Warn("at port write failed", zap.String("path", tmp), zap.Error(err))
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		logger.Warn("at port rename failed", zap.String("path", s.path), zap.Error(err))
	}
}
