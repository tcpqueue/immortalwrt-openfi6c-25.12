package kernel

import (
	"modemd/module/atparse"
	"modemd/module/common"
	"modemd/module/config"
	"modemd/module/httpx"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ====================== SIM 热插拔（换卡 / 拔卡 / 插卡） ======================
//
// 需求：模组带电时拔插 SIM 卡，程序要能发现，并且**让新卡真正生效**。
//
// 检出用两条路（互补，缺一不可）：
//  1. 模组主动上报 `+CPIN: …`（3GPP 的 SIM 状态 URC）：读协程看到就置一个 dirty 标记，
//     巡检很快去核实 —— 拔卡/插卡通常立刻就来，不用干等一个轮询周期；
//  2. 定期轮询 `AT+CPIN?` + ICCID：兜住"悄悄换了卡、但固件根本不发 URC"的情况。
//
// ⚠️ 只靠 URC 会被"不发 URC 的固件"坑；只靠轮询又要等一整个周期才提示 ⇒ 两条都要。
// ⚠️ 检出后要不要复位模组是**可配置**的：不少模组（尤其带 SIM 插座没做热插拔检测的）
// 不复位就一直在用旧卡，现象是"换了卡还是旧卡的号" ⇒ 默认 `[sim] hotswap_reset = true`，
// 发一次 AT+CFUN=1,1 让模组重新读卡、重新注网。
//
// 误报防护：
//   - 身份只比 ICCID / IMSI，而且**两边都非空才比**（读卡偶发失败会回空串，拿空值比会误判成换卡）；
//   - 动作之间有 simHotswapCooldown 冷却（反复插拔、读卡抖动不会把模组反复复位）。

// ---- from: modem_sim_hotswap.go ----

// SIMIdentity SIM 卡身份 —— 用来回答"还是不是同一张卡"。
type SIMIdentity struct {
	Present bool   `json:"present"`
	State   string `json:"state,omitempty"` // AT+CPIN 的状态文本：READY / SIM PIN / SIM PUK …
	ICCID   string `json:"iccid,omitempty"`
	IMSI    string `json:"imsi,omitempty"` // 只在读不到 ICCID 时才用它当身份
}

// SIM 热插拔事件类型
const (
	SIMUnchanged = ""
	SIMRemoved   = "removed"  // 卡被拔了
	SIMInserted  = "inserted" // 原本没卡，插上了
	SIMSwapped   = "swapped"  // 换了一张卡
)

// simHotswapCooldown 两次"换卡动作"之间至少隔这么久。
// 30 秒：够覆盖"拔出来再插回去"的手速，又能挡住读卡抖动导致的反复复位。
const simHotswapCooldown = 30 * time.Second

// simResetSMSCacheDelay 复位模组（AT+CFUN=1,1）之后等多久再去读新卡的短信。
// 25 秒：模组重新读卡 + 注网大概要这么久，等太早只会读到 `SIM not inserted`。
const simResetSMSCacheDelay = 25 * time.Second

// CPINPresent 判断 +CPIN 的状态文本是否表示"卡在位"。
//
// 约定（3GPP TS 27.007）：`AT+CPIN?` 答 `+CPIN: READY` / `+CPIN: SIM PIN` / `SIM PUK` 都算在位
// （只是可能锁着）；**拿不到 +CPIN 行**（多数固件这时回 `+CME ERROR: 10` = SIM not inserted）
// 才算不在位。少数固件用 `+CPIN: SIM REMOVED` 这种文本来表示不在。
func CPINPresent(state string) bool {
	s := strings.ToUpper(strings.TrimSpace(state))
	if s == "" {
		return false
	}
	for _, bad := range []string{"REMOVED", "NOT INSERTED", "INSERTED-NOT", "ABSENT", "NO SIM"} {
		if strings.Contains(s, bad) {
			return false
		}
	}
	return true
}

// simIdentityDiffers 是不是换了一张卡。**只比 ICCID / IMSI，且两边都非空才比**：
// 读卡偶发失败会回空串，拿空值去比会把"读失败"误判成"换卡"。
func simIdentityDiffers(a, b SIMIdentity) bool {
	if a.ICCID != "" && b.ICCID != "" && a.ICCID != b.ICCID {
		return true
	}
	if a.IMSI != "" && b.IMSI != "" && a.IMSI != b.IMSI {
		return true
	}
	return false
}

// simHotswapEvent 把"上一次 / 这一次"的身份分类成事件（纯函数，便于用例覆盖）。
//
// ⚠️⚠️ 这里踩过一个很贵的坑（2026-10-01，真机把模块搞成反复掉线）：
// 模组被复位/重新枚举时 SIM 必然读不到，接下来读到的又是同一张卡 —— 如果只看
// "present false→true" 就当成"插了新卡"，而插入又触发 `AT+CFUN=1,1` 复位，
// 就会变成**无限复位循环**（每 2~3 分钟把模组打死一次）。
// 所以：① 只有 **ICCID/IMSI 确实变了** 才算换卡；② 拔卡前记住的身份会保留（见 checkSIMHotswap），
// 同一张卡回来时能比出来”一样”。
func simHotswapEvent(prev, cur SIMIdentity) string {
	switch {
	case prev.Present && !cur.Present:
		return SIMRemoved
	case !prev.Present && cur.Present:
		if simIdentityDiffers(prev, cur) {
			return SIMSwapped
		}
		// 之前没卡、现在有卡；但我们没读到过旧身份（拿不到 ICCID 的固件）⇒ 只能当"插卡"喊一声，
		// **绝不能当换卡去复位模组**（见 shouldResetModem）。
		return SIMInserted
	case prev.Present && cur.Present:
		if simIdentityDiffers(prev, cur) {
			return SIMSwapped
		}
	}
	return SIMUnchanged
}

// shouldResetModem 该不该为这个热插拔事件复位模组（AT+CFUN=1,1）。
//
// ⚠️ **只有"确认换了另一张卡"才允许复位**：
//
//	· 插卡/拔卡（SIMInserted / SIMRemoved）可能是读卡抖动或我们自己复位造成的假动作，
//	  拿它去复位就会变成无限循环（真机实测：模块每 3 分钟掉一次）；
//	· 还要用户在 config.toml 里显式打开 `[sim] hotswap_reset`（默认 false）。
func shouldResetModem(kind string, cfgReset bool) bool {
	return cfgReset && kind == SIMSwapped
}

// shouldCheckSIM 现在该不该去读一次 SIM。
//
//   - dirty（收到过 `+CPIN:` URC）⇒ 立刻读，不等周期；
//   - 否则按 every 节流；every <= 0 表示不检查（配置里关掉了）。
func shouldCheckSIM(lastCheck time.Time, every time.Duration, dirty bool, now time.Time) bool {
	if dirty {
		return true
	}
	if every <= 0 {
		return false
	}
	return lastCheck.IsZero() || now.Sub(lastCheck) >= every
}

// ---- 模组侧 ----

// simSnapshot 取当前记录的 SIM 身份（加锁）
func (mod *Modem) simSnapshot() SIMIdentity {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	return mod.simIdent
}

// MarkSIMDirty 标记"该去核实 SIM 了"。
//
// 读协程收到 `+CPIN:` 且**与已知状态不同**时调用 —— 这样 `AT+CPIN?` 自己的应答
// （内容与已知状态一样）不会再触发一轮，避免变成 2 秒一次的自我轮询。
func (mod *Modem) MarkSIMDirty() {
	mod.mu.Lock()
	mod.simDirty = true
	mod.mu.Unlock()
}

// noteCPINLine 读协程对每一行 `+CPIN:` 的处理：与已知状态不同就是变化，置 dirty。
func (mod *Modem) noteCPINLine(line string) {
	st := atparse.ParseCPIN([]string{line})
	if st == "" {
		return
	}
	mod.mu.Lock()
	known := mod.simIdent.State
	mod.mu.Unlock()
	if !strings.EqualFold(strings.TrimSpace(st), strings.TrimSpace(known)) {
		mod.MarkSIMDirty()
	}
}

// querySIMIdentity 读一次 SIM 身份。
//
// 便宜的在前面：`AT+CPIN?` 一条就能判"卡在不在"，不在就不必再问 ICCID。
// 读不到 ICCID 的少数固件退回 IMSI 当身份（换卡时它同样会变）。
func (mod *Modem) querySIMIdentity() SIMIdentity {
	id := SIMIdentity{}
	if resp, err := mod.SendAT("AT+CPIN?"); err == nil && !common.RespHasError(resp) {
		id.State = atparse.ParseCPIN(resp)
	}
	id.Present = CPINPresent(id.State)
	if !id.Present {
		return id
	}
	if v := mod.queryICCID(); v != "" {
		id.ICCID = v
	} else if resp, err := mod.SendAT("AT+CIMI"); err == nil && !common.RespHasError(resp) {
		id.IMSI = atparse.ParseIMSI(resp)
	}
	return id
}

// SIMSnapshot 对外暴露的 SIM 身份 + 最近一次热插拔事件（给 /api/sim 用）。
type SIMSnapshot struct {
	SIMIdentity
	LastEvent   string    `json:"last_event,omitempty"`
	LastEventAt time.Time `json:"last_event_at,omitempty"`
	CheckedAt   time.Time `json:"checked_at,omitempty"`
}

// SIMState 取 SIM 身份与最近事件（只读，不发 AT）
func (mod *Modem) SIMState() SIMSnapshot {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	return SIMSnapshot{
		SIMIdentity: mod.simIdent,
		LastEvent:   mod.simLastEvent,
		LastEventAt: mod.simLastEventAt,
		CheckedAt:   mod.simCheckedAt,
	}
}

// checkSIMHotswap 读一次 SIM 身份；变了就处理（换卡动作 + 广播事件）。
//
// ⚠️ 两条"绝不误判"的规则（都是真机踩出来的）：
//  1. **读不到确定答复（State == ""）时不判任何事件** —— 模组复位/USB 重新枚举期间
//     CPIN 一定读不到，把它当"拔卡"会引出无限复位循环；
//  2. **卡不在位时保留上次读到的 ICCID/IMSI**，并且第一个"确定答复"只建基线不事件。
func (mod *Modem) checkSIMHotswap() {
	now := time.Now()
	mod.mu.Lock()
	mod.simCheckedAt = now
	mod.simDirty = false
	prev, known := mod.simIdent, mod.simIdentKnown
	mod.mu.Unlock()

	cur := mod.querySIMIdentity()

	mod.mu.Lock()
	if cur.State == "" {
		// 读失败：只更新"何时查过"，身份原样保留（连 Present 都不改）
		mod.mu.Unlock()
		return
	}
	kind := SIMUnchanged
	if known {
		kind = simHotswapEvent(prev, cur)
	}
	if !cur.Present {
		// 卡读不到时别把身份丢了（否则同一张卡回来会被当成新卡）
		cur.ICCID, cur.IMSI = prev.ICCID, prev.IMSI
	}
	mod.simIdent = cur
	mod.simIdentKnown = true
	if kind == SIMUnchanged {
		mod.mu.Unlock()
		return
	}
	if now.Before(mod.simCooldownUntil) {
		mod.mu.Unlock()
		logger.Info("sim hotswap detected but in cooldown; state updated only",
			zap.Int("modem", mod.ID), zap.String("kind", kind))
		return
	}
	mod.simCooldownUntil = now.Add(simHotswapCooldown)
	mod.simLastEvent, mod.simLastEventAt = kind, now
	// 缓存的 SIM/模组状态（ICCID、IMSI、号码、SIM 状态、温度…）整块作废：卡都换了，
	// 界面下次一读就是新卡；不然会拿着旧卡的 ICCID 显示。
	mod.ModemStatus = nil
	mod.ownNumCRSMOff = false
	mod.mu.Unlock()

	logger.Warn("sim hotswap detected",
		zap.Int("modem", mod.ID),
		zap.String("kind", kind),
		zap.Bool("present", cur.Present),
		zap.String("state", cur.State),
		zap.String("iccid", cur.ICCID))
	pushSIMEvent(mod, kind, cur)

	go mod.reapplyAfterSIMChange(kind)
}

// reapplyAfterSIMChange 换卡之后让新卡真正生效。
//
// ① 重配"按卡"的短信设置（存储区 CPMS + 新短信上报 CNMI）—— 这两项是存在卡/模组上的，
//
//	换了卡要重来一遍，否则可能出现"短信读不出来 / 新短信不再推送"。
//
// ② 可选复位模组（**只有确认换了另一张卡 + 用户显式打开 hotswap_reset** 才做）：
// 不带 SIM 热插拔硬件的模组不复位会一直用旧卡。
//
// ⚠️ 拔卡时什么都不做（卡都不在，发指令只会超时）；
// ⚠️ 插卡/读卡抖动**绝不复位**（否则会无限复位模块，把模块搞成反复掉线）。
func (mod *Modem) reapplyAfterSIMChange(kind string) {
	if kind == SIMRemoved {
		// 卡拔了：卡里的短信已经读不到了，别让屏幕/网页还挂着旧卡那几条
		mod.clearSMSCache()
		return
	}
	if !mod.isRunningNow() {
		return
	}
	// 换卡/插卡的一瞬先把缓存清了（等下面读新卡可能要几十秒，不能让旧卡的短信留在屏幕上）
	mod.clearSMSCache()
	mod.applySMSStorage()
	mod.applySMSNotify()

	if !shouldResetModem(kind, config.Cfg.SIM.HotswapReset) {
		// 不复位：直接读新卡的短信（新卡已生效）
		go mod.refreshSMSCache(0, 0)
		return
	}
	// AT+CFUN=1,1 = 复位模组（重新读卡 + 重新注网）。
	// ⚠️ 有的固件这条**不回 OK**（实测鼎桥 MT5700 就是），超时是正常的 ⇒ 只记 Debug。
	if _, err := mod.SendAT("AT+CFUN=1,1"); err != nil {
		logger.Debug("sim hotswap: modem reset returned no OK (normal on some firmwares)",
			zap.Int("modem", mod.ID), zap.Error(err))
	}
	logger.Warn("sim hotswap: modem reset to activate the new SIM (AT+CFUN=1,1)",
		zap.Int("modem", mod.ID))
	// 复位后要等模组重新起来才能读卡：等一会儿再读新卡的短信填回缓存。
	// （期间模组重枚举会再跑一次 startupProbe，那里也会读一遍；这里作兜底。）
	go mod.refreshSMSCache(simResetSMSCacheDelay, 0)
}

// apiSIMState GET /api/sim?modem_id=0
//
// 当前 SIM 身份（在不在 / 状态 / ICCID / IMSI）+ 最近一次热插拔事件 + 上次核实时间。
// **只读、不发 AT**（要完整信息——温度/本机号码/时钟——走 /api/modem/status）。
// 放这个文件是因为它就是 SIM 热插拔的家（路由注册在 api.go）。
func apiSIMState(mgr *ModemManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mod, err := modemFromQuery(mgr, r)
		if err != nil {
			httpx.WriteJSON(w, -1, nil, err.Error())
			return
		}
		httpx.WriteJSON(w, 0, mod.SIMState(), "ok")
	}
}

// SIMWatchLoop SIM 热插拔巡检。
//
// interval 是"没有 URC 时的兜底轮询间隔"（配置 `[sim] hotswap_interval`，<=0 关闭）。
// 循环本身跑得比较密（simWatchTick），但**只有到点或收到 URC 才真去读 AT** ——
// 这样拔卡能秒级发现，平时也不会一直占串口。
func SIMWatchLoop(mgr *ModemManager, interval time.Duration) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(simWatchTick)
	defer t.Stop()
	for range t.C {
		for _, mod := range mgr.listModems() {
			if !mod.isRunningNow() {
				continue
			}
			now := time.Now()
			mod.mu.Lock()
			last, dirty := mod.simCheckedAt, mod.simDirty
			mod.mu.Unlock()
			if !shouldCheckSIM(last, interval, dirty, now) {
				continue
			}
			mod.checkSIMHotswap()
		}
	}
}

// simWatchTick 巡检节拍：只决定"多久看一眼 dirty / 到没到点"，不决定读 AT 的频率。
const simWatchTick = 2 * time.Second
