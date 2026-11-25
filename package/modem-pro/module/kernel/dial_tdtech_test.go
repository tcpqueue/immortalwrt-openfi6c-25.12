package kernel

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"modemd/module/dial"
	"modemd/module/tdtech"
)

func TestTDTechDialAndHangup(t *testing.T) {
	for _, mode := range []string{"0", "4"} {
		t.Run(mode, func(t *testing.T) {
			mod, sent := newTDTechDialFixture(t, mode, true)
			st, err := mod.Dial(dial.DialRequest{CID: 3, APN: "ctnet"})
			if err != nil || st.State != dial.DialConnected || st.IPv4 != "192.0.2.10" {
				t.Fatalf("dial returned %+v, %v", st, err)
			}
			commands := drainSent(sent)
			connect := slices.Index(commands, "AT^NDISDUP=3,1")
			if connect < 0 || !slices.Contains(commands[:connect], "AT+CGDCONT=3,\"IPV4V6\",\"ctnet\"") ||
				strings.Count(strings.Join(commands[connect:], "\n"), tdtech.NDISStatusQuery) < 2 {
				t.Fatalf("expected APN write, NDIS connect, and polling after the initial acknowledgement: %v", commands)
			}

			st, err = mod.Dial(dial.DialRequest{CID: 3, Reconnect: true})
			if err != nil || st.State != dial.DialConnected {
				t.Fatalf("reconnect returned %+v, %v", st, err)
			}
			commands = drainSent(sent)
			stop, start := slices.Index(commands, "AT^NDISDUP=3,0"), slices.Index(commands, "AT^NDISDUP=3,1")
			if stop < 0 || start <= stop || !slices.Contains(commands[stop+1:start], tdtech.NDISStatusQuery) {
				t.Fatalf("expected confirmed disconnect before reconnect: %v", commands)
			}

			st, err = mod.Hangup(0)
			if err != nil || !st.ManualStop || st.State != dial.DialIdle {
				t.Fatalf("hangup returned %+v, %v", st, err)
			}
			commands = drainSent(sent)
			if !slices.Contains(commands, "AT^NDISDUP=3,0") || slices.Contains(commands, "AT+CGACT=0,3") {
				t.Fatalf("hangup used the wrong command: %v", commands)
			}
		})
	}
}

func TestTDTechNCMWithoutNDISUsesStandardPDP(t *testing.T) {
	mod, sent := newTDTechDialFixture(t, "4", false)
	if _, err := mod.Dial(dial.DialRequest{CID: 3}); err != nil {
		t.Fatal(err)
	}
	commands := drainSent(sent)
	if !slices.Contains(commands, tdtech.NDISCapabilityQuery) || !slices.Contains(commands, "AT+CGACT=1,3") ||
		slices.Contains(commands, "AT^NDISDUP=3,1") {
		t.Fatalf("unexpected NCM dial sequence: %v", commands)
	}
}

func TestTDTechRefreshDetectsLostConnection(t *testing.T) {
	mod, sent := newTDTechDialFixture(t, "0", true)
	st, err := mod.Dial(dial.DialRequest{CID: 3})
	if err != nil {
		t.Fatal(err)
	}
	drainSent(sent)
	if mod.refreshTDTechNDIS(st) {
		t.Fatal("a live connection must stay connected")
	}
	if _, err := mod.SendAT("AT^NDISDUP=3,0"); err != nil {
		t.Fatal(err)
	}
	if !mod.refreshTDTechNDIS(st) || mod.DialStatus().State != dial.DialIdle || mod.DialStatus().IPv4 != "" {
		t.Fatalf("lost NDIS session should return to idle: %+v", mod.DialStatus())
	}
}

func TestTDTechQuotaUsesNDIS(t *testing.T) {
	mod, sent := newTDTechDialFixture(t, "0", true)
	if _, err := mod.Dial(dial.DialRequest{CID: 3}); err != nil {
		t.Fatal(err)
	}
	drainSent(sent)
	if err := mod.SetDataBlocked(true); err != nil {
		t.Fatal(err)
	}
	quotaUpdate(mod, func(st *State) { st.Blocked = true })
	if !slices.Equal(quotaState(mod).BlockedCIDs, []int{3}) {
		t.Fatalf("blocked CIDs: %v", quotaState(mod).BlockedCIDs)
	}
	if _, err := mod.Dial(dial.DialRequest{CID: 3}); err == nil {
		t.Fatal("quota must prevent starting another NDIS connection")
	}
	calls := drainSent(sent)
	if !slices.Contains(calls, "AT^NDISDUP=3,0") || slices.Contains(calls, "AT^NDISDUP=3,1") {
		t.Fatalf("unexpected blocked commands: %v", calls)
	}
	if err := mod.SetDataBlocked(false); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(drainSent(sent), "AT^NDISDUP=3,1") || len(quotaState(mod).BlockedCIDs) != 0 {
		t.Fatal("expected to restore only the recorded NDIS session")
	}
}

