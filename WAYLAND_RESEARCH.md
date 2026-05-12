# Wayland Support Research for KubeDoom

## Executive Summary

**Recommended path: Option B** — Keep the existing `psdoom`/SDL1.2 binary unchanged, drop the ancient `Xvfb`+`x11vnc` stack, and run it inside a minimal **wlroots-based Wayland compositor** (`sway` or `labwc`) with **XWayland** and **wayvnc**.

This is the only option that:
* Requires **zero changes** to the `psdoom` C source code.
* Uses packages that are **officially available in Debian 12** (`sway`, `xwayland`, `wayvnc`).
* Works in a **fully headless, GPU-less container** using software rendering (`pixman`).
* Can run as a **non-root user** (UID 65532) with only a writable `XDG_RUNTIME_DIR`.

Option A (porting `psdoom` to SDL2) would require rewriting thousands of lines of video, audio, and text-screen code. Option C (replacing the engine) would lose the custom Kubernetes-socket integration that `kubedoom.go` relies on.

---

## Option A: Patch psdoom to compile against SDL2

### Findings

The `dockerdoom/trunk/` tree is a **hard SDL 1.2 codebase**:

* `configure.in` explicitly searches for SDL ≥ 1.1.3 (`AM_PATH_SDL(1.1.3)`) using `sdl-config`.
* `src/i_video.c` uses the classic SDL 1.2 API:
  * `SDL_SetVideoMode`, `SDL_Surface`, `SDL_Flip`
  * `SDL_WM_SetCaption`, `SDL_WM_GrabInput`
  * `SDL_GetAppState`, `SDL_ACTIVEEVENT`, `SDL_RESIZABLE`
  * `SDL_EnableUNICODE`, `SDL_EnableKeyRepeat`
  * `SDL_ListModes`
* `textscreen/txt_sdl.c` (the setup UI font renderer) is also deeply tied to `SDL_Surface`/`SDL_SetColors`/`SDL_UpdateRect`.
* Sound (`i_sdlsound.c`) and music (`i_sdlmusic.c`) link against `SDL_mixer` and `SDL_net` **1.2**.

There are a few `#if SDL_VERSION_ATLEAST(1, 3, 0)` guards (SDL 1.3 became SDL2), but they only paper over tiny API differences (e.g. `SDL_GetRelativeMouseState` signatures). They do **not** abstract away the fundamental changes between SDL 1.2 and SDL2 (no more `SDL_Surface` screen buffer, no more `SDL_Flip`, event loop changes, etc.).

### Verdict

**Impractical.** A full SDL2 migration would mean rewriting `i_video.c`, `txt_sdl.c`, the build system (`configure.in` → `configure.ac` with `PKG_CHECK_MODULES([SDL2], …)`), and re-testing all rendering paths. This is weeks of work for a single container feature.

---

## Option B: XWayland + minimal Wayland compositor + wayvnc

### Findings

Because `psdoom` only speaks X11 (via SDL 1.2’s X11 backend), we can keep the binary exactly as-is and give it an **X11 server** inside a **Wayland world**. XWayland does precisely this: it is a full X11 server that runs as a Wayland client.

The container stack becomes:

```
[VNC client] → wayvnc → sway/labwc (Wayland compositor) → XWayland → psdoom
```

**Why this works in a container:**

1. **Headless backend:** wlroots-based compositors support a `headless` backend. Setting `WLR_BACKENDS=headless` tells the compositor to create a virtual output with no physical display.
2. **Software rendering:** Setting `WLR_RENDERER=pixman` forces the compositor to render entirely in software (no `/dev/dri` or GPU required).
3. **No input devices needed:** Setting `WLR_LIBINPUT_NO_DEVICES=1` suppresses libinput errors when there are no keyboards/mice.
4. **VNC:** `wayvnc` attaches to a running wlroots compositor using the `wlr-screencopy` protocol and exposes an RFB/VNC stream.
5. **XWayland:** When an X11 client (like `psdoom`) starts, the compositor automatically launches `XWayland` and sets the `DISPLAY` environment variable.

**Debian 12 package availability:**

| Package | Debian 12? | Purpose |
|---------|-----------|---------|
| `sway` | ✅ Yes | wlroots-based compositor (well-documented for headless) |
| `labwc` | ✅ Yes | Lighter wlroots-based stacking compositor (also works headless) |
| `xwayland` | ✅ Yes | X11 server for Wayland |
| `wayvnc` | ✅ Yes | VNC server for wlroots compositors |
| `wlr-randr` | ✅ Yes | Utility to configure headless output resolution at runtime |

**Proven in the wild:**

* The `bbusse/swayvnc` container image runs `sway` + `wayvnc` headlessly for remote application streaming.
* The Arch Wiki documents headless sway + wayvnc exactly:  
  `WLR_BACKENDS=headless WLR_LIBINPUT_NO_DEVICES=1 sway` → `WAYLAND_DISPLAY=wayland-1 wayvnc`
* `labwc` users on Raspberry Pi OS (Debian 12) run `WLR_BACKENDS=headless labwc` + `wayvnc` successfully.

### Non-root / UID 65532 considerations

* `sway`/`labwc` and `wayvnc` do **not** need root. They only need:
  * `XDG_RUNTIME_DIR` set to a directory owned by the user (mode `0700`), e.g. `/tmp/sway-runtime`.
  * `WLR_BACKENDS=headless` so they never try to open `/dev/dri` or take a TTY.
