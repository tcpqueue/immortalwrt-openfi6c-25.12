package kernel

import (
	"errors"
	"fmt"
	"modemd/module/common"
	"modemd/module/modedialect"
	"strings"

	"go.uber.org/zap"
)

// ====================== 组网模式偏好 ======================

// ModePrefCaps 本机的组网模式偏好能力（界面用它渲染下拉）
type ModePrefCaps struct {
	Value string `json:"value"` // 当前值（语义档位，或未取证时的本机原码）
	Name  string `json:"name,omitempty"`
	Cmd   string `json:"cmd,omitempty"` // 查询指令
	// Choices 可选档位：已取证的语义档位（中文名 + 可写）；
	// 码表未取证的厂商会把**所有**语义档位列出来但标不可写，另外把「设备枚举/脚本取证的原码」列为可写。
	Choices []modedialect.ModeChoice `json:"choices,omitempty"`
	Note    string                   `json:"note,omitempty"`
	// Freeform 本机是不是"只能按原码填"（界面据此显示原码输入框）
	Freeform bool `json:"freeform,omitempty"`
	// ReadOnly 本机的组网模式**只能读**（取值表/写语法未取证，见 modedialect.Dialect.readOnly）。
	// 界面据此把写入控件置灰：给用户一个点了必定失败的按钮比不给更糟。
	ReadOnly bool `json:"read_only,omitempty"`
	// Auto 本机的方言是**挨个问**试出来的（厂商/清单都推不出来，或推出来的那条被模组拒了）。
	// 界面据此提示“指令是自动识别的、拿不准就到接口调试页核对”。
	Auto bool `json:"auto,omitempty"`
}

// QueryModePrefCaps 读当前组网模式偏好 + 本机可选档位（含中文名）
//
// 找方言的顺序（用户报“好多模块不支持”——以前只会第一步）：
//
//	① 厂商/清单/型号名推出来的那个（modeDialectFor）；
//	② 它被模组**拒了**（回 ERROR）或认不出取值 ⇒ 换“挨个问”：把各家的查询指令依次发一遍，
//	   第一条能解出合法取值的就采用（结果缓存 modeProbeTTL）；
//	③ 还不行 ⇒ 退回锁网方言里的只读查询（旧路径：移远/芯讯通/鼎桥），能显示就不能写；
//	④ 最后才如实报“本机未提供组网模式偏好指令”，并把②里试过的指令列出来。
func (mod *Modem) QueryModePrefCaps() (*ModePrefCaps, error) {
	d := modeDialectFor(mod)
	if d == nil {
		// 缓存里的探测结果（同一个模组 10 分钟内不重复挨个问）
		if cd, _, cached := mod.cachedModeProbe(); cached {
			d = cd
		}
	}
	if d != nil {
		caps, err := mod.queryModeCapsWith(d)
		if err == nil {
			return caps, nil
		}
		if !errors.Is(err, errModePrefRejected) {
			// 传输层错误（超时/打不开串口）：别浪费十几条指令，直接报
			return nil, err
		}
		logger.Info("mode pref command not accepted, probing other dialects",
			zap.Int("modem", mod.ID), zap.String("cmd", d.Query), zap.Error(err))
	}
	// ② 挨个问
	if pd, tried, cached := mod.cachedModeProbe(); cached && pd == nil {
		return nil, modePrefProbeError(tried)
	}
	pd, caps, tried := mod.probeModeDialect()
	mod.storeModeProbe(pd, tried)
	if pd != nil {
		return caps, nil
	}
	// ③ 退回锁网方言里的只读查询（旧路径：移远/芯讯通/鼎桥）
	if l := mod.lockerFor(); l != nil {
		if cmd := l.CommandQueryModePref(); cmd != "" {
			resp, err := mod.SendAT(cmd)
			if err != nil {
				return nil, err
			}
			if !common.RespHasError(resp) {
				c := &ModePrefCaps{Cmd: cmd, Freeform: true}
				if ops := lockVendorOpsOf(l); ops != nil {
					if v, ok := ops.ParseModePref(resp); ok {
						c.Value = v
					}
				}
				if c.Value == "" {
					c.Value = firstNonEmptyLine(resp)
				}
				c.Name = modedialect.ModeLabel(c.Value)
				c.Note = l.CommandQueryModePref() + "（该方言只提供只读查询）"
				return c, nil
			}
		}
	}
	if len(tried) > 0 {
		return nil, modePrefProbeError(tried)
	}
	return nil, fmt.Errorf("本机未提供组网模式偏好指令")
}

