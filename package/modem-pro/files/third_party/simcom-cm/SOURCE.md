# simcom-cm 的来源与真机验证记录

## 为什么需要它

`modemd` 的 QMI 模式（接口 `proto=none`）把数据通道交给**模组厂商自己的**主机侧拨号器。
移远用 `quectel-CM`，**SIMCom 必须用 `simcom-cm`** —— 实测（2026-10-03，SIM8200EA-M2，
固件 `SIM8200M44A-M2_V1.20` / 基带 `MPSS.HI.2.0.c3-00246-SDX55_CPEALL_PACK-1`）：

| 拨号器 | 结果 |
| --- | --- |
| `quectel-CM` | 能开控制口，但 `Get clientWDA` **拿不到**（`QMIType 11 has no clientID` → 那是高通/移远的 WDA 私有服务，SIMCom 固件不给），随后 `requestSetupDataCall QMUXResult=0x1, QMUXError=0xe` → **拨不起来** |
| `simcom-cm` | WDS/DMS/NAS/UIM/WDA **五个 client 全拿到**，读到 `SIM_READY`，注册后 `udhcpc` 拿到 `10.51.59.230/30` → **通** |

## 参数（这一版实测出来的，别照抄 quectel-CM）

```
simcom-cm [-s [apn [user password auth]]] [-p pincode] [-f logfilename]
```

- **没有 `-i`、没有 `-4`/`-6`**：传了就直接打印 usage 然后退出
  （`simcom-cm -s 3GNET -4 -6` → `Usage: …`）。网卡/IP 族它自己从 QMI 里认
  （日志：`Find qmichannel = /dev/cdc-wdm0` / `Find usbnet_adapter = wwan0`）。
- 所以 `module/kernel/hostdial.go` 的 `hostDialArgsFor()` 对 SIMCom 只传 `-s <APN>`。
- 副作用：它不支持指定网卡，**一张板子上多张 SIMCom 模组时只会用第一张**。

## 数据网卡要先切 raw IP

SIMCom 固件里的做法（他们的热插拔脚本）：

```sh
insmod qmi_wwan_simcom.ko
echo Y > /sys/class/net/wwan0/qmi/raw_ip
echo 80 > /sys/class/net/wwan0/qmi/add_mux
```

实测这台机器上跑的是**通用 `qmi_wwan`**（不是 SIMCom 那个 kmod），但 sysfs 也带
`raw_ip`：`raw_ip=Y` 时拨号成功；`raw_ip=N`（802.3 以太网模式）拨不上。
`modemd` 现在会在给 SIMCom 模组拉起拨号器之前自动把它切成 `Y`（幂等，见
`module/kernel/hostdial_posix.go` 的 `prepareSimcomRawIP`）。

⚠️ 两点真机踩过的细节：

1. `raw_ip` **只能在网卡 down 的时候改**，而**模组刚重新枚举出来的头几秒写它必回 `EBUSY`**
   （拔插、`AT+CFUN=1,1` 都会重新枚举，顺便把 `raw_ip` 复位成 `N`）。日志长这样：

   ```
   WARN host dial: set raw_ip failed, will retry attempt=1 of 5 ... device or resource busy
   WARN host dial: set raw_ip failed, will retry attempt=2 of 5 ...
   WARN host dial: set raw_ip failed, will retry attempt=3 of 5 ...
   INFO host dial: raw_ip switched to Y (SIMCom data bearer is raw IP)  attempt=4
   INFO host dial: started  bin=/usr/lib/modemd/simcom-cm  args=["-s","3GNET"]
   ```

   所以现在是 `down → 写 → up` 重试 5 次（间隔 800 ms）。**重试全失败就不拉起拨号器**
   —— 否则网卡停在 `N`，拨号器只会一直刷 `PS: Detached` 空转，页面显示"正在跑"却永远没地址
   （2026-10-01 实测空转了 14 分钟）。
