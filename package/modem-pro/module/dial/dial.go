package dial

// ====================== 拨号上网（纯逻辑） ======================
//
// 「拨号上网」= 把模组的数据链路建立起来并盯住它。这里分两件事：
//
//	① 认类型：模组以哪种 USB 协议把上网能力交给主机（AT+QCFG="usbnet"，取值表见 net.go）。
//	   它决定**谁来拨号**：QMI/NDIS 与 MBIM 要靠主机侧拨号器
//	   （QMI：各家自己的 quectel-CM / fibocom-dial / meig-cm / simcom-cm；MBIM：一律 quectel-CM，
//	    只有它实现了 MBIM —— 见 hostdial.go 的 hostDialKindVendor），
//	   ECM / RNDIS / NCM 只要模组侧把 PDP 激活、主机对网卡跑 DHCP 就能上网。
//	   固件不支持那条 AT 时退回读 USB 描述符（接口类码）推断，见 usbdesc_*.go。
//	② 拨号：写 APN（AT+CGDCONT）→ 激活 PDP（AT+CGACT=1,<cid>）→ 回读确认 → 读本机地址
//	   （AT+CGPADDR）→ 找主机侧网卡。这几步全是 3GPP 标准指令，各家固件都支持。
//
// 本文件只有纯逻辑（取值表 / 判定 / 状态机 / 主机网卡匹配），
// 发指令的胶水在 dial_engine.go，HTTP 接口在 api_dial.go。

import (
	"fmt"
	"modemd/module/usbids"
	"net"
	"sort"
	"strings"
	"time"
)

// ---------- 状态机 ----------

// 拨号状态的四个取值（页面上就这四种说法）
const (
	DialIdle      = "idle"      // 未拨号
	DialDialing   = "dialing"   // 拨号中
	DialConnected = "connected" // 已连接
	DialFailed    = "failed"    // 拨号失败
)

// DialStateText 拨号状态的中文说明
func DialStateText(state string) string {
	switch state {
	case DialDialing:
		return "拨号中"
	case DialConnected:
		return "已连接"
	case DialFailed:
		return "拨号失败"
	default:
		return "未拨号"
	}
}

// DialStep 拨号过程中的一步。页面按顺序列出来，失败的那一步一眼能看到。
type DialStep struct {
	Name string    `json:"name"`
	OK   bool      `json:"ok"`
	Info string    `json:"info,omitempty"`
	At   time.Time `json:"at,omitempty"`
}

// DialState 当前拨号状态（页面「拨号上网」显示它；WS 推送的类型是 dial）。
type DialState struct {
	ModemID   int    `json:"modem_id"`
	State     string `json:"state"`
	StateText string `json:"state_text"`
	// CID 拨的是哪条 PDP 上下文（默认 DefaultDataCID）
	CID     int    `json:"cid"`
	APN     string `json:"apn,omitempty"`
	PDPType string `json:"pdp_type,omitempty"`
	IPv4    string `json:"ipv4,omitempty"`
	IPv6    string `json:"ipv6,omitempty"`

	// 拨号时识别出的 USB 类型（见 USBType），它决定主机侧怎么拨
	Kind      string `json:"kind,omitempty"`
	TypeName  string `json:"type_name,omitempty"`
	HostSetup string `json:"host_setup,omitempty"`

	// 主机侧网卡候选，**仅供参考**：同一台机器上别的网卡也可能命中命名规则
	// （台式机自带的网口就叫"以太网"），所以页面上标的是"候选"。
	HostIface string   `json:"host_iface,omitempty"`
	HostIPs   []string `json:"host_ips,omitempty"`

	ConnectedAt time.Time `json:"connected_at,omitempty"`
	ElapsedSec  int       `json:"elapsed_sec"`
	LastError   string    `json:"last_error,omitempty"`
	// ManualStop 用户手动点过「断开」→ 自动组网就别再替他拨回来（尊重人的意思）。
	// 手动拨号 / 重新插拔（模组对象重建）都会把这个标记清掉。
	ManualStop bool       `json:"manual_stop,omitempty"`
	Steps      []DialStep `json:"steps,omitempty"`
}

