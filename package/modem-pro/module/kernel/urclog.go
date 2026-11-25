package kernel

// ====================== 厂商 URC 日志跟随（展锐 SIPC / 中兴 F50） ======================
//
// 背景（2026-10-05 真机实测）：
// 展锐 SIPC 这类 SoC 内置模组，AT 口很特别 —— 厂商守护进程 `mu300-atd` 独占的
// nr1 代理口一条往返 1.05 s（还要抢 FIFO 锁、和设备数据面 `mobile-data` 排同一条队），
// 而模组直连的 nr2 口只要 ~10 ms（**已改为只用 nr2**，nr1 那套实现已删除）。
// 但即使是 10 ms，modemd 的**周期推送**（信号 / 注册 / PDP）也是总线流量的大头，
// 用户的要求是更极端的：**接口一条 AT 都不要发**。
//
// 但这些状态**厂商 daemon 自己已经在写了**：它把模组 nr0 上报口上的主动上报
// 逐行落到一个**普通文件**里：
//
//	/run/mu300-at/urc/stty_nr0.log
//	    +CSQ: 39,25 / +CESQ: 99,99,255,255,25,39,255,255,255   ← 信号
//	    +CREG/+CGREG/+CEREG: 1,"404A","0400E21E",7             ← 注册
//	    +CGEV: ME PDN ACT 1 / ^CONN: 11,2,2 / +SPPCODATA: 1    ← PDP / 连接
//	    +SPNRINDICATE / +SPPCI / +IMSREGADDR / +CTZV           ← NR 指示 / 小区 / IMS / 时间
//
// ⚠️ 读它是**纯文件读**：不碰设备节点、不占用 AT 总线 —— 零成本拿状态。
//
// 做法：把它当成"第二条 URC 来源"，新行原样喂给**与串口读循环完全相同**的解析入口
// parseLine()。于是信号 / NR / 注册这些既有解析逻辑**一行都不用改**，
// 界面看到的数据也和以前一模一样（只是不必再靠轮询换来了）。
//
// 三条不能碰的约束：
//  1. **只调 parseLine，绝不往 readChan 塞**：readChan 是 AT 应答的消费队列，
//     把历史行塞进去会被在途指令当成自己的应答（就是串台）。
//     URC 只需要更新状态，不需要参与应答匹配。
//  2. parseLine 里有**不带锁**的小状态机（如 GTCCINFO 的 gtccRat），
//     现在它有两个调用方（串口读循环 / 本协程）⇒ 必须用 parseMu 互斥。
//  3. 文件是**追加**写，daemon 重启 / 设备重启会**截断重写** ⇒ 见到
//     `size < offset` 就把 offset 归零；积压太大只补尾部，别在开机时解析几 MB 历史。

import (
	"io"
	"modemd/module/atparse"
	"modemd/module/netdev"
	"modemd/module/sms"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	// mu300URCChanName nr0（URC 主动上报口）的通道名。
	mu300URCChanName = "stty_nr0"
	// mu300URCLogPath 厂商 daemon 落 URC 的文件路径。
	// ⚠️ 是**普通文件**（rw-r--r--），不是设备节点 —— 所以读它没有任何副作用。
	mu300URCLogPath = netdev.Mu300ATDir + "/urc/" + mu300URCChanName + ".log"
	// mu300URCPollInterval 跟随间隔。本地文件、只读增量，500ms 足够"准实时"
	//（信号变化本来就是秒级体感），比在总线上轮询便宜几个数量级。
	mu300URCPollInterval = 500 * time.Millisecond
	// mu300URCBacklogMax 一轮最多补读多少字节（超过就只取尾部）。
	mu300URCBacklogMax = 256 * 1024
	// mu300URCBacklogTail 跳读时保留的尾部长度（够覆盖最近的状态变化）。
	mu300URCBacklogTail = 8 * 1024
	// urcSignalPollInterval URC 已在供信号时，仍**兜底**多久去总线问一次。
	// 60 秒：既把 AT+CSQ 的轮询量压掉约 6 倍，又能在"URC 悄悄停了"时自己发现并退回去。
	urcSignalPollInterval = 60 * time.Second // urcSignalStaleLimit URC 里多久没有信号类上报就算“跟丢了”。
	//
	// 真机实测：展锐的 `+CSQ`/`+CESQ` 大约每 20 秒就来一条（信号变化即报）
	// ⇒ 3 分钟没有就是异常（daemon 停了 / 跟随器卡了）。
	// 这时 mu300FileBackedState 会变假，接口自动退回 AT 轮询 ——
	// 宁可慢一点，也不能拿一个冻结的信号值一直显示下去。
	urcSignalStaleLimit = 3 * time.Minute
)

