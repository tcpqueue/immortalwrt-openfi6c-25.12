package config

// ====================== kernel：按功能分文件 ======================

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BurntSushi/toml"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ---- from: config/go ----

// Config 程序配置，对应 toml

type Config struct {
	Server struct {
		Listen      string   `toml:"listen"`
		TLSEnabled  *bool    `toml:"tls_enabled"`
		TLSNames    []string `toml:"tls_names"`
		TLSCertFile string   `toml:"tls_cert_file"`
		TLSKeyFile  string   `toml:"tls_key_file"`
		// ScanInterval 后台热插拔巡检间隔（秒）——**插拔自动检测就靠它**。
		// <= 0 表示不启用后台周期任务：只在启动时发现一次模组，之后不会自动发现；
		// 页面「刷新模组」只读缓存（不再重探串口，不会枚举 / 发 AT 探测），
		// 需要手动重探时用 GET /api/modems?discover=1（排障 / 脚本）。
		// > 0 时按该间隔后台巡检（只有**新出现**的端口、或到了重探时刻的端口才发 AT 探测；
		// 被否定的口按端口退避重探，不会每轮都白等探测超时）。一般 1~5。
		ScanInterval     int    `toml:"scan_interval"`
		PidFile          string `toml:"pid_file"`
		LogFile          string `toml:"log_file"`
		LogLevel         string `toml:"log_level"`
		AutoDetectVendor bool   `toml:"auto_detect_vendor"`
		// PushInterval WebSocket 推送间隔（秒）。
		// > 0 时每 N 秒把信号推给在线客户端；网络状态周期由 NetworkInterval 单独控制。
		// <= 0 关闭周期推送（仅事件推送：新短信 / 热插拔 / 扫描完成 / 锁网变更）。
		// 注意：没有客户端连在 /ws 上时，即使开启也不会发任何 AT 指令。
		PushInterval int `toml:"push_interval"`
		// NetworkInterval 网络制式/运营商状态查询周期（秒）。
		NetworkInterval int `toml:"network_interval"`
		// TrafficInterval 上行/下行速度的采样推送间隔（秒），默认 1。
		// 速度是流量计数器差分算出来的，采样越规律、间隔越短读数越实时，
		// 因此单独走一个更短的节拍，不受 push_interval 影响；<=0 时用默认值 1。
		TrafficInterval int `toml:"traffic_interval"`
		// ChangeInterval 波动记录的**连续后台采样**间隔（秒），默认 0 = 关闭。
		// 网络/信号的波动记录挂在已有的查询上（推送循环 / 接口调用），
		// 所以默认只在"有人在看"时才有记录；设 > 0 后即使没连推送也会按该间隔
		// 采样并记录（信号每轮、网络每 3 轮），代价是后台会持续发 AT 指令。
		ChangeInterval int `toml:"change_interval"`
	} `toml:"server"`
	Serial struct {
		Baud               int           `toml:"baud"`
		ATProbeTimeout     time.Duration `toml:"at_probe_timeout"`
		CmdTimeout         time.Duration `toml:"cmd_timeout"`
		ReadStuckThreshold time.Duration `toml:"read_stuck_threshold"`
		// ProbeWarmup USB 设备刚被枚举出来后的**预热等待**。
		//
		// 背景（真机踩到）：模组由路由器 USB 供电，路由器一重启模组也跟着断电重启。
		// 5G 模组内部是一台小 Linux，开机后要 1~2 分钟才把 AT 守护进程拉起来；
		// 这期间 ttyUSB 节点已存在、open 也不报错，但发 AT 要么不回、要么只回第一条，
		// 结果是第一轮探测全部超时 → 全被写进"非 AT 口"否定缓存 → 模组长时间认不出来。
		// 现在的做法：设备一出现**先探一次**（已就绪的模组立刻发现）；这次失败的话，
		// 预热期内每 warmupProbeEvery（15s）再探一次，出了预热期转指数退避 ——
		// 既不干等（重插同一台模组也能较快认出来），也不会每轮都打扰启动中的固件。
		// ⚠️ 默认 90s：不写、或写 "0s"，都用默认值；写**负值**（如 "-1s"）才是不等待。
		ProbeWarmup time.Duration `toml:"probe_warmup"`
		// USBAutoBind 插上的已知模组如果没有交出任一串口（OpenWrt 上很常见：
		// 内核 option 驱动不认识它），是否自动 modprobe + 写 new_id 把 ttyUSB 补出来。
		// 只在 Linux/OpenWrt 生效（Windows 的串口由驱动 inf 决定，auto bind 做不到）。
		// 默认 true；不写也算 true（见 ApplyDefaults）。
		USBAutoBind bool `toml:"usb_autobind"`
	} `toml:"serial"`
	SMS struct {
		SendQueueSize int    `toml:"send_queue_size"`
		SMSMemory     string `toml:"sms_memory"`
	} `toml:"sms"`

	// SIM 热插拔（见 simhotswap.go）：模组带电拔插 / 换卡。
	SIM struct {
		// HotswapInterval 没收到 URC 时的**兜底轮询**间隔（在不在 / 换没换卡）。
		// 不写默认 20s；写 0s 或负数 = 关掉检查（只靠 URC，漏了不管）。
		HotswapInterval time.Duration `toml:"hotswap_interval"`
		// HotswapReset 确认换卡后是否发一次 AT+CFUN=1,1 让新卡生效。
		// ⚠️ 不写默认 **false**（只探测、只提示，不动模组）：复位模组会让它重新枚举、
		// 掉线重连好几分钟，一旦误判就会变成"模块反复出不来"（2026-10-01 真机事故）。
		// 确实需要复位才能让新卡生效的模组（很多没做 SIM 热插拔硬件）自己写 true。
		HotswapReset bool `toml:"hotswap_reset"`
	} `toml:"sim"`

	// Net OpenWrt 上的“模组上线自动组网”（见 netprov.go）：
	// 模组一插上就自动拨号，拨号成功后看该模组的网卡在 UCI 里有没有对应的接口，
	// 没有就自动建一个 WAN 接口（绑到那张网卡）+ 加进防火墙的 wan 区 ——
	// 不这么做，新插上的模组拿不到地址、上不了网，每次都要去 LuCI 手工建接口。
	Net struct {
		// AutoWAN 总开关。默认 true（不写也算开，见 Load）；只在 Linux+OpenWrt 上真正动手，
		// Windows 上是空操作（那边的网卡/防火墙是 netsh 那套，不碰）。
		AutoWAN bool `toml:"auto_wan"`
		// AutoDial 自动做“模组侧拨号”（写 APN → 激活 PDP）。
		//
		// ⚠️ **主机侧拨号的类型（QMI / MBIM）不靠它上网**：那种模组的数据通道由主机侧拨号器
		// （见 [net] host_dial）建，这里的 AT 侧激活只代表模组那一端就绪。
		// 所以这两类上把它关掉也不影响上网（主机侧拨号器该拉起还是拉起）；
		// 其它类型（ECM / NCM / RNDIS）关掉就等于不做模组侧拨号。
		AutoDial bool `toml:"auto_dial"`
		// WANMetric 自动建的 WAN 接口的默认路由 metric（越小越优先）。
		// 默认 20：路由器本来就有 WAN 时不会被模组抢走默认路由；想让模组优先就改小（如 5）。
		WANMetric int `toml:"wan_metric"`
		// WANZone 把新接口加进哪个防火墙区（默认 wan）。找不到这个区就只建接口、不动防火墙。
		WANZone string `toml:"wan_zone"`
		// QMIProto QMI 类模组的 netifd 接口（modem<id>）用哪个 proto。
		//
		// ⚠️ 用户 2026-10-09 定的口径：**QMI 就用 dhcp**（netifd 的 udhcpc 取地址/路由/DNS，
		// 数据呼叫由主机侧厂商拨号器拉起）。中间试过“拿不到租约就自动降级成 none”，
		// **已经去掉**：none 让 netifd 完全不跑协议，链路上就算有 DHCP 也拿不到地址 = 上不了网。
		//
		//	"" / "auto"（默认）：dhcp。旧版自己建的 qmi / quectel / none 会就地迁回 dhcp。
		//	"dhcp"：同上（显式写法）。
		//	"none"：显式要求不跑协议 —— 地址/路由全靠主机侧拨号器或手工配置。
		// WANMode 自动组网生成的 WAN 接口形态（用户 2026-10-13 要求）。三种取值：
		//
		//	"modem_pro"（默认）：只用一张接口 modem<id>（proto=modem_pro），v4+v6 都跑在上面。
		//	"dhcp"             ：传统两张 —— modem<id>(proto=dhcp) + modem<id>_6(proto=dhcpv6)。
		//	"both"             ：两种都要 —— modem<id>(proto=modem_pro) + modem<id>_6(proto=dhcpv6)。
		//
		// ★ 模式 1/3 下**不再只认 QMI**：qmi / ecm / ncm / rndis / mbim / 未知类型一律用 modem_pro
		// （见 netprov_proto.go 的 wanProtoForKindMode）。旧代码只有一个 case "qmi"，所以
		// 其它类型都落在 dhcp 上、再被配上 modem<id>_6 ⇒ 用户看到的就是"出现两个接口"。
		//
		// ★ MBIM 也不再是例外（用户 2026-10-13）：它的会话建立改由 modemd 托管的厂商拨号器做
		// （只有 quectel-CM 实现了 MBIM，见 hostdial.go 的 hostDialKindVendor）。
		// ⚠️ 只有 [net] host_dial = false 时才退回 proto=mbim（那时没人拨号，只能靠 netifd 的 mbim.sh）。
		//
		// 认不出的值（含空串、写错）一律回落到默认 modem_pro（见 ParseWANMode）。
		WANMode  string `toml:"wan_mode"`
		QMIProto string `toml:"qmi_proto"`
		// V6FixModels "要**特别处理 IPv6**"的模组型号关键字名单（**只对这些型号生效**：
		// dhcpv6 用 auto/hostname/extendprefix/extend_prefix + 写 /etc/firewall.user 那段
		// DNS 重定向/MASQUERADE + 写 route6 modemd_lan6route + 配 odhcpd NDP relay）。
		// ⚠️ 名单外的模组**一个字都不写**（老版本行为），以前写过的会被收掉。
		// 空 = 用内置默认（只有 rm500u）。以后发现新型号也要这套，**往这里加一行就行**。
		V6FixModels []string `toml:"v6_fix_models"`
		// Lan6FixModels 额外需要"把模组的 /64 钉在 LAN + 配 NDP relay"的型号（见 netprov_lan6.go）：
		// 只要模组的 /64 被发给了内网，不钉就会导致"回给内网设备的包走去模组"（v6 时通时不通）。
		// 留空 = 跟 v6_fix_models 同一份名单（RM500U 一直享受这一项，不受这里影响）。
		Lan6FixModels []string `toml:"lan6_fix_models"`
		// HostDial QMI 模组的主机侧拨号器由 modemd 自己托管（默认 true）。
		//
		// 为什么要有这个开关：QMI 的接口是 dhcp（netifd 只负责取地址/路由/DNS），
		// **数据呼叫**（QMI 建 PDN → wwan0 起来）全靠这个拨号器 ——
		// 拨号（QMI 建 PDN → wwan0 拿地址/路由/DNS）全由这个拨号器干。关掉它就只剩一张
		// 什么都不拨的空接口（除非你自己有别的拨号器）。
		HostDial bool `toml:"host_dial"`
		// HostDialBin 拨号器可执行文件路径。留空 = 按模组厂商自动找（**四家各一份**）：
		// /usr/lib/modemd/<厂商拨号器> → /usr/bin/<厂商拨号器> → /usr/sbin/<厂商拨号器>。
		// 移远 quectel-CM／芯讯通 simcom-cm／广和通 fibocom-dial／美格 meig-cm 都随本包安装
		// （源码在 third_party/，见 README「QMI 模式」）。
		HostDialBin string `toml:"host_dial_bin"`
		// HostDialPDN 给厂家拨号器传的 `-n <pdn>`（QMAP 模组的 PDN / mux 序号）。
		//
		//	0（默认） 自动：先按**数据网卡名**反推（`wwan0_1` → 1）；反推不到、但驱动是
		//	          QMAP 模式（`/sys/class/net/<dev>/qmap_mode` = 1，厂家 qmi_wwan_q/_f）
		//	          时按默认承载传 1（见 hostDialPDNFor / hostDialQMAPModeOn）。
		//	          实测 FM160-CN + qmi_wwan_f：不传它拨号器一直不 add_mux，父口 wwan0
		//	          永远 NO-CARRIER、没地址；传上 `-n 1` 立刻 link:1 并读回 IPv4 地址。
		//	>0       强制用这个序号（比如 APN 配在 cid 3 上就写 3）。
		//	-1       永不传 `-n`（老行为：只按网卡名反推）。
		//
		// 只影响 QMI 的厂家拨号器（quectel-CM / fibocom-dial / meig-cm）；simcom-cm 不认这个参数。
		HostDialPDN int `toml:"host_dial_pdn"`
		// ResetBeforeDial 拉起主机侧拨号器**之前**，先把模组“飞行模式 → 复位”一次。
		//
		// 【用户 2026-10-13 要求】所有拨号前都做：`AT+CFUN=4`（关射频）→ 略等 → `AT+CFUN=1,1`（复位）。
		//
		// 为什么需要：FM160-CN 这类模组会陷入“模组侧自己占着唯一的数据承载 / 内部状态卡死”，
		// 症状有两种，都不好认：
		//   · QMI StartNetworkInterface 迟迟不回（实测 6 秒以上、拿不到 handle），
		//     拨号器一直重试、wwan0 永远没地址；
		//   · 地址/路由/模组侧全正常，但**发不出去**（TX 计数一直在涨、RX 始终为 0），
		//     而且内核**不报** NETDEV WATCHDOG ⇒ TXWedge 那条自动复位路径救不了。
		// 真机验证（H29K + FM160-CN，2026-10-13）：
		//   不复位时：拨上后几十秒就发不出去；
		//   飞行模式 + AT+CFUN=1,1 之后再拨：60 秒内拨通，连续 150 秒 ping 0% 丢包。
		//
		// 代价：模组重启要 40~90 秒（AT 口/网卡会消失再回来），这一轮拨号会被拖长。
		// 所以配了 ResetBeforeDialCooldown 做节流；拨通后本来就不会再拉起（不会反复复位）。
		ResetBeforeDial bool `toml:"reset_before_dial"`
		// ResetBeforeDialCooldown 两次“拨号前复位”之间至少隔这么久（秒，默认 300）。
		// 0 = 不节流。
		ResetBeforeDialCooldown int `toml:"reset_before_dial_cooldown"`
		// ResetBeforeDialAfterFails 连续拨号失败够这么多次，才允许做一次“拨号前复位”（默认 5）。
		//
		// 【为什么要有这个阈值 —— 用户 2026-10-13 的质疑】AT+CFUN=1,1 是**整机重启**
		// （射频 + 固件 + USB 重新枚举，40~90 秒不可用），**每次拨号前都做不合理**：
		// 会把拨号周期拉到 90 秒以上，而且实测有“USB 断开后不回来”的风险；
		// AT+CFUN=4（飞行模式）更糟 —— 模组不重启但 AT 口不响应，SendAT 会卡好几分钟。
		// 所以它的定位改成**兜底**：前面的“轻量清场”是 PDP 释放
		//   （releaseHostDialDataContext = AT+CGACT=0,<cid>，秒级、不重启模组，每次拉起前都做），
		// 只有连着失败这么多次（说明承载真的卡住了）才升级到重启模组。
		// 0 = 每次拉起前都复位（老行为，不建议）。
		ResetBeforeDialAfterFails int `toml:"reset_before_dial_after_fails"`
		// WANIPv6 模组这一侧要不要用 IPv6（默认 true，页面上「拨号上网」那个开关写的就是它）。
		//
		// 关掉 = 只走 IPv4，四种拨号方式都按这个来：
		//   · QMI / MBIM：不给拨号器加 -6（不让它去要 v6 承载）
		//   · ECM / NCM / RNDIS：不建 modem<id>_6（dhcpv6 兄弟接口），已经建过的会被删掉
		// 打开时按各协议本来的做法走（见 README「IPv6」）。
		WANIPv6 bool `toml:"wan_ipv6"`
		// QMIV6Sibling QMI（主机侧拨号器）模式还要不要另建 **v6 兄弟接口** `modem<id>_6`
		// （proto=dhcpv6，绑同一张 wwan0）。**默认 true = 老行为**。
		//
		// 背景：QMI 下 wwan0 的 v4/v6 地址、默认路由、DNS 都是**主机侧拨号器**给的；
		// 这张兄弟接口是给 netifd / 局域网侧看的一份“v6 上联”（LAN 的 v6 对账、
		// ip6prefix 交给内网那一路都假定有这么张接口）。留着它就是老行为。
		// 把它设成 false 则 QMI 下一律不建这种兄弟接口（已经建过的会被收掉）——
		// 适合“只想让拨号器管 v6、不希望 netifd 再碰这张网卡”的环境。
		QMIV6Sibling bool `toml:"qmi_v6_sibling"`
		// LANIPv6 把模组拿到的 IPv6 前缀交给内网（默认 true）。
		// 模组（尤其 NCM/ECM）往往只在链路上发一个 /64（RA/SLAAC）**不做前缀委派（PD）**，
		// 这时 netifd 不知道能往内网发哪个前缀，内网设备就只有 ULA。做法是把那个 /64 写进
		// 上联接口的 `ip6prefix`（实测内网立刻拿到全局地址）。
		// 不想要内网走模组的 IPv6（比如已经用别的隧道）就关掉它。
		LANIPv6 bool `toml:"lan_ipv6"`
		// DialEnabled 「拨号总开关」：关掉后**所有拨号方式都不拨号**
		// （页面上「拨号上网 → 拨号开关」写的就是它，默认 true）。
		//
		// 关掉的语义（每一处都短路过，调用点见 DialEnabled 的注释）：
		//   · 自动组网循环整台模组跳过：不激活 PDP、不拉主机侧拨号器、不建 WAN 接口；
		//   · 主机侧拨号器（QMI）会被停掉（数据链路随之断）；
		//   · 手动「拨号」直接拒并说明原因，不再下发任何 AT。
		// 已经建过的 WAN 接口不动（只是拿不到地址了），要拆接口自己去网络设置里删。
		DialEnabled bool `toml:"dial_enabled"`
		// DialEnabledSet 配置文件里**是否显式写过** dial_enabled（运行时填，不参与 toml）。
		// 有它才能区分「没写（= 默认开）」与「显式写 false」—— 见 DialEnabled()。
		DialEnabledSet bool `toml:"-"`
		// AllowRoaming 「数据漫游」开关：关掉后**国际漫游时不用数据**（本地卡不受影响 ——
		// 本地网与同国异网漫游都照常上网）。页面在「高级配置 → 参数设置」，默认 **允许**
		// （不写配置就是允许）。判据 = 注册状态 + 卡归属（IMSI 前 3 位），不看厂商私有指令
		// ⇒ 所有模组通用，见 AllowRoaming() / roaming.go。
		AllowRoaming bool `toml:"allow_roaming"`
		// AllowRoamingSet 配置文件里**是否显式写过** allow_roaming（运行时填，不参与 toml）。
		// 有它才能区分「没写（= 默认允许）」与「显式写 false」—— 见 AllowRoaming()。
		AllowRoamingSet bool `toml:"-"`
	} `toml:"net"`
	// Dial 拨号行为开关（见 dial_engine.go / pdp_ims.go）。
	Dial struct {
		// ActivateIMS 拨号成功（或写完 APN）之后，是否顺手把模组的 **IMS 上下文**
		// （APN = ims，通常 cid=1）也激活起来。
		//
		// 为什么要它：IMS 上下文是 VoLTE / 走 IMS 的短信（5G SA 下常见）的先决条件，
		// 不少固件/卡出厂时它是**未激活**的，得手动 AT+CGACT=1,<ims cid>。
		// ⚠️ 失败/WARN**不影响**拨号结果（实测 FM350 直接回 ERROR：该固件不让手动激活 IMS），
		// 只在步骤与日志里如实写出来。不写默认 true；要关就显式写 false。
		ActivateIMS bool `toml:"activate_ims"`
		// ActivateIMSSet 配置文件里**是否显式写过** activate_ims（运行时填，不参与 toml）。
		// 有它才能区分「没写（= 默认开）」与「显式写 false」—— 见 ActivateIMSEnabled()。
		ActivateIMSSet bool `toml:"-"`
	} `toml:"dial"`
	Modem struct {
		EnableNRSignal bool `toml:"enable_nr_signal"`
		// TTYPerModem 每个模组占用的 ttyUSB 端口数。
		// 如移远模组常占 4 个端口（ttyUSB0-3），第二个模组从 ttyUSB4 开始。
		// 设置为 0 时退化为"按连续编号分组"（兼容旧逻辑）。
		TTYPerModem int `toml:"tty_per_modem"`
	} `toml:"modem"`

	// UI 调试台自身的访问控制
	UI struct {
		// SMSPasswordRequired 进「短信」页 / 调短信接口是否需要密码。
		// 不写配置时**默认 true**（带上保护），要关就显式写 false。
		SMSPasswordRequired bool `toml:"sms_password_required"`
		// SMSPassword 访问密码。不写 = 默认 "password"；显式写空串 = 不启用密码。
		SMSPassword string `toml:"sms_password"`
		// SMSPasswordSet 配置文件里是否显式写了 sms_password（运行时填，不参与 toml）
		SMSPasswordSet bool `toml:"-"`
		// RequireLuciLogin 是否要求"先登录路由器管理员账号"才给调接口（实现见 luciauth.go）。
		// **不写配置时默认开**（用户 2026-10-06 要求：「默认打开，没有配置也是打开」）；
		// 要关就显式写 false，或在 LuCI 的「服务控制」页把开关关掉（会写回这一行）。
		// 打开后：静态页与 /api/version 仍放行（未登录也能看到"请先登录"的提示与版本），
		// 受保护接口需要有效会话，ubus 不可用时拒绝访问。
		RequireLuciLogin bool `toml:"require_luci_login"`
		// RequireLuciLoginSet 配置文件里**是否显式写过** require_luci_login（运行时填，不参与 toml）。
		// 有它才能区分「没写（= 默认开）」与「显式写 false（= 关）」—— 见 Load()。
		RequireLuciLoginSet bool `toml:"-"`
		// UbusHTTP ubus 的 HTTP JSON-RPC 端点：登录/退出走它（密码走请求体，不出现在 ps 里）。
		// 留空 = 默认 "http://127.0.0.1/ubus"（= uhttpd 的 `ubus_prefix`）；
		// uhttpd 改过监听端口或前缀就在这里写全。
		UbusHTTP string `toml:"ubus_http"`
	} `toml:"ui"`

	// WeChat 微信机器人（腾讯 iLink Bot / ClawBot）：新短信转发到微信。
	// 协议实现见 wechat.go；配置值在这里，运行时状态（登录阶段/二维码/游标）在内存里。
	WeChat struct {
		// Enabled 是否把新短信推送到微信
		Enabled bool `toml:"enabled"`
		// BaseURL API 基地址（留空用官方默认；登录成功后服务端可能下发新地址）
		BaseURL string `toml:"base_url"`
		// BotToken 扫码登录后服务端下发的 Bot 凭据（请求头 Authorization: Bearer）
		BotToken string `toml:"bot_token"`
		// BotID 机器人账号（ilink_bot_id，形如 xxxx@im.bot）
		BotID string `toml:"bot_id"`
		// BotUserID 扫码登录的微信用户（ilink_user_id）
		BotUserID string `toml:"bot_user_id"`
		// ToUserID 推送目标（形如 xxxx@im.wechat）；留空则用“最近给机器人发过消息的人”
		ToUserID string `toml:"to_user_id"`
		// Template 消息模板，支持的占位符见 wechat.go
		Template string `toml:"template"`
		// Listen 是否长轮询收微信消息（用于学习推送目标；默认 true）
		Listen bool `toml:"listen"`
		// AllowedSenders 仅接受这些发送者（逗号分隔，子串匹配；留空 = 不限制）
		AllowedSenders string `toml:"allowed_senders"`
		// ListenSet 配置文件里是否显式写了 listen（运行时填，不参与 toml）
		ListenSet bool `toml:"-"`
	} `toml:"wechat"`
}

