package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"fmt"
	"modemd/module/common"
	"modemd/module/config"
	"strings"

	"go.uber.org/zap"
)

// ---- from: modem_schedule.go ----

// ---- from: modem_schedule.go ----
//
// 任务/动作模型、校验、时间判定与落盘在 schedule.go（纯逻辑）；
// 这里管"到点了怎么执行"：解析目标模组 → 发对应的 AT → 记录结果 → 推送。
//
// 任务/动作模型、校验、时间判定与落盘在 schedule.go（纯逻辑）；
// 这里管"到点了怎么执行"：解析目标模组 → 发对应的 AT → 记录结果 → 推送。

var schedStoreInst = NewSchedStore()

// InitSchedule 载入 json（main 启动时调用一次）
func InitSchedule() {
	schedStoreInst.Init(Path(config.ConfigPath))
}

// FlushSchedule 强制落盘（进程退出前 / 状态变化后调用）
func FlushSchedule() { schedStoreInst.Flush() }

// schedSnapshot 任务快照（加锁拷贝，供 API / 循环使用）
func schedSnapshot() []*Task { return schedStoreInst.Snapshot() }

// schedUpsert 新增/更新一条任务
func schedUpsert(t *Task) error { return schedStoreInst.Upsert(t) }

// schedDelete 删除任务
func schedDelete(id string) bool { return schedStoreInst.Delete(id) }

// schedRecord 记录一次执行结果
func schedRecord(id string, run Run) { schedStoreInst.Record(id, run) }

// schedLogAllowed 日志节流
func schedLogAllowed(id string) bool { return schedStoreInst.LogAllowed(id) }

// ------------------------------------------------------------------ 执行 ---

// ApplySchedAction 执行一个定时动作。所有分支都复用已有的锁网 / 飞行模式实现。
func (mod *Modem) ApplySchedAction(a Action) error {
	switch a.Kind {
	case KindBandLock:
		tech, err := ParseLockTech(a.Tech)
		if err != nil {
			return err
		}
		bands, _ := common.ParseBandList(a.Bands)
		_, err = mod.SetBandLock(tech, bands)
		return err

	case KindBandUnlock:
		tech, err := ParseLockTech(a.Tech)
		if err != nil {
			return err
		}
		l := mod.lockerFor()
		if l == nil {
			return errLockUnsupported(mod.Vendor)
		}
		return mod.unlockBand(l, tech)

	case KindCellLock:
		tech, err := ParseLockTech(a.Tech)
		if err != nil {
			return err
		}
		_, err = mod.LockCell(CellLockRequest{
			Tech: tech, ARFCN: a.ARFCN, PCID: a.PCID, LockType: a.LockType,
		}, a.Persist)
		return err

	case KindCellUnlock:
		tech, err := ParseLockTech(a.Tech)
		if err != nil {
			return err
		}
		_, err = mod.LockCell(CellLockRequest{Tech: tech, LockType: CellLockNone}, false)
		return err

	case KindUnlockAll:
		_, err := mod.UnlockAll()
		return err

	case KindModePref:
		// 走**统一的组网模式引擎**（各家方言 + 全部档位 + 本机原码），
		// 不再只限 AUTO/LTE/NR5G/LTE:NR5G 四档（那套旧路径会把新增档位拒掉）
		_, err := mod.SetModePrefCaps(a.Mode)
		return err

	case KindAirplaneOn:
		_, err := mod.SetAirplane(true)
		return err

	case KindAirplaneOff:
		_, err := mod.SetAirplane(false)
		return err
	}
	return fmt.Errorf("未知动作类型 %q", a.Kind)
}

// schedActionSupported 该模组的固件是否支持这个动作（用于界面提前提示 + 执行前拦截）
func schedActionSupported(mod *Modem, kind string) error {
	info, ok := KindInfoFor(kind)
	if !ok {
		return fmt.Errorf("未知动作类型 %q", kind)
	}
	if info.NeedsLocker && mod.lockerFor() == nil {
		return fmt.Errorf("模组 %s（%s）的固件不支持锁网指令，无法执行「%s」",
			mod.ATPort, mod.Vendor, info.Label)
	}
	// 组网模式看的是 mode 方言（与锁网方言是两套表）
	if info.NeedsMode && modeDialectFor(mod) == nil {
		return fmt.Errorf("模组 %s（%s）未识别出组网模式指令，无法执行「%s」",
			mod.ATPort, mod.Vendor, info.Label)
	}
	// 机型根本不支持开飞行模式的（F50：模组射频就是整机唯一上行，关了连管理/内网
	// 通道一起断 —— 见 AirplaneBlockedReason），“开飞行模式”类任务要在列表里
	// 提前标成“机型不支持”；“关飞行模式”仍允许（它是恢复手段）。
	if kind == KindAirplaneOn {
		if why := mod.AirplaneBlockedReason(); why != "" {
			return fmt.Errorf("模组 %s（%s）：%s", mod.ATPort, mod.Vendor, why)
		}
	}
	return nil
}

// ------------------------------------------------------------------ 目标模组 ---

// schedResolve 把任务的目标解析成在线模组。
// 先按端口精确匹配；端口变了（换 USB 口）时，若 IMEI 唯一匹配则迁移键。
func schedResolve(mgr *ModemManager, t *Task) *Modem {
	if mgr == nil {
		return nil
	}
	list := mgr.listModems()
	for _, m := range list {
		if quotaKey(m) == t.ModemKey {
			return m
		}
	}
	if t.IMEI == "" {
		return nil
	}
	var hit *Modem
	n := 0
	for _, m := range list {
		if strings.TrimSpace(m.IMEI) == t.IMEI {
			hit, n = m, n+1
		}
	}
	if n != 1 {
		// 两台模组 IMEI 相同时不猜（宁可不执行，也不能锁错模组）
		return nil
	}
	oldKey := t.ModemKey
	t.ModemKey = quotaKey(hit)
	logger.Info("schedule task retargeted",
		zap.String("id", t.ID), zap.String("from", oldKey), zap.String("to", t.ModemKey))
	schedStoreInst.Retarget(t.ID, t.ModemKey)
	return hit
}

// ------------------------------------------------------------------ 循环 ---

// SchedKindList 供 API 直接返回动作类型元信息
func SchedKindList() []KindInfo { return Kinds() }

// newSchedID 任务 id
func newSchedID() string { return NewID() }

// schedFillTarget 用目标模组补全 Port / IMEI（仅展示与换端口迁移用）
func schedFillTarget(mgr *ModemManager, t *Task, modemID int) {
	var mod *Modem
	if mgr != nil {
		if t.ModemKey != "" {
			for _, m := range mgr.listModems() {
				if quotaKey(m) == t.ModemKey {
					mod = m
					break
				}
			}
		}
		if mod == nil && t.ModemKey == "" {
			// 前端没给目标时，回退到请求里的 modem_id
			if m, err := modemByID(mgr, modemID); err == nil {
				mod = m
				t.ModemKey = quotaKey(m)
			}
		}
	}
	if mod != nil {
		t.Port = mod.ATPort
		t.IMEI = mod.IMEI
	}
}

// ---- from: modem_schedule_loop.go ----

// ScheduleLoop 定时器循环。
//
// ⚠️ 和配额一样**不看有没有客户端在线**：定时动作（锁频段/锁基站）必须到点就执行。
// 启动时先跑一轮，把"进程重启前几分钟内该做但还没来得及做"的槽位补上。
