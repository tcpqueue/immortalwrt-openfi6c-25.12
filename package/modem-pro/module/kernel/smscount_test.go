package kernel

// ====================== 短信角标（未读条数） ======================
//
// 为什么单独钉住：悬浮小窗要在**不联网读正文**的前提下显示「未读几条」，
// 靠的就是这两个数（SMSTotal/SMSUnread）。判据错了不会报错，只会让角标永远显示 0 ——
// 实测 FM350 走 PDU 读法，状态是**数字**（0 = 未读），只认 "UNREAD" 字样就永远数不出来。

import (
	"modemd/module/fibocom"
	"modemd/module/sms"
	"strings"
	"testing"
)

// 两种固件的状态写法都要认：文本模式回 "REC UNREAD"，PDU 模式回数字 0
func TestSMSStatusUnread(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{"REC UNREAD", true},
		{`"REC UNREAD"`, true},
		{"REC READ", false},
		{"STO SENT", false},
		{"0", true},   // PDU 模式：0 = 未读
		{`"0"`, true}, // 有的固件给数字也带引号
		{"1", false},  // PDU 模式：1 = 已读
		{"2", false},  // 已发送
		{"", false},   // 拿不到状态：宁可少算，别虚报一个消不掉的角标
		{"   ", false},
		{"?", false},
	}
	for _, c := range cases {
		if got := SMSStatusUnread(c.status); got != c.want {
			t.Errorf("SMSStatusUnread(%q) = %v, want %v", c.status, got, c.want)
		}
	}
}

func TestCountUnreadSMS(t *testing.T) {
	if n := CountUnreadSMS(nil); n != 0 {
		t.Errorf("空列表 = %d", n)
	}
	list := []sms.SMS{
		{Index: "1", Status: "0"},          // 未读
		{Index: "2", Status: "1"},          // 已读
		{Index: "3", Status: "REC UNREAD"}, // 未读（文本模式）
		{Index: "4", Status: ""},           // 状态未知
	}
	if n := CountUnreadSMS(list); n != 2 {
		t.Errorf("未读数 = %d, want 2", n)
	}
}

// 走一遍真的 ReadSMS（PDU 路径，FM350-GL）：读完要把条数与未读数落下来，
// 且**未读**只算状态为 0 的那条。
func TestReadSMSUpdatesCounts(t *testing.T) {
	withATTimeout(t)
	mod := &Modem{
		ID:        0,
		Model:     "ATI|Manufacturer: Fibocom Wireless Inc.|Model: FM350-GL|OK",
		cmdImpl:   &fibocom.FibocomCommand{},
		writeChan: make(chan string, 64),
		readChan:  make(chan string, 64),
	}
	go func() {
		for cmd := range mod.writeChan {
			switch {
			case strings.HasPrefix(cmd, "AT+CMGF=0"):
				mod.readChan <- cmd
				mod.readChan <- "OK"
			case strings.HasPrefix(cmd, "AT+CMGL=4"):
				mod.readChan <- cmd
				// PDU 模式的第 2 格是数字状态：0 = REC UNREAD，1 = REC READ
				mod.readChan <- `+CMGL: 1,0,,159`
				mod.readChan <- fm350PDUPart1
				mod.readChan <- `+CMGL: 2,1,,159`
				mod.readChan <- fm350PDUPart1
				mod.readChan <- "OK"
			default: // AT+CMGF=1（读完切回文本模式）
				mod.readChan <- cmd
				mod.readChan <- "OK"
			}
		}
	}()
	defer close(mod.writeChan)

	list, err := mod.ReadSMS()
	if err != nil {
		t.Fatalf("读短信失败：%v", err)
	}
	if len(list) != 2 {
		t.Fatalf("条目数 = %d, want 2", len(list))
	}
	mod.mu.Lock()
	total, unread := mod.SMSTotal, mod.SMSUnread
	mod.mu.Unlock()
	if total != 2 || unread != 1 {
		t.Errorf("落盘的角标数 = 共 %d / 未读 %d, want 共 2 / 未读 1", total, unread)
	}
}

// +CMTI 上报要**立刻**把未读 +1（不必等那次几百毫秒的读取）——
// 悬浮小窗的角标靠它做到"新短信一来就变"。
func TestNoteSMSArrivalBumpsUnread(t *testing.T) {
	mod := &Modem{ID: 0}
	mod.noteSMSArrival()
	mod.noteSMSArrival()
	mod.mu.Lock()
	unread := mod.SMSUnread
	mod.mu.Unlock()
	if unread != 2 {
		t.Errorf("未读 = %d, want 2", unread)
	}
	// 再读一次列表：未读数按**刚读到的状态**重算（用户看过短信后角标会回落）
	mod.setSMSListCounts([]sms.SMS{{Index: "1", Status: "1"}, {Index: "2", Status: "1"}})
	mod.mu.Lock()
	defer mod.mu.Unlock()
	if mod.SMSUnread != 0 || mod.SMSTotal != 2 {
		t.Errorf("重算后 = 共 %d / 未读 %d, want 共 2 / 未读 0", mod.SMSTotal, mod.SMSUnread)
	}
}
