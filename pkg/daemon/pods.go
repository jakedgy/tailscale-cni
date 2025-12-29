//go:build linux

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/tailscale/wireguard-go/tun"
	"github.com/vishvananda/netlink"
	"tailscale.com/control/controlclient"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnlocal"
	"tailscale.com/ipn/store"
	"tailscale.com/net/netmon"
	"tailscale.com/net/tsdial"
	"tailscale.com/net/tstun"
	"tailscale.com/tsd"
	"tailscale.com/types/logid"
	"tailscale.com/types/logger"
	"tailscale.com/wgengine"
	"tailscale.com/wgengine/netstack"
)

var (
	// Regex patterns for Kubernetes pod name suffix stripping
	// Pattern for ReplicaSet: {name}-{hash}-{random}
	// The hash is 8-10 alphanumeric characters, followed by a dash and 5 alphanumeric characters
	replicaSetPattern = regexp.MustCompile(`^(.+)-[a-z0-9]{8,10}-[a-z0-9]{5}$`)
	
	// Pattern for Deployment/ReplicaSet without random suffix: {name}-{hash}
	// Only strip if hash looks like a ReplicaSet hash (8-10 alphanumeric characters)
	deploymentPattern = regexp.MustCompile(`^(.+)-[a-z0-9]{8,10}$`)
	
	// Pattern to detect StatefulSet ordinals (e.g., -0, -1, -2, up to -999)
	// This checks if the pod name ends with a dash followed by 1-3 digits
	statefulSetOrdinalPattern = regexp.MustCompile(`-\d{1,3}$`)
	
	// Patterns for hostname sanitization
	hostnameInvalidCharsPattern = regexp.MustCompile(`[^a-z0-9-]`)
	hostnameMultipleDashPattern = regexp.MustCompile(`-+`)
)

// WireGuard overhead is 60 bytes (IPv4) or 80 bytes (IPv6) for outer headers.
// Default veth MTU allows for standard 1500-byte ethernet minus WireGuard overhead.
const defaultVethMTU = 1420

// PodManager manages Tailscale nodes for pods using LocalBackend + TUN.
type PodManager struct {
	stateDir    string
	clusterName string
	oauthMgr    *OAuthManager

	mu      sync.RWMutex
	servers map[string]*ManagedServer // containerID -> server
}

// ManagedServer represents a Tailscale node managed for a pod.
type ManagedServer struct {
	Backend       *ipnlocal.LocalBackend
	Engine        wgengine.Engine
	Sys           *tsd.System
	NetMon        *netmon.Monitor
	ContainerID   string
	PodName       string
	Namespace     string
	Hostname      string
	ClusterIP     string
	HostVethName  string
	TailscaleIPv4 netip.Addr
	TailscaleIPv6 netip.Addr
	CreatedAt     time.Time
}

// PodMetadata is persisted to disk for recovery.
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

// NewPodManager creates a new pod manager.
func NewPodManager(stateDir, clusterName string, oauthMgr *OAuthManager) *PodManager {
	return &PodManager{
		stateDir:    stateDir,
		clusterName: clusterName,
		oauthMgr:    oauthMgr,
		servers:     make(map[string]*ManagedServer),
	}
}

// stripKubernetesSuffixes removes common Kubernetes-generated suffixes from pod names.
// Examples:
//   - "nginx-deployment-7b5d9c6f8-xyz12" -> "nginx-deployment"
//   - "plex-7b5d9c6f8-abcde" -> "plex"
//   - "plex-statefulset-0" -> "plex-statefulset-0" (StatefulSet ordinals are kept)
//
// Note: StatefulSet ordinals (-0, -1, -2) are checked first and preserved.
// In real Kubernetes, StatefulSet pods are named "{name}-{ordinal}" without hashes.
func stripKubernetesSuffixes(podName string) string {
	// Don't strip if it looks like a StatefulSet pod (ends with ordinal like -0, -1, -2, etc.)
	// Check this first to avoid accidentally stripping StatefulSet ordinals
	if statefulSetOrdinalPattern.MatchString(podName) {
		return podName
	}
	
	// Pattern for ReplicaSet: {name}-{hash}-{random}
	if matches := replicaSetPattern.FindStringSubmatch(podName); len(matches) > 1 {
		return matches[1]
	}
	
	// Pattern for Deployment/ReplicaSet without random suffix: {name}-{hash}
	if matches := deploymentPattern.FindStringSubmatch(podName); len(matches) > 1 {
		return matches[1]
	}
	
	// If no pattern matches, return the original name
	return podName
}

// sanitizeHostname converts a string to a valid Tailscale hostname.
func sanitizeHostname(s string) string {
	s = strings.ToLower(s)
	s = hostnameInvalidCharsPattern.ReplaceAllString(s, "-")
	s = hostnameMultipleDashPattern.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}

// tunNameForContainer returns a TUN device name for the given container ID.
// Uses up to the first 8 characters, or the full ID if shorter.
func tunNameForContainer(containerID string) string {
	suffix := containerID
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return fmt.Sprintf("ts-%s", suffix)
}

