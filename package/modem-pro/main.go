// modemd 5G 模组调试台 —— 程序入口。
//
// 这里只做「装配」：读配置 → 初始化日志 → 写 PID → 起 HTTP 与后台循环。
// 领域逻辑（AT 收发 / 短信 / 锁网 / 推送 / 波动记录 / HTTP 接口…）都在 module/kernel/ 里。
// kernel 是**单一 package、按领域分文件**（不是一层层子包），常用文件：
//
//	module/kernel/api.go       HTTP 路由与接口        module/kernel/atcmd.go    AT 命令层
//	module/kernel/modem.go     模组对象与生命周期      module/kernel/query.go    查询/AT 收发
//	module/kernel/sms.go       短消息与 PDU            module/kernel/lock.go    锁网/锁频
//	module/kernel/quota.go     流量配额                module/kernel/schedule.go 定时任务
//	module/kernel/history.go   信号历史                module/kernel/push.go    WebSocket 推送
//	module/config/             配置与日志              module/cell/              小区与频段
//	module/netprov/            联网协议（MBIM/QMI）   module/luciauth/          跟随 LuCI 登录
//	module/httpx/              JSON 响应/请求体解码    module/registry/          厂商指令类型注册
//
// ⚠️ web.go 与 files/web/ 必须留在根目录：页面是 go:embed 打进二进制的，
// 而 embed 只能引用当前目录及子目录（不能写 ../files/web）。
package main

import (
	"modemd/module/atport"
	"modemd/module/config"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"modemd/module/kernel"
)

// logger 本包的日志门面（见 module/config/config.go）
var logger = config.Logger

