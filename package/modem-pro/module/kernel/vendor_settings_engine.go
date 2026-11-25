package kernel

import (
	"fmt"
	"modemd/module/catalog"
	"modemd/module/imeidialect"
	"modemd/module/tdtech"
	"modemd/module/usbdialect"
	"sort"
	"strings"

	"go.uber.org/zap"
)

// ====================== 厂商设置方言的引擎桥（USB 网卡模式 / 组网模式偏好） ======================
//
// 「USB 网卡模式」的**显示与下发**以模块清单（`modem_catalog.json` 的 `usb_at`）为准 ——
// 数据与支持清单同出一门（QModem 的 `cmds_*.sh` / `vendor_*.sh`，取证工具 files/tools/catalogmodeat）：
//
//	· 取值表   = `usb_at.modes` 的档位（qmi/ecm/mbim/rndis/ncm/rmnet/auto）× 本型号 `usb` 列；
//	· 下发指令 = `usb_at.modes[档位]` **原样照抄**，不再由厂商方言表拼字符串；
//	· 清单登记了 `usb_at` 但只有说明（参数未取证：Quectel RG801、Huawei S10、SIMCom 展锐档、
//	  Neoway N510M…）→ 只读：列表为空 + 说明原因，写入直接拒绝 —— **不猜参数**。
//
// 厂商方言表（`vendor_settings_usb.go`）退到两处：
//
//	① 型号**不在清单里**时的兜底（老行为不变）；
//	② 应答解析（各家回读格式不同，清单里没有这部分信息）与写入前的进门指令（pre/preSet）。

// USBNetDialectOf 本机的 USB 网卡模式能力（ok=false = 本机没有这个概念）
func (mod *Modem) USBNetDialectOf() (query string, modes []Mode, note string, ok bool) {
	if e, known := catalogUSBATFor(mod); known {
		return strings.TrimSpace(e.USBModeAT.Query), catalogUSBModesFromAT(e, e.USB), usbnetNote(mod), true
	}
	d := usbDialectFor(mod)
	if d == nil {
		return "", nil, "", false
	}
	return d.Query, usbnetModes(mod), usbnetNote(mod), true
}

// ---------- 清单口径：`modem_catalog.json` 的 `usb_at` ----------

// catalogUSBATFor 本机的清单条目（**且**该型号登记了 usb_at）；
// 型号不在清单 / 清单里没有 usb_at → false（调用方退回厂商方言表）。
func catalogUSBATFor(mod *Modem) (catalog.ModemCatalogEntry, bool) {
	if mod == nil {
		return catalog.ModemCatalogEntry{}, false
	}
	e, ok := catalog.CatalogEntryForModel(modCatalogKeys(mod))
	if !ok || e.USBModeAT == nil {
		return catalog.ModemCatalogEntry{}, false
	}
	return e, true
}

// catalogKindName 清单口径 → 展示名（认不出就原样大写）
func catalogKindName(kind string) string {
	if s, ok := catalog.CatalogKindLabel[kind]; ok {
		return s
	}
	return strings.ToUpper(kind)
}

// catalogKindNote 清单口径的通用说明（界面在下拉里显示；厂商特有的说法在方言表里）
var catalogKindNote = map[string]string{
	catalog.CatModeQMI:     "高通原生协议（需 qmi_wwan 等驱动 / 主机侧拨号器）",
	catalog.CatModeGobiNet: "高通 GobiNet 驱动",
	catalog.CatModeECM:     "USB 以太网（Linux 免驱常用）",
	catalog.CatModeMBIM:    "微软标准（Windows 推荐）",
	catalog.CatModeRNDIS:   "经典 USB 网卡（Windows 免驱）",
	catalog.CatModeNCM:     "高速 USB 网络（Linux 常用，带宽更高）",
	catalog.CatModeRMNet:   "高通 RMNet",
	catalog.CatModeAuto:    "固件自动选择",
	catalog.CatModeEAP:     "EAP 模式",
	catalog.CatModePPP:     "PPP 模式",
}

