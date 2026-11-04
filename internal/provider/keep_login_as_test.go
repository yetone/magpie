package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
)

// Kept signed in to an account of the user's choosing (#524 follow-up: the
// first is often only the one whose allowance goes first): Codex is signed
// in to it at once and stays there however used up, moved off it it goes
// back, a drag or Make first changes the order and not the sign-in, and let
// go of it, Codex is signed in to the first again.
func TestKeepLoginAsChosen(t *testing.T) {
	home := signIn(t) // me@example.com
	rememberLogins(true)
	codexSignIn(t, home, "spare@example.com", "r-spare")
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	for _, u := range []string{"spare@example.com", "me@example.com"} {
		if err := SetLoginOn("codex", u, true); err != nil {
			t.Fatal(err)
		}
	}
	// the fake's answers read what the test sets next: the usage check
	// runs on a request of its own, so the map is guarded (a list built
	// sooner reached the fake later, and the race detector said so)
	var used struct {
		sync.Mutex
		m map[string]float64
	}
	used.m = map[string]float64{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		used.Lock()
		defer used.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
			"primary_window":   map[string]any{"used_percent": 0, "limit_window_seconds": 18000},
			"secondary_window": map[string]any{"used_percent": used.m[r.Header.Get("chatgpt-account-id")], "limit_window_seconds": 604800}}})
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
	state := func() (active string, order []string) {
		t.Helper()
		for _, l := range Logins("codex") {
			if l.Active {
				active = l.User
			}
			order = append(order, l.User)
		}
		return
	}
	if a, _ := state(); a != "work@example.com" {
		t.Fatalf("signed in to %s", a)
	}

	if err := SetKeepLoginAs("codex", "nobody@example.com"); err == nil {
		t.Fatal("kept on an account not saved")
	}
	// kept on me: signed in to it now, the order as the gateway had it
	if err := SetKeepLoginAs("codex", "me@example.com"); err != nil {
		t.Fatal(err)
	}
	a, order := state()
	if a != "me@example.com" || !slices.Equal(order, []string{"work@example.com", "me@example.com", "spare@example.com"}) {
		t.Fatalf("kept on me: signed in to %s, order %v", a, order)
	}
	if got := InUseLogin("codex"); got != "work@example.com" {
		t.Fatalf("the gateway's first %q", got)
	}
	p, err := Find("codex")
	if err != nil || !p.KeepLogin || p.KeepLoginAs != "me@example.com" {
		t.Fatalf("kept: %+v %v", p, err)
	}
	if r := p.LoginRanks(); r["work@example.com"] != 0 || r["me@example.com"] != 1 || r["spare@example.com"] != 2 {
		t.Fatalf("ranks %v", r)
	}
	// used up, it stays; the setting outlives a routing change
	used.Lock()
	used.m["acct-me@example.com"], used.m["acct-1"] = 100, 100
	used.Unlock()
	if err := SetRouting("codex", Ordered); err != nil {
		t.Fatal(err)
	}
	if to := switched(); to != "" {
		t.Fatalf("kept on me, moved to %s", to)
	}
	// moved off it, back on it
	if err := SwitchLogin("codex", "spare@example.com"); err != nil {
		t.Fatal(err)
	}
	if to := switched(); to != "me@example.com" {
		t.Fatalf("moved off me, back to %q", to)
	}
	// a drag puts spare first: the gateway's order, not the sign-in
	if err := SetAccountOrder("codex", []string{"spare@example.com", "work@example.com", "me@example.com"}); err != nil {
		t.Fatal(err)
	}
	if a, order := state(); a != "me@example.com" || order[0] != "spare@example.com" {
		t.Fatalf("after the drag: signed in to %s, order %v", a, order)
	}
	if got := InUseLogin("codex"); got != "spare@example.com" {
		t.Fatalf("the gateway's first %q", got)
	}
	// kept on the first instead: signed in to spare, the first
	if err := SetKeepLogin("codex", true); err != nil {
		t.Fatal(err)
	}
	if a, _ := state(); a != "spare@example.com" {
		t.Fatalf("kept on the first, signed in to %s", a)
	}
	if p, _ := Find("codex"); !p.KeepLogin || p.KeepLoginAs != "" || p.LoginRanks() != nil {
		t.Fatalf("kept on the first: %+v", p)
	}
}
