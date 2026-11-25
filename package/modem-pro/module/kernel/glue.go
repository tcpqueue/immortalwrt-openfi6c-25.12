package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"fmt"
)

// ---- from: modem_glue.go ----

// ---- from: modem_pdp.go ----
//
// 解析算法在 data.go（纯函数、可单独测试）；这里只负责"选指令 + 发 + 收"，
// 因为它要用到引擎的东西：运行期探测出的指令表（resolveVendorCmd）与串口。
// QueryPDPAddrs 查询 PDP 上下文的 IPv4 / IPv6 地址。
// IPv6 拿不到时只返回 IPv4（不算错误：可能只开了 IPv4 或模组没上报）。
//
// 解析算法在 data.go（纯函数、可单独测试）；这里只负责"选指令 + 发 + 收"，
// 因为它要用到引擎的东西：运行期探测出的指令表（resolveVendorCmd）与串口。
// QueryPDPAddrs 查询 PDP 上下文的 IPv4 / IPv6 地址。
// IPv6 拿不到时只返回 IPv4（不算错误：可能只开了 IPv4 或模组没上报）。

func (mod *Modem) QueryPDPAddrs() (ipv4, ipv6 string, err error) {
	cmd := mod.resolveVendorCmd(KindPDPAddr)
	if cmd == "" {
		return "", "", fmt.Errorf("no PDP address command available")
	}
	resp, sendErr := mod.SendAT(cmd)
	if sendErr != nil {
		return "", "", sendErr
	}
	ipv4, ipv6 = ParseCGPADDR(resp, DefaultDataCID)
	if ipv4 == "" && ipv6 == "" {
		return "", "", fmt.Errorf("PDP address not reported")
	}
	return ipv4, ipv6, nil
}

// ---- from: modem_apn.go ----

//
// 解析在 data.go（纯逻辑，含数据 APN / 内部 APN 的判定）；
// 这里只剩"发哪条指令 + 错误处理"。
// QueryAPN 查询当前网络 APN 与本机 IP。// 优先 AT+CGCONTRDP（当前**激活**的 PDP），不可用时退回 AT+CGDCONT?（只说明配置了哪些）。

// ====================== APN（引擎侧的胶水） ======================
//
// 解析在 data.go（纯逻辑，含数据 APN / 内部 APN 的判定）；
// 这里只剩"发哪条指令 + 错误处理"。

// QueryAPN 查询当前网络 APN 与本机 IP。// 优先 AT+CGCONTRDP（当前**激活**的 PDP），不可用时退回 AT+CGDCONT?（只说明配置了哪些）。
func (mod *Modem) QueryAPN() (apnName, ip string, err error) {
	cmd := mod.resolveVendorCmd(KindAPN)
	if cmd == "" {
		return "", "", fmt.Errorf("no APN command available")
	}
	resp, sendErr := mod.SendAT(cmd)
	if sendErr != nil {
		return "", "", sendErr
	}
	apnName, ip = ParseResp(cmd, resp)
	if apnName == "" {
		return "", "", fmt.Errorf("APN not reported")
	}
	return apnName, ip, nil
}

// ---- from: modem_ambr.go ----

//
// 解析在 data.go（纯逻辑，含三条路径与字段口径说明）；
// 这里管两件与模组强相关的事：发哪条指令、TTL 缓存（挂在 Modem 上）。
// QueryAMBR 查询签约速率；模组/厂商不支持时返回 nil（前端显示"未上报"）。
// 结果带 TTL 缓存，见 data.go 的说明。
//
// 取值顺序：① 厂商私有指令（移远 AT+QNWCFG="nr5g_ambr" / 鼎桥 AT^DSAMBR）→
// ② 3GPP 标准 AT+CGCONTRDP 的尾部 AMBR 扩展（部分固件才有）。
