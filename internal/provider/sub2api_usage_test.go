package provider

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

// sub2apiUsage is a sub2api key's /v1/usage as sub2api v0.1.149 answered
// it, the key given a $300 day and an $800 week in its panel: before its
// next request (not started) and after one.
func sub2apiUsage(t *testing.T, started bool) []byte {
	t.Helper()
	name := "testdata/sub2api_usage_key_limits_not_started.json"
	if started {
		name = "testdata/sub2api_usage_key_limits.json"
	}
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// weekUsed is the started reply with $used of the week spent, its day and
// week resetting a day and three days from now, as a test run on any day
// sees them.
func weekUsed(t *testing.T, used string, week time.Time) []byte {
	t.Helper()
	b := sub2apiUsage(t, true)
	b = bytes.Replace(b, []byte(`"used":0.0000958,"window":"7d"`), []byte(`"used":`+used+`,"window":"7d"`), 1)
	b = bytes.Replace(b, []byte("2026-10-07T00:00:00+08:00"), []byte(time.Now().Add(24*time.Hour).Format(time.RFC3339)), 1)
	return bytes.Replace(b, []byte("2026-10-13T00:00:00+08:00"), []byte(week.Format(time.RFC3339)), 1)
}

// A key given limits answers with its windows' spend in dollars; a
// window not started (or run out since) says used 0 and no reset_at.
func TestReadSub2APIKeyLimits(t *testing.T) {
	ws, ok := readSub2APIKeyLimits(sub2apiUsage(t, true))
	if !ok || len(ws) != 2 {
		t.Fatalf("windows = %+v, %v", ws, ok)
	}
	day, week := ws[0], ws[1]
	reset := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if day.Name != "1 day" || day.Span != 24*time.Hour || day.Limit != 300 || day.Unit != "USD" ||
		day.ResetsAt == nil || !day.ResetsAt.Equal(reset("2026-10-07T00:00:00+08:00")) ||
		day.Amount != 0.0000958 || day.Used <= 0 || day.Used > 0.0001 {
		t.Fatalf("day = %+v", day)
	}
	if week.Name != "7 days" || week.Span != 7*24*time.Hour || week.Limit != 800 ||
		week.ResetsAt == nil || !week.ResetsAt.Equal(reset("2026-10-13T00:00:00+08:00")) {
		t.Fatalf("week = %+v", week)
	}

	ws, ok = readSub2APIKeyLimits(sub2apiUsage(t, false))
	if !ok || len(ws) != 2 || ws[0].ResetsAt != nil || ws[1].ResetsAt != nil || ws[1].Used != 0 || ws[1].Span != 7*24*time.Hour {
		t.Fatalf("not started = %+v, %v", ws, ok)
	}

	// $720 of the week's $800 is 90%, over it no more than 100
	b := bytes.Replace(sub2apiUsage(t, true), []byte(`"used":0.0000958,"window":"7d"`), []byte(`"used":720,"window":"7d"`), 1)
	if ws, _ = readSub2APIKeyLimits(b); len(ws) != 2 || ws[1].Used != 90 || ws[1].Amount != 720 {
		t.Fatalf("$720 of $800 = %+v", ws)
	}
	b = bytes.Replace(sub2apiUsage(t, true), []byte(`"used":0.0000958,"window":"7d"`), []byte(`"used":812.5,"window":"7d"`), 1)
	if ws, _ = readSub2APIKeyLimits(b); len(ws) != 2 || ws[1].Used != 100 {
		t.Fatalf("$812.50 of $800 = %+v", ws)
	}

	// a window without a limit is none
	b = bytes.Replace(sub2apiUsage(t, true), []byte(`"limit":300`), []byte(`"limit":0`), 1)
	if ws, _ = readSub2APIKeyLimits(b); len(ws) != 1 || ws[0].Name != "7 days" {
		t.Fatalf("a day's limit of 0 = %+v", ws)
	}

	// a key on the wallet has none
	if ws, ok := readSub2APIKeyLimits([]byte(`{"mode":"unrestricted","isValid":true,"planName":"钱包余额","remaining":7.25,"unit":"USD","balance":7.25}`)); ok || ws != nil {
		t.Fatalf("wallet = %+v, %v", ws, ok)
	}
}

// keyLimitsHome gives a test a magpie of its own, without any key or
// window another test read.
func keyLimitsHome(t *testing.T) {
	t.Helper()
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("PATH", h)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}
	restart := func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
		ForgetBalances()
		keyAllowances.Lock()
		keyAllowances.m = nil
		keyAllowances.Unlock()
	}
	restart()
	t.Cleanup(func() {
		// the reads behind the test, and the sign-ins they look up, end
		// before the next test's isolate changes what they call
		deadline := time.Now().Add(10 * time.Second)
		for busy := true; busy && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			busy = false
			keyAllowances.Lock()
			for _, e := range keyAllowances.m {
				busy = busy || e.loading
			}
			keyAllowances.Unlock()
			devinStatus.Lock()
			busy = busy || devinStatus.refreshing
			devinStatus.Unlock()
		}
		restart()
	})
}

