package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/cni/pkg/version"
	pb "github.com/jakedgy/tailscale-cni/pkg/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// NetConf represents the CNI network configuration.
type NetConf struct {
	types.NetConf
	DaemonSocket string `json:"daemonSocket"`
	ClusterName  string `json:"clusterName"`
}

// K8sArgs represents Kubernetes-specific CNI arguments.
type K8sArgs struct {
	types.CommonArgs
	K8S_POD_NAME               types.UnmarshallableString
	K8S_POD_NAMESPACE          types.UnmarshallableString
	K8S_POD_INFRA_CONTAINER_ID types.UnmarshallableString
	K8S_POD_UID                types.UnmarshallableString
}

func main() {
	skel.PluginMainFuncs(skel.CNIFuncs{
		Add:   cmdAdd,
		Del:   cmdDel,
		Check: cmdCheck,
	}, version.PluginSupports("0.3.0", "0.3.1", "0.4.0", "1.0.0"), "tailscale-cni")
}

func loadConf(bytes []byte) (*NetConf, error) {
	conf := &NetConf{}
	if err := json.Unmarshal(bytes, conf); err != nil {
		return nil, fmt.Errorf("failed to parse network config: %w", err)
	}
	if conf.DaemonSocket == "" {
		conf.DaemonSocket = "/var/run/tailscale-cni/daemon.sock"
	}
	// Parse the previous result from raw JSON
	if err := version.ParsePrevResult(&conf.NetConf); err != nil {
		return nil, fmt.Errorf("failed to parse prevResult: %w", err)
	}
	return conf, nil
}

func parseK8sArgs(args string) (*K8sArgs, error) {
	k8sArgs := &K8sArgs{}
	if err := types.LoadArgs(args, k8sArgs); err != nil {
		return nil, fmt.Errorf("failed to parse CNI_ARGS: %w", err)
	}
	return k8sArgs, nil
}

func connectToDaemon(socketPath string) (pb.TailscaleCNIClient, *grpc.ClientConn, error) {
	// Retry connection with exponential backoff
	// This handles the case where pods start before the daemon is ready
	var conn *grpc.ClientConn
	var err error

	maxRetries := 10
	baseDelay := 500 * time.Millisecond

	for attempt := 0; attempt < maxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

		conn, err = grpc.DialContext(ctx, "unix://"+socketPath,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(),
		)
		cancel()

		if err == nil {
			return pb.NewTailscaleCNIClient(conn), conn, nil
		}

		// Check if socket exists - if not, daemon isn't ready yet
		if _, statErr := os.Stat(socketPath); os.IsNotExist(statErr) {
			// Socket doesn't exist, wait and retry
			delay := baseDelay * time.Duration(1<<uint(attempt))
			if delay > 10*time.Second {
				delay = 10 * time.Second
			}
			time.Sleep(delay)
			continue
		}

		// Socket exists but connection failed - wait shorter and retry
		delay := baseDelay * time.Duration(1<<uint(attempt))
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
		time.Sleep(delay)
	}

	return nil, nil, fmt.Errorf("connecting to daemon at %s after %d attempts: %w", socketPath, maxRetries, err)
}

func cmdAdd(args *skel.CmdArgs) error {
	conf, err := loadConf(args.StdinData)
	if err != nil {
		return err
	}

	k8sArgs, err := parseK8sArgs(args.Args)
	if err != nil {
		return err
	}

	// Get cluster IP from previous CNI result (chaining)
	var clusterIP string
	if conf.PrevResult != nil {
		prevResult, err := current.GetResult(conf.PrevResult)
		if err == nil && len(prevResult.IPs) > 0 {
			clusterIP = prevResult.IPs[0].Address.IP.String()
		}
	}

	client, conn, err := connectToDaemon(conf.DaemonSocket)
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	req := &pb.AddRequest{
		ContainerId:  args.ContainerID,
		Netns:        args.Netns,
		IfName:       args.IfName,
		PodName:      string(k8sArgs.K8S_POD_NAME),
		PodNamespace: string(k8sArgs.K8S_POD_NAMESPACE),
		PodUid:       string(k8sArgs.K8S_POD_UID),
		ClusterIp:    clusterIP,
	}

	resp, err := client.Add(ctx, req)
	if err != nil {
		return fmt.Errorf("daemon Add failed: %w", err)
	}

	// Parse the returned IP
	tailscaleIP := net.ParseIP(resp.TailscaleIpv4)
	if tailscaleIP == nil {
		return fmt.Errorf("invalid Tailscale IP: %s", resp.TailscaleIpv4)
	}

	// Build CNI result
	result := &current.Result{
		CNIVersion: conf.CNIVersion,
		Interfaces: []*current.Interface{
			{
				Name:    args.IfName,
				Sandbox: args.Netns,
			},
		},
		IPs: []*current.IPConfig{
			{
				Address: net.IPNet{
					IP:   tailscaleIP,
					Mask: net.CIDRMask(32, 32),
				},
				Interface: intPtr(0),
			},
		},
		// Add route for Tailscale CGNAT range
		Routes: []*types.Route{
			{
				Dst: net.IPNet{
					IP:   net.ParseIP("100.64.0.0"),
					Mask: net.CIDRMask(10, 32),
				},
			},
		},
	}

	// Add IPv6 if available
	if resp.TailscaleIpv6 != "" {
		tailscaleIPv6 := net.ParseIP(resp.TailscaleIpv6)
		if tailscaleIPv6 != nil {
			result.IPs = append(result.IPs, &current.IPConfig{
				Address: net.IPNet{
					IP:   tailscaleIPv6,
					Mask: net.CIDRMask(128, 128),
				},
				Interface: intPtr(0),
			})
		}
	}

	return types.PrintResult(result, conf.CNIVersion)
}

func cmdDel(args *skel.CmdArgs) error {
	conf, err := loadConf(args.StdinData)
	if err != nil {
		return err
	}

	client, conn, err := connectToDaemon(conf.DaemonSocket)
	if err != nil {
		// If daemon is not available, assume cleanup already happened
		// This is safe because DEL must be idempotent
		fmt.Fprintf(os.Stderr, "Warning: could not connect to daemon: %v\n", err)
		return nil
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := &pb.DelRequest{
		ContainerId: args.ContainerID,
		Netns:       args.Netns,
		IfName:      args.IfName,
	}

	_, err = client.Del(ctx, req)
	if err != nil {
		// DEL should be idempotent, so we don't fail if the pod is already gone
		fmt.Fprintf(os.Stderr, "Warning: daemon Del returned error: %v\n", err)
	}

	return nil
}

func cmdCheck(args *skel.CmdArgs) error {
	conf, err := loadConf(args.StdinData)
	if err != nil {
		return err
	}

	client, conn, err := connectToDaemon(conf.DaemonSocket)
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req := &pb.CheckRequest{
		ContainerId: args.ContainerID,
		Netns:       args.Netns,
		IfName:      args.IfName,
	}

	resp, err := client.Check(ctx, req)
	if err != nil {
		return fmt.Errorf("daemon Check failed: %w", err)
	}

	if !resp.Healthy {
		return fmt.Errorf("unhealthy: %s", resp.Message)
	}

	return nil
}

func intPtr(i int) *int {
	return &i
}
