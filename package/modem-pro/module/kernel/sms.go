package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"errors"
	"fmt"
	"modemd/module/common"
	"modemd/module/sms"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ---- from: modem_sms.go ----

// ---- from: modem_sms.go ----
// ReadSMS 读取全部短信。
//
// 分两类固件：
//  1. 常规固件：文本模式读正文（本固件的 UCS2 短信以 UTF-16BE 十六进制返回，见
//     DecodeSMSText）+ 再切一次 PDU 模式只为取 UDH 拼接信息（合成长短信）；
//  2. 声明了 PDUSMSReader 的固件（实测鼎桥 MT5700：文本模式下模组**不回正文**）：
//     只走 PDU 模式，正文/号码/时间/分条信息全部从 PDU 里解（见 sms.ParsePDUList）。
//
// 最后按时间倒序排序：模组的存储位置顺序不代表到达时间（新短信会占用被删除
// 记录空出来的位置），直接按位置展示会把最新一条夹在旧短信中间。
// ReadSMS 读取全部短信。
//
// 分两类固件：
//  1. 常规固件：文本模式读正文（本固件的 UCS2 短信以 UTF-16BE 十六进制返回，见
//     DecodeSMSText）+ 再切一次 PDU 模式只为取 UDH 拼接信息（合成长短信）；
//  2. 声明了 PDUSMSReader 的固件（实测鼎桥 MT5700：文本模式下模组**不回正文**）：
//     只走 PDU 模式，正文/号码/时间/分条信息全部从 PDU 里解（见 sms.ParsePDUList）。
//
// 最后按时间倒序排序：模组的存储位置顺序不代表到达时间（新短信会占用被删除
// 记录空出来的位置），直接按位置展示会把最新一条夹在旧短信中间。

func (mod *Modem) ReadSMS() ([]sms.SMS, error) {
	// 整段序列串行化（见 core.go 的 smsMu）：CMGF 切换与 CMGL 之间不能被另一次读插队。
	mod.smsMu.Lock()
	list, err := mod.readSMSRaw()
	if err == nil {
		list = SortSMSNewestFirst(list)
		mod.mu.Lock()
		mod.SMSList = list
		mod.SMSTotal, mod.SMSUnread = len(list), CountUnreadSMS(list)
		mod.smsReadAt = time.Now()
		mod.mu.Unlock()
	}
	mod.smsMu.Unlock()
	if err != nil {
		return nil, err
	}
	// 读短信往往是新短信通知触发/用户手动点，顺手推给所有在线客户端
	pushSMS(mod, list)
	// 角标（条数）单独推一份：它不过短信密码闸门，悬浮小窗/别的页面也能显示未读数
	pushSMSCount(mod)
	return list, nil
}

// setSMSListCounts 按刚读到的列表刷新条数/未读数（角标用；不含正文）。
//
// ⚠️ 未读判据分两种固件：文本模式的 `REC UNREAD`，PDU 模式的**数字状态**（0 = 未读）。
// 只看 "UNREAD" 字样的话，FM350 这类走 PDU 读法的机型角标永远是 0（实测 status 就是 "0"）。
func (mod *Modem) setSMSListCounts(list []sms.SMS) {
	total, unread := len(list), CountUnreadSMS(list)
	mod.mu.Lock()
	mod.SMSTotal, mod.SMSUnread = total, unread
	mod.mu.Unlock()
}

// clearSMSCache 立刻把短信缓存清空并广播出去（换卡/拔卡时用）。
//
// 为什么不能只等下一次读：换卡后模组要重新注网才能读新卡（开了 hotswap_reset 还会复位，
// 有时要 20~30 秒），这段时间**旧卡的短信不能还挂在缓存里**（屏幕/网页会以为还是老卡）。
func (mod *Modem) clearSMSCache() {
	mod.mu.Lock()
	mod.SMSList = nil
	mod.SMSTotal, mod.SMSUnread = 0, 0
	mod.mu.Unlock()
	pushSMS(mod, []sms.SMS{})
	pushSMSCount(mod)
}

