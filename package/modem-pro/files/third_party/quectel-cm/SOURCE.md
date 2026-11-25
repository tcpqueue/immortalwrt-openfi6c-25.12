# quectel-CM（QMI 主机侧拨号器）

本目录是 **Quectel QConnectManager（quectel-CM）1.6.8** 的源码拷贝，用于 modemd 的
QMI 模式（USB 类型 0）：模组不做 AT 拨号，改由主机侧的 quectel-CM 通过 QMI 拉起数据链路。

## 来源与校验

| 项 | 值 |
|---|---|
| 版本 | 1.6.8（`QConnectManager_Linux_V1.6.8`） |
| 下载地址 | <https://sources.immortalwrt.org/quectel-cm-1.6.8.tar.gz> |
| SHA-256 | `30de214fe17c7930774dbb48113a26ffa3a68fd2918badc69b71e12dbd729032` |
| 同源包 | OpenWrt / immortalwrt 的 `net/quectel-cm`（`PKG_VERSION:=1.6.8`，`PKG_HASH` 与上表一致） |
| 下载日期 | 2026-10-03 |

为什么不用官方 GitHub 仓库：`quectel-open-source/Quectel_QConnectManager_Linux` 目前只有
**V1.6.7**，而那一版把 QMI 拨号路径**删掉了**（`main.c` 里 `software_interface` 只处理
`SOFTWARE_ECM_RNDIS_NCM`，QMI 会打印 `unsupport software_interface 1`，`qmi_request_ops`
在源码中已不存在）。1.6.8 是 OpenWrt 生态实际在用的版本，QMI / MBIM / GobiNet / QRTR 都在。

## 编译

由本包 Makefile 用目标的交叉工具链编译（原 Windows 脚本那套便携版 zig 已随脚本删除）（每个板子一个静态 musl 二进制，
见 `-CMCC` / `-DownloadZig` 参数），产物随 ipk 装到 **`/usr/lib/modemd/quectel-CM`**
（不放 `/usr/bin/`：很多 5G 固件自带 `quectel-CM-5G` 包，会 opkg 文件冲突）。

```
zig cc -target aarch64-linux-musl -static -O2 -w -o quectel-CM <Makefile release 目标的那组 .c> -lpthread
```

源文件清单与上游 `Makefile` 的 `release` 目标一致：
`QmiWwanCM.c GobiNetCM.c main.c QCQMUX.c QMIThread.c util.c qmap_bridge_mode.c mbim-cm.c
device.c atc.c atchannel.c at_tok.c udhcpc.c`
（`qrtr.c` / `rmnetctl.c` 不在内 —— 那两个要 libmnl。）

## 许可

见同目录 `NOTICE`（原文，未改动）。⚠️ **注意它的措辞**：

> This program is totally open souce code, and public domain software for customers of
> Quectel company. … Customers are free to modify the source codes and redistribute them.
> For those who is not Quectel's customer, all rights are closed, and any copying and
> commercial development over this progrma is not allowed.

即：**只对 Quectel 客户放开**修改与再分发。OpenWrt / immortalwrt 把它标成
`PKG_LICENSE:=Public Domain` 并直接打包分发（很多 5G 固件里也预装了这个包），
但如果要把**本项目**连同 quectel-CM 一起对外分发，请自行确认这层授权是否覆盖你的场景。
不想承担这个风险的话，打包时加 `-SkipQuectelCM`（包照出，只是不带 quectel-CM，
此时 `quectel.sh` 会退回用设备上已有的 `/usr/bin/quectel-CM`）。

## 用到的东西

- `main.c` — CLI：`-i <网卡>`（多模组时指定数据网卡）、`-s <apn> [user password auth]`、
  `-4` / `-6`、`-n <pdn>`、`-d`（改用 QMI 取地址而不是内置 udhcpc）、`-v`（详细日志）
- `QMIThread.c` / `QmiWwanCM.c` — QMI WDS 数据呼叫
- `device.c` — 找 `/dev/cdc-wdmN` 与对应网卡（`/sys/class/usbmisc/<dev>/device/net`）
- `udhcpc.c` — 内置 DHCP 客户端（模组以 DHCP 方式下发地址时用）
