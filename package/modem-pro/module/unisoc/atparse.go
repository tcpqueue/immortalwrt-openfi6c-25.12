package unisoc

import (
	"strconv"
	"strings"

	"modemd/module/atparse"
	"modemd/module/common"
)

// ParseC5GQOSRDP 有方（Neoway）`AT+C5GQOSRDP` 的解析（vendor/neoway.sh 的 network_info()）。
//
// 脚本里抄的是 3GPP 的 5GS QoS 格式（Original）：
//
//	+C5GQOSRDP: <cid>,<5QI>[,<DL_GFBR>,<UL_GFBR>[,<DL_MFBR>,<UL_MFBR>[,<DL_SAMBR>,<UL_SAMBR>[,<Averaging_window>]]]]]
//	DL_SAMBR = $7、UL_SAMBR = $8（单位 kbit/s，脚本再 /1024 换成 Mbps）
//
// ⚠️ 脚本用的是**固定下标**（`awk -F',' '{print $7}'`）：GFBR 与 MFBR 要么都有要么都没有，
// 所以 $7/$8 只有在给了 MFBR 之后才落在 SAMBR 上。这里就按同一个口径（至少 8 段、取 f7/f8），
// **不猜别的下标**；取不到（占位 `-`、越界、全 0）就当未上报。
//
// 与脚本的一处**有意差异**：f1 是 3GPP 布局里的 `<5QI>`（= QCI，承载的 QoS 等级），
// 脚本没取，本项目把它一并带出来（`AMBRRate.QCI`）给界面显示；它不影响 AMBR 的成败 ——
// 占位 `-` / 越界（1~255 之外）只把 QCI 记成未上报(0)，签约速率照常返回。
func ParseC5GQOSRDP(resp []string) (*atparse.AMBRRate, bool) {
	return ParseC5GQOSRDPContext(resp, 0)
}

// ParseC5GQOSRDPContext 同上，但**只取拨号的那条承载**：
// 查询不带 cid 时，模组会把每条 PDP 上下文各回一行（`<cid>,<5QI>,…`，IMS 也占一行），
// 所以按 cid 选中拨号那条；cid<=0 时退回第一条（见 pickAMBR）。
func ParseC5GQOSRDPContext(resp []string, cid int) (*atparse.AMBRRate, bool) {
	var cands []*atparse.AMBRRate
	for _, line := range resp {
		s := strings.TrimSpace(line)
		if !strings.Contains(strings.ToUpper(s), "C5GQOSRDP") {
			continue
		}
		i := strings.Index(s, ":")
		if i < 0 {
			continue
		}
		f := strings.Split(s[i+1:], ",")
		if len(f) < 8 {
			continue
		}
		dl, e1 := strconv.Atoi(strings.TrimSpace(f[6]))
		ul, e2 := strconv.Atoi(strings.TrimSpace(f[7]))
		if e1 != nil || e2 != nil || (ul == 0 && dl == 0) {
			continue
		}
		cands = append(cands, &atparse.AMBRRate{
			CID:    common.AtoiOr0(f[0]), // f0 = <cid>：选「拨号那条」时用
			DLMbps: atparse.KbpsToMbps(dl),
			ULMbps: atparse.KbpsToMbps(ul),
			QCI:    atparse.ParseQCI(f[1]),
		})
	}
	return atparse.PickAMBR(cands, cid, "")
}