// refreshSMSCache 读一遍短信，**全量替换**缓存（ReadSMS 里就是 mod.SMSList = list）
// 并广播给屏幕/网页/微信（微信按指纹去重，不会重复推）。
//
// delay > 0 时先等一下：复位模组（AT+CFUN=1,1）之后模组要重新启动才能读卡。
// minGap > 0 时去抖：距离上次**成功**读到短信不到 minGap 就直接跳过 —— 屏幕
// 断线重连很频繁，每次连上都读一遍既没必要又会和别的读撞车。
func (mod *Modem) refreshSMSCache(delay, minGap time.Duration) {
	if delay > 0 {
		time.Sleep(delay)
	}
	if minGap > 0 {
		mod.mu.Lock()
		last := mod.smsReadAt
		mod.mu.Unlock()
		if !last.IsZero() && time.Since(last) < minGap {
			logger.Debug("sms cache refresh skipped (read recently)",
				zap.Int("modem", mod.ID), zap.Duration("ago", time.Since(last)))
			return
		}
	}
	if _, err := mod.ReadSMS(); err != nil {
		logger.Debug("sms cache refresh failed", zap.Int("modem", mod.ID), zap.Error(err))
	}
}

// noteSMSArrival 新短信上报（+CMTI）时记一笔：未读 +1。
//
// 不在这里读短信（调用方另起 goroutine 去读）：这样角标**立刻**就变，
// 不必等那几百毫秒的 AT 读取。
func (mod *Modem) noteSMSArrival() {
	mod.mu.Lock()
	mod.SMSUnread++
	mod.mu.Unlock()
	pushSMSCount(mod)
}

// SMSStatusUnread 判断单条短信的状态是否为"未读"。
//
//	文本模式 → `REC UNREAD` / `UNREAD`...
//	PDU 模式  → +CMGL 的第 2 格是**数字**：0 = REC UNREAD，1 = REC READ，2/3 = 已存/已发
//
// 空状态判**未读=false**：拿不到状态时宁可少算，不要虚报一个永远消不掉的角标。
func SMSStatusUnread(status string) bool {
	st := strings.TrimSpace(strings.Trim(strings.TrimSpace(status), `"`))
	if st == "" {
		return false
	}
	if strings.Contains(strings.ToUpper(st), "UNREAD") {
		return true
	}
	if n, err := strconv.Atoi(st); err == nil {
		return n == 0
	}
	return false
}

// CountUnreadSMS 统计列表里的未读条数（纯函数，便于单测）
func CountUnreadSMS(list []sms.SMS) int {
	n := 0
	for _, s := range list {
		if SMSStatusUnread(s.Status) {
			n++
		}
	}
	return n
}

// smsPDUReader 决定这台模组读短信要不要走 PDU 模式（不要就返回 nil）：
//
//	① 机型特例表标了 `SMSReadPDU`（实测 FM350-GL：文本模式回的“正文”其实是十六进制 PDU，
//	   按 UCS2 文本解会把 UDH 解成怪字符）→ 用标准 PDU 读法；
//	② 厂商自己声明了 PDUSMSReader（鼎桥 MT5700：文本模式干脆不回正文）→ 用厂商的写法。
//
// 两者分开是因为②是“整个厂商都这样”、①是“同厂个别机型这样”，不能互相带。
func (mod *Modem) smsPDUReader() (PDUSMSReader, bool) {
	if mod.Quirk().SMSReadPDU {
		if r, ok := mod.cmdImpl.(PDUSMSReader); ok {
			return r, true // 厂商另有写法（带 cid 之类）就用它的
		}
		return StandardPDUReader{}, true
	}
	if r, ok := mod.cmdImpl.(PDUSMSReader); ok {
		return r, true
	}
	return nil, false
}

// readSMSRaw 按固件能力选择读短信的方式，返回**未排序**的列表。
func (mod *Modem) readSMSRaw() ([]sms.SMS, error) {
	if r, ok := mod.smsPDUReader(); ok {
		list, err := mod.readSMSViaPDU(r)
		if err == nil {
			return list, nil
		}
		logger.Warn("read sms in PDU mode failed, fall back to text mode",
			zap.Int("modem", mod.ID), zap.Error(err))
	}
	// 必须先切到文本模式：模组默认可能是 PDU 模式（AT+CMGF? 返回 0），
	// 此时 AT+CMGL="ALL" 会直接返回 ERROR，表现为"读不到短信"。
	if _, err := mod.SendAT(mod.cmdImpl.CommandSMSTextMode()); err != nil {
		return nil, err
	}
	resp, err := mod.SendAT(mod.cmdImpl.CommandReadAllSMS())
	if err != nil {
		return nil, err
	}
	// ⚠️ 回 ERROR（例如模式没切过去，PDU 模式下 CMGL="ALL" 会直接报错）**不是**
	// “卡里没短信”：当成错误返回，否则会把空列表缓存下来，屏幕/网页显示 0 条。
	if e := atError(resp); e != "" {
		return nil, fmt.Errorf("read sms failed: %s", e)
	}
	return sms.GroupSMSConcat(sms.ParseSMSList(resp), mod.readConcatInfo()), nil
}

