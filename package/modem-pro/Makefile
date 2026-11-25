#
# modem-pro —— 5G 多模组管理器（modemd）的 OpenWrt 打包
#
# 一个 Makefile 出两个包，选中就直接打包，不拆成一堆子包：
#
#   modem-pro        守护进程包：Go 主程序 + third_party 里的四家厂商 C 拨号器
#                    + /etc/init.d/modemd + netifd 的 quectel 协议 + 默认配置
#   luci-app-modemd-pro  界面包：LuCI 控制器 / JS 视图 / 菜单 / ACL（依赖 modemd-pro）
#
# ★ files/third_party 的 C 代码由本包自己用「目标的交叉工具链」编成静态 musl 可执行文件，
#   直接落在 /usr/lib/modemd/，和守护进程打进同一个 ipk：
#     · 不拆成独立 OpenWrt 包（不按 OpenWrt 那套把它拆散）
#     · 不依赖 zig（Windows 侧那份脚本是给没有交叉工具链的机器用的；这里工具链是现成的）
#   源文件清单与编译开关见下方清单（取自原 Windows 打包脚本，脚本已删）（那份在真机上验证过）。
#
# 依赖：需要 golang feed（提供 golang/host 和 golang-package.mk）
#     ./scripts/feeds install -a -p golang
#   没有这个 feed 时本包自动标 @BROKEN —— Makefile 解析照样成功，整棵树不会坏。
#
# 版本号单一来源：files/packaging/openwrt/VERSION（形如 0.2.300-1），改版本只改那一个文件。
include $(TOPDIR)/rules.mk

MODEM_PRO_VER:=$(strip $(file <$(CURDIR)/files/packaging/openwrt/VERSION))
PKG_VERSION:=$(word 1,$(subst -, ,$(MODEM_PRO_VER)))
PKG_RELEASE:=$(strip $(word 2,$(subst -, ,$(MODEM_PRO_VER))))
ifeq ($(PKG_VERSION),)
PKG_VERSION:=0.2.300
PKG_RELEASE:=1
endif

# LuCI 菜单标题用哪种语言：menu.d/*.json 里的标题在 JS 版 LuCI 里**没有**可用的服务端翻译
# （服务端只认 .lmo，而我们的 app 没有 i18n 包），所以标题必须在打包时写成一种语言。
#   zh-cn（默认，同 postinst 里 auto/空 的处理）→ 简体
#   zh-tw → 繁体      en → 保持英文
# 设备端 postinst 还会按 uci 的 luci.main.lang 再精修（见 files/packaging/openwrt/luci/control/postinst）：
# 它同时认得「英文标题」和「已经是中文的标题」，所以两种来源都覆盖。
MODEM_PRO_LUCI_LANG?=zh-cn

PKG_NAME:=modemd-pro
PKG_MAINTAINER:=daocaoren
PKG_LICENSE:=MIT
PKG_BUILD_PARALLEL:=1
PKG_USE_MIPS16:=0

# ---- golang 框架（可选依赖：没有 golang feed 就标 @BROKEN）----
GO_MK:=$(TOPDIR)/feeds/packages/lang/golang/golang-package.mk
GO_FOUND:=$(if $(wildcard $(GO_MK)),1,0)
ifeq ($(GO_FOUND),1)
PKG_BUILD_DEPENDS:=golang/host
endif

# Go 主程序：纯 Go（无 cgo）。files/web 与 module/bands/*.json 是 go:embed 的，必须一起进构建目录
GO_PKG:=modemd
GO_PKG_BUILD_PKG:=modemd
GO_PKG_INSTALL_BIN_PATH:=/usr/bin

# 去掉符号表与 DWARF（-s -w），和 files/tools/script/Makefile 的 fast 目标一致。
# ⚠️ 实测对**打出来的 ipk 没有任何影响**：OpenWrt 的 scripts/rstrip.sh 在打包时本来就会
#    strip 一遍（加不加这一行，ipk 里的 modemd 都是 10741344 字节；对已 -s -w 的产物再跑
#    strip 也是 0 变化）。留着是为了万一 strip 被关掉、或手工用这个 Makefile 编的时候也有用。
#    modemd 有 10MB 是因为 Go 运行时 + go:embed 的整页调试台，不是因为没 strip。
#
# -X 把版本号注入到 `GET /api/version`（module/version.BuildVersion）。
# ⚠️ 路径必须跟着包走：包一改名（曾经的 corev2 → module/version）这里不改，
#    Binary 会静默继续显示 "dev"，而 build 一切正常 —— 只能靠页面核对才能发现。
GO_PKG_LDFLAGS:=-s -w -X modemd/module/version.BuildVersion=$(PKG_VERSION)-$(PKG_RELEASE)

