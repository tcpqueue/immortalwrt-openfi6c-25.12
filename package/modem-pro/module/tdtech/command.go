package tdtech

// ====================== 鼎桥 TD Tech MT5700 ======================
//
// MT5700 模组（ATI 含 "MT5700"）。它把网络/信号/温度/流量全放在 `^` 私有命令里，
// 移远/展锐那套**全部回 ERROR**（2026-09-30 实机核对）：
//
//	AT+QENG="servingcell" / AT+CPSI? / AT+QCSQ / AT+QNWINFO   → ERROR
//	AT+CESQ / AT+CGCONTRDP / AT+QTEMP / AT+QGDCNT? / AT+CBC   → ERROR
//	AT+CCID                                                    → ERROR（ICCID 要用 AT^ICCID?）
//	AT+CSQ                                                     → 回**空值**（`+CSQ:` 后面什么都没有）
//
// 所以这里按手册（MT5700M-CN 5G 系列模组 AT 命令手册）改成它自家的命令：
//
//	信号     AT^HCSQ?（§13.5，索引）/ AT^MONSC（§13.9，直接是 dB，优先）
//	服务小区 AT^MONSC         邻区 AT^MONNC（§13.10）
//	频段频点 AT^HFREQINFO?（§13.16，含 band / ARFCN / 带宽）
//	链路制式 AT^SYSINFOEX（§13.1）→ 实测 `^SYSINFOEX: 2,2,0,1,,11,"NR-5GC",111,"NR-5GC"`
//	温度     AT^CHIPTEMP?（§17.1，12 路传感器，0.1 ℃）
//	流量     AT^DSFLOWQRY（§16.12，**十六进制**的累计收发字节 → 计数器式，可算速度/配额）
//	ICCID    AT^ICCID?（§11.3）
//	时钟     AT+CCLK? 可用（但报的是 UTC 而有 +32 后缀 → 见 quirks 的 MT5700 条目）
//
// 锁网与载波聚合见本包下面几节；CA 的应答解析在 parse.go。
//
// 这个包**不依赖内核**：发指令只走 iface.ATDoer / iface.LockATDoer，
// ^MONSC / ^HCSQ 那两个通用解析器由内核注入（见 hooks.go），
// 这样"厂商实现"与"内核"之间没有循环依赖。

import (
	"fmt"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"modemd/iface"
	"modemd/module/common"
)

// ---- 载波聚合（CA，AT^CASCELLINFO? / AT^MONSSC）----

//
// 手册：《MT5700M-CN 5G 系列模组 AT 命令手册》
//
//	§13.18 AT^CASCELLINFO?  LTE CA 状态下 SCELL 信息（最多 4 个 SCell）
//	§13.27 AT^MONSSC        NSA(EN-DC) 情况下查询辅连接服务小区（PSCell + SCell，可多行）
//
// ⚠️ 与移远（AT+QCAINFO）最大的不同：**"没有聚合"用 ERROR / NONE 表示**（手册明确），
// 不是空应答 —— 所以拿到 ERROR 要当成"当前没有 CA"，而不是"指令不支持"。
//
// ⚠️ 实测（2026-09-30，中国电信 460/11 驻留 n78 SA）：
//
//	AT^CASCELLINFO? → ERROR       （= 当前没有 LTE CA，符合手册“CA 未配置时返回 ERROR”）
//	AT^MONSSC       → ^MONSSC: NONE（= 当前没有辅连接，SA 组网本就没有 EN-DC SCG）
//
// 也就是说这两条指令**在本机当前组网下拿不到聚合数据**（没有 CA 可报），
// 解析逻辑按手册给的例子写成单测样例；等模组进 LTE CA / NSA 双连接时才有真实数据。

const (
	casCellInfo = "AT^CASCELLINFO?"
	monssc      = "AT^MONSSC"
)

// CACmds 本机 CA 查询指令（界面拿它渲染提示，别再写死移远的 AT+QCAINFO）
func (m *MT5700Command) CACmds() []string {
	return []string{casCellInfo, monssc}
}

// CAEmptyNote 空列表的说明：这两条指令**没有聚合时分别回 ERROR / NONE**，
// 那不是"不支持"，界面要讲清楚，免得用户以为是设备不报。
func (m *MT5700Command) CAEmptyNote() string {
	return "本机没有聚合时，" + casCellInfo + " 回 ERROR、" + monssc + " 回 NONE（属正常）；" +
		"本固件只查 LTE CA 与 NSA 辅连接，NR SA 的载波聚合没有查询指令"
}

