package gosuncn

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"modemd/iface"
	"modemd/module/common"
	"modemd/module/lock"
)

// ====================== 高新兴 Gosuncn 锁网（AT+ZBAND / AT+ZLOCKCELL） ======================
//
// 指令与格式依据 QModem 的 `cmds/gosuncn.sh` + `vendor/gosuncn.sh`（真机脚本）：
//
//	锁频段：AT+ZBAND=<rat>,<num>,<b1,b2,…>   （rat 0=解锁 1=LTE 5=NR5G；num=频段个数）
//	        查询 AT+ZBAND?  复位 AT+ZBAND=0
//	锁小区：AT+ZLOCKCELL=1,1,<earfcn>,<pci>            （LTE）
//	        AT+ZLOCKCELL=1,2,<narfcn>,<pci>,<scs>,<band>（NR）
//	        查询 AT+ZLOCKCELL?  解锁 AT+ZLOCKCELL=0
//
// **两种形态**（同一厂商、不同平台/固件 —— 依据 QModem `vendor/gosuncn.sh` 的平台分派）：
//
//	A) qualcomm 机型（GM800 这类）：`get_lockband()`/`set_lockband()` 里 `case $platform in qualcomm)`
//	   → 写 `AT+ZBAND=<rat>,<num>,<list>`（rat 0=Unlock 1=LTE 2=TDSCDMA 3=WCDMA 4=GSM 5=NR5G；num 限 1-10），
//	     复位 `AT+ZBAND=0`；读用 “含 WCDMA/LTE/NR5G 的行 + 冒号后内容” 分隔。
//	B) lte 平台机型（ME3630-W 这类；`*)` 也走它）：
//	     写 `cmd_zband_set_lte() = AT+ZBAND=all,all,all,<十六进制位图>`（`convert2hex` 算：band N → bit(N-1)），
//	     复位 `cmd_zband_reset_all_lte() = AT+ZBAND=all,all,all,all`。
//
// ⭐ 两种形态用**同一条查询指令** `AT+ZBAND?`，而且 B 的应答第一个字段就是字面 `all`
//（QModem 自己写的复兴值就是 `all,all,all,all`），所以本实现在**下发前回读一次**、
// 看到 `all,all,all,<位图>` 就当 B 处理（只替换第 4 段的位图，其余原样保留），
// 否则当 A 处理（即原来的单条写法）—— 这样一台 ME3630-W 不会被发成 A 的写法（固件会回 ERROR），
// 也不会因为回读失败而旧功能回退。
//
// ⚠️ A 的**当前值**仍然不解析（QModem 的 `cut -d':' -f2` 在两个可能的应答形态下会取到 `LTE` 字样
// 本身，说明它自己也不严格）—— 宁可留空显示原始应答，也不臆造频段列表。
//
// ⚠️ 高新兴机型（GM800/ME3630）在本仓库的平台表里不是独立平台，
// 运行期会**先探测通用 QNW**；只有在 QNW/其它方言都不被接受时才回退到这一套（见 lockerFor）。
//
// 命令层原文（QModem `cmds/gosuncn.sh`，2026-10 核对）：
//
//	cmd_zband_set_qualcomm()      { at "$1" "AT+ZBAND=$2,$3,$4"; }
//	  # $2=RAT（0=Unlock / 1=LTE / 2=TDSCDMA / 3=WCDMA / 4=GSM / 5=NR5G） $3=频段个数(1-10) $4=逗号频段表
//	cmd_zband_reset_all_qualcomm() { at "$1" "AT+ZBAND=0"; }
//	cmd_zband_query()             { at "$1" 'AT+ZBAND?'; }
//	cmd_zband_list_query()        { at "$1" 'AT+ZBAND=?'; }
//
// ⚠️ 另有一条 **LTE 专用**写法 `cmd_zband_set_lte() { at "$1" "AT+ZBAND=all,all,all,$2"; }`
// （$2 是**十六进制位图**，`convert2hex` 按 band N → bit(N-1) 算），复位 `AT+ZBAND=all,all,all,all`
// —— 见于 **platform=lte** 的机型（ME3630-W 这类）。本仓库**在回读判定后自动走它**，见下方 SetBandLockAt。

