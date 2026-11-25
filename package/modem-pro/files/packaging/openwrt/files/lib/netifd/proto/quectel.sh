#!/bin/sh
# quectel.sh - netifd 的 "quectel" 协议：主机侧用 quectel-CM 拨 QMI 数据链路。
#
# 为什么不用 OpenWrt 自带的 qmi.sh（uqmi）：
#   · uqmi 要额外 opkg install，而且拨号失败时只说"连不上"，看不到模组原话；
#   · quectel-CM 是模组厂商自己的实现（随本包一起装，见 /usr/lib/modemd/quectel-CM），
#     QMAP / 多 PDN / IPv6 更完整、日志更详细。
# 详见 README「QMI 模式：接口 proto=dhcp（netifd 取地址）+ modemd 自己跑拨号器」。
#
# ⚠️ 这个协议**固定用 quectel-CM**（名字就叫 quectel），适合移远模组。
#    广和通 / 美格 / 芯讯通有三家自己的拨号器（fibocom-dial / meig-cm / simcom-cm），
#    netifd 这边拿不到厂商信息，挑不了 —— 那三家请走**自动组网**那条路：
#    接口 proto=dhcp + modemd 托管（module/kernel/hostdial.go 按厂商挑，见 README 同一节）。
#
# netifd 用 proto_run_command 托管 quectel-CM：进程在 → 接口 up；进程退出 → 接口 down
# 并由 netifd 重拨。IP / 默认路由 / DNS 由 quectel-CM 自己写进内核（QMI WDS），
# 这里只负责把它拉起、把数据网卡交给 netifd（proto_init_update <ifname> 1）。
#
# UCI 选项：device(控制口 /dev/cdc-wdmX，必填) / apn / username / password / auth /
#           pdptype(ip|ipv6|ipv4v6) / delay / mtu

[ -n "$INCLUDE_ONLY" ] || {
	. /lib/functions.sh
	. ../netifd-proto.sh
	init_proto "$@"
}

proto_quectel_init_config() {
	available=1
	no_device=1
	proto_config_add_string "device:device"
	proto_config_add_string "apn"
	proto_config_add_string "username"
	proto_config_add_string "password"
	proto_config_add_string "auth"
	proto_config_add_string "pdptype"
	proto_config_add_int "delay"
	proto_config_add_int "mtu"
	proto_config_add_defaults
}

proto_quectel_setup() {
	local interface="$1"
	local device apn username password auth pdptype delay mtu
	local $PROTO_DEFAULT_OPTIONS

	json_get_vars device apn username password auth pdptype delay mtu

	[ -n "$delay" ] && sleep "$delay"

	[ -n "$device" ] || {
		echo "protocol quectel: no control device (option device)"
		proto_notify_error "$interface" NO_DEVICE
		proto_set_available "$interface" 0
		return 1
	}
	device="$(readlink -f "$device")"
	[ -c "$device" ] || {
		echo "protocol quectel: $device is not a character device"
		proto_notify_error "$interface" NO_DEVICE
		proto_set_available "$interface" 0
		return 1
	}

	# 控制口 → 数据网卡：qmi_wwan 会把网卡挂在同一 USB 接口目录下
	local devname devpath ifname
	devname="$(basename "$device")"
	devpath="$(readlink -f "/sys/class/usbmisc/$devname/device/")"
	ifname="$(ls "$devpath/net" 2>/dev/null | head -n 1)"
	[ -n "$ifname" ] || {
		echo "protocol quectel: no netdev under $devpath"
		proto_notify_error "$interface" NO_IFACE
		proto_set_available "$interface" 0
		return 1
	}

	# pdptype → quectel-CM 的 -4 / -6（不写就按 IPv4）
	local ipv4opt="" ipv6opt=""
	case "$pdptype" in
		ip|ipv4|ipv4v6|"") ipv4opt="-4" ;;
	esac
	case "$pdptype" in
		ipv6|ipv4v6) ipv6opt="-6" ;;
	esac
	[ -n "$ipv4opt$ipv6opt" ] || ipv4opt="-4"
	[ -n "$auth" ] || auth="none"

	# 拨号器：优先用本包自带的（/usr/lib/modemd/quectel-CM，静态、不依赖设备 libc），
	# 没有就退回固件自带的 /usr/bin/quectel-CM（很多 5G 固件的 quectel-CM-5G 包会提供）
	local cmbin="/usr/lib/modemd/quectel-CM"
	[ -x "$cmbin" ] || cmbin="/usr/bin/quectel-CM"
	[ -x "$cmbin" ] || cmbin="quectel-CM"

	# 拨号交给 quectel-CM（-i 指定数据网卡；netifd 负责保活与重拨）
	eval "proto_run_command '$interface' '$cmbin' -i '$ifname' $ipv4opt $ipv6opt -s '$apn' '$username' '$password' '$auth'"

	# 等它把地址写上来（quectel-CM 自己配 IP，不是走 DHCP）；等不到也照样交给
	# netifd —— 接口先 up，quectel-CM 后面拿到地址时 NAT 立刻可用。
	local i=0
	while [ "$i" -lt 20 ]; do
		if ip -o addr show dev "$ifname" 2>/dev/null | grep -q " inet"; then
			break
		fi
		sleep 1
		i=$((i + 1))
	done
	ip link set dev "$ifname" up 2>/dev/null
	[ -n "$mtu" ] && ip link set dev "$ifname" mtu "$mtu" 2>/dev/null

	echo "protocol quectel: $ifname (${ipv4opt}${ipv6opt}) dialing via quectel-CM"

	proto_init_update "$ifname" 1
	proto_set_keep 1
	proto_send_update "$interface"
}

proto_quectel_teardown() {
	local interface="$1"

	# 杀掉 quectel-CM：它会自己把 QMI 数据呼叫挂断（SIGTERM 处理）
	proto_kill_command "$interface"

	proto_init_update "*" 0
	proto_send_update "$interface"
}

[ -n "$INCLUDE_ONLY" ] || {
	add_protocol quectel
}