// AddPod creates a new Tailscale node for a pod.
// Architecture:
//   - TUN device created in HOST namespace for wgengine
//   - veth pair bridges pod namespace to host
//   - Kernel IP forwarding routes between TUN and veth
func (pm *PodManager) AddPod(ctx context.Context, containerID, netnsPath, ifName, podName, namespace, clusterIP string) (*ManagedServer, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if srv, ok := pm.servers[containerID]; ok {
		log.Printf("Pod %s/%s already exists with Tailscale IP %s", namespace, podName, srv.TailscaleIPv4)
		return srv, nil
	}

	// Strip Kubernetes-generated suffixes for cleaner hostnames
	cleanPodName := stripKubernetesSuffixes(podName)
	hostname := sanitizeHostname(fmt.Sprintf("%s-%s-%s", pm.clusterName, namespace, cleanPodName))
	if cleanPodName != podName {
		log.Printf("Creating Tailscale node for pod %s/%s with hostname %s (cleaned: %s -> %s)", namespace, podName, hostname, podName, cleanPodName)
	} else {
		log.Printf("Creating Tailscale node for pod %s/%s with hostname %s", namespace, podName, hostname)
	}

	// Get auth key
	authKey, err := pm.oauthMgr.CreateAuthKey(ctx, podName, namespace)
	if err != nil {
		return nil, fmt.Errorf("creating auth key: %w", err)
	}
	log.Printf("Got auth key for %s/%s", namespace, podName)

	podStateDir := filepath.Join(pm.stateDir, "pods", containerID)
	if err := os.MkdirAll(podStateDir, 0700); err != nil {
		return nil, fmt.Errorf("creating state directory: %w", err)
	}

	logf := func(format string, args ...any) {
		log.Printf("[ts:%s] %s", hostname, fmt.Sprintf(format, args...))
	}

	// Create TUN device in HOST namespace
	tunName := tunNameForContainer(containerID)
	tunDev, actualTunName, err := tstun.New(logf, tunName)
	if err != nil {
		os.RemoveAll(podStateDir)
		return nil, fmt.Errorf("creating TUN device: %w", err)
	}
	log.Printf("Created TUN device %s in host namespace", actualTunName)

	// Bring up the TUN interface at the kernel level
	tunLink, err := netlink.LinkByName(actualTunName)
	if err != nil {
		tunDev.Close()
		os.RemoveAll(podStateDir)
		return nil, fmt.Errorf("getting TUN link: %w", err)
	}
	if err := netlink.LinkSetUp(tunLink); err != nil {
		tunDev.Close()
		os.RemoveAll(podStateDir)
		return nil, fmt.Errorf("bringing up TUN: %w", err)
	}
	log.Printf("TUN device %s is now UP", actualTunName)

	// Create system dependencies
	sys := tsd.NewSystem()

	dialer := &tsdial.Dialer{Logf: logf}
	dialer.SetBus(sys.Bus.Get())
	sys.Set(dialer)

	netMon, err := netmon.New(sys.Bus.Get(), logf)
	if err != nil {
		tunDev.Close()
		os.RemoveAll(podStateDir)
		return nil, fmt.Errorf("creating network monitor: %w", err)
	}
	sys.Set(netMon)

	// Create wgengine
	eng, err := wgengine.NewUserspaceEngine(logf, wgengine.Config{
		Tun:           tunDev,
		EventBus:      sys.Bus.Get(),
		NetMon:        netMon,
		Dialer:        dialer,
		SetSubsystem:  sys.Set,
		ControlKnobs:  sys.ControlKnobs(),
		HealthTracker: sys.HealthTracker.Get(),
		Metrics:       sys.UserMetricsRegistry(),
	})
	if err != nil {
		netMon.Close()
		tunDev.Close()
		os.RemoveAll(podStateDir)
		return nil, fmt.Errorf("creating wgengine: %w", err)
	}
	sys.Set(eng)
	sys.HealthTracker.Get().SetMetricsRegistry(sys.UserMetricsRegistry())

	// Create netstack (required but we'll use kernel routing)
	nsImpl, err := netstack.Create(logf, sys.Tun.Get(), eng, sys.MagicSock.Get(), dialer, sys.DNSManager.Get(), sys.ProxyMapper())
	if err != nil {
		eng.Close()
		netMon.Close()
		tunDev.Close()
		os.RemoveAll(podStateDir)
		return nil, fmt.Errorf("creating netstack: %w", err)
	}
	sys.Tun.Get().Start()
	sys.Set(nsImpl)
	nsImpl.ProcessLocalIPs = false
	nsImpl.ProcessSubnets = false

	// Use FileStore to persist node state (including node key) for recovery
	stateStorePath := filepath.Join(podStateDir, "tailscale.state")
	stateStore, err := store.NewFileStore(logf, stateStorePath)
	if err != nil {
		nsImpl.Close()
		eng.Close()
		netMon.Close()
		os.RemoveAll(podStateDir)
		return nil, fmt.Errorf("creating state store: %w", err)
	}
	sys.Set(stateStore)

	logID, err := logid.NewPrivateID()
	if err != nil {
		nsImpl.Close()
		eng.Close()
		netMon.Close()
		os.RemoveAll(podStateDir)
		return nil, fmt.Errorf("creating log ID: %w", err)
	}

	// Don't use LoginEphemeral - we want nodes to persist for recovery.
	// Cleanup happens explicitly via CNI DEL -> DeletePod.
	loginFlags := controlclient.LocalBackendStartKeyOSNeutral
	lb, err := ipnlocal.NewLocalBackend(logf, logID.Public(), sys, loginFlags)
	if err != nil {
		nsImpl.Close()
		eng.Close()
		netMon.Close()
		os.RemoveAll(podStateDir)
		return nil, fmt.Errorf("creating LocalBackend: %w", err)
	}
	lb.SetVarRoot(podStateDir)

	if err := nsImpl.Start(lb); err != nil {
		lb.Shutdown()
		nsImpl.Close()
		eng.Close()
		netMon.Close()
		os.RemoveAll(podStateDir)
		return nil, fmt.Errorf("starting netstack: %w", err)
	}

	prefs := ipn.NewPrefs()
	prefs.Hostname = hostname
	prefs.WantRunning = true
	prefs.ControlURL = ipn.DefaultControlURL

	if err := lb.Start(ipn.Options{
		AuthKey:     authKey,
		UpdatePrefs: prefs,
	}); err != nil {
		lb.Shutdown()
		nsImpl.Close()
		eng.Close()
		netMon.Close()
		os.RemoveAll(podStateDir)
		return nil, fmt.Errorf("starting LocalBackend: %w", err)
	}

	// If state is NeedsLogin, kick off the login process
	if st := lb.State(); st == ipn.NeedsLogin {
		log.Printf("State is NeedsLogin, calling StartLoginInteractive")
		if err := lb.StartLoginInteractive(ctx); err != nil {
			lb.Shutdown()
			nsImpl.Close()
			eng.Close()
			netMon.Close()
			os.RemoveAll(podStateDir)
			return nil, fmt.Errorf("starting login: %w", err)
		}
	}

	// Wait for Tailscale IP
	ctxWithTimeout, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var tailscaleIPv4, tailscaleIPv6 netip.Addr
	for {
		status := lb.Status()
		if status.BackendState == "Running" && len(status.TailscaleIPs) > 0 {
			for _, ip := range status.TailscaleIPs {
				if ip.Is4() && !tailscaleIPv4.IsValid() {
					tailscaleIPv4 = ip
				} else if ip.Is6() && !tailscaleIPv6.IsValid() {
					tailscaleIPv6 = ip
				}
			}
			if tailscaleIPv4.IsValid() {
				break
			}
		}

		select {
		case <-ctxWithTimeout.Done():
			lb.Shutdown()
			nsImpl.Close()
			eng.Close()
			netMon.Close()
			os.RemoveAll(podStateDir)
			return nil, fmt.Errorf("timeout waiting for Tailscale IP (state: %s)", status.BackendState)
		case <-time.After(500 * time.Millisecond):
		}
	}

	log.Printf("Pod %s/%s connected to Tailscale with IP %s", namespace, podName, tailscaleIPv4)

	// Now set up veth bridging to pod namespace
	hostVethName, err := setupVethBridge(netnsPath, ifName, actualTunName, tailscaleIPv4, defaultVethMTU)
	if err != nil {
		lb.Shutdown()
		nsImpl.Close()
		eng.Close()
		netMon.Close()
		os.RemoveAll(podStateDir)
		return nil, fmt.Errorf("setting up veth bridge: %w", err)
	}

	managed := &ManagedServer{
		Backend:       lb,
		Engine:        eng,
		Sys:           sys,
		NetMon:        netMon,
		ContainerID:   containerID,
		PodName:       podName,
		Namespace:     namespace,
		Hostname:      hostname,
		ClusterIP:     clusterIP,
		HostVethName:  hostVethName,
		TailscaleIPv4: tailscaleIPv4,
		TailscaleIPv6: tailscaleIPv6,
		CreatedAt:     time.Now(),
	}

	pm.servers[containerID] = managed

	if err := pm.saveMetadata(containerID, managed, netnsPath); err != nil {
		log.Printf("Warning: failed to save metadata for %s: %v", containerID, err)
	}

	return managed, nil
}

