#!/bin/bash
# Run this ON THE PI, as root, from inside the cloned repo (Arch Linux
# ARM — HamVoIP's OS). It makes sure the tools needed to build this
# project are installed, pulls the latest code if there is any, then
# builds natively and redeploys via deploy/install.sh.
#
# It always builds and deploys, including when the pull found nothing
# new. That matters for a first-time install: the user has just cloned,
# so there is nothing to fetch, and skipping the build in that case
# leaves them with no binary installed at all. Go's build cache makes a
# no-change rebuild cheap, so the cost of always building is small next
# to the cost of silently doing nothing.
#
# Usage: sudo ./install.sh

cat <<'EOF'
╔══════════════════════════════════════════════════════════╗
║                                                          ║
║     ███╗   ██╗ █████╗ ██╗  ██╗██╗    ██╗██╗  ██╗         ║
║     ████╗  ██║██╔══██╗██║  ██║██║    ██║╚██╗██╔╝         ║
║     ██╔██╗ ██║███████║███████║██║ █╗ ██║ ╚███╔╝          ║
║     ██║╚██╗██║██╔══██║╚════██║██║███╗██║ ██╔██╗          ║
║     ██║ ╚████║██║  ██║     ██║╚███╔███╔╝██╔╝ ██╗         ║
║     ╚═╝  ╚═══╝╚═╝  ╚═╝     ╚═╝ ╚══╝╚══╝ ╚═╝  ╚═╝         ║
║     ____________________________________________         ║
║            A L L S T A R   D A S H B O A R D             ║
║     ____________________________________________         ║
╚══════════════════════════════════════════════════════════╝
EOF

set -euo pipefail

MIN_GO_VERSION="1.22"
GO_TARBALL_VERSION="1.22.5"

log() { echo "==> $*"; }
err() { echo "error: $*" >&2; exit 1; }

[ "$(id -u)" = "0" ] || err "run as root: sudo ./install.sh"
command -v pacman >/dev/null 2>&1 || err "pacman not found — this script is for Arch Linux (HamVoIP's OS)"

REPO_ROOT=$(cd "$(dirname "$0")" && pwd)
cd "$REPO_ROOT"
[ -d .git ] || err "$REPO_ROOT is not a git checkout — clone the repo first"

# --- required tools -----------------------------------------------------

log "Checking required tools"

pacman_install() {
	# -Sy (not -Syu) so this only syncs enough to fetch the specific
	# missing package rather than performing a full system upgrade on a
	# live repeater node, which is a bigger action than "install the
	# tools this script needs" and not something to do unannounced. If
	# this fails because the local package database is too stale, run
	# `pacman -Syu` yourself first and re-run this script.
	pacman -Sy --noconfirm --needed "$@"
}

command -v git >/dev/null 2>&1 || { log "Installing git"; pacman_install git; }
command -v make >/dev/null 2>&1 || { log "Installing make"; pacman_install make; }
command -v curl >/dev/null 2>&1 || { log "Installing curl"; pacman_install curl; }
command -v tar >/dev/null 2>&1 || { log "Installing tar"; pacman_install tar; }

if ! command -v espeak-ng >/dev/null 2>&1 && ! command -v espeak >/dev/null 2>&1; then
	log "Installing espeak fallback (espeak-ng/espeak)"
	set +e
	pacman_install espeak-ng
	ESPEAK_INSTALL_STATUS=$?
	set -e
	if [ "$ESPEAK_INSTALL_STATUS" != "0" ]; then
		log "espeak-ng package not found; trying espeak"
		set +e
		pacman_install espeak
		ESPEAK_INSTALL_STATUS=$?
		set -e
		if [ "$ESPEAK_INSTALL_STATUS" != "0" ]; then
			log "warning: couldn't install espeak fallback package (tried espeak-ng and espeak). Text-to-speech fallback may be unavailable unless one is installed manually."
		fi
	fi
fi

