package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fakeWeekOnCredits stands in for ChatGPT's backend for an account whose
// week is used up and which goes on on its credits, as the real Pro
// reading does (testdata/codex_usage_pro_credits.json): the balance the
// next reading says is *balance, and each spend is counted.
func fakeWeekOnCredits(t *testing.T, balance *atomic.Value) *atomic.Int32 {
	t.Helper()
	weekEnds := time.Now().Add(72 * time.Hour).Unix()
	soon := time.Now().Add(5 * 24 * time.Hour).UTC().Format(time.RFC3339)
	var consumed atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			json.NewEncoder(w).Encode(map[string]any{"plan_type": "pro",
				"rate_limit": map[string]any{
					"allowed": false, "limit_reached": true,
					"primary_window": map[string]any{"used_percent": 100, "limit_window_seconds": 604800, "reset_at": weekEnds},
				},
				"credits":                  map[string]any{"has_credits": true, "unlimited": false, "balance": balance.Load().(string)},
				"rate_limit_reset_credits": map[string]any{"available_count": 1}})
		case "/backend-api/wham/rate-limit-reset-credits":
			json.NewEncoder(w).Encode(map[string]any{"available_count": 1, "credits": []any{
				map[string]any{"id": "card", "status": "available", "expires_at": soon},
			}})
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			consumed.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"code": "reset", "windows_reset": 2})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(fake.Close)
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	return &consumed
}

// shangyuan0818 on X: the week used up over an hour before, the account
// went on on its credits and spent many, and no reset was used, the
// requests never passing magpie's gateway where one would have been. Two
// readings in that week, the credits fewer in the second, spend the reset
// set to be used when the week runs out, with no request at all; a
// balance that holds, or an account not set to, spends none.
func TestWeekUsedUpCreditsGoingSpendsReset(t *testing.T) {
	for name, c := range map[string]struct {
		when     string // the case turned on, "" none
		balances []string
		spent    int32
	}{
		"credits going":                   {ResetCaseWeek, []string{"28263.2358130000", "28250.1000000000"}, 1},
		"credits going, both cases on":    {"both", []string{"28263.2358130000", "28263.2"}, 1},
		"credits holding":                 {ResetCaseWeek, []string{"28263.2358130000", "28263.2358130000"}, 0},
		"credits bought":                  {ResetCaseWeek, []string{"100", "28263.2358130000"}, 0},
		"one reading":                     {ResetCaseWeek, []string{"28263.2358130000"}, 0},
		"kept to the one about to expire": {ResetCaseExpiring, []string{"28263.2358130000", "28250"}, 0},
		"not set to":                      {"", []string{"28263.2358130000", "28250"}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			home := signIn(t)
			forgetWeekCredits(t)
			switch c.when {
			case "both":
				if err := SetCodexAutoReset("me@example.com", true); err != nil {
					t.Fatal(err)
				}
			case "":
			default:
				if err := SetCodexAutoResetCase("me@example.com", c.when, true); err != nil {
					t.Fatal(err)
				}
			}
			var balance atomic.Value
			consumed := fakeWeekOnCredits(t, &balance)
			dir := filepath.Join(home, ".codex")
			for _, b := range c.balances {
				balance.Store(b)
				if q := codexSubscriptionUsage(t.Context(), dir, "me@example.com"); q.Error != "" {
					t.Fatalf("reading: %s", q.Error)
				}
				endReadsOut(t, "after a reading")
			}
			if got := consumed.Load(); got != c.spent {
				t.Fatalf("spent %d resets, want %d", got, c.spent)
			}
		})
	}
}

// The readings of every account magpie holds (loginQuota) count the same
// as the signed-in one's card, and a third reading with the credits still
// going, the week's reset spent, spends no second one.
func TestWeekUsedUpCreditsGoingSpendsOneAWeek(t *testing.T) {
	signIn(t)
	forgetWeekCredits(t)
	if err := SetCodexAutoResetCase("me@example.com", ResetCaseWeek, true); err != nil {
		t.Fatal(err)
	}
	var balance atomic.Value
	consumed := fakeWeekOnCredits(t, &balance)
	for _, b := range []string{"500", "490", "480"} {
		balance.Store(b)
		if q := loginQuota(t.Context(), Login{Agent: "codex", User: "me@example.com", Active: true}); q.Error != "" {
			t.Fatalf("reading: %s", q.Error)
		}
		endReadsOut(t, "after a reading")
	}
	if got := consumed.Load(); got != 1 {
		t.Fatalf("spent %d resets, want 1", got)
	}
}

// forgetWeekCredits starts a test with no reading kept.
func forgetWeekCredits(t *testing.T) {
	t.Helper()
	reset := func() {
		weekCredits.Lock()
		weekCredits.m = map[string]weekCreditsSeen{}
		weekCredits.Unlock()
	}
	reset()
	t.Cleanup(reset)
}
