#!/bin/bash
set -e

# =============================================================================
# KubeDoom entrypoint — launches the graphics/VNC stack then execs kubedoom.
# =============================================================================

BACKEND="${KUBEDOOM_DISPLAY_BACKEND:-wayland}"
RESOLUTION="640x480"
VNC_PORT="5900"

# Runtime environment required by Wayland compositors and X11 apps.
export XDG_RUNTIME_DIR=/tmp/sway-runtime
export HOME=/home/kubedoom

mkdir -p "$XDG_RUNTIME_DIR"
chmod 700 "$XDG_RUNTIME_DIR"

# Keep track of background PIDs so we can clean up on premature exit.
BG_PIDS=""

cleanup() {
    if [ -n "$BG_PIDS" ]; then
        # shellcheck disable=SC2086
        kill $BG_PIDS 2>/dev/null || true
    fi
}
trap cleanup EXIT

if [ "$BACKEND" = "wayland" ]; then
    echo "[entrypoint] Using Wayland backend (sway + XWayland + wayvnc)"

    export WLR_BACKENDS=headless
    export WLR_RENDERER=pixman
    export WLR_LIBINPUT_NO_DEVICES=1

    # Remove stale Wayland sockets from previous runs.
    rm -f "$XDG_RUNTIME_DIR"/wayland-*

    # Minimal sway config: hide bar and force headless output resolution.
    mkdir -p /tmp/sway-config
    cat > /tmp/sway-config/config <<EOF
bar {
    mode invisible
}

output HEADLESS-1 resolution ${RESOLUTION} position 0,0
EOF

    echo "[entrypoint] Starting headless Sway compositor ..."
    sway -c /tmp/sway-config/config &
    SWAY_PID=$!
    BG_PIDS="$SWAY_PID"

    # Wait for the Wayland socket to appear (up to 15 s).
    WAYLAND_SOCK=""
    for i in $(seq 1 30); do
        WAYLAND_SOCK=$(ls "$XDG_RUNTIME_DIR"/wayland-* 2>/dev/null | head -n1 || true)
        if [ -S "$WAYLAND_SOCK" ]; then
            break
        fi
        sleep 0.5
    done

    if [ ! -S "$WAYLAND_SOCK" ]; then
        echo "[entrypoint] ERROR: Sway did not create a Wayland socket!"
        exit 1
    fi

    export WAYLAND_DISPLAY=$(basename "$WAYLAND_SOCK")
    echo "[entrypoint] Wayland socket ready: $WAYLAND_DISPLAY"

    # Give XWayland time to start (it auto-launches on first X11 client).
    sleep 2

    # Detect DISPLAY that XWayland exported into sway's environment.
    DISPLAY_VAL=$(tr '\0' '\n' < /proc/$SWAY_PID/environ | grep '^DISPLAY=' | cut -d= -f2 || true)
    if [ -z "$DISPLAY_VAL" ]; then
        echo "[entrypoint] WARNING: Could not read DISPLAY from sway's environ; assuming :0"
        DISPLAY_VAL=":0"
    fi
    export DISPLAY="$DISPLAY_VAL"
    echo "[entrypoint] XWayland DISPLAY is ${DISPLAY}"

    # Start wayvnc.
    echo "[entrypoint] Starting wayvnc on 0.0.0.0:${VNC_PORT} ..."
    wayvnc 0.0.0.0 "${VNC_PORT}" &
    BG_PIDS="$BG_PIDS $!"

    # Start psdoom.
    echo "[entrypoint] Starting psdoom ..."
    DISPLAY=$DISPLAY /usr/local/games/psdoom -warp -E1M1 -skill 1 -nomouse &
    BG_PIDS="$BG_PIDS $!"

elif [ "$BACKEND" = "x11" ]; then
    echo "[entrypoint] Using X11 backend (Xvfb + x11vnc)"

    echo "[entrypoint] Starting Xvfb ..."
    Xvfb :99 -ac -screen 0 "${RESOLUTION}x24" &
    BG_PIDS="$!"
    sleep 2

    echo "[entrypoint] Starting x11vnc on port ${VNC_PORT} ..."
    x11vnc -geometry "${RESOLUTION}" -forever -usepw -display :99 -rfbport "${VNC_PORT}" &
    BG_PIDS="$BG_PIDS $!"

    echo "[entrypoint] Starting psdoom ..."
    DISPLAY=:99 /usr/local/games/psdoom -warp -E1M1 -skill 1 -nomouse &
    BG_PIDS="$BG_PIDS $!"

else
    echo "[entrypoint] ERROR: Unknown KUBEDOOM_DISPLAY_BACKEND '${BACKEND}'. Use 'wayland' or 'x11'."
    exit 1
fi

# Remove the EXIT trap so that normal exec does not kill the children we just
# launched.  After exec the Go binary becomes PID 1 and handles signals itself.
trap - EXIT

# Replace this shell with the kubedoom binary.
exec /usr/bin/kubedoom "$@"