# --- WiFi hotspot fallback (hostapd + dnsmasq) --------------------------
#
# Backs internal/wifi's automatic hotspot fallback for nodes running the
# dhcpcd+wpa_supplicant network stack. Skipped when NetworkManager is
# already active -- NetworkManager has its own built-in hotspot support
# (nmcli device wifi hotspot) and needs neither package; see
# internal/wifi.DetectBackend.
if systemctl is-active --quiet NetworkManager; then
	log "NetworkManager is active — skipping hostapd/dnsmasq (using its built-in hotspot support instead)"
else
	command -v hostapd >/dev/null 2>&1 || { log "Installing hostapd"; pacman_install hostapd; }
	command -v dnsmasq >/dev/null 2>&1 || { log "Installing dnsmasq"; pacman_install dnsmasq; }

	# Both ship their own default systemd units/config -- explicitly
	# disabled so they stay completely inert until internal/wifi's
	# watchdog starts them itself, directly, against its own generated
	# config at /etc/hamvoip-gui/hostapd-wlan0.conf and
	# /etc/hamvoip-gui/dnsmasq-wlan0.conf (see internal/wifi's package
	# doc) -- never via these units or their own default config files.
	systemctl disable --now hostapd >/dev/null 2>&1 || true
	systemctl disable --now dnsmasq >/dev/null 2>&1 || true

	# `command -v` above only confirms the binary is *present* on PATH,
	# not that it actually runs -- confirmed on a real Arch ARM node:
	# hostapd was already installed but dynamically linked against
	# libssl.so.1.1, which no longer existed after an OpenSSL upgrade
	# elsewhere on the system, so it failed immediately on every launch
	# with "error while loading shared libraries". `--needed` means
	# pacman_install alone wouldn't have reinstalled/fixed an
	# already-"installed" but broken package, and this would otherwise
	# fail completely silently until the watchdog actually needed the
	# hotspot.
	if ! hostapd -v >/dev/null 2>&1; then
		# Confirmed on a real Arch ARM node: the packaged hostapd (2.6-6,
		# with a local package database stale enough that a plain
		# reinstall couldn't fix it -- and a broader repo sync wasn't
		# something to risk on a live node) was dynamically linked
		# against libssl.so.1.1, which no longer existed once the
		# system's own OpenSSL moved on to 3.x.
		#
		# Rather than depend on the OS's own hostapd package at all,
		# build a minimal one directly from hostapd's own upstream
		# source. Verified against the real hostapd/Makefile (not
		# guessed): CONFIG_TLS defaults to "openssl" and links
		# -lcrypto/-lssl unconditionally whenever it's left unset --
		# even with EAP fully disabled, since that flag also selects the
		# core WPA crypto backend, not just the EAP/TLS bits.
		# CONFIG_TLS=internal is what actually avoids OpenSSL, using
		# hostapd's own bundled AES/SHA1/MD5 implementations instead --
		# all a plain WPA2-PSK access point (everything this feature
		# needs; no EAP/WPS/RADIUS) ever uses anyway. Pinned to the
		# latest tagged release (hostap_2_11) rather than a moving
		# development branch tip, for a reproducible build.
		log "hostapd is missing or fails to run — building a minimal one from source with no OpenSSL dependency"
		command -v gcc >/dev/null 2>&1 || { log "Installing base-devel"; pacman_install base-devel; }
		# The pkg-config *provider* package is named "pkgconf" on current
		# mainline Arch but still "pkg-config" on this Arch Linux ARM
		# image's own repos (confirmed on a real node: "pkgconf" doesn't
		# exist there at all) -- same "try the common case, fall back"
		# shape as the espeak-ng/espeak handling above.
		if ! command -v pkg-config >/dev/null 2>&1; then
			log "Installing pkgconf"
			pacman_install pkgconf 2>/dev/null || { log "pkgconf package not found; trying pkg-config"; pacman_install pkg-config; }
		fi
		pkg-config --exists libnl-3.0 2>/dev/null || { log "Installing libnl"; pacman_install libnl; }

		HOSTAPD_BUILD_DIR=$(mktemp -d)
		if git clone --quiet --depth 1 --branch hostap_2_11 https://w1.fi/hostap.git "$HOSTAPD_BUILD_DIR"; then
			cat >"$HOSTAPD_BUILD_DIR/hostapd/.config" <<-'HOSTAPD_CONFIG'
			CONFIG_DRIVER_NL80211=y
			CONFIG_LIBNL32=y
			CONFIG_TLS=internal
			HOSTAPD_CONFIG
			if make -C "$HOSTAPD_BUILD_DIR/hostapd" -j"$(nproc)" >"$HOSTAPD_BUILD_DIR/build.log" 2>&1; then
				install -Dm755 "$HOSTAPD_BUILD_DIR/hostapd/hostapd" /usr/local/bin/hostapd
			else
				log "warning: building hostapd from source failed — see $HOSTAPD_BUILD_DIR/build.log for details. The WiFi hotspot fallback will not work until this is fixed."
			fi
		else
			log "warning: could not fetch hostapd source (https://w1.fi/hostap.git) — check network access. The WiFi hotspot fallback will not work until this is fixed."
		fi

		if hostapd -v >/dev/null 2>&1; then
			log "hostapd now works (built from source, installed to /usr/local/bin/hostapd)"
			rm -rf "$HOSTAPD_BUILD_DIR"
		else
			log "warning: hostapd still does not run after attempting a from-source build — the WiFi hotspot fallback will not work until this is fixed. Build output kept at $HOSTAPD_BUILD_DIR/build.log for troubleshooting."
		fi
	fi
	if ! dnsmasq -v >/dev/null 2>&1; then
		log "warning: dnsmasq is installed but fails to run — try 'pacman -S dnsmasq', or a full 'pacman -Syu' if that's not enough, then re-run install.sh. The WiFi hotspot fallback will not work until this is fixed."
	fi

	# wpa_supplicant needs two global directives its own config file may
	# not have -- confirmed both missing on a real HamVoIP/Arch ARM node
	# (its wpa_supplicant@wlan0.service ran against a plain
	# "network={...}" block with nothing else at all):
	#   - ctrl_interface: without it, no control socket is ever created,
	#     so every wpa_cli command internal/wifi/wpa.go relies on fails
	#     with "Failed to connect to non-global ctrl_ifname: wlan0: No
	#     such file or directory".
	#   - update_config=1: without it, wpa_supplicant refuses to persist
	#     anything back to the config file as a deliberate safety
	#     measure, so "wpa_cli save_config" (the last step of Connect)
	#     fails with a bare "FAIL" even though the network switch itself
	#     already happened in memory.
	# Patches whichever config file the running (or configured-to-run)
	# wpa_supplicant@wlan0 instance actually uses -- not assumed to be
	# the "standard" wpa_supplicant-wlan0.conf name, since this image's
	# own copy is custom-named (wpa_supplicant_custom-wlan0.conf).
	WPA_CONF=""
	if WPA_PID=$(pgrep -f 'wpa_supplicant .*-iwlan0' | head -n1) && [ -n "$WPA_PID" ]; then
		WPA_CONF=$(tr '\0' '\n' <"/proc/$WPA_PID/cmdline" | sed -n 's/^-c//p' | head -n1)
	fi
	if [ -z "$WPA_CONF" ]; then
		WPA_CONF=$(systemctl cat wpa_supplicant@wlan0.service 2>/dev/null | sed -n 's/.*-c\([^ ]*wlan0\.conf\).*/\1/p' | head -n1)
	fi
	if [ -n "$WPA_CONF" ] && [ -f "$WPA_CONF" ]; then
		WPA_CONF_ADDITIONS=""
		grep -q '^ctrl_interface=' "$WPA_CONF" || WPA_CONF_ADDITIONS="${WPA_CONF_ADDITIONS}ctrl_interface=/run/wpa_supplicant\nctrl_interface_group=0\n"
		grep -q '^update_config=' "$WPA_CONF" || WPA_CONF_ADDITIONS="${WPA_CONF_ADDITIONS}update_config=1\n"
		if [ -z "$WPA_CONF_ADDITIONS" ]; then
			log "wpa_supplicant ctrl_interface/update_config already configured in $WPA_CONF"
		else
			log "Adding ctrl_interface/update_config to $WPA_CONF so the System page's Wireless scan/connect can talk to and persist changes via wpa_supplicant"
			WPA_CONF_PERMS=$(stat -c '%a' "$WPA_CONF" 2>/dev/null || echo 600)
			{
				printf '%b' "$WPA_CONF_ADDITIONS"
				printf '\n'
				cat "$WPA_CONF"
			} >"$WPA_CONF.tmp"
			chmod "$WPA_CONF_PERMS" "$WPA_CONF.tmp"
			mv "$WPA_CONF.tmp" "$WPA_CONF"
			systemctl restart wpa_supplicant@wlan0.service || log "warning: could not restart wpa_supplicant@wlan0.service after updating its config — restart it manually"
		fi
	else
		log "warning: could not determine which config file wpa_supplicant@wlan0.service uses — if the System page's Wireless scan/connect fails with a ctrl_ifname or save_config error, add 'ctrl_interface=/run/wpa_supplicant' and 'update_config=1' to its config file manually and restart the service"
	fi
