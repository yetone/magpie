package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// wowbbs on Discord (shamen_xiaohong too): a group of check-in accounts —
// WorkBuddy CN twice, MiniMax, Trae CN, Qoder CN — each given free quota
// that is lost on its own day, beside a paid one. Routed "expiring", the
// group spends the free quota lost soonest first, goes on to the next free
// one once one is used up, and reaches the paid one only when no free one
// has quota left. The Routing page is told which are free and when each
// one's quota is lost.
func TestGroupSpendsFreeQuotaSoonestToExpireFirst(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	now := time.Now()
	share := map[string]provider.Allowance{}
	allowances = func(string) map[string]provider.Allowance { return share }
	window := func(used float64, resets time.Duration) provider.Allowance {
		return provider.Allowance{{Name: "Credits", Used: used, Resets: now.Add(resets), Span: 30 * 24 * time.Hour}}
	}
	share["pro"] = window(10, 2*time.Hour) // renews soonest: Smart's first
	share["wb1"] = window(30, 20*24*time.Hour)
	share["wb2"] = window(30, 3*24*time.Hour)
	share["mm"] = provider.Allowance{{Name: "Credits", Used: 20, RunsOut: now.Add(26 * time.Hour)}}
	share["qoder"] = window(50, 9*24*time.Hour)
	acct := func(agent, user, plan string) candidate {
		return candidate{p: provider.Provider{ID: agent, Account: &provider.Account{Agent: agent, User: user, Plan: plan}}, model: "m", rest: agent + "#" + user}
	}
	g := provider.Provider{ID: provider.GroupPrefix + "checkins", Routing: "expiring"}
	order := func() (string, []candidate, weighing) {
		cs := []candidate{acct("paid", "pro", "Pro"), acct("workbuddy", "wb1", "Free plan"), acct("workbuddy", "wb2", "Free plan"),
			acct("minimax-code", "mm", "Free"), acct("qoder-cn", "qoder", "Free")}
		got, wg := weigh(g, cs, "m", provider.Chat)
		var s []string
		for _, c := range got {
			s = append(s, c.p.Account.User)
		}
		return strings.Join(s, ","), got, wg
	}
	got, cs, wg := order()
	if want := "mm,wb2,qoder,wb1,pro"; got != want {
		t.Fatalf("free by expiry: %s, want %s", got, want)
	}
	b, _ := json.Marshal(weighed(cs[0], g, wg, false, provider.Chat))
	var row map[string]any
	_ = json.Unmarshal(b, &row)
	if row["free"] != true || row["expires"] == nil {
		t.Errorf("the Routing page's row for MiniMax: %s", b)
	}
	b, _ = json.Marshal(weighed(cs[4], g, wg, false, provider.Chat))
	row = nil
	_ = json.Unmarshal(b, &row)
	if row["free"] == true || row["expires"] != nil {
		t.Errorf("the paid one's row: %s", b)
	}

	// the soonest used up, the next goes first; every free one used up,
	// the paid one
	share["mm"] = provider.Allowance{{Name: "Credits", Used: 100, RunsOut: now.Add(26 * time.Hour)}}
	if got, _, _ := order(); !strings.HasPrefix(got, "wb2,qoder,wb1,pro,") {
		t.Errorf("MiniMax used up: %s", got)
	}
	for _, u := range []string{"wb1", "wb2", "qoder"} {
		share[u] = window(100, 24*time.Hour)
	}
	if got, _, _ := order(); !strings.HasPrefix(got, "pro,") {
		t.Errorf("every free one used up: %s, want the paid one first", got)
	}
}
