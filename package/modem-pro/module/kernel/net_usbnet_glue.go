package kernel

import (
	"fmt"
	"modemd/module/atparse"
	"modemd/module/common"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ====================== USB 网卡模式（引擎侧的胶水） ======================
//
// 取值表与解析在 net.go（Mode / Status / Modes / ParseUSBnet）；
// 这里负责"向厂商要指令 + 下发 + 回读"，以及让配置生效的复位。

// usbnetConfigurator 取当前厂商的 USB 网卡模式指令；未实现该能力的厂商返回 nil
//
// ⚠️ 新路径（多数厂商）走 `vendor_settings_usb.go` 的方言表：同一件事各家指令与取值都不同。
// 本函数只保留给"声明了 USBnetConfigurator 但没有方言表"的厂商（兼容旧路径）。
func (mod *Modem) usbnetConfigurator() (USBnetConfigurator, error) {
	// PCIe（MHI / wwan）模组**没有**「USB 网卡模式」这个概念：那是 USB 侧的协议切换
	// （AT+QCFG="usbnet" / AT+GTUSBMODE…）。PCIe 上数据面协议由 MHI / MBIM 决定，
	// 硬发这条 AT 只会拿到 ERROR。这里如实说明"为什么没有"，而不是报一条看不懂的查询失败。
	if p := PCIeInfoFor(mod); p != nil {
		return nil, fmt.Errorf("这台模组是 PCIe（%s）接法，没有「USB 网卡模式」：数据面协议由 MHI/MBIM 决定"+
			"（QMI → 主机侧厂商拨号器；MBIM → umbim）——具体形态见「拨号上网 → 模块总线类型」卡片", p.Label())
	}
	if d := usbDialectFor(mod); d != nil {
		return dialectUSBnet{mod: mod}, nil
	}
	// 型号在清单里（有 usb_at）但没有厂商方言：仍然让引擎走（查询/写入会自己报
	// "参数未取证，只能读"），而不是报一句含糊的 "vendor unknown does not implement…"。
	if _, ok := catalogUSBATFor(mod); ok {
		return dialectUSBnet{mod: mod}, nil
	}
	c, ok := mod.cmdImpl.(USBnetConfigurator)
	if !ok {
		return nil, fmt.Errorf("vendor %s does not implement USB net mode (AT+QCFG=\"usbnet\")", mod.Vendor)
	}
	return c, nil
}

// dialectUSBnet 把方言表包成 USBnetConfigurator，让旧的调用点（含测试）无感切换
type dialectUSBnet struct{ mod *Modem }

func (d dialectUSBnet) CommandQueryUSBnet() string {
	cmd, err := d.mod.usbnetQueryCommand()
	if err != nil {
		return ""
	}
	return cmd
}

func (d dialectUSBnet) CommandSetUSBnet(value string) string {
	cmd, err := d.mod.usbnetSetCommand(value)
	if err != nil {
		return ""
	}
	return cmd
}

// QueryUSBnet 查询当前 USB 网卡模式
//
// 指令来源见 `usbnetQueryCommand`（清单 usb_at.query 优先，其次厂商方言表）；
// 空指令（厂商声明"本机没这个概念"、或清单里没有查询指令）在这里就会被挡下，
// **发空指令出去**要等超时白花好几秒，以前就是从这个缝里漏出去的。
func (mod *Modem) QueryUSBnet() (*CFUNStatus, error) {
	if _, err := mod.usbnetConfigurator(); err != nil {
		return nil, err
	}
	cmd, err := mod.usbnetQueryCommand()
	if err != nil {
		return nil, err
	}
	mod.usbnetPrepare() // 进门指令（如 Sierra 的 AT!ENTERCND）——best-effort
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return nil, err
	}
	if common.RespHasError(resp) {
		return nil, fmt.Errorf("USB net mode query rejected: %s", FirstLine(resp))
	}
	value, ok := usbnetParse(mod, resp)
	if !ok {
		return nil, fmt.Errorf("USB net mode not reported: %s", FirstLine(resp))
	}
	st := mod.usbnetStatus(value, cmd)
	mod.mu.Lock()
	mod.usbNetStatus = st
	mod.usbNetStatusAt = time.Now()
	mod.mu.Unlock()
	return st, nil
}

