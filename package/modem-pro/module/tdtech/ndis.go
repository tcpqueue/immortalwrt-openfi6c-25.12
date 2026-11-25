package tdtech

import (
	"fmt"
	"strconv"
	"strings"

	"modemd/module/common"
)

const (
	NDISStatusQuery     = "AT^NDISSTATQRY?"
	NDISCapabilityQuery = "AT^NDISDUP=?"
)

type NDISState uint8

const (
	NDISDisconnected NDISState = iota
	NDISTransitioning
	NDISConnected
)

func IsMT5700MCN(model string) bool {
	return strings.Contains(strings.ToUpper(model), "MT5700M-CN")
}

func ValidNDISCID(cid int) bool {
	return cid >= 1 && cid <= 11
}

func NDISDialCommand(cid int, connect bool) string {
	if connect {
		return fmt.Sprintf("AT^NDISDUP=%d,1", cid)
	}
	return fmt.Sprintf("AT^NDISDUP=%d,0", cid)
}

// ParseNDISStatus reads the query form in section 16.4 of the MT5700M-CN AT
// manual. Each CID can carry separate IPv4/IPv6 groups. One connected stack is
// enough for a data connection. All stacks must be down to confirm a hangup.
func ParseNDISStatus(lines []string) (map[int]NDISState, error) {
	states := make(map[int]NDISState)
	for _, line := range lines {
		payload, ok := strings.CutPrefix(strings.TrimSpace(line), "^NDISSTATQRY:")
		if !ok {
			continue
		}
		fields := common.SplitCSVFields(payload)
		if len(fields) < 2 || len(fields) > 9 {
			return nil, fmt.Errorf("无法解析 ^NDISSTATQRY 的 CID 和连接状态")
		}
		cid, err := strconv.Atoi(strings.TrimSpace(fields[0]))
		if err != nil || !ValidNDISCID(cid) {
			return nil, fmt.Errorf("^NDISSTATQRY 返回无效的 CID")
		}
		for index := 1; index < len(fields); index += 4 {
			stat, err := strconv.Atoi(strings.TrimSpace(fields[index]))
			if err != nil || stat < 0 || stat > 3 {
				return nil, fmt.Errorf("^NDISSTATQRY 返回无效的连接状态（CID %d）", cid)
			}
			state := NDISDisconnected
			switch stat {
			case 1:
				state = NDISConnected
			case 2, 3:
				state = NDISTransitioning
			}
			states[cid] = max(states[cid], state)
		}
	}
	return states, nil
}
