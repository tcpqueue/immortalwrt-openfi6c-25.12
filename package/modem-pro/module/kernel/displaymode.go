package kernel

import (
	"modemd/module/common"
	"modemd/module/modedialect"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ====================== 屏幕端（tftdisplay）：组网模式偏好 ======================
//
// 屏幕上 Mobile 页第 3 屏有一排三个挡位按钮：**4G / 5G / 4G+5G**
// （用户原话：「在 Mobile 添加一个新的左右划 UI 功能是 组网模式偏好 4G 5G 4G+5G 3 种」）。
//
// 值就是本机码表里的**语义档位**（移远：`AT+QNWPREFCFG="mode_pref",LTE / NR5G / LTE:NR5G`），
// 由 vendor_settings_engine.go 的 SetModePrefCaps 落地到各家语法上 —— 本文件只负责：
//
//	① 把「当前值 + 中文名 + 三个按钮（能不能按）」整理成一条 WS 推送（type = "mode"）；
//	② 按 TTL 缓存，屏幕重连/来回切页不会把串口刷满。
//
// ⚠️ 这是 AT **动作**（读当前值要发一条指令），不是纯状态：本机 USB 模组毫秒级，
// 但同一份代码还跑在展锐 SIPC 的慢总线上（一条 ~10 秒）⇒ **必须**能缓存。
// 这个坑项目里踩过：web 前端 `loadModePref()` 每次推送都真读，40 秒发了 12 条 `AT+SPRAT?`
//（见 /memories 的 f50 笔记）；所以这里与 web 端一样按 modID 记 TTL。

// displayModeTTL 组网模式偏好的缓存时长：
// 屏幕切页/重连很频繁，而模式是"用户点了才变"的东西，一分钟读一次足够了。
// 一小时内用户手动连续点 3 个挡位时，每次写都会刷新缓存（见 SetModePrefForDisplay），
// 所以不会看到旧值。
const displayModeTTL = 60 * time.Second

// displayModeScreenLabels 屏幕上的三个挡位（顺序 = 界面从左到右）：
// 语义值 → 屏幕按钮上的字。**标签在服务端算**，屏幕只管画：
// 这样"本机没有这一档"（如展锐只有 仅4G / 4G+5G 两档）能直接标成不可按，而不是让用户点了必定失败。
var displayModeScreenLabels = []struct{ Value, Label string }{
	{common.ModeLTE, "4G"},
	{common.ModeNR5G, "5G"},
	{common.ModeLTENR5G, "4G+5G"},
}

// displayModeShortLabels 语义值 → 屏幕右上“当前 xxx”用的**短名**。
// ⚠️ 长名字放不下：320px 宽的屏，右上角那行只有 ~190px，而
// “自动（2G/3G/4G/5G）”这种全名会把左边那一行顶到重叠（真机截图踩过）。
var displayModeShortLabels = map[string]string{
	common.ModeAuto:     "自动",
	common.ModeWCDMA:    "仅3G",
	common.ModeLTE:      "仅4G",
	common.ModeNR5G:     "仅5G",
	common.ModeWCDMALTE: "3G+4G",
	common.ModeWCDMANR:  "3G+5G",
	common.ModeLTENR5G:  "4G+5G",
}

// displayModeShortLabel 取短名（认不出的取值返回空串，屏幕会退回用全名）。
func displayModeShortLabel(value string) string {
	return displayModeShortLabels[strings.ToUpper(strings.TrimSpace(value))]
}

// DisplayModeInfo 屏幕「组网模式偏好」一屏要的全部内容（WS type = "mode" 的 data）。
type DisplayModeInfo struct {
	// Value 当前值（语义档位，如 LTE / NR5G / LTE:NR5G；未取证机型是本机原码）
	Value string `json:"value,omitempty"`
	// Name 当前值的中文全名（"仅 4G（LTE）" / "4G + 5G（NSA/SA）"…）
	Name string `json:"name,omitempty"`
	// Short 当前值的**短名**（自动 / 仅4G / 4G+5G…），屏幕右上角那行用它
	Short string `json:"short,omitempty"`
	// Msg 最近一次操作的反馈，屏幕在按钮下面显示一行：
	// "已切换到 仅 5G（NR）" / "设置失败：…"（空 = 没话说，屏幕不显示这行）
	Msg string `json:"msg,omitempty"`
	// Choices 本机全部可选档位（屏幕只挑其中三个做按钮，这份留给排查/将来扩展）
	Choices []modedialect.ModeChoice `json:"choices,omitempty"`
	// Screen 屏幕那三个按钮：value = 语义档位、name = 4G/5G/4G+5G、writable = 本机能不能写
	Screen []modedialect.ModeChoice `json:"screen,omitempty"`
	// ReadOnly 本机的组网模式**只能读**（屏幕把三个按钮全置灰并写明原因）
	ReadOnly bool `json:"read_only,omitempty"`
}

// screenModeChoices 从本机档位表里挑出屏幕那三个按钮（挑不到 / 不可写的标 writable=false）。
func screenModeChoices(caps *ModePrefCaps) []modedialect.ModeChoice {
	out := make([]modedialect.ModeChoice, 0, len(displayModeScreenLabels))
	for _, s := range displayModeScreenLabels {
		writable := false
		if caps != nil {
			for _, c := range caps.Choices {
				if strings.EqualFold(strings.TrimSpace(c.Value), s.Value) {
					writable = c.Writable
					break
				}
			}
			// 只读机型（取值表未取证）一律不可写：给用户一个点了必定失败的按钮比不给更糟。
			if caps.ReadOnly {
				writable = false
			}
		}
		out = append(out, modedialect.ModeChoice{
			Value:    s.Value,
			Name:     s.Label,
			Writable: writable,
			Source:   "屏幕挡位",
		})
	}
	return out
}

// displayModeFromCaps 把一次查询结果整理成屏幕要的那份（msg 是附带的操作反馈）。
func displayModeFromCaps(caps *ModePrefCaps, msg string) *DisplayModeInfo {
	if caps == nil {
		caps = &ModePrefCaps{}
	}
	return &DisplayModeInfo{
		Value:    caps.Value,
		Name:     caps.Name,
		Short:    displayModeShortLabel(caps.Value),
		Msg:      msg,
		Choices:  caps.Choices,
		Screen:   screenModeChoices(caps),
		ReadOnly: caps.ReadOnly,
	}
}

// cloneDisplayMode 拷贝一份（推送要交给别的 goroutine 序列化，别把内部指针递出去）。
func cloneDisplayMode(in *DisplayModeInfo) *DisplayModeInfo {
	if in == nil {
		return nil
	}
	out := *in
	out.Choices = append([]modedialect.ModeChoice(nil), in.Choices...)
	out.Screen = append([]modedialect.ModeChoice(nil), in.Screen...)
	return &out
}

// ModePrefForDisplay 取屏幕要的组网模式信息：
// 缓存新鲜（displayModeTTL 内）就直接给缓存，**一条 AT 都不发**；过期才真读一次。
//
// 读失败时：沿用上一次那份（别把屏幕上的值擦成空白），并把时间戳也推后 ——
// 免得模组不认这条指令时每次推送都白等一次超时。首轮就失败则返回 nil（屏幕显示"读取中"）。
func (mod *Modem) ModePrefForDisplay() *DisplayModeInfo {
	mod.displayModeMu.Lock()
	if mod.displayMode != nil && time.Since(mod.displayModeAt) < displayModeTTL {
		info := cloneDisplayMode(mod.displayMode)
		mod.displayModeMu.Unlock()
		return info
	}
	if mod.displayModeBusy { // 已经有人在读了：别排队占串口，先把现成的给出去
		info := cloneDisplayMode(mod.displayMode)
		mod.displayModeMu.Unlock()
		return info
	}
	mod.displayModeBusy = true
	mod.displayModeMu.Unlock()

	caps, err := mod.QueryModePrefCaps()

	mod.displayModeMu.Lock()
	mod.displayModeBusy = false
	mod.displayModeAt = time.Now()
	if err != nil {
		logger.Debug("display mode query failed", zap.Int("modem", mod.ID), zap.Error(err))
		info := cloneDisplayMode(mod.displayMode)
		mod.displayModeMu.Unlock()
		return info
	}
	mod.displayMode = displayModeFromCaps(caps, "")
	info := cloneDisplayMode(mod.displayMode)
	mod.displayModeMu.Unlock()
	return info
}

// SetModePrefForDisplay 屏幕点了挡位：切组网模式，成功/失败都写进缓存那份的 Msg。
//
// ⚠️ 由调用方丢 goroutine 里跑（写模式要下发 + 回读确认，可能占串口几秒）。
func (mod *Modem) SetModePrefForDisplay(value string) error {
	caps, err := mod.SetModePrefCaps(value)
	if err != nil {
		return err
	}
	label := displayModeScreenLabel(caps.Value)
	if label == "" {
		label = displayModeShortLabel(caps.Value)
	}
	if label == "" {
		label = caps.Name
	}
	mod.displayModeMu.Lock()
	mod.displayMode = displayModeFromCaps(caps, "已切换到 "+label)
	mod.displayModeAt = time.Now()
	mod.displayModeMu.Unlock()
	logger.Info("display mode_pref set", zap.Int("modem", mod.ID),
		zap.String("value", caps.Value))
	return nil
}

// noteDisplayModeMsg 只改缓存那份的操作反馈（设置失败时用：屏幕要看到原因）。
func (mod *Modem) noteDisplayModeMsg(msg string) {
	mod.displayModeMu.Lock()
	if mod.displayMode == nil {
		mod.displayMode = &DisplayModeInfo{}
	}
	mod.displayMode.Msg = msg
	// ⚠️ 故意**不**刷新 displayModeAt：失败不该让缓存变"新鲜"而挡住下一步重试很久。
	mod.displayModeMu.Unlock()
}

// displayModeScreenLabel 语义值 → 屏幕按钮上的字（LTE → 4G）。
func displayModeScreenLabel(value string) string {
	for _, s := range displayModeScreenLabels {
		if strings.EqualFold(strings.TrimSpace(value), s.Value) {
			return s.Label
		}
	}
	return ""
}

// pushDisplayMode 把组网模式偏好推给屏幕（没人连着时 broadcast 自己早退）。
//
// ⚠️ 会走 ModePrefForDisplay ⇒ 缓存过期时真发一条 AT：调用点必须是
// "屏幕连上/切到该页/用户改完"这种低频入口，**不要**放进周期推送。
func pushDisplayMode(mod *Modem) {
	info := mod.ModePrefForDisplay()
	if info == nil {
		return
	}
	id := mod.ID
	broadcast("mode", &id, info)
}
