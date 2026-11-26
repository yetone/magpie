package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// setResetDays saves the days a reset used by itself may have left, in a
// sandbox HOME.
func setResetDays(t *testing.T, least, most int) {
	t.Helper()
	if err := SetCodexResetDays(least, most); err != nil {
		t.Fatal(err)
	}
}

// shangyuan0818 on X: only the resets with so many days left before they
// expire, by the clock now, are used by themselves; none set is every
// one, as before. A reset that never expires has no most to be within.
func TestResetInDays(t *testing.T) {
	now := time.Now()
	in := func(d time.Duration) *time.Time { u := now.Add(d); return &u }
	day := 24 * time.Hour
	for _, c := range []struct {
		least, most int
		until       *time.Time
		want        bool
	}{
		{0, 0, in(20 * time.Minute), true},
		{0, 0, in(300 * day), true},
		{0, 0, nil, true},
		{0, 7, in(20 * time.Minute), true},
		{0, 7, in(3 * day), true},
		{0, 7, in(7 * day), true},
		{0, 7, in(7*day + time.Second), false},
		{0, 7, in(20 * day), false},
		{0, 7, nil, false},
		{3, 0, in(2 * day), false},
		{3, 0, in(3 * day), true},
		{3, 0, nil, true},
		{1, 7, in(20 * time.Minute), false},
		{1, 7, in(3 * day), true},
		{0, 9999, in(400 * day), false}, // held to the limit, 365
	} {
		s := settings.Settings{CodexResetDaysLeast: c.least, CodexResetDaysMost: c.most}
		if got := s.ResetInDays(c.until, now); got != c.want {
			var left any = "never"
			if c.until != nil {
				left = c.until.Sub(now)
			}
			t.Errorf("%d–%d days, %v left: %v, want %v", c.least, c.most, left, got, c.want)
		}
	}
}

func TestSetCodexResetDays(t *testing.T) {
	signIn(t)
	for _, c := range [][2]int{{-1, 0}, {0, -1}, {0, settings.CodexResetDaysLimit + 1}, {8, 7}} {
		if err := SetCodexResetDays(c[0], c[1]); err == nil {
			t.Errorf("%v taken", c)
		}
	}
	if s := settings.Load(); s.CodexResetDaysLeast != 0 || s.CodexResetDaysMost != 0 {
		t.Fatalf("a refused range was written: %d–%d", s.CodexResetDaysLeast, s.CodexResetDaysMost)
	}
	setResetDays(t, 5, 0) // at least, no most
	setResetDays(t, 0, 7)
	if s := settings.Load(); s.CodexResetDaysLeast != 0 || s.CodexResetDaysMost != 7 {
		t.Fatalf("saved %d–%d, want 0–7", s.CodexResetDaysLeast, s.CodexResetDaysMost)
	}
}

// fakeResetCards stands in for ChatGPT with an account whose week is
// used up, holding a reset that runs out in 20 minutes, one in 3 days,
// one in 20 days and one that never does, listed out of order as the
// vendor may; it says which one was spent.
func fakeResetCards(t *testing.T) func() []string {
	now := time.Now()
	at := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339) }
	var mu sync.Mutex
	var spent []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			json.NewEncoder(w).Encode(map[string]any{"plan_type": "pro",
				"rate_limit": map[string]any{
					"primary_window":   map[string]any{"used_percent": 10, "limit_window_seconds": 18000},
					"secondary_window": map[string]any{"used_percent": 100, "limit_window_seconds": 604800, "reset_at": time.Now().Add(72 * time.Hour).Unix()},
				},
				"rate_limit_reset_credits": map[string]any{"available_count": 4}})
		case "/backend-api/wham/rate-limit-reset-credits":
			json.NewEncoder(w).Encode(map[string]any{"available_count": 4, "credits": []any{
				map[string]any{"id": "far", "status": "available", "expires_at": at(20 * 24 * time.Hour)},
				map[string]any{"id": "never", "status": "available"},
				map[string]any{"id": "mid", "status": "available", "expires_at": at(3 * 24 * time.Hour)},
				map[string]any{"id": "soon", "status": "available", "expires_at": at(20 * time.Minute)},
			}})
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			var b struct {
				Credit string `json:"credit_id"`
			}
			json.NewDecoder(r.Body).Decode(&b)
			mu.Lock()
			spent = append(spent, b.Credit)
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"code": "reset", "windows_reset": 2})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(fake.Close)
	oldBase := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = oldBase })
	return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), spent...) }
}

