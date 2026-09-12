#!/bin/sh
# OpenFi 6C uses inverted 25 kHz PWM: 0 ns is full output.
SYSFS=${OPENFI_SYSFS:-/sys}
RUNDIR=${OPENFI_RUNDIR:-/var/run}
PWM="$SYSFS/class/pwm/pwmchip0"

uint() {
    case "$1" in ''|*[!0-9]*) return 1;; esac
    [ "${#1}" -le 3 ] && [ "$1" -ge "$2" ] && [ "$1" -le "$3" ]
}
option() {
    value=$(uci -q get "openfi.fan.$1")
    if uint "$value" "$3" "$4"; then awk -v n="$value" 'BEGIN {printf "%d\n",n}'; else echo "$2"; fi
}
load_config() {
    mode=$(uci -q get openfi.fan.mode)
    case "$mode" in auto|manual) ;; *) mode=auto;; esac
    cpu_low=$(option cpu_temp_low 55 20 95)
    cpu_high=$(option cpu_temp_high 65 25 100)
    [ "$cpu_high" -ge "$((cpu_low + 6))" ] || { cpu_low=55; cpu_high=65; }
    period=$(option period 5 2 30)
    # Preserve approximately the old middle/slow duty as the starting floor.
    legacy=$(uci -q get openfi.fan.level)
    case "$legacy" in 0) floor=43;; 1) floor=55;; 2) floor=100;; *) floor=5;; esac
    min_speed=$(option min_speed "$floor" 5 100)
    manual_speed=$(option manual_speed 55 0 100)
    emergency_temp=$(option emergency_temp 85 60 100)
    fan_stop=$(option fan_stop 1 0 1)
    # Expand the old two-threshold configuration when upgrading.
    t1=$(option temp1 "$cpu_low" 20 95)
    t2=$(option temp2 "$((cpu_low + (cpu_high - cpu_low) / 3))" 20 95)
    t3=$(option temp3 "$((cpu_low + 2 * (cpu_high - cpu_low) / 3))" 20 95)
    t4=$(option temp4 "$cpu_high" 20 95)
    s1=$(option speed1 "$min_speed" 0 100)
    s2=$(option speed2 "$((min_speed + (100 - min_speed) / 3))" 0 100)
    s3=$(option speed3 "$((min_speed + 2 * (100 - min_speed) / 3))" 0 100)
    s4=$(option speed4 100 0 100)
    if ! valid_curve; then
        t1=55; t2=58; t3=61; t4=65
        s1=$min_speed; s2=$((min_speed + (100 - min_speed) / 3))
        s3=$((min_speed + 2 * (100 - min_speed) / 3)); s4=100
        emergency_temp=85
        logger -t openfi-fan "Invalid curve; using the default CPU fan curve"
    fi
}
valid_curve() {
    [ "$t2" -ge "$((t1 + 2))" ] && [ "$t3" -ge "$((t2 + 2))" ] &&
    [ "$t4" -ge "$((t3 + 2))" ] && [ "$emergency_temp" -ge "$((t4 + 2))" ] &&
    [ "$s1" -ge "$min_speed" ] && [ "$s2" -ge "$s1" ] &&
    [ "$s3" -ge "$s2" ] && [ "$s4" -ge "$s3" ]
}
curve() {
    if [ "$1" -le "$t1" ]; then echo "$s1"; return; fi
    if [ "$1" -ge "$t4" ]; then echo "$s4"; return; fi
    left_t=$t1; left_s=$s1
    for point in "$t2:$s2" "$t3:$s3" "$t4:$s4"; do
        right_t=${point%:*}; right_s=${point#*:}
        if [ "$1" -le "$right_t" ]; then
            echo "$((left_s + (right_s - left_s) * ($1 - left_t) / (right_t - left_t)))"
            return
        fi
        left_t=$right_t; left_s=$right_s
    done
}
read_temperatures() {
    cpu=$(awk '$1 ~ /^[0-9]+$/ && $1 <= 150000 {printf "%d\n", $1/1000}' "$SYSFS/class/thermal/thermal_zone0/temp" 2>/dev/null)
}
choose_speed() {
    reason=auto
    target=0
    if ! uint "$cpu" 0 150; then target=100; reason=sensor_fault; return; fi
    if [ "$cpu" -ge "$emergency_temp" ]; then target=100; reason=overheat; return; fi
    if [ "$mode" = manual ]; then
        target=$manual_speed
        [ "$target" -eq 0 ] || [ "$target" -ge "$min_speed" ] || target=$min_speed
        reason=manual
    else
        target=$(curve "$cpu")
        if [ "$fan_stop" -eq 1 ]; then
            if [ "$current" -eq 0 ] && [ "$cpu" -lt "$t1" ]; then target=0
            elif [ "$current" -gt 0 ] && [ "$cpu" -le "$((t1 - 2))" ]; then target=0
            fi
        fi
    fi
}
next_output() {
    next=$target
    # Rise immediately; reduce at most 10 percentage points per sample.
    if [ "$next" -lt "$((current - 10))" ]; then next=$((current - 10)); fi
    if [ "$next" -gt 0 ] && [ "$next" -lt "$min_speed" ]; then
        if [ "$target" -eq 0 ]; then next=0; else next=$min_speed; fi
    fi
}
init_pwm() {
    for channel in 0 1; do
        path="$PWM/pwm$channel"
        if [ ! -d "$path" ]; then
            printf '%s\n' "$channel" > "$PWM/export" || return 1
        fi
        [ -w "$path/duty_cycle" ] || return 1
        # A newly exported PWM has period=0. Linux rejects all duty writes
        # until a valid period is set, even when writing a duty of zero.
        old_period=$(cat "$path/period" 2>/dev/null)
        if [ "${old_period:-0}" -gt 0 ]; then
            echo 0 > "$path/duty_cycle" || return 1
        fi
        echo 40000 > "$path/period" || return 1
        echo 0 > "$path/duty_cycle" || return 1
        echo 1 > "$path/enable" || return 1
    done
}
write_speed() {
    duty=$((40000 * (100 - $1) / 100))
    failed=0
    for channel in 0 1; do
        printf '%s\n' "$duty" > "$PWM/pwm$channel/duty_cycle" || failed=1
    done
    [ "$failed" -eq 0 ]
}
status() {
    now=$(date +%s)
    printf '{"updated":%s,"mode":"%s","reason":"%s","output":%s,"target":%s,"cpu":%s,"period":%s}\n'         "$now" "$mode" "$reason" "$current" "$target" "${cpu:-null}" "$period"         > "$RUNDIR/openfi-fan.json.tmp"
    mv "$RUNDIR/openfi-fan.json.tmp" "$RUNDIR/openfi-fan.json"
}
shutdown() {
    trap - EXIT INT TERM
    if ! write_speed 100; then reason=pwm_fault; fi
    current=100; target=100
    [ "$reason" = pwm_fault ] || reason=stopped
    status
    exit
}
main() {
    reload_pending=0
    trap 'reload_pending=1' HUP
    load_config
    cpu=; current=100; target=100
    trap shutdown EXIT
    trap 'exit 0' INT TERM
    read_temperatures
    while ! init_pwm; do
        reason=pwm_fault; status
        logger -t openfi-fan "PWM initialization failed; retrying in 5 seconds"
        sleep 5
        read_temperatures
    done
    # Full output for one second reliably starts a stopped fan.
    sleep 1
    while :; do
        if [ "$reload_pending" -eq 1 ]; then
            reload_pending=0
            load_config
        fi
        read_temperatures
        choose_speed
        if [ "$target" -gt 0 ] && [ "$current" -eq 0 ]; then
            write_speed 100 || exit 1
            current=100; reason=starting; status
            sleep 1
        fi
        next_output
        if ! write_speed "$next"; then
            reason=pwm_fault; status
            logger -t openfi-fan "PWM write failed"
            exit 1
        fi
        current=$next
        status
        sleep "$period"
    done
}
[ "${1:-}" = --library ] || main "$@"
