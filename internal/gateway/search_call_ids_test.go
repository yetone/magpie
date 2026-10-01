package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A search a translated vendor's model made goes back to Codex as a
// tool_search_call with a tsc_ id, the one kind OpenAI takes for it.
func TestToolSearchCallHasTscID(t *testing.T) {
	req := &Request{Model: "m", Namespaced: map[string]nsTool{"tool_search": {Search: true}}}
	rec := httptest.NewRecorder()
	enc := encoder(provider.Responses, newSSEWriter(rec), req)
	enc.event(Event{Kind: KToolStart, ID: "s2", Name: "tool_search"})
	enc.event(Event{Kind: KToolArgs, Text: `{"query":"calendar"}`})
	enc.event(Event{Kind: KToolStart, ID: "c1", Name: "shell"})
	enc.finish()
	ids := map[string]string{}
	for _, ev := range events(rec.Body.String()) {
		if ev["type"] == "response.output_item.done" {
			it := ev["item"].(map[string]any)
			ids[it["type"].(string)], _ = it["id"].(string)
		}
	}
	if !strings.HasPrefix(ids["tool_search_call"], "tsc_") || !strings.HasPrefix(ids["function_call"], "fc_") {
		t.Fatalf("streamed ids = %v", ids)
	}

	res := Result{Parts: []Part{{Kind: ToolCall, ID: "s3", Name: "tool_search", Args: json.RawMessage(`{"query":"mail"}`)}}}
	var out struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(render(provider.Responses, res, req), &out); err != nil {
		t.Fatal(err)
	}
	if id, _ := out.Output[0]["id"].(string); len(out.Output) != 1 || !strings.HasPrefix(id, "tsc_") {
		t.Fatalf("rendered = %v", out.Output)
	}
}

// A conversation that searched on another vendor's model before (Lullaby
// on Discord: "Invalid 'input[98].id': 'fc_18da0e90a0fc07ee0000005f'.
// Expected an ID that begins with 'tsc'", Codex's compaction failing with
// it) goes on to OpenAI with that search's id a tsc_ one, on a turn and on
// a compaction; OpenAI's own ids and everything else go as they came.
func TestCodexOwnModelSearchCallIDs(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	var got []string
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, string(b))
		var q struct {
			Input []map[string]any `json:"input"`
		}
		json.Unmarshal(b, &q)
		for i, it := range q.Input {
			if id, _ := it["id"].(string); it["type"] == "tool_search_call" && id != "" && !strings.HasPrefix(id, "tsc") {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":{"message":"Invalid 'input[`+string(rune('0'+i))+`].id': '`+id+`'. Expected an ID that begins with 'tsc'.","type":"invalid_request_error","param":"input[`+string(rune('0'+i))+`].id","code":"invalid_value"}}`)
				return
			}
		}
		w.Header()["Content-Type"] = nil
		io.WriteString(w, sse(`data: {"type":"response.completed","response":{"id":"r1"}}`))
	})
	input := `[{"type":"message","role":"user","content":[{"type":"input_text","text":"find mail"}]},
	  {"type":"tool_search_call","id":"fc_18da0e90a0fc07ee0000005f","call_id":"s1","status":"completed","execution":"client","arguments":{"query":"mail"}},
	  {"type":"tool_search_output","call_id":"s1","status":"completed","execution":"client","tools":[]},
	  {"type":"tool_search_call","id":"tsc_openai","call_id":"s2","status":"completed","execution":"client","arguments":{"query":"cal"}},
	  {"type":"tool_search_output","call_id":"s2","status":"completed","execution":"client","tools":[]}`
	for _, path := range []string{"/responses", "/responses/compact"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", CodexPath+path, strings.NewReader(`{"model":"gpt-5.5","stream":true,"store":false,"input":`+input+`]}`))
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		req.Header.Set("chatgpt-account-id", "acct-1")
		New().Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		last := got[len(got)-1]
		if !strings.Contains(last, `"tsc_18da0e90a0fc07ee0000005f"`) || !strings.Contains(last, `"tsc_openai"`) || !strings.Contains(last, "find mail") {
			t.Errorf("%s upstream: %s", path, last)
		}
	}
}

// Nothing to fix leaves the request byte for byte.
func TestSearchCallIDsUntouched(t *testing.T) {
	for _, b := range []string{
		`{"model":"gpt-5.5","input":[{"type":"function_call","id":"fc_1","call_id":"c","name":"shell","arguments":"{}"}]}`,
		`{"model":"gpt-5.5",  "input":[{"type":"tool_search_call","id":"tsc_1","call_id":"s","arguments":{}},{"type":"tool_search_call","call_id":"t","arguments":{}}]}`,
	} {
		if got := searchCallIDs([]byte(b)); string(got) != b {
			t.Errorf("%s -> %s", b, got)
		}
	}
}
