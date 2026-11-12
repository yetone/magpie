package gateway

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A Claude account's Fable week used up rests Fable on it, not the account
// (kaitokiku on X: the account hit its Fable limit with the other weekly
// quota left, and every model 429'd on it). Claude Code's refusal says
// only "You've hit your limit", and its rate_limit_info may not say which
// window (rejected, no rateLimitType: the transcript's own row), so the
// reading held before the refusal still has Fable at 60%. The account
// rests at first; once its windows are read again with the Fable week
// full, the rest is Fable's alone and Opus routes there again.
func TestClaudeFableWeekRestsFableOnly(t *testing.T) {
	old, oldAwait := allowances, awaitAllowance
	defer func() { allowances, awaitAllowance = old, oldAwait }()
	defer narrowing.Wait()
	fresh(t)
	var mu sync.Mutex
	read := map[string]provider.Allowance{}
	allowances = func(string) map[string]provider.Allowance {
		mu.Lock()
		defer mu.Unlock()
		return read
	}
	const user = "fable@example.com"
	five, week := time.Now().Add(2*time.Hour).Unix(), time.Now().Add(4*24*time.Hour).Unix()
	note := func(fable float64) {
		mu.Lock()
		defer mu.Unlock()
		read = map[string]provider.Allowance{user: provider.ClaudeAllowanceForTest([]provider.ClaudeLimit{
			{Kind: "five_hour", Used: 0.2, ResetsAt: five},
			{Kind: "seven_day", Used: 0.4, ResetsAt: week},
			{Kind: "seven_day_overage_included", Used: fable, ResetsAt: week},
		}, time.Now())}
	}
	note(0.6)
	awaitAllowance = func(_ context.Context, agent, u string) bool {
		note(1)
		return true
	}
	acct := func(model string) candidate {
		return candidate{p: provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: user}}, model: model, rest: "claude"}
	}
	s := &Server{}
	fable := acct("claude-fable-5-1")
	r := s.restAfter(fable, http.StatusTooManyRequests, http.Header{}, []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"You've hit your limit · resets 5:58am"}}`))
	if r.Why != failQuota {
		t.Fatalf("rest: %+v", r)
	}
	narrowing.Wait()
	if got, ok := restingOf(fable); !ok || got.Key != fable.restID() || got.Model != "claude-fable-5-1" {
		t.Fatalf("Fable after the reading: %+v %v, want its own rest", got, ok)
	}
	for _, m := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-4-5"} {
		if _, ok := restingOf(acct(m)); ok {
			t.Fatalf("%s rests on an account whose Fable week is used up", m)
		}
	}
}
