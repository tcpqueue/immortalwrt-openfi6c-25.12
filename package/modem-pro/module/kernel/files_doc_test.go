package kernel

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// kernelFilesDocPath 逐个文件的说明文档（见 §"kernel 文件说明"）。
const kernelFilesDocPath = "../../MD/kernel-文件说明.md"

// TestCorev2FilesDocumented `MD\kernel-文件说明.md` 必须**覆盖 kernel 里的每个 .go 文件**。
//
// 为什么要有这条：这一层文件多（160 个），新人（以及几个月后的自己）打开目录根本猜不出
// `glue.go` / `fillstate.go` / `zerowire` 是干什么的；而"补了文件忘了写文档"是**必然**会发生的
// —— 靠自觉不如靠测试。这条只管一件事：每个文件名都在文档里出现过（内容对不对拦不住，
// 但"漏了整块"能立刻发现）。
//
// 文档缺失时跳过（比如只拷了代码仓库的一棵子树），与 TestCatalogSyncDoc 的处理一致。
func TestCorev2FilesDocumented(t *testing.T) {
	raw, err := os.ReadFile(kernelFilesDocPath)
	if err != nil {
		t.Skipf("跳过：读不到 %s（%v）", kernelFilesDocPath, err)
	}
	doc := string(raw)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读 kernel 目录失败：%v", err)
	}
	// 文档里以 `文件名` 的形式引用（带反引号），所以直接找 "`name`"
	var missing []string
	total := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		total++
		if !strings.Contains(doc, "`"+e.Name()+"`") {
			missing = append(missing, e.Name())
		}
	}
	// 非 .go 的数据文件也要登记（清单 JSON 是 go:embed 的单一数据源）
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if e.Name() == "modem_catalog.json" && !strings.Contains(doc, e.Name()) {
			missing = append(missing, e.Name())
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("kernel 里有 %d 个文件没写进 %s：\n  %s\n"+
			"（在对应分组里补一行；测试文件补到「测试文件」那节）",
			len(missing), filepath.ToSlash(kernelFilesDocPath), strings.Join(missing, "\n  "))
	}
	if total < 100 {
		t.Errorf("只扫到 %d 个 .go 文件，明显不对（目录读错？）", total)
	}
}