type GosuncnLockCommand struct{}

// ---------- NetLocker ----------

func (f *GosuncnLockCommand) CommandQueryBandLock(lock.CellTech) string { return "AT+ZBAND?" }

// CommandSetBandLock LTE=rat1 / NR=rat5，格式 `AT+ZBAND=<rat>,<num>,<b1,b2,…>`
func (f *GosuncnLockCommand) CommandSetBandLock(tech lock.CellTech, bands []int) string {
	if len(bands) == 0 || tech == lock.CellTechNRNSA {
		return "" // 一套 NR 列表（见 NSAIndependent）
	}
	rat := 1
	if tech == lock.CellTechNR {
		rat = 5
	}
	parts := make([]string, 0, len(bands))
	for _, b := range bands {
		parts = append(parts, strconv.Itoa(b))
	}
	return fmt.Sprintf("AT+ZBAND=%d,%d,%s", rat, len(bands), strings.Join(parts, ","))
}

func (f *GosuncnLockCommand) CommandQueryBandCapability(lock.CellTech) string { return "" }
func (f *GosuncnLockCommand) CommandQueryPolicyBand() string                  { return "" }

// CommandQueryCellLock LTE/NR 共用 AT+ZLOCKCELL?
func (f *GosuncnLockCommand) CommandQueryCellLock(lock.CellTech) string { return "AT+ZLOCKCELL?" }

// CommandLockCell LTE/NR 的锁定都**必须带 PCI**（只锁频点不支持）
func (f *GosuncnLockCommand) CommandLockCell(req lock.CellLockRequest) string {
	switch req.LockType {
	case lock.CellLockNone:
		return "AT+ZLOCKCELL=0"
	case lock.CellLockARFCN:
		return "" // 该指令的两种写法都要求 pci
	}
	if req.Tech == lock.CellTechNR {
		return fmt.Sprintf("AT+ZLOCKCELL=1,2,%d,%d,%d,%d", req.ARFCN, req.PCID, iface.NormalizeSCS(req.SCS), req.Band())
	}
	return fmt.Sprintf("AT+ZLOCKCELL=1,1,%d,%d", req.ARFCN, req.PCID)
}

func (f *GosuncnLockCommand) CommandBandLockCaps() string { return "AT+ZBAND=?" }
func (f *GosuncnLockCommand) CommandCellLockCaps() string { return "AT+ZLOCKCELL?" }

// CommandQueryModePref / SetModePref 高新兴用 AT+ZSNT 选网，取值未确认 → 不猜
func (f *GosuncnLockCommand) CommandQueryModePref() string     { return "" }
func (f *GosuncnLockCommand) CommandSetModePref(string) string { return "" }

// ---------- LockVendorOps ----------

// ParseBandLockState 只解析 **LTE 位图形态**（`+ZBAND: all,all,all,<hex>`）：
// 位图 → 频段（band N → bit(N-1)），语义与写入（convert2hex）自洽；
// 其余形态（qualcomm 的 `<rat>,<num>,<list>`）**不臆造**，返回 ok=false 让界面显示原始应答。
func (f *GosuncnLockCommand) ParseBandLockState(tech lock.CellTech, resp []string) ([]int, bool) {
	if tech != lock.CellTechLTE {
		return nil, false
	}
	fs, ok := GosuncnZBANDLTEHexFields(resp)
	if !ok {
		return nil, false
	}
	// 位图非法 → 空列表（上游据此显示原始应答）
	if bands := common.HexBitmapBands(fs[3], 0); len(bands) > 0 {
		return bands, true // locked 由上游按能力表补判（与 qcfgband/openluat 同一口径）
	}
	return nil, false
}

