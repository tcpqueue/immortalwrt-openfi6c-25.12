package kernel

import (
	"modemd/module/atparse"
	"modemd/module/meig"
	"modemd/module/unisoc"
	"strings"
)

// ====================== 可选能力：附加网络信息 / 供电电压（引擎侧） ======================
//
// 解析在 net.go（Extra / ParseExtraNetInfo / ParseHFREQINFO / ParseCBC，
// 含 CNWINFO / HFREQINFO / CBC 三种方言的实测说明）；
// 这里只剩"该不该发指令、发哪条"——这就是"其它模组不受影响"的兑现方式。

// extraNetInfoCommand 附加网络信息指令。**没声明该能力的厂商返回空串**，
// 上层看到空串连指令都不发 —— 这就是"其它模组不受影响"的兑现方式。
func (mod *Modem) extraNetInfoCommand() string {
	if eq, ok := mod.cmdImpl.(ExtraNetInfoQuerier); ok {
		return eq.CommandExtraNetInfo()
	}
	return ""
}

// voltageCommand 供电电压指令：
//  1. 机型特例表的 voltageCmd（如 EM05 的 AT+CBC）—— 用于“同厂只有部分型号支持”；
//  2. 否则看厂商有没有声明 VoltageQuerier（如芯讯通）；
//  3. 都没有返回空串 → 上层连指令都不发，其它模组行为不变。
//
// ⚠️ 内部会取 mod.mu（机型特例），不要在已持锁的地方调。
func (mod *Modem) voltageCommand() string {
	if c := mod.Quirk().VoltageCmd; c != "" {
		return c
	}
	if vq, ok := mod.cmdImpl.(VoltageQuerier); ok {
		return vq.CommandVoltage()
	}
	return ""
}

// ambrCommand 签约速率查询指令（空串 = 本机不发）：
//  1. 机型特例表的 ambrCmd（美格 SRM 系列：`AT^DSAMBR=<cid>`，vendor/meig.sh 的 cmd_dsambr）；
//  2. 否则看厂商有没有声明 AMBRQuerier（移远 `AT+QNWCFG="nr5g_ambr"`、鼎桥 `AT^DSAMBR=1`）。
//
// ⚠️ 内部会取 mod.mu（机型特例），不要在已持锁的地方调。
func (mod *Modem) ambrCommand() string {
	if c := mod.Quirk().AMBRCmd; c != "" {
		return c
	}
	if q, ok := mod.cmdImpl.(AMBRQuerier); ok {
		return q.CommandAMBR()
	}
	return ""
}

// parseAMBRForModem 选解析：**按指令文本**分派到它自己的布局。
//
// ⚠️ 不要写成"凡是有 Quirk.AMBRCmd 的走美格那套" —— 那是一种"新机型默认落到旧解析"的写法，
// 正是本项目踩过的坑（MeigCommand 继承 QuectelCommand 那两次）。
//
//	AT+C5GQOSRDP  3GPP 5GS QoS：<cid>,<5QI>,…,<DL_SAMBR>,<UL_SAMBR>，kbit/s
//	              —— 有方 N510M 走 Quirk 声明、展锐 UMS9620（中兴 F50）走 AMBRQuerier，
//	                 指令名本身就能定布局，所以**不看它是怎么声明的**，统一进同一套解析。
//	AT^DSAMBR…    **同名不同布局**：只有机型特例（美格 SRM）声明的才是 f9=UL/f10=DL；
//	              鼎桥从 AMBRQuerier 拿到的同名指令是 `<cid>,<DL>,<UL>,"<apn>",<QCI>`，走通用解析。
//
// 其余（移远 AT+QNWCFG="nr5g_ambr"、AT+CGCONTRDP 尾部扩展）走通用 ParseAMBR。
//
// ⚠️ 两条路都按**拨号的那条 PDP 上下文**选行（`dialedPDP`）：多行应答里 IMS 与数据 APN 各占一行，
// 取错就会把 IMS 的档位当成拨号的档位显示出来。
func parseAMBRForModem(mod *Modem, cmd string, resp []string) (*atparse.AMBRRate, bool) {
	cid, apn := mod.dialedPDP()
	up := strings.ToUpper(strings.TrimSpace(cmd))
	if strings.HasPrefix(up, "AT+C5GQOSRDP") {
		return unisoc.ParseC5GQOSRDPContext(resp, cid)
	}
	q := mod.Quirk()
	if q.AMBRCmd != "" && q.AMBRCmd == cmd {
		return meig.ParseDSAMBR(resp)
	}
	return atparse.ParseAMBRContext(resp, cid, apn)
}

// ---- from: modem_quirk.go ----
//
// 特例表本身在 atcmd.go（纯数据 + 纯匹配）；
// 这里只负责“拿到本模组的型号串再查表”，因为型号存在 Modem 里、要加锁读。
// Quirk 返回当前模组的特例（没命中就返回零值）。
//
// ⚠️ 内部会取 mod.mu，**不要在已经持有 mod.mu 的地方调用**。
