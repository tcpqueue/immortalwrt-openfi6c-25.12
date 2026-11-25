package kernel

// ====================== kernel：按功能分文件 ======================

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"modemd/module/config"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ---- from: wechat/ilink.go ----

//
// 用途：把「收到的新短信」推送到微信（本项目用它做短信转发 / 告警）。
// 协议与腾讯官方 @tencent-weixin/openclaw-weixin 一致；C# 侧参考实现见
// D:\AutoRobot\src\AutoRobot.Client\Services\ILinkApiClient.cs（字段口径完全照抄，
// 两边可以共用一个微信机器人账号，凭据互通）。
//
//	POST /ilink/bot/get_bot_qrcode?bot_type=3     申请登录二维码（返回 qrcode + qrcode_img_content）
//	GET  /ilink/bot/get_qrcode_status?qrcode=…    轮询扫码状态（服务端长轮询，客户端 35 秒超时）
//	POST /ilink/bot/getupdates                    长轮询拉新消息（get_updates_buf 游标）
//	POST /ilink/bot/sendmessage                   发消息（推短信就走这一条）
//	POST /ilink/bot/msg/notifystart|notifystop    上/下线通知
//
// 所有请求都带固定头：iLink-App-Id / iLink-App-ClientVersion / AuthorizationType /
// X-WECHAT-UIN（随机 uint32 的十进制字符串再 base64）/ User-Agent，
// 登录后额外带 Authorization: Bearer <bot_token>；POST body 里带 base_info。
//
// ⚠️ qrcode_img_content 是**二维码内容文本**（不是图片），要自己渲染成二维码图片；
// 见 wechat.go 的 wechatQRDataURI（离线用 github.com/skip2/go-qrcode 画，纯 Go 无依赖）。

const (
	// ilinkDefaultBaseURL 官方默认 API 基地址
	ilinkDefaultBaseURL = "https://ilinkai.weixin.qq.com"
	// ilinkBotType 本渠道使用的 bot_type
	ilinkBotType = "3"
	// ilinkAppID 请求头 iLink-App-Id 的取值
	ilinkAppID = "bot"
	// ilinkChannelVersion 写入 base_info.channel_version
	ilinkChannelVersion = "2.4.8"
	// ilinkAppClientVer 请求头 iLink-App-ClientVersion：0x00MMNNPP 编码（对应 2.4.8）
	ilinkAppClientVer = (2 << 16) | (4 << 8) | 8
	// ilinkBotAgent 写入 base_info.bot_agent（仅用于观测）
	ilinkBotAgent = "modemd/1.0"

	// 各类请求超时（官方口径）
	ilinkTimeoutAPI    = 15 * time.Second
	ilinkTimeoutConfig = 10 * time.Second
	ilinkTimeoutPoll   = 35 * time.Second
	ilinkTimeoutLong   = 40 * time.Second
)

// 二维码轮询状态（与官方状态字符串一致）
const (
	ilinkStatusWait        = "wait"                // 等待扫码
	ilinkStatusScanned     = "scaned"              // 已扫码待确认
	ilinkStatusConfirmed   = "confirmed"           // 已确认，凭据已下发
	ilinkStatusExpired     = "expired"             // 二维码过期
	ilinkStatusRedirect    = "scaned_but_redirect" // 已扫码，需换轮询主机
	ilinkStatusNeedVerify  = "need_verifycode"     // 需要配对码
	ilinkStatusVerifyBlock = "verify_code_blocked" // 配对码错太多被阻塞
	ilinkStatusBinded      = "binded_redirect"     // 该微信已绑定过，本地凭据仍有效
)

// ilinkClient iLink Bot 协议客户端。一个实例对应一个 bot_token；
// 未登录时 token 为空，此时只有「申请二维码 / 轮询状态」可用。
type ilinkClient struct {
	baseURL string
	token   string
	agent   string
	hc      *http.Client
}

func newILinkClient(baseURL, token string) *ilinkClient {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		base = ilinkDefaultBaseURL
	}
	return &ilinkClient{
		baseURL: strings.TrimRight(base, "/"),
		token:   strings.TrimSpace(token),
		agent:   ilinkBotAgent,
		// 不设全局超时：长轮询要挂 35 秒，逐请求自己控时
		hc: &http.Client{},
	}
}

func (c *ilinkClient) authenticated() bool { return c.token != "" }

