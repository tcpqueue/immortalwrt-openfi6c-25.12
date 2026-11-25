package huawei

import (
	"modemd/module/imeidialect"
)

// IMEIPhynum 华为/鼎桥（巴龙）档
func IMEIPhynum() *imeidialect.ImeiDialect {
	return &imeidialect.ImeiDialect{
		// QModem cmds/huawei.sh：at^phynum=IMEI,<imei>（**小写 at、不带引号**，按原文照发）
		Set: func(imei string) string { return "at^phynum=IMEI," + imei },
		Note: "华为/鼎桥（巴龙）用 at^phynum=IMEI,<IMEI>（QModem cmds/huawei.sh 原文，小写且不带引号）；" +
			"写完回读 AT+CGSN 确认 —— 若回 ERROR，说明该固件不允许改。",
	}
}
