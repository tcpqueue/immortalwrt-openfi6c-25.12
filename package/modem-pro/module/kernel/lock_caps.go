package kernel

import (
	"go.uber.org/zap"
	"strings"

	"modemd/module/common"
	"modemd/module/foxconn"
	"modemd/module/gosuncn"
	"modemd/module/lock"
	"modemd/module/neoway"
	"modemd/module/netdev"
	"modemd/module/openluat"
	"modemd/module/platform"
	"modemd/module/quectel"
	"modemd/module/simcom"
	"modemd/module/telit"
	"modemd/module/thales"
	"modemd/module/unisoc"
	"modemd/module/variant"
)

// ====================== 厂商能力接口（定义在 atcmd.go） ======================

// netLockerOf 已搬到 modemd/module/common（NetLockerOf）；
// kernel 保留同名别名，见 variant_alias.go。

// lockVendorOpsOf 取锁网的可选厂商扩展；未实现返回 nil
func lockVendorOpsOf(l NetLocker) LockVendorOps {
	ops, _ := l.(LockVendorOps)
	return ops
}

// BandLockSetter 已搬到 modemd/iface（见 iface/lock.go）；kernel 保留同名别名。

// lockCandidate 一套候选锁网方言：只读探测指令 + 被固件接受时应出现的标记。
// lockCandidates 候选锁网方言，按**平台提示**排先后（同平台方言更接近），
// 但最终以固件**实际接受**的为准 —— 实测证明"同平台 = 同命令"不成立：
// QModem 真机抓包 `testcases/quectel/unisoc/rm500u-cnv-...` 显示展锐平台的
// RM500U-CNV 照样认 `AT+QNWPREFCFG="lte_band"/"nr5g_band"`。
func (mod *Modem) lockCandidates() []variant.LockCandidate {
	// base 顺序按平台提示（同平台方言更接近）—— 顺序是**数据**，见 vendor_platform.go 的
	// lockBaseDefault / lockBaseQualcomm / lockBaseMediaTek / lockBaseHiSilicon
	base := variant.LockCandidatesByName(variant.LockBaseDefault)
	if v := variant.PlatformVariantFor(mod.Vendor, mod.Model+" "+mod.ModelName); v != nil && len(v.LockBase) > 0 {
		base = variant.LockCandidatesByName(v.LockBase)
	}
	// 次要方言（列表式锁频段 / 高新兴 / 旧款 QCFG band；均无锁小区或沿用 QNW 锁小区）：
	// ⚠️ qcfgband 排在最前：它是**旧款移远的替代路径**，若排在末尾，一台只认它的设备
	// 要白试 8 条指令才命中（探测只做一次并缓存，但每条都要等超时/ERROR 往返）。
	extra := []variant.LockCandidate{
		{Name: "qcfgband", Locker: quectel.QcfgBandCommand(), Probe: `AT+QCFG="band"`, Mark: `+QCFG: "BAND"`},
		{Name: "cnbp", Locker: simcom.CnbpCommand(), Probe: "AT+CNBP?", Mark: "+CNBP:"},
		{Name: "gosuncn", Locker: &gosuncn.GosuncnLockCommand{}, Probe: "AT+ZBAND?", Mark: "+ZBAND:"},
		{Name: "telit", Locker: telit.TelitLockCommand(), Probe: "AT#BND?", Mark: "#BND:"},
		{Name: "thales", Locker: thales.BandPrefLockCommand(), Probe: "AT+BAND_PREF?", Mark: "+BAND_PREF:"},
		{Name: "foxconn", Locker: foxconn.BandPrefLockCommand(), Probe: "AT^BAND_PREF?", Mark: "^BAND_PREF:"},
		{Name: "neoway", Locker: neoway.NeowayLockCommand(), Probe: "AT+NWSETBAND?", Mark: "+NWSETBAND:"},
		{Name: "atband", Locker: &lock.AtBandLocker{}, Probe: "AT!BAND?", Mark: "!BAND:"},
		{Name: "openluat", Locker: &openluat.OpenluatLockCommand{}, Probe: "AT*BAND?", Mark: "*BAND:"},
	}
	list := append(base, extra...)
	// 展锐 SIPC（中兴 F50 / UMS9620：AT 走 `/dev/stty_nr*`）用的是 `AT+SP*` 私令，
	// 与上表所有方言都不一样。把探针放到**最前**：这条通道一条 AT 只要 ~10ms，
	// 没必要让它在 13 套已知方言上白试一轮（其余机型一个字不受影响）。
	if netdev.IsMu300SIPCChan(mod.ATPort) {
		list = append([]variant.LockCandidate{{Name: "unisoc", Locker: &unisoc.UnisocLockCommand{}, Probe: unisoc.CmdSPCellLockCaps, Mark: "+SPCELLLOCK:"}}, list...)
	}
	return list
}

