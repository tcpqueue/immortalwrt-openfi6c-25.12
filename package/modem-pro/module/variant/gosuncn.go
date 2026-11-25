package variant

import ()

func GosuncnModeQualcomm() *modeDialect { return gosuncnMode(true) }

func GosuncnModeOther() *modeDialect { return gosuncnMode(false) }
