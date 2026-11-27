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

// cursorCallID is a call id of the kind IotXws97689's Copilot turned away
// at 87 characters: Cursor's call-<uuid>-<n>__fc_<uuid>_0 (#6941), one
// another provider made, carried in the history to Copilot.
const cursorCallID = "call-4f1c2a9e-8b7d-4c3e-9a1f-2d6b5e8c7a10-12__fc_9e8d7c6b-5a4f-4e3d-8c2b-1a0f9e8d7c6b_0"

// callIDChecker plays an upstream behind OpenAI's schema that checks a
// call's id at 64 characters, word for word as IotXws97689's Copilot
// refused it: on /responses each input item's call_id, on
// /chat/completions each tool call's id and each tool message's
// tool_call_id (#6941's relay words those as input[n].call_id too, as it
// checks them through Responses). It answers with a text, or with a call
// of its own when call is set.
type callIDChecker struct {
	mu    sync.Mutex
	call  string
	asked [][]string // each request's call ids, in order
}

func (u *callIDChecker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var q struct {
		Input []struct {
			CallID string `json:"call_id"`
		} `json:"input"`
		Messages []struct {
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	json.Unmarshal(body, &q)
	var ids []string
	for _, it := range q.Input {
		if it.CallID != "" {
			ids = append(ids, it.CallID)
		}
	}
	for _, m := range q.Messages {
		for _, c := range m.ToolCalls {
			ids = append(ids, c.ID)
		}
		if m.ToolCallID != "" {
			ids = append(ids, m.ToolCallID)
		}
	}
	u.mu.Lock()
	u.asked = append(u.asked, ids)
	call := u.call
	u.mu.Unlock()
	for i, id := range ids {
		if len(id) > 64 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Invalid 'input[`+mustString(int64(i))+`].call_id': string too long. Expected a string with maximum length 64, but got a string with length `+mustString(int64(len(id)))+` instead.","code":"invalid_request_body"}}`)
			return
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if strings.HasSuffix(r.URL.Path, "/chat/completions") {
		io.WriteString(w, sse(
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"DONE"}}]}`,
			`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
			`data: [DONE]`))
		return
	}
	item := `{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"DONE"}]}`
	if call != "" {
		item = `{"type":"function_call","id":"fc_1","call_id":"` + call + `","name":"Bash","arguments":"{\"command\":\"ls\"}","status":"completed"}`
	}
	io.WriteString(w, sse(
		`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
		`data: {"type":"response.output_item.added","output_index":0,"item":`+item+`}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":`+item+`}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[`+item+`],"usage":{"input_tokens":5,"output_tokens":1}}}`))
}

func (u *callIDChecker) sent() [][]string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([][]string(nil), u.asked...)
}

// copilotAt signs in a fake Copilot whose /responses is up, serving
// gpt-6.1 on /responses alone.
func copilotAt(t *testing.T, up http.Handler) {
	t.Helper()
	copilotAPIFake(t, map[string]string{"gpt-6.1": `["/responses"]`}, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			io.WriteString(w, `{"data":[]}`)
		case "/responses":
			up.ServeHTTP(w, r)
		default:
			w.WriteHeader(404)
		}
	})
}

// checkPairBound fails unless ids are a call and its output, both
// cursorCallID bound to 64 characters at most, the same way.
func checkPairBound(t *testing.T, ids []string) {
	t.Helper()
	if len(ids) != 2 {
		t.Fatalf("call ids sent: %q, want a call and its output", ids)
	}
	if ids[0] != ids[1] {
		t.Errorf("the call went as %q and its output as %q: no longer a pair", ids[0], ids[1])
	}
	if ids[0] != provider.BoundCallID(cursorCallID) || len(ids[0]) > 64 {
		t.Errorf("call id sent %q (%d), want %q", ids[0], len(ids[0]), provider.BoundCallID(cursorCallID))
	}
}

