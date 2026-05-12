# Kube DOOM
## Kill Kubernetes pods using Id's Doom!

The next level of chaos engineering is here! Kill pods inside your Kubernetes
cluster by shooting them in Doom!

This is a fork of the excellent
[gideonred/dockerdoomd](https://github.com/gideonred/dockerdoomd) using a
slightly modified Doom, forked from https://github.com/gideonred/dockerdoom,
which was forked from psdoom.

> **Modernized fork**: This version uses `client-go` instead of shelling out to
> `kubectl`, runs on **Debian 12**, supports **Wayland** (with X11 fallback),
> and runs as a **non-root** user with hardened Kubernetes manifests.

![DOOM](assets/doom.jpg)

---

## Requirements

- A Kubernetes cluster (local or remote) and a valid `kubeconfig`
- [Docker Desktop](https://www.docker.com/products/docker-desktop) (macOS/Linux/Windows)
- A VNC viewer:
  - **macOS**: [TigerVNC](https://github.com/TigerVNC/tigervnc/releases), [RealVNC Viewer](https://www.realvnc.com/en/connect/download/viewer/), or [Chicken of the VNC](https://sourceforge.net/projects/chicken/)
  - **Linux**: `vncviewer` (TigerVNC), `remmina`, `vinagre`
  - **Windows**: RealVNC Viewer, TightVNC

---

## Running Locally (macOS / Linux / Windows)

The container needs access to your `kubeconfig` so it can talk to your cluster.
Because the image now runs as a **non-root user** (`kubedoom`, UID `65532`),
mount your kubeconfig into `/home/kubedoom/.kube`.

> **macOS users**: `--net=host` does **not** work on Docker Desktop for Mac.
> Use port forwarding (`-p 5900:5900`) as shown below.

### Docker (recommended for macOS)

```console
$ docker run -p 5900:5900 \
  -v ~/.kube:/home/kubedoom/.kube \
  -e KUBECONFIG=/home/kubedoom/.kube/config \
  --rm -it --name kubedoom \
  ghcr.io/storax/kubedoom:latest
```

Then connect your VNC client to **`localhost:5900`**.

### Podman

```console
$ podman run -it -p 5900:5900/tcp \
  -v ~/.kube:/home/kubedoom/.kube --security-opt label=disable \
  -e KUBECONFIG=/home/kubedoom/.kube/config \
  --name kubedoom \
  ghcr.io/storax/kubedoom:latest
```

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `NAMESPACE` | *(empty)* | Limit pod deletion to a single namespace |
| `KUBECONFIG` | `/home/kubedoom/.kube/config` | Path to kubeconfig inside the container |
| `KUBEDOOM_DISPLAY_BACKEND` | `wayland` | Display stack: `wayland` (sway+XWayland+wayvnc) or `x11` (Xvfb+x11vnc) |

### Limiting to a namespace

```console
$ docker run -p 5900:5900 \
  -v ~/.kube:/home/kubedoom/.kube \
  -e KUBECONFIG=/home/kubedoom/.kube/config \
  -e NAMESPACE=default \
  --rm -it ghcr.io/storax/kubedoom:latest
```

### Killing namespaces

```console
$ docker run -p 5900:5900 \
  -v ~/.kube:/home/kubedoom/.kube \
  -e KUBECONFIG=/home/kubedoom/.kube/config \
  --rm -it ghcr.io/storax/kubedoom:latest \
  -mode namespaces
```

---

## Connecting via VNC

Once the container is running, open your VNC viewer and connect to:

```
localhost:5900
```

- **X11 mode**: The password is `idbehold` (configurable at build time via `VNCPASSWORD`).
- **Wayland mode**: `wayvnc` does not use the x11vnc password file. If you need authentication, configure `wayvnc` separately or switch to X11 fallback.

You should now see DOOM! If you want to get the job done quickly, enter the
cheat `idspispopd` and walk through the wall on your right. You should be
greeted by your pods as little pink monsters. Press `CTRL` to fire. If the
pistol is not your thing, cheat with `idkfa` and press `5` for a nice surprise.
Pause the game with `ESC`.

---

## Running inside Kubernetes (kind)

The `/manifest` directory contains hardened, production-ready manifests:
- Least-privilege RBAC (no more `cluster-admin`!)
- `securityContext` with non-root user, read-only root FS, dropped capabilities
- `NetworkPolicy` default-deny
- `PodDisruptionBudget`
- VNC exposed via `NodePort` Service

### 1. Create a kind cluster

```console
$ kind create cluster --config kind-config.yaml
Creating cluster "kind" ...
 ✓ Ensuring node image (kindest/node:v1.32.0) 🖼
 ✓ Preparing nodes 📦 📦
 ✓ Writing configuration 📜
 ✓ Starting control-plane 🕹️
 ✓ Installing CNI 🔌
 ✓ Installing StorageClass 💾
 ✓ Joining worker nodes 🚜
Set kubectl context to "kind-kind"
```

### 2. Deploy KubeDoom

```console
$ kubectl apply -k manifest/
namespace/kubedoom created
serviceaccount/kubedoom created
clusterrole.rbac.authorization.k8s.io/kubedoom created
clusterrolebinding.rbac.authorization.k8s.io/kubedoom created
service/kubedoom created
networkpolicy.networking.k8s.io/kubedoom created
poddisruptionbudget.policy/kubedoom created
deployment.apps/kubedoom created
```

### 3. Connect via VNC

Use port-forward (simplest for kind):

```console
$ kubectl port-forward -n kubedoom svc/kubedoom 5900:5900
```

Then connect your VNC viewer to **`localhost:5900`**.

Alternatively, use the NodePort (`30090`) if you exposed it through kind's
`extraPortMappings`.

---

## Building the image

This Dockerfile uses **BuildKit** features (cache mounts, `COPY --link`).
BuildKit is enabled by default in Docker 23.0+; for older versions set:

```console
$ export DOCKER_BUILDKIT=1
```

Then build:

```console
$ docker build --build-arg=TARGETARCH=amd64 -t kubedoom .
```

Supported architectures: `linux/amd64`, `linux/arm64`.

To change the default VNC password (X11 mode only):

```console
$ docker build --build-arg=VNCPASSWORD=differentpw -t kubedoom .
```

---

## Architecture Overview

```
┌─────────────────────────────────────────────┐
│  Container (Debian 12, non-root UID 65532)  │
│  ┌─────────────────────────────────────┐    │
│  │  entrypoint.sh                      │    │
│  │  ├─ Wayland: sway + XWayland        │    │
│  │  │            + wayvnc (:5900)      │    │
│  │  ├─ X11 fallback: Xvfb + x11vnc     │    │
│  │  └─ psdoom (SDL1.2)                 │    │
│  └─────────────────────────────────────┘    │
│  ┌─────────────────────────────────────┐    │
│  │  kubedoom (Go binary)               │    │
│  │  ├─ client-go (list/delete pods)    │    │
│  │  └─ Unix socket /dockerdoom.socket  │    │
│  └─────────────────────────────────────┘    │
└─────────────────────────────────────────────┘
```

---

## What changed in this modernized fork?

- **Go 1.23** + `client-go` v0.31 — no more `kubectl` shell-outs
- **Structured logging** with `log/slog`
- **Graceful shutdown** on `SIGTERM`/`SIGINT`
- **Debian 12** base image (was Ubuntu 21.10 EOL)
- **Non-root user** with hardened `securityContext`
- **Wayland support** via `sway` + `XWayland` + `wayvnc`
- **X11 fallback** still available via `KUBEDOOM_DISPLAY_BACKEND=x11`
- **Least-privilege RBAC** (pods/namespaces: get/list/delete only)
- **`NetworkPolicy`** default-deny
- **17 Go tests** covering hash logic, fake clientset, and socket protocol
