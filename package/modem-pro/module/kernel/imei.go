package kernel

import (
	"fmt"
	"modemd/module/common"
	"modemd/module/imeidialect"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ====================== IMEI 管理（读取 + 改写） ======================
//
// 读取用 3GPP 标准 `AT+CGSN`（见 CommandIMEI）；**改写每家指令都不同**，
// 方言表在 vendor_settings_imei.go，本文件只负责引擎侧流程：
//
//	校验（15 位数字）→ 下发（可能需要多条）→ **回读 AT+CGSN 确认**→ 返回新值
//
// 为什么一定要回读：改 IMEI 是"写 NV"这类操作，模组回 `OK` 不代表真的写进去了
// （QModem 的脚本也是写完立刻 `get_imei` 再展示一次）；回读不一致就如实报错，
// 让用户知道没生效，而不是显示一个假的成功。
//
// ⚠️ 改 IMEI 在多数国家/地区受法律限制（伪造 IMEI 属于违法行为），界面必须提示。

// IMEICaps IMEI 改写能力（界面用它决定是否显示输入框）
type IMEICaps struct {
	IMEI      string `json:"imei"`      // 当前 IMEI（读不到为空）
	ReadCmd   string `json:"read_cmd"`  // 读取指令（本机）
	Supported bool   `json:"supported"` // 是否支持改写
	Cmd       string `json:"cmd,omitempty"`
	Note      string `json:"note,omitempty"`

	// 原始 IMEI 备份（imei_backup.go）：**改写前**自动存一份，界面可一键恢复。
	// 按模块分别记录（键 = AT 口），多台模组各有各的原始值。
	Original        string    `json:"original,omitempty"`          // 备份的原始 IMEI（没备份过为空）
	OriginalSavedAt time.Time `json:"original_saved_at,omitempty"` // 原始值是什么时候存下的
	Writes          int       `json:"writes,omitempty"`            // 通过本程序改写了几次
	RestoredAt      time.Time `json:"restored_at,omitempty"`       // 最近一次恢复原始值的时间
	CanRestore      bool      `json:"can_restore"`                 // 有备份且当前值 ≠ 原始值（可以恢复）
}

// reIMEIFromGSN 从 AT+CGSN 应答里取 15 位 IMEI
var reIMEIFromGSN = regexp.MustCompile(`\b(\d{15})\b`)

// ParseIMEI 从应答里取 IMEI（各厂商 AT+CGSN 的包装不同，统一找 15 位数字串）
func ParseIMEI(resp []string) (string, bool) {
	for _, line := range resp {
		// 带引号的写法（如 +CGSN: "86…"）也走同一条正则
		if m := reIMEIFromGSN.FindStringSubmatch(line); m != nil {
			return m[1], true
		}
	}
	return "", false
}

// QueryIMEI 读当前 IMEI
//
// 主写法是 3GPP 标准 `AT+CGSN`（上游各厂商脚本读侧也是它）。少数固件只认老写法
// `AT+GSN`（上游部分脚本/工具用这条）⇒ 主写法被拒/不回 IMEI 时自动兜一次，
// 免得界面上一台能正常工作的模组显示"IMEI 读不到"。
func (mod *Modem) QueryIMEI() (string, error) {
	cmds := imeiReadCommands(mod.cmdImpl.CommandIMEI())
	var lastErr error
	for i, c := range cmds {
		resp, err := mod.SendAT(c)
		if err != nil {
			lastErr = err
			continue
		}
		if common.RespHasError(resp) {
			lastErr = fmt.Errorf("IMEI 读取被拒：%s", FirstLine(resp))
			continue
		}
		if v, ok := ParseIMEI(resp); ok {
			return v, nil
		}
		lastErr = fmt.Errorf("IMEI 未上报：%s", FirstLine(resp))
		if i == 0 {
			logger.Debug("imei read empty, trying legacy form",
				zap.Int("modem", mod.ID), zap.String("cmd", cmdIMEILegacy))
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("IMEI 未上报")
	}
	return "", lastErr
}

// imeiReadCommands IMEI 读取的指令序列：厂商指令 + 老写法兜底（去重、丢空串）
func imeiReadCommands(primary string) []string {
	out := make([]string, 0, 2)
	for _, c := range []string{primary, cmdIMEILegacy} {
		if c == "" || (len(out) > 0 && out[len(out)-1] == c) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// cmdIMEILegacy 老写法（上游部分脚本用）：不是 3GPP 标准，但个别固件只认它
const cmdIMEILegacy = "AT+GSN"

// IMEICapsOf 汇总当前模组的 IMEI 改写能力（含当前 IMEI）
func (mod *Modem) IMEICapsOf() *IMEICaps {
	caps := &IMEICaps{ReadCmd: mod.cmdImpl.CommandIMEI()}
	if d := imeiDialectFor(mod); d != nil {
		caps.Supported = true
		caps.Cmd = d.Set(strings.Repeat("0", 15)) // 用占位 IMEI 渲染命令模板，便于界面核对
		caps.Note = d.Note
	} else {
		caps.Note = "本机没有可用的改 IMEI 指令：Modem-Pro 的 cmds/<vendor>.sh 里没有该厂商的实现" +
			"（例如 Thales MV32-W 明确回 \"IMEI modification not supported\"），所以不发未验证指令。"
	}
	if imei, err := mod.QueryIMEI(); err == nil {
		caps.IMEI = imei
	}
	// 原始 IMEI 备份（改写前存下的）：当前值 ≠ 原始值 → 界面显示「恢复原始 IMEI」
	if b, ok := imeiBackupFor(mod); ok {
		caps.Original = b.Original
		caps.OriginalSavedAt = b.SavedAt
		caps.Writes = b.Writes
		caps.RestoredAt = b.RestoredAt
		// 能不能恢复看**刚刚读到的实时值**（不用记录里的 Current：那是上次改写时的值，
		// 用户可能在「AT 指令」页手改过）
		caps.CanRestore = caps.Supported && imeidialect.ValidIMEI(caps.IMEI) && caps.IMEI != b.Original
	}
	return caps
}

// SetIMEI 改写 IMEI：校验 → 下发 → 回读确认。
//
// 返回回读到的 IMEI（成功时等于入参）。任何一步不成功都返回 error，不会假报成功。
func (mod *Modem) SetIMEI(imei string) (string, error) {
	imei = strings.TrimSpace(imei)
	if !imeidialect.ValidIMEI(imei) {
		return "", fmt.Errorf("IMEI 必须是 15 位数字（当前输入 %d 位）", len(imei))
	}
	d := imeiDialectFor(mod)
	if d == nil {
		return "", fmt.Errorf("本机不支持改 IMEI（没有可用的厂商指令）")
	}
	cmd := d.Set(imei)
	if strings.TrimSpace(cmd) == "" {
		return "", fmt.Errorf("本机不支持改 IMEI")
	}
	// 下发前先记下"现在是什么"—— 写进去就再也读不回来了，没有这一步就没法恢复。
	// 优先用缓存里的 mod.IMEI（少发一条 AT）；缓存为空才真去读。
	before := strings.TrimSpace(mod.IMEI)
	if !imeidialect.ValidIMEI(before) {
		if v, err := mod.QueryIMEI(); err == nil {
			before = v
		}
	}
	logger.Info("imei write",
		zap.Int("modem", mod.ID), zap.Int("cmd_count", len(strings.Split(cmd, ";"))))
	// ⚠️ 备份必须记在**下发之前**（见 imei_backup.go 的 rememberBefore）：
	// 回读可能失败（展锐写完 IMEI 会先回一阵 +CME ERROR: 22），
	// 放到"回读成功之后"再记 ⇒ 这一笔永远记不下 ⇒ 原始 IMEI 永久丢失。
	// 2026-10-05 真机上就真实丢过一次（imei.json 至今不存在）。
	// 只写 Original、不计 Writes；回读成功后的 imeiBackupRecord 再补 Current 与计数。
	if imeidialect.ValidIMEI(before) {
		imeiBackupRememberBefore(mod, before)
	}
	if err := imeiApply(mod, cmd); err != nil {
		return "", err
	}
	// 回读确认（改 IMEI 属于写 NV，回 OK 不一定生效）
	got, err := mod.QueryIMEI()
	if err != nil {
		return "", fmt.Errorf("已下发写入指令，但回读失败：%w", err)
	}
	// 不管回读是否等于目标值都记一笔：回读不一致时值可能已经变了，
	// 那一笔对"之后能不能恢复"同样关键（记的是 before 与真实回读值）。
	imeiBackupRecord(mod, before, got)
	if got != imei {
		return got, fmt.Errorf("写入未生效：回读到的 IMEI 仍是 %s（部分固件需要重启模组或改 NV 后才更新）", got)
	}
	return got, nil
}

// RestoreIMEI 把 IMEI 写回"改写前保存的原始值"（见 imei_backup.go）。
//
// 没有备份时**明确报错，绝不猜一个值写下去** —— 写错 IMEI 比不改更麻烦。
func (mod *Modem) RestoreIMEI() (string, error) {
	b, ok := imeiBackupFor(mod)
	if !ok || !imeidialect.ValidIMEI(b.Original) {
		return "", fmt.Errorf("这台模组还没有保存过原始 IMEI（第一次改写时会自动保存，之后就能一键恢复）")
	}
	// 已经是原始值就不必再写（省一次写 NV）—— 但只有读得到当前值时才这么判
	if cur, err := mod.QueryIMEI(); err == nil && cur == b.Original {
		return cur, nil
	}
	got, err := mod.SetIMEI(b.Original)
	if err != nil {
		return got, err
	}
	imeiBackupMarkRestored(mod, got)
	return got, nil
}