2. `simcom-cm` 起来后前几秒会报 `MCC: 0, MNC: 0, PS: Detached` —— 它在**轮询**注册状态。
   注意：这个"开头几秒"和下面那条"纯 SA 永远 PS: Detached"要区分开。

## ⚠️ 真正的门坎：`simcom-cm` 看的是 **EPS 注册**（纯 5G SA 拨不上）

`simcom-cm` 判断"能不能拨"读的是 **QMI NAS 的 EPS 注册状态**（日志里 `requestRegistrationState2`）。
2026-10-01 在这台 SIM8200EA-M2 上抓到的**纯 NR5G-SA**状态（AT 侧一切正常，它却一直不拨）：

| 查询 | 结果 |
| --- | --- |
| `AT+CPIN?` | `+CPIN: READY` |
| `AT+C5GREG?` | `+C5GREG: 0,1`（5G 已注册） |
| `AT+CGATT?` | `+CGATT: 1`（已附着） |
| `AT+CPSI?` | `+CPSI: NR5G_SA,Online,460-11,…,NR5G_BAND78,…` |
| `AT+CEREG?` | **`+CEREG: 0,0`（没有 EPS 注册）** ← simcom-cm 只看这个 |
| `AT+CGPADDR` | cid 1 → `0.0.0.0`（数据 PDP 起不来） |
| simcom-cm 日志 | 一直 `requestRegistrationState2 MCC: 0, MNC: 0, PS: Detached, DataCap: UNKNOW` |

**它既不拨也不报错**，就这么无限空转。试过、都**没用**的：`AT+CGACT=1,1` 手动激活数据 PDP、
`AT+CFUN=0` + `AT+CFUN=1` 软射频循环、`AT+CFUN=1,1` 整机复位。
**唯一有效的**是把网络模式锁到 LTE / LTE+NR：

```sh
AT+CNMP=38   # LTE only
AT+CNMP=109  # LTE + NR（NSA）
```

锁完 `+CEREG` 立刻变 `0,1`，**同一张卡、同一份 `simcom-cm` 马上拨上**：
`wwan0=10.53.199.114/30`、默认路由 `via 10.53.199.113`（我们标 metric 20）、
`ping -I wwan0 223.5.5.5` 通。

⇒ 结论：**SIMCom 模组要跑 5G 请用 NSA（LTE 打底）**；纯 SA 这条路 `simcom-cm` 走不通。
modemd 不替用户锁网，但 SIMCom 拨号器空转 >90 秒仍没地址会在页面那行**补一句提示**
（`hostdial.go` 的 `hostDialStuckHint`）。

## 关于"模组侧数据 PDP 要先激活"（旧结论，已被上面那条取代）

早期实测确实是"数据 PDP（cid 1）激活 → 拨上；没激活 → 一直 `PS: Detached`"，
但现在看这是**相关性**而不是因果：真正决定成败的是 EPS 注册（`+CEREG`）。
留 `auto_dial` 开着没坏处（模组那端就绪，`AT+CGCONTRDP`/`AT+CGPADDR` 里能看到地址），
但**别指望它能救纯 SA**。

## 端到端验证记录（2026-10-03，AXT1800 / QWRT 6.1.57 aarch64_generic）

```sh
# 接口（modemd 自动建的）：不配置协议 + 绑网卡
network.modem0.proto='none'
network.modem0.device='wwan0'
network.modem0.ifname='wwan0'

# 拨号器（modemd 自己拉起，参数只有 -s）
18867 root  /usr/lib/modemd/simcom-cm -s 3GNET

# 结果
inet 10.64.46.216/28 scope global wwan0
default via 10.64.46.217 dev wwan0 metric 20      # metric 由 modemd 对齐（udhcpc 默认是 0）
ping -I wwan0 223.5.5.5 → 3 packets, 0% loss, avg 32 ms

# modemd 日志
host dial: started   bin=/usr/lib/modemd/simcom-cm args=["-s","3GNET"]
host dial: simcom-cm out=udhcpc: started, v1.35.0
host dial: default route metric aligned  dev=wwan0 gw=10.64.46.217 metric=20
```

