package kernel

import (
	"context"
	"encoding/json"
	"modemd/module/config"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ====================== 微信机器人：新短信推送到微信 ======================
//
// 需求：短信页能配一个微信机器人，收到新短信就推到微信（协议实现见 ilink.go）。
//
// 组成：
//   · 扫码登录：申请二维码 → 轮询状态 → 拿到 bot_token 写回 toml（重启不丢）
//   · 监听（可选）：长轮询 getupdates，记录"谁给机器人发过消息" → 作为默认推送目标
//   · 推送：新短信 → 模板渲染 → 队列串行发送（限速 + 重试）
//
// 三个"不看有没有人开页面"的地方（和定时器/流量配额同一原则）：
// 监听、发送队列、登录轮询都是后台 goroutine，页面关掉也照样工作。
//
// ⚠️ 新短信的判定：ReadSMS 返回的是**全量**列表，这里按"短信指纹"去重 ——
// 指纹 = 号码|时间|内容哈希|分条号，与存储位置无关（位置会因删除/插入变化）。
// **第一次运行只建基线不推送**（否则一装好就把卡里的历史短信全推一遍）。
//
// ⚠️ 配置项（含 bot_token）与短信密码同级敏感：接口必须过短信密码闸门，
// 页面上的卡片也放在「短信」页的解锁区里（否则谁都能把短信转发到自己的微信）。

const (
	// wechatQRTTL 二维码有效期（官方 5 分钟）
	wechatQRTTL = 5 * time.Minute
	// wechatQRRefreshMax 过期后自动刷新次数上限
	wechatQRRefreshMax = 3
	// wechatLoginWait 等待扫码的总时长
	wechatLoginWait = 9 * time.Minute
	// wechatQRWidth 二维码图片边长（px）
	wechatQRWidth = 320
	// wechatSendGap 两条消息之间的最小间隔（防刷屏 / 防限流）
	wechatSendGap = 800 * time.Millisecond
	// wechatSendRetry 单条消息最多尝试次数
	wechatSendRetry = 3
	// wechatSendBackoff 重试间隔
	wechatSendBackoff = 2 * time.Second
	// wechatQueueSize 待发队列长度（满了就丢，绝不阻塞串口读线程）
	wechatQueueSize = 64
	// wechatSeenMax 已推送指纹的保留条数
	wechatSeenMax = 400
	// wechatSaveEvery 状态落盘节流
	wechatSaveEvery = 5 * time.Second
)

// 登录阶段（给界面显示）
const (
	wechatStageIdle    = "idle"            // 未登录
	wechatStageWait    = "waiting"         // 等待扫码
	wechatStageScan    = "scanned"         // 已扫码待确认
	wechatStageVerify  = "need_verifycode" // 需要配对码
	wechatStageReady   = "ready"           // 已登录
	wechatStageExpired = "expired"         // 二维码失效 / 等待超时
	wechatStageFailed  = "failed"          // 登录失败 / 凭据失效
)

// WeChatSendRecord 最近一次发送结果（界面上显示，便于对账）
type WeChatSendRecord struct {
	At   time.Time `json:"at"`
	OK   bool      `json:"ok"`
	Msg  string    `json:"msg,omitempty"`
	Text string    `json:"text,omitempty"`
	Test bool      `json:"test,omitempty"`
}

// wechatCfgSnapshot 配置快照（受 runtime 锁保护，避免和 API 写入竞争）
type wechatCfgSnapshot struct {
	Enabled   bool
	BaseURL   string
	BotToken  string
	BotID     string
	BotUserID string
	ToUserID  string
	Template  string
	Listen    bool
	Allowed   string
}

// wechatStateFile 落盘文件（wechat.json，和 schedule.json / quota.json 同一套路）
type wechatStateFile struct {
	Version    int       `json:"version"`
	SavedAt    time.Time `json:"saved_at"`
	SyncBuf    string    `json:"sync_buf,omitempty"`
	LastSender string    `json:"last_sender,omitempty"`
	// ContextToken 最近一条用户消息的会话令牌（发送时必须带上，否则服务端回 prepare failed）
	ContextToken string `json:"context_token,omitempty"`
	// HeardUser 是否**真的**收到过微信用户的消息（= 会话建立过）。
	// ⚠️ 必须与“扫码用户 ID”分开：后者在扫码登录时就写进 last_sender 了，
	// 拿它当“会话已建立”会让页面永远不提示“先去给机器人发一条消息”（踩过）。
	HeardUser      bool      `json:"heard_user,omitempty"`
	LastIncoming   string    `json:"last_incoming,omitempty"`
	LastIncomingAt time.Time `json:"last_incoming_at,omitempty"`
	Seen           []string  `json:"seen,omitempty"`
	Ready          bool      `json:"ready,omitempty"`
}

type wechatRuntime struct {
	mu sync.Mutex

	path string
	cfg  wechatCfgSnapshot

	client *ilinkClient

	// loginClient 登录过程中用的**匿名**客户端（不带 bot_token）。
	// ⚠️ 不能复用 r.client：登录轮询/刷新二维码如果带上旧的（可能已失效的）token，
	// 服务端会直接回 `batch verify bot token fail`，把扫码登录搞挂。
	loginClient *ilinkClient

	stage    string
	stageMsg string

	qrKey    string // 轮询键
	qrPNG    string // data URI（前端 <img src>）
	qrAt     time.Time
	pollBase string
	verify   string // 待提交的配对码（界面填好后由轮询带上）
	refresh  int

	lastSender string
	// lastContext 最近一条用户消息的 context_token。
	// ⚠️ **发消息必须带上它**（官方 SDK 回复消息就是这么做的）：
	// 不带的话服务端会回 ret=-2 prepare failed —— 因为不存在会话。
	lastContext string
	// heardUser 真的收到过用户消息（= 会话建立过）；lastIncoming 是最近发消息的那个人。
	// ⚠️ 别拿 lastSender 代替：它在扫码登录时就会被填成“扫码用户”。
	heardUser      bool
	lastIncoming   string
	lastIncomingAt time.Time
	lastSend       *WeChatSendRecord
	sentOK         int
	sentFail       int

	buf     string // getupdates 游标
	seen    []string
	seenSet map[string]bool
	ready   bool // 指纹基线是否已建立

	queue chan string

	dirty   bool
	savedAt time.Time

	loginCancel  context.CancelFunc
	listenCancel context.CancelFunc
	listenOn     bool
}

var wechatInst = &wechatRuntime{
	stage:   wechatStageIdle,
	seenSet: map[string]bool{},
	queue:   make(chan string, wechatQueueSize),
}

// ------------------------------------------------------------ 初始化 ---

// InitWeChatRuntime 载入运行状态 + 按配置启动后台循环（main 启动时调用一次）
func InitWeChatRuntime() {
	r := wechatInst
	path := "wechat.json"
	if p := strings.TrimSpace(config.ConfigPath); p != "" {
		path = filepath.Join(filepath.Dir(p), "wechat.json")
	}
	st := loadWeChatState(path)

	r.mu.Lock()
	r.path = path
	r.cfg = wechatCfgFromConfig()
	r.buf = st.SyncBuf
	r.lastSender = st.LastSender
	r.lastContext = st.ContextToken
	r.heardUser = st.HeardUser
	r.lastIncoming = st.LastIncoming
	r.lastIncomingAt = st.LastIncomingAt
	r.ready = st.Ready
	r.seen = append([]string(nil), st.Seen...)
	for _, k := range st.Seen {
		r.seenSet[k] = true
	}
	if r.cfg.BotToken != "" && r.cfg.BotID != "" {
		r.client = newILinkClient(r.cfg.BaseURL, r.cfg.BotToken)
		r.stage = wechatStageReady
		if r.lastSender == "" {
			r.stageMsg = "已登录（还没收到过微信消息，推送目标可以用「最近发消息的人」自动确定）"
		} else {
			r.stageMsg = "已登录"
		}
	}
	hasToken := r.cfg.BotToken != ""
	enabled := r.cfg.Enabled
	botID := r.cfg.BotID
	r.mu.Unlock()

	// 发送队列：常驻（进程级）
	go wechatSendLoop(context.Background())

	if hasToken {
		wechatStartListen()
	}
	logger.Info("wechat init",
		zap.String("path", path), zap.Bool("enabled", enabled),
		zap.Bool("has_token", hasToken), zap.String("bot_id", botID),
		zap.Int("seen", len(st.Seen)))
}

func wechatCfgFromConfig() wechatCfgSnapshot {
	c := config.Cfg.WeChat
	return wechatCfgSnapshot{
		Enabled:   c.Enabled,
		BaseURL:   c.BaseURL,
		BotToken:  c.BotToken,
		BotID:     c.BotID,
		BotUserID: c.BotUserID,
		ToUserID:  c.ToUserID,
		Template:  c.Template,
		Listen:    c.Listen,
		Allowed:   c.AllowedSenders,
	}
}

func loadWeChatState(path string) wechatStateFile {
	var st wechatStateFile
	b, err := os.ReadFile(path)
	if err != nil {
		return st // 首次运行没有该文件，正常
	}
	if err := json.Unmarshal(b, &st); err != nil {
		logger.Warn("wechat state broken, start from scratch", zap.String("path", path), zap.Error(err))
		return wechatStateFile{}
	}
	return st
}

// FlushWeChat 强制落盘（进程退出前调用）
func FlushWeChat() {
	r := wechatInst
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saveLocked()
}

func (r *wechatRuntime) saveLocked() {
	if r.path == "" {
		return
	}
	payload := wechatStateFile{
		Version: 1, SavedAt: time.Now(),
		SyncBuf: r.buf, LastSender: r.lastSender, ContextToken: r.lastContext,
		HeardUser: r.heardUser, LastIncoming: r.lastIncoming, LastIncomingAt: r.lastIncomingAt,
		Seen: r.seen, Ready: r.ready,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		logger.Warn("wechat state write failed", zap.String("path", tmp), zap.Error(err))
		return
	}
	if err := os.Rename(tmp, r.path); err != nil {
		logger.Warn("wechat state rename failed", zap.String("path", r.path), zap.Error(err))
		return
	}
	r.dirty = false
	r.savedAt = time.Now()
}

// saveMaybeLocked 节流落盘（新短信/游标变化都挺频繁，不必每次都写）
func (r *wechatRuntime) saveMaybeLocked() {
	r.dirty = true
	if time.Since(r.savedAt) >= wechatSaveEvery {
		r.saveLocked()
	}
}

// ------------------------------------------------------------ 视图 ---
