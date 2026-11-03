package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A reply that says the account Codex is signed in to is out has its
// allowance read again at once, not at the next look five minutes on
// (MasterYi on Discord: the Codex app greyed Send meanwhile): once per
// codexRecheckEvery, for the active account alone, and only in a magpie
// that keeps Codex on an account with room.
func TestCodexMayBeUsedUpReadsAtOnce(t *testing.T) {
	signIn(t) // me@example.com, active
	rememberLogins(true)
	reads := make(chan string, 8)
	oldRead, oldEvery := codexReadNow, codexRecheckEvery
	codexReadNow = func(user string) { reads <- user }
	t.Cleanup(func() {
		codexReadNow, codexRecheckEvery = oldRead, oldEvery
		codexLiveChecks.Store(0)
		codexRecheck.Lock()
		codexRecheck.at = time.Time{}
		codexRecheck.Unlock()
	})
	none := func(when string) {
		t.Helper()
		select {
		case u := <-reads:
			t.Fatalf("%s: read %q", when, u)
		case <-time.After(50 * time.Millisecond):
		}
	}
	read := func(when string) {
		t.Helper()
		select {
		case u := <-reads:
			if u != "me@example.com" {
				t.Fatalf("%s: read %q", when, u)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: not read", when)
		}
	}
	full := []CodexLimit{{Used: 100, Span: 5 * time.Hour}}

	NoteCodexLimits("codex", "me@example.com", full)
	none("live checks off")

	codexLiveChecks.Store(1)
	NoteCodexLimits("codex", "me@example.com", []CodexLimit{{Used: 99, Span: 5 * time.Hour}})
	none("a window with room")
	NoteCodexLimits("codex", "other@example.com", full)
	none("an account Codex isn't on")
	NoteCodexLimits("codex", "me@example.com", full)
	read("a window full")
	CodexMayBeUsedUp("me@example.com")
	none("again at once")

	codexRecheckEvery = 0
	CodexMayBeUsedUp("ME@example.com")
	read("usage_limit_reached")
}

// The reading a reply's word asks for signs Codex in to the next account
// with room as soon as the one it is on is out, not at the loop's next
// look up to five minutes on: the Codex app greys Send once it is out, so
// no request comes to tell magpie again (danzi002 on Discord restarted the
// app every time instead).
func TestCodexReadNowSwitchesAtOnce(t *testing.T) {
	home := signIn(t) // me@example.com, acct-1
	rememberLogins(true)
	codexSignIn(t, home, "new@example.com", "r-new") // signed in to now
	rememberLogins(true)
	used := map[string]float64{"acct-new@example.com": 100, "acct-1": 20}
	reset := time.Now().Add(3 * time.Hour).Unix()
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
			"allowed":        used[r.Header.Get("chatgpt-account-id")] < 100,
			"primary_window": map[string]any{"used_percent": used[r.Header.Get("chatgpt-account-id")], "limit_window_seconds": 18000, "reset_at": reset}}})
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	if err := SetLoginOn("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { codexWasUsedUp.Store(false) })
	codexReadNow("new@example.com")
	for _, l := range Logins("codex") {
		if l.Active && !strings.EqualFold(l.User, "me@example.com") {
			t.Fatalf("out, with me@example.com at 20%%: Codex left on %s", l.User)
		}
	}
}

// Whether magpie has anything to serve Codex's own model on while the
// account Codex is signed in to is out (xiaoq6285 on Discord, one Plus
// account): with that account alone and no fallback, nothing; another
// ChatGPT account not known out, or a fallback on the Codex
// subscription, is something. Another account known out is not.
func TestCodexServesWhileOut(t *testing.T) {
	home := signIn(t) // me@example.com
	rememberLogins(true)
	if CodexServesWhileOut("gpt-5.5") {
		t.Fatal("one account, no fallback: serves")
	}
	if CodexServesWhileOut("") {
		t.Fatal("no model: serves")
	}
	codexSignIn(t, home, "spare@example.com", "r-spare")
	rememberLogins(true)
	var active, other string
	for _, l := range Logins("codex") {
		if l.Active {
			active = l.User
		} else {
			other = l.User
		}
	}
	if other == "" {
		t.Fatalf("no second account: %+v", Logins("codex"))
	}
	loginUsageCache.Lock()
	oldCache := loginUsageCache.m
	loginUsageCache.m = map[string]loginUsageEntry{
		"codex/" + active: {at: time.Now(), q: SubscriptionQuota{Provider: "codex", User: active, Held: true}},
	}
	loginUsageCache.Unlock()
	t.Cleanup(func() {
		loginUsageCache.Lock()
		loginUsageCache.m = oldCache
		loginUsageCache.Unlock()
	})
	if !CodexServesWhileOut("gpt-5.5") {
		t.Fatalf("another account (%s), not known out: doesn't serve", other)
	}
	loginUsageCache.Lock()
	loginUsageCache.m["codex/"+other] = loginUsageEntry{at: time.Now(), q: SubscriptionQuota{Provider: "codex", User: other, Held: true}}
	loginUsageCache.Unlock()
	if CodexServesWhileOut("gpt-5.5") {
		t.Fatal("every account out, no fallback: serves")
	}
	if err := Save(Provider{ID: "fb", Name: "FB", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "codex", Fallback: []string{"fb/m"}}); err != nil {
		t.Fatal(err)
	}
	if !CodexServesWhileOut("gpt-5.5") {
		t.Fatal("a fallback on the Codex subscription: doesn't serve")
	}
}
