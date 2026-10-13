# OpenFi 6C USB HNAT 核对与修正

## 对照范围

- OpenFi 6C 基线：chasey-dev/immortalwrt-mt798x-rebase，25.12，dea037c4c6bb81acbf2d6e637c4e1f1abd4e9414。
- WiFi 7 对照：同仓库 25.12-dev-wifi7，1b6fa75ca02aa7a7dd6d0b6c68865f16eef5ae0f；对照源码与该远端分支提交一致。
- 旧 6.6 参考：padavanonly/immortalwrt-mt798x-6.6 的 9999-01-hnat.patch。

两分支的 HNAT 基础源码和 hnat-detect 工具一致，均已支持外部接口加速。5109、5112、5113 补丁的差异仅为上下文行号；5110 的 WiFi 7 版本将 QDMA 回送识别扩展到 NETSYS V3，但仍假设硬件 MAC0 存在。WiFi 7 分支的 5102 补丁为 NETSYS V3 选择 32K 流表；MT7981 的 NETSYS V2 仍为 16K，无需移植该差异。

## 数据路径

1. USB RNDIS/ECM 驱动完成 USB 收包和协议处理，生成以太网 skb。
2. 外部接口按 wwan、usb、eth 前缀注册到 HNAT，实际 MTK GMAC 会按设备树兼容字符串排除。
3. 非 WHNAT 的外部接口在 IPv4/IPv6 prerouting 中补充 HNAT 标记，通过实际 GMAC 的 QDMA 发送入口送入 PPE。此处用于内部 DMA，不是向网线上发包。
4. 未绑定流的学习回送包通过 rxppd 回到内核，恢复外部接口身份并完成路由/NAT 学习；符合条件的后续流进入 PPE。
5. 发往 USB 的已绑定流由 PPE 返回 CPU，通过流表中的外部接口索引交给 USB 网卡驱动发送。USB 总线搬运和 RNDIS/ECM 解封装仍需要 CPU。

hnat-detect 从 fw4 的 NAT 区域识别外部 WAN，并把 rxppd 加入 LAN 桥。NETSYS V2 的无载波 PPD 发送补丁 5116 已存在，物理网线未连接时的内部 QDMA 提交不走普通 carrier 限制。

## OpenFi 单网口问题

旧固件同时暴露硬件 MAC0 和 MAC1，Linux 名称为 eth0、eth1，实体口在后者。新仓库只暴露 mac@1，reg=1；它是第一个注册的网卡，所以 Linux 名称为 eth0。Linux 名称不会改变硬件槽位，eth->netdev[0] 仍为空。

原 5110 补丁把 QDMA 回送包强制归到硬件 MAC0。驱动随后检查 eth->netdev[mac]，会在 rxppd 重定向之前丢弃该包。因此，“已包含 HNAT 模块和外部接口注册”不足以证明这一板级拓扑可用。

另一个问题是 NETSYS V2 的 HNAT 描述符使用四位 sport（rxd4 的 22:19），WiFi WDMA 端口为 8、9。普通网口的 RX_DMA_GET_SPORT 只取三位，并把第 22 位视作特殊标记。只修 MAC 选择仍会漏掉 WiFi 回送，必须在物理网口检查之前使用完整 HNAT 端口及 CPU reason 识别内部包。

## 修正范围

新增 5119 补丁，不直接覆盖 WiFi 7 分支补丁组：

- 统一 QDMA 和 WDMA 回送识别。WDMA 仅在 HIT_BIND_FORCE_CPU 时走回送路径。
- NETSYS V2 从完整四位 HNAT 端口解析回送；普通物理收包保留原特殊标记处理。
- 回送选择实际存在的 GMAC，原有 MAC0 存在时仍优先使用；只有 MAC1 的 OpenFi 选择槽位 1。
- 没有 rxppd 或没有可用 GMAC 时按原保护逻辑丢弃；不修改原始描述符中的流表索引、源端口和 PPE 元数据。
- 设备树将 LAN/PPD 设为 eth0，硬件 WAN/LAN2 设为不匹配任何网卡名的“/”。USB 仍由外部接口表处理，避免 USB 被命名为 eth1/eth2 时误走硬件 WAN/LAN2 分支。Linux 网络 WAN 仍为 USB DHCP，这些属性不是 network.wan 配置。

## 验证

tests/openfi6c/hnat_rx.py 从实际已应用补丁的内核源码提取 RX 分派代码，从驱动头文件提取端口、CPU reason 和 HNAT 描述符定义，编译并运行带未定义行为检查的 C 测试。160 个用例覆盖 NETSYS V2/V3、MAC0/1/2 的不同存在组合、QDMA 学习、WDMA 回送、普通物理收包和缺少 rxppd。

原代码可重现失败，修正后 160 个用例通过。设备树测试同时确认 mac@1、WAN/LAN2 不匹配占位、LAN/PPD eth0 和外部接口前缀。发布记录、软件清单和镜像校验和见 [2026-09-12 发布目录](../releases/2026-09-12/)。

2026-09-12 完整 make -j16 返回 0。升级包内 FIT 的 LZMA 内核解压后与本次 Image 逐字节一致，内核 SHA-256 为 400a8d4dc8411f3076f2973a88f5b4544bb858ae872d1eca179c3e7540042a9a；FIT 设备树与构建产物一致，上述 HNAT 属性及单 MAC1 拓扑已从升级包内直接验证。sysupgrade SHA-256 为 e02a228bf4dd8ea2bf08670aef57f1f8d51d21ea2b02da1259b94ab68640e597。

2026-09-12 已收到 USB 硬件加速可用的实机反馈，未公布具体吞吐或 CPU 占用数据。后续复核可分别用有线客户端和 WiFi 客户端经 USB WAN 传输，核对外部接口表、rxppd 桥接、PPD 状态和对应流的 BIND 状态/计数变化。仅看到 hook_toggle=enabled 或 external_interface 中出现 usb0，不能判定实际已加速。代理及加密运算也不属于此处的硬件 NAT。

## 2026-10-13 上游同步

上游同步至 468e251（Linux 6.12.112）后，5110 补丁改为在描述符解析前声明 sport，并把 QDMA 回送判断移到各 NETSYS 分支之后，原 5119 的 3 个块无法应用。新代码仍把 QDMA 回送固定到硬件 MAC0，NETSYS V2 仍只取三位源端口，因此单 MAC1 拓扑的问题依旧存在，5119 仍然必要。

5119 已按新结构重写，语义与 2026-09-12 实机验证版本相同：统一 QDMA/WDMA 回送识别，NETSYS V2 使用完整四位端口及 CPU reason，选择第一个实际存在的 GMAC，并移除后段重复的 WDMA 判断。对 6.12.112 应用全部通用及 mediatek 补丁后，5119 及其后补丁均可应用；hnat_rx.py 对未加 5119 的上游驱动失败，重写后 160 个用例通过。

同期上游与 HNAT 相关的修正：NETSYS V2 读取 mcast_table 调试文件导致的空指针崩溃已修复；NETSYS V2 流表维持 16K，避免 14 位 RX 索引无法定位 32K 表上半部分的条目；EQoS 改为保留 HNAT 例外标记第 7 位。

2026-10-13 完整 make -j16 返回 0，内核 6.12.112 及 kmod-mediatek_hnat 由已应用新 5119 的源码重新编译；scripts/check-openfi6c.sh 全部通过。sysupgrade SHA-256 为 c5ac01e6c37cec2dac3327bab135068cd27d9888348bbb0427765f380bb8afc8，尚待实机复核 USB 流的 BIND 状态，未发布 Release。
