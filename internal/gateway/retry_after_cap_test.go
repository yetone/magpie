package gateway

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// burnBoth rests both keys the way a plan's day running out does: the
// quota comes back at, hours from now.
func burnBoth(t *testing.T, hours int, on ...func()) *httptest.ResponseRecorder {
	d, srv := deadChain(t, "dead-a", "dead-b")
	t.Cleanup(func() { _ = d.took() })
	for _, f := range on {
		f()
	}
	at := time.Now().Add(time.Duration(hours)*time.Hour + 21*time.Minute).Truncate(time.Minute)
	if err := provider.SetQuotaResetAt("mockdead", at.UTC().Format("15:04")+"Z"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		postDead(t, srv)
	}
	rec := postDead(t, srv)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("turn-away: %d %s", rec.Code, rec.Body)
	}
	// the confirm asks restLast makes (one resting candidate a request,
	// #1772) are the vendor's business as ever; the cap is about the
	// wait the agent is told, not about burning the accounts
	d.took()
	return rec
}

// #5371 (tlerbao): a quota exhaustion rests the account for hours, and
// the 429 the agent gets carried the whole horizon as Retry-After — an
// agent that runs its own fallback chain (OpenCode clamps a single wait
// at 900s, retries the same model a few times) then stalls ~45 minutes
// before it ever fails over.
func TestRetryAfterGoesOutWholeByDefault(t *testing.T) {
	rec := burnBoth(t, 5)
	ra, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || ra < 4*3600 {
		t.Fatalf("default: Retry-After %q, want the rest's whole horizon (~5h), err %v", rec.Header().Get("Retry-After"), err)
	}
	if !strings.Contains(rec.Body.String(), "weekly usage limit") {
		t.Errorf("default: the vendor's reason is gone: %s", rec.Body)
	}
}

func TestRetryAfterCapBoundsTheAgentWait(t *testing.T) {
	rec := burnBoth(t, 5, func() {
		// the cap is on before the plan's day runs out: the agent's wait
		// is bounded, the accounts rest the whole horizon just the same
		capRetryAfterOn(t, 60)
	})
	ra, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || ra > 60 {
		t.Fatalf("capped: Retry-After %q, want at most 60s — the agent fails over instead of sleeping the horizon (err %v)", rec.Header().Get("Retry-After"), err)
	}
	if !strings.Contains(rec.Body.String(), "weekly usage limit") {
		t.Errorf("capped: the vendor's reason is gone: %s", rec.Body)
	}
}

// capRetryAfterOn turns the cap on, as Settings' row saves it.
func capRetryAfterOn(t *testing.T, secs int) {
	t.Helper()
	st := settings.Load()
	st.RetryAfterCap = &secs
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
}

// The cap bounds only what the agent is told: the accounts rest the
// plan's whole horizon inside magpie, and are not asked again sooner.
func TestRetryAfterCapLeavesTheRestWhole(t *testing.T) {
	burnBoth(t, 5, func() { capRetryAfterOn(t, 60) })
	restingUntil.Lock()
	var longest time.Time
	for _, until := range restingUntil.m {
		if until.After(longest) {
			longest = until
		}
	}
	restingUntil.Unlock()
	if time.Until(longest) < 4*time.Hour {
		t.Fatalf("the accounts rest until %v, want the plan's reset ~5h away", longest)
	}
}

// A vendor's own Retry-After, passed on with its 429 (keepRetry), is
// bounded the same way: Antigravity's quota runs out with a reset hours
// away, said by the vendor rather than by magpie's rest.
func TestRetryAfterCapBoundsTheVendorsOwn(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cap        int
		said, want string
	}{
		{"unset", 0, "19821", "19821"},
		{"over", 60, "19821", "60"},
		{"under", 60, "30", "30"},
		{"date", 60, time.Now().Add(5 * time.Hour).UTC().Format(http.TimeFormat), "60"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			restingUntil.Lock()
			restingUntil.m, restingUntil.note = map[string]time.Time{}, map[string]Rest{}
			restingUntil.Unlock()
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", tc.said)
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"error":{"message":"quota exhausted","type":"rate_limit_error"}}`))
			}))
			t.Cleanup(up.Close)
			if err := provider.Save(provider.Provider{ID: "mockdead", Name: "mockdead", Chat: up.URL + "/v1", Models: []string{"glm-5.3-flash"}, Key: "k"}); err != nil {
				t.Fatal(err)
			}
			if tc.cap > 0 {
				capRetryAfterOn(t, tc.cap)
			}
			rec := postDead(t, New())
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("status %d %s", rec.Code, rec.Body)
			}
			if got := rec.Header().Get("Retry-After"); got != tc.want {
				t.Errorf("Retry-After %q, want %q", got, tc.want)
			}
		})
	}
}
