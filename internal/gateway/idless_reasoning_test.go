package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// idlessReasoningMsg is the reporter's error, word for word (yyhhyyyyyy on
// X): a DeepSeek channel's Responses API refusing the " " reasoning item
// magpie put first in a step that had none.
const idlessReasoningMsg = `Input tag 'reasoning' found using 'type' does not match any of the expected tags: 'message', 'tool_result'`

// idlessReasoning422s are that message in the envelopes a channel answers
// in: pydantic's own (FastAPI's detail list) and OpenAI's error object.
var idlessReasoning422s = []string{
	`{"detail":[{"type":"union_tag_invalid","loc":["body","input",3],"msg":"` + idlessReasoningMsg + `","input":{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":" "}]},"ctx":{"discriminator":"'type'","tag":"reasoning","expected_tags":"'message', 'tool_result'"}}]}`,
	`{"error":{"message":"` + idlessReasoningMsg + `","type":"invalid_request_error"}}`,
}

// idlessChannel is that channel: a reasoning item with an id (one it gave)
// is taken, one without is turned away with 422.
type idlessChannel struct {
	mu     sync.Mutex
	refuse string
	inputs [][]map[string]any
}

func (u *idlessChannel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var q struct {
		Input []map[string]any `json:"input"`
	}
	json.Unmarshal(body, &q)
	u.mu.Lock()
	u.inputs = append(u.inputs, q.Input)
	u.mu.Unlock()
	for _, it := range q.Input {
		if it["type"] == "reasoning" && it["id"] == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, u.refuse)
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(
		`data: {"type":"response.created","response":{"id":"resp_ch","status":"in_progress"}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_ch","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`))
}

func (u *idlessChannel) sent() (n int, last []map[string]any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.inputs) == 0 {
		return 0, nil
	}
	return len(u.inputs), u.inputs[len(u.inputs)-1]
}