func (mod *Modem) usbnetStatus(value, query string) *CFUNStatus {
	autoReboot := mod.isMT5700MCN()
	return &CFUNStatus{
		Value:        value,
		Name:         usbnetModeName(mod, value),
		Modes:        usbnetModes(mod),
		NeedReboot:   !autoReboot,
		AutoReboot:   autoReboot,
		BlockedModes: usbnetBlockedModes(mod),
		Cmd:          query,
		ModeCmds:     usbnetModeCmds(mod),
		Note:         usbnetNote(mod),
	}
}

const usbNetStatusCacheTTL = 5 * time.Minute

func (mod *Modem) usbNetStatusOrQuery(force bool) (*CFUNStatus, error) {
	mod.usbNetQueryMu.Lock()
	defer mod.usbNetQueryMu.Unlock()
	if !force {
		mod.mu.Lock()
		st, at := mod.usbNetStatus, mod.usbNetStatusAt
		if st != nil {
			cached := *st
			cached.Modes = append([]Mode(nil), st.Modes...)
			st = &cached
		}
		mod.mu.Unlock()
		if st != nil && time.Since(at) <= usbNetStatusCacheTTL {
			return st, nil
		}
	}
	return mod.QueryUSBnet()
}

// SetUSBnet 设置 USB 网卡模式。MT5700M-CN 接受设置后由模组自行重启。
func (mod *Modem) SetUSBnet(value string) (*CFUNStatus, error) {
	if _, err := mod.usbnetConfigurator(); err != nil {
		return nil, err
	}
	// 本型号只有 PCIe 形态（QModem 支持列表的 USB 列就是 ✘）→ 直接拒，别发指令
	if usbnetNoUSBForm(mod) {
		return nil, fmt.Errorf("本型号在 QModem 支持列表里只有 PCIe 形态（USB ✘），没有可设置的 USB 网卡模式；" +
			"PCIe 侧请看「PCIe 模组」页")
	}
	// 清单登记了 usb_at、但**一个切换指令都没有**（参数未取证，如 RG801 / SIMCom 展锐档 /
	// Neoway N510M）→ 不猜参数，直接拒并说清楚；界面侧取值表也是空的（点不到“应用”）。
	if e, ok := catalogUSBATFor(mod); ok && !e.USBModeAT.Known() {
		return nil, fmt.Errorf("本型号在模块清单里没有可下发的 USB 切换指令（%s）：只能读取当前值", usbnetCatalogWhy(e))
	}
	// 取值合法性：
	//   · 本机有取值表（清单口径，或厂商方言表过一遍支持清单）→ **必须在表内**
	//     （以前"是数字就放行"，填个 99 也会被下发，模组只能回 ERROR）；
	//   · 取值表为空且型号不在清单里 → 只接受纯数字/十六进制**原码**（就是设备回报的那个值）。
	modes := usbnetModes(mod)
	if len(modes) > 0 {
		if modeNameIn(modes, value) == "" {
			return nil, fmt.Errorf("本机不支持 USB 网卡模式 %q（本机可选：%s）",
				value, usbnetModeSummary(modes))
		}
	} else if !common.IsDecimal(value) && !common.IsHexValue(value) {
		return nil, fmt.Errorf("本机只能填模组回报的取值（数字/十六进制）；%q 不是有效取值", value)
	}
	// 只展示、不允许执行的档位（广和通 RNDIS：切过去就没 AT 口了，见 usbnetBlockedModes）。
	// 拦在这里而不是靠界面置灰：接口是公开的，curl 也能打进来。
	if why := usbnetBlockedReason(mod, value); why != "" {
		return nil, fmt.Errorf("USB 网卡模式 %q 只展示、不允许执行：%s", value, why)
	}
	cmd, err := mod.usbnetSetCommand(value)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cmd) == "" {
		return nil, fmt.Errorf("USB net mode %q 在本机方言下拼不出指令", value)
	}
	mod.usbnetPrepare() // 进门指令（如 Sierra 的 AT!ENTERCND）——best-effort
	mod.usbnetPrepareSet()
	autoReboot := mod.isMT5700MCN()
	if autoReboot {
		mod.ndisMu.Lock()
		defer mod.ndisMu.Unlock()
	}
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return nil, err
	}
	if common.RespHasError(resp) {
		return nil, fmt.Errorf("USB net mode set rejected: %s", FirstLine(resp))
	}
	if autoReboot {
		if !hasATOK(resp) {
			return nil, fmt.Errorf("USB 模式切换未获得模组确认")
		}
		// AT^SETMODE restarts the modem. Its old AT port can disappear as
		// soon as the command is acknowledged, so query again after enumeration.
		mod.mu.Lock()
		mod.usbNetStatus = nil
		mod.mu.Unlock()
		mod.dialMu.Lock()
		mod.dialType = nil
		mod.dialMu.Unlock()
		query, _ := mod.usbnetQueryCommand()
		return mod.usbnetStatus(value, query), nil
	}
	logger.Info("usbnet mode applied (reboot required)",
		zap.Int("modem", mod.ID), zap.String("value", value), zap.String("cmd", cmd))
	return mod.QueryUSBnet()
}

