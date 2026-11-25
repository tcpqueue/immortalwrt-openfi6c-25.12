package simcom

import (
	"fmt"
	"strconv"
	"strings"

	"modemd/iface"
	"modemd/module/common"
	"modemd/module/lock"
)

// ====================== 芯讯通 SIMCom 锁网（AT+CSYSSEL / AT+CCELLCFG / AT+C5GCELLCFG） ======================
//
// 指令依据：《SIM8200 Series AT Command Manual V1.01》
//
//	§4.2.20  AT+CSYSSEL   锁频段（lte_band / nr5g_band / nsa_nr5g_band / w_band，冒号分隔；
//	                      `AT+CSYSSEL`（不带参数）恢复全部频段为默认能力）
//	§4.2.21  AT+CCELLCFG  LTE 锁小区（`=1,<pci>,<freq>` 锁、`=0` 解锁、`?` 查询）
//	§4.2.22  AT+C5GCELLCFG NR5G 锁小区（`="pci",<pci>,<freq>,<scs>,<band>` /
//	                      `="arfcn",<len>,<scs:freq>` / `="unlock"` / `?` 查询）
//
// ⚠️ 手册对 AT+C5GCELLCFG 标注了 "not verified test, suggest customer not use it now"
// —— 所以 NR 锁小区下发前请先用「探测固件支持」确认；解析失败时如实返回模组错误，不静默降级。
//
// 与移远/鼎桥都不同：解锁频段没有 policy 写回，直接用 `AT+CSYSSEL`（恢复全部频段）。

type SimcomLockCommand struct{}

// simcomBandKey 技术域 → AT+CSYSSEL 的 key
func simcomBandKey(tech lock.CellTech) string {
	switch tech {
	case lock.CellTechNR:
		return "nr5g_band"
	case lock.CellTechNRNSA:
		return "nsa_nr5g_band" // 手册 §4.2.20 明列该 key
	}
	return "lte_band"
}

// NSABandKey 芯讯通的 AT+CSYSSEL 支持 lte_band / nr5g_band / **nsa_nr5g_band** 三个 key
// ⇒ NR 的 NSA / SA 可以分别锁（见 lock.go 的 NSAIndependent）。
func (f *SimcomLockCommand) NSABandKey() string { return "nsa_nr5g_band" }

// parseCSYSSEL 从 `+CSYSSEL: "<key>",<values>` 取指定 key 的值
func parseCSYSSEL(resp []string, key string) (string, bool) {
	const head = "+CSYSSEL:"
	for _, line := range resp {
		i := strings.Index(strings.ToUpper(line), head)
		if i < 0 {
			continue
		}
		f := lock.SplitATFields(line[i+len(head):])
		if len(f) < 2 || !strings.EqualFold(f[0], key) {
			continue
		}
		return f[1], true
	}
	return "", false
}

// atFieldOr 安全取字段（下标越界返回空串）
func atFieldOr(f []string, i int) string {
	if i >= 0 && i < len(f) {
		return f[i]
	}
	return ""
}

// parseSCSFreq 解析 NR "arfcn" 形式里的 `<scs>:<freq>`（取频点）；纯数字原样返回。
func parseSCSFreq(s string) int {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[i+1:]
	}
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// ---------- NetLocker 的指令实现 ----------

func (f *SimcomLockCommand) CommandQueryBandLock(tech lock.CellTech) string {
	return fmt.Sprintf(`AT+CSYSSEL="%s"`, simcomBandKey(tech))
}

// CommandSetBandLock 设置频段（冒号分隔，与移远同形）。空列表返回空串（绝不写 0）。
func (f *SimcomLockCommand) CommandSetBandLock(tech lock.CellTech, bands []int) string {
	if len(bands) == 0 {
		return ""
	}
	return fmt.Sprintf(`AT+CSYSSEL="%s",%s`, simcomBandKey(tech), lock.FormatBandList(bands))
}

