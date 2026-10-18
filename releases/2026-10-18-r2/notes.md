OpenFi 6C 的 ImmortalWrt 25.12 固件。内核、USB HNAT、WiFi 驱动、主题和 OpenFi 插件与 2026-10-18 版相同（上游 468e251，Linux 6.12.112，闭源 MT WiFi 7.6.7.3）。本版新增模组工具和 PassWall，并同步 PassWall2 及代理依赖。

**本版尚未实机刷写验证。** 已完整编译，并解包固件核对新增程序和插件。基础系统与已实机验证的 2026-10-15 / 2026-10-18 版相同。

### 更新内容

**新增模组工具**
- `adb` 5.0.2：Android 调试桥。可用于带 adb 接口的模组（如部分移远、广和通模组开启调试口后）。
- `qfirehose` 1.4.21：移远模组 Firehose 刷机工具，来自 [QModem](https://github.com/FUjr/QModem)。用法 `qfirehose -f 固件目录`。刷错固件或刷写中断可能使模组变砖，请自行承担风险。
- `minicom` 2.9：串口终端，例如 `minicom -D /dev/ttyUSB2` 直接发送 AT 指令。

**新增 PassWall 26.10.16-r2**（[Openwrt-Passwall/openwrt-passwall](https://github.com/Openwrt-Passwall/openwrt-passwall) `00202bb`）
- 与 PassWall2 一致，只选 Xray 核心，nftables 透明代理，包含 HAProxy 和 Geoview；不包含 Sing-box、Shadowsocks-Rust、SSR、Simple-Obfs、V2ray-Plugin。
- 随之加入依赖 dns2socks 2.1、microsocks 1.0.5、ipt2socks 1.1.4。
- ImmortalWrt luci 源中有旧版同名插件，本固件已改用上游最新版。
- PassWall 和 PassWall2 可同时安装，但请只启用其中一个，否则两者的透明代理规则会冲突。

**同步 PassWall2 和代理依赖**
- PassWall2 同步至上游 `e378885`（版本号仍为 26.10.1-r2，含订阅解析、中文翻译等上游后续修改）。
- 依赖包同步至 openwrt-passwall-packages `3ab1385`：Xray 26.10.10 → 26.10.17（仍用 Go 1.27.2 编译），v2ray-geoip / geosite 更新至 2026-10-16 数据。

### 说明

- 5G 默认不做雷达检测（DfsEnable=0），启动时不需等待 CAC。默认 HE160 在国内使用 36–64 信道，其中 52–64 按规定须做雷达检测，请按当地法规决定是否改为 HE80 或固定 36–48、149–161 信道。
- 默认发射功率为 100%（2.4G 约 25 dBm，5G 约 24 dBm），未启用按国家的功率限制表（SKUenable=0），请按当地法规自行调低。
- 在线源提供 ImmortalWrt 官方构建的软件包；内核模块（kmod-*）需随本固件内核编译，在线源中没有与之匹配的版本。

### 升级

升级包约 89.86 MiB，适用官方 OpenFi 6C 256MB NAND 分区布局。从 2026-09-12 及之后的 25.12 版升级可保留配置；从旧 24.10 / 6.6 固件升级时，建议先备份，再不保留旧配置升级。

从 2026-09-12 版升级时：若之前取消勾选过“反转模块复位引脚”，旧版本中该选项不起作用，新版本会实际将复位引脚设为 0。使用移远模块时请保持默认勾选。

下载 `OpenFi6C-25.12-20261018-r2-sysupgrade.bin`，并使用附件 `sha256sums` 核验。`packages.manifest` 为此固件的软件清单。

### 加装包：luci-app-mt5700

MT5700M 模组管理插件继续使用 [2026-10-15 版](https://github.com/tcpqueue/immortalwrt-openfi6c-25.12/releases/tag/v25.12-openfi6c-20261015)附带的 `luci-app-mt5700-1.14.21-r2.apk`，与本固件兼容，安装方法见该版说明。
