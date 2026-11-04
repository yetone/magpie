package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// ysicing_01858 on Discord, three accounts under Use before expiry: one at
// 90% of its week, renewing today, took few requests, most going to one
// renewing tomorrow. 90% of any window put an account behind every one
// with room, so the week it would lose at its reset went unused. A week at
// 90% alone now keeps its place by when it renews (Smart) or its pace
// (Use before expiry), and new conversations kept for the session may go
// to it; 90% of its five hours, a rate cap, still holds it back, and 98%
// of its week still puts it last. Least used first is unchanged.
func TestWeekLowIsSpentBeforeItsReset(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	now := time.Now()
	share := map[string]provider.Allowance{}
	allowances = func(string) map[string]provider.Allowance { return share }
	acct := func(user string) candidate {
		return candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: user}}, model: "gpt-5.6-sol", rest: "codex#" + user}
	}
	plan := func(fiveHours, week float64, renews time.Duration) provider.Allowance {
		return provider.Allowance{
			{Used: fiveHours, Resets: now.Add(3 * time.Hour), Span: 5 * time.Hour},
			{Used: week, Resets: now.Add(renews), Span: 7 * 24 * time.Hour},
		}
	}
	for _, routing := range []string{provider.Pace, ""} {
		name := map[string]string{provider.Pace: "pace", "": "smart"}[routing]
		p := provider.Provider{ID: "codex", Routing: routing, Account: &provider.Account{Agent: "codex", User: "b@x.com"}}
		order := func() (string, []Weighed) {
			got, wg := weigh(p, []candidate{acct("b@x.com"), acct("a@x.com")}, "gpt-5.6-sol", provider.Chat)
			var s []string
			var ws []Weighed
			for _, c := range got {
				s = append(s, c.p.Account.User[:1])
				ws = append(ws, weighed(c, p, wg, false, provider.Chat))
			}
			return strings.Join(s, ""), ws
		}
		// B has room and renews tomorrow; A is at 91% of its week, which
		// renews in three hours: 9% left in 3h is more per hour than 70%
		// in 48h, and sooner, so A goes first by either routing
		share["b@x.com"] = plan(10, 30, 48*time.Hour)
		share["a@x.com"] = plan(20, 91, 3*time.Hour)
		got, ws := order()
		if got != "ab" {
			t.Fatalf("%s: a week at 91%% renewing in 3h went %s, want ab", name, got)
		}
		if !ws[0].WeekLow || ws[1].WeekLow {
			t.Fatalf("%s: weekLow %v %v, want A only", name, ws[0].WeekLow, ws[1].WeekLow)
		}
		// a new conversation kept for the session may go to A: Smart and
		// Use before expiry would put one there
		if !readyForNew([]Weighed{ws[1], ws[0]}, 1) {
			t.Fatalf("%s: A at 91%% of its week alone isn't ready for a new conversation", name)
		}
		// at 92% of its five hours it is near a rate cap: kept for last
		share["a@x.com"] = plan(92, 91, 3*time.Hour)
		if got, ws = order(); got != "ba" || ws[1].WeekLow {
			t.Fatalf("%s: five hours at 92%%: %s weekLow %v, want ba, false", name, got, ws[1].WeekLow)
		}
		if readyForNew(ws, 1) {
			t.Fatalf("%s: A at 92%% of its five hours is ready for a new conversation", name)
		}
		// all but used up: last, whatever its reset
		share["a@x.com"] = plan(20, 98, 3*time.Hour)
		if got, ws = order(); got != "ba" || ws[1].WeekLow {
			t.Fatalf("%s: week at 98%%: %s weekLow %v, want ba, false", name, got, ws[1].WeekLow)
		}
	}
	// Least used first goes by the share used: A at 91% goes after B, and
	// the trace doesn't call it kept with those with room
	p := provider.Provider{ID: "codex", Routing: provider.LeastUsed, Account: &provider.Account{Agent: "codex", User: "b@x.com"}}
	share["a@x.com"] = plan(20, 91, 3*time.Hour)
	got, wg := weigh(p, []candidate{acct("a@x.com"), acct("b@x.com")}, "gpt-5.6-sol", provider.Chat)
	if got[0].p.Account.User != "b@x.com" {
		t.Fatalf("least used: %s first, want b", got[0].p.Account.User)
	}
	if w := weighed(got[1], p, wg, false, provider.Chat); w.WeekLow {
		t.Fatalf("least used: A told weekLow")
	}
}
