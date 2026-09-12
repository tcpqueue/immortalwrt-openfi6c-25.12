OpenFi 6C 的 ImmortalWrt 25.12 固件，Linux 6.12.103，闭源 MT WiFi 7.6.7.3。

- 设备使用和 USB HNAT 已获实机验证。
- 当前支持 5G 模块通过 ECM / RNDIS 上网，推荐带独立 Web 管理页面的移远高通 X55、X62 系列模块。
- 实体 eth0 为 LAN，USB 模块为 DHCP WAN；修正单 MAC 回送、MT7981 WDMA 端口解析及 USB 接口误分类。
- CPU 四点风扇曲线，可拖动调节，最低输出 5%，带启动助推和过热全速。
- 包含 PassWall2（Xray + HAProxy）、Tailscale 社区版、OpenList、DiskMan、Samba4、MTK EQoS/TurboACC、统计图表等软件及中文包。

升级包约 89.25 MiB，适用官方 OpenFi 6C 256MB NAND 分区布局。从旧 24.10 / 6.6 固件升级时，建议先备份，再不保留旧配置升级。

已完成完整编译、设备树与风扇/USB WAN 检查、160 组 HNAT 收包分派测试，以及升级包内核、设备树、插件和 SHA-256 校验。

下载 `OpenFi6C-25.12-20260912-USBHNAT-sysupgrade.bin`，并使用附件 `sha256sums` 核验。`packages.manifest` 为此固件的软件清单。
