package cli

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestParseTokens(t *testing.T) {
	for in, want := range map[string]int{"200000": 200000, "200k": 200000, "200K": 200000, "1m": 1000000, "1.5m": 1500000, "128_000": 128000, " 32k ": 32000} {
		if got, err := parseTokens(in); err != nil || got != want {
			t.Errorf("%q: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "k", "lots", "-5", "1g", "nan", "inf", "1e20"} {
		if _, err := parseTokens(in); err == nil {
			t.Errorf("%q read", in)
		}
	}
}

func TestGroupRuleCmd(t *testing.T) {
	groupsHome(t)
	if _, err := addGroup("Opus", []string{"models=a/claude-opus-5-5,b/gpt-5.5,a/only-a"}); err != nil {
		t.Fatal(err)
	}
	rules := func() []provider.Rule {
		t.Helper()
		g, err := findGroup("opus")
		if err != nil {
			t.Fatal(err)
		}
		return g.Rules
	}
	uses := func() string {
		var out []string
		for _, r := range rules() {
			out = append(out, r.Use)
		}
		return strings.Join(out, " ")
	}
	if err := ruleCmd([]string{"opus"}); err != nil { // no rules yet: said, not an error
		t.Fatal(err)
	}
	if err := ruleCmd([]string{"nosuch"}); err == nil {
		t.Fatal("a word that is no group and no verb is an error")
	}
	if err := ruleCmd([]string{"add", "opus", "use=gpt-5.5", "tokens=200k"}); err != nil {
		t.Fatal(err)
	}
	if err := ruleCmd([]string{"opus"}); err != nil {
		t.Fatal(err)
	}
	if err := ruleCmd([]string{"add", "opus", "use=a/only-a", "images", "effort=HIGH", "agents=codex, claude-code"}); err != nil {
		t.Fatal(err)
	}
	if err := ruleCmd([]string{"add", "opus", "use=claude-opus-5-5", "thinking", "at=1"}); err != nil {
		t.Fatal(err)
	}
	rs := rules()
	if uses() != "a/claude-opus-5-5 b/gpt-5.5 a/only-a" || rs[0].Effort != "on" || rs[1].Tokens != 200000 ||
		!rs[2].Images || rs[2].Effort != "high" || strings.Join(rs[2].Agents, ",") != "codex,claude-code" {
		t.Fatalf("%+v", rs)
	}
	for _, bad := range [][]string{
		{"add", "opus", "tokens=5"},                // no model
		{"add", "opus", "use=a/m", "tokens=5"},     // not in the group
		{"add", "opus", "use=gpt-5.5"},             // no condition
		{"add", "opus", "use=gpt-5.5", "effort=x"}, // no such level
		{"add", "opus", "use=gpt-5.5", "images=maybe"},
		{"add", "opus", "use=gpt-5.5", "tokens=5", "color=red"},
		{"add", "opus", "use=gpt-5.5", "tokens=5", "at=0"},
		{"rm", "opus", "4"},
		{"rm", "opus", "0"},
		{"mv", "opus", "1", "9"},
		{"zap", "opus"},
		{"add", "nope", "use=gpt-5.5", "tokens=5"},
	} {
		if err := ruleCmd(bad); err == nil {
			t.Errorf("%v taken", bad)
		}
	}
	if len(rules()) != 3 {
		t.Fatalf("a refused rule changed them: %+v", rules())
	}
	if err := ruleCmd([]string{"mv", "opus", "1", "3"}); err != nil || uses() != "b/gpt-5.5 a/only-a a/claude-opus-5-5" {
		t.Fatalf("mv: %v %s", err, uses())
	}
	if err := ruleCmd([]string{"rm", "opus", "2"}); err != nil || uses() != "b/gpt-5.5 a/claude-opus-5-5" {
		t.Fatalf("rm: %v %s", err, uses())
	}
	// dropping a model from the group drops its rules
	if _, err := setGroup("opus", []string{"models-=b/gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	if uses() != "a/claude-opus-5-5" {
		t.Fatalf("after models-=: %s", uses())
	}
	// stored as the file keeps it
	gs := storedGroups(t)
	if len(gs) != 1 || len(gs[0]["rules"].([]any)) != 1 {
		t.Fatalf("stored: %v", gs)
	}
	// a found group gets rules and becomes the user's
	if err := ruleCmd([]string{"add", "auto-m", "use=b/vendor/m", "tokens=1k"}); err != nil {
		t.Fatal(err)
	}
	if g, _ := findGroup("auto-m"); g.Auto || len(g.Rules) != 1 {
		t.Fatalf("auto-m: %+v", g)
	}
}

// A rule with an intent, and the group's classifier with it.
func TestGroupRuleIntentCmd(t *testing.T) {
	groupsHome(t)
	if _, err := addGroup("Opus", []string{"models=a/claude-opus-5-5,b/gpt-5.5,a/only-a"}); err != nil {
		t.Fatal(err)
	}
	group := func() provider.Group {
		t.Helper()
		g, err := findGroup("opus")
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	for _, bad := range [][]string{
		{"add", "opus", "use=gpt-5.5", "intent=a quick question"},                         // no classifier
		{"add", "opus", "use=gpt-5.5", "intent=", "classifier=a/only-a"},                  // no words
		{"add", "opus", "use=gpt-5.5", "intent=a quick question", "classifier=z/nothing"}, // unknown model
		{"add", "opus", "use=gpt-5.5", "intent=a quick question", "classifier=group/opus"},
		{"classifier", "opus", "a/only-a"}, // no intent yet
	} {
		if err := ruleCmd(bad); err == nil {
			t.Errorf("%v taken", bad)
		}
	}
	if err := ruleCmd([]string{"add", "opus", "use=gpt-5.5", "intent=  a quick   question ", "classifier=magpie/a/only-a"}); err != nil {
		t.Fatal(err)
	}
	g := group()
	if len(g.Rules) != 1 || g.Rules[0].Intent != "a quick question" || g.Classifier != "a/only-a" {
		t.Fatalf("%+v", g)
	}
	// a second needs no classifier again
	if err := ruleCmd([]string{"add", "opus", "use=claude-opus-5-5", "use-effort=high", "intent=planning", "agents=codex"}); err != nil {
		t.Fatal(err)
	}
	if err := ruleCmd([]string{"classifier", "opus", "b/gpt-5.5"}); err != nil || group().Classifier != "b/gpt-5.5" {
		t.Fatalf("%v %+v", err, group())
	}
	// several, asked in order when one can't answer (qinzhidaoa on X)
	if err := ruleCmd([]string{"classifier", "opus", "b/gpt-5.5,a/only-a"}); err != nil || strings.Join(group().Classifiers(), ",") != "b/gpt-5.5,a/only-a" {
		t.Fatalf("%v %+v", err, group())
	}
	if err := ruleCmd([]string{"classifier", "opus", "a/only-a", "b/gpt-5.5"}); err != nil || strings.Join(group().Classifiers(), ",") != "a/only-a,b/gpt-5.5" {
		t.Fatalf("%v %+v", err, group())
	}
	if gs := storedGroups(t); gs[0]["classifierMore"] == nil {
		t.Fatalf("stored: %v", gs)
	}
	if err := ruleCmd([]string{"classifier", "opus", "b/gpt-5.5"}); err != nil || group().Classifier != "b/gpt-5.5" || group().ClassifierMore != nil {
		t.Fatalf("back to one: %v %+v", err, group())
	}
	if gs := storedGroups(t); gs[0]["classifierMore"] != nil {
		t.Fatalf("one stored: %v", gs)
	}
	if err := ruleCmd([]string{"opus"}); err != nil {
		t.Fatal(err)
	}
	if got := ruleLine(group().Rules[1]); !strings.Contains(got, `agent codex · intent "planning"`) || !strings.HasSuffix(got, "high") || group().Rules[1].UseEffort != "high" {
		t.Fatalf("%q", got)
	}
	// with the intents gone, so is the classifier
	for range 2 {
		if err := ruleCmd([]string{"rm", "opus", "1"}); err != nil {
			t.Fatal(err)
		}
	}
	if g := group(); len(g.Rules) != 0 || g.Classifier != "" {
		t.Fatalf("%+v", g)
	}
	if gs := storedGroups(t); gs[0]["classifier"] != nil {
		t.Fatalf("stored: %v", gs)
	}
}

func TestGroupMemberNames(t *testing.T) {
	g := provider.Group{ID: "x", Members: []string{"a/m", "b/vendor/m", "a/Big", "c/deepseek/deepseek-v4-pro"}}
	for in, want := range map[string]string{"a/m": "a/m", "magpie/a/m": "a/m", "A/BIG": "a/Big", "big": "a/Big", "vendor/m": "b/vendor/m",
		"deepseek-v4-pro": "c/deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-pro": "c/deepseek/deepseek-v4-pro"} {
		if got, err := groupMember(g, in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	// "m" is a's model by name before b's by its last name
	if got, err := groupMember(g, "m"); err != nil || got != "a/m" {
		t.Errorf("m: %q %v", got, err)
	}
	if _, err := groupMember(g, "gone"); err == nil || !strings.Contains(err.Error(), "a/m") {
		t.Errorf("gone: %v", err)
	}
}

// A rule naming several models (monkeyin_92 on X): use=a,b, in order; a
// model dropped from the group leaves the rule, which goes with its last.
func TestGroupRuleSeveralModelsCmd(t *testing.T) {
	groupsHome(t)
	if _, err := addGroup("Opus", []string{"models=a/claude-opus-5-5,b/gpt-5.5,a/only-a"}); err != nil {
		t.Fatal(err)
	}
	rule := func() []provider.Rule {
		t.Helper()
		g, err := findGroup("opus")
		if err != nil {
			t.Fatal(err)
		}
		return g.Rules
	}
	if err := ruleCmd([]string{"add", "opus", "use=gpt-5.5,only-a,claude-opus-5-5", "tokens=200k"}); err != nil {
		t.Fatal(err)
	}
	if rs := rule(); len(rs) != 1 || strings.Join(rs[0].Uses(), " ") != "b/gpt-5.5 a/only-a a/claude-opus-5-5" {
		t.Fatalf("%+v", rs)
	}
	if err := ruleCmd([]string{"opus"}); err != nil {
		t.Fatal(err)
	}
	if err := ruleCmd([]string{"add", "opus", "use=gpt-5.5,a/m", "tokens=5"}); err == nil {
		t.Fatal("a model not in the group taken")
	}
	if _, err := setGroup("opus", []string{"models-=b/gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	if rs := rule(); len(rs) != 1 || strings.Join(rs[0].Uses(), " ") != "a/only-a a/claude-opus-5-5" || rs[0].Tokens != 200000 {
		t.Fatalf("after models-=b: %+v", rs)
	}
	if _, err := setGroup("opus", []string{"models-=a/only-a,a/claude-opus-5-5", "models+=b/gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	if rs := rule(); len(rs) != 0 {
		t.Fatalf("a rule left with no model kept: %+v", rs)
	}
}

// A rule's hours typed with no zone are this computer's ($TZ here), kept
// as its IANA name so a server on another clock reads them as meant; a
// zone typed is kept, and one magpie doesn't know is refused.
func TestGroupRuleTimeZoneCmd(t *testing.T) {
	groupsHome(t)
	t.Setenv("TZ", "Asia/Shanghai")
	if _, err := addGroup("Peak", []string{"models=a/claude-opus-5-5,b/gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	for _, words := range [][]string{
		{"add", "peak", "pause=claude-opus-5-5", "time=09:00-18:00", "days=mon-fri"},
		{"add", "peak", "use=gpt-5.5", "time=22:00-08:00@Europe/Berlin"},
		{"add", "peak", "use=gpt-5.5", "days=sat", "tz=America/New_York"},
	} {
		if err := ruleCmd(words); err != nil {
			t.Fatalf("%v: %v", words, err)
		}
	}
	g, err := findGroup("peak")
	if err != nil {
		t.Fatal(err)
	}
	var zones []string
	for _, r := range g.Rules {
		zones = append(zones, r.Time.Zone)
	}
	if got := strings.Join(zones, " "); got != "Asia/Shanghai Europe/Berlin America/New_York" {
		t.Fatalf("zones %q", got)
	}
	if got := g.Rules[0].Line(); got != "pause=a/claude-opus-5-5 time=09:00-18:00 days=mon-fri tz=Asia/Shanghai" {
		t.Errorf("line %q", got)
	}
	for _, words := range [][]string{
		{"add", "peak", "use=gpt-5.5", "time=09:00-18:00", "tz=Beijing"},
		{"add", "peak", "use=gpt-5.5", "time=09:00-18:00@Asia/Shangai"},
		{"add", "peak", "use=gpt-5.5", "tz=Asia/Shanghai"},
	} {
		if err := ruleCmd(words); err == nil {
			t.Errorf("%v: saved", words)
		}
	}
	if g, _ = findGroup("peak"); len(g.Rules) != 3 {
		t.Errorf("%d rules after the refused ones", len(g.Rules))
	}
	// with no zone name to be had, none is kept: magpie's clock, as before
	t.Setenv("TZ", "UTC+8")
	if err := ruleCmd([]string{"add", "peak", "use=gpt-5.5", "time=01:00-02:00"}); err != nil {
		t.Fatal(err)
	}
	if g, _ = findGroup("peak"); g.Rules[3].Time.Zone != "" {
		t.Errorf("zone %q from TZ=UTC+8", g.Rules[3].Time.Zone)
	}
}