// Snapshot 复制一份：切片要另存，否则调用方会与拨号协程共享底层数组
// （拨号过程中还在往里 append，前端 JSON 序列化时就会读到半截）。
func (s DialState) Snapshot() DialState {
	out := s
	out.HostIPs = append([]string(nil), s.HostIPs...)
	out.Steps = append([]DialStep(nil), s.Steps...)
	return out
}

// WithElapsed 填上"已连接多久"（每次对外给状态时重算，不是存下来的）
func (s DialState) WithElapsed(now time.Time) DialState {
	if s.State == DialConnected && !s.ConnectedAt.IsZero() {
		s.ElapsedSec = int(now.Sub(s.ConnectedAt).Seconds())
	}
	return s
}

// DialRequest 一次拨号请求（HTTP 层与引擎共用）
type DialRequest struct {
	CID       int    `json:"cid"`
	APN       string `json:"apn"`
	PDPType   string `json:"pdp_type"`
	Reconnect bool   `json:"reconnect"` // 已激活的先断开再连，让刚写入的 APN 生效
	Force     bool   `json:"force"`     // 重新识别 USB 类型（不走缓存）
}

// ---------- USB 类型 → 拨号方式 ----------

// DialType 一种 USB 网卡类型（= AT+QCFG="usbnet" 的取值）对应的**拨号方式**。
//
// Kind 是归一化的协议名（qmi / ecm / mbim / rndis / ncm / unknown），
// 界面与状态里用 Kind 做判定，展示名用 Name。
type DialType struct {
	Value      string `json:"value"`       // usbnet 取值，如 "1"
	Name       string `json:"name"`        // 展示名，如 "ECM"
	Kind       string `json:"kind"`        // 归一化协议名：qmi/ecm/mbim/rndis/ncm
	Protocol   string `json:"protocol"`    // 主机内核里长什么样
	HostDriver string `json:"host_driver"` // 主机侧需要的驱动 / 拨号工具
	HostSetup  string `json:"host_setup"`  // 主机侧要执行的拨号动作（照抄即可）
	ATDial     bool   `json:"at_dial"`     // 模组侧 AT 拨号（= 本页「拨号」按钮）是否够用
	Note       string `json:"note,omitempty"`
}

