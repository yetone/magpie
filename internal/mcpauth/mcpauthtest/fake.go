// Package mcpauthtest is a remote MCP server behind an OAuth sign-in, as
// Neon's or Notion's is, for the tests of magpie signing in to one (#615).
package mcpauthtest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/mcpauth"
)

// Fake is the server: its MCP endpoint is URL, which takes only a token it
// gave out and hasn't revoked.
type Fake struct {
	*httptest.Server
	URL string // the MCP endpoint, <server>/mcp

	mu sync.Mutex
	// Registered are the clients registration made, as they asked
	Registered []map[string]any
	// Exchanged are the code exchanges, Refreshed the renewals asked
	Exchanged, Refreshed int
	// Calls are the MCP requests that got through: method and JSON-RPC
	// method, with the session they named
	Calls []string
	// Seen are the headers of the last MCP request that got through
	Seen http.Header
	// SeenQuery is the query of the last MCP request that got through, as
	// it came (Exa's ?login is a bare key)
	SeenQuery string
	ExpiresIn int // what a token is said to last, in seconds
	// NoMetadata leaves out the resource metadata: the server of the 2025-03
	// spec, whose authorization server is its own origin
	NoMetadata bool
	// OpenID is an authorization server that is an OpenID provider, as
	// Vercel's is: the server names only the scope "openid" and its 401 none,
	// and a refresh token is given only to a sign-in that asked for
	// offline_access, which the authorization server's metadata offers
	OpenID bool

	codes   map[string]codeGrant
	valid   map[string]bool // access tokens
	refresh map[string]bool
	n       int
}

type codeGrant struct{ challenge, client, redirect, resource, scope string }

// New starts the server.
func New(t *testing.T) *Fake {
	f := &Fake{codes: map[string]codeGrant{}, valid: map[string]bool{}, refresh: map[string]bool{}, ExpiresIn: 3600}
	mux := http.NewServeMux()
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	f.URL = f.Server.URL + "/mcp"
	issuer := f.Server.URL + "/oauth"
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		if f.NoMetadata {
			http.NotFound(w, r)
			return
		}
		scopes := []string{"read", "write"}
		if f.OpenID {
			scopes = []string{"openid"}
		}
		json.NewEncoder(w).Encode(map[string]any{"resource": f.URL, "authorization_servers": []string{issuer}, "scopes_supported": scopes})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server/oauth", func(w http.ResponseWriter, r *http.Request) {
		m := map[string]any{
			"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token",
			"registration_endpoint": issuer + "/register", "code_challenge_methods_supported": []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none"},
		}
		if f.OpenID {
			m["scopes_supported"] = []string{"openid", "email", "offline_access", "profile"}
			m["grant_types_supported"] = []string{"authorization_code", "refresh_token"}
		}
		json.NewEncoder(w).Encode(m)
	})
	register := func(w http.ResponseWriter, r *http.Request) {
		var reg map[string]any
		json.NewDecoder(r.Body).Decode(&reg)
		f.mu.Lock()
		f.Registered = append(f.Registered, reg)
		id := fmt.Sprintf("client-%d", len(f.Registered))
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"client_id": id, "redirect_uris": reg["redirect_uris"]})
	}
	authorize := func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
			http.Error(w, "bad authorize request: "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.n++
		code := fmt.Sprintf("code-%d", f.n)
		f.codes[code] = codeGrant{q.Get("code_challenge"), q.Get("client_id"), q.Get("redirect_uri"), q.Get("resource"), q.Get("scope")}
		f.mu.Unlock()
		back, _ := url.Parse(q.Get("redirect_uri"))
		bq := back.Query()
		bq.Set("code", code)
		bq.Set("state", q.Get("state"))
		back.RawQuery = bq.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	}
	token := func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		fail := func(code string) {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": code})
		}
		offline := !f.OpenID // what a refresh token is given to
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			g, ok := f.codes[r.PostForm.Get("code")]
			delete(f.codes, r.PostForm.Get("code"))
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge || r.PostForm.Get("client_id") != g.client ||
				r.PostForm.Get("redirect_uri") != g.redirect || r.PostForm.Get("resource") != g.resource {
				fail("invalid_grant")
				return
			}
			f.Exchanged++
			offline = offline || slices.Contains(strings.Fields(g.scope), "offline_access")
		case "refresh_token":
			if !f.refresh[r.PostForm.Get("refresh_token")] {
				fail("invalid_grant")
				return
			}
			delete(f.refresh, r.PostForm.Get("refresh_token")) // spent once
			f.Refreshed++
			offline = true
		default:
			fail("unsupported_grant_type")
			return
		}
		f.n++
		a, rt := fmt.Sprintf("access-%d", f.n), fmt.Sprintf("refresh-%d", f.n)
		f.valid[a] = true
		out := map[string]any{"access_token": a, "token_type": "Bearer", "expires_in": f.ExpiresIn}
		if offline {
			f.refresh[rt] = true
			out["refresh_token"] = rt
		}
		json.NewEncoder(w).Encode(out)
	}
	mux.HandleFunc("/oauth/register", register)
	mux.HandleFunc("/oauth/authorize", authorize)
	mux.HandleFunc("/oauth/token", token)
	// the 2025-03 spec's paths at the origin
	mux.HandleFunc("/register", register)
	mux.HandleFunc("/authorize", authorize)
	mux.HandleFunc("/token", token)
	mux.HandleFunc("/mcp", f.mcp)
	return f
}