// Cfg 全局配置实例（进程启动时由 main 调用 Load 填充）
var Cfg Config

const (
	// DefaultWeChatBaseURL 微信 iLink Bot 官方 API 基地址
	DefaultWeChatBaseURL = "https://ilinkai.weixin.qq.com"
	// DefaultListenAddr Web 界面（设备中心）的默认监听地址：`[server] listen` 不写时用它。
	// ⚠️ 别退回“让 Addr 空着”：http.Server 的空 Addr 会落到 `:http` 也就是 80 端口。
	// 改端口就改 toml 的 `[server] listen`，改完 **必须重启服务**。
	DefaultListenAddr = "127.0.0.1:9527"
	// DefaultWeChatTemplate 新短信转发到微信时的默认文案。
	// 占位符：{modem} {number} {time} {content} {parts}
	// ⚠️ {parts} 只在长短信分条时非空（形如「（2/3）」），所以直接跟在标题后面。
	DefaultWeChatTemplate = "📩 新短信{parts}\n模组：{modem}\n号码：{number}\n时间：{time}\n内容：{content}"
)

// loadConfig 加载 TOML 配置文件
func Load() error {
	md, err := toml.DecodeFile(ConfigPath, &Cfg)
	if err != nil {
		return fmt.Errorf("load config %s failed: %w", ConfigPath, err)
	}
	// 短信访问密码：**不写配置也要保护上**（默认 password）。
	// 区分"没写"和"写了空串"只能靠解码元数据：
	//   没写           → required=true、密码回落默认 password
	//   sms_password="" → 显式关掉密码校验
	Cfg.UI.SMSPasswordSet = md.IsDefined("ui", "sms_password")
	if !md.IsDefined("ui", "sms_password_required") {
		Cfg.UI.SMSPasswordRequired = true
	}
	// 外层登录闸门（跟随路由器账号）：**不写配置也默认打开**。
	// 区分"没写"与"写了 false"：没写 → 打开；显式 false → 关闭
	// （LuCI「服务控制」页的开关写的就是这一行；device 上从旧版升上来时配置里根本没这一行，
	//   以前那版默认 false ⇒ “装了新版却没有登录框”，2026-10-06 用户报的正是这个）。
	Cfg.UI.RequireLuciLoginSet = md.IsDefined("ui", "require_luci_login")
	if !Cfg.UI.RequireLuciLoginSet {
		Cfg.UI.RequireLuciLogin = true
	}
	// 微信机器人：listen 不写默认开启（要能收指令 / 学习推送目标）；
	// base_url 与模板不写就用默认值。
	Cfg.WeChat.ListenSet = md.IsDefined("wechat", "listen")
	if !Cfg.WeChat.ListenSet {
		Cfg.WeChat.Listen = true
	}
	if Cfg.WeChat.BaseURL == "" {
		Cfg.WeChat.BaseURL = DefaultWeChatBaseURL
	}
	if Cfg.WeChat.Template == "" {
		Cfg.WeChat.Template = DefaultWeChatTemplate
	}
	// 默认值兜底
	// scan_interval <= 0 是合法值（关闭后台周期巡检），不做兜底覆盖
	if Cfg.Server.ScanInterval < 0 {
		Cfg.Server.ScanInterval = 0
	}
	// 速度采样默认 1 秒一次
	if Cfg.Server.TrafficInterval <= 0 {
		Cfg.Server.TrafficInterval = 1
	}
	if Cfg.Server.NetworkInterval <= 0 {
		Cfg.Server.NetworkInterval = 3
	}
	// Web 界面监听地址：默认 0.0.0.0:9527。
	// 改端口 = 改 toml 的 `[server] listen`，**改完必须重启服务**才生效。
	if Cfg.Server.Listen == "" {
		Cfg.Server.Listen = DefaultListenAddr
	}
	if Cfg.Serial.Baud <= 0 {
		Cfg.Serial.Baud = 115200
	}
	if Cfg.Serial.ATProbeTimeout <= 0 {
		// 4 秒（旧值 2 秒偏紧）：刚上电的模组应答可能慢到 2~3 秒，
		// 探测超时太短会把真正能用的 AT 口误判成"非 AT 口"。
		Cfg.Serial.ATProbeTimeout = 4 * time.Second
	}
	if Cfg.Serial.ProbeWarmup == 0 {
		Cfg.Serial.ProbeWarmup = 90 * time.Second
	}
	if Cfg.Serial.ProbeWarmup < 0 {
		Cfg.Serial.ProbeWarmup = 0 // 负值 = 关闭预热等待
	}
	if Cfg.Serial.CmdTimeout <= 0 {
		Cfg.Serial.CmdTimeout = 3 * time.Second
	}
	if Cfg.Serial.ReadStuckThreshold <= 0 {
		Cfg.Serial.ReadStuckThreshold = 30 * time.Second
	}
	// USB 模组驱动补绑：不写也算开（OpenWrt 上"插了模组却没有 ttyUSB"要靠它）
	if !md.IsDefined("serial", "usb_autobind") {
		Cfg.Serial.USBAutoBind = true
	}
	// SIM 热插拔：兜底轮询间隔不写默认 20s（写 0s / 负数 = 关掉检查，只靠 URC）；
	// ⚠️ 换卡后复位模组不写默认 **关**：只有用户显式写 hotswap_reset = true 才会动模组。
	// （早期版本默认开，结果"插卡抖动/模组刚重枚举"被误判成换卡 → 无限复位模块，真机事故。）
	if !md.IsDefined("sim", "hotswap_interval") {
		Cfg.SIM.HotswapInterval = 20 * time.Second
	}
	if Cfg.SIM.HotswapInterval < 0 {
		Cfg.SIM.HotswapInterval = 0
	}
	// 自动组网（OpenWrt）：不写也算开，两个开关都默认 true；
	// metric 默认 20（不抢路由器原有 WAN 的默认路由），防火墙区默认 wan。
	if !md.IsDefined("net", "auto_wan") {
		Cfg.Net.AutoWAN = true
	}
	if !md.IsDefined("net", "auto_dial") {
		Cfg.Net.AutoDial = true
	}
	if Cfg.Net.WANMetric <= 0 {
		Cfg.Net.WANMetric = 20
	}
	if strings.TrimSpace(Cfg.Net.WANZone) == "" {
		Cfg.Net.WANZone = "wan"
		if len(Cfg.Net.V6FixModels) == 0 {
			// 出厂默认：只有 RM500U 走"特别处理 IPv6"那套（用户 2026-10-08：改名/加型号都从配置走）
			Cfg.Net.V6FixModels = []string{"rm500u"}
		}
	}
	if !md.IsDefined("net", "host_dial") {
		Cfg.Net.HostDial = true
	}
	if !md.IsDefined("net", "reset_before_dial") {
		// ⚠️ **默认关**（2026-10-13 用户实测澄清）：`AT+CFUN=1,1`（重启模组）**会把模块搞掉线**
		// ——掉线之后连查都查不到，属于"用破坏性手段清场"，不划算；
		// `AT+CFUN=4`（飞行模式）则会让 AT 口不响应、SendAT 卡好几分钟。
		// 拨号前的清场因此改为**只动 PDP**：releaseHostDialDataContext
		// = `AT+CGACT=0,<cid>`（秒级、不动射频、不影响 USB），每次拉起拨号器前都会做。
		// 想强行开启这个兜底（连续失败多次后重启模组）就显式写 true。
		Cfg.Net.ResetBeforeDial = false
	}
	if !md.IsDefined("net", "reset_before_dial_cooldown") {
		// 节流：默认 5 分钟。拨通后不会再拉起，所以正常一天只复位几次。
		Cfg.Net.ResetBeforeDialCooldown = 300
	}
	if !md.IsDefined("net", "reset_before_dial_after_fails") {
		// 兜底阈值：连续失败 5 次才重启模组（前面先用轻量的 PDP 释放清场，见字段注释）。
		Cfg.Net.ResetBeforeDialAfterFails = 5
	}
	if !md.IsDefined("net", "wan_ipv6") {
		Cfg.Net.WANIPv6 = true
	}
	// QMI 的 v6 兄弟接口：不写配置就算开（老行为；要收掉就写 false）。
	// 同样必须用 IsDefined 判：bool 零值是 false，不加这一条会把“没写”当成“关掉”。
	// WAN 接口形态（用户 2026-10-13 要求）：默认 modem_pro —— 一张接口跑 v4+v6。
	// ParseWANMode 本身也能处理空串，这里显式写一次是为了让它在配置里"看得见"。
	if !md.IsDefined("net", "wan_mode") {
		Cfg.Net.WANMode = string(WANModeModemPro)
	}
	// QMIV6Sibling 自 2026-10-13 起被 wan_mode 取代（模式 2/3 才建 v6 兄弟接口）。
	// 字段与默认值**保留**只为兼容老配置文件不报错；实际决策见 modemV6SiblingWanted。
	if !md.IsDefined("net", "qmi_v6_sibling") {
		Cfg.Net.QMIV6Sibling = true
	}
	if !md.IsDefined("net", "lan_ipv6") {
		Cfg.Net.LANIPv6 = true
	}
	// 拨号总开关：不写配置就算开（页面上「拨号上网 → 拨号开关」，默认开）。
	// ⚠️ 必须用 IsDefined 判：bool 的零值是 false，不加这一条会把"没写"当成"关掉"，
	// 于是升级上来的设备一启动就永远不拨号了。
	// （DialEnabled() 也会看这个标志，两处取同一个口径。）
	Cfg.Net.DialEnabledSet = md.IsDefined("net", "dial_enabled")
	if !Cfg.Net.DialEnabledSet {
		Cfg.Net.DialEnabled = true
	}
	// 数据漫游：不写配置就算**允许**（「高级配置 → 参数设置 → 数据漫游」，默认开）。
	// ⚠️ 同 DialEnabled：bool 零值是 false，不加 IsDefined 这一层就会把"没写"当成"禁止漫游"，
	// 升级上来的设备一到漫游（包括国内异网漫游）就自己把数据掐了。
	// （AllowRoaming() 也会看这个标志，两处取同一个口径。）
	Cfg.Net.AllowRoamingSet = md.IsDefined("net", "allow_roaming")
	if !Cfg.Net.AllowRoamingSet {
		Cfg.Net.AllowRoaming = true
	}
	// 拨号时顺带激活 IMS 上下文：不写也算开（用户要求「APN 要自动激活对应的 PDP 和 ims」）。
	Cfg.Dial.ActivateIMSSet = md.IsDefined("dial", "activate_ims")
	if !Cfg.Dial.ActivateIMSSet {
		Cfg.Dial.ActivateIMS = true
	}
	if Cfg.SMS.SendQueueSize <= 0 {
		Cfg.SMS.SendQueueSize = 16
	}
	if Cfg.SMS.SMSMemory == "" {
		Cfg.SMS.SMSMemory = "SM"
	}
	if Cfg.Server.LogFile == "" {
		Cfg.Server.LogFile = defaultLogFile()
	}
	if Cfg.Server.PidFile == "" {
		Cfg.Server.PidFile = defaultPidFile()
	}
	return nil
}

