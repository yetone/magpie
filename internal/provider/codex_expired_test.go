package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// expiredBody is the backend's 401 for an access token it calls expired
// while the token's own exp is an hour ahead (willz on Discord, #2973).
const expiredBody = `{"error":{"message":"Provided authentication token is expired. Please try refreshing it.","type":null,"code":"token_expired","param":null},"status":401}`

// codexExpiredFakes signs in me@ (saved) and work@ (Codex's own), both
// with an hour left, and points usage and the token refresh at fakes.
// usage answers each token as refused says; refresh answers each
// refresh token with access token "fresh-<refresh token>", or as
// refuseRefresh says. It returns the refresh tokens sent and the usage
// reads made, by account and token.
func codexExpiredFakes(t *testing.T, refused func(tok string) (int, string), refuseRefresh bool) (home string, refreshes func() []string, reads func() map[string]int) {
	t.Helper()
	home = signIn(t)
	resetRevoked() // marks are read from this HOME's logins.json
	t.Cleanup(resetRevoked)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	loginUsageCache.Lock()
	loginUsageCache.m = nil
	loginUsageCache.Unlock()

	var mu sync.Mutex
	var sent []string
	seen := map[string]int{}
	usage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			w.WriteHeader(404)
			return
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		seen[r.Header.Get("chatgpt-account-id")+" "+tok]++
		mu.Unlock()
		if code, body := refused(tok); code != 0 {
			w.WriteHeader(code)
			w.Write([]byte(body))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "team", "rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": 12, "limit_window_seconds": 18000}}})
	}))
	t.Cleanup(usage.Close)
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		sent = append(sent, body["refresh_token"])
		mu.Unlock()
		if refuseRefresh {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh-" + body["refresh_token"], "refresh_token": body["refresh_token"] + "-2"})
	}))
	t.Cleanup(refresh.Close)
	oldBase, oldURL := CodexBase, codexTokenURL
	CodexBase, codexTokenURL = usage.URL+"/backend-api/codex", refresh.URL
	t.Cleanup(func() { CodexBase, codexTokenURL = oldBase, oldURL })
	refreshes = func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), sent...)
	}
	reads = func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]int{}
		for k, v := range seen {
			out[k] = v
		}
		return out
	}
	return home, refreshes, reads
}

// staleExpired answers every token magpie didn't refresh as expired.
func staleExpired(tok string) (int, string) {
	if strings.HasPrefix(tok, "fresh-") {
		return 0, ""
	}
	return http.StatusUnauthorized, expiredBody
}

func TestCodexUsageRenewsTokenCalledExpired(t *testing.T) {
	home, refreshes, reads := codexExpiredFakes(t, staleExpired, false)

	u := LoginUsage(context.Background(), "codex")
	for _, user := range []string{"me@example.com", "work@example.com"} {
		q := u[user]
		if q.Error != "" || len(q.Windows) != 1 || q.Windows[0].Used != 12 {
			t.Fatalf("%s: %+v", user, q)
		}
	}
	got := refreshes()
	if len(got) != 2 || !(got[0] == "r" && got[1] == "r-work" || got[0] == "r-work" && got[1] == "r") {
		t.Fatalf("refreshed with %q, want r and r-work once each", got)
	}
	if n := reads(); n["acct-1 fresh-r"] != 1 || n["acct-work@example.com fresh-r-work"] != 1 {
		t.Fatalf("reads %v", n)
	}
	// each renewed where it is kept: the saved one in logins.json, Codex's
	// own in its auth.json, every other field as it was
	var live codexAuth
	readJSON(filepath.Join(home, ".codex", "auth.json"), &live)
	if live.Tokens.AccessToken != "fresh-r-work" || live.Tokens.RefreshToken != "r-work-2" || live.Tokens.AccountID != "acct-work@example.com" {
		t.Fatalf("Codex's own sign-in: %+v", live.Tokens)
	}
	for _, l := range readLogins() {
		if l.User == "me@example.com" && !strings.Contains(string(l.Auth), `"fresh-r"`) {
			t.Fatalf("the saved account's refresh wasn't kept: %s", l.Auth)
		}
	}

	// read again: the new tokens go out, with no refresh
	loginUsageCache.Lock()
	loginUsageCache.m = nil
	loginUsageCache.Unlock()
	u = LoginUsage(context.Background(), "codex")
	if u["me@example.com"].Error != "" || u["work@example.com"].Error != "" || len(refreshes()) != 2 {
		t.Fatalf("again: %+v, refreshes %q", u, refreshes())
	}
}