// setupVethBridge creates veth pair and configures routing between TUN and pod.
func setupVethBridge(netnsPath, podIfName, tunName string, tailscaleIP netip.Addr, mtu int) (string, error) {
	podNS, err := ns.GetNS(netnsPath)
	if err != nil {
		return "", fmt.Errorf("getting netns: %w", err)
	}
	defer podNS.Close()

	// Generate cryptographically random veth name to avoid collisions
	var randBytes [4]byte
	if _, err := rand.Read(randBytes[:]); err != nil {
		return "", fmt.Errorf("generating random veth name: %w", err)
	}
	hostVethName := "veth" + hex.EncodeToString(randBytes[:])

	// Create veth pair in pod namespace
	err = podNS.Do(func(hostNS ns.NetNS) error {
		veth := &netlink.Veth{
			LinkAttrs: netlink.LinkAttrs{
				Name: podIfName,
				MTU:  mtu,
			},
			PeerName: hostVethName,
		}

		if err := netlink.LinkAdd(veth); err != nil {
			return fmt.Errorf("creating veth pair: %w", err)
		}

		// Get interfaces
		podLink, err := netlink.LinkByName(podIfName)
		if err != nil {
			return fmt.Errorf("getting pod interface: %w", err)
		}

		hostLink, err := netlink.LinkByName(hostVethName)
		if err != nil {
			return fmt.Errorf("getting host interface: %w", err)
		}

		// Move host veth to host namespace
		if err := netlink.LinkSetNsFd(hostLink, int(hostNS.Fd())); err != nil {
			return fmt.Errorf("moving host veth: %w", err)
		}

		// Configure pod interface with Tailscale IP
		addr := &netlink.Addr{
			IPNet: &net.IPNet{
				IP:   tailscaleIP.AsSlice(),
				Mask: net.CIDRMask(32, 32),
			},
		}
		if err := netlink.AddrAdd(podLink, addr); err != nil {
			return fmt.Errorf("adding IP to pod interface: %w", err)
		}

		if err := netlink.LinkSetUp(podLink); err != nil {
			return fmt.Errorf("bringing up pod interface: %w", err)
		}

		// Route Tailscale CGNAT range via this interface
		_, tailscaleCIDR, _ := net.ParseCIDR("100.64.0.0/10")
		route := &netlink.Route{
			LinkIndex: podLink.Attrs().Index,
			Dst:       tailscaleCIDR,
			Scope:     netlink.SCOPE_LINK,
		}
		if err := netlink.RouteAdd(route); err != nil {
			return fmt.Errorf("adding Tailscale route: %w", err)
		}

		return nil
	})
	if err != nil {
		return "", err
	}

	// Configure host side
	hostLink, err := netlink.LinkByName(hostVethName)
	if err != nil {
		return "", fmt.Errorf("getting host veth: %w", err)
	}

	if err := netlink.LinkSetUp(hostLink); err != nil {
		return "", fmt.Errorf("bringing up host veth: %w", err)
	}

	// Route to pod's Tailscale IP via host veth
	podRoute := &netlink.Route{
		LinkIndex: hostLink.Attrs().Index,
		Dst: &net.IPNet{
			IP:   tailscaleIP.AsSlice(),
			Mask: net.CIDRMask(32, 32),
		},
		Scope: netlink.SCOPE_LINK,
	}
	if err := netlink.RouteAdd(podRoute); err != nil {
		log.Printf("Warning: failed to add route to pod: %v", err)
	}

	// Enable proxy ARP on host veth so it responds to ARP for Tailscale IPs
	proxyArpPath := fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/proxy_arp", hostVethName)
	if err := os.WriteFile(proxyArpPath, []byte("1"), 0644); err != nil {
		log.Printf("Warning: failed to enable proxy ARP: %v", err)
	}

	// Enable IP forwarding
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644); err != nil {
		log.Printf("Warning: failed to enable IP forwarding: %v", err)
	}

	// Add route for Tailscale CGNAT range to go via TUN
	// This allows traffic from pod (arriving via veth) to be forwarded to TUN
	tunLink, err := netlink.LinkByName(tunName)
	if err != nil {
		return "", fmt.Errorf("getting TUN link for routing: %w", err)
	}
	_, tailscaleCIDR, _ := net.ParseCIDR("100.64.0.0/10")
	tunRoute := &netlink.Route{
		LinkIndex: tunLink.Attrs().Index,
		Dst:       tailscaleCIDR,
		Scope:     netlink.SCOPE_LINK,
	}
	if err := netlink.RouteAdd(tunRoute); err != nil {
		// Might already exist from a previous pod
		log.Printf("Note: adding Tailscale route to TUN: %v", err)
	}

	log.Printf("Set up veth bridge: %s <-> %s (TUN: %s)", podIfName, hostVethName, tunName)

	return hostVethName, nil
}

