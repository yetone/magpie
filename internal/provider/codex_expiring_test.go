package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A reset is spent by itself only within resetLead of running out,
// and only when the account's windows have something to start again; an
// account is read again when there may be something to do, not before.
func TestResetSpentBeforeItRunsOut(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) *time.Time { u := now.Add(d); return &u }
	week := func(used float64) []QuotaWindow {
		return []QuotaWindow{{Span: 7 * 24 * time.Hour, Used: used, ResetsAt: at(72 * time.Hour)}}
	}
	for name, c := range map[string]struct {
		windows []QuotaWindow
		resets  *ResetCredits
		spend   bool
		next    time.Duration // when it is read again
	}{
		"runs out soon, used":            {week(40), &ResetCredits{Count: 1, Until: at(20 * time.Minute)}, true, resetExpiryClose},
		"runs out within the hour, used": {week(40), &ResetCredits{Count: 1, Until: at(time.Hour)}, false, resetExpiryClose},
		"runs out soon, nothing used":    {week(0), &ResetCredits{Count: 1, Until: at(20 * time.Minute)}, false, resetExpiryClose},
		"on-demand only used":            {[]QuotaWindow{{Span: 30 * 24 * time.Hour, Used: 50, Aside: true}}, &ResetCredits{Count: 1, Until: at(20 * time.Minute)}, false, resetExpiryClose},
		"runs out tomorrow":              {week(40), &ResetCredits{Count: 1, Until: at(24 * time.Hour)}, false, resetExpiryWatch},
		"runs out in five hours":         {week(40), &ResetCredits{Count: 1, Until: at(5 * time.Hour)}, false, resetExpiryWatch},
		"runs out in 80 minutes":         {week(40), &ResetCredits{Count: 1, Until: at(80 * time.Minute)}, false, 20 * time.Minute},
		"never runs out":                 {week(40), &ResetCredits{Count: 1}, false, resetExpiryFar},
		"none held":                      {week(40), nil, false, resetExpiryFar},
		"ran out already":                {week(40), &ResetCredits{Count: 1, Until: at(-time.Minute)}, false, resetExpiryClose},
	} {
		t.Run(name, func(t *testing.T) {
			e := expiringResets{next: map[string]time.Time{}}
			spent := 0
			out, err := e.check("Me@example.com", now, func() ([]QuotaWindow, *ResetCredits, error) { return c.windows, c.resets, nil },
				func() (ResetOutcome, error) { spent++; return ResetOutcome{Code: "reset", Windows: 2}, nil })
			if err != nil || (spent == 1) != c.spend || (out.Code == "reset") != c.spend {
				t.Fatalf("spent %d, %+v %v", spent, out, err)
			}
			if got := e.next["me@example.com"].Sub(now); got != c.next {
				t.Fatalf("read again in %v, want %v", got, c.next)
			}
			// not read again before then
			looked := false
			e.check("me@example.com", now.Add(c.next-time.Second), func() ([]QuotaWindow, *ResetCredits, error) { looked = true; return nil, nil, nil },
				func() (ResetOutcome, error) { t.Fatal("spent"); return ResetOutcome{}, nil })
			if looked {
				t.Fatal("read again too soon")
			}
		})
	}
}

// A read or a spend that fails is tried again later, not on every pass.
func TestResetExpiryRetriesLater(t *testing.T) {
	now := time.Now()
	soon := now.Add(20 * time.Minute)
	e := expiringResets{next: map[string]time.Time{}}
	_, err := e.check("me@example.com", now, func() ([]QuotaWindow, *ResetCredits, error) { return nil, nil, http.ErrHandlerTimeout },
		func() (ResetOutcome, error) { t.Fatal("spent"); return ResetOutcome{}, nil })
	if err == nil || e.next["me@example.com"].Sub(now) != resetExpiryWatch {
		t.Fatalf("%v, next %v", err, e.next["me@example.com"].Sub(now))
	}
	e.next = map[string]time.Time{}
	out, _ := e.check("me@example.com", now, func() ([]QuotaWindow, *ResetCredits, error) {
		return []QuotaWindow{{Span: 5 * time.Hour, Used: 10}}, &ResetCredits{Count: 1, Until: &soon}, nil
	}, func() (ResetOutcome, error) { return ResetOutcome{Code: "nothing_to_reset"}, nil })
	if out.Code != "nothing_to_reset" || e.next["me@example.com"].Sub(now) != resetExpiryClose {
		t.Fatalf("%+v, next %v", out, e.next["me@example.com"].Sub(now))
	}
}

