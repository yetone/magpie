package mcpauth_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/mcpauth/mcpauthtest"
	"github.com/yetone/magpie/internal/settings"
)

func home(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
}

// Signing in finds the authorization server from the server's 401 (its
// resource metadata, then the server's metadata), registers magpie as a
// client sent back to a loopback address, and trades the code for tokens
// with the PKCE verifier; the tokens are kept where only the user can read
// them.
func TestSignIn(t *testing.T) {
	home(t)
	f := mcpauthtest.New(t)
	if mcpauth.SignedIn("neon", f.URL) {
		t.Fatal("signed in before signing in")
	}
	f.SignIn(t, "neon")
	if len(f.Registered) != 1 || f.Exchanged != 1 {
		t.Fatalf("registered %d, exchanged %d", len(f.Registered), f.Exchanged)
	}
	reg := f.Registered[0]
	redirect, _ := reg["redirect_uris"].([]any)
	if len(redirect) != 1 || !strings.HasPrefix(redirect[0].(string), "http://127.0.0.1:") || !strings.HasSuffix(redirect[0].(string), "/callback") {
		t.Errorf("redirect_uris = %v", reg["redirect_uris"])
	}
	if reg["token_endpoint_auth_method"] != "none" || reg["client_name"] != "Magpie" {
		t.Errorf("registration = %v", reg)
	}
	r, ok := mcpauth.Get("neon")
	if !ok || r.Access == "" || r.Refresh == "" || r.Resource != f.URL || r.ClientID != "client-1" || r.Scope != "read write" || r.Expires == 0 {
		t.Fatalf("record = %+v", r)
	}
	if !mcpauth.SignedIn("neon", f.URL) || mcpauth.SignedIn("neon", f.URL+"/other") || mcpauth.SignedIn("notion", f.URL) {
		t.Error("SignedIn is the name and the URL signed in to")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(settings.Dir(), "mcp-signins.json"))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("mcp-signins.json: %v %v", fi.Mode(), err)
		}
	}
	// a rename takes the sign-in along; signing out forgets it
	if err := mcpauth.Rename("neon", "neon2"); err != nil || !mcpauth.SignedIn("neon2", f.URL) || mcpauth.SignedIn("neon", f.URL) {
		t.Fatal("rename")
	}
	if err := mcpauth.SignOut("neon2"); err != nil || mcpauth.SignedIn("neon2", f.URL) {
		t.Fatal("sign out")
	}
}

// A server of the older spec, with no resource metadata, has its
// authorization server at its own origin, at /authorize, /token and
// /register.
func TestSignInWithoutMetadata(t *testing.T) {
	home(t)
	f := mcpauthtest.New(t)
	f.NoMetadata = true
	f.SignIn(t, "old")
	if f.Exchanged != 1 {
		t.Fatal("no code exchange")
	}
	r, _ := mcpauth.Get("old")
	if r.TokenURL != f.Server.URL+"/token" {
		t.Errorf("token endpoint %s", r.TokenURL)
	}
}

// The sign-in page's URL asks for the code with PKCE (S256), the state, the
// resource and the scope the server's 401 named.
func TestSignInPage(t *testing.T) {
	home(t)
	f := mcpauthtest.New(t)
	st, err := mcpauth.Start(t.Context(), "neon", f.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpauth.Cancel(st.ID)
	u, _ := url.Parse(st.URL)
	q := u.Query()
	if u.Path != "/oauth/authorize" || q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 ||
		q.Get("resource") != f.URL || q.Get("scope") != "read write" || q.Get("client_id") != "client-1" || q.Get("state") == "" {
		t.Errorf("sign-in page %s", st.URL)
	}
	mcpauth.Cancel(st.ID)
	if got, _ := mcpauth.Progress(st.ID); got.State != "canceled" {
		t.Errorf("state %s after Cancel", got.State)
	}
}

// A server that answers without a sign-in has nothing to sign in to.
func TestNothingToSignIn(t *testing.T) {
	home(t)
	f := mcpauthtest.New(t)
	_, err := mcpauth.Start(t.Context(), "open", f.Server.URL+"/.well-known/oauth-protected-resource/mcp")
	if err == nil {
		t.Fatal("signed in to a server that asks for none")
	}
	// said to work as it is, not as a failure to find the sign-in
	if !errors.Is(err, mcpauth.ErrNoSignIn) || !strings.Contains(err.Error(), "already works without signing in") {
		t.Fatalf("error %v", err)
	}
}

// A token that runs out within two minutes is renewed before it is used,
// once however many requests find it due at the same time: a vendor that
// takes a refresh token once would refuse the second.
func TestTokenRenewsBeforeItRunsOut(t *testing.T) {
	home(t)
	f := mcpauthtest.New(t)
	f.ExpiresIn = 60
	f.SignIn(t, "neon")
	old, _ := mcpauth.Get("neon")
	f.ExpiresIn = 3600
	var wg sync.WaitGroup
	got := make([]string, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := mcpauth.Token(t.Context(), "neon")
			if err != nil {
				t.Error(err)
			}
			got[i] = tok
		}()
	}
	wg.Wait()
	if f.Refreshed != 1 {
		t.Fatalf("renewed %d times", f.Refreshed)
	}
	for _, g := range got {
		if g == old.Access || g != got[0] {
			t.Fatalf("tokens %v (old %s)", got, old.Access)
		}
	}
	// one that lasts is used as it is
	if tok, _ := mcpauth.Token(t.Context(), "neon"); tok != got[0] || f.Refreshed != 1 {
		t.Fatal("renewed a token that lasts")
	}
}