// QueryCA 查两组辅小区：LTE CA 的 SCell + NSA 辅连接小区。
//
// 两条都发：各自在自己的组网下才会回数据（另一个回 ERROR / NONE），
// 单条指令很快，换来的是"不管当前在 LTE CA 还是 NSA 都能报出来"。
func (m *MT5700Command) QueryCA(at iface.ATDoer) ([]iface.CCInfo, error) {
	out := []iface.CCInfo{}
	if resp, err := at.SendAT(casCellInfo); err == nil {
		out = append(out, ParseCASCellInfo(resp)...)
	} else {
		logger.Warn("mt5700 query CASCELLINFO failed", zap.Int("modem", at.ModemID()), zap.Error(err))
	}
	if resp, err := at.SendAT(monssc); err == nil {
		out = append(out, ParseMONSSC(resp)...)
	} else {
		logger.Warn("mt5700 query MONSSC failed", zap.Int("modem", at.ModemID()), zap.Error(err))
	}
	return out, nil
}

// ---- 锁网（AT^LTEFREQLOCK / AT^NRFREQLOCK / AT^SYSCFGEX）----

//
// 手册：《MT5700M-CN 5G 系列模组 AT 命令手册》
//
//	§13.12 AT^LTEFREQLOCK=<operatetype>[,<forbidFlag>,<num>[,<band>[,<arfcn>[,<pci>]]]]
//	§13.13 AT^NRFREQLOCK=<operatetype>[,<forbidFlag>,<num>[,<band>,<arfcn>[,<scstype>[,<pci>]]]]
//	§13.2  AT^SYSCFGEX=<acqorder>,<band>,<roam>,<srvdomain>,<lteband>,<reserve1>,<reserve2>
//
// 与移远那套（AT+QNWPREFCFG / AT+QNWLOCK）的关键区别：
//  1. **锁频段 / 锁频点 / 锁小区是同一条指令的三个 operatetype**（3 / 1 / 2），查询应答也是同一格式；
//  2. band / arfcn / pci 都是**字符串**参数（可一次带一组，逗号分隔），NR 还要额外交 scstype；
//  3. 查询应答是三段式：锁类型行 + 汇总行（`forbidFlag,num`）+ 若干数据行；
//  4. 解锁就是 `operatetype=0` 一步，**没有 policy 频段可写回**；
//  5. 锁定**掉电保存**（属性表 Y），不需要额外的 save_ctrl 指令；
//  6. ⚠️ 下发锁 / 解锁后要**切一次飞行模式（软关机软开机）**才生效（手册 §13.12 / §13.13 备注）。
//
// 实机核对（2026-09-30，中国电信 460/11 驻留 n78；每条都 OK，测完已解锁复原）：
//
//	AT^NRFREQLOCK=3,1,1,"78"                    → ^NRFREQLOCK: 3 / 1,1 / 78
//	AT^NRFREQLOCK=2,1,1,"78","627264","1","277" → ^NRFREQLOCK: 2 / 1,1 / 78,627264,1,277
//	AT^NRFREQLOCK=1,1,1,"78","627264","1"       → ^NRFREQLOCK: 1 / 1,1 / 78,627264,1
//	AT^NRFREQLOCK=0                             → ^NRFREQLOCK: 0
//	AT^LTEFREQLOCK=3,1,1,"1"                    → ^LTEFREQLOCK: 3 / 1,1 / 1
//	AT^LTEFREQLOCK=2,1,1,"1","100","300"        → ^LTEFREQLOCK: 2 / 1,1 / 1,100,300
//	AT^LTEFREQLOCK=1,1,1,"1","100"              → ^LTEFREQLOCK: 1 / 1,1 / 1,100
//
// 同一个 operatetype 编号在两条指令里的含义相同：
//
//	0 = 关闭锁定   1 = 锁频点   2 = 锁小区（频点+PCI）   3 = 锁频段

const (
	lockOff  = 0
	lockFreq = 1 // 锁频点：band + arfcn（NR 还要 scstype）
	lockCell = 2 // 锁小区：再加 pci
	lockBand = 3 // 锁频段：只带一组 band
)

