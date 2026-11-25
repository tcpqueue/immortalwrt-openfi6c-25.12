package quectel

// ====================== 移远 QNW 系锁网应答解析 ======================
//
// 从 module/kernel/lock.go 搬过来：引擎侧（kernel）与方言侧（module/lock 的 qcfgband）
// 都要用它，但它只认移远的 `+QNWLOCK:` 格式 ⇒ 属于厂商知识，住在厂商包。
// kernel 保留搬家前的老名字（parseQNWLOCK / cmdQNWLockSave）作为别名。

import (
	"strings"

	"modemd/iface"
	"modemd/module/common"
)

// QNWLockSaveCmd 移远固件的小区锁定持久化指令（厂商没声明自己的写法时用它）
const QNWLockSaveCmd = `AT+QNWLOCK="save_ctrl",1,1`

// ParseQNWLOCK 解析小区锁定应答：
//
//	+QNWLOCK: "common/4g",1,2452,155   // 已锁定 EARFCN=2452 / PCI=155
//	+QNWLOCK: "common/5g",0           // 未锁定
func ParseQNWLOCK(resp []string, kind string) (iface.CellLock, bool) {
	const head = "+QNWLOCK:"
	for _, line := range resp {
		idx := strings.Index(strings.ToUpper(line), head)
		if idx < 0 {
			continue
		}
		f := common.SplitATFields(line[idx+len(head):])
		if len(f) < 2 || !strings.EqualFold(f[0], kind) {
			continue
		}
		cl := iface.CellLock{
			LockType: common.AtIntOr0(f, 1),
			RawValue: strings.Join(f, ","),
		}
		cl.Locked = cl.LockType != iface.CellLockNone
		if cl.Locked {
			cl.ARFCN = common.AtIntOr0(f, 2)
			cl.PCID = common.AtIntOr0(f, 3)
		}
		return cl, true
	}
	return iface.CellLock{}, false
}
