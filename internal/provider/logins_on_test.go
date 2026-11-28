package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubscriptionsOnAtOnce(t *testing.T) {
	home := signIn(t)
	// a saved account whose access token has run out: it's refreshed in
	// logins.json when it's used, never put back into the agent's store
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": "old@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(-time.Hour).Unix())}),
			"refresh_token": "r-old", "account_id": "acct-old",
		},
	})
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)

	var refreshed string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		refreshed = body["refresh_token"]
		json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh-old", "refresh_token": "r-old-2"})
	}))
	defer fake.Close()
	old := codexTokenURL
	codexTokenURL = fake.URL
	t.Cleanup(func() { codexTokenURL = old })

	p, err := Find("codex")
	if err != nil || p.Account == nil || p.Account.User != "work@example.com" {
		t.Fatalf("codex: %+v %v", p, err)
	}
	if n := len(p.AlsoOn()); n != 0 {
		t.Fatalf("%d accounts on before any was turned on", n)
	}
	if err := SetLoginOn("codex", "work@example.com", false); err == nil {
		t.Fatal("turned off the account codex is signed in to")
	}
	if err := SetLoginOn("codex", "OLD@example.com", true); err != nil {
		t.Fatal(err)
	}
	// me@ and old@ are both saved; only old@ is on
	also := p.AlsoOn()
	if len(also) != 1 {
		t.Fatalf("%d accounts also on", len(also))
	}
	q := also[0]
	if q.ID != "codex" || q.Account.User != "old@example.com" || p.Account.User != "work@example.com" {
		t.Fatalf("copy %s %s, primary %s", q.ID, q.Account.User, p.Account.User)
	}
	tok, ok, err := q.Account.Token(context.Background())
	if err != nil || !ok || tok != "fresh-old" || refreshed != "r-old" {
		t.Fatalf("token %q %v %v, refreshed with %q", tok, ok, err, refreshed)
	}
	req, _ := http.NewRequest("POST", "http://x", nil)
	if err := q.Account.sign(context.Background(), req, nil); err != nil || req.Header.Get("Authorization") != "Bearer fresh-old" || req.Header.Get("chatgpt-account-id") != "acct-old" {
		t.Fatalf("signed %v %v", req.Header, err)
	}
	for _, l := range readLogins() {
		if l.User == "old@example.com" && !strings.Contains(string(l.Auth), "r-old-2") {
			t.Fatalf("the refresh wasn't kept: %s", l.Auth)
		}
	}
	var live codexAuth
	readJSON(filepath.Join(home, ".codex", "auth.json"), &live)
	if live.Tokens.RefreshToken != "r-work" {
		t.Fatalf("the agent's own sign-in changed: %+v", live.Tokens)
	}
	if _, ok, _ := p.Account.Token(context.Background()); ok {
		t.Fatal("the agent's own account has a token of magpie's")
	}

	// switching keeps the one it replaces in use
	if err := SwitchLogin("codex", "old@example.com"); err != nil {
		t.Fatal(err)
	}
	for _, l := range Logins("codex") {
		if l.User == "work@example.com" && !l.On || l.User == "me@example.com" && l.On {
			t.Fatalf("after switch: %+v", l)
		}
	}
	if err := SetLoginOn("codex", "work@example.com", false); err != nil {
		t.Fatal(err)
	}
	if err := SetLoginOn("codex", "nobody@example.com", true); err == nil {
		t.Fatal("turned on an unknown account")
	}
}

func TestLoginUsageEachAccount(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	loginUsageCache.Lock()
	loginUsageCache.m = nil
	loginUsageCache.Unlock()
	used := map[string]float64{"acct-1": 12, "acct-work@example.com": 97}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "pro", "rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": used[r.Header.Get("chatgpt-account-id")], "limit_window_seconds": 18000}}})
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })

	u := LoginUsage(context.Background(), "codex")
	if len(u) != 2 || u["me@example.com"].Windows[0].Used != 12 || u["work@example.com"].Windows[0].Used != 97 || u["work@example.com"].Windows[0].Name != "5 hours" {
		t.Fatalf("usage %+v", u)
	}
	if len(LoginUsage(context.Background(), "opencode")) != 0 {
		t.Fatal("usage for an agent without accounts")
	}
}

