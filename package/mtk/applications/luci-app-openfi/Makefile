#
# Copyright (C) Hua Shao <nossiac@163.com>
#
# This is free software, licensed under the Apache License, Version 2.0 .
#

include $(TOPDIR)/rules.mk
include $(INCLUDE_DIR)/package.mk

LUCI_TITLE:=OpenFi Proprietary Configuration.
LUCI_DEPENDS:=+luci-compat +luci-lib-jsonc
LUCI_PKGARCH:=all
PKG_VERSION:=2026.09.12
PKG_RELEASE:=2

include $(TOPDIR)/feeds/luci/luci.mk

# call BuildPackage - OpenWrt buildroot signature
