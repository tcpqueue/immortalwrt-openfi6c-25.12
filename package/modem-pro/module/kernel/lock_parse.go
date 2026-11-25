package kernel

import (
	"modemd/module/common"
	"strings"
)

// ====================== 解析与格式化 ======================

// ParseBandList / ParseLockTech / EarfcnValid 已搬到 atcmd.go（锁网入参问题，
// 与模组无关）：ParseBandList / ParseLockTech / EarfcnValid；
// kernel 里保留老名字别名，见 glue.go。

// parseQNWPREFCFG 从 AT+QNWPREFCFG 应答中取指定 key 的值：
//
//	+QNWPREFCFG: "lte_band",1:3:5
//	+QNWPREFCFG: "mode_pref",AUTO
func parseQNWPREFCFG(resp []string, key string) (string, bool) {
	const head = "+QNWPREFCFG:"
	for _, line := range resp {
		idx := strings.Index(strings.ToUpper(line), head)
		if idx < 0 {
			continue
		}
		f := common.SplitATFields(line[idx+len(head):])
		if len(f) < 2 || !strings.EqualFold(f[0], key) {
			continue
		}
		return f[1], true
	}
	return "", false
}

// parseQNWLOCK 解析小区锁定应答（`+QNWLOCK: "common/4g",1,2452,155`）——
// 实现已搬到 modemd/module/quectel（ParseQNWLOCK）；kernel 保留同名别名，见 iface_alias.go。

// atError 从 AT 应答中提取错误信息；无错误返回空串。
// 用后缀匹配 "ERROR" 以避免把回显的指令误判为错误。
func atError(resp []string) string {
	for _, l := range resp {
		u := strings.ToUpper(strings.TrimSpace(l))
		if strings.HasSuffix(u, "ERROR") || strings.Contains(u, "+CME ERROR") || strings.Contains(u, "+CMS ERROR") {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// bandLockRestricted 判断当前频段配置是否确实做了限制。
//
// policy 是设备允许使用的全量频段（AT+QNWPREFCFG="policy_band"）：
//   - 配置为空（模组上报 0）→ 频段被全部禁用，模组必然脱网，视为受限
//   - policy 中有频段没被启用 → 受限
//   - 配置与 policy 完全一致 → 未受限
//
// 拿不到 policy 时不臆断，一律返回 false。
func bandLockRestricted(current, policy []int) bool {
	if len(policy) == 0 {
		return false
	}
	if len(current) == 0 {
		// 上报 0 = 频段列表为空（不是"不限制"），等同于全部禁用
		return true
	}
	enabled := make(map[int]bool, len(current))
	for _, b := range current {
		enabled[b] = true
	}
	for _, b := range policy {
		if !enabled[b] {
			return true
		}
	}
	return false
}