fi

version_ge() { # version_ge A B => A >= B
	[ "$1" = "$2" ] && return 0
	[ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -n1)" = "$2" ]
}

go_version() {
	command -v go >/dev/null 2>&1 || return 1
	go version | sed -n 's/^go version go\([0-9.]*\).*/\1/p'
}

need_go_install=1
if v=$(go_version); then
	if version_ge "$v" "$MIN_GO_VERSION"; then
		log "go $v already installed (>= $MIN_GO_VERSION)"
		need_go_install=0
	else
		log "go $v is installed but too old (need >= $MIN_GO_VERSION)"
	fi
fi

if [ "$need_go_install" = "1" ]; then
	# pacman's go package on Arch Linux ARM has been observed to lag far
	# behind — go1.6 (from 2016) on the node this project was tested
	# against, nowhere near new enough for go.mod's "go 1.22" requirement.
	# Try pacman first in case it's current on your system; fall back to
	# installing the official upstream release directly if not.
	log "Trying pacman's go package"
	pacman_install go || true
	v=$(go_version || echo "0")
	if ! version_ge "$v" "$MIN_GO_VERSION"; then
		log "pacman's go ($v) is still too old — installing go $GO_TARBALL_VERSION from go.dev directly"
		case "$(uname -m)" in
			aarch64|arm64)
				GOARCH_TARBALL="arm64" ;;
			armv6l|armv7l|arm)
				GOARCH_TARBALL="armv6l" ;;
			*)
				err "unrecognized architecture $(uname -m) — install Go manually from https://go.dev/dl/" ;;
		esac
		TARBALL="go${GO_TARBALL_VERSION}.linux-${GOARCH_TARBALL}.tar.gz"
		TMP=$(mktemp -d)
		curl -fsSL -o "$TMP/$TARBALL" "https://go.dev/dl/$TARBALL"
		rm -rf /usr/local/go
		tar -C /usr/local -xzf "$TMP/$TARBALL"
		rm -rf "$TMP"
		ln -sf /usr/local/go/bin/go /usr/local/bin/go
		ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
		v=$(go_version) || err "go install from go.dev failed — check manually"
		log "Installed go $v to /usr/local/go"
	fi
