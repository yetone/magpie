package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func grokSignedIn(t *testing.T, home, email string) {
	t.Helper()
	writeFile(t, filepath.Join(home, "auth.json"), map[string]any{
		"https://auth.x.ai": map[string]any{"key": "k-" + email, "email": email, "expires_at": time.Now().Add(time.Hour)},
	})
}

// Grok has its own allowance cache, reached through the same public entry
// point. A cache hit after a fetch starts must share its result lock.
func TestLoginUsageGrokMixedCache(t *testing.T) {
	home := signIn(t)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	grokSignedIn(t, GrokHome(), "me@x.ai")
	extra, err := newGrokHome()
	if err != nil {
		t.Fatal(err)
	}
	grokSignedIn(t, extra, "two@x.ai")
	if _, err := addGrokLogin(extra); err != nil {
		t.Fatal(err)
	}
	ls := grokLogins()
	if len(ls) != 2 {
		t.Fatalf("logins: %+v", ls)
	}
	// Keep the cached account last regardless of how logins are ordered,
	// so its map write follows the uncached account's go statement.
	fetched, cached := ls[0], ls[len(ls)-1]
	grokHomeUsage.Lock()
	oldCache := grokHomeUsage.m
	grokHomeUsage.m = map[string]loginUsageEntry{
		cached.Home: {at: time.Now(), q: SubscriptionQuota{Provider: "grok",
			Windows: []QuotaWindow{{Name: "7 days", Used: 97}}}},
	}
	grokHomeUsage.Unlock()
	t.Cleanup(func() {
		grokHomeUsage.Lock()
		grokHomeUsage.m = oldCache
		grokHomeUsage.Unlock()
	})

	var hits atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/billing" || r.URL.Query().Get("format") != "credits" ||
			r.Header.Get("Authorization") != "Bearer k-"+fetched.User {
			t.Errorf("unexpected usage request: %s", r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"config":{"creditUsagePercent":12,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY"}}}`))
	}))
	defer fake.Close()
	old := GrokBase
	GrokBase = fake.URL
	t.Cleanup(func() { GrokBase = old })

	for i := range 2 { // the fetched account is cached on the second read
		u := LoginUsage(context.Background(), "grok")
		if len(u) != 2 {
			t.Fatalf("read %d: usage for %d accounts, want 2", i, len(u))
		}
		for user, want := range map[string]float64{fetched.User: 12, cached.User: 97} {
			q := u[user]
			if q.Error != "" || len(q.Windows) != 1 || q.Windows[0].Used != want {
				t.Fatalf("read %d: %s usage %+v, want %v%%", i, user, q, want)
			}
		}
		if got := hits.Load(); got != 1 {
			t.Fatalf("read %d: %d requests, want only the uncached account fetched once", i, got)
		}
	}
}

// The Usage page's whole-page read of a Grok account and the per-home read
// LoginUsage makes of the same account write the one key on disk: the page's
// started first, so its late answer is the older reading, and must not
// become what the next failed read falls back to.
func TestGrokPerHomeReadKeepsTheNewerReading(t *testing.T) {
	home := signIn(t)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	grokSignedIn(t, GrokHome(), "grok@example.com")
	reset := func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
		grokHomeUsage.Lock()
		grokHomeUsage.m = nil
		grokHomeUsage.Unlock()
	}
	reset()
	t.Cleanup(reset)

	// what the CLI's own /usage says, and whether the vendor is refusing it:
	// the httptest handler reads both off its own goroutine, while the test
	// writes them here
	used, refused := atomic.Int32{}, atomic.Bool{}
	used.Store(80)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if refused.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, `{"config":{"creditUsagePercent":%d,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY"}}}`, used.Load())
	}))
	t.Cleanup(srv.Close)
	oldBase := GrokBase
	GrokBase = srv.URL
	t.Cleanup(func() { GrokBase = oldBase })

	// the page's read starts first
	pageCtx, _ := quotaReading(context.Background())

	used.Store(80) // the per-home read, started later, answers first
	u := grokLoginUsage(context.Background())
	if got := u["grok@example.com"].Windows[0].Used; got != 80 {
		t.Fatalf("per-home read %v%%, want 80", got)
	}

	used.Store(10) // the page's answer: started first, so the older reading
	keepReading(pageCtx, readNow(grokSubscriptionUsage(pageCtx)), "")

	c := &lastQuotas
	c.Lock()
	kept := c.m["grok/grok@example.com"].Q
	c.Unlock()
	if kept.Windows[0].Used != 80 {
		t.Fatalf("last reading on disk is %v%%, want the newer 80", kept.Windows[0].Used)
	}

	// the vendor has a hiccup: the last reading kept is what the account
	// falls back to, the file read again as after a restart
	c.Lock()
	c.m, c.loaded = nil, false
	c.Unlock()
	refused.Store(true)
	failCtx, _ := quotaReading(context.Background())
	q := keepReading(failCtx, grokUsageAt(failCtx, GrokHome()), "grok@example.com")
	if q.Error != "" || q.AsOf == nil || len(q.Windows) != 1 || q.Windows[0].Used != 80 {
		t.Fatalf("after a failed read %+v, want the kept 80%%", q)
	}
}

