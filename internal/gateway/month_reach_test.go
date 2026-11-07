package gateway

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// In a group routed Smart, a Volcengine Agent Plan key (5 hours of 2,000,
// a week of 7,000, a month of 20,000) stands behind a Codex account whose
// week renews in three days while its month can still be used up — and
// goes first once the weeks left before the month renews can't carry
// what the month has left, as what its week leaves is lost at its reset
// then (#5639, louisgreen0726's numbers). The Routing page is told which
// window ranks it.
func TestSmartSpendsAWeekTheMonthCantWait(t *testing.T) {
	now := time.Now()
	old := allowances
	t.Cleanup(func() { allowances = old })
	allowances = func(string) map[string]provider.Allowance {
		return map[string]provider.Allowance{"me@example.com": {{Used: 10, Resets: now.Add(3 * 24 * time.Hour), Span: 7 * 24 * time.Hour}}}
	}
	weekRenews := now.Add(23*time.Hour + 36*time.Minute)
	agentPlan := func(weekUsed, monthUsed float64, month time.Time) provider.Allowance {
		return provider.Allowance{
			{Used: 0, Span: 5 * time.Hour, Of: 2000, Name: "5 hours"},
			{Used: weekUsed / 70, Amount: weekUsed, Of: 7000, Resets: weekRenews, Span: 7 * 24 * time.Hour, Name: "Weekly"},
			{Used: monthUsed / 200, Amount: monthUsed, Of: 20000, Resets: month, Span: 30 * 24 * time.Hour, Name: "Month"},
		}
	}
	of := map[string]provider.Allowance{}
	keyWindows(t, of)
	codex := provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: "me@example.com"}}
	ark := provider.Provider{ID: "volcengine", Key: "ark-key", Anthropic: "https://ark.cn-beijing.volces.com/api/plan"}
	heads := []candidate{
		{p: codex, model: "gpt-6-astra", rest: "codex"},
		{p: ark, model: "doubao-seed-2.0-pro", rest: "volcengine"},
	}
	g := provider.Provider{ID: provider.GroupPrefix + "g"}

	// as reported: 18,372 of the month left, four weeks to carry it
	of["ark-key"] = agentPlan(1628, 1628, now.Add(696*time.Hour))
	got, wg := weigh(g, heads, "", provider.Responses)
	if restsOf(got) != "codex volcengine " {
		t.Fatalf("the month within reach: %s", restsOf(got))
	}
	if w := weighed(got[1], ark, wg, false, provider.Responses); w.Unreached != 0 {
		t.Fatalf("the month within reach, traced: %+v", w)
	}

	// two weeks on, the plan not used: two weeks hold 14,000 of 18,372
	of["ark-key"] = agentPlan(0, 1628, weekRenews.Add(2*7*24*time.Hour+24*time.Minute))
	got, wg = weigh(g, heads, "", provider.Responses)
	if restsOf(got) != "volcengine codex " {
		t.Fatalf("the month out of reach: %s", restsOf(got))
	}
	if w := weighed(got[0], ark, wg, false, provider.Responses); w.Unreached != 1 || len(w.Renews) != 3 {
		t.Fatalf("the month out of reach, traced: %+v", w)
	}

	// a month holding less than its weeks add up to, with 9,000 left
	// that two weeks carry: the month ranks it, as before (#1778)
	of["ark-key"] = agentPlan(0, 11000, weekRenews.Add(2*7*24*time.Hour))
	if got, _ := weigh(g, heads, "", provider.Responses); restsOf(got) != "codex volcengine " {
		t.Fatalf("the month the real cap: %s", restsOf(got))
	}
}