// ParseCellLockState 尽力解析：找含 ZLOCKCELL 且含本技术域字样（LTE / NR5G）的行，
// 取冒号后内容里的数字（约定 <arfcn>,<pci>…）；含 unlock 视为未锁。
func (f *GosuncnLockCommand) ParseCellLockState(tech lock.CellTech, resp []string) (lock.CellLock, bool) {
	cl := lock.CellLock{Tech: tech}
	want := "LTE"
	if tech == lock.CellTechNR {
		want = "NR5G"
	}
	for _, line := range resp {
		u := strings.ToUpper(line)
		if !strings.Contains(u, "ZLOCKCELL") {
			continue
		}
		cl.RawValue = strings.TrimSpace(line)
		seg := u
		if i := strings.LastIndex(seg, ":"); i >= 0 {
			seg = seg[i+1:]
		}
		if !strings.Contains(seg, want) || strings.Contains(seg, "UNLOCK") {
			continue
		}
		// 切掉制式词本身再取数字 —— 否则 "NR5G" 里的 5 会被当成频点
		if i := strings.Index(seg, want); i >= 0 {
			seg = seg[i+len(want):]
		}
		nums := AtNumbers(seg)
		if len(nums) == 0 {
			continue
		}
		cl.Locked = true
		cl.ARFCN = nums[0]
		if len(nums) >= 2 {
			cl.PCID = nums[1]
			cl.LockType = lock.CellLockARFCNPCI
		} else {
			cl.LockType = lock.CellLockARFCN
		}
	}
	return cl, true
}

func (f *GosuncnLockCommand) ParseModePref([]string) (string, bool) { return "", false }
func (f *GosuncnLockCommand) SetModePrefVendor(lock.LockATDoer, string) (bool, error) {
	return false, nil
}

// CommandUnlockBand 空串：解锁要分形态（`AT+ZBAND=0` 还是 `AT+ZBAND=all,all,all,all`），
// 由 BandLockSetter.UnlockBandAt 回读后决定。
func (f *GosuncnLockCommand) CommandUnlockBand(lock.CellTech) string { return "" }

func (f *GosuncnLockCommand) CommandSaveCellLock() string { return "" }

func (f *GosuncnLockCommand) QueryBandCapability(lock.LockATDoer) map[lock.CellTech][]int { return nil }

func (f *GosuncnLockCommand) BandCapabilityNote(lock.CellTech) string {
	return "该固件（高新兴）不上报设备能力频段"
}

func (f *GosuncnLockCommand) StatusNote() string {
	return "高新兴 Gosuncn（两种形态）：qualcomm 机型用 AT+ZBAND=<rat>,<个数>,<频段表>" +
		"（1=LTE、5=NR5G、复位 =0）；lte 平台机型用 AT+ZBAND=all,all,all,<十六进制位图>" +
		"（复位 =all,all,all,all）—— 下发前会回读 AT+ZBAND? 自动判定。" +
		"锁小区两种形态都用 AT+ZLOCKCELL（LTE =1,1,频点,PCI；NR =1,2,频点,PCI,SCS,band；解锁 =0）。"
}

// ---------- BandLockSetter（回读判定形态） ----------

// GosuncnZBANDLTEHexFields 从 `AT+ZBAND?` 应答里找出 **LTE 位图形态** 那一行的字段。
//
// 判定条件（宁缺勿猜）：逗号分段 ≥ 4 且**前三段都是字面 all**、第 4 段是十六进制位图或 all。
// 依据：QModem 的复位写法就是 `AT+ZBAND=all,all,all,all`，写入只换第 4 段。
func GosuncnZBANDLTEHexFields(resp []string) ([]string, bool) {
	for _, line := range resp {
		if !strings.Contains(strings.ToUpper(line), "ZBAND") {
			continue
		}
		seg := line
		if i := strings.LastIndex(seg, ":"); i >= 0 {
			seg = seg[i+1:]
		}
		fs := common.SplitCSVFields(seg)
		if len(fs) < 4 {
			continue
		}
		if !strings.EqualFold(fs[0], "all") || !strings.EqualFold(fs[1], "all") || !strings.EqualFold(fs[2], "all") {
			continue
		}
		mask := common.CleanHexField(fs[3])
		if !strings.EqualFold(mask, "all") && !common.IsHexBitmap(mask) {
			continue
		}
		return fs, true
	}
	return nil, false
}

