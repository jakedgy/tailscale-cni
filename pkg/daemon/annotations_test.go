//go:build linux

package daemon

import (
	"reflect"
	"testing"
)

func TestParsePodAnnotations(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        *PodConfig
	}{
		{
			name:        "no annotations - all defaults",
			annotations: map[string]string{},
			want: &PodConfig{
				Enabled:   true,
				Tags:      nil,
				Hostname:  "",
				Ephemeral: false,
			},
		},
		{
			name: "disabled via annotation",
			annotations: map[string]string{
				AnnotationEnabled: "false",
			},
			want: &PodConfig{
				Enabled:   false,
				Tags:      nil,
				Hostname:  "",
				Ephemeral: false,
			},
		},
		{
			name: "explicitly enabled",
			annotations: map[string]string{
				AnnotationEnabled: "true",
			},
			want: &PodConfig{
				Enabled:   true,
				Tags:      nil,
				Hostname:  "",
				Ephemeral: false,
			},
		},
		{
			name: "custom tags - single",
			annotations: map[string]string{
				AnnotationTags: "tag:media",
			},
			want: &PodConfig{
				Enabled:   true,
				Tags:      []string{"tag:media"},
				Hostname:  "",
				Ephemeral: false,
			},
		},
		{
			name: "custom tags - multiple",
			annotations: map[string]string{
				AnnotationTags: "tag:media,tag:plex",
			},
			want: &PodConfig{
				Enabled:   true,
				Tags:      []string{"tag:media", "tag:plex"},
				Hostname:  "",
				Ephemeral: false,
			},
		},
		{
			name: "custom tags - with spaces",
			annotations: map[string]string{
				AnnotationTags: "tag:media, tag:plex , tag:streaming",
			},
			want: &PodConfig{
				Enabled:   true,
				Tags:      []string{"tag:media", "tag:plex", "tag:streaming"},
				Hostname:  "",
				Ephemeral: false,
			},
		},
		{
			name: "custom hostname",
			annotations: map[string]string{
				AnnotationHostname: "plex",
			},
			want: &PodConfig{
				Enabled:   true,
				Tags:      nil,
				Hostname:  "plex",
				Ephemeral: false,
			},
		},
		{
			name: "custom hostname with spaces trimmed",
			annotations: map[string]string{
				AnnotationHostname: "  my-server  ",
			},
			want: &PodConfig{
				Enabled:   true,
				Tags:      nil,
				Hostname:  "my-server",
				Ephemeral: false,
			},
		},
		{
			name: "ephemeral enabled",
			annotations: map[string]string{
				AnnotationEphemeral: "true",
			},
			want: &PodConfig{
				Enabled:   true,
				Tags:      nil,
				Hostname:  "",
				Ephemeral: true,
			},
		},
		{
			name: "ephemeral explicitly disabled",
			annotations: map[string]string{
				AnnotationEphemeral: "false",
			},
			want: &PodConfig{
				Enabled:   true,
				Tags:      nil,
				Hostname:  "",
				Ephemeral: false,
			},
		},
		{
			name: "all options combined",
			annotations: map[string]string{
				AnnotationEnabled:   "true",
				AnnotationTags:      "tag:media,tag:plex",
				AnnotationHostname:  "my-plex-server",
				AnnotationEphemeral: "false",
			},
			want: &PodConfig{
				Enabled:   true,
				Tags:      []string{"tag:media", "tag:plex"},
				Hostname:  "my-plex-server",
				Ephemeral: false,
			},
		},
		{
			name: "invalid boolean values use defaults",
			annotations: map[string]string{
				AnnotationEnabled:   "maybe",
				AnnotationEphemeral: "sometimes",
			},
			want: &PodConfig{
				Enabled:   true, // Defaults to enabled on parse error
				Tags:      nil,
				Hostname:  "",
				Ephemeral: false, // Defaults to false on parse error
			},
		},
		{
			name: "empty tag string",
			annotations: map[string]string{
				AnnotationTags: "",
			},
			want: &PodConfig{
				Enabled:   true,
				Tags:      nil,
				Hostname:  "",
				Ephemeral: false,
			},
		},
		{
			name: "tags with empty elements",
			annotations: map[string]string{
				AnnotationTags: "tag:media,,tag:plex",
			},
			want: &PodConfig{
				Enabled:   true,
				Tags:      []string{"tag:media", "tag:plex"},
				Hostname:  "",
				Ephemeral: false,
			},
		},
		{
			name: "opt-out example from issue",
			annotations: map[string]string{
				AnnotationEnabled: "false",
			},
			want: &PodConfig{
				Enabled:   false,
				Tags:      nil,
				Hostname:  "",
				Ephemeral: false,
			},
		},
		{
			name: "plex server example from issue",
			annotations: map[string]string{
				AnnotationTags:      "tag:media,tag:plex",
				AnnotationHostname:  "plex",
				AnnotationEphemeral: "false",
			},
			want: &PodConfig{
				Enabled:   true,
				Tags:      []string{"tag:media", "tag:plex"},
				Hostname:  "plex",
				Ephemeral: false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParsePodAnnotations(tt.annotations)
			if got.Enabled != tt.want.Enabled {
				t.Errorf("Enabled = %v, want %v", got.Enabled, tt.want.Enabled)
			}
			if !reflect.DeepEqual(got.Tags, tt.want.Tags) {
				t.Errorf("Tags = %v, want %v", got.Tags, tt.want.Tags)
			}
			if got.Hostname != tt.want.Hostname {
				t.Errorf("Hostname = %q, want %q", got.Hostname, tt.want.Hostname)
			}
			if got.Ephemeral != tt.want.Ephemeral {
				t.Errorf("Ephemeral = %v, want %v", got.Ephemeral, tt.want.Ephemeral)
			}
		})
	}
}