// ConfigPath 配置文件路径（支持 -config 参数）
var ConfigPath = "toml"

// parseFlags 解析命令行参数（在 main 中调用；测试时不会调用）
func ParseFlags() {
	flag.StringVar(&ConfigPath, "config", "toml", "path to toml")
	flag.Parse()
}

// writePidFile 写入 PID 文件，防止重复启动
func WritePidFile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%d", os.Getpid())
	return err
}

func RemovePidFile(path string) {
	_ = os.Remove(path)
}

// ---- from: config/edit.go ----

//
// 页面上改「短信访问密码」要能持久化，最自然的落点就是 toml 本身
// （用户下次打开文件看到的就是当前值）。
//
// ⚠️ 为什么不是"用 toml 库重新编码整个文件"：会**丢掉全部注释** ——
// toml 里的注释是给用户看的说明书，不能被工具吃掉。
// 所以这里做**定向文本替换**：只在 [ui] 段里改/插指定的键，其它字节原样保留。
//
// 安全措施：
//   - 写之前先把原文件备份成 toml.bak（覆盖式，保留上一版）
//   - 原子写（先写 .tmp 再 rename），避免断电留下半个文件
//   - 只碰 [ui] 段；段不存在就追加，不猜别的段
// tomlQuote 把一个值编码成 TOML 基本字符串。
// 调用方已经限制了字符集（不允许 " 与 \），这里只做兜底转义。

func tomlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// setTomlKey 在 text 里设置 section 段内的 key = value（没有段/键就插入）。
// 返回新的文本内容。lineEnd 用于插入新行时保持原有换行风格。
//
// ⚠️ 实现要点（踩过的坑）：这里按 "\n" 切行，所以**行元素末尾可能带一个 "\r"**。
// 替换/插入的行必须只补 eol（CRLF 文件补 "\r"），**不能再补完整 lineEnd** ——
// 否则 Join("\n") 之后会变成 "\r\n\n"，文件里凭空多出空行。
func setTomlKey(text, section, key, value, lineEnd string) (string, error) {
	// 统一按 "\n" 切（每行可能带结尾的 \r，会被原样保留）
	lines := strings.Split(text, "\n")
	eol := ""
	if strings.HasSuffix(lineEnd, "\r\n") {
		eol = "\r"
	}
	inSection := false
	sectionAt := -1
	keyAt := -1
	curSection := ""

	for i, ln := range lines {
		body := strings.TrimSpace(strings.TrimSuffix(ln, "\r"))
		if strings.HasPrefix(body, "[") && strings.HasSuffix(body, "]") {
			curSection = strings.TrimSpace(body[1 : len(body)-1])
			if curSection == section {
				inSection = true
				sectionAt = i
			} else {
				inSection = false
			}
			continue
		}
		if !inSection || keyAt >= 0 {
			continue
		}
		// `key = ...` / `key=...` 都认，但不认 `somekey = ...`
		head := body
		if idx := strings.Index(body, "="); idx >= 0 {
			head = strings.TrimSpace(body[:idx])
		}
		if head == key {
			keyAt = i
		}
	}

	newLine := key + " = " + value + eol
	if keyAt >= 0 {
		lines[keyAt] = newLine
	} else if sectionAt >= 0 {
		// 插在该段的第一行后面
		out := make([]string, 0, len(lines)+1)
		out = append(out, lines[:sectionAt+1]...)
		out = append(out, newLine)
		out = append(out, lines[sectionAt+1:]...)
		lines = out
	} else {
		// 段不存在：追加在文件末尾（前面补一个空行）
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "["+section+"]", newLine)
	}
	return strings.Join(lines, "\n"), nil
}