// sub2apiServer answers /v1/usage for sk-limited with body(), counting
// the asks; any other key is refused as sub2api refuses it.
func sub2apiServer(t *testing.T, body func() ([]byte, int)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage" {
			t.Errorf("asked %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-limited" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"type":"authentication_error","message":"Invalid API key"}}`))
			return
		}
		asked.Add(1)
		b, status := body()
		w.WriteHeader(status)
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

// sub2apiKey is a key at srv, under an id of the test's own: a renewal a
// reading behind another test tells, after it ended, is not this one's.
func sub2apiKey(t *testing.T, srv *httptest.Server) Provider {
	return Provider{ID: "sub2api-" + Slug(t.Name()), Name: "Sub2API", Chat: srv.URL + "/v1", Responses: srv.URL + "/v1",
		Key: "sk-limited", BalanceURL: srv.URL + "/v1/usage"}
}

// A key its owner gave limits has no "remaining" of its own: its card
// shows the windows it has spent of, not "nothing at remaining"; a field
// the user wrote is still read, beside them.
func TestSub2APIKeyLimitsOnItsCard(t *testing.T) {
	keyLimitsHome(t)
	var body atomic.Pointer[[]byte]
	b := sub2apiUsage(t, true)
	body.Store(&b)
	srv, _ := sub2apiServer(t, func() ([]byte, int) { return *body.Load(), 200 })
	p := sub2apiKey(t, srv)
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	card := func() SubscriptionQuota {
		ForgetBalances()
		for _, q := range KeyBalances(context.Background()) {
			if q.Provider == p.ID {
				return q
			}
		}
		t.Fatal("no card for the key")
		return SubscriptionQuota{}
	}
	q := card()
	if q.Error != "" || q.Balance != "" || len(q.Windows) != 2 || q.Windows[0].Name != "1 day" || q.Windows[1].Name != "7 days" || q.ReadAt == nil {
		t.Fatalf("card = %+v", q)
	}
	// checked from its editor, or magpie provider show: what it can
	// spend now, the least its windows have left
	if amount, ok, err := Balance(context.Background(), p); err != nil || !ok || amount != "$300.00" {
		t.Fatalf("Balance = %q %v %v", amount, ok, err)
	}
	week := weekUsed(t, "720", time.Now().Add(72*time.Hour))
	body.Store(&week)
	if amount, _, err := Balance(context.Background(), p); err != nil || amount != "$80.00" {
		t.Fatalf("Balance with $720 of the week's $800 spent = %q %v", amount, err)
	}
	body.Store(&b)

	p.BalancePath = "usage.total.actual_cost"
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if q = card(); q.Error != "" || q.Balance != "192.33" || len(q.Windows) != 2 {
		t.Fatalf("with a field: %+v", q)
	}
	// a field the user wrote that isn't there still says so
	p.BalancePath = "remaining"
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if q = card(); q.Error == "" || len(q.Windows) != 0 {
		t.Fatalf("a field not there: %+v", q)
	}
}

// waitAllowance waits for p's key's windows to be read.
func waitAllowance(t *testing.T, p Provider, until func(Allowance) bool) Allowance {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if a, ok := KeyAllowance(p); ok && until(a) {
			return a
		}
		if time.Now().After(deadline) {
			t.Fatal("the key's windows were never read")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Routing asks for a key's windows on every request: it must never wait
// for sub2api, nor ask it more than once at a time.
func TestKeyAllowanceNeverWaits(t *testing.T) {
	keyLimitsHome(t)
	week := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	release := make(chan struct{})
	srv, asked := sub2apiServer(t, func() ([]byte, int) {
		<-release
		return weekUsed(t, "720", week), 200
	})
	p := sub2apiKey(t, srv)
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			if a, ok := KeyAllowance(p); ok || a != nil {
				t.Errorf("known before it was read: %+v", a)
			}
			if d := time.Since(start); d > time.Second {
				t.Errorf("waited %v", d)
			}
		}()
	}
	wg.Wait()
	close(release)
	a := waitAllowance(t, p, func(Allowance) bool { return true })
	if used, _ := a.For("gpt-6-astra", time.Now()); used != 90 {
		t.Fatalf("used = %v of %+v", used, a)
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("asked %d times", n)
	}
	// read within the minute: not asked again
	KeyAllowance(p)
	time.Sleep(20 * time.Millisecond)
	if n := asked.Load(); n != 1 {
		t.Fatalf("asked %d times within the minute", n)
	}
}

// A read that fails says nothing of the key: what was known stands, and
// is asked for again a minute on rather than on every request.
func TestKeyAllowanceFailedReadKeepsLast(t *testing.T) {
	keyLimitsHome(t)
	week := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	var down atomic.Bool
	srv, asked := sub2apiServer(t, func() ([]byte, int) {
		if down.Load() {
			return []byte(`{"error":{"type":"api_error","message":"internal error"}}`), 502
		}
		return weekUsed(t, "720", week), 200
	})
	p := sub2apiKey(t, srv)
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	waitAllowance(t, p, func(Allowance) bool { return true })
	down.Store(true)
	StaleKeyAllowance(p)
	KeyAllowance(p) // asks again, and is told nothing
	deadline := time.Now().Add(5 * time.Second)
	for asked.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("not asked again once stale")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for range 20 {
		a, ok := KeyAllowance(p)
		if used, _ := a.For("gpt-6-astra", time.Now()); !ok || used != 90 {
			t.Fatalf("after a failed read: %+v, %v", a, ok)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := asked.Load(); n != 2 {
		t.Fatalf("asked %d times after failing", n)
	}
}

// A read under way as the keys are forgotten is dropped, and the key is
// read again at its next ask — not left waiting on the read dropped.
func TestKeyAllowanceForgottenMidRead(t *testing.T) {
	keyLimitsHome(t)
	release := make(chan struct{})
	var first atomic.Bool
	srv, asked := sub2apiServer(t, func() ([]byte, int) {
		if first.CompareAndSwap(false, true) {
			<-release
		}
		return sub2apiUsage(t, true), 200
	})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	p := sub2apiKey(t, srv)
	KeyAllowance(p)
	for asked.Load() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	ForgetBalances()
	once.Do(func() { close(release) })
	waitAllowance(t, p, func(a Allowance) bool { return len(a) == 2 })
	if n := asked.Load(); n != 2 {
		t.Fatalf("asked %d times", n)
	}
}

// A key that says it is out of its windows while a reading of them is
// out has them read again once that reading is back, without being asked:
// the reading was asked before, and the rest it would lift is planned on
// the one after (an ordered group's member read as it is planned).
func TestKeyAllowanceStaleMidRead(t *testing.T) {
	keyLimitsHome(t)
	week := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	release := make(chan struct{})
	var first atomic.Bool
	srv, asked := sub2apiServer(t, func() ([]byte, int) {
		if first.CompareAndSwap(false, true) {
			<-release
			return weekUsed(t, "800", week), 200
		}
		return weekUsed(t, "400", week), 200
	})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	p := sub2apiKey(t, srv)
	renewedHooks.Lock()
	hooks := renewedHooks.fs
	renewedHooks.Unlock()
	t.Cleanup(func() {
		renewedHooks.Lock()
		renewedHooks.fs = hooks
		renewedHooks.Unlock()
	})
	renewed := make(chan struct{})
	var told sync.Once
	OnRenewed(func(agent, user string) {
		if agent == "" && user == KeyAllowanceID(p) {
			told.Do(func() { close(renewed) })
		}
	})
	KeyAllowance(p)
	for asked.Load() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	StaleKeyAllowance(p) // out of its week, as the request found
	once.Do(func() { close(release) })
	waitAllowance(t, p, func(a Allowance) bool { u, _ := a.For("gpt-6-astra", time.Now()); return u > 0 && u < 100 })
	if n := asked.Load(); n != 2 {
		t.Fatalf("asked %d times", n)
	}
	// the reading after tells the week renewed only after it is kept, so
	// waitAllowance can return first: waited for here, or a later test's
	// hook is told it
	select {
	case <-renewed:
	case <-time.After(5 * time.Second):
		t.Fatal("the week renewed was never told")
	}
}

// Until the key is first read (magpie just started), its windows are the
// ones its card last showed, kept on disk; a window whose reset passed
// since is empty again.
func TestKeyAllowanceSeededFromItsCard(t *testing.T) {
	keyLimitsHome(t)
	week := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	var down atomic.Bool
	srv, _ := sub2apiServer(t, func() ([]byte, int) {
		if down.Load() {
			return nil, 503
		}
		return weekUsed(t, "720", week), 200
	})
	p := sub2apiKey(t, srv)
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	KeyBalances(context.Background())
	down.Store(true)
	// as after a restart: nothing in memory
	lastQuotas.Lock()
	lastQuotas.m, lastQuotas.loaded = nil, false
	lastQuotas.Unlock()
	keyAllowances.Lock()
	keyAllowances.m = nil
	keyAllowances.Unlock()
	a, ok := KeyAllowance(p)
	now := time.Now()
	if used, _ := a.For("gpt-6-astra", now); !ok || used != 90 {
		t.Fatalf("seeded = %+v, %v", a, ok)
	}
	// the week renews on the reset sub2api told, not a week from now
	if r := a.Renewal("gpt-6-astra", now); len(r) != 2 || !r[0].Equal(week) {
		t.Fatalf("renewal = %v", r)
	}
}

// Only a key whose Balance URL is sub2api's /v1/usage is asked: any other
// key, or an account, costs nothing.
func TestKeyAllowanceOnlyForSub2APIKeys(t *testing.T) {
	keyLimitsHome(t)
	srv, asked := sub2apiServer(t, func() ([]byte, int) { return sub2apiUsage(t, true), 200 })
	for _, p := range []Provider{
		{ID: "relay", Chat: srv.URL + "/v1", Key: "sk-limited", BalanceURL: srv.URL + "/api/usage/token"},
		{ID: "plain", Chat: srv.URL + "/v1", Key: "sk-limited"},
		{ID: "sub2api", Chat: srv.URL + "/v1", BalanceURL: srv.URL + "/v1/usage"},
		{ID: "codex", Chat: srv.URL + "/v1", Key: "sk-limited", BalanceURL: srv.URL + "/v1/usage", Account: &Account{Agent: "codex", User: "a@example.com"}},
	} {
		if a, ok := KeyAllowance(p); ok || a != nil {
			t.Errorf("%s: %+v", p.ID, a)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if n := asked.Load(); n != 0 {
		t.Fatalf("asked %d times", n)
	}
}

// Each key of a provider has windows of its own; a key or Balance URL
// changed (ForgetBalances) has them all read again.
func TestKeyAllowancePerKeyAndForgotten(t *testing.T) {
	keyLimitsHome(t)
	srv, asked := sub2apiServer(t, func() ([]byte, int) { return sub2apiUsage(t, true), 200 })
	p := sub2apiKey(t, srv)
	now := time.Now()
	week := now.Add(72 * time.Hour)
	noteKeyAllowance(p, []QuotaWindow{{Name: "7 days", Span: 7 * 24 * time.Hour, Used: 95, ResetsAt: &week}}, now)
	other := p
	other.Key = "sk-other"
	if a, ok := KeyAllowance(other); ok || a != nil {
		t.Fatalf("another key's: %+v", a)
	}
	if _, ok := KeyAllowance(p); !ok {
		t.Fatal("the key's own not known")
	}
	// a read begun before ForgetBalances doesn't land after it
	keyAllowances.Lock()
	gen := keyAllowances.gen
	keyAllowances.Unlock()
	ForgetBalances()
	readKeyAllowance(p, keyAllowanceID(p), gen)
	keyAllowances.Lock()
	e := keyAllowances.m[keyAllowanceID(p)]
	var kept Allowance
	if e != nil {
		kept = e.a
	}
	keyAllowances.Unlock()
	if len(kept) != 1 || kept[0].Used != 95 {
		t.Fatalf("after ForgetBalances, and a read begun before it: %+v", kept)
	}
	// what was read stands, not the card's older reading, until the key
	// is read again at its next ask
	if a, ok := KeyAllowance(p); !ok || len(a) != 1 || a[0].Used != 95 {
		t.Fatalf("after ForgetBalances: %+v, %v", a, ok)
	}
	waitAllowance(t, p, func(a Allowance) bool { return len(a) == 2 })
	if n := asked.Load(); n != 2 { // the read dropped, and the one after
		t.Fatalf("asked %d times after ForgetBalances", n)
	}
	if strings.Contains(keyAllowanceID(p), "sk-limited") {
		t.Fatal("the key itself names its windows")
	}
}

// A key out of a window is told renewed (OnRenewed, as agent "" and its
// KeyAllowanceID) once a reading finds that window full no more — its
// limit raised in the panel, or its usage reset — whether routing read it
// or its card did; not on its first reading, while it stays full, as it
// fills, or when a read fails.
func TestKeyAllowanceRenewed(t *testing.T) {
	keyLimitsHome(t)
	week := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	var body atomic.Pointer[[]byte]
	srv, _ := sub2apiServer(t, func() ([]byte, int) {
		if b := *body.Load(); b != nil {
			return b, 200
		}
		return []byte(`{"error":{"type":"api_error","message":"internal error"}}`), 502
	})
	p := sub2apiKey(t, srv)
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	renewedHooks.Lock()
	hooks := renewedHooks.fs
	renewedHooks.Unlock()
	t.Cleanup(func() {
		renewedHooks.Lock()
		renewedHooks.fs = hooks
		renewedHooks.Unlock()
	})
	id := KeyAllowanceID(p)
	if id != keyAllowanceID(p) {
		t.Fatalf("KeyAllowanceID = %q", id)
	}
	var mu sync.Mutex
	var told []string
	// only its own key's: another test's reading may still tell its own
	OnRenewed(func(agent, user string) {
		if user != id {
			return
		}
		mu.Lock()
		told = append(told, agent+"/"+user)
		mu.Unlock()
	})
	reply := func(b []byte) { body.Store(&b) }
	// read is a reading as routing's, done here rather than behind it
	read := func(b []byte) {
		reply(b)
		keyAllowances.Lock()
		gen := keyAllowances.gen
		keyAllowances.Unlock()
		readKeyAllowance(p, id, gen)
	}
	tellsSo := func(what string, n int) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if len(told) != n {
			t.Fatalf("%s: told %v", what, told)
		}
		for _, s := range told {
			if s != "/"+id {
				t.Fatalf("%s: told %v", what, told)
			}
		}
	}
	full := weekUsed(t, "800", week)
	reply(full)
	waitAllowance(t, p, func(a Allowance) bool { u, _ := a.For("gpt-6-astra", time.Now()); return u == 100 })
	tellsSo("its first reading, out of its week", 0)
	read(full)
	tellsSo("its week still used up", 0)
	read(bytes.Replace(full, []byte(`"limit":800`), []byte(`"limit":1000`), 1))
	tellsSo("its week's limit raised to $1,000", 1)
	read(full)
	tellsSo("its week used up again", 1)
	read(nil)
	tellsSo("a read that failed", 1)
	// its usage reset in the panel, as its card reads it
	reply(sub2apiUsage(t, false))
	ForgetBalances()
	KeyBalances(context.Background())
	tellsSo("its usage reset, read on its card", 2)
}
