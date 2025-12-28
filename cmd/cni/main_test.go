package main

import (
	"testing"
)

func TestLoadConf(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		wantErr        bool
		wantSocket     string
		wantCNIVersion string
	}{
		{
			name: "valid minimal config",
			input: `{
				"cniVersion": "1.0.0",
				"name": "tailscale",
				"type": "tailscale-cni"
			}`,
			wantErr:        false,
			wantSocket:     "/var/run/tailscale-cni/daemon.sock",
			wantCNIVersion: "1.0.0",
		},
		{
			name: "config with custom socket",
			input: `{
				"cniVersion": "1.0.0",
				"name": "tailscale",
				"type": "tailscale-cni",
				"daemonSocket": "/custom/path/daemon.sock"
			}`,
			wantErr:        false,
			wantSocket:     "/custom/path/daemon.sock",
			wantCNIVersion: "1.0.0",
		},
		{
			name: "config with cluster name",
			input: `{
				"cniVersion": "1.0.0",
				"name": "tailscale",
				"type": "tailscale-cni",
				"clusterName": "production"
			}`,
			wantErr:        false,
			wantSocket:     "/var/run/tailscale-cni/daemon.sock",
			wantCNIVersion: "1.0.0",
		},
		{
			name:    "invalid json",
			input:   `{invalid json}`,
			wantErr: true,
		},
		{
			name:    "empty config",
			input:   `{}`,
			wantErr: false,
			// Empty is technically valid, just missing fields
			wantSocket: "/var/run/tailscale-cni/daemon.sock",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf, err := loadConf([]byte(tt.input))
			if (err != nil) != tt.wantErr {
				t.Errorf("loadConf() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}

			if conf.DaemonSocket != tt.wantSocket {
				t.Errorf("loadConf().DaemonSocket = %q, want %q", conf.DaemonSocket, tt.wantSocket)
			}

			if tt.wantCNIVersion != "" && conf.CNIVersion != tt.wantCNIVersion {
				t.Errorf("loadConf().CNIVersion = %q, want %q", conf.CNIVersion, tt.wantCNIVersion)
			}
		})
	}
}

func TestParseK8sArgs(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantErr       bool
		wantPodName   string
		wantNamespace string
		wantUID       string
	}{
		{
			name:          "valid k8s args",
			input:         "K8S_POD_NAME=nginx;K8S_POD_NAMESPACE=default;K8S_POD_UID=abc-123",
			wantErr:       false,
			wantPodName:   "nginx",
			wantNamespace: "default",
			wantUID:       "abc-123",
		},
		{
			name:          "full k8s args with infra container",
			input:         "K8S_POD_NAME=my-pod;K8S_POD_NAMESPACE=kube-system;K8S_POD_UID=uid-456;K8S_POD_INFRA_CONTAINER_ID=container-789",
			wantErr:       false,
			wantPodName:   "my-pod",
			wantNamespace: "kube-system",
			wantUID:       "uid-456",
		},
		{
			name:          "empty string",
			input:         "",
			wantErr:       false,
			wantPodName:   "",
			wantNamespace: "",
			wantUID:       "",
		},
		{
			name:          "partial args - only pod name",
			input:         "K8S_POD_NAME=just-name",
			wantErr:       false,
			wantPodName:   "just-name",
			wantNamespace: "",
			wantUID:       "",
		},
		{
			name:          "args with special characters in values",
			input:         "K8S_POD_NAME=pod-with-dashes;K8S_POD_NAMESPACE=ns_underscore;K8S_POD_UID=123-456-789",
			wantErr:       false,
			wantPodName:   "pod-with-dashes",
			wantNamespace: "ns_underscore",
			wantUID:       "123-456-789",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, err := parseK8sArgs(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseK8sArgs() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}

			if string(args.K8S_POD_NAME) != tt.wantPodName {
				t.Errorf("parseK8sArgs().K8S_POD_NAME = %q, want %q", args.K8S_POD_NAME, tt.wantPodName)
			}
			if string(args.K8S_POD_NAMESPACE) != tt.wantNamespace {
				t.Errorf("parseK8sArgs().K8S_POD_NAMESPACE = %q, want %q", args.K8S_POD_NAMESPACE, tt.wantNamespace)
			}
			if string(args.K8S_POD_UID) != tt.wantUID {
				t.Errorf("parseK8sArgs().K8S_POD_UID = %q, want %q", args.K8S_POD_UID, tt.wantUID)
			}
		})
	}
}