fi

# --- Piper (text-to-speech, for the "Create from text" sound generator) ----
#
# Piper's current, actively maintained project (OHF-Voice/piper1-gpl) only
# ships as a pip wheel with no standalone binary, and only for 64-bit ARM
# (aarch64) — no package at all for 32-bit ARM. That's a worse fit here
# than its predecessor project's last release, both because this app's own
# internal/tts package already shells out to a standalone
# "piper --model ... --output_file ..." binary (confirmed against the OLD
# release specifically — the new pip package uses a different, incompatible
# "python3 -m piper -m ... -f ..." invocation) and because HamVoIP
# explicitly supports 32-bit ARM (Pi Zero/1/2) as a first-class target,
# which only the old release covers. That repo is archived (frozen since
# Oct 2025) so it won't see updates, but for an offline-only local tool
# with no network exposure that's an acceptable tradeoff — it's the only
# option that (a) works on 32-bit ARM and (b) matches this app's existing,
# already-tested invocation with no code changes.

log "Checking Piper (text-to-speech)"

PIPER_RELEASE_VERSION="2023.11.14-2"
PIPER_INSTALL_DIR="/usr/local/lib/piper"
PIPER_VOICES_DIR="/etc/hamvoip-gui/piper-voices"
PIPER_VOICE="en_US-lessac-medium"
PIPER_VOICE_PATH="en/en_US/lessac/medium/en_US-lessac-medium"

