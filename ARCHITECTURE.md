# Architecture

This document describes the technical implementation of tailscale-cni.

## High-Level Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Kubernetes Node                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  kubelet                                                                    │
│     │                                                                       │
│     │ CNI ADD/DEL/CHECK                                                     │
│     ▼                                                                       │
│  ┌─────────────────┐         gRPC          ┌─────────────────────────────┐  │
│  │ tailscale-cni   │ ───────────────────▶  │   tailscale-cni-daemon      │  │
│  │ (CNI binary)    │    Unix Socket        │       (DaemonSet)           │  │
│  └─────────────────┘                       │                             │  │
│                                            │  ┌───────────────────────┐  │  │
│                                            │  │    OAuthManager       │  │  │
│                                            │  │  - Token caching      │  │  │
│                                            │  │  - Auth key creation  │  │  │
│                                            │  └───────────────────────┘  │  │
│                                            │                             │  │
│                                            │  ┌───────────────────────┐  │  │
│                                            │  │     PodManager        │  │  │
│                                            │  │  - LocalBackend/pod   │  │  │
│                                            │  │  - TUN devices        │  │  │
│                                            │  │  - veth bridging      │  │  │
│                                            │  └───────────────────────┘  │  │
│                                            └─────────────────────────────┘  │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                        Host Network Namespace                        │   │
│  │                                                                      │   │
│  │   ┌──────────┐        ┌──────────┐                                  │   │
│  │   │ ts-abc123│        │ veth789  │◄─── one pair per pod            │   │
│  │   │  (TUN)   │        │ (veth)   │                                  │   │
│  │   └────┬─────┘        └────┬─────┘                                  │   │
│  │        │                   │                                        │   │
│  │        │    kernel         │                                        │   │
│  │        │    routing        │                                        │   │
│  │        │◄─────────────────►│                                        │   │
│  │        │  100.64.0.0/10    │                                        │   │
│  │        │                   │                                        │   │
│  └────────┼───────────────────┼────────────────────────────────────────┘   │
│           │                   │                                             │
│  ┌────────┼───────────────────┼────────────────────────────────────────┐   │
│  │        │    Pod Network    │ Namespace                               │   │
│  │        │                   │                                        │   │
│  │        ▼                   ▼                                        │   │
│  │   ┌─────────┐         ┌─────────┐                                   │   │
│  │   │ wgengine│         │   ts0   │◄─── Tailscale IP (100.x.x.x)     │   │
│  │   │(usersp) │         │ (veth)  │                                   │   │
│  │   └────┬────┘         └────┬────┘                                   │   │
│  │        │                   │                                        │   │
│  │        ▼                   ▼                                        │   │
│  │   WireGuard            Application                                  │   │
│  │   Encryption           (nginx, etc)                                 │   │
│  │        │                                                            │   │
│  └────────┼────────────────────────────────────────────────────────────┘   │
│           │                                                                 │
│           ▼                                                                 │
│      Internet/Tailnet                                                       │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Components

### CNI Binary (`cmd/cni/main.go`)

A thin shim that kubelet invokes for pod network lifecycle events. It:

1. Parses CNI configuration from stdin
2. Extracts Kubernetes args (pod name, namespace, UID)
3. Connects to the daemon via gRPC on a Unix socket
4. Forwards ADD/DEL/CHECK requests
5. Returns CNI result with assigned Tailscale IP

The binary does no heavy lifting - all networking logic lives in the daemon.

### Daemon (`cmd/daemon/main.go`, `pkg/daemon/`)

A DaemonSet that runs on each node and manages Tailscale nodes for pods.

**OAuthManager** (`pkg/daemon/oauth.go`):
- Caches OAuth access tokens (with 5-minute refresh buffer)
- Creates ephemeral, preauthorized auth keys for each pod
- Auth keys have 5-minute TTL and are single-use

**PodManager** (`pkg/daemon/pods.go`):
- Maintains a map of container ID → ManagedServer
- Creates/destroys LocalBackend instances
- Handles TUN device and veth pair setup
- Persists pod metadata and Tailscale state to disk (FileStore)
- Recovers existing pods on daemon restart (`RecoverPods()`)
- Cleans up orphaned network resources (`CleanupOrphanedResources()`)

**gRPC Server** (`pkg/daemon/server.go`):
- Listens on `/var/run/tailscale-cni/daemon.sock`
- Implements Add, Del, Check RPCs
- Delegates to PodManager

## Network Architecture

### Per-Pod Resources

Each pod gets:

| Resource | Location | Name Format | Purpose |
|----------|----------|-------------|---------|
| TUN device | Host namespace | `ts-<containerID[:8]>` | WireGuard traffic |
| veth pair | Both | `ts0` (pod) / `veth<random>` (host) | Bridge to pod |
| LocalBackend | Daemon process | N/A | Tailscale state machine |
| wgengine | Daemon process | N/A | WireGuard encryption |

### Traffic Flow: Pod → Tailnet

```
1. Application sends packet to 100.x.x.x (another Tailscale node)

2. Kernel routes via ts0 (pod veth)
   └─ Route: 100.64.0.0/10 → ts0

3. Packet traverses veth pair to host namespace

4. Host kernel routes to TUN device
   └─ Route: 100.64.0.0/10 → ts-abc123

5. wgengine reads from TUN

6. WireGuard encrypts packet

7. UDP packet sent to peer (direct or via DERP relay)
```

### Traffic Flow: Tailnet → Pod