// usbnetModeSummary 本机 USB 网卡可选值的中文摘要（报错提示用）
func usbnetModeSummary(modes []Mode) string {
	var parts []string
	for _, m := range modes {
		parts = append(parts, m.Name+"（"+m.Value+"）")
	}
	if len(parts) == 0 {
		return "（本机未列出取值，填模组回报的数字）"
	}
	return strings.Join(parts, " / ")
}

// ResetModule 重启模组（AT+CFUN=1,1），用于让 usbnet 等配置生效。
// 模组可能来不及回 OK 就重启了，超时按"已下发"处理。
func (mod *Modem) ResetModule() error {
	resp, err := mod.SendAT("AT+CFUN=1,1")
	if err != nil {
		logger.Info("module reset sent (no ack, module likely rebooting)",
			zap.Int("modem", mod.ID))
		return nil
	}
	if common.RespHasError(resp) {
		return fmt.Errorf("module rejected reset: %s", FirstLine(resp))
	}
	logger.Info("module reset accepted", zap.Int("modem", mod.ID))
	// 复位后模组可能回到默认的 CPMS / CNMI：端口没重新枚举就不会再跑一遍 Start，
	// 这里等它回来补一次，否则新短信推送会静默失效。
	mod.reapplySMSConfigAfterReset()
	return nil
}

// ---- from: netinfo/extra.go ----

//
// 来源：芯讯通 SIM8200 的 `AT+CNWINFO?`（手册 §4.2.18 p.106）。
//
//	+CNWINFO: <SYS_MODE>,<EGCI>,<eNBID>,<MCS>,<DL_MOD>,<UL_MOD>,<CQI>,<TX_POWER>[,<BW>]
//
// 实测（SIM8200EA-M2 / 中国电信 5G SA）：
//
//	+CNWINFO: NR5G,4601155735066633,0xCFA11A0,0,UNKOWN,UNKOWN,0,0,100M
//
// 手册例：
//
//	+CNWINFO: LTE,4601195333890,0x5AEAE,1,QPSK,16QAM,15,9
//
// 几个要点：
//   - <EGCI> 是**十进制**，就是 PLMN+CellID 直接拼起来（4601155735066633 = 460|11|55735066633）；
//   - <eNBID> 是**十六进制**（0xCFA11A0），由 NCI 去掉低 8 位得到；
//   - 实测末尾还会多一个手册没写的带宽字段（100M）→ 按带宽处理，没有就不填；
//   - 调制字段固件会写 "UNKOWN"（原文如此，拼写错误）→ 归一成空（界面显示未上报）。
//
// 第二条路是鼎桥 MT5700 的 `AT^HFREQINFO?`（工作频率 / 频段 / 带宽，见 ParseHFREQINFO）。
//
// 做成可选能力（atcmd 的 ExtraNetInfoQuerier）：没声明该接口的厂商连这条指令都不发，
// 移远 / 广和通 / MT5700 之外的行为与以前完全一致。发指令的判定留在 kernel（见 glue.go）。

