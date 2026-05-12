#!/bin/bash
set -e

# ============================================================================
# Proof-of-Concept: headless Wayland + XWayland + wayvnc for KubeDoom
#
# This script demonstrates how to run an SDL1.2/X11 application (psdoom)
# inside a wlroots-based Wayland compositor with remote VNC access,
# WITHOUT a physical GPU or Xvfb.
#
# Run this inside a Debian 12 / Ubuntu 22.04+ container after installing:
#   apt install sway xwayland wayvnc wlr-randr
#
# The script is designed to work as a non-root user (e.g. UID 65532).
# ============================================================================

RESOLUTION="640x480"
VNC_PORT="${VNC_PORT:-5900}"

# 1. Prepare the runtime directory required by every Wayland compositor.
export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/tmp/sway-runtime}"
mkdir -p "$XDG_RUNTIME_DIR"
chmod 700 "$XDG_RUNTIME_DIR"

# 2. Force headless / software rendering (no GPU, no DRM, no TTY needed).
export WLR_BACKENDS=headless
export WLR_RENDERER=pixman
export WLR_LIBINPUT_NO_DEVICES=1

# 3. Clean up stale sockets from previous runs.
rm -f "$XDG_RUNTIME_DIR"/wayland-*

# 4. Create a minimal sway config that sets the headless output resolution.
#    We also optionally auto-start an X11 app here via sway's 'exec'.
mkdir -p /tmp/sway-config
cat > /tmp/sway-config/config <<EOF
# No status bar, no background, minimal overhead
bar {
    mode invisible
}

# Force the virtual output to the resolution psdoom expects
output HEADLESS-1 resolution ${RESOLUTION} position 0,0

# You can uncomment the line below to auto-start psdoom once XWayland is ready.
# exec /usr/local/games/psdoom -warp -E1M1 -skill 1 -nomouse
EOF

echo "[PoC] Starting headless Sway compositor (resolution: ${RESOLUTION}) ..."
sway -c /tmp/sway-config/config &
SWAY_PID=$!

# 5. Wait for the Wayland socket to appear.
WAYLAND_SOCK=""
for i in $(seq 1 30); do
    WAYLAND_SOCK=$(ls "$XDG_RUNTIME_DIR"/wayland-* 2>/dev/null | head -n1 || true)
    if [ -S "$WAYLAND_SOCK" ]; then
        break
    fi
    sleep 0.5
done

if [ ! -S "$WAYLAND_SOCK" ]; then
    echo "[PoC] ERROR: Sway did not create a Wayland socket!"
    exit 1
fi

export WAYLAND_DISPLAY=$(basename "$WAYLAND_SOCK")
echo "[PoC] Wayland socket ready: $WAYLAND_DISPLAY"

# 6. Give XWayland a moment to spin up (it starts automatically on first X11 client).
sleep 2

# 7. Determine the DISPLAY that XWayland chose.
#    In a pristine container it is almost always :0, but we extract it from
#    sway's environment to be safe.
DISPLAY_VAL=$(tr '\0' '\n' < /proc/$SWAY_PID/environ | grep '^DISPLAY=' | cut -d= -f2 || true)
if [ -z "$DISPLAY_VAL" ]; then
    echo "[PoC] WARNING: Could not read DISPLAY from sway's environ; assuming :0"
    DISPLAY_VAL=":0"
fi
export DISPLAY="$DISPLAY_VAL"
echo "[PoC] XWayland DISPLAY is ${DISPLAY}"

# 8. Start wayvnc attached to our headless session.
echo "[PoC] Starting wayvnc on 0.0.0.0:${VNC_PORT} ..."
wayvnc 0.0.0.0 "${VNC_PORT}" &
WAYVNC_PID=$!

echo "[PoC] ============================================================"
echo "[PoC] VNC server should now be reachable on port ${VNC_PORT}"
echo "[PoC] Connect with: vncviewer localhost:${VNC_PORT}"
echo "[PoC] ============================================================"

# 9. Optionally start a test X11 app to prove the pipeline works.
#    If you have psdoom installed, replace the line below.
if command -v xclock >/dev/null 2>&1; then
    echo "[PoC] Launching test X11 app (xclock) ..."
    xclock &
elif [ -x /usr/local/games/psdoom ]; then
    echo "[PoC] Launching psdoom ..."
    /usr/local/games/psdoom -warp -E1M1 -skill 1 -nomouse &
else
    echo "[PoC] No X11 test app found; leaving compositor idle."
fi

# 10. Keep the script alive until sway exits.
wait $SWAY_PID