func (f *SimcomLockCommand) CommandQueryBandCapability(lock.CellTech) string { return "" }
func (f *SimcomLockCommand) CommandQueryPolicyBand() string                  { return "" }

func (f *SimcomLockCommand) CommandQueryCellLock(tech lock.CellTech) string {
	if tech == lock.CellTechNR {
		return "AT+C5GCELLCFG?"
	}
	return "AT+CCELLCFG?"
}

// CommandLockCell 构造锁小区指令。上游 vendor/simcom.sh 的 lockcell_qualcomm 原文是：
//
//	LTE：AT+CCELLCFG=1,<pci>,<freq>;+CNMP=38
//	NR ：AT+C5GCELLCFG="pci",<pci>,<freq>,<scs>,<band>;+CNMP=71
//
// ⚠️ 后面那段 `;+CNMP=<n>` 是上游故意加的：**锁小区要先切到对应制式才生效**（上游还会把它记成
// "开机重放"的启动钩子）。少了这一段，用户看到的现象是"点了锁小区但小区没锁住"。
// 解锁（`=0` / `"unlock"`）不带这段：解锁不该顺手改用户的制式偏好。
func (f *SimcomLockCommand) CommandLockCell(req lock.CellLockRequest) string {
	if req.Tech == lock.CellTechNR {
		switch req.LockType {
		case lock.CellLockNone:
			return `AT+C5GCELLCFG="unlock"`
		case lock.CellLockARFCN:
			return fmt.Sprintf(`AT+C5GCELLCFG="arfcn",1,%d:%d;+CNMP=71`, iface.NormalizeSCS(req.SCS), req.ARFCN)
		default:
			return fmt.Sprintf(`AT+C5GCELLCFG="pci",%d,%d,%d,%d;+CNMP=71`,
				req.PCID, req.ARFCN, iface.NormalizeSCS(req.SCS), req.Band())
		}
	}
	switch req.LockType {
	case lock.CellLockNone:
		return "AT+CCELLCFG=0"
	case lock.CellLockARFCN:
		return "" // LTE 的 CCELLCFG 必须带 pci，只锁频点不支持
	default:
		return fmt.Sprintf("AT+CCELLCFG=1,%d,%d;+CNMP=38", req.PCID, req.ARFCN)
	}
}

func (f *SimcomLockCommand) CommandBandLockCaps() string { return "AT+CSYSSEL=?" }
func (f *SimcomLockCommand) CommandCellLockCaps() string { return "AT+CCELLCFG?" }

// 组网模式（AT+CNMP）：QModem `cmds/simcom.sh` / `vendor/simcom.sh` 实测用 2=自动、38=LTE、71=NR5G。
// （芯讯通锁小区时脚本还会顺带 `+CNMP=38/71` 把模式切到对应制式——本实现不替用户改模式，只给显式入口。）
func (f *SimcomLockCommand) CommandQueryModePref() string { return "AT+CNMP?" }

func (f *SimcomLockCommand) CommandSetModePref(mode string) string {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "AUTO":
		return "AT+CNMP=2"
	case "LTE":
		return "AT+CNMP=38"
	case "NR5G":
		return "AT+CNMP=71"
	}
	return ""
}

// ---------- LockVendorOps：应答解析与说明 ----------

// ParseBandLockState 解析 `+CSYSSEL: "nr5g_band",1:28:41:78`。
// ⚠️ 该固件无「设备能力频段」，locked 只表示"配了具体频段列表"，无法与 policy 比对。
func (f *SimcomLockCommand) ParseBandLockState(tech lock.CellTech, resp []string) ([]int, bool) {
	v, ok := parseCSYSSEL(resp, simcomBandKey(tech))
	if !ok {
		return nil, false
	}
	bands, _ := lock.ParseBandList(v)
	return bands, len(bands) > 0
}

