// Package mcpauth signs magpie in to a remote MCP server that asks for an
// OAuth sign-in (Neon, Notion, Supabase…), once for every agent (#615).
// Each agent would otherwise run the sign-in itself and keep its own token,
// in its own place: magpie signs in, keeps the tokens in mcp-signins.json
// beside its other sign-ins, renews them, and the gateway's /mcp/<name>
// relays the agents' requests to the server with them (gateway/mcp.go).
// The library then gives the agents that address in place of the server's.
//
// The sign-in is the one the MCP spec lays out (2025-06-18, Authorization):
// the server's 401 names its protected resource metadata (RFC 9728), that
// names the authorization server, whose metadata (RFC 8414) says where to
// register a client (RFC 7591), authorize with PKCE (S256) and get tokens.
// A server of the older spec (2025-03-26) with no resource metadata has its
// authorization server at its own origin.
package mcpauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/steady"
)

// Client makes the sign-in's requests, through the proxy magpie's others
// take; tests point it elsewhere.
var Client = &http.Client{Timeout: 30 * time.Second, Transport: proxied()}

func proxied() http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = netproxy.Func
	return netproxy.Dispatch(t)
}

// Changed is called when a server is signed in or out, for the library to
// give the agents its new address.
var Changed func(name string)

// renewLead is how long before it runs out a token is renewed.
var renewLead = 2 * time.Minute

// signInTimeout is how long a sign-in waits for the browser.
var signInTimeout = 10 * time.Minute

// ErrNotSignedIn is a server magpie holds no sign-in for.
var ErrNotSignedIn = errors.New("magpie isn't signed in to this server")

// ErrExpired is a sign-in the server no longer takes: it has to be done again.
var ErrExpired = errors.New("magpie's sign-in to this server has run out: sign in again in the Library")

// ErrNoSignIn is a server that answered an agent's first request without
// asking for a sign-in: it works as it is. One that takes a key as a header
// takes it in Headers; one that signs in only at another address (Exa's
// mcp.exa.ai/mcp?login) is signed in to there. The GUI has it in Chinese.
var ErrNoSignIn = errors.New("this server already works without signing in, so there is nothing to sign in to: the agents given it can use it as it is. A key it takes goes in Headers; a server that signs in at another address (as Exa's ?login) needs that URL")

// Record is magpie's sign-in to one server.
type Record struct {
	URL      string `json:"url"`                // the server's, as the library has it
	Resource string `json:"resource,omitempty"` // what the tokens are for (RFC 8707)
	Issuer   string `json:"issuer,omitempty"`
	TokenURL string `json:"tokenURL"`
	ClientID string `json:"clientID"`
	Secret   string `json:"clientSecret,omitempty"`
	// AuthMethod is how the client authenticates at the token endpoint:
	// none, client_secret_post or client_secret_basic
	AuthMethod string `json:"authMethod,omitempty"`
	Scope      string `json:"scope,omitempty"`
	Access     string `json:"access"`
	Refresh    string `json:"refresh,omitempty"`
	Expires    int64  `json:"expires,omitempty"` // unix ms; 0 when the server didn't say
	At         int64  `json:"at"`                // when it was signed in, unix ms
	// Dead is a sign-in the server refused to renew: the page asks for it
	// again
	Dead bool `json:"dead,omitempty"`
}

var fileMu sync.Mutex

func path() string { return filepath.Join(settings.Dir(), "mcp-signins.json") }

func load() map[string]*Record {
	m := map[string]*Record{}
	if b, err := os.ReadFile(path()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func save(m map[string]*Record) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path(), append(b, '\n'))
}