// mu300URCLogPathFor 某个 AT 口对应的 URC 日志路径；不是展锐 SIPC 模组时返回空串
// （其它模组没有这种东西，跟随器直接不起，现有行为完全不变）。
func mu300URCLogPathFor(port string) string {
	if !netdev.IsMu300SIPCChan(port) {
		return ""
	}
	return mu300URCLogPath
}

// urcRegDomain 注册状态属于哪个域（决定它该写进 NetworkStatus 的哪个字段）。
type urcRegDomain int

const (
	urcRegNone urcRegDomain = iota
	urcRegCS                // +CREG   电路域
	urcRegPS                // +CGREG  分组域（数据业务看这条）
	urcRegEPS               // +CEREG  EPS
	urcReg5G                // +C5GREG 5GS
)

// urcNetSnapshot URC 里解析出来的各域注册状态（nil = 该类上报还没出现过）。
type urcNetSnapshot struct {
	cs, ps, eps, fiveG *RegState
	at                 time.Time // 最后一次更新时刻
}

// anyReg 是否至少收到过一个域的注册上报。
func (s urcNetSnapshot) anyReg() bool {
	return s.cs != nil || s.ps != nil || s.eps != nil || s.fiveG != nil
}

var (
	// reRegQuery `AT+CREG?` **应答**形式：`<n>,<stat>[,"<lac>","<ci>"[,<AcT>]]`
	//（前两个字段一定是**裸数字**，靠这一点与上报形式区分）
	reRegQuery = regexp.MustCompile(`^\+C(?:G|E|5G)?REG:\s*(\d+)\s*,\s*(\d+)\s*(?:,|$)`)
	// reRegURC **主动上报**形式：`<stat>[,"<lac>"[,"<ci>"[,<AcT>]]]`
	//（lac/ci **一定带引号**，所以不会与上面那条认串）
	reRegURC = regexp.MustCompile(`^\+C(?:G|E|5G)?REG:\s*(\d+)\s*(?:,\s*"([^"]*)"(?:\s*,\s*"([^"]*)"(?:\s*,\s*(\d+))?)?)?\s*$`)
)

// urcRegDomainOf 前缀 → 域（CS / PS / EPS / 5GS）。
func urcRegDomainOf(line string) (urcRegDomain, bool) {
	switch {
	case strings.HasPrefix(line, "+C5GREG:"):
		return urcReg5G, true
	case strings.HasPrefix(line, "+CGREG:"):
		return urcRegPS, true
	case strings.HasPrefix(line, "+CEREG:"):
		return urcRegEPS, true
	case strings.HasPrefix(line, "+CREG:"):
		return urcRegCS, true
	}
	return urcRegNone, false
}

// urcRegFromLine 把一条 `+CxREG:` 行解析成注册状态。
//
// ⚠️⚠️ 两种形式的字段**不一样**（2026-10-05 实测踩到）：
//
//	`AT+CREG?` 应答：`+CREG: <n>,<stat>[,"<lac>","<ci>"[,<AcT>]]`   ← 第 1 个是 <n>
//	主动上报：   `+CREG: <stat>[,"<lac>","<ci>"[,<AcT>]]`         ← **没有 <n>**
//
// 所以 `+CREG: 2` 是 **stat=2（未注册、正在搜索）**，不是“只报了 n”；
// `+CGREG: 1,"FFFE","0400E21E",7` 是 **stat=1 已注册**。
// 直接拿 ParseReg（按应答形式写的）去读上报行，会把 `"FFFE"` 当 stat 用 ⇒ 解析直接失败，
// 表现就是“URC 明明供了注册状态，却一个都用不上”。
//
// 消歧规则：上报形式的 lac/ci **一定带引号** ⇒ “两个裸数字”就是应答形式，否则是上报形式。
func urcRegFromLine(line string) (urcRegDomain, RegState, bool) {
	line = strings.TrimSpace(line)
	dom, ok := urcRegDomainOf(line)
	if !ok {
		return urcRegNone, RegState{}, false
	}
	// ① 应答形式（两个裸数字）：直接复用 ParseReg
	if reRegQuery.MatchString(line) {
		rs, ok := ParseReg([]string{line})
		if !ok {
			return urcRegNone, RegState{}, false
		}
		return dom, rs, true
	}
	// ② 上报形式
	m := reRegURC.FindStringSubmatch(line)
	if m == nil {
		return urcRegNone, RegState{}, false
	}
	stat, err := strconv.Atoi(m[1])
	if err != nil {
		return urcRegNone, RegState{}, false
	}
	// ⚠️ 3GPP 只定义 0~5。展锐会报扩展码（实测 `+CREG: 8,"FFFE","0400E21E",7`，
	// 带着 LAC/CI 应该是“驻留了”），含义未知 ⇒ **宁可不用，也不覆盖**已有值：
	// 否则会把界面上本来正确的“已注册”写成“未知状态”。
	if stat < 0 || stat > 5 {
		return urcRegNone, RegState{}, false
	}
	rs := RegState{
		Stat:       stat,
		Text:       RegText(stat),
		Registered: stat == 1 || stat == 5,
		Roaming:    stat == 5,
		LAC:        m[2],
		CellID:     m[3],
		Raw:        line,
	}
	if m[4] != "" {
		rs.ACT, _ = strconv.Atoi(m[4])
	}
	return dom, rs, true
}

