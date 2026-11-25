# meig-cm 的来源、编译参数与用法

## 这是哪来的

用户提供的 `5G-Modem-Support` 源码集里的 `meig-cm/`（美格智能官方的 Linux 主机侧拨号器，
以 OpenWrt 包的形式分发）。保留了上游 `Makefile`（包 Makefile）与 `src/Makefile`（源文件清单）。

## 编的是哪一套：`release`

`src/Makefile` 的 `release` 目标，源文件清单（本包 Makefile 的 清单 与它一致）：

```
QmiWwanCM.c GobiNetCM.c main.c MPQMUX.c QMIThread.c util.c
udhcpc.c qmap_bridge_mode.c mbim-cm.c device.c
```

`mbim-cm.c` / `device.c` 是这套的特色（它同时支持 QMI 与 MBIM 两种模组），必须带上。

## 交叉编译要加的开关

| 开关 | 为什么 |
| --- | --- |
| `-std=gnu89` | 与广和通那份同源，同样是"先调用后定义"的 gcc 时代写法，clang 18 会当错误 |
| `-Wno-error=return-type` | 同上（非 void 函数里裸 `return ;`） |
| `-static` | 不依赖设备 libc（zig 带 musl） |

产物（剥离符号后，静态 musl）：**aarch64 226 KB / armv7 262 KB / mipsel 312 KB**。

## 上游自带的两个可执行文件已剔除

源码目录里本来躺着 `src/meig-cm`（146 KB）与 `src/meig-qmi-proxy`（22 KB）两个**预编译二进制**，
实测是 **x86-64**（`ELF64 LE machine=0x3e`，解释器 `/lib64/ld-linux-x86-64.so.2`）—— 是
厂内开发机上的产物，对 OpenWrt 毫无用处，所以**没有**拷进本仓库。
`meig-qmi-proxy.c` 是配套的 QMI 代理（需要多进程访问 QMI 时用），modemd 不用，只编 `meig-cm`。

## modemd 怎么调它

参数与 `quectel-CM` 一致（见 `src/main.c` 的 `usage()`）：
`-s [apn [user password auth]]`、`-p pincode`、`-f logfile`、`-i interface`、`-4`、`-6`、`-m/-n`（多 PDN）。
所以走 `module/kernel/hostdial.go` 的 `hostDialArgs()`。

## 美格模组的 USB 身份（取自美格自家的 qmi_wwan 驱动）

`third_party/kmods/meig_QMI_WWAN/src/qmi_wwan_m.c` 的 `products[]` 里：

| VID:PID | 型号 |
| --- | --- |
| `2dee:4d22` | SRM815（美格自有 VID `0x2dee`） |
| `05c6:f601` | SLM750 / SLM730 / SLM750VR2.0（高通通用 VID） |

这两条已经抄进 `module/kernel/usbhotplug.go` 的 `modemUSBFamilies`（新厂商接入清单见 README「新增厂商」）。

## ⚠️ 验证状态

**没有真机验证过**（手上没有美格模组）：编译通过、参数与 USB 身份是从源码核对的。
