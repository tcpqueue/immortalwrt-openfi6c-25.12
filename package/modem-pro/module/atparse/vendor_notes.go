package atparse

// ====================== 厂商补充 AT 的解析（按厂商分文件） ======================
//
// 目录地图（改哪家就只看哪家的文件，不用翻别人的）：
//
//      telit.go         AT#TEMPSENS / AT#CBC          ParseTelitTemp
//      thales.go        AT^TEMP                       ParseThalesTemp
//      meig.go          AT+TEMP / AT^DSAMBR           ParseMeigTemp / ParseMeigDSAMBR
//      sierra.go        AT!GSTATUS 温度 / 供电电压    ParseSierraPCTemp / ParsePCVolt
//      unisoc.go        AT+C5GQOSRDP                  ParseC5GQOSRDP(Context)
//      vendor_common.go 多家共用的「标签: 值」拆解    parseNamedTemps / prependUnique
//      temp.go          通用温度解析（QTEMP/CPMUTEMP/MTSM/GTLADC/GTSTemp/CHIPTEMP）
//      sim.go / cbc.go / ambr.go / digits.go         通用 SIM / 电压 / 签约速率 / 数字判断
//
// 为什么这些厂商**没有自己的 module/<vendor> 包**：它们在本项目里没有 Command 实现，
// 走的是「未识别厂商 + 自适应指令集 + 机型特例表」，指令由 kernel/atcmd.go 的 Quirk
// 指定，解析在本包。所以只能按**文件**分厂商，按**包**分不了。
//
// 这一层只做"把 QModem 各厂商脚本里**有取证**、而本项目原先缺的 AT 补上"，
// 指令的**归属**登记在 `atcmd.go` 的机型特例表（Quirk 的 TempCmd / VoltageCmd /
// ICCIDCmd / ServingCellCmd / NeighbourCellCmd），这里放对应的**解析**。
//
// 为什么要单独一层：这些厂商（telit / thales / gosuncn / meig / sierra / openluat）
// 在本项目里没有专门的 Command 实现，走的是「未识别厂商 + 自适应指令集 + 候选探测」；
// 用机型特例表 + 解析函数就能做到"只对这些型号生效"，别的型号一个字节都不变。
//
// 证据（全部是 QModem 原文，本地副本 `d:\Go\_pdfx\qmodem\`）：
//
//	cmds/telit.sh      cmd_cbc       → AT#CBC          （电压；不是 3GPP 的 AT+CBC）
//	cmds/telit.sh      cmd_tempsens  → AT#TEMPSENS=2   （温度）
//	vendor/telit.sh    → 从应答取 `#TEMPSENS: TSENS,<值>`
//	cmds/telit.sh      cmd_qss_query → AT#QSS?         （SIM 状态，本项目走 AT+CPIN?，未接）
//	cmds/thales.sh     cmd_temp_query → AT^TEMP        （温度，手册 §15.13）
//	vendor/thales.sh   → sed -n 's/.*TSENS: \([0-9]*\)C.*/\1/p'（应答里带 `TSENS: 33C`）
//	cmds/meig.sh       cmd_temp      → AT+TEMP         （温度）
//	vendor/meig.sh     → grep 'TEMP: "soc-thmzone"' / 'TEMP: "cpu0-0-usr"'，值在第 4 个引号字段
//	cmds/gosuncn.sh    cmd_mtsm      → AT+MTSM=1       （温度；解析复用 ParseMTSM，见 sim.go）
//	cmds/openluat.sh   AT+CCED=0,1 / AT+CCED=0,2 / AT+ICCID / AT+CBC
//	vendor/sierra.sh   解锁高级指令：`AT!ENTERCND="A710"`（本项目接在 USB 模式方言的 pre 里）

// ====================== openluat Air724UG 的 `+CCED:` ======================
//
// ⚠️ 这里**故意不再放一份"识形"表**：`+CCED` 四类行（LTE/GSM × 服务小区/邻区）
// 的标签与字段数只存在一份 —— `vendor_cell_extra.go` 的 `openluatCCEDKinds` +
// `openluatCCEDLine()`，由 `ParseOpenluatCCED` / `ParseOpenluatCCEDNeighbours` 直接用。
// （以前 vendor_at_extra.go 里另有一份 `ccedFieldCount` + `CCEDRowKind`，
//  但因为没人调用而成了死代码，且**两张表要手动保持同步** —— 已删。）