// DialTypes 全部已知类型（页面用这份列表把当前取值翻成"该怎么拨"）
func DialTypes() []DialType {
	return []DialType{
		{
			Value: "0", Name: "QMI/NDIS", Kind: "qmi",
			Protocol:   "rmnet（wwan0）",
			HostDriver: "qmi_wwan + 厂商 QMI 拨号器（移远 quectel-CM / 广和通 fibocom-dial / 美格 meig-cm / 芯讯通 simcom-cm）/ uqmi；Windows 装高通或移远驱动",
			HostSetup:  "主机侧拨号：厂商拨号器（quectel-CM / fibocom-dial / meig-cm / simcom-cm，modemd 按厂商自动挑） -s <APN>，或 uqmi -d /dev/cdc-wdm0 --start-network <APN>；Windows 由 WWAN 服务自动建立。",
			ATDial:     true,
			Note:       "高通原生协议，模组自己不发 DHCP；AT 侧激活 PDP 只代表模组这一端就绪。",
		},
		{
			Value: "1", Name: "ECM", Kind: "ecm",
			Protocol:   "cdc_ether（usb0 / eth1）",
			HostDriver: "Linux 内核自带 cdc_ether，免驱",
			HostSetup:  "AT 侧拨号成功后在主机跑 DHCP：udhcpc -i usb0（OpenWrt：ifup usb0 / 建 usb0 接口设为 DHCP 客户端）。",
			ATDial:     true,
			Note:       "地址常由模组侧的 DHCP 下发；接口名以主机实际为准（usb0 / eth1 / enx…）。",
		},
		{
			Value: "2", Name: "MBIM", Kind: "mbim",
			Protocol:   "cdc_mbim（wwan0 + /dev/cdc-wdm0）",
			HostDriver: "cdc_mbim + quectel-CM 的 mbim-cm（Windows 自带 MBIM 类驱动）",
			HostSetup:  "主机侧拨号：modemd 拉起 quectel-CM 的 MBIM 路径（`-s <APN>` **必填**）—— 它自己认 MBIM 设备、建会话，再把链路地址配到网卡上。等价的手工命令：`umbim -d /dev/cdc-wdm0 connect`（netifd 的 mbim.sh 用的就是它）。",
			ATDial:     false,
			Note:       "MBIM 是微软标准，一套会话里既有拨号也有 IP 配置指令；模组的 DHCP 服务器通常也在链路上，所以 udhcpc 能拿到同一份地址。⚠️ 模组只有**一个**数据承载：模组侧先激活过 PDP 的话，MBIM_CID_CONNECT 会被拒（Status=2），得先把它收掉。",
		},
		{
			Value: "3", Name: "RNDIS", Kind: "rndis",
			Protocol:   "rndis_host（usb0 / eth1）",
			HostDriver: "Windows 免驱；Linux 需 rndis_host（部分固件兼容性一般）",
			HostSetup:  "同 ECM：AT 侧拨号成功后在主机对该网卡跑 DHCP 即可。",
			ATDial:     true,
			Note:       "经典 USB 网卡，兼容性广但性能一般。",
		},
		{
			Value: "5", Name: "NCM", Kind: "ncm",
			Protocol:   "cdc_ncm（wwan0 / usb0）",
			HostDriver: "cdc_ncm + cdc_ether（Linux 自带；OpenWrt 装 kmod-usb-net-cdc-ncm）",
			HostSetup:  "同 ECM：AT 侧拨号成功后在主机对该网卡跑 DHCP 即可。",
			ATDial:     true,
			Note:       "高速 USB 网络，带宽比 ECM/RNDIS 高。",
		},
	}
}

// DialKindOf usbnet 取值 → 归一化协议名（表外/空值都是 unknown）
func DialKindOf(value string) string {
	switch strings.TrimSpace(value) {
	case "0":
		return "qmi"
	case "1":
		return "ecm"
	case "2":
		return "mbim"
	case "3":
		return "rndis"
	case "5":
		return "ncm"
	}
	return "unknown"
}

// DialTypeByValue 取某个取值的拨号方式（表外取值返回 false）
func DialTypeByValue(value string) (DialType, bool) {
	v := strings.TrimSpace(value)
	for _, t := range DialTypes() {
		if t.Value == v {
			return t, true
		}
	}
	return DialType{}, false
}

// DialKindFromName 把**本机取值表里的展示名**归一成协议名。
//
// 为什么需要它：各家的 usbnet 取值完全不同 —— 移远 `0` 是 QMI，而广和通 `32` 才是 QMI
// （`AT+GTUSBMODE` 那套：32 QMI / 30 MBIM / 18 ECM…）。DialTypes 表是按**移远取值**做 key 的，
// 所以跨厂商时必须先按名字认出协议，再用协议查表 ——
// 否则广和通模组会出现"QMI 认出来了，但主机侧接口/驱动显示未识别"（实测反馈）。
func DialKindFromName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	switch {
	case s == "":
		return ""
	// ⚠️ rndis 必须排在 ndis 前面："RNDIS" 里就含 "ndis"，
	// 否则 RNDIS 会被判成 QMI（实测踩过）
	case strings.Contains(s, "rndis"):
		return "rndis"
	case strings.Contains(s, "mbim"):
		return "mbim"
	case strings.Contains(s, "ecm"):
		return "ecm"
	case strings.Contains(s, "ncm"):
		return "ncm"
	case strings.Contains(s, "qmi"), strings.Contains(s, "ndis"):
		return "qmi"
	}
	return ""
}

