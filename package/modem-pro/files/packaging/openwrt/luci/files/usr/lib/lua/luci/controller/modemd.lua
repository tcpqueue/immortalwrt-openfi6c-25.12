-- modemd 5G 多模组管理器 : LuCI (Lua) 入口
--
-- 菜单结构（独立的一级菜单，不在"服务"下面）：
--   admin/modemd            -> 5G 模组管理（父节点，落到第一个子页）
--     admin/modemd/overview -> 概览：服务状态 + 内嵌 modemd 自带网页
--     admin/modemd/service  -> 服务控制：启动/停止/重启/开机自启
--   （admin/modemd/status、admin/modemd/ctrl 是给页面用的数据/动作端点，leaf，不进菜单）
--
-- 为什么 LUA 版要单独写一套：菜单注册有两套机制，取决于固件里的 LuCI 是哪一代 ——
--   * Lua LuCI（有 luci-lua-runtime / luci-mod-admin-full，老 QWRT、19.07 系固件）：
--     菜单只能靠本目录的 controller/*.lua 用 entry() 注册；
--   * JS LuCI（OpenWrt 21.02+）：读 /usr/share/luci/menu.d/*.json。
-- 这台 Hlink H28K/QWRT 是 Lua 版，所以两套都放进包里，谁在就用谁。
--
-- !!! 四条真机踩出来的规矩，改这个文件时务必保留 !!!
--
-- 1) **本文件不允许出现"文件级 local"**（local 变量 / local function）。
--    原因：LuCI 的 createindex() 会把每个控制器的 index() 存成字节码写进
--    /tmp/luci-indexcache（util.get_bytecode / loadfile），而 string.dump **不保存
--    upvalue**。于是缓存命中后 index() 的 upvalue 全是 nil：
--      `attempt to call upvalue 'T' (a nil value)` / `attempt to index upvalue 'disp' (a nil value)`
--    整棵菜单树直接 500。固件自带的 msd_lite.lua / modemdata.lua 通篇零文件级 local，也是这个道理。
--    → 需要的东西全部在**函数内部**取（函数内部的 local 每次调用重新创建，安全）。
--
-- 2) **index() 的环境不是本模块表，而是 `setmetatable({}, {__index = luci.dispatcher})`**
--    （见 luci.dispatcher.createtree()：`local scope = ...; setfenv(v, scope); v()`）。
--    所以在 index() 里：
--      ✓ 可以直接用 luci.dispatcher 的成员：entry / template / call / post / firstchild
--      ✓ 可以直接用 _G 全局：require、io、os、luci、以及本包注册的 modemdT / modemdTF
--      ✗ **不能**直接用本模块自己定义的函数（modemdPort 等）——它们只存在于模块表里，
--        scope 里没有，_G 里也没有 ⇒ `attempt to call global 'modemdPort' (a nil value)`。
--    → 要拿本模块的函数，就在函数里 `local M = require("luci.controller.modemd")` 再用 M.xxx。
--      在 index() 里创建的闭包同理（闭包继承 index() 的环境），所以下面 modemdUIURL 自己 require 一次。
--
-- 3) 顶层不要 require 任何东西：require 一律放在 index() / handler 里。
--
-- 4) 改了控制器/模板要清 LuCI 缓存：/tmp/luci-indexcache* + /tmp/luci-modulecache（目录，rm -rf）。

module("luci.controller.modemd", package.seeall)

function index()
	-- 本模块表句柄：index() 的环境（scope）里没有本模块的函数，必须这样取（见规矩 2）
	local M = require("luci.controller.modemd")

	-- 载入翻译表（顺便把 modemdT / modemdTF 注册成 _G 全局，模板/这里都能直接用）
	require("luci.modemd_i18n")

	-- 注册全局助手："modemd 自带网页的地址"，模板里用 <iframe src="<%=modemdUIURL()%>">
	-- 闭包内自己 require，不依赖任何 upvalue（规矩 1 + 2）
	_G.modemdUIURL = function()
		local MM   = require("luci.controller.modemd")
		local http = require("luci.http")
		local host = http.getenv("SERVER_NAME") or http.getenv("HTTP_HOST") or ""
		host = tostring(host):gsub(":%d+$", "")
		if host == "" then host = "192.168.1.1" end
		local port, scheme = MM.modemdPort()
		return scheme .. "://" .. host .. ":" .. port .. "/"
	end
	_G.modemdConfigIntervals = function()
		local MM = require("luci.controller.modemd")
		return MM.modemdIntervals()
	end

	-- 独立的一级菜单：admin -> "5G 模组管理"（order 26：紧跟固件自带的「Modem」）
	entry({"admin", "modemd"}, firstchild(), modemdT("5G Terminal"), 26).dependent = false

	-- 概览（第一个子页，也是点父菜单时的落地页）
	entry({"admin", "modemd", "overview"},
		template("modemd/overview"), modemdT("Overview"), 1).dependent = true

	-- 服务控制（单独一页）
	entry({"admin", "modemd", "service"},
		template("modemd/service"), modemdT("Service Control"), 2).dependent = true

	-- 数据/动作端点（leaf：不会出现在菜单里）
	entry({"admin", "modemd", "status"}, call("action_status")).leaf = true
	entry({"admin", "modemd", "ctrl"}, post("action_ctrl")).leaf = true
	entry({"admin", "modemd", "intervals"}, call("action_intervals")).leaf = true
	entry({"admin", "modemd", "intervals_save"}, post("action_intervals_save")).leaf = true

	-- 让 M 被使用，避免"未使用变量"的误导（也方便以后加自检）
	M.controller_ready = true
end

-- modemdPort 读 /etc/modemd/config.toml 的 [server] listen（并返回当前监听使用的协议）。
-- （写成模块函数而不是文件级 local，见文件头规矩 1）
-- ⚠️ config.toml 里有**两个** listen：`[server] listen` 是地址，`[wechat] listen = true` 是布尔开关
--    ⇒ 必须只在 [server] 段里找，不能全文匹配第一个 ":端口"。
function modemdPort()
    local port, scheme = 9527, "http"
    local f = io.open("/etc/modemd/config.toml", "r")
    if f then
            local s = f:read("*a") or ""
            f:close()
            local rest = s:match("%\[server%\][^\n]*\n(.*)") or ""
            local sec  = rest:match("^(.-)\n%[") or rest
            local p = sec:match('listen%s*=%s*["\']?[^"\'\n]*:(%d+)')
		if p then port = tonumber(p) or port end
        local enabled = sec:match('\ntls_enabled%s*=%s*(%a+)')
        local cert = sec:match('\ntls_cert_file%s*=%s*"([^"]+)"')
        local key = sec:match('\ntls_key_file%s*=%s*"([^"]+)"')
        if enabled == "true" or (enabled == nil and cert and key) then
            scheme = "https"
        end
	end
	return port, scheme
end

function modemdIntervals()
	local values = { push_interval = 1, network_interval = 3, traffic_interval = 1 }
	local f = io.open("/etc/modemd/config.toml", "r")
	if not f then return values end
	local section = ""
	for line in f:lines() do
		local name = line:match("^%s*%[([^%]]+)%]")
		if name then
			section = name
		elseif section == "server" then
			local key, value = line:match("^%s*([%w_]+)%s*=%s*(-?%d+)")
			if key and values[key] ~= nil then values[key] = tonumber(value) end
		end
	end
	f:close()
	return values
end

function modemdWriteIntervals(values)
	local path = "/etc/modemd/config.toml"
	local current = ""
	local f = io.open(path, "r")
	if f then current = f:read("*a") or ""; f:close() end

	local lines, found, serverStart, serverEnd = {}, {}, nil, nil
	local inServer = false
	for line in (current .. "\n"):gmatch("(.-)\n") do
		line = line:gsub("\r$", "")
		local name = line:match("^%s*%[([^%]]+)%]%s*$")
		if name then
			if inServer and not serverEnd then serverEnd = #lines end
			inServer = name == "server"
			if inServer then serverStart = #lines + 1 end
		end
		table.insert(lines, line)
		if inServer and not name then
			local key = line:match("^%s*([%w_]+)%s*=")
			if key and values[key] ~= nil then
				found[key] = true
				lines[#lines] = key .. " = " .. tostring(values[key])
			end
		end
	end
	if lines[#lines] == "" then table.remove(lines) end
	if serverEnd and serverEnd > #lines then serverEnd = #lines end
	if inServer and not serverEnd then serverEnd = #lines end
	if not serverStart then return false, "config.toml is missing the [server] section" end

	local keys = { "push_interval", "network_interval", "traffic_interval" }
	local missing = {}
	for _, key in ipairs(keys) do
		if not found[key] then table.insert(missing, key .. " = " .. tostring(values[key])) end
	end
	for i = #missing, 1, -1 do table.insert(lines, serverEnd + 1, missing[i]) end

	local tmp = path .. ".luci-tmp"
	local out, err = io.open(tmp, "w")
	if not out then return false, err or "cannot create temporary config" end
	out:write(table.concat(lines, "\n"), "\n")
	out:close()
	local fs = require("nixio.fs")
	if not fs.chmod(tmp, 384) then -- 0600: config may contain credentials
		os.remove(tmp)
		return false, "cannot secure temporary config file"
	end
	local ok, renameErr = os.rename(tmp, path)
	if not ok then os.remove(tmp); return false, renameErr or "cannot replace config" end
	return true
end

function action_intervals()
	local http = require("luci.http")
	http.prepare_content("application/json")
	http.write_json(modemdIntervals())
end

function action_intervals_save()
	local http = require("luci.http")
	local sys = require("luci.sys")
	local disp = require("luci.dispatcher")
	local function number(name, min, max)
		local value = tonumber(http.formvalue(name))
		if not value or value ~= math.floor(value) or value < min or value > max then return nil end
		return value
	end
	local values = {
		push_interval = number("push_interval", 0, 60),
		network_interval = number("network_interval", 1, 3600),
		traffic_interval = number("traffic_interval", 1, 60),
	}
	local err
	for _, key in ipairs({ "push_interval", "network_interval", "traffic_interval" }) do
		if values[key] == nil then err = key .. " is outside the allowed range"; break end
	end
	local ok = false
	if not err then ok, err = modemdWriteIntervals(values) end
	if ok then
		sys.init.stop("modemd")
		sys.init.start("modemd")
	end
	if not ok then
		http.status(400, "Bad Request")
		http.prepare_content("text/plain")
		http.write(err or "invalid interval configuration")
		return
	end
	http.redirect(disp.build_url("admin", "modemd", "service"))
end

-- action_status：页面每 3 秒拉一次（JSON）
-- 注意：handler 的环境是 dispatch() 造的代理表（rawget → 模块表 → 模块环境 → _G），
-- 模块函数能拿到；但 luci.sys / luci.http 不一定被其它页面加载过，所以显式 require。
function action_status()
	local M    = require("luci.controller.modemd")
	local sys  = require("luci.sys")
	local http = require("luci.http")

	local e = {
		running = (sys.call("pgrep -f '/usr/bin/modemd' >/dev/null 2>&1") == 0),
		enabled = (sys.init and sys.init.enabled) and sys.init.enabled("modemd") or false,
		port    = M.modemdPort(),
		pid     = 0,
	}

	local pid = tostring(sys.exec("pgrep -f '/usr/bin/modemd' 2>/dev/null | head -1") or "")
	e.pid = tonumber(pid:match("%d+") or "") or 0

	http.prepare_content("application/json")
	http.write_json(e)
end

-- action_ctrl：启动 / 停止 / 重启 / 开机自启 / 取消自启，然后回到服务控制页
function action_ctrl()
	local sys  = require("luci.sys")
	local http = require("luci.http")
	local disp = require("luci.dispatcher")

	local a = http.formvalue("action") or ""

	if a == "start" then
		sys.init.start("modemd")
	elseif a == "stop" then
		sys.init.stop("modemd")
	elseif a == "restart" then
		-- luci.sys.init 没有 restart：停一下再起（procd 会拉起来）
		sys.init.stop("modemd")
		sys.init.start("modemd")
	elseif a == "enable" then
		sys.init.enable("modemd")
	elseif a == "disable" then
		sys.init.disable("modemd")
	end

	http.redirect(disp.build_url("admin", "modemd", "service"))
end
