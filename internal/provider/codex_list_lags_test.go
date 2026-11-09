package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// twoPlusAccounts signs Codex in to work@ and keeps me@ beside it, on: the
// two ChatGPT Plus accounts of wudao_85256 on Discord. Magpie speaks as
// Codex 9.0.0 (a client seen through the gateway), so a list asked for an
// older Codex is behind.
func twoPlusAccounts(t *testing.T) Provider {
	t.Helper()
	home := signIn(t) // me@example.com
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true) // Codex on work@, me@ saved
	if err := SetLoginOn("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	ForgetCodexClientsForTest()
	codexSeen.saw("9.0.0")
	t.Cleanup(ForgetCodexClientsForTest)
	p, ok := find(All(), "codex")
	if !ok || p.Account == nil || p.Account.User != "work@example.com" {
		t.Fatalf("codex provider %+v", p)
	}
	return p
}

// keepList writes one account's own model list as magpie keeps it: fetched
// at, asked for Codex version asked (none when ""), and since, when each
// model was first in it (none kept when nil).
func keepList(t *testing.T, user string, at time.Time, asked string, since map[string]time.Time, ids ...string) {
	t.Helper()
	var ms []map[string]any
	for _, id := range ids {
		ms = append(ms, map[string]any{"id": id, "name": id})
	}
	f := map[string]any{"fetched": at, "base": CodexBase, "models": ms, "kinded": true}
	if asked != "" {
		f["asked"] = asked
	}
	if since != nil {
		f["since"] = since
	}
	b, _ := json.Marshal(f)
	path := catalog.LivePath(accountModels("codex", user))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func usersOf(as []AccountOn) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.Account.User)
	}
	return out
}

// An account whose own list leaves a model out is "its plan lacks it"
// only when that list can know the model: fetched after another account's
// list first had it, and asked as the Codex magpie speaks as now. A list
// older than that, or asked for an older Codex (the backend lists
// gpt-6.1-sol only to Codex 0.161.0 and later), or one saved before the
// version was kept, says nothing of it: the account is routed over, not
// left out (wudao_85256 on Discord: Routing said "its plan doesn't list
// gpt-6.1-sol" of the second of two Plus accounts, and nothing failed over
// to it once the first ran out). A Free account's list fetched after the
// Plus one had the model still leaves it out (#520).
func TestStaleAccountListIsNotAPlanWithoutTheModel(t *testing.T) {
	p := twoPlusAccounts(t)
	now := time.Now()
	hourAgo, dayAgo := now.Add(-time.Hour), now.Add(-24*time.Hour)
	// work@, Codex's own: asked now, has had gpt-6.1-sol for an hour
	work := func() {
		keepList(t, "work@example.com", now, "9.0.0",
			map[string]time.Time{"gpt-5.5": dayAgo, "gpt-6.1-sol": hourAgo}, "gpt-5.5", "gpt-6.1-sol")
	}
	for _, c := range []struct {
		name      string
		keep      func()
		left      bool
		wantOrder []string
	}{
		{"asked for an older Codex", func() {
			work()
			keepList(t, "me@example.com", now, "0.159.0", nil, "gpt-5.5")
		}, false, nil},
		{"version not kept", func() {
			work()
			keepList(t, "me@example.com", now, "", nil, "gpt-5.5")
		}, false, nil},
		{"fetched before the model was listed", func() {
			work()
			keepList(t, "me@example.com", dayAgo, "9.0.0", nil, "gpt-5.5")
		}, false, nil},
		{"the other's list saved before since was kept", func() {
			keepList(t, "work@example.com", now, "9.0.0", nil, "gpt-5.5", "gpt-6.1-sol")
			keepList(t, "me@example.com", hourAgo, "9.0.0", nil, "gpt-5.5")
		}, false, nil},
		{"fetched in the same look: its plan lacks it", func() {
			keepList(t, "work@example.com", now, "9.0.0",
				map[string]time.Time{"gpt-5.5": dayAgo, "gpt-6.1-sol": now}, "gpt-5.5", "gpt-6.1-sol")
			keepList(t, "me@example.com", now.Add(-time.Minute), "9.0.0", nil, "gpt-5.5")
		}, true, nil},
		{"fetched since, for this Codex: its plan lacks it", func() {
			work()
			keepList(t, "me@example.com", now, "9.0.0", nil, "gpt-5.5")
		}, true, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.keep()
			out, left, barred := p.AccountsFor("gpt-6.1-sol", nil)
			if len(barred) != 0 {
				t.Fatalf("barred %v", usersOf(barred))
			}
			if c.left {
				if !slices.Equal(usersOf(out), []string{"work@example.com"}) || !slices.Equal(usersOf(left), []string{"me@example.com"}) {
					t.Fatalf("out %v, left %v; want me@ left out", usersOf(out), usersOf(left))
				}
				return
			}
			if len(left) != 0 || len(out) != 2 {
				t.Fatalf("out %v, left %v; want both routed over", usersOf(out), usersOf(left))
			}
			// at their own places: Codex's account first, as before
			if out[0].Account.User != "work@example.com" {
				t.Errorf("order %v", usersOf(out))
			}
		})
	}
}