// DialKindLabel 协议名的中文展示（没认出来就返回原值）
func DialKindLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "qmi":
		return "QMI"
	case "ecm":
		return "ECM"
	case "mbim":
		return "MBIM"
	case "rndis":
		return "RNDIS"
	case "ncm":
		return "NCM"
	}
	return kind
}

// DialTypeByKind 按归一化协议名取拨号方式（跨厂商时用这条）
func DialTypeByKind(kind string) (DialType, bool) {
	k := strings.ToLower(strings.TrimSpace(kind))
	if k == "" || k == "unknown" {
		return DialType{}, false
	}
	for _, t := range DialTypes() {
		if t.Kind == k {
			return t, true
		}
	}
	return DialType{}, false
}

// ---------- USB 描述符 ----------

// USBDesc 模组 USB 侧的实际描述（AT 取值拿不到时的兜底依据）。
//
// 只看两样：VID/PID 与**各接口的类码**。类码是 USB-IF 的通用约定
// （02/06 = CDC-ECM 控制口、02/0d = CDC-NCM 控制口、02/0e = MBIM、ef/04/01 = RNDIS；
// 0a 只是 ECM/NCM/MBIM 共用的数据口），与厂商无关，
// 所以固件不认 AT+QCFG="usbnet" 时也能推断出拨号方式。
type USBDesc struct {
	VID     string   `json:"vid,omitempty"`     // 厂商号，如 2c7c
	PID     string   `json:"pid,omitempty"`     // 产品号，如 0800
	Product string   `json:"product,omitempty"` // 设备描述（拿不到就空）
	Classes []string `json:"classes,omitempty"` // 各接口的 类/子类/协议，如 02/06/00
	Source  string   `json:"source,omitempty"`  // 数据来源：windows-registry / sysfs
	Note    string   `json:"note,omitempty"`
}

// ReduceUSBCodes 把"同一个接口的多种简写"收敛成最具体的那一条。
//
// Windows 的 CompatibleIDs 对每个接口会同时给三行（实测）：
//
//	USB\Class_e0&SubClass_01&Prot_03
//	USB\Class_e0&SubClass_01
//	USB\Class_e0
//
// 指的是同一个接口。全收下来页面上会出现 `e0/01/03`、`e0/01/`、`e0//` 三条同义条目。
// 规则：同一个类下，`类/子类/协议` 比 `类/子类` 具体、`类/子类` 比 `类` 具体 —— 只留最具体的那条。
func ReduceUSBCodes(codes []string) []string {
	type triple struct{ class, sub, proto string }
	triples := make([]triple, 0, len(codes))
	for _, c := range codes {
		if class, sub, proto, ok := usbids.SplitUSBClassCode(c); ok {
			triples = append(triples, triple{class, sub, proto})
		}
	}
	out := make([]string, 0, len(triples))
	seen := map[string]bool{}
	for i, t := range triples {
		shorthand := false
		for j, u := range triples {
			if i == j || u.class != t.class {
				continue
			}
			// 同一类里，别人比本条目"更具体" → 本条目是简写
			if t.sub == "" && u.sub != "" {
				shorthand = true
				break
			}
			if t.proto == "" && u.proto != "" && u.sub == t.sub {
				shorthand = true
				break
			}
		}
		if shorthand {
			continue
		}
		code := t.class
		if t.sub != "" {
			code += "/" + t.sub
		}
		if t.proto != "" {
			code += "/" + t.proto
		}
		if !seen[code] {
			seen[code] = true
			out = append(out, code)
		}
	}
	sort.Strings(out)
	return out
}