// ParseCellLockState 解析锁小区应答：
//
//	LTE: +CCELLCFG: <pci>,<freq>
//	NR : +C5GCELLCFG: "pci",<pci>,<freq>,<scs>,<band>  或  "arfcn",<len>,<scs:freq>
func (f *SimcomLockCommand) ParseCellLockState(tech lock.CellTech, resp []string) (lock.CellLock, bool) {
	cl := lock.CellLock{Tech: tech}
	if tech == lock.CellTechNR {
		for _, line := range resp {
			i := strings.Index(strings.ToUpper(line), "+C5GCELLCFG:")
			if i < 0 {
				continue
			}
			f := lock.SplitATFields(line[i+len("+C5GCELLCFG:"):])
			if len(f) < 2 {
				continue
			}
			cl.RawValue = strings.Join(f, ",")
			switch strings.ToLower(f[0]) {
			case "pci":
				cl.LockType = lock.CellLockARFCNPCI
				cl.PCID = lock.AtIntOr0(f, 1)
				cl.ARFCN = lock.AtIntOr0(f, 2)
				cl.Locked = true
			case "arfcn":
				cl.LockType = lock.CellLockARFCN
				cl.ARFCN = parseSCSFreq(atFieldOr(f, 2))
				cl.Locked = true
			}
		}
		return cl, true
	}
	for _, line := range resp {
		i := strings.Index(strings.ToUpper(line), "+CCELLCFG:")
		if i < 0 {
			continue
		}
		f := lock.SplitATFields(line[i+len("+CCELLCFG:"):])
		if len(f) < 2 {
			continue
		}
		cl.PCID = lock.AtIntOr0(f, 0)
		cl.ARFCN = lock.AtIntOr0(f, 1)
		cl.RawValue = strings.Join(f, ",")
		if cl.PCID > 0 || cl.ARFCN > 0 {
			cl.LockType = lock.CellLockARFCNPCI
			cl.Locked = true
		}
	}
	return cl, true
}

// ParseModePref 解析 `+CNMP: <n>` → 2=AUTO / 38=LTE / 71=NR5G
func (f *SimcomLockCommand) ParseModePref(resp []string) (string, bool) {
	return ParseCNMP(resp)
}

// parseCNMP 解析 `+CNMP: <n>` —— 实现已搬到 modemd/module/simcom（ParseCNMP）；
// kernel 保留同名别名，见 lock_alias.go。

// SetModePrefVendor 不处理
func (f *SimcomLockCommand) SetModePrefVendor(lock.LockATDoer, string) (bool, error) {
	return false, nil
}

// CommandUnlockBand 解除频段限制：`AT+CSYSSEL`（手册：恢复全部频段为默认能力）。
// 注意它是**全制式**恢复，不是只解一个制式 —— 界面上有说明。
func (f *SimcomLockCommand) CommandUnlockBand(lock.CellTech) string { return "AT+CSYSSEL" }

// CommandSaveCellLock 该固件锁小区是否掉电保存未知 → 不额外下发（persist 时沿用默认）
func (f *SimcomLockCommand) CommandSaveCellLock() string { return "" }

// QueryBandCapability 无设备能力频段查询
func (f *SimcomLockCommand) QueryBandCapability(lock.LockATDoer) map[lock.CellTech][]int { return nil }

// BandCapabilityNote 「设备能力」拿不到时的说明
func (f *SimcomLockCommand) BandCapabilityNote(lock.CellTech) string {
	return "该固件（芯讯通）不上报设备能力频段"
}

// StatusNote 锁网页下方的机型提示
func (f *SimcomLockCommand) StatusNote() string {
	return "芯讯通 SIM8xxx：锁频段用 AT+CSYSSEL（lte_band / nr5g_band，冒号分隔）；" +
		"锁小区 LTE 用 AT+CCELLCFG、NR 用 AT+C5GCELLCFG（手册标注 NR 这条未充分验证）；" +
		"解除频段限制用 AT+CSYSSEL（恢复全部频段，非单制式）。"
}

