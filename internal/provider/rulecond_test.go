package provider

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// Each field with each of its ops, against requests on both sides of it
// (aipha2025 on X: field, op, value).
func TestCondHolds(t *testing.T) {
	off := RuleRequest{Off: true, Effort: "low"} // effort none, which Effort reads as low
	none := RuleRequest{}                        // no reasoning asked
	on := RuleRequest{Thinking: true}            // reasoning at no stated level
	med := RuleRequest{Thinking: true, Effort: "medium"}
	high := RuleRequest{Effort: "high"}
	for _, tc := range []struct {
		c    Cond
		q    RuleRequest
		want bool
	}{
		{Cond{"tokens", "gte", "100"}, RuleRequest{Tokens: 100}, true},
		{Cond{"tokens", "gte", "100"}, RuleRequest{Tokens: 99}, false},
		{Cond{"tokens", "gt", "100"}, RuleRequest{Tokens: 100}, false},
		{Cond{"tokens", "gt", "100"}, RuleRequest{Tokens: 101}, true},
		{Cond{"tokens", "lt", "100"}, RuleRequest{Tokens: 99}, true},
		{Cond{"tokens", "lt", "100"}, RuleRequest{Tokens: 100}, false},
		{Cond{"tokens", "lte", "100"}, RuleRequest{Tokens: 100}, true},
		{Cond{"tokens", "lte", "100"}, RuleRequest{Tokens: 101}, false},

		{Cond{"effort", "is", "off"}, off, true},
		{Cond{"effort", "is", "off"}, none, true},
		{Cond{"effort", "is", "off"}, on, false},
		{Cond{"effort", "is", "on"}, on, true},
		{Cond{"effort", "is", "on"}, off, false},
		{Cond{"effort", "is", "medium"}, med, true},
		{Cond{"effort", "is", "medium"}, high, false},
		{Cond{"effort", "is", "low"}, off, false}, // off is not low, though Effort reads it so
		{Cond{"effort", "not", "off"}, on, true},
		{Cond{"effort", "not", "off"}, off, false},
		{Cond{"effort", "not", "high"}, med, true},
		{Cond{"effort", "not", "high"}, high, false},
		{Cond{"effort", "not", "high"}, none, true},
		{Cond{"effort", "gte", "medium"}, med, true},
		{Cond{"effort", "gte", "medium"}, high, true},
		{Cond{"effort", "gte", "medium"}, RuleRequest{Effort: "low"}, false},
		{Cond{"effort", "gte", "low"}, off, false},
		{Cond{"effort", "gte", "low"}, on, false},
		{Cond{"effort", "lte", "medium"}, med, true},
		{Cond{"effort", "lte", "medium"}, high, false},
		{Cond{"effort", "lte", "low"}, off, true},
		{Cond{"effort", "lte", "low"}, none, true},
		{Cond{"effort", "lte", "max"}, on, false}, // a level not stated is not known to be at most one

		{Cond{"agent", "is", "codex,opencode"}, RuleRequest{Agent: "OpenCode"}, true},
		{Cond{"agent", "is", "codex,opencode"}, RuleRequest{Agent: "claude-code"}, false},
		{Cond{"agent", "not", "codex"}, RuleRequest{Agent: "claude-code"}, true},
		{Cond{"agent", "not", "codex"}, RuleRequest{Agent: "codex"}, false},
		{Cond{"agent", "not", "codex"}, RuleRequest{}, true},

		{Cond{"subagent", "is", ""}, RuleRequest{Subagent: true}, true},
		{Cond{"subagent", "is", ""}, RuleRequest{}, false},
		{Cond{"subagent", "not", ""}, RuleRequest{}, true},
		{Cond{"subagent", "not", ""}, RuleRequest{Subagent: true}, false},

		{Cond{"message", "contains", "Run The Tests"}, RuleRequest{Message: "please run the tests now"}, true},
		{Cond{"message", "contains", "tests"}, RuleRequest{Message: "write docs"}, false},
		{Cond{"message", "excludes", "tests"}, RuleRequest{Message: "write docs"}, true},
		{Cond{"message", "excludes", "tests"}, RuleRequest{Message: "fix TESTS"}, false},
		{Cond{"message", "starts", "/review"}, RuleRequest{Message: "  /Review this"}, true},
		{Cond{"message", "starts", "/review"}, RuleRequest{Message: "please /review"}, false},
		{Cond{"message", "matches", `^fix .*bug$`}, RuleRequest{Message: "Fix the login BUG"}, true},
		{Cond{"message", "matches", `^fix .*bug$`}, RuleRequest{Message: "a bug to fix"}, false},
		{Cond{"message", "contains", "翻译"}, RuleRequest{Message: "帮我翻译这段"}, true},

		// the system prompt's blocks and the last user message's texts
		// (SucroseMoE on X): one of them holds it
		{Cond{"system", "matches", `^你是对话标题命名`}, RuleRequest{System: []string{"x-anthropic-billing-header: cc_version=2", "你是对话标题命名助手，"}}, true},
		{Cond{"system", "matches", `^你是对话标题命名`}, RuleRequest{System: []string{"请注意：你是对话标题命名助手"}}, false},
		{Cond{"system", "matches", `^你是对话标题命名`}, RuleRequest{Message: "你是对话标题命名", Last: []string{"你是对话标题命名"}}, false},
		{Cond{"system", "starts", "You are"}, RuleRequest{System: []string{"billing", "  you are a title namer"}}, true},
		{Cond{"system", "contains", "title"}, RuleRequest{System: []string{"a", "Make a TITLE"}}, true},
		{Cond{"system", "excludes", "title"}, RuleRequest{System: []string{"a", "Make a TITLE"}}, false},
		{Cond{"system", "excludes", "title"}, RuleRequest{System: []string{"a", "b"}}, true},
		{Cond{"system", "excludes", "title"}, RuleRequest{}, true},
		{Cond{"system", "contains", "title"}, RuleRequest{}, false},
		{Cond{"last", "matches", `^正在上下文压缩`}, RuleRequest{Last: []string{"<system-reminder>x</system-reminder>", "正在上下文压缩，请总结"}}, true},
		{Cond{"last", "matches", `^正在上下文压缩`}, RuleRequest{Message: "正在上下文压缩", System: []string{"正在上下文压缩"}}, false},
		{Cond{"last", "starts", "正在"}, RuleRequest{Last: []string{"好的，正在"}}, false},

		{Cond{"weather", "is", "rain"}, RuleRequest{}, false}, // one this magpie doesn't know
		{Cond{"tokens", "eq", "1"}, RuleRequest{Tokens: 1}, false},
	} {
		if got := tc.c.Holds(tc.q); got != tc.want {
			t.Errorf("%s on %+v: %v, want %v", tc.c.Text(), tc.q, got, tc.want)
		}
	}
}