// catalogUSBModeList 清单里的全部档位（按键名排序 → 固定顺序；取值要**一组一起**算，
// 见 usbnetATParamsOf）。取值重复的（如广和通高通平台的 ECM/NCM 都是 18）只留先出现的那个。
type catalogUSBMode struct {
	Kind  string // 清单口径（qmi/ecm/…）
	Value string // 设备上的取值（从指令里取出来的那一段）
	Cmd   string // 完整切换指令（原样照抄，可直接下发）
}

func catalogUSBModeList(e catalog.ModemCatalogEntry) []catalogUSBMode {
	if e.USBModeAT == nil || len(e.USBModeAT.Modes) == 0 {
		return nil
	}
	all := make([]string, 0, len(e.USBModeAT.Modes))
	for k := range e.USBModeAT.Modes {
		all = append(all, k)
	}
	sort.Strings(all)
	cmds := make([]string, len(all))
	for i, k := range all {
		cmds[i] = e.USBModeAT.Modes[k]
	}
	params := usbnetATParamsOf(cmds)
	out := make([]catalogUSBMode, 0, len(all))
	for i, k := range all {
		if params[i] == "" {
			continue
		}
		dup := false
		for _, m := range out {
			if strings.EqualFold(m.Value, params[i]) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		out = append(out, catalogUSBMode{Kind: k, Value: params[i], Cmd: cmds[i]})
	}
	return out
}

// catalogUSBModesFromAT 由清单的 `usb_at.modes` 生成界面取值表。
//
// kinds = nil：不过型号过滤（**回显当前值**用 —— 设备可能停在清单外的档位上）；
// kinds 非 nil：只留这些档位，**顺序照 kinds 走**（= 支持列表的 usb 列顺序，与清单一致）。
func catalogUSBModesFromAT(e catalog.ModemCatalogEntry, kinds []string) []Mode {
	list := catalogUSBModeList(e)
	if len(list) == 0 {
		return nil
	}
	out := make([]Mode, 0, len(list))
	if kinds == nil {
		for _, m := range list {
			out = append(out, catalogModeOf(m))
		}
		return out
	}
	byKind := make(map[string]catalogUSBMode, len(list))
	for _, m := range list {
		byKind[m.Kind] = m
	}
	for _, k := range kinds { // 顺序照 kinds 走（= 支持列表 usb 列的顺序）
		m, ok := byKind[strings.ToLower(strings.TrimSpace(k))]
		if !ok {
			continue // 清单里没有这一档的切换指令（QModem 未取证 / 脚本里被注释）→ 不列
		}
		out = append(out, catalogModeOf(m))
	}
	return out
}

// catalogModeOf 清单档位 → 界面取值（value 就是设备上的取值）
func catalogModeOf(m catalogUSBMode) Mode {
	return Mode{Value: m.Value, Name: catalogKindName(m.Kind), Note: catalogKindNote[m.Kind]}
}

// catalogUSBModeCmds 取值 → 该档的完整切换指令（清单里的全部档位，含本型号不支持的；
// 界面拿它显示"这一档会发什么"，也方便现场核对）。
func catalogUSBModeCmds(e catalog.ModemCatalogEntry) map[string]string {
	list := catalogUSBModeList(e)
	if len(list) == 0 {
		return nil
	}
	out := make(map[string]string, len(list))
	for _, m := range list {
		out[m.Value] = m.Cmd
	}
	return out
}

// usbnetModeCmds 本机各取值的完整切换指令（清单口径；型号不在清单里 → nil）
func usbnetModeCmds(mod *Modem) map[string]string {
	if e, ok := catalogUSBATFor(mod); ok {
		return catalogUSBModeCmds(e)
	}
	return nil
}

// usbnetATParamsOf 取一组（同一型号的）切换指令里"唯一变化的那一段"作为各档取值。
//
// 指令形状只有十几种，按逗号切字段后再看哪一段在变，比正则/后缀剥离稳
// （取值之间会共享首尾字符：9025/90d5、9001/9011）：
//
//	AT+QCFG="usbnet",2            → 2
//	AT^SETMODE=4                  → 4
//	AT+SER=2,1                    → 2            （尾巴 ",1" 固定）
//	AT$MYCONFIG="USBNETMODE",1,1  → 1            （前缀与尾巴都固定）
//	AT!USBCOMP=1,4,0x00001009     → 0x00001009
//	AT+CUSBCFG=usbid,1e0e,9011    → 9011
func usbnetATParamsOf(cmds []string) []string {
	fields := make([][]string, len(cmds))
	for i, c := range cmds {
		fields[i] = strings.Split(c, ",")
	}
	vary := -1
	if len(fields) > 1 {
		n := len(fields[0])
		same := true
		for _, f := range fields {
			if len(f) != n {
				same = false
				break
			}
		}
		if same {
			for i := 0; i < n; i++ {
				for _, f := range fields[1:] {
					if f[i] != fields[0][i] {
						vary = i
						break
					}
				}
				if vary >= 0 {
					break
				}
			}
		}
	}
	out := make([]string, len(fields))
	for i, f := range fields {
		part := f[len(f)-1]
		if vary >= 0 && vary < len(f) {
			part = f[vary]
		}
		out[i] = usbnetATValue(part)
	}
	return out
}

// usbnetATValue 字段 → 取值：去掉最后一个 `=` 之前的前缀，再去掉引号
func usbnetATValue(field string) string {
	s := strings.TrimSpace(field)
	if i := strings.LastIndex(s, "="); i >= 0 {
		s = s[i+1:]
	}
	return strings.Trim(strings.TrimSpace(s), `"`)
}

// catalogUSBSetCommand 取值 → 清单里的完整切换指令（**原样返回**）。
//
// value 可以是三种写法，按"清单口径 → 界面模式名 → 本机取值"依次认：
// "mbim"（档位） / "MBIM"（模式名） / "30"（设备上的取值）。
func catalogUSBSetCommand(e catalog.ModemCatalogEntry, value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return ""
	}
	if cmd := e.USBModeAT.ATForMode(v); cmd != "" {
		return cmd
	}
	if k := catalog.CatalogKindOfModeName(v); k != "" {
		if cmd := e.USBModeAT.ATForMode(k); cmd != "" {
			return cmd
		}
	}
	for _, m := range catalogUSBModeList(e) {
		if strings.EqualFold(m.Value, v) {
			return m.Cmd
		}
	}
	return ""
}

