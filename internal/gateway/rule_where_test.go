package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A rule's field/op/value conditions (aipha2025 on X) decide a turn
// through the gateway: each field, each op, on a request that meets it
// and one that doesn't.
func TestRuleWhereDecides(t *testing.T) {
	type req struct {
		name, text, extra string
		headers           map[string]string
		want              string // the key that answers: kb when the rule sent it to b/big
	}
	for _, tc := range []struct {
		field string
		conds []provider.Cond
		reqs  []req
	}{
		{"tokens <", []provider.Cond{{Field: "tokens", Op: "lt", Value: "2000"}}, []req{
			{name: "short", text: "hi", want: "kb"},
			{name: "long", text: long(5000), want: "ka"},
		}},
		{"tokens >", []provider.Cond{{Field: "tokens", Op: "gt", Value: "2000"}}, []req{
			{name: "short", text: "hi", want: "ka"},
			{name: "long", text: long(5000), want: "kb"},
		}},
		{"effort is not off", []provider.Cond{{Field: "effort", Op: "not", Value: "off"}}, []req{
			{name: "medium", text: "a", extra: `,"reasoning_effort":"medium"`, want: "kb"},
			{name: "none", text: "b", extra: `,"reasoning_effort":"none"`, want: "ka"},
			{name: "unasked", text: "c", want: "ka"},
		}},
		{"effort is off", []provider.Cond{{Field: "effort", Op: "is", Value: "off"}}, []req{
			{name: "none", text: "a", extra: `,"reasoning_effort":"none"`, want: "kb"},
			{name: "low", text: "b", extra: `,"reasoning_effort":"low"`, want: "ka"},
		}},
		{"effort ≤ medium", []provider.Cond{{Field: "effort", Op: "lte", Value: "medium"}}, []req{
			{name: "low", text: "a", extra: `,"reasoning_effort":"low"`, want: "kb"},
			{name: "high", text: "b", extra: `,"reasoning_effort":"high"`, want: "ka"},
		}},
		{"agent is not", []provider.Cond{{Field: "agent", Op: "not", Value: "ruletester"}}, []req{
			{name: "other", text: "a", headers: map[string]string{"User-Agent": "other/1.0"}, want: "kb"},
			{name: "tester", text: "b", headers: map[string]string{"User-Agent": "RuleTester/1.0"}, want: "ka"},
		}},
		{"subagent", []provider.Cond{{Field: "subagent", Op: "is"}}, []req{
			{name: "claude code's", text: "a", headers: map[string]string{"x-claude-code-agent-id": "explore-1"}, want: "kb"},
			{name: "codex's", text: "b", headers: map[string]string{"User-Agent": "codex_cli_rs/0.99.0", "x-openai-subagent": "collab_spawn"}, want: "kb"},
			{name: "a lead's", text: "c", want: "ka"},
			{name: "another kind", text: "d", headers: map[string]string{"User-Agent": "codex_cli_rs/0.99.0", "x-openai-subagent": "review"}, want: "ka"},
		}},
		{"not subagent", []provider.Cond{{Field: "subagent", Op: "not"}}, []req{
			{name: "claude code's", text: "a", headers: map[string]string{"x-claude-code-agent-id": "explore-1"}, want: "ka"},
			{name: "a lead's", text: "b", want: "kb"},
		}},
		{"message contains", []provider.Cond{{Field: "message", Op: "contains", Value: "run the tests"}}, []req{
			{name: "has it", text: "Please RUN the tests now", want: "kb"},
			{name: "hasn't", text: "write the docs", want: "ka"},
		}},
		{"message doesn't contain", []provider.Cond{{Field: "message", Op: "excludes", Value: "tests"}}, []req{
			{name: "has it", text: "fix the tests", want: "ka"},
			{name: "hasn't", text: "write the docs", want: "kb"},
		}},
		{"message starts with", []provider.Cond{{Field: "message", Op: "starts", Value: "/review"}}, []req{
			{name: "starts", text: "/review this diff", want: "kb"},
			{name: "within", text: "please /review", want: "ka"},
		}},
		{"message matches", []provider.Cond{{Field: "message", Op: "matches", Value: `^translate\b`}}, []req{
			{name: "matches", text: "Translate this to French", want: "kb"},
			{name: "doesn't", text: "don't translate", want: "ka"},
		}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			s, _, _ := ruled(t, provider.Rule{Use: "b/big", Where: tc.conds})
			for _, q := range tc.reqs {
				r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chat(q.text, nil, 0, q.extra)))
				for k, v := range q.headers {
					r.Header.Set(k, v)
				}
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, r)
				hit := lastRoute(s).Rule
				n := 0
				if q.want == "kb" {
					n = 1
				}
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from "+q.want) || hit == nil || hit.N != n {
					t.Errorf("%s: %d %s %+v", q.name, rec.Code, rec.Body.String(), hit)
				}
			}
		})
	}
}

// The message a rule reads is the user's own words as the turn began:
// a tool round of the turn stays where the turn went, and what an agent
// adds to the message for the model is not the user's.
func TestRuleMessageIsTheUsers(t *testing.T) {
	s, _, _ := ruled(t, provider.Rule{Use: "b/big", Where: []provider.Cond{{Field: "message", Op: "contains", Value: "deploy"}}})
	for _, tc := range []struct {
		name, body, want string
	}{
		{"begins", chat("deploy it", nil, 0, ""), "kb"},
		{"its tool round", chat("deploy it", nil, 1, ""), "kb"},
		{"a reminder says it", chat("<system-reminder>deploy</system-reminder>\nwrite docs", nil, 0, ""), "ka"},
	} {
		_, out := postAs(t, s, "sess-msg", tc.body)
		if !strings.Contains(out, "from "+tc.want) {
			t.Errorf("%s: %s %+v", tc.name, out, lastRoute(s).Rule)
		}
	}
}
