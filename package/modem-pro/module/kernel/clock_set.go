package kernel

// ====================== 模组时钟：校时（AT+CCLK） ======================
//
// 实测（中兴 F50 / UMS9620，2026-10-06）：`AT+CCLK?` 能读，但这台机器的 RTC **从未校时**，
// 长期回 `+CCLK: "13/01/01,00:00:00+00"`（与主机 UTC 差十几年）——而**短信时间戳用的就是它**，
// 所以界面上的"模组时钟"不校时就是一句废话。
//
// 写法按 3GPP TS 27.007 §8.15：`AT+CCLK="yy/MM/dd,hh:mm:ss±zz"`，其中 `zz` 是**刻钟**为单位
// （+32 = UTC+8；本机回读里也见过 +32）。模组内部按 UTC 存时间 —— 本项目的 ParseCCLK 就是这么读的，
// 而且还有一批固件"带时区后缀但报的就是 UTC"的特例（见 atcmd.go 的 IgnoreCCLKTZ）。
// 因此校时**一律写 +00**，把主机的 UTC 时刻原样塞进去，避免二次偏移。

import (
	"fmt"
	"modemd/module/atparse"
	"modemd/module/common"
	"time"

	"go.uber.org/zap"
)

// formatCCLK 把时刻格式化成 AT+CCLK 的取值（UTC，时区后缀 +00）
func formatCCLK(t time.Time) string {
	u := t.UTC()
	return fmt.Sprintf("%02d/%02d/%02d,%02d:%02d:%02d+00",
		u.Year()%100, int(u.Month()), u.Day(), u.Hour(), u.Minute(), u.Second())
}

// ModuleClockCommand 校时指令（导出让「AT 指令」页与测试核对写法）
func ModuleClockCommand(t time.Time) string { return `AT+CCLK="` + formatCCLK(t) + `"` }

// readModuleClock 读一次 `AT+CCLK?`（raw 为模组原样文本）
func (mod *Modem) readModuleClock() (raw string, unix int64, skew int64, ok bool) {
	resp, err := mod.SendAT("AT+CCLK?")
	if err != nil || common.RespHasError(resp) {
		return "", 0, 0, false
	}
	return atparse.ParseCCLK(resp, time.Now(), mod.Quirk().IgnoreCCLKTZ)
}

// SetModuleClock 把模组时钟校到 t（零值 = 主机当前时刻），写完**回读确认**，
// 返回模组回读到的原文（如 `26/10/05,17:30:00+00`）。
//
// ⚠️ 回读确认是必须的：实测有固件对 `AT+CCLK=` 静默忽略（回 OK 但值不变），
// 不确认就等于"界面说成功、短信时间戳还是错的"。
// ⚠️ 内部会取 mod.mu（机型特例），不要在已持锁的地方调。
func (mod *Modem) SetModuleClock(t time.Time) (string, error) {
	if t.IsZero() {
		t = time.Now()
	}
	want := t.UTC().Truncate(time.Second)
	cmd := ModuleClockCommand(want)
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return "", err
	}
	if common.RespHasError(resp) {
		return "", fmt.Errorf("校时被拒：%s", FirstLine(resp))
	}
	for attempt := 0; attempt < 3; attempt++ {
		raw, unix, _, ok := mod.readModuleClock()
		if ok {
			if time.Unix(unix, 0).UTC().Truncate(time.Second).Equal(want) {
				logger.Info("module clock set",
					zap.Int("modem", mod.ID), zap.String("cclk", raw))
				return raw, nil
			}
			if attempt == 2 {
				return raw, fmt.Errorf("模组没接受校时：写的是 %s，回读 %s", formatCCLK(want), raw)
			}
		}
		time.Sleep(time.Second) // 固件可能异步生效（同 mode_pref 的坑）
	}
	return "", fmt.Errorf("校时后读不到 +CCLK，无法确认是否生效")
}