// DeletePod removes a pod's Tailscale node.
func (pm *PodManager) DeletePod(containerID string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	managed, ok := pm.servers[containerID]
	if !ok {
		log.Printf("Pod %s not found, already cleaned up", containerID)
		return nil
	}

	log.Printf("Deleting Tailscale node for pod %s/%s", managed.Namespace, managed.PodName)

	managed.Backend.Shutdown()
	managed.Engine.Close()
	if managed.NetMon != nil {
		managed.NetMon.Close()
	}

	// Clean up host veth (pod side gets cleaned up with namespace)
	if managed.HostVethName != "" {
		if link, err := netlink.LinkByName(managed.HostVethName); err == nil {
			netlink.LinkDel(link)
		}
	}

	podStateDir := filepath.Join(pm.stateDir, "pods", containerID)
	os.RemoveAll(podStateDir)

	delete(pm.servers, containerID)
	return nil
}

// CheckPod verifies a pod's Tailscale connection is healthy.
func (pm *PodManager) CheckPod(containerID string) (bool, string, error) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	managed, ok := pm.servers[containerID]
	if !ok {
		return false, "pod not found", nil
	}

	status := managed.Backend.Status()
	if status.BackendState != "Running" {
		return false, fmt.Sprintf("backend state is %s", status.BackendState), nil
	}

	return true, "healthy", nil
}

// GetPod returns the managed server for a container ID.
func (pm *PodManager) GetPod(containerID string) (*ManagedServer, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	srv, ok := pm.servers[containerID]
	return srv, ok
}

// GetPodByName returns the managed server for a pod by namespace and name.
func (pm *PodManager) GetPodByName(namespace, name string) (*ManagedServer, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for _, srv := range pm.servers {
		if srv.Namespace == namespace && srv.PodName == name {
			return srv, true
		}
	}
	return nil, false
}

// saveMetadata persists pod metadata to disk.
func (pm *PodManager) saveMetadata(containerID string, managed *ManagedServer, netnsPath string) error {
	meta := PodMetadata{
		ContainerID:   managed.ContainerID,
		PodName:       managed.PodName,
		Namespace:     managed.Namespace,
		Hostname:      managed.Hostname,
		TailscaleIPv4: managed.TailscaleIPv4.String(),
		CreatedAt:     managed.CreatedAt,
		NetnsPath:     netnsPath,
		HostVethName:  managed.HostVethName,
		ClusterIP:     managed.ClusterIP,
	}
	if managed.TailscaleIPv6.IsValid() {
		meta.TailscaleIPv6 = managed.TailscaleIPv6.String()
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}

	metaPath := filepath.Join(pm.stateDir, "pods", containerID, "metadata.json")
	return os.WriteFile(metaPath, data, 0600)
}