type Extra struct {
	SysMode string `json:"sys_mode,omitempty"` // NR5G / LTE
	EGCI    string `json:"egci,omitempty"`     // 十进制：PLMN+CellID
	ENBID   string `json:"enb_id,omitempty"`   // 十六进制原文，如 0xCFA11A0
	MCS     int    `json:"mcs"`                // 调制编码阶数 0~31；-1 = 未上报
	DLMod   string `json:"dl_mod,omitempty"`   // QPSK / 16QAM / 64QAM / 256QAM
	ULMod   string `json:"ul_mod,omitempty"`
	CQI     int    `json:"cqi"`                     // 信道质量 0~15；-1 = 未上报
	TxPower int    `json:"tx_power"`                // 上行发射功率 dBm；-99 = 未上报（手册范围 -99~30）
	BWmHz   int    `json:"bandwidth_mhz,omitempty"` // 带宽 MHz（手册未写、实测多一格 "100M"）
	// Band 驻留频段号（鼎桥 MT5700 的 AT^HFREQINFO? 提供；CNWINFO 不提供 → 0）
	// 「网络」卡片的「频段」在这一栏为空、而这里有时会用它补上（见 net.go）。
	Band int    `json:"band,omitempty"`
	Raw  string `json:"raw,omitempty"`
}

// reCNWINFO 匹配 +CNWINFO 行（冒号后整段，再按逗号切）
var reCNWINFO = regexp.MustCompile(`\+CNWINFO:\s*(.+)$`)

// reBW 末尾的带宽字段："100M" / "100MHZ" / "20M"
var reBW = regexp.MustCompile(`^(\d+)\s*M(?:HZ)?$`)

// NormalizeMod 调制方式归一：只认手册列的几种，其它（含固件拼错的 "UNKOWN"）当未上报。
func NormalizeMod(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "QPSK", "16QAM", "64QAM", "256QAM", "1024QAM":
		return strings.ToUpper(strings.TrimSpace(s))
	}
	return ""
}

// ParseExtraNetInfo 解析 AT+CNWINFO? 应答（认不出就返回 false，不瞎填）。
func ParseExtraNetInfo(resp []string) (*Extra, bool) {
	for _, line := range resp {
		m := reCNWINFO.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		f := common.SplitATFields(m[1])
		// 手册定义 8 个字段；实测固件会多补一个带宽，所以按"至少 8"来判
		if len(f) < 8 {
			continue
		}
		sysMode := strings.ToUpper(strings.TrimSpace(f[0]))
		if sysMode != "LTE" && sysMode != "NR5G" {
			continue
		}
		out := &Extra{
			SysMode: sysMode,
			EGCI:    strings.TrimSpace(f[1]),
			ENBID:   strings.ToUpper(strings.TrimSpace(f[2])),
			MCS:     -1,
			DLMod:   NormalizeMod(f[4]),
			ULMod:   NormalizeMod(f[5]),
			CQI:     -1,
			TxPower: -99,
			Raw:     strings.TrimSpace(line),
		}
		// EGCI 必须是一串数字（十进制 PLMN+CellID），否则说明字段顺序不对
		if !atparse.IsAllDigits(out.EGCI) {
			continue
		}
		// eNBID 是十六进制；带上 0x 前缀才认（免得把别的东西当 ID）。
		// 前缀统一成小写 "0x"，后面数字大写（0xCFA11A0），否则会得到 "0XCFA11A0" 这种歪写法
		enb := strings.TrimSpace(f[2])
		if len(enb) > 2 && strings.EqualFold(enb[:2], "0x") {
			out.ENBID = "0x" + strings.ToUpper(enb[2:])
		} else {
			out.ENBID = ""
		}
		if v, err := strconv.Atoi(strings.TrimSpace(f[3])); err == nil && v >= 0 && v <= 31 {
			out.MCS = v
		}
		if v, err := strconv.Atoi(strings.TrimSpace(f[6])); err == nil && v >= 0 && v <= 15 {
			out.CQI = v
		}
		if v, err := strconv.Atoi(strings.TrimSpace(f[7])); err == nil && v >= -99 && v <= 30 {
			out.TxPower = v
		}
		// 多出来的那格如果是 "100M" 这种，就是带宽
		if len(f) >= 9 {
			if mm := reBW.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(f[8]))); mm != nil {
				if v, err := strconv.Atoi(mm[1]); err == nil && v > 0 && v <= 400 {
					out.BWmHz = v
				}
			}
		}
		return out, true
	}
	return nil, false
}
