package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// WalterAIBuilder on X: Codex's subscription routed Until used up, two
// accounts. The first used up, magpie signed Codex in to the second, and
// signed it back in to the first once that had room again, while the
// gateway, routed Until used up, kept requests on the second as that
// routing says: Codex and the accounts list showed the first in use while
// the second's week was spent. Routed so, Codex's sign-in stays on the
// account it moved to till that is used up too, as the requests do; In
// order still signs it back in to the first.
func TestCodexDrainSignInStaysOnTheNext(t *testing.T) {
	for _, c := range []struct {
		routing string
		back    bool // signed back in to the first once it has room
	}{
		{Drain, false},
		{Ordered, true},
	} {
		t.Run(c.routing, func(t *testing.T) {
			home := signIn(t) // me@example.com, acct-1
			rememberLogins(true)
			codexSignIn(t, home, "spare@example.com", "r-spare")
			rememberLogins(true)
			codexSignIn(t, home, "me@example.com", "r-1")
			rememberLogins(true)
			used := map[string]float64{"acct-me@example.com": 100, "acct-spare@example.com": 20}
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
					"primary_window": map[string]any{"used_percent": used[r.Header.Get("chatgpt-account-id")], "limit_window_seconds": 18000}}})
			}))
			defer fake.Close()
			old := CodexBase
			CodexBase = fake.URL + "/backend-api/codex"
			t.Cleanup(func() { CodexBase = old })
			switched := func() string {
				t.Helper()
				loginUsageCache.Lock()
				loginUsageCache.m = nil
				loginUsageCache.Unlock()
				to, err := SwitchWhenSpent(context.Background(), "codex")
				if err != nil {
					t.Fatal(err)
				}
				return to
			}
			signedIn := func() string {
				t.Helper()
				var live codexAuth
				readJSON(filepath.Join(home, ".codex", "auth.json"), &live)
				return live.Tokens.RefreshToken
			}
			returns := func() bool {
				for _, l := range Logins("codex") {
					if l.Returns {
						return true
					}
				}
				return false
			}
			if err := SetLoginOn("codex", "spare@example.com", true); err != nil {
				t.Fatal(err)
			}
			if err := SetRouting("codex", c.routing); err != nil {
				t.Fatal(err)
			}
			// the first used up: on to the spare
			if to := switched(); to != "spare@example.com" || signedIn() != "r-spare" {
				t.Fatalf("switched to %q, Codex signed in with %q", to, signedIn())
			}
			// the accounts list marks the first as the one gone back to
			// only where magpie goes back to it
			if returns() != c.back {
				t.Fatalf("first marked to go back to: %v, want %v", returns(), c.back)
			}
			// the first has room again
			used["acct-me@example.com"] = 10
			to := switched()
			if c.back {
				if !strings.HasPrefix(to, "me@example.com") || signedIn() != "r-1" {
					t.Fatalf("In order: back to %q, Codex signed in with %q; want the first", to, signedIn())
				}
				return
			}
			if to != "" || signedIn() != "r-spare" {
				t.Fatalf("Until used up: moved off the spare to %q, Codex signed in with %q", to, signedIn())
			}
			// the spare used up in turn: on to the first
			used["acct-spare@example.com"] = 100
			if to := switched(); !strings.HasPrefix(to, "me@example.com") || signedIn() != "r-1" {
				t.Fatalf("spare used up: on to %q, Codex signed in with %q", to, signedIn())
			}
		})
	}
}
