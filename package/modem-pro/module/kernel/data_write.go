package kernel

import (
	"errors"
	"fmt"
	"go.uber.org/zap"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"modemd/module/atparse"
	"modemd/module/common"
	"modemd/module/config"
	"modemd/module/modelquirk"
)

// ====================== 写入 ======================

// SetAPN 改写某个 PDP 上下文的 APN，并回读确认。
// 校验不通过直接返回错误（不发 AT）；模组拒绝了也会如实报错。
func (mod *Modem) SetAPN(cid int, pdpType, apnName string) (*Status, error) {
	apnName = strings.TrimSpace(apnName)
	pdpType = strings.ToUpper(strings.TrimSpace(pdpType))
	if !ValidCID(cid) {
		return nil, fmt.Errorf("cid 必须在 0~42 之间，当前 %d", cid)
	}
	if !ValidPDPType(pdpType) {
		return nil, fmt.Errorf("PDP 类型只支持 IP / IPV6 / IPV4V6，当前 %q", pdpType)
	}
	if !ValidAPN(apnName) {
		return nil, fmt.Errorf("APN 只能包含字母、数字、. - _，且长度 1~100（当前 %q）", apnName)
	}
	cmd := CmdCGDCONT(cid, pdpType, apnName)
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return nil, err
	}
	if common.RespHasError(resp) {
		return nil, fmt.Errorf("modem rejected %s", cmd)
	}
	logger.Info("APN written", zap.Int("modem", mod.ID),
		zap.Int("cid", cid), zap.String("pdp_type", pdpType), zap.String("apn", apnName))

	// 回读确认：AT 写完只回 OK，不代表值真的存下了（实测有些固件会静默忽略非法组合）
	st, err := mod.QueryParams()
	if err != nil {
		return nil, err
	}
	if !APNInContexts(st.Contexts, cid, apnName) {
		logger.Warn("APN read-back mismatch", zap.Int("modem", mod.ID), zap.Int("cid", cid))
		return st, fmt.Errorf("模组已回 OK，但回读 cid=%d 的 APN 不是 %q（可能被固件忽略）", cid, apnName)
	}
	return st, nil
}

// DeleteAPN 删除某个 PDP 上下文的 APN 配置（AT+CGDCONT=<cid>），并回读确认。
//
// 删完模组就不再上报这个 cid（有的固件是上报成空 APN），两种都算删成功。
// 少数固件不支持删除，会回 ERROR —— 如实报错，不回退成"只清空 APN"（那会留下一条空
// 上下文，用户以为删干净了，实际上拨号候选里还挂着它）。
//
// 主动拦下的两种情况：该 cid 本来就没配 APN（没得删，别顺手清掉别人的上下文）、
// 该 cid 还激活着（先在界面上停用，不在删除时偷偷把链路断掉）。
func (mod *Modem) DeleteAPN(cid int) (*Status, error) {
	if !ValidCID(cid) {
		return nil, fmt.Errorf("cid 必须在 0~42 之间，当前 %d", cid)
	}
	pre, err := mod.QueryParams()
	if err != nil {
		return nil, err
	}
	target := ByCID(pre.Contexts, cid)
	if target == nil || strings.TrimSpace(target.APN) == "" {
		return nil, fmt.Errorf("模组里的 cid=%d 本来就没有 APN 配置，没有可删除的", cid)
	}
	if target.Active {
		return nil, fmt.Errorf("cid=%d 当前处于激活状态，请先「停用」再删除（避免删配置时链路状态不明）", cid)
	}
	cmd := CmdCGDCONTDelete(cid)
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return nil, err
	}
	if common.RespHasError(resp) {
		return nil, fmt.Errorf("模组拒绝 %s（部分固件不支持删除 PDP 上下文）", cmd)
	}
	logger.Info("APN deleted", zap.Int("modem", mod.ID),
		zap.Int("cid", cid), zap.String("apn", target.APN))

	// 回读确认：AT 写完只回 OK，不代表真删掉了（实测有些固件会静默忽略）
	st, err := mod.QueryParams()
	if err != nil {
		return nil, err
	}
	if c := ByCID(st.Contexts, cid); c != nil && strings.TrimSpace(c.APN) != "" {
		logger.Warn("APN read-back mismatch after delete", zap.Int("modem", mod.ID), zap.Int("cid", cid))
		return st, fmt.Errorf("模组已回 OK，但回读 cid=%d 的 APN 仍是 %q（可能被固件忽略）", cid, c.APN)
	}
	return st, nil
}

// PDP 激活是"异步"的：模组回 OK 时数据链路可能还在协商，立刻回读会误判成失败，
// 所以给几次机会（间隔 800ms，最多约 4s）。
const (
	pdpConfirmTries = 5
	pdpConfirmGap   = 800 * time.Millisecond
)

// pdpWaitActive 回读确认 cid 的激活状态是否已变成 want（AT+CGACT?）。
func (mod *Modem) pdpWaitActive(cid int, want bool) bool {
	for i := 0; i < pdpConfirmTries; i++ {
		if i > 0 {
			time.Sleep(pdpConfirmGap)
		}
		resp, err := mod.SendAT("AT+CGACT?")
		if err != nil || common.RespHasError(resp) {
			continue
		}
		if ParseCGACT(resp)[cid] == want {
			return true
		}
	}
	return false
}

// pdpCommandTimeout 激活 / 停用 PDP（AT+CGACT）的等待超时。
//
// 机型特例可单独加长（Quirk.PDPTimeout，实测 FM650-CN 要 5~20s）；
// 没写特例就用配置的 cmd_timeout；配置也没设（单测直接构造 Config）时兜底 3s。
func (mod *Modem) pdpCommandTimeout(q modelquirk.Quirk) time.Duration {
	if q.PDPTimeout > 0 {
		return q.PDPTimeout
	}
	if config.Cfg.Serial.CmdTimeout > 0 {
		return config.Cfg.Serial.CmdTimeout
	}
	return 3 * time.Second
}