// netnsExists checks if a network namespace path is still valid.
func netnsExists(netnsPath string) bool {
	if netnsPath == "" {
		return false
	}
	_, err := os.Stat(netnsPath)
	return err == nil
}

// getOrCreateTUN returns a new TUN device, deleting any existing one first.
func (pm *PodManager) getOrCreateTUN(logf logger.Logf, tunName string) (tun.Device, string, error) {
	// Check if TUN device already exists and delete it
	// (we can't reuse the file descriptor from a previous daemon run)
	if link, err := netlink.LinkByName(tunName); err == nil {
		log.Printf("Deleting existing TUN device %s", tunName)
		if err := netlink.LinkDel(link); err != nil {
			return nil, "", fmt.Errorf("deleting existing TUN: %w", err)
		}
	}

	// Create fresh TUN device
	tunDev, actualTunName, err := tstun.New(logf, tunName)
	if err != nil {
		return nil, "", fmt.Errorf("creating TUN device: %w", err)
	}

	// Bring it up
	tunLink, err := netlink.LinkByName(actualTunName)
	if err != nil {
		tunDev.Close()
		return nil, "", fmt.Errorf("getting TUN link: %w", err)
	}
	if err := netlink.LinkSetUp(tunLink); err != nil {
		tunDev.Close()
		return nil, "", fmt.Errorf("bringing up TUN: %w", err)
	}

	return tunDev, actualTunName, nil
}

// ensureRoutes verifies and fixes routes for an existing veth setup.
func (pm *PodManager) ensureRoutes(tunName, vethName string, tailscaleIP netip.Addr) error {
	// Route to pod's Tailscale IP via veth
	vethLink, err := netlink.LinkByName(vethName)
	if err != nil {
		return fmt.Errorf("getting veth: %w", err)
	}

	podRoute := &netlink.Route{
		LinkIndex: vethLink.Attrs().Index,
		Dst: &net.IPNet{
			IP:   tailscaleIP.AsSlice(),
			Mask: net.CIDRMask(32, 32),
		},
		Scope: netlink.SCOPE_LINK,
	}
	// RouteReplace is idempotent for existing routes
	if err := netlink.RouteReplace(podRoute); err != nil {
		log.Printf("Warning: failed to replace pod route: %v", err)
	}

	// Route for Tailscale CGNAT to TUN
	tunLink, err := netlink.LinkByName(tunName)
	if err != nil {
		return fmt.Errorf("getting TUN: %w", err)
	}
	_, tailscaleCIDR, _ := net.ParseCIDR("100.64.0.0/10")
	tunRoute := &netlink.Route{
		LinkIndex: tunLink.Attrs().Index,
		Dst:       tailscaleCIDR,
		Scope:     netlink.SCOPE_LINK,
	}
	if err := netlink.RouteReplace(tunRoute); err != nil {
		log.Printf("Warning: failed to replace TUN route: %v", err)
	}

	return nil
}

// updatePodIP updates the pod's interface IP when Tailscale assigns a different IP on recovery.
// This modifies the pod's ts0 interface in-place without restarting the pod.
func (pm *PodManager) updatePodIP(netnsPath string, oldIP, newIP netip.Addr) error {
	if oldIP == newIP {
		return nil // No change needed
	}

	podNS, err := ns.GetNS(netnsPath)
	if err != nil {
		return fmt.Errorf("getting netns: %w", err)
	}
	defer podNS.Close()

	err = podNS.Do(func(_ ns.NetNS) error {
		// Find the pod's Tailscale interface (ts0)
		podLink, err := netlink.LinkByName("ts0")
		if err != nil {
			return fmt.Errorf("getting ts0 interface: %w", err)
		}

		// Remove the old IP
		oldAddr := &netlink.Addr{
			IPNet: &net.IPNet{
				IP:   oldIP.AsSlice(),
				Mask: net.CIDRMask(32, 32),
			},
		}
		if err := netlink.AddrDel(podLink, oldAddr); err != nil {
			// Log but continue - might already be gone
			log.Printf("Note: failed to remove old IP %s from ts0: %v", oldIP, err)
		}

		// Add the new IP
		newAddr := &netlink.Addr{
			IPNet: &net.IPNet{
				IP:   newIP.AsSlice(),
				Mask: net.CIDRMask(32, 32),
			},
		}
		if err := netlink.AddrAdd(podLink, newAddr); err != nil {
			return fmt.Errorf("adding new IP %s to ts0: %w", newIP, err)
		}

		log.Printf("Updated pod interface ts0: %s -> %s", oldIP, newIP)
		return nil
	})

	return err
}