// A rule of conditions alone routes by them; every one must hold, with
// the rule's own fields too.
func TestRuleWhereMatches(t *testing.T) {
	r := Rule{Use: "a/m", Tokens: 1000, Where: []Cond{{"effort", "not", "off"}, {"agent", "not", "codex"}}}
	if !r.Matches(RuleRequest{Tokens: 2000, Thinking: true, Agent: "claude-code"}) {
		t.Error("all hold: no match")
	}
	for _, q := range []RuleRequest{
		{Tokens: 500, Thinking: true, Agent: "claude-code"}, // its tokens field
		{Tokens: 2000, Agent: "claude-code"},                // reasoning off
		{Tokens: 2000, Thinking: true, Agent: "codex"},      // codex
	} {
		if r.Matches(q) {
			t.Errorf("%+v matched", q)
		}
	}
	short := Rule{Use: "a/m", Where: []Cond{{"tokens", "lt", "20000"}}}
	if !short.Matches(RuleRequest{Tokens: 19999}) || short.Matches(RuleRequest{Tokens: 20000}) {
		t.Error("tokens < 20000")
	}
	if MatchRule([]Rule{short, {Use: "b/big", Tokens: 1}}, RuleRequest{Tokens: 30000}) != 1 {
		t.Error("the second rule is the first that matches")
	}
}

