package kernel

// ====================== 拨号上网（引擎侧的胶水） ======================
//
// 纯逻辑在 dial.go（类型表 / 状态机 / 主机网卡匹配 / USB 接口类码），
// 这里只管"发哪条指令、按什么顺序、失败停在哪一步"：
//
//	写 APN（AT+CGDCONT）→ 激活 PDP（AT+CGACT=1,<cid>）→ 回读确认（AT+CGACT?）
//	→ 读本机地址（AT+CGPADDR）→ 找主机侧网卡（net.Interfaces，纯本机、不占串口）
//
// 激活那一步直接复用「可写参数」里的 SetPDPActive：它已经做了 cid 校验、配额限制检查、
// "先断后连"和回读确认，拨号没有再发明一套（也就不会有两套行为不一致的激活逻辑）。
//
// USB 类型的识别有缓存（dialTypeTTL）：识别要发一条 AT+QCFG="usbnet"，
// 页面每次刷新都问一遍没必要；改了 usbnet 配置后点「重新识别」（force）即可。

import (
	"fmt"
	"modemd/module/atparse"
	"modemd/module/catalog"
	"modemd/module/common"
	"modemd/module/config"
	"modemd/module/dial"
	"modemd/module/hostnic"
	"modemd/module/netprov"
	"modemd/module/tdtech"
	"modemd/module/usbdesc"
	"modemd/module/usbids"
	"modemd/module/usbnet"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// dialTypeTTL USB 类型识别结果的缓存时长
const dialTypeTTL = 30 * time.Second

// DialTypeInfo USB 类型识别结果（页面「模块 USB 类型」卡片显示它）
type DialTypeInfo struct {
	ModemID   int    `json:"modem_id"`
	Value     string `json:"value,omitempty"` // usbnet 取值，如 "1"
	Name      string `json:"name,omitempty"`  // 展示名，如 "ECM"
	Kind      string `json:"kind"`            // qmi / ecm / mbim / rndis / ncm / unknown
	Source    string `json:"source"`          // at（问固件）/ descriptor（读 USB 描述符）/ hostiface（主机侧网卡驱动）/ none
	Confident bool   `json:"confident"`       // 取值是否可信（AT 或类码明确认出来的）
	// QueryCmd 本机实际用的那条查询指令（各家不同：移远 AT+QCFG="usbnet"、
	// 广和通 AT+GTUSBMODE? …）—— 页面“固件上报”后面就显示它，不写死 QCFG
	QueryCmd string `json:"query_cmd,omitempty"`

	Descriptor *dial.USBDesc  `json:"descriptor,omitempty"` // 读到的 USB 描述（可能为空）
	Dial       *dial.DialType `json:"dial,omitempty"`       // 该类型的拨号方式（表外取值为空）
	// HostIface 主机侧网卡探测结果（Linux：sysfs 里的驱动名 + USB 祖先）——
	// 它是第③条识别路径的依据，页面上能直接看到“哪张卡、什么驱动”
	HostIface *usbnet.HostIfaceHint `json:"host_iface_hint,omitempty"`

	// Bus 总线类型："usb" / "pcie"（都不确定时为空）。
	// PCIe 模组（MHI / wwan）走不了 USB 那条路：它的 AT 口是字符设备、
	// 网卡由 mhi_netdev/rmnet 交出来，所以单独带一份 PCIeInfo。
	Bus  string    `json:"bus,omitempty"`
	PCIe *PCIeInfo `json:"pcie,omitempty"`

	// CatalogModel / CatalogUSB / CatalogPCIe：命中**模块支持清单**
	// （modem_catalog.go，数据来自 QModem `docs/support_list.md`）时给出 ——
	// 本型号在 USB / PCIe 两种形态下各支持哪几种数据协议。
	// 型号不在清单里时留空（不猜）。
	CatalogModel string   `json:"catalog_model,omitempty"`
	CatalogUSB   []string `json:"catalog_usb,omitempty"`
	CatalogPCIe  []string `json:"catalog_pcie,omitempty"`

	Note       string    `json:"note,omitempty"` // 识别不出来时的说明
	ProbedAt   time.Time `json:"probed_at"`
	tdtechNDIS bool
}

// USBType 识别模组当前的 USB 类型（页面「模块 USB 类型」卡片）。
//
// 三条路**都会走一遍**（都只是本地读取/一条 AT，很便宜），各补各的信息 ——
// 以前是"前一条成功就 return"，于是 AT 报出取值后，主机侧接口/驱动/VID:PID/USB 设备
// 全是空的（实测反馈的那种"QMI 认出来了，下面四项还是未识别"）：
//
//	① 问固件 `AT+QCFG="usbnet"`（各家方言不同：移远 0/1/2/3/5、广和通 32/30/18…）
//	② 读 USB 描述符 → VID:PID、产品名、接口类码
//	③ 读主机侧网卡的驱动 → wwan0 ← qmi_wwan 这类，能把协议直接定下来
//
// 协议（Kind）的判定顺序：**AT 取值在本机取值表里的名字** → usbnet 取值表 →
// 描述符类码 → 主机侧网卡驱动。跨厂商的关键是第 1 个：广和通的 32 只有按名字才知道是 QMI。
func (mod *Modem) USBType(force bool) *DialTypeInfo {
	mod.dialMu.Lock()
	previous := mod.dialType
	if !force && previous != nil && time.Since(mod.dialTypeAt) < dialTypeTTL {
		info := *previous
		mod.dialMu.Unlock()
		return &info
	}
	mod.dialMu.Unlock()

	info := &DialTypeInfo{ModemID: mod.ID, Kind: "unknown", Source: "none", ProbedAt: time.Now()}

	// ① 固件自己说（移远/广和通/芯讯通/华为… 各家取值不同，Name 就是本机的协议名）
	var atErr error
	if st, err := mod.QueryUSBnet(); err == nil {
		info.Value, info.Source, info.Confident = st.Value, "at", true
		info.Name = st.Name
	} else {
		atErr = err
	}
	// 本机用哪条指令问的（各家不同，页面要如实显示而不是写死 QCFG）
	if d := usbDialectFor(mod); d != nil {
		info.QueryCmd = d.Query
	}

	// ② USB 描述符：VID:PID / 产品名 / 接口类码（AT 成功时也读，页面那两格要显示）
	if d := usbdesc.USBDescriptor(mod.ATPort); d != nil {
		info.Descriptor = d
	}

	// ③ 主机侧网卡的驱动（本地 sysfs，不发 AT）
	if h := usbnet.HostIfaceHintImpl(); h != nil {
		info.HostIface = h
		// 描述符读不到时，用"网卡所在的那个 USB 设备"补上 VID/PID/产品名
		if info.Descriptor == nil && (h.VID != "" || len(h.Classes) > 0) {
			info.Descriptor = &dial.USBDesc{
				VID: h.VID, PID: h.PID, Product: h.Product,
				Classes: h.Classes, Source: "sysfs（网卡所在的 USB 设备）",
			}
		}
	}

	// 取值（AT 最权威；拿不到才退回本地推断）
	if info.Value == "" && info.Descriptor != nil {
		if v, ok := usbids.ClassifyUSBClasses(info.Descriptor.Classes); ok {
			info.Value, info.Source, info.Confident = v, "descriptor", true
		} else if dial.HasVendorInterface(info.Descriptor.Classes) {
			// 只有厂商自定义接口（AT / QMI / 诊断口），没有标准数据类 → 按 QMI/NDIS 推断
			info.Value, info.Source = "0", "descriptor"
			info.Note = "USB 接口里没有 ECM/MBIM/NCM/RNDIS 数据类（只有厂商自定义接口），按 QMI/NDIS 推断"
		}
	}
	if info.Value == "" && info.HostIface != nil && info.HostIface.Value != "" {
		info.Value, info.Source, info.Confident = info.HostIface.Value, "hostiface", true
		info.Note = "固件不支持 AT+QCFG=\"usbnet\"、USB 描述符也读不到，" +
			"按**主机侧网卡的驱动**判定：" + info.HostIface.Iface + " ← " + info.HostIface.Driver
	}

	// 协议名：先按本机取值表里的名字（跨厂商关键），再按 usbnet 取值，再看主机侧驱动
	if info.Kind = dial.DialKindFromName(info.Name); info.Kind == "" {
		if info.Kind = dial.DialKindOf(info.Value); info.Kind == "unknown" {
			if info.HostIface != nil {
				info.Kind = dial.DialKindFromName(info.HostIface.Driver)
			}
		}
	}
	// ④ PCIe（MHI / wwan）模组：AT 口是字符设备，USB 那三条路全不适用 ——
	// 判据改成"有没有 QMI / MBIM 控制口"（见 pcie.go），并补上网卡、BDF、驱动。
	// 放在最后是为了不打扰 USB 路径：USB 模组查不到 PCIe 归属时这里什么都不做。
	if p := PCIeInfoFor(mod); p != nil {
		info.PCIe, info.Bus = p, "pcie"
		if info.HostIface == nil && len(p.Netdevs) > 0 {
			// 读真实的驱动名（mhi_netdev / rmnet / qmi_wwan…），不要编一个
			info.HostIface = usbnet.ReadHostIfaceHint(p.Netdevs[0])
		}
		if k := p.Kind(); k != "" {
			if info.Kind == "" || info.Kind == "unknown" {
				info.Kind = k
				info.Source = "pcie"
			}
		}
		if info.Name == "" {
			info.Name = p.Label()
		}
		if info.Note == "" && p.Note != "" {
			info.Note = p.Note
		}
	} else if info.Descriptor != nil || info.HostIface != nil {
		info.Bus = "usb"
	}
	// ⑤ 模块支持清单（QModem docs/support_list.md）：本型号 USB / PCIe 各支持哪些模式。
	// 只做"提示与过滤"，不参与上面的识别判定（识别一律以本机实测为准）。
	if e, ok := catalog.CatalogEntryForModel(modCatalogKeys(mod)); ok {
		info.CatalogModel = strings.ToUpper(e.Model)
		info.CatalogUSB = append([]string(nil), e.USB...)
		info.CatalogPCIe = append([]string(nil), e.PCIe...)
	}
	if info.Kind == "" {
		info.Kind = "unknown"
	}
	// 展示名：本机取值表的 Name 优先，否则用协议名（QMI/ECM/…）
	if info.Name == "" {
		info.Name = dial.DialKindLabel(info.Kind)
	}
	// 拨号方式：先按取值查（移远口径），跨厂商的按协议查（广和通 32 → qmi）
	if dt, ok := dial.DialTypeByValue(info.Value); ok && dt.Kind == info.Kind {
		info.Dial = &dt
	} else if dt, ok := dial.DialTypeByKind(info.Kind); ok {
		info.Dial = &dt
	}

	if info.Value == "" && info.Bus != "pcie" {
		info.Note = `未能识别 USB 类型：固件不支持 AT+QCFG="usbnet"，也没有读到 USB 描述符`
		if info.HostIface != nil && info.HostIface.Driver != "" {
			info.Note = "主机侧网卡 " + info.HostIface.Iface + " 由 " + info.HostIface.Driver +
				" 驱动（该驱动不能唯一确定协议），USB 类型仍未能识别"
		}
		if atErr != nil {
			info.Note += "（AT 查询：" + atErr.Error() + "）"
		}
	}

	if previous != nil && previous.tdtechNDIS && previous.Kind == info.Kind &&
		previous.Value == info.Value && info.Bus != "pcie" {
		info.tdtechNDIS = true
	} else {
		info.tdtechNDIS = mod.probeTDTechNDIS(info)
	}
	mod.dialMu.Lock()
	mod.dialType, mod.dialTypeAt = info, time.Now()
	mod.dialMu.Unlock()
	logger.Debug("usb type probed", zap.Int("modem", mod.ID),
		zap.String("value", info.Value), zap.String("kind", info.Kind), zap.String("source", info.Source))
	return info
}

// DialStatus 当前拨号状态（对外给状态时顺手算一下"已连接多久"）。
// 状态零值（没拨过号）归一成 idle/未拨号，免得页面上出现空白状态。
func (mod *Modem) DialStatus() *dial.DialState {
	mod.dialMu.Lock()
	st := mod.dialState.Snapshot()
	mod.dialMu.Unlock()
	if st.State == "" {
		st.State = dial.DialIdle
	}
	st.StateText = dial.DialStateText(st.State)
	st = st.WithElapsed(time.Now())
	return &st
}

// cachedDialKind 拨号状态里记着的 USB 类型（不发 AT，只读缓存）。没拨过号就是空串。
func (mod *Modem) cachedDialKind() string {
	mod.dialMu.Lock()
	kind := mod.dialState.Kind
	mod.dialMu.Unlock()
	return strings.TrimSpace(kind)
}

// dialUpdate 在锁内改状态（改完全量替换回去，避免共享切片被改坏）
func (mod *Modem) dialUpdate(fn func(*dial.DialState)) {
	mod.dialMu.Lock()
	fn(&mod.dialState)
	mod.dialMu.Unlock()
}

// dialStep 追加一步过程记录（页面按顺序显示，失败那一步一眼能看到）
func (mod *Modem) dialStep(name string, ok bool, info string) {
	mod.dialUpdate(func(s *dial.DialState) {
		s.Steps = append(s.Steps, dial.DialStep{Name: name, OK: ok, Info: info, At: time.Now()})
	})
}

// dialFail 记下失败原因、把状态置成 failed，并返回当前状态与错误。
// 返回状态是故意的：页面即使拿到错误也能显示"卡在哪一步"。
func (mod *Modem) dialFail(err error) (*dial.DialState, error) {
	mod.dialUpdate(func(s *dial.DialState) {
		s.State, s.StateText = dial.DialFailed, dial.DialStateText(dial.DialFailed)
		s.LastError = err.Error()
	})
	pushDial(mod)
	logger.Warn("dial failed", zap.Error(err))
	return mod.DialStatus(), err
}

// Dial 拨号。**按 USB 类型只走一种方式**：
//
//	QMI   → 写 APN → 让出模组侧承载 → 拉起主机侧厂商拨号器 → 找主机侧网卡（见 dialByHostDialer）
//	其它   → 写 APN（可选）→ 激活 PDP → 回读确认 → 读本机地址 → 找主机侧网卡
//
// 同时只允许一个拨号流程在跑（拨号要连发几条 AT，两次并发会互相干扰串口应答）。
func (mod *Modem) Dial(req dial.DialRequest) (*dial.DialState, error) {
	// 拨号总开关（页面「拨号上网 → 拨号开关」）关掉时，手动拨号也一律拒：
	// "所有模式都不需要拨号"就包括手动 —— 否则用户点了按钮却只看到一行
	// "拨号总开关已关闭"的失败，不如直接在入口挡住并说清在哪儿打开。
	// ⚠️ 这里**不动**已经建立的链路（用户要断就用「断开」），只是不再新拨。
	if !config.DialEnabled() {
		return mod.dialFail(fmt.Errorf("拨号总开关已关闭：打开「拨号上网 → 拨号开关」后才会下发拨号指令"))
	}
	if !mod.beginDialOperation() {
		return nil, fmt.Errorf("上一次拨号还在进行中，请稍候再试")
	}
	defer mod.endDialOperation()

	cid := req.CID
	if cid <= 0 {
		cid = DefaultDataCID
	}
	if !ValidCID(cid) {
		return mod.dialFail(fmt.Errorf("cid 必须在 0~42 之间，当前 %d", cid))
	}
	apn := strings.TrimSpace(req.APN)
	pdpType := strings.ToUpper(strings.TrimSpace(req.PDPType))
	if apn != "" && !ValidAPN(apn) {
		return mod.dialFail(fmt.Errorf("APN 只能包含字母、数字与 . - _（当前 %q）", apn))
	}
	if pdpType != "" && !ValidPDPType(pdpType) {
		return mod.dialFail(fmt.Errorf("PDP 类型只支持 IP / IPV6 / IPV4V6（当前 %q）", pdpType))
	}

	// 先认总线 + 类型：它决定"主机侧谁来拨"，也写进状态里给页面看
	info := mod.USBType(req.Force)
	ndis := info.tdtechNDIS
	activateStep := "激活 PDP（AT+CGACT）"
	if ndis {
		if !tdtech.ValidNDISCID(cid) {
			return mod.dialFail(fmt.Errorf("MT5700M-CN 的 NDIS CID 须在 1~11 之间（当前 %d）", cid))
		}
		activateStep = "建立 NDIS 连接（AT^NDISDUP）"
	}
	hostSetup := ""
	if info.Dial != nil {
		hostSetup = info.Dial.HostSetup
	}
	// PCIe（MHI / wwan）模组要把这句主机侧说明**换掉**：表格里那句是 USB 口径的
	// （`-i wwan0`、`/dev/cdc-wdm0`），而 PCIe 上既不传 `-i`（拨号器自己认 MHI，
	// 传错名字它一台设备都找不到），控制口也不是 cdc-wdmN —— 照抄会把人带沟里。
	if info.PCIe != nil {
		hostSetup = pcieHostSetup(info)
	}
	mod.dialUpdate(func(s *dial.DialState) {
		s.State, s.StateText = dial.DialDialing, dial.DialStateText(dial.DialDialing)
		s.CID, s.APN, s.PDPType = cid, apn, pdpType
		s.Kind, s.TypeName, s.HostSetup = info.Kind, info.Name, hostSetup
		s.IPv4, s.IPv6, s.HostIface, s.HostIPs = "", "", "", nil
		s.ConnectedAt, s.LastError, s.Steps = time.Time{}, "", nil
		s.ManualStop = false // 手动/自动拨号都会走到这里：用户以前的“断开”到此作废
	})
	pushDial(mod)

	// ① 写入 APN（可选：留空表示沿用模组里已有的配置）
	if apn != "" {
		pt := pdpType
		if pt == "" {
			pt = "IPV4V6"
		}
		if _, err := mod.SetAPN(cid, pt, apn); err != nil {
			// 不中断：上下文里可能已有可用 APN，先激活看看；失败原因留在步骤里
			mod.dialStep("写入 APN（AT+CGDCONT）", false, err.Error())
			logger.Warn("dial: write APN failed", zap.Int("modem", mod.ID), zap.Error(err))
		} else {
			mod.dialStep("写入 APN（AT+CGDCONT）", true,
				fmt.Sprintf("cid=%d %s %s", cid, pt, apn))
		}
	} else {
		mod.dialStep("写入 APN（AT+CGDCONT）", true, "沿用模组已有配置")
	}

	// ② 按 USB 类型分派 —— 一台模组只走**一种**拨号方式：
	//
	//	QMI   → 数据呼叫归主机侧厂商拨号器（见 dialByHostDialer/hostdial.go）。这里绝不能
	//	        再发 AT+CGACT=1：模组只有一个数据承载，模组侧先激活会把拨号器的请求顶掉
	//	        （实测 RM502Q-GL：requestSetupDataCall 被拒 QMUXError 0xe、wwan0 link_state 0）。
	//	其它   → ECM/NCM/RNDIS/MBIM 走模组侧 AT 激活（下面 ② 这段）。
	if netprov.WANHostDialByDaemon(info.Kind) {
		return mod.dialByHostDialer(cid, info, hostSetup)
	}

	// ② 激活 PDP（复用「可写参数」那条路径：cid 校验 / 配额限制 / 先断后连 / 回读确认）
	st, err := mod.SetPDPActive(cid, true, req.Reconnect)
	if err != nil && apn == "" && !ndis {
		// ⚠️ 实测 FM350-GL：AT+CGACT=0,<cid> 之后 **该 cid 会从 AT+CGDCONT? 里消失**，
		// 于是"断开 → 拨号（APN 留空）"会报"模组未上报 cid=x 的 PDP 上下文"。
		// 这时拿**上次用过（本来就从模组读出来）的 APN** 补写一次配置再激活，
		// 让"断开再拨"能正常来回 —— 补写这件事会记进步骤里，不偷偷做。
		if last := mod.lastDialAPN(); last != "" {
			pt := pdpType
			if pt == "" {
				pt = "IPV4V6"
			}
			if _, aerr := mod.SetAPN(cid, pt, last); aerr == nil {
				mod.dialStep("补写 APN（该 cid 停用后会从配置列表里消失）", true,
					fmt.Sprintf("cid=%d %s %s", cid, pt, last))
				st, err = mod.SetPDPActive(cid, true, req.Reconnect)
			} else {
				logger.Debug("dial: fallback APN write failed",
					zap.Int("modem", mod.ID), zap.Error(aerr))
			}
		}
	}
	if err != nil {
		mod.dialStep(activateStep, false, err.Error())
		return mod.dialFail(err)
	}
	apnInUse, typeInUse := apn, pdpType
	if st != nil {
		if c := ByCID(st.Contexts, cid); c != nil {
			if c.APN != "" {
				apnInUse = c.APN
			}
			if c.PDPType != "" {
				typeInUse = c.PDPType
			}
		}
	}
	if st != nil {
		if c := ByCID(st.Contexts, cid); c != nil && c.Internal {
			activateStep = "激活 PDP（AT+CGACT）"
		}
	}
	mod.dialStep(activateStep, true, "cid="+strconv.Itoa(cid)+" 已激活")
	mod.dialUpdate(func(s *dial.DialState) {
		s.APN, s.PDPType = apnInUse, typeInUse
	})

	// ②b 顺手把 **IMS 上下文**也激活（用户要求「APN 要自动激活对应的 PDP 和 ims」）。
	// IMS 是 VoLTE / 走 IMS 的短信（5G SA 下常见）的先决条件，很多卡出厂时它是未激活的。
	// ⚠️ 失败只记一条步骤（✗）+ WARN 日志：有些固件就是不许手动激活 IMS（实测 FM350 回 ERROR），
	// 这跟"数据能不能上网"无关，绝不能因此把拨号判成失败（见 pdp_ims.go）。
	if ActivateIMSEnabled() && st != nil {
		res := mod.ActivateIMS(st.Contexts)
		mod.dialStep("激活 IMS 上下文（AT+CGACT）", res.Status != "fail", res.Note)
	}

	// ③ 读本机地址（读不到不算致命：分配可能还在协商，页面可稍后刷新）
	ipv4, ipv6, ipErr := mod.queryPDPAddrs(cid)
	if ipErr != nil {
		mod.dialStep("读取本机地址（AT+CGPADDR）", false, ipErr.Error())
	} else {
		mod.dialStep("读取本机地址（AT+CGPADDR）", true, strings.TrimSpace(ipv4+" "+ipv6))
	}

	// ④ 找主机侧网卡（纯本机枚举，不发 AT；只是提示，见 dial.go 的说明）
	iface, ok := dial.PickDialIface(dial.DialIfaceSnapshot())
	if ok {
		note := iface.Name
		if !iface.Strong {
			note += "（按名字猜的，供参考）"
		}
		if len(iface.Addr) > 0 {
			note += " " + strings.Join(iface.Addr, ", ")
		}
		// 少数机型（FM350 系列）模组侧 DHCP 很慢/不稳：主机网卡可能要手工配 IP
		// （那个面板已按用户要求在页面上下线，所以这里只提示"可能要手工配"，不指卡片）
		if need, _ := hostnic.HostNICQuirk(mod.ModelName); need {
			note += "；⚠️ 该机型主机侧 DHCP 很慢/不稳，可能要手工配一次网卡 IP 才能上网"
		}
		mod.dialStep("主机侧网卡候选", true, note)
	} else {
		mod.dialStep("主机侧网卡候选", false, "本机没找到像模组数据口的网卡（可能主机侧拨号器还没拉起来）")
	}

	mod.dialUpdate(func(s *dial.DialState) {
		s.State, s.StateText = dial.DialConnected, dial.DialStateText(dial.DialConnected)
		s.IPv4, s.IPv6 = ipv4, ipv6
		s.ConnectedAt, s.LastError = time.Now(), ""
		if ok {
			s.HostIface, s.HostIPs = iface.Name, iface.Addr
		}
	})
	pushDial(mod)
	logger.Info("dial ok", zap.Int("modem", mod.ID), zap.Int("cid", cid),
		zap.String("kind", info.Kind), zap.String("ipv4", ipv4))
	return mod.DialStatus(), nil
}

// dialByHostDialer QMI 的拨号：把数据呼叫交给主机侧厂商拨号器（见 hostdial.go）。
//
// 与模组侧 AT 拨号**二选一**（由 Dial 按 USB 类型分派）：这里绝不发 AT+CGACT=1 ——
// 模组只有一个数据承载，模组侧先激活会把拨号器的请求顶掉
// （实测 RM502Q-GL：requestSetupDataCall 被拒 QMUXError 0xe、wwan0 link_state 0）。
//
// 步骤：让出模组侧承载 → 拉起/确认拨号器 → 找主机侧网卡 → 置状态。
// 地址/默认路由/DNS 由拨号器（内部 udhcpc）与 netifd 的 DHCP 一起给，
// 所以这里**不读 AT+CGPADDR**（模组侧没有我们激活的上下文，读了也只是空）。
func (mod *Modem) dialByHostDialer(cid int, info *DialTypeInfo, hostSetup string) (*dial.DialState, error) {
	// ① 让出模组侧数据承载：进程重启后可能还留着上次激活的 cid；手动拨号前再强制清一次
	//   （用户可能刚通过「可写参数」把同一个 cid 激活过）—— 见 releaseHostDialDataContext。
	if note := releaseHostDialDataContext(mod, nil, true); note != "" {
		mod.dialStep("释放模组侧数据承载（交给主机侧拨号器）", true, note)
	} else {
		mod.dialStep("释放模组侧数据承载（交给主机侧拨号器）", true, "模组侧没有我们激活的数据上下文")
	}

	// ② 拉起主机侧拨号器（modemd 托管的厂商拨号器；非 OpenWrt / [net] host_dial=false 时不接管）
	//
	// 判据用**拨号器现在在不在跑**（HostDialStatusFor），而不是 SyncHostDial 那句话 ——
	// 那句话在"已经跑着"、"退避中"、"被停掉"几种情况下都可能是非空串。
	note, errMsg := dialHostSideOnce(mod, mod.DialStatus())
	running := HostDialStatusFor(mod.ID) != ""
	switch {
	case errMsg != "":
		mod.dialStep("拉起主机侧拨号器", false, errMsg)
		return mod.dialFail(fmt.Errorf("主机侧拨号器没起来：%s", errMsg))
	case running:
		mod.dialStep("拉起主机侧拨号器", true, note)
	default:
		// 没跑起来：可能是退避中/被停掉，也可能 modemd 压根不托管（非 OpenWrt 或关掉了 host_dial）。
		// 后者要把"该跑什么"告诉页面（就是 DialType.HostSetup 那句话）。
		tip := note
		if strings.TrimSpace(tip) == "" {
			hint := strings.TrimSpace(hostSetup)
			if hint == "" {
				hint = "见「模块 USB 类型」卡片的主机侧说明"
			}
			tip = "本机不由 modemd 托管主机侧拨号器；请自行运行：" + hint
		}
		mod.dialStep("拉起主机侧拨号器", false, tip)
	}

	// ③ 找主机侧网卡（纯本机枚举，不发 AT）—— QMI 的地址在主机网卡上，不在模组 AT 侧
	var ipv4, ipv6 string
	iface, ok := dial.PickDialIface(dial.DialIfaceSnapshot())
	if ok {
		for _, a := range iface.Addr {
			switch {
			case strings.Contains(a, ":"):
				if ipv6 == "" {
					ipv6 = a
				}
			default:
				if ipv4 == "" {
					ipv4 = a
				}
			}
		}
		mod.dialStep("主机侧网卡", true, iface.Name+" "+strings.Join(iface.Addr, ", "))
	} else {
		mod.dialStep("主机侧网卡", false, "还没认到模组的数据网卡（拨号器把链路拉起来后才有）")
	}

	// ④ 置状态：拨号器在跑 = 这次"拨号"动作到位；没跑起来就停在"拨号中"，等自动组网循环重试
	mod.dialUpdate(func(s *dial.DialState) {
		s.IPv4, s.IPv6 = ipv4, ipv6
		if ok {
			s.HostIface, s.HostIPs = iface.Name, iface.Addr
		}
		if running {
			s.State, s.StateText = dial.DialConnected, dial.DialStateText(dial.DialConnected)
			s.ConnectedAt, s.LastError = time.Now(), ""
		} else {
			s.State, s.StateText = dial.DialDialing, dial.DialStateText(dial.DialDialing)
			s.ConnectedAt = time.Time{}
			s.LastError = "主机侧拨号器还没跑起来（非 OpenWrt 请按主机侧说明手动运行；OpenWrt 上等自动组网循环重试）"
		}
	})
	pushDial(mod)
	logger.Info("dial (host-side)", zap.Int("modem", mod.ID), zap.Int("cid", cid),
		zap.String("kind", info.Kind), zap.Bool("dialer_running", running))
	return mod.DialStatus(), nil
}

// lastDialAPN 最近一次**从模组读出来**的 APN（拨号时记下的，或周期网络查询同步来的）。
//
// 用途：实测部分固件（FM350-GL）停用上下文后会把该 cid 从 AT+CGDCONT? 里抹掉，
// "留空 APN 再拨"就没得可用了 —— 这时用这个值补写配置（见 Dial 的 ② 步）。
// 内部专用 APN（IMS / sos）不算，它们不是上网用的。
func (mod *Modem) lastDialAPN() string {
	mod.dialMu.Lock()
	st := mod.dialState
	mod.dialMu.Unlock()
	apn := strings.TrimSpace(st.APN)
	if apn == "" || atparse.IsInternal(apn) {
		return ""
	}
	return apn
}

func (mod *Modem) beginDialOperation() bool {
	mod.dialMu.Lock()
	defer mod.dialMu.Unlock()
	if mod.dialBusy {
		return false
	}
	mod.dialBusy = true
	return true
}

func (mod *Modem) endDialOperation() {
	mod.dialMu.Lock()
	mod.dialBusy = false
	mod.dialMu.Unlock()
}

// Hangup 断开数据连接。
//
//	QMI   → 停主机侧拨号器（模组侧本来就没有我们激活的上下文，见 dialByHostDialer）
//	鼎桥 MT5700M-CN NDIS 数据会话 → AT^NDISDUP=<cid>,0
//	其它   → 发 AT+CGACT=0,<cid>
//
// cid <= 0 时用当前拨号状态里的 cid，再不行用数据默认 cid。
// ⚠️ 内部上下文（IMS / sos）有的固件不许手动停用，会回 ERROR —— 那时如实报错、不改状态。
func (mod *Modem) Hangup(cid int) (*dial.DialState, error) {
	if cid <= 0 {
		mod.dialMu.Lock()
		cid = mod.dialState.CID
		mod.dialMu.Unlock()
	}
	if cid <= 0 {
		cid = DefaultDataCID
	}
	if !ValidCID(cid) {
		return mod.DialStatus(), fmt.Errorf("cid 必须在 0~42 之间，当前 %d", cid)
	}

	// QMI：数据呼叫归主机侧拨号器，模组侧本来就没有我们激活的上下文（见 dialByHostDialer），
	// 再发 AT+CGACT=0 多半被固件回 ERROR —— 不能因此把"断开"判成失败、更不能让拨号器继续跑。
	if kind := mod.cachedDialKind(); netprov.WANHostDialByDaemon(kind) {
		note := stopHostDial(mod.ID, "用户手动断开")
		mod.dialUpdate(func(s *dial.DialState) {
			s.State, s.StateText = dial.DialIdle, dial.DialStateText(dial.DialIdle)
			s.CID = cid
			s.IPv4, s.IPv6, s.HostIface, s.HostIPs = "", "", "", nil
			s.ConnectedAt, s.LastError = time.Time{}, ""
			s.ManualStop = true // 尊重"人"的断开：自动组网不要再替他拉起来
			step := dial.DialStep{Name: "断开数据连接（停止主机侧拨号器）", OK: true, At: time.Now()}
			if note != "" {
				step.Info = note
			}
			s.Steps = []dial.DialStep{step}
		})
		pushDial(mod)
		logger.Info("dial hangup (host-side)", zap.Int("modem", mod.ID), zap.Int("cid", cid))
		return mod.DialStatus(), nil
	}

	cmd := CmdCGACT(cid, false)
	if mod.usesTDTechNDIS() {
		if !mod.beginDialOperation() {
			return mod.DialStatus(), fmt.Errorf("上一次拨号还在进行中，请稍候再试")
		}
		defer mod.endDialOperation()
		if err := mod.setTDTechNDIS(cid, false, false); err != nil {
			return mod.DialStatus(), err
		}
		cmd = tdtech.NDISDialCommand(cid, false)
	} else {
		resp, err := mod.SendAT(cmd)
		if err != nil {
			return mod.DialStatus(), err
		}
		if common.RespHasError(resp) {
			return mod.DialStatus(), fmt.Errorf("模组拒绝 %s（内部上下文 IMS/sos 不允许手动停用；或该 cid 本来就没激活）", cmd)
		}
		mod.pdpWaitActive(cid, false)
	}
	mod.dialUpdate(func(s *dial.DialState) {
		s.State, s.StateText = dial.DialIdle, dial.DialStateText(dial.DialIdle)
		s.CID = cid
		s.IPv4, s.IPv6, s.HostIface, s.HostIPs = "", "", "", nil
		s.ConnectedAt, s.LastError = time.Time{}, ""
		// 是“人”断的：自动组网不要再替他把链路上拉（见 netprov.go）
		s.ManualStop = true
		s.Steps = []dial.DialStep{{Name: "断开数据连接（" + cmd + "）", OK: true, At: time.Now()}}
	})
	pushDial(mod)
	logger.Info("dial hangup", zap.Int("modem", mod.ID), zap.Int("cid", cid))
	return mod.DialStatus(), nil
}

// queryPDPAddrs 读指定 cid 的本机地址（AT+CGPADDR；个别机型要带 cid，见 quirks）。
// 与 QueryPDPAddrs 的区别：这里按**拨号选的那个 cid** 取，那边固定取数据 cid。
func (mod *Modem) queryPDPAddrs(cid int) (string, string, error) {
	cmd := mod.resolveVendorCmd(KindPDPAddr)
	if cmd == "" {
		return "", "", fmt.Errorf("该机型没有可用的地址查询指令")
	}
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return "", "", err
	}
	if common.RespHasError(resp) {
		return "", "", fmt.Errorf("模组拒绝 %s", cmd)
	}
	ipv4, ipv6 := ParseCGPADDR(resp, cid)
	if ipv4 == "" && ipv6 == "" {
		return "", "", fmt.Errorf("模组未上报本机地址（链路可能还在协商，稍后点「刷新状态」再看）")
	}
	return ipv4, ipv6, nil
}

