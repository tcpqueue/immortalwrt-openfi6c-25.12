package common

import "modemd/iface"

// UnknownCommand 未识别型号的兜底实现。
//
// 以前未知厂商直接按移远指令集走，现在换成自适应指令集（AdaptiveCommands）：
// 首选指令与移远完全一致，额外多了候选回退与宽容解析，
// 所以未被识别的型号不会再"一旦不是移远系就什么都查不出来"。
type UnknownCommand struct{ AdaptiveCommands }

var _ iface.ModemCommand = (*UnknownCommand)(nil)

func (u *UnknownCommand) DetectVendor(resp []string) iface.ModemVendor { return iface.VendorUnknown }

// CommandCAInfo 未识别型号沿用小系常用的 AT+QCAINFO，不支持时只记日志。
func (u *UnknownCommand) CommandCAInfo() string { return "AT+QCAINFO" }