网卡模式是 **NR5G_SA**（`AT+CPSI: NR5G_SA,Online,460-11,…`，`AT+C5GREG: 0,1`），也就是说
SA 下也能拨 —— 关键是模组侧那条数据 PDP 得是激活的（见上一节）。

## ⚠️ 另一个坑：`/dev/cdc-wdm0` 的 QMI 通道会被"硬杀"弄僵

观察到的现象：**手动 `kill -9` 拨号器（或连着好几次在拨号中途重启）之后**，
`simcom-cm` 会一直报 `MCC: 0, MNC: 0, PS: Detached` 不再拨号，此时连
`uqmi -d /dev/cdc-wdm0 --get-serving-system` 都**不返回**（通道被占/客户端表泄漏），
但 AT 侧一切正常（`+CPSI: NR5G_SA,Online,460-11`、`+CGATT: 1`、`+C5GREG: 0,1`）。
一次 `AT+CFUN=1,1` 复位模组后恢复正常。

这解释了为什么 `modemd` 停拨号器时要"先礼后兵"：**SIGTERM 给整个进程组 → 等 3 秒 →
SIGKILL**（见 `module/kernel/hostdial.go` 的 `stopHostDial`），让拨号器有机会走自己的
QMI 释放流程；服务重启（opkg 升级 / `init.d stop`）也会先走这条路
（实测升级后新起的实例照样能拨起来）。


## 源码（已 vendored，优先于预编译二进制）

源码在 **`src/`**，来自 SIMCom 官方驱动包
`SIM8200_OS_Driver/Linux/linux_doc/linux_doc/simcom-cm（0812）.rar`
（目录名里的 `0812` 就是厂商标的 2019-08-12；15 个文件，含 `Makefile` / `default.script`）。
`原 Windows 打包脚本（已删）` 里的 清单 与那份 `Makefile` 的 `release` 目标一致：

```
QmiWwanCM.c GobiNetCM.c main.c MPQMUX.c QMIThread.c util.c udhcpc.c   (-lpthread)
```

（`src/dhcpclient.c` 解出来在，但**不在**编译列表 —— SIMCom 自己的 Makefile 也没编它。）

编译开关：**不用加任何东西**。这份 2019 年的代码用 clang 18（zig 带的就是它）直接编得过，
不像广和通/美格那份要 `-std=gnu89 -Wno-error=return-type`。用 zig 编出来是**静态 musl**：

| 目标 | 大小（已 strip） | 备注 |
| --- | --- | --- |
| `aarch64-linux-musl` | 97 KB | 2026-10-03 真机验证过能拨（见下） |
| `arm-linux-musleabihf` | 95 KB | 之前**没有** armv7 二进制，现在有了 |
| `mipsel-linux-musl` | 136 KB | — |

因为源码静态链接，ipk 里**不会**再因为 `simcom-cm` 而声明 `Depends: libgcc1`
（那是下面那些预编译二进制才需要的）。

### 附带的 SIMCom 内核驱动

同一个驱动包里还有 `qmi_wwan_simcom.rar` → 已解到
**`third_party/kmods/qmi_wwan_simcom/qmi_wwan_simcom.c`**（SIMCom 的 `qmi_wwan` 分支）。
它就是提供 `.../qmi/raw_ip` 与 `.../qmi/add_mux` 那个驱动 —— modemd 会替拨号器把主线
`qmi_wwan` 的 `raw_ip` 切成 `Y`，原因见 README「QMI 模式」第 2 条。
内核模块要对着目标内核编，用法见 `third_party/kmods/README.md`。

## 预编译二进制（`bins/`，已不再保存 —— 这里只是历史记录）

⚠️ **这两个预编译二进制已经不在了**（源码 vendored 之后就没用了，已从仓库删掉，
`third_party/simcom-cm/bins/` 目录本身也没了）。
打包脚本仍然支持"源码编不出来时退回 `bins/<zig目标>/simcom-cm`"这条路，
想用它就往那个路径丢一份即可；默认没有文件，所以**不会**触发任何兜底。
下面的记录保留下来，是因为它们帮我们确认过 ABI 与真机行为（真机验证的地址、行为都出自这两个）。

