package kernel

import (
	"errors"
	"go.uber.org/zap"
	"strings"
	"time"

	"modemd/module/common"
	"modemd/module/fibocom"
	"modemd/module/foxconn"
	"modemd/module/gosuncn"
	"modemd/module/huawei"
	"modemd/module/meig"
	"modemd/module/modedialect"
	"modemd/module/neoway"
	"modemd/module/openluat"
	"modemd/module/quectel"
	"modemd/module/sierra"
	"modemd/module/simcom"
	"modemd/module/telit"
	"modemd/module/thales"
	"modemd/module/unisoc"
)

// ====================== 组网模式：挨个问（未知厂商 / 写死的映射对不上时） ======================
//
// 为什么要这一层（用户报“好多模块不支持”）：写死的“厂商 → 方言”映射总有对不上的时候 ——
// 厂商标识认不出来、型号不在清单里、或者**固件根本不认那家的指令**（同一家不同平台都可能有差别，
// 比如同一句 AT+QNWPREFCFG 在高通平台与展锐/海思平台的固件上不一定都实现）。
// 那就直接问，按候选顺序发查询，**第一条能解出合法取值的就采用**。

// errModePrefRejected 模组明确拒了这条指令（回 ERROR），或应答里没有本机认识的那一行 ⇒
// “这条指令不是它家的”，值得换别的方言试（见 QueryModePrefCaps）。“发不出去/超时”不包在内。
var errModePrefRejected = errors.New("mode pref command rejected")

// modeProbeTTL 「挨个问」结果的缓存时长：一组探测要发十几条指令（慢总线上一条好几秒），
// 不能每次读都来一遍；但如果真的换模组/换固件了，用户点「读当前值」会清缓存重试。
const modeProbeTTL = 10 * time.Minute

// modeProbeNote 探测出来的方言在界面上的开头说明（告诉用户“这不是清单里写好的”）
const modeProbeNote = "本机不认厂商预设的组网模式指令，以下指令是**挨个试**出来的（自动识别）；"

// cachedModeProbe 取缓存的探测结果：(方言, 试过的指令, 缓存是否有效)；方言为 nil = 上次一圈都不认
func (mod *Modem) cachedModeProbe() (*modedialect.Dialect, []string, bool) {
	mod.modeProbeMu.Lock()
	defer mod.modeProbeMu.Unlock()
	if mod.modeProbeAt.IsZero() || time.Since(mod.modeProbeAt) > modeProbeTTL {
		return nil, nil, false
	}
	return mod.modeProbeD, mod.modeProbeTried, true
}

// storeModeProbe 记下探测结果（d == nil = 都不认，也缓存 —— 免得每次读都重试十几条）
func (mod *Modem) storeModeProbe(d *modedialect.Dialect, tried []string) {
	mod.modeProbeMu.Lock()
	mod.modeProbeAt, mod.modeProbeD, mod.modeProbeTried = time.Now(), d, tried
	mod.modeProbeMu.Unlock()
}

// clearModeProbe 清掉探测缓存（用户点「读当前值」时走这里）
func (mod *Modem) clearModeProbe() {
	mod.modeProbeMu.Lock()
	mod.modeProbeAt, mod.modeProbeD, mod.modeProbeTried = time.Time{}, nil, nil
	mod.modeProbeMu.Unlock()
}