// End to end against a fake ChatGPT: an account that lets its resets be
// spent, one of them running out within the half hour and its week used, spends
// that one; an account that doesn't let it is never read.
func TestSpendExpiringCodexResets(t *testing.T) {
	signIn(t)
	old, oldUntil := expiring.next, expiring.until
	expiring.next, expiring.until = map[string]time.Time{}, map[string]time.Time{}
	t.Cleanup(func() { expiring.next, expiring.until = old, oldUntil })
	soon := time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339)
	later := time.Now().Add(20 * 24 * time.Hour).UTC().Format(time.RFC3339)
	var read, consumed atomic.Int32
	var spentID string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			read.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"plan_type": "pro",
				"rate_limit": map[string]any{
					"primary_window":   map[string]any{"used_percent": 0, "limit_window_seconds": 18000},
					"secondary_window": map[string]any{"used_percent": 35, "limit_window_seconds": 604800, "reset_at": time.Now().Add(72 * time.Hour).Unix()}},
				"rate_limit_reset_credits": map[string]any{"available_count": 2}})
		case "/backend-api/wham/rate-limit-reset-credits":
			json.NewEncoder(w).Encode(map[string]any{"available_count": 2, "credits": []any{
				map[string]any{"id": "later", "status": "available", "expires_at": later},
				map[string]any{"id": "soon", "status": "available", "expires_at": soon},
			}})
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumed.Add(1)
			var b struct {
				Credit string `json:"credit_id"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			spentID = b.Credit
			json.NewEncoder(w).Encode(map[string]any{"code": "reset", "windows_reset": 2})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(fake.Close)
	oldBase := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = oldBase })

	SpendExpiringCodexResets(t.Context())
	if read.Load() != 0 || consumed.Load() != 0 {
		t.Fatalf("not turned on, yet read %d, spent %d", read.Load(), consumed.Load())
	}
	// kept to its week used up (#2078): the one about to run out isn't its to spend
	if err := SetCodexAutoResetCase("me@example.com", ResetCaseWeek, true); err != nil {
		t.Fatal(err)
	}
	SpendExpiringCodexResets(t.Context())
	if read.Load() != 0 || consumed.Load() != 0 {
		t.Fatalf("kept to its week, yet read %d, spent %d", read.Load(), consumed.Load())
	}
	if err := SetCodexAutoReset("me@example.com", true); err != nil {
		t.Fatal(err)
	}
	SpendExpiringCodexResets(t.Context())
	if consumed.Load() != 1 || spentID != "soon" {
		t.Fatalf("spent %d (%q), want the one running out", consumed.Load(), spentID)
	}
	// the next pass, straight after, doesn't read it again
	n := read.Load()
	SpendExpiringCodexResets(t.Context())
	if read.Load() != n || consumed.Load() != 1 {
		t.Fatalf("read %d more, spent %d", read.Load()-n, consumed.Load())
	}
}

// The checks and the spend are of the same reset (#5555): a card that
// passed them and vanished by the spend's own read is never replaced by
// another — the spend reads the account's cards afresh, and one the
// checks had passed gone from that read spends nothing, not the card
// that runs out later (which is no more due to be spent). The list is
// read three times on the way to a spend: the look's, the read again
// under the lock, and the spend's own — only the last one loses the
// card. The week at 43% is used, none of it spent, so the account never
// counts as used up.
func TestExpiringSpendKeptToTheCreditChecked(t *testing.T) {
	signIn(t)
	old, oldUntil := expiring.next, expiring.until
	expiring.next, expiring.until = map[string]time.Time{}, map[string]time.Time{}
	t.Cleanup(func() { expiring.next, expiring.until = old, oldUntil })
	soon := time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339)
	later := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	var reads, consumed atomic.Int32
	var spentID string
	// every read from the spend's own on leaves the expired card out, as
	// the vendor's list would
	expireSoon := atomic.Bool{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			json.NewEncoder(w).Encode(map[string]any{"plan_type": "pro",
				"rate_limit": map[string]any{
					"primary_window":   map[string]any{"used_percent": 0, "limit_window_seconds": 18000},
					"secondary_window": map[string]any{"used_percent": 43, "limit_window_seconds": 604800, "reset_at": time.Now().Add(72 * time.Hour).Unix()}},
				"rate_limit_reset_credits": map[string]any{"available_count": 2}})
		case "/backend-api/wham/rate-limit-reset-credits":
			credits := []any{
				map[string]any{"id": "later", "status": "available", "expires_at": later},
				map[string]any{"id": "soon", "status": "available", "expires_at": soon},
			}
			if expireSoon.Load() && reads.Add(1) > 2 { // the spend's own read: "soon" expired and went
				credits = credits[:1]
			}
			json.NewEncoder(w).Encode(map[string]any{"available_count": len(credits), "credits": credits})
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumed.Add(1)
			var b struct {
				Credit string `json:"credit_id"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			spentID = b.Credit
			json.NewEncoder(w).Encode(map[string]any{"code": "reset", "windows_reset": 2})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(fake.Close)
	oldBase := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = oldBase })
	if err := SetCodexAutoReset("me@example.com", true); err != nil {
		t.Fatal(err)
	}
	due := func() {
		expiring.Lock()
		expiring.next["me@example.com"] = time.Time{}
		expiring.Unlock()
	}

	expireSoon.Store(true)
	SpendExpiringCodexResets(t.Context())
	if consumed.Load() != 0 {
		t.Fatalf("spent %d (%q): the card that ran out was replaced by one the checks never passed", consumed.Load(), spentID)
	}
	// the account is looked at again soon: the next pass sees "later" alone,
	// which isn't due to be spent, and still spends nothing
	due()
	SpendExpiringCodexResets(t.Context())
	if consumed.Load() != 0 {
		t.Fatalf("spent %d (%q) with no card about to run out", consumed.Load(), spentID)
	}
	// the checks and the spend of the same card: nothing gone, that one is spent
	expireSoon.Store(false)
	due()
	SpendExpiringCodexResets(t.Context())
	if consumed.Load() != 1 || spentID != "soon" {
		t.Fatalf("spent %d (%q), want the one that runs out first", consumed.Load(), spentID)
	}
}