// pdpActivateVerifyAfterTimeout AT+CGACT 超时后，是否改用回读判定而不是直接报错。
//
// 只有"该型号本来就可能不回最终结果"（Quirk.NoReplyOnPDPActivate）**且**错误确实是超时时才成立；
// 打开 / 写入失败等其它错误照旧上报，所以别的型号行为不变。
func pdpActivateVerifyAfterTimeout(err error, q modelquirk.Quirk) bool {
	return q.NoReplyOnPDPActivate && errors.Is(err, errATTimeout)
}

// SetPDPActive 激活 / 停用某个 PDP 上下文（AT+CGACT），并回读确认。
//
// reconnect=true 表示「重新激活」：已激活的先断开再激活。这是让刚写入的 APN
// 立即生效的标准做法 —— 上下文重新协商时才会按 AT+CGDCONT 的最新配置建链路。
//
// 主动拒绝的两种情况：cid 没配过、配额超限停用期间不许激活（否则等于绕过限制）。
//
// ⚠️ **内部上下文（IMS / sos）现在也允许手动激活 / 重新激活** ——
// 有些模组（VoLTE / 需要 IMS 的场景）出厂时 IMS 上下文是未激活的，不给手动启动的话
// 就一直起不来；而停用内部上下文实测有的固件会直接回 ERROR（如 RM502Q-GL 的
// `AT+CGACT=0,2`），所以"重新激活"里的那一步断开是 **best-effort**：被拒就跳过、照样去激活。
func (mod *Modem) SetPDPActive(cid int, active, reconnect bool) (*Status, error) {
	if !ValidCID(cid) {
		return nil, fmt.Errorf("cid 必须在 0~42 之间，当前 %d", cid)
	}
	if reconnect {
		// 「重新激活」语义就是激活，不看请求里的 active
		active = true
	}
	// 先看清这个 cid 是什么、当前激活没有（决定要不要先断开）
	pre, err := mod.QueryParams()
	if err != nil {
		return nil, err
	}
	target := ByCID(pre.Contexts, cid)
	if target == nil {
		return nil, fmt.Errorf("模组未上报 cid=%d 的 PDP 上下文（先写入 APN 再激活）", cid)
	}
	if target.Internal {
		logger.Info("pdp internal context touched manually",
			zap.Int("modem", mod.ID), zap.Int("cid", cid), zap.String("apn", target.APN))
	}
	if active {
		if qs := quotaState(mod); qs.Blocked {
			return nil, fmt.Errorf("流量配额已超限，数据连接是配额停用的；请先到「流量配额」卡片解除限制（或提高上限）再激活")
		}
	}

	if !target.Internal && mod.usesTDTechNDIS() {
		if err := mod.setTDTechNDIS(cid, active, reconnect); err != nil {
			return pre, err
		}
		if reconnect {
			clearTrafficBaseline(mod)
		}
		target.Active = active
		return pre, nil
	}

	steps := Steps(active, target.Active, reconnect)
	quirk := mod.Quirk()
	pdpTO := mod.pdpCommandTimeout(quirk)
	for _, on := range steps {
		cmd := CmdCGACT(cid, on)
		resp, err := mod.SendATTimeout(cmd, pdpTO)
		if err != nil {
			// ⚠️ 实测 FM650-CN：AT+CGACT 可能**不给最终结果**（既无 OK 也无 ERROR）——
			// 超时不等于失败，改由下面的 AT+CGACT? 回读判定；这样也不会因为把半截应答
			// 留在通道里而拖累下一条指令（用户看到的"AT+CGDCONT? 超时"就是这么来的）。
			if !pdpActivateVerifyAfterTimeout(err, quirk) {
				return nil, err
			}
			logger.Warn("pdp activate got no final reply, verify by AT+CGACT? read-back",
				zap.Int("modem", mod.ID), zap.Int("cid", cid), zap.String("cmd", cmd))
			resp = nil
		}
		if common.RespHasError(resp) {
			// 内部上下文的"先断开"是尽力而为：实测有些固件就是不许手动停 IMS，
			// 这一步被拒不该挡住后面的激活（否则"重新激活"内部上下文永远做不成）
			if !on && target.Internal && DeactivateBestEffort(target.Internal) {
				logger.Info("pdp deactivate refused, continue activating",
					zap.Int("modem", mod.ID), zap.Int("cid", cid), zap.String("cmd", cmd))
				continue
			}
			return nil, fmt.Errorf("模组拒绝 %s（常见原因：SIM 未注册 / APN 不可用 / 该 cid 没有配置；"+
				"内部上下文 IMS·sos 还可能是固件不允许手动停用）", cmd)
		}
	}
	want := steps[len(steps)-1]
	logger.Info("PDP active changed", zap.Int("modem", mod.ID), zap.Int("cid", cid),
		zap.Bool("active", want), zap.Bool("reconnect", reconnect), zap.String("apn", target.APN))

	// 回读确认：模组回 OK ≠ 真的连上了（未注册 / APN 不可用时常回 OK 却没有链路）
	if !mod.pdpWaitActive(cid, want) {
		st, qerr := mod.QueryParams()
		if qerr != nil {
			return nil, fmt.Errorf("回读 cid=%d 仍未%s（AT+CGACT 未确认，且重新读取参数也失败）", cid, ActiveWord(want))
		}
		return st, fmt.Errorf("回读 cid=%d 仍未%s（AT+CGACT 未确认；检查模块注册状态与该 APN 是否可用）", cid, ActiveWord(want))
	}
	if reconnect {
		// 链路重建后模组的流量计数器可能回退/重建，清掉本地差分基线，
		// 免得把"计数器重来"算成一次暴涨（同 ResetTraffic 的处理）
		clearTrafficBaseline(mod)
	}
	// 再读一遍，把最新的激活状态返回给前端
	return mod.QueryParams()
}

