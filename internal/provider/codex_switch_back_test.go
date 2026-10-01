package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// The account the user made first is signed back in once it has room
// again, after magpie signed Codex in to another while it was spent: the
// one made first stays first (#408). A switch the user makes in the
// meantime is theirs, and magpie doesn't undo it.
func TestCodexSwitchedBackWhenItHasRoom(t *testing.T) {
	home := signIn(t) // me@example.com, acct-1
	rememberLogins(true)
	codexSignIn(t, home, "spare@example.com", "r-spare")
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	if err := SetLoginOn("codex", "spare@example.com", true); err != nil {
		t.Fatal(err)
	}
	used := map[string]float64{"acct-work@example.com": 99, "acct-1": 0, "acct-spare@example.com": 20}
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
	returns := func(user string) bool {
		t.Helper()
		for _, l := range Logins("codex") {
			if l.User == user {
				return l.Returns
			}
		}
		t.Fatalf("no %s", user)
		return false
	}

	if to := switched(); to != "spare@example.com" {
		t.Fatalf("switched to %q", to)
	}
	if !returns("work@example.com") || returns("spare@example.com") {
		t.Fatalf("the account to go back to isn't told: %+v", Logins("codex"))
	}
	// still low: it stays on the spare
	used["acct-work@example.com"] = 95
	if to := switched(); to != "" {
		t.Fatalf("switched to %s while the first is still low", to)
	}
	// renewed: back on the first
	used["acct-work@example.com"] = 3
	if to := switched(); to != "work@example.com" {
		t.Fatalf("switched to %q once the first has room", to)
	}
	if signedIn() != "r-work" || returns("work@example.com") {
		t.Fatalf("after going back: signed in with %s, %+v", signedIn(), Logins("codex"))
	}
	if to := switched(); to != "" {
		t.Fatalf("switched again, to %s", to)
	}

	// spent again, then the user signs Codex in to another account: that
	// one is theirs, and the first isn't signed back in
	used["acct-work@example.com"] = 100
	if to := switched(); to != "spare@example.com" {
		t.Fatalf("switched to %q", to)
	}
	if err := SwitchLogin("codex", "me@example.com"); err != nil {
		t.Fatal(err)
	}
	if returns("work@example.com") {
		t.Fatalf("still going back after the user's switch: %+v", Logins("codex"))
	}
	used["acct-work@example.com"] = 0
	if to := switched(); to != "" {
		t.Fatalf("undid the user's switch, to %s", to)
	}
	if signedIn() != "r" {
		t.Fatalf("after the user's switch: signed in with %s", signedIn())
	}
}
