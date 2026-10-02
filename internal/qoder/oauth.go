package qoder

// PLUGIN-SERVED (see AGENTS.md): Qoder ("qoder") and Qoder CN ("qoder-cn")
// are deprecated built-in subscriptions served by their plugin,
// @magpie-community/opencode-qoder-auth, each once moved onto it
// (provider.Moved; the default for a new sign-in). A moved one's
// sign-ins, models, requests and usage are all the plugin's, never this
// code's (only the move, in migrate*.go, still reads its accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/qoder) and raise the
// movers' min in internal/provider/migrate_qoder.go.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeviceToken is the dt- device token a finished device flow returns.
type DeviceToken struct {
	Token        string `json:"token"`
	DeviceToken  string `json:"device_token,omitempty"`
	RefreshToken string `json:"refresh_token"`
	UserID       string `json:"user_id"`
	UserName     string `json:"user_name,omitempty"`
	// ExpiresIn (seconds) or ExpiresAt (a date) is the token's lifetime, as
	// Qoder's CLI reads it; either may be missing.
	ExpiresIn int64  `json:"expires_in,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// Expiry is when the device token lapses; zero when Qoder said nothing.
func (t DeviceToken) Expiry() time.Time {
	if at, err := time.Parse(time.RFC3339, strings.TrimSpace(t.ExpiresAt)); err == nil {
		return at
	}
	if t.ExpiresIn > 0 {
		return time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	}
	return time.Time{}
}

// JobToken is the jt- task token that authorizes the model calls.
type JobToken struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
	// ExpiresIn is Qoder's lifetime for the token, in milliseconds.
	ExpiresIn int64 `json:"expires_in"`
}

// Expiry is the job token's lifetime as a duration; zero when Qoder said none.
func (r JobToken) Expiry() time.Duration {
	if r.ExpiresIn <= 0 {
		return 0
	}
	return time.Duration(r.ExpiresIn) * time.Millisecond
}

// DeviceFlow runs Qoder's sign-in: an authorization page the user opens, then
// a poll for the device token, then its exchange for the job token that serves
// models. It is a helper, not a state machine: the caller owns the browser and
// the waiting, and calls these one at a time.
type DeviceFlow struct {
	client    *http.Client
	site      *Site
	machineID string
}

// NewDeviceFlow makes a flow on site (nil is the global one); a nil client is
// a 20-second-timeout one.
func NewDeviceFlow(client *http.Client, site *Site) *DeviceFlow {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if site == nil {
		site = Global
	}
	return &DeviceFlow{client: client, site: site, machineID: newUUID()}
}

// Site is the Qoder site the flow signs in on.
func (f *DeviceFlow) Site() *Site { return f.site }

// Client is the flow's HTTP client, reused for the model fetch after sign-in.
func (f *DeviceFlow) Client() *http.Client { return f.client }

func (f *DeviceFlow) MachineID() string { return f.machineID }

// Authorization returns the page to open and the (verifier, nonce) the poll
// needs. It is a PKCE device flow: a random verifier is kept, its S256 digest
// is what the page sees.
func (f *DeviceFlow) Authorization() (authURL, verifier, nonce string, err error) {
	raw := make([]byte, 64)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", fmt.Errorf("qoder auth url: random verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	nonce = newUUID()
	q := url.Values{}
	q.Set("challenge", challenge)
	q.Set("challenge_method", "S256")
	q.Set("nonce", nonce)
	q.Set("machine_id", f.machineID)
	q.Set("client_id", f.site.ClientID)
	if f.site.RedirectURI != "" {
		q.Set("redirect_uri", f.site.RedirectURI)
	}
	return f.site.Web + DeviceSelectAccountsPath + "?" + q.Encode(), verifier, nonce, nil
}

// PollDeviceToken asks for the device token until the user has authorized it,
// ctx ends the wait, or interval elapses between tries.
func (f *DeviceFlow) PollDeviceToken(ctx context.Context, nonce, verifier string, interval time.Duration) (*DeviceToken, error) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	endpoint := f.site.OpenAPI + DeviceTokenPollPath
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("qoder device poll: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("qoder device poll: create request: %w", err)
		}
		q := req.URL.Query()
		q.Set("nonce", nonce)
		q.Set("verifier", verifier)
		q.Set("challenge_method", "S256")
		req.URL.RawQuery = q.Encode()
		req.Header.Set("Accept", "application/json")
		resp, err := f.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("qoder device poll: request failed: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var t DeviceToken
			if json.Unmarshal(body, &t) == nil && strings.TrimSpace(t.Token) != "" {
				return &t, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("qoder device poll: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
}

// JobToken trades a device token for the job token the model calls use.
func (f *DeviceFlow) JobToken(ctx context.Context, deviceToken string) (*JobToken, error) {
	body, _ := json.Marshal(map[string]string{"clientId": f.site.ClientID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.site.OpenAPI+JobTokenPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("qoder job token: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+deviceToken)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qoder job token: request failed: %w", err)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &JobTokenHTTPError{StatusCode: resp.StatusCode, Body: sanitize(raw)}
	}
	var t JobToken
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("qoder job token: decode: %w", err)
	}
	if strings.TrimSpace(t.Token) == "" {
		return nil, fmt.Errorf("qoder job token: empty token in response")
	}
	return &t, nil
}

// JobTokenHTTPError is a refused device-to-job token exchange.
type JobTokenHTTPError struct {
	StatusCode int
	Body       string
}

func (e *JobTokenHTTPError) Error() string {
	return fmt.Sprintf("qoder job token: status %d: %s", e.StatusCode, e.Body)
}

// RefreshJobToken trades a job token's refresh token for a new pair on site
// (nil is the global one). The old refresh token is spent, so the caller must
// keep the new one.
func RefreshJobToken(ctx context.Context, client *http.Client, site *Site, refreshToken string) (*JobToken, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("qoder job token refresh: missing refresh token; sign in again")
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if site == nil {
		site = Global
	}
	return postJobRefresh(ctx, client, site.OpenAPI+JobTokenRefreshPath, refreshToken)
}

func postJobRefresh(ctx context.Context, client *http.Client, endpoint, refreshToken string) (*JobToken, error) {
	body, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("qoder job token refresh: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qoder job token refresh: request failed: %w", err)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &JobTokenRefreshHTTPError{StatusCode: resp.StatusCode}
	}
	var t JobToken
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("qoder job token refresh: decode: %w", err)
	}
	if strings.TrimSpace(t.Token) == "" || strings.TrimSpace(t.RefreshToken) == "" {
		return nil, fmt.Errorf("qoder job token refresh: incomplete token pair")
	}
	return &t, nil
}

// JobTokenRefreshHTTPError preserves a rejected job-token refresh status.
type JobTokenRefreshHTTPError struct {
	StatusCode int
}

func (e *JobTokenRefreshHTTPError) Error() string {
	return fmt.Sprintf("qoder job token refresh: status %d", e.StatusCode)
}

// DeviceTokenRefreshHTTPError preserves a rejected refresh status without response data.
type DeviceTokenRefreshHTTPError struct {
	StatusCode int
}

func (e *DeviceTokenRefreshHTTPError) Error() string {
	return fmt.Sprintf("qoder device token refresh: upstream HTTP %d", e.StatusCode)
}

// RefreshDeviceToken rotates the device token used by account endpoints; an
// empty endpoint is the global site's.
func RefreshDeviceToken(ctx context.Context, client *http.Client, endpoint, refreshToken string) (*DeviceToken, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("qoder device token refresh: missing refresh token; sign in again")
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if endpoint == "" {
		endpoint = Global.OpenAPI + DeviceTokenRefreshPath
	}
	body, err := json.Marshal(map[string]string{"refresh_token": refreshToken})
	if err != nil {
		return nil, fmt.Errorf("qoder device token refresh: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("qoder device token refresh: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qoder device token refresh: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &DeviceTokenRefreshHTTPError{StatusCode: resp.StatusCode}
	}
	var token DeviceToken
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&token); err != nil {
		return nil, fmt.Errorf("qoder device token refresh: decode: %w", err)
	}
	if token.Token == "" {
		token.Token = token.DeviceToken
	}
	if strings.TrimSpace(token.Token) == "" || strings.TrimSpace(token.RefreshToken) == "" {
		return nil, fmt.Errorf("qoder device token refresh: incomplete token pair")
	}
	return &token, nil
}
