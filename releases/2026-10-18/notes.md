OpenFi 6C 的 ImmortalWrt 25.12 固件。内核、USB HNAT、主题、插件和软件清单与 2026-10-15 版相同（上游 468e251，Linux 6.12.112，闭源 MT WiFi 7.6.7.3），本版只修正 WiFi 驱动启动时的误报日志。

### 实机验证

2026-10-15 版已在 OpenFi 6C（搭配移远 RM520N-EU，ECM 模式）上实机运行：

- 正常启动，千兆网口、2.4G / 5G WiFi 工作正常
- 模组 `usb0` 通过 DHCP 获取 IPv4 和 IPv6 地址
- USB HNAT 上下行流量均进入 BIND 状态
- 风扇服务运行，USB 存储（ext4）自动挂载
- 驱动读取的 EEPROM 与本机 Factory 分区一致（芯片标识、MAC、校准数据）

本版的 WiFi 驱动改动已在同一台设备上用相同编译的驱动重启验证。完整的 2026-10-18 升级包尚未在实机上刷写。

### 更新内容

闭源 WiFi 驱动每次启动约有 40 行错误级日志，都不是真实故障。本版去掉这些误报，驱动行为不变：

- `CTS/ACK FEATURE is not enable` 和 `DAT config band(x) cck/ofdm/ofdma_timeout Fail`（每次 12 行）：ACK/CTS 超时功能默认关闭时，驱动仍会尝试写入默认超时值，写入被拒绝后报错。现在功能关闭时直接跳过，以前这一步也从未生效。
- `invalid ch_band 0`（2.4G 每次 26 行）：2.4G 没有 80/160MHz 信道组，驱动仍逐信道查询并报错。现在 2.4G 直接返回，结果不变。
- `INDEX0_debug_level / INDEX0_debug_option not found`：l1profile 补上这两项。调试级别设为 1（只输出错误级，原默认会输出大量扫信道日志），输出格式保持驱动默认值。

### 说明

- dmesg 中剩下的提示不影响使用：`efuse = deaddead`（校准数据从 Flash 读取，不使用片内 efuse）、`eeprom 0x19a bit 0 is 0, do runtime cal`（出厂未预存 DPD 数据，开机现场校准）、`mtk_soc_eth: legacy DT: missing interrupt-names`（上游 MT7981 通用提示）、`block: unable to load configuration (fstab...)`（OpenWrt 挂载 overlay 前的标准检查）、模组 `invalid interface number`（模组自身 USB 描述符问题）。另外，MTK 驱动把不少普通信息也按 L1 级别输出，这部分仍会出现。
- 5G 默认不做雷达检测（驱动配置 DfsEnable=0），启动时不需等待约 1 分钟的 CAC，也不会因雷达改变信道或带宽。默认 HE160 在国内使用 36–64 信道，其中 52–64 按规定须做雷达检测，请按当地法规决定是否改为 HE80 或固定 36–48、149–161 信道。
- 默认发射功率为 100%（2.4G 约 25 dBm，5G 约 24 dBm），未启用按国家的功率限制表（SKUenable=0），请按当地法规自行调低。
- 在线源提供 ImmortalWrt 官方构建的软件包；内核模块（kmod-*）需随本固件内核编译，在线源中没有与之匹配的版本。

### 升级

升级包约 89.84 MiB，适用官方 OpenFi 6C 256MB NAND 分区布局。从 2026-09-12、2026-10-14 或 2026-10-15 版升级可保留配置；从旧 24.10 / 6.6 固件升级时，建议先备份，再不保留旧配置升级。

从 2026-09-12 版升级时：若之前取消勾选过“反转模块复位引脚”，旧版本中该选项不起作用，新版本会实际将复位引脚设为 0。使用移远模块时请保持默认勾选。

下载 `OpenFi6C-25.12-20261018-sysupgrade.bin`，并使用附件 `sha256sums` 核验。`packages.manifest` 为此固件的软件清单。

### 加装包：luci-app-mt5700

MT5700M 模组管理插件继续使用 [2026-10-15 版](https://github.com/tcpqueue/immortalwrt-openfi6c-25.12/releases/tag/v25.12-openfi6c-20261015)附带的 `luci-app-mt5700-1.14.21-r2.apk`，与本固件兼容，安装方法见该版说明。
