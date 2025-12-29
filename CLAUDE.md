# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build Commands

```bash
make build           # Build both binaries (bin/tailscale-cni, bin/tailscale-cni-daemon)
make build-cni       # Build CNI plugin only
make build-daemon    # Build daemon only
make proto           # Generate protobuf code (requires protoc + go plugins)
make test            # Run tests in Docker (required - CNI code is Linux-only)
make docker          # Build Docker image
make k3d-setup       # Create single-node k3d cluster and deploy
make k3d-setup-multi # Create 3-node k3d cluster (1 server + 2 agents) and deploy
make k3d-create      # Create single-node k3d cluster
make k3d-delete      # Delete k3d cluster
make test-nginx      # Smoke test: deploy nginx, curl via Tailscale IP
make deploy          # Deploy to Kubernetes (kubectl apply -k deploy/)
make undeploy        # Remove from Kubernetes
make logs            # View daemon logs
make restart         # Restart daemon DaemonSet
make fmt             # Format code
make lint            # Run golangci-lint
make deps            # Download and tidy dependencies
```

To run a specific test:
```bash
docker run --rm -v $(pwd):/app -w /app golang:1.25 go test -v -run TestName ./pkg/daemon/
```

## Architecture

This is a Kubernetes CNI plugin that gives each pod its own Tailscale IP and identity using kernel networking (not userspace).

### Components

1. **CNI Binary** (`cmd/cni/main.go`) - Thin shim invoked by kubelet for pod network lifecycle (ADD/DEL/CHECK). Forwards requests to daemon via gRPC over Unix socket.

2. **Daemon** (`cmd/daemon/main.go`) - DaemonSet that runs on each node:
   - `OAuthManager` (`pkg/daemon/oauth.go`) - Caches OAuth tokens, creates auth keys (5-min TTL)
   - `PodManager` (`pkg/daemon/pods.go`) - Creates LocalBackend instances per pod, manages TUN/veth networking
   - `Server` (`pkg/daemon/server.go`) - gRPC server on `/var/run/tailscale-cni/daemon.sock`

3. **Protobuf Definitions** (`pkg/proto/cni.proto`) - gRPC service definition with Add/Del/Check RPCs.

### Per-Pod Resources

Each pod gets:
- TUN device in host namespace (`ts-<containerID[:8]>`) for WireGuard traffic
- veth pair bridging pod to host (`ts0` in pod, `veth<random>` on host)
- LocalBackend + wgengine from tailscale.com library (not tsnet - we need kernel routing, not gVisor)
- State persisted to `/var/lib/tailscale-cni/pods/<containerID>/`

### Traffic Flow

Pod → ts0 (veth) → kernel routing → TUN → wgengine → WireGuard → tailnet

### Key Design Decisions

- Uses `ipnlocal.LocalBackend` directly instead of `tsnet.Server` for kernel TCP/IP via TUN (native performance, all IP protocols, standard debugging tools)
- Pods use FileStore for state persistence, enabling daemon recovery
- Hostname format: `{cluster-name}-{namespace}-{pod-name}` (with Kubernetes suffixes automatically stripped, sanitized to 63 chars)

## Development Notes

- Daemon binary has `//go:build linux` constraint - must develop/test on Linux or use Docker
- Tests run in Docker container via `make test` for Linux compatibility
- OAuth credentials via `TS_OAUTH_CLIENT_ID` and `TS_OAUTH_CLIENT_SECRET` env vars
- CNI binary returns result with Tailscale IP (100.x.x.x) as primary, routes 100.64.0.0/10 via ts0
- Use `make k3d-setup` for quick local development

## Known Limitations

- When daemon crashes, wgengine dies and all pod networking stops (kernel resources survive but nothing processes packets)
- NetworkPolicy bypass - Tailscale traffic uses ts0 interface, not eth0
- ~10-20MB memory per pod due to LocalBackend overhead
- No IPv6 support currently
- Only tested with k3d - other Kubernetes distributions may work but are untested
