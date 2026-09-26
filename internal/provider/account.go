package provider

// Accounts: agents the user has signed in to, offered as providers.
//
// Claude Code, Codex CLI (ChatGPT), and Copilot logins are subscriptions with
// models behind them. magpie reads the credentials the agent itself keeps on
// disk or in the macOS Keychain, so every other agent can use those models
// through the gateway. Nothing is stored twice: sign out of the agent and the
// provider is gone.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/proc"
)

// Account is the signed-in agent behind a provider.
type Account struct {
	Agent string `json:"agent"`          // the agent's id: codex, copilot
	User  string `json:"user"`           // who is signed in: an email, a GitHub login
	Plan  string `json:"plan,omitempty"` // the subscription, when the agent says

	// Stream is set when the backend only streams; magpie then translates
	// a non-streaming request instead of relaying it.
	Stream bool `json:"-"`

	// Home is where a Grok account keeps its sign-in: the CLI's own home,
	// or one of magpie's for a further account (see grok_accounts.go).
	Home string `json:"-"`

	// token is set on a saved sign-in in use beside the agent's own (see
	// logins_on.go): the access token to run the agent's binary with.
	token func(ctx context.Context) (string, error)

	// codeAssist is where a Gemini CLI or Antigravity account's requests
	// go (google.go).
	codeAssist string

	sign   func(ctx context.Context, req *http.Request, body []byte) error
	body   func(body []byte) []byte // request tweaks the backend insists on
	models func() []catalog.Model
	fetch  func(ctx context.Context) ([]catalog.Model, error)
}

// APIs lists the APIs model is served on, as the provider's last model
// list said: Copilot serves its GPT models on Responses alone and its
// Claude models on Chat and Anthropic's. nil is not known, and every API
// the provider speaks may be tried.
func (p Provider) APIs(model string) []Protocol {
	ms, _, _ := catalog.Live(p.ID)
	for _, m := range ms {
		if m.ID == model && len(m.APIs) > 0 {
			out := make([]Protocol, len(m.APIs))
			for i, a := range m.APIs {
				out[i] = Protocol(a)
			}
			return out
		}
	}
	return nil
}

// Sign authenticates a request to the provider, refreshing what needs it.
// Plain providers get their key; accounts get the agent's tokens.
func (p Provider) Sign(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if p.Account != nil && p.Account.sign != nil {
		return p.Account.sign(ctx, req, body)
	}
	for k, v := range AuthHeaders(p, proto) {
		req.Header.Set(k, v)
	}
	// The user's own headers ride on plain key+URL providers, after auth so
	// they can override a default when a gateway insists on a private scheme.
	// Written to the map directly, not via Set, so the name keeps the exact
	// case the user typed — some gateways match header names case-sensitively.
	for k, v := range p.Headers {
		req.Header[k] = []string{v}
	}
	return nil
}

// Prepare adjusts a request body the way the backend wants it.
func (p Provider) Prepare(body []byte) []byte {
	if p.Account != nil && p.Account.body != nil {
		return p.Account.body(body)
	}
	return body
}

// Exclusion is a sign-in magpie found but will not offer as a provider.
type Exclusion struct {
	Agent    string `json:"agent"`
	Provider string `json:"provider,omitempty"` // set when the user removed it; saving it brings it back
	Why      string `json:"why"`
	// SignedOut: the agent has accounts saved in magpie but isn't signed
	// in where magpie looks, and so none of them is offered.
	SignedOut bool `json:"signedOut,omitempty"`
}

// Excluded lists sign-ins magpie detects but leaves out: the accounts the
// user removed from magpie, and the saved accounts of an agent that isn't
// signed in here (a magpie serve under another HOME, say).
func Excluded() []Exclusion {
	var out []Exclusion
	for _, a := range Hidden() {
		out = append(out, Exclusion{Agent: a.Account.Agent, Provider: a.ID, Why: "You removed it from magpie."})
	}
	return append(out, savedButSignedOut()...)
}

const (
	claudeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	// These values mirror the genuine Claude Code release used by Alma. The
	// installed CLI version wins when it is newer, keeping UA and cc_version in
	// lockstep as Claude's model gates require.
	claudeVersionFloor           = "2.1.280"
	claudeSDKVersion             = "0.112.1"
	claudeRuntimeVersion         = "v22.13.0"
	claudeFingerprintSalt        = "59cf53e54c78"
	claudeCCHSeed         uint64 = 0x6e52736ac806831e
)

var claudeHaikuBetas = "oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,prompt-caching-scope-2026-01-05,claude-code-20250219"
var claudeDefaultBetas = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,prompt-caching-scope-2026-01-05,advanced-tool-use-2025-11-20,effort-2025-11-24"