// syncDialState 用一次网络查询的结果校准拨号状态（**不额外发 AT**）。
//
// 拨号卡片要显示的"在用 APN / 本机地址 / 主机网卡"与「网络」卡片本来就是同一批数据，
// 周期推送里那段查询顺手同步过来就行，页面不用为了这几个字段再占一次串口。
// 只在已拨号（拨号中/已连接）时同步；没拨过号就不去动状态。
func (mod *Modem) syncDialState(st *NetworkStatus) {
	if st == nil {
		return
	}
	mod.dialMu.Lock()
	cur := mod.dialState
	if cur.State != dial.DialConnected && cur.State != dial.DialDialing {
		mod.dialMu.Unlock()
		return
	}
	changed := false
	if st.APN != "" && st.APN != cur.APN {
		cur.APN, changed = st.APN, true
	}
	if st.IPv4 != "" && st.IPv4 != cur.IPv4 {
		cur.IPv4, changed = st.IPv4, true
	}
	if st.IPv6 != "" && st.IPv6 != cur.IPv6 {
		cur.IPv6, changed = st.IPv6, true
	}
	// 主机侧网卡是本地枚举，顺手看看有没有新出现的（主机侧拨号器起来后才有地址）
	iface, ok := dial.PickDialIface(dial.DialIfaceSnapshot())
	if ok && iface.Name != cur.HostIface {
		cur.HostIface, cur.HostIPs, changed = iface.Name, iface.Addr, true
	}
	if !changed {
		mod.dialMu.Unlock()
		return
	}
	mod.dialState = cur
	mod.dialMu.Unlock()
	pushDial(mod)
}

// pushDial 广播拨号状态（页面「拨号上网」靠它实时刷新）
func pushDial(mod *Modem) {
	st := mod.DialStatus()
	id := mod.ID
	broadcast("dial", &id, st)
}
