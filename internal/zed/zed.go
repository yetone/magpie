// Package zed speaks to Zed's hosted models, the way the Zed editor does for
// a Zed Pro (or trial, student, business) account.
//
// Read from Zed's source (github.com/zed-industries/zed, 1.23.0):
//
//   - Sign-in (crates/client/src/client.rs, crates/rpc/src/auth.rs): the app
//     makes an RSA-2048 key, listens on a port of 127.0.0.1 and opens
//     zed.dev/native_app_signin?native_app_port=&native_app_public_key=, the
//     key PKCS#1 DER in padded URL-safe base64. The browser comes back to that
//     port, on any path, with user_id and access_token, the token encrypted to
//     the key (RSA-OAEP SHA-256, PKCS#1 v1.5 before that). The pair is sent
//     as "Authorization: <user_id> <access_token>" to cloud.zed.dev/client/*
//     and lasts until a 401.
//   - GET /client/users/me is the account: its organizations, the plan.
//   - POST /client/llm_tokens {"organization_id"} trades the pair for a
//     short-lived LLM token, renewed on a 401 or an x-zed-expired-token /
//     x-zed-outdated-token reply.
//   - GET /models lists what the account may call; POST /completions takes
//     {provider, model, provider_request} — the provider's own request:
//     Anthropic Messages, OpenAI Responses, xAI chat completions, Gemini's
//     generateContent — and streams newline-delimited JSON, each line
//     {"event": <the provider's own stream event>} or a status.
package zed

// PLUGIN-SERVED (see AGENTS.md): Zed ("zed") is a deprecated built-in
// subscription served by its plugin, @magpie-community/opencode-zed-auth,
// once moved onto it (provider.Moved; the default for a new sign-in). A
// moved one's sign-ins, models, requests and usage are all the plugin's,
// never this code's (only the move, in migrate*.go, still reads its
// accounts). A fix here alone doesn't reach those users; fix the plugin
// (github.com/magpie-community/plugins, packages/zed) and raise the mover's
// min in internal/provider/migrate_zed.go.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/catalog"
)

const (
	// SiteURL is where the browser signs in.
	SiteURL = "https://zed.dev"
	// CloudURL is the account and model API.
	CloudURL = "https://cloud.zed.dev"
	// Version is the Zed release the requests say they come from.
	Version = "1.23.0"
	// SucceededURL is where the browser is sent once the callback is read,
	// as Zed sends it.
	SucceededURL = SiteURL + "/native_app_signin_succeeded"
)

// UserAgent is Zed's: Zed/<version> (<os>; <arch>), in Rust's names.
func UserAgent() string {
	os := runtime.GOOS
	if os == "darwin" {
		os = "macos"
	}
	arch := map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[runtime.GOARCH]
	if arch == "" {
		arch = runtime.GOARCH
	}
	return fmt.Sprintf("Zed/%s (%s; %s)", Version, os, arch)
}

// HTTPStatusError is a reply that wasn't a success, its status kept typed.
type HTTPStatusError struct {
	StatusCode int
	Body       string
	Header     http.Header
}

func (e *HTTPStatusError) Error() string {
	msg := Failure(e.StatusCode, []byte(e.Body))
	return fmt.Sprintf("Zed: %s (%d)", msg, e.StatusCode)
}