func (f *Fake) mcp(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	ok := f.valid[tok]
	f.mu.Unlock()
	if !ok {
		meta := f.Server.URL + "/.well-known/oauth-protected-resource/mcp"
		if f.OpenID {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", resource_metadata="`+meta+`"`)
		} else {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token", resource_metadata="`+meta+`", scope="read write"`)
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var msg struct {
		ID     any    `json:"id"`
		Method string `json:"method"`
	}
	body, _ := io.ReadAll(r.Body)
	json.Unmarshal(body, &msg)
	f.mu.Lock()
	f.Calls = append(f.Calls, r.Method+" "+msg.Method+" "+r.Header.Get("Mcp-Session-Id"))
	f.Seen = r.Header.Clone()
	f.SeenQuery = r.URL.RawQuery
	f.mu.Unlock()
	switch r.Method {
	case http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i := 1; i <= 2; i++ {
			fmt.Fprintf(w, "id: %d\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/ping\",\"params\":{\"n\":%d}}\n\n", i, i)
			fl.Flush()
			time.Sleep(20 * time.Millisecond)
		}
	case http.MethodPost:
		w.Header().Set("Mcp-Session-Id", "sess-1")
		id, _ := json.Marshal(msg.ID)
		if msg.Method == "tools/call" {
			// answered as a stream: a progress note, then the result
			w.Header().Set("Content-Type", "text/event-stream")
			fl := w.(http.Flusher)
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
			fl.Flush()
			fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}\n\n", id)
			fl.Flush()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"method":%q}}`, id, msg.Method)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// Revoke makes every access token given out so far unusable, as a server
// that ended them early would.
func (f *Fake) Revoke() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.valid = map[string]bool{}
}

// SignIn signs magpie in to the server as name, standing in for the
// browser: it opens the sign-in page, which sends it back to magpie.
func (f *Fake) SignIn(t *testing.T, name string) mcpauth.State {
	t.Helper()
	return SignIn(t, name, f.URL)
}

// SignIn signs magpie in to the server at u as name, standing in for the
// browser.
func SignIn(t *testing.T, name, u string) mcpauth.State {
	t.Helper()
	st, err := mcpauth.Start(t.Context(), name, u)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(st.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("the sign-in page answered %d: %s", resp.StatusCode, b)
	}
	got, _ := mcpauth.Progress(st.ID)
	if got.State != "done" {
		t.Fatalf("sign-in %s: %s (%s)", got.State, got.Error, b)
	}
	return got
}

// ForgetRefresh makes every refresh token given out so far unusable.
func (f *Fake) ForgetRefresh() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refresh = map[string]bool{}
}
