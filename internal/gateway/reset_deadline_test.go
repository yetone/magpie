package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A Codex account that spends a reset about to run out by itself (auto-use
// on, its windows used) loses what its week has left when that reset is
// spent, resetExpiryLead (half an hour) before it runs out: Weekly pace and Smart take
// that for its deadline when it is sooner than the week renews (#717,
// #718, thedavidweng: A with 80% left renewing in five days and a reset
// running out that afternoon went behind B with 50% left renewing in two).
// Holding a reset that runs out after the week renews, or none, changes
// nothing; the 90% and 98% tiers are kept; the trace says when the reset
// set it.
func TestResetSpentIsTheDeadline(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	now := time.Now()
	share := map[string]provider.Allowance{}
	allowances = func(string) map[string]provider.Allowance { return share }
	acct := func(user string) candidate {
		return candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: user}}, model: "gpt-5.6-sol", rest: "codex#" + user}
	}
	// runsOut zero: no reset the account spends by itself — none held,
	// or auto-use off (provider.resetRunsOut leaves it out then)
	week := func(used float64, renews, runsOut time.Duration) provider.Allowance {
		a := provider.Allowance{
			{Used: 10, Resets: now.Add(3 * time.Hour), Span: 5 * time.Hour},
			{Used: used, Resets: now.Add(renews), Span: 7 * 24 * time.Hour},
		}
		if runsOut != 0 {
			for i := range a {
				a[i].ResetRunsOut = now.Add(runsOut)
			}
		}
		return a
	}
	for _, routing := range []string{provider.Pace, ""} {
		p := provider.Provider{ID: "codex", Routing: routing, Account: &provider.Account{Agent: "codex", User: "a@x.com"}}
		order := func() (string, weighing, []candidate) {
			got, wg := weigh(p, []candidate{acct("b@x.com"), acct("a@x.com")}, "gpt-5.6-sol", provider.Chat)
			var s []string
			for _, c := range got {
				s = append(s, c.p.Account.User[:1])
			}
			return strings.Join(s, ""), wg, got
		}
		name := map[string]string{provider.Pace: "pace", "": "smart"}[routing]
		share["b@x.com"] = week(50, 48*time.Hour, 0)

		// no reset: B, renewing sooner, as before
		share["a@x.com"] = week(20, 120*time.Hour, 0)
		if got, _, _ := order(); got != "ba" {
			t.Fatalf("%s, no reset: %s", name, got)
		}
		// the issue's case: A's reset runs out in an hour and a half, spent
		// in one, its 80% lost
		share["a@x.com"] = week(20, 120*time.Hour, 90*time.Minute)
		got, wg, cs := order()
		if got != "ab" {
			t.Fatalf("%s, reset spent before the week renews: %s", name, got)
		}
		w := weighed(cs[0], p, wg, false, provider.Chat)
		if routing == provider.Pace && (w.DueBy != "reset" || w.Due == nil || !w.Due.Equal(now.Add(time.Hour))) {
			t.Fatalf("pace trace: due %v by %q", w.Due, w.DueBy)
		}
		if w.Restarts == nil {
			t.Fatalf("%s trace: restarts not told", name)
		}
		if b := weighed(cs[1], p, wg, false, provider.Chat); b.DueBy != "" || b.Restarts != nil {
			t.Fatalf("%s trace: B by %q, restarts %v", name, b.DueBy, b.Restarts)
		}
		// the reset runs out after the week renews: the week decides
		share["a@x.com"] = week(20, 120*time.Hour, 6*24*time.Hour)
		if got, wg, cs = order(); got != "ba" {
			t.Fatalf("%s, week sooner than the reset: %s", name, got)
		}
		if w := weighed(cs[1], p, wg, false, provider.Chat); w.DueBy != "" || w.Restarts != nil {
			t.Fatalf("%s trace, week sooner: by %q, restarts %v", name, w.DueBy, w.Restarts)
		}
		// A sooner than the week renews, but at 90% of its five hours:
		// it waits behind B all the same
		share["a@x.com"] = week(20, 120*time.Hour, 90*time.Minute)
		share["a@x.com"][0].Used = 92
		if got, _, _ := order(); got != "ba" {
			t.Fatalf("%s, low tier kept: %s", name, got)
		}
		// all but used up: last, whatever its reset
		share["a@x.com"] = week(99, 120*time.Hour, 90*time.Minute)
		if got, _, _ := order(); got != "ba" {
			t.Fatalf("%s, spent tier kept: %s", name, got)
		}
		// both spend one: the sooner first
		share["a@x.com"] = week(20, 120*time.Hour, 30*time.Hour)
		share["b@x.com"] = week(50, 120*time.Hour, 10*time.Hour)
		if got, _, _ := order(); got != "ba" {
			t.Fatalf("%s, both spend one, B sooner: %s", name, got)
		}
	}
}
