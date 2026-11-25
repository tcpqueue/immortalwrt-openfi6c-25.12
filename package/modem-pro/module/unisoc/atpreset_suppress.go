package unisoc

// 展锐不支持的通用指令（详见原注释）。
// 某些厂商**不支持**的通用项——要从通用清单里摘掉。
//
// 为什么需要这个机制：通用清单的口径是"3GPP 标准、谁都该支持"，但真机上总有例外，
// 而"这个模块只显示它自己支持的指令"是用户明确提的硬要求：多列一条注定回 ERROR 的
// 指令，比少列一条糟得多（用户会照着它试，然后把"模组不认识"当成设备故障）。
//
// 目前只有一条：展锐 UMS9620（中兴 F50）**不认不带 cid 的 `AT+CGCONTRDP`**
// （实测回 `+CME ERROR: 65536014`），只认 `AT+CGCONTRDP=<cid>`。
func SuppressedCommonPresets() []string {
	return []string{"AT+CGCONTRDP"}
}