// A token the server refused is renewed once, by whichever request saw it
// refused first; the others get the new one.
func TestRenewRefused(t *testing.T) {
	home(t)
	f := mcpauthtest.New(t)
	f.SignIn(t, "neon")
	stale, _ := mcpauth.Token(t.Context(), "neon")
	f.Revoke()
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := mcpauth.Renew(t.Context(), "neon", stale)
			if err != nil || tok == stale {
				t.Error(tok, err)
			}
		}()
	}
	wg.Wait()
	if f.Refreshed != 1 {
		t.Fatalf("renewed %d times", f.Refreshed)
	}
}

// A sign-in the server won't renew has run out: it is said so, and not
// tried again till it is signed in again.
func TestRenewRefusedForGood(t *testing.T) {
	home(t)
	f := mcpauthtest.New(t)
	f.SignIn(t, "neon")
	stale, _ := mcpauth.Token(t.Context(), "neon")
	f.Revoke()
	f.ForgetRefresh()
	if _, err := mcpauth.Renew(t.Context(), "neon", stale); !errors.Is(err, mcpauth.ErrExpired) {
		t.Fatalf("renewing a sign-in the server forgot: %v", err)
	}
	if st := mcpauth.StatusOf("neon", f.URL); !st.SignedIn || !st.Dead {
		t.Fatalf("status %+v", st)
	}
	if _, err := mcpauth.Token(t.Context(), "neon"); !errors.Is(err, mcpauth.ErrExpired) {
		t.Fatalf("token of a dead sign-in: %v", err)
	}
	if f.Refreshed != 0 {
		t.Fatal("a dead sign-in was renewed")
	}
	f.SignIn(t, "neon")
	if st := mcpauth.StatusOf("neon", f.URL); !st.SignedIn || st.Dead {
		t.Fatalf("signed in again: %+v", st)
	}
}

// A token endpoint that takes no public client and no client_secret_post
// is registered with and signed in to by client_secret_basic, whatever it
// lists first (RFC 8414's list has no order of preference): one listing
// private_key_jwt or tls_client_auth before it was asked to register
// magpie for that, which it can't do, and turned the sign-in away.
func TestSignInBySecretBasic(t *testing.T) {
	home(t)
	for _, methods := range [][]string{
		{"private_key_jwt", "client_secret_basic"},
		{"tls_client_auth", "client_secret_jwt", "client_secret_basic"},
		{"client_secret_basic"},
	} {
		f := mcpauthtest.New(t)
		f.AuthMethods = methods
		f.SignIn(t, "neon")
		if got := f.Registered[len(f.Registered)-1]["token_endpoint_auth_method"]; got != "client_secret_basic" {
			t.Fatalf("%v: registered for %v", methods, got)
		}
		if r, _ := mcpauth.Get("neon"); r.AuthMethod != "client_secret_basic" || r.Secret == "" || r.Access == "" || f.Exchanged != 1 {
			t.Fatalf("%v: record %+v, exchanged %d", methods, r, f.Exchanged)
		}
	}
	// client_secret_post is still taken over basic where both are
	f := mcpauthtest.New(t)
	f.AuthMethods = []string{"private_key_jwt", "client_secret_basic", "client_secret_post"}
	f.SignIn(t, "kc")
	if r, _ := mcpauth.Get("kc"); r.AuthMethod != "client_secret_post" || f.Exchanged != 1 {
		t.Fatalf("both: record %+v", r)
	}
}