// readSMSViaPDU 用 PDU 模式读一遍短信并自行解码。
//
// 正文、号码、时间戳、分条（UDH 拼接）信息全在 PDU 里，所以不必再单独跑
// readConcatInfo。无论成功与否都会把模式切回文本模式 —— 发短信依赖文本模式。
func (mod *Modem) readSMSViaPDU(r PDUSMSReader) ([]sms.SMS, error) {
	if _, err := mod.SendAT(r.CommandSMSModePDU()); err != nil {
		return nil, err
	}
	defer func() {
		if _, err := mod.SendAT(mod.cmdImpl.CommandSMSTextMode()); err != nil {
			logger.Error("restore text mode failed", zap.Int("modem", mod.ID), zap.Error(err))
		}
	}()
	resp, err := mod.SendAT(r.CommandReadAllSMSPDU())
	if err != nil {
		return nil, err
	}
	list, concat := sms.ParsePDUList(resp)
	if len(list) == 0 {
		return nil, fmt.Errorf("no sms decoded from PDU response")
	}
	return sms.GroupSMSConcat(list, concat), nil
}

// readConcatInfo 切到 PDU 模式读一遍短信，只为解析分条（UDH 拼接）信息。
//
// 文本模式不暴露 UDH，而存储位置的先后顺序不能代表段序（实测同一条长短信
// 既有正序也有逆序存放），因此只有这条路径能可靠还原分条。
// 无论成功与否都会把模式切回文本模式——发短信依赖文本模式。
func (mod *Modem) readConcatInfo() map[string]sms.ConcatInfo {
	if _, err := mod.SendAT("AT+CMGF=0"); err != nil {
		logger.Warn("switch to PDU mode failed", zap.Int("modem", mod.ID), zap.Error(err))
		return nil
	}
	// 用 defer 保证异常路径也回到文本模式
	defer func() {
		if _, err := mod.SendAT(mod.cmdImpl.CommandSMSTextMode()); err != nil {
			logger.Error("restore text mode failed", zap.Int("modem", mod.ID), zap.Error(err))
		}
	}()
	// PDU 模式下 4 = 全部短信（对应文本模式的 "ALL"）
	resp, err := mod.SendAT("AT+CMGL=4")
	if err != nil {
		logger.Warn("read sms in PDU mode failed", zap.Int("modem", mod.ID), zap.Error(err))
		return nil
	}
	return sms.ParsePDUConcatList(resp)
}

// smsWorkerLoop 短信发送队列协程：避免并发发送指令冲突
func (mod *Modem) smsWorkerLoop() {
	for {
		select {
		case <-mod.closeChan:
			return
		case job := <-mod.smsSendQueue:
			err := mod.doSendSMS(job.Phone, job.Content)
			// 防止 RespChan 未接收导致 goroutine 泄漏
			select {
			case job.RespChan <- err:
			case <-time.After(10 * time.Second):
				logger.Warn("sms job response channel full", zap.Int("modem", mod.ID))
			}
		}
	}
}

// SendSMS 入队发送短信（线程安全）
func (mod *Modem) SendSMS(phone, content string) error {
	respChan := make(chan error, 1)
	mod.smsSendQueue <- SMSJob{
		Phone:    phone,
		Content:  content,
		RespChan: respChan,
	}
	select {
	case err := <-respChan:
		return err
	// ⚠️ 外层等待必须覆盖内层的**最坏情况**（等提示符 smsPromptGrace + 等提交确认
	// smsSubmitTimeout），否则会出现“内层还在等模组回执、外层已经报 queue timeout”。
	// 实测 MT5700：以前是 CmdTimeout+5s（8 秒）⇒ 正文发出后 7.5 秒就报 queue timeout。
	case <-time.After(smsPromptGrace + smsSubmitTimeout + 5*time.Second):
		return fmt.Errorf("send sms queue timeout")
	}
}