# 国内拉不动 proxy.golang.org，默认走 goproxy.cn（想换：make GOPROXY=...）
GOPROXY?=https://goproxy.cn,direct
export GOPROXY

include $(INCLUDE_DIR)/package.mk
ifeq ($(GO_FOUND),1)
include $(GO_MK)
endif

##############################################################################
# files/third_party 的厂商 C 拨号器
#
# 源文件清单 / 额外开关全部见下方清单—— 那份清单是逐条对过
# 上游 Makefile 的 release 目标挑出来的，别随手改：
#   quectel-CM   14 个 .c（不含 qrtr.c / rmnetctl.c：那俩要 libmnl）
#   simcom-cm     7 个 .c（不含 dhcpclient.c：SIMCom 自己的 Makefile 也没编它）
#   fibocom-dial  8 个 .c + 自带 libmnl 的 5 个 .c（ndhcp：自带 netlink DHCP，
#                 不依赖设备上的 busybox udhcpc）；必须带 -DGHT_FEATURE_PCIE_AUTO
#   meig-cm       9 个 .c
# 这批代码是 gcc 时代的写法（隐式函数声明、非 void 函数里裸 return），
# 所以 fibocom/meig 要 -std=gnu89 -Wno-error=return-type 才编得过。
##############################################################################
TP_SRC:=$(CURDIR)/files/third_party
TP_OUT:=$(PKG_BUILD_DIR)/dialers

QUECTEL_CM_SRC:=QmiWwanCM.c GobiNetCM.c main.c QCQMUX.c QMIThread.c util.c \
	qmap_bridge_mode.c mbim-cm.c device.c atc.c atchannel.c at_tok.c udhcpc.c
SIMCOM_CM_SRC:=QmiWwanCM.c GobiNetCM.c main.c MPQMUX.c QMIThread.c util.c udhcpc.c
FB_DIAL_SRC:=QmiWwanCM.c GobiNetCM.c main.c MPQMUX.c QMIThread.c util.c \
	qmap_bridge_mode.c query_pcie_mode.c udhcpc_netlink.c \
	libmnl/ifutils.c libmnl/attr.c libmnl/callback.c libmnl/nlmsg.c libmnl/socket.c
MEIG_CM_SRC:=QmiWwanCM.c GobiNetCM.c main.c MPQMUX.c QMIThread.c util.c \
	udhcpc.c qmap_bridge_mode.c mbim-cm.c device.c

LEGACY_CFLAGS:=-std=gnu89 -Wno-error=return-type

# -O2 -w 关警告、-s 去符号表。工具链里有 libc.a 就静态链接（换板子/换 libc 也能跑，
# 与 Windows 侧 zig 产物行为等价）；万一没有就退回动态，不会因此编不过。
DIALER_CFLAGS:=-O2 -w -s \
	$(if $(wildcard $(TOOLCHAIN_DIR)/lib/libc.a $(TOOLCHAIN_DIR)/lib64/libc.a),-static)

# $(1)=产物名  $(2)=源码目录  $(3)=源文件列表  $(4)=额外开关（可选）
define ModemPro/Build/Dialer
	$(TARGET_CC) $(TARGET_CPPFLAGS) $(TARGET_CFLAGS) $(DIALER_CFLAGS) $(4) -o $(TP_OUT)/$(1) \
		$(addprefix $(2)/,$(3)) -lpthread
endef

##############################################################################
# 构建
##############################################################################
# 只把 Go 源码铺进构建目录（C 源码直接从 $(CURDIR)/files/third_party 编，不搬不污染）
#
# ⚠️ 这里漏一个目录 = ipk 编不出来（构建目录里只有 main.go，`go build` 找不到包）。
#    目录结构变过两次（corev2 → module/kernel、新增 iface/），每次都必须同步这份清单。
define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
	$(CP) $(CURDIR)/go.mod $(CURDIR)/go.sum $(CURDIR)/main.go $(CURDIR)/server_tls.go $(CURDIR)/web.go $(PKG_BUILD_DIR)/
	mkdir -p $(PKG_BUILD_DIR)/files
	$(CP) $(CURDIR)/module $(CURDIR)/iface $(PKG_BUILD_DIR)/
	$(CP) $(CURDIR)/files/web $(PKG_BUILD_DIR)/files/