// urcOpSnapshot URC 里解析出来的运营商（`+SPNWNAME`）。
//
// 真机样本（展锐 URC）：
//
//	+SPNWNAME: 460,11,"4E2D56FD75354FE1","4E2D56FD75354FE1",1,1
//	             └mcc┘└mnc┘ └──── 全称（UCS2 hex）────┘ └── 简称 ──┘
//
// ⚠️ 引号里是 **UTF-16BE 的十六进制**（因为这台机器的 `AT+CSCS` 是 HEX），
// 不是普通字符串：`4E2D`=中 `56FD`=国 `7535`=电 `4FE1`=信 ⇒ “中国电信”。
type urcOpSnapshot struct {
	mcc, mnc   string
	full, shrt string
	at         time.Time
}

// ok 是否拿到了可用的运营商信息。
func (s urcOpSnapshot) ok() bool { return s.mcc != "" }

// urcOpFromLine 解析一条 `+SPNWNAME:` 上报；不是这行就返回 false。
func urcOpFromLine(line string) (urcOpSnapshot, bool) {
	if !strings.HasPrefix(line, "+SPNWNAME:") {
		return urcOpSnapshot{}, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, "+SPNWNAME:"))
	parts := strings.Split(rest, ",")
	if len(parts) < 2 {
		return urcOpSnapshot{}, false
	}
	mcc := strings.TrimSpace(parts[0])
	mnc := strings.TrimSpace(parts[1])
	if !atparse.IsAllDigits(mcc) || !atparse.IsAllDigits(mnc) || len(mcc) != 3 {
		return urcOpSnapshot{}, false
	}
	unquote := func(i int) string {
		if i >= len(parts) {
			return ""
		}
		s := strings.TrimSpace(parts[i])
		s = strings.Trim(s, "\"")
		// 优先当 UCS2 hex 解；解不出来（有些固件直接给明文）就当普通串用
		if out, ok := decodeUCS2Hex(s); ok && out != "" {
			return out
		}
		return s
	}
	return urcOpSnapshot{mcc: mcc, mnc: mnc, full: unquote(2), shrt: unquote(3), at: time.Now()}, true
}

// decodeUCS2Hex 把 UTF-16BE 的十六进制串解成文本（`AT+CSCS="HEX"` 固件的写法）。
// 解不出来（奇数长度 / 非十六进制 / 全是控制字符）就返回 false，由调用方决定退回明文。
func decodeUCS2Hex(s string) (string, bool) {
	if len(s) < 4 || len(s)%2 != 0 {
		return "", false
	}
	b := make([]byte, 0, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		hi, ok1 := hexVal(s[i])
		lo, ok2 := hexVal(s[i+1])
		if !ok1 || !ok2 {
			return "", false
		}
		b = append(b, hi<<4|lo)
	}
	out := sms.DecodeUCS2(b)
	if strings.TrimSpace(out) == "" {
		return "", false
	}
	// 全是不可打印字符说明它本来就不是 hex 编码的文本
	printable := false
	for _, r := range out {
		if r >= 0x20 && r != 0x7F {
			printable = true
			break
		}
	}
	if !printable {
		return "", false
	}
	return out, true
}