// modeDialectCandidates 候选方言（探测顺序）：
//
//	① 厂商/清单/型号名推出来的那个排最前（它是“有依据”的，真能答就直接用它）；
//	② 其余按“市面上常见的程度”排（高通→移远/芯讯通/广和通/高新兴，海思→华为，展锐→展锐）。
//
// ⚠️ 查询指令相同的候选会被跳过一个（两家都有 AT^SYSCFGEX：华为 5G 码 08 / 美格 5G 码 04）——
// 谁排前面就用谁的码表，所以顺序很重要（modeProbeAccept 还会再按码表交叉核对一道）。
func modeDialectCandidates(mod *Modem) []*modedialect.Dialect {
	if mod == nil {
		return nil
	}
	primary := modeDialectFor(mod)
	all := []*modedialect.Dialect{
		quectel.ModePrefNR(), simcom.Mode(), fibocom.Mode(), huawei.Mode(), gosuncn.Mode(true),
		unisoc.Mode(), meig.Mode(), sierra.Mode(), thales.Mode(), telit.Mode(), foxconn.Mode(),
		neoway.Mode(), openluat.Mode(), gosuncn.Mode(false), quectel.ModePrefLTE(),
	}
	out := make([]*modedialect.Dialect, 0, len(all)+1)
	if primary != nil {
		out = append(out, primary)
	}
	for _, d := range all {
		if d == nil || d.Query == "" {
			continue
		}
		// ⚠️ 只跳过"和 primary 同一个查询口"的候选（没必要再问一次）；**不能按查询指令去重** ——
		// 华为与美格的组网模式都是 `AT^SYSCFGEX?`（同一个查询口、**5G 码不同**：08 vs 04），
		// 两家都得留在候选里，靠 modeProbeAccept 用各自的码表交叉核对来分谁是谁。
		if primary != nil && d.Query == primary.Query {
			continue
		}
		out = append(out, d)
	}
	return out
}

// modeProbeAccept 探测时封定“这条应答是不是它家的”：
//
//	① 应答不能是 ERROR；
//	② 解出来的值必须是**语义档位**，或**本方言码表里认识的码**；
//	③ 两样都不是 ⇒ 不认（例：美格的 5G 码 04 在华为的码表里没有；华为的 08 在美格那边也没有 ——
//	   两家都是 AT^SYSCFGEX:"00",… 的格式，光看“有应答”分不出来，得用码表交叉核对）。
func modeProbeAccept(d *modedialect.Dialect, resp []string) (string, bool) {
	if d == nil || d.Parse == nil || common.RespHasError(resp) {
		return "", false
	}
	v, ok := d.Parse(resp)
	v = strings.TrimSpace(v)
	if !ok || v == "" {
		return "", false
	}
	if modedialect.IsModePrefSemantic(v) {
		return v, true
	}
	if d.CodeName != nil {
		if d.CodeName(v) != "" {
			return v, true
		}
		return "", false
	}
	// 没码表的方言（如展锐 5G 开关）：取值形态对就算
	if modedialect.LooksLikeModeCode(v) {
		return v, true
	}
	return "", false
}

// probeModeDialect 挨个问：按候选顺序发查询，第一条能解出合法取值的就采用。
//
// 返回（命中的方言, 现读到的能力, 发过的查询指令）；都不认时方言为 nil（tried 仍会给出）。
func (mod *Modem) probeModeDialect() (*modedialect.Dialect, *ModePrefCaps, []string) {
	cands := modeDialectCandidates(mod)
	tried := make([]string, 0, len(cands))
	for _, d := range cands {
		resp, err := mod.SendAT(d.Query)
		tried = append(tried, d.Query)
		if err != nil {
			continue
		}
		v, ok := modeProbeAccept(d, resp)
		if !ok {
			continue
		}
		caps := &ModePrefCaps{
			Value: v, Name: modedialect.ModeLabel(v), Cmd: d.Query,
			Note: modeProbeNote + d.Note, Freeform: len(d.Choices) == 0,
			ReadOnly: d.ReadOnly, Auto: true,
		}
		caps.Choices = mod.modePrefChoices(d, v)
		logger.Info("mode pref dialect detected by probing",
			zap.Int("modem", mod.ID), zap.String("cmd", d.Query),
			zap.String("value", v), zap.Int("tried", len(tried)))
		return d, caps, tried
	}
	logger.Warn("no mode pref dialect answered",
		zap.Int("modem", mod.ID), zap.Strings("tried", tried))
	return nil, nil, tried
}
