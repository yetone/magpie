package gateway

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// ccDeferred is Claude Code 2.1.327's reminder naming its deferred tools,
// as a sandboxed run with three MCP tools sent it.
func ccDeferred(names ...string) string {
	return "The following deferred tools are now available via ToolSearch. Their schemas are NOT loaded — calling them directly will fail with InputValidationError. Use ToolSearch with query \"select:<name>[,<name>...]\" to load tool schemas before calling them:\n" +
		strings.Join(names, "\n") + "\n\nAvailable agent types for the Agent tool:\n- claude: Catch-all"
}

var ccDeferredNames = []string{"CronCreate", "CronDelete", "CronList", "EnterWorktree", "ExitWorktree", "Monitor", "NotebookEdit", "SendMessage", "TaskStop", "WebFetch", "WebSearch", "mcp__fk__alpha", "mcp__fk__beta", "mcp__fk__gamma"}

func ccSearchRequest(names []string, tools ...string) string {
	reminder := fmt.Sprintf(`{"role":"user","content":[{"type":"text","text":%q},{"type":"text","text":"hi"}]}`, ccDeferred(names...))
	return tsRequest(append([]string{tsTools0}, tools...), reminder)
}

// lightwlh on X: Claude Code's ToolSearch stopped working on magpie, every
// tool declared up front, on a routing group (group/group) whose members
// answer on Chat. magpie turned Claude Code's deferred tools away there
// with a 400 naming defer_loading, which Claude Code 2.1.327 remembers as
// "Tool search is off for this conversation: the endpoint refused it
// earlier in this process". Tool search is Claude Code's to keep: a
// request with deferred tools, Claude Code's reminder naming them and a
// tool ToolSearch loaded is served on every route, its placeholder never
// sent, the loaded tool declared as a plain tool.
func TestDeferredToolsServedOnEveryRoute(t *testing.T) {
	chat := sse(
		`data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
		`data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`,
		`data: [DONE]`)
	responses := sse(
		`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
		`data: {"type":"response.completed","response":{"id":"r1","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`)
	for _, c := range []struct {
		name  string
		proto provider.Protocol
		reply string
	}{{"chat", provider.Chat, chat}, {"responses relay", provider.Responses, responses}} {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{t: t, reply: c.reply}
			setup(t, c.proto, f)
			// the first request: nothing loaded yet; then one with read_a
			// loaded by ToolSearch
			first := ccSearchRequest(ccDeferredNames, tsPlaceholder)
			loaded := tsRequest([]string{tsTools0, tsReadA, tsPlaceholder},
				fmt.Sprintf(`{"role":"user","content":[{"type":"text","text":%q},{"type":"text","text":"hi"}]}`, ccDeferred(ccDeferredNames...)), tsSearchA)
			for i, body := range []string{first, loaded} {
				f.calls = 0
				code, out := post(t, "/v1/messages", body)
				if code != 200 || f.calls != 1 {
					t.Fatalf("request %d: status %d, %d upstream calls: %s", i, code, f.calls, out)
				}
				if strings.Contains(out, "defer_loading") {
					t.Fatalf("request %d turned away: %s", i, out)
				}
				sent := string(f.got)
				if strings.Contains(sent, "DeferredToolPlaceholder") || strings.Contains(sent, `"defer_loading"`) {
					t.Fatalf("request %d sent the placeholder or defer_loading: %s", i, sent)
				}
				if i == 1 && !strings.Contains(sent, "mcp__probe__read_a") {
					t.Fatalf("the loaded tool was not declared: %s", sent)
				}
			}
		})
	}
}

// A Claude model on Anthropic's API keeps tool search.
func TestDeferredToolsKeptOnAnthropic(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`}
	setup(t, provider.Anthropic, f)
	if code, out := post(t, "/v1/messages", ccSearchRequest(ccDeferredNames, tsPlaceholder)); code != 200 || !strings.Contains(string(f.got), "DeferredToolPlaceholder") {
		t.Fatalf("status %d: %s\nsent %s", code, out, f.got)
	}
}