// TestIdlessReasoningRefusedByChannel (yyhhyyyyyy on X): Codex continuing
// on a DeepSeek channel whose reply had no reasoning sends a step with
// nothing to replay, and magpie leads it with a " " reasoning item without
// an id (reasonFirst), which DeepSeek's own API wants (#1822). The channel
// turned the whole request away with 422 and the agent got it. It is asked
// once more without the item, and not sent one again; its own reasoning,
// with an id, still goes.
func TestIdlessReasoningRefusedByChannel(t *testing.T) {
	for _, refuse := range idlessReasoning422s {
		fresh(t)
		up := &idlessChannel{refuse: refuse}
		srv := httptest.NewServer(up)
		defer srv.Close()
		if err := provider.Save(provider.Provider{ID: "ch", Name: "Channel", Key: "k", Models: []string{"deepseek-v4-pro"}, Responses: srv.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		s := New()
		input := `{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the login bug"}]},
		  {"type":"reasoning","id":"rs_ch1","summary":[],"content":[{"type":"reasoning_text","text":"Look first."}]},
		  {"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"},
		  {"type":"function_call_output","call_id":"c1","output":"main.go"},
		  {"type":"function_call","call_id":"c2","name":"shell","arguments":"{}"},
		  {"type":"function_call_output","call_id":"c2","output":"ok"}`
		req := `{"model":"ch/deepseek-v4-pro","stream":true,"store":false,"tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],"input":[` + input + `]}`
		code, b := postTo(t, s, "/v1/responses", req)
		if code != 200 || strings.Contains(b, "expected tags") {
			t.Fatalf("%.40s: %d %s", refuse, code, b)
		}
		n, last := up.sent()
		if n != 2 {
			t.Fatalf("asked %d times, want the refusal and once more", n)
		}
		if got := stepShape(last); got != `message:user,reasoning="Look first.",function_call,function_call_output,function_call,function_call_output` {
			t.Fatalf("sent %s", got)
		}
		if code, b := postTo(t, s, "/v1/responses", req); code != 200 {
			t.Fatalf("again: %d %s", code, b)
		}
		if n, _ := up.sent(); n != 3 {
			t.Fatalf("asked %d times in all, want the second request taken at once", n)
		}
	}
}

// Translated to the channel (an agent on Chat), a turn's reasoning goes
// as an item without an id, and the " " before a step without one too:
// the channel's 422 has them left out, and the member asked again.
func TestTranslatedIdlessReasoningRefusedByChannel(t *testing.T) {
	fresh(t)
	up := &idlessChannel{refuse: idlessReasoning422s[0]}
	srv := httptest.NewServer(up)
	defer srv.Close()
	if err := provider.Save(provider.Provider{ID: "ch", Name: "Channel", Key: "k", Models: []string{"deepseek-v4-pro"}, Responses: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	req := `{"model":"ch/deepseek-v4-pro","stream":false,"tools":[{"type":"function","function":{"name":"shell","parameters":{"type":"object"}}}],"messages":[
	  {"role":"user","content":"fix the login bug"},
	  {"role":"assistant","content":"","reasoning_content":"Look first.","tool_calls":[{"id":"c1","type":"function","function":{"name":"shell","arguments":"{}"}}]},
	  {"role":"tool","tool_call_id":"c1","content":"main.go"},
	  {"role":"assistant","content":"","tool_calls":[{"id":"c2","type":"function","function":{"name":"shell","arguments":"{}"}}]},
	  {"role":"tool","tool_call_id":"c2","content":"ok"}]}`
	code, b := postTo(t, s, "/v1/chat/completions", req)
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	n, last := up.sent()
	if n != 2 {
		t.Fatalf("asked %d times, want the refusal and once more", n)
	}
	for _, it := range last {
		if it["type"] == "reasoning" {
			t.Fatalf("sent %s", stepShape(last))
		}
	}
	if code, b := postTo(t, s, "/v1/chat/completions", req); code != 200 {
		t.Fatalf("again: %d %s", code, b)
	}
	if n, _ := up.sent(); n != 3 {
		t.Fatalf("asked %d times in all, want the second request taken at once", n)
	}
}

// What DeepSeek's own API wants is unchanged: an upstream that never
// refused it still gets the " " a step leads with (#1822).
func TestIdlessReasoningKeptForDeepSeek(t *testing.T) {
	fresh(t)
	up := &deepseekResponses{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Key: "k", Models: []string{"deepseek-flash"}, Responses: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	code, b := post(t, "/v1/responses", `{"model":"deepseek/deepseek-flash","stream":true,"store":false,"input":[
	  {"type":"message","role":"user","content":[{"type":"input_text","text":"ls"}]},
	  {"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"},
	  {"type":"function_call_output","call_id":"c1","output":"main.go"}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if got := stepShape(up.last()); got != `message:user,reasoning=" ",function_call,function_call_output` {
		t.Fatalf("sent %s", got)
	}
}

// A channel that turns such an item away is a shape it can't read, not a
// fault of the account or of the request: a group's next member, DeepSeek's
// own API, may take it as it is, and the member doesn't rest.
func TestGroupFailsOverIdlessReasoningRefusal(t *testing.T) {
	for _, refuse := range idlessReasoning422s {
		if !retryable(422, []byte(refuse)) || !shapeRefused(422, []byte(refuse)) {
			t.Errorf("not handed on: %s", refuse)
		}
		fresh(t)
		a := &scripted{replies: []reply{{422, "", refuse}}}
		b := &scripted{replies: []reply{{200, "", chatOK}}}
		scriptedOn(t, "a", provider.Chat, a)
		scriptedOn(t, "b", provider.Chat, b)
		if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
			t.Fatal(err)
		}
		s := New()
		code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 || !strings.Contains(body, "hello") || a.n != 1 || b.n != 1 {
			t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.n)
		}
		r := s.trace.routes[len(s.trace.routes)-1]
		if len(r.Tries) != 2 || r.Tries[0].Fail != failShape || r.Tries[0].Rest != nil {
			t.Fatalf("tries %+v", r.Tries)
		}
	}
}

func TestWithoutIDlessReasoning(t *testing.T) {
	in := `{"model":"m","input":[{"type":"message","role":"user","content":"hi"},{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":" "}]},{"type":"reasoning","id":"rs_1","summary":[]},{"type":"function_call","call_id":"c","name":"x","arguments":"{}"}]}`
	var q struct {
		Input []map[string]any `json:"input"`
	}
	json.Unmarshal(withoutIDlessReasoning([]byte(in)), &q)
	if got := stepShape(q.Input); got != "message:user,reasoning,function_call" || q.Input[1]["id"] != "rs_1" {
		t.Fatalf("%s", got)
	}
	for _, b := range []string{`{"model":"m","input":"hi"}`, `{"model":"m","input":[{"type":"reasoning","id":"rs_1"}]}`} {
		if got := string(withoutIDlessReasoning([]byte(b))); got != b {
			t.Errorf("%s became %s", b, got)
		}
	}
}
