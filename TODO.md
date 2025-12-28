# TODO

Future work and known issues for tailscale-cni.

## High Priority

### Metrics / Observability
**Issue:** No visibility into daemon health or per-pod status.

**Tasks:**
- [ ] Expose Prometheus metrics endpoint
- [ ] Track: pods managed, auth key creations, failures, latency
- [ ] Add structured logging (JSON)

## Medium Priority

### IPv6 Support
**Issue:** IPv6 is not implemented.

**Tasks:**
- [ ] Assign IPv6 Tailscale address to pods
- [ ] Add IPv6 routes (fd7a:115c:a1e0::/48)
- [ ] Test dual-stack connectivity

### Health Checks
**Issue:** CNI CHECK just verifies the pod exists in memory, not that networking actually works.

**Tasks:**
- [ ] Verify TUN device exists and is UP
- [ ] Verify veth pair exists
- [ ] Optionally ping control plane to verify connectivity

## Low Priority

### Multi-Tailnet Support
**Issue:** All pods join the same tailnet (determined by OAuth credentials).

**Use case:** Different namespaces on different tailnets.

**Tasks:**
- [ ] Allow per-namespace OAuth credentials (via Secrets)
- [ ] Track which tailnet each pod belongs to

### Pod Annotations
**Issue:** No way to customize per-pod Tailscale behavior.

**Use cases:**
- Custom hostname override
- Additional tags
- Opt-out of Tailscale entirely

**Tasks:**
- [ ] Read pod annotations in CNI ADD
- [ ] Pass to daemon for processing
- [ ] Document supported annotations

### Exit Node Support
**Issue:** Pods can't act as exit nodes.

**Tasks:**
- [ ] Add annotation to enable exit node
- [ ] Configure LocalBackend with AdvertiseRoutes
- [ ] Security considerations (probably namespace-restricted)

## Won't Fix (Probably)

### NetworkPolicy Integration
Tailscale traffic bypasses Kubernetes NetworkPolicies because it uses a separate interface (ts0) that NetworkPolicy controllers don't manage.

**Why won't fix:** This would require deep integration with CNI chaining and network policy implementations. The complexity isn't worth it for an MVP. Users who need NetworkPolicy should use Tailscale ACLs instead.

### Windows/macOS Node Support
CNI plugins only run on Linux. This is a Kubernetes limitation, not ours.

---

*Last updated: 2025-12*
