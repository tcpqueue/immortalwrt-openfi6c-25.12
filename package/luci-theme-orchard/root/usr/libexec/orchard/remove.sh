#!/bin/sh
set -eu
[ "$(id -u)" = 0 ] || { echo 'Run as root.'; exit 1; }
if command -v opkg >/dev/null && opkg status luci-theme-orchard 2>/dev/null | grep -q '^Status:.* installed$'; then
 exec opkg remove luci-theme-orchard
fi
if command -v apk >/dev/null && apk info -e luci-theme-orchard >/dev/null 2>&1; then
 exec apk del luci-theme-orchard
fi
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [ "$(uci -q get luci.main.mediaurlbase || true)" = /luci-static/orchard ]; then
 sh "$script_dir/restore.sh"
fi
uci -q delete luci.themes.Orchard || true
uci commit luci
rm -rf /www/luci-static/orchard /www/luci-static/resources/view/orchard /usr/share/ucode/luci/template/themes/orchard
rm -rf /usr/share/doc/luci-theme-orchard
rm -f /www/luci-static/resources/menu-orchard.js /www/luci-static/resources/orchard-cpu.js /usr/share/luci/menu.d/luci-theme-orchard.json /usr/share/rpcd/acl.d/luci-theme-orchard.json /etc/orchard-theme.previous /tmp/luci-indexcache.*
/etc/init.d/rpcd reload
rm -rf /usr/libexec/orchard
echo 'Orchard removed.'
