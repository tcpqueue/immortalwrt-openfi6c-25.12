package kernel

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"go.uber.org/zap"
)

// ---- from: modem_sms_charset.go ----
//
// ====================== 短信发送：字符集（AT+CSCS）与提示符 ======================
//
// ⚠️ 实测坑（FM350-GL「发送短信不正常」的根因，两个叠在一起）：
//
//  1. **正文要按 `AT+CSCS` 编码，号码不用**：`AT+CSCS` 是模组级**全局状态**，
//     出厂默认值随平台不同 —— 移远是 `"GSM"`，而广和通 FM350-GL 是 `"UCS2"`。
//     CSCS=UCS2 时正文必须写成 UTF-16BE 十六进制，否则模组回 `ERROR`
//     （实测：`AT+CMGS="13520309333"` → `>` → `nihao␚` → `ERROR`）。
//     号码则**保持 ASCII**：实测 FM350 在 UCS2 下照样给 `AT+CMGS="13520309333"`
//     回 `>`，反倒是不给十六进制号码回提示符。
//  2. **必须等模组回 `>` 再发正文**：以前固定 `time.Sleep(200ms)`，FM350 上不够，
//     正文先到会被丢掉 —— `AT+CMGS` 一路超时，模组一直等正文，
//     之后**每条** AT 指令（包括恢复状态的）都被当成正文吞掉，AT 通道“卡死”。
//
// 处理原则：**不去改模组的长期状态**。需要 UCS2 时（本来就是 UCS2，或正文含中文等
// 非 ASCII 而当前不是 UCS2）临时切到 UCS2 并配上 DCS=UCS2，发完立刻用 undo 恢复原值。

// reSMSCharset 解析 `AT+CSCS?` 的应答行：`+CSCS: "UCS2"`
var reSMSCharset = regexp.MustCompile(`\+CSCS:\s*"?([^"\r\n]+)"?`)

// parseSMSCharset 从 `AT+CSCS?` 的应答里取出字符集名（大写）；取不到返回 ""。
func parseSMSCharset(resp []string) string {
	for _, line := range resp {
		if m := reSMSCharset.FindStringSubmatch(line); m != nil {
			return strings.ToUpper(strings.TrimSpace(m[1]))
		}
	}
	return ""
}

// reSMSCSMP 解析 `AT+CSMP?` 的应答行：`+CSMP: 0, 71, 0, 0`
//
// ⚠️ 必须允许**空字段**：鼎桥 MT5700M-CN 出厂就是 `+CSMP: ,,0,0`（<fo>/<vp> 为空）。
// 老正则 `([0-9]+(?:\s*,\s*[0-9]+)*)` 要求首位是数字 ⇒ 这种情况**匹配失败**，
// 于是“CSMP 为空就补一发”的修复永远不触发（用例 `TestSMSSendFixesEmptyCSMP` 抓到的）。
var reSMSCSMP = regexp.MustCompile(`\+CSMP:\s*([0-9,\s]*)`)

// parseSMSCSMP 从 `AT+CSMP?` 的应答里取出参数串（已去掉空格，用来写回恢复）
// 与其中的 dcs（第 4 个参数，决定正文编码：8 = UCS2）。
func parseSMSCSMP(resp []string) (params string, dcs int, ok bool) {
	for _, line := range resp {
		m := reSMSCSMP.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		parts := strings.Split(m[1], ",")
		if len(parts) < 4 {
			return "", 0, false
		}
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		d, err := strconv.Atoi(parts[3])
		if err != nil {
			return "", 0, false
		}
		return strings.Join(parts, ","), d, true
	}
	return "", 0, false
}