// allowHandover 允许重选和切换（forbidFlag=1）。
//
// 两个用途用不同的值：
//   - **锁频段**：=1，把业务限制在这几个频段，但在频段内仍然允许正常重选/切换（否则会死死钉在一个小区上）；
//   - **锁频点 / 锁小区**：=0「不允许重选和切换」，这才是"钉住某小区"的本意。
const allowHandover = 1

// lockCmd 取该技术域对应的锁网指令名
func lockCmd(tech iface.CellTech) string {
	if tech == iface.CellTechNR {
		return "AT^NRFREQLOCK"
	}
	return "AT^LTEFREQLOCK"
}

// ---------- NetLocker 的指令实现 ----------

// CommandQueryBandLock 查询频段/频点/小区锁定状态（同一条指令，三种锁共用一个查询）
func (m *MT5700Command) CommandQueryBandLock(tech iface.CellTech) string {
	return lockCmd(tech) + "?"
}

// CommandQueryCellLock 与频段查询是同一条指令：鼎桥用 operatetype 区分锁的类型
func (m *MT5700Command) CommandQueryCellLock(tech iface.CellTech) string {
	return lockCmd(tech) + "?"
}

// CommandQueryBandCapability 该固件的“设备能力频段”查询。
//
// ⚠️ 只能拿 LTE：实测 `AT^SYSCFGEX=?` 列的是 GSM/WCDMA 频带位图与 **LTE BC 名单**
// （`((1e200000095,"LTE BC1/LTE BC3/LTE BC5/LTE BC8/LTE BC34/LTE BC38/LTE BC39/LTE BC40/LTE BC41"),
// (7fffffffffffffff,"All bands"))`），**没有 NR 频段名单**；
// 其它可能的来源（AT^NRRCCAPQRY / AT^ERRCCAPQRY / AT+CNBP?）在本固件全回 ERROR。
// 所以 NR 留空，界面会显示 BandCapabilityNote 里的说明，而不是编一份“支持列表”出来。
func (m *MT5700Command) CommandQueryBandCapability(tech iface.CellTech) string {
	if tech == iface.CellTechLTE {
		return "AT^SYSCFGEX=?"
	}
	return ""
}

// QueryBandCapability 设备支持的频段（界面上的「设备能力」列）。
// LTE 取自 AT^SYSCFGEX=? 的 LTE BC 名单；NR 本固件不提供（所以不返回该键）。
func (m *MT5700Command) QueryBandCapability(at iface.LockATDoer) map[iface.CellTech][]int {
	resp, err := at.SendAT("AT^SYSCFGEX=?")
	if err != nil {
		logger.Warn("mt5700 query band capability failed", zap.Int("modem", at.ModemID()), zap.Error(err))
		return nil
	}
	lte := common.ParseLTEBandNames(resp)
	if len(lte) == 0 {
		return nil
	}
	return map[iface.CellTech][]int{iface.CellTechLTE: lte}
}

// BandCapabilityNote 「设备能力」列拿不到时的说明（界面会在该列显示它，而不是光一个 `-`）
func (m *MT5700Command) BandCapabilityNote(tech iface.CellTech) string {
	if tech == iface.CellTechNR {
		return "本固件不上报 NR 频段能力（AT^SYSCFGEX=? 只列 LTE 频段）"
	}
	return ""
}

// CommandQueryPolicyBand 该固件没有 policy 频段（解锁走 operatetype=0，见 CommandUnlockBand）
func (m *MT5700Command) CommandQueryPolicyBand() string { return "" }

// CommandSetBandLock 锁频段：`AT^NRFREQLOCK=3,1,<num>,"<band1>,<band2>"`。
//
// bands 至少一个才下发（空列表由上层改走 CommandUnlockBand，绝不下发空串）。
func (m *MT5700Command) CommandSetBandLock(tech iface.CellTech, bands []int) string {
	if len(bands) == 0 || tech == iface.CellTechNRNSA {
		return "" // 一套 NR 列表：没有"单独锁 NSA"这条指令（见 iface.NSAIndependent）
	}
	parts := make([]string, 0, len(bands))
	for _, b := range bands {
		parts = append(parts, strconv.Itoa(b))
	}
	return fmt.Sprintf(`%s=%d,%d,%d,"%s"`,
		lockCmd(tech), lockBand, allowHandover, len(parts), strings.Join(parts, ","))
}