// SetNetSelect 设置选网方式：mode 为 "auto"（自动）或 "manual"（手动，需给 PLMN）。
//
// 手动选网会触发一次重新选网（可能短暂断网），并且模组**不会**自动回退，
// 所以前端会提示用户；要恢复自动传 mode="auto" 即可。
func (mod *Modem) SetNetSelect(mode, plmn string) (*Status, error) {
	var cmd string
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "auto", "automatic":
		cmd = "AT+COPS=0"
	case "manual":
		plmn = strings.TrimSpace(plmn)
		if !ValidPLMN(plmn) {
			return nil, fmt.Errorf("手动选网需要 5~6 位数字的 PLMN（如 46011），当前 %q", plmn)
		}
		// 1 = 手动，2 = 数字格式的运营商号
		cmd = fmt.Sprintf(`AT+COPS=1,2,"%s"`, plmn)
	default:
		return nil, fmt.Errorf("mode 只支持 auto / manual，当前 %q", mode)
	}
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return nil, err
	}
	if common.RespHasError(resp) {
		return nil, fmt.Errorf("modem rejected %s", cmd)
	}
	logger.Info("net select written", zap.Int("modem", mod.ID), zap.String("cmd", cmd))

	// 回读：记下模式（注册本身要几秒到几十秒，不必在这里等）
	st, err := mod.QueryParams()
	if err != nil {
		return nil, err
	}
	return st, nil
}

// ---- from: traffic/traffic.go ----

// Package traffic 是「数据流量统计」的纯逻辑部分：把模组应答解析成累计字节数
// 或瞬时速率，并把 byte/s 格式化成人读文本。
//
// 3GPP 没有"当前速率"这一类指令，只能采样模组的字节统计做差分。实机上面见过两种形状：
//
//  1. **累计计数器**（移远 AT+QGDCNT?、鼎桥 AT^DSFLOWQRY）：
//
//  1. 首次下发使能指令让模组开始计数（只影响计数开关，不动网络状态）
//
//  2. 周期查询 → 累计发送 / 接收字节
//
//  3. 相邻两次采样的差值 ÷ 时间差 = 上行/下行速度
//     计数器变小说明模组侧复位（掉网 / PDP 重建），此时只重置基线，不产生负值。
//
//  2. **瞬时速率**（广和通 AT+GTSTATIS?，实测 FM160-EAU / FM650-CN）：
//     `+GTSTATIS: <下载 B/s>,<上传 B/s>,<..>,<..>` —— 没有累计计数器，
//     速率直接用，累计量靠"速率 × 时间窗"**积分**得到（界面会标"估算"）。
//
// 差分 / 积分 / 缓存这些**要状态**的部分在 kernel（见 data.go），
// 因为那份状态挂在 Modem 上、还要和串口查询节奏绑在一起。
// TrafficSample 一次采样：计数式模组用 Sent/Recv（累计字节），
// 速率式模组用 RateMode + UpBps/DownBps。

type TrafficSample struct {
	Sent uint64
	Recv uint64
	At   time.Time
	// RateMode = true 时 Sent/Recv 无效，速率由模组直接上报（如广和通 AT+GTSTATIS?）
	RateMode bool
	UpBps    float64
	DownBps  float64
}

// Rate 上行/下行速度（byte/s）与累计量，用于前端展示
type Rate struct {
	Supported bool      `json:"supported"`  // 模组是否上报了流量统计
	UpBps     float64   `json:"up_bps"`     // 上行 byte/s
	DownBps   float64   `json:"down_bps"`   // 下行 byte/s
	UpText    string    `json:"up_text"`    // 已格式化，如 "12.3 KB/s"
	DownText  string    `json:"down_text"`  // 已格式化
	Sent      uint64    `json:"sent"`       // 累计上行字节
	Recv      uint64    `json:"recv"`       // 累计下行字节
	SampledAt time.Time `json:"sampled_at"` // 采样时间
	Interval  float64   `json:"interval_s"` // 本次速率统计的时间窗（秒）
	// Estimated = true 表示累计量是**本地积分估算**（模组只报瞬时速率），
	// 前端会在"累计上行/下行"后面加"（估算）"。
	Estimated bool `json:"estimated,omitempty"`
}

// 模组上报的累计字节数：+QGDCNT: <sent>,<received>
var reQGDCNT = regexp.MustCompile(`\+QGDCNT:\s*(\d+)\s*,\s*(\d+)`)

// 模组上报的瞬时速率（广和通）：+GTSTATIS: <下载 B/s>,<上传 B/s>[,<..>,<..>]
var reGTSTATIS = regexp.MustCompile(`\+GTSTATIS:\s*(\d+)\s*,\s*(\d+)`)

// 鼎桥 MT5700（TD Tech）：`AT^DSFLOWQRY` 回 6 个**十六进制**字段（手册 §16.12）
//
//	^DSFLOWQRY: <最后一次DS连接时长>,<最后一次发送字节>,<最后一次接收字节>,
//	            <累计DS连接时长>,<累计发送字节>,<累计接收字节>
//
// 实测：`^DSFLOWQRY: 000002A1,00000000000019A5,0000000000001666,000002A1,00000000000019A5,0000000000001666`
// → 累计发送 0x19A5 = 6565 B、累计接收 0x1666 = 5734 B。
// 与移远 AT+QGDCNT? 同属"累计计数器" → 走同一条差分路径（速度和配额都按它算），
// 区别只是进制（十六进制）和字段位置（累计量在后半段）。
var reDSFLOWQRY = regexp.MustCompile(`(?i)\^DSFLOWQRY:\s*([0-9A-Fa-f]+)\s*,\s*([0-9A-Fa-f]+)\s*,\s*([0-9A-Fa-f]+)\s*,\s*([0-9A-Fa-f]+)\s*,\s*([0-9A-Fa-f]+)\s*,\s*([0-9A-Fa-f]+)`)

