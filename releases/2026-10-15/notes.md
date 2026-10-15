OpenFi 6C 的 ImmortalWrt 25.12 固件。内核、USB HNAT、无线驱动和 PassWall2 等与 2026-10-14 版相同（上游 468e251，Linux 6.12.112，闭源 MT WiFi 7.6.7.3），本版调整界面主题、OpenFi 管理页面和在线软件源。

**此版本尚未经实机验证。** 已完成完整编译，并解包固件核对软件源、主题、插件和无线配置；USB HNAT、无线和风扇行为请以实机为准。需要已验证版本请使用 [v25.12-openfi6c-20260912](https://github.com/tcpqueue/immortalwrt-openfi6c-25.12/releases/tag/v25.12-openfi6c-20260912)。

### 更新内容

- 主题：恢复 Aurora 1.4.0（上游当前最新），移除 2026-10-14 版的 Orchard。从 2026-10-14 版保留配置升级时自动切回 Aurora，残留的 Orchard 主题项会被删除；自选的其他主题保持不变。
- OpenFi 插件 2026.10.15-r1：管理页面改为 LuCI JavaScript 实现，不再依赖 luci-compat。页面不再带自定义配色和区块样式，运行状态为普通表格，温控曲线颜色跟随主题，在 Aurora 和 Bootstrap 的浅色、深色及手机布局下与其他页面一致；去掉单独的“OpenFi Settings”页签，原地址仍可打开。风扇、开关和模块复位的配置项与行为不变。
- 修复：风扇状态过期且 CPU 温度传感器不可读时，旧状态接口出错，页面只显示“状态接口未返回数据”，看不到“风扇进程未运行”等诊断。
- 在线软件源改用北京大学镜像 `mirrors.pku.edu.cn/immortalwrt`（25.12-SNAPSHOT，与官方同步）。移除官方未发布的 video 源，`apk update` 不再报该源错误。保留配置升级后同样生效。

### 说明

- 5G 默认不做雷达检测（驱动配置 DfsEnable=0），启动时不需等待约 1 分钟的 CAC，也不会因雷达改变信道或带宽。默认 HE160 在国内使用 36–64 信道，其中 52–64 按规定须做雷达检测，请按当地法规决定是否改为 HE80 或固定 36–48、149–161 信道。
- 在线源提供 ImmortalWrt 官方构建的软件包；内核模块（kmod-*）需随本固件内核编译，在线源中没有与之匹配的版本。

### 升级

升级包约 89.84 MiB，适用官方 OpenFi 6C 256MB NAND 分区布局。从 2026-09-12 或 2026-10-14 版升级可保留配置；从旧 24.10 / 6.6 固件升级时，建议先备份，再不保留旧配置升级。

从 2026-09-12 版升级时：若之前取消勾选过“反转模块复位引脚”，旧版本中该选项不起作用，新版本会实际将复位引脚设为 0。使用移远模块时请保持默认勾选。

下载 `OpenFi6C-25.12-20261015-sysupgrade.bin`，并使用附件 `sha256sums` 核验。`packages.manifest` 为此固件的软件清单。

### 加装包：luci-app-mt5700（MT5700M 5G 模组管理）

单独提供，固件未内置。来自 [LianXia233/luci-app-mt5700](https://github.com/LianXia233/luci-app-mt5700) v1.14.21（`ea320a6`，GPL-3.0），用本固件的 25.12 构建环境编译，架构 aarch64_cortex-a53，内含 Rust 后端（静态链接）。唯一改动是让中文翻译包使用 1.14.21 版本号；对应源码见 `luci-app-mt5700-1.14.21-src.tar.gz`（含改动与编译说明）。

上传两个 apk 到路由器后安装：

```sh
apk add --allow-untrusted /tmp/luci-app-mt5700-1.14.21-r1.apk /tmp/luci-i18n-mt5700-zh-cn-1.14.21.apk
```

- 安装后服务自动启动，并创建 `MT5700M`、`MT5700Mv6` 接口加入 WAN 区域；这两个接口被删除后，服务启动时会重新创建。固件预设的 `wan`、`wan6` 也使用模组的 `usb0`，同一网卡会运行两套 DHCP，请在“网络 → 接口”中删除 `wan`、`wan6`。
- 固件已包含所需的 busybox nc、USB 串口（option、ACM）及 ECM/NCM/RNDIS/MBIM 驱动。
- 未在 OpenFi 6C 搭配 MT5700M 的实机上验证；包签名为本构建的私钥，需 `--allow-untrusted` 安装。
