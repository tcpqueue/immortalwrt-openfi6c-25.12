package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"fmt"
	"modemd/module/common"
	"time"

	"go.uber.org/zap"
)

// ---- from: modem_traffic.go ----

//
// 解析 / 格式化在 data.go（纯逻辑，不认识 Modem）；这里管三件事：
//   - 采样节奏：发哪条指令（厂商私有，未声明的厂商不发）；
//   - 状态：上一次采样、缓存速率、积分量（都挂在 Modem 上，和串口共用锁）；
//   - 与配额计量的衔接：在**算差分的地方**顺手累计字节（全进程只有这一处）。
// SampleTraffic 采样一次流量统计并更新速率（返回最近一次结果）。
// 采样失败/指令不可用时返回上一次已知结果，不会清空成 0。

func (mod *Modem) SampleTraffic() Rate {
	// 展锐 SIPC（F50）这类厂商托管数据面：速率直接读**本机网卡计数器**，不发 AT
	//（见 filestate.go；它的 sipa_ethN 就是 PDP 主承载）。
	// ⚠️ 只有这一族会进这个分支（判据里第一道门就是 SIPC 通道名）。
	if mu300FileBackedState(mod) {
		if r, ok := mod.SampleTrafficFromHost(); ok {
			return r
		}
	}
	cmd := mod.resolveVendorCmd(KindTraffic)
	if cmd == "" {
		// 该厂商未声明流量指令：不猜测、不发未验证指令
		return Rate{}
	}
	mod.enableTrafficCounter()
	resp, err := mod.SendAT(cmd)
	if err != nil {
		logger.Debug("sample traffic failed", zap.Int("modem", mod.ID), zap.Error(err))
		return mod.TrafficSnapshot()
	}
	now := time.Now()
	s, ok := ParseTraffic(resp)
	if !ok {
		return mod.TrafficSnapshot()
	}
	// 速率式模组（广和通 AT+GTSTATIS?：直接给 byte/s，没有累计计数器）
	if s.RateMode {
		return mod.applyTrafficRates(s.UpBps, s.DownBps, now)
	}
	return mod.applyTrafficSample(s.Sent, s.Recv, now)
}

// applyTrafficRates 用模组**直接上报的瞬时速率**更新（没有累计计数器的模组，如广和通 AT+GTSTATIS?）。
//
// 累计量只能靠"速率 × 时间窗"积分得到，所以它是**估算值**（Estimated=true，界面标注）；
// 流量配额计量也用它 —— 与计数式模组相比精度差一些，但至少能拦住"用超了"。
// 采样间隔越接近标称值越准；跨重启不保留（与计数式模组的累计量行为一致）。
func (mod *Modem) applyTrafficRates(up, down float64, now time.Time) Rate {
	mod.mu.Lock()
	defer mod.mu.Unlock()

	rate := Rate{
		Supported: true,
		UpBps:     up,
		DownBps:   down,
		SampledAt: now,
		Estimated: true,
	}
	if prev := mod.trafficPrev; prev != nil && prev.RateMode {
		dt := now.Sub(prev.At).Seconds()
		if dt >= 0.5 {
			rate.Interval = dt
			// 积分成字节：速率 × 时间窗。放在这里是为了和计数式模组一样，
			// 全进程只有一处累加（1 秒的速率循环与 60 秒的配额循环不会重复计数）。
			upBytes := uint64(up * dt)
			downBytes := uint64(down * dt)
			mod.trafficIntegUp += upBytes
			mod.trafficIntegDown += downBytes
			quotaAddBytes(mod, upBytes, downBytes)
		} else {
			rate.Interval = mod.trafficRate.Interval
		}
	}
	rate.Sent = mod.trafficIntegUp
	rate.Recv = mod.trafficIntegDown
	rate.UpText = FormatRate(rate.UpBps)
	rate.DownText = FormatRate(rate.DownBps)
	mod.trafficPrev = &TrafficSample{At: now, RateMode: true, UpBps: up, DownBps: down}
	mod.trafficRate = rate
	return rate
}

