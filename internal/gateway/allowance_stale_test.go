package gateway

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

var staleMidReadRuns atomic.Int32

// An account refused while its allowance is being read, and refused again
// once that reading is back, rests until its window renews: the reading,
// asked before the first refusal, is read again at once, not trusted for a
// minute, so the second refusal finds the window full rather than resting
// a quarter of an hour by "quota" and being tried and refused once more.
func TestRefusedMidReadRestsByWindow(t *testing.T) {
	fresh(t)
	old := allowances
	allowances = provider.Allowances
	t.Cleanup(func() { allowances = old })
	// an agent no other test, nor an earlier run of this one, reads; named
	// as a subscription moved to its plugin is kept by
	agent := fmt.Sprintf("plugin:stale-mid-read-%d", staleMidReadRuns.Add(1))
	week := time.Now().Add(3 * time.Hour)
	release := make(chan struct{})
	var once sync.Once
	var readings, out atomic.Int32
	provider.LoginUsageVia(func(_ context.Context, ag string) map[string]provider.SubscriptionQuota {
		if ag != agent {
			return nil
		}
		out.Add(1)
		defer out.Add(-1)
		used := 100.0
		switch readings.Add(1) {
		case 1:
			used = 90
		case 2: // out as the request is refused
			<-release
			used = 97
		}
		return map[string]provider.SubscriptionQuota{"me@example.com": {Windows: []provider.QuotaWindow{{Span: 7 * 24 * time.Hour, Used: used, ResetsAt: &week}}}}
	})
	t.Cleanup(func() {
		once.Do(func() { close(release) })
		for out.Load() > 0 {
			time.Sleep(5 * time.Millisecond)
		}
		provider.LoginUsageVia(nil)
	})
	share := func() float64 {
		u, _ := provider.Allowances(agent)["me@example.com"].For("gpt-5.5", time.Now())
		return u
	}
	if u := share(); u != 90 {
		t.Fatalf("first reading %v%%", u)
	}
	provider.StaleAllowance(agent, "me@example.com") // as read over a minute ago
	share()                                          // read again behind the request
	for readings.Load() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	acct := candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: agent, User: "me@example.com"}}, model: "gpt-5.5", rest: agent + "#me@example.com"}
	s := &Server{}
	refused := []byte(`{"error":{"message":"You've hit your usage limit"}}`)
	if r := s.restAfter(acct, 429, http.Header{}, refused); r.By != "quota" {
		t.Fatalf("first refusal, the window not known full: rests by %s", r.By)
	}
	once.Do(func() { close(release) })
	// the routing asks before each request; give the reading after it time
	got, deadline := share(), time.Now().Add(2*time.Second)
	for got != 100 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		got = share()
	}
	r := s.restAfter(acct, 429, http.Header{}, refused)
	if d := time.Until(r.Until); r.By != "window" || d < 2*time.Hour {
		t.Fatalf("second refusal rests %v by %s; the share read %v%%, readings %d", d.Round(time.Minute), r.By, got, readings.Load())
	}
}
