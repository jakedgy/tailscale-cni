# TODO

Future work for tailscale-cni. This is a homelab project - scope accordingly.

## Actually Useful

### Configurable Auth Key TTL
**Why:** Auth keys are currently hardcoded to 5 minutes. If your container registry is slow and image pull takes 6 minutes... the key expires and the pod never connects.

**Ideas:**
- [ ] Daemon flag `--auth-key-ttl=10m`
- [ ] Sensible default, but let people increase it for slow environments

### Pod Annotations
**Why:** Not every pod needs a Tailscale IP. Your CSI driver? Probably not. Your Plex server? Definitely.

**Ideas:**
- [ ] `tailscale.com/enabled: "false"` - opt out of Tailscale for this pod
- [ ] `tailscale.com/tags: "tag:media,tag:plex"` - custom tags per pod
- [ ] `tailscale.com/hostname: "plex"` - custom hostname override
- [ ] `tailscale.com/ephemeral: "false"` - persist node after pod dies (useful for stable Plex identity)

### Namespace Defaults
**Why:** Maybe you want all pods in `media` namespace to get Tailscale IPs, but nothing in `kube-system`.

**Ideas:**
- [ ] Namespace annotation to opt-in/opt-out all pods
- [ ] Namespace-level default tags
- [ ] Pod annotations override namespace defaults

### Ephemeral Mode Control
**Why:** Ephemeral nodes disappear when the pod dies. Great for random workloads, annoying for your Plex server that you want to keep the same Tailscale IP forever.

**Ideas:**
- [ ] Daemon flag `--ephemeral=true|false` for global default
- [ ] Pod annotation override (see above)
- [ ] Non-ephemeral nodes need manual cleanup (or re-use on pod recreation)

### Auto-tags from Namespace
**Why:** Less config, more convention. Pod in `media` namespace automatically gets `tag:media`.

**Ideas:**
- [ ] `tag:ns-{namespace}` automatically added to all pods
- [ ] Makes ACLs "just work" with your namespace structure
- [ ] Optional - daemon flag to enable/disable

### Better Hostname Generation
**Why:** `k3d-media-plex-deployment-7b5d9c6f8-xyz` is ugly. You just want `plex`.

**Ideas:**
- [ ] Smarter defaults (drop deployment hash suffix)
- [ ] Hostname annotation override (see above)

### ACL Templates / Documentation
**Why:** Help people not shoot themselves in the foot.

**Ideas:**
- [ ] Example ACLs for common setups (media server, game server, dev tools)
- [ ] "Phone can access Plex but not Postgres" patterns
- [ ] Recommended tag hierarchy for homelab

## Maybe Someday

### MagicDNS
**Why:** Does `plex.your-tailnet.ts.net` just work? If so, document it prominently. If not, figure out why.

**Tasks:**
- [ ] Test if MagicDNS resolution works out of the box
- [ ] Document in README if it does
- [ ] Fix it if it doesn't

### Game Servers
**Why:** This might be the killer app. Minecraft, Valheim, Factorio - friends connect directly via Tailscale IP. No port forwarding, no Realms subscription, no Hamachi nonsense.

**Ideas:**
- [ ] Document this use case prominently
- [ ] Example manifests for common game servers
- [ ] Maybe a `examples/minecraft/` directory

### Headscale Support
**Why:** Some homelabbers run their own control plane instead of Tailscale's servers.

**Ideas:**
- [ ] Daemon flag `--control-url=https://headscale.example.com`
- [ ] Might just work? Needs testing
- [ ] Document any quirks

### Stale Node Cleanup
**Why:** If pods die ungracefully, ghost nodes linger in your Tailscale admin console forever.

**Ideas:**
- [ ] Reconciliation loop that compares running pods to Tailscale nodes
- [ ] Clean up nodes that no longer have matching pods
- [ ] Maybe just a manual `kubectl ts cleanup` command

### Exit Node Mode
**Why:** Route all your traffic through your homelab when traveling. VPN home without running a separate VPN server.

**Ideas:**
- [ ] `tailscale.com/exit-node: "true"` annotation
- [ ] Needs LocalBackend to advertise exit node capability

**Caveat:** Unclear if LocalBackend exposes this. Needs investigation.

### Node Sharing
**Why:** Share your Plex pod with a friend without them joining your entire tailnet. "Here's movie night access, nothing else."

**Caveat:** This might be control-plane-only and "just work" via the Tailscale admin console. Or it might need code changes. Unknown.

### Stable Identity for StatefulSets
**Why:** `postgres-0` gets rescheduled to another node. Can it keep the same Tailscale IP? Right now state is per-node, so it gets a new identity.

**Ideas:**
- [ ] Detect StatefulSet pods and use persistent identity
- [ ] Maybe store state in a PVC instead of hostPath
- [ ] Or use the pod name as a stable key for state lookup

**Caveat:** This is tricky. Might involve significant rearchitecting.

### Status/Debug Command
**Why:** "Is my pod actually connected to Tailscale?" is a common question.

**Ideas:**
- [ ] `kubectl ts status` or similar
- [ ] Show pod → Tailscale IP mapping, connection state, last handshake
- [ ] Could just be a kubectl plugin that queries the daemon

### Funnel Support
**Why:** Expose a pod to the public internet without port forwarding. Great for webhooks, public sites, or letting a friend access something without joining your tailnet.

**Caveat:** Unclear if LocalBackend exposes the right knobs for Funnel, or if the control plane honors it for auth-key-created nodes. Might need research. Might need a sidecar. Might be a "won't fix."

### Tailscale SSH
**Why:** SSH into pods via Tailscale. No `kubectl exec`, no port-forward, just `ssh root@plex`.

**Caveat:** Unclear if this works with LocalBackend or if it expects a full `tailscaled` setup. Needs investigation.

### Taildrop to Pods
**Why:** Send files to a pod via Taildrop. Drop a file onto your Nextcloud pod from your phone.

**Caveat:** Fun but probably low priority. Unclear if LocalBackend handles Taildrop or if that's a `tailscaled` thing.

### IPv6 Support
Would be nice, not critical for most homelab setups.

### Health Checks
CNI CHECK currently just verifies the pod exists in memory. Could actually verify networking works.

## Won't Fix

### NetworkPolicy Integration
Tailscale traffic bypasses NetworkPolicies (uses ts0, not eth0). Use Tailscale ACLs instead.

### Enterprise Features
Metrics, multi-tailnet, audit logging - if you need these, you probably need a real solution.

### Windows/macOS Nodes
CNI plugins only run on Linux. That's Kubernetes, not us.

---

*Last updated: 2025-12*