// ====================== 芯讯通 SIMCom 旧款 4G：AT+CNBP（十六进制位图） ======================
//
// 芯讯通**一家两套**锁频段方言（依据 QModem `vendor/simcom.sh` 的平台分派）：
//
//	platform=qualcomm / 其它 → `set_lockband_nr()`：AT+CSYSSEL="<key>",<a:b:c>（本仓库 lock_simcom.go）
//	platform=lte             → `set_lockband_lte()`：AT+CNBP=<mode>,<lte16>,<lteExt4>[,<saveMode>]
//
// 这条 **AT+CNBP** 才是 SIM7600 / SIM7100 这类老芯讯通 4G 机型的写法：
//
//	查询  AT+CNBP?  → `+CNBP:<mode>[,<lte_mode>][,<lte_modeExt>][,<saveMode>]`
//	写入  AT+CNBP=<mode>,<lte_mode>,<lte_modeExt>,<saveMode>
//
// 三段的含义（QModem 的 `convert2hex_lte` / `convert2hex_lte_ext` / `normalize_hex_width`）：
//
//	<mode>          GSM/WCDMA 的位图，**16 位十六进制**（脚本里回读后原样写回）
//	<lte_mode>      LTE 频段位图，**16 位十六进制**，band N → bit(N-1)（N = 1..32）
//	<lte_modeExt>   LTE 扩展位图，**4 位十六进制**，ext_band = N-32 → bit(ext_band-1)（N = 33..48）
//	<saveMode>      QModem 写死 0（不写 NV）；本实现**回读到的原值优先**，读不到才用 0
//
// ⚠️ 两处与 QModem 的差异（都是往"更不容易把模组锁死"的方向）：
//
//	① 回读后**只替换 LTE 两段**，`<mode>`（GSM/WCDMA）与 `<saveMode>` 用设备自报的原值 ——
//	   QModem 会把 <mode> 原样保留（这点一致），但 saveMode 写死 0，等于锁完不落 NV。
//	② QModem **没有**解锁分支（`lock_band` 为空时 `convert2hex "…"` 会得到 0x0，
//	   真发下去就是"一个频段都不许用"）。本实现的解锁 = LTE 两段写全 1（不限制）。
//
// 本方言**没有锁小区指令**（CCELLCFG/C5GCELLCFG 是 qualcomm 平台那套），也不提供能力查询：
// `AT+CNBP=?` 不枚举频段，界面按"未上报"显示。

type CnbpLocker struct{}

// CNBP 位图宽度（与 QModem `normalize_hex_width` 的调用一致：LTE 段 16 位、扩展段 4 位）
const (
	cnbpLTEWidth = 16
	cnbpExtWidth = 4
)

// cnbpHexFields 取 `+CNBP:` 那一行的逗号字段（≥2 段才算有效应答）
func cnbpHexFields(resp []string) ([]string, bool) {
	for _, line := range resp {
		i := strings.Index(strings.ToUpper(line), "+CNBP:")
		if i < 0 {
			continue
		}
		f := common.SplitCSVFields(line[i+len("+CNBP:"):])
		if len(f) >= 2 {
			return f, true
		}
	}
	return nil, false
}

// cnbpPadHex 归一化成 QModem 的写法：`0x` + 大写、左补零到 width 位
func cnbpPadHex(s string, width int) string {
	h := strings.ToUpper(common.CleanHexField(s))
	if h == "" {
		h = "0"
	}
	if len(h) < width {
		h = strings.Repeat("0", width-len(h)) + h
	}
	return "0x" + h
}

// cnbpBitmask 位图里的低 width*4 位 → 频段号（band = bit + 1 + offset）
func cnbpBitmask(hexStr string, offset, bits int) []int {
	all := common.HexBitmapBands(hexStr, 0) // band = bit+1
	var out []int
	for _, b := range all {
		if b-1 < bits { // 只看低 bits 位
			out = append(out, b+offset)
		}
	}
	return out
}

