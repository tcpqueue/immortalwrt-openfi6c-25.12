//go:build !windows

package kernel

import "testing"

// TestAutoWANForModemSkipsAirplane：自动组网每轮都会调 autoWANForModem（netprov_engine.go 只在非 Windows 编译）。
// 飞行模式开着时必须**直接返回**：不激活 PDP、不去写 UCI，也不留下拨号器进程记录。
func TestAutoWANForModemSkipsAirplane(t *testing.T) {
	mod := &Modem{ID: 4243}
	mod.isRunning = true
	mod.noteFuncMode(CFUNAirplane)

	autoWANForModem(mod) // 不该 panic、不该拨号

	hostDials.mu.Lock()
	run := hostDials.runs[mod.ID]
	hostDials.mu.Unlock()
	if run != nil && run.running {
		t.Fatalf("飞行模式下不该有拨号器在跑")
	}
}