// preferredLocker 按"平台提示 + 厂商默认"猜一套（**探测不可用时**的回退）。
//
// ⚠️ 这只是猜测：广和通一家三条平台三套指令（高通 QNW / MTK AT+GTACT / 展锐未接入），
// 芯讯通 SIM8200 虽是高通平台但官方手册给的是 AT+CSYSSEL 这一套。
// 真正用哪套以运行期探测（lockerFor）为准。
func (mod *Modem) preferredLocker() NetLocker {
	model := mod.Model + " " + mod.ModelName
	// 展锐 SIPC（中兴 F50 / UMS9620）：`AT+SP*` 私令方言，见 lock_unisoc.go
	// （只做能力探测与只读，锁写不下发）。先于下面所有机型判断。
	if netdev.IsMu300SIPCChan(mod.ATPort) {
		return &unisoc.UnisocLockCommand{}
	}
	// openluat（Air724UG）：锁频段走 AT*BAND —— 应答是复合字段，本实现**回读后只替换
	// lte_high/lte_low 两个字段**，其余（含漫游/服务域）原样写回，见 lock_openluat.go。
	if platform.IsOpenluatModel(model) {
		return &openluat.OpenluatLockCommand{}
	}
	if platform.IsSimcomLockModel(model) {
		return &simcom.SimcomLockCommand{}
	}
	if platform.IsGosuncnModel(model) {
		return &gosuncn.GosuncnLockCommand{}
	}
	if platform.IsTelitModel(model) {
		return telit.TelitLockCommand()
	}
	if platform.IsThalesModel(model) {
		return thales.BandPrefLockCommand()
	}
	if platform.IsFoxconnModel(model) {
		return foxconn.BandPrefLockCommand()
	}
	if platform.IsNeowayModel(model) {
		return neoway.NeowayLockCommand()
	}
	// 剩下的按"厂商家族 × 平台档位"查变体表（含移远 lte 旧款 → AT+QCFG="band"、
	// 广和通 MTK/展锐 → AT+GTACT、巴龙 → ^LTEFREQLOCK、展锐 → 通用 QNW…）。
	// 具体哪档对应哪套方言见 vendor_platform.go 的 platformVariants。
	if v := variant.PlatformVariantFor(mod.Vendor, model); v != nil && v.Locker != nil {
		return v.Locker(mod.device())
	}
	// ASR（A8200 那类）与未识别平台：**不猜** —— 方言未知，留给运行期候选探测。
	return common.NetLockerOf(mod.cmdImpl)
}

// lockFamilyLabels 方言技术名 → 界面显示名（与 lockCandidates 的 name 一一对应）。
// 现场排查（"为什么这台报了 ERROR"）第一件要看的就是本机到底选中了哪套方言。
var lockFamilyLabels = map[string]string{
	"unisoc":   "展锐 SP*（组网模式=AT+SP5GRAN 5G 开关；小区锁定只能解除；锁目标值语义未取证 ⇒ 不写）",
	"qnw":      "高通 QNW（AT+QNWPREFCFG / AT+QNWLOCK）",
	"qcfgband": "移远旧款 4G：AT+QCFG=\"band\"（LTE 位图）",
	"cnbp":     "芯讯通旧款 4G：AT+CNBP（LTE 位图）",
	"mtk":      "联发科 + 展锐 GT（AT+GTACT / AT+GTCELLLOCK）",
	"simcom":   "芯讯通（AT+CSYSSEL / AT+CCELLCFG）",
	"balong":   "海思巴龙（AT^LTEFREQLOCK / AT^NRFREQLOCK）",
	"gosuncn":  "高新兴（AT+ZBAND / AT+ZLOCKCELL）",
	"telit":    "Telit（AT#BND）",
	"thales":   "Thales（AT+BAND_PREF）",
	"foxconn":  "Foxconn（AT^BAND_PREF）",
	"neoway":   "有方（AT+NWSETBAND）",
	"atband":   "华为 / Sierra（AT!BAND）",
	"openluat": "openluat Air724UG（AT*BAND）",
}

// lockDialect 描述本机实际使用的锁网方言：
//
//	name   技术名（空串 = 探测没命中，走机型猜测）
//	label  界面显示名
//	note   说明（探测选中 / 为什么回退）
//
// 调用前必须先经过 lockerFor()（它会把结果缓存在 mod 上）。
func (mod *Modem) lockDialect() (name, label, note string) {
	mod.mu.Lock()
	probed := mod.lockProbed
	name = mod.lockFamily
	mod.mu.Unlock()
	if !probed {
		return "", "", ""
	}
	if name == "" {
		return "", "", "未探测到可用的锁网方言（候选指令全部被固件拒绝，或模组尚未启动），" +
			"当前是按机型 " + mod.Model + " 的猜测实现，命令可能不被接受 —— " +
			"可点「固件支持探测」看每条指令的真实应答。"
	}
	return name, lockFamilyLabels[name], "运行期只读探测选中该方言（唯一可信口径：同平台不同厂商/固件方言都可能不同）。"
}

