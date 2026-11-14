package gateway

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A rest is kept on the wall clock alone. A time from time.Now() carries
// the monotonic clock too, and Go compares two such times by it; on macOS
// it stops while the computer sleeps, so a rest to 00:40 set the day
// before still held at 09:57 after a night of sleep, 17 hours past the
// time it showed (plugins#1126). Sleep can't be had in a test, so this
// checks what makes it harmless: no rest magpie keeps carries a monotonic
// reading ("m=" in its String).
func TestRestsKeepTheWallClock(t *testing.T) {
	monotonic := func(at time.Time) bool { return strings.Contains(at.String(), " m=") }
	if !monotonic(time.Now().Add(time.Hour)) {
		t.Fatal("time.Now() carries no monotonic reading here: the test proves nothing")
	}
	codexSignedIn(t)
	s := New()
	c := candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "me@example.com"}}, model: "gpt-6-astra", rest: "codex@me@example.com"}

	quota := []byte(`{"error":{"message":"usage exceeds frequency limit, your usage will reset at ` +
		time.Now().Add(14*time.Hour).Format("2006-01-02 15:04:05") + `","type":"usage_limit_reached"}}`)
	for _, x := range []struct {
		name string
		body []byte
		code int
	}{
		{"quota", quota, http.StatusTooManyRequests},
		{"500", []byte(`{"error":{"message":"An error occurred while processing your request.","type":"server_error"}}`), http.StatusInternalServerError},
	} {
		r := s.restAfter(c, x.code, http.Header{}, x.body)
		if r.Until.IsZero() {
			t.Fatalf("%s: no rest: the test proves nothing", x.name)
		}
		if monotonic(r.Until) {
			t.Errorf("%s: the rest's end %v counts on the monotonic clock", x.name, r.Until)
		}
		restingUntil.Lock()
		for k, until := range restingUntil.m {
			if monotonic(until) {
				t.Errorf("%s: %s rests to %v on the monotonic clock", x.name, k, until)
			}
		}
		restingUntil.Unlock()
	}

	slow := candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "slow@example.com"}}, model: "gpt-6-astra", rest: "codex@slow@example.com"}
	rested := false
	for i := 0; i < slowRests+1; i++ {
		if r, ok := restSlow(slow, time.Now()); ok {
			rested = true
			if monotonic(r.Until) {
				t.Errorf("a slow start's rest to %v counts on the monotonic clock", r.Until)
			}
		}
	}
	if !rested {
		t.Fatal("slow starts never rested: the test proves nothing")
	}

	restSearchKey("tavily", "k1", time.Hour, "rate limited", time.Now())
	if r, ok := searchKeyRest("tavily", "k1", time.Now()); !ok || monotonic(r.Until) {
		t.Errorf("a search key's rest %v (%v) counts on the monotonic clock", r.Until, ok)
	}
}
