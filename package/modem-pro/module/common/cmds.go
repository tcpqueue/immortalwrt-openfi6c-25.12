package common

// 跨厂商复用的 AT 指令常量。
//
// 为什么放在这里而不是各厂商包里：**多个厂商共用同一条指令** ——
// QENG 被移远/广和通/芯讯通/自适应都用、GTCCINFO 是广和通两套方言之一、
// CPSI 是芯讯通的主用指令。字符串字面量只应存在一份。
//
// 依赖方向：module/common ──> 标准库。本文件不依赖任何其它包。

// CmdQENGServing 服务小区查询的首选写法（移远/广和通/芯讯通都用它，只有指令头不同）。
const CmdQENGServing = `AT+QENG="servingcell"`

// CmdCPSI 芯讯通自己的服务小区/信号查询指令。
// 实测 SIM8200EA-M2：AT+QENG="servingcell" 回 ERROR，AT+CPSI? 回完整服务小区 + 信号
// （制式/PLMN/TAC/CellID/PCI/频率/频点/RSRP/RSRQ/SINR）。
const CmdCPSI = "AT+CPSI?"

// CmdGTCCINFO 广和通 FM650（展锐平台）的服务小区 + 邻区查询指令。
// 应答是"标签行 + 数据行"成组写法，解析见 module/kernel/cell 的 GTCCINFO 段。
const CmdGTCCINFO = "AT+GTCCINFO?"

// CmdGTSTATIS 广和通（FM160-EAU / FM650-CN 实测可用）的数据统计指令。
// 与移远 AT+QGDCNT? 不同：它回的是**瞬时速率**（B/s）而不是累计字节数，
//
//	+GTSTATIS: <下载 B/s>,<上传 B/s>,<未公开>,<未公开>
//
// 字段口径取官方集成脚本 fibocom.sh 的"速率统计"（tx_rate=$2、rx_rate=$1）。
const CmdGTSTATIS = "AT+GTSTATIS?"

// CmdCGCONTRDP 3GPP 标准的 PDP 承载信息查询（激活的上下文 + 本机 IP + 网关 + DNS）。
// 顺带作为**签约速率（AMBR）的备用来源**：部分固件会在行尾追加 <下行 kbps>,<上行 kbps>。
//
// 展锐 UMS9620（中兴 F50）只认**带 cid** 的写法，所以模块侧要用 `AT+CGCONTRDP=1`。
const CmdCGCONTRDP = "AT+CGCONTRDP"
