package mcpauth_test

import (
	"errors"
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
