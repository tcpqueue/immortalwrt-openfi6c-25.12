#!/bin/sh
SYSFS=${OPENFI_SYSFS:-/sys}
RUNDIR=${OPENFI_RUNDIR:-/var/run}

switch_state() {
    # gpio-button-hotplug sends the initial EV_SW state and later transitions.
    state=$(cat "$RUNDIR/openfi-switch.state" 2>/dev/null)
    case "$state" in on|off) echo "$state"; return;; esac
    # Fallback for service installation after the initial event. DTS label is mode.
    awk '/\|[[:space:]]*mode[[:space:]]*\)/ {
        for(i=1;i<=NF;i++) { if($i=="hi") {print "off"; exit}; if($i=="lo") {print "on"; exit} }
    }' "$SYSFS/kernel/debug/gpio" 2>/dev/null
}

set_modem_power() {
    node="$SYSFS/class/gpio/lte_power/value"
    [ -w "$node" ] || return 1
    # Preserve OpenFi's existing board-specific power GPIO convention.
    [ "$1" = 1 ] && value=0 || value=1
    [ "$(cat "$node" 2>/dev/null)" = "$value" ] || printf '%s\n' "$value" > "$node"
}

set_leds() {
    for led_path in "$SYSFS"/class/leds/*; do
        [ -d "$led_path" ] || continue
        led_name=${led_path##*/}
        case "$led_name" in internet|modem|system|wifi|*:status|*:wlan) ;; *) continue;; esac
        saved="$RUNDIR/openfi-led-$led_name"
        if [ "$1" = 0 ]; then
            if [ ! -f "$saved" ]; then
                sed -n 's/.*\[\([^]]*\)\].*/\1/p' "$led_path/trigger" > "$saved"
                cat "$led_path/brightness" > "$saved.brightness"
            fi
            echo none > "$led_path/trigger"
            echo 0 > "$led_path/brightness"
        elif [ -f "$saved" ]; then
            old_trigger=$(cat "$saved")
            old_brightness=$(cat "$saved.brightness" 2>/dev/null)
            case "$old_brightness" in ''|*[!0-9]*) old_brightness=1;; esac
            [ "$old_trigger" != none ] || old_brightness=1
            printf '%s\n' "$old_brightness" > "$led_path/brightness"
            [ -z "$old_trigger" ] || printf '%s\n' "$old_trigger" > "$led_path/trigger"
            rm -f "$saved" "$saved.brightness"
        elif [ "$(cat "$RUNDIR/openfi-leds.enabled" 2>/dev/null)" != 1 ]; then
            current_trigger=$(sed -n 's/.*\[\([^]]*\)\].*/\1/p' "$led_path/trigger")
            [ "$current_trigger" != none ] || echo 1 > "$led_path/brightness"
        fi
    done
    printf '%s\n' "$1" > "$RUNDIR/openfi-leds.enabled"
}

apply_hardware() {
    func=$(uci -q get openfi.switch.func)
    power=$(uci -q get openfi.switch.power)
    leds=$(uci -q get openfi.switch.led)
    [ "$power" = 0 ] || power=1
    [ "$leds" = 0 ] || leds=1
    physical=$(switch_state)
    case "$func:$physical" in
        2:on) power=1;; 2:off) power=0;;
        3:on) leds=1;; 3:off) leds=0;;
    esac
    set_modem_power "$power"
    set_leds "$leds"
}
