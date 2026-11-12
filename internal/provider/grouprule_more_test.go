package provider

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// A rule names several models (monkeyin_92 on X): each must be the
// group's, each is named once, and a pause names one.
func TestCleanRulesSeveralModels(t *testing.T) {
	members := []string{"a/m", "b/big", "c/x"}
	got, err := cleanRules([]Rule{{Use: " b/big ", More: []string{" a/m", "", "b/big", "c/x"}, Tokens: 5}}, members)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Use != "b/big" || !slices.Equal(got[0].More, []string{"a/m", "c/x"}) || !slices.Equal(got[0].Uses(), []string{"b/big", "a/m", "c/x"}) {
		t.Fatalf("%+v", got[0])
	}
	if _, err := cleanRules([]Rule{{Use: "b/big", More: []string{"d/gone"}, Tokens: 5}}, members); err == nil || !strings.Contains(err.Error(), "d/gone is not in the group") {
		t.Fatalf("a model not in the group: %v", err)
	}
	if _, err := cleanRules([]Rule{{Use: "b/big", More: []string{"a/m"}, Pause: true, Agents: []string{"codex"}}}, members); err == nil || !strings.Contains(err.Error(), "a pause names one model") {
		t.Fatalf("pause: %v", err)
	}
	// only More, as a hand-edited file may have it: the first is Use
	if got, err := cleanRules([]Rule{{More: []string{"c/x", "a/m"}, Tokens: 5}}, members); err != nil || got[0].Use != "c/x" || !slices.Equal(got[0].More, []string{"a/m"}) {
		t.Fatalf("only more: %+v %v", got, err)
	}
}

// A group saved before More reads as it was; one saved with it keeps
// Use first, which is what a magpie from before More sends to.
func TestRuleMoreOnDisk(t *testing.T) {
	var old Rule
	if err := json.Unmarshal([]byte(`{"use":"b/big","tokens":5}`), &old); err != nil || old.Use != "b/big" || old.More != nil {
		t.Fatalf("old: %+v %v", old, err)
	}
	b, _ := json.Marshal(old)
	if strings.Contains(string(b), "more") {
		t.Fatalf("a rule of one model writes more: %s", b)
	}
	b, _ = json.Marshal(Rule{Use: "b/big", More: []string{"a/m"}, Tokens: 5})
	var before struct {
		Use string `json:"use"`
	}
	if err := json.Unmarshal(b, &before); err != nil || before.Use != "b/big" {
		t.Fatalf("%s: %+v", b, before)
	}
}

// Without takes one model out of a rule; the rule goes only with its last.
func TestRuleWithout(t *testing.T) {
	r := Rule{Use: "a/m", More: []string{"b/big"}, Tokens: 5}
	if k, ok := r.Without("a/m"); !ok || k.Use != "b/big" || k.More != nil || k.Tokens != 5 {
		t.Fatalf("%+v %v", k, ok)
	}
	if k, ok := r.Without("b/big"); !ok || k.Use != "a/m" || k.More != nil {
		t.Fatalf("%+v %v", k, ok)
	}
	if _, ok := (Rule{Use: "a/m", Tokens: 5}).Without("a/m"); ok {
		t.Fatal("a rule left with no model kept")
	}
	if !r.Names("b/big") || r.Names("c/x") {
		t.Fatal("Names")
	}
}

// use=a,b names several, in order; Line writes them back the same way.
func TestParseRuleSeveralModels(t *testing.T) {
	g := Group{ID: "g", Members: []string{"a/small", "b/big", "c/x"}}
	r, _, _, err := ParseRule(g, []string{"use=big,small", "tokens=5000"})
	if err != nil || r.Use != "b/big" || !slices.Equal(r.More, []string{"a/small"}) {
		t.Fatalf("%+v %v", r, err)
	}
	if line := r.Line(); !strings.Contains(line, "use=b/big,a/small") {
		t.Fatalf("line %q", line)
	}
	back, _, _, err := ParseRule(g, strings.Fields(r.Line()))
	if err != nil || back.Use != r.Use || !slices.Equal(back.More, r.More) {
		t.Fatalf("round trip %+v %v", back, err)
	}
	if _, _, _, err := ParseRule(g, []string{"use=big,nope", "tokens=5"}); err == nil {
		t.Fatal("a model not in the group taken")
	}
	if _, _, _, err := ParseRule(g, []string{"use=", "tokens=5"}); err == nil {
		t.Fatal("no model taken")
	}
	if r, _, _, err := ParseRule(g, []string{"use=big,small", "pause=c/x", "agent=codex"}); err != nil || r.More != nil || r.Use != "c/x" {
		t.Fatalf("pause after use: %+v %v", r, err)
	}
}

