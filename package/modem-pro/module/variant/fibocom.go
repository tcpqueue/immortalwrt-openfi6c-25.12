package variant

import (
	"modemd/iface"
)

func FibocomMTKLocker(iface.DeviceInfo) NetLocker { return &fibocomMTKCommand{} }