// updateHostRoute updates the host-side route to the pod when its IP changes.
func (pm *PodManager) updateHostRoute(vethName string, oldIP, newIP netip.Addr) error {
	if oldIP == newIP {
		return nil // No change needed
	}

	vethLink, err := netlink.LinkByName(vethName)
	if err != nil {
		return fmt.Errorf("getting veth %s: %w", vethName, err)
	}

	// Delete old route
	oldRoute := &netlink.Route{
		LinkIndex: vethLink.Attrs().Index,
		Dst: &net.IPNet{
			IP:   oldIP.AsSlice(),
			Mask: net.CIDRMask(32, 32),
		},
		Scope: netlink.SCOPE_LINK,
	}
	if err := netlink.RouteDel(oldRoute); err != nil {
		log.Printf("Note: failed to delete old route to %s: %v", oldIP, err)
	}

	// Add new route
	newRoute := &netlink.Route{
		LinkIndex: vethLink.Attrs().Index,
		Dst: &net.IPNet{
			IP:   newIP.AsSlice(),
			Mask: net.CIDRMask(32, 32),
		},
		Scope: netlink.SCOPE_LINK,
	}
	if err := netlink.RouteAdd(newRoute); err != nil {
		return fmt.Errorf("adding route to %s: %w", newIP, err)
	}

	log.Printf("Updated host route: %s -> %s via %s", oldIP, newIP, vethName)
	return nil
}

// reconnectVethBridge verifies and reconnects the veth bridge.
func (pm *PodManager) reconnectVethBridge(netnsPath, tunName, existingVethName string, tailscaleIP netip.Addr) (string, error) {
	// Check if existing veth still exists on host side
	if existingVethName != "" {
		if _, err := netlink.LinkByName(existingVethName); err == nil {
			// Veth exists - just ensure routes are correct
			log.Printf("Reusing existing veth %s", existingVethName)
			if err := pm.ensureRoutes(tunName, existingVethName, tailscaleIP); err != nil {
				log.Printf("Warning: failed to verify routes: %v", err)
			}
			return existingVethName, nil
		}
	}

	// Veth doesn't exist - need to recreate
	log.Printf("Veth %s not found, recreating veth bridge", existingVethName)
	return setupVethBridge(netnsPath, "ts0", tunName, tailscaleIP, defaultVethMTU)
}

// cleanupOrphanedPod removes resources for a pod that no longer exists.
// Must be called with pm.mu held.
func (pm *PodManager) cleanupOrphanedPod(containerID, hostVethName string) {
	log.Printf("Cleaning up orphaned pod %s", containerID)

	// Delete TUN device
	tunName := tunNameForContainer(containerID)
	if link, err := netlink.LinkByName(tunName); err == nil {
		if err := netlink.LinkDel(link); err != nil {
			log.Printf("Warning: failed to delete TUN %s: %v", tunName, err)
		} else {
			log.Printf("Deleted orphaned TUN %s", tunName)
		}
	}

	// Delete host veth
	if hostVethName != "" {
		if link, err := netlink.LinkByName(hostVethName); err == nil {
			if err := netlink.LinkDel(link); err != nil {
				log.Printf("Warning: failed to delete veth %s: %v", hostVethName, err)
			} else {
				log.Printf("Deleted orphaned veth %s", hostVethName)
			}
		}
	}

	// Remove state directory
	podStateDir := filepath.Join(pm.stateDir, "pods", containerID)
	if err := os.RemoveAll(podStateDir); err != nil {
		log.Printf("Warning: failed to remove state dir %s: %v", podStateDir, err)
	}
}

// CleanupOrphanedResources scans for TUN devices not associated with known pods.
func (pm *PodManager) CleanupOrphanedResources() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	log.Printf("Scanning for orphaned network resources...")

	// Build set of known TUN names
	knownTUNs := make(map[string]bool)
	for containerID := range pm.servers {
		knownTUNs[tunNameForContainer(containerID)] = true
	}

	// Enumerate all network interfaces
	links, err := netlink.LinkList()
	if err != nil {
		log.Printf("Warning: failed to list network interfaces: %v", err)
		return
	}

	for _, link := range links {
		name := link.Attrs().Name

		// Check for orphaned TUN devices (ts-* pattern)
		if strings.HasPrefix(name, "ts-") && !knownTUNs[name] {
			log.Printf("Found orphaned TUN: %s", name)
			if err := netlink.LinkDel(link); err != nil {
				log.Printf("Warning: failed to delete orphaned TUN %s: %v", name, err)
			} else {
				log.Printf("Deleted orphaned TUN %s", name)
			}
		}
	}
}

// loadMetadata loads pod metadata from disk.
func (pm *PodManager) loadMetadata(containerID string) (*PodMetadata, error) {
	metaPath := filepath.Join(pm.stateDir, "pods", containerID, "metadata.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("reading metadata: %w", err)
	}

	var meta PodMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parsing metadata: %w", err)
	}
	return &meta, nil
}