// applyTrafficSample 用一次采样更新速率（与上一次采样做差分）
func (mod *Modem) applyTrafficSample(sent, recv uint64, now time.Time) Rate {
	mod.mu.Lock()
	defer mod.mu.Unlock()

	rate := Rate{
		Supported: true,
		Sent:      sent,
		Recv:      recv,
		SampledAt: now,
	}
	if prev := mod.trafficPrev; prev != nil {
		// 配额计量：**在算差分的地方顺手累计**（全进程只有这里在算计数器差分，
		// 所以 1 秒的速率循环和 60 秒的配额循环不会重复计数）。
		// 注意要放在下面 dt>=0.5 判断之前：时间窗太短时速率沿用上一轮，
		// 但字节数照样涨了，漏掉就会少算流量。
		if sent >= prev.Sent && recv >= prev.Recv {
			quotaAddBytes(mod, sent-prev.Sent, recv-prev.Recv)
		}
		dt := now.Sub(prev.At).Seconds()
		// 时间窗太短会被计数器粒度放大，直接沿用上一次的速率
		if dt >= 0.5 {
			rate.Interval = dt
			if sent >= prev.Sent && recv >= prev.Recv {
				rate.UpBps = float64(sent-prev.Sent) / dt
				rate.DownBps = float64(recv-prev.Recv) / dt
			}
			// 计数器回退（模组侧 PDP 重建）→ 本次速率为 0，只重置基线
		} else if mod.trafficRate.Supported {
			rate.UpBps = mod.trafficRate.UpBps
			rate.DownBps = mod.trafficRate.DownBps
			rate.Interval = mod.trafficRate.Interval
		}
	}
	rate.UpText = FormatRate(rate.UpBps)
	rate.DownText = FormatRate(rate.DownBps)
	mod.trafficPrev = &TrafficSample{Sent: sent, Recv: recv, At: now}
	mod.trafficRate = rate
	return rate
}

// TrafficSnapshot 返回最近一次速率（不触发 AT 查询）
func (mod *Modem) TrafficSnapshot() Rate {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	return mod.trafficRate
}

// clearTrafficBaseline 清掉本地差分基线、缓存速率、积分量与就绪标记。
// 计数器被清零（清零累计流量）或链路重建（PDP 重新激活）后都要调，
// 否则下一轮采样会把"计数器重来"当成一次暴涨。
func clearTrafficBaseline(mod *Modem) {
	mod.mu.Lock()
	mod.trafficPrev = nil
	mod.trafficRate = Rate{}
	mod.trafficIntegUp, mod.trafficIntegDown = 0, 0
	mod.trafficReady = true
	mod.mu.Unlock()
}

// ResetTraffic 清零累计流量并立刻重新开始统计。
//
// 计数式模组（移远）：`AT+QGDCNT=0` 会**停止计数并清零**，所以要紧接着再发一次使能指令。
// 速率式模组（广和通 AT+GTSTATIS?）：模组侧**没有可清零的累计计数器**，
// 只能把本地的积分量/差分基线清掉（界面上的"累计"本来就是本地估算值）。
//
// 两种情况下都要清本地基线，否则下一轮采样会把"计数器从 0 涨回来"当成巨大的瞬时速率。
func (mod *Modem) ResetTraffic() (Rate, error) {
	cc, ok := mod.cmdImpl.(CommandCandidates)
	if !ok {
		return Rate{}, fmt.Errorf("vendor %s 未声明流量统计指令", mod.Vendor)
	}
	if mod.resolveVendorCmd(KindTraffic) == "" {
		return Rate{}, fmt.Errorf("vendor %s 未声明流量统计指令", mod.Vendor)
	}
	enable := cc.TrafficEnableCommand()
	if enable == "" {
		// 速率式模组：模组侧没有计数器可清，只清本地估算量
		// （返回一个"已支持 + 0 B/s"的结果，前端不会瞬间掉回"未上报"）
		clearTrafficBaseline(mod)
		logger.Info("traffic stats are rate-based, reset local estimate only",
			zap.Int("modem", mod.ID))
		return Rate{Supported: true, UpText: "0 B/s", DownText: "0 B/s", Estimated: true,
			SampledAt: time.Now()}, nil
	}
	// AT+QGDCNT=0：停止并清零
	resp, err := mod.SendAT("AT+QGDCNT=0")
	if err != nil {
		return Rate{}, err
	}
	if common.RespHasError(resp) {
		return Rate{}, fmt.Errorf("modem rejected AT+QGDCNT=0")
	}
	// 立刻恢复计数：计数器归零后不使能就不会再涨
	if _, err := mod.SendAT(enable); err != nil || common.RespHasError(resp) {
		logger.Warn("re-enable traffic counter failed", zap.Int("modem", mod.ID))
	}
	// 清掉本地基线与缓存速率，避免把"计数器回零"算成一次巨大波动
	clearTrafficBaseline(mod)
	logger.Info("traffic counter reset", zap.Int("modem", mod.ID))
	// 立刻采样一次，返回给用户一个新的起点
	return mod.SampleTraffic(), nil
}

