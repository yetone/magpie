package gateway

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

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

// Seapawn1 on X: GPT in Claude Code loses its prompt cache with tool
// search on. A tool ToolSearch loads joins the request's tools on a Chat
// route, before every message, so the start of the prompt changed at each
// load. Such a route turns the deferred tools away with a 400 naming
// defer_loading, which Claude Code answers with every tool declared in
// full; a route that loads in place, an Anthropic one, a list too long to
// declare and a request without Claude Code's placeholder go as before.
func TestDeferredToolsTurnedAwayWhereTheyLoseTheCache(t *testing.T) {
	ok := sse(
		`data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
		`data: {"id":"c1","model":"m1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`,
		`data: [DONE]`)
	f := &fake{t: t, reply: ok}
	setup(t, provider.Chat, f)

	body := ccSearchRequest(ccDeferredNames, tsPlaceholder)
	code, out := post(t, "/v1/messages", body)
	if code != 400 || !strings.Contains(out, "defer_loading") || f.calls != 0 {
		t.Fatalf("Chat route: status %d, %d upstream calls: %s", code, f.calls, out)
	}
	if gjson.Get(out, "type").String() != "error" || gjson.Get(out, "error.type").String() != "invalid_request_error" {
		t.Fatalf("not an Anthropic invalid_request_error: %s", out)
	}
	// the beta header named would make Claude Code drop tool search for
	// every conversation, a Claude model's too
	if strings.Contains(out, "anthropic-beta") || strings.Contains(out, "tool-search") || strings.Contains(out, "advanced-tool-use") {
		t.Fatalf("the refusal names the beta: %s", out)
	}

	// Claude Code's retry: every tool declared, no placeholder
	full := ccSearchRequest(ccDeferredNames, tsReadA, tsReadB)
	if code, out := post(t, "/v1/messages", full); code != 200 {
		t.Fatalf("the retry with every tool declared: status %d: %s", code, out)
	}
	if n := len(gjson.GetBytes(f.got, "tools").Array()); n != 4 {
		t.Fatalf("the retry reached the upstream with %d tools: %s", n, f.got)
	}

	// hundreds of deferred tools, an MCP router's, keep tool search
	many := make([]string, 300)
	for i := range many {
		many[i] = fmt.Sprintf("mcp__router__t%d", i)
	}
	f.calls = 0
	if code, out := post(t, "/v1/messages", ccSearchRequest(many, tsPlaceholder)); code != 200 || f.calls != 1 {
		t.Fatalf("300 deferred tools: status %d, %d calls: %s", code, f.calls, out)
	}
	// no reminder to count from: unknown, so tool search stays
	f.calls = 0
	if code, out := post(t, "/v1/messages", tsRequests[1]); code != 200 || f.calls != 1 {
		t.Fatalf("no reminder: status %d, %d calls: %s", code, f.calls, out)
	}
}

// A Responses upstream that takes additional_tools keeps tool search: the
// tools it loads go in place, and the prefix holds (#2660).
func TestDeferredToolsKeptWhereLoadedInPlace(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
		`data: {"type":"response.completed","response":{"id":"r1","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`)}
	setup(t, provider.Responses, f)
	body := ccSearchRequest(ccDeferredNames, tsPlaceholder)
	if code, out := post(t, "/v1/messages", body); code != 400 || !strings.Contains(out, "defer_loading") {
		t.Fatalf("a Responses relay without additional_tools: status %d: %s", code, out)
	}
	p, err := provider.Find("fake")
	if err != nil {
		t.Fatal(err)
	}
	p.AdditionalTools = true
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if code, out := post(t, "/v1/messages", body); code != 200 {
		t.Fatalf("a relay that takes additional_tools: status %d: %s", code, out)
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

func TestDeferredCount(t *testing.T) {
	r, err := parseAnthropic([]byte(ccSearchRequest(ccDeferredNames, tsPlaceholder)))
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := deferredCount(r); !ok || n != len(ccDeferredNames) {
		t.Fatalf("count = %d %v, want %d", n, ok, len(ccDeferredNames))
	}
	if !r.DeferredNamed {
		t.Fatal("the placeholder was not seen")
	}
	// the refusal is about the request: a group doesn't fall to its next
	// member for it, nor rest the account
	if body := []byte(writeErrorBody(deferredRefusal)); retryable(400, body) || shapeRefused(400, body) {
		t.Fatal("the refusal reads as a member's fault")
	}
}

func writeErrorBody(msg string) string {
	rec := httptest.NewRecorder()
	writeError(rec, provider.Anthropic, 400, msg)
	return rec.Body.String()
}