// #2226 (JasonBuildAI): a Codex switched off or removed from magpie is
// left alone in the background, so the loop reads none of its accounts'
// allowance and spends none of their resets, even with auto-use on.
func TestExpiringResetLeftAlone(t *testing.T) {
	signIn(t)
	old, oldUntil := expiring.next, expiring.until
	expiring.next, expiring.until = map[string]time.Time{}, map[string]time.Time{}
	t.Cleanup(func() { expiring.next, expiring.until = old, oldUntil })
	soon := time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339)
	var read, consumed atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			read.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"plan_type": "pro",
				"rate_limit": map[string]any{
					"primary_window":   map[string]any{"used_percent": 0, "limit_window_seconds": 18000},
					"secondary_window": map[string]any{"used_percent": 35, "limit_window_seconds": 604800, "reset_at": time.Now().Add(72 * time.Hour).Unix()}},
				"rate_limit_reset_credits": map[string]any{"available_count": 1}})
		case "/backend-api/wham/rate-limit-reset-credits":
			json.NewEncoder(w).Encode(map[string]any{"available_count": 1, "credits": []any{
				map[string]any{"id": "soon", "status": "available", "expires_at": soon},
			}})
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumed.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"code": "reset", "windows_reset": 2})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(fake.Close)
	oldBase := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = oldBase })
	if err := SetCodexAutoReset("me@example.com", true); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		p    map[string]any
	}{
		{"switched off", map[string]any{"id": "codex", "off": true}},
		{"removed from magpie", map[string]any{"id": "codex", "hidden": true}},
	} {
		writeFile(t, Path(), map[string]any{"providers": []map[string]any{c.p}})
		SpendExpiringCodexResets(t.Context())
		if read.Load() != 0 || consumed.Load() != 0 {
			t.Errorf("%s: read its allowance %d times, spent %d of its resets", c.name, read.Load(), consumed.Load())
		}
	}
	// switched on again, the same pass spends the one running out
	writeFile(t, Path(), map[string]any{"providers": []map[string]any{{"id": "codex"}}})
	SpendExpiringCodexResets(t.Context())
	if consumed.Load() != 1 {
		t.Fatalf("switched on: spent %d, want the one running out", consumed.Load())
	}
}

