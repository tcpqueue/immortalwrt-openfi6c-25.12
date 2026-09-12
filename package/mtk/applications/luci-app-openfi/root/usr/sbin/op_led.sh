#!/bin/sh
. "${OPENFI_LIB:-/usr/share/openfi/hardware.sh}"
mkdir -p "$RUNDIR"
while :; do
    apply_hardware
    sleep 2
done
