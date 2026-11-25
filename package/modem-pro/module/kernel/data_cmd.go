package kernel

import (
	"fmt"
	"strings"
)

// ====================== 指令拼装 ======================

// CmdCGDCONT 写一个 PDP 上下文的 APN（3GPP TS 27.007 标准指令）
func CmdCGDCONT(cid int, pdpType, apnName string) string {
	return fmt.Sprintf(`AT+CGDCONT=%d,"%s","%s"`, cid, strings.ToUpper(pdpType), apnName)
}

// CmdCGDCONTDelete 删除一个 PDP 上下文的整条配置。
// 3GPP TS 27.007：只给 <cid>（不带 <PDP_type> / <APN>）就是把该 cid 从上下文列表里删掉，
// 移远 / 广和通等厂商手册的写法一致。
func CmdCGDCONTDelete(cid int) string {
	return fmt.Sprintf("AT+CGDCONT=%d", cid)
}

// CmdCGACT 激活 / 停用一个 PDP 上下文（3GPP TS 27.007 标准指令）。
// <state>：1 = 激活（建立数据链路），0 = 停用；一次只动一个 cid，避免误伤别的 APN。
func CmdCGACT(cid int, active bool) string {
	if active {
		return fmt.Sprintf("AT+CGACT=1,%d", cid)
	}
	return fmt.Sprintf("AT+CGACT=0,%d", cid)
}

// Steps 一次请求要按顺序发的动作（true = 激活）。
//
// 「重新激活」(reconnect) 时把已激活的先断开：AT+CGDCONT 改完只改配置，**已激活的
// PDP 仍跑旧 APN**，只有重新协商才会用上新配置。本来就没激活的直接激活即可。
func Steps(active, wasActive, reconnect bool) []bool {
	if !reconnect {
		return []bool{active}
	}
	if wasActive {
		return []bool{false, true}
	}
	return []bool{true}
}

// ByCID 在上下文列表里找某个 cid（找不到返回 nil）。
// 注意取的是切片元素的地址而不是循环变量，否则返回的指针会指向副本。
func ByCID(list []Context, cid int) *Context {
	for i := range list {
		if list[i].CID == cid {
			return &list[i]
		}
	}
	return nil
}

// DeactivateBestEffort 停用被固件拒绝时是否忽略并继续。
//
// 内部上下文（IMS / sos）为 true：实测有的固件直接拒绝手动停用内部上下文
// （RM502Q-GL 的 `AT+CGACT=0,2` 回 ERROR），而"重新激活"的需要目的就是把它启动起来，
// 所以断开那一步失败不应当拦住后面的激活。普通上网上下文仍为 false（失败就是失败）。
func DeactivateBestEffort(internal bool) bool { return internal }

// ActiveWord 激活状态的中文说法（拼错误信息用）
func ActiveWord(on bool) string {
	if on {
		return "激活"
	}
	return "停用"
}
