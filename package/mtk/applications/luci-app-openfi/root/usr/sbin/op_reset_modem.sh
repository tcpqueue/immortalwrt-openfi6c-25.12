#!/bin/sh
# Drive the modem reset line to the configured level. The default 1 matches
# the DTS export value; unchecking the option writes 0.
SYSFS=${OPENFI_SYSFS:-/sys}
node="$SYSFS/class/gpio/lte_reset/value"

state=$(uci -q get openfi.reset.state)
[ "$state" = 0 ] || state=1

logger -t openfi-modem "reset line: $state"

[ -w "$node" ] || exit 1
[ "$(cat "$node" 2>/dev/null)" = "$state" ] || printf '%s\n' "$state" > "$node"
