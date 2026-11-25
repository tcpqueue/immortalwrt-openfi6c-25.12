package fibocom

// 本文件是广和通自己的「USB 网卡模式：哪些档位只展示、不允许执行」的知识。
//
// 原先写在 module/kernel/vendor_settings_usb.go 里（那里对 *Modem 直接判断厂商常量），
// 现在判定权归厂商包：kernel 只负责把「清单 kind + 设备回报的取值/展示名」收集好传进来。
//
// 为什么不能只按取值钉：同一档位（RNDIS）在各平台取值不同（高通/lte 是 24、展锐 38、
// 联发科 41），清单口径给的是 kind（qmi/ecm/rndis…），而厂商方言表里只有展示名，
// 所以 kind 与展示名两条路都要认。

import (
	"strings"

	"modemd/iface"
)

// USBnetRNDISWhy 广和通 RNDIS 被钉成"只展示"的原因（界面要写清楚，别写成含糊的"不支持"）。
//
// 2026-10-09 FM160-CN 现场：广和通切到 RNDIS 后 USB 组成里**没有 AT 口**
// （`2cb7:010b` = RNDIS+DUN+DIAG+ADB，其中 ttyUSB 是 ff/ff/30，发 AT 不回；ADB 口倒是还在），
// 页面上的 AT 功能（信号/短信/拨号/USB 模式自己）全部失联，最后是从模组内部
// 用 ADB + root 改 `/fibonv/data/mode.cfg` 才救回来的 —— 这种"一改就回不来"的档位
// 不该从页面上给出去。
const USBnetRNDISWhy = "广和通切到 RNDIS 后 USB 组成里就没有 AT 口了（实测 FM160-CN：" +
	"2cb7:010b = RNDIS+DUN+DIAG+ADB，ttyUSB 发 AT 不回），页面上所有 AT 功能" +
	"（信号/短信/拨号/切回其它模式）会一起失联，只能从模组内部用 ADB + root 改 " +
	"/fibonv/data/mode.cfg 才能救回来 —— 所以这一档只展示、不允许执行。"

// USBnetBlockedKind 被钉住的档位（清单口径的 kind）
const USBnetBlockedKind = "rndis"

// IsRNDISModeName 展示名是不是 RNDIS 那一档
func IsRNDISModeName(name string) bool {
	return strings.Contains(strings.ToUpper(strings.TrimSpace(name)), "RNDIS")
}

// USBModeRef 一条 USB 网卡模式的几个口径（调用方按自己手上有什么填）：
//   - Kind 清单口径的档位（qmi/ecm/rndis…）；清单里查得到就填
//   - Value 取值（设备回读的码）
//   - Name 展示名（厂商方言表给的）
type USBModeRef struct {
	Kind  string
	Value string
	Name  string
}

// USBnetBlockedValues 本机"只展示、不允许执行"的取值 → 原因（空 = 都允许）。
//
// 只有广和通有这一档；厂商不是广和通（或 vendor 为空）直接返回 nil。
func USBnetBlockedValues(vendor iface.ModemVendor, modes []USBModeRef) map[string]string {
	if vendor != iface.VendorFibocom {
		return nil
	}
	out := map[string]string{}
	for _, m := range modes {
		if strings.EqualFold(strings.TrimSpace(m.Kind), USBnetBlockedKind) ||
			IsRNDISModeName(m.Name) {
			if v := strings.TrimSpace(m.Value); v != "" {
				out[v] = USBnetRNDISWhy
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
