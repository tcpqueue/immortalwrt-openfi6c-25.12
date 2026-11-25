package kernel

import (
	"modemd/module/atparse"
	"modemd/module/common"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ====================== 签约速率（AMBR，引擎侧的胶水） ======================
//
// 解析在 data.go（纯逻辑，含三条路径与字段口径说明）；
// 这里管两件与模组强相关的事：发哪条指令、TTL 缓存（挂在 Modem 上）。

// dialedPDP 本机「拨号的那条 PDP 上下文」——AMBR / CQI / QCI 一整套都只取它的值，
// IMS / sos 这类内部承载一律不取（同一台模组常同时报好几条，取错就会把 IMS 的档位显示出来）。
//
// 取法（**不发 AT**，只读现有状态，所以能安全地放在每秒一轮的查询里）：
//  1. 拨号状态里记着的 cid / APN（`DialStatus()`，「拨号上网」页拨过号后最可信）；
//  2. 自动组网记住的“上次拨成功的 cid”（`preferredDialCID`，见 netprov.go）；
//  3. **只读缓存里的 PDP 上下文**里“已激活且非内部”的那条（见 `cachedDataContext`）——
//     厂商托管数据面（展锐 SIPC / F50：PDP 是模组侧厂商守护拨的，modemd 自己没拨过号）
//     没有 ①② 可用，这一档就是“软件拨号的那一条”，也是**把 IMS 排除在外**的硬约束；
//  4. 兜底 `DefaultDataCID`。
//
// APN 拿不到就返回空串（只按 cid 匹配）。
func (mod *Modem) dialedPDP() (int, string) {
	if mod == nil {
		return DefaultDataCID, ""
	}
	st := mod.DialStatus()
	cid, apn := st.CID, strings.TrimSpace(st.APN)
	if cid <= 0 {
		cid = preferredDialCID(mod.ID)
	}
	if cid <= 0 {
		if c := mod.cachedDataContext(); c != nil {
			cid = c.CID
			apn = strings.TrimSpace(c.APN)
		}
	}
	if cid <= 0 {
		cid = DefaultDataCID
	}
	// 配置里的 APN 往往是空的（展锐 cid 1 就是 `+CGDCONT:1,IPV4V6,,…`），但**在用**的那个
	// 一定非空（`+CGCONTRDP=1` → ctnet）⇒ 用缓存的“当前在用 APN”补上，
	// 界面才能显示成「APN ctnet（cid 1）」：一眼看出取的是拨号那条，不是 IMS。
	if apn == "" {
		if c := mod.cachedDataContext(); c != nil && c.CID == cid {
			apn = mod.cachedCurrentAPN()
		}
	}
	return cid, apn
}

// cachedContexts 只读缓存里的 PDP 上下文（**绕不发 AT**）。
// 缓存不存在就返回 nil —— 本函数服务于每秒一轮的查询路径，不能在那里引入 AT。
func (mod *Modem) cachedContexts() []Context {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	if mod.paramsStatus == nil {
		return nil
	}
	return append([]Context(nil), mod.paramsStatus.Contexts...)
}

// cachedCurrentAPN 只读缓存里的“当前在用 APN”（`/api/params` 那份），不发 AT。
func (mod *Modem) cachedCurrentAPN() string {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	if mod.paramsStatus == nil {
		return ""
	}
	return strings.TrimSpace(mod.paramsStatus.APN)
}

// cachedDataContext 缓存里“已激活且非内部”的数据上下文 —— 也就是**软件拨号的那一条**。
//
// 为什么需要它：展锐 SIPC（中兴 F50）这类**厂商托管数据面**上，PDP 是模组侧厂商守护
// （`/opt/mu300/bin/mobile-data`）拨的，modemd 自己从没拨过号 ⇒ `DialStatus()` 里既没 cid
// 也没 APN。这时若直接退回 `DefaultDataCID`(1)，碰上“cid 1 其实是 IMS / 别的内部承载”的固件
// 就会把 **IMS 的档位**显示出来（用户 2026-10-06 明确要求：“IMS 不能算 —— 就是软件拨号的那一条”）。
//
// 多条时取 cid 最大的（与 netprov.AutoDialCandidates 的排序口径一致）。
func (mod *Modem) cachedDataContext() *Context {
	var best *Context
	for _, c := range mod.cachedContexts() {
		if !c.Active || c.Internal || c.CID <= 0 {
			continue
		}
		if best == nil || c.CID > best.CID {
			cc := c
			best = &cc
		}
	}
	return best
}

// QueryAMBR 查询签约速率；模组/厂商不支持时返回 nil（前端显示"未上报"）。
// 结果带 TTL 缓存，见 data.go 的说明。
//
// 取值顺序：① 厂商私有指令（移远 AT+QNWCFG="nr5g_ambr" / 鼎桥 AT^DSAMBR）→
// ② 3GPP 标准 AT+CGCONTRDP 的尾部 AMBR 扩展（部分固件才有）。
func (mod *Modem) QueryAMBR() *atparse.AMBRRate {
	mod.mu.Lock()
	cached, at := mod.ambrCache, mod.ambrCacheAt
	fallbackOff := mod.ambrFallbackOff
	mod.mu.Unlock()
	if cached != nil && time.Since(at) < TTL {
		return cached
	}

	rate := (*atparse.AMBRRate)(nil)
	// 拨号的那条 PDP 上下文：AMBR / CQI / QCI 只取它的值（见 dialedPDP）
	cid, apn := mod.dialedPDP()

	// ① 厂商私有指令（机型特例优先：美格 SRM 的 `AT^DSAMBR=<cid>`）
	if cmd := mod.ambrCommand(); cmd != "" {
		if resp, err := mod.SendAT(cmd); err != nil {
			logger.Debug("query AMBR failed", zap.Int("modem", mod.ID), zap.Error(err))
		} else if r, ok := parseAMBRForModem(mod, cmd, resp); ok {
			r.Source = cmd
			// 3GPP 的 C5GQOSRDP 应答里**没有 APN 字段**（只有 cid）⇒ 用拨号那条的 APN 补上，
			// 界面才能显示「APN ctnet（cid 1）」，一眼看出取的**不是** IMS。
			// ⚠️ 只在与选中承载同一个 cid（或应答没给 cid）时补，避免张冠李戴。
			if r.APN == "" && apn != "" && (r.CID == 0 || r.CID == cid) {
				r.APN = apn
			}
			rate = r
		}
	}

	// ② 退回标准 PDP 承载信息：只有带"尾部 AMBR 扩展"的固件才读得到
	if rate == nil && !fallbackOff {
		switch resp, err := mod.SendAT(common.CmdCGCONTRDP); {
		case err != nil:
			logger.Debug("query CGCONTRDP ambr failed", zap.Int("modem", mod.ID), zap.Error(err))
		case common.RespHasError(resp):
			// 该固件不认这条标准指令 → 停用，不再每轮白发一条
			mod.disableAMBRFallback()
		default:
			if r, ok := ParseCGCONTRDPContext(resp, cid, apn); ok {
				if r.DLMbps < r.ULMbps {
					// 字段顺序取自信任的文档（末两个 = 下行,上行），本机没有可对照的样本
					logger.Warn("CGCONTRDP ambr looks reversed (ul > dl), verify field order",
						zap.Int("modem", mod.ID), zap.Int("dl_mbps", r.DLMbps),
						zap.Int("ul_mbps", r.ULMbps), zap.String("apn", r.APN))
				}
				r.Source = common.CmdCGCONTRDP
				rate = r
			}
		}
	}

	if rate != nil {
		rate.QueriedAt = time.Now()
	}
	mod.mu.Lock()
	mod.ambrCache, mod.ambrCacheAt = rate, time.Now()
	mod.mu.Unlock()
	return rate
}

// disableAMBRFallback 记住"该固件不认 AT+CGCONTRDP"，避免每分钟白发一条无效指令。
func (mod *Modem) disableAMBRFallback() {
	mod.mu.Lock()
	mod.ambrFallbackOff = true
	mod.mu.Unlock()
	logger.Info("CGCONTRDP ambr fallback disabled", zap.Int("modem", mod.ID))
}

// ---- from: modem_ussd.go ----

//
// 解析/文案/码校验在 parse_test.go（纯逻辑）；这里只有与模组打交道的那几件：
// 读协程捕获 URC 后落库并推送、下发指令、SIM 状态判断。
// recordUSSD 由读协程调用：记下 URC 并推给在线客户端