// A server that registers magpie for a method magpie can't send — RFC
// 7591's registration response lets it assign another of the methods it
// supports than the one asked for — is said no at the registration,
// before the browser is opened, not at the code exchange after it.
func TestSignInRefusesAssignedSecretJWT(t *testing.T) {
	home(t)
	f := mcpauthtest.New(t)
	f.AuthMethods = []string{"client_secret_jwt", "client_secret_basic"}
	f.Assign = "client_secret_jwt"
	st, err := mcpauth.Start(t.Context(), "neon", f.URL)
	if err != nil {
		if !strings.Contains(err.Error(), "can't do") {
			t.Fatalf("signing in to a server that registered magpie for client_secret_jwt: %v", err)
		}
		return
	}
	mcpauth.Cancel(st.ID)
	t.Fatalf("the sign-in page was opened for a client magpie can't authenticate as: %s", st.URL)
}

// An assignment magpie can do is taken, both ways round (#1490): a client
// that asked none and was given a secret for basic or post sends it as
// assigned, and one that asked for a secret method and was registered
// public, with no secret, sends none; at the code exchange and at a
// renewal both, which the fake's token endpoint checks against what it
// assigned.
func TestSignInTakesAnAssignedMethodItCanDo(t *testing.T) {
	home(t)
	for _, c := range []struct {
		listed []string
		assign string
	}{
		{[]string{"none", "client_secret_basic"}, "client_secret_basic"},
		{[]string{"none", "client_secret_post"}, "client_secret_post"},
		{[]string{"client_secret_post"}, "none"},
		{[]string{"client_secret_basic"}, "client_secret_post"},
	} {
		f := mcpauthtest.New(t)
		f.AuthMethods, f.Assign = c.listed, c.assign
		f.SignIn(t, "neon")
		r, _ := mcpauth.Get("neon")
		if r.AuthMethod != c.assign || (r.Secret == "") != (c.assign == "none") || r.Access == "" || f.Exchanged != 1 {
			t.Fatalf("%v assigned %s: record %+v, exchanged %d", c.listed, c.assign, r, f.Exchanged)
		}
		f.Revoke()
		if tok, err := mcpauth.Renew(t.Context(), "neon", r.Access); err != nil || tok == r.Access || f.Refreshed != 1 {
			t.Fatalf("%v assigned %s: renewed to %q, %v (renewals %d)", c.listed, c.assign, tok, err, f.Refreshed)
		}
	}
}