// CnbpBandsFromResp 解析 `+CNBP:<mode>,<lte16>,<ext4>[,…]` → LTE 频段
func CnbpBandsFromResp(resp []string) ([]int, bool) {
	f, ok := cnbpHexFields(resp)
	if !ok {
		return nil, false
	}
	lte := cnbpBitmask(f[0+1], 0, 32) // 第 2 段：band 1..32
	var ext []int
	if len(f) >= 3 {
		ext = cnbpBitmask(f[2], 32, 16) // 第 3 段：band 33..48
	}
	bands := append(lte, ext...)
	// 全是 0 的情况：两个字段都解析不出频段 → 视为"没有解析出结果"（宁可空白）
	if len(bands) == 0 {
		return nil, false
	}
	return bands, true
}

// cnbpBandFields 频段表 → (lte 段, ext 段)，附越界检查
func cnbpBandFields(bands []int) (string, string, error) {
	var low, ext uint64
	used := 0
	for _, b := range bands {
		switch {
		case b >= 1 && b <= 32:
			low |= 1 << uint(b-1)
			used++
		case b >= 33 && b <= 48:
			ext |= 1 << uint(b-33)
			used++
		default:
			return "", "", fmt.Errorf("频段 %d 超出 AT+CNBP 能表达的范围（1-32 与 33-48）", b)
		}
	}
	if used == 0 {
		return "", "", fmt.Errorf("频段表 %v 为空或全部越界", bands)
	}
	return fmt.Sprintf("0x%016X", low), fmt.Sprintf("0x%04X", ext), nil
}

// ---------- NetLocker ----------

func (c *CnbpLocker) CommandQueryBandLock(tech lock.CellTech) string {
	if tech == lock.CellTechNR {
		return ""
	}
	return "AT+CNBP?"
}

func (c *CnbpLocker) CommandSetBandLock(lock.CellTech, []int) string { return "" } // 要回读保留 mode/saveMode

func (c *CnbpLocker) CommandQueryBandCapability(lock.CellTech) string { return "" }
func (c *CnbpLocker) CommandQueryPolicyBand() string                  { return "" }

// 本方言无锁小区指令（CNBP 只管频段）
func (c *CnbpLocker) CommandQueryCellLock(lock.CellTech) string   { return "" }
func (c *CnbpLocker) CommandLockCell(lock.CellLockRequest) string { return "" }

func (c *CnbpLocker) CommandBandLockCaps() string { return "" }
func (c *CnbpLocker) CommandCellLockCaps() string { return "" }

// 组网模式：芯讯通老款也是 AT+CNMP（取值 2=自动 / 38=LTE / 71=NR5G）
func (c *CnbpLocker) CommandQueryModePref() string { return "AT+CNMP?" }

func (c *CnbpLocker) CommandSetModePref(mode string) string {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "AUTO":
		return "AT+CNMP=2"
	case "LTE":
		return "AT+CNMP=38"
	}
	return ""
}

// ---------- LockVendorOps ----------

func (c *CnbpLocker) ParseBandLockState(tech lock.CellTech, resp []string) ([]int, bool) {
	if tech != lock.CellTechLTE {
		return nil, false
	}
	return CnbpBandsFromResp(resp)
}

func (c *CnbpLocker) ParseCellLockState(lock.CellTech, []string) (lock.CellLock, bool) {
	return lock.CellLock{}, false
}

func (c *CnbpLocker) ParseModePref(resp []string) (string, bool) { return ParseCNMP(resp) }

func (c *CnbpLocker) SetModePrefVendor(lock.LockATDoer, string) (bool, error) { return false, nil }

// CommandUnlockBand 空串：解锁要"回读保留 mode/saveMode + LTE 两段写全 1"，走 BandLockSetter
func (c *CnbpLocker) CommandUnlockBand(lock.CellTech) string { return "" }

func (c *CnbpLocker) CommandSaveCellLock() string { return "" }

func (c *CnbpLocker) QueryBandCapability(lock.LockATDoer) map[lock.CellTech][]int { return nil }

