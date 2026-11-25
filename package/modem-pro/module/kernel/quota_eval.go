package kernel

import (
	"time"

	"go.uber.org/zap"
)

// ====================== 评估 + 采样 ======================

// ---- from: modem_quota_loop.go ----

// quotaEvaluate 对照上限决定"该不该断网"，并保证状态与配置一致。
// 返回是否发生了状态变化（true 时前端会收到推送）。
//
// 会发 AT（只在启用配额或已处于限制态时才有实际动作）：
//   - 已超限 + 动作为断网 + 还没断 → 断
//   - 不再超限 / 改成只提醒 / 本周期解除限制 → 恢复
//   - 已经断了 → 每轮确认一下没被系统重新拨上来（重新激活就再断一次）

// quotaEvaluate 对照上限决定"该不该断网"，并保证状态与配置一致。
// 返回是否发生了状态变化（true 时前端会收到推送）。
//
// 会发 AT（只在启用配额或已处于限制态时才有实际动作）：
//   - 已超限 + 动作为断网 + 还没断 → 断
//   - 不再超限 / 改成只提醒 / 本周期解除限制 → 恢复
//   - 已经断了 → 每轮确认一下没被系统重新拨上来（重新激活就再断一次）
func (mod *Modem) quotaEvaluate() bool {
	st := quotaState(mod)
	now := time.Now()

	// 周期滚动：到新周期就把用量归零（用量是"本周期"的）
	if period := Period(now, st.ResetDay); period != st.Period {
		old := st.Period
		st = quotaUpdate(mod, func(s *State) {
			s.Period = period
			s.UsedUp, s.UsedDown = 0, 0
			s.Suspended = false // 新周期重新开始计
		})
		logger.Info("quota period rollover", zap.Int("modem", mod.ID),
			zap.String("from", old), zap.String("to", period), zap.Int("reset_day", st.ResetDay))
	}

	wantBlock := st.Enabled && st.Action != ActionWarn && st.Over() && !st.Suspended

	if st.Blocked && !wantBlock {
		if err := mod.setQuotaBlock(false, st.BlockedAction()); err != nil {
			logger.Warn("quota resume failed",
				zap.Int("modem", mod.ID), zap.Error(err))
			return false
		}
		st = quotaUpdate(mod, func(s *State) { s.Blocked = false; s.BlockedBy = "" })
		FlushQuota()
		pushQuota(mod, true)
		return true
	}
	if wantBlock && !st.Blocked {
		if !quotaStoreInst.TryAllowed(mod.ID) {
			return false // 上一步刚试过，别秒级重试刷屏
		}
		if err := mod.setQuotaBlock(true, st.Action); err != nil {
			logger.Warn("quota block failed",
				zap.Int("modem", mod.ID), zap.String("action", st.Action), zap.Error(err))
			return false
		}
		st = quotaUpdate(mod, func(s *State) { s.Blocked = true; s.BlockedAt = time.Now(); s.BlockedBy = s.Action })
		FlushQuota()
		pushQuota(mod, true)
		return true
	}
	if wantBlock && st.Blocked {
		// 限制期间又被重新拨上了（这台模组会自动重拨）→ 再断一次。
		// 节流：1 秒的采样循环也会走到这里，不能每次都发 AT。
		if !quotaStoreInst.CheckAllowed(mod.ID) {
			return false
		}
		reapply := false
		switch {
		case st.Action == ActionBlockRF:
			// ⚠️ 不发 AT+CFUN?（会掉模块，见 core.go 的 cfunLast 注释）—— 读缓存
			if fun := mod.CFUNCached(); fun != 0 && fun != CFUNAirplane {
				reapply = true
			} else if fun == 0 && !mod.AirplaneOn() {
				reapply = true // 还没读到过：按标志判
			}
		case modemUsesHostDialer(mod):
			// QMI：数据面归主机侧拨号器 —— 它又被拉起来（比如有人手动拨号）就再停一次
			reapply = HostDialStatusFor(mod.ID) != ""
		case len(mod.quotaActiveCIDs()) > 0:
			reapply = true
		}
		if reapply {
			if err := mod.setQuotaBlock(true, st.Action); err == nil {
				logger.Warn("quota: link came back during block, blocked again",
					zap.Int("modem", mod.ID), zap.String("action", st.Action))
			}
		}
	}
	return false
}

// QuotaTick 采样一次流量计数器并评估配额（QuotaLoop 每轮调用）。
// 未启用且没有待恢复的限制时直接返回，不白发 AT。
func (mod *Modem) QuotaTick() {
	st := quotaState(mod)
	if !st.Enabled && !st.Blocked {
		return
	}
	if st.Enabled && mod.trafficSupported() {
		mod.SampleTraffic() // 差分增量在 applyTrafficSample 里计入配额
	}
	if mod.quotaEvaluate() {
		return
	}
	pushQuota(mod, false)
}

// quotaAfterSample 在"有客户端在看"的 1 秒采样循环里顺带评估一次，
// 让超限反应更快（不用等到下一个 60 秒的 QuotaLoop）。
func (mod *Modem) quotaAfterSample() {
	st := quotaState(mod)
	if !st.Enabled && !st.Blocked {
		return
	}
	if mod.quotaEvaluate() {
		return
	}
	pushQuota(mod, false)
}
