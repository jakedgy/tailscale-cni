//go:build linux

package daemon

import "testing"

func TestStripKubernetesSuffixes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "deployment with replicaset hash and random suffix",
			input: "nginx-deployment-7b5d9c6f8-xyz12",
			want:  "nginx-deployment",
		},
		{
			name:  "deployment with full replicaset suffix",
			input: "plex-deployment-7b5d9c6f8-abcde",
			want:  "plex-deployment",
		},
		{
			name:  "simple app name with replicaset suffix",
			input: "plex-7b5d9c6f8-xyz12",
			want:  "plex",
		},
		{
			name:  "statefulset with ordinal (should not strip)",
			input: "redis-statefulset-0",
			want:  "redis-statefulset-0",
		},
		{
			name:  "statefulset with higher ordinal",
			input: "cassandra-3",
			want:  "cassandra-3",
		},
		{
			name:  "deployment with only hash (no random suffix)",
			input: "api-server-5f7d8c9b2a",
			want:  "api-server",
		},
		{
			name:  "simple pod name without suffixes",
			input: "my-pod",
			want:  "my-pod",
		},
		{
			name:  "pod with single dash",
			input: "frontend-backend",
			want:  "frontend-backend",
		},
		{
			name:  "cronjob pod with timestamp",
			input: "backup-job-1234567890-xyz12",
			want:  "backup-job",
		},
		{
			name:  "short hash that looks like replicaset",
			input: "app-12345678-abcde",
			want:  "app",
		},
		{
			name:  "10 character hash",
			input: "web-abcdef1234-xyz12",
			want:  "web",
		},
		{
			name:  "name with multiple dashes",
			input: "my-very-long-app-name-7b5d9c6f8-xyz12",
			want:  "my-very-long-app-name",
		},
		{
			name:  "kubernetes system pod",
			input: "coredns-7b5d9c6f8-xyz12",
			want:  "coredns",
		},
		{
			name:  "pod with numbers in name",
			input: "redis123-deployment-7b5d9c6f8-xyz12",
			want:  "redis123-deployment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripKubernetesSuffixes(tt.input)
			if got != tt.want {
				t.Errorf("stripKubernetesSuffixes(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeHostname(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "already valid",
			input: "my-pod",
			want:  "my-pod",
		},
		{
			name:  "uppercase to lowercase",
			input: "MY-POD",
			want:  "my-pod",
		},
		{
			name:  "mixed case",
			input: "MyPod-Name",
			want:  "mypod-name",
		},
		{
			name:  "underscores to dashes",
			input: "my_pod_name",
			want:  "my-pod-name",
		},
		{
			name:  "dots to dashes",
			input: "my.pod.name",
			want:  "my-pod-name",
		},
		{
			name:  "special characters",
			input: "my@pod#name!",
			want:  "my-pod-name",
		},
		{
			name:  "multiple consecutive dashes collapsed",
			input: "my--pod---name",
			want:  "my-pod-name",
		},
		{
			name:  "leading dash trimmed",
			input: "-my-pod",
			want:  "my-pod",
		},
		{
			name:  "trailing dash trimmed",
			input: "my-pod-",
			want:  "my-pod",
		},
		{
			name:  "leading and trailing dashes trimmed",
			input: "-my-pod-",
			want:  "my-pod",
		},
		{
			name:  "numbers preserved",
			input: "pod-123-abc",
			want:  "pod-123-abc",
		},
		{
			name:  "long name truncated to 63 chars",
			input: "this-is-a-very-long-hostname-that-exceeds-the-dns-limit-of-63-characters-total",
			want:  "this-is-a-very-long-hostname-that-exceeds-the-dns-limit-of-63-c",
		},
		{
			name:  "exactly 63 chars unchanged",
			input: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			want:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "only special characters",
			input: "@#$%^&*()",
			want:  "",
		},
		{
			name:  "kubernetes pod name format",
			input: "nginx-deployment-7b5d9c6f8-xyz12",
			want:  "nginx-deployment-7b5d9c6f8-xyz12",
		},
		{
			name:  "full hostname with cluster and namespace",
			input: "minikube-default-nginx-deployment-7b5d9c6f8-xyz12",
			want:  "minikube-default-nginx-deployment-7b5d9c6f8-xyz12",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeHostname(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeHostname(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