// An uncached account is fetched in a goroutine while the next account
// comes from the cache. Both results must be added to the same map safely.
func TestLoginUsageMixedCache(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	ls := Logins("codex")
	if len(ls) != 2 {
		t.Fatalf("logins: %+v", ls)
	}
	// Cache the last account in the order LoginUsage reads them: its map
	// write must follow the go statement for the uncached account.
	fetched, cached := ls[0].User, ls[len(ls)-1].User
	accountIDs := map[string]string{"me@example.com": "acct-1", "work@example.com": "acct-work@example.com"}

	loginUsageCache.Lock()
	oldCache := loginUsageCache.m
	loginUsageCache.m = map[string]loginUsageEntry{
		"codex/" + strings.ToLower(cached): {at: time.Now(), q: SubscriptionQuota{Provider: "codex",
			Windows: []QuotaWindow{{Name: "5 hours", Used: 97}}}},
	}
	loginUsageCache.Unlock()
	t.Cleanup(func() {
		loginUsageCache.Lock()
		loginUsageCache.m = oldCache
		loginUsageCache.Unlock()
	})

	var hits atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/backend-api/wham/usage" || r.Header.Get("chatgpt-account-id") != accountIDs[fetched] {
			t.Errorf("unexpected usage request: %s, account %q", r.URL.Path, r.Header.Get("chatgpt-account-id"))
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "pro", "rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": 12, "limit_window_seconds": 18000}}})
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })

	for i := range 2 { // the fetched account is cached on the second read
		u := LoginUsage(context.Background(), "codex")
		if len(u) != 2 {
			t.Fatalf("read %d: usage for %d accounts, want 2", i, len(u))
		}
		for user, want := range map[string]float64{fetched: 12, cached: 97} {
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

// With several accounts, the usage page has a card for each, the one the
// agent is signed in to first.
func TestSubscriptionUsageEachAccount(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	used := map[string]float64{"acct-1": 12, "acct-work@example.com": 97}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "pro", "rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": used[r.Header.Get("chatgpt-account-id")], "limit_window_seconds": 18000}}})
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })

	var codex []SubscriptionQuota
	for _, q := range fetchSubscriptionUsage(context.Background()) {
		if q.Provider == "codex" {
			codex = append(codex, q)
		}
	}
	if len(codex) != 2 || codex[0].User != "work@example.com" || codex[0].Windows[0].Used != 97 ||
		codex[1].User != "me@example.com" || codex[1].Windows[0].Used != 12 || codex[1].Name != "ChatGPT" {
		t.Fatalf("cards %+v", codex)
	}
}

// codexCards are the Usage page's Codex cards, by account.
func codexCards() map[string]SubscriptionQuota {
	out := map[string]SubscriptionQuota{}
	for _, q := range fetchSubscriptionUsage(context.Background()) {
		if q.Provider == "codex" {
			out[q.User] = q
		}
	}
	return out
}

// An account's row and its card on the Usage page show one reading:
// whichever is read first, the other is told it within the minute, and
// the vendor is asked once — not twice, a moment apart, two numbers.
func TestLoginUsageSharedWithUsagePage(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	var mu sync.Mutex
	asked := map[string]int{}
	var n atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			w.WriteHeader(404)
			return
		}
		mu.Lock()
		asked[r.Header.Get("chatgpt-account-id")]++
		mu.Unlock()
		// each reading its own, so two are told apart
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "pro", "rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": n.Add(1), "limit_window_seconds": 18000}}})
	}))
	defer fake.Close()
	old, oldCopilot := CodexBase, CopilotUserURL
	CodexBase = fake.URL + "/backend-api/codex"
	CopilotUserURL = fake.URL + "/copilot" // octocat's card asks no one
	t.Cleanup(func() { CodexBase, CopilotUserURL = old, oldCopilot })

	for _, pageFirst := range []bool{false, true} {
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
		mu.Lock()
		clear(asked)
		mu.Unlock()
		var rows, cards map[string]SubscriptionQuota
		if pageFirst {
			cards = codexCards()
			rows = LoginUsage(context.Background(), "codex")
		} else {
			rows = LoginUsage(context.Background(), "codex")
			cards = codexCards()
		}
		if len(rows) != 2 || len(cards) != 2 {
			t.Fatalf("page first %v: rows %+v, cards %+v", pageFirst, rows, cards)
		}
		for user, c := range cards {
			r := rows[user]
			if c.Error != "" || r.Error != "" || len(c.Windows) != 1 || len(r.Windows) != 1 || c.Windows[0].Used != r.Windows[0].Used ||
				c.ReadAt == nil || r.ReadAt == nil || !c.ReadAt.Equal(*r.ReadAt) {
				t.Fatalf("page first %v: %s's card %+v, its row %+v", pageFirst, user, c, r)
			}
		}
		mu.Lock()
		if len(asked) != 2 || asked["acct-1"] != 1 || asked["acct-work@example.com"] != 1 {
			t.Errorf("page first %v: asked %v, want each account once", pageFirst, asked)
		}
		mu.Unlock()
	}
}

