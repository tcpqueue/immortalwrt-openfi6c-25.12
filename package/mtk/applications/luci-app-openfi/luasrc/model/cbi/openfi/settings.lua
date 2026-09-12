local m = Map("openfi", translate("OpenFi 6C"), translate("CPU 温控、设备开关与硬件设置。修改后点击“保存并应用”生效。"))
m:section(SimpleSection).template = "openfi/status"

local fan = m:section(NamedSection, "fan", "fan", translate("风扇控制"))
fan.addremove = false
fan:tab("control", translate("调速方式"))
fan:tab("curve", translate("温控曲线"))

local mode = fan:taboption("control", ListValue, "mode", translate("控制模式"))
mode:value("auto", translate("自动 · 跟随 CPU 温度"))
mode:value("manual", translate("手动 · 固定 PWM 输出"))
mode.default = "auto"
mode.rmempty = false
mode.description = translate("手动输出与自动曲线分别保存，切换模式不会重置曲线。保存并应用后，风扇在下一次采样时采用新设置。")

local speed = fan:taboption("control", Value, "manual_speed", translate("手动输出"))
speed.template = "openfi/range"
speed.datatype = "range(0,100)"
speed.default = "55"
speed.rmempty = false
speed:depends("mode", "manual")
speed.description = translate("0% 请求停转；非零输出不会低于最低稳定输出。CPU 达到强制全速温度时仍全速保护。")

local minimum = fan:taboption("control", Value, "min_speed", translate("最低稳定输出"))
minimum.template = "openfi/range"
minimum.minimum = 5
minimum.datatype = "range(5,100)"
minimum.rmempty = false
minimum.description = translate("默认 5%，同时约束手动输出和自动曲线。此项不是手动转速；调高后若与曲线冲突会提示修正，不会覆盖曲线。风扇从停转状态起转时全速助推 1 秒。")
function minimum.cfgvalue(self, section)
    local value = Value.cfgvalue(self, section)
    if value then return value end
    local old = m.uci:get("openfi", "fan", "level")
    return old == "0" and "43" or old == "1" and "55" or old == "2" and "100" or "5"
end

local function integer(value, fallback, lower, upper)
    local n = tonumber(value)
    if not n or n ~= math.floor(n) or n < lower or n > upper then return fallback end
    return n
end
local oldlow = integer(m.uci:get("openfi", "fan", "cpu_temp_low"), 55, 20, 95)
local oldhigh = integer(m.uci:get("openfi", "fan", "cpu_temp_high"), 65, 25, 100)
if oldhigh - oldlow < 6 then oldlow=55; oldhigh=65 end
local floor = integer(minimum:cfgvalue("fan"), 5, 5, 100)
local defaults = {}
local points = {}
for i=1,4 do
    defaults["temp"..i] = tostring(oldlow + math.floor((oldhigh-oldlow)*(i-1)/3))
    defaults["speed"..i] = tostring(floor + math.floor((100-floor)*(i-1)/3))
end
local curve = fan:taboption("curve", DummyValue, "_curve")
curve.template = "openfi/curve"

for i=1,4 do
    for _, prefix in ipairs({"temp", "speed"}) do
        local name = prefix..i
        local o = fan:taboption("curve", Value, name)
        o.template = "openfi/hidden"
        o.rmempty = false
        o.default = defaults[name]
        points[name] = o
    end
end
local stop = fan:taboption("curve", Flag, "fan_stop", translate("低温停转"))
stop.default = "1"
stop.template = "openfi/stop"
stop.rmempty = false
stop.description = translate("启用时，降至第一个温度点以下 2°C 后逐步停转，回升至第一个点时起转。关闭后，低温保持第一个点的输出。")
local emergency = fan:taboption("curve", Value, "emergency_temp", translate("强制全速温度（°C）"))
emergency.default = "85"
emergency.template = "openfi/number"
emergency.rmempty = false
emergency.description = translate("独立于四点曲线，自动和手动模式均生效。需至少高于最后一个温度点 2°C。")
local function submitted(o, section)
    return o:formvalue(section) or o:cfgvalue(section) or o.default
