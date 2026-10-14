# OpenFi 6C：25.12 / Linux 6.12 迁移

硬件依据为 OpenWrt01/immortalwrt-mt798x-24.10 的 openwrt-24.10-6.6 分支，提交 e31e61fc2d6396f290e18cc8fef6f90d29bace61；采用第二版 defconfig/mt7981-ax3000-openfi6c.config。一、二版的关键引脚、分区一致，第二版增加 GPIO11 的 modem LED。

目标为 chasey-dev/immortalwrt-mt798x-rebase 的 25.12 分支，初始迁移基线为提交 dea037c4c6bb81acbf2d6e637c4e1f1abd4e9414（Linux 6.12.103）；2026-10-13 已同步至 468e251661cb46face4ca0c961238282a6eaaa0a，Linux 6.12.112。迁移配置保留官方 MT7981 硬件选项，驱动采用目标仓库 MT WiFi 7.6.7.3 和 mt7981-fw-20260601 固件。

## 硬件迁移

- 使用目标仓库的 mt7981b.dtsi、6.12 公共补丁和 NVMEM fixed-layout 格式，不覆盖为旧 mt7981.dtsi。
- 保留原有 Flash 分区地址：Factory 位于 0x180000，大小 0x200000；UBI 位于 0x680000，大小 0xe980000。保留目标仓库对 Factory、引导及厂商配置分区的只读保护。
- WiFi 绑定 mediatek,wbsys，通过 mediatek,mtd-eeprom 从 Factory 偏移 0 读取。避免原 mt76 的 4 KiB NVMEM cell 限制闭源驱动读取范围。公共设备树已有 conninfra、WED 和保留内存支持。
- 双频 MAC 按原目标板定义，由 Factory+4 的基准 MAC 加 2、加 3 得到，写入 ra0/rax0 对应 DAT；已设置的有效 MAC 保留。
- 保留目标仓库 USB regulator 控制 GPIO8，替代旧 hub_reset 导出；同一引脚只由一个驱动持有。
- GPIO9/10/12 保持官方极性，并使用 lte_power、lte_reset、lte_reset_2 名称与管理插件对接。LED 使用 system、internet、wifi、modem。
- 禁用公共 pwm-fan 节点，启用 PWM0/PWM1 pinctrl。由 OpenFi 服务独占两路反向 25 kHz PWM。
- 使用此前修复的 LuCI 插件：CPU 四点曲线、5% 最低输出、启动助推、过热全速、PWM 初始化重试、表单校验修复、开关控制模块或 LED。2026.10.15-r1 起页面为 LuCI JavaScript 视图，只依赖 luci-base。
- 不迁移官方插件的旧 opkg 软件源和生产测试工具；不包含 qcserial，也不加入 PCIe 模块驱动。

## 网络预设与加速

本分支只注册实体网口对应的 GMAC1，Linux 将它编号为 eth0。旧固件同时注册 GMAC0/1，实体口编号为 eth1；因此不能直接沿用旧名称。

- LAN：实体 eth0，默认 br-lan。
- WAN：RNDIS/ECM 以太网模式 5G 模块，DHCP。
- WAN6：@wan，DHCPv6。

Linux 6.12 usbnet 会根据驱动标记和 MAC 地址命名：RNDIS/ECM 使用局部 MAC 时通常为 usbX，其他情况可能为 ethX，带 FLAG_WWAN 的设备为 wwanX。脚本按 sysfs USB 路径及驱动名识别，而非仅凭接口名称。默认先占位 usb0，启动前扫描及 USB 热插拔时自动绑定实际接口；多个符合条件的设备存在时，优先保留当前设备，否则选择扫描到的第一个。适用于单个 USB 上网模块。

仅接受 rndis_host、cdc_ether、cdc_ncm、huawei_cdc_ncm，排除实体 eth0 和需要独立拨号控制的 qmi_wwan、cdc_mbim。模块需预先处于可通过 DHCP 上网的 USB 模式；此预设不会切换模块工作模式。手动更换 WAN 的协议或设备后，自动绑定不覆盖新设置。可用 uci set network.wan.openfi_usb_auto=0 并提交 network 关闭自动选择。

新仓库 hnat-detect 在接口上线后识别外部 WAN，选择实体 GMAC 作为 PPD，并建立 rxppd 加入 LAN 桥用于回送；不需要复制旧设备树中的空 GMAC0。已选择 kmod-mediatek_hnat、kmod-warp、hnat-detect 和 kmod-dummy。USB 收发及协议解封装仍由 CPU/驱动处理，符合条件的转发/NAT 才进入 PPE。接口注册或加入桥不等于已实测硬件流表绑定。

## 构建

在现有 WSL Linux 原生项目目录执行（先按 README 创建 CPython 3.12.14 虚拟环境）：

```sh
source scripts/openfi-env.sh
cp defconfig/mt7981-ax3000-openfi6c.config .config
make defconfig
make download -j16
make -j16
```

## 软件组合

新增第三方源码直接保存在 package/passwall2、package/passwall-packages、package/tailscale-community、package/openlist、package/luci-theme-aurora 和 package/user-apps。没有添加第三方 feed；标准 feeds 继续提供公共库及构建工具。`./scripts/feeds install -a` 会保留 package 中已有的源码；不要添加 `-f` 强制覆盖。