// A rule saved before Where reads, routes and shows the same: no where in
// its JSON, the same conditions, the same line.
func TestRuleWithoutWhereUnchanged(t *testing.T) {
	old := `{"use":"b/big","tokens":200000,"effort":"high","agents":["claude-code"]}`
	var r Rule
	if err := json.Unmarshal([]byte(old), &r); err != nil {
		t.Fatal(err)
	}
	got, err := cleanRules([]Rule{r}, []string{"a/m", "b/big"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got[0])
	if string(b) != old {
		t.Errorf("%s, want %s", b, old)
	}
	if c := strings.Join(got[0].Conditions(), "; "); c != "tokens ≥ 200000; effort ≥ high; agent claude-code" {
		t.Errorf("conditions %q", c)
	}
	if l := got[0].Line(); l != "use=b/big tokens=200000 effort=high agents=claude-code" {
		t.Errorf("line %q", l)
	}
}

func TestCleanRulesWhere(t *testing.T) {
	members := []string{"a/m", "b/big"}
	for _, tc := range []struct {
		c   Cond
		err string
	}{
		{Cond{"weather", "is", "x"}, "looks at one of"},
		{Cond{"tokens", "is", "5"}, "a tokens condition is"},
		{Cond{"tokens", "lt", "lots"}, "not a length"},
		{Cond{"effort", "gte", "on"}, "at least or at most"},
		{Cond{"effort", "is", "huge"}, "effort is off, on or"},
		{Cond{"agent", "not", " , "}, "names the agents"},
		{Cond{"message", "contains", "  "}, "needs the text"},
		{Cond{"message", "contains", strings.Repeat("x", MaxCondText+1)}, "at most"},
		{Cond{"message", "matches", "(unclosed"}, "not a regular expression"},
		{Cond{"system", "matches", "(unclosed"}, "system matches"},
		{Cond{"last", "contains", " "}, "a last condition needs the text"},
		{Cond{"system", "is", "x"}, "a system condition is"},
	} {
		_, err := cleanRules([]Rule{{Use: "a/m", Where: []Cond{tc.c}}}, members)
		if err == nil || !strings.Contains(err.Error(), tc.err) || !strings.HasPrefix(err.Error(), "rule 1:") {
			t.Errorf("%+v: %v, want …%s", tc.c, err, tc.err)
		}
	}
	got, err := cleanRules([]Rule{{Use: "a/m", Where: []Cond{
		{" Tokens ", " LT ", "20k"}, {"agent", "not", "Codex, opencode,codex"}, {"effort", "Is", " OFF "}, {"subagent", "is", "ignored"}, {"message", "contains", "  run tests "},
	}}}, members)
	if err != nil {
		t.Fatal(err)
	}
	want := []Cond{{"tokens", "lt", "20000"}, {"agent", "not", "codex,opencode"}, {"effort", "is", "off"}, {"subagent", "is", ""}, {"message", "contains", "run tests"}}
	if !slices.Equal(got[0].Where, want) {
		t.Errorf("%+v", got[0].Where)
	}
	// a pause holds by agents among these, never by what a request asks
	if _, err := cleanRules([]Rule{{Use: "a/m", Pause: true, Where: []Cond{{"message", "contains", "x"}}}}, members); err == nil || !strings.Contains(err.Error(), "a pause holds by") {
		t.Errorf("pause on a message: %v", err)
	}
	p, err := cleanRules([]Rule{{Use: "a/m", Pause: true, Where: []Cond{{"agent", "not", "codex"}}}}, members)
	if err != nil {
		t.Fatal(err)
	}
	if !p[0].Pauses("claude-code", "", time.Now()) || p[0].Pauses("codex", "", time.Now()) {
		t.Error("a pause for every agent but codex")
	}
}

func TestParseCond(t *testing.T) {
	for in, want := range map[string]Cond{
		"effort is not off":                {"effort", "not", "off"},
		"effort != high":                   {"effort", "not", "high"},
		"effort at least medium":           {"effort", "gte", "medium"},
		"effort ≤ low":                     {"effort", "lte", "low"},
		"reasoning is on":                  {"effort", "is", "on"},
		"tokens < 20k":                     {"tokens", "lt", "20000"},
		"tokens >= 1m":                     {"tokens", "gte", "1000000"},
		"tokens >200k":                     {"tokens", "gt", "200000"},
		"agent is not codex,opencode":      {"agent", "not", "codex,opencode"},
		"agents is claude-code":            {"agent", "is", "claude-code"},
		"subagent":                         {"subagent", "is", ""},
		"not subagent":                     {"subagent", "not", ""},
		"message contains run the tests":   {"message", "contains", "run the tests"},
		"message does not contain tests":   {"message", "excludes", "tests"},
		"content starts with /review":      {"message", "starts", "/review"},
		`message matches ^fix\b`:           {"message", "matches", `^fix\b`},
		"message contains is not a thing ": {"message", "contains", "is not a thing"},
		"system matches ^你是对话标题命名":         {"system", "matches", "^你是对话标题命名"},
		"system[0] matches /^你是对话标题命名/":    {"system", "matches", "^你是对话标题命名"},
		"user[-1] matches /^正在上下文压缩/":      {"last", "matches", "^正在上下文压缩"},
		"last starts with 正在上下文压缩":         {"last", "starts", "正在上下文压缩"},
		"system does not contain title":    {"system", "excludes", "title"},
	} {
		got, err := ParseCond(in)
		if err != nil || got != want {
			t.Errorf("%q: %+v %v, want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "weather is rain", "tokens is 5", "effort is", "message", "agent likes codex", "effort isoff"} {
		if c, err := ParseCond(bad); err == nil {
			t.Errorf("%q: %+v", bad, c)
		}
	}
}

// when= reads as ParseRule's other words do, and Line writes it back.
func TestParseRuleWhen(t *testing.T) {
	g := Group{ID: "g:r", Members: []string{"a/m", "b/big"}}
	r, _, _, err := ParseRule(g, RuleWords(`use=big when="effort is not off" when="message contains run the tests" when=subagent tokens=10k`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Cond{{"effort", "not", "off"}, {"message", "contains", "run the tests"}, {"subagent", "is", ""}}
	if r.Use != "b/big" || r.Tokens != 10000 || !slices.Equal(r.Where, want) {
		t.Fatalf("%+v", r)
	}
	line := r.Line()
	if line != `use=b/big tokens=10000 when="effort is not off" when="message contains run the tests" when=subagent` {
		t.Errorf("line %q", line)
	}
	back, _, _, err := ParseRule(g, RuleWords(line))
	if err != nil || !slices.Equal(back.Where, want) {
		t.Errorf("read back: %+v %v", back.Where, err)
	}
	if _, _, _, err := ParseRule(g, RuleWords(`pause=m when="message contains x"`)); err == nil {
		t.Error("a pause on a message")
	}
	p, _, _, err := ParseRule(g, RuleWords(`pause=m when="agent is not codex"`))
	if err != nil || !p.Pause || len(p.Where) != 1 {
		t.Errorf("a pause on agents: %+v %v", p, err)
	}
}

// Conditions shows each as a list and a trace do.
func TestWhereConditions(t *testing.T) {
	r := Rule{Use: "a/m", Where: []Cond{{"tokens", "lt", "20000"}, {"effort", "not", "off"}, {"agent", "not", "codex,opencode"}, {"subagent", "not", ""}, {"message", "excludes", "x"}, {"system", "matches", "^你是"}, {"last", "starts", "正在"}}}
	got := strings.Join(r.Conditions(), "; ")
	if got != `tokens < 20000; effort is not off; agent is not codex|opencode; not subagent; message doesn't contain "x"; system matches "^你是"; last starts with "正在"` {
		t.Errorf("%q", got)
	}
}
