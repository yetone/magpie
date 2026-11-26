package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// smartRuled is intented's group with the smart template's rules: a hard
// turn to b/big, an easy one to a/small (TemplateSmart).
func smartRuled(t *testing.T, affinity string) (*Server, *clsUp, func() time.Time, func(time.Duration)) {
	t.Helper()
	now := time.Date(2026, 11, 27, 10, 4, 0, 0, time.Local)
	ruleClock = func() time.Time { return now }
	t.Cleanup(func() { ruleClock = time.Now })
	s, _, _, c := intented(t,
		provider.Rule{Use: "b/big", Intent: provider.IntentHard},
		provider.Rule{Use: "a/small", Intent: provider.IntentEasy},
	)
	if affinity != "" {
		g, _, _ := provider.FindGroup("group/r")
		g.Affinity = affinity
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	return s, c, func() time.Time { return now }, func(d time.Duration) { now = now.Add(d) }
}

// A conversation in a smart routing group stays on the model its cache is
// warm on when the classifier only takes a turn to be easier
// (chet030714 on X: one Claude Code conversation flipped between two
// models turn by turn, and each flip read the whole history uncached). A
// harder turn still moves up at once; once the cache went cold, an easier
// turn moves down.
func TestRuleStaysWhereTheCacheIs(t *testing.T) {
	s, c, _, wait := smartRuled(t, "")
	var said []string
	turn := func(answer, words string, tools int, wantFrom, wantUse, wantStay string) Route {
		t.Helper()
		c.set(answer, 0)
		if tools == 0 {
			said = append(said, words)
		}
		out, r := postOK(t, s, "cc", chat(said[0], said[1:], tools, ""))
		stay := ""
		if r.Rule != nil && r.Rule.Stay != nil {
			stay = r.Rule.Stay.Use
		}
		if !strings.Contains(out, "from "+wantFrom) || r.Rule == nil || r.Rule.Use != wantUse || stay != wantStay {
			t.Fatalf("%q (%d tool rounds): %s, rule %+v (stay %q); want from %s, %s, stay %q", words, tools, out, r.Rule, stay, wantFrom, wantUse, wantStay)
		}
		return r
	}
	r := turn("1", "the parser deadlocks under load, find out why", 0, "kb", "b/big", "")
	if r.Rule.N != 1 || r.Rule.Classified == nil || r.Rule.Classified.Intent != provider.IntentHard {
		t.Fatalf("turn 1: %+v", r.Rule)
	}
	wait(30 * time.Second)
	r = turn("2", "now rename the helper", 0, "kb", "b/big", "a/small") // easier: stays for the cache
	if r.Rule.N != 1 || r.Rule.Stay.N != 2 || r.Rule.Stay.Tokens < cacheWorth || r.Rule.Classified.Intent != provider.IntentEasy {
		t.Fatalf("turn 2: %+v %+v", r.Rule, r.Rule.Stay)
	}
	wait(10 * time.Second)
	r = turn("2", "", 2, "kb", "b/big", "") // its tool rounds with it
	if !r.Rule.Held {
		t.Fatalf("turn 2's tools: %+v", r.Rule)
	}
	wait(30 * time.Second)
	turn("2", "and the test name", 0, "kb", "b/big", "a/small") // easier again: still stays
	wait(cacheCold + time.Minute)
	turn("2", "run the tests", 0, "ka", "a/small", "") // the cache went cold: it moves down
	wait(30 * time.Second)
	turn("1", "they fail on a race, redesign the locking", 0, "kb", "b/big", "") // harder: up at once
	wait(30 * time.Second)
	turn("2", "commit it", 0, "kb", "b/big", "a/small")
}

// A group whose Stays is "within a turn" or "never" moves a conversation
// with each turn's intent, as before: the user's setting wins.
func TestRuleStayFollowsTheGroupsStays(t *testing.T) {
	for _, aff := range []string{provider.AffinityTurn, provider.AffinityOff} {
		t.Run(aff, func(t *testing.T) {
			s, c, _, wait := smartRuled(t, aff)
			c.set("1", 0)
			first := "the parser deadlocks under load, find out why"
			if out, r := postOK(t, s, "cc", chat(first, nil, 0, "")); !strings.Contains(out, "from kb") || r.Rule.Use != "b/big" {
				t.Fatalf("turn 1: %s %+v", out, r.Rule)
			}
			wait(30 * time.Second)
			c.set("2", 0)
			if out, r := postOK(t, s, "cc", chat(first, []string{"now rename the helper"}, 0, "")); !strings.Contains(out, "from ka") || r.Rule.Use != "a/small" || r.Rule.Stay != nil {
				t.Fatalf("turn 2: %s %+v", out, r.Rule)
			}
		})
	}
}

// A conversation answered by another member than its rule's (it failed
// over) has no cache on the rule's member: an easier turn moves.
func TestRuleStayNeedsTheCacheThere(t *testing.T) {
	s, c, _, wait := smartRuled(t, "")
	c.set("1", 0)
	first := "the parser deadlocks under load, find out why"
	postOK(t, s, "cc", chat(first, nil, 0, ""))
	turnRules.Lock()
	for k, tr := range turnRules.m {
		tr.by = answeredBy{model: "small", rest: "a"} // as if a/small had answered
		turnRules.m[k] = tr
	}
	turnRules.Unlock()
	wait(30 * time.Second)
	c.set("2", 0)
	if out, r := postOK(t, s, "cc", chat(first, []string{"now rename the helper"}, 0, "")); !strings.Contains(out, "from ka") || r.Rule.Stay != nil {
		t.Fatalf("turn 2: %s %+v", out, r.Rule)
	}
}

// keepsMember holds a conversation only against a move the classifier's
// answer alone makes, down, while the cache it has there is warm.
func TestKeepsMember(t *testing.T) {
	fresh(t)
	no := false
	for _, x := range []struct{ id, model string }{{"a", "small"}, {"b", "big"}} {
		if err := provider.Save(provider.Provider{ID: x.id, Name: strings.ToUpper(x.id), Key: "k" + x.id, Models: []string{x.model}, Chat: "http://127.0.0.1:9/v1"}); err != nil {
			t.Fatal(err)
		}
		ctx := 1_000_000
		if x.id == "a" {
			ctx = 64_000
		}
		if err := catalog.SaveLive(x.id, "http://127.0.0.1:9/v1", []catalog.Model{{ID: x.model, Context: ctx, ImageInput: &no}}); err != nil {
			t.Fatal(err)
		}
	}
	hard, easy := provider.Rule{Use: "b/big", Intent: provider.IntentHard}, provider.Rule{Use: "a/small", Intent: provider.IntentEasy}
	g := provider.Group{ID: "r", Members: []string{"a/small", "b/big"}, Rules: []provider.Rule{hard, easy}}
	ms := []provider.Member{
		{ID: "a/small", Path: []string{"a/small"}, Provider: provider.Provider{ID: "a"}, Model: "small", At: 0},
		{ID: "b/big", Path: []string{"b/big"}, Provider: provider.Provider{ID: "b"}, Model: "big", At: 1},
	}
	at := time.Date(2026, 11, 27, 10, 4, 0, 0, time.Local)
	onBig := turnRule{use: "b/big", n: 1, at: at, input: 40_000, intent: provider.IntentHard, size: 40_000, by: answeredBy{model: "big", rest: "b"}}
	onSmall := turnRule{use: "a/small", n: 2, at: at, input: 40_000, intent: provider.IntentEasy, size: 40_000, by: answeredBy{model: "small", rest: "a"}}
	easyQ := provider.RuleRequest{Tokens: 41_000, Intent: provider.IntentEasy}
	hardQ := provider.RuleRequest{Tokens: 41_000, Intent: provider.IntentHard}
	toSmall, toBig := &RuleHit{N: 2, Use: "a/small"}, &RuleHit{N: 1, Use: "b/big"}
	soon := at.Add(time.Minute)
	with := func(tr turnRule, f func(*turnRule)) turnRule { f(&tr); return tr }
	for _, c := range []struct {
		name   string
		g      provider.Group
		tr     turnRule
		q      provider.RuleRequest
		hit    *RuleHit
		tokens int
		now    time.Time
		want   bool
	}{
		{"down, warm: stays", g, onBig, easyQ, toSmall, 41_000, soon, true},
		{"up: moves", g, onSmall, hardQ, toBig, 41_000, soon, false},
		{"same rule", g, onBig, hardQ, toBig, 41_000, soon, false},
		{"cold", g, onBig, easyQ, toSmall, 41_000, at.Add(cacheCold + time.Second), false},
		{"an hour's cache: warm at 20 minutes", g, with(onBig, func(tr *turnRule) { tr.hour = hourCache{40_000, at} }), easyQ, toSmall, 41_000, at.Add(20 * time.Minute), true},
		{"compacted", g, onBig, easyQ, toSmall, 12_000, soon, false},
		{"too short to cache", g, with(onBig, func(tr *turnRule) { tr.input = cacheWorth - 1 }), easyQ, toSmall, 41_000, soon, false},
		{"answered by another", g, with(onBig, func(tr *turnRule) { tr.by = answeredBy{model: "small", rest: "a"} }), easyQ, toSmall, 41_000, soon, false},
		{"never answered", g, with(onBig, func(tr *turnRule) { tr.by = answeredBy{} }), easyQ, toSmall, 41_000, soon, false},
		{"stays within a turn", with2(g, provider.AffinityTurn), onBig, easyQ, toSmall, 41_000, soon, false},
		{"stays never", with2(g, provider.AffinityOff), onBig, easyQ, toSmall, 41_000, soon, false},
		{"stays for the session", with2(g, provider.AffinitySession), onBig, easyQ, toSmall, 41_000, soon, true},
		{"strict", with2(g, provider.AffinityStrict), onBig, easyQ, toSmall, 41_000, soon, true},
		{"the rule changed", provider.Group{ID: "r", Rules: []provider.Rule{{Use: "a/small", Intent: provider.IntentHard}, easy}}, onBig, easyQ, toSmall, 41_000, soon, false},
		// an image now: the rule moves for that, not for the intent
		{"not the intent alone", provider.Group{ID: "r", Rules: []provider.Rule{{Use: "a/small", Images: true}, hard, easy}}, with(onBig, func(tr *turnRule) { tr.n = 2 }), provider.RuleRequest{Tokens: 41_000, Intent: provider.IntentHard, Images: true}, &RuleHit{N: 1, Use: "a/small"}, 41_000, soon, false},
		{"the intent alone, an image rule after", provider.Group{ID: "r", Rules: []provider.Rule{hard, {Use: "a/small", Images: true}, easy}}, onBig, provider.RuleRequest{Tokens: 41_000, Intent: provider.IntentEasy, Images: true}, &RuleHit{N: 2, Use: "a/small"}, 41_000, soon, true},
		// intents a user wrote: kinds of work, each to its rule's member
		{"not the smart ladder", provider.Group{ID: "r", Rules: []provider.Rule{{Use: "b/big", Intent: "coding"}, {Use: "a/small", Intent: "a question"}}}, with(onBig, func(tr *turnRule) { tr.intent = "coding" }), provider.RuleRequest{Tokens: 41_000, Intent: "a question"}, toSmall, 41_000, soon, false},
		{"held before, easy again", g, with(onBig, func(tr *turnRule) { tr.intent, tr.ruleIntent = provider.IntentEasy, provider.IntentHard }), easyQ, toSmall, 41_000, soon, true},
		// the member it is on is too small for the conversation now
		{"outgrown", g, with(onSmall, func(tr *turnRule) { tr.input = 70_000; tr.size = 70_000 }), provider.RuleRequest{Tokens: 70_000, Intent: provider.IntentHard}, toBig, 70_000, soon, false},
	} {
		if got := keepsMember(c.g, ms, c.tr, c.q, c.hit, c.now, c.tokens); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func with2(g provider.Group, affinity string) provider.Group {
	g.Affinity = affinity
	return g
}
