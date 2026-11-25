-- modemd LuCI 多语言（中文 / 繁体 / English）
--
-- ⚠️⚠️ **本文件里不允许出现任何"文件级 local"**（local 变量/函数），原因见下。
--
-- 【为什么】LuCI 的 ccache（/etc/config/luci 里 config internal 'ccache'）会把控制器/模块**按模块名**
-- 缓存编译后的字节码到 /tmp/luci-modulecache/，命中缓存时**不校验源文件是否变化**。
-- 真机实测（2026-09-30）：缓存命中时，文件级 local 被闭包捕获成 upvalue 后**值是 nil**
-- （现象：控制器 index() 里 `attempt to call upvalue 'T' (a nil value)` /
--  更早那次是 `attempt to index upvalue 'disp'`，整棵菜单树 500）。
-- 固件里自带的 Lua 应用（msd_lite.lua / modemdata.lua）**通篇零文件级 local**，也印证了这条规则。
-- 所以这里：
--   * 不用 local，全部写成"模块表里的字段"（module(...) 之后裸名就是字段 ✓）；
--   * 对外暴露两个**全局**函数 modemdT / modemdTF（模板与控制器里直接裸名调用，
--     靠 module(...) 的 __index 链 luci.dispatcher -> _G 也能找到 ✓）。
-- 语言选择顺序：luci.i18n.context.lang（请求里 LuCI 已设好，注意是 threadlocal）
--              → /etc/config/luci 的 luci.main.lang → en（返回英文原文）。
--
-- 为什么不直接用 LuCI 的 /usr/lib/lua/luci/i18n/*.lmo：这台固件的 luci.template.parser 是 C 模块，
-- .lmo 是哈希+二进制布局（要 po2lmo 之类的构建工具生成），实测把纯文本 Lua 表命名为 .lmo
-- 放进去 load_catalog() 返回 true 但 translate() 查不到 —— 所以自带表最稳。

module("luci.modemd_i18n", package.seeall)

-- 键 = 英文原文；缺键自动回落英文（英文表就是空的）
STRINGS = {
	["zh-cn"] = {
		["modemd 5G Modem"] = "5G终端",
		["5G Terminal"] = "5G终端",
		["Overview"] = "设备中心",
		["Service Control"] = "服务控制",
		["Actions"] = "操作",
		["Console"] = "调试台",

		["Floating widget"] = "悬浮小窗",
		["Enable"] = "开启",
		["Disabled"] = "已关闭",
		["Enabled — shown on every LuCI page"] = "已开启 —— 所有 LuCI 页面都会显示",
		["Current form"] = "当前形态",
		["window"] = "小窗",
		["title bar only"] = "只有标题条",
		["small badge"] = "小角标",
		["Re-show the floating widget (reset position and form)"] = "恢复显示悬浮小窗（重置位置与形态）",
		["Show a floating window at the bottom-left of every LuCI page: a dark bar with a cyan gradient, labelled “modemd”. It shows the operator, the basic signal (RSRP/SINR/RSRQ) and the unread-SMS badge, read live from the device center."] =
			"在 LuCI 的每个页面左下角显示一个小窗：深色底 + 青色渐变标题条，标题写着「5G终端悬浮小窗」。里面只显示三样东西 —— 运营商、基本信号（RSRP/SINR/RSRQ）、短信未读角标，数据从设备中心实时读取。",
		["You can drag it, minimise it to the title bar, or click × to shrink it into a small badge — click that badge to bring the window back (it is never lost)."] =
			"可以拖动；点 – 只留标题条；点 × 收成一个小角标 —— 点那个角标就能把窗重新打开（不会再出现「关了就找不回来」）。",
		["This switch is stored in this browser (localStorage), so enable it once per computer."] =
			"这个开关存在浏览器本地（localStorage），每台电脑各开一次。",

		["Service"] = "服务",
		["Running"] = "运行中",
		["Stopped"] = "未运行",
		["Enabled at boot"] = "已设开机自启",
		["Not enabled at boot"] = "未设开机自启",
		["Reading…"] = "读取中…",
		["Web UI port"] = "网页端口",
		["modemd web UI (embedded)"] = "设备中心（内嵌）",
		["Open in a new window"] = "在新窗口打开",
		["This is modemd's own web UI: modem list, signal, cells, SMS, dialing and automatic WAN provisioning."] =
			"下面是 modemd 自带的界面：模组列表、信号、小区、短信、拨号、自动组网都在里面。",
		["Service state is refreshed every 3 seconds."] = "服务状态每 3 秒自动刷新。",

		["Status"] = "状态",
		["Start"] = "启动",
		["Stop"] = "停止",
		["Restart"] = "重启",
		["Enable at boot"] = "设为开机自启",
		["Disable at boot"] = "取消开机自启",
		["Config file"] = "配置文件",
		["Log file"] = "日志文件",
		["System log"] = "系统日志",
		["About"] = "关于",
		["Package"] = "软件包",
		["Author"] = "作者",
		["Start or stop the modemd daemon, or change whether it starts at boot."] =
			"这里可以启停 modemd 服务，或改它是否随开机自启。",
		["Configuration is read at startup — press Restart to apply changes."] =
			"配置在启动时读取 —— 改完点「重启」生效。",
		["Refresh intervals"] = "刷新间隔",
		["Signal push interval (seconds)"] = "信号推送间隔（秒）",
		["Network/operator interval (seconds)"] = "网络/运营商状态间隔（秒）",
		["Upload/download speed interval (seconds)"] = "上下行速率采样间隔（秒）",
		["Save intervals and restart"] = "保存并重启服务",
		["Push interval 0 disables periodic signal and network updates. Network interval applies while periodic updates are enabled."] =
			"信号间隔设为 0 会关闭周期信号和网络更新；网络间隔在周期更新开启时生效。",
		["Invalid interval value: "] = "间隔值无效：",
		["Intervals saved and service restarted."] = "间隔已保存，服务已重启。",
		["Failed to save intervals: "] = "保存间隔失败：",
		["The daemon is a separate HTTP service on port %s."] = "守护进程是独立的 HTTP 服务，端口 %s。",

		["Loading the 5G Terminal…"] = "正在加载 5G 终端…",
		["Waiting for the modemd service to start…"] = "正在等待 modemd 服务启动…",
		["Still waiting — the service usually takes a few seconds after a reboot."] =
			"仍在等待 —— 路由器刚重启时，服务通常需要几秒到几十秒。",
		["No response after 1 minute. The service may be stopped or failed to start."] =
			"已等待 1 分钟仍无响应：服务可能已停止或启动失败。",
		["Restart service"] = "重启服务",
		["Restarting…"] = "正在重启…",
		["Service restart requested. Waiting for it to come back…"] =
			"已请求重启服务，正在等待它恢复…",
		["Failed to restart the service: "] = "重启服务失败：",
		["The daemon may not be installed. Check the Service Control page."] =
			"守护进程可能未安装，请到「服务控制」页检查。",
	},

	["zh-tw"] = {
		["modemd 5G Modem"] = "5G終端",
		["5G Terminal"] = "5G終端",
		["Overview"] = "設備中心",
		["Service Control"] = "服務控制",
		["Actions"] = "操作",
		["Console"] = "偵錯台",

		["Floating widget"] = "懸浮小窗",
		["Enable"] = "開啟",
		["Disabled"] = "已關閉",
		["Enabled — shown on every LuCI page"] = "已開啟 —— 所有 LuCI 頁面都會顯示",
		["Current form"] = "目前形態",
		["window"] = "小窗",
		["title bar only"] = "只有標題列",
		["small badge"] = "小角標",
		["Re-show the floating widget (reset position and form)"] = "恢復顯示懸浮小窗（重設位置與形態）",
		["Show a floating window at the bottom-left of every LuCI page: a dark bar with a cyan gradient, labelled “modemd”. It shows the operator, the basic signal (RSRP/SINR/RSRQ) and the unread-SMS badge, read live from the device center."] =
			"在 LuCI 的每個頁面左下角顯示一個小窗：深色底 + 青色漸層標題列，標題寫著「5G終端懸浮小窗」。裡面只顯示三樣東西 —— 電信商、基本訊號（RSRP/SINR/RSRQ）、簡訊未讀角標，資料從裝置中心即時讀取。",
		["You can drag it, minimise it to the title bar, or click × to shrink it into a small badge — click that badge to bring the window back (it is never lost)."] =
			"可以拖動；點 – 只留標題列；點 × 收成一個小角標 —— 點那個角標就能把窗重新打開（不會再出現「關了就找不回來」）。",
		["This switch is stored in this browser (localStorage), so enable it once per computer."] =
			"這個開關存在瀏覽器本機（localStorage），每台電腦各開一次。",

		["Service"] = "服務",
		["Running"] = "執行中",
		["Stopped"] = "未執行",
		["Enabled at boot"] = "已設開機自啟",
		["Not enabled at boot"] = "未設開機自啟",
		["Reading…"] = "讀取中…",
		["Web UI port"] = "網頁連接埠",
		["modemd web UI (embedded)"] = "設備中心（內嵌）",
		["Open in a new window"] = "另開視窗",
		["This is modemd's own web UI: modem list, signal, cells, SMS, dialing and automatic WAN provisioning."] =
			"下面是 modemd 自帶的介面：模組清單、訊號、小區、簡訊、撥號、自動組網都在裡面。",
		["Service state is refreshed every 3 seconds."] = "服務狀態每 3 秒自動更新。",

		["Status"] = "狀態",
		["Start"] = "啟動",
		["Stop"] = "停止",
		["Restart"] = "重新啟動",
		["Enable at boot"] = "設為開機自啟",
		["Disable at boot"] = "取消開機自啟",
		["Config file"] = "設定檔",
		["Log file"] = "日誌檔",
		["System log"] = "系統日誌",
		["Start or stop the modemd daemon, or change whether it starts at boot."] =
			"這裡可以啟停 modemd 服務，或改它是否隨開機自啟。",
		["Configuration is read at startup — press Restart to apply changes."] =
			"設定在啟動時讀取 —— 改完按「重新啟動」生效。",
		["Refresh intervals"] = "重新整理間隔",
		["Signal push interval (seconds)"] = "訊號推送間隔（秒）",
		["Network/operator interval (seconds)"] = "網路/電信商狀態間隔（秒）",
		["Upload/download speed interval (seconds)"] = "上下行速率取樣間隔（秒）",
		["Save intervals and restart"] = "儲存並重新啟動服務",
		["Push interval 0 disables periodic signal and network updates. Network interval applies while periodic updates are enabled."] =
			"訊號間隔設為 0 會關閉週期訊號與網路更新；網路間隔在週期更新開啟時生效。",
		["Invalid interval value: "] = "間隔值無效：",
		["Intervals saved and service restarted."] = "間隔已儲存，服務已重新啟動。",
		["Failed to save intervals: "] = "儲存間隔失敗：",
		["The daemon is a separate HTTP service on port %s."] = "守護行程是獨立的 HTTP 服務，連接埠 %s。",

		["Loading the 5G Terminal…"] = "正在載入 5G 終端…",
		["Waiting for the modemd service to start…"] = "正在等待 modemd 服務啟動…",
		["Still waiting — the service usually takes a few seconds after a reboot."] =
			"仍在等待 —— 路由器剛重啟時，服務通常需要幾秒到數十秒。",
		["No response after 1 minute. The service may be stopped or failed to start."] =
			"已等待 1 分鐘仍無回應：服務可能已停止或啟動失敗。",
		["Restart service"] = "重啟服務",
		["Restarting…"] = "正在重啟…",
		["Service restart requested. Waiting for it to come back…"] =
			"已請求重啟服務，正在等待它恢復…",
		["Failed to restart the service: "] = "重啟服務失敗：",
		["The daemon may not be installed. Check the Service Control page."] =
			"守護行程可能未安裝，請到「服務控制」頁檢查。",
	},

	["en"] = {},
}

-- language 当前语言（形如 "zh-cn"）
function language()
	local ok, i18n = pcall(require, "luci.i18n")
	if ok and type(i18n) == "table" and type(i18n.context) == "table" and i18n.context.lang then
		return (tostring(i18n.context.lang):gsub("_", "-"):lower())
	end

	local f = io.open("/etc/config/luci", "r")
	if f then
		local s = f:read("*a") or ""
		f:close()
		local l = s:match("option%s+lang%s+['\"]([%w%-_]+)['\"]")
		if l then
			return (l:gsub("_", "-"):lower())
		end
	end

	return "en"
end

-- t 翻译一个键；查不到就返回原文（英文）
function t(key)
	if type(key) ~= "string" then
		return key
	end
	local lang = language()
	local tbl = STRINGS[lang] or STRINGS[lang:match("^([a-z][a-z])")]
	if type(tbl) == "table" and tbl[key] ~= nil then
		return tbl[key]
	end
	return key
end

-- tf 带 %s 之类的格式化翻译
function tf(key, ...)
	local fmt = t(key)
	if select("#", ...) == 0 then
		return fmt
	end
	local args = { ... }
	local up = unpack or table.unpack
	local ok, out = pcall(string.format, fmt, up(args))
	if ok then
		return out
	end
	return fmt
end

-- 暴露成全局：模板（<%=modemdT("Overview")%>）和控制器（index 里的 scope 链会走到 _G）都能直接叫
_G.modemdT  = t
_G.modemdTF = tf