PIPER_ARCH=""
case "$(uname -m)" in
	aarch64|arm64)
		PIPER_ARCH="aarch64" ;;
	armv7l)
		PIPER_ARCH="armv7l" ;;
	armv6l|arm)
		log "Piper has no build for 32-bit armv6 (Pi Zero/1) — skipping Piper setup. The app will use espeak-ng as the text-to-speech fallback."
		;;
	*)
		log "Piper has no known build for $(uname -m) — skipping text-to-speech setup."
		;;
esac

if [ -n "$PIPER_ARCH" ]; then
	if [ -x "$PIPER_INSTALL_DIR/piper" ]; then
		log "Piper already installed at $PIPER_INSTALL_DIR/piper"
	else
		log "Installing Piper ($PIPER_ARCH)"
		TMP=$(mktemp -d)
		if curl -fsSL -o "$TMP/piper.tar.gz" "https://github.com/rhasspy/piper/releases/download/$PIPER_RELEASE_VERSION/piper_linux_${PIPER_ARCH}.tar.gz"; then
			# The tarball's own top-level directory is "piper/", which is
			# also PIPER_INSTALL_DIR's basename — extracting straight into
			# its parent lands it exactly where it needs to be, no rename.
			rm -rf "$PIPER_INSTALL_DIR"
			tar -C "$(dirname "$PIPER_INSTALL_DIR")" -xzf "$TMP/piper.tar.gz"
			# piper needs the .so files and espeak-ng-data/ that ship
			# alongside it in the same directory (it locates them via an
			# $ORIGIN-relative rpath, confirmed present in the binary) — so
			# this symlinks just the executable, not a copy, keeping it
			# next to everything it depends on.
			ln -sf "$PIPER_INSTALL_DIR/piper" /usr/local/bin/piper
			log "Installed Piper to $PIPER_INSTALL_DIR (symlinked to /usr/local/bin/piper)"
		else
			log "warning: couldn't download Piper (offline?) — skipping. Re-run this script with network access to pick it up, or set up text-to-speech manually later."
		fi
		rm -rf "$TMP"
	fi

	PIPER_READY=0
	if [ -x "$PIPER_INSTALL_DIR/piper" ]; then
		set +e
		PIPER_CHECK_OUTPUT=$("$PIPER_INSTALL_DIR/piper" --help 2>&1)
		PIPER_CHECK_STATUS=$?
		set -e
		if [ "$PIPER_CHECK_STATUS" = "0" ]; then
			PIPER_READY=1
		else
			log "warning: Piper is installed but cannot run on this system; skipping text-to-speech voice setup."
			log "Piper check output: ${PIPER_CHECK_OUTPUT//$'\n'/ | }"
			log "This is usually a glibc/libstdc++ version mismatch in older HamVoIP images."
			log "The app will fall back to espeak-ng for \"Create from text\" where available."
		fi
	fi

	if [ "$PIPER_READY" = "1" ]; then
		mkdir -p "$PIPER_VOICES_DIR"
		if [ -f "$PIPER_VOICES_DIR/$PIPER_VOICE.onnx" ]; then
			log "Voice $PIPER_VOICE already downloaded"
		else
			log "Downloading default voice: $PIPER_VOICE"
			# Staged as .tmp and only renamed into place once both files
			# succeed, so a connection drop mid-download can never leave a
			# half-downloaded .onnx file that a re-run would mistake for
			# "already downloaded".
			if curl -fsSL -o "$PIPER_VOICES_DIR/$PIPER_VOICE.onnx.tmp" "https://huggingface.co/rhasspy/piper-voices/resolve/main/$PIPER_VOICE_PATH.onnx" \
				&& curl -fsSL -o "$PIPER_VOICES_DIR/$PIPER_VOICE.onnx.json" "https://huggingface.co/rhasspy/piper-voices/resolve/main/$PIPER_VOICE_PATH.onnx.json"; then
				mv "$PIPER_VOICES_DIR/$PIPER_VOICE.onnx.tmp" "$PIPER_VOICES_DIR/$PIPER_VOICE.onnx"
				log "Downloaded voice $PIPER_VOICE to $PIPER_VOICES_DIR (more voices at https://huggingface.co/rhasspy/piper-voices)"
			else
				rm -f "$PIPER_VOICES_DIR/$PIPER_VOICE.onnx.tmp" "$PIPER_VOICES_DIR/$PIPER_VOICE.onnx.json"
				log "warning: couldn't download the default Piper voice (offline?) — the \"Create from text\" sound generator will show no voices until one is downloaded. Re-run this script with network access, or see https://huggingface.co/rhasspy/piper-voices"
			fi
		fi
	fi