func main() {
	config.ParseFlags()
	if err := config.Load(); err != nil {
		config.Init("", "info") // 配置读不出来也要有日志落地
		logger.Fatal("config error", zap.Error(err))
	}
	config.Init(config.Cfg.Server.LogFile, config.Cfg.Server.LogLevel)
	defer func() { _ = config.L().Sync() }()

	webConfig := config.Cfg.Server
	tlsConfig, err := webTLSConfig(config.ConfigPath, webConfig.TLSEnabled, webConfig.TLSCertFile, webConfig.TLSKeyFile, webConfig.TLSNames)
	if err != nil {
		logger.Fatal("web server config error", zap.Error(err))
	}

	if err := config.WritePidFile(config.Cfg.Server.PidFile); err != nil {
		logger.Fatal("write pid failed", zap.Error(err))
	}
	defer config.RemovePidFile(config.Cfg.Server.PidFile)

	mgr := kernel.NewModemManager()
	kernel.InitWS()
	// 流量配额：载入 quota.json（与配置文件同目录），退出前把用量落盘
	kernel.InitQuota()
	defer kernel.FlushQuota()
	// 定时器状态（定时列表 + 各槽位上次执行时间）同样落盘
	kernel.InitSchedule()
	defer kernel.FlushSchedule()
	// 微信机器人（iLink Bot）：凭据在 config.toml、游标/已推送指纹在 wechat.json。
	// 登录后监听与发送队列都是后台 goroutine（不看有没有人开页面）。
	kernel.InitWeChat()
	defer kernel.FlushWeChat()
	// IMEI 原始值备份（imei.json）：改写前自动存一份，之后可一键恢复；按模块分别记录。
	kernel.InitIMEIBackup()
	defer kernel.FlushIMEIBackup()
	// 手动指定的 AT 命令口（atport.json）：自动探测可能挑错口，用户在「AT 指令」页选过就记住。
	kernel.InitATPorts()
	// 按 USB 设备（VID:PID）绑定的命令口（atport_bind.json）：绑了就直接用、不再扫描。
	atport.InitATPortBinds()
	// 短信密码：仅提示“还在用默认密码”，避免用户以为自己已经保护上了
	kernel.LogSMSPasswordHint()
	srv := &http.Server{
		Addr:      webConfig.Listen,
		Handler:   kernel.RegisterRoutes(mgr, apiUI()),
		TLSConfig: tlsConfig,
	}

	// 启动时发现一次（否则不知道有哪些模组）
	kernel.DiscoverAndStart(mgr)

	// 后台周期巡检：默认关闭（scan_interval <= 0）—— **插拔自动检测就靠这个开关**。
	// 周期巡检会不断枚举串口、并对未知端口发 AT 探测（每个端口约 4s），
	// 属于"后台自动刷新"，所以不默认启用；要自动检测插拔就把 scan_interval 设为正数。
	// 页面的「刷新模组」只读缓存（不重探串口）；需要手动重探时用
	// GET /api/modems?discover=1（排障 / 脚本）。
	scanSec := config.Cfg.Server.ScanInterval
	if scanSec > 0 {
		scanTicker := time.NewTicker(time.Duration(scanSec) * time.Second)
		defer scanTicker.Stop()
		go func() {
			for range scanTicker.C {
				kernel.DiscoverAndStart(mgr)
			}
		}()
	} else {
		scanSec = 0
	}

	// WebSocket 周期推送：只服务已连接的客户端（没人连时不发 AT 指令）。
	// push_interval <= 0 时关闭，仅保留事件推送。
	if config.Cfg.Server.PushInterval > 0 {
		go kernel.PushLoop(mgr, time.Duration(config.Cfg.Server.PushInterval)*time.Second)
	}
	// 上下行速度单独用更短的节拍（默认 1s）：计数器差分采样越规律读数越实时
	if config.Cfg.Server.TrafficInterval > 0 {
		go kernel.TrafficLoop(mgr, time.Duration(config.Cfg.Server.TrafficInterval)*time.Second)
	}
	// 主机资源占用（CPU / 内存）+ 模组温度 —— 屏幕 INFO 页要的那几个数。
	// CPU/内存读的是 /proc（不吃串口）；温度按分钟级单独查一次（见 module/kernel/hostinfo.go）。
	// 固定 1 秒一拍，也不看有没有客户端：没人看时 broadcast 自己早退。
	go kernel.SysLoop(mgr, 1*time.Second)
	// 短信：**不设定时轮询**（用户要求去掉：2 分钟一轮太慢，屏幕等不及）。
	// 改成事件驱动，缓存由这几处填（都走 ReadSMS，全量替换）：
	//   · 模组开机启动探测完（module/kernel/modem.go 的 startupProbe）
	//   · 换卡 / 拔卡 / 插卡（module/kernel/simhotswap.go，换卡前先清缓存）
	//   · 收到新短信 +CMTI（module/kernel/modem.go，立刻读）
	//   · 屏幕的 /display/ws 连上来（module/kernel/displayws.go，连上就读一遍）
	// OpenWrt 自动组网：模组上线就拨号，并把模组那张网卡接成 WAN（UCI + 防火墙）——
	// 新插上的模组不用去 LuCI 手工建接口就能上网。其他平台是空操作。
	if kernel.AutoWANEnabled() {
		go kernel.AutoWANLoop(mgr, 15*time.Second)
	}
	// 波动记录的连续采样（默认关闭）：只在前端没连推送时跑，
	// 有人看的时候由推送循环顺便记录，不重复占串口。
	if config.Cfg.Server.ChangeInterval > 0 {
		go kernel.ChangeLoop(mgr, time.Duration(config.Cfg.Server.ChangeInterval)*time.Second)
	}
	// 运行日志：tail 文件把新行推给页面（纯文件读，不占串口；没人连时不读）
	if config.Cfg.Server.LogFile != "" {
		go kernel.LogPushLoop(config.Cfg.Server.LogFile)
	}
	// 流量配额：与其它循环不同，**不看有没有客户端在线** —— 配额是 24 小时都得生效的东西，
	// 没人开页面也得按量断网；代价是每轮一条 AT+QGDCNT?（60 秒一次，占用可忽略）。
	go kernel.QuotaLoop(mgr, 60*time.Second)

	// 定时器：同样不看有没有客户端在线（到点必须执行）。
	// 节拍 10 秒（最小粒度是分钟，10 秒足够准），到点后 15 分钟内都允许补做。
	go kernel.ScheduleLoop(mgr, 10*time.Second)

	// SIM 热插拔：模组带电拔卡 / 插卡 / 换卡 —— URC 立刻触发 + 周期兜底轮询，
	// 检出后重配“按卡”的短信设置，并按配置复位模组让新卡生效（见 simhotswap.go）。
	if config.Cfg.SIM.HotswapInterval > 0 {
		go kernel.SIMWatchLoop(mgr, config.Cfg.SIM.HotswapInterval)
	}

	logger.Info("modemd started",
		zap.String("addr", srv.Addr),
		zap.Bool("tls", srv.TLSConfig != nil),
		zap.Int("scan_interval_s", scanSec),
		zap.Int("push_interval_s", config.Cfg.Server.PushInterval),
		zap.Int("traffic_interval_s", config.Cfg.Server.TrafficInterval),
		zap.Int("change_interval_s", config.Cfg.Server.ChangeInterval),
		zap.String("websocket", "GET /ws"))

	// 退出前把主机侧拨号器（QMI 的 quectel-CM）收干净。
	//
	// 为什么要专门做：它占着 /dev/cdc-wdm0，modemd 一退（opkg 升级 / 停服务 / 重启）要是留着，
	// 下一次拨号就打不开控制口（真机踩过：残留进程把 QMI 设备占死，新拨号器一直报超时）。
	// procd 停服务发的就是 SIGTERM。
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		s := <-sigCh
		logger.Warn("signal received, stopping host dialers", zap.String("signal", s.String()))
		kernel.StopHostDials()
		// 直接退：跟以前被信号打死的行为一致（defer 本来也不会执行），
		// 只是多了一步"先把拨号器收干净"。
		os.Exit(0)
	}()

	serve := srv.ListenAndServe
	if srv.TLSConfig != nil {
		serve = func() error { return srv.ListenAndServeTLS("", "") }
	}
	if err := serve(); err != nil && err != http.ErrServerClosed {
		logger.Fatal("http server exit", zap.Error(err))
	}
}