// HasVendorInterface 是否至少有一个厂商自定义（ff）接口。
//
// 用途：**没有**任何 ECM / MBIM / NCM / RNDIS 数据接口、但有厂商自定义接口时，
// 基本就是"AT 口 + QMI/NDIS 口"的经典组合（实测 FM350-GL：ff/00/00 + ff/42/01 + 蓝牙 e0/01/03），
// 按 QMI/NDIS（取值 0）处理最稳 —— 比"未知"更有用，但要在页面上标清是推断的。
// 注意：蓝牙 / 音频 / U 盘这些接口不参与判断，它们不可能是拨号口。
func HasVendorInterface(classes []string) bool {
	for _, c := range classes {
		if class, _, _, ok := usbids.SplitUSBClassCode(c); ok && class == "ff" {
			return true
		}
	}
	return false
}

// ClassSummary 把接口类码翻成人话（页面「USB 描述」一栏）。
// 认不出的原样回传，不做猜测。
//
// ⚠️ CDC 设备里"协议"写在**控制接口（class 02）的子类**上，数据接口（class 0a）是共用的：
//
//	02/0d = NCM 控制口、02/06 = ECM 控制口、02/0e = MBIM 控制口
//	0a    = 数据口（ECM / NCM / MBIM 都长这样，光看它认不出协议）
//
// 实测鼎桥 MT5700M-CN：2.0 = 02/0d/00 + 2.1 = 0a/00/01。老写法把 02/0d 说成笼统的
// "CDC（通信类）"、把 0a 一律说成 "CDC-NCM"，正好把"哪个接口是什么协议"说反了。
func ClassSummary(code string) string {
	class, sub, _, ok := usbids.SplitUSBClassCode(code)
	if !ok {
		return code
	}
	subText := sub
	if subText == "" {
		subText = "-"
	}
	switch {
	case class == "02" && sub == "0d":
		return code + " CDC-NCM 控制接口"
	case class == "02" && sub == "0e":
		return code + " CDC-MBIM 控制接口"
	case class == "02" && sub == "06":
		return code + " CDC-ECM 控制接口"
	case class == "02" && sub == "02":
		return code + " CDC-ACM 串口（AT 口）"
	case class == "02":
		return code + " CDC 控制接口（子类 " + subText + "）"
	case class == "0a":
		return code + " CDC 数据接口（ECM / NCM / MBIM 共用）"
	case class == "ef" && sub == "04":
		return code + " RNDIS"
	case class == "ff" && sub == "06":
		return code + " 厂商自定义串口（AT / 诊断口）"
	case class == "ff":
		return code + " 厂商自定义（AT / QMI / 诊断口）"
	case class == "08":
		return code + " 大容量存储（U 盘）"
	case class == "0e":
		return code + " 视频"
	case class == "01":
		return code + " 音频"
	case class == "03":
		return code + " HID"
	case class == "e0":
		return code + " 无线控制器（蓝牙等）"
	case class == "00":
		return code + " 未指定（在接口级别定义）"
	}
	return code
}

