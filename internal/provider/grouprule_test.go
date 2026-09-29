package provider

import (
	"slices"
	"strings"
	"testing"
)

func TestCleanRules(t *testing.T) {
	members := []string{"a/m", "b/big"}
	for _, tc := range []struct {
		rule Rule
		err  string
	}{
		{Rule{Tokens: 10}, "which model"},
		{Rule{Use: "c/x", Tokens: 10}, "not in the group"},
		{Rule{Use: "a/m", Tokens: -1}, "negative"},
		{Rule{Use: "a/m", Effort: "huge"}, "effort is on or one of"},
		{Rule{Use: "a/m"}, "needs a condition"},
		{Rule{Use: "a/m", Agents: []string{" ", ""}}, "needs a condition"},
		{Rule{Use: "a/m", Intent: " \n\t "}, "needs a condition"},
		{Rule{Use: "a/m", Intent: strings.Repeat("字", MaxIntent+1)}, "at most"},
	} {
		_, err := cleanRules([]Rule{{Use: "b/big", Images: true}, tc.rule}, members)
		if err == nil || !strings.Contains(err.Error(), tc.err) || !strings.HasPrefix(err.Error(), "rule 2:") {
			t.Errorf("%+v: %v, want rule 2: …%s", tc.rule, err, tc.err)
		}
	}
	got, err := cleanRules([]Rule{
		{Use: " b/big ", Effort: " HIGH ", Agents: []string{" Claude-Code ", "codex", "codex", ""}},
		{Use: "a/m", Effort: "On", Agents: []string{}, Intent: "  writing\n  tests "},
	}, members)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Use != "b/big" || got[0].Effort != "high" || !slices.Equal(got[0].Agents, []string{"claude-code", "codex"}) {
		t.Errorf("%+v", got[0])
	}
	if got[1].Effort != "on" || got[1].Agents != nil || got[1].Intent != "writing tests" {
		t.Errorf("%+v", got[1])
	}
	if got, err := cleanRules(nil, members); err != nil || got != nil {
		t.Errorf("no rules: %v %v", got, err)
	}
}