// modePrefProbeError 一圏都没认出来时的错误（把试过的指令列出来：用户和我们都靠它排查）
func modePrefProbeError(tried []string) error {
	if len(tried) == 0 {
		return fmt.Errorf("本机未提供组网模式偏好指令")
	}
	return fmt.Errorf("未找到本机的组网模式指令（已试过：%s）——\n可在「接口调试」页手工发这些指令看模组回什么",
		strings.Join(tried, "、"))
}

// queryModeCapsWith 用指定的方言读一次当前值 + 档位。
//
// 返回 errModePrefRejected（用 errors.Is 判）表示“**这条指令不是它家的**”：模组回了 ERROR，
// 或者回了 OK 但没有我们认识的那一行 —— 两种都值得换别的方言再试（见 QueryModePrefCaps）。
func (mod *Modem) queryModeCapsWith(d *modedialect.Dialect) (*ModePrefCaps, error) {
	resp, err := mod.SendAT(d.Query)
	if err != nil {
		return nil, err
	}
	if common.RespHasError(resp) {
		return nil, fmt.Errorf("%w：%s", errModePrefRejected, FirstLine(resp))
	}
	caps := &ModePrefCaps{Cmd: d.Query, Note: d.Note, Freeform: len(d.Choices) == 0, ReadOnly: d.ReadOnly}
	v, ok := "", false
	if d.Parse != nil {
		v, ok = d.Parse(resp)
	}
	if !ok || strings.TrimSpace(v) == "" {
		return nil, fmt.Errorf("%w：%s 的应答里没有本机的取值", errModePrefRejected, d.Query)
	}
	caps.Value = v
	caps.Name = modedialect.ModeLabel(v)
	caps.Choices = mod.modePrefChoices(d, v)
	return caps, nil
}

// modePrefChoices 组装下拉档位（按顺序）：
//
//  1. 本机码表里的档位（中文名 + 可写）—— 顺序固定：自动 → 单制式 → 组合；
//  2. 模组自己枚举（`AT+XXX=?`）出来的**额外取值**（能翻成档位就显示中文名，翻不出来显示那个取值）；
//  3. 当前值兜底（固件自定义码也要能原样写回）。
//
// 码表来自 QModem `vendor/*.sh`，所以正常情况下第 1 类就是全部可选档位。
func (mod *Modem) modePrefChoices(d *modedialect.Dialect, current string) []modedialect.ModeChoice {
	out := make([]modedialect.ModeChoice, 0, 16)
	seen := map[string]bool{}
	add := func(ch modedialect.ModeChoice) {
		k := strings.ToUpper(strings.TrimSpace(ch.Value))
		if k == "" || seen[k] {
			return
		}
		if ch.Name == "" {
			ch.Name = modedialect.ModeLabel(ch.Value)
		}
		seen[k] = true
		out = append(out, ch)
	}
	for _, v := range d.Choices {
		add(modedialect.ModeChoice{Value: v, Name: modedialect.ModeLabel(v), Writable: true, Source: "本机档位"})
	}
	for _, code := range mod.modePrefEnumerate(d) {
		sem := ""
		if d.CodeName != nil {
			sem = d.CodeName(code)
		}
		if modedialect.IsModePrefSemantic(sem) {
			add(modedialect.ModeChoice{Value: sem, Name: modedialect.ModeLabel(sem), Writable: true,
				Source: "设备枚举（" + d.Enum + "）"})
			continue
		}
		// 翻不出中文名的码：原样显示那个码（不编名字）
		add(modedialect.ModeChoice{Value: code, Name: code, Writable: true,
			Source: "设备枚举（" + d.Enum + "）"})
	}
	// 设备报了个我们没列到的值（固件自定义码）：也要能原样写回
	if cur := strings.TrimSpace(current); cur != "" && !seen[strings.ToUpper(cur)] {
		add(modedialect.ModeChoice{Value: cur, Name: modedialect.ModeLabel(cur), Writable: true, Source: "当前值"})
	}
	return out
}

