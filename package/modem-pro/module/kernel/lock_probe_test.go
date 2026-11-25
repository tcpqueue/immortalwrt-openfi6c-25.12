package kernel

import (
	"strings"
	"testing"

	"modemd/module/common"
	"modemd/module/fibocom"
	"modemd/module/simcom"
)

// TestLockCandidatesOrder 候选方言按平台提示排序（同平台更接近的排前面）。
// 注意：顺序只是"先试谁"，最终以固件接受为准 —— 见 TestLockerForUsesPreferredWhenNotStarted。
func TestLockCandidatesOrder(t *testing.T) {
	cases := []struct {
		model string
		first string
	}{
		{"Fibocom FM350-GL", "mtk"},
		{"MT5700M-CN", "balong"},
		{"RM502Q-GL", "qnw"},
		{"SIM8200EA-M2", "qnw"}, // 高通平台：先试 QNW，不行再试芯讯通 CSYSSEL
		{"SOME UNKNOWN", "qnw"},
	}
	for _, c := range cases {
		m := &Modem{ID: 0, Model: c.model, ModelName: c.model, cmdImpl: &common.UnknownCommand{}}
		cs := m.lockCandidates()
		if len(cs) != 13 { // 核心 4 套 + 次要 9 套（qcfgband/cnbp/gosuncn/telit/thales/foxconn/neoway/atband/openluat）
			t.Fatalf("%s: 候选数 = %d, want 13", c.model, len(cs))
		}
		if cs[0].Name != c.first {
			t.Errorf("%s: 首选候选 = %q, want %q", c.model, cs[0].Name, c.first)
		}
		// 四套方言都要有探测指令与标记，且互不相同
		seen := map[string]bool{}
		for _, x := range cs {
			if x.Probe == "" || x.Mark == "" || x.Locker == nil {
				t.Errorf("%s: 候选 %+v 不完整", c.model, x)
			}
			if seen[x.Name] {
				t.Errorf("%s: 候选 %q 重复", c.model, x.Name)
			}
			seen[x.Name] = true
		}
	}
}

// TestLockerForUsesPreferredWhenNotStarted 模组没启动（无 AT 通道，如单元测试）时
// 不做探测，直接用"平台提示"的猜测；并且结果被缓存。
func TestLockerForUsesPreferredWhenNotStarted(t *testing.T) {
	mtk := &Modem{ID: 0, Vendor: VendorFibocom, Model: "Fibocom FM350-GL", ModelName: "FM350-GL", cmdImpl: &fibocom.FibocomCommand{}}
	l := mtk.lockerFor()
	if _, ok := l.(*fibocom.FibocomMTKCommand); !ok {
		t.Fatalf("未启动时应回退到平台提示(MTK)，实际 %T", l)
	}
	// 缓存：再调一次应命中缓存且仍是同一套
	if again := mtk.lockerFor(); again == nil {
		t.Fatal("缓存后不该为 nil")
	} else if _, ok := again.(*fibocom.FibocomMTKCommand); !ok {
		t.Fatalf("缓存后类型变了：%T", again)
	}
	mtk.mu.Lock()
	probed := mtk.lockProbed
	mtk.mu.Unlock()
	if !probed {
		t.Error("探测结果应被缓存（lockProbed=true）")
	}

	// 芯讯通 SIM8200：未启动 → 回退到 CSYSSEL 那套
	sim := &Modem{ID: 0, Vendor: VendorSimcom, Model: "SIM8200EA-M2", ModelName: "SIM8200EA-M2", cmdImpl: &simcom.SimcomCommand{}}
	if _, ok := sim.lockerFor().(*simcom.SimcomLockCommand); !ok {
		t.Errorf("SIM8200 未启动时应回退到芯讯通实现")
	}

	// 认不出、且厂商没实现锁网 → nil（"不支持"）
	unk := &Modem{ID: 0, Vendor: VendorSimcom, Model: "SOME MODEM", cmdImpl: &simcom.SimcomCommand{}}
	if l := unk.lockerFor(); l != nil {
		t.Errorf("认不出平台且厂商未实现时应为 nil，实际 %T", l)
	}
}

// TestLockCandidateAcceptedMarks 标记匹配用的是"整行包含"（大小写无关），
// 且探测失败（无通道 → SendAT 报错）必须返回 false，绝不能误判为支持。
func TestLockCandidateAcceptedMarks(t *testing.T) {
	mod := &Modem{ID: 0, Model: "X", cmdImpl: &common.UnknownCommand{}}
	for _, c := range mod.lockCandidates() {
		if mod.lockCandidateAccepted(c) {
			t.Errorf("无 AT 通道时不该判定 %s 被接受", c.Name)
		}
	}
}

// TestLockDialectFallback 探测没命中（无 AT 通道）时，方言说明必须是"回退"口径，
// 不能谎称"探测选中"—— 现场排查要靠这句判断该不该怀疑命令发错了。
func TestLockDialectFallback(t *testing.T) {
	mod := &Modem{ID: 0, Model: "Fibocom FM350-GL", ModelName: "FM350-GL", cmdImpl: &fibocom.FibocomCommand{}}
	if name, label, note := mod.lockDialect(); name != "" || label != "" || note != "" {
		t.Errorf("还没探测过就不该有方言结论：%q %q %q", name, label, note)
	}
	mod.lockerFor() // 触发（这里只能走回退）
	name, label, note := mod.lockDialect()
	if name != "" || label != "" {
		t.Errorf("未探测命中时不该有方言名：%q %q", name, label)
	}
	if !strings.Contains(note, "未探测到") || !strings.Contains(note, "FM350-GL") {
		t.Errorf("回退说明应点明未探测到并给机型，实际 %q", note)
	}
}

// TestLockDialectLabels 每个候选方言都要有界面显示名（漏一个界面上就会显示空）。
func TestLockDialectLabels(t *testing.T) {
	mod := &Modem{ID: 0, Model: "X", cmdImpl: &common.UnknownCommand{}}
	for _, c := range mod.lockCandidates() {
		if lockFamilyLabels[c.Name] == "" {
			t.Errorf("方言 %q 缺显示名（lockFamilyLabels）", c.Name)
		}
	}
	// 命中的方言名 → 说明文案
	mod.mu.Lock()
	mod.lockProbed = true
	mod.lockFamily = "qnw"
	mod.lockTried = []string{"qnw: 接受（AT+QNWPREFCFG=\"lte_band\"）"}
	mod.mu.Unlock()
	name, label, note := mod.lockDialect()
	if name != "qnw" || label != lockFamilyLabels["qnw"] {
		t.Errorf("命中的方言应回填显示名：%q %q", name, label)
	}
	if !strings.Contains(note, "探测选中") {
		t.Errorf("命中时应说明是探测选中，实际 %q", note)
	}
}
