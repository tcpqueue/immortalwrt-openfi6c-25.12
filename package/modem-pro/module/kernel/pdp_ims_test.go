package kernel

// ====================== PDP / IMS 自动激活 ======================
//
// 需求（用户）：「APN 要自动激活对应的 PDP 和 ims」。
//   · 写完 APN 就自动重新激活那个 cid（不然 AT+CGDCONT 改了也不生效）；
//   · 顺带把模组的 IMS 上下文激活起来（VoLTE / IMS 短信的先决条件）。
// 这里钉住三件事：① 怎么找 IMS 上下文（别找错 cid）；② 真会发 AT+CGACT=1,<ims cid>；
// ③ 写 APN 接口默认自动激活，且能显式关掉。

import (
	"errors"
	"fmt"
	"modemd/module/config"
	"modemd/module/fibocom"
	"modemd/module/modelquirk"
	"modemd/module/sms"
	"strings"
	"testing"
	"time"
)

func TestIMSContextCIDs(t *testing.T) {
	cases := []struct {
		name string
		ctxs []Context
		want []int
	}{
		{
			name: "标准写法：cid1 = ims",
			ctxs: []Context{{CID: 1, APN: "ims", Internal: true}, {CID: 2, APN: "ctnet", Active: true}},
			want: []int{1},
		},
		{
			name: "大小写与后缀变体都认",
			ctxs: []Context{{CID: 3, APN: "IMS"}, {CID: 5, APN: "ims.apn"}},
			want: []int{3, 5},
		},
		{
			// 有些固件 IMS 那条不写 APN（空串），只在 CGDCONT 里留个位；3GPP 惯例 cid 1
			name: "cid1 空 APN 且被标成内部上下文 → 当 IMS",
			ctxs: []Context{{CID: 1, APN: "", Internal: true}, {CID: 2, APN: "ctnet"}},
			want: []int{1},
		},
		{
			// ⚠️ 别的空 APN 上下文只是"没配置"，不能当 IMS（否则会去激活一个空配置，必然失败）
			name: "非 cid1 的空 APN 不算",
			ctxs: []Context{{CID: 4, APN: "", Internal: false}},
			want: nil,
		},
		{
			name: "imsfoo 这类普通 APN 不误判",
			ctxs: []Context{{CID: 2, APN: "imsfoo"}},
			want: nil,
		},
		{
			name: "只有数据 APN 时什么都不挑（调用方据此跳过）",
			ctxs: []Context{{CID: 1, APN: "3gnet", Active: true}, {CID: 2, APN: "ctnet"}},
			want: nil,
		},
		{
			name: "重复的 cid 只算一次、结果升序",
			ctxs: []Context{{CID: 5, APN: "ims"}, {CID: 2, APN: "ims"}, {CID: 5, APN: "ims"}},
			want: []int{2, 5},
		},
	}
	for _, c := range cases {
		got := IMSContextCIDs(c.ctxs)
		if len(got) != len(c.want) {
			t.Errorf("%s：cid 列表 = %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s：cid 列表 = %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}

// 假模组：记着每个 cid 的激活状态与 APN 配置，够 QueryParams / SetPDPActive / DeleteAPN 用。
func fakePDPModem(apns map[int]string, active map[int]bool) func(cmd string) []string {
	// 指令里第一个数字字段（如 "AT+CGDCONT=3,"..."" → "3"）
	atoi := func(s string) int {
		n := 0
		for _, r := range s {
			if r >= '0' && r <= '9' {
				n = n*10 + int(r-'0')
			}
		}
		return n
	}
	return func(cmd string) []string {
		switch {
		case strings.HasPrefix(cmd, "AT+CGDCONT="):
			// AT+CGDCONT=<cid>,"<type>","<apn>" 写配置；只给 <cid> 是删除整条上下文
			f := sms.SplitCMGLFields(strings.TrimPrefix(cmd, "AT+CGDCONT="))
			if len(f) == 0 {
				return []string{"OK"}
			}
			cid := atoi(f[0])
			if len(f) >= 3 {
				apns[cid] = f[2]
			} else if len(f) == 1 {
				delete(apns, cid)
				delete(active, cid)
			}
			return []string{"OK"}
		case strings.HasPrefix(cmd, "AT+CGDCONT?"):
			out := []string{}
			for cid := 0; cid <= 8; cid++ {
				if apn, ok := apns[cid]; ok {
					out = append(out, `+CGDCONT: `+itoa(cid)+`,"IPV4V6","`+apn+`","",0,0,0,0,0,0,0,0,0,0`)
				}
			}
			return append(out, "OK")
		case strings.HasPrefix(cmd, "AT+CGACT="):
			f := sms.SplitCMGLFields(strings.TrimPrefix(cmd, "AT+CGACT="))
			if len(f) >= 2 {
				active[atoi(f[0])] = strings.TrimSpace(f[1]) == "1"
			}
			return []string{"OK"}
		case strings.HasPrefix(cmd, "AT+CGACT?"):
			out := []string{}
			for cid := 0; cid <= 8; cid++ {
				if _, ok := apns[cid]; ok {
					v := 0
					if active[cid] {
						v = 1
					}
					out = append(out, `+CGACT: `+itoa(cid)+`,`+itoa(v))
				}
			}
			return append(out, "OK")
		case strings.HasPrefix(cmd, "AT+COPS?"):
			return []string{`+COPS: 0,0,"CHN-UNICOM",11`, "OK"}
		case strings.HasPrefix(cmd, "AT+CGCONTRDP"):
			return []string{"OK"}
		}
		return []string{"OK"}
	}
}

// ActivateIMS：未激活的 IMS 上下文会被 AT+CGACT=1,<cid> 激活；已经激活的不再发指令。
func TestActivateIMSActivatesInactiveContext(t *testing.T) {
	withATTimeout(t)
	apns := map[int]string{1: "ims", 2: "ctnet"}
	active := map[int]bool{2: true} // ims 还没激活
	mod := &Modem{ID: 0, cmdImpl: &fibocom.FibocomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	sent := fakeATServe(mod, fakePDPModem(apns, active))
	defer close(mod.writeChan)

	res := mod.ActivateIMS([]Context{
		{CID: 1, APN: "ims", Internal: true},
		{CID: 2, APN: "ctnet", Active: true},
	})
	if res.Status != "ok" {
		t.Fatalf("状态 = %q（%s）", res.Status, res.Note)
	}
	if !strings.Contains(res.Note, "cid=1") {
		t.Errorf("说明里应写明激活了哪个 cid：%q", res.Note)
	}
	cmds := drainSent(sent)
	if !hasCmd(cmds, "AT+CGACT=1,1") {
		t.Errorf("没发激活 IMS 的指令：%v", cmds)
	}
	if hasCmd(cmds, "AT+CGACT=1,2") {
		t.Errorf("已激活的数据上下文不该被动：%v", cmds)
	}
}

// 没有 IMS 上下文时直接跳过，**一条 AT 都不发**（别去猜别的 cid）
func TestActivateIMSSkipsWithoutIMSContext(t *testing.T) {
	mod := &Modem{ID: 0, cmdImpl: &fibocom.FibocomCommand{},
		writeChan: make(chan string, 8), readChan: make(chan string, 8)}
	sent := fakeATServe(mod, fakePDPModem(map[int]string{2: "ctnet"}, map[int]bool{2: true}))
	defer close(mod.writeChan)

	res := mod.ActivateIMS([]Context{{CID: 2, APN: "ctnet", Active: true}})
	if res.Status != "skip" {
		t.Fatalf("状态 = %q（%s）", res.Status, res.Note)
	}
	if n := len(drainSent(sent)); n != 0 {
		t.Fatalf("跳过时不该发指令，实际发了 %d 条", n)
	}
}

// 固件拒绝（实测 FM350 就是不许手动激活 IMS）→ 只报 fail，不 panic、不影响别人
func TestActivateIMSModemRejects(t *testing.T) {
	withATTimeout(t)
	apns := map[int]string{1: "ims", 2: "ctnet"}
	active := map[int]bool{}
	script := fakePDPModem(apns, active)
	mod := &Modem{ID: 0, cmdImpl: &fibocom.FibocomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	sent := fakeATServe(mod, func(cmd string) []string {
		if cmd == "AT+CGACT=1,1" {
			return []string{"ERROR"}
		}
		return script(cmd)
	})
	defer close(mod.writeChan)

	res := mod.ActivateIMS([]Context{{CID: 1, APN: "ims", Internal: true}})
	if res.Status != "fail" {
		t.Fatalf("被拒时状态应为 fail，实际 %q（%s）", res.Status, res.Note)
	}
	if !strings.Contains(res.Note, "IMS 激活失败") {
		t.Errorf("说明应说清失败原因：%q", res.Note)
	}
	if !hasCmd(drainSent(sent), "AT+CGACT=1,1") {
		t.Error("应该确实尝试过激活")
	}
}

// 写 APN 接口：默认**自动激活**该 cid（先断后连）并把 IMS 也激活起来
func TestAPISetAPNAutoActivates(t *testing.T) {
	withATTimeout(t)
	apns := map[int]string{1: "ims", 2: "oldnet"}
	active := map[int]bool{2: true}
	mod := &Modem{ID: 0, cmdImpl: &fibocom.FibocomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	mgr := NewModemManager()
	mgr.modems[0] = mod
	sent := fakeATServe(mod, fakePDPModem(apns, active))
	defer close(mod.writeChan)

	code, msg := doPostRequest(t, apiSetAPN(mgr), "/api/params/apn",
		`{"modem_id":0,"cid":2,"pdp_type":"IPV4V6","apn":"ctnet"}`)
	if code != 0 {
		t.Fatalf("code = %d, msg = %s", code, msg)
	}
	if !strings.Contains(msg, "已写入 cid=2") || !strings.Contains(msg, "重新激活") {
		t.Errorf("响应应说明已自动激活：%q", msg)
	}
	if !strings.Contains(msg, "IMS") {
		t.Errorf("响应应带上 IMS 处理结果：%q", msg)
	}
	cmds := drainSent(sent)
	if !hasCmd(cmds, `AT+CGDCONT=2,"IPV4V6","ctnet"`) {
		t.Errorf("没写 APN：%v", cmds)
	}
	if !hasCmd(cmds, "AT+CGACT=1,2") {
		t.Errorf("没自动激活 cid=2：%v", cmds)
	}
	if !hasCmd(cmds, "AT+CGACT=1,1") {
		t.Errorf("没自动激活 IMS（cid=1）：%v", cmds)
	}
}

// 显式 "activate": false → 只写配置、不碰链路（给脚本/批量改配置留的后路）
func TestAPISetAPNCanSkipActivation(t *testing.T) {
	withATTimeout(t)
	apns := map[int]string{2: "oldnet"}
	active := map[int]bool{}
	mod := &Modem{ID: 0, cmdImpl: &fibocom.FibocomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	mgr := NewModemManager()
	mgr.modems[0] = mod
	sent := fakeATServe(mod, fakePDPModem(apns, active))
	defer close(mod.writeChan)

	code, msg := doPostRequest(t, apiSetAPN(mgr), "/api/params/apn",
		`{"modem_id":0,"cid":2,"pdp_type":"IPV4V6","apn":"ctnet","activate":false}`)
	if code != 0 {
		t.Fatalf("code = %d, msg = %s", code, msg)
	}
	if !strings.Contains(msg, "未激活") {
		t.Errorf("响应应说明按请求未激活：%q", msg)
	}
	for _, c := range drainSent(sent) {
		if strings.HasPrefix(c, "AT+CGACT=") {
			t.Fatalf("activate=false 时不该发 AT+CGACT 写指令：%v", c)
		}
	}
}

// FM650（展锐）特例：AT+CGACT 可能不给最终结果 → 加长超时 + 超时后改走 AT+CGACT? 回读。
// ⚠️ 这个特例**只给 FM650**，同厂其它机型与别家都不受影响。
func TestFM650PDPNoReplyQuirk(t *testing.T) {
	q := modelquirk.Lookup("ATI|Manufacturer: Fibocom Wireless Inc.|Model: FM650-CN|Revision: 86600.1000.00.05.17.11|OK")
	if !q.NoReplyOnPDPActivate {
		t.Error("FM650 应标 NoReplyOnPDPActivate（AT+CGACT 可能不回最终结果）")
	}
	if q.PDPTimeout < 15*time.Second {
		t.Errorf("FM650 的 PDP 专用超时 %v 太短（实测要 5~20s）", q.PDPTimeout)
	}
	for _, m := range []string{"FM350-GL", "FM160-EAU", "RM502Q-GL", "MT5700M-CN", ""} {
		if modelquirk.Lookup(m).NoReplyOnPDPActivate {
			t.Errorf("%q 不该带 NoReplyOnPDPActivate（只给 FM650）", m)
		}
	}
}

func TestPDPActivateVerifyAfterTimeout(t *testing.T) {
	fm650 := modelquirk.Lookup("FM650-CN")
	other := modelquirk.Lookup("RM502Q-GL")
	if !pdpActivateVerifyAfterTimeout(errATTimeout, fm650) {
		t.Error("FM650 + 超时 → 应改走 AT+CGACT? 回读判定")
	}
	if !pdpActivateVerifyAfterTimeout(fmt.Errorf("%w: AT+CGACT=1,0", errATTimeout), fm650) {
		t.Error("包装过的超时也要认得（errors.Is）")
	}
	if pdpActivateVerifyAfterTimeout(fmt.Errorf("open port failed"), fm650) {
		t.Error("非超时错误不该改走回读判定")
	}
	if pdpActivateVerifyAfterTimeout(errATTimeout, other) {
		t.Error("没有该特例的型号不该改行为（不影响别的模块）")
	}
}

func TestPDPCommandTimeout(t *testing.T) {
	defer func(old time.Duration) { config.Cfg.Serial.CmdTimeout = old }(config.Cfg.Serial.CmdTimeout)
	config.Cfg.Serial.CmdTimeout = 3 * time.Second
	mod := &Modem{ID: 0}
	if got, want := mod.pdpCommandTimeout(modelquirk.Lookup("FM650-CN")), modelquirk.Lookup("FM650-CN").PDPTimeout; got != want {
		t.Errorf("FM650 应取特例超时 %v，得到 %v", want, got)
	}
	if got := mod.pdpCommandTimeout(modelquirk.Lookup("RM502Q-GL")); got != 3*time.Second {
		t.Errorf("无特例应取 cmd_timeout 3s，得到 %v", got)
	}
	config.Cfg.Serial.CmdTimeout = 0
	if got := mod.pdpCommandTimeout(modelquirk.Quirk{}); got != 3*time.Second {
		t.Errorf("配置为 0（单测直接构造）时应兜底 3s，得到 %v", got)
	}
}

// 超时要能被 errors.Is 认出来（SetPDPActive 靠它区分"超时"与"打开失败"）。
func TestSendATTimeoutSentinel(t *testing.T) {
	mod := &Modem{ID: 0, writeChan: make(chan string, 1), readChan: make(chan string, 1)}
	_, err := mod.SendATTimeout("AT+NOREPLY", 30*time.Millisecond)
	if !errors.Is(err, errATTimeout) {
		t.Fatalf("超时应是 errATTimeout，得到 %v", err)
	}
}

// 删除 APN 配置：发 AT+CGDCONT=<cid>（只给 cid = 删掉整条上下文），删完回读确认
func TestDeleteAPNRemovesContext(t *testing.T) {
	withATTimeout(t)
	apns := map[int]string{1: "ims", 3: "testapn"}
	active := map[int]bool{1: true}
	mod := &Modem{ID: 0, cmdImpl: &fibocom.FibocomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	sent := fakeATServe(mod, fakePDPModem(apns, active))
	defer close(mod.writeChan)

	st, err := mod.DeleteAPN(3)
	if err != nil {
		t.Fatalf("删除失败：%v", err)
	}
	if c := ByCID(st.Contexts, 3); c != nil && c.APN != "" {
		t.Errorf("回读里 cid=3 的 APN 应已清空，实际 %q", c.APN)
	}
	if !hasCmd(drainSent(sent), "AT+CGDCONT=3") {
		t.Error("没发删除指令 AT+CGDCONT=3")
	}
	if _, ok := apns[3]; ok {
		t.Error("假模组里 cid=3 应已被删除")
	}
	if apns[1] != "ims" {
		t.Errorf("不该动别的 cid（cid=1 = %q）", apns[1])
	}
}

// 已激活的上下文不许删 —— 先让用户停用，不在删除时偷偷把链路断掉
func TestDeleteAPNRefusesActiveContext(t *testing.T) {
	withATTimeout(t)
	apns := map[int]string{2: "ctnet"}
	active := map[int]bool{2: true}
	mod := &Modem{ID: 0, cmdImpl: &fibocom.FibocomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	sent := fakeATServe(mod, fakePDPModem(apns, active))
	defer close(mod.writeChan)

	if _, err := mod.DeleteAPN(2); err == nil || !strings.Contains(err.Error(), "停用") {
		t.Fatalf("应拦下已激活的 cid 并提示先停用，得到 %v", err)
	}
	for _, c := range drainSent(sent) {
		if strings.HasPrefix(c, "AT+CGDCONT=") {
			t.Fatalf("被拦下时不该发写指令：%v", c)
		}
	}
}

// 本来就没配 APN 的 cid：直接报错，一条写指令都不发（别顺手清掉别人的上下文）
func TestDeleteAPNRefusesEmptyContext(t *testing.T) {
	withATTimeout(t)
	mod := &Modem{ID: 0, cmdImpl: &fibocom.FibocomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	sent := fakeATServe(mod, fakePDPModem(map[int]string{1: "ims"}, map[int]bool{1: true}))
	defer close(mod.writeChan)

	if _, err := mod.DeleteAPN(4); err == nil || !strings.Contains(err.Error(), "没有可删除") {
		t.Fatalf("空上下文应被拦下，得到 %v", err)
	}
	for _, c := range drainSent(sent) {
		if strings.HasPrefix(c, "AT+CGDCONT=") {
			t.Fatalf("被拦下时不该发写指令：%v", c)
		}
	}
}

// 固件不支持删除（回 ERROR）→ 如实报错
func TestDeleteAPNModemRejects(t *testing.T) {
	withATTimeout(t)
	apns := map[int]string{3: "testapn"}
	script := fakePDPModem(apns, map[int]bool{})
	mod := &Modem{ID: 0, cmdImpl: &fibocom.FibocomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	fakeATServe(mod, func(cmd string) []string {
		if cmd == "AT+CGDCONT=3" {
			return []string{"ERROR"}
		}
		return script(cmd)
	})
	defer close(mod.writeChan)

	_, err := mod.DeleteAPN(3)
	if err == nil || !strings.Contains(err.Error(), "模组拒绝") {
		t.Fatalf("应如实报错，得到 %v", err)
	}
	if apns[3] != "testapn" {
		t.Errorf("被拒后不该改动配置，实际 %q", apns[3])
	}
}

// 回读不一致（固件回 OK 但没真删）→ 报错，但仍把刷新的 st 返回给前端重绘
func TestDeleteAPNReadBackMismatch(t *testing.T) {
	withATTimeout(t)
	apns := map[int]string{3: "testapn"}
	mod := &Modem{ID: 0, cmdImpl: &fibocom.FibocomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	fakeATServe(mod, func(cmd string) []string {
		if strings.HasPrefix(cmd, "AT+CGDCONT=") {
			return []string{"OK"} // 静默忽略，没真删
		}
		return fakePDPModem(apns, map[int]bool{})(cmd)
	})
	defer close(mod.writeChan)

	st, err := mod.DeleteAPN(3)
	if err == nil || !strings.Contains(err.Error(), "回读") {
		t.Fatalf("回读不一致应报错，得到 %v", err)
	}
	if st == nil {
		t.Fatal("回读不一致也要把刷新后的 st 返回给前端重绘")
	}
}

// 接口层：POST /api/params/apn/delete
func TestAPIDeleteAPN(t *testing.T) {
	withATTimeout(t)
	apns := map[int]string{3: "testapn"}
	mod := &Modem{ID: 0, cmdImpl: &fibocom.FibocomCommand{},
		writeChan: make(chan string, 64), readChan: make(chan string, 64)}
	mgr := NewModemManager()
	mgr.modems[0] = mod
	sent := fakeATServe(mod, fakePDPModem(apns, map[int]bool{}))
	defer close(mod.writeChan)

	code, msg := doPostRequest(t, apiDeleteAPN(mgr), "/api/params/apn/delete",
		`{"modem_id":0,"cid":3}`)
	if code != 0 {
		t.Fatalf("code = %d, msg = %s", code, msg)
	}
	if !strings.Contains(msg, "已删除 cid=3") {
		t.Errorf("响应应说清删了哪个 cid：%q", msg)
	}
	if !hasCmd(drainSent(sent), "AT+CGDCONT=3") {
		t.Error("没发删除指令")
	}
}
