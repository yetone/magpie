package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// cdredfox on X: in a Smart group of WorkBuddy accounts, the one whose
// credits left run out first goes first, since what it has left is lost
// then; the Credits window has no reset, so before they all ranked alike
// and kept their order. Its share used still decides first: one all but
// spent waits behind, whatever its credits' end.
func TestSmartSpendsWorkBuddyCreditsRunningOutFirst(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	now := time.Now()
	share := map[string]provider.Allowance{}
	allowances = func(string) map[string]provider.Allowance { return share }
	p := provider.Provider{ID: "workbuddy-ai", Account: &provider.Account{Agent: "workbuddy-ai", User: "a@x.com"}}
	acct := func(user string) candidate {
		return candidate{p: provider.Provider{ID: "workbuddy-ai", Account: &provider.Account{Agent: "workbuddy-ai", User: user}}, model: "hy3", rest: "workbuddy-ai#" + user}
	}
	credits := func(used float64, in time.Duration) provider.Allowance {
		l := provider.Limit{Used: used, Amount: used * 22.5, Of: 2250, Unit: "credits"}
		if in > 0 {
			l.RunsOut = now.Add(in)
		}
		return provider.Allowance{l}
	}
	order := func(cs ...candidate) string {
		got, _ := weigh(p, cs, "hy3", provider.Chat)
		var s []string
		for _, c := range got {
			s = append(s, c.p.Account.User[:1])
		}
		return strings.Join(s, "")
	}

	share["a@x.com"] = credits(10, 18*24*time.Hour)
	share["b@x.com"] = credits(30, 30*time.Hour)
	if got := order(acct("a@x.com"), acct("b@x.com")); got != "ba" {
		t.Fatalf("credits ending sooner first: %s", got)
	}
	// one not known (an older plugin) after those known
	share["a@x.com"] = credits(10, 0)
	if got := order(acct("a@x.com"), acct("b@x.com")); got != "ba" {
		t.Fatalf("not known last: %s", got)
	}
	// ending the same hour: their order, for the cache
	hour := now.Truncate(time.Hour).Add(31 * time.Hour).Sub(now)
	share["a@x.com"] = credits(10, hour+40*time.Minute)
	share["b@x.com"] = credits(30, hour+10*time.Minute)
	if got := order(acct("a@x.com"), acct("b@x.com")); got != "ab" {
		t.Fatalf("same hour keeps order: %s", got)
	}
	// all but spent: behind, though its credits end first
	share["a@x.com"] = credits(10, 18*24*time.Hour)
	share["b@x.com"] = credits(99, 30*time.Hour)
	if got := order(acct("b@x.com"), acct("a@x.com")); got != "ab" {
		t.Fatalf("spent last: %s", got)
	}
}

// zhaocai_83020 on Discord: several WorkBuddy accounts, and the credits
// that expire soonest are to be spent first. A free account's Credits
// window resets at the month's end, the same moment on every account
// (plugin 0.1.35's cycleOf), and the credits it was given lapse on their
// own days (runsOutAt). Those lapsing after the month's end ranked alike
// and kept their order, so the account whose credits were lost first was
// not spent first. Renewing together, the one lapsing first goes first.
// The windows are the plugin's for a real China-site free account
// (packages/workbuddy usage.test.mjs, CN_FREE): reset 10-31 23:59:59 +08
// over 31 days, given credits lapsing on their own day.
func TestSmartSpendsCreditsLapsingFirstWhenRenewingTogether(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	now := time.Now()
	share := map[string]provider.Allowance{}
	allowances = func(string) map[string]provider.Allowance { return share }
	p := provider.Provider{ID: "workbuddy", Account: &provider.Account{Agent: "workbuddy", User: "a"}}
	acct := func(user string) candidate {
		return candidate{p: provider.Provider{ID: "workbuddy", Account: &provider.Account{Agent: "workbuddy", User: user}}, model: "hy3", rest: "workbuddy#" + user}
	}
	month := now.Truncate(time.Hour).Add(4*24*time.Hour + 59*time.Minute)
	credits := func(used float64, lapses time.Duration) provider.Allowance {
		l := provider.Limit{Used: used, Amount: used * 34, Of: 3400, Unit: "credits", Resets: month, Span: 31*24*time.Hour - time.Second}
		if lapses > 0 {
			l.RunsOut = month.Add(lapses)
		}
		return provider.Allowance{l}
	}
	order := func(cs ...candidate) string {
		got, _ := weigh(p, cs, "hy3", provider.Chat)
		var s []string
		for _, c := range got {
			s = append(s, c.p.Account.User)
		}
		return strings.Join(s, "")
	}

	// both renew at the month's end; b's given credits lapse 5 days
	// after it, a's 50: b first, though listed second and more used
	share["a"] = credits(10, 50*24*time.Hour)
	share["b"] = credits(30, 5*24*time.Hour)
	if got := order(acct("a"), acct("b")); got != "ba" {
		t.Fatalf("lapsing first goes first: %s", got)
	}
	// one whose credits never lapse (none given, or an older plugin)
	// keeps them: after one whose do
	share["a"] = credits(10, 0)
	if got := order(acct("a"), acct("b")); got != "ba" {
		t.Fatalf("not lapsing last: %s", got)
	}
	// lapsing within the same hour: their order, for the cache
	share["a"] = credits(10, 5*24*time.Hour)
	if got := order(acct("a"), acct("b")); got != "ab" {
		t.Fatalf("same hour keeps order: %s", got)
	}
	// the renewal still decides first: one renewing sooner goes ahead of
	// one lapsing sooner
	share["a"] = credits(10, 50*24*time.Hour)
	share["a"][0].Resets = month.Add(-48 * time.Hour)
	if got := order(acct("b"), acct("a")); got != "ab" {
		t.Fatalf("renewal first: %s", got)
	}
	// and the share used before both: all but spent waits behind
	share["a"] = credits(99, 5*24*time.Hour)
	share["b"] = credits(10, 50*24*time.Hour)
	if got := order(acct("a"), acct("b")); got != "ba" {
		t.Fatalf("spent last: %s", got)
	}
}

