package kernel

// ====================== PDP / IMS 自动激活 ======================
//
// 需求（用户）：「APN 要自动激活对应的 PDP 和 ims」。
//
// 两件事：
//  ① 写完 APN（「参数设置」→ 写入 APN）就**自动激活**这个 cid 的 PDP ——
//     以前只写配置、要用户再点一次「立即重新激活」，而 AT+CGDCONT 改完不重新激活等于没生效；
//  ② 顺带把模组的 **IMS 上下文**也激活（APN = ims，多数固件固定 cid=1）——
//     IMS 是 VoLTE / 走 IMS 的短信（5G SA 下常见）的先决条件，而不少卡/固件出厂时它是未激活的。
//
// 为什么"自动做"是安全的：
//   · 走的是既有的 SetPDPActive（cid 校验 / 配额限制 / 先断后连 / 回读确认都在里面）；
//   · IMS 那一步**只可能是"额外激活"**，失败就在这里被吃掉、写进步骤与日志，绝不影响拨号结果
//     （实测 FM350 拒绝手动激活 IMS：`AT+CGACT=1,1` 直接 ERROR）。
//
// 开关：`[dial] activate_ims`（不写默认 true；要关就显式写 false）。

import (
	"modemd/module/config"
	"sort"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// ActivateIMSEnabled 配置开关（不写默认 **开**，见 config.go 的 ApplyDefaults）。
//
// ⚠️ 这里不是裸读 `Cfg.Dial.ActivateIMS`：配置结构体在**没走 Load()** 的场景
// （单元测试、直接构造 Config）里字段是零值 false，裸读会把"默认开"读成"关"。
// 所以让 Load 记下"用户是否显式写过这个键"，没写过就按默认（开）算。
func ActivateIMSEnabled() bool {
	if config.Cfg.Dial.ActivateIMSSet {
		return config.Cfg.Dial.ActivateIMS
	}
	return true
}

// IMSContextCIDs 从上下文列表里挑出「IMS 上下文」的 cid（去重、升序）。
//
// 判据（纯逻辑，便于单测）：
//
//  1. APN 就是 ims 的各种写法：`ims` / `IMS` / `ims.`（有的卡写 `ims.apn`）——
//     以 `ims` 开头且后面不是字母数字（避免把 `imsfoo` 这种普通 APN 误判）；
//  2. 内部上下文（AT+CGDCONT? 里被标成 IMS/sos）且 **cid == 1**、APN 为空 ——
//     有些固件 IMS 那条不写 APN；3GPP 的惯例是 cid 1 留给 IMS。
//     ⚠️ 只有 cid==1 才这么认：别的空 APN 上下文只是"没配置"，不能当 IMS。
//
// 找不到就返回空切片（调用方据此跳过，不猜别的 cid）。
func IMSContextCIDs(ctxs []Context) []int {
	seen := map[int]bool{}
	var out []int
	for _, c := range ctxs {
		hit := false
		if imsAPNName(c.APN) {
			hit = true
		} else if c.CID == 1 && strings.TrimSpace(c.APN) == "" && c.Internal {
			hit = true
		}
		if !hit || seen[c.CID] {
			continue
		}
		seen[c.CID] = true
		out = append(out, c.CID)
	}
	sort.Ints(out)
	return out
}

// imsAPNName APN 名字看起来是 IMS：`ims` / `IMS` / `ims.apn` / `ims-xxx`。
// 以 ims 开头，且紧接着不是字母数字（`imsfoo` 不算，防止误伤普通 APN）。
func imsAPNName(apn string) bool {
	s := strings.ToLower(strings.TrimSpace(apn))
	if !strings.HasPrefix(s, "ims") {
		return false
	}
	rest := s[len("ims"):]
	if rest == "" {
		return true
	}
	c := rest[0]
	return !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9')
}

// IMSActivateResult 一次"顺带激活 IMS"的结果（给步骤/日志用）
type IMSActivateResult struct {
	// Status: "ok"（激活成功或本来就在激活状态）/ "skip"（没有 IMS 上下文，跳过）/
	//         "fail"（模组拒绝或回读不一致）
	Status string
	// Note 一行说明（写进拨号步骤 / 接口响应，已带 cid）
	Note string
}

// ActivateIMS 把 IMS 上下文激活起来（拨号后 / 写完 APN 后调用）。
//
// ctxs 传调用方**刚读到**的上下文列表（省一条 AT+CGDCONT?）。
// 已经是激活状态的不再发指令；失败只记日志 + 返回 fail，**绝不返回 error 打断上层**。
func (mod *Modem) ActivateIMS(ctxs []Context) IMSActivateResult {
	cids := IMSContextCIDs(ctxs)
	if len(cids) == 0 {
		return IMSActivateResult{Status: "skip", Note: "模组没上报 IMS 上下文（跳过）"}
	}
	var done, failed, skipped []string
	for _, cid := range cids {
		if c := ByCID(ctxs, cid); c != nil && c.Active {
			skipped = append(skipped, strconv.Itoa(cid))
			continue
		}
		if _, err := mod.SetPDPActive(cid, true, false); err != nil {
			logger.Warn("activate IMS context failed",
				zap.Int("modem", mod.ID), zap.Int("cid", cid), zap.Error(err))
			failed = append(failed, "cid="+strconv.Itoa(cid)+"（"+err.Error()+"）")
			continue
		}
		logger.Info("IMS context activated", zap.Int("modem", mod.ID), zap.Int("cid", cid))
		done = append(done, strconv.Itoa(cid))
	}

	switch {
	case len(failed) > 0 && len(done) == 0 && len(skipped) == 0:
		return IMSActivateResult{Status: "fail", Note: "IMS 激活失败：" + strings.Join(failed, "；") +
			"（有些固件/卡不许手动激活 IMS，不影响数据上网）"}
	case len(failed) > 0:
		return IMSActivateResult{Status: "ok", Note: "IMS 部分成功（已激活 cid=" + strings.Join(done, ",") +
			"；失败：" + strings.Join(failed, "；") + "）"}
	case len(done) > 0:
		return IMSActivateResult{Status: "ok", Note: "已激活 cid=" + strings.Join(done, ",")}
	default:
		return IMSActivateResult{Status: "ok", Note: "已激活（cid=" + strings.Join(skipped, ",") + "）"}
	}
}
