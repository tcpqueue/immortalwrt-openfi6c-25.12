#!/bin/sh
# The physical switch only controls modem power or LEDs, never networking.
. "${OPENFI_LIB:-/usr/share/openfi/hardware.sh}"
mkdir -p "$RUNDIR"
case "${1:-}" in
    on|off) printf '%s\n' "$1" > "$RUNDIR/openfi-switch.state"; exit 0;;
esac
apply_hardware