// wechatUIN X-WECHAT-UIN：随机 uint32 → 十进制字符串 → base64（官方 SDK 同款）
func wechatUIN() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return base64.StdEncoding.EncodeToString([]byte(strconv.FormatInt(time.Now().UnixNano()%1e9, 10)))
	}
	v := binary.LittleEndian.Uint32(b[:])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(v), 10)))
}

// ilinkBaseInfo 每个请求都要带的 base_info
func ilinkBaseInfo() map[string]any {
	return map[string]any{
		"channel_version": ilinkChannelVersion,
		"bot_agent":       ilinkBotAgent,
	}
}

// do 发一个 iLink 请求并返回响应体（body 为 nil 时不带请求体，用 GET）。
func (c *ilinkClient) do(ctx context.Context, method, endpoint string, body any, timeout time.Duration) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("序列化请求失败: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, c.baseURL+endpoint, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("iLink-App-Id", ilinkAppID)
	req.Header.Set("iLink-App-ClientVersion", strconv.Itoa(ilinkAppClientVer))
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("X-WECHAT-UIN", wechatUIN())
	req.Header.Set("User-Agent", c.agent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("HTTP %d：%s", resp.StatusCode, truncateLog(string(data), 300))
	}
	return data, nil
}

// ---------------------------------------------------------------- 登录 ---

// ilinkQRSession 一次二维码会话
type ilinkQRSession struct {
	Qrcode    string    // 轮询键（get_qrcode_status 的 qrcode 参数）
	Content   string    // 二维码内容（qrcode_img_content，要自己渲染成图片）
	CreatedAt time.Time // 生成时间（官方有效期 5 分钟）
	BaseURL   string    // 生成时使用的基地址
}

// fetchQrCode 申请登录二维码（localTokens 用于让服务端判断"这个微信号是否已绑定过"）。
func (c *ilinkClient) fetchQrCode(ctx context.Context, localTokens []string) (*ilinkQRSession, error) {
	if len(localTokens) > 10 {
		localTokens = localTokens[:10]
	}
	tokens := make([]string, 0, len(localTokens))
	for _, t := range localTokens {
		if strings.TrimSpace(t) != "" {
			tokens = append(tokens, strings.TrimSpace(t))
		}
	}
	body := map[string]any{"local_token_list": tokens}

	raw, err := c.do(ctx, http.MethodPost,
		"/ilink/bot/get_bot_qrcode?bot_type="+url.QueryEscape(ilinkBotType), body, ilinkTimeoutAPI)
	if err != nil {
		return nil, err
	}
	var r struct {
		Qrcode  string `json:"qrcode"`
		Content string `json:"qrcode_img_content"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("解析二维码响应失败: %w（%s）", err, truncateLog(string(raw), 200))
	}
	if strings.TrimSpace(r.Qrcode) == "" || strings.TrimSpace(r.Content) == "" {
		return nil, fmt.Errorf("二维码响应缺少字段：%s", truncateLog(string(raw), 200))
	}
	return &ilinkQRSession{
		Qrcode:    strings.TrimSpace(r.Qrcode),
		Content:   strings.TrimSpace(r.Content),
		CreatedAt: time.Now(),
		BaseURL:   c.baseURL,
	}, nil
}

// ilinkQRStatus 二维码状态轮询结果
type ilinkQRStatus struct {
	Status       string
	BotToken     string
	BotID        string
	BaseURL      string
	UserID       string
	RedirectHost string
	Raw          string
}

// confirmed 凭据已下发
func (s ilinkQRStatus) confirmed() bool { return strings.EqualFold(s.Status, ilinkStatusConfirmed) }

// binded 该微信已绑定过（本地凭据仍有效）
func (s ilinkQRStatus) binded() bool { return strings.EqualFold(s.Status, ilinkStatusBinded) }

// needVerify 需要配对码
func (s ilinkQRStatus) needVerify() bool { return strings.EqualFold(s.Status, ilinkStatusNeedVerify) }

// pollQrStatus 轮询扫码状态。
//
// ⚠️ 客户端超时（长轮询正常现象）、网关 5xx、网络抖动全部按 "wait" 返回 ——
// 与官方 SDK 一致，避免把一次网络抖动当成登录失败。
func (c *ilinkClient) pollQrStatus(ctx context.Context, pollBase, qrcode, verifyCode string) ilinkQRStatus {
	ep := "/ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qrcode)
	if strings.TrimSpace(verifyCode) != "" {
		ep += "&verify_code=" + url.QueryEscape(verifyCode)
	}
	cl := c
	if b := strings.TrimSpace(pollBase); b != "" && strings.TrimRight(b, "/") != c.baseURL {
		cp := *c
		cp.baseURL = strings.TrimRight(b, "/")
		cl = &cp
	}
	raw, err := cl.do(ctx, http.MethodGet, ep, nil, ilinkTimeoutPoll)
	if err != nil {
		return ilinkQRStatus{Status: ilinkStatusWait}
	}
	var r struct {
		Status       string `json:"status"`
		BotToken     string `json:"bot_token"`
		BotID        string `json:"ilink_bot_id"`
		BaseURL      string `json:"baseurl"`
		UserID       string `json:"ilink_user_id"`
		RedirectHost string `json:"redirect_host"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return ilinkQRStatus{Status: ilinkStatusWait}
	}
	if strings.TrimSpace(r.Status) == "" {
		r.Status = ilinkStatusWait
	}
	return ilinkQRStatus{
		Status:       strings.TrimSpace(r.Status),
		BotToken:     strings.TrimSpace(r.BotToken),
		BotID:        strings.TrimSpace(r.BotID),
		BaseURL:      strings.TrimSpace(r.BaseURL),
		UserID:       strings.TrimSpace(r.UserID),
		RedirectHost: strings.TrimSpace(r.RedirectHost),
		Raw:          string(raw),
	}
}