// A used-up week spends the soonest reset within the days set, never one
// out of them; none set, the soonest of all, as before.
func TestWeekResetKeptToTheDays(t *testing.T) {
	for _, c := range []struct {
		name        string
		least, most int
		want        string // "" none
	}{
		{"none set", 0, 0, "soon"},
		{"at most 7", 0, 7, "soon"},
		{"1 to 7", 1, 7, "mid"},
		{"at least 5", 5, 0, "far"},
		{"at least 30", 30, 0, "never"},
		{"8 to 10", 8, 10, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			signIn(t)
			spent := fakeResetCards(t)
			if err := SetCodexAutoResetCase("me@example.com", ResetCaseWeek, true); err != nil {
				t.Fatal(err)
			}
			setResetDays(t, c.least, c.most)
			out, err := AutoUseCodexReset(t.Context(), "me@example.com")
			if err != nil {
				t.Fatal(err)
			}
			got := spent()
			if c.want == "" {
				if len(got) != 0 || out.Code == "reset" {
					t.Fatalf("spent %v (%+v), want none", got, out)
				}
				return
			}
			if out.Code != "reset" || len(got) != 1 || got[0] != c.want {
				t.Fatalf("spent %v (%+v), want %s", got, out, c.want)
			}
		})
	}
}

// The spend itself holds to the days too, at the moment it spends: a
// reset named by the expiring path out of them isn't spent, and one
// spent by hand never is held to them.
func TestSpendKeptToTheDays(t *testing.T) {
	signIn(t)
	spent := fakeResetCards(t)
	setResetDays(t, 1, 7)
	if out, err := spendCodexReset(t.Context(), "me@example.com", "soon", ResetUsedExpiring); err != nil || out.Code != resetNoneInDays {
		t.Fatalf("expiring, out of the days: %+v %v", out, err)
	}
	if out, err := spendCodexReset(t.Context(), "me@example.com", "", ResetUsedWeek); err != nil || out.Code != "reset" {
		t.Fatalf("the week's: %+v %v", out, err)
	}
	if out, err := spendCodexReset(t.Context(), "me@example.com", "far", ResetUsedByHand); err != nil || out.Code != "reset" {
		t.Fatalf("by hand: %+v %v", out, err)
	}
	if got := spent(); len(got) != 2 || got[0] != "mid" || got[1] != "far" {
		t.Fatalf("spent %v, want mid by itself and far by hand", got)
	}
	if (ResetOutcome{Code: resetNoneInDays}).Text() == resetNoneInDays {
		t.Fatal("none_in_days has no words")
	}
}

// The one about to run out is spent by itself only when it has the days
// left then, and routing counts on it starting the windows again only
// then: the two go by the same time (Allowance.Restarts).
func TestExpiringResetKeptToTheDays(t *testing.T) {
	signIn(t)
	now := time.Now().Round(0)
	until := now.Add(20 * time.Minute)
	used := []QuotaWindow{{Span: 7 * 24 * time.Hour, Used: 40}}
	a := Allowance{{Used: 40, Resets: now.Add(72 * time.Hour), Span: 7 * 24 * time.Hour}}.restartedBy(until)
	check := func() string {
		e := expiringResets{next: map[string]time.Time{}}
		out, err := e.check("me@example.com", now, func() ([]QuotaWindow, *ResetCredits, error) {
			return used, &ResetCredits{Count: 1, Until: &until}, nil
		}, func() (ResetOutcome, error) { return ResetOutcome{Code: "reset"}, nil })
		if err != nil {
			t.Fatal(err)
		}
		return out.Code
	}
	if got := check(); got != "reset" {
		t.Fatalf("none set: %q, want it spent", got)
	}
	if got := a.Restarts(now); !got.Equal(now) {
		t.Fatalf("none set: routing restarts at %v", got)
	}
	setResetDays(t, 1, 0)
	if got := check(); got != "" {
		t.Fatalf("at least a day: %q, want none spent", got)
	}
	if got := a.Restarts(now); !got.IsZero() {
		t.Fatalf("at least a day: routing counts on a restart at %v", got)
	}
	setResetDays(t, 0, 7)
	if got := check(); got != "reset" {
		t.Fatalf("at most 7 days: %q, want it spent", got)
	}
}