// An account being read is waited for, not asked for again: LoginUsage and
// the Usage page asking meanwhile have the one request's answer, which the
// caller that started it giving up doesn't cut short.
func TestLoginUsageReadOnceAtATime(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	var hits atomic.Int32
	hold := make(chan struct{})
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			w.WriteHeader(404)
			return
		}
		n := hits.Add(1)
		select {
		case <-hold:
		case <-r.Context().Done():
		}
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "pro", "rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": n, "limit_window_seconds": 18000}}})
	}))
	t.Cleanup(fake.Close)
	var released atomic.Bool
	release := sync.OnceFunc(func() { released.Store(true); close(hold) })
	t.Cleanup(release)
	old, oldCopilot := CodexBase, CopilotUserURL
	CodexBase = fake.URL + "/backend-api/codex"
	CopilotUserURL = fake.URL + "/copilot"
	t.Cleanup(func() { CodexBase, CopilotUserURL = old, oldCopilot })

	// the first to ask starts both accounts' readings, then gives up
	ctx, cancel := context.WithCancel(context.Background())
	gaveUp := make(chan map[string]SubscriptionQuota, 1)
	go func() { gaveUp <- LoginUsage(ctx, "codex") }()
	for deadline := time.Now().Add(5 * time.Second); hits.Load() < 2; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d readings started, want 2", hits.Load())
		}
	}
	// the rest ask while those are out
	var wg sync.WaitGroup
	var early atomic.Int32
	rows := make([]map[string]SubscriptionQuota, 3)
	cards := make([]map[string]SubscriptionQuota, 3)
	for i := range 3 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			rows[i] = LoginUsage(context.Background(), "codex")
			if !released.Load() {
				early.Add(1)
			}
		}()
		go func() {
			defer wg.Done()
			cards[i] = codexCards()
			if !released.Load() {
				early.Add(1)
			}
		}()
	}
	cancel()
	select {
	case u := <-gaveUp:
		if len(u) != 2 || u["me@example.com"].Error == "" {
			t.Fatalf("given up on: %+v", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a caller that gave up still waited for the reading")
	}
	time.Sleep(100 * time.Millisecond) // the rest waiting for the readings
	release()
	wg.Wait()

	if got := hits.Load(); got != 2 {
		t.Fatalf("%d requests, want one an account", got)
	}
	if early.Load() != 0 {
		t.Fatalf("%d callers didn't wait for the reading", early.Load())
	}
	want := rows[0]
	if len(want) != 2 {
		t.Fatalf("usage %+v", want)
	}
	for user, q := range want {
		if q.Error != "" || len(q.Windows) != 1 || q.ReadAt == nil {
			t.Fatalf("%s: %+v (cut short with the caller that started it?)", user, q)
		}
		for i := range 3 {
			r, c := rows[i][user], cards[i][user]
			if len(r.Windows) != 1 || len(c.Windows) != 1 || r.Windows[0].Used != q.Windows[0].Used || c.Windows[0].Used != q.Windows[0].Used ||
				r.ReadAt == nil || c.ReadAt == nil || !r.ReadAt.Equal(*q.ReadAt) || !c.ReadAt.Equal(*q.ReadAt) {
				t.Fatalf("%s: row %+v, card %+v, want the one reading %+v", user, r, c, q)
			}
		}
	}
}
