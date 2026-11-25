# simcom-cm（SIMCom 模组的主机侧拨号器）

这个目录是 SIMCom 主机侧拨号器的**源码**（`src/`，随包交叉编译成静态 musl），
由本包 OpenWrt 包打进 ipk（`make package/daocaoren/modem-pro/compile`），装到 **`/usr/lib/modemd/simcom-cm`**。
（以前这里还放过厂商的预编译二进制做兜底，源码进来之后删掉了 —— 打包脚本仍支持
`bins/<zig目标>/simcom-cm` 那条退回路径，默认没有文件。）

## 为什么要它

`modemd` 的 QMI 模式里，接口是 `proto=none`，真正的数据通道交给**厂商自己的**主机侧拨号器：

| 模组厂商 | 拨号器 | 谁提供 |
| --- | --- | --- |
| 移远 Quectel | `quectel-CM` | 本仓库自带（`third_party/quectel-cm`） |
| 芯讯通 SIMCom | **`simcom-cm`** | 本仓库自带（`third_party/simcom-cm/src`，随包编译） |
| 广和通 Fibocom | `fibocom-dial` | 本仓库自带（`third_party/fibocom-dial`） |
| 美格 MeiG | `meig-cm` | 本仓库自带（`third_party/meig-cm`） |

真机实测（2026-10-03，SIM8200EA-M2）：拿 `quectel-CM` 去拨 SIMCom 模组，控制口能打开、
`requestSetupDataCall` 也发出去了，但会报

```
QmiWwanGetClientID message timeout
QMIType 11 has no clientID            # WDA（高通/移远那套私有服务）SIMCom 固件不提供
requestSetupDataCall QMUXResult = 0x1, QMUXError = 0xe
call_end_reason is 1 / call_end_reason_verbose is 241
```

→ 拨不起来。所以 SIMCom 模组要用它自家的 `simcom-cm`。

## 源码在哪、怎么编

源码在 `src/`（从 SIMCom 官方驱动包里的 `simcom-cm（0812）.rar` 解出来的，来历与日期见
`SOURCE.md`），照常打包即可：

```powershell
（Windows 侧脚本已删除，改用 OpenWrt 构建：make package/daocaoren/<包名>/compile） ipk zig      # 第一次：下便携版 zig（约 76 MB，只下一次）
（Windows 侧脚本已删除，改用 OpenWrt 构建：make package/daocaoren/<包名>/compile） ipk all      # 六个平台各打一组，每个 ipk 里都会带 simcom-cm
```

本包 Makefile 里的 `$scmSources` 与 SIMCom 那份 `Makefile` 的 `release` 目标**一致**：

```
QmiWwanCM.c GobiNetCM.c main.c MPQMUX.c QMIThread.c util.c udhcpc.c   (-lpthread)
```

（`src/dhcpclient.c` 同目录但**不在**编译列表里 —— SIMCom 自己的 Makefile 也没编它。）

这份代码用 clang 18（zig 带的就是它）**直接编得过**，不需要广和通/美格那种
`-std=gnu89 -Wno-error=return-type`。产物是静态 musl，因此**不依赖设备 libc**：

| 目标 | 大小 |
| --- | --- |
| aarch64-linux-musl | 97 KB |
| arm-linux-musleabihf | 95 KB |
| mipsel-linux-musl | 136 KB |

想手工指定源文件：在本目录（或 `src/`）放一个 `sources.txt`（逐行文件名，`#` 注释），
脚本就以它为准。

参数：
- `-SkipSimcomCM` —— 原 Windows 打包脚本的开关（脚本已删除；OpenWrt 侧拨号器由本包 Makefile 统一编译）
- `-CMCC <你的交叉 gcc>` —— 不用 zig，用自己的交叉编译器（`-static` 由脚本带上）
- 编译缓存看的是源文件 mtime + 开关指纹：改过源码才会重编

## 运行时怎么被用起来

`modemd` 在模组是 SIMCom（`vendor=simcom`）时按这个顺序找：

```
/usr/lib/modemd/simcom-cm  ->  /usr/bin/simcom-cm  ->  /usr/sbin/simcom-cm
```

找到就拉起，它的输出一行行转发进 `modemd` 日志；也可以完全绕过自动查找：
`[net] host_dial_bin = "/路径/simcom-cm"`。

⚠️ **参数与 quectel-CM 不一样**（真机实测）：`simcom-cm` 只认

```
simcom-cm [-s [apn [user password auth]]] [-p pincode] [-f logfilename]
```

传 `-i` / `-4` / `-6` 它会**打印 usage 然后立刻退出**（日志里几秒刷一次 usage）。
所以 `module/kernel/hostdial.go` 的 `hostDialArgsFor()` 对 SIMCom 只传 `-s <APN>` ——
网卡与 IP 族它自己从 QMI 里认（日志 `Find qmichannel = /dev/cdc-wdm0` /
`Find usbnet_adapter = wwan0`）。代价：它不支持指定网卡，一张板子上多张 SIMCom 模组时只用第一张。

## 许可

SIMCom 的这份代码是**厂商自有**的（不属于本项目的开源部分）。
`.gitignore` 只忽略 `bins/`（万一又有人往那儿放预编译二进制）；`src/` 随仓库走 ——
如果要对外分发本仓库，请自行确认这层授权是否覆盖你的场景。
不想带它出包的话加 `-SkipSimcomCM`（SIMCom 模组会提示缺什么，不影响别的厂商）。