// SetSMSMemory 把短信存储区（AT+CPMS 的 <mem1>）写回配置文件，并同步内存里的 Cfg。
//
// 只动 [sms] 段的 sms_memory 一个键：页面上切一次存储区，下次启动就不用再切了。
// （配置里本来就有这个键，但一直没有 UI 去改它 —— 切了不写回的话，重启就白切了。）
func SetSMSMemory(mem string) error {
	if ConfigPath == "" {
		return fmt.Errorf("配置路径为空，无法写回")
	}
	b, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("读配置失败: %w", err)
	}
	text := string(b)
	lineEnd := "\n"
	if strings.Contains(text, "\r\n") {
		lineEnd = "\r\n"
	}
	text, err = setTomlKey(text, "sms", "sms_memory", tomlQuote(mem), lineEnd)
	if err != nil {
		return err
	}
	// 备份上一版，再原子替换（与改密码同一套做法）
	_ = os.WriteFile(ConfigPath+".bak", b, 0o644)
	tmp := ConfigPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, ConfigPath); err != nil {
		return fmt.Errorf("替换配置文件失败: %w", err)
	}
	Cfg.SMS.SMSMemory = mem
	return nil
}

// SetWANIPv6 把「模组侧 IPv6」开关写回配置文件，并同步内存里的 Cfg（页面上切一下马上生效，
// 不用重启进程）。只动 [net] 段的 wan_ipv6 一个键。
//
// 为什么要有它：这个开关以前只能改配置文件 + 重启；页面上那个开关（「拨号上网」）要能即时生效。
// 写回之后**组网循环（≤15 秒）**会按新值处理：关掉时把已经建过的 v6 兄弟接口删掉、
// QMI 下一轮不再带 -6、MBIM 下一轮只写 ip。
func SetWANIPv6(on bool) error {
	if ConfigPath == "" {
		return fmt.Errorf("配置路径为空，无法写回")
	}
	b, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("读配置失败: %w", err)
	}
	text := string(b)
	lineEnd := "\n"
	if strings.Contains(text, "\r\n") {
		lineEnd = "\r\n"
	}
	val := "false"
	if on {
		val = "true"
	}
	text, err = setTomlKey(text, "net", "wan_ipv6", val, lineEnd)
	if err != nil {
		return err
	}
	// 备份上一版，再原子替换（与 SetSMSMemory 同一套做法）
	_ = os.WriteFile(ConfigPath+".bak", b, 0o644)
	tmp := ConfigPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, ConfigPath); err != nil {
		return fmt.Errorf("替换配置文件失败: %w", err)
	}
	Cfg.Net.WANIPv6 = on
	return nil
}