fi

# --- SkywarnPlus (optional weather-alert automation) ------------------------
#
# A third-party, no-longer-maintained tool
# (https://github.com/Mason10198/SkywarnPlus) that announces National
# Weather Service alerts over the repeater. Unlike everything else this
# script sets up, this is entirely optional — not everyone wants it — so
# it's the one thing here that asks first rather than just doing it.
#
# Installing it from here (rather than a button in the running web app)
# matches its own upstream installer's own shape: the real swp-install is
# heavily interactive (several prompts, an offer to open config.yaml in
# nano) and was never meant to be triggered from a web server's HTTP
# handler. What's below reimplements only the non-interactive parts it
# actually needs (dependencies, download, cron); the app's own Automation
# tab configures whatever this installs (county codes, which node
# announces, feature on/off toggles) via deploy/sky_configure.py and
# SkywarnPlus's own SkyControl.py — see internal/skywarnplus's package doc.

SKYWARN_DIR="/usr/local/bin/SkywarnPlus"
SKYWARN_RELEASE_VERSION="v0.8.1"

if [ -x "$SKYWARN_DIR/SkywarnPlus.py" ]; then
	log "SkywarnPlus already installed at $SKYWARN_DIR"
elif [ ! -t 0 ]; then
	# No terminal attached (this script run non-interactively somehow) --
	# can't prompt, so skip rather than silently install something optional.
	log "Skipping SkywarnPlus prompt (no interactive terminal attached)"