// CommandLockCell 锁频点（lock_type=2）/ 锁小区（lock_type=1）/ 解锁。
// 频点与 PCI 都必须带 band（手册：锁频点需携带 band+arfcn，锁小区再加 pci）。
func (m *MT5700Command) CommandLockCell(req iface.CellLockRequest) string {
	cmd := lockCmd(req.Tech)
	if req.LockType == iface.CellLockNone {
		return fmt.Sprintf("%s=%d", cmd, lockOff)
	}
	band := req.Band()
	arfcn := strconv.Itoa(req.ARFCN)
	// scstype 是索引（0=15k 1=30k 2=60k 3=120k 4=240k），索引在最后
	scs := strconv.Itoa(iface.NormalizeSCS(req.SCS))
	if req.Tech == iface.CellTechNR {
		if req.LockType == iface.CellLockARFCNPCI {
			return fmt.Sprintf(`%s=%d,0,1,"%d","%s","%s","%d"`,
				cmd, lockCell, band, arfcn, scs, req.PCID)
		}
		return fmt.Sprintf(`%s=%d,0,1,"%d","%s","%s"`,
			cmd, lockFreq, band, arfcn, scs)
	}
	// LTE 没有 scstype
	if req.LockType == iface.CellLockARFCNPCI {
		return fmt.Sprintf(`%s=%d,0,1,"%d","%s","%d"`,
			cmd, lockCell, band, arfcn, req.PCID)
	}
	return fmt.Sprintf(`%s=%d,0,1,"%d","%s"`, cmd, lockFreq, band, arfcn)
}

// CommandUnlockBand 解除频段锁定：operatetype=0（锁频段/锁频点/锁小区一起清掉）
func (m *MT5700Command) CommandUnlockBand(tech iface.CellTech) string {
	return fmt.Sprintf("%s=%d", lockCmd(tech), lockOff)
}

// CommandBandLockCaps / CommandCellLockCaps 能力探测（只读，对应"探测固件支持"按钮）
func (m *MT5700Command) CommandBandLockCaps() string { return "AT^LTEFREQLOCK=?" }
func (m *MT5700Command) CommandCellLockCaps() string { return "AT^NRFREQLOCK=?" }

// CommandSaveCellLock 该固件锁定本身就掉电保存（属性表 Y），不需要额外指令
func (m *MT5700Command) CommandSaveCellLock() string { return "" }

// ---------- 组网模式偏好（AT^SYSCFGEX） ----------

// CommandQueryModePref 组网模式：AT^SYSCFGEX? 回 `^SYSCFGEX: "<acqorder>",<band>,<roam>,<srvdomain>,<lteband>`
func (m *MT5700Command) CommandQueryModePref() string { return "AT^SYSCFGEX?" }

// CommandSetModePref 恒返回空串：这条指令**不能只写一个模式**（手册 §13.2 注意事项 2：
// 7 个必选参数少一个就 +CME ERROR: 50），必须先回读当前 band/lteband 再整串改写，
// 所以由 SetModePrefVendor 自己组装（上层看到空串不会下发任何指令）。
func (m *MT5700Command) CommandSetModePref(string) string { return "" }

// AcqOrder 组网模式 → at^syscfgex 的 <acqorder>。
//
// 取值（§13.2.3）："01" GSM（不支持）/ "02" WCDMA / "03" LTE / "08" NR，按优先级顺序拼接。
//   - AUTO     → "080302"：本机出厂默认值（NR→LTE→WCDMA），也就是"全制式自动"；
//   - NR5G     → "08"；
//   - LTE      → "03"；
//   - LTE:NR5G → "0803"：LTE 与 NR 都允许，NR 优先（与出厂默认的优先级一致）。
func AcqOrder(mode string) string {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "AUTO":
		return "080302"
	case "NR5G":
		return "08"
	case "LTE":
		return "03"
	case "LTE:NR5G":
		return "0803"
	}
	return ""
}