// validSMSPhone 收信号码校验（写 AT 之前先拦住）。
//
// ⚠️ 必须校验：号码会直接拼进 `AT+CMGS="..."`，带引号/逗号能把 AT 语法改掉。
// 规则：可选开头 `+`（国际号），其余只能是数字；总长 5~21（3GPP 目的地址最长 20 位）。
func validSMSPhone(p string) bool {
	p = strings.TrimSpace(p)
	if len(p) < 5 || len(p) > 21 {
		return false
	}
	for i, r := range p {
		if r == '+' && i == 0 {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// validSMSContent 短信内容校验：非空、不含 Ctrl+Z（0x1A 是发送结束符，混在正文里会被提前截断）、
// 不超过 2000 字符（本程序不做长短信分条，太长由用户自己拆）。
func validSMSContent(c string) bool {
	if strings.TrimSpace(c) == "" || len(c) > 2000 {
		return false
	}
	return !strings.ContainsRune(c, 0x1A)
}

// classifySMSSendResult 判定发送结果（AT+CMGS 的应答）。
//
// ⚠️ 以前这里只看传输层错误，**模组回 ERROR 也当成功**：
// 实测 `AT+CMGS="abc"`（非法号码）会被模组拒绝，接口却回 `ok`，界面提示"发送成功"，
// 用户完全对不上。现在必须确认模组回了 `+CMGS: <mr>`（或至少 OK）。
//
// 错误里带上模组的**原始错误行**（`+CMS ERROR: <code>` 这种）+ 一句针对性的排查建议。
func classifySMSSendResult(resp []string) error {
	if common.RespHasError(resp) {
		return smsSendRejectError(resp)
	}
	for _, line := range resp {
		u := strings.ToUpper(strings.TrimSpace(line))
		if strings.Contains(u, "+CMGS:") || u == "OK" {
			return nil
		}
	}
	return fmt.Errorf("模组没有返回发送确认（+CMGS），收到：%s", strings.Join(resp, " | "))
}

// smsSendRejectError 模组明确拒绝时的统一文案：模组的**原始错误行** + 按错误码给的一句建议。
// 文本模式与 PDU 模式两条发送路径共用（以前 PDU 那条只抛原始错误码，用户在界面上看不懂）。
func smsSendRejectError(resp []string) error {
	line := sms.SmsErrorLine(resp)
	return fmt.Errorf("模组/网络拒绝了这次发送（%s）：%s", line, smsErrorHint(line))
}

// smsErrorHint 按错误码给一句更贴切的排查建议（模组的原话已经带上去了）。
//
// #27（Missing or unknown APN）是实测遇到的：**网络侧**直接拒了这次发送，
// 换成「先激活一条 PDP（拨号上网）」也照样拒 —— 物联网卡 / 纯数据卡常这样（不支持短信发送），
// 或者该网络的短信要走 IMS 而 IMS 还没注册。所以提示要指向“卡/网络”，
// 不能再笼统地说“号码格式不对”，那会把用户带到沟里。
func smsErrorHint(line string) string {
	u := strings.ToUpper(line)
	switch {
	case strings.Contains(u, "#27"), strings.Contains(u, "MISSING OR UNKNOWN APN"):
		return "错误码指向 APN/承载，是网络侧直接拒绝了发送 —— 物联网卡 / 纯数据卡常不支持短信发送，" +
			"或该网络要求短信走 IMS 而 IMS 未注册；换一张普通手机卡试最快"
	case strings.Contains(u, "#330"), strings.Contains(u, "ERROR: 330"):
		return "指向短信中心（SMSC）：用 AT+CSCA? 看看是否为空 / 是否正确"
	case strings.Contains(u, "#21"), strings.Contains(u, "ERROR: 21"),
		strings.Contains(u, "#22"), strings.Contains(u, "ERROR: 22"):
		return "指向号码格式：试试带国家码的写法（如 +8613800138000）"
	// 313 实测（中兴 F50 / 展锐 UMS9620 + 电信流量卡，2026-10-06）：
	// 收短信正常（+CPMS 里有 10 条），发短信**毫秒级**被本地拒绝（根本没走网络），
	// `AT+CMEE=2` 下固件原文是 `SIM failure`；换存储（ME/SM）、内嵌 SMSC、文本模式、
	// `AT+CSMS=0`、`AT+CGSMS=0/2`（强制走 PS 域）全都还是 313 ⇒ 卡侧问题，AT 侧无解。
	case strings.Contains(u, "ERROR: 313"), strings.Contains(u, "#313"),
		strings.Contains(u, "SIM FAILURE"):
		return "313 是模组把这次发送归到 SIM 侧（AT+CMEE=2 时固件原文 SIM failure）：" +
			"实测流量卡/物联卡收短信正常但发短信会被本地拒绝（毫秒级、不走网络），" +
			"换 PS 域（AT+CGSMS=0/2）也没用 ⇒ 这张卡没开通短信发送（MO SMS），AT 侧绕不过去；" +
			"换一张普通手机卡最快验证"
	case strings.Contains(u, "ERROR: 310"), strings.Contains(u, "SIM NOT INSERTED"),
		strings.Contains(u, "ERROR: 311"), strings.Contains(u, "SIM PIN"),
		strings.Contains(u, "ERROR: 316"), strings.Contains(u, "SIM PUK"):
		return "错误码指向 SIM 本身（未插好 / 需要 PIN 或 PUK）：先看「概览」的 SIM 状态（AT+CPIN?）"
	case strings.Contains(u, "ERROR: 314"), strings.Contains(u, "SIM BUSY"),
		strings.Contains(u, "ERROR: 315"), strings.Contains(u, "SIM WRONG"):
		return "错误码指向 SIM 忙 / 卡无效：稍后重试；反复出现就换一张卡试"
	case strings.Contains(u, "ERROR: 331"), strings.Contains(u, "NO NETWORK SERVICE"):
		return "错误码指向网络服务不可用：先确认数据域已注册（概览里的 PS/EPS 域）与信号强度，再试"
	case strings.Contains(u, "ERROR: 332"), strings.Contains(u, "NETWORK TIMEOUT"):
		return "错误码指向网络超时：短信可能仍在提交中，稍后看对方是否收到，别急着重发"
	default:
		return "常见原因是号码格式不对、SIM 未插好、短信中心未配置或该卡未开通短信功能"
	}
}

// doSendSMSPDULocked 走 PDU 模式发一条短信（给声明了 PDUSMSSender 的固件用）。
//
// 流程 —— 厂商自己的 `/opt/mu300/bin/sms send` 就是这么做的，**两步各算一条命令**：
//
//	AT+CMGF=0                切 PDU 模式
//	AT+CMGS=<TPDU 字节数>     第一步，只为要那个 `>` 提示符（没有最终结果码）
//	<PDU 十六进制> + 0x1A     第二步：正文本身就是命令，Ctrl+Z 是结束符
//	→ `+CMGS: <mr>` 紧跟 `OK`
//
// PDU 由 EncodeSMSSubmit 生成，基准是厂商编码器在真机上产出的样例（见 sms_pdu_encode.go）。
//
// ⚠️ 调用方必须**已经持有 atMu**：整段序列一旦被别的指令插队，
// 插进来的那条会被模组当成短信正文吞掉，AT 通道随之错位。
func (mod *Modem) doSendSMSPDULocked(s PDUSMSSender, phone, content string) error {
	pdu, tpduLen, err := sms.EncodeSMSSubmit(phone, content)
	if err != nil {
		return err
	}
	if _, err := mod.sendATLocked(s.CommandSMSModePDU()); err != nil {
		return err
	}
	promptCmd := s.CommandSendSMSPDULength(tpduLen)
	mod.writeChan <- promptCmd
	// 等提示符：与文本模式共用同一套判定（`>` / 0x1A 结尾行 / OK 都算就绪；
	// 到了宽限时间还没有也不硬等 —— 模组本来就会为正文等几十秒）。
	if _, err := mod.waitSendPromptLocked(); err != nil {
		return err
	}
	logger.Info("sending sms in PDU mode",
		zap.Int("modem", mod.ID), zap.String("phone", phone),
		zap.Int("tpdu_len", tpduLen), zap.Int("pdu_hex_len", len(pdu)))
	mod.writeChan <- pdu + string(byte(26)) // 0x1A Ctrl+Z 结束符
	// requireEcho=false：提示符那一拍已经确认过同步，而正文这条命令很多固件不回显，
	// 等回显只会白等一个超时（同文本模式那边对 CMGS 正文的处理）。
	resp, err := mod.readResponseLockedTimeout(promptCmd, false, smsSubmitTimeout)
	if err != nil {
		return fmt.Errorf("没等到模组的发送确认（%w）；短信可能仍在提交中，稍后看对方是否收到，别急着重发", err)
	}
	if common.RespHasError(resp) {
		logger.Warn("sms send (PDU) rejected by modem",
			zap.Int("modem", mod.ID), zap.String("resp", strings.Join(resp, " | ")))
		// 与文本模式共用同一套文案：带模组原始错误行 + 按错误码给的建议
		// （以前这里只抛 `模组拒绝了这条短信：+CMS ERROR: 313`，界面上没法看出是卡的问题）。
		return smsSendRejectError(resp)
	}
	return nil
}

func (mod *Modem) doSendSMS(phone, content string) error {
	// 先做入参校验：号码会拼进 AT 指令，且非法输入不该白占一次 AT 通道
	phone = strings.TrimSpace(phone)
	if !validSMSPhone(phone) {
		return fmt.Errorf("收信号码不合法（只能是数字，可带开头的 +，长度 5~21）：%q", phone)
	}
	if !validSMSContent(content) {
		return fmt.Errorf("短信内容不合法（不能为空、不能含 Ctrl+Z、长度不超过 2000 字符）")
	}
	// 整段发送序列（CMGF → CMGS → 内容 → Ctrl+Z）必须独占 AT 通道，
	// 否则中途插入的其他指令会把短信内容与应答错位
	mod.atMu.Lock()
	defer mod.atMu.Unlock()
	// 有的固件**只能走 PDU 模式发**（中兴 F50 / 展锐 UMS9620 实测：
	// `AT+CSCS?` 是 "HEX"、`AT+CMGF?` 是 0，厂商自己的 sms 脚本也是 PDU）。
	// 单独一条早退分支，下面那整套文本模式逻辑一个字都不受影响。
	if s, ok := mod.cmdImpl.(PDUSMSSender); ok {
		return mod.doSendSMSPDULocked(s, phone, content)
	}
	if _, err := mod.sendATLocked(mod.cmdImpl.CommandSMSTextMode()); err != nil {
		return err
	}
	// ⚠️⚠️ 发送前必须保证 AT+CSMP 合法（<fo>/<vp> 不能为空）—— 实测鼎桥 MT5700M-CN
	// 出厂 `+CSMP: ,,0,0` 时 `AT+CMGS` 会被**静默吞掉**：只回一行 0x1A，之后既不报错也
	// 不回 +CMGS（抓串口 30 秒无输出）。补上手册 §9.7 的 `AT+CSMP=17,167,0,0` 后，
	// 同样的 CMGS 立刻回 OK（实测）。
	// ⚠️ 先确保模组的错误详情是开着的：CMEE=0 时网络拒绝只会回一个裸 `ERROR`，
	// 界面只能给"号码格式 / SIM / 短信中心"这类通用猜测（见 ensureSMSErrorDetailLocked
	// 的实测对照）。这一步是 best-effort：读不到/改不动就照常往下走。
	restoreCMEE := mod.ensureSMSErrorDetailLocked()
	defer restoreCMEE()
	if err := mod.ensureSMSCSMPLocked(); err != nil {
		return err
	}
	// 正文的写法由模组当前的字符集（AT+CSCS）决定：UCS2 下要写成 UTF-16BE
	// 十六进制。号码始终原样（ASCII 数字）—— 实测 FM350-GL 在 UCS2 下也接受
	// `AT+CMGS="13520309333"`，反倒是不接受十六进制号码。
	body, numbers, restore, err := mod.smsSendBody(content, phone)
	defer restore()
	if err != nil {
		return err
	}
	// 号码写法要试（候选见 smsSendBody 的说明）：多数固件在 UCS2 下仍收 ASCII 号码，
	// 但芯讯通 SIM8200 必须把号码也编成 UCS2 十六进制 —— 实测 CSCS=UCS2 时
	// `AT+CMGS="13800138000"` 秒回 ERROR，写成 `00310033…0030` 才回 `>`。
	// ⚠️ 只在"模组明确拒绝"（回了 ERROR，说明它**没有**进入正文输入态）时才换下一种写法；
	// 超时（可能已经在等正文）绝不再发一条 CMGS，那会被当成正文内容吞掉。
	phoneSent := numbers[0]
	var promptless bool
	for i, num := range numbers {
		phoneSent = num
		mod.writeChan <- mod.cmdImpl.CommandSendSMS(num)
		// ⚠️ 必须等模组回 `>` 提示符再发正文：以前这里固定 sleep(200ms)，在 FM350 上
		// 不够（提示符本来就要几百毫秒），正文先到会被模组丢掉 —— 表现为 AT+CMGS 一路超时，
		// 而且之后**所有** AT 指令都被当成正文吞掉，AT 通道看上去“卡死”。
		promptless, err = mod.waitSendPromptLocked()
		if err == nil {
			break
		}
		if i == len(numbers)-1 || !errors.Is(err, errSMSPromptRejected) {
			return err
		}
		logger.Info("modem rejected the ASCII number under UCS2; retrying with a UCS2-encoded number",
			zap.Int("modem", mod.ID), zap.String("phone", phone))
	}
	mod.writeChan <- body + string(byte(26)) // 0x1A Ctrl+Z 结束符
	// 正文的应答（+CMGS/OK/ERROR）没有自己的回显（提示符前一拍已确认同步）
	//
	// ⚠️ 这里用**专用超时**：`+CMGS: <mr>` 要等网络回执，实测 MT5700 上 3 秒的
	// CmdTimeout 等不到（正文已发出，但确认慢）⇒ 以前会报“发送失败”让用户以为没发出去。
	// 不回 `>` 的固件（MT5700 实测）也**不回 +CMGS 确认**（抓串口 30 秒没任何输出）⇒
	// 只等一小会儿（smsPromptlessConfirmWait）。
	wait := smsSubmitTimeout
	if promptless {
		wait = smsPromptlessConfirmWait
	}
	resp, err := mod.readResponseLockedTimeout(mod.cmdImpl.CommandSendSMS(phoneSent), false, wait)
	if err != nil {
		// 正文发出去之后模组**一个字都不回**：本地完全无法核实，只能如实报失败。
		//
		// ⚠️ 以前这里对 promptless 固件是「按已提交处理、返回成功」—— 实测证明那是错的：
		// 鼎桥 MT5700M-CN（固件 V200R001C20B016）的**正文输入态压根不完成**：
		//
		//	AT+CMGS="13520309333"  → 只回一行 `AT+CMGS="13520309333"<0x1A>`（不是 `>`）
		//	正文 + Ctrl+Z            → 没有任何回应（既无 `+CMGS: <mr>` 也无 `+CMS ERROR`）
		//	AT+CMGW（只存储、不发网络）→ 同样不完成，存储里也不新增条目
		//
		// 即：换文本/PDU 模式、号码带不带 `+`、ESC 结束、双 Ctrl+Z、CR 结束、
		// 整条一次性突发写，结果都一样。所以这里必须报失败，不能再让界面显示
		// “发送成功”让用户白等（对方收不到、用户还会反复重发）。
		if promptless {
			logger.Warn("promptless modem: no +CMGS confirmation; report failure",
				zap.Int("modem", mod.ID), zap.String("phone", phone), zap.Error(err))
			return fmt.Errorf("模组收到正文后没有任何回应（既没有 +CMGS 确认也没有错误码），" +
				"这条短信很可能没有发出去（实测鼎桥 MT5700M-CN 固件存在该问题：连只存储不发送的 " +
				"AT+CMGW 都不完成）；建议换卡/换模块或联系模组固件方")
		}
		return fmt.Errorf("没等到模组的发送确认（%w）；短信可能仍在提交中，稍后看对方是否收到，别急着重发", err)
	}
	if err := classifySMSSendResult(resp); err != nil {
		logger.Warn("sms send rejected by modem",
			zap.Int("modem", mod.ID), zap.String("resp", strings.Join(resp, " | ")))
		return err
	}
	return nil
}
