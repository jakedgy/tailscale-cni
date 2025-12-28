//go:build linux

package daemon

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
)

// configureTailscaleRoutes sets up routing for the Tailscale TUN device.
// This is called inside the pod network namespace.
func configureTailscaleRoutes(ifName string, tailscaleIP netip.Addr) error {
	// Get the TUN interface
	link, err := netlink.LinkByName(ifName)
	if err != nil {
		return fmt.Errorf("getting interface %s: %w", ifName, err)
	}

	// Assign Tailscale IP to the interface (/32 for point-to-point)
	addr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   tailscaleIP.AsSlice(),
			Mask: net.CIDRMask(32, 32),
		},
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return fmt.Errorf("adding IP %s to %s: %w", tailscaleIP, ifName, err)
	}

	// Bring up the interface (TUN should already be up, but be safe)
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("bringing up %s: %w", ifName, err)
	}

	// Add route for Tailscale CGNAT range (100.64.0.0/10)
	// This ensures traffic to other Tailscale nodes goes through this interface
	_, tailscaleCIDR, _ := net.ParseCIDR("100.64.0.0/10")
	tailscaleRoute := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       tailscaleCIDR,
		Scope:     netlink.SCOPE_LINK,
	}
	if err := netlink.RouteAdd(tailscaleRoute); err != nil {
		return fmt.Errorf("adding Tailscale route: %w", err)
	}

	return nil
}