// ModeFromAcq 反过来：<acqorder> → 界面上的模式名。
// 含 LTE+NR 且还含 2G/3G 的（如 "080302"）算 AUTO。
func ModeFromAcq(acq string) string {
	acq = strings.Trim(strings.TrimSpace(acq), `"`)
	hasNR := strings.Contains(acq, "08")
	hasLTE := strings.Contains(acq, "03")
	hasOther := strings.Contains(acq, "01") || strings.Contains(acq, "02") ||
		strings.Contains(acq, "04") || strings.Contains(acq, "07")
	switch {
	case hasNR && hasLTE && hasOther:
		return "AUTO"
	case hasNR && hasLTE:
		return "LTE:NR5G"
	case hasNR:
		return "NR5G"
	case hasLTE:
		return "LTE"
	}
	return ""
}

// ParseModePref 解析组网模式偏好（LockVendorOps）
func (m *MT5700Command) ParseModePref(resp []string) (string, bool) {
	cfg, ok := common.ParseSyscfgEX(resp)
	if !ok {
		return "", false
	}
	mode := ModeFromAcq(cfg.AcqOrder)
	return mode, mode != ""
}

// SetModePrefVendor 组网模式：先回读再整串改写（`AT^SYSCFGEX="03",<band>,<roam>,<srvdomain>,<lteband>,,`，
// 与手册 §13.2 的 LTE only 示例同形）。
//
// 只改 <acqorder>，<band>/<lteband> 原样带回去 —— 这条指令同时能配频带，
// 如果按"从零构造"就会把频带位图改成 0（等于禁用全部频带，模组直接脱网）。
func (m *MT5700Command) SetModePrefVendor(at iface.LockATDoer, mode string) (bool, error) {
	acq := AcqOrder(mode)
	if acq == "" {
		return true, fmt.Errorf("不支持的组网模式 %q（MT5700 支持 AUTO/LTE/NR5G/LTE:NR5G）", mode)
	}
	resp, err := at.SendAT("AT^SYSCFGEX?")
	if err != nil {
		return true, err
	}
	cfg, ok := common.ParseSyscfgEX(resp)
	if !ok {
		return true, fmt.Errorf("AT^SYSCFGEX? 应答无法解析，拒绝改写（避免把频带位图写坏）")
	}
	cmd := fmt.Sprintf(`AT^SYSCFGEX="%s",%s,%d,%d,%s,,`, acq, cfg.Band, cfg.Roam, cfg.SrvDomain, cfg.LTEBand)
	logger.Info("mt5700 mode_pref apply",
		zap.Int("modem", at.ModemID()), zap.String("mode", mode),
		zap.String("acqorder", acq), zap.String("command", cmd))
	return true, at.SendLockCommand(cmd)
}

// ---------- 应答解析 ----------

// FreqLock AT^LTEFREQLOCK? / AT^NRFREQLOCK? 的解析结果
type FreqLock struct {
	Op    int   // 0=关闭 1=锁频点 2=锁小区 3=锁频段
	Bands []int // op=3 时是"被限制的频段"；op=1/2 时是对应小区的频段
	ARFCN int   // op=1/2
	SCS   int   // NR 的 scstype（0~4）
	PCID  int   // op=2
	Raw   string
}

// ParseFreqLock 解析三段式应答（实测格式）：
//
//	^NRFREQLOCK: 2        ← 锁类型
//	1,1                   ← 汇总：forbidFlag,num（固定第二行，位置固定，不能按字段数猜）
//	78,627264,1,277       ← 数据行：NR=band,arfcn,scstype,pci / LTE=band,arfcn,pci
//
// ⚠️ 两个坑：
//   - 汇总行与 LTE 的"锁频点"数据行**字段数相同**（都是 2 格），所以只能按位置区分；
//   - 模组每秒刷 ^RSSI/^HCSQ 等 URC，长应答里会夹进来，必须只认"纯数字+逗号"的数据行。
func ParseFreqLock(tech iface.CellTech, resp []string) (FreqLock, bool) {
	var out FreqLock
	seenOp, seenSummary := false, false
	for _, line := range resp {
		t := strings.TrimSpace(line)
		u := strings.ToUpper(t)
		if i := strings.Index(u, "^LTEFREQLOCK:"); i >= 0 {
			out.Op, _ = strconv.Atoi(strings.TrimSpace(t[i+len("^LTEFREQLOCK:"):]))
			seenOp = true
			continue
		}
		if i := strings.Index(u, "^NRFREQLOCK:"); i >= 0 {
			out.Op, _ = strconv.Atoi(strings.TrimSpace(t[i+len("^NRFREQLOCK:"):]))
			seenOp = true
			continue
		}
		if !seenOp || !common.IsDigitsAndCommas(t) {
			continue // URC（^RSSI/^HCSQ/+CMTI…）/ OK / ERROR / 回显：都不是数据行
		}
		if !seenSummary {
			seenSummary = true // 汇总行（forbidFlag,num）跳过
			continue
		}
		f := common.SplitATFields(t)
		if len(f) == 0 {
			continue
		}
		if b, err := strconv.Atoi(f[0]); err == nil && b > 0 {
			out.Bands = append(out.Bands, b)
		}
		if len(f) > 1 {
			if n, err := strconv.Atoi(f[1]); err == nil {
				out.ARFCN, out.Raw = n, t
			}
		}
		// 字段含义按制式分：NR 多一格 scstype，PCI 在最后
		//   NR ：band,arfcn,scstype,pci
		//   LTE：band,arfcn,pci
		if tech == iface.CellTechNR {
			if len(f) > 2 {
				if n, err := strconv.Atoi(f[2]); err == nil {
					out.SCS = n
				}
			}
			if len(f) > 3 {
				if n, err := strconv.Atoi(f[3]); err == nil {
					out.PCID = n
				}
			}
		} else if len(f) > 2 {
			if n, err := strconv.Atoi(f[2]); err == nil {
				out.PCID = n
			}
		}
	}
	return out, seenOp
}