// writePrivate replaces a file only the user can read, atomically.
func writePrivate(p string, b []byte) error {
	p, err := edit.Target(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return steady.Rename(tmp.Name(), p)
}

func update(f func(m map[string]*Record) bool) error {
	fileMu.Lock()
	defer fileMu.Unlock()
	m := load()
	if !f(m) {
		return nil
	}
	return save(m)
}

// Get is the sign-in to the server by that name.
func Get(name string) (Record, bool) {
	fileMu.Lock()
	defer fileMu.Unlock()
	r := load()[name]
	if r == nil {
		return Record{}, false
	}
	return *r, true
}

// SignedIn says whether magpie holds a sign-in to the server by that name
// at that URL: one to give the agents magpie's address for. One the server
// refused to renew still is, so the agents get magpie's word to sign in
// again rather than a sign-in of their own.
func SignedIn(name, serverURL string) bool {
	r, ok := Get(name)
	return ok && r.URL == serverURL
}

// Status is a server's sign-in as the page shows it.
type Status struct {
	SignedIn bool  `json:"signedIn"`
	Dead     bool  `json:"dead,omitempty"`
	At       int64 `json:"at,omitempty"`
}

// StatusOf is the sign-in to the server by that name at that URL.
func StatusOf(name, serverURL string) Status {
	r, ok := Get(name)
	if !ok || r.URL != serverURL {
		return Status{}
	}
	return Status{SignedIn: true, Dead: r.Dead, At: r.At}
}

// SignOut forgets magpie's sign-in to the server.
func SignOut(name string) error {
	had := false
	err := update(func(m map[string]*Record) bool {
		_, had = m[name]
		delete(m, name)
		return had
	})
	if err == nil && had && Changed != nil {
		Changed(name)
	}
	return err
}

// Forget forgets the sign-in to a server the library no longer has, which
// gives it to no agent: there is nothing to tell.
func Forget(name string) error {
	return update(func(m map[string]*Record) bool {
		_, had := m[name]
		delete(m, name)
		return had
	})
}

// Rename moves a sign-in to the server's new name.
func Rename(old, name string) error {
	return update(func(m map[string]*Record) bool {
		r := m[old]
		if r == nil || old == name {
			return false
		}
		delete(m, old)
		m[name] = r
		return true
	})
}

// ---- tokens ----------------------------------------------------------------

// renewing is one renewal per server at a time: a vendor that hands out a
// new refresh token with each renewal takes the old one only once.
var renewing sync.Map // name → *sync.Mutex

func lockOf(name string) *sync.Mutex {
	m, _ := renewing.LoadOrStore(name, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// Token is the access token for the server by that name, renewed first
// when it runs out within renewLead.
func Token(ctx context.Context, name string) (string, error) {
	r, ok := Get(name)
	if !ok {
		return "", ErrNotSignedIn
	}
	if r.Dead {
		return "", ErrExpired
	}
	if !due(r) {
		return r.Access, nil
	}
	return renew(ctx, name, func(cur Record) bool { return due(cur) })
}

// Renew renews the sign-in the server just refused stale, unless another
// request renewed it meanwhile: then the new token is returned.
func Renew(ctx context.Context, name, stale string) (string, error) {
	return renew(ctx, name, func(cur Record) bool { return cur.Access == stale })
}

func due(r Record) bool {
	return r.Expires != 0 && time.Now().Add(renewLead).UnixMilli() >= r.Expires
}

func renew(ctx context.Context, name string, still func(Record) bool) (string, error) {
	mu := lockOf(name)
	mu.Lock()
	defer mu.Unlock()
	r, ok := Get(name)
	if !ok {
		return "", ErrNotSignedIn
	}
	if r.Dead {
		return "", ErrExpired
	}
	if !still(r) {
		return r.Access, nil
	}
	if r.Refresh == "" {
		markDead(name, r.Access)
		return "", ErrExpired
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {r.Refresh}}
	if r.Resource != "" {
		form.Set("resource", r.Resource)
	}
	t, err := tokenRequest(ctx, r, form)
	if err != nil {
		var te *tokenError
		if errors.As(err, &te) && (te.Code == "invalid_grant" || te.Code == "invalid_client" || te.Status == http.StatusUnauthorized) {
			markDead(name, r.Access)
			return "", ErrExpired
		}
		return "", err
	}
	err = update(func(m map[string]*Record) bool {
		cur := m[name]
		if cur == nil || cur.Access != r.Access {
			return false // signed in again, or out, meanwhile
		}
		cur.Access = t.Access
		if t.Refresh != "" {
			cur.Refresh = t.Refresh
		}
		cur.Expires = t.expires()
		return true
	})
	return t.Access, err
}

func markDead(name, access string) {
	_ = update(func(m map[string]*Record) bool {
		cur := m[name]
		if cur == nil || cur.Access != access {
			return false
		}
		cur.Dead = true
		return true
	})
}

type tokens struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	ExpiresIn int64  `json:"expires_in"`
	Scope     string `json:"scope"`
}

func (t tokens) expires() int64 {
	if t.ExpiresIn <= 0 {
		return 0
	}
	return time.Now().Add(time.Duration(t.ExpiresIn) * time.Second).UnixMilli()
}

type tokenError struct {
	Status      int
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *tokenError) Error() string {
	s := fmt.Sprintf("the token endpoint answered %d", e.Status)
	if e.Code != "" {
		s += ": " + e.Code
	}
	if e.Description != "" {
		s += " (" + e.Description + ")"
	}
	return s
}

// tokenRequest posts form to the record's token endpoint, as its client.
func tokenRequest(ctx context.Context, r Record, form url.Values) (tokens, error) {
	if r.AuthMethod != "client_secret_basic" {
		form.Set("client_id", r.ClientID)
	}
	if r.AuthMethod == "client_secret_post" && r.Secret != "" {
		form.Set("client_secret", r.Secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if r.AuthMethod == "client_secret_basic" {
		req.SetBasicAuth(url.QueryEscape(r.ClientID), url.QueryEscape(r.Secret))
	}
	resp, err := Client.Do(req)
	if err != nil {
		return tokens{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		te := &tokenError{Status: resp.StatusCode}
		_ = json.Unmarshal(b, te)
		return tokens{}, te
	}
	var t tokens
	if err := json.Unmarshal(b, &t); err != nil {
		// a few answer as a form, as GitHub's does
		if v, perr := url.ParseQuery(string(b)); perr == nil && v.Get("access_token") != "" {
			t.Access, t.Refresh, t.Scope = v.Get("access_token"), v.Get("refresh_token"), v.Get("scope")
			fmt.Sscan(v.Get("expires_in"), &t.ExpiresIn)
			return t, nil
		}
		return tokens{}, fmt.Errorf("the token endpoint's answer isn't JSON: %w", err)
	}
	if t.Access == "" {
		return tokens{}, errors.New("the token endpoint gave no access token")
	}
	return t, nil
}

// ---- discovery -------------------------------------------------------------

// meta is what discovery found: where the sign-in goes.
type meta struct {
	Resource     string
	Issuer       string
	AuthorizeURL string
	TokenURL     string
	RegisterURL  string
	Scope        string
	AuthMethods  []string // token_endpoint_auth_methods_supported
}

func origin(u *url.URL) string { return u.Scheme + "://" + u.Host }

// challenge is the server's 401: its resource metadata and the scope it
// asks for, from WWW-Authenticate.
func challenge(h http.Header) (metadata, scope string) {
	for _, v := range h.Values("WWW-Authenticate") {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "bearer") {
			continue
		}
		for k, val := range authParams(strings.TrimSpace(v)[len("bearer"):]) {
			switch strings.ToLower(k) {
			case "resource_metadata":
				metadata = val
			case "scope":
				scope = val
			}
		}
	}
	return
}

// authParams parses a challenge's k=v and k="v" parameters.
func authParams(s string) map[string]string {
	out := map[string]string{}
	for s = strings.TrimSpace(s); s != ""; {
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		k := strings.TrimSpace(strings.TrimLeft(s[:eq], ", "))
		s = strings.TrimSpace(s[eq+1:])
		var v string
		if strings.HasPrefix(s, `"`) {
			var b strings.Builder
			i := 1
			for ; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				b.WriteByte(s[i])
			}
			v, s = b.String(), s[min(i+1, len(s)):]
		} else {
			end := strings.IndexByte(s, ',')
			if end < 0 {
				end = len(s)
			}
			v, s = strings.TrimSpace(s[:end]), s[end:]
		}
		out[k] = v
		s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), ","))
	}
	return out
}