// #718 (thedavidweng): an account held up for its five hours until 17:09,
// 32% of its week used, a reset running out that afternoon (18:15 here),
// spent that reset at 15:20, within three hours of it, losing the 68% of the week it could have used
// after 17:09. Spending it starts the windows again, and the fresh ones
// are the same whenever it is: so it is spent half an hour before it runs
// out, looked at every five minutes in its last hour. Only an account held
// up past then spends it at once — waiting can use nothing more, and
// spending it frees the account; windows unused spend nothing.
func TestResetSpentAsLateAsSafe(t *testing.T) {
	day := time.Date(2026, 10, 4, 0, 0, 0, 0, time.Local)
	clock := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	ptr := func(t time.Time) *time.Time { return &t }
	until := clock(18, 15)
	weekBack := clock(0, 0).Add(5 * 24 * time.Hour)
	// fiveBack zero: the five hours don't say when they start again
	windows := func(fiveUsed float64, fiveBack time.Time, weekUsed float64) func(time.Time) []QuotaWindow {
		return func(now time.Time) []QuotaWindow {
			five := QuotaWindow{Span: 5 * time.Hour, Used: fiveUsed}
			if !fiveBack.IsZero() {
				five.ResetsAt = ptr(fiveBack)
				if !now.Before(fiveBack) {
					five.Used = 0
				}
			}
			return []QuotaWindow{five, {Span: 7 * 24 * time.Hour, Used: weekUsed, ResetsAt: ptr(weekBack)}}
		}
	}
	for name, c := range map[string]struct {
		windows func(time.Time) []QuotaWindow
		spent   time.Time // zero: never
	}{
		"the report: five hours free at 17:09": {windows(100, clock(17, 9), 32), clock(17, 45)},
		"five hours free at 17:40":             {windows(100, clock(17, 40), 32), clock(17, 45)},
		"five hours free at 17:50":             {windows(100, clock(17, 50), 32), clock(15, 20)},
		"five hours free after it runs out":    {windows(100, clock(19, 0), 32), clock(15, 20)},
		"five hours used up, not saying when":  {windows(100, time.Time{}, 32), clock(15, 20)},
		"week used up past it":                 {windows(40, clock(17, 0), 100), clock(15, 20)},
		"not held up":                          {windows(60, clock(17, 9), 32), clock(17, 45)},
		"nothing used":                         {windows(0, clock(17, 9), 0), time.Time{}},
	} {
		t.Run(name, func(t *testing.T) {
			e := expiringResets{next: map[string]time.Time{}}
			var spent time.Time
			var looks []time.Time
			// the loop goes over the accounts every resetExpiryClose
			for now := clock(15, 20); now.Before(until.Add(time.Hour)); now = now.Add(resetExpiryClose) {
				e.check("me@example.com", now, func() ([]QuotaWindow, *ResetCredits, error) {
					looks = append(looks, now)
					if !spent.IsZero() || !now.Before(until) {
						return c.windows(now), nil, nil
					}
					return c.windows(now), &ResetCredits{Count: 1, Until: &until}, nil
				}, func() (ResetOutcome, error) {
					if !spent.IsZero() {
						t.Fatalf("spent twice, at %v", now)
					}
					spent = now
					return ResetOutcome{Code: "reset", Windows: 2}, nil
				})
			}
			if !spent.Equal(c.spent) {
				t.Fatalf("spent at %v, want %v (looked at %v)", spent, c.spent, looks)
			}
			// read every half hour or so, not on every pass
			if len(looks) > 20 {
				t.Fatalf("looked %d times: %v", len(looks), looks)
			}
		})
	}
}

