#!/bin/sh
set -eu
[ "$(id -u)" = 0 ] || { echo 'Run as root.'; exit 1; }
previous=$(cat /etc/orchard-theme.previous 2>/dev/null || echo /luci-static/bootstrap)
case "$previous" in
 /luci-static/orchard|*..*|*[!a-zA-Z0-9_/.-]*) previous=/luci-static/bootstrap ;;
 /luci-static/*) ;;
 *) previous=/luci-static/bootstrap ;;
esac
[ -d "/www$previous" ] || { echo "Previous theme assets are missing: $previous"; exit 1; }
uci set "luci.main.mediaurlbase=$previous"
uci commit luci
rm -f /tmp/luci-indexcache.*
echo "Restored: $previous"
