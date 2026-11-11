package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// limitedUpstream is a provider whose every call uses 300 input and 50
// output tokens, counting the calls it is sent.
func limitedUpstream(t *testing.T, delay time.Duration) *atomic.Int64 {
	t.Helper()
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":300,"completion_tokens":50}}`)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "upstream-secret", Chat: up.URL + "/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	return &calls
}

func setLimit(t *testing.T, id string, l *access.Limit) {
	t.Helper()
	if _, err := access.Update("limit-key", access.Change{Key: id, Limit: l}); err != nil {
		t.Fatal(err)
	}
}

// pinLimitClock holds the gateway's limit checks at a fixed past noon, so
// a test can't see its calls split across a midnight, a Monday or a 1st.
// The calls are still recorded at the real time, later than that, and a
// window counts every call from its start, so they all count in its window.
func pinLimitClock(t *testing.T) time.Time {
	t.Helper()
	at := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	limitClock = func() time.Time { return at }
	t.Cleanup(func() { limitClock = time.Now })
	return at
}

// A key past its limit is refused before any provider is asked, with a
// 429 that names the key, the limit and when it resets; another key and a
// keyless request from this computer go on; what was used survives a
// restart; raising the limit lets it in again (#585).
func TestKeyLimitRefusesBeforeUpstream(t *testing.T) {
	fresh(t)
	budget.Forget()
	now := pinLimitClock(t)
	calls := limitedUpstream(t, 0)
	keys, secrets := newCaller(t, "Capped", "Other")
	setLimit(t, keys[0].ID, &access.Limit{Period: "day", Tokens: 700})
	h := New().Handler()
	call := func(secret, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		if secret != "" {
			r.Header.Set("Authorization", "Bearer "+secret)
		} else {
			r.RemoteAddr = "127.0.0.1:5000"
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for i := range 2 {
		if w := call(secrets[0], "/v1/chat/completions", chatReq); w.Code != 200 {
			t.Fatal("within the limit", i, w.Code, w.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
	w := call(secrets[0], "/v1/chat/completions", chatReq)
	if w.Code != http.StatusTooManyRequests {
		t.Fatal("spent key let in", w.Code, w.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatal("a spent key's request reached the provider")
	}
	var e struct {
		Error struct{ Message, Type string }
	}
	json.Unmarshal(w.Body.Bytes(), &e)
	if e.Error.Type != "rate_limit_error" || !strings.Contains(e.Error.Message, `"Capped"`) || !strings.Contains(e.Error.Message, "700 of its 700") || !strings.Contains(e.Error.Message, "resets at") {
		t.Fatal("refusal says", w.Body.String())
	}
	// it resets at the next midnight after the check
	_, reset := access.Window("day", now)
	if ra := w.Header().Get("Retry-After"); ra != strconv.Itoa(int(reset.Sub(now).Seconds())) || w.Header().Get("X-Magpie-Limit-Reset") != reset.Format(time.RFC3339) || w.Header().Get("X-Should-Retry") != "false" {
		t.Fatal("headers", w.Header())
	}
	rec := lastUsage(t)
	if !rec.Rejected || rec.CallerKeyID != keys[0].ID || rec.ErrType != "gateway_key_limit" || rec.Status != 429 {
		t.Fatalf("refusal recorded as %+v", rec)
	}
	// said in Anthropic's shape on its API
	w = call(secrets[0], "/v1/messages", `{"model":"plan/m1","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	var a struct {
		Type  string
		Error struct{ Type string }
	}
	json.Unmarshal(w.Body.Bytes(), &a)
	if w.Code != 429 || a.Type != "error" || a.Error.Type != "rate_limit_error" || calls.Load() != 2 {
		t.Fatal("anthropic refusal", w.Code, w.Body.String())
	}
	if w := call(secrets[1], "/v1/chat/completions", chatReq); w.Code != 200 {
		t.Fatal("another key held to the spent one's limit", w.Code)
	}
	if w := call("", "/v1/chat/completions", chatReq); w.Code != 200 {
		t.Fatal("a keyless loopback request was limited", w.Code)
	}
	// the gateway restarts: the window is read back from usage.jsonl
	budget.Forget()
	if w := call(secrets[0], "/v1/chat/completions", chatReq); w.Code != 429 {
		t.Fatal("a restart forgot what the key used", w.Code)
	}
	st := budget.Of(access.Key{ID: keys[0].ID, Limit: &access.Limit{Period: "day", Tokens: 700}}, now)
	if st == nil || st.Tokens != 700 || st.Calls != 2 || !st.Spent || st.TokensLeft != 0 {
		t.Fatalf("status %+v", st)
	}
	setLimit(t, keys[0].ID, &access.Limit{Period: "day", Tokens: 1050})
	if w := call(secrets[0], "/v1/chat/completions", chatReq); w.Code != 200 {
		t.Fatal("a raised limit still refused", w.Code)
	}
	setLimit(t, keys[0].ID, nil)
	if w := call(secrets[0], "/v1/chat/completions", chatReq); w.Code != 200 {
		t.Fatal("no limit still refused", w.Code)
	}
	// the key asks after its own limit
	setLimit(t, keys[0].ID, &access.Limit{Period: "week", Tokens: 5000})
	r := httptest.NewRequest("GET", "/v1/magpie/limit", nil)
	r.Header.Set("Authorization", "Bearer "+secrets[0])
	lw := httptest.NewRecorder()
	h.ServeHTTP(lw, r)
	var got struct {
		Limited bool
		Limit   budget.Status
	}
	json.Unmarshal(lw.Body.Bytes(), &got)
	monday, _ := access.Window("week", now)
	if !got.Limited || got.Limit.Tokens != 1400 || got.Limit.TokensLeft != 3600 || got.Limit.Period != "week" || !got.Limit.Start.Equal(monday) {
		t.Fatal("limit endpoint", lw.Body.String())
	}
}

// N requests at once can't all pass a check made before any is counted:
// those in flight hold a reservation, so the key goes over by about one
// call, not by N.
func TestKeyLimitHoldsParallelRequests(t *testing.T) {
	fresh(t)
	budget.Forget()
	now := pinLimitClock(t)
	calls := limitedUpstream(t, 80*time.Millisecond)
	keys, secrets := newCaller(t, "Busy")
	setLimit(t, keys[0].ID, &access.Limit{Period: "month", Tokens: 2000})
	h := New().Handler()
	// a body of about the 300 tokens the call uses
	body := `{"model":"plan/m1","messages":[{"role":"user","content":"` + strings.Repeat("x", 1130) + `"}]}`
	call := func() int {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if call() != 200 {
		t.Fatal("first call")
	}
	var wg sync.WaitGroup
	var ok, refused atomic.Int64
	for range 20 {
		wg.Go(func() {
			switch call() {
			case 200:
				ok.Add(1)
			case 429:
				refused.Add(1)
			}
		})
	}
	wg.Wait()
	st := budget.Of(access.Key{ID: keys[0].ID, Limit: &access.Limit{Period: "month", Tokens: 2000}}, now)
	if st.Tokens > 2000+350 || refused.Load() == 0 || ok.Load()+refused.Load() != 20 {
		t.Fatalf("20 at once: %d let in, %d refused, %d tokens used of 2000, %d upstream calls", ok.Load(), refused.Load(), st.Tokens, calls.Load())
	}
	if st.InFlight != 0 || st.Reserved != 0 {
		t.Fatal("reservations left behind", st)
	}
	if call() != 429 {
		t.Fatal("spent key let in")
	}
}

// A window ends at the calendar's: what was used today counts nothing
// tomorrow, and a call before the window started never counts.
func TestKeyLimitWindowResets(t *testing.T) {
	fresh(t)
	budget.Forget()
	now := pinLimitClock(t)
	limitedUpstream(t, 0)
	keys, secrets := newCaller(t, "Daily")
	lim := &access.Limit{Period: "day", Tokens: 350}
	setLimit(t, keys[0].ID, lim)
	// yesterday's call
	budget.Append(usage.Record{Time: now.AddDate(0, 0, -1), Provider: "plan", Model: "m1", Input: 5000, Status: 200, CallerKeyID: keys[0].ID})
	h := New().Handler()
	call := func() int {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq))
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if call() != 200 {
		t.Fatal("yesterday's use counted today")
	}
	if call() != 429 {
		t.Fatal("spent key let in")
	}
	who := access.Identity{KeyID: keys[0].ID, KeyName: "Daily", Limit: lim}
	if _, refused := budget.Reserve(who, 10, "", now); refused == nil {
		t.Fatal("spent today")
	}
	// the calls were recorded at the real time, so the next window that has
	// none of them is the one after the real now
	_, reset := access.Window("day", time.Now())
	release, refused := budget.Reserve(who, 10, "", reset.Add(time.Minute))
	if refused != nil {
		t.Fatal("tomorrow's window still spent", refused)
	}
	release()
}

// An unfinished call belongs to the window it started in, including its
// reservation. A late check or release of it cannot spend the new window.
func TestKeyLimitReservationsStayInTheirWindow(t *testing.T) {
	for _, period := range access.Periods {
		if period == "total" {
			continue // one window that never ends: no boundary to cross (internal/budget's total_test.go holds it)
		}
		for _, cap := range []struct {
			name   string
			tokens int64
			cost   float64
		}{{"tokens", 100, 0}, {"cost", 0, 1}} {
			t.Run(period+"/"+cap.name, func(t *testing.T) {
				fresh(t)
				budget.Forget()
				t.Cleanup(budget.Forget)
				input, zero := 10_000.0, 0.0 // 100 input tokens reserve $1 with a cost cap.
				if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
					"test/m": {Input: &input, Output: &zero, CacheRead: &zero, CacheWrite: &zero},
				}}); err != nil {
					t.Fatal(err)
				}
				lim := &access.Limit{Period: period, Tokens: cap.tokens, Cost: cap.cost}
				who := access.Identity{KeyID: "key", KeyName: "Key", Limit: lim}
				at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
				if period == "days" {
					lim.Days, lim.Since = 3, at.Add(-50*time.Hour)
				}
				_, after := lim.Window(at)
				before := after.Add(-time.Second)
				reserve := func(at time.Time) func() {
					t.Helper()
					release, refused := budget.Reserve(who, 400, "test/m", at)
					if refused != nil {
						t.Fatal(refused)
					}
					t.Cleanup(release)
					return release
				}
				checkHeld := func(at time.Time, n int) {
					t.Helper()
					st := budget.Of(access.Key{ID: who.KeyID, Limit: lim}, at)
					if st.InFlight != n || st.Reserved != int64(n)*100 || st.ReservedCost != float64(n)*cap.cost || st.Tokens != 0 || st.Cost != 0 {
						t.Errorf("at %s: want %d reservations and no settled usage, got %+v", at, n, st)
					}
				}
				checkRefused := func(at time.Time) {
					t.Helper()
					if release, refused := budget.Reserve(who, 4, "test/m", at); refused == nil {
						release()
						t.Fatalf("at %s: let another call in while the window's budget was reserved", at)
					}
				}

				old := reserve(before)
				checkRefused(before)
				checkHeld(after, 0) // status is right even before a new call arrives
				current := reserve(after)
				checkHeld(after, 1)
				// A request timed before the boundary may acquire the lock last.
				checkRefused(before)
				budget.Append(usage.Record{Time: before, Provider: "test", Model: "m", Input: 100, Status: 200, CallerKeyID: who.KeyID})
				old()
				checkHeld(after, 1)
				checkRefused(after)
				current()
				checkHeld(after, 0)
				reserve(after)()
			})
		}
	}
}

// A key held to every N days (#1509): refused once spent, with the cycle
// named; the cycle survives a restart and ends N×24h after it began;
// Reset lets it in again at once, both caps counted from 0, and the next
// cycle runs N days from the reset.
func TestKeyLimitDaysCycleAndReset(t *testing.T) {
	fresh(t)
	budget.Forget()
	t.Cleanup(budget.Forget)
	calls := limitedUpstream(t, 0)
	// 300 input tokens a call at $1000 a million: $0.30 a call
	in, zero := 1000.0, 0.0
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"plan/m1": {Input: &in, Output: &zero, CacheRead: &zero, CacheWrite: &zero},
	}}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Cycle")
	set := time.Now().Add(-time.Hour).Round(0)
	access.Now = func() time.Time { return set }
	t.Cleanup(func() { access.Now = time.Now })
	setLimit(t, keys[0].ID, &access.Limit{Period: "days", Days: 1, Tokens: 100000, Cost: 0.5})
	access.Now = time.Now
	h := New().Handler()
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq))
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	// a call from before the cycle began never counts
	budget.Append(usage.Record{Time: set.Add(-time.Minute), Provider: "plan", Model: "m1", Input: 900000, Status: 200, CallerKeyID: keys[0].ID})
	for i := range 2 {
		if w := call(); w.Code != 200 {
			t.Fatal("within the limit", i, w.Code, w.Body.String())
		}
	}
	w := call()
	if w.Code != 429 || calls.Load() != 2 {
		t.Fatal("spent key let in", w.Code, calls.Load())
	}
	if !strings.Contains(w.Body.String(), "this 1-day cycle") {
		t.Fatal("refusal says", w.Body.String())
	}
	if got := w.Header().Get("X-Magpie-Limit-Reset"); got != set.Add(24*time.Hour).Format(time.RFC3339) {
		t.Fatal("resets at", got, "not a day after", set)
	}
	stored := func() access.Key {
		t.Helper()
		ks, err := access.List()
		if err != nil {
			t.Fatal(err)
		}
		return ks[0]
	}
	// a restart: still spent, the cycle where it was
	budget.Forget()
	if w := call(); w.Code != 429 {
		t.Fatal("a restart forgot the cycle", w.Code)
	}
	st := budget.Of(stored(), time.Now())
	if st.Days != 1 || !st.Start.Equal(set) || st.Tokens != 700 || st.Cost < 0.59 || !st.Spent {
		t.Fatalf("status %+v (set %s)", st, set)
	}
	// the next cycle, a day after it was set
	who := access.Identity{KeyID: keys[0].ID, KeyName: "Cycle", Limit: stored().Limit}
	if _, refused := budget.Reserve(who, 10, "", set.Add(24*time.Hour-time.Second)); refused == nil {
		t.Fatal("let in before the cycle ended")
	}
	release, refused := budget.Reserve(who, 10, "", set.Add(24*time.Hour))
	if refused != nil {
		t.Fatal("the next cycle still spent", refused)
	}
	release()
	// Reset: in again now, tokens and cost both from 0, a day from now
	if _, err := access.Update("reset-limit-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	st = budget.Of(stored(), time.Now())
	if st.Tokens != 0 || st.Cost != 0 || st.Calls != 0 || st.Spent || st.CostLeft != 0.5 || st.TokensLeft != 100000 {
		t.Fatalf("after Reset: %+v", st)
	}
	if !st.Reset.Equal(st.Start.Add(24*time.Hour)) || st.Start.Before(set.Add(time.Hour-time.Minute)) {
		t.Fatalf("the new cycle: %s – %s", st.Start, st.Reset)
	}
	if w := call(); w.Code != 200 {
		t.Fatal("refused after Reset", w.Code, w.Body.String())
	}
	if w := call(); w.Code != 200 {
		t.Fatal("refused after Reset", w.Code, w.Body.String())
	}
	if w := call(); w.Code != 429 || calls.Load() != 4 {
		t.Fatal("spent again after Reset", w.Code, calls.Load())
	}
}

// A calendar limit's Reset counts from the reset to the window's own end.
func TestKeyLimitCalendarReset(t *testing.T) {
	fresh(t)
	budget.Forget()
	t.Cleanup(budget.Forget)
	limitedUpstream(t, 0)
	keys, secrets := newCaller(t, "Monthly")
	setLimit(t, keys[0].ID, &access.Limit{Period: "month", Tokens: 350})
	h := New().Handler()
	call := func() int {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq))
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if call() != 200 || call() != 429 {
		t.Fatal("the month's limit")
	}
	if _, err := access.Update("reset-limit-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	if c := call(); c != 200 {
		t.Fatal("refused after Reset", c)
	}
	ks, _ := access.List()
	st := budget.Of(ks[0], time.Now())
	if _, end := access.Window("month", time.Now()); !st.Reset.Equal(end) || st.Tokens != 350 {
		t.Fatalf("after Reset: %+v", st)
	}
}

// A key on a 5-hour cycle (#2102) is refused once spent, named so, until
// 5 hours after the limit was set; /v1/magpie/limit tells the key its
// cycle; Reset lets it in again with a new 5 hours from then.
func TestKeyLimitHoursCycle(t *testing.T) {
	fresh(t)
	budget.Forget()
	t.Cleanup(budget.Forget)
	calls := limitedUpstream(t, 0)
	in, zero := 1000.0, 0.0
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"plan/m1": {Input: &in, Output: &zero, CacheRead: &zero, CacheWrite: &zero},
	}}); err != nil {
		t.Fatal(err)
	}
	keys, secrets := newCaller(t, "Family")
	set := time.Now().Add(-time.Hour).Round(0)
	access.Now = func() time.Time { return set }
	t.Cleanup(func() { access.Now = time.Now })
	setLimit(t, keys[0].ID, &access.Limit{Period: "hours", Hours: 5, Cost: 0.5})
	access.Now = time.Now
	h := New().Handler()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	// 6 hours before it was set: a cycle's length back, never counted
	budget.Append(usage.Record{Time: set.Add(-6 * time.Hour), Provider: "plan", Model: "m1", Input: 900000, Status: 200, CallerKeyID: keys[0].ID})
	for i := range 2 {
		if w := call("POST", "/v1/chat/completions", chatReq); w.Code != 200 {
			t.Fatal("within the limit", i, w.Code, w.Body.String())
		}
	}
	w := call("POST", "/v1/chat/completions", chatReq)
	if w.Code != 429 || calls.Load() != 2 || !strings.Contains(w.Body.String(), "this 5-hour cycle") {
		t.Fatal("spent key:", w.Code, calls.Load(), w.Body.String())
	}
	if got := w.Header().Get("X-Magpie-Limit-Reset"); got != set.Add(5*time.Hour).Format(time.RFC3339) {
		t.Fatal("resets at", got, "not 5 hours after", set)
	}
	lim := call("GET", "/v1/magpie/limit", "")
	if lim.Code != 200 || !strings.Contains(lim.Body.String(), `"period":"hours"`) || !strings.Contains(lim.Body.String(), `"hours":5`) {
		t.Fatal("/v1/magpie/limit:", lim.Code, lim.Body.String())
	}
	ks, _ := access.List()
	who := access.Identity{KeyID: keys[0].ID, KeyName: "Family", Limit: ks[0].Limit}
	if _, refused := budget.Reserve(who, 10, "", set.Add(5*time.Hour-time.Second)); refused == nil {
		t.Fatal("let in before the cycle ended")
	}
	release, refused := budget.Reserve(who, 10, "", set.Add(5*time.Hour))
	if refused != nil {
		t.Fatal("the next 5 hours still spent", refused)
	}
	release()
	if _, err := access.Update("reset-limit-key", access.Change{Key: keys[0].ID}); err != nil {
		t.Fatal(err)
	}
	ks, _ = access.List()
	st := budget.Of(ks[0], time.Now())
	if st.Spent || st.Cost != 0 || !st.Reset.Equal(st.Start.Add(5*time.Hour)) || st.Hours != 5 {
		t.Fatalf("after Reset: %+v", st)
	}
	if w := call("POST", "/v1/chat/completions", chatReq); w.Code != 200 {
		t.Fatal("refused after Reset", w.Code, w.Body.String())
	}
}

// A key with several limits (#2227) is let in only while every one has
// something left: any one spent refuses it, and the 429 names the spent
// one that resets last, as the key is refused until then. A request held
// in flight counts in each limit's window; /v1/magpie/limit reports each.
func TestKeyLimitSeveralLimits(t *testing.T) {
	fresh(t)
	budget.Forget()
	t.Cleanup(budget.Forget)
	now := pinLimitClock(t)
	calls := limitedUpstream(t, 0)
	keys, secrets := newCaller(t, "Shared")
	at := func(n int, l *access.Limit) {
		t.Helper()
		if _, err := access.Update("limit-key", access.Change{Key: keys[0].ID, Limit: l, Index: &n}); err != nil {
			t.Fatal(err)
		}
	}
	at(0, &access.Limit{Period: "day", Tokens: 700})
	at(1, &access.Limit{Period: "week", Tokens: 5000})
	h := New().Handler()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secrets[0])
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	ks, _ := access.List()
	who := access.Identity{KeyID: keys[0].ID, KeyName: "Shared", Limit: ks[0].Limit, More: ks[0].More}
	// one in flight is held in the day's window and the week's
	release, refused := budget.Reserve(who, 400, "", now)
	if refused != nil {
		t.Fatal(refused)
	}
	all := budget.All(ks[0], now)
	if len(all) != 2 || all[0].InFlight != 1 || all[1].InFlight != 1 || all[0].Period != "day" || all[1].Period != "week" {
		t.Fatalf("in flight: %+v", all)
	}
	release()
	if all := budget.All(ks[0], now); all[0].InFlight != 0 || all[1].InFlight != 0 {
		t.Fatalf("released: %+v", all)
	}
	for i := range 2 {
		if w := call("POST", "/v1/chat/completions", chatReq); w.Code != 200 {
			t.Fatal("within both limits", i, w.Code, w.Body.String())
		}
	}
	// the day is spent, the week isn't: refused, until the day resets
	_, endOfDay := access.Window("day", now)
	_, endOfWeek := access.Window("week", now)
	w := call("POST", "/v1/chat/completions", chatReq)
	if w.Code != 429 || calls.Load() != 2 || w.Header().Get("X-Magpie-Limit-Reset") != endOfDay.Format(time.RFC3339) {
		t.Fatal("day spent:", w.Code, calls.Load(), w.Header(), w.Body.String())
	}
	// both spent: the week is the one that holds it longest
	at(1, &access.Limit{Period: "week", Tokens: 700})
	w = call("POST", "/v1/chat/completions", chatReq)
	if w.Code != 429 || w.Header().Get("X-Magpie-Limit-Reset") != endOfWeek.Format(time.RFC3339) ||
		w.Header().Get("Retry-After") != strconv.Itoa(int(endOfWeek.Sub(now).Seconds())) {
		t.Fatal("both spent:", w.Code, w.Header(), w.Body.String())
	}
	var got struct {
		Limited bool
		Limit   budget.Status
		Limits  []budget.Status
	}
	lim := call("GET", "/v1/magpie/limit", "")
	json.Unmarshal(lim.Body.Bytes(), &got)
	if !got.Limited || len(got.Limits) != 2 || got.Limit.Period != "week" || !got.Limits[0].Spent || !got.Limits[1].Spent || got.Limits[0].Tokens != 700 {
		t.Fatal("/v1/magpie/limit:", lim.Body.String())
	}
	// the day's limit raised: the week still refuses
	at(0, &access.Limit{Period: "day", Tokens: 5000})
	if w := call("POST", "/v1/chat/completions", chatReq); w.Code != 429 || w.Header().Get("X-Magpie-Limit-Reset") != endOfWeek.Format(time.RFC3339) {
		t.Fatal("week spent:", w.Code, w.Body.String())
	}
	// the week's taken off: in again
	at(1, nil)
	if w := call("POST", "/v1/chat/completions", chatReq); w.Code != 200 {
		t.Fatal("the day alone has room:", w.Code, w.Body.String())
	}
}

// Limits whose windows start at the same moment (a day and a week on a
// Monday, a day and a month on the 1st) count a call once in the window
// they share. budget kept such a window once on the key's first request,
// and once a limit from its second on, and Append added each call to it
// that many times: a day and week key was refused on a Monday at half its
// day, while limit list and Usage showed the right sum (#2942).
func TestKeyLimitWindowsThatStartTogetherCountACallOnce(t *testing.T) {
	zone := time.FixedZone("UTC+8", 8*3600)
	for _, c := range []struct {
		name string
		at   time.Time
		more []string
	}{
		{"monday", time.Date(2026, 10, 19, 12, 0, 0, 0, zone), []string{"week"}},
		{"the 1st", time.Date(2026, 10, 1, 12, 0, 0, 0, zone), []string{"month"}},
		{"monday the 1st", time.Date(2026, 6, 1, 12, 0, 0, 0, zone), []string{"week", "month"}},
	} {
		// the reporter's caps are a cost: 100 input tokens are $1 here
		for _, cost := range []bool{false, true} {
			name := c.name + "/tokens"
			if cost {
				name = c.name + "/cost"
			}
			t.Run(name, func(t *testing.T) {
				fresh(t)
				budget.Forget()
				t.Cleanup(budget.Forget)
				input, zero := 10_000.0, 0.0
				if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
					"plan/m1": {Input: &input, Output: &zero, CacheRead: &zero, CacheWrite: &zero},
				}}); err != nil {
					t.Fatal(err)
				}
				// each call is 100 tokens, $1: the day lets in 400 ($4), the others plenty
				cap := func(period string, n int) access.Limit {
					if cost {
						return access.Limit{Period: period, Cost: float64(n) / 100}
					}
					return access.Limit{Period: period, Tokens: int64(n)}
				}
				first := cap("day", 400)
				who := access.Identity{KeyID: "key", KeyName: "Key", Limit: &first}
				day, _ := access.Window("day", c.at)
				for _, p := range c.more {
					if start, _ := access.Window(p, c.at); !start.Equal(day) {
						t.Fatalf("the %s starts %v, the day %v", p, start, day)
					}
					who.More = append(who.More, cap(p, 100_000))
				}
				used := func(r *budget.Refusal) float64 {
					if cost {
						return r.Cost * 100
					}
					return float64(r.Tokens)
				}
				call := func() *budget.Refusal {
					release, refused := budget.Reserve(who, 0, "plan/m1", c.at)
					if refused != nil {
						return refused
					}
					budget.Append(usage.Record{Time: c.at, Provider: "plan", Model: "m1", Input: 100, Status: 200, CallerKeyID: "key"})
					release()
					return nil
				}
				for i := range 4 {
					if refused := call(); refused != nil {
						t.Fatalf("call %d refused with %d of 400 used; it counts %.0f", i+1, 100*i, used(refused))
					}
				}
				// the day is spent now, and says the 400 it holds
				refused := call()
				if refused == nil {
					t.Fatal("a fifth call let in past the day's 400")
				}
				if refused.Period != "day" || used(refused) < 399.5 || used(refused) > 400.5 {
					t.Fatalf("refused on the %s at %.0f, want the day's 400", refused.Period, used(refused))
				}
				// the calls and tokens are counted once too, not only the
				// cost (xiedekai), as limit list reads them from the file
				if refused.Calls != 4 || refused.Tokens != 400 {
					t.Fatalf("refused with %d calls and %d tokens, want 4 and 400", refused.Calls, refused.Tokens)
				}
				for _, st := range budget.All(access.Key{ID: who.KeyID, Limit: who.Limit, More: who.More}, c.at) {
					if st.Calls != refused.Calls || st.Tokens != refused.Tokens {
						t.Fatalf("the %s's list says %d calls and %d tokens, the gateway %d and %d", st.Period, st.Calls, st.Tokens, refused.Calls, refused.Tokens)
					}
				}
			})
		}
	}
}