// Within the last hour an account is looked at every five minutes, and a
// read or a spend that fails there is tried again in five, so a look
// missed — the Mac asleep, ChatGPT not answering — still spends the reset
// before it runs out; before the last hour every half hour, but never past
// its start.
func TestResetExpiryLastHour(t *testing.T) {
	now := time.Now()
	used := []QuotaWindow{{Span: 7 * 24 * time.Hour, Used: 40}}
	for name, c := range map[string]struct {
		runsOut time.Duration
		next    time.Duration
	}{
		"three hours":     {3 * time.Hour, 30 * time.Minute},
		"ninety minutes":  {90 * time.Minute, 30 * time.Minute},
		"seventy minutes": {70 * time.Minute, 10 * time.Minute},
		"fifty minutes":   {50 * time.Minute, 5 * time.Minute},
	} {
		if got := nextResetLook(now.Add(c.runsOut), now).Sub(now); got != c.next {
			t.Errorf("%s: next look in %v, want %v", name, got, c.next)
		}
	}

	until := now.Add(50 * time.Minute)
	e := expiringResets{next: map[string]time.Time{}}
	// read once, not due yet
	e.check("me@example.com", now, func() ([]QuotaWindow, *ResetCredits, error) { return used, &ResetCredits{Count: 1, Until: &until}, nil },
		func() (ResetOutcome, error) { t.Fatal("spent early"); return ResetOutcome{}, nil })
	// a failed read: again in five minutes, by the reset read before
	now = now.Add(5 * time.Minute)
	if _, err := e.check("me@example.com", now, func() ([]QuotaWindow, *ResetCredits, error) { return nil, nil, http.ErrHandlerTimeout },
		func() (ResetOutcome, error) { return ResetOutcome{}, nil }); err == nil || e.next["me@example.com"].Sub(now) != resetExpiryClose {
		t.Fatalf("failed read: %v, next in %v", err, e.next["me@example.com"].Sub(now))
	}
	// due, and the spend fails: again in five minutes
	now = until.Add(-25 * time.Minute)
	out, err := e.check("me@example.com", now, func() ([]QuotaWindow, *ResetCredits, error) { return used, &ResetCredits{Count: 1, Until: &until}, nil },
		func() (ResetOutcome, error) { return ResetOutcome{}, http.ErrHandlerTimeout })
	if err == nil || out.Code != "" || e.next["me@example.com"].Sub(now) != resetExpiryClose {
		t.Fatalf("failed spend: %+v %v, next in %v", out, err, e.next["me@example.com"].Sub(now))
	}
	// the Mac asleep past the lead: spent on the first look awake
	now = until.Add(-3 * time.Minute)
	if out, _ = e.check("me@example.com", now, func() ([]QuotaWindow, *ResetCredits, error) { return used, &ResetCredits{Count: 1, Until: &until}, nil },
		func() (ResetOutcome, error) { return ResetOutcome{Code: "reset"}, nil }); out.Code != "reset" {
		t.Fatalf("after sleeping: %+v", out)
	}
}

// the expiring spend reads the account again under autoReset's lock: a
// held-up account the week's used-up spend has just started again (its
// windows back at 0) gets no second reset, one still held up does
func TestExpiringSpendReadAgain(t *testing.T) {
	now := time.Date(2026, 10, 4, 15, 20, 0, 0, time.UTC)
	until := now.Add(2 * time.Hour)
	back := now.Add(3 * time.Hour)
	held := &ResetCredits{Count: 2, Until: &until}
	blocked := []QuotaWindow{{Span: 7 * 24 * time.Hour, Used: 100, ResetsAt: &back}}
	fresh := []QuotaWindow{{Span: 7 * 24 * time.Hour, Used: 0, ResetsAt: &back}}
	freeSoon := now.Add(30 * time.Minute)
	waiting := []QuotaWindow{{Span: 5 * time.Hour, Used: 100, ResetsAt: &freeSoon}, {Span: 7 * 24 * time.Hour, Used: 32}}
	for name, c := range map[string]struct {
		w    []QuotaWindow
		r    *ResetCredits
		want bool
	}{
		"still held up past expiry":  {blocked, held, true},
		"started again meanwhile":    {fresh, held, false},
		"free again before the lead": {waiting, held, false},
		"none held any more":         {blocked, &ResetCredits{}, false},
		"run out already":            {blocked, &ResetCredits{Count: 1, Until: &now}, false},
	} {
		if _, got := spendExpiringNow(c.w, c.r, now); got != c.want {
			t.Errorf("%s: spendExpiringNow = %v, want %v", name, got, c.want)
		}
	}
}