// SetWANMode 把「接口模式」写回配置文件（[net] wan_mode），并同步内存里的 Cfg
// （页面上切一下马上生效，不用重启进程）。只动 [net] 段的 wan_mode 一个键。
//
// 三种模式见 netprov_mode.go；真正动手的是组网循环（≤15 秒一轮）：
//
//	modem_pro → 主接口改成 modem_pro，并把多出来的 modem<id>_6 收掉、防火墙那段也清掉
//	dhcp      → 主接口改回 dhcp，并按需补一张 modem<id>_6（dhcpv6）
//	both      → 主接口 modem_pro + 额外保留一张 modem<id>_6
//
// ⚠️ 写回的值先过 ParseWANMode 归一化：认不出的输入不会写进文件（免得配置文件里留下
// 一个下次启动才发现的怪值）。
func SetWANMode(m WANMode) error {
	mode := ParseWANMode(string(m))
	if ConfigPath == "" {
		return fmt.Errorf("配置路径为空，无法写回")
	}
	b, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("读配置失败: %w", err)
	}
	text := string(b)
	lineEnd := "\n"
	if strings.Contains(text, "\r\n") {
		lineEnd = "\r\n"
	}
	text, err = setTomlKey(text, "net", "wan_mode", tomlQuote(mode.String()), lineEnd)
	if err != nil {
		return err
	}
	// 备份上一版，再原子替换（与 SetWANIPv6 同一套做法）
	_ = os.WriteFile(ConfigPath+".bak", b, 0o644)
	tmp := ConfigPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, ConfigPath); err != nil {
		return fmt.Errorf("替换配置文件失败: %w", err)
	}
	Cfg.Net.WANMode = mode.String()
	return nil
}

// DialEnabled 「拨号总开关」：[net] dial_enabled（默认开）。
//
// 关掉后**所有拨号方式都不拨号**，四处都按它短路：
//
//	Modem.Dial（手动「拨号」/「重拨」）→ 直接拒，不下发任何 AT
//	autoWANForModem（自动组网循环）    → 整台模组跳过，并把在跑的主机侧拨号器收掉
//	hostDialStopReason（拨号器看护）    → 报"总开关已关"，幂等地把它停着
//	SetDialEnabled(false)             → 立刻停所有主机侧拨号器（不等下一轮循环）
//
// 为什么要有：现场调试/排错时不想让程序自己拨号（只想看注册、信号、短信），
// 或者一台设备上要用别的拨号器接管数据面 —— 以前只能把 [net] 的三个开关一个个关掉。
func DialEnabled() bool {
	// ⚠️ 与 ActivateIMSEnabled 同一套写法：**没显式写过配置就算开**。
	// 不能只看 Cfg.Net.DialEnabled 的零值 —— 单元测试（以及任何"配置里没写这个键"的场合）
	// 里它是 false，那样所有拨号都会被误封（升级上来的设备也会一启动就不拨号）。
	if Cfg.Net.DialEnabledSet {
		return Cfg.Net.DialEnabled
	}
	return true
}

// SetDialEnabled 把「拨号总开关」写回配置文件（[net] dial_enabled），并同步内存里的 Cfg。
//
// 页面上切一下马上生效：组网循环 ≤15 秒按新值走；**关掉时立刻停掉正在跑的主机侧拨号器**
// （QMI 模式下那是唯一还活着的数据面 —— 不然"关了还在拨"的观感很差）。
func SetDialEnabled(on bool) error {
	if ConfigPath == "" {
		return fmt.Errorf("配置路径为空，无法写回")
	}
	b, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("读配置失败: %w", err)
	}
	text := string(b)
	lineEnd := "\n"
	if strings.Contains(text, "\r\n") {
		lineEnd = "\r\n"
	}
	val := "false"
	if on {
		val = "true"
	}
	text, err = setTomlKey(text, "net", "dial_enabled", val, lineEnd)
	if err != nil {
		return err
	}
	// 备份上一版，再原子替换（与 SetWANIPv6 同一套做法）
	_ = os.WriteFile(ConfigPath+".bak", b, 0o644)
	tmp := ConfigPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, ConfigPath); err != nil {
		return fmt.Errorf("替换配置文件失败: %w", err)
	}
	Cfg.Net.DialEnabled = on
	Cfg.Net.DialEnabledSet = true // 写回之后就是"显式写过"了
	if !on {
		// 立刻收掉所有主机侧拨号器（幂等：没在跑的返回空串）
		OnDialDisabled("拨号总开关已关闭（页面「拨号开关」）")
	}
	return nil
}

// AllowRoaming 「数据漫游」开关：[net] allow_roaming（默认**允许**）。
//
// 语义同手机上的「数据漫游」：关掉后**国际漫游时不用数据**。⭐ **本地卡不受影响**
// （用户 2026-10-06 明确要求："漫游是为了国外卡支持，本地卡是不影响的"）：
//
//	本地网注册（`<stat> == 1`）       → 不介入，照常上网
//	同国异网漫游（MCC 相同、MNC 不同）→ 不介入（国内运营商之间的共享/异网漫游，本地卡也会这样）
//	国际漫游（卡归属 MCC ≠ 注册网 MCC）→ 开关关着就不拨号 / 把拨号器收掉
//	判不出国别（读不到 IMSI/PLMN）    → 不介入（宁可该拦没拦，也不误断本地卡）
//
// 判据 = 3GPP 注册状态 + 卡归属（IMSI 前 3 位），**不看厂商私有指令** ⇒ 所有模组通用，
// 详见 module/kernel/roaming.go。三处按它短路：
//
//	autoWANForModem（自动组网循环）  → 国际漫游中整台模组这一轮不拨号，并把在跑的主机侧拨号器收掉
//	hostDialStopReason（拨号器看护） → 报"国际漫游中且已关闭数据漫游"，幂等地把它停着
//	SetAllowRoaming(false)          → 加上 API 那层的 stopHostDialIfRoaming：立刻停掉**正在国际漫游**那几台
//
// 为什么不做成"给模组发厂商 AT 指令"：AT 侧没有标准漫游开关，各家私有写法取值语义互相矛盾，
// 详见 module/kernel/roaming.go 顶部的说明。
func AllowRoaming() bool {
	// ⚠️ 与 DialEnabled 同一套写法：**没显式写过配置就算允许**。
	// 不能只看 Cfg.Net.AllowRoaming 的零值 —— 单元测试（以及任何"配置里没写这个键"的场合）
	// 里它是 false，那样一到漫游就断网（升级上来的设备同样会被误封）。
	if Cfg.Net.AllowRoamingSet {
		return Cfg.Net.AllowRoaming
	}
	return true
}