// ParseRates 解析广和通 AT+GTSTATIS? 的**瞬时速率**（byte/s）。
//
// 字段口径取自广和通官方集成脚本（luci-app-modem → fibocom.sh，#速率统计）：
//
//	#当前上传速率（单位，Byte/s）: tx_rate=$2
//	#当前下载速率（单位，Byte/s）: rx_rate=$1
//
// 即 **$1 = 下载、$2 = 上传**；后两格官方脚本没用，含义未公开。
// ⚠️ 本机（Windows 主机不走模组数据面）数据面一直空闲，只测到 0,0 —— 非零值的
// 映射依官方脚本，未能在实机上看到非零样本。
func ParseRates(resp []string) (up, down float64, ok bool) {
	for _, line := range resp {
		m := reGTSTATIS.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		d, err1 := strconv.ParseFloat(m[1], 64)
		u, err2 := strconv.ParseFloat(m[2], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		return u, d, true // 注意顺序：函数返回 (上行, 下行)
	}
	return 0, 0, false
}

// 美格 SRM（MeiG）：`AT^DSFLOWQRY` 回 `^DSFLOWRPT: <上行 B/s>,<下行 B/s>,…`
//
// 证据（QModem `vendor/meig.sh` 的 network_info()）：
//
//	response=$(cmd_dsflowqry "$at_port" | grep "\^DSFLOWRPT:" …)
//	tx_rate=$(echo $response | awk -F',' '{print (NF>=1)?$1:"0"}')
//	rx_rate=$(echo $response | awk -F',' '{print (NF>=2)?$2:"0"}')
//	add_speed_entry rx $rx_rate ; add_speed_entry tx $tx_rate
//
// 即 **f1 = 上行、f2 = 下行**，而且与移远 `+QNWCFG: "up/down"` / 广和通 `+GTSTATIS`
// （两者注释里都写明是 Byte/s）喂给同一个 `add_speed_entry` ⇒ 按 Byte/s 的**瞬时速率**处理
// （速率式路径：本地积分出累计量，界面标「（估算）」）。
// ⚠️ 脚本注释没有直接写单位，这是"与另两家同一路径"的推断；
// 与鼎桥 MT5700 的 `^DSFLOWQRY:`（6 个十六进制**累计**字节）互不影响：这里只认 `^DSFLOWRPT:`。
var reDSFLOWRPT = regexp.MustCompile(`(?i)\^DSFLOWRPT:\s*(\d+)\s*,\s*(\d+)`)

// ParseDSFlowRpt 解析美格的瞬时速率（byte/s），返回 (上行, 下行)。
func ParseDSFlowRpt(resp []string) (up, down float64, ok bool) {
	for _, line := range resp {
		m := reDSFLOWRPT.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		u, err1 := strconv.ParseFloat(m[1], 64)
		d, err2 := strconv.ParseFloat(m[2], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		return u, d, true
	}
	return 0, 0, false
}

// ParseCounters 解析累计收发字节数（移远 AT+QGDCNT?，十进制）
func ParseCounters(resp []string) (sent, recv uint64, ok bool) {
	for _, line := range resp {
		m := reQGDCNT.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		s, err1 := strconv.ParseUint(m[1], 10, 64)
		r, err2 := strconv.ParseUint(m[2], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		return s, r, true
	}
	return 0, 0, false
}

// ParseHexCounters 取累计发送/接收字节（鼎桥 AT^DSFLOWQRY，十六进制，取第 5/6 格）
func ParseHexCounters(resp []string) (sent, recv uint64, ok bool) {
	for _, line := range resp {
		m := reDSFLOWQRY.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		s, err1 := strconv.ParseUint(m[5], 16, 64)
		r, err2 := strconv.ParseUint(m[6], 16, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		return s, r, true
	}
	return 0, 0, false
}

// ParseTraffic 按**应答形状**分流，把一次查询结果变成采样点。
//
// 顺序有讲究：两种速率式（广和通 GTSTATIS、美格 DSFLOWRPT）先试，
// 因为它们和计数式不会同时出现；计数式再依次试移远（十进制）与鼎桥（十六进制）。都不认就返回 false。
func ParseTraffic(resp []string) (TrafficSample, bool) {
	if up, down, ok := ParseRates(resp); ok {
		return TrafficSample{RateMode: true, UpBps: up, DownBps: down}, true
	}
	if up, down, ok := ParseDSFlowRpt(resp); ok {
		return TrafficSample{RateMode: true, UpBps: up, DownBps: down}, true
	}
	sent, recv, ok := ParseCounters(resp)
	if !ok {
		sent, recv, ok = ParseHexCounters(resp)
	}
	if !ok {
		return TrafficSample{}, false
	}
	return TrafficSample{Sent: sent, Recv: recv}, true
}

// FormatRate 把 byte/s 格式化成便于阅读的速度文本
func FormatRate(bps float64) string {
	switch {
	case bps <= 0:
		return "0 B/s"
	case bps < 1024:
		return fmt.Sprintf("%.0f B/s", bps)
	case bps < 1024*1024:
		return fmt.Sprintf("%.1f KB/s", bps/1024)
	default:
		return fmt.Sprintf("%.2f MB/s", bps/(1024*1024))
	}
}

// ---- from: pdp/pdp.go ----

// Package pdp 解析 PDP 上下文的地址（AT+CGPADDR）。
//
// 这里是**纯解析**：不碰串口、不持有模组状态（只认字符串），所以能单独测试。
// 真正发指令的那一层是 `(*Modem).QueryPDPAddrs`
// （见 glue.go），它把应答交给这一段的解析函数。
//
// 实测 RM502Q-GL（IPV4V6 双栈，电信卡）：
//
//	AT+CGPADDR
//	+CGPADDR: 1,"10.149.102.75,36.14.4.0.6.24.12.75.24.217.129.210.10.3.26.30"
//	+CGPADDR: 2,"36.14.5.120.6.32.0.214.24.217.129.211.40.108.195.174"
//
// 即同一个引号字段里用逗号分隔多个地址，其中 **IPv6 被拆成 16 个十进制字节**：
// 36.14.4.0.6.24.12.75.24.217.129.210.10.3.26.30
//   → 0x24 0x0e 0x04 0x00 0x06 0x18 0x0c 0x4b ...
//   → 240e:400:618:c4b:18d9:81d2:a03:1a1e
// 所以必须自己按 16 字节拼回 8 组十六进制，不能当成点分十进制地址。
// DefaultDataCID 主数据上下文（多数模组的第一个 PDP 上下文）

const DefaultDataCID = 1

// ⚠️ 必须抓**整行剩余部分**：RM500U 的应答是两个引号字段
//
//	+CGPADDR: 1,"10.161.107.67","240e:0400:5828:…"
//
// 只抓第一个引号字段的话，IPv6 直接丢掉（这正是“IPv6 显示不出来”的原因）。
var reCGPADDR = regexp.MustCompile(`\+CGPADDR:\s*(\d+)\s*,\s*(.*)$`)

// SplitAddrs 解析 +CGPADDR 的地址字段，分出 IPv4 与 IPv6。
//
// 各固件写法不一样，三种都要认（实测）：
//
//	RM502Q-GL：`"10.149.102.75,36.14.4.0.6.24.…"`（IPv6 是 16 个十进制字节，逗号分隔）
//	RM500U-CNV：`"10.161.107.67","240e:0400:5828:1856:18d9:c182:afc8:b63a"`
//	            —— 注意是**两个引号字段**（IPv6 是正常写法）；本函数拿到的是拼接后的字符串
//	            或单字段 `"240e:0400:…"`（IPv6 only 的上下文，如 IMS）
//	FM350-GL：`"10.2.98.236","0.0.0.0.0.0.0.0.24.217.217.24.12.29.50.199"`
//	            —— **只报接口标识**（前 8 字节全 0），见 HasZeroPrefix
func SplitAddrs(field string) (ipv4, ipv6 string) {
	for _, part := range strings.Split(field, ",") {
		part = strings.Trim(strings.TrimSpace(part), `"`)
		if part == "" {
			continue
		}
		// 标准写法（含冒号）直接认：比 16 字节十进制那种怪写法更常见
		if strings.Contains(part, ":") {
			if ipv6 == "" && LooksLikeIPv6(part) && !HasZeroPrefix(net.ParseIP(part).To16()) {
				ipv6 = strings.ToLower(part)
			}
			continue
		}
		segs := strings.Split(part, ".")
		if len(segs) == 4 && LooksLikeIP(part) && ipv4 == "" {
			ipv4 = part
			continue
		}
		// 16 个十进制字节 = IPv6（移远某些固件的怪写法）
		if len(segs) == 16 && ipv6 == "" {
			if b, ok := bytesFromDecimal(segs); ok && !HasZeroPrefix(b) {
				ipv6 = formatIPv6(b)
			}
		}
	}
	return ipv4, ipv6
}

// HasZeroPrefix 判断 IPv6 是不是"没有前缀"（前 64 位全 0）。
//
// 实测 FM350-GL（固件 81600.0000.00.29.22.40）的 `AT+CGPADDR=<cid>` 只报**接口标识**：
//
//	+CGPADDR: 2,"10.2.98.236","0.0.0.0.0.0.0.0.24.217.217.24.12.29.50.199"
//	                                     └── 前 8 字节全 0 = 没有前缀 ──┘
//
// 全局单播地址的前缀不可能是全 0，按字面显示就是 `::18d9:d918:c1d:32c7` —— 看着就像"IPv6 地址不对"。
// 同一固件报的 IPv6 DNS 是完整的 `240e:5a::6666`（`+GTDNS`/`+CGCONTRDP` 都是 36.14.0.90.0.…），
// 说明不是"格式没打开"：`AT+CGPIAF` 的 4 个开关（0,0,0,0 / 1,0,1,1 / 1,1,1,1 都试过）对返回值没有任何影响，
// `AT+CGCONTRDP` 的 `<local_addr>`（IPv4/IPv6 两行）也都是空的 —— 这个 PDP 真的没下发前缀。
//
// 所以按项目惯例**不显示不可信的地址**（界面「链路」的 IPv6 显示 `-`），
// 原始值仍可在「AT 指令」页自己发 `AT+CGPADDR=2` 看到。
func HasZeroPrefix(b []byte) bool {
	if len(b) != 16 {
		return false
	}
	for _, x := range b[:8] {
		if x != 0 {
			return false
		}
	}
	return true
}

// LooksLikeIPv6 粗校验：能按 net.ParseIP 解析出 IPv6 且不是全零地址
func LooksLikeIPv6(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() != nil {
		return false
	}
	return !ip.IsUnspecified()
}

// LooksLikeIP 判断是否为可用的点分十进制 IPv4 地址。
// 必须是 4 段（模组的掩码/DNS 字段会用 6 段编码，不能当成 IP），且不能是全零（未分配）。
// 原先在 data.go 段里，PDP 解析与流量解析都要用，所以留在这里当公共校验；
// kernel 里保留了旧名字 `looksLikeIP` 的别名，调用点不用改。
func LooksLikeIP(s string) bool {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	allZero := true
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 255 {
			return false
		}
		if n != 0 {
			allZero = false
		}
	}
	return !allZero
}

// bytesFromDecimal 把 16 个十进制字节（每个 0~255）转成字节数组
func bytesFromDecimal(segs []string) ([]byte, bool) {
	if len(segs) != 16 {
		return nil, false
	}
	out := make([]byte, 0, 16)
	for _, s := range segs {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 0 || n > 255 {
			return nil, false
		}
		out = append(out, byte(n))
	}
	return out, true
}

// formatIPv6 把 16 字节格式化成 IPv6 文本（按 RFC 5952：小写、去前导零、
// 最长的一段连续零组用 :: 压缩）。
func formatIPv6(b []byte) string {
	if len(b) != 16 {
		return ""
	}
	groups := make([]string, 8)
	for i := 0; i < 8; i++ {
		groups[i] = strconv.FormatUint(uint64(uint16(b[i*2])<<8|uint16(b[i*2+1])), 16)
	}
	// 找最长连续 "0" 段（至少 2 组才压缩）
	bestStart, bestLen := -1, 0
	for i := 0; i < 8; {
		if groups[i] != "0" {
			i++
			continue
		}
		j := i
		for j < 8 && groups[j] == "0" {
			j++
		}
		if j-i >= 2 && j-i > bestLen {
			bestStart, bestLen = i, j-i
		}
		i = j
	}
	if bestStart < 0 {
		return strings.Join(groups, ":")
	}
	head := strings.Join(groups[:bestStart], ":")
	tail := strings.Join(groups[bestStart+bestLen:], ":")
	return head + "::" + tail
}

// ParseCGPADDR 从 AT+CGPADDR 应答里取主数据上下文的 IPv4/IPv6。
// 优先 preferCID（通常 cid=1），拿不到再取第一个非空的上下文。
func ParseCGPADDR(resp []string, preferCID int) (ipv4, ipv6 string) {
	first := struct{ v4, v6 string }{}
	for _, line := range resp {
		m := reCGPADDR.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		cid, _ := strconv.Atoi(m[1])
		v4, v6 := SplitAddrs(strings.TrimSuffix(m[2], "\r"))
		if v4 == "" && v6 == "" {
			continue
		}
		if cid == preferCID {
			return v4, v6
		}
		if first.v4 == "" && first.v6 == "" {
			first.v4, first.v6 = v4, v6
		}
	}
	return first.v4, first.v6
}

// ---- from: apn/apn.go ----

// Package apn 是 APN 相关的纯解析：从 AT+CGCONTRDP / AT+CGDCONT? 的应答里
// 取出「上网用的 APN」与本机 IPv4，并判断哪些 APN 是内部专用（IMS / 紧急呼叫）。
//
// 读取流程（发指令、错误处理）留在 kernel（见 glue.go），
// 因为那一步要和串口、厂商指令解析打交道。
// ReCGDCONT 匹配 `+CGDCONT: <cid>,"<pdp_type>","<apn>"` 三格（后面的网关/地址不关心）。
// 仅表示配置了哪些 APN；当前**激活**的上下文要看 AT+CGCONTRDP。

var ReCGDCONT = regexp.MustCompile(`\+CGDCONT:\s*(\d+)\s*,\s*"([^"]*)"\s*,\s*"([^"]*)"`)

// cgcontrdpLocalAddr 从 `AT+CGCONTRDP` 的 `<local_addr and subnet_mask>` 字段里取出 IPv4 地址。
//
// 3GPP 27.007 规定这个字段是"地址 + 掩码"**拼在一起**的，不是单独一个 IP：
//
//	IPv4 → `a.b.c.d.e.f.g.h`（前 4 段是地址、后 4 段是掩码）
//	IPv6 → 16 字节的点分形式
//
// 实测展锐 UMS9620（中兴 F50）用的就是标准 IPv4 写法：
//
//	+CGCONTRDP: 1,6,ctnet,10.194.190.206.255.0.0.0,,218.2.2.2,218.4.4.4,…
//	                      └─ 10.194.190.206 + 255.0.0.0
//
// 与**同一台机器** `AT+CGPADDR` 报的 cid 1 地址完全一致（交叉验证）。
// 以前这里只认"整段就是一个合法 IP"（那是 FM350 那种把掩码放到别的字段的写法），
// 于是标准写法会被整段判成非法 ⇒ 界面「链路」里的本机 IP 一直是 `-`。
func cgcontrdpLocalAddr(field string) string {
	field = strings.TrimSpace(field)
	if field == "" {
		return ""
	}
	if LooksLikeIP(field) {
		return field
	}
	// IPv4 的 `<地址>.<掩码>`：恰好 8 段、每段都是 0~255 的十进制整数
	parts := strings.Split(field, ".")
	if len(parts) != 8 {
		return ""
	}
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < 0 || n > 255 {
			return ""
		}
	}
	ip := strings.Join(parts[:4], ".")
	if ip == "0.0.0.0" {
		return "" // 未分配（IMS 上下文常见）——宁可不显示，也不显示一个假的地址
	}
	return ip
}

// ParseResp 从 AT+CGCONTRDP / AT+CGDCONT? 应答里取 APN 与本机 IP。
// cmd 决定用哪种格式解析；取不到返回空串（不算错误，模组可能只是没激活 PDP）。
func ParseResp(cmd string, resp []string) (name, ip string) {
	if strings.Contains(strings.ToUpper(cmd), "CGCONTRDP") {
		for _, line := range resp {
			i := strings.Index(strings.ToUpper(line), "+CGCONTRDP:")
			if i < 0 {
				continue
			}
			// ⚠️ 不能要求 <bearer_id> 是数字：实测 FM350-GL 把它留空
			//（`+CGCONTRDP: 2,,"ctnet","","","218.2.2.2","218.4.4.4",…`），
			// 按固定正则匹配会整行认不出来 → 界面「链路」里的 APN 一直是 `-`。
			f := common.SplitATFields(line[i+len("+CGCONTRDP:"):])
			if len(f) < 4 {
				continue
			}
			a := f[2]
			if a == "" || atparse.IsInternal(a) {
				continue
			}
			addr := cgcontrdpLocalAddr(f[3])
			return a, addr
		}
		return "", ""
	}
	// AT+CGDCONT?：优先 cid 最小的数据 APN
	best, bestCid := "", 0
	fallback := ""
	for _, line := range resp {
		m := ReCGDCONT.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		cid, _ := strconv.Atoi(m[1])
		a := strings.TrimSpace(m[3])
		if a == "" {
			continue
		}
		if fallback == "" {
			fallback = a
		}
		if atparse.IsInternal(a) {
			continue
		}
		if best == "" || cid < bestCid {
			best, bestCid = a, cid
		}
	}
	if best != "" {
		return best, ""
	}
	return fallback, ""
}

// ---- from: ambr/ambr.go ----

// Package ambr 是「签约速率（AMBR）」的纯解析。
//
// 「签约速率」指的是网络在 PDU 会话建立时下发的 AMBR（Aggregate Maximum Bit AMBRRate，
// 聚合最大比特率）—— 也就是运营商套餐给这条链路的上/下行速率上限，不是**实测**速度。
// 它跟 data.go 用流量计数器差分算出来的实时速率是两回事，所以界面上分开显示。
//
// 3GPP 的 AT+CGCONTRDP / AT+CGEQNEG 在移远固件上都不带速率（实测 +CGEQNEG 直接 ERROR），
// 移远自家的查询指令是：
//
//	AT+QNWCFG="nr5g_ambr"
//	+QNWCFG: "nr5g_ambr","IMS",6,300,6,75
//	                                                     ↑  CQI DL / AMBR DL / CQI UL / AMBR UL
//
// 即 [APN, CQI 下行, 签约下行(Mbps), CQI 上行, 签约上行(Mbps)]。
// 本机实测（RM502Q-GL / 中国电信 5G SA）只有 IMS 上下文会上报，IMS 是 VoNR 的会话，
// 数字本身是运营商给这张卡的签约档位，所以照原样展示，并在界面上标注 APN 与来源指令。
//
// 第二条路是 **AT+CGCONTRDP 的"尾部 AMBR 扩展"**（见 ParseCGCONTRDP）：
// 3GPP TS 27.007 的 +CGCONTRDP 本身没有 AMBR 字段，但部分固件会在行尾追加
// `<下行 kbps>,<上行 kbps>`（用户提供的 SIMCom 系文档样例：
// `+CGCONTRDP: 1,5,"cmiot","",1,0,0,0,0,0,0,5000000,1000000` = 下行 5000 / 上行 1000 Mbps）。
// ⚠️ 本机 FM160-EAU（固件 89611.1000.00.04.01.07）实测**没有**这两个字段
// （行尾是引号包着的 P-CSCF 地址），所以这条路只在"确实带尾巴"的固件上生效。
//
// 第三条路是鼎桥 MT5700 的 `AT^DSAMBR=<cid>`（见 ParseDSAMBR），**单位 kbps**。
//
// 顺带上报的两个小字段（都随签约速率一起来，界面在 AMBR 那组一并显示，取不到就不显示）：
//   - **CQI**（信道质量 0~15）：移远 `AT+QNWCFG="nr5g_ambr"` 的第 3/5 格（口径同 QModem 的 vendor_quectel.sh）；
//   - **QCI / 5QI**（承载的 QoS 等级）：有方 `AT+C5GQOSRDP` 的第 2 格（3GPP 5GS QoS 布局 `<cid>,<5QI>,…`），
//     以及鼎桥 `AT^DSAMBR` 的末位（`<cid>,<DL>,<UL>,"<apn>",<QCI>`，实机 `"ctnet",6` / `"ims",5`）。
//     移远 QNWCFG、美格 DSAMBR 都不带这个字段 —— 没有的就不显示，不拿未定义的下标去猜。
//
// 发指令 + TTL 缓存留在 kernel（见 glue.go），因为那部分的缓存挂在 Modem 上。
// TTL 签约速率缓存时长：超过这个时间才会重新问一次模组。
// 该指令在 3 秒一轮的网络推送里会被反复调用，但签约速率只在 PDU 会话重建时才会变。

const TTL = 60 * time.Second

// +CGCONTRDP 行头：`+CGCONTRDP: <cid>,<bearer>,<apn>,"<本地地址/掩码>",…`
var reCGCONTRDPHead = regexp.MustCompile(`(?i)^\s*\+CGCONTRDP:\s*(\d+)\s*,\s*[\d-]+\s*,\s*"([^"]*)"\s*(.*)$`)

// 尾部两个 kbps 数字的合理区间（下限 1 Mbps）：
// 标准 +CGCONTRDP 尾巴上的可选字段都是**小整数**（IM_CN 标志 0/1、NSLPI 0~3、WLAN_Offload 0~2 …）
// 或者是引号包着的地址，所以"两个 ≥ 1 Mbps 的裸数字"才是厂商扩展的 AMBR；
// 只有单个大数字（例如 IPv4_MTU=1500）会因为另一个不够大而被丢掉。
const (
	tailMinKbps = 1000
	tailMaxKbps = 100_000_000
)

// tailKbps 把候选字段解析成 ≤ 10 位裸数字（带引号/带小数的都不认）
func tailKbps(tok string) (int, bool) {
	s := strings.TrimSpace(strings.TrimSuffix(tok, "\r"))
	if s == "" || strings.ContainsAny(s, `".`) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < tailMinKbps || n > tailMaxKbps {
		return 0, false
	}
	return n, true
}

// ParseCGCONTRDP 从标准 AT+CGCONTRDP 的应答里读"尾部 AMBR 扩展"。
//
// 字段顺序按用户提供的文档：**末尾两个 = <下行 kbps>,<上行 kbps>**，例如
//
//	+CGCONTRDP: 1,5,"cmiot","",1,0,0,0,0,0,0,5000000,1000000   → 下行 5000 / 上行 1000 Mbps
//
// ⚠️ 本机 FM160-EAU 实测**没有**这两个字段（行尾是引号包着的 P-CSCF 地址），
// 所以该路径在无扩展的固件上不会生效，也不会把引号里的地址误当成速率；
// 上下行的先后只依文档，未能实机对照（换上行下会打一条 WARN 日志）。
// 优先取**上网用**的数据 APN（跳过 IMS/sos）。
func ParseCGCONTRDP(resp []string) (*atparse.AMBRRate, bool) {
	return ParseCGCONTRDPContext(resp, 0, "")
}

// ParseCGCONTRDPContext 同上，但**只认拨号的那条承载**（cid 优先、其次 APN，见 pickAMBR）。
// 同一台模组常同时报 IMS 与数据两条（甚至同一 cid 的 IPv4/IPv6 两行），取错就会把 IMS 的档位显示出来。
func ParseCGCONTRDPContext(resp []string, cid int, apn string) (*atparse.AMBRRate, bool) {
	var cands []*atparse.AMBRRate
	for _, line := range resp {
		m := reCGCONTRDPHead.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		lineCID, _ := strconv.Atoi(m[1])
		name := strings.TrimSpace(m[2])
		toks := strings.Split(m[3], ",")
		if len(toks) < 2 {
			continue
		}
		dl, ok1 := tailKbps(toks[len(toks)-2])
		ul, ok2 := tailKbps(toks[len(toks)-1])
		if !ok1 || !ok2 {
			continue
		}
		cands = append(cands, &atparse.AMBRRate{APN: name, CID: lineCID, DLMbps: dl / 1000, ULMbps: ul / 1000})
	}
	return atparse.PickAMBR(cands, cid, apn)
}

// ---- from: params/params.go ----

// Package params 是「可写参数（APN / 选网）」的纯逻辑：校验、AT 指令拼装、应答解析。
//
// 与只读查询分开放的原因：写参数必须先**校验再下发**，下发后还要**回读确认** ——
// AT 通道没有事务语义，只有"写完再问一次"才能证明真的生效了（模组经常回 OK
// 但把非法值悄悄忽略掉）。
//
// 只做 3GPP 标准指令（AT+CGDCONT / AT+CGACT / AT+COPS），各家固件都支持；
// 厂商私有参数（如漫游偏好 roam_pref）语义无法从实测确认，不在这里暴露，
// 需要时走「接口调试」直接发 AT。
//
// 发指令 + 回读确认的流程在 kernel（见 data.go），因为要碰串口与配额状态。
// Context 一个 PDP 上下文：配置（AT+CGDCONT?）+ 激活状态（AT+CGACT?）

type Context struct {
	CID      int    `json:"cid"`
	PDPType  string `json:"pdp_type"` // IP / IPV6 / IPV4V6
	APN      string `json:"apn"`
	Active   bool   `json:"active"`   // 该上下文当前是否已激活（有数据链路）
	Internal bool   `json:"internal"` // IMS / sos 等内部专用 APN，不用于上网
}

// NetSelect 选网方式（AT+COPS）
type NetSelect struct {
	Mode     int    `json:"mode"`               // 0 自动 / 1 手动 / 2 注销 / 4 手动转自动 / 5 自动转手动
	ModeText string `json:"mode_text"`          // 中文说明
	PLMN     string `json:"plmn,omitempty"`     // 仅当模组回的是数字格式（format=2）
	Operator string `json:"operator,omitempty"` // 名称格式（format=0）；实测自动模式下常是 "????"
	AcT      int    `json:"act,omitempty"`      // 接入技术编号（27.007）
	Raw      string `json:"raw,omitempty"`
}

// Status 参数总览（GET /api/params）
type Status struct {
	ModemID   int       `json:"modem_id"`
	QueriedAt time.Time `json:"queried_at"`
	APN       string    `json:"apn,omitempty"` // 当前**在用**的 APN（激活的上下文）
	Contexts  []Context `json:"contexts"`
	NetSelect NetSelect `json:"net_select"`
}

var (
	// +CGDCONT: 1,"IPV4V6","3GNET","0.0.0.0",…   （正则复用 data.go 的那份）
	// +CGACT: 1,1                                 cid, 是否激活
	reCGACT = regexp.MustCompile(`\+CGACT:\s*(\d+)\s*,\s*(\d+)`)
	// +COPS: 0,0,"????",11 / +COPS: 1,2,"46011",7 / +COPS: 0
	//（cell.go 里的 reCOPS 只取运营商名，这里要模式/PLMN/AcT，所以另起一个名字）
	reCOPSMode = regexp.MustCompile(`\+COPS:\s*(\d+)(?:\s*,\s*(\d+)\s*,\s*"([^"]*)")?(?:\s*,\s*(\d+))?`)
)