// ------------------------------------------------------------ 收消息 ---

// ilinkMessage 一条收到的消息
type ilinkMessage struct {
	Seq          int
	MessageID    int64
	FromUserID   string
	ToUserID     string
	ContextToken string
	MessageType  int // 1 = 真人用户，2 = Bot
	MessageState int // 0 新增 / 1 生成中 / 2 完成
	Text         string
	VoiceText    string
	ItemTypes    []int
	Raw          string
}

// fromUser 是否真人用户发的（不是机器人自己发出去的）
func (m ilinkMessage) fromUser() bool { return m.MessageType == 1 }

// effectiveText 可读文本（文本优先，其次语音转写）
func (m ilinkMessage) effectiveText() string {
	if strings.TrimSpace(m.Text) != "" {
		return m.Text
	}
	return m.VoiceText
}

// ilinkUpdates 一次长轮询的结果
type ilinkUpdates struct {
	Ret       int
	ErrCode   int
	ErrMsg    string
	Msgs      []ilinkMessage
	Buf       string
	TimeoutMS int
	Raw       string
}

// getUpdates 长轮询拉新消息（buf 为空表示从头开始 / 重新建立会话）。
func (c *ilinkClient) getUpdates(ctx context.Context, buf string) (*ilinkUpdates, error) {
	body := map[string]any{
		"get_updates_buf": buf,
		"base_info":       ilinkBaseInfo(),
	}
	raw, err := c.do(ctx, http.MethodPost, "/ilink/bot/getupdates", body, ilinkTimeoutLong)
	if err != nil {
		return nil, err
	}
	var r struct {
		Ret       int               `json:"ret"`
		ErrCode   int               `json:"errcode"`
		ErrMsg    string            `json:"errmsg"`
		Msgs      []json.RawMessage `json:"msgs"`
		Buf       string            `json:"get_updates_buf"`
		TimeoutMS int               `json:"longpolling_timeout_ms"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("解析 getupdates 响应失败: %w（%s）", err, truncateLog(string(raw), 200))
	}
	out := &ilinkUpdates{
		Ret: r.Ret, ErrCode: r.ErrCode, ErrMsg: r.ErrMsg,
		Buf: r.Buf, TimeoutMS: r.TimeoutMS, Raw: string(raw),
	}
	for _, item := range r.Msgs {
		out.Msgs = append(out.Msgs, parseILinkMessage(item))
	}
	return out, nil
}

// parseILinkMessage 把一条消息 JSON 解析成 ilinkMessage
func parseILinkMessage(raw json.RawMessage) ilinkMessage {
	var m struct {
		Seq          int    `json:"seq"`
		MessageID    int64  `json:"message_id"`
		FromUserID   string `json:"from_user_id"`
		ToUserID     string `json:"to_user_id"`
		ContextToken string `json:"context_token"`
		MessageType  int    `json:"message_type"`
		MessageState int    `json:"message_state"`
		Items        []struct {
			Type     int `json:"type"`
			TextItem *struct {
				Text string `json:"text"`
			} `json:"text_item"`
			VoiceItem *struct {
				Text string `json:"text"`
			} `json:"voice_item"`
		} `json:"item_list"`
	}
	out := ilinkMessage{Raw: string(raw)}
	if err := json.Unmarshal(raw, &m); err != nil {
		return out
	}
	out.Seq = m.Seq
	out.MessageID = m.MessageID
	out.FromUserID = m.FromUserID
	out.ToUserID = m.ToUserID
	out.ContextToken = m.ContextToken
	out.MessageType = m.MessageType
	out.MessageState = m.MessageState
	for _, it := range m.Items {
		out.ItemTypes = append(out.ItemTypes, it.Type)
		switch it.Type {
		case 1:
			if it.TextItem != nil && out.Text == "" {
				out.Text = it.TextItem.Text
			}
		case 3:
			if it.VoiceItem != nil && out.VoiceText == "" {
				out.VoiceText = it.VoiceItem.Text
			}
		}
	}
	return out
}

// ------------------------------------------------------------ 发消息 ---

// sendText 给指定用户发一条文本消息（推短信就是走这里）。
// clientID 为空时自动生成（32 位 hex）。
func (c *ilinkClient) sendText(ctx context.Context, toUserID, text, contextToken, clientID string) error {
	to := strings.TrimSpace(toUserID)
	if to == "" {
		return fmt.Errorf("推送目标为空（不知道发给谁）")
	}
	if strings.TrimSpace(clientID) == "" {
		clientID = randomHex(16)
	}
	item := map[string]any{
		"type":      1,
		"text_item": map[string]any{"text": text},
	}
	msg := map[string]any{
		"from_user_id":  "",
		"to_user_id":    to,
		"client_id":     clientID,
		"message_type":  2, // BOT
		"message_state": 2, // FINISH
		"item_list":     []any{item},
	}
	if strings.TrimSpace(contextToken) != "" {
		msg["context_token"] = contextToken
	}
	body := map[string]any{"msg": msg, "base_info": ilinkBaseInfo()}

	raw, err := c.do(ctx, http.MethodPost, "/ilink/bot/sendmessage", body, ilinkTimeoutAPI)
	if err != nil {
		return err
	}
	var r struct {
		Ret    int    `json:"ret"`
		ErrMsg string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("解析 sendmessage 响应失败: %w（%s）", err, truncateLog(string(raw), 200))
	}
	if r.Ret != 0 {
		return fmt.Errorf("发送失败：ret=%d errmsg=%s", r.Ret, r.ErrMsg)
	}
	return nil
}

// notify 上/下线通知（notifystart / notifystop），best-effort
func (c *ilinkClient) notify(ctx context.Context, path string) error {
	body := map[string]any{"base_info": ilinkBaseInfo()}
	_, err := c.do(ctx, http.MethodPost, path, body, ilinkTimeoutConfig)
	return err
}

// ------------------------------------------------------------ 小工具 ---

// randomHex 生成 n 字节的随机 hex 串（sendmessage 的 client_id 用 16 字节 = 32 hex）
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%0*x", n*2, time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// 注：truncateLog（截断过长文本）现在的实现在 config.go，别名定义在 wechat.go。

// ---- from: wechat/wechat.go ----

// SMS 短信类型（parse_test.go 的领域类型，这里起个别名，模板渲染与指纹去重都用它）

// truncateLog 截断过长文本（实现已上收到 config.go，这里保留旧名字）
var truncateLog = config.Truncate

// ModemInfo 渲染推送模板/记录指纹时要用的模组信息。
//
// 本包不认识 *Modem（那是引擎的东西）：kernel 组装好这个结构再传进来，
// 所以微信这块能完全独立成包、单独测试。
type ModemInfo struct {
	ID        int    // 模组序号（模板里的 #0）
	ATPort    string // AT 串口名
	Model     string // ATI 原文
	ModelName string // 从 ATI 解析出的短型号（如 MT5700M-CN）
	IMEI      string
}

// OnChange 状态变化回调（kernel 用来通过 WebSocket 推给页面）；nil = 不推。
// 注册在 glue.go 的 InitWeChatRuntime 里。
var OnChange func(WeChatView)

// notifyChange 状态变了：通知上层（没有回调时就什么也不做）
func notifyChange() {
	if OnChange != nil {
		OnChange(wechatView())
	}
}

// HasToken 是否已扫码登录（拿过 bot_token）—— 接口层用它判定“还没登录”
func HasToken() bool {
	r := wechatInst
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg.BotToken != ""
}

// Snapshot 面板数据（配置 + 登录状态 + 最近发送结果）。
// 外部（api.go）通过兼容别名 wechatView() 调用它。
func Snapshot() WeChatView { return wechatView() }