func (c *CnbpLocker) BandCapabilityNote(lock.CellTech) string {
	return "该固件（AT+CNBP）不上报能力频段；Modem-Pro 为 platform=lte 的芯讯通机型列出的静态可选表为 " +
		"1,2,3,4,5,7,8,12,13,14,17,18,19,20,25,26,28,29,30,32,34,38,39,40,41,42,43,48,66,71（仅供参考，非设备上报）"
}

func (c *CnbpLocker) StatusNote() string {
	return "芯讯通旧款 4G：锁频段走 AT+CNBP=<mode>,<LTE 16 位十六进制位图>,<扩展 4 位位图>,<saveMode>" +
		"（band N → bit(N-1)；33..48 走扩展段 ext=N-32 → bit(ext-1)）；" +
		"本实现先回读 AT+CNBP?，**只替换 LTE 两段**（mode 与 saveMode 用设备原值），" +
		"解除锁定 = LTE 两段写全 1。无锁小区指令，组网模式沿用 AT+CNMP。"
}

// ---------- BandLockSetter ----------

// cnbpCurrent 回读并取出 (mode, lte16, ext4, saveMode)
func (c *CnbpLocker) cnbpCurrent(at lock.LockATDoer) ([]string, bool) {
	resp, err := at.SendAT("AT+CNBP?")
	if err != nil || common.RespHasError(resp) {
		return nil, false
	}
	f, ok := cnbpHexFields(resp)
	if !ok {
		return nil, false
	}
	mode, lte, ext, save := f[0], f[1], "0", "0"
	if len(f) >= 3 {
		ext = f[2]
	}
	if len(f) >= 4 {
		save = f[3]
	}
	if !common.IsHexBitmap(common.CleanHexField(mode)) || !common.IsHexBitmap(common.CleanHexField(lte)) {
		return nil, false
	}
	return []string{mode, lte, ext, save}, true
}

// SetBandLockAt 锁定 LTE：回读 → 替换 LTE 两段 → 整串写回
func (c *CnbpLocker) SetBandLockAt(at lock.LockATDoer, tech lock.CellTech, bands []int) error {
	if tech != lock.CellTechLTE {
		return fmt.Errorf("AT+CNBP 只支持 LTE 频段（该指令没有 NR 段）")
	}
	cur, ok := c.cnbpCurrent(at)
	if !ok {
		return fmt.Errorf("AT+CNBP? 应答读不出 <mode>,<LTE 位图>（需要 `+CNBP:` 行），拒绝下发 —— " +
			"否则会把 GSM/WCDMA 位图一起写坏")
	}
	low, ext, err := cnbpBandFields(bands)
	if err != nil {
		return err
	}
	// mode（GSM/WCDMA）与 saveMode 原样保留
	return at.SendLockCommand(fmt.Sprintf("AT+CNBP=%s,%s,%s,%s",
		cnbpPadHex(cur[0], cnbpLTEWidth), low, ext, strings.TrimSpace(cur[3])))
}

// UnlockBandAt 解除：LTE 两段写全 1（不限制），mode/saveMode 保留
func (c *CnbpLocker) UnlockBandAt(at lock.LockATDoer, tech lock.CellTech) error {
	if tech != lock.CellTechLTE {
		return fmt.Errorf("AT+CNBP 只支持 LTE 频段（该指令没有 NR 段）")
	}
	cur, ok := c.cnbpCurrent(at)
	if !ok {
		return fmt.Errorf("AT+CNBP? 应答读不出 <mode>,<LTE 位图>，拒绝下发解锁指令")
	}
	return at.SendLockCommand(fmt.Sprintf("AT+CNBP=%s,%s,%s,%s",
		cnbpPadHex(cur[0], cnbpLTEWidth),
		"0x"+strings.Repeat("F", cnbpLTEWidth),
		"0x"+strings.Repeat("F", cnbpExtWidth),
		strings.TrimSpace(cur[3])))
}

// CnbpCommand 方言实例
func CnbpCommand() *CnbpLocker { return &CnbpLocker{} }