// ParseBandLockState 频段限制：只有 operatetype=3 才是"频段被限制"
func (m *MT5700Command) ParseBandLockState(tech iface.CellTech, resp []string) ([]int, bool) {
	fl, ok := ParseFreqLock(tech, resp)
	if !ok || fl.Op != lockBand || len(fl.Bands) == 0 {
		return nil, false
	}
	return fl.Bands, true
}

// ParseCellLockState 频点/小区锁定：
//   - operatetype=2（锁小区）→ lock_type=1（频点+PCI）
//   - operatetype=1（锁频点）→ lock_type=2（仅频点）
//   - operatetype=3（锁频段）不算小区锁定（频段限制在「锁频段」卡片里展示）
func (m *MT5700Command) ParseCellLockState(tech iface.CellTech, resp []string) (iface.CellLock, bool) {
	fl, ok := ParseFreqLock(tech, resp)
	if !ok {
		return iface.CellLock{}, false
	}
	cl := iface.CellLock{RawValue: strings.Join(resp, "|")}
	switch fl.Op {
	case lockCell:
		cl.LockType, cl.ARFCN, cl.PCID, cl.Locked = iface.CellLockARFCNPCI, fl.ARFCN, fl.PCID, true
	case lockFreq:
		cl.LockType, cl.ARFCN, cl.Locked = iface.CellLockARFCN, fl.ARFCN, true
	}
	return cl, true
}

// StatusNote 该机型锁网的额外提示（界面上会显示在锁网卡片下方）。
//
// 两条都是实测/手册确认的硬约束：
//   - 锁频段 / 锁频点 / 锁小区**共用同一条指令的三个 operatetype**，所以后下发的会覆盖前一个
//     （实测：先锁频段 78 再锁小区，频段锁就变成未限制）；
//   - 下发锁 / 解锁后要切一次飞行模式（软关机软开机）才生效（手册 §13.12 / §13.13 备注）。
func (m *MT5700Command) StatusNote() string {
	return "MT5700 的锁频段 / 锁频点 / 锁小区共用同一条指令（AT^LTEFREQLOCK / AT^NRFREQLOCK），" +
		"后下发的会覆盖前一个；下发锁/解锁后需切一次飞行模式（软关机软开机）才生效；锁定本身掉电保存"
}

// ---- 指令集（自适应底座 + 鼎桥私有覆盖）----

// MT5700Command MT5700 的指令集：自适应底座（common.AdaptiveCommands）之上，
// 覆盖掉识别、候选指令、以及所有本方私有的查询/锁网/CA 能力。
type MT5700Command struct{ common.AdaptiveCommands }

// DetectVendor 识别：ATI 里含 "MT5700"
func (m *MT5700Command) DetectVendor(resp []string) iface.ModemVendor {
	for _, s := range resp {
		if strings.Contains(strings.ToUpper(s), "MT5700") {
			return iface.VendorTDTech
		}
	}
	return iface.VendorUnknown
}

