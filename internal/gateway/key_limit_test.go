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
				_, after := access.Window(period, at)
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