// An authorization server that is an OpenID provider, as Vercel's is, gives
// a refresh token only to a sign-in that asks for offline_access, while the
// server names only "openid": magpie asks for it too when the authorization
// server offers it, so the hour-long token is renewed rather than the
// sign-in running out (#615).
func TestSignInAsksForOfflineAccess(t *testing.T) {
	home(t)
	f := mcpauthtest.New(t)
	f.OpenID = true
	f.ExpiresIn = 60
	st, err := mcpauth.Start(t.Context(), "vercel", f.URL)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(st.URL)
	if q := u.Query().Get("scope"); q != "openid offline_access" {
		t.Errorf("scope %q", q)
	}
	mcpauth.Cancel(st.ID)
	f.SignIn(t, "vercel")
	old, _ := mcpauth.Get("vercel")
	if old.Refresh == "" {
		t.Errorf("no refresh token: %+v", old)
	}
	tok, err := mcpauth.Token(t.Context(), "vercel")
	if err != nil || tok == old.Access || f.Refreshed != 1 {
		t.Fatalf("renewing: %q %v, renewed %d", tok, err, f.Refreshed)
	}
	if st := mcpauth.StatusOf("vercel", f.URL); st.Dead {
		t.Fatal("the sign-in ran out")
	}
	// a server whose authorization server doesn't offer it isn't asked for it
	g := mcpauthtest.New(t)
	st, err = mcpauth.Start(t.Context(), "neon", g.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpauth.Cancel(st.ID)
	if u, _ := url.Parse(st.URL); u.Query().Get("scope") != "read write" {
		t.Errorf("scope %q", u.Query().Get("scope"))
	}
}

// A firewall's 403 page in front of a server is said to be one: it judged
// the network, and the server never got to offer a sign-in. A 403 the
// server gives in its own words is not (fremontdango on Discord: Cloudflare
// at agents.stackoverflow.com, told as "says nothing of a sign-in").
func TestFirewallTurnedAway(t *testing.T) {
	home(t)
	for _, c := range []struct {
		name   string
		header map[string]string
		body   string
		away   bool
	}{
		{"cloudflare page", map[string]string{"Content-Type": "text/html; charset=UTF-8", "Server": "cloudflare"}, "<title>Attention Required! | Cloudflare</title>", true},
		{"cloudflare challenge", map[string]string{"Content-Type": "text/plain", "Cf-Mitigated": "challenge"}, "", true},
		{"server's own 403", map[string]string{"Content-Type": "application/json"}, `{"error":"forbidden"}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range c.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, c.body)
			}))
			defer srv.Close()
			_, err := mcpauth.Start(t.Context(), "sofa", srv.URL+"/mcp")
			if err == nil {
				t.Fatal("signed in past a 403")
			}
			if got := errors.Is(err, mcpauth.ErrTurnedAway); got != c.away {
				t.Fatalf("turned away %v, want %v: %v", got, c.away, err)
			}
			if !c.away && !strings.Contains(err.Error(), "answered 403") {
				t.Fatalf("error %v", err)
			}
		})
	}
}

// A sign-in can be finished by pasting the address the browser was sent
// back to, for a magpie the browser can't reach (magpie on a server, in
// Docker, the page open on another machine): the loopback callback never
// arrives, so magpie takes the code from the pasted address instead
// (@OoopsHh on X). Every sign-in offers this way.
func TestPastedCallbackFinishesSignIn(t *testing.T) {
	home(t)
	f := mcpauthtest.New(t)
	st, err := mcpauth.Start(t.Context(), "neon", f.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpauth.Cancel(st.ID)
	if !st.PasteCallback {
		t.Fatal("a sign-in doesn't offer the pasted-address way")
	}
	// the browser authorizes, but its redirect to the loopback callback
	// never reaches this magpie: capture where it was sent instead of
	// following it there
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Get(st.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back := resp.Header.Get("Location")
	if back == "" {
		t.Fatalf("the sign-in page didn't send the browser back (status %d)", resp.StatusCode)
	}
	// nothing has reached the callback: the sign-in is still waiting
	if got, _ := mcpauth.Progress(st.ID); got.State != "waiting" {
		t.Fatalf("state %s before the address was pasted", got.State)
	}
	if err := mcpauth.Submit(st.ID, back); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got, _ := mcpauth.Progress(st.ID); got.State != "done" {
		t.Fatalf("state %s after the address was pasted", got.State)
	}
	if r, ok := mcpauth.Get("neon"); !ok || r.Access == "" || r.Refresh == "" {
		t.Fatalf("record after paste = %+v", r)
	}
	if f.Exchanged != 1 {
		t.Fatalf("exchanged %d times", f.Exchanged)
	}
}

// A pasted address is taken only when it is this sign-in's own callback —
// its port and path, on loopback, with its state — so a stray or crafted
// address can't finish someone's sign-in.
func TestPastedCallbackRejectsStrangers(t *testing.T) {
	home(t)
	f := mcpauthtest.New(t)
	st, err := mcpauth.Start(t.Context(), "neon", f.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer mcpauth.Cancel(st.ID)
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Get(st.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back := resp.Header.Get("Location")
	u, err := url.Parse(back)
	if err != nil {
		t.Fatal(err)
	}
	wrongState := *u
	q := wrongState.Query()
	q.Set("state", "not-this-signins-state")
	wrongState.RawQuery = q.Encode()
	for _, raw := range []string{
		wrongState.String(), // another sign-in's state
		strings.Replace(back, "127.0.0.1", "10.0.0.5", 1), // not loopback
		"https://" + strings.TrimPrefix(back, "http://"),  // not http
		"not a url",
		"",
	} {
		if err := mcpauth.Submit(st.ID, raw); err == nil {
			t.Errorf("took a stranger: %q", raw)
		}
	}
	// the refusals left the sign-in untouched; its own address still finishes it
	if got, _ := mcpauth.Progress(st.ID); got.State != "waiting" {
		t.Fatalf("state %s after refused pastes", got.State)
	}
	if err := mcpauth.Submit(st.ID, back); err != nil {
		t.Fatalf("own address refused after the strangers: %v", err)
	}
	if got, _ := mcpauth.Progress(st.ID); got.State != "done" {
		t.Fatalf("state %s after the own address was pasted", got.State)
	}
}
