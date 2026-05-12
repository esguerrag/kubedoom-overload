# syntax=docker/dockerfile:1

# =============================================================================
# Multi-stage Dockerfile for KubeDoom
# Optimized for BuildKit with cache mounts, layer reduction, and minimal
# runtime footprint.
# =============================================================================

# -----------------------------------------------------------------------------
# Stage 1: Build the kubedoom Go binary
# -----------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build-kubedoom
WORKDIR /go/src/kubedoom
COPY go.mod go.sum ./
COPY kubedoom.go .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o kubedoom .

# -----------------------------------------------------------------------------
# Stage 2: Download game assets
# -----------------------------------------------------------------------------
FROM debian:12-slim AS build-assets
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
    apt-get update && apt-get install -y \
      -o APT::Install-Suggests=0 \
      --no-install-recommends \
      ca-certificates wget
RUN wget -qO /doom1.wad \
    http://distro.ibiblio.org/pub/linux/distributions/slitaz/sources/packages/d/doom1.wad

# -----------------------------------------------------------------------------
# Stage 3: Build psdoom from C source
# -----------------------------------------------------------------------------
FROM debian:12-slim AS build-doom
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
    apt-get update && apt-get install -y \
      -o APT::Install-Suggests=0 \
      --no-install-recommends \
      build-essential gcc \
      libsdl-mixer1.2-dev libsdl-net1.2-dev
COPY dockerdoom /dockerdoom
WORKDIR /dockerdoom/trunk
RUN ./configure && make && make install

# -----------------------------------------------------------------------------
# Stage 4: Final runtime image — hardened & minimal
# -----------------------------------------------------------------------------
FROM debian:12-slim
ARG VNCPASSWORD=idbehold

# Install runtime dependencies.
# We use apt cache mounts so repeated builds don't re-download package indexes.
# Packages intentionally OMITTED vs the legacy Dockerfile:
#   - netcat-openbsd   : never used by the application
#   - fonts-dejavu     : psdoom uses internal bitmap fonts; system fonts are unused
#   - wlr-randr        : resolution is set via sway config (output HEADLESS-1)
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
    apt-get update && apt-get install -y \
      -o APT::Install-Suggests=0 \
      --no-install-recommends \
      ca-certificates \
      libsdl-mixer1.2 libsdl-net1.2 \
      x11vnc xvfb \
      sway xwayland wayvnc \
      libpixman-1-0 libwayland-client0 libxkbcommon0

# Create non-root user, configure x11vnc password fallback, and ensure the
# root directory is group-writable so the kubedoom user can create the Unix
# socket at /tmp/dockerdoom.socket.  Everything in a single RUN to minimise layers.
# Extend UID_MAX so useradd doesn't warn about high UIDs (common for
# distroless/container users).  This is cosmetic only.
RUN sed -i 's/^UID_MAX[[:space:]]*.*/UID_MAX\t\t65536/' /etc/login.defs && \
    groupadd -g 65532 kubedoom && \
    useradd -l -u 65532 -g kubedoom -s /bin/sh -m kubedoom && \
    mkdir -p /home/kubedoom/.vnc && \
    x11vnc -storepasswd "${VNCPASSWORD}" /home/kubedoom/.vnc/passwd && \
    chown -R kubedoom:kubedoom /home/kubedoom/.vnc && \
    chgrp kubedoom / && chmod 775 /

# Copy binaries and assets from previous stages.
# COPY --link allows these layers to be cached independently of the previous
# RUN, speeding up incremental builds when only application code changes.
COPY --link --from=build-kubedoom /go/src/kubedoom/kubedoom /usr/bin/kubedoom
COPY --link --from=build-assets    /doom1.wad                /home/kubedoom/doom1.wad
COPY --link --from=build-doom      /usr/local/games/psdoom   /usr/local/games/psdoom
COPY --link entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD test -S /tmp/dockerdoom.socket || grep -q kubedoom /proc/1/comm || exit 1

USER kubedoom
WORKDIR /home/kubedoom
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
