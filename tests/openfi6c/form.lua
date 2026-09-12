-- Exercise the repository's real CBI parser with an in-memory UCI/form boundary.
local function read(path)
    local f = assert(io.open(path)); local s = f:read('*a'); f:close(); return s
end
local cbi = read('feeds/luci/modules/luci-compat/luasrc/cbi.lua')
local cfg, form, options, errors
AbstractValue = {}
Value = setmetatable({}, {__index=AbstractValue})
ListValue = setmetatable({}, {__index=AbstractValue})
Flag = setmetatable({}, {__index=AbstractValue})
DummyValue = setmetatable({}, {__index=AbstractValue})
NamedSection = {}; SimpleSection = {}
function translate(s) return s end
function AbstractValue.cfgvalue(self, section) return cfg[section..'.'..self.option] end
function AbstractValue.formvalue(self, section) return form[section..'.'..self.option] end
function AbstractValue.value() end
function AbstractValue.depends() end
function AbstractValue.transform(self, value) return value end
function AbstractValue.write(self, section, value) cfg[section..'.'..self.option]=value; return true end
function AbstractValue.remove() end
function AbstractValue.add_error(self, section, kind, message) errors[self.option]=message or kind end
assert(loadstring(assert(cbi:match('(function AbstractValue.parse%(.-\nend)'))))()
assert(loadstring(assert(cbi:match('(function Value.parse%(.-\nend)'))))()
function Map()
    local m = {uci={get=function(_, _, section, option) return cfg[section..'.'..option] end}}
    function m:section(_, name)
        local section = {}
        function section:tab() end
        function section:option(class, option, title)
            local o=setmetatable({option=option, title=title, map=m, section=section}, {__index=class})
            options[option]=o
            return o
        end
        function section:taboption(_, ...) return self:option(...) end
        return section
    end
    return m
end
local function setup(path)
    cfg={['fan.mode']='auto', ['fan.manual_speed']='55', ['fan.min_speed']='6', ['fan.period']='5',
         ['fan.emergency_temp']='85', ['switch.func']='0', ['switch.power']='1', ['switch.led']='1'}
    form={['fan.mode']='auto', ['fan.min_speed']='6', ['fan.period']='5', ['fan.emergency_temp']='85'}
    for i,t in ipairs({55,58,61,65}) do
        cfg['fan.temp'..i]=tostring(t); form['fan.temp'..i]=tostring(t)
        cfg['fan.speed'..i]=tostring(({55,70,85,100})[i]); form['fan.speed'..i]=cfg['fan.speed'..i]
    end
    options={}; errors={}
    assert(loadfile(path))()
end
setup('package/mtk/applications/luci-app-openfi/luasrc/model/cbi/openfi/settings.lua')
options.manual_speed:parse('fan')
assert(not errors.manual_speed and cfg['fan.manual_speed']=='55', 'Automatic mode must preserve hidden manual value')
options.min_speed:parse('fan')
assert(not errors.min_speed and cfg['fan.min_speed']=='6', 'Visible integer 6 must be valid')
form['fan.mode']='manual'; form['fan.manual_speed']='6.5'
options.manual_speed:parse('fan')
assert(errors.manual_speed:find('手动输出',1,true), 'Invalid manual value needs field-specific error')
errors={}; form['fan.manual_speed']='6'; options.manual_speed:parse('fan')
assert(not errors.manual_speed and cfg['fan.manual_speed']=='6')
form['switch.func']='2'; options.power:parse('switch')
assert(not errors.power and cfg['switch.power']=='1', 'Physical modem control must preserve hidden software setting')
form['switch.func']='3'; options.led:parse('switch')
assert(not errors.led and cfg['switch.led']=='1', 'Physical LED control must preserve hidden software setting')
print('25.12 CBI checks passed: automatic curve save, integer validation, hidden values and physical-switch dependencies.')
