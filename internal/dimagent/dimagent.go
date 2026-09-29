// Package dimagent speaks DimAgent's (dimagent.cn) API: an OAuth 2.0
// authorization-code + PKCE sign-in (a fixed public client and a fixed
// localhost:54321 callback), a model listing at /v1/models?type=dim, an
// account-usage endpoint, and OpenAI chat completions at
// /v1/chat/completions.
//
// The flow, the endpoint paths, the header values and the token semantics
// follow CLIProxyAPI's dimagent integration and the DimAgent desktop app,
// whose requests the upstream recognises by them.
package dimagent

import (
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

const (
	// ProviderKey is how magpie knows this subscription.
	ProviderKey = "dimagent"

	// DefaultBaseURL is the upstream's site, where its OAuth and API live.
	DefaultBaseURL = "https://dimagent.cn"

	// AuthEndpointPath is the OAuth authorization endpoint path.
	AuthEndpointPath = "/oauth/authorize"
	// TokenEndpointPath is the OAuth token endpoint path.
	TokenEndpointPath = "/oauth/token"
	// ModelsPath lists the account's models (dim-scoped ones).
	ModelsPath = "/v1/models?type=dim"
	// ChatCompletionsPath is the OpenAI-compatible chat endpoint.
	ChatCompletionsPath = "/v1/chat/completions"
	// UsagePath is where the account's allowance stands, asked with its
	// access token (the desktop app's "用量" page).
	UsagePath = "/api/me/usage"

	// ClientID is the public OAuth client the desktop app registers with.
	ClientID = "f025fda6d5014fd2b6d4aba45cd8b2b6"
	// RedirectURI is the callback the upstream validates for that client;
	// it is fixed, so the port can't be chosen per run.
	RedirectURI = "http://localhost:54321/auth/callback"
	// CallbackPort is the callback port RedirectURI names.
	CallbackPort = 54321
	// Scope is what the desktop app asks to be authorized for.
	Scope = "openid profile email market.read remote:delegate"
	// SourceParam is the extra authorize query the upstream expects.
	SourceParam = "app"

	// DesktopUserAgent and ChatUserAgent are the User-Agents the upstream
	// sees from the desktop app, on its OAuth and model calls and on chat.
	DesktopUserAgent = "DimAgent-Desktop"
	ChatUserAgent    = "DimAgent/0.9.21"
	// TitleDesktop and TitleChat are the matching x-title values.
	TitleDesktop = "DimAgent"
	TitleChat    = "DimCode"
	// RefererURL is the HTTP-Referer the desktop app sends.
	RefererURL = "https://dimagent.com/"

	// defaultAccessTokenTTL stands in when a token reply says no expires_in.
	defaultAccessTokenTTL = 7 * 24 * time.Hour
)

// TokenResponse is what /oauth/token answers for both the code exchange and
// a refresh.
type TokenResponse struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	Scope        string `json:"scope,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
}

// Expiry is when the access token lapses, read from expires_in; a reply that
// says none gets the seven days the desktop app's tokens run for.
func (t *TokenResponse) Expiry() time.Time {
	ttl := time.Duration(t.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = defaultAccessTokenTTL
	}
	return time.Now().Add(ttl)
}

// Subject is who an account is: the "sub" claim, then account_id, read from
// the access token and then the id token, as the app reads them.
func (t *TokenResponse) Subject() string {
	return claimOf(t, "sub", "account_id", "accountId")
}

// Plan is what plan an account is on, as its token's claims name it. The
// usage reply's subscription record is the richer source (ParseUsage); this
// is what shows before a usage read, and when one says nothing.
func (t *TokenResponse) Plan() string {
	return claimOf(t, "plan_type", "planType", "tier", "subscription_tier")
}

// Nickname is the name the account goes by, the claim the app labels it with.
func (t *TokenResponse) Nickname() string {
	return claimOf(t, "nickname", "name", "preferred_username")
}

// Email is the account's address, when its token says one.
func (t *TokenResponse) Email() string {
	return claimOf(t, "email")
}

// claimOf is the first of several claims both tokens carry, the access
// token's read first — the same order the app's own reader goes in.
func claimOf(t *TokenResponse, keys ...string) string {
	for _, tok := range []string{t.AccessToken, t.IDToken} {
		claims := decodeClaims(tok)
		if claims == nil {
			continue
		}
		for _, k := range keys {
			if s, ok := claims[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// decodeClaims is the middle part of a JWT, read as JSON. A token that isn't
// one, or whose claims aren't an object, says nothing.
func decodeClaims(tok string) map[string]any {
	parts := strings.Split(strings.TrimSpace(tok), ".")
	if len(parts) < 2 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		if raw, err = base64.URLEncoding.DecodeString(parts[1]); err != nil {
			return nil
		}
	}
	var claims map[string]any
	if json.Unmarshal(raw, &claims) != nil {
		return nil
	}
	return claims
}

// PKCECodes is a PKCE pair: the verifier is kept, its S256 digest is what
// the sign-in page sees, and the exchange gives the verifier back.
type PKCECodes struct {
	CodeVerifier  string
	CodeChallenge string
}

// NewPKCE makes a verifier and its S256 challenge, sized as the desktop
// app's are: 64 random bytes base64url'd, and the digest of that string.
func NewPKCE() (*PKCECodes, error) {
	raw := make([]byte, 64)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("dimagent: pkce verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return &PKCECodes{CodeVerifier: verifier, CodeChallenge: base64.RawURLEncoding.EncodeToString(sum[:])}, nil
}

// NewState is a random OAuth state, as the desktop app makes one.
func NewState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("dimagent: state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// baseURL normalizes an override, "" for the upstream's site.
func baseURL(override string) string {
	return strings.TrimRight(strings.TrimSpace(override), "/")
}

// AuthURL is the page to open: the browser signs in there and comes back
// to RedirectURI with a code.
func AuthURL(base, state string, pkce *PKCECodes) (string, error) {
	if pkce == nil || pkce.CodeChallenge == "" {
		return "", fmt.Errorf("dimagent: pkce codes are required")
	}
	if strings.TrimSpace(state) == "" {
		return "", fmt.Errorf("dimagent: state is required")
	}
	b := baseURL(base)
	if b == "" {
		b = DefaultBaseURL
	}
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", ClientID)
	q.Set("redirect_uri", RedirectURI)
	q.Set("scope", Scope)
	q.Set("code_challenge", pkce.CodeChallenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("source", SourceParam)
	return b + AuthEndpointPath + "?" + q.Encode(), nil
}

func tokenURL(base string) string {
	b := baseURL(base)
	if b == "" {
		b = DefaultBaseURL
	}
	return b + TokenEndpointPath
}

// doForm posts an OAuth form and reads the token pair it answers.
func doForm(ctx context.Context, client *http.Client, base string, form url.Values, refresh bool, action string) (*TokenResponse, error) {
	if client == nil {
		client = &http.Client{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL(base), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("dimagent: %s request: %w", action, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if refresh {
		// the desktop app's refresh looks like DimCode's service, not the
		// sign-in page's client: only the referer and its own title.
		req.Header.Set("HTTP-Referer", RefererURL)
		req.Header.Set("X-Title", TitleChat)
	} else {
		req.Header.Set("Accept", "*/*")
		req.Header.Set("User-Agent", DesktopUserAgent)
		req.Header.Set("X-Title", TitleDesktop)
		req.Header.Set("HTTP-Referer", RefererURL)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dimagent %s: %w", action, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("dimagent %s: read response: %w", action, err)
	}
	if res.StatusCode/100 != 2 {
		// the status travels typed as well as said: an account the upstream
		// refused is ended from it, and a hiccup isn't
		return nil, fmt.Errorf("dimagent %s: HTTP %d: %s: %w", action, res.StatusCode, sanitize(body),
			&HTTPStatusError{StatusCode: res.StatusCode})
	}
	var tok TokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("dimagent %s: invalid json: %w", action, err)
	}
	if strings.TrimSpace(tok.AccessToken) == "" {
		return nil, fmt.Errorf("dimagent %s: no access_token: %s", action, sanitize(body))
	}
	return &tok, nil
}

// ExchangeCode trades the code the callback carried for the account's
// tokens, proving it with the PKCE verifier the URL was made from.
func ExchangeCode(ctx context.Context, client *http.Client, base, code string, pkce *PKCECodes) (*TokenResponse, error) {
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("dimagent: code is required")
	}
	if pkce == nil || pkce.CodeVerifier == "" {
		return nil, fmt.Errorf("dimagent: pkce codes are required")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", RedirectURI)
	form.Set("client_id", ClientID)
	form.Set("code_verifier", pkce.CodeVerifier)
	return doForm(ctx, client, base, form, false, "token exchange")
}

// Refresh trades a refresh token for a new pair. The upstream rotates it:
// whatever the reply carries replaces what was spent.
func Refresh(ctx context.Context, client *http.Client, base, refreshToken string) (*TokenResponse, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("dimagent: refresh token is required")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", ClientID)
	return doForm(ctx, client, base, form, true, "token refresh")
}

// HTTPStatusError is an upstream answer that wasn't a success, with its
// status kept so a caller can tell "this sign-in is gone" from a hiccup.
type HTTPStatusError struct{ StatusCode int }

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("dimagent: upstream HTTP %d", e.StatusCode)
}

// Get asks one of the account's JSON endpoints with its access token, the
// way the desktop app does, and answers the body it said. A status that
// isn't a success comes back as an *HTTPStatusError.
//
// The app's "用量" page is the web layer talking to its own server, so it
// says who it is twice over: a Bearer token, and the same token as a cookie.
func Get(ctx context.Context, client *http.Client, url, accessToken string) ([]byte, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("dimagent: access token is required")
	}
	if client == nil {
		client = &http.Client{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("dimagent: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Cookie", "access_token="+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", DesktopUserAgent)
	req.Header.Set("X-Title", TitleDesktop)
	req.Header.Set("HTTP-Referer", RefererURL)
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dimagent: %s: %w", url, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("dimagent: read response: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("dimagent: HTTP %d: %s: %w", res.StatusCode, sanitize(body), &HTTPStatusError{StatusCode: res.StatusCode})
	}
	return body, nil
}

// FetchModels asks for the account's models.
func FetchModels(ctx context.Context, client *http.Client, base, accessToken string) ([]byte, error) {
	b := baseURL(base)
	if b == "" {
		b = DefaultBaseURL
	}
	return Get(ctx, client, b+ModelsPath, accessToken)
}

func sanitize(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 512 {
		s = s[:512] + "..."
	}
	return s
}
