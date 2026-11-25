package kernel

import (
	"errors"
	"fmt"
	"modemd/module/common"
	"modemd/module/config"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ====================== 短信错误详情（AT+CMEE） ======================
//
// ⚠️⚠️ 实测坑（FM350-GL，2026-10-01）：模组的 `AT+CMEE` 是 **0（关闭错误详情）**，
// 而且**不持久**（实测被重置回 0；手动设 2 之后过一段时间/重注册又变回 0）。
// CMEE=0 时短信被网络拒绝只会回一个**裸 `ERROR`**，界面只能给
// "号码格式 / SIM / 短信中心"这类通用猜测 —— 完全指错方向；
// 同一次发送在 CMEE=2 下回的是
// `+CME ERROR: Missing or unknown APN (#27)`（承载/卡侧的真实原因）。
//
// 对照实测（同一台 FM350、同一张卡、AT+COPS=11 的 5G SA）：
//
//	CMEE=0 + CGSMS=1/2/3 → 全部 "ERROR"（看不出任何原因）
//	CMEE=2 + CGSMS=1/2/3 → 全部 "+CME ERROR: Missing or unknown APN (#27)"
//
// ⇒ 错误码的有无**只取决于 CMEE**，与 CGSMS 无关（所以不要去动 CGSMS）。
// 发送前先确保一次 CMEE=2，发完写回原值（不去改模组的长期状态）。

// parseCMEE 从 `AT+CMEE?` 的应答里取出 <n>；取不到返回 ""。
func parseCMEE(resp []string) string {
	for _, line := range resp {
		if i := strings.Index(strings.ToUpper(line), "CMEE:"); i >= 0 {
			return strings.TrimSpace(line[i+len("CMEE:"):])
		}
	}
	return ""
}

// ensureSMSErrorDetailLocked 让模组的错误应答带上错误码（AT+CMEE=2）。
//
// 返回一个 restore：调用方 defer 它即可把 CMEE 写回原值（本来就是 2 / 读不到时是空操作）。
// 调用方必须已持有 atMu —— 内部会额外发 1~2 条指令。
func (mod *Modem) ensureSMSErrorDetailLocked() (restore func()) {
	restore = func() {}
	resp, err := mod.sendATLocked("AT+CMEE?")
	if err != nil {
		return
	}
	cur := parseCMEE(resp)
	if cur == "" || cur == "2" {
		return // 已经是详细模式（或无 <n>）：一个字节都不动
	}
	if _, err := mod.sendATLocked("AT+CMEE=2"); err != nil {
		logger.Warn("sms: enable CMEE=2 failed", zap.Int("modem", mod.ID), zap.Error(err))
		return
	}
	undo := "AT+CMEE=" + cur
	restore = func() {
		if _, e := mod.sendATLocked(undo); e != nil {
			logger.Debug("restore CMEE failed", zap.Int("modem", mod.ID), zap.Error(e))
		}
	}
	return
}

// errSMSPromptRejected AT+CMGS 被模组**明确拒绝**（回了 ERROR / +CME ERROR / +CMS ERROR），
// 即"模组没有进入正文输入态"。调用方可以据此安全地换一种号码写法重试 ——
// 与"超时"（模组可能已经在等正文）严格区分：后者绝不能再发一条 CMGS（会被当成正文吞掉）。
var errSMSPromptRejected = errors.New("模组拒绝了 AT+CMGS")

// waitSendPromptLocked 等模组进入正文输入态（AT+CMGS 已接受）。
//
// 判定见 smsPromptGrace 的说明：`>` / 0x1A 结尾行 / `OK`（实测 MT5700：CSMP 合法时
// CMGS 之后先回那行 0x1A、紧接一个 OK，表示“命令已接受、可输入正文”）/ 宽限时间到
// 都算就绪；只有 ERROR / +CME ERROR / +CMS ERROR 才是真拒绝，立刻报错，
// 不让用户白等一个 CmdTimeout。
//
// ⚠️ 前置条件：`>` 这类提示符**多数固件不带换行**，读循环必须能把它单独上报
// （见 modem.go 的 splitATLines 与那儿的实测说明），否则这里永远等不到。
func (mod *Modem) waitSendPromptLocked() (promptless bool, err error) {
	deadline := time.NewTimer(config.Cfg.Serial.CmdTimeout)
	defer deadline.Stop()
	grace := time.NewTimer(smsPromptGrace)
	defer grace.Stop()
	last := ""
	for {
		select {
		case <-deadline.C:
			if last != "" {
				return false, fmt.Errorf("模组没有进入正文输入态（等 `>` 超时，最后收到：%s）", last)
			}
			return false, fmt.Errorf("等模组 `>` 提示符超时（AT+CMGS 没被接受：号码格式或短信中心有问题）")
		case <-grace.C:
			logger.Info("modem sent no prompt; sending sms body anyway",
				zap.Int("modem", mod.ID), zap.String("last", last))
			return true, nil
		case line := <-mod.readChan:
			u := strings.ToUpper(strings.TrimSpace(line))
			if strings.HasPrefix(u, ">") {
				return false, nil
			}
			// 这类固件把"可以输入正文"表示成以 Ctrl+Z(0x1A) 结尾的一行
			if strings.ContainsRune(line, 0x1A) {
				logger.Info("modem prompt is 0x1A terminated; sending sms body",
					zap.Int("modem", mod.ID), zap.String("line", strings.TrimSpace(line)))
				return true, nil
			}
			// 实测 MT5700：CSMP 合法时，CMGS 之后先回那行 0x1A、紧接着一个 `OK` ——
			// 这个 OK 是“命令已接受、可输入正文”，不是拒绝，不能当错误报。
			if u == "OK" {
				logger.Info("modem accepted AT+CMGS (OK); sending sms body", zap.Int("modem", mod.ID))
				return true, nil
			}
			if isATEndLine(line) {
				// 模组直接拒了：立刻把模组原话报出来
				// （包一层 errSMSPromptRejected，调用方据此判断"没进输入态、可以换号码写法重试"）
				return false, fmt.Errorf("%w（%s）", errSMSPromptRejected, strings.TrimSpace(line))
			}
			// 其余行（回显、URC）先记下：超时时能告诉用户模组到底说了什么
			last = strings.TrimSpace(line)
		}
	}
}

// smsSendBody 决定这次发送的**正文**写法与**号码**写法候选，并返回"把模组状态写回原值"的函数。
//
// ⚠️ 正文必须按 `AT+CSCS` 编码，号码则**看固件**（实测两家正好相反，所以按顺序试）：
//
//	· 广和通 FM350-GL：`CSCS="UCS2"` 下照样接受 ASCII 号码 `AT+CMGS="13520309333"`（回 `>`），
//	  把号码编成十六进制反倒拿不到提示符；
//	· 芯讯通 SIM8200EA-M2：`CSCS="UCS2"` 下 ASCII 号码直接被拒（实测秒回 `ERROR`，
//	  因为 "13800138000" 是奇数长度的十六进制串、根本解不出来），必须写成
//	  `AT+CMGS="00310033…0030"` 才回 `>`。
//
// 所以 numbers 返回候选列表（先 ASCII，再 UCS2 十六进制），由调用方按序试：
// 第一条被"明确拒绝"（没进输入态）时才试第二条，见 doSendSMS。
//
// ⚠️ 调用方必须已持有 atMu（内部会独立发包读/写 CSCS、CSMP），并且**无论成功失败都要
// defer restore()** —— 需要时由它把临时改掉的 CSCS / CSMP 恢复；没改动时是空操作。
func (mod *Modem) smsSendBody(content, phone string) (body string, numbers []string, restore func(), err error) {
	var undo []string // 需要恢复的指令，逆序执行
	restore = func() {
		for i := len(undo) - 1; i >= 0; i-- {
			if _, e := mod.sendATLocked(undo[i]); e != nil {
				logger.Error("restore sms charset state failed",
					zap.Int("modem", mod.ID), zap.String("cmd", undo[i]), zap.Error(e))
			}
		}
	}

	charset := ""
	if resp, e := mod.sendATLocked("AT+CSCS?"); e == nil {
		charset = parseSMSCharset(resp)
	}
	// 纯 ASCII 正文 + 非 UCS2 字符集（移远这类默认 "GSM" 的固件）：保持原样，
	// 一个字节都不动模组状态，号码也只用 ASCII 写法。
	if charset != "UCS2" && smsASCIIOnly(content) {
		return content, []string{phone}, restore, nil
	}

	// 到这里说明正文要用 UCS2（本来就是 UCS2，或者正文含中文等非 ASCII）
	if charset != "UCS2" {
		if charset != "" {
			undo = append(undo, fmt.Sprintf(`AT+CSCS="%s"`, charset))
		}
		if _, e := mod.sendATLocked(`AT+CSCS="UCS2"`); e != nil {
			return content, nil, restore, fmt.Errorf("切 UCS2 字符集失败（发中文短信需要它）：%w", e)
		}
	}
	// DCS 要与 UCS2 匹配（8 = UCS2），否则号码对、对方收到的正文是乱码/空
	if resp, e := mod.sendATLocked("AT+CSMP?"); e == nil {
		if params, dcs, ok := parseSMSCSMP(resp); ok && dcs != 8 {
			undo = append(undo, "AT+CSMP="+params)
			if _, e := mod.sendATLocked("AT+CSMP=" + smsCSMPWithDCS(params, 8)); e != nil {
				logger.Warn("set CSMP dcs=8 (UCS2) failed", zap.Int("modem", mod.ID), zap.Error(e))
			}
		}
	}
	return encodeUCS2Hex(content), []string{phone, encodeUCS2Hex(phone)}, restore, nil
}

// ---- from: modem_sms_storage.go ----

// SMSStorage 短信存储用量。
// 模组会同时上报三个存储区（读/写/接收），多数模组三者相同。
//
// ⚠️ used/total 不加 omitempty：**用量为 0 时字段不能消失** ——
// 以前 Mem2Used/Mem3Used 带 omitempty，收件箱空（实测 EM05 的 `+CPMS: "SM",0,40,…`）时
// JSON 里就只剩 mem2_total，前端算出 `undefined/40（NaN%）`。

// SMSStorage 短信存储用量。
// 模组会同时上报三个存储区（读/写/接收），多数模组三者相同。
//
// ⚠️ used/total 不加 omitempty：**用量为 0 时字段不能消失** ——
// 以前 Mem2Used/Mem3Used 带 omitempty，收件箱空（实测 EM05 的 `+CPMS: "SM",0,40,…`）时
// JSON 里就只剩 mem2_total，前端算出 `undefined/40（NaN%）`。
type SMSStorage struct {
	Mem1      string `json:"mem1"`
	Mem1Used  int    `json:"mem1_used"`
	Mem1Total int    `json:"mem1_total"`
	Mem2      string `json:"mem2,omitempty"`
	Mem2Used  int    `json:"mem2_used"`
	Mem2Total int    `json:"mem2_total"`
	Mem3      string `json:"mem3,omitempty"`
	Mem3Used  int    `json:"mem3_used"`
	Mem3Total int    `json:"mem3_total"`
	// Current 当前存储区（= mem1，方便前端直接画“当前：SM”）
	Current string `json:"current,omitempty"`
	// Supported 模组支持的存储区（取自 AT+CPMS=?），用于页面上的切换下拉框；
	// 部分固件不回应答，此时为空，页面退回 SM / ME / MT 三个选项。
	Supported []string `json:"supported,omitempty"`
	Raw       string   `json:"raw,omitempty"`
}

// validSMSMem 存储区名称校验。
// 只允许 3GPP TS 27.005 定义的三种：SM（SIM 卡）/ ME（模块）/ MT（两者均可用）。
// ⚠️ 要校验：这个字符串会直接拼进 `AT+CPMS="…","…","…"`。
func validSMSMem(mem string) bool {
	switch strings.ToUpper(strings.TrimSpace(mem)) {
	case "SM", "ME", "MT":
		return true
	}
	return false
}

// reCPMSMemToken 从 AT+CPMS=? 里抠出 "ME"/"SM" 这样的存储区名
var reCPMSMemToken = regexp.MustCompile(`"([A-Za-z0-9_]+)"`)

// parseCPMSSupported 解析 AT+CPMS=? 的支持列表：
//
//	+CPMS: ("ME","MT","SM"),("ME","MT","SM"),("ME","MT","SM")
//
// 三个存储区列表通常一样，去重后按原顺序返回。
func parseCPMSSupported(resp []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range resp {
		i := strings.Index(strings.ToUpper(line), "CPMS:")
		if i < 0 {
			continue
		}
		for _, tok := range reCPMSMemToken.FindAllStringSubmatch(line[i:], -1) {
			m := strings.ToUpper(tok[1])
			if seen[m] {
				continue
			}
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// parseSMSStorage 解析 AT+CPMS? 应答：
//
//	+CPMS: "SM",3,30,"SM",3,30,"SM",3,30
func parseSMSStorage(resp []string) (SMSStorage, bool) {
	const head = "+CPMS:"
	for _, line := range resp {
		idx := strings.Index(strings.ToUpper(line), head)
		if idx < 0 {
			continue
		}
		f := common.SplitATFields(line[idx+len(head):])
		if len(f) < 6 {
			continue
		}
		st := SMSStorage{
			Mem1: f[0], Mem1Used: common.AtIntOr0(f, 1), Mem1Total: common.AtIntOr0(f, 2),
			Mem2: f[3], Mem2Used: common.AtIntOr0(f, 4), Mem2Total: common.AtIntOr0(f, 5),
			Current: strings.ToUpper(f[0]),
			Raw:     strings.TrimSpace(line),
		}
		if len(f) >= 9 {
			st.Mem3 = f[6]
			st.Mem3Used = common.AtIntOr0(f, 7)
			st.Mem3Total = common.AtIntOr0(f, 8)
		}
		return st, true
	}
	return SMSStorage{}, false
}

// applySMSStorage 下发短信存储位置（AT+CPMS）。
// 此前 Cfg.SMS.SMSMemory 配置项从未生效，导致读到的可能是模组默认存储区而非 SIM 卡。
func (mod *Modem) applySMSStorage() {
	mem := config.Cfg.SMS.SMSMemory
	if mem == "" {
		mem = "SM"
	}
	resp, err := mod.SendAT(mod.cmdImpl.CommandSMSStorage(mem))
	if err != nil {
		logger.Warn("set sms storage failed",
			zap.Int("modem", mod.ID), zap.String("mem", mem), zap.Error(err))
		return
	}
	if e := atError(resp); e != "" {
		logger.Warn("modem rejected sms storage",
			zap.Int("modem", mod.ID), zap.String("mem", mem), zap.String("resp", e))
		return
	}
	logger.Info("sms storage applied", zap.Int("modem", mod.ID), zap.String("mem", mem))
}
