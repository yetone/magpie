package gateway

import (
	"net/http"
	"strconv"
	"strings"
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

// A failure rested by a full window rests the model when every full
// window that counts it counts some models only: the account's other
// models are still in theirs (plugins#817, zane668). ZCode's Start Plan
// gift gives GLM-5.3 and GLM-5.3-Flash a bucket each; GLM-5.3's spent,
// its 200 "exceed quota limit" reached magpie as a 502 that did not
// stream (failOther) and rested the whole account until the bucket
// renewed, Flash's own bucket with 71% left with it. A window of the
// whole account full still rests the account.
func TestScopedWindowFullRestsTheModelOnly(t *testing.T) {
	now := time.Now()
	reset := now.Add(22 * time.Hour).UTC().Format(time.RFC3339)
	weekend := now.Add(3 * 24 * time.Hour).UTC().Format(time.RFC3339)
	// the zcode plugin's startUsage as the report's `magpie quota zcode`
	// read it: each bucket lists its model and the model's -Trial entry
	gift := `{"name":"GLM-5.3","used":100,"amount":3000000,"limit":3000000,"span":86400,"resetsAt":"` + reset + `","models":["glm-5.3","GLM-5.3-Trial"]},` +
		`{"name":"GLM-5.3-Flash","used":29,"amount":1459132,"limit":5000000,"span":86400,"resetsAt":"` + reset + `","models":["glm-5.3-flash","GLM-5.3-Flash-Trial"]},` +
		`{"name":"GLM-5.3-Flash · ZCODE WEEKEND BUILD","used":0,"span":259200,"resetsAt":"` + weekend + `","models":["glm-5.3-flash","GLM-5.3-Flash-Trial"]}`
	// a coding plan's own window counts every model
	plan := func(used int) string {
		return `{"name":"5 hours","used":` + strconv.Itoa(used) + `,"span":18000,"resetsAt":"` + reset + `"}`
	}
	usage := func(ws ...string) provider.Allowance {
		return provider.PluginAllowanceForTest([]byte(`{"windows":[`+strings.Join(ws, ",")+`]}`), now)
	}
	stalled := []byte(`{"type":"error","error":{"type":"api_error","message":"ZCode did not stream: exceed quota limit"}}`)
	spent := []byte(`{"code":1005,"msg":"exceed quota limit"}`)

	for _, c := range []struct {
		name   string
		al     provider.Allowance
		status int
		body   []byte
		why    string
		model  bool // the model rests, not the account
	}{
		{"gift bucket full, did not stream", usage(gift), 502, stalled, failOther, true},
		{"gift bucket full, said 429", usage(gift), 429, spent, failQuota, true},
		{"gift bucket and the plan full", usage(plan(100), gift), 502, stalled, failOther, false},
		{"the plan full alone", usage(plan(100)), 502, stalled, failOther, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			old := allowances
			defer func() { allowances = old }()
			fresh(t)
			allowances = func(string) map[string]provider.Allowance {
				return map[string]provider.Allowance{"me@zcode": c.al}
			}
			acct := func(model string) candidate {
				return candidate{p: provider.Provider{ID: "zcode", Account: &provider.Account{Agent: "plugin:zcode", User: "me@zcode"}}, model: model, rest: "zcode"}
			}
			glm, flash := acct("GLM-5.3"), acct("GLM-5.3-Flash")
			s := &Server{}
			r := s.restAfter(glm, c.status, http.Header{}, c.body)
			if r.Why != c.why {
				t.Fatalf("GLM-5.3 failed %s, want %s", r.Why, c.why)
			}
			if d := time.Until(r.Until); d < 21*time.Hour || d > 22*time.Hour {
				t.Fatalf("GLM-5.3 rests %v by %s, want till its window renews", d, r.By)
			}
			_, flashRests := restingOf(flash)
			if c.model {
				if r.Key != glm.restID() || r.Model != glm.model {
					t.Fatalf("GLM-5.3's own bucket full rests %s (model %q), want the model alone", r.Key, r.Model)
				}
				if flashRests {
					t.Fatal("GLM-5.3's bucket full rests GLM-5.3-Flash, which has its own")
				}
				return
			}
			if r.Key != glm.restKey() || r.Model != "" {
				t.Fatalf("the account's window full rests %s (model %q), want the account", r.Key, r.Model)
			}
			if !flashRests {
				t.Fatal("the account's window full leaves GLM-5.3-Flash in")
			}
		})
	}
}