end
local function validate_curve(self, value, section)
    local min = integer(submitted(minimum, section), nil, 5, 100)
    local limit = integer(submitted(emergency, section), nil, 60, 100)
    local last_t, last_s = 18, min
    if not min or not limit then return nil, translate("请检查最低稳定输出和强制全速温度。") end
    for i=1,4 do
        local t = integer(submitted(points["temp"..i], section), nil, 20, 95)
        local v = integer(submitted(points["speed"..i], section), nil, min, 100)
        if not t or not v or t < last_t + 2 or v < last_s then
            return nil, translate("四个温度点需相隔至少 2°C，输出须为整数且不低于最低稳定输出，并随温度保持不变或升高。")
        end
        last_t, last_s = t, v
    end
    if limit < last_t + 2 then return nil, translate("强制全速温度需至少高于第四个温度点 2°C。") end
    return tostring(tonumber(value))
end
for _, o in pairs(points) do o.validate=validate_curve end
emergency.validate=validate_curve

local period = fan:taboption("curve", Value, "period", translate("采样间隔（秒）"))
period.datatype = "range(2,30)"
period.default = "5"
period.rmempty = false
period.description = translate("升温立即响应；每次采样最多降速 10 个百分点。CPU 温度不可读时全速保护。")
for _, option in ipairs({speed, minimum, period}) do
    function option.validate(self, value)
        local n = tonumber(value)
        local lower = self == minimum and 5 or self == period and 2 or 0
        local upper = self == period and 30 or 100
        if not n or n ~= math.floor(n) or n < lower or n > upper then
            return nil, string.format(translate("%s：请输入 %d–%d 之间的整数。"), self.title, lower, upper)
        end
        if self == minimum then return validate_curve(self, value, "fan") end
        return tostring(n)
    end
end
-- CBI removes inactive dependency fields from the submitted form, but still
-- parses mandatory options on the server. Preserve the hidden manual setting.
function speed.parse(self, section, novld)
    local active_mode = mode:formvalue(section) or mode:cfgvalue(section) or mode.default
    if active_mode ~= "manual" then return end
    return Value.parse(self, section, novld)
end
local sw = m:section(NamedSection, "switch", "switch", translate("拨动开关"))
sw.addremove = false
sw.description = translate("网口默认作为 LAN，不再由实体开关切换。实体开关可控制 5G 模块电源或指示灯，其余选项由页面控制。关闭 5G 电源会中断蜂窝网络。")
local func = sw:option(ListValue, "func", translate("实体开关用途"))
func:value("0", translate("不接管 · 由页面设置"))
func:value("2", translate("5G 模块电源"))
func:value("3", translate("指示灯"))
func.rmempty = false
func.default = "0"
function func.cfgvalue(self, section)
    local value = ListValue.cfgvalue(self, section)
    return (value == "2" or value == "3") and value or "0"
end
local function software_option(name, title, owner, values)
    local o = sw:option(ListValue, name, translate(title))
    for _, v in ipairs(values) do o:value(v[1], translate(v[2])) end
    for _, v in ipairs({"0", "2", "3"}) do if v ~= owner then o:depends("func", v) end end
    o.rmempty = false
    function o.parse(self, section, novld)
        local active_func = func:formvalue(section) or func:cfgvalue(section) or func.default
        if active_func == owner then return end
        return ListValue.parse(self, section, novld)
    end
    return o
end
software_option("power", "5G 模块电源", "2", {{"0", "关闭"}, {"1", "开启"}})
software_option("led", "指示灯", "3", {{"0", "关闭"}, {"1", "开启"}})

local advanced = m:section(NamedSection, "reset", "modem", translate("高级硬件设置"))
advanced.addremove = false
advanced:tab("hardware", translate("模块复位"))
local reset = advanced:taboption("hardware", Flag, "state", translate("反转模块复位引脚"))
reset.rmempty = false
reset.description = translate("用于适配不同模块的复位电平，通常保持默认。")
return m