// A further Grok account signs in in a home of magpie's: the CLI's own
// stays as it is, both are in use, and either can go first.
func TestGrokAccounts(t *testing.T) {
	home := signIn(t)
	own := filepath.Join(home, ".grok")
	grokSignedIn(t, own, "me@x.ai")

	extra, err := newGrokHome()
	if err != nil {
		t.Fatal(err)
	}
	grokSignedIn(t, extra, "two@x.ai")
	if u, err := addGrokLogin(extra); err != nil || u != "two@x.ai" {
		t.Fatalf("add: %q %v", u, err)
	}
	ls := Logins("grok")
	if len(ls) != 2 || ls[0].User != "me@x.ai" || !ls[0].Active || ls[1].User != "two@x.ai" || !ls[1].On {
		t.Fatalf("logins: %+v", ls)
	}
	p := Provider{ID: "grok", Account: &Account{Agent: "grok", User: "me@x.ai"}}
	if also := p.AlsoOn(); len(also) != 1 || also[0].Account.User != "two@x.ai" || also[0].Account.Home != extra {
		t.Fatalf("also on: %+v", also)
	}

	if err := SwitchLogin("grok", "two@x.ai"); err != nil {
		t.Fatal(err)
	}
	ls = Logins("grok")
	if ls[0].User != "two@x.ai" || !ls[0].Active || !ls[1].On {
		t.Fatalf("after switch: %+v", ls)
	}
	if b, _ := os.ReadFile(filepath.Join(own, "auth.json")); len(b) == 0 {
		t.Fatal("the CLI's own sign-in was touched")
	}
	if err := SetLoginOn("grok", "me@x.ai", false); err != nil {
		t.Fatal(err)
	}
	if also := p.AlsoOn(); len(also) != 0 {
		t.Fatalf("turned off, still on: %+v", also)
	}

	// signed in again, an account keeps one home
	again, _ := newGrokHome()
	grokSignedIn(t, again, "two@x.ai")
	addGrokLogin(again)
	if _, err := os.Stat(extra); !os.IsNotExist(err) {
		t.Fatal("the old home was kept")
	}
	SwitchLogin("grok", "me@x.ai")
	if err := ForgetLogin("grok", "two@x.ai"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(again); !os.IsNotExist(err) || len(Logins("grok")) != 1 {
		t.Fatalf("forgotten: %v %+v", err, Logins("grok"))
	}
}

// A Grok account refused while its allowance is being read is read again
// on the next ask: the reading out at the refusal goes to its caller but
// isn't kept, so it isn't served for a minute as the account's share.
func TestGrokUsageStaleMidRead(t *testing.T) {
	home := signIn(t)
	t.Setenv("GROK_HOME", filepath.Join(home, ".grok"))
	grokSignedIn(t, GrokHome(), "me@x.ai")
	grokHomeUsage.Lock()
	oldCache := grokHomeUsage.m
	grokHomeUsage.m = nil
	grokHomeUsage.Unlock()
	t.Cleanup(func() {
		grokHomeUsage.Lock()
		grokHomeUsage.m = oldCache
		grokHomeUsage.Unlock()
	})

	release := make(chan struct{})
	var once sync.Once
	var hits atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		used := 40
		if hits.Add(1) == 1 { // out as the request is refused
			<-release
			used = 90
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"config":{"creditUsagePercent":%d,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY"}}}`, used)
	}))
	var wg sync.WaitGroup
	// the reading this test starts is waited for before the server closes
	t.Cleanup(func() {
		once.Do(func() { close(release) })
		wg.Wait()
		fake.Close()
	})
	old := GrokBase
	GrokBase = fake.URL
	t.Cleanup(func() { GrokBase = old })

	used := func(u map[string]SubscriptionQuota) float64 {
		q := u["me@x.ai"]
		if q.Error != "" || len(q.Windows) != 1 {
			t.Fatalf("usage %+v", q)
		}
		return q.Windows[0].Used
	}
	var first map[string]SubscriptionQuota
	wg.Add(1)
	go func() {
		defer wg.Done()
		first = LoginUsage(context.Background(), "grok")
	}()
	for hits.Load() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	StaleAllowance("grok", "me@x.ai")
	once.Do(func() { close(release) })
	wg.Wait()
	if u := used(first); u != 90 {
		t.Fatalf("the reading out at the refusal gave its caller %v%%", u)
	}
	if u := used(LoginUsage(context.Background(), "grok")); u != 40 || hits.Load() != 2 {
		t.Fatalf("next reading %v%%, %d requests: the one from before the refusal was kept", u, hits.Load())
	}
}
