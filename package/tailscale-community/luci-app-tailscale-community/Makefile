include $(TOPDIR)/rules.mk

LUCI_TITLE:=LuCI support for Tailscale
LUCI_URL:=https://github.com/tokisaki-galaxy/luci-app-tailscale-community
PKG_DESCRIPTION:=Provides a LuCI Web management interface for Tailscale, allowing viewing status, configuring nodes and daemons.
PKG_MAINTAINER:=Tokisaki-Galaxy <moebest@outlook.jp>
LUCI_DEPENDS:=+tailscale +ip-full +luci-base
LUCI_PKGARCH:=all

PKG_SRC_VERSION:=26.221.31940~99d7dea
PKG_PO_VERSION:=26.221.31940~99d7dea

include $(TOPDIR)/feeds/luci/luci.mk

# call BuildPackage - OpenWrt buildroot signature
