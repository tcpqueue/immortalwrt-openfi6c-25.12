package kernel

import (
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ScheduleLoop 定时器循环。
//
// ⚠️ 和配额一样**不看有没有客户端在线**：定时动作（锁频段/锁基站）必须到点就执行。
// 启动时先跑一轮，把"进程重启前几分钟内该做但还没来得及做"的槽位补上。
func ScheduleLoop(mgr *ModemManager, interval time.Duration) {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	schedTickAll(mgr)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		schedTickAll(mgr)
	}
}

// schedTickAll 跑一轮：把到点的槽位执行掉
func schedTickAll(mgr *ModemManager) {
	now := time.Now()
	changed := false
	for _, t := range schedSnapshot() {
		due := t.DuePhases(now)
		if len(due) == 0 {
			continue
		}
		mod := schedResolve(mgr, t)
		for _, phase := range due {
			if mod == nil {
				schedRecord(t.ID, Run{
					Phase: phase, Slot: Slot(now, TimeOfPhase(t, phase)),
					At: now, OK: false, Msg: "目标模组当前不在线，已跳过",
				})
				changed = true
				continue
			}
			if !mod.isRunningNow() {
				schedRecord(t.ID, Run{
					Phase: phase, Slot: Slot(now, TimeOfPhase(t, phase)),
					At: now, OK: false, Msg: "模组未运行（串口未打开），已跳过",
				})
				changed = true
				continue
			}
			run := runSchedPhase(mod, t, phase)
			schedRecord(t.ID, run)
			changed = true
		}
	}
	if changed {
		pushSchedule(mgr)
		FlushSchedule()
	}
}

// runSchedPhase 执行一个槽位（按顺序执行该时间点的全部动作）并返回结果记录。
//
// 多个动作是**依次下发**的：一个动作失败不影响后面的（例如固件不支持锁频段，
// 但飞行模式那一步仍然要执行），最终结果里会写清成功几个、哪个失败。
func runSchedPhase(mod *Modem, t *Task, phase string) Run {
	actions := ActionsOf(t, phase)
	slot := Slot(time.Now(), TimeOfPhase(t, phase))
	run := Run{Phase: phase, Slot: slot, At: time.Now()}
	if len(actions) == 0 {
		run.Msg = "该时间点没有动作（请重新编辑任务）"
		return run
	}

	var fails []string
	done := 0
	for _, action := range actions {
		text := ActionText(action)

		// 先看固件支持不支持（避免把"不支持"当执行失败反复写日志）
		if err := schedActionSupported(mod, action.Kind); err != nil {
			fails = append(fails, text+"："+err.Error())
			if schedLogAllowed(t.ID) {
				logger.Warn("schedule action unsupported",
					zap.String("task", t.ID), zap.Int("modem", mod.ID), zap.Error(err))
			}
			continue
		}
		if err := mod.ApplySchedAction(action); err != nil {
			fails = append(fails, text+"："+err.Error())
			if schedLogAllowed(t.ID) {
				logger.Warn("schedule action failed",
					zap.String("task", t.ID), zap.Int("modem", mod.ID),
					zap.String("phase", phase), zap.String("action", text), zap.Error(err))
			}
			continue
		}
		done++
		logger.Info("schedule action applied",
			zap.String("task", t.ID), zap.String("name", t.Name), zap.Int("modem", mod.ID),
			zap.String("phase", phase), zap.String("action", text))
	}

	// 只要有动作真的下发成功，网络状态就可能变了 → 推一次让其它页面同步
	if done > 0 {
		pushNetworkAfterParamChange(mod)
	}
	switch {
	case len(fails) == 0:
		run.OK = true
		if len(actions) == 1 {
			run.Msg = "已执行"
		} else {
			run.Msg = fmt.Sprintf("已执行 %d 个动作：%s", done, ActionsLabel(actions))
		}
	case done > 0:
		run.Msg = fmt.Sprintf("已执行 %d/%d 个动作；失败：%s", done, len(actions), strings.Join(fails, "；"))
	default:
		run.Msg = strings.Join(fails, "；")
	}
	return run
}

// RunSchedTaskNow 手动执行一次（界面的「立即执行」按钮），不推进槽位记录。
func RunSchedTaskNow(mgr *ModemManager, id string) (*Run, error) {
	var task *Task
	for _, t := range schedSnapshot() {
		if t.ID == id {
			task = t
			break
		}
	}
	if task == nil {
		return nil, fmt.Errorf("任务不存在")
	}
	mod := schedResolve(mgr, task)
	if mod == nil {
		return nil, fmt.Errorf("目标模组当前不在线")
	}
	if !mod.isRunningNow() {
		return nil, fmt.Errorf("模组未运行（串口未打开）")
	}
	run := runSchedPhase(mod, task, PhaseStart)
	schedRecord(id, run)
	pushSchedule(mgr)
	FlushSchedule()
	return &run, nil
}

// ------------------------------------------------------------------ 视图 ---

// ---- from: modem_schedule_view.go ----

// scheduleView 组装视图
