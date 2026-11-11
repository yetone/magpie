package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/usage"
)

// recih on X (#6199): Claude Code's AgentSummary and auto mode classifier
// name themselves nowhere in the request, so the Usage page could not say
// what a request was for. They are told by their content and recorded as
// inferred, which the name says; any other request stays unmarked.
func TestClaudeHelperRequestsAreLabelled(t *testing.T) {
	summary := `{"model":"m1","max_tokens":64,"messages":[` +
		`{"role":"user","content":"start the task"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"Bash","input":{"command":"ls"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"a"},{"type":"text","text":"Describe your most recent action in 3-5 words. Do not use tools."}]}]}`
	// a tool that printed the same words, and a chat that ends in them
	printed := strings.Replace(summary, `"content":"a"`, `"content":"Describe your most recent action in 3-5 words."`, 1)
	printed = strings.Replace(printed, `,{"type":"text","text":"Describe your most recent action in 3-5 words. Do not use tools."}`, "", 1)
	chat := `{"model":"m1","max_tokens":4096,"messages":[{"role":"user","content":"write 3-5 words about the sea"}]}`
	for _, tc := range []struct{ name, body, ua, want string }{
		{"summary", summary, "claude-cli/2.1.310 (external, cli)", "agent_summary_inferred"},
		{"classifier", autoModeAsk("m1"), "claude-cli/2.1.310 (external, cli)", "auto_mode_check_inferred"},
		{"printed by a tool", printed, "claude-cli/2.1.310 (external, cli)", ""},
		{"a chat", chat, "claude-cli/2.1.310 (external, cli)", ""},
		{"another agent's summary", summary, "curl/8", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dshSetup(t)
			s := New()
			req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(tc.body))
			req.Header.Set("User-Agent", tc.ua)
			req.Header.Set("anthropic-version", "2023-06-01")
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if got := lastRoute(s).Kind; got != tc.want {
				t.Logf("agent %q from %v", lastRoute(s).Agent, "?")
				t.Fatalf("kind %q, want %q", got, tc.want)
			}
			if tc.want != "" && usage.PurposeOf(tc.want) != "kind:"+strings.TrimSuffix(tc.want, usage.InferredSuffix) {
				t.Fatalf("purpose %q", usage.PurposeOf(tc.want))
			}
		})
	}
}