// donutsone on X: a reset card ran out unspent with "before one expires"
// on. A failed read of when the cards run out (the second call, bounded
// at 4s) gave a nil Until, which check took for "none runs out": the
// account went unread for resetExpiryFar and the card it had just seen,
// minutes from running out, was forgotten. Now that read is unknown: the
// account is looked at again by the card last read, and spends it.
func TestExpiringResetSpentPastAFailedRead(t *testing.T) {
	signIn(t)
	old, oldUntil := expiring.next, expiring.until
	expiring.next, expiring.until = map[string]time.Time{}, map[string]time.Time{}
	t.Cleanup(func() { expiring.next, expiring.until = old, oldUntil })
	soon := time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339)
	var used, creditsDown atomic.Bool
	var consumed atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			week := 0
			if used.Load() {
				week = 35
			}
			json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus",
				"rate_limit": map[string]any{
					"primary_window":   map[string]any{"used_percent": 0, "limit_window_seconds": 18000},
					"secondary_window": map[string]any{"used_percent": week, "limit_window_seconds": 604800, "reset_at": time.Now().Add(72 * time.Hour).Unix()}},
				"rate_limit_reset_credits": map[string]any{"available_count": 1}})
		case "/backend-api/wham/rate-limit-reset-credits":
			if creditsDown.Load() {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"available_count": 1, "credits": []any{
				map[string]any{"id": "soon", "status": "available", "expires_at": soon},
			}})
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumed.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"code": "reset", "windows_reset": 2})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(fake.Close)
	oldBase := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = oldBase })
	if err := SetCodexAutoResetCase("me@example.com", ResetCaseExpiring, true); err != nil {
		t.Fatal(err)
	}
	due := func() { // the next look's time has come
		expiring.Lock()
		expiring.next["me@example.com"] = time.Time{}
		expiring.Unlock()
	}
	wait := func() time.Duration {
		expiring.Lock()
		defer expiring.Unlock()
		return time.Until(expiring.next["me@example.com"])
	}

	// the card is seen, 20 minutes left, nothing used yet: kept, looked at again soon
	SpendExpiringCodexResets(t.Context())
	if consumed.Load() != 0 || wait() > resetExpiryClose {
		t.Fatalf("spent %d, next look in %v", consumed.Load(), wait())
	}
	// the account is used, and the read of when the card runs out fails
	used.Store(true)
	creditsDown.Store(true)
	due()
	SpendExpiringCodexResets(t.Context())
	if consumed.Load() != 0 {
		t.Fatalf("spent %d with the card unread", consumed.Load())
	}
	if w := wait(); w > resetExpiryClose {
		t.Fatalf("a failed read leaves the account for %v, past the card running out", w)
	}
	// the next look reads it, and spends it
	creditsDown.Store(false)
	due()
	SpendExpiringCodexResets(t.Context())
	if consumed.Load() != 1 {
		t.Fatalf("spent %d, want the card about to run out", consumed.Load())
	}
}

// A failed read of when the resets run out is unknown (untilUnread); one
// that says none does is not.
func TestCodexResetsUntilUnread(t *testing.T) {
	var down atomic.Bool
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"available_count": 1, "credits": []any{
			map[string]any{"id": "a", "status": "available"},
		}})
	}))
	t.Cleanup(fake.Close)
	if r := codexResets(t.Context(), fake.URL, "tok", "", 1); r.Until != nil || r.untilUnread {
		t.Fatalf("one that never runs out: %+v", r)
	}
	down.Store(true)
	if r := codexResets(t.Context(), fake.URL, "tok", "", 1); r.Until != nil || !r.untilUnread {
		t.Fatalf("a failed read: %+v", r)
	}
	// the background look waits longer than the card's 4s for it
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]any{"available_count": 1, "credits": []any{
			map[string]any{"id": "a", "status": "available", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
		}})
	}))
	t.Cleanup(slow.Close)
	oldWait := codexResetDetailsTimeout
	codexResetDetailsTimeout = 50 * time.Millisecond
	t.Cleanup(func() { codexResetDetailsTimeout = oldWait })
	if r := codexResets(t.Context(), slow.URL, "tok", "", 1); !r.untilUnread {
		t.Fatalf("the card's look past its bound: %+v", r)
	}
	if r := codexResets(withResetDetailsWait(t.Context(), 10*time.Second), slow.URL, "tok", "", 1); r.Until == nil || r.untilUnread {
		t.Fatalf("the background look: %+v", r)
	}
}