* `XWayland` is spawned by the compositor and runs as the same UID.
* No `setcap`, `seatd` daemon, or `systemd-logind` is required in a headless container.

### Tiny integration change needed in kubedoom.go

`kubedoom.go` currently hard-codes `DISPLAY=:99` when launching `psdoom`. Under XWayland the display number is dynamic (usually `:0` in a fresh container, but not guaranteed).

**Minimal fix:** Instead of hard-coding `:99`, read the `DISPLAY` variable that the compositor exports, or launch `psdoom` via the compositor’s IPC (`swaymsg exec …`). This is a one-line-ish change compared to rewriting a Doom engine.

---

## Option C: Replace psdoom with a modern SDL2 port

### Findings

Modern SDL2-based Doom engines are readily available in Debian 12:

| Port | Debian 12 package | SDL2? |
|------|-------------------|-------|
| Chocolate Doom | `chocolate-doom` | ✅ Yes |
| Crispy Doom | `crispy-doom` | ✅ Yes |
| DSDA-Doom | `dsda-doom` | ✅ Yes |
| PrBoom+ | `prboom-plus` | ✅ Yes |

These ports run natively on Wayland (via SDL2’s `wayland` backend) or on XWayland.

### The blocker

`kubedoom.go` relies on a **custom Unix socket protocol** (`/dockerdoom.socket`) that is hard-coded into `psdoom`:

* `psdoom` connects to `/dockerdoom.socket`.
* It sends `list` to get pod/namespace names (hashed).
* It sends `kill <hash>` when the player shoots a monster.
* `kubedoom.go` listens on that socket and translates the hashes into `kubectl delete` commands.

None of the modern engines above implement this socket protocol. Porting it would require:
1. Patching the new engine’s monster-spawning and death logic.
2. Re-implementing the custom text-screen UI that shows entity names.

### Verdict

**Not viable** without a major feature-reimplementation effort. You would lose the “Kubernetes entities as Doom monsters” core feature of the project.

---

## Recommended Implementation Details

### Package list (Debian 12 / Ubuntu 22.04+)

```bash
apt-get update && apt-get install -y --no-install-recommends \
    sway          \
    xwayland      \
    wayvnc        \
    wlr-randr     \
    libpixman-1-0 \
    libwayland-client0 \
    libxkbcommon0 \
    fonts-dejavu  # or any font package, avoids sway font warnings
```

> **Note:** `labwc` can be used instead of `sway` if you want a lighter stacking (floating-window) compositor rather than a tiling one. Both are wlroots-based and support the same environment variables.

### PoC script: `wayland-poc.sh`

See the companion file [`wayland-poc.sh`](wayland-poc.sh). It demonstrates:
1. Launching `sway` in headless mode with a software renderer.
2. Starting `wayvnc` on port 5900.
3. Launching a test X11 application (`xclock` or `psdoom`) under XWayland.

### Dockerfile snippet (future integration)

```dockerfile
# --- Final stage (based on Debian 12) ---
FROM debian:12-slim

# Install runtime deps
RUN apt-get update && apt-get install -y --no-install-recommends \
    libsdl-mixer1.2 libsdl-net1.2 \
    sway xwayland wayvnc wlr-randr \
    libpixman-1-0 libwayland-client0 libxkbcommon0 \
    && rm -rf /var/lib/apt/lists/*

# Copy psdoom, kubedoom, kubectl, wad, etc.
# COPY --from=build-converge /build /

# Runtime user (distroless style)
USER 65532
ENV XDG_RUNTIME_DIR=/tmp/sway-runtime
ENV WLR_BACKENDS=headless
ENV WLR_RENDERER=pixman
ENV WLR_LIBINPUT_NO_DEVICES=1

# The entrypoint would become a small wrapper that starts sway,
# extracts DISPLAY, starts wayvnc, and then execs kubedoom.
ENTRYPOINT ["/usr/local/bin/wayland-entrypoint.sh"]
```

---

## Gotchas & Blockers

| Issue | Mitigation |
|-------|------------|
| **Dynamic DISPLAY number** | In a fresh container `XWayland` usually claims `:0`, but it is not guaranteed. Use a wrapper script that reads `DISPLAY` from `/proc/<sway_pid>/environ` or launch `psdoom` via `swaymsg exec`. |
| **Resolution** | Headless outputs default to 1920×1080. Use `wlr-randr` or a line in `sway` config (`output HEADLESS-1 resolution 640x480`) to force the desired size. |
| **Wayland socket cleanup** | Always remove stale `wayland-*` sockets in `XDG_RUNTIME_DIR` before starting the compositor. |
| **VNC password** | `wayvnc` does **not** use a VNC password file like `x11vnc`. It supports plain-text config or TLS. If you need password auth, create a `wayvnc` config file (`~/.config/wayvnc/config`). |
| **No audio in VNC** | `wayvnc` only transports video/input. Audio was never part of the existing `x11vnc` setup, so there is no regression. |
| **Container size** | `sway` + `wayvnc` + `xwayland` pulls in more libraries than `xvfb`+`x11vnc`. Expect ~50-100 MB extra. Using `labwc` instead of `sway` shaves a few MB. |

---

## Conclusion

**Option B is the only realistic path.** It swaps the display/VNC layer without touching the Doom engine or the Kubernetes integration. The technology is mature, packaged in Debian 12, and proven in headless containers. The only code change required in the Go layer is removing the hard-coded `DISPLAY=:99` so that `psdoom` inherits the `DISPLAY` exported by XWayland.