else
	echo
	read -r -p "Install SkywarnPlus weather-alert automation? [y/N] " SKYWARN_ANSWER
	case "$SKYWARN_ANSWER" in
		[yY]|[yY][eE][sS])
			log "Installing SkywarnPlus dependencies"
			pacman_install ffmpeg unzip

			# HamVoIP's own Python is documented by SkywarnPlus's own README
			# as "very outdated" -- its own instructions bootstrap pip via
			# Python 3.5's get-pip.py and pin an old ruamel.yaml for it. Try
			# a normal pip3 install first in case your image differs, fall
			# back to that exact documented bootstrap if not.
			if command -v pip3 >/dev/null 2>&1 && pip3 install --quiet requests python-dateutil pydub ruamel.yaml >/dev/null 2>&1; then
				log "Installed Python dependencies via pip3"
			else
				log "Modern pip install didn't work -- bootstrapping pip for HamVoIP's Python (per SkywarnPlus's own README)"
				TMP=$(mktemp -d)
				if curl -fsSL -o "$TMP/get-pip.py" "https://bootstrap.pypa.io/pip/3.5/get-pip.py" && python3 "$TMP/get-pip.py"; then
					pip3 install requests python-dateutil pydub
					pip3 install ruamel.yaml==0.15.100
					log "Installed Python dependencies via bootstrapped pip"
				else
					log "warning: couldn't set up Python dependencies for SkywarnPlus -- install requests/python-dateutil/pydub/ruamel.yaml manually, see https://github.com/Mason10198/SkywarnPlus#installation"
				fi
				rm -rf "$TMP"
			fi

			log "Downloading SkywarnPlus $SKYWARN_RELEASE_VERSION"
			TMP=$(mktemp -d)
			if curl -fsSL -o "$TMP/SkywarnPlus.zip" "https://github.com/Mason10198/SkywarnPlus/releases/download/$SKYWARN_RELEASE_VERSION/SkywarnPlus.zip"; then
				rm -rf "$SKYWARN_DIR"
				unzip -q "$TMP/SkywarnPlus.zip" -d "$(dirname "$SKYWARN_DIR")"
				chmod +x "$SKYWARN_DIR"/*.py

				cp "$REPO_ROOT/deploy/sky_configure.py" "$SKYWARN_DIR/"
				chmod +x "$SKYWARN_DIR/sky_configure.py"

				PYTHON3_BIN=$(command -v python3 || echo /usr/bin/python3)
				echo "* * * * * root $PYTHON3_BIN $SKYWARN_DIR/SkywarnPlus.py" > /etc/cron.d/SkywarnPlus
				log "Installed SkywarnPlus to $SKYWARN_DIR, scheduled via /etc/cron.d/SkywarnPlus (every 60s)"
				log "Finish setup on the node's Automation tab: pick your county codes and register this node."
			else
				log "warning: couldn't download SkywarnPlus (offline?) -- re-run this script with network access to finish installing it."
			fi
			rm -rf "$TMP"
			;;
		*)
			log "Skipping SkywarnPlus"
			;;
	esac
fi

# --- pull latest ---------------------------------------------------------

log "Fetching latest from git"
git fetch origin

BRANCH=$(git rev-parse --abbrev-ref HEAD)
LOCAL=$(git rev-parse @)
REMOTE=$(git rev-parse "@{u}" 2>/dev/null) || err "branch $BRANCH has no upstream — check out a branch that tracks origin"

if [ "$LOCAL" = "$REMOTE" ]; then
	# Nothing to pull, but still build and deploy below. A first-time
	# install is exactly this case — the user just cloned, so there is
	# by definition nothing new to fetch, and an earlier version of this
	# script exited here and left them with no binary installed at all.
	log "Already up to date ($LOCAL) — building and deploying the current checkout"
else
	# Only require a clean tree when there is actually something to
	# merge. Someone who tweaked a file locally and just wants to
	# rebuild shouldn't be blocked by a pull they don't need.
	if [ -n "$(git status --porcelain)" ]; then
		err "working tree has uncommitted changes and origin/$BRANCH has new commits — commit or stash, then re-run"
	fi
	log "Updating $BRANCH: $LOCAL -> $REMOTE"
	git pull --ff-only origin "$BRANCH"
fi

# --- build and deploy -----------------------------------------------------

log "Building"
make build

log "Deploying"
./deploy/install.sh "$REPO_ROOT/bin/hamvoip-gui"

log "Done"

echo
echo "This script has installed the HamVoIP GUI and its dependencies (already running via systemd)."
echo "If you installed SkywarnPlus, finish its setup on the node's SkywarnPlus tab."
echo
echo
if [ ! -d "$SKYWARN_DIR" ]; then
    echo "You can re-run this script later to install SkywarnPlus."
fi
echo "You can re-run this script at any time to update the application to the latest version from git."
echo
echo "Visit http://<node-ip>:8088 in a browser to access the GUI."
echo
echo
echo
