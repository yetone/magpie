package gateway

import (
	"encoding/json"
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

// A rule's system and last conditions (SucroseMoE on X) read the
// request's texts as the agent sent them, with no model asked: each block
// of its system prompt, in every protocol, and each text of its last user
// message. A harness's title or compaction call is told apart by its
// prompt alone.
func TestRuleSystemAndLastText(t *testing.T) {
	asGroup := func(body []byte) string {
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatal(err)
		}
		m["model"], m["stream"] = "group/r", false
		delete(m, "thinking")
		delete(m, "reasoning")
		b, _ := json.Marshal(m)
		return string(b)
	}
	title := `{"model":"group/r","messages":[{"role":"system","content":"你是对话标题命名助手，用不超过十个字概括对话。"},{"role":"user","content":"帮我写一个排序函数"}]}`
	titleAnthropic := `{"model":"group/r","max_tokens":64,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.296"},{"type":"text","text":"你是对话标题命名助手，用不超过十个字概括对话。"}],"messages":[{"role":"user","content":"帮我写一个排序函数"}]}`
	titleResponses := `{"model":"group/r","instructions":"你是对话标题命名助手。","input":[{"role":"user","content":"帮我写一个排序函数"}]}`
	titleDeveloper := `{"model":"group/r","input":[{"role":"developer","content":[{"type":"input_text","text":"你是对话标题命名助手。"}]},{"role":"user","content":"帮我写一个排序函数"}]}`
	said := `{"model":"group/r","messages":[{"role":"system","content":"You are a coding agent."},{"role":"user","content":"你是对话标题命名助手吗？"}]}`
	compact := `{"model":"group/r","messages":[{"role":"system","content":"You are a coding agent."},{"role":"user","content":"帮我写一个排序函数"},{"role":"assistant","content":"好的"},{"role":"user","content":[{"type":"text","text":"<system-reminder>todo</system-reminder>"},{"type":"text","text":"正在上下文压缩，请总结以上对话。"}]}]}`
	carryOn := `{"model":"group/r","messages":[{"role":"system","content":"You are a coding agent."},{"role":"user","content":"正在上下文压缩，请总结以上对话。"},{"role":"assistant","content":"好的"},{"role":"user","content":"接着写测试"}]}`
	for _, tc := range []struct {
		name string
		cond provider.Cond
		path string
		body string
		want string
	}{
		{"chat system", provider.Cond{Field: "system", Op: "matches", Value: `/^你是对话标题命名/`}, "/v1/chat/completions", title, "kb"},
		{"anthropic's second block", provider.Cond{Field: "system", Op: "matches", Value: `^你是对话标题命名`}, "/v1/messages", titleAnthropic, "kb"},
		{"responses instructions", provider.Cond{Field: "system", Op: "matches", Value: `^你是对话标题命名`}, "/v1/responses", titleResponses, "kb"},
		{"responses developer", provider.Cond{Field: "system", Op: "starts", Value: "你是对话标题命名"}, "/v1/responses", titleDeveloper, "kb"},
		{"the user said it", provider.Cond{Field: "system", Op: "matches", Value: `^你是对话标题命名`}, "/v1/chat/completions", said, "ka"},
		{"claude code's own", provider.Cond{Field: "system", Op: "matches", Value: `^You are a Claude agent`}, "/v1/messages", asGroup(fixture(t, "claude-code-2.1.296.json")), "kb"},
		{"codex's own", provider.Cond{Field: "system", Op: "matches", Value: `^You are a coding agent running in the Codex CLI`}, "/v1/responses", asGroup(fixture(t, "codex-0.162.0.json")), "kb"},
		{"not in pi's", provider.Cond{Field: "system", Op: "contains", Value: "你是对话标题命名"}, "/v1/chat/completions", asGroup(fixture(t, "pi-1.1.0.json")), "ka"},
		{"compaction last", provider.Cond{Field: "last", Op: "matches", Value: `/^正在上下文压缩/`}, "/v1/chat/completions", compact, "kb"},
		{"compaction earlier", provider.Cond{Field: "last", Op: "matches", Value: `^正在上下文压缩`}, "/v1/chat/completions", carryOn, "ka"},
		{"system excludes", provider.Cond{Field: "system", Op: "excludes", Value: "标题"}, "/v1/chat/completions", compact, "kb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := ruled(t, provider.Rule{Use: "b/big", Where: []provider.Cond{tc.cond}})
			rec := httptest.NewRecorder()
			r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			r.Header.Set("x-session-id", tc.name)
			s.Handler().ServeHTTP(rec, r)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "from "+tc.want) {
				t.Errorf("%d %.300s %+v", rec.Code, rec.Body.String(), lastRoute(s).Rule)
			}
		})
	}
}