- PassWall2 26.10.1-r2，Xray 26.10.10，HAProxy 3.2.21（SSL/Lua）。仅选择 Xray 代理核心，不选择独立 V2Ray、Shadowsocks、Sing-box 核心。v2ray-geoip/geosite 为规则数据，保留作为 PassWall2 依赖。
- 社区版 Tailscale LuCI，核心 1.98.3；OpenList LuCI 4.2.4，核心及前端 4.2.6。
- DiskMan 0.2.13、SMART/分区工具；Samba4 4.22.7；TimeWOL 和 etherwake。
- MTK EQoS、TurboACC、HNAT、WED 相关模块；网页终端、uHTTPd 设置、HTTPS LuCI、nftables UPnP。
- LuCI Statistics 和 collectd：CPU、内存、负载、接口、无线、Ping、温度、运行时间及 RRD。接口采集排除 lo，其余接口自动覆盖；Ping 默认沿用上游 localhost，可在页面中改为需要监测的目标。RRD 默认写入 /tmp。
- 防火墙、APK 软件包管理、IPv6、PPPoE；Aurora 1.4.0、Bootstrap 和上述插件中文包。全新配置默认使用 Aurora。从使用 Orchard 的 2026-10-14 固件保留配置升级时，Aurora 重新注册为默认主题，残留的 Orchard 主题项被删除；所选主题已不存在时回到 Aurora（target/linux/mediatek/filogic/base-files/etc/uci-defaults/97-openfi-theme）。
- htop、nano、curl、tcpdump、tcping、ethtool、lscpu、lsof、lm-sensors、CoreMark、SFTP 服务。

源码提交和路径见 [软件来源记录](openfi6c-app-sources.txt)，各插件源码已直接包含在本仓库中。未复制官方仓库的旧软件源。当前支持及维护范围为 ECM / RNDIS 模块模式。

直接复制的 LuCI 软件包及翻译使用显式版本号；没有上游发行版本号的插件采用源码提交日期和提交号，避免脱离原 Git 目录后自动版本变成 0。完整记录见 docs/openfi6c-app-sources.txt。

Xray 26.10.10 的 go.mod 要求 Go 1.27。package/build-tools/golang 提供适配本分支多版本构建系统的 Go 1.27.2，源码散列取自 go.dev 发布清单；Xray 和 OpenList 显式依赖该版本。标准 feeds 的 Go 1.26 保留供其他软件使用。OpenList 的版本信息读取实际选用的 Go 编译器。

## 验证状态

已应用目标仓库全部内核准备补丁，并将新设备树编译为 DTB。检查覆盖闭源 WiFi 绑定、分区地址、GPIO/LED、PWM 所有权、双频 MAC 生成和保留手动值、RNDIS/ECM 名称变化、自动 WAN 选择、排除 QMI/MBIM、风扇控制及目标 25.12 CBI 解析器。

复查命令：先准备项目 .venv（uv venv --python 3.12.14 .venv），执行 make target/linux/prepare，再运行 bash scripts/check-openfi6c.sh。

2026-09-12 已完成 make download -j16 和 make -j16，工具链、Linux 6.12.103、MT WiFi 和 OpenFi 插件均构建通过。已解包 sysupgrade 固件核对驱动模块、插件、网络脚本、启动项、实际设备树和 SHA-256。mt_wifi.ko 的模块依赖包含 mtkhnat，启动加载 MT WiFi 时会解析加载 HNAT。qcserial 未编入。

同日完成上述完整软件组合的多线程下载和构建，最终 make -j16 返回 0。已核对固件 manifest、全部请求的插件和中文翻译、Xray/HAProxy/Tailscale/OpenList/Samba 等实际可执行文件及 collectd 模块。未包含独立 V2Ray、Shadowsocks、Sing-box 核心，LuCI 源码版本均非 0。

随后完成单 MAC USB HNAT 修正：在 MAC 有效性检查之前识别 QDMA/WDMA 回送，使用实际存在的硬件 MAC；NETSYS V2 解析完整四位源端口。设备树显式设置 LAN/PPD eth0、硬件 WAN/LAN2 不匹配占位“/”，防止 USB eth1/eth2 被误分类。160 组实际 RX 分派代码测试及完整构建通过；从升级包提取的内核与本次 Image 完全一致，实际 DTB 的上述属性核对通过。依据和边界见 openfi6c-usb-hnat.md。

当前 sysupgrade 的 SHA-256：e02a228bf4dd8ea2bf08670aef57f1f8d51d21ea2b02da1259b94ab68640e597。Release 文件名为 OpenFi6C-25.12-20260912-USBHNAT-sysupgrade.bin。

2026-09-12 已收到实机使用正常、USB 硬件加速可用的反馈；尚未记录标准化吞吐或射频测试数据。设备树编译的几项警告来自公共 SoC 节点；运行检查脚本后可在 `.build-local/dtc.log` 查看。

2026-10-13 同步上游 468e251 并更新 PassWall2 26.10.1、Xray 26.10.10、Tailscale LuCI d9a3954、OpenList OpenWrt 4bf7266、Go 1.27.2 及 OpenFi 插件 2026.10.13-r1，并以 Orchard 0.3.0-r1 替换 Aurora 后，make download -j16 和 make -j16 返回 0，Xray 实际由 Go 1.27.2 编译，check-openfi6c.sh 与软件包选择检查通过。5119 按上游新的收包代码重写，详见 openfi6c-usb-hnat.md。Orchard 已在 ImmortalWrt 25.12.2 x86 虚拟机检查登录、状态、系统、网络、防火墙和 OpenFi 页面的浅色、深色及手机布局。该版本升级包约 89.70 MiB，SHA-256 为 41a7e3ad09ae7e5813e27e4bba6ad2f8c6ae934691c584d5576c7dcbcd893f16，尚未实机验证。

从此前 24.10 固件升级时，先备份配置，建议不保留旧配置。新版实体接口从旧 eth1 变为 eth0，并使用新的无线配置后端，直接保留旧 network/wireless 可能使网络预设无法生效。