// gosuncnZBANDCurrent 回读并判定形态：lteHex=true 时 fields 是完整字段（≥4 段）。
func (f *GosuncnLockCommand) gosuncnZBANDCurrent(at lock.LockATDoer) (fields []string, lteHex bool, readOK bool) {
	resp, err := at.SendAT(f.CommandQueryBandLock(lock.CellTechLTE))
	if err != nil || common.RespHasError(resp) {
		return nil, false, false
	}
	if fs, ok := GosuncnZBANDLTEHexFields(resp); ok {
		return fs, true, true
	}
	return nil, false, true // 能读、但不是位图形态 → 按 qualcomm 写法（原有行为）
}

// SetBandLockAt 锁定频段：先回读判定形态
//
//	lte 位图形态 → `AT+ZBAND=all,all,all,<位图>`（只替第 4 段，band N → bit(N-1)）；
//	其余（含回读失败）→ qualcomm 的 `AT+ZBAND=<rat>,<个数>,<频段表>`（原有行为，不退化）。
func (f *GosuncnLockCommand) SetBandLockAt(at lock.LockATDoer, tech lock.CellTech, bands []int) error {
	if len(bands) == 0 {
		return fmt.Errorf("频段表为空（解除锁定请走解锁接口）")
	}
	fields, lteHex, _ := f.gosuncnZBANDCurrent(at)
	if lteHex {
		if tech == lock.CellTechNR {
			return fmt.Errorf("本机 AT+ZBAND 只上报一段 LTE 位图（all,all,all,<位图>），无法只锁 NR 频段")
		}
		mask, err := GosuncnBandMaskHex(bands)
		if err != nil {
			return err
		}
		// 只替换第 4 段位图，前三段用设备自报的原值（通常是 all）
		return at.SendLockCommand(fmt.Sprintf("AT+ZBAND=%s,%s,%s,%s", fields[0], fields[1], fields[2], mask))
	}
	cmd := f.CommandSetBandLock(tech, bands)
	if cmd == "" {
		return fmt.Errorf("频段表 %v 无法拼成 AT+ZBAND 指令", bands)
	}
	return at.SendLockCommand(cmd)
}

// UnlockBandAt 解除锁定：lte 位图形态写 `AT+ZBAND=all,all,all,all`，其余写 `AT+ZBAND=0`。
// ⚠️ 两种写法都是**全制式复位**（与原有 `AT+ZBAND=0` 一致），会把另一个制式的锁定一起清掉。
func (f *GosuncnLockCommand) UnlockBandAt(at lock.LockATDoer, tech lock.CellTech) error {
	if _, lteHex, _ := f.gosuncnZBANDCurrent(at); lteHex {
		return at.SendLockCommand("AT+ZBAND=all,all,all,all")
	}
	return at.SendLockCommand("AT+ZBAND=0")
}

// GosuncnBandMaskHex 频段表 → 十六进制位图（band N → bit(N-1)），与 QModem `convert2hex` 同口径
// （bc 输出大写、不补零；这里用 big.Int 同理，只在全为 0 时回 "0"）。
func GosuncnBandMaskHex(bands []int) (string, error) {
	v := new(big.Int)
	for _, b := range bands {
		if b < 1 || b > 128 {
			return "", fmt.Errorf("频段 %d 超出范围（1-128）", b)
		}
		v.SetBit(v, b-1, 1)
	}
	if v.Sign() == 0 {
		return "", fmt.Errorf("频段表 %v 里没有可用频段", bands)
	}
	return strings.ToUpper(v.Text(16)), nil
}

// AtNumbers 取字符串里的所有整数（用于"尽力解析"未逐字确认的应答；实现在 module/common）
var AtNumbers = common.AtNumbers
