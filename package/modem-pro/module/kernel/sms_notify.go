package kernel

import (
	"fmt"
	"modemd/module/common"
	"modemd/module/config"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ====================== 新短信上报（AT+CNMI） ======================

// QuerySMSStorage 查询短信存储用量
func (mod *Modem) QuerySMSStorage() (*SMSStorage, error) {
	resp, err := mod.SendAT(mod.cmdImpl.CommandQuerySMSStorage())
	if err != nil {
		return nil, err
	}
	st, ok := parseSMSStorage(resp)
	if !ok {
		return nil, fmt.Errorf("cannot parse CPMS response: %s", strings.Join(resp, "|"))
	}
	// 支持的存储区（best-effort）：部分固件不认 AT+CPMS=?，拿不到就留空，
	// 页面上退回 SM / ME / MT 固定三个选项。
	// 这是 3GPP 标准指令，各家写法一致，所以直接用字面量（同 network.go 里的 AT+CGREG?）。
	if sup, err := mod.SendAT("AT+CPMS=?"); err == nil && !common.RespHasError(sup) {
		st.Supported = parseCPMSSupported(sup)
	}
	return &st, nil
}

// SetSMSStorage 切换短信存储区（读/写/接收三处一起切，与 applySMSStorage 一致）。
//
// 为什么要能在页面上切：存储区决定 `AT+CMGL` 读的是哪一片 —— SIM 卡（SM，常见 40 条）
// 与模块内存（ME，常见 99 条甚至更多）里的短信是**分开**的，模组默认存哪个看固件，
// 换一张卡/换个模组就可能“明明有短信却读不到”。
//
// 流程：校验名称 → 下发 → **回读确认**（模组回 OK 不代表真切了）→ 写回 toml
// （失败不致命：本次运行已生效，只是重启后会回到配置里的旧值）。
func (mod *Modem) SetSMSStorage(mem string) (*SMSStorage, error) {
	mem = strings.ToUpper(strings.TrimSpace(mem))
	if !validSMSMem(mem) {
		return nil, fmt.Errorf("存储区只支持 SM（SIM 卡）/ ME（模块）/ MT（自动），当前 %q", mem)
	}
	// 先看当前状态：既拿到了支持列表（可以给出更准的报错），也避免重复下发
	cur, err := mod.QuerySMSStorage()
	if err != nil {
		return nil, err
	}
	if len(cur.Supported) > 0 && !containsFold(cur.Supported, mem) {
		return nil, fmt.Errorf("该模组不支持 %s 存储区（支持：%s）", mem, strings.Join(cur.Supported, " / "))
	}
	if strings.EqualFold(cur.Current, mem) {
		return cur, nil // 已经在这个存储区上，不必再发指令
	}
	resp, err := mod.SendAT(mod.cmdImpl.CommandSMSStorage(mem))
	if err != nil {
		return nil, err
	}
	if e := atError(resp); e != "" {
		return nil, fmt.Errorf("模组拒绝切换到 %s（AT+CPMS 回 %s）", mem, e)
	}
	logger.Info("sms storage switched", zap.Int("modem", mod.ID), zap.String("mem", mem))

	// 回读确认：拿新状态回给前端（顺便验证真的切过去了）。
	// ⚠️ 要重试几次：实测 EM05 刚下发完 AT+CPMS 的那一瞬间，`AT+CPMS?` 会报成 `MT`
	// （存储池已经是模块的 99 条，只是名字还没稳定），等一两百毫秒再读就是 `ME`。
	var st *SMSStorage
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(300 * time.Millisecond)
		}
		st, err = mod.QuerySMSStorage()
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(st.Current, mem) {
			break
		}
	}
	if !strings.EqualFold(st.Current, mem) {
		return st, fmt.Errorf("已下发 %s，但回读仍是 %s（固件可能忽略了）", mem, st.Current)
	}
	// 写回配置：下次启动直接就用这个存储区
	if err := config.SetSMSMemory(mem); err != nil {
		logger.Warn("persist sms storage failed", zap.Int("modem", mod.ID), zap.Error(err))
	}
	return st, nil
}

// containsFold 忽略大小写的字符串包含判断
func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// DeleteSMS 删除短信：index >= 0 删除指定位置；index < 0 删除全部
func (mod *Modem) DeleteSMS(index int) error {
	resp, err := mod.SendAT(mod.cmdImpl.CommandDeleteSMS(index))
	if err != nil {
		return err
	}
	if e := atError(resp); e != "" {
		return fmt.Errorf("delete sms failed: %s", e)
	}
	logger.Info("sms deleted", zap.Int("modem", mod.ID), zap.Int("index", index))
	return nil
}

