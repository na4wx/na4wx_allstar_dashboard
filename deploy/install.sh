#!/bin/sh
# Installs hamvoip-gui on a HamVoIP node. Run this ON THE PI, as root
# (or via sudo), from the directory containing the cross-compiled
# binary (see the Makefile's `pi`/`pi64` targets).
#
# Usage: sudo ./install.sh [path-to-binary]
# If no path is given, picks bin/hamvoip-gui-armv6 or bin/hamvoip-gui-arm64
# next to this script based on `uname -m`.

set -e

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/.." && pwd)

BINARY="$1"
if [ -z "$BINARY" ]; then
	case "$(uname -m)" in
		aarch64|arm64)
			BINARY="$REPO_ROOT/bin/hamvoip-gui-arm64"
			;;
		*)
			BINARY="$REPO_ROOT/bin/hamvoip-gui-armv6"
			;;
	esac
fi

if [ ! -f "$BINARY" ]; then
	echo "error: binary not found at $BINARY" >&2
	echo "Build it first with 'make pi' or 'make pi64' on your dev machine," >&2
	echo "copy it to this Pi, then re-run: sudo ./install.sh /path/to/binary" >&2
	exit 1
fi

if [ "$(id -u)" != "0" ]; then
	echo "error: this script must be run as root (sudo ./install.sh)" >&2
	exit 1
fi

echo "Installing $BINARY -> /usr/local/bin/hamvoip-gui"
install -m 0755 "$BINARY" /usr/local/bin/hamvoip-gui

echo "Installing systemd unit"
install -m 0644 "$SCRIPT_DIR/hamvoip-gui.service" /etc/systemd/system/hamvoip-gui.service

mkdir -p /etc/hamvoip-gui

systemctl daemon-reload
systemctl enable hamvoip-gui
systemctl restart hamvoip-gui

# Reports eth0/wlan0's own address specifically, rather than
# `hostname -I`'s first-interface-wins guess -- on a node with both
# connected, that guess is often wrong about which one an operator
# actually wants to visit.
iface_ip() {
	ip -4 -o addr show dev "$1" 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -n1
}

ETH0_IP=$(iface_ip eth0)
WLAN0_IP=$(iface_ip wlan0)

echo
echo "Installed and started. Visit this URL to finish setup:"
if [ -n "$ETH0_IP" ] && [ -n "$WLAN0_IP" ]; then
	echo "  http://$ETH0_IP:8088/setup   (Ethernet)"
	echo "  http://$WLAN0_IP:8088/setup   (WiFi)"
elif [ -n "$ETH0_IP" ]; then
	echo "  http://$ETH0_IP:8088/setup"
elif [ -n "$WLAN0_IP" ]; then
	echo "  http://$WLAN0_IP:8088/setup"
else
	echo "  http://<this-pi-ip>:8088/setup"
fi
