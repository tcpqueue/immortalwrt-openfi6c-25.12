package kernel

import (
	"fmt"
	"strings"
	"time"

	"modemd/module/common"
	"modemd/module/dial"
	"modemd/module/tdtech"
)

func (mod *Modem) isMT5700MCN() bool {
	return tdtech.IsMT5700MCN(mod.ModelName) || tdtech.IsMT5700MCN(mod.Model)
}

func (mod *Modem) usesTDTechNDIS() bool {
	return mod.isMT5700MCN() && mod.USBType(false).tdtechNDIS
}

func (mod *Modem) probeTDTechNDIS(info *DialTypeInfo) bool {
	if !mod.isMT5700MCN() || info.Bus == "pcie" {
		return false
	}
	if info.Kind == "ecm" {
		return true
	}
	if info.Kind != "ncm" {
		return false
	}
	// Section 16.1 documents Linux ECM explicitly. Preserve the standard
	// PDP path for NCM firmware that does not advertise this capability.
	resp, err := mod.SendAT(tdtech.NDISCapabilityQuery)
	if err != nil || common.RespHasError(resp) || !hasATOK(resp) {
		return false
	}
	for _, line := range resp {
		if strings.HasPrefix(strings.TrimSpace(line), "^NDISDUP:") {
			return true
		}
	}
	return false
}

func hasATOK(resp []string) bool {
	for _, line := range resp {
		if strings.EqualFold(strings.TrimSpace(line), "OK") {
			return true
		}
	}
	return false
}

func (mod *Modem) queryTDTechNDIS() (map[int]tdtech.NDISState, error) {
	resp, err := mod.SendAT(tdtech.NDISStatusQuery)
	if err != nil {
		return nil, err
	}
	if common.RespHasError(resp) {
		return nil, fmt.Errorf("模组拒绝 %s", tdtech.NDISStatusQuery)
	}
	if !hasATOK(resp) {
		return nil, fmt.Errorf("%s 未返回最终确认", tdtech.NDISStatusQuery)
	}
	return tdtech.ParseNDISStatus(resp)
}

func (mod *Modem) changeTDTechNDIS(cid int, connect bool) error {
	if !tdtech.ValidNDISCID(cid) {
		return fmt.Errorf("MT5700M-CN 的 NDIS CID 须在 1~11 之间（当前 %d）", cid)
	}
	cmd := tdtech.NDISDialCommand(cid, connect)
	resp, err := mod.SendAT(cmd)
	if err != nil {
		return err
	}
	if reason := atError(resp); reason != "" {
		return fmt.Errorf("模组拒绝 %s（%s）", cmd, reason)
	}
	if !hasATOK(resp) {
		return fmt.Errorf("模组未接受 %s", cmd)
	}

	want := tdtech.NDISDisconnected
	if connect {
		want = tdtech.NDISConnected
	}
	// The OK above acknowledges receipt, not completion (manual §16.1).
	deadline := time.Now().Add(12 * time.Second)
	for {
		states, err := mod.queryTDTechNDIS()
		if err != nil {
			return err
		}
		if states[cid] == want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s 已受理，%s 未确认连接状态（CID %d）", cmd, tdtech.NDISStatusQuery, cid)
		}
		time.Sleep(400 * time.Millisecond)
	}
}

func (mod *Modem) setTDTechNDIS(cid int, active, reconnect bool) error {
	if !tdtech.ValidNDISCID(cid) {
		return fmt.Errorf("MT5700M-CN 不支持 NDIS CID %d", cid)
	}
	mod.ndisMu.Lock()
	defer mod.ndisMu.Unlock()
	if active && quotaBlocked(mod) {
		return fmt.Errorf("流量配额已超限，数据连接已停用")
	}

	states, err := mod.queryTDTechNDIS()
	if err != nil {
		return err
	}
	state := states[cid]
	if !active && state == tdtech.NDISDisconnected {
		return nil
	}
	if active && state != tdtech.NDISDisconnected {
		if !reconnect && state == tdtech.NDISConnected {
			return nil
		}
		if err := mod.changeTDTechNDIS(cid, false); err != nil {
			return err
		}
	}
	return mod.changeTDTechNDIS(cid, active)
}

// Refresh only connections managed by Dial, so an explicit hangup is never
// undone. An unavailable or malformed status reply leaves the state alone.
func (mod *Modem) refreshTDTechNDIS(st *dial.DialState) bool {
	if st.State != dial.DialConnected || !mod.usesTDTechNDIS() || !mod.ndisMu.TryLock() {
		return false
	}
	defer mod.ndisMu.Unlock()
	states, err := mod.queryTDTechNDIS()
	if err != nil || states[st.CID] != tdtech.NDISDisconnected {
		return false
	}

	mod.dialMu.Lock()
	if mod.dialBusy || mod.dialState.State != dial.DialConnected ||
		mod.dialState.CID != st.CID || !mod.dialState.ConnectedAt.Equal(st.ConnectedAt) {
		mod.dialMu.Unlock()
		return false
	}
	mod.dialState.State, mod.dialState.StateText = dial.DialIdle, dial.DialStateText(dial.DialIdle)
	mod.dialState.ConnectedAt = time.Time{}
	mod.dialState.IPv4, mod.dialState.IPv6, mod.dialState.HostIface, mod.dialState.HostIPs = "", "", "", nil
	mod.dialMu.Unlock()
	pushDial(mod)
	return true
}
