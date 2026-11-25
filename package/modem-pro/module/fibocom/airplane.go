package fibocom

import "modemd/iface"

// 广和通的"飞行模式禁开"事实（AT+CFUN=4 之后本模组 AT 口不再响应）。
//
// 现象：`AT+CFUN=4` 之后这台的 AT 口**不再响应** —— 串口读心跳停止 ⇒ isRunning 掉成 false
// ⇒ 自动组网循环每轮 `!isRunningNow()` 提前 return ⇒ 主机侧拨号器**永远拉不起来**。
// 用户看到的就是「IPv6 掉了、整机没网，必须重启 modemd」。
//
// 文案（为什么必须挡住、要怎么断网）留在 kernel 的界面层，这里只回答"这家禁不禁"。
func AirplaneBlocked(vendor iface.ModemVendor) bool { return vendor == iface.VendorFibocom }
