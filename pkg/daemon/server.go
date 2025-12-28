//go:build linux

package daemon

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"

	pb "github.com/jakedgy/tailscale-cni/pkg/proto"
	"google.golang.org/grpc"
)

// Server implements the TailscaleCNI gRPC service.
type Server struct {
	pb.UnimplementedTailscaleCNIServer
	podMgr     *PodManager
	grpcServer *grpc.Server
	socketPath string
}

// NewServer creates a new gRPC server.
func NewServer(socketPath string, podMgr *PodManager) *Server {
	return &Server{
		socketPath: socketPath,
		podMgr:     podMgr,
	}
}

// Start begins listening on the Unix socket.
func (s *Server) Start() error {
	// Ensure socket directory exists
	socketDir := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(socketDir, 0755); err != nil {
		return fmt.Errorf("creating socket directory: %w", err)
	}

	// Remove existing socket file if present
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing existing socket: %w", err)
	}

	// Create Unix socket listener
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.socketPath, err)
	}

	// Set socket permissions to allow CNI binary access
	if err := os.Chmod(s.socketPath, 0660); err != nil {
		listener.Close()
		return fmt.Errorf("setting socket permissions: %w", err)
	}

	// Create gRPC server
	s.grpcServer = grpc.NewServer()
	pb.RegisterTailscaleCNIServer(s.grpcServer, s)

	log.Printf("Starting gRPC server on %s", s.socketPath)

	// Start serving in a goroutine
	go func() {
		if err := s.grpcServer.Serve(listener); err != nil {
			log.Printf("gRPC server error: %v", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the server.
func (s *Server) Stop() {
	if s.grpcServer != nil {
		log.Printf("Stopping gRPC server")
		s.grpcServer.GracefulStop()
	}
	os.Remove(s.socketPath)
}

// Add handles CNI ADD requests.
func (s *Server) Add(ctx context.Context, req *pb.AddRequest) (*pb.AddResponse, error) {
	log.Printf("CNI ADD: container=%s pod=%s/%s netns=%s ifname=%s clusterIP=%s",
		req.ContainerId, req.PodNamespace, req.PodName, req.Netns, req.IfName, req.ClusterIp)

	// Use ts0 as the Tailscale interface name (eth0 is already used by primary CNI)
	tsIfName := "ts0"
	managed, err := s.podMgr.AddPod(ctx, req.ContainerId, req.Netns, tsIfName, req.PodName, req.PodNamespace, req.ClusterIp)
	if err != nil {
		log.Printf("CNI ADD failed: %v", err)
		return nil, fmt.Errorf("adding pod: %w", err)
	}

	resp := &pb.AddResponse{
		TailscaleIpv4:     managed.TailscaleIPv4.String(),
		TailscaleHostname: managed.Hostname,
	}
	if managed.TailscaleIPv6.IsValid() {
		resp.TailscaleIpv6 = managed.TailscaleIPv6.String()
	}

	log.Printf("CNI ADD success: container=%s ip=%s hostname=%s",
		req.ContainerId, resp.TailscaleIpv4, resp.TailscaleHostname)

	return resp, nil
}

// Del handles CNI DEL requests.
func (s *Server) Del(ctx context.Context, req *pb.DelRequest) (*pb.DelResponse, error) {
	log.Printf("CNI DEL: container=%s netns=%s ifname=%s",
		req.ContainerId, req.Netns, req.IfName)

	if err := s.podMgr.DeletePod(req.ContainerId); err != nil {
		log.Printf("CNI DEL failed: %v", err)
		return nil, fmt.Errorf("deleting pod: %w", err)
	}

	log.Printf("CNI DEL success: container=%s", req.ContainerId)

	return &pb.DelResponse{}, nil
}

// Check handles CNI CHECK requests.
func (s *Server) Check(ctx context.Context, req *pb.CheckRequest) (*pb.CheckResponse, error) {
	log.Printf("CNI CHECK: container=%s netns=%s ifname=%s",
		req.ContainerId, req.Netns, req.IfName)

	healthy, message, err := s.podMgr.CheckPod(req.ContainerId)
	if err != nil {
		log.Printf("CNI CHECK failed: %v", err)
		return nil, fmt.Errorf("checking pod: %w", err)
	}

	log.Printf("CNI CHECK result: container=%s healthy=%v message=%s",
		req.ContainerId, healthy, message)

	return &pb.CheckResponse{
		Healthy: healthy,
		Message: message,
	}, nil
}
