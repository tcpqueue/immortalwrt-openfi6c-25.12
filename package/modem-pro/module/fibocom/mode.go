package fibocom

import (
	"modemd/module/modedialect"
)

import ()

// Mode 广和通：AT+GTACT=<act>[,<频段参数…>]（QModem vendor/fibocom.sh + cmds/fibocom.sh）
//
// 1 仅3G、2 仅4G、4 3G+4G、10 全部、14 仅5G、16 3G+5G、17 4G+5G、20 全部。
//
// ⚠️⚠️ **写入只发档位号**（真机 FM160-CN 实测，2026-10-07）：
//
//	AT+GTACT=17,6,3,1,8,101,103,…,5078,5079 → **ERROR**（模组把后面的频段串按新的档位重新对齐，
//	                                          多出一个 3G 频段组就对不上 ⇒ 拒）
//	AT+GTACT=17                              → **OK**，且模组自己把参数重组成 `17,6,,101,…,5079`
//	                                          （3G 频段组自动清空、4G/5G 频段原样保留）
//
// QModem 上游也是这个思路：`cmd_gtact_set` 收的是“参数尾串”，而 `fibocom_gtact_params` 在
// 没有频段时**就只给档位号**；要带频段时它只带**选中制式**的那几组（单制式还用 `,,,` 空段形式）。
// 我们的界面只改档位、不改频段 ⇒ 只发档位号最贴合，也不会顺手把用户的频段配置改掉。
func Mode() *modedialect.Dialect {
	d := modedialect.ModeCodesDialect("AT+GTACT?", "AT+GTACT=?", "+GTACT:",
		"广和通 AT+GTACT=<act>（QModem vendor/fibocom.sh：1 仅3G、2 仅4G、4 3G+4G、10 全部、14 仅5G、"+
			"16 3G+5G、17 4G+5G、20 全部）。写入**只发档位号**，后面的频段参数由模组自己按新档位重组"+
			"（真机 FM160-CN 实测：带完整频段表会被 ERROR 拒掉）。",
		map[string]string{
			modedialect.ModeAuto: "20", modedialect.ModeWCDMA: "1", modedialect.ModeLTE: "2", modedialect.ModeNR5G: "14",
			modedialect.ModeWCDMALTE: "4", modedialect.ModeWCDMANR: "16", modedialect.ModeLTENR5G: "17",
		},
		map[string]string{
			"1": modedialect.ModeWCDMA, "2": modedialect.ModeLTE, "4": modedialect.ModeWCDMALTE, "10": modedialect.ModeAuto,
			"14": modedialect.ModeNR5G, "16": modedialect.ModeWCDMANR, "17": modedialect.ModeLTENR5G, "20": modedialect.ModeAuto,
		},
		modedialect.FirstCSVField("+GTACT:"),
		func(code string) string { return "AT+GTACT=" + code })
	// 备用写法：把回读到的整串（f2/f3 + 各制式频段）原样带上 —— 少数固件可能坚持要完整参数表
	d.RawMatch = "+GTACT:"
	d.Alt = func(v, rawLine string) string {
		return modedialect.RebuildSetFromQuery("AT+GTACT=", rawLine, d.CodeFor(v))
	}
	return d
}