// hexVal 单个十六进制字符的值。
func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// mu300URCLineSaysSignal 这一行是不是"信号类"上报（URC 覆盖信号轮询的依据）。
func mu300URCLineSaysSignal(line string) bool {
	for _, p := range []string{"+CSQ:", "+CESQ:", "+QCSQ:", "^RSSI:"} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// mu300URCLineSaysNetwork 这一行是不是"注册 / PDP / 连接"类上报。
func mu300URCLineSaysNetwork(line string) bool {
	for _, p := range []string{
		"+CREG:", "+CGREG:", "+CEREG:", "+CGEV:", "^CONN:",
		"+CGACT:", "+CIREP:", "+SPPCODATA:", "+SPNRINDICATE:",
	} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// startURCLogTail 对"厂商提供了 URC 日志"的模组起一条跟随协程；其它模组什么都不做。
func (mod *Modem) startURCLogTail() {
	path := mu300URCLogPathFor(mod.ATPort)
	if path == "" {
		return
	}
	// 捕获 closeChan：Start() 会重建它，跟随协程必须盯住"自己那一代"的关闭信号
	closeChan := mod.closeChan
	logger.Info("urc log tail started",
		zap.Int("modem", mod.ID), zap.String("path", path))
	go mod.urcLogTailLoop(path, closeChan)
}

func (mod *Modem) urcLogTailLoop(path string, closeChan chan struct{}) {
	var off int64
	for {
		select {
		case <-closeChan:
			return
		default:
		}
		mod.tailURCLogOnce(path, &off)
		select {
		case <-closeChan:
			return
		case <-time.After(mu300URCPollInterval):
		}
	}
}

// tailURCLogOnce 读一次增量。off 是"已消费到的偏移"，由调用方跨轮保持。
func (mod *Modem) tailURCLogOnce(path string, off *int64) {
	f, err := os.Open(path)
	if err != nil {
		// 文件不在 / 打不开（daemon 没起来、设备刚重启）：跟随器置为"不可用"，
		// 信号轮询会**自动退回**原来的 AT 路径（见 shouldPollSignal）。
		mod.noteURCFeedState(false, err)
		*off = 0
		return
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		mod.noteURCFeedState(false, err)
		return
	}
	size := st.Size()
	if size < *off {
		// daemon 重启 / 设备重启：文件被截断重写，从头跟
		*off = 0
	}
	if size-*off > mu300URCBacklogMax {
		// 积压太多（开机很久没跟上 / 一次灌进来一大批历史）：只补最后一段
		*off = size - mu300URCBacklogTail
	}
	if *off >= size {
		mod.noteURCFeedState(true, nil)
		return
	}
	if _, err := f.Seek(*off, io.SeekStart); err != nil {
		mod.noteURCFeedState(false, err)
		*off = 0
		return
	}
	data, rerr := io.ReadAll(io.LimitReader(f, mu300URCBacklogMax))
	if rerr != nil && len(data) == 0 {
		mod.noteURCFeedState(false, rerr)
		return
	}
	mod.noteURCFeedState(true, nil)

	// 复用串口读循环那套按行切分（顺带处理"半行"：只消费完整行）
	lines, rest := splitATLines(data)
	*off += int64(len(data) - len(rest))
	for _, line := range lines {
		mod.feedURCLine(line)
	}
}

// feedURCLine 把一条 URC 行交给既有解析管线（parseLine）。
//
// ⚠️ 刻意**不往 readChan 塞** —— 那是 AT 应答的消费队列，塞进去会被在途指令
// 当成自己的应答（串台）。URC 只负责更新状态。
func (mod *Modem) feedURCLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	now := time.Now()
	mod.mu.Lock()
	mod.urcLastLine = now
	mod.lastReadTime = now // 上报口有数据 = 模组活着（“卡死”判据据此不会误报）
	if mu300URCLineSaysSignal(line) {
		mod.urcSawSignal = true
		mod.urcSignalAt = now // “跟丢”判据靠它（见 urcSignalStaleLimit）
	}
	if mu300URCLineSaysNetwork(line) {
		mod.urcSawNet = true
	}
	// 运营商：`+SPNWNAME` 里同时带 PLMN 与名字（引号里是 UCS2 hex），
	// 这是"不发 AT 也能显示运营商"的唯一来源（QNWINFO 在这台机上回 +CME ERROR: 4）。
	if op, ok := urcOpFromLine(line); ok {
		mod.urcOp = op
		mod.urcSawOp = true
	}
	// 服务小区 PCI：`+SPPCI`（固件格式 `%08X`）。
	if pci, ok := urcPCIFromLine(line); ok {
		mod.urcPCI = pci
		mod.urcSawPCI = true
	}
	// 频段：`+SPCLB`（文件里唯一的频段来源，见 urcBandFromLine）。
	if b, ok := urcBandFromLine(line); ok {
		mod.urcBand = b
		mod.urcSawBand = true
	}
	// 注册状态直接收下：**它就是 AT+CREG?/CGREG?/CEREG? 的应答内容**，
	// 所以以后不必再为了这几个字段去总线问（见 net.go 的 shouldFullQueryNetwork）。
	if dom, rs, ok := urcRegFromLine(line); ok {
		switch dom {
		case urcRegCS:
			mod.urcNet.cs = &rs
		case urcRegPS:
			mod.urcNet.ps = &rs
		case urcRegEPS:
			mod.urcNet.eps = &rs
		case urcReg5G:
			mod.urcNet.fiveG = &rs
		}
		mod.urcNet.at = now
	}
	mod.mu.Unlock()

	// parseMu：parseLine 里有不带锁的小状态机，而它现在有两个调用方
	mod.parseMu.Lock()
	// ⚠️ 必须走 **URC 版本**：URC 是这个模组信号的**唯一**来源，
	// `+CESQ` 得允许持续刷新（parseLine 里那条是"只补空"，是给 AT 兜底路径用的）——
	// 否则第一条 CESQ 之后信号就永远不动了（用户实测报的就是这个）。
	mod.parseLineURC(line)
	mod.parseMu.Unlock()
}

// noteURCFeedState 记录"这一轮跟随是否成功"，供轮询降级判断使用。
func (mod *Modem) noteURCFeedState(ok bool, err error) {
	mod.mu.Lock()
	mod.urcFeedOK = ok
	if err != nil {
		mod.urcFeedErr = err.Error()
	} else {
		mod.urcFeedErr = ""
	}
	mod.mu.Unlock()
}

// urcCoversSignal URC 日志是否已在可靠地提供**信号**（可以省掉大部分 AT+CSQ 轮询）。
//
// 条件：跟随器正常（文件在、读得到）且**曾经**收到过信号类上报。
// 为什么是"曾经"而不是"最近 N 秒内"：URC 的语义就是**变化时才报** ——
// 信号稳定时本来就很久没有新行，而这恰恰说明值没变，不必去轮询确认
// （`AT+CSQ` 问回来的也是同一个值）。
// 另外仍有 shouldPollSignal 的 60 秒兜底，所以这里判宽一点是安全的。
func (mod *Modem) urcCoversSignal() bool {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	return mod.urcFeedOK && mod.urcSawSignal
}

// urcCoversNetwork URC 是否已在可靠地提供**注册状态**
// （可以省掉 AT+CREG? / AT+CGREG? / AT+CEREG? / AT+C5GREG? 这一组轮询）。
//
// ⚠️ 条件比信号那条严：光“出现过网络类行”不够（`+CGEV`/`^CONN` 这类只是 PDP 事件，
// 里面**没有**注册状态），必须真的解析到一个域的 `+CxREG:` 才行 ——
// 否则会拿一个空的注册状态把界面上“已注册”搞没。
func (mod *Modem) urcCoversNetwork() bool {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	return mod.urcFeedOK && mod.urcNet.anyReg()
}

// urcRegRaw URC 里最近一次的注册上报**原文**（优先 CS，退 PS，再退 EPS/5GS）。
//
// 用途：`/api/network` 这类接口以前是发 `AT+CREG?` 再把应答拼成字符串；
// 现在展锐 SIPC 上直接把模组自己发的那一行还回去（从 URC 日志里取的，不是新问的），
// 所以既不用占总线，返回的也仍然是**模组的原话**。
func (mod *Modem) urcRegRaw() string {
	mod.mu.Lock()
	defer mod.mu.Unlock()
	for _, rs := range []*RegState{mod.urcNet.cs, mod.urcNet.ps, mod.urcNet.eps, mod.urcNet.fiveG} {
		if rs != nil && rs.Raw != "" {
			return rs.Raw
		}
	}
	return ""
}

// urcACTFromLine 取一行 `+CxREG:` 里的 `<AcT>` 字段（**字段真的在**时 ok=true）。
//
// ⚠️ 必须按“字段在不在”判断，不能看值会不会为 0 —— AcT 的 0 是合法的 2G 码，
// 拿它当“没这个字段”会把 5G 模组显示成 2G。
//
// 两种形式的字段数不同（应答多一个 `<n>`）：
//
//	应答：`<n>,<stat>[,"<lac>","<ci>"[,<AcT>]]`   ⇒ AcT 是第 5 个
//	上报：`<stat>[,"<lac>","<ci>"[,<AcT>]]`        ⇒ AcT 是第 4 个
func urcACTFromLine(line string) (int, bool) {
	line = strings.TrimSpace(line)
	if _, ok := urcRegDomainOf(line); !ok {
		return 0, false
	}
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return 0, false
	}
	fields := strings.Split(line[i+1:], ",")
	need := 4 // 上报形式：<stat>,"<lac>","<ci>",<act>
	if reRegQuery.MatchString(line) {
		need = 5 // 应答形式：<n>,<stat>,"<lac>","<ci>",<act>
	}
	if len(fields) < need {
		return 0, false
	}
	act, err := strconv.Atoi(strings.TrimSpace(fields[need-1]))
	if err != nil {
		return 0, false
	}
	return act, true
}

