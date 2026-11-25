package kernel

import (
	"modemd/module/catalog"
	"strings"
	"testing"
)

// ====================== 能力缺口表（"为什么没有"）不变量测试 ======================

// TestCapabilityGapTableWellFormed 缺口表自身的结构约束。
func TestCapabilityGapTableWellFormed(t *testing.T) {
	valid := map[gapStatus]bool{gapOpen: true, gapNotNeeded: true, gapDone: true, gapResearch: true}
	seen := map[string]int{}
	for i, g := range capabilityGaps {
		if strings.TrimSpace(g.Capability) == "" {
			t.Errorf("第 %d 条没写 Capability", i)
			continue
		}
		if !valid[g.Status] {
			t.Errorf("%s：状态 %q 不认识（只能是 %q/%q/%q/%q）",
				g.Capability, g.Status, gapOpen, gapNotNeeded, gapDone, gapResearch)
		}
		if strings.TrimSpace(g.Reason) == "" {
			t.Errorf("%s：没写 Reason（这张表存在的意义就是写清为什么）", g.Capability)
		}
		if strings.TrimSpace(g.Evidence) == "" {
			t.Errorf("%s：没写 Evidence（依据哪个脚本/手册/代码；没有证据就该写“没有证据”）", g.Capability)
		}
		if dup, ok := seen[g.Capability]; ok {
			t.Errorf("%q 重复：第 %d 条与第 %d 条（查找只取第一条）", g.Capability, dup, i)
		}
		seen[g.Capability] = i
	}
}

// gapReasonAllowlist 允许"没有原因记录"的缺口能力：不与任何型号绑定的通用能力
// （例如"供电电压"是硬件是否引出/固件是否支持的问题，逐型号写在别处）。
// ⚠️ 往这里加东西前先问一句：是不是应该给它写一条 capabilityGaps 记录？
var gapReasonAllowlist = map[string]string{}

// TestCapabilityGapReasonCoversAllPlanGaps AT 计划里列出的**每一个**未接入能力，
// 都必须能在缺口表里查到原因。
//
// 为什么要有这条：界面上"本型号未接入：AMBR"这行字如果没有原因，用户只会反复来问
// "为什么没有 AMBR"；而原因（手册里就没这条指令 / 字段口径没取证 / 刻意不接）
// 是**已经查过的结论**，必须随计划一起出现。
func TestCapabilityGapReasonCoversAllPlanGaps(t *testing.T) {
	missing := map[string][]string{}
	for _, e := range catalog.ModemCatalog {
		p := ATPlanForModel(e.Model)
		if len(p.Gaps) != len(p.GapNotes) {
			t.Fatalf("%s：Gaps(%d) 与 GapNotes(%d) 长度不一致（界面按下标取用）",
				e.Model, len(p.Gaps), len(p.GapNotes))
		}
		for _, g := range p.Gaps {
			if _, ok := gapReasonAllowlist[g]; ok {
				continue
			}
			if strings.TrimSpace(GapReasonFor(g)) == "" {
				missing[g] = append(missing[g], e.Model)
			}
		}
	}
	for g, models := range missing {
		t.Errorf("缺口 %q（%d 个型号，如 %s）在 capability_gaps.go 里没有原因记录"+
			"（补一条，或加进 gapReasonAllowlist 并说明原因）",
			g, len(models), strings.Join(models[:min(3, len(models))], "/"))
	}
}

// TestCapabilityGapMarkdownStable 缺口表 → Markdown 的基本形状（文档 F.2 直接贴它）。
func TestCapabilityGapMarkdownStable(t *testing.T) {
	md := CapabilityGapsMarkdown()
	if !strings.HasPrefix(md, "| 能力 | 状态 | 哪些型号 | 为什么没有 / 为什么不接 | 证据 / 线索 |") {
		t.Fatalf("缺口表的表头变了：%q", strings.SplitN(md, "\n", 2)[0])
	}
	lines := strings.Split(strings.TrimRight(md, "\n"), "\n")
	if len(lines) != len(capabilityGaps)+2 {
		t.Fatalf("表格行数 %d ≠ 缺口条数 %d + 2", len(lines), len(capabilityGaps))
	}
	for _, l := range lines[2:] {
		pipes, unescaped := 0, 0
		for i := 0; i < len(l); i++ {
			if l[i] == '|' {
				pipes++
				if i == 0 || l[i-1] != '\\' {
					unescaped++
				}
			}
		}
		if unescaped != 6 {
			t.Errorf("列数不对（管未转义的竖线会破坏 Markdown）：%s", l)
		}
	}
	if s := CapabilityGapsStats(); !strings.Contains(s, "共 ") {
		t.Errorf("统计串不对：%s", s)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
