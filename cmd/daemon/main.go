//go:build linux

package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jakedgy/tailscale-cni/pkg/daemon"
)

func main() {
	// Parse flags
	socketPath := flag.String("socket", "/var/run/tailscale-cni/daemon.sock", "Path to Unix socket")
	stateDir := flag.String("state-dir", "/var/lib/tailscale-cni", "Directory for state storage")
	clusterName := flag.String("cluster-name", "", "Kubernetes cluster name (used in Tailscale hostnames)")
	tagsFlag := flag.String("tags", "", "Comma-separated Tailscale tags for pods (e.g., tag:k8s-pod)")
	authKeyTTL := flag.Duration("auth-key-ttl", 5*time.Minute, "TTL for auth keys (default 5m)")
	flag.Parse()

	// Get OAuth credentials from environment
	clientID := os.Getenv("TS_OAUTH_CLIENT_ID")
	clientSecret := os.Getenv("TS_OAUTH_CLIENT_SECRET")

	if clientID == "" || clientSecret == "" {
		log.Fatal("TS_OAUTH_CLIENT_ID and TS_OAUTH_CLIENT_SECRET environment variables are required")
	}

	// Use cluster name from flag or environment
	cluster := *clusterName
	if cluster == "" {
		cluster = os.Getenv("CLUSTER_NAME")
	}
	if cluster == "" {
		cluster = "k8s"
	}

	// Parse tags
	var tags []string
	tagsStr := *tagsFlag
	if tagsStr == "" {
		tagsStr = os.Getenv("TS_TAGS")
	}
	if tagsStr != "" {
		for _, t := range strings.Split(tagsStr, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tags = append(tags, t)
			}
		}
	}
	if len(tags) == 0 {
		tags = []string{"tag:k8s-pod"}
	}

	log.Printf("Starting tailscale-cni daemon")
	log.Printf("  Socket: %s", *socketPath)
	log.Printf("  State dir: %s", *stateDir)
	log.Printf("  Cluster name: %s", cluster)
	log.Printf("  Tags: %v", tags)
	log.Printf("  Auth key TTL: %s", *authKeyTTL)

	// Create state directory
	if err := os.MkdirAll(*stateDir, 0700); err != nil {
		log.Fatalf("Failed to create state directory: %v", err)
	}

	// Initialize OAuth manager
	oauthMgr := daemon.NewOAuthManager(clientID, clientSecret, tags, *authKeyTTL)

	// Initialize pod manager
	podMgr := daemon.NewPodManager(*stateDir, cluster, oauthMgr)

	// Recover pods from previous daemon session
	log.Printf("Recovering pods from previous session...")
	ctx := context.Background()
	recovered, errs := podMgr.RecoverPods(ctx)
	log.Printf("Recovered %d pods", recovered)
	for _, err := range errs {
		log.Printf("Recovery error: %v", err)
	}

	// Clean up any orphaned network resources
	podMgr.CleanupOrphanedResources()

	// Initialize and start gRPC server
	server := daemon.NewServer(*socketPath, podMgr)
	if err := server.Start(); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

	log.Printf("Daemon ready and listening")

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Printf("Shutting down...")

	// Graceful shutdown
	server.Stop()
	if err := podMgr.Close(); err != nil {
		log.Printf("Error closing pod manager: %v", err)
	}

	log.Printf("Shutdown complete")
}