## 这些二进制是哪来的（历史）

| 目标 | 文件 | sha256 | 来源 |
| --- | --- | --- | --- |
| `aarch64-linux-musl` | `bins/aarch64-linux-musl/simcom-cm`（已删） | `f486421486e721ed07ee42df325ac83d18df39c106843a19188ba1f31c2c6439` | 本机 `D:\历史\杂项\openwrt Documents\安装包\simcom-cm_1-1_aarch64_generic.ipk`（厂商打的包，`SourceDateEpoch 1688193693`）里解出来的 `/bin/simcom-cm`（49,343 B，ELF aarch64，动态链接 libc；在 AXT1800 / QWRT 6.1.57 aarch64 上跑通过拨号） |
| `mipsel-linux-musl` | `bins/mipsel-linux-musl/simcom-cm`（已删） | `9b15bea59af1b12aa876b07d3b3a9aed364ecbf48463b5f7c5fa2ac1a9f71cd2` | 本机 `D:\历史\杂项\openwrt Documents\SLK-R620-v2\usr\sbin\simcom-cm`（65,768 B，ELF mips-LE；**没在真机上验证过**） |
| `arm-linux-musleabihf` | （厂商没给） | | **现在由源码编出来**（`src/`，95 KB 静态），这个缺口已经补上 |

### 这两个二进制的 ABI 校验（离线看的 ELF 头 + `PT_INTERP`）

| 文件 | 类型 | `PT_INTERP` | 依赖 |
| --- | --- | --- | --- |
| aarch64 | `ELF64 LE`, machine=`0xb7` (AArch64) | `/lib/ld-musl-aarch64.so.1` | `libc.so`, `libgcc_s.so.1` |
| mipsel | `ELF32 LE`, machine=`0x08` (MIPS) | `/lib/ld-musl-mipsel-sf.so.1` | `libc.so`, `libgcc_s.so.1` |

结论：

- 两个都是 **musl 动态链接**，不是 uClibc —— 正好对上 OpenWrt（musl）的目标包。
- mipsel 那个的解释器带 **`-sf`（soft-float）**，对上的正是 OpenWrt 的 `mipsel_24kc`
  （`mipsel_24kc` 就是软浮点；硬浮点的 `mipsel_24kc_24kf` 才对不上）。所以这个"没上真机"的
  二进制在 **`mipsel_24kc` 上是能加载的**；真正没验证的只是"能不能拨通"，不是"能不能跑"。
- `ld-musl-*.so.1` → `libc.so` 是 OpenWrt 基础包自带；但 **`libgcc_s.so.1` 来自 `libgcc1`**，
  不是所有精简固件都装了。所以 `原 Windows 打包脚本（已删）` 在**打进动态链接的拨号器时**会扫一遍该二进制，
  命中 `libgcc_s.so` 就给 ipk 加 `Depends: libgcc1`（自己用 zig 编的 `quectel-CM` 是**全静态**的，
  不会触发；实测三个平台都是 `interp=(static)`）。

⚠️ 历史记录：这些是 **SIMCom 的闭源二进制**（不属于本项目的开源部分），已经删掉了。
`.gitignore` 里留着 `third_party/simcom-cm/bins/` 这条忽略规则 —— 万一以后又有人往那儿放二进制，
不会被误提交。源码 `src/` 随仓库走。版本号不详（厂商 ipk 里写的是 `1-1`）。

## 用源码编（推荐，也是现在的默认路径）

源码已经在 `src/` 里，`原 Windows 打包脚本（已删）` 会**优先用源码**交叉编译（静态 musl，六个平台都能出），
只有源码编不出来时才退回上面那些预先编译好的二进制。见本文件上面「源码（已 vendored）」
一节，以及 `README.md`。