// recoverPodBackend creates a new LocalBackend using persisted state.
// This preserves the node key, ensuring the same Tailscale IP.
func (pm *PodManager) recoverPodBackend(ctx context.Context, containerID string, meta *PodMetadata, expectedIP netip.Addr) (*ManagedServer, error) {
	podStateDir := filepath.Join(pm.stateDir, "pods", containerID)

	logf := func(format string, args ...any) {
		log.Printf("[ts:%s] %s", meta.Hostname, fmt.Sprintf(format, args...))
	}

	// Create TUN device (deletes any existing one first)
	tunName := tunNameForContainer(containerID)
	tunDev, actualTunName, err := pm.getOrCreateTUN(logf, tunName)
	if err != nil {
		return nil, fmt.Errorf("getting TUN: %w", err)
	}

	// Create system dependencies (same as AddPod)
	sys := tsd.NewSystem()
	dialer := &tsdial.Dialer{Logf: logf}
	dialer.SetBus(sys.Bus.Get())
	sys.Set(dialer)

	netMon, err := netmon.New(sys.Bus.Get(), logf)
	if err != nil {
		tunDev.Close()
		return nil, fmt.Errorf("creating network monitor: %w", err)
	}
	sys.Set(netMon)

	// Create wgengine
	eng, err := wgengine.NewUserspaceEngine(logf, wgengine.Config{
		Tun:           tunDev,
		EventBus:      sys.Bus.Get(),
		NetMon:        netMon,
		Dialer:        dialer,
		SetSubsystem:  sys.Set,
		ControlKnobs:  sys.ControlKnobs(),
		HealthTracker: sys.HealthTracker.Get(),
		Metrics:       sys.UserMetricsRegistry(),
	})
	if err != nil {
		netMon.Close()
		tunDev.Close()
		return nil, fmt.Errorf("creating wgengine: %w", err)
	}
	sys.Set(eng)
	sys.HealthTracker.Get().SetMetricsRegistry(sys.UserMetricsRegistry())

	// Create netstack
	nsImpl, err := netstack.Create(logf, sys.Tun.Get(), eng, sys.MagicSock.Get(), dialer, sys.DNSManager.Get(), sys.ProxyMapper())
	if err != nil {
		eng.Close()
		netMon.Close()
		tunDev.Close()
		return nil, fmt.Errorf("creating netstack: %w", err)
	}
	sys.Tun.Get().Start()
	sys.Set(nsImpl)
	nsImpl.ProcessLocalIPs = false
	nsImpl.ProcessSubnets = false

	// Load existing FileStore (preserves node key)
	stateStorePath := filepath.Join(podStateDir, "tailscale.state")
	stateStore, err := store.NewFileStore(logf, stateStorePath)
	if err != nil {
		nsImpl.Close()
		eng.Close()
		netMon.Close()
		tunDev.Close()
		return nil, fmt.Errorf("loading state store: %w", err)
	}
	sys.Set(stateStore)

	logID, err := logid.NewPrivateID()
	if err != nil {
		nsImpl.Close()
		eng.Close()
		netMon.Close()
		tunDev.Close()
		return nil, fmt.Errorf("creating log ID: %w", err)
	}

	// Do NOT use LoginEphemeral - we want to reuse existing identity
	loginFlags := controlclient.LocalBackendStartKeyOSNeutral
	lb, err := ipnlocal.NewLocalBackend(logf, logID.Public(), sys, loginFlags)
	if err != nil {
		nsImpl.Close()
		eng.Close()
		netMon.Close()
		tunDev.Close()
		return nil, fmt.Errorf("creating LocalBackend: %w", err)
	}
	lb.SetVarRoot(podStateDir)

	if err := nsImpl.Start(lb); err != nil {
		lb.Shutdown()
		nsImpl.Close()
		eng.Close()
		netMon.Close()
		tunDev.Close()
		return nil, fmt.Errorf("starting netstack: %w", err)
	}

	prefs := ipn.NewPrefs()
	prefs.Hostname = meta.Hostname
	prefs.WantRunning = true
	prefs.ControlURL = ipn.DefaultControlURL

	// Start with persisted state - the FileStore contains the node key which
	// determines our Tailscale IP. We do NOT create a new auth key here.
	if err := lb.Start(ipn.Options{
		UpdatePrefs: prefs,
	}); err != nil {
		lb.Shutdown()
		nsImpl.Close()
		eng.Close()
		netMon.Close()
		tunDev.Close()
		return nil, fmt.Errorf("starting LocalBackend: %w", err)
	}

	// If NeedsLogin, use StartLoginInteractive which reconnects with the
	// persisted node key - preserving our Tailscale IP.
	if st := lb.State(); st == ipn.NeedsLogin {
		log.Printf("Pod %s/%s reconnecting with persisted identity...",
			meta.Namespace, meta.PodName)
		if err := lb.StartLoginInteractive(ctx); err != nil {
			lb.Shutdown()
			nsImpl.Close()
			eng.Close()
			netMon.Close()
			tunDev.Close()
			return nil, fmt.Errorf("reconnecting with persisted identity: %w", err)
		}
	}

	// Wait for connection
	ctxWithTimeout, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var actualIP netip.Addr
	for {
		status := lb.Status()
		if status.BackendState == "Running" && len(status.TailscaleIPs) > 0 {
			for _, ip := range status.TailscaleIPs {
				if ip.Is4() {
					actualIP = ip
					break
				}
			}
			if actualIP.IsValid() {
				break
			}
		}

		select {
		case <-ctxWithTimeout.Done():
			lb.Shutdown()
			nsImpl.Close()
			eng.Close()
			netMon.Close()
			tunDev.Close()
			return nil, fmt.Errorf("timeout waiting for Tailscale connection")
		case <-time.After(500 * time.Millisecond):
		}
	}

	// Handle IP change if needed
	if actualIP != expectedIP {
		log.Printf("Tailscale IP changed for pod %s/%s: %s -> %s",
			meta.Namespace, meta.PodName, expectedIP, actualIP)

		// Update the pod's interface IP in-place
		if err := pm.updatePodIP(meta.NetnsPath, expectedIP, actualIP); err != nil {
			log.Printf("Warning: failed to update pod IP: %v", err)
			// Continue anyway - might need manual intervention
		}

		// Update host-side route if veth exists
		if meta.HostVethName != "" {
			if err := pm.updateHostRoute(meta.HostVethName, expectedIP, actualIP); err != nil {
				log.Printf("Warning: failed to update host route: %v", err)
			}
		}

		// Update metadata with new IP
		meta.TailscaleIPv4 = actualIP.String()
	}

	// Reconnect veth bridge if needed (handles any remaining route setup)
	hostVethName, err := pm.reconnectVethBridge(meta.NetnsPath, actualTunName, meta.HostVethName, actualIP)
	if err != nil {
		lb.Shutdown()
		nsImpl.Close()
		eng.Close()
		netMon.Close()
		tunDev.Close()
		return nil, fmt.Errorf("reconnecting veth bridge: %w", err)
	}

	var tailscaleIPv6 netip.Addr
	status := lb.Status()
	for _, ip := range status.TailscaleIPs {
		if ip.Is6() {
			tailscaleIPv6 = ip
			break
		}
	}

	managed := &ManagedServer{
		Backend:       lb,
		Engine:        eng,
		Sys:           sys,
		NetMon:        netMon,
		ContainerID:   containerID,
		PodName:       meta.PodName,
		Namespace:     meta.Namespace,
		Hostname:      meta.Hostname,
		ClusterIP:     meta.ClusterIP,
		HostVethName:  hostVethName,
		TailscaleIPv4: actualIP,
		TailscaleIPv6: tailscaleIPv6,
		CreatedAt:     meta.CreatedAt,
	}

	return managed, nil
}