// MarkSMSRead 把某条短信标成**已读** —— 屏幕上看过之后由它调（用户要求："查看完了就把未读处理一下"）。
//
// 做法就是按存储位置读一遍（AT+CMGR=<index>）：3GPP TS 27.005 规定读一条 REC UNREAD
// 的短信时模组会把存储里的状态改成 REC READ（手机上"点开就变成已读"就是这个机制）。
// 读到的正文这里不用（列表里有），只要那个状态变化；读完顺手重读一遍列表，
// 条数/未读数（角标）立刻回落并广播出去。
//
// ⚠️ index 是**存储位置**（SMS.Index），不是列表下标 —— 列表是按时间倒序排的，两者不同。
func (mod *Modem) MarkSMSRead(index int) error {
	if index < 0 {
		return nil
	}
	resp, err := mod.SendAT("AT+CMGR=" + strconv.Itoa(index))
	if err != nil {
		return err
	}
	if e := atError(resp); e != "" {
		return fmt.Errorf("read sms %d failed: %s", index, e)
	}
	logger.Info("sms marked read", zap.Int("modem", mod.ID), zap.Int("index", index))
	_, _ = mod.ReadSMS() // 状态变了 ⇒ 重读列表并广播（角标一起回落）
	return nil
}

// ---- from: modem_sms_notify.go ----

// CNMISettings AT+CNMI 的五个参数（3GPP TS 27.005）。

// CNMISettings AT+CNMI 的五个参数（3GPP TS 27.005）。
type CNMISettings struct {
	Mode int `json:"mode"` // <mode>：0 不报 / 1 有链路才报 / 2 空闲直报 + 忙时缓冲 / 3 直接报
	MT   int `json:"mt"`   // <mt>：新短信上报方式，1 = 用 +CMTI 报（本程序解析这种）
	BM   int `json:"bm"`   // 广播消息
	DS   int `json:"ds"`   // 状态报告
	BFR  int `json:"bfr"`  // 缓冲清空方式
}

// ReportsNewSMS 新短信是否会以 +CMTI 的形式主动上报（推送链路的先决条件）。
// mt=2/3 会改用 +CMT（URC 里直接带正文），本程序不解析那种报文，所以不算可用。
func (s CNMISettings) ReportsNewSMS() bool { return s.Mode != 0 && s.MT == 1 }

// parseCNMI 解析 AT+CNMI? 应答：
//
//	+CNMI: 2,1,0,0,0
func parseCNMI(resp []string) (CNMISettings, bool) {
	const head = "+CNMI:"
	for _, line := range resp {
		idx := strings.Index(strings.ToUpper(line), head)
		if idx < 0 {
			continue
		}
		f := common.SplitATFields(line[idx+len(head):])
		if len(f) < 2 {
			continue
		}
		st := CNMISettings{Mode: common.AtIntOr0(f, 0), MT: common.AtIntOr0(f, 1)}
		if len(f) >= 5 {
			st.BM, st.DS, st.BFR = common.AtIntOr0(f, 2), common.AtIntOr0(f, 3), common.AtIntOr0(f, 4)
		}
		return st, true
	}
	return CNMISettings{}, false
}

// applySMSNotify 下发新短信上报配置（AT+CNMI）并回读确认，返回是否确认生效。
//
// 之前完全依赖模组自身的 CNMI 值：本机实测恰好是 2,1,0,0,0，所以 +CMTI 一直能收到。
// 但换模组、或换卡后 ROM/默认配置不同，一旦变成 0,0,0,0,0，新短信推送会**静默失效**
// （页面上看不出任何异常，只是永远不弹窗），所以启动时显式设一次。
func (mod *Modem) applySMSNotify() bool {
	resp, err := mod.SendAT(mod.cmdImpl.CommandSMSNotify())
	if err != nil {
		logger.Warn("set sms notify failed", zap.Int("modem", mod.ID), zap.Error(err))
		return false
	}
	if e := atError(resp); e != "" {
		logger.Warn("modem rejected sms notify", zap.Int("modem", mod.ID), zap.String("resp", e))
		return false
	}
	// 回读确认：模组常回 OK 却把不支持的值静默忽略（与 APN / mode_pref 同一个坑）
	qresp, err := mod.SendAT(mod.cmdImpl.CommandQuerySMSNotify())
	if err != nil {
		logger.Warn("query sms notify failed", zap.Int("modem", mod.ID), zap.Error(err))
		return false
	}
	st, ok := parseCNMI(qresp)
	if !ok {
		logger.Warn("cannot parse CNMI response",
			zap.Int("modem", mod.ID), zap.String("resp", strings.Join(qresp, "|")))
		return false
	}
	if !st.ReportsNewSMS() {
		logger.Warn("modem will not report new sms as +CMTI",
			zap.Int("modem", mod.ID), zap.Int("mode", st.Mode), zap.Int("mt", st.MT))
		return false
	}
	logger.Info("sms notify applied",
		zap.Int("modem", mod.ID), zap.Int("mode", st.Mode), zap.Int("mt", st.MT))
	return true
}