// ClassSummaryList 一串类码 → 一串人话（去重，保持原顺序）
func ClassSummaryList(classes []string) []string {
	out := make([]string, 0, len(classes))
	seen := map[string]bool{}
	for _, c := range classes {
		s := ClassSummary(c)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// ---------- 主机侧网卡候选 ----------

// IfaceInfo 主机上的一张网卡（只看名字 / 状态 / 地址，不判断厂商）
type IfaceInfo struct {
	Name string   `json:"name"`
	Up   bool     `json:"up"`
	Addr []string `json:"addrs,omitempty"`
	// Strong = 名字本身就指向 USB / WWAN 口（wwan*、rmnet*、usb*、enx*）。
	// 通用以太网名（eth*、Windows 的"以太网"）为 false —— 台式机自带的网口也叫这个，
	// 只能当参考，页面会把它们排在后面并注明"参考"。
	Strong bool `json:"strong"`
}

// DialIfaceReject 与模组数据口无关的网卡（虚拟 / 无线 / 蓝牙 / 环回）。
//
// 必须先排掉：这些名字里带 "eth"（vEthernet）、"usb"（VirtualBox Host-Only）的一大堆，
// 不排掉页面上的"候选网卡"会变成一长串噪音。
var DialIfaceReject = []string{
	"loopback", "docker", "veth", "vethernet", "wsl", "bluetooth", "蓝牙",
	"wi-fi", "wifi", "wlan", "wireless", "无线",
	// OpenWrt 的无线接口不一定叫 wlan*（新命名是 phy0-ap0 / phy1-mesh0 这种），
	// 一并排掉，免得网卡下拉里混进一堆 wifi 口
	"phy", "-ap", "mesh",
	"virtualbox", "vmware", "vmnet", "hyper-v", "tap", "tun", "tailscale",
	"zerotier", "openvpn", "wireguard", "hamachi", "npcap", "radmin",
	"default switch", "bridge", "br-", "virbr", "npf", "vpn",
}

// dialIfaceStrong 名字里直接指向 USB / WWAN 的（可信度高）
var dialIfaceStrong = []string{"wwan", "rmnet", "wwx", "usb", "enx", "rndis", "cdc-wdm", "qmi"}

// dialIfaceWeak 通用以太网名（Linux 的 eth*/en*、Windows 的"以太网"）：
// 只作参考，理由是台式机自带网口/其它 USB 网卡也叫这个。
var dialIfaceWeak = []string{"eth", "en", "以太网", "ethernet"}

// MatchDialIface 这张网卡像不像模组的数据口。第二个返回值表示"可信"（强候选）。
func MatchDialIface(name string) (ok, strong bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false, false
	}
	for _, bad := range DialIfaceReject {
		if strings.Contains(n, bad) {
			return false, false
		}
	}
	for _, p := range dialIfaceStrong {
		if strings.Contains(n, p) {
			return true, true
		}
	}
	for _, p := range dialIfaceWeak {
		if strings.Contains(n, p) {
			return true, false
		}
	}
	return false, false
}

// DialIfaceSnapshot 扫一遍主机网卡，挑出可能是模组数据口的那些（强候选在前，其余按名字排）。
//
// 只是**提示**：真正把网卡拉起来要看主机侧的驱动/拨号器（见 DialType.HostSetup），
// 本程序不去改主机的路由/接口（那属于路由器固件该干的事）。
func DialIfaceSnapshot() []IfaceInfo {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]IfaceInfo, 0, 4)
	for _, in := range ifaces {
		ok, strong := MatchDialIface(in.Name)
		if !ok {
			continue
		}
		info := IfaceInfo{Name: in.Name, Up: in.Flags&net.FlagUp != 0, Strong: strong}
		if addrs, err := in.Addrs(); err == nil {
			for _, a := range addrs {
				if ipn, ok := a.(*net.IPNet); ok && ipn.IP != nil {
					info.Addr = append(info.Addr, ipn.IP.String())
				}
			}
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Strong != out[j].Strong {
			return out[i].Strong
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// PickDialIface 从候选里挑一个最像的：优先"已 up 且拿到地址"的强候选，
// 其次任意强候选，最后弱候选；一个都没有则返回空。
func PickDialIface(list []IfaceInfo) (IfaceInfo, bool) {
	best := IfaceInfo{}
	found := false
	rank := func(i IfaceInfo) int {
		switch {
		case i.Strong && i.Up && len(i.Addr) > 0:
			return 0
		case i.Strong:
			return 1
		case i.Up && len(i.Addr) > 0:
			return 2
		default:
			return 3
		}
	}
	for _, i := range list {
		if !found || rank(i) < rank(best) {
			best, found = i, true
		}
	}
	return best, found
}

// FormatVidPid VID/PID 的展示形式（都是空则返回空串）
func FormatVidPid(vid, pid string) string {
	if vid == "" && pid == "" {
		return ""
	}
	if pid == "" {
		return vid
	}
	return fmt.Sprintf("%s:%s", vid, pid)
}