endef

define Build/Compile
	# 1) Go 主程序（走 OpenWrt 的 golang 框架：自己编一份 host go，交叉编译）
	$(call GoPackage/Build/Compile)
	# 2) 厂商 C 拨号器（用目标的交叉工具链，进同一个 ipk）
	mkdir -p $(TP_OUT)
	$(if $(CONFIG_MODEM_PRO_QUECTEL),$(call ModemPro/Build/Dialer,quectel-CM,$(TP_SRC)/quectel-cm,$(QUECTEL_CM_SRC)))
	$(if $(CONFIG_MODEM_PRO_SIMCOM),$(call ModemPro/Build/Dialer,simcom-cm,$(TP_SRC)/simcom-cm/src,$(SIMCOM_CM_SRC)))
	$(if $(CONFIG_MODEM_PRO_FIBOCOM),$(call ModemPro/Build/Dialer,fibocom-dial,$(TP_SRC)/fibocom-dial/src,$(FB_DIAL_SRC),-DGHT_FEATURE_PCIE_AUTO $(LEGACY_CFLAGS)))
	$(if $(CONFIG_MODEM_PRO_MEIG),$(call ModemPro/Build/Dialer,meig-cm,$(TP_SRC)/meig-cm/src,$(MEIG_CM_SRC),$(LEGACY_CFLAGS)))
endef

##############################################################################
# 包定义
##############################################################################
define Package/modemd-pro
  SECTION:=net
  CATEGORY:=Network
  TITLE:=5G 多模组管理器（modemd）+ 四家厂商主机侧拨号器
  URL:=https://github.com/daocaoren/modem-pro
  # 模组相关的运行时依赖：勾了 modem-pro 就一起带上（这几个包在 lede 里的位置：
  #   adb-enablemodem / comgt[+directip,ncm] / umbim / uqmi → package/network/utils/
  #   modemband                                        → feeds/packages/net/
  #     adb-enablemodem 用 adb 把被锁住的模组拉起来；modemband 改 LTE/5G 频段；
  #     uqmi / umbim 是 QMI / MBIM 控制口工具；comgt(+directip/ncm) 是老的 GPRS/NCM 拨号工具
  DEPENDS:=+kmod-usb-serial-option +kmod-usb-wdm +adb-enablemodem \
 +comgt +comgt-directip +comgt-ncm +modemband +umbim +uqmi \
	$(if $(filter 0,$(GO_FOUND)),@BROKEN)
endef

define Package/modemd-pro/description
  modemd 是 5G 模组管理器：AT 收发 / 短消息 / 锁网锁频 / 流量配额 / 信号历史 / 微信推送，
  自带 HTTP 调试台（页面已 go:embed 进二进制）。

  这个包一次装齐（选中它就全打包，files/third_party 的 C 代码不单独出包）：
    /usr/bin/modemd              Go 主程序
    /usr/lib/modemd/<厂商拨号器>  四家厂商主机侧拨号器（源码来自 files/third_party/，本包自己交叉编译）
    /etc/init.d/modemd           procd 启动脚本
    /lib/netifd/proto/quectel.sh netifd 的 proto=quectel（手工配接口时用）
    /etc/modemd/config.toml      默认配置（conffile，升级不覆盖）

  网页面板见 luci-app-modemd-pro 包。
endef

define Package/modemd-pro/config
	menu "modem-pro 选项"
		depends on PACKAGE_modemd-pro

		config MODEM_PRO_QUECTEL
			bool "打进移远 quectel-CM 拨号器（Quectel EC20 / RM500 等）"
			default y

		config MODEM_PRO_SIMCOM
			bool "打进芯讯通 simcom-cm 拨号器（SIMCom SIM7600 / SIM8200 等）"
			default y

		config MODEM_PRO_FIBOCOM
			bool "打进广和通 fibocom-dial 拨号器（Fibocom FM150 / FM350 / FM650 等）"
			default y

		config MODEM_PRO_MEIG
			bool "打进美格 meig-cm 拨号器（MeiG SRM815 / SLM750 等）"
			default y
	endmenu