// lockerFor 取该模组**实际可用**的锁网实现。
//
// 不按"平台/厂商"硬编码 —— 同平台不同厂商、甚至同型号不同固件，方言都可能不同
// （真机抓包见 lockCandidates 注释）。所以：**运行期逐个只读探测候选方言，
// 谁被固件接受就用谁**（同「服务小区」候选探测的做法），结果缓存；
// 全部不接受（或模组还没启动、探测不可用）时退回 preferredLocker 的猜测。
func (mod *Modem) lockerFor() NetLocker {
	if mod == nil || mod.cmdImpl == nil {
		return nil
	}
	mod.mu.Lock()
	if mod.lockProbed {
		l := mod.lockChosen
		mod.mu.Unlock()
		return l
	}
	canProbe := mod.writeChan != nil // 没启动（含单元测试）时探测必失败
	mod.mu.Unlock()

	var chosen NetLocker
	family := ""
	var tried []string
	if canProbe {
		for _, c := range mod.lockCandidates() {
			ok := mod.lockCandidateAccepted(c)
			mark := "拒绝"
			if ok {
				mark = "接受"
			}
			// 探测记录：界面直接展示，现场排查用
			tried = append(tried, c.Name+": "+mark+"（"+c.Probe+"）")
			if ok {
				logger.Info("lock family selected",
					zap.Int("modem", mod.ID), zap.String("family", c.Name), zap.String("probe", c.Probe))
				chosen = c.Locker
				family = c.Name
				break
			}
		}
	}
	if chosen == nil {
		chosen = mod.preferredLocker()
	}
	// 锁小区写法按平台分档（展锐 / 旧款 4G 与高通不同）。候选表与 preferredLocker 里
	// 都是无参构造器，只有这里（已经能拿到 mod 了）才能把档位补上。
	chosen = variant.ApplyCellProfile(chosen, mod.device())
	mod.mu.Lock()
	mod.lockProbed = true
	mod.lockChosen = chosen
	mod.lockFamily = family
	mod.lockTried = tried
	mod.mu.Unlock()
	return chosen
}

// lockCandidateAccepted 只读探测一套方言：指令不报错、且应答里出现该方言的标记。
func (mod *Modem) lockCandidateAccepted(c variant.LockCandidate) bool {
	resp, err := mod.SendAT(c.Probe)
	if err != nil {
		return false
	}
	mark := strings.ToUpper(c.Mark)
	for _, line := range resp {
		if strings.Contains(strings.ToUpper(line), mark) {
			return true
		}
	}
	return false
}

// isFibocomMTK 判断广和通机型是不是 MTK 平台（FM350 系列）。
// ATI 原文/短名里出现 FM350 即认定；FM160/FM150 是高通，FM650 是展锐。
func isFibocomMTK(model string) bool {
	return strings.Contains(strings.ToUpper(model), "FM350")
}

// cmdQNWLockSave 移远固件的小区锁定持久化指令 ——
// 已搬到 modemd/module/quectel（QNWLockSaveCmd）；kernel 保留旧名别名，见 iface_alias.go。

// lockCmdSummary 汇总该机型实际使用的锁网指令，交给界面渲染提示文案。
// 页面上以前把这些指令写死在文案里，换机型就会写成错的，所以改成接口下发。
func lockCmdSummary(l NetLocker) map[string]string {
	save := cmdQNWLockSave
	unlockLTE, unlockNR := "", ""
	if ops := lockVendorOpsOf(l); ops != nil {
		save = ops.CommandSaveCellLock()
		unlockLTE = ops.CommandUnlockBand(CellTechLTE)
		unlockNR = ops.CommandUnlockBand(CellTechNR)
	}
	return map[string]string{
		"band_lte":   l.CommandQueryBandLock(CellTechLTE),
		"band_nr":    l.CommandQueryBandLock(CellTechNR),
		"cell_lte":   l.CommandQueryCellLock(CellTechLTE),
		"cell_nr":    l.CommandQueryCellLock(CellTechNR),
		"mode":       l.CommandQueryModePref(),
		"unlock_lte": unlockLTE,
		"unlock_nr":  unlockNR,
		"save":       save,
	}
}

// ---------- 移远 Quectel ----------

// QnwBandKey / QnwLockKind 与 FormatBandList 已统一到 atcmd.go
// （与指令类型同包），同包内直接用。
