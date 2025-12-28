# Why This Is Probably A Terrible Idea

You're looking at a CNI plugin that gives every Kubernetes pod its own Tailscale node identity. Every. Single. Pod. Yes, even that busybox you're using to debug DNS.

Before you deploy this, let's have an honest conversation about what you're getting yourself into.

## The Problem We're Solving

Kubernetes pods are ephemeral. They come and go like clouds on a windy day. Tailscale nodes, historically, are not. They have identities, keys, and ACL entries. They show up in your admin console. They expect to exist for more than the 47 seconds it takes your CrashLoopBackOff pod to fail again.

This CNI tries to bridge that gap. Each pod gets:
- Its own Tailscale IP (100.x.x.x)
- Its own identity on your tailnet
- Its own WireGuard keys
- Its own connection to the Tailscale control plane

This means any device on your tailnet can reach your pods directly. No LoadBalancer, no Ingress, no port-forwarding. Just `curl http://100.67.175.20:8080` from your laptop and you're talking to nginx running in your cluster.

## Why This Works (But Barely)

The implementation uses Tailscale's `LocalBackend` - the same machinery that powers the regular Tailscale client - running one instance per pod. Each LocalBackend:

1. Creates a TUN device in the host namespace
2. Spins up a WireGuard engine
3. Connects to the Tailscale control plane
4. Negotiates keys with your other tailnet devices
5. Routes traffic through a veth pair into the pod

It's like running `tailscaled` for each pod, except embedded in a daemon that manages the lifecycle.

## Resource Concerns

Let's talk about what "one LocalBackend per pod" actually means:

**Memory**: Each LocalBackend allocates buffers for WireGuard, maintains routing tables, and keeps control plane state. Ballpark: 10-20MB per pod. Run 100 pods? That's 1-2GB just for Tailscale.

**Goroutines**: The WireGuard engine loves goroutines. Packet processing, timer wheels, control plane polling - each pod spawns dozens. Your `pprof` output will be... interesting.

**Control Plane Connections**: Every pod maintains its own connection to `controlplane.tailscale.com`. Tailscale's servers are robust, but if you're running thousands of pods, maybe give them a heads up first.

**Tailnet Node Count**: Your Tailscale admin console will fill up with entries like `k3d-default-nginx-7b5d9-xyz`. Each one counts against your device limit. Hope you're on a plan with headroom.

## The Auth Key Dance

Getting a pod onto your tailnet requires an auth key. Here's the circus:

1. Daemon has OAuth credentials (`TS_OAUTH_CLIENT_ID`, `TS_OAUTH_CLIENT_SECRET`)
2. Daemon requests an access token from Tailscale API
3. Daemon creates an ephemeral, preauthorized auth key (5-minute TTL)
4. LocalBackend uses that key to authenticate
5. Tailscale control plane validates and issues the real credentials

If your pod takes longer than 5 minutes to start networking (looking at you, slow container registries), congratulations - you've found a new failure mode. The auth key expires and the pod will never connect.

The keys are ephemeral, meaning when the node disconnects, it's automatically removed from your tailnet. In theory. In practice, you might see ghost entries for a bit.

## What Could Go Wrong

Here's a non-exhaustive list of ways this can fail:

**Cleanup Races**: When a pod dies, we try to clean up - shutdown the LocalBackend, remove the TUN device, delete the veth pair. But sometimes the pod's network namespace is already gone. Sometimes the daemon crashes first. Sometimes Mercury is in retrograde. Good news: the daemon now scans for orphaned devices on startup and cleans them up. The astrology problem remains unsolved.

**State Recovery**: Good news! We actually fixed this one. The daemon now persists node keys to disk (FileStore), and on restart it scans for existing pods, checks if their network namespaces still exist, and reconnects them with the same Tailscale IPs. It's like nothing happened. Mostly. Usually. The veth bridge might need rewiring, but we handle that too.

**ACL Complexity**: Your Tailscale ACLs now need to account for potentially thousands of dynamically-created nodes. The `tag:k8s-pod` tag helps, but ACL debugging becomes... exciting.

**Network Policy Confusion**: Kubernetes NetworkPolicies don't know about Tailscale interfaces. Your carefully crafted egress rules? They apply to `eth0`, not `ts0`. Tailscale traffic bypasses them entirely.

**IPv6**: Not implemented. Don't ask.

## You Might Want This If...

- You need to access specific pods from outside the cluster without exposing them to the internet
- You're building a hybrid cloud setup where pods need to reach on-prem resources via Tailscale
- You want per-pod identity for auditing/ACL purposes
- You enjoy living dangerously
- You have a tailnet and want to make it more... exciting

## You Definitely Don't Want This If...

- You have more than a few hundred pods
- You need battle-tested production reliability (though we're getting closer)
- You want to use Kubernetes NetworkPolicies for Tailscale traffic
- You're running on a shared/managed Kubernetes where you can't deploy DaemonSets
- You expect things to "just work" the first time
- Your SRE team asks questions like "why are there 500 goroutines per pod?"

## The Honest Assessment

This started as an MVP and has grown some actual features. Daemon recovery works. Orphaned resource cleanup works. It's no longer held together purely with optimism - we've upgraded to cautious optimism.

That said, it's still held together with veth pairs, kernel routing tables, and gRPC calls. The resource overhead per pod is real. The control plane connection count is real. The "why are there 500 entries in my Tailscale admin console" is very real.

If you're experimenting with hybrid networking, building a dev environment, or just curious about how Tailscale internals work - welcome aboard.

If you're planning to run this in production with thousands of pods and strict uptime requirements - maybe test thoroughly first. And keep my contact info handy. Just in case.

## Still Here?

Check out [ARCHITECTURE.md](ARCHITECTURE.md) for the gory technical details, or just `kubectl apply` and see what happens. YOLO.

---

*"It's not a bug, it's a learning opportunity."*
