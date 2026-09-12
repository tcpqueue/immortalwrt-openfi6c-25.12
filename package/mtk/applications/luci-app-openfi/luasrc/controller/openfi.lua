module("luci.controller.openfi", package.seeall)
local http = require("luci.http")
local i18n = require("luci.i18n")
local sys = require "luci.sys"
local jsc = require "luci.jsonc"
local nfs = require "nixio.fs"

function index()
    entry({"admin", "openfi"}, firstchild(), _("OpenFi"), 25).dependent=false
    entry({"admin", "openfi", "openfi6"}, alias("admin", "openfi", "openfi6", "settings"), _("OpenFi6"), 100).dependent = true
    entry({"admin", "openfi", "openfi6", "settings"},cbi("openfi/settings"),luci.i18n.translate("OpenFi Settings"),3).leaf = true

    entry({"admin", "openfi", "openfi6", "status"}, call("fan_status")).leaf = true

    local page
    page = entry({"admin", "op_help", "mfg_info"}, template("openfi/mfg_info"), _("mfg_info")); page.dependent = false; page.sysauth = false; page.hidden = true
end



function fan_status()
    local data = jsc.parse(nfs.readfile("/var/run/openfi-fan.json") or "")
    if type(data) ~= "table" then data = {} end
    data.stale = not tonumber(data.updated) or os.time() - data.updated > math.max(15, (tonumber(data.period) or 5) * 3)
    if data.stale then
        local raw = tonumber(nfs.readfile("/sys/class/thermal/thermal_zone0/temp"))
        data.cpu = raw and raw >= 0 and raw <= 150000 and math.floor(raw / 1000) or nil
        data.output = nil
        data.target = nil
    end
    data.pwm_present = nfs.access("/sys/class/pwm/pwmchip0/export") and true or false
    local services = jsc.parse(sys.exec("ubus call service list '{\"name\":\"openfi\"}' 2>/dev/null")) or {}
    local instances = services.openfi and services.openfi.instances or {}
    data.running = instances.fan and instances.fan.running == true or false
    http.header("Cache-Control", "no-store")
    http.prepare_content("application/json")
    http.write(jsc.stringify(data))
end
