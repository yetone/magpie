package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Use before expiry (Pace) takes credits that expire before their window
// renews for that window's deadline: what expires, as a share of the
// window, per hour until it does. lcyksp on Discord: a WorkBuddy month of
// 500 credits renewing in 28 days, 70.85 check-in credits in it expiring
// tomorrow morning, was billed after an account whose month had more to
// lose per hour, so the check-in credits lapsed unspent. Credits that
// expire after the window renews change nothing, nor do a few credits
// expiring when the month has more to lose; when the amount isn't told,
// all that is left is taken as lost then, as Smart's Renewal takes it.
// The trace says the deadline is the expiry.
func TestPaceGoesByCreditsExpiring(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	now := time.Now()
	share := map[string]provider.Allowance{}
	allowances = func(string) map[string]provider.Allowance { return share }
	const model = "deepseek-v4.1-flash"
	p := provider.Provider{ID: "workbuddy", Routing: provider.Pace, Account: &provider.Account{Agent: "workbuddy", User: "a@x.com"}}
	acct := func(user string) candidate {
		return candidate{p: provider.Provider{ID: "workbuddy", Account: &provider.Account{Agent: "workbuddy", User: user}}, model: model, rest: "workbuddy#" + user}
	}
	month := func(used float64, renews time.Duration) provider.Allowance {
		return provider.Allowance{{Used: used, Amount: 500 * used / 100, Of: 500, Unit: "credits", Resets: now.Add(renews), Name: "Credits"}}
	}
	order := func() (string, weighing, []candidate) {
		got, wg := weigh(p, []candidate{acct("b@x.com"), acct("a@x.com")}, model, provider.Chat)
		var s []string
		for _, c := range got {
			s = append(s, c.p.Account.User[:1])
		}
		return strings.Join(s, ""), wg, got
	}
	// B: half its month left, renewing in ten days: 50/240h ≈ 0.21 an
	// hour, ahead of A's 100/672h ≈ 0.15 when nothing of A's expires
	share["b@x.com"] = month(50, 10*24*time.Hour)
	share["a@x.com"] = month(0, 28*24*time.Hour)
	if got, _, _ := order(); got != "ba" {
		t.Fatalf("nothing expiring: %s", got)
	}

	// the report: 70.85 of A's credits expire in 14 hours, 14% of its
	// 500 lost then, 1% an hour
	share["a@x.com"][0].RunsOut, share["a@x.com"][0].RunsOutLeft = now.Add(14*time.Hour), 70.85
	got, wg, cs := order()
	if got != "ab" {
		t.Fatalf("credits expiring tomorrow go first: %s", got)
	}
	w := weighed(cs[0], p, wg, false, provider.Chat)
	if w.DueBy != "expiry" || w.Due == nil || !w.Due.Equal(now.Add(14*time.Hour)) {
		t.Fatalf("trace: due %v by %q", w.Due, w.DueBy)
	}
	if b := weighed(cs[1], p, wg, false, provider.Chat); b.DueBy != "" {
		t.Fatalf("trace: B by %q", b.DueBy)
	}

	// the amount not told: all A has left is taken as lost then
	share["a@x.com"][0].RunsOutLeft = 0
	if got, _, _ := order(); got != "ab" {
		t.Fatalf("amount not told: %s", got)
	}

	// one credit expiring: 0.2% over 14h is less than A's month per hour,
	// so its month decides, and B goes first
	share["a@x.com"][0].RunsOutLeft = 1
	if got, wg, cs = order(); got != "ba" {
		t.Fatalf("a credit expiring: %s", got)
	}
	if w := weighed(cs[1], p, wg, false, provider.Chat); w.DueBy != "" {
		t.Fatalf("trace, month decides: by %q", w.DueBy)
	}

	// expiring after the month renews: lost to the reset first, nothing
	// changes
	share["a@x.com"][0].RunsOut, share["a@x.com"][0].RunsOutLeft = now.Add(30*24*time.Hour), 70.85
	if got, _, _ := order(); got != "ba" {
		t.Fatalf("expiring after the renewal: %s", got)
	}

	// expired already: nothing changes
	share["a@x.com"][0].RunsOut = now.Add(-time.Hour)
	if got, _, _ := order(); got != "ba" {
		t.Fatalf("expired already: %s", got)
	}
}