// reapplySMSConfigAfterReset 重启模组后重新下发短信相关配置（CNMI + CPMS）。
//
// 这些配置只在串口 Start 时下发过。AT+CFUN=1,1 复位后模组可能回到默认值
// （CNMI 变了 → 新短信不再报 +CMTI，推送静默失效；存储区变了 → 读不到新短信），
// 而端口没有重新枚举时 Start 不会再跑一遍，所以这里等模组回来再重试几次。
// 全程 best-effort：失败只记日志，不影响重启本身。
func (mod *Modem) reapplySMSConfigAfterReset() {
	go func() {
		for attempt, wait := range []time.Duration{30 * time.Second, 15 * time.Second, 15 * time.Second} {
			time.Sleep(wait)
			if !mod.applySMSNotify() {
				logger.Warn("re-apply sms notify after reset failed",
					zap.Int("modem", mod.ID), zap.Int("attempt", attempt+1))
				continue
			}
			mod.applySMSStorage()
			logger.Info("sms config re-applied after reset", zap.Int("modem", mod.ID))
			return
		}
	}()
}

// ---- from: sms/pdu.go ----

//
// 为什么需要它：实测鼎桥 MT5700（固件 V200R001C20B016）在**文本模式**下
// `AT+CMGL="ALL"` / `AT+CMGR=<idx>` **只回 `+CMGL: <位置>,"REC READ","号码",,"时间"` 头，
// 不回正文**（单条、长短信都一样）→ 界面短信列表有条目、内容全空。
// 切到 PDU 模式（AT+CMGF=0 + AT+CMGL=4）后正文完整，所以这台固件必须走 PDU 读取。
//
// 号码/时间与文本模式头互相对得上（实测 idx 0/1/2/8/9/10 全对），说明解码口径正确。
//
// 支持：
//   - SMS-DELIVER（TP-MTI=0，收到的）与 SMS-SUBMIT（TP-MTI=1，已发送件）
//   - UDH 拼接 IE（8 位引用 0x00 / 16 位引用 0x08）——复用 ParseConcatUDH 的口径，
//     所以长短信照样能合成分条
//   - 字符集：GSM 7 位默认字母表（含 0x1B 扩展表）、UCS2、8 位数据（按十六进制展示）
//   - 时间戳：BCD 的 TP-SCTS → "YY/MM/DD,HH:MM:SS±ZZ"（与文本模式同格式，TimeKey 直接可用）
//
// 实测样例（本机 SM 存储里的 11 条，`AT+CMGL=4` 原样）：
//
//	+CMGL: 2,1,,23
//	0891688109520000F0240BA13125309033F3000862908222120423044F60597D
//	→ 号码 13520309333、时间 26/09/28,22:21:40+32、正文「你好」（DCS=0x08 UCS2）
// ErrNotSMS 该 PDU 不是可解码的短信（状态报告 / 回执 / 未知 TP-MTI）。
// 调用方按"有条目但读不出正文"处理，不会把整段应答丢掉。

// 字符集（DCS 的 bit3-2）
const ()

// ---- from: sms/sms.go ----

//
// AT+CMGL 文本模式应答示例（实测移远 RM502Q-GL）：
//
//	+CMGL: 0,"REC UNREAD","10001",,"25/04/29,10:56:44+32"
//	002E003100380039002E0063006E...
//	+CMGL: 1,"REC READ","+8613800138000","","25/04/29,10:56:44+32"
//	hello from mom
//
// 关键点：第 3、4 个字段之间的 alpha（别名）字段，模组可能返回
//
//	空且不带引号  →  ,"10001",,        （实测如此，固定正则匹配不到）
//	带引号的空串  →  "10001",""
//
// 因此必须按"引号感知"的方式切分字段，不能用固定正则。
