package main

import (
	"context"
	"fmt"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// getPodAnnotations retrieves annotations for a pod using the in-cluster Kubernetes API.
func getPodAnnotations(namespace, name string) (map[string]string, error) {
	// Create in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		// If we can't get in-cluster config, return empty annotations
		// This handles cases where CNI runs outside a cluster (e.g., tests)
		fmt.Fprintf(os.Stderr, "Warning: cannot create in-cluster config: %v\n", err)
		return make(map[string]string), nil
	}

	// Create clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	// Get pod with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("getting pod %s/%s: %w", namespace, name, err)
	}

	// Return annotations (will be nil/empty if pod has no annotations)
	if pod.Annotations == nil {
		return make(map[string]string), nil
	}

	return pod.Annotations, nil
}
