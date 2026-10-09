package gateway

import (
	"encoding/json"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// inputOf is a Responses request's input items, as maps.
func inputOf(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var q struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(body, &q); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return q.Input
}

// #1341: Volcengine's coding plan checks a relayed /responses' tool
// exchange on the chat form it turns it into, and answers a call no result
// answers with 400 "An assistant message with 'tool_calls' must be followed
// by tool messages responding to each 'tool_call_id'" — the same request
// with the result went through (the reporter's own experiment). The Chat
// path has mended this since #1275 (pairToolMessages); the Responses relay
// didn't. Thanks to ggbdpq for the first version of this mend.
func TestPairToolItemsAnswersALoneCall(t *testing.T) {
	const interrupted = "[The result of this tool call is unavailable: the turn was interrupted.]"
	// a lone call gets a synthetic result right after itself
	in := inputOf(t, pairToolItems([]byte(`{"model":"m","input":[`+
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},`+
		`{"type":"function_call","call_id":"call_lone","name":"echo","arguments":"{}"},`+
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"again"}]}]}`)))
	if len(in) != 4 || in[2]["type"] != "function_call_output" || in[2]["call_id"] != "call_lone" || in[2]["output"] != interrupted {
		t.Fatalf("no synthetic result after the lone call: %v", in)
	}

	// a lone custom call gets the custom kind of result
	in = inputOf(t, pairToolItems([]byte(`{"model":"m","input":[{"type":"custom_tool_call","call_id":"call_c","name":"apply_patch","input":"x"}]}`)))
	if len(in) != 2 || in[1]["type"] != "custom_tool_call_output" || in[1]["call_id"] != "call_c" {
		t.Fatalf("no synthetic custom result: %v", in)
	}

	// parallel calls, the first unanswered: its result goes after the
	// run's results, so no result sits between two calls of one turn
	in = inputOf(t, pairToolItems([]byte(`{"model":"m","input":[`+
		`{"type":"function_call","call_id":"c1","name":"echo","arguments":"{}"},`+
		`{"type":"function_call","call_id":"c2","name":"echo","arguments":"{}"},`+
		`{"type":"function_call_output","call_id":"c2","output":"second"},`+
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}]}`)))
	var order []string
	for _, it := range in {
		order = append(order, it["type"].(string)+":"+func() string { s, _ := it["call_id"].(string); return s }())
	}
	want := []string{"function_call:c1", "function_call:c2", "function_call_output:c2", "function_call_output:c1", "message:"}
	if len(order) != len(want) {
		t.Fatalf("items %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("items %v, want %v", order, want)
		}
	}

	// paired exchanges, parallel results out of order, a request with no
	// tool items, and a result naming a call an earlier request carried
	// (previous_response_id) go byte for byte as they came
	for _, keep := range []string{
		`{"model":"m","input":[{"type":"function_call","call_id":"c1","name":"echo","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`,
		`{"model":"m","input":[{"type":"function_call","call_id":"c1","name":"echo","arguments":"{}"},{"type":"function_call","call_id":"c2","name":"echo","arguments":"{}"},{"type":"function_call_output","call_id":"c2","output":"second"},{"type":"function_call_output","call_id":"c1","output":"first"}]}`,
		`{"model":"m","input":[{"type":"custom_tool_call","call_id":"c3","name":"apply_patch","input":"x"},{"type":"custom_tool_call_output","call_id":"c3","output":"done"}]}`,
		`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
		`{"model":"m","previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"call_gone","output":"late result"}]}`,
		`not json`, `{}`, `{"input":"hello"}`,
	} {
		if got := pairToolItems([]byte(keep)); string(got) != keep {
			t.Fatalf("rewritten:\n%s\nto\n%s", keep, got)
		}
	}

	// the caller's buffer is untouched
	original := []byte(`{"model":"m","input":[{"type":"function_call","call_id":"call_lone","name":"echo","arguments":"{}"}]}`)
	before := string(original)
	pairToolItems(original)
	if string(original) != before {
		t.Fatal("changed the caller's request buffer")
	}
}

// The relay sends a lone call with its result, through the whole gateway.
func TestResponsesRelayAnswersALoneCall(t *testing.T) {
	f := &fake{t: t, reply: sse(`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[]}}`)}
	setup(t, provider.Responses, f)
	body := `{"model":"fake/m1","stream":true,"input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"run it"}]},` +
		`{"type":"function_call","call_id":"call_lone","name":"echo","arguments":"{}"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"go on"}]}]}`
	if code, out := post(t, "/v1/responses", body); code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	if f.path != "/v1/responses" {
		t.Fatalf("not relayed as Responses: %s", f.path)
	}
	in := inputOf(t, f.got)
	if len(in) != 4 || in[2]["type"] != "function_call_output" || in[2]["call_id"] != "call_lone" {
		t.Fatalf("the lone call went unanswered: %s", f.got)
	}
}