// claudeBase is Anthropic's API root. A var so tests can point it elsewhere.
var claudeBase = "https://api.anthropic.com"

// claudeTokenURL is where a Claude subscription's OAuth token is refreshed;
// a var so tests can point it elsewhere.
var claudeTokenURL = "https://platform.claude.com/v1/oauth/token"

// claudeKeychain reads credentials from the macOS Keychain; a var so tests
// never touch the machine's own login.
var claudeKeychain = runtime.GOOS == "darwin"

var (
	claudeMu sync.Mutex // serializes refresh; a rotated token is written back once

	claudeStatusMu   sync.Mutex
	claudeStatusAt   time.Time
	claudeStatusUser string
	claudeStatusPlan string
	claudeStatusOut  bool // Claude Code says nobody is signed in

	claudeCacheMu  sync.Mutex
	claudeCacheAt  time.Time
	claudeCacheC   claudeCredentials
	claudeCacheLoc claudeCredentialLocation
	claudeCacheOK  bool
)

// claudeCacheTTL keeps All() from spawning `security` on every gateway
// request while still noticing a fresh login quickly.
const claudeCacheTTL = 10 * time.Second

type claudeAuth struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAt        int64    `json:"expiresAt"` // milliseconds since the Unix epoch
	RefreshExpiresAt int64    `json:"refreshTokenExpiresAt"`
	Scopes           []string `json:"scopes"`
	SubscriptionType string   `json:"subscriptionType"`
	RateLimitTier    string   `json:"rateLimitTier"`
}

// claudeCredentials keeps the whole credential blob in raw, so refreshing a
// token writes back everything else — MCP OAuth state included — untouched.
type claudeCredentials struct {
	raw   map[string]any
	OAuth claudeAuth
}

func parseClaudeCredentials(b []byte) (claudeCredentials, bool) {
	var raw map[string]any
	if json.Unmarshal(b, &raw) != nil {
		return claudeCredentials{}, false
	}
	c := claudeCredentials{raw: raw}
	if o, ok := raw["claudeAiOauth"].(map[string]any); ok {
		ob, _ := json.Marshal(o)
		json.Unmarshal(ob, &c.OAuth)
	}
	return c, c.OAuth.AccessToken != ""
}

func (c claudeCredentials) marshal() ([]byte, error) {
	raw := c.raw
	if raw == nil {
		raw = map[string]any{}
	}
	oauth, _ := raw["claudeAiOauth"].(map[string]any)
	if oauth == nil {
		oauth = map[string]any{}
	}
	oauth["accessToken"] = c.OAuth.AccessToken
	oauth["refreshToken"] = c.OAuth.RefreshToken
	oauth["expiresAt"] = c.OAuth.ExpiresAt
	if c.OAuth.RefreshExpiresAt != 0 {
		oauth["refreshTokenExpiresAt"] = c.OAuth.RefreshExpiresAt
	}
	if len(c.OAuth.Scopes) > 0 {
		oauth["scopes"] = c.OAuth.Scopes
	}
	if c.OAuth.SubscriptionType != "" {
		oauth["subscriptionType"] = c.OAuth.SubscriptionType
	}
	if c.OAuth.RateLimitTier != "" {
		oauth["rateLimitTier"] = c.OAuth.RateLimitTier
	}
	raw["claudeAiOauth"] = oauth
	return json.MarshalIndent(raw, "", "  ")
}

// claudeExpiry reads expiresAt whether a version stored seconds or ms.
func claudeExpiry(v int64) time.Time {
	if v < 1e12 {
		return time.Unix(v, 0)
	}
	return time.UnixMilli(v)
}

type claudeCredentialLocation struct {
	path     string
	account  string
	keychain bool
}

// claudeCredentialsPath is Claude Code's credentials file, where it keeps
// its sign-in off the Mac's keychain.
func claudeCredentialsPath() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, ".credentials.json")
}