// IotXws97689 on X: Claude Code Desktop routed to a GPT model on GitHub
// Copilot, whose history held a tool call another provider had made with
// an 87-character id. Claude Code sends Messages, magpie built Copilot's
// /responses with the id as it came, and Copilot turned every turn away:
// "Invalid 'input[157].call_id': string too long. Expected a string with
// maximum length 64, but got a string with length 87 instead." The call
// and its result now go bound the same way, the same on every turn, and a
// call Copilot makes in its reply reaches Claude Code under Copilot's id.
func TestCopilotTakesClaudeCodesOverlongCallID(t *testing.T) {
	up := &callIDChecker{call: "call_copilot_next"}
	copilotAt(t, up)
	ask := `{"model":"copilot/gpt-6.1","max_tokens":1024,"stream":true,"messages":[
		{"role":"user","content":"list the files"},
		{"role":"assistant","content":[{"type":"text","text":"Listing."},{"type":"tool_use","id":"` + cursorCallID + `","name":"Bash","input":{"command":"ls"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + cursorCallID + `","content":"main.go"},{"type":"text","text":"now again"}]}]}`
	s := New()
	for turn := range 2 {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(ask)))
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "string too long") {
			t.Fatalf("turn %d: %d %s", turn, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"id":"call_copilot_next"`) {
			t.Errorf("turn %d: Copilot's call reached Claude Code under another id: %s", turn, rec.Body.String())
		}
	}
	asked := up.sent()
	if len(asked) != 2 {
		t.Fatalf("asked Copilot %d times, want once a turn", len(asked))
	}
	for _, ids := range asked {
		checkPairBound(t, ids)
	}
}

// A Chat client's history goes to a Responses upstream the same way, one
// magpie can't tell checks ids too: the body is magpie's own writing, so
// it goes bound at once, not after a refusal.
func TestChatClientsOverlongCallIDToResponsesBound(t *testing.T) {
	fresh(t)
	up := &callIDChecker{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt-6.1"}, Responses: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	ask := `{"model":"relay/gpt-6.1","stream":true,"messages":[
		{"role":"user","content":"list the files"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"` + cursorCallID + `","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\"ls\"}"}}]},
		{"role":"tool","tool_call_id":"` + cursorCallID + `","content":"main.go"}]}`
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(ask)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "DONE") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	asked := up.sent()
	if len(asked) != 1 {
		t.Fatalf("asked the relay %d times, want once", len(asked))
	}
	checkPairBound(t, asked[0])
}

// runyuxu (#6941): a Responses request translated onto a Chat upstream
// that checks a call's id at 64, its history holding Cursor's 87: the
// tool call and the tool message answering it go bound the same way.
func TestResponsesOverlongCallIDToChatBound(t *testing.T) {
	fresh(t)
	up := &callIDChecker{}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	if err := provider.Save(provider.Provider{ID: "tvlk", Name: "TVLK", Key: "k", Models: []string{"gpt-6.1-sol"}, Chat: srv.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	ask := `{"model":"tvlk/gpt-6.1-sol","stream":true,"store":false,"input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"list the files"}]},
		{"type":"function_call","call_id":"` + cursorCallID + `","name":"shell","arguments":"{\"cmd\":\"ls\"}"},
		{"type":"function_call_output","call_id":"` + cursorCallID + `","output":"main.go"}]}`
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(ask)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "DONE") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	asked := up.sent()
	if len(asked) != 1 {
		t.Fatalf("asked the upstream %d times, want once", len(asked))
	}
	checkPairBound(t, asked[0])
}

// A Responses client's own request relayed to an upstream magpie can't
// tell checks call ids goes byte for byte, until the upstream refuses an
// overlong call_id: asked once more with call ids bound, and from then on
// asked that way at once. An upstream known to check them (Copilot) is
// sent them bound from the first ask.
func TestRelayRefusingOverlongCallIDAskedAgain(t *testing.T) {
	ask := `{"model":"%s","stream":true,"store":false,"input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"list the files"}]},
		{"type":"function_call","call_id":"` + cursorCallID + `","name":"shell","arguments":"{\"cmd\":\"ls\"}"},
		{"type":"function_call_output","call_id":"` + cursorCallID + `","output":"main.go"}]}`
	t.Run("relay", func(t *testing.T) {
		fresh(t)
		up := &callIDChecker{}
		srv := httptest.NewServer(up)
		t.Cleanup(srv.Close)
		if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Models: []string{"gpt-6.1"}, Responses: srv.URL + "/v1"}); err != nil {
			t.Fatal(err)
		}
		s := New()
		for turn := range 2 {
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(strings.Replace(ask, "%s", "relay/gpt-6.1", 1))))
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "DONE") {
				t.Fatalf("turn %d: %d %s", turn, rec.Code, rec.Body.String())
			}
		}
		asked := up.sent()
		if len(asked) != 3 {
			t.Fatalf("asked the relay %d times, want the refusal, the ask again and the next turn", len(asked))
		}
		if asked[0][0] != cursorCallID {
			t.Errorf("the first ask went as %q, not as the client sent it", asked[0][0])
		}
		checkPairBound(t, asked[1])
		checkPairBound(t, asked[2])
	})
	t.Run("copilot", func(t *testing.T) {
		up := &callIDChecker{}
		copilotAt(t, up)
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(strings.Replace(ask, "%s", "copilot/gpt-6.1", 1))))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "DONE") {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		asked := up.sent()
		if len(asked) != 1 {
			t.Fatalf("asked Copilot %d times, want once", len(asked))
		}
		checkPairBound(t, asked[0])
	})
}
