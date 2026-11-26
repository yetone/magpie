package mcpauth_test

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/mcpauth/mcpauthtest"
)

// testClock is a clock a test moves on by hand.
type testClock struct{ now atomic.Int64 }

func setClock(t *testing.T) *testClock {
	c := &testClock{}
	// whole seconds: a wait is kept in ms, and a Retry-After date has
	// seconds
	c.now.Store(time.Now().Truncate(time.Second).UnixNano())
	t.Cleanup(mcpauth.SetClockForTest(func() time.Time { return time.Unix(0, c.now.Load()) }))
	return c
}

func (c *testClock) add(d time.Duration) { c.now.Add(int64(d)) }

func (c *testClock) at() time.Time { return time.Unix(0, c.now.Load()) }

// staleSignIn signs in to a fake whose tokens are spent: every request
// asks for a renewal, which the server turns away with 429 and
// retryAfter.
func staleSignIn(t *testing.T, retryAfter string) (*mcpauthtest.Fake, string) {
	f := mcpauthtest.New(t)
	f.SignIn(t, "canva")
	stale, _ := mcpauth.Token(t.Context(), "canva")
	f.Revoke()
	f.Throttle(retryAfter)
	return f, stale
}

// #7451 (Asher-S-Wu): Canva's token endpoint answered a renewal 429
// ("Too many refresh_token requests"), and every request after it asked
// again. The Retry-After it gave is waited out, by every request at once,
// and the sign-in stands; once the wait is over one renewal is asked for,
// however many requests want it.
func TestRenew429WaitsRetryAfter(t *testing.T) {
	home(t)
	c := setClock(t)
	f, stale := staleSignIn(t, "60")
	_, err := mcpauth.Renew(t.Context(), "canva", stale)
	var rl *mcpauth.RateLimitError
	if !errors.As(err, &rl) || !errors.Is(err, mcpauth.ErrRateLimited) {
		t.Fatalf("a renewal answered 429: %v", err)
	}
	if !strings.Contains(err.Error(), "Too many refresh_token requests") || !strings.Contains(err.Error(), "1m0s") {
		t.Errorf("the error doesn't give the server's words and the wait: %v", err)
	}
	if got := rl.At.Sub(c.at()); got != time.Minute {
		t.Errorf("waits %v, want Retry-After's 1m", got)
	}
	// every request meanwhile waits, without asking the server again
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := mcpauth.Renew(t.Context(), "canva", stale); !errors.Is(err, mcpauth.ErrRateLimited) {
				t.Errorf("Renew in the wait: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := mcpauth.Token(t.Context(), "canva"); err != nil {
				t.Errorf("Token in the wait of a token that lasts: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := f.RefreshAsked(); n != 1 {
		t.Fatalf("asked to renew %d times in the wait, want 1", n)
	}
	st := mcpauth.StatusOf("canva", f.URL)
	if !st.SignedIn || st.Dead || st.RetryAt != rl.At.UnixMilli() {
		t.Fatalf("status in the wait %+v, want signed in with RetryAt %d", st, rl.At.UnixMilli())
	}

	// the wait over, one renewal, however many ask
	c.add(61 * time.Second)
	f.Unthrottle()
	got := make([]string, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := mcpauth.Renew(t.Context(), "canva", stale)
			if err != nil {
				t.Error(err)
			}
			got[i] = tok
		}()
	}
	wg.Wait()
	if n := f.RefreshAsked(); n != 2 {
		t.Fatalf("asked to renew %d times after the wait, want 2 in all", n)
	}
	for _, g := range got {
		if g == stale || g != got[0] {
			t.Fatalf("tokens %v (stale %s)", got, stale)
		}
	}
	if st := mcpauth.StatusOf("canva", f.URL); st.RetryAt != 0 {
		t.Fatalf("a renewal didn't end the wait: %+v", st)
	}
	if r, _ := mcpauth.Get("canva"); r.RetryAt != 0 || r.RateLimits != 0 {
		t.Fatalf("a renewal kept the wait: %+v", r)
	}
}

// A Retry-After given as a date is waited out to that date, and one far
// off at most a day.
func TestRenew429RetryAfterDate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after func(now time.Time) string
		want  time.Duration
	}{
		{"date", func(now time.Time) string { return now.Add(10 * time.Minute).UTC().Format(http.TimeFormat) }, 10 * time.Minute},
		{"far date", func(now time.Time) string { return now.Add(30 * 24 * time.Hour).UTC().Format(http.TimeFormat) }, 24 * time.Hour},
		{"far seconds", func(time.Time) string { return "99999999999" }, 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home(t)
			c := setClock(t)
			_, stale := staleSignIn(t, tc.after(c.at()))
			_, err := mcpauth.Renew(t.Context(), "canva", stale)
			var rl *mcpauth.RateLimitError
			if !errors.As(err, &rl) {
				t.Fatalf("Renew: %v", err)
			}
			if got := rl.At.Sub(c.at()); got != tc.want {
				t.Fatalf("waits %v, want %v", got, tc.want)
			}
		})
	}
}