func readClaudeCredential() (claudeCredentials, claudeCredentialLocation, bool) {
	path := claudeCredentialsPath()
	if b, err := os.ReadFile(path); err == nil {
		if c, ok := parseClaudeCredentials(b); ok {
			return c, claudeCredentialLocation{path: path}, true
		}
	}
	if !claudeKeychain {
		return claudeCredentials{}, claudeCredentialLocation{}, false
	}
	out, err := proc.Command("security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
	if err != nil {
		return claudeCredentials{}, claudeCredentialLocation{}, false
	}
	b, wasHex := keychainText(bytes.TrimSpace(out))
	c, ok := parseClaudeCredentials(b)
	loc := claudeCredentialLocation{keychain: true, account: claudeKeychainAccount()}
	if ok && wasHex {
		// written by magpie before it wrote them on one line: Claude Code
		// reads that hex as no sign-in, so it is written again as it
		// writes it
		saveClaudeCredential(loc, c)
	}
	return c, loc, ok
}

// claudeKeychainAccount is the account Claude Code keeps its sign-in
// under: $USER, else the login name, and claude-code-user for a name it
// won't use.
func claudeKeychainAccount() string {
	name := os.Getenv("USER")
	if name == "" {
		if u, err := user.Current(); err == nil {
			name = u.Username
		}
	}
	if !keychainAccountRe.MatchString(name) {
		return "claude-code-user"
	}
	return name
}

var keychainAccountRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// keychainText undoes `security find-generic-password -w` printing a
// password with a character it can't print — a newline — as hex.
func keychainText(out []byte) ([]byte, bool) {
	if len(out) == 0 || len(out)%2 != 0 || out[0] == '{' {
		return out, false
	}
	b, err := hex.DecodeString(string(out))
	if err != nil || !json.Valid(b) {
		return out, false
	}
	return b, true
}

func claudeCredential() (claudeCredentials, claudeCredentialLocation, bool) {
	claudeCacheMu.Lock()
	defer claudeCacheMu.Unlock()
	if time.Since(claudeCacheAt) < claudeCacheTTL {
		return claudeCacheC, claudeCacheLoc, claudeCacheOK
	}
	c, loc, ok := readClaudeCredential()
	claudeCacheC, claudeCacheLoc, claudeCacheOK, claudeCacheAt = c, loc, ok, time.Now()
	return c, loc, ok
}

func cacheClaudeCredential(c claudeCredentials, loc claudeCredentialLocation) {
	claudeCacheMu.Lock()
	claudeCacheC, claudeCacheLoc, claudeCacheOK, claudeCacheAt = c, loc, true, time.Now()
	claudeCacheMu.Unlock()
}

// forgetClaudeCredential drops the cache; tests use it between homes.
func forgetClaudeCredential() {
	claudeCacheMu.Lock()
	claudeCacheAt = time.Time{}
	claudeCacheMu.Unlock()
}

func saveClaudeCredential(loc claudeCredentialLocation, c claudeCredentials) error {
	b, err := c.marshal()
	if err != nil {
		return err
	}
	if loc.keychain {
		// on one line, as Claude Code writes it: a password with a newline
		// comes back from `security -w` as hex, which Claude Code takes for
		// no sign-in at all (#70)
		var one bytes.Buffer
		if err := json.Compact(&one, b); err != nil {
			return err
		}
		b = one.Bytes()
	}
	if !loc.keychain {
		if err := os.WriteFile(loc.path, append(b, '\n'), 0o600); err != nil {
			return err
		}
	} else {
		args := []string{"add-generic-password", "-U", "-s", "Claude Code-credentials"}
		if loc.account != "" {
			args = append(args, "-a", loc.account)
		}
		args = append(args, "-w", string(b))
		if out, err := proc.Command("security", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("save Claude Code credentials: %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	cacheClaudeCredential(c, loc)
	return nil
}

// claudeExecutable finds the claude CLI; a var so tests can fake it.
var claudeExecutable = func() string {
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{filepath.Join(home, ".local", "bin", "claude"), "/usr/local/bin/claude", "/opt/homebrew/bin/claude"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// claudeIdentity asks Claude Code itself which account is active. Its credential
// blob intentionally contains tokens and plan metadata but no display identity;
// `claude auth status --json` is the authoritative, non-secret view shown by the
// CLI. Cache it briefly because the providers screen refreshes often.
//
// It also says when Claude Code is signed out even though credentials are
// still lying around (a keychain item logout left behind), so a signed-out
// account stops showing up as a provider.
func claudeIdentity() (user, plan string, signedOut bool) {
	claudeStatusMu.Lock()
	defer claudeStatusMu.Unlock()
	if time.Since(claudeStatusAt) < 30*time.Second {
		return claudeStatusUser, claudeStatusPlan, claudeStatusOut
	}
	claudeStatusAt = time.Now()
	path := claudeExecutable()
	if path == "" {
		return claudeStatusUser, claudeStatusPlan, claudeStatusOut
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// signed out, `claude auth status` exits 1 but still prints the JSON
	out, _ := proc.CommandContext(ctx, path, "auth", "status", "--json").Output()
	var status struct {
		LoggedIn         *bool  `json:"loggedIn"`
		Email            string `json:"email"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if json.Unmarshal(out, &status) != nil || status.LoggedIn == nil {
		return claudeStatusUser, claudeStatusPlan, claudeStatusOut
	}
	claudeStatusOut = !*status.LoggedIn
	claudeStatusUser, claudeStatusPlan = "", ""
	if *status.LoggedIn {
		claudeStatusUser = strings.TrimSpace(status.Email)
		claudeStatusPlan = strings.TrimSpace(status.SubscriptionType)
	}
	return claudeStatusUser, claudeStatusPlan, claudeStatusOut
}

func claudeAccount() (Provider, bool) {
	c, _, ok := claudeCredential()
	if !ok {
		return Provider{}, false
	}
	user, statusPlan, signedOut := claudeIdentity()
	if signedOut {
		return Provider{}, false
	}
	plan := c.OAuth.SubscriptionType
	if statusPlan != "" {
		plan = statusPlan
	}
	if acct, ok := claudeProfileAccount(); ok && user != "" {
		if email, _ := acct["emailAddress"].(string); strings.EqualFold(email, user) {
			user = claudeUser(user, plan, acct)
		}
	}
	if user == "" {
		user = "Claude account"
		if plan != "" {
			user = "Claude " + strings.ToUpper(plan[:1]) + plan[1:]
		}
	}
	acct := &Account{Agent: "claude", User: user, Plan: plan}
	acct.sign = func(ctx context.Context, req *http.Request, body []byte) error {
		tok, err := claudeToken(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("User-Agent", claudeUserAgent())
		req.Header.Set("X-Claude-Code-Session-Id", claudeSessionID())
		req.Header.Set("anthropic-beta", claudeBetaHeader(claudeModelOf(body)))
		req.Header.Set("anthropic-dangerous-direct-browser-access", "true")
		req.Header.Set("x-app", "cli")
		req.Header.Set("x-client-request-id", randomUUID())
		for k, v := range claudeStainlessHeaders() {
			req.Header.Set(k, v)
		}
		return nil
	}
	acct.body = claudeBody
	acct.models = func() []catalog.Model { return catalog.Provider("anthropic") }
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		ms, err := claudeModels(ctx)
		if err != nil {
			return nil, err
		}
		return ms, catalog.SaveLive("claude", claudeBase, ms)
	}
	return Provider{ID: "claude", Name: "Claude Code", Icon: "claudecode-color", Anthropic: claudeBase,
		Catalog: "anthropic", Website: "https://claude.ai", Account: acct}, true
}

// claudeModels asks Anthropic's Models API, authenticated with the account's
// own OAuth token, so the picker follows the vendor instead of a snapshot.
// Nothing here is compiled in: a new model shows up the moment Anthropic
// lists it (which is what the refresh button runs).
func claudeModels(ctx context.Context) ([]catalog.Model, error) {
	tok, err := claudeToken(ctx)
	if err != nil {
		return nil, err
	}
	var out []catalog.Model
	after := ""
	for {
		u := claudeBase + "/v1/models?limit=1000"
		if after != "" {
			u += "&after_id=" + url.QueryEscape(after)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("anthropic-beta", claudeBetaHeader(""))
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", claudeUserAgent())
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, errors.New("Claude model list: " + err.Error())
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Claude model list: %s", res.Status)
		}
		var page struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if json.Unmarshal(b, &page) != nil {
			return nil, errors.New("Claude model list: unexpected payload")
		}
		for _, m := range page.Data {
			if m.ID == "" {
				continue
			}
			name := m.DisplayName
			if name == "" {
				name = m.ID
			}
			out = append(out, catalog.Model{ID: m.ID, Name: name, Provider: "anthropic"})
		}
		if !page.HasMore || page.LastID == "" || page.LastID == after {
			break
		}
		after = page.LastID
	}
	if len(out) == 0 {
		return nil, errors.New("Claude listed no models")
	}
	return out, nil
}

// claudeFresh reports whether a token is usable for the next few minutes.
func claudeFresh(c claudeCredentials) bool {
	return c.OAuth.AccessToken != "" &&
		(c.OAuth.ExpiresAt == 0 || time.Until(claudeExpiry(c.OAuth.ExpiresAt)) > 5*time.Minute)
}

// claudeToken returns a usable access token, refreshing it through Anthropic
// when it is about to expire. A refresh rotates the refresh token, so the new
// pair goes back where Claude Code will look for it.
func claudeToken(ctx context.Context) (string, error) {
	claudeMu.Lock()
	defer claudeMu.Unlock()
	c, loc, ok := claudeCredential()
	if !ok {
		return "", errors.New("Claude Code is signed out; run claude auth login")
	}
	if !claudeFresh(c) {
		// Claude Code itself may have rotated the token since the cache was
		// filled; a stale refresh token would be rejected, so look again.
		if latest, latestLoc, ok := readClaudeCredential(); ok {
			c, loc = latest, latestLoc
		}
	}
	if claudeFresh(c) {
		return c.OAuth.AccessToken, nil
	}
	if err := claudeRefresh(ctx, &c); err != nil {
		return "", err
	}
	if err := saveClaudeCredential(loc, c); err != nil {
		return "", err
	}
	return c.OAuth.AccessToken, nil
}

// refreshRefused is a refresh the vendor answered and turned down: the
// sign-in is gone, where a refresh that got no answer may yet go through.
type refreshRefused string

func (e refreshRefused) Error() string { return string(e) }

// refreshFailed is a refresh that didn't go through: refused when the vendor
// turned the token down (400 invalid_grant, 401), a hiccup otherwise — a 403
// is as likely a proxy or bot check in the way as the vendor's answer.
func refreshFailed(status int, agent, msg string) error {
	if status == http.StatusBadRequest || status == http.StatusUnauthorized {
		return refreshRefused(msg)
	}
	return fmt.Errorf("%s token refresh failed (HTTP %d)", agent, status)
}

// claudeRefresh trades a sign-in's refresh token for a new pair.
func claudeRefresh(ctx context.Context, c *claudeCredentials) error {
	if c.OAuth.RefreshToken == "" {
		return refreshRefused("Claude Code OAuth token expired; run claude auth login")
	}
	body, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": c.OAuth.RefreshToken,
		"client_id": claudeClientID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeTokenURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.New("Claude Code token refresh: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var fresh struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if res.StatusCode != http.StatusOK || json.Unmarshal(b, &fresh) != nil || fresh.AccessToken == "" {
		return refreshFailed(res.StatusCode, "Claude Code", "Claude Code is signed out (token refresh failed); run claude auth login")
	}
	c.OAuth.AccessToken = fresh.AccessToken
	if fresh.RefreshToken != "" {
		c.OAuth.RefreshToken = fresh.RefreshToken
	}
	if fresh.ExpiresIn > 0 {
		c.OAuth.ExpiresAt = time.Now().Add(time.Duration(fresh.ExpiresIn) * time.Second).UnixMilli()
	}
	return nil
}

// Accounts lists the signed-in agents as providers.
func Accounts() []Provider {
	rememberLogins(false)
	home, _ := os.UserHomeDir()
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	var out []Provider
	if p, ok := claudeAccount(); ok {
		out = append(out, p)
	}
	if p, ok := codexAccount(home); ok {
		out = append(out, p)
	}
	if p, ok := copilotAccount(cfg); ok {
		out = append(out, p)
	}
	if p, ok := cursorAccount(); ok {
		out = append(out, p)
	}
	if p, ok := grokAccount(); ok {
		out = append(out, p)
	}
	if p, ok := devinAccount(); ok {
		out = append(out, p)
	}
	if p, ok := kiroAccount(); ok {
		out = append(out, p)
	}
	if p, ok := zcodeAccount(); ok {
		out = append(out, p)
	}
	for _, agent := range []string{"gemini", "antigravity"} {
		if p, ok := googleAccountOf(agent); ok {
			out = append(out, p)
		}
	}
	return out
}

func readJSON(path string, v any) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

// jwtClaims decodes the payload of a JWT without checking it; the
// tokens are the user's own, only their expiry and subject matter here.
func jwtClaims(tok string) map[string]any {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func claimString(m map[string]any, keys ...string) string {
	var v any = m
	for _, k := range keys {
		mm, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = mm[k]
	}
	s, _ := v.(string)
	return s
}

// ---- Codex CLI: a ChatGPT account ----------------------------------------------

const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann" // Codex CLI's own OAuth client

// codexTokenURL is where ChatGPT's tokens are issued and refreshed; a var
// so tests can point it elsewhere.
var codexTokenURL = "https://auth.openai.com/oauth/token"

// CodexBase is where a ChatGPT account's Codex requests go; a var so tests
// can point it elsewhere.
var CodexBase = "https://chatgpt.com/backend-api/codex"

var codexMu sync.Mutex

type codexAuth struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

func codexAccount(home string) (Provider, bool) {
	path := filepath.Join(home, ".codex", "auth.json")
	var a codexAuth
	if !readJSON(path, &a) || a.Tokens.AccessToken == "" || a.AuthMode == "apikey" {
		return Provider{}, false
	}
	id := jwtClaims(a.Tokens.IDToken)
	acct := &Account{Agent: "codex", Stream: true,
		User: codexUser(id), Plan: claimString(id, "https://api.openai.com/auth", "chatgpt_plan_type")}
	if acct.User == "" {
		acct.User = "ChatGPT"
	}
	acct.sign = codexSign(func(ctx context.Context) (string, string, error) { return codexToken(ctx, path) })
	acct.body = codexBody
	acct.models = catalog.Codex
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		ms, err := codexModels(ctx, acct.sign)
		if err != nil {
			return nil, err
		}
		catalog.SaveLive(accountModels("codex", acct.User), CodexBase, ms)
		codexFetchSaved(ctx)
		return ms, catalog.SaveLive("codex", CodexBase, ms)
	}
	return Provider{ID: "codex", Name: "Codex", Icon: "codex-color", Responses: CodexBase, Website: "https://chatgpt.com/codex", Account: acct}, true
}

// codexToken returns a usable access token, refreshing it through OpenAI
// when it is about to expire. A refresh rotates the tokens, so the new
// ones go back into auth.json for Codex CLI to find.
func codexToken(ctx context.Context, path string) (tok, accountID string, err error) {
	codexMu.Lock()
	defer codexMu.Unlock()
	var a codexAuth
	if !readJSON(path, &a) || a.Tokens.AccessToken == "" {
		return "", "", errors.New("Codex is signed out; run codex login")
	}
	accountID = a.Tokens.AccountID
	if accountID == "" {
		accountID = claimString(jwtClaims(a.Tokens.IDToken), "https://api.openai.com/auth", "chatgpt_account_id")
	}
	if exp, _ := jwtClaims(a.Tokens.AccessToken)["exp"].(float64); exp == 0 || time.Until(time.Unix(int64(exp), 0)) > 5*time.Minute {
		return a.Tokens.AccessToken, accountID, nil
	}
	var raw map[string]any
	if !readJSON(path, &raw) {
		return "", "", errors.New("Codex is signed out; run codex login")
	}
	tok, err = codexRefresh(ctx, raw)
	if err != nil {
		return "", "", err
	}
	// keep every other field of the file as Codex CLI wrote it
	if out, err := json.MarshalIndent(raw, "", "  "); err == nil {
		os.WriteFile(path, append(out, '\n'), 0o600)
	}
	return tok, accountID, nil
}

// codexRefresh renews the tokens of an auth.json-shaped sign-in in place,
// every other field left as it was, and returns the new access token.
func codexRefresh(ctx context.Context, raw map[string]any) (string, error) {
	toks, _ := raw["tokens"].(map[string]any)
	if toks == nil {
		toks = map[string]any{}
	}
	refresh, _ := toks["refresh_token"].(string)
	body, _ := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": refresh, "scope": "openid profile email"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexTokenURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", errors.New("Codex token refresh: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var fresh struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if res.StatusCode != 200 || json.Unmarshal(b, &fresh) != nil || fresh.AccessToken == "" {
		return "", refreshFailed(res.StatusCode, "Codex", "Codex is signed out (token refresh failed); run codex login")
	}
	toks["access_token"] = fresh.AccessToken
	if fresh.IDToken != "" {
		toks["id_token"] = fresh.IDToken
	}
	if fresh.RefreshToken != "" {
		toks["refresh_token"] = fresh.RefreshToken
	}
	raw["tokens"] = toks
	raw["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	return fresh.AccessToken, nil
}

// ---- Copilot: a GitHub account -------------------------------------------------

// CopilotTokenURL trades the GitHub OAuth token for a short-lived Copilot
// session token; a var so tests can point it elsewhere.
var CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

const copilotBase = "https://api.githubcopilot.com"

var copilotHeaders = map[string]string{
	"Editor-Version":         "vscode/1.104.0",
	"Editor-Plugin-Version":  "copilot-chat/0.31.0",
	"Copilot-Integration-Id": "vscode-chat",
	"User-Agent":             "GitHubCopilotChat/0.31.0",
}

type copilotSession struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	Endpoints struct {
		API string `json:"api"`
	} `json:"endpoints"`
	direct bool // the Copilot CLI's token, sent as is
}

// headers are what a request with this session carries.
func (s copilotSession) headers() map[string]string {
	if s.direct {
		return copilotCLIHeaders
	}
	return copilotHeaders
}

var (
	copilotMu       sync.Mutex
	copilotSessions = map[string]copilotSession{} // by GitHub token
)

type copilotApp struct {
	User  string `json:"user"`
	Token string `json:"oauth_token"`
	cli   bool   // the standalone Copilot CLI's sign-in
}

// copilotLogin finds the GitHub token Copilot's editors and CLI keep.
func copilotLogin(cfg string) (copilotApp, bool) {
	for _, name := range []string{"apps.json", "hosts.json"} {
		var apps map[string]copilotApp
		if !readJSON(filepath.Join(cfg, "github-copilot", name), &apps) {
			continue
		}
		var keys []string
		for k := range apps {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if strings.HasPrefix(k, "github.com") && apps[k].Token != "" {
				return apps[k], true
			}
		}
	}
	return copilotCLILogin()
}

// session is what this sign-in's requests carry.
func (a copilotApp) session(ctx context.Context) (copilotSession, error) {
	if a.cli {
		return copilotDirect(ctx, a.Token)
	}
	return copilotToken(ctx, a.Token)
}

// copilotProvider is Copilot as one GitHub account serves it.
func copilotProvider(app copilotApp, plan string) Provider {
	acct := &Account{Agent: "copilot", User: app.User, Plan: plan}
	if acct.User == "" {
		acct.User = "GitHub"
	}
	acct.sign = func(ctx context.Context, req *http.Request, body []byte) error {
		s, err := app.session(ctx)
		if err != nil {
			return err
		}
		if s.Endpoints.API != "" {
			if u, err := url.Parse(s.Endpoints.API + req.URL.Path); err == nil {
				req.URL, req.Host = u, u.Host
			}
		}
		copilotAccept(ctx, app, s, bodyModel(body))
		req.Header.Set("Authorization", "Bearer "+s.Token)
		for k, v := range s.headers() {
			req.Header.Set(k, v)
		}
		req.Header.Set("Openai-Intent", "conversation-panel")
		// a turn the user typed is billed as one; a tool's reply is not
		req.Header.Set("X-Initiator", "agent")
		if lastRole(body) == "user" {
			req.Header.Set("X-Initiator", "user")
		}
		if bytes.Contains(body, []byte(`"image_url"`)) || bytes.Contains(body, []byte(`"input_image"`)) || bytes.Contains(body, []byte(`"type":"image"`)) {
			req.Header.Set("Copilot-Vision-Request", "true")
		}
		return nil
	}
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		ms, err := copilotModels(ctx, app)
		if err != nil {
			return nil, err
		}
		return ms, catalog.SaveLive("copilot", copilotBase, ms)
	}
	// each model is served on some of these: the newest GPT models on
	// /responses alone, Claude's on /v1/messages and /chat/completions (its
	// model list says; see Provider.APIs)
	return Provider{ID: "copilot", Name: "Copilot", Icon: "githubcopilot", Chat: copilotBase, Responses: copilotBase, Anthropic: copilotBase, Website: "https://github.com/features/copilot", Account: acct}
}

// bodyModel is the model a request asks for.
func bodyModel(body []byte) string {
	var v struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &v)
	return v.Model
}

// lastRole is the role of the last message in a chat, Anthropic or
// Responses request. Tool results are "tool" in Anthropic's, where they
// ride in a user message, and have none in Responses.
func lastRole(body []byte) string {
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	if len(v.Messages) > 0 {
		last := v.Messages[len(v.Messages)-1]
		var blocks []struct {
			Type string `json:"type"`
		}
		if last.Role == "user" && json.Unmarshal(last.Content, &blocks) == nil && len(blocks) > 0 {
			results := 0
			for _, b := range blocks {
				if b.Type == "tool_result" {
					results++
				}
			}
			if results == len(blocks) {
				return "tool"
			}
		}
		return last.Role
	}
	var text string
	if json.Unmarshal(v.Input, &text) == nil {
		return "user"
	}
	var items []struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(v.Input, &items) != nil || len(items) == 0 {
		return ""
	}
	return items[len(items)-1].Role
}

func copilotToken(ctx context.Context, github string) (copilotSession, error) {
	copilotMu.Lock()
	defer copilotMu.Unlock()
	if s, ok := copilotSessions[github]; ok && time.Until(time.Unix(s.ExpiresAt, 0)) > 2*time.Minute {
		return s, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return copilotSession{}, err
	}
	req.Header.Set("Authorization", "token "+github)
	req.Header.Set("Accept", "application/json")
	for k, v := range copilotHeaders {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return copilotSession{}, errors.New("Copilot sign-in: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var s copilotSession
	if res.StatusCode != 200 || json.Unmarshal(b, &s) != nil || s.Token == "" {
		return copilotSession{}, errors.New("Copilot is signed out (" + APIError(b, res.Status) + "); sign in to Copilot again")
	}
	copilotSessions[github] = s
	return s, nil
}

// copilotAPIs names the APIs of Copilot's supported_endpoints; the
// websocket one is left out, as is anything magpie doesn't speak.
func copilotAPIs(endpoints []string) []string { return catalog.EndpointAPIs(endpoints) }

// internal is a Copilot model id nobody picks by hand.
var copilotInternal = regexp.MustCompile(`^(copilot-search|exec-agent|trajectory)|-(secondary|tertiary|4th|free-auto)$`)

// A model Copilot offers with terms of its own stays disabled until the
// account accepts them, as VS Code does when one is first picked; magpie
// lists it and accepts them the first time a request asks for it.
var (
	copilotTermsMu sync.Mutex
	copilotTerms   = map[string]map[string]bool{} // by GitHub token: models whose terms wait
)

// copilotAccept enables model for the account when its terms still wait.
// A failure is left to the request, whose answer then says why.
func copilotAccept(ctx context.Context, app copilotApp, s copilotSession, model string) {
	if model == "" {
		return
	}
	copilotTermsMu.Lock()
	waiting, known := copilotTerms[app.Token]
	copilotTermsMu.Unlock()
	if !known {
		// not listed since magpie started; the list says which wait
		if _, err := copilotModels(ctx, app); err != nil {
			return
		}
		copilotTermsMu.Lock()
		waiting = copilotTerms[app.Token]
		copilotTermsMu.Unlock()
	}
	if !waiting[model] {
		return
	}
	base := s.Endpoints.API
	if base == "" {
		base = copilotBase
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/models/"+url.PathEscape(model)+"/policy", strings.NewReader(`{"state":"enabled"}`))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.headers() {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	res.Body.Close()
	if res.StatusCode/100 == 2 {
		copilotTermsMu.Lock()
		delete(copilotTerms[app.Token], model)
		copilotTermsMu.Unlock()
	}
}

// copilotModels asks Copilot which chat models this account may use.
func copilotModels(ctx context.Context, app copilotApp) ([]catalog.Model, error) {
	s, err := app.session(ctx)
	if err != nil {
		return nil, err
	}
	base := s.Endpoints.API
	if base == "" {
		base = copilotBase
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	for k, v := range s.headers() {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	var v struct {
		Data []struct {
			ID           string   `json:"id"`
			Name         string   `json:"name"`
			Vendor       string   `json:"vendor"`
			Picker       bool     `json:"model_picker_enabled"`
			Category     string   `json:"model_picker_category"`
			Endpoints    []string `json:"supported_endpoints"`
			Capabilities struct {
				Type     string `json:"type"`
				Supports struct {
					Efforts []string `json:"reasoning_effort"`
				} `json:"supports"`
			} `json:"capabilities"`
			Policy *struct {
				State string `json:"state"`
				Terms string `json:"terms"`
			} `json:"policy"`
		} `json:"data"`
	}
	if res.StatusCode != 200 || json.Unmarshal(b, &v) != nil {
		return nil, errors.New("Copilot models: " + APIError(b, res.Status))
	}
	var out []catalog.Model
	waiting := map[string]bool{}
	for _, m := range v.Data {
		if m.Capabilities.Type != "chat" || copilotInternal.MatchString(m.ID) || m.Vendor == "Experimental" {
			continue
		}
		// a model no picker offers is an old snapshot or Copilot's own
		if !m.Picker && m.Category == "" {
			continue
		}
		switch {
		case m.Policy != nil && m.Policy.State == "enabled", m.Policy == nil && m.Picker:
		case m.Policy != nil && m.Policy.Terms != "":
			waiting[m.ID] = true
		default:
			continue // not the account's to enable: it answers 403
		}
		out = append(out, catalog.Model{ID: m.ID, Name: m.Name, Efforts: m.Capabilities.Supports.Efforts, APIs: copilotAPIs(m.Endpoints)})
	}
	if len(out) == 0 {
		return nil, errors.New("Copilot lists no chat model for this account")
	}
	copilotTermsMu.Lock()
	copilotTerms[app.Token] = waiting
	copilotTermsMu.Unlock()
	return out, nil
}

// forgetClaudeStatus drops what `claude auth status` said; tests use it.
func forgetClaudeStatus() {
	claudeStatusMu.Lock()
	claudeStatusAt, claudeStatusUser, claudeStatusPlan, claudeStatusOut = time.Time{}, "", "", false
	claudeStatusMu.Unlock()
}
