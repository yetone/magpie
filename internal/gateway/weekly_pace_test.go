package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Weekly pace puts first the account with the most of its week left per
// hour until it renews — the one with the most to lose at its reset — not
// the one with the most left: 20% left and an hour to go beats 60% left
// and five days. Alike within a tenth go by the tokens sent lately, then
// keep their order; one at 90% or more of any window waits behind the
// rest, one all but used up behind those, each by how far; one not known
// counts as a fresh week, as does one whose vendor tells the five hours
// alone, with their share used; one that tells as it answers goes first.
//
// PIN (2026-10-01): weekly pace is its own option, "pace"; Least used
// keeps its most-left-first meaning and Smart is left as it is (yetone,
// PR #517 review).
func TestWeeklyPace(t *testing.T) {
	old := allowances
	defer func() { allowances = old }()
	now := time.Now()
	share := map[string]provider.Allowance{}
	allowances = func(string) map[string]provider.Allowance { return share }
	p := provider.Provider{ID: "codex", Routing: provider.Pace, Account: &provider.Account{Agent: "codex", User: "a@x.com"}}
	acct := func(user string) candidate {
		return candidate{p: provider.Provider{ID: "codex", Account: &provider.Account{Agent: "codex", User: user}}, model: "gpt-5.6-sol", rest: "codex#" + user}
	}
	week := func(used float64, in time.Duration) provider.Allowance {
		return provider.Allowance{
			{Used: 10, Resets: now.Add(time.Hour), Span: 5 * time.Hour},
			{Used: used, Resets: now.Add(in), Span: 7 * 24 * time.Hour},
		}
	}
	order := func(cs ...candidate) string {
		got, _ := weigh(p, cs, "gpt-5.6-sol", provider.Chat)
		var s []string
		for _, c := range got {
			s = append(s, c.p.Account.User[:1])
		}
		return strings.Join(s, "")
	}

	// the owner's case: 80% used renewing in an hour has the most to lose
	share["a@x.com"] = week(40, 5*24*time.Hour) // 60 left / 120 h = 0.5
	share["b@x.com"] = week(80, time.Hour)      // 20 left / 1 h = 20
	if got := order(acct("a@x.com"), acct("b@x.com")); got != "ba" {
		t.Fatalf("imminent renewal first: %s", got)
	}
	// renewing the same hour, pace is most left first
	share["a@x.com"] = week(30, 76*time.Hour)
	share["b@x.com"] = week(70, 76*time.Hour)
	if got := order(acct("b@x.com"), acct("a@x.com")); got != "ab" {
		t.Fatalf("same deadline, most left first: %s", got)
	}
	// within a tenth alike: their order, for the cache
	share["a@x.com"] = week(50, 76*time.Hour)
	share["b@x.com"] = week(54, 76*time.Hour)
	if got := order(acct("b@x.com"), acct("a@x.com")); got != "ba" {
		t.Fatalf("alike keep their order: %s", got)
	}
	// all but used up: last, though its pace is the highest
	share["a@x.com"] = week(40, 5*24*time.Hour)
	share["b@x.com"] = week(98, time.Hour) // 2 left / 1 h = 2 > 0.5
	if got := order(acct("b@x.com"), acct("a@x.com")); got != "ab" {
		t.Fatalf("spent last: %s", got)
	}
	// its five hours at 90%, its week healthy and renewing soon: behind
	// the fresh account all the same — the five hours are a rate cap it
	// would be sent into before its allowance was read again
	share["a@x.com"] = provider.Allowance{
		{Used: 95, Resets: now.Add(time.Hour), Span: 5 * time.Hour},
		{Used: 10, Resets: now.Add(2 * time.Hour), Span: 7 * 24 * time.Hour},
	}
	share["b@x.com"] = week(0, 7*24*time.Hour)
	if got := order(acct("a@x.com"), acct("b@x.com")); got != "ba" {
		t.Fatalf("five hours running low wait: %s", got)
	}
	// two running low go by their share, ahead of one spent
	share["c@x.com"] = provider.Allowance{{Used: 92, Resets: now.Add(time.Hour), Span: 5 * time.Hour}, {Used: 10, Resets: now.Add(2 * time.Hour), Span: 7 * 24 * time.Hour}}
	share["b@x.com"] = week(99, time.Hour)
	if got := order(acct("b@x.com"), acct("a@x.com"), acct("c@x.com")); got != "cab" {
		t.Fatalf("low by share, then the spent: %s", got)
	}
	delete(share, "c@x.com")
	// Opus's own week decides an Opus request; Sonnet's goes by the general
	share["a@x.com"] = provider.Allowance{
		{Used: 10, Resets: now.Add(2 * 24 * time.Hour), Span: 7 * 24 * time.Hour},
		{Used: 95, Resets: now.Add(2 * 24 * time.Hour), Span: 7 * 24 * time.Hour, Model: "opus"},
	}
	share["b@x.com"] = provider.Allowance{
		{Used: 60, Resets: now.Add(2 * 24 * time.Hour), Span: 7 * 24 * time.Hour},
		{Used: 60, Resets: now.Add(2 * 24 * time.Hour), Span: 7 * 24 * time.Hour, Model: "opus"},
	}
	opus := func(user string) candidate {
		c := acct(user)
		c.model = "claude-opus-5-5"
		return c
	}
	if got, _ := weigh(p, []candidate{opus("a@x.com"), opus("b@x.com")}, "claude-opus-5-5", provider.Anthropic); got[0].p.Account.User != "b@x.com" {
		t.Fatalf("Opus request goes by the Opus week: %s first", got[0].p.Account.User)
	}
	if got := order(acct("b@x.com"), acct("a@x.com")); got != "ab" {
		t.Fatalf("a Sonnet request goes by the general week: %s", got)
	}
	// one not known counts as a fresh week: ahead of one half through its,
	// behind one about to lose what it has
	delete(share, "c@x.com")
	share["a@x.com"] = week(40, 5*24*time.Hour)
	share["b@x.com"] = week(80, time.Hour)
	if got := order(acct("a@x.com"), acct("c@x.com"), acct("b@x.com")); got != "bca" {
		t.Fatalf("not known as a fresh week: %s", got)
	}
	// one not known that tells what it has left as it answers (a Claude
	// account) goes first, once: else it would never be known
	claude := func(user string) candidate {
		c := acct(user)
		c.p.Account.Agent, c.rest = "claude", "claude#"+user
		return c
	}
	if got, _ := weigh(p, []candidate{claude("a@x.com"), claude("b@x.com"), claude("c@x.com")}, "gpt-5.6-sol", provider.Chat); got[0].p.Account.User != "c@x.com" {
		t.Fatalf("one that learns goes first: %s", got[0].p.Account.User)
	}
	// ...and by its pace once its answer has told
	share["c@x.com"] = week(50, 5*24*time.Hour) // 0.42, behind a's 0.5
	if got, _ := weigh(p, []candidate{claude("c@x.com"), claude("a@x.com"), claude("b@x.com")}, "gpt-5.6-sol", provider.Chat); got[0].p.Account.User != "b@x.com" || got[1].p.Account.User != "a@x.com" {
		t.Fatalf("known, it goes by its pace: %s %s", got[0].p.Account.User, got[1].p.Account.User)
	}
	delete(share, "c@x.com")
	// at 90% exactly, running low; just under, not
	share["a@x.com"] = provider.Allowance{{Used: 90, Resets: now.Add(time.Hour), Span: 5 * time.Hour}, {Used: 10, Resets: now.Add(2 * time.Hour), Span: 7 * 24 * time.Hour}}
	share["b@x.com"] = provider.Allowance{{Used: 89.9, Resets: now.Add(time.Hour), Span: 5 * time.Hour}, {Used: 60, Resets: now.Add(5 * 24 * time.Hour), Span: 7 * 24 * time.Hour}}
	if got := order(acct("a@x.com"), acct("b@x.com")); got != "ba" {
		t.Fatalf("90%% is running low, 89.9%% is not: %s", got)
	}
	// its five hours all but used up, its week healthy: last all the same
	share["a@x.com"] = provider.Allowance{
		{Used: 98, Resets: now.Add(time.Hour), Span: 5 * time.Hour},
		{Used: 10, Resets: now.Add(time.Hour), Span: 7 * 24 * time.Hour},
	}
	share["b@x.com"] = week(60, 5*24*time.Hour)
	if got := order(acct("a@x.com"), acct("b@x.com")); got != "ba" {
		t.Fatalf("five hours spent: %s", got)
	}
	// both spent: the one with the least used first, it may yet answer
	share["b@x.com"] = provider.Allowance{{Used: 100, Resets: now.Add(time.Hour), Span: 5 * time.Hour}, {Used: 10, Resets: now.Add(time.Hour), Span: 7 * 24 * time.Hour}}
	if got := order(acct("b@x.com"), acct("a@x.com")); got != "ab" {
		t.Fatalf("spent by how far: %s", got)
	}
	// the vendor tells the five hours alone (Claude's own usage command):
	// by what they have used, a fresh one as one not known
	share["a@x.com"] = provider.Allowance{{Used: 80, Resets: now.Add(time.Hour), Span: 5 * time.Hour}}
	share["b@x.com"] = provider.Allowance{{Used: 5, Resets: now.Add(time.Hour), Span: 5 * time.Hour}}
	share["c@x.com"] = provider.Allowance{{Used: 0, Resets: now.Add(time.Hour), Span: 5 * time.Hour}}
	delete(share, "d@x.com")
	if got := order(acct("a@x.com"), acct("d@x.com"), acct("c@x.com"), acct("b@x.com")); got != "dcba" {
		t.Fatalf("five hours alone go by their share used, the fresh with the unknown: %s", got)
	}
	// three paces a twelfth apart: alike pairwise all round, yet an order
	// — bands drawn from the top, so the first two are alike and the
	// third is not; within a band, the tokens sent lately decide
	share["a@x.com"] = week(0, 100*time.Hour)  // 1.00
	share["b@x.com"] = week(9, 100*time.Hour)  // 0.91
	share["c@x.com"] = week(17, 100*time.Hour) // 0.83
	sent := func(c candidate, n float64) {
		routed.Lock()
		defer routed.Unlock()
		if _, had := routed.used[c.restKey()]; !had {
			t.Cleanup(func() { routed.Lock(); delete(routed.used, c.restKey()); routed.Unlock() })
		}
		routed.used[c.restKey()] = tokenUse{n, now}
	}
	sent(acct("a@x.com"), 3000)
	sent(acct("b@x.com"), 2000)
	sent(acct("c@x.com"), 1000)
	if got := order(acct("a@x.com"), acct("b@x.com"), acct("c@x.com")); got != "bac" {
		t.Fatalf("bands from the top, tokens within: %s", got)
	}
	if got := order(acct("c@x.com"), acct("b@x.com"), acct("a@x.com")); got != "bac" {
		t.Fatalf("the same from the other end: %s", got)
	}
	// one running low doesn't draw the bands for those ahead of it: its
	// pace 1.05 would part a (1.00) from b (0.91), and a's tokens lose
	share["c@x.com"] = provider.Allowance{{Used: 95, Resets: now.Add(time.Hour), Span: 5 * time.Hour}, {Used: 16, Resets: now.Add(80 * time.Hour), Span: 7 * 24 * time.Hour}}
	if got := order(acct("a@x.com"), acct("b@x.com"), acct("c@x.com")); got != "bac" {
		t.Fatalf("the bands are the healthy ones' alone: %s", got)
	}
	// keys have no week: by the tokens sent lately
	key := func(id string) candidate {
		return candidate{p: provider.Provider{ID: id, Key: "k"}, model: "gpt-5.6-sol", rest: id}
	}
	sent(key("k1"), 500)
	sent(key("k2"), 100)
	kp := provider.Provider{ID: "keys", Routing: provider.Pace}
	if got, _ := weigh(kp, []candidate{key("k1"), key("k2")}, "gpt-5.6-sol", provider.Chat); got[0].p.ID != "k2" {
		t.Fatalf("keys by tokens: %s first", got[0].p.ID)
	}
	// a key follows the subscriptions with a week
	if got, _ := weigh(p, []candidate{key("k2"), acct("a@x.com")}, "gpt-5.6-sol", provider.Chat); got[0].p.Account == nil {
		t.Fatal("a key before a subscription")
	}
	// the trace tells the pace and the window it went by, the week's
	share["a@x.com"] = week(40, 5*24*time.Hour)
	share["b@x.com"] = week(80, time.Hour)
	got, wg := weigh(p, []candidate{acct("a@x.com"), acct("b@x.com")}, "gpt-5.6-sol", provider.Chat)
	if w := weighed(got[0], p, wg, false, provider.Chat); w.Pace < 19 || w.Pace > 21 || w.Due == nil || !w.Due.Equal(share["b@x.com"][1].Resets) {
		t.Fatalf("the trace: pace %v due %v", w.Pace, w.Due)
	}
	// Least used keeps its meaning: the most left first, whatever renews when
	p.Routing = provider.LeastUsed
	if got := order(acct("b@x.com"), acct("a@x.com")); got != "ab" {
		t.Fatalf("least used is most left first: %s", got)
	}
}