// ServingCellCandidates 服务小区：首选鼎桥自家的 AT^MONSC；
// 后面三条留给"同型号但换了固件、改用通用命令"的情况（实测本固件全回 ERROR）。
func (m *MT5700Command) ServingCellCandidates() []string {
	return []string{"AT^MONSC", common.CmdQENGServing, common.CmdCPSI, "AT+QCSQ"}
}

// NeighbourCellCandidates 邻区：首选 AT^MONNC（一行一个小区）。
func (m *MT5700Command) NeighbourCellCandidates() []string {
	return []string{"AT^MONNC", `AT+QENG="neighbourcell"`}
}

// TrafficCandidates 流量统计：AT^DSFLOWQRY 回**累计收发字节**（十六进制）→ 走"计数器式"路径
// （差分算速度、配额按累计量算），不需要 AT+QGDCNT 那种"先使能"的步骤。
func (m *MT5700Command) TrafficCandidates() []string { return []string{"AT^DSFLOWQRY", "AT+QGDCNT?"} }

// TrafficEnableCommand DS 计数器一直在计，没有使能指令。
func (m *MT5700Command) TrafficEnableCommand() string { return "" }

// CommandNetInfo 链路制式/系统信息：AT^QNWINFO 不支持，用鼎桥的 AT^SYSINFOEX。
func (m *MT5700Command) CommandNetInfo() string { return "AT^SYSINFOEX" }

// CommandTemp 温度：AT^CHIPTEMP?（12 路传感器，单位 0.1 ℃；失败时该路回 65535）。
func (m *MT5700Command) CommandTemp() string { return "AT^CHIPTEMP?" }

// CommandICCID ICCID：AT+CCID 在本固件回 ERROR，要用 AT^ICCID?。
func (m *MT5700Command) CommandICCID() string { return "AT^ICCID?" }

// CommandExtraNetInfo 工作频率（频段 / ARFCN / 带宽）：AT^HFREQINFO?。
func (m *MT5700Command) CommandExtraNetInfo() string { return "AT^HFREQINFO?" }

// CommandCAInfo MT5700 的载波聚合指令（AT^CASCELLINFO）只覆盖 LTE CA，
// NR 下没有等价指令 —— 宁可不报也不发未经验证的指令。
func (m *MT5700Command) CommandCAInfo() string { return "" }

// CommandAMBR 签约速率（AMBR）：鼎桥自家的 AT^DSAMBR=<cid>（手册 §16.17）。
//
// 手册明说“目前只支持 cid 为 1 的查询”，实测应答：
//
//	AT^DSAMBR=1 → ^DSAMBR: 1,1000000,1000000,"ctnet",6
//
// 单位 kbps（这里换算成 Mbps 展示）。cid 1 正是带本机 IP 的数据上下文
// （AT+CGPADDR 的 cid 1），所以不必动态找 cid。
func (m *MT5700Command) CommandAMBR() string { return "AT^DSAMBR=1" }

// CommandSMSModePDU 切到 PDU 模式：该固件只在 PDU 模式下回短信正文（见 PDUSMSReader）。
func (m *MT5700Command) CommandSMSModePDU() string { return "AT+CMGF=0" }

// CommandReadAllSMSPDU PDU 模式下 4 = 全部短信（对应文本模式的 "ALL"）。
func (m *MT5700Command) CommandReadAllSMSPDU() string { return "AT+CMGL=4" }

// ParseNRSignal 从鼎桥的两种上报行里取 RSRP/RSRQ/SINR：
//
//	^MONSC: NR,460,11,627264,1,CFA116002,115,CFA103,-78,-10,30   （直接 dB）
//	^HCSQ:  "NR",63,206,30                                       （索引）
func (m *MT5700Command) ParseNRSignal(line string) (rsrp, rsrq, sinr int, ok bool) {
	if rsrp, rsrq, sinr, ok := ParseMONSCSignal(line); ok && rsrp != 0 {
		return rsrp, rsrq, sinr, true
	}
	return ParseHCSQSignal(line)
}

// 编译期断言：MT5700 的能力集与搬家前完全一致（少了/多了一个方法都会编译失败）
var (
	_ iface.ModemCommand  = (*MT5700Command)(nil)
	_ iface.CAVendorOps   = (*MT5700Command)(nil)
	_ iface.NetLocker     = (*MT5700Command)(nil)
	_ iface.LockVendorOps = (*MT5700Command)(nil)
)