// ResetModeEnum 清掉「设备枚举」缓存，下次查询重新问一次 `AT+XXX=?`。
// 换固件 / 换卡后设备支持的取值可能变，界面的「读当前值」按钮会调它。
//
// 顺手清掉“挨个问”的缓存：用户点「读当前值」就是想重新试一遍（换固件/换模组后方言可能变），
// 不把上次“都不认”的结论带过来。
func (mod *Modem) ResetModeEnum() {
	mod.modeEnumMu.Lock()
	mod.modeEnumDone = false
	mod.modeEnum = nil
	mod.modeEnumMu.Unlock()
	mod.clearModeProbe()
}

// modePrefEnumerate 问一次 `AT+XXX=?` 让模组自报支持的取值（结果缓存，失败不再重试）。
// 枚举只是"补充信息"：问不到就返回 nil，不影响读写当前值。
func (mod *Modem) modePrefEnumerate(d *modedialect.Dialect) []string {
	if d.Enum == "" {
		return nil
	}
	mod.modeEnumMu.Lock()
	defer mod.modeEnumMu.Unlock()
	if mod.modeEnumDone {
		return mod.modeEnum
	}
	mod.modeEnumDone = true
	resp, err := mod.SendAT(d.Enum)
	if err != nil || common.RespHasError(resp) {
		logger.Debug("mode pref enumerate failed",
			zap.Int("modem", mod.ID), zap.String("cmd", d.Enum))
		return nil
	}
	mod.modeEnum = modedialect.ParseEnumValues(resp, d.EnumLine)
	return mod.modeEnum
}