// usbnetCatalogWhy 清单为什么没有可下发的指令（说明文字，报错与界面共用）
func usbnetCatalogWhy(e catalog.ModemCatalogEntry) string {
	if n := strings.TrimSpace(e.USBModeAT.Note); n != "" {
		return n
	}
	return "Modem-Pro 没有提供该型号的 USB 拨号模式切换指令"
}

// usbnetCatalogText 清单原文摘要（界面/报错用）：本型号支持哪些档位、哪些档位没有切换指令
func usbnetCatalogText(e catalog.ModemCatalogEntry) string {
	s := fmt.Sprintf("清单：USB 支持 %s（平台 %s）", catalog.CatalogKindsText(e.USB), e.Platform)
	if m := e.USBModeAT; m != nil && len(m.Modes) > 0 {
		var missing []string
		for _, k := range e.USB {
			if m.ATForMode(k) == "" {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			s += "；其中 " + strings.Join(missing, "、") + " 档没有切换指令（Modem-Pro 未取证）⇒ 不收进取值表"
		}
	}
	return s + "。"
}

// usbnetQueryCommand 查询指令：清单 `usb_at.query` 优先，其次厂商方言表，最后厂商 Command 声明（老路径）
func (mod *Modem) usbnetQueryCommand() (string, error) {
	if e, ok := catalogUSBATFor(mod); ok {
		if q := strings.TrimSpace(e.USBModeAT.Query); q != "" {
			return q, nil
		}
		return "", fmt.Errorf("本型号在模块清单里没有 USB 网卡模式：%s", usbnetCatalogWhy(e))
	}
	if d := usbDialectFor(mod); d != nil {
		return d.Query, nil
	}
	c, ok := mod.cmdImpl.(USBnetConfigurator)
	if !ok {
		return "", fmt.Errorf("vendor %s 未提供 USB 网卡模式指令", mod.Vendor)
	}
	// 空串 = 厂商明确声明“本机没这个概念”（如展锐 UMS9620：AT+QCFG 回 +CME ERROR: 4）。
	// 这里直接报“不支持”，不要返回空串让上层白发一条空指令。
	cmd := c.CommandQueryUSBnet()
	if strings.TrimSpace(cmd) == "" {
		return "", fmt.Errorf("本机（%s）没有 USB 网卡模式：5G 基带在 SoC 内部，USB 侧只有给局域网用的 gadget", mod.Vendor)
	}
	return cmd, nil
}

// usbnetSetCommand 设置指令：清单 `usb_at.modes[档位]` **原样照抄**，其次厂商方言表
func (mod *Modem) usbnetSetCommand(value string) (string, error) {
	if e, ok := catalogUSBATFor(mod); ok {
		if cmd := catalogUSBSetCommand(e, value); cmd != "" {
			return cmd, nil
		}
		if !e.USBModeAT.Known() {
			return "", fmt.Errorf("本型号在模块清单里没有可下发的 USB 切换指令（%s）", usbnetCatalogWhy(e))
		}
		return "", fmt.Errorf("本机不支持 USB 网卡模式 %q：%s", value, usbnetCatalogText(e))
	}
	if d := usbDialectFor(mod); d != nil {
		cmd := d.Set(value)
		if strings.TrimSpace(cmd) == "" {
			return "", fmt.Errorf("取值 %q 在本机方言下拼不出指令", value)
		}
		return cmd, nil
	}
	c, ok := mod.cmdImpl.(USBnetConfigurator)
	if !ok {
		return "", fmt.Errorf("vendor %s 未提供 USB 网卡模式指令", mod.Vendor)
	}
	cmd := c.CommandSetUSBnet(value)
	if strings.TrimSpace(cmd) == "" {
		return "", fmt.Errorf("本机（%s）没有 USB 网卡模式，不能设置", mod.Vendor)
	}
	return cmd, nil
}

// usbnetParse 解析当前值：厂商方言表优先 —— 回读格式各家不同，而清单里没有这部分信息；
// 型号在清单里但没有方言（新增厂商）时，按"设备自己报的原码"读，不假装知道含义。
func usbnetParse(mod *Modem, resp []string) (string, bool) {
	if mod.isMT5700MCN() {
		return tdtech.ParseUSBMode(resp)
	}
	if d := usbDialectFor(mod); d != nil {
		return d.Parse(resp)
	}
	if v, ok := usbdialect.ParseUSBnet(resp); ok {
		return v, true
	}
	if _, ok := catalogUSBATFor(mod); ok {
		return imeidialect.RawValueParser(resp)
	}
	return "", false
}

// usbnetModes 本机可选取值（界面下拉就是它）：
//
//	· 型号在清单里 → `usb_at.modes` × 本型号 `usb` 列（**清单口径**：取值与指令都来自清单）；
//	  清单只有说明（参数未取证）时返回空表 —— 宁可不列，也不退回厂商表猜参数。
//	· 型号不在清单里 → 厂商方言表 × 支持清单过滤（旧行为）。
func usbnetModes(mod *Modem) []Mode {
	if e, ok := catalogUSBATFor(mod); ok {
		return catalogUSBModesFromAT(e, e.USB)
	}
	modes, _ := catalog.CatalogUSBModesFor(modCatalogKeys(mod), usbnetModesUnfiltered(mod))
	return modes
}

// usbnetModesUnfiltered 未过型号清单的取值表（清单口径优先，其次厂商方言表，最后通用表）。
//
// **回显当前值必须用它**：设备可能停在清单外的档位上（用户手动改过固件配置），
// 拿过滤过的表去查名字会认不出来。
func usbnetModesUnfiltered(mod *Modem) []Mode {
	if e, ok := catalogUSBATFor(mod); ok {
		return catalogUSBModesFromAT(e, nil)
	}
	if d := usbDialectFor(mod); d != nil && len(d.Modes) > 0 {
		return d.Modes
	}
	return usbdialect.Modes()
}

// usbnetModeName 取值 → 展示名：清单/方言的取值表都查一遍（清单口径优先）。
func usbnetModeName(mod *Modem, value string) string {
	if mod.isMT5700MCN() {
		return tdtech.USBModeName(value)
	}
	if n := modeNameIn(usbnetModes(mod), value); n != "" {
		return n
	}
	return modeNameIn(usbnetModesUnfiltered(mod), value)
}

// modCatalogKeys 模块支持清单的匹配串：ATI 原文 + 短型号名（两个都可能带型号信息）
func modCatalogKeys(mod *Modem) string {
	if mod == nil {
		return ""
	}
	return mod.Model + " " + mod.ModelName
}

// usbnetNoUSBForm 支持清单是否**明确**写了"本型号没有 USB 形态"（只有 PCIe），
// 如 Foxconn T99W640（QModem 支持列表的 USB 列就是 ✘）。
// 型号不在清单里 → false（不知道，不拦）。
func usbnetNoUSBForm(mod *Modem) bool {
	kinds, known := catalog.CatalogUSBKindsForModel(modCatalogKeys(mod))
	return known && len(kinds) == 0
}

// usbnetPrepare 发主指令前的“进门”指令（best-effort，见 usbDialect.pre）。
//
// 目前只有 Sierra 用到：`AT!ENTERCND="A710"`（QModem cmds/sierra.sh 的 cmd_entercnd）。
// 失败不报错：有的固件本来就不需要解锁，或密码不同 —— 解开与否由后面的查询指令说了算。
func (mod *Modem) usbnetPrepare() {
	d := usbDialectFor(mod)
	if d == nil || len(d.Pre) == 0 {
		return
	}
	for _, c := range d.Pre {
		if _, err := mod.SendAT(c); err != nil {
			logger.Debug("usbnet pre-command failed",
				zap.Int("modem", mod.ID), zap.String("cmd", c))
		}
	}
}

// usbnetPrepareSet 写入 USB 模式前的“有副作用的”进门指令（见 usbDialect.preSet）。
//
// 目前只有 openluat 用到：`AT+RNDISCALL=0,0` 会**停掉当前分组数据呼叫**（= 短暂断数据），
// 所以绝不能放在查询路径上（查询也调 usbnetPrepare 那一条）。
func (mod *Modem) usbnetPrepareSet() {
	d := usbDialectFor(mod)
	if d == nil || len(d.PreSet) == 0 {
		return
	}
	for _, c := range d.PreSet {
		if _, err := mod.SendAT(c); err != nil {
			logger.Debug("usbnet pre-set command failed",
				zap.Int("modem", mod.ID), zap.String("cmd", c))
		}
	}
}

// usbnetNote 本机说明：清单口径（指令出处 + 未取证的档位 + usb_at 自己的说明）；
// 型号不在清单里时退回厂商方言说明 + 支持清单说明（旧行为）。
func usbnetNote(mod *Modem) string {
	var parts []string
	if e, ok := catalogUSBATFor(mod); ok {
		parts = append(parts, "指令取自模块清单 modem_catalog.json 的 usb_at（Modem-Pro 取证）；"+usbnetCatalogText(e))
		if s := strings.TrimSpace(e.USBModeAT.Note); s != "" {
			parts = append(parts, s)
		}
		// ⚠️ 清单口径下**不再叠加厂商方言说明**：取值与指令都以清单为准，
		// 两套说法并存只会互相矛盾（"改完需重启"由界面的 need_reboot 提示）。
		return strings.Join(parts, " ")
	}
	if d := usbDialectFor(mod); d != nil && strings.TrimSpace(d.Note) != "" {
		parts = append(parts, d.Note)
	}
	if _, note := catalog.CatalogUSBModesFor(modCatalogKeys(mod), nil); strings.TrimSpace(note) != "" {
		parts = append(parts, note)
	}
	return strings.Join(parts, " ")
}