// Failure is the message in an error body: {"code","message"}, else the body
// itself, else the status's name.
func Failure(status int, body []byte) string {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   any    `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil {
		if e.Message != "" {
			return e.Message
		}
		if s, ok := e.Error.(string); ok && s != "" {
			return s
		}
		if e.Code != "" {
			return e.Code
		}
	}
	if s := strings.TrimSpace(string(body)); s != "" && len(s) < 500 {
		return s
	}
	if status == http.StatusPaymentRequired {
		return "payment required: this Zed account has no plan that calls models, or its allowance is used up"
	}
	return http.StatusText(status)
}

// ---- sign-in ---------------------------------------------------------------

// NewKey is the key the access token comes back encrypted to, and its public
// half as the sign-in page takes it.
func NewKey() (*rsa.PrivateKey, string, error) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, "", err
	}
	return k, base64.URLEncoding.EncodeToString(x509.MarshalPKCS1PublicKey(&k.PublicKey)), nil
}

// SignInURL is the page to open for a sign-in coming back to port.
func SignInURL(site string, port int, publicKey, systemID string) string {
	q := url.Values{}
	q.Set("native_app_port", strconv.Itoa(port))
	q.Set("native_app_public_key", publicKey)
	if systemID != "" {
		q.Set("system_id", systemID)
	}
	return site + "/native_app_signin?" + q.Encode()
}

// Decrypt reads the access token the callback carried.
func Decrypt(k *rsa.PrivateKey, ciphertext string) (string, error) {
	ct, err := base64.URLEncoding.DecodeString(ciphertext)
	if err != nil {
		if ct, err = base64.RawURLEncoding.DecodeString(strings.TrimRight(ciphertext, "=")); err != nil {
			return "", fmt.Errorf("the access token isn't base64: %w", err)
		}
	}
	pt, err := rsa.DecryptOAEP(sha256.New(), nil, k, ct, nil)
	if err != nil {
		if pt, err = rsa.DecryptPKCS1v15(nil, k, ct); err != nil {
			return "", errors.New("the access token couldn't be decrypted")
		}
	}
	return string(pt), nil
}

// NewSystemID is a random UUID v4, what Zed calls the machine.
func NewSystemID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// ---- the account -----------------------------------------------------------

// Me is GET /client/users/me, as far as magpie reads it.
type Me struct {
	User struct {
		LegacyID    int64  `json:"legacy_user_id"`
		Username    string `json:"username"`
		GitHubLogin string `json:"github_login"`
		Name        string `json:"name"`
	} `json:"user"`
	Organizations []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		IsPersonal bool   `json:"is_personal"`
	} `json:"organizations"`
	DefaultOrganizationID string            `json:"default_organization_id"`
	PlansByOrganization   map[string]string `json:"plans_by_organization"`
	ConfigByOrganization  map[string]struct {
		ModelsEnabled *bool `json:"is_zed_model_provider_enabled"`
	} `json:"configuration_by_organization"`
	Plan struct {
		V3                 string `json:"plan_v3"`
		SubscriptionPeriod *struct {
			StartedAt string `json:"started_at"`
			EndedAt   string `json:"ended_at"`
		} `json:"subscription_period"`
		TrialStartedAt    string `json:"trial_started_at"`
		AccountTooYoung   bool   `json:"is_account_too_young"`
		HasOverdueInvoice bool   `json:"has_overdue_invoices"`
	} `json:"plan"`
}

// Who names the account: its GitHub login, its username, its name, its id.
func (m *Me) Who() string {
	for _, s := range []string{m.User.GitHubLogin, m.User.Username, m.User.Name} {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	if m.User.LegacyID != 0 {
		return strconv.FormatInt(m.User.LegacyID, 10)
	}
	return "Zed account"
}

// Org is the organization the account's models are asked under: its
// default one when it is among its organizations, else its first, as Zed
// picks it.
func (m *Me) Org() string {
	for _, o := range m.Organizations {
		if o.ID == m.DefaultOrganizationID {
			return o.ID
		}
	}
	if len(m.Organizations) > 0 {
		return m.Organizations[0].ID
	}
	return m.DefaultOrganizationID
}

// PlanID is the organization's plan, else the account's.
func (m *Me) PlanID(org string) string {
	if p := m.PlansByOrganization[org]; p != "" {
		return p
	}
	return m.Plan.V3
}

// ModelsOff is set when the organization has Zed's models turned off.
func (m *Me) ModelsOff(org string) bool {
	c, ok := m.ConfigByOrganization[org]
	return ok && c.ModelsEnabled != nil && !*c.ModelsEnabled
}

// PlanName is a plan's id as a reader would name it.
func PlanName(id string) string {
	switch id {
	case "zed_pro":
		return "Pro"
	case "zed_pro_trial":
		return "Pro Trial"
	case "zed_business":
		return "Business"
	case "zed_student":
		return "Student"
	case "zed_vip":
		return "VIP"
	case "zed_free", "":
		return "Free"
	}
	return strings.TrimPrefix(id, "zed_")
}

// cloud sends one /client/* request with the account's pair.
func cloud(ctx context.Context, c *http.Client, base, method, path, uid, token, systemID string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", uid+" "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent())
	if systemID != "" {
		req.Header.Set("x-zed-system-id", systemID)
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode/100 != 2 {
		return nil, &HTTPStatusError{StatusCode: res.StatusCode, Body: string(b), Header: res.Header}
	}
	return b, nil
}

// FetchMe reads the account.
func FetchMe(ctx context.Context, c *http.Client, base, uid, token, systemID string) (*Me, error) {
	b, err := cloud(ctx, c, base, http.MethodGet, "/client/users/me", uid, token, systemID, nil)
	if err != nil {
		return nil, err
	}
	var m Me
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("Zed: an unreadable account: %w", err)
	}
	return &m, nil
}

// LLMToken trades the account's pair for a token the models take.
func LLMToken(ctx context.Context, c *http.Client, base, uid, token, systemID, org string) (string, error) {
	b, err := cloud(ctx, c, base, http.MethodPost, "/client/llm_tokens", uid, token, systemID, map[string]string{"organization_id": org})
	if err != nil {
		return "", err
	}
	var out struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(b, &out) != nil || out.Token == "" {
		return "", errors.New("Zed: no model token in the reply")
	}
	return out.Token, nil
}

// TokenStale says a model request was turned away for its LLM token, which a
// new one would fix.
func TokenStale(status int, h http.Header) bool {
	return status == http.StatusUnauthorized || h.Get("x-zed-expired-token") != "" || h.Get("x-zed-outdated-token") != ""
}

// ---- models ----------------------------------------------------------------

// ModelsHeaders are what a /models request carries beside its token.
func ModelsHeaders(h http.Header, llmToken string) {
	h.Set("Authorization", "Bearer "+llmToken)
	h.Set("x-zed-client-supports-x-ai", "true")
	h.Set("User-Agent", UserAgent())
}

// FetchModels reads GET /models as it came.
func FetchModels(ctx context.Context, c *http.Client, base, llmToken string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, nil, err
	}
	ModelsHeaders(req.Header, llmToken)
	res, err := c.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, nil, err
	}
	if res.StatusCode/100 != 2 {
		return nil, res.Header, &HTTPStatusError{StatusCode: res.StatusCode, Body: string(b), Header: res.Header}
	}
	return b, res.Header, nil
}

// Model is one model /models lists.
type Model struct {
	Provider        string `json:"provider"` // anthropic, open_ai, google, x_ai
	ID              string `json:"id"`
	DisplayName     string `json:"display_name"`
	MaxTokens       int    `json:"max_token_count"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	SupportsImages  bool   `json:"supports_images"`
	SupportsThink   bool   `json:"supports_thinking"`
	Efforts         []struct {
		Value     string `json:"value"`
		IsDefault bool   `json:"is_default"`
	} `json:"supported_effort_levels"`
	Disabled bool `json:"is_disabled"`
}