func TestRuleMatches(t *testing.T) {
	for _, tc := range []struct {
		name string
		rule Rule
		q    RuleRequest
		want bool
	}{
		{"tokens below", Rule{Tokens: 1000}, RuleRequest{Tokens: 999}, false},
		{"tokens at", Rule{Tokens: 1000}, RuleRequest{Tokens: 1000}, true},
		{"tokens above", Rule{Tokens: 1000}, RuleRequest{Tokens: 5000}, true},
		{"images none", Rule{Images: true}, RuleRequest{Tokens: 1 << 20}, false},
		{"images", Rule{Images: true}, RuleRequest{Images: true}, true},
		{"on, none", Rule{Effort: "on"}, RuleRequest{}, false},
		{"on, thinking", Rule{Effort: "on"}, RuleRequest{Thinking: true}, true},
		{"on, a level", Rule{Effort: "on"}, RuleRequest{Effort: "low"}, true},
		{"high, none", Rule{Effort: "high"}, RuleRequest{}, false},
		{"high, thinking without a level", Rule{Effort: "high"}, RuleRequest{Thinking: true}, false},
		{"high, medium", Rule{Effort: "high"}, RuleRequest{Effort: "medium"}, false},
		{"high, high", Rule{Effort: "high"}, RuleRequest{Effort: "high"}, true},
		{"high, xhigh", Rule{Effort: "high"}, RuleRequest{Effort: "xhigh"}, true},
		{"high, max", Rule{Effort: "high"}, RuleRequest{Effort: "max"}, true},
		{"high, unknown level", Rule{Effort: "high"}, RuleRequest{Effort: "minimal"}, false},
		{"low, minimal", Rule{Effort: "low"}, RuleRequest{Effort: "minimal"}, false},
		{"agent", Rule{Agents: []string{"codex"}}, RuleRequest{Agent: "Codex"}, true},
		{"other agent", Rule{Agents: []string{"codex"}}, RuleRequest{Agent: "claude-code"}, false},
		{"no agent", Rule{Agents: []string{"codex"}}, RuleRequest{}, false},
		{"and, one short", Rule{Tokens: 100, Images: true}, RuleRequest{Tokens: 100}, false},
		{"and, all", Rule{Tokens: 100, Images: true, Agents: []string{"codex"}}, RuleRequest{Tokens: 100, Images: true, Agent: "codex"}, true},
		{"and, wrong agent", Rule{Tokens: 100, Images: true, Agents: []string{"codex"}}, RuleRequest{Tokens: 100, Images: true, Agent: "x"}, false},
		{"no condition", Rule{Use: "a/m"}, RuleRequest{Tokens: 5, Images: true}, false},
	} {
		if got := tc.rule.Matches(tc.q); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestMatchRuleFirstWins(t *testing.T) {
	rules := []Rule{
		{Use: "img", Images: true},
		{Use: "long", Tokens: 100},
		{Use: "longer", Tokens: 50},
	}
	for _, tc := range []struct {
		q    RuleRequest
		want int
	}{
		{RuleRequest{Tokens: 10}, -1},
		{RuleRequest{Tokens: 60}, 2},
		{RuleRequest{Tokens: 200}, 1},
		{RuleRequest{Tokens: 200, Images: true}, 0},
	} {
		if got := MatchRule(rules, tc.q); got != tc.want {
			t.Errorf("%+v: %d, want %d", tc.q, got, tc.want)
		}
	}
	if MatchRule(nil, RuleRequest{Tokens: 1}) != -1 {
		t.Error("no rules matched")
	}
}

// An intent matches the classifier's answer, whatever its case; it holds
// with the rule's other conditions.
func TestRuleIntent(t *testing.T) {
	r := Rule{Use: "a/m", Intent: "Writing tests", Agents: []string{"codex"}}
	for _, tc := range []struct {
		q         RuleRequest
		match, bi bool
	}{
		{RuleRequest{Agent: "codex", Intent: "writing tests"}, true, true},
		{RuleRequest{Agent: "codex", Intent: "WRITING TESTS"}, true, true},
		{RuleRequest{Agent: "codex"}, false, true},
		{RuleRequest{Agent: "codex", Intent: "debugging"}, false, true},
		{RuleRequest{Agent: "claude", Intent: "writing tests"}, false, false},
	} {
		if r.Matches(tc.q) != tc.match || r.MatchesBesidesIntent(tc.q) != tc.bi {
			t.Errorf("%+v: %v %v", tc.q, r.Matches(tc.q), r.MatchesBesidesIntent(tc.q))
		}
	}
	if c := r.Conditions(); len(c) != 2 || c[1] != `intent "Writing tests"` {
		t.Errorf("%q", c)
	}
	rules := []Rule{
		{Use: "a", Intent: "quick question"},
		{Use: "b", Intent: "debugging", Images: true},
		{Use: "b", Intent: "Quick Question", Agents: []string{"codex"}},
		{Use: "c", Intent: "planning"},
		{Use: "d", Tokens: 100},
		{Use: "e", Intent: "after"},
	}
	for _, tc := range []struct {
		q    RuleRequest
		want []string
	}{
		{RuleRequest{Tokens: 500, Agent: "codex"}, []string{"quick question", "planning"}},
		{RuleRequest{Tokens: 500, Images: true}, []string{"quick question", "debugging", "planning"}},
		{RuleRequest{Tokens: 500}, []string{"quick question", "planning"}},
		{RuleRequest{Tokens: 5}, []string{"quick question", "planning", "after"}},
	} {
		if got := Intents(rules, tc.q); !slices.Equal(got, tc.want) {
			t.Errorf("%+v: %q, want %q", tc.q, got, tc.want)
		}
	}
	if got := Intents([]Rule{{Use: "d", Tokens: 1}, {Use: "a", Intent: "x"}}, RuleRequest{Tokens: 5}); got != nil {
		t.Errorf("a plain rule first: %q", got)
	}
}

func TestRuleConditions(t *testing.T) {
	got := Rule{Tokens: 200000, Images: true, Effort: "high", Agents: []string{"codex", "claude-code"}}.Conditions()
	want := []string{"tokens ≥ 200000", "images", "effort ≥ high", "agent codex|claude-code"}
	if !slices.Equal(got, want) {
		t.Errorf("%q", got)
	}
	if got := (Rule{Effort: "on"}).Conditions(); !slices.Equal(got, []string{"reasoning"}) {
		t.Errorf("%q", got)
	}
}

// ruledEntry says a group can do more than all its members only when its
// rules make sure every request that needs more goes where there is more.
func TestRuledEntry(t *testing.T) {
	no, yes := false, true
	entries := []Entry{
		{ID: "a/text", Model: "text", Provider: Provider{ID: "a"}, Context: 128000, ImageInput: &no},
		{ID: "a/vision", Model: "vision", Provider: Provider{ID: "a"}, Context: 200000, Images: true, ImageInput: &yes},
		{ID: "b/huge", Model: "huge", Provider: Provider{ID: "b"}, Context: 1000000},
		{ID: "b/small", Model: "small", Provider: Provider{ID: "b"}, Context: 32000},
	}
	member := func(id string) Member {
		p, m, _ := strings.Cut(id, "/")
		return Member{ID: id, Provider: Provider{ID: p}, Model: m}
	}
	run := func(members []string, rules []Rule) Entry {
		var ms []Member
		for _, id := range members {
			ms = append(ms, member(id))
		}
		// as groupEntries leaves it without rules: what every member can do
		e := Entry{Images: false, Context: 0}
		for _, x := range entries {
			if slices.Contains(members, x.ID) && x.Context > 0 && (e.Context == 0 || x.Context < e.Context) {
				e.Context = x.Context
			}
		}
		ruledEntry(&e, Group{Members: members, Rules: rules}, ms, entries)
		return e
	}
	for _, tc := range []struct {
		name    string
		members []string
		rules   []Rule
		images  bool
		context int
	}{
		{"no rules", []string{"a/text", "a/vision"}, nil, false, 128000},
		{"images rule", []string{"a/text", "a/vision"}, []Rule{{Use: "a/vision", Images: true}}, true, 128000},
		{"images and more", []string{"a/text", "a/vision"}, []Rule{{Use: "a/vision", Images: true, Agents: []string{"codex"}}}, false, 128000},
		{"images to text", []string{"a/text", "a/vision"}, []Rule{{Use: "a/text", Images: true}}, false, 128000},
		{"text rule before", []string{"a/text", "a/vision"}, []Rule{{Use: "a/text", Agents: []string{"codex"}}, {Use: "a/vision", Images: true}}, false, 128000},
		{"vision rule before", []string{"a/text", "a/vision"}, []Rule{{Use: "a/vision", Tokens: 1}, {Use: "a/vision", Images: true}}, true, 200000},
		{"unknown rule before", []string{"a/text", "a/vision", "b/huge"}, []Rule{{Use: "b/huge", Effort: "high"}, {Use: "a/vision", Images: true}}, false, 128000},
		{"long rule", []string{"a/text", "b/huge"}, []Rule{{Use: "b/huge", Tokens: 100000}}, false, 1000000},
		{"long rule past a member", []string{"a/text", "b/huge"}, []Rule{{Use: "b/huge", Tokens: 150000}}, false, 128000},
		{"long rule, a smaller member", []string{"a/text", "b/small", "b/huge"}, []Rule{{Use: "b/huge", Tokens: 100000}}, false, 32000},
		{"images and an intent", []string{"a/text", "a/vision"}, []Rule{{Use: "a/vision", Images: true, Intent: "screenshots"}}, false, 128000},
		{"long rule and an intent", []string{"a/text", "b/huge"}, []Rule{{Use: "b/huge", Tokens: 100000, Intent: "big refactors"}}, false, 128000},
		{"long rule and more", []string{"a/text", "b/huge"}, []Rule{{Use: "b/huge", Tokens: 100000, Effort: "on"}}, false, 128000},
		{"capped by a rule before", []string{"a/text", "a/vision", "b/huge"}, []Rule{{Use: "a/vision", Agents: []string{"codex"}}, {Use: "b/huge", Tokens: 100000}}, false, 200000},
		{"long rule to less", []string{"a/text", "a/vision"}, []Rule{{Use: "a/text", Tokens: 1000}}, false, 128000},
		{"rule to one not ready", []string{"a/text", "b/huge"}, []Rule{{Use: "c/gone", Tokens: 100000}}, false, 128000},
		{"one not ready before", []string{"a/text", "b/huge"}, []Rule{{Use: "c/gone", Agents: []string{"x"}}, {Use: "b/huge", Tokens: 100000}}, false, 128000},
	} {
		e := run(tc.members, tc.rules)
		if e.Images != tc.images || e.Context != tc.context {
			t.Errorf("%s: images %v context %d, want %v %d", tc.name, e.Images, e.Context, tc.images, tc.context)
		}
	}
}

// A saved group keeps its rules cleaned, and one naming a member it
// doesn't have is refused.
func TestSaveGroupRules(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, id := range []string{"a", "b"} {
		if err := Save(Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m", "big"}}); err != nil {
			t.Fatal(err)
		}
	}
	err := SaveGroup(Group{Name: "R", Members: []string{"a/m", "b/big"}, Rules: []Rule{{Use: "b/m", Tokens: 5}}})
	if err == nil || !strings.Contains(err.Error(), "not in the group") {
		t.Fatalf("%v", err)
	}
	if err := SaveGroup(Group{Name: "R", Members: []string{"a/m", "b/big"}, Rules: []Rule{{Use: "b/big", Tokens: 5, Effort: "HIGH"}}}); err != nil {
		t.Fatal(err)
	}
	g, _, ok := FindGroup(GroupPrefix + "r")
	if !ok || len(g.Rules) != 1 || g.Rules[0].Effort != "high" || g.Rules[0].Use != "b/big" {
		t.Fatalf("%v %+v", ok, g)
	}
}

// A group with an intent needs a classifier: a model magpie knows, or
// another group, never itself. Without intents it keeps none.
func TestSaveGroupClassifier(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, id := range []string{"a", "b"} {
		if err := Save(Provider{ID: id, Name: id, Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m", "big"}}); err != nil {
			t.Fatal(err)
		}
	}
	intent := []Rule{{Use: "b/big", Intent: "planning"}}
	for _, tc := range []struct {
		classifier, err string
	}{
		{"", "needs the group's classifier"},
		{"group/other", "no group"},
		{"group/r", "its own classifier"},
		{"z/nothing", "knows no model"},
	} {
		err := SaveGroup(Group{Name: "R", Members: []string{"a/m", "b/big"}, Rules: intent, Classifier: tc.classifier})
		if err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%q: %v, want …%s", tc.classifier, err, tc.err)
		}
	}
	if err := SaveGroup(Group{Name: "R", Members: []string{"a/m", "b/big"}, Rules: intent, Classifier: " magpie/a/m "}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := FindGroup(GroupPrefix + "r")
	if g.Classifier != "a/m" || g.Rules[0].Intent != "planning" {
		t.Fatalf("%+v", g)
	}
	// another group classifies with its failover; renamed, it is followed,
	// and it can't be removed while it does
	if err := SaveGroup(Group{Name: "Fast", Members: []string{"a/m", "b/m"}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{Name: "R", Members: []string{"a/m", "b/big"}, Rules: intent, Classifier: "group/fast"}); err != nil {
		t.Fatal(err)
	}
	if err := RenameGroup("fast", "quick"); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup(GroupPrefix + "r"); g.Classifier != "group/quick" {
		t.Fatalf("classifier %q after the rename", g.Classifier)
	}
	if err := DeleteGroup("quick"); err == nil || !strings.Contains(err.Error(), "classifier") {
		t.Fatalf("%v", err)
	}
	if err := SaveGroup(Group{Name: "R", Members: []string{"a/m", "b/big"}, Rules: []Rule{{Use: "b/big", Tokens: 5}}, Classifier: "a/m"}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup(GroupPrefix + "r"); g.Classifier != "" {
		t.Fatalf("kept %q without an intent", g.Classifier)
	}
}

func TestThenUses(t *testing.T) {
	rules := []Rule{
		{Use: "codex/gpt", Images: true},
		{Use: "ai/long", Tokens: 100000},
		{Use: "codex/gpt", Images: true, Tokens: 10},
		{Use: "ai/flash", Images: true},
	}
	q := RuleRequest{Images: true, Tokens: 50}
	if got := ThenUses(rules, q, 1, "codex/gpt"); !slices.Equal(got, []string{"ai/flash"}) {
		t.Fatal(got)
	}
	if got := ThenUses(rules, q, 4, "ai/flash"); len(got) != 0 {
		t.Fatal(got)
	}
}
