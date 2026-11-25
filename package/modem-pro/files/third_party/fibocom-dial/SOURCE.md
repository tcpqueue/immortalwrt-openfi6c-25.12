# fibocom-dial 的来源、编译参数与用法

## 这是哪来的

用户提供的 `5G-Modem-Support` 源码集里的 `fibocom-dial/`（广和通官方的 Linux 主机侧拨号器，
以 OpenWrt 包的形式分发）。拷贝时保留了两份上游 Makefile：

- `Makefile` —— OpenWrt **包** Makefile（`Build/Prepare` + `Package/fibocom-dial/install`）；它会把
  `src/*` 拷进 build dir，再走 `src/Makefile` 编。它安装的其实有**三个**二进制。
- `src/Makefile` —— 真正的源文件清单与三套目标（`release` / `dhcp` / `ndhcp`）。

## 我们编的是哪一套：`ndhcp`

`ndhcp` = `fibocom-dial` + **自带**的 netlink DHCP 客户端 + **自带**的 libmnl
→ **不依赖设备上的 `busybox udhcpc`**，比 `release`（fork 一个 `udhcpc`）更适合随固件发。
`原 Windows 打包脚本（已删）` 里的 清单 与它一致：

```
QmiWwanCM.c GobiNetCM.c main.c MPQMUX.c QMIThread.c util.c
qmap_bridge_mode.c query_pcie_mode.c udhcpc_netlink.c
libmnl/{ifutils,attr,callback,nlmsg,socket}.c
```

（`src/libmnl/dhcp/*.c` 三个目标都不编，是上游留的冗余源码；保留它是为了与上游目录一致。）

## 交叉编译要加的开关（原样 gcc 编不过 clang 18）

| 开关 | 为什么 |
| --- | --- |
| `-DGHT_FEATURE_PCIE_AUTO` | `src/Makefile` 里的必需宏（CFLAGS += …） |
| `-std=gnu89` | `QMIThread.c:2344 get_private_gateway`、`main.c:1447 requestGetSIMCardNumber`、`1449/1450 requestSimBindSubscription_*` 都是**先调用后定义**（无函数声明）。gcc 只当警告，clang 18 直接 `error: call to undeclared function`。gnu89 允许隐式声明 |
| `-Wno-error=return-type` | `main.c:927` 的 `thread_socket_server` 里有裸 `return ;`（非 void 函数） |
| `-static` | 不依赖设备上的 libc 版本（zig 带 musl），换固件也能跑 |

产物（剥离符号后，静态 musl）：**aarch64 132 KB / armv7 131 KB / mipsel 180 KB**。

## modemd 怎么调它

`fibocom-dial` 的参数与 `quectel-CM` **完全一致**（见 `src/main.c` 的 `usage()`）：
`-s [apn [user password auth]]`、`-p pincode`、`-f logfile`、`-i interface`、`-4`、`-6`，
另外还有多 PDN 用的 `-m/-n/-N` 与 `-k`（kill）。

所以 `module/kernel/hostdial.go` 里广和通走 `hostDialArgs()`（`-i <wwan0> -4 [-6] -s <apn> [-n <pdn>]`），
不需要像 `simcom-cm` 那样单列。`-n <pdn>` 在数据网卡是 QMAP mux（`wwan0_<n>`）时才加
（`hostDialPDN` 从 mux 网卡名反推）。`/sys/class/net/<if>/qmi/raw_ip` 由 modemd 统一在拨号前切好
（见 README「QMI 模式」第 2 条）。

## 不进包的东西

上游包里还装 `multi-pdn-manager` 与 `fibo_qmimsg_server`（多 PDN 管理器、QMI 消息代理）。
modemd 一张模组一条默认链路，用不上，所以只编 `fibocom-dial`；源码都还在，要的话往
本包 Makefile 的 `$dialers` 加一行即可（它们的源文件清单见 `src/Makefile`）。

## ⚠️ 验证状态

**没有真机验证过**（手上没有广和通模组）：编译通过、参数与 QMI 行为是从源码核对的。
首次上机请先看 modemd 日志里拨号器那几行，以及 `ubus call network.interface.modem0 status`。
