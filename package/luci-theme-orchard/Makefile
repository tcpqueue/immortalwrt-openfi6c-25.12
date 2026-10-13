include $(TOPDIR)/rules.mk

PKG_NAME:=luci-theme-orchard
PKG_VERSION:=0.3.0
PKG_RELEASE:=1
PKG_LICENSE:=Apache-2.0
LUCI_TITLE:=Orchard - a calm, native-style LuCI theme
LUCI_DEPENDS:=+luci-base +luci-mod-status +rpcd-mod-file
LUCI_PKGARCH:=all
LUCI_MAINTAINER:=Orchard
LUCI_URL:=
# Preserve modern CSS features when building with older csstidy versions.
LUCI_MINIFY_CSS:=0

define Package/luci-theme-orchard/postrm
#!/bin/sh
[ "$${1:-}" = upgrade ] && exit 0
[ -n "$${IPKG_INSTROOT}" ] || {
 if [ "$$(uci -q get luci.main.mediaurlbase)" = /luci-static/orchard ]; then
  previous="$$(cat /etc/orchard-theme.previous 2>/dev/null)"
  case "$$previous" in /luci-static/orchard|*..*|*[!a-zA-Z0-9_/.-]*) previous=/luci-static/bootstrap ;; /luci-static/*) ;; *) previous=/luci-static/bootstrap ;; esac
  [ -d "/www$$previous" ] && uci set "luci.main.mediaurlbase=$$previous"
 fi
 uci -q delete luci.themes.Orchard
 uci commit luci
 rm -f /tmp/luci-indexcache.*
}
endef

define Build/Prepare/luci-theme-orchard
	$(SED) 's/@ORCHARD_VERSION@/$(PKG_VERSION)-r$(PKG_RELEASE)/g' $(PKG_BUILD_DIR)/ucode/template/themes/orchard/*.ut
endef

include $(TOPDIR)/feeds/luci/luci.mk

# call BuildPackage - OpenWrt buildroot signature