// The rules after the one that matched name who comes next, leaving out
// every model the matching rule names itself.
func TestThenUsesLeavesOutTheRulesOwn(t *testing.T) {
	rules := []Rule{
		{Use: "a/m", More: []string{"b/big"}, Tokens: 10},
		{Use: "b/big", Tokens: 10},
		{Use: "c/x", More: []string{"a/m", "d/y"}, Tokens: 10},
	}
	q := RuleRequest{Tokens: 50}
	if got := ThenUses(rules, q, 1, rules[0].Uses()); !slices.Equal(got, []string{"c/x", "d/y"}) {
		t.Fatal(got)
	}
}

// What a group says it can take, as its rules have it: a rule's request
// may go to any model it names, so it is what each of them takes.
func TestRuledEntrySeveralModels(t *testing.T) {
	no, yes := false, true
	entries := []Entry{
		{ID: "a/text", Model: "text", Provider: &Provider{ID: "a"}, Context: 128000, ImageInput: &no},
		{ID: "a/vision", Model: "vision", Provider: &Provider{ID: "a"}, Context: 200000, Images: true, ImageInput: &yes},
		{ID: "b/vision", Model: "vision", Provider: &Provider{ID: "b"}, Context: 400000, Images: true, ImageInput: &yes},
	}
	run := func(members []string, rules []Rule) Entry {
		var ms []Member
		for _, id := range members {
			p, m, _ := strings.Cut(id, "/")
			ms = append(ms, Member{ID: id, Provider: Provider{ID: p}, Model: m})
		}
		e := Entry{Context: 128000}
		ruledEntry(&e, Group{Members: members, Rules: rules}, ms, entries)
		return e
	}
	if e := run([]string{"a/text", "a/vision", "b/vision"}, []Rule{{Use: "a/vision", More: []string{"b/vision"}, Images: true}}); !e.Images {
		t.Errorf("two that see: %+v", e)
	}
	if e := run([]string{"a/text", "a/vision"}, []Rule{{Use: "a/vision", More: []string{"a/text"}, Images: true}}); e.Images {
		t.Errorf("one blind: %+v", e)
	}
	if e := run([]string{"a/text", "a/vision", "b/vision"}, []Rule{{Use: "b/vision", More: []string{"a/vision"}, Tokens: 100000}}); e.Context != 200000 {
		t.Errorf("long: the least of the rule's: %+v", e)
	}
}

// A shared group's rule keeps the models this magpie has, in order; one
// it hasn't is left out of the rule, and the rule goes when none is here.
func TestSharedGroupRuleSeveralModels(t *testing.T) {
	shareHome(t, Provider{ID: "mine", Name: "Mine", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"x", "y"}})
	text, _ := json.Marshal(SharedGroup{Magpie: ShareMark, Version: ShareVersion, Group: Group{
		ID: "theirs", Name: "Theirs", Members: []string{"relay/x", "gone/z", "relay/y"}, Routing: Ordered,
		Rules: []Rule{
			{Use: "gone/z", More: []string{"relay/y", "relay/x"}, Tokens: 1000},
			{Use: "gone/z", Agents: []string{"claude"}},
		},
	}})
	res, err := ImportGroup(string(text))
	if err != nil {
		t.Fatal(err)
	}
	g := sharedAt(t, res.ID)
	if res.Rules != 1 || len(g.Rules) != 1 || g.Rules[0].Use != "mine/y" || !slices.Equal(g.Rules[0].More, []string{"mine/x"}) {
		t.Fatalf("left out %d, %+v", res.Rules, g.Rules)
	}
}

// A model renamed, or the group's member removed, is renamed or taken out
// in every rule naming it, More too; a member a rule's More names stays.
func TestRuleMoreRenamedAndKept(t *testing.T) {
	shareHome(t, Provider{ID: "relay", Name: "R", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}})
	if err := SaveGroup(Group{ID: "top", Name: "Top", Members: []string{"relay/m1", "relay/m2"},
		Rules: []Rule{{Use: "relay/m1", More: []string{"relay/m2"}, Tokens: 5}}}); err != nil {
		t.Fatal(err)
	}
	if err := Rename("relay", "fast"); err != nil {
		t.Fatal(err)
	}
	g, _, _ := FindGroup("group/top")
	if g.Rules[0].Use != "fast/m1" || !slices.Equal(g.Rules[0].More, []string{"fast/m2"}) {
		t.Fatalf("renamed: %+v", g.Rules)
	}
	kept := withoutRemoved(Group{Members: []string{"fast/m1", "gone/m2"}, Rules: []Rule{{Use: "fast/m1", More: []string{"gone/m2"}, Tokens: 5}}}, map[string]bool{"fast": true})
	if !slices.Contains(kept.Members, "gone/m2") {
		t.Fatalf("a model a rule's More names was taken out: %+v", kept)
	}
}