// SetAllowRoaming 把「数据漫游」开关写回配置文件（[net] allow_roaming），并同步内存里的 Cfg。
//
// 只写配置，不动任何模组（写错也不会发 AT）。切回"允许"时不用特意做什么：下一轮组网循环
// 自己会把数据面重新拉起来；切到"禁止"时的**立刻生效**在 API 那层做
// （apiRoamSet → stopHostDialIfRoaming，因为那层才拿得到 ModemManager）。
func SetAllowRoaming(on bool) error {
	if ConfigPath == "" {
		return fmt.Errorf("配置路径为空，无法写回")
	}
	b, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("读配置失败: %w", err)
	}
	text := string(b)
	lineEnd := "\n"
	if strings.Contains(text, "\r\n") {
		lineEnd = "\r\n"
	}
	val := "false"
	if on {
		val = "true"
	}
	text, err = setTomlKey(text, "net", "allow_roaming", val, lineEnd)
	if err != nil {
		return err
	}
	// 备份上一版，再原子替换（与 SetDialEnabled 同一套做法）
	_ = os.WriteFile(ConfigPath+".bak", b, 0o644)
	tmp := ConfigPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, ConfigPath); err != nil {
		return fmt.Errorf("替换配置文件失败: %w", err)
	}
	Cfg.Net.AllowRoaming = on
	Cfg.Net.AllowRoamingSet = true // 写回之后就是"显式写过"了
	return nil
}

// SetUISMSAuth 把短信访问密码 / 是否启用写回配置文件，并同步内存里的 Cfg。
//
// 只动 [ui] 段的 sms_password 与 sms_password_required 两个键。
func SetUISMSAuth(password string, required bool) error {
	if ConfigPath == "" {
		return fmt.Errorf("配置路径为空，无法写回")
	}
	b, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("读配置失败: %w", err)
	}
	text := string(b)
	lineEnd := "\n"
	if strings.Contains(text, "\r\n") {
		lineEnd = "\r\n"
	}

	text, err = setTomlKey(text, "ui", "sms_password", tomlQuote(password), lineEnd)
	if err != nil {
		return err
	}
	reqVal := "false"
	if required {
		reqVal = "true"
	}
	text, err = setTomlKey(text, "ui", "sms_password_required", reqVal, lineEnd)
	if err != nil {
		return err
	}

	// 备份上一版（覆盖式：只保留最近一次修改前的状态）
	_ = os.WriteFile(ConfigPath+".bak", b, 0o644)

	tmp := ConfigPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, ConfigPath); err != nil {
		return fmt.Errorf("替换配置文件失败: %w", err)
	}

	// 内存里同步生效（不必重启进程）
	Cfg.UI.SMSPassword = password
	Cfg.UI.SMSPasswordSet = true
	Cfg.UI.SMSPasswordRequired = required
	return nil
}

// SetUILogin 把「是否要求先登录路由器账号（LuCI 那套）」写回配置文件，并同步内存里的 Cfg。
//
// 只动 [ui] 段的 require_luci_login 一个键（段/键不存在就插入 ——
// 设备上从旧版升上来的 toml 里往往根本没有这一行）。
//
// 与短信密码同一套做法：定向文本替换（保留注释）+ 备份 + 原子替换。
// 闸门（LuciAuthEnabled）是每次请求现读 Cfg 的，所以**内存一改就立刻生效**；
// LuCI 的「服务控制」页保存后会顺手重启一次服务，只是让进程与文件完全一致。
func SetUILogin(required bool) error {
	if ConfigPath == "" {
		return fmt.Errorf("配置路径为空，无法写回")
	}
	b, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("读配置失败: %w", err)
	}
	text := string(b)
	lineEnd := "\n"
	if strings.Contains(text, "\r\n") {
		lineEnd = "\r\n"
	}

	text, err = setTomlKey(text, "ui", "require_luci_login", tomlBool(required), lineEnd)
	if err != nil {
		return err
	}

	// 备份上一版（覆盖式：只保留最近一次修改前的状态）
	_ = os.WriteFile(ConfigPath+".bak", b, 0o644)

	tmp := ConfigPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, ConfigPath); err != nil {
		return fmt.Errorf("替换配置文件失败: %w", err)
	}

	// 内存里同步生效（不必重启进程）
	Cfg.UI.RequireLuciLogin = required
	Cfg.UI.RequireLuciLoginSet = true
	return nil
}

// ====================== 微信机器人（[wechat] 段） ======================

// WeChatSettings 要写回 [wechat] 段的配置（值都是"给用户看的当前值"）。
type WeChatSettings struct {
	Enabled   bool
	BaseURL   string
	BotToken  string
	BotID     string
	BotUserID string
	ToUserID  string
	Template  string
	Listen    bool
	Allowed   string
}

// tomlQuoteText 把一个可能带换行的文本编码成 TOML 基本字符串
// （TOML 里字符串不能跨行，所以换行要写成 \n）。
func tomlQuoteText(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\r\n", `\n`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return `"` + s + `"`
}

func tomlBool(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// SetWeChat 把微信机器人配置写回配置文件（[wechat] 段），并同步内存里的 Cfg。
//
// 与改密码同一套做法：定向文本替换（保留注释）+ 备份 + 原子替换。
// 段不存在时会追加，所以老配置文件也能用。
func SetWeChat(s WeChatSettings) error {
	if ConfigPath == "" {
		return fmt.Errorf("配置路径为空，无法写回")
	}
	b, err := os.ReadFile(ConfigPath)
	if err != nil {
		return fmt.Errorf("读配置失败: %w", err)
	}
	text := string(b)
	lineEnd := "\n"
	if strings.Contains(text, "\r\n") {
		lineEnd = "\r\n"
	}

	kv := [][2]string{
		{"enabled", tomlBool(s.Enabled)},
		{"base_url", tomlQuote(s.BaseURL)},
		{"bot_token", tomlQuote(s.BotToken)},
		{"bot_id", tomlQuote(s.BotID)},
		{"bot_user_id", tomlQuote(s.BotUserID)},
		{"to_user_id", tomlQuote(s.ToUserID)},
		{"template", tomlQuoteText(s.Template)},
		{"listen", tomlBool(s.Listen)},
		{"allowed_senders", tomlQuote(s.Allowed)},
	}
	for _, item := range kv {
		text, err = setTomlKey(text, "wechat", item[0], item[1], lineEnd)
		if err != nil {
			return err
		}
	}

	// 备份上一版（覆盖式：只保留最近一次修改前的状态）
	_ = os.WriteFile(ConfigPath+".bak", b, 0o644)

	tmp := ConfigPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return fmt.Errorf("写临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, ConfigPath); err != nil {
		return fmt.Errorf("替换配置文件失败: %w", err)
	}

	// 内存里同步生效（不必重启进程）
	Cfg.WeChat.Enabled = s.Enabled
	Cfg.WeChat.BaseURL = s.BaseURL
	Cfg.WeChat.BotToken = s.BotToken
	Cfg.WeChat.BotID = s.BotID
	Cfg.WeChat.BotUserID = s.BotUserID
	Cfg.WeChat.ToUserID = s.ToUserID
	Cfg.WeChat.Template = s.Template
	Cfg.WeChat.Listen = s.Listen
	Cfg.WeChat.ListenSet = true
	Cfg.WeChat.AllowedSenders = s.Allowed
	return nil
}

// ---- from: logging/logging.go ----

// Package logging 提供进程级日志入口。
//
// 各包想保留 `logger.Info(...)` 这种顺手写法，只要写一行：
//
//	var logger = logging.Logger
//
// 这是**无状态门面**（值类型、零值即可用）：真正的 *zap.Logger 由 main 读完配置后
// 通过 Init 设置，门面每次调用现取再转发。
// 之所以不直接导出一个 *zap.Logger 变量：包级变量初始化时它还是 nil，
// 各包会拿到 nil 而不是后来创建的实例。

var underlying atomic.Pointer[zap.Logger]

// L 取当前日志器；尚未初始化时返回 no-op 日志器（启动早期调用也不会 panic）
func L() *zap.Logger {
	if l := underlying.Load(); l != nil {
		return l
	}
	return zap.NewNop()
}

// Init 初始化日志：控制台 + 日志文件双输出。
// 日志文件打不开时退化为只写 stdout（不阻断启动）。
func Init(logFile, level string) {
	lv := zap.InfoLevel
	if level == "debug" {
		lv = zap.DebugLevel
	}
	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderCfg.EncodeLevel = zapcore.CapitalLevelEncoder

	var kernel zapcore.Core
	fileWriter, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Printf("cannot open log file, fallback to stdout only: %v\n", err)
		kernel = zapcore.NewCore(zapcore.NewConsoleEncoder(encoderCfg), os.Stdout, lv)
	} else {
		kernel = zapcore.NewTee(
			zapcore.NewCore(zapcore.NewConsoleEncoder(encoderCfg), os.Stdout, lv),
			zapcore.NewCore(zapcore.NewJSONEncoder(encoderCfg), zapcore.AddSync(fileWriter), lv),
		)
	}
	underlying.Store(zap.New(kernel))
}