endef

define Package/modemd-pro/conffiles
/etc/modemd/config.toml
endef

define Package/modemd-pro/install
	# ---- Go 主程序 → /usr/bin/modemd（golang 框架负责安装 + strip）----
	$(call GoPackage/Package/Install/Bin,$(1))
	# ---- 厂商拨号器 → /usr/lib/modemd/ ----
	$(INSTALL_DIR) $(1)/usr/lib/modemd
	$(if $(CONFIG_MODEM_PRO_QUECTEL),$(INSTALL_BIN) $(TP_OUT)/quectel-CM $(1)/usr/lib/modemd/quectel-CM)
	$(if $(CONFIG_MODEM_PRO_SIMCOM),$(INSTALL_BIN) $(TP_OUT)/simcom-cm $(1)/usr/lib/modemd/simcom-cm)
	$(if $(CONFIG_MODEM_PRO_FIBOCOM),$(INSTALL_BIN) $(TP_OUT)/fibocom-dial $(1)/usr/lib/modemd/fibocom-dial)
	$(if $(CONFIG_MODEM_PRO_MEIG),$(INSTALL_BIN) $(TP_OUT)/meig-cm $(1)/usr/lib/modemd/meig-cm)
	# ---- 默认配置 → /etc/modemd/config.toml（conffile）----
	$(INSTALL_DIR) $(1)/etc/modemd
	$(INSTALL_CONF) $(CURDIR)/config.toml $(1)/etc/modemd/config.toml
	$(INSTALL_DIR) $(1)/lib/upgrade/keep.d
	printf '%s\n' /etc/modemd/modemd-tls.pem > $(1)/lib/upgrade/keep.d/modemd-pro-tls
	# ---- OpenWrt 载荷：procd 启动脚本 + netifd 协议 ----
	$(CP) $(CURDIR)/files/packaging/openwrt/files/* $(1)/
	$(INSTALL_BIN) $(CURDIR)/files/packaging/openwrt/files/etc/init.d/modemd $(1)/etc/init.d/modemd
	$(INSTALL_BIN) $(CURDIR)/files/packaging/openwrt/files/lib/netifd/proto/quectel.sh $(1)/lib/netifd/proto/quectel.sh
endef

define Package/luci-app-modemd-pro
  SECTION:=luci
  CATEGORY:=LuCI
  SUBMENU:=3. Applications
  TITLE:=LuCI app - 5G 终端（modemd 调试台）
  URL:=https://github.com/daocaoren/modem-pro
  # ★ 必须带 luci-proto-mbim：MBIM 模组的接口是 proto=mbim，拨号由 netifd 的 mbim.sh(umbim)
  #   做（umbim 已在 modemd-pro 的依赖里），但 **LuCI 那侧还要 protocol/mbim.js** 才认得这个
  #   协议。缺了它，「网络 → 接口」那页对 modem<id> 只会显示「不支持的协议类型。」，而且
  #   编辑按钮被禁用 —— 连 APN 都填不进去（真机现象：ubus 报 mbim/NO_APN 却无从下手）。
  DEPENDS:=+modemd-pro +luci-base +luci-proto-mbim
endef

define Package/luci-app-modemd-pro/description
  modem-pro 的 LuCI 界面：设备中心 / 5G 终端（实时数据 + 服务启停），
  同时带 Lua 控制器与 JS 视图两套（安装脚本会按固件的 LuCI 类型自动只留一套）。
endef

define Package/luci-app-modemd-pro/install
	$(CP) $(CURDIR)/files/packaging/openwrt/luci/files/* $(1)/
	# 菜单标题：打包时按 MODEM_PRO_LUCI_LANG 写死（这样全新刷机也直接是中文，不靠 postinst）
	@case "$(MODEM_PRO_LUCI_LANG)" in \
		en) : ;; \
		zh-tw) \
			sed -i 's/"5G Terminal"/"5G終端"/g; s/"Device Center"/"設備中心"/g; s/"Service Control"/"服務控制"/g' \
				$(1)/usr/share/luci/menu.d/luci-app-modemd.json; \
			echo "  luci-app-modemd-pro: 菜单标题写成繁体中文" ;; \
		*) \
			sed -i 's/"5G Terminal"/"5G终端"/g; s/"Device Center"/"设备中心"/g; s/"Service Control"/"服务控制"/g' \
				$(1)/usr/share/luci/menu.d/luci-app-modemd.json; \
			echo "  luci-app-modemd-pro: 菜单标题写成简体中文" ;; \
	esac
	# ⚠️ Lua 视图 / JS 视图：**打包时就只装一套**
	#   OpenWrt 23.05+ 的 ucode LuCI 带 Lua 兼容桥（由 luci-compat 提供 ucodebridge.lua），
	#   桥会**优先渲染** view/modemd/*.htm，而这时 Lua 控制器并没有被 dispatcher 加载
	#   ⇒ 页面直接 500（真机实测：Failed to load template 'modemd/overview'）。
	#   postinst 里也有一段同样的清理逻辑，但那段在**镜像编译期**会被 IPKG_INSTROOT 提前
	#   return 掉（预装 rootfs 时不跑脚本），所以必须在打包这里就决定：有 luci-compat
	#   （= ucode 桥）就不打 Lua 那套，只留 JS 视图。
	@if [ -n "$(CONFIG_PACKAGE_luci-compat)" ]; then \
		echo "  luci-app-modemd-pro: ucode LuCI（有 luci-compat）→ 只装 JS 视图"; \
		rm -rf $(1)/usr/lib/lua; \
	else \
		echo "  luci-app-modemd-pro: 老 Lua LuCI → Lua 控制器 + 视图"; \
	fi
endef

##############################################################################
# postinst / prerm / postrm
#
# 直接引用仓库里那两份脚本（单一来源，Windows 侧 ipk 脚本读的是同一份，不会漂移）。
# 注意用 $(file <...) 读入：它保留换行与 $（$(shell cat) 会把换行压成空格，不能用），
# 需要 GNU Make >= 4.2（Ubuntu 20.04+ / Debian 11+ 都满足）。
##############################################################################
MODEM_PRO_POSTINST:=$(file <$(CURDIR)/files/packaging/openwrt/control/postinst)
MODEM_PRO_PRERM:=$(file <$(CURDIR)/files/packaging/openwrt/control/prerm)
# ⚠️ 只替换**带引号**的那个 __PKGVER__，也就是 `__ver="__PKGVER__"` 这一处。
# 那份脚本里 __PKGVER__ 一共出现 3 次，另外两处**不带引号**：
#   ① 上面注释里提到"原 Windows 打包脚本（已删） 把 __PKGVER__ 换成真版本"（不该动）
#   ② 紧跟着的兜底 `case "$__ver" in *__PKGVER__*) __ver="" ;; esac`
# 全局替换（PowerShell 的 .Replace() 是无引号的全局；不带引号的 $(subst) 也是）
# 会把 ② 的模式也改成版本号 ⇒ 它必然命中自己 ⇒ __ver 被清空 ⇒ 最后退成 "dev"。
# 只换带引号的那一处，__ver 就是真版本，② 也按原意不触发。
LUCI_POSTINST:=$(subst "__PKGVER__","$(PKG_VERSION)-$(PKG_RELEASE)",$(file <$(CURDIR)/files/packaging/openwrt/luci/control/postinst))
LUCI_POSTRM:=$(file <$(CURDIR)/files/packaging/openwrt/luci/control/postrm)

ifeq ($(strip $(MODEM_PRO_POSTINST)),)
$(warning modem-pro: 读不到 files/packaging/openwrt/control/postinst（需要 GNU Make >= 4.2）—— 安装后不会自动 enable/启动服务)
endif
ifeq ($(strip $(LUCI_POSTINST)),)
$(warning luci-app-modemd-pro: 读不到 files/packaging/openwrt/luci/control/postinst —— LuCI 缓存不会被清，菜单可能不刷新)
endif

define Package/modemd-pro/postinst :=
$(MODEM_PRO_POSTINST)
endef

define Package/modemd-pro/prerm :=
$(MODEM_PRO_PRERM)
endef

define Package/luci-app-modemd-pro/postinst :=
$(LUCI_POSTINST)
endef

define Package/luci-app-modemd-pro/postrm :=
$(LUCI_POSTRM)
endef

$(eval $(call BuildPackage,modemd-pro))
$(eval $(call BuildPackage,luci-app-modemd-pro))