// Models is GET /models's reply.
type Models struct {
	Models      []Model  `json:"models"`
	Default     string   `json:"default_model"`
	Recommended []string `json:"recommended_models"`
}

// ErrNoModels is a reply that lists nothing callable.
var ErrNoModels = errors.New("Zed lists no models for this account")

// ParseModels reads /models as the catalog's models, the disabled ones left
// out.
func ParseModels(raw []byte) ([]catalog.Model, error) {
	if len(raw) == 0 {
		return nil, ErrNoModels
	}
	var r Models
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("Zed: an unreadable model list: %w", err)
	}
	var out []catalog.Model
	for _, m := range r.Models {
		if m.Disabled || m.ID == "" || Wire(m.Provider) == "" {
			continue
		}
		cm := catalog.Model{ID: m.ID, Name: m.DisplayName, Provider: vendor(m.Provider),
			Context: m.MaxTokens, Output: m.MaxOutputTokens, Images: m.SupportsImages}
		if cm.Name == "" {
			cm.Name = m.ID
		}
		img := m.SupportsImages
		cm.ImageInput = &img
		for _, e := range m.Efforts {
			if e.Value != "" {
				cm.Efforts = append(cm.Efforts, e.Value)
			}
		}
		out = append(out, cm)
	}
	if len(out) == 0 {
		return nil, ErrNoModels
	}
	return out, nil
}

// ProviderOf is the provider /models names for model, "" when it isn't
// listed.
func ProviderOf(raw []byte, model string) string {
	var r Models
	if json.Unmarshal(raw, &r) != nil {
		return ""
	}
	for _, m := range r.Models {
		if m.ID == model {
			return m.Provider
		}
	}
	return ""
}