// recoverPod attempts to recover a single pod from persisted state.
// Must be called with pm.mu held.
func (pm *PodManager) recoverPod(ctx context.Context, containerID string) error {
	// Load metadata
	meta, err := pm.loadMetadata(containerID)
	if err != nil {
		return fmt.Errorf("loading metadata: %w", err)
	}

	// Check if netns still exists
	if !netnsExists(meta.NetnsPath) {
		log.Printf("Pod %s/%s netns %s no longer exists, cleaning up",
			meta.Namespace, meta.PodName, meta.NetnsPath)
		pm.cleanupOrphanedPod(containerID, meta.HostVethName)
		return nil
	}

	// Check if state file exists (needed for IP stability)
	stateStorePath := filepath.Join(pm.stateDir, "pods", containerID, "tailscale.state")
	if _, err := os.Stat(stateStorePath); os.IsNotExist(err) {
		log.Printf("Pod %s/%s has no state file, cannot recover with same IP, cleaning up",
			meta.Namespace, meta.PodName)
		pm.cleanupOrphanedPod(containerID, meta.HostVethName)
		return nil
	}

	log.Printf("Recovering pod %s/%s (container %s)",
		meta.Namespace, meta.PodName, containerID)

	// Parse stored Tailscale IP
	tailscaleIPv4, err := netip.ParseAddr(meta.TailscaleIPv4)
	if err != nil {
		return fmt.Errorf("parsing stored Tailscale IP: %w", err)
	}

	// Recover with same state (node key persisted in FileStore)
	managed, err := pm.recoverPodBackend(ctx, containerID, meta, tailscaleIPv4)
	if err != nil {
		return fmt.Errorf("recovering backend: %w", err)
	}

	pm.servers[containerID] = managed

	// Update persisted metadata if IP changed
	if managed.TailscaleIPv4 != tailscaleIPv4 {
		log.Printf("Updating persisted metadata with new IP %s", managed.TailscaleIPv4)
		if err := pm.saveMetadata(containerID, managed, meta.NetnsPath); err != nil {
			log.Printf("Warning: failed to update metadata: %v", err)
		}
	}

	log.Printf("Recovered pod %s/%s with IP %s",
		meta.Namespace, meta.PodName, managed.TailscaleIPv4)

	return nil
}

// RecoverPods scans stored metadata and recovers pods that still exist.
// Returns number of recovered pods and list of errors encountered.
func (pm *PodManager) RecoverPods(ctx context.Context) (int, []error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	var recovered int
	var errors []error

	podsDir := filepath.Join(pm.stateDir, "pods")
	entries, err := os.ReadDir(podsDir)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("No pods directory, nothing to recover")
			return 0, nil
		}
		return 0, []error{fmt.Errorf("reading pods directory: %w", err)}
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		containerID := entry.Name()

		if err := pm.recoverPod(ctx, containerID); err != nil {
			log.Printf("Failed to recover pod %s: %v", containerID, err)
			errors = append(errors, fmt.Errorf("pod %s: %w", containerID, err))
			// Clean up this pod's resources on failure
			meta, _ := pm.loadMetadata(containerID)
			vethName := ""
			if meta != nil {
				vethName = meta.HostVethName
			}
			pm.cleanupOrphanedPod(containerID, vethName)
		} else {
			recovered++
		}
	}

	return recovered, errors
}

// Close shuts down all managed servers.
func (pm *PodManager) Close() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for containerID, managed := range pm.servers {
		log.Printf("Closing Tailscale node for %s", containerID)
		managed.Backend.Shutdown()
		managed.Engine.Close()
		if managed.NetMon != nil {
			managed.NetMon.Close()
		}
	}
	pm.servers = make(map[string]*ManagedServer)
	return nil
}

// Ensure tun.Device is imported
var _ tun.Device
