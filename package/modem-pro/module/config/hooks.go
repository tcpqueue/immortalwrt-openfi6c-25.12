package config

// 本文件是 config 这个**叶子包**与引擎之间的注入点。
//
// config 的定位是「纯配置」：只依赖标准库 + TOML + zap，可以被子系统随意 import；
// 但它有几处「配置一改、引擎要跟着做事」的地方（如拨号总开关关掉时要立刻收掉
// 所有主机侧拨号器）。那种动作属于引擎，放进来会让 config 反向依赖引擎 ⇒ 用 hook。

// OnDialDisabled 拨号总开关被关掉时调用（why 是给用户看的原因文案）。
// 默认空实现：没接引擎时什么都不做（纯配置解析、单元测试都安全）。
var OnDialDisabled = func(why string) {}
