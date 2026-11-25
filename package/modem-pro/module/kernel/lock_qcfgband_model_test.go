package kernel

import (
	"testing"

	"modemd/module/platform"
	"modemd/module/quectel"
)

// TestQuectelLegacyLTEModels 移远"旧款 4G"（QModem 平台=lte）回退到 AT+QCFG="band"
//
// 依据 QModem `docs/support_list.md`（平台列） + `vendor/quectel.sh` 的平台分派：
//
//	platform=lte   → set_lockband_lte → AT+QCFG="band",0,<hex>,0
//	platform=lte12 → 落到 `*)` 分支 → AT+QNWPREFCFG
func TestQuectelLegacyLTEModels(t *testing.T) {
	legacy := []string{
		"EC20F", "EC25", "EC25-AF", "EG25", "EG25-G", "EC21", "EC200A",
		"EM05", "EM05-CN", "EP06", "EM12", "Quectel EC25",
	}
	for _, m := range legacy {
		if !platform.IsQuectelLegacyLTEModel(m) {
			t.Errorf("%s 应判为旧款 4G（AT+QCFG=\"band\"）", m)
		}
	}
	// lte12（EM120K / EM160R-GL）走的是 QNWPREFCFG，不能被前缀 EM12 误命中
	notLegacy := []string{
		"EM120K", "EM120K-GL", "EM160R-GL", "RM520N-GL", "RM500U-CNV", "FM350-GL", "SIM7600",
	}
	for _, m := range notLegacy {
		if platform.IsQuectelLegacyLTEModel(m) {
			t.Errorf("%s 不该判为旧款 4G", m)
		}
	}
	// 回退方言：未启动时（探测不可用）EC25 应拿到 QCFG band 那套
	mod := &Modem{ID: 0, Vendor: VendorQuectel, Model: "EC25", ModelName: "EC25", cmdImpl: &quectel.QuectelCommand{}}
	if _, ok := mod.preferredLocker().(*quectel.QcfgBandLocker); !ok {
		t.Errorf("EC25 的回退方言应为 qcfgband，实际 %T", mod.preferredLocker())
	}
	// EM120K（lte12）仍应保持 QNW
	mod2 := &Modem{ID: 0, Vendor: VendorQuectel, Model: "EM120K", ModelName: "EM120K", cmdImpl: &quectel.QuectelCommand{}}
	if _, ok := mod2.preferredLocker().(*quectel.QuectelCommand); !ok {
		t.Errorf("EM120K 的回退方言应为通用 QNW，实际 %T", mod2.preferredLocker())
	}
}