// Proxy 日志门面：转发给 L()
type Proxy struct{}

func (Proxy) Debug(msg string, fields ...zap.Field) { L().Debug(msg, fields...) }
func (Proxy) Info(msg string, fields ...zap.Field)  { L().Info(msg, fields...) }
func (Proxy) Warn(msg string, fields ...zap.Field)  { L().Warn(msg, fields...) }
func (Proxy) Error(msg string, fields ...zap.Field) { L().Error(msg, fields...) }
func (Proxy) Fatal(msg string, fields ...zap.Field) { L().Fatal(msg, fields...) }
func (Proxy) Sync() error                           { return L().Sync() }

// Logger 各包挂门面实例用（无状态，值拷贝安全）
var Logger Proxy

// ---- from: logging/truncate.go ----

// Package logging：日志门面。这里再放一个与日志展示相关的小工具。
// Truncate 截断过长文本（日志/错误信息展示用），超长时补省略号。
//
// 原先叫 wechat.go 段里的 truncateLog（微信协议错误信息用），
// 现在微信与 iLink 的代码都在 wechat.go 段，这里当公共小工具；旧名字保留别名。

func Truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// ---- from: logfile/logfile.go ----

//
// 调试台把日志写在 server.log_file 里，排查问题时要能在页面上直接看，
// 不必登录到机器上 tail。
//
// 实现要点：**只读文件尾部**。日志可能有几十 MB，整份读进来会吃掉内存
// （还会和串口读循环抢 IO），所以按"定位到末尾前 N KB"的方式取最后若干行。
// LogTailBytes 每次最多从文件末尾回读多少字节（约够 2000 行调试日志）

const LogTailBytes = 512 * 1024

// tailLogFile 读日志文件最后 maxLines 行。maxLines<=0 时用 200，上限 5000。
// 文件不存在或为空返回空切片（不算错误 —— 刚启动还没写日志是正常的）。
func TailFile(path string, maxLines int) ([]string, error) {
	if maxLines <= 0 {
		maxLines = 200
	}
	if maxLines > 5000 {
		maxLines = 5000
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	offset := int64(0)
	if size > LogTailBytes {
		offset = size - LogTailBytes
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}

	// 从窗口起点开始按行读，只保留最后 maxLines 行（环形丢弃，避免整段驻留）
	ring := make([]string, 0, maxLines)
	// 截断窗口时首行大概率是被切断的半行：跳过它，而不是读完后丢 ring[0]
	// （窗口里的行数常多于 maxLines，读完后丢第一个会白丢一条**完整**日志，
	//   表现就是界面写「最后 200 行」却只显示 199 行）
	skipPartial := offset > 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 日志行可能很长（带原始 AT 报文）
	for sc.Scan() {
		if skipPartial {
			skipPartial = false
			continue
		}
		line := strings.TrimRight(sc.Text(), "\r")
		if len(ring) < maxLines {
			ring = append(ring, line)
			continue
		}
		copy(ring, ring[1:])
		ring[len(ring)-1] = line
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return ring, nil
}

// ====================== 运行日志推送 ======================
//
// 日志是"一直在写"的数据，靠前端 5 秒轮询看着就不像实时的（而且白跑 HTTP）。
// 这里直接 tail 文件，把**新增的整行**推给所有在线页面；没有客户端时整轮跳过，
// 一个字节都不读 —— 和其它推送一个原则：有人在看才干活。

const (
	// logPushInterval 检查间隔：日志是给人看的，秒级足够
	logPushInterval = time.Second
	// LogPushMaxBytes 单次最多推多少字节（长时间没人看之后别一次推巨量）
	LogPushMaxBytes = 64 * 1024
)

// NewPusher 创建增量读取器（首次读取只建基线，不返回历史行）
func NewPusher(path string) *Pusher { return &Pusher{Path: path} }

// LogTailSig 记住 offset 之前的一小段字节，用来识别文件被轮转/截断 ——
// 只看"文件变小了"是不够的：日志被 truncate 后又被写长，长度可能反而超过旧 offset。
const LogTailSig = 256

// Pusher 记住日志文件读到哪儿了，只把之后的新行推出去
type Pusher struct {
	Path   string
	offset int64
	ready  bool   // 是否已经有基线（首次只记位置，历史行由页面自己拉）
	tail   []byte // offset 之前最后 LogTailSig 字节（轮转检测用）
}

// tailOK 校验 offset 之前那段字节是否还在原处：不在就说明文件换了一份（轮转/截断）
func (p *Pusher) tailOK(f *os.File) bool {
	if p.offset <= 0 || len(p.tail) == 0 {
		return true
	}
	start := p.offset - int64(len(p.tail))
	if start < 0 {
		return true
	}
	buf := make([]byte, len(p.tail))
	if _, err := f.ReadAt(buf, start); err != nil {
		return true // 读不到就按"没变"处理，别乱重置
	}
	return bytes.Equal(buf, p.tail)
}

// rememberTail 记下已消费内容末尾的一小段，供下次 tailOK 校验
func (p *Pusher) rememberTail(consumed []byte) {
	n := len(consumed)
	if n > LogTailSig {
		n = LogTailSig
	}
	p.tail = append(p.tail[:0], consumed[len(consumed)-n:]...)
}

// readNew 读一次新增的**完整行**（末尾不完整的部分留到下一轮），并推进 offset。
// 返回 nil 表示"没有可推的东西"（首次建基线 / 没有新增 / 只有半行 / 落后太多）。
func (p *Pusher) ReadNew() []string {
	f, err := os.Open(p.Path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	size := st.Size()
	if !p.ready {
		// 首次只建基线：进页面时前端会自己拉一次尾部，不需要把历史补推一遍
		p.offset, p.ready = size, true
		p.tail = p.tail[:0]
		return nil
	}
	if size == p.offset {
		return nil
	}
	if size < p.offset || !p.tailOK(f) { // 轮转 / 截断：整份重来
		p.offset, p.tail = 0, nil
	}
	// 落后太多时只读最后一段；但窗口是从中间切进来的，首行可能是半行 → 丢掉它
	start, truncated := p.offset, false
	if size-start > LogPushMaxBytes {
		start, truncated = size-LogPushMaxBytes, true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, LogPushMaxBytes))
	if err != nil {
		return nil
	}
	if truncated {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return nil
		}
		data = data[i+1:]
	}
	// 只推**完整行**：末尾没有换行的部分留到下一轮（否则页面上会出现半行 JSON），
	// 也不推进 offset，让它下次接着读。
	lastNL := bytes.LastIndexByte(data, '\n')
	if lastNL < 0 {
		return nil
	}
	consumed := data[:lastNL+1]
	body := strings.TrimSuffix(string(consumed), "\n")
	body = strings.TrimSuffix(body, "\r")
	p.offset = start + int64(lastNL+1)
	p.rememberTail(consumed)
	if body == "" {
		return nil
	}
	raw := strings.Split(body, "\n")
	rows := make([]string, 0, len(raw))
	for _, l := range raw {
		rows = append(rows, strings.TrimRight(l, "\r"))
	}
	return rows
}