// smsCSMPWithDCS 把 CSMP 参数串的第 4 个参数（dcs）换成 dcs，其余原样保留。
func smsCSMPWithDCS(params string, dcs int) string {
	parts := strings.Split(params, ",")
	if len(parts) < 4 {
		return params
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	parts[3] = strconv.Itoa(dcs)
	return strings.Join(parts, ",")
}

// encodeUCS2Hex 把字符串按 UTF-16BE 编成十六进制串（`CSCS="UCS2"` 时**正文**的写法）。
func encodeUCS2Hex(s string) string {
	units := utf16.Encode([]rune(s))
	var b strings.Builder
	b.Grow(len(units) * 4)
	for _, u := range units {
		fmt.Fprintf(&b, "%04X", u)
	}
	return b.String()
}

// smsASCIIOnly 判断内容是否只含 ASCII 可打印字符。
// 这类内容在 GSM 字符集下也能发，因此不必为了它去动模组的 CSCS。
func smsASCIIOnly(s string) bool {
	for _, r := range s {
		if r < 0x20 || r > 0x7E {
			return false
		}
	}
	return true
}

// smsPromptGrace 等 `>` 提示符的"宽限时间"：到点还没有提示符、也没有报错，就按
// "不回提示符的固件"处理，直接把正文发下去。
//
// ⚠️⚠️ 实测（鼎桥 MT5700M-CN，固件 V200R001C20B016）：`AT+CMGS="<号>"` **根本不回 `>`**。
// 抓串口原始字节（停掉 modemd 直接读 /dev/ttyUSB1）得到的完整应答是：
//
//	AT+CMGS="10086" 1a 0d 0a        ← 就这一行，0x1A 结尾，之后再没有任何输出
//
// 而且其它指令（AT / AT+CMGF=1 / AT+CSMP=… / AT+CSCA?）都**不回显**，所以这行是模组
// 自己发的（不是 flush 出来的回显）。旧实现"发完 AT+CMGS 固定 sleep(200ms) 就发正文"
// 在这台机器上是能用的；为了修 FM350（提示符要几百毫秒才来）改成"死等 `>`"之后，
// 在 MT5700 上就**永远等不到提示符** ⇒ 发短信直接失败（用户反馈："之前是正常的"）。
//
// 所以策略改成三条取或：① 收到 `>`；② 收到以 0x1A 结尾的行（这类固件的"可以输入正文"）；
// ③ 宽限时间到且既无提示符也无报错 ⇒ 当无提示符固件，继续发正文（真发不出去会在
// 后面的应答里给出 ERROR，不会变成静默失败）。FM350 的提示符来得比 smsPromptGrace 早，
// 所以这条兜底不会误伤它；万一它真慢了，也只是回到"旧实现"的行为。
const smsPromptGrace = 1200 * time.Millisecond

// smsSubmitTimeout 等短信提交应答（`+CMGS: <mr>` / `OK`）的超时。
//
// ⚠️ 实测（鼎桥 MT5700M-CN）：正文 + Ctrl+Z 之后模组要等**网络侧回执**才回确认，
// 比普通指令的 CmdTimeout（默认 3s）长得多 —— 给到 30s，宁可让页面转一会，
// 也别把“其实已经发出去”的短信报成失败。
//
// 做成变量是为了用例能把它调小（否则每个“不回确认”的用例都要跑 30 秒）。
var smsSubmitTimeout = 30 * time.Second

// smsPromptlessConfirmWait 对“连 `>` 都不回的固件”等确认的时间。
//
// 实测 MT5700：正文发出后 30 秒也没什么都没回 ⇒ 反正本地拿不到确认（按“已提交”处理），
// 只给 5 秒，免得用户为一条注定拿不到确认的短信干等 30 秒。
var smsPromptlessConfirmWait = 5 * time.Second

// ensureSMSCSMPLocked 保证 `AT+CSMP` 的 <fo>/<vp> 不为空（空时补一手册 §9.7 的标准值）。
//
// ⚠️⚠️ 实测（鼎桥 MT5700M-CN，固件 V200R001C20B016）：出厂 `AT+CSMP?` 回 `+CSMP: ,,0,0`
// （<fo>/<vp> 为空）时，`AT+CMGS="…"` 会被**静默吞掉**：只回一行以 0x1A 结尾的内容，
// 之后既不回 +CMGS 也不报错（抓串口 30 秒无输出）⇒ 表现就是“发送短信失败/永远超时”。
// 补上 `AT+CSMP=17,167,0,0`（17 = SMS-SUBMIT + 相对有效期，167 = 24 小时）之后，
// 同样的 `AT+CMGS` 立刻回 `OK`，整个发送流程才正常。
//
// 只在**确实读到空值**时才写；读不到/解析不了时不动模组状态（免得多写一条指令干扰其它机型）。
func (mod *Modem) ensureSMSCSMPLocked() error {
	resp, err := mod.sendATLocked("AT+CSMP?")
	if err != nil {
		return nil // 读不到就算了：后面 CMGS 会给出真实原因
	}
	params, _, ok := parseSMSCSMP(resp)
	if !ok {
		return nil
	}
	parts := strings.Split(params, ",")
	if len(parts) >= 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
		return nil // 合法：一个字节都不动
	}
	if _, err := mod.sendATLocked(smsDefaultCSMP); err != nil {
		return fmt.Errorf("修正 AT+CSMP 失败（该固件 CSMP 为空时发短信会被静默吞掉）：%w", err)
	}
	logger.Warn("sms CSMP was empty; applied default",
		zap.Int("modem", mod.ID), zap.String("was", params), zap.String("now", smsDefaultCSMP))
	return nil
}

// smsDefaultCSMP 手册 §9.7 的标准 SMS-SUBMIT 参数：<fo=17>、<vp=167>（24 小时）、pid=0、dcs=0。
const smsDefaultCSMP = "AT+CSMP=17,167,0,0"

// ---- from: modem_sms_service.go ----
