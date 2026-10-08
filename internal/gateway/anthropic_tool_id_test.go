package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// Claude Code validates tool_use ids against Anthropic's charset
// ([a-zA-Z0-9_-]) and drops the whole block on any other byte. An upstream
// that invents its own call ids - devin/swe-2 sends "Bash:0#a65b…" - then
// breaks every local tool call. The id is sanitized at the Anthropic
// boundary; the client echoes the sanitized id back as tool_use_id, so both
// sides still match.
func TestToolUseIDCharset(t *testing.T) {
	const raw = "Bash:0#a65b6a5e02194b87bbc796c4428c946d"
	const want = "Bash0a65b6a5e02194b87bbc796c4428c946d"

	res := renderAnthropic(Result{Parts: []Part{{Kind: ToolCall, ID: raw, Name: "Bash"}}}, "m")
	if !strings.Contains(string(res), `"id":"`+want+`"`) {
		t.Fatalf("non-streaming kept raw id: %s", res)
	}

	rec := httptest.NewRecorder()
	e := &anthropicEncoder{w: newSSEWriter(rec), model: "m"}
	e.event(Event{Kind: KStart, MsgID: "m1", Model: "m"})
	e.event(Event{Kind: KToolStart, ID: raw, Name: "Bash"})
	e.event(Event{Kind: KToolArgs, Text: `{"command":"date"}`})
	e.finish()
	if !strings.Contains(rec.Body.String(), `"id":"`+want+`"`) {
		t.Fatalf("streaming kept raw id: %s", rec.Body.String())
	}

	res = renderAnthropic(Result{Parts: []Part{{Kind: ToolCall, ID: "call_abc-123_X", Name: "Bash"}}}, "m")
	if !strings.Contains(string(res), `"id":"call_abc-123_X"`) {
		t.Fatalf("valid id rewritten: %s", res)
	}

	res = renderAnthropic(Result{Parts: []Part{{Kind: ToolCall, ID: ":#", Name: "Bash"}}}, "m")
	var out struct {
		Content []struct {
			ID string `json:"id"`
		} `json:"content"`
	}
	if err := json.Unmarshal(res, &out); err != nil || len(out.Content) != 1 || !strings.HasPrefix(out.Content[0].ID, "toolu_") {
		t.Fatalf("all-invalid id not replaced: %s", res)
	}
}