// A refresh refused says to sign in again, once; a token still called
// expired after its refresh is read no third time.
func TestCodexUsageExpiredOnce(t *testing.T) {
	t.Run("refresh refused", func(t *testing.T) {
		_, refreshes, reads := codexExpiredFakes(t, staleExpired, true)
		u := LoginUsage(context.Background(), "codex")
		if e := u["me@example.com"].Error; !strings.Contains(e, "add the account again") {
			t.Fatalf("saved account: %q", e)
		}
		// signed in again in magpie, not with codex login (charliepu)
		if e := u["work@example.com"].Error; e != revokedText("work@example.com") {
			t.Fatalf("Codex's own account: %q", e)
		}
		if n := len(refreshes()); n != 2 {
			t.Fatalf("%d refreshes, want one each", n)
		}
		for k, n := range reads() {
			if n != 1 {
				t.Fatalf("%s read %d times", k, n)
			}
		}
	})
	t.Run("still expired", func(t *testing.T) {
		always := func(string) (int, string) { return http.StatusUnauthorized, expiredBody }
		_, refreshes, reads := codexExpiredFakes(t, always, false)
		u := LoginUsage(context.Background(), "codex")
		for _, user := range []string{"me@example.com", "work@example.com"} {
			// signed out for the refreshed token too (charliepu)
			if e := u[user].Error; e != revokedText(user) {
				t.Fatalf("%s: %q", user, e)
			}
		}
		if n := len(refreshes()); n != 2 {
			t.Fatalf("%d refreshes, want one each", n)
		}
		if n := reads(); n["acct-1 fresh-r"] != 1 || n["acct-work@example.com fresh-r-work"] != 1 {
			t.Fatalf("reads %v", n)
		}
	})
}

// Any other 401 isn't the token's age: nothing is refreshed for it.
func TestCodexUsageOther401NotRenewed(t *testing.T) {
	other := func(string) (int, string) {
		return http.StatusUnauthorized, `{"error":{"message":"Unauthorized","code":"unauthorized"}}`
	}
	_, refreshes, _ := codexExpiredFakes(t, other, false)
	u := LoginUsage(context.Background(), "codex")
	if u["me@example.com"].Error != "ChatGPT answered the usage read with HTTP 401 Unauthorized: Unauthorized" || len(refreshes()) != 0 {
		t.Fatalf("%+v, refreshes %q", u, refreshes())
	}
}

func TestSignInExpiredWords(t *testing.T) {
	for body, want := range map[string]bool{
		expiredBody: true,
		`{"detail":"Your authentication token has expired. Please try signing in again."}`: true,
		`{"error":{"code":"refresh_token_expired"}}`:                                       false,
		`{"error":{"code":"token_revoked"}}`:                                               false,
		`Unauthorized`:                                                                     false,
	} {
		if got := SignInExpired(http.StatusUnauthorized, []byte(body)); got != want {
			t.Errorf("%s: %v", body, got)
		}
	}
	if SignInExpired(http.StatusForbidden, []byte(expiredBody)) {
		t.Error("a 403")
	}
}

// The gateway's 401 token expired renews the account the request went
// with: the saved one in logins.json, and only while the token sent is
// the one still saved.
func TestNoteExpiredSignIn(t *testing.T) {
	_, refreshes, _ := codexExpiredFakes(t, staleExpired, false)
	if err := SetLoginOn("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	also := p.AlsoOn()
	if len(also) != 1 || also[0].Account.User != "me@example.com" {
		t.Fatalf("also on: %+v", also)
	}
	a := also[0].Account
	req, _ := http.NewRequest("POST", "http://x", nil)
	if err := a.sign(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	stale := req.Header.Get("Authorization")
	NoteExpiredSignIn(context.Background(), a, req)
	NoteExpiredSignIn(context.Background(), a, req) // a second reply for the same token
	if got := refreshes(); len(got) != 1 || got[0] != "r" {
		t.Fatalf("refreshed with %q", got)
	}
	req, _ = http.NewRequest("POST", "http://x", nil)
	if err := a.sign(context.Background(), req, nil); err != nil || req.Header.Get("Authorization") != "Bearer fresh-r" || stale == "Bearer fresh-r" {
		t.Fatalf("next request signed %q (was %q), %v", req.Header.Get("Authorization"), stale, err)
	}
}