// With no Retry-After, the wait doubles from 30s with each 429 in a row,
// up to 15m.
func TestRenew429BacksOff(t *testing.T) {
	home(t)
	c := setClock(t)
	f, stale := staleSignIn(t, "")
	for i, want := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute} {
		_, err := mcpauth.Renew(t.Context(), "canva", stale)
		var rl *mcpauth.RateLimitError
		if !errors.As(err, &rl) {
			t.Fatalf("429 %d: %v", i+1, err)
		}
		if got := rl.At.Sub(c.at()); got != want {
			t.Fatalf("429 %d waits %v, want %v", i+1, got, want)
		}
		if n := f.RefreshAsked(); n != i+1 {
			t.Fatalf("429 %d: asked %d times", i+1, n)
		}
		c.add(want)
	}
}

// A token still good when its renewal is turned away with 429 is used
// until it runs out: the request that met the 429 gets it too. Once it
// has run out, the wait is what a request gets.
func TestRenew429KeepsAGoodToken(t *testing.T) {
	home(t)
	c := setClock(t)
	f := mcpauthtest.New(t)
	f.ExpiresIn = 100 // due for a renewal, not run out
	f.SignIn(t, "canva")
	r, _ := mcpauth.Get("canva")
	f.Throttle("300")
	for range 3 {
		tok, err := mcpauth.Token(t.Context(), "canva")
		if err != nil || tok != r.Access {
			t.Fatalf("Token = %q, %v; want the token that lasts", tok, err)
		}
	}
	if n := f.RefreshAsked(); n != 1 {
		t.Fatalf("asked to renew %d times, want 1", n)
	}
	c.add(101 * time.Second)
	if _, err := mcpauth.Token(t.Context(), "canva"); !errors.Is(err, mcpauth.ErrRateLimited) {
		t.Fatalf("Token of a run-out token in the wait: %v", err)
	}
	if n := f.RefreshAsked(); n != 1 {
		t.Fatalf("asked to renew %d times, want 1", n)
	}
}

// Each account has its own wait: switching to another account renews
// that one, and signing in again ends the wait.
func TestRenew429IsTheAccounts(t *testing.T) {
	home(t)
	setClock(t)
	f := mcpauthtest.New(t)
	f.SignIn(t, "canva")
	first, _ := mcpauth.Get("canva")
	mcpauthtest.SignInAccount(t, "canva", f.URL, "work")
	work, _ := mcpauth.Get("canva")
	f.Revoke()
	f.Throttle("600")
	if _, err := mcpauth.Renew(t.Context(), "canva", work.Access); !errors.Is(err, mcpauth.ErrRateLimited) {
		t.Fatalf("Renew: %v", err)
	}
	if err := mcpauth.Use("canva", mcpauth.Accounts("canva")[0].ID); err != nil {
		t.Fatal(err)
	}
	if st := mcpauth.StatusOf("canva", f.URL); st.RetryAt != 0 {
		t.Fatalf("the other account waits too: %+v", st)
	}
	f.Unthrottle()
	if _, err := mcpauth.Renew(t.Context(), "canva", first.Access); err != nil {
		t.Fatalf("the other account wasn't renewed: %v", err)
	}
	if n := f.RefreshAsked(); n != 2 {
		t.Fatalf("asked %d times, want 2", n)
	}

	// signing in again ends the wait
	f.Throttle("600")
	cur, _ := mcpauth.Get("canva")
	f.Revoke()
	if _, err := mcpauth.Renew(t.Context(), "canva", cur.Access); !errors.Is(err, mcpauth.ErrRateLimited) {
		t.Fatalf("Renew: %v", err)
	}
	f.Unthrottle()
	f.SignIn(t, "canva")
	if st := mcpauth.StatusOf("canva", f.URL); st.RetryAt != 0 || st.Dead {
		t.Fatalf("signed in again, still waits: %+v", st)
	}
	if r, _ := mcpauth.Get("canva"); r.RetryAt != 0 || r.RateLimits != 0 {
		t.Fatalf("signed in again, still waits: %+v", r)
	}
}
