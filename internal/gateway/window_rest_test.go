package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A subscription that fails with a window full rests until the window
// renews, but no longer than one out of quota would: a reset read a year
// off (Claude Code's /usage date with no year, taken for next year) once
// benched the account for that year.
func TestRoutingHoldsAWindowRestToLongestQuota(t *testing.T) {
	forgetRouting()
	old := allowances
	defer func() { allowances = old }()
	now := time.Now()
	allowances = func(string) map[string]provider.Allowance {
		return map[string]provider.Allowance{
			"far@x.com":  {{Used: 100, Resets: now.AddDate(1, 0, 0), Span: 7 * 24 * time.Hour}},
			"near@x.com": {{Used: 100, Resets: now.Add(3 * time.Hour), Span: 5 * time.Hour}},
		}
	}
	acct := func(user string) candidate {
		return candidate{p: provider.Provider{ID: "claude", Account: &provider.Account{Agent: "claude", User: user}}, model: "claude-sonnet-4-5", rest: "claude#" + user}
	}
	s := &Server{}
	defer s.Unrest(acct("far@x.com").restKey())
	defer s.Unrest(acct("near@x.com").restKey())
	near := func(d, want time.Duration) bool { return d > want-time.Minute && d <= want }

	r := s.restAfter(acct("far@x.com"), 502, http.Header{}, []byte("bad gateway"))
	if r.Why != failOther || r.By != "window" || !near(time.Until(r.Until), longestQuota) {
		t.Fatalf("window read a year off: rests %v by %s (%s), want at most %v", time.Until(r.Until), r.By, r.Why, longestQuota)
	}
	r = s.restAfter(acct("near@x.com"), 502, http.Header{}, []byte("bad gateway"))
	if r.Why != failOther || r.By != "window" || !near(time.Until(r.Until), 3*time.Hour) {
		t.Fatalf("window renewing in 3h: rests %v by %s (%s)", time.Until(r.Until), r.By, r.Why)
	}
}
