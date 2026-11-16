package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// qwenInputRefusal is a local Qwen server's Responses API turning away an
// input item it has no type for, as cyberestro on X got it from
// http://127.0.0.1:30010/v1 for a Codex subagent's agent_message:
// "qwen: Unsupported input item: agent_message".
func qwenInputRefusal(kind string) string {
	return `{"error":{"message":"Unsupported input item: ` + kind + `","type":"invalid_request_error","code":400}}`
}

// basicItem is whether a Responses server that knows OpenAI's first
// input items alone takes it: messages, function calls, their outputs and
// reasoning.
func basicItem(kind string) bool {
	switch kind {
	case "", "message", "function_call", "function_call_output", "reasoning":
		return true
	}
	return false
}

// strictInputUpstream is such a server: an item of another kind is
// refused, in qwen's words; anything else is answered "ok".
type strictInputUpstream struct {
	mu    sync.Mutex
	asked []map[string]any
}

func (u *strictInputUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var q map[string]any
	json.Unmarshal(b, &q)
	u.mu.Lock()
	u.asked = append(u.asked, q)
	u.mu.Unlock()
	items, _ := q["input"].([]any)
	for _, it := range items {
		if m, _ := it.(map[string]any); m != nil {
			if k, _ := m["type"].(string); !basicItem(k) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				io.WriteString(w, qwenInputRefusal(k))
				return
			}
		}
	}
	answerOK(w, r, q["stream"] == true)
}

// answerOK answers "ok", streamed or whole.
func answerOK(w http.ResponseWriter, r *http.Request, stream bool) {
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		msg := `{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"ok"}]}`
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","status":"in_progress","output":[]}}`,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[]}}`,
			`data: {"type":"response.output_text.delta","output_index":0,"item_id":"m1","content_index":0,"delta":"ok"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":`+msg+`}`,
			`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[`+msg+`],"usage":{"input_tokens":9,"output_tokens":1}}}`))
		return
	}
	answer(w, r)
}

// subagentTurn is a Codex subagent's turn under MultiAgentV2: its task
// came as an agent_message (the item codex-rs hands a subagent its
// NEW_TASK in, as #5259's and plugins#61's requests carry it), and it has
// called a tool since.
const subagentTurn = `{"model":%q,"stream":%t,"store":false,"input":[
  {"type":"message","role":"developer","content":[{"type":"input_text","text":"be brief"}]},
  {"type":"agent_message","author":"/root","recipient":"/root/research","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nPayload:\nCompare Vault secret rotation with rollout restart timing."}]},
  {"type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
  {"type":"function_call_output","call_id":"call_1","output":"notes.md"},
  {"type":"agent_message","author":"/root","recipient":"/root/research","content":[{"type":"input_text","text":"Message Type: MESSAGE\nPayload:\nKeep the observed event order."}]}],
  "tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}]}`

// cyberestro on X: a Codex subagent on a local Qwen server's Responses
// API was refused 400 "Unsupported input item: agent_message", the item
// its task comes in, and magpie gave the 400 back, every turn. The turn
// is now asked again translated on the same API, the agent_message as the
// user's turn it is, and the next turn goes translated at once; a server
// that takes the item still gets it as sent.
func TestUnsupportedInputItemTranslated(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream %t", stream), func(t *testing.T) {
			fresh(t)
			strict := &strictInputUpstream{}
			qs := httptest.NewServer(strict)
			t.Cleanup(qs.Close)
			var taking []map[string]any
			var mu sync.Mutex
			lax := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				var q map[string]any
				json.Unmarshal(b, &q)
				mu.Lock()
				taking = append(taking, q)
				mu.Unlock()
				answerOK(w, r, q["stream"] == true)
			}))
			t.Cleanup(lax.Close)
			for _, p := range []provider.Provider{
				{ID: "qwen", Name: "qwen", Key: "k", Models: []string{"qwen"}, Responses: qs.URL + "/v1"},
				{ID: "lax", Name: "Lax", Key: "k", Models: []string{"m1"}, Responses: lax.URL + "/v1"},
			} {
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
			}
			s := New()
			ask := func(model string) *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(fmt.Sprintf(subagentTurn, model, stream))))
				return rec
			}
			rec := ask("qwen/qwen")
			if rec.Code != 200 {
				t.Fatalf("qwen: %d %s", rec.Code, rec.Body)
			}
			if len(strict.asked) != 2 {
				t.Fatalf("qwen asked %d times", len(strict.asked))
			}
			again, _ := json.Marshal(strict.asked[1]["input"])
			for _, want := range []string{"Compare Vault secret rotation", "Keep the observed event order", "exec_command", "notes.md"} {
				if !strings.Contains(string(again), want) {
					t.Errorf("asked again without %q: %s", want, again)
				}
			}
			if !strings.Contains(rec.Body.String(), "ok") {
				t.Errorf("Codex got %s", rec.Body)
			}

			// the next turn goes translated at once
			if rec = ask("qwen/qwen"); rec.Code != 200 || len(strict.asked) != 3 {
				t.Fatalf("next turn: %d, asked %d times: %s", rec.Code, len(strict.asked), rec.Body)
			}

			// a server that takes the item gets it as sent
			if rec = ask("lax/m1"); rec.Code != 200 || len(taking) != 1 {
				t.Fatalf("lax: %d, asked %d times: %s", rec.Code, len(taking), rec.Body)
			}
			if b, _ := json.Marshal(taking[0]["input"]); !strings.Contains(string(b), `"agent_message"`) {
				t.Errorf("lax got %s", b)
			}
		})
	}
}

// The refusal's wordings that name an input item the server has no type
// for are read as one; a refusal of anything else is not.
func TestInputItemRefusalWordings(t *testing.T) {
	for _, b := range []string{
		qwenInputRefusal("agent_message"),
		"Unsupported input item: custom_tool_call",
		commandCodeInputRefusal,
		`{"error":{"message":"unknown item type"}}`,
	} {
		if !inputKindWords.MatchString(b) {
			t.Errorf("not read as an input item refusal: %s", b)
		}
	}
	for _, b := range []string{
		`{"error":{"message":"Unsupported parameter: temperature"}}`,
		`{"error":{"message":"Invalid API key"}}`,
		deepSeekCustomRefusal,
	} {
		if inputKindWords.MatchString(b) {
			t.Errorf("read as an input item refusal: %s", b)
		}
	}
}

// cyberestro's compaction: the summary request of a subagent's
// conversation, refused for its agent_message in qwen's words, is asked
// again with the conversation as text, which the server takes, and the
// subagent's task is in it.
func TestCodexCompactQwenInputItemAsksAgainAsText(t *testing.T) {
	f := &fake{t: t}
	f.refuse = func(b []byte) (int, string) {
		f.reply = wholeSummary(provider.Responses)
		var q struct {
			Input []struct {
				Type string `json:"type"`
			} `json:"input"`
		}
		json.Unmarshal(b, &q)
		for _, it := range q.Input {
			if !basicItem(it.Type) {
				return 400, qwenInputRefusal(it.Type)
			}
		}
		return 0, ""
	}
	setup(t, provider.Responses, f)
	code, body := codexPost(t, compactSubagentRequest)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if got := compactedText(t, body); got != "SUMMARY" {
		t.Fatalf("summary %q, want the model's", got)
	}
	if !strings.Contains(string(f.got), "Compare Vault secret rotation") {
		t.Errorf("the summary was asked without the task: %s", f.got)
	}
}
