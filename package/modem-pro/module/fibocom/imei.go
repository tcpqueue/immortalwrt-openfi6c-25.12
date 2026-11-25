package fibocom

import (
	"modemd/module/imeidialect"
)
import ()

// IMEIGTSN 高通/默认档
func IMEIGTSN() *imeidialect.ImeiDialect {
	return &imeidialect.ImeiDialect{
		Set:  func(imei string) string { return `AT+GTSN=1,7,"` + imei + `"` },
		Note: "广和通（高通/默认）用 AT+GTSN=1,7,\"<IMEI>\"；写完回读 AT+CGSN 确认。",
	}
}

// IMEIEGMREXT 联发科平台档
func IMEIEGMREXT() *imeidialect.ImeiDialect {
	return &imeidialect.ImeiDialect{
		Set:  func(imei string) string { return `AT+EGMREXT=1,7,"` + imei + `"` },
		Note: "广和通（联发科平台）用 AT+EGMREXT=1,7,\"<IMEI>\"；写完回读 AT+CGSN 确认。",
	}
}

// IMEILCTSN lte 款（NL668/NL678）档
func IMEILCTSN() *imeidialect.ImeiDialect {
	return &imeidialect.ImeiDialect{
		Set:  func(imei string) string { return `AT+LCTSN=1,7,"` + imei + `"` },
		Note: "广和通（旧款 4G / lte 平台 NL668/NL678）用 AT+LCTSN=1,7,\"<IMEI>\"；写完回读 AT+CGSN 确认。",
	}
}
