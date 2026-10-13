#!/bin/sh
set -eu
[ "$(id -u)" = 0 ] || { echo 'Run as root.'; exit 1; }
[ -f /www/luci-static/orchard/cascade.css ] || { echo 'Install Orchard first.'; exit 1; }
previous=$(uci -q get luci.main.mediaurlbase || echo /luci-static/bootstrap)
if [ "$previous" != /luci-static/orchard ]; then
 umask 077
 printf '%s\n' "$previous" > /etc/orchard-theme.previous
fi
uci set luci.main.mediaurlbase=/luci-static/orchard
uci commit luci
rm -f /tmp/luci-indexcache.*
echo 'Orchard is active. Reload LuCI with Ctrl+F5.'