// Wire is the API a provider's requests are written in: "anthropic",
// "responses", "chat" or "gemini"; "" for one magpie can't write.
func Wire(provider string) string {
	switch provider {
	case "anthropic":
		return "anthropic"
	case "open_ai":
		return "responses"
	case "x_ai":
		return "chat"
	case "google":
		return "gemini"
	}
	return ""
}

// vendor is the models.dev id of a Zed provider.
func vendor(p string) string {
	switch p {
	case "open_ai":
		return "openai"
	case "x_ai":
		return "xai"
	}
	return p
}

// GuessProvider is the provider of a model id that isn't in the list kept.
func GuessProvider(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.HasPrefix(m, "claude"):
		return "anthropic"
	case strings.HasPrefix(m, "gemini"):
		return "google"
	case strings.HasPrefix(m, "grok"):
		return "x_ai"
	}
	return "open_ai"
}

// ---- completions -----------------------------------------------------------

// CompletionURL is where a completion is asked.
func CompletionURL(base string) string { return base + "/completions" }

// CompletionHeaders are what a /completions request carries.
func CompletionHeaders(h http.Header, llmToken string) {
	h.Set("Authorization", "Bearer "+llmToken)
	h.Set("Content-Type", "application/json")
	h.Set("x-zed-version", Version)
	h.Set("x-zed-client-supports-status-messages", "true")
	h.Set("x-zed-client-supports-stream-ended-request-completion-status", "true")
	h.Set("User-Agent", UserAgent())
}

// CompletionBody wraps a provider's own request.
func CompletionBody(provider, model, threadID string, request json.RawMessage) []byte {
	body := map[string]any{"provider": provider, "model": model, "provider_request": request}
	if threadID != "" {
		body["thread_id"] = threadID
	}
	b, _ := json.Marshal(body)
	return b
}

// Line is one line of a completion's stream.
type Line struct {
	Event  json.RawMessage // the provider's own event, when it is one
	Ended  bool            // "stream_ended": the reply is whole
	Failed *Failed         // the request failed along the way
}

// Failed is a failure the stream reports.
type Failed struct {
	Code       string  `json:"code"`
	Message    string  `json:"message"`
	RequestID  string  `json:"request_id"`
	RetryAfter float64 `json:"retry_after"`
}

// Status is the HTTP status a failure stands for: upstream_http_<n> or
// http_<n>, else what its code names.
func (f *Failed) Status() int {
	for _, p := range []string{"upstream_http_", "http_"} {
		if n, err := strconv.Atoi(strings.TrimPrefix(f.Code, p)); err == nil && strings.HasPrefix(f.Code, p) {
			return n
		}
	}
	switch {
	case strings.Contains(f.Code, "rate_limit"):
		return 429
	case strings.Contains(f.Code, "overloaded"):
		return 529
	case strings.Contains(f.Code, "billing") || strings.Contains(f.Code, "payment"):
		return 402
	case strings.Contains(f.Code, "context_length"):
		return 400
	}
	return 502
}

func (f *Failed) Error() string {
	if f.Message != "" {
		return f.Message
	}
	return f.Code
}

// ReadLines reads a completion's stream line by line. wrapped is whether
// the reply said it sends statuses (x-zed-server-supports-status-messages);
// without, each line is a bare event.
func ReadLines(r io.Reader, wrapped bool, fn func(Line) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		if !wrapped {
			if err := fn(Line{Event: append(json.RawMessage(nil), b...)}); err != nil {
				return err
			}
			continue
		}
		var l struct {
			Event  json.RawMessage `json:"event"`
			Status json.RawMessage `json:"status"`
		}
		if err := json.Unmarshal(b, &l); err != nil {
			return fmt.Errorf("an unreadable line: %w", err)
		}
		switch {
		case len(l.Event) > 0:
			if err := fn(Line{Event: l.Event}); err != nil {
				return err
			}
		case len(l.Status) > 0:
			var s string
			if json.Unmarshal(l.Status, &s) == nil {
				if s == "stream_ended" {
					if err := fn(Line{Ended: true}); err != nil {
						return err
					}
				}
				continue // "started"
			}
			var st struct {
				Failed *Failed `json:"failed"`
			}
			if json.Unmarshal(l.Status, &st) == nil && st.Failed != nil {
				if err := fn(Line{Failed: st.Failed}); err != nil {
					return err
				}
			}
			// {"queued": …} and statuses newer than this says nothing to act on
		}
	}
	return sc.Err()
}
