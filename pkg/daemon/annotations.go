//go:build linux

package daemon

import (
	"strconv"
	"strings"
)

const (
	// Annotation keys
	AnnotationEnabled   = "tailscale.com/enabled"
	AnnotationTags      = "tailscale.com/tags"
	AnnotationHostname  = "tailscale.com/hostname"
	AnnotationEphemeral = "tailscale.com/ephemeral"
)

// PodConfig represents parsed pod annotations for Tailscale configuration.
type PodConfig struct {
	// Enabled indicates whether Tailscale should be enabled for this pod.
	// Default is true.
	Enabled bool

	// Tags are Tailscale tags to apply to this pod's node.
	// If empty, daemon-level tags are used.
	Tags []string

	// Hostname is the custom hostname for this pod.
	// If empty, the default cluster-namespace-podname format is used.
	Hostname string

	// Ephemeral indicates whether to use LoginEphemeral.
	// When true, the pod will lose connectivity if the daemon restarts.
	// Default is false (persistent nodes).
	Ephemeral bool
}

// ParsePodAnnotations extracts Tailscale configuration from pod annotations.
func ParsePodAnnotations(annotations map[string]string) *PodConfig {
	config := &PodConfig{
		Enabled:   true, // Default to enabled
		Ephemeral: false, // Default to persistent
	}

	// Parse enabled flag
	if val, ok := annotations[AnnotationEnabled]; ok {
		if enabled, err := strconv.ParseBool(val); err == nil {
			config.Enabled = enabled
		}
	}

	// Parse tags
	if val, ok := annotations[AnnotationTags]; ok {
		val = strings.TrimSpace(val)
		if val != "" {
			for _, tag := range strings.Split(val, ",") {
				tag = strings.TrimSpace(tag)
				if tag != "" {
					config.Tags = append(config.Tags, tag)
				}
			}
		}
	}

	// Parse hostname
	if val, ok := annotations[AnnotationHostname]; ok {
		config.Hostname = strings.TrimSpace(val)
	}

	// Parse ephemeral flag
	if val, ok := annotations[AnnotationEphemeral]; ok {
		if ephemeral, err := strconv.ParseBool(val); err == nil {
			config.Ephemeral = ephemeral
		}
	}

	return config
}
