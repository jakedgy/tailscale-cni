package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	// maxConcurrentAuthKeys limits concurrent auth key API requests to prevent
	// thundering herd when many pods start simultaneously.
	maxConcurrentAuthKeys = 5

	// authKeyMinInterval is the minimum time between auth key requests.
	// This prevents burst requests from overwhelming the Tailscale API.
	authKeyMinInterval = 100 * time.Millisecond
)

// OAuthManager handles Tailscale OAuth authentication and auth key creation.
type OAuthManager struct {
	clientID     string
	clientSecret string
	baseURL      string
	tags         []string

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time

	// Rate limiting for auth key creation
	authKeySem  chan struct{} // Semaphore for concurrent requests
	lastAuthKey time.Time     // Time of last auth key request

	httpClient *http.Client
}

// NewOAuthManager creates a new OAuth manager with the given credentials.
func NewOAuthManager(clientID, clientSecret string, tags []string) *OAuthManager {
	return &OAuthManager{
		clientID:     clientID,
		clientSecret: clientSecret,
		baseURL:      "https://api.tailscale.com",
		tags:         tags,
		authKeySem:   make(chan struct{}, maxConcurrentAuthKeys),
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

// tokenResponse represents the OAuth token response from Tailscale.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// authKeyRequest represents the request to create an auth key.
type authKeyRequest struct {
	Capabilities authKeyCapabilities `json:"capabilities"`
	ExpirySeconds int                `json:"expirySeconds,omitempty"`
	Description   string             `json:"description,omitempty"`
}

type authKeyCapabilities struct {
	Devices authKeyDevices `json:"devices"`
}

type authKeyDevices struct {
	Create authKeyCreate `json:"create"`
}

type authKeyCreate struct {
	Reusable      bool     `json:"reusable"`
	Ephemeral     bool     `json:"ephemeral"`
	Preauthorized bool     `json:"preauthorized"`
	Tags          []string `json:"tags"`
}

// authKeyResponse represents the response when creating an auth key.
type authKeyResponse struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// getAccessToken returns a valid access token, refreshing if necessary.
func (m *OAuthManager) getAccessToken(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Return cached token if still valid (with 5 minute buffer)
	if m.accessToken != "" && time.Now().Add(5*time.Minute).Before(m.tokenExpiry) {
		return m.accessToken, nil
	}

	// Refresh the token
	data := url.Values{}
	data.Set("client_id", m.clientID)
	data.Set("client_secret", m.clientSecret)

	req, err := http.NewRequestWithContext(ctx, "POST", m.baseURL+"/api/v2/oauth/token", bytes.NewBufferString(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("creating token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("decoding token response: %w", err)
	}

	m.accessToken = tokenResp.AccessToken
	m.tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	return m.accessToken, nil
}

// CreateAuthKey creates a new ephemeral, preauthorized auth key for a pod.
// Rate-limited to prevent overwhelming the Tailscale API during burst pod creation.
func (m *OAuthManager) CreateAuthKey(ctx context.Context, podName, namespace string) (string, error) {
	// Acquire semaphore slot (limits concurrent requests)
	select {
	case m.authKeySem <- struct{}{}:
		defer func() { <-m.authKeySem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	// Enforce minimum interval between requests
	m.mu.Lock()
	elapsed := time.Since(m.lastAuthKey)
	if elapsed < authKeyMinInterval {
		wait := authKeyMinInterval - elapsed
		m.mu.Unlock()
		log.Printf("Rate limiting auth key request, waiting %v", wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		m.mu.Lock()
	}
	m.lastAuthKey = time.Now()
	m.mu.Unlock()

	token, err := m.getAccessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("getting access token: %w", err)
	}

	keyReq := authKeyRequest{
		Capabilities: authKeyCapabilities{
			Devices: authKeyDevices{
				Create: authKeyCreate{
					Reusable:      false,
					Ephemeral:     false, // Non-ephemeral for recovery support
					Preauthorized: true,
					Tags:          m.tags,
				},
			},
		},
		ExpirySeconds: 300, // 5 minutes, enough time for pod to start
		Description:   fmt.Sprintf("tailscale-cni %s %s", namespace, podName),
	}

	body, err := json.Marshal(keyReq)
	if err != nil {
		return "", fmt.Errorf("marshaling auth key request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", m.baseURL+"/api/v2/tailnet/-/keys", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("creating auth key request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting auth key: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("auth key request failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var keyResp authKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&keyResp); err != nil {
		return "", fmt.Errorf("decoding auth key response: %w", err)
	}

	return keyResp.Key, nil
}
