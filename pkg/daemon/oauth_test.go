//go:build linux

package daemon

import (
	"testing"
	"time"
)

func TestNewOAuthManager_DefaultTTL(t *testing.T) {
	mgr := NewOAuthManager("client-id", "client-secret", []string{"tag:test"}, 0)
	
	// When TTL is 0, should default to 5 minutes
	expected := 5 * time.Minute
	if mgr.authKeyTTL != expected {
		t.Errorf("Expected default TTL %v, got %v", expected, mgr.authKeyTTL)
	}
}

func TestNewOAuthManager_CustomTTL(t *testing.T) {
	customTTL := 10 * time.Minute
	mgr := NewOAuthManager("client-id", "client-secret", []string{"tag:test"}, customTTL)
	
	if mgr.authKeyTTL != customTTL {
		t.Errorf("Expected custom TTL %v, got %v", customTTL, mgr.authKeyTTL)
	}
}

func TestNewOAuthManager_VariousTTLs(t *testing.T) {
	tests := []struct {
		name     string
		ttl      time.Duration
		expected time.Duration
	}{
		{
			name:     "zero defaults to 5m",
			ttl:      0,
			expected: 5 * time.Minute,
		},
		{
			name:     "1 minute",
			ttl:      1 * time.Minute,
			expected: 1 * time.Minute,
		},
		{
			name:     "10 minutes",
			ttl:      10 * time.Minute,
			expected: 10 * time.Minute,
		},
		{
			name:     "15 minutes",
			ttl:      15 * time.Minute,
			expected: 15 * time.Minute,
		},
		{
			name:     "30 seconds",
			ttl:      30 * time.Second,
			expected: 30 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := NewOAuthManager("client-id", "client-secret", []string{"tag:test"}, tt.ttl)
			if mgr.authKeyTTL != tt.expected {
				t.Errorf("Expected TTL %v, got %v", tt.expected, mgr.authKeyTTL)
			}
		})
	}
}