```
1. UDP packet arrives from Tailscale peer

2. wgengine decrypts WireGuard packet

3. wgengine writes plaintext to TUN device

4. Kernel routes to destination (pod's Tailscale IP)
   └─ Route: 100.x.x.x/32 → veth789

5. Packet traverses veth pair into pod namespace

6. Application receives packet on ts0 interface
```

## LocalBackend vs tsnet

We use `ipnlocal.LocalBackend` directly instead of `tsnet.Server`. Here's why:

| Aspect | tsnet.Server | LocalBackend |
|--------|--------------|--------------|
| Networking | gVisor userspace stack | Kernel TCP/IP via TUN |
| Performance | Slower (userspace) | Native kernel speed |
| Protocol support | TCP/UDP only | All IP protocols |
| Memory | Higher (gVisor overhead) | Lower |
| Debugging | Complex (no tcpdump) | Standard tools work |

tsnet is designed for embedding Tailscale in Go programs that handle their own TCP connections (like a web server). We need raw IP packet forwarding, which requires kernel networking.

## Key Data Structures

### ManagedServer

Represents a running Tailscale node for a pod:

```go
type ManagedServer struct {
    Backend       *ipnlocal.LocalBackend  // Tailscale state machine
    Engine        wgengine.Engine         // WireGuard engine
    Sys           *tsd.System             // Tailscale system dependencies
    NetMon        *netmon.Monitor         // Network change monitor
    ContainerID   string
    PodName       string
    Namespace     string
    Hostname      string                  // Tailscale hostname
    ClusterIP     string                  // Kubernetes cluster IP
    HostVethName  string                  // Host side of veth pair
    TailscaleIPv4 netip.Addr
    TailscaleIPv6 netip.Addr
    CreatedAt     time.Time
}
```

### PodMetadata

Persisted to disk for recovery:

```go
type PodMetadata struct {
    ContainerID   string    `json:"containerId"`
    PodName       string    `json:"podName"`
    Namespace     string    `json:"namespace"`
    Hostname      string    `json:"hostname"`
    TailscaleIPv4 string    `json:"tailscaleIpv4"`
    TailscaleIPv6 string    `json:"tailscaleIpv6"`
    CreatedAt     time.Time `json:"createdAt"`
    NetnsPath     string    `json:"netnsPath"`
    HostVethName  string    `json:"hostVethName"`
    ClusterIP     string    `json:"clusterIP"`
}
```

## State Management

### Daemon State

- In-memory map of containerID → ManagedServer
- Recovered on daemon restart from persisted state
- Metadata persisted to `/var/lib/tailscale-cni/pods/<containerID>/metadata.json`

### Tailscale State

- WireGuard keys stored in FileStore (`tailscale.state`)
- Node keys persist across daemon restarts, preserving Tailscale IPs
- Nodes are NOT ephemeral - cleanup happens explicitly via CNI DEL

## Hostname Generation

Pod hostnames on the tailnet follow the pattern:

```
{cluster-name}-{namespace}-{pod-name}
```

Sanitization rules (`sanitizeHostname()`):
- Lowercase
- Replace non-alphanumeric with dashes
- Collapse multiple dashes
- Trim leading/trailing dashes
- Truncate to 63 characters (DNS limit)

Example: `k3d-default-nginx-deployment-7b5d9c6f8-xyz`

## Cleanup

### Pod Deletion

1. CNI binary calls daemon's Del RPC
2. Daemon calls `LocalBackend.Shutdown()`
3. Daemon calls `Engine.Close()`
4. Daemon calls `NetMon.Close()`
5. Daemon deletes host veth (pod side cleaned up with namespace)
6. Daemon removes state directory
7. Tailscale control plane removes ephemeral node

### Daemon Shutdown / Crash

When the daemon dies, **networking temporarily stops** until the daemon restarts.

The wgengine (WireGuard encryption/decryption) runs inside the daemon process. When the daemon dies:

| Resource | Survives? | Notes |
|----------|-----------|-------|
| TUN device | Yes | Kernel resource, still exists |
| veth pair | Yes | Kernel resource, still exists |
| Routing tables | Yes | Kernel state, still exists |
| Node keys (FileStore) | Yes | Persisted to disk |
| wgengine | **No** | Dies with daemon - no encryption/decryption |
| LocalBackend | **No** | Dies with daemon - no control plane |

Without wgengine, packets arrive at the TUN device but nothing reads them. Traffic stops flowing.

**On daemon restart (automatic recovery):**
1. Daemon scans `/var/lib/tailscale-cni/pods/` for metadata files
2. For each pod, checks if network namespace still exists
3. If netns exists: recovers using persisted FileStore (preserves node key → same Tailscale IP)
4. If netns is gone: cleans up orphaned TUN/veth devices
5. Creates new TUN device and wgengine for each recovered pod
6. Reconnects to Tailscale control plane with existing identity
7. If Tailscale IP changed: updates pod interface and host routes in-place

Pods maintain their Tailscale IPs across daemon restarts thanks to FileStore persistence.

## Failure Modes

| Failure | Impact | Recovery |
|---------|--------|----------|
| Auth key expires before pod starts | Pod never gets Tailscale IP | Delete and recreate pod |
| Daemon crashes | Pod networking stops temporarily | Automatic recovery on daemon restart |
| Control plane unreachable | No new pods, existing may stop working | Wait for connectivity |
| TUN device creation fails | Pod creation fails | Check permissions, kernel modules |
| veth creation fails | Pod creation fails | Check netns still exists |
| Recovery fails for a pod | That pod loses Tailscale connectivity | Pod is cleaned up; recreate if needed |