func newTDTechDialFixture(t *testing.T, mode string, supportsNDIS bool) (*Modem, chan string) {
	t.Helper()
	initTestLogger()
	withATTimeout(t)
	mod := &Modem{
		ID: 9751, ModelName: "MT5700M-CN", Vendor: VendorTDTech, IMEI: "900000000009751",
		cmdImpl: &tdtech.MT5700Command{}, writeChan: make(chan string, 64), readChan: make(chan string, 64),
	}
	t.Cleanup(func() {
		close(mod.writeChan)
		quotaUpdate(mod, func(st *State) { *st = State{} })
	})
	f := &tdtechDialFixture{mode: mode, supportsNDIS: supportsNDIS}
	return mod, fakeATServe(mod, f.reply)
}

type tdtechDialFixture struct {
	mode         string
	supportsNDIS bool
	active       bool
	pending      bool
}

func (f *tdtechDialFixture) reply(cmd string) []string {
	switch cmd {
	case "AT^SETMODE?":
		if f.mode == "0" {
			return []string{cmd, "0", "OK"} // §11.8 also documents the bare numeric reply.
		}
		return []string{cmd, "^SETMODE: " + f.mode, "OK"}
	case tdtech.NDISCapabilityQuery:
		if !f.supportsNDIS {
			return []string{cmd, "ERROR"}
		}
		return []string{cmd, "^NDISDUP: (1-11),(0-2)", "OK"}
	case tdtech.NDISStatusQuery:
		status := 0
		if f.active {
			status = 1
		}
		if f.pending {
			f.pending = false
			status = 2
		}
		return []string{cmd, "^NDISSTATQRY: 3," + strconv.Itoa(status) + ",,,\"IPV4\",0,,,\"IPV6\"", "OK"}
	case "AT^NDISDUP=3,1":
		f.active, f.pending = true, true
	case "AT^NDISDUP=3,0", "AT+CGACT=0,3":
		f.active, f.pending = false, false
	case "AT+CGACT=1,3":
		f.active = true
	case "AT+CGACT?":
		state := "0"
		if f.active {
			state = "1"
		}
		return []string{cmd, "+CGACT: 3," + state, "OK"}
	case "AT+CGDCONT?":
		return []string{cmd, "+CGDCONT: 3,\"IPV4V6\",\"ctnet\",\"0.0.0.0\",0,0", "OK"}
	case "AT+CGPADDR", "AT+CGPADDR=3":
		return []string{cmd, "+CGPADDR: 3,\"192.0.2.10\"", "OK"}
	}
	return []string{cmd, "OK"}
}

func TestTDTechUSBModeReadAndSwitch(t *testing.T) {
	initTestLogger()
	withATTimeout(t)
	mod := &Modem{
		ID: 9752, ModelName: "MT5700M-CN", Vendor: VendorTDTech,
		cmdImpl: &tdtech.MT5700Command{}, writeChan: make(chan string, 64), readChan: make(chan string, 64),
	}
	t.Cleanup(func() { close(mod.writeChan) })
	sent := fakeATServe(mod, func(cmd string) []string {
		switch cmd {
		case "AT^SETMODE?":
			return []string{cmd, "^RSSI: 31", "^SETMODE: 2", "OK"}
		case "AT^SETMODE=4":
			return []string{cmd, "OK"}
		default:
			return []string{cmd, "ERROR"}
		}
	})
	status, err := mod.QueryUSBnet()
	if err != nil || status.Value != "2" || status.Name != "ECM" || status.NeedReboot || !status.AutoReboot {
		t.Fatalf("unexpected reported debug mode: %+v, %v", status, err)
	}
	drainSent(sent)
	status, err = mod.SetUSBnet("4")
	if err != nil || status.Name != "NCM" || !status.AutoReboot || status.NeedReboot {
		t.Fatalf("unexpected switch response: %+v, %v", status, err)
	}
	if calls := drainSent(sent); !slices.Equal(calls, []string{"AT^SETMODE=4"}) {
		t.Fatalf("mode switch must let the modem reboot automatically: %v", calls)
	}
}
