package kernel

// config 是叶子包，不能反过来调引擎；所以「拨号开关关掉要收掉拨号器」这件事
// 由内核在这里反向注入（见 module/config/hooks.go）。
import "modemd/module/config"

func init() { config.OnDialDisabled = StopHostDialsFor }
