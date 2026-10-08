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

// relayUpstream plays a Responses relay in front of OpenAI as #1044's
// reporter captured it: a request whose input has reasoning with nothing
// sealed in it is turned away with the relay's own 400, word for word,
// naming only the input; without such reasoning the turn is answered. With
// refuse unset it takes the reasoning, as a relay for a DeepSeek model does
// (#388). With status set, every request gets that error instead.
type relayUpstream struct {
	mu     sync.Mutex
	refuse bool
	status int
	errMsg string
	inputs [][]map[string]any
}

func (u *relayUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var q struct {
		Input []map[string]any `json:"input"`
	}
	json.Unmarshal(body, &q)
	u.mu.Lock()
	u.inputs = append(u.inputs, q.Input)
	u.mu.Unlock()
	if u.status != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(u.status)
		io.WriteString(w, u.errMsg)
		return
	}
	for _, it := range q.Input {
		enc, _ := it["encrypted_content"].(string)
		if u.refuse && it["type"] == "reasoning" && enc == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"bad response status code 400 (request id: 20261007000605227481978n1OdHdSC)","type":"invalid_request_error","param":"input","code":null}}`)
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(
		`data: {"type":"response.created","response":{"id":"resp_relay","status":"in_progress"}}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"SUMMARY"}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_relay","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}}`))
}

func (u *relayUpstream) asked() [][]map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][]map[string]any(nil), u.inputs...)
}

// bareConversation is the shape of the reporter's old conversation: messages,
// function calls, Codex's apply_patch as custom tool calls, and reasoning
// items with nothing sealed in them (one with a summary, as magpie gives
// Codex in a translated reply; one with an id alone), beside one sealed.
const bareConversation = `{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>"}]},
  {"type":"message","role":"user","content":[{"type":"input_text","text":"fix the login bug"}]},
  {"type":"reasoning","id":"rs_18dbe693b3dce4800000003c","summary":[{"type":"summary_text","text":"Looking at main.go"}]},
  {"type":"function_call","id":"fc_1","call_id":"call_1","name":"shell","arguments":"{\"cmd\":\"ls\"}"},
  {"type":"function_call_output","call_id":"call_1","output":"main.go"},
  {"type":"reasoning","id":"rs_18dbe693b3dce4800000003d","summary":[]},
  {"type":"custom_tool_call","id":"ctc_1","status":"completed","call_id":"call_2","name":"apply_patch","input":"*** Begin Patch\n*** Update File: main.go\n@@\n-old\n+new\n*** End Patch"},
  {"type":"custom_tool_call_output","call_id":"call_2","output":"Success. Updated the following files:\nM main.go"},
  {"type":"reasoning","id":"rs_sealed","summary":[],"encrypted_content":"gAAAA-relay-sealed"},
  {"type":"message","role":"assistant","content":[{"type":"output_text","text":"I fixed main.go"}]}`

// bareRequests are a compaction (Codex's trigger, no tools) and a turn
// relayed as it is, each over bareConversation, for model.
func bareRequests(model string) []struct{ name, body string } {
	return []struct{ name, body string }{
		{"compaction", `{"model":"` + model + `","stream":true,"store":false,"include":["reasoning.encrypted_content"],"tools":[{"type":"function","name":"shell","parameters":{"type":"object"}}],
		  "input":[` + bareConversation + `,{"type":"compaction_trigger"}]}`},
		{"a turn relayed as it is", `{"model":"` + model + `","stream":true,"store":false,"include":["reasoning.encrypted_content"],
		  "input":[` + bareConversation + `,{"type":"message","role":"user","content":[{"type":"input_text","text":"now add a test"}]}]}`},
	}
}

func saveRelay(t *testing.T, u *relayUpstream) {
	t.Helper()
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt-5.4"}, Responses: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

func codexPostTo(s *Server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func reasoningIDs(input []map[string]any) []string {
	var ids []string
	for _, it := range input {
		if it["type"] == "reasoning" {
			id, _ := it["id"].(string)
			ids = append(ids, id)
		}
	}
	return ids
}

func kinds(input []map[string]any) map[string]int {
	out := map[string]int{}
	for _, it := range input {
		k, _ := it["type"].(string)
		out[k]++
	}
	return out
}

// #1044: Codex on a Responses relay in front of OpenAI, its old
// conversation carrying reasoning with nothing sealed in it. The relay
// turned the compaction (and any turn relayed as it is) away with a 400
// naming only the input, and the reporter saw the same request answered
// once those items were left out. It is asked again without them, every
// other item kept, and that provider's model isn't sent them again.
func TestBareReasoningLeftOutWhereRefused(t *testing.T) {
	for _, tc := range bareRequests("relay/gpt-5.4") {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			u := &relayUpstream{refuse: true}
			saveRelay(t, u)
			s := New()
			rec := codexPostTo(s, tc.body)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "SUMMARY") && !strings.Contains(rec.Body.String(), "U1VNTUFSWQ") {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			got := u.asked()
			if len(got) != 2 {
				t.Fatalf("asked the relay %d times, want 2", len(got))
			}
			if ids := strings.Join(reasoningIDs(got[0]), ","); ids != "rs_18dbe693b3dce4800000003c,rs_18dbe693b3dce4800000003d,rs_sealed" {
				t.Errorf("first ask's reasoning: %s", ids)
			}
			if ids := strings.Join(reasoningIDs(got[1]), ","); ids != "rs_sealed" {
				t.Errorf("asked again with reasoning %s, want the sealed only", ids)
			}
			before, after := kinds(got[0]), kinds(got[1])
			for _, k := range []string{"message", "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output"} {
				if after[k] == 0 || after[k] != before[k] {
					t.Errorf("%s: %d asked again, %d first", k, after[k], before[k])
				}
			}

			// the next request goes without them at once
			rec = codexPostTo(s, tc.body)
			if got = u.asked(); rec.Code != 200 || len(got) != 3 || strings.Join(reasoningIDs(got[2]), ",") != "rs_sealed" {
				t.Fatalf("again: %d, %d asks, reasoning %v", rec.Code, len(got), reasoningIDs(got[len(got)-1]))
			}
		})
	}
}

// An upstream that takes reasoning with nothing sealed in it, as a relay
// for a DeepSeek model wants it back (#388), still gets it as Codex sent
// it, and is asked once.
func TestBareReasoningKeptWhereTaken(t *testing.T) {
	for _, tc := range bareRequests("relay/gpt-5.4") {
		t.Run(tc.name, func(t *testing.T) {
			fresh(t)
			u := &relayUpstream{}
			saveRelay(t, u)
			rec := codexPostTo(New(), tc.body)
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			got := u.asked()
			if len(got) != 1 {
				t.Fatalf("asked %d times", len(got))
			}
			if ids := strings.Join(reasoningIDs(got[0]), ","); ids != "rs_18dbe693b3dce4800000003c,rs_18dbe693b3dce4800000003d,rs_sealed" {
				t.Errorf("reasoning sent: %s", ids)
			}
			b, _ := json.Marshal(got[0])
			if !strings.Contains(string(b), "Looking at main.go") {
				t.Errorf("the summary didn't go: %s", b)
			}
		})
	}
}

// A 400 over something other than the input is the upstream's answer: the
// reasoning isn't given up for it, and the request isn't asked again.
func TestBareReasoningKeptOverAnotherRefusal(t *testing.T) {
	fresh(t)
	u := &relayUpstream{status: http.StatusBadRequest,
		errMsg: `{"error":{"message":"Invalid value for 'reasoning.effort'","type":"invalid_request_error","param":"reasoning.effort","code":null}}`}
	saveRelay(t, u)
	rec := codexPostTo(New(), bareRequests("relay/gpt-5.4")[1].body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if got := u.asked(); len(got) != 1 || len(reasoningIDs(got[0])) != 3 {
		t.Fatalf("asked %d times, reasoning %v", len(got), reasoningIDs(got[0]))
	}
}

func TestRefusesInput(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `{"error":{"message":"bad response status code 400 (request id: x)","type":"invalid_request_error","param":"input","code":null}}`, true},
		{400, `{"error":{"message":"Invalid 'input[3].id'","type":"invalid_request_error","param":"input[3].id"}}`, true},
		{400, `{"error":{"code":null,"message":"Item with id 'rs_1' not found.","param":null,"type":"invalid_request_error"}}`, true},
		{400, `{"error":{"message":"bad response status code 400","type":"invalid_request_error"}}`, false},
		{400, `{"error":{"message":"Invalid value for 'reasoning.effort'","param":"reasoning.effort"}}`, false},
		{400, `{"error":{"message":"bad","param":"inputs"}}`, false},
		{429, `{"error":{"param":"input"}}`, false},
	} {
		if got := refusesInput(tc.status, []byte(tc.body)); got != tc.want {
			t.Errorf("%d %s: %v", tc.status, tc.body, got)
		}
	}
}