// SetModePrefCaps 设置组网模式偏好：语义值/原码 → 本机指令 → 回读确认
func (mod *Modem) SetModePrefCaps(value string) (*ModePrefCaps, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return nil, fmt.Errorf("组网模式取值不能为空")
	}
	d := modeDialectFor(mod)
	if d == nil {
		// 方言认不出来：用缓存过的探测结果；没缓存过就先挨个问一遍
		//（用户可能直接点“下发”：得先把本机到底认哪条指令找出来）
		if cd, _, cached := mod.cachedModeProbe(); cached {
			d = cd
		} else {
			pd, _, tried := mod.probeModeDialect()
			mod.storeModeProbe(pd, tried)
			d = pd
		}
	}
	if d == nil {
		return nil, fmt.Errorf("本机未提供组网模式偏好写入指令（各家的查询指令都试过了一遍）")
	} // 只读方言（取值/写语法未取证的固件）：读得到当前值，但不能写。
	// 这里直接把厂商自己写的理由报出来，比含糊的"本机不支持"有用得多。
	if d.ReadOnly {
		return nil, fmt.Errorf("本机的组网模式只能读，不能写：%s", d.Note)
	} // 取值校验：
	//   · 语义档位（AUTO/LTE/…）→ 必须在本机码表里（codes），否则这一档本机没有；
	//   · 其它 → 必须是"像本机码"的取值（表内码 / 设备枚举出来的 / 数字·十六进制原码）。
	allowed := false
	if modedialect.IsModePrefSemantic(value) {
		for _, v := range d.Choices {
			if common.NormalizeModePref(v) == value {
				allowed = true
				break
			}
		}
	} else {
		allowed = d.CodeFor(value) != ""
	}
	if !allowed {
		return nil, fmt.Errorf("本机不支持组网模式 %q（本机可选：%s）", value,
			modePrefChoiceSummary(mod.modePrefChoices(d, "")))
	}
	// 复合指令（如华为/美格 SYSCFGEX：后面还跟着频段/漫游参数）**先回读再改写**，
	// 只替换第 1 个字段、其余原样保留；回读不到就报错，不拿半条指令去发。
	//
	// ⚠️ 备用写法（d.alt）也需要"写入前的应答原文" ⇒ 只要配了 alt 就先把当前值读出来。
	var cmd, rawLine string
	if d.Rebuild != nil || d.Alt != nil {
		resp, err := mod.SendAT(d.Query)
		if err != nil {
			return nil, err
		}
		if line, ok := common.FirstLineWith(resp, d.RawMatch); ok {
			rawLine = line
		}
		if d.Rebuild != nil {
			if rawLine == "" {
				return nil, fmt.Errorf("读不到当前取值（%s）：%s", d.Query, FirstLine(resp))
			}
			cmd = d.Rebuild(rawLine, d.CodeFor(value))
		}
	}
	if cmd == "" {
		cmd = d.Set(value)
	}
	if strings.TrimSpace(cmd) == "" {
		return nil, fmt.Errorf("本机不支持组网模式 %q（本机可选：%s）", value,
			modePrefChoiceSummary(mod.modePrefChoices(d, "")))
	}
	logger.Info("mode pref write", zap.Int("modem", mod.ID), zap.String("cmd", cmd))
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return nil, err
	}
	if common.RespHasError(resp) {
		// 主写法被拒 ⇒ 试"备用写法"：
		//
		// 真机教训（2026-10-07，FM160-CN）：复合参数指令各固件接受的**形式**不一样 ——
		// 我们"把回读到的整串原样发回去"的写法被回 ERROR（空参数组会被省略 ⇒ 位置对齐有歧义），
		// 而只发档位号 / 上游 QModem 的固定模板就能过。两种写法都试，用户不必关心哪种是哪种。
		if alt := d.AltCmd(value, rawLine); alt != "" && alt != cmd {
			logger.Warn("mode pref primary form rejected, trying the fallback form",
				zap.Int("modem", mod.ID), zap.String("primary", cmd), zap.String("fallback", alt))
			resp2, err2 := mod.SendAT(alt)
			if err2 == nil && !common.RespHasError(resp2) {
				return mod.QueryModePrefCaps()
			}
			return nil, fmt.Errorf("模组把两种写法都拒了：%s / %s", cmd, alt)
		}
		return nil, fmt.Errorf("模组拒绝 %s：%s", cmd, FirstLine(resp))
	}
	// 回读确认（切制式是异步的，失败不算致命，但要把真实结果告诉用户）
	return mod.QueryModePrefCaps()
}

// modePrefChoiceSummary 可写档位的中文名列表（报错时提示"本机可选"用）
func modePrefChoiceSummary(choices []modedialect.ModeChoice) string {
	var names []string
	for _, c := range choices {
		if !c.Writable {
			continue
		}
		names = append(names, c.Name)
	}
	if len(names) == 0 {
		return "只能填模组回报的取值（先点「读当前值」看它报什么）"
	}
	return strings.Join(names, " / ")
}

// firstNonEmptyLine 取应答里第一条非空、非 OK 的行（只读方言拿不到解析器时兜底显示）
func firstNonEmptyLine(resp []string) string {
	for _, l := range resp {
		t := strings.TrimSpace(l)
		if t == "" || strings.EqualFold(t, "OK") {
			continue
		}
		return t
	}
	return ""
}
