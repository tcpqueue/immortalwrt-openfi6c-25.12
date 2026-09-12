# OpenFi 6C management

Hardware reference: https://github.com/OpenWrt01/immortalwrt-mt798x-24.10
Branch: openwrt-24.10-6.6
Commit: e31e61fc2d6396f290e18cc8fef6f90d29bace61
Profile: defconfig/mt7981-ax3000-openfi6c.config (revision 2, compatible with revision 1)

Ported to chasey-dev/immortalwrt-mt798x-rebase, branch 25.12.
Retains the previously tested CPU-only four-point fan control, inverted 25 kHz dual PWM,
5% minimum output, cold-start initialization and form validation fixes.
The physical switch controls modem power or LEDs. Network settings are not changed.
Legacy Lua LuCI dependencies are declared explicitly for 25.12.
Factory test tools and the hard-coded 24.10 opkg repository file are omitted.