// enableTrafficCounter 首次采样前下发一次"开始计数"指令。
// 只影响模组的计数开关，不改变网络/驻网状态；失败也不影响速率读取。
func (mod *Modem) enableTrafficCounter() {
	mod.mu.Lock()
	if mod.trafficReady {
		mod.mu.Unlock()
		return
	}
	// 只尝试一次：模组不支持时没必要每轮重试（否则每 5s 白发一条无效指令）
	mod.trafficReady = true
	mod.mu.Unlock()

	enable := ""
	if cc, ok := mod.cmdImpl.(CommandCandidates); ok {
		enable = cc.TrafficEnableCommand()
	}
	if enable == "" {
		return
	}
	resp, err := mod.SendAT(enable)
	if err != nil || common.RespHasError(resp) {
		logger.Debug("enable traffic counter failed",
			zap.Int("modem", mod.ID), zap.String("cmd", enable))
		return
	}
	logger.Info("traffic counter enabled", zap.Int("modem", mod.ID), zap.String("cmd", enable))
}

// ---- from: modem_params.go ----

//
// 校验 / 指令拼装 / 应答解析在 data.go（纯逻辑，含各家固件的实测坑）；
// 这里管流程：发 AT → 回读确认 → 必要时重试（AT 通道没有事务语义，
// 只有"写完再问一次"才能证明真的生效了）。
// QueryParams 读一遍可写参数：PDP 上下文（含激活状态）+ 当前在用 APN + 选网方式。
// 只读，不改模组状态。

func (mod *Modem) QueryParams() (*Status, error) {
	st := &Status{ModemID: mod.ID, QueriedAt: time.Now()}

	// PDP 相关指令用（可能被机型特例加长的）专用超时：实测 FM650 的 AT 通道会变慢，
	// 默认 3s 会在它还没回之前就超时（之后这条半截应答还会把通道越带越歪）。
	pdpTO := mod.pdpCommandTimeout(mod.Quirk())
	resp, err := mod.SendATTimeout("AT+CGDCONT?", pdpTO)
	if err != nil || common.RespHasError(resp) {
		if err == nil {
			err = fmt.Errorf("modem rejected AT+CGDCONT?")
		}
		return nil, err
	}
	st.Contexts = ParseCGDCONT(resp)

	// 激活状态：不是所有固件都支持 CGACT?，失败就当"未知"（全部 false），不影响展示配置
	if act, err := mod.SendATTimeout("AT+CGACT?", pdpTO); err == nil && !common.RespHasError(act) {
		active := ParseCGACT(act)
		for i := range st.Contexts {
			st.Contexts[i].Active = active[st.Contexts[i].CID]
		}
	}

	// 当前在用 APN：优先"已激活的数据上下文"，其次 cid 最小的数据上下文
	bestCID := 0
	for _, c := range st.Contexts {
		if c.Internal || c.APN == "" {
			continue
		}
		if c.Active {
			st.APN = c.APN
			bestCID = c.CID
			break
		}
		if bestCID == 0 || c.CID < bestCID {
			st.APN, bestCID = c.APN, c.CID
		}
	}
	if st.APN == "" {
		// 兜底：当前激活的 PDP（可能报的是内部 APN）
		if apn, _, err := mod.QueryAPN(); err == nil {
			st.APN = apn
		}
	}

	if resp, err := mod.SendAT("AT+COPS?"); err == nil && !common.RespHasError(resp) {
		if ns, ok := ParseCOPS(resp); ok {
			st.NetSelect = ns
		}
	} else {
		logger.Debug("query COPS failed", zap.Int("modem", mod.ID))
	}
	mod.mu.Lock()
	cached := *st
	cached.Contexts = append([]Context(nil), st.Contexts...)
	mod.paramsStatus = &cached
	mod.mu.Unlock()
	return st, nil
}

const paramsStatusCacheTTL = 20 * time.Second

func (mod *Modem) paramsStatusOrQuery(force bool) (*Status, error) {
	mod.paramsQueryMu.Lock()
	defer mod.paramsQueryMu.Unlock()
	if !force {
		mod.mu.Lock()
		st := mod.paramsStatus
		if st != nil {
			cached := *st
			cached.Contexts = append([]Context(nil), st.Contexts...)
			st = &cached
		}
		mod.mu.Unlock()
		if st != nil && !st.QueriedAt.IsZero() && time.Since(st.QueriedAt) <= paramsStatusCacheTTL {
			return st, nil
		}
	}
	return mod.QueryParams()
}