// A saved account's list asked for an older Codex is asked again at the
// next look, for the version magpie speaks as now, and kept with it; one
// asked for this version waits its day, as before.
func TestBehindListAskedAgain(t *testing.T) {
	twoPlusAccounts(t)
	var mu sync.Mutex
	var asked []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		asked = append(asked, r.URL.Query().Get("client_version"))
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"models": []any{
			map[string]any{"slug": "gpt-5.5", "visibility": "list", "priority": 1},
			map[string]any{"slug": "gpt-6.1-sol", "visibility": "list", "priority": 2},
		}})
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	codexSavedAsked.Lock()
	codexSavedAsked.m = map[string]time.Time{}
	codexSavedAsked.Unlock()
	t.Cleanup(func() {
		codexSavedAsked.Lock()
		codexSavedAsked.m = map[string]time.Time{}
		codexSavedAsked.Unlock()
	})

	keepList(t, "me@example.com", time.Now(), "0.159.0", nil, "gpt-5.5")
	codexFetchSaved(context.Background())
	mu.Lock()
	got := slices.Clone(asked)
	asked = nil
	mu.Unlock()
	if !slices.Equal(got, []string{"9.0.0"}) {
		t.Fatalf("asked %v, want me@'s list asked once for 9.0.0", got)
	}
	key := accountModels("codex", "me@example.com")
	if v := catalog.LiveAsked(key); v != "9.0.0" {
		t.Fatalf("kept as asked for %q", v)
	}
	if _, ok := catalog.LiveSince(key, "gpt-6.1-sol"); !ok {
		t.Fatal("the list asked again lacks gpt-6.1-sol")
	}
	codexFetchSaved(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 0 {
		t.Fatalf("a list asked for this Codex was asked again at once: %v", asked)
	}
}

// A saved account's list fetched before another account's list first had a
// model is asked again at the next look, not at its day's end; one fetched
// in the same look as that list waits its day.
func TestListOlderThanAnothersModelAskedAgain(t *testing.T) {
	twoPlusAccounts(t)
	var mu sync.Mutex
	n := 0
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"models": []any{
			map[string]any{"slug": "gpt-5.5", "visibility": "list", "priority": 1},
		}})
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	forget := func() {
		codexSavedAsked.Lock()
		codexSavedAsked.m = map[string]time.Time{}
		codexSavedAsked.Unlock()
	}
	t.Cleanup(forget)
	now := time.Now()
	for _, c := range []struct {
		name string
		gap  time.Duration
		want int
	}{{"fetched an hour before", time.Hour, 1}, {"fetched in the same look", time.Minute, 0}} {
		t.Run(c.name, func(t *testing.T) {
			forget()
			mu.Lock()
			n = 0
			mu.Unlock()
			keepList(t, "work@example.com", now, "9.0.0",
				map[string]time.Time{"gpt-5.5": now.Add(-48 * time.Hour), "gpt-6.1-sol": now}, "gpt-5.5", "gpt-6.1-sol")
			keepList(t, "me@example.com", now.Add(-c.gap), "9.0.0", nil, "gpt-5.5")
			codexFetchSaved(context.Background())
			mu.Lock()
			defer mu.Unlock()
			if n != c.want {
				t.Fatalf("me@'s list asked %d times, want %d", n, c.want)
			}
		})
	}
}

// Since carries over from save to save: a model first listed yesterday
// keeps yesterday through today's save, a new one gets today, and a list
// saved before Since was kept counts from its own fetch.
func TestLiveSinceCarriesOver(t *testing.T) {
	twoPlusAccounts(t)
	key := accountModels("codex", "me@example.com")
	dayAgo := time.Now().Add(-24 * time.Hour).Truncate(time.Second)
	keepList(t, "me@example.com", dayAgo, "9.0.0", nil, "gpt-5.5")
	if s, ok := catalog.LiveSince(key, "gpt-5.5"); !ok || !s.Equal(dayAgo) {
		t.Fatalf("old list: since %v %v, want its fetch %v", s, ok, dayAgo)
	}
	before := time.Now()
	if err := catalog.SaveLiveAsked(key, CodexBase, "9.0.0", []catalog.Model{{ID: "gpt-5.5"}, {ID: "gpt-6.1-sol"}}); err != nil {
		t.Fatal(err)
	}
	if s, _ := catalog.LiveSince(key, "gpt-5.5"); !s.Equal(dayAgo) {
		t.Errorf("gpt-5.5 since %v, want %v", s, dayAgo)
	}
	if s, ok := catalog.LiveSince(key, "gpt-6.1-sol"); !ok || s.Before(before) {
		t.Errorf("gpt-6.1-sol since %v %v, want now", s, ok)
	}
	if _, ok := catalog.LiveSince(key, "gpt-9"); ok {
		t.Error("a model the list lacks has a since")
	}
}
