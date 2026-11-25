package unisoc

import (
	"modemd/module/atpreset"
)

// ====================== 展锐 Unisoc（中兴 F50 / UMS9620） ======================
//
// ⚠️ 这份清单有三处**和其他厂商不一样**的地方，都是真机实测得来的：
//  1. **不支持 ATI**（回 `+CME ERROR: 4`），所以型号/固件要看 `AT+CGMR`；
//  2. 本机默认字符集是 **HEX**（`AT+CSCS?` → `"HEX"`），`AT+CSCA?` 会回 hex 串，
//     USSD 代码也必须 hex —— 想按人看的方式读，先 `AT+CSCS="GSM"`；
//  3. 短信**必须 PDU 模式**（文本模式下字母型发件人会乱码、长短信也分不出段）。
//
// 锁网是展锐私有的 `AT+SP*` 族。**组网模式有可写路径**（2026-10-06 取证）：
// 回读 `AT+SPRAT?`（唯一能回读的锁网项），写入走 **5G 开关 `AT+SP5GRAN=0/1`**
// （实测：=0 后 NR 消失、落到 LTE；=1 恢复 5G+4G）—— 界面上的「组网模式」页就是这两档。
// 小区锁定则**只有"解除"取证了**：`AT+SPCELLLOCK=0,0,0` 实测回 `+SPCELLLOCK:0,0,0,0` 并把锁清掉。
// 但 `SPCOMMLOCKBAND?`/`SPCOMMLOCKFRQ?`/`SPCELLLOCK?` 状态回读全是 `+CME ERROR: 21`，
// 锁**目标值**的参数语义也**没有取证**（实测写 1,0,3,140 只存下第 1 个参数、
// 写 1,1,0,140 只回 OK 且无 URC），所以这几条**只列"查参数形式"的 `=?`**，
// 具体取值由用户按 `=?` 的回报自己填 —— 不臆造，也不替用户瞎锁网。
// 连"写"的模板也不给：那会把一个未验证的参数写法当成可用写法推荐出去。
//
// ⚠️⚠️ **`AT+SPCOMMLOCKBAND` 带参数执行很危险（2026-10-06 真机事故）**：
//
//	AT+SPCOMMLOCKBAND=1,12,0,0,0,0,0 → 固件回 OK（接受了！）但把频段配置清空，
//	                                     立刻搜不到网（+CEREG: 2,0 / +COPS: 0），
//	                                     `=0,<码>,...` 与 `=1,12,<频段号>,...` 全被拒（+CME ERROR: 0）；
//	恢复方法：`AT+CFUN=0` → 等 5 秒 → `AT+CFUN=1`（实测有效；`AT+SFUN=4` 重启模组**无效**）。
//	⇒ 包内代码对锁频段只用 `AT^SYSCFGEX`（见下），不碰这一族。
//
// ⭐ **锁频段的读写走 `AT^SYSCFGEX`（2026-10-06 取证）**：
//
//	读：`AT^SYSCFGEX?` → `^SYSCFGEX: "00",2000000400000,0,2,1e200000095`，
//	    第 5 个字段是 **LTE 频段位图**（bit(频段-1)＝LTE B<n>）；第 2 个是 2G/3G 频带码表
//	    （与鼎桥同型：2000000400380 = GSM900/1800+WCDMA900/2100），**不是**频段位图；
//	写：`AT^SYSCFGEX="<组网模式>",<2G/3G码>,<漫游>,<服务域>,<LTE位图>`（其余字段回读后原样带回去）；
//	能力：`AT^SYSCFGEX=?` 里固件自己列了 LTE BC 名单（实测 BC1/3/5/8/34/38/39/40/41）；
//	交叉验证：`AT+SPBANDCTRL=0,0,0,0` 按 5 组回显频段（组 4 = LTE-TDD、组 5 = LTE-FDD，两份名单逐字一致）。
//
// ⚠️⚠️ **永久拉黑（2026-10-05/06 两次真机事故，各把模组弄挂十几分钟）**：
//
//	· `AT+SPENGMD`（任何 `type >= 1`，补齐 7 个参数也一样）—— 一发就堵死 AT 队列，
//	  第二次甚至连厂商数据面一起掉（`sipa_eth0` 掉地址、外网不通）；
//	· 带参数的 `AT+SPFREQSCAN`（只有 `=?` 是安全的）。
//	它们**绝不能出现在预设里**（用户点一下就会把模组弄挂），`TestNoWedgingPresets` 钉着这条。
func ATPresets() []atpreset.ATPreset {
	return []atpreset.ATPreset{
		atpreset.Of("展锐（本模块）", "AT+CGMR", "平台/硬件版本（本模组不支持 ATI，发 ATI 会回 +CME ERROR: 4）"),
		atpreset.Of("展锐（本模块）", "AT+CGMI", "模组厂商（本机回 Spreadtrum Communication CO.）"),
		atpreset.Of("展锐（本模块）", "AT+CGCONTRDP=1", "当前激活承载：APN + 本机 IP + 掩码 + DNS（必须带 cid）"),
		atpreset.Of("展锐（本模块）", "AT+CGCONTRDP=?", "本机支持的 cid 列表（实测 (1,11)：1 = 数据、11 = IMS）"),
		atpreset.Of("展锐（本模块）", "AT+SPIMEI?", "展锐原生 IMEI 回读（改 IMEI 走页面上的「IMEI」面板）"),
		atpreset.Of("展锐（本模块）", "AT+SPPHYCHINFO?",
			"频点 / 带宽 / PCI / 频段（实测：5G 回 11,1,627264,0,100,100,\"1,11\",521,78 ＝ NR-ARFCN 627264、band 78、100MHz；仅 4G 回 13,1,1850,19850,5,5,\"1,11\",140,3 ＝ EARFCN 1850、band 3）"),
		atpreset.Of("展锐（本模块）", "AT+SPMEID?", "MEID（实测 AA00000203EDE3）"),
		atpreset.Of("展锐（本模块）", "AT+CGDCONT?", "PDP 上下文（展锐的应答不带引号）"),
		atpreset.Of("展锐（本模块）", "AT+C5GQOSRDP",
			"签约速率 AMBR + QCI（3GPP 5GS QoS，每条承载一行；实测 cid 1=数据 1000/1000 Mbps、5QI=9，cid 11=IMS 300/75、5QI=5）"),
		atpreset.Of("展锐（本模块）", "AT+CCLK?", "模组时钟（本机出厂未校时；页面「校时」按钮走 AT+CCLK= 写入）"),
		atpreset.Of("展锐（本模块）", "AT+COPS=?", "扫网：列出可见运营商（耗时较长，会短暂影响业务）"),
		atpreset.Of("短信（展锐·PDU）", "AT+CMGF=0", "切 PDU 模式（本机默认就是 0，必须用它）"),
		atpreset.Of("短信（展锐·PDU）", "AT+CMGL=4", "读全部短信（PDU 模式，4 = 全部）"),
		atpreset.Of("短信（展锐·PDU）", "AT+CPMS?", "短信存储用量（本机实测 SM 区）"),
		atpreset.Of("短信（展锐·PDU）", "AT+CSCA?", "短信中心号码（CSCS 为 HEX 时回的是 hex 串）"),
		atpreset.Of("短信（展锐·PDU）", `AT+CSCS="GSM"`, "切回 GSM 字符集（这样 CSCA/USSD 才是可读文本）"),
		atpreset.Of("组网模式（展锐）", "AT+SPRAT?", "读组网模式（本机唯一能回读的锁网项；实测 LTE 32 = 已开 5G、LTE 16 = 仅 4G）"),
		atpreset.Of("组网模式（展锐）", "AT+SP5GRAN?", "读 5G 开关：1 = 5G+4G、0 = 仅 4G（本机唯一可写的组网模式旋钮）"),
		atpreset.Of("组网模式（展锐）", "AT+SP5GRAN=?", "5G 开关的参数形式（实测 (0-1)）"),
		atpreset.Of("组网模式（展锐）", "AT+SP5GRAN=0", "关闭 5G（仅 4G）。实测：NR 消失、落到 LTE，AT+SPRAT? 从 LTE 32 变 LTE 16"),
		atpreset.Of("组网模式（展锐）", "AT+SP5GRAN=1", "开启 5G（5G+4G，默认）。要恢复 5G 就发这一条"),
		atpreset.Of("锁网（展锐 SP*·参数未取证）", "AT+SPCOMMLOCKBAND=?",
			"锁频段：参数形式（7 个）。⚠️ 带参数执行实测会把频段配置清空导致掉网（=1,12,0,0,0,0,0 回 OK 但搜不到网），包内一律不自动发；掉网后用 AT+CFUN=0 再 AT+CFUN=1 可恢复"),
		atpreset.Of("锁网（展锐 SP*·参数未取证）", "AT+SPCOMMLOCKFRQ=?",
			"锁频点：参数形式（9 个）。实测 =2,12,0 被接受（回 OK）但无回显，含义无法确认"),
		atpreset.Of("锁网（展锐 SP*·参数未取证）", "AT+SPCELLLOCK=?",
			"锁基站：参数形式（4 个）。实测 =0,0,0 是清除（回 +SPCELLLOCK:0,0,0,0），=1,0,3,140 只存下第 1 个参数"),
		atpreset.Of("锁网（展锐 SP*·已实测可用）", "AT+SPCELLLOCK=0,0,0",
			"解除小区锁定（实测可用：回 +SPCELLLOCK:0,0,0,0，不影响驻网）。自己试锁网之后靠它把模组拉回来"),
		atpreset.Of("锁频段（展锐·已实测可用）", "AT^SYSCFGEX?",
			"读锁频段/组网配置：第 5 个字段是 LTE 频段位图（bit(频段-1)；实测 1e200000095 = BC1/3/5/8/34/38/39/40/41），第 2 个是 2G/3G 频带码"),
		atpreset.Of("锁频段（展锐·已实测可用）", "AT^SYSCFGEX=?",
			"本机声明的 LTE 频段名单（实测 BC1/BC3/BC5/BC8/BC34/BC38/BC39/BC40/BC41）—— 页面的「设备能力」列就是它"),
		atpreset.Of("锁频段（展锐·已实测可用）", "AT+SPBANDCTRL=0,0,0,0",
			"按 5 组回显频段配置（只读形态；实测 1=GSM、2=TD-SCDMA、3=WCDMA、4=LTE-TDD、5=LTE-FDD）"),
		atpreset.Of("锁网（展锐 SP*·参数未取证）", "AT+SPLBAND=?", "锁频段（位图写法，6 个参数）"),
		atpreset.Of("锁网（展锐 SP*·参数未取证）", "AT+SPBANDCTRL=?", "频段控制（4 个参数）"),
		atpreset.Of("锁网（展锐 SP*·参数未取证）", "AT+SPFREQSCAN=?", "频点扫描的参数形式（12 个参数）。只发这一条（=?）；带参数执行会堵死模组 AT 队列，要重启才恢复"),
		// 2026-10-06 从固件应答前缀表里新挖出来的候选（这几条本来不在清单里）。
		// ⚠️ 仍然**只列 `=?`**：执行形式没取证，而 `AT+SPNCELLSCAN` 是真扫描（可能短暂掉线）。
		atpreset.Of("展锐·信道/邻区（未取证·只列 =?）", "AT+SPNRCHANNEL=?",
			"NR 信道上报开关（参数 0/1）。固件里另有 +SPNRCHANNELU 上报，带 5 个十六进制值（疑似信道/PCI）"),
		atpreset.Of("展锐·信道/邻区（未取证·只列 =?）", "AT+SPNCELLSCAN=?",
			"邻区扫描（参数 0/1/2/16/19）。执行形式会触发扫描、可能短暂掉线，请在你方便时再试"),
		atpreset.Of("展锐·信道/邻区（未取证·只列 =?）", "AT+SPQ4GNCELL=?", "4G 邻区（应答格式 +SPQ4GNCELL: %d）"),
		atpreset.Of("展锐·信道/邻区（未取证·只列 =?）", "AT+SPQ4GNCELLEX=?", "4G 邻区扩展（2 个自由参数）"),
		atpreset.Of("展锐·信道/邻区（未取证·只列 =?）", "AT+SPFDDBAND=?", "FDD 频段配置（3 个参数）"),
		atpreset.Of("展锐·信道/邻区（未取证·只列 =?）", "AT+SPCCHO=?", "小区选择相关（3 个参数）"),
		atpreset.Of("设置（谨慎·展锐）", "AT+SFUN=4", "模组重启（厂商工具用的就是这条，约 20 秒后回来）"),
		atpreset.Tpl("设置（谨慎·展锐）", `AT+SPIMEI=0,"<15位IMEI>"`,
			"写 IMEI（模板：把 <15位IMEI> 换成实际号码；写完必须 AT+CGSN 回读确认）"),
	}
}