func getJSON(ctx context.Context, u string, v any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	resp, err := Client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return false
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v) == nil
}

const protocolVersion = "2025-06-18"

// discover finds how to sign in to the server at serverURL.
func discover(ctx context.Context, serverURL string) (meta, error) {
	su, err := url.Parse(serverURL)
	if err != nil || su.Host == "" {
		return meta{}, fmt.Errorf("%q isn't a URL", serverURL)
	}
	// ask it something, as an agent would: its 401 says where to sign in
	body := `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"` + protocolVersion + `","capabilities":{},"clientInfo":{"name":"magpie","version":"1"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL, strings.NewReader(body))
	if err != nil {
		return meta{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	resp, err := Client.Do(req)
	if err != nil {
		return meta{}, fmt.Errorf("couldn't reach %s: %w", su.Host, err)
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return meta{}, ErrNoSignIn
	}
	metadataURL, scope := challenge(resp.Header)

	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		Scopes               []string `json:"scopes_supported"`
	}
	var tries []string
	if metadataURL != "" {
		tries = append(tries, metadataURL)
	}
	if p := strings.TrimRight(su.Path, "/"); p != "" {
		tries = append(tries, origin(su)+"/.well-known/oauth-protected-resource"+p)
	}
	tries = append(tries, origin(su)+"/.well-known/oauth-protected-resource")
	found := false
	for _, u := range tries {
		if getJSON(ctx, u, &prm) && len(prm.AuthorizationServers) > 0 {
			found = true
			break
		}
	}
	m := meta{Resource: serverURL, Scope: scope}
	issuer := origin(su)
	if found {
		issuer = strings.TrimRight(prm.AuthorizationServers[0], "/")
		if prm.Resource != "" {
			m.Resource = prm.Resource
		}
		if m.Scope == "" && len(prm.Scopes) > 0 {
			m.Scope = strings.Join(prm.Scopes, " ")
		}
	}
	var as struct {
		Issuer        string   `json:"issuer"`
		Authorize     string   `json:"authorization_endpoint"`
		Token         string   `json:"token_endpoint"`
		Register      string   `json:"registration_endpoint"`
		Challenges    []string `json:"code_challenge_methods_supported"`
		AuthMethods   []string `json:"token_endpoint_auth_methods_supported"`
		ScopesSupport []string `json:"scopes_supported"`
	}
	iu, err := url.Parse(issuer)
	if err != nil || iu.Host == "" {
		return meta{}, fmt.Errorf("the server names an authorization server that isn't a URL (%q)", issuer)
	}
	var asTries []string
	if p := strings.TrimRight(iu.Path, "/"); p != "" {
		asTries = []string{origin(iu) + "/.well-known/oauth-authorization-server" + p,
			origin(iu) + "/.well-known/openid-configuration" + p, issuer + "/.well-known/openid-configuration"}
	} else {
		asTries = []string{issuer + "/.well-known/oauth-authorization-server", issuer + "/.well-known/openid-configuration"}
	}
	got := false
	for _, u := range asTries {
		if getJSON(ctx, u, &as) && as.Authorize != "" && as.Token != "" {
			got = true
			break
		}
	}
	switch {
	case got:
		if len(as.Challenges) > 0 && !contains(as.Challenges, "S256") {
			return meta{}, errors.New("the server's sign-in doesn't offer PKCE (S256), which magpie needs")
		}
		m.Issuer, m.AuthorizeURL, m.TokenURL, m.RegisterURL, m.AuthMethods = as.Issuer, as.Authorize, as.Token, as.Register, as.AuthMethods
		m.Scope = withOffline(m.Scope, as.ScopesSupport)
	case !found:
		// the older spec: the server's origin is its authorization server,
		// at these paths when it has no metadata
		o := origin(su)
		m.Issuer, m.AuthorizeURL, m.TokenURL, m.RegisterURL = o, o+"/authorize", o+"/token", o+"/register"
	default:
		return meta{}, fmt.Errorf("couldn't read the metadata of %s, the server's authorization server", issuer)
	}
	if resp.StatusCode != http.StatusUnauthorized && !found && !got {
		return meta{}, fmt.Errorf("the server answered %d and says nothing of a sign-in", resp.StatusCode)
	}
	return m, nil
}

// withOffline asks for offline_access too when the authorization server
// offers it. An OpenID provider gives a refresh token only to a sign-in that
// asks for it (Vercel's: the server names only "openid", and without
// offline_access its access token runs out in an hour with nothing to renew
// it by, #615); the MCP spec lets a client add it. A sign-in that names no
// scope is left as the server's default.
func withOffline(scope string, supported []string) string {
	if scope == "" || !contains(supported, "offline_access") || contains(strings.Fields(scope), "offline_access") {
		return scope
	}
	return scope + " offline_access"
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// register makes magpie a client of the authorization server, sent back to
// redirect.
func register(ctx context.Context, m meta, redirect string) (id, secret, method string, err error) {
	if m.RegisterURL == "" {
		return "", "", "", errors.New("the server's sign-in doesn't let an app register itself (no dynamic client registration), so magpie can't sign in to it")
	}
	method = "none"
	if len(m.AuthMethods) > 0 && !contains(m.AuthMethods, "none") {
		method = m.AuthMethods[0]
		if !contains(m.AuthMethods, "client_secret_post") && !contains(m.AuthMethods, "client_secret_basic") {
			return "", "", "", fmt.Errorf("the server's token endpoint takes only %s, which magpie can't do", strings.Join(m.AuthMethods, ", "))
		}
		if contains(m.AuthMethods, "client_secret_post") {
			method = "client_secret_post"
		}
	}
	reg := map[string]any{
		"client_name":                "Magpie",
		"client_uri":                 "https://usemagpie.ai",
		"redirect_uris":              []string{redirect},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": method,
	}
	if m.Scope != "" {
		reg["scope"] = m.Scope
	}
	b, _ := json.Marshal(reg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.RegisterURL, bytes.NewReader(b))
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := Client.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", "", "", fmt.Errorf("registering magpie with the server's sign-in failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	var out struct {
		ID     string `json:"client_id"`
		Secret string `json:"client_secret"`
		Method string `json:"token_endpoint_auth_method"`
	}
	if err := json.Unmarshal(rb, &out); err != nil || out.ID == "" {
		return "", "", "", errors.New("the server's sign-in registered magpie but gave it no client id")
	}
	if out.Method != "" {
		method = out.Method
	}
	if out.Secret == "" {
		method = "none"
	} else if method == "none" {
		method = "client_secret_post"
	}
	return out.ID, out.Secret, method, nil
}

// ---- the sign-in -------------------------------------------------------------

// State is where a sign-in stands, for the page to show.
type State struct {
	ID     string `json:"id"`
	Server string `json:"server"`
	URL    string `json:"url,omitempty"` // the sign-in page, to open
	State  string `json:"state"`         // waiting, done, failed or canceled
	Error  string `json:"error,omitempty"`
}

type flow struct {
	mu       sync.Mutex
	st       State
	srv      *http.Server
	done     chan struct{}
	claimed  bool
	finished bool
}

var flows = struct {
	sync.Mutex
	m map[string]*flow
}{m: map[string]*flow{}}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Start begins signing magpie in to the server called name at serverURL:
// open the State's URL in a browser and the rest happens on its own;
// StatusOf follows it.
func Start(ctx context.Context, name, serverURL string) (State, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	m, err := discover(ctx, serverURL)
	if err != nil {
		return State{}, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return State{}, err
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	id, secret, method, err := register(ctx, m, redirect)
	if err != nil {
		ln.Close()
		return State{}, err
	}
	verifier, state := randomToken(48), randomToken(24)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {id},
		"redirect_uri":          {redirect},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"state":                 {state},
		"resource":              {m.Resource},
	}
	if m.Scope != "" {
		q.Set("scope", m.Scope)
	}
	au, err := url.Parse(m.AuthorizeURL)
	if err != nil {
		ln.Close()
		return State{}, fmt.Errorf("the authorization endpoint isn't a URL: %w", err)
	}
	aq := au.Query()
	for k, v := range q {
		aq[k] = v
	}
	au.RawQuery = aq.Encode()

	f := &flow{done: make(chan struct{}), st: State{ID: randomToken(9), Server: name, URL: au.String(), State: "waiting"}}
	rec := Record{URL: serverURL, Resource: m.Resource, Issuer: m.Issuer, TokenURL: m.TokenURL, ClientID: id, Secret: secret, AuthMethod: method, Scope: m.Scope}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		v := r.URL.Query()
		if v.Get("state") != state {
			page(w, http.StatusBadRequest, "This sign-in link is out of date. Start the sign-in again from Magpie.")
			return
		}
		f.mu.Lock()
		if f.claimed || f.finished {
			f.mu.Unlock()
			page(w, http.StatusOK, "Magpie has this sign-in already. You can close this tab.")
			return
		}
		f.claimed = true
		f.mu.Unlock()
		if e := v.Get("error"); e != "" {
			msg := e
			if d := v.Get("error_description"); d != "" {
				msg += ": " + d
			}
			f.finish(State{State: "failed", Error: "the sign-in was refused (" + msg + ")"})
			page(w, http.StatusOK, "The sign-in was refused: "+msg)
			return
		}
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		form := url.Values{"grant_type": {"authorization_code"}, "code": {v.Get("code")}, "redirect_uri": {redirect}, "code_verifier": {verifier}}
		if rec.Resource != "" {
			form.Set("resource", rec.Resource)
		}
		t, err := tokenRequest(cctx, rec, form)
		if err != nil {
			f.finish(State{State: "failed", Error: err.Error()})
			page(w, http.StatusOK, "Magpie couldn't finish the sign-in: "+err.Error())
			return
		}
		r2 := rec
		r2.Access, r2.Refresh, r2.Expires, r2.At = t.Access, t.Refresh, t.expires(), time.Now().UnixMilli()
		if t.Scope != "" {
			r2.Scope = t.Scope
		}
		if err := update(func(m map[string]*Record) bool { m[name] = &r2; return true }); err != nil {
			f.finish(State{State: "failed", Error: err.Error()})
			page(w, http.StatusOK, "Magpie couldn't keep the sign-in: "+err.Error())
			return
		}
		// the agents are given magpie's address before the sign-in is said done
		if Changed != nil {
			Changed(name)
		}
		f.finish(State{State: "done"})
		page(w, http.StatusOK, "Magpie is signed in to "+name+". Every agent given it in the Library uses this sign-in. You can close this tab.")
	})
	f.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go f.srv.Serve(ln)
	flows.Lock()
	flows.m[f.st.ID] = f
	flows.Unlock()
	go func() {
		select {
		case <-f.done:
		case <-time.After(signInTimeout):
			f.finish(State{State: "failed", Error: "the sign-in timed out; start it again"})
		}
	}()
	return f.status(), nil
}

func (f *flow) status() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *flow) finish(st State) {
	f.mu.Lock()
	if f.finished {
		f.mu.Unlock()
		return
	}
	f.finished = true
	f.st.State, f.st.Error = st.State, st.Error
	f.mu.Unlock()
	close(f.done)
	// the browser still has the page to load
	go func() {
		time.Sleep(2 * time.Second)
		f.srv.Close()
	}()
	go func() {
		time.Sleep(10 * time.Minute)
		flows.Lock()
		delete(flows.m, f.st.ID)
		flows.Unlock()
	}()
}

// Progress is a sign-in Start began.
func Progress(id string) (State, bool) {
	flows.Lock()
	f := flows.m[id]
	flows.Unlock()
	if f == nil {
		return State{}, false
	}
	return f.status(), true
}

// Cancel ends a sign-in Start began.
func Cancel(id string) {
	flows.Lock()
	f := flows.m[id]
	flows.Unlock()
	if f != nil {
		f.finish(State{State: "canceled"})
	}
}

func page(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Magpie</title><body style="font:15px -apple-system,system-ui,sans-serif;max-width:32em;margin:4em auto;padding:0 1em;color:#222">%s</body>`, html.EscapeString(msg))
}
