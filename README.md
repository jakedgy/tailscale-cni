# Tailscale CNI

A Kubernetes CNI plugin that gives each pod its own Tailscale IP and identity.

> **Hold my beer.** This was built on a Saturday evening to answer the question: "Can I even make this work?" Turns out, yes. Should you run it in production? Absolutely not. Should you run it in your home lab and see what happens? That's the spirit.

## What Is This?

This is a weekend hack project that embeds Tailscale's `LocalBackend` (the guts of `tailscaled`) into a Kubernetes CNI plugin. Every pod gets its own Tailscale identity, its own 100.x.x.x IP, and shows up in your tailnet like a first-class citizen.

It's not production software. It's "I wonder if I can make the Tailscale internals do this" software.

**Intended use case**: Messing around in your home lab.

## Features

- Each pod gets a unique Tailscale IP address (100.x.x.x)
- Pods are directly accessible from any device on your tailnet
- Uses kernel networking (not userspace) for native performance
- Survives daemon restarts without losing pod connectivity

## The Price of Admission

- ~10-20MB memory **per pod** (each one runs its own LocalBackend)
- Your Tailscale admin console will fill up with nodes
- If the daemon dies, all pod networking stops until it restarts
- Only tested with k3d
- Linux only
- No IPv6

See [WHY.md](WHY.md) for a brutally honest assessment.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     Kubernetes Node                         │
├─────────────────────────────────────────────────────────────┤
│  kubelet                                                    │
│     │ CNI ADD/DEL                                           │
│     ▼                                                       │
│  tailscale-cni ──────gRPC────▶ tailscale-cni-daemon        │
│  (shim binary)         Unix   (DaemonSet)                   │
│                       Socket       │                        │
│                                    ├── OAuth Manager        │
│                                    ├── Pod Manager          │
│                                    │   └── LocalBackend/pod │
│                                    └── TUN + veth bridging  │
│                                              │              │
│  Host Namespace         Pod Namespace        │              │
│  ┌──────────────┐      ┌─────────────────────┼─────────┐   │
│  │ ts-abc (TUN) │◄────►│ ts0 (veth)          │         │   │
│  │    ▲         │      │  100.x.x.x ◄────────┘         │   │
│  │    │         │      │                               │   │
│  │ wgengine     │      │ eth0: 10.244.x.x (flannel)    │   │
│  └──────────────┘      └───────────────────────────────┘   │
└─────────────────────────────────────────────────────────────┘
```

For the gory details, see [ARCHITECTURE.md](ARCHITECTURE.md).

## Quick Start (k3d)

```bash
# Create a k3d cluster, build everything, deploy it
make k3d-setup

# But first, create your OAuth credentials:
# 1. Go to https://login.tailscale.com/admin/settings/oauth
# 2. Create OAuth client with 'devices' write + 'auth keys' write scopes
# 3. Copy deploy/secret.yaml.example to deploy/secret.yaml
# 4. Fill in your credentials
```

## Prerequisites

1. **k3d** (other distributions might work, no promises)
2. A Tailscale account
3. An OAuth client with `devices` write and `auth keys` write scopes
4. A sense of adventure

## Tailscale ACL Setup

Add to your Tailscale ACL policy:

```json
{
  "tagOwners": {
    "tag:k8s-pod": ["autogroup:admin"]
  },
  "acls": [
    {
      "action": "accept",
      "src": ["tag:k8s-pod"],
      "dst": ["*:*"]
    }
  ]
}
```

## Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `TS_OAUTH_CLIENT_ID` | Tailscale OAuth client ID | Required |
| `TS_OAUTH_CLIENT_SECRET` | Tailscale OAuth client secret | Required |
| `CLUSTER_NAME` | Cluster name (used in hostnames) | `k8s` |
| `TS_TAGS` | Comma-separated Tailscale tags | `tag:k8s-pod` |
| `AUTH_KEY_TTL` | TTL for auth keys (e.g., `5m`, `10m`) | `5m` |

## How It Works

1. kubelet invokes CNI plugin
2. CNI shim talks to daemon via gRPC
3. Daemon creates OAuth auth key, TUN device, LocalBackend, veth pair
4. Pod gets Tailscale IP alongside its cluster IP
5. Traffic flows: `pod → veth → kernel routing → TUN → wgengine → WireGuard → tailnet`

## Development

```bash
make build      # Build binaries
make test       # Run tests (in Docker, because Linux)
make docker     # Build container image
make logs       # View daemon logs
make restart    # Restart the daemon
```

## When Things Break

```bash
# Check daemon logs
kubectl -n kube-system logs -l app=tailscale-cni -f

# Nuclear option: delete everything and start over
make k3d-delete && make k3d-setup
```

## What I Learned Building This

- `tsnet.Server` uses gVisor's userspace TCP/IP stack; `LocalBackend` gives you kernel networking
- Persisting the node key (FileStore) is how you keep the same Tailscale IP across restarts
- Network namespace manipulation in Go is fiddly but doable
- veth pairs are the duct tape of Linux networking
- The Tailscale codebase is surprisingly approachable once you find the right entry points

## License

BSD-3-Clause

---

*Built with mass spectrometry and mass quantities of caffeine.*
