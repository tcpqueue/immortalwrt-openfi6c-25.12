package kernel

import (
	"fmt"
	"modemd/module/atparse"
	"modemd/module/common"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ====================== USSD（引擎侧的胶水） ======================
//
// 解析/文案/码校验在 parse_test.go（纯逻辑）；这里只有与模组打交道的那几件：
// 读协程捕获 URC 后落库并推送、下发指令、SIM 状态判断。

// recordUSSD 由读协程调用：记下 URC 并推给在线客户端
func (mod *Modem) recordUSSD(res Result) {
	mod.mu.Lock()
	// 把"刚刚那条 +SPERROR"附上（如有）：这是"网络侧拒了这次会话"的直接佐证，
	// 否则界面上只剩一句看不出原因的"会话被网络结束"。
	AttachSPError(&res, mod.lastSPError, mod.lastSPErrorAt, time.Now())
	mod.USSD = &res
	mod.mu.Unlock()
	logger.Info("ussd response", zap.Int("modem", mod.ID),
		zap.Int("status", res.Status), zap.String("text", res.Text), zap.String("sperror", res.SPError))
	id := mod.ID
	broadcast("ussd", &id, res)
}

// noteSPError 记一条展锐 `+SPERROR` 上报（读协程调用）。
// 不单独做界面：它的用途是给紧随其后的 USSD/业务失败提供"哪一侧拒的"线索。
func (mod *Modem) noteSPError(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	mod.mu.Lock()
	mod.lastSPError = line
	mod.lastSPErrorAt = time.Now()
	mod.mu.Unlock()
}

// USSDLast 读最近一次 USSD 应答（加锁）
func (mod *Modem) USSDLast() *Result {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	if mod.USSD == nil {
		return nil
	}
	cp := *mod.USSD
	return &cp
}

// SendUSSD 下发一个 USSD 码，例如 *100#（查余额）。
//
// 返回值只是"已下发"：真正的应答要等模组的 +CUSD URC，见 recordUSSD。
func (mod *Modem) SendUSSD(code string) error {
	code = strings.TrimSpace(code)
	if !atparse.ValidCode(code) {
		return fmt.Errorf("USSD 码只能包含数字与 * # +，且长度 1~64（当前 %q）", code)
	}
	if strings.EqualFold(mod.SIMStateNow(), "SIM PIN") {
		return fmt.Errorf("SIM 卡处于锁定状态，无法发送 USSD")
	}
	mod.mu.Lock()
	mod.USSD = nil // 清掉上一次结果，避免拿着旧应答当新应答
	// 同时清掉上一条 +SPERROR：它是"上一次尝试"的线索，不能拿来解释这一次
	mod.lastSPError = ""
	mod.lastSPErrorAt = time.Time{}
	mod.mu.Unlock()

	// USSD 码的"写法"由本机字符集决定：有的固件（展锐 UMS9620 / 中兴 F50）默认
	// `AT+CSCS="HEX"`，直接发 `*101#` 会被拒（实测 `+CME ERROR: 3`），要发 hex。
	// 没声明该能力的厂商原样发。
	sendCode := code
	if f, ok := mod.cmdImpl.(USSDFormatter); ok {
		sendCode = f.USSDCode(code)
	}
	// 15 = 7bit 默认字符集；会话模式 1 = 需要应答
	cmd := fmt.Sprintf("AT+CUSD=1,\"%s\",15", sendCode)
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return err
	}
	if common.RespHasError(resp) {
		// 实测：固件 AT+CUSD=? 报 (0-2)（声明支持），但真正发送时被拒 ——
		// USSD 是**电路域(CS)**业务，而 5G SA 数据卡/物联网卡往往没有 CS 注册
		//（本机 AT+CREG? 恒为 0,0），这种组合下 USSD 实际不可用。
		return fmt.Errorf("模组拒绝 %s：USSD 是电路域(CS)业务，当前 CS 未注册或卡不支持（可用「概览→网络」里的 CS 域状态确认）", cmd)
	}
	logger.Info("ussd sent", zap.Int("modem", mod.ID), zap.String("code", code))
	return nil
}

// SIMStateNow 取最近一次读到的 SIM 状态（没读过返回空串）
func (mod *Modem) SIMStateNow() string {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	if mod.ModemStatus == nil {
		return ""
	}
	return mod.ModemStatus.SIMState
}

// ---- from: modem_extra.go ----

//
// 解析在 net.go（Extra / ParseExtraNetInfo / ParseHFREQINFO / ParseCBC，
// 含 CNWINFO / HFREQINFO / CBC 三种方言的实测说明）；
// 这里只剩"该不该发指令、发哪条"——这就是"其它模组不受影响"的兑现方式。
// extraNetInfoCommand 附加网络信息指令。**没声明该能力的厂商返回空串**，
// 上层看到空串连指令都不发 —— 这就是"其它模组不受影响"的兑现方式。