// kudi3699 on X: a WorkBuddy account holds a 5000-credit pack that ends
// next March, half used, and check-in credits that end tomorrow. Its
// share used over the whole pool (about 50%) kept it behind a fresher
// account in "Least used first" and the check-in credits were lost. What
// lapses soon unspent is spent first.
func TestLeastUsedSpendsWorkBuddyCheckInCreditsAboutToLapse(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	now := time.Now()
	share := map[string]provider.Allowance{}
	allowances = func(string) map[string]provider.Allowance { return share }
	p := provider.Provider{ID: "workbuddy", Routing: provider.LeastUsed, Account: &provider.Account{Agent: "workbuddy", User: "a"}}
	acct := func(user string) candidate {
		return candidate{p: provider.Provider{ID: "workbuddy", Account: &provider.Account{Agent: "workbuddy", User: user}}, model: "hy3", rest: "workbuddy#" + user}
	}
	order := func(cs ...candidate) string {
		got, _ := weigh(p, cs, "hy3", provider.Chat)
		var s []string
		for _, c := range got {
			s = append(s, c.p.Account.User)
		}
		return strings.Join(s, "")
	}
	// 5500 in all, 2750 used (50%); 500 of it lapses in a day
	share["old"] = provider.Allowance{{Used: 50, Amount: 2750, Of: 5500, Unit: "credits", RunsOut: now.Add(24 * time.Hour), RunsOutLeft: 500}}
	share["new"] = provider.Allowance{{Used: 20, Amount: 1100, Of: 5500, Unit: "credits", RunsOut: now.Add(200 * 24 * time.Hour), RunsOutLeft: 4400}}
	if got := order(acct("new"), acct("old")); got != "oldnew" {
		t.Fatalf("credits lapsing tomorrow not spent first: %s", got)
	}
	// nothing about to lapse: the least used, as before
	share["old"] = provider.Allowance{{Used: 50, Amount: 2750, Of: 5500, Unit: "credits", RunsOut: now.Add(200 * 24 * time.Hour), RunsOutLeft: 2750}}
	if got := order(acct("old"), acct("new")); got != "newold" {
		t.Fatalf("least used first as before: %s", got)
	}
}

// kudi3699 on X, again: the check-in reward that lapses is a few dozen
// credits (20 of 5100, six days out), not 500 tomorrow. It was faded to
// under the threshold, so the half-used account was still never picked.
func TestLeastUsedSpendsSmallCheckInRewardSixDaysOut(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	now := time.Now()
	share := map[string]provider.Allowance{}
	allowances = func(string) map[string]provider.Allowance { return share }
	p := provider.Provider{ID: "workbuddy", Routing: provider.LeastUsed, Account: &provider.Account{Agent: "workbuddy", User: "a"}}
	acct := func(user string) candidate {
		return candidate{p: provider.Provider{ID: "workbuddy", Account: &provider.Account{Agent: "workbuddy", User: user}}, model: "hy3", rest: "workbuddy#" + user}
	}
	share["old"] = provider.Allowance{{Used: 50, Amount: 2550, Of: 5100, Unit: "credits", RunsOut: now.Add(6 * 24 * time.Hour), RunsOutLeft: 20}}
	share["new"] = provider.Allowance{{Used: 20, Amount: 1020, Of: 5100, Unit: "credits", RunsOut: now.Add(200 * 24 * time.Hour), RunsOutLeft: 4080}}
	got, _ := weigh(p, []candidate{acct("new"), acct("old")}, "hy3", provider.Chat)
	if got[0].p.Account.User != "old" {
		t.Fatalf("a small reward ending in 6 days not spent first: %s", got[0].p.Account.User)
	}
}
