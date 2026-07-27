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