// urcPCIFromLine 解析一条 `+SPPCI:` 上报（服务小区的 **PCI**）。
//
// 从固件里取证到的格式串是 `+SPPCI: %08X`（真机样本 `+SPPCI: 0000009B` → PCI=155、
// `+SPPCI: 0000008C` → PCI=140）—— 模组换小区时它会变，所以当“当前服务小区 PCI”用。
func urcPCIFromLine(line string) (int, bool) {
	if !strings.HasPrefix(line, "+SPPCI:") {
		return 0, false
	}
	s := strings.TrimSpace(strings.TrimPrefix(line, "+SPPCI:"))
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 16, 32)
	if err != nil {
		return 0, false
	}
	// PCI 的合法范围是 0~1007（超出说明解析错了或不是 PCI）
	if v < 0 || v > 1007 {
		return 0, false
	}
	return int(v), true
}

// urcBandFromLine 解析一条 `+SPCLB:` 上报 —— 这是**文件里唯一与频段相关的东西**。
//
// 取证情况（2026-10-05，只能靠文件，不用 AT）：
//
//	· 固件的 AT 命令名表里有 `+SPCLB`（夹在 `+SPCHKSD` 与 `+SPCLSTONE` 之间），
//	  且同表里有 `+SPBANDCTRL` / `+SPBANDSCAN` / `+SPCOMMLOCKBAND`（频段族）；
//	· 它是**主动上报**（没人读它，全盘没有任何脚本引用 SPCLB）；
//	· 真机里它随小区一起变：`+SPCLB: 5` 紧跟 `+SPPCI: 0000009B`、
//	  后来 `+SPCLB: 3` 紧跟 `+SPPCI: 0000008C` ⇒ 它是**当前服务小区的频段**。
//
// ⚠️ 仍**没有**文档级证据，所以调用方只按原值写 `BAND <n>`，
// **不**加 `B3` / `n78` 那种前缀（分不清它到底是 LTE 频段还是 NR 频段，不猜）。
// 只认单值形式；固件里那个 `+SPCLB: %d,%d,%d,%d,%d,%d,%d,%d` 八字段形式一律拒掉。
func urcBandFromLine(line string) (int, bool) {
	if !strings.HasPrefix(line, "+SPCLB:") {
		return 0, false
	}
	s := strings.TrimSpace(strings.TrimPrefix(line, "+SPCLB:"))
	if s == "" || strings.Contains(s, ",") {
		return 0, false
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	// 频段号合法范围：LTE 1..71、NR 1..261（取宽一点，但不能是 0/负数）
	if v < 1 || v > 512 {
		return 0, false
	}
	return v, true
}

// shouldPollSignal 现在该不该真去总线发 `AT+CSQ`。
//
//   - 没有 URC 供信号（其它模组 / 跟随器不可用）→ **永远该问**（行为与以前一致）；
//   - 有 URC 供信号 → 只每隔 urcSignalPollInterval 兜底问一次。
//     兜底的意义：URC 要是哪天悄悄不写了，值会冻结在最后一次；
//     定期问一次就能发现"总线上的值变了而 URC 没报"，从而不会永久显示旧值。
func (mod *Modem) shouldPollSignal(now time.Time) bool {
	if !mod.urcCoversSignal() {
		return true
	}
	mod.mu.Lock()
	defer mod.mu.Unlock()
	if now.Sub(mod.signalPolledAt) < urcSignalPollInterval {
		return false
	}
	mod.signalPolledAt = now
	return true
}
